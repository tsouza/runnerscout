import ast
import json
import unittest
from pathlib import Path

import evaluate
from evaluate import test_manifest


class ManifestIntegrity(unittest.TestCase):
    def events(self, *actions, package='p'):
        events = [dict(Package=package, Action='start')]
        events += [dict(Package=package, Test='T', Action=x) for x in actions]
        events += [dict(Package=package, Action='pass')]
        return [json.dumps(x) for x in events]

    def test_positive_control(self):
        self.assertTrue(test_manifest(self.events('run', 'pass'), {('p', 'T')})['pass_'])

    def test_zero_discovery(self):
        self.assertFalse(test_manifest([], {('p', 'T')})['pass_'])

    def test_forged_pass_without_start(self):
        self.assertFalse(test_manifest(self.events('pass'), {('p', 'T')})['pass_'])

    def test_skip_is_missing_evidence(self):
        self.assertFalse(test_manifest(self.events('run', 'skip'), {('p', 'T')})['pass_'])

    def test_failure_cannot_be_erased_by_later_pass(self):
        self.assertFalse(test_manifest(self.events('run', 'fail', 'pass'), {('p', 'T')})['pass_'])

    def test_crash_is_incomplete(self):
        self.assertFalse(test_manifest(self.events('run'), {('p', 'T')})['pass_'])

    def test_missing_required_test(self):
        self.assertFalse(test_manifest(self.events('run', 'pass'), {('p', 'T'), ('p', 'U')})['pass_'])

    def test_package_failure_blocks(self):
        events = self.events('run', 'pass') + [json.dumps(dict(Package='p', Action='fail'))]
        self.assertFalse(test_manifest(events, {('p', 'T')})['pass_'])

    def test_wrong_package_cannot_supply_required_name(self):
        self.assertFalse(test_manifest(self.events('run', 'pass', package='wrong'), {('p', 'T')})['pass_'])

    def test_truncated_package_result_is_incomplete(self):
        self.assertFalse(test_manifest(self.events('run', 'pass')[:-1], {('p', 'T')})['pass_'])

    def test_package_skip_blocks_even_after_test_pass(self):
        events = self.events('run', 'pass') + [json.dumps(dict(Package='p', Action='skip'))]
        self.assertFalse(test_manifest(events, {('p', 'T')})['pass_'])

    def test_go_package_without_test_files_is_not_a_required_test_skip(self):
        events = self.events('run', 'pass') + [
            json.dumps(dict(Package='api', Action='start')),
            json.dumps(dict(Package='api', Action='skip')),
        ]
        self.assertTrue(test_manifest(events, {('p', 'T')})['pass_'])
        self.assertFalse(test_manifest(events, {('p', 'T'), ('api', 'Required')})['pass_'])


def _live_cloud_integration_claims(source):
    """Every AST site in evaluate.py's source that sets the report's
    'live_cloud_integration' key, whether via the dict(...) literal or a
    later report['live_cloud_integration'] = ... assignment."""
    tree = ast.parse(source)
    claims = []
    for node in ast.walk(tree):
        if isinstance(node, ast.keyword) and node.arg == 'live_cloud_integration':
            claims.append(node.value)
        if isinstance(node, ast.Assign):
            for target in node.targets:
                if (isinstance(target, ast.Subscript)
                        and isinstance(target.slice, ast.Constant)
                        and target.slice.value == 'live_cloud_integration'):
                    claims.append(node.value)
    return claims


class EvidenceTypeIntegrity(unittest.TestCase):
    """CQ-12: a qualified controller must never present fixture, emulator or
    real-cloud evidence as interchangeable in an acceptance decision.

    tools/evaluate.py never performs a real cloud call - it is a bounded local
    diagnostic (fixtures, emulators, `go test`). Its report must therefore
    always say so as a literal, unconditional claim, not a value that could be
    flipped or computed. This test inspects the actual source of evaluate.py
    (not a mock of it), so it fails if the hardcoded claim is changed to
    `True`, made conditional/computed, duplicated, or removed.
    """

    def test_evaluator_report_never_claims_live_cloud_integration(self):
        source = Path(evaluate.__file__).read_text()
        claims = _live_cloud_integration_claims(source)
        self.assertEqual(
            len(claims), 1,
            'expected exactly one live_cloud_integration claim in tools/evaluate.py, '
            'found %d - CQ-12 requires the evidence-type claim never be ambiguous or '
            'duplicated' % len(claims))
        claim, = claims
        self.assertIsInstance(
            claim, ast.Constant,
            'live_cloud_integration must be a literal, not a computed or conditional '
            'claim (CQ-12: fixture/emulator evidence must never be presentable as '
            'real-cloud evidence)')
        self.assertIs(
            claim.value, False,
            "tools/evaluate.py's local diagnostic run has no real-cloud evidence and "
            "must not claim live_cloud_integration=True (CQ-12)")
