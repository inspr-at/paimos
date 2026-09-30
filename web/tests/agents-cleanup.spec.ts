// SPDX-License-Identifier: AGPL-3.0-only
// AEON-402: /agents cleanup. Revoked computers fold into "Revoked (N)" and a
// person removes them; every account row has Remove; a queued run has Cancel;
// accounts and computers fold into one remembered line. AEON402_SHOTS=<dir>
// writes screenshots at 1600 and 390 in light and dark.
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { agentData, mockAgents, type AgentWorld } from './agents-fixtures'
import { ACCOUNTS, NOW, TZ, capacityWorld } from './capacity-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { pairingEnrollment, pairingView } from './agent-pairing-fixtures'

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
const id = (n: number) => `a0000000-0000-4000-8000-${String(n).padStart(12, '0')}`
const QUEUED = 'b0000000-0000-4000-8000-000000000001'

function computers() {
  const live = pairingView({
    request_id: id(90), state: 'redeemed', computer_id: id(91), computer_state: 'connected', computer_name: 'studio', revision: 4,
    setup_state: 'connected', connectivity: 'online', last_seen_at: new Date(NOW - 60_000).toISOString(),
    enrollments: [pairingEnrollment(ACCOUNTS.main, 'main', 'codex', 'Main')],
  })
  // Three revoked pairings of the same computer from one morning.
  const revoked = [1, 2, 3].map(n => pairingView({
    request_id: id(n), state: 'revoked', computer_id: id(10 + n), computer_state: 'revoked', computer_name: 'mbp2607', revision: 3,
    setup_state: 'connected', last_seen_at: new Date(NOW - n * 3_600_000).toISOString(),
    enrollments: [{ ...pairingEnrollment(id(20 + n), `claude-${n}`, 'claude', 'admin@augmentoring.com'), state: 'revoked' }],
  }))
  return [live, ...revoked]
}

