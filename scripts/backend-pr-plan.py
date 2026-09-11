#!/usr/bin/env python3
"""Plan PR runner allocation using the same selectors and lane dry runs."""
import json
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parent.parent
SCRIPTS = ROOT / 'scripts'
# Counts are execution contracts validated by the lane scripts and gate tests.
LANES = (
    ('backend-pr', 'test', 'affected', 2),
    ('backend-pr-db', 'test', 'db', None),
    ('backend-pr-handlers', 'test', 'handlers', 5),
    ('backend-pr-performance', 'test', 'performance', None),
    ('backend-pr-race', 'race', 'affected', 4),
    ('backend-pr-managedharness-race', 'race', 'managedharness', 7),
    ('backend-pr-db-race', 'race', 'db', None),
    ('backend-pr-handlers-race', 'race', 'handlers', 5),
)


def run(args, input=None):
    return subprocess.check_output(args, cwd=ROOT, input=input, text=True).strip()


def plan(args, input=None):
    # Both failures propagate before emitting any trusted outputs. In particular,
    # a selector failure cannot be mistaken for an intentionally empty lane.
    selection = run([str(SCRIPTS / 'backend-ci-packages.sh'), *args], input)
    direct = run([str(SCRIPTS / 'backend-ci-packages.sh'), '--direct', *args], input)
    outputs = {'selection': selection, 'direct_selection': direct}
    for job, kind, lane, count in LANES:
        packages = selection.splitlines()
        active = []
        if packages:
            for shard in range(count or 1):
                command = [str(SCRIPTS / f'backend-pr-{kind}.sh'), '--dry-run', f'--lane={lane}']
                if count:
                    command.append(f'--shard={shard}/{count}')
                command.append(f'--direct-packages={direct}')
                if run([*command, *packages]):
                    active.append(shard)
        outputs[job] = 'true' if active else 'false'
        if count:
            outputs[job + '-shards'] = json.dumps(active, separators=(',', ':'))
    return outputs


def main():
    args = sys.argv[1:]
    if len(args) != 2:
        raise SystemExit('usage: backend-pr-plan.py <base> <head> | --files-from -')
    outputs = plan(args, sys.stdin.read() if args == ['--files-from', '-'] else None)
    # Package selections are multiline GitHub outputs, passed to jobs via env.
    # Check the delimiter before writing anything; lane booleans and matrices
    # are generated locally and never contain untrusted expression text.
    delimiter = 'PAIMOS_PR_PLAN_END'
    if any(delimiter in value.splitlines() for value in outputs.values()):
        raise SystemExit('backend-pr-plan: output delimiter collision')
    for name, value in outputs.items():
        if name in ('selection', 'direct_selection'):
            print(f'{name}<<{delimiter}\n{value}\n{delimiter}')
        else:
            print(f'{name}={value}')


if __name__ == '__main__':
    main()
