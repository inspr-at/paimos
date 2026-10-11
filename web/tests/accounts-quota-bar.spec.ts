// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1172 (design AEON-1149): Settings › Accounts shows one picture of every limit.
// Risks: a row or the panel claims room agents do not have; a limit save moves the
// controls; a touch target in the new rows is under 44 px; the legend or bars push
// the page sideways on a phone. The plan's daily_state drives the bars; accounts
// outside it show only their reading. A plan that could not refresh says so and stops
// promising room when the server stops trusting its readings; Remove and End now leave
// every control below in place, also while the reads after them are still out.
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { ACCOUNTS, NOW, TZ, capacityWorld, type CapacityOptions } from './capacity-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { controlStability } from './control-stability'
import type { AccountUsagePolicy } from '../src/lib/accountUsage'

test.use({ timezoneId: TZ })
// Read as the page opens: the server trusts a reading for two minutes from read_at.
const door = (id: string, used: number, start: number, limit: number, over: Record<string, unknown> = {}) => ({
  account_id: id, label: id, order: 1, used_pct: used, left_pct: 100 - used, start_of_day_used_pct: start, limit_used_pct: limit, floor_pct: 10,
  resets_at: '2026-10-02T07:14:00Z', read_at: new Date(NOW).toISOString(), freshness: 'fresh', resets: null, routable: true, can_edit: true, ...over,
})
// Spare is low and at its floor, Main has 4 points left today, Studio's reading is old; Claude is on Boost today.
const PLAN = {
  total: 15, limits: {}, principal_id: me.id, running: {}, running_total: 0, source: 'plan', updated_at: null, daily_default_points: 10,
  daily: { codex: { pace: { mode: 'pace', points_per_day: null }, boost_today: null, at_limit: 'ladder' }, claude: { pace: { mode: 'pace', points_per_day: 6 }, boost_today: { limit_used_pct: 68, entered_as: 'used', until: '2026-09-29T22:00:00Z' }, at_limit: 'wait' } },
  daily_state: {
    codex: { state: 'on_pace', limit_used_pct: 62, today_points_used: 3, today_points_allowed: 7, over_pace_points: 0, active_account_id: ACCOUNTS.main, next_on_ladder: null, accounts: [door(ACCOUNTS.spare, 91, 89, 90), door(ACCOUNTS.main, 58, 55, 62), door(ACCOUNTS.studio, 22, 22, 32, { freshness: 'stale' })] },
    claude: { state: 'on_pace', limit_used_pct: 68, today_points_used: 4, today_points_allowed: 11, over_pace_points: 0, active_account_id: ACCOUNTS.claude, next_on_ladder: null, accounts: [door(ACCOUNTS.claude, 63, 59, 68)] },
  },
}
async function setup(page: Page, options: CapacityOptions = {}) {
  await page.clock.setSystemTime(NOW)
  await mockWork(page, fixtures(), { admin: true })
  const data = agentData({ me: me.id, now: NOW, projects: {}, tickets: {}, nodes: {} })
  const capacity = capacityWorld(options)
  data.accounts = capacity.accounts as unknown as typeof data.accounts
  await mockAgents(page, data, { capacity })
  await page.route('**/api/agents/plan', route => route.request().method() === 'GET' ? route.fulfill({ json: PLAN }) : route.fallback())
  await page.route('**/api/settings/quota-warnings', route => route.request().method() === 'GET' ? route.fulfill({ json: { early_percent: 10, urgent_percent: 3 } }) : route.fallback())
  await page.route('**/api/me/permissions*', route => {
    const answer = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    answer.workspace.permissions.push('account.read', 'account.manage', 'model_prefs.manage')
    return route.fulfill({ json: answer })
  })
  let policy: AccountUsagePolicy = { account_id: ACCOUNTS.main, posture: 'balanced', source: 'person', floor_percent: 10, own_floor_percent: 10, revision: 1, binding_revision: 2, can_set_posture: true, can_set_floor: true }
  const floors: unknown[] = []
  await page.route('**/api/agent-accounts/overview*', route => route.fulfill({ json: { accounts: [{ account_id: policy.account_id, harness: 'codex', usage_policy: policy }], has_more: false } }))
  await page.route(/\/api\/agent-accounts\/[^/]+\/floor$/, route => {
    const body = route.request().postDataJSON(); floors.push(body)
    policy = { ...policy, floor_percent: body.floor_percent, own_floor_percent: body.floor_percent, revision: policy.revision + 1 }
    return route.fulfill({ json: policy })
  })
  return { capacity, floors }
}
const listRow = (page: Page, id: string) => page.locator(`.list-row[data-accounts~="${id}"]`)
const pane = (page: Page) => page.locator('section.pane')
const overflow = (page: Page) => page.evaluate(() => document.documentElement.scrollWidth - innerWidth)

