// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1071: streaming replies and capability-driven sends in the session chat.
// Risks: a streaming reply or a starting turn moves the composer's controls;
// Send now appears where the session cannot steer natively; Stop is missing or
// dead mid-stream; live text outlives the saved reply or is stored at all.
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { controlStability } from './control-stability'

const now = Date.parse('2026-09-29T06:00:00Z')
const conversation = '7c000000-0000-4000-8000-000000001071'
const panelOf = (page: Page) => page.getByRole('complementary', { name: 'Session details' })

async function setup(page: Page, options: { theme?: 'light' | 'dark'; session?: Record<string, unknown>; bound?: boolean } = {}) {
  // A scripted EventSource: the agents feed opens quietly, and the chat-live
  // stream takes frames from the test.
  await page.addInitScript(() => {
    class Stream extends EventTarget {
      onopen: ((event: Event) => void) | null = null
      onerror: ((event: Event) => void) | null = null
      readyState = 1
      url: string
      constructor(url: string) {
        super()
        this.url = url
        queueMicrotask(() => this.onopen?.(new Event('open')))
        if (url.includes('/live')) {
          const live = (window as unknown as { __live?: Stream[] }).__live ??= []
          live.push(this)
          queueMicrotask(() => this.dispatchEvent(new MessageEvent('ready', { data: '{"contract":"chat-live-v1"}' })))
        }
      }
      close() { this.readyState = 2 }
    }
    Object.assign(window, { EventSource: Stream })
  })
  await page.clock.install({ time: now })
  const work = fixtures(); work.preferences.theme = { choice: options.theme ?? 'light' }
  await mockWork(page, work, { admin: true })
  const data = agentData({ now, me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' }, nodes: {} })
  const worker = data.sessions[0]!
  Object.assign(worker, { display_label: 'release-lead', run_id: null, activity: 'idle', harness: 'claude', management_mode: 'managed', advertised_capabilities: ['inbox', 'status', 'steer', 'interrupt', 'stop'] }, options.session ?? {})
  data.sessions.splice(1); data.runs.splice(0); data.approvals.splice(0)
  const template = data.messages[1]!
  const messages = ['Picked up the release checks.', 'Good. Keep the fix small.', 'Counts are in.'].map((body, i) => ({
    ...template, id: `3e000000-0000-4000-8000-${String(100 + i).padStart(12, '0')}`, sent_event_id: 200 + i, body, created_at: new Date(now - (3 - i) * 90_000).toISOString(),
    ...(i === 1
      ? { sender_principal_id: me.id, recipient_principal_id: worker.agent_principal_id, to: 'claude:camy', recipient_session_id: worker.id, sender_session_id: undefined, sender_label: 'Markus' }
      : { sender_principal_id: worker.agent_principal_id, recipient_principal_id: me.id, to: 'paimos:markus', sender_session_id: worker.id, sender_label: 'release-lead' }),
  }) as typeof template)
  data.messages.splice(0, data.messages.length, ...messages)
  const calls = await mockAgents(page, data)
  const lookups: string[] = []
  // The bound chat thread's saved messages: chat-native finals live only here,
  // never in the project message list.
  const chat: { message: Record<string, unknown> }[] = []
  await page.route(/\/api\/chat-threads\/[^/]+\/messages(\?|$)/, route => route.fulfill({ json: { contract: 'chat-v1', items: chat, has_more_before: false, has_more_after: false, before_cursor: null, after_cursor: null, snapshot_cursor: 'c', migration_epoch: '0' } }))
  await page.route('**/api/chat-sessions/*/thread', route => {
    lookups.push(new URL(route.request().url()).pathname)
    return options.bound === false
      ? route.fulfill({ status: 404, json: { error: 'chat binding unavailable' } })
      : route.fulfill({ json: { contract: 'chat-v1', conversation_id: conversation, role: { contract: 'chat-v1' }, revision: '2', binding_epoch: '1', readiness: { state: 'offline', capabilities: [], reason: 'session_offline' } } })
  })
  return { worker, data, calls, lookups, chat }
}

const emit = (page: Page, type: string, payload: unknown) => page.evaluate(([kind, data]) => {
  const live = (window as unknown as { __live?: EventTarget[] }).__live ?? []
  live.at(-1)?.dispatchEvent(new MessageEvent(kind, { data }))
}, [type, JSON.stringify(payload)] as const)
const update = (page: Page, sequence: number, body: Record<string, unknown>) => emit(page, 'chat', { type: 'update', update: body, dropped_events: 0, source_sequence: sequence })
const chunk = (page: Page, sequence: number, text: string) => update(page, sequence, { sessionUpdate: 'agent_message_chunk', content: { type: 'text', text } })

for (const width of [390, 1440]) for (const theme of ['light', 'dark'] as const) {
  test(`AEON-1071 streams in place, stops mid-stream and yields to the saved reply ${theme} ${width}`, async ({ page }, testInfo) => {
    const { worker, data, calls, lookups, chat } = await setup(page, { theme })
    await page.setViewportSize({ width, height: 860 })
    await page.goto(`/agents/${worker.id}?tab=messages`)
    const panel = panelOf(page)
    const composer = panel.getByRole('textbox', { name: 'Message to release-lead' })
    const send = panel.locator('.compose .send'), steer = panel.locator('.steer-send'), stop = panel.locator('.chat-stop')
    await expect(composer).toBeVisible()
    await expect.poll(() => lookups.length).toBe(1)
    expect(lookups[0]).toBe(`/api/chat-sessions/${worker.id}/thread`)
    await expect.poll(() => page.evaluate(() => (window as unknown as { __live?: unknown[] }).__live?.length ?? 0)).toBe(1)
    // Idle: every send action is already in place, the turn-bound ones disabled.
    await expect(send).toContainText('Send')
    await expect(steer).toBeDisabled()
    await expect(stop).toBeDisabled()
    await composer.fill('Bitte die vollständige Freigabeprüfung im Bericht berücksichtigen.')
    await composer.blur()
    const guard = await controlStability(page, { composer, send, steer, stop })

    await guard.check(async () => {
      await update(page, 1, { sessionUpdate: 'state', state: 'running' })
      await update(page, 2, { sessionUpdate: 'tool_call', toolCallId: 'tool-1', title: 'Read web/src/components/agents/SessionChat.vue', status: 'in_progress' })
      await chunk(page, 3, 'Reading the release checklist first. ')
      await expect(panel.locator('.live-turn')).toContainText('Reading the release checklist first.')
      await expect(panel.locator('.live-turn .working')).toContainText('Working')
      await expect(send).toContainText('After this turn')
      await expect(steer).toBeEnabled()
      await expect(steer).toContainText('Send now')
      await expect(stop).toBeEnabled()
    })
    await page.screenshot({ path: testInfo.outputPath(`aeon-1071-streaming-${theme}-${width}.png`) })
    await guard.check(async () => {
      await update(page, 4, { sessionUpdate: 'tool_call', toolCallId: 'tool-1', status: 'completed' })
      for (let i = 0; i < 24; i++) await chunk(page, 5 + i, `Paragraph ${i + 1}: the gate stays green and the counts match the staging build.\n\n`)
      const last = panel.getByText('Paragraph 24: the gate stays green', { exact: false })
      await expect(last).toBeVisible()
      await expect(last).toBeInViewport()
    })
    // Stop works mid-stream: Esc outside the field interrupts the turn.
    await guard.check(async () => {
      await page.keyboard.press('Escape')
      await expect.poll(() => calls.filter(call => call.method === 'POST' && call.path.endsWith('/controls/interrupt')).length).toBe(1)
      await expect(panel.locator('.live-turn')).toContainText('Stopping…')
    })
    // The turn ends before the interrupt is confirmed: no claim yet.
    await guard.check(async () => {
      await update(page, 40, { sessionUpdate: 'state', state: 'idle' })
      await expect(panel.locator('.live-turn .fold')).toContainText('Used 1 tool')
      await expect(panel.locator('.live-turn')).not.toContainText('Stopping…')
      await expect(panel.locator('.live-turn')).not.toContainText('You stopped this turn')
      await expect(send).toContainText('Send')
      await expect(steer).toBeDisabled()
    })
    // Once the harness applied it, the block says the person stopped the turn.
    await guard.check(async () => {
      Object.assign(data.controls.at(-1)!, { state: 'completed', outcome: 'applied', completed_at: new Date(now + 2_000).toISOString() })
      await page.clock.runFor(2_100)
      await expect(panel.locator('.live-turn')).toContainText('You stopped this turn')
    })
    // The tool fold opens below its toggle; the toggle stays put.
    const fold = panel.locator('.live-turn .fold')
    const folding = await controlStability(page, { fold, composer, send, stop })
    await folding.check(async () => {
      await fold.click()
      await expect(fold).toHaveAttribute('aria-expanded', 'true')
      await expect(panel.locator('.live-turn .tools')).toContainText('Read web/src/components/agents/SessionChat.vue')
    })
    await folding.check(async () => {
      await fold.click()
      await expect(panel.locator('.live-turn .tools')).toHaveCount(0)
    })
    folding.done()
    await page.screenshot({ path: testInfo.outputPath(`aeon-1071-live-${theme}-${width}.png`) })
    // The saved chat-native reply replaces the live block in place.
    chat.push({ message: { message_id: '3e000000-0000-4000-8000-000000000900', conversation_id: conversation, read_seq: '900', sent_event_position: '900', body: 'Final: the release checks pass.', payload_mode: 'inline', sender_principal_id: worker.agent_principal_id, created_at: new Date(now + 5_000).toISOString() } })
    await guard.check(async () => {
      await emit(page, 'chat', { type: 'message', message_id: '3e000000-0000-4000-8000-000000000900', receipt: null })
      await expect(panel.getByText('Final: the release checks pass.', { exact: true })).toBeVisible()
      await expect(panel.locator('.live-turn')).toHaveCount(0)
    })
    expect(data.messages.some(message => message.body.startsWith('Final:'))).toBe(false)
    guard.done()
    // Nothing interim was sent anywhere: the only writes are the interrupt and
    // the read marker the confirmation wait let flush, and none carries live text.
    const writes = calls.filter(call => call.method !== 'GET')
    expect(writes.map(call => call.path)).toEqual([expect.stringMatching(/\/controls\/interrupt$/), `/api/projects/${worker.project_id}/harness-sessions/${worker.id}/read-marker`])
    for (const write of writes) expect(JSON.stringify(write.body ?? null)).not.toMatch(/Paragraph|release checklist|SessionChat\.vue/)
  })
}

const cases = [
  { name: 'native steer', session: { harness: 'claude', advertised_capabilities: ['inbox', 'steer', 'interrupt'] }, steer: 'Send now', primary: 'After this turn', stop: true },
  { name: 'no steer control', session: { harness: 'codex', advertised_capabilities: ['inbox', 'interrupt'] }, steer: null, primary: 'After this turn', stop: true },
  { name: 'next-step steer', session: { harness: 'pi', advertised_capabilities: ['inbox', 'steer', 'interrupt'] }, steer: 'At next step', primary: 'After this turn', stop: true },
  { name: 'unmanaged', session: { harness: 'grok', management_mode: 'unmanaged', advertised_capabilities: ['inbox', 'status'] }, steer: null, primary: 'Send', stop: false },
] as const
for (const item of cases) test(`AEON-1071 composer offers sends per capability: ${item.name}`, async ({ page }) => {
  const { worker } = await setup(page, { session: { ...item.session, activity: 'busy' }, bound: false })
  await page.setViewportSize({ width: 1024, height: 860 })
  await page.goto(`/agents/${worker.id}?tab=messages`)
  const panel = panelOf(page)
  await expect(panel.getByRole('textbox', { name: 'Message to release-lead' })).toBeVisible()
  if (item.steer) await expect(panel.locator('.steer-send')).toContainText(item.steer)
  else await expect(panel.locator('.steer-send')).toHaveCount(0)
  await expect(panel.getByRole('button', { name: 'Send now', exact: true })).toHaveCount(item.steer === 'Send now' ? 1 : 0)
  await expect(panel.locator('.compose .send')).toContainText(item.primary)
  // Stop is always in the composer; it works wherever an interrupt is allowed.
  await expect(panel.locator('.chat-stop')).toBeVisible()
  if (item.stop) await expect(panel.locator('.chat-stop')).toBeEnabled()
  else await expect(panel.locator('.chat-stop')).toBeDisabled()
  // An unbound session simply has no live stream.
  expect(await page.evaluate(() => (window as unknown as { __live?: unknown[] }).__live?.length ?? 0)).toBe(0)
})

// Risk (AEON-1071 gate): the turn ended on its own, then the interrupt was
// refused, and the block still claimed the person stopped it.
test('AEON-1071 a refused stop after the turn ended claims nothing', async ({ page }) => {
  const { worker, data, calls } = await setup(page)
  // The shared fixture applies every polled control; this harness refuses it.
  await page.route(/\/harness-sessions\/[^/]+\/controls\/c[^/]+$/, route => {
    if (route.request().method() !== 'GET') return route.fallback()
    const id = new URL(route.request().url()).pathname.split('/').at(-1)
    const found = data.controls.find(control => control.id === id)
    return found ? route.fulfill({ json: found }) : route.fallback()
  })
  await page.setViewportSize({ width: 1440, height: 860 })
  await page.goto(`/agents/${worker.id}?tab=messages`)
  const panel = panelOf(page)
  const composer = panel.getByRole('textbox', { name: 'Message to release-lead' })
  await expect(composer).toBeVisible()
  // Esc outside a field: leave the composer so the key reaches the chat.
  await composer.focus()
  await composer.blur()
  await expect.poll(() => page.evaluate(() => (window as unknown as { __live?: unknown[] }).__live?.length ?? 0)).toBe(1)
  await update(page, 1, { sessionUpdate: 'state', state: 'running' })
  await chunk(page, 2, 'Checking the gate.')
  await expect(panel.locator('.live-turn')).toContainText('Checking the gate.')
  await page.keyboard.press('Escape')
  await expect.poll(() => calls.filter(call => call.method === 'POST' && call.path.endsWith('/controls/interrupt')).length).toBe(1)
  await expect(panel.locator('.live-turn')).toContainText('Stopping…')
  await update(page, 3, { sessionUpdate: 'state', state: 'idle' })
  await expect(panel.locator('.live-turn')).not.toContainText('Stopping…')
  Object.assign(data.controls.at(-1)!, { state: 'completed', outcome: 'rejected', reason: 'not_running', completed_at: new Date(now + 2_000).toISOString() })
  await page.clock.runFor(2_100)
  // The completed control was read (its toast shows) before the claim is judged.
  await expect(page.getByText(/refused the interrupt/).first()).toBeVisible()
  await expect(panel.locator('.live-turn')).toContainText('Checking the gate.')
  await expect(panel.locator('.live-turn')).not.toContainText('You stopped this turn')
})
