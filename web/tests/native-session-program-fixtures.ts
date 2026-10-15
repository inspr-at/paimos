// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1309 risk: panel wiring changes a writer's authority, retargets a late
// context read, moves controls, or moves the reader when live details grow.
import { expect, type Locator, type Page } from '@playwright/test'
import { mockLeadFlow, LEAD_SESSION } from './lead-flow-fixtures'
import { me } from './work-fixtures'
import type { NativeBinding, NativeSessionView, NativeStreamEvent } from '../src/lib/nativeSessionContract'

export const WORKER = 'aaaaaaaa-0000-4000-8000-000000000002'
export const binding: NativeBinding = { tenant_id: 't1', project_id: 'p-pharos', owned_session_id: 'owned-worker', harness_session_id: WORKER,
  runtime_epoch: 'runtime-1', daemon_id: 'fixture-host', daemon_generation: 'daemon-1', binding_revision: 1, run_id: null }
export const makeNative = (): NativeSessionView => ({ owned_session_id: binding.owned_session_id, current_binding: { ...binding }, owner_person_id: me.id,
  role: 'worker', profile: 'native', harness: 'codex', state: 'running', mode: 'structured', revision: 1, generation: 1,
  capabilities: { input: true, steer: 'native', interrupt: true, stop: true, resume: true, take: true, expert: false, terminal_view: false,
    approvals: false, questions: false, correlated_receipts: true, quota: 'stream', qualification_digest: 'a'.repeat(64),
    take_supported: true, events_supported: true, interrupt_supported: true, chat_supported: true },
  conversation_ref: 'opaque-fixture', predecessor_harness_session_id: null, controller: null, quota: null, last_receipt: null, reason: null,
  terminal_viewer_url: 'https://terminal.example.test/worker' })

export async function setup(page: Page, theme = 'light') {
  const world = await mockLeadFlow(page, { lead: 'working', question: false, longText: true })
  world.data.preferences['theme'] = { choice: theme }
  const worker = world.data.live.find(row => row.session_id === WORKER)!
  worker.model = 'gpt-6.1-sol'; worker.reasoning_effort = 'xhigh'
  worker.ticket!.title = 'Verbindungswiederherstellung mit revisionsgebundener Sitzungssteuerung und überprüfter Prozessidentität'
  const state = { native: makeNative(), unavailable: false, calls: [] as { path: string; method: string; body: unknown }[], note: 'Working on the assigned change.' }
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

export async function refresh(page: Page) {
  await page.evaluate(async () => { const { useAgents } = await import('/src/stores/agents.ts'); await useAgents().loadAll() })
}

// The long title yields first: checking textContent alone would pass even
// when the key is painted as an ellipsis or clipped by its heading.
export async function expectPanelIdentity(panel: Locator, key: string, title: string) {
  const heading = panel.locator('.pane-title'), name = heading.locator('.name')
  await expect(name).toHaveText(key)
  await expect(heading.locator('.title-separator')).toHaveJSProperty('textContent', ' · ')
  await expect(heading.locator('.title-detail')).toHaveText(title)
  expect(await heading.evaluate(el => el.textContent?.startsWith(`${el.querySelector('.name')!.textContent} · `))).toBe(true)
  await expect.poll(() => name.evaluate(el => {
    const range = document.createRange(); range.selectNodeContents(el)
    const text = range.getBoundingClientRect(), box = el.getBoundingClientRect()
    const heading = el.closest('.pane-title')!.getBoundingClientRect()
    const identity = el.closest('.pane-identity')!.getBoundingClientRect()
    return text.width > 0 && text.left >= Math.max(box.left, heading.left, identity.left) - .5 &&
      text.right <= Math.min(box.right, heading.right, identity.right) + .5 && el.scrollWidth <= el.clientWidth
  }), 'the full key must be painted inside the heading').toBe(true)
}

export async function expectNativeMenu(page: Page) {
  const menu = page.getByRole('menu', { name: 'More session actions' })
  const browserReason = 'Not from the browser yet: use aeon on the computer that runs it'
  for (const name of [/^Interrupt/, /^Stop…/, /^Take over/]) {
    const action = menu.getByRole('menuitem', { name })
    await expect(action).toBeDisabled()
    await expect(action.locator('small')).toHaveText(browserReason)
  }
  const pause = menu.getByRole('menuitem', { name: /^Pause/ })
  await expect(pause).toBeDisabled()
  await expect(pause.locator('small')).toHaveText("Pause and resume aren't available here yet")
  const disabled = menu.locator('.panel-menu-item:disabled')
  expect(await disabled.count()).toBeGreaterThanOrEqual(4)
  for (const action of await disabled.all()) {
    const reason = action.locator('small')
    const label = await action.innerText()
    await expect(reason).toHaveText(label.startsWith('Pause') ? "Pause and resume aren't available here yet" : browserReason)
    expect((await reason.innerText()).split('\n')).toHaveLength(1)
    expect(await reason.evaluate(el => {
      const range = document.createRange(); range.selectNodeContents(el)
      return new Set([...range.getClientRects()].map(rect => rect.top)).size
    }), 'each disabled reason paints on one line').toBe(1)
    expect(await reason.evaluate(el => {
      const range = document.createRange(); range.selectNodeContents(el)
      const text = range.getBoundingClientRect(), menu = el.closest('.panel-menu')!.getBoundingClientRect()
      return Math.max(menu.left - text.left, text.right - menu.right)
    }), 'the whole reason fits inside the menu').toBeLessThanOrEqual(.5)
  }
}
// Layout saves use a queued preference writer. Await each manual save's real
// response before observing whether the later automatic layout sends a write.
export async function savedLayout(page: Page, layout: string, action: () => Promise<unknown>) {
  const response = page.waitForResponse(response => response.ok() && response.request().method() === 'PUT' &&
    decodeURIComponent(new URL(response.url()).pathname).startsWith('/api/preferences/agents:panel.layout.') && response.request().postDataJSON()?.value?.layout === layout)
  await action()
  await response
}
