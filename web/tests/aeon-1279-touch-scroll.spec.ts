// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1279: phone portrait scrolling ran away. Risk: a live change lands while a finger
// scrolls, or above the screen without anchoring, and the rows on screen jump; or the footer
// folding away resizes the page's scroller while momentum runs. Real touch gestures come from
// Chromium's CDP (Input.dispatchTouchEvent). WebKit has no scroll
// anchoring, so every surface here runs with it off, as on an iPhone. Live changes arrive on
// the page clock, never after a sleep; the first row on screen is measured before and after.
import { appendFileSync } from 'node:fs'
import { test, expect, type Browser, type CDPSession, type Page, type TestInfo } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { agentData, mockAgents, type AgentData } from './agents-fixtures'
import { liveServer } from './live-server'

const NOW = Date.parse('2026-10-13T07:00:00Z')
const PHONE = { width: 390, height: 844 }
const ago = (minutes: number) => new Date(NOW - minutes * 60_000).toISOString()
const sid = (n: number) => `5e000000-0000-4000-8000-${String(n).padStart(12, '0')}`
const ARRIVALS = [0, 1, 2].map(n => sid(900 + n))

test.use({ hasTouch: true, isMobile: true, viewport: PHONE })

// ---------- Helpers ----------
// WebKit (iPhone Safari) has no scroll anchoring: turn Chromium's off everywhere.
const asOnSafari = (page: Page) => page.addStyleTag({ content: '*, *::before, *::after { overflow-anchor: none !important; }' })
type SwipeState = { started: boolean; down: number; ended: boolean; start: number; top: number; events: string[] }
type SwipeWindow = { __swipe: SwipeState; __stopSwipe?: () => void }
// One native touch swipe; dy < 0 moves the finger up. A synthesized scroll can finish
// without delivering a scrollend on Linux. Dispatch the touch sequence explicitly, prove
// that the intended scroller moved while the finger was down, then wait for its own trusted
// scrollend after release. The input timestamps give the finger a stationary end (no fling),
// as Chromium's preventFling does; neither page-clock advances nor sleeps settle the scroll.
async function swipe(page: Page, cdp: CDPSession, at: { x: number; y: number }, dy: number, scroller = 'main') {
  await page.locator(scroller).evaluate(el => {
    const win = window as unknown as SwipeWindow
    win.__stopSwipe?.()
    const listeners = new AbortController()
    const state: SwipeState = { started: false, down: 0, ended: false, start: el.scrollTop, top: el.scrollTop, events: [] }
    Object.assign(win, { __swipe: state, __stopSwipe: () => listeners.abort() })
    const options = { passive: true, signal: listeners.signal }
    for (const type of ['touchstart', 'touchmove', 'touchend', 'touchcancel']) {
      el.addEventListener(type, event => {
        if (!event.isTrusted) return
        state.events.push(type)
        state.down = (event as TouchEvent).touches.length
        if (type === 'touchstart') state.started = true
      }, options)
    }
    el.addEventListener('scroll', () => { state.top = el.scrollTop; state.events.push('scroll') }, options)
    el.addEventListener('scrollend', event => {
      state.events.push('scrollend')
      // A pending setup scrollend, or one from a nested scroller, cannot finish this swipe.
      if (event.isTrusted && event.target === el && state.started && state.down === 0 && el.scrollTop !== state.start) state.ended = true
    }, options)
  })
  const state = () => page.evaluate(() => (window as unknown as SwipeWindow).__swipe)
  // Keep the entire input history in the past: future touch samples can be resampled
  // against an earlier compositor frame and lose part of the requested movement.
  const releasedAt = Date.now() / 1000
  const timestamp = releasedAt - 0.5
  let down = false
  try {
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', timestamp, touchPoints: [{ ...at, id: 0 }] })
    down = true
    for (let step = 1; step <= 4; step++) {
      await cdp.send('Input.dispatchTouchEvent', { type: 'touchMove', timestamp: timestamp + step * 0.016, touchPoints: [{ x: at.x, y: at.y + dy * step / 4, id: 0 }] })
    }
    await expect.poll(async () => { const s = await state(); return s.started && s.down === 1 && (s.top - s.start) * -Math.sign(dy) > 0 }, 'the native touch moves the intended scroller with the finger down').toBe(true)
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchMove', timestamp: timestamp + 0.49, touchPoints: [{ x: at.x, y: at.y + dy, id: 0 }] })
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', timestamp: releasedAt, touchPoints: [] })
    down = false
    await expect.poll(state, 'the native scroll has finished').toMatchObject({ ended: true, down: 0 })
    const finished = await state()
    note(test.info(), `native finger travel ${dy} px; ${scroller} moved ${(finished.top - finished.start).toFixed(1)} px; events: ${finished.events.join(' ')}`)
  } finally {
    if (down) await cdp.send('Input.dispatchTouchEvent', { type: 'touchCancel', timestamp: releasedAt, touchPoints: [] })
    await page.evaluate(() => (window as unknown as SwipeWindow).__stopSwipe?.())
  }
}
// The first row fully on screen below the sticky heads that are stuck, and where it is.
async function firstOnScreen(page: Page, rows: string, heads: string, scroller = 'main') {
  return page.evaluate(([rows, heads, scroller]) => {
    const box = document.querySelector(scroller)!.getBoundingClientRect()
    let edge = box.top
    for (const head of document.querySelectorAll(heads)) {
      const r = head.getBoundingClientRect()
      if (r.height > 0 && r.top <= edge + 1) edge = Math.max(edge, r.bottom)
    }
    for (const el of document.querySelectorAll<HTMLElement>(rows)) {
      const r = el.getBoundingClientRect()
      if (r.height <= 0 || r.top < edge - 0.5 || r.bottom > box.bottom) continue
      const ref = el.dataset.row ? `[data-row="${el.dataset.row}"]` : el.dataset.id ? `[data-id="${el.dataset.id}"]` : `#${el.id}`
      return { ref, top: r.top, edge }
    }
    return null
  }, [rows, heads, scroller] as const)
}
const topOf = (page: Page, selector: string) => page.locator(selector).evaluate(el => el.getBoundingClientRect().top)
const rectOf = (page: Page, selector: string) => page.locator(selector).evaluate(el => { const r = el.getBoundingClientRect(); return { top: r.top, bottom: r.bottom, height: r.height, client: el.clientHeight } })
// An in-page record: touches down, pointer events on the list, the scroller's height on every
// scroll event, and whether the rows changed while a finger was down.
async function watchTouch(page: Page, rows: string, list = '.sessions', scroller = 'main') {
  await page.evaluate(([rows, list, scroller, offer]) => {
    const log = { down: 0, events: [] as string[], changedWhileDown: 0, offeredWhileDown: 0, heights: [] as number[] }
    Object.assign(window, { __touch: log })
    const ids = () => Array.from(document.querySelectorAll(rows), el => el.getAttribute('data-row') ?? el.id).join(',')
    let shown = ids()
    for (const type of ['touchstart', 'touchend', 'touchcancel'] as const) {
      window.addEventListener(type, event => { log.down = event.touches.length; log.events.push(type) }, { capture: true, passive: true })
    }
    const target = document.querySelector(list)
    for (const type of ['pointerenter', 'pointerdown', 'pointercancel', 'pointerleave']) target?.addEventListener(type, event => log.events.push(`${type}:${(event as PointerEvent).pointerType}`))
    target?.addEventListener('focusin', event => log.events.push(`focusin:${(event.target as HTMLElement).className}`))
    const el = document.querySelector<HTMLElement>(scroller)!
    el.addEventListener('scroll', () => log.heights.push(Math.round(el.getBoundingClientRect().height * 100) / 100), { passive: true })
    new MutationObserver(() => {
      const now = ids()
      if (now !== shown && log.down > 0) log.changedWhileDown++
      if (log.down > 0 && document.querySelector(offer)) log.offeredWhileDown++
      shown = now
    }).observe(document.body, { childList: true, subtree: true, attributes: true, attributeFilter: ['class'] })
  }, [rows, list, scroller, SHOW_NOW] as const)
}
type TouchLog = { down: number; events: string[]; changedWhileDown: number; offeredWhileDown: number; heights: number[] }
const touchLog = (page: Page) => page.evaluate(() => (window as unknown as { __touch: TouchLog }).__touch)
// The measured numbers, beside the test's screenshots (the evidence for the ticket).
function note(info: TestInfo, line: string) { appendFileSync(info.outputPath('aeon-1279-numbers.md'), `- ${info.title}: ${line}\n`) }

