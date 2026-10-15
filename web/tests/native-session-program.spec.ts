// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1309 risk: panel wiring changes a writer's authority, retargets a late
// context read, moves controls, or moves the reader when live details grow.
import { expect, test, type Page } from '@playwright/test'
import { controlStability } from './control-stability'
import { mockLeadFlow, LEAD_SESSION } from './lead-flow-fixtures'
import { me } from './work-fixtures'
import type { NativeBinding, NativeSessionView, NativeStreamEvent } from '../src/lib/nativeSessionContract'

const WORKER = 'aaaaaaaa-0000-4000-8000-000000000002'
const binding: NativeBinding = { tenant_id: 't1', project_id: 'p-pharos', owned_session_id: 'owned-worker', harness_session_id: WORKER,
  runtime_epoch: 'runtime-1', daemon_id: 'fixture-host', daemon_generation: 'daemon-1', binding_revision: 1, run_id: null }
const makeNative = (): NativeSessionView => ({ owned_session_id: binding.owned_session_id, current_binding: { ...binding }, owner_person_id: me.id,
  role: 'worker', profile: 'native', harness: 'codex', state: 'running', mode: 'structured', revision: 1, generation: 1,
  capabilities: { input: true, steer: 'native', interrupt: true, stop: true, resume: true, take: true, expert: false, terminal_view: false,
    approvals: false, questions: false, correlated_receipts: true, quota: 'stream', qualification_digest: 'a'.repeat(64),
    take_supported: true, events_supported: true, interrupt_supported: true, chat_supported: true },
  conversation_ref: 'opaque-fixture', predecessor_harness_session_id: null, controller: null, quota: null, last_receipt: null, reason: null,
  terminal_viewer_url: 'https://terminal.example.test/worker' })

async function setup(page: Page, theme = 'light') {
  const world = await mockLeadFlow(page, { lead: 'working', question: false, longText: true })
  world.data.preferences['theme'] = { choice: theme }
  const worker = world.data.live.find(row => row.session_id === WORKER)!
  worker.model = 'gpt-6.1-sol'; worker.reasoning_effort = 'xhigh'
  worker.ticket!.title = 'Verbindungswiederherstellung mit revisionsgebundener Sitzungssteuerung und überprüfter Prozessidentität'
  const state = { native: makeNative(), unavailable: false, calls: [] as { path: string; method: string; body: unknown }[], note: 'Working on the assigned change.' }
  await page.addInitScript(() => Object.assign(window, { panelAnchorDebug: [] }))
  await page.route('**/src/lib/readerAnchor.ts', async route => {
    const response = await route.fetch(), original = await response.text()
    const instrumented = original.replace('restore() {', 'restore() { if (capturedScroller?.id === "session-panel-overview") window.panelAnchorDebug.push({ kind: "restore", marks: marks.map(m => ({ tag: m.el.tagName, cls: m.el.className, top: m.top, current: m.el.getBoundingClientRect().top })), scroll: capturedScroller.scrollTop });')
    await route.fulfill({ response, body: instrumented })
  })
  const errors: string[] = []
  page.on('pageerror', error => errors.push(error.message))
  await page.addInitScript(value => localStorage.setItem('aeon:theme', value), theme)
  await page.route('**/api/projects/*/harness-sessions/*/native-context', route => {
    if (!route.request().url().includes(WORKER) || state.unavailable) return route.fulfill({ status: 503, json: { reason: 'feature_disabled' } })
    return route.fulfill({ json: state.native })
  })
  // List and detail are the same complete row. A newer list must preserve
  // the history/brief asserted by the reader test, rather than deleting them.
  const sessionRow = (agent: typeof worker) => ({
    id: agent.session_id, project_id: agent.project_id, agent_principal_id: agent.principal_id,
    parent_harness_session_id: agent.role === 'worker' ? LEAD_SESSION : null, work_order_id: null, run_id: null,
    ticket_node_id: agent.ticket?.id ?? null, ticket: agent.ticket ?? null, project: { id: agent.project_id, key: 'PHAROS', title: 'Pharos' },
    harness: agent.harness, host: 'fixture-host', management_mode: agent.management_mode, role: agent.role,
    model: agent.model, reasoning_effort: agent.reasoning_effort, advertised_capabilities: ['inbox', 'status'],
    phase: agent.phase, activity: agent.activity, heartbeat_at: agent.heartbeat_at, created_at: agent.since,
    stopped_at: null, display_label: agent.name, activity_sequence: state.native.revision, revision: state.native.revision, work_shape: 'ship',
    ...(agent.session_id === WORKER ? {
      activity_note: state.note, agent_activity_mode: 'agent_summary', current_activity: { text: state.note, source: 'agent', at: '2026-10-06T10:00:00Z' },
      activity_history: Array.from({ length: 6 }, (_, index) => ({ at: `2026-10-06T09:${50 + index}:00Z`, note: `Step ${index + 1}: checking the assigned session and preserving its current owner and permission boundary.` })),
      brief: 'Validate the session panel, keep the controls stable and preserve the reader’s position. '.repeat(15),
    } : {}),
  })
  await page.route('**/api/harness-sessions?*', route => route.fulfill({ json: { items: world.data.live.map(sessionRow), next_cursor: null } }))
  await page.route(`**/api/projects/p-pharos/harness-sessions/${WORKER}`, route => route.fulfill({ json: sessionRow(worker) }))
  await page.route('**/api/projects/p-pharos/native-sessions/**', async route => {
    const request = route.request(), url = new URL(request.url())
    state.calls.push({ path: url.pathname, method: request.method(), body: request.postData() ? request.postDataJSON() : null })
    if (url.pathname.endsWith('/events')) {
      expect(url.searchParams.get('generation')).toBe(String(state.native.generation))
      const event: NativeStreamEvent = { binding: { ...state.native.current_binding! }, generation: state.native.generation, seq: '1', sequence: '1',
        observed_at: '2026-10-06T10:00:00Z', dropped_frames: '0', source: 'native', kind: 'structured', payload: { kind: 'input_evidence', text: null, reference: 'input-fixture', state: 'write_acknowledged' } }
      return route.fulfill({ json: { generation: state.native.generation, items: Array.from({ length: 22 }, (_, index) => ({ ...event, seq: String(22 - index), sequence: String(22 - index) })), next_before: null, truncated: false } })
    }
    // The UI may request a read-only viewer, but this fixture leaves the I2-core
    // stream unavailable. It must report that limitation and never acquire control.
    return route.fulfill({ status: 503, json: { reason: 'feature_disabled' } })
  })
  return { world, worker, state, errors }
}

