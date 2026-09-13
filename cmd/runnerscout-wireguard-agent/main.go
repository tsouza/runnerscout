// Command runnerscout-wireguard-agent is the VM-side binary a wireguard-mode
// runner's boot process (a systemd unit or other init integration - not
// built here, see internal/wireguard/agent's package doc comment) would
// invoke: it loads the cloud-init-delivered
// internal/wireguard.CloudInitPayload, brings up a real WireGuard tunnel via
// internal/wireguard/tunnel.BringUp, and polls
// internal/health.WireGuardPeersHandler on a bounded interval to keep the
// tunnel's peer set converged with the controller's authoritative list. All
// of the actual logic lives in internal/wireguard/agent; this binary is
// thin flag/signal wiring around it.
//
// This is a separate binary from cmd/runnerscout, never imported by it, so
// that golang.zx2c4.com/wireguard/tun/netstack's gVisor dependency tree
// (pulled in transitively via internal/wireguard/agent ->
// internal/wireguard/tunnel) never reaches the main controller binary's own
// dependency graph or size - see internal/wireguard/tunnel's package doc
// comment for the same reasoning applied one layer down.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tsouza/runnerscout/internal/wireguard/agent"
)

type options struct {
	payloadPath  string
	pollInterval time.Duration
}

func parseOptions(args []string) (options, error) {
	var o options
	flags := flag.NewFlagSet("runnerscout-wireguard-agent", flag.ContinueOnError)
	flags.StringVar(&o.payloadPath, "payload", "/run/runnerscout/wireguard.json", "cloud-init-delivered WireGuard payload file (internal/wireguard.CloudInitPayload JSON) - see provider.BootstrapWithWireGuard for exactly where it is written")
	flags.DurationVar(&o.pollInterval, "poll-interval", agent.DefaultPollInterval, "how often to poll the controller's WireGuard peer-list endpoint")
	if err := flags.Parse(args); err != nil {
		return o, err
	}
	if flags.NArg() != 0 {
		return o, errors.New("unexpected positional arguments")
	}
	if o.payloadPath == "" {
		return o, errors.New("-payload is required")
	}
	if o.pollInterval <= 0 {
		return o, errors.New("-poll-interval must be positive")
	}
	return o, nil
}

func run(args []string) error {
	o, err := parseOptions(args)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return agent.Run(ctx, agent.Options{PayloadPath: o.payloadPath, PollInterval: o.pollInterval})
}

func main() {
	err := run(os.Args[1:])
	// A clean shutdown (SIGTERM/SIGINT, delivered as ctx cancellation) or
	// -h/-help must exit 0 - the same convention cmd/runnerscout's own
	// benignShutdown establishes for the sibling binary.
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, flag.ErrHelp) {
		return
	}
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