// ---------- Agents: a long Sessions list, live ----------
async function agentsPage(page: Page, theme: 'light' | 'dark' = 'light') {
  await page.clock.install({ time: NOW })
  const work = fixtures(); work.preferences.theme = { choice: theme }
  work.preferences['ui.agents.sections'] = { dial: false }
  work.preferences['projects:order'] = { ids: ['p-pharos', 'p-aeon', 'p-frozen'] }
  await mockWork(page, work, { admin: true })
  const data = agentData({ now: NOW, me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' } })
  // Enough working sessions in Aeon that the list scrolls several screens on a phone.
  const worker = data.sessions[2]!
  for (let i = 0; i < 36; i++) data.sessions.push({ ...worker, id: sid(500 + i), display_label: `Worker ${i + 1}`, run_id: null, ticket_node_id: null, ticket: null, created_at: ago(20 + i), heartbeat_at: ago(0.2) })
  await mockAgents(page, data)
  return data
}
// New workers in Pharos, the first group: they arrive above the rows on screen.
function arrive(data: AgentData) {
  ARRIVALS.forEach((id, n) => data.sessions.push({ ...data.sessions[1]!, id, display_label: `Arrival ${n + 1}`, run_id: null, ticket_node_id: null, ticket: null, created_at: new Date(NOW + (n + 1) * 1000).toISOString(), heartbeat_at: new Date(NOW + 1000).toISOString() }))
}
const SESSION_ROWS = '.sessions .row[data-row]'
const SESSION_HEADS = '.sessions .th-stick, .sessions .thead'
const sessionRow = (ref: string) => `.sessions ${ref}`
// The held note's Show now face (its unseen sizing twin carries .held-face too).
const SHOW_NOW = '.sessions [data-held-note] button.held-face:not(.off)'
const arrivalRow = (n: number) => sessionRow(`[data-row="s:${ARRIVALS[n]}"]`)
async function openAgents(page: Page, theme: 'light' | 'dark' = 'light') {
  const data = await agentsPage(page, theme)
  await page.goto('/agents')
  await expect.poll(() => page.locator(SESSION_ROWS).count()).toBeGreaterThan(40)
  await asOnSafari(page)
  // Read deep into the list: the Pharos group is well above the screen.
  await page.locator('main').evaluate(el => { el.scrollTop = el.scrollHeight - el.clientHeight - 900 })
  await page.clock.runFor(400)
  return { data, cdp: await page.context().newCDPSession(page) }
}

test('agents 390: a refresh during a touch swipe waits until the swipe settles, then lands without moving the rows on screen', async ({ page }, info) => {
  const errors = watchErrors(page)
  const { data, cdp } = await openAgents(page)
  await watchTouch(page, SESSION_ROWS)
  arrive(data)
  // Keep the native touch down across the response barrier. A timed synthesizeScrollGesture
  // can finish before polling notices its cancellation, especially in Linux Chromium.
  const top = await page.locator('main').evaluate(el => el.scrollTop)
  await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x: 195, y: 600, id: 0 }] })
  for (const y of [580, 560, 520, 480]) {
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x: 195, y, id: 0 }] })
    await page.clock.runFor(32)
  }
  await expect.poll(() => page.locator('main').evaluate(el => el.scrollTop)).toBeGreaterThan(top)
  // The browser takes the scroll over from the page (pointercancel); the next refresh lands with the finger down.
  await expect.poll(async () => (await touchLog(page)).events.join(' ')).toContain('pointercancel:touch')
  const refreshed = page.waitForResponse(response => new URL(response.url()).pathname === '/api/harness-sessions')
  await page.clock.runFor(21_000)
  await refreshed
  const during = await touchLog(page)
  expect(during.down, 'the refresh is proven to overlap the native touch').toBe(1)
  await expect(page.locator(arrivalRow(0))).toHaveCount(0)
  const settled = (await firstOnScreen(page, SESSION_ROWS, SESSION_HEADS))!
  await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] })
  const log = await touchLog(page)
  note(info, `finger down when the refresh landed: ${during.down > 0}; row changes while a finger was down: ${log.changedWhileDown}; Show now offered while down: ${log.offeredWhileDown}; events: ${log.events.filter(e => !e.startsWith('touchmove')).join(' ')}`)
  expect(log.changedWhileDown, 'no row arrives, leaves or moves while a finger is on the screen').toBe(0)
  expect(log.offeredWhileDown, 'a swipe never offers Show now: its hold ends by itself').toBe(0)
  // The swipe has settled: the change lands and the first row on screen stays where it is.
  await page.clock.runFor(2_000)
  await expect(page.locator(arrivalRow(0))).toBeAttached()
  const after = await topOf(page, sessionRow(settled.ref))
  note(info, `first row on screen ${settled.ref} at ${settled.top.toFixed(1)} px before the change, ${after.toFixed(1)} px after (Δ ${(after - settled.top).toFixed(1)})`)
  expect(await topOf(page, arrivalRow(0)), 'the arrivals sit above the rows on screen').toBeLessThan(settled.edge)
  expect(Math.abs(after - settled.top), 'the first row on screen keeps its place').toBeLessThanOrEqual(0.5)
  expect(errors).toEqual([])
})

