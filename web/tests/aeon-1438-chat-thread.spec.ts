// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1438: the chat thread and composer for people, phone and desktop.
// Risks: a Desk delivery shows its question id and CLI words; receipts repeat on
// history; delivery jargon reaches the thread; Copy/Reply clutter every message
// or move it on hover; the composer moves when a turn starts or ends, on focus
// or on send; a long word scrolls the page sideways; the desktop thread spreads
// past a reading column; quiet text drops below 4.5:1 in either theme; the
// rhythm between items depends on their kind (Copy/Reply reserving a row);
// a touch-opened menu shows the keyboard focus ring.
import { test, expect, type Locator, type Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { controlStability } from './control-stability'
import { mockEffectivePermissions } from './authz-fixtures'

test.use({ timezoneId: 'Europe/Vienna' })
const now = Date.parse('2026-10-16T06:04:00Z') // 08:04 in Vienna
const conversation = '7c000000-0000-4000-8000-000000001438'
const answered = '55adee55-145a-4c2e-9b1f-0c7d2e1a9f30'
const waiting = '66bdff66-256b-4d3f-8c2a-1d8e3f2b0a41'
const longWord = 'sha256:4f9c2a7be1d04c55a3a6c7f1d2e9b8a7c6d5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a4b3c2d1e0f9a8b7c6d5e4f3a2b1'
// The strings list: what never reaches a person in the thread.
const JARGON = [/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/i, /ask status/i, /read through/i, /question_id/i, /"type":/, /between steps/i, /inbox hook/i, /\bharness\b/i, /\bCLI\b/]
const REPLY_PATH = 'M6.4 3.6 2.6 7.4l3.8 3.8M2.6 7.4h6.8a4 4 0 0 1 4 4v1'
const panelOf = (page: Page) => page.getByRole('complementary', { name: 'Session details' })

async function setup(page: Page, options: { theme?: 'light' | 'dark'; session?: Record<string, unknown>; today?: boolean } = {}) {
  // A scripted EventSource: the agents feed opens quietly; the chat-live stream takes frames from the test.
  await page.addInitScript(() => {
    class Stream extends EventTarget {
      onopen: ((event: Event) => void) | null = null
      onerror: ((event: Event) => void) | null = null
      readyState = 1
      constructor(url: string) {
        super()
        queueMicrotask(() => this.onopen?.(new Event('open')))
        if (url.includes('/live')) {
          ((window as unknown as { __live?: Stream[] }).__live ??= []).push(this)
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
  Object.assign(worker, { display_label: 'AEON-LEAD', run_id: null, activity: 'idle', harness: 'claude', management_mode: 'managed', advertised_capabilities: ['inbox', 'status', 'steer', 'interrupt', 'stop'] }, options.session ?? {})
  data.sessions.splice(1); data.runs.splice(0); data.approvals.splice(0)
  const template = data.messages[1]!
  let event = 300
  const at = (minutesAgo: number) => new Date(now - minutesAgo * 60_000).toISOString()
  const agentPost = (body: string, minutesAgo: number, fields: Record<string, unknown> = {}) => ({ ...template, id: `3e000000-0000-4000-8000-${String(++event).padStart(12, '0')}`, sent_event_id: event, body, created_at: at(minutesAgo), sender_principal_id: worker.agent_principal_id, recipient_principal_id: me.id, to: 'paimos:markus', sender_session_id: worker.id, sender_label: 'AEON-LEAD', ...fields }) as typeof template
  const myPost = (body: string, minutesAgo: number, fields: Record<string, unknown> = {}) => ({ ...template, id: `3e000000-0000-4000-8000-${String(++event).padStart(12, '0')}`, sent_event_id: event, body, created_at: at(minutesAgo), sender_principal_id: me.id, recipient_principal_id: worker.agent_principal_id, to: 'claude:camy', recipient_session_id: worker.id, sender_session_id: undefined, sender_label: 'Markus', ...fields }) as typeof template
  const yesterday = options.today ? [] : Array.from({ length: 12 }, (_, i) => i % 3 === 2
    ? myPost(`Yesterday's note ${i + 1}: keep the release gate as it is.`, 60 * 20 - i * 6)
    : agentPost(`Yesterday's report ${i + 1}: the nightly run is green and the queue is empty.`, 60 * 20 - i * 6))
  const deskRoot = (question: string, minutesAgo: number) => agentPost(`Decision Desk question ${question}; read through ask status`, minutesAgo, { is_action_request: true, expects_reply: true, human_resolution_outcome: 'resolved' })
  const root = deskRoot(answered, 20)
  const messages = [
    ...yesterday,
    agentPost('Morning, Markus. Overnight on `main`:\n\n- Merged 4 PRs; the nightly gate is green since 06:10.\n- AEON-1182 passed review and waits for its merge.\n\nThe merge question for AEON-1182 is on the Desk.', 23),
    root,
    myPost(JSON.stringify({ type: 'question.answer', question_id: answered, answer_id: 'a1', revision: 2, answer: 'merge now', replaces: '', asker_id: 'k1' }), 20, { reply_to: root.id }),
    agentPost('Merged AEON-1182. Shall I start the release check for 135 now, or wait until the CI fixes are in?', 6, { is_action_request: true, expects_reply: true, human_resolution_outcome: 'resolved' }),
    deskRoot(waiting, 5),
    agentPost(`The image digest is ${longWord} and matches the staging build.`, 4),
    myPost('Start it now. Keep the CI fixes in their own PR.', 2),
  ]
  data.messages.splice(0, data.messages.length, ...messages)
  const calls = await mockAgents(page, data)
  // A person who may decide Desk questions: an open one waits for them.
  await page.route('**/api/me/permissions*', route => {
    const answer = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    answer.workspace.permissions.push('questions.read', 'questions.decide')
    return route.fulfill({ json: answer })
  })
  const ownIds = messages.filter(m => m.sender_principal_id === me.id).map(m => m.id)
  const latest = ownIds.at(-1)!
  await page.route('**/api/inbox/message-status?*', route => {
    const ids = new URL(route.request().url()).searchParams.get('ids')!.split(',')
    return route.fulfill({ json: { items: ids.filter(id => ownIds.includes(id) || data.sent.some(m => m.id === id)).map(id => ({ message_id: id, status: id === latest ? 'read' : 'delivered', delivered_at: at(1), read_at: id === latest ? at(1) : null, deliver_by: at(-4) })) } })
  })
  const question = (id: string, text: string, state: 'open' | 'answered') => ({
    id, project_id: worker.project_id, revision: state === 'answered' ? 2 : 1, state, created_at: at(25), updated_at: at(20), suggested_outcome: 'once', suggestion_reason: 'agent_suggestion', pending: [],
    input: { request_id: `r-${id}`, question: text, options: [{ id: 'yes', title: 'Yes, merge now', answer: 'merge now' }, { id: 'wait', title: 'Wait for the 14:00 train', answer: 'wait' }], meanwhile: 'carries_on' },
    askers: [{ id: 'k1', principal_id: worker.agent_principal_id, session_id: worker.id, reply_root_id: 'root', comment_node_id: 'c1', input: { request_id: `r-${id}`, question: text, options: [], meanwhile: 'carries_on' } }],
    ...(state === 'answered' ? { answer: { id: 'a1', revision: 2, answer: 'merge now', option_id: 'yes', outcome: 'once', decided_by: me.id, created_at: at(20), deliver_after: at(20) } } : {}),
  })
  await page.route('**/api/questions/*', route => {
    const id = decodeURIComponent(new URL(route.request().url()).pathname.split('/').at(-1)!)
    if (id === answered) return route.fulfill({ json: question(id, 'Merge AEON-1182 now, or wait for the 14:00 release train?', 'answered') })
    if (id === waiting) return route.fulfill({ json: question(id, 'Ship the 135 notes as they are, or wait for the copy gate?', 'open') })
    return route.fulfill({ status: 404, json: { error: 'not found' } })
  })
  await page.route(/\/api\/chat-threads\/[^/]+\/messages(\?|$)/, route => route.fulfill({ json: { contract: 'chat-v1', items: [], has_more_before: false, has_more_after: false, before_cursor: null, after_cursor: null, snapshot_cursor: 'c', migration_epoch: '0' } }))
  await page.route('**/api/chat-sessions/*/thread', route => route.fulfill({ json: { contract: 'chat-v1', conversation_id: conversation, role: { contract: 'chat-v1' }, revision: '2', binding_epoch: '1', readiness: { state: 'offline', capabilities: [], reason: 'session_offline' } } }))
  return { worker, data, calls, messages, latest }
}

const emit = (page: Page, type: string, payload: unknown) => page.evaluate(([kind, data]) => {
  ((window as unknown as { __live?: EventTarget[] }).__live ?? []).at(-1)?.dispatchEvent(new MessageEvent(kind, { data }))
}, [type, JSON.stringify(payload)] as const)
const turn = (page: Page, sequence: number, state: 'running' | 'idle') => emit(page, 'chat', { type: 'update', update: { sessionUpdate: 'state', state }, dropped_events: 0, source_sequence: sequence })
const liveReady = (page: Page) => expect.poll(() => page.evaluate(() => (window as unknown as { __live?: unknown[] }).__live?.length ?? 0)).toBe(1)

const noHorizontalScroll = (page: Page) => page.evaluate(() => {
  const doc = document.scrollingElement!, panel = document.querySelector('.session-panel')!
  const inner = [...document.querySelectorAll<HTMLElement>('.session-panel .thread-scroll, .session-panel .composer')]
  return { doc: doc.scrollWidth <= doc.clientWidth, panel: panel.scrollWidth <= panel.clientWidth, inner: inner.every(el => el.scrollWidth <= el.clientWidth + 1) }
})
// The visible gap between consecutive thread items. A message counts from its
// first to its last visible part; the Copy/Reply row is not part of it, so a
// reserved row shows up as a wider gap under that message.
const rhythm = (thread: Locator) => thread.evaluate(list => {
  const kind = (item: Element) => item.matches('.day, .new-divider') ? 'separator' : item.matches('.desk') ? 'card' : 'text'
  const bounds = (item: Element) => {
    if (!item.matches('.msg')) return item.getBoundingClientRect()
    const parts = [...item.children].filter(child => !child.matches('.message-actions')).map(child => child.getBoundingClientRect())
    return { top: Math.min(...parts.map(part => part.top)), bottom: Math.max(...parts.map(part => part.bottom)) }
  }
  const items = [...list.children]
  return items.slice(1).map((item, index) => ({ pair: `${kind(items[index]!)}→${kind(item)}`, gap: Math.round((bounds(item).top - bounds(items[index]!).bottom) * 10) / 10 }))
})
// WCAG contrast of an element's text over the layers actually behind it,
// including a ::before paper (the Desk card) and translucent tints.
async function contrast(locator: Locator) {
  return locator.first().evaluate(el => {
    const canvas = document.createElement('canvas'); canvas.width = canvas.height = 1
    const ctx = canvas.getContext('2d', { willReadFrequently: true })!
    const rgba = (css: string) => { ctx.clearRect(0, 0, 1, 1); ctx.fillStyle = '#000'; ctx.fillStyle = css; ctx.fillRect(0, 0, 1, 1); const [r, g, b, a] = ctx.getImageData(0, 0, 1, 1).data; return [r!, g!, b!, a! / 255] as const }
    const layers: (readonly [number, number, number, number])[] = []
    for (let node: Element | null = el; node; node = node.parentElement) {
      const before = getComputedStyle(node, '::before')
      if (before.content !== 'none' && node !== el) { const c = rgba(before.backgroundColor); if (c[3] > 0) layers.push(c) }
      const c = rgba(getComputedStyle(node).backgroundColor)
      if (c[3] > 0) layers.push(c)
      if (c[3] >= 1) break
    }
    let bg: [number, number, number] = [255, 255, 255]
    for (const [r, g, b, a] of layers.reverse()) bg = [r * a + bg[0] * (1 - a), g * a + bg[1] * (1 - a), b * a + bg[2] * (1 - a)]
    const [r, g, b, a] = rgba(getComputedStyle(el).color)
    const fg = [r * a + bg[0] * (1 - a), g * a + bg[1] * (1 - a), b * a + bg[2] * (1 - a)]
    const lum = (c: number[]) => { const [x, y, z] = c.map(v => { const s = v / 255; return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4 }); return 0.2126 * x! + 0.7152 * y! + 0.0722 * z! }
    const [hi, lo] = [lum(fg), lum(bg)].sort((p, q) => q - p)
    return (hi! + 0.05) / (lo! + 0.05)
  })
}

for (const width of [1440, 1024, 390, 320]) for (const theme of ['light', 'dark'] as const) {
  test(`AEON-1438 thread ${width} ${theme}: Desk cards, quiet meta, latest receipt, calm composer`, async ({ page }, testInfo) => {
    const { worker, latest } = await setup(page, { theme })
    await page.setViewportSize({ width, height: width === 320 ? 568 : width <= 390 ? 844 : 900 })
    await page.goto(`/agents/${worker.id}?tab=messages`)
    const panel = panelOf(page)
    const thread = panel.getByRole('list', { name: 'Messages', exact: true })
    const composer = panel.getByRole('textbox', { name: 'Message to AEON-LEAD' })
    await expect(composer).toBeVisible()
    await liveReady(page)

    // A Desk delivery is one card: the question, its state and Open.
    const desk = panel.getByRole('article', { name: /^Decision Desk: Merge AEON-1182 now/ })
    await expect(desk).toContainText('Answered: Yes, merge now')
    await expect(desk.getByRole('link', { name: 'Open on the Decision Desk' })).toHaveAttribute('href', new RegExp(`/decision-desk\\?item=q(:|%3A)${answered}`))
    await expect(panel.getByRole('article', { name: /^Decision Desk: Ship the 135 notes/ })).toContainText('Waiting for you')
    await expect(thread.locator('.msg.desk')).toHaveCount(2)
    // The strings list: no question id, CLI or delivery words anywhere in the thread.
    const text = await thread.innerText()
    for (const word of JARGON) expect(text, String(word)).not.toMatch(word)
    expect(text).not.toContain('Delivered between steps')
    await expect(thread.getByRole('separator', { name: 'Yesterday', exact: true })).toHaveCount(1)
    await expect(thread.getByRole('separator', { name: 'Today', exact: true })).toHaveCount(1)
    // Each day opens with its separator: the thread's first item, and right above today's first post.
    await expect(thread.locator(':scope > li').first()).toHaveAttribute('aria-label', 'Yesterday')
    await expect(thread.locator(':scope > li.day + li.msg').last()).toContainText('Morning, Markus.')

    // Receipts only on the latest own message.
    const own = thread.locator('.msg.mine')
    await expect(own.last()).toHaveAttribute('data-id', latest)
    await expect(own.last().locator('.delivery.read')).toContainText('Read')
    await expect(thread.locator('.delivery')).toHaveCount(1)
    await expect(thread.locator('.msg.mine[data-receipt="delivered"]').first()).toBeAttached()
    await expect(thread.getByText('Sending', { exact: true })).toHaveCount(0)
    // One meta line: name and time on one line, at most one chip, which fits beside them.
    const request = thread.locator('.msg', { hasText: 'Shall I start the release check' })
    await expect(request.locator('.msg-chip')).toHaveCount(1)
    await expect(request.locator('.msg-chip')).toHaveText('Action request · resolved')
    const meta = await request.locator('.msg-meta').evaluate(row => {
      const box = (selector: string) => row.querySelector(selector)!.getBoundingClientRect()
      const line = row.getBoundingClientRect(), name = box('.msg-author'), time = box('.msg-time'), chip = box('.msg-chip')
      return { height: line.height, nameTime: Math.abs((name.top + name.bottom) / 2 - (time.top + time.bottom) / 2), chipInside: chip.top >= line.top - 0.5 && chip.bottom <= line.bottom + 0.5 && chip.right <= line.right + 0.5 }
    })
    expect(meta.height, 'one meta line').toBeLessThanOrEqual(21)
    expect(meta.nameTime, 'time beside the name').toBeLessThanOrEqual(2)
    expect(meta.chipInside, 'the chip fits on the meta line').toBe(true)
    await expect(request.locator('.msg-time')).toHaveText('07:58')
    expect(await noHorizontalScroll(page)).toEqual({ doc: true, panel: true, inner: true })
    if (width >= 1024) {
      // The reading column: at most 720 px; own bubbles end at its right edge.
      const column = await panel.locator('.thread-col').evaluate(col => {
        const box = col.getBoundingClientRect(), mine = [...col.querySelectorAll('.msg.mine .msg-body')].at(-1)!.getBoundingClientRect()
        return { width: box.width, right: box.right, bubble: mine.right }
      })
      expect(column.width).toBeLessThanOrEqual(720.5)
      expect(Math.abs(column.right - column.bubble), 'own bubble on the column edge').toBeLessThanOrEqual(1)
    }
    // Contrast: quiet type stays at 4.5:1 or more in both themes.
    for (const [name, target] of [
      ['time', request.locator('.msg-time')], ['chip', request.locator('.msg-chip')], ['day', thread.locator('.day').last()],
      ['desk state', desk.locator('.desk-state')], ['desk open', desk.locator('.desk-open')], ['desk time', desk.locator('time')],
      ['receipt', own.last().locator('.delivery')], ['own time', own.last().locator('.msg-time')],
    ] as const) expect(await contrast(target), `${name} contrast`).toBeGreaterThanOrEqual(4.5)
    await page.screenshot({ path: testInfo.outputPath(`AEON-1438/thread-${width}-${theme}.png`) })

    // The composer never moves: not on focus, not when a turn starts or ends, not on send.
    const send = panel.locator('.compose .send')
    const phone = width <= 720
    const controls: Record<string, Locator> = phone
      ? { send, plus: panel.getByRole('button', { name: 'Add a ticket or session' }), field: composer, frame: panel }
      : { send, row: panel.locator('.compose-row'), slot: panel.locator('.compose-row .choice'), field: composer, frame: panel }
    const guard = await controlStability(page, controls)
    await guard.check(async () => {
      await composer.focus()
      if (!phone) await expect(panel.locator('.compose-hint')).toHaveCSS('opacity', '1')
    })
    await guard.check(async () => {
      await turn(page, 1, 'running')
      await expect(panel.locator('.live-turn')).toBeVisible()
      await expect(send).toHaveAccessibleName('After this turn')
      if (!phone) await expect(panel.getByRole('button', { name: 'Send now', exact: true })).toBeVisible()
      if (!phone) await expect(panel.locator('.compose-hint')).toContainText('After this turn')
      await page.screenshot({ path: testInfo.outputPath(`AEON-1438/turn-${width}-${theme}.png`) })
    })
    await guard.check(async () => {
      await turn(page, 2, 'idle')
      await expect(send).toHaveAccessibleName('Send')
      await expect(panel.getByRole('button', { name: 'Send now', exact: true })).toHaveCount(0)
    })
    await guard.check(async () => {
      await composer.fill('Attach the shots.')
      await composer.press('Enter')
      await expect(composer).toHaveValue('')
      await expect(thread.locator('.msg.mine').last()).toContainText('Attach the shots.')
      // The receipt moved to the new latest message; history keeps none.
      await expect(thread.locator('.delivery')).toHaveCount(1)
      await expect(thread.locator('.msg.mine').last().locator('.delivery')).toHaveCount(1)
    })
    guard.done()
    await page.screenshot({ path: testInfo.outputPath(`AEON-1438/sent-${width}-${theme}.png`) })
    // A reply: the reply line sits above the field.
    const asking = thread.locator('.msg.agent', { hasText: 'Shall I start the release check' })
    await asking.hover()
    await asking.getByRole('button', { name: 'Reply', exact: true }).click()
    await expect(panel.locator('.replying')).toContainText('Replying to “Merged AEON-1182.')
    await page.screenshot({ path: testInfo.outputPath(`AEON-1438/reply-${width}-${theme}.png`) })
  })
}

for (const width of [1440, 1024, 390]) test(`AEON-1438 Copy and Reply on demand ${width}: hover and focus reveal them in the gap, nothing moves`, async ({ page }, testInfo) => {
  const { worker } = await setup(page)
  await page.setViewportSize({ width, height: width <= 390 ? 844 : 900 })
  await page.goto(`/agents/${worker.id}?tab=messages`)
  const panel = panelOf(page)
  const thread = panel.getByRole('list', { name: 'Messages', exact: true })
  const message = panel.locator('.msg.agent', { hasText: 'Shall I start the release check' })
  const next = panel.locator('.msg.desk').last()
  const copy = message.getByRole('button', { name: 'Copy', exact: true }), reply = message.getByRole('button', { name: 'Reply', exact: true })
  await expect(message).toBeVisible()
  await expect(panel.getByRole('article', { name: /^Decision Desk: Ship the 135 notes/ })).toContainText('Waiting for you')
  await expect(panel.getByRole('article', { name: /^Decision Desk: Merge AEON-1182 now/ })).toContainText('Answered: Yes, merge now')

  // One gap between every two items, whatever their kind: 20 px, 16 px on phones.
  const expected = width <= 720 ? 16 : 20
  const gaps = await rhythm(thread)
  for (const pair of ['text→text', 'text→card', 'card→text']) expect(gaps.some(item => item.pair === pair), `${pair} is in the fixture`).toBe(true)
  for (const item of gaps) expect(Math.abs(item.gap - expected), `${item.pair} gap ${item.gap} px`).toBeLessThanOrEqual(1)

  // Never permanently visible: the row is transparent until hover or focus, and it takes no space.
  await expect(message.locator('.message-actions')).toHaveCSS('opacity', '0')
  await panel.locator('.pane-bar').hover()
  const guard = await controlStability(page, { message, next, copy, reply, composer: panel.locator('.compose-box') })
  await guard.check(async () => { await message.hover(); await expect(message.locator('.message-actions')).toHaveCSS('opacity', '1') })
  // The icons sit in the gap under the message: below its text, above the next item.
  const place = await message.evaluate(row => {
    const text = row.querySelector('.chat-markdown, .markdown-body')!.getBoundingClientRect(), after = row.nextElementSibling!.getBoundingClientRect()
    const icons = [...row.querySelectorAll('.message-actions svg')].map(icon => icon.getBoundingClientRect())
    return { rowEnd: row.getBoundingClientRect().bottom - text.bottom, icons: icons.map(icon => ({ top: icon.top - text.bottom, bottom: after.top - icon.bottom })) }
  })
  expect(Math.abs(place.rowEnd), 'the message ends with its text').toBeLessThanOrEqual(.5)
  expect(place.icons).toHaveLength(2)
  for (const icon of place.icons) { expect(icon.top, 'icon below the text').toBeGreaterThanOrEqual(-.5); expect(icon.bottom, 'icon above the next item').toBeGreaterThanOrEqual(-.5) }
  await page.screenshot({ path: testInfo.outputPath(`AEON-1438/hover-${width}-light.png`) })
  // The pointer travels through the gap to the actions without losing them.
  const box = (await copy.boundingBox())!, text = (await message.locator('.chat-markdown, .markdown-body').first().boundingBox())!
  await page.mouse.move(text.x + text.width / 2, text.y + text.height + expected / 2)
  await expect(message.locator('.message-actions')).toHaveCSS('opacity', '1')
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2)
  await expect(message.locator('.message-actions')).toHaveCSS('opacity', '1')
  await guard.check(async () => { await panel.locator('.pane-bar').hover(); await copy.focus(); await expect(message.locator('.message-actions')).toHaveCSS('opacity', '1') })
  // Reply uses the reply glyph, never an arrow that reads as Back.
  expect(await reply.locator('path').getAttribute('d')).toBe(REPLY_PATH)
  await guard.check(async () => {
    await reply.click()
    await expect(panel.locator('.replying')).toContainText('Replying to “Merged AEON-1182.')
    await expect(panel.getByRole('textbox', { name: 'Message to AEON-LEAD' })).toBeFocused()
  })
  guard.done()
  expect(await rhythm(thread), 'hover and Reply leave the rhythm alone').toEqual(gaps)
})

// The thread opens with its day: a thread that starts today shows "Today" first, at scrollTop 0.
for (const width of [1440, 390]) test(`AEON-1438 the thread starts with its day separator, ${width}`, async ({ page }, testInfo) => {
  const { worker } = await setup(page, { today: true })
  await page.setViewportSize({ width, height: width <= 390 ? 844 : 900 })
  await page.goto(`/agents/${worker.id}?tab=messages`)
  const panel = panelOf(page)
  const thread = panel.getByRole('list', { name: 'Messages', exact: true })
  const scroller = panel.locator('.thread-scroll')
  await expect(panel.getByRole('article', { name: /^Decision Desk: Ship the 135 notes/ })).toContainText('Waiting for you')
  await scroller.hover()
  await page.mouse.wheel(0, -20000)
  await expect.poll(() => scroller.evaluate(el => el.scrollTop)).toBe(0)
  const first = thread.locator(':scope > li').first()
  await expect(first).toHaveAttribute('role', 'separator')
  await expect(first).toHaveAttribute('aria-label', 'Today')
  await expect(first).toBeInViewport()
  await expect(thread.locator('.day')).toHaveCount(1)
  await page.screenshot({ path: testInfo.outputPath(`AEON-1438/thread-start-${width}-light.png`) })
})

test.describe('touch', () => {
  test.use({ hasTouch: true, isMobile: true, viewport: { width: 390, height: 844 } })
  const longPress = async (page: Page, target: Locator) => {
    const box = (await target.boundingBox())!
    const point = { clientX: box.x + box.width / 2, clientY: box.y + Math.min(box.height / 2, 30), pointerType: 'touch', isPrimary: true, bubbles: true, pointerId: 7 }
    await target.dispatchEvent('pointerdown', point)
    await page.clock.runFor(500)
    await target.dispatchEvent('pointerup', point)
  }
  for (const theme of ['light', 'dark'] as const) test(`AEON-1438 long press opens Reply and Copy in place, 390 ${theme}`, async ({ page }, testInfo) => {
    const { worker } = await setup(page, { theme })
    await page.goto(`/agents/${worker.id}?tab=messages`)
    const panel = panelOf(page)
    const message = panel.locator('.msg.agent', { hasText: 'Shall I start the release check' })
    await expect(message).toBeVisible()
    // Touch has no hover: no action row at all, so nothing is permanently visible.
    await expect(message.locator('.message-actions')).toBeHidden()
    await expect(panel.getByRole('article', { name: /^Decision Desk: Ship the 135 notes/ })).toContainText('Waiting for you')
    for (const item of await rhythm(panel.getByRole('list', { name: 'Messages', exact: true }))) expect(Math.abs(item.gap - 16), `${item.pair} gap ${item.gap} px`).toBeLessThanOrEqual(1)
    const menu = page.getByRole('menu', { name: 'Message actions' })
    const guard = await controlStability(page, { message, next: panel.locator('.msg.desk').last(), send: panel.locator('.compose .send'), plus: panel.getByRole('button', { name: 'Add a ticket or session' }) })
    await guard.check(async () => {
      await longPress(page, message)
      await expect(menu).toBeVisible()
      await expect(message).toHaveClass(/pressed/)
      await expect(panel.locator('.msg-scrim')).toBeVisible()
    })
    for (const item of await menu.getByRole('menuitem').all()) expect((await item.boundingBox())!.height).toBeGreaterThanOrEqual(44)
    await expect(menu.getByRole('menuitem')).toHaveText(['Reply', 'Copy'])
    // A press moves focus into the menu without the keyboard ring (the JoeDesk P4 rule).
    const first = menu.getByRole('menuitem', { name: 'Reply' })
    await expect(first).toBeFocused()
    await expect(first).toHaveCSS('outline-style', 'none')
    await page.screenshot({ path: testInfo.outputPath(`AEON-1438/long-press-390-${theme}.png`) })
    await guard.check(async () => {
      await page.keyboard.press('Escape')
      await expect(menu).toHaveCount(0)
      await expect(message).not.toHaveClass(/pressed/)
    })
    // Your own message offers Copy only; a tap on Copy hands focus back to the message, still without a ring.
    const mine = panel.locator('.msg.mine').last()
    await longPress(page, mine)
    await expect(menu.getByRole('menuitem')).toHaveText(['Copy'])
    await menu.getByRole('menuitem', { name: 'Copy' }).click()
    await expect(menu).toHaveCount(0)
    await expect(mine).toBeFocused()
    await expect(mine).toHaveCSS('box-shadow', 'none')
    // The keyboard reaches the same menu: Enter on the focused message, and there the ring shows.
    await message.focus()
    await page.keyboard.press('Enter')
    await expect(menu).toBeVisible()
    await expect(menu.getByRole('menuitem', { name: 'Reply' })).toBeFocused()
    await expect(menu.getByRole('menuitem', { name: 'Reply' })).toHaveCSS('outline-style', 'solid')
    if (theme === 'light') await page.screenshot({ path: testInfo.outputPath('AEON-1438/menu-keyboard-390-light.png') })
    await menu.getByRole('menuitem', { name: 'Reply' }).click()
    await expect(panel.locator('.replying')).toContainText('Merged AEON-1182.')
    guard.done()
    // 44 px targets on phones.
    for (const target of [panel.locator('.compose .send'), panel.getByRole('button', { name: 'Add a ticket or session' }), panel.getByRole('link', { name: 'Open on the Decision Desk' }).first()]) {
      const box = (await target.boundingBox())!
      expect(box.height).toBeGreaterThanOrEqual(44)
    }
  })

  test('AEON-1438 phone composer: the choice row follows typing during a turn, never the turn itself', async ({ page }, testInfo) => {
    const { worker } = await setup(page)
    await page.goto(`/agents/${worker.id}?tab=messages`)
    const panel = panelOf(page)
    const composer = panel.getByRole('textbox', { name: 'Message to AEON-LEAD' })
    const send = panel.locator('.compose .send'), plus = panel.getByRole('button', { name: 'Add a ticket or session' })
    await expect(composer).toBeVisible()
    await liveReady(page)
    // A turn starting with an empty draft moves nothing.
    const idle = await controlStability(page, { composer, send, plus })
    await idle.check(async () => { await turn(page, 1, 'running'); await expect(panel.locator('.live-turn')).toBeVisible() })
    idle.done()
    // Typing during the turn opens the second row: Send now · After this turn.
    await composer.fill('Also post the release note on the Desk once pma is live.')
    await expect(panel.locator('.composer')).toHaveClass(/two/)
    await expect(panel.getByRole('button', { name: 'Send now', exact: true })).toBeVisible()
    await expect(send).toHaveAccessibleName('After this turn')
    await page.screenshot({ path: testInfo.outputPath('AEON-1438/two-row-390-light.png') })
    // The turn ends with the draft present: Send now hides, its slot stays.
    const choice = await controlStability(page, { composer, send, plus, slot: panel.locator('.compose-row .choice') })
    await choice.check(async () => {
      await turn(page, 2, 'idle')
      await expect(panel.getByRole('button', { name: 'Send now', exact: true })).toHaveCount(0)
      await expect(send).toHaveAccessibleName('Send')
    })
    choice.done()
    // Clearing the draft leaves the second row.
    await composer.fill('')
    await expect(panel.locator('.composer')).not.toHaveClass(/two/)
    await expect(send).toBeDisabled()
  })

  test('AEON-1438 "+" inserts a ticket reference, never an attachment', async ({ page }) => {
    const { worker } = await setup(page)
    await page.route('**/api/nodes?*', route => route.fulfill({ json: { items: [{ id: 'n-1182', key: 'AEON-1182', title: 'Merge queue fairness', kind_id: 'k', state: 'open', fields: {}, parent_id: null, created_at: new Date(now).toISOString(), updated_at: new Date(now).toISOString() }], next_cursor: null } }))
    await page.route('**/api/search?*', route => route.fulfill({ json: { items: [] } }))
    await page.goto(`/agents/${worker.id}?tab=messages`)
    const panel = panelOf(page)
    const composer = panel.getByRole('textbox', { name: 'Message to AEON-LEAD' })
    await composer.fill('Look at')
    await panel.getByRole('button', { name: 'Add a ticket or session' }).click()
    const menu = page.getByRole('menu', { name: 'Add a ticket or session' })
    await expect(menu.getByRole('menuitem')).toHaveText(['Ticket…', 'Session…'])
    await menu.getByRole('menuitem', { name: 'Ticket…' }).click()
    await page.getByRole('searchbox', { name: 'Search tickets' }).fill('1182')
    await page.clock.runFor(300)
    await page.getByRole('menuitem', { name: /AEON-1182/ }).click()
    await expect(composer).toHaveValue('Look at AEON-1182 ')
    await expect(composer).toBeFocused()
  })
})
