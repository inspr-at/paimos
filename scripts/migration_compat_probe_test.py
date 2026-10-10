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
import tempfile
import threading
import unittest
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location('migration_compat_probe', Path(__file__).with_name('migration-compat-probe.py'))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class MigrationCompatImagesTest(unittest.TestCase):
    # Risk: once the latest stable binary supports account-use, an unconditional
    # rollback-floor probe against that binary fails despite a sound DB fence.
    # Mock only process boundaries; execute the real shell orchestration and
    # retain strict refusals against an independently pinned incapable binary.
    def test_capable_previous_release_keeps_the_incapable_rollback_probe(self):
        latest_tag = 'v261010123154.0.0'
        latest_digest = 'sha256:' + 'a' * 64
        floor_digest = 'sha256:d916ebb57249fda5f192e74b37ebd770c0eb67c26aafeb1c0045a635e8aa940c'
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            scripts = root / 'scripts'
            scripts.mkdir()
            script = scripts / 'migration-compat.sh'
            script.write_text(Path(__file__).with_name(script.name).read_text())
            tools = root / 'tools'
            tools.mkdir()
            fake = '''import json, os, sys
from pathlib import Path
tool, args = Path(sys.argv[0]).name, sys.argv[1:]
root = Path(os.environ['COMPAT_FIXTURE_ROOT'])
with (root / 'calls.jsonl').open('a') as log:
    log.write(json.dumps([tool, args]) + '\\n')
latest_id, floor_id = 'sha256:' + '1' * 64, 'sha256:' + '2' * 64
active = root / 'active-image'
active_database = root / 'active-database'
if tool == 'docker':
    if args[:2] == ['image', 'ls']:
        print(floor_id if args[-1].endswith(os.environ['COMPAT_FLOOR_DIGEST']) else latest_id)
    elif args[0] == 'port':
        print('127.0.0.1:5432' if args[-1] == '5432/tcp' else '127.0.0.1:8080')
    elif args[0] == 'run' and args[-1] in (latest_id, floor_id):
        active.write_text(args[-1])
        database = next(value for value in args if value.startswith('AEON_DATABASE_URL=')).split('/')[-1].split('?')[0]
        active_database.write_text(database)
elif tool == 'python3':
    mode = args[1]
    if mode == 'seed':
        Path(args[args.index('--state') + 1]).write_text('{}')
    if mode in ('seed', 'check', 'account-use'):
        version = args[args.index('--version') + 1]
        expected = '261009095632.0.0' if active.read_text() == floor_id else '261010123154.0.0'
        if version != expected:
            sys.exit('tested binary does not match the previous release')
    if mode == 'account-use':
        database = args[args.index('--database') + 1] if '--database' in args else 'aeon'
        if database != active_database.read_text():
            sys.exit('probe and binary used different fixtures')
        if '--require-refusal' in args:
            if active.read_text() != floor_id or database != 'aeon_legacy':
                sys.exit('capability refusal must exercise the independent legacy fixture')
        elif active.read_text() != latest_id or database != 'aeon':
            sys.exit('capability-aware activation must exercise the latest binary')
elif tool not in ('go', 'trash'):
    sys.exit('unexpected tool')
'''
            for name in ('docker', 'python3', 'go', 'trash'):
                executable = tools / name
                executable.write_text('#!' + sys.executable + '\n' + fake)
                executable.chmod(0o755)
            summary = root / 'summary'
            environment = os.environ.copy()
            environment.update({
                'PATH': str(tools) + os.pathsep + environment['PATH'],
                'COMPAT_FIXTURE_ROOT': str(root),
                'COMPAT_FLOOR_DIGEST': floor_digest,
                'GITHUB_STEP_SUMMARY': str(summary),
            })
            completed = subprocess.run(
                ['bash', str(script), latest_tag, latest_digest], env=environment,
                capture_output=True, text=True, timeout=15, check=False)
            self.assertEqual(completed.returncode, 0, completed.stderr)
            calls = [json.loads(line) for line in (root / 'calls.jsonl').read_text().splitlines()]
            boots = [args[-1] for tool, args in calls
                     if tool == 'docker' and args[0] == 'run' and args[-1].startswith('sha256:')]
            self.assertEqual(boots, ['sha256:' + '1' * 64] * 3 + ['sha256:' + '2' * 64])
            probes = [(args[1], args[args.index('--version') + 1]) for tool, args in calls
                      if tool == 'python3' and '--version' in args]
            self.assertEqual(probes, [
                ('seed', latest_tag[1:]), ('check', latest_tag[1:]),
                ('account-use', latest_tag[1:]),
                ('check', '261009095632.0.0'), ('account-use', '261009095632.0.0')])
            activations = [args for tool, args in calls if tool == 'python3' and args[1] == 'account-use']
            self.assertEqual(['--require-refusal' in args for args in activations], [False, True])
            self.assertNotIn('--database', activations[0])
            self.assertEqual(activations[1][activations[1].index('--database') + 1], 'aeon_legacy')
            copies = [i for i, (_tool, args) in enumerate(calls)
                      if 'CREATE DATABASE aeon_legacy WITH TEMPLATE aeon OWNER aeon' in args]
            self.assertEqual(len(copies), 1)
            self.assertLess(copies[0], calls.index(['python3', activations[0]]))
            pulls = [args[-1] for tool, args in calls if tool == 'docker' and args[0] == 'pull']
            self.assertEqual(pulls, ['ghcr.io/inspr-at/aeon@' + latest_digest,
                                     'ghcr.io/inspr-at/aeon@' + floor_digest])
            self.assertIn('registry image ghcr.io/inspr-at/aeon@' + latest_digest +
                          '; loaded image sha256:' + '1' * 64, summary.read_text())
            self.assertIn('registry image ghcr.io/inspr-at/aeon@' + floor_digest +
                          '; loaded image sha256:' + '2' * 64, summary.read_text())


