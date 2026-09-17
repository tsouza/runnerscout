import subprocess
import tempfile
import unittest
from pathlib import Path

from release_gate import assess


def git_repo_with_tags(tmp: Path, tags: list[str]) -> Path:
    subprocess.run(['git', 'init', '--quiet'], cwd=tmp, check=True)
    subprocess.run(['git', 'config', 'user.email', 'test@example.com'], cwd=tmp, check=True)
    subprocess.run(['git', 'config', 'user.name', 'test'], cwd=tmp, check=True)
    (tmp / 'placeholder').write_text('x')
    subprocess.run(['git', 'add', '.'], cwd=tmp, check=True)
    subprocess.run(['git', 'commit', '--quiet', '-m', 'init'], cwd=tmp, check=True)
    for tag in tags:
        subprocess.run(['git', 'tag', tag], cwd=tmp, check=True)
    return tmp


def chart(tmp: Path, app_version: str) -> Path:
    path = tmp / 'Chart.yaml'
    path.write_text(f'apiVersion: v2\nname: runnerscout\nversion: {app_version}\nappVersion: {app_version}\n')
    return path


class ReleaseGate(unittest.TestCase):
    def test_unreleased_version_publishes(self):
        with tempfile.TemporaryDirectory() as raw:
            tmp = Path(raw)
            repo = git_repo_with_tags(tmp, tags=['v1.2.1'])
            result = assess(chart(tmp, '1.2.2'), repo)
            self.assertEqual(result, {'publish': True, 'version': 'v1.2.2'})

    def test_already_tagged_version_does_not_republish(self):
        with tempfile.TemporaryDirectory() as raw:
            tmp = Path(raw)
            repo = git_repo_with_tags(tmp, tags=['v1.2.1', 'v1.2.2'])
            result = assess(chart(tmp, '1.2.2'), repo)
            self.assertEqual(result, {'publish': False, 'version': 'v1.2.2'})

    def test_no_tags_at_all_still_publishes(self):
        with tempfile.TemporaryDirectory() as raw:
            tmp = Path(raw)
            repo = git_repo_with_tags(tmp, tags=[])
            result = assess(chart(tmp, '0.1.0'), repo)
            self.assertEqual(result, {'publish': True, 'version': 'v0.1.0'})

    def test_placeholder_app_version_does_not_publish(self):
        # "development" is Chart.yaml's own steady-state appVersion between
        # releases (unchanged since the chart's original commit) - an
        # ordinary push to main with no chore(release) bump pending must not
        # be treated as a release attempt, let alone fail the gate job.
        with tempfile.TemporaryDirectory() as raw:
            tmp = Path(raw)
            repo = git_repo_with_tags(tmp, tags=['v1.2.2'])
            result = assess(chart(tmp, 'development'), repo)
            self.assertEqual(result, {'publish': False, 'version': None})

    def test_missing_app_version_does_not_publish(self):
        with tempfile.TemporaryDirectory() as raw:
            tmp = Path(raw)
            repo = git_repo_with_tags(tmp, tags=[])
            path = tmp / 'Chart.yaml'
            path.write_text('apiVersion: v2\nname: runnerscout\nversion: 0.1.0\n')
            result = assess(path, repo)
            self.assertEqual(result, {'publish': False, 'version': None})
