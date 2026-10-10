// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1007 (AEON-994 package 7): Delivery on a phone, in dark and by keyboard. Risks: a
// text falls under 4.5:1 in one theme; a control on a phone is smaller than 44 px; the four
// project sections overflow a phone or lose their names; a keyboard path skips a control
// group; the window, level, zoom or playhead moves a control at 1440 or at 400 px.
import { expect, test, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { controlStability } from './control-stability'
import { deliveryMetrics, mockDelivery } from './delivery-numbers-fixtures'
import { mockFlow, ZONE } from './delivery-flow-fixtures'

test.use({ timezoneId: ZONE })

type Theme = 'light' | 'dark'
async function setup(page: Page, options: { theme?: Theme; lang?: 'en' | 'de'; level?: 'simple' | 'expert' } = {}) {
  const work = fixtures()
  work.preferences.theme = { choice: options.theme ?? 'light' }
  work.preferences['delivery:numbers'] = { level: options.level ?? 'simple', window: 7 }
  await mockWork(page, work)
  if (options.lang === 'de') { const data = settingsData(); data.profile.locale = 'de-AT'; await mockSettings(page, data) }
  await mockDelivery(page, async () => ({ status: 200, body: deliveryMetrics() }), ['delivery.read'])
  return mockFlow(page)
}
const head = (page: Page) => page.locator('.dl-head')
const sections = (page: Page) => page.getByRole('tablist', { name: 'Project sections' })
const lanes = (page: Page) => page.getByTestId('flow-lanes')
const clock = (page: Page) => page.getByTestId('flow-clock')
async function center(page: Page, selector: string) {
  const box = (await page.locator(selector).first().boundingBox())!
  return { x: box.x + box.width / 2, y: box.y + box.height / 2 }
}
async function numbersReady(page: Page, level: 'simple' | 'expert') {
  await expect(level === 'simple' ? page.getByTestId('delivery-summary') : page.locator('.tiles [role="listitem"]').first()).toBeVisible()
  await expect(page.locator('.dl .sk')).toHaveCount(0)
}
async function flowReady(page: Page) {
  await expect(page.getByTestId('flow-moment')).toBeVisible()
  await expect(page.locator('.dl .sk')).toHaveCount(0)
}

// ---------- Contrast: what is painted, measured like the design's check ----------
// Every text on the page (HTML text and SVG labels) is measured against the pixels behind
// it: the text is hidden, the page is captured, and the median pixel under each text box is
// its background (glass, tints, lane rows and bands included). A halo stroke behind an SVG
// label is that label's background. Disabled controls are exempt (WCAG 1.4.3).
interface ContrastMiss { text: string; where: string; fg: string; bg: string; ratio: number }
async function contrastMisses(page: Page): Promise<ContrastMiss[]> {
  await page.mouse.move(0, 0)
  // The page scrolls inside main: the viewport grows by what main hides, so every text is on screen.
  const viewport = page.viewportSize()!
  const extra = await page.evaluate(() => {
    window.scrollTo(0, 0)
    const main = document.querySelector('main')
    if (main) main.scrollTop = 0
    return Math.max(document.documentElement.scrollHeight - innerHeight, main ? main.scrollHeight - main.clientHeight : 0)
  })
  await page.setViewportSize({ width: viewport.width, height: Math.min(viewport.height + extra + 8, 7000) })
  await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))))
  const count = await page.evaluate(() => {
    const roots = Array.from(document.querySelectorAll<Element>('.dl, .dl-tip, .dl-learn, [role="tablist"][aria-label="Project sections"]'))
    let n = 0
    for (const root of roots) {
      for (const el of Array.from(root.querySelectorAll<Element>('*')).concat(root)) {
        const svgText = el instanceof SVGTextElement
        const own = svgText ? (el.textContent ?? '').trim() : Array.from(el.childNodes).filter(c => c.nodeType === Node.TEXT_NODE).map(c => c.textContent ?? '').join('').trim()
        if (!own || el.closest('.sk, :disabled, [aria-disabled="true"], .sr-only')) continue
        if (!el.checkVisibility({ visibilityProperty: true, opacityProperty: true })) continue
        el.setAttribute('data-cx', String(n++))
      }
    }
    return n
  })
  expect(count, 'texts measured').toBeGreaterThan(20)
  await page.addStyleTag({ content: '[data-cx], [data-cx] * { color: transparent !important; -webkit-text-fill-color: transparent !important; fill: transparent !important; stroke: transparent !important; text-shadow: none !important; }' })
  await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))))
  const shot = (await page.screenshot({ animations: 'disabled' })).toString('base64')
  const misses = await page.evaluate(async (png: string) => {
    // The capture is taken: the words get their colours back before they are read.
    document.querySelectorAll('style').forEach(s => { if (s.textContent?.includes('[data-cx]')) s.remove() })
    const img = new Image()
    img.src = `data:image/png;base64,${png}`
    await img.decode()
    const scale = img.naturalWidth / window.innerWidth
    const canvas = document.createElement('canvas')
    canvas.width = img.naturalWidth; canvas.height = img.naturalHeight
    const ctx = canvas.getContext('2d', { willReadFrequently: true })!
    ctx.drawImage(img, 0, 0)
    const probe = document.createElement('canvas').getContext('2d', { willReadFrequently: true })!
    probe.canvas.width = 1; probe.canvas.height = 1
    const rgba = (value: string): [number, number, number, number] | null => {
      if (!value || value === 'none' || value.startsWith('url(')) return null
      probe.clearRect(0, 0, 1, 1); probe.fillStyle = '#000'; probe.fillStyle = value; probe.fillRect(0, 0, 1, 1)
      const [r, g, b, a] = probe.getImageData(0, 0, 1, 1).data
      return [r!, g!, b!, a! / 255]
    }
    const over = (fg: [number, number, number, number], bg: [number, number, number]): [number, number, number] =>
      [0, 1, 2].map(i => fg[i]! * fg[3] + bg[i]! * (1 - fg[3])) as [number, number, number]
    const lum = (c: [number, number, number]) => {
      const [r, g, b] = c.map(v => { const s = v / 255; return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4 })
      return 0.2126 * r! + 0.7152 * g! + 0.0722 * b!
    }
    const ratio = (a: [number, number, number], b: [number, number, number]) => { const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p); return (x! + 0.05) / (y! + 0.05) }
    const hex = (c: number[]) => `#${c.slice(0, 3).map(v => Math.round(v).toString(16).padStart(2, '0')).join('')}`
    const box = (el: Element): DOMRect | null => {
      const own = el.getBoundingClientRect()
      let rect = own
      if (!(el instanceof SVGElement)) {
        const range = document.createRange()
        const rects = Array.from(el.childNodes).filter(c => c.nodeType === Node.TEXT_NODE && (c.textContent ?? '').trim()).map(c => { range.selectNodeContents(c); return range.getBoundingClientRect() })
        if (rects.length) {
          const x0 = Math.max(own.left, Math.min(...rects.map(r => r.left))), y0 = Math.max(own.top, Math.min(...rects.map(r => r.top)))
          const x1 = Math.min(own.right, Math.max(...rects.map(r => r.right))), y1 = Math.min(own.bottom, Math.max(...rects.map(r => r.bottom)))
          rect = new DOMRect(x0, y0, x1 - x0, y1 - y0)
        }
      }
      // What an SVG paints is clipped by its own frame; nothing beyond the capture counts.
      const frame = el instanceof SVGElement ? el.ownerSVGElement?.getBoundingClientRect() : null
      const fx0 = Math.max(rect.left, frame?.left ?? 0, 0), fy0 = Math.max(rect.top, frame?.top ?? 0, 0)
      const fx1 = Math.min(rect.right, frame?.right ?? Infinity, innerWidth), fy1 = Math.min(rect.bottom, frame?.bottom ?? Infinity, innerHeight)
      return fx1 - fx0 >= 2 && fy1 - fy0 >= 4 ? new DOMRect(fx0, fy0, fx1 - fx0, fy1 - fy0) : null
    }
    // A text under a later painted shape or an open popover is not on show (the playhead pill over "now").
    const painted = (e: Element) => {
      const cs = getComputedStyle(e)
      if (e instanceof SVGGeometryElement) { const f = rgba(cs.fill); return !!f && f[3] > 0.5 && Number(cs.opacity) > 0.5 }
      const b = rgba(cs.backgroundColor)
      return !(e instanceof SVGElement) && !!b && b[3] > 0.5
    }
    const occluded = (el: Element, rect: DOMRect) => {
      for (const e of document.elementsFromPoint(rect.left + rect.width / 2, rect.top + rect.height / 2)) {
        if (e === el || el.contains(e) || e.contains(el)) return false
        if (painted(e) && (el.compareDocumentPosition(e) & Node.DOCUMENT_POSITION_FOLLOWING || !(e instanceof SVGElement))) return true
      }
      return false
    }
    const out: { text: string; where: string; fg: string; bg: string; ratio: number }[] = []
    for (const el of Array.from(document.querySelectorAll<Element>('[data-cx]'))) {
      const rect = box(el)
      if (!rect || occluded(el, rect)) continue
      const style = getComputedStyle(el)
      const svgText = el instanceof SVGTextElement
      const fg = rgba(svgText ? style.fill : style.color)
      if (!fg) continue
      let opacity = 1
      for (let node: Element | null = el; node; node = node.parentElement) opacity *= Number(getComputedStyle(node).opacity)
      fg[3] *= opacity
      // The median pixel under the text box is its background.
      const x0 = Math.max(0, Math.floor(rect.left * scale)), y0 = Math.max(0, Math.floor(rect.top * scale))
      const w = Math.max(1, Math.min(canvas.width - x0, Math.round(rect.width * scale))), h = Math.max(1, Math.min(canvas.height - y0, Math.round(rect.height * scale)))
      const data = ctx.getImageData(x0, y0, w, h).data
      const pixels: [number, number, number][] = []
      for (let i = 0; i < data.length; i += 4) pixels.push([data[i]!, data[i + 1]!, data[i + 2]!])
      pixels.sort((a, b) => lum(a) - lum(b))
      let bg = pixels[Math.floor(pixels.length / 2)]!
      // A halo stroke painted under an SVG label is the label's background.
      if (svgText && style.paintOrder.startsWith('stroke') && parseFloat(style.strokeWidth) >= 2) { const halo = rgba(style.stroke); if (halo) bg = over(halo, bg) }
      const shown = over(fg, bg), r = ratio(shown, bg)
      if (r < 4.5) {
        const where = [el.tagName.toLowerCase(), ...Array.from(el.classList)].join('.') + ' in ' + [el.parentElement?.tagName.toLowerCase(), ...Array.from(el.parentElement?.classList ?? [])].join('.')
        out.push({ text: (el.textContent ?? '').trim().slice(0, 60), where, fg: hex(shown), bg: hex(bg), ratio: Math.round(r * 100) / 100 })
      }
    }
    return out
  }, shot)
  await page.evaluate(() => { document.querySelectorAll('[data-cx]').forEach(el => el.removeAttribute('data-cx')); document.querySelectorAll('style').forEach(s => { if (s.textContent?.includes('[data-cx]')) s.remove() }) })
  await page.setViewportSize(viewport)
  return misses
}