async function refresh(page: Page) {
  await page.evaluate(async () => { const { useAgents } = await import('/src/stores/agents.ts'); await useAgents().loadAll() })
}

test('AEON-1309: fake-host mode changes stay read-only, preserve the menu and reject stale context', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const { state, worker, errors } = await setup(page)
  await page.goto(`/agents/${WORKER}`)
  const panel = page.getByRole('complementary', { name: 'Session details' })
  await expect(panel.getByRole('tab', { name: 'Details', exact: true })).toHaveAttribute('aria-selected', 'true')
  await expect(panel.getByRole('tab')).toHaveCount(4)
  await panel.getByRole('tab', { name: 'Events', exact: true }).click()
  await expect(panel.getByRole('button', { name: 'Load earlier' })).toHaveCount(0)
  await panel.getByRole('button', { name: 'Watch live' }).click()
  await expect(panel.getByText('Live events are not available for this session yet.')).toBeVisible()
  const attached = state.calls.find(call => call.path.endsWith('/attach'))!
  expect(attached.body).toMatchObject({ mode: 'view', view: 'structured', consent_id: null, expected_binding: binding })
  expect(state.calls.filter(call => /\/(take|return|input|stop|resize)$/.test(call.path))).toEqual([])

  // A qualified fake host makes the actual runtime transition. The UI cannot
  // pretend it performed a handover for which it has no controller/consent.
  state.native = { ...state.native, state: 'taken', mode: 'terminal', revision: 2, generation: 2,
    current_binding: { ...binding, runtime_epoch: 'runtime-2', binding_revision: 2 }, terminal_viewer_url: null,
    controller: { controller_id: 'controller-2', actor_id: me.id, client_id: 'local-client', epoch: 'lease-2', expires_at: '2027-01-01T00:00:00Z' } }
  worker.heartbeat_at = '2026-10-06T10:00:01Z'
  await refresh(page)
  await panel.getByRole('tab', { name: 'Terminal', exact: true }).click()
  await expect(panel.getByRole('button', { name: 'Hand back', exact: true })).toBeDisabled()
  await expect(panel.getByText('Terminal output needs current watch permission and the owner’s active consent.')).toBeVisible()
  await expect(panel.getByRole('tab')).toHaveCount(4)

  state.native = { ...state.native, state: 'recovering', mode: 'none', controller: null, revision: 3, generation: 3,
    current_binding: { ...binding, runtime_epoch: 'runtime-3', binding_revision: 3 }, reason: 'after_exit_unconfirmed' }
  worker.heartbeat_at = '2026-10-06T10:00:02Z'
  await refresh(page)
  await expect(panel.getByRole('button', { name: 'Take over', exact: true })).toBeDisabled()
  await expect(panel.getByRole('tab', { name: 'Terminal', exact: true })).toHaveAttribute('aria-selected', 'true')

  let arrived!: () => void, release!: () => void
  const started = new Promise<void>(resolve => { arrived = resolve }), barrier = new Promise<void>(resolve => { release = resolve })
  await page.route(`**/api/projects/p-pharos/harness-sessions/${WORKER}/native-context`, async route => {
    const old = structuredClone(state.native); arrived(); await barrier
    await route.fulfill({ json: old }).catch(() => {}) // Navigation deliberately aborts this read.
  })
  worker.heartbeat_at = '2026-10-06T10:00:03Z'
  await refresh(page); await started
  await page.evaluate(async id => { const { router } = await import('/src/router.ts'); await router.push(`/agents/${id}`) }, LEAD_SESSION)
  await expect(panel.getByRole('tab', { name: 'Lead', exact: true })).toBeVisible()
  release()
  await expect(panel.getByRole('tab', { name: 'Terminal', exact: true })).toHaveCount(0)
  expect(state.calls.filter(call => /\/(take|return|input|stop|resize)$/.test(call.path))).toEqual([])
  // The same panel is also mounted by project overlays. Its shortcut owner
  // must take precedence without registering the actions twice.
  await page.evaluate(async () => { const { openLeadPanel } = await import('/src/lib/leadOverlay.ts'); openLeadPanel('p-pharos') })
  const overlay = page.locator('.project-lead-panel')
  await expect(overlay.getByRole('tab', { name: 'Lead', exact: true })).toBeVisible()
  await page.keyboard.press('3')
  await expect(overlay.getByRole('tab', { name: 'Chat', exact: true })).toHaveAttribute('aria-selected', 'true')
  await overlay.getByRole('button', { name: 'Full screen', exact: true }).click()
  await expect(overlay).toHaveAttribute('data-layout', 'full')
  await overlay.evaluate(el => { const holder = document.createElement('div'); holder.dataset.nativeTerminalHolder = 'true'; holder.tabIndex = 0; el.append(holder); holder.focus() })
  await page.keyboard.press('Escape')
  await expect(overlay).toHaveAttribute('data-layout', 'full')
  await overlay.getByRole('button', { name: 'Close session details' }).focus()
  await page.keyboard.press('Escape')
  await expect(overlay).toHaveAttribute('data-layout', 'side')
  await page.keyboard.press('Escape')
  await expect(overlay).toHaveCount(0)
  expect(errors).toEqual([])
})

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark']) {
  test(`AEON-1309: ${width} ${theme} panel controls, layouts and live-reader anchor stay stable`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: 900 })
    const { state, worker, errors } = await setup(page, theme)
    await page.goto(`/agents/${WORKER}`)
    const panel = page.getByRole('complementary', { name: 'Session details' })
    await expect(panel.getByRole('tab', { name: 'Details', exact: true })).toHaveAttribute('aria-selected', 'true')
    const tabs = Object.fromEntries(['Details', 'Chat', 'Events', 'Terminal'].map(name => [name, panel.getByRole('tab', { name, exact: true })]))
    const stable = await controlStability(page, { ...tabs, menu: panel.getByRole('tablist'), more: panel.getByRole('button', { name: 'More session actions' }), close: panel.getByRole('button', { name: 'Close session details' }), frame: panel })
    for (const name of ['Chat', 'Events', 'Terminal', 'Details']) await stable.check(async () => { await tabs[name]!.click(); await expect(tabs[name]!).toHaveAttribute('aria-selected', 'true') })
    stable.done()
    const headHeight = await panel.locator('.panel-head').evaluate(el => el.getBoundingClientRect().height)
    expect(headHeight).toBeLessThanOrEqual(width <= 720 ? 93 : 45)
    if (width <= 720) {
      for (const name of ['More session actions', 'Close session details']) {
        const box = await panel.getByRole('button', { name }).boundingBox(); expect(box!.width).toBeGreaterThanOrEqual(44); expect(box!.height).toBeGreaterThanOrEqual(44)
      }
    }
    await page.screenshot({ path: info.outputPath(`AEON-1309-${width}-${theme}-details.png`) })

    // WebKit-style anchoring: disable the browser's own correction, scroll the
    // facts into view, then grow a real activity line above the reader.
    await panel.locator('#session-panel-overview').evaluate(el => { (el as HTMLElement).style.overflowAnchor = 'none' })
    const facts = panel.locator('.block.first')
    await facts.evaluate(el => { const scroller = el.closest<HTMLElement>('#session-panel-overview')!; scroller.scrollTop += el.getBoundingClientRect().top - scroller.getBoundingClientRect().top })
    const before = await facts.evaluate(el => el.getBoundingClientRect().top)
    state.note = 'Neue Erkenntnisse zur Sitzungssteuerung und zur Wiederherstellung sichern die aktuelle Prozessidentität.'
    worker.heartbeat_at = '2026-10-06T10:00:04Z'; state.native.revision++
    await refresh(page)
    await expect(panel.locator('.now-step')).toContainText('Neue Erkenntnisse')
    const after = await facts.evaluate(el => el.getBoundingClientRect().top)
    expect(Math.abs(after - before), JSON.stringify({ before, after, anchors: await page.evaluate(() => (window as unknown as { panelAnchorDebug: unknown[] }).panelAnchorDebug) })).toBeLessThanOrEqual(.5)

    await panel.getByRole('tab', { name: 'Terminal', exact: true }).click()
    const link = panel.getByRole('link', { name: 'Open live terminal' })
    await expect(link).toHaveAttribute('target', '_blank'); await expect(link).toHaveAttribute('rel', 'noopener noreferrer')
    await page.screenshot({ path: info.outputPath(`AEON-1309-${width}-${theme}-terminal.png`) })
    await panel.getByRole('button', { name: 'More session actions' }).click()
    await page.getByRole('menuitem', { name: /^Full screen/ }).click()
    await expect(panel).toHaveAttribute('data-layout', 'full')
    await page.screenshot({ path: info.outputPath(`AEON-1309-${width}-${theme}-full.png`) })
    if (width <= 720) {
      await expect(panel.getByRole('tablist')).toBeHidden()
      await panel.getByRole('button', { name: 'More session actions' }).click()
      await page.getByRole('menuitemradio', { name: 'Events', exact: true }).click()
      await expect(panel.locator('#session-panel-events')).toBeVisible()
    }
    // A holder terminal owns Esc, number keys, F and full-screen shortcuts.
    await panel.evaluate(el => { const terminal = document.createElement('div'); terminal.dataset.nativeTerminalHolder = 'true'; terminal.tabIndex = 0; terminal.id = 'holder-key-fixture'; el.append(terminal); terminal.focus() })
    await page.keyboard.press('Escape'); await page.keyboard.press('1'); await page.keyboard.press('Shift+E'); await page.keyboard.press('f')
    await expect(panel).toHaveAttribute('data-layout', 'full')
    await panel.getByRole('button', { name: 'Close session details' }).focus()
    await page.keyboard.press('Escape')
    await expect(panel).toHaveAttribute('data-layout', 'side')
    await page.keyboard.press('2')
    await expect(panel.getByRole('tab', { name: 'Chat', exact: true })).toHaveAttribute('aria-selected', 'true')
    expect(errors).toEqual([])
  })
}

