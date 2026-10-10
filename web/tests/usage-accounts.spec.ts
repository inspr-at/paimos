// SPDX-License-Identifier: AGPL-3.0-only
import { mkdir } from 'node:fs/promises'
import { test, expect, type Page } from '@playwright/test'
import { setupUsage, USAGE_TZ } from './usage-fixtures'
import { ACCOUNTS, capacityWorld } from './capacity-fixtures'
import { pairingEnrollment } from './agent-pairing-fixtures'
import { watchErrors } from './work-fixtures'

test.use({ timezoneId: USAGE_TZ })
const cause = 'the default Claude profile is not private (requires mode 0700)'
const fix = 'chmod 700 "$HOME/.claude"'
const queued = 'c0000000-0000-4000-8000-000000000099'

async function setup(page: Page, theme: 'light' | 'dark', extra = false) {
  await setupUsage(page, { theme, variant: 'empty' })
  const world = capacityWorld()
  const keep = new Set([ACCOUNTS.main, ACCOUNTS.claude, ACCOUNTS.cursor])
  const accounts = world.accounts.filter(a => keep.has(a.id))
  accounts.find(a => a.id === ACCOUNTS.main)!.label = 'admin@example.com'
  accounts.find(a => a.id === ACCOUNTS.claude)!.label = 'markus.barta@example.com'
  accounts.find(a => a.id === ACCOUNTS.claude)!.last_probe_ok = false
  accounts.find(a => a.id === ACCOUNTS.cursor)!.label = 'markus@barta.com'
  const computer = {
    ...world.computers[0],
    harness_statuses: { codex: 'ready', cursor: 'ready', claude: 'blocked' },
    harness_details: { claude: { state: 'blocked', reason: 'probe_failed', reason_detail: cause, fix: { kind: 'permissions', command: fix } } },
    enrollments: accounts.map(a => ({ ...pairingEnrollment(a.id, a.account_key, a.harness, a.label), verification_state: a.harness === 'claude' ? 'queued' : 'completed' })),
  }
  if (extra) computer.enrollments.push({ ...pairingEnrollment(queued, 'codex-pending', 'codex', 'Pending Codex account'), verification_state: 'queued' })
  // Claude is pairing-only: no account list entry or capacity projection yet.
  await page.route('**/api/agent-accounts', route => route.fulfill({ json: accounts.filter(a => a.id !== ACCOUNTS.claude) }))
  const projection = world.handle('/api/agent-accounts/capacity', 'GET', undefined)!.json as { account_id: string }[]
  await page.route('**/api/agent-accounts/capacity', route => route.fulfill({ json: projection.filter(c => c.account_id === ACCOUNTS.main) }))
  await page.route('**/api/agent-pairing/computers', route => route.fulfill({ json: { computers: [computer] } }))
}

for (const width of [1440, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`configured account states at ${width}px in ${theme}`, async ({ page }) => {
    const errors = watchErrors(page)
    await page.setViewportSize({ width, height: 1000 })
    await setup(page, theme)
    await page.goto('/agents/usage')
    const band = page.getByRole('region', { name: 'Capacity' })
    await expect(band.locator('.account-line')).toHaveCount(3)
    await expect(band.locator('.meta')).toHaveText('2 of 3 accounts ready')
    await expect(band.locator('[data-pool="codex"] .left')).toContainText('42%')
    await expect(band.locator(`[data-account="${ACCOUNTS.main}"]`)).toContainText('admin@example.com')
    const cursor = band.locator('[data-pool="cursor"]')
    await expect(cursor).toContainText('Usage unknown · reserve not enforceable')
    await expect(cursor.getByRole('meter')).toHaveCount(0)
    await expect(cursor).not.toContainText('No reading yet')
    const claude = band.locator(`[data-account="${ACCOUNTS.claude}"]`)
    await expect(claude).toHaveAttribute('data-ready', 'attention')
    await expect(claude).toContainText(`Claude: sign-in check failed: ${cause}.`)
    await expect(claude).toContainText(fix)
    await expect(claude).not.toContainText('Ready')
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true)
    await mkdir('test-results/aeon-623', { recursive: true })
    await page.screenshot({ path: `test-results/aeon-623/usage-${width}-${theme}.png`, fullPage: true })
    expect(errors).toEqual([])
  })
}

