#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Merge slice manifests and independent verdicts without losing provenance."""

import argparse
from collections import Counter, defaultdict
import re
import sys

sys.dont_write_bytecode = True
from common import HERE, SEVERITIES, cli, dump, fingerprint, load, manifest


def reviews(path):
    """Accept JSON verdict arrays or the prototype BEGIN-VERIFY JSONL envelope."""
    import json
    text = path.read_text(encoding='utf-8')
    if text.lstrip().startswith('['):
        return json.loads(text)
    match = re.search(r'BEGIN-VERIFY\s*(.*?)\s*END-VERIFY', text, re.S)
    if not match:
        raise ValueError('review must be a JSON array or BEGIN-VERIFY envelope')
    return [json.loads(line) for line in match[1].splitlines() if line.strip()]


def merge(inputs, verdicts=(), release='', areas=None):
    if not inputs:
        raise ValueError('at least one manifest required')
    for data in inputs:
        manifest(data)
    shas = {d['sha'] for d in inputs}
    sha = max(shas, key=len)
    if any(not sha.startswith(s) for s in shas):
        raise ValueError('mixed snapshots')
    if len({d['slice'] for d in inputs}) != len(inputs):
        raise ValueError('duplicate slice')
    ids = [f['id'] for d in inputs for f in d['findings']]
    if len(set(ids)) != len(ids):
        raise ValueError('duplicate finding ID')
    tool_review_ids = {'T:' + f['theme'] for d in inputs if d['slice'] == 'T' for f in d['findings']}
    verified = {}
    for review in verdicts:
        if not isinstance(review, dict) or not isinstance(review.get('id'), str) or not isinstance(review.get('verdict'), str):
            raise ValueError('review must have string id and verdict')
        fid = review['id']
        verdict = review['verdict']
        kind, _, target = verdict.partition(':')
        if fid not in set(ids) | tool_review_ids or fid in verified:
            raise ValueError('unknown or duplicate review ID')
        if kind not in ('confirmed', 'partly', 'refuted', 'duplicate'):
            raise ValueError('unknown verdict')
        if kind == 'duplicate' and (target not in ids or target == fid) or kind != 'duplicate' and target:
            raise ValueError('invalid duplicate target')
        if fid in tool_review_ids and fid not in ids and kind == 'duplicate':
            raise ValueError('tool theme duplicates require per-finding verdicts')
        if review.get('severity') is not None and review['severity'] not in SEVERITIES:
            raise ValueError('unknown review severity')
        if not isinstance(review.get('evidence'), str) or not review['evidence'].strip():
            raise ValueError('verification requires evidence')
        if any(review.get(key) is not None and not isinstance(review[key], str) for key in ('note', 'pass')):
            raise ValueError('review note and pass must be strings')
        verified[fid] = review
    areas = areas or {}
    findings = []
    coverage = {}
    good = []
    for data in sorted(inputs, key=lambda d: d['slice']):
        sid = data['slice']
        paths = [c['file'] for c in data['coverage']]
        if len(set(paths)) != len(paths):
            raise ValueError('duplicate coverage path')
        coverage[sid] = {'files': len(paths), 'lines': sum(c['lines'] for c in data['coverage']),
                         'status': dict(sorted(Counter(c['status'] for c in data['coverage']).items()))}
        good.extend({'slice': sid, 'text': text} for text in sorted(set(data['good'])))
        for finding in data['findings']:
            review = verified.get(finding['id'], {})
            if sid == 'T' and not review:
                review = verified.get('T:' + finding['theme'], {})
            kind, _, target = review.get('verdict', 'unverified').partition(':')
            findings.append({**finding, 'slice': sid, 'area': areas.get(sid, sid),
                             'fingerprint': fingerprint(finding), 'filed_severity': finding['severity'],
                             'severity': review.get('severity') or finding['severity'], 'verdict': kind,
                             'dup_of': target or None, 'verify_evidence': review.get('evidence'),
                             'verify_note': review.get('note'), 'verify_pass': review.get('pass')})
    by_id = {f['id']: f for f in findings}
    for finding in findings:
        if finding['verdict'] == 'duplicate':
            seen = {finding['id']}
            target = finding['dup_of']
            while by_id[target]['verdict'] == 'duplicate':
                if target in seen:
                    raise ValueError('duplicate review cycle')
                seen.add(target)
                target = by_id[target]['dup_of']
            if by_id[target]['verdict'] not in ('confirmed', 'partly'):
                raise ValueError('duplicate target must survive verification')
            finding['dup_of'] = target
    buckets = defaultdict(list)
    dropped = []
    for finding in findings:
        if finding['verdict'] in ('confirmed', 'partly'):
            buckets[finding['fingerprint']].append(finding)
        else:
            dropped.append(finding)
    kept = []
    for key in sorted(buckets):
        candidates = sorted(buckets[key], key=lambda f: (SEVERITIES.index(f['severity']), f['verdict'] != 'confirmed', f['id']))
        winner = dict(candidates[0])
        winner['aliases'] = sorted(f['id'] for f in candidates[1:])
        winner['locations'] = sorted({p for f in candidates for p in f['locations']})
        winner['related_tickets'] = sorted({p for f in candidates for p in f['related_tickets']})
        kept.append(winner)
        for duplicate in candidates[1:]:
            dropped.append({**duplicate, 'verdict': 'duplicate', 'dup_of': winner['id']})
    aliases = {fid: f['id'] for f in kept for fid in [f['id'], *f['aliases']]}
    for finding in dropped:
        if finding['verdict'] == 'duplicate':
            finding['dup_of'] = aliases.get(finding['dup_of'], finding['dup_of'])
    order = lambda f: (SEVERITIES.index(f['severity']), f['slice'], f['id'])
    tool_groups = defaultdict(list)
    for finding in findings:
        if finding['slice'] == 'T':
            tool_groups[finding['theme']].append(finding)
    tools = []
    for theme, candidates in sorted(tool_groups.items()):
        verdict_set = {f['verdict'] for f in candidates}
        tools.append({'id': 'T:' + theme, 'theme': theme, 'candidates': len(candidates),
                      'verdict': next(iter(verdict_set)) if len(verdict_set) == 1 else 'mixed',
                      'evidence': ' '.join(sorted({f['verify_evidence'] for f in candidates if f['verify_evidence']})),
                      'note': ' '.join(sorted({f['verify_note'] for f in candidates if f['verify_note']}))})
    return {'snapshot': sha, 'release': release, 'areas': areas, 'coverage': coverage,
            'findings': sorted(kept, key=order), 'dropped': sorted(dropped, key=order),
            'tool_themes': tools, 'tool_stats': {'candidates': sum(len(d['findings']) for d in inputs if d['slice'] == 'T')},
            'good': good, 'themes': {t: sorted(f['id'] for f in kept if f['theme'] == t) for t in sorted({f['theme'] for f in kept})}}


def main():
    from pathlib import Path
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--manifest', action='append', required=True)
    parser.add_argument('--reviews', action='append', default=[])
    parser.add_argument('--slices', default=HERE / 'slices.json')
    parser.add_argument('--release', default='')
    parser.add_argument('--out', required=True)
    args = parser.parse_args()
    areas = {s['id']: s['title'] for s in load(args.slices)['slices']}
    areas['T'] = 'Tool sweep'
    dump(merge([load(p) for p in args.manifest], [v for p in args.reviews for v in reviews(Path(p))], args.release, areas), args.out)


if __name__ == '__main__':
    cli(main)
