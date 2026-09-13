package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/actions/scaleset"
	"github.com/tsouza/runnerscout/internal/azurequeue"
	"github.com/tsouza/runnerscout/internal/configapi"
	"github.com/tsouza/runnerscout/internal/githubjobs"
	"github.com/tsouza/runnerscout/internal/health"
	"github.com/tsouza/runnerscout/internal/operator"
	"github.com/tsouza/runnerscout/internal/provider"
	"github.com/tsouza/runnerscout/internal/state"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type options struct {
	checkCRD, checkUninstall                       bool
	healthAddress, configPath, namespace, scaleSet string
	tokenPath, appID, appKey                       string
	installationID                                 int64
	validate                                       bool
}

func parseOptions(args []string) (options, error) {
	var o options
	flags := flag.NewFlagSet("runnerscout", flag.ContinueOnError)
	flags.StringVar(&o.healthAddress, "health-address", ":8080", "HTTP liveness/readiness listen address")
	flags.StringVar(&o.configPath, "config", "", "mounted JSON configuration file")
	flags.StringVar(&o.namespace, "namespace", "", "namespace of the RunnerScaleSet CRD")
	flags.StringVar(&o.scaleSet, "scale-set", "", "name of the RunnerScaleSet CRD to reconcile")
	flags.BoolVar(&o.validate, "validate", false, "validate mounted configuration without external operations")
	flags.StringVar(&o.tokenPath, "github-token-file", "", "mounted GitHub token file")
	flags.StringVar(&o.appID, "github-app-client-id", "", "GitHub App client ID")
	flags.Int64Var(&o.installationID, "github-app-installation-id", 0, "GitHub App installation ID")
	flags.StringVar(&o.appKey, "github-app-key-file", "", "mounted GitHub App private-key file")
	flags.BoolVar(&o.checkCRD, "check-crd", false, "check a CRD snapshot through Kubernetes without GitHub or cloud operations")
	flags.BoolVar(&o.checkUninstall, "check-uninstall", false, "check that CRD deletion and durable cleanup are complete before uninstall")
	if err := flags.Parse(args); err != nil {
		return o, err
	}
	if flags.NArg() != 0 {
		return o, errors.New("unexpected positional arguments")
	}
	if (o.checkCRD || o.checkUninstall) && o.scaleSet == "" || o.checkCRD && o.checkUninstall {
		return o, errors.New("select only one CRD check and provide -scale-set and -namespace")
	}
	if o.scaleSet != "" {
		if o.configPath != "" || o.tokenPath != "" || o.appID != "" || o.installationID != 0 || o.appKey != "" || o.validate {
			return o, errors.New("-scale-set uses CRD configuration and Secret references; mounted configuration/authentication flags and -validate are incompatible")
		}
		if len(validation.IsDNS1123Label(o.scaleSet)) != 0 || len(validation.IsDNS1123Label(o.namespace)) != 0 {
			return o, errors.New("-scale-set and -namespace must be valid Kubernetes names")
		}
	} else if o.configPath == "" || o.namespace != "" {
		return o, errors.New("provide either -config or both -scale-set and -namespace")
	}
	return o, nil
}

func readConfig(path string) (operator.Config, error) {
	var cfg operator.Config
	file, err := os.Open(path)
	if err != nil {
		return cfg, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 4<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return cfg, errors.New("configuration must contain one JSON object")
	}
	return cfg, cfg.Validate()
}

func githubClient(o options, cfg operator.Config) (*scaleset.Client, error) {
	system := scaleset.SystemInfo{System: "runnerscout", Version: "development"}
	var client *scaleset.Client
	var err error
	if o.appID != "" || o.installationID != 0 || o.appKey != "" {
		if o.appID == "" || o.installationID <= 0 || o.appKey == "" || o.tokenPath != "" {
			return nil, errors.New("complete GitHub App settings required; cannot combine App and PAT")
		}
		key, readErr := os.ReadFile(o.appKey)
		if readErr != nil || len(key) == 0 {
			return nil, errors.New("cannot read GitHub App private-key file")
		}
		client, err = scaleset.NewClientWithGitHubApp(scaleset.ClientWithGitHubAppConfig{GitHubConfigURL: cfg.GitHubURL, GitHubAppAuth: scaleset.GitHubAppAuth{ClientID: o.appID, InstallationID: o.installationID, PrivateKey: string(key)}, SystemInfo: system})
	} else {
		if o.tokenPath == "" {
			return nil, errors.New("mounted GitHub App credentials or token file required")
		}
		token, readErr := os.ReadFile(o.tokenPath)
		if readErr != nil || strings.TrimSpace(string(token)) == "" {
			return nil, errors.New("cannot read GitHub token file")
		}
		client, err = scaleset.NewClientWithPersonalAccessToken(scaleset.NewClientWithPersonalAccessTokenConfig{GitHubConfigURL: cfg.GitHubURL, PersonalAccessToken: strings.TrimSpace(string(token)), SystemInfo: system})
	}
	if err != nil {
		return nil, errors.New("GitHub client initialization failed")
	}
	return client, nil
}