for (const theme of ['light', 'dark'] as const) {
  for (const width of [1440, 400] as const) {
    test(`every text on Delivery reaches 4.5:1 in ${theme} at ${width}`, async ({ page }) => {
      await page.setViewportSize({ width, height: 1000 })
      await setup(page, { theme })
      const misses: (ContrastMiss & { view: string })[] = []
      const measure = async (view: string) => { for (const miss of await contrastMisses(page)) misses.push({ view, ...miss }) }
      await page.goto('/p/AEON/delivery')
      await numbersReady(page, 'simple')
      await measure('numbers simple')
      // Learn pinned open: the popover's words on its own surface.
      await page.locator('.learn').first().click()
      await expect(page.locator('.dl-learn')).toBeVisible()
      await measure('numbers simple, Learn open')
      await page.keyboard.press('Escape')
      await head(page).getByRole('radio', { name: 'Expert' }).click()
      await numbersReady(page, 'expert')
      await measure('numbers expert')
      for (const mode of ['live', 'replay', 'compare'] as const) {
        await page.goto(`/p/AEON/delivery?view=flow${mode === 'live' ? '' : `&mode=${mode}`}`)
        await flowReady(page)
        await measure(`flow ${mode}`)
      }
      expect(misses, 'texts under 4.5:1').toEqual([])
    })
  }
}

