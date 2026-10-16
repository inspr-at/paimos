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
  // The panel reads tier evidence separately from the sessions list. Keep the
  // capture honest: no invented vendor report and no unmocked backend error.
  await page.route('**/api/projects/*/harness-sessions/*/tier', route => {
    const id = new URL(route.request().url()).pathname.split('/').at(-2)
    const session = data.sessions.find(s => s.id === id)
    if (!session || route.request().method() !== 'GET') return route.fallback()
    return route.fulfill({ json: { session_id: session.id, revision: 0, active_tier: null, pending: null, reports: [], requests: [], read_only: true } })
  })
  return { work, calls, data }
}
// The list launcher remains until a session opens; then the header owns layout.
const dock = (page: Page) => page.locator('[data-layout-cycle]:visible, .session-panel .pane-more')
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
    // check.mjs's row contract also applies to the real table and compact
    // forms. Measure rendered text, including each parent's summary, clipping
    // ranges to overflow bounds so hidden ellipsis text is never counted.
    const rows = [...work.querySelectorAll('.sessions .row[data-row]')].filter(visible)
    const contentOverflow: string[] = [], rowOverlap: string[] = []
    for (let i = 1; i < rows.length; i++) {
      if (rows[i - 1]!.getBoundingClientRect().bottom > rows[i]!.getBoundingClientRect().top + .5) rowOverlap.push(`${rows[i - 1]!.getAttribute('data-row')} / ${rows[i]!.getAttribute('data-row')}`)
    }
    const texts = rows.map(row => {
      const frame = row.getBoundingClientRect()
      const samples: { text: string; left: number; right: number; top: number; bottom: number }[] = []
      const walker = document.createTreeWalker(row, NodeFilter.SHOW_TEXT)
      while (walker.nextNode()) {
        const node = walker.currentNode, parent = node.parentElement!
        if (!node.textContent?.trim() || parent.closest('.sr-only, svg, [aria-hidden="true"]') || !visible(parent)) continue
        const range = document.createRange(); range.selectNodeContents(node)
        for (const rect of range.getClientRects()) {
          let { left, right, top, bottom } = rect
          for (let ancestor: Element | null = parent; ancestor && work.contains(ancestor); ancestor = ancestor.parentElement) {
            const css = getComputedStyle(ancestor), box = ancestor.getBoundingClientRect()
            if (css.overflowX !== 'visible') { left = Math.max(left, box.left); right = Math.min(right, box.right) }
            if (css.overflowY !== 'visible') { top = Math.max(top, box.top); bottom = Math.min(bottom, box.bottom) }
          }
          if (right - left > .5 && bottom - top > .5) {
            const text = node.textContent.trim().slice(0, 40)
            samples.push({ text, left, right, top, bottom })
            if (top < frame.top - .5 || bottom > frame.bottom + .5) contentOverflow.push(text)
          }
        }
      }
      return samples
    })
    const overlaps: string[] = []
    for (let i = 0; i < texts.length; i++) for (let j = i + 1; j < texts.length; j++) for (const a of texts[i]!) for (const b of texts[j]!) {
      if (Math.min(a.right, b.right) - Math.max(a.left, b.left) > .5 && Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top) > .5) overlaps.push(`${a.text} / ${b.text}`)
    }
    return { edges, glyphs, avatars, english, overlaps, contentOverflow, rowOverlap, overflow: document.documentElement.scrollWidth - innerWidth }
  })
}
async function checkRows(page: Page, label: string) {
  const probe = await designProbe(page)
  expect(probe.overlaps, `${label}: no two row texts overlap`).toEqual([])
  expect(probe.contentOverflow, `${label}: row text stays within its own frame`).toEqual([])
  expect(probe.rowOverlap, `${label}: row frames do not overlap`).toEqual([])
  expect(probe.edges).toEqual([]); expect(probe.glyphs).toEqual([]); expect(probe.english).toEqual([]); expect(probe.overflow).toBeLessThanOrEqual(1)
  console.log(`check.mjs (P5 routed adapter): PASS ${label} · row texts do not overlap · no edge accents · SVG icons · English controls · no sideways scroll`)
}

