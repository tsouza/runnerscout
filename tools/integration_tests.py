#!/usr/bin/env python3
"""Execute required Kubernetes tests; an exit-zero skip or empty run is failure."""
import argparse
import json
from pathlib import Path
import re
import subprocess
import uuid

from evaluate import ROOT, test_manifest


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--package', required=True)
    parser.add_argument('--test', required=True)
    args = parser.parse_args()
    output = ROOT / 'evidence' / ('integration-' + str(uuid.uuid4()))
    output.mkdir(parents=True)
    command = ['go', 'test', '-json', '-tags', 'integration', '-count=1',
               '-run', '^' + re.escape(args.test) + '$', args.package]
    report = {'command': command, 'verdict': 'fail'}
    try:
        with (output / 'tests.jsonl').open('w') as log:
            result = subprocess.run(command, cwd=ROOT, stdout=log,
                                    stderr=subprocess.STDOUT, timeout=600, check=False)
        report['exit_code'] = result.returncode
        report['tests'] = test_manifest((output / 'tests.jsonl').read_text().splitlines(),
                                       {(args.package, args.test)})
        if result.returncode == 0 and report['tests']['pass_']:
            report['verdict'] = 'pass'
    except subprocess.TimeoutExpired:
        report['verdict'] = 'inconclusive'
        report['reason'] = 'integration timeout'
    except (OSError, ValueError) as error:
        report['reason'] = str(error)
    (output / 'manifest.json').write_text(json.dumps(report, indent=2) + '\n')
    print(json.dumps({'verdict': report['verdict'], 'evidence': str(output.relative_to(ROOT))}))
    return 0 if report['verdict'] == 'pass' else 1


if __name__ == '__main__':
    raise SystemExit(main())
