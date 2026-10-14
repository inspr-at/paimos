// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1362 (design r4b §5–§7): the status icon set. In progress is diagonal stripes that slide continuously; every other
// status plays one short one-off per pointer entry or keyboard focus, and its base shape never blanks; nothing moves with
// reduced motion, off-screen or in a hidden tab; nothing shifts.
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { controlStability } from './control-stability'
import { statusFill } from '../src/lib/statusMotion'

type OneOffCall = { part: string; status: string; delay: number; duration: number; easing: string; fill: string; props: string[] }

// §7.2, written out here so the spec checks the design's numbers rather than the code's table.
const EASE = 'cubic-bezier(.22, .8, .3, 1)'
const ONE_OFF: Record<string, [part: string, delay: number, duration: number][]> = {
  new: [['dot', 0, 520]], backlog: [['dots', 0, 520]], open: [['glint', 0, 520]], blocked: [['dashes', 0, 420]], progress: [],
  qa: [['pulse', 0, 560]], done: [['tick', 0, 480]], delivered: [['tick', 0, 480]], accepted: [['tick', 0, 300], ['tick2', 200, 320]],
  cancelled: [['slash', 0, 460]], archived: [['sq', 0, 420]],
}
// The base shape that must never blank, and whether it is a plain shape (no dash at all).
const BASE: Record<string, [selector: string, plain: boolean]> = {
  new: ['.ring', true], open: ['.ring', true], backlog: ['.dots', false], blocked: ['.dashes', false], qa: ['.ring', true],
  done: ['.ring', true], delivered: ['.disc', true], accepted: ['.disc', true], cancelled: ['.ring', true], archived: ['.sq', true],
}
const MENU_ORDER = ['new', 'backlog', 'open', 'blocked', 'progress', 'qa', 'done', 'delivered', 'accepted', 'cancelled', 'archived']

const grid = (page: Page) => page.getByRole('grid', { name: 'Tickets' })
const row = (page: Page, key: string) => grid(page).locator('tr.ticket-row').filter({ has: page.locator('.key', { hasText: new RegExp(`^${key}$`) }) })
const statusButton = (page: Page, key: string) => row(page, key).locator('.status-btn')
const groupCount = (page: Page, id: string) => page.getByRole('group', { name: 'Filter tickets by status', exact: true }).locator(`[data-group="${id}"] .group-count`)

// Counts every one-off the app starts (Element.animate with the one-off id), so timing cannot fool a replay check.
async function instrument(page: Page) {
  await page.addInitScript(() => {
    const calls: unknown[] = []
    Object.assign(window, { __oneOffs: calls })
    const animate = Element.prototype.animate
    Element.prototype.animate = function (this: Element, keyframes: Keyframe[] | PropertyIndexedKeyframes | null, options?: number | KeyframeAnimationOptions) {
      if (typeof options === 'object' && options.id === 'status-one-off') {
        const frames = Array.isArray(keyframes) ? keyframes : []
        calls.push({
          part: this.getAttribute('class'), status: [...(this.closest('svg')?.classList ?? [])].find(name => name.startsWith('st-'))?.slice(3),
          delay: options.delay, duration: options.duration, easing: options.easing, fill: options.fill,
          props: [...new Set(frames.flatMap(frame => Object.keys(frame)))].filter(key => key !== 'offset').sort(),
        })
      }
      return animate.call(this, keyframes, options)
    }
  })
}
const oneOffs = (page: Page) => page.evaluate(() => (window as unknown as { __oneOffs: OneOffCall[] }).__oneOffs.length)
const oneOffsSince = (page: Page, from: number) => page.evaluate(start => (window as unknown as { __oneOffs: OneOffCall[] }).__oneOffs.slice(start), from)
function expectOneOff(calls: OneOffCall[], status: string) {
  expect(calls.map(call => [call.status, call.part, call.delay, call.duration]), status).toEqual(ONE_OFF[status].map(([part, delay, duration]) => [status, part, delay, duration]))
  for (const call of calls) {
    expect(call.easing).toBe(EASE); expect(call.fill).toBe('backwards')
    expect(call.delay + call.duration).toBeLessThanOrEqual(600)
    for (const prop of call.props) expect(['opacity', 'strokeDashoffset', 'transform']).toContain(prop)
  }
}

