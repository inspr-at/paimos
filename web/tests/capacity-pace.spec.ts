// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1175 (design AEON-1149): Settings › Capacity is the home of pace, one stored value
// with two editors. Risks: the dial and Capacity store pace differently or overwrite a
// change made elsewhere; a number equal to the default is copied instead of followed;
// the default pace saves without Undo; the picker or a save moves a control; a person
// without account management can change pace here; the phone layout scrolls sideways
// or has targets under 44 px. AEON1175_SHOTS=1 also writes screenshots into each test's
// output directory, including the Keep for you and work-week editors for the pixel check.
import { test, expect, type Page, type TestInfo } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { ACCOUNTS, NOW, TZ, capacityWorld } from './capacity-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { controlStability } from './control-stability'
import { mockDailyPlan } from './agents-daily-fixtures'
import { HARNESS_LABEL } from '../src/lib/agentState'

test.use({ timezoneId: TZ })
// The harness's name as the dial shows it: "Claude" until AEON-1160 names it "Claude Code"; both pass.
// The vendor's accounts stay "Claude" ("the Claude account").
const CLAUDE = HARNESS_LABEL.claude!

async function permissions(page: Page, manage: boolean) {
  await page.route('**/api/me/permissions*', route => {
    const answer = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    answer.workspace.permissions.push('account.read', ...(manage ? ['account.manage'] : []))
    return route.fulfill({ json: answer })
  })
}
async function world(page: Page, options: { dial?: Record<string, unknown> } = {}) {
  await page.clock.setSystemTime(NOW)
  const work = fixtures()
  work.preferences['ui.agents.sections'] = { dial: true }
  if (options.dial) work.preferences['ui.agents.dial'] = options.dial
  await mockWork(page, work, { admin: true })
  const data = agentData({ me: me.id, now: NOW, projects: {}, tickets: {}, nodes: {} })
  const capacity = capacityWorld()
  data.accounts = (capacity.accounts as unknown as typeof data.accounts).map(a => ({ ...a, registered_by_principal_id: me.id }))
  await mockAgents(page, data, { capacity })
  await page.route('**/api/queue', route => route.fulfill({ json: { items: [], manual_order: false, capacity: {} } }))
  return { work, capacity }
}

// ---------- One stored value, two editors ----------
test('one value, two editors: the dial’s pace shows in Capacity, Capacity’s on the dial, both through the same guarded write', async ({ page }) => {
  const errors = watchErrors(page)
  await page.setViewportSize({ width: 1440, height: 1000 })
  await world(page, { dial: { selected: 'claude', folds: { 'claude:pace': true } } })
  await permissions(page, true)
  const backend = await mockDailyPlan(page, { example: 'pace' })

  await page.goto('/agents')
  const points = page.locator('[data-detail="claude"]').getByLabel('Percentage points a day')
  await expect(points).toHaveValue('10')
  await points.fill('14'); await points.press('Enter')
  await expect.poll(() => backend.puts.length).toBe(1)
  expect(backend.puts[0]!.value.daily!.claude!.pace).toEqual({ mode: 'pace', points_per_day: 14 })

  // The dial's "in Capacity" lands on the harness's row.
  await page.goto('/settings/capacity#pace-claude')
  const picker = page.locator('button[data-pace="claude"]')
  await expect(picker).toHaveAccessibleName(`Daily limit for ${CLAUDE}: +14 percentage points a day. Change`)
  await expect(page.locator('#pace-claude')).toBeFocused()
  await expect(page).toHaveURL('/settings/capacity')

  await picker.click()
  const pop = page.getByRole('dialog', { name: `Daily limit for ${CLAUDE}`, exact: true })
  await expect(pop.getByRole('radio', { name: 'Its own pace' })).toHaveAttribute('aria-checked', 'true')
  await pop.getByLabel('Own pace, percentage points a day').fill('12')
  await pop.getByRole('radio', { name: 'Its own pace' }).click()
  await expect.poll(() => backend.puts.length).toBe(2)
  // The same write as the dial's: the plan as read, one harness changed, guarded by its revision.
  expect(backend.puts[1]).toEqual({
    expected_updated_at: new Date(NOW + 1000).toISOString(),
    value: {
      total: 12, limits: { codex: 6, claude: 2, cursor: 'no_limit' },
      daily: {
        claude: { pace: { mode: 'pace', points_per_day: 12 }, boost_today: null, at_limit: 'ladder' },
        codex: { pace: { mode: 'pace', points_per_day: null }, boost_today: null, at_limit: 'ladder' },
        cursor: { pace: { mode: 'pace', points_per_day: null }, boost_today: null, at_limit: 'ladder' },
      },
    },
  })
  await expect(page.locator('.toast').filter({ hasText: `${CLAUDE}: +12 percentage points a day` })).toBeVisible()
  await expect(picker).toHaveAccessibleName(`Daily limit for ${CLAUDE}: +12 percentage points a day. Change`)
  await expect(picker).toBeFocused()

  await page.goto('/agents')
  await expect(page.locator('[data-detail="claude"]').getByLabel('Percentage points a day')).toHaveValue('12')
  expect(errors).toEqual([])
})

