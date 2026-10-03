# SPDX-License-Identifier: AGPL-3.0-only
"""Offline synthetic-contract tests; no repository findings are fixtures."""

import copy
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

sys.dont_write_bytecode = True
from baseline import accept, compare
from build import build
from common import HERE, fingerprint, load, manifest, tracked, snapshot
from covcheck import check, owners
from groups import group
from merge import merge, reviews


SHA = 'a' * 40


def finding(fid='S1-001', title='Synthetic test finding', location='internal/auth/test.go:10', theme='test-theme'):
    return {'id': fid, 'title': title, 'category': 'testing', 'severity': 'high',
            'confidence': 'high', 'locations': [location], 'evidence': 'Synthetic evidence',
            'why_it_matters': 'Synthetic impact', 'fix': 'Synthetic fix', 'effort': 'S',
            'related_tickets': [], 'theme': theme}


def data(sid='S1', findings=None, files=('internal/auth/test.go',)):
    return {'slice': sid, 'sha': SHA, 'coverage': [{'file': f, 'lines': 10, 'status': 'read'} for f in files],
            'findings': findings if findings is not None else [finding()], 'good': []}


def verdict(fid='S1-001', value='confirmed', **kwargs):
    return {'id': fid, 'verdict': value, 'evidence': 'Synthetic verification', **kwargs}


CONFIG = {'code_extensions': ['.go', '.ts'], 'code_names': ['Dockerfile'],
          'slices': [{'id': 'S1', 'paths': ['internal/auth/**']}, {'id': 'S2', 'paths': ['web/**']}]}


class CoverageTests(unittest.TestCase):
    def test_glob_star_stays_within_a_component(self):
        config = copy.deepcopy(CONFIG)
        config['slices'][0]['paths'] = ['*.go']
        self.assertEqual(owners('root.go', config), ['S1'])
        self.assertEqual(owners('internal/root.go', config), [])

    def test_unassigned_code_fails_but_noncode_may_be_outside_slices(self):
        result = check(['new/x.go', 'new/info.txt', 'Dockerfile'], CONFIG)
        self.assertFalse(result['ok'])
        self.assertEqual([e['file'] for e in result['errors']], ['Dockerfile', 'new/x.go'])

    def test_assignment_is_not_a_coverage_claim(self):
        result = check(['internal/auth/test.go'], CONFIG)
        self.assertTrue(result['ok'])
        self.assertEqual(result['mode'], 'assignment')

    def test_complete_manifests(self):
        result = check(['internal/auth/test.go', 'web/main.ts'], CONFIG,
                       [data(), data('S2', [], ('web/main.ts',))], SHA)
        self.assertTrue(result['ok'])
        self.assertEqual(result['mode'], 'coverage')

    def test_missing_slice_and_file(self):
        result = check(['internal/auth/test.go', 'internal/auth/new.go', 'web/main.ts'], CONFIG, [data()], SHA)
        self.assertEqual({e['file'] for e in result['errors']}, {'internal/auth/new.go', 'web/main.ts'})

    def test_rolling_selection(self):
        result = check(['internal/auth/test.go', 'web/main.ts'], CONFIG, [data()], SHA, ['S1'])
        self.assertTrue(result['ok'])

    def test_wrong_owner_stale_and_duplicate_coverage(self):
        doc = data(files=('web/main.ts', 'internal/auth/test.go', 'internal/auth/test.go'))
        result = check(['internal/auth/test.go', 'web/main.ts'], CONFIG, [doc], SHA)
        self.assertEqual({e['kind'] for e in result['errors']}, {'duplicate', 'unexpected', 'missing'})

    def test_snapshot_mismatch_and_duplicate_manifests(self):
        doc = data()
        doc['sha'] = 'b' * 40
        with self.assertRaises(ValueError):
            check(['internal/auth/test.go'], CONFIG, [doc], SHA)
        with self.assertRaises(ValueError):
            check([], CONFIG, [data(), data()], SHA)

    def test_overlap_is_an_error(self):
        config = copy.deepcopy(CONFIG)
        config['slices'][1]['paths'].append('internal/**')
        self.assertEqual(check(['internal/auth/test.go'], config)['errors'][0]['kind'], 'ambiguous')

    def test_current_snapshot_has_no_unassigned_code(self):
        repo = HERE.parent.parent
        sha = snapshot(repo, 'HEAD')
        result = check(tracked(repo, sha), load(HERE / 'slices.json'), sha=sha)
        self.assertTrue(result['ok'], result['errors'])

    def test_original_coverage_gaps_are_owned(self):
        config = load(HERE / 'slices.json')
        expected = {'deploy/compose/compose.yaml': 'S7', 'embed.go': 'S1',
                    'web/embed.go': 'S9', 'web/vite.config.ts': 'S9',
                    'web/tsconfig.app.json': 'S9', 'web/e2e/smoke.spec.ts': 'S9',
                    'web/src/views/Test.vue': 'S8', 'web/src/lib/test.ts': 'S9'}
        for path, sid in expected.items():
            self.assertEqual(owners(path, config), [sid])
        self.assertFalse(check(['internal/new-package/main.go'], config)['ok'])

    def test_service_tiers_belong_to_agent_runtime(self):
        config = load(HERE / 'slices.json')
        for path in ('internal/servicetier/tier.go', 'internal/servicetier/tier_test.go'):
            self.assertEqual(owners(path, config), ['S2'])


