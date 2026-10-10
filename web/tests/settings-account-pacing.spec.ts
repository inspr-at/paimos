// SPDX-License-Identifier: AGPL-3.0-only
// AEON-786: Settings › Accounts and computers carries what the Agents page
// offered before AEON-782 removes it there: the account menu (Back to the
// plan, Sprint until reset, Hold, Remove account…), the one-time plan card and
// the pacing editors under Capacity and load. AEON786_SHOTS=1 also writes
// screenshots into this test's output directory.
import { test, expect, type Page, type TestInfo } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { agentData, mockAgents, type AgentWorld } from './agents-fixtures'
import { ACCOUNTS, NOW, TZ, capacityWorld, type CapacityOptions } from './capacity-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { controlStability } from './control-stability'

test.use({ timezoneId: TZ })
const world: AgentWorld = { me: me.id, now: NOW, projects: {}, tickets: {}, nodes: {} }
async function setup(page: Page, options: CapacityOptions & { manage?: boolean } = {}) {
  await page.clock.setSystemTime(NOW)
  await mockWork(page, fixtures(), { admin: true })
  const data = agentData(world)
  const capacity = capacityWorld(options)
  data.accounts = capacity.accounts as unknown as typeof data.accounts
  await mockAgents(page, data, { capacity })
  await page.route('**/api/me/permissions*', route => {
    const answer = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    answer.workspace.permissions = [...answer.workspace.permissions, 'account.read', ...(options.manage === false ? [] : ['account.manage'])]
    return route.fulfill({ json: answer })
  })
  return { capacity }
}
const listRow = (page: Page, id: string) => page.locator(`.list-row[data-accounts~="${id}"]`)
const zone = (page: Page) => page.locator('#capacity-and-load')
async function open(page: Page, path = '/settings/accounts') {
  await page.goto(path)
  await expect(listRow(page, ACCOUNTS.main)).toBeVisible()
}
async function accountMenu(page: Page, id: string) {
  await listRow(page, id).click()
  await page.locator('section.pane').getByRole('button', { name: /^More actions for / }).click()
  const menu = page.getByRole('menu')
  await expect(menu).toBeVisible()
  return menu
}
const pools = (capacity: ReturnType<typeof capacityWorld>) => capacity.puts.filter(p => (p as { scope: string }).scope === 'pool') as { pool: string; schedule: { override?: string; override_until?: string } | null }[]
async function shots(page: Page, info: TestInfo, name: string) {
  if (!process.env.AEON786_SHOTS) return
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme })
    await page.screenshot({ path: info.outputPath(`${name}-${colorScheme}.png`), fullPage: true })
  }
  await page.emulateMedia({ colorScheme: 'light' })
}

test('the account menu sprints, holds until the time it showed, and goes back to the plan', async ({ page }, info) => {
  const errors = watchErrors(page)
  await page.setViewportSize({ width: 1280, height: 1000 })
  const { capacity } = await setup(page)
  await open(page)
  let menu = await accountMenu(page, ACCOUNTS.main)
  await expect(menu.getByRole('menuitem', { name: /Back to the plan/ })).toHaveCount(0)
  await expect(menu.getByRole('group', { name: 'Hold Codex' })).toBeVisible()
  await shots(page, info, 'account-menu')
  await menu.getByRole('menuitem', { name: /Sprint Codex until reset/ }).click()
  await expect.poll(() => pools(capacity).length).toBe(1)
  expect(pools(capacity)[0]).toMatchObject({ pool: 'codex', schedule: { override: 'sprint' } })
  await expect(page.getByText('Sprint: agents may use everything left on Codex until it resets.')).toBeVisible()

  // Sprinting: the menu now offers Back to the plan and Hold, not Sprint again.
  menu = await page.locator('section.pane').getByRole('button', { name: /^More actions for / }).click().then(() => page.getByRole('menu'))
  await expect(menu.getByRole('menuitem', { name: /Sprint Codex/ })).toHaveCount(0)
  // Hold for 2 hours saves the instant the menu named (14:02 + 2 h, on the minute).
  await menu.getByRole('menuitem', { name: 'Hold for 2 hours' }).click()
  await expect.poll(() => pools(capacity).length).toBe(2)
  expect(pools(capacity)[1]).toMatchObject({ pool: 'codex', schedule: { override: 'hold', override_until: new Date(NOW + 2 * 3600e3 - (NOW % 60_000)).toISOString() } })
  await expect(page.getByText('Holding Codex until 16:02. Running steps finish.')).toBeVisible()

  menu = await page.locator('section.pane').getByRole('button', { name: /^More actions for / }).click().then(() => page.getByRole('menu'))
  await expect(menu.getByRole('group', { name: 'Hold Codex' })).toHaveCount(0)
  await menu.getByRole('menuitem', { name: /Back to the plan/ }).click()
  await expect.poll(() => pools(capacity).length).toBe(3)
  expect(pools(capacity)[2]).toMatchObject({ pool: 'codex', schedule: null })
  await expect(page.getByText('Codex follows your work week again.')).toBeVisible()
  expect(errors).toEqual([])
})

