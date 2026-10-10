#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Seed through the previous binary, then verify its SPA reads after migration."""
import argparse
import hashlib
import http.cookiejar
import json
from pathlib import Path
import secrets
import subprocess
import time
import urllib.error
import urllib.request
import uuid


def previous_account_use_capable(tag):
    """The pinned release's embedded entry migration identifies its floor.

    A supported release must pass policy checks; an HTTP error never chooses
    the legacy branch or turns a broken supported release into a passing gate.
    """
    result = subprocess.run(['git', 'ls-tree', '--name-only', tag, '--',
        'internal/db/migrations/1310_account_use_entry_guard.sql'],
        capture_output=True, text=True, timeout=15, check=False)
    if result.returncode:
        raise AssertionError('cannot determine the pinned release account-use floor')
    return bool(result.stdout.strip())


def release_ready(payload):
    """A previous binary is ready only when status is ready and it names no failure.

    AEON-995 adds pool statistics beside status. Older releases return status
    alone. A reason, another field, or a non-object is not readiness.
    """
    if not isinstance(payload, dict) or set(payload) - {'status', 'pool'}:
        return False
    pool = payload.get('pool')
    return payload.get('status') == 'ready' and (pool is None or isinstance(pool, dict))


def wait_ready(base, attempts=60, pause=1):
    for _ in range(attempts):
        try:
            with urllib.request.urlopen(base + '/api/ready', timeout=2) as response:
                if response.status == 200 and release_ready(json.load(response)):
                    return
        except (OSError, ValueError):
            pass
        if pause:
            time.sleep(pause)
    raise SystemExit('Previous release did not become ready; compatibility gate failed')


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

    def refused(self, path, body=None, status=None, message=None):
        headers = {'Origin': 'http://localhost:8080'}
        if body is not None:
            headers['Content-Type'] = 'application/json'
        request = urllib.request.Request(self.base + path,
            data=None if body is None else json.dumps(body).encode(), headers=headers)
        try:
            with self.opener.open(request, timeout=15) as response:
                payload = response.read(1 << 20)
                raise AssertionError(f'{path}: activated previous binary returned HTTP {response.status}')
        except urllib.error.HTTPError as error:
            with error:
                raw = error.read(1 << 20)
                if error.code < 400 or error.code >= 600 or b'command_template' in raw:
                    raise AssertionError(f'{path}: unsafe activated response')
                if status is not None and error.code != status:
                    raise AssertionError(f'{path}: expected HTTP {status}, got {error.code}')
                if message is not None and json.loads(raw).get('error') != message:
                    raise AssertionError(f'{path}: wrong policy refusal')
                return error.code

    def login(self):
        self.call('/api/auth/dev-login', {'email': 'compat@example.invalid'})

    def seed(self):
        self.login()
        kinds = {item['slug']: item['id'] for item in self.call('/api/kinds')['items']}
        work_kind_id = kinds.get('work') or kinds.get('ticket')
        if work_kind_id is None:
            raise AssertionError('previous release has no supported work kind')
        project = self.call('/api/nodes', {'kind_id': kinds['project'], 'title': 'Migration compatibility project'})
        ticket = self.call('/api/nodes', {'kind_id': work_kind_id, 'parent_id': project['id'],
                                          'title': 'Migration compatibility work'})
        return {'project': project['id'], 'ticket': ticket['id']}

    def check(self, state, version):
        if self.call('/api/health') != {'status': 'ok', 'db': 'ok'}:
            raise AssertionError('previous release health is not OK')
        if not release_ready(self.call('/api/ready')):
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


CAPABILITY_ERROR = 'account-use capability required: this binary is below the rollback floor'
# Released agentaccounts.ContextSkipReason: the ladder names the whole refusal,
# never a bare category, so membership must match this exact text.
CONTEXT_SKIP_REASON = "no account allowed for this project's context"


