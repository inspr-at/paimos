// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors, type MockOptions } from './work-fixtures'
import { defaultStatusHelp, type StatusHelp } from '../src/lib/statusDefinitions'

const panel = (page: Page) => page.getByRole('complementary', { name: 'Ticket details' })
const shots = process.env.AEON_521A_SHOTS
async function setup(page: Page, admin = true, options: MockOptions = {}) {
  const data = fixtures()
  data.nodes.find(node => node.id === 'n-1')!.human_check = 'Touch ID on the paired Mac'
  const calls = await mockWork(page, data, { admin, ...options })
  const help: StatusHelp = defaultStatusHelp()
  help.limits_source = 'workspace'; help.project_name = 'Pharos'; help.autopilot.rules.accept.days = 45
  help.definitions.find(def => def.state === 'accepted')!.set_by = 'Person, or the 45-day rule'
  await page.route('**/api/status/help**', route => route.fulfill({ json: help }))
  await page.goto('/p/PHAROS/PHAROS-11')
  return { data, calls, help }
}
async function openMenu(page: Page) { await panel(page).getByRole('button', { name: /Status: In progress/ }).click() }

test('approved menu, hints, icons, live help and Queued in light and dark', async ({ page }) => {
  const errors = watchErrors(page)
  await setup(page)
  const labels = ['New', 'Backlog', 'Open', 'Blocked', 'In progress', 'QA', 'Done', 'Delivered', 'Accepted', 'Cancelled', 'Archived']
  for (const theme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme: theme })
    await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
    await page.setViewportSize({ width: 1440, height: 1300 })
    await openMenu(page)
    const menu = page.getByRole('menu', { name: 'Status of PHAROS-11' })
    await expect(menu.getByRole('menuitemradio').locator('.label')).toHaveText(labels)
    await expect(menu.getByRole('menuitemradio', { name: 'Accepted', exact: true })).toHaveAttribute('data-tip', /45 days after delivery/)
    await expect(menu.locator('.st-new circle')).toHaveCount(2)
    await expect(menu.locator('.st-open circle')).toHaveAttribute('stroke', 'var(--st-progress)')
    await expect(menu.locator('.st-done circle')).toHaveAttribute('stroke', 'var(--st-ok)')
    await expect(menu.locator('.st-delivered circle')).toHaveAttribute('fill', 'var(--st-ok)')
    await expect(menu.locator('.st-accepted path')).toHaveCount(2)
    if (shots) { mkdirSync(shots, { recursive: true }); await page.screenshot({ path: join(shots, `ticket-menu-${theme}.png`) }) }
    await menu.getByRole('menuitem', { name: 'What do these mean?' }).click()
    const sheet = page.getByRole('dialog', { name: 'What the statuses mean' })
    await expect(sheet).toBeVisible()
    await expect(sheet.locator('[data-rule="accept"] .limit')).toHaveText('45 days')
    await expect(sheet.getByRole('row', { name: /Queued Open or Blocked plus a place/ })).toBeVisible()
    await expect(sheet.getByText('Open or Blocked in the queue; not a status')).toBeVisible()
    await expect(sheet.getByRole('button', { name: 'Change limits' })).toBeVisible()
    await expect(sheet.getByText('aeon status help --json')).toBeVisible()
    if (shots) await sheet.screenshot({ path: join(shots, `status-help-${theme}.png`) })
    await page.keyboard.press('Escape')
    await expect(panel(page).getByRole('button', { name: /Status: In progress/ })).toBeFocused()
  }
  expect(errors).toEqual([])
})

test('help refreshes limits, master/rule off states and member access; mobile has no overflow', async ({ page }) => {
  const { help } = await setup(page, false)
  await page.setViewportSize({ width: 390, height: 844 })
  help.autopilot.effective_enabled = false; help.autopilot.project_mode = 'off'
  help.autopilot.rules.accept.days = 1; help.definitions[8].set_by = 'Person'
  await openMenu(page)
  const menu = page.getByRole('menu', { name: 'Status of PHAROS-11' })
  await expect(menu.getByRole('menuitemradio', { name: 'Accepted', exact: true })).toHaveAttribute('data-tip', 'Confirmed by a person or customer')
  await menu.getByRole('menuitem', { name: 'What do these mean?' }).click()
  const sheet = page.getByRole('dialog', { name: 'What the statuses mean' })
  await expect(sheet.locator('[data-rule="accept"]')).toContainText('Off')
  await expect(sheet.locator('[data-rule="accept"] .limit')).toHaveText('1 day')
  await expect(sheet.getByText('Only workspace admins change these.')).toBeVisible()
  await expect(sheet.getByRole('button', { name: 'Change limits' })).toHaveCount(0)
  expect(await sheet.evaluate(element => element.scrollWidth - element.clientWidth)).toBeLessThanOrEqual(1)
  if (shots) await sheet.screenshot({ path: join(shots, 'status-help-mobile.png') })
  await page.keyboard.press('Escape')
  help.autopilot.effective_enabled = true; help.autopilot.rules.new.enabled = false
  await openMenu(page)
  await page.getByRole('menuitem', { name: 'What do these mean?' }).click()
  await expect(sheet.locator('[data-rule="new"]')).toContainText('Off')
  await expect(sheet.locator('[data-rule="blocked"] .off-chip')).toHaveCount(0)
})

