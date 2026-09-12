import json
import unittest
from evaluate import test_manifest

class ManifestIntegrity(unittest.TestCase):
 def events(self,*actions):return [json.dumps(dict(Package='p',Test='T',Action=x)) for x in actions]
 def test_positive_control(self):self.assertTrue(test_manifest(self.events('run','pass'),{'T'})['pass_'])
 def test_zero_discovery(self):self.assertFalse(test_manifest([],{'T'})['pass_'])
 def test_forged_pass_without_start(self):self.assertFalse(test_manifest(self.events('pass'),{'T'})['pass_'])
 def test_skip_is_missing_evidence(self):self.assertFalse(test_manifest(self.events('run','skip'),{'T'})['pass_'])
 def test_failure_cannot_be_erased_by_later_pass(self):self.assertFalse(test_manifest(self.events('run','fail','pass'),{'T'})['pass_'])
 def test_crash_is_incomplete(self):self.assertFalse(test_manifest(self.events('run'),{'T'})['pass_'])
 def test_missing_required_test(self):self.assertFalse(test_manifest(self.events('run','pass'),{'T','U'})['pass_'])
 def test_package_failure_blocks(self):
  events=self.events('run','pass')+[json.dumps(dict(Package='p',Action='fail'))]
  self.assertFalse(test_manifest(events,{'T'})['pass_'])