test('another configured account in the same pool remains visible while verification is queued', async ({ page }) => {
  await setup(page, 'light', true)
  await page.goto('/agents/usage')
  const band = page.getByRole('region', { name: 'Capacity' })
  await expect(band.locator('.account-line')).toHaveCount(4)
  await expect(band.locator(`[data-account="${queued}"]`)).toContainText('Verification queued')
  await expect(band.locator(`[data-account="${queued}"]`)).toHaveAttribute('data-ready', 'waiting')
  await expect(band.locator(`[data-account="${ACCOUNTS.main}"] .account-reading`)).toContainText('42% left')
  await expect(band.locator(`[data-account="${ACCOUNTS.main}"]`)).not.toContainText(fix)
})


test('a failed initial computer read reports incomplete inventory instead of all ready or empty', async ({ page }) => {
  await setup(page, 'light')
  await page.route('**/api/agent-pairing/computers', route => route.fulfill({ status: 503, json: { error: 'temporarily unavailable' } }))
  await page.goto('/agents/usage')
  const band = page.getByRole('region', { name: 'Capacity' })
  await expect(band.getByRole('alert')).toContainText('Some configured accounts may be missing')
  await expect(band.locator('.meta')).toHaveText('Account status incomplete')
  await expect(band.locator('.account-state')).toHaveText(['Status not checked', 'Status not checked'])
  await expect(band).not.toContainText('No accounts yet')
  await page.route('**/api/agent-accounts', route => route.fulfill({ json: [] }))
  await band.getByRole('button', { name: 'Try again' }).click()
  await expect(band.locator('.account-line')).toHaveCount(0)
  await expect(band.getByRole('alert')).toBeVisible()
  await expect(band).not.toContainText('No accounts yet')
})

test('a failed refresh retains pairing-only accounts and names their status as last known', async ({ page }) => {
  await setup(page, 'light')
  await page.goto('/agents/usage')
  const band = page.getByRole('region', { name: 'Capacity' })
  await expect(band.locator('.account-line')).toHaveCount(3)
  await page.route('**/api/agent-pairing/computers', route => route.fulfill({ status: 503, json: { error: 'temporarily unavailable' } }))
  await page.evaluate(() => window.dispatchEvent(new Event('online')))
  await expect(band.getByRole('alert')).toContainText('Showing last known account status')
  await expect(band.locator('.account-line')).toHaveCount(3)
  await expect(band.locator(`[data-account="${ACCOUNTS.claude}"]`)).toContainText(cause)
  await expect(band.locator(`[data-account="${ACCOUNTS.claude}"] .account-state`)).toHaveText('Last known: Sign-in check failed')
  await expect(band.locator('.meta')).toHaveText('Account status incomplete')
})

test('explicit pairing 403 keeps accessible accounts usable without a false service error', async ({ page }) => {
  await setup(page, 'light')
  await page.route('**/api/agent-pairing/computers', route => route.fulfill({ status: 403, json: { error: 'forbidden' } }))
  await page.goto('/agents/usage')
  const band = page.getByRole('region', { name: 'Capacity' })
  await expect(band.locator('.account-line')).toHaveCount(2)
  await expect(band.getByRole('alert')).toHaveCount(0)
  await expect(band.locator('.meta')).toHaveText('2 of 2 accounts ready')
  await expect(band.locator('[data-pool="codex"] .left')).toContainText('42%')
})

test('the first computer read settles before Usage claims the complete account count', async ({ page }) => {
  await setup(page, 'light')
  let release!: () => void
  const pending = new Promise<void>(resolve => { release = resolve })
  let requested!: () => void
  const started = new Promise<void>(resolve => { requested = resolve })
  await page.route('**/api/agent-pairing/computers', async route => {
    requested()
    await pending
    await route.fallback()
  })
  await page.goto('/agents/usage')
  await started
  const band = page.getByRole('region', { name: 'Capacity' })
  await expect(band.locator('.skeleton-row')).toHaveCount(2)
  await expect(band.locator('.meta')).toHaveCount(0)
  await expect(band).not.toContainText('No accounts yet')
  release()
  await expect(band.locator('.account-line')).toHaveCount(3)
  await expect(band.locator('.meta')).toHaveText('2 of 3 accounts ready')
})
