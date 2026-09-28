// SPDX-License-Identifier: AGPL-3.0-only
// AEON-242. Coordinator: npx playwright test -c playwright.ui.config.ts tests/indicator-controls.spec.ts --workers=2
import { expect, test, type Locator, type Page } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import { resolve } from 'node:path'
import { ICON_SIZE, indicatorArtScale, indicatorRing, indicatorVariants, nativeIconSize, type AgentIndicatorStyle } from '../src/lib/indicatorVariants'
import { mockIndicator } from './agent-indicator-fixtures'

const shots = resolve('..', '.agent-shots')
test.beforeAll(() => mkdirSync(shots, { recursive: true }))
const inner = '.inner-art, .robot1 .robot, .robot5 .bob'
// The layer that carries each style's ring motion.
const ringMotion = '.ring-sweep, .pulse .sweep, .indicator-orbit .satellite'
const loops = (locator: Locator, selector?: string) => locator.evaluate((el, only) => el.getAnimations({ subtree: true })
  .filter(a => a.effect?.getTiming().iterations === Infinity && (!only || ((a.effect as KeyframeEffect).target as Element | null)?.matches(only))).length, selector)
const pxs = [20, 26, 64]
async function open(page: Page, query = '') {
  await page.setViewportSize({ width: 1400, height: 1000 })
  await page.goto(`/tests/indicator-controls.html${query}`)
  await expect(page.locator('.cell[data-ring] .agent-indicator-art')).toHaveCount(4 * 4 * 9 * pxs.length)
  await expect(page.locator('.cell.live .live-bot .agent-indicator-art')).toHaveCount(6 * 9 * 3)
}

// Inner edge of each style's ring (32-unit box): radius minus half its stroke.
// Orbit keeps clear of its satellite; Robot 5 fills its disk.
const ringInside: Record<AgentIndicatorStyle, number> = {
  pulse: 11 - .75, 'robot-1': 14 - .675, 'robot-2': 14 - .625, 'robot-3': 14 - .625, 'robot-4': 14 - .625,
  'robot-5': 16, orbit: 12 - 1.8 - .5, quill: 15.1 - .625, sprite: 15.1 - .625,
}
// Parts that reach over the ring as drawn: antenna tips and Quill's ink tail.
const overTheRing = '.antenna, .antenna-halo, .tip, .tip-glow, .ink-track, .ink-line, .antennae, .glint'
/** The body's outer radius, from its real outlines plus half the rendered stroke, in 32-unit box terms. */
const bodyRadius = (roots: Locator) => roots.evaluateAll((nodes, [art, skip]) => nodes.map(root => {
  const r = root.getBoundingClientRect(), unit = r.width / 32, cx = r.x + r.width / 2, cy = r.y + r.height / 2
  let radius = 0
  for (const el of root.querySelector(art)!.querySelectorAll<SVGGeometryElement>('path, circle, rect, ellipse')) {
    if (el.closest(skip)) continue
    const style = getComputedStyle(el), m = el.getScreenCTM()!
    const half = style.stroke === 'none' ? 0 : parseFloat(style.strokeWidth) * Math.hypot(m.a, m.b) / unit / 2
    const length = el.getTotalLength()
    for (let i = 0; i <= 160; i++) {
      const point = el.getPointAtLength(length * i / 160).matrixTransform(m)
      radius = Math.max(radius, Math.hypot(point.x - cx, point.y - cy) / unit + half)
    }
  }
  return radius
}), [inner, overTheRing] as const)

test('100% fills the space inside each ring; 30% is 30% of it; unset keeps each drawing', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await open(page)
  for (const variant of indicatorVariants) {
    const at = (size: number | 'drawn') => page.locator(`.cell[data-variant="${variant.id}"][data-ring="still"][data-size="${size}"] [data-px="64"] > *`)
    const [full] = await bodyRadius(at(ICON_SIZE.max)), [small] = await bodyRadius(at(ICON_SIZE.min)), [drawn] = await bodyRadius(at('drawn'))
    expect(full!, `${variant.id} 100% stays inside its ring`).toBeLessThanOrEqual(ringInside[variant.id] - .2)
    expect(full!, `${variant.id} 100% reaches its ring`).toBeGreaterThanOrEqual(ringInside[variant.id] - 1.1)
    // Strokes follow the square root of the scale, so small bodies are a touch fuller than linear.
    expect(small! / full!, `${variant.id} 30% of 100%`).toBeGreaterThan(.29)
    expect(small! / full!, `${variant.id} 30% of 100%`).toBeLessThan(.4)
    expect(Math.abs(drawn! / full! * 100 - nativeIconSize(variant.id)), `${variant.id} drawn at ${nativeIconSize(variant.id)}%`).toBeLessThan(4)
  }
})

