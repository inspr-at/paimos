// SPDX-License-Identifier: AGPL-3.0-only
// The approved A4 retirement replaces dialog-only cases with page entry-point
// regressions; orders, locks, Undo and identity safety live in models-board specs.
import { expect, test } from '@playwright/test'
import { fixtures, liveAgent, mockWork } from './work-fixtures'
import { mockModelsSettings } from './models-settings-page'

async function setup(page: import('@playwright/test').Page) {
  const data = fixtures()
  data.preferences['list:p-pharos'] = { visible: ['status', 'model'] }
  const ticket = data.nodes.find(node => node.key === 'PHAROS-11')!
  ticket.fields.area = 'backend'
  ticket.planning = { route: { label: 'Codex Sol', profile: 'model-codex-high', harness: 'codex', model: 'gpt-6.1-sol', effort: 'high', revision: '1' }, tokens: { spent: null, input: 0, output: 0, cached: 0, sessions: 0, unreported: 0, estimated: null } }
  data.live.push(liveAgent({ project_id: 'p-aeon', name: 'worker', session_id: 'prefs-worker' }))
  await mockWork(page, data, { admin: true })
  return mockModelsSettings(page, { fallback: true })
}

test('Agents model preferences navigates to the page without opening the old dialog stack', async ({ page }) => {
  await setup(page); await page.goto('/agents')
  await page.getByRole('button', { name: 'More agent actions', exact: true }).click()
  await page.getByRole('menuitem', { name: 'Model preferences', exact: true }).click()
  await expect(page).toHaveURL(/\/settings\/models\?layer=mine/)
  await expect(page.locator('[data-models-ready="true"]')).toBeVisible()
  await expect(page.locator('dialog[open]')).toHaveCount(0)
})
test('LiveAgents preferences keeps the project rules scope in the Models URL', async ({ page }) => {
  await setup(page); await page.goto('/')
  await page.locator('[data-project-id="p-aeon"] .live-chip').click()
  await page.getByRole('button', { name: 'Model preferences for Aeon', exact: true }).click()
  await expect(page).toHaveURL(/\/settings\/models\?project_id=p-aeon&layer=rules/)
  await expect(page.locator('[data-models-ready="true"]')).toBeVisible()
  await expect(page.locator('dialog[open]')).toHaveCount(0)
})
test('PlanningCell opens the server explanation with the captured ticket, kind and project', async ({ page }) => {
  await setup(page); await page.goto('/p/PHAROS/tickets')
  const planning = page.getByRole('button', { name: /Codex Sol.*Why this model/ }).first()
  await planning.click()
  await expect(page).toHaveURL(/\/settings\/models\?/)
  const url = new URL(page.url())
  expect(url.pathname).toBe('/settings/models')
  expect(url.searchParams.get('kind')).toBe('backend')
  expect(url.searchParams.get('ticket')).toBeTruthy()
  expect(url.searchParams.get('project_id')).toBe('p-pharos')
  expect(url.searchParams.get('why')).toBe('1')
  await expect(page.getByRole('dialog', { name: 'Why this model?' })).toBeVisible()
  await expect(page.locator('dialog[open]')).toHaveCount(0)
})