class ProbeTest(unittest.TestCase):
    def setUp(self):
        self.requests = []
        self.posts = []
        self.api_fallback = (403, 'application/json',
                             b'{"error":"permission denied","code":"forbidden","route":"/api/"}')
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
                fallback = (owner.api_fallback if self.path.startswith('/api/aeon-compat-absent-')
                            else (404, 'application/json', b'{}'))
                value = owner.responses.get(self.path, fallback)
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

    def test_capability_detection_uses_the_binary_api_and_rejects_ambiguous_responses(self):
        # Risk: the authenticated floor returns 403 for missing API routes;
        # only its exact fallback can prove absence, never a distinct failure.
        path = '/api/account-use?limit=1'
        matrix = {'rules': {'revision': 1, 'enforced_at': None},
                  'accounts': [], 'contexts': [], 'cells': []}
        for version in ('260930115354.0.0', '261010123154.0.0', 'dev'):
            with self.subTest(version=version), contextlib.redirect_stdout(io.StringIO()):
                self.responses['/api/version'] = {'version': version}
                self.responses[path] = matrix
                self.assertEqual(self.probe.account_use(), matrix)
                self.responses[path] = self.api_fallback
                self.assertIsNone(self.probe.account_use())
        self.api_fallback = (404, 'application/json', b'{"error":"not found"}')
        with contextlib.redirect_stdout(io.StringIO()):
            self.responses[path] = self.api_fallback
            self.assertIsNone(self.probe.account_use())
            # Compare parsed JSON, not serialization whitespace or key order.
            self.api_fallback = (403, 'application/json', b'{"error":"permission denied","code":"forbidden"}')
            self.responses[path] = (403, 'application/json', b'{ "code": "forbidden", "error": "permission denied" }')
            self.assertIsNone(self.probe.account_use())
        self.api_fallback = (404, 'application/json', b'{"error":"not found"}')
        for response, reason in (({}, 'invalid account-use capability response'),
                         ((200, 'application/json', b'null'), 'invalid account-use capability response'),
                         ((200, 'text/html', b'<html>SPA</html>'), 'expected JSON'),
                         ((200, 'application/json', b'x' * ((1 << 20) + 1)), 'response exceeds bound'),
                         ((403, 'application/json', b'{"error":"forbidden"}'), 'HTTP 403'),
                         ((500, 'application/json', b'{"error":"failed"}'), 'HTTP 500'),
                         ((404, 'application/json', b'{"error":"account-use resource not found"}'), 'HTTP 404'),
                         ((404, 'application/json; charset=utf-8', b'{"error":"not found"}'), 'HTTP 404'),
                         ((403, 'text/html', b'<html>denied</html>'), 'expected JSON'),
                         ((403, 'application/json', b'not JSON'), 'expected JSON'),
                         ((403, 'application/json', b'x' * ((1 << 20) + 1)), 'response exceeds bound')):
            with self.subTest(response=response[:2] if isinstance(response, tuple) else response):
                self.responses[path] = response
                with self.assertRaisesRegex(AssertionError, reason):
                    self.probe.account_use()
        self.responses[path] = (403, 'application/json', b'{"error":"permission denied","code":"forbidden"}')
        for fallback, reason in (((500, 'application/json', b'{"error":"permission denied","code":"forbidden"}'), 'HTTP 403'),
                                ((403, 'application/json', b'{"error":"different refusal","code":"forbidden"}'), 'HTTP 403'),
                                ((403, 'application/json; charset=utf-8', b'{"error":"permission denied","code":"forbidden"}'), 'HTTP 403'),
                                ((403, 'text/html', b'<html>denied</html>'), 'expected JSON'),
                                ((403, 'application/json', b'not JSON'), 'expected JSON'),
                                ((403, 'application/json', b'x' * ((1 << 20) + 1)), 'response exceeds bound')):
            with self.subTest(fallback=fallback[:2]):
                self.api_fallback = fallback
                with self.assertRaisesRegex(AssertionError, reason):
                    self.probe.account_use()
        # A matching successful response still needs a valid capability matrix.
        self.api_fallback = (200, 'application/json', b'{}')
        self.responses[path] = {}
        with self.assertRaisesRegex(AssertionError, 'invalid account-use capability response'):
            self.probe.account_use()
        sentinels = [request for request in self.requests if request != path]
        self.assertEqual(len(sentinels), len(set(sentinels)))
        for sentinel in sentinels:
            self.assertRegex(sentinel, r'^/api/aeon-compat-absent-[0-9a-f-]{36}\?limit=1$')
            module.uuid.UUID(sentinel.removeprefix('/api/aeon-compat-absent-').removesuffix('?limit=1'))
        self.assertNotIn('/api/version', self.requests)

    def test_capable_binary_serves_strict_reads_after_activation(self):
        # Risk: capability support can no longer make an activated HTTP 200
        # fail the gate, but lost rows or a broken resolver must still fail it.
        ticket = '11111111-1111-4111-8111-111111111111'
        tenant = '22222222-2222-4222-8222-222222222222'
        owner = '33333333-3333-4333-8333-333333333333'
        state = {'project': 'project', 'ticket': ticket}
        self.responses['/api/me'] = {'principal': {'tenant_id': tenant, 'id': owner}}
        self.responses['/api/nodes?parent_id=project&limit=100'] = {'items': [{'id': ticket}]}
        self.responses['/api/nodes/' + ticket] = {'id': ticket}
        path = '/api/account-use?limit=1'
        resolve = '/api/models/resolve?role=build&harness=codex'
        matrix = {'rules': {'revision': 1, 'enforced_at': None},
                  'accounts': [], 'contexts': [], 'cells': []}
        self.responses[path] = matrix
        self.responses[resolve] = {'command_template': 'fixture launch'}
        boundary = module.AccountUseBoundary(self.probe, 'fixture-container')
        def sql(statement):
            if statement.startswith('UPDATE account_use_rules'):
                matrix['rules']['enforced_at'] = '2026-10-10T00:00:00Z'
            return ''
        boundary.sql = Mock(side_effect=sql)
        boundary.errors = Mock(return_value=(0, 0))
        with contextlib.redirect_stdout(io.StringIO()):
            boundary.check(state, '260930115354.0.0')
        self.assertIsNotNone(matrix['rules']['enforced_at'])
        self.assertIn('/api/nodes/' + ticket, self.requests)
        self.assertIn(resolve, self.requests)
        self.assertEqual(boundary.errors.call_count, 2)
        with self.assertRaisesRegex(AssertionError, 'unexpectedly supports account_use_v1'):
            boundary.check(state, '260930115354.0.0', require_refusal=True)
        self.responses['/api/nodes/' + ticket] = (500, 'application/json', b'{"error":"read failed"}')
        with self.assertRaisesRegex(AssertionError, '/api/nodes/' + ticket + ': HTTP 500'):
            boundary.check(state, '260930115354.0.0')
        self.responses['/api/nodes/' + ticket] = {'id': ticket}
        self.responses[resolve] = (500, 'application/json', b'{"error":"resolve failed"}')
        with self.assertRaisesRegex(AssertionError, 'models/resolve.*HTTP 500'):
            boundary.check(state, '260930115354.0.0')
        self.responses[resolve] = {'command_template': 'fixture launch'}
        boundary.errors = Mock(side_effect=[(0, 0), (1, 1)])
        with self.assertRaisesRegex(AssertionError, 'capable binary hit the capability refusal gate'):
            boundary.request(self.probe, resolve, capable=True)

    def test_unsupported_binary_retains_both_strict_activated_pool_probes(self):
        tenant = '22222222-2222-4222-8222-222222222222'
        owner = '33333333-3333-4333-8333-333333333333'
        self.responses['/api/me'] = {'principal': {'tenant_id': tenant, 'id': owner}}
        self.responses['/api/account-use?limit=1'] = self.api_fallback
        resolve = '/api/models/resolve?role=build&harness=codex'
        self.responses[resolve] = {}
        boundary = module.AccountUseBoundary(self.probe, 'fixture-container', 'aeon_legacy')
        def sql(statement):
            if statement.startswith('SELECT (SELECT count(*) FROM agent_accounts'):
                return '0,1,true'
            if statement.startswith('SELECT count(*) FROM user_preferences'):
                return '0'
            return ''
        boundary.sql = Mock(side_effect=sql)
        boundary.snapshot = Mock(return_value='unchanged')
        with patch.object(boundary, 'request') as request, patch.object(module.time, 'sleep'), contextlib.redirect_stdout(io.StringIO()):
            boundary.check({'ticket': '11111111-1111-4111-8111-111111111111'}, '260930115354.0.0')
        # Every original endpoint and the real runtime claim path remain in
        # both empty and populated probes, with the strict default expectation.
        self.assertEqual(request.call_count, 12)
        for offset in (0, 6):
            self.assertEqual(request.call_args_list[offset].args[1], resolve)
            self.assertRegex(request.call_args_list[offset + 5].args[1], r'^/api/runs/.+/claim$')
        self.assertTrue(all(not call.kwargs for call in request.call_args_list))
        self.assertEqual(boundary.snapshot.call_count, 4)


