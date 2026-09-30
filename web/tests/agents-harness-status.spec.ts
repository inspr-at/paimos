// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test } from '@playwright/test'
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

for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) {
  test(`per-harness readiness and recovery at ${width} ${theme}`, async ({ page }) => {
    const errors = watchErrors(page)
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
    await mockWork(page, fixtures())
    const report = computerReport()
    const calls = await mockPairing(page, {}, report)
    await page.goto('/agents')
    const computers = page.getByRole('region', { name: 'Connected computers' })
    await expect(computers).toBeVisible()
    await expect(computers.locator('.harness-report').filter({ hasText: 'Claude' })).toContainText('Dependency needs repair')
    await expect(computers.locator('.harness-report').filter({ hasText: 'Codex' })).toContainText('Ready')
    await expect(computers.locator('.status')).toHaveText('Connected')
    await expect(computers).not.toContainText('Setup unconfirmed')
    await page.getByRole('button', { name: 'Show details for studio' }).click()
    await expect(computers.locator('.harness-report').filter({ hasText: 'Claude' })).toContainText('aeon-agentd repin --harness claude')
    await expect(computers).not.toContainText('ignored daemon-supplied command')
    await expect(computers).toContainText('unconfirmed local processes')
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
    if (width === 390) {
      expect(await computers.locator('.harness-report-text').evaluateAll(items => items.every(item => item.scrollWidth <= item.clientWidth))).toBe(true)
    }
    if (process.env.HARNESS_STATUS_SHOTS) {
      mkdirSync(process.env.HARNESS_STATUS_SHOTS, { recursive: true })
      await computers.screenshot({ path: join(process.env.HARNESS_STATUS_SHOTS, `agents-harnesses-${width}-${theme}.png`), animations: 'disabled' })
    }
    report.harness_details.claude = { state: 'blocked', reason: 'repin_pending', fix: 'must not be shown' }
    await page.getByRole('button', { name: 'Refresh computers' }).click()
    await expect(computers.locator('.harness-report').filter({ hasText: 'Claude' })).toContainText('Waiting for repin')
    await expect(computers).toContainText('Retries automatically.')
    await expect(computers.locator('.harness-fix')).toHaveCount(0)
    await expect(computers).not.toContainText('Needs attention')
    if (process.env.HARNESS_STATUS_SHOTS) {
      await computers.screenshot({ path: join(process.env.HARNESS_STATUS_SHOTS, `agents-repin-pending-${width}-${theme}.png`), animations: 'disabled' })
    }
    report.harness_statuses.claude = 'ready'
    await page.getByRole('button', { name: 'Refresh computers' }).click()
    await expect(computers.locator('.harness-report').filter({ hasText: 'Claude' })).toContainText('Ready')
    await expect(computers).not.toContainText('Dependency needs repair')
    await expect(computers).not.toContainText('aeon-agentd setup status')
    expect(calls.every(call => call.method === 'GET')).toBe(true)
    expect(errors).toEqual([])
  })
}

