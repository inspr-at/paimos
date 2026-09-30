// SPDX-License-Identifier: AGPL-3.0-only
// AEON-384: Settings → Accounts as a list with an inline detail. Windows with
// source and freshness, the last readings, inline rename with "Name it", the
// Advanced sentence, old limits as "Set by you" (Remove, Make this repeat) and
// money for an API key. No form asks for start, end, unit, pace or burst.
// AEON384_SHOTS=<dir> also writes screenshots at 1600 and 390, light and dark.
import { mkdirSync } from 'node:fs'
import { test, expect, type Browser, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { agentData, mockAgents, type AgentWorld } from './agents-fixtures'
import { ACCOUNTS, NOW, OLD_WINDOW, TZ, capacityWorld, type CapacityOptions } from './capacity-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'

test.use({ timezoneId: TZ })
const world: AgentWorld = { me: me.id, now: NOW, projects: {}, tickets: {}, nodes: {} }
interface Setup extends CapacityOptions { manage?: boolean }
async function setup(page: Page, options: Setup = {}) {
  await page.clock.setSystemTime(NOW)
  await mockWork(page, fixtures(), { admin: true })
  const data = agentData(world)
  const capacity = capacityWorld(options)
  data.accounts = capacity.accounts as unknown as typeof data.accounts
  const calls = await mockAgents(page, data, { capacity })
  await page.route('**/api/me/permissions*', route => {
    const answer = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    answer.workspace.permissions = [...answer.workspace.permissions, 'account.read', ...(options.manage === false ? [] : ['account.manage'])]
    return route.fulfill({ json: answer })
  })
  return { data, capacity, calls }
}
const card = (page: Page) => page.locator('#agent-accounts')
const row = (page: Page, id: string) => page.locator(`[data-account="${id}"]`)
async function open(page: Page) {
  await page.goto('/settings/accounts')
  await expect(card(page).getByRole('heading', { name: /Codex/ })).toBeVisible()
  await expect(row(page, ACCOUNTS.main).locator('.chip.host')).toHaveText('mbp2607')
}
async function details(page: Page, id: string) {
  await row(page, id).getByRole('button', { name: /^Details for/ }).click()
  const detail = page.locator(`#account-detail-${id}`)
  await expect(detail).toBeVisible()
  return detail
}
const noScroll = (page: Page) => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)

test('matching login hints need a person to confirm the selected pair', async ({ page }) => {
  const errors = watchErrors(page)
  const { capacity } = await setup(page)
  const fingerprint = 'ab'.repeat(32)
  for (const a of capacity.accounts.filter(a => a.harness === 'codex')) Object.assign(a, { quota_fingerprint: fingerprint, quota_pool_fingerprint: '' })
  const writes: Record<string, unknown>[] = []
  await page.route('**/api/agent-accounts/quota-pool', async route => {
    const body = route.request().postDataJSON()
    writes.push(body)
    for (const a of capacity.accounts) if (body.account_ids.includes(a.id)) Object.assign(a, { quota_pool_fingerprint: body.confirmed ? body.quota_fingerprint : '' })
    await route.fulfill({ status: 204 })
  })
  await open(page)
  const detail = await details(page, ACCOUNTS.main)
  const pool = detail.getByRole('button', { name: 'Pool with Spare · mbp2607', exact: true })
  await pool.click()
  const confirm = page.getByRole('dialog', { name: 'Same login — pool them?' })
  await expect(confirm).toContainText('Main · mbp2607')
  await expect(confirm).toContainText('Spare · mbp2607')
  await confirm.getByRole('button', { name: 'Cancel' }).click()
  expect(writes).toEqual([])
  await pool.click()
  await confirm.getByRole('button', { name: 'Pool accounts' }).click()
  await expect.poll(() => writes.length).toBe(1)
  expect(writes[0]).toEqual({ account_ids: [ACCOUNTS.main, ACCOUNTS.spare], quota_fingerprint: fingerprint, confirmed: true })
  await expect(detail.getByRole('button', { name: 'Stop sharing quota' })).toBeVisible()
  await expect(detail).toContainText('Shared with Spare · mbp2607')
  await expect(detail.getByRole('button', { name: 'Pool with Studio · studio' })).toBeVisible()
  await expect(pool).toHaveCount(0)
  const shots = process.env.AEON397_SHOTS
  if (shots) {
    mkdirSync(shots, { recursive: true })
    for (const width of [1600, 390]) for (const colorScheme of ['light', 'dark'] as const) {
      await page.setViewportSize({ width, height: 1000 })
      await page.emulateMedia({ colorScheme })
      expect(await noScroll(page)).toBe(true)
      await page.screenshot({ path: `${shots}/quota-${width}-${colorScheme}.png`, fullPage: true })
    }
  }
  await detail.getByRole('button', { name: 'Stop sharing quota' }).click()
  await page.getByRole('dialog', { name: 'Stop sharing quota?' }).getByRole('button', { name: 'Stop sharing', exact: true }).click()
  await expect.poll(() => writes.length).toBe(2)
  expect(writes[1]).toEqual({ account_ids: [ACCOUNTS.main], quota_fingerprint: fingerprint, confirmed: false })
  expect(errors).toEqual([])
})

