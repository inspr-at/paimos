// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1005 (AEON-994 package 5): Delivery › Flow lanes. Risks: zooming, panning or
// moving the time moves a control or a lane; a click moves the time; the playhead
// cannot be dragged or stepped; wheel input hijacks the page's vertical scroll.
import { expect, test, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { controlStability } from './control-stability'
import { deliveryMetrics, mockDelivery } from './delivery-numbers-fixtures'
import { mockFlow } from './delivery-flow-fixtures'

async function setup(page: Page, options: { theme?: 'light' | 'dark'; lang?: 'en' | 'de' } = {}) {
  const work = fixtures()
  work.preferences.theme = { choice: options.theme ?? 'light' }
  work.preferences['delivery:numbers'] = { level: 'simple', window: 7 }
  await mockWork(page, work)
  if (options.lang === 'de') { const data = settingsData(); data.profile.locale = 'de-AT'; await mockSettings(page, data) }
  await mockDelivery(page, async () => ({ status: 200, body: deliveryMetrics() }), ['delivery.read'])
  // No recorded run yet: Flow shows the labelled example (AEON-1006 reads the recorded runs).
  await mockFlow(page, { empty: true })
}
const lanes = (page: Page) => page.getByTestId('flow-lanes')
const overview = (page: Page) => page.getByTestId('flow-overview')
const clock = (page: Page) => page.getByTestId('flow-clock')
const chip = (page: Page) => page.getByTestId('flow-chip').locator('.shown')
const followLabel = (page: Page) => page.getByTestId('flow-follow').locator('.shown')
const windowText = (page: Page) => overview(page).getAttribute('aria-valuetext')
const minuteOf = (text: string | null) => { const [h, m] = (text ?? '').match(/(\d\d):(\d\d)/)!.slice(1).map(Number); return h! * 60 + m! }
async function center(page: Page, selector: string) {
  const box = (await page.locator(selector).first().boundingBox())!
  return { x: box.x + box.width / 2, y: box.y + box.height / 2 }
}

test('lanes zoom, pan and move the time without moving a control, and a click never moves the time', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1100 })
  await setup(page)
  await page.goto('/p/AEON/delivery?view=flow')
  await expect(page.getByRole('status')).toContainText('No flow data recorded yet. Until then, release 126 from 8 Oct is shown as an example.')
  const bar = page.getByTestId('flow-bar')
  const zoom = bar.getByRole('radiogroup', { name: 'Zoom' })
  const follow = page.getByTestId('flow-follow')
  // Live opens at 45 min with now (20:25) at 65 %, following now; no preset matches 45 min.
  await expect(clock(page)).toHaveText('At 20:25 (now)')
  await expect(chip(page)).toHaveText('Live · now 20:25')
  await expect(follow).toBeDisabled()
  await expect(followLabel(page)).toHaveText('Following now')
  await expect(zoom.getByRole('radio', { checked: true })).toHaveCount(0)
  await expect.poll(() => windowText(page)).toBe('19:55 – 20:40')
  // Six lanes, rows reserved once: the lane rectangles never move while zooming.
  await expect(lanes(page).locator('.ln-lane')).toHaveCount(6)
  const laneBoxes = async () => lanes(page).locator('.ln-lane').evaluateAll(els => els.map(el => { const r = el.getBoundingClientRect(); return [r.y, r.height] }))
  const rowsBefore = await laneBoxes()

  const guard = await controlStability(page, {
    chip: page.getByTestId('flow-chip'), fit: zoom.getByRole('radio', { name: 'Fit all' }), z15: zoom.getByRole('radio', { name: '15 min' }), z60: zoom.getByRole('radio', { name: '1 h' }),
    follow, readout: page.getByTestId('flow-readout'), overview: overview(page), lanes: lanes(page),
  })
  await guard.check(() => zoom.getByRole('radio', { name: '15 min' }).click())
  await expect(zoom.getByRole('radio', { name: '15 min' })).toHaveAttribute('aria-checked', 'true')
  await expect.poll(() => windowText(page)).toBe('20:15 – 20:30')
  await guard.check(() => zoom.getByRole('radio', { name: 'Fit all' }).click())
  await expect.poll(() => windowText(page)).toBe('18:10 – 21:40')
  // A "+N" cluster of slivers zooms in on click.
  const clusters = lanes(page).locator('[data-cluster]')
  await expect(clusters.first()).toBeVisible()
  await guard.check(async () => { const c = await center(page, '[data-testid="flow-lanes"] [data-cluster] .ln-cluster'); await page.mouse.click(c.x, c.y) })
  await expect(zoom.getByRole('radio', { checked: true })).toHaveCount(0)
  await guard.check(() => zoom.getByRole('radio', { name: '30 min' }).click())
  await expect.poll(async () => { const w = await windowText(page); return minuteOf(w?.split('–')[1] ?? null) - minuteOf(w) }).toBe(30)
  await expect(clock(page)).toHaveText('At 20:25 (now)')
  expect(await laneBoxes()).toEqual(rowsBefore)

  // ⌘/Ctrl+wheel zooms around the pointer; a sideways wheel pans; a plain wheel is the page's.
  const box = (await lanes(page).boundingBox())!
  await page.mouse.move(box.x + box.width * 0.6, box.y + 60)
  await guard.check(async () => { await page.keyboard.down('Control'); await page.mouse.wheel(0, -300); await page.keyboard.up('Control') })
  const zoomed = await windowText(page)
  expect(minuteOf(zoomed?.split('–')[1] ?? null) - minuteOf(zoomed)).toBeLessThan(30)
  await guard.check(() => page.mouse.wheel(300, 0))
  expect(await windowText(page)).not.toBe(zoomed)
  const panned = await windowText(page)
  await page.evaluate(() => { const seen: boolean[] = []; Object.assign(window, { wheelSeen: seen }); window.addEventListener('wheel', e => seen.push(e.defaultPrevented)) })
  await page.mouse.wheel(0, 200)
  await expect.poll(() => page.evaluate(() => (window as unknown as { wheelSeen: boolean[] }).wheelSeen)).toEqual([false])
  expect(await windowText(page)).toBe(panned)
  await page.evaluate(() => window.scrollTo(0, 0))
  await expect(clock(page)).toHaveText('At 20:25 (now)')

  // Dragging empty lane space pans; the time stays.
  await guard.check(() => zoom.getByRole('radio', { name: '1 h' }).click())
  const before = await windowText(page)
  const you = (await lanes(page).locator('.ln-lane[data-lane="you"]').boundingBox())!
  await guard.check(async () => {
    await page.mouse.move(you.x + 170, you.y + you.height / 2); await page.mouse.down()
    await page.mouse.move(you.x + 260, you.y + you.height / 2, { steps: 6 }); await page.mouse.up()
  })
  expect(await windowText(page)).not.toBe(before)
  await expect(clock(page)).toHaveText('At 20:25 (now)')
  await expect(followLabel(page)).toHaveText('Back to now')

  // A click selects a step (outline + readout) and never moves the time; Esc clears.
  await guard.check(() => zoom.getByRole('radio', { name: 'Fit all' }).click())
  await guard.check(async () => { const c = await center(page, '[data-step="0:c983:2"] .fl-hit'); await page.mouse.click(c.x, c.y) })
  await expect(page.getByTestId('flow-readout')).toHaveText('983–986 · Reviewed: OK · Reviewer (agent) · 19:50–20:14 (24 min) · Working')
  await expect(lanes(page).locator('.ln-selbox')).toHaveCount(1)
  await expect(clock(page)).toHaveText('At 20:25 (now)')
  await lanes(page).focus()
  await guard.check(() => page.keyboard.press('Escape'))
  await expect(lanes(page).locator('.ln-selbox')).toHaveCount(0)
  await expect(page.getByTestId('flow-readout')).toContainText('Drag the time handle to move the time')

  // Only the playhead moves the time: drag its pill, or Shift+arrows a minute at a time.
  const pill = await center(page, '[data-testid="flow-playhead"] .ln-phpill')
  await guard.check(async () => {
    await page.mouse.move(pill.x, pill.y); await page.mouse.down()
    await page.mouse.move(pill.x - 120, pill.y, { steps: 8 }); await page.mouse.up()
  })
  const dragged = minuteOf(await clock(page).textContent())
  expect(dragged).toBeLessThan(20 * 60 + 25)
  await expect(chip(page)).toHaveText(/^Viewing \d\d:\d\d$/)
  await expect(follow).toBeEnabled()
  await lanes(page).focus()
  await guard.check(() => page.keyboard.press('Shift+ArrowRight'))
  await expect.poll(async () => minuteOf(await clock(page).textContent())).toBe(dragged + 1)
  await guard.check(() => page.keyboard.press('Shift+ArrowLeft'))
  await guard.check(() => page.keyboard.press('Shift+ArrowLeft'))
  await expect.poll(async () => minuteOf(await clock(page).textContent())).toBe(dragged - 1)
  // Back to now: the time returns to now and following resumes.
  await guard.check(() => follow.click())
  await expect(clock(page)).toHaveText('At 20:25 (now)')
  await expect(follow).toBeDisabled()
  await expect(chip(page)).toHaveText('Live · now 20:25')
  guard.done()
})

