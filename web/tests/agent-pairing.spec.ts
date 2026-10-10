// SPDX-License-Identifier: AGPL-3.0-only
import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page, type Route } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { HOMEBREW_COMMAND, NIX_PAIR_COMMAND, SETUP_COMMAND, mockAnonymousGuide, mockFormulaGuide, mockPairing, pairingEnrollment, pairingGuide, pairingView } from './agent-pairing-fixtures'

test('Homebrew offers two commands and removal after draining', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  await mockAnonymousGuide(page)
  await page.goto('/agents/register-agent')
  await expect(page.getByText('Open this address on the computer')).toHaveCount(0)
  await expect(page.getByLabel('Install on this computer')).toBeVisible()
  await expect(page.getByText('Nix or Home Manager on this Mac? Choose macOS · Nix.')).toBeVisible()
  const share = page.getByText('Setting up another computer, or letting an agent do it? Share this address.')
  await expect(share).toBeVisible()
  expect(await page.evaluate(() => {
    const install = document.querySelector('.install-choice')
    const address = document.querySelector('.agent-address')
    return !!install && !!address && (install.compareDocumentPosition(address) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0
  })).toBe(true)
  await page.getByRole('button', { name: 'Copy guide address' }).click()
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe('https://aeon.example/agents/register-agent')
  await page.getByLabel('Install on this computer').selectOption('homebrew')
  const commands = HOMEBREW_COMMAND
  await expect(page.locator('.install-guide pre')).toHaveText(commands)
  await page.getByRole('button', { name: 'Copy commands', exact: true }).click()
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(commands)
  await expect(page.getByText(/approve the code below to start the service/)).toBeVisible()
  await page.getByText('Release and upgrades', { exact: true }).click()
  await expect(page.getByText(/Homebrew installs the latest INSPR release/)).toBeVisible()
  await expect(page.getByText(/tells you if this server needs a different version/)).toBeVisible()
  await expect(page.getByText(/does not drain or restart an existing daemon/)).toBeVisible()
  await page.getByText('Disconnect and uninstall', { exact: true }).click()
  await expect(page.getByText('aeon-agentd disconnect', { exact: true })).toBeVisible()
  await expect(page.getByText('brew uninstall aeon-agentd', { exact: true })).toBeVisible()
  await expect(page.getByText(/Wait for “disconnected”/)).toBeVisible()
  await expect(page.getByText(/Vendor sign-ins and project files stay/)).toBeVisible()
  expect(await page.locator('pre').allTextContents()).not.toEqual(expect.arrayContaining([expect.stringMatching(/<verified|<absolute|Cellar/)]))
  await page.getByText('Trouble?', { exact: true }).click()
  await expect(page.getByText(/usage: paimos-agentd setup\|status…/)).toBeVisible()
  await expect(page.getByText('env "$(brew --prefix)/bin/aeon-agentd" add-harness', { exact: true })).toBeVisible()
  await page.setViewportSize({ width: 390, height: 844 })
  await page.getByText('Manual and agent setup', { exact: true }).click()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
})

test('checksum installation uses the selected platform and the same pair command', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  await mockAnonymousGuide(page)
  const targets = ['darwin/arm64', 'linux/amd64'].map(platform => ({
    platform: platform.split('/')[0], arch: platform.split('/')[1],
    service: platform.startsWith('darwin') ? 'launchd-user' : 'systemd-user',
    qualification: 'candidate', artifact_url: `https://release.example/${platform}`, checksums_url: 'https://release.example/SHA256SUMS',
    command: `# checksum installer for ${platform}\ninstall-verified-release`,
  }))
  await page.route('**/api/agent-pairing/guide', route => route.fulfill({ json: { ...pairingGuide(), install_available: true, install_targets: targets } }))
  await page.goto('/agents/register-agent')
  await page.getByLabel('Install on this computer').selectOption('manual')
  await page.getByLabel('Install platform').selectOption('linux/amd64')
  await page.getByRole('button', { name: 'Copy checksum installer' }).click()
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(targets[1]!.command)
  await page.getByRole('button', { name: 'Copy pairing command' }).click()
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(`export PATH="$HOME/.local/bin:$PATH"\n${SETUP_COMMAND}`)
  await page.getByText('Disconnect and uninstall', { exact: true }).click()
  await expect(page.getByText(/Remove the aeon-agentd link in/)).toBeVisible()
  await expect(page.getByText('brew uninstall aeon-agentd', { exact: true })).toHaveCount(0)
  await page.setViewportSize({ width: 390, height: 844 })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
})