test('a reader cannot confirm matching login hints', async ({ page }) => {
  const { capacity } = await setup(page, { manage: false })
  for (const a of capacity.accounts.filter(a => a.harness === 'codex')) Object.assign(a, { quota_fingerprint: 'ab'.repeat(32) })
  await open(page)
  const detail = await details(page, ACCOUNTS.main)
  await expect(detail.getByRole('button', { name: /^Pool with/ })).toHaveCount(0)
})

test('accounts are a list by vendor with how each is read, and no allowance form', async ({ page }) => {
  const errors = watchErrors(page)
  await setup(page)
  await open(page)
  await expect(card(page).locator('.group-head')).toHaveText(['Codex3 accounts', 'Claude1 account', 'Grok1 account', 'Cursor1 account'])
  await expect(row(page, ACCOUNTS.main)).toContainText('Reads every 5 min')
  await expect(row(page, ACCOUNTS.claude)).toContainText('Reads during runs')
  await expect(row(page, ACCOUNTS.grok)).toContainText("Doesn't show its limit")
  // Studio's computer is offline: the row says so; a usable row carries no state word.
  await expect(row(page, ACCOUNTS.studio).locator('.state')).toHaveText('Offline')
  await expect(row(page, ACCOUNTS.main).locator('.state')).toHaveCount(0)
  await expect(row(page, ACCOUNTS.main).getByRole('switch', { name: 'Agents may use it · Main' })).toHaveAttribute('aria-checked', 'true')
  // The form is gone: nothing asks for a start, end, unit, pace or burst.
  await expect(page.getByRole('button', { name: /allowance window/i })).toHaveCount(0)
  await expect(page.getByLabel(/^(starts?|ends?|unit|pace|burst)/i)).toHaveCount(0)
  expect(errors).toEqual([])
})

test('a row opens its detail: windows with source and freshness, and the last three readings', async ({ page }) => {
  await setup(page)
  await open(page)
  const detail = await details(page, ACCOUNTS.claude)
  await expect(row(page, ACCOUNTS.claude).getByRole('button', { name: 'Details for markus' })).toHaveAttribute('aria-expanded', 'true')
  await expect(detail.locator('.window')).toHaveText([
    'Weekly37% left · resets Sun 11:00 · Claude reported · 6 min ago',
    '5-hour60% left · resets 16:40 · Claude reported · 6 min ago',
  ])
  await expect(detail.locator('.readings li')).toHaveText(['13:56 63% used Claude reported', '13:34 62% read on mbp2607', '09:01 61% read on mbp2607'])
  // The row itself toggles too; the detail closes.
  await row(page, ACCOUNTS.claude).locator('.reads').click()
  await expect(detail).toHaveCount(0)
  // An account without a reading says how its first number comes.
  await page.goto('/settings/accounts')
  const studio = await details(page, ACCOUNTS.studio)
  await expect(studio.locator('.window')).toContainText('Read on studio · 3 h ago · offline')
})

