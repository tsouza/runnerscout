import json
import unittest
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
