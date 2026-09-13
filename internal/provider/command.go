// Package provider implements isolated cloud adapters with durable ownership.
// AWS, Azure and GCP use native SDKs with isolated credential scopes.
package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/tsouza/runnerscout/internal/lifecycle"
	"github.com/tsouza/runnerscout/internal/wireguard"
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
	// NetworkPeers supplies the current WireGuard peer snapshot for an
	// allocation intending wireguard-mode networking
	// (lifecycle.Allocation.NetworkProfile != ""), the same optional,
	// caller-injected side-effect pattern Bootstrap already uses for the
	// GitHub JIT token. No caller sets this today: nothing in this codebase's
	// configuration path can ever produce an allocation with NetworkProfile
	// set (see lifecycle.Allocation.NetworkProfile), so this field stays nil
	// everywhere it is constructed. The eventual caller is expected to source
	// the allocation list this hook needs from internal/state.Kubernetes.List
	// and filter it through internal/wireguard.Snapshot.
	NetworkPeers func(context.Context, lifecycle.Allocation) ([]wireguard.Peer, error)
	// AzureInterrupted is a per-Tick-cycle snapshot of confirmed Azure spot
	// preemptions, keyed by the exact ARM resource ID azureID builds
	// (compared case-insensitively - see azureConfirmedPreemption), mapped
	// to that message's own Preempted verdict. internal/operator.Operator
	// populates it once per Tick cycle - immediately before each allocation's
	// Controller.Step call, never per-allocation - from exactly one
	// internal/azurequeue.Client.Poll call, mirroring how refreshAWSPrices/
	// refreshAzurePrices (internal/operator/prices.go) compute a live value
	// once per Tick and thread it through rather than re-querying per
	// offering. See docs/azure-interruption-delivery.md's "Correlation"
	// section for why this is a plain map consulted at the exact allocation
	// being observed, not a persisted cross-Tick cache or index.
	//
	// nil (the default, and every Command whose kind isn't "azure", and
	// every deployment that never opts into internal/operator.Operator's
	// AzureInterruptionQueueURL) is a complete no-op: observeAzure never
	// reports Observation.Interrupted for Azure without an exact, present
	// match in this map.
	AzureInterrupted map[string]bool
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

// bootstrapTrap is the exact literal Bootstrap emits for its cleanup trap.
// BootstrapWithWireGuard locates it by this constant rather than duplicating
// Bootstrap's construction, so the two functions cannot silently drift apart.
const bootstrapTrap = "trap 'rm -f /run/runnerscout/jit;"

// BootstrapWithWireGuard extends Bootstrap's cloud-init script with a second,
// optional secret file for a wireguard-mode allocation: a base64-encoded JSON
// blob (wireGuardJSON, an internal/wireguard.CloudInitPayload) written to
// /run/runnerscout/wireguard.json, cleaned up by the same EXIT trap that
// already removes the JIT token. wireGuardJSON == "" reproduces Bootstrap(jit)
// byte for byte - Bootstrap itself is never modified by this function, so
// that equivalence holds by construction, not merely by testing it - which is
// the "zero effect until wired" guarantee this task requires for every
// allocation that does not intend wireguard mode (every allocation this
// codebase can produce today).
//
// The consumer of this file (VM-side WireGuard interface bring-up) is not
// implemented yet; see internal/wireguard.CloudInitPayload's doc comment and
// docs/networking-peer-model.md's "What this document does not decide"
// section for the open wire-format question this payload leaves unresolved.
func BootstrapWithWireGuard(jit, wireGuardJSON string) (string, error) {
	script := Bootstrap(jit)
	if wireGuardJSON == "" {
		return script, nil
	}
	if !strings.Contains(script, bootstrapTrap) {
		return "", errors.New("wireguard: bootstrap script trap line changed shape; refusing to silently drop the wireguard secret file")
	}
	wb64 := base64.StdEncoding.EncodeToString([]byte(wireGuardJSON))
	install := "printf '%s' '" + wb64 + "' | base64 -d > /run/runnerscout/wireguard.json\nchown runner:runner /run/runnerscout/wireguard.json\n"
	extendedTrap := "trap 'rm -f /run/runnerscout/jit /run/runnerscout/wireguard.json;"
	return strings.Replace(script, bootstrapTrap, install+extendedTrap, 1), nil
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
	// Wireguard-mode embedding only runs when NetworkProfile is set - which,
	// as of this change, no allocation this codebase produces ever is (see
	// lifecycle.Allocation.NetworkProfile and NetworkPeers above). Every
	// existing call path takes the wireGuardJSON == "" branch of
	// BootstrapWithWireGuard, which reproduces Bootstrap(jit) unchanged.
	var wireGuardJSON string
	var wireGuardPublicKey []byte
	var wireGuardPollTokenHash []byte
	if a.NetworkProfile != "" {
		if p.NetworkPeers == nil {
			return lifecycle.Creation{}, lifecycle.ErrNoEffect
		}
		keyPair, genErr := wireguard.Generate()
		if genErr != nil {
			return lifecycle.Creation{}, genErr
		}
		pollToken, pollTokenHash, tokenErr := wireguard.GeneratePollToken()
		if tokenErr != nil {
			return lifecycle.Creation{}, tokenErr
		}
		peers, peersErr := p.NetworkPeers(ctx, a)
		if peersErr != nil {
			return lifecycle.Creation{}, peersErr
		}
		encoded, marshalErr := json.Marshal(wireguard.CloudInitPayload{
			PrivateKey:     keyPair.Private.Base64(),
			PollToken:      pollToken,
			OverlayAddress: a.WireGuardOverlayAddress,
			Peers:          peers,
		})
		if marshalErr != nil {
			return lifecycle.Creation{}, marshalErr
		}
		wireGuardJSON = string(encoded)
		wireGuardPublicKey = keyPair.Public.Bytes()
		wireGuardPollTokenHash = pollTokenHash[:]
	}
	script, err := BootstrapWithWireGuard(jit, wireGuardJSON)
	if err != nil {
		return lifecycle.Creation{}, err
	}
	var creation lifecycle.Creation
	switch p.Config.Kind {
	case "aws":
		creation, err = p.createAWS(ctx, a, script)
	case "azure":
		creation, err = p.createAzure(ctx, a, script)
	default:
		var id string
		id, err = p.createGCP(ctx, a, script)
		creation = lifecycle.Creation{ResourceID: id}
	}
	if len(wireGuardPublicKey) > 0 {
		creation.WireGuardPublicKey = wireGuardPublicKey
	}
	if len(wireGuardPollTokenHash) > 0 {
		creation.WireGuardPollTokenHash = wireGuardPollTokenHash
	}
	return creation, err
}
func (p *Command) Observe(ctx context.Context, a lifecycle.Allocation) (lifecycle.Observation, error) {
	if len(a.Resources) > 0 && p.Config.Kind != "aws" && p.Config.Kind != "azure" {
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
	if p.Config.Kind == "azure" {
		return p.deleteAzure(ctx, a)
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
	return p.deleteGCP(ctx, a)
}

// ValidateName limits names interpolated into provider filter/label grammars.
func ValidateName(s string) error {
	if len(s) < 1 || len(s) > 63 || strings.Trim(s, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" || s[0] < 'a' || s[0] > 'z' {
		return fmt.Errorf("invalid provider-safe name")
	}
	return nil
}
