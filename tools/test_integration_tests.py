import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import integration_tests


class IntegrationStreams(unittest.TestCase):
    def test_successful_go_run_with_stderr_download_diagnostics(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            shim = root / 'go'
            events = '\n'.join(json.dumps(e) for e in [
                {'Package': 'p', 'Action': 'start'},
                {'Package': 'p', 'Test': 'T', 'Action': 'run'},
                {'Package': 'p', 'Test': 'T', 'Action': 'pass'},
                {'Package': 'p', 'Action': 'pass'},
            ])
            shim.write_text("#!/bin/sh\nprintf 'go: downloading test dependency\\n' >&2\ncat <<'EVENTS'\n" + events + "\nEVENTS\n")
            shim.chmod(0o700)
            with patch.object(integration_tests, 'ROOT', root), patch.dict(os.environ, {'PATH': str(root) + os.pathsep + os.environ['PATH']}), patch('sys.argv', ['integration_tests.py', '--package', 'p', '--test', 'T']):
                self.assertEqual(integration_tests.main(), 0)
            result = next((root / 'evidence').iterdir())
            self.assertIn('downloading', (result / 'stderr.log').read_text())
            self.assertNotIn('downloading', (result / 'tests.jsonl').read_text())
            self.assertEqual(json.loads((result / 'manifest.json').read_text())['verdict'], 'pass')
