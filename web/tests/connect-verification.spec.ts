// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { fixtures, me, mockWork } from './work-fixtures'
import { mockPairing } from './agent-pairing-fixtures'

const mixed = {
  requested_accounts: ['claude', 'codex', 'cursor'].map(harness => ({
    account_key: `${harness}-1`, harness, label: `${harness} work`,
  })),
  verification_capabilities: {
    claude: { supported: true, policy: 'no_tools', reason: '' },
    codex: { supported: false, policy: 'unavailable', reason: 'Codex verification is unavailable.' },
    cursor: { supported: false, policy: 'unavailable', reason: 'Cursor verification is unavailable.' },
  },
}

async function openReview(page: Page, query = '') {
  await page.goto(`/agents/register-agent${query}`)
  await page.getByLabel('Pairing code').fill('123-456-789')
  await page.getByRole('button', { name: 'Look up code' }).click()
  await expect(page.getByRole('region', { name: 'Pairing review' })).toBeVisible()
}

async function captureReview(page: Page, state: string) {
  const directory = process.env.CONNECT_VERIFICATION_SHOTS
  if (!directory) return
  mkdirSync(directory, { recursive: true })
  for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme: theme })
    await page.getByRole('region', { name: 'Pairing review' }).locator('.actions').scrollIntoViewIfNeeded()
    await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
    await page.screenshot({ path: join(directory, `${state}__${width}__${theme}.png`), fullPage: true })
  }
}

test('mixed harnesses keep Connect enabled and offer an explicit verification choice', async ({ page }) => {
  await mockWork(page, fixtures())
  const calls = await mockPairing(page, mixed)
  await openReview(page)
  await captureReview(page, 'mixed')
  const review = page.getByRole('region', { name: 'Pairing review' })
  await expect(review.locator('.verify .sub')).toHaveText('Claude can be verified. Codex and Cursor can’t be verified.')
  await expect(review.getByRole('checkbox', { name: 'Verify selected harnesses' })).toBeChecked()
  const connect = review.getByRole('button', { name: 'Connect your machine', exact: true })
  await expect(connect).toBeEnabled()
  await connect.click()
  const choice = review.getByRole('group', { name: 'Verification choice' })
  await expect(choice.getByRole('status')).toHaveText('Codex and Cursor can’t be verified — connect without verification, or leave them out.')
  await expect(choice.getByRole('button', { name: 'Connect without verification', exact: true })).toBeEnabled()
  await expect(choice.getByRole('button', { name: 'Connect without verification', exact: true })).toBeFocused()
  await expect(choice.getByRole('button', { name: 'Leave them out', exact: true })).toBeEnabled()
  expect(calls.filter(call => call.path.endsWith('/approve'))).toEqual([])
  await captureReview(page, 'choice')
  await review.getByRole('radio', { name: /Keep agents paused/ }).check()
  await choice.getByRole('button', { name: 'Connect without verification', exact: true }).click()
  await expect.poll(() => calls.find(call => call.path.endsWith('/approve'))?.body).toEqual({
    request_digest: 'ab'.repeat(32), verification: 'connect_only',
    selected_account_keys: ['claude-1', 'codex-1', 'cursor-1'],
  })
})

test('leaving unverifiable harnesses out preserves verification and requires Connect', async ({ page }) => {
  await mockWork(page, fixtures())
  const calls = await mockPairing(page, mixed)
  await openReview(page)
  const connect = page.getByRole('button', { name: 'Connect your machine', exact: true })
  await connect.click()
  await page.getByRole('button', { name: 'Leave them out', exact: true }).click()
  await expect(page.getByRole('checkbox', { name: 'Connect Claude', exact: true })).toBeChecked()
  for (const name of ['Codex', 'Cursor']) await expect(page.getByRole('checkbox', { name: `Connect ${name}`, exact: true })).not.toBeChecked()
  await expect(page.getByRole('checkbox', { name: 'Verify selected harnesses' })).toBeChecked()
  await expect(page.locator('.verify .sub').first()).toHaveText('Claude will be verified.')
  await expect(page.getByRole('group', { name: 'Verification choice' })).toHaveCount(0)
  await expect(connect).toBeFocused()
  expect(calls.filter(call => call.path.endsWith('/approve'))).toEqual([])
  await page.getByRole('radio', { name: /Keep agents paused/ }).check()
  await connect.click()
  await expect.poll(() => calls.find(call => call.path.endsWith('/approve'))?.body).toEqual({
    request_digest: 'ab'.repeat(32), verification: 'one_per_harness', selected_account_keys: ['claude-1'],
  })
})

