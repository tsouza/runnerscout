// Package provider implements isolated cloud adapters with durable ownership.
// AWS, Azure and GCP use native SDKs with isolated credential scopes.
package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/tsouza/runnerscout/internal/lifecycle"
	"github.com/tsouza/runnerscout/internal/wireguard"
)

// wireGuardEndpoint renders a VM's already-cloud-assigned private IP as the
// "host:port" outer WireGuard dial address lifecycle.Allocation.
// WireGuardEndpoint/wireguard.Peer.Endpoint carry - see
// lifecycle.Allocation.WireGuardEndpoint's doc comment for the reasoning and
// its known multi-NetworkMapping limitation. privateIP == "" (a provider
// that has not captured one - see Creation.WireGuardEndpoint's doc comment
// for which ones do today) returns "", never a synthesized/partial address.
func wireGuardEndpoint(privateIP string) string {
	if privateIP == "" {
		return ""
	}
	return net.JoinHostPort(privateIP, strconv.Itoa(int(wireguard.DefaultListenPort)))
}

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
	// WireGuardControllerURL is this controller deployment's own externally-
	// reachable base URL, embedded into a wireguard-mode allocation's
	// cloud-init as wireguard.CloudInitPayload.ControllerURL so a VM-side
	// agent's poll loop knows where to send
	// "GET /v1/wireguard/peers/{id}". Operator-supplied: nothing in this
	// codebase observes its own reachable address, the same reason
	// Subnet/SecurityGroup/SSHPublicKey above are all operator-supplied
	// rather than discovered. Empty for every deployment that never opts
	// into wireguard mode.
	WireGuardControllerURL string `json:"wireGuardControllerURL,omitempty"`
	// AzureDiskControllerType, when non-empty, is passed through verbatim
	// as the deployed VM's storageProfile.diskControllerType ("SCSI" or
	// "NVMe") - Azure ARM's own explicit-request mechanism for choosing a
	// controller type independent of what a classic managed image (which
	// carries no controller-type metadata of its own - see
	// docs/qualification-real-cloud.background.md) implies by default.
	// Empty (the default) omits the field entirely, preserving Azure's own
	// default inference exactly as before this field existed - this is a
	// purely additive, opt-in escape hatch for VM sizes whose only
	// supported controller type (found operationally, not derivable from
	// anything else in this Config) doesn't match that default, never a
	// behavior change for anyone who doesn't set it.
	AzureDiskControllerType string `json:"azureDiskControllerType,omitempty"`
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
	// GitHub JIT token. internal/operator.New constructs a real, non-nil
	// value here for every provider.Command it builds, sourcing the
	// allocation list this hook needs from the same
	// internal/state.Kubernetes.List the Operator already reconciles
	// against, filtered through internal/wireguard.Snapshot. It remains nil
	// only when a *Command is built directly, bypassing operator.New (e.g. a
	// unit test exercising a single provider in isolation).
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
	// Wireguard-mode embedding only runs when NetworkProfile is set - only
	// true for an allocation compiled from a "wireguard" mode NetworkProfile
	// (see lifecycle.Allocation.NetworkProfile and NetworkPeers above).
	// Every allocation without wireguard intent still takes the
	// wireGuardJSON == "" branch of BootstrapWithWireGuard, which reproduces
	// Bootstrap(jit) unchanged.
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
			AllocationID:   a.ID,
			ControllerURL:  p.Config.WireGuardControllerURL,
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
		creation, err = p.createGCP(ctx, a, script)
	}
	if len(wireGuardPublicKey) > 0 {
		creation.WireGuardPublicKey = wireGuardPublicKey
	}
	if len(wireGuardPollTokenHash) > 0 {
		creation.WireGuardPollTokenHash = wireGuardPollTokenHash
	}
	// Unlike the two fields above (only ever computed above when
	// a.NetworkProfile != ""), a create* helper captures WireGuardEndpoint
	// unconditionally from its own cloud response - it costs no extra API
	// call regardless of wireguard intent. Clearing it here for every other
	// allocation preserves this task's "zero effect until wired" guarantee
	// (TestCreateWithResourcesZeroEffectWithoutNetworkProfile): no allocation
	// without wireguard intent should gain a new persisted checkpoint field.
	if a.NetworkProfile == "" {
		creation.WireGuardEndpoint = ""
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
