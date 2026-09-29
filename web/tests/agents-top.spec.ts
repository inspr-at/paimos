// SPDX-License-Identifier: AGPL-3.0-only
// AEON-299: the top of /agents — the compact live line, Needs you only when
// something waits, and the Accounts block with today's plan, the two simple pacing
// controls, the two gear editors and Sprint/Hold. AEON299_SHOTS=<dir> also writes
// screenshots of every state at 375/390/430/1600 in light and dark.
import { mkdirSync } from 'node:fs'
import { test, expect, type Browser, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { agentData, mockAgents, type AgentWorld } from './agents-fixtures'
import { ACCOUNTS, NOW, TZ, capacityWorld, type CapacityOptions } from './capacity-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'

test.use({ timezoneId: TZ })
const world: AgentWorld = {
  me: me.id, now: NOW,
  projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' },
  tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' },
  nodes: {
    'p-pharos': { key: 'PRJ-17', title: 'Pharos' }, 'p-aeon': { key: 'PRJ-35', title: 'Aeon' }, 'p-frozen': { key: 'PRJ-26', title: 'Studio infrastructure' },
    'n-1': { key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning' }, 'n-2': { key: 'PHAROS-12', title: 'Add an Oracle Cloud connector' },
    'n-a1': { key: 'AEON-1', title: 'Aeon foundation' }, 'n-5': { key: 'PHAROS-15', title: 'Beacon health probes' }, 'n-6': { key: 'PHAROS-16', title: 'Retire the old dashboard' },
  },
}
interface Setup extends CapacityOptions { needs?: boolean; manage?: boolean }
async function setup(page: Page, options: Setup = {}) {
  await page.clock.setSystemTime(NOW)
  await mockWork(page, fixtures(), { admin: true })
  const data = agentData(world)
  const capacity = capacityWorld(options)
  data.accounts = capacity.accounts as unknown as typeof data.accounts
  if (!options.needs) {
    // Nothing waits: every request is decided, no held action request.
    data.approvals = data.approvals.filter(a => a.decision)
    data.messages = data.messages.filter(m => !m.is_action_request)
  }
  const calls = await mockAgents(page, data, { capacity })
  await page.route('**/api/me/permissions*', route => {
    const answer = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    answer.workspace.permissions = [...answer.workspace.permissions, 'account.read', ...(options.manage === false ? [] : ['account.manage']), 'run.create', 'run.read', 'models.read', 'work_orders.read']
    return route.fulfill({ json: answer })
  })
  return { data, capacity, calls }
}
const cap = (page: Page) => page.getByRole('region', { name: 'Accounts' })
const pool = (page: Page, id: string) => page.locator(`.pool[data-pool="${id}"]`)
async function open(page: Page) {
  await page.goto('/agents')
  await expect(page.getByRole('heading', { name: 'Agents', level: 1 })).toBeVisible()
  await expect(pool(page, 'codex').locator('.plan')).toBeVisible()
}
const noScroll = (page: Page) => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)

test('nothing waits: no Needs you, a compact live line, and accounts with one plan per pool', async ({ page }) => {
  const errors = watchErrors(page)
  await setup(page)
  await open(page)
  await expect(page.getByRole('region', { name: 'Needs you' })).toHaveCount(0)
  await expect(page.getByRole('heading', { name: 'Live now' })).toHaveCount(0)
  const line = page.getByRole('group', { name: 'Live sessions' })
  await expect(line).toContainText(/\d+ live/)
  await expect(line.getByRole('button', { name: /working/ })).toBeVisible()
  await expect(cap(page).locator('.cap-meta')).toHaveText('5 of 6 ready')
  await expect(pool(page, 'codex').locator('.pool-plan')).toHaveText('Pro · weekly')
  await expect(pool(page, 'codex').locator('.plan')).toHaveText('Today: ~6% of Spare, then ~15% of Main — soonest reset first, so each lands at 0% as it resets.')
  await expect(pool(page, 'codex').locator('.acct .nm')).toHaveText(['Spare', 'Main', 'Studio'])
  const studio = page.locator(`[data-account="${ACCOUNTS.studio}"]`)
  await expect(studio.locator('.source')).toHaveText('Read on studio · 3 h ago · offline')
  await expect(studio.locator('.today')).toHaveText('waits for studio')
  await expect(page.locator(`[data-account="${ACCOUNTS.claude}"] .win5`)).toHaveText('5-hour window 60% left · resets 16:40')
  await expect(pool(page, 'claude').locator('.plan')).toContainText('Today: use up to ~10% of Claude (4% so far) — on track to finish at 0% by Fri 22:00, before it resets Sun 11:00.')
  await expect(pool(page, 'grok').locator('.plan')).toContainText('(fresh week)')
  await expect(pool(page, 'cursor').locator('.plan')).toHaveText('Ahead of pace: 7% used today, the plan was ~6%. Agents ease off Cursor until tomorrow.')
  await expect(page.locator(`[data-account="${ACCOUNTS.cursor}"] .today`)).toHaveText('7% of ~6% · ahead')
  // The gauge carries today's share and the stop tick; the meter names them.
  await expect(page.locator(`[data-account="${ACCOUNTS.spare}"] [role="meter"]`)).toHaveAttribute('aria-label', "Spare: 9% left, today's share 6%, 2% used today")
  // No coloured edge accents anywhere in the new top (AGENTS.md rule 11).
  const edges = await page.locator('.cap, .cap *, .live-line, .live-line *').evaluateAll(els => els.filter(el => { const s = getComputedStyle(el); return ['Left', 'Top'].some(side => parseFloat(s[`border${side}Width` as 'borderLeftWidth']) >= 3 && s[`border${side}Style` as 'borderLeftStyle'] !== 'none') }).length)
  expect(edges).toBe(0)
  expect(errors).toEqual([])
})

test('Needs you appears only when something waits, with the sign-in and its command', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  await setup(page, { needs: true, signin: true })
  await open(page)
  const needs = page.getByRole('region', { name: 'Needs you' })
  await expect(needs).toBeVisible()
  const signin = needs.getByRole('listitem', { name: 'Cursor needs a new sign-in on mbp2607' })
  await expect(signin).toContainText('Run cursor-agent login there · agents skip this account until then')
  await signin.getByRole('button', { name: 'Copy command' }).click()
  await expect(page.getByText('Copied: cursor-agent login — run it on mbp2607.')).toBeVisible()
  await expect(pool(page, 'cursor').locator('.plan')).toHaveText('Paused until you sign in again on mbp2607. 57% left, resets Wed 14 Oct.')
  const row = page.locator(`[data-account="${ACCOUNTS.cursor}"]`)
  await expect(row.locator('.source')).toHaveText('Sign-in expired · last read 2 d ago')
  await expect(row.getByRole('button', { name: 'Sign in again' })).toBeVisible()
  await expect(cap(page).locator('.cap-meta')).toHaveText('4 of 6 ready')
})

