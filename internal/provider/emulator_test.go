//go:build emulators

package provider

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/tsouza/runnerscout/internal/lifecycle"
	"github.com/tsouza/runnerscout/internal/placement"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

type dockerAWS struct {
	network      string
	loseResponse bool
	image        string
}

func (d *dockerAWS) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name != "aws" {
		return nil, errors.New("AWS-only emulator executor")
	}
	cmd := []string{"run", "--rm", "--network", d.network, "-e", "AWS_ACCESS_KEY_ID=test", "-e", "AWS_SECRET_ACCESS_KEY=test", "-e", "AWS_DEFAULT_REGION=us-east-1", "-e", "AWS_EC2_METADATA_DISABLED=true"}
	for _, arg := range args {
		if strings.HasPrefix(arg, "file://") {
			path := strings.TrimPrefix(arg, "file://")
			if !strings.HasPrefix(path, os.TempDir()+"/runnerscout-") {
				return nil, errors.New("unexpected emulator file mount")
			}
			cmd = append(cmd, "--mount", "type=bind,src="+path+",dst="+path+",readonly")
		}
	}
	cmd = append(cmd, d.image)
	cmd = append(cmd, args...)
	cmd = append(cmd, "--endpoint-url", "http://aws:4566")
	result, e := exec.CommandContext(ctx, "docker", cmd...).CombinedOutput()
	if e != nil {
		return nil, errors.New("local AWS CLI failed: " + string(result))
	}
	if d.loseResponse && strings.Contains(strings.Join(args, " "), "run-instances") {
		d.loseResponse = false
		return nil, errors.New("injected response loss after emulator committed")
	}
	return result, nil
}
func TestMinistackAWSLostResponseAndCleanup(t *testing.T) {
	network := os.Getenv("RUNNERSCOUT_EMULATOR_NETWORK")
	image := os.Getenv("RUNNERSCOUT_AWS_CLI_IMAGE")
	if !strings.HasPrefix(network, "runnerscout-emulators-") || !strings.Contains(image, "@sha256:") {
		t.Fatal("explicit isolated emulator network and pinned CLI digest required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	d := &dockerAWS{network: network, image: image}
	call := func(args ...string) map[string]json.RawMessage {
		t.Helper()
		b, e := d.Run(ctx, "aws", append([]string{"ec2", "--output", "json", "--no-cli-pager"}, args...)...)
		if e != nil {
			t.Fatal(e)
		}
		if len(strings.TrimSpace(string(b))) == 0 {
			return map[string]json.RawMessage{}
		}
		var m map[string]json.RawMessage
		if e = json.Unmarshal(b, &m); e != nil {
			t.Fatal(e)
		}
		return m
	}
	v := call("create-vpc", "--cidr-block", "10.77.0.0/16")
	var vpc struct {
		ID string `json:"VpcId"`
	}
	if e := json.Unmarshal(v["Vpc"], &vpc); e != nil || vpc.ID == "" {
		t.Fatal(v, e)
	}
	s := call("create-subnet", "--vpc-id", vpc.ID, "--cidr-block", "10.77.1.0/24", "--availability-zone", "us-east-1a")
	var subnet struct {
		ID string `json:"SubnetId"`
	}
	if e := json.Unmarshal(s["Subnet"], &subnet); e != nil || subnet.ID == "" {
		t.Fatal(s, e)
	}
	sg := call("create-security-group", "--group-name", "runnerscout-test", "--description", "isolated emulator test", "--vpc-id", vpc.ID)
	var group string
	if e := json.Unmarshal(sg["GroupId"], &group); e != nil || group == "" {
		t.Fatal(sg, e)
	}
	p := Command{Config: Config{Kind: "aws", Owner: "test", Subnet: subnet.ID, SecurityGroup: group}, Exec: d, Bootstrap: func(context.Context, string) (string, error) { return "emulator-fixture-not-a-github-credential", nil }}
	a := lifecycle.Allocation{ID: "rs-" + uuid.NewString(), Offering: placement.Offering{Region: "us-east-1", Zone: "us-east-1a", Machine: "t3.micro", Image: "ami-0123456789abcdef0", Spot: true}}
	d.loseResponse = true
	if _, e := p.Create(ctx, a); e == nil {
		t.Fatal("response loss was not injected")
	}
	// A fresh adapter reconciles the durable operation identity against the real
	// emulator process, not a programmed response fixture.
	p2 := Command{Config: p.Config, Exec: d}
	ob, e := p2.Observe(ctx, a)
	if e != nil || !ob.Known || !ob.Exists || ob.ResourceID == "" {
		t.Fatal(ob, e)
	}
	a.ResourceID = ob.ResourceID
	if e = p2.Delete(ctx, a); e != nil {
		t.Fatal(e)
	}
	ob, e = p2.Observe(ctx, a)
	if e != nil || !ob.Known || ob.Exists {
		t.Fatal("cleanup not confirmed", ob, e)
	}
	call("delete-security-group", "--group-id", group)
	call("delete-subnet", "--subnet-id", subnet.ID)
	call("delete-vpc", "--vpc-id", vpc.ID)
}
