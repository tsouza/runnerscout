// Package provider implements isolated cloud adapters with durable ownership.
// AWS, Azure and GCP use native SDKs with isolated credential scopes.
package provider

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/tsouza/runnerscout/internal/lifecycle"
	"strings"
	"sync"
)

type Config struct {
	AccountID     string `json:"accountID,omitempty"`
	Subscription  string `json:"subscription,omitempty"`
	ResourceGroup string `json:"resourceGroup,omitempty"`
	SSHPublicKey  string `json:"sshPublicKey,omitempty"`
	Kind          string `json:"kind"`
	Project       string `json:"project,omitempty"`
	Profile       string `json:"profile,omitempty"`
	Subnet        string `json:"subnet"`
	SecurityGroup string `json:"securityGroup,omitempty"`
	Owner         string `json:"owner"`
}
type Command struct {
	AWS       *AWSSDK
	GCP       *GCPSDK
	Azure     *AzureSDK
	azureOnce sync.Once
	Config    Config
	Bootstrap func(context.Context, string) (string, error)
}

func (p *Command) Validate() error {
	if err := ValidateName(p.Config.Owner); err != nil {
		return err
	}
	if p.Config.Owner == "" || p.Config.Subnet == "" {
		return errors.New("owner and isolated subnet required")
	}
	if p.Config.Kind == "aws" && p.Config.SecurityGroup != "" && len(p.Config.AccountID) == 12 && strings.Trim(p.Config.AccountID, "0123456789") == "" {
		return nil
	}
	if p.Config.Kind == "azure" && p.Config.Subscription != "" && p.Config.ResourceGroup != "" && p.Config.SecurityGroup != "" && strings.HasPrefix(p.Config.SSHPublicKey, "ssh-") {
		return nil
	}
	if p.Config.Kind == "gcp" && p.Config.Project != "" {
		return nil
	}
	return errors.New("aws requires securityGroup and 12-digit accountID; gcp requires project; azure requires subscription, resourceGroup, securityGroup and SSH public key")
}
func Bootstrap(jit string) string {
	// cloud-init never prints the JIT token; the image already contains runner binaries.
	b64 := base64.StdEncoding.EncodeToString([]byte(jit))
	return "#!/bin/bash\nset -eu\numask 077\ninstall -d -m 0700 -o runner -g runner /run/runnerscout\nprintf '%s' '" + b64 + "' | base64 -d > /run/runnerscout/jit\nchown runner:runner /run/runnerscout/jit\ntrap 'rm -f /run/runnerscout/jit; shutdown -h now' EXIT\ncd /opt/actions-runner\nrunuser -u runner -- sh -c 'exec ./run.sh --jitconfig \"$(cat /run/runnerscout/jit)\"'\n"
}
func (p *Command) Create(ctx context.Context, a lifecycle.Allocation) (string, error) {
	receipt, err := p.CreateWithResources(ctx, a)
	return receipt.ResourceID, err
}

func (p *Command) CreateWithResources(ctx context.Context, a lifecycle.Allocation) (lifecycle.Creation, error) {
	if a.ResourceID != "" || len(a.Resources) > 0 {
		return lifecycle.Creation{}, errors.New("cannot create over recorded cloud resources")
	}
	if err := ValidateName(a.ID); err != nil {
		return lifecycle.Creation{}, err
	}
	if err := p.Validate(); err != nil {
		return lifecycle.Creation{}, err
	}
	if p.Bootstrap == nil {
		return lifecycle.Creation{}, lifecycle.ErrNoEffect
	}
	jit, err := p.Bootstrap(ctx, a.ID)
	if err != nil || jit == "" {
		return lifecycle.Creation{}, lifecycle.ErrNoEffect
	}
	script := Bootstrap(jit)
	switch p.Config.Kind {
	case "aws":
		return p.createAWS(ctx, a, script)
	case "azure":
		id, err := p.createAzure(ctx, a, script)
		return lifecycle.Creation{ResourceID: id}, err
	default:
		id, err := p.createGCP(ctx, a, script)
		return lifecycle.Creation{ResourceID: id}, err
	}
}
func (p *Command) Observe(ctx context.Context, a lifecycle.Allocation) (lifecycle.Observation, error) {
	if len(a.Resources) > 0 && p.Config.Kind != "aws" {
		return lifecycle.Observation{}, errors.New("provider cannot reconcile recorded cloud dependencies")
	}
	if err := ValidateName(a.ID); err != nil {
		return lifecycle.Observation{}, err
	}
	if err := p.Validate(); err != nil {
		return lifecycle.Observation{}, err
	}
	switch p.Config.Kind {
	case "aws":
		return p.observeAWS(ctx, a)
	case "azure":
		return p.observeAzure(ctx, a)
	default:
		return p.observeGCP(ctx, a)
	}
}

func (p *Command) ReconcileCreation(ctx context.Context, a lifecycle.Allocation) (lifecycle.Observation, error) {
	if a.Phase != lifecycle.Creating {
		return lifecycle.Observation{}, errors.New("creation reconciliation requires committed intent")
	}
	if p.Config.Kind != "azure" {
		return p.Observe(ctx, a)
	}
	if err := ValidateName(a.ID); err != nil {
		return lifecycle.Observation{}, err
	}
	if err := p.Validate(); err != nil {
		return lifecycle.Observation{}, err
	}
	if len(a.Resources) > 0 {
		return lifecycle.Observation{}, errors.New("provider cannot reconcile recorded cloud dependencies")
	}
	return p.reconcileAzureCreation(ctx, a)
}
func (p *Command) Delete(ctx context.Context, a lifecycle.Allocation) error {
	if err := ValidateName(a.ID); err != nil {
		return err
	}
	if err := p.Validate(); err != nil {
		return err
	}
	if p.Config.Kind == "aws" {
		return p.deleteAWS(ctx, a)
	}
	ob, err := p.Observe(ctx, a)
	if err != nil {
		return err
	}
	if !ob.Known {
		return errors.New("resource inventory unknown")
	}
	if !ob.Exists {
		return nil
	}
	if a.ResourceID != "" && a.ResourceID != ob.ResourceID {
		return errors.New("resource identity mismatch")
	}
	a.ResourceID = ob.ResourceID
	if p.Config.Kind == "azure" {
		return p.deleteAzure(ctx, a)
	}
	return p.deleteGCP(ctx, a)
}

// ValidateName limits names interpolated into provider filter/label grammars.
func ValidateName(s string) error {
	if len(s) < 1 || len(s) > 63 || strings.Trim(s, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" || s[0] < 'a' || s[0] > 'z' {
		return fmt.Errorf("invalid provider-safe name")
	}
	return nil
}