for (const theme of ['light', 'dark'] as const) {
  test(`AEON-1213 ${theme} 1440: one dock, presets, live divider and top-starting panes`, async ({ page }, info) => {
    await page.setViewportSize({ width: 1440, height: 1000 })
    const errors = watchErrors(page), { data, work, calls } = await setup(page, theme)
    let releaseLayout!: () => void
    const layoutRead = new Promise<void>(resolve => { releaseLayout = resolve })
    await page.route('**/api/preferences/**', async route => {
      if (decodeURIComponent(new URL(route.request().url()).pathname).endsWith('/agents:layout') && route.request().method() === 'GET') await layoutRead
      await route.fallback()
    })
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
    await dock(page).evaluate(el => { (window as unknown as { p5Dock: Element }).p5Dock = el })
    await page.keyboard.press('s'); await preset(page, 'focus')
    await expect(page.locator('[data-layout-cycle]')).toBeHidden()
    const stability = await controlStability(page, { preset: dock(page) })
    await stability.check(async () => { await dock(page).click(); await expect(page.getByRole('menuitem', { name: /^Full screen/ })).toBeVisible(); await page.keyboard.press('Escape') })
    expect(await listWidth(page)).toBe(320)
    await expect(page.locator('.sessions .thead')).toBeHidden()
    await page.clock.runFor(500)
    expect(calls.filter(call => call.method === 'PUT' && call.path === '/api/preferences/agents:layout')).toEqual([])
    releaseLayout()
    await checkRows(page, `1440 ${theme} Focus`)
    const stale = page.locator('.sessions .row:has(.bin)').first()
    await expect(stale.locator('.bin')).toBeHidden()
    // A short name is fully readable; a long worker name retains twelve
    // characters plus its ellipsis before the time in the 320 px sidebar.
    for (const name of [stale.locator('.result'), row(page, data.sessions[9]!.id).locator('.result')]) {
      expect(await name.evaluate(el => {
        const text = el.textContent!.trim(), context = document.createElement('canvas').getContext('2d')!
        context.font = getComputedStyle(el).font
        return el.clientWidth - context.measureText(text.length > 12 ? `${text.slice(0, 12)}…` : text).width
      }), 'readable compact name').toBeGreaterThanOrEqual(-.5)
    }
    const actions = stale.getByRole('button', { name: /^Actions for / })
    await page.mouse.move(1400, 950)
    await pane(page).focus()
    await expect(actions).toHaveCSS('opacity', '0')
    const rowGuard = await controlStability(page, { row: stale, name: stale.locator('.result'), time: stale.locator('.t-inline'), actions })
    await rowGuard.check(async () => { await stale.hover(); await expect(actions).toHaveCSS('opacity', '1') })
    await rowGuard.check(async () => { await actions.focus(); await page.mouse.move(1400, 950); await expect(actions).toHaveCSS('opacity', '1') })
    await rowGuard.check(async () => { await actions.click(); await expect(page.getByRole('menuitem', { name: 'Remove stale session', exact: true })).toBeVisible() })
    await page.keyboard.press('Escape')
    rowGuard.done()
    await pane(page).focus()
    await page.screenshot({ path: info.outputPath(`focus-1440-${theme}.png`) })
    await stability.check(async () => { await page.keyboard.press('s'); await preset(page, 'overview') })
    const width = await page.locator('.agents-work').evaluate(el => el.getBoundingClientRect().width)
    expect(await listWidth(page)).toBe(width - 16 - 480)
    await expect(page.locator('.sessions .thead')).toBeVisible()
    const dimensions = await pane(page).evaluate(el => ({ top: el.getBoundingClientRect().top, height: el.getBoundingClientRect().height, want: document.querySelector('main')!.clientHeight - 8, header: document.querySelector('main')!.getBoundingClientRect().top }))
    expect(dimensions.top).toBe(dimensions.header); expect(dimensions.height).toBe(dimensions.want)
    await checkRows(page, `1440 ${theme} Overview`)
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
    const closingScroll = await page.locator('main').evaluate(el => el.scrollTop)
    await stability.check(async () => {
      await dragTo(page, width - 16 - 379, false)
      await expect(page.locator('.agents-page')).toHaveClass(/will-close/)
    })
    stability.done()
    await page.mouse.up(); await preset(page, 'list'); await expect(pane(page)).toHaveCount(0)
    expect(await page.locator('main').evaluate(el => el.scrollTop), 'closing the split preserves page scroll').toBe(closingScroll)
    expect(await dock(page).evaluate(el => el === (window as unknown as { p5Dock: Element }).p5Dock)).toBe(true)
    await expect(page.locator('.session-panel .pane-more')).toHaveCount(0)
    await row(page, data.sessions[1]!.id).click()
    await preset(page, 'focus')
    expect(await listWidth(page)).toBe(280) // the last valid handle, not the closing drag
    await page.keyboard.press('Escape'); await preset(page, 'list')
    await toWork(page)
    const probe = await designProbe(page)
    expect(probe.edges).toEqual([]); expect(probe.glyphs).toEqual([]); expect(probe.english).toEqual([]); expect(probe.overflow).toBeLessThanOrEqual(1)
    expect(probe.avatars.length).toBeGreaterThan(3)
    for (const avatar of probe.avatars) { expect(avatar.name).toBeTruthy(); expect(avatar.badges).toBe(1); expect(avatar.harness).toBe(0) }
    await checkRows(page, `1440 ${theme} List only`)
    await page.screenshot({ path: info.outputPath(`list-1440-${theme}.png`) })
    expect(errors).toEqual([])
  })

  for (const width of [1024, 390]) {
    test(`AEON-1213 ${theme} ${width}: a sheet replaces the split without losing the list`, async ({ page }, info) => {
      await page.setViewportSize({ width, height: 900 })
      const { data } = await setup(page, theme, 320)
      await page.goto(`/agents/${data.sessions[1]!.id}`)
      await expect(pane(page)).toHaveClass(/sheet/)
      await expect(page.locator('[data-layout-cycle]')).toBeHidden(); await expect(pane(page).getByRole('button', { name: 'More session actions' })).toBeVisible()
      await expect(page.locator('.agents-divider')).not.toHaveAttribute('role', 'separator')
      await expect(page.getByRole('button', { name: 'Close session details', exact: true })).toBeVisible()
      const body = pane(page).locator('.scroll:visible')
      await expect.poll(() => body.evaluate(el => el.scrollTop)).toBe(0)
      const guard = await controlStability(page, { back: page.getByRole('button', { name: 'Close session details', exact: true }), frame: pane(page) })
      await guard.check(async () => { await body.evaluate(el => { el.scrollTop = 200 }); await expect.poll(() => body.evaluate(el => el.scrollTop)).toBeGreaterThan(0) })
      await guard.check(async () => { await body.evaluate(el => { el.scrollTop = 0 }); await expect.poll(() => body.evaluate(el => el.scrollTop)).toBe(0) })
      guard.done()
      const box = (await pane(page).boundingBox())!
      if (width === 390) { expect(box.y).toBe(0); expect(box.height).toBe(900); expect((await page.getByRole('button', { name: 'Close session details', exact: true }).boundingBox())!.height).toBeGreaterThanOrEqual(44) }
      expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(1)
      await page.screenshot({ path: info.outputPath(`sheet-${width}-${theme}.png`) })
      console.log(`check.mjs (P5 routed adapter): PASS ${width} ${theme} sheet · opens at top · pinned Back control`)
      // Tick's reference comparison uses 400 px phones and a 1000 px tablet.
      const referenceWidth = width === 390 ? 400 : 1000
      await page.setViewportSize({ width: referenceWidth, height: 1000 })
      await expect(pane(page)).toHaveClass(/sheet/)
      await expect.poll(() => body.evaluate(el => el.scrollTop)).toBe(0)
      await expect(page.locator('[data-layout-cycle]')).toBeHidden(); await expect(pane(page).getByRole('button', { name: 'More session actions' })).toBeVisible()
      // The visual viewport updates the phone sheet on its next animation frame.
      if (referenceWidth === 400) await expect(pane(page)).toHaveCSS('height', '1000px')
      expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(1)
      await page.screenshot({ path: info.outputPath(`${referenceWidth === 1000 ? '1000-sheet' : 'sheet'}-${referenceWidth}-${theme}.png`) })
      await page.setViewportSize({ width, height: 900 })
      await page.keyboard.press('s'); await expect(pane(page)).toHaveClass(/sheet/)
      await page.keyboard.press('Escape'); await expect(pane(page)).toHaveCount(0)
      await expect(row(page, data.sessions[1]!.id)).toBeFocused()
      await row(page, data.sessions[1]!.id).click()
      await expect(pane(page)).toHaveClass(/sheet/)
      await expect.poll(() => pane(page).locator('.scroll:visible').evaluate(el => el.scrollTop)).toBe(0)
      await page.keyboard.press('Escape'); await expect(pane(page)).toHaveCount(0)
      await toWork(page)
      const probe = await designProbe(page)
      expect(probe.edges).toEqual([]); expect(probe.glyphs).toEqual([]); expect(probe.english).toEqual([]); expect(probe.overflow).toBeLessThanOrEqual(1)
      await checkRows(page, `${width} ${theme} list`)
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
  await page.keyboard.press('f'); await expect(search).toBeFocused()
  await search.press('Escape')
  await separator.focus(); await page.keyboard.press('s'); await preset(page, 'list')
  await page.keyboard.press('s'); await preset(page, 'focus')
  await separator.focus(); await page.keyboard.press('Escape'); await preset(page, 'list')
  await expect(pane(page)).toHaveCount(0)
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
  await page.goto(`/agents/${data.sessions[1]!.id}?tab=messages`)
  await expect(pane(page)).toHaveClass(/docked/)
  expect(await page.locator('.agents-list-pane').evaluate(el => getComputedStyle(el).transitionDuration)).toBe('0.24s')
  await page.emulateMedia({ reducedMotion: 'reduce' })
  expect(await page.locator('.agents-list-pane').evaluate(el => getComputedStyle(el).transitionDuration)).toBe('0s')
  // Measure the work area itself, independent of the shell's responsive gutter.
  await page.locator('.agents-work').evaluate(el => { (el as HTMLElement).style.width = '1055px' })
  await expect(pane(page)).toHaveClass(/sheet/); await expect(page.locator('[data-layout-cycle]')).toBeHidden(); await expect(pane(page).getByRole('button', { name: 'More session actions' })).toBeVisible()
  await page.locator('.agents-work').evaluate(el => { (el as HTMLElement).style.width = '1056px' })
  await expect(pane(page)).toHaveClass(/docked/); await expect(dock(page)).toBeVisible()
  await page.locator('.agents-work').evaluate(el => { (el as HTMLElement).style.removeProperty('width') })
  // Fix6: a pane with an uninitialized viewport height can pass visibility
  // checks while its composer is below the fold. Resize through both pane
  // forms, then prove typing keeps the entire composer at the pane bottom.
  const composer = pane(page).locator('.compose textarea')
  const footer = pane(page).locator('.composer')
  for (const { width, height } of [{ width: 1440, height: 860 }, { width: 1280, height: 720 }, { width: 1024, height: 1000 }, { width: 390, height: 900 }, { width: 1440, height: 1000 }]) {
    await page.setViewportSize({ width, height })
    await expect(pane(page)).toHaveClass(width >= 1280 ? /docked/ : /sheet/)
    await expect(footer).toBeInViewport({ ratio: 1 })
    const geometry = await pane(page).evaluate(el => {
      const frame = el.getBoundingClientRect(), footer = el.querySelector('.composer')!.getBoundingClientRect()
      return { gap: frame.bottom - footer.bottom, inset: parseFloat(getComputedStyle(el).borderBottomWidth) + parseFloat(getComputedStyle(el).paddingBottom) }
    })
    expect(Math.abs(geometry.gap - geometry.inset), `${width}: composer pinned to pane bottom`).toBeLessThanOrEqual(.5)
    // AEON-1438: the field grows upward with a long draft; its bottom edge and the actions stay.
    // On phones, typing during a running turn opens the composer's second row by design
    // (Send now · After this turn), so there only the header and the pinned footer are held.
    const phone = width <= 720
    const guard = await controlStability(page, { ...(phone ? {} : { composer, send: pane(page).locator('.compose .send') }), stop: pane(page).getByRole('button', { name: 'More session actions', exact: true }) }, { grows: ['composer'] })
    for (const draft of ['Die vollständigen Berechtigungseinstellungen für sämtliche angeschlossenen Arbeitsbereiche zuverlässig prüfen.', '']) {
      await guard.check(async () => {
        await composer.fill(draft); await composer.blur()
        await expect(footer).toBeInViewport({ ratio: 1 })
        const gap = await pane(page).evaluate(el => el.getBoundingClientRect().bottom - el.querySelector('.composer')!.getBoundingClientRect().bottom - parseFloat(getComputedStyle(el).borderBottomWidth) - parseFloat(getComputedStyle(el).paddingBottom))
        expect(Math.abs(gap), `${width}: composer stays pinned while typing`).toBeLessThanOrEqual(.5)
      })
    }
    guard.done()
  }
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

// Data loss: a failed GET is not an empty shared preference. The local handle
// must survive the outage without replacing folded_projects on the server.
test('AEON-1213 regression: failed layout reads retain the local handle until recovery merges shared fields', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const { data, work, calls } = await setup(page, 'light', 420)
  let recover = false, reads = 0, release!: () => void
  const recovery = new Promise<void>(resolve => { release = resolve })
  await page.route('**/api/preferences/**', async route => {
    if (decodeURIComponent(new URL(route.request().url()).pathname).endsWith('/agents:layout') && route.request().method() === 'GET') {
      reads++
      if (!recover) return route.fulfill({ status: 503, json: { error: 'unavailable' } })
      await recovery
    }
    await route.fallback()
  })
  const failedRead = page.waitForResponse(response => decodeURIComponent(new URL(response.url()).pathname).endsWith('/agents:layout') && response.status() === 503)
  await page.goto(`/agents/${data.sessions[1]!.id}`)
  await (await failedRead).finished()
  await expect(pane(page)).toHaveClass(/docked/)
  const separator = page.getByRole('separator', { name: 'Resize the session list' })
  const before = await listWidth(page)
  await separator.focus(); await page.keyboard.press('ArrowRight')
  await expect.poll(() => listWidth(page)).toBe(before + 16)
  await page.clock.runFor(500)
  const writes = () => calls.filter(call => call.method === 'PUT' && call.path === '/api/preferences/agents:layout')
  expect(writes()).toEqual([])
  expect(work.preferences['agents:layout']).toEqual({ handle: 420, folded_projects: ['keep-this-preference'] })

  recover = true
  const previousReads = reads
  await page.keyboard.press('ArrowRight')
  await expect.poll(() => reads).toBeGreaterThan(previousReads)
  await page.clock.runFor(500)
  expect(writes()).toEqual([])
  await expect.poll(() => listWidth(page)).toBe(before + 32)
  release()
  await expect.poll(() => page.evaluate(async () => {
    const path = '/src/lib/preferences.ts'
    const { usePreference } = await import(path)
    return usePreference('agents:layout').value.value?.handle
  })).toBe(before + 32)
  await page.clock.runFor(500)
  await expect.poll(() => work.preferences['agents:layout']).toEqual({ handle: before + 32, folded_projects: ['keep-this-preference'] })
  expect(writes()).toHaveLength(1)
})

// Folding after a failed GET must stay local until recovery supplies the shared
// handle and previous folds. Changes during that recovery keep their latest intent.
test('AEON-1213 regression: failed layout reads keep fold intents local until recovery preserves shared fields', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const { data, work, calls } = await setup(page, 'light', 420)
  const stored = { handle: 420, folded_projects: ['p-aeon'], extension: { keep: true } }
  work.preferences['agents:layout'] = structuredClone(stored)
  let recover = false, reads = 0, release!: () => void
  const recovery = new Promise<void>(resolve => { release = resolve })
  await page.route('**/api/preferences/**', async route => {
    if (decodeURIComponent(new URL(route.request().url()).pathname).endsWith('/agents:layout') && route.request().method() === 'GET') {
      reads++
      if (!recover) return route.fulfill({ status: 503, json: { error: 'unavailable' } })
      await recovery
    }
    await route.fallback()
  })
  const failedRead = page.waitForResponse(response => decodeURIComponent(new URL(response.url()).pathname).endsWith('/agents:layout') && response.status() === 503)
  await page.goto('/agents')
  await (await failedRead).finished()
  const lead = row(page, data.sessions[0]!.id), worker = row(page, data.sessions[9]!.id)
  const fold = lead.locator('.tree-fold')
  await expect(worker).toBeVisible()
  const guard = await controlStability(page, { fold, lead })
  await guard.check(async () => { await fold.click(); await expect(worker).toHaveCount(0) })
  await page.clock.runFor(500)
  const writes = () => calls.filter(call => call.method === 'PUT' && call.path === '/api/preferences/agents:layout')
  expect(writes()).toEqual([])
  expect(work.preferences['agents:layout']).toEqual(stored)
  await expect.poll(() => reads).toBe(2)

  recover = true
  await guard.check(async () => { await fold.click(); await expect(worker).toBeVisible() })
  await expect.poll(() => reads).toBe(3)
  await guard.check(async () => { await fold.click(); await expect(worker).toHaveCount(0) })
  await page.clock.runFor(500)
  expect(writes()).toEqual([])
  expect(work.preferences['agents:layout']).toEqual(stored)
  expect(reads).toBe(3)
  release()
  await expect.poll(() => work.preferences['agents:layout']).toEqual({ ...stored, folded_projects: ['p-aeon', 'p-pharos'] })
  expect(writes()).toHaveLength(1)
  await expect(worker).toHaveCount(0)
  guard.done()
})

test('AEON-1213 regression: delayed fold recovery cannot follow a person switch', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const { data, work, calls } = await setup(page, 'light', 420)
  let reads = 0, release!: () => void
  const recovery = new Promise<void>(resolve => { release = resolve })
  await page.route('**/api/preferences/**', async route => {
    if (decodeURIComponent(new URL(route.request().url()).pathname).endsWith('/agents:layout') && route.request().method() === 'GET') {
      reads++
      if (reads === 1) return route.fulfill({ status: 503, json: { error: 'unavailable' } })
      if (reads === 2) await recovery
    }
    await route.fallback()
  })
  const failedRead = page.waitForResponse(response => decodeURIComponent(new URL(response.url()).pathname).endsWith('/agents:layout') && response.status() === 503)
  await page.goto('/agents')
  await (await failedRead).finished()
  const lead = row(page, data.sessions[0]!.id), worker = row(page, data.sessions[9]!.id)
  await expect(worker).toBeVisible()
  await lead.locator('.tree-fold').click(); await expect(worker).toHaveCount(0)
  await page.clock.runFor(500)
  const writes = () => calls.filter(call => call.method === 'PUT' && call.path === '/api/preferences/agents:layout')
  expect(writes()).toEqual([])
  await expect.poll(() => reads).toBe(2)
  const nextPerson = { handle: 660, folded_projects: ['p-aeon'] }
  work.preferences['agents:layout'] = structuredClone(nextPerson)
  await page.evaluate(async () => {
    const sessionPath = '/src/stores/session.ts', prefPath = '/src/lib/preferences.ts'
    const { useSession } = await import(sessionPath), { setPreferenceOwner } = await import(prefPath)
    const session = useSession(), identity = { ...session.identity, principal: { ...session.identity.principal, id: '22222222-2222-4222-8222-222222222222' } }
    setPreferenceOwner(identity); session.identity = identity
  })
  await expect.poll(() => reads).toBe(3)
  await expect(worker).toBeVisible()
  release()
  await page.clock.runFor(500)
  expect(writes()).toEqual([])
  expect(work.preferences['agents:layout']).toEqual(nextPerson)
  await expect(lead.locator('.tree-fold')).toHaveAttribute('aria-expanded', 'true')
})

// Approved table contract: fields and tracks must move together, so removing a
// column cannot put model/computer/beat content under the wrong heading.
test('AEON-1213 regression: the table uses the approved columns with the ticket in the session name', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const { data, work } = await setup(page)
  const ticket = work.nodes.find(node => node.id === data.sessions[1]!.ticket_node_id)!
  Object.assign(data.sessions[1]!, { ticket: { id: ticket.id, key: ticket.key, title: ticket.title } })
  await page.goto('/agents')
  const worker = row(page, data.sessions[1]!.id)
  await expect(worker).toBeVisible()
  await expect(page.locator('.sessions .thead [role="columnheader"]')).toHaveText(['Session', 'Time', 'Execution', 'Computer', 'Beat', 'Actions'])
  await expect(worker.locator('.l1 .key-inline')).toHaveText(`${ticket.key} ·`)
  await expect(worker.locator('.l1 .key-inline')).toBeVisible()
  await expect(page.locator('.sessions .c-ticket, .sessions .c-tier')).toHaveCount(0)
  const header = page.locator('.sessions .thead')
  for (const cell of ['c-time', 'c-exec', 'c-host', 'c-beat']) {
    const heading = (await header.locator(`.${cell}`).boundingBox())!
    const body = (await worker.locator(`.${cell}`).boundingBox())!
    expect(body.x).toBeCloseTo(heading.x, 1)
    expect(body.width).toBeCloseTo(heading.width, 1)
  }
  await page.keyboard.press('s'); await preset(page, 'focus')
  await page.keyboard.press('s'); await preset(page, 'overview')
  await checkRows(page, '1440 light Overview column regression')
})