test('work days and nights: presets save the schedule at once', async ({ page }) => {
  const { capacity } = await setup(page)
  await open(page)
  const days = cap(page).getByRole('radiogroup', { name: 'Work days a week' })
  await expect(days.getByRole('radio', { name: '5' })).toHaveAttribute('aria-checked', 'true')
  await days.getByRole('radio', { name: '6' }).click()
  await expect(cap(page).locator('.setting.days .cap-hint')).toHaveText('Mon–Sat')
  await expect.poll(() => capacity.puts.length).toBe(1)
  // One atomic request: the server carries Sprint/Hold entries in the same transaction.
  expect(capacity.puts[0]).toMatchObject({ scope: 'user', carry_overrides: true })
  expect((capacity.puts[0] as { scope: string; schedule: { week: { on: boolean }[] } }).schedule.week.filter(d => d.on)).toHaveLength(6)
  const nights = cap(page).getByRole('switch', { name: 'Agents at night' })
  await expect(nights).toHaveAttribute('aria-checked', 'false')
  await nights.click()
  await expect(nights).toHaveAttribute('aria-checked', 'true')
  await expect(pool(page, 'claude').locator('.plan')).toContainText('by day and')
  expect((capacity.puts.at(-1) as { schedule: { nights: boolean; week: { on: boolean }[] } }).schedule).toMatchObject({ nights: true })
})

