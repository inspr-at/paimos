// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1196 r7 check.mjs, adapted to the real routed application for package P5.
// Risk: presets move their trigger, resizing hides features or loses the picked row,
// details open scrolled down, and an old person's handle/selection or pending save leaks.
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { controlStability } from './control-stability'

const NOW = Date.parse('2026-10-11T10:00:00Z')
async function setup(page: Page, theme: 'light' | 'dark' = 'light', handle?: number) {
  await page.clock.install({ time: NOW })
  const work = fixtures()
  work.preferences.theme = { choice: theme }
  work.preferences['ui.agents.sections'] = { dial: false }
  work.preferences['agents:layout'] = { ...(handle === undefined ? {} : { handle }), folded_projects: ['keep-this-preference'] }
  const calls = await mockWork(page, work, { admin: true })
  const data = agentData({ now: NOW, me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' } })
  data.approvals = []
  data.sessions.forEach(s => Object.assign(s, { model: 'gpt-6.1-sol', reasoning_effort: 'xhigh', account_label: 'ChatGPT Pro' }))
  Object.assign(data.sessions[4]!, { brief: 'Die Sicherungskopien aller Produktionsserver prüfen und die Ergebnisse mit den vorherigen Protokollen vergleichen. '.repeat(20) })
  // A long list and long German content test actual scrolling and text bounds.
  for (let i = 20; i < 42; i++) data.sessions.push({ ...data.sessions[1]!, id: `5e000000-0000-4000-8000-0000000000${i}`, display_label: `Worker ${i}: Sicherungskopien auf allen Produktionsservern überprüfen`, ticket_node_id: null, ticket: null, run_id: null, parent_harness_session_id: data.sessions[0]!.id })
  await mockAgents(page, data)
  return { work, calls, data }
}
const dock = (page: Page) => page.locator('[data-layout-cycle]')
const pane = (page: Page) => page.locator('.session-panel')
const row = (page: Page, id: string) => page.locator(`.sessions [data-row="s:${id}"]`)
const listWidth = (page: Page) => page.locator('.agents-list-pane').evaluate(el => el.getBoundingClientRect().width)
const preset = (page: Page, name: string) => expect(page.locator('.agents-work')).toHaveAttribute('data-layout', name)
async function toWork(page: Page) {
  await page.locator('.agents-work').evaluate(el => { const main = el.closest('main')!; main.scrollTop += el.getBoundingClientRect().top - main.getBoundingClientRect().top })
}
async function dragTo(page: Page, width: number, release = true) {
  const divider = page.locator('.agents-divider'), box = (await divider.boundingBox())!
  const left = await page.locator('.agents-work').evaluate(el => el.getBoundingClientRect().left)
  const y = Math.max(box.y + 20, Math.min(box.y + box.height - 20, 450))
  await page.mouse.move(box.x + 8, y); await page.mouse.down()
  await page.mouse.move(left + width + 8, y, { steps: 5 })
  if (release) await page.mouse.up()
}
async function designProbe(page: Page) {
  return page.locator('.agents-work').evaluate(work => {
    const visible = (el: Element) => { const r = el.getBoundingClientRect(), cs = getComputedStyle(el); return r.width > 0 && r.height > 0 && cs.visibility !== 'hidden' && cs.display !== 'none' }
    const edges: string[] = [], glyphs: string[] = []
    for (const el of work.querySelectorAll('*')) {
      if (!visible(el)) continue
      const css = getComputedStyle(el)
      for (const [a, b] of [['left', 'right'], ['top', 'bottom']]) {
        const aw = parseFloat(css.getPropertyValue(`border-${a}-width`)), bw = parseFloat(css.getPropertyValue(`border-${b}-width`))
        if (aw >= 2 && aw - bw >= 1 || bw >= 2 && bw - aw >= 1) edges.push(String(el.className))
      }
      for (const node of el.childNodes) if (node.nodeType === 3 && /[←→↑↓↵⏎✓✔✕✖×▾▸▴▲▼◂▶◀★☆●○◉⚠✗✘➜➔⟶]/.test(node.textContent ?? '')) glyphs.push(node.textContent ?? '')
    }
    const avatars = [...work.querySelectorAll('.row .ava')].filter(visible).map(el => ({ name: el.getAttribute('aria-label'), badges: el.querySelectorAll('svg.agent-state-mark').length, harness: el.querySelectorAll('.harness-badge').length }))
    const english = (work.querySelector('.th-stick') as HTMLElement).innerText.match(/[äöüÄÖÜß]|\b(und|der|die|das|nicht|mit|für|oder|ist|Sitzung|Agenten|Projekt|Fehler|bitte)\b/g) ?? []
    return { edges, glyphs, avatars, english, overflow: document.documentElement.scrollWidth - innerWidth }
  })
}

for (const theme of ['light', 'dark'] as const) {
  test(`AEON-1213 ${theme} 1440: one dock, presets, live divider and top-starting panes`, async ({ page }, info) => {
    await page.setViewportSize({ width: 1440, height: 1000 })
    const errors = watchErrors(page), { data, work } = await setup(page, theme)
    await page.goto('/agents')
    await expect(row(page, data.sessions[0]!.id)).toBeVisible()
    await toWork(page)
    const dockBox = (await dock(page).boundingBox())!
    for (const amount of [400, 1000]) {
      await page.locator('main').evaluate((el, amount) => { el.scrollTop += amount }, amount)
      const after = (await dock(page).boundingBox())!
      for (const dimension of ['x', 'y', 'width', 'height'] as const) expect(Math.abs(after[dimension] - dockBox[dimension])).toBeLessThanOrEqual(.5)
    }
    await toWork(page)
    const stability = await controlStability(page, { preset: dock(page) })
    await dock(page).evaluate(el => { (window as unknown as { p5Dock: Element }).p5Dock = el })
    await stability.check(async () => { await dock(page).click(); await preset(page, 'focus') })
    expect(await listWidth(page)).toBe(320)
    await expect(page.locator('.sessions .thead')).toBeHidden()
    await page.screenshot({ path: info.outputPath(`focus-1440-${theme}.png`) })
    await stability.check(async () => { await dock(page).click(); await preset(page, 'overview') })
    const width = await page.locator('.agents-work').evaluate(el => el.getBoundingClientRect().width)
    expect(await listWidth(page)).toBe(width - 16 - 480)
    await expect(page.locator('.sessions .thead')).toBeVisible()
    const dimensions = await pane(page).evaluate(el => ({ top: el.getBoundingClientRect().top, height: el.getBoundingClientRect().height, want: document.querySelector('main')!.clientHeight - 8, header: document.querySelector('main')!.getBoundingClientRect().top }))
    expect(dimensions.top).toBe(dimensions.header); expect(dimensions.height).toBe(dimensions.want)
    await page.screenshot({ path: info.outputPath(`overview-1440-${theme}.png`) })
    // The divider is continuous; crossing 560 changes the rows, without moving the dock.
    await stability.check(async () => { await dragTo(page, 559); expect(await listWidth(page)).toBe(559) })
    await expect(page.locator('.sessions .thead')).toBeHidden()
    await stability.check(async () => { await dragTo(page, 560); expect(await listWidth(page)).toBe(560) })
    await expect(page.locator('.sessions .thead')).toBeVisible()
    await page.clock.runFor(500)
    await expect.poll(() => work.preferences['agents:layout']?.handle).toBe(560)
    expect(work.preferences['agents:layout']?.folded_projects).toEqual(['keep-this-preference'])
    await stability.check(async () => { await dragTo(page, 50); expect(await listWidth(page)).toBe(280) })
    await dragTo(page, width - 16 - 480)
    const body = page.locator('.sessions .table')
    await body.evaluate(el => { el.scrollTop = el.scrollHeight })
    const last = page.locator('.sessions .row[data-row]').last(), lastId = (await last.getAttribute('data-row'))!.slice(2)
    await expect(last).toBeInViewport()
    const before = (await last.boundingBox())!.y, pageScroll = await page.locator('main').evaluate(el => el.scrollTop)
    await last.click()
    await expect(page).toHaveURL(new RegExp(lastId))
    expect((await last.boundingBox())!.y).toBeCloseTo(before, 1)
    expect(await page.locator('main').evaluate(el => el.scrollTop)).toBe(pageScroll)
    await page.screenshot({ path: info.outputPath(`pick-last-row-1440-${theme}.png`) })
    await pane(page).locator('.scroll:visible').evaluate(el => { el.scrollTop = 200 })
    await expect.poll(() => pane(page).locator('.scroll:visible').evaluate(el => el.scrollTop)).toBeGreaterThan(0)
    await body.evaluate(el => { el.scrollTop = 0 })
    await row(page, data.sessions[1]!.id).click()
    await expect.poll(() => pane(page).locator('.scroll:visible').evaluate(el => el.scrollTop)).toBe(0)
    await dragTo(page, 280)
    await stability.check(async () => {
      await dragTo(page, width - 16 - 379, false)
      await expect(page.locator('.agents-page')).toHaveClass(/will-close/)
      await page.mouse.up(); await preset(page, 'list'); await expect(pane(page)).toHaveCount(0)
    })
    expect(await dock(page).evaluate(el => el === (window as unknown as { p5Dock: Element }).p5Dock)).toBe(true)
    stability.done()
    await row(page, data.sessions[1]!.id).click()
    await preset(page, 'focus')
    expect(await listWidth(page)).toBe(280) // the last valid handle, not the closing drag
    await page.keyboard.press('Escape'); await preset(page, 'list')
    await toWork(page)
    const probe = await designProbe(page)
    expect(probe.edges).toEqual([]); expect(probe.glyphs).toEqual([]); expect(probe.english).toEqual([]); expect(probe.overflow).toBeLessThanOrEqual(1)
    expect(probe.avatars.length).toBeGreaterThan(3)
    for (const avatar of probe.avatars) { expect(avatar.name).toBeTruthy(); expect(avatar.badges).toBe(1); expect(avatar.harness).toBe(0) }
    await page.screenshot({ path: info.outputPath(`list-1440-${theme}.png`) })
    expect(errors).toEqual([])
  })

  for (const width of [1024, 390]) {
    test(`AEON-1213 ${theme} ${width}: a sheet replaces the split without losing the list`, async ({ page }, info) => {
      await page.setViewportSize({ width, height: 900 })
      const { data } = await setup(page, theme, 320)
      await page.goto(`/agents/${data.sessions[1]!.id}`)
      await expect(pane(page)).toHaveClass(/sheet/)
      await expect(dock(page)).toBeHidden()
      await expect(page.locator('.agents-divider')).not.toHaveAttribute('role', 'separator')
      await expect(page.getByRole('button', { name: 'Back to the list', exact: true })).toBeVisible()
      const guard = await controlStability(page, { back: page.getByRole('button', { name: 'Back to the list', exact: true }), frame: pane(page) })
      await guard.check(async () => { await pane(page).locator('.scroll:visible').evaluate(el => { el.scrollTop = 200 }) })
      guard.done()
      const box = (await pane(page).boundingBox())!
      if (width === 390) { expect(box.y).toBe(0); expect(box.height).toBe(900); expect((await page.getByRole('button', { name: 'Back to the list', exact: true }).boundingBox())!.height).toBeGreaterThanOrEqual(44) }
      expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(1)
      await page.screenshot({ path: info.outputPath(`sheet-${width}-${theme}.png`) })
      // Tick's reference comparison uses 400 px phones and a 1000 px tablet.
      const referenceWidth = width === 390 ? 400 : 1000
      await page.setViewportSize({ width: referenceWidth, height: 1000 })
      await expect(pane(page)).toHaveClass(/sheet/)
      await expect(dock(page)).toBeHidden()
      expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(1)
      await page.screenshot({ path: info.outputPath(`${referenceWidth === 1000 ? '1000-sheet' : 'sheet'}-${referenceWidth}-${theme}.png`) })
      await page.setViewportSize({ width, height: 900 })
      await page.keyboard.press('s'); await expect(pane(page)).toHaveClass(/sheet/)
      await page.keyboard.press('Escape'); await expect(pane(page)).toHaveCount(0)
      await expect(row(page, data.sessions[1]!.id)).toBeFocused()
      await toWork(page)
      const probe = await designProbe(page)
      expect(probe.edges).toEqual([]); expect(probe.glyphs).toEqual([]); expect(probe.english).toEqual([]); expect(probe.overflow).toBeLessThanOrEqual(1)
      await page.screenshot({ path: info.outputPath(`list-${width}-${theme}.png`) })
    })
  }
}

test('AEON-1213: registered F, numpad slash, S and divider keys respect fields and native keys', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const { data } = await setup(page)
  await page.goto('/agents')
  await expect(row(page, data.sessions[0]!.id)).toBeVisible()
  const search = page.getByRole('searchbox', { name: 'Search sessions in this list' })
  await page.keyboard.press('f'); await expect(search).toBeFocused()
  await search.press('s'); await preset(page, 'list')
  await search.press('Escape'); await search.press('Escape')
  await page.evaluate(() => document.body.dispatchEvent(new KeyboardEvent('keydown', { key: '/', code: 'NumpadDivide', bubbles: true, cancelable: true })))
  await expect(search).toBeFocused(); await expect(page.locator('dialog.palette[open]')).toHaveCount(0)
  await search.press('Escape')
  await page.keyboard.press('s'); await preset(page, 'focus')
  await page.keyboard.press('s'); await preset(page, 'overview')
  const separator = page.getByRole('separator', { name: 'Resize the session list' })
  await separator.focus(); await page.keyboard.press('Home')
  expect(await listWidth(page)).toBe(280)
  await page.keyboard.press('ArrowRight'); expect(await listWidth(page)).toBe(296)
  await page.keyboard.press('ArrowLeft'); expect(await listWidth(page)).toBe(280)
  await page.keyboard.press('End')
  const workWidth = await page.locator('.agents-work').evaluate(el => el.getBoundingClientRect().width)
  expect(await listWidth(page)).toBe(workWidth - 16 - 380)
  const native = await page.evaluate(() => ['s', 'r', 'd', 'p', 'a'].map(key => {
    const event = new KeyboardEvent('keydown', { key, ctrlKey: true, bubbles: true, cancelable: true }); document.body.dispatchEvent(event); return event.defaultPrevented
  }))
  expect(native).toEqual([false, false, false, false, false])
})

test('AEON-1213: a person change closes details and cancels the previous handle save', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const { data, work, calls } = await setup(page, 'light', 420)
  await page.goto(`/agents/${data.sessions[1]!.id}`)
  await preset(page, 'focus'); await expect.poll(() => listWidth(page)).toBe(420)
  await page.getByRole('separator', { name: 'Resize the session list' }).focus()
  await page.keyboard.press('ArrowRight'); expect(await listWidth(page)).toBe(436)
  // Switch before the preference debounce, then allow the old queued work to run.
  work.preferences['agents:layout'] = { handle: 660 }
  await page.evaluate(async () => {
    const sessionPath = '/src/stores/session.ts', prefPath = '/src/lib/preferences.ts'
    const { useSession } = await import(sessionPath), { setPreferenceOwner } = await import(prefPath)
    const session = useSession(), identity = { ...session.identity, principal: { ...session.identity.principal, id: '22222222-2222-4222-8222-222222222222' } }
    setPreferenceOwner(identity); session.identity = identity
  })
  await expect(page).toHaveURL(/\/agents$/); await expect(pane(page)).toHaveCount(0)
  await page.clock.runFor(500)
  expect(calls.filter(call => call.method === 'PUT' && call.path === '/api/preferences/agents:layout')).toEqual([])
  await row(page, data.sessions[1]!.id).click()
  await expect.poll(() => listWidth(page)).toBe(660)
})