for (const [width, touch] of [[1440, false], [1024, false], [390, true]] as const) for (const theme of ['light', 'dark'] as const) {
  test.describe(`${width} ${theme}`, () => {
    test.use({ viewport: { width, height: width > 600 ? 1000 : 860 }, hasTouch: touch, isMobile: touch, colorScheme: theme })
    test(`accounts ${width} ${theme}: each row carries the bar and one caption; the panel says what stops agents first`, async ({ page }, info) => {
      const errors = watchErrors(page)
      await setup(page, { limits: true })
      await page.goto('/settings/accounts')
      await expect(listRow(page, ACCOUNTS.main)).toBeVisible()
      const caption = (id: string) => listRow(page, id).locator('[data-quota-caption]')
      await expect(caption(ACCOUNTS.main)).toHaveText('42 % left this week · resets Fri 09:14')
      await expect(caption(ACCOUNTS.spare)).toHaveText('Low · 9 % left · agents stopped at the floor')
      await expect(caption(ACCOUNTS.studio)).toHaveText('78 % left · reading out of date')
      // Outside the plan and without a vendor reading: never a guess.
      await expect(caption(ACCOUNTS.cursor)).toHaveText('Quota not reported')
      await expect(listRow(page, ACCOUNTS.cursor).locator('[data-quota-bar]')).toHaveAttribute('aria-label', /quota not reported/)
      await expect(listRow(page, ACCOUNTS.main).locator('[data-quota-bar]')).toHaveAttribute('aria-label', /58 % used this week; today’s limit 62 %;.*floor 10 %\./)
      await expect(page.locator('.acc-lh [data-quota-legend]')).toBeVisible({ visible: width > 600 })
      await expect(page.locator('#quota-warnings')).toContainText('Warn me when an account is low: at 10 % left, urgently at 3 %. Warnings never stop agents.')
      expect(await overflow(page)).toBeLessThanOrEqual(1)
      await page.screenshot({ path: info.outputPath(`accounts-list-${width}-${theme}.png`), fullPage: true })

      await listRow(page, ACCOUNTS.main).click()
      await expect(pane(page).locator('[data-quota-say]')).toHaveText('Agents may use 4 more percentage points today. They stop at today’s limit (62 %) first.')
      await expect(pane(page).locator('[data-limits] .p-head h3')).toHaveText('Limits on this account')
      await expect(pane(page).locator('[data-limit="daily"]')).toContainText('+10 percentage points a day, the default. The same for every Codex account; set in Capacity.')
      await expect(pane(page).locator('[data-rule]')).toContainText('Set by you · at most 20 % a day')
      await expect(pane(page).getByRole('link', { name: 'Agents, Models' })).toHaveCount(0)
      expect(await overflow(page)).toBeLessThanOrEqual(1)
      await page.screenshot({ path: info.outputPath(`accounts-panel-${width}-${theme}.png`), fullPage: false })
      if (touch) {
        const targets = { floor: pane(page).getByRole('spinbutton'), capacity: pane(page).getByRole('link', { name: 'Daily limit, in Capacity' }), remove: pane(page).locator('[data-rule]').getByRole('button', { name: 'Remove' }), use: pane(page).locator(`[data-use="${ACCOUNTS.main}"]`).getByRole('switch') }
        for (const [name, target] of Object.entries(targets)) {
          await target.scrollIntoViewIfNeeded()
          const box = (await target.boundingBox())!
          expect(Math.min(box.width, box.height), name).toBeGreaterThanOrEqual(44)
        }
      }
      // Boost today sets Claude's limit; the panel names it.
      await pane(page).getByRole('button', { name: 'Close details' }).click()
      await listRow(page, ACCOUNTS.claude).click()
      await expect(pane(page).locator('[data-quota-say]')).toContainText('today’s Boost (68 %)')
      await expect(pane(page).locator('[data-limit="daily"]')).toContainText('+6 percentage points a day. Boost today: up to 68 % used, until midnight.')
      if (touch) {
        await pane(page).getByRole('button', { name: 'Close details' }).click()
        const change = page.locator('#quota-warnings').getByRole('button', { name: 'Change the low-quota warnings' })
        await change.scrollIntoViewIfNeeded()
        const box = (await change.boundingBox())!
        expect(Math.min(box.width, box.height), 'warnings Change').toBeGreaterThanOrEqual(44)
        const row = (await listRow(page, ACCOUNTS.main).boundingBox())!
        expect(row.height, 'account row').toBeGreaterThanOrEqual(44)
      }
      expect(errors).toEqual([])
    })

    // The approved design's check (AEON-1149 check.mjs), on the built page: folds and panels never move what
    // is above them, the menu offers no Sprint or Hold, Escape closes the menu first and then the panel, and
    // every control has a name.
    test(`accounts ${width} ${theme}: the design's checks hold on the built page`, async ({ page }) => {
      const errors = watchErrors(page)
      await setup(page, { limits: true })
      await page.goto('/settings/accounts')
      await expect(listRow(page, ACCOUNTS.main)).toBeVisible()
      const top = (locator: ReturnType<Page['locator']>) => locator.evaluate(el => Math.round(el.getBoundingClientRect().top * 10) / 10)
      const add = page.getByRole('button', { name: 'Add an account' }), heading = page.locator('#accounts-list-title')
      await add.scrollIntoViewIfNeeded()
      const before = [await top(add), await top(heading)]
      await add.click(); await expect(add).toHaveAttribute('aria-expanded', 'true')
      // ±0.5 px, the house tolerance for controls that stay put (AEON-541).
      expect(Math.abs(await top(add) - before[0]), 'Add an account moved').toBeLessThanOrEqual(.5)
      expect(Math.abs(await top(heading) - before[1]), 'Add an account moved what is above it').toBeLessThanOrEqual(.5)
      await add.click()
      const row = listRow(page, ACCOUNTS.main)
      await row.scrollIntoViewIfNeeded()
      const rowTop = await top(row)
      await row.click()
      await expect(pane(page).locator('[data-limits]')).toBeVisible()
      expect(Math.abs(await top(row) - rowTop), 'the account panel moved the page').toBeLessThanOrEqual(.5)
      const unnamed = await page.evaluate(() => [...document.querySelectorAll('.settings-page button, .settings-page input, .settings-page select, .settings-page [role="switch"], section.pane button, section.pane input, section.pane [role="switch"]')]
        .filter(e => (e as HTMLElement).offsetParent !== null)
        .filter(e => !(e.getAttribute('aria-label') || e.getAttribute('aria-labelledby') || e.textContent?.trim() || e.closest('label') || (e as HTMLElement).title))
        .map(e => e.outerHTML.slice(0, 90)))
      expect(unnamed, 'unnamed controls').toEqual([])
      await pane(page).getByRole('button', { name: /^More actions for / }).click()
      const menu = page.getByRole('menu')
      await expect(menu).toContainText('Read quota now'); await expect(menu).toContainText('Remove account')
      await expect(menu).not.toContainText(/Sprint|Hold|Back to the plan/)
      await page.keyboard.press('Escape')
      await expect(menu).toHaveCount(0); await expect(pane(page)).toBeVisible()
      await page.keyboard.press('Escape')
      await expect(pane(page)).toHaveCount(0)
      expect(await overflow(page)).toBeLessThanOrEqual(1)
      expect(errors).toEqual([])
    })

    test(`accounts ${width} ${theme}: a floor save and its Undo move no control in the panel`, async ({ page }) => {
      const { floors } = await setup(page)
      await page.goto('/settings/accounts')
      await listRow(page, ACCOUNTS.main).click()
      const limits = pane(page).locator('[data-limits]'), floor = limits.getByRole('spinbutton')
      await expect(floor).toHaveValue('10')
      const guard = await controlStability(page, { floor, capacity: limits.getByRole('link', { name: 'Daily limit, in Capacity' }), use: limits.locator(`[data-use="${ACCOUNTS.main}"]`).getByRole('switch'), close: pane(page).getByRole('button', { name: 'Close details' }), bar: pane(page).locator('[data-quota-bar]') })
      // A change saves on leaving the field.
      await guard.check(async () => {
        await floor.fill('15'); await floor.press('Tab')
        await expect(page.locator('.toast').filter({ hasText: 'Floor saved: 15 %.' })).toBeVisible()
        await expect(floor).toHaveValue('15')
      })
      await guard.check(async () => {
        await page.locator('.toast').filter({ hasText: 'Floor saved: 15 %.' }).getByRole('button', { name: 'Undo' }).click()
        await expect(page.locator('.toast').filter({ hasText: 'Floor restored: 10 %.' })).toBeVisible()
        await expect(floor).toHaveValue('10')
      })
      guard.done()
      expect(floors).toEqual([{ floor_percent: 15, revision: 1, binding_revision: 2 }, { floor_percent: 10, revision: 2, binding_revision: 2 }])
    })

    // AEON-1172 fix round 2: a successful Remove or End now used to drop its row, and
    // "Agents may use it" with everything below moved up under the pointer (AEON-541).
    test(`accounts ${width} ${theme}: Remove and End now leave “Agents may use it” and everything below in place`, async ({ page }, info) => {
      const errors = watchErrors(page)
      const { capacity } = await setup(page, { limits: true })
      // A Hold set before it was retired, saved as the old menu saved it: the schedule without its Keep for you.
      const { reserve: _r, reserve_percent: _p, ...shape } = capacity.schedules.find(e => e.scope === 'user')!.schedule
      capacity.schedules.push({ scope: 'pool', pool: 'codex', schedule: { ...shape, override: 'hold', override_until: new Date(NOW + 2 * 3600e3 - (NOW % 60_000)).toISOString() } })
      await page.goto('/settings/accounts')
      await listRow(page, ACCOUNTS.main).click()
      const limits = pane(page).locator('[data-limits]'), rule = limits.locator(`[data-rule="${ACCOUNTS.main}"]`), held = limits.locator('[data-held]')
      await expect(held.locator('[data-say] b')).toHaveText('Held until 16:02')
      await expect(pane(page).locator('[data-quota-say]')).toHaveText('Agents leave it alone until the hold ends.')
      const use = limits.locator(`[data-use="${ACCOUNTS.main}"]`).getByRole('switch')
      // Scrolled so the rows sit at the top of the panel, where the browser's scroll
      // anchoring acts: the guard measures in scroll coordinates, the screen check where the person sees it.
      await rule.evaluate(el => el.scrollIntoView({ block: 'start' }))
      const onScreen = () => use.evaluate(el => el.getBoundingClientRect().top), seen = await onScreen()
      const guard = await controlStability(page, { use, signedIn: pane(page).getByRole('heading', { name: 'Signed in on' }), close: pane(page).getByRole('button', { name: 'Close details' }) })
      await guard.check(async () => {
        await rule.getByRole('button', { name: 'Remove' }).click()
        await expect(rule.locator('[data-say] small')).toHaveText(/^Remove it for good\?/)
      })
      expect(Math.abs(await onScreen() - seen), 'switch on screen after Remove asks').toBeLessThanOrEqual(.5)
      await guard.check(async () => {
        await rule.getByRole('button', { name: 'Remove' }).click()
        await expect(rule.locator('[data-say] small')).toHaveText('Removed for good. The daily limit paces this account now.')
        await expect(rule.getByRole('button')).toHaveCount(0)
      })
      expect(Math.abs(await onScreen() - seen), 'switch on screen after Remove').toBeLessThanOrEqual(.5)
      await guard.check(async () => {
        await held.getByRole('button', { name: 'End now' }).click()
        await expect(held.locator('[data-say] b')).toHaveText('Hold ended')
        await expect(held.getByRole('button')).toHaveCount(0)
        await expect(pane(page).locator('[data-quota-say]')).toHaveText('Agents may use 4 more percentage points today. They stop at today’s limit (62 %) first.')
      })
      expect(Math.abs(await onScreen() - seen), 'switch on screen after End now').toBeLessThanOrEqual(.5)
      guard.done()
      await limits.scrollIntoViewIfNeeded()
      await page.screenshot({ path: info.outputPath(`accounts-limits-settled-${width}-${theme}.png`), fullPage: false })
      expect(capacity.writes.filter(w => w.method === 'DELETE')).toEqual([expect.objectContaining({ path: `/api/agent-accounts/${ACCOUNTS.main}/limit` })])
      expect(errors).toEqual([])
    })
  })
}