test('work week editor: a custom week with its own hours, live preview, Save', async ({ page }) => {
  const { capacity } = await setup(page)
  await open(page)
  await cap(page).getByRole('button', { name: 'Customize work week' }).click()
  const editor = page.getByRole('dialog', { name: 'Work week' })
  await expect(editor).toBeVisible()
  await expect(editor.getByRole('heading', { name: 'Work week' })).toBeFocused()
  await editor.getByRole('switch', { name: 'Monday' }).click()
  await editor.getByRole('switch', { name: 'Friday' }).click()
  await editor.getByLabel('Same hours every day').uncheck()
  await editor.getByLabel('Thursday until').selectOption('14')
  await expect(editor.locator('.sec-t').first()).toContainText('3 days · Tue–Thu')
  // The preview asks the server to pace the draft and marks what changes.
  await expect.poll(() => capacity.previews.length).toBeGreaterThan(0)
  await expect(editor.locator('.pv li.changed').first()).toBeVisible()
  await editor.getByRole('radio', { name: 'Rest' }).click()
  await expect(editor).toContainText('Capacity that resets before your next work day is lost.')
  await editor.getByRole('button', { name: 'Save' }).click()
  await expect(editor).toHaveCount(0)
  await expect(cap(page).getByRole('radio', { name: 'Custom · 3 days' })).toHaveAttribute('aria-checked', 'true')
  await expect(cap(page).locator('.setting.days .cap-hint')).toHaveText('Tue–Thu · own hours')
  const saved = (capacity.puts.at(-1) as { schedule: { off_days: string; week: { on: boolean; end: number }[] } }).schedule
  expect(saved.off_days).toBe('rest')
  expect(saved.week[3]).toMatchObject({ on: true, end: 14 })
  // Reset to default and Escape: nothing saved.
  await cap(page).getByRole('button', { name: 'Customize work week' }).click()
  await editor.getByRole('button', { name: 'Reset to default' }).click()
  await expect(editor.locator('.sec-t').first()).toContainText('5 days · Mon–Fri')
  await page.keyboard.press('Escape')
  await expect(editor).toHaveCount(0)
  await expect(cap(page).getByRole('button', { name: 'Customize work week' })).toBeFocused()
  await expect(cap(page).locator('.setting.days .cap-hint')).toHaveText('Tue–Thu · own hours')
})

test('night editor: day and night, three shifts with adjustable pace, custom blocks', async ({ page }) => {
  const { capacity } = await setup(page)
  await open(page)
  await cap(page).getByRole('button', { name: 'Customize night and shifts' }).click()
  const editor = page.getByRole('dialog', { name: 'Agents outside your hours' })
  await editor.getByRole('radio', { name: /Three shifts/ }).check()
  await editor.getByRole('slider', { name: 'Night shift pace' }).fill('40')
  await expect(editor.locator('.rb-legend')).toContainText('Night 22–06 · 40%')
  await editor.getByRole('radio', { name: /Custom blocks/ }).check()
  await editor.getByRole('radio', { name: 'Reduced' }).click()
  await editor.getByRole('slider', { name: 'Reduced pace' }).fill('30')
  const hour = editor.getByRole('button', { name: /^03:00 to 04:00/ })
  await hour.click()
  await expect(hour).toHaveAccessibleName('03:00 to 04:00: reduced, 30%')
  await hour.press('o')
  await expect(hour).toHaveAccessibleName('03:00 to 04:00: off')
  await editor.getByRole('button', { name: 'Save' }).click()
  await expect(cap(page).locator('.setting.nights .cap-hint')).toHaveText('Custom')
  await expect(cap(page).getByRole('switch', { name: 'Agents at night' })).toHaveAttribute('aria-checked', 'true')
  const saved = (capacity.puts.at(-1) as { schedule: { model: string; nights: boolean; blocks: number[]; shifts: { k: number[] } } }).schedule
  expect(saved).toMatchObject({ model: 'blocks', nights: true })
  expect(saved.blocks[3]).toBe(0)
  expect(saved.shifts.k[2]).toBe(0.4)
  // Day and night with its own hours.
  await cap(page).getByRole('button', { name: 'Customize night and shifts' }).click()
  await editor.getByRole('radio', { name: /Day and night/ }).check()
  await editor.getByLabel('Night starts').selectOption('18')
  await editor.getByLabel('Night ends').selectOption('6')
  await editor.getByRole('button', { name: 'Save' }).click()
  await expect(cap(page).locator('.setting.nights .cap-hint')).toHaveText('18–06')
})

