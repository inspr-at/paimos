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
if tool == 'docker':
    if args[:2] == ['image', 'ls']:
        print(floor_id if args[-1].endswith(os.environ['COMPAT_FLOOR_DIGEST']) else latest_id)
    elif args[0] == 'port':
        print('127.0.0.1:5432' if args[-1] == '5432/tcp' else '127.0.0.1:8080')
    elif args[0] == 'run' and args[-1] in (latest_id, floor_id):
        active.write_text(args[-1])
elif tool == 'python3':
    mode = args[1]
    if mode == 'seed':
        Path(args[args.index('--state') + 1]).write_text('{}')
    if mode in ('seed', 'check', 'account-use'):
        version = args[args.index('--version') + 1]
        expected = '261009095632.0.0' if active.read_text() == floor_id else '261010123154.0.0'
        if version != expected:
            sys.exit('tested binary does not match the previous release')
    if mode == 'account-use' and active.read_text() != floor_id:
        sys.exit('activated previous binary returned HTTP 200')
elif tool == 'git':
    if args != ['show', 'v261009095632.0.0:internal/db/visibility.go']:
        sys.exit('unexpected git call')
    print('func enterTenant() {}')
elif tool not in ('go', 'trash'):
    sys.exit('unexpected tool')
'''
            for name in ('docker', 'python3', 'go', 'git', 'trash'):
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
            self.assertEqual(boots, ['sha256:' + '1' * 64] * 2 + ['sha256:' + '2' * 64])
            probes = [(args[1], args[args.index('--version') + 1]) for tool, args in calls
                      if tool == 'python3' and '--version' in args]
            self.assertEqual(probes, [
                ('seed', latest_tag[1:]), ('check', latest_tag[1:]),
                ('check', '261009095632.0.0'), ('account-use', '261009095632.0.0')])
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
                env={**os.environ, 'PATH': str(tools) + os.pathsep + os.environ['PATH'],
                     'GITHUB_STEP_SUMMARY': str(root / 'summary')},
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
    log.write(json.dumps({'command': name, 'args': args, 'image': state.get('image'), 'migrated': state.get('migrated', False)}) + '\\n')
if name == 'docker':
    if args[:2] == ['image', 'ls']:
        print(args[-1].split('@')[1])
    elif args[0] == 'run' and '--name' in args and args[args.index('--name') + 1].startswith('aeon-compat-app-'):
        state['image'] = args[-1]
    elif args[0] == 'port':
        print('127.0.0.1:8080' if args[-1] == '8080/tcp' else '127.0.0.1:5432')
    elif args[0] == 'exec' and '-i' in args:
        sys.stdin.read()
elif name == 'git':
    if args != ['show', 'v261009095632.0.0:internal/db/visibility.go']:
        sys.exit('unexpected git call')
    print('func enterTenant() {}')
elif name == 'go':
    state['migrated'] = True
elif name == 'python3':
    mode = args[1]
    if mode == 'seed':
        Path(args[args.index('--state') + 1]).write_text('{}')
    elif mode == 'account-use':
        if state.get('image') != os.environ['HARNESS_LEGACY_DIGEST']:
            sys.exit('capability refusal must exercise the immutable legacy image')
        if os.environ['HARNESS_FAIL_BOUNDARY'] == '1':
            sys.exit(23)
state_path.write_text(json.dumps(state))
'''
            for name in ('docker', 'python3', 'go', 'git', 'trash'):
                executable = bin_dir / name
                executable.write_text('#!' + sys.executable + '\n' + command)
                executable.chmod(0o755)
            for fail_boundary in (False, True):
                with self.subTest(fail_boundary=fail_boundary):
                    log = root / f'commands-{fail_boundary}.jsonl'
                    harness_env = dict(os.environ, PATH=str(bin_dir) + os.pathsep + os.environ['PATH'],
                        HARNESS_LOG=str(log), HARNESS_STATE=str(root / f'state-{fail_boundary}.json'),
                        HARNESS_LEGACY_DIGEST=legacy_digest, HARNESS_FAIL_BOUNDARY=str(int(fail_boundary)),
                        GITHUB_STEP_SUMMARY=str(root / f'summary-{fail_boundary}.txt'))
                    result = subprocess.run(['bash', str(scripts / 'migration-compat.sh'), latest_tag, latest_digest],
                        env=harness_env, capture_output=True, text=True, timeout=15, check=False)
                    self.assertEqual(result.returncode, 23 if fail_boundary else 0, result.stdout + result.stderr)
                    events = [json.loads(line) for line in log.read_text().splitlines()]
                    migrations = [event for event in events if event['command'] == 'go']
                    self.assertEqual(len(migrations), 1)
                    probes = [event for event in events if event['command'] == 'python3']
                    reads = [event for event in probes if event['args'][1] == 'check']
                    self.assertEqual([(event['image'], event['migrated'], event['args'][-1]) for event in reads],
                        [(latest_digest, True, latest_tag[1:]), (legacy_digest, True, legacy_tag[1:])])
                    boundary = [event for event in probes if event['args'][1] == 'account-use']
                    self.assertEqual(len(boundary), 1)
                    self.assertEqual(boundary[0]['image'], legacy_digest)
                    self.assertTrue(boundary[0]['migrated'])
                    self.assertEqual(boundary[0]['args'][boundary[0]['args'].index('--version') + 1], legacy_tag[1:])
                    if fail_boundary:
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
elif tool == 'git':
    if args == ['show', 'v261009095632.0.0:internal/db/visibility.go']:
        print('func enterTenant() {}')
    else:
        sys.exit('unexpected git call')
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
                for name in ('docker', 'go', 'python3', 'git', 'trash'):
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
