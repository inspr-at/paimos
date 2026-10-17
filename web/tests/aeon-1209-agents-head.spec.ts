// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1209 (design AEON-1196 r7, package P1): the Agents head, the capacity row and the Sessions
// card head. Risk: a control moves under the pointer (a dial unit's − / +, the sticky card head),
// a plain key fires inside a text field, the accounts chip stops leading into Settings, a phone
// scrolls sideways or gets a target under 44 px.
import { test, expect, type Locator, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { capacityWorld, NOW } from './capacity-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { mockDailyPlan, type DailyPlanOptions } from './agents-daily-fixtures'
import { expectStableControls } from './helpers/stable'

async function setup(page: Page, options: DailyPlanOptions & { open?: boolean; sessions?: number } = {}) {
  await page.clock.setSystemTime(NOW)
  const work = fixtures()
  work.preferences['ui.agents.sections'] = { dial: !!options.open }
  await mockWork(page, work, { admin: true })
  const data = agentData({ me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' } })
  // A long list, so the page scrolls far enough for the card head to stick.
  const base = data.sessions[0]!
  for (let i = 0; i < (options.sessions ?? 24); i++) data.sessions.push({ ...base, id: `5e100000-0000-4000-8000-${String(i).padStart(12, '0')}`, display_label: `Long list worker w${i + 1}x`, parent_harness_session_id: null })
  const capacity = capacityWorld()
  data.accounts = (capacity.accounts as unknown as typeof data.accounts).filter(a => ['codex', 'claude', 'cursor'].includes(a.harness)).map(a => ({ ...a, registered_by_principal_id: me.id }))
  await mockAgents(page, data, { capacity })
  await page.route('**/api/me/permissions*', route => {
    const answer = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    answer.workspace.permissions.push('account.read', 'account.manage')
    return route.fulfill({ json: answer })
  })
  await mockDailyPlan(page, { example: 'limit', ...options })
  await page.route('**/api/queue', route => route.fulfill({ json: { items: [{ node_id: 'waiting-0', key: 'AEON-0', title: 'Waiting work', state: 'open', priority: 'normal', estimate_hours: 1, queued: { model_profile_id: null, waiting: true, wait_reason: '' } }], manual_order: false, capacity: {} } }))
  return { data, capacity }
}

const capRow = (page: Page) => page.locator('.cap-row')
const unit = (page: Page, key: string) => page.locator(`.cdial [data-unit="${key}"]`)
const stick = (page: Page) => page.locator('.sessions [data-stick]')
// Layout positions of every visible element in a region (scroll-independent: main's scroll is added back).
async function positions(locator: Locator) {
  return locator.evaluate(root => {
    const main = document.querySelector('main')!
    return [...root.querySelectorAll('*')].filter(el => el.getClientRects().length && getComputedStyle(el).position !== 'absolute').map(el => {
      const r = el.getBoundingClientRect()
      return [Math.round((r.x) * 2) / 2, Math.round((r.y + main.scrollTop) * 2) / 2, Math.round(r.width * 2) / 2, Math.round(r.height * 2) / 2]
    })
  })
}
const maxShift = (a: number[][], b: number[][]) => a.length !== b.length ? Infinity : Math.max(0, ...a.flatMap((r, i) => r.map((v, j) => Math.abs(v - b[i]![j]!))))
const opacity = (locator: Locator) => locator.evaluate(el => Number(getComputedStyle(el).opacity))
// Every supported harness, with a limit each: the most the capacity row has to hold.
const SEVEN = { codex: 4, claude: 2, cursor: 'no_limit', grok: 2, pi: 'off', gemini: 'no_limit', opencode: 3 }
const UNITS = ['total', ...Object.keys(SEVEN)]
type Box = { x0: number; y0: number; x1: number; y1: number }
type Reach = { clip: Box; plate: Box | null; targets: (Box & { name: string; misses: string[] })[] }
/** A unit's −, number and + as a finger or pointer can reach them: each box grown by its touch ::after,
 *  and the control that sampled points inside it actually hit (a 5 × 5 grid on touch, five inner points
 *  for round mouse targets). Also the row's clip box and, while open, the plate. */
async function reach(page: Page, key: string, touch: boolean): Promise<Reach> {
  return unit(page, key).evaluate((el, grid) => {
    const grow = (b: Element, pseudo: string) => {
      const r = b.getBoundingClientRect(), a = getComputedStyle(b, pseudo), on = a.content !== 'none' && a.position === 'absolute'
      const v = (k: string) => { const x = parseFloat(a.getPropertyValue(k)); return on && Number.isFinite(x) ? Math.min(0, x) : 0 }
      return { on, opacity: Number(a.opacity), box: { x0: r.left + v('left'), y0: r.top + v('top'), x1: r.right - v('right'), y1: r.bottom - v('bottom') } }
    }
    const misses = (b: Element, t: Box) => {
      const w = t.x1 - t.x0, h = t.y1 - t.y0
      const points = grid ? [0, 1, 2, 3, 4].flatMap(i => [0, 1, 2, 3, 4].map(j => [t.x0 + 1 + (w - 2) * i / 4, t.y0 + 1 + (h - 2) * j / 4]))
        : [[.5, .5], [.3, .5], [.7, .5], [.5, .3], [.5, .7]].map(([u, v]) => [t.x0 + w * u!, t.y0 + h * v!])
      return points.flatMap(([x, y]) => {
        const found = document.elementFromPoint(x!, y!), hit = found?.closest('button')
        return hit === b ? [] : [`${Math.round(x!)},${Math.round(y!)} reaches ${hit?.getAttribute('aria-label') ?? found?.getAttribute('class') ?? 'nothing'}`]
      })
    }
    const row = el.closest('.cap-row')!.getBoundingClientRect(), plate = grow(el, '::before')
    const targets = ([['−', '.pm.dec'], ['number', '.value'], ['+', '.pm.inc']] as const).map(([name, selector]) => {
      const b = el.querySelector(selector)!, box = grow(b, '::after').box
      return { name, ...box, misses: misses(b, box) }
    })
    return { clip: { x0: Math.max(0, row.left), y0: 0, x1: Math.min(innerWidth, row.right), y1: innerHeight }, plate: plate.on && plate.opacity === 1 ? plate.box : null, targets }
  }, touch)
}
function expectReach(r: Reach, label: string, touch: boolean) {
  for (const t of r.targets) {
    expect(t.misses, `${label} ${t.name}: every sampled point reaches it`).toEqual([])
    if (touch) expect([t.x1 - t.x0 >= 43.5, t.y1 - t.y0 >= 43.5], `${label} ${t.name}: a 44 px target`).toEqual([true, true])
    expect(t.x0 >= r.clip.x0 - .5 && t.x1 <= r.clip.x1 + .5, `${label} ${t.name}: inside the row and the screen`).toBe(true)
    if (r.plate) expect(t.x0 >= r.plate.x0 - .5 && t.x1 <= r.plate.x1 + .5 && t.y0 >= r.plate.y0 - .5 && t.y1 <= r.plate.y1 + .5, `${label} ${t.name}: on its plate`).toBe(true)
  }
  const [dec, value, inc] = r.targets as [Reach['targets'][number], Reach['targets'][number], Reach['targets'][number]]
  for (const [a, b] of [[dec, value], [value, inc], [dec, inc]] as const) {
    const ox = Math.min(a.x1, b.x1) - Math.max(a.x0, b.x0), oy = Math.min(a.y1, b.y1) - Math.max(a.y0, b.y0)
    expect(ox > .5 && oy > .5, `${label}: ${a.name} and ${b.name} must not overlap`).toBe(false)
  }
}

for (const theme of ['light', 'dark'] as const) {
  test(`${theme} 1440: head order, the capacity row and no Desk panel`, async ({ page }, info) => {
    await page.setViewportSize({ width: 1440, height: 1000 })
    await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
    const errors = watchErrors(page)
    await setup(page)
    await page.goto('/agents')
    const head = page.locator('.agents-page .page-head')
    const wind = head.getByRole('button', { name: 'Wind down' }), more = head.getByRole('button', { name: 'More agent actions', exact: true }), add = head.getByRole('button', { name: /^New: / })
    await expect(add).toBeVisible()
    // Wind down · More · New, with New last; one control height.
    const [w, m, n] = [(await wind.boundingBox())!, (await more.boundingBox())!, (await add.boundingBox())!]
    expect(w.x).toBeLessThan(m.x); expect(m.x).toBeLessThan(n.x)
    for (const box of [w, m, n]) expect(Math.round(box.height)).toBe(36)
    // The capacity row: the dial's units, then the accounts chip; the Desk lives in the app header now.
    await expect(capRow(page)).toHaveAttribute('aria-label', 'Capacity: what may run')
    await expect(unit(page, 'total')).toBeVisible()
    await expect(capRow(page).locator('.acc-chip')).toContainText('accounts')
    await expect(page.locator('.desk-panel')).toHaveCount(0)
    await more.click()
    await expect(page.getByRole('menu')).toBeVisible()
    await expect(page.getByRole('menu').getByRole('menuitem', { name: /Decision Desk/ })).toHaveCount(0)
    await page.keyboard.press('Escape')
    // Each unit has no outline and no fill; its number sits within 8 px of its mark; − / + keep their room unseen.
    for (const key of ['total', 'codex', 'claude', 'cursor']) {
      const u = unit(page, key)
      const look = await u.evaluate(el => {
        const cs = getComputedStyle(el), mark = el.querySelector('.du-mark')!.getBoundingClientRect(), n = el.querySelector('.value-slot')!.getBoundingClientRect()
        return { bare: /rgba\(0, 0, 0, 0\)|transparent/.test(cs.backgroundColor) && cs.boxShadow === 'none' && parseFloat(cs.borderTopWidth) === 0, gap: n.left - mark.right, pm: [...el.querySelectorAll('.pm')].map(b => [getComputedStyle(b).opacity, getComputedStyle(b).position]) }
      })
      expect(look.bare, `${key} has no outline and no fill`).toBe(true)
      expect(look.gap).toBeGreaterThanOrEqual(0); expect(look.gap).toBeLessThanOrEqual(8)
      expect(look.pm).toEqual([['0', 'relative'], ['0', 'relative']])
    }
    // Claude is at today's limit: the gold flag after its number, with no fill.
    await expect(unit(page, 'claude').locator('.cd-flag.st-at_limit svg')).toBeVisible()
    expect(await unit(page, 'claude').locator('.cd-flag').evaluate(el => getComputedStyle(el).backgroundColor)).toMatch(/rgba\(0, 0, 0, 0\)|transparent/)
    await page.screenshot({ path: info.outputPath(`1440-${theme}-head.png`) })
    expect(errors).toEqual([])
  })

  test(`${theme}: hovering a unit shows its − / + in place, wide and tight`, async ({ page }, info) => {
    await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
    await setup(page)
    for (const width of [1440, 900]) {
      await page.setViewportSize({ width, height: 1000 })
      await page.goto('/agents')
      await expect(unit(page, 'claude')).toBeVisible()
      await page.mouse.move(2, 990)
      const before = await positions(capRow(page)), head = await positions(page.locator('.page-head'))
      const plus = unit(page, 'claude').locator('.pm.inc')
      expect(await opacity(plus)).toBe(0)
      await unit(page, 'claude').locator('.du-mark').hover()
      await expect.poll(() => opacity(plus)).toBe(1)
      const after = await positions(capRow(page))
      expect(maxShift(before, after), `${width}: nothing in the capacity row moves`).toBeLessThanOrEqual(0.5)
      expect(maxShift(head, await positions(page.locator('.page-head')))).toBeLessThanOrEqual(0.5)
      if (width === 900) {
        // Tight: − / + float over the neighbours on a plate (a shadow, never an outline).
        expect(await plus.evaluate(el => getComputedStyle(el).position)).toBe('absolute')
        const plate = await unit(page, 'claude').evaluate(el => { const p = getComputedStyle(el, '::before'); return { opacity: p.opacity, shadow: p.boxShadow } })
        expect(plate.opacity).toBe('1'); expect(plate.shadow).not.toMatch(/inset/)
      }
      await page.screenshot({ path: info.outputPath(`${width}-${theme}-unit-hover.png`) })
      await page.mouse.move(2, 990)
    }
  })
}

// Risk: with every supported harness, a tablet-wide row clips the dial's last units, their − and +, or the live state.
for (const width of [1024, 768, 390]) test(`${width}: with all seven harnesses every unit's −, number and +, the fold and the live state stay reachable`, async ({ page }, info) => {
  await page.setViewportSize({ width, height: 1000 })
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await setup(page, { limits: SEVEN })
  await page.goto('/agents')
  await expect(page.locator('.cdial .f-chip')).toHaveCount(7)
  const fit = await page.evaluate(() => {
    const row = document.querySelector('.cap-row')!, r = row.getBoundingClientRect(), d = document.querySelector('.cdial')!.getBoundingClientRect(), cs = getComputedStyle(row)
    return { dial: d.right, row: r.right - parseFloat(cs.paddingRight), main: document.querySelector('main')!.scrollWidth - document.querySelector('main')!.clientWidth }
  })
  expect(fit.dial, 'the dial ends inside the row').toBeLessThanOrEqual(fit.row + .5)
  expect(fit.main, 'no sideways scroll').toBeLessThanOrEqual(.5)
  for (const key of UNITS) {
    await page.mouse.move(2, 990)
    await unit(page, key).locator('.du-mark').hover()
    expectReach(await reach(page, key, false), `${width} ${key}`, false)
    if (key === 'opencode') await page.screenshot({ path: info.outputPath(`${width}-seven-${key}.png`) })
  }
  await page.mouse.move(2, 990)
  for (const selector of ['.cdial .cdf', '.cdial .cd-live']) {
    const hit = await page.locator(selector).evaluate(el => {
      const r = el.getBoundingClientRect(), row = el.closest('.cap-row')!.getBoundingClientRect()
      return { inside: r.left >= row.left - .5 && r.right <= Math.min(row.right, innerWidth) + .5, reached: document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2)?.closest('button') === el }
    })
    expect(hit, `${width} ${selector}`).toEqual({ inside: true, reached: true })
  }
})

test('the accounts chip opens three tiles that lead into Settings; its tooltip stays away while open', async ({ page }, info) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await setup(page)
  await page.goto('/agents')
  const chip = capRow(page).locator('.acc-chip')
  await expect(chip).toHaveAttribute('data-tip', 'Accounts, computers and capacity')
  await chip.click()
  await expect(chip).toHaveAttribute('aria-expanded', 'true')
  await expect(chip).not.toHaveAttribute('data-tip', /./)
  const pop = page.getByRole('dialog', { name: 'Accounts and computers' })
  const tiles = pop.locator('.tile')
  await expect(tiles).toHaveCount(3)
  await expect(tiles.nth(0)).toHaveAttribute('href', '/settings/accounts')
  await expect(tiles.nth(1)).toHaveAttribute('href', '/settings/computers')
  await expect(tiles.nth(2)).toHaveAttribute('href', '/settings/capacity')
  await expect(tiles.nth(0)).toContainText('Accounts')
  await page.screenshot({ path: info.outputPath('1440-light-accounts.png') })
  await tiles.nth(1).click()
  await expect(page).toHaveURL(/\/settings\/computers$/)
})

for (const width of [1440, 390]) test(`${width}: the Sessions card head sticks inside its card with the same box and insides`, async ({ page }, info) => {
  await page.setViewportSize({ width, height: 900 })
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await setup(page)
  await page.goto('/agents')
  await expect(page.locator('.sessions .row[data-row]').nth(20)).toBeAttached()
  const sample = () => stick(page).evaluate(el => {
    const r = el.getBoundingClientRect(), top = document.querySelector('main')!.getBoundingClientRect().top
    return { top: r.top - top, w: r.width, h: r.height, kids: [...el.querySelectorAll('*')].filter(k => k.getClientRects().length).map(k => { const b = k.getBoundingClientRect(); return [b.x - r.x, b.y - r.y, b.width, b.height] }), stuck: el.classList.contains('stuck') }
  })
  const before = await sample()
  expect(before.stuck).toBe(false)
  await page.locator('main').evaluate(el => { el.scrollTop = el.scrollHeight })
  await expect.poll(async () => (await sample()).stuck).toBe(true)
  const after = await sample()
  expect(Math.abs(after.top)).toBeLessThanOrEqual(0.5)
  expect(Math.abs(after.w - before.w)).toBeLessThanOrEqual(0.5)
  expect(Math.abs(after.h - before.h)).toBeLessThanOrEqual(0.5)
  expect(maxShift(before.kids, after.kids)).toBeLessThanOrEqual(0.5)
  await page.screenshot({ path: info.outputPath(`${width}-light-sticky.png`) })
})

test('F and / search this list; typing in the search only types; Esc clears, then leaves', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await setup(page)
  await page.goto('/agents')
  const search = page.getByRole('searchbox', { name: 'Search sessions in this list' })
  await expect(page.locator('.sessions .row[data-row]').nth(20)).toBeAttached()
  await page.locator('h1').click()
  await page.keyboard.press('f')
  await expect(search).toBeFocused()
  await page.keyboard.type('worker w12x')
  await expect(search).toHaveValue('worker w12x')
  await expect(page).toHaveURL(/q=worker\+w12x/)
  // Only matching rows stay (and a match's ancestors as context); the count follows.
  await expect(page.locator('.sessions .row[data-row]').filter({ hasText: 'Long list worker w2x' })).toHaveCount(0)
  await expect(page.locator('.sessions .row[data-row]').filter({ hasText: 'Long list worker w12x' })).toHaveCount(1)
  await expect(page.locator('.sessions .fs-count')).toHaveText('1')
  await expect(page.locator('.sessions .pills-row').getByRole('button', { name: 'Clear all' })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(search).toHaveValue('')
  await expect(search).toBeFocused()
  await page.keyboard.press('Escape')
  await expect(search).not.toBeFocused()
  await page.keyboard.press('/')
  await expect(search).toBeFocused()
  await expect(page.locator('dialog.palette[open]')).toHaveCount(0)
  // Shift F opens the filter (AEON-1210: Filter by State, Project, Harness, Computer); picking a state filters like the head count does.
  await page.locator('h1').click()
  await page.keyboard.press('Shift+F')
  await page.getByRole('menu', { name: 'Filter by' }).getByRole('menuitem', { name: 'State' }).click()
  const menu = page.getByRole('menu', { name: 'Filter sessions by state' })
  await expect(menu).toBeVisible()
  await menu.getByRole('menuitemradio', { name: /Working/ }).click()
  await expect(page.locator('.page-head .count[data-filter="working"]')).toHaveAttribute('aria-pressed', 'true')
  await expect(page.locator('.sessions .pills-row .chip-main')).toHaveAccessibleName('State: Working. Change the filter')
  await expect(page.locator('.sessions .pills-row .chip-value')).toHaveText('Working')
})

// Risk: a search hides the session under the cursor, and Enter, p or r still act on it unseen.
test('a search that hides the session under the cursor moves the cursor to a match; Enter and p never act on a hidden session', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.emulateMedia({ reducedMotion: 'reduce' })
  const { data } = await setup(page)
  // The long list's workers can all be paused, so p would open a sheet for any session it reached.
  for (const s of data.sessions.slice(9)) Object.assign(s, { role: 'worker', owner_principal_id: me.id, advertised_capabilities: ['pause', 'inbox'], supported_pause_levels: ['stop_now', 'pause_quickly', 'pause', 'wrap_up'] })
  const id = (n: number) => `5e100000-0000-4000-8000-${String(n - 1).padStart(12, '0')}`
  const row = (n: number) => page.locator(`.sessions [data-row="s:${id(n)}"]`)
  const search = page.getByRole('searchbox', { name: 'Search sessions in this list' })
  const pauseSheet = page.getByRole('dialog', { name: /^Pause / })
  await page.goto('/agents')
  await row(3).focus()
  await expect(row(3)).toHaveClass(/\bactive\b/)
  await page.keyboard.press('f')
  await search.fill('worker w12x')
  await expect(row(3)).toHaveCount(0)
  await page.locator('h1').click()
  // The cursor left the hidden row for the one match.
  await expect(page.locator('.sessions .row.active')).toHaveAttribute('data-row', `s:${id(12)}`)
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(new RegExp(`/agents/${id(12)}\\?`))
  await page.keyboard.press('Escape')
  await expect(page).toHaveURL(/\/agents\?q=/)
  // Nothing matches: no row is a target, so Enter and p do nothing.
  await page.keyboard.press('f')
  await search.fill('nothing matches zzz')
  await expect(page.locator('.sessions .row[data-row]')).toHaveCount(0)
  await page.locator('h1').click()
  await page.keyboard.press('Enter')
  await page.keyboard.press('p')
  await expect(page).toHaveURL(/\/agents\?q=nothing/)
  await expect(pauseSheet).toHaveCount(0)
  // The same p on a shown row does open its sheet, so the silence above is the guard, not a dead key.
  await search.fill('worker w12x')
  await expect(row(12)).toHaveCount(1)
  await page.locator('h1').click()
  await page.keyboard.press('j')
  await expect(page.locator('.sessions .row.active')).toHaveAttribute('data-row', `s:${id(12)}`)
  await page.keyboard.press('p')
  await expect(page.getByRole('dialog', { name: 'Pause Long list worker w12x' })).toBeVisible()
})