// The slides of in-progress icons: one CSS animation on each icon's stripe layer.
const slides = (page: Page) => page.evaluate(() => document.getAnimations().filter(animation => animation instanceof CSSAnimation && animation.animationName === 'ip-slide').map(animation => {
  const effect = animation.effect as KeyframeEffect
  const timing = effect.getComputedTiming()
  return {
    state: animation.playState, pseudo: effect.pseudoElement, duration: timing.duration, iterations: timing.iterations, easing: effect.getTiming().easing,
    props: [...new Set(effect.getKeyframes().flatMap(frame => Object.keys(frame)))].filter(key => !['offset', 'computedOffset', 'easing', 'composite'].includes(key)),
    offscreen: (effect.target as Element).closest('.status-icon')!.classList.contains('offscreen'),
  }
}))

function workData(theme: string, extraProgress = 0) {
  const data = fixtures()
  data.preferences.theme = { choice: theme }
  const seed = data.nodes.find(node => node.id === 'n-4')!
  const add = (id: string, key: string, state: string, title: string) => data.nodes.push({ ...seed, fields: { priority: 'medium' }, id, key, state, title })
  add('n-open', 'PHAROS-17', 'open', 'Open the provisioning API to agents')
  add('n-blocked', 'PHAROS-18', 'blocked', 'Wait for the Hetzner quota increase')
  for (let index = 0; index < extraProgress; index++) add(`n-ip-${index}`, `PHAROS-${40 + index}`, index % 2 ? 'in_progress' : 'in-progress', `Überprüfung der Berechtigungen, Lauf ${index + 1}`)
  return data
}

for (const width of [1440, 960, 390]) for (const theme of ['light', 'dark']) {
  test(`AEON-1362 icons in the list and header at ${width}px ${theme}: diagonal slide, one-offs once per entry, no shift`, async ({ page }, testInfo) => {
    const errors = watchErrors(page)
    await instrument(page)
    await page.setViewportSize({ width, height: 900 })
    await mockWork(page, workData(theme))
    await page.goto('/p/PHAROS')
    await expect(row(page, 'PHAROS-11')).toBeVisible()

    // In progress is the HTML shape; it keeps the SVG icons' box, so labels line up in every row.
    await expect(statusButton(page, 'PHAROS-11').locator('span.status-icon.st-ip')).toHaveCount(1)
    await expect(statusButton(page, 'PHAROS-13').locator('svg.status-icon.st-qa')).toHaveCount(1)
    const geometry = await grid(page).evaluate(table => [...table.querySelectorAll('tr.ticket-row:not(.ghost) .status-btn')].map(button => {
      const icon = button.querySelector('.status-icon')!
      const iconBox = icon.getBoundingClientRect(), label = button.querySelector(':scope > span:not(.status-icon)')!.getBoundingClientRect(), box = button.getBoundingClientRect()
      return { tag: icon.tagName.toLowerCase(), width: iconBox.width, height: iconBox.height, iconLeft: iconBox.left - box.left, labelLeft: label.left - box.left, centre: iconBox.top + iconBox.height / 2 - (label.top + label.height / 2) }
    }))
    expect(geometry.map(item => item.tag)).toContain('span'); expect(geometry.map(item => item.tag)).toContain('svg')
    for (const item of geometry) {
      expect(item.width).toBeCloseTo(14, 0); expect(item.height).toBeCloseTo(14, 0)
      expect(Math.abs(item.iconLeft - geometry[0].iconLeft)).toBeLessThanOrEqual(.5)
      expect(Math.abs(item.labelLeft - geometry[0].labelLeft)).toBeLessThanOrEqual(.5)
      expect(Math.abs(item.centre)).toBeLessThanOrEqual(1)
    }

    // The only continuous animation: transform-only stripes, one period every 1.8 s, endless.
    const running = await slides(page)
    expect(running.length).toBeGreaterThan(0)
    for (const slide of running) expect(slide).toEqual({ state: 'running', pseudo: '::before', duration: 1800, iterations: Infinity, easing: 'linear', props: ['transform'], offscreen: false })

    // One-offs on the table status and the header counts, measured against moving controls (±0.5 px).
    const guard = await controlStability(page, {
      progress: statusButton(page, 'PHAROS-11'), qa: statusButton(page, 'PHAROS-13'), new: statusButton(page, 'PHAROS-14'),
      qaIcon: statusButton(page, 'PHAROS-13').locator('.status-icon'), qaLabel: statusButton(page, 'PHAROS-13').locator(':scope > span:not(.status-icon)'),
      open: groupCount(page, 'open'), done: groupCount(page, 'done'),
    })
    let count = await oneOffs(page)
    await guard.check(() => statusButton(page, 'PHAROS-13').hover({ position: { x: 4, y: 4 } }))
    expectOneOff(await oneOffsSince(page, count), 'qa')
    count = await oneOffs(page)
    await guard.check(() => statusButton(page, 'PHAROS-13').hover({ position: { x: 20, y: 6 } }))
    expect(await oneOffsSince(page, count), 'moving inside does not replay').toEqual([])
    await guard.check(() => row(page, 'PHAROS-13').locator('.key').hover())
    await guard.check(() => statusButton(page, 'PHAROS-13').hover())
    expectOneOff(await oneOffsSince(page, count), 'qa')
    count = await oneOffs(page)
    await guard.check(() => statusButton(page, 'PHAROS-11').hover())
    expect(await oneOffsSince(page, count), 'in progress has no one-off').toEqual([])
    await guard.check(() => groupCount(page, 'done').hover())
    expectOneOff(await oneOffsSince(page, count), 'done')
    count = await oneOffs(page)
    await guard.check(() => groupCount(page, 'open').hover())
    expectOneOff(await oneOffsSince(page, count), 'open')
    guard.done()

    await page.mouse.move(0, 0)
    await page.screenshot({ path: testInfo.outputPath(`status-icons-list-${width}-${theme}.png`) })
    expect(errors).toEqual([])
  })
}

