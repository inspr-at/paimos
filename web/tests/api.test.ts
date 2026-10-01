// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, test } from 'node:test'
import assert from 'node:assert/strict'
import { api, getSession, sessionEnded } from '../src/lib/api.ts'

const originalFetch = globalThis.fetch
afterEach(() => { globalThis.fetch = originalFetch; sessionEnded.blocked = false })

function respond(body: unknown, status = 200) {
  sessionEnded.blocked = false
  globalThis.fetch = async () => new Response(JSON.stringify(body), {
    status, headers: { 'Content-Type': 'application/json' },
  })
}

test('a 401 blocks later protected requests while sign-in remains available', async () => {
  const urls: string[] = []
  globalThis.fetch = async url => {
    urls.push(String(url))
    return new Response(null, { status: String(url) === '/api/nodes' ? 401 : 204 })
  }
  assert.equal((await api('/nodes')).status, 401)
  assert.equal((await api('/nodes')).status, 401)
  assert.equal((await api('/auth/dev-login')).status, 204)
  assert.deepEqual(urls, ['/api/nodes', '/api/auth/dev-login'])
})

test('accepts the authenticated principal and tenant', async () => {
  const identity = { principal: { id: 'p1', name: 'Markus Barta' }, tenant: { id: 't1', name: 'INSPR' } }
  respond(identity)
  assert.deepEqual(await getSession(), { identity, devMode: false })
})

test('a bare 401 still redirects to sign-in with development disabled', async () => {
  globalThis.fetch = async () => new Response(null, { status: 401 })
  assert.deepEqual(await getSession(), { identity: null, devMode: false })
})

test('only a literal server true enables development sign-in', async () => {
  for (const dev_mode of [undefined, null, false, 'true', 1]) {
    respond({ dev_mode }, 401)
    assert.equal((await getSession()).devMode, false)
  }
  respond({ dev_mode: true }, 401)
  assert.deepEqual(await getSession(), { identity: null, devMode: true })
})

test('reads the public OIDC display name on anonymous and authenticated responses', async () => {
  const identity = { principal: { id: 'p1', name: 'Person' }, tenant: { id: 't1', name: 'Workspace' } }
  for (const status of [200, 401]) {
    for (const oidc_display_name of ['', ' \t ', ' Acme SSO ', 'INSPR ID']) {
      respond({ ...(status === 200 ? identity : {}), oidc_display_name }, status)
      assert.equal((await getSession()).oidcDisplayName, oidc_display_name.trim())
    }
    for (const oidc_display_name of [undefined, null, true, 123, {}]) {
      respond({ ...(status === 200 ? identity : {}), oidc_display_name }, status)
      assert.equal((await getSession()).oidcDisplayName, undefined)
    }
  }
})

test('rejects malformed identities and server errors', async () => {
  for (const body of [{}, { principal: { name: 'Someone' }, tenant: { name: 'INSPR' } }]) {
    respond(body)
    await assert.rejects(getSession(), /Invalid session/)
  }
  respond({ dev_mode: true }, 503)
  await assert.rejects(getSession(), /Session unavailable/)
})

test('API requests use same-origin credentials, no cache and a timeout signal', async () => {
  globalThis.fetch = async (url, options) => {
    assert.equal(url, '/api/auth/logout')
    assert.equal(options?.method, 'POST')
    assert.equal(options?.credentials, 'same-origin')
    assert.equal(options?.cache, 'no-store')
    assert.ok(options?.signal instanceof AbortSignal)
    assert.equal(new Headers(options?.headers).get('Accept'), 'application/json')
    return new Response(null, { status: 204 })
  }
  assert.equal((await api('/auth/logout', { method: 'POST' })).status, 204)
})