for (const width of [1440, 1024, 390]) test(`${width}: the head, the capacity row and the card head stay put through search, filters, Display and the accounts popover`, async ({ page }) => {
  await page.setViewportSize({ width, height: 1000 })
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await setup(page)
  await page.goto('/agents')
  const head = page.locator('.agents-page .page-head'), card = stick(page)
  const search = card.getByRole('searchbox', { name: 'Search sessions in this list' })
  const filter = card.getByRole('button', { name: 'Filter', exact: true }), display = card.getByRole('button', { name: /^Display: / })
  await expect(page.locator('.sessions .row[data-row]').nth(5)).toBeAttached()
  await expectStableControls({
    controls: {
      wind: head.getByRole('button', { name: 'Wind down' }), more: head.getByRole('button', { name: 'More agent actions', exact: true }), add: head.getByRole('button', { name: /^New: / }),
      total: unit(page, 'total'), claude: unit(page, 'claude'), chip: capRow(page).locator('.acc-chip'),
      title: card.locator('#sessions-title'), search, filter, display, history: card.getByRole('button', { name: /^Show history/ }),
    },
    scrollAreas: { main: page.locator('main') },
    interactions: [
      { name: 'type a search', run: async () => { await search.fill('worker w1x'); await expect(page.locator('.sessions .fs-count')).toHaveText('1') } },
      { name: 'clear it', run: async () => { await search.fill(''); await expect(page.locator('.sessions .pills-row')).toHaveCount(0) } },
      { name: 'pick a state', run: async () => { await filter.click(); await page.getByRole('menu', { name: 'Filter by' }).getByRole('menuitem', { name: 'State' }).click(); await page.getByRole('menuitemradio', { name: /Working/ }).click(); await expect(page.locator('.sessions .pills-row')).toBeVisible() } },
      { name: 'clear all', run: async () => { await page.locator('.sessions .pills-row').getByRole('button', { name: 'Clear all' }).click(); await expect(page.locator('.sessions .pills-row')).toHaveCount(0) } },
      { name: 'open Display', run: async () => { await display.click(); await expect(page.getByRole('dialog', { name: 'Display' })).toBeVisible() } },
      { name: 'close Display', run: async () => { await page.keyboard.press('Escape'); await expect(page.getByRole('dialog', { name: 'Display' })).toHaveCount(0) } },
      { name: 'open the accounts popover', run: async () => { await capRow(page).locator('.acc-chip').click(); await expect(page.getByRole('dialog', { name: 'Accounts and computers' })).toBeVisible() } },
      { name: 'close it', run: async () => { await page.keyboard.press('Escape'); await expect(page.getByRole('dialog', { name: 'Accounts and computers' })).toHaveCount(0) } },
    ],
  })
})

