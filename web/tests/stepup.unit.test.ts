// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it, vi } from 'vitest'
import { draftFor } from '../src/lib/decisionDesk'
import { commitDesk, emptySources, loadDesk, stepupItem } from '../src/lib/decisionDeskApi'
import { settledElsewhere, StepupNavigation, stepupRows, type StepupRequest } from '../src/lib/stepup'

export function stepupFixture(state: StepupRequest['state'] = 'pending'): StepupRequest {
  return { id: 'step-1', requested_by: 'agent-1', permission: 'settings.manage', payload: { kind: 'feature', key: 'workspace-summary', project_id: null, enabled: true, expected_revision: 0 },
    before: { key: 'workspace-summary', project_id: null, override: null, revision: 0 }, after: { key: 'workspace-summary', project_id: null, override: true, revision: 1 },
    before_hash: 'b'.repeat(64), after_hash: 'c'.repeat(64), request_digest: 'd'.repeat(64), created_at: '2026-10-09T15:00:00Z', expires_at: new Date(Date.now() + 600_000).toISOString(), state, revision: 1 }
}
const credential = { id: 'cred', rawId: new Uint8Array([1]).buffer, type: 'public-key', authenticatorAttachment: 'platform', getClientExtensionResults: () => ({}),
  response: { clientDataJSON: new Uint8Array([2]).buffer, authenticatorData: new Uint8Array([3]).buffer, signature: new Uint8Array([4]).buffer, userHandle: null } }
function server(options: Record<string, unknown>, settle: (body: Record<string, unknown>) => StepupRequest) {
  const calls: { path: string; body: Record<string, unknown> }[] = []
  vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
    const body = JSON.parse(init?.body as string ?? '{}'); calls.push({ path, body })
    return Response.json(path.endsWith('/options') ? options : settle(body))
  }))
  return calls
}
const sourced = (request: StepupRequest) => { const item = stepupItem(request), sources = emptySources(); sources.stepups.set(item.id, request); return { item, sources } }
afterEach(() => vi.unstubAllGlobals())