// ---------- Learn stays open when the page only reports a scroll ----------
// Risk: a scroll event that arrives after the click (the browser scrolling the tapped Learn
// into view, a late layout) closes the pinned words although the button never moved under
// them; only a scroll that moves the button may close them.
const frames = (page: Page) => page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))))
test('pinned Learn words survive a scroll that leaves the button in place and close when the button moves', async ({ page }) => {
  await page.setViewportSize({ width: 400, height: 520 })
  await setup(page)
  await page.goto('/p/AEON/delivery')
  await numbersReady(page, 'simple')
  const main = page.locator('main')
  const learn = page.locator('.learn').last()
  const tip = page.locator('.dl-learn')
  const scrolled = () => main.evaluate(el => el.scrollTop)
  expect(await scrolled(), 'the page starts at the top').toBe(0)
  // The last tile's Learn starts below the fold: the click scrolls it into view and the scroll event lands after the click.
  await learn.click()
  await frames(page)
  expect(await scrolled(), 'the click scrolled the page').toBeGreaterThan(0)
  await expect(tip).toBeVisible()
  await expect(learn).toHaveAttribute('aria-expanded', 'true')
  // A scroll report that moved nothing is no reason to close.
  await main.evaluate(el => el.dispatchEvent(new Event('scroll')))
  await frames(page)
  await expect(tip).toBeVisible()
  await expect(learn).toHaveAttribute('aria-expanded', 'true')
  // A scroll that does move the button closes the words (they are fixed on the body).
  const before = (await learn.boundingBox())!.y
  await main.evaluate(el => { el.scrollTop -= 60 })
  await frames(page)
  expect(Math.abs((await learn.boundingBox())!.y - before), 'the scroll moved the button').toBeGreaterThan(20)
  await expect(tip).toHaveCount(0)
  await expect(learn).toHaveAttribute('aria-expanded', 'false')
})

