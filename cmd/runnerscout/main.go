package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/actions/scaleset"
	"github.com/tsouza/runnerscout/internal/operator"
	"io"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

func run() error {
	configPath := flag.String("config", "", "JSON configuration file")
	validate := flag.Bool("validate", false, "validate configuration without contacting cloud or GitHub")
	tokenPath := flag.String("github-token-file", "", "mounted GitHub token file (never passed as a token argument)")
	appID := flag.String("github-app-client-id", "", "GitHub App client ID")
	installationID := flag.Int64("github-app-installation-id", 0, "GitHub App installation ID")
	appKey := flag.String("github-app-key-file", "", "mounted GitHub App private-key file")
	flag.Parse()
	if *configPath == "" {
		return errors.New("-config required")
	}
	f, e := os.Open(*configPath)
	if e != nil {
		return e
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 4<<20))
	d.DisallowUnknownFields()
	var cfg operator.Config
	if e = d.Decode(&cfg); e != nil {
		return e
	}
	var extra any
	if e = d.Decode(&extra); e != io.EOF {
		return errors.New("configuration must contain one JSON object")
	}
	if e = cfg.Validate(); e != nil {
		return e
	}
	if *validate {
		fmt.Println("configuration valid; no external operations performed")
		return nil
	}

	var github *scaleset.Client
	system := scaleset.SystemInfo{System: "runnerscout", Version: "development"}
	if *appID != "" || *installationID != 0 || *appKey != "" {
		if *tokenPath != "" || *appID == "" || *installationID <= 0 || *appKey == "" {
			return errors.New("provide all GitHub App settings, without a PAT")
		}
		key, readErr := os.ReadFile(*appKey)
		if readErr != nil || len(key) == 0 {
			return errors.New("cannot read GitHub App key file")
		}
		github, e = scaleset.NewClientWithGitHubApp(scaleset.ClientWithGitHubAppConfig{GitHubConfigURL: cfg.GitHubURL, GitHubAppAuth: scaleset.GitHubAppAuth{ClientID: *appID, InstallationID: *installationID, PrivateKey: string(key)}, SystemInfo: system})
	} else {
		if *tokenPath == "" {
			return errors.New("mounted GitHub App credentials or token file required")
		}
		token, readErr := os.ReadFile(*tokenPath)
		if readErr != nil || strings.TrimSpace(string(token)) == "" {
			return errors.New("cannot read GitHub token file")
		}
		github, e = scaleset.NewClientWithPersonalAccessToken(scaleset.NewClientWithPersonalAccessTokenConfig{GitHubConfigURL: cfg.GitHubURL, PersonalAccessToken: strings.TrimSpace(string(token)), SystemInfo: system})
	}
	if e != nil {
		return errors.New("GitHub client initialization failed")
	}

	kc, e := rest.InClusterConfig()
	if e != nil {
		return errors.New("in-cluster Kubernetes configuration required")
	}
	kc.Timeout = 30_000_000_000
	k, e := kubernetes.NewForConfig(kc)
	if e != nil {
		return e
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return operator.New(cfg, k, github).Run(ctx)
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
