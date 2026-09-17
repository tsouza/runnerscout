#!/usr/bin/env python3
"""Decides whether a commit on main is a release: does
charts/runnerscout/Chart.yaml's own appVersion at that commit already have a
matching vMAJOR.MINOR.PATCH git tag? If not, merging the chore(release) PR
that bumped it is what makes this commit a release - release.yml's own
`gate` job runs this before `preflight`/`goreleaser`.

Deliberately not the thing that decides *how* to bump a version (that is
prepare_release.py's job, run earlier, staged into a PR a human reviews) or
whether it is *safe* to publish (that is the existing, unchanged
release_preflight.py, run after this gate says yes). Three separate
concerns, three separate tools - mirroring cerberus's own split between a
version gate and a preflight gate, in this project's existing Python/
tools/ convention rather than its Node one.
"""
import argparse
import re
import subprocess
import sys
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parent.parent
VERSION_RE = re.compile(r'^\d+\.\d+\.\d+$')


def chart_app_version(chart_path: Path) -> str:
    document = yaml.safe_load(chart_path.read_text())
    version = document.get('appVersion')
    if not isinstance(version, str) or not VERSION_RE.fullmatch(version):
        raise ValueError(f'{chart_path} appVersion {version!r} is not a bare MAJOR.MINOR.PATCH string')
    return version


def tag_exists(tag: str, repo: Path) -> bool:
    result = subprocess.run(['git', 'tag', '-l', tag], cwd=repo, capture_output=True, text=True, check=True)
    return result.stdout.strip() == tag


def assess(chart_path: Path, repo: Path) -> dict:
    app_version = chart_app_version(chart_path)
    tag = 'v' + app_version
    publish = not tag_exists(tag, repo)
    return {'publish': publish, 'version': tag}


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument('--chart', default=str(ROOT / 'charts' / 'runnerscout' / 'Chart.yaml'))
    parser.add_argument('--repo', default=str(ROOT))
    args = parser.parse_args()
    try:
        result = assess(Path(args.chart), Path(args.repo))
    except (OSError, ValueError, yaml.YAMLError) as exc:
        print(f'release_gate: {exc}', file=sys.stderr)
        return 1
    print(f"publish={'true' if result['publish'] else 'false'}")
    print(f"version={result['version']}")
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