class MergeTests(unittest.TestCase):
    def test_tool_theme_sampling_and_individual_override(self):
        doc = data('T', [finding('T-001'), finding('T-002', title='Other tool finding')], ())
        result = merge([doc], [verdict('T:test-theme', 'refuted'), verdict('T-002')])
        self.assertEqual([f['id'] for f in result['findings']], ['T-002'])
        self.assertEqual(result['tool_themes'][0]['candidates'], 2)
        self.assertEqual(result['tool_themes'][0]['verdict'], 'mixed')
        result = merge([doc], [verdict('T:test-theme', 'partly', severity='low')])
        self.assertEqual(len(result['findings']), 2)
        self.assertEqual(result['tool_themes'][0]['verdict'], 'partly')
        self.assertEqual({f['severity'] for f in result['findings']}, {'low'})

    def test_verdicts_and_severity_change(self):
        doc = data(findings=[finding('S1-001'), finding('S1-002', title='Other'), finding('S1-003', title='Unverified')])
        result = merge([doc], [verdict(severity='medium'), verdict('S1-002', 'refuted')])
        self.assertEqual(len(result['findings']), 1)
        kept = result['findings'][0]
        self.assertEqual((kept['filed_severity'], kept['severity']), ('high', 'medium'))
        self.assertEqual({f['verdict'] for f in result['dropped']}, {'refuted', 'unverified'})

    def test_fingerprint_ignores_line_churn_and_title_case(self):
        first = finding()
        other = finding('S2-001', title='  SYNTHETIC   TEST FINDING ', location='internal/auth/test.go:80-90')
        self.assertEqual(fingerprint(first), fingerprint(other))
        other['theme'] = 'different'
        self.assertNotEqual(fingerprint(first), fingerprint(other))

    def test_dedup_keeps_locations_tickets_and_provenance(self):
        other = finding('S2-001', location='internal/auth/test.go:90')
        other['related_tickets'] = ['AEON-1']
        result = merge([data(), data('S2', [other], ())], [verdict(), verdict('S2-001', 'partly')])
        self.assertEqual(len(result['findings']), 1)
        kept = result['findings'][0]
        self.assertEqual(kept['aliases'], ['S2-001'])
        self.assertEqual(len(kept['locations']), 2)
        self.assertEqual(kept['related_tickets'], ['AEON-1'])
        self.assertEqual(result['dropped'][0]['dup_of'], kept['id'])

    def test_deterministic_input_order(self):
        docs = [data(), data('S2', [finding('S2-001', title='Other')], ('web/main.ts',))]
        revs = [verdict(), verdict('S2-001', severity='low')]
        self.assertEqual(merge(docs, revs), merge(docs[::-1], revs[::-1]))

    def test_duplicate_verdict_and_cycle(self):
        doc = data(findings=[finding(), finding('S1-002', title='Other')])
        result = merge([doc], [verdict(), verdict('S1-002', 'duplicate:S1-001')])
        self.assertEqual(result['dropped'][0]['dup_of'], 'S1-001')
        with self.assertRaises(ValueError):
            merge([doc], [verdict(value='duplicate:S1-002'), verdict('S1-002', 'duplicate:S1-001')])
        with self.assertRaises(ValueError):
            merge([doc], [verdict('S1-002', 'duplicate:S1-001')])

    def test_invalid_reviews_fail_closed(self):
        for revs in [[verdict('unknown')], [verdict(), verdict()], [verdict(value='maybe')],
                     [verdict(severity='severe')], [verdict(value='duplicate:missing')], [verdict(evidence='')],
                     [None], [verdict(value=1)]]:
            with self.subTest(revs=revs), self.assertRaises(ValueError):
                merge([data()], revs)

    def test_mixed_snapshots_and_ids(self):
        other = data('S2', [finding('S2-001')], ())
        other['sha'] = 'b' * 40
        with self.assertRaises(ValueError):
            merge([data(), other])
        with self.assertRaises(ValueError):
            merge([data(), data('S2')])

    def test_short_snapshot_prefix_is_preserved_with_full_snapshot(self):
        other = data('S2', [], ())
        other['sha'] = SHA[:12]
        self.assertEqual(merge([data(), other])['snapshot'], SHA)

    def test_legacy_review_envelope(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / 'review.txt'
            path.write_text('Reviewer prose\nBEGIN-VERIFY\n' + json.dumps(verdict()) + '\nEND-VERIFY\n')
            self.assertEqual(reviews(path), [verdict()])


class BaselineAndReportTests(unittest.TestCase):
    def test_cli_pipeline(self):
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp)
            def write(name, value):
                path = directory / name
                path.write_text(json.dumps(value))
                return str(path)

            def run(script, *args):
                result = subprocess.run([sys.executable, '-B', str(HERE / script), *args], capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stderr)

            tools = data('T', [finding('T-001'), finding('T-002', title='New tool candidate')], ())
            known = accept(data('T', [finding('T-001')], ()), {'version': 1, 'fingerprints': []})
            tools_path, baseline_path = write('T.json', tools), write('baseline.json', known)
            new_path = str(directory / 'T-new.json')
            run('baseline.py', 'compare', '--input', tools_path, '--baseline', baseline_path, '--out', new_path)
            self.assertEqual([f['id'] for f in load(new_path)['findings']], ['T-002'])
            audit_path, grouped_path, html_path = [str(directory / f) for f in ('merged.json', 'grouped.json', 'audit.html')]
            run('merge.py', '--manifest', write('S1.json', data()), '--manifest', new_path,
                '--reviews', write('reviews.json', [verdict(), verdict('T-002')]), '--out', audit_path)
            run('groups.py', '--audit', audit_path, '--out', grouped_path)
            run('build.py', '--audit', grouped_path, '--out', html_path)
            result = subprocess.run(['node', str(HERE / 'run.cjs'), html_path], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)

    def test_delta_cli_and_malformed_input_exit_codes(self):
        repo = HERE.parent.parent
        result = subprocess.run([sys.executable, '-B', str(HERE / 'covcheck.py'), '--repo', str(repo),
                                 '--since', 'HEAD', '--summary'], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout)['tracked'], 0)
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / 'bad.json'; path.write_text('not JSON')
            result = subprocess.run([sys.executable, '-B', str(HERE / 'merge.py'), '--manifest', str(path),
                                     '--out', str(Path(temp) / 'out.json')], capture_output=True, text=True)
            self.assertEqual(result.returncode, 2)
            self.assertNotIn('Traceback', result.stderr)

    def test_baseline_reports_only_new_and_retains_known_hashes(self):
        empty = {'version': 1, 'fingerprints': []}
        doc = data('T', [finding('T-001')], ())
        baseline = accept(doc, empty)
        self.assertEqual(set(baseline), {'version', 'fingerprints'})
        self.assertEqual(compare(doc, baseline)['findings'], [])
        moved = copy.deepcopy(doc)
        moved['findings'][0]['locations'] = ['internal/auth/test.go:500']
        self.assertEqual(compare(moved, baseline)['findings'], [])
        moved['findings'][0]['title'] = 'New finding'
        self.assertEqual(len(compare(moved, baseline)['findings']), 1)
        self.assertEqual(accept(data('T', [], ()), baseline), baseline)

    def test_tool_identity_and_invalid_baseline(self):
        one = finding('T-001'); two = {**one, 'tool': 'Other scanner'}
        self.assertNotEqual(fingerprint(one, tool=True), fingerprint(two, tool=True))
        with self.assertRaises(ValueError):
            compare(data('T', [], ()), {'version': 1, 'fingerprints': ['invalid']})

    def test_schema_rejects_malformed_coverage_and_findings(self):
        for field, value in [('lines', -1), ('lines', True), ('status', 'skimmed')]:
            doc = data(); doc['coverage'][0][field] = value
            with self.assertRaises(ValueError):
                manifest(doc)
        doc = data(); doc['findings'][0]['locations'] = ['../outside.go:1']
        with self.assertRaises(ValueError):
            manifest(doc)

    def test_report_group_completeness_and_script_escape(self):
        doc = data(findings=[finding(title='</script><img src=x onerror=alert(1)>')])
        audit = group(merge([doc], [verdict()]))
        html = build(audit)
        self.assertEqual(html, build(audit))
        self.assertNotIn('</script><img', html)
        self.assertIn('\\u003c/script', html)
        self.assertNotIn('fonts.googleapis', html)
        audit['groups'] = []
        with self.assertRaises(ValueError):
            build(audit)

    def test_page_renderer_empty_and_nonempty(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / 'report.html'
            for findings, revs in [([], []), ([finding()], [verdict()])]:
                path.write_text(build(group(merge([data(findings=findings)], revs))))
                result = subprocess.run(['node', str(HERE / 'run.cjs'), str(path)], capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stderr)

    def test_smoke_rejects_a_replaced_renderer(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / 'report.html'
            html = build(group(merge([data(findings=[])], [])))
            path.write_text(html.replace('(() => {', '(() => { throw new Error("must never execute");'))
            result = subprocess.run(['node', str(HERE / 'run.cjs'), str(path)], capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('report must use the bundled renderer', result.stderr)


if __name__ == '__main__':
    unittest.main()