// AEON-1172 fix round 2: a failed plan read kept the last plan, and with it a door the
// server had called fresh, for good. The page now says the readings may be out of
// date, and two minutes after the reading was taken (the server's own trust in a
// reading) the bars stop promising room. The clock is injected; nothing waits on real time.
test('a plan that could not refresh says so, and its bars stop promising room after two minutes', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const errors = watchErrors(page)
  await setup(page, { limits: true })
  let planFails = false, plan = PLAN
  await page.route('**/api/agents/plan', route => route.request().method() !== 'GET' ? route.fallback() : planFails ? route.fulfill({ status: 503, json: { error: 'unavailable' } }) : route.fulfill({ json: plan }))
  const read = () => page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await page.goto('/settings/accounts')
  const caption = listRow(page, ACCOUNTS.main).locator('[data-quota-caption]'), status = page.locator('.loading-state')
  await expect(caption).toHaveText('42 % left this week · resets Fri 09:14')
  await listRow(page, ACCOUNTS.main).click()
  const say = pane(page).locator('[data-quota-say]'), room = 'Agents may use 4 more percentage points today. They stop at today’s limit (62 %) first.'
  await expect(say).toHaveText(room)
  await expect(listRow(page, ACCOUNTS.main).locator('[data-quota-bar] .room')).toHaveCount(1)
  await expect(status).not.toContainText('out of date')
  // The next read fails: the page says so at once; the plan that arrived a moment ago still holds.
  planFails = true
  await read()
  await expect(status).toContainText('Some readings may be out of date.')
  await expect(say).toHaveText(room)
  // Five minutes on, the plan is older than the server trusts a reading: no room is promised.
  await page.clock.setSystemTime(NOW + 5 * 60_000)
  await read()
  await expect(say).toHaveText('The last reading is out of date. Agents wait for a fresh one before they use more.')
  await expect(caption).toHaveText('42 % left · reading out of date')
  await expect(listRow(page, ACCOUNTS.main).locator('[data-quota-bar] .room')).toHaveCount(0)
  // A read that works again, with the server's new reading, brings the room back and the warning goes.
  planFails = false
  plan = structuredClone(PLAN)
  for (const state of Object.values(plan.daily_state)) for (const d of state.accounts) d.read_at = new Date(NOW + 5 * 60_000).toISOString()
  await read()
  await expect(say).toHaveText(room)
  await expect(status).not.toContainText('out of date')
  expect(errors).toEqual([])
})

