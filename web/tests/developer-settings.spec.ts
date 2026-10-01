// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { mockJourney, journeyWorld } from './journey-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import AxeBuilder from '@axe-core/playwright'
import { mkdirSync } from 'node:fs'

const switchName = 'Show the flow controls (not yet tested end to end)'
async function setup(page: Page, value?: Record<string, unknown>) {
  const data = fixtures()
  if (value) data.preferences['developer-ui'] = value
  await mockWork(page, data)
  await mockJourney(page, journeyWorld('plan'), { flowControls: false })
  return data
}
async function absent(page: Page) {
  await expect(page.locator('.flow-slot, .journey-chip, .journey-view, .walker-bar')).toHaveCount(0)
  await expect(page.getByRole('tab', { name: 'Journey', exact: true })).toHaveCount(0)
}

test('fresh preferences leave no flow DOM, space or keyboard stops on several screens', async ({ page }) => {
  await setup(page)
  for (const path of ['/p/PHAROS/tickets', '/p/PHAROS/knowledge', '/p/PHAROS/PHAROS-11']) {
    await page.goto(path)
    await expect(page.getByRole('heading', { name: 'Pharos', exact: true })).toBeVisible()
    await absent(page)
    await expect(page.locator('.footer-name')).not.toHaveClass(/compact/)
  }
  await page.keyboard.press('?')
  await expect(page.getByRole('heading', { name: 'Journey', exact: true })).toHaveCount(0)
})

test('a person deliberately opts in; it persists on reload and disappears again after opt-out', async ({ page }) => {
  const data = await setup(page)
  // The preference needs no workspace-management grant.
  await page.route('**/api/me/permissions*', route => route.fulfill({ json: mockEffectivePermissions('member') }))
  await page.goto('/settings/developer#flow-controls')
  const toggle = page.getByRole('switch', { name: switchName })
  await expect(page.getByText('For people working on Paimos itself.', { exact: false })).toBeVisible()
  await expect(toggle).not.toBeChecked()
  await toggle.check()
  await expect.poll(() => data.preferences['developer-ui']?.show_flow_controls).toBe(true)
  for (const path of ['/p/PHAROS/tickets', '/p/PHAROS/knowledge', '/p/PHAROS/PHAROS-11']) {
    await page.goto(path)
    await expect(page.locator('.app-footer .journey-chip')).toBeVisible()
    await expect(page.getByRole('tab', { name: 'Journey', exact: true })).toBeVisible()
  }
  await page.reload()
  await expect(page.locator('.app-footer .journey-chip')).toBeVisible()
  await page.goto('/settings/developer')
  await expect(toggle).toBeChecked()
  await toggle.uncheck()
  await expect.poll(() => data.preferences['developer-ui']?.show_flow_controls).toBe(false)
  await page.goto('/p/PHAROS/journey?stage=plan&walk=PHAROS-11')
  await absent(page)
  await expect(page.getByRole('heading', { name: 'Flow controls are a developer feature' })).toBeVisible()
  await page.reload()
  await absent(page)
})

test('canonical and legacy deep links explain the opt-in without mounting the stages or walker', async ({ page }) => {
  await setup(page)
  for (const path of ['/p/PHAROS/journey?stage=inspire', '/p/PHAROS?view=journey&walk=PHAROS-11', '/p/PHAROS/PHAROS-11?section=journey&stage=build', '/projects/p-pharos/journey/plan']) {
    await page.goto(path)
    await expect(page.getByRole('heading', { name: 'Flow controls are a developer feature' })).toBeVisible()
    await absent(page)
    await expect(page.getByRole('link', { name: 'Developer settings', exact: true })).toHaveAttribute('href', '/settings/developer#flow-controls')
  }
  await page.getByRole('link', { name: 'Developer settings', exact: true }).click()
  await expect(page.getByRole('switch', { name: switchName })).not.toBeChecked()
})