test('a number equal to the default follows the default in Capacity too, and Undo writes the value before', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await world(page)
  await permissions(page, true)
  const backend = await mockDailyPlan(page, { example: 'pace', settings: { claude: { pace: { mode: 'pace', points_per_day: 15 }, boost_today: null, at_limit: 'ladder' } } })
  await page.goto('/settings/capacity')
  const picker = page.locator('button[data-pace="claude"]')
  await expect(picker).toHaveAccessibleName(`Daily limit for ${CLAUDE}: +15 percentage points a day. Change`)
  await picker.click()
  const pop = page.getByRole('dialog', { name: `Daily limit for ${CLAUDE}`, exact: true })
  await pop.getByLabel('Own pace, percentage points a day').fill('10')
  await pop.getByLabel('Own pace, percentage points a day').press('Enter')
  await expect.poll(() => backend.puts.length).toBe(1)
  expect(backend.puts[0]!.value.daily!.claude!.pace).toEqual({ mode: 'pace', points_per_day: null })
  await expect(picker).toHaveAccessibleName(`Daily limit for ${CLAUDE}: +10 percentage points a day, the default. Change`)
  await expect(picker).toContainText('· default')
  const toast = page.locator('.toast').filter({ hasText: `${CLAUDE}: +10 percentage points a day, the default` })
  await toast.getByRole('button', { name: 'Undo' }).click()
  await expect.poll(() => backend.puts.length).toBe(2)
  // Undo is guarded by the revision its own write left, so it never overwrites a later change.
  expect(backend.puts[1]).toMatchObject({ expected_updated_at: new Date(NOW + 1000).toISOString(), value: { daily: { claude: { pace: { mode: 'pace', points_per_day: 15 } } } } })
  await expect(picker).toHaveAccessibleName(`Daily limit for ${CLAUDE}: +15 percentage points a day. Change`)
})

test('a pace changed elsewhere meanwhile is read again and said; nothing is overwritten', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await world(page)
  await permissions(page, true)
  const backend = await mockDailyPlan(page, { example: 'pace' })
  let reads = 0
  page.on('request', request => { if (request.url().endsWith('/api/agents/plan') && request.method() === 'GET') reads++ })
  await page.goto('/settings/capacity')
  const picker = page.locator('button[data-pace="codex"]')
  await expect(picker).toHaveAccessibleName('Daily limit for Codex: +10 percentage points a day, the default. Change')
  // Another tab sets Codex to use everything; this page still shows the plan it read.
  backend.settings = { ...backend.settings, codex: { pace: { mode: 'everything', points_per_day: null }, boost_today: null, at_limit: 'ladder' } }
  backend.conflicts = 1
  await picker.click()
  const before = reads
  await page.getByRole('dialog', { name: 'Daily limit for Codex' }).getByRole('radio', { name: 'Its own pace' }).click()
  await expect(page.locator('.toast.error').filter({ hasText: 'Changed elsewhere; shown as it is now.' })).toBeVisible()
  await expect.poll(() => reads).toBeGreaterThan(before)
  await expect(picker).toHaveAccessibleName('Daily limit for Codex: Use everything. Change')
  expect(backend.puts).toHaveLength(1)
  expect(backend.settings.codex!.pace).toEqual({ mode: 'everything', points_per_day: null })
})

