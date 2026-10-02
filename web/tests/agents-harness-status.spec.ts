// SPDX-License-Identifier: AGPL-3.0-only
// Per-harness readiness and recovery on /agents, on the computer-first cards of
// AEON-499: each account on a computer carries its harness's state, the one
// fix command (never a daemon-supplied one) and any short hint; an offline or
// revoked computer never presents an old report as live readiness.
import { expect, test, type Page } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { mockPairing } from './agent-pairing-fixtures'

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
const studio = (page: Page) => page.getByRole('region', { name: 'Computer studio' })
const account = (page: Page, vendor: string) => studio(page).getByRole('row').filter({ hasText: vendor })
/** The page's poll reads the computers again (the online event refreshes every list). */
const refresh = (page: Page) => page.evaluate(() => window.dispatchEvent(new Event('online')))
const fits = (page: Page) => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)

for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) {
  test(`per-harness readiness and recovery at ${width} ${theme}`, async ({ page }) => {
    const errors = watchErrors(page)
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
    await mockWork(page, fixtures())
    const report = computerReport()
    const calls = await mockPairing(page, {}, report)
    await page.goto('/agents')
    await expect(studio(page)).toBeVisible()
    await expect(account(page, 'Claude')).toContainText('Dependency needs repair')
    await expect(account(page, 'Codex')).toContainText('Ready')
    await expect(studio(page).getByText(/^Online · seen /)).toBeVisible()
    await expect(studio(page)).not.toContainText('Setup unconfirmed')
    await expect(account(page, 'Claude').locator('.fix')).toHaveText('aeon-agentd repin --harness claude')
    await expect(studio(page)).not.toContainText('ignored daemon-supplied command')
    expect(await fits(page)).toBe(true)
    if (process.env.HARNESS_STATUS_SHOTS) {
      mkdirSync(process.env.HARNESS_STATUS_SHOTS, { recursive: true })
      await studio(page).screenshot({ path: join(process.env.HARNESS_STATUS_SHOTS, `agents-harnesses-${width}-${theme}.png`), animations: 'disabled' })
    }
    report.harness_details.claude = { state: 'blocked', reason: 'repin_pending', fix: 'must not be shown' }
    await refresh(page)
    await expect(account(page, 'Claude')).toContainText('Waiting for repin')
    await expect(account(page, 'Claude')).toContainText('Retries automatically.')
    await expect(studio(page).locator('.fix')).toHaveCount(0)
    await expect(studio(page)).not.toContainText('Needs attention')
    report.harness_statuses.claude = 'ready'
    await refresh(page)
    await expect(account(page, 'Claude')).toContainText('Ready')
    await expect(studio(page)).not.toContainText('Dependency needs repair')
    await expect(studio(page)).not.toContainText('aeon-agentd setup status')
    expect(calls.every(call => call.method === 'GET')).toBe(true)
    expect(errors).toEqual([])
  })
}

test('offline and revoked computers do not present prior harness reports as live readiness', async ({ page }) => {
  await mockWork(page, fixtures())
  const report: Record<string, unknown> = { ...computerReport(), connectivity: 'offline' }
  await mockPairing(page, {}, report)
  await page.goto('/agents')
  await expect(studio(page).getByText(/^Offline/).first()).toBeVisible()
  await expect(studio(page).getByRole('status')).toContainText('aeon-agentd status')
  // Never "Offline" beside "Last reported: ready", and never Ready on an offline computer.
  await expect(studio(page)).not.toContainText('Last reported')
  await expect(studio(page).getByText('Ready', { exact: true })).toHaveCount(0)
  await expect(account(page, 'Claude')).toContainText('Paused · computer offline')
  await expect(account(page, 'Codex')).toContainText('Paused · computer offline')
  report.computer_state = 'revoked'
  await refresh(page)
  // A revoked computer folds into the Revoked disclosure (AEON-402), with no harness reports.
  await expect(studio(page)).toHaveCount(0)
  await expect(page.getByRole('region', { name: 'Revoked computers' }).getByRole('button', { name: 'Revoked computers · 1' })).toBeVisible()
})

