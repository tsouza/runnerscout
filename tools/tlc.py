#!/usr/bin/env python3
"""Runs every TLA+ spec/config pair under spec/ against TLC and checks each
one's actual outcome against its declared expectation below: either a clean
run, or a specific named invariant violation - not just "some invariant
failed," since a config with multiple listed invariants could have the wrong
one break and still look superficially like a pass. Not wired into CI: these
are hand-written models of runnerscout's own domain logic, not a check
against the real Go code, so a clean run here is not a substitute for
`make test` - see spec/README.md.

Self-contained: downloads tla2tools.jar into spec/.tools/ on first run if not
already present there. The download is unpinned (tracks whatever TLC
currently ships as `latest`) and unverified (no checksum) - acceptable for a
locally-run, non-CI developer tool, but worth knowing if a run ever behaves
differently than expected: the TLC version itself may have changed underfoot.
"""
import pathlib
import re
import subprocess
import sys
import urllib.request
from typing import Optional

SPEC_DIR = pathlib.Path(__file__).resolve().parent.parent / "spec"
TOOLS_DIR = SPEC_DIR / ".tools"
JAR_PATH = TOOLS_DIR / "tla2tools.jar"
JAR_URL = "https://github.com/tlaplus/tlaplus/releases/latest/download/tla2tools.jar"

# (config file, module file, expected invariant violation or None, description)
#
# The third field is None for a config expected to run clean, or the exact
# invariant name TLC must report as violated - never a bare True/False, so a
# config listing multiple INVARIANTs (e.g. TypeOK plus a real property) can't
# silently "pass" by violating the wrong one.
#
# A named-violation entry is a deliberate counterexample/witness config - it
# exists to prove a gap is real and reachable (or, for *_pending_only/*_buggy/
# *_unknown_leak, to reproduce an already-fixed historical bug or an
# explicitly-unsafe design), not to be fixed. See spec/README.md's coverage
# matrix for what each one means.
RUNS = [
    ("RunnerRegistration_pending_only.cfg", "RunnerRegistration.tla", "NoOrphanedRegistration",
     "PR #163 as it shipped: only Pending->TimedOut deregisters, age-only pruning"),
    ("RunnerRegistration_all_four.cfg", "RunnerRegistration.tla", None,
     "PR #168 + the current re-verifying prune design, no unknown leak"),
    ("RunnerRegistration_unknown_leak.cfg", "RunnerRegistration.tla", "NoIrrecoverableOrphan",
     "#168 plus an unpatched fifth path plus #161's original age-only pruning"),
    ("RunnerRegistration_reverifying_prune.cfg", "RunnerRegistration.tla", None,
     "same unpatched fifth path, but pruning re-verifies before deleting"),
    ("TickConcurrency_buggy.cfg", "TickConcurrency.tla", "MuReleasedOnlyWhenIdle",
     "pre-#157: an early return could release o.mu with goroutines running"),
    ("TickConcurrency_fixed.cfg", "TickConcurrency.tla", None,
     "PR #157: every path out of Tick's loop joins before releasing o.mu"),
    ("BudgetAdmission_ceiling.cfg", "BudgetAdmission.tla", None,
     "budget.go's admission-gating arithmetic never exceeds the ceiling"),
    ("BudgetAdmission_lockout_witness.cfg", "BudgetAdmission.tla", "NeverFullyLockedOutWhileIdle",
     "finding #6: an all-interrupted day can still show the ceiling as full"),
    ("RetryBound_max1.cfg", "RetryBound.tla", None,
     "retry.go's bounded-rerun protocol respects MaxRetries=1"),
    ("RetryBound_max2.cfg", "RetryBound.tla", None,
     "retry.go's bounded-rerun protocol respects MaxRetries=2"),
    ("RetryBound_max3.cfg", "RetryBound.tla", None,
     "retry.go's bounded-rerun protocol respects MaxRetries=3"),
    ("AdmissionSlot_pre174.cfg", "AdmissionSlot.tla", "EveryTerminalIsReleasable",
     "issue #174 as it shipped in v1.2.0: TimedOut never released its slot"),
    ("AdmissionSlot_post175.cfg", "AdmissionSlot.tla", None,
     "PR #175: both terminal phases release their slot"),
    ("JITRequest_spacing_disabled.cfg", "JITRequest.tla", "NoJitBurst",
     "pre-#179: a concurrently-admitted batch can start JIT requests in the same slot"),
    ("JITRequest_spacing_enabled.cfg", "JITRequest.tla", None,
     "PR #179: the shared burst-1 limiter spaces JIT requests apart"),
    ("ExternalFailureVisibility_pre178.cfg", "ExternalFailureVisibility.tla", "FailureCauseVisible",
     "issue #176: a JIT failure was collapsed to the generic preparation sentinel"),
    ("ExternalFailureVisibility_cloud_swallow.cfg", "ExternalFailureVisibility.tla", "FailureCauseVisible",
     "extrapolated sibling: a cloud-create failure collapsed to the same generic sentinel"),
    ("ExternalFailureVisibility_fixed.cfg", "ExternalFailureVisibility.tla", None,
     "both external failure classes preserve their observable cause"),
]

