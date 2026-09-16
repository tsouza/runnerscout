#!/usr/bin/env python3
"""Runs every TLA+ spec/config pair under spec/ against TLC and checks each
one's actual outcome (clean vs. invariant violation) against its declared
expectation below. Not wired into CI: these are hand-written models of
runnerscout's own domain logic, not a check against the real Go code, so a
clean run here is not a substitute for `make test` - see spec/README.md.

Self-contained: downloads tla2tools.jar into spec/.tools/ on first run if not
already present there.
"""
import pathlib
import re
import subprocess
import sys
import urllib.request

SPEC_DIR = pathlib.Path(__file__).resolve().parent.parent / "spec"
TOOLS_DIR = SPEC_DIR / ".tools"
JAR_PATH = TOOLS_DIR / "tla2tools.jar"
JAR_URL = "https://github.com/tlaplus/tlaplus/releases/latest/download/tla2tools.jar"

# (config file, module file, expect_violation, invariant this run is about)
#
# expect_violation=True entries are deliberate counterexample/witness
# configs - they exist to prove a gap is real and reachable (or, for
# *_buggy/_pending_only, to reproduce an already-fixed historical bug), not
# to be fixed. See spec/README.md's coverage matrix for what each one means.
RUNS = [
    ("RunnerRegistration_pending_only.cfg", "RunnerRegistration.tla", True,
     "PR #163 alone (pre-#167/#168): only Pending->TimedOut deregisters"),
    ("RunnerRegistration_all_four.cfg", "RunnerRegistration.tla", False,
     "PR #168: all four known Deleted/TimedOut sites deregister"),
    ("RunnerRegistration_unknown_leak.cfg", "RunnerRegistration.tla", True,
     "#168 plus an unpatched fifth path plus today's age-only #161 pruning"),
    ("RunnerRegistration_reconciliation.cfg", "RunnerRegistration.tla", False,
     "unpatched fifth path, but with reconciliation + checked-gated pruning"),
    ("TickConcurrency_buggy.cfg", "TickConcurrency.tla", True,
     "pre-#157: an early return could release o.mu with goroutines running"),
    ("TickConcurrency_fixed.cfg", "TickConcurrency.tla", False,
     "PR #157: every path out of Tick's loop joins before releasing o.mu"),
    ("BudgetAdmission_ceiling.cfg", "BudgetAdmission.tla", False,
     "budget.go's admission-gating arithmetic never exceeds the ceiling"),
    ("BudgetAdmission_lockout_witness.cfg", "BudgetAdmission.tla", True,
     "finding #6: an all-interrupted day can still show the ceiling as full"),
    ("RetryBound.cfg", "RetryBound.tla", False,
     "retry.go's bounded-rerun protocol respects MaxRetries"),
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


def run_one(jar: pathlib.Path, cfg: str, module: str) -> tuple[bool, str]:
    result = subprocess.run(
        ["java", "-jar", str(jar), "-deadlock", "-config", cfg, module],
        cwd=SPEC_DIR,
        capture_output=True,
        text=True,
        timeout=120,
    )
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
