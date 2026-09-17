#!/usr/bin/env python3
"""Stages a release: computes the next version from the latest vMAJOR.MINOR.PATCH
tag, bumps charts/runnerscout/Chart.yaml's version/appVersion, renders a
CHANGELOG.md section from commit subjects since that tag, and writes a PR
body. Does not touch git branches, commits, or PRs itself -
.github/workflows/prepare-release.yml does that mechanical part with the
files this tool writes.

A human reviews and merges the resulting chore(release) PR; merging it is
what tools/release_gate.py later recognizes as a release-worthy commit (its
own appVersion has no matching tag yet) - see docs/releases.md.
"""
import argparse
import re
import subprocess
from pathlib import Path
from typing import Optional

ROOT = Path(__file__).resolve().parent.parent
CHART_PATH = ROOT / 'charts' / 'runnerscout' / 'Chart.yaml'
CHANGELOG_PATH = ROOT / 'CHANGELOG.md'
PR_BODY_PATH = ROOT / 'release-pr-body.md'

VERSION_RE = re.compile(r'^v(\d+)\.(\d+)\.(\d+)$')

# Maps a commit subject's leading `type:`/`type(scope):` prefix to one of a
# small fixed set of changelog sections. This project's own real commit
# history uses fix/docs/spec/mitigate/chore (checked against `git log`, not
# assumed) - a pragmatic mapping over what commits actually say here, not
# the strict conventional-commits vocabulary (feat/fix/docs/chore/refactor/
# test/ci/build/perf) a from-scratch project might use instead.
SECTION_BY_PREFIX = {
    'fix': 'Fixed', 'mitigate': 'Fixed',
    'feat': 'Added', 'spec': 'Added',
    'docs': 'Documentation',
    'chore': 'Chores',
    'refactor': 'Changed', 'perf': 'Changed',
}
SECTION_ORDER = ['Added', 'Fixed', 'Changed', 'Documentation', 'Chores', 'Other']
PREFIX_RE = re.compile(r'^([a-z]+)(\([a-z0-9_-]+\))?:\s*(.+)$')


def latest_tag(repo: Path) -> Optional[str]:
    result = subprocess.run(['git', 'tag', '-l', 'v*'], cwd=repo, capture_output=True, text=True, check=True)
    tags = [t for t in result.stdout.splitlines() if VERSION_RE.fullmatch(t)]
    if not tags:
        return None

    def key(tag: str) -> tuple:
        return tuple(int(part) for part in VERSION_RE.fullmatch(tag).groups())

    return max(tags, key=key)


def next_version(current: Optional[str], bump: str) -> str:
    if bump not in ('patch', 'minor', 'major'):
        raise ValueError(f'unknown bump level {bump!r}')
    if current is None:
        return 'v1.0.0' if bump == 'major' else 'v0.1.0'
    major, minor, patch = (int(p) for p in VERSION_RE.fullmatch(current).groups())
    if bump == 'major':
        major, minor, patch = major + 1, 0, 0
    elif bump == 'minor':
        minor, patch = minor + 1, 0
    else:
        patch += 1
    return f'v{major}.{minor}.{patch}'


def commit_subjects_since(tag: Optional[str], repo: Path) -> list:
    range_arg = f'{tag}..HEAD' if tag else 'HEAD'
    result = subprocess.run(['git', 'log', range_arg, '--format=%s'], cwd=repo, capture_output=True, text=True, check=True)
    return [line for line in result.stdout.splitlines() if line.strip()]


def categorize(subjects: list) -> dict:
    sections = {name: [] for name in SECTION_ORDER}
    for subject in subjects:
        match = PREFIX_RE.match(subject)
        if match:
            prefix, _scope, rest = match.groups()
            sections[SECTION_BY_PREFIX.get(prefix, 'Other')].append(rest)
        else:
            sections['Other'].append(subject)
    return sections


def render_changelog_section(version: str, sections: dict) -> str:
    lines = [f'## {version}', '']
    wrote_any = False
    for name in SECTION_ORDER:
        items = sections.get(name) or []
        if not items:
            continue
        wrote_any = True
        lines.append(f'### {name}')
        lines.extend(f'- {item}' for item in items)
        lines.append('')
    if not wrote_any:
        lines.append('No changes recorded.')
        lines.append('')
    return '\n'.join(lines).rstrip() + '\n'


def prepend_changelog(section: str, path: Path) -> None:
    header = '# Changelog\n\n'
    existing = path.read_text() if path.exists() else ''
    if existing.startswith(header):
        existing = existing[len(header):]
    path.write_text(header + section + (('\n' + existing) if existing else ''))


def bump_chart_version(new_version: str, path: Path) -> None:
    # A targeted line substitution, not a YAML parse+dump: PyYAML's default
    # dump reformats the whole document (key ordering, quoting, comments
    # dropped) - Chart.yaml has no comments today, but a full round-trip
    # would silently start discarding any added later. This only ever
    # touches the three lines it names.
    bare = new_version.lstrip('v')
    text = path.read_text()
    text, n1 = re.subn(r'(?m)^version:\s*.*$', f'version: {bare}', text, count=1)
    text, n2 = re.subn(r'(?m)^appVersion:\s*.*$', f'appVersion: {bare}', text, count=1)
    # The artifacthub.io/images annotation's embedded image tag - if this
    # isn't bumped alongside version/appVersion, Artifact Hub's rendered
    # chart page silently keeps pointing at a stale image tag forever after
    # the first release under this annotation.
    text, n3 = re.subn(r'(?m)^(\s*image: ghcr\.io/tsouza/runnerscout:)v.*$', rf'\g<1>{new_version}', text, count=1)
    if n1 != 1 or n2 != 1 or n3 != 1:
        raise ValueError(f'{path} does not have exactly one version:/appVersion:/artifacthub image line each')
    path.write_text(text)


def render_pr_body(version: str, sections: dict) -> str:
    return (
        render_changelog_section(version, sections)
        + '\n---\nOpened by `.github/workflows/prepare-release.yml` via '
        '`tools/prepare_release.py`. Merging this PR is what triggers the '
        'release - see docs/releases.md.\n'
    )


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument('--bump', choices=['patch', 'minor', 'major'], default='patch')
    parser.add_argument('--repo', default=str(ROOT))
    parser.add_argument('--chart', default=str(CHART_PATH))
    parser.add_argument('--changelog', default=str(CHANGELOG_PATH))
    parser.add_argument('--pr-body', default=str(PR_BODY_PATH))
    args = parser.parse_args()

    repo = Path(args.repo)
    current = latest_tag(repo)
    version = next_version(current, args.bump)
    subjects = commit_subjects_since(current, repo)
    sections = categorize(subjects)

    bump_chart_version(version, Path(args.chart))
    prepend_changelog(render_changelog_section(version, sections), Path(args.changelog))
    Path(args.pr_body).write_text(render_pr_body(version, sections))

    print(f'version={version}')
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