test('Sprint and Hold from the pool menu, and back to the plan', async ({ page }) => {
  const { capacity } = await setup(page)
  await open(page)
  await pool(page, 'codex').getByRole('button', { name: 'Codex: sprint or hold' }).click()
  const menu = page.getByRole('menu', { name: 'Codex: sprint or hold' })
  await expect(menu.getByRole('menuitem', { name: /Sprint until reset/ })).toContainText('Agents may use everything left until tomorrow 18:02.')
  await menu.getByRole('menuitem', { name: /Sprint until reset/ }).click()
  await expect(pool(page, 'codex').locator('.override')).toContainText('Sprint')
  await expect(pool(page, 'codex').locator('.plan')).toHaveText('Sprint: agents may use everything left on Spare (9%) and Main (42%) until tomorrow 18:02.')
  await expect(page.locator(`[data-account="${ACCOUNTS.spare}"] .today`)).toHaveText('all 9%')
  expect(capacity.puts.at(-1)).toMatchObject({ scope: 'pool', pool: 'codex', schedule: { override: 'sprint' } })
  await pool(page, 'codex').getByRole('button', { name: 'Back to the plan' }).click()
  await expect(pool(page, 'codex').locator('.override')).toHaveCount(0)
  expect(capacity.puts.at(-1)).toEqual({ scope: 'pool', pool: 'codex', schedule: null })
  await pool(page, 'grok').getByRole('button', { name: 'Grok: sprint or hold' }).click()
  await page.getByRole('menu').getByRole('menuitem', { name: /Hold/ }).click()
  await expect(pool(page, 'grok').locator('.plan')).toHaveText("On hold. Agents leave Grok alone until you resume; today's share moves to the coming days.")
  await expect(page.locator(`[data-account="${ACCOUNTS.grok}"] .today`)).toHaveText('on hold')
  // A schedule change carries the Hold along instead of leaving a stale copy.
  await cap(page).getByRole('radiogroup', { name: 'Work days a week' }).getByRole('radio', { name: '7' }).click()
  await expect.poll(() => capacity.schedules.find(e => e.pool === 'grok')?.schedule.week.filter(d => d.on).length).toBe(7)
  expect(capacity.schedules.find(e => e.pool === 'grok')?.schedule.override).toBe('hold')
})

// AEON-299 review 1: a save that fails says so, keeps the editor open, and the
// page shows what the server still has; nothing reads "Saved".
test('a failed schedule save keeps the editor open and changes nothing', async ({ page }) => {
  const { capacity } = await setup(page)
  await open(page)
  await pool(page, 'grok').getByRole('button', { name: 'Grok: sprint or hold' }).click()
  await page.getByRole('menu').getByRole('menuitem', { name: /Hold/ }).click()
  await expect(pool(page, 'grok').locator('.override')).toBeVisible()
  let failures = 0
  await page.route('**/api/agent-accounts/capacity/schedule', route => {
    if (route.request().method() === 'PUT' && failures++ === 0) return route.fulfill({ status: 500, json: { error: 'database unavailable' } })
    return route.fallback()
  })
  await cap(page).getByRole('button', { name: 'Customize work week' }).click()
  const editor = page.getByRole('dialog', { name: 'Work week' })
  await editor.getByRole('switch', { name: 'Saturday' }).click()
  await editor.getByRole('switch', { name: 'Sunday' }).click()
  await editor.getByRole('button', { name: 'Save' }).click()
  await expect(page.getByText(/Nothing was saved: database unavailable/)).toBeVisible()
  await expect(page.getByText("Saved. Today's plan follows the new schedule.")).toHaveCount(0)
  await expect(editor).toBeVisible()
  await expect(cap(page).locator('.setting.days .cap-hint')).toHaveText('Mon–Fri')
  expect(capacity.schedules.find(e => e.pool === 'grok')?.schedule.week.filter(d => d.on)).toHaveLength(5)
  // Trying again saves the week and the Hold follows it, in one request.
  await editor.getByRole('button', { name: 'Save' }).click()
  await expect(editor).toHaveCount(0)
  await expect(cap(page).locator('.setting.days .cap-hint')).toHaveText('every day')
  expect(capacity.schedules.find(e => e.pool === 'grok')?.schedule).toMatchObject({ override: 'hold' })
  expect(capacity.schedules.find(e => e.pool === 'grok')?.schedule.week.filter(d => d.on)).toHaveLength(7)
})