test('the Advanced sentence round-trips and caps on top of the plan', async ({ page }) => {
  const { capacity } = await setup(page, { limits: true })
  await open(page)
  await expect(row(page, ACCOUNTS.main).locator('.chip.mine')).toHaveText('Limit · 20% a day')
  const main = await details(page, ACCOUNTS.main)
  await expect(main.getByLabel('At most')).toHaveValue('20')
  await expect(main.getByLabel('Counted in')).toHaveValue('percent')
  await expect(main.getByLabel('Per')).toHaveValue('day')
  // 8 left under the sentence, 12 in today's plan: the sentence binds first.
  await expect(main.locator('.use')).toHaveText('12% used today · binds before the plan today')
  await expect(main.getByRole('button', { name: 'Apply' })).toBeDisabled()
  await main.getByLabel('At most').fill('10')
  await main.getByRole('button', { name: 'Apply' }).click()
  await expect(main.getByRole('status')).toHaveText('Saved: at most 10% a day.')
  expect(capacity.writes.at(-1)).toEqual({ path: `/api/agent-accounts/${ACCOUNTS.main}/limit`, method: 'PUT', body: { amount: 10, unit: 'percent', period: 'day' } })
  await expect(row(page, ACCOUNTS.main).locator('.chip.mine')).toHaveText('Limit · 10% a day')
  await expect(main.getByLabel('At most')).toHaveValue('10')
  await main.getByRole('button', { name: 'Remove' }).click()
  await expect(main.getByRole('button', { name: 'Set a limit by hand' })).toBeVisible()
  expect(capacity.writes.at(-1)).toMatchObject({ method: 'DELETE', path: `/api/agent-accounts/${ACCOUNTS.main}/limit` })
  await expect(row(page, ACCOUNTS.main).locator('.chip.mine')).toHaveCount(0)
  // Runs count on Grok; a percent is offered only because this world reads it.
  const grok = await details(page, ACCOUNTS.grok)
  await expect(grok.locator('.use')).toHaveText('3 of 5 runs this week')
  await expect(grok.getByLabel('Counted in').locator('option')).toHaveText(['%', 'runs'])
  // A bad amount names the one thing to fix and sends nothing.
  const writes = capacity.writes.length
  await grok.getByLabel('At most').fill('two')
  await grok.getByRole('button', { name: 'Apply' }).click()
  await expect(grok.getByRole('alert')).toHaveText('Enter a whole number above 0.')
  expect(capacity.writes.length).toBe(writes)
})

test('old limits set by hand show as Set by you, with Make this repeat and Remove', async ({ page }) => {
  const { capacity } = await setup(page, { limits: true })
  await open(page)
  await expect(row(page, ACCOUNTS.spare).locator('.chip.mine')).toHaveText('Set by you')
  const spare = await details(page, ACCOUNTS.spare)
  const mine = spare.locator('.mine li')
  await expect(mine.locator('.what')).toHaveText('300 requests · 28 Sep – 28 Oct')
  await expect(mine.locator('.what')).toHaveAttribute('data-tip', '40 requests used of 300 requests, 28 Sep – 28 Oct')
  await mine.getByRole('button', { name: 'Make this repeat' }).click()
  await expect(spare.getByRole('status')).toHaveText('Now repeats: at most 300 requests a month.')
  expect(capacity.writes.at(-1)).toMatchObject({ method: 'POST', path: `/api/agent-accounts/${ACCOUNTS.spare}/windows/${OLD_WINDOW}/repeat` })
  await expect(spare.locator('.mine')).toHaveCount(0)
  await expect(spare.getByLabel('Counted in')).toHaveValue('requests')
  await expect(spare.getByLabel('Per')).toHaveValue('month')
  await expect(row(page, ACCOUNTS.spare).locator('.chip.mine')).toHaveText('Limit · 300 requests a month')
})