test('Nix pairing offers the short instance command and preserves person approval', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  await mockAnonymousGuide(page)
  await page.goto('/agents/register-agent')
  await page.getByLabel('Install on this computer').selectOption('nix')
  await expect(page.getByText(NIX_PAIR_COMMAND, { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Copy pairing command' })).toBeVisible()
  await page.getByRole('button', { name: 'Copy pairing command' }).click()
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(NIX_PAIR_COMMAND)
  await expect(page.getByText('Confirm the folder and accounts, then enter the code below.')).toBeVisible()
  await expect(page.getByText(/Service module: macOS only/)).toBeVisible()
  await expect(page.getByText(/from a reviewed release pin with pair/)).toBeVisible()
  await expect(page.getByText(/does not put it on PATH/)).toBeVisible()
  await expect(page.getByText(/project folder, not your home folder/)).toBeVisible()
  await page.getByText('Declarative service', { exact: true }).click()
  await expect(page.getByRole('link', { name: 'services.aeon.enable' })).toHaveAttribute('href', 'https://example.test/instance/module.nix')
  await expect(page.getByText(/needs a paired-service update/)).toBeVisible()
  await expect(page.getByText(/Entering the code does not grant access/)).toBeVisible()
  await page.getByText('Disconnect and uninstall', { exact: true }).click()
  await expect(page.getByText(/Disable the service and remove the package in your Nix/)).toBeVisible()
  await expect(page.getByRole('button', { name: 'Connect your machine', exact: true })).toHaveCount(0)
  expect(NIX_PAIR_COMMAND).not.toMatch(/mkdir|\/nix\/store|--state-root|--harness|--workspace/)
  await page.setViewportSize({ width: 390, height: 844 })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
})

test('an unconfigured instance omits the Nix module without losing the public guide', async ({ page }) => {
  await mockAnonymousGuide(page)
  const { managed_setup: _managed, ...unconfigured } = pairingGuide()
  await page.route('**/api/agent-pairing/guide', route => route.fulfill({ json: unconfigured }))
  await page.goto('/agents/register-agent')
  await expect(page.getByRole('heading', { name: 'Connect your machine' })).toBeVisible()
  await expect(page.getByText('Nix / Home Manager', { exact: true })).toHaveCount(0)
  await page.getByLabel('Install on this computer').selectOption('homebrew')
  await page.getByText('Manual and agent setup', { exact: true }).click()
  await expect(page.locator('pre').filter({ hasText: 'brew install' })).toHaveText(HOMEBREW_COMMAND)
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
  // AEON-333: the guide's pair command discovers harnesses, pi included, so it names none.
  await expect(page.locator('.install-guide pre')).toContainText("pair --url 'https://aeon.example'")
  await expect(page.locator('.install-guide pre')).not.toContainText('pi')
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
      await page.getByRole('button', { name: 'Connect your machine', exact: true }).scrollIntoViewIfNeeded()
      await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
      await page.screenshot({ path: join(process.env.PI_PAIRING_SHOTS, `pi-pairing__${width}__${theme}.png`), fullPage: true })
    }
  }
  await page.getByRole('button', { name: 'Connect your machine', exact: true }).click()
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
  await expect(page.getByRole('button', { name: 'Connect your machine', exact: true })).toBeEnabled()
  expect(calls.some(call => call.path.endsWith('/approve'))).toBe(false)
  await page.getByRole('button', { name: 'Connect your machine', exact: true }).click()
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
  await expect(page.getByRole('heading', { name: 'Connect your machine' })).toBeVisible()
  await expect(page.getByLabel('Install on this computer')).toHaveValue('manual')
  await expect(page.getByText('This AEON has not published a verified installer.').first()).toBeVisible()
  await page.getByLabel('Install on this computer').selectOption('homebrew')
  await expect(page.locator('pre').filter({ hasText: 'brew install' })).toBeVisible()
  await page.getByText('Manual and agent setup').click()
  await expect(page.getByText('This AEON has not published a verified installer.').first()).toBeVisible()
  await expect(page.getByText(/curl\|sh|aeon\.barta\.cm/)).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Sign in to review the code' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Connect your machine' })).toHaveCount(0)

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
  await expect(page.getByRole('heading', { name: 'Connect your machine' })).toBeVisible()
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
  await expect(page.getByRole('radio', { name: /Let agents use these accounts/ })).toBeChecked()
  await expect(page.getByRole('spinbutton', { name: 'Requests', exact: true })).toHaveCount(0)
  await expect(page.getByText('Set ongoing limits')).toHaveCount(0)
  await page.getByRole('radio', { name: /Keep agents paused/ }).check()

  await page.getByRole('button', { name: 'Connect your machine', exact: true }).click()
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
  await expect(page.getByText('Only a signed-in person who can manage accounts can connect or deny this computer.')).toBeVisible()
})

