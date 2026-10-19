// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1459 (design aeon-1279-compact-chrome-r2, Tick spec v1): the shared compact chrome and the
// Agents list on phones and short screens. Risks: the header wraps or lets rows show through; a
// footer bar comes back on compact; the Sessions toolbar takes two lines; the toolbar starts too
// low to show rows; the header sticks in short landscape and starves the list; the scroller
// resizes while a finger scrolls; search or the held chip move the toolbar or the rows; a target
// drops under 44 px; desktop 1440 drifts from main. Every assert is also written to a checks
// file (size and theme per entry) beside the screenshots, for the hand-over evidence.
import { test, expect, type Browser, type CDPSession, type Locator, type Page, type TestInfo } from '@playwright/test'
import { mkdir, writeFile } from 'node:fs/promises'
import { dirname } from 'node:path'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { agentData, mockAgents, type AgentData } from './agents-fixtures'
import { capacityWorld } from './capacity-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { mockReleases, presentedHistory } from './releases-fixtures'

type Size = { width: number; height: number }
type Look = 'light' | 'dark'
type Box = { x: number; y: number; width: number; height: number }
const NOW = Date.parse('2026-10-16T08:00:00Z')
const PORTRAIT: Size = { width: 390, height: 664 }
const LANDSCAPE: Size = { width: 844, height: 340 }
const WIDE_LANDSCAPE: Size = { width: 932, height: 380 }
const NARROWEST: Size = { width: 320, height: 568 }
const ROWS = '.sessions .table [role="row"][data-row]'
const sizeName = (size: Size) => `${size.width}x${size.height}`
const short = (size: Size) => size.height <= 500 && size.width >= 720

// ---------- Evidence: every assert, with its size and theme ----------
type Entry = { check: string; size: string; theme: Look; value: unknown; expected: string; pass: boolean }
function checks(info: TestInfo, size: Size, theme: Look) {
  const entries: Entry[] = []
  return {
    is(check: string, value: unknown, expected: string, pass: boolean) {
      entries.push({ check, size: sizeName(size), theme, value, expected, pass })
      expect(pass, `${check} at ${sizeName(size)} ${theme}: ${JSON.stringify(value)}, expected ${expected}`).toBe(true)
    },
    async write(name: string) {
      const path = info.outputPath(`AEON-1459/checks-${name}.md`)
      await mkdir(dirname(path), { recursive: true })
      await writeFile(path, ['```json', JSON.stringify(entries, null, 1), '```', ''].join('\n'))
    },
  }
}
const shot = (page: Page, info: TestInfo, size: Size, theme: Look, state: string) => page.screenshot({ path: info.outputPath(`AEON-1459/aeon1459-${sizeName(size)}-${theme}-${state}.png`) })
const box = (locator: Locator) => locator.first().evaluate(el => {
  const r = el.getBoundingClientRect()
  return { x: +r.x.toFixed(2), y: +r.y.toFixed(2), width: +r.width.toFixed(2), height: +r.height.toFixed(2) }
})
const inside = (b: Box, size: Size) => b.x >= -.5 && b.y >= -.5 && b.x + b.width <= size.width + .5 && b.y + b.height <= size.height + .5
const atFoot = (b: Box, size: Size) => b.x <= .5 && Math.abs(b.width - size.width) <= .5 && Math.abs(b.y + b.height - size.height) <= .5

