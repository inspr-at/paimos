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
  const chat: { message: Record<string, unknown>; person_read_state?: string }[] = []
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

// Fix round 3. The project routes know only project messages: these mocks
// refuse a chat-native parent or read marker the way the server does
// (inbox_compat_messages membership), so a wrong route fails the test.
const nativeFinal = (worker: { agent_principal_id: string }, id: string, position: number, body: string, extra: Record<string, unknown> = {}) => ({
  message: { message_id: id, conversation_id: conversation, read_seq: String(position), sent_event_position: String(position), body, payload_mode: 'inline', sender_principal_id: worker.agent_principal_id, created_at: new Date(now - 30_000).toISOString() },
  ...extra,
})
async function projectValidation(page: Page, data: { messages: { id: string }[]; sent: { id: string }[] }) {
  const refused: { path: string; body: unknown }[] = []
  const known = () => new Set([...data.messages, ...data.sent].map(message => message.id))
  await page.route(/\/api\/projects\/[^/]+\/messages$/, route => {
    const body = route.request().postDataJSON() as { reply_to?: string } | null
    if (route.request().method() !== 'POST' || !body?.reply_to || known().has(body.reply_to)) return route.fallback()
    refused.push({ path: new URL(route.request().url()).pathname, body })
    return route.fulfill({ status: 400, json: { error: 'invalid reply_to_id' } })
  })
  await page.route(/\/api\/projects\/[^/]+\/harness-sessions\/[^/]+\/read-marker$/, route => {
    const body = route.request().method() === 'PUT' ? route.request().postDataJSON() as { last_read_message_id?: string } : null
    if (!body || known().has(body.last_read_message_id ?? '')) return route.fallback()
    refused.push({ path: new URL(route.request().url()).pathname, body })
    return route.fulfill({ status: 400, json: { error: 'message is not in this session' } })
  })
  return refused
}

// The chat outbox accepts a reply only to a message of its own thread.
async function chatOutbox(page: Page, chat: { message: Record<string, unknown> }[]) {
  const outbox: { client_message_id: string; body: string; reply_to?: string }[] = []
  await page.route(/\/api\/chat-threads\/[^/]+\/outbox$/, route => {
    if (route.request().method() !== 'POST') return route.fallback()
    const input = route.request().postDataJSON() as { client_message_id: string; body: string; reply_to?: string }
    outbox.push(input)
    if (input.reply_to && !chat.some(item => item.message.message_id === input.reply_to)) return route.fulfill({ status: 404, json: { error: 'not found' } })
    const position = 901 + outbox.length - 1
    const message = { message_id: `3e000000-0000-4000-8000-${String(position).padStart(12, '0')}`, conversation_id: conversation, read_seq: String(position), sent_event_position: String(position), body: input.body, payload_mode: 'inline', reply_to: input.reply_to ?? null }
    chat.push({ message: { ...message, sender_principal_id: me.id, created_at: new Date(now).toISOString() } })
    return route.fulfill({ json: { contract: 'chat-live-v1', message, receipt: { message_id: message.message_id, state: 'sent' } } })
  })
  return outbox
}