// ---------- 44 px: every control on a phone ----------
async function smallTargets(page: Page) {
  return page.evaluate(() => {
    const out: string[] = []
    const roots = Array.from(document.querySelectorAll<Element>('.dl, [role="tablist"][aria-label="Project sections"]'))
    for (const root of roots) {
      for (const el of Array.from(root.querySelectorAll<HTMLElement | SVGElement>('button, [role="radio"], [role="tab"], select, a[href], [tabindex="0"], .ln-phhit, .ln-hhit'))) {
        if (el.matches(':disabled') || !el.checkVisibility({ visibilityProperty: true })) continue
        if (el instanceof SVGElement && el.closest('[data-part]') !== el && !el.matches('.ln-phhit, .ln-hhit')) continue
        const r = el.getBoundingClientRect()
        if (r.width < 43.5 || r.height < 43.5) out.push(`${el.tagName.toLowerCase()}.${Array.from(el.classList).join('.')} "${(el.getAttribute('aria-label') ?? el.textContent ?? '').trim().slice(0, 30)}" ${Math.round(r.width)}×${Math.round(r.height)}`)
      }
    }
    return out
  })
}

test('on a phone every Delivery control and the section tabs are at least 44 px', async ({ page }) => {
  await page.setViewportSize({ width: 400, height: 900 })
  await setup(page)
  const found: string[] = []
  await page.goto('/p/AEON/delivery')
  await numbersReady(page, 'simple')
  found.push(...(await smallTargets(page)).map(s => `numbers simple: ${s}`))
  await head(page).getByRole('radio', { name: 'Expert' }).click()
  await numbersReady(page, 'expert')
  found.push(...(await smallTargets(page)).map(s => `numbers expert: ${s}`))
  for (const mode of ['live', 'replay', 'compare'] as const) {
    await page.goto(`/p/AEON/delivery?view=flow${mode === 'live' ? '' : `&mode=${mode}`}`)
    await flowReady(page)
    found.push(...(await smallTargets(page)).map(s => `flow ${mode}: ${s}`))
  }
  expect(found).toEqual([])
})