// ---------- The world: about 30 sessions in three project groups, as the design draws the list ----------
// A lead with its workers, and a named release. `accounts` adds readable accounts and computers (the
// capacity line names them); 1440 keeps the world it was measured in on main.
async function agentsWorld(page: Page, size: Size, look: Look, options: { accounts?: boolean; sessions?: number } = {}) {
  await page.setViewportSize(size)
  await page.emulateMedia({ colorScheme: look, reducedMotion: 'reduce' })
  await page.clock.install({ time: NOW })
  const work = fixtures({ bigProject: 40 })
  work.preferences.theme = { choice: look }
  // The dial folded, so the list starts at the same place on every machine (the open dial's text wraps by font).
  work.preferences['ui.agents.sections'] = { dial: false }
  await mockWork(page, work, { admin: true })
  const data = agentData({ now: NOW, me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' }, nodes: {} })
  const base = data.sessions[1]!, lead = data.sessions[0]!.id
  for (let n = 0; n < (options.sessions ?? 24); n++) data.sessions.push({ ...base, id: `5e000000-0000-4000-8000-${String(100 + n).padStart(12, '0')}`, display_label: `worker-${n + 1}`, parent_harness_session_id: n < 12 ? lead : null } as typeof base)
  if (options.accounts) {
    const capacity = capacityWorld()
    data.accounts = (capacity.accounts as unknown as typeof data.accounts).map(account => ({ ...account, registered_by_principal_id: me.id }))
    await mockAgents(page, data, { capacity })
    await page.route('**/api/me/permissions*', route => {
      const answer = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
      answer.workspace.permissions.push('account.read', 'account.manage')
      return route.fulfill({ json: answer })
    })
  } else await mockAgents(page, data)
  const history = presentedHistory(NOW)
  await mockReleases(page, history, { codename: 'Myriad Maser' })
  return { work, data, history }
}
async function openAgents(page: Page, size: Size, look: Look, options: { accounts?: boolean; sessions?: number } = {}) {
  const world = await agentsWorld(page, size, look, options)
  await page.goto('/agents')
  await expect(page.locator(ROWS).nth(20)).toBeAttached()
  await page.evaluate(() => document.fonts.ready.then(() => undefined))
  return world
}
const main = (page: Page) => page.locator('main')
const toolbar = (page: Page) => page.locator('.sessions .th-stick')
const scrollTo = (page: Page, top: number) => main(page).evaluate((el, y) => { el.scrollTop = y }, top)
// The toolbar's offset in the page's scroller: scrolling past it sticks the toolbar.
const toolbarOffset = (page: Page) => page.evaluate(() => {
  const scroller = document.querySelector('main')!, stick = document.querySelector<HTMLElement>('.sessions .th-stick')!
  return stick.getBoundingClientRect().top - scroller.getBoundingClientRect().top + scroller.scrollTop
})
// Rows wholly on screen and not covered: the row is what the point hits at its top and its bottom.
const fullyVisible = (page: Page) => page.evaluate(selector => [...document.querySelectorAll<HTMLElement>(selector)].filter(row => {
  const b = row.getBoundingClientRect(), x = b.left + Math.min(b.width / 2, 120)
  if (b.height === 0 || b.top < 0 || b.bottom > innerHeight) return false
  const top = document.elementFromPoint(x, b.top + 1), bottom = document.elementFromPoint(x, b.bottom - 1)
  return !!top && !!bottom && row.contains(top) && row.contains(bottom)
}).length, ROWS)
// Every visible control in the chrome: the header, the page's head and the toolbar.
const chromeTargets = (page: Page) => page.evaluate(() => {
  const out: { name: string; width: number; height: number }[] = []
  for (const root of ['.app-header', '.agents-page .page-head', '.sessions .th-stick']) for (const el of document.querySelectorAll<HTMLElement>(`${root} :is(button, a[href], input, [role="button"])`)) {
    if (!el.checkVisibility({ visibilityProperty: true, opacityProperty: true }) || el.closest('.sr-only, [inert], [aria-hidden="true"]')) continue
    const r = el.getBoundingClientRect()
    if (r.width === 0 || r.height === 0) continue
    out.push({ name: el.getAttribute('aria-label') || el.textContent?.trim() || el.className, width: +r.width.toFixed(1), height: +r.height.toFixed(1) })
  }
  return out
})
// Every visible child of a line sits inside it: nothing wraps onto a second line.
const oneLine = (line: Locator) => line.evaluate(el => {
  const h = el.getBoundingClientRect()
  return [...el.querySelectorAll<HTMLElement>(':scope > *, :scope > .list-tools > *')]
    .filter(child => child.checkVisibility() && getComputedStyle(child).position !== 'absolute' && getComputedStyle(child).display !== 'contents')
    .filter(child => { const r = child.getBoundingClientRect(); return r.height > 0 && (r.top < h.top - .5 || r.bottom > h.bottom + .5) })
    .map(child => child.className || child.tagName)
})
// Opaque: the same pixels whether what lies behind it is shown or hidden. A mismatch keeps both shots.
async function opaque(page: Page, target: Locator, info?: TestInfo, name = 'probe') {
  const clip = await box(target)
  const before = await page.screenshot({ clip, animations: 'disabled', caret: 'hide' })
  await page.addStyleTag({ content: '.sessions .table, .agents-page .layout { visibility: hidden !important; } /* aeon-1459-probe */' })
  const after = await page.screenshot({ clip, animations: 'disabled', caret: 'hide' })
  await page.evaluate(() => document.querySelectorAll('style').forEach(style => { if (style.textContent?.includes('aeon-1459-probe')) style.remove() }))
  // Pixels that differ (any channel by more than 6 of 255; a 94 % bar over text changes about 12): rows behind a see-through bar
  // change thousands of them; antialiasing noise between two shots of the same pixels changes a few.
  const changed = before.equals(after) ? 0 : await page.evaluate(async ([a, b]) => {
    const pixels = async (base64: string) => {
      const bitmap = await createImageBitmap(await (await fetch(`data:image/png;base64,${base64}`)).blob())
      const canvas = new OffscreenCanvas(bitmap.width, bitmap.height), ctx = canvas.getContext('2d')!
      ctx.drawImage(bitmap, 0, 0)
      return ctx.getImageData(0, 0, bitmap.width, bitmap.height).data
    }
    const [x, y] = await Promise.all([pixels(a!), pixels(b!)])
    let count = 0
    for (let i = 0; i < x.length; i += 4) if (Math.max(Math.abs(x[i]! - y[i]!), Math.abs(x[i + 1]! - y[i + 1]!), Math.abs(x[i + 2]! - y[i + 2]!)) > 6) count++
    return count
  }, [before.toString('base64'), after.toString('base64')] as const)
  const same = changed <= Math.max(2, Math.round(clip.width * clip.height * .0005))
  if (changed && info) {
    await mkdir(info.outputPath('AEON-1459'), { recursive: true })
    await writeFile(info.outputPath(`AEON-1459/opaque-${name}-shown.png`), before)
    await writeFile(info.outputPath(`AEON-1459/opaque-${name}-hidden.png`), after)
  }
  return { same, changed }
}

// ---------- C1 + C2: the Agents page at the top and while reading ----------
const C1: { size: Size; looks: Look[]; rows: number; topMax: number; scrolled: Look[] }[] = [
  { size: PORTRAIT, looks: ['light', 'dark'], rows: 9, topMax: 180, scrolled: ['light'] },
  { size: LANDSCAPE, looks: ['light', 'dark'], rows: 5, topMax: 110, scrolled: ['light', 'dark'] },
  { size: WIDE_LANDSCAPE, looks: ['light'], rows: 6, topMax: 110, scrolled: [] },
  { size: NARROWEST, looks: ['light'], rows: 7, topMax: 180, scrolled: [] },
]
for (const { size, looks, rows, topMax, scrolled } of C1) for (const look of looks) {
  test(`AEON-1459 C1/C2 agents ${sizeName(size)} ${look}: 44 px opaque header, one-line toolbar, no footer, rows first`, async ({ page }, info) => {
    const errors = watchErrors(page)
    await openAgents(page, size, look)
    const c = checks(info, size, look)
    try {
      const header = page.locator('header.app-header')
      const headerBox = await box(header)
      c.is('header height', headerBox.height, '44', Math.abs(headerBox.height - 44) <= .5)
      const headerWrap = await oneLine(header)
      c.is('header one line (nothing wraps)', headerWrap, '[]', headerWrap.length === 0)
      const headerAlpha = await header.evaluate(el => { const c = getComputedStyle(el).backgroundColor; const m = /rgba?\(([^)]+)\)/.exec(c); const parts = m ? m[1]!.split(',').map(Number) : []; return parts.length === 4 ? parts[3]! : 1 })
      c.is('header background alpha', headerAlpha, '1', headerAlpha === 1)
      const footers = await page.locator('footer.app-footer').count()
      c.is('no footer element on compact', footers, '0', footers === 0)
      const bar = toolbar(page), line = page.locator('.sessions .tree-head')
      const [barBox, lineBox] = await Promise.all([box(bar), box(line)])
      c.is('toolbar height', barBox.height, '44', Math.abs(barBox.height - 44) <= .5)
      c.is('toolbar line height', lineBox.height, '44', Math.abs(lineBox.height - 44) <= .5)
      const wrapped = await oneLine(line)
      c.is('toolbar parts inside its one line', wrapped, '[]', wrapped.length === 0)
      const columnHead = await page.locator('.sessions .thead').isVisible()
      c.is('no column head', columnHead, 'false', !columnHead)
      c.is('toolbar top before scrolling', barBox.y, `≤ ${topMax}`, barBox.y <= topMax)
      const status = page.locator('.sessions .compact-status')
      if (size.width >= 380) {
        await expect(status).toBeVisible()
        const role = await status.getAttribute('role'), name = await status.getAttribute('aria-label') ?? ''
        c.is('status slot: role=status, words in the name', `${role} · ${name}`, 'status · "N working…"', role === 'status' && /^\d+ working/.test(name))
      } else {
        const shown = await status.isVisible()
        c.is('status slot steps aside under 380 px', shown, 'false', !shown)
      }
      if (size.width <= 390) {
        const small = (await chromeTargets(page)).filter(target => target.width < 43.5 || target.height < 43.5)
        c.is('chrome targets ≥ 44 × 44', small, '[]', small.length === 0)
      }
      const sideways = await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)
      c.is('no sideways scroll', sideways, '≤ 0', sideways <= 0)
      await shot(page, info, size, look, 'top')

      // C2: reading. The toolbar sticks; the chrome is 88 px in portrait and 44 px in short landscape.
      const base = Math.round(await toolbarOffset(page) + 320)
      const step = short(size) ? 12 : 14
      const counts: number[] = []
      for (const k of [0, 1, 2, 3]) { await scrollTo(page, base + k * step); counts.push(await fullyVisible(page)) }
      c.is('fully visible rows at four scroll offsets', counts, `each ≥ ${rows}`, counts.every(n => n >= rows))
      const stuck = await box(bar)
      const chrome = stuck.y + stuck.height
      c.is('chrome while reading', chrome, short(size) ? '44' : '88', Math.abs(chrome - (short(size) ? 44 : 88)) <= .5)
      const barOpaque = await opaque(page, bar, info, 'toolbar')
      c.is('toolbar opaque over rows (pixels changed when the rows behind are hidden)', barOpaque.changed, '≈ 0 (antialiasing only)', barOpaque.same)
      if (short(size)) {
        await scrollTo(page, 60)
        const top = await box(header)
        c.is('short landscape: header off-screen after scrolling 60 px', top.y + top.height, '≤ 0', top.y + top.height <= .5)
        await scrollTo(page, base)
        const sticky = (await box(bar)).y
        c.is('short landscape: toolbar sticks at the top', sticky, '0', Math.abs(sticky) <= .5)
      } else {
        const headerOpaque = await opaque(page, header, info, 'header')
        c.is('header opaque (pixels changed when the page behind is hidden)', headerOpaque.changed, '≈ 0 (antialiasing only)', headerOpaque.same)
        const top = (await box(header)).y
        c.is('portrait: the header stays while reading', top, '0', Math.abs(top) <= .5)
      }
      if (scrolled.includes(look)) await shot(page, info, size, look, 'scrolled')
      expect(errors).toEqual([])
    } finally { await c.write(`c1-${sizeName(size)}-${look}`) }
  })
}