// Risk (AEON-1071 gate): Reply on a chat-native final sent its id as the
// parent of a project message, which the server refuses.
test('AEON-1071 a reply to a chat-native final goes to its chat thread', async ({ page }) => {
  const { worker, data, chat } = await setup(page)
  const refused = await projectValidation(page, data)
  const final = '3e000000-0000-4000-8000-000000000900'
  chat.push(nativeFinal(worker, final, 900, 'Final: the release checks pass.'))
  const outbox = await chatOutbox(page, chat)
  await page.setViewportSize({ width: 1440, height: 860 })
  await page.goto(`/agents/${worker.id}?tab=messages`)
  const panel = panelOf(page)
  const row = panel.locator('.msg', { hasText: 'Final: the release checks pass.' })
  await expect(row).toBeVisible()
  await row.hover()
  await row.getByRole('button', { name: 'Reply', exact: true }).click()
  await expect(panel.locator('.replying')).toContainText('Final: the release checks pass.')
  await panel.getByRole('textbox', { name: 'Message to release-lead' }).fill('Ship it after the smoke test.')
  await panel.locator('.compose .send').click()
  await expect.poll(() => outbox.length).toBe(1)
  expect(outbox[0]).toMatchObject({ body: 'Ship it after the smoke test.', reply_to: final })
  expect(outbox[0]!.client_message_id).toBeTruthy()
  const reply = panel.locator('.msg', { hasText: 'Ship it after the smoke test.' })
  await expect(reply).toBeVisible()
  await expect(reply.locator('[data-status="not_delivered"]')).toHaveCount(0)
  expect(refused).toEqual([])
  expect(data.sent.some(message => message.body === 'Ship it after the smoke test.')).toBe(false)
})

// Risk (AEON-1071 gate): seeing a chat-native final wrote its id to the
// session read marker, which refuses it, so read state never persisted.
test('AEON-1071 a seen chat-native final is recorded on its chat thread', async ({ page }) => {
  const { worker, data, chat } = await setup(page)
  const refused = await projectValidation(page, data)
  const final = '3e000000-0000-4000-8000-000000000900'
  chat.push(nativeFinal(worker, final, 900, 'Final: the release checks pass.', { person_read_state: 'known_unread' }))
  const seen: string[][] = []
  await page.route(/\/api\/chat-threads\/[^/]+\/read-marker$/, route => {
    if (route.request().method() !== 'PUT') return route.fulfill({ json: { contract: 'chat-v1', conversation_id: conversation, contiguous_prefix: '0', chunks: [], migration_epoch: '0', revision: '0', next_cursor: null } })
    const ids = (route.request().postDataJSON() as { visible_message_ids: string[] }).visible_message_ids
    seen.push(ids)
    // The history page reports it as seen from now on, on every device.
    for (const item of chat) if (ids.includes(String(item.message.message_id))) Object.assign(item, { person_read_state: 'seen' })
    return route.fulfill({ json: { contract: 'chat-v1', conversation_id: conversation, contiguous_prefix: '0', chunks: [], migration_epoch: '0', revision: '1', next_cursor: null } })
  })
  await page.setViewportSize({ width: 1440, height: 860 })
  await page.goto(`/agents/${worker.id}?tab=messages`)
  const panel = panelOf(page)
  await expect(panel.locator('.msg', { hasText: 'Final: the release checks pass.' })).toBeInViewport()
  await page.clock.runFor(2_000)
  await expect.poll(() => seen.length).toBe(1)
  expect(seen[0]).toEqual([final])
  expect(refused).toEqual([])
  // Another visit: the thread's evidence stands, nothing is sent again.
  await page.evaluate(() => localStorage.removeItem('aeon.session-read.v1'))
  await page.reload()
  await expect(panel.locator('.msg', { hasText: 'Final: the release checks pass.' })).toBeInViewport()
  await page.clock.runFor(2_000)
  await expect.poll(() => page.evaluate(() => JSON.stringify(localStorage.getItem('aeon.session-read.v1')))).toContain('900')
  expect(seen.length).toBe(1)
  expect(refused).toEqual([])
})

