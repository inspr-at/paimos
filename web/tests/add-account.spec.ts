// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Locator, type Page } from '@playwright/test'
import { mockEffectivePermissions } from './authz-fixtures'
import { agentData, mockAgents, type AgentWorld } from './agents-fixtures'
import { capacityWorld, NOW, TZ } from './capacity-fixtures'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'

test.use({ timezoneId: TZ })

const world: AgentWorld = {
  me: me.id,
  projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' },
  tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' },
  nodes: {
    'p-pharos': { key: 'PRJ-17', title: 'Pharos' }, 'p-aeon': { key: 'PRJ-35', title: 'Aeon' }, 'p-frozen': { key: 'PRJ-26', title: 'Studio infrastructure' },
    'n-1': { key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning' }, 'n-2': { key: 'PHAROS-12', title: 'Add an Oracle Cloud connector' },
    'n-a1': { key: 'AEON-1', title: 'Aeon foundation' }, 'n-5': { key: 'PHAROS-15', title: 'Beacon health probes' }, 'n-6': { key: 'PHAROS-16', title: 'Retire the old dashboard' },
  },
}

type Computers = 'ready' | 'empty' | 'error' | 'hold'
const homebrew = (harness: string) => `env "$(brew --prefix)/bin/aeon-agentd" add-harness --harness ${harness}`
const nix = (harness: string) => `env "$HOME/.nix-profile/bin/aeon-agentd" add-harness --harness ${harness}`
const direct = (harness: string) => `env "$HOME/.local/bin/aeon-agentd" add-harness --harness ${harness}`
const CALM = 'Sign-in happens on the machine; the password never reaches AEON.'
const MEMBER = 'Only account owners with management permission can act. This view shows the status available to you.'
const CREDENTIAL = 'Every sign-in stays on its computer. PAIMOS stores status and timestamps, never the vendor credential.'
const CONNECT = 'Connect a computer'
const accountsList = (page: Page) => page.getByRole('region', { name: /^Accounts \d+$/ })

async function setup(page: Page, options: { manage?: boolean; desk?: boolean; computers?: Computers } = {}) {
  if (options.desk) await page.clock.setSystemTime(NOW)
  await mockWork(page, fixtures(), { admin: true })
  const data = agentData(world)
  const capacity = capacityWorld()
  if (options.desk) {
    data.accounts = capacity.accounts as unknown as typeof data.accounts
    data.approvals = data.approvals.filter(item => item.decision)
    data.messages = data.messages.filter(item => !item.is_action_request)
  }
  await mockAgents(page, data, options.desk ? { capacity } : {})
  let mode: Computers = options.computers ?? 'ready'
  let releaseHold = () => {}
  const held = new Promise<void>(resolve => { releaseHold = resolve })
  if (!options.desk) {
    await page.route('**/api/agent-pairing/computers*', async route => {
      if (route.request().method() !== 'GET') return route.fallback()
      if (mode === 'hold') await held
      if (mode === 'error') return route.fulfill({ status: 500, json: { error: 'down' } })
      if (mode === 'empty' || mode === 'hold') return route.fulfill({ json: { computers: [] } })
      return route.fulfill({ json: { computers: capacity.computers } })
    })
  }
  await page.route('**/api/me/permissions*', route => {
    const answer = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    const extra = ['run.create', 'run.read', 'models.read', 'work_orders.read', 'account.read']
    if (options.manage !== false) extra.push('account.manage')
    answer.workspace.permissions = [...answer.workspace.permissions, ...extra]
    return route.fulfill({ json: answer })
  })
  return { data, release: releaseHold, setComputers(next: Computers) { mode = next } }
}

async function shoot(page: Page, name: string, locator: Locator) {
  const dir = process.env.ADD_ACCOUNT_SHOTS
  if (!dir) return
  mkdirSync(dir, { recursive: true })
  const previous = page.viewportSize()
  for (const theme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme: theme })
    await page.evaluate(choice => { document.documentElement.dataset.theme = choice }, theme)
    for (const width of [1600, 390] as const) {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
      await locator.scrollIntoViewIfNeeded()
      await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1)).toBe(true)
      await locator.screenshot({ path: join(dir, `${name}-${width}-${theme}.png`) })
    }
  }
  if (previous) await page.setViewportSize(previous)
  await page.emulateMedia({ colorScheme: 'light' })
  await page.evaluate(() => { document.documentElement.dataset.theme = 'light' })
}

const noBar = (page: Page) => page.locator('#add-account-panel, #add-account-panel *').evaluateAll(els => els.filter(el => {
  const style = getComputedStyle(el)
  return ['Left', 'Top'].some(side => parseFloat(style[`border${side}Width` as 'borderLeftWidth']) >= 3 && style[`border${side}Style` as 'borderLeftStyle'] !== 'none')
}).length)

