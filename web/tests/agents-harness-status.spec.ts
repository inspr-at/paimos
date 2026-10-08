// SPDX-License-Identifier: AGPL-3.0-only
// Per-harness readiness and recovery on /agents (AEON-499), as the status line
// and Needs you of AEON-782 show it: a blocked sign-in is a Needs you row with
// its state, the one fix command (never a daemon-supplied one) and any short
// hint; the grid cell carries the sign-in's own words; an offline or revoked
// computer never presents an old report as live readiness.
import { expect, test, type Page } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { mockPairing } from './agent-pairing-fixtures'
import { agentData, mockAgents } from './agents-fixtures'

function computerReport() {
  return {
    harness_statuses: { claude: 'blocked', codex: 'ready' } as Record<string, string>,
    harness_details: { claude: { state: 'blocked', reason: 'dependency_invalid', fix: 'ignored daemon-supplied command' }, codex: { state: 'ready' } } as Record<string, { state: string; reason?: string; fix?: string }>,
    enrollments: ['claude', 'codex'].map((harness, i) => ({
      account_id: i ? '55555555-5555-4555-8555-555555555555' : '44444444-4444-4444-8444-444444444444',
      account_key: `${harness}-1`, harness, label: `${harness === 'claude' ? 'Claude' : 'Codex'} work`,
      model_profile_id: '66666666-6666-4666-8666-666666666666', state: 'connected', local_cleanup: 'pending',
      verification_run_id: null, active_run_ids: [], verification_state: 'not_selected', verification_error: '',
      local_processes: 'unconfirmed', accounting_state: 'settled',
    })),
  }
}
type Report = ReturnType<typeof computerReport> & Record<string, unknown>
const section = (page: Page) => page.getByRole('region', { name: 'Accounts and computers' })
/** The Needs you row that names this vendor or account. */
const row = (page: Page, text: string) => section(page).locator('.att-row').filter({ hasText: text })
const cell = (page: Page, name: string) => section(page).getByRole('link', { name })
/** The page's poll reads the computers again (the online event refreshes every list). */
const refresh = (page: Page) => page.evaluate(() => window.dispatchEvent(new Event('online')))
const fits = (page: Page) => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)
async function setup(page: Page, report: Report) {
  const work = fixtures()
  work.preferences['ui.agents.sections'] = { dial: true, accounts: true, sessions: true, queued: true }
  await mockWork(page, work, { admin: true })
  const data = agentData({ me: me.id, now: Date.now(), projects: {}, tickets: {}, nodes: {} })
  data.approvals = []; data.messages = []
  data.accounts = (report.enrollments as { account_id: string; account_key: string; harness: string; label: string }[]).map(e => ({
    id: e.account_id, account_key: e.account_key, harness: e.harness, daemon_id: 'studio', label: e.label, plan: '', host_label: 'studio', registered_by_principal_id: me.id,
    state: 'available', max_parallel_runs: 2, last_probe_at: new Date().toISOString(), last_probe_ok: true, created_at: new Date().toISOString(), windows: [],
  })) as unknown as typeof data.accounts
  await mockAgents(page, data)
  return mockPairing(page, {}, report)
}
async function open(page: Page) {
  await page.goto('/agents')
  await expect(section(page).getByRole('table')).toBeVisible()
}