// ---------- Capacity on its own: the default pace, bars, stability, read-only, phone ----------
// Read as the page opens: the server trusts a reading for two minutes from read_at.
const door = (id: string, label: string, order: number, used: number, start: number, limit: number, over: Record<string, unknown> = {}) => ({
  account_id: id, label, order, used_pct: used, left_pct: 100 - used, start_of_day_used_pct: start, limit_used_pct: limit, floor_pct: 10,
  resets_at: '2026-10-02T07:14:00Z', read_at: new Date(NOW).toISOString(), freshness: 'fresh', resets: null, routable: true, can_edit: true, ...over,
})
function planFor(defaultPoints: number | null, settings: Record<string, unknown>, updatedAt: string | null) {
  const def = defaultPoints ?? 10
  return {
    total: 15, limits: {}, principal_id: me.id, running: {}, running_total: 0, source: 'plan', updated_at: updatedAt, daily_default_points: def, daily_timezone: TZ, daily_until: '2026-09-29T22:00:00Z',
    daily: settings,
    daily_state: {
      // Spare is low and at its floor, Main has room, Studio's reading is old; Claude is on Boost today.
      codex: { state: 'on_pace', limit_used_pct: 55 + def, today_points_used: 3, today_points_allowed: def, over_pace_points: 0, active_account_id: ACCOUNTS.main, next_on_ladder: null, accounts: [door(ACCOUNTS.main, 'Main', 1, 58, 55, 55 + def), door(ACCOUNTS.spare, 'Spare', 2, 91, 89, 90), door(ACCOUNTS.studio, 'Studio', 3, 22, 22, 22 + def, { freshness: 'stale' })] },
      claude: { state: 'on_pace', limit_used_pct: 68, today_points_used: 4, today_points_allowed: 9, over_pace_points: 0, active_account_id: ACCOUNTS.claude, next_on_ladder: null, accounts: [door(ACCOUNTS.claude, 'markus', 1, 63, 59, 68)] },
      cursor: { state: 'no_limit', limit_used_pct: null, today_points_used: null, today_points_allowed: null, over_pace_points: null, active_account_id: ACCOUNTS.cursor, next_on_ladder: null, accounts: [door(ACCOUNTS.cursor, 'markus', 1, 0, 0, 0, { used_pct: null, left_pct: null, limit_used_pct: null, no_daily_limit: true, freshness: 'unknown', read_at: null })] },
    },
  }
}
const SETTINGS = {
  codex: { pace: { mode: 'pace', points_per_day: null }, boost_today: null, at_limit: 'ladder' },
  claude: { pace: { mode: 'pace', points_per_day: 6 }, boost_today: { limit_used_pct: 68, entered_as: 'used', until: '2026-09-29T22:00:00Z' }, at_limit: 'wait' },
}
async function capacityPage(page: Page, options: { manage?: boolean } = {}) {
  await world(page)
  await permissions(page, options.manage !== false)
  const state = {
    stored: null as number | null, profileRevision: 0, patches: [] as unknown[], settings: structuredClone(SETTINGS) as Record<string, unknown>, revision: null as string | null,
    // While set, profile reads answer with this older copy: what the page cached before another tab's change.
    cached: undefined as { stored: number | null; revision: number } | undefined, profileReads: 0,
  }
  await page.route('**/api/agents/plan', route => route.request().method() === 'GET' ? route.fulfill({ json: planFor(state.stored, state.settings, state.revision) }) : route.fallback())
  await page.route('**/api/settings/quota-warnings', route => route.request().method() === 'GET' ? route.fulfill({ json: { early_percent: 10, urgent_percent: 3 } }) : route.fallback())
  const profile = (stored = state.stored, revision = state.profileRevision) => ({ principal_id: me.id, email: 'markus@example.test', first_name: 'Markus', last_name: 'Barta', preferred_name: '', short_name: 'mba', initials: 'MB', timezone: TZ, locale: 'en-GB', greeting_enabled: true, avatar_color: 'teal', avatar_hashes: {}, week_start: 1, daily_points_per_day: stored, revision })
  await page.route('**/api/me/profile', route => {
    if (route.request().method() === 'PATCH') {
      const body = route.request().postDataJSON() as { daily_points_per_day?: number | null; expected_revision?: number }
      state.patches.push(body)
      // As the server: a guarded patch applies only at its revision; a change bumps the revision.
      if (body.expected_revision !== undefined && body.expected_revision !== state.profileRevision) return route.fulfill({ status: 409, json: { errors: { revision: 'changed since it was read' } } })
      if ('daily_points_per_day' in body && (body.daily_points_per_day ?? null) !== state.stored) { state.stored = body.daily_points_per_day ?? null; state.profileRevision++ }
      return route.fulfill({ json: profile() })
    }
    state.profileReads++
    return route.fulfill({ json: state.cached ? profile(state.cached.stored, state.cached.revision) : profile() })
  })
  return state
}
const overflow = (page: Page) => page.evaluate(() => document.documentElement.scrollWidth - innerWidth)
async function shot(page: Page, info: TestInfo, name: string, full = true) {
  if (!process.env.AEON1175_SHOTS) return
  if (!full) { await page.screenshot({ path: info.outputPath(`${name}.png`) }); return }
  // Settings scrolls in its own body: the shot takes a viewport as tall as that body, then restores it.
  const size = page.viewportSize()!
  const tallest = await page.evaluate(() => Math.max(innerHeight, ...[...document.querySelectorAll('*')].filter(e => /auto|scroll/.test(getComputedStyle(e).overflowY) && e.scrollHeight > e.clientHeight + 4).map(e => e.scrollHeight - e.clientHeight + innerHeight)))
  await page.setViewportSize({ width: size.width, height: Math.min(6000, Math.ceil(tallest)) })
  await page.screenshot({ path: info.outputPath(`${name}.png`) })
  await page.setViewportSize(size)
}