// The design's own gate (AEON-1196 check.mjs), on the real page for this package's parts: no coloured edge accents,
// SVG icons only, English only, no sideways scroll; screenshots of each state for the evidence.
const PROBES = () => {
  const parts = () => [...document.querySelectorAll('.agents-page .page-head, .cap-row, .dial-card, .sessions [data-stick], .floating')]
  const all = () => parts().flatMap(root => [root, ...root.querySelectorAll('*')])
  const edge: string[] = []
  for (const el of all()) {
    const cs = getComputedStyle(el)
    const bw = (side: string) => parseFloat(cs.getPropertyValue(`border-${side}-width`)) || 0
    const opaque = (side: string) => { const c = cs.getPropertyValue(`border-${side}-color`); return !!c && !/rgba\(.*,\s*0\)$/.test(c) && c !== 'transparent' }
    for (const [a, b] of [['left', 'right'], ['top', 'bottom']]) for (const [x, y] of [[a, b], [b, a]]) if (bw(x!) >= 2 && bw(x!) - bw(y!) >= 1 && opaque(x!)) edge.push(`${el.className} border-${x}`)
    for (const part of cs.boxShadow === 'none' ? [] : cs.boxShadow.split(/,(?![^(]*\))/)) {
      if (!/inset/.test(part)) continue
      const [x = 0, y = 0, blur = 0] = (part.replace(/rgba?\([^)]*\)|inset/g, '').match(/-?[\d.]+px/g) || []).map(parseFloat)
      if ((Math.abs(x) >= 2 || Math.abs(y) >= 2) && blur === 0) edge.push(`${el.className} inset ${part.trim()}`)
    }
    for (const pseudo of ['::before', '::after']) {
      const ps = getComputedStyle(el, pseudo)
      if (ps.content === 'none' || ps.position !== 'absolute') continue
      const w = parseFloat(ps.width) || 0, h = parseFloat(ps.height) || 0, bg = ps.backgroundColor
      if (w >= 2 && w <= 6 && h >= 16 && bg && !/rgba\(.*,\s*0\)$/.test(bg) && bg !== 'transparent') edge.push(`${el.className} ${pseudo} bar`)
    }
  }
  const glyphs: string[] = [], re = /[←→↑↓↵⏎✓✔✕✖×▾▸▴▲▼◂▶◀★☆●○◉⚠✗✘➜➔⟶]/
  const walk = (n: Node) => { for (const c of n.childNodes) { if (c.nodeType === 3 && re.test(c.textContent ?? '')) glyphs.push((c.textContent ?? '').trim().slice(0, 40)); else if (c.nodeType === 1 && !['SCRIPT', 'STYLE'].includes((c as Element).tagName)) walk(c) } }
  parts().forEach(walk)
  const text = parts().map(p => (p as HTMLElement).innerText).join(' ')
  const german = text.match(/[äöüÄÖÜß]|\b(und|der|die|das|nicht|mit|für|oder|ist|Sitzung|Agenten|Projekt|Fehler|Einstellungen|bitte)\b/g) ?? []
  const main = document.querySelector('main')!
  return { edge, glyphs, german, hscroll: Math.max(document.documentElement.scrollWidth - innerWidth, main.scrollWidth - main.clientWidth) }
}
for (const width of [1440, 1024, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`design gate at ${width} ${theme}: no edge accents, SVG icons only, English only, no sideways scroll, through every state`, async ({ page }, info) => {
    test.setTimeout(90_000)
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
    await setup(page)
    await page.goto('/agents')
    await expect(page.locator('.sessions .row[data-row]').nth(5)).toBeAttached()
    const gate = async (state: string) => {
      const r = await page.evaluate(PROBES)
      expect(r.edge, `${state}: edge accents`).toEqual([])
      expect(r.glyphs, `${state}: text glyphs`).toEqual([])
      expect(r.german, `${state}: German words`).toEqual([])
      expect(r.hscroll, `${state}: sideways scroll`).toBeLessThanOrEqual(0.5)
      await page.screenshot({ path: info.outputPath(`${width}-${theme}-${state}.png`) })
    }
    await gate('top')
    await page.locator('.cdial .fs-tog').click(); await expect(page.locator('.dial-card')).toBeVisible()
    await gate('dial-open')
    await page.locator('.cdial .fs-tog').click(); await expect(page.locator('.dial-card')).toHaveCount(0)
    await unit(page, 'claude').locator('.du-mark').hover(); await gate('unit-hover'); await page.mouse.move(2, 990)
    await capRow(page).locator('.acc-chip').click(); await expect(page.getByRole('dialog', { name: 'Accounts and computers' })).toBeVisible()
    await gate('accounts'); await page.keyboard.press('Escape')
    await stick(page).getByRole('button', { name: 'Filter', exact: true }).click(); await page.getByRole('menu', { name: 'Filter by' }).getByRole('menuitem', { name: 'State' }).click(); await expect(page.getByRole('menu', { name: 'Filter sessions by state' })).toBeVisible()
    await gate('filter'); await page.getByRole('menuitemradio', { name: /Working/ }).click()
    await expect(page.locator('.sessions .pills-row')).toBeVisible(); await gate('pill')
    await stick(page).getByRole('button', { name: /^Display: / }).click(); await expect(page.getByRole('dialog', { name: 'Display' })).toBeVisible()
    await gate('display'); await page.keyboard.press('Escape')
    await page.locator('main').evaluate(el => { el.scrollTop = el.scrollHeight }); await expect(stick(page)).toHaveClass(/stuck/)
    await gate('sticky')
  })
}

