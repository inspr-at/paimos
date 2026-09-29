// SPDX-License-Identifier: AGPL-3.0-only
// AEON-273: the session chat on phones and desktop. Overview and Messages tabs with
// an unread badge, a per-viewer read watermark, a pinned bottom with a jump button,
// delivery ticks, and no horizontal scroll with long unbroken tokens.
import { mkdirSync } from 'node:fs'
import { resolve } from 'node:path'
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'

const now = Date.parse('2026-09-29T06:00:00Z')
const longUrl = 'https://ci.example.test/inspr-at/paimos/actions/runs/18446744073709551615/jobs/9223372036854775807/logs?attempt=3&filter=playwright-session-chat-overflow-check'
const longId = 'sha256:4f9c2a7be1d04c55a3a6c7f1d2e9b8a7c6d5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a4b3c2d1e0f9a8b7c6'

async function setup(page: Page, options: { theme?: 'light' | 'dark'; count?: number; storage?: Record<string, string>; readMark?: { event: number; id: string }; failReadMarks?: number } = {}) {
  await page.clock.install({ time: now })
  const work = fixtures(); work.preferences.theme = { choice: options.theme ?? 'light' }
  await mockWork(page, work, { admin: true })
  const data = agentData({ now, me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' }, nodes: {} })
  const worker = data.sessions[0]!
  Object.assign(worker, { display_label: 'release-lead', run_id: null })
  data.sessions.splice(1); data.runs.splice(0); data.approvals.splice(0)
  const template = data.messages[1]!
  const count = options.count ?? 12
  const bodies = [
    'Picked up the release checks. Running the session chat suite first.',
    `CI log for the failing job: ${longUrl}`,
    'Good. Keep the fix small and ship it behind the release gate.',
    `The image digest is ${longId} and matches the staging build.`,
    'Counts are in. Should stale hosts sort last, or keep their position with a muted row?',
  ]
  const messages = Array.from({ length: count }, (_, i) => {
    const mine = i % 3 === 2
    return {
      ...template, id: `3e000000-0000-4000-8000-${String(100 + i).padStart(12, '0')}`, sent_event_id: 200 + i,
      body: bodies[i % bodies.length]!, created_at: new Date(now - (count - i) * 90_000).toISOString(),
      ...(mine
        ? { sender_principal_id: me.id, recipient_principal_id: worker.agent_principal_id, to: 'claude:camy', recipient_session_id: worker.id, sender_session_id: undefined, sender_label: 'Markus' }
        : { sender_principal_id: worker.agent_principal_id, recipient_principal_id: me.id, to: 'paimos:markus', sender_session_id: worker.id, sender_label: 'release-lead' }),
    } as typeof template
  })
  data.messages.splice(0, data.messages.length, ...messages)
  // Seed once per tab, so a reload shows what the page itself stored.
  if (options.storage) await page.addInitScript(entries => {
    if (sessionStorage.getItem('seeded')) return
    for (const [k, v] of Object.entries(entries)) localStorage.setItem(k, v)
    sessionStorage.setItem('seeded', '1')
  }, options.storage)
  const calls = await mockAgents(page, data, {
    ...(options.readMark ? { readMark: { sessionId: worker.id, ...options.readMark } } : {}),
    ...(options.failReadMarks ? { failReadMarks: options.failReadMarks } : {}),
  })
  const receipts: string[] = []
  await page.route('**/api/inbox/messages/*/receipt', route => {
    const id = route.request().url().split('/').at(-2)!
    receipts.push(id)
    const index = messages.findIndex(m => m.id === id)
    if (index < 0) return route.fulfill({ status: 404, json: { error: 'not found' } })
    const handed = index < count - 3
    return route.fulfill({ json: { message_id: id, state: handed ? 'handed_off' : 'queued', handed_off_at: handed ? new Date(now - 60_000).toISOString() : null, failure_reason: '' } })
  })
  return { data, worker, calls, messages, receipts }
}
// The viewer has read up to the post with this sent_event_id (200 + index).
const readUpTo = (sessionId: string, event: number) => ({ 'aeon.session-read.v1': JSON.stringify({ [`${me.id}:${sessionId}`]: { event, id: 'seen', at: 1 } }) })
const worker0 = '5e000000-0000-4000-8000-000000000001'
const markerPuts = (calls: { method: string; path: string; body: unknown }[]) => calls.filter(call => call.method === 'PUT' && call.path.endsWith('/read-marker'))
const localEvent = (page: Page, sessionId: string) => page.evaluate(key => {
  const raw = localStorage.getItem('aeon.session-read.v1')
  return raw ? (JSON.parse(raw)[key]?.event as number | undefined) ?? 0 : 0
}, `${me.id}:${sessionId}`)
const panelOf = (page: Page) => page.getByRole('complementary', { name: 'Session details' })
const messagesTab = (page: Page) => panelOf(page).getByRole('tab', { name: /Messages/ })
const scroller = (page: Page) => panelOf(page).locator('.thread-scroll')
const noHorizontalScroll = (page: Page) => page.evaluate(() => {
  const doc = document.scrollingElement!
  const panel = document.querySelector('.session-panel')!
  const inner = [...document.querySelectorAll('.session-panel .scroll, .session-panel .thread-scroll')] as HTMLElement[]
  return { doc: doc.scrollWidth <= doc.clientWidth, panel: panel.scrollWidth <= panel.clientWidth, inner: inner.every(el => el.offsetParent === null || el.scrollWidth <= el.clientWidth) }
})
function shot(page: Page, name: string) {
  const dir = process.env.SESSION_CHAT_SHOTS
  if (!dir) return
  mkdirSync(dir, { recursive: true })
  return page.screenshot({ path: resolve(dir, `${name}.png`) })
}

for (const width of [375, 390, 430]) {
  test(`${width}: no horizontal scroll with long URLs and IDs, in both tabs and while replying`, async ({ page }) => {
    const { worker } = await setup(page)
    await page.setViewportSize({ width, height: 844 })
    await page.goto(`/agents/${worker.id}`)
    await expect(panelOf(page).getByRole('tab', { name: 'Overview' })).toHaveAttribute('aria-selected', 'true')
    expect(await noHorizontalScroll(page)).toEqual({ doc: true, panel: true, inner: true })
    await messagesTab(page).click()
    await expect(panelOf(page).getByText(longUrl.slice(0, 40), { exact: false }).first()).toBeVisible()
    expect(await noHorizontalScroll(page)).toEqual({ doc: true, panel: true, inner: true })
    await panelOf(page).getByRole('button', { name: 'Reply' }).last().click()
    await panelOf(page).getByRole('textbox', { name: 'Message to release-lead' }).fill(`${longUrl} ${longId}`)
    expect(await noHorizontalScroll(page)).toEqual({ doc: true, panel: true, inner: true })
    // 16px keeps iOS Safari from zooming (and side-scrolling) on focus.
    expect(await panelOf(page).locator('textarea').evaluate(el => getComputedStyle(el).fontSize)).toBe('16px')
  })
}

test('Messages is its own tab with an unread badge; seeing the posts clears it and the tab is remembered', async ({ page }) => {
  const { worker } = await setup(page, { count: 5, storage: readUpTo(worker0, 201) })
  await page.setViewportSize({ width: 1600, height: 1000 })
  await page.goto(`/agents/${worker.id}`)
  const panel = panelOf(page)
  // Read up to the second post: the two later agent posts are unread.
  await expect(messagesTab(page).locator('.count')).toHaveText('2')
  await expect(panel.locator('#session-panel-messages')).toBeHidden()
  await shot(page, 'overview-1600')
  await messagesTab(page).click()
  await expect(messagesTab(page)).toHaveAttribute('aria-selected', 'true')
  await expect(panel.getByRole('separator', { name: '2 new' })).toBeVisible()
  await expect(panel.locator('.msg').nth(2).getByText('You')).toBeVisible() // Own post after the watermark, above the divider.
  await expect(messagesTab(page).locator('.count')).toHaveCount(0)
  // Everything is in view, so the watermark reaches the newest post.
  await expect.poll(() => page.evaluate(key => JSON.parse(localStorage.getItem('aeon.session-read.v1') ?? '{}')[key]?.event, `${me.id}:${worker.id}`)).toBe(204)
  await shot(page, 'messages-unread-1600')
  // Remembered per viewer, and the posts stay read.
  await page.reload()
  await expect(messagesTab(page)).toHaveAttribute('aria-selected', 'true')
  await expect(panel.getByRole('separator')).toHaveCount(0)
  await panel.getByRole('tab', { name: 'Overview' }).click()
  await page.reload()
  await expect(panel.getByRole('tab', { name: 'Overview' })).toHaveAttribute('aria-selected', 'true')
  await expect(messagesTab(page).locator('.count')).toHaveCount(0)
})

for (const width of [1600, 390]) {
  test(`the unread badge follows the server read marker at ${width}`, async ({ page }) => {
    const seen = '3e000000-0000-4000-8000-000000000101'
    const { worker, calls } = await setup(page, { count: 5, readMark: { event: 201, id: seen } })
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.goto(`/agents/${worker.id}`)
    const panel = panelOf(page)
    const overview = panel.getByRole('tab', { name: 'Overview' })
    const badge = messagesTab(page).locator('.count')
    // No local watermark: the badge is the server marker (event 201 leaves two later posts).
    await expect(overview).toHaveAttribute('aria-selected', 'true')
    await expect(panel.locator('.msg')).toHaveCount(5)
    await expect(badge).toHaveText('2')
    await expect.poll(() => page.evaluate(() => localStorage.getItem('aeon.session-read.v1'))).toContain('"event":201')
    await page.evaluate(() => localStorage.removeItem('aeon.session-read.v1'))
    await page.reload()
    await expect(overview).toHaveAttribute('aria-selected', 'true')
    await expect(panel.locator('.msg')).toHaveCount(5)
    await expect(badge).toHaveText('2')
    await shot(page, `server-unread-${width}`)
    // Reading the thread moves the server marker. The badge is read back on Overview,
    // where selecting Messages cannot hide it or mark the posts again.
    await messagesTab(page).click()
    await expect(panel.locator('.msg').first()).toBeVisible()
    await scroller(page).evaluate(el => el.scrollTo({ top: el.scrollHeight }))
    await expect.poll(() => localEvent(page, worker.id)).toBe(204)
    await page.clock.fastForward(1_600)
    await expect.poll(() => markerPuts(calls).at(-1)?.body).toMatchObject({ last_read_event_id: 204 })
    await page.evaluate(() => {
      localStorage.removeItem('aeon.session-read.v1')
      localStorage.setItem('aeon.session-tab', 'overview')
    })
    await page.reload()
    await expect(overview).toHaveAttribute('aria-selected', 'true')
    await expect(panel.locator('.msg')).toHaveCount(5)
    await expect(badge).toHaveCount(0)
    await shot(page, `server-read-${width}`)
  })
}

for (const width of [1600, 390]) {
  test(`scrolling a 40-message thread sends at most two read markers at ${width}`, async ({ page }) => {
    const { worker, calls } = await setup(page, { count: 40, storage: { 'aeon.session-tab': 'messages' } })
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.goto(`/agents/${worker.id}`)
    const thread = scroller(page)
    await expect(panelOf(page).locator('.msg').first()).toBeVisible()
    await thread.evaluate(el => {
      const step = Math.max(48, Math.floor(el.clientHeight * 0.7))
      for (let top = 0; top < el.scrollHeight; top += step) el.scrollTop = top
      el.scrollTop = el.scrollHeight
    })
    // The opening view can mark a post before the scroll's later observations land.
    // The clock stays put, so the trailing timer cannot fire until that settles.
    const lastEvent = 200 + 39
    await expect.poll(() => localEvent(page, worker.id)).toBe(lastEvent)
    expect(markerPuts(calls)).toHaveLength(0)
    await page.clock.fastForward(1_600)
    await expect.poll(() => (markerPuts(calls).at(-1)?.body as { last_read_event_id?: number } | undefined)?.last_read_event_id).toBe(lastEvent)
    expect(markerPuts(calls).length).toBeGreaterThan(0)
    expect(markerPuts(calls).length).toBeLessThanOrEqual(2)
  })
}

test('the thread renders before the read marker answers', async ({ page }) => {
  const { worker } = await setup(page, { count: 6, storage: { 'aeon.session-tab': 'messages' } })
  let release: () => void = () => {}
  const gate = new Promise<void>(resolve => { release = resolve })
  await page.route('**/read-marker', async route => {
    if (route.request().method() === 'GET') await gate
    await route.fallback()
  })
  try {
    const opened = page.goto(`/agents/${worker.id}`)
    await expect(panelOf(page).locator('.msg').first()).toBeVisible()
    release()
    await opened
  } finally { release() }
})

test('focusing the window picks up a read from another device', async ({ page }) => {
  const seen = '3e000000-0000-4000-8000-000000000101'
  const { worker } = await setup(page, { count: 5, readMark: { event: 201, id: seen } })
  let event = 201
  await page.route('**/read-marker', async route => {
    if (route.request().method() !== 'GET') return route.fallback()
    const sessionId = new URL(route.request().url()).pathname.split('/').at(-2)
    return route.fulfill({ json: { session_id: sessionId, last_read_message_id: seen, last_read_event_id: event, read_at: '2026-09-29T06:10:00.000Z' } })
  })
  await page.setViewportSize({ width: 1600, height: 1000 })
  await page.goto(`/agents/${worker.id}`)
  const badge = messagesTab(page).locator('.count')
  await expect(panelOf(page).locator('.msg')).toHaveCount(5)
  await expect(badge).toHaveText('2')
  event = 204
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect(badge).toHaveCount(0)
})

test('a failed read marker is sent again on the next flush, without an error', async ({ page }) => {
  const { worker, calls } = await setup(page, { count: 8, storage: { 'aeon.session-tab': 'messages' }, failReadMarks: 1 })
  await page.setViewportSize({ width: 1600, height: 1000 })
  await page.goto(`/agents/${worker.id}`)
  await expect(panelOf(page).locator('.msg').first()).toBeVisible()
  await scroller(page).evaluate(el => el.scrollTo({ top: el.scrollHeight }))
  await expect.poll(() => localEvent(page, worker.id)).toBeGreaterThan(200)
  await page.clock.fastForward(1_600)
  await expect.poll(() => markerPuts(calls).length).toBe(1)
  await expect(panelOf(page).getByRole('alert')).toHaveCount(0)
  await page.evaluate(() => {
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' })
    document.dispatchEvent(new Event('visibilitychange'))
  })
  await expect.poll(() => markerPuts(calls).length).toBe(2)
  await expect(panelOf(page).getByRole('alert')).toHaveCount(0)
})

test('an agent viewer does not write a read marker', async ({ page }) => {
  const { worker, calls } = await setup(page, { count: 8, storage: { 'aeon.session-tab': 'messages' } })
  await page.route('**/api/me', route => route.fulfill({
    json: { principal: { id: me.id, name: me.name, kind: 'agent', roles: ['member'] }, tenant: { id: 't1', name: 'INSPR Studio' }, identity: null, dev_mode: true },
  }))
  await page.setViewportSize({ width: 1600, height: 1000 })
  await page.goto(`/agents/${worker.id}`)
  await expect(panelOf(page).locator('.msg').first()).toBeVisible()
  await scroller(page).evaluate(el => el.scrollTo({ top: el.scrollHeight }))
  await expect.poll(() => localEvent(page, worker.id)).toBeGreaterThan(200)
  await page.clock.fastForward(1_600)
  await page.evaluate(() => {
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' })
    document.dispatchEvent(new Event('visibilitychange'))
  })
  expect(markerPuts(calls)).toHaveLength(0)
})

test('?tab=messages deep-links to the thread; an ended session starts read on a new browser', async ({ page }) => {
  const { worker } = await setup(page, { count: 4 })
  Object.assign(worker, { phase: 'stopped', stopped_at: new Date(now).toISOString(), stop_reason: 'done' })
  await page.goto(`/agents/${worker.id}?tab=messages`)
  await expect(messagesTab(page)).toHaveAttribute('aria-selected', 'true')
  await expect(panelOf(page).locator('.msg').first()).toBeVisible()
  await expect(messagesTab(page).locator('.count')).toHaveCount(0)
  await expect(panelOf(page).getByText('This session has ended.', { exact: true })).toBeVisible()
})

test('the thread stays pinned at the bottom, counts posts that arrive while scrolled up, and jumps back', async ({ page }) => {
  const { worker, data, messages } = await setup(page, { count: 16, storage: { 'aeon.session-tab': 'messages' } })
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto(`/agents/${worker.id}`)
  const panel = panelOf(page)
  const thread = scroller(page)
  await expect(panel.locator('.msg').first()).toBeVisible()
  // Everything unread: the view opens at the first post, without a divider.
  await expect(panel.locator('.msg').first()).toBeInViewport()
  await expect(panel.getByRole('separator')).toHaveCount(0)
  await expect(panel.getByRole('button', { name: /new messages, go to the latest/ })).toBeVisible()
  // Reading to the bottom clears the badge.
  await thread.evaluate(el => el.scrollTo({ top: el.scrollHeight }))
  await expect(messagesTab(page).locator('.count')).toHaveCount(0)
  const atBottom = () => thread.evaluate(el => el.scrollHeight - el.scrollTop - el.clientHeight <= 32)
  expect(await atBottom()).toBe(true)
  await expect(panel.getByRole('button', { name: /latest message/ })).toHaveCount(0)

  const arrive = async (n: number, body: string) => {
    const template = messages.at(-3)!
    data.messages.push({ ...template, id: `3e000000-0000-4000-8000-${String(900 + n).padStart(12, '0')}`, sent_event_id: 900 + n, body, created_at: new Date(now + n * 1000).toISOString() })
    await page.clock.runFor(21_000)
  }
  // At the bottom: a new post keeps the view pinned.
  await arrive(1, 'Pinned: this arrives while you read the latest post.')
  await expect(panel.getByText('Pinned: this arrives')).toBeInViewport()
  expect(await atBottom()).toBe(true)

  // Scrolled up: the post only counts toward the jump button.
  await thread.evaluate(el => el.scrollTo({ top: 0 }))
  await expect(panel.getByRole('button', { name: 'Go to the latest message' })).toBeVisible()
  await arrive(2, 'Off-screen: this arrives while you read older posts.')
  const jump = panel.getByRole('button', { name: '1 new message, go to the latest' })
  await expect(jump).toBeVisible()
  await expect(jump).toHaveText('1 new')
  // The open tab carries no badge; the count sits on the jump button.
  await expect(messagesTab(page).locator('.count')).toHaveCount(0)
  await shot(page, 'scrolled-up-390')
  await jump.click()
  await expect.poll(atBottom).toBe(true)
  await expect(jump).toHaveCount(0)
  await panel.getByRole('tab', { name: 'Overview' }).click()
  await expect(messagesTab(page).locator('.count')).toHaveCount(0)
  await messagesTab(page).click()

  // Focusing the composer goes to the bottom.
  await thread.evaluate(el => el.scrollTo({ top: 0 }))
  await panel.getByRole('textbox', { name: 'Message to release-lead' }).focus()
  await expect.poll(atBottom).toBe(true)
  await expect(panel.getByRole('button', { name: /latest message/ })).toHaveCount(0)
  await shot(page, 'focused-390')
})

test('own posts show quiet delivery ticks from the sender receipt', async ({ page }) => {
  const { worker, receipts, messages } = await setup(page, { count: 9, storage: { 'aeon.session-tab': 'messages' } })
  await page.goto(`/agents/${worker.id}`)
  const mine = panelOf(page).locator('.msg.mine')
  await expect(mine).toHaveCount(3)
  await expect(mine.first().locator('.tick.handed_off')).toHaveAttribute('data-tip', /^Picked up by the session · /)
  await expect(mine.last().locator('.tick.queued')).toHaveAttribute('data-tip', 'Sent · waiting for the session to pick it up')
  // Only the viewer's own posts are asked for, never the agent's.
  const own = new Set(messages.filter(m => m.sender_principal_id === me.id).map(m => m.id))
  expect(receipts.length).toBeGreaterThan(0)
  expect(receipts.every(id => own.has(id))).toBe(true)
})

test('a managed session has no composer or Reply; Steer lives in the session controls (AEON-260)', async ({ page }) => {
  const { worker } = await setup(page, { count: 4, storage: { 'aeon.session-tab': 'messages' } })
  worker.advertised_capabilities = [...worker.advertised_capabilities, 'managed_control_v1']
  await page.goto(`/agents/${worker.id}`)
  const panel = panelOf(page)
  await expect(panel.locator('.msg').first()).toBeVisible()
  await expect(panel.locator('.composer')).toHaveCount(0)
  await expect(panel.getByRole('button', { name: 'Reply' })).toHaveCount(0)
  await expect(panel.getByText('Steer this managed session with the controls above.')).toBeVisible()
})

test.describe('phone screenshots', () => {
  // A touch phone: no hover, so Reply stays visible as on an iPhone.
  test.use({ hasTouch: true, isMobile: true, viewport: { width: 390, height: 844 } })
  for (const theme of ['light', 'dark'] as const) {
    test(`${theme} 390: screenshots of both tabs`, async ({ page }) => {
      test.skip(!process.env.SESSION_CHAT_SHOTS, 'screenshots only')
      const { worker } = await setup(page, { theme, count: 7, storage: readUpTo(worker0, 203) })
      await page.goto(`/agents/${worker.id}`)
      await expect(messagesTab(page)).toBeVisible()
      await shot(page, `overview-${theme}-390`)
      await messagesTab(page).click()
      await expect(panelOf(page).getByRole('separator', { name: /new/ })).toBeVisible()
      await shot(page, `messages-unread-${theme}-390`)
      await scroller(page).evaluate(el => el.scrollTo({ top: 0 }))
      await expect(panelOf(page).getByRole('button', { name: /latest message/ })).toBeVisible()
      await shot(page, `scrolled-up-${theme}-390`)
      await panelOf(page).getByRole('textbox').focus()
      await expect(panelOf(page).getByRole('button', { name: /latest message/ })).toHaveCount(0)
      await shot(page, `focused-${theme}-390`)
    })
  }
})
