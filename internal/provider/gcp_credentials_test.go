package provider

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"
	compute "google.golang.org/api/compute/v1"
	"google.golang.org/api/option"
)

func TestGCPCredentialFilesIsolateRealTokenExchanges(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/ambient/invalid.json")
	t.Setenv("CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE", "/ambient/other.json")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "ambient-secret")
	keys := map[string]*rsa.PrivateKey{}
	for _, name := range []string{"first", "second"} {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		keys[name+"@fixture.invalid"] = key
	}
	var mu sync.Mutex
	exchanges := map[string]int{}
	requests := map[string]int{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/token" {
			if err := r.ParseForm(); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			claims := jwt.MapClaims{}
			token, err := jwt.ParseWithClaims(r.FormValue("assertion"), claims, func(token *jwt.Token) (any, error) {
				iss, _ := token.Claims.GetIssuer()
				key := keys[iss]
				if key == nil {
					return nil, fmt.Errorf("unexpected identity")
				}
				return &key.PublicKey, nil
			}, jwt.WithValidMethods([]string{"RS256"}))
			if err != nil || !token.Valid || claims["scope"] != compute.ComputeScope {
				t.Error("invalid SDK token exchange", err)
				w.WriteHeader(400)
				return
			}
			iss, _ := claims.GetIssuer()
			name := strings.Split(iss, "@")[0]
			mu.Lock()
			exchanges[name]++
			mu.Unlock()
			writeJSON(w, map[string]any{"access_token": name + "-token", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		name := strings.TrimSuffix(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "-token")
		if name != "first" && name != "second" {
			t.Error("request used ambient or missing credentials")
			w.WriteHeader(403)
			return
		}
		mu.Lock()
		requests[name]++
		mu.Unlock()
		writeJSON(w, gcpOwned("instances"))
	}))
	defer server.Close()
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, server.Client())
	services := []*compute.Service{}
	files := []string{}
	for _, name := range []string{"first", "second"} {
		key, _ := x509.MarshalPKCS8PrivateKey(keys[name+"@fixture.invalid"])
		data, _ := json.Marshal(map[string]any{"type": "service_account", "project_id": "test-project", "client_email": name + "@fixture.invalid", "private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})), "token_uri": server.URL + "/token"})
		file := filepath.Join(t.TempDir(), "credentials.json")
		projectGCPCredential(t, file, data)
		environment := map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": file}
		if name == "second" {
			environment = map[string]string{"CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE": file}
		}
		source, err := gcpCredential(ctx, environment)
		if err != nil {
			t.Fatal(err)
		}
		command, cleanup, err := NewCommand(credentialConfig("gcp"), environment)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		if command.Exec != nil || command.GCP == nil || strings.Contains(fmt.Sprintf("%v %#v", command.GCP, command.GCP), "PRIVATE KEY") {
			t.Fatal("native credential scope used a CLI or exposed key material")
		}
		environment["GOOGLE_APPLICATION_CREDENTIALS"] = "/later/invalid.json"
		service, err := compute.NewService(ctx, option.WithEndpoint(server.URL+"/compute/v1/"), option.WithHTTPClient(gcpHTTPClient(ctx, source)))
		if err != nil {
			t.Fatal(err)
		}
		services = append(services, service)
		files = append(files, file)
	}
	var wg sync.WaitGroup
	for _, service := range services {
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := service.Instances.Get("test-project", "us-central1-a", "rs-test").Context(ctx).Do(); err != nil {
					t.Error("native authenticated request failed", err)
				}
			}()
		}
	}
	wg.Wait()
	mu.Lock()
	initialOK := exchanges["first"] == 1 && exchanges["second"] == 1 && requests["first"] == 2 && requests["second"] == 2
	mu.Unlock()
	if !initialOK {
		t.Fatal("initial scoped exchanges failed", exchanges, requests)
	}
	// Kubernetes projects a changed Secret by replacing the mounted file after
	// API watchers may already have reloaded the same path. Keep this client.
	replacement, err := os.ReadFile(files[1])
	if err != nil {
		t.Fatal(err)
	}
	projectGCPCredential(t, files[0], replacement)
	if _, err := services[0].Instances.Get("test-project", "us-central1-a", "rs-test").Context(ctx).Do(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	rotated := requests["first"] == 2 && requests["second"] == 3 && exchanges["second"] == 2
	mu.Unlock()
	if !rotated {
		t.Error("projected file rotation reused the previous identity", exchanges, requests)
	}
	projectGCPCredential(t, files[0], []byte("invalid-projected-update"))
	if _, err := services[0].Instances.Get("test-project", "us-central1-a", "rs-test").Context(ctx).Do(); err == nil {
		t.Error("invalid projected credentials fell back to a cached identity")
	}
	if err := os.Remove(filepath.Join(filepath.Dir(files[0]), "..data")); err != nil {
		t.Fatal(err)
	}
	if _, err := services[0].Instances.Get("test-project", "us-central1-a", "rs-test").Context(ctx).Do(); err == nil {
		t.Error("missing projected credentials fell back to a cached identity")
	}
	// The independently configured second provider continues to authenticate.
	if _, err := services[1].Instances.Get("test-project", "us-central1-a", "rs-test").Context(ctx).Do(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests["first"] != 2 || requests["second"] != 4 || exchanges["first"] != 1 || exchanges["second"] != 2 {
		t.Error("rotation or invalid update crossed provider scope", exchanges, requests)
	}
	if os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") != "/ambient/invalid.json" || os.Getenv("AWS_SECRET_ACCESS_KEY") != "ambient-secret" {
		t.Fatal("credential selection changed the process environment")
	}
}