test('AEON-1309: negative control reproduces reader drift with the anchor disabled', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 900 })
  const { state, worker } = await setup(page)
  // Disable only this fix in the browser's test module. No throwaway source
  // commit, production fixture, other worktree or model process is involved.
  await page.route('**/src/lib/readerAnchor.ts', async route => {
    const response = await route.fetch(), original = await response.text()
    const disabled = original.replace(/onBeforeUpdate\(anchor.capture\);?/, '').replace(/onUpdated\(anchor.restore\);?/, '')
    expect(disabled).not.toBe(original)
    await route.fulfill({ response, body: disabled })
  })
  await page.goto(`/agents/${WORKER}`)
  const panel = page.getByRole('complementary', { name: 'Session details' })
  await expect(panel.getByRole('tab', { name: 'Details', exact: true })).toHaveAttribute('aria-selected', 'true')
  await panel.locator('#session-panel-overview').evaluate(el => { (el as HTMLElement).style.overflowAnchor = 'none' })
  const facts = panel.locator('.block.first')
  await facts.evaluate(el => { const scroller = el.closest<HTMLElement>('#session-panel-overview')!; scroller.scrollTop += el.getBoundingClientRect().top - scroller.getBoundingClientRect().top })
  const before = await facts.evaluate(el => el.getBoundingClientRect().top)
  state.note = 'Neue Erkenntnisse zur Sitzungssteuerung und zur Wiederherstellung sichern die aktuelle Prozessidentität.'
  worker.heartbeat_at = '2026-10-06T10:00:04Z'; state.native.revision++
  await refresh(page)
  await expect(panel.locator('.now-step')).toContainText('Neue Erkenntnisse')
  const after = await facts.evaluate(el => el.getBoundingClientRect().top)
  expect(Math.abs(after - before), 'the positive control must fail without reader anchoring').toBeGreaterThan(.5)
})
