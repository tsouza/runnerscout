package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/tsouza/runnerscout/internal/lifecycle"
	"github.com/tsouza/runnerscout/internal/placement"
	"os"
	"strings"
	"testing"
)

type fakeExec struct {
	calls     [][]string
	responses [][]byte
	check     func(string, []string)
}

func (f *fakeExec) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if f.check != nil {
		f.check(name, args)
	}
	if len(f.responses) == 0 {
		return nil, errors.New("unexpected call")
	}
	out := f.responses[0]
	f.responses = f.responses[1:]
	return out, nil
}
func allocation() lifecycle.Allocation {
	return lifecycle.Allocation{ID: "rs-test", Offering: placement.Offering{Region: "us-east-1", Zone: "us-east-1a", Machine: "c6i.large", Image: "ami-test", Spot: true}}
}
func TestAWSCreateUsesDurableTokenAndPrivateBootstrap(t *testing.T) {
	f := &fakeExec{responses: [][]byte{[]byte(`{"Account":"000000000000"}`), []byte(`{"Instances":[{"InstanceId":"i-test"}]}`)}}
	f.check = func(name string, args []string) {
		if name != "aws" {
			t.Fatal(name)
		}
		if args[0] == "sts" {
			return
		}
		var input string
		for _, s := range args {
			if strings.HasPrefix(s, "file://") {
				input = strings.TrimPrefix(s, "file://")
			}
			if strings.Contains(s, "secret-jit") {
				t.Fatal("JIT in command arguments")
			}
		}
		info, e := os.Stat(input)
		if e != nil || info.Mode().Perm() != 0600 {
			t.Fatal("unprotected bootstrap", e)
		}
		b, e := os.ReadFile(input)
		if e != nil {
			t.Fatal(e)
		}
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		metadata, ok := body["MetadataOptions"].(map[string]any)
		if !ok || metadata["HttpEndpoint"] != "enabled" || metadata["HttpTokens"] != "required" || metadata["HttpPutResponseHopLimit"] != float64(1) || metadata["InstanceMetadataTags"] != "disabled" || metadata["HttpProtocolIpv6"] != "disabled" {
			t.Errorf("cloud-init requires reachable, token-protected metadata: %v", metadata)
		}
		if _, present := body["IamInstanceProfile"]; present {
			t.Error("runner VM must not inherit a cloud role")
		}
		userData, err := base64.StdEncoding.DecodeString(body["UserData"].(string))
		if err != nil || string(userData) != Bootstrap("secret-jit") {
			t.Error("cloud-init user data cannot start the JIT runner")
		}
		if body["ClientToken"] != "rs-test" || body["MaxCount"] != float64(1) {
			t.Fatal(body)
		}
	}
	p := Command{Config: Config{Kind: "aws", AccountID: "000000000000", Owner: "test", Subnet: "subnet", SecurityGroup: "sg"}, Exec: f, Bootstrap: func(context.Context, string) (string, error) { return "secret-jit", nil }}
	id, e := p.Create(context.Background(), allocation())
	if e != nil || id != "i-test" {
		t.Fatal(id, e)
	}
}
func TestUnknownProviderOutputIsNotAbsence(t *testing.T) {
	f := &fakeExec{responses: [][]byte{[]byte(`{"Account":"000000000000"}`), []byte(`not-json`)}}
	p := Command{Config: Config{Kind: "aws", AccountID: "000000000000", Owner: "test", Subnet: "subnet", SecurityGroup: "sg"}, Exec: f}
	ob, e := p.Observe(context.Background(), allocation())
	if e == nil || ob.Known {
		t.Fatal(ob, e)
	}
}

func TestMissingInventoryCannotConfirmCleanup(t *testing.T) {
	for _, body := range []string{`{}`, `null`} {
		f := &fakeExec{responses: [][]byte{[]byte(`{"Account":"000000000000"}`), []byte(body)}}
		p := Command{Config: Config{Kind: "aws", AccountID: "000000000000", Owner: "test", Subnet: "subnet", SecurityGroup: "sg"}, Exec: f}
		ob, e := p.Observe(context.Background(), allocation())
		if e == nil || ob.Known {
			t.Fatal(body, ob, e)
		}
	}
}

func TestAWSAccountDriftCannotConfirmAbsence(t *testing.T) {
	f := &fakeExec{responses: [][]byte{[]byte(`{"Account":"999999999999"}`)}}
	p := Command{Config: Config{Kind: "aws", AccountID: "000000000000", Owner: "test", Subnet: "subnet", SecurityGroup: "sg"}, Exec: f}
	ob, e := p.Observe(context.Background(), allocation())
	if e == nil || ob.Known || len(f.calls) != 1 {
		t.Fatal(ob, e, f.calls)
	}
}

func TestJITPreparationFailureHasNoCloudEffects(t *testing.T) {
	for _, mode := range []string{"failure", "empty"} {
		t.Run(mode, func(t *testing.T) {
			exec := &fakeExec{}
			p := Command{Config: Config{Kind: "aws", AccountID: "000000000000", Owner: "test", Subnet: "subnet", SecurityGroup: "sg"}, Exec: exec,
				Bootstrap: func(context.Context, string) (string, error) {
					if mode == "failure" {
						return "", errors.New("secret diagnostic must not escape")
					}
					return "", nil
				},
			}
			id, err := p.Create(context.Background(), allocation())
			if id != "" || !errors.Is(err, lifecycle.ErrNoEffect) || len(exec.calls) != 0 {
				t.Fatalf("id=%q err=%v cloud calls=%d", id, err, len(exec.calls))
			}
			if strings.Contains(err.Error(), "secret diagnostic") {
				t.Fatal("upstream diagnostic leaked")
			}
		})
	}
}