// AEON-1172 fix round 3: the two minutes run from when the reading was taken, as the
// server counts them, not from when the plan reached the page. Main's reading is 90 s
// old on arrival; 35 s later, with every refresh failing, the server no longer trusts
// it and the page promises no room. The clock is injected; nothing waits on real time.
test('a reading nearly two minutes old stops promising room when the server stops trusting it', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const errors = watchErrors(page)
  await setup(page, { limits: true })
  const old = structuredClone(PLAN)
  old.daily_state.codex.accounts[1]!.read_at = new Date(NOW - 90_000).toISOString()
  let planFails = false
  await page.route('**/api/agents/plan', route => route.request().method() !== 'GET' ? route.fallback() : planFails ? route.fulfill({ status: 503, json: { error: 'unavailable' } }) : route.fulfill({ json: old }))
  const read = () => page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await page.goto('/settings/accounts')
  const caption = listRow(page, ACCOUNTS.main).locator('[data-quota-caption]'), status = page.locator('.loading-state')
  await listRow(page, ACCOUNTS.main).click()
  const say = pane(page).locator('[data-quota-say]')
  await expect(say).toHaveText('Agents may use 4 more percentage points today. They stop at today’s limit (62 %) first.')
  await expect(caption).toHaveText('42 % left this week · resets Fri 09:14')
  planFails = true
  await page.clock.setSystemTime(NOW + 35_000)
  await read()
  await expect(status).toContainText('Some readings may be out of date.')
  await expect(say).toHaveText('The last reading is out of date. Agents wait for a fresh one before they use more.')
  await expect(caption).toHaveText('42 % left · reading out of date')
  await expect(listRow(page, ACCOUNTS.main).locator('[data-quota-bar] .room')).toHaveCount(0)
  expect(errors).toEqual([])
})

