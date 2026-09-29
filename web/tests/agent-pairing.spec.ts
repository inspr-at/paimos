// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { NIX_PAIR_COMMAND, SETUP_COMMAND, mockAnonymousGuide, mockPairing, pairingGuide } from './agent-pairing-fixtures'

test('Nix pairing offers the short instance command and preserves person approval', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  await mockAnonymousGuide(page)
  await page.goto('/agents/register-agent')
  await page.getByText('Nix / Home Manager', { exact: true }).click()
  await expect(page.getByText(NIX_PAIR_COMMAND, { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Copy pairing command' })).toBeVisible()
  await page.getByRole('button', { name: 'Copy pairing command' }).click()
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(NIX_PAIR_COMMAND)
  await expect(page.getByText('Confirm the folder and accounts, then enter the code below.')).toBeVisible()
  await expect(page.getByText(/Service module: macOS only/)).toBeVisible()
  await expect(page.getByText(/PATH from a reviewed release pin/)).toBeVisible()
  await expect(page.getByText(/project folder, not your home folder/)).toBeVisible()
  await page.getByText('Declarative service', { exact: true }).click()
  await expect(page.getByRole('link', { name: 'services.aeon.enable' })).toHaveAttribute('href', 'https://example.test/instance/module.nix')
  await expect(page.getByText(/needs a paired-service update/)).toBeVisible()
  await expect(page.getByText(/Entering the code does not grant access/)).toBeVisible()
  await expect(page.getByRole('button', { name: 'Connect computer', exact: true })).toHaveCount(0)
  expect(NIX_PAIR_COMMAND).not.toMatch(/mkdir|\/nix\/store|--state-root|--harness|--workspace/)
  await page.setViewportSize({ width: 390, height: 844 })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
})

test('an unconfigured instance omits the Nix module without losing the public guide', async ({ page }) => {
  await mockAnonymousGuide(page)
  const { managed_setup: _managed, ...unconfigured } = pairingGuide()
  await page.route('**/api/agent-pairing/guide', route => route.fulfill({ json: unconfigured }))
  await page.goto('/agents/register-agent')
  await expect(page.getByRole('heading', { name: 'Connect a computer' })).toBeVisible()
  await expect(page.getByText('Nix / Home Manager', { exact: true })).toHaveCount(0)
  await page.getByText('Manual and agent setup', { exact: true }).click()
  await expect(page.getByText(SETUP_COMMAND, { exact: true })).toBeVisible()
  await expect(page.getByText(/Use the owning Nix or Home Manager configuration/)).toBeVisible()
  await expect(page.getByRole('button', { name: 'Sign in to review the code' })).toBeVisible()
  await expect(page.getByRole('link', { name: /uzumaki|services.aeon/ })).toHaveCount(0)
})

test('pi pairing names the local provider, shows its SVG and connects without verification', async ({ page }) => {
  await page.clock.install({ time: new Date('2026-09-27T20:00:00.000Z') })
  await mockWork(page, fixtures())
  const reason = 'pi verification has no qualified no-tools policy for extensions and provider configuration.'
  const calls = await mockPairing(page, {
    requested_accounts: [{ account_key: 'pi-1', harness: 'pi', provider: 'anthropic', label: 'pi / anthropic (local profile)' }],
    verification_capabilities: { pi: { supported: false, policy: 'unavailable', reason } },
  })
  await page.goto('/agents/register-agent')
  await page.getByText('Manual and agent setup').click()
  await expect(page.getByText(/For pi, use \/login and \/model/)).toBeVisible()
  // `pair --url` offers every signed-in harness, pi included; no harness list to name.
  await expect(page.getByText(SETUP_COMMAND, { exact: false })).toBeVisible()
  await page.getByLabel('Pairing code').fill('123-456-789')
  await page.getByRole('button', { name: 'Look up code' }).click()
  const review = page.getByRole('region', { name: 'Pairing review' })
  await expect(review.getByLabel('Connect Pi')).toBeChecked()
  await expect(review.getByLabel('Account for Pi').locator('option:checked')).toHaveText('pi / anthropic (local profile)')
  await expect(review.locator('.harness .mark svg path')).toHaveCount(3)
  await expect(review.getByRole('checkbox', { name: 'Verify selected harnesses' })).toBeDisabled()
  await expect(review.getByText('Pi can’t be verified.')).toHaveAttribute('title', reason)

  if (process.env.PI_PAIRING_SHOTS) {
    mkdirSync(process.env.PI_PAIRING_SHOTS, { recursive: true })
    for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
      await page.emulateMedia({ colorScheme: theme })
      await page.getByRole('button', { name: 'Connect computer', exact: true }).scrollIntoViewIfNeeded()
      await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
      await page.screenshot({ path: join(process.env.PI_PAIRING_SHOTS, `pi-pairing__${width}__${theme}.png`), fullPage: true })
    }
  }
  await page.getByRole('button', { name: 'Connect computer', exact: true }).click()
  await expect.poll(() => calls.find(call => call.path.endsWith('/approve'))?.body).toEqual({
    request_digest: 'ab'.repeat(32), verification: 'connect_only', selected_account_keys: ['pi-1'],
  })
})

test('a qualified helper removes Connect only without a harness-name special case', async ({ page }) => {
  await page.clock.install({ time: new Date('2026-09-27T20:00:00.000Z') })
  await mockWork(page, fixtures())
  // Deliberately synthetic future qualification; production remains unavailable.
  const calls = await mockPairing(page, {
    verification_capabilities: {
      cursor: { supported: true, policy: 'no_tools', reason: '' },
      codex: { supported: true, policy: 'read_only', reason: '' },
    },
  })
  await page.goto('/agents/register-agent')
  await page.getByLabel('Pairing code').fill('123-456-789')
  await page.getByRole('button', { name: 'Look up code' }).click()
  const review = page.getByRole('region', { name: 'Pairing review' })
  await expect(review.getByRole('checkbox', { name: 'Verify selected harnesses' })).toBeChecked()
  await expect(review.getByRole('button', { name: 'Connect only', exact: true })).toHaveCount(0)
  await expect(review.locator('.harness-note')).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Connect computer', exact: true })).toBeEnabled()
  expect(calls.some(call => call.path.endsWith('/approve'))).toBe(false)
  await page.getByRole('button', { name: 'Connect computer', exact: true }).click()
  await expect.poll(() => calls.find(call => call.path.endsWith('/approve'))?.body).toEqual({
    request_digest: 'ab'.repeat(32),
    verification: 'one_per_harness',
    selected_account_keys: ['cursor-1', 'codex-1'],
  })
})

test('the public guide is readable without sign-in and keeps only the human code', async ({ page }) => {
  await mockAnonymousGuide(page)
  await page.goto('/agents/register-agent')
  await expect(page).toHaveURL('/agents/register-agent')
  await expect(page.getByRole('heading', { name: 'Connect a computer' })).toBeVisible()
  await expect(page.getByText(SETUP_COMMAND, { exact: false })).toBeHidden()
  await page.getByText('Manual and agent setup').click()
  await expect(page.getByText(SETUP_COMMAND, { exact: false })).toBeVisible()
  await expect(page.getByText('This AEON has not published a verified installer.')).toBeVisible()
  await expect(page.getByText(/curl\|sh|aeon\.barta\.cm/)).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Sign in to review the code' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Connect computer' })).toHaveCount(0)

  await page.getByLabel('Pairing code').fill('123456789')
  await page.getByRole('button', { name: 'Sign in to review the code' }).click()
  await expect(page).toHaveURL(/\/signin\?return=/)
  await expect.poll(() => page.evaluate(() => sessionStorage.getItem('aeon.pairingUserCode'))).toBe('123-456-789')
  const stored = await page.evaluate(() => JSON.stringify(sessionStorage))
  expect(stored).not.toMatch(/device_secret|runtime_secret|lifecycle_secret/)
})