test('removing an old limit asks once and keeps the rest', async ({ page }) => {
  const { capacity } = await setup(page, { limits: true })
  await open(page)
  const spare = await details(page, ACCOUNTS.spare)
  await spare.locator('.mine').getByRole('button', { name: 'Remove' }).click()
  const confirm = page.getByRole('dialog', { name: 'Remove 300 requests on Spare?' })
  await expect(confirm).toContainText("Agents follow your plan and the vendor's readings on Spare. Its history stays.")
  await confirm.getByRole('button', { name: 'Remove limit' }).click()
  await expect(spare.locator('.mine')).toHaveCount(0)
  expect(capacity.writes.at(-1)).toMatchObject({ method: 'DELETE', path: `/api/agent-accounts/${ACCOUNTS.spare}/windows/${OLD_WINDOW}` })
  await expect(row(page, ACCOUNTS.spare).locator('.chip.mine')).toHaveCount(0)
})

test('names that clash ask to be named, and a rename changes only the name', async ({ page }) => {
  const { capacity } = await setup(page, { clash: true })
  await open(page)
  await expect(card(page).getByRole('button', { name: /^Name it/ })).toHaveCount(2)
  await details(page, ACCOUNTS.studio)
  const trigger = row(page, ACCOUNTS.studio).getByRole('button', { name: /^Name it/ })
  await trigger.click()
  const input = page.locator(`#account-detail-${ACCOUNTS.studio}`).getByRole('textbox', { name: 'New name for Main' })
  await expect(input).toBeFocused()
  await input.press('Escape')
  await expect(trigger).toBeFocused()
  await trigger.press('Enter')
  await expect(input).toBeFocused()
  await input.fill('Studio')
  await input.press('Enter')
  await expect(row(page, ACCOUNTS.studio).locator('.name')).toHaveText('Studio')
  expect(capacity.writes.at(-1)).toEqual({ path: `/api/agent-accounts/${ACCOUNTS.studio}/label`, method: 'PUT', body: { label: 'Studio' } })
  await expect(card(page).getByRole('button', { name: /^Name it/ })).toHaveCount(0)
  // Escape leaves the name as it was.
  const main = page.locator(`#account-detail-${ACCOUNTS.main}`)
  await details(page, ACCOUNTS.main)
  await main.getByRole('button', { name: 'Rename Main' }).click()
  await main.getByRole('textbox').fill('')
  await main.getByRole('textbox').press('Enter')
  await expect(main.getByRole('alert')).toHaveText('Give the account a name.')
  await main.getByRole('textbox').press('Escape')
  await expect(main.locator('.nm')).toHaveText('Main')
  await expect(main.getByRole('button', { name: 'Rename Main' })).toBeFocused()
})

test('an API key shows money, and a dollar limit reads back', async ({ page }) => {
  const { capacity } = await setup(page, { apiKey: true })
  await open(page)
  const pi = await details(page, ACCOUNTS.pi)
  await expect(pi).toContainText('Spend$12.40 this month · no limit · list prices')
  await expect(pi).toContainText("Pi doesn't show its limit to AEON. One run at a time by day, freely tonight.")
  await pi.getByRole('button', { name: 'Set a limit by hand' }).click()
  await expect(pi.getByLabel('At most')).toBeFocused()
  await pi.getByLabel('Counted in').selectOption('dollars')
  await pi.getByLabel('Per').selectOption('month')
  await pi.getByLabel('At most').fill('50')
  await pi.getByRole('button', { name: 'Apply' }).click()
  expect(capacity.writes.at(-1)).toEqual({ path: `/api/agent-accounts/${ACCOUNTS.pi}/limit`, method: 'PUT', body: { amount: 50_000_000, unit: 'cost_micros', period: 'month' } })
  await expect(pi).toContainText('Spend$12.40 of $50 · resets Sun 1 Nov')
})

test('accounts without priced run usage never offer dollar limits', async ({ page }) => {
  await setup(page, { unread: true })
  await open(page)
  for (const id of [ACCOUNTS.main, ACCOUNTS.pi]) {
    const detail = await details(page, id)
    await detail.getByRole('button', { name: 'Set a limit by hand' }).click()
    await expect(detail.getByLabel('Counted in').locator('option[value="dollars"]')).toHaveCount(0)
  }
})