// Risk (AEON-1071 gate): the first history read finished after a turn began;
// an earlier saved reply became the newest agent message and dismissed the
// ended turn before its own final arrived.
test('AEON-1071 a late history read never dismisses the live turn', async ({ page }) => {
  const { worker, chat } = await setup(page)
  chat.push(nativeFinal(worker, '3e000000-0000-4000-8000-000000000210', 210, 'Earlier saved reply.'))
  let release = () => {}
  const held = new Promise<void>(resolve => { release = resolve })
  let first = true
  await page.route(/\/api\/chat-threads\/[^/]+\/messages(\?|$)/, async route => {
    if (first) { first = false; await held }
    return route.fallback()
  })
  await page.setViewportSize({ width: 1440, height: 860 })
  await page.goto(`/agents/${worker.id}?tab=messages`)
  const panel = panelOf(page)
  const stop = panel.locator('.chat-stop')
  await expect.poll(() => page.evaluate(() => (window as unknown as { __live?: unknown[] }).__live?.length ?? 0)).toBe(1)
  await update(page, 1, { sessionUpdate: 'state', state: 'running' })
  await chunk(page, 2, 'Thinking about the next step.')
  await expect(panel.locator('.live-turn')).toContainText('Thinking about the next step.')
  await expect(stop).toBeEnabled()
  release()
  await expect(panel.getByText('Earlier saved reply.', { exact: true })).toBeVisible()
  await update(page, 3, { sessionUpdate: 'state', state: 'idle' })
  await expect(stop).toBeDisabled()
  await expect(panel.locator('.live-turn')).toContainText('Thinking about the next step.')
  // Its own final, announced by the stream, replaces it.
  chat.push(nativeFinal(worker, '3e000000-0000-4000-8000-000000000950', 950, 'Final: next step planned.'))
  await emit(page, 'chat', { type: 'message', message_id: '3e000000-0000-4000-8000-000000000950', receipt: null })
  await expect(panel.getByText('Final: next step planned.', { exact: true })).toBeVisible()
  await expect(panel.locator('.live-turn')).toHaveCount(0)
})

// Risk (AEON-1071 gate, fix round 4): reopening with a cached watermark on a
// chat-native final sent that id to the session read marker before the chat
// history had loaded, and retried the refused mark even after it had.
test('AEON-1071 a cached watermark on a chat-native final never reaches the session marker', async ({ page }) => {
  const { worker, data, calls, chat } = await setup(page)
  const refused = await projectValidation(page, data)
  const final = '3e000000-0000-4000-8000-000000000900'
  chat.push(nativeFinal(worker, final, 900, 'Final: the release checks pass.', { person_read_state: 'seen' }))
  // This browser read up to the chat-native final on an earlier visit.
  await page.addInitScript(([key, event, id, at]) => {
    localStorage.setItem('aeon.session-read.v1', JSON.stringify({ [key]: { event, id, at } }))
  }, [`${me.id}:${worker.id}`, 900, final, now] as const)
  await page.setViewportSize({ width: 1440, height: 860 })
  await page.goto(`/agents/${worker.id}?tab=messages`)
  const panel = panelOf(page)
  await expect(panel.locator('.msg', { hasText: 'Final: the release checks pass.' })).toBeVisible()
  await page.clock.runFor(2_000)
  const marks = () => calls.filter(call => call.method === 'PUT' && call.path.endsWith('/read-marker')).map(call => call.body as { last_read_message_id: string; last_read_event_id: number })
  // The session marker gets the newest project message before the final.
  await expect.poll(marks).toEqual([{ last_read_message_id: data.messages[2]!.id, last_read_event_id: 202 }])
  await page.clock.runFor(4_000)
  expect(refused).toEqual([])
  expect(marks()).toHaveLength(1)
  // The local watermark stays on the final.
  expect(await page.evaluate(() => JSON.parse(localStorage.getItem('aeon.session-read.v1') ?? '{}'))).toEqual({ [`${me.id}:${worker.id}`]: expect.objectContaining({ event: 900, id: final }) })
})