test('the default pace saves on Enter with Undo; harnesses that follow it follow along, the others keep their own', async ({ page }) => {
  const errors = watchErrors(page)
  await page.setViewportSize({ width: 1440, height: 1000 })
  const state = await capacityPage(page)
  await page.goto('/settings/capacity')
  const field = page.locator('#def-pace')
  await expect(field).toHaveValue('10')
  await expect(page.locator('button[data-pace="codex"]')).toHaveAccessibleName('Daily limit for Codex: +10 percentage points a day, the default. Change')
  await expect(page.locator('button[data-pace="claude"]')).toHaveAccessibleName(`Daily limit for ${CLAUDE}: +6 percentage points a day. Change`)
  const guard = await controlStability(page, { field, codex: page.locator('button[data-pace="codex"]'), claude: page.locator('button[data-pace="claude"]'), keep: page.locator('#capacity-and-load').getByRole('button', { name: /Keep for you/ }) })
  await guard.check(async () => {
    await field.fill('14'); await field.press('Enter')
    await expect.poll(() => state.patches).toEqual([{ daily_points_per_day: 14, expected_revision: 0 }])
    await expect(page.locator('button[data-pace="codex"]')).toHaveAccessibleName('Daily limit for Codex: +14 percentage points a day, the default. Change')
  })
  await expect(page.locator('button[data-pace="claude"]')).toHaveAccessibleName(`Daily limit for ${CLAUDE}: +6 percentage points a day. Change`)
  await expect(field).toHaveValue('14')
  await guard.check(async () => {
    await page.locator('.toast').filter({ hasText: 'Default pace: +14 percentage points a day' }).getByRole('button', { name: 'Undo' }).click()
    // The value before was the built-in default: Undo stores null again, not a copy of 10.
    await expect.poll(() => state.patches).toEqual([{ daily_points_per_day: 14, expected_revision: 0 }, { daily_points_per_day: null, expected_revision: 1 }])
    await expect(field).toHaveValue('10')
  })
  guard.done()
  // A wrong number is said and nothing is written; Esc leaves the field with the stored value.
  await field.fill('60'); await field.press('Enter')
  await expect(page.locator('.toast.error').filter({ hasText: 'Use a whole number from 1 to 50 for the default pace.' })).toBeVisible()
  await expect(field).toHaveValue('10')
  await field.focus(); await field.fill('3'); await field.press('Escape')
  await expect(field).not.toBeFocused()
  await expect(field).toHaveValue('10')
  expect(state.patches).toHaveLength(2)
  expect(errors).toEqual([])
})

