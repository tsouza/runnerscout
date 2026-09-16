#!/usr/bin/env python3
"""Runs every TLA+ spec/config pair under spec/ against TLC and checks each
one's actual outcome (clean vs. invariant violation) against its declared
expectation below. Not wired into CI: these are hand-written models of
runnerscout's own domain logic, not a check against the real Go code, so a
clean run here is not a substitute for `make test` - see spec/README.md.

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

# (config file, module file, expect_violation, invariant this run is about)
#
# expect_violation=True entries are deliberate counterexample/witness
# configs - they exist to prove a gap is real and reachable (or, for
# *_pending_only/*_buggy/*_unknown_leak, to reproduce an already-fixed
# historical bug or an explicitly-unsafe design), not to be fixed. See
# spec/README.md's coverage matrix for what each one means.
RUNS = [
    ("RunnerRegistration_pending_only.cfg", "RunnerRegistration.tla", True,
     "PR #163 as it shipped: only Pending->TimedOut deregisters, age-only pruning"),
    ("RunnerRegistration_all_four.cfg", "RunnerRegistration.tla", False,
     "PR #168 + the current re-verifying prune design, no unknown leak"),
    ("RunnerRegistration_unknown_leak.cfg", "RunnerRegistration.tla", True,
     "#168 plus an unpatched fifth path plus #161's original age-only pruning"),
    ("RunnerRegistration_reverifying_prune.cfg", "RunnerRegistration.tla", False,
     "same unpatched fifth path, but pruning re-verifies before deleting"),
    ("TickConcurrency_buggy.cfg", "TickConcurrency.tla", True,
     "pre-#157: an early return could release o.mu with goroutines running"),
    ("TickConcurrency_fixed.cfg", "TickConcurrency.tla", False,
     "PR #157: every path out of Tick's loop joins before releasing o.mu"),
    ("BudgetAdmission_ceiling.cfg", "BudgetAdmission.tla", False,
     "budget.go's admission-gating arithmetic never exceeds the ceiling"),
    ("BudgetAdmission_lockout_witness.cfg", "BudgetAdmission.tla", True,
     "finding #6: an all-interrupted day can still show the ceiling as full"),
    ("RetryBound_max1.cfg", "RetryBound.tla", False,
     "retry.go's bounded-rerun protocol respects MaxRetries=1"),
    ("RetryBound_max2.cfg", "RetryBound.tla", False,
     "retry.go's bounded-rerun protocol respects MaxRetries=2"),
    ("RetryBound_max3.cfg", "RetryBound.tla", False,
     "retry.go's bounded-rerun protocol respects MaxRetries=3"),
]

VIOLATION_RE = re.compile(r"^Error: Invariant (\S+) is violated\.", re.MULTILINE)
CLEAN_RE = re.compile(r"Model checking completed\. No error has been found\.")


def ensure_jar() -> pathlib.Path:
    if JAR_PATH.exists():
        return JAR_PATH
    TOOLS_DIR.mkdir(parents=True, exist_ok=True)
    print(f"fetching {JAR_URL} -> {JAR_PATH}", file=sys.stderr)
    urllib.request.urlretrieve(JAR_URL, JAR_PATH)
    return JAR_PATH


def run_one(jar: pathlib.Path, cfg: str, module: str) -> tuple[Optional[bool], str]:
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
    output = result.stdout + result.stderr
    violated = VIOLATION_RE.search(output)
    clean = CLEAN_RE.search(output)
    if violated:
        return True, violated.group(1)
    if clean:
        return False, ""
    return None, output  # neither pattern matched - treat as a hard failure


def main() -> int:
    jar = ensure_jar()
    failures = 0
    for cfg, module, expect_violation, description in RUNS:
        outcome, detail = run_one(jar, cfg, module)
        if outcome is None:
            print(f"FAIL  {cfg}: TLC produced no recognizable result\n{detail}")
            failures += 1
            continue
        ok = outcome == expect_violation
        status = "ok" if ok else "FAIL"
        shape = f"violates {detail}" if outcome else "clean"
        print(f"{status}  {cfg} ({module}): {shape} - {description}")
        if not ok:
            failures += 1
    print()
    if failures:
        print(f"{failures} of {len(RUNS)} runs did not match their declared expectation")
        return 1
    print(f"all {len(RUNS)} runs matched their declared expectation")
    return 0


if __name__ == "__main__":
    sys.exit(main())
