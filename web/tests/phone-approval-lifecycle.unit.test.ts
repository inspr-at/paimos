// SPDX-License-Identifier: AGPL-3.0-only
// P09: execute the production phone view and passkey workflow with held proofs.
// Native form dispatch and phone action-bar geometry remain browser risks.
import { afterEach, expect, it, vi } from 'vitest'
import { nextTick, reactive, ref } from 'vue'
import * as phone from '../src/lib/phoneApprovals'
import { useIdentityScope } from '../src/lib/useIdentityScope'
import { clientComponent, mountRuntime } from './helpers/runtime-component'

vi.mock('../src/stores/session', () => ({ useSession: () => session }))
vi.mock('../src/lib/authz', () => ({ onAccessRevalidate: () => () => {} }))
const session = reactive({ identity: { tenant: { id: 'tenant' }, principal: { id: 'A', kind: 'person' } }, authenticationCurrent: () => true })
const id = '33333333-3333-4333-8333-333333333333', challenge = '44444444-4444-4444-8444-444444444444'
const route = reactive({ params: { kind: 'approval', requestId: id }, fullPath: `/phone-approvals/approval/${id}` })
const view = clientComponent(new URL('../src/views/PhoneApprovalView.vue', import.meta.url), {
  'vue-router': { useRoute: () => route, onBeforeRouteLeave: () => {} },
  '../lib/authz': { can: () => true, onAccessRevalidate: () => () => {} },
  '../lib/useIdentityScope': { useIdentityScope }, '../stores/session': { useSession: () => session },
  '../lib/brand': { brand: ref({ short_name: 'Aeon' }) }, '../lib/phoneApprovals': phone,
  '../lib/agentState': { scopeLabel: (value: string) => value }, '../components/deploy/TargetSummary.vue': { default: {} }, '../components/AppIcon.vue': { default: {} },
}, false)
const bytes = new Uint8Array([1, 2, 3]).buffer
const credential = { id: 'AQID', rawId: bytes, type: 'public-key', authenticatorAttachment: 'platform', getClientExtensionResults: () => ({}),
  response: { clientDataJSON: bytes, authenticatorData: bytes, signature: bytes, userHandle: null } }
const mounts: ReturnType<typeof mountRuntime>[] = []
afterEach(() => { for (const app of mounts.splice(0)) app.unmount(); vi.useRealTimers(); vi.unstubAllGlobals() })
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done }); return { promise, resolve } }
async function flush() { for (let i = 0; i < 20; i++) await Promise.resolve(); await nextTick() }
interface PhoneState {
  reason: string; decide: (decision: 'approved' | 'denied') => void
  outcome: string; pending: boolean; error: string; review: phone.PhoneReview | null
}
function setup(expiresIn = 3600_000) {
  vi.useFakeTimers(); vi.setSystemTime(new Date('2026-10-01T10:00:00Z'))
  session.identity.principal.id = 'A'
  const current = { kind: 'approval', request_id: id, request_hash: 'a'.repeat(64), pending: true, approval: {
    id, agent_principal_id: 'agent', scope: 'nodes.write', resource_kind: 'tenant', rationale: 'Reviewed change', proposed_at: new Date().toISOString(), expires_at: new Date(Date.now() + expiresIn).toISOString(), decision: null as string | null,
  } }
  const calls: { path: string; body?: Record<string, unknown> }[] = []
  const get = vi.fn(async (_options: CredentialRequestOptions) => credential)
  vi.stubGlobal('navigator', { credentials: { get } })
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    const path = url.replace(/^\/api/, ''), body = init?.body ? JSON.parse(String(init.body)) : undefined
    calls.push({ path, body })
    if (path.endsWith('/options')) return Response.json({ challenge_id: challenge, publicKey: { challenge: 'AQID', allowCredentials: [{ id: 'AQID', type: 'public-key' }] } })
    if (path.endsWith('/decision')) return Response.json({ ...current, pending: false, approval: { ...current.approval, decision: body.decision } })
    return Response.json(current)
  }))
  const app = mountRuntime(view); mounts.push(app)
  return { current, calls, get, state: (app.instance.$ as unknown as { setupState: PhoneState }).setupState }
}

it('requires an explicit choice and a fresh proof before posting the exact reviewed request, hash and reason', async () => {
  const { calls, get, state } = setup(); await flush()
  expect(calls.map(call => call.path)).toEqual([`/phone-approvals/approval/${id}`])
  const proof = deferred<typeof credential>(), entered = deferred<void>()
  get.mockImplementationOnce(async () => { entered.resolve(); return proof.promise })
  state.reason = '  Maintenance window  '; state.decide('approved'); await entered.promise
  expect(calls.map(call => call.path)).toEqual([`/phone-approvals/approval/${id}`, `/phone-approvals/approval/${id}/options`])
  expect(state.outcome).toBe('')
  expect(get.mock.calls[0]![0]).toMatchObject({ publicKey: { userVerification: 'required', challenge: bytes } })
  proof.resolve(credential); await flush()
  expect(calls.at(-1)).toEqual({ path: `/phone-approvals/approval/${id}/decision`, body: {
    decision: 'approved', request_hash: 'a'.repeat(64), reason: 'Maintenance window', challenge_id: challenge,
    credential: { id: 'AQID', rawId: 'AQID', type: 'public-key', authenticatorAttachment: 'platform', clientExtensionResults: {}, response: { clientDataJSON: 'AQID', authenticatorData: 'AQID', signature: 'AQID', userHandle: null } },
  } })
  expect(state.outcome).toBe('Approved. Your decision was recorded.')
  expect(state.pending).toBe(false)
})

it('records nothing on passkey cancellation, then binds an explicit decline to its new verification', async () => {
  const { calls, get, state } = setup(); await flush()
  get.mockRejectedValueOnce(new DOMException('Cancelled', 'NotAllowedError'))
  state.decide('approved'); await flush()
  expect(state.error).toContain('Verification cancelled')
  expect(state.outcome).toBe('')
  expect(calls.filter(call => call.path.endsWith('/decision'))).toEqual([])
  state.decide('denied'); await flush()
  expect(calls.filter(call => call.path.endsWith('/decision')).map(call => call.body?.decision)).toEqual(['denied'])
  expect(state.outcome).toBe('Declined. Your decision was recorded.')
})

it('expires the displayed request with its injected clock and starts no verification or decision afterwards', async () => {
  const { calls, get, state } = setup(1000); await flush()
  expect(state.pending).toBe(true)
  await vi.advanceTimersByTimeAsync(1000)
  expect(state.pending).toBe(false)
  state.decide('approved'); await flush()
  expect(get).not.toHaveBeenCalled()
  expect(calls).toHaveLength(1)
  expect(state.outcome).toBe('')
})

it('aborts a held passkey after a person change and never posts or displays A’s decision for B', async () => {
  const { calls, get, state, current } = setup(); await flush()
  const proof = deferred<typeof credential>(), entered = deferred<void>()
  get.mockImplementationOnce(async () => { entered.resolve(); return proof.promise })
  state.reason = 'Private A reason'; state.decide('approved'); await entered.promise
  const signal = get.mock.calls[0]![0].signal as AbortSignal
  current.request_hash = 'b'.repeat(64); session.identity.principal.id = 'B'; await flush()
  expect(signal.aborted).toBe(true)
  proof.resolve(credential); await flush()
  expect(calls.filter(call => call.path.endsWith('/decision'))).toEqual([])
  expect(state.reason).toBe(''); expect(state.outcome).toBe(''); expect(state.error).toBe('')
  expect(state.review!.request_hash).toBe('b'.repeat(64))
})
