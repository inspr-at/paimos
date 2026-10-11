// SPDX-License-Identifier: AGPL-3.0-only
// AEON-782, now the accounts chip in the capacity row (AEON-1209, design AEON-1196; the tiles
// are AEON-1176 P6). The chip names what there is and flags what needs a person; its popover
// keeps the status line, Away and Verify again, then three tiles that open Settings › Accounts,
// Computers and Capacity. Needs you and the sign-ins grid live in those parts now.
// Risk: the chip or a tile claims all is well while something blocks agents, Verify again
// stops working from the Agents page, or a control moves while it works.
import AxeBuilder from '@axe-core/playwright'
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents, type AgentWorld } from './agents-fixtures'
import { ACCOUNTS, NOW, TZ, capacityWorld, type CapacityOptions } from './capacity-fixtures'
import { controlStability } from './control-stability'
import type { PairingView } from '../src/lib/agentPairing'
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
interface Options extends CapacityOptions { manage?: boolean; thresholds?: { early_percent: number; urgent_percent: number }; unfolded?: boolean }
/** Every sign-in reports ready and both computers are online: the calm state (the studio one is online here). */
function calm(capacity: ReturnType<typeof capacityWorld>) {
  for (const c of capacity.computers as unknown as PairingView[]) {
    const harnesses = [...new Set(c.enrollments.map(e => e.harness))]
    Object.assign(c, {
      connectivity: 'online', revision: 1,
      harness_statuses: Object.fromEntries(harnesses.map(h => [h, 'ready'])), harness_details: Object.fromEntries(harnesses.map(h => [h, { state: 'ready' }])),
      verification_capabilities: Object.fromEntries(harnesses.map(h => [h, { supported: true, policy: 'read_only', reason: '' }])),
    })
  }
}
/** Claude's verification on build-7 ran out: the one thing that blocks agents. */
function expireClaude(capacity: ReturnType<typeof capacityWorld>) {
  const c = (capacity.computers as unknown as PairingView[])[0]!
  const e = c.enrollments.find(e => e.account_id === ACCOUNTS.claude)!
  Object.assign(e, { can_verify: true, verification_state: 'expired', verification_expired_ready: false })
  return { c, e }
}
async function setup(page: Page, options: Options = {}) {
  await page.clock.setSystemTime(NOW)
  const work = fixtures()
  await mockWork(page, work, { admin: true })
  const data = agentData(world)
  const capacity = capacityWorld(options)
  calm(capacity)
  data.accounts = (capacity.accounts as unknown as typeof data.accounts).map(a => ({ ...a, registered_by_principal_id: me.id }))
  data.approvals = data.approvals.filter(a => a.decision)
  data.messages = data.messages.filter(m => !m.is_action_request)
  if (options.unfolded) work.preferences['ui.agents.sections'] = { dial: true, accounts: true, sessions: true, queued: true }
  await mockAgents(page, data, { capacity, workingPreference: () => work.preferences['agents.working'] })
  const thresholds = options.thresholds ?? { early_percent: 5, urgent_percent: 2 }
  await page.route('**/api/settings/quota-warnings', route => route.fulfill({ json: thresholds }))
  await page.route('**/api/me/permissions*', route => {
    const answer = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    answer.workspace.permissions = [...answer.workspace.permissions, 'account.read', ...(options.manage === false ? [] : ['account.manage']), 'run.create', 'run.read', 'models.read', 'work_orders.read']
    return route.fulfill({ json: answer })
  })
  return { capacity, work, data, thresholds }
}
const chip = (page: Page) => page.locator('.cap-row .acc-chip')
const pop = (page: Page) => page.getByRole('dialog', { name: 'Accounts and computers' })
const status = (page: Page) => pop(page).locator('.acc-state')
const tile = (page: Page, part: string) => pop(page).locator(`.tile[data-part="${part}"]`)
async function open(page: Page, url = '/agents') {
  await page.goto(url)
  await expect(page.getByRole('heading', { name: 'Agents', level: 1 })).toBeVisible()
  await expect(chip(page)).toBeVisible()
}
async function unfold(page: Page) {
  if (await chip(page).getAttribute('aria-expanded') !== 'true') await chip(page).click()
  await expect(pop(page)).toBeVisible()
}