// ---------- C3: search opens over the toolbar; nothing moves ----------
test('AEON-1459 C3 agents 390x664 light: search opens over the toolbar line and Cancel clears it, nothing moves', async ({ page }, info) => {
  await openAgents(page, PORTRAIT, 'light')
  const c = checks(info, PORTRAIT, 'light')
  try {
    const watch = { toolbar: toolbar(page), row1: page.locator(ROWS).nth(0), row2: page.locator(ROWS).nth(1), row3: page.locator(ROWS).nth(2) }
    const measure = async () => Object.fromEntries(await Promise.all(Object.entries(watch).map(async ([name, locator]) => [name, await box(locator)] as const))) as Record<string, Box>
    const moved = (a: Record<string, Box>, b: Record<string, Box>) => Math.max(...Object.keys(a).flatMap(name => (['x', 'y', 'width', 'height'] as const).map(key => Math.abs(a[name]![key] - b[name]![key]))))
    const before = await measure()
    const open = page.getByRole('button', { name: 'Search sessions', exact: true })
    await open.click()
    const field = page.getByRole('searchbox', { name: 'Search sessions in this list' })
    await expect(field).toBeFocused()
    const opened = moved(before, await measure())
    c.is('search opens: movementPx of the toolbar and the first rows', opened, '0', opened <= .01)
    const fieldBox = await box(field)
    c.is('search field height', fieldBox.height, '44', Math.abs(fieldBox.height - 44) <= .5)
    const font = await field.evaluate(el => parseFloat(getComputedStyle(el).fontSize))
    c.is('search field text size', font, '16', font === 16)
    const overlay = await box(page.locator('.sessions .sess-search')), bar = await box(toolbar(page))
    c.is('the overlay sits in the toolbar box', overlay, `inside ${JSON.stringify(bar)}`, overlay.y >= bar.y - .5 && overlay.y + overlay.height <= bar.y + bar.height + .5)
    await field.fill('worker-1')
    await expect(page).toHaveURL(/q=worker-1/)
    await shot(page, info, PORTRAIT, 'light', 'search')
    await page.getByRole('button', { name: 'Cancel', exact: true }).click()
    await expect(page).not.toHaveURL(/q=/)
    await expect(field).toBeHidden()
    const value = await page.locator('#sessions-q').inputValue()
    c.is('Cancel clears the query', value, '""', value === '')
    const closed = moved(before, await measure())
    c.is('search closes: movementPx of the toolbar and the first rows', closed, '0', closed <= .01)
    await expect(open).toBeFocused()
  } finally { await c.write('c3-390x664-light') }
})