// Risk (AEON-1071 gate, fix round 4): only the newest visible row counted as
// read evidence, and a collapsed group named only its first and last posts.
test('AEON-1071 every visible chat-native post is recorded on its chat thread', async ({ page }) => {
  const { worker, chat } = await setup(page)
  const ids = [901, 902, 903, 904, 905].map(n => `3e000000-0000-4000-8000-000000000${n}`)
  const bodies = ['Alpha: the build is green.', 'Beta: the counts match.', 'Same check passed.', 'Same check passed.', 'Same check passed.']
  ids.forEach((id, i) => chat.push(nativeFinal(worker, id, 901 + i, bodies[i]!, { person_read_state: 'known_unread' })))
  const seen = new Set<string>()
  await page.route(/\/api\/chat-threads\/[^/]+\/read-marker$/, route => {
    if (route.request().method() !== 'PUT') return route.fallback()
    for (const id of (route.request().postDataJSON() as { visible_message_ids: string[] }).visible_message_ids) seen.add(id)
    return route.fulfill({ json: { contract: 'chat-v1', conversation_id: conversation, contiguous_prefix: '0', chunks: [], migration_epoch: '0', revision: '1', next_cursor: null } })
  })
  await page.setViewportSize({ width: 1440, height: 860 })
  await page.goto(`/agents/${worker.id}?tab=messages`)
  const panel = panelOf(page)
  // The three identical posts render as one row.
  await expect(panel.locator('.msg', { hasText: 'Same check passed.' }).locator('.duplicate')).toHaveText('×3')
  for (const body of bodies.slice(0, 3)) await expect(panel.locator('.msg', { hasText: body })).toBeInViewport({ ratio: 1 })
  await page.clock.runFor(2_000)
  await expect.poll(() => [...seen].sort()).toEqual(ids)
})

// Risk (AEON-1071 gate, fix round 4): a reply to a chat-native final offered
// Send now, but the chat outbox is read only between turns, so the chosen
// mode was silently dropped. The offered sends follow the next message's transport.
for (const width of [390, 1440]) test(`AEON-1071 a chat-native reply offers only what its transport consumes ${width}`, async ({ page }, testInfo) => {
  const { worker, data, chat } = await setup(page, { session: { activity: 'busy' } })
  const final = '3e000000-0000-4000-8000-000000000900'
  chat.push(nativeFinal(worker, final, 900, 'Final: the release checks pass.'))
  const outbox = await chatOutbox(page, chat)
  await page.setViewportSize({ width, height: 860 })
  await page.goto(`/agents/${worker.id}?tab=messages`)
  const panel = panelOf(page)
  const composer = panel.getByRole('textbox', { name: 'Message to release-lead' })
  const send = panel.locator('.compose .send'), steer = panel.locator('.steer-send'), stop = panel.locator('.chat-stop'), note = panel.locator('.steer-note')
  const row = panel.locator('.msg', { hasText: 'Final: the release checks pass.' })
  await expect(row).toBeVisible()
  await composer.fill('Ship it after the smoke test.')
  await composer.blur()
  // Managed delivery steers natively.
  await expect(steer).toContainText('Send now')
  await expect(steer).toBeEnabled()
  await expect(note).toHaveCount(0)
  const guard = await controlStability(page, { composer, send, stop })
  await guard.check(async () => {
    await row.hover()
    await row.getByRole('button', { name: 'Reply', exact: true }).click()
    await expect(panel.locator('.replying')).toContainText('Final: the release checks pass.')
    // The chat outbox is consumed between turns: no steer action, not even a disabled one.
    await expect(steer).toHaveCount(0)
    await expect(note).toHaveText('Chat replies arrive between turns')
    await expect(send).toContainText('After this turn')
    await expect(stop).toBeEnabled()
  })
  await page.screenshot({ path: testInfo.outputPath(`aeon-1071-chat-reply-${width}.png`) })
  await guard.check(async () => {
    // The steer shortcut sends after the turn, to the chat thread.
    await composer.press('ControlOrMeta+Enter')
    await expect.poll(() => outbox.length).toBe(1)
    expect(outbox[0]).toMatchObject({ body: 'Ship it after the smoke test.', reply_to: final })
    // The reply is done: the next message is managed again and may steer.
    await expect(steer).toContainText('Send now')
    await expect(note).toHaveCount(0)
  })
  guard.done()
  expect(data.sent.some(message => message.body === 'Ship it after the smoke test.')).toBe(false)
})