test('calm: the chip names what there is; its popover says all is ready and offers the three parts', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1100 })
  await setup(page)
  await open(page)
  await expect(chip(page)).toContainText('6 accounts · 2 computers')
  await expect(chip(page).locator('.flag')).toHaveCount(0)
  await expect(chip(page)).toHaveAttribute('aria-expanded', 'false')
  await unfold(page)
  await expect(status(page)).toHaveText('All 6 ready · 2 computers online')
  await expect(pop(page).getByRole('button', { name: 'Verify again' })).toHaveCount(0)
  await expect(tile(page, 'accounts')).toContainText('All 6 ready')
  await expect(tile(page, 'computers')).toContainText('2 of 2 online')
  await expect(tile(page, 'capacity')).toContainText('5 days · keep auto · no nights')
  // Needs you and the sign-ins grid moved to the Settings parts (AEON-1176).
  await expect(page.getByRole('table')).toHaveCount(0)
  await expect(page.getByRole('heading', { name: 'Nothing needs you' })).toHaveCount(0)
  expect((await new AxeBuilder({ page }).include('.floating').analyze()).violations.map(v => `${v.id}: ${v.nodes.map(n => n.target.join(' ')).join(', ')}`)).toEqual([])
})

test('a problem: the chip flags it, the line names it, and Verify again fixes it from the popover', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1100 })
  const { capacity } = await setup(page, { thresholds: { early_percent: 10, urgent_percent: 3 } })
  const { c, e } = expireClaude(capacity)
  let posted: unknown = null
  await page.route('**/api/agent-pairing/computers/*/enrollments/*/verify', route => {
    posted = route.request().postDataJSON()
    Object.assign(e, { verification_state: 'queued', verification_run_id: 'b0000000-0000-4000-8000-000000000782' })
    return route.fulfill({ json: { account_id: e.account_id, run_id: 'b0000000-0000-4000-8000-000000000782' } })
  })
  await open(page)
  // Claude on build-7 plus Codex low (9% left this week, early warning at 10%).
  await expect(chip(page).locator('.flag')).toHaveText('2 need you')
  await unfold(page)
  await expect(status(page)).toHaveText('5 of 6 ready · Claude needs verifying on build-7 · +1')
  await expect(tile(page, 'accounts')).toContainText('Codex is low: 9% left this week')
  await expect(tile(page, 'computers')).toContainText('Claude needs verifying on build-7')
  const verify = pop(page).locator('.verify-button')
  const guard = await controlStability(page, { verify, chip: chip(page), accounts: tile(page, 'accounts'), capacity: tile(page, 'capacity') })
  await guard.check(async () => { await verify.click(); await expect(verify).toHaveAttribute('aria-busy', 'true') })
  await expect(pop(page).getByRole('status').filter({ hasText: 'Waiting for the computer to start' })).toBeVisible()
  guard.done()
  expect(posted).toEqual({ expected_revision: c.revision, expected_verification_run_id: null })
  // The queued check stays visible and disabled; readiness still awaits its result.
  await expect(verify).toBeDisabled()
  await expect(status(page)).toHaveText('5 of 6 ready · Codex is low: 9% left this week')
})

test('people without account management see the problem, not Verify again', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1100 })
  const { capacity } = await setup(page, { manage: false })
  expireClaude(capacity)
  await open(page)
  await unfold(page)
  await expect(status(page)).toContainText('Claude needs verifying on build-7')
  await expect(pop(page).getByRole('button', { name: 'Verify again' })).toHaveCount(0)
  await expect(tile(page, 'computers')).toContainText('Claude needs verifying on build-7')
})

test('each tile opens its part of Settings', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1100 })
  await setup(page)
  await open(page)
  for (const [part, path] of [['accounts', '/settings/accounts'], ['computers', '/settings/computers'], ['capacity', '/settings/capacity']] as const) {
    await unfold(page)
    await tile(page, part).click()
    await expect(page).toHaveURL(new RegExp(`${path}$`))
    await expect(pop(page)).toHaveCount(0)
    await page.goBack()
    await expect(chip(page)).toBeVisible()
  }
})

test('Away shows on the chip; Back now in the popover ends it with one click', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1100 })
  await setup(page, { away: true })
  await open(page)
  await expect(chip(page).locator('.flag.calm')).toHaveText('Away')
  await unfold(page)
  const away = pop(page).locator('.away')
  await expect(away).toContainText('Away until Mon 5 Oct')
  await expect(tile(page, 'capacity')).toContainText('Away until Mon 5 Oct')
  await away.getByRole('button', { name: 'Back now: end Away' }).click()
  await expect(page.locator('.toast').filter({ hasText: 'Welcome back. Agents follow your plan again.' })).toBeVisible()
  await expect(away).toHaveCount(0)
  await expect(chip(page).locator('.flag')).toHaveCount(0)
})

