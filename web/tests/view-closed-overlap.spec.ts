// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1559 risk: teleported settings bypass toolbar container queries and
// overlap in a docked list; toggling or threshold jitter must not move controls.
import { expect, test, type Locator, type Page } from '@playwright/test'
import { writeFile } from 'node:fs/promises'
import { fixtures, mockWork } from './work-fixtures'
import { expectStableControls } from './helpers/stable'

const navigation = (page: Page) => page.locator('.project-navigation')
const settings = (page: Page) => page.locator('#project-view-settings .view-settings')
const step = async (page: Page) => Number(await settings(page).getAttribute('data-navigation-step'))

async function setup(page: Page, theme = 'light', pane = false) {
  await page.setViewportSize({ width: pane ? 2200 : 1600, height: 1000 })
  const data = fixtures()
  data.preferences.theme = { choice: theme }
  data.preferences['list:display'] = { headerGraph: false }
  data.projects[0]!.title = 'Pharos · Berechtigungsverwaltung'
  data.projects[0]!.description = 'Überprüfung der mandantenübergreifenden Berechtigungsverwaltung für sämtliche verantwortlichen Personen und Agenten.'
  await mockWork(page, data)
  await page.route('**/api/queue?*', route => route.fulfill({ json: { items: [], manual_order: false, capacity: { queued_hours: 0, parallel_runs: 0, work_hours: 0, warning: false } } }))
  await page.goto(pane ? '/p/PHAROS/PHAROS-11' : '/p/PHAROS')
  await expect(settings(page)).toBeVisible()
  if (pane) {
    await expect(page.getByRole('complementary', { name: 'Ticket details' })).toBeVisible()
    // The same user-selected dock width at every size. Resize the viewport to
    // produce the requested list width, rather than faking the navigation size.
    await page.locator('.project-page').evaluate(el => (el as HTMLElement).style.setProperty('--panel-user-w', '560px'))
  }
  await page.evaluate(() => document.fonts.ready)
}

async function settle(page: Page) {
  // ResizeObserver delivers after animation frames; sample after its render.
  await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))))
}

async function fitRow(page: Page, width: number) {
  for (let attempt = 0; attempt < 6; attempt++) {
    await settle(page)
    const actual = (await navigation(page).boundingBox())!.width
    if (Math.abs(actual - width) < 0.5) break
    await page.setViewportSize({ width: Math.round(page.viewportSize()!.width + width - actual), height: 1000 })
  }
  await settle(page)
  expect((await navigation(page).boundingBox())!.width).toBeCloseTo(width, 0)
}

async function geometry(page: Page) {
  return navigation(page).evaluate(row => {
    const rect = (el: Element) => {
      const r = el.getBoundingClientRect()
      return { x: r.x, y: r.y, right: r.right, bottom: r.bottom, width: r.width, height: r.height }
    }
    const visible = (el: Element) => { const r = rect(el); return r.width > 0 && r.height > 0 && getComputedStyle(el).visibility !== 'hidden' }
    const intersects = (a: ReturnType<typeof rect>, b: ReturnType<typeof rect>) => a.x < b.right && a.right > b.x && a.y < b.bottom && a.bottom > b.y
    const group = row.querySelector('.view-settings')!
    const controls = [...group.querySelectorAll('.closed-switch, .closed-pill, .hide-gear, .display-btn')].filter(visible).map(rect)
    const frame = rect(row)
    const switchLabel = group.querySelector('.closed-switch')!
    const track = switchLabel.querySelector('input')!
    const trackBox = rect(track)
    const labelBox = rect(switchLabel.querySelector('.hide-label-slot')!)
    const pseudo = getComputedStyle(track, '::after')
    const transform = pseudo.transform === 'none' ? new DOMMatrix() : new DOMMatrix(pseudo.transform)
    const knobWidth = parseFloat(pseudo.width), knobHeight = parseFloat(pseudo.height)
    const left = pseudo.left === 'auto' ? trackBox.width - parseFloat(pseudo.right) - knobWidth : parseFloat(pseudo.left)
    const knob = { x: trackBox.x + left + transform.m41, y: trackBox.y + parseFloat(pseudo.top) + transform.m42, width: knobWidth, height: knobHeight }
    const switchShown = visible(track)
    const children = [...row.children].filter(visible).map(rect)
    return {
      step: Number(group.getAttribute('data-navigation-step')),
      controls: controls.length,
      controlOverlaps: controls.flatMap((a, i) => controls.slice(i + 1).filter(b => intersects(a, b))),
      outside: controls.filter(r => r.x < frame.x || r.right > frame.right),
      wrapped: children.some(r => Math.abs((r.y + r.height / 2) - (children[0]!.y + children[0]!.height / 2)) > 1),
      trackLabelIntersect: switchShown && intersects(trackBox, labelBox),
      gap: switchShown ? labelBox.x - trackBox.right : null,
      knobInside: !switchShown || (Number.isFinite(knob.x) && knob.width > 0 && knob.height > 0 && knob.x >= trackBox.x && knob.x + knob.width <= trackBox.right && knob.y >= trackBox.y && knob.y + knob.height <= trackBox.bottom),
    }
  })
}