// Gate finding (fix round 2): Undo restored the cached profile's value, and overwrote a later change.
test('the default pace’s Undo puts back the value stored when it saved, never a stale copy, and never overwrites a change made since', async ({ page }) => {
  const errors = watchErrors(page)
  await page.setViewportSize({ width: 1440, height: 1000 })
  const state = await capacityPage(page)
  // Another tab set the default to 20 after this page cached the profile (still null); the plan shows 20.
  Object.assign(state, { stored: 20, profileRevision: 1, cached: { stored: null, revision: 0 } })
  await page.goto('/settings/capacity')
  const field = page.locator('#def-pace')
  await expect(field).toHaveValue('20')
  await expect.poll(() => state.profileReads).toBeGreaterThan(0)
  state.cached = undefined
  await field.fill('30'); await field.press('Enter')
  // Guarded by the revision of the value it replaces, read as it saves.
  await expect.poll(() => state.patches).toEqual([{ daily_points_per_day: 30, expected_revision: 1 }])
  await expect(field).toHaveValue('30')
  await page.locator('.toast').filter({ hasText: 'Default pace: +30 percentage points a day' }).getByRole('button', { name: 'Undo' }).click()
  // The 20 that was stored, not the cached null; guarded by the revision the edit left.
  await expect.poll(() => state.patches.at(-1)).toEqual({ daily_points_per_day: 20, expected_revision: 2 })
  await expect(field).toHaveValue('20')
  expect(state.stored).toBe(20)

  // Edit again; once the page has read the plan back, another tab sets 35. Only Undo's guard can see it.
  const readBack = page.waitForResponse(r => r.url().endsWith('/api/agents/plan') && r.request().method() === 'GET' && state.patches.length >= 3)
  await field.fill('25'); await field.press('Enter')
  await expect.poll(() => state.patches.at(-1)).toEqual({ daily_points_per_day: 25, expected_revision: 3 })
  await readBack
  await expect(field).toHaveValue('25')
  Object.assign(state, { stored: 35, profileRevision: 5 })
  await page.locator('.toast').filter({ hasText: 'Default pace: +25 percentage points a day' }).getByRole('button', { name: 'Undo' }).click()
  await expect(page.locator('.toast.error').filter({ hasText: 'Changed elsewhere; shown as it is now.' })).toBeVisible()
  expect(state.patches.at(-1)).toEqual({ daily_points_per_day: 20, expected_revision: 4 })
  await expect(field).toHaveValue('35')
  expect(state.stored).toBe(35)
  expect(errors).toEqual([])
})

test('Undo of a harness’s pace puts back the whole pace before, the number “use everything” keeps included', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await world(page)
  await permissions(page, true)
  const backend = await mockDailyPlan(page, { example: 'pace', settings: { claude: { pace: { mode: 'everything', points_per_day: 13 }, boost_today: null, at_limit: 'ladder' } } })
  await page.goto('/settings/capacity')
  const picker = page.locator('button[data-pace="claude"]')
  await expect(picker).toHaveAccessibleName(`Daily limit for ${CLAUDE}: Use everything. Change`)
  await picker.click()
  const own = page.getByRole('dialog', { name: `Daily limit for ${CLAUDE}`, exact: true }).getByLabel('Own pace, percentage points a day')
  await own.fill('20'); await own.press('Enter')
  await expect.poll(() => backend.puts.length).toBe(1)
  expect(backend.puts[0]!.value.daily!.claude!.pace).toEqual({ mode: 'pace', points_per_day: 20 })
  await page.locator('.toast').filter({ hasText: `${CLAUDE}: +20 percentage points a day` }).getByRole('button', { name: 'Undo' }).click()
  await expect.poll(() => backend.puts.length).toBe(2)
  // The 13 comes back with "use everything", so switching back to pace on the dial uses 13 again.
  expect(backend.puts[1]!.expected_updated_at).toBe(new Date(NOW + 1000).toISOString())
  expect(backend.puts[1]!.value.daily!.claude!.pace).toEqual({ mode: 'everything', points_per_day: 13 })
  await expect(picker).toHaveAccessibleName(`Daily limit for ${CLAUDE}: Use everything. Change`)
  await expect(page.locator('.toast').filter({ hasText: `${CLAUDE}: uses everything before the reset` })).toBeVisible()
})

