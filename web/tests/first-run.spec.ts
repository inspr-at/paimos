// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { mockStartAgent } from './start-agent-fixtures'
import { mockPairing } from './agent-pairing-fixtures'
import { fixtures, mockWork } from './work-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'

async function appearance(page: Page, theme: 'light' | 'dark') {
  await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
}
async function capture(page: Page, name: string) {
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  if (process.env.AEON353_SHOTS) {
    mkdirSync(process.env.AEON353_SHOTS, { recursive: true })
    await page.screenshot({ path: join(process.env.AEON353_SHOTS, `${name}.png`), fullPage: true })
  }
}
for (const width of [1600, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`first run Start ${width} ${theme}: schedule wait and run now once`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme: theme })
    await page.clock.install({ time: new Date('2026-09-29T21:10:00Z') })
    const mock = await mockStartAgent(page, { wait: { code: 'schedule', until: '2026-09-30T06:00:00Z', timezone: 'Europe/Vienna', run_now_allowed: true } })
    await page.goto('/agents')
    await appearance(page, theme)
    await page.locator('button.start-agent').click()
    const dialog = page.getByRole('dialog', { name: 'Start agent', exact: true })
    await dialog.getByRole('button', { name: /PHAROS-11/ }).click()
    await dialog.getByLabel('Account', { exact: true }).selectOption(mock.account.id)
    await expect(dialog.getByText('Codex agents start at 08:00', { exact: true })).toBeVisible()
    await capture(page, `start-${width}-${theme}`)
    await dialog.getByRole('button', { name: 'Queue run', exact: true }).scrollIntoViewIfNeeded()
    await capture(page, `start-actions-${width}-${theme}`)
    await dialog.getByRole('button', { name: 'Run now once', exact: true }).click()
    await expect.poll(() => mock.calls.find(c => c.method === 'POST' && c.path.endsWith('/runs'))?.body?.capacity_override).toBe('now')
    await expect(dialog.getByRole('heading', { name: 'Queued', exact: true })).toBeVisible()
  })
  test(`first run pairing ${width} ${theme}: approve without a form`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme: theme })
    await mockWork(page, fixtures())
    const calls = await mockPairing(page)
    await page.goto('/agents/register-agent')
    await appearance(page, theme)
    await page.getByLabel('Pairing code').fill('123-456-789')
    await page.getByRole('button', { name: 'Look up code' }).click()
    await expect(page.getByRole('radio', { name: /Let agents use these accounts/ })).toBeChecked()
    await expect(page.getByLabel('Requests', { exact: true })).toHaveCount(0)
    await capture(page, `pairing-${width}-${theme}`)
    await page.getByRole('button', { name: 'Connect your machine', exact: true }).scrollIntoViewIfNeeded()
    await capture(page, `pairing-approval-${width}-${theme}`)
    await page.getByRole('button', { name: 'Connect your machine', exact: true }).click()
    await expect.poll(() => calls.filter(c => c.path.endsWith('/capacity/approve')).length).toBe(2)
    expect(calls.some(c => c.path.endsWith('/windows'))).toBe(false)
    await expect(page.getByText('Agents may use the selected accounts once setup finishes.')).toBeVisible()
  })
  test(`first run settings ${width} ${theme}: approve, then drain`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme: theme })
    const mock = await mockStartAgent(page)
    mock.account.ongoing_use_approved = false
    const writes: string[] = []
    await page.route('**/api/me/permissions*', route => {
      const p = mockEffectivePermissions('admin')
      p.workspace.permissions.push('account.manage', 'account.read')
      return route.fulfill({ json: p })
    })
    await page.route('**/api/agent-accounts/*', async route => {
      const path = new URL(route.request().url()).pathname
      if (route.request().method() !== 'PATCH') return route.fallback()
      writes.push('state')
      Object.assign(mock.account, route.request().postDataJSON())
      return route.fulfill({ json: mock.account })
    })
    await page.route('**/api/agent-accounts/*/capacity/approve', route => {
      writes.push('approval'); mock.account.ongoing_use_approved = true
      return route.fulfill({ status: 204 })
    })
    await page.goto('/settings/accounts')
    await appearance(page, theme)
    const toggle = page.getByRole('switch', { name: /Agents may use it/ })
    await expect(toggle).toHaveAttribute('aria-checked', 'false')
    await expect(toggle).toHaveCSS('opacity', '1')
    await capture(page, `settings-${width}-${theme}`)
    await toggle.click()
    await expect(toggle).toHaveAttribute('aria-checked', 'true')
    expect(writes).toEqual(['approval', 'state'])
    await toggle.click()
    const confirm = page.getByRole('dialog', { name: /Drain Workspace account/ })
    await expect(confirm).toBeVisible()
    await confirm.getByRole('button', { name: 'Drain account' }).click()
    await expect(toggle).toHaveAttribute('aria-checked', 'false')
    expect(writes).toEqual(['approval', 'state', 'state'])
  })
}