test('on a phone only the selected project section shows its word; the others keep icon, name and tooltip', async ({ page }) => {
  await page.setViewportSize({ width: 360, height: 800 })
  await setup(page)
  await page.goto('/p/AEON/delivery')
  await numbersReady(page, 'simple')
  const tabs = sections(page).getByRole('tab')
  await expect(tabs).toHaveCount(4)
  for (const name of ['Tickets', 'Knowledge', 'Delivery', 'Settings']) {
    const tab = sections(page).getByRole('tab', { name })
    await expect(tab).toHaveAttribute('aria-label', name)
    await expect(tab).toHaveAttribute('data-tip', name)
    await expect(tab.locator('svg')).toBeVisible()
    if (name === 'Delivery') await expect(tab.locator('.tab-label')).toBeVisible()
    else await expect(tab.locator('.tab-label')).toBeHidden()
  }
  // The four fit the row: nothing overflows, nothing is clipped, the selected word is whole.
  const fit = await sections(page).evaluate(el => ({ over: el.scrollWidth - el.clientWidth, page: document.documentElement.scrollWidth - document.documentElement.clientWidth }))
  expect(fit.over).toBeLessThanOrEqual(1)
  expect(fit.page).toBeLessThanOrEqual(1)
  const word = sections(page).getByRole('tab', { name: 'Delivery' }).locator('.tab-label')
  expect(await word.evaluate(el => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1)
  // The name of an icon-only section is a tooltip on keyboard focus.
  await sections(page).getByRole('tab', { name: 'Delivery' }).focus()
  await page.keyboard.press('ArrowLeft')
  await expect(page).toHaveURL(/\/p\/AEON\/knowledge/)
})

// ---------- Keyboard: one tab stop per group, arrows inside, every group reachable ----------
async function tabWalk(page: Page, steps: number) {
  const seen: string[] = []
  for (let i = 0; i < steps; i++) {
    await page.keyboard.press('Tab')
    seen.push(await page.evaluate(() => {
      const el = document.activeElement as HTMLElement | null
      if (!el) return ''
      const group = el.closest<HTMLElement>('[role="radiogroup"], [role="tablist"]')
      return el.dataset.testid ?? group?.dataset.testid ?? group?.getAttribute('aria-label') ?? el.getAttribute('aria-label') ?? (el.textContent ?? '').trim().slice(0, 20)
    }))
  }
  return seen
}

test('the keyboard reaches every control group of Numbers and Flow, and arrows move inside a group', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1100 })
  await setup(page)
  await page.goto('/p/AEON/delivery')
  await numbersReady(page, 'simple')
  // Every radio group and tab list holds exactly one tab stop.
  for (const group of await page.locator('.dl [role="radiogroup"], .dl [role="tablist"]').all()) {
    await expect(group.locator('[tabindex="0"]')).toHaveCount(1)
  }
  // Learn: focus shows it, Enter pins it, Esc closes it and leaves the focus on the button.
  const learn = page.locator('.learn').first()
  await learn.focus()
  await expect(page.locator('.dl-learn')).toBeVisible()
  await page.keyboard.press('Enter')
  await page.mouse.move(5, 5)
  await expect(page.locator('.dl-learn')).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.locator('.dl-learn')).toHaveCount(0)
  await expect(learn).toBeFocused()
  const views = head(page).getByRole('tablist', { name: 'Delivery views' })
  await views.getByRole('tab', { selected: true }).focus()
  const numbers = await tabWalk(page, 3)
  expect(numbers.slice(0, 2)).toEqual(['Chart window', 'Level of detail'])
  // Arrows choose inside the window group; the focus follows the choice.
  const windows = head(page).getByRole('radiogroup', { name: 'Chart window' })
  await windows.getByRole('radio', { checked: true }).focus()
  await page.keyboard.press('ArrowRight')
  await expect(windows.getByRole('radio', { name: '30 days' })).toBeFocused()
  await expect(windows.getByRole('radio', { name: '30 days' })).toHaveAttribute('aria-checked', 'true')
  await page.keyboard.press('End')
  await expect(windows.getByRole('radio', { name: '365 days' })).toBeFocused()
  await page.keyboard.press('Home')
  await expect(windows.getByRole('radio', { name: '7 days' })).toHaveAttribute('aria-checked', 'true')
  await expect(windows.getByRole('radio', { name: '7 days' })).toBeFocused()

  // Views: arrows move to Flow; there the window keeps its slot but leaves the tab order.
  await views.getByRole('tab', { selected: true }).focus()
  await page.keyboard.press('ArrowRight')
  await expect(page).toHaveURL(/view=flow/)
  await flowReady(page)
  await expect(views.getByRole('tab', { name: 'Flow' })).toBeFocused()
  const flow = await tabWalk(page, 6)
  expect(flow.slice(0, 5)).toEqual(['Level of detail', 'flow-modes', 'Zoom', 'flow-overview', 'flow-lanes'])
  // Mode by arrows; the lanes take arrows, Shift+arrows, Enter and Esc.
  await page.getByTestId('flow-modes').getByRole('radio', { checked: true }).focus()
  await page.keyboard.press('ArrowRight')
  await expect(page).toHaveURL(/mode=replay/)
  await expect(page.getByTestId('flow-modes').getByRole('radio', { name: 'Replay' })).toBeFocused()
  await flowReady(page)
  await lanes(page).focus()
  const before = await clock(page).textContent()
  await page.keyboard.press('Shift+ArrowRight')
  await expect(clock(page)).not.toHaveText(before!)
  // Enter selects the step at the playhead in the focused lane; ↓ moves to the next lane until one has a step there.
  for (let lane = 0; lane < 6 && await lanes(page).locator('.ln-selbox').count() === 0; lane++) {
    await page.keyboard.press('Enter')
    if (await lanes(page).locator('.ln-selbox').count() === 0) await page.keyboard.press('ArrowDown')
  }
  await expect(lanes(page).locator('.ln-selbox')).toHaveCount(1)
  await expect(page.getByTestId('flow-readout')).not.toContainText('Drag the time handle')
  await page.keyboard.press('Escape')
  await expect(lanes(page).locator('.ln-selbox')).toHaveCount(0)
})

