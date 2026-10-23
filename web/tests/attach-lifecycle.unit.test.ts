// SPDX-License-Identifier: AGPL-3.0-only
// P09: the real attach component setup owns async state and exact request
// binding. Native code entry, focus, geometry and live text stay in browsers.
import { afterEach, expect, it, vi } from 'vitest'
import { nextTick, reactive, ref } from 'vue'
import * as attach from '../src/lib/attachWatch'
import { attachCopy } from '../src/lib/attachCopy'
import { useIdentityScope } from '../src/lib/useIdentityScope'
import { clientComponent, mountRuntime } from './helpers/runtime-component'
import type { AttachReview, AttachQueueRow, LocalAuthComputer } from '../src/lib/attachWatch'

vi.mock('../src/stores/session', () => ({ useSession: () => identity }))
vi.mock('../src/lib/authz', () => ({ onAccessRevalidate: () => () => {} }))
const now = Date.parse('2026-10-01T10:00:00Z')
const identity = reactive({ identity: { tenant: { id: 'tenant' }, principal: { id: 'A', kind: 'person' } }, authenticationCurrent: () => true })
const permissions = reactive({ manage: true })
const list = vi.fn(), action = vi.fn(), getNode = vi.fn(), api = vi.fn(), afterWrite = vi.fn(async () => {})
const access: { reset: () => void }[] = []
const imports = {
  '../../lib/api': { getNode, api }, '../../lib/attachWatch': { ...attach, listPendingAttach: list, attachAction: action },
  '../../lib/authz': { can: () => permissions.manage, ensurePermissions: async () => 'known', onAccessRevalidate: (callbacks: { reset: () => void }) => { access.push(callbacks); return () => {} } },
  '../../lib/agentState': { duration: () => '9m' }, '../../lib/capacity': { HARNESS_NAME: { codex: 'Codex' } },
  '../../lib/useIdentityScope': { useIdentityScope }, '../../stores/session': { useSession: () => identity },
  '../../lib/usePolledData': { usePoller: (refresh: () => unknown) => ({ start: () => { void refresh() }, stop: () => {} }) },
  '../../lib/brand': { brand: ref({ short_name: 'Aeon' }) }, '../../lib/attachCopy': { attachCopy },
  '../../lib/attachLink': { formatAttachCode: attach.formatAttachCode, onAttachCode: () => () => {}, takeAttachCode: () => null },
  '../../stores/agents': { useAgents: () => ({ afterWrite }) }, '../AppIcon.vue': { default: {} }, '../KeyCap.vue': { default: {} },
}
const pending = clientComponent(new URL('../src/components/agents/AttachPending.vue', import.meta.url), imports, false)
const approval = clientComponent(new URL('../src/components/agents/AttachApproval.vue', import.meta.url), imports, false)
const security = clientComponent(new URL('../src/components/settings/SessionWatchingSettings.vue', import.meta.url), imports, false)
const mounts: ReturnType<typeof mountRuntime>[] = []
afterEach(() => { for (const app of mounts.splice(0)) app.unmount(); access.length = 0; vi.resetAllMocks(); vi.unstubAllGlobals() })
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done }); return { promise, resolve } }
async function flush() { for (let i = 0; i < 6; i++) await Promise.resolve(); await nextTick() }
function review(id = 'request-A', patch: Partial<AttachReview> = {}): AttachReview {
  return { request_id: id, request_digest: 'a'.repeat(64), consent_digest: 'b'.repeat(64), consent_mode: 'aeon', state: 'pending', expires_at: new Date(now + 540_000).toISOString(),
    snapshot: { platform: 'darwin', computer_id: 'computer', project_id: 'project', ticket_id: 'ticket', host: 'Paired Mac', harness: 'codex', transcript: '/fixture/session.jsonl', file_id: '1:234',
      process: { pid: 4812, uid: 501, started: new Date(now).toISOString(), executable: '/fixture/codex', cwd: '/fixture/project' } }, ...patch }
}
interface AttachState {
  show: (review: AttachReview) => void; submit: () => void; close: () => void; request: (save?: boolean) => Promise<void>
  choice: string; canSubmit: boolean; review: AttachReview | null; error: string
  saved: string | null; canPickLocal: boolean; canSave: boolean; notice: string; savedUnavailable: boolean; blocked: string[]; computers: LocalAuthComputer[]; busy: boolean
}
function mount(component = approval, props: Record<string, unknown> = {}) {
  identity.identity = { tenant: { id: 'tenant' }, principal: { id: 'A', kind: 'person' } }; permissions.manage = true
  vi.stubGlobal('navigator', { platform: 'MacIntel' })
  getNode.mockImplementation(async (id: string) => ({ id, key: id.toUpperCase(), title: `${id} name` }))
  const app = mountRuntime(component, { now, ...props }); mounts.push(app)
  return { ...app, state: (app.instance.$ as unknown as { setupState: AttachState }).setupState }
}

