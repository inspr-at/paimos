// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1172 (design AEON-1149): Settings › Accounts shows one picture of every limit.
// Risks: a row or the panel claims room agents do not have; a limit save moves the
// controls; a touch target in the new rows is under 44 px; the legend or bars push
// the page sideways on a phone. The plan's daily_state drives the bars; accounts
// outside it show only their reading.
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { ACCOUNTS, NOW, TZ, capacityWorld, type CapacityOptions } from './capacity-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { controlStability } from './control-stability'
import type { AccountUsagePolicy } from '../src/lib/accountUsage'

test.use({ timezoneId: TZ })
const door = (id: string, used: number, start: number, limit: number, over: Record<string, unknown> = {}) => ({
  account_id: id, label: id, order: 1, used_pct: used, left_pct: 100 - used, start_of_day_used_pct: start, limit_used_pct: limit, floor_pct: 10,
  resets_at: '2026-10-02T07:14:00Z', read_at: new Date(NOW - 2 * 60_000).toISOString(), freshness: 'fresh', resets: null, routable: true, can_edit: true, ...over,
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
      expect([await top(add), await top(heading)], 'Add an account moved what is above it').toEqual(before)
      await add.click()
      const row = listRow(page, ACCOUNTS.main)
      await row.scrollIntoViewIfNeeded()
      const rowTop = await top(row)
      await row.click()
      await expect(pane(page).locator('[data-limits]')).toBeVisible()
      expect(await top(row), 'the account panel moved the page').toBe(rowTop)
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
        await expect(limits.locator('[data-limit="floor"] small')).toContainText('the last 15 %')
        await expect(page.locator('.toast').filter({ hasText: 'Floor saved: 15 %.' })).toBeVisible()
      })
      await guard.check(async () => {
        await page.locator('.toast').filter({ hasText: 'Floor saved: 15 %.' }).getByRole('button', { name: 'Undo' }).click()
        await expect(limits.locator('[data-limit="floor"] small')).toContainText('the last 10 %')
        await expect(floor).toHaveValue('10')
      })
      guard.done()
      expect(floors).toEqual([{ floor_percent: 15, revision: 1, binding_revision: 2 }, { floor_percent: 10, revision: 2, binding_revision: 2 }])
    })
  })
}