test('a failed Sprint is said as a failure, not as saved', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1000 })
  await setup(page)
  await page.route('**/api/agent-accounts/capacity/schedule', route => route.request().method() === 'PUT' ? route.fulfill({ status: 422, json: { error: 'schedule rejected' } }) : route.fallback())
  await open(page)
  const menu = await accountMenu(page, ACCOUNTS.claude)
  await menu.getByRole('menuitem', { name: /Sprint Claude until reset/ }).click()
  await expect(page.locator('.toast.error')).toBeVisible()
  await expect(page.locator('.toast').filter({ hasText: /^Sprint: agents may use/ })).toHaveCount(0)
})

test('a menu opened before the pool changed acts on nothing and says so', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1000 })
  const { capacity } = await setup(page)
  await open(page)
  const menu = await accountMenu(page, ACCOUNTS.main)
  // Another tab sprints Codex while this menu still offers Sprint; a refresh brings it in.
  capacity.schedules.push({ scope: 'pool', pool: 'codex', schedule: { ...capacity.schedules.find(e => e.scope === 'user')!.schedule, override: 'sprint', override_until: '2026-09-30T16:02:00.000Z' } })
  const reread = page.waitForResponse(response => response.url().endsWith('/api/agent-accounts/capacity/schedule') && response.request().method() === 'GET')
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await reread
  await expect(menu.getByRole('menuitem', { name: /Sprint Codex until reset/ })).toBeVisible()
  await menu.getByRole('menuitem', { name: /Sprint Codex until reset/ }).click()
  await expect(page.locator('.toast.error').filter({ hasText: 'This record changed. Reopen its menu.' })).toBeVisible()
  expect(pools(capacity)).toEqual([])
})

test('Remove account… says what stays untouched, and only Remove account removes it', async ({ page }) => {
  const errors = watchErrors(page)
  await page.setViewportSize({ width: 1280, height: 1000 })
  const { capacity } = await setup(page)
  const archived: string[] = []
  await page.route('**/api/agent-accounts/*/archive', async route => {
    const id = new URL(route.request().url()).pathname.split('/').at(-2)!
    archived.push(id)
    const at = capacity.accounts.findIndex(a => a.id === id)
    const [gone] = capacity.accounts.splice(at, 1)
    await route.fulfill({ json: { ...gone, state: 'archived' } })
  })
  await open(page)
  let menu = await accountMenu(page, ACCOUNTS.grok)
  await menu.getByRole('menuitem', { name: /Remove account/ }).click()
  const confirm = page.getByRole('dialog', { name: 'Remove Grok · markus?' })
  await expect(confirm).toContainText('It leaves Accounts and agents stop using it.')
  await expect(confirm).toContainText('Its binding on build-7 is disconnected.')
  await expect(confirm).toContainText('Its runs and history stay. Other accounts and the vendor subscription are untouched.')
  await confirm.getByRole('button', { name: 'Cancel' }).click()
  expect(archived).toEqual([])

  menu = await page.locator('section.pane').getByRole('button', { name: /^More actions for / }).click().then(() => page.getByRole('menu'))
  await menu.getByRole('menuitem', { name: /Remove account/ }).click()
  await page.getByRole('dialog', { name: 'Remove Grok · markus?' }).getByRole('button', { name: 'Remove account' }).click()
  await expect.poll(() => archived).toEqual([ACCOUNTS.grok])
  await expect(page.getByText('Removed Grok · markus. Its runs and history stay.')).toBeVisible()
  await expect(listRow(page, ACCOUNTS.grok)).toHaveCount(0)
  expect(errors).toEqual([])
})