it('discards a pending attach list after a person change even when its transport ignores abort', async () => {
  const held = deferred<AttachReview[]>(), entered = deferred<void>(), rows = vi.fn(), updated = vi.fn()
  let signal!: AbortSignal
  list.mockImplementationOnce((value: AbortSignal) => { signal = value; entered.resolve(); return held.promise }).mockResolvedValue([])
  mount(pending, { onRows: rows, onUpdated: updated }); await entered.promise
  identity.identity.principal.id = 'B'; await nextTick()
  expect(signal.aborted).toBe(true)
  held.resolve([review()]); await flush()
  expect(updated.mock.calls.flat()).not.toContainEqual([review()])
  expect(rows.mock.calls.flat(2).some((row: AttachQueueRow) => row.review?.request_id === 'request-A')).toBe(false)
})

it('waits for labels and an explicit choice, then writes both digests for the displayed request exactly once', async () => {
  const names = deferred<{ key: string; title: string }>(), changed = vi.fn()
  const { state } = mount(approval, { onChanged: changed })
  state.show(review()); await flush()
  state.submit(); expect(action).not.toHaveBeenCalled()
  getNode.mockReturnValue(names.promise)
  state.show(review()); state.submit(); expect(action).not.toHaveBeenCalled()
  state.choice = 'allow'; state.submit(); expect(action).not.toHaveBeenCalled()
  names.resolve({ key: 'AEON-1', title: 'Reviewed target' }); await flush()
  expect(state.canSubmit).toBe(true)
  const held = deferred<AttachReview>()
  action.mockReturnValue(held.promise)
  state.submit(); state.submit()
  expect(action).toHaveBeenCalledTimes(1)
  expect(action.mock.calls[0]!.slice(0, 2)).toEqual(['/request-A/approve', { request_digest: 'a'.repeat(64), consent_digest: 'b'.repeat(64) }])
  const accepted = review('request-A', { state: 'approved' }); held.resolve(accepted); await flush()
  expect(changed).toHaveBeenCalledWith(accepted, false)
  expect(afterWrite).toHaveBeenCalledTimes(1)
  expect(state.review!.state).toBe('approved')
})

it('drops a held approval body on person change, including refresh and parent notifications', async () => {
  const changed = vi.fn(), { state } = mount(approval, { onChanged: changed })
  state.show(review()); await flush(); state.choice = 'allow'
  const held = deferred<AttachReview>(); action.mockReturnValue(held.promise)
  state.submit(); const signal = action.mock.calls[0]![2] as AbortSignal
  identity.identity.principal.id = 'B'; await nextTick()
  expect(signal.aborted).toBe(true)
  held.resolve(review('request-A', { state: 'approved' })); await flush()
  expect(changed).not.toHaveBeenCalled(); expect(afterWrite).not.toHaveBeenCalled()
  expect(state.review).toBeNull()
})

