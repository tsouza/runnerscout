package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/tsouza/runnerscout/internal/health"
)

func TestCRDCommandRejectsMixedConfigurationAndAuthentication(t *testing.T) {
	for _, args := range [][]string{
		{}, {"-namespace=test"}, {"-scale-set=build"}, {"-scale-set=../build", "-namespace=test"},
		{"-scale-set=build", "-namespace=other/test"}, {"-config=config.json", "-namespace=test"},
		{"-scale-set=build", "-namespace=test", "-config=config.json"},
		{"-scale-set=build", "-namespace=test", "-github-token-file=token"},
		{"-scale-set=build", "-namespace=test", "-github-app-client-id=app"},
		{"-scale-set=build", "-namespace=test", "-github-app-installation-id=1"},
		{"-scale-set=build", "-namespace=test", "-github-app-key-file=key"},
		{"-scale-set=build", "-namespace=test", "-validate"},
		{"-scale-set=build", "-namespace=test", "extra"},
	} {
		if _, err := parseOptions(args); err == nil {
			t.Errorf("accepted ambiguous arguments: %v", args)
		}
	}
	for _, args := range [][]string{{"-config=config.json", "-validate"}, {"-scale-set=build", "-namespace=test"}} {
		if _, err := parseOptions(args); err != nil {
			t.Errorf("valid configuration rejected: %v", err)
		}
	}
}

func TestMountedValidationRemainsOfflineAndStrict(t *testing.T) {
	data, err := os.ReadFile("../../charts/runnerscout/tests/values.json")
	if err != nil {
		t.Fatal(err)
	}
	var values struct {
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal(data, &values); err != nil {
		t.Fatal(err)
	}
	values.Config["namespace"] = "test"
	data, err = json.Marshal(values.Config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")
	if err := run([]string{"-config=" + path, "-validate"}); err != nil {
		t.Fatal("offline validation required Kubernetes or authentication", err)
	}
	if err := os.WriteFile(path, append(data, []byte(" {}")...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readConfig(path); err == nil {
		t.Fatal("configuration trailer accepted")
	}
	values.Config["unsupported"] = true
	data, err = json.Marshal(values.Config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readConfig(path); err == nil {
		t.Fatal("unknown configuration accepted")
	}
}

func TestHealthListenerFailurePreventsControllerEffects(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	called := false
	err = withHealth(context.Background(), listener.Addr().String(), func(context.Context, *health.Status) error { called = true; return nil })
	if err == nil || called {
		t.Fatal("controller started without its required health endpoint")
	}
	expected := errors.New("controller failed")
	if err := withHealth(context.Background(), "127.0.0.1:0", func(context.Context, *health.Status) error { return expected }); err != expected {
		t.Fatal("health lifecycle hid the controller error", err)
	}
}

func TestShutdownExitPreservesCleanupFailures(t *testing.T) {
	wrapped := fmt.Errorf("listener stopped: %w", context.Canceled)
	if !benignShutdown(wrapped) || !benignShutdown(errors.Join(wrapped, context.Canceled)) {
		t.Fatal("ordinary wrapped cancellation became a process failure")
	}
	cleanup := errors.New("provider credential cache cleanup incomplete")
	if benignShutdown(errors.Join(wrapped, cleanup)) || benignShutdown(fmt.Errorf("shutdown: %w", errors.Join(wrapped, cleanup))) || benignShutdown(cleanup) {
		t.Fatal("cancellation concealed a real cleanup failure")
	}
}