test('Remove account… confirmed after the account was regrouped removes nothing and says so', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1000 })
  const { capacity } = await setup(page)
  // Main and Spare share one quota, so they are one account with two logins.
  const fingerprint = 'cd'.repeat(32)
  for (const a of capacity.accounts.filter(a => a.id === ACCOUNTS.main || a.id === ACCOUNTS.spare)) Object.assign(a, { quota_fingerprint: fingerprint, quota_pool_fingerprint: fingerprint })
  const archived: string[] = []
  await page.route('**/api/agent-accounts/*/archive', async route => {
    archived.push(new URL(route.request().url()).pathname.split('/').at(-2)!)
    await route.fulfill({ json: { state: 'archived' } })
  })
  await open(page)
  await expect(listRow(page, ACCOUNTS.spare)).toHaveAttribute('data-accounts', `${ACCOUNTS.spare} ${ACCOUNTS.main}`)
  const menu = await accountMenu(page, ACCOUNTS.main)
  await menu.getByRole('menuitem', { name: /Remove account/ }).click()
  const confirm = page.getByRole('dialog', { name: /^Remove Codex · / })
  await expect(confirm).toContainText('All 2 logins that share this quota are removed.')
  // Another tab stops the sharing: Spare is now an account of its own, and the
  // account still shown here holds only Main.
  capacity.accounts.find(a => a.id === ACCOUNTS.spare)!.quota_pool_fingerprint = ''
  const reread = page.waitForResponse(response => response.url().endsWith('/api/agent-accounts/capacity/schedule') && response.request().method() === 'GET')
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await reread
  await expect(listRow(page, ACCOUNTS.spare)).toHaveAttribute('data-accounts', ACCOUNTS.spare)
  await confirm.getByRole('button', { name: 'Remove account' }).click()
  await expect(page.getByText('This record changed. Reopen its menu.')).toBeVisible()
  expect(archived).toEqual([])
})

test('people without account management get no account menu and read-only pacing', async ({ page }) => {
  await setup(page, { manage: false, planCard: 'first' })
  await open(page)
  await listRow(page, ACCOUNTS.main).click()
  await expect(page.locator('section.pane').getByRole('button', { name: /^More actions for / })).toHaveCount(0)
  await expect(page.getByRole('region', { name: 'The plan' })).toHaveCount(0)
  await expect(zone(page).getByRole('radio', { name: '5' })).toBeDisabled()
  await expect(zone(page).getByRole('button', { name: 'Customize work week' })).toBeDisabled()
  await expect(zone(page).getByRole('switch')).toBeDisabled()
})

test('the plan card shows until it is answered: Looks right saves Auto, then it is gone', async ({ page }) => {
  const errors = watchErrors(page)
  const { capacity } = await setup(page, { planCard: 'first' })
  await open(page)
  const card = page.getByRole('region', { name: 'The plan' })
  await expect(card).toContainText("Here's the plan.")
  await card.getByRole('button', { name: 'Looks right' }).click()
  await expect.poll(() => capacity.puts.length).toBe(1)
  expect(capacity.puts[0]).toMatchObject({ scope: 'user', schedule: { reserve: 'auto' } })
  await expect(card).toHaveCount(0)
  await page.reload()
  await expect(listRow(page, ACCOUNTS.main)).toBeVisible()
  await expect(page.getByRole('region', { name: 'The plan' })).toHaveCount(0)
  expect(errors).toEqual([])
})

test('the plan card: Change leads to Capacity and load without saving', async ({ page }) => {
  const { capacity } = await setup(page, { planCard: 'first' })
  await open(page)
  await page.getByRole('region', { name: 'The plan' }).getByRole('button', { name: 'Change' }).click()
  await expect(page.getByRole('region', { name: 'The plan' })).toHaveCount(0)
  await expect(zone(page).getByRole('radio', { name: '5' })).toBeFocused()
  expect(capacity.puts).toEqual([])
})

test('the plan card for an existing schedule: Turn off keeps nothing for you', async ({ page }) => {
  const { capacity } = await setup(page, { planCard: 'new' })
  await open(page)
  const card = page.getByRole('region', { name: 'The plan' })
  await expect(card).toContainText('New:')
  await card.getByRole('button', { name: 'Turn off' }).click()
  await expect.poll(() => capacity.puts.length).toBe(1)
  expect(capacity.puts[0]).toMatchObject({ scope: 'user', schedule: { reserve: 'off' } })
  await expect(card).toHaveCount(0)
})