class MigrationHarnessTest(unittest.TestCase):
    def test_latest_compatibility_and_legacy_capability_use_distinct_images(self):
        # Risk: once the latest release understands account use, expecting it
        # to fail the legacy capability guard rejects every subsequent PR.
        # Execute the harness with disposable command doubles: keep latest
        # read compatibility and exact legacy refusal as independent gates.
        latest_tag = 'v261010123154.0.0'
        latest_digest = 'sha256:c9952c1561c8d32c4a3efb17ed95934f81efd6bc2546de41a3b56bd138304deb'
        legacy_tag = 'v261009095632.0.0'
        legacy_digest = 'sha256:d916ebb57249fda5f192e74b37ebd770c0eb67c26aafeb1c0045a635e8aa940c'
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            scripts = root / 'scripts'
            scripts.mkdir()
            (scripts / 'migration-compat.sh').write_text(
                Path(__file__).with_name('migration-compat.sh').read_text())
            bin_dir = root / 'bin'
            bin_dir.mkdir()
            command = '''import json, os, sys
from pathlib import Path
name, args = Path(sys.argv[0]).name, sys.argv[1:]
state_path = Path(os.environ['HARNESS_STATE'])
state = json.loads(state_path.read_text()) if state_path.exists() else {}
with open(os.environ['HARNESS_LOG'], 'a') as log:
    log.write(json.dumps({'command': name, 'args': args, 'image': state.get('image'), 'database': state.get('database'), 'migrated': state.get('migrated', False)}) + '\\n')
if name == 'docker':
    if args[:2] == ['image', 'ls']:
        print(args[-1].split('@')[1])
    elif args[0] == 'run' and '--name' in args and args[args.index('--name') + 1].startswith('aeon-compat-app-'):
        state['image'] = args[-1]
        state['database'] = next(value for value in args if value.startswith('AEON_DATABASE_URL=')).split('/')[-1].split('?')[0]
    elif args[0] == 'port':
        print('127.0.0.1:8080' if args[-1] == '8080/tcp' else '127.0.0.1:5432')
    elif args[0] == 'exec' and '-i' in args:
        sys.stdin.read()
elif name == 'go':
    state['migrated'] = True
elif name == 'python3':
    mode = args[1]
    if mode == 'seed':
        Path(args[args.index('--state') + 1]).write_text('{}')
    elif mode == 'account-use':
        database = args[args.index('--database') + 1] if '--database' in args else 'aeon'
        if database != state.get('database'):
            sys.exit('probe and binary used different fixtures')
        boundary = 'legacy' if '--require-refusal' in args else 'latest'
        if boundary == 'legacy':
            if state.get('image') != os.environ['HARNESS_LEGACY_DIGEST'] or database != 'aeon_legacy':
                sys.exit('capability refusal must exercise the independent immutable legacy image')
        elif state.get('image') != os.environ['HARNESS_LATEST_DIGEST'] or database != 'aeon':
            sys.exit('capability-aware activation must exercise the latest binary')
        if os.environ['HARNESS_FAIL_BOUNDARY'] == boundary:
            sys.exit(23)
state_path.write_text(json.dumps(state))
'''
            for name in ('docker', 'python3', 'go', 'trash'):
                executable = bin_dir / name
                executable.write_text('#!' + sys.executable + '\n' + command)
                executable.chmod(0o755)
            for fail_boundary in ('none', 'latest', 'legacy'):
                with self.subTest(fail_boundary=fail_boundary):
                    log = root / f'commands-{fail_boundary}.jsonl'
                    harness_env = dict(os.environ, PATH=str(bin_dir) + os.pathsep + os.environ['PATH'],
                        HARNESS_LOG=str(log), HARNESS_STATE=str(root / f'state-{fail_boundary}.json'),
                        HARNESS_LATEST_DIGEST=latest_digest, HARNESS_LEGACY_DIGEST=legacy_digest,
                        HARNESS_FAIL_BOUNDARY=fail_boundary,
                        GITHUB_STEP_SUMMARY=str(root / f'summary-{fail_boundary}.txt'))
                    result = subprocess.run(['bash', str(scripts / 'migration-compat.sh'), latest_tag, latest_digest],
                        env=harness_env, capture_output=True, text=True, timeout=15, check=False)
                    self.assertEqual(result.returncode, 0 if fail_boundary == 'none' else 23, result.stdout + result.stderr)
                    events = [json.loads(line) for line in log.read_text().splitlines()]
                    migrations = [event for event in events if event['command'] == 'go']
                    self.assertEqual(len(migrations), 1)
                    probes = [event for event in events if event['command'] == 'python3']
                    reads = [event for event in probes if event['args'][1] == 'check']
                    self.assertEqual([(event['image'], event['migrated'], event['args'][-1]) for event in reads],
                        [(latest_digest, True, latest_tag[1:])] +
                        ([] if fail_boundary == 'latest' else [(legacy_digest, True, legacy_tag[1:])]))
                    boundary = [event for event in probes if event['args'][1] == 'account-use']
                    self.assertEqual([(event['image'], event['database'], event['migrated'],
                                       event['args'][event['args'].index('--version') + 1],
                                       '--require-refusal' in event['args']) for event in boundary],
                        [(latest_digest, 'aeon', True, latest_tag[1:], False)] +
                        ([] if fail_boundary == 'latest' else [(legacy_digest, 'aeon_legacy', True, legacy_tag[1:], True)]))
                    if fail_boundary != 'none':
                        self.assertNotIn('Migration compatibility passed:', result.stdout)
                    else:
                        self.assertIn('Migration compatibility passed:', result.stdout)


