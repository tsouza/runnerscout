# WireGuard VM-boot integration (reference)

This directory is a reference for the one piece `docs/operations.md`'s
wireguard-mode section used to say an operator had to build entirely alone:
getting `cmd/runnerscout-wireguard-agent` actually running on a wireguard-mode
runner VM at boot. It is example material to copy into your own runner image
build, not something this repository installs for you.

**This does not make wireguard mode usable end-to-end.** Overlay IP address
allocation (`lifecycle.Allocation.WireGuardOverlayAddress`) is a separate,
still-open gap tracked on
[issue #16](https://github.com/tsouza/runnerscout/issues/16): no code path in
this repository assigns it today, so every real allocation's cloud-init
payload has an empty `OverlayAddress`, and the agent's `LoadPayload` step
refuses to start on it (see `internal/wireguard/agent/agent.go`). Wiring up
this systemd unit correctly gets the agent running and ready; it does not
change that outcome until the overlay-allocation gap above is closed. See
`docs/operations.md`'s wireguard-mode section for the authoritative statement
of what is and is not usable today.

## What's here

- `runnerscout-wireguard-agent.service` — a systemd unit that starts the
  agent binary once the cloud-init-delivered payload file exists, running as
  the same unprivileged `runner` user the rest of this repository's
  cloud-init scripts already use.

## Building the binary

Build `cmd/runnerscout-wireguard-agent` as its own binary — it is
deliberately never imported by `cmd/runnerscout`, so building it does not
pull the controller's own dependency graph:

```sh
CGO_ENABLED=0 go build -o runnerscout-wireguard-agent ./cmd/runnerscout-wireguard-agent
```

## Installing into a runner image

1. Copy the built binary to `/usr/local/bin/runnerscout-wireguard-agent` in
   your runner image (matching the path this unit's `ExecStart` uses; change
   both if you place it elsewhere).
2. Copy `runnerscout-wireguard-agent.service` to
   `/etc/systemd/system/runnerscout-wireguard-agent.service` in the image.
3. Enable the unit at image build time so it activates on every boot without
   a first-boot script having to do it:

   ```sh
   systemctl enable runnerscout-wireguard-agent.service
   ```

No further installation step is needed at runtime: the unit's own
`ConditionPathExists=/run/runnerscout/wireguard.json` makes it inert on a VM
whose allocation was never provisioned for wireguard mode, and a no-op (skip,
not fail) is what `systemctl status` shows in that case. That path — where
`provider.BootstrapWithWireGuard`'s cloud-init script actually writes the
payload — is the one piece of the wiring you must not change independently
of `internal/provider/command.go`; the unit and the flag default in
`cmd/runnerscout-wireguard-agent/main.go` both hard-code it for exactly that
reason.

## Verifying on a booted VM

```sh
systemctl status runnerscout-wireguard-agent.service
journalctl -u runnerscout-wireguard-agent.service
```

Given the overlay-allocation gap above, expect to see the unit fail its
`LoadPayload` step and retry (`Restart=on-failure`) rather than run
successfully, until that gap is closed.
