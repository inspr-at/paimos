# SPDX-License-Identifier: AGPL-3.0-only
import contextlib
import importlib.util
import io
import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import subprocess
import sys
import threading
import tempfile
import unittest
import uuid
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location('migration_compat_probe', Path(__file__).with_name('migration-compat-probe.py'))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class ProbeTest(unittest.TestCase):
    def setUp(self):
        self.requests = []
        self.posts = []
        self.responses = {
            '/': (200, 'text/html', b'<html>previous SPA</html>'),
            '/api/health': {'status': 'ok', 'db': 'ok'},
            '/api/ready': {'status': 'ready'},
            '/api/version': {'version': '260930115354.0.0'},
            '/api/auth/dev-login': {}, '/api/me': {}, '/api/me/permissions': {},
            '/api/members': {}, '/api/kinds': {},
            '/api/projects?include_archived=true': {'items': [{'id': 'project'}]},
            '/api/nodes?kind=project&limit=100': {'items': [{'id': 'project'}]},
            '/api/nodes?parent_id=project&limit=100': {'items': [{'id': 'ticket'}]},
            '/api/nodes/project': {'id': 'project'}, '/api/nodes/ticket': {'id': 'ticket'},
        }
        owner = self

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                owner.requests.append(self.path)
                value = owner.responses.get(self.path, (404, 'application/json', b'{}'))
                if callable(value):
                    value = value()
                if isinstance(value, dict):
                    value = (200, 'application/json', json.dumps(value).encode())
                status, content_type, body = value
                self.send_response(status)
                self.send_header('Content-Type', content_type)
                self.end_headers()
                self.wfile.write(body)

            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers.get('Content-Length', 0))))
                owner.posts.append((self.path, body))
                self.do_GET()

            def log_message(self, *_args):
                pass

        self.server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.probe = module.Probe(f'http://127.0.0.1:{self.server.server_port}')

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join()

    def check(self):
        with contextlib.redirect_stdout(io.StringIO()):
            self.probe.check({'project': 'project', 'ticket': 'ticket'}, '260930115354.0.0')

    def seed(self, kinds):
        self.responses['/api/kinds'] = {'items': [
            {'slug': slug, 'id': 'kind-' + slug} for slug in kinds]}
        self.responses['/api/nodes'] = lambda: {
            'id': 'ticket' if self.posts[-1][1].get('parent_id') else 'project'}
        with contextlib.redirect_stdout(io.StringIO()):
            state = self.probe.seed()
            self.probe.check(state, '260930115354.0.0')
        self.assertEqual(state, {'project': 'project', 'ticket': 'ticket'})
        creates = [body for path, body in self.posts if path == '/api/nodes']
        self.assertEqual(len(creates), 2)
        self.assertEqual(creates[0]['kind_id'], 'kind-project')
        self.assertEqual(creates[1]['parent_id'], state['project'])
        return creates[1]

    def test_seed_uses_canonical_work_kind_from_release_123(self):
        self.assertEqual(self.seed(['project', 'work'])['kind_id'], 'kind-work')

    def test_seed_prefers_canonical_work_when_ticket_is_also_present(self):
        self.assertEqual(self.seed(['project', 'work', 'ticket'])['kind_id'], 'kind-work')

    def test_seed_supports_previous_releases_with_ticket_kind(self):
        self.assertEqual(self.seed(['project', 'ticket'])['kind_id'], 'kind-ticket')

    def test_seed_rejects_missing_work_kind_before_creating_nodes(self):
        self.responses['/api/kinds'] = {'items': [{'slug': 'project', 'id': 'kind-project'}]}
        with contextlib.redirect_stdout(io.StringIO()):
            with self.assertRaisesRegex(AssertionError, 'previous release has no supported work kind'):
                self.probe.seed()
        self.assertFalse(any(path == '/api/nodes' for path, _ in self.posts))

    def test_reads_existing_rows_after_healthy_startup(self):
        self.check()
        self.assertEqual(set(self.requests), set(self.responses))

    def test_read_failure_fails_even_with_healthy_ready_binary(self):
        self.responses['/api/nodes/ticket'] = (500, 'application/json', b'{"error":"read failed"}')
        with self.assertRaisesRegex(AssertionError, '/api/nodes/ticket: HTTP 500'):
            self.check()

    def test_successful_empty_response_cannot_hide_lost_project(self):
        self.responses['/api/projects?include_archived=true'] = {'items': []}
        with self.assertRaisesRegex(AssertionError, 'seeded project missing'):
            self.check()

    def test_wrong_binary_and_unready_process_fail(self):
        self.responses['/api/version'] = {'version': 'dev'}
        with self.assertRaisesRegex(AssertionError, 'does not match'):
            self.check()
        self.responses['/api/ready'] = (503, 'application/json', b'{"status":"unavailable"}')
        with self.assertRaisesRegex(AssertionError, '/api/ready: HTTP 503'):
            self.check()

    def pool_ready(self):
        # AEON-995: a ready process reports pool statistics beside status.
        return {
            'status': 'ready',
            'pool': {
                'max': 4, 'acquired': 1, 'idle': 3, 'waiting': 0,
                'background_limit': 2, 'background_acquired': 0,
                'acquire_duration_p95_ms': 0, 'acquire_samples': 1,
                'nested_acquires': 0,
            },
        }

    def test_pool_statistics_still_mean_the_previous_release_is_ready(self):
        self.responses['/api/ready'] = self.pool_ready()
        self.check()
        completed = subprocess.run(
            [sys.executable, str(Path(module.__file__)), 'wait-ready', '--base', self.probe.base],
            capture_output=True, text=True, timeout=15, check=False)
        self.assertEqual(completed.returncode, 0, completed.stderr)

    def test_a_failure_reason_is_not_readiness(self):
        self.responses['/api/ready'] = {'status': 'ready', 'reason': 'not_accepting'}
        with self.assertRaisesRegex(AssertionError, 'previous release is not ready'):
            self.check()

    def test_activated_refusal_rejects_success_and_executable_errors(self):
        path = '/api/models/resolve?role=build&harness=codex'
        self.responses[path] = {'command_template': 'unsafe launch'}
        with self.assertRaisesRegex(AssertionError, 'activated previous binary returned HTTP 200'):
            self.probe.refused(path)
        self.responses[path] = (500, 'application/json', b'{"command_template":"unsafe launch"}')
        with self.assertRaisesRegex(AssertionError, 'unsafe activated response'):
            self.probe.refused(path)

    def test_unrelated_refusal_cannot_pass_the_capability_gate(self):
        path = '/api/models/resolve?role=build&harness=codex'
        self.responses[path] = (403, 'application/json', b'{"error":"forbidden"}')
        boundary = module.AccountUseBoundary(self.probe, 'fixture-container')
        boundary.errors = Mock(side_effect=[(0, 0), (0, 0)])
        with self.assertRaisesRegex(AssertionError, 'lacks exact capability entry evidence'):
            boundary.request(self.probe, path)
        boundary.errors = Mock(side_effect=[(0, 0), (1, 1)])
        with contextlib.redirect_stdout(io.StringIO()):
            boundary.request(self.probe, path)

    def test_error_evidence_requires_exact_sqlstate_and_entry_function(self):
        boundary = module.AccountUseBoundary(self.probe, 'fixture-container')
        result = Mock(returncode=0, stdout='', stderr='ERROR: 42501: '+module.CAPABILITY_ERROR)
        with patch.object(module.subprocess, 'run', return_value=result):
            self.assertEqual(boundary.errors(), (0, 0))
            result.stderr = 'ERROR: 0A000: '+module.CAPABILITY_ERROR+'\nCONTEXT: aeon_enter_principal(uuid,uuid,boolean)'
            self.assertEqual(boundary.errors(), (1, 1))

    def capable_fixture(self):
        tenant, owner, agent, account, run, order, request = [str(uuid.UUID(int=i)) for i in range(1, 8)]
        self.responses['/api/me'] = {'principal': {'id': owner, 'tenant_id': tenant}}
        preview = {'preview': True, 'profile': {'id': 'catalog-only'}, 'ladder': []}
        resolutions = iter([preview, preview, {'profile': None, 'ladder': [{'skip_reasons': ['context']}]}])
        self.responses['/api/models/resolve?role=build&harness=codex'] = lambda: next(resolutions)
        capacity = iter([{'accounts': [], 'parallel_runs': 0, 'wait': {'code': reason}}
                         for reason in ('offline', 'context')])
        self.responses['/api/agent-accounts/capacity/next?harness=codex'] = lambda: next(capacity)
        uses = iter([(404, 'application/json', b'{"error":"account not found"}'),
                     (409, 'application/json', b'{"error":"account_not_allowed_for_context"}')])
        self.responses[f'/api/agent-accounts/use?harness=codex&account_id={account}'] = lambda: next(uses)
        self.responses['/api/agent-accounts/route'] = (403, 'application/json', b'{"error":"agent key required"}')
        self.responses[f'/api/agent-pairing/requests/{request}/approve'] = (404, 'application/json', b'{"error":"pairing not found"}')
        claims = iter([(409, 'application/json', json.dumps({'error': reason}).encode())
                       for reason in ('account reservation required', 'account daemon generation changed')])
        self.responses[f'/api/runs/{run}/claim'] = lambda: next(claims)
        boundary = module.AccountUseBoundary(self.probe, 'fixture-container')
        boundary.capable = True

        def sql(statement):
            if 'enforced_at IS NULL' in statement:
                return '0,1,true'
            if "key='agents.working'" in statement:
                return '0'
            if 'jsonb_build_object' in statement:
                return 'unchanged reservations, runs and claims'
            return ''

        boundary.sql = Mock(side_effect=sql)
        boundary.errors = Mock(return_value=(0, 0))
        ids = [uuid.UUID(value) for value in (agent, account, run, order, request)]
        ids += [uuid.UUID(int=i) for i in (8, 9)]
        return boundary, ids, {'ticket': str(uuid.UUID(int=10))}

    # Risk: the latest published binary already supports account_use_v1. Its
    # safe HTTP 200 preview must pass while runnable material still fails.
    def test_supported_previous_release_checks_both_activated_pools(self):
        boundary, ids, state = self.capable_fixture()
        with patch.object(module.uuid, 'uuid4', side_effect=ids), patch.object(module.time, 'sleep'), contextlib.redirect_stdout(io.StringIO()):
            boundary.check(state)
        boundary.errors.assert_not_called()
        self.assertEqual(sum('jsonb_build_object' in c.args[0] for c in boundary.sql.call_args_list), 4)
        self.assertTrue(any('DELETE FROM account_use_cells' in c.args[0] for c in boundary.sql.call_args_list))
        self.assertEqual(sum(path.startswith('/api/runs/') for path, _ in self.posts), 2)

    def test_supported_previous_release_rejects_executable_preview(self):
        boundary, ids, state = self.capable_fixture()
        self.responses['/api/models/resolve?role=build&harness=codex'] = {
            'preview': True, 'profile': {'id': 'unsafe'}, 'command_template': 'unsafe launch'}
        with patch.object(module.uuid, 'uuid4', side_effect=ids), contextlib.redirect_stdout(io.StringIO()):
            with self.assertRaisesRegex(AssertionError, 'activated pool supplied executable material'):
                boundary.check(state)

    def test_supported_previous_release_requires_context_and_no_capacity(self):
        boundary = module.AccountUseBoundary(self.probe, 'fixture-container')
        path = '/api/models/resolve?role=build&harness=codex'
        self.responses[path] = {'profile': None, 'ladder': [{'skip_reasons': ['offline']}]}
        with contextlib.redirect_stdout(io.StringIO()):
            with self.assertRaisesRegex(AssertionError, 'denied pool lacks its context refusal'):
                boundary.selection(True, 'account')
            self.responses[path] = {'profile': None, 'ladder': [{'skip_reasons': ['context']}]}
            self.responses['/api/agent-accounts/capacity/next?harness=codex'] = {
                'accounts': [{'account_id': 'denied'}], 'parallel_runs': 1, 'wait': {'code': 'context'}}
            with self.assertRaisesRegex(AssertionError, 'pool supplied capacity or lost its refusal'):
                boundary.selection(True, 'account')

    def test_pinned_release_floor_selects_checks_without_http_fallback(self):
        with patch.object(module.subprocess, 'run') as run:
            run.return_value = Mock(returncode=0, stdout='internal/db/migrations/1310_account_use_entry_guard.sql\n')
            self.assertTrue(module.previous_account_use_capable('pinned-release'))
            self.assertEqual(run.call_args.args[0][3], 'pinned-release')
            run.return_value = Mock(returncode=0, stdout='')
            self.assertFalse(module.previous_account_use_capable('legacy-release'))
            run.return_value = Mock(returncode=128, stdout='')
            with self.assertRaisesRegex(AssertionError, 'cannot determine the pinned release'):
                module.previous_account_use_capable('missing-release')

    def test_supported_refusal_rejects_wrong_status_or_reason(self):
        path = '/api/agent-accounts/use'
        self.responses[path] = (500, 'application/json', b'{"error":"unrelated"}')
        with self.assertRaisesRegex(AssertionError, 'expected HTTP 409, got 500'):
            self.probe.refused(path, status=409, message='account_not_allowed_for_context')
        self.responses[path] = (409, 'application/json', b'{"error":"unrelated"}')
        with self.assertRaisesRegex(AssertionError, 'wrong policy refusal'):
            self.probe.refused(path, status=409, message='account_not_allowed_for_context')