// ---------- C4: the ⋯ menus ----------
test('AEON-1459 C4 agents 390x664 light: the head ⋯ leads with Wind down, the Sessions ⋯ holds the list tools', async ({ page }, info) => {
  await openAgents(page, PORTRAIT, 'light')
  const c = checks(info, PORTRAIT, 'light')
  try {
    await page.getByRole('button', { name: 'More agent actions', exact: true }).click()
    const head = page.getByRole('menu').filter({ has: page.getByRole('menuitem', { name: /^Model preferences/ }) })
    await expect(head).toBeVisible()
    const first = ((await head.getByRole('menuitem').first().textContent()) ?? '').replace(/\s+/g, ' ').trim()
    c.is('head ⋯: Wind down… first', first, '"Wind down… Finish what runs, start nothing new"', first.startsWith('Wind down…') && first.includes('Finish what runs, start nothing new'))
    const headBox = await box(page.locator('.floating').filter({ has: head }))
    c.is('head ⋯ stays inside the viewport', headBox, 'inside 390 × 664', inside(headBox, PORTRAIT))
    await shot(page, info, PORTRAIT, 'light', 'head-menu')
    await page.keyboard.press('Escape')
    await expect(head).toBeHidden()

    await page.getByRole('button', { name: 'More list actions', exact: true }).click()
    const list = page.getByRole('menu', { name: 'More list actions' })
    await expect(list).toBeVisible()
    const names = (await list.getByRole('menuitem').allTextContents()).map(text => text.replace(/\s+/g, ' ').trim())
    for (const label of ['Display…', 'Collapse all', 'History']) c.is(`Sessions ⋯ holds ${label}`, names, `an item ${label}`, names.some(name => name.startsWith(label) || (label === 'Collapse all' && name.startsWith('Expand all'))))
    const heights = await list.getByRole('menuitem').evaluateAll(items => items.map(item => +item.getBoundingClientRect().height.toFixed(1)))
    c.is('Sessions ⋯ items ≥ 44 px', heights, 'each ≥ 44', heights.every(h => h >= 43.5))
    const sheet = await box(page.locator('.floating').filter({ has: list }))
    c.is('Sessions ⋯ is a full-width sheet on the phone', sheet, 'x 0, width 390, bottom 664', atFoot(sheet, PORTRAIT))
    await shot(page, info, PORTRAIT, 'light', 'list-menu')
    await list.getByRole('menuitem', { name: /^Display…/ }).click()
    await expect(page.getByRole('radiogroup', { name: 'Group by' })).toBeVisible()
    await page.keyboard.press('Escape')
  } finally { await c.write('c4-390x664-light') }
})

