package provider

import (
	"context"
	"encoding/base64"
	"errors"
	"net/url"
	"testing"

	"github.com/tsouza/runnerscout/internal/lifecycle"
	"github.com/tsouza/runnerscout/internal/testutil"
	"github.com/tsouza/runnerscout/internal/wireguard"
)

func nativeAWSFixture(t *testing.T) (*Command, *testutil.AWS, lifecycle.Allocation) {
	f := testutil.NewAWS(t)
	p, cleanup, err := NewCommand(credentialConfig("aws"), map[string]string{"AWS_ACCESS_KEY_ID": "fixture-id", "AWS_SECRET_ACCESS_KEY": "fixture-secret"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cleanup() })
	p.AWS.HTTPClient, p.AWS.Endpoint = f.Server.Client(), f.Server.URL
	p.Bootstrap = func(context.Context, string) (string, error) { return "secret-jit", nil }
	a := allocation()
	a.Offering.Architecture = "amd64"
	return p, f, a
}
func awsEffects(requests []url.Values) []string {
	var effects []string
	for _, request := range requests {
		switch request.Get("Action") {
		case "RunInstances", "TerminateInstances", "DeleteVolume", "DeleteNetworkInterface":
			effects = append(effects, request.Get("Action"))
		}
	}
	return effects
}

