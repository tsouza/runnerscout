#!/usr/bin/env python3
"""Reject transient files in tracked source; private ignored artifacts are allowed."""
from pathlib import Path, PurePosixPath
import subprocess

ROOT = Path(__file__).resolve().parents[1]
TRANSIENT_DIRS = {'work', 'evidence', 'outputs', '__pycache__'}
TRANSIENT_NAMES = {'records.json', 'development-history.jsonl', 'local-evidence.json',
                   'readiness.md', 'runtime-security.json'}


def violations(paths):
    bad = []
    for name in paths:
        path = PurePosixPath(name)
        lower = path.name.lower()
        if (TRANSIENT_DIRS.intersection(path.parts)
                or lower in TRANSIENT_NAMES
                or 'handoff' in lower
                or lower.endswith(('-plan.md', '.pyc'))):
            bad.append(name)
    return sorted(bad)


def main():
    tracked = subprocess.check_output(
        ['git', 'ls-files', '-z', '--cached', '--others', '--exclude-standard'],
        cwd=ROOT).decode().split('\0')
    # Pending deletions must not make a development worktree fail verification.
    present = [p for p in tracked if p and (ROOT / p).exists()]
    bad = violations(present)
    if bad:
        raise SystemExit('Transient source files:\n' + '\n'.join(bad))
    print('Source hygiene passed')


if __name__ == '__main__':
    main()