class MigrationRuntimeTest(unittest.TestCase):
    def test_latest_compatibility_and_legacy_refusal_use_their_own_images(self):
        # Risk: after publishing a capable release, an activated 200 from that
        # release must not replace the below-floor binary's strict refusal gate.
        floor_digest = 'sha256:d916ebb57249fda5f192e74b37ebd770c0eb67c26aafeb1c0045a635e8aa940c'
        floor_version = '261009095632.0.0'
        latest_digest = 'sha256:' + 'a' * 64
        latest_version = '261010123154.0.0'
        for latest_capable, latest_fails, legacy_fails in ((True, False, False), (False, False, False),
                                                        (True, True, False), (True, False, True)):
            with self.subTest(latest_capable=latest_capable, latest_fails=latest_fails, legacy_fails=legacy_fails), tempfile.TemporaryDirectory() as directory:
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
        database = next(value for value in args if value.startswith('AEON_DATABASE_URL=')).split('/')[-1].split('?')[0]
        (root / 'database').write_text(database)
        record(['boot', args[-1], database])
    elif args[0] == 'port':
        print('127.0.0.1:18080')
    elif args[0] == 'exec' and '-i' in args:
        sys.stdin.read()
    elif args[0] == 'exec' and 'CREATE DATABASE aeon_legacy WITH TEMPLATE aeon OWNER aeon' in args:
        record(['copy-fixture'])