it('approves with a bound passkey assertion and names the method in the Decided answer', async () => {
  const request = stepupFixture()
  const calls = server({ method: 'passkey', challenge_id: 'challenge-1', publicKey: { challenge: 'AQID', rpId: 'test' } },
    () => ({ ...request, state: 'applied', revision: 2, decided_by: 'person-1', decided_by_name: 'Markus', decision: 'approve', method: 'passkey_platform', applied_at: '2026-10-09T15:02:00Z' }))
  const get = vi.fn(async () => credential)
  const { item, sources } = sourced(request)
  const result = await commitDesk(item, { ...draftFor(item), optionId: 'approve' }, sources, 'unused', false, { stepup: { credentials: { get } as never } })
  expect(calls.map(call => call.path)).toEqual(['/api/stepup-requests/step-1/options', '/api/stepup-requests/step-1/approve'])
  expect(calls[0]!.body).toEqual({ request_digest: request.request_digest, revision: 1 })
  expect(calls[1]!.body).toMatchObject({ request_digest: request.request_digest, revision: 1, challenge_id: 'challenge-1', credential: { id: 'cred', response: { signature: 'BA' } } })
  expect(result).toMatchObject({ decided: true, answer: 'Approved · Markus · device passkey' })
  expect(sources.stepups.get(item.id)?.state).toBe('applied')
})
it('starts the fresh sign-in by navigation and never reports a decision', async () => {
  const navigate = vi.fn()
  const calls = server({ method: 'oidc_reauth', authorize_url: 'https://login.example.test/authorize?prompt=login&max_age=0' }, () => { throw new Error('no approve call') })
  const { item, sources } = sourced(stepupFixture())
  await expect(commitDesk(item, { ...draftFor(item), optionId: 'approve' }, sources, 'unused', false, { stepup: { navigate } })).rejects.toBeInstanceOf(StepupNavigation)
  expect(navigate).toHaveBeenCalledWith('https://login.example.test/authorize?prompt=login&max_age=0')
  expect(calls).toHaveLength(1)
  expect(sources.stepups.get(item.id)?.state).toBe('pending')
})
it('refuses unsafe sign-in addresses, cancelled passkeys and changed sources without approving', async () => {
  const navigate = vi.fn()
  let calls = server({ method: 'oidc_reauth', authorize_url: 'javascript:alert(1)' }, () => { throw new Error('no approve call') })
  let { item, sources } = sourced(stepupFixture())
  await expect(commitDesk(item, { ...draftFor(item), optionId: 'approve' }, sources, 'unused', false, { stepup: { navigate } })).rejects.toThrow('not secure')
  expect(navigate).not.toHaveBeenCalled()
  calls = server({ method: 'passkey', challenge_id: 'challenge-1', publicKey: { challenge: 'AQID' } }, () => { throw new Error('no approve call') })
  const get = vi.fn(async () => { throw new DOMException('cancelled', 'NotAllowedError') })
  await expect(commitDesk(item, { ...draftFor(item), optionId: 'approve' }, sources, 'unused', false, { stepup: { credentials: { get } as never } })).rejects.toThrow('cancelled. Nothing changed')
  expect(calls.map(call => call.path)).toEqual(['/api/stepup-requests/step-1/options'])
  ;({ item, sources } = sourced(stepupFixture())); sources.stepups.set(item.id, { ...stepupFixture(), request_digest: 'e'.repeat(64) })
  calls = server({}, () => stepupFixture('applied'))
  await expect(commitDesk(item, { ...draftFor(item), optionId: 'decline' }, sources, 'unused', false)).rejects.toThrow('changed')
  expect(calls).toHaveLength(0)
})
it('declines without a step-up and keeps another decider’s first outcome honest', async () => {
  const request = stepupFixture()
  const calls = server({}, () => ({ ...request, state: 'declined', revision: 2, decided_by: 'person-1', decided_by_name: 'Markus', decision: 'decline' }))
  const { item, sources } = sourced(request)
  const result = await commitDesk(item, { ...draftFor(item), optionId: 'decline' }, sources, 'unused', false)
  expect(calls.map(call => call.path)).toEqual(['/api/stepup-requests/step-1/decline'])
  expect(result.answer).toBe('Declined · Markus')
  expect(settledElsewhere(result.stepup!, 'person-1')).toBeUndefined()
  expect(settledElsewhere({ ...result.stepup!, state: 'applied', decided_by: 'person-2', decided_by_name: 'Anna', method: 'passkey' }, 'person-1')).toBe('Decided by Anna first: Approved · Anna · passkey. Nothing for you to do.')
  expect(settledElsewhere({ ...request, state: 'withdrawn' }, 'person-1')).toBe('Withdrawn by the agent. Nothing changed.')
})
it('shows the feature change as Before → After and reads both states bounded', async () => {
  const rows = stepupRows(stepupFixture(), 'Workspace')
  expect(rows.before.map(row => [row.label, row.value, row.changed])).toEqual([['Feature', 'workspace-summary', false], ['Applies to', 'Workspace', false], ['Setting', 'Inherited', true]])
  expect(rows.after.at(-1)).toEqual({ label: 'Setting', value: 'On', changed: true })
  const reads: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (path: string) => {
    reads.push(path)
    if (path.startsWith('/api/stepup-requests')) return Response.json(path.includes('state=pending') ? { items: [stepupFixture()], has_more: true, next_cursor: 'x' } : { items: [{ ...stepupFixture('expired'), id: 'step-2' }], has_more: false })
    return Response.json(new URL(path, 'https://test.invalid').pathname === '/api/approvals' ? [] : { items: [], has_more: false })
  }))
  const read = await loadDesk()
  expect(reads.filter(path => path.startsWith('/api/stepup-requests'))).toEqual(['/api/stepup-requests?state=pending&limit=100', '/api/stepup-requests?state=decided&limit=100'])
  expect(read.items.filter(item => item.kind === 'stepup').map(item => [item.title, item.decided, item.answer])).toEqual([['Turn on workspace-summary for the workspace?', false, undefined], ['Turn on workspace-summary for the workspace?', true, 'Expired without a decision']])
  expect(read.warnings).toContain('More step-up approvals are waiting; the first 100 are shown.')
})