test('panning an incident to the right edge keeps its caption inside the lane frame', async ({ page }) => {
  await page.setViewportSize({ width: 400, height: 900 })
  await setup(page)
  await page.goto('/p/AEON/delivery?view=flow')
  await expect.poll(() => windowText(page)).toBe('20:12 – 20:32')
  await lanes(page).focus()
  for (let i = 0; i < 8; i++) await page.keyboard.press('ArrowLeft')
  await expect.poll(() => windowText(page)).toBe('19:56 – 20:16')
  const caption = lanes(page).locator('.fl-inc-t')
  await expect(caption).toBeVisible()
  const frame = (await lanes(page).locator(':scope > svg').boundingBox())!
  const text = (await caption.boundingBox())!
  const dot = (await lanes(page).locator('.fl-inc-dot').boundingBox())!
  expect(dot.x).toBeGreaterThanOrEqual(frame.x - 0.5)
  expect(text.x).toBeGreaterThanOrEqual(frame.x - 0.5)
  expect(dot.x + dot.width).toBeLessThanOrEqual(frame.x + frame.width + 0.5)
  expect(text.x + text.width).toBeLessThanOrEqual(frame.x + frame.width + 0.5)
  await expect(lanes(page).locator('.sr-only')).toHaveText('Live, but not working properly · since 20:14')
})