test('only the inner artwork scales; footprint, ring and state mark keep their geometry', async ({ page }) => {
  await open(page)
  const rows = await page.locator('.cell[data-ring]').evaluateAll((cells, selector) => cells.flatMap(cell => [...cell.querySelectorAll('[data-px]')].map(holder => {
    const root = holder.firstElementChild!, px = Number((holder as HTMLElement).dataset.px)
    const r = root.getBoundingClientRect(), a = root.querySelector(selector)!.getBoundingClientRect(), m = root.querySelector('.agent-state-mark')!.getBoundingClientRect()
    const unit = (n: number) => n / px * 32
    return {
      key: `${(cell as HTMLElement).dataset.variant}/${(cell as HTMLElement).dataset.ring}/${px}`, size: (cell as HTMLElement).dataset.size!, px,
      box: [r.width, r.height], mark: [m.x - r.x, m.y - r.y, m.width, m.height].map(n => Math.round(n * 10) / 10),
      art: { top: unit(a.top - r.top), left: unit(a.left - r.left), bottom: unit(a.bottom - r.top), right: unit(a.right - r.left), width: a.width },
    }
  })), inner)
  for (const row of rows) {
    const [variant] = row.key.split('/')
    expect(row.box, row.key).toEqual([row.px, row.px])
    // The drawing may peek over the top (antennae) but never leaves the footprint sideways or below.
    expect(row.art.top, `${row.key} ${row.size}% top`).toBeGreaterThanOrEqual(-2.5)
    expect(row.art.left, `${row.key} ${row.size}% left`).toBeGreaterThanOrEqual(0.5)
    expect(row.art.right, `${row.key} ${row.size}% right`).toBeLessThanOrEqual(31.5)
    expect(row.art.bottom, `${row.key} ${row.size}% bottom`).toBeLessThanOrEqual(32)
    const drawn = rows.find(other => other.key === row.key && other.size === 'drawn')!
    expect(row.mark, `${row.key} ${row.size}% mark`).toEqual(drawn.mark)
    const ratio = row.size === 'drawn' ? 1 : indicatorArtScale(variant as AgentIndicatorStyle, Number(row.size))
    expect(row.art.width / drawn.art.width, `${row.key} ${row.size}% scale`).toBeCloseTo(ratio, 1)
  }
})

test('ring modes: only Moving animates the ring, the artwork keeps its own motion, reduced motion stills all', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'no-preference' })
  await open(page)
  for (const variant of indicatorVariants) {
    const cell = (ring: string) => page.locator(`.cell[data-variant="${variant.id}"][data-ring="${ring}"][data-size="drawn"] [data-px="64"]`)
    expect(await loops(cell('moving'), ringMotion), `${variant.id} moving`).toBeGreaterThan(0)
    for (const ring of ['still', 'off']) expect(await loops(cell(ring), ringMotion), `${variant.id} ${ring}`).toBe(0)
    await expect(cell('off').locator('.rim, .ring-track, .track, .ticks, .orbit-track, .point, .indicator-ring')).toHaveCount(0)
    await expect(cell('still').locator('.rim, .ring-track, .track, .orbit-track').or(cell('still').locator('.robot5:not(.ring-off) .disk')).first()).toBeVisible()
    // Off leaves no circular outline: Robot 5's disk keeps its fill but loses its rim and edge shadow.
    if (variant.id === 'robot-5') {
      await expect(cell('off').locator('.disk')).toHaveCSS('box-shadow', 'none')
      await expect(cell('still').locator('.disk')).not.toHaveCSS('box-shadow', 'none')
    }
    // Robot 1, Pulse and Orbit move only through their ring; every other drawing keeps its working motion.
    if (!['robot-1', 'pulse', 'orbit'].includes(variant.id)) expect(await loops(cell('off')), `${variant.id} artwork motion`).toBeGreaterThan(0)
    const own = indicatorRing(variant.id)
    expect(await loops(cell('own'), ringMotion) > 0, `${variant.id} keeps its own ring`).toBe(own === 'moving')
  }
  await page.emulateMedia({ reducedMotion: 'reduce' })
  expect(await loops(page.locator('main'))).toBe(0)
})