// AEON-299 review 2: a computer-wide login flag or a check that could not run
// is not a sign-in prompt; only a confirmed sign-out for that account is.
test('sign-in prompts only for a confirmed sign-out of that account', async ({ page }) => {
  await setup(page, { computerLogin: true, unavailable: true })
  await open(page)
  await expect(page.getByRole('region', { name: 'Needs you' })).toHaveCount(0)
  await expect(page.locator('.cap .dot.signin')).toHaveCount(0)
  const grok = page.locator(`[data-account="${ACCOUNTS.grok}"]`)
  await expect(grok.locator('.today')).toHaveText('reading unavailable')
  await expect(grok.locator('.source')).toHaveText('Read on mbp2607 · 12 min ago')
  await expect(pool(page, 'grok').locator('.plan')).toHaveText('Reading unavailable on mbp2607. Agents skip it until the next check succeeds.')
  await expect(page.locator(`[data-account="${ACCOUNTS.spare}"] .dot`)).toHaveClass(/live/)
})

// AEON-299 review 4: Sprint promises the server's end, the 5-hour reset included.
test('Sprint promises the limiting reset, including the 5-hour window', async ({ page }) => {
  const { capacity } = await setup(page)
  await open(page)
  await pool(page, 'claude').getByRole('button', { name: 'Claude: sprint or hold' }).click()
  const sprint = page.getByRole('menu').getByRole('menuitem', { name: /Sprint until reset/ })
  await expect(sprint).toContainText('Agents may use everything left until 16:40.')
  await sprint.click()
  await expect(pool(page, 'claude').locator('.plan')).toContainText('until 16:40.')
  expect(capacity.schedules.find(e => e.pool === 'claude')?.schedule.override_until).toBe(new Date(Date.parse('2026-09-29T14:40:00Z')).toISOString())
})

test('stale readings, % used per account and globally, and Manage accounts', async ({ page }) => {
  await setup(page, { stale: true })
  await open(page)
  const grok = page.locator(`[data-account="${ACCOUNTS.grok}"]`)
  await expect(grok.locator('.source')).toHaveText('Read on mbp2607 · 7 h ago · stale')
  await expect(grok.locator('.gauge')).toHaveClass(/frozen/)
  const spare = page.locator(`[data-account="${ACCOUNTS.spare}"]`)
  await spare.getByRole('button', { name: /Spare: 9% left/ }).click()
  await expect(spare.locator('.left')).toHaveText('91%used')
  await expect(page.locator(`[data-account="${ACCOUNTS.main}"] .left`)).toHaveText('42%left')
  await cap(page).getByRole('radio', { name: '% used' }).click()
  await expect(page.locator(`[data-account="${ACCOUNTS.main}"] .left`)).toHaveText('58%used')
  await expect(page.locator(`[data-account="${ACCOUNTS.claude}"] .win5`)).toContainText('40% used')
  await cap(page).getByRole('link', { name: /Manage/ }).click()
  await expect(page).toHaveURL('/settings/accounts')
  await expect(page.getByRole('region', { name: 'Accounts and pacing' })).toContainText('Spare')
})