test('the overview brush pans and resizes the window, and its playhead moves the time', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1100 })
  await setup(page)
  await page.goto('/p/AEON/delivery?view=flow')
  await expect.poll(() => windowText(page)).toBe('19:55 – 20:40')
  const brush = await center(page, '[data-testid="flow-overview"] .ln-brush')
  await page.mouse.move(brush.x, brush.y); await page.mouse.down(); await page.mouse.move(brush.x - 150, brush.y, { steps: 6 }); await page.mouse.up()
  const moved = await windowText(page)
  expect(minuteOf(moved)).toBeLessThan(19 * 60 + 55)
  expect(minuteOf(moved?.split('–')[1] ?? null) - minuteOf(moved)).toBe(45)
  await expect(clock(page)).toHaveText('At 20:25 (now)')
  const right = await center(page, '[data-testid="flow-overview"] [data-part="hr"]')
  await page.mouse.move(right.x, right.y); await page.mouse.down(); await page.mouse.move(right.x + 100, right.y, { steps: 6 }); await page.mouse.up()
  const wider = await windowText(page)
  expect(minuteOf(wider?.split('–')[1] ?? null) - minuteOf(wider)).toBeGreaterThan(45)
  // Keys on the overview move the window, not the time.
  await overview(page).focus()
  await page.keyboard.press('ArrowRight')
  expect(await windowText(page)).not.toBe(wider)
  await expect(clock(page)).toHaveText('At 20:25 (now)')
  const ph = await center(page, '[data-testid="flow-overview"] [data-part="ovph"]')
  await page.mouse.move(ph.x, ph.y); await page.mouse.down(); await page.mouse.move(ph.x - 200, ph.y, { steps: 6 }); await page.mouse.up()
  expect(minuteOf(await clock(page).textContent())).toBeLessThan(20 * 60)
})

