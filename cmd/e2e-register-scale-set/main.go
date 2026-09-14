// Command e2e-register-scale-set registers (or deletes) one real GitHub
// Actions runner scale set against a real repository or organization, using
// the same github.com/actions/scaleset client the production controller
// consumes read-only (internal/operator.Operator's Config.ScaleSetID).
// Nothing in internal/operator or cmd/runnerscout creates a scale set - this
// is the one-time (or scripted) setup step issue #80 asks for, kept as its
// own small program rather than a flag on cmd/runnerscout so that the
// production controller binary never gains a code path that creates or
// deletes billable/durable GitHub-side objects.
//
// Idempotent-safe by construction: it always looks up an existing scale set
// by (runner group, name) via Client.GetRunnerScaleSet before creating one,
// so re-running this against an already-registered name is a no-op that
// reports the existing ID rather than a duplicate. -delete is symmetric:
// deleting an already-absent name is also a no-op, never an error - the
// same "confirm state, then converge" shape as the rest of this codebase's
// cleanup paths (see docs/architecture.md's "Recovery and cleanup").
//
// This program never runs itself against real GitHub - see
// docs/e2e-qualification.md. It is invoked by an operator, by hand or via
// tools/e2e/register-scale-set.sh, only after deliberately choosing to spend
// real time/money on the full tools/e2e/ harness.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/actions/scaleset"
	"github.com/tsouza/runnerscout/internal/version"
)

type options struct {
	githubURL, name, runnerGroup       string
	tokenPath, appClientID, appKeyPath string
	appInstallationID                  int64
	deleteScaleSet                     bool
	evidencePath                       string
}

func parseOptions(args []string) (options, error) {
	var o options
	flags := flag.NewFlagSet("e2e-register-scale-set", flag.ContinueOnError)
	flags.StringVar(&o.githubURL, "github-url", "", "GitHub.com organization or repository HTTPS URL that owns the scale set")
	flags.StringVar(&o.name, "name", "", "runner scale set name (also becomes the runs-on label GitHub exposes for it)")
	flags.StringVar(&o.runnerGroup, "runner-group", "Default", "name of the existing GitHub runner group to register the scale set under")
	flags.StringVar(&o.tokenPath, "github-token-file", "", "mounted GitHub PAT file (mutually exclusive with -github-app-*)")
	flags.StringVar(&o.appClientID, "github-app-client-id", "", "GitHub App client ID")
	flags.Int64Var(&o.appInstallationID, "github-app-installation-id", 0, "GitHub App installation ID")
	flags.StringVar(&o.appKeyPath, "github-app-key-file", "", "mounted GitHub App private-key file")
	flags.BoolVar(&o.deleteScaleSet, "delete", false, "delete the named scale set instead of registering it (no-op if already absent)")
	flags.StringVar(&o.evidencePath, "evidence-file", "", "optional path to write a JSON record of the action taken")
	if err := flags.Parse(args); err != nil {
		return o, err
	}
	if flags.NArg() != 0 {
		return o, errors.New("unexpected positional arguments")
	}
	if o.githubURL == "" || o.name == "" || o.runnerGroup == "" {
		return o, errors.New("-github-url, -name and -runner-group are required")
	}
	appMode := o.appClientID != "" || o.appInstallationID != 0 || o.appKeyPath != ""
	patMode := o.tokenPath != ""
	if appMode == patMode {
		return o, errors.New("exactly one of -github-token-file or the complete -github-app-* trio is required")
	}
	if appMode && (o.appClientID == "" || o.appInstallationID <= 0 || o.appKeyPath == "") {
		return o, errors.New("complete GitHub App settings required: -github-app-client-id, -github-app-installation-id, -github-app-key-file")
	}
	return o, nil
}