test('a person checks and restores the flag under a revision; a read-only ticket has no action', async ({ page }) => {
  const { calls } = await setup(page)
  const ws = panel(page)
  await expect(ws.getByRole('note', { name: 'Needs a human check' })).toContainText('Touch ID on the paired Mac')
  await ws.getByRole('button', { name: 'Mark checked' }).focus()
  await page.keyboard.press('Enter')
  await expect(ws.getByText('Human check done')).toBeVisible()
  await expect(ws.locator('.human-check').getByRole('button', { name: 'Undo' })).toBeFocused()
  const check = calls.find(call => call.method === 'PATCH' && call.path === '/api/nodes/n-1')!
  expect(check.body).toEqual({ human_check: null })
  await page.keyboard.press('Enter')
  await expect(ws.getByRole('note', { name: 'Needs a human check' })).toBeVisible()
  await expect(ws.getByRole('button', { name: 'Mark checked' })).toBeFocused()
  await setup(page, true, { readOnly: true })
  await expect(panel(page).getByRole('note', { name: 'Needs a human check' })).toBeVisible()
  await expect(panel(page).getByRole('button', { name: 'Mark checked' })).toHaveCount(0)
})

test('an agent cannot clear a pending check through the editor and can still save other edits', async ({ page }) => {
  const { calls } = await setup(page, false, { principalKind: 'agent' })
  const ws = panel(page)
  await expect(ws.getByRole('button', { name: 'Mark checked' })).toHaveCount(0)
  await ws.getByRole('button', { name: 'Edit', exact: true }).click()
  const check = ws.getByLabel('Needs a human check', { exact: true })
  await check.fill('')
  await ws.getByRole('button', { name: 'Save', exact: true }).click()
  await expect(page.getByText('Only a person can mark a human check checked.', { exact: true })).toBeVisible()
  await expect(check).toBeFocused()
  expect(calls.filter(call => call.method === 'PATCH' && call.path === '/api/nodes/n-1')).toHaveLength(0)
  await check.fill('Touch ID on the paired Mac')
  await ws.getByLabel('Title', { exact: true }).fill('Title edited by an agent')
  await ws.getByRole('button', { name: 'Save', exact: true }).click()
  await expect(ws.getByRole('button', { name: 'Edit', exact: true })).toBeVisible()
  await expect(ws.getByRole('note', { name: 'Needs a human check' })).toContainText('Touch ID on the paired Mac')
  const patch = calls.find(call => call.method === 'PATCH' && call.path === '/api/nodes/n-1')!
  expect(patch.body).toEqual({ title: 'Title edited by an agent' })
  await ws.getByRole('button', { name: 'Edit', exact: true }).click()
  await check.fill('Check Touch ID and pairing')
  await ws.getByRole('button', { name: 'Save', exact: true }).click()
  await expect(ws.getByRole('note', { name: 'Needs a human check' })).toContainText('Check Touch ID and pairing')
})

test('an agent edits a checked ticket without exposing Undo or erasing completion', async ({ page }) => {
  const data = fixtures()
  const node = data.nodes.find(node => node.id === 'n-1')!
  const completion = { text: 'Touch ID on the paired Mac', by: 'mira', at: '2026-10-01T18:00:00Z' }
  node.human_check = null; node.fields.human_check_completed = completion
  const calls = await mockWork(page, data, { principalKind: 'agent' })
  await page.goto('/p/PHAROS/PHAROS-11')
  const ws = panel(page)
  await expect(ws.getByText('Human check done')).toBeVisible()
  await expect(ws.locator('.human-check').getByRole('button', { name: 'Undo' })).toHaveCount(0)
  await ws.getByRole('button', { name: 'Edit', exact: true }).click()
  await expect(ws.getByLabel('Needs a human check', { exact: true })).toBeDisabled()
  await ws.getByLabel('Title', { exact: true }).fill('Checked ticket edited by an agent')
  await ws.getByRole('button', { name: 'Save', exact: true }).click()
  await expect(ws.getByText('Human check done')).toBeVisible()
  expect(calls.find(call => call.method === 'PATCH' && call.path === '/api/nodes/n-1')!.body).toEqual({ title: 'Checked ticket edited by an agent' })
  expect(node.fields.human_check_completed).toEqual(completion)
})

test('a stale check leaves the pending flag for review; help never chooses a status', async ({ page }) => {
  const { calls } = await setup(page, true, { conflictOn: 'n-1' })
  await panel(page).getByRole('button', { name: 'Mark checked' }).click()
  await expect(panel(page).getByRole('note', { name: 'Needs a human check' })).toBeVisible()
  const before = calls.filter(call => call.method === 'PATCH').length
  await openMenu(page)
  await page.getByRole('menuitem', { name: 'What do these mean?' }).click()
  await expect(page.getByRole('dialog', { name: 'What the statuses mean' })).toBeVisible()
  expect(calls.filter(call => call.method === 'PATCH')).toHaveLength(before)
})

test('human-check filter is linked and removes tickets that do not need a check', async ({ page }) => {
  const { calls } = await setup(page)
  await page.goto('/p/PHAROS')
  await page.getByRole('button', { name: 'Filter by more', exact: true }).click()
  await page.getByRole('menuitem', { name: 'Human check', exact: true }).click()
  const choices = page.getByRole('dialog', { name: 'Filter by Human check' })
  await expect(choices.locator('.facet-option').filter({ hasText: 'Needs a human check' }).locator('.count')).toHaveText('1')
  await expect.poll(() => calls.some(call => call.path === '/api/nodes' && call.query.get('facets') === 'human_check')).toBe(true)
  await page.goto('/p/PHAROS?human_check=pending')
  await expect(page.locator('.tickets tbody .ticket-row').filter({ hasText: 'Connect Hetzner Cloud' })).toHaveCount(1)
  const filter = page.getByRole('button', { name: 'Edit Human check filter: Needs a human check', exact: true })
  await expect(filter).toBeVisible()
  await expect(filter).toContainText('Needs a human check')
  await expect(page.locator('.tickets tbody .ticket-row').filter({ hasText: 'Build the fleet dashboard' })).toHaveCount(0)
})
