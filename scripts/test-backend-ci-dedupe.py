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
    def test_full_tree_fallback_does_not_elide_packages_outside_broad_race(self):
        with tempfile.TemporaryDirectory() as tmp:
            fake = Path(tmp) / 'go'
            fake.write_text('''#!/usr/bin/env python3
import sys
if sys.argv[1] == 'list':
    print('github.com/inspr-at/paimos/backend/contracts')
else:
    assert sys.argv[1:3] == ['test', '-list']
    print('TestConcurrentContract')
''')
            fake.chmod(0o755)
            normal = run('bash', 'scripts/backend-pr-test.sh', '--dry-run',
                         '--lane=affected', '--shard=0/2', '--direct-packages=./...', './...',
                         env={**os.environ, 'GO_COMMAND': str(fake)})
            self.assertIn(MODULE + '/contracts', normal)
            self.assertNotIn('TestConcurrentContract', normal)

    def test_dependency_only_race_is_bounded_exact_and_deduplicated(self):
        # Include every semantic marker, an ordinary test, a partial stream
        # contract, and packages with fewer tests than runners or no contracts.
        names = ['TestConcurrentCreate', 'TestConcurrencyLease', 'TestWriteRace',
                 'TestAtomicCommit', 'TestReplayReceipt', 'TestRecoversAfterCrash',
                 'TestRecoveryJournal', 'TestSerialRegistration']
        with tempfile.TemporaryDirectory() as tmp:
            fake = Path(tmp) / 'go'
            fake.write_text('''#!/usr/bin/env python3
import re, sys
args = sys.argv[1:]
assert args[:2] == ['test', '-list']
pattern, package = args[2:4]
names = %r
if package.endswith('/empty'): names = ['TestSerialRegistration']
if package.endswith('/small'): names = ['TestReplayReceipt']
if package.endswith('/agentmode'):
    names = ['TestConcurrentCreate', 'TestStreamSubscribeRaceOverflowLostWakeRestartAndPermissionChanges']
for name in names:
    if re.search(pattern, name): print(name)
''' % names)
            fake.chmod(0o755)
            fixture_env = {**os.environ, 'GO_COMMAND': str(fake)}
            direct = '--direct-packages=' + MODULE + '/db'
            def race(*args):
                return run('bash', 'scripts/backend-pr-race.sh', *args, env=fixture_env)
            expected = set(names[:-1])
            for package, count, lane in (('consumer', 4, 'affected'),
                                         ('handlers', 5, 'handlers'),
                                         ('managedharness', 7, 'managedharness')):
                with self.subTest(package=package):
                    full = race('--dry-run', direct, MODULE + '/' + package)
                    shards = [race('--dry-run', '--lane=' + lane, f'--shard={i}/{count}',
                                   direct, MODULE + '/' + package) for i in range(count)]
                    self.assertEqual(sorted(full.splitlines()),
                                     sorted(line for shard in shards for line in shard.splitlines()))
                    selected = re.findall(r'Test\w+', full)
                    self.assertEqual(set(selected), expected)
                    self.assertEqual(len(selected), len(expected))
                    for line in full.splitlines():
                        self.assertIn('-run', shlex.split(line))
                        self.assertLessEqual(len(re.findall(r'Test\w+', line)), 4)
                    coverage = race('--coverage', direct, MODULE + '/' + package)
                    self.assertEqual(set(re.findall(r'Test\w+', coverage)), expected)
            self.assertEqual(race('--dry-run', direct, MODULE + '/empty'), '')
            self.assertEqual(race('--coverage', direct, MODULE + '/empty'), '')
            self.assertEqual(race('--dry-run', '--lane=affected', '--shard=3/4',
                                  direct, MODULE + '/small'), '')
            # A default direct target still races its whole package.
            self.assertNotIn('-run', race('--dry-run', '--direct-packages=' + MODULE + '/consumer',
                                           MODULE + '/consumer'))
            normal = run('bash', 'scripts/backend-pr-test.sh', '--dry-run',
                         '--lane=affected', '--shard=0/2', direct, MODULE + '/consumer',
                         env=fixture_env)
            args = shlex.split(normal)
            skip = args[args.index('-skip') + 1]
            self.assertEqual({n for n in names if re.search(skip, n)}, expected)
            self.assertFalse(re.search(skip, 'TestSerialRegistration'))
            stream = race('--coverage', direct, MODULE + '/agentmode')
            self.assertIn('/(subscribe before high-water|permission grant and revoke)$', stream)
            self.assertNotIn('overflow lost wake', stream)
            self.assertNotIn('Changes$\n', stream + '\n')

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
                     'scripts/test-backend-full-evidence.py', 'scripts/wait-backend-full.sh',
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
        for event, ref_type in (('pull_request', 'branch'), ('push', 'branch'), ('push', 'tag')):
            statuses = {key: ('success' if event == 'pull_request' else 'skipped') for key in keys}
            statuses.update({key: 'true' if event == 'pull_request' else '' for key in planned})
            statuses['BACKEND_PUBLISH_INVARIANTS'] = 'success' if ref_type == 'tag' else 'skipped'
            for key in ('QUALITY', 'FRONTEND_QUALITY', 'E2E'):
                self.assertIn(key, statuses)
            def check(values):
                return subprocess.run(['bash', '-c', script], env={**os.environ, **values,
                                      'EVENT_NAME': event, 'GITHUB_REF_TYPE': ref_type, 'GITHUB_REF': 'refs/heads/main'}).returncode
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