test('the account ring and size reach every preview; malformed values keep each drawing as designed', async ({ page }) => {
  await open(page, '?ring=off&size=100')
  const live = page.locator('.cell.live .live-bot')
  const first = (variant: string) => page.locator(`.cell.live[data-variant="${variant}"][data-state="working"] .live-bot`).nth(1)
  for (const variant of indicatorVariants) {
    const bots = page.locator(`.cell.live[data-variant="${variant.id}"] .live-bot`)
    await expect(bots.locator('[data-ring="off"]')).toHaveCount(18)
    await expect(bots.locator('.agent-state-mark')).toHaveCount(18)
    await expect(bots.locator('.rim, .ring-track, .track, .ticks, .orbit-track, .point, .indicator-ring')).toHaveCount(0)
  }
  await expect(live.locator('.robot5.ring-off')).toHaveCount(18)
  await expect(live.locator('.robot5:not(.ring-off)')).toHaveCount(0)
  await expect(first('robot-5').locator('.bot')).toHaveAttribute('width', String(Math.round(26 * 1.1 * 1.04)))
  await expect(first('robot-1').locator('.robot')).toHaveCSS('scale', '1.21')
  await expect(first('robot-4').locator('.inner-art')).toHaveCSS('scale', '1.07')
  await expect(first('pulse').locator('.inner-art')).toHaveCSS('scale', '1.9')
  for (const state of ['working', 'waiting', 'problem', 'stopped']) {
    await expect(page.locator(`.cell.live[data-state="${state}"] .live-bot`).first()).toHaveAttribute('data-state', state)
  }
  // Out-of-range numbers clamp to the ends of the slider.
  await open(page, '?size=5')
  await expect(first('robot-1').locator('.robot')).toHaveCSS('scale', String(indicatorArtScale('robot-1', ICON_SIZE.min)))
  await open(page, '?size=1e9')
  await expect(first('orbit').locator('.inner-art')).toHaveCSS('scale', '1.06')
  for (const query of ['?ring=spin&size=abc', '?ring=&size=-1e999', '?ring=Moving&size=null']) {
    await open(page, query)
    for (const variant of indicatorVariants) {
      const bot = page.locator(`.cell.live[data-variant="${variant.id}"] .live-bot`).first()
      await expect(bot.locator(`[data-ring="${indicatorRing(variant.id)}"]`)).toHaveCount(1)
      await expect(bot.locator('.agent-state-mark')).toHaveCount(1)
    }
    await expect(page.locator('.cell.live .inner-art[style*="scale"], .cell.live .robot[style*="scale"]')).toHaveCount(0)
    await expect(first('robot-5').locator('.bot')).toHaveAttribute('width', '29')
    await expect(page.locator('.cell.live [style*="--art-stroke"]')).toHaveCount(0)
  }
})