test('AEON-1213: the work-area threshold and 240 ms presets honor reduced motion', async ({ page }) => {
  const errors = watchErrors(page)
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.emulateMedia({ reducedMotion: 'no-preference' })
  const { data } = await setup(page)
  await page.goto(`/agents/${data.sessions[1]!.id}`)
  await expect(pane(page)).toHaveClass(/docked/)
  expect(await page.locator('.agents-list-pane').evaluate(el => getComputedStyle(el).transitionDuration)).toBe('0.24s')
  await page.emulateMedia({ reducedMotion: 'reduce' })
  expect(await page.locator('.agents-list-pane').evaluate(el => getComputedStyle(el).transitionDuration)).toBe('0s')
  // Measure the work area itself, independent of the shell's responsive gutter.
  await page.locator('.agents-work').evaluate(el => { (el as HTMLElement).style.width = '1055px' })
  await expect(pane(page)).toHaveClass(/sheet/); await expect(dock(page)).toBeHidden()
  await page.locator('.agents-work').evaluate(el => { (el as HTMLElement).style.width = '1056px' })
  await expect(pane(page)).toHaveClass(/docked/); await expect(dock(page)).toBeVisible()
  expect(errors).toEqual([])
})

test('AEON-1213: a failed handle save is visible without moving the preset', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const { data } = await setup(page)
  await page.route('**/api/preferences/**', route => {
    if (decodeURIComponent(new URL(route.request().url()).pathname).endsWith('/agents:layout') && route.request().method() === 'PUT') return route.fulfill({ status: 503, json: { error: 'unavailable' } })
    return route.fallback()
  })
  await page.goto(`/agents/${data.sessions[1]!.id}`)
  await expect(pane(page)).toHaveClass(/docked/)
  const guard = await controlStability(page, { preset: dock(page) })
  await guard.check(async () => {
    await dragTo(page, 420)
    await page.clock.runFor(500)
    await expect(page.getByRole('status').filter({ hasText: 'The layout could not be saved' })).toBeVisible()
  })
  guard.done()
})

