#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Store only fingerprints; explicitly accept reviewed tool findings into a baseline."""

import argparse
import re
import sys

sys.dont_write_bytecode = True
from common import cli, dump, fingerprint, load, manifest


def fingerprints(baseline):
    values = baseline.get('fingerprints')
    if baseline.get('version') != 1 or not isinstance(values, list) or any(not isinstance(v, str) or not re.fullmatch('[0-9a-f]{64}', v) for v in values):
        raise ValueError('invalid tool baseline')
    return set(values)


def compare(data, baseline):
    manifest(data)
    if data['slice'] != 'T':
        raise ValueError('baseline input must be the tool sweep')
    known = fingerprints(baseline)
    unique = {}
    for finding in sorted(data['findings'], key=lambda f: f['id']):
        unique.setdefault(fingerprint(finding, tool=True), finding)
    return {**data, 'findings': [f for key, f in sorted(unique.items()) if key not in known]}


def accept(data, baseline):
    compare(data, baseline)  # Validate before updating; retain history across clean runs.
    return {'version': 1, 'fingerprints': sorted(fingerprints(baseline) | {fingerprint(f, tool=True) for f in data['findings']})}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['compare', 'accept'])
    parser.add_argument('--input', required=True)
    parser.add_argument('--baseline', required=True)
    parser.add_argument('--out', required=True)
    args = parser.parse_args()
    operation = compare if args.action == 'compare' else accept
    dump(operation(load(args.input), load(args.baseline)), args.out)


if __name__ == '__main__':
    cli(main)