it('refreshes a confirmed exact request after its review closes, while discarding the closed dialog body', async () => {
  const changed = vi.fn(), { state } = mount(approval, { onChanged: changed })
  state.show(review()); await flush(); state.choice = 'allow'
  const held = deferred<AttachReview>(); action.mockReturnValue(held.promise)
  state.submit(); state.close()
  const signal = action.mock.calls[0]![2] as AbortSignal
  expect(signal.aborted).toBe(false)
  const accepted = review('request-A', { state: 'approved' }); held.resolve(accepted); await flush()
  expect(changed).toHaveBeenCalledWith(accepted, false); expect(afterWrite).toHaveBeenCalledTimes(1)
  expect(state.review).toBeNull()
})

it('refuses unsupported local confirmation, revoked authority and failed writes without claiming an approval', async () => {
  const changed = vi.fn(), { state } = mount(approval, { onChanged: changed })
  const unsupported = review(); unsupported.consent_mode = 'local_auth'; unsupported.snapshot.platform = 'linux'
  state.show(unsupported); await flush(); state.choice = 'allow'
  expect(state.canSubmit).toBe(false); state.submit(); expect(action).not.toHaveBeenCalled()
  state.show(review()); await flush(); state.choice = 'allow'
  action.mockRejectedValue(new Error('Approval refused'))
  state.submit(); await flush()
  expect(state.error).toBe('Approval refused'); expect(state.review!.state).toBe('pending')
  expect(changed).not.toHaveBeenCalled(); expect(afterWrite).not.toHaveBeenCalled()
  action.mockClear(); permissions.manage = false; await nextTick(); state.submit()
  expect(action).not.toHaveBeenCalled(); expect(state.review).toBeNull()
})

const localComputer = { computer_id: 'paired-mac', name: 'Paired Mac', capability: 'available', pairing_upgraded: true }
it('keeps the saved consent policy and the stricter draft separate after a refused save', async () => {
  api.mockResolvedValue(Response.json({ consent_mode: 'aeon', consent_saved: true, local_auth_computers: [localComputer] }))
  const { state } = mount(security); await flush()
  expect(state.saved).toBe('aeon'); expect(state.canPickLocal).toBe(true)
  state.choice = 'local_auth'; api.mockResolvedValue(Response.json({ error: 'refused' }, { status: 503 }))
  await state.request(true)
  expect(JSON.parse(api.mock.calls.at(-1)![1].body)).toEqual({ consent_mode: 'local_auth' })
  expect(state.saved).toBe('aeon'); expect(state.choice).toBe('local_auth'); expect(state.canSave).toBe(true)
  expect(state.notice).toBe(''); expect(state.error).toBe('Your setting was not saved. Try again.')
})

it('refuses local consent without a capable upgraded computer and drops a held saved policy on access reset', async () => {
  const unsupported = { ...localComputer, capability: 'no_gui' }
  api.mockResolvedValue(Response.json({ consent_mode: 'local_auth', consent_saved: true, local_auth_computers: [unsupported] }))
  const { state } = mount(security); await flush()
  expect(state.canPickLocal).toBe(false); expect(state.savedUnavailable).toBe(true)
  expect(state.blocked).toEqual(['Paired Mac has no graphical session, so watches there stay off.'])
  const count = api.mock.calls.length
  await state.request(true); expect(api).toHaveBeenCalledTimes(count)
  const held = deferred<Response>(); api.mockReturnValue(held.promise)
  const loading = state.request()
  const signal = api.mock.calls.at(-1)![1].signal as AbortSignal
  access.at(-1)!.reset(); expect(signal.aborted).toBe(true)
  held.resolve(Response.json({ consent_mode: 'local_auth', consent_saved: true, local_auth_computers: [localComputer] })); await loading
  expect(state.saved).toBeNull(); expect(state.choice).toBe('aeon'); expect(state.computers).toEqual([])
  expect(state.notice).toBe(''); expect(state.busy).toBe(false)
})