test('without account.manage the pacing controls stay visible but inert', async ({ page }) => {
  await setup(page, { manage: false })
  await open(page)
  await expect(cap(page).getByRole('radio', { name: '6' })).toBeDisabled()
  await expect(cap(page).getByRole('switch', { name: 'Agents at night' })).toBeDisabled()
  await expect(cap(page).getByRole('button', { name: 'Customize work week' })).toBeDisabled()
  await expect(pool(page, 'codex').getByRole('button', { name: /sprint or hold/ })).toHaveCount(0)
})

test.describe('phone', () => {
  test.use({ viewport: { width: 390, height: 844 } })
  test('stacked rows, no sideways scroll, editors as bottom sheets', async ({ page }) => {
    await setup(page, { needs: true, signin: true })
    await open(page)
    expect(await noScroll(page)).toBe(true)
    await cap(page).getByRole('button', { name: 'Customize work week' }).click()
    const sheet = page.getByRole('dialog', { name: 'Work week' })
    await expect(sheet).toHaveAttribute('aria-modal', 'true')
    const box = (await sheet.boundingBox())!
    expect(Math.round(box.x)).toBe(0)
    expect(Math.round(box.width)).toBe(390)
    await sheet.getByRole('button', { name: 'Cancel' }).click()
    await expect(sheet).toHaveCount(0)
    await cap(page).getByRole('button', { name: 'Customize night and shifts' }).click()
    await expect(page.getByRole('dialog', { name: 'Agents outside your hours' })).toBeVisible()
    await page.locator('.scrim').click({ position: { x: 20, y: 20 } })
    await expect(page.getByRole('dialog')).toHaveCount(0)
    expect(await noScroll(page)).toBe(true)
    // AEON-299 review 5: with a toast in the background, focus never leaves the
    // sheet, from the heading or anywhere else, and the page behind is inert.
    await pool(page, 'codex').getByRole('button', { name: 'Codex: sprint or hold' }).click()
    await page.getByRole('menuitem', { name: /Hold/ }).click()
    await expect(page.locator('.toast').first()).toBeVisible()
    await cap(page).getByRole('button', { name: 'Customize work week' }).click()
    const trap = page.getByRole('dialog', { name: 'Work week' })
    await expect(trap.getByRole('heading', { name: 'Work week' })).toBeFocused()
    await expect(page.locator('#app')).toHaveAttribute('inert', '')
    const inside = () => page.evaluate(() => !!document.activeElement?.closest('[role="dialog"]'))
    await page.keyboard.press('Shift+Tab')
    expect(await inside()).toBe(true)
    for (let i = 0; i < 40; i++) { await page.keyboard.press('Tab'); expect(await inside()).toBe(true) }
    for (let i = 0; i < 40; i++) { await page.keyboard.press('Shift+Tab'); expect(await inside()).toBe(true) }
    await page.keyboard.press('Escape')
    await expect(trap).toHaveCount(0)
    await expect(page.locator('#app')).not.toHaveAttribute('inert', '')
    // The pool menu opens under its button, in view, with focus on the first choice.
    await pool(page, 'claude').getByRole('button', { name: 'Claude: sprint or hold' }).click()
    const menu = page.getByRole('menu')
    await expect(menu).toBeInViewport()
    await expect(menu.getByRole('menuitem').first()).toBeFocused()
  })
})

