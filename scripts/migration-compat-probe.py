#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Seed through the previous binary, then verify its SPA reads after migration."""
import argparse
import http.cookiejar
import json
from pathlib import Path
import urllib.error
import urllib.request


class Probe:
    def __init__(self, base):
        self.base = base
        self.opener = urllib.request.build_opener(
            urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

    def call(self, path, body=None):
        request = urllib.request.Request(
            self.base + path,
            data=None if body is None else json.dumps(body).encode(),
            headers={} if body is None else {'Content-Type': 'application/json'})
        try:
            with self.opener.open(request, timeout=15) as response:
                if response.status not in (200, 201):
                    raise AssertionError(f'{path}: HTTP {response.status}')
                if response.headers.get_content_type() != 'application/json':
                    raise AssertionError(f'{path}: expected JSON')
                payload = json.load(response)
        except urllib.error.HTTPError as error:
            # Do not dump headers, cookies or response bodies into CI logs.
            status = error.code
            error.close()
            raise AssertionError(f'{path}: HTTP {status}') from None
        print(f'previous release: {path} OK')
        return payload

    def login(self):
        self.call('/api/auth/dev-login', {'email': 'compat@example.invalid'})

    def seed(self):
        self.login()
        kinds = {item['slug']: item['id'] for item in self.call('/api/kinds')['items']}
        project = self.call('/api/nodes', {'kind_id': kinds['project'], 'title': 'Migration compatibility project'})
        ticket = self.call('/api/nodes', {'kind_id': kinds['ticket'], 'parent_id': project['id'],
                                          'title': 'Migration compatibility ticket'})
        return {'project': project['id'], 'ticket': ticket['id']}

    def check(self, state, version):
        if self.call('/api/health') != {'status': 'ok', 'db': 'ok'}:
            raise AssertionError('previous release health is not OK')
        if self.call('/api/ready') != {'status': 'ready'}:
            raise AssertionError('previous release is not ready')
        if self.call('/api/version')['version'] != version:
            raise AssertionError('tested binary does not match the previous release')
        with self.opener.open(self.base + '/', timeout=15) as response:
            if response.status != 200 or b'<html' not in response.read().lower():
                raise AssertionError('previous release SPA is missing')
        self.login()
        for path in ['/api/me', '/api/me/permissions', '/api/members', '/api/kinds']:
            if not isinstance(self.call(path), dict):
                raise AssertionError(f'{path}: expected object')
        projects = self.call('/api/projects?include_archived=true')['items']
        if state['project'] not in {item['id'] for item in projects}:
            raise AssertionError('seeded project missing from SPA summaries')
        for path, key in [('/api/nodes?kind=project&limit=100', 'project'),
                          (f"/api/nodes?parent_id={state['project']}&limit=100", 'ticket')]:
            if state[key] not in {item['id'] for item in self.call(path)['items']}:
                raise AssertionError(f'{path}: seeded node missing')
        for key in ('project', 'ticket'):
            if self.call(f"/api/nodes/{state[key]}")['id'] != state[key]:
                raise AssertionError('seeded node detail changed')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('mode', choices=['seed', 'check'])
    parser.add_argument('--base', required=True)
    parser.add_argument('--state', type=Path, required=True)
    parser.add_argument('--version', required=True)
    args = parser.parse_args()
    probe = Probe(args.base)
    if args.mode == 'seed':
        state = probe.seed()
        args.state.write_text(json.dumps(state))
    else:
        state = json.loads(args.state.read_text())
    probe.check(state, args.version)


if __name__ == '__main__':
    try:
        main()
    except (AssertionError, OSError, ValueError, KeyError) as error:
        raise SystemExit(f'Migration compatibility failed: {error}') from None