test.describe('phone', () => {
  test.use({ viewport: { width: 390, height: 900 }, hasTouch: true, isMobile: true })
  for (const theme of ['light', 'dark'] as const) test(`${theme} 390: no sideways scroll, 44 px targets in the head, the capacity row and the card head`, async ({ page }, info) => {
    await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
    await setup(page)
    await page.goto('/agents')
    await expect(unit(page, 'claude')).toBeVisible()
    const wide = await page.evaluate(() => { const m = document.querySelector('main')!; return { doc: document.documentElement.scrollWidth - innerWidth, main: m.scrollWidth - m.clientWidth } })
    expect(wide.doc).toBeLessThanOrEqual(0.5); expect(wide.main).toBeLessThanOrEqual(0.5)
    const small = await page.evaluate(() => {
      const bad: string[] = []
      for (const el of document.querySelectorAll<HTMLElement>('.page-head :is(button, a[href], input), .cap-row :is(button, a[href]), .sessions [data-stick] :is(button, a[href], input)')) {
        const r = el.getBoundingClientRect(), cs = getComputedStyle(el)
        if (!r.width || !r.height || cs.visibility === 'hidden' || Number(cs.opacity) === 0) continue
        const a = getComputedStyle(el, '::after'); let w = r.width, h = r.height
        if (a.content !== 'none' && a.position === 'absolute') {
          const v = (k: string) => { const x = parseFloat(a.getPropertyValue(k)); return Number.isFinite(x) ? x : 0 }
          w = Math.max(w, r.width - Math.min(0, v('left')) - Math.min(0, v('right'))); h = Math.max(h, r.height - Math.min(0, v('top')) - Math.min(0, v('bottom')))
        }
        if (w < 43.5 || h < 43.5) bad.push(`${el.className} "${(el.getAttribute('aria-label') ?? el.textContent ?? '').trim().slice(0, 30)}" ${Math.round(w)}x${Math.round(h)}`)
      }
      return bad
    })
    expect(small).toEqual([])
    // A tap on a unit's mark opens it on its plate with − and +.
    await unit(page, 'codex').locator('.du-mark').tap()
    await expect(unit(page, 'codex')).toHaveClass(/open/)
    await expect.poll(() => opacity(unit(page, 'codex').locator('.pm.inc'))).toBe(1)
    await page.screenshot({ path: info.outputPath(`390-${theme}-head.png`), fullPage: false })
  })
})