test('agents 390: a refresh after one 40 px swipe lands without moving the rows on screen; touch shows no hold', async ({ page }, info) => {
  const { data, cdp } = await openAgents(page)
  await watchTouch(page, SESSION_ROWS)
  await swipe(page, cdp, { x: 195, y: 600 }, -40)
  note(info, `events: ${(await touchLog(page)).events.join(' ')}`)
  // A finger is not a pointer that rests on the list: nothing waits behind Show now.
  await expect(page.locator(SHOW_NOW)).toHaveCount(0)
  arrive(data)
  const before = (await firstOnScreen(page, SESSION_ROWS, SESSION_HEADS))!
  await page.clock.runFor(21_000)
  await expect(page.locator(arrivalRow(0))).toBeAttached()
  const after = await topOf(page, sessionRow(before.ref))
  note(info, `first row on screen ${before.ref} at ${before.top.toFixed(1)} px before the refresh, ${after.toFixed(1)} px after (Δ ${(after - before.top).toFixed(1)})`)
  expect(await topOf(page, arrivalRow(0)), 'the arrivals sit above the rows on screen').toBeLessThan(before.edge)
  expect(Math.abs(after - before.top), 'the first row on screen keeps its place').toBeLessThanOrEqual(0.5)
  await expect(page.locator(SHOW_NOW)).toHaveCount(0)
})

