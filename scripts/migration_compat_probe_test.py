# SPDX-License-Identifier: AGPL-3.0-only
import contextlib
import importlib.util
import io
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import threading
import unittest

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


if __name__ == '__main__':
    unittest.main()