test('an opted-in journey mounts its stages and walker', async ({ page }) => {
  await setup(page, { show_flow_controls: true })
  await page.goto('/p/PHAROS/journey?stage=plan')
  await expect(page.getByRole('navigation', { name: 'Project journey' })).toBeVisible()
  await page.getByRole('button', { name: 'Full screen', exact: true }).click()
  await expect(page.getByRole('dialog', { name: 'Release walker' })).toBeVisible()
})

test('malformed and unreadable preferences fail closed', async ({ page }) => {
  await setup(page, { show_flow_controls: 'true' })
  await page.goto('/p/PHAROS/tickets')
  await expect(page.getByRole('heading', { name: 'Pharos', exact: true })).toBeVisible()
  await absent(page)
  await page.route('**/api/preferences/developer-ui', route => route.fulfill({ status: 503, json: { error: 'unavailable' } }))
  await page.reload()
  await absent(page)
})

test('a failed save keeps the preference off and offers a visible retry', async ({ page }) => {
  const data = await setup(page)
  let fail = true
  await page.route('**/api/preferences/developer-ui', route => route.request().method() === 'PUT' && fail
    ? route.fulfill({ status: 503, json: { error: 'unavailable' } }) : route.fallback())
  await page.goto('/settings/developer')
  const toggle = page.getByRole('switch', { name: switchName })
  await toggle.click()
  await expect(page.getByRole('alert')).toContainText('Your flow preference could not be saved')
  await expect(toggle).not.toBeChecked()
  expect(data.preferences['developer-ui']).toBeUndefined()
  fail = false
  await toggle.check()
  await expect.poll(() => data.preferences['developer-ui']?.show_flow_controls).toBe(true)
})

test('preferences belong to a person; another person and an agent remain off', async ({ page }) => {
  await setup(page, { show_flow_controls: true })
  await page.goto('/p/PHAROS/tickets')
  await expect(page.locator('.journey-chip')).toBeVisible()
  await page.route('**/api/me', route => route.fulfill({ json: { principal: { id: 'other-person', name: 'Other person', kind: 'person', roles: ['member'] }, tenant: { id: 't1', name: 'INSPR Studio' } } }))
  await page.route('**/api/preferences/developer-ui', route => route.fulfill({ json: { value: null } }))
  await page.getByRole('tab', { name: 'Knowledge', exact: true }).click()
  await absent(page)
  await page.route('**/api/me', route => route.fulfill({ json: { principal: { id: 'agent', name: 'Agent', kind: 'agent', roles: ['member'] }, tenant: { id: 't1', name: 'INSPR Studio' } } }))
  await page.reload()
  await absent(page)
})

test('Developer settings and the hidden flow remain accessible on desktop and phone', async ({ page }, info) => {
  await setup(page)
  const SHOTS = process.env.AEON_500_SCREENSHOT_DIR ?? info.outputPath('shots')
  mkdirSync(SHOTS, { recursive: true })
  for (const width of [1440, 390]) {
    await page.setViewportSize({ width, height: 900 })
    await page.goto('/settings/developer')
    const toggle = page.getByRole('switch', { name: switchName })
    await expect(toggle).toBeVisible()
    expect((await new AxeBuilder({ page }).include('main').analyze()).violations).toEqual([])
    await page.screenshot({ path: `${SHOTS}/developer-${width}.png` })
    await page.goto('/p/PHAROS/tickets')
    await expect(page.getByRole('heading', { name: 'Pharos', exact: true })).toBeVisible()
    await absent(page)
    await page.screenshot({ path: `${SHOTS}/flow-off-${width}.png` })
    await page.goto('/p/PHAROS/journey')
    await expect(page.getByRole('heading', { name: 'Flow controls are a developer feature' })).toBeVisible()
    // The hidden Journey tab must not leave the remaining tabs unreachable.
    await expect(page.getByRole('tab', { name: 'Tickets', exact: true })).toHaveAttribute('tabindex', '0')
    await page.screenshot({ path: `${SHOTS}/deep-link-${width}.png` })
  }
})
