#!/usr/bin/env python3
"""Read-only repository release gates. This is not full release qualification."""
import argparse
import datetime as dt
import json
from pathlib import Path
import re
import subprocess
import uuid

ROOT = Path(__file__).resolve().parents[1]
REPOSITORY = 'tsouza/runnerscout'
REQUIRED = {'verify', 'vulnerability', 'kubernetes-integration', 'analyze',
            'cloud-emulators', 'chart', 'runtime-image'}
QUERY = '''query {
 repository(owner:"tsouza", name:"runnerscout") {
  nameWithOwner
  pullRequests(states:OPEN) { totalCount }
  issues(states:OPEN) { totalCount }
  defaultBranchRef { name target { ... on Commit {
   oid statusCheckRollup { state contexts(first:100) {
    pageInfo { hasNextPage }
    nodes {
     __typename
     ... on CheckRun { name status conclusion checkSuite { app { slug } } }
     ... on StatusContext { context state }
    }
   } }
  } } }
 }
}'''


def assess(snapshot, candidate, now):
    reasons = []
    if not re.fullmatch(r'[0-9a-f]{40}', candidate):
        reasons.append('candidate must be a full commit SHA')
    try:
        collected = dt.datetime.fromisoformat(snapshot['collected_at'])
        age = (now - collected).total_seconds()
        if age < 0 or age > 60:
            reasons.append('repository snapshot is stale or future-dated')
        response = snapshot['response']
        if response.get('errors'):
            reasons.append('GitHub returned incomplete or failed evidence')
        repo = response['data']['repository']
        if repo['nameWithOwner'] != REPOSITORY:
            reasons.append('wrong repository')
        for field, label in [('pullRequests', 'pull requests'), ('issues', 'issues')]:
            count = repo[field]['totalCount']
            if type(count) is not int or count < 0:
                reasons.append('invalid count of open ' + label)
            elif count:
                reasons.append(f'{count} open {label}')
        branch = repo['defaultBranchRef']
        if branch['name'] != 'main' or branch['target']['oid'] != candidate:
            reasons.append('candidate is not the current main head')
        rollup = branch['target']['statusCheckRollup']
        if rollup['state'] != 'SUCCESS':
            reasons.append('main CI is not successful')
        contexts = rollup['contexts']
        if contexts['pageInfo']['hasNextPage'] is not False:
            reasons.append('check evidence is truncated')
        seen = set()
        for check in contexts['nodes']:
            if check['__typename'] == 'CheckRun':
                name = check['name']
                if check['status'] != 'COMPLETED' or check['conclusion'] != 'SUCCESS':
                    reasons.append('non-successful check: ' + name)
                if name in REQUIRED:
                    if name in seen:
                        reasons.append('ambiguous required check: ' + name)
                    seen.add(name)
                    if check['checkSuite']['app']['slug'] != 'github-actions':
                        reasons.append('unexpected required check producer: ' + name)
            elif check['__typename'] == 'StatusContext':
                if check['state'] != 'SUCCESS':
                    reasons.append('non-successful status: ' + check['context'])
            else:
                reasons.append('unknown check evidence type')
        if REQUIRED - seen:
            reasons.append('missing required checks: ' + ', '.join(sorted(REQUIRED - seen)))
    except (KeyError, TypeError, ValueError, AttributeError):
        reasons.append('missing or malformed repository evidence')
    return {'repository_gates_pass': not reasons, 'reasons': reasons}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--candidate', required=True)
    parser.add_argument('--gh', default='gh-tsouza', help='account-scoped GitHub wrapper')
    args = parser.parse_args()
    output = ROOT / 'evidence' / ('release-preflight-' + str(uuid.uuid4()))
    output.mkdir(parents=True)
    report = {'candidate': args.candidate, 'scope': 'repository state only',
              'release_qualified': False, 'repository_gates_pass': False}
    try:
        result = subprocess.run([args.gh, 'api', 'graphql', '--input', '-'],
                                input=json.dumps({'query': QUERY}), text=True,
                                capture_output=True, timeout=45, check=False)
        if result.returncode:
            report['reasons'] = ['GitHub repository-state query failed']
        else:
            snapshot = {'collected_at': dt.datetime.now(dt.timezone.utc).isoformat(),
                        'response': json.loads(result.stdout)}
            report['snapshot'] = snapshot
            report.update(assess(snapshot, args.candidate, dt.datetime.now(dt.timezone.utc)))
    except (OSError, ValueError, subprocess.TimeoutExpired):
        report['reasons'] = ['repository-state evidence unavailable']
    (output / 'manifest.json').write_text(json.dumps(report, indent=2) + '\n')
    print(json.dumps({'repository_gates_pass': report['repository_gates_pass'],
                      'reasons': report.get('reasons', []),
                      'evidence': str(output.relative_to(ROOT))}))
    return 0 if report['repository_gates_pass'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