for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) {
  test(`a blocked harness is a Needs you row with its one fix, beside a ready one at ${width} ${theme}`, async ({ page }) => {
    const errors = watchErrors(page)
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
    const report = computerReport()
    const calls = await setup(page, report)
    await open(page)
    await expect(row(page, 'Claude')).toContainText('Claude needs attention on studio')
    await expect(row(page, 'Claude')).toContainText('Dependency needs repair')
    await expect(row(page, 'Claude')).toContainText('run aeon-agentd repin --harness claude')
    await expect(section(page)).not.toContainText('ignored daemon-supplied command')
    await expect(cell(page, 'Codex on studio: Ready')).toBeVisible()
    await expect(section(page).locator('.fs-sum')).toContainText('1 of 2 ready · Claude needs attention on studio')
    expect(await fits(page)).toBe(true)
    if (process.env.HARNESS_STATUS_SHOTS) {
      mkdirSync(process.env.HARNESS_STATUS_SHOTS, { recursive: true })
      await section(page).screenshot({ path: join(process.env.HARNESS_STATUS_SHOTS, `agents-harnesses-${width}-${theme}.png`), animations: 'disabled' })
    }
    // A hold that settles by itself is not a request for action: the row goes, the cell says why.
    report.harness_details.claude = { state: 'blocked', reason: 'repin_pending', fix: 'must not be shown' }
    await refresh(page)
    await expect(cell(page, 'Claude on studio: Waiting for repin')).toBeVisible()
    await expect(section(page).locator('.att-row')).toHaveCount(0)
    report.harness_statuses.claude = 'ready'
    report.harness_details.claude = { state: 'ready' }
    await refresh(page)
    await expect(cell(page, 'Claude on studio: Ready')).toBeVisible()
    await expect(section(page).getByRole('heading', { name: 'Nothing needs you' })).toBeVisible()
    expect(calls.every(call => call.method === 'GET')).toBe(true)
    expect(errors).toEqual([])
  })
}

test('offline and revoked computers do not present prior harness reports as live readiness', async ({ page }) => {
  const report: Report = { ...computerReport(), connectivity: 'offline' }
  await setup(page, report)
  await open(page)
  // Never a problem row from an old report, and never Ready on an offline computer.
  await expect(section(page).locator('.att-row')).toHaveCount(0)
  await expect(section(page).getByText('Ready', { exact: true })).toHaveCount(0)
  await expect(section(page).getByRole('link', { name: /^Claude on studio: Last reported: / })).toBeVisible()
  await expect(section(page).locator('thead').getByText('studio')).toBeVisible()
  report.computer_state = 'revoked'
  await refresh(page)
  // A revoked computer is named as removed, with no harness reports.
  await expect(section(page).locator('thead')).toContainText('Removed')
  await expect(section(page).getByText('Ready', { exact: true })).toHaveCount(0)
})

for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) for (const [reason, command] of [
  ['pin_missing', 'aeon-agentd repin --harness claude'],
  ['login_required', 'claude auth login'],
  ['cli_unavailable', 'aeon-agentd setup'],
]) test(`the specific ${reason} command is the row's fix at ${width} ${theme}`, async ({ page }) => {
  await page.setViewportSize({ width, height: 844 })
  await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
  const report = computerReport()
  if (reason === 'login_required') report.harness_statuses.claude = 'login_required'
  report.harness_details.claude = { state: report.harness_statuses.claude!, reason: reason!, fix: 'untrusted' }
  await setup(page, report)
  await open(page)
  await expect(row(page, 'Claude')).toContainText(`run ${command}`)
  await expect(row(page, 'Claude')).not.toContainText('untrusted')
  if (reason === 'cli_unavailable') await expect(row(page, 'Claude')).toContainText('Restore the approved executable, then retry.')
  expect(await fits(page)).toBe(true)
})

