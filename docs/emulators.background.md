# Cloud API emulators — background

## Why AWS and Azure have one and GCP doesn't

`tools/emulators.py`'s own module docstring states the design constraint
for this whole file: "no real credentials and no Docker socket mounts."
`moto`, `ministack` and `floci-az` all run as plain, unprivileged
containers under that constraint - none of them get `/var/run/docker.sock`,
and none needs it: they emulate the cloud provider's API surface as a pure
state machine, never by spinning up a real backing resource per emulated
VM.

No equivalent exists for GCP Compute Engine that respects the same
constraint, as of two candidates actually investigated:

- **`floci-gcp`** (`work/emulator-research/floci-gcp.md` has the full
  research) emulates Pub/Sub, Firestore, Datastore, Cloud Storage, Secret
  Manager, Cloud KMS, IAM, Cloud Run, Cloud Functions, GKE, Cloud SQL,
  BigQuery, Cloud Tasks, Cloud Scheduler, Cloud Monitoring, Service Usage,
  and Firebase Auth - a long list, but **Compute Engine is not on it,
  anywhere**. `internal/provider`'s GCP adapter calls exactly Compute
  Engine (`Instances.Insert`, `Disks`, `ZoneOperations` - see
  `docs/gcp-iam.md`'s permission list for the exact API surface). Wiring
  `floci-gcp` into `tools/emulators.py` would add Docker complexity for
  zero coverage of the code path it would exist to test.
- **`MiniSky`** (`github.com/qamarudeenm/minisky`) does cover Compute
  Engine, with first-class Long-Running-Operation handling that matches
  GCP's real `instances.insert -> zoneOperations.get` shape - the closest
  fit found. But it backs each emulated VM with a real Docker container
  ("actual containers doing actual work"), which needs the host Docker
  socket mounted into the emulator - the exact thing this file's own
  docstring rules out for every other provider here. It is also a much
  younger, less-established project than `moto` (a single-maintainer
  GitHub repo plus one blog post, versus an Apache-adjacent project used
  across the AWS tooling ecosystem for years), with no confirmed official
  Docker image tag or health-check endpoint at the time this was
  evaluated - not something to pull into an automated pipeline as an
  unvetted dependency with host Docker access on the strength of a web
  search alone.

## What would change this

Either a socket-free Compute Engine emulator reaching the maturity/trust
bar `moto` already clears, or a deliberate decision to accept the
Docker-socket exception for GCP specifically (weakening this file's
uniform isolation guarantee for one provider) with `MiniSky` - or
whichever tool is current at that point - vetted properly first (pinned
image, confirmed health endpoint, a maintained upstream). Until one of
those happens, GCP's adapter correctness relies on the fixture-test and
real-cloud-qualification tiers described in `emulators.md`, which is a
narrower guarantee than AWS/Azure get from also having a Docker-emulator
tier, but not a gap that adding `floci-gcp` would actually close.