test('an approval link opens the popover once and offers that account\'s check', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1100 })
  const { work } = await setup(page)
  await open(page, `/agents?verify_account=${ACCOUNTS.claude}`)
  await expect(pop(page)).toBeVisible()
  await expect(pop(page).locator('.acc-fix')).toContainText('Verify Claude on build-7')
  await expect(pop(page).locator('.acc-fix')).toContainText('An approval link asked for a fresh check.')
  await expect(tile(page, 'computers')).toContainText('Verify Claude on build-7')
  expect(work.preferences['ui.agents.sections']).toBeUndefined()
  // Closing it is the person's choice while the link is still in the address.
  await page.keyboard.press('Escape')
  await expect(pop(page)).toHaveCount(0)
  await expect(chip(page)).toHaveAttribute('aria-expanded', 'false')
  expect(new URL(page.url()).searchParams.get('verify_account')).toBe(ACCOUNTS.claude)
})

test('loading, a failed read with Try again, and an empty workspace each say so', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1100 })
  await setup(page)
  let fail = true
  await page.route('**/api/agent-accounts/capacity', route => route.request().method() === 'GET' && fail ? route.fulfill({ status: 500, json: { error: 'down' } }) : route.fallback())
  await open(page)
  await expect(chip(page)).toContainText('Accounts could not be read')
  await unfold(page)
  fail = false
  await status(page).getByRole('button', { name: 'Try again' }).click()
  await expect(status(page)).toHaveText('All 6 ready · 2 computers online')
  await expect(chip(page)).toContainText('6 accounts · 2 computers')
  await page.route('**/api/agent-pairing/computers', route => route.fulfill({ json: { computers: [] } }))
  await page.route('**/api/agent-accounts', route => route.request().method() === 'GET' ? route.fulfill({ json: [] }) : route.fallback())
  await page.route('**/api/agent-accounts/capacity', route => route.fulfill({ json: [] }))
  await page.reload()
  await expect(chip(page)).toContainText('No accounts yet')
  await unfold(page)
  await expect(status(page)).toContainText('No accounts or computers yet')
  await expect(status(page).getByRole('link', { name: 'Connect a computer' })).toHaveAttribute('href', '/agents/register-agent')
})

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark']) {
  test(`the chip and the popover keep their controls put through verify and refresh at ${width} ${theme}`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: 1100 })
    const { capacity } = await setup(page, { thresholds: { early_percent: 10, urgent_percent: 3 } })
    const { e } = expireClaude(capacity)
    await page.route('**/api/agent-pairing/computers/*/enrollments/*/verify', route => {
      Object.assign(e, { verification_state: 'queued', verification_run_id: 'b0000000-0000-4000-8000-000000000782' })
      return route.fulfill({ json: { account_id: e.account_id, run_id: 'b0000000-0000-4000-8000-000000000782' } })
    })
    await page.addInitScript(t => { document.documentElement.dataset.theme = t }, theme)
    await open(page)
    await page.evaluate(t => { document.documentElement.dataset.theme = t }, theme)
    await unfold(page)
    await expect(status(page)).toContainText('Claude needs verifying on build-7')
    const guard = await controlStability(page, { chip: chip(page), verify: pop(page).locator('.verify-button'), accounts: tile(page, 'accounts'), computers: tile(page, 'computers'), capacity: tile(page, 'capacity') })
    await guard.check(async () => { await pop(page).locator('.verify-button').click(); await expect(pop(page)).toContainText('Waiting for the computer to start the check.'); await expect(pop(page).locator('.verify-button')).toHaveAttribute('aria-busy', 'true') })
    await guard.check(async () => { await chip(page).hover(); await expect(chip(page)).not.toHaveAttribute('data-tip', /./) })
    guard.done()
    await info.attach('popover', { body: await page.screenshot({ animations: 'disabled' }), contentType: 'image/png' })
    if (width === 390) {
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
      for (const target of [chip(page), tile(page, 'accounts'), pop(page).locator('.verify-button')]) {
        const box = (await target.boundingBox())!
        expect(box.height).toBeGreaterThanOrEqual(43.5)
      }
    }
  })
}