VIOLATION_RE = re.compile(r"^Error: Invariant (\S+) is violated\.", re.MULTILINE)
CLEAN_RE = re.compile(r"Model checking completed\. No error has been found\.")


def ensure_jar() -> pathlib.Path:
    if JAR_PATH.exists():
        return JAR_PATH
    TOOLS_DIR.mkdir(parents=True, exist_ok=True)
    print(f"fetching {JAR_URL} -> {JAR_PATH}", file=sys.stderr)
    # Download to a sibling temp path and rename into place atomically, so an
    # interrupted download (Ctrl-C, network drop) never leaves a truncated
    # jar at JAR_PATH that a later run would treat as already-fetched and
    # silently try to use.
    partial = JAR_PATH.with_suffix(JAR_PATH.suffix + ".part")
    with urllib.request.urlopen(JAR_URL, timeout=60) as response, open(partial, "wb") as out:
        out.write(response.read())
    partial.rename(JAR_PATH)
    return JAR_PATH


def run_one(jar: pathlib.Path, cfg: str, module: str) -> tuple[Optional[str], str]:
    """Returns (violated_invariant_or_None, raw_output). The first element is
    a sentinel string "<clean>" when TLC reported no violation, or None when
    neither a violation nor a clean-completion message could be recognized in
    the output at all (a hard failure - unexpected TLC output shape, a crash,
    or a timeout)."""
    try:
        result = subprocess.run(
            # -deadlock disables TLC's deadlock check: every spec here is a
            # finite, intentionally-terminating protocol (e.g. Prune leaves
            # nothing enabled once every id is terminal), so the natural
            # "no next state" endpoint is expected, not a bug to report.
            # -cleanup removes TLC's own generated states/ metadata
            # directory afterward, so repeated runs don't leave it behind.
            ["java", "-jar", str(jar), "-deadlock", "-cleanup", "-config", cfg, module],
            cwd=SPEC_DIR,
            capture_output=True,
            text=True,
            timeout=120,
        )
    except subprocess.TimeoutExpired:
        return None, f"TLC did not finish within the 120s timeout for {cfg}"
    except FileNotFoundError as exc:
        return None, f"could not run java (is a JRE installed and on PATH?): {exc}"
    output = result.stdout + result.stderr
    violated = VIOLATION_RE.search(output)
    if violated:
        return violated.group(1), output
    if CLEAN_RE.search(output):
        return "<clean>", output
    return None, output  # neither pattern matched - treat as a hard failure


def main() -> int:
    jar = ensure_jar()
    failures = 0
    for cfg, module, expect_invariant, description in RUNS:
        outcome, detail = run_one(jar, cfg, module)
        expected = expect_invariant or "<clean>"
        if outcome is None:
            print(f"FAIL  {cfg}: TLC produced no recognizable result\n{detail}")
            failures += 1
            continue
        ok = outcome == expected
        status = "ok" if ok else "FAIL"
        shape = "clean" if outcome == "<clean>" else f"violates {outcome}"
        print(f"{status}  {cfg} ({module}): {shape} - {description}")
        if not ok:
            print(f"      expected: {'clean' if expect_invariant is None else 'violates ' + expect_invariant}")
            failures += 1
    print()
    if failures:
        print(f"{failures} of {len(RUNS)} runs did not match their declared expectation")
        return 1
    print(f"all {len(RUNS)} runs matched their declared expectation")
    return 0


if __name__ == "__main__":
    sys.exit(main())