// ---------- C5: the capacity line and its sheet ----------
for (const size of [PORTRAIT, LANDSCAPE]) test(`AEON-1459 C5 agents ${sizeName(size)} light: the capacity line opens the dial and the accounts as a sheet`, async ({ page }, info) => {
  await openAgents(page, size, 'light', { accounts: true })
  const c = checks(info, size, 'light')
  try {
    const line = page.locator('.agents-page .cap-line')
    await expect(line).toBeVisible()
    // The accounts and computers read land after the dial's.
    await expect(line).toContainText('computer')
    const text = (await line.innerText()).replace(/\s+/g, ' ').trim()
    c.is('capacity line copy', text, '"N agents · N running · N accounts · N computers"', /^\d+ agents? · \d+ running · \d+ accounts? · \d+ computers?$/.test(text))
    const raw = await line.locator('.cl-t').evaluate(el => el.textContent ?? '')
    c.is('NBSP between numbers and words; "·" never at a line edge', raw, 'no plain space beside a number or a dot', !/\d [a-z]/.test(raw) && !/ ·|· /.test(raw))
    const lineBox = await box(line)
    c.is('capacity line height', lineBox.height, '44', Math.abs(lineBox.height - 44) <= .5)
    await expect(page.locator('.agents-page .cap-row')).toBeHidden()
    await line.click()
    const sheet = page.getByRole('dialog', { name: 'Capacity' })
    await expect(sheet).toBeVisible()
    await expect(sheet.locator('.cdial')).toBeVisible()
    await expect(sheet.getByRole('button', { name: /The full dial/ })).toBeVisible()
    const sheetBox = await box(sheet)
    if (size === PORTRAIT) c.is('capacity sheet full width at the foot', sheetBox, 'x 0, width 390, bottom 664', atFoot(sheetBox, size))
    else c.is('capacity sheet inside the viewport', sheetBox, `inside ${sizeName(size)}`, inside(sheetBox, size))
    await shot(page, info, size, 'light', 'capacity')
    await page.keyboard.press('Escape')
    await expect(sheet).toBeHidden()
    await expect(line).toBeFocused()
    // The dial went back to the page: still one control, so one writer.
    const dials = await page.locator('.cdial').count()
    c.is('one dial control after closing', dials, '1', dials === 1)
  } finally { await c.write(`c5-${sizeName(size)}-light`) }
})