// Risk: on touch, the targets of a unit's −, number and + overlap, so a tap on the number steps the
// limit instead of opening the editor, or a target hangs outside the plate or the row, out of reach.
test.describe('touch targets', () => {
  test.use({ hasTouch: true, isMobile: true })
  for (const [width, seven] of [[390, false], [390, true], [768, true]] as const) test(`${width}${seven ? ' with seven harnesses' : ''}: an open plate holds −, the number and + as three distinct 44 px targets`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: 900 })
    await page.emulateMedia({ reducedMotion: 'reduce' })
    await setup(page, seven ? { limits: SEVEN } : {})
    await page.goto('/agents')
    const keys = seven ? UNITS : ['total', 'codex', 'claude', 'cursor']
    await expect(unit(page, keys.at(-1)!)).toBeVisible()
    // Closed, each unit is one target (a tap opens its plate) and the fold keeps its own 44 px reach beside the total.
    const closed = await page.evaluate(() => {
      const misses: string[] = []
      const sample = (b: { left: number; top: number; right: number; bottom: number }, ok: (el: Element | null) => boolean, label: string) => {
        for (let i = 0; i < 5; i++) for (let j = 0; j < 5; j++) {
          const x = b.left + 1 + (b.right - b.left - 2) * i / 4, y = b.top + 1 + (b.bottom - b.top - 2) * j / 4, el = document.elementFromPoint(x, y)
          if (!ok(el)) misses.push(`${label} at ${Math.round(x)},${Math.round(y)} reaches ${el?.closest('button')?.getAttribute('aria-label') ?? el?.getAttribute('class') ?? 'nothing'}`)
        }
      }
      const fold = document.querySelector('.cdial .cdf')!, r = fold.getBoundingClientRect(), a = getComputedStyle(fold, '::after')
      const v = (k: string) => Math.min(0, parseFloat(a.getPropertyValue(k)) || 0)
      if (r.right - v('right') - (r.left + v('left')) < 43.5) misses.push('fold is under 44 px')
      sample({ left: r.left + v('left'), top: r.top + v('top'), right: r.right - v('right'), bottom: r.bottom - v('bottom') }, el => el?.closest('button') === fold, 'fold')
      for (const u of document.querySelectorAll<HTMLElement>('.cdial [data-unit]')) sample(u.getBoundingClientRect(), el => el?.closest('[data-unit]') === u && !el.closest('button'), u.dataset.unit!)
      return misses
    })
    expect(closed).toEqual([])
    for (const key of keys) {
      await unit(page, key).locator('.du-mark').tap()
      await expect(unit(page, key)).toHaveClass(/\bopen\b/)
      expectReach(await reach(page, key, true), `${width} ${key}`, true)
      if (key === 'total' || key === 'grok') await page.screenshot({ path: info.outputPath(`${width}-${seven ? 'seven' : 'three'}-${key}-plate.png`) })
      await page.locator('h1').tap()
      await expect(unit(page, key)).not.toHaveClass(/\bopen\b/)
    }
    // A tap on the open plate's number types; a tap on its + steps once.
    const total = unit(page, 'total')
    await total.locator('.du-mark').tap()
    const number = await total.locator('.value').boundingBox()
    await page.touchscreen.tap(number!.x + number!.width / 2, number!.y + number!.height / 2)
    await expect(total.locator('input')).toBeFocused()
    await total.locator('input').press('Escape')
    await expect(total.locator('.f-num')).toHaveText('12')
    await total.locator('.pm.inc').tap()
    await expect(total.locator('.f-num')).toHaveText('13')
  })

  test('1280 with seven harnesses: the wide row shows −, the number and + as three distinct 44 px targets in every unit', async ({ page }, info) => {
    await page.setViewportSize({ width: 1280, height: 900 })
    await page.emulateMedia({ reducedMotion: 'reduce' })
    await setup(page, { limits: SEVEN })
    await page.goto('/agents')
    await expect(page.locator('.cdial .f-chip')).toHaveCount(7)
    for (const key of UNITS) expectReach(await reach(page, key, true), `1280 ${key}`, true)
    expect(await page.locator('main').evaluate(el => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(.5)
    await page.screenshot({ path: info.outputPath('1280-touch-seven.png') })
  })
})

