package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tsouza/runnerscout/internal/health"
	"github.com/tsouza/runnerscout/internal/operator"
	"github.com/tsouza/runnerscout/internal/placement"
	"github.com/tsouza/runnerscout/internal/provider"
	"github.com/tsouza/runnerscout/internal/recovery"
	"github.com/tsouza/runnerscout/internal/testutil"
	"k8s.io/client-go/kubernetes/fake"
)

func awsPriceRefreshConfig(enabled bool, providers map[string]provider.Config, requirementProviders []string) operator.Config {
	return operator.Config{Name: "test", Namespace: "test", GitHubURL: "https://github.com/tsouza/runnerscout", ScaleSetID: 1, MaxRunners: 2, ProvisioningSeconds: 60, MaxLifetimeSeconds: 600,
		Requirements:    placement.Requirements{CPU: 1, MemoryMiB: 1, Architecture: "amd64", MaxPriceMicros: 100, Providers: requirementProviders, Regions: []string{"r"}, Policy: "lowest-price"},
		Providers:       providers,
		AWSPriceRefresh: enabled,
	}
}

func TestAWSPricesObserverNilWhenDisabled(t *testing.T) {
	cfg := awsPriceRefreshConfig(false, map[string]provider.Config{"aws": {Kind: "aws", Owner: "test", AccountID: "000000000000", Subnet: "private", SecurityGroup: "private"}}, []string{"aws"})
	controller := operator.New(cfg, fake.NewClientset(), nil)
	observer, err := awsPricesObserver(controller, cfg)
	if err != nil || observer != nil {
		t.Fatal("disabled price refresh still built an observer", observer, err)
	}
}

func TestAWSPricesObserverRequiresConfiguredAWSProvider(t *testing.T) {
	cfg := awsPriceRefreshConfig(true, map[string]provider.Config{"azure": {Kind: "azure", Owner: "test", Subnet: "private", Subscription: "sub", ResourceGroup: "rg", SecurityGroup: "sg", SSHPublicKey: "ssh-ed25519 AAAA"}}, []string{"azure"})
	controller := operator.New(cfg, fake.NewClientset(), nil)
	if observer, err := awsPricesObserver(controller, cfg); err == nil || observer != nil {
		t.Fatal("expected an error without a configured \"aws\" provider", observer, err)
	}
}

