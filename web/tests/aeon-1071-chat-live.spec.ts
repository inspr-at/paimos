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
  await page.route('**/api/chat-sessions/*/thread', route => {
    lookups.push(new URL(route.request().url()).pathname)
    return options.bound === false
      ? route.fulfill({ status: 404, json: { error: 'chat binding unavailable' } })
      : route.fulfill({ json: { contract: 'chat-v1', conversation_id: conversation, role: { contract: 'chat-v1' }, revision: '2', binding_epoch: '1', readiness: { state: 'offline', capabilities: [], reason: 'session_offline' } } })
  })
  return { worker, data, calls, lookups, template }
}

const emit = (page: Page, type: string, payload: unknown) => page.evaluate(([kind, data]) => {
  const live = (window as unknown as { __live?: EventTarget[] }).__live ?? []
  live.at(-1)?.dispatchEvent(new MessageEvent(kind, { data }))
}, [type, JSON.stringify(payload)] as const)
const update = (page: Page, sequence: number, body: Record<string, unknown>) => emit(page, 'chat', { type: 'update', update: body, dropped_events: 0, source_sequence: sequence })
const chunk = (page: Page, sequence: number, text: string) => update(page, sequence, { sessionUpdate: 'agent_message_chunk', content: { type: 'text', text } })

for (const width of [390, 1440]) for (const theme of ['light', 'dark'] as const) {
  test(`AEON-1071 streams in place, stops mid-stream and yields to the saved reply ${theme} ${width}`, async ({ page }, testInfo) => {
    const { worker, data, calls, lookups, template } = await setup(page, { theme })
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
    await guard.check(async () => {
      await update(page, 40, { sessionUpdate: 'state', state: 'idle' })
      await expect(panel.locator('.live-turn')).toContainText('You stopped this turn')
      await expect(panel.locator('.live-turn .fold')).toContainText('Used 1 tool')
      await expect(send).toContainText('Send')
      await expect(steer).toBeDisabled()
    })
    await page.screenshot({ path: testInfo.outputPath(`aeon-1071-live-${theme}-${width}.png`) })
    // The saved final reply replaces the live block in place.
    data.messages.push({ ...template, id: '3e000000-0000-4000-8000-000000000900', sent_event_id: 900, body: 'Final: the release checks pass.', created_at: new Date(now + 5_000).toISOString(), sender_principal_id: worker.agent_principal_id, recipient_principal_id: me.id, to: 'paimos:markus', sender_session_id: worker.id, sender_label: 'release-lead' } as typeof template)
    await guard.check(async () => {
      await emit(page, 'chat', { type: 'message', message_id: '3e000000-0000-4000-8000-000000000900', receipt: null })
      await expect(panel.getByText('Final: the release checks pass.', { exact: true })).toBeVisible()
      await expect(panel.locator('.live-turn')).toHaveCount(0)
    })
    guard.done()
    // Nothing interim was sent anywhere: the only writes are the interrupt.
    expect(calls.filter(call => call.method !== 'GET').map(call => call.path)).toEqual([expect.stringMatching(/\/controls\/interrupt$/)])
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