for (const theme of ['light', 'dark']) {
  test(`AEON-1362 every one-off in the status menu (${theme}): timings, marks only, base never blanks, ends static, keyboard`, async ({ page }, testInfo) => {
    const errors = watchErrors(page)
    await instrument(page)
    await page.setViewportSize({ width: 1440, height: 1000 })
    await mockWork(page, workData(theme))
    await page.goto('/p/PHAROS/PHAROS-13')
    const chip = page.getByRole('complementary', { name: 'Ticket details' }).getByRole('button', { name: /Status: QA/ })
    let count = await oneOffs(page)
    await chip.hover()
    expectOneOff(await oneOffsSince(page, count), 'qa')
    await chip.click()
    const menu = page.getByRole('menu', { name: 'Status of PHAROS-13' })
    const items = menu.getByRole('menuitemradio')
    await expect(items).toHaveCount(11)
    await expect(items.nth(4).locator('span.status-icon.st-ip')).toHaveCount(1)

    for (const [index, status] of MENU_ORDER.entries()) {
      count = await oneOffs(page)
      await items.nth(index).hover({ position: { x: 6, y: 6 } })
      const calls = await oneOffsSince(page, count)
      expectOneOff(calls, status)
      if (status === 'progress') continue
      // Rewind the one-off to its first frame: the base shape is whole there; then finish it: the icon is static again.
      const [selector, plain] = BASE[status]
      const frames = await items.nth(index).locator('svg.status-icon').evaluate((icon, base) => {
        const animations = icon.getAnimations({ subtree: true })
        for (const animation of animations) { animation.pause(); animation.currentTime = 0 }
        const style = getComputedStyle(icon.querySelector(base)!)
        const matrix = new DOMMatrixReadOnly(style.transform === 'none' ? undefined : style.transform)
        const first = { opacity: style.opacity, dashOffset: style.strokeDashoffset, dashArray: style.strokeDasharray, scale: Math.hypot(matrix.a, matrix.b) }
        for (const animation of animations) animation.finish()
        const marks = [...icon.querySelectorAll('.tick, .tick2, .slash')].map(mark => getComputedStyle(mark).strokeDashoffset)
        const hidden = [...icon.querySelectorAll('.pulse, .glint')].map(part => getComputedStyle(part).opacity)
        return { animations: animations.length, first, after: icon.getAnimations({ subtree: true }).length, marks, hidden }
      }, selector)
      expect(frames.animations, status).toBe(ONE_OFF[status].length)
      expect(frames.first.opacity, `${status} base opacity`).toBe('1')
      expect(frames.first.dashOffset, `${status} base dash offset`).toBe('0px')
      if (plain) expect(frames.first.dashArray, `${status} base carries no dash`).toBe('none')
      expect(frames.first.scale, `${status} base scale`).toBeGreaterThanOrEqual(.9 - 1e-6)
      expect(frames.after, `${status} ends static`).toBe(0)
      for (const offset of frames.marks) expect(offset).toBe('0px')
      for (const opacity of frames.hidden) expect(opacity).toBe('0')
    }

    // Moving inside an item does not replay; leaving and coming back does.
    count = await oneOffs(page)
    await items.nth(7).hover({ position: { x: 30, y: 8 } })
    expect(await oneOffsSince(page, count)).toEqual([])
    await items.nth(6).hover()
    await items.nth(7).hover()
    const back = await oneOffsSince(page, count)
    expect(back.map(call => call.status)).toEqual(['done', 'delivered'])

    // Keyboard focus plays it too.
    await items.nth(9).focus()
    count = await oneOffs(page)
    await page.keyboard.press('ArrowDown')
    await expect(items.nth(10)).toBeFocused()
    expectOneOff(await oneOffsSince(page, count), 'archived')

    await page.screenshot({ path: testInfo.outputPath(`status-icons-menu-${theme}.png`) })
    expect(errors).toEqual([])
  })
}

