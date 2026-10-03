#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Group verified findings by root-cause theme, optionally with editorial copy."""

import argparse
from collections import defaultdict
import sys

sys.dont_write_bytecode = True
from common import cli, dump, load


def group(audit, descriptions=None):
    themes = defaultdict(list)
    for finding in audit['findings']:
        themes[finding['theme']].append(finding['id'])
    descriptions = descriptions or {}
    if descriptions.keys() - themes.keys():
        raise ValueError('editorial theme has no findings')
    groups = []
    for theme, ids in sorted(themes.items()):
        copy = descriptions.get(theme, {})
        groups.append({'key': theme, 'title': copy.get('title', theme),
                       'summary': copy.get('summary', ''), 'ids': sorted(ids),
                       'tickets': copy.get('tickets')})
    return {**audit, 'groups': groups}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--audit', required=True)
    parser.add_argument('--descriptions', help='optional JSON theme → title/summary/tickets object')
    parser.add_argument('--out', required=True)
    args = parser.parse_args()
    dump(group(load(args.audit), load(args.descriptions) if args.descriptions else None), args.out)


if __name__ == '__main__':
    cli(main)