// ---------- No shift: window and level, zoom and playhead, at 1440 and at 400 ----------
for (const width of [1440, 400] as const) {
  test(`window and level switches keep every control still at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    await setup(page, { lang: width === 400 ? 'de' : 'en' })
    await page.goto('/p/AEON/delivery')
    await numbersReady(page, 'simple')
    // Width 400 loads a de-AT profile. The app language stays English (AEON-998).
    const windows = head(page).getByRole('radiogroup', { name: 'Chart window' })
    const levels = head(page).getByRole('radiogroup', { name: 'Level of detail' })
    const views = head(page).getByRole('tablist', { name: 'Delivery views' })
    const guard = await controlStability(page, {
      sections: sections(page), views, windows, w7: windows.getByRole('radio').first(), w365: windows.getByRole('radio').last(),
      levels, simple: levels.getByRole('radio').first(), expert: levels.getByRole('radio').last(), updated: page.getByTestId('delivery-updated'),
    })
    for (const days of ['30', '90', '180', '365', '7']) await guard.check(() => windows.getByRole('radio').filter({ hasText: new RegExp(`^${days}$`) }).click())
    await guard.check(() => levels.getByRole('radio').last().click())
    await numbersReady(page, 'expert')
    await guard.check(() => windows.getByRole('radio').filter({ hasText: /^90$/ }).click())
    await guard.check(() => levels.getByRole('radio').first().click())
    await numbersReady(page, 'simple')
    await guard.check(async () => { await windows.getByRole('radio', { checked: true }).focus(); await page.keyboard.press('ArrowRight') })
    guard.done()
  })

  test(`zoom and playhead keep every control still at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 400 ? 2600 : 1400 })
    await setup(page)
    for (const mode of ['live', 'replay'] as const) {
      await page.goto(`/p/AEON/delivery?view=flow${mode === 'live' ? '' : `&mode=${mode}`}`)
      await flowReady(page)
      const bar = page.getByTestId('flow-bar')
      const zoom = bar.getByRole('radiogroup', { name: 'Zoom' })
      const guard = await controlStability(page, {
        modes: page.getByTestId('flow-modes'), head: page.getByTestId('flow-head'), fit: zoom.getByRole('radio', { name: 'Fit all' }), z60: zoom.getByRole('radio', { name: '1 h' }),
        // The clock is a readout before the bar's spacer: its words may change length, the controls after it may not move.
        follow: page.getByTestId('flow-follow'), readout: page.getByTestId('flow-readout'), overview: page.getByTestId('flow-overview'),
        lanes: lanes(page), moment: page.getByTestId('flow-moment'),
        ...(mode === 'live' ? { chip: page.getByTestId('flow-chip') } : { pick: page.getByTestId('flow-pick'), play: page.getByTestId('flow-play') }),
      })
      for (const preset of ['15 min', '1 h', 'Fit all', '30 min']) await guard.check(() => zoom.getByRole('radio', { name: preset }).click())
      // The playhead: dragged by its pill, then a minute at a time, then to the end, then back to follow.
      const pill = await center(page, '[data-testid="flow-playhead"] .ln-phpill')
      await guard.check(async () => { await page.mouse.move(pill.x, pill.y); await page.mouse.down(); await page.mouse.move(pill.x - 60, pill.y, { steps: 6 }); await page.mouse.up() })
      await guard.check(async () => { await lanes(page).focus(); await page.keyboard.press('Shift+ArrowRight') })
      await guard.check(async () => { await lanes(page).focus(); await page.keyboard.press('End') })
      await guard.check(async () => { await lanes(page).focus(); await page.keyboard.press('+') })
      // The overview playhead also moves the time.
      const ov = await center(page, '[data-testid="flow-overview-playhead"]')
      await guard.check(async () => { await page.mouse.move(ov.x, ov.y); await page.mouse.down(); await page.mouse.move(ov.x - 30, ov.y, { steps: 4 }); await page.mouse.up() })
      await guard.check(() => page.getByTestId('flow-follow').click())
      guard.done()
    }
  })
}