test('AEON-1362 reduced motion: nothing slides and no one-off plays', async ({ page }, testInfo) => {
  await instrument(page)
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await page.setViewportSize({ width: 1440, height: 900 })
  await mockWork(page, workData('light', 3))
  await page.goto('/p/PHAROS')
  await expect(row(page, 'PHAROS-11').locator('.st-ip')).toHaveCount(1)
  const still = () => page.evaluate(() => ({
    animations: document.getAnimations().filter(animation => animation instanceof CSSAnimation ? /slide/.test(animation.animationName) : (animation.effect as KeyframeEffect | null)?.target?.closest?.('.status-icon')).length,
    names: [...document.querySelectorAll('.st-ip > i')].map(layer => getComputedStyle(layer, '::before').animationName),
  }))
  const rest = await still()
  expect(rest.animations).toBe(0)
  expect(rest.names.length).toBeGreaterThan(0)
  for (const name of rest.names) expect(name).toBe('none')
  for (const key of ['PHAROS-13', 'PHAROS-14', 'PHAROS-17', 'PHAROS-18']) await statusButton(page, key).hover()
  await groupCount(page, 'done').hover()
  await statusButton(page, 'PHAROS-13').click()
  const items = page.getByRole('menu', { name: 'Status of PHAROS-13' }).getByRole('menuitemradio')
  for (let index = 0; index < 11; index++) await items.nth(index).hover()
  await page.keyboard.press('ArrowDown')
  expect(await oneOffs(page)).toBe(0)
  expect((await still()).animations).toBe(0)
  await page.screenshot({ path: testInfo.outputPath('status-icons-reduced-motion.png') })
})

test('AEON-1362 slides pause off-screen and in a hidden tab', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 700 })
  await mockWork(page, workData('light', 36))
  await page.goto('/p/PHAROS')
  const ip = grid(page).locator('tr.ticket-row .status-btn .st-ip')
  await expect(ip).toHaveCount(37)
  const last = ip.last(), first = ip.first()
  await expect(last).toHaveClass(/offscreen/)
  await expect(first).not.toHaveClass(/offscreen/)
  const check = async () => {
    const all = await slides(page)
    expect(all.some(slide => slide.offscreen)).toBe(true)
    expect(all.some(slide => !slide.offscreen)).toBe(true)
    for (const slide of all) expect(slide.state).toBe(slide.offscreen ? 'paused' : 'running')
  }
  await check()
  await last.scrollIntoViewIfNeeded()
  await expect(first).toHaveClass(/offscreen/)
  await expect(last).not.toHaveClass(/offscreen/)
  await check()

  await page.evaluate(() => { Object.defineProperty(document, 'hidden', { configurable: true, get: () => true }); document.dispatchEvent(new Event('visibilitychange')) })
  await expect(page.locator('html')).toHaveClass(/tab-hidden/)
  await expect.poll(async () => (await slides(page)).every(slide => slide.state === 'paused')).toBe(true)
  await page.evaluate(() => { Object.defineProperty(document, 'hidden', { configurable: true, get: () => false }); document.dispatchEvent(new Event('visibilitychange')) })
  await expect(page.locator('html')).not.toHaveClass(/tab-hidden/)
  await check()
})