for (const [width, theme] of [[1600, 'light'], [390, 'dark']] as const) {
  test(`a partial block names the account that needs attention at ${width} ${theme}`, async ({ page }) => {
    const errors = watchErrors(page)
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
    const blocked = '88888888-8888-4888-8888-888888888888'
    const healthy = '55555555-5555-4555-8555-555555555555'
    await setup(page, {
      harness_statuses: { codex: 'ready' },
      harness_details: { codex: { state: 'ready', attention_accounts: [{ account_id: blocked, reason: 'pin_drifted' }], fix: 'untrusted' } },
      enrollments: [healthy, blocked].map((id, index) => ({
        account_id: id, account_key: `codex-${index}`, harness: 'codex', label: index ? 'Blocked work' : 'Healthy work',
        model_profile_id: '66666666-6666-4666-8666-666666666666', state: 'connected', local_cleanup: 'pending',
        verification_run_id: null, active_run_ids: [], verification_state: 'not_selected', verification_error: '',
        local_processes: 'unconfirmed', accounting_state: 'settled',
      })),
    } as unknown as Report)
    await open(page)
    await expect(section(page).locator('.att-row')).toHaveCount(1)
    await expect(row(page, 'Codex')).toContainText('Pin changed')
    await expect(row(page, 'Codex')).toContainText('run aeon-agentd add-harness --harness codex')
    await expect(section(page)).not.toContainText('untrusted')
    expect(await fits(page)).toBe(true)
    expect(errors).toEqual([])
  })
}

test('six blocked accounts of seven stay blocked when the list is truncated', async ({ page }) => {
  const errors = watchErrors(page)
  await page.setViewportSize({ width: 1600, height: 1000 })
  const ids = Array.from({ length: 7 }, (_, index) => `88888888-8888-4888-8888-88888888888${index}`)
  const enrollments = ids.map((account_id, index) => ({
    account_id, account_key: `codex-${index}`, harness: 'codex', label: index ? `Blocked ${index}` : 'Healthy work',
    model_profile_id: '66666666-6666-4666-8666-666666666666', state: 'connected', local_cleanup: 'pending',
    verification_run_id: null, active_run_ids: [], verification_state: 'not_selected', verification_error: '',
    local_processes: 'unconfirmed', accounting_state: 'settled',
  }))
  const report = {
    harness_statuses: { codex: 'ready' },
    harness_details: { codex: { state: 'ready', attention_count: 6, attention_accounts: ids.slice(1).map(account_id => ({ account_id, reason: 'pin_drifted' })) } as Record<string, unknown> },
    enrollments,
  } as unknown as Report
  await setup(page, report)
  await open(page)
  await expect(section(page).locator('.att-row').filter({ hasText: 'Pin changed' })).toHaveCount(6)
  await expect(section(page).locator('.fs-sum')).toContainText('1 of 7 ready')
  report.harness_details.codex = { state: 'ready', attention_count: 6, attention_truncated: true, attention_accounts: ids.slice(1, 6).map(account_id => ({ account_id, reason: 'pin_drifted' })) }
  await refresh(page)
  // The list is cut: nobody is called Ready that the report cannot vouch for.
  await expect(section(page).locator('.att-row')).toHaveCount(7)
  await expect(section(page).getByText('Ready', { exact: true })).toHaveCount(0)
  expect(await fits(page)).toBe(true)
  expect(errors).toEqual([])
})

