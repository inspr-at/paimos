// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { effectScope, reactive, nextTick } from 'vue'
import type { HarnessSession } from '../src/lib/agents'
const mocks = vi.hoisted(() => ({ request: vi.fn(), read: vi.fn(), toast: vi.fn() }))
const identity = reactive({ principal: { id: 'person-1', kind: 'person' } })
vi.mock('../src/stores/session', () => ({ useSession: () => ({ identity }) }))
vi.mock('../src/lib/authz', () => ({ can: () => true }))
vi.mock('../src/lib/agentRows', () => ({ requestAgentRecovery: mocks.request, readAgentRecovery: mocks.read }))
vi.mock('../src/lib/toast', () => ({ toast: mocks.toast }))
import { useAgentRecovery } from '../src/lib/agentRecovery'
import { capabilityWords } from '../src/components/agents/sessionChat'

const session = { id: 'session-1', project_id: 'project-1', archived_at: null, agent_recovery: { session_id: 'session-1', observed_revision: 'a'.repeat(64), cause: 'heartbeat_overdue', detail: 'Heartbeat overdue', action: 'restart' } } as HarnessSession
let scope = effectScope()
const setup = () => scope.run(useAgentRecovery)!
beforeEach(() => {
  scope = effectScope(); identity.principal.id = 'person-1'; vi.clearAllMocks(); vi.useFakeTimers()
  mocks.request.mockImplementation(async (_project, id, body) => ({ id: body.request_id, session_id: id, state: 'pending' }))
  mocks.read.mockImplementation(async (_project, id, request) => ({ id: request, session_id: id, state: 'claimed', outcome: null }))
})
afterEach(() => { scope.stop(); vi.useRealTimers() })

test('accepted recovery stays pending until verified result and never calls queued work restarted', async () => {
  const recovery = setup()
  await recovery.request(session, 'Worker')
  expect(recovery.busy[session.id]).toBe(true)
  expect(mocks.request.mock.calls[0]?.slice(0, 2)).toEqual(['project-1', 'session-1'])
  expect(mocks.request.mock.calls[0]?.[2]).toMatchObject({ expected_revision: 'a'.repeat(64), action: 'restart' })
  expect(mocks.toast.mock.calls.at(-1)?.[0]).toContain('requested. Waiting')
  mocks.read.mockImplementation(async (_project, id, request) => ({ id: request, session_id: id, state: 'completed', outcome: 'continuation_queued' }))
  await vi.advanceTimersByTimeAsync(1000)
  expect(mocks.toast.mock.calls.at(-1)?.[0]).toContain('Continuation queued for normal dispatch')
  expect(recovery.busy[session.id]).toBeUndefined()
})

test('rejected or expired recovery surfaces failure and stops polling', async () => {
  const recovery = setup()
  mocks.read.mockImplementation(async (_project, id, request) => ({ id: request, session_id: id, state: 'expired', outcome: 'unconfirmed' }))
  await recovery.request(session, 'Worker')
  await Promise.resolve()
  expect(mocks.toast.mock.calls.at(-1)?.[0]).toContain('outcome is unconfirmed')
  expect(mocks.toast.mock.calls.at(-1)?.[1]).toMatchObject({ tone: 'error' })
  const reads = mocks.read.mock.calls.length
  await vi.advanceTimersByTimeAsync(120000)
  expect(mocks.read).toHaveBeenCalledTimes(reads)
  expect(recovery.busy[session.id]).toBeUndefined()
})

test('person changes discard a late accepted request and clear owned state', async () => {
  const recovery = setup()
  let finish!: (value: unknown) => void
  mocks.request.mockImplementation((_project, id, body) => new Promise(resolve => { finish = () => resolve({ id: body.request_id, session_id: id, state: 'pending' }) }))
  const pending = recovery.request(session, 'Worker')
  identity.principal.id = 'person-2'; await nextTick(); finish({})
  await pending
  expect(mocks.toast).not.toHaveBeenCalled()
  expect(mocks.read).not.toHaveBeenCalled()
  expect(recovery.busy[session.id]).toBeUndefined()
})

test('disposal and wrong-session responses cannot produce a recovery success', async () => {
  const recovery = setup()
  mocks.read.mockResolvedValue({ id: 'wrong-request', session_id: 'session-2', state: 'completed', outcome: 'reconnected' })
  await recovery.request(session, 'Worker'); await Promise.resolve()
  expect(mocks.toast.mock.calls.at(-1)?.[0]).toContain('did not match this session')
  expect(mocks.toast.mock.calls.at(-1)?.[1]).toMatchObject({ tone: 'error' })
  const reads = mocks.read.mock.calls.length
  scope.stop(); await vi.advanceTimersByTimeAsync(10000)
  expect(mocks.read).toHaveBeenCalledTimes(reads)
})


test('switching the visible session discards a late recovery result', async () => {
  const visible = reactive({ id: session.id })
  const recovery = scope.run(() => useAgentRecovery(() => visible.id))!
  let finish!: () => void
  mocks.request.mockImplementation((_project, id, body) => new Promise(resolve => { finish = () => resolve({ id: body.request_id, session_id: id, state: 'pending' }) }))
  const pending = recovery.request(session, 'Worker')
  visible.id = 'session-2'; await nextTick(); finish()
  await pending
  expect(mocks.read).not.toHaveBeenCalled()
  expect(mocks.toast).not.toHaveBeenCalled()
  expect(recovery.busy[session.id]).toBeUndefined()
})