// githubJobsClient builds the client internal/recovery's interruption-retry
// composition needs to look up REST evidence and request reruns. Compile
// already refuses Retry.Enabled for App authentication, so this only ever
// needs a PAT; nil is returned when retries are not configured at all.
func githubJobsClient(o options, cfg operator.Config) (*githubjobs.Client, error) {
	if !cfg.Retry.Enabled {
		return nil, nil
	}
	if o.tokenPath == "" || o.appID != "" || o.installationID != 0 || o.appKey != "" {
		return nil, errors.New("retry execution requires PAT authentication")
	}
	token, err := os.ReadFile(o.tokenPath)
	if err != nil || strings.TrimSpace(string(token)) == "" {
		return nil, errors.New("cannot read GitHub token file")
	}
	return &githubjobs.Client{Token: strings.TrimSpace(string(token))}, nil
}

// awsPricesObserver builds a live EC2 spot price observer from the already
// credentialed "aws" provider entry, mirroring githubJobsClient: nil unless
// the operator explicitly opts in via cfg.AWSPriceRefresh, since this makes
// live AWS API calls on every admission cycle. It reuses the AWS credential
// scope controller already resolved for provisioning rather than deriving
// its own, so provisioning and price observation always share one identity.
func awsPricesObserver(controller *operator.Operator, cfg operator.Config) (*provider.AWSSpotPrices, error) {
	if !cfg.AWSPriceRefresh {
		return nil, nil
	}
	command, ok := controller.Controller.Providers["aws"].(*provider.Command)
	if !ok || command.AWS == nil {
		return nil, errors.New(`AWS price refresh requires a configured "aws" provider`)
	}
	return command.AWS.SpotPrices(), nil
}

// azurePricesObserver builds a live Azure Retail Prices spot observer,
// mirroring awsPricesObserver: nil unless the operator explicitly opts in
// via cfg.AzurePriceRefresh. Unlike AWS, the Retail Prices API needs no
// credentials at all, so the "azure" provider entry here is required only
// to confirm Azure is actually a configured provider, never to supply
// credentials to the price call itself.
func azurePricesObserver(controller *operator.Operator, cfg operator.Config) (*provider.AzureSpotPrices, error) {
	if !cfg.AzurePriceRefresh {
		return nil, nil
	}
	command, ok := controller.Controller.Providers["azure"].(*provider.Command)
	if !ok || command.Azure == nil {
		return nil, errors.New(`Azure price refresh requires a configured "azure" provider`)
	}
	return command.Azure.SpotPrices(), nil
}

// azureInterruptionsObserver builds a live internal/azurequeue.Client from
// the already-credentialed "azure" provider entry, mirroring
// awsPricesObserver/azurePricesObserver: nil unless the operator explicitly
// opts in via cfg.AzureInterruptionQueueURL, since this makes live Azure
// Storage Queue calls on every Tick cycle. It reuses AzureSDK's own already-
// resolved credential chain via InterruptionQueue rather than resolving a
// separate one - see AzureSDK.InterruptionQueue's doc comment for why.
func azureInterruptionsObserver(controller *operator.Operator, cfg operator.Config) (*azurequeue.Client, error) {
	if cfg.AzureInterruptionQueueURL == "" {
		return nil, nil
	}
	command, ok := controller.Controller.Providers["azure"].(*provider.Command)
	if !ok || command.Azure == nil {
		return nil, errors.New(`Azure interruption delivery requires a configured "azure" provider`)
	}
	return command.Azure.InterruptionQueue(cfg.AzureInterruptionQueueURL)
}

// wireGuardPeersHandler builds the narrow, allocation-scoped WireGuard
// peer-poll endpoint (docs/networking-peer-model.md's "Revocation" section;
// internal/health.WireGuardPeersHandler) for one scale set, from exactly the
// same (namespace, owner-name) pair internal/operator.New already uses to
// build its own internal/state.Kubernetes Store. Unlike
// awsPricesObserver/azurePricesObserver/azureInterruptionsObserver, this is
// never gated behind an opt-in Config flag: it costs nothing beyond one
// idle *state.Kubernetes value until a request actually arrives, and it is
// a complete no-op for every allocation whose NetworkProfile is "" - see
// WireGuardPeersHandler.ServeHTTP's own eligibility check. There is also no
// startup misconfiguration to error on: namespace and name are already
// validated (parseOptions's DNS1123 checks for the CRD path,
// operator.Config.Validate for the mounted-config path) before this is
// ever called, unlike the price/interruption observers above, which can
// fail because they each depend on one specific provider actually being
// configured.
func wireGuardPeersHandler(client kubernetes.Interface, namespace, name string) http.Handler {
	return &health.WireGuardPeersHandler{Store: &state.Kubernetes{Maps: client.CoreV1().ConfigMaps(namespace), Owner: name}}
}

