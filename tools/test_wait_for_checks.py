import unittest
from release_preflight import REQUIRED
from wait_for_checks import parse_pending, wait

SHA = 'a' * 40


def response(check_overrides=None):
    nodes = [{'__typename': 'CheckRun', 'name': name, 'status': 'COMPLETED',
              'conclusion': 'SUCCESS'} for name in sorted(REQUIRED)]
    if check_overrides:
        for node, status in zip(nodes, check_overrides):
            node['status'] = status
    return {'data': {'repository': {'defaultBranchRef': {'name': 'main', 'target': {
        'oid': SHA, 'statusCheckRollup': {'contexts': {
            'pageInfo': {'hasNextPage': False}, 'nodes': nodes}}
    }}}}}


class ParsePending(unittest.TestCase):
    def test_all_completed_is_empty(self):
        self.assertEqual(parse_pending(response(), SHA), set())

    def test_in_progress_check_is_pending(self):
        overrides = ['COMPLETED'] * len(REQUIRED)
        overrides[0] = 'IN_PROGRESS'
        pending = parse_pending(response(overrides), SHA)
        self.assertEqual(pending, {sorted(REQUIRED)[0]})

    def test_missing_check_counts_as_pending(self):
        body = response()
        body['data']['repository']['defaultBranchRef']['target']['statusCheckRollup']['contexts']['nodes'].pop()
        pending = parse_pending(body, SHA)
        self.assertEqual(len(pending), 1)

    def test_wrong_commit_is_unsettled(self):
        self.assertIsNone(parse_pending(response(), 'b' * 40))

    def test_truncated_evidence_is_unsettled(self):
        body = response()
        body['data']['repository']['defaultBranchRef']['target']['statusCheckRollup']['contexts']['pageInfo']['hasNextPage'] = True
        self.assertIsNone(parse_pending(body, SHA))

    def test_graphql_error_is_unsettled(self):
        self.assertIsNone(parse_pending({'errors': [{'message': 'boom'}]}, SHA))

    def test_malformed_response_is_unsettled(self):
        self.assertIsNone(parse_pending({}, SHA))


class Wait(unittest.TestCase):
    def test_returns_true_as_soon_as_pending_set_empties(self):
        results = iter([{'verify'}, {'verify'}, set()])
        slept = []
        clock = iter([0, 1, 2, 3])
        self.assertTrue(wait(lambda: next(results), timeout_seconds=100,
                              interval_seconds=5, sleep=slept.append, clock=lambda: next(clock)))
        self.assertEqual(slept, [5, 5])

    def test_none_is_treated_as_still_pending_not_a_pass(self):
        results = iter([None, None, set()])
        clock = iter([0, 1, 2, 3])
        self.assertTrue(wait(lambda: next(results), timeout_seconds=100,
                              interval_seconds=5, sleep=lambda _: None, clock=lambda: next(clock)))

    def test_times_out_when_never_settled(self):
        clock = iter([0, 50, 101])
        self.assertFalse(wait(lambda: {'verify'}, timeout_seconds=100,
                               interval_seconds=50, sleep=lambda _: None, clock=lambda: next(clock)))