// ---------------------------------------------------------------- screenshots
const SHOTS = process.env.AEON299_SHOTS
type Shot = { name: string; options?: Setup; act?: (page: Page) => Promise<void> }
const SHOT_STATES: Shot[] = [
  { name: 'quiet' },
  { name: 'needs-signin', options: { needs: true, signin: true } },
  { name: 'stale', options: { stale: true } },
  { name: 'unavailable', options: { unavailable: true, computerLogin: true } },
  { name: 'nights-on', act: async page => { await cap(page).getByRole('switch', { name: 'Agents at night' }).click(); await expect(pool(page, 'claude').locator('.plan')).toContainText('by day and') } },
  { name: 'sprint-hold', act: async page => {
    await pool(page, 'codex').getByRole('button', { name: 'Codex: sprint or hold' }).click(); await page.getByRole('menuitem', { name: /Sprint/ }).click()
    await expect(pool(page, 'codex').locator('.override')).toBeVisible()
    await pool(page, 'grok').getByRole('button', { name: 'Grok: sprint or hold' }).click(); await page.getByRole('menuitem', { name: /Hold/ }).click()
    await expect(pool(page, 'grok').locator('.override')).toBeVisible()
  } },
  { name: 'menu', act: async page => { await pool(page, 'claude').getByRole('button', { name: 'Claude: sprint or hold' }).click(); await expect(page.getByRole('menu')).toBeVisible() } },
  { name: 'week-editor', act: async page => {
    await cap(page).getByRole('button', { name: 'Customize work week' }).click()
    const ed = page.getByRole('dialog', { name: 'Work week' })
    await ed.getByRole('switch', { name: 'Monday' }).click(); await ed.getByRole('switch', { name: 'Friday' }).click()
    await ed.getByLabel('Same hours every day').uncheck(); await ed.getByLabel('Thursday until').selectOption('14')
    await expect(ed.locator('.pv li.changed').first()).toBeVisible()
  } },
  { name: 'night-shifts', act: async page => {
    await cap(page).getByRole('button', { name: 'Customize night and shifts' }).click()
    const ed = page.getByRole('dialog', { name: 'Agents outside your hours' })
    await ed.getByRole('radio', { name: /Three shifts/ }).check()
    await ed.getByRole('slider', { name: 'Late shift pace' }).fill('80'); await ed.getByRole('slider', { name: 'Night shift pace' }).fill('40')
    await expect(ed.locator('.pv li.changed').first()).toBeVisible()
  } },
  { name: 'night-blocks', act: async page => {
    await cap(page).getByRole('button', { name: 'Customize night and shifts' }).click()
    const ed = page.getByRole('dialog', { name: 'Agents outside your hours' })
    await ed.getByRole('radio', { name: /Custom blocks/ }).check()
    await ed.getByRole('radio', { name: 'Reduced' }).click()
    for (const h of [0, 1, 2]) await ed.getByRole('button', { name: new RegExp(`^0${h}:00 to`) }).click()
    await ed.getByRole('radio', { name: 'Off' }).click()
    await ed.getByRole('button', { name: /^06:00 to/ }).click(); await ed.getByRole('button', { name: /^12:00 to/ }).click()
  } },
  { name: 'custom-closed', act: async page => {
    await cap(page).getByRole('button', { name: 'Customize work week' }).click()
    const ed = page.getByRole('dialog', { name: 'Work week' })
    await ed.getByRole('switch', { name: 'Monday' }).click(); await ed.getByRole('switch', { name: 'Friday' }).click()
    await ed.getByRole('button', { name: 'Save' }).click(); await expect(ed).toHaveCount(0)
    await cap(page).getByRole('switch', { name: 'Agents at night' }).click()
  } },
]
async function shoot(browser: Browser, shot: Shot, width: number, theme: 'light' | 'dark') {
  // The app scrolls inside its own frame, so page states use a tall viewport; overlays keep a real one.
  const overlay = /editor|shifts|blocks|menu/.test(shot.name)
  const height = width < 800 ? (overlay ? 844 : 2600) : (overlay ? 1100 : 1300)
  const context = await browser.newContext({ viewport: { width, height }, colorScheme: theme, reducedMotion: 'reduce', timezoneId: TZ })
  const page = await context.newPage()
  try {
    await setup(page, shot.options)
    await open(page)
    await shot.act?.(page)
    await page.waitForTimeout(350)
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1)
    await page.screenshot({ path: `${SHOTS}/${width}-${theme}-${shot.name}${overflow ? '-OVERFLOW' : ''}.png` })
  } finally { await context.close() }
}
test.describe('screenshots', () => {
  test.skip(!SHOTS, 'set AEON299_SHOTS=<dir> to capture')
  test.setTimeout(600_000)
  for (const shot of SHOT_STATES) test(`shot ${shot.name}`, async ({ browser }) => {
    mkdirSync(SHOTS!, { recursive: true })
    const widths = (process.env.AEON299_WIDTHS ?? '375,390,430,1600').split(',').map(Number)
    for (const theme of ['light', 'dark'] as const) for (const width of widths) await shoot(browser, shot, width, theme)
  })
})