test('at Fit all a phone overview keeps each 44 px handle inside and a drag at either edge resizes', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await setup(page)
  await page.goto('/p/AEON/delivery?view=flow')
  await page.getByTestId('flow-bar').getByRole('radio', { name: 'Fit all' }).click()
  await expect.poll(() => windowText(page)).toBe('18:10 – 21:40')
  await overview(page).scrollIntoViewIfNeeded()
  const before = await windowText(page)
  const geometry = await overview(page).evaluate(host => {
    const svg = host.querySelector('svg')!
    const frame = svg.getBoundingClientRect()
    const box = (id: string) => host.querySelector(`[data-testid="${id}"]`)!.getBoundingClientRect()
    const clip = (r: DOMRect) => ({
      w: Math.min(r.right, frame.right) - Math.max(r.left, frame.left),
      h: Math.min(r.bottom, frame.bottom) - Math.max(r.top, frame.top),
    })
    const area = (a: DOMRect, b: DOMRect) => {
      const w = Math.min(a.right, b.right) - Math.max(a.left, b.left)
      const h = Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top)
      return Math.max(0, w) * Math.max(0, h)
    }
    const hr = box('flow-overview-hr'), hl = box('flow-overview-hl'), ph = box('flow-overview-playhead')
    const x = frame.right - 30, y = frame.top + frame.height / 2
    return {
      frameW: frame.width,
      hl: clip(hl), hr: clip(hr),
      playOverlap: area(hr, ph) + area(hl, ph),
      rightPart: document.elementFromPoint(x, y)?.getAttribute('data-part') ?? '',
      x, y,
    }
  })
  expect(geometry.frameW).toBeGreaterThan(240)
  expect(geometry.frameW).toBeLessThan(640)
  expect(geometry.hl.w).toBeGreaterThanOrEqual(44)
  expect(geometry.hl.h).toBeGreaterThanOrEqual(44)
  expect(geometry.hr.w).toBeGreaterThanOrEqual(44)
  expect(geometry.hr.h).toBeGreaterThanOrEqual(44)
  expect(geometry.playOverlap).toBe(0)
  // 30 px in from the right edge is outside the old 12 px sliver, so a miss does not resize.
  expect(geometry.rightPart).toBe('hr')
  await page.mouse.move(geometry.x, geometry.y)
  await page.mouse.down()
  await page.mouse.move(geometry.x - 36, geometry.y, { steps: 6 })
  await page.mouse.up()
  const shrunk = await windowText(page)
  expect(shrunk?.split('–')[0]?.trim()).toBe(before?.split('–')[0]?.trim())
  expect(minuteOf(shrunk?.split('–')[1] ?? null)).toBeLessThan(minuteOf(before?.split('–')[1] ?? null))
  const left = await center(page, '[data-testid="flow-overview-hl"]')
  const end = await windowText(page)
  await page.mouse.move(left.x, left.y)
  await page.mouse.down()
  await page.mouse.move(left.x + 36, left.y, { steps: 6 })
  await page.mouse.up()
  const later = await windowText(page)
  expect(later?.split('–')[1]?.trim()).toBe(end?.split('–')[1]?.trim())
  expect(minuteOf(later)).toBeGreaterThan(minuteOf(end))
})