test('the bars, Boost today and what happens at the limit say what the plan says', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await capacityPage(page)
  await page.goto('/settings/capacity')
  const codex = page.locator('#pace-codex'), claude = page.locator('#pace-claude')
  // A row names the harness, as the dial does (design AEON-1149: "Claude Code").
  expect(['Claude', 'Claude Code']).toContain(CLAUDE)
  await expect(claude.locator('.hz-n b')).toHaveText(CLAUDE)
  await expect(codex.locator('.hz-s')).toHaveText('3 accounts · used in this order')
  await expect(codex.locator('.ab-who b')).toHaveText(['Main', 'Spare', 'Studio'])
  const caption = (id: string) => page.locator(`.ab[data-account="${id}"] [data-capacity-caption]`)
  await expect(caption(ACCOUNTS.main)).toHaveText('58 % used · 7 more percentage points today · resets Fri 09:14')
  await expect(caption(ACCOUNTS.spare)).toHaveText('Low · 9 % left · agents stopped at the floor')
  await expect(caption(ACCOUNTS.studio)).toHaveText('78 % left · reading out of date')
  await expect(claude.locator('.boost')).toHaveText('Boost today: up to 68 % used · ends at midnight · set on the Agents dial')
  await expect(page.locator('#pace-cursor .hz-none')).toHaveText('Cursor doesn’t report its quota, so there is nothing to pace. The agent count on the dial still caps it.')
  await expect(page.locator('#pace-cursor [data-pace]')).toHaveCount(0)
  // At the limit follows Models, shown as a link; Claude still waits as set on the dial.
  const foot = page.locator('.cap-foot')
  await expect(foot.locator('.cap-line').first()).toContainText(`At the limit, new work follows Settings › Models: the next model in its order runs, or the work waits. ${CLAUDE} waits until midnight instead, as set on the Agents dial.`)
  await expect(foot.getByRole('link', { name: 'Models' })).toHaveAttribute('href', '/settings/models')
  await expect(foot.getByRole('link', { name: 'Agents' })).toHaveAttribute('href', '/agents')
  // The floor links to the account's own panel, where it is set.
  await page.getByRole('button', { name: 'Change the floor of Codex · Main in its account panel' }).click()
  await expect(page.locator('section.pane [data-limits]')).toBeVisible()
})

test('the picker overlays and a save moves nothing; the picker keeps its width whatever it says', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const backend = await (async () => { await world(page); await permissions(page, true); return mockDailyPlan(page, { example: 'pace' }) })()
  await page.goto('/settings/capacity')
  const codex = page.locator('button[data-pace="codex"]'), claude = page.locator('button[data-pace="claude"]')
  await expect(codex).toBeVisible()
  const guard = await controlStability(page, { codex, claude, field: page.locator('#def-pace'), days: page.locator('#capacity-and-load').getByRole('radio', { name: '5' }), keep: page.locator('#capacity-and-load').getByRole('button', { name: /Keep for you/ }) })
  await guard.check(async () => {
    await claude.click()
    await expect(page.getByRole('dialog', { name: `Daily limit for ${CLAUDE}`, exact: true })).toBeVisible()
  })
  await guard.check(async () => {
    await page.getByRole('dialog', { name: `Daily limit for ${CLAUDE}`, exact: true }).getByRole('radio', { name: 'Use everything before the reset' }).click()
    await expect.poll(() => backend.puts.length).toBe(1)
    await expect(claude).toHaveAccessibleName(`Daily limit for ${CLAUDE}: Use everything. Change`)
  })
  await guard.check(async () => {
    await claude.click()
    await page.getByRole('dialog', { name: `Daily limit for ${CLAUDE}`, exact: true }).getByRole('radio', { name: 'Follow the default' }).click()
    await expect.poll(() => backend.puts.length).toBe(2)
    await expect(claude).toContainText('· default')
  })
  guard.done()
  // Keyboard: the arrows move between the choices, Esc closes and hands focus back.
  await claude.click()
  const pop = page.getByRole('dialog', { name: `Daily limit for ${CLAUDE}`, exact: true })
  // The title names the harness, the foot the vendor's account.
  await expect(pop.getByRole('heading')).toHaveText(`Daily limit for ${CLAUDE}`)
  await expect(pop.locator('.pp-foot')).toContainText('Applies to the Claude account.')
  await expect(pop.getByRole('radio', { name: 'Follow the default' })).toBeFocused()
  await page.keyboard.press('ArrowDown')
  await expect(pop.getByRole('radio', { name: 'Its own pace' })).toBeFocused()
  await page.keyboard.press('Escape')
  await expect(pop).toHaveCount(0)
  await expect(claude).toBeFocused()
  expect(backend.puts).toHaveLength(2)
})