test('AEON-1362 bar form: diagonal stripes slide only live or under the pointer; Cancelled is one slash, never stripes', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await mockWork(page, workData('light'))
  await page.goto('/p/PHAROS')
  await expect(row(page, 'PHAROS-11')).toBeVisible()
  expect(statusFill('in-progress')).toBe('f-diag fc-progress')
  expect(statusFill('canceled')).toBe('f-cancel fc-cancelled')
  const fills = { diag: statusFill('in_progress'), cancel: statusFill('cancelled'), accepted: statusFill('accepted'), delivered: statusFill('delivered') }
  await page.evaluate(classes => {
    const host = document.createElement('div')
    host.id = 'fill-probe'; host.style.cssText = 'position: fixed; left: 20px; top: 20px; display: grid; gap: 8px; width: 240px; z-index: 9999'
    host.innerHTML = `<div class="f-slide-hover" data-probe="hover"><i class="${classes.diag}" data-probe="past" style="display: block; height: 10px"></i></div>
      <i class="${classes.diag} f-live" data-probe="live" style="display: block; height: 10px"></i>
      <i class="${classes.diag} f-short" data-probe="short" style="display: block; width: 8px; height: 10px"></i>
      <i class="${classes.cancel}" data-probe="cancel" style="display: block; height: 10px"></i>
      <i class="${classes.accepted}" data-probe="accepted" style="display: block; height: 10px"></i>
      <i class="${classes.delivered}" data-probe="delivered" style="display: block; height: 10px"></i>`
    document.body.append(host)
  }, fills)
  const probe = (name: string) => page.locator(`[data-probe="${name}"]`)
  const state = (name: string) => probe(name).evaluate(element => {
    const before = getComputedStyle(element, '::before'), after = getComputedStyle(element, '::after')
    return { image: before.backgroundImage, play: before.animationPlayState, name: before.animationName, display: before.display, afterImage: after.backgroundImage, afterWidth: after.width, color: getComputedStyle(element).color }
  })
  const past = await state('past')
  expect(past.image).toContain('repeating-linear-gradient')
  expect(past).toMatchObject({ name: 'diag-slide', play: 'paused' })
  expect((await state('live')).play).toBe('running')
  expect((await state('short')).display).toBe('none')
  await probe('hover').hover()
  expect((await state('past')).play).toBe('running')
  const cancel = await state('cancel')
  expect(cancel.image).toBe('none')
  expect(cancel).toMatchObject({ afterImage: 'none', afterWidth: '1.5px' })
  expect((await state('accepted')).color).not.toBe((await state('delivered')).color)
  await page.emulateMedia({ reducedMotion: 'reduce' })
  expect((await state('live')).name).toBe('none')
  expect((await state('past')).name).toBe('none')
})

test('AEON-1362 budget: 24 sliding icons cost at most 1 ms of main-thread time per frame', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1400 })
  await mockWork(page, workData('light', 24))
  await page.goto('/p/PHAROS')
  await expect(grid(page).locator('tr.ticket-row .st-ip')).toHaveCount(25)
  await page.mouse.move(0, 0)
  const running = (await slides(page)).filter(slide => slide.state === 'running' && !slide.offscreen)
  expect(running.length).toBeGreaterThanOrEqual(20)
  const cdp = await page.context().newCDPSession(page)
  await cdp.send('Performance.enable')
  const taskSeconds = async () => (await cdp.send('Performance.getMetrics')).metrics.find(metric => metric.name === 'TaskDuration')!.value
  const start = await taskSeconds()
  // The measurement window itself (§6.4): steady state, nothing on the page asking for frames.
  await page.waitForTimeout(3000)
  const perFrame = (await taskSeconds() - start) * 1000 / (3 * 60)
  console.log(`AEON-1362 sliding budget: ${running.length} icons, ${perFrame.toFixed(4)} ms main-thread time per frame`)
  expect(perFrame).toBeLessThanOrEqual(1)
})
