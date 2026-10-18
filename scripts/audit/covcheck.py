#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Check explicit slice ownership and coverage against an immutable git tree."""

import argparse
import fnmatch
import sys
from pathlib import PurePosixPath

sys.dont_write_bytecode = True
from common import HERE, cli, dump, git, load, manifest, snapshot, tracked


def owners(path, config):
    def matches(pattern):
        parts, globs = path.split('/'), pattern.split('/')

        def match_at(i, j):
            if j == len(globs):
                return i == len(parts)
            if globs[j] == '**':
                return match_at(i, j + 1) or i < len(parts) and match_at(i + 1, j)
            return i < len(parts) and fnmatch.fnmatchcase(parts[i], globs[j]) and match_at(i + 1, j + 1)

        return match_at(0, 0)

    return [s['id'] for s in config['slices']
            if any(matches(p) for p in s['paths'])
            and not any(matches(p) for p in s.get('exclude', []))]


def check(files, config, manifests=(), sha=None, selected=None):
    ids = [s['id'] for s in config['slices']]
    if len(set(ids)) != len(ids) or not ids:
        raise ValueError('slice IDs must be unique')
    selected = set(selected or ids)
    if selected - set(ids):
        raise ValueError('unknown selected slice')
    assigned = {s: [] for s in ids}
    errors = []
    for path in sorted(files):
        matches = owners(path, config)
        code = PurePosixPath(path).suffix in config['code_extensions'] or PurePosixPath(path).name in config['code_names']
        if len(matches) > 1:
            errors.append({'kind': 'ambiguous', 'file': path, 'slices': matches})
        elif not matches and code:
            errors.append({'kind': 'unassigned', 'file': path})
        elif matches:
            assigned[matches[0]].append(path)
    coverage = {}
    for data in manifests:
        manifest(data)
        sid = data['slice']
        if sid not in selected or sid in coverage:
            raise ValueError('unknown or duplicate manifest slice')
        if sha and not sha.startswith(data['sha']):
            raise ValueError('manifest snapshot mismatch')
        seen = set()
        for entry in data['coverage']:
            path = entry['file']
            if path in seen:
                errors.append({'kind': 'duplicate', 'slice': sid, 'file': path})
            seen.add(path)
            if path not in assigned[sid]:
                errors.append({'kind': 'unexpected', 'slice': sid, 'file': path})
        coverage[sid] = seen
    if manifests:
        for sid in sorted(selected):
            for path in sorted(set(assigned[sid]) - coverage.get(sid, set())):
                errors.append({'kind': 'missing', 'slice': sid, 'file': path})
    return {'snapshot': sha, 'mode': 'coverage' if manifests else 'assignment',
            'ok': not errors, 'tracked': len(files), 'assigned': assigned, 'errors': errors}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--repo', default='.')
    parser.add_argument('--sha', default='HEAD')
    parser.add_argument('--slices', default=HERE / 'slices.json')
    parser.add_argument('--manifest', action='append', default=[])
    parser.add_argument('--slice', action='append', dest='selected')
    parser.add_argument('--since', help='delta scope: files changed since this commit')
    parser.add_argument('--out')
    parser.add_argument('--summary', action='store_true', help='emit assignment counts instead of file lists')
    args = parser.parse_args()
    sha = snapshot(args.repo, args.sha)
    files = tracked(args.repo, sha)
    config = load(args.slices)
    # Assignment must pass for the whole tree even during a delta or rolling read.
    full = check(files, config, sha=sha)
    if args.since:
        base = snapshot(args.repo, args.since)
        changed = set(p.decode() for p in git(args.repo, 'diff', '--name-only', '-z', base, sha).split(b'\0') if p)
        files = [f for f in files if f in changed]
    result = check(files, config, [load(p) for p in args.manifest], sha, args.selected)
    for error in full['errors']:
        if error not in result['errors']:
            result['errors'].append(error)
    result['ok'] = not result['errors']
    if args.summary:
        result['assigned'] = {sid: len(paths) for sid, paths in result['assigned'].items()}
    dump(result, args.out)
    if not result['ok']:
        raise SystemExit(1)


if __name__ == '__main__':
    cli(main)