// ---------- C6: held changes float; the toolbar keeps its line ----------
const arrive = (data: AgentData) => {
  for (let n = 0; n < 3; n++) data.sessions.push({ ...data.sessions[1]!, id: `5e000000-0000-4000-8000-${String(900 + n).padStart(12, '0')}`, display_label: `Arrival ${n + 1}`, run_id: null, ticket_node_id: null, ticket: null, created_at: new Date(NOW + (n + 1) * 1000).toISOString(), heartbeat_at: new Date(NOW + 1000).toISOString() } as typeof data.sessions[number])
}
test('AEON-1459 C6 agents 844x340 light: held changes float as a chip; the toolbar keeps its one 44 px line', async ({ page }, info) => {
  test.setTimeout(60_000)
  const { data } = await openAgents(page, LANDSCAPE, 'light')
  const c = checks(info, LANDSCAPE, 'light')
  try {
    const bar = toolbar(page)
    // A mouse resting on the list holds changes (touch never does).
    await scrollTo(page, Math.round(await toolbarOffset(page) + 120))
    const row = await box(page.locator(ROWS).nth(4))
    await page.mouse.move(row.x + 40, row.y + row.height / 2)
    const before = await box(bar)
    arrive(data)
    await page.clock.runFor(21_000)
    const chip = page.locator('[data-held-chip]')
    await expect(chip).toBeVisible()
    await expect(chip).toContainText(/\d+\s*changes? waits?/)
    await expect(chip).toContainText('Show now')
    const position = await chip.evaluate(el => getComputedStyle(el).position)
    c.is('held chip position', position, 'fixed', position === 'fixed')
    const held = await box(bar)
    c.is('toolbar height unchanged with the chip', held.height, '44', Math.abs(held.height - before.height) <= .5 && Math.abs(held.height - 44) <= .5)
    c.is('toolbar did not move for the chip', held, JSON.stringify(before), Math.abs(held.y - before.y) <= .5 && Math.abs(held.x - before.x) <= .5)
    const chipBox = await box(chip)
    c.is('chip inside the viewport, 44 px tall', chipBox, 'inside 844 × 340', inside(chipBox, LANDSCAPE) && chipBox.height >= 43.5)
    await shot(page, info, LANDSCAPE, 'light', 'held')
    // The chip is the list's: moving the mouse onto it keeps the hold, and its click applies the rows at once.
    await chip.hover()
    await page.clock.runFor(2_000)
    await expect(chip, 'pointing at the chip keeps holding').toBeVisible()
    await chip.click()
    await expect(chip).toBeHidden()
    await expect(page.locator(`.sessions [data-row="s:5e000000-0000-4000-8000-000000000900"]`)).toBeAttached()
  } finally { await c.write('c6-844x340-light') }
})

// ---------- C7: the project crumb opens the project's sheet ----------
for (const size of [PORTRAIT, LANDSCAPE]) test(`AEON-1459 C7 project ${sizeName(size)} light: the crumb opens the project's sheet`, async ({ page }, info) => {
  await agentsWorld(page, size, 'light')
  await page.goto('/p/PHAROS')
  const c = checks(info, size, 'light')
  try {
    const crumb = page.locator('.app-header .crumb-sheet')
    await expect(crumb).toBeVisible()
    const crumbBox = await box(crumb)
    c.is('crumb target ≥ 44 × 44', crumbBox, 'width and height ≥ 44', crumbBox.width >= 43.5 && crumbBox.height >= 43.5)
    const crumbText = (await crumb.innerText()).replace(/\s+/g, ' ').trim()
    c.is(size.width < 720 ? 'portrait crumb: the key' : 'landscape crumb: the key and the name', crumbText, size.width < 720 ? '"PHAROS"' : '"PHAROS Pharos"', size.width < 720 ? crumbText === 'PHAROS' : /^PHAROS ?Pharos$/.test(crumbText))
    await crumb.click()
    const sheet = page.getByRole('dialog', { name: 'PHAROS · Pharos' })
    await expect(sheet).toBeVisible()
    await expect(sheet.getByRole('heading', { name: /Pharos/ })).toBeVisible()
    const sections = (await sheet.getByRole('menuitemradio').allTextContents()).map(text => text.trim())
    c.is('sheet sections', sections, 'Tickets first, Knowledge, Settings', sections[0] === 'Tickets' && sections.includes('Knowledge') && sections.includes('Settings'))
    const checked = await sheet.getByRole('menuitemradio', { name: 'Tickets' }).getAttribute('aria-checked')
    c.is('the section on screen is checked', checked, 'true', checked === 'true')
    const sheetBox = await box(sheet)
    if (size === PORTRAIT) c.is('crumb sheet full width at the foot', sheetBox, 'x 0, width 390, bottom 664', atFoot(sheetBox, size))
    else c.is('crumb sheet inside the viewport', sheetBox, `inside ${sizeName(size)}`, inside(sheetBox, size))
    await shot(page, info, size, 'light', 'crumb')
    await page.keyboard.press('Escape')
    await expect(sheet).toBeHidden()
    await expect(crumb).toBeFocused()
    await crumb.click()
    await sheet.getByRole('menuitemradio', { name: 'Knowledge' }).click()
    await expect(page).toHaveURL(/\/p\/PHAROS\/knowledge/)
  } finally { await c.write(`c7-${sizeName(size)}-light`) }
})