test('without account.manage the list is read-only', async ({ page }) => {
  await setup(page, { manage: false, limits: true })
  await open(page)
  await expect(row(page, ACCOUNTS.main).getByRole('switch')).toBeDisabled()
  const main = await details(page, ACCOUNTS.main)
  await expect(main.getByRole('button', { name: /Rename/ })).toHaveCount(0)
  await expect(main.getByRole('textbox')).toHaveCount(0)
  await expect(main).toContainText('LimitAt most 20% a day')
  const spare = await details(page, ACCOUNTS.spare)
  await expect(spare.locator('.mine').getByRole('button')).toHaveCount(0)
})

test('phone: no horizontal scroll, 44 px targets, the detail stacks', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await setup(page, { limits: true, apiKey: true })
  await open(page)
  const main = await details(page, ACCOUNTS.main)
  expect(await noScroll(page)).toBe(true)
  for (const target of [row(page, ACCOUNTS.main).getByRole('switch'), row(page, ACCOUNTS.main).getByRole('button', { name: /^Details for/ }), main.getByRole('button', { name: 'Apply' })]) {
    const box = await target.boundingBox()
    expect(box!.height).toBeGreaterThanOrEqual(44)
  }
})

test('phone: Name it is reachable and opens the rename', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await setup(page, { clash: true })
  await open(page)
  await row(page, ACCOUNTS.studio).getByRole('button', { name: /^Name it/ }).click()
  await expect(page.locator(`#account-detail-${ACCOUNTS.studio}`).getByRole('textbox', { name: 'New name for Main' })).toBeFocused()
  expect(await noScroll(page)).toBe(true)
})

// ---------------------------------------------------------------- screenshots
const SHOTS = process.env.AEON384_SHOTS
type Shot = { name: string; options?: Setup; act?: (page: Page) => Promise<void> }
const SHOT_STATES: Shot[] = [
  { name: 'list', options: { limits: true, apiKey: true } },
  { name: 'detail-limit', options: { limits: true, apiKey: true }, act: async page => { await details(page, ACCOUNTS.main) } },
  { name: 'detail-set-by-you', options: { limits: true }, act: async page => { await details(page, ACCOUNTS.spare) } },
  { name: 'detail-claude', act: async page => { await details(page, ACCOUNTS.claude) } },
  { name: 'detail-money', options: { apiKey: true }, act: async page => { const pi = await details(page, ACCOUNTS.pi); await pi.getByRole('button', { name: 'Set a limit by hand' }).click() } },
  { name: 'name-it', options: { clash: true }, act: async page => { await row(page, ACCOUNTS.studio).getByRole('button', { name: /^Name it/ }).click() } },
  { name: 'read-only', options: { manage: false, limits: true }, act: async page => { await details(page, ACCOUNTS.grok) } },
]
async function shoot(browser: Browser, shot: Shot, width: number, theme: 'light' | 'dark') {
  const context = await browser.newContext({ viewport: { width, height: width < 800 ? 2000 : 1200 }, colorScheme: theme, reducedMotion: 'reduce', timezoneId: TZ })
  const page = await context.newPage()
  try {
    await setup(page, shot.options)
    await open(page)
    await shot.act?.(page)
    await page.waitForTimeout(300)
    const overflow = !(await noScroll(page))
    await page.screenshot({ path: `${SHOTS}/${width}-${theme}-${shot.name}${overflow ? '-OVERFLOW' : ''}.png` })
  } finally {
    // A route handler still answering a background read must not hold the close.
    await page.unrouteAll({ behavior: 'ignoreErrors' })
    await context.close()
  }
}
test.describe('screenshots', () => {
  test.skip(!SHOTS, 'set AEON384_SHOTS=<dir> to capture')
  test.setTimeout(600_000)
  for (const shot of SHOT_STATES) test(`shot ${shot.name}`, async ({ browser }) => {
    mkdirSync(SHOTS!, { recursive: true })
    for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) await shoot(browser, shot, width, theme)
  })
})