test('the steps name the machine, the known sign-in, and a path-proof add-harness', async ({ page }) => {
  const errors = watchErrors(page)
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write'])
  await setup(page)
  await page.goto('/settings/accounts')
  const accounts = accountsList(page)
  await expect(accounts).toContainText('Claude Max')
  await expect(page.getByText(CALM)).toHaveCount(0)
  const open = page.getByRole('button', { name: 'Add an account' })
  await open.click()
  const panel = page.getByRole('region', { name: 'Add an account' })
  await expect(panel).toBeVisible()
  await expect(open).toHaveAttribute('aria-expanded', 'true')
  await expect(page.getByText(CALM)).toHaveCount(1)
  await expect(page.getByRole('combobox', { name: 'Machine' }).locator('option')).toHaveText(['mbp2607', 'studio'])
  const harness = page.getByRole('combobox', { name: 'Harness' })
  await expect(harness.locator('option')).toHaveText(['Pi', 'Another Codex account', 'Another Claude account', 'Another Grok account', 'Another Cursor account'])
  await expect(page.getByRole('combobox', { name: 'Installed with' }).locator('option')).toHaveText(['Homebrew', 'Nix profile', 'Direct download'])
  await expect(page.getByLabel('Sign-in step')).toHaveValue('For pi, use /login and /model in pi first.')
  await expect(page.getByLabel('Enroll command')).toHaveValue(homebrew('pi'))

  await page.getByRole('combobox', { name: 'Machine' }).selectOption({ label: 'studio' })
  await expect(harness.locator('option').first()).toHaveText('Claude')
  await expect(page.getByLabel('Sign-in command')).toHaveValue('claude /login')
  await page.getByRole('combobox', { name: 'Machine' }).selectOption({ label: 'mbp2607' })

  await harness.selectOption({ label: 'Another Grok account' })
  await expect(page.getByLabel('Sign-in step')).toHaveValue("Sign in with Grok's CLI.")
  await expect(panel).not.toContainText('grok login')
  await harness.selectOption({ label: 'Another Cursor account' })
  await expect(page.getByLabel('Sign-in command')).toHaveValue('cursor-agent login')
  await harness.selectOption({ label: 'Another Codex account' })
  await expect(page.getByLabel('Sign-in command')).toHaveValue('codex login')
  await harness.selectOption({ label: 'Another Claude account' })
  await expect(page.getByLabel('Sign-in command')).toHaveValue('claude /login')
  await expect(page.getByLabel('Enroll command')).toHaveAttribute('title', homebrew('claude'))

  await page.getByRole('combobox', { name: 'Installed with' }).selectOption('nix')
  await expect(page.getByLabel('Enroll command')).toHaveValue(nix('claude'))
  await page.getByRole('combobox', { name: 'Installed with' }).selectOption('direct')
  await expect(page.getByLabel('Enroll command')).toHaveValue(direct('claude'))
  await page.getByRole('combobox', { name: 'Installed with' }).selectOption('homebrew')
  expect(await noBar(page)).toBe(0)
  await shoot(page, 'panel', page.locator('#add-account-panel'))

  const steps = panel.getByRole('listitem')
  await steps.nth(0).getByRole('button', { name: 'Copy' }).click()
  await expect(steps.nth(0).getByRole('button', { name: 'Copied' })).toBeVisible()
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe('claude /login')
  await steps.nth(1).getByRole('button', { name: 'Copy' }).click()
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(homebrew('claude'))

  await page.getByRole('combobox', { name: 'Installed with' }).selectOption('direct')
  await page.reload()
  await page.getByRole('button', { name: 'Add an account' }).click()
  await expect(page.getByLabel('Enroll command')).toHaveValue(direct('pi'))
  await open.click()
  await expect(panel).toHaveCount(0)
  expect(errors).toEqual([])
})

test('the Agents page links to Settings; Add an account lives there and opens the steps', async ({ page }) => {
  const errors = watchErrors(page)
  await setup(page, { desk: true })
  await page.goto('/agents')
  const cap = page.getByRole('region', { name: 'Accounts and computers' })
  await expect(cap.getByRole('link', { name: /Manage/ })).toHaveAttribute('href', '/settings/accounts')
  // AEON-782: the section is a status line; adding an account is a Settings action.
  await expect(cap.getByRole('link', { name: 'Add an account' })).toHaveCount(0)
  await cap.getByRole('link', { name: /Manage/ }).click()
  await expect(page).toHaveURL(/\/settings\/accounts$/)
  await page.getByRole('button', { name: 'Add an account' }).click()
  await expect(page.getByRole('region', { name: 'Add an account' })).toBeVisible()
  await expect(page.getByLabel('Sign-in step')).toHaveValue('For pi, use /login and /model in pi first.')
  expect(errors).toEqual([])
})