// ---------- C8: the avatar menu carries the theme and the release ----------
test('AEON-1459 C8 agents 390x664 light: the avatar menu has the theme, What’s new with the release, and the foot', async ({ page }, info) => {
  await openAgents(page, PORTRAIT, 'light')
  const c = checks(info, PORTRAIT, 'light')
  try {
    const moon = await page.locator('.app-header .theme-btn').isVisible()
    c.is('no moon in the compact header', moon, 'false', !moon)
    await page.getByRole('button', { name: /^Account for / }).click()
    const menu = page.getByRole('menu', { name: 'Account' })
    await expect(menu).toBeVisible()
    await expect(menu.getByRole('menuitem', { name: /Switch to dark theme/ })).toBeVisible()
    await expect(menu.getByRole('group', { name: 'Theme' })).toBeVisible()
    const news = menu.getByRole('menuitem', { name: /^What’s new/ })
    await expect(news).toBeVisible()
    const newsText = (await news.innerText()).replace(/\s+/g, ' ').trim()
    c.is('What’s new · release · version', newsText, '"What’s new <release> · <version>"', /^What’s new .+ · .+/.test(newsText))
    const foot = (await menu.locator('.me-foot').textContent())?.trim()
    c.is('menu foot', foot, '"PAIMOS AEON"', foot === 'PAIMOS AEON')
    const menuBox = await box(menu)
    c.is('avatar menu inside the viewport', menuBox, 'inside 390 × 664', inside(menuBox, PORTRAIT))
    await shot(page, info, PORTRAIT, 'light', 'avatar')
    await news.click()
    await expect(page).toHaveURL(/releases=/)
  } finally { await c.write('c8-390x664-light') }
})

// ---------- The scroller never resizes while a finger scrolls (H2) ----------
// Chromium's own touch scroll gestures (CDP), finger up the screen, until 2000 px have passed; every
// scroll event records main's clientHeight.
type HeightWindow = { __heights: number[] }
async function touchPage(browser: Browser, size: Size) {
  const context = await browser.newContext({ viewport: size, hasTouch: true, isMobile: true })
  return { page: await context.newPage(), close: () => context.close() }
}
for (const size of [PORTRAIT, LANDSCAPE]) test(`AEON-1459 agents ${sizeName(size)} light: the scroller's height never changes across a 2000 px touch scroll`, async ({ browser }, info) => {
  test.setTimeout(60_000)
  const { page, close } = await touchPage(browser, size)
  const c = checks(info, size, 'light')
  try {
    // A list long enough for 2000 px of reading on either screen.
    await openAgents(page, size, 'light', { sessions: 70 })
    // WebKit (iPhone Safari) has no scroll anchoring.
    await page.addStyleTag({ content: '*, *::before, *::after { overflow-anchor: none !important; }' })
    const cdp: CDPSession = await page.context().newCDPSession(page)
    await main(page).evaluate(el => {
      const win = window as unknown as HeightWindow
      win.__heights = [el.clientHeight]
      el.addEventListener('scroll', () => win.__heights.push(el.clientHeight), { passive: true })
    })
    const start = await main(page).evaluate(el => el.scrollTop)
    const x = Math.round(size.width / 2), y = Math.round(size.height * .75), distance = -Math.round(size.height * .5)
    let travelled = 0
    for (let n = 0; n < 24 && travelled < 2000; n++) {
      const before = await main(page).evaluate(el => el.scrollTop)
      await cdp.send('Input.synthesizeScrollGesture', { x, y, yDistance: distance, gestureSourceType: 'touch', speed: 1200, preventFling: true })
      await expect.poll(() => main(page).evaluate(el => el.scrollTop), 'the finger moves the page').toBeGreaterThan(before)
      travelled = (await main(page).evaluate(el => el.scrollTop)) - start
    }
    const heights = await page.evaluate(() => (window as unknown as HeightWindow).__heights)
    c.is('touch scroll travelled', Math.round(travelled), '≥ 2000 px', travelled >= 2000)
    c.is('main clientHeight while scrolling', [...new Set(heights)], `one value (${heights[0]})`, new Set(heights).size === 1)
    const footers = await page.locator('footer.app-footer').count()
    c.is('no footer element', footers, '0', footers === 0)
  } finally { await c.write(`touch-${sizeName(size)}-light`); await close() }
})