test('every LiveBot caller follows the viewer: cards, list rows, popovers and /agents', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'no-preference' })
  const { data } = await mockIndicator(page)
  data.preferences['agent-indicator'] = { style: 'robot-4', hovering: true, ring: 'moving', size: 50 }
  const consistent = async (scope: Locator) => {
    const bots = scope.locator('.live-bot')
    await expect(bots.first()).toBeVisible()
    await expect(bots.locator('.indicator-art > [data-ring]:not([data-ring="moving"])')).toHaveCount(0)
    for (const bot of await bots.all()) {
      await expect(bot).toHaveAttribute('data-style', 'robot-4')
      await expect(bot.locator('.inner-art')).toHaveCSS('scale', '0.535')
      await expect(bot.locator('.agent-state-mark')).toHaveCount(1)
    }
  }
  await page.goto('/')
  const project = page.locator('[data-project-id="p-aeon"]')
  for (const view of ['Cards', 'List']) {
    await page.getByRole('radio', { name: `${view} view`, exact: true }).click()
    await consistent(project.locator('.live-chip'))
    await project.locator('.live-chip').hover()
    await consistent(page.getByRole('dialog', { name: 'Agents working on Aeon' }))
    await page.keyboard.press('Escape')
    await page.mouse.move(0, 0)
  }
  await page.goto('/agents')
  await consistent(page.locator('.agents-page'))
  expect(await loops(page.locator('.agents-page .live-bot[data-state="working"]').first(), ringMotion)).toBeGreaterThan(0)
  expect(await loops(page.locator('.agents-page .live-bot[data-state="waiting"]').first())).toBe(0)
  expect(data.preferences['agent-indicator']).toEqual({ style: 'robot-4', hovering: true, ring: 'moving', size: 50 })
})

async function settings(page: Page) {
  await page.goto('/settings/personal#agents')
  await expect(page.getByRole('radiogroup', { name: 'Agent indicator' })).toBeVisible()
}
const option = (page: Page, name: string) => page.getByRole('radiogroup', { name: 'Agent indicator' }).getByRole('radio', { name, exact: true })
const ringOption = (page: Page, name: string) => page.getByRole('radiogroup', { name: 'Activity ring' }).getByRole('radio', { name, exact: true })

test('compact picker: icons with names and descriptions, one caption for the selected style, a matching demo', async ({ page }) => {
  await mockIndicator(page)
  await settings(page)
  const caption = page.getByTestId('indicator-caption')
  await expect(caption).toHaveText('Robot 1 · Calm and composed')
  for (const variant of indicatorVariants) {
    await expect(option(page, variant.name)).toHaveAccessibleDescription(variant.description)
    const visibleText = await option(page, variant.name).evaluate(el => [...el.querySelectorAll('*')]
      .filter(node => !node.closest('.sr-only') && [...node.childNodes].some(child => child.nodeType === Node.TEXT_NODE && child.textContent!.trim())).length)
    expect(visibleText, `${variant.name} shows only its icon`).toBe(0)
  }
  // Browsing does not move the caption; choosing does.
  await option(page, 'Robot 1').focus()
  await page.keyboard.press('ArrowRight')
  await expect(option(page, 'Robot 2')).toBeFocused()
  await expect(caption).toHaveText('Robot 1 · Calm and composed')
  await page.keyboard.press('Enter')
  await expect(caption).toHaveText('Robot 2 · A little more warmth')
  await expect(page.locator('.demo .live-bot')).toHaveAttribute('data-style', 'robot-2')
  // Selected and focused stay distinguishable: a tint for the choice, an outline for focus.
  const look = (name: string) => option(page, name).evaluate(el => { const s = getComputedStyle(el); return { outline: s.outlineStyle, background: s.backgroundImage + s.backgroundColor } })
  await page.keyboard.press('ArrowRight')
  const [chosen, focused] = [await look('Robot 2'), await look('Robot 3')]
  expect(chosen.outline).toBe('none')
  expect(focused.outline).toBe('solid')
  expect(chosen.background).not.toBe(focused.background)
  await page.setViewportSize({ width: 390, height: 900 })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  const tops = await page.getByRole('radiogroup', { name: 'Agent indicator' }).getByRole('radio').evaluateAll(els => els.map(el => el.getBoundingClientRect().bottom))
  expect(Math.max(...tops)).toBeLessThan(await caption.evaluate(el => el.getBoundingClientRect().top))
})