async function setup(page: Page, options: { folded?: boolean; manage?: boolean } = {}) {
  await page.clock.setSystemTime(NOW)
  const work = fixtures()
  if (options.folded) work.preferences['agents-page'] = { setupFolded: true }
  await mockWork(page, work, { admin: true })
  const data = agentData(world)
  const capacity = capacityWorld()
  data.accounts = capacity.accounts as unknown as typeof data.accounts
  data.approvals = data.approvals.filter(a => a.decision)
  data.messages = data.messages.filter(m => !m.is_action_request)
  data.runs.push({
    id: QUEUED, work_order_id: 'n-2', agent_principal_id: data.runs[0]?.agent_principal_id ?? me.id, model_profile_id: null, account_id: null, requested_account_id: ACCOUNTS.claude,
    status: 'queued', requested_model: null, effective_model: null, model_evidence: 'unverified', input_tokens: 0, output_tokens: 0, cost_micros: 0, created_at: new Date(NOW - 20 * 60_000).toISOString(),
  } as unknown as typeof data.runs[number])
  const calls = await mockAgents(page, data, { capacity })
  const list = computers()
  const posts: { path: string; method: string }[] = []
  // A test may hold the next list read to answer it later with what it saw then.
  const hold: { next?: (answer: () => void) => void } = {}
  await page.route('**/api/agent-pairing/computers**', route => {
    const path = new URL(route.request().url()).pathname, method = route.request().method()
    if (method === 'GET' && path === '/api/agent-pairing/computers') {
      const json = { computers: [...list] }
      const held = hold.next
      if (held) { hold.next = undefined; held(() => void route.fulfill({ json })); return }
      return route.fulfill({ json })
    }
    const remove = /^\/api\/agent-pairing\/computers\/([^/]+)\/remove$/.exec(path)
    if (remove && method === 'POST') {
      posts.push({ path, method })
      const index = list.findIndex(c => c.computer_id === remove[1])
      const [gone] = list.splice(index, 1)
      // The server archives its account bindings with it: they leave Accounts.
      for (const e of gone.enrollments) { const at = data.accounts.findIndex(a => a.id === e.account_id); if (at !== -1) data.accounts.splice(at, 1) }
      return route.fulfill({ json: { ...gone, archived_at: new Date(NOW).toISOString() } })
    }
    return route.fallback()
  })
  await page.route('**/api/agent-accounts/*/archive', route => {
    const path = new URL(route.request().url()).pathname
    posts.push({ path, method: route.request().method() })
    const account = path.split('/')[3]
    const index = data.accounts.findIndex(a => a.id === account)
    const [gone] = data.accounts.splice(index, 1)
    // The server cancels the queued runs meant for it in the same transaction.
    for (const run of data.runs) if (run.status === 'queued' && (run.account_id === account || (run as { requested_account_id?: string }).requested_account_id === account)) Object.assign(run, { status: 'cancelled' })
    return route.fulfill({ json: { ...gone, state: 'unavailable' } })
  })
  // A test may hold the next GET of these lists: it answers later with what the
  // server had when it was asked, after a write has changed it.
  const holdGets = new Set<string>()
  const heldGets: (() => void)[] = []
  const snapshot: Record<string, () => unknown> = {
    '/api/runs': () => ({ items: data.runs, next_cursor: null }),
    '/api/agent-accounts': () => data.accounts,
    '/api/agent-accounts/capacity': () => capacity.handle('/api/agent-accounts/capacity', 'GET', null)?.json,
  }
  await page.route(url => url.pathname in snapshot, route => {
    const url = new URL(route.request().url())
    if (route.request().method() !== 'GET' || url.searchParams.has('agent') || !holdGets.delete(url.pathname)) return route.fallback()
    const json = JSON.parse(JSON.stringify(snapshot[url.pathname]()))
    heldGets.push(() => void route.fulfill({ json }))
  })
  await page.route('**/api/runs/*/cancel', route => {
    posts.push({ path: new URL(route.request().url()).pathname, method: route.request().method() })
    const run = data.runs.find(r => r.id === QUEUED)!
    Object.assign(run, { status: 'cancelled', ended_at: new Date(NOW).toISOString() })
    return route.fulfill({ json: run })
  })
  await page.route('**/api/me/permissions*', route => {
    const answer = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    answer.workspace.permissions = [...answer.workspace.permissions, 'account.read', ...(options.manage === false ? [] : ['account.manage']), 'run.create', 'run.read', 'models.read', 'work_orders.read', 'work_orders.write']
    return route.fulfill({ json: answer })
  })
  return { data, calls, posts, work, hold, list, holdGets, heldGets }
}
async function open(page: Page) {
  await page.goto('/agents')
  await expect(page.getByRole('heading', { name: 'Agents', level: 1 })).toBeVisible()
}
const setupToggle = (page: Page) => page.getByRole('button', { name: /^Accounts and computers/ })
const computersRegion = (page: Page) => page.getByRole('region', { name: 'Connected computers' })
const noScroll = (page: Page) => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)
async function shots(page: Page, state: string) {
  const dir = process.env.AEON402_SHOTS
  if (!dir) return
  mkdirSync(dir, { recursive: true })
  for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
    await expect.poll(() => noScroll(page)).toBe(true)
    await page.screenshot({ path: join(dir, `${state}__${width}__${theme}.png`), fullPage: true, animations: 'disabled' })
  }
  await page.setViewportSize({ width: 1600, height: 1000 })
}

