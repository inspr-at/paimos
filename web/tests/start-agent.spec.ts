// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { sessionListReads } from './agents-fixtures'
import { mockStartAgent } from './start-agent-fixtures'
import { expectStableControls } from './helpers/stable'
import { me } from './work-fixtures'

const dialog = (page: Page) => page.getByRole('dialog', { name: 'Start agent', exact: true })
const newAgent = (page: Page) => page.getByRole('button', { name: 'New: start an agent, attach a session or connect a machine', exact: true })
async function open(page: Page, ticket = false) {
  // Ticket dispatch now uses the Queue/assignee menu (work-queue.spec.ts).
  // Keep the explicit account cascade covered through the /agents New menu.
  await page.goto('/agents')
  await newAgent(page).click()
  await page.getByRole('menuitem', { name: /^Start agent…/ }).click()
  await expect(dialog(page)).toBeVisible()
  if (ticket) {
    await dialog(page).getByRole('searchbox').fill('PHAROS-11')
    await dialog(page).getByRole('button', { name: /PHAROS-11 Connect Hetzner/ }).click()
  }
}
async function ready(page: Page, ticket = false) {
  if (!ticket) {
    await dialog(page).getByRole('searchbox').fill('PHAROS-11')
    await dialog(page).getByRole('button', { name: /PHAROS-11 Connect Hetzner/ }).click()
  }
  await expect(dialog(page).getByLabel('Host', { exact: true })).toHaveValue('workstation')
  await expect(dialog(page).getByLabel('Thinking', { exact: true })).not.toHaveValue('')
  await expect(dialog(page).getByText('Account available', { exact: true })).toBeVisible()
}

test('creates, readies and queues a ticket, then follows claim and managed registration', async ({ page }) => {
  const mock = await mockStartAgent(page)
  await open(page)
  await ready(page)
  await expect(dialog(page).getByRole('option', { name: 'ungranted-model' })).toHaveCount(0)
  await expect(dialog(page).getByText('should-not-render-key')).toHaveCount(0)
  await expect(dialog(page).getByText('Selected because it has the most allowance left.')).toBeVisible()
  await dialog(page).getByRole('button', { name: 'Queue run', exact: true }).dblclick()
  await expect(dialog(page).getByRole('heading', { name: 'Queued', exact: true })).toBeVisible()
  await expect(dialog(page).getByRole('list')).toContainText('Work Mac')
  await expect(dialog(page).getByRole('list')).toContainText('Workspace account · Pro')
  expect(mock.calls.filter(c => c.method === 'POST' && c.path === '/api/work-orders')).toHaveLength(1)
  expect(mock.calls.filter(c => c.method === 'POST' && c.path.endsWith('/runs'))).toHaveLength(1)
  expect(mock.calls.find(c => c.method === 'POST' && c.path.endsWith('/runs'))?.body).toEqual({ agent_principal_id: mock.agentId, model_profile_id: mock.profile.id, requested_account_id: mock.account.id })
  expect(mock.calls.find(c => c.method === 'POST' && c.path === '/api/work-orders')?.body?.parent_id).toBe('n-1')
  expect(mock.calls.some(c => c.path.includes('agent-launch-catalog'))).toBe(false)
  expect(mock.calls.some(c => c.path === '/api/agent-accounts/catalog')).toBe(true)
  mock.claim()
  await dialog(page).getByRole('button', { name: 'Refresh status' }).click()
  await expect(dialog(page).getByRole('heading', { name: 'Claimed', exact: true })).toBeVisible()
  await expect(dialog(page).getByRole('link', { name: 'Open session' })).toHaveCount(0)
  mock.claim(true)
  await dialog(page).getByRole('button', { name: 'Refresh status' }).click()
  await expect(dialog(page).getByRole('heading', { name: 'Managed session connected' })).toBeVisible()
  await expect(dialog(page).getByText('Requested configuration')).toBeVisible()
  await expect(dialog(page).getByText('Reported by the session')).toBeVisible()
  await expect(dialog(page).getByText('workspace-build').first()).toBeVisible()
  await expect(dialog(page).getByText('This differs from the requested configuration.')).toHaveCount(0)
  await expect(dialog(page).getByRole('link', { name: 'Open session' })).toHaveAttribute('href', '/agents/managed-1')
})