test('R1 search encodes its query and opaque cursors', async () => {
  const { searchNodes } = await import('../src/lib/api.ts')
  const urls: string[] = []
  globalThis.fetch = async url => { urls.push(String(url)); return Response.json({ items: [], next_cursor: null }) }
  await searchNodes('a & b/文', { cursor: 'a+/=' })
  const search = new URL(urls[0]!, 'https://aeon.invalid')
  assert.equal(search.searchParams.get('q'), 'a & b/文')
  assert.equal(search.searchParams.get('cursor'), 'a+/=')
})

test('R1 mutations send JSON, tolerate 204, and surface API failures', async () => {
  const { updateNode, deleteNode, APIError } = await import('../src/lib/api.ts')
  globalThis.fetch = async (url, init) => {
    assert.equal(url, '/api/nodes/node%2Fid')
    assert.equal(init?.method, 'PATCH')
    assert.equal(new Headers(init?.headers).get('Content-Type'), 'application/json')
    assert.deepEqual(JSON.parse(String(init?.body)), { body: '# Changed' })
    return Response.json({ id: 'node/id', body: '# Changed' })
  }
  assert.equal((await updateNode('node/id', { body: '# Changed' })).body, '# Changed')
  globalThis.fetch = async () => new Response(null, { status: 204 })
  assert.deepEqual(await deleteNode('node-1'), { revision: null })
  // A delete names the revision of its event (AEON-326).
  globalThis.fetch = async () => new Response(null, { status: 204, headers: { 'aeon-revision': '2026-09-29T10:00:04.000001Z' } })
  assert.deepEqual(await deleteNode('node-2'), { revision: '2026-09-29T10:00:04.000001Z' })
  respond({ error: 'Owner only' }, 403)
  await assert.rejects(deleteNode('node-1'), error => error instanceof APIError && error.status === 403 && error.message === 'Owner only')
  globalThis.fetch = async () => new Response('gateway unavailable', { status: 502 })
  await assert.rejects(deleteNode('node-1'), /Request failed \(502\)/)
})

test('R2 reads and mutations use only the human-session contract', async () => {
  const { listAccounts, listApprovals, setAccountState, decideApproval, revokeApproval } = await import('../src/lib/agents.ts')
  const { getRun } = await import('../src/lib/agentRows.ts')
  const calls: { url: string; method: string; body: unknown }[] = []
  globalThis.fetch = async (url, init) => {
    calls.push({ url: String(url), method: init?.method ?? 'GET', body: init?.body ? JSON.parse(String(init.body)) : undefined })
    assert.equal(init?.credentials, 'same-origin')
    if (init?.body) assert.equal(new Headers(init.headers).get('Content-Type'), 'application/json')
    return Response.json({})
  }
  await listAccounts(); await getRun('run/id'); await listApprovals()
  await setAccountState('account/id', 'draining')
  await decideApproval('approval/id', 'denied', 'Wrong resource')
  await revokeApproval('approval/id')
  assert.deepEqual(calls, [
    { url: '/api/agent-accounts', method: 'GET', body: undefined },
    { url: '/api/runs/run%2Fid', method: 'GET', body: undefined },
    { url: '/api/approvals?limit=200', method: 'GET', body: undefined },
    { url: '/api/agent-accounts/account%2Fid', method: 'PATCH', body: { state: 'draining' } },
    { url: '/api/approvals/approval%2Fid/decision', method: 'POST', body: { decision: 'denied', reason: 'Wrong resource' } },
    { url: '/api/approvals/approval%2Fid/revoke', method: 'POST', body: undefined },
  ])
})

test('R2 failures retain HTTP status and tolerate non-JSON error bodies', async () => {
  const { getRun } = await import('../src/lib/agentRows.ts')
  const { APIError } = await import('../src/lib/api.ts')
  for (const status of [401, 403, 404, 409, 503]) {
    respond({ error: 'Not available' }, status)
    await assert.rejects(getRun('run-1'), error => error instanceof APIError && error.status === status && error.message === 'Not available')
  }
  globalThis.fetch = async () => new Response('Unavailable', { status: 502 })
  await assert.rejects(getRun('run-1'), /Request failed \(502\)/)
})