// ---------- C9: desktop 1440 keeps main's geometry (±1 px), light and dark ----------
// Measured on main before this change (5aaf10ee4) with this world: boxes that no font can move.
const MAIN_1440: Record<string, Box> = {
  header: { x: 0, y: 0, width: 1440, height: 56 },
  'page head': { x: 28, y: 78, width: 1384, height: 48 },
  'capacity row': { x: 0, y: 140, width: 1440, height: 36 },
  toolbar: { x: 29, y: 191, width: 1382, height: 58 },
  'toolbar line': { x: 37, y: 197, width: 1366, height: 52 },
  'column head': { x: 29, y: 249, width: 1382, height: 32 },
  'column 1': { x: 41, y: 252, width: 738, height: 26 },
  'column 2': { x: 779, y: 252, width: 92, height: 26 },
  'column 3': { x: 871, y: 252, width: 220, height: 26 },
  'column 4': { x: 1091, y: 252, width: 132, height: 26 },
  'column 5': { x: 1223, y: 252, width: 80, height: 26 },
  'column 6': { x: 1303, y: 265, width: 96, height: 0 },
}
async function desktopGeometry(page: Page) {
  const out: Record<string, Box> = {
    header: await box(page.locator('.app-header')),
    'page head': await box(page.locator('.agents-page .page-head')),
    'capacity row': await box(page.locator('.agents-page .cap-row')),
    toolbar: await box(page.locator('.sessions .th-stick')),
    'toolbar line': await box(page.locator('.sessions .tree-head')),
    'column head': await box(page.locator('.sessions .thead')),
  }
  const columns = await page.locator('.sessions .thead > [role="columnheader"]').evaluateAll(cells => cells.map(cell => { const r = cell.getBoundingClientRect(); return { x: +r.x.toFixed(2), y: +r.y.toFixed(2), width: +r.width.toFixed(2), height: +r.height.toFixed(2) } }))
  columns.forEach((column, index) => { out[`column ${index + 1}`] = column })
  return out
}
for (const look of ['light', 'dark'] as const) test(`AEON-1459 C9 1440 ${look}: header, page head, capacity row, toolbar and columns keep main's geometry`, async ({ page }, info) => {
  const size = { width: 1440, height: 900 }
  await openAgents(page, size, look)
  const c = checks(info, size, look)
  try {
    const now = await desktopGeometry(page)
    for (const [name, expected] of Object.entries(MAIN_1440)) {
      const actual = now[name]
      const drift = actual ? Math.max(...(['x', 'y', 'width', 'height'] as const).map(key => Math.abs(actual[key] - expected[key]))) : Infinity
      c.is(`1440 ${name} vs main`, actual, `${JSON.stringify(expected)} ±1`, drift <= 1)
    }
    const footers = await page.locator('footer.app-footer').count()
    c.is('1440 keeps the footer bar', footers, '1', footers === 1)
    const compactParts = await page.locator('.cap-line, .compact-status, .sessions .tb-more, [data-held-chip]').evaluateAll(els => els.filter(el => (el as HTMLElement).checkVisibility()).length)
    c.is('1440 shows no compact parts', compactParts, '0', compactParts === 0)
    await scrollTo(page, 400)
    const barOpaque = await opaque(page, toolbar(page), info, 'toolbar-1440')
    c.is('1440 toolbar opaque over rows (pixels changed when the rows behind are hidden)', barOpaque.changed, '≈ 0 (antialiasing only)', barOpaque.same)
    await scrollTo(page, 0)
    await shot(page, info, size, look, 'desktop')
  } finally { await c.write(`c9-1440-${look}`) }
})