test('revoked computers fold into one disclosure and a person removes them', async ({ page }) => {
  const errors = watchErrors(page)
  const { posts } = await setup(page)
  await open(page)
  const region = computersRegion(page)
  await expect(region.locator('.count')).toHaveText('1')
  await expect(region.locator('article.computer')).toHaveCount(1)
  const toggle = region.getByRole('button', { name: 'Revoked (3)' })
  await expect(toggle).toHaveAttribute('aria-expanded', 'false')
  await expect(region).not.toContainText('0 harnesses')
  await toggle.click()
  await expect(toggle).toHaveAttribute('aria-expanded', 'true')
  await expect(region.locator('.revoked-list li')).toHaveCount(3)
  await shots(page, 'revoked-open')
  await region.getByRole('button', { name: 'Remove mbp2607' }).first().click()
  const dialog = page.getByRole('dialog')
  await expect(dialog).toContainText('Remove mbp2607?')
  await expect(dialog).toContainText('Its history stays in the audit log.')
  await expect(dialog).toContainText('Its account binding leaves Accounts too.')
  await shots(page, 'computer-remove-confirm')
  await dialog.getByRole('button', { name: 'Remove', exact: true }).click()
  await expect(region.locator('.revoked-list li')).toHaveCount(2)
  await expect(region.getByRole('button', { name: 'Revoked (2)' })).toBeVisible()
  await expect(region.getByRole('button', { name: 'Remove mbp2607' }).first()).toBeFocused()
  await shots(page, 'computer-removed')
  expect(posts.map(p => p.path)).toEqual([`/api/agent-pairing/computers/${id(11)}/remove`])
  // A connected computer that reported is disconnected, never removed.
  await expect(region.locator('article.computer').getByRole('button', { name: /^Remove/ })).toHaveCount(0)
  await expect(region.locator('article.computer').getByRole('button', { name: 'Disconnect' })).toBeVisible()
  expect(errors).toEqual([])
})

test('a list read that started before Remove does not bring the computer back', async ({ page }) => {
  const { hold } = await setup(page)
  await open(page)
  const region = computersRegion(page)
  await region.getByRole('button', { name: 'Revoked (3)' }).click()
  let answer: (() => void) | undefined
  hold.next = release => { answer = release }
  await region.getByRole('button', { name: 'Refresh computers' }).click()
  await expect.poll(() => answer !== undefined).toBe(true)
  await region.getByRole('button', { name: 'Remove mbp2607' }).first().click()
  await page.getByRole('dialog').getByRole('button', { name: 'Remove', exact: true }).click()
  await expect(region.locator('.revoked-list li')).toHaveCount(2)
  answer!()
  await page.waitForTimeout(300)
  await expect(region.locator('.revoked-list li')).toHaveCount(2)
  await expect(region.getByRole('button', { name: 'Refresh computers' })).toBeEnabled()
})

// The page's poll holds the shared list reads across a write; the fresh read after
// the write must win and the old answers, released late, must change nothing.
async function pollHeldAcross(page: Page, holdGets: Set<string>, heldGets: (() => void)[], paths: string[]) {
  for (const path of paths) holdGets.add(path)
  await page.evaluate(() => window.dispatchEvent(new Event('online')))
  await expect.poll(() => heldGets.length).toBe(paths.length)
}