func TestAWSPricesObserverBuildsFromConfiguredAWSProvider(t *testing.T) {
	cfg := awsPriceRefreshConfig(true, map[string]provider.Config{"aws": {Kind: "aws", Owner: "test", AccountID: "000000000000", Subnet: "private", SecurityGroup: "private"}}, []string{"aws"})
	credentials := map[string]map[string]string{"aws": {"AWS_ACCESS_KEY_ID": "fixture-id", "AWS_SECRET_ACCESS_KEY": "fixture-secret"}}
	controller, cleanup, err := operator.NewWithCredentials(cfg, fake.NewClientset(), nil, credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	observer, err := awsPricesObserver(controller, cfg)
	if err != nil || observer == nil {
		t.Fatal("expected a configured observer", observer, err)
	}
}

func azurePriceRefreshConfig(enabled bool, providers map[string]provider.Config, requirementProviders []string) operator.Config {
	return operator.Config{Name: "test", Namespace: "test", GitHubURL: "https://github.com/tsouza/runnerscout", ScaleSetID: 1, MaxRunners: 2, ProvisioningSeconds: 60, MaxLifetimeSeconds: 600,
		Requirements:      placement.Requirements{CPU: 1, MemoryMiB: 1, Architecture: "amd64", MaxPriceMicros: 100, Providers: requirementProviders, Regions: []string{"r"}, Policy: "lowest-price"},
		Providers:         providers,
		AzurePriceRefresh: enabled,
	}
}

func TestAzurePricesObserverNilWhenDisabled(t *testing.T) {
	cfg := azurePriceRefreshConfig(false, map[string]provider.Config{"azure": {Kind: "azure", Owner: "test", Subnet: "private", Subscription: "sub", ResourceGroup: "rg", SecurityGroup: "sg", SSHPublicKey: "ssh-ed25519 AAAA"}}, []string{"azure"})
	controller := operator.New(cfg, fake.NewClientset(), nil)
	observer, err := azurePricesObserver(controller, cfg)
	if err != nil || observer != nil {
		t.Fatal("disabled price refresh still built an observer", observer, err)
	}
}

func TestAzurePricesObserverRequiresConfiguredAzureProvider(t *testing.T) {
	cfg := azurePriceRefreshConfig(true, map[string]provider.Config{"aws": {Kind: "aws", Owner: "test", AccountID: "000000000000", Subnet: "private", SecurityGroup: "private"}}, []string{"aws"})
	controller := operator.New(cfg, fake.NewClientset(), nil)
	if observer, err := azurePricesObserver(controller, cfg); err == nil || observer != nil {
		t.Fatal("expected an error without a configured \"azure\" provider", observer, err)
	}
}

func TestAzurePricesObserverBuildsFromConfiguredAzureProvider(t *testing.T) {
	cfg := azurePriceRefreshConfig(true, map[string]provider.Config{"azure": {Kind: "azure", Owner: "test", Subnet: "private", Subscription: "sub", ResourceGroup: "rg", SecurityGroup: "sg", SSHPublicKey: "ssh-ed25519 AAAA"}}, []string{"azure"})
	credentials := map[string]map[string]string{"azure": {"AZURE_CLIENT_ID": "fixture-client"}}
	controller, cleanup, err := operator.NewWithCredentials(cfg, fake.NewClientset(), nil, credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	observer, err := azurePricesObserver(controller, cfg)
	if err != nil || observer == nil {
		t.Fatal("expected a configured observer", observer, err)
	}
}

func gcpPriceRefreshConfig(enabled bool, providers map[string]provider.Config, requirementProviders []string) operator.Config {
	return operator.Config{Name: "test", Namespace: "test", GitHubURL: "https://github.com/tsouza/runnerscout", ScaleSetID: 1, MaxRunners: 2, ProvisioningSeconds: 60, MaxLifetimeSeconds: 600,
		Requirements:    placement.Requirements{CPU: 1, MemoryMiB: 1, Architecture: "amd64", MaxPriceMicros: 100, Providers: requirementProviders, Regions: []string{"r"}, Policy: "lowest-price"},
		Providers:       providers,
		GCPPriceRefresh: enabled,
	}
}

func TestGCPPricesObserverNilWhenDisabled(t *testing.T) {
	cfg := gcpPriceRefreshConfig(false, map[string]provider.Config{"gcp": {Kind: "gcp", Owner: "test", Project: "project", Subnet: "private"}}, []string{"gcp"})
	controller := operator.New(cfg, fake.NewClientset(), nil)
	observer, err := gcpPricesObserver(controller, cfg)
	if err != nil || observer != nil {
		t.Fatal("disabled price refresh still built an observer", observer, err)
	}
}

func TestGCPPricesObserverRequiresConfiguredGCPProvider(t *testing.T) {
	cfg := gcpPriceRefreshConfig(true, map[string]provider.Config{"aws": {Kind: "aws", Owner: "test", AccountID: "000000000000", Subnet: "private", SecurityGroup: "private"}}, []string{"aws"})
	controller := operator.New(cfg, fake.NewClientset(), nil)
	if observer, err := gcpPricesObserver(controller, cfg); err == nil || observer != nil {
		t.Fatal("expected an error without a configured \"gcp\" provider", observer, err)
	}
}

func TestGCPPricesObserverRequiresBillingAPIKey(t *testing.T) {
	cfg := gcpPriceRefreshConfig(true, map[string]provider.Config{"gcp": {Kind: "gcp", Owner: "test", Project: "project", Subnet: "private"}}, []string{"gcp"})
	// No GCP_BILLING_API_KEY supplied - construction of the GCP provider
	// itself still succeeds (it needs no billing key at all), so this
	// exercises gcpPricesObserver's own extra check, not provider setup.
	// GOOGLE_APPLICATION_CREDENTIALS is set to a valid fixture (rather than
	// left absent) so this test's outcome never depends on whether the host
	// process happens to have ambient GCP credential environment variables.
	credentials := map[string]map[string]string{"gcp": {"GOOGLE_APPLICATION_CREDENTIALS": testutil.GCPServiceAccountFixture(t)}}
	controller, cleanup, err := operator.NewWithCredentials(cfg, fake.NewClientset(), nil, credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if observer, err := gcpPricesObserver(controller, cfg); err == nil || observer != nil {
		t.Fatal("expected an error without a configured billing API key", observer, err)
	}
}

func TestGCPPricesObserverBuildsFromConfiguredGCPProvider(t *testing.T) {
	cfg := gcpPriceRefreshConfig(true, map[string]provider.Config{"gcp": {Kind: "gcp", Owner: "test", Project: "project", Subnet: "private"}}, []string{"gcp"})
	credentials := map[string]map[string]string{"gcp": {"GCP_BILLING_API_KEY": "fixture-key"}}
	controller, cleanup, err := operator.NewWithCredentials(cfg, fake.NewClientset(), nil, credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	observer, err := gcpPricesObserver(controller, cfg)
	if err != nil || observer == nil {
		t.Fatal("expected a configured observer", observer, err)
	}
}

func azureInterruptionQueueConfig(url string, providers map[string]provider.Config, requirementProviders []string) operator.Config {
	return operator.Config{Name: "test", Namespace: "test", GitHubURL: "https://github.com/tsouza/runnerscout", ScaleSetID: 1, MaxRunners: 2, ProvisioningSeconds: 60, MaxLifetimeSeconds: 600,
		Requirements:              placement.Requirements{CPU: 1, MemoryMiB: 1, Architecture: "amd64", MaxPriceMicros: 100, Providers: requirementProviders, Regions: []string{"r"}, Policy: "lowest-price"},
		Providers:                 providers,
		AzureInterruptionQueueURL: url,
	}
}

func TestAzureInterruptionsObserverNilWhenUnset(t *testing.T) {
	cfg := azureInterruptionQueueConfig("", map[string]provider.Config{"azure": {Kind: "azure", Owner: "test", Subnet: "private", Subscription: "sub", ResourceGroup: "rg", SecurityGroup: "sg", SSHPublicKey: "ssh-ed25519 AAAA"}}, []string{"azure"})
	controller := operator.New(cfg, fake.NewClientset(), nil)
	observer, err := azureInterruptionsObserver(controller, cfg)
	if err != nil || observer != nil {
		t.Fatal("unset queue URL still built an observer", observer, err)
	}
}

func TestAzureInterruptionsObserverRequiresConfiguredAzureProvider(t *testing.T) {
	cfg := azureInterruptionQueueConfig("https://fixture.queue.core.windows.net/interruptions", map[string]provider.Config{"aws": {Kind: "aws", Owner: "test", AccountID: "000000000000", Subnet: "private", SecurityGroup: "private"}}, []string{"aws"})
	controller := operator.New(cfg, fake.NewClientset(), nil)
	if observer, err := azureInterruptionsObserver(controller, cfg); err == nil || observer != nil {
		t.Fatal("expected an error without a configured \"azure\" provider", observer, err)
	}
}

func TestAzureInterruptionsObserverBuildsFromConfiguredAzureProvider(t *testing.T) {
	cfg := azureInterruptionQueueConfig("https://fixture.queue.core.windows.net/interruptions", map[string]provider.Config{"azure": {Kind: "azure", Owner: "test", Subnet: "private", Subscription: "sub", ResourceGroup: "rg", SecurityGroup: "sg", SSHPublicKey: "ssh-ed25519 AAAA"}}, []string{"azure"})
	credentials := map[string]map[string]string{"azure": {"AZURE_CLIENT_ID": "fixture-client"}}
	controller, cleanup, err := operator.NewWithCredentials(cfg, fake.NewClientset(), nil, credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	observer, err := azureInterruptionsObserver(controller, cfg)
	if err != nil || observer == nil {
		t.Fatal("expected a configured observer", observer, err)
	}
}

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
	err = withHealth(context.Background(), listener.Addr().String(), nil, func(context.Context, *health.Status) error { called = true; return nil })
	if err == nil || called {
		t.Fatal("controller started without its required health endpoint")
	}
	expected := errors.New("controller failed")
	if err := withHealth(context.Background(), "127.0.0.1:0", nil, func(context.Context, *health.Status) error { return expected }); err != expected {
		t.Fatal("health lifecycle hid the controller error", err)
	}
}

// TestWithHealthMountsWireGuardPeersHandlerBeforeServing proves the real
// structural constraint this wiring has to satisfy: Status.Handler builds
// its mux once, so a non-nil WireGuardPeers handler must already be set on
// the *health.Status before withHealth calls Handler() to build the server -
// setting it later, from inside run, would silently never register the
// route. withHealth takes it as a parameter for exactly this reason.
func TestWithHealthMountsWireGuardPeersHandlerBeforeServing(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	marker := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		done <- withHealth(ctx, addr, marker, func(ctx context.Context, status *health.Status) error {
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	var resp *http.Response
	for i := 0; i < 50; i++ {
		resp, err = http.Get("http://" + addr + "/v1/wireguard/peers/x")
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTeapot {
		t.Fatalf("expected the provided WireGuardPeers handler to be mounted, got status %d", resp.StatusCode)
	}
}

func TestWireGuardPeersHandlerServesTheOwnerScopedAllocationStore(t *testing.T) {
	client := fake.NewClientset()
	handler := wireGuardPeersHandler(client, "test", "build")
	if handler == nil {
		t.Fatal("expected a non-nil WireGuardPeers handler")
	}
	req := httptest.NewRequest("GET", "/v1/wireguard/peers/does-not-exist", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	// No Authorization header and no such allocation: this must be the
	// handler's own 401, not a 404 (which would mean it wasn't reached) or a
	// panic (which would mean its Store was left nil).
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 from a real WireGuardPeersHandler, got %d", recorder.Code)
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

// TestVersionFlagBypassesConfigRequirement proves -version is a pure query:
// it parses successfully alone, with neither -config nor -scale-set/-namespace
// present, unlike every other mode this binary supports.
func TestVersionFlagBypassesConfigRequirement(t *testing.T) {
	o, err := parseOptions([]string{"-version"})
	if err != nil {
		t.Fatal(err)
	}
	if !o.printVersion {
		t.Fatal("printVersion not set from -version flag")
	}
}

// TestVersionFlagRunExitsBeforeAnyClusterOrConfigAccess proves run() itself
// - not just parseOptions - honors -version as an immediate, side-effect-free
// exit: no in-cluster Kubernetes config is required (unlike every other
// mode, which fails without one).
func TestVersionFlagRunExitsBeforeAnyClusterOrConfigAccess(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")
	if err := run([]string{"-version"}); err != nil {
		t.Fatal("-version required Kubernetes or configuration", err)
	}
}

func TestCRDChecksRequireOneExplicitMode(t *testing.T) {
	for _, args := range [][]string{
		{"-check-crd"}, {"-config=config.json", "-check-uninstall"},
		{"-scale-set=build", "-namespace=test", "-check-crd", "-check-uninstall"},
		{"-scale-set=build", "-namespace=test", "-check-crd", "-validate"},
	} {
		if _, err := parseOptions(args); err == nil {
			t.Fatalf("accepted mixed check mode: %v", args)
		}
	}
	for _, mode := range []string{"-check-crd", "-check-uninstall"} {
		if _, err := parseOptions([]string{"-scale-set=build", "-namespace=test", mode}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestInterruptedSafetyCheckCannotReportSuccessfulExit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, result := range []error{context.Canceled, nil} {
		err := runCheck(ctx, func(context.Context) error { return result })
		if benignShutdown(err) {
			t.Fatal("interrupted safety check became a successful process exit")
		}
	}
	if err := runCheck(context.Background(), func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubJobsClientRequiresPATWhenRetryEnabled(t *testing.T) {
	enabled := operator.Config{Retry: recovery.Policy{Enabled: true}}
	if client, err := githubJobsClient(options{}, enabled); err == nil || client != nil {
		t.Fatal("retry enabled without any GitHub auth was accepted", client, err)
	}
	if client, err := githubJobsClient(options{appID: "app", installationID: 1, appKey: "key"}, enabled); err == nil || client != nil {
		t.Fatal("retry enabled with App authentication was accepted", client, err)
	}
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(" secret-token \n"), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := githubJobsClient(options{tokenPath: path}, enabled)
	if err != nil || client == nil || client.Token != "secret-token" {
		t.Fatal("PAT authentication did not produce a usable client", client, err)
	}
}
func TestGitHubJobsClientNilWhenRetryDisabled(t *testing.T) {
	client, err := githubJobsClient(options{}, operator.Config{})
	if err != nil || client != nil {
		t.Fatal("disabled retry policy still built a client", client, err)
	}
}