test('the last install choice is remembered in this browser', async ({ page }) => {
  await mockAnonymousGuide(page)
  await page.goto('/agents/register-agent')
  await page.getByLabel('Install on this computer').selectOption('nix')
  await expect.poll(() => page.evaluate(() => localStorage.getItem('aeon.pairingInstall'))).toBe('nix')
  await page.reload()
  await expect(page.getByLabel('Install on this computer')).toHaveValue('nix')
  await expect(page.getByText(NIX_PAIR_COMMAND, { exact: true })).toBeVisible()
})

test('a legacy formula hint does not hide Homebrew', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('aeon.pairingInstall', 'homebrew'))
  await mockFormulaGuide(page, false, true)
  await page.goto('/agents/register-agent')
  await expect(page.locator('option', { hasText: 'macOS · Homebrew' })).toHaveCount(1)
  await expect(page.getByText(/on its way/)).toHaveCount(0)
  await expect(page.getByLabel('Install on this computer')).toHaveValue('homebrew')
  await expect(page.locator('pre').filter({ hasText: 'brew install' })).toHaveCount(1)
  await page.getByLabel('Install on this computer').selectOption('manual')
  await expect(page.getByRole('button', { name: 'Copy checksum installer' })).toBeVisible()
})

test('direct download comes first and Homebrew remains available with an old guide', async ({ page }) => {
  await mockFormulaGuide(page, null, true)
  await page.goto('/agents/register-agent')
  await expect(page.locator('option', { hasText: 'Homebrew' })).toHaveCount(1)
  await expect(page.getByText(/on its way/)).toHaveCount(0)
  await expect(page.getByLabel('Install on this computer')).toHaveValue('manual')
  await expect(page.locator('option', { hasText: 'direct download' })).toHaveCount(1)
  await expect(page.getByRole('button', { name: 'Copy checksum installer' })).toBeVisible()
})

const LIVE_COMPUTER = '33333333-3333-4333-8333-333333333333'
const LIVE_OTHER = '99999999-9999-4999-8999-999999999999'
const LIVE_ENROLLMENTS = () => [
  pairingEnrollment('44444444-4444-4444-8444-444444444444', 'cursor-1', 'cursor', 'Cursor work'),
  pairingEnrollment('55555555-5555-4555-8555-555555555555', 'codex-1', 'codex', 'Codex work'),
]
const liveSettingUp = () => pairingView({
  state: 'approved', computer_id: LIVE_COMPUTER, computer_state: 'connected', revision: 2, setup_state: 'approved', connectivity: 'unknown', enrollments: LIVE_ENROLLMENTS(),
})
const liveConnected = () => pairingView({
  state: 'redeemed', computer_id: LIVE_COMPUTER, computer_state: 'connected', revision: 4, setup_state: 'connected', connectivity: 'online',
  last_seen_at: '2026-09-27T20:00:30.000Z', harness_statuses: { codex: 'ready' }, harness_details: { codex: { state: 'ready' } }, enrollments: LIVE_ENROLLMENTS(),
})

// A stream the test drives: it records the URLs the page opens and delivers named events with JSON data.
async function fakeEventStream(page: Page) {
  await page.addInitScript(() => {
    const opened: string[] = []
    Object.assign(window, { __streams: opened })
    class Stream extends EventTarget {
      onopen: ((event: Event) => void) | null = null
      onerror: ((event: Event) => void) | null = null
      constructor(url: string) {
        super()
        opened.push(String(url))
        window.addEventListener('test:stream', event => {
          const { name, data } = (event as CustomEvent<{ name: string; data: unknown }>).detail
          this.dispatchEvent(new MessageEvent(name, { data: JSON.stringify(data) }))
        })
      }
      close() {}
    }
    Object.assign(window, { EventSource: Stream })
  })
}
const emitStream = (page: Page, name: string, data: unknown) => page.evaluate(detail => window.dispatchEvent(new CustomEvent('test:stream', { detail })), { name, data })
const reported = (computer: string) => ({ id: 1, type: 'agent_pairing.reported', after: { computer_id: computer, setup_state: 'connected' } })