// AEON-1209 G7 · risk: independent read failures are shown as empty or green;
// last-good data lose their stale label, or a recovery moves the controls.
// AEON-1336: each viewport/theme keeps the complete state sequence in its own
// test. Eight screenshot matrices in one test exhausted its 180-second CI limit
// and then the shard's 20-minute limit; every assertion and limit stays intact.
const readStatesTitle = 'AEON-1209 G7: the chip and each tile tell loading, unreadable, stale, forbidden and authorized empty apart'
for (const width of [1440, 1024, 400, 390]) for (const theme of ['light', 'dark'] as const) {
test(width === 1440 && theme === 'light' ? readStatesTitle : `${readStatesTitle} (${width} ${theme})`, async ({ page }, info) => {
  test.setTimeout(180_000)
  await page.setViewportSize({ width, height: 1000 })
  await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
  const { data, capacity } = await setup(page)
  for (const computer of capacity.computers as { computer_name: string }[]) computer.computer_name = 'Entwicklungscomputer mit einem sehr langen Namen'
  const paths = { accounts: '/api/agent-accounts', computers: '/api/agent-pairing/computers', capacity: '/api/agent-accounts/capacity', schedules: '/api/agent-accounts/capacity/schedule' }
  const failures: Partial<Record<keyof typeof paths, number>> = {}
  let empty = false, release!: () => void, releaseTiles!: () => void
  const initialRead = new Promise<void>(resolve => { release = resolve })
  const tileReads = new Promise<void>(resolve => { releaseTiles = resolve })
  let waiting = true, tilesWaiting = true
  for (const [source, path] of Object.entries(paths)) await page.route(`**${path}`, async route => {
    if (source === 'accounts' && waiting) await initialRead
    if ((source === 'computers' || source === 'schedules') && tilesWaiting) await tileReads
    const status = failures[source as keyof typeof paths]
    if (status) return route.fulfill({ status, json: { error: `${source} unavailable`, message: `${source} unavailable` } })
    if (source === 'accounts' && empty) return route.fulfill({ json: [] })
    return route.fallback()
  })
  failures.accounts = 500
  await page.goto('/agents')
  const chip = capRow(page).locator('.acc-chip')
  const pop = page.getByRole('dialog', { name: 'Accounts and computers' })
  const tile = (part: string) => pop.locator(`.tile-shell[data-part="${part}"]`)
  let loadingBox: { width: number; height: number }
  const evidence = async (state: string, popover = true) => {
    if (popover) await expect(pop).toBeVisible()
    if (state === 'loading') loadingBox = (await capRow(page).locator('.acc-sk').boundingBox())!
    if (state === 'ready') {
      const box = (await chip.boundingBox())!
      for (const axis of ['width', 'height'] as const) expect(Math.abs(box[axis] - loadingBox[axis]), `${width}: chip ${axis} matches its skeleton`).toBeLessThanOrEqual(.5)
      await page.locator('.agents-page .page-head').screenshot({ path: info.outputPath(`head-${width}-${theme}.png`), animations: 'disabled' })
      await capRow(page).screenshot({ path: info.outputPath(`capacity-row-${width}-${theme}.png`), animations: 'disabled' })
    }
    expect(await page.locator('main').evaluate(el => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(0.5)
    await page.screenshot({ path: info.outputPath(`${state}-${width}-${theme}.png`), animations: 'disabled' })
  }
  await expect(capRow(page).locator('.acc-sk')).toBeVisible()
  await expect(unit(page, 'total')).toBeVisible()
  await expect(capRow(page)).not.toContainText('No accounts yet')
  await evidence('loading', false)
  waiting = false; release()
  await expect(chip).toHaveText('Accounts unreadable')
  await expect(chip).toHaveAttribute('data-tip', /accounts unavailable/)
  await chip.click()
  await expect(tile('accounts')).toContainText("Couldn't read accounts")
  await expect(tile('accounts')).not.toContainText(/\d+.*ready|No accounts yet/)
  for (const part of ['computers', 'capacity']) {
    await expect(tile(part)).toHaveAttribute('data-state', 'loading')
    await expect(tile(part).locator('.tile-sk')).toBeVisible()
  }
  await evidence('loading-tiles')
  await expectStableControls({
    controls: { chip, accounts: tile('accounts').locator('.tile'), computers: tile('computers').locator('.tile'), capacity: tile('capacity').locator('.tile') },
    interactions: [{ name: 'independent computer and schedule reads land', run: async () => {
      tilesWaiting = false; releaseTiles()
      await expect(tile('computers')).toHaveAttribute('data-state', 'ready')
      await expect(tile('capacity')).toHaveAttribute('data-state', 'ready')
    } }],
  })
  await expect(tile('computers')).toContainText(/\d+ of \d+ online/)
  await expect(tile('capacity')).toHaveAttribute('data-state', 'ready')
  await evidence('unreadable')
  await expectStableControls({
    controls: { chip, accounts: tile('accounts').locator('.tile'), computers: tile('computers').locator('.tile'), capacity: tile('capacity').locator('.tile') },
    interactions: [{ name: 'retry the accounts read', run: async () => {
      delete failures.accounts
      await tile('accounts').getByRole('button', { name: 'Try again' }).click()
      await expect(tile('accounts')).toHaveAttribute('data-state', 'ready')
    } }],
  })
  await evidence('ready')
  const count = await chip.locator('.sc-t').innerText()
  for (const [source, part] of [['accounts', 'accounts'], ['capacity', 'accounts'], ['computers', 'computers'], ['schedules', 'capacity']] as const) {
    failures[source] = 500
    await page.evaluate(() => window.dispatchEvent(new Event('online')))
    await expect(tile(part)).toHaveAttribute('data-state', 'stale')
    await expect(chip.locator('.sc-t')).toHaveText(count)
    await expect(tile(part)).toContainText(/as of \d{2}:\d{2}/)
    await expect(pop.locator('.acc-state .dot.ok')).toHaveCount(0)
    if (source === 'accounts') { await expect(tile('computers')).toHaveAttribute('data-state', 'ready'); await expect(tile('capacity')).toHaveAttribute('data-state', 'ready') }
    if (source === 'computers') await evidence('stale')
    delete failures[source]
    await pop.getByRole('button', { name: 'Refresh', exact: true }).click()
    await expect(chip).toHaveAttribute('data-state', 'ready')
  }
  // Forbidden after a successful read suppresses the values immediately.
  failures.computers = 403
  await page.evaluate(() => window.dispatchEvent(new Event('online')))
  await expect(tile('computers')).toContainText('Owners and admins see computers here')
  await expect(tile('computers')).not.toContainText(/online|as of/)
  await expect(chip.locator('.sc-t')).toHaveText('Accounts')
  await expect(chip.locator('.flag')).toHaveCount(0)
  await evidence('forbidden')
  delete failures.computers
  await page.evaluate(() => window.dispatchEvent(new Event('online')))
  await expect(chip).toHaveAttribute('data-state', 'ready')
  empty = true
  await page.evaluate(() => window.dispatchEvent(new Event('online')))
  await expect(tile('accounts')).toContainText('No accounts yet')
  await expect(tile('accounts').getByRole('link', { name: 'Connect', exact: true })).toBeVisible()
  await evidence('empty')
  empty = false
  await page.evaluate(() => window.dispatchEvent(new Event('online')))
  await expect(tile('accounts')).toHaveAttribute('data-state', 'ready')
  expect(data.accounts.length).toBeGreaterThan(0)
  // Every source also fails on an initial read, with no snapshot to keep.
  for (const [source, part] of [['capacity', 'accounts'], ['computers', 'computers'], ['schedules', 'capacity']] as const) {
    failures[source] = 500
    await page.goto('/agents')
    await expect(chip).toHaveText('Accounts unreadable')
    await chip.click()
    await expect(tile(part)).toContainText(`Couldn't read ${part}`)
    await expect(tile(part)).not.toContainText(/No accounts yet|\d+.*ready|\d+ of \d+ online/)
    delete failures[source]
  }
})
}
