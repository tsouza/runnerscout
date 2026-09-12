import copy
import datetime as dt
import unittest
from release_preflight import REQUIRED, assess

NOW = dt.datetime(2026, 9, 12, tzinfo=dt.timezone.utc)
SHA = 'a' * 40


def fixture():
    return {'collected_at': NOW.isoformat(), 'response': {'data': {'repository': {
        'nameWithOwner': 'tsouza/runnerscout',
        'pullRequests': {'totalCount': 0}, 'issues': {'totalCount': 0},
        'defaultBranchRef': {'name': 'main', 'target': {'oid': SHA, 'statusCheckRollup': {
            'state': 'SUCCESS', 'contexts': {'pageInfo': {'hasNextPage': False}, 'nodes': [
                {'__typename': 'CheckRun', 'name': name, 'status': 'COMPLETED',
                 'conclusion': 'SUCCESS', 'checkSuite': {'app': {'slug': 'github-actions'}}}
                for name in sorted(REQUIRED)
            ]}
        }}}
    }}}}


class RepositoryReleaseGates(unittest.TestCase):
    def test_qualified_repository_positive_control(self):
        self.assertTrue(assess(fixture(), SHA, NOW)['repository_gates_pass'])

    def test_open_pr_or_issue_blocks_green_main(self):
        for field in ['pullRequests', 'issues']:
            snapshot = fixture()
            snapshot['response']['data']['repository'][field]['totalCount'] = 1
            self.assertFalse(assess(snapshot, SHA, NOW)['repository_gates_pass'])

    def test_main_movement_and_stale_evidence_block(self):
        self.assertFalse(assess(fixture(), 'b' * 40, NOW)['repository_gates_pass'])
        self.assertFalse(assess(fixture(), SHA, NOW + dt.timedelta(seconds=61))['repository_gates_pass'])
        self.assertFalse(assess(fixture(), SHA, NOW - dt.timedelta(seconds=1))['repository_gates_pass'])

    def test_non_successful_main_checks_block_even_with_green_rollup(self):
        for status, conclusion in [('IN_PROGRESS', None), ('COMPLETED', 'FAILURE'),
                                   ('COMPLETED', 'SKIPPED'), ('COMPLETED', 'CANCELLED'),
                                   ('COMPLETED', 'NEUTRAL')]:
            snapshot = fixture()
            check = snapshot['response']['data']['repository']['defaultBranchRef']['target']['statusCheckRollup']['contexts']['nodes'][0]
            check.update(status=status, conclusion=conclusion)
            self.assertFalse(assess(snapshot, SHA, NOW)['repository_gates_pass'])

    def test_missing_truncated_or_wrong_producer_evidence_blocks(self):
        for mode in ['missing', 'truncated', 'producer', 'duplicate', 'graphql-error']:
            snapshot = fixture()
            contexts = snapshot['response']['data']['repository']['defaultBranchRef']['target']['statusCheckRollup']['contexts']
            if mode == 'missing': contexts['nodes'].pop()
            if mode == 'truncated': contexts['pageInfo']['hasNextPage'] = True
            if mode == 'producer': contexts['nodes'][0]['checkSuite']['app']['slug'] = 'other-app'
            if mode == 'duplicate': contexts['nodes'].append(copy.deepcopy(contexts['nodes'][0]))
            if mode == 'graphql-error': snapshot['response']['errors'] = [{'message': 'incomplete'}]
            self.assertFalse(assess(snapshot, SHA, NOW)['repository_gates_pass'], mode)

    def test_malformed_evidence_cannot_pass(self):
        for snapshot in [{}, {'collected_at': 'invalid'}, {'collected_at': NOW.isoformat(), 'response': {}}]:
            self.assertFalse(assess(snapshot, SHA, NOW)['repository_gates_pass'])