for (const theme of ['light', 'dark'] as const) {
  test(`${theme} 390: the footer folds away over the page; the page's scroller never changes size while scrolling`, async ({ page }, info) => {
    const { cdp } = await openAgents(page, theme)
    // Reading mid-page with the footer showing: back up a little after the jump down folded it.
    await page.clock.runFor(400)
    await page.locator('main').evaluate(el => { el.scrollTop -= 80 })
    await watchTouch(page, SESSION_ROWS)
    const footer = page.locator('.app-footer')
    await expect(footer).not.toHaveClass(/hidden/)
    await expect(footer).toBeInViewport()
    // Showing it starts a 260 ms cooldown. Advance the injected clock before reversing again.
    await page.clock.runFor(400)
    const before = await rectOf(page, 'main')
    await page.screenshot({ path: info.outputPath(`agents-390-${theme}-footer-shown.png`) })
    await swipe(page, cdp, { x: 195, y: 600 }, -200)
    await expect(footer).toHaveClass(/hidden/)
    await expect(footer).not.toBeInViewport()
    const folded = await rectOf(page, 'main')
    const { heights } = await touchLog(page)
    note(info, `main ${before.height} px before, ${folded.height} px with the footer folded; heights seen while scrolling: ${[...new Set(heights)].join(', ')}`)
    expect(folded, 'the scroller keeps its size when the footer folds away').toEqual(before)
    expect([...new Set(heights)], 'the scroller never changes size while scrolling').toEqual([before.height])
    await page.screenshot({ path: info.outputPath(`agents-390-${theme}-footer-folded.png`) })
    // Back up a little: the footer returns over the page, and the page still keeps its size.
    await page.clock.runFor(400)
    await swipe(page, cdp, { x: 195, y: 300 }, 80)
    await expect(footer).not.toHaveClass(/hidden/)
    await expect(footer).toBeInViewport()
    expect(await rectOf(page, 'main')).toEqual(before)
    // At the very end the last row clears the footer.
    await page.locator('main').evaluate(el => el.scrollTo({ top: el.scrollHeight }))
    await expect(footer).toBeInViewport()
    const last = await page.locator(SESSION_ROWS).last().evaluate(el => el.getBoundingClientRect().bottom)
    expect(last).toBeLessThanOrEqual((await footer.boundingBox())!.y + 0.5)
  })
}

