// SPDX-License-Identifier: AGPL-3.0-only
// AEON-299, reworked for AEON-499: the top of /agents — the compact live line,
// Needs you only when something waits, and Accounts and computers: one card per
// computer with one readiness state per account, the pacing summary with its
// settings and editors, and Sprint/Hold in the account row menu.
// AEON299_SHOTS=<dir> also writes screenshots of every state at 375/390/430/1600
// in light and dark.
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
const cap = (page: Page) => page.getByRole('region', { name: 'Accounts and computers' })
const row = (page: Page, id: string) => page.locator(`[data-account="${id}"]`)
const summary = (page: Page) => cap(page).getByRole('button', { name: /days · keep/ })
const pacingDialog = (page: Page) => page.getByRole('dialog', { name: 'Pacing' })
/** The pacing settings live in a popover under the summary button. */
async function pacing(page: Page) {
  if (!(await pacingDialog(page).count())) await summary(page).click()
  await expect(pacingDialog(page)).toBeVisible()
  return pacingDialog(page)
}
async function rowMenu(page: Page, id: string) {
  await row(page, id).getByRole('button', { name: /^More for / }).click()
  const menu = page.getByRole('menu')
  await expect(menu).toBeVisible()
  return menu
}
async function open(page: Page) {
  await page.goto('/agents')
  await expect(page.getByRole('heading', { name: 'Agents', level: 1 })).toBeVisible()
  await expect(row(page, ACCOUNTS.main).locator('[role="meter"]')).toBeVisible()
}
const noScroll = (page: Page) => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)