test('pairing keeps accounts paused when selected and does not approve on a later visit', async ({ page }) => {
  await mockWork(page, fixtures()); const calls = await mockPairing(page)
  await page.goto('/agents/register-agent')
  await page.getByLabel('Pairing code').fill('123-456-789')
  await page.getByRole('button', { name: 'Look up code' }).click()
  await page.getByRole('radio', { name: /Keep agents paused/ }).check()
  await page.getByRole('button', { name: 'Connect your machine', exact: true }).click()
  await expect(page.getByText('Agents stay paused. Turn them on in Settings / Accounts.')).toBeVisible()
  expect(calls.filter(c => c.path.endsWith('/capacity/approve'))).toHaveLength(0)
  await page.reload()
  await expect(page.getByRole('heading', { name: 'Connect your machine', exact: true })).toBeVisible()
  expect(calls.filter(c => c.path.endsWith('/capacity/approve'))).toHaveLength(0)
})

test('a queued run exposes its wait and a run-scoped override from the queue', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  const mock = await mockStartAgent(page, { wait: { code: 'reserve', run_now_allowed: true } })
  await page.goto('/agents')
  await page.locator('button.start-agent').click()
  const dialog = page.getByRole('dialog', { name: 'Start agent', exact: true })
  await dialog.getByRole('button', { name: /PHAROS-11/ }).click()
  await dialog.getByLabel('Account', { exact: true }).selectOption(mock.account.id)
  await dialog.getByRole('button', { name: 'Queue run', exact: true }).click()
  await expect(dialog.getByRole('heading', { name: 'Waiting', exact: true })).toBeVisible()
  await expect(dialog.getByText('Kept for you', { exact: true })).toBeVisible()
  expect(mock.state.runs[0].capacity_override).toBe('')
  await dialog.getByRole('button', { name: 'Close', exact: true }).click()
  const queue = page.getByRole('region', { name: 'Runs awaiting a session' })
  await expect(queue.getByText('Kept for you', { exact: true })).toBeVisible()
  await queue.scrollIntoViewIfNeeded()
  await capture(page, 'queued-390-light')
  await queue.getByRole('button', { name: 'Run now once' }).click()
  await expect.poll(() => mock.state.runs[0].capacity_override).toBe('now')
  expect(mock.calls.filter(c => c.method === 'POST' && c.path.endsWith('/capacity-override'))).toHaveLength(1)
  await expect(queue.getByRole('button', { name: 'Run now once' })).toHaveCount(0)
})

test('pairing retries only account approval after a partial save', async ({ page }) => {
  await mockWork(page, fixtures())
  const calls = await mockPairing(page)
  let attempts = 0
  await page.route('**/api/agent-accounts/*/capacity/approve', route => {
    attempts++
    return attempts === 2 ? route.fulfill({ status: 503, json: { error: 'Please retry' } }) : route.fallback()
  })
  await page.goto('/agents/register-agent')
  await page.getByLabel('Pairing code').fill('123-456-789')
  await page.getByRole('button', { name: 'Look up code' }).click()
  await page.getByRole('button', { name: 'Connect your machine', exact: true }).click()
  await page.getByRole('button', { name: 'Retry account approval', exact: true }).click()
  await expect(page.getByText('Agents may use the selected accounts once setup finishes.')).toBeVisible()
  expect(attempts).toBe(4)
  expect(calls.filter(c => c.path.endsWith('/approve') && !c.path.includes('/capacity/'))).toHaveLength(1)
  expect(calls.some(c => c.path.endsWith('/windows'))).toBe(false)
})