test('Capacity and load: work days, nights and the editors, with controls that stay put', async ({ page }, info) => {
  const errors = watchErrors(page)
  await page.setViewportSize({ width: 1280, height: 1000 })
  const { capacity } = await setup(page)
  await open(page, '/settings/accounts#capacity-and-load')
  await expect(zone(page).getByRole('radio', { name: '5' })).toBeFocused()
  const guard = await controlStability(page, {
    weekGear: zone(page).getByRole('button', { name: 'Customize work week' }),
    keep: zone(page).getByRole('button', { name: /Keep for you/ }),
    nights: zone(page).getByRole('switch', { name: 'Agents at night' }),
    nightGear: zone(page).getByRole('button', { name: 'Customize night and shifts' }),
  })
  await guard.check(async () => {
    await zone(page).getByRole('radio', { name: '7' }).click()
    await expect.poll(() => capacity.puts.length).toBe(1)
    await expect(zone(page).getByRole('radio', { name: '7' })).toHaveAttribute('aria-checked', 'true')
  })
  expect((capacity.puts[0] as { schedule: { week: { on: boolean }[] } }).schedule.week.every(d => d.on)).toBe(true)
  await guard.check(async () => {
    await zone(page).getByRole('switch', { name: 'Agents at night' }).click()
    await expect(zone(page).getByRole('switch', { name: 'Agents at night' })).toHaveAttribute('aria-checked', 'true')
  })
  expect(capacity.puts[1]).toMatchObject({ scope: 'user', schedule: { nights: true } })
  guard.done()

  await zone(page).getByRole('button', { name: 'Customize work week' }).click()
  const week = page.getByRole('dialog').filter({ has: page.locator('#ed-title') })
  await expect(week).toBeVisible()
  await shots(page, info, 'pacing-week-editor')
  await page.keyboard.press('Escape')
  await expect(week).toHaveCount(0)
  await expect(zone(page).getByRole('button', { name: 'Customize work week' })).toBeFocused()

  await zone(page).getByRole('button', { name: /Keep for you/ }).click()
  await expect(page.getByRole('dialog').filter({ has: page.locator('#keep-title') })).toBeVisible()
  expect(errors).toEqual([])
})

test('Capacity and load: leaving a custom week for 5, 6 or 7 moves none of them', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1000 })
  const { capacity } = await setup(page)
  // Mon–Thu: no preset matches, so the custom-week radio is shown.
  const user = capacity.schedules.find(e => e.scope === 'user')!.schedule
  user.week = user.week.map((d, i) => ({ ...d, on: i < 4 }))
  await open(page, '/settings/accounts#capacity-and-load')
  await expect(zone(page).locator('.days [data-v="custom"]')).toHaveAttribute('aria-checked', 'true')
  const guard = await controlStability(page, {
    five: zone(page).getByRole('radio', { name: '5' }),
    six: zone(page).getByRole('radio', { name: '6' }),
    seven: zone(page).getByRole('radio', { name: '7' }),
    weekGear: zone(page).getByRole('button', { name: 'Customize work week' }),
  })
  await guard.check(async () => {
    await zone(page).getByRole('radio', { name: '6' }).click()
    await expect.poll(() => capacity.puts.length).toBe(1)
    await expect(zone(page).getByRole('radio', { name: '6' })).toHaveAttribute('aria-checked', 'true')
    await expect(zone(page).locator('.days [data-v="custom"]')).toHaveCount(0)
  })
  await guard.check(async () => {
    await zone(page).getByRole('radio', { name: '5' }).click()
    await expect.poll(() => capacity.puts.length).toBe(2)
    await expect(zone(page).getByRole('radio', { name: '5' })).toHaveAttribute('aria-checked', 'true')
  })
  guard.done()
})

test('phone: the editor is a full-height sheet and nothing scrolls sideways', async ({ page }, info) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await setup(page, { planCard: 'first' })
  await open(page)
  await expect(page.getByRole('region', { name: 'The plan' })).toBeVisible()
  await shots(page, info, 'phone-plan-card')
  await zone(page).getByRole('button', { name: 'Customize night and shifts' }).click()
  const sheet = page.locator('.pacing-sheet-host [role="dialog"][aria-modal="true"]')
  await expect(sheet).toBeVisible()
  const box = await sheet.boundingBox()
  expect(box?.height).toBeGreaterThan(800)
  await shots(page, info, 'phone-night-sheet')
  await page.keyboard.press('Escape')
  await expect(sheet).toHaveCount(0)
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true)
})
