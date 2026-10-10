# SPDX-License-Identifier: AGPL-3.0-only
import contextlib
import importlib.util
import io
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest.mock import Mock, patch


class MigrationCompatibilityRunnerTest(unittest.TestCase):
    # Risk: a capable latest release must still pass read compatibility, while
    # an actual pre-capability image must retain every activated refusal check.
    def test_latest_and_below_floor_images_keep_separate_compatibility_checks(self):
        latest = '261010123154.0.0'
        latest_digest = 'sha256:c9952c1561c8d32c4a3efb17ed95934f81efd6bc2546de41a3b56bd138304deb'
        below_floor = '261009095632.0.0'
        below_floor_digest = 'sha256:d916ebb57249fda5f192e74b37ebd770c0eb67c26aafeb1c0045a635e8aa940c'
        source = Path(__file__).with_name('migration-compat.sh').read_text()
        for version, digest, refuses in [(latest, latest_digest, True),
                                          (latest, latest_digest, False),
                                          (below_floor, below_floor_digest, True)]:
            with self.subTest(version=version, refuses=refuses), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                scripts, binaries = root / 'scripts', root / 'bin'
                scripts.mkdir()
                binaries.mkdir()
                runner = scripts / 'migration-compat.sh'
                runner.write_text(source)
                calls = root / 'calls.jsonl'
                # Exercise the real shell orchestration without Docker, a build,
                # or a network. The latest capable binary succeeds at resolve;
                # treating it as below-floor produces the exact CI failure.
                stub = f'''#!{sys.executable}
import json
from pathlib import Path
import sys
name, args = Path(sys.argv[0]).name, sys.argv[1:]
with Path({str(calls)!r}).open('a') as output:
    output.write(json.dumps([name, args]) + '\\n')
if name == 'docker' and args[:2] == ['image', 'ls']:
    print('sha256:' + 'a' * 64)
elif name == 'docker' and args[:1] == ['port']:
    print('127.0.0.1:8080')
elif name == 'python3' and args[1] == 'account-use':
    version = args[args.index('--version') + 1]
    if version != {below_floor!r} or not {refuses!r}:
        print('Migration compatibility failed: /api/models/resolve?role=build&harness=codex: activated previous binary returned HTTP 200', file=sys.stderr)
        sys.exit(1)
'''
                for name in ('docker', 'python3', 'go', 'trash'):
                    executable = binaries / name
                    executable.write_text(stub)
                    executable.chmod(0o755)
                # Supply a minimal environment; never inherit credentials or
                # the real CI summary path into this disposable runner.
                result = subprocess.run(['bash', str(runner), 'v' + version, digest],
                                        env={'PATH': str(binaries) + ':/usr/bin:/bin'},
                                        capture_output=True, text=True, timeout=15, check=False)
                if not refuses:
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn('activated previous binary returned HTTP 200', result.stderr)
                else:
                    self.assertEqual(result.returncode, 0, result.stderr)
                recorded = [json.loads(line) for line in calls.read_text().splitlines()]
                expected = [(latest, latest_digest), (below_floor, below_floor_digest)] if version == latest else [(below_floor, below_floor_digest)]
                pulls = [args[-1] for name, args in recorded if name == 'docker' and args[:1] == ['pull']]
                self.assertEqual(pulls, ['ghcr.io/inspr-at/aeon@' + digest for _, digest in expected])
                for mode in ('seed', 'check'):
                    versions = [args[args.index('--version') + 1] for name, args in recorded
                                if name == 'python3' and args[1] == mode]
                    self.assertEqual(versions, [version for version, _ in expected])
                activated = [args[args.index('--version') + 1] for name, args in recorded
                             if name == 'python3' and args[1] == 'account-use']
                self.assertEqual(activated, [below_floor])
                migrations = [args for name, args in recorded if name == 'go']
                self.assertEqual(migrations, [['run', '-p', '2', './scripts/migrate-candidate.go']] * len(expected))


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


if __name__ == '__main__':
    unittest.main()
