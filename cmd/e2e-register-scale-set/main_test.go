package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestParseOptionsRequiresExactlyOneAuthMode(t *testing.T) {
	base := []string{"-github-url", "https://github.com/example/repo", "-name", "build"}

	if _, err := parseOptions(base); err == nil {
		t.Fatal("expected an error when neither auth mode is supplied")
	}

	patArgs := append(append([]string{}, base...), "-github-token-file", "/tmp/token")
	o, err := parseOptions(patArgs)
	if err != nil {
		t.Fatalf("PAT-only args should parse: %v", err)
	}
	if o.tokenPath != "/tmp/token" || o.appClientID != "" {
		t.Fatal("PAT mode should not populate App fields")
	}

	appArgs := append(append([]string{}, base...),
		"-github-app-client-id", "Iv1.abc",
		"-github-app-installation-id", "1",
		"-github-app-key-file", "/tmp/key.pem")
	o, err = parseOptions(appArgs)
	if err != nil {
		t.Fatalf("complete App args should parse: %v", err)
	}
	if o.tokenPath != "" {
		t.Fatal("App mode should not populate the token path")
	}

	bothArgs := append(append([]string{}, patArgs...), "-github-app-client-id", "Iv1.abc")
	if _, err := parseOptions(bothArgs); err == nil {
		t.Fatal("expected an error when both auth modes are supplied")
	}

	incompleteApp := append(append([]string{}, base...), "-github-app-client-id", "Iv1.abc")
	if _, err := parseOptions(incompleteApp); err == nil {
		t.Fatal("expected an error for an incomplete App auth trio")
	}
}

func TestParseOptionsRequiresGitHubURLAndName(t *testing.T) {
	if _, err := parseOptions([]string{"-name", "build", "-github-token-file", "/tmp/token"}); err == nil {
		t.Fatal("expected an error when -github-url is missing")
	}
	if _, err := parseOptions([]string{"-github-url", "https://github.com/example/repo", "-github-token-file", "/tmp/token"}); err == nil {
		t.Fatal("expected an error when -name is missing")
	}
}

func TestParseOptionsRejectsPositionalArguments(t *testing.T) {
	args := []string{"-github-url", "https://github.com/example/repo", "-name", "build", "-github-token-file", "/tmp/token", "extra"}
	if _, err := parseOptions(args); err == nil {
		t.Fatal("expected an error for an unexpected positional argument")
	}
}

func TestParseOptionsDefaultsRunnerGroupToDefault(t *testing.T) {
	o, err := parseOptions([]string{"-github-url", "https://github.com/example/repo", "-name", "build", "-github-token-file", "/tmp/token"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if o.runnerGroup != "Default" {
		t.Fatalf("expected default runner group 'Default', got %q", o.runnerGroup)
	}
}

func TestWriteEvidenceWritesReadableJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "evidence.json")
	r := result{Action: "created", Name: "build", RunnerGroupID: 1, ScaleSetID: 42, At: "2026-09-14T00:00:00Z"}
	if err := writeEvidence(path, r); err != nil {
		t.Fatalf("writeEvidence failed: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read evidence file: %v", err)
	}
	var got result
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("evidence file is not valid JSON: %v", err)
	}
	if got != r {
		t.Fatalf("round-tripped evidence %+v does not match original %+v", got, r)
	}
}

func TestWriteEvidenceNoopWhenPathEmpty(t *testing.T) {
	if err := writeEvidence("", result{Action: "existing"}); err != nil {
		t.Fatalf("expected no-op success for an empty path, got: %v", err)
	}
}