// buildClient mirrors cmd/runnerscout/main.go's githubClient: same two
// mutually exclusive auth modes, same "read the mounted file, never accept a
// credential value as a flag" discipline.
func buildClient(o options) (*scaleset.Client, error) {
	system := scaleset.SystemInfo{System: "runnerscout-e2e-register-scale-set", Version: version.Version}
	if o.tokenPath != "" {
		token, err := os.ReadFile(o.tokenPath)
		if err != nil || strings.TrimSpace(string(token)) == "" {
			return nil, errors.New("cannot read GitHub token file")
		}
		client, err := scaleset.NewClientWithPersonalAccessToken(scaleset.NewClientWithPersonalAccessTokenConfig{GitHubConfigURL: o.githubURL, PersonalAccessToken: strings.TrimSpace(string(token)), SystemInfo: system})
		if err != nil {
			return nil, fmt.Errorf("GitHub client initialization failed: %w", err)
		}
		return client, nil
	}
	key, err := os.ReadFile(o.appKeyPath)
	if err != nil || len(key) == 0 {
		return nil, errors.New("cannot read GitHub App private-key file")
	}
	client, err := scaleset.NewClientWithGitHubApp(scaleset.ClientWithGitHubAppConfig{GitHubConfigURL: o.githubURL, GitHubAppAuth: scaleset.GitHubAppAuth{ClientID: o.appClientID, InstallationID: o.appInstallationID, PrivateKey: string(key)}, SystemInfo: system})
	if err != nil {
		return nil, fmt.Errorf("GitHub client initialization failed: %w", err)
	}
	return client, nil
}

type result struct {
	Action        string `json:"action"`
	Name          string `json:"name"`
	RunnerGroupID int    `json:"runnerGroupID,omitempty"`
	ScaleSetID    int    `json:"scaleSetID,omitempty"`
	At            string `json:"at"`
}

func writeEvidence(path string, r result) error {
	if path == "" {
		return nil
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

func run(ctx context.Context, o options, client *scaleset.Client, stdout, stderr io.Writer) (result, error) {
	group, err := client.GetRunnerGroupByName(ctx, o.runnerGroup)
	if err != nil {
		return result{}, fmt.Errorf("resolve runner group %q: %w", o.runnerGroup, err)
	}
	existing, err := client.GetRunnerScaleSet(ctx, group.ID, o.name)
	if err != nil {
		return result{}, fmt.Errorf("look up existing scale set %q: %w", o.name, err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if o.deleteScaleSet {
		if existing == nil {
			fmt.Fprintf(stderr, "scale set %q not found under runner group %q; nothing to delete\n", o.name, o.runnerGroup)
			return result{Action: "absent", Name: o.name, RunnerGroupID: group.ID, At: now}, nil
		}
		if err := client.DeleteRunnerScaleSet(ctx, existing.ID); err != nil {
			return result{}, fmt.Errorf("delete scale set %d (%q): %w", existing.ID, o.name, err)
		}
		fmt.Fprintf(stderr, "deleted scale set %d (%q) under runner group %q\n", existing.ID, o.name, o.runnerGroup)
		return result{Action: "deleted", Name: o.name, RunnerGroupID: group.ID, ScaleSetID: existing.ID, At: now}, nil
	}
	if existing != nil {
		fmt.Fprintf(stderr, "scale set %q already registered as id %d under runner group %q; not creating a duplicate\n", o.name, existing.ID, o.runnerGroup)
		fmt.Fprintln(stdout, existing.ID)
		return result{Action: "existing", Name: o.name, RunnerGroupID: group.ID, ScaleSetID: existing.ID, At: now}, nil
	}
	created, err := client.CreateRunnerScaleSet(ctx, &scaleset.RunnerScaleSet{Name: o.name, RunnerGroupID: group.ID})
	if err != nil {
		return result{}, fmt.Errorf("create scale set %q: %w", o.name, err)
	}
	fmt.Fprintf(stderr, "created scale set %d (%q) under runner group %q\n", created.ID, o.name, o.runnerGroup)
	fmt.Fprintln(stdout, created.ID)
	return result{Action: "created", Name: o.name, RunnerGroupID: group.ID, ScaleSetID: created.ID, At: now}, nil
}

func main() {
	o, err := parseOptions(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	client, err := buildClient(o)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	r, err := run(ctx, o, client, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := writeEvidence(o.evidencePath, r); err != nil {
		fmt.Fprintln(os.Stderr, fmt.Errorf("write evidence file: %w", err))
		os.Exit(1)
	}
}