test.describe('touch', () => {
  test.use({ hasTouch: true })
  test('AEON-1213: the preset, divider hit area and phone Back target are 44 px', async ({ page }, info) => {
    await page.setViewportSize({ width: 1440, height: 1000 })
    const { data } = await setup(page, 'dark')
    await page.goto(`/agents/${data.sessions[1]!.id}`)
    await expect(dock(page)).toBeVisible()
    const box = (await dock(page).boundingBox())!
    expect(box.width).toBeGreaterThanOrEqual(44); expect(box.height).toBeGreaterThanOrEqual(44)
    const hitWidth = await page.locator('.agents-divider').evaluate(el => { const css = getComputedStyle(el, '::after'); return el.getBoundingClientRect().width - parseFloat(css.left) - parseFloat(css.right) })
    expect(hitWidth).toBeGreaterThanOrEqual(44)
    await page.screenshot({ path: info.outputPath('1440-dark-touch.png') })
    await page.setViewportSize({ width: 390, height: 900 })
    await expect(pane(page)).toHaveClass(/sheet/)
    const back = (await page.getByRole('button', { name: 'Back to the list', exact: true }).boundingBox())!
    expect(back.width).toBeGreaterThanOrEqual(44); expect(back.height).toBeGreaterThanOrEqual(44)
    expect(await pane(page).evaluate(el => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1)
    await page.screenshot({ path: info.outputPath('390-dark-touch-sheet.png') })
  })
})
