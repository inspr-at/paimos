// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1473 (Tick's decisions 1–5): the Decision Desk count in the browser tab.
// Risk: the tab shows another count than the header badge, misses a change, moves
// when nothing new arrived or under reduced motion, takes focus, or marks the tab
// of an agent or a signed-out visitor. Time runs on Playwright's clock.
import { expect, test, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { dotLayout, NEW_DECISION } from '../src/lib/deskTab'

async function setup(page: Page, options: { open?: number; principalKind?: 'person' | 'agent' } = {}) {
  const desk = { open: options.open ?? 0, reads: 0 }
  await mockWork(page, fixtures(), options.principalKind ? { principalKind: options.principalKind } : {})
  await page.route('**/api/decision-desk/projection**', route => {
    desk.reads++
    const items = Array.from({ length: desk.open }, (_, n) => ({ id: `q-${n}`, kind: 'question', project_id: 'p-aeon', revision: 1, title: `Question ${n + 1}`, created_at: '2026-10-17T08:00:00Z', held: false, href: `/decision-desk?item=q:q-${n}`, source: `/api/questions/q-${n}` }))
    return route.fulfill({ json: { items, counts: { open: desk.open, held: 0, chores: 0 }, has_more: false, as_of: new Date().toISOString() } })
  })
  return desk
}
// Coming back online reads the Desk again at once, as the header's poll does.
async function readAgain(page: Page) {
  const read = page.waitForResponse(response => new URL(response.url()).pathname === '/api/decision-desk/projection')
  await page.evaluate(() => window.dispatchEvent(new Event('online')))
  await read
}
const badge = (page: Page) => page.locator('.places [data-place="desk"] .needs-badge').first()
const icon = (page: Page) => page.locator('link[rel~="icon"]')
// Every favicon and title the tab shows, in order, stamped with the page's (installed) clock.
interface TabEntry { at: number; value: string }
async function recordTab(page: Page) {
  await page.evaluate(() => {
    const log = { icons: [] as TabEntry[], titles: [] as TabEntry[] }
    ;(window as unknown as { tabLog: typeof log }).tabLog = log
    const link = document.querySelector('link[rel~="icon"]')!
    new MutationObserver(() => log.icons.push({ at: Date.now(), value: link.getAttribute('href') ?? '' })).observe(link, { attributes: true, attributeFilter: ['href'] })
    new MutationObserver(() => log.titles.push({ at: Date.now(), value: document.title })).observe(document.querySelector('title')!, { childList: true, characterData: true, subtree: true })
  })
}
const tabLog = (page: Page) => page.evaluate(() => (window as unknown as { tabLog: { icons: TabEntry[]; titles: TabEntry[] } }).tabLog)
const noticeShown = (log: { titles: TabEntry[] }) => log.titles.filter(entry => entry.value === NEW_DECISION)
// The page's clock starts at T0 and is paused once the page has loaded: from then on
// timers (pop frames, the notice, the background reader) fire only through runFor.
const T0 = Date.parse('2026-10-17T10:00:00Z')
async function pauseClock(page: Page) { await page.clock.pauseAt(T0 + 20_000) }
// A background tab reads the Desk once its last answer is a minute old. Jump the paused
// clock past that minute in one step (timers due in between fire once, as after a closed
// laptop lid); the header's poll wakes and cancels the store's read too, and the read it
// drops is asked for again. Nothing after the read (the two-second notice) runs until runFor.
async function backgroundRead(page: Page, desk: { reads: number }) {
  const before = desk.reads
  await page.clock.fastForward(75_000)
  await expect.poll(() => desk.reads).toBeGreaterThan(before)
}
async function hideTab(page: Page) {
  await page.evaluate(() => {
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' })
    Object.defineProperty(document, 'hidden', { configurable: true, get: () => true })
    document.dispatchEvent(new Event('visibilitychange'))
  })
}
async function watchFocusAndSound(page: Page) {
  await page.addInitScript(() => {
    const calls = { focus: 0, sound: 0 }
    ;(window as unknown as { tabCalls: typeof calls }).tabCalls = calls
    const focus = window.focus.bind(window)
    window.focus = () => { calls.focus++; focus() }
    const play = HTMLMediaElement.prototype.play
    HTMLMediaElement.prototype.play = function (this: HTMLMediaElement) { calls.sound++; return play.call(this) }
    const Audio = window.AudioContext
    if (Audio) window.AudioContext = class extends Audio { constructor(...args: ConstructorParameters<typeof AudioContext>) { super(...args); calls.sound++ } }
  })
}

test('AEON-1473: every page leads with the badge’s count: none at 0, (1), (9), then (9+)', async ({ page }) => {
  const desk = await setup(page, { open: 0 })
  await page.goto('/')
  await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
  await expect(badge(page)).toHaveText('0')
  await expect(page).toHaveTitle(/^Projects · /)
  for (const [open, title, shown] of [[1, /^\(1\) Projects · /, '1'], [9, /^\(9\) Projects · /, '9'], [10, /^\(9\+\) Projects · /, '10'], [0, /^Projects · /, '0']] as const) {
    desk.open = open
    await readAgain(page)
    // Within one second of the projection changing.
    await expect(page).toHaveTitle(title, { timeout: 1000 })
    await expect(badge(page)).toHaveText(shown)
  }
  desk.open = 3
  await readAgain(page)
  await expect(page).toHaveTitle(/^\(3\) Projects · /, { timeout: 1000 })
  await page.goto('/p/PHAROS/PHAROS-11')
  await expect(page).toHaveTitle(/^\(3\) .*PHAROS-11.* · /)
})

test('AEON-1473: a rise pops the favicon dot once in six frames; the visible title never swaps', async ({ page }) => {
  // The UI config reduces motion by default; this case is about the motion.
  await page.emulateMedia({ reducedMotion: 'no-preference' })
  await page.clock.install({ time: T0 })
  const desk = await setup(page, { open: 2 })
  await page.goto('/')
  await expect(page).toHaveTitle(/^\(2\) Projects · /)
  await expect(icon(page)).toHaveAttribute('data-desk-count', '2')
  await expect(icon(page)).toHaveAttribute('type', 'image/png')
  await pauseClock(page)
  await recordTab(page)
  const places = await page.getByRole('navigation', { name: 'Places' }).boundingBox()
  desk.open = 3
  await readAgain(page)
  await expect(page).toHaveTitle(/^\(3\) Projects · /, { timeout: 1000 })
  await page.clock.runFor(2000)
  const log = await tabLog(page)
  expect(log.icons, 'six frames, then nothing loops').toHaveLength(6)
  expect(log.icons[2].value, 'the largest frame differs from the resting dot').not.toBe(log.icons[5].value)
  expect(log.icons.map(entry => entry.at - log.icons[0].at), 'six frames of 100 ms').toEqual([0, 100, 200, 300, 400, 500])
  expect(noticeShown(log)).toHaveLength(0)
  await expect(icon(page)).toHaveAttribute('data-desk-count', '3')
  const after = await page.getByRole('navigation', { name: 'Places' }).boundingBox()
  for (const key of ['x', 'y', 'width', 'height'] as const) expect(Math.abs(after![key] - places![key]), `header places ${key}`).toBeLessThanOrEqual(0.5)
  // A fall is shown at rest.
  desk.open = 1
  await readAgain(page)
  await page.clock.runFor(2000)
  expect((await tabLog(page)).icons).toHaveLength(7)
})

test('AEON-1473: a background tab reads the Desk itself and says New decision for two seconds, without focus or sound', async ({ page }) => {
  await watchFocusAndSound(page)
  await page.emulateMedia({ reducedMotion: 'no-preference' })
  await page.clock.install({ time: T0 })
  const desk = await setup(page, { open: 1 })
  await page.goto('/')
  await expect(page).toHaveTitle(/^\(1\) Projects · /)
  await expect(icon(page)).toHaveAttribute('data-desk-count', '1')
  await pauseClock(page)
  await recordTab(page)
  await hideTab(page)
  // The header's poll stops in a background tab and cancels the store's read; the count it read stays.
  await page.clock.runFor(5000)
  await expect(page).toHaveTitle(/^\(1\) Projects · /)
  desk.open = 2
  await backgroundRead(page, desk)
  await expect(page).toHaveTitle(NEW_DECISION, { timeout: 1000 })
  await page.clock.runFor(1999)
  await expect(page).toHaveTitle(NEW_DECISION)
  await page.clock.runFor(1)
  await expect(page).toHaveTitle(/^\(2\) Projects · /)
  const log = await tabLog(page)
  const notice = log.titles.findIndex(entry => entry.value === NEW_DECISION)
  expect(log.titles[notice + 1].at - log.titles[notice].at, 'two seconds').toBe(2000)
  expect(log.icons, 'the dot changes once: a hidden tab gets no frames').toHaveLength(1)
  await expect(icon(page)).toHaveAttribute('data-desk-count', '2')
  expect(noticeShown(log)).toHaveLength(1)
  expect(await page.evaluate(() => (window as unknown as { tabCalls: { focus: number; sound: number } }).tabCalls)).toEqual({ focus: 0, sound: 0 })
})

test('AEON-1473: reduced motion changes only the number, visible or in the background', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await page.clock.install({ time: T0 })
  const desk = await setup(page, { open: 1 })
  await page.goto('/')
  await expect(page).toHaveTitle(/^\(1\) Projects · /)
  await expect(icon(page)).toHaveAttribute('data-desk-count', '1')
  await pauseClock(page)
  await recordTab(page)
  desk.open = 2
  await readAgain(page)
  await expect(page).toHaveTitle(/^\(2\) Projects · /, { timeout: 1000 })
  await page.clock.runFor(2000)
  await hideTab(page)
  desk.open = 3
  await backgroundRead(page, desk)
  await expect(page).toHaveTitle(/^\(3\) Projects · /, { timeout: 1000 })
  await page.clock.runFor(3000)
  const log = await tabLog(page)
  expect(log.icons, 'one change per count, no frames').toHaveLength(2)
  expect(noticeShown(log)).toHaveLength(0)
})