elif tool == 'go':
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
        database = args[args.index('--database') + 1] if '--database' in args else 'aeon'
        if database != (root / 'database').read_text():
            sys.exit('probe and binary used different fixtures')
        if current == latest_id:
            if '--require-refusal' in args:
                sys.exit('latest binary forced to refuse regardless of capability')
            record(['activated-latest', os.environ['AEON_COMPAT_TEST_LATEST_CAPABLE']])
            if os.environ['AEON_COMPAT_TEST_LATEST_FAILS'] == 'true':
                sys.exit('capable normal path failed')
        else:
            if database != 'aeon_legacy' or '--require-refusal' not in args:
                sys.exit('legacy refusal gate lost its independent strict fixture')
            if os.environ['AEON_COMPAT_TEST_LEGACY_FAILS'] == 'true':
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
                        'AEON_COMPAT_TEST_LATEST_CAPABLE': str(latest_capable).lower(),
                        'AEON_COMPAT_TEST_LATEST_FAILS': str(latest_fails).lower(),
                        'AEON_COMPAT_TEST_LEGACY_FAILS': str(legacy_fails).lower()},
                    capture_output=True, text=True, timeout=30, check=False)
                events = [json.loads(line) for line in (root / 'events.jsonl').read_text().splitlines()]
                latest_id, floor_id = 'sha256:' + 'b' * 64, 'sha256:' + 'c' * 64
                checks = [event for event in events if event[:2] == ['probe', 'check']]
                self.assertEqual(checks, [['probe', 'check', latest_id, latest_version]] +
                    ([] if latest_fails else [['probe', 'check', floor_id, floor_version]]))
                self.assertEqual([event for event in events if event[:2] == ['probe', 'account-use']],
                    [['probe', 'account-use', latest_id, latest_version]] +
                    ([] if latest_fails else [['probe', 'account-use', floor_id, floor_version]]))
                self.assertEqual([event for event in events if event[0] == 'pull'],
                    [['pull', 'ghcr.io/inspr-at/aeon@' + latest_digest]] +
                    ([] if latest_fails else [['pull', 'ghcr.io/inspr-at/aeon@' + floor_digest]]))
                self.assertLess(events.index(['migrate']), events.index(checks[0]))
                self.assertLess(events.index(checks[0]), events.index(['copy-fixture']))
                self.assertLess(events.index(['copy-fixture']), events.index(['activated-latest', str(latest_capable).lower()]))
                if not latest_fails:
                    self.assertLess(events.index(checks[0]), events.index(checks[1]))
                    self.assertIn(['boot', floor_id, 'aeon_legacy'], events)
                if latest_fails or legacy_fails:
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn('capable normal path failed' if latest_fails else 'legacy refusal gate failed', result.stderr)
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