// ---------- Evidence: every changed view at 390, 1024 and 1440, light and dark ----------
for (const width of [390, 1024, 1440] as const) {
  for (const theme of ['light', 'dark'] as const) {
    test(`Delivery screenshots at ${width} ${theme}`, async ({ page }, info) => {
      await page.setViewportSize({ width, height: width === 390 ? 900 : 1000 })
      await setup(page, { theme, lang: theme === 'dark' ? 'de' : 'en' })
      const shot = async (name: string) => {
        await page.mouse.move(0, 0)
        // main scrolls, not the page: grow the viewport by what main hides, capture, and restore.
        const viewport = page.viewportSize()!
        const extra = await page.evaluate(() => { const main = document.querySelector('main'); if (main) main.scrollTop = 0; return main ? main.scrollHeight - main.clientHeight : 0 })
        await page.setViewportSize({ width, height: Math.min(viewport.height + extra + 8, 7000) })
        await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))))
        await page.screenshot({ path: info.outputPath(`aeon-994-p7-polish/${name}-${width}-${theme}.png`) })
        await page.setViewportSize(viewport)
      }
      await page.goto('/p/AEON/delivery')
      await numbersReady(page, 'simple')
      await shot('numbers-simple')
      await head(page).getByRole('radio').filter({ hasText: /^Expert$/ }).click()
      await numbersReady(page, 'expert')
      await shot('numbers-expert')
      for (const mode of ['live', 'replay', 'compare'] as const) {
        await page.goto(`/p/AEON/delivery?view=flow${mode === 'live' ? '' : `&mode=${mode}`}`)
        await flowReady(page)
        await shot(`flow-${mode}`)
      }
    })
  }
}