class MigrationRuntimeTest(unittest.TestCase):
    def test_latest_compatibility_and_legacy_refusal_use_their_own_images(self):
        # Risk: the latest release's policy checks and the below-floor binary's
        # strict refusals must both run against their own inactive fixture.
        floor_digest = 'sha256:d916ebb57249fda5f192e74b37ebd770c0eb67c26aafeb1c0045a635e8aa940c'
        floor_version = '261009095632.0.0'
        latest_digest = 'sha256:' + 'a' * 64
        latest_version = '261010123154.0.0'
        for latest_fails, legacy_fails in ((False, False), (False, True), (True, False)):
            with self.subTest(latest_fails=latest_fails, legacy_fails=legacy_fails), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                scripts, commands = root / 'scripts', root / 'commands'
                scripts.mkdir()
                commands.mkdir()
                script = scripts / 'migration-compat.sh'
                script.write_bytes(Path(module.__file__).with_name('migration-compat.sh').read_bytes())
                driver = '#!' + sys.executable + '\n' + '''
import json, os, sys
from pathlib import Path
root = Path(os.environ['AEON_COMPAT_TEST_ROOT'])
tool, args = Path(sys.argv[0]).name, sys.argv[1:]
floor = 'sha256:d916ebb57249fda5f192e74b37ebd770c0eb67c26aafeb1c0045a635e8aa940c'
latest_id, floor_id = 'sha256:' + 'b' * 64, 'sha256:' + 'c' * 64
def record(event):
    with (root / 'events.jsonl').open('a') as out:
        out.write(json.dumps(event) + '\\n')
if tool == 'docker':
    if args[0] == 'pull':
        record(['pull', args[-1]])
    elif args[:2] == ['image', 'ls']:
        print(floor_id if args[-1].endswith(floor) else latest_id)
    elif args[0] == 'run' and args[-1] in (latest_id, floor_id):
        (root / 'image').write_text(args[-1])
        (root / 'running').write_text('true')
        record(['boot', args[-1]])
    elif args[0] == 'stop':
        (root / 'running').write_text('false')
    elif args[0] == 'port':
        print('127.0.0.1:18080')
    elif args[0] == 'exec' and '-i' in args:
        statement = sys.stdin.read()
        if 'CREATE DATABASE aeon_compat_floor' in statement:
            if (root / 'running').read_text() != 'false' or (root / 'fixture').read_text() != 'inactive':
                sys.exit('snapshot requires an inactive fixture with no server connected')
            (root / 'floor-fixture').write_text((root / 'fixture').read_text())
            record(['snapshot'])
        elif 'DROP DATABASE aeon;' in statement:
            if (root / 'running').read_text() != 'false' or 'ALTER DATABASE aeon_compat_floor RENAME TO aeon;' not in statement:
                sys.exit('restore requires no server connected and the saved database')
            (root / 'fixture').write_text((root / 'floor-fixture').read_text())
            record(['restore'])
elif tool == 'go':
    (root / 'fixture').write_text('inactive')
    record(['migrate'])
elif tool == 'python3':
    mode = args[1]
    current = (root / 'image').read_text()
    version = args[args.index('--version') + 1] if '--version' in args else None
    record(['probe', mode, current, version])
    if mode in ('seed', 'check'):
        expected = '261009095632.0.0' if current == floor_id else '261010123154.0.0'
        if version != expected:
            sys.exit('wrong binary version')
    if mode == 'account-use':
        expected = '261009095632.0.0' if current == floor_id else '261010123154.0.0'
        if version != expected:
            sys.exit('wrong account-use capability release')
        if current == latest_id and args[args.index('--release-tag') + 1] != 'v' + expected:
            sys.exit('latest capability must use its pinned release')
        if current == floor_id and '--release-tag' in args:
            sys.exit('legacy refusal must remain unconditional')
        if (root / 'fixture').read_text() != 'inactive':
            sys.exit('account-use probes require an empty inactive fixture')
        (root / 'fixture').write_text('activated with populated pool')
        if current == latest_id and os.environ['AEON_COMPAT_TEST_LATEST_FAILS'] == 'true':
            sys.exit('latest policy gate failed')
        if current == floor_id and os.environ['AEON_COMPAT_TEST_LEGACY_FAILS'] == 'true':
            sys.exit('legacy refusal gate failed')
'''
                for name in ('docker', 'go', 'python3', 'trash'):
                    executable = commands / name
                    executable.write_text(driver)
                    executable.chmod(0o700)
                result = subprocess.run(['bash', str(script), 'v' + latest_version, latest_digest],
                    cwd=root, env={**os.environ, 'PATH': str(commands) + os.pathsep + os.environ['PATH'],
                        'GITHUB_STEP_SUMMARY': str(root / 'summary'),
                        'AEON_COMPAT_TEST_ROOT': str(root),
                        'AEON_COMPAT_TEST_LATEST_FAILS': str(latest_fails).lower(),
                        'AEON_COMPAT_TEST_LEGACY_FAILS': str(legacy_fails).lower()},
                    capture_output=True, text=True, timeout=30, check=False)
                events = [json.loads(line) for line in (root / 'events.jsonl').read_text().splitlines()]
                latest_id, floor_id = 'sha256:' + 'b' * 64, 'sha256:' + 'c' * 64
                checks = [event for event in events if event[:2] == ['probe', 'check']]
                if latest_fails:
                    self.assertEqual(checks, [['probe', 'check', latest_id, latest_version]])
                    self.assertEqual([event for event in events if event[:2] == ['probe', 'account-use']],
                        [['probe', 'account-use', latest_id, latest_version]])
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn('latest policy gate failed', result.stderr)
                    self.assertNotIn('Migration compatibility passed:', result.stdout)
                    self.assertNotIn(['restore'], events)
                    continue
                self.assertEqual(checks, [['probe', 'check', latest_id, latest_version],
                    ['probe', 'check', floor_id, floor_version]])
                self.assertEqual([event for event in events if event[:2] == ['probe', 'account-use']],
                    [['probe', 'account-use', latest_id, latest_version],
                     ['probe', 'account-use', floor_id, floor_version]])
                self.assertEqual([event for event in events if event[0] == 'pull'],
                    [['pull', 'ghcr.io/inspr-at/aeon@' + latest_digest],
                     ['pull', 'ghcr.io/inspr-at/aeon@' + floor_digest]])
                self.assertLess(events.index(['migrate']), events.index(['snapshot']))
                self.assertLess(events.index(['snapshot']), events.index(checks[0]))
                self.assertLess(events.index(['probe', 'account-use', latest_id, latest_version]), events.index(['restore']))
                self.assertLess(events.index(['restore']), events.index(checks[1]))
                if legacy_fails:
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn('legacy refusal gate failed', result.stderr)
                    self.assertNotIn('Migration compatibility passed:', result.stdout)
                else:
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertIn('Migration compatibility passed:', result.stdout)
                    summary = (root / 'summary').read_text()
                    self.assertIn(latest_version, summary)
                    self.assertIn(latest_id, summary)
                    self.assertIn(floor_version, summary)
                    self.assertIn(floor_id, summary)


if __name__ == '__main__':
    unittest.main()
