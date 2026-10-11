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
  return { data }
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
  // Shift F opens the filter; picking a state filters like the head count does.
  await page.locator('h1').click()
  await page.keyboard.press('Shift+F')
  const menu = page.getByRole('menu', { name: 'Filter sessions by state' })
  await expect(menu).toBeVisible()
  await menu.getByRole('menuitemradio', { name: /Working/ }).click()
  await expect(page.locator('.page-head .count[data-filter="working"]')).toHaveAttribute('aria-pressed', 'true')
  await expect(page.locator('.sessions .pills-row .chip-main')).toHaveAccessibleName('State: Working. Change the filter')
  await expect(page.locator('.sessions .pills-row .chip-value')).toHaveText('Working')
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
      { name: 'pick a state', run: async () => { await filter.click(); await page.getByRole('menuitemradio', { name: /Working/ }).click(); await expect(page.locator('.sessions .pills-row')).toBeVisible() } },
      { name: 'clear all', run: async () => { await page.locator('.sessions .pills-row').getByRole('button', { name: 'Clear all' }).click(); await expect(page.locator('.sessions .pills-row')).toHaveCount(0) } },
      { name: 'open Display', run: async () => { await display.click(); await expect(page.getByRole('dialog', { name: 'Display' })).toBeVisible() } },
      { name: 'close Display', run: async () => { await page.keyboard.press('Escape'); await expect(page.getByRole('dialog', { name: 'Display' })).toHaveCount(0) } },
      { name: 'open the accounts popover', run: async () => { await capRow(page).locator('.acc-chip').click(); await expect(page.getByRole('dialog', { name: 'Accounts and computers' })).toBeVisible() } },
      { name: 'close it', run: async () => { await page.keyboard.press('Escape'); await expect(page.getByRole('dialog', { name: 'Accounts and computers' })).toHaveCount(0) } },
    ],
  })
})

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