test('AEON-1473: an agent principal and a signed-out visitor see no count and the plain icon', async ({ page }) => {
  const desk = await setup(page, { open: 4, principalKind: 'agent' })
  await page.goto('/')
  await expect(page).toHaveTitle(/^Projects · /)
  await expect(page.getByRole('navigation', { name: 'Places' })).toBeVisible()
  await expect(icon(page)).toHaveAttribute('href', '/brand/aeon-icon.svg')
  await expect(icon(page)).not.toHaveAttribute('data-desk-count', /.*/)
  expect(desk.reads).toBe(0)

  await page.unrouteAll({ behavior: 'ignoreErrors' })
  await page.route('**/api/**', route => {
    const path = new URL(route.request().url()).pathname
    if (path === '/api/me') return route.fulfill({ status: 401, json: { error: 'unauthorized', dev_mode: false } })
    if (path === '/api/decision-desk/projection') return route.fulfill({ json: { items: [], counts: { open: 4, held: 0, chores: 0 }, has_more: false } })
    return route.fulfill({ status: 404, json: { error: 'Unmocked route' } })
  })
  await page.goto('/signin')
  await expect(page.getByRole('heading', { name: 'Sign in', level: 1 })).toBeVisible()
  await expect(page).toHaveTitle(/^Sign in · /)
  await expect(icon(page)).toHaveAttribute('href', '/brand/aeon-icon.svg')
})

