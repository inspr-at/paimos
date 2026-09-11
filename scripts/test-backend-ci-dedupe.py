#!/usr/bin/env python3
"""Regression checks for PAI-1000 coverage ownership and quality inputs."""
import importlib.util
import os
from pathlib import Path
import re
import shlex
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent.parent
MODULE = 'github.com/inspr-at/paimos/backend'
GO = os.environ.get('GO_COMMAND', 'go')


def run(*args, **kwargs):
    return subprocess.check_output(args, cwd=ROOT, text=True, **kwargs).strip()


def load(name):
    spec = importlib.util.spec_from_file_location(name, ROOT / 'scripts' / f'{name}.py')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


quality = load('quality-ci-inputs')
race_guards = load('backend-race-exclusions')
workflow = (ROOT / '.github/workflows/ci-v2.yml').read_text()


def job(name):
    return re.search(rf'^  {name}:\n(.*?)(?=^  [\w-]+:|\Z)',
                     workflow, re.M | re.S)[1]


class DedupeTests(unittest.TestCase):
    def test_quality_inputs_and_clock_exceptions(self):
        for path in ('unrelated.txt', 'LICENSE'):
            result = quality.select([path])
            self.assertEqual({k for k, v in result.items() if v}, {'release', 'release_version'})
        for key, paths in quality.INPUTS.items():
            for path in paths:
                with self.subTest(check=key, path=path):
                    self.assertTrue(quality.select([path + ('fixture' if path.endswith(('/', '-')) else '')])[key])
        self.assertTrue(all(quality.select(None).values()))
        self.assertTrue(all(quality.select(['.github/workflows/ci-v2.yml']).values()))
        # Real doc/schema edits must activate the fixture-backed live contract.
        for path in ('docs/INSTALL.md', 'backend/handlers/schema.go', 'VERSION'):
            self.assertTrue(quality.select([path])['knowledge_freshness'])
        self.assertFalse(quality.select(['docs/INSTALL.md'])['dev_login'])
        self.assertTrue(quality.select(['backend/go.sum'])['dev_login'])
        # Independent dependency cases, not just enumeration of the mapping.
        for path in ('scripts/testdata/backend-gate/fail-go-list-test.sh',
                     'scripts/test-backend-full-reuse.py', 'scripts/wait-backend-full.sh',
                     '.github/workflows/release-v2.yml', 'backend/go.mod',
                     'backend/go.sum', 'Dockerfile', 'Justfile'):
            self.assertTrue(all(quality.select([path]).values()), path)
        for path in ('backend/auth/any_new_file.go', 'backend/unrelated/helper.go',
                     'backend/dev_login_dev.go', '.github/workflows/ci-v2.yml'):
            self.assertTrue(quality.select([path])['dev_login'], path)
        # An unavailable base cannot turn into a trusted empty diff.
        output = run('python3', 'scripts/quality-ci-inputs.py', 'missing-pai1000-ref', 'HEAD',
                     stderr=subprocess.DEVNULL)
        self.assertNotIn('=false', output)

    def test_quality_workflow_wiring(self):
        block = job('quality')
        for key in quality.INPUTS:
            self.assertIn(f"if: steps.inputs.outputs.{key} == 'true'", block)
        self.assertIn('"$PR_BASE" "$PR_HEAD" >> "$GITHUB_OUTPUT"', block)
        for script in ('test-release.sh', 'test-release-version.sh'):
            step = next(s for s in block.split('      - ') if f'./scripts/{script}' in s)
            self.assertNotIn('if:', step)
        for lane in ('backend-pr', 'backend-pr-db', 'backend-pr-handlers', 'backend-pr-performance'):
            self.assertIn('needs.backend-pr-plan.outputs.direct_selection', job(lane))
            self.assertIn('--direct-packages="$direct_selection"', job(lane))
        invariants = job('backend-security-invariants')
        self.assertNotIn('backend-security-invariants.sh', invariants)
        self.assertNotIn('TestRegression_', invariants)
        self.assertNotIn('TestAuthzFuzz_', invariants)
        self.assertEqual(invariants.count('go test '), 2)
        self.assertIn('-tags paimos_test_unsupported', invariants)
        self.assertNotIn('go test', job('backend-publish-invariants'))
        for lane in ('quality', 'frontend-quality', 'e2e'):
            self.assertRegex(job(lane), r"(?m)^    if: github.event_name == 'pull_request'\n")
        self.assertIn("github.ref == 'refs/heads/main'", job('security-scan'))
        self.assertIn("needs['security-scan'].result == 'success'", job('docker'))

    def test_aggregator_requires_correct_results(self):
        block = job('test')
        script = block.split('        run: |\n', 1)[1]
        keys = re.findall(r'^          (\w+): \$\{\{ needs\.[\w-]+\.result', block, re.M)
        planned = re.findall(r'^          (\w+_PLANNED):', block, re.M)
        for event in ('pull_request', 'push'):
            statuses = {key: ('success' if event == 'pull_request' else 'skipped') for key in keys}
            statuses.update({key: 'true' if event == 'pull_request' else '' for key in planned})
            statuses['BACKEND_PUBLISH_INVARIANTS'] = 'skipped' if event == 'pull_request' else 'success'
            for key in ('QUALITY', 'FRONTEND_QUALITY', 'E2E'):
                self.assertIn(key, statuses)
            def check(values):
                return subprocess.run(['bash', '-c', script], env={**os.environ, **values,
                                      'EVENT_NAME': event, 'GITHUB_REF_TYPE': 'branch'}).returncode
            self.assertEqual(check(statuses), 0)
            for key in keys:
                with self.subTest(event=event, lane=key):
                    self.assertNotEqual(check({**statuses, key: 'failure'}), 0)
                    opposite = 'skipped' if statuses[key] == 'success' else 'success'
                    self.assertNotEqual(check({**statuses, key: opposite}), 0)

    def test_pinned_scans_are_pr_only_live_databases_stay_on_main(self):
        block = job('security-scan')
        for name, pin in (('gitleaks', 'GITLEAKS_VERSION: 8.30.1'),
                          ('gosec', 'gosec@v2.27.1')):
            step = next(s for s in block.split('      - ') if s.startswith('name: ' + name))
            self.assertIn("if: github.event_name == 'pull_request'", step)
            self.assertIn(pin, step)
        for name in ('govulncheck', 'npm audit'):
            step = next(s for s in block.split('      - ') if s.startswith('name: ' + name))
            self.assertNotIn('if:', step)

    def test_race_guard_current_tree_is_a_visible_gate(self):
        self.assertEqual(run('python3', 'scripts/backend-race-exclusions.py',
                             '--check', str(ROOT / 'backend')), '')

    def test_race_guard_recognizes_only_execution_dependencies(self):
        for source in ('//go:build !race', '// +build linux,!race',
                       '//go:build linux && (race || cgo)',
                       'import "runtime/race"', 'import ( r "runtime/race" )',
                       'if raceEnabled { t.Skip("race") }',
                       'const race_enabled = true'):
            with self.subTest(source=source):
                self.assertTrue(race_guards.inspect(source)[0])
        for source in ('debug.ReadBuildInfo()', 'ReadBuildInfo()',
                       '// raceEnabled is documented here',
                       's := "raceEnabled"', 's := "runtime/race"',
                       '//go:build tracerace', '/* //go:build race */'):
            with self.subTest(source=source):
                self.assertFalse(race_guards.inspect(source)[0])

    def test_race_dependent_helpers_keep_only_importing_packages(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / 'scripts').mkdir()
            backend = root / 'backend'
            backend.mkdir()
            (backend / 'go.mod').write_text('module ' + MODULE + '\n')
            for package, source in {
                'helper': '//go:build !race\npackage helper\nconst Instrumented = false\n',
                'middle': 'package middle\nimport _ "' + MODULE + '/helper"\n',
                'localjournal': 'package localjournal_test\nimport _ "' + MODULE + '/middle"\n',
                'unrelated': 'package unrelated\n',
            }.items():
                (backend / package).mkdir()
                (backend / package / 'guard_test.go').write_text(source)
            for name in ('backend-pr-test.sh', 'backend-pr-race.sh', 'backend-race-exclusions.py'):
                dest = root / 'scripts' / name
                dest.write_bytes((ROOT / 'scripts' / name).read_bytes())
                dest.chmod(0o755)
            fake_go = root / 'go'
            fake_go.write_text('#!/bin/sh\nprintf "%s\\n" TestOne TestTwo\n')
            fake_go.chmod(0o755)
            command = ['bash', str(root / 'scripts/backend-pr-test.sh'), '--dry-run',
                       '--lane=affected', '--shard=0/2']
            def plan(package):
                return run(*command, '--direct-packages=' + MODULE + '/' + package,
                           MODULE + '/' + package,
                           env={**os.environ, 'GO_COMMAND': str(fake_go)})
            self.assertEqual(race_guards.exclusions(backend)[2],
                             {MODULE + '/' + p for p in ('helper', 'middle', 'localjournal')})
            self.assertIn(MODULE + '/localjournal', plan('localjournal'))
            self.assertEqual(plan('unrelated'), '')
            check = subprocess.run(['python3', str(root / 'scripts/backend-race-exclusions.py'),
                                    '--check', str(backend)], capture_output=True, text=True)
            self.assertNotEqual(check.returncode, 0)
            self.assertIn(MODULE + '/helper', check.stderr)

    def test_normal_and_race_partition_real_tests(self):
        # Exercise all special masks and a default full-package race target.
        for name in ('handlers', 'db', 'cmd/paimos', 'supervision', 'agentmessage',
                     'auth', 'externalstage', 'managedharness', 'localjournal',
                     '', 'lifecycleintents', 'delivery', 'baselinebatch', 'releaseacceptance', 'agentmode'):
            package = MODULE + ('/' + name if name else '')
            relative = './' + name if name else '.'
            with self.subTest(package=name):
                listed = subprocess.check_output(
                                 [GO, 'test', '-list', '^(Test|Fuzz|Example)', relative],
                                 cwd=ROOT / 'backend', text=True)
                names = set(re.findall(r'^(?:Test|Fuzz|Example)\w*$', listed, re.M))
                race = run('bash', 'scripts/backend-pr-race.sh', '--dry-run', package)
                lane, count = ('handlers', 5) if name == 'handlers' else ('db', 1) if name == 'db' else ('affected', 2)
                normal = []
                for shard in range(count):
                    args = ['bash', 'scripts/backend-pr-test.sh', '--dry-run', '--lane=' + lane,
                            '--direct-packages=' + package]
                    if lane != 'db': args.append(f'--shard={shard}/{count}')
                    normal.append(run(*args, package))
                def selected(plan):
                    result = []
                    for line in plan.splitlines():
                        if not line.startswith('go test '): continue
                        args = shlex.split(line)
                        pattern = args[args.index('-run') + 1] if '-run' in args else '.'
                        skip = args[args.index('-skip') + 1] if '-skip' in args else '(?!)'
                        if '/' in pattern: continue  # Stream subtests checked below.
                        result.extend(n for n in names if re.search(pattern, n) and not re.search(skip, n))
                    return result
                normal_names, race_names = selected('\n'.join(normal)), selected(race)
                expected = names.copy()
                if name == 'agentmode':
                    expected.remove('TestStreamSubscribeRaceOverflowLostWakeRestartAndPermissionChanges')
                    self.assertNotIn('subscribe\\ before\\ high-water', '\n'.join(normal))
                    perf = run('bash', 'scripts/backend-pr-test.sh', '--dry-run', '--lane=performance',
                               '--direct-packages=' + package, package)
                    self.assertIn('overflow\\ lost\\ wake\\ coalescing\\ and\\ restart', perf)
                if name == 'db':
                    for oracle in ('TestM147ConcurrentCanonicalCommandsConverge',
                                   'TestM147ConcurrentRuntimeAcceptanceHasOneEffectOwner'):
                        self.assertIn(oracle, normal_names)
                        self.assertIn(oracle + 'ProductionPool', race_names)
                self.assertEqual(set(normal_names) & set(race_names), set())
                self.assertEqual(set(normal_names) | set(race_names), expected)
                self.assertEqual(len(normal_names + race_names), len(expected))


if __name__ == '__main__':
    unittest.main()
