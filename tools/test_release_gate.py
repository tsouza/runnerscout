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

    def test_malformed_app_version_raises(self):
        with tempfile.TemporaryDirectory() as raw:
            tmp = Path(raw)
            repo = git_repo_with_tags(tmp, tags=[])
            with self.assertRaises(ValueError):
                assess(chart(tmp, 'development'), repo)
