package provider

import (
	"context"
	"encoding/base64"
	"errors"
	"net/url"
	"testing"

	"github.com/tsouza/runnerscout/internal/lifecycle"
	"github.com/tsouza/runnerscout/internal/testutil"
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
