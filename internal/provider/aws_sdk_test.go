package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

func TestAWSSDKBindsAccountAndEC2ToOneIdentity(t *testing.T) {
	file := filepath.Join(t.TempDir(), "credentials")
	projectGCPCredential(t, file, []byte("[build]\naws_access_key_id = first-id\naws_secret_access_key = first-secret\n"))
	var stsCalls, ec2Calls atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		authorization := r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/xml")
		switch r.Form.Get("Action") {
		case "GetCallerIdentity":
			stsCalls.Add(1)
			account := "000000000000"
			if strings.Contains(authorization, "Credential=second-id/") {
				account = "999999999999"
			} else if !strings.Contains(authorization, "Credential=first-id/") {
				t.Error("STS used an unexpected identity")
				w.WriteHeader(403)
				return
			}
			fmt.Fprintf(w, `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><GetCallerIdentityResult><Account>%s</Account><Arn>arn:aws:iam::%s:user/fixture</Arn><UserId>fixture</UserId></GetCallerIdentityResult><ResponseMetadata><RequestId>fixture</RequestId></ResponseMetadata></GetCallerIdentityResponse>`, account, account)
		case "DescribeInstances":
			ec2Calls.Add(1)
			if !strings.Contains(authorization, "Credential=first-id/") {
				t.Error("EC2 identity changed after STS account verification")
				w.WriteHeader(403)
				return
			}
			fmt.Fprint(w, `<DescribeInstancesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>fixture</requestId><reservationSet/></DescribeInstancesResponse>`)
		default:
			t.Error("unexpected AWS operation")
			w.WriteHeader(400)
		}
	}))
	defer server.Close()
	sdk := &AWSSDK{scope: &awsCredentialScope{values: map[string]string{"AWS_SHARED_CREDENTIALS_FILE": file}, profile: "build", client: server.Client(), endpoint: server.URL}}
	ctx := context.Background()
	client, err := sdk.session(ctx, credentialConfig("aws"), "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	projectGCPCredential(t, file, []byte("[build]\naws_access_key_id = second-id\naws_secret_access_key = second-secret\n"))
	if _, err := client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{}); err != nil {
		t.Fatal("frozen SDK session could not observe EC2", err)
	}
	if next, err := sdk.session(ctx, credentialConfig("aws"), "us-east-1"); err == nil || next != nil {
		t.Fatal("rotated account mismatch enabled EC2")
	}
	if stsCalls.Load() != 2 || ec2Calls.Load() != 1 {
		t.Fatal("account drift invoked EC2 or skipped identity verification", stsCalls.Load(), ec2Calls.Load())
	}
}