for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) for (const [reason, command] of [
  ['pin_missing', 'aeon-agentd repin --harness claude'],
  ['login_required', 'claude auth login'],
  ['cli_unavailable', 'aeon-agentd setup'],
]) test(`harness row shows the specific ${reason} command at ${width} ${theme}`, async ({ page }) => {
  await page.setViewportSize({ width, height: 844 })
  await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
  await mockWork(page, fixtures())
  const report = computerReport()
  if (reason === 'login_required') report.harness_statuses.claude = 'login_required'
  report.harness_details.claude = { state: report.harness_statuses.claude!, reason: reason!, fix: 'untrusted' }
  await mockPairing(page, {}, report)
  await page.goto('/agents')
  const row = account(page, 'Claude')
  await expect(row.locator('.fix')).toHaveText(command!)
  await expect(row).not.toContainText('untrusted')
  if (reason === 'cli_unavailable') await expect(row).toContainText('Restore the approved executable, then retry.')
  expect(await fits(page)).toBe(true)
  if (process.env.HARNESS_STATUS_SHOTS) {
    await studio(page).screenshot({ path: join(process.env.HARNESS_STATUS_SHOTS, `agents-${reason}-${width}-${theme}.png`), animations: 'disabled' })
  }
})

for (const [width, theme] of [[1600, 'light'], [390, 'dark']] as const) {
  test(`partial block names the account that needs attention at ${width} ${theme}`, async ({ page }) => {
    const errors = watchErrors(page)
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
    await mockWork(page, fixtures())
    const blocked = '88888888-8888-4888-8888-888888888888'
    const healthy = '55555555-5555-4555-8555-555555555555'
    await mockPairing(page, {}, {
      harness_statuses: { codex: 'ready' },
      harness_details: { codex: { state: 'ready', attention_accounts: [{ account_id: blocked, reason: 'pin_drifted' }], fix: 'untrusted' } },
      enrollments: [healthy, blocked].map((id, index) => ({
        account_id: id, account_key: `codex-${index}`, harness: 'codex', label: index ? 'Blocked work' : 'Healthy work',
        model_profile_id: '66666666-6666-4666-8666-666666666666', state: 'connected', local_cleanup: 'pending',
        verification_run_id: null, active_run_ids: [], verification_state: 'not_selected', verification_error: '',
        local_processes: 'unconfirmed', accounting_state: 'settled',
      })),
    })
    await page.goto('/agents')
    await expect(studio(page).getByText(/^Online · seen /)).toBeVisible()
    await expect(account(page, 'Blocked work')).toContainText('Pin changed')
    await expect(account(page, 'Blocked work').locator('.fix')).toHaveText('aeon-agentd add-harness --harness codex')
    await expect(account(page, 'Healthy work')).toContainText('Ready')
    await expect(account(page, 'Healthy work').locator('.fix')).toHaveCount(0)
    await expect(studio(page)).not.toContainText('untrusted')
    expect(await fits(page)).toBe(true)
    expect(errors).toEqual([])
  })
}

test('six blocked accounts of seven stay blocked when the list is truncated', async ({ page }) => {
  const errors = watchErrors(page)
  await page.setViewportSize({ width: 1600, height: 1000 })
  await page.emulateMedia({ colorScheme: 'light', reducedMotion: 'reduce' })
  await mockWork(page, fixtures())
  const ids = Array.from({ length: 7 }, (_, index) => `88888888-8888-4888-8888-88888888888${index}`)
  const enrollments = ids.map((account_id, index) => ({
    account_id, account_key: `codex-${index}`, harness: 'codex', label: index ? `Blocked ${index}` : 'Healthy work',
    model_profile_id: '66666666-6666-4666-8666-666666666666', state: 'connected', local_cleanup: 'pending',
    verification_run_id: null, active_run_ids: [], verification_state: 'not_selected', verification_error: '',
    local_processes: 'unconfirmed', accounting_state: 'settled',
  }))
  const report = {
    harness_statuses: { codex: 'ready' },
    harness_details: { codex: {
      state: 'ready', attention_count: 6,
      attention_accounts: ids.slice(1).map(account_id => ({ account_id, reason: 'pin_drifted' })),
    } as Record<string, unknown> },
    enrollments,
  }
  await mockPairing(page, {}, report)
  await page.goto('/agents')
  await expect(studio(page).getByRole('row').filter({ hasText: 'Pin changed' })).toHaveCount(6)
  await expect(account(page, 'Healthy work')).toContainText('Ready')
  report.harness_details.codex = {
    state: 'ready', attention_count: 6, attention_truncated: true,
    attention_accounts: ids.slice(1, 6).map(account_id => ({ account_id, reason: 'pin_drifted' })),
  }
  await refresh(page)
  await expect(account(page, 'Blocked 6')).toContainText('Needs attention')
  await expect(account(page, 'Blocked 6')).not.toContainText('Ready')
  await expect(account(page, 'Healthy work')).toContainText('Needs attention')
  expect(await fits(page)).toBe(true)
  expect(errors).toEqual([])
})