// TestAWSSDKCreationCapturesPrivateIPAsWireGuardEndpoint proves the outer-
// endpoint gap resolution for AWS (lifecycle.Allocation.WireGuardEndpoint's
// doc comment): the private IP AWS already returns in the exact
// RunInstances response createAWS already parses for other fields is
// captured onto Creation.WireGuardEndpoint as "ip:internal/wireguard.
// DefaultListenPort", with no new API call.
func TestAWSSDKCreationCapturesPrivateIPAsWireGuardEndpoint(t *testing.T) {
	p, _, a := nativeAWSFixture(t)
	a.NetworkProfile = "profile-a"
	a.WireGuardOverlayAddress = "10.60.0.7"
	p.NetworkPeers = func(context.Context, lifecycle.Allocation) ([]wireguard.Peer, error) { return nil, nil }
	receipt, err := p.CreateWithResources(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	want := testutil.AWSPrivateIP + ":51820"
	if receipt.WireGuardEndpoint != want {
		t.Fatalf("wireguard endpoint: got %q want %q", receipt.WireGuardEndpoint, want)
	}
}

// TestAWSSDKCreationWithoutWireGuardIntentNeverCapturesAnEndpoint is this
// field's "zero effect until wired" regression guard, mirroring
// TestCreateWithResourcesZeroEffectWithoutNetworkProfile in
// wireguard_test.go for the new field: createAWS always has the private IP
// available in its own response, but CreateWithResources must not surface it
// onto Creation for an allocation that never asked for wireguard mode.
func TestAWSSDKCreationWithoutWireGuardIntentNeverCapturesAnEndpoint(t *testing.T) {
	p, _, a := nativeAWSFixture(t)
	if a.NetworkProfile != "" {
		t.Fatal("test fixture unexpectedly set NetworkProfile")
	}
	receipt, err := p.CreateWithResources(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.WireGuardEndpoint != "" {
		t.Fatalf("wireguard endpoint captured with no NetworkProfile intent: %q", receipt.WireGuardEndpoint)
	}
}

func TestAWSSDKCreationReceiptAndOrderedCleanup(t *testing.T) {
	p, f, a := nativeAWSFixture(t)
	ctx := context.Background()
	receipt, err := p.CreateWithResources(ctx, a)
	if err != nil || receipt.ResourceID != testutil.AWSInstanceID || len(receipt.Resources) != 2 {
		t.Fatal("native creation receipt failed", receipt, err)
	}
	var request url.Values
	for _, call := range f.Requests() {
		if call.Get("Action") == "RunInstances" {
			request = call
		}
	}
	if request == nil {
		t.Fatal("SDK did not execute RunInstances")
	}
	for key, want := range map[string]string{
		"InstanceInitiatedShutdownBehavior": "terminate", "ClientToken": a.ID, "MinCount": "1", "MaxCount": "1", "MetadataOptions.HttpEndpoint": "enabled", "MetadataOptions.HttpTokens": "required", "MetadataOptions.HttpPutResponseHopLimit": "1", "MetadataOptions.HttpProtocolIpv6": "disabled", "MetadataOptions.InstanceMetadataTags": "disabled",
		"NetworkInterface.1.AssociatePublicIpAddress": "false", "NetworkInterface.1.DeleteOnTermination": "true", "NetworkInterface.1.SubnetId": p.Config.Subnet, "NetworkInterface.1.SecurityGroupId.1": p.Config.SecurityGroup,
		"BlockDeviceMapping.1.Ebs.DeleteOnTermination": "true", "BlockDeviceMapping.1.Ebs.Encrypted": "true", "InstanceMarketOptions.MarketType": "spot", "InstanceMarketOptions.SpotOptions.InstanceInterruptionBehavior": "terminate",
	} {
		if got := request.Get(key); got != want {
			t.Errorf("%s: got %q want %q", key, got, want)
		}
	}
	if request.Get("IamInstanceProfile.Arn") != "" || request.Get("IamInstanceProfile.Name") != "" {
		t.Error("runner received a cloud role")
	}
	script, err := base64.StdEncoding.DecodeString(request.Get("UserData"))
	if err != nil || string(script) != Bootstrap("secret-jit") {
		t.Fatal("runner bootstrap was not encoded correctly", err)
	}
	a.ResourceID = receipt.ResourceID
	if err := p.Delete(ctx, a); err == nil || len(awsEffects(f.Requests())) != 1 {
		t.Fatal("delete did not require durable dependencies", err)
	}
	a.Resources = receipt.Resources
	for step := 0; step < 3; step++ {
		if err := p.Delete(ctx, a); err != nil {
			t.Fatal("ordered cleanup failed", step, err)
		}
		observed, err := p.Observe(ctx, a)
		if err != nil || !observed.Known || observed.Exists != (step < 2) || len(observed.Resources) != 2 {
			t.Fatal("cleanup acknowledged before all resources were absent", step, observed, err)
		}
	}
	effects := awsEffects(f.Requests())
	want := []string{"RunInstances", "TerminateInstances", "DeleteVolume", "DeleteNetworkInterface"}
	if len(effects) != len(want) {
		t.Fatal("unexpected effects", effects)
	}
	for i := range want {
		if effects[i] != want[i] {
			t.Fatal("cleanup order changed", effects)
		}
	}
}

func TestAWSSDKLostCreateResponseRecoversDependencies(t *testing.T) {
	p, f, a := nativeAWSFixture(t)
	f.LoseNextCreate()
	ctx := context.Background()
	receipt, err := p.CreateWithResources(ctx, a)
	if err == nil || receipt.ResourceID != "" || errors.Is(err, lifecycle.ErrCapacity) || errors.Is(err, lifecycle.ErrNoEffect) {
		t.Fatal("lost response misclassified", receipt, err)
	}
	restarted := &Command{Config: p.Config, AWS: p.AWS}
	observed, err := restarted.observeAWS(ctx, a)
	if err != nil || !observed.Known || !observed.Exists || observed.ResourceID != testutil.AWSInstanceID || len(observed.Resources) != 2 {
		t.Fatal("lost create did not recover owned dependencies", observed, err)
	}
	if len(awsEffects(f.Requests())) != 1 {
		t.Fatal("unknown create was retried", awsEffects(f.Requests()))
	}
}

func TestAWSSDKAccountDriftStopsEffects(t *testing.T) {
	p, f, a := nativeAWSFixture(t)
	ctx := context.Background()
	receipt, err := p.CreateWithResources(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	a.ResourceID = receipt.ResourceID
	a.Resources = receipt.Resources
	f.SetAccount("999999999999")
	if observed, err := p.Observe(ctx, a); err == nil || observed.Known {
		t.Fatal("account drift authorized inventory")
	}
	if err := p.Delete(ctx, a); err == nil || len(awsEffects(f.Requests())) != 1 {
		t.Fatal("account drift authorized deletion", err)
	}
}

func TestAWSSDKDefinitiveCapacityRejectionHasNoReceipt(t *testing.T) {
	p, f, a := nativeAWSFixture(t)
	f.RejectCapacity()
	receipt, err := p.createAWS(context.Background(), a, Bootstrap("fixture-jit"))
	if !errors.Is(err, lifecycle.ErrCapacity) || receipt.ResourceID != "" || len(receipt.Resources) != 0 {
		t.Fatal("definitive capacity rejection misclassified", receipt, err)
	}
}

func TestAWSObservationConfirmsSpotInterruption(t *testing.T) {
	p, f, a := nativeAWSFixture(t)
	a.Offering.Spot = true
	ctx := context.Background()
	receipt, err := p.CreateWithResources(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	a.ResourceID, a.Resources = receipt.ResourceID, receipt.Resources
	f.SpotInterrupt()
	observed, err := p.Observe(ctx, a)
	if err != nil || !observed.Known || observed.Exists || !observed.Interrupted {
		t.Fatal("confirmed spot interruption not observed", observed, err)
	}
}
func TestAWSObservationOrdinaryTerminationIsNotInterruption(t *testing.T) {
	p, _, a := nativeAWSFixture(t)
	a.Offering.Spot = true
	ctx := context.Background()
	receipt, err := p.CreateWithResources(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	a.ResourceID, a.Resources = receipt.ResourceID, receipt.Resources
	var observed lifecycle.Observation
	for step := 0; step < 3; step++ {
		if err := p.Delete(ctx, a); err != nil {
			t.Fatal("ordered cleanup failed", step, err)
		}
		observed, err = p.Observe(ctx, a)
		if err != nil || !observed.Known {
			t.Fatal("cleanup observation unknown", step, err)
		}
	}
	if observed.Exists || observed.Interrupted {
		t.Fatal("controller-initiated termination misclassified as interruption", observed)
	}
}
func TestAWSObservationRequiresSpotOfferingForInterruption(t *testing.T) {
	p, f, a := nativeAWSFixture(t)
	a.Offering.Spot = false
	ctx := context.Background()
	receipt, err := p.CreateWithResources(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	a.ResourceID, a.Resources = receipt.ResourceID, receipt.Resources
	f.SpotInterrupt()
	observed, err := p.Observe(ctx, a)
	if err != nil || !observed.Known || observed.Exists || observed.Interrupted {
		t.Fatal("on-demand offering misclassified as spot interruption", observed, err)
	}
}