test('Activity ring and Icon size save independently of style and Hovering; every preview follows', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
  const { data } = await mockIndicator(page)
  data.preferences['agent-indicator'] = { style: 'robot-1', hovering: true }
  await settings(page)
  const saved = () => data.preferences['agent-indicator']
  // Unset: each style's own ring and drawn size, and nothing is written.
  await expect(ringOption(page, 'Moving')).toBeChecked()
  const slider = page.getByRole('slider', { name: 'Icon size' })
  await expect(slider).toHaveValue(String(nativeIconSize('robot-1')))
  await expect(slider).toHaveAttribute('aria-valuetext', `${nativeIconSize('robot-1')}%, as drawn`)
  await option(page, 'Quill').click()
  await expect(ringOption(page, 'Off')).toBeChecked()
  await expect(slider).toHaveValue(String(nativeIconSize('quill')))
  await expect.poll(saved).toEqual({ style: 'quill', hovering: true })
  // Arrow keys move through the ring modes and save the choice.
  await ringOption(page, 'Off').focus()
  await page.keyboard.press('ArrowLeft')
  await expect(ringOption(page, 'Still')).toBeChecked()
  await expect(ringOption(page, 'Still')).toBeFocused()
  await expect.poll(saved).toEqual({ style: 'quill', hovering: true, ring: 'still' })
  await expect(page.locator('.indicator-choices .live-bot [data-ring]:not([data-ring="still"])')).toHaveCount(0)
  await expect(page.locator('.demo [data-ring="still"] .indicator-ring')).toHaveCount(1)
  // Native slider keys: arrows step by 5, Home and End reach the ends.
  await slider.focus()
  await page.keyboard.press('Home')
  await expect.poll(saved).toEqual({ style: 'quill', hovering: true, ring: 'still', size: ICON_SIZE.min })
  await page.keyboard.press('ArrowRight')
  await expect(slider).toHaveValue(String(ICON_SIZE.min + ICON_SIZE.step))
  await expect(slider).toHaveAttribute('aria-valuetext', `${ICON_SIZE.min + ICON_SIZE.step}%`)
  await expect(page.locator('.demo .inner-art')).toHaveCSS('scale', String(indicatorArtScale('quill', ICON_SIZE.min + ICON_SIZE.step)))
  await page.keyboard.press('End')
  await expect.poll(saved).toEqual({ style: 'quill', hovering: true, ring: 'still', size: ICON_SIZE.max })
  // The same percentage carries to another style without a plateau.
  await option(page, 'Robot 4').click()
  await expect(slider).toHaveValue(String(ICON_SIZE.max))
  await expect(page.locator('.demo .inner-art')).toHaveCSS('scale', String(indicatorArtScale('robot-4', ICON_SIZE.max)))
  await page.getByRole('switch', { name: 'Hovering' }).uncheck()
  await expect.poll(saved).toEqual({ style: 'robot-4', hovering: false, ring: 'still', size: ICON_SIZE.max })
  await page.getByRole('button', { name: 'Use drawn sizes' }).click()
  await expect.poll(saved).toEqual({ style: 'robot-4', hovering: false, ring: 'still' })
  await expect(slider).toHaveValue(String(nativeIconSize('robot-4')))
  await expect(page.locator('.demo .inner-art[style*="scale"]')).toHaveCount(0)
  await page.reload()
  await expect(ringOption(page, 'Still')).toBeChecked()
  await expect(option(page, 'Robot 4')).toBeChecked()
})

test('inactive opacity is a styled native slider: keyboard, disabled with Dim inactive off', async ({ page }) => {
  const { data } = await mockIndicator(page)
  await settings(page)
  const opacity = page.getByRole('slider', { name: 'Inactive opacity' })
  const before = await opacity.inputValue()
  await opacity.focus()
  await page.keyboard.press('ArrowRight')
  await expect(opacity).toHaveValue(String(Number(before) + 1))
  await expect(opacity).toHaveAttribute('aria-valuetext', `${Number(before) + 1}%`)
  await expect.poll(() => (data.preferences['agent-state'] as { inactiveOpacity?: number } | undefined)?.inactiveOpacity).toBe(Number(before) + 1)
  await page.getByRole('switch', { name: 'Dim inactive' }).uncheck()
  await expect(opacity).toBeDisabled()
  expect(await opacity.evaluate(el => getComputedStyle(el).appearance)).toBe('none')
})