test('a runs read that started before account Remove does not bring its cancelled run back', async ({ page }) => {
  const { holdGets, heldGets } = await setup(page)
  await open(page)
  const item = page.getByRole('region', { name: 'Runs awaiting a session' }).locator('li').filter({ hasText: 'Add an Oracle Cloud connector' })
  await expect(item).toHaveCount(1)
  await pollHeldAcross(page, holdGets, heldGets, ['/api/runs', '/api/agent-accounts', '/api/agent-accounts/capacity'])
  const row = page.locator(`.acct[data-account="${ACCOUNTS.claude}"]`)
  await row.getByRole('button', { name: /^Remove / }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Remove account' }).click()
  await expect(row).toHaveCount(0)
  // The fresh read after Remove shows the queued run cancelled.
  await expect(item).toHaveCount(0)
  for (const release of heldGets.splice(0)) release()
  await page.waitForTimeout(300)
  await expect(item).toHaveCount(0)
  await expect(row).toHaveCount(0)
})

test('account and capacity reads that started before computer Remove do not bring its account back', async ({ page }) => {
  const { list, holdGets, heldGets } = await setup(page)
  // The first revoked mbp2607 still carries the Spare binding in this case.
  list[1]!.enrollments[0]!.account_id = ACCOUNTS.spare
  await open(page)
  const row = page.locator(`.acct[data-account="${ACCOUNTS.spare}"]`)
  await expect(row).toHaveCount(1)
  const region = computersRegion(page)
  await region.getByRole('button', { name: 'Revoked (3)' }).click()
  await pollHeldAcross(page, holdGets, heldGets, ['/api/agent-accounts', '/api/agent-accounts/capacity'])
  await region.getByRole('button', { name: 'Remove mbp2607' }).first().click()
  await page.getByRole('dialog').getByRole('button', { name: 'Remove', exact: true }).click()
  await expect(region.locator('.revoked-list li')).toHaveCount(2)
  await expect(row).toHaveCount(0)
  for (const release of heldGets.splice(0)) release()
  await page.waitForTimeout(300)
  await expect(row).toHaveCount(0)
  await expect(region.locator('.revoked-list li')).toHaveCount(2)
})

test('every account row has a person-only Remove that names what goes away', async ({ page }) => {
  const { posts } = await setup(page)
  await open(page)
  const row = page.locator(`[data-account="${ACCOUNTS.claude}"]`)
  await row.getByRole('button', { name: /^Remove / }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog).toContainText('Its runs and history stay.')
  await expect(dialog).toContainText('One queued run for it is cancelled.')
  await shots(page, 'account-remove-confirm')
  await dialog.getByRole('button', { name: 'Remove account' }).click()
  await expect(row).toHaveCount(0)
  // Focus moves to the next Remove, which is enabled again.
  await expect(page.locator('.acct-remove:focus')).toHaveCount(1)
  await expect(page.locator('.acct-remove:focus')).toBeEnabled()
  await shots(page, 'account-removed')
  expect(posts.map(p => p.path)).toEqual([`/api/agent-accounts/${ACCOUNTS.claude}/archive`])
  await expect(page.locator('.acct-remove').first()).toBeVisible()
})

test('without account management, account rows have no Remove', async ({ page }) => {
  await setup(page, { manage: false })
  await open(page)
  await expect(page.locator('.acct').first()).toBeVisible()
  await expect(page.locator('.acct-remove')).toHaveCount(0)
})

test('a queued run can be cancelled from /agents', async ({ page }) => {
  const { posts } = await setup(page)
  await open(page)
  const queue = page.getByRole('region', { name: 'Runs awaiting a session' })
  const item = queue.locator('li').filter({ hasText: 'Add an Oracle Cloud connector' })
  await item.getByRole('button', { name: /^Cancel/ }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog).toContainText('It has not started.')
  await dialog.getByRole('button', { name: 'Cancel run' }).click()
  await expect(item).toHaveCount(0)
  expect(posts.map(p => p.path)).toEqual([`/api/runs/${QUEUED}/cancel`])
  // Focus stays in the queue, or goes to the page title once the queue closes; never lost.
  await expect.poll(() => page.evaluate(() => {
    const el = document.activeElement as HTMLElement | null
    return !!el && el !== document.body && !(el as HTMLButtonElement).disabled && (!!el.closest('.run-queue') || el.id === 'agents-title')
  })).toBe(true)
})

test('accounts and computers fold into one calm line, remembered per person', async ({ page }) => {
  const { work } = await setup(page)
  await open(page)
  await expect(page.getByRole('region', { name: 'Accounts' })).toBeVisible()
  await shots(page, 'expanded')
  await setupToggle(page).click()
  await expect(setupToggle(page)).toHaveAttribute('aria-expanded', 'false')
  await expect(setupToggle(page)).toContainText(/^Accounts and computers1 computer · (\d+ of )?\d+ accounts? ready$/)
  await expect(page.getByRole('region', { name: 'Accounts' })).toHaveCount(0)
  await expect(computersRegion(page)).toHaveCount(0)
  await expect.poll(() => work.preferences['agents-page']).toEqual({ setupFolded: true })
  await shots(page, 'folded')
  await page.reload()
  await expect(setupToggle(page)).toHaveAttribute('aria-expanded', 'false')
  await setupToggle(page).click()
  await expect(page.getByRole('region', { name: 'Accounts' })).toBeVisible()
  await expect.poll(() => work.preferences['agents-page']).toEqual({ setupFolded: false })
})

test('a stored folded choice opens folded, without a flash of the cards', async ({ page }) => {
  await setup(page, { folded: true })
  await open(page)
  await expect(setupToggle(page)).toHaveAttribute('aria-expanded', 'false')
  await expect(page.locator('.cap')).toHaveCount(0)
})