// AEON-1172 fix round 3: End now's write re-reads everything; the capacity read lands
// first, without the hold, while the others are still out. The Held row used to vanish
// until they all answered, moving "Agents may use it" up and back under the pointer.
// Remove's row vanished the same way when a read landed before its write answered.
// Barriers hold those answers; the guard measures while they are held.
test('End now and Remove keep their rows while the reads after them are still out', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const errors = watchErrors(page)
  const { capacity } = await setup(page, { limits: true })
  const { reserve: _r, reserve_percent: _p, ...shape } = capacity.schedules.find(e => e.scope === 'user')!.schedule
  capacity.schedules.push({ scope: 'pool', pool: 'codex', schedule: { ...shape, override: 'hold', override_until: new Date(NOW + 2 * 3600e3 - (NOW % 60_000)).toISOString() } })
  const barrier = () => { let open!: () => void; const passed = new Promise<void>(resolve => { open = resolve }); return { open, passed } }
  // The removal is saved at once; its answer waits behind a barrier. So do the runs reads after End now.
  const removal = barrier(), runs = barrier()
  let holdRuns = false, runsHeld = 0
  await page.route(/\/api\/agent-accounts\/[^/]+\/limit$/, async route => {
    if (route.request().method() !== 'DELETE') return route.fallback()
    const answer = capacity.handle(new URL(route.request().url()).pathname, 'DELETE', null)
    await removal.passed
    return route.fulfill({ status: answer?.status ?? 500 })
  })
  await page.route(/\/api\/runs\?/, async route => {
    if (holdRuns && route.request().method() === 'GET') { runsHeld++; await runs.passed }
    return route.fallback()
  })
  await page.goto('/settings/accounts')
  await listRow(page, ACCOUNTS.main).click()
  const limits = pane(page).locator('[data-limits]'), rule = limits.locator(`[data-rule="${ACCOUNTS.main}"]`), held = limits.locator('[data-held]')
  const bar = listRow(page, ACCOUNTS.main).locator('[data-quota-bar]'), say = pane(page).locator('[data-quota-say]')
  await expect(held.locator('[data-say] b')).toHaveText('Held until 16:02')
  await expect(bar).toHaveAttribute('aria-label', /limit set by you at 66 %/)
  const use = limits.locator(`[data-use="${ACCOUNTS.main}"]`).getByRole('switch')
  const guard = await controlStability(page, { use, signedIn: pane(page).getByRole('heading', { name: 'Signed in on' }), close: pane(page).getByRole('button', { name: 'Close details' }) })
  await guard.check(() => rule.getByRole('button', { name: 'Remove' }).click())
  // Remove is confirmed; a read lands without the rule before the removal answers.
  await guard.check(async () => {
    await rule.getByRole('button', { name: 'Remove' }).click()
    await page.evaluate(() => window.dispatchEvent(new Event('focus')))
    await expect(bar).not.toHaveAttribute('aria-label', /limit set by you/)
    await expect(rule.locator('[data-say] small')).toHaveText(/^Remove it for good\?/)
    await expect(rule.getByRole('button', { name: 'Remove' })).toBeDisabled()
  })
  await guard.check(async () => {
    removal.open()
    await expect(rule.locator('[data-say] small')).toHaveText('Removed for good. The daily limit paces this account now.')
  })
  // End now: the capacity read lands without the hold while the runs read is held.
  // Remove's own reads have all answered once End now is enabled again.
  await expect(held.getByRole('button', { name: 'End now' })).toBeEnabled()
  holdRuns = true
  await guard.check(async () => {
    await held.getByRole('button', { name: 'End now' }).click()
    await expect(say).toHaveText('Agents may use 4 more percentage points today. They stop at today’s limit (62 %) first.')
    await expect.poll(() => runsHeld).toBeGreaterThan(0)
    await expect(held.locator('[data-say] b')).toHaveText('Held until 16:02')
    await expect(held.getByRole('button', { name: 'End now' })).toBeDisabled()
  })
  await guard.check(async () => {
    runs.open()
    await expect(held.locator('[data-say] b')).toHaveText('Hold ended')
    await expect(held.getByRole('button')).toHaveCount(0)
  })
  guard.done()
  expect(capacity.writes.filter(w => w.method === 'DELETE')).toEqual([expect.objectContaining({ path: `/api/agent-accounts/${ACCOUNTS.main}/limit` })])
  expect(errors).toEqual([])
})