test('a failed ring or size save is visible and can be retried', async ({ page }) => {
  const { data } = await mockIndicator(page)
  let fail = true
  await page.route('**/api/preferences/agent-indicator', route => route.request().method() === 'PUT' && fail ? route.fulfill({ status: 503, json: { error: 'unavailable' } }) : route.fallback())
  await settings(page)
  await ringOption(page, 'Off').click()
  await expect(page.getByRole('alert')).toContainText('agent indicator setting could not be saved')
  fail = false
  await page.getByRole('button', { name: 'Try again', exact: true }).click()
  await expect.poll(() => data.preferences['agent-indicator']).toEqual({ style: 'robot-1', hovering: false, ring: 'off' })
  await expect(page.locator('.save-error')).toHaveCount(0)
})

test.describe('zoomed craft details', () => {
  test.use({ deviceScaleFactor: 3 })
  for (const theme of ['light', 'dark'] as const) test(`${theme}: each ring mode as drawn, at 30, 65 and 100%, at 20, 26 and 64 px`, async ({ page }) => {
    await page.emulateMedia({ reducedMotion: 'reduce' })
    await open(page, `?theme=${theme}`)
    const union = async (selector: string) => {
      const boxes = await page.locator(selector).evaluateAll(nodes => nodes.map(node => node.getBoundingClientRect().toJSON() as DOMRect))
      const x = Math.min(...boxes.map(b => b.x)), y = Math.min(...boxes.map(b => b.y))
      return { x, y, width: Math.max(...boxes.map(b => b.right)) - x, height: Math.max(...boxes.map(b => b.bottom)) - y }
    }
    for (const ring of ['own', 'moving', 'still', 'off']) {
      await page.screenshot({ path: resolve(shots, `aeon242-zoom-${theme}-${ring}.png`), clip: await union(`.cell[data-ring="${ring}"]`), fullPage: true })
    }
    await page.screenshot({ path: resolve(shots, `aeon242-zoom-${theme}-states.png`), clip: await union('.cell.live'), fullPage: true })
  })
})

for (const theme of ['light', 'dark'] as const) {
  test(`craft sheets: ${theme}, frozen motion and reduced motion`, async ({ page }) => {
    for (const [motion, query] of [['no-preference', ''], ['reduce', '&ring=moving&size=100'], ['reduce', '&ring=off&size=30']] as const) {
      await page.emulateMedia({ reducedMotion: motion })
      await open(page, `?theme=${theme}${query}`)
      await page.getByRole('button', { name: 'Send event' }).click()
      await page.evaluate(() => { for (const animation of document.getAnimations()) { animation.pause(); animation.currentTime = animation.effect?.getTiming().iterations === 1 ? 180 : 700 } })
      await page.screenshot({ path: resolve(shots, `aeon242-sheet-${theme}-${motion}${query.replace(/[&=]/g, '-')}.png`), fullPage: true })
    }
  })
  for (const width of [1280, 390]) test(`real surfaces: ${theme} ${width}px with a chosen ring and size`, async ({ page }) => {
    await page.emulateMedia({ reducedMotion: 'reduce', colorScheme: theme })
    const { data } = await mockIndicator(page)
    data.preferences['agent-indicator'] = { style: 'robot-5', hovering: false, ring: 'off', size: 60 }
    data.preferences['agent-state'] = { palette: theme === 'light' ? 'deutan' : 'tritan' }
    await page.setViewportSize({ width, height: 900 })
    for (const [path, name] of [['/', 'projects'], ['/agents', 'agents'], ['/settings/personal#agents', 'settings']] as const) {
      await page.goto(path)
      await expect(page.locator('.live-bot').first()).toBeVisible()
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `${name} fits ${width}px`).toBe(true)
      await page.screenshot({ path: resolve(shots, `aeon242-${name}-${theme}-${width}.png`), fullPage: name !== 'agents' })
    }
    // The agents card alone, with keyboard focus on a style that is not selected.
    await page.setViewportSize({ width, height: 2400 })
    await option(page, 'Robot 5').focus()
    await page.keyboard.press('ArrowRight')
    await page.locator('.indicator-settings').screenshot({ path: resolve(shots, `aeon242-settings-card-${theme}-${width}.png`) })
  })
}
