//go:build emulators

package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/google/uuid"
	"github.com/tsouza/runnerscout/internal/lifecycle"
	"github.com/tsouza/runnerscout/internal/placement"
)

type lostAWSResponse struct {
	base       http.RoundTripper
	lost       atomic.Bool
	diagnostic func(int, string)
}

func (r *lostAWSResponse) RoundTrip(request *http.Request) (*http.Response, error) {
	data, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	_ = request.Body.Close()
	request.Body = io.NopCloser(bytes.NewReader(data))
	form, _ := url.ParseQuery(string(data))
	response, err := r.base.RoundTrip(request)
	if err == nil {
		payload, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		response.Body = io.NopCloser(bytes.NewReader(payload))
		if readErr != nil {
			return nil, readErr
		}
		if r.diagnostic != nil {
			r.diagnostic(response.StatusCode, string(payload))
		}
	}
	if err == nil && response.StatusCode == http.StatusOK && form.Get("Action") == "RunInstances" && r.lost.CompareAndSwap(false, true) {
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		return nil, errors.New("injected response loss after emulator committed")
	}
	return response, err
}

func TestMotoAWSLostResponseAndCleanup(t *testing.T)                 { qualifyAWSEmulator(t, false) }
func TestMinistackAWSUnsupportedImageRetainsObligation(t *testing.T) { qualifyAWSEmulator(t, true) }
func qualifyAWSEmulator(t *testing.T, unsupported bool) {
	t.Helper()
	network, endpoint := os.Getenv("RUNNERSCOUT_EMULATOR_NETWORK"), os.Getenv("RUNNERSCOUT_AWS_ENDPOINT")
	if unsupported {
		endpoint = os.Getenv("RUNNERSCOUT_MINISTACK_ENDPOINT")
	}
	target, err := url.Parse(endpoint)
	if err != nil || !strings.HasPrefix(network, "runnerscout-emulators-") || target.Scheme != "http" || (target.Port() != "4566" && target.Port() != "5000") || !net.ParseIP(target.Hostname()).IsPrivate() {
		t.Fatal("explicit isolated emulator network and private AWS endpoint required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second}
	config := aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), HTTPClient: client, RetryMaxAttempts: 1}
	admin := ec2.NewFromConfig(config, func(o *ec2.Options) { o.BaseEndpoint = aws.String(endpoint) })
	vpc, err := admin.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.77.0.0/16")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: vpc.Vpc.VpcId}); err != nil {
			t.Error(err)
		}
	}()
	subnet, err := admin.CreateSubnet(ctx, &ec2.CreateSubnetInput{VpcId: vpc.Vpc.VpcId, CidrBlock: aws.String("10.77.1.0/24"), AvailabilityZone: aws.String("us-east-1a")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.DeleteSubnet(ctx, &ec2.DeleteSubnetInput{SubnetId: subnet.Subnet.SubnetId}); err != nil {
			t.Error(err)
		}
	}()
	group, err := admin.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{VpcId: vpc.Vpc.VpcId, GroupName: aws.String("runnerscout-test"), Description: aws.String("isolated fixture")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{GroupId: group.GroupId}); err != nil {
			t.Error(err)
		}
	}()
	var imageID *string
	if unsupported {
		volume, err := admin.CreateVolume(ctx, &ec2.CreateVolumeInput{AvailabilityZone: aws.String("us-east-1a"), Size: aws.Int32(8), VolumeType: "gp3"})
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if _, err := admin.DeleteVolume(ctx, &ec2.DeleteVolumeInput{VolumeId: volume.VolumeId}); err != nil {
				t.Error(err)
			}
		}()
		if err := ec2.NewVolumeAvailableWaiter(admin).Wait(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{aws.ToString(volume.VolumeId)}}, 30*time.Second); err != nil {
			t.Fatal(err)
		}
		snapshot, err := admin.CreateSnapshot(ctx, &ec2.CreateSnapshotInput{VolumeId: volume.VolumeId})
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if _, err := admin.DeleteSnapshot(ctx, &ec2.DeleteSnapshotInput{SnapshotId: snapshot.SnapshotId}); err != nil {
				t.Error(err)
			}
		}()
		if err := ec2.NewSnapshotCompletedWaiter(admin).Wait(ctx, &ec2.DescribeSnapshotsInput{SnapshotIds: []string{aws.ToString(snapshot.SnapshotId)}}, 30*time.Second); err != nil {
			t.Fatal(err)
		}
		image, err := admin.RegisterImage(ctx, &ec2.RegisterImageInput{Name: aws.String("runnerscout-test"), Architecture: "x86_64", RootDeviceName: aws.String("/dev/sda1"), VirtualizationType: aws.String("hvm"), BlockDeviceMappings: []types.BlockDeviceMapping{{DeviceName: aws.String("/dev/sda1"), Ebs: &types.EbsBlockDevice{SnapshotId: snapshot.SnapshotId, VolumeSize: aws.Int32(8), VolumeType: "gp3", DeleteOnTermination: aws.Bool(true)}}}})
		if err != nil {
			t.Fatal(err)
		}
		registeredID := image.ImageId
		defer func() {
			if _, err := admin.DeregisterImage(ctx, &ec2.DeregisterImageInput{ImageId: registeredID}); err != nil {
				t.Error(err)
			}
		}()
		if err := ec2.NewImageAvailableWaiter(admin).Wait(ctx, &ec2.DescribeImagesInput{ImageIds: []string{aws.ToString(image.ImageId)}}, 30*time.Second); err != nil {
			t.Fatal(err)
		}
		imageID = image.ImageId
	} else {
		available, err := admin.DescribeImages(ctx, &ec2.DescribeImagesInput{Filters: []types.Filter{{Name: aws.String("architecture"), Values: []string{"x86_64"}}, {Name: aws.String("root-device-type"), Values: []string{"ebs"}}}})
		if err != nil {
			t.Fatal(err)
		}
		for _, candidate := range available.Images {
			if candidate.Architecture == "x86_64" && candidate.RootDeviceType == "ebs" && candidate.Platform == "" && len(candidate.BlockDeviceMappings) > 0 && len(candidate.ProductCodes) == 0 && candidate.State == "available" {
				imageID = candidate.ImageId
				break
			}
		}
		if imageID == nil {
			t.Fatal("emulator has no usable built-in Linux EBS image")
		}
	}
	var unrelatedID string
	if !unsupported {
		unrelated, err := admin.RunInstances(ctx, &ec2.RunInstancesInput{ImageId: imageID, MinCount: aws.Int32(1), MaxCount: aws.Int32(1), InstanceType: "t3.micro", SubnetId: subnet.Subnet.SubnetId, ClientToken: aws.String("rs-unrelated")})
		if err != nil || len(unrelated.Instances) != 1 {
			t.Fatal("unrelated filter control failed", err)
		}
		unrelatedID = aws.ToString(unrelated.Instances[0].InstanceId)
		defer func() {
			if _, err := admin.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: []string{unrelatedID}}); err != nil {
				t.Error(err)
			}
		}()
	}
	p, cleanup, err := NewCommand(Config{Kind: "aws", AccountID: "000000000000", Owner: "test", Subnet: aws.ToString(subnet.Subnet.SubnetId), SecurityGroup: aws.ToString(group.GroupId)}, map[string]string{"AWS_ACCESS_KEY_ID": "test", "AWS_SECRET_ACCESS_KEY": "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	var responses []string
	t.Cleanup(func() {
		if t.Failed() {
			for _, response := range responses {
				t.Log(response)
			}
		}
	})
	loss := &lostAWSResponse{base: transport, diagnostic: func(status int, body string) {
		responses = append(responses, fmt.Sprintf("isolated AWS response: %d %s", status, body))
	}}
	p.AWS.HTTPClient, p.AWS.Endpoint = &http.Client{Transport: loss, Timeout: 15 * time.Second}, endpoint
	p.Bootstrap = func(context.Context, string) (string, error) { return "emulator-fixture-not-a-github-credential", nil }
	a := lifecycle.Allocation{ID: "rs-" + uuid.NewString(), Offering: placement.Offering{Region: "us-east-1", Zone: "us-east-1a", Architecture: "amd64", Machine: "t3.micro", Image: aws.ToString(imageID), Spot: true}}
	if _, err := p.Observe(ctx, a); err != nil {
		t.Fatal("initial inventory", err)
	}
	receipt, err := p.CreateWithResources(ctx, a)
	if unsupported {
		if err == nil || receipt.ResourceID != "" || loss.lost.Load() || errors.Is(err, lifecycle.ErrCapacity) || errors.Is(err, lifecycle.ErrNoEffect) {
			t.Fatal("unsupported emulator launch falsely confirmed", receipt, err)
		}
		// A failed create remains uncertain until subsequent inventory proves absence.
		ob, observeErr := p.Observe(ctx, a)
		if observeErr != nil || !ob.Known || ob.Exists {
			t.Fatal("unsupported launch did not reconcile", ob, observeErr)
		}
		return
	}
	if err == nil || receipt.ResourceID != "" || !loss.lost.Load() || errors.Is(err, lifecycle.ErrCapacity) || errors.Is(err, lifecycle.ErrNoEffect) {
		t.Fatal("expected ambiguous committed create", receipt, err)
	}
	ob, err := p.Observe(ctx, a)
	if err != nil || !ob.Known || !ob.Exists || ob.ResourceID == "" || len(ob.Resources) < 2 {
		t.Fatal("lost response/dependencies not recovered", ob, err)
	}
	for token, want := range map[string]string{a.ID: ob.ResourceID, "rs-unrelated": unrelatedID, "rs-missing": ""} {
		inventory, err := admin.DescribeInstances(ctx, &ec2.DescribeInstancesInput{Filters: []types.Filter{{Name: aws.String("client-token"), Values: []string{token}}}})
		if err != nil {
			t.Fatal("emulator filter extension failed", err)
		}
		var ids []string
		for _, reservation := range inventory.Reservations {
			for _, instance := range reservation.Instances {
				ids = append(ids, aws.ToString(instance.InstanceId))
			}
		}
		if want == "" && len(ids) != 0 || want != "" && (len(ids) != 1 || ids[0] != want) {
			t.Fatal("client-token filter crossed allocation identities", token, ids)
		}
	}
	a.ResourceID, a.Resources = ob.ResourceID, ob.Resources
	for _, resource := range a.Resources {
		if resource.Kind != "aws-network-interface" {
			continue
		}
		tag := func(owner string) {
			t.Helper()
			if _, err := admin.CreateTags(ctx, &ec2.CreateTagsInput{Resources: []string{resource.ID}, Tags: []types.Tag{{Key: aws.String("runnerscout-owner"), Value: aws.String(owner)}}}); err != nil {
				t.Fatal(err)
			}
		}
		tag("foreign-owner")
		if observed, err := p.Observe(ctx, a); err == nil || observed.Known {
			t.Fatal("foreign interface ownership was silently accepted", observed, err)
		}
		tag(p.Config.Owner)
		if observed, err := p.Observe(ctx, a); err != nil || !observed.Known || !observed.Exists {
			t.Fatal("restored interface ownership not observed", observed, err)
		}
	}

	for attempts := 0; attempts < 20; attempts++ {
		if err := p.Delete(ctx, a); err != nil {
			t.Fatal(err)
		}
		ob, err = p.Observe(ctx, a)
		if err != nil || !ob.Known {
			t.Fatal(ob, err)
		}
		if !ob.Exists {
			unrelated, err := admin.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{unrelatedID}})
			if err != nil || len(unrelated.Reservations) != 1 || len(unrelated.Reservations[0].Instances) != 1 {
				t.Fatal("unrelated instance lost during cleanup", err)
			}
			state := unrelated.Reservations[0].Instances[0].State
			if state == nil || state.Name != "pending" && state.Name != "running" {
				t.Fatal("cleanup affected unrelated instance", state)
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
	t.Fatal("cleanup not observed", ob)
}