test('nothing waits: no Needs you, a compact live line, and one card per computer', async ({ page }) => {
  const errors = watchErrors(page)
  await setup(page)
  await open(page)
  await expect(page.getByRole('region', { name: 'Needs you' })).toHaveCount(0)
  await expect(page.getByRole('heading', { name: 'Live now' })).toHaveCount(0)
  const line = page.getByRole('group', { name: 'Live sessions' })
  await expect(line).toContainText(/\d+ live/)
  await expect(line.getByRole('button', { name: /working/ })).toBeVisible()
  await expect(cap(page).getByText('5 of 6 ready · studio offline')).toBeVisible()
  await expect(cap(page).getByRole('region', { name: 'Computer mbp2607' }).getByRole('row')).toHaveCount(5)
  await expect(row(page, ACCOUNTS.studio)).toContainText('Paused · computer offline')
  await expect(row(page, ACCOUNTS.studio)).toContainText('Last reading 3 h ago')
  await expect(row(page, ACCOUNTS.claude)).toContainText('5-hour window: 60% left')
  // Keep for you (AEON-375, Auto 30%): Spare's last 9% is kept for you.
  await expect(row(page, ACCOUNTS.spare)).toContainText('Kept for you while you work')
  await expect(row(page, ACCOUNTS.spare).locator('[role="meter"]')).toHaveAttribute('aria-label', /^Spare: 9% left this week, resets tomorrow 18:02/)
  await expect(row(page, ACCOUNTS.main).locator('[role="meter"]')).toHaveAttribute('aria-label', /^Main: 42% left this week, resets Fri 09:14; today's share 15%/)
  await expect(row(page, ACCOUNTS.cursor)).toContainText('Estimated')
  await expect(summary(page)).toHaveText('5 days · keep auto · no nights')
  await expect((await pacing(page)).getByRole('button', { name: 'Keep for you Auto · ~30%' })).toBeVisible()
  await expect(cap(page).getByRole('region', { name: 'The plan' })).toHaveCount(0)
  await expect(cap(page).getByRole('region', { name: 'Computer mbp2607' })).toContainText('kept for you')
  // No coloured edge accents anywhere in the new top (AGENTS.md rule 11).
  const edges = await page.locator('.ac, .ac *, .live-line, .live-line *').evaluateAll(els => els.filter(el => { const s = getComputedStyle(el); return ['Left', 'Top'].some(side => parseFloat(s[`border${side}Width` as 'borderLeftWidth']) >= 3 && s[`border${side}Style` as 'borderLeftStyle'] !== 'none') }).length)
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
  const cursor = row(page, ACCOUNTS.cursor)
  await expect(cursor).toContainText('Signed out')
  await expect(cursor.getByRole('button', { name: 'cursor-agent login' })).toBeVisible()
  await expect(cap(page).getByText('4 of 6 ready · studio offline')).toBeVisible()
})

test('work days and nights: presets save the schedule at once', async ({ page }) => {
  const { capacity } = await setup(page)
  await open(page)
  const days = (await pacing(page)).getByRole('radiogroup', { name: 'Work days a week' })
  await expect(days.getByRole('radio', { name: '5' })).toHaveAttribute('aria-checked', 'true')
  await days.getByRole('radio', { name: '6' }).click()
  await expect(pacingDialog(page).locator('.setting.days .hint')).toHaveText('Mon–Sat')
  await expect(summary(page)).toHaveText('6 days · keep auto · no nights')
  await expect.poll(() => capacity.puts.length).toBe(1)
  // One atomic request: the server carries Sprint/Hold entries in the same transaction.
  expect(capacity.puts[0]).toMatchObject({ scope: 'user', carry_overrides: true })
  expect((capacity.puts[0] as { scope: string; schedule: { week: { on: boolean }[] } }).schedule.week.filter(d => d.on)).toHaveLength(6)
  const nights = pacingDialog(page).getByRole('switch', { name: 'Agents at night' })
  await expect(nights).toHaveAttribute('aria-checked', 'false')
  await nights.click()
  await expect(nights).toHaveAttribute('aria-checked', 'true')
  await expect(summary(page)).toHaveText('6 days · keep auto · nights 22–08')
  expect((capacity.puts.at(-1) as { schedule: { nights: boolean; week: { on: boolean }[] } }).schedule).toMatchObject({ nights: true })
})

test('work week editor: a custom week with its own hours, live preview, Save', async ({ page }) => {
  const { capacity } = await setup(page)
  await open(page)
  await (await pacing(page)).getByRole('button', { name: 'Customize work week' }).click()
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
  await expect(summary(page)).toHaveText('3 days · keep auto · no nights')
  await expect((await pacing(page)).getByRole('radio', { name: 'Custom · 3 days' })).toHaveAttribute('aria-checked', 'true')
  await expect(pacingDialog(page).locator('.setting.days .hint')).toHaveText('Tue–Thu · own hours')
  const saved = (capacity.puts.at(-1) as { schedule: { off_days: string; week: { on: boolean; end: number }[] } }).schedule
  expect(saved.off_days).toBe('rest')
  expect(saved.week[3]).toMatchObject({ on: true, end: 14 })
  // Reset to default and Escape: nothing saved, focus back on the summary.
  await pacingDialog(page).getByRole('button', { name: 'Customize work week' }).click()
  await editor.getByRole('button', { name: 'Reset to default' }).click()
  await expect(editor.locator('.sec-t').first()).toContainText('5 days · Mon–Fri')
  await page.keyboard.press('Escape')
  await expect(editor).toHaveCount(0)
  await expect(summary(page)).toBeFocused()
  await expect(summary(page)).toHaveText('3 days · keep auto · no nights')
})

test('night editor: day and night, three shifts with adjustable pace, custom blocks', async ({ page }) => {
  const { capacity } = await setup(page)
  await open(page)
  await (await pacing(page)).getByRole('button', { name: 'Customize night and shifts' }).click()
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
  await expect(summary(page)).toHaveText('5 days · keep auto · nights custom')
  await expect((await pacing(page)).getByRole('switch', { name: 'Agents at night' })).toHaveAttribute('aria-checked', 'true')
  const saved = (capacity.puts.at(-1) as { schedule: { model: string; nights: boolean; blocks: number[]; shifts: { k: number[] } } }).schedule
  expect(saved).toMatchObject({ model: 'blocks', nights: true })
  expect(saved.blocks[3]).toBe(0)
  expect(saved.shifts.k[2]).toBe(0.4)
  // Day and night with its own hours.
  await pacingDialog(page).getByRole('button', { name: 'Customize night and shifts' }).click()
  await editor.getByRole('radio', { name: /Day and night/ }).check()
  await editor.getByLabel('Night starts').selectOption('18')
  await editor.getByLabel('Night ends').selectOption('6')
  await editor.getByRole('button', { name: 'Save' }).click()
  await expect(summary(page)).toHaveText('5 days · keep auto · nights 18–06')
})

test('Sprint and Hold from the row menu, and back to the plan', async ({ page }) => {
  const { capacity } = await setup(page)
  await open(page)
  const menu = await rowMenu(page, ACCOUNTS.spare)
  await expect(menu.getByRole('menuitem', { name: /Sprint Codex until reset/ })).toContainText('Agents may use everything left until tomorrow 18:02.')
  await menu.getByRole('menuitem', { name: /Sprint Codex until reset/ }).click()
  await expect(row(page, ACCOUNTS.spare)).toContainText('Sprint: all of it until tomorrow 18:02')
  await expect(row(page, ACCOUNTS.main)).toContainText('Sprint: all of it until tomorrow 18:02')
  expect(capacity.puts.at(-1)).toMatchObject({ scope: 'pool', pool: 'codex', schedule: { override: 'sprint' } })
  await (await rowMenu(page, ACCOUNTS.main)).getByRole('menuitem', { name: /Back to the plan/ }).click()
  await expect(row(page, ACCOUNTS.spare)).not.toContainText('Sprint')
  expect(capacity.puts.at(-1)).toEqual({ scope: 'pool', pool: 'codex', schedule: null })
  await (await rowMenu(page, ACCOUNTS.grok)).getByRole('menuitem', { name: 'Hold until I resume' }).click()
  await expect(row(page, ACCOUNTS.grok)).toContainText('On hold')
  // A schedule change carries the Hold along instead of leaving a stale copy.
  await (await pacing(page)).getByRole('radiogroup', { name: 'Work days a week' }).getByRole('radio', { name: '7' }).click()
  await expect.poll(() => capacity.schedules.find(e => e.pool === 'grok')?.schedule.week.filter(d => d.on).length).toBe(7)
  expect(capacity.schedules.find(e => e.pool === 'grok')?.schedule.override).toBe('hold')
})

// AEON-375: Keep for you, the runway reserve. One choice in the pacing popover,
// Auto by default, keyboard-complete, with the server's live preview.
test('Keep for you: the popover is keyboard-complete, previews live and saves one choice', async ({ page }) => {
  const { capacity } = await setup(page)
  await open(page)
  const button = (await pacing(page)).getByRole('button', { name: 'Keep for you Auto · ~30%' })
  await button.focus()
  await page.keyboard.press('Enter')
  const ed = page.getByRole('dialog', { name: 'Keep for you' })
  await expect(ed.getByRole('heading', { name: 'Keep for you' })).toBeFocused()
  await expect(ed.getByRole('radio', { name: /Auto/ })).toBeChecked()
  // Arrows move between the radios; the stepper takes ↑/↓ and selects the share.
  await ed.getByRole('radio', { name: /Auto/ }).focus()
  await page.keyboard.press('ArrowDown')
  await expect(ed.getByRole('radio', { name: /A fixed share/ })).toBeChecked()
  const share = ed.getByRole('spinbutton', { name: 'Fixed share' })
  await share.focus()
  await page.keyboard.press('ArrowUp')
  await page.keyboard.press('ArrowUp')
  await expect(share).toHaveAttribute('aria-valuenow', '40')
  await page.keyboard.press('End')
  await expect(share).toHaveAttribute('aria-valuenow', '80')
  await expect(ed.getByRole('button', { name: 'Keep 5% more' })).toBeDisabled()
  await page.keyboard.press('Home')
  await page.keyboard.press('ArrowUp')
  await expect(share).toHaveAttribute('aria-valuenow', '15')
  // The preview asks the server to pace the draft, reserve included.
  await expect.poll(() => (capacity.previews.at(-1) as { schedule?: { reserve?: string; reserve_percent?: number } } | undefined)?.schedule).toMatchObject({ reserve: 'fixed', reserve_percent: 15 })
  await expect(ed.locator('.pv li.changed').first()).toBeVisible()
  // Per vendor writes a pool's own choice; the preview carries it.
  await ed.getByRole('button', { name: 'Per vendor' }).click()
  await ed.getByLabel('Codex: keep for you').selectOption('off')
  await expect.poll(() => (capacity.previews.at(-1) as { pool_reserves?: { pool: string; reserve: string }[] }).pool_reserves?.find(r => r.pool === 'codex')?.reserve).toBe('off')
  await expect(ed.locator('.pv li').first()).toContainText('Today: ~6% of Spare, then ~15% of Main')
  await ed.getByRole('button', { name: 'Save' }).click()
  await expect(ed).toHaveCount(0)
  await expect(page.getByText('Saved. Agents leave you room while you work.')).toBeVisible()
  expect(capacity.puts.find(p => (p as { scope: string }).scope === 'user')).toMatchObject({ scope: 'user', carry_overrides: true, schedule: { reserve: 'fixed', reserve_percent: 15 } })
  expect(capacity.puts.at(-1)).toMatchObject({ scope: 'pool', pool: 'codex', schedule: { reserve: 'off' } })
  await expect(summary(page)).toHaveText('5 days · keep 15% · no nights')
  await expect((await pacing(page)).getByRole('button', { name: 'Keep for you 15%' })).toBeVisible()
  // Esc closes without saving and returns focus to the summary.
  await pacingDialog(page).getByRole('button', { name: /^Keep for you/ }).click()
  await ed.getByRole('radio', { name: /Nothing/ }).check()
  await page.keyboard.press('Escape')
  await expect(ed).toHaveCount(0)
  await expect(summary(page)).toBeFocused()
  await expect(summary(page)).toHaveText('5 days · keep 15% · no nights')
  // Reset to default: Auto, every pool follows it again.
  await (await pacing(page)).getByRole('button', { name: /^Keep for you/ }).click()
  await ed.getByRole('button', { name: 'Reset to default' }).click()
  await expect(ed.getByRole('radio', { name: /Auto/ })).toBeChecked()
  await ed.getByRole('button', { name: 'Save' }).click()
  await expect(ed).toHaveCount(0)
  expect(capacity.puts.at(-1)).toEqual({ scope: 'pool', pool: 'codex', schedule: null })
  await expect(summary(page)).toHaveText('5 days · keep auto · no nights')
})

test('Keep for you: a numeric per-vendor share previews and saves that share', async ({ page }) => {
  const { capacity } = await setup(page)
  await open(page)
  await (await pacing(page)).getByRole('button', { name: /^Keep for you/ }).click()
  const ed = page.getByRole('dialog', { name: 'Keep for you' })
  await ed.getByRole('button', { name: 'Per vendor' }).click()
  await ed.getByLabel('Codex: keep for you').selectOption('40')
  await expect(ed.getByLabel('Codex: keep for you')).toHaveValue('40')
  const codexReserve = () => (capacity.previews.at(-1) as { pool_reserves?: { pool: string; reserve: string; reserve_percent?: number }[] } | undefined)?.pool_reserves?.find(r => r.pool === 'codex')
  await expect.poll(codexReserve).toEqual({ pool: 'codex', reserve: 'fixed', reserve_percent: 40 })
  await expect(ed.getByText('The preview could not be updated')).toHaveCount(0)
  await expect(ed.locator('.pv li').first()).toContainText('keeping ~40% for you')
  await ed.getByRole('button', { name: 'Save' }).click()
  await expect(ed).toHaveCount(0)
  await expect(page.getByText('Saved. Agents leave you room while you work.')).toBeVisible()
  const saved = capacity.puts.find(p => (p as { pool?: string }).pool === 'codex') as { scope: string; pool: string; schedule: { reserve?: string; reserve_percent?: number; percent?: number } }
  expect(saved).toMatchObject({ scope: 'pool', pool: 'codex', schedule: { reserve: 'fixed', reserve_percent: 40 } })
  expect(saved.schedule).not.toHaveProperty('percent')
})

test("Keep for you: I'm away until… is one save and one header chip", async ({ page }) => {
  const { capacity } = await setup(page)
  await open(page)
  await (await pacing(page)).getByRole('button', { name: /^Keep for you/ }).click()
  const ed = page.getByRole('dialog', { name: 'Keep for you' })
  await ed.getByRole('button', { name: 'Mon 5 Oct 08:00' }).click()
  await expect(ed.getByRole('button', { name: 'Mon 5 Oct 08:00' })).toHaveAttribute('aria-pressed', 'true')
  await expect(ed).toContainText('Agents use everything left until Mon 5 Oct, across resets.')
  await expect.poll(() => (capacity.previews.at(-1) as { schedule?: { override?: string } } | undefined)?.schedule?.override).toBe('away')
  await ed.getByRole('button', { name: 'Save' }).click()
  await expect(ed).toHaveCount(0)
  expect(capacity.puts.at(-1)).toMatchObject({ scope: 'user', schedule: { override: 'away', override_until: new Date(Date.parse('2026-10-05T06:00:00Z')).toISOString(), reserve: 'auto' } })
  await expect(cap(page).locator('.away-chip')).toContainText('Away until Mon 5 Oct')
  await expect(row(page, ACCOUNTS.claude)).toContainText("Away: all of it until you're back")
  // A work-week change keeps Away; the chip ends it.
  await (await pacing(page)).getByRole('radiogroup', { name: 'Work days a week' }).getByRole('radio', { name: '6' }).click()
  await expect.poll(() => (capacity.puts.at(-1) as { schedule: { override?: string } }).schedule.override).toBe('away')
  await cap(page).getByRole('button', { name: 'Back now: end Away' }).click()
  await expect(cap(page).locator('.away-chip')).toHaveCount(0)
  expect((capacity.puts.at(-1) as { schedule: { override?: string } }).schedule.override).toBeUndefined()
})

test('the one-time plan card: Looks right saves Auto; Change opens the pacing settings', async ({ page }) => {
  const { capacity } = await setup(page, { planCard: 'first' })
  await open(page)
  const card = cap(page).getByRole('region', { name: 'The plan' })
  await expect(card).toHaveText(/Here's the plan\. Agents work alongside you Mon–Fri, 08:00–22:00, pace each account to its reset, and leave you about 30% of every limit while you work\./)
  await card.getByRole('button', { name: 'Change' }).click()
  await expect(card).toHaveCount(0)
  await expect(pacingDialog(page).getByRole('radio', { name: '5' })).toBeFocused()
  expect(capacity.puts).toHaveLength(0)
  await page.reload()
  await expect(card).toBeVisible()
  await card.getByRole('button', { name: 'Looks right' }).click()
  await expect(card).toHaveCount(0)
  expect(capacity.puts.at(-1)).toMatchObject({ scope: 'user', schedule: { reserve: 'auto' } })
})

test('the plan card on a release 11 schedule offers Turn off', async ({ page }) => {
  const { capacity } = await setup(page, { planCard: 'new' })
  await open(page)
  const card = cap(page).getByRole('region', { name: 'The plan' })
  await expect(card).toContainText('New: agents now leave you room while you work, about 30% of every limit.')
  await card.getByRole('button', { name: 'Turn off' }).click()
  await expect(card).toHaveCount(0)
  expect(capacity.puts.at(-1)).toMatchObject({ scope: 'user', schedule: { reserve: 'off' } })
  await expect(summary(page)).toHaveText('5 days · keep nothing · no nights')
  await expect((await pacing(page)).getByRole('button', { name: 'Keep for you Nothing' })).toBeVisible()
})

test('Hold for 2 hours or until tomorrow’s hours, with a pool’s own reserve kept', async ({ page }) => {
  const { capacity } = await setup(page)
  await open(page)
  const menu = await rowMenu(page, ACCOUNTS.grok)
  await expect(menu.getByRole('menuitem', { name: 'Hold for 2 hours' })).toContainText('until 16:02')
  await expect(menu.getByRole('menuitem', { name: 'Hold until tomorrow 08:00' })).toBeVisible()
  // Arrow keys reach every duration.
  await page.keyboard.press('ArrowDown')
  await expect(menu.getByRole('menuitem', { name: 'Hold for 2 hours' })).toBeFocused()
  await page.keyboard.press('Enter')
  await expect(row(page, ACCOUNTS.grok)).toContainText('On hold until 16:02')
  expect(capacity.puts.at(-1)).toMatchObject({ scope: 'pool', pool: 'grok', schedule: { override: 'hold', override_until: new Date(Date.parse('2026-09-29T14:02:00Z')).toISOString() } })
  // A pool with its own reserve keeps it through Hold and back to the plan.
  await (await pacing(page)).getByRole('button', { name: /^Keep for you/ }).click()
  const ed = page.getByRole('dialog', { name: 'Keep for you' })
  await ed.getByRole('button', { name: 'Per vendor' }).click()
  await ed.getByLabel('Grok: keep for you').selectOption('50')
  await ed.getByRole('button', { name: 'Save' }).click()
  await expect(ed).toHaveCount(0)
  expect(capacity.schedules.find(e => e.pool === 'grok')?.schedule).toMatchObject({ override: 'hold', reserve: 'fixed', reserve_percent: 50 })
  await (await rowMenu(page, ACCOUNTS.grok)).getByRole('menuitem', { name: /Back to the plan/ }).click()
  await expect(row(page, ACCOUNTS.grok)).not.toContainText('On hold')
  expect(capacity.puts.at(-1)).toMatchObject({ scope: 'pool', pool: 'grok', schedule: { override: '', reserve: 'fixed', reserve_percent: 50 } })
})

// AEON-299 review 1: a save that fails says so, keeps the editor open, and the
// page shows what the server still has; nothing reads "Saved".
test('a failed schedule save keeps the editor open and changes nothing', async ({ page }) => {
  const { capacity } = await setup(page)
  await open(page)
  await (await rowMenu(page, ACCOUNTS.grok)).getByRole('menuitem', { name: 'Hold until I resume' }).click()
  await expect(row(page, ACCOUNTS.grok)).toContainText('On hold')
  let failures = 0
  await page.route('**/api/agent-accounts/capacity/schedule', route => {
    if (route.request().method() === 'PUT' && failures++ === 0) return route.fulfill({ status: 500, json: { error: 'database unavailable' } })
    return route.fallback()
  })
  await (await pacing(page)).getByRole('button', { name: 'Customize work week' }).click()
  const editor = page.getByRole('dialog', { name: 'Work week' })
  await editor.getByRole('switch', { name: 'Saturday' }).click()
  await editor.getByRole('switch', { name: 'Sunday' }).click()
  await editor.getByRole('button', { name: 'Save' }).click()
  await expect(page.getByText(/Nothing was saved: database unavailable/)).toBeVisible()
  await expect(page.getByText("Saved. Today's plan follows the new schedule.")).toHaveCount(0)
  await expect(editor).toBeVisible()
  await expect(summary(page)).toHaveText('5 days · keep auto · no nights')
  expect(capacity.schedules.find(e => e.pool === 'grok')?.schedule.week.filter(d => d.on)).toHaveLength(5)
  // Trying again saves the week and the Hold follows it, in one request.
  await editor.getByRole('button', { name: 'Save' }).click()
  await expect(editor).toHaveCount(0)
  await expect(summary(page)).toHaveText('7 days · keep auto · no nights')
  expect(capacity.schedules.find(e => e.pool === 'grok')?.schedule).toMatchObject({ override: 'hold' })
  expect(capacity.schedules.find(e => e.pool === 'grok')?.schedule.week.filter(d => d.on)).toHaveLength(7)
})

// AEON-299 re-review 2: a lost answer is uncertain; the page asks the server
// what it has and says Saved, Not saved, or that it could not confirm.
for (const scenario of [
  { name: 'committed, answer lost', commit: true, refetch: true, says: "Saved. Today's plan follows the new schedule.", open: false, days: '7 days' },
  { name: 'never arrived', commit: false, refetch: true, says: 'Not saved: the connection dropped before the server took it. Try again.', open: true, days: '5 days' },
  { name: 'server unreachable', commit: false, refetch: false, says: "Couldn't confirm the save: the connection dropped. Check the schedule, then try again.", open: true, days: '5 days' },
]) {
  test(`a save whose answer is lost: ${scenario.name}`, async ({ page }) => {
    const { capacity } = await setup(page)
    await open(page)
    let dropped = false
    await page.route('**/api/agent-accounts/capacity/schedule', async route => {
      const request = route.request()
      if (request.method() === 'PUT' && !dropped) {
        dropped = true
        if (scenario.commit) capacity.handle('/api/agent-accounts/capacity/schedule', 'PUT', request.postDataJSON())
        return route.abort('connectionreset')
      }
      if (request.method() === 'GET' && dropped && !scenario.refetch) return route.abort('connectionreset')
      return route.fallback()
    })
    await (await pacing(page)).getByRole('button', { name: 'Customize work week' }).click()
    const editor = page.getByRole('dialog', { name: 'Work week' })
    await editor.getByRole('switch', { name: 'Saturday' }).click()
    await editor.getByRole('switch', { name: 'Sunday' }).click()
    await editor.getByRole('button', { name: 'Save' }).click()
    await expect(page.getByText(scenario.says)).toBeVisible()
    await expect(page.getByText(/Nothing was saved/)).toHaveCount(0)
    await expect(editor).toHaveCount(scenario.open ? 1 : 0)
    if (scenario.refetch) await expect(summary(page)).toHaveText(`${scenario.days} · keep auto · no nights`)
  })
}

// AEON-299 review 2: a computer-wide login flag or a check that could not run
// is not a sign-in prompt; only a confirmed sign-out for that account is.
test('sign-in prompts only for a confirmed sign-out of that account', async ({ page }) => {
  await setup(page, { computerLogin: true, unavailable: true })
  await open(page)
  await expect(page.getByRole('region', { name: 'Needs you' })).toHaveCount(0)
  await expect(cap(page).getByText('Signed out')).toHaveCount(0)
  await expect(cap(page).getByRole('region', { name: 'Computer mbp2607' }).getByText(/^Online · seen /)).toBeVisible()
  await expect(row(page, ACCOUNTS.grok)).toContainText("Couldn't check")
  await expect(row(page, ACCOUNTS.spare)).toContainText('Ready')
})

// AEON-299 review 4: Sprint promises the server's end, the 5-hour reset included.
test('Sprint promises the limiting reset, including the 5-hour window', async ({ page }) => {
  const { capacity } = await setup(page)
  await open(page)
  const sprint = (await rowMenu(page, ACCOUNTS.claude)).getByRole('menuitem', { name: /Sprint Claude until reset/ })
  await expect(sprint).toContainText('Agents may use everything left until 16:40.')
  await sprint.click()
  await expect(row(page, ACCOUNTS.claude)).toContainText('Sprint: all of it until 16:40')
  expect(capacity.schedules.find(e => e.pool === 'claude')?.schedule.override_until).toBe(new Date(Date.parse('2026-09-29T14:40:00Z')).toISOString())
})

test('stale readings are dimmed and dated, and Manage opens Settings / Accounts', async ({ page }) => {
  await setup(page, { stale: true })
  await open(page)
  const grok = row(page, ACCOUNTS.grok)
  await expect(grok).toContainText('Reading is old · 7 h ago')
  await expect(grok.locator('.gauge')).toHaveClass(/dim/)
  await cap(page).getByRole('link', { name: /Manage/ }).click()
  await expect(page).toHaveURL('/settings/accounts')
  await expect(page.locator('#agent-accounts')).toContainText('Spare')
})

test('without account.manage the pacing controls stay visible but inert', async ({ page }) => {
  await setup(page, { manage: false })
  await open(page)
  const p = await pacing(page)
  await expect(p.getByRole('radio', { name: '6' })).toBeDisabled()
  await expect(p.getByRole('switch', { name: 'Agents at night' })).toBeDisabled()
  await expect(p.getByRole('button', { name: 'Customize work week' })).toBeDisabled()
  await expect(p.getByRole('button', { name: /^Keep for you/ })).toBeDisabled()
  await expect(cap(page).getByRole('button', { name: /^More for Codex/ })).toHaveCount(0)
})

test('a server routing wait is the row’s one state', async ({ page }) => {
  const { capacity } = await setup(page)
  await page.route('**/api/agent-accounts/capacity', async route => {
    const data = capacity.handle('/api/agent-accounts/capacity', 'GET', null)!.json as { account_id: string; routing?: { rank: number; available_slots: number; wait?: { code: string; until?: string; run_now_allowed: boolean } } }[]
    for (const item of data) item.routing = item.account_id === ACCOUNTS.main
      ? { rank: 0, available_slots: 0, wait: { code: 'vendor', until: '2026-09-29T15:30:00Z', run_now_allowed: false } }
      : { rank: 1, available_slots: 1 }
    await route.fulfill({ json: data })
  })
  await open(page)
  await expect(row(page, ACCOUNTS.main)).toContainText('At limit until 17:30')
  await expect(row(page, ACCOUNTS.spare)).toContainText('Ready')
  await expect(cap(page).getByText('4 of 6 ready · studio offline')).toBeVisible()
})

test.describe('phone', () => {
  test.use({ viewport: { width: 390, height: 844 } })
  test('stacked rows, no sideways scroll, editors as bottom sheets', async ({ page }) => {
    await setup(page, { needs: true, signin: true })
    await open(page)
    expect(await noScroll(page)).toBe(true)
    await (await pacing(page)).getByRole('button', { name: 'Customize work week' }).click()
    const sheet = page.getByRole('dialog', { name: 'Work week' })
    await expect(sheet).toHaveAttribute('aria-modal', 'true')
    const box = (await sheet.boundingBox())!
    expect(Math.round(box.x)).toBe(0)
    expect(Math.round(box.width)).toBe(390)
    await sheet.getByRole('button', { name: 'Cancel' }).click()
    await expect(sheet).toHaveCount(0)
    await (await pacing(page)).getByRole('button', { name: 'Customize night and shifts' }).click()
    await expect(page.getByRole('dialog', { name: 'Agents outside your hours' })).toBeVisible()
    await page.locator('.scrim').click({ position: { x: 20, y: 20 } })
    await expect(page.getByRole('dialog')).toHaveCount(0)
    expect(await noScroll(page)).toBe(true)
    // AEON-299 review 5: with a toast in the background, focus never leaves the
    // sheet, from the heading or anywhere else, and the page behind is inert.
    await (await rowMenu(page, ACCOUNTS.spare)).getByRole('menuitem', { name: 'Hold until I resume' }).click()
    await expect(page.locator('.toast').first()).toBeVisible()
    await (await pacing(page)).getByRole('button', { name: 'Customize work week' }).click()
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
    // Keep for you is a bottom sheet with sticky Cancel and Save.
    await (await pacing(page)).getByRole('button', { name: /^Keep for you/ }).click()
    const keep = page.getByRole('dialog', { name: 'Keep for you' })
    await expect(keep).toHaveAttribute('aria-modal', 'true')
    expect(Math.round((await keep.boundingBox())!.width)).toBe(390)
    await expect(keep.getByRole('button', { name: 'Save' })).toBeInViewport()
    await page.keyboard.press('Escape')
    await expect(keep).toHaveCount(0)
    await expect(page.locator('#app')).not.toHaveAttribute('inert', '')
    await expect(summary(page)).toBeFocused()
    expect(await noScroll(page)).toBe(true)
    // The row menu opens under its button, in view, with focus on the first choice.
    await row(page, ACCOUNTS.claude).getByRole('button', { name: /^More for / }).click()
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
  { name: 'pacing', act: async page => { await pacing(page) } },
  { name: 'sprint-hold', act: async page => {
    await (await rowMenu(page, ACCOUNTS.spare)).getByRole('menuitem', { name: /Sprint/ }).click()
    await expect(row(page, ACCOUNTS.spare)).toContainText('Sprint')
    await (await rowMenu(page, ACCOUNTS.grok)).getByRole('menuitem', { name: 'Hold for 2 hours' }).click()
    await expect(row(page, ACCOUNTS.grok)).toContainText('On hold')
  } },
  { name: 'menu', act: async page => { await rowMenu(page, ACCOUNTS.claude) } },
  { name: 'week-editor', act: async page => {
    await (await pacing(page)).getByRole('button', { name: 'Customize work week' }).click()
    const ed = page.getByRole('dialog', { name: 'Work week' })
    await ed.getByRole('switch', { name: 'Monday' }).click(); await ed.getByRole('switch', { name: 'Friday' }).click()
    await ed.getByLabel('Same hours every day').uncheck(); await ed.getByLabel('Thursday until').selectOption('14')
    await expect(ed.locator('.pv li.changed').first()).toBeVisible()
  } },
  { name: 'keep-editor', act: async page => {
    await (await pacing(page)).getByRole('button', { name: /^Keep for you/ }).click()
    const ed = page.getByRole('dialog', { name: 'Keep for you' })
    await ed.getByRole('radio', { name: /A fixed share/ }).check()
    await ed.getByRole('button', { name: 'Per vendor' }).click()
    await ed.getByLabel('Codex: keep for you').selectOption('off')
    await expect(ed.locator('.pv li.changed').first()).toBeVisible()
  } },
  { name: 'away', options: { away: true } },
  { name: 'plan-first', options: { planCard: 'first' } },
]
async function shoot(browser: Browser, shot: Shot, width: number, theme: 'light' | 'dark') {
  // The app scrolls inside its own frame, so page states use a tall viewport; overlays keep a real one.
  const overlay = /editor|menu|pacing/.test(shot.name)
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