// Approve the fixture pairing and land on the review that follows the computer.
async function approveFixture(page: Page) {
  await page.goto('/agents/register-agent')
  await page.getByLabel('Pairing code').fill('123-456-789')
  await page.getByRole('button', { name: 'Look up code' }).click()
  await page.getByRole('radio', { name: /Keep agents paused/ }).check()
  await page.getByRole('button', { name: 'Connect your machine', exact: true }).click()
  const review = page.getByRole('region', { name: 'Pairing review' })
  await expect(review.getByRole('heading', { name: 'Setting up', exact: true })).toBeVisible()
  return review
}

test('a daemon connect updates the review to Connected without a reload', async ({ page }) => {
  await page.clock.install({ time: new Date('2026-09-27T20:00:00.000Z') })
  await fakeEventStream(page)
  await mockWork(page, fixtures())
  await mockPairing(page)
  let connectedNow = false
  let reads = 0
  await page.route('**/api/agent-pairing/computers**', route => {
    if (route.request().method() !== 'GET') return route.fallback()
    const path = new URL(route.request().url()).pathname
    if (path === '/api/agent-pairing/computers') return route.fulfill({ json: { computers: connectedNow ? [liveConnected()] : [] } })
    reads += 1
    return route.fulfill({ json: connectedNow ? liveConnected() : liveSettingUp() })
  })
  const review = await approveFixture(page)
  await expect(page.getByRole('heading', { name: 'Connected', exact: true })).toHaveCount(0)
  await expect(page.getByText('No computers are connected in this workspace yet.')).toBeVisible()
  // The page follows the stream in live mode: it never asks for the history.
  expect(await page.evaluate(() => (window as unknown as { __streams: string[] }).__streams)).toContain('/api/events/stream?after=latest')
  expect(await page.evaluate(() => (window as unknown as { __streams: string[] }).__streams.filter(url => !url.includes('after=latest')).length)).toBe(0)
  // Another computer's report does not wake it.
  await emitStream(page, 'agent_pairing.reported', reported(LIVE_OTHER))
  await page.clock.runFor(1000)
  expect(reads).toBe(0)
  connectedNow = true
  // A burst of its own reports reads once.
  for (let i = 0; i < 12; i++) await emitStream(page, 'agent_pairing.reported', reported(LIVE_COMPUTER))
  await page.clock.runFor(400)
  await expect(review.getByRole('heading', { name: 'Connected', exact: true })).toBeVisible()
  expect(reads).toBe(1)
  await expect(review.getByText('Ready', { exact: true })).toBeVisible()
  await expect(review.getByText('Add another harness from this computer, or set an ongoing allowance when you want more work.')).toBeVisible()
  await expect(page.getByRole('region', { name: 'Connected computers' }).locator('.status')).toHaveText('Connected')
})

test('a reset during an outstanding read that then fails does not restart polling', async ({ page }) => {
  await page.clock.install({ time: new Date('2026-09-27T20:00:00.000Z') })
  await fakeEventStream(page)
  await mockWork(page, fixtures())
  await mockPairing(page)
  const held: Route[] = []
  await page.route('**/api/agent-pairing/computers/**', route => {
    if (route.request().method() !== 'GET') return route.fallback()
    held.push(route)
  })
  const review = await approveFixture(page)
  await page.clock.runFor(5000)
  await expect.poll(() => held.length).toBe(1)
  await review.getByRole('button', { name: 'Use a different code' }).click()
  await expect(page.getByLabel('Pairing code')).toBeVisible()
  await held[0].fulfill({ status: 503, json: { error: 'unavailable', code: 'unavailable' } })
  // Half an hour of backoff and lifetime would pass: nothing reads again.
  await page.clock.runFor(30 * 60 * 1000)
  expect(held).toHaveLength(1)
  await expect(page.getByRole('alert')).toHaveCount(0)
})

for (const [name, final] of [
  ['a failed setup', () => pairingView({ state: 'redeemed', computer_id: LIVE_COMPUTER, computer_state: 'connected', revision: 3, setup_state: 'setup_failed', setup_error: 'installation_failed', connectivity: 'offline', enrollments: LIVE_ENROLLMENTS() })],
  ['a failed verification on an offline computer', () => pairingView({ state: 'redeemed', computer_id: LIVE_COMPUTER, computer_state: 'connected', revision: 3, setup_state: 'connected', connectivity: 'offline', enrollments: LIVE_ENROLLMENTS().map(item => ({ ...item, verification_state: 'failed', verification_error: 'The run did not finish.' })) })],
  ['a cancelled verification on an offline computer', () => pairingView({ state: 'redeemed', computer_id: LIVE_COMPUTER, computer_state: 'connected', revision: 3, setup_state: 'connected', connectivity: 'offline', enrollments: LIVE_ENROLLMENTS().map(item => ({ ...item, verification_state: 'cancelled' })) })],
] as const) {
  test(`${name} ends the polling`, async ({ page }) => {
    await page.clock.install({ time: new Date('2026-09-27T20:00:00.000Z') })
    await fakeEventStream(page)
    await mockWork(page, fixtures())
    await mockPairing(page)
    let reads = 0
    await page.route('**/api/agent-pairing/computers/**', route => {
      if (route.request().method() !== 'GET') return route.fallback()
      reads += 1
      return route.fulfill({ json: reads === 1 ? final() : liveSettingUp() })
    })
    await approveFixture(page)
    await page.clock.runFor(5000)
    await expect.poll(() => reads).toBe(1)
    await page.clock.runFor(10 * 60 * 1000)
    expect(reads).toBe(1)
  })
}