// ---------- Ticket list: a long project list, live from a second browser ----------
async function phoneAndDesk(browser: Browser) {
  const data = fixtures({ bigProject: 50 })
  const live = liveServer(data)
  const contexts = [await browser.newContext({ viewport: PHONE, hasTouch: true, isMobile: true }), await browser.newContext({ viewport: { width: 1280, height: 900 } })]
  const [phone, desk] = await Promise.all(contexts.map(context => context.newPage()))
  for (const [page, name] of [[phone, 'a'], [desk, 'b']] as const) { await mockWork(page!, data, { nativeEvents: true }); await live.install(page!, name) }
  await Promise.all([phone!.goto('/p/AEON/tickets'), desk!.goto('/p/AEON/tickets')])
  await expect.poll(() => new Set(live.requests.map(r => r.page)).size).toBe(2)
  return { phone: phone!, desk: desk!, close: () => Promise.all(contexts.map(context => context.close())) }
}
const TICKET_ROWS = 'tr.ticket-row[id^="row-"]'
const TICKET_HEADS = '.toolbar-wrap, .table-card thead th, .group-row th'

test('ticket list 390: a swipe never resizes the page; a ticket closed elsewhere leaves without moving the rows on screen', async ({ browser }, info) => {
  const { phone, desk, close } = await phoneAndDesk(browser)
  try {
    const errors = watchErrors(phone)
    await expect.poll(() => phone.locator(TICKET_ROWS).count()).toBeGreaterThan(40)
    await asOnSafari(phone)
    const cdp = await phone.context().newCDPSession(phone)
    const top = await phone.locator(TICKET_ROWS).first().evaluate(el => ({ id: el.id.slice(4), key: el.querySelector('.key-btn')?.textContent?.trim() ?? '' }))
    await phone.locator('main').evaluate(el => { el.scrollTop = 1200 })
    await watchTouch(phone, TICKET_ROWS, '.table-card')
    const before = await rectOf(phone, 'main')
    await swipe(phone, cdp, { x: 195, y: 600 }, -200)
    await expect(phone.locator('.app-footer')).toHaveClass(/hidden/)
    const { heights } = await touchLog(phone)
    note(info, `main ${before.height} px; heights seen while scrolling: ${[...new Set(heights)].join(', ')}`)
    expect([...new Set(heights)], 'the scroller never changes size while scrolling').toEqual([before.height])
    expect(await rectOf(phone, 'main')).toEqual(before)
    // The top ticket, far above the screen, is closed on the desk; it leaves once the phone is idle.
    const anchor = (await firstOnScreen(phone, TICKET_ROWS, TICKET_HEADS))!
    await desk.locator(`#row-${top.id} .status-btn`).click()
    await desk.getByRole('menu', { name: `Status of ${top.key}` }).getByRole('menuitemradio', { name: 'Cancelled' }).click()
    await expect(phone.locator(`#row-${top.id}`)).toHaveCount(0, { timeout: 8000 })
    const after = await topOf(phone, anchor.ref)
    note(info, `first row on screen ${anchor.ref} at ${anchor.top.toFixed(1)} px before ${top.key} left, ${after.toFixed(1)} px after (Δ ${(after - anchor.top).toFixed(1)})`)
    expect(Math.abs(after - anchor.top), 'the first row on screen keeps its place').toBeLessThanOrEqual(0.5)
    await phone.screenshot({ path: info.outputPath('tickets-390-light.png') })
    expect(errors).toEqual([])
  } finally { await close() }
})