async function assertGeometry(page: Page) {
  const result = await geometry(page)
  expect(result.controls).toBeGreaterThan(0)
  expect(result.controlOverlaps).toEqual([])
  expect(result.outside).toEqual([])
  expect(result.wrapped).toBe(false)
  expect(result.trackLabelIntersect).toBe(false)
  expect(result.knobInside).toBe(true)
  if (result.gap !== null) expect(result.gap).toBe(6)
  return result
}

async function stableToggle(page: Page) {
  const form = await step(page)
  if (form === 4) await settings(page).locator('.display-btn').click()
  const toggle = form === 4 ? page.locator('.menu-switch input') : form < 2 ? settings(page).locator('.closed-switch input') : settings(page).locator('.closed-pill')
  const controls: Record<string, Locator> = { sections: navigation(page).locator('.sections'), attention: navigation(page).locator('.attention-view-link'), display: settings(page).locator('.display-btn'), toggle }
  // Sample every currently visible row action, including the clicked label,
  // the gear and saved-view links, while ignoring inert/overflowed content.
  const actions = navigation(page).locator('button, a, .closed-switch, .hide-control')
  for (let i = 0; i < await actions.count(); i++) if (await actions.nth(i).isVisible()) controls[`row-${i}`] = actions.nth(i)
  const boxes = async () => Object.fromEntries(await Promise.all(Object.entries(controls).map(async ([name, el]) => [name, await el.boundingBox()])))
  const before = await boxes()
  await expectStableControls({ controls, scrollAreas: { row: navigation(page) }, interactions: [false, true].map(hidden => ({ name: hidden ? 'hide closed' : 'show closed', run: async () => {
    await toggle.click()
    if (form < 2 || form === 4) await expect(toggle).toBeChecked({ checked: hidden })
    else await expect(toggle).toHaveAttribute('aria-pressed', String(hidden))
    await expect(page).toHaveURL(hidden ? /^(?!.*[?&]closed=1)/ : /[?&]closed=1/)
    await expect(settings(page)).toHaveAttribute('data-navigation-step', String(form))
    await assertGeometry(page)
    expect(await boxes(), 'every row control moves exactly 0 px').toEqual(before)
  } })) })
  if (form === 4) await page.keyboard.press('Escape')
}

for (const theme of ['light', 'dark']) {
  test(`AEON-1559 navigation settings fit docked and undocked list widths in ${theme}`, async ({ page }, testInfo) => {
    const checks = []
    for (const pane of [false, true]) {
      await setup(page, theme, pane)
      for (const width of [1440, 1280, 1100, 1000, 900, 760]) {
        await fitRow(page, width)
        const result = await assertGeometry(page)
        if (width === 1000) expect(result.step).toBeGreaterThanOrEqual(1)
        if (width === 1000) expect(result.step).toBeLessThanOrEqual(2)
        if (width === 760) expect(result.step).toBeGreaterThanOrEqual(3)
        await stableToggle(page)
        checks.push({ width, pane, theme, ...result, movementPx: 0 })
        if ([1440, 1000, 760].includes(width)) await page.screenshot({ path: testInfo.outputPath(`aeon1559-${width}-${pane ? 'open' : 'closed'}-${theme}.png`) })
      }
    }
    const json = JSON.stringify(checks, null, 2)
    await writeFile(testInfo.outputPath(`aeon1559-checks-${theme}.json`), json)
    await testInfo.attach(`aeon1559-checks-${theme}`, { body: json, contentType: 'application/json' })
    // The guarded remote runner returns Markdown as well as PNG artifacts.
    await writeFile(testInfo.outputPath(`aeon1559-checks-${theme}.md`), `\`\`\`json\n${json}\n\`\`\`\n`)
  })
}