// The page states after approval, in light and dark, axe-clean and without sideways
// scroll at 390 px. PAIRING_LIVE_SHOTS=<dir> also writes the screenshots.
const LIVE_STATES: [string, () => Record<string, unknown>, string][] = [
  ['setting-up', () => ({ state: 'approved', computer_id: LIVE_COMPUTER, computer_state: 'connected', revision: 2, setup_state: 'approved', connectivity: 'unknown', enrollments: LIVE_ENROLLMENTS() }), 'Setting up'],
  ['connected', () => ({ state: 'redeemed', computer_id: LIVE_COMPUTER, computer_state: 'connected', revision: 4, setup_state: 'connected', connectivity: 'online', last_seen_at: '2026-09-27T20:00:30.000Z', harness_statuses: { codex: 'ready' }, harness_details: { codex: { state: 'ready' } }, enrollments: LIVE_ENROLLMENTS() }), 'Connected'],
  ['setup-failed', () => ({ state: 'redeemed', computer_id: LIVE_COMPUTER, computer_state: 'connected', revision: 3, setup_state: 'setup_failed', setup_error: 'installation_failed', connectivity: 'offline', enrollments: LIVE_ENROLLMENTS() }), ''],
  ['verification-failed', () => ({ state: 'redeemed', computer_id: LIVE_COMPUTER, computer_state: 'connected', revision: 3, setup_state: 'connected', connectivity: 'offline', enrollments: LIVE_ENROLLMENTS().map(item => ({ ...item, verification_state: 'failed', verification_error: 'The run did not finish.' })) }), ''],
]
for (const [name, state, title] of LIVE_STATES) {
  for (const colorScheme of ['light', 'dark'] as const) {
    for (const width of [1280, 390]) {
      test(`register page ${name} in ${colorScheme} at ${width}px passes axe and fits`, async ({ page }) => {
        await page.clock.install({ time: new Date('2026-09-27T20:00:00.000Z') })
        await page.emulateMedia({ colorScheme, reducedMotion: 'reduce' })
        await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
        await fakeEventStream(page)
        await mockWork(page, fixtures())
        await mockPairing(page, {}, state())
        await page.route('**/api/agent-pairing/computers/**', route => route.request().method() === 'GET' ? route.fulfill({ json: pairingView(state()) }) : route.fallback())
        const review = await approveFixture(page)
        await page.clock.runFor(5500)
        if (title) await expect(review.getByRole('heading', { name: title, exact: true })).toBeVisible()
        await expect(page.getByRole('region', { name: 'Connected computers' })).toBeVisible()
        await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
        const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).exclude('.version-coordinate').exclude('.calendar-version').analyze()
        const summary = results.violations.map(v => `${v.id} (${v.impact}): ${v.help}\n${v.nodes.slice(0, 4).map(n => `    ${n.target.join(' ')}`).join('\n')}`)
        expect(summary, summary.join('\n')).toEqual([])
        if (process.env.PAIRING_LIVE_SHOTS) {
          mkdirSync(process.env.PAIRING_LIVE_SHOTS, { recursive: true })
          await page.screenshot({ path: join(process.env.PAIRING_LIVE_SHOTS, `register-${name}__${width}__${colorScheme}.png`), fullPage: true })
        }
      })
    }
  }
}

test('Agents links to Connect your machine', async ({ page }) => {
  await mockWork(page, fixtures())
  await page.goto('/agents')
  // AEON-782: Accounts and computers is a status line; the entry is the New menu.
  await page.getByRole('button', { name: /^New/ }).click()
  await expect(page.getByRole('menuitem', { name: /Connect your machine/ })).toHaveAttribute('href', '/agents/register-agent')
  await page.keyboard.press('Escape')
  await page.setViewportSize({ width: 390, height: 844 })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
})
