// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Page } from '@playwright/test'
import { agentData, mockAgents } from './agents-fixtures'
import { mockStartAgent } from './start-agent-fixtures'
import { defaultSchedule } from './capacity-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { fixtures, me, mockWork } from './work-fixtures'

const shots = process.env.AEON389_SHOTS
const now = Date.parse('2026-09-29T12:00:00Z')
const fingerprint = 'ab'.repeat(32)
const clientGroup = 'aa000000-0000-4000-8000-000000000389'
const studioId = 'ac000000-0000-4000-8000-000000000099'
const modelId = 'b1000000-0000-4000-8000-000000000389'
const queuedId = '70000000-0000-4000-8000-000000000389'

function shot(page: Page, name: string) {
  if (!shots) return Promise.resolve()
  mkdirSync(shots, { recursive: true })
  return page.screenshot({ path: join(shots, `${name}.png`), fullPage: true })
}
function noPath(body: unknown) {
  const text = JSON.stringify(body ?? {})
  expect(text).not.toMatch(/\/users|codex_home|claude_config|\\|socket/i)
}
function windowOf() {
  return {
    reading: { window_kind: 'weekly', window_minutes: 10080, used_percent: 28, resets_at: '2026-10-06T07:00:00.000Z', plan: 'Pro', source: 'harness', read_at: '2026-09-29T11:50:00.000Z' },
    starts_at: '2026-09-29T07:00:00.000Z', allowance: 100, remaining_percent: 72, freshness: 'fresh', usage_today_known: true,
    pacing: { usable_hours: 8, percent_per_hour: 4, suggested_today_percent: 12, budget_percent: 14, used_today_percent: 2, tonight_percent: 0, reserve_percent: 30, reserve_effective_percent: 20 },
  }
}