// ---------- Chat: the open session's thread on a phone ----------
async function chatPage(page: Page) {
  await page.clock.install({ time: NOW })
  const work = fixtures(); work.preferences.theme = { choice: 'light' }
  await mockWork(page, work, { admin: true })
  const data = agentData({ now: NOW, me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' } })
  const worker = data.sessions[0]!
  Object.assign(worker, { display_label: 'release-lead', run_id: null, activity: 'idle' })
  data.sessions.splice(1); data.runs.splice(0); data.approvals.splice(0)
  const template = data.messages[1]!
  const bodies = ['Picked up the release checks. Running the session chat suite first.', 'Good. Keep the fix small and ship it behind the release gate.', 'Counts are in. Should stale hosts sort last, or keep their position with a muted row?']
  const messages = Array.from({ length: 30 }, (_, i) => {
    const mine = i % 3 === 2
    return {
      ...template, id: `3e000000-0000-4000-8000-${String(100 + i).padStart(12, '0')}`, sent_event_id: 200 + i, body: bodies[i % bodies.length]!, created_at: new Date(NOW - (30 - i) * 90_000).toISOString(),
      ...(mine
        ? { sender_principal_id: me.id, recipient_principal_id: worker.agent_principal_id, to: 'claude:camy', recipient_session_id: worker.id, sender_session_id: undefined, sender_label: 'Markus' }
        : { sender_principal_id: worker.agent_principal_id, recipient_principal_id: me.id, to: 'paimos:markus', sender_session_id: worker.id, sender_label: 'release-lead' }),
    } as typeof template
  })
  data.messages.splice(0, data.messages.length, ...messages)
  await mockAgents(page, data)
  await page.route('**/api/inbox/message-status?*', route => route.fulfill({ json: { items: [] } }))
  return { data, worker, messages }
}

test('chat 390: a post that arrives while reading up the thread never moves the posts on screen or the sheet', async ({ page }, info) => {
  const errors = watchErrors(page)
  const { data, worker, messages } = await chatPage(page)
  await page.goto(`/agents/${worker.id}?tab=messages`)
  const thread = page.locator('.session-panel .thread-scroll')
  await expect(thread.locator('.msg').last()).toBeInViewport()
  await page.clock.runFor(400)
  await asOnSafari(page)
  const cdp = await page.context().newCDPSession(page)
  const box = (await thread.boundingBox())!
  const bottom = await thread.evaluate(el => el.scrollTop)
  // Read up the thread with a finger.
  await swipe(page, cdp, { x: box.x + box.width / 2, y: box.y + 80 }, 300, '.session-panel .thread-scroll')
  await expect.poll(() => thread.evaluate(el => el.scrollTop), 'the native swipe actually leaves the bottom').toBeLessThan(bottom - 160)
  const sheet = await rectOf(page, '.session-panel')
  const mainTop = await page.locator('main').evaluate(el => el.scrollTop)
  const before = (await firstOnScreen(page, '.session-panel .thread-scroll .msg[data-id]', '.session-panel .thread-scroll .never', '.session-panel .thread-scroll'))!
  data.messages.push({ ...messages.at(-3)!, id: '3e000000-0000-4000-8000-000000000999', sent_event_id: 999, body: 'Arrives while you read older posts.', created_at: new Date(NOW + 1000).toISOString() })
  await page.clock.runFor(21_000)
  await expect(page.locator('.session-panel').getByText('Arrives while you read older posts.')).toBeAttached()
  const after = await topOf(page, `.session-panel .thread-scroll .msg${before.ref}`)
  note(info, `first post on screen at ${before.top.toFixed(1)} px before the arrival, ${after.toFixed(1)} px after (Δ ${(after - before.top).toFixed(1)}); sheet ${sheet.height} px`)
  expect(Math.abs(after - before.top), 'the first post on screen keeps its place').toBeLessThanOrEqual(0.5)
  expect(await rectOf(page, '.session-panel'), 'the sheet keeps its size').toEqual(sheet)
  expect(await page.locator('main').evaluate(el => el.scrollTop), 'the page behind the sheet never scrolls').toBe(mainTop)
  await page.screenshot({ path: info.outputPath('chat-390-light.png') })
  expect(errors).toEqual([])
})
