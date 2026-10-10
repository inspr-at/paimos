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


class MigrationCompatHarnessTest(unittest.TestCase):
    # Risk: after the capability ships, the latest image legitimately enters
    # activated tenants. Migration reads still need that image, while every
    # rollback-floor assertion needs a real published image below the floor.
    latest_tag = 'v261010123154.0.0'
    latest_digest = 'sha256:' + 'a' * 64
    legacy_tag = 'v261009095632.0.0'
    legacy_digest = 'sha256:d916ebb57249fda5f192e74b37ebd770c0eb67c26aafeb1c0045a635e8aa940c'

    def run_harness(self, legacy_entry='func enterTenant() {}', legacy_pull_rc=0):
        with tempfile.TemporaryDirectory(prefix='aeon-migration-harness-') as directory:
            root = Path(directory)
            scripts, tools = root / 'scripts', root / 'bin'
            scripts.mkdir()
            tools.mkdir()
            script = scripts / 'migration-compat.sh'
            script.write_text(Path(module.__file__).with_name('migration-compat.sh').read_text())
            log = root / 'calls.jsonl'
            stub = f'''#!{sys.executable}
import json
from pathlib import Path
import sys
tool, args = Path(sys.argv[0]).name, sys.argv[1:]
with Path({str(log)!r}).open('a') as output:
    output.write(json.dumps([tool, *args]) + '\\n')
if tool == 'docker':
    if args[:1] == ['pull'] and args[-1].endswith({self.legacy_digest!r}):
        sys.exit({legacy_pull_rc})
    elif args[:2] == ['image', 'ls']:
        print('sha256:' + ('a' if args[-1].endswith({self.latest_digest!r}) else 'b') * 64)
    elif args[:1] == ['port']:
        print('127.0.0.1:' + ('18080' if args[-1] == '8080/tcp' else '15432'))
elif tool == 'git':
    if args == ['show', {self.legacy_tag!r} + ':internal/db/visibility.go']:
        print({legacy_entry!r})
    else:
        sys.exit(19)
'''
            for name in ('docker', 'go', 'python3', 'git', 'trash'):
                path = tools / name
                path.write_text(stub)
                path.chmod(0o700)
            # Only the subprocess's PATH changes; neither Docker nor a binary
            # runs. The real shell orchestrator must select both immutable IDs.
            completed = subprocess.run(['bash', str(script), self.latest_tag, self.latest_digest],
                env={**os.environ, 'PATH': str(tools) + os.pathsep + os.environ['PATH']},
                capture_output=True, text=True, timeout=15, check=False)
            calls = [json.loads(line) for line in log.read_text().splitlines()]
            return completed, calls

    def test_latest_image_reads_and_below_floor_image_refusals_both_run(self):
        completed, calls = self.run_harness()
        self.assertEqual(completed.returncode, 0, completed.stderr)
        pulls = [call[-1] for call in calls if call[:2] == ['docker', 'pull']]
        self.assertEqual(pulls, ['ghcr.io/inspr-at/aeon@' + self.latest_digest,
                                 'ghcr.io/inspr-at/aeon@' + self.legacy_digest])
        boots = [call[-1] for call in calls if call[:2] == ['docker', 'run'] and '--memory' in call]
        self.assertEqual(boots, ['sha256:' + 'a' * 64, 'sha256:' + 'a' * 64,
                                 'sha256:' + 'b' * 64])
        probes = [call for call in calls if call[:2] == ['python3', 'scripts/migration-compat-probe.py']]
        modes = [call[2] for call in probes]
        self.assertEqual(modes, ['wait-ready', 'seed', 'wait-ready', 'check',
                                 'wait-ready', 'check', 'account-use'])
        checks = [call[call.index('--version') + 1] for call in probes if call[2] == 'check']
        self.assertEqual(checks, [self.latest_tag[1:], self.legacy_tag[1:]])
        activation = next(call for call in probes if call[2] == 'account-use')
        self.assertEqual(activation[activation.index('--version') + 1], self.legacy_tag[1:])
        self.assertIn(['git', 'show', self.legacy_tag + ':internal/db/visibility.go'], calls)
        migration = next(i for i, call in enumerate(calls) if call[:2] == ['go', 'run'])
        self.assertLess(migration, calls.index(probes[modes.index('check')]))
        self.assertLess(calls.index(probes[-2]), calls.index(activation))

    def test_a_capable_pin_cannot_satisfy_the_rollback_floor_probe(self):
        completed, calls = self.run_harness(legacy_entry='func enterTenant() { aeon.account_use_capable }')
        self.assertEqual(completed.returncode, 1)
        self.assertIn('Pinned rollback image must predate the account-use capability', completed.stderr)
        self.assertFalse(any(call[:3] == ['python3', 'scripts/migration-compat-probe.py', 'account-use'] for call in calls))

    def test_a_failed_rollback_image_pull_cannot_report_a_pass(self):
        completed, calls = self.run_harness(legacy_pull_rc=19)
        self.assertEqual(completed.returncode, 19)
        self.assertNotIn('Account-use rollback floor passed:', completed.stdout)
        self.assertFalse(any(call[:3] == ['python3', 'scripts/migration-compat-probe.py', 'account-use'] for call in calls))


if __name__ == '__main__':
    unittest.main()