async function desk(page: Page, theme: 'light' | 'dark', splitQuota = false) {
  await page.clock.install({ time: new Date(now) })
  const work = fixtures()
  work.preferences.theme = { choice: theme }
  await mockWork(page, work, { admin: true })
  const data = agentData({
    now, me: me.id,
    projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' },
    tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' },
    nodes: { 'n-1': { key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning' } },
  })
  const spare = data.accounts.find(account => account.harness === 'codex')!
  const claude = data.accounts.find(account => account.harness === 'claude')!
  Object.assign(spare, { label: 'Spare', host_label: 'mbp2607', quota_fingerprint: fingerprint, daemon_id: 'mbp2607' })
  data.accounts.push({ ...spare, id: studioId, account_key: 'codex-studio', label: 'Studio', host_label: 'studio', daemon_id: 'studio', quota_fingerprint: fingerprint })
  if (splitQuota) Object.assign(spare, { group_id: 'bb000000-0000-4000-8000-000000000389', group_name: 'Client login' })
  Object.assign(claude, { group_id: clientGroup, group_name: 'Client', host_label: 'imac0' })
  data.runs.push({
    id: queuedId, work_order_id: 'n-1', agent_principal_id: claude.registered_by_principal_id, model_profile_id: modelId,
    status: 'queued', model_evidence: 'unverified', requested_model: 'claude', input_tokens: 0, output_tokens: 0, cost_micros: 0,
    started_at: null, ended_at: null, created_at: new Date(now).toISOString(),
  })
  const calls = await mockAgents(page, data, {
    groups: [{ id: clientGroup, harness: 'claude', name: 'Client', exclusive: true, account_ids: [claude.id], project_ids: ['p-pharos'] }],
  })
  await page.route('**/api/me/permissions*', route => {
    const permissions = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    for (const grant of ['account.read', 'account.manage', 'run.create']) if (!permissions.workspace.permissions.includes(grant)) permissions.workspace.permissions.push(grant)
    return route.fulfill({ json: permissions })
  })
  await page.route('**/api/models', route => route.fulfill({ json: [{ id: modelId, slug: 'Build', harness: 'claude', family: 'anthropic', model: 'claude', effort: 'high', tier: 'standard', enabled: true }] }))
  await page.route('**/api/agent-accounts/capacity', route => {
    const path = new URL(route.request().url()).pathname
    if (path !== '/api/agent-accounts/capacity' || route.request().method() !== 'GET') return route.fallback()
    const schedule = defaultSchedule()
    return route.fulfill({ json: data.accounts.map(account => {
      const alias = account.id === studioId
      const readable = account.state === 'available' && account.last_probe_ok !== false
      return {
        account_id: account.id, schedule, windows: readable ? [windowOf()] : [], ongoing_use_approved: true,
        ...(account.quota_fingerprint ? { quota_fingerprint: account.quota_fingerprint } : {}),
        ...(account.id === claude.id ? { group_id: clientGroup, group_name: 'Client' } : {}),
        host_label: account.host_label, hosts: account.host_label ? [account.host_label] : [],
        ...(alias ? { same_quota_as: spare.id } : {}),
        routing: { rank: readable ? 1 : 0, available_slots: alias || !readable ? 0 : 1 },
      }
    }) })
  })
  await page.route('**/api/agent-pairing/computers', route => route.fulfill({ json: { computers: [] } }))
  return { data, calls, spare, claude }
}

for (const width of [1600, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`one quota, its own pool, and move ${width} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme: theme })
    const { calls, spare } = await desk(page, theme)
    await page.goto('/agents')
    const card = page.getByRole('region', { name: 'Accounts and computers' })
    // One quota, one bar: the other door names where its gauge is.
    await expect(card.locator(`[data-account="${studioId}"]`)).toContainText('Shares quota with Spare on mbp2607')
    await expect(card.locator(`[data-account="${spare.id}"] [role="meter"]`)).toHaveCount(1)
    await expect(card.locator(`[data-account="${studioId}"] [role="meter"]`)).toHaveCount(0)
    await expect(card.locator('.vendor-name', { hasText: 'Claude · Client' })).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    await card.scrollIntoViewIfNeeded()
    await shot(page, `pools-${width}-${theme}`)

    const queue = page.getByRole('region', { name: 'Runs awaiting a session' })
    await queue.scrollIntoViewIfNeeded()
    await queue.getByRole('button', { name: 'Move to…' }).click()
    await expect(queue.getByRole('button', { name: 'Client', exact: true })).toBeVisible()
    await expect(queue.getByRole('button', { name: 'Claude Max · imac0' })).toBeVisible()
    await shot(page, `move-${width}-${theme}`)
    await queue.getByRole('button', { name: 'Client', exact: true }).click()
    await expect.poll(() => calls.find(call => call.method === 'POST' && call.path === `/api/agent-accounts/runs/${queuedId}/target`)?.body).toEqual({ group_id: clientGroup })
    noPath(calls.find(call => call.path.endsWith('/target'))?.body)

    await page.goto('/settings/accounts')
    const row = page.getByRole('listitem').filter({ hasText: `aeon use codex ${spare.id}` })
    await row.getByRole('button', { name: 'Keep separate…' }).click()
    const dialog = page.getByRole('dialog', { name: 'Keep separate' })
    await expect(dialog.getByText('Only for these projects', { exact: true })).toBeVisible()
    const exclusive = dialog.getByRole('checkbox', { name: 'Other work waits rather than using this group.' })
    await expect(exclusive).toBeDisabled()
    await dialog.getByRole('checkbox', { name: /PRJ-17 · Pharos/ }).check()
    await expect(exclusive).toBeEnabled()
    await exclusive.check()
    await dialog.getByLabel('Name').fill('Night work')
    await shot(page, `separate-${width}-${theme}`)
    await dialog.getByRole('button', { name: 'Save', exact: true }).click()
    await expect(row.getByText('Night work', { exact: true })).toBeVisible()
    await expect(row.getByRole('button', { name: 'Back in the pool' })).toBeVisible()
    const created = calls.find(call => call.method === 'POST' && call.path === '/api/agent-accounts/groups')
    expect(created?.body).toMatchObject({ harness: 'codex', name: 'Night work', exclusive: true, account_ids: [spare.id], project_ids: ['p-pharos'] })
    noPath(created?.body)
    await row.scrollIntoViewIfNeeded()
    await shot(page, `grouped-${width}-${theme}`)
    await row.getByRole('button', { name: 'Back in the pool' }).click()
    const confirm = page.getByRole('dialog', { name: 'Back in the pool?' })
    await expect(confirm.getByText('Other work can use these accounts again.', { exact: true })).toBeVisible()
    await shot(page, `pool-confirm-${width}-${theme}`)
    await confirm.getByRole('button', { name: 'Back in the pool', exact: true }).click()
    await expect(row.getByRole('button', { name: 'Keep separate…' })).toBeVisible()
    expect(calls.some(call => call.method === 'DELETE' && call.path.startsWith('/api/agent-accounts/groups/'))).toBe(true)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  })
}

for (const width of [1600, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`remember for this ticket ${width} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme: theme })
    const mock = await mockStartAgent(page, { pin: { ticket_id: 'n-1', harness: 'codex', account_id: 'ac000000-0000-4000-8000-000000000002' } })
    await page.goto('/agents')
    await page.locator('button.start-agent').click()
    const dialog = page.getByRole('dialog', { name: 'Start agent', exact: true })
    await dialog.getByRole('button', { name: /PHAROS-11/ }).click()
    const remember = dialog.getByRole('checkbox', { name: 'Remember for this ticket' })
    await expect(remember).toBeChecked()
    await expect(dialog.getByLabel('Account', { exact: true })).toHaveValue(mock.spareAccountId)
    await shot(page, `remember-${width}-${theme}`)
    await remember.uncheck()
    await dialog.getByRole('button', { name: 'Queue run', exact: true }).click()
    await expect(dialog.getByRole('heading', { name: 'Queued', exact: true })).toBeVisible()
    expect(mock.calls.filter(call => call.method === 'DELETE' && call.path === '/api/agent-accounts/pins')).toHaveLength(1)
    expect(mock.pins).toEqual([])
    expect(mock.calls.some(call => call.method === 'PUT' && call.path === '/api/agent-accounts/pins')).toBe(false)
    noPath(mock.calls.find(call => call.path.includes('/pins'))?.body)
  })
}


