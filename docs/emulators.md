# Cloud API emulators

`make emulators` (`tools/emulators.py`) runs the AWS and Azure provider
adapters (`internal/provider`) against isolated, credential-free Docker API
emulators on the local Docker host - no cloud account, no real cloud
resources, no Docker socket mounted into any emulator container.

| Provider | Image | Covers |
|---|---|---|
| AWS | `motoserver/moto` + `tools/fixtures/moto_server.py` (client-token/interface-tag extensions) | EC2 create/observe/delete state transitions, including lost-response and cleanup-retention behavior |
| AWS | `ministackorg/ministack` | Unsupported-image rejection retaining cleanup obligations |
| Azure | `floci/floci-az` | ARM VM/disk/NIC create, unsupported-image rejection, disk-binding and NIC-identity handling |

**GCP has no emulator tier.** `internal/provider`'s GCP adapter
(`gcp.go`, `gcp_sdk.go`) is instead tested by:

- `internal/provider/gcp_sdk_test.go` and sibling files, using
  `httptest.NewTLSServer` fixtures directly in the normal `go test` suite -
  no Docker, no separate `make` target, always run.
- Real-cloud qualification (`docs/qualification-real-cloud.md`'s
  `provider: gcp` branch of `.github/workflows/qualify.yml`, `make
  qualify`), which creates and tears down a real, billed GCP VM.

See [emulators.background.md](emulators.background.md) for why no
Docker-based GCP emulator tier exists between those two.
