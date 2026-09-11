#!/usr/bin/env python3
"""PAI-1003: exact-code release evidence and runner-free merge verification."""
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent.parent
HEAD = '1' * 40
OTHER = '2' * 40
REQUIRED = ('backend-full-authorize', 'backend-full-serial', 'backend-full',
            'backend-full-race (core)', 'backend-full-race (handlers)',
            'backend-full-race (runtime)')


class EvidenceTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.path = Path(self.tmp.name)
        self.run = dict(databaseId=42, headSha=HEAD, event='schedule',
                        headBranch='main', status='completed', conclusion='success',
                        displayTitle='backend-full ' + HEAD)
        self.jobs = [dict(name=name, status='completed', conclusion='success')
                     for name in REQUIRED]
        self.gh = self.path / 'gh'
        self.gh.write_text('''#!/usr/bin/env python3
import json, os, pathlib, sys
p = pathlib.Path(os.environ['EVIDENCE_FIXTURE'])
args = sys.argv[1:]
with (p / 'calls').open('a') as f: f.write(json.dumps(args) + '\\n')
if args[:2] == ['workflow', 'run']:
    if os.environ.get('DISPATCH_FAIL') == '1': sys.exit(1)
    assert '--ref' in args and args[args.index('--ref') + 1] == 'main'
    head = next(a.split('=', 1)[1] for a in args if a.startswith('target_sha='))
    run = dict(databaseId=43, headSha='2'*40, event='workflow_dispatch',
               headBranch='main', displayTitle='backend-full ' + head,
               status='completed', conclusion='success')
    (p / 'dispatch').write_text(json.dumps([run]))
elif args[:2] == ['run', 'list']:
    if '--commit' in args:
        print((p / 'runs').read_text())
    else:
        assert args[args.index('--branch') + 1] == 'main'
        print((p / 'dispatch').read_text())
elif args[:2] == ['run', 'view']:
    print((p / 'jobs').read_text())
else:
    sys.exit(64)
''')
        self.gh.chmod(0o755)
        sleep = self.path / 'sleep'
        sleep.write_text('#!/bin/sh\necho sleep >> "$EVIDENCE_FIXTURE/calls"\n'
                         'test "$ALLOW_FIXTURE_SLEEP" = 1\n')
        sleep.chmod(0o755)

    def check(self, runs=None, dispatch=None, mode='--check', **env):
        (self.path / 'runs').write_text(json.dumps([self.run] if runs is None else runs))
        (self.path / 'dispatch').write_text(json.dumps(dispatch or []))
        (self.path / 'jobs').write_text(json.dumps({'jobs': self.jobs}))
        (self.path / 'calls').write_text('')
        return subprocess.run(['bash', str(ROOT / 'scripts/wait-backend-full.sh'), mode, HEAD],
                              capture_output=True, text=True, timeout=10,
                              env={**os.environ, 'GH_COMMAND': str(self.gh),
                                   'EVIDENCE_FIXTURE': str(self.path),
                                   'GITHUB_ACTIONS': 'false',
                                   'PATH': str(self.path) + ':' + os.environ['PATH'],
                                   'BACKEND_FULL_TIMEOUT_SECONDS': '2',
                                   'BACKEND_FULL_POLL_SECONDS': '0', **env})

    def test_actual_exact_head_or_pinned_dispatch_is_accepted(self):
        self.assertEqual(self.check().returncode, 0)
        self.run.update(event='workflow_dispatch', headSha=OTHER)
        self.assertEqual(self.check(runs=[], dispatch=[self.run]).returncode, 0)
        self.run.update(event='pull_request', headSha=HEAD, headBranch='feature')
        self.assertEqual(self.check().returncode, 0)

    def test_dispatch_head_metadata_cannot_substitute_for_checkout(self):
        self.run.update(event='workflow_dispatch', displayTitle='backend-full ' + OTHER)
        self.assertNotEqual(self.check().returncode, 0)
        self.run.update(displayTitle='backend-full ' + HEAD, headBranch='feature')
        self.assertNotEqual(self.check().returncode, 0)

    def test_every_full_execution_job_is_required(self):
        for job in self.jobs:
            for conclusion in ('skipped', 'cancelled', 'failure'):
                with self.subTest(job=job['name'], conclusion=conclusion):
                    job['conclusion'] = conclusion
                    self.assertNotEqual(self.check().returncode, 0)
            job['conclusion'] = 'success'
        self.jobs = [self.jobs[2]]
        self.assertNotEqual(self.check().returncode, 0)  # Old reuse-only aggregator.

    def test_tag_check_never_sleeps_or_dispatches(self):
        for status, conclusion in (('queued', None), ('in_progress', None),
                                   ('completed', 'failure'), ('completed', 'cancelled')):
            self.run.update(status=status, conclusion=conclusion)
            self.assertNotEqual(self.check(GITHUB_ACTIONS='true').returncode, 0)
            calls = (self.path / 'calls').read_text()
            self.assertNotIn('sleep', calls)
            self.assertNotIn('"workflow", "run"', calls)
        self.assertNotEqual(self.check(runs=[]).returncode, 0)
        self.run.update(status='completed', conclusion='success', headSha=OTHER)
        self.assertNotEqual(self.check().returncode, 0)

    def test_only_operator_can_dispatch_and_wait(self):
        result = self.check(runs=[], mode='--dispatch', ALLOW_FIXTURE_SLEEP='1')
        self.assertEqual(result.returncode, 0, result.stderr)
        calls = (self.path / 'calls').read_text()
        self.assertEqual(calls.count('"workflow", "run"'), 1)
        self.assertIn('target_sha=' + HEAD, calls)
        self.assertIn('sleep', calls)
        self.assertNotEqual(self.check(runs=[], mode='--dispatch', GITHUB_ACTIONS='true').returncode, 0)
        self.assertEqual((self.path / 'calls').read_text(), '')
        self.assertNotEqual(self.check(runs=[], mode='--dispatch', DISPATCH_FAIL='1').returncode, 0)
        self.assertNotIn('sleep', (self.path / 'calls').read_text())

    def test_resume_uses_green_evidence_without_dispatch(self):
        self.assertEqual(self.check(mode='--dispatch').returncode, 0)
        self.assertNotIn('"workflow", "run"', (self.path / 'calls').read_text())

    def test_nightly_and_release_checkout_and_failure_contract(self):
        workflow = (ROOT / '.github/workflows/backend-full.yml').read_text()
        self.assertNotRegex(workflow, r'(?m)^  push:')
        self.assertIn('run-name: backend-full ${{ inputs.target_sha || github.event.pull_request.head.sha || github.sha }}', workflow)
        self.assertEqual(workflow.count('ref: ${{ needs.backend-full-authorize.outputs.source_sha }}'), 2)
        self.assertIn('git merge-base --is-ancestor "$target" origin/main', workflow)
        for forbidden in ('run_full', 'backend-full-reuse', 'continue-on-error', 'sleep '):
            self.assertNotIn(forbidden, workflow)
        aggregate = re.search(r'^  backend-full:\n(.*)', workflow, re.M | re.S)[1]
        script = aggregate.split('        run: |\n')[1]
        for failed in ('AUTHORIZATION', 'SERIAL', 'RACE'):
            for status in ('failure', 'cancelled', 'skipped', ''):
                result = subprocess.run(['bash', '-c', script], capture_output=True,
                                        env={**os.environ, 'AUTHORIZATION': 'success',
                                             'SERIAL': 'success', 'RACE': 'success', failed: status})
                self.assertNotEqual(result.returncode, 0)
        release = (ROOT / 'scripts/release.sh').read_text()
        dispatch = '"$ROOT/scripts/wait-backend-full.sh" --dispatch "$TAG_OID"'
        self.assertLess(release.index(dispatch), release.index('tag_release_merge "$TAG_OID"'))


if __name__ == '__main__':
    unittest.main()