// AEON-1007: a 15 min window with the playhead on the range end used to stack both
// 44 px handles on one x and leave the playhead over the grips, so the left edge
// could not be resized. Each edge is zoomed fresh so one drag cannot pin the other.
test('a 15 min window at a phone overview edge keeps both handles draggable', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await setup(page)
  await page.goto('/p/AEON/delivery?view=flow')
  const zoom = page.getByTestId('flow-bar').getByRole('radio', { name: '15 min' })
  const follow = page.getByTestId('flow-follow')
  async function edge(key: 'End' | 'Home') {
    if (await follow.isEnabled()) await follow.click()
    await zoom.click()
    await expect.poll(() => windowText(page)).toBe('20:15 – 20:30')
    await lanes(page).focus()
    await page.keyboard.press(key)
    await expect.poll(() => windowText(page)).toBe(key === 'End' ? '21:25 – 21:40' : '18:10 – 18:25')
    await overview(page).scrollIntoViewIfNeeded()
    const geometry = await overview(page).evaluate(host => {
      const box = (id: string) => host.querySelector(`[data-testid="${id}"]`)!.getBoundingClientRect()
      const area = (a: DOMRect, b: DOMRect) => {
        const w = Math.min(a.right, b.right) - Math.max(a.left, b.left)
        const h = Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top)
        return Math.max(0, w) * Math.max(0, h)
      }
      const hr = box('flow-overview-hr'), hl = box('flow-overview-hl'), ph = box('flow-overview-playhead')
      const at = (x: number, y: number) => document.elementFromPoint(x, y)?.getAttribute('data-part') ?? ''
      const mid = (r: DOMRect) => ({ x: r.x + r.width / 2, y: r.y + r.height / 2 })
      const grips = Array.from(host.querySelectorAll('.ln-grip')).map(el => el.getBoundingClientRect())
      const hlC = mid(hl), hrC = mid(hr)
      return {
        hl: { w: hl.width, h: hl.height }, hr: { w: hr.width, h: hr.height },
        overlap: area(hl, hr) + area(hl, ph) + area(hr, ph),
        apart: Math.abs(hlC.x - hrC.x),
        hlPart: at(hlC.x, hlC.y), hrPart: at(hrC.x, hrC.y),
        gripParts: grips.map(g => at(g.x + g.width / 2, g.y + g.height / 2)),
        hlC, hrC,
      }
    })
    expect(geometry.overlap).toBe(0)
    expect(geometry.apart).toBeGreaterThan(20)
    expect(geometry.hl.w).toBeGreaterThanOrEqual(44)
    expect(geometry.hl.h).toBeGreaterThanOrEqual(44)
    expect(geometry.hr.w).toBeGreaterThanOrEqual(44)
    expect(geometry.hr.h).toBeGreaterThanOrEqual(44)
    expect(geometry.hlPart).toBe('hl')
    expect(geometry.hrPart).toBe('hr')
    expect(geometry.gripParts).toEqual(['hl', 'hr'])
    return geometry
  }
  async function drag(at: { x: number; y: number }, dx: number) {
    await page.mouse.move(at.x, at.y)
    await page.mouse.down()
    await page.mouse.move(at.x + dx, at.y, { steps: 6 })
    await page.mouse.up()
  }
  const rightLeft = await edge('End')
  const rightBefore = await windowText(page)
  await drag(rightLeft.hlC, 28)
  const rightShrunk = await windowText(page)
  expect(rightShrunk?.split('–')[1]?.trim()).toBe(rightBefore?.split('–')[1]?.trim())
  expect(minuteOf(rightShrunk)).toBeGreaterThan(minuteOf(rightBefore))
  const rightRight = await edge('End')
  const rightWide = await windowText(page)
  await drag(rightRight.hrC, -28)
  const rightNarrow = await windowText(page)
  expect(rightNarrow?.split('–')[0]?.trim()).toBe(rightWide?.split('–')[0]?.trim())
  expect(minuteOf(rightNarrow?.split('–')[1] ?? null)).toBeLessThan(minuteOf(rightWide?.split('–')[1] ?? null))
  const leftLeft = await edge('Home')
  const leftBefore = await windowText(page)
  await drag(leftLeft.hlC, 28)
  const leftLater = await windowText(page)
  expect(leftLater?.split('–')[1]?.trim()).toBe(leftBefore?.split('–')[1]?.trim())
  expect(minuteOf(leftLater)).toBeGreaterThan(minuteOf(leftBefore))
  const leftRight = await edge('Home')
  const leftWide = await windowText(page)
  await drag(leftRight.hrC, -28)
  const leftNarrow = await windowText(page)
  expect(leftNarrow?.split('–')[0]?.trim()).toBe(leftWide?.split('–')[0]?.trim())
  expect(minuteOf(leftNarrow?.split('–')[1] ?? null)).toBeLessThan(minuteOf(leftWide?.split('–')[1] ?? null))
})

for (const [width, theme, lang] of [[1440, 'light', 'en'], [1440, 'dark', 'en'], [400, 'light', 'en'], [400, 'dark', 'en'], [1440, 'light', 'de'], [400, 'dark', 'de']] as const) {
  test(`Flow lanes at ${width} ${theme} ${lang} keep controls still`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: width === 400 ? 900 : 1100 })
    await setup(page, { theme, lang })
    await page.goto('/p/AEON/delivery?view=flow')
    const zoom = page.getByTestId('flow-bar').getByRole('radiogroup', { name: 'Zoom' })
    await expect(lanes(page).locator('.ln-lane')).toHaveCount(6)
    // Phone: Live opens at 20 min. A de-AT profile still uses the English app language (AEON-998).
    await expect.poll(() => windowText(page)).toBe(width === 400 ? '20:12 – 20:32' : '19:55 – 20:40')
    const guard = await controlStability(page, {
      fit: zoom.getByRole('radio', { name: 'Fit all' }), z60: zoom.getByRole('radio', { name: '1 h' }),
      follow: page.getByTestId('flow-follow'), readout: page.getByTestId('flow-readout'), overview: overview(page), lanes: lanes(page),
    })
    await guard.check(() => zoom.getByRole('radio', { name: '1 h' }).click())
    await guard.check(() => zoom.getByRole('radio', { name: '30 min' }).click())
    await guard.check(async () => { await lanes(page).focus(); await page.keyboard.press('Shift+ArrowLeft') })
    await guard.check(() => page.getByTestId('flow-follow').click())
    guard.done()
    await page.mouse.move(0, 0)
    await page.locator('.fl').evaluate(el => el.scrollIntoView({ block: 'start' }))
    await page.locator('.fl').screenshot({ path: info.outputPath(`aeon-994-p5-lanes/flow-lanes-${width}-${theme}-${lang}.png`) })
  })
}