// withHealth serves /healthz, /readyz and (when wireGuardPeers is non-nil)
// the WireGuard peer-poll endpoint for the lifetime of run. wireGuardPeers
// must be supplied here, before status.Handler() builds its mux below,
// rather than assigned onto status from inside run: Status.Handler builds
// its route table exactly once, so setting status.WireGuardPeers any later
// would silently never register the route.
func withHealth(ctx context.Context, address string, wireGuardPeers http.Handler, run func(context.Context, *health.Status) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	status := health.Status{WireGuardPeers: wireGuardPeers}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("health listener: %w", err)
	}
	server := &http.Server{Handler: status.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	healthErrors := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			healthErrors <- err
			cancel()
		}
	}()
	defer func() { _ = server.Close(); <-done }()
	err = run(ctx, &status)
	status.SetReady(false)
	select {
	case healthErr := <-healthErrors:
		return errors.Join(err, fmt.Errorf("health server: %w", healthErr))
	default:
	}
	return err
}

func run(args []string) error {
	o, err := parseOptions(args)
	if err != nil {
		return err
	}
	var cfg operator.Config
	if o.configPath != "" {
		cfg, err = readConfig(o.configPath)
		if err != nil {
			return err
		}
		if o.validate {
			fmt.Println("configuration valid; no external operations performed")
			return nil
		}
	}
	kc, err := rest.InClusterConfig()
	if err != nil {
		return errors.New("in-cluster Kubernetes configuration required")
	}
	kc.Timeout = 30 * time.Second
	client, err := kubernetes.NewForConfig(kc)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if o.checkCRD || o.checkUninstall {
		dc, err := dynamic.NewForConfig(kc)
		if err != nil {
			return err
		}
		checkCtx, stop := context.WithTimeout(ctx, time.Minute)
		defer stop()
		controller := &configapi.Runtime{Namespace: o.namespace, Name: o.scaleSet, Client: client, Dynamic: dc}
		check := controller.CheckConfiguration
		if o.checkUninstall {
			check = controller.CheckUninstall
		}
		return runCheck(checkCtx, check)
	}
	// namespace/name identify the one internal/state.Kubernetes ConfigMap
	// scope this scale set's allocations live in, under either configuration
	// path - exactly the (Namespace, Name) pair operator.New itself uses to
	// build its own Store. Known upfront in both branches below, which is
	// what lets wireGuardPeersHandler be built before withHealth serves any
	// request (see withHealth's own doc comment for why that ordering
	// matters).
	namespace, name := o.namespace, o.scaleSet
	if o.configPath != "" {
		namespace, name = cfg.Namespace, cfg.Name
	}
	return withHealth(ctx, o.healthAddress, wireGuardPeersHandler(client, namespace, name), func(ctx context.Context, status *health.Status) error {
		if o.scaleSet != "" {
			dynamicClient, err := dynamic.NewForConfig(kc)
			if err != nil {
				return err
			}
			controller := &configapi.Runtime{Namespace: o.namespace, Name: o.scaleSet, Client: client, Dynamic: dynamicClient, Readiness: status.SetReady}
			return controller.Run(ctx)
		}
		github, err := githubClient(o, cfg)
		if err != nil {
			return err
		}
		jobsClient, err := githubJobsClient(o, cfg)
		if err != nil {
			return err
		}
		controller, cleanup, err := operator.NewWithCredentials(cfg, client, github, nil)
		if err != nil {
			return err
		}
		if jobsClient != nil {
			controller.GitHubJobs = jobsClient
		}
		awsPrices, err := awsPricesObserver(controller, cfg)
		if err != nil {
			return err
		}
		if awsPrices != nil {
			controller.AWSPrices = awsPrices
		}
		azurePrices, err := azurePricesObserver(controller, cfg)
		if err != nil {
			return err
		}
		if azurePrices != nil {
			controller.AzurePrices = azurePrices
		}
		azureInterruptions, err := azureInterruptionsObserver(controller, cfg)
		if err != nil {
			return err
		}
		if azureInterruptions != nil {
			controller.AzureInterruptions = azureInterruptions
		}
		controller.Readiness = status.SetReady
		err = controller.Run(ctx)
		if cleanupErr := cleanup(); cleanupErr != nil {
			return errors.Join(err, errors.New("provider credential cache cleanup incomplete"))
		}
		return err
	})
}

func runCheck(ctx context.Context, check func(context.Context) error) error {
	err := check(ctx)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		// A controller can stop normally on cancellation. A one-shot safety
		// check must not report success unless its verification completed.
		return errors.Join(errors.New("Kubernetes safety check did not complete successfully"), err)
	}
	return nil
}

func main() {
	if err := run(os.Args[1:]); !benignShutdown(err) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func benignShutdown(err error) bool {
	if err == nil || err == flag.ErrHelp || err == context.Canceled {
		return true
	}
	// errors.Is alone would hide a cleanup failure joined with cancellation.
	// Every leaf must be benign before the process can report a clean exit.
	switch wrapped := err.(type) {
	case interface{ Unwrap() []error }:
		children := wrapped.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !benignShutdown(child) {
				return false
			}
		}
		return true
	case interface{ Unwrap() error }:
		child := wrapped.Unwrap()
		return child != nil && benignShutdown(child)
	default:
		return false
	}
}
