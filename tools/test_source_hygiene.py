"""Source/archive boundaries must also hold for force-added ignored files."""
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import source_hygiene


class SourceHygiene(unittest.TestCase):
    def test_release_source_accepts_durable_documents_and_behavior_fixtures(self):
        self.assertEqual(source_hygiene.violations([
            'README.md', 'docs/architecture.md', 'docs/releases.md',
            'internal/state/testdata/allocation.json', 'tools/fixtures/github.py',
        ]), [])

    def test_release_source_rejects_transient_documents(self):
        paths = ['outputs/records.json', 'work/notes.txt', 'evidence/manifest.json',
                 'docs/UBUNTU-DEVELOPMENT-HANDOFF.md', 'docs/release-plan.md',
                 'docs/development-history.jsonl']
        self.assertEqual(source_hygiene.violations(paths), sorted(paths))

    def test_ignored_local_archive_allowed_but_force_added_archive_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            def git(*args):
                subprocess.run(['git', *args], cwd=root, check=True,
                               stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
            git('init')
            (root / '.gitignore').write_text('/work/\n')
            (root / 'work').mkdir()
            (root / 'work/notes.txt').write_text('private development history')
            (root / 'README.md').write_text('# Example\n')
            git('add', '.gitignore', 'README.md')
            with patch.object(source_hygiene, 'ROOT', root):
                source_hygiene.main()
                git('add', '-f', 'work/notes.txt')
                with self.assertRaisesRegex(SystemExit, 'work/notes.txt'):
                    source_hygiene.main()
