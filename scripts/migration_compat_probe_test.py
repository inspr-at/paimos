# SPDX-License-Identifier: AGPL-3.0-only
import contextlib
import importlib.util
import io
import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import threading
import unittest
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


class MigrationCompatImageTest(unittest.TestCase):
    def run_gate(self, refuse_floor=True):
        # Risk: a moving latest release acquires the capability, so treating
        # that binary as below-floor makes every unrelated PR fail CI.
        # Execute the real shell orchestration with disposable command doubles;
        # no images, containers, credentials or network calls are involved.
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'scripts').mkdir()
            script = root / 'scripts' / 'migration-compat.sh'
            script.write_text(Path(module.__file__).with_name('migration-compat.sh').read_text())
            tools = root / 'tools'
            tools.mkdir()
            log = root / 'commands.jsonl'
            double = '''import json
import os
from pathlib import Path
import shutil
import sys
tool = Path(sys.argv[0]).name
args = sys.argv[1:]
with open(os.environ['COMPAT_FIXTURE_LOG'], 'a') as log:
    log.write(json.dumps([tool, *args]) + '\\n')
if tool == 'docker':
    if args[:2] == ['image', 'ls']:
        print(args[-1].split('@')[1])
    elif args[:1] == ['port']:
        print('127.0.0.1:' + ('8080' if args[-1] == '8080/tcp' else '5432'))
elif tool == 'trash':
    shutil.rmtree(args[0])
elif tool == 'python3' and args[1] == 'account-use':
    version = args[args.index('--version') + 1]
    # A capable latest binary accepts entry, so the unchanged refusal probe
    # fails. Only the archived incapable fixture can provide that evidence.
    if version != '261009095632.0.0' or os.environ['COMPAT_FIXTURE_REFUSES'] != 'yes':
        sys.exit(23)
'''
            for name in ('docker', 'go', 'python3', 'trash'):
                executable = tools / name
                executable.write_text(f'#!{sys.executable}\n' + double)
                executable.chmod(0o755)
            result = subprocess.run(
                [shutil.which('bash'), str(script), 'v261010123154.0.0', 'sha256:' + 'a' * 64],
                env={'PATH': str(tools) + os.pathsep + os.defpath,
                     'COMPAT_FIXTURE_LOG': str(log),
                     'COMPAT_FIXTURE_REFUSES': 'yes' if refuse_floor else 'no'},
                capture_output=True, text=True, timeout=15, check=False)
            commands = [json.loads(line) for line in log.read_text().splitlines()]
            return result, commands

    def test_latest_compatibility_and_below_floor_refusal_use_separate_images(self):
        result, commands = self.run_gate()
        self.assertEqual(result.returncode, 0, result.stderr)
        images = [args[-1] for tool, *args in commands if tool == 'docker' and args[0] == 'pull']
        self.assertEqual(images, [
            'ghcr.io/inspr-at/aeon@sha256:' + 'a' * 64,
            'ghcr.io/inspr-at/aeon@sha256:d916ebb57249fda5f192e74b37ebd770c0eb67c26aafeb1c0045a635e8aa940c',
        ])
        phases = [(args[1], args[args.index('--version') + 1])
                  for tool, *args in commands
                  if tool == 'python3' and args[1] != 'wait-ready']
        self.assertEqual(phases, [
            ('seed', '261010123154.0.0'), ('check', '261010123154.0.0'),
            ('seed', '261009095632.0.0'), ('check', '261009095632.0.0'),
            ('account-use', '261009095632.0.0'),
        ])
        databases = [args[args.index('--name') + 1] for tool, *args in commands
                     if tool == 'docker' and args[0] == 'run' and 'POSTGRES_USER=postgres' in args]
        self.assertEqual(len(set(databases)), 2)
        # Each lane cleans its own database/app and network after its checks.
        cleanups = [args for tool, *args in commands
                    if tool == 'docker' and args[:2] == ['network', 'rm']]
        self.assertEqual(len(cleanups), 2)

    def test_below_floor_acceptance_still_fails_the_whole_gate(self):
        result, commands = self.run_gate(refuse_floor=False)
        self.assertEqual(result.returncode, 23)
        floor_probes = [args for tool, *args in commands
                        if tool == 'python3' and args[1] == 'account-use']
        self.assertEqual(len(floor_probes), 1)
        self.assertEqual(floor_probes[0][floor_probes[0].index('--version') + 1], '261009095632.0.0')


class MigrationRuntimeTest(unittest.TestCase):
    def test_latest_compatibility_and_legacy_refusal_use_their_own_images(self):
        # Risk: after publishing a capable release, an activated 200 from that
        # release must not replace the below-floor binary's strict refusal gate.
        floor_digest = 'sha256:d916ebb57249fda5f192e74b37ebd770c0eb67c26aafeb1c0045a635e8aa940c'
        floor_version = '261009095632.0.0'
        latest_digest = 'sha256:' + 'a' * 64
        latest_version = '261010123154.0.0'
        for legacy_fails in (False, True):
            with self.subTest(legacy_fails=legacy_fails), tempfile.TemporaryDirectory() as directory:
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
        record(['boot', args[-1]])
    elif args[0] == 'port':
        print('127.0.0.1:18080')
    elif args[0] == 'exec' and '-i' in args:
        sys.stdin.read()
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
        if current != floor_id or version != '261009095632.0.0':
            sys.exit('activated capable release correctly returns HTTP 200')
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
                        'AEON_COMPAT_TEST_LEGACY_FAILS': str(legacy_fails).lower()},
                    capture_output=True, text=True, timeout=30, check=False)
                events = [json.loads(line) for line in (root / 'events.jsonl').read_text().splitlines()]
                latest_id, floor_id = 'sha256:' + 'b' * 64, 'sha256:' + 'c' * 64
                checks = [event for event in events if event[:2] == ['probe', 'check']]
                self.assertEqual(checks, [['probe', 'check', latest_id, latest_version],
                    ['probe', 'check', floor_id, floor_version]])
                self.assertEqual([event for event in events if event[:2] == ['probe', 'account-use']],
                    [['probe', 'account-use', floor_id, floor_version]])
                self.assertEqual([event for event in events if event[0] == 'pull'],
                    [['pull', 'ghcr.io/inspr-at/aeon@' + latest_digest],
                     ['pull', 'ghcr.io/inspr-at/aeon@' + floor_digest]])
                self.assertLess(events.index(['migrate']), events.index(checks[0]))
                self.assertLess(events.index(checks[0]), events.index(checks[1]))
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