// Model Kubernetes AtomicWriter: the leaf symlink stays in place while ..data
// atomically changes to a new directory containing the Secret revision.
func projectGCPCredential(t *testing.T, file string, data []byte) {
	t.Helper()
	dir := filepath.Dir(file)
	revision, err := os.MkdirTemp(dir, "revision-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(revision, filepath.Base(file)), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(file); os.IsNotExist(err) {
		if err := os.Symlink(filepath.Join("..data", filepath.Base(file)), file); err != nil {
			t.Fatal(err)
		}
	}
	next := filepath.Join(dir, "..data-next")
	if err := os.Symlink(filepath.Base(revision), next); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(next, filepath.Join(dir, "..data")); err != nil {
		t.Fatal(err)
	}
}

func TestGCPCredentialExchangeCancellationAndRecovery(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			block := false
			once.Do(func() { block = true; close(started) })
			if block {
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
			}
			w.Header().Set("Content-Type", "application/json")
			writeJSON(w, map[string]any{"access_token": "fixture-token", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("missing recovered identity")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		writeJSON(w, gcpOwned("instances"))
	}))
	defer server.Close()
	defer close(release)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]string{"type": "service_account", "client_email": "cancel@fixture.invalid", "private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded})), "token_uri": server.URL + "/token"})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "credentials.json")
	projectGCPCredential(t, file, data)
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, server.Client())
	source, err := gcpCredential(ctx, map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": file})
	if err != nil {
		t.Fatal(err)
	}
	service, err := compute.NewService(ctx, option.WithEndpoint(server.URL+"/compute/v1/"), option.WithHTTPClient(gcpHTTPClient(ctx, source)))
	if err != nil {
		t.Fatal(err)
	}
	requestCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := service.Instances.Get("test-project", "us-central1-a", "rs-test").Context(requestCtx).Do()
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("token exchange did not start")
	}
	// A second request must respect its own deadline while the refresh is busy.
	waitCtx, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	defer stop()
	if _, err := service.Instances.Get("test-project", "us-central1-a", "rs-test").Context(waitCtx).Do(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("waiting request did not preserve cancellation", err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("token exchange did not preserve cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("token exchange ignored cancellation")
	}
	// The same client must refresh with a new request context after cancellation.
	recoveryCtx, stopRecovery := context.WithTimeout(ctx, 5*time.Second)
	defer stopRecovery()
	if _, err := service.Instances.Get("test-project", "us-central1-a", "rs-test").Context(recoveryCtx).Do(); err != nil {
		t.Fatal("cancelled exchange poisoned subsequent authentication", err)
	}
}
func TestGCPExplicitCredentialFailureNeverFallsBack(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/ambient/ignored.json")
	for _, contents := range []string{`{}`, `{"type":"unknown"}`, `{"type":"external_account","credential_source":{"executable":{"command":"must-not-run"}}}`, `{"type":"impersonated_service_account","source_credentials":{"credential_source":{"executable":{}}}}`} {
		file := filepath.Join(t.TempDir(), "credentials.json")
		if err := os.WriteFile(file, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		command, cleanup, err := NewCommand(credentialConfig("gcp"), map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": file})
		if err == nil || command != nil || cleanup != nil || strings.Contains(err.Error(), "must-not-run") {
			t.Fatal("invalid explicit credentials fell back or leaked", err)
		}
	}
	if command, cleanup, err := NewCommand(credentialConfig("gcp"), map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": "/missing/explicit.json"}); err == nil || command != nil || cleanup != nil {
		t.Fatal("missing explicit file fell back", err)
	}
	command, cleanup, err := NewCommand(credentialConfig("gcp"), map[string]string{})
	if err != nil || command.GCP == nil || command.Exec != nil {
		t.Fatal("explicit metadata mode consulted ambient ADC or CLI", err)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
}
