// Package provider implements isolated cloud adapters with durable ownership.
// Azure and GCP use native SDKs; AWS uses an isolated CLI without a shell.
package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/tsouza/runnerscout/internal/lifecycle"
	"os"
	"os/exec"
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
type Executor interface {
	Run(context.Context, string, ...string) ([]byte, error)
}
type OSExecutor struct{ environment []string }

func (OSExecutor) String() string   { return "cloud executor (credentials redacted)" }
func (OSExecutor) GoString() string { return "cloud executor (credentials redacted)" }

func (e OSExecutor) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = e.environment
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if name == "aws" && errors.As(err, &exit) && strings.Contains(string(exit.Stderr), "An error occurred (InsufficientInstanceCapacity) when calling the RunInstances operation") {
			return nil, lifecycle.ErrCapacity
		}
		return nil, errors.New("cloud command failed; commitment may be unknown")
	}
	return out, nil
}

type Command struct {
	GCP       *GCPSDK
	Azure     *AzureSDK
	azureOnce sync.Once
	Config    Config
	Exec      Executor
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
func (p *Command) aws(ctx context.Context, region string, args ...string) ([]byte, error) {
	// A credential profile may be rotated or rebound without changing its name.
	// Verify the immutable account before interpreting resource absence or effects.
	identityArgs := []string{"sts", "get-caller-identity", "--region", region, "--output", "json", "--no-cli-pager"}
	if p.Config.Profile != "" {
		identityArgs = append(identityArgs, "--profile", p.Config.Profile)
	}
	identity, e := p.Exec.Run(ctx, "aws", identityArgs...)
	if e != nil {
		return nil, errors.New("AWS account identity unavailable")
	}
	var who struct{ Account string }
	if json.Unmarshal(identity, &who) != nil || who.Account != p.Config.AccountID {
		return nil, errors.New("AWS account identity mismatch")
	}

	all := []string{"ec2", "--region", region, "--output", "json", "--no-cli-pager"}
	if p.Config.Profile != "" {
		all = append(all, "--profile", p.Config.Profile)
	}
	return p.Exec.Run(ctx, "aws", append(all, args...)...)
}
func privateFile(content string) (string, func(), error) {
	f, e := os.CreateTemp("", "runnerscout-*")
	if e != nil {
		return "", nil, e
	}
	name := f.Name()
	cleanup := func() { _ = os.Remove(name) }
	if _, e = f.WriteString(content); e != nil {
		_ = f.Close()
		cleanup()
		return "", nil, e
	}
	if e = f.Close(); e != nil {
		cleanup()
		return "", nil, e
	}
	return name, cleanup, nil
}
func Bootstrap(jit string) string {
	// cloud-init never prints the JIT token; the image already contains runner binaries.
	b64 := base64.StdEncoding.EncodeToString([]byte(jit))
	return "#!/bin/bash\nset -eu\numask 077\ninstall -d -m 0700 -o runner -g runner /run/runnerscout\nprintf '%s' '" + b64 + "' | base64 -d > /run/runnerscout/jit\nchown runner:runner /run/runnerscout/jit\ntrap 'rm -f /run/runnerscout/jit; shutdown -h now' EXIT\ncd /opt/actions-runner\nrunuser -u runner -- sh -c 'exec ./run.sh --jitconfig \"$(cat /run/runnerscout/jit)\"'\n"
}
func (p *Command) Create(ctx context.Context, a lifecycle.Allocation) (string, error) {
	if len(a.Resources) > 0 {
		return "", errors.New("cannot create over recorded cloud dependencies")
	}
	if err := ValidateName(a.ID); err != nil {
		return "", err
	}
	if err := p.Validate(); err != nil {
		return "", err
	}
	jit, err := p.Bootstrap(ctx, a.ID)
	if err != nil {
		return "", lifecycle.ErrNoEffect
	}
	if jit == "" {
		return "", lifecycle.ErrNoEffect
	}
	script := Bootstrap(jit)
	if p.Config.Kind == "azure" {
		return p.createAzure(ctx, a, script)
	}
	if p.Config.Kind == "aws" {
		// cloud-init reads user data from IMDS. Require tokens and a single-hop
		// response; no instance profile grants cloud credentials to the runner.
		body := map[string]any{"ImageId": a.Offering.Image, "InstanceType": a.Offering.Machine, "MinCount": 1, "MaxCount": 1, "ClientToken": a.ID, "SubnetId": p.Config.Subnet, "SecurityGroupIds": []string{p.Config.SecurityGroup}, "Placement": map[string]string{"AvailabilityZone": a.Offering.Zone}, "UserData": base64.StdEncoding.EncodeToString([]byte(script)), "MetadataOptions": map[string]any{"HttpEndpoint": "enabled", "HttpTokens": "required", "HttpPutResponseHopLimit": 1, "HttpProtocolIpv6": "disabled", "InstanceMetadataTags": "disabled"}, "InstanceInitiatedShutdownBehavior": "terminate", "TagSpecifications": []any{map[string]any{"ResourceType": "instance", "Tags": []any{map[string]string{"Key": "runnerscout-owner", "Value": p.Config.Owner}, map[string]string{"Key": "runnerscout-operation", "Value": a.ID}}}}}
		if a.Offering.Spot {
			body["InstanceMarketOptions"] = map[string]any{"MarketType": "spot", "SpotOptions": map[string]any{"SpotInstanceType": "one-time", "InstanceInterruptionBehavior": "terminate"}}
		}
		b, _ := json.Marshal(body)
		path, clean, e := privateFile(string(b))
		if e != nil {
			return "", e
		}
		defer clean()
		out, e := p.aws(ctx, a.Offering.Region, "run-instances", "--cli-input-json", "file://"+path)
		if e != nil {
			return "", e
		}
		var response struct {
			Instances []struct {
				InstanceID string `json:"InstanceId"`
			}
		}
		if e = json.Unmarshal(out, &response); e != nil || len(response.Instances) != 1 {
			return "", errors.New("invalid create response")
		}
		return response.Instances[0].InstanceID, nil
	}
	return p.createGCP(ctx, a, script)
}
func (p *Command) Observe(ctx context.Context, a lifecycle.Allocation) (lifecycle.Observation, error) {
	if len(a.Resources) > 0 {
		return lifecycle.Observation{}, errors.New("provider cannot reconcile recorded cloud dependencies")
	}
	if err := ValidateName(a.ID); err != nil {
		return lifecycle.Observation{}, err
	}
	if err := p.Validate(); err != nil {
		return lifecycle.Observation{}, err
	}
	if p.Config.Kind == "aws" {
		out, err := p.aws(ctx, a.Offering.Region, "describe-instances", "--filters", "Name=client-token,Values="+a.ID, "Name=tag:runnerscout-owner,Values="+p.Config.Owner, "Name=tag:runnerscout-operation,Values="+a.ID, "Name=instance-state-name,Values=pending,running,shutting-down,stopping,stopped")
		if err != nil {
			return lifecycle.Observation{}, err
		}
		var response struct {
			Reservations []struct {
				Instances []struct {
					ID string `json:"InstanceId"`
				}
			}
		}
		if err = json.Unmarshal(out, &response); err != nil {
			return lifecycle.Observation{}, err
		}
		if response.Reservations == nil {
			return lifecycle.Observation{}, errors.New("missing reservation inventory")
		}
		ids := []string{}
		for _, r := range response.Reservations {
			for _, i := range r.Instances {
				ids = append(ids, i.ID)
			}
		}
		if len(ids) > 1 {
			return lifecycle.Observation{}, errors.New("multiple resources for operation")
		}
		if len(ids) == 0 {
			return lifecycle.Observation{Known: true}, nil
		}
		if ids[0] == "" {
			return lifecycle.Observation{}, errors.New("empty observed identity")
		}
		return lifecycle.Observation{Known: true, Exists: true, ResourceID: ids[0]}, nil
	}
	if p.Config.Kind == "azure" {
		return p.observeAzure(ctx, a)
	}
	return p.observeGCP(ctx, a)
}
func (p *Command) Delete(ctx context.Context, a lifecycle.Allocation) error {
	ob, err := p.Observe(ctx, a)
	if err != nil {
		return err
	}
	if !ob.Known {
		return errors.New("unknown ownership")
	}
	if !ob.Exists {
		return nil
	}
	if a.ResourceID != "" && a.ResourceID != ob.ResourceID {
		return errors.New("resource identity changed")
	}
	if p.Config.Kind == "azure" {
		return p.deleteAzure(ctx, a)
	}
	if p.Config.Kind == "aws" {
		_, err = p.aws(ctx, a.Offering.Region, "terminate-instances", "--instance-ids", ob.ResourceID)
	} else {
		err = p.deleteGCP(ctx, a)
	}
	return err
}

// ValidateName limits names interpolated into provider filter/label grammars.
func ValidateName(s string) error {
	if len(s) < 1 || len(s) > 63 || strings.Trim(s, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" || s[0] < 'a' || s[0] > 'z' {
		return fmt.Errorf("invalid provider-safe name")
	}
	return nil
}