test('advanced launch retains its selected ticket and retry reuses the created work order', async ({ page }) => {
  const mock = await mockStartAgent(page, { failQueue: true })
  await open(page, true)
  await expect(dialog(page).getByText('PHAROS-11', { exact: true })).toBeVisible()
  await expect(dialog(page).getByRole('searchbox')).toHaveCount(0)
  await ready(page, true)
  await dialog(page).getByRole('button', { name: 'Queue run', exact: true }).click()
  await expect(dialog(page).getByRole('alert')).toContainText('Temporary queue failure')
  await expect(dialog(page).getByLabel('Host', { exact: true })).toHaveValue('workstation')
  mock.state.failQueue = false
  await dialog(page).getByRole('button', { name: 'Queue run', exact: true }).click()
  await expect(dialog(page).getByRole('heading', { name: 'Queued', exact: true })).toBeVisible()
  expect(mock.calls.filter(c => c.method === 'POST' && c.path === '/api/work-orders')).toHaveLength(1)
})

test('canonical leaf search excludes mixed parent results and queues only the selected leaf', async ({ page }) => {
  const mock = await mockStartAgent(page)
  const reads: URLSearchParams[] = []
  const leaf = { id: 'n-1', key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning', is_leaf: true, fields: {}, kind_slug: 'work' }
  await page.route('**/api/nodes?**', route => {
    const query = new URL(route.request().url()).searchParams
    if (query.get('limit') !== '30' || query.get('sort') !== '-updated_at') return route.fallback()
    reads.push(query)
    // Deliberately include an ineligible row even when shape was requested.
    return route.fulfill({ json: { items: [{ ...leaf, id: 'parent', key: 'PHAROS-10', title: 'Work parent', is_leaf: false }, leaf], next_cursor: null } })
  })
  await open(page)
  const search = dialog(page).getByRole('searchbox')
  await expect(dialog(page).getByRole('button', { name: /PHAROS-11 Connect Hetzner/ })).toBeVisible()
  await expectStableControls({
    controls: { search },
    interactions: [{ name: 'filter leaf results', run: async () => {
      await search.fill('PHAROS')
      await expect.poll(() => reads.at(-1)?.get('q')).toBe('PHAROS')
    } }],
    scrollAreas: { results: dialog(page).locator('.ticket-results') },
  })
  expect(reads.length).toBeGreaterThan(0)
  for (const query of reads) {
    expect(query.get('kind')).toBe('work')
    expect(query.get('shape')).toBe('leaf')
  }
  await expect(dialog(page).getByRole('button', { name: /PHAROS-10 Work parent/ })).toHaveCount(0)
  await expect(dialog(page).getByRole('button', { name: 'Queue run', exact: true })).toBeDisabled()
  await dialog(page).getByRole('button', { name: /PHAROS-11 Connect Hetzner/ }).click()
  await ready(page, true)
  await dialog(page).getByRole('button', { name: 'Queue run', exact: true }).click()
  await expect(dialog(page).getByRole('heading', { name: 'Queued', exact: true })).toBeVisible()
  expect(mock.calls.filter(c => c.method === 'POST' && c.path === '/api/work-orders').map(c => c.body?.parent_id)).toEqual(['n-1'])
})

test('a queue that fails after the work order was written still reads the lists again', async ({ page }) => {
  const mock = await mockStartAgent(page, { failQueue: true })
  await open(page)
  await ready(page)
  const before = sessionListReads(mock.calls)
  await dialog(page).getByRole('button', { name: 'Queue run', exact: true }).click()
  await expect(dialog(page).getByRole('alert')).toContainText('Temporary queue failure')
  expect(mock.calls.filter(c => c.method === 'POST' && c.path === '/api/work-orders')).toHaveLength(1)
  await expect.poll(() => sessionListReads(mock.calls)).toBeGreaterThan(before)
})

for (const scenario of [
  { offline: true, label: 'Sign-in probe is stale' },
  { unavailable: true, label: 'Account is draining' },
] as const) {
  test(`reports ${scenario.label} and still pins that account`, async ({ page }) => {
    const mock = await mockStartAgent(page, scenario)
    await open(page)
    await readyAccount(page)
    await expect(dialog(page).getByText(scenario.label, { exact: true })).toBeVisible()
    await expect(dialog(page).getByRole('button', { name: 'Queue run', exact: true })).toBeEnabled()
    await dialog(page).getByRole('button', { name: 'Queue run', exact: true }).click()
    await expect(dialog(page).getByRole('heading', { name: 'Queued', exact: true })).toBeVisible()
    expect(mock.calls.find(c => c.method === 'POST' && c.path.endsWith('/runs'))?.body?.requested_account_id).toBe(mock.account.id)
    await dialog(page).getByRole('button', { name: 'Close', exact: true }).click()
    await expect(page.getByRole('region', { name: 'Runs awaiting a session' })).toContainText('Queued')
  })
}

async function readyAccount(page: Page) {
  await dialog(page).getByRole('searchbox').fill('PHAROS-11')
  await dialog(page).getByRole('button', { name: /PHAROS-11 Connect Hetzner/ }).click()
  await expect(dialog(page).getByLabel('Account', { exact: true })).not.toHaveValue('')
  await expect(dialog(page).getByLabel('Thinking', { exact: true })).not.toHaveValue('')
}

test('an empty model grant is not filled from the tenant catalog', async ({ page }) => {
  await mockStartAgent(page, { catalog: 'empty-grants' })
  await open(page)
  await dialog(page).getByRole('searchbox').fill('PHAROS-11')
  await dialog(page).getByRole('button', { name: /PHAROS-11 Connect Hetzner/ }).click()
  await expect(dialog(page).getByText('No model granted', { exact: true })).toBeVisible()
  await expect(dialog(page).locator('[id$="-model-note"]')).toHaveText('No granted model is available to choose.')
  await expect(dialog(page).getByRole('option', { name: 'ungranted-model' })).toHaveCount(0)
  await expect(dialog(page).getByRole('option', { name: 'workspace-build' })).toHaveCount(0)
  await expect(dialog(page).getByRole('button', { name: 'Queue run', exact: true })).toBeDisabled()
})

test('a catalog miss stays empty until retry', async ({ page }) => {
  await mockStartAgent(page, { catalog: 'retry' })
  await open(page)
  const alert = dialog(page).getByRole('alert')
  await expect(alert).toContainText('The account catalog did not load')
  await expect(dialog(page).getByLabel('Host', { exact: true })).toBeDisabled()
  await expect(dialog(page).getByRole('button', { name: 'Queue run', exact: true })).toBeDisabled()
  await alert.getByRole('button', { name: 'Retry catalog' }).click()
  await expect(dialog(page).getByText('Account available', { exact: true })).toBeVisible()
  await expect(dialog(page).getByRole('option', { name: 'ungranted-model' })).toHaveCount(0)
})

test('pi registry model ids stay offered and a secret-shaped id does not', async ({ page }) => {
  await mockStartAgent(page, { catalog: 'pi' })
  await open(page, true)
  await ready(page, true)
  await expect(dialog(page).getByLabel('Model', { exact: true }).locator('option:checked')).toHaveText('anthropic/claude-opus-5')
  await expect(dialog(page).getByRole('option', { name: 'anthropic/claude-sonnet-5', exact: true })).toHaveCount(1)
  await expect(dialog(page).getByRole('option', { name: 'sk-live-token', exact: true })).toHaveCount(0)
  await expect(dialog(page).getByRole('button', { name: 'Queue run', exact: true })).toBeEnabled()
})

test('a provisional window is unknown and is not treated as fully unused', async ({ page }) => {
  await mockStartAgent(page, { catalog: 'provisional' })
  await open(page, true)
  await ready(page, true)
  await expect(dialog(page).getByText('Selected because it has the most allowance left.')).toBeVisible()
  const spare = dialog(page).getByRole('option', { name: /Spare account/ })
  await expect(spare).toContainText('allowance unknown')
  await expect(spare).not.toContainText('100%')
})

test('a stale grant does not queue or fall back, and catalog refresh drops the hidden model', async ({ page }) => {
  const mock = await mockStartAgent(page, { staleGrant: true })
  await open(page, true)
  await ready(page, true)
  const account = await dialog(page).getByLabel('Account', { exact: true }).inputValue()
  await dialog(page).getByRole('button', { name: 'Queue run', exact: true }).click()
  const alert = dialog(page).getByRole('alert').filter({ hasText: 'no longer allows that model' })
  await expect(alert).toBeVisible()
  await expect(dialog(page).getByRole('heading', { name: 'Queued', exact: true })).toHaveCount(0)
  await expect(dialog(page).getByLabel('Account', { exact: true })).toHaveValue(account)
  await expect(dialog(page).getByLabel('Model', { exact: true })).toHaveValue('')
  await expect(dialog(page).getByRole('button', { name: 'Queue run', exact: true })).toBeDisabled()
  await alert.getByRole('button', { name: 'Refresh catalog' }).click()
  await expect(dialog(page).getByRole('option', { name: 'workspace-build', exact: true })).toHaveCount(0)
  await expect(dialog(page).getByLabel('Model', { exact: true })).toHaveValue('')
  await expect(dialog(page).getByLabel('Account', { exact: true })).toHaveValue(account)
  await expect(dialog(page).getByRole('button', { name: 'Queue run', exact: true })).toBeDisabled()
  expect(mock.calls.filter(c => c.method === 'POST' && c.path.endsWith('/runs'))).toHaveLength(1)
  expect(mock.calls.find(c => c.method === 'POST' && c.path.endsWith('/runs'))?.body?.requested_account_id).toBe(account)
})

test('a connected session keeps the request separate from what it reports', async ({ page }) => {
  const mock = await mockStartAgent(page)
  await open(page, true)
  await ready(page, true)
  await dialog(page).getByRole('button', { name: 'Queue run', exact: true }).click()
  await expect(dialog(page).getByText('Requested configuration')).toBeVisible()
  mock.claim(true, { model: 'anthropic/claude-opus-5', account_label: 'Other subscription', reasoning_effort: 'xhigh' })
  await dialog(page).getByRole('button', { name: 'Refresh status' }).click()
  await expect(dialog(page).getByRole('heading', { name: 'Managed session connected' })).toBeVisible()
  const reported = dialog(page).locator('.requested').nth(1)
  await expect(reported).toContainText('anthropic/claude-opus-5')
  await expect(reported).toContainText('Other subscription')
  await expect(reported).toContainText('Extra high')
  await expect(dialog(page).getByText('This differs from the requested configuration.')).toBeVisible()
  await expect(dialog(page).locator('.requested').first()).toContainText('workspace-build')
})

test('an unreported session does not reuse the requested labels as fact', async ({ page }) => {
  const mock = await mockStartAgent(page)
  await open(page, true)
  await ready(page, true)
  await dialog(page).getByRole('button', { name: 'Queue run', exact: true }).click()
  await expect(dialog(page).getByText('Requested configuration', { exact: true })).toBeVisible()
  mock.claim(true, { model: null, account_label: null, reasoning_effort: null })
  await dialog(page).getByRole('button', { name: 'Refresh status' }).click()
  const reported = dialog(page).locator('.requested').nth(1)
  await expect(reported).toContainText('Unknown')
  await expect(reported).not.toContainText('workspace-build')
  await expect(dialog(page).getByText('The session has not reported every field.')).toBeVisible()
  await expect(dialog(page).getByText('This differs from the requested configuration.')).toHaveCount(0)
})

test('changing host replaces the granted model', async ({ page }) => {
  await mockStartAgent(page, { catalog: 'two-hosts' })
  await open(page, true)
  await expect(dialog(page).getByLabel('Model', { exact: true }).locator('option:checked')).toHaveText('workspace-build')
  await dialog(page).getByLabel('Host', { exact: true }).selectOption({ label: 'Laptop' })
  await expect(dialog(page).getByLabel('Harness', { exact: true }).locator('option:checked')).toHaveText('Claude')
  await expect(dialog(page).getByLabel('Model', { exact: true }).locator('option:checked')).toHaveText('other-model')
  await expect(dialog(page).getByRole('option', { name: 'workspace-build' })).toHaveCount(0)
  await expect(dialog(page).getByLabel('Thinking', { exact: true }).locator('option:checked')).toHaveText('Medium')
})

test('failed prerequisites keep launch disabled and read-only people have no action', async ({ page }) => {
  await mockStartAgent(page, { forbidden: true })
  await open(page)
  await expect(dialog(page).getByRole('alert')).toContainText('Permission denied')
  await expect(dialog(page).getByRole('button', { name: 'Queue run', exact: true })).toBeDisabled()
  await page.unrouteAll({ behavior: 'wait' })
  await mockStartAgent(page, { readOnly: true })
  await page.goto('/agents')
  await expect(page.getByRole('heading', { name: 'Agents', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Start agent', exact: true })).toHaveCount(0)
  await newAgent(page).click()
  await expect(page.getByRole('menuitem', { name: /^Start agent…/ })).toHaveCount(0)
})

for (const theme of ['light', 'dark']) {
  test(`keyboard, axe and phone layout in ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 })
    await page.emulateMedia({ colorScheme: theme as 'light' | 'dark' })
    await mockStartAgent(page)
    await open(page, true)
    await ready(page, true)
    await page.evaluate(theme => document.documentElement.dataset.theme = theme, theme)
    await dialog(page).getByLabel('Host', { exact: true }).focus()
    await page.keyboard.press('Tab')
    await expect(dialog(page).getByLabel('Harness', { exact: true })).toBeFocused()
    const result = await new AxeBuilder({ page }).include('.launch-dialog').withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'best-practice']).analyze()
    expect(result.violations).toEqual([])
    expect(await dialog(page).evaluate(el => el.scrollWidth <= el.clientWidth + 1)).toBe(true)
    await page.keyboard.press('Escape')
    await expect(dialog(page)).not.toBeVisible()
    await expect(newAgent(page)).toBeFocused()
  })
}

for (const theme of ['light', 'dark'] as const) {
  for (const width of [1600, 390]) {
    test(`screenshot ${width} ${theme}`, async ({ page }) => {
      mkdirSync('../.agent-shots', { recursive: true })
      await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
      await page.emulateMedia({ colorScheme: theme })
      await mockStartAgent(page)
      await page.goto('/settings/accounts')
      await page.evaluate(theme => document.documentElement.dataset.theme = theme, theme)
      const accounts = page.locator('#agent-accounts')
      // AEON-686: the Accounts overview lists each account as a row; its logins open in the docked panel.
      await expect(accounts.locator('.list-row[data-account]').first()).toBeVisible()
      await accounts.screenshot({ path: `../.agent-shots/acu1-accounts-${width}-${theme}.png` })
      await open(page)
      await ready(page)
      await page.evaluate(theme => document.documentElement.dataset.theme = theme, theme)
      await dialog(page).screenshot({ path: `../.agent-shots/acu1-start-${width}-${theme}.png` })
    })
  }
}

for (const theme of ['light', 'dark'] as const) for (const width of [390, 1024, 1440]) {
  test(`New menu restores its trigger without moving header controls at ${width} ${theme}`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ colorScheme: theme })
    await mockStartAgent(page)
    await page.route('**/api/me', route => route.fulfill({ json: {
      principal: { id: me.id, name: me.name, kind: 'person', roles: ['admin'] },
      tenant: { id: 't1', name: 'Agentensteuerung für gemeinsame Entwicklungsprojekte und zuverlässige Übergaben' },
    } }))
    await page.goto('/agents')
    await page.evaluate(theme => document.documentElement.dataset.theme = theme, theme)
    const header = page.locator('.agents-page .page-head')
    await expectStableControls({
      controls: { new: newAgent(page), more: header.getByRole('button', { name: 'More agent actions', exact: true }) },
      scrollAreas: { header },
      interactions: [
        { name: 'open New menu', run: async () => {
          await newAgent(page).click()
          await expect(page.getByRole('menuitem', { name: /^Start agent…/ })).toBeVisible()
          await page.screenshot({ path: info.outputPath(`agents-header-${width}-${theme}.png`), fullPage: true })
        } },
        { name: 'open start dialog', run: async () => {
          await page.getByRole('menuitem', { name: /^Start agent…/ }).click()
          await expect(dialog(page)).toBeVisible()
        } },
        { name: 'close start dialog', run: async () => {
          await dialog(page).getByRole('button', { name: 'Close start agent', exact: true }).click()
          await expect(dialog(page)).not.toBeVisible()
          await expect(newAgent(page)).toBeFocused()
        } },
      ],
    })
  })
}