test('without account.manage the action is gone and the hint stays', async ({ page }) => {
  await setup(page, { manage: false })
  await page.goto('/settings/accounts')
  await expect(accountsList(page)).toContainText('Claude Max')
  await expect(page.getByText(MEMBER)).toBeVisible()
  await expect(page.getByText(CREDENTIAL)).toBeVisible()
  await expect(page.getByRole('button', { name: 'Add an account' })).toHaveCount(0)
  await expect(page.getByRole('link', { name: CONNECT })).toHaveCount(0)
  await expect(page.getByRole('link', { name: 'Add an account' })).toHaveCount(0)
  await shoot(page, 'hint', page.locator('#agent-accounts'))
  await page.goto('/agents')
  await expect(page.getByRole('region', { name: 'Accounts and computers' }).getByRole('link', { name: 'Add an account' })).toHaveCount(0)
  await expect(page.getByRole('link', { name: /Manage/ })).toBeVisible()
})

test('no paired machine links to Connect your machine, including from the hash', async ({ page }) => {
  const { release } = await setup(page, { computers: 'hold' })
  try {
    await page.goto('/settings/accounts')
    await expect(accountsList(page)).toContainText('Claude Max')
    await expect(page.getByRole('button', { name: 'Add an account' })).toHaveCount(0)
  } finally { release() }
  const connect = page.getByRole('link', { name: CONNECT })
  await expect(connect).toBeVisible()
  await expect(connect).toHaveAttribute('href', '/agents/register-agent')
  await expect(page.getByText('No paired computers yet.')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Add an account' })).toHaveCount(0)
  await shoot(page, 'connect', page.locator('#agent-accounts'))
  await page.goto('/settings/accounts#add-account')
  await expect(page.getByRole('link', { name: CONNECT })).toBeVisible()
  await expect(page.getByRole('region', { name: 'Add an account' })).toHaveCount(0)
  await connect.click()
  await expect(page).toHaveURL('/agents/register-agent')
})

test('a failed computer list offers Try again and then the steps', async ({ page }) => {
  const errors = watchErrors(page)
  const { setComputers } = await setup(page, { computers: 'error' })
  await page.goto('/settings/accounts')
  // AEON-686: load failures are reported in the page's one status line.
  const alert = page.locator('#agent-accounts .loading-state[role="status"]')
  await expect(alert).toHaveText('Accounts or computers could not be loaded. Try again')
  await expect(page.getByText('No paired computers yet.')).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Add an account' })).toHaveCount(0)
  setComputers('ready')
  await alert.getByRole('button', { name: 'Try again' }).click()
  await page.getByRole('button', { name: 'Add an account' }).click()
  await expect(page.getByRole('region', { name: 'Add an account' })).toBeVisible()
  await expect(alert).toHaveText('')
  expect(errors).toEqual([])
})

test('the accounts hash opens the steps once a machine is paired', async ({ page }) => {
  await setup(page)
  await page.goto('/settings/accounts#add-account')
  await expect(page.getByRole('region', { name: 'Add an account' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Add an account' })).toHaveAttribute('aria-expanded', 'true')
  await expect(page).toHaveURL(/\/settings\/accounts$/)
  await expect(page.locator('#agent-accounts')).toBeVisible()
})

test('closing the hash-opened panel stays closed through a refresh and a focus', async ({ page }) => {
  const errors = watchErrors(page)
  await setup(page)
  await page.goto('/settings/accounts')
  const panel = page.getByRole('region', { name: 'Add an account' })
  const toggle = page.getByRole('button', { name: 'Add an account' })
  await expect(toggle).toHaveAttribute('aria-expanded', 'false')
  await page.goto('/settings/accounts#add-account')
  await expect(panel).toBeVisible()
  await expect(page).toHaveURL(/\/settings\/accounts$/)
  await toggle.click()
  await expect(panel).toHaveCount(0)

  const computers = () => page.waitForResponse(response => response.url().includes('/api/agent-pairing/computers') && response.request().method() === 'GET' && response.ok())
  const refreshed = computers()
  await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')))
  await refreshed
  await expect(panel).toHaveCount(0)
  const focused = computers()
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await focused
  await expect(panel).toHaveCount(0)

  await page.goBack()
  await expect(page).toHaveURL(/\/settings\/accounts$/)
  await expect(panel).toHaveCount(0)
  await toggle.click()
  await expect(panel).toBeVisible()
  await expect(page).not.toHaveURL(/#add-account/)
  expect(errors).toEqual([])
})

test('a new account shows up when the window is focused again', async ({ page }) => {
  const { data } = await setup(page)
  await page.goto('/settings/accounts')
  await expect(page.getByText('Claude Max')).toBeVisible()
  await expect(page.getByText('Night Codex')).toHaveCount(0)
  data.accounts.push({ ...data.accounts[0], id: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa1', label: 'Night Codex', account_key: 'codex-night' })
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect(page.getByText('Night Codex')).toBeVisible()
})
