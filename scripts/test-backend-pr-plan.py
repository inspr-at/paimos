#!/usr/bin/env python3
"""PAI-1002: empty lanes must be skipped before allocating any runner."""
import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location('planner', ROOT / 'scripts/backend-pr-plan.py')
planner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(planner)
WORKFLOW = (ROOT / '.github/workflows/ci-v2.yml').read_text()
MODULE = 'github.com/inspr-at/paimos/backend'
# Deliberately independent pins: topology changes need a review of both the
# execution command and runner allocation, including PAI-1000's normal masks.
LANES = {
    'backend-pr': ('test', 'affected', 2),
    'backend-pr-db': ('test', 'db', None),
    'backend-pr-handlers': ('test', 'handlers', 5),
    'backend-pr-performance': ('test', 'performance', None),
    'backend-pr-race': ('race', 'affected', 4),
    'backend-pr-managedharness-race': ('race', 'managedharness', 7),
    'backend-pr-db-race': ('race', 'db', None),
    'backend-pr-handlers-race': ('race', 'handlers', 5),
}


def job(name):
    return re.search(rf'^  {name}:\n(.*?)(?=^  [\w-]+:|\Z)', WORKFLOW, re.M | re.S)[1]


class PlanTests(unittest.TestCase):
    def test_job_allocation_cannot_regress_to_step_only_skips(self):
        plan = job('backend-pr-plan')
        self.assertIn("    if: github.event_name == 'pull_request'\n", plan)
        self.assertIn('fetch-depth: 0', plan)
        self.assertIn('PR_BASE: ${{ github.event.pull_request.base.sha }}', plan)
        self.assertIn('PR_HEAD: ${{ github.event.pull_request.head.sha }}', plan)
        self.assertIn('backend-pr-plan.py "$PR_BASE" "$PR_HEAD" >> "$GITHUB_OUTPUT"', plan)
        for name, (kind, lane, count) in LANES.items():
            with self.subTest(job=name):
                block = job(name)
                prefix = f'needs.backend-pr-plan.outputs.{name}'
                self.assertIn('    needs: backend-pr-plan\n', block)
                self.assertIn(f"    if: github.event_name == 'pull_request' && {prefix} == 'true'\n", block)
                self.assertNotIn('continue-on-error:', block)
                self.assertNotIn('backend-ci-packages.sh', block)
                self.assertIn(f'{name}: ${{{{ steps.plan.outputs.{name} }}}}', plan)
                selected = 'selection' if kind == 'test' else 'direct_selection'
                self.assertIn(f'selection: ${{{{ needs.backend-pr-plan.outputs.{selected} }}}}', block)
                self.assertIn(f'backend-pr-{kind}.sh --lane={lane}', block)
                if kind == 'test':
                    self.assertIn('--direct-packages="$direct_selection"', block)
                if count:
                    self.assertIn(f'--shard="${{{{ matrix.shard }}}}/{count}"', block)
                    self.assertIn(f"shard: ${{{{ fromJSON({prefix} == 'true' && {prefix}-shards || '[0]') }}}}", block)
                    self.assertIn(f'{name}-shards: ${{{{ steps.plan.outputs.{name}-shards }}}}', plan)
                    self.assertIn('fail-fast: false', block)
        # These checks have unconditional PR invocations and must not be
        # weakened by the selection plan, even when every package lane is empty.
        for name in ('backend-pr-vet', 'backend-security-invariants',
                     'quality', 'frontend-quality', 'e2e', 'dco'):
            self.assertIn("    if: github.event_name == 'pull_request'\n", job(name))
            self.assertNotIn('needs:', job(name))
        self.assertIn("    if: github.ref == 'refs/heads/main' || github.event_name == 'pull_request'\n",
                      job('security-scan'))

    def test_same_selection_once_and_each_exact_lane_dry_run(self):
        calls = []

        def fake_run(args, input=None):
            calls.append(args)
            if args[0].endswith('backend-ci-packages.sh'):
                return MODULE + ('/direct' if '--direct' in args else '/affected')
            return 'go test selected' if '--shard=0/2' in args else ''

        with patch.object(planner, 'run', side_effect=fake_run):
            result = planner.plan(['base-sha', 'head-sha'])
        self.assertEqual(calls[:2], [
            [str(planner.SCRIPTS / 'backend-ci-packages.sh'), 'base-sha', 'head-sha'],
            [str(planner.SCRIPTS / 'backend-ci-packages.sh'), '--direct', 'base-sha', 'head-sha'],
        ])
        expected = []
        for name, (kind, lane, count) in LANES.items():
            for shard in range(count or 1):
                args = [str(planner.SCRIPTS / f'backend-pr-{kind}.sh'), '--dry-run', f'--lane={lane}']
                if count:
                    args.append(f'--shard={shard}/{count}')
                if kind == 'test':
                    args.append('--direct-packages=' + MODULE + '/direct')
                args.append(MODULE + ('/affected' if kind == 'test' else '/direct'))
                expected.append(args)
        self.assertEqual(calls[2:], expected)
        self.assertEqual(result['backend-pr'], 'true')
        self.assertEqual(json.loads(result['backend-pr-shards']), [0])
        self.assertTrue(all(result[name] == 'false' for name in LANES if name != 'backend-pr'))

    def test_real_simulated_prs(self):
        expected_active = {
            'backend/handlers/issues.go': {'backend-pr', 'backend-pr-handlers', 'backend-pr-handlers-race'},
            'frontend/src/main.ts': set(),
            'docs/INSTALL.md': set(),
        }
        for path, active in expected_active.items():
            with self.subTest(path=path):
                result = planner.plan(['--files-from', '-'], path + '\n')
                self.assertEqual({name for name in LANES if result[name] == 'true'}, active)
                if active:
                    self.assertEqual(result['direct_selection'], MODULE + '/handlers')
                    self.assertIn(MODULE + '/handlers', result['selection'].splitlines())
                    self.assertEqual(json.loads(result['backend-pr-shards']), [0, 1])
                    for name in ('backend-pr-handlers', 'backend-pr-handlers-race'):
                        self.assertEqual(json.loads(result[name + '-shards']), list(range(5)))
                else:
                    self.assertEqual(result['selection'], '')
                    self.assertEqual(result['direct_selection'], '')
                for name, (_, _, count) in LANES.items():
                    if count and name not in active:
                        self.assertEqual(json.loads(result[name + '-shards']), [])

    def test_selector_and_discovery_failures_never_emit_an_empty_plan(self):
        for fixture in ('fail-command.sh', 'no-sentinel.sh'):
            result = subprocess.run(
                ['python3', str(planner.SCRIPTS / 'backend-pr-plan.py'), 'HEAD', 'HEAD'],
                env={**os.environ, 'BACKEND_SELECTOR_COMMAND': str(planner.SCRIPTS / 'testdata/backend-gate' / fixture)},
                capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(result.stdout, '')
        for successful_calls in (1, 2, 3):
            with patch.object(planner, 'run', side_effect=[MODULE] * successful_calls +
                              [subprocess.CalledProcessError(1, 'discovery')]):
                with self.assertRaises(subprocess.CalledProcessError):
                    planner.plan(['base', 'head'])

    def test_empty_cli_outputs_remain_explicit(self):
        output = subprocess.check_output(
            ['python3', str(planner.SCRIPTS / 'backend-pr-plan.py'), '--files-from', '-'],
            input='frontend/src/main.ts\n', text=True)
        lines = iter(output.splitlines())
        outputs = {}
        for line in lines:
            if '<<' in line:
                name, delimiter = line.split('<<', 1)
                value = []
                for line in lines:
                    if line == delimiter:
                        break
                    value.append(line)
                else:
                    self.fail('unterminated GitHub output')
                outputs[name] = '\n'.join(value)
            else:
                name, value = line.split('=', 1)
                outputs[name] = value
        self.assertEqual(outputs['selection'], '')
        self.assertEqual(outputs['direct_selection'], '')
        for name, (_, _, count) in LANES.items():
            self.assertEqual(outputs[name], 'false')
            if count:
                self.assertEqual(outputs[name + '-shards'], '[]')

    def test_aggregator_fails_closed_for_unplanned_skips_and_bad_plans(self):
        block = job('test')
        self.assertIn('    if: always()\n', block)
        self.assertIn('      - backend-pr-plan\n', block)
        self.assertIn('BACKEND_PR_PLAN: ${{ needs.backend-pr-plan.result }}', block)
        script = block.split('        run: |\n', 1)[1]
        keys = re.findall(r'^          (\w+): \$\{\{ needs\.[\w-]+\.result', block, re.M)
        statuses = {key: 'success' for key in keys}
        statuses['BACKEND_PUBLISH_INVARIANTS'] = 'skipped'
        for name in LANES:
            key = name.upper().replace('-', '_')
            self.assertIn(f'{key}_PLANNED: ${{{{ needs.backend-pr-plan.outputs.{name} }}}}', block)
            self.assertIn(f'      - {name}\n', block)
            statuses[key + '_PLANNED'] = 'false'
            statuses[key] = 'skipped'

        def check(updates):
            return subprocess.run(['bash', '-c', script], capture_output=True,
                                  env={**os.environ, **statuses, **updates,
                                       'EVENT_NAME': 'pull_request'}).returncode

        self.assertEqual(check({}), 0)
        for result in ('failure', 'cancelled', 'skipped', ''):
            self.assertNotEqual(check({'BACKEND_PR_PLAN': result}), 0)
        for name in LANES:
            key = name.upper().replace('-', '_')
            with self.subTest(job=name):
                self.assertEqual(check({key + '_PLANNED': 'true', key: 'success'}), 0)
                for planned in ('true', '', 'garbage'):
                    self.assertNotEqual(check({key + '_PLANNED': planned}), 0)
                for result in ('success', 'failure', 'cancelled', ''):
                    self.assertNotEqual(check({key: result}), 0)
                for result in ('skipped', 'failure', 'cancelled', ''):
                    self.assertNotEqual(check({key + '_PLANNED': 'true', key: result}), 0)
        for key in ('BACKEND_PR_VET', 'BACKEND_SECURITY_INVARIANTS', 'QUALITY', 'FRONTEND_QUALITY', 'E2E'):
            self.assertNotEqual(check({key: 'skipped'}), 0)


if __name__ == '__main__':
    unittest.main()