class AccountUseBoundary:
    """Disposable fixture only; SQL and bearer values never appear in output.

    Below-floor handlers sanitize DB errors into generic HTTP errors. The
    verbose log proves the exact SQLSTATE, message and entry function. Supported
    releases instead prove safe previews and exact policy/authority refusals;
    an unrelated 403/404/500 cannot satisfy either branch.
    """
    def __init__(self, probe, container, capable=False):
        self.probe, self.container = probe, container
        self.capable = capable

    def sql(self, statement):
        result = subprocess.run(['docker', 'exec', '-i', self.container, 'psql',
            '-X', '-qAt', '-U', 'postgres', '-d', 'aeon', '-v', 'ON_ERROR_STOP=1'],
            input=statement, text=True, capture_output=True, timeout=30, check=False)
        if result.returncode:
            raise AssertionError('disposable account-use fixture SQL failed')
        return result.stdout.strip()

    def errors(self):
        result = subprocess.run(['docker', 'logs', '--tail', '2000', self.container],
            capture_output=True, text=True, timeout=15, check=False)
        if result.returncode:
            raise AssertionError('cannot read disposable Postgres error evidence')
        logs = result.stdout + result.stderr
        return logs.count('0A000: ' + CAPABILITY_ERROR), logs.count('aeon_enter_principal(')

    def request(self, probe, path, body=None):
        before = self.errors()
        probe.refused(path, body)
        after = self.errors()
        if not all(a > b for a, b in zip(after, before)):
            raise AssertionError(f'{path}: refusal lacks exact capability entry evidence')
        print(f'previous release: {path} refused at capability entry (0A000)')

    def snapshot(self, tenant):
        return self.sql(f"""SELECT jsonb_build_object(
            'reservations',(SELECT coalesce(jsonb_agg(jsonb_build_array(id,state) ORDER BY id),'[]') FROM account_reservations WHERE tenant_id='{tenant}'),
            'runs',(SELECT coalesce(jsonb_agg(jsonb_build_array(id,status,started_at,daemon_id,daemon_generation) ORDER BY id),'[]') FROM agent_runs WHERE tenant_id='{tenant}'),
            'claims',(SELECT count(*) FROM events WHERE tenant_id='{tenant}' AND type IN ('run.claimed','work_order.started')));""")

    def selection(self, populated, account):
        """Supported releases enforce policy without rejecting every read."""
        path = '/api/models/resolve?role=build&harness=codex'
        resolved = self.probe.call(path)
        if resolved.get('command_template'):
            raise AssertionError(f'{path}: activated pool supplied executable material')
        if not populated:
            if resolved.get('preview') is not True or not isinstance(resolved.get('profile'), dict):
                raise AssertionError(f'{path}: empty pool is not a catalog-only preview')
        elif (resolved.get('profile') is not None or resolved.get('preview', False)
              or not any(CONTEXT_SKIP_REASON in item.get('skip_reasons', []) for item in resolved.get('ladder', []))):
            raise AssertionError(f'{path}: denied pool lacks its context refusal')
        next_path = '/api/agent-accounts/capacity/next?harness=codex'
        next_account = self.probe.call(next_path)
        reason = 'context' if populated else 'offline'
        if (next_account.get('accounts') != [] or next_account.get('parallel_runs') != 0
                or next_account.get('wait', {}).get('code') != reason):
            raise AssertionError(f'{next_path}: pool supplied capacity or lost its refusal')
        self.probe.refused(f'/api/agent-accounts/use?harness=codex&account_id={account}',
            status=409 if populated else 404,
            message='account_not_allowed_for_context' if populated else 'account not found')

    def check(self, state):
        self.probe.login()
        me = self.probe.call('/api/me')['principal']
        tenant, owner = str(uuid.UUID(me['tenant_id'])), str(uuid.UUID(me['id']))
        ticket = str(uuid.UUID(state['ticket']))
        # Prepare an enabled catalog before activation, with no daily settings.
        self.probe.call('/api/models/resolve?role=build&harness=codex')
        self.sql("ALTER SYSTEM SET log_error_verbosity='verbose'; SELECT pg_reload_conf();")
        agent, account, run, order, request = (str(uuid.uuid4()) for _ in range(5))
        prefix = tenant.replace('-', '') + secrets.token_hex(8)
        secret = secrets.token_hex(32)
        # This disposable key exercises the actual old agent authentication
        # path. It has one existing scope, stays in memory and is never saved.
        runtime = Probe(self.probe.base)
        runtime.opener.addheaders = [('Authorization', 'Bearer aeon_' + prefix + '_' + secret)]
        self.sql(f"""BEGIN;
            SELECT set_config('aeon.tenant_id','{tenant}',true),set_config('aeon.visible_projects','*',true),set_config('aeon.account_use_capable','on',true);
            INSERT INTO principals(tenant_id,id,kind,name) VALUES('{tenant}','{agent}','agent','Compatibility daemon');
            INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type)
                SELECT '{tenant}','{agent}',id,'workspace' FROM roles WHERE tenant_id='{tenant}' AND key='member';
            INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,created_by_principal_id,person_owner_required)
                VALUES('{tenant}','{agent}','Disposable compatibility runtime','{prefix}','{hashlib.sha256(secret.encode()).hexdigest()}',ARRAY['run.claim'],'{owner}',true);
            INSERT INTO nodes(tenant_id,id,kind_id,key,title,parent_id)
                SELECT '{tenant}','{order}',id,aeon_next_node_key('{tenant}',short_prefix),'Compatibility order','{ticket}' FROM node_kinds WHERE slug='work_order';
            INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id) VALUES('{tenant}','{order}','{owner}');
            INSERT INTO agent_runs(tenant_id,id,work_order_id,agent_principal_id,model_profile_id)
                SELECT '{tenant}','{run}','{order}','{agent}',id FROM model_profiles WHERE tenant_id='{tenant}' AND harness='codex' AND enabled LIMIT 1;
            COMMIT;""")
        facts = self.sql(f"SELECT (SELECT count(*) FROM agent_accounts WHERE tenant_id='{tenant}')||','||(SELECT count(*) FROM model_profiles WHERE tenant_id='{tenant}' AND enabled)||','||(SELECT enforced_at IS NULL FROM account_use_rules WHERE tenant_id='{tenant}');")
        pool, profiles, inactive = facts.split(',')
        if pool != '0' or int(profiles) < 1 or inactive != 'true':
            raise AssertionError('empty-pool counterexample fixture is not inactive with enabled profiles')
        if self.sql(f"SELECT count(*) FROM user_preferences WHERE tenant_id='{tenant}' AND key='agents.working' AND value ? 'daily';") != '0':
            raise AssertionError('empty-pool fixture unexpectedly has saved daily settings')
        # A successful old-key self request proves authentication before the floor.
        runtime.call('/api/me')
        self.sql(f"UPDATE account_use_rules SET enforced_at=clock_timestamp() WHERE tenant_id='{tenant}';")
        for populated in (False, True):
            if populated:
                self.sql(f"""BEGIN;
                    SELECT set_config('aeon.tenant_id','{tenant}',true),set_config('aeon.account_use_capable','on',true);
                    INSERT INTO agent_accounts(tenant_id,id,account_key,harness,daemon_id,registered_by_principal_id,label)
                        VALUES('{tenant}','{account}','compat','codex','compat-daemon','{agent}','Compatibility login');
                    DELETE FROM account_use_cells WHERE tenant_id='{tenant}' AND account_id='{account}';
                    UPDATE agent_runs SET account_id='{account}' WHERE id='{run}';
                    COMMIT;""")
            before = self.snapshot(tenant)
            reads = [('/api/models/resolve?role=build&harness=codex', None),
                (f'/api/agent-accounts/use?harness=codex&account_id={account}', None),
                ('/api/agent-accounts/capacity/next?harness=codex', None)]
            writes = [
                ('/api/agent-accounts/route', {'run_id':run,'daemon_id':'compat-daemon','account_ids':[account],'estimated_units':{'requests':1}}),
                (f'/api/agent-pairing/requests/{request}/approve', {'request_digest':'0'*64,'verification':'connect_only','selected_account_keys':['compat']})]
            claim = (f'/api/runs/{run}/claim', {'daemon_id':'compat-daemon','daemon_generation':'compat-generation','reservation_ids':[str(uuid.uuid4())]})
            if self.capable:
                self.selection(populated, account)
                self.probe.refused(*writes[0], status=403, message='agent key required')
                self.probe.refused(*writes[1], status=404, message='pairing not found')
                runtime.refused(*claim, status=409, message='account daemon generation changed'
                    if populated else 'account reservation required')
            else:
                for path, body in reads + writes:
                    self.request(self.probe, path, body)
                self.request(runtime, *claim)
            # The specification deliberately asks for a fixed background-job
            # window. State equality, not elapsed time, is the assertion.
            time.sleep(10)
            if self.snapshot(tenant) != before:
                raise AssertionError('old background jobs created a reservation, claim or start')
            print('previous release: activated ' + ('denied' if populated else 'empty') + ' pool and background jobs fail closed')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('mode', choices=['seed', 'check', 'wait-ready', 'account-use'])
    parser.add_argument('--base', required=True)
    parser.add_argument('--state', type=Path)
    parser.add_argument('--version')
    parser.add_argument('--database-container')
    parser.add_argument('--release-tag')
    args = parser.parse_args()
    if args.mode == 'wait-ready':
        wait_ready(args.base)
        return
    if args.state is None or not args.version:
        parser.error('seed and check require --state and --version')
    probe = Probe(args.base)
    if args.mode == 'account-use':
        if not args.database_container:
            parser.error('account-use requires --database-container')
        capable = previous_account_use_capable(args.release_tag) if args.release_tag else False
        AccountUseBoundary(probe, args.database_container, capable).check(json.loads(args.state.read_text()))
        return
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