// Render the real header and Session evidence for both ownership modes. Browser
// geometry coverage lives in agent-recovery.spec.ts; this gate can also run
// while another worker owns the browser lane.
test('changing diagnosis remains below every session control and tab', async () => {
  const { readFileSync } = await import('node:fs')
  const { parse } = await import('@vue/compiler-sfc')
  const { compile, baseParse } = await import('@vue/compiler-dom')
  const Vue = await import('vue')
  const { renderToString } = await import('@vue/server-renderer')
  const { descriptor } = parse(readFileSync(new URL('../src/components/agents/SessionPanel.vue', import.meta.url), 'utf8'))
  const template = descriptor.template!.content
  // Select whole elements, including their conditional wrappers. Slicing from
  // ManagedSessionControls to Facts crossed two unmatched template/div tags.
  const tree = baseParse(template)
  const elements = (node: typeof tree | (typeof tree.children)[number]): string[] => {
    if (node.type === 1 && (node.tag === 'header' || node.tag === 'template' && node.loc.source.includes('<ManagedSessionControls') || node.tag === 'section' && node.loc.source.includes('class="recovery-evidence"'))) return [node.loc.source]
    return 'children' in node ? node.children.flatMap(elements) : []
  }
  const fixture = `<div>${elements(tree).join('')}</div>`
  const ts = await import('typescript')
  const code = ts.transpileModule(compile(fixture, { mode: 'function', prefixIdentifiers: true, expressionPlugins: ['typescript'] }).code, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText
  const render = new Function('Vue', code)(Vue)
  const { default: SessionTabs } = await import('../src/components/agents/SessionTabs.vue')
  const { PANEL_TAB_LABEL } = await import('../src/lib/leadPanel')
  for (const management_mode of ['managed', 'unmanaged']) for (const detail of ['Reporting is current.', 'The paired computer still reports, but this exact session heartbeat and inbox binding remain overdue. '.repeat(5)]) {
    const app = Vue.createSSRApp({ render, setup: () => ({
      view: { name: 'Worker', harness: 'Claude', status: { state: 'working', label: 'Working' }, session: { ...session, management_mode, advertised_capabilities: ['managed_control_v1'], agent_recovery: { ...session.agent_recovery, detail } } },
      loading: false, reported: undefined, brand: { short_name: 'Aeon' },
      agentRecovery: { action: () => 'restart', busy: {} }, pausingSession: () => false, works: () => false,
      actionsAnchor: {}, showRecover: false, showRemove: false, quick: true, tab: 'overview', unread: 0, now: 0,
      ticketState: '', selectTab: () => {}, serviceTiers: { unavailable: () => true },
      capability: 'native', capText: capabilityWords.en.native,
      isLead: false, pinned: null, hasQuestion: false, managed: null, s: session,
      interruptReason: '', pauseReason: '', stopReason: '', resumeOffered: false,
      modelLine: '', leads: { busy: {} }, interrupt: () => {}, pauseOrResume: () => {}, stopSession: () => {}, pinTab: () => {}, closeActions: () => {},
      shortcutKeys: () => [], identityTitle: 'Worker · Working', menuReady: true, leadAtOpen: false, terminalAtOpen: true,
      sidebarAvailable: true, panelLayout: 'side', fullScreenTip: 'Full screen', openedTab: 'overview', why: '',
      nativeKnown: false, managedControls: management_mode === 'managed', viewerAtOpen: false,
      availableTabs: ['overview', 'chat', 'events', 'terminal'], PANEL_TAB_LABEL,
      settingReason: () => '', nativeAction: null, nativeReason: '', phoneHead: false, requestAnchor: null,
      menuSeparator: () => false, steer: () => {}, editSetting: () => {}, requestLayout: () => {},
      openRequest: () => {}, pickTier: () => {}, pinChoice: null, pinNote: '', requestsOffered: false,
      viewer: { kind: 'agent' }, terminalLink: null, questions: [], tierNote: '',
    }) })
    app.component('SessionTabs', SessionTabs)
    app.component('ManagedSessionControls', { render: () => Vue.h('span', { 'data-control': 'ManagedSessionControls' }) })
    app.component('FloatingPanel', { setup: (_, { slots }) => () => Vue.h('div', slots.default?.()) })
    for (const name of ['AgentGlyph', 'AppIcon', 'KeyCap', 'SessionRecovery', 'RemoveSessionDialog']) app.component(name, { render: () => Vue.h('span') })
    const html = await renderToString(app)
    const feedback = html.indexOf(detail)
    const chip = html.indexOf(capabilityWords.en.native[0])
    expect(feedback).toBeGreaterThan(0)
    expect(chip).toBeGreaterThan(0)
    expect(chip).toBeLessThan(feedback)
    for (const selector of ['aria-label="Panel actions"', 'aria-label="More session actions"', 'aria-label="Close session details"', 'data-control="ManagedSessionControls"', 'role="tablist"', 'role="menuitem"']) {
      const control = html.indexOf(selector)
      expect(control).toBeGreaterThan(0)
      expect(control).toBeLessThan(feedback)
    }
    for (const label of ['Details', 'Chat', 'Events', 'Terminal']) expect(html).toMatch(new RegExp(`role="tab"[^>]*aria-label="${label}"`))
    for (const label of ['Interrupt', 'Pause', 'Stop…', 'Restart']) expect(html.slice(0, feedback)).toContain(`<span>${label}`)
    if (management_mode === 'managed') for (const label of ['Name…', 'Model…', 'Effort…']) expect(html.slice(0, feedback)).toContain(`<span>${label}`)
  }
})
