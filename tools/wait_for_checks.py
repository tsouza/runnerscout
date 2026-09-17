#!/usr/bin/env python3
"""Polls this commit's REQUIRED checks (see release_preflight.py) until every
one reaches a terminal CheckRun status, or a timeout elapses.

release_preflight.py deliberately takes one, single, un-retried snapshot -
that design assumes its caller only invokes it once CI has actually settled.
That held for the old tag-push trigger: a human paced the tag push well
after the push-to-main commit's own CI had finished. release.yml's
push-to-main trigger fires on the exact same push event as ci.yml, so
nothing guarantees that assumption on its own - preflight's snapshot query
was observed losing that race in production (all seven REQUIRED checks
still queued/in-progress when it ran seconds after the triggering merge).
This script is release.yml's own wait step ahead of preflight; it never
judges pass/fail itself and release_preflight.py stays unchanged.
"""
import argparse
import json
import subprocess
import sys
import time

from release_preflight import QUERY, REQUIRED


def parse_pending(response, candidate):
    """Returns the REQUIRED check names not yet COMPLETED, or None if the
    response itself is unusable (malformed, wrong commit, truncated) -
    None means "not settled", exactly like a non-empty set does; the
    caller does not need to tell the two apart."""
    try:
        if response.get('errors'):
            return None
        branch = response['data']['repository']['defaultBranchRef']
        if branch['name'] != 'main' or branch['target']['oid'] != candidate:
            return None
        contexts = branch['target']['statusCheckRollup']['contexts']
        if contexts['pageInfo']['hasNextPage'] is not False:
            return None
        settled = set()
        for check in contexts['nodes']:
            if check.get('__typename') == 'CheckRun' and check.get('name') in REQUIRED:
                if check.get('status') == 'COMPLETED':
                    settled.add(check['name'])
        return REQUIRED - settled
    except (KeyError, TypeError, AttributeError):
        return None


def fetch_pending(candidate, gh):
    result = subprocess.run([gh, 'api', 'graphql', '--input', '-'],
                             input=json.dumps({'query': QUERY}), text=True,
                             capture_output=True, timeout=45, check=False)
    if result.returncode:
        return None
    try:
        return parse_pending(json.loads(result.stdout), candidate)
    except ValueError:
        return None


def wait(fetch, timeout_seconds, interval_seconds, sleep=time.sleep, clock=time.monotonic):
    deadline = clock() + timeout_seconds
    while True:
        pending = fetch()
        if pending is not None and not pending:
            return True
        if clock() >= deadline:
            return False
        sleep(interval_seconds)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--candidate', required=True)
    parser.add_argument('--gh', default='gh')
    parser.add_argument('--timeout-seconds', type=int, default=1200)
    parser.add_argument('--interval-seconds', type=int, default=20)
    args = parser.parse_args()
    settled = wait(lambda: fetch_pending(args.candidate, args.gh),
                    args.timeout_seconds, args.interval_seconds)
    if not settled:
        print(f'wait_for_checks: required checks did not settle within {args.timeout_seconds}s',
              file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