test('offline and revoked computers do not present prior harness reports as live readiness', async ({ page }) => {
  await mockWork(page, fixtures())
  const report: Record<string, unknown> = { ...computerReport(), connectivity: 'offline' }
  await mockPairing(page, {}, report)
  await page.goto('/agents')
  const computers = page.getByRole('region', { name: 'Connected computers' })
  await expect(computers.locator('.status')).toHaveText('Offline')
  await expect(computers).toContainText('Last reported: ready')
  report.computer_state = 'revoked'
  await page.getByRole('button', { name: 'Refresh computers' }).click()
  // A revoked computer folds into the Revoked disclosure (AEON-402), with no harness reports.
  await expect(computers.locator('.status')).toHaveCount(0)
  await expect(computers.getByRole('button', { name: 'Revoked (1)' })).toBeVisible()
  await expect(computers.locator('.harness-report')).toHaveCount(0)
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
  const row = page.getByRole('region', { name: 'Connected computers' }).locator('.harness-report').filter({ hasText: 'Claude' })
  await expect(row.locator('code')).toHaveText(command!)
  await expect(row).not.toContainText('untrusted')
  if (reason === 'cli_unavailable') await expect(row).toContainText('Restore the approved executable, then retry.')
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  if (process.env.HARNESS_STATUS_SHOTS) {
    await page.getByRole('region', { name: 'Connected computers' }).screenshot({ path: join(process.env.HARNESS_STATUS_SHOTS, `agents-${reason}-${width}-${theme}.png`), animations: 'disabled' })
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
    const computers = page.getByRole('region', { name: 'Connected computers' })
    const row = computers.locator('.harness-report').filter({ hasText: 'Codex' })
    await expect(computers.locator('.status')).toHaveText('Connected')
    await expect(row).toContainText('1 of 2 accounts needs attention')
    await expect(row.locator('code')).toHaveText('aeon-agentd add-harness --harness codex')
    await expect(row).not.toContainText('untrusted')
    await page.getByRole('button', { name: 'Show details for studio' }).click()
    await expect(computers.locator('.enrollment-meta', { hasText: 'Pin changed' })).toHaveCount(1)
    await expect(computers.locator('.enrollment-meta', { hasText: 'Ready' })).toHaveCount(1)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
    if (width === 390) {
      expect(await computers.locator('.harness-report-text').evaluateAll(items => items.every(item => item.scrollWidth <= item.clientWidth))).toBe(true)
    }
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
    } },
    enrollments,
  }
  await mockPairing(page, {}, report)
  await page.goto('/agents')
  const computers = page.getByRole('region', { name: 'Connected computers' })
  const row = computers.locator('.harness-report').filter({ hasText: 'Codex' })
  await expect(row).toContainText('6 of 7 accounts need attention')
  await page.getByRole('button', { name: 'Show details for studio' }).click()
  await expect(computers.locator('.enrollment-meta', { hasText: 'Pin changed' })).toHaveCount(6)
  await expect(computers.locator('.enrollment-meta', { hasText: 'Ready' })).toHaveCount(1)
  report.harness_details.codex = {
    state: 'ready', attention_count: 6, attention_truncated: true,
    attention_accounts: ids.slice(1, 6).map(account_id => ({ account_id, reason: 'pin_drifted' })),
  }
  await page.getByRole('button', { name: 'Refresh computers' }).click()
  await expect(row).toContainText('6 of 7 accounts need attention')
  const omitted = computers.locator('li', { hasText: 'Blocked 6' })
  await expect(omitted.locator('.enrollment-meta').first()).toContainText('Needs attention')
  await expect(omitted.locator('.enrollment-meta').first()).not.toContainText('Ready')
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  expect(errors).toEqual([])
})

// AEON-347/348: per-account pin blocks and per-harness holds share one reason
// vocabulary and fix; one ready harness keeps the computer connected.
for (const [width, theme] of [[1600, 'light'], [390, 'dark']] as const) test(`every shared reason shows its fix beside a ready harness at ${width} ${theme}`, async ({ page }) => {
  const errors = watchErrors(page)
  await page.setViewportSize({ width, height: 844 })
  await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
  await mockWork(page, fixtures())
  const report = computerReport()
  report.harness_details.claude = { state: 'blocked', reason: 'pin_drifted', fix: 'untrusted' }
  await mockPairing(page, {}, report)
  await page.goto('/agents')
  const computers = page.getByRole('region', { name: 'Connected computers' })
  const row = (name: string) => computers.locator('.harness-report').filter({ hasText: name })
  await expect(computers.locator('.status')).toHaveText('Connected')
  await expect(row('Claude')).toContainText('Pin changed')
  await expect(row('Claude').locator('code')).toHaveText('aeon-agentd repin --harness claude')
  await expect(row('Codex')).toContainText('Ready')
  await expect(row('Codex').locator('code')).toHaveCount(0)
  if (process.env.HARNESS_STATUS_SHOTS) {
    await computers.screenshot({ path: join(process.env.HARNESS_STATUS_SHOTS, `agents-pin-drifted-${width}-${theme}.png`), animations: 'disabled' })
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
    await page.getByRole('button', { name: 'Refresh computers' }).click()
    await expect(row(harness)).toContainText(label)
    if (command) await expect(row(harness).locator('code')).toHaveText(command)
    else await expect(row(harness).locator('code')).toHaveCount(0)
    await expect(computers.locator('.status')).toHaveText('Connected')
    await expect(computers).not.toContainText('untrusted')
  }
  // A newer daemon's state is kept out of the legacy map but never dropped.
  report.harness_statuses = { codex: 'ready' }
  report.harness_details = { claude: { state: 'future_state', reason: 'future_reason' }, codex: { state: 'ready' } }
  await page.getByRole('button', { name: 'Refresh computers' }).click()
  await expect(row('Claude')).toContainText('Needs attention · future_reason')
  await expect(row('Claude').locator('code')).toHaveCount(0)
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  expect(errors).toEqual([])
})
