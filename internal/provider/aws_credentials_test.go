package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
)

func TestAWSProfileProjectionAndFrozenIdentity(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "ambient-id")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "ambient-secret")
	t.Setenv("AWS_PROFILE", "ambient-profile")
	file := filepath.Join(t.TempDir(), "credentials")
	projectGCPCredential(t, file, []byte("[build]\naws_access_key_id = first-id\naws_secret_access_key = first-secret\n"))
	scope := &awsCredentialScope{values: map[string]string{"AWS_SHARED_CREDENTIALS_FILE": file}, profile: "build"}
	ctx := context.Background()
	first, err := scope.retrieve(ctx, "us-east-1")
	if err != nil || first.AccessKeyID != "first-id" {
		t.Fatal("explicit profile did not select the first identity", err)
	}
	frozen := frozenAWSCredentials(first)
	projectGCPCredential(t, file, []byte("[build]\naws_access_key_id = second-id\naws_secret_access_key = second-secret\n"))
	second, err := scope.retrieve(ctx, "us-east-1")
	if err != nil || second.AccessKeyID != "second-id" {
		t.Fatal("projected profile did not rotate", err)
	}
	prior, err := frozen.Retrieve(ctx)
	if err != nil || prior.AccessKeyID != "first-id" {
		t.Fatal("rotation changed the identity of an operation in progress", err)
	}
	projectGCPCredential(t, file, []byte("[build]\naws_access_key_id = incomplete\n"))
	if _, err := scope.retrieve(ctx, "us-east-1"); err == nil {
		t.Fatal("incomplete projection used cached or ambient credentials")
	}
	if err := os.Remove(filepath.Join(filepath.Dir(file), "..data")); err != nil {
		t.Fatal(err)
	}
	if _, err := scope.retrieve(ctx, "us-east-1"); err == nil {
		t.Fatal("deleted projection used cached or ambient credentials")
	}
}

func TestAWSProfileRejectsProcessAndAmbientSources(t *testing.T) {
	for _, config := range []string{
		"credential_process = must-not-run",
		"role_arn = arn:aws:iam::000000000000:role/test\ncredential_source = Environment",
		"sso_session = developer\nsso_account_id = 000000000000\nsso_role_name = admin",
	} {
		file := filepath.Join(t.TempDir(), "config")
		if err := os.WriteFile(file, []byte("[profile build]\n"+config+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		scope := &awsCredentialScope{values: map[string]string{"AWS_CONFIG_FILE": file}, profile: "build"}
		if _, err := scope.retrieve(context.Background(), "us-east-1"); err == nil || strings.Contains(err.Error(), "must-not-run") {
			t.Fatal("unsupported profile was used or disclosed", err)
		}
	}
	if _, err := (&awsCredentialScope{}).retrieve(context.Background(), "us-east-1"); err == nil {
		t.Fatal("unconfigured provider consulted ambient credentials")
	}
	if _, err := frozenAWSCredentials(aws.Credentials{}).Retrieve(context.Background()); err == nil {
		t.Fatal("empty frozen identity accepted")
	}
}

func TestAWSWebIdentityUsesProjectedTokenAndScopedEndpoint(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "ambient-id")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "ambient-secret")
	t.Setenv("AWS_ENDPOINT_URL_STS", "http://invalid-ambient-endpoint.invalid")
	started := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		token := r.Form.Get("WebIdentityToken")
		if token == "blocked" {
			close(started)
			<-r.Context().Done()
			return
		}
		if r.Form.Get("Action") != "AssumeRoleWithWebIdentity" || r.Form.Get("RoleArn") != "arn:aws:iam::000000000000:role/build" || (token != "first" && token != "second") || r.Header.Get("Authorization") != "" {
			t.Error("web identity request used the wrong token, role or ambient signing credentials")
			w.WriteHeader(403)
			return
		}
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprintf(w, `<AssumeRoleWithWebIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleWithWebIdentityResult><Credentials><AccessKeyId>%s-id</AccessKeyId><SecretAccessKey>fixture-secret</SecretAccessKey><SessionToken>fixture-session</SessionToken><Expiration>%s</Expiration></Credentials><SubjectFromWebIdentityToken>fixture</SubjectFromWebIdentityToken></AssumeRoleWithWebIdentityResult><ResponseMetadata><RequestId>fixture</RequestId></ResponseMetadata></AssumeRoleWithWebIdentityResponse>`, token, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	}))
	defer server.Close()
	file := filepath.Join(t.TempDir(), "token")
	scope := &awsCredentialScope{values: map[string]string{"AWS_WEB_IDENTITY_TOKEN_FILE": file, "AWS_ROLE_ARN": "arn:aws:iam::000000000000:role/build"}, client: server.Client(), endpoint: server.URL}
	for _, identity := range []string{"first", "second"} {
		projectGCPCredential(t, file, []byte(identity))
		value, err := scope.retrieve(context.Background(), "us-east-1")
		if err != nil || value.AccessKeyID != identity+"-id" || !value.CanExpire {
			t.Fatal("projected web identity exchange failed", err)
		}
	}
	projectGCPCredential(t, file, []byte("blocked"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := scope.retrieve(ctx, "us-east-1"); done <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("STS exchange did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("STS exchange did not preserve cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("STS exchange ignored cancellation")
	}
}