// AEON-347/348: per-account pin blocks and per-harness holds share one reason
// vocabulary and fix; one ready harness keeps the computer online.
for (const [width, theme] of [[1600, 'light'], [390, 'dark']] as const) test(`every shared reason shows its fix beside a ready harness at ${width} ${theme}`, async ({ page }) => {
  const errors = watchErrors(page)
  await page.setViewportSize({ width, height: 844 })
  await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
  await mockWork(page, fixtures())
  const report = computerReport()
  report.harness_details.claude = { state: 'blocked', reason: 'pin_drifted', fix: 'untrusted' }
  await mockPairing(page, {}, report)
  await page.goto('/agents')
  await expect(studio(page).getByText(/^Online · seen /)).toBeVisible()
  await expect(account(page, 'Claude')).toContainText('Pin changed')
  await expect(account(page, 'Claude').locator('.fix')).toHaveText('aeon-agentd repin --harness claude')
  await expect(account(page, 'Codex')).toContainText('Ready')
  await expect(account(page, 'Codex').locator('.fix')).toHaveCount(0)
  if (process.env.HARNESS_STATUS_SHOTS) {
    await studio(page).screenshot({ path: join(process.env.HARNESS_STATUS_SHOTS, `agents-pin-drifted-${width}-${theme}.png`), animations: 'disabled' })
  }
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
    await expect(account(page, harness)).toContainText(label)
    if (command) await expect(account(page, harness).locator('.fix')).toHaveText(command)
    else await expect(account(page, harness).locator('.fix')).toHaveCount(0)
    await expect(studio(page).getByText(/^Online · seen /)).toBeVisible()
    await expect(studio(page)).not.toContainText('untrusted')
  }
  // A newer daemon's state is kept out of the legacy map but never dropped.
  report.harness_statuses = { codex: 'ready' }
  report.harness_details = { claude: { state: 'future_state', reason: 'future_reason' }, codex: { state: 'ready' } }
  await refresh(page)
  await expect(account(page, 'Claude')).toContainText('Needs attention · future_reason')
  await expect(account(page, 'Claude').locator('.fix')).toHaveCount(0)
  expect(await fits(page)).toBe(true)
  expect(errors).toEqual([])
})

for (const [width, theme] of [[1600, 'light'], [390, 'dark']] as const) {
  test(`pairing blockers and verification refusal are named at ${width} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
    await mockWork(page, fixtures())
    const report = computerReport()
    report.harness_statuses = { claude: 'ready', codex: 'blocked' }
    report.harness_details = { claude: { state: 'ready' }, codex: { state: 'blocked', reason: 'binding_missing' } }
    Object.assign(report.enrollments[0]!, { verification_state: 'failed', verification_error: 'verification_unavailable', verification_reason: 'adapter_unsupported' })
    await mockPairing(page, {}, report)
    await page.goto('/agents')
    await expect(account(page, 'Codex')).toContainText("Codex was approved but isn't set up on this computer")
    await expect(account(page, 'Claude')).toContainText("Claude verification couldn't run on this computer")
    await expect(account(page, 'Claude')).toContainText('installed adapter cannot enforce safe verification')
    await expect(account(page, 'Codex').locator('.fix')).toHaveText('aeon-agentd add-harness --harness codex')
    expect(await fits(page)).toBe(true)
    if (process.env.HARNESS_STATUS_SHOTS) await studio(page).screenshot({ path: join(process.env.HARNESS_STATUS_SHOTS, `pairing-reasons-${width}-${theme}.png`), animations: 'disabled' })
  })
}

for (const width of [1600, 390]) test(`computer compatibility advice at ${width}`, async ({ page }) => {
  await page.setViewportSize({ width, height: 844 })
  await mockWork(page, fixtures())
  const advice = 'Update aeon-agentd to at least 261001072608.0.0.'
  const report = { ...computerReport(), agent_compatibility: { status: 'compatible', action: '' } }
  await mockPairing(page, {}, report)
  await page.goto('/agents')
  await expect(studio(page)).toBeVisible()
  await expect(studio(page).locator('.c-advice')).toHaveCount(0)
  for (const status of ['update_required', 'protocol_mismatch']) {
    Object.assign(report.agent_compatibility, { status, action: advice })
    await refresh(page)
    await expect(studio(page).locator('.c-advice')).toHaveText(advice)
    await expect(studio(page).getByText(/^Online · seen /)).toBeVisible()
  }
  Object.assign(report.agent_compatibility, { status: 'unknown', action: '' })
  await refresh(page)
  await expect(studio(page).locator('.c-advice')).toHaveCount(0)
  expect(await fits(page)).toBe(true)
})