test('changing the selection clears a pending verification choice', async ({ page }) => {
  await mockWork(page, fixtures())
  await mockPairing(page, mixed)
  await openReview(page)
  await page.getByRole('button', { name: 'Connect your machine', exact: true }).click()
  await page.getByRole('checkbox', { name: 'Verify selected harnesses' }).uncheck()
  await expect(page.getByRole('group', { name: 'Verification choice' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Connect your machine', exact: true })).toBeEnabled()
})

test('no selected harness gives a visible reason at Connect', async ({ page }) => {
  await mockWork(page, fixtures())
  await mockPairing(page)
  await openReview(page)
  for (const name of ['Codex', 'Cursor']) await page.getByRole('checkbox', { name: `Connect ${name}`, exact: true }).uncheck()
  const connect = page.getByRole('button', { name: 'Connect your machine', exact: true })
  await expect(connect).toBeDisabled()
  await expect(connect).toHaveAccessibleDescription('Choose at least one harness.')
  await expect(page.locator('.actions').getByRole('status')).toHaveText('Choose at least one harness.')
  await captureReview(page, 'no-selection')
  await page.getByRole('checkbox', { name: 'Connect Codex', exact: true }).check()
  await expect(connect).toBeEnabled()
  await expect(connect).not.toHaveAttribute('aria-describedby')
})

for (const kind of ['agent', 'reader']) test(`${kind} sees the permission reason at Connect`, async ({ page }) => {
  await mockWork(page, fixtures())
  const calls = await mockPairing(page, mixed)
  if (kind === 'agent') await page.route('**/api/me', route => route.fulfill({ json: {
    principal: { id: me.id, name: 'Runtime', kind: 'agent' }, tenant: { id: 't1', name: 'INSPR Studio' },
  } }))
  else await page.route('**/api/me/permissions*', route => route.fulfill({ json: {
    workspace: { role: { id: 'reader', key: 'reader', name: 'Reader' }, permissions: ['account.read'] }, project: null,
  } }))
  await openReview(page)
  const reason = 'Only a signed-in person who can manage accounts can connect this computer.'
  const connect = page.getByRole('button', { name: 'Connect your machine', exact: true })
  await expect(connect).toBeDisabled()
  await expect(connect).toHaveAccessibleDescription(reason)
  await expect(page.locator('.actions').getByRole('status')).toHaveText(reason)
  expect(calls.filter(call => call.path.endsWith('/approve'))).toEqual([])
})

test('a mismatched computer has a reason at Connect and a working recovery', async ({ page }) => {
  await mockWork(page, fixtures())
  await mockPairing(page)
  await openReview(page, '?computer=33333333-3333-4333-8333-333333333333')
  const connect = page.getByRole('button', { name: 'Connect your machine', exact: true })
  const reason = 'Use the matching code, or review this as a new computer.'
  await expect(connect).toBeDisabled()
  await expect(connect).toHaveAccessibleDescription(reason)
  await expect(page.locator('.actions').getByRole('status')).toHaveText(reason)
  await captureReview(page, 'target-mismatch')
  await page.getByRole('button', { name: 'Review as a new computer' }).click()
  await expect(connect).toBeEnabled()
})

test('an in-flight connection has a visible reason and sends only one approval', async ({ page }) => {
  await mockWork(page, fixtures())
  const calls = await mockPairing(page)
  let finish!: () => void
  const pending = new Promise<void>(resolve => { finish = resolve })
  await page.route('**/api/agent-pairing/requests/*/approve', async route => { await pending; await route.fallback() })
  await openReview(page)
  await page.getByRole('button', { name: 'Connect your machine', exact: true }).click()
  try {
    const connect = page.getByRole('button', { name: 'Connecting…', exact: true })
    await expect(connect).toBeDisabled()
    await expect(connect).toHaveAccessibleDescription('Connecting this computer…')
    await expect(page.locator('.actions').getByRole('status')).toHaveText('Connecting this computer…')
  } finally { finish() }
  await expect.poll(() => calls.filter(call => call.path.startsWith('/api/agent-pairing/requests/') && call.path.endsWith('/approve')).length).toBe(1)
})
