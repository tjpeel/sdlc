#!/usr/bin/env python3
"""Connected smoke test for the marker ticket in examples/smoke-ticket.md.

Prints the plan unless --execute is supplied. An optional --ticket runs Codex
first; otherwise the test uses the completed branch in the existing workspace.
The container checks the marker before publishing, then verifies the resulting
branch and draft PR on GitHub.
"""
import argparse
from pathlib import Path
import shlex
import subprocess
import sys


ROOT = Path(__file__).resolve().parents[1]


def commands(args):
    common = ['--profiles', str(args.profiles.resolve()), '--profile', args.profile,
              '--repo', args.repo, '--branch', args.branch]
    if args.model is not None:
        common += ['--model', args.model]
    launcher = [sys.executable, str(ROOT / 'scripts/sdlc.py')]
    plan = []
    if args.ticket:
        plan.append(('Implement and check the ticket in Docker',
                     launcher + ['exec', *common, '--ticket', str(args.ticket.resolve())]))
    plan.append(('Verify the completed signed branch in Docker',
                 launcher + ['verify', *common]))
    plan.append(('Push, create a draft PR and verify it from Docker',
                 launcher + ['publish', *common, '--title', args.title,
                             '--body', str(args.body.resolve()), '--execute',
                             '--smoke-checks', '--verify-published']))
    return plan


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--profiles', type=Path, default=ROOT / 'profiles.local.json')
    parser.add_argument('--profile', required=True)
    parser.add_argument('--repo', required=True)
    parser.add_argument('--model', help='Codex model override for the container.')
    parser.add_argument('--branch', required=True)
    parser.add_argument('--title', required=True)
    parser.add_argument('--body', type=Path, required=True)
    parser.add_argument('--ticket', type=Path,
                        help='Optional marker ticket for a fresh branch; omit to finish an existing branch.')
    parser.add_argument('--execute', action='store_true',
                        help='Run the test, including GitHub branch push and draft PR creation.')
    args = parser.parse_args(argv)
    plan = commands(args)
    if not args.execute:
        print('Print only: no Docker, Codex or GitHub operations are executed.')
        for label, command in plan:
            print(f'\n{label}:\n{shlex.join(command)}')
        return 0

    # Check every input before running Codex or changing the container checkout.
    for path in (args.profiles, args.body, args.ticket):
        if path is not None and not path.is_file():
            parser.error(f'Missing input file: {path}')
    for index, (label, command) in enumerate(plan, start=1):
        print(f'\nStage {index}/{len(plan)}: {label}', flush=True)
        try:
            subprocess.run(command, check=True)
        except subprocess.CalledProcessError:
            print(f'Connected GitHub smoke test failed at: {label}.', file=sys.stderr)
            if index == len(plan):
                print('A branch or PR may already exist. Inspect GitHub before retrying.',
                      file=sys.stderr)
            return 1
    print('\nConnected GitHub smoke test passed: the container checked the marker, pushed the signed branch, '
          'created a draft PR and confirmed the GitHub branch and PR match the local commit.')
    return 0


if __name__ == '__main__':
    sys.exit(main())