// The tab strip draws the favicon at 16 CSS px: 16 device px on a plain screen, 32 on a
// Retina one. Light and dark strips are Chrome's default tab colours.
const STRIPS = { light: '#ffffff', dark: '#35363a' } as const
for (const scale of [1, 2] as const) test.describe(`${16 * scale} px favicon`, () => {
  test.use({ deviceScaleFactor: scale })
  test(`AEON-1473: the amber dot reads at ${16 * scale} px on light and dark tab strips`, async ({ page }, testInfo) => {
    const size = 16 * scale
    const desk = await setup(page, { open: 3 })
    await page.goto('/')
    await expect(icon(page)).toHaveAttribute('data-desk-count', '3')
    const href = (await icon(page).getAttribute('href'))!
    expect(href.startsWith('data:image/png')).toBe(true)
    const dot = dotLayout(size, 3)!
    expect(dot.label, size < 24 ? 'a plain dot when too small for a digit' : 'the digit').toBe(size < 24 ? '' : '3')
    for (const [strip, background] of Object.entries(STRIPS)) {
      const sample = await page.evaluate(async ({ href, size, background, dot }) => {
        const image = new Image(); image.src = href; await image.decode()
        const canvas = document.createElement('canvas'); canvas.width = canvas.height = size
        const ctx = canvas.getContext('2d')!
        ctx.fillStyle = background; ctx.fillRect(0, 0, size, size); ctx.drawImage(image, 0, 0)
        const px = (x: number, y: number) => [...ctx.getImageData(Math.floor(x), Math.floor(y), 1, 1).data.slice(0, 3)]
        const cx = dot.x + dot.width / 2, cy = dot.y + dot.height / 2, r = dot.height / 2
        // Inside the dot, left of any digit; the gap cut between the dot and the icon; ink pixels of the digit.
        const fill = px(cx - r * 0.55, cy)
        const gap = px(cx - r - dot.gap / 2, cy)
        let ink = 0
        for (let y = Math.ceil(cy - r * 0.5); y < cy + r * 0.5; y++) for (let x = Math.ceil(cx - r * 0.3); x < cx + r * 0.3; x++) { const [red, green, blue] = px(x, y); if (red + green + blue < 300) ink++ }
        return { natural: image.naturalWidth, fill, gap, ink, strip: px(0, size - 1) }
      }, { href, size, background, dot })
      expect(sample.natural).toBe(size)
      const near = (a: number[], b: number[], tolerance: number) => a.every((v, i) => Math.abs(v - b[i]) <= tolerance)
      expect(near(sample.fill, [0xe8, 0xc0, 0x7a], 24), `${strip}: the dot is the badge's amber, got ${sample.fill}`).toBe(true)
      expect(Math.max(...sample.fill.map((v, i) => Math.abs(v - sample.strip[i]))), `${strip}: the dot stands apart from the strip`).toBeGreaterThan(60)
      if (size >= 24) expect(sample.ink, `${strip}: the digit is drawn`).toBeGreaterThan(0)
      else expect(sample.ink, `${strip}: no digit at 16 px`).toBe(0)
      expect(near(sample.gap, sample.strip, 40), `${strip}: a gap separates the dot from the icon, got ${sample.gap}`).toBe(true)
    }
    // Evidence for the visual check: pinned (icon only) and normal tabs on both strips.
    await page.evaluate(({ href, strips, title }) => {
      const board = document.createElement('div')
      board.id = 'tab-strips'
      board.style.cssText = 'position:fixed;inset:0 auto auto 0;z-index:99999;display:grid;gap:0;font:12px system-ui;background:#888'
      for (const [name, background] of Object.entries(strips)) {
        const row = document.createElement('div')
        row.style.cssText = `display:flex;align-items:center;gap:8px;padding:8px 10px;background:${background};color:${name === 'dark' ? '#e8eaed' : '#202124'}`
        for (const pinned of [true, false]) {
          const tab = document.createElement('div')
          tab.style.cssText = 'display:flex;align-items:center;gap:8px;padding:6px 10px;border-radius:8px'
          const img = document.createElement('img'); img.src = href; img.width = img.height = 16
          tab.append(img)
          if (!pinned) tab.append(Object.assign(document.createElement('span'), { textContent: title }))
          row.append(tab)
        }
        board.append(row)
      }
      document.body.append(board)
    }, { href, strips: STRIPS, title: await page.title() })
    await page.locator('#tab-strips').screenshot({ path: testInfo.outputPath(`aeon-1473-tab-strips-${size}px.png`) })
    expect(desk.reads).toBeGreaterThan(0)
  })
})
