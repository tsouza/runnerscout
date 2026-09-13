package main

import (
	"testing"
	"time"

	"github.com/tsouza/runnerscout/internal/wireguard/agent"
)

func TestParseOptionsDefaults(t *testing.T) {
	o, err := parseOptions(nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.payloadPath != "/run/runnerscout/wireguard.json" {
		t.Fatalf("unexpected default payload path: %q", o.payloadPath)
	}
	if o.pollInterval != agent.DefaultPollInterval {
		t.Fatalf("unexpected default poll interval: %v", o.pollInterval)
	}
}

func TestParseOptionsOverrides(t *testing.T) {
	o, err := parseOptions([]string{"-payload", "/tmp/fixture.json", "-poll-interval", "5s"})
	if err != nil {
		t.Fatal(err)
	}
	if o.payloadPath != "/tmp/fixture.json" || o.pollInterval != 5*time.Second {
		t.Fatalf("unexpected options: %+v", o)
	}
}

func TestParseOptionsRejectsPositionalArguments(t *testing.T) {
	if _, err := parseOptions([]string{"extra"}); err == nil {
		t.Fatal("expected an error for a positional argument")
	}
}

func TestParseOptionsRejectsEmptyPayloadPath(t *testing.T) {
	if _, err := parseOptions([]string{"-payload", ""}); err == nil {
		t.Fatal("expected an error for an empty -payload")
	}
}

func TestParseOptionsRejectsNonPositivePollInterval(t *testing.T) {
	if _, err := parseOptions([]string{"-poll-interval", "0s"}); err == nil {
		t.Fatal("expected an error for a non-positive -poll-interval")
	}
	if _, err := parseOptions([]string{"-poll-interval", "-1s"}); err == nil {
		t.Fatal("expected an error for a negative -poll-interval")
	}
}