for (const width of [1600, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`shared login keeps both group pools ${width} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme: theme })
    const { spare } = await desk(page, theme, true)
    spare.label = "Spare's account; (false) $HOME"
    await page.goto('/agents')
    const card = page.getByRole('region', { name: 'Accounts and computers' })
    const grouped = card.locator(`[data-account="${spare.id}"]`)
    const general = card.locator(`[data-account="${studioId}"]`)
    await expect(grouped).toBeVisible()
    await expect(general).toBeVisible()
    // Both doors of one login stay listed, each in its own pool; the quota has one bar.
    await expect(grouped.locator('.vendor-name')).toHaveText('Codex · Client login')
    await expect(card.locator(`[data-account="${spare.id}"] [role="meter"], [data-account="${studioId}"] [role="meter"]`)).toHaveCount(1)
    await expect(general).toContainText(`Shares quota with ${spare.label} on mbp2607`)
    await expect(grouped.locator('.identity')).toHaveText(spare.label)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    if (shots) {
      mkdirSync(shots, { recursive: true })
      // The app scrolls inside its shell. Fit the complete card for capture.
      await page.setViewportSize({ width, height: width === 390 ? 3400 : 1400 })
      await card.scrollIntoViewIfNeeded()
      await card.screenshot({ path: join(shots, `shared-pools-${width}-${theme}.png`) })
      await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    }
    await page.goto('/settings/accounts')
    const row = page.getByRole('listitem').filter({ hasText: `aeon use codex ${spare.id}` })
    await expect(row).toContainText(spare.label)
    await expect(row.locator('.use-line')).toHaveText(`aeon use codex ${spare.id}`)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    if (shots) await row.screenshot({ path: join(shots, `safe-command-${width}-${theme}.png`) })
  })
}