test('without account management, Capacity reads: no picker, no default field, no pacing controls', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await capacityPage(page, { manage: false })
  await page.goto('/settings/capacity')
  await expect(page.locator('#pace-codex .pick-s.ro')).toHaveText('+10 percentage points a day · default')
  await expect(page.locator('button[data-pace]')).toHaveCount(0)
  await expect(page.locator('#def-pace')).toBeDisabled()
  const editable = await page.locator('#capacity-and-load button:not(:disabled), #capacity-and-load [role="switch"]:not(:disabled)').count()
  expect(editable).toBe(0)
})

for (const [width, touch] of [[1440, false], [1024, false], [390, true]] as const) for (const theme of ['light', 'dark'] as const) {
  test.describe(`${width} ${theme}`, () => {
    test.use({ viewport: { width, height: width > 600 ? 1000 : 860 }, hasTouch: touch, isMobile: touch, colorScheme: theme })
    test(`capacity ${width} ${theme}: named controls, no sideways scroll, ${touch ? '44 px targets, ' : ''}one-line labels stay on one line`, async ({ page }, info) => {
      const errors = watchErrors(page)
      await capacityPage(page)
      await page.goto('/settings/capacity')
      await expect(page.locator('button[data-pace="codex"]')).toBeVisible()
      expect(await overflow(page)).toBeLessThanOrEqual(1)
      const audit = await page.evaluate(touch => {
        const scope = [...document.querySelectorAll<HTMLElement>('#daily, #capacity-and-load')]
        const inScope = (e: Element) => scope.some(s => s.contains(e)) && (e as HTMLElement).offsetParent !== null
        const unnamed = [...document.querySelectorAll('button, [role="switch"], input, select')].filter(inScope).filter(b => !(b.getAttribute('aria-label') || b.getAttribute('aria-labelledby') || b.textContent?.trim() || b.closest('label'))).map(b => b.outerHTML.slice(0, 80))
        const wrapped = [...document.querySelectorAll<HTMLElement>('.pick-s, .keep-btn, .seg button, .cap-line .go')].filter(inScope).filter(e => e.getBoundingClientRect().height > Math.max(parseFloat(getComputedStyle(e).lineHeight) || 18, 18) * 1.7 + 14).map(e => e.textContent?.trim())
        const small = touch ? [...document.querySelectorAll<HTMLElement>('button, a, input, [role="switch"]')].filter(inScope).map(e => ({ e, r: e.getBoundingClientRect() })).filter(({ r }) => r.width && r.height && (r.width < 43.5 || r.height < 43.5)).map(({ e, r }) => `${(e.textContent || e.getAttribute('aria-label') || e.className).toString().trim().slice(0, 24)} ${Math.round(r.width)}×${Math.round(r.height)}`) : []
        return { unnamed, wrapped, small }
      }, touch)
      expect(audit.unnamed).toEqual([])
      expect(audit.wrapped).toEqual([])
      expect(audit.small).toEqual([])
      await shot(page, info, `capacity-${width}-${theme}`)
      await page.locator('button[data-pace="claude"]').click()
      await expect(page.getByRole('dialog', { name: `Daily limit for ${CLAUDE}`, exact: true })).toBeVisible()
      expect(await overflow(page)).toBeLessThanOrEqual(1)
      await shot(page, info, `capacity-picker-${width}-${theme}`, false)
      await page.keyboard.press('Escape')
      if (width !== 1024) {
        // The editors behind Keep for you and the work-week gear, unchanged (compared pixel by pixel against main).
        await page.locator('#capacity-and-load').getByRole('button', { name: /Keep for you/ }).click()
        const keep = page.getByRole('dialog').filter({ has: page.locator('#keep-title') })
        await expect(keep).toBeVisible()
        if (process.env.AEON1175_SHOTS) await keep.screenshot({ path: info.outputPath(`editor-keep-${width}-${theme}.png`) })
        await page.keyboard.press('Escape')
        await expect(keep).toHaveCount(0)
        await page.locator('#capacity-and-load').getByRole('button', { name: 'Customize work week' }).click()
        const week = page.getByRole('dialog').filter({ has: page.locator('#ed-title') })
        await expect(week).toBeVisible()
        if (process.env.AEON1175_SHOTS) await week.screenshot({ path: info.outputPath(`editor-week-${width}-${theme}.png`) })
        await page.keyboard.press('Escape')
      }
      expect(errors).toEqual([])
    })
  })
}