test('AEON-1559 all five forms have stable toggles and 16 px resize hysteresis', async ({ page }) => {
  await setup(page)
  // Each threshold has a 16px band [threshold, threshold + 16). Neither
  // approach changes form within it: ±8px around its centre cannot flap.
  for (const [index, threshold] of [1100, 960, 820, 720].entries()) {
    await fitRow(page, threshold + 40)
    await expect(settings(page)).toHaveAttribute('data-navigation-step', String(index))
    for (const width of [threshold + 16, threshold + 8, threshold, threshold + 8]) {
      await fitRow(page, width)
      expect(await step(page)).toBe(index)
    }
    await fitRow(page, threshold - 1)
    expect(await step(page)).toBe(index + 1)
    for (const width of [threshold, threshold + 8, threshold + 15]) {
      await fitRow(page, width)
      expect(await step(page)).toBe(index + 1)
    }
    await stableToggle(page)
    await fitRow(page, threshold + 16)
    expect(await step(page)).toBe(index)
  }
})

test('AEON-1559 non-teleported toolbar retains its 800 px container swap', async ({ page }) => {
  await setup(page)
  await page.evaluate(async () => {
    const path = '/src/components/work/ListToolbar.vue', filtersPath = '/src/lib/ticketList.ts'
    const compiled = await (await fetch(path)).text()
    const vuePath = /from ["']([^"']*\/vue\.js[^"']*)["']/.exec(compiled)![1]!
    const { createApp, h } = await import(vuePath)
    const { default: Toolbar } = await import(path)
    const { filtersFromQuery } = await import(filtersPath)
    const host = document.createElement('div')
    host.id = 'toolbar-regression'
    host.style.cssText = 'position:fixed;top:500px;left:0;width:820px;container:toolbar / inline-size'
    document.body.append(host)
    const root = document.querySelector('#app') as Element & { __vue_app__: { config: { globalProperties: { $pinia: unknown } } } }
    const app = createApp({ render: () => h(Toolbar, { filters: filtersFromQuery({}), options: () => [], label: () => '', total: 0, loading: false, density: 'compact', stuck: false, view: 'list' }) })
    app.use(root.__vue_app__.config.globalProperties.$pinia)
    app.mount(host)
  })
  const toolbar = page.locator('#toolbar-regression .toolbar')
  await expect(toolbar.locator('.view-settings')).toBeVisible()
  await expect(toolbar.locator('.view-settings')).not.toHaveClass(/navigation-settings/)
  for (const [width, pill] of [[820, false], [799, true], [820, false]] as const) {
    await toolbar.locator('..').evaluate((el, width) => { (el as HTMLElement).style.width = `${width}px` }, width)
    await settle(page)
    await expect(toolbar.locator('.closed-pill')).toBeVisible({ visible: pill })
    await expect(toolbar.locator('.closed-switch')).toBeVisible({ visible: !pill })
  }
})

for (const theme of ['light', 'dark']) test(`AEON-1559 phone and standard viewport evidence in ${theme}`, async ({ page }, testInfo) => {
  await setup(page, theme)
  for (const width of [1440, 1024, 390]) {
    await page.setViewportSize({ width, height: 1000 })
    await settle(page)
    await expect(settings(page)).toBeVisible()
    expect(await navigation(page).evaluate(el => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1)
    if (width === 390) {
      expect(await step(page)).toBe(4)
      await settings(page).locator('.display-btn').click()
      await expect(page.locator('.menu-switch input')).toBeVisible()
      await page.keyboard.press('Escape')
    }
    await page.screenshot({ path: testInfo.outputPath(`aeon1559-viewport-${width}-${theme}.png`) })
  }
})
