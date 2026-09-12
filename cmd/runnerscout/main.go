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
	if *tokenPath == "" {
		return errors.New("mounted GitHub token file required")
	}
	token, e := os.ReadFile(*tokenPath)
	if e != nil {
		return errors.New("cannot read token file")
	}
	if strings.TrimSpace(string(token)) == "" {
		return errors.New("empty GitHub token")
	}
	github, e := scaleset.NewClientWithPersonalAccessToken(scaleset.NewClientWithPersonalAccessTokenConfig{GitHubConfigURL: cfg.GitHubURL, PersonalAccessToken: strings.TrimSpace(string(token)), SystemInfo: scaleset.SystemInfo{System: "runnerscout", Version: "development"}})
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