test('a person reviews real accounts, can leave a harness out, and does not treat approval as connected', async ({ page }) => {
  const errors = watchErrors(page)
  await page.clock.install({ time: new Date('2026-09-27T20:00:00.000Z') })
  await mockWork(page, fixtures())
  const calls = await mockPairing(page)
  await page.goto('/agents/register-agent')
  await expect(page.getByRole('heading', { name: 'Connect a computer' })).toBeVisible()
  await page.getByLabel('Pairing code').fill('123-456-789')
  await page.getByRole('button', { name: 'Look up code' }).click()
  const review = page.getByRole('region', { name: 'Pairing review' })
  await expect(review.getByRole('heading', { name: 'Review this computer' })).toBeVisible()
  await expect(review.getByText('studio', { exact: true })).toBeVisible()
  await expect(review.getByText('INSPR', { exact: true })).toBeVisible()
  await expect(review.getByText('/Users/markus/work', { exact: true })).toBeVisible()
  await expect(review.getByLabel('Account for Cursor')).toHaveValue('cursor-1')
  await expect(review.getByLabel('Account for Cursor').locator('option:checked')).toHaveText('Cursor work')
  await expect(review.getByLabel('Account for Codex')).toHaveValue('codex-1')
  await expect(review.getByLabel('Account for Codex').locator('option:checked')).toHaveText('Codex work')
  const verify = review.getByRole('checkbox', { name: 'Verify selected harnesses' })
  await expect(verify).toBeDisabled()
  await expect(verify).not.toBeChecked()
  await expect(review.getByText('Cursor and Codex can’t be verified.')).toBeVisible()
  await expect(review.locator('time')).toHaveCount(0)
  await expect(review.getByText(/1 request per selected harness/)).toHaveCount(0)
  await expect(review.getByText('One at a time')).toHaveCount(0)
  // Nothing can be verified, so verification is off and the precise row reasons stay hidden; the one-line note is enough.
  await expect(review.getByText('Ask mode and an isolated config do not enforce a no-tools policy.')).toHaveCount(0)
  await expect(review.getByText('Read-only sandboxing does not isolate inherited MCP tools and startup hooks.')).toHaveCount(0)
  await expect(review.getByText(/Cursor: Cursor/)).toHaveCount(0)
  await expect(page.getByText(/15-minute|15 minutes/)).toHaveCount(0)

  await page.getByRole('checkbox', { name: 'Connect Cursor' }).uncheck()
  await expect(review.getByText('Codex can’t be verified.')).toBeVisible()
  await expect(verify).toBeDisabled()
  await page.getByRole('radio', { name: /Set ongoing limits/ }).check()
  await expect(page.getByText('Cost in micros')).toHaveCount(0)
  await expect(page.getByText('Tokens')).toHaveCount(0)
  await expect(page.getByRole('spinbutton', { name: 'Requests', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Connect computer', exact: true })).toBeEnabled()
  await page.getByRole('radio', { name: /Keep ongoing runs paused/ }).check()

  await page.getByRole('button', { name: 'Connect computer', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Setting up' })).toBeVisible()
  await expect(page.getByText('The computer reported that setup finished, and a recent probe succeeded.')).toHaveCount(0)
  const approval = calls.find(call => call.path.endsWith('/approve'))
  expect(approval?.body).toEqual({
    request_digest: 'ab'.repeat(32),
    verification: 'connect_only',
    selected_account_keys: ['codex-1'],
  })
  expect(JSON.stringify(approval?.body)).not.toMatch(/device_secret|expected_revision|allowance/)

  await page.getByRole('button', { name: 'Disconnect', exact: true }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByRole('button', { name: 'Disconnect', exact: true })).toBeVisible()
  const revoke = dialog.getByRole('button', { name: 'Revoke access now' })
  await expect(revoke).toBeVisible()
  await expect(revoke).toHaveAttribute('data-tip', /Local processes stay unconfirmed until the computer reports them/)
  await dialog.getByRole('button', { name: 'Disconnect', exact: true }).click()
  await expect.poll(() => calls.some(call => call.path.endsWith('/disconnect'))).toBe(true)
  const disconnect = calls.find(call => call.path.endsWith('/disconnect'))
  expect(disconnect?.body).toEqual({ mode: 'drain', expected_revision: 4 })
  expect(errors).toEqual([])
})

test('an expired session clears computer details and leaves the code ready for sign-in', async ({ page }) => {
  await mockWork(page, fixtures())
  await mockPairing(page)
  await page.route('**/api/agent-pairing/lookup', route => route.fulfill({ status: 401, json: { error: 'sign in required' } }))
  await page.goto('/agents/register-agent')
  await expect(page.getByRole('heading', { name: 'Connected computers' })).toBeVisible()
  await page.getByLabel('Pairing code').fill('123-456-789')
  await page.getByRole('button', { name: 'Look up code' }).click()
  await expect(page.getByRole('heading', { name: 'Connected computers' })).toHaveCount(0)
  await expect(page.getByText('studio', { exact: true })).toHaveCount(0)
  await expect(page.getByLabel('Pairing code')).toHaveValue('123-456-789')
  await expect(page.getByRole('button', { name: 'Sign in to review the code' })).toBeEnabled()
})

test('add harness is labeled from the request, and an agent cannot approve', async ({ page }) => {
  await mockWork(page, fixtures())
  await mockPairing(page)
  await page.route('**/api/me', route => {
    if (new URL(route.request().url()).pathname !== '/api/me') return route.fallback()
    return route.fulfill({ json: { principal: { id: me.id, name: 'Runtime', kind: 'agent' }, tenant: { id: 't1', name: 'INSPR Studio' } } })
  })
  await page.goto('/agents/register-agent')
  await page.getByLabel('Pairing code').fill('111-222-333')
  await page.getByRole('button', { name: 'Look up code' }).click()
  await expect(page.getByRole('heading', { name: 'Add a harness' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Add harness', exact: true })).toBeDisabled()
  await expect(page.getByText('Only a signed-in person who can manage accounts can connect this computer.')).toBeVisible()
})

test('Agents links to Connect computer', async ({ page }) => {
  await mockWork(page, fixtures())
  await page.goto('/agents')
  await expect(page.getByRole('link', { name: 'Connect computer' })).toBeVisible()
})