test.describe('touch', () => {
  test.use({ hasTouch: true })
  test('AEON-1213: the preset, divider hit area and phone Back target are 44 px', async ({ page }, info) => {
    await page.setViewportSize({ width: 1440, height: 1000 })
    const { data } = await setup(page, 'dark')
    await page.goto(`/agents/${data.sessions[1]!.id}`)
    const panelMore = pane(page).getByRole('button', { name: 'More session actions', exact: true })
    await expect(panelMore).toBeVisible()
    const box = (await panelMore.boundingBox())!
    expect(box.width).toBeGreaterThanOrEqual(44); expect(box.height).toBeGreaterThanOrEqual(44)
    const hitWidth = await page.locator('.agents-divider').evaluate(el => { const css = getComputedStyle(el, '::after'); return el.getBoundingClientRect().width - parseFloat(css.left) - parseFloat(css.right) })
    expect(hitWidth).toBeGreaterThanOrEqual(44)
    await dragTo(page, 320)
    const more = page.locator('.sessions .row').first().getByRole('button', { name: /^Actions for / })
    await expect(more).toHaveCSS('opacity', '1')
    const moreBox = (await more.boundingBox())!
    expect(moreBox.width).toBeGreaterThanOrEqual(44); expect(moreBox.height).toBeGreaterThanOrEqual(44)
    await expect(page.locator('.sessions .bin').first()).toBeHidden()
    await page.screenshot({ path: info.outputPath('1440-dark-touch.png') })
    await pane(page).locator('.scroll:visible').evaluate(el => { el.scrollTop = 200 })
    await expect.poll(() => pane(page).locator('.scroll:visible').evaluate(el => el.scrollTop)).toBeGreaterThan(0)
    await page.setViewportSize({ width: 390, height: 900 })
    await expect(pane(page)).toHaveClass(/sheet/)
    await expect.poll(() => pane(page).locator('.scroll:visible').evaluate(el => el.scrollTop)).toBe(0)
    const back = (await page.getByRole('button', { name: 'Close session details', exact: true }).boundingBox())!
    expect(back.width).toBeGreaterThanOrEqual(44); expect(back.height).toBeGreaterThanOrEqual(44)
    expect(await pane(page).evaluate(el => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1)
    await page.screenshot({ path: info.outputPath('390-dark-touch-sheet.png') })
  })
})