// AEON-347/348: per-account pin blocks and per-harness holds share one reason
// vocabulary and fix; one ready harness keeps the computer online.
test('every shared reason shows its fix beside a ready harness', async ({ page }) => {
  const errors = watchErrors(page)
  await page.setViewportSize({ width: 1600, height: 1000 })
  const report = computerReport()
  report.harness_details.claude = { state: 'blocked', reason: 'pin_drifted', fix: 'untrusted' }
  await setup(page, report)
  await open(page)
  await expect(row(page, 'Claude')).toContainText('Pin changed')
  await expect(row(page, 'Claude')).toContainText('run aeon-agentd repin --harness claude')
  await expect(row(page, 'Codex')).toHaveCount(0)
  const repin = 'aeon-agentd repin --harness claude'
  const addCodex = 'aeon-agentd add-harness --harness codex'
  for (const [harness, reason, label, command] of [
    ['Claude', 'pin_missing', 'Pin missing', repin],
    ['Claude', 'pin_partial', 'Pin incomplete', repin],
    ['Claude', 'pin_invalid', 'Pin invalid', repin],
    ['Claude', 'pin_unsafe', 'Pin unsafe', repin],
    ['Claude', 'harness_failed', 'Failed to start', 'aeon-agentd setup'],
    ['Codex', 'pin_missing', 'Pin missing', addCodex],
    ['Codex', 'pin_partial', 'Pin incomplete', addCodex],
    ['Codex', 'pin_drifted', 'Pin changed', addCodex],
    ['Codex', 'pin_invalid', 'Pin invalid', addCodex],
    ['Codex', 'pin_unsafe', 'Pin unsafe', addCodex],
    ['Codex', 'dependency_invalid', 'Dependency needs repair', addCodex],
    ['Claude', 'future_reason', 'Needs attention · future_reason', ''],
  ] as const) {
    const held = harness.toLowerCase()
    const other = held === 'claude' ? 'codex' : 'claude'
    report.harness_statuses = { [held]: 'blocked', [other]: 'ready' }
    report.harness_details = { [held]: { state: 'blocked', reason, fix: 'untrusted' }, [other]: { state: 'ready' } }
    await refresh(page)
    await expect(row(page, harness)).toContainText(label)
    if (command) await expect(row(page, harness)).toContainText(`run ${command}`)
    else await expect(row(page, harness)).not.toContainText(', run ')
    await expect(section(page)).not.toContainText('untrusted')
  }
  // A newer daemon's state is kept out of the legacy map but never dropped.
  report.harness_statuses = { codex: 'ready' }
  report.harness_details = { claude: { state: 'future_state', reason: 'future_reason' }, codex: { state: 'ready' } }
  await refresh(page)
  await expect(row(page, 'Claude')).toContainText('Needs attention · future_reason')
  expect(await fits(page)).toBe(true)
  expect(errors).toEqual([])
})

for (const [width, theme] of [[1600, 'light'], [390, 'dark']] as const) {
  test(`pairing blockers and verification refusal are named at ${width} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
    const report = computerReport()
    report.harness_statuses = { claude: 'ready', codex: 'blocked' }
    report.harness_details = { claude: { state: 'ready' }, codex: { state: 'blocked', reason: 'binding_missing' } }
    Object.assign(report.enrollments[0]!, { verification_state: 'failed', verification_error: 'verification_unavailable', verification_reason: 'adapter_unsupported' })
    await setup(page, report)
    await open(page)
    await expect(row(page, 'Codex')).toContainText("Codex was approved but isn't set up on this computer")
    await expect(row(page, 'Claude')).toContainText("Claude verification couldn't run on this computer")
    await expect(row(page, 'Claude')).toContainText('installed adapter cannot enforce safe verification')
    await expect(row(page, 'Codex')).toContainText('run aeon-agentd add-harness --harness codex')
    expect(await fits(page)).toBe(true)
  })
}

for (const width of [1600, 390]) test(`a computer that needs an update says so at ${width}`, async ({ page }) => {
  await page.setViewportSize({ width, height: 844 })
  const advice = 'Update aeon-agentd to at least 261001072608.0.0.'
  const report = { ...computerReport(), harness_statuses: { claude: 'ready', codex: 'ready' }, harness_details: { claude: { state: 'ready' }, codex: { state: 'ready' } }, agent_compatibility: { status: 'compatible', action: '' } }
  await setup(page, report)
  await open(page)
  await expect(section(page).locator('.att-row')).toHaveCount(0)
  for (const status of ['update_required', 'protocol_mismatch']) {
    Object.assign(report.agent_compatibility, { status, action: advice })
    await refresh(page)
    await expect(row(page, 'studio needs an update')).toContainText(advice)
    await expect(section(page).locator('.fs-sum')).toContainText('studio needs an update')
  }
  Object.assign(report.agent_compatibility, { status: 'unknown', action: '' })
  await refresh(page)
  await expect(section(page).locator('.att-row')).toHaveCount(0)
  expect(await fits(page)).toBe(true)
})
