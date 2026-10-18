#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Build a deterministic, self-contained HTML audit page from grouped JSON."""

import argparse
from collections import Counter
import json
from pathlib import Path
import sys

sys.dont_write_bytecode = True
from common import HERE, cli, load


def build(audit):
    findings = audit['findings']
    ids = {f['id'] for f in findings}
    if len(ids) != len(findings):
        raise ValueError('duplicate report finding')
    group_ids = [fid for group in audit['groups'] for fid in group['ids']]
    if set(group_ids) != ids or len(group_ids) != len(ids):
        raise ValueError('every retained finding must appear in exactly one group')
    counts = Counter(f['verdict'] for f in findings)
    if set(counts) - {'confirmed', 'partly'}:
        raise ValueError('report findings must be verified')
    data = {**audit, 'cov': {'files': sum(c['files'] for c in audit['coverage'].values()),
                           'lines': sum(c['lines'] for c in audit['coverage'].values())},
            'vstats': {'confirmed': counts['confirmed'], 'partly': counts['partly']},
            'tldr': audit.get('tldr', []),
            'method': audit.get('method', 'Immutable git snapshot; shared severity rubric; per-slice coverage manifests; independent verdicts; fingerprint deduplication; root-cause themes. Tool verdicts and coverage are limited to the supplied evidence. No production systems are accessed by these tools.')}
    # Escape HTML delimiters even inside JSON: a finding cannot close the data script.
    encoded = json.dumps(data, ensure_ascii=False, sort_keys=True, separators=(',', ':'))
    for character, escaped in [('&', '\\u0026'), ('<', '\\u003c'), ('>', '\\u003e'), ('\u2028', '\\u2028'), ('\u2029', '\\u2029')]:
        encoded = encoded.replace(character, escaped)
    return (HERE / 'template.html').read_text(encoding='utf-8').replace('__DATA__', encoded)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--audit', required=True)
    parser.add_argument('--out', required=True)
    args = parser.parse_args()
    Path(args.out).write_text(build(load(args.audit)), encoding='utf-8')


if __name__ == '__main__':
    cli(main)
