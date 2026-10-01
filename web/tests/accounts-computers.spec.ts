// SPDX-License-Identifier: AGPL-3.0-only
// AEON-499: Accounts and computers on /agents, computer first. Each computer is
// a card with its status and its accounts inside, one readiness state each; an
// offline computer says why and the one fix and never calls an account ready;
// pacing is one summary button; destructive actions live in row menus.
// AEON499_SHOTS=<dir> also writes screenshots at 1280 and 390.
import { mkdirSync } from 'node:fs'
import AxeBuilder from '@axe-core/playwright'
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents, type AgentWorld } from './agents-fixtures'
import { NOW, TZ, capacityWorld, type CapacityOptions } from './capacity-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'

test.use({ timezoneId: TZ })
const world: AgentWorld = {
  me: me.id, now: NOW,
  projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' },
  tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' },
  nodes: {
    'p-pharos': { key: 'PRJ-17', title: 'Pharos' }, 'p-aeon': { key: 'PRJ-35', title: 'Aeon' }, 'p-frozen': { key: 'PRJ-26', title: 'Studio infrastructure' },
    'n-1': { key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning' }, 'n-2': { key: 'PHAROS-12', title: 'Add an Oracle Cloud connector' },
    'n-a1': { key: 'AEON-1', title: 'Aeon foundation' }, 'n-5': { key: 'PHAROS-15', title: 'Beacon health probes' }, 'n-6': { key: 'PHAROS-16', title: 'Retire the old dashboard' },
  },
}
async function setup(page: Page, options: CapacityOptions & { manage?: boolean } = {}) {
  await page.clock.setSystemTime(NOW)
  await mockWork(page, fixtures(), { admin: true })
  const data = agentData(world)
  const capacity = capacityWorld(options)
  data.accounts = capacity.accounts as unknown as typeof data.accounts
  data.approvals = data.approvals.filter(a => a.decision)
  data.messages = data.messages.filter(m => !m.is_action_request)
  await mockAgents(page, data, { capacity })
  await page.route('**/api/me/permissions*', route => {
    const answer = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    answer.workspace.permissions = [...answer.workspace.permissions, 'account.read', ...(options.manage === false ? [] : ['account.manage']), 'run.create', 'run.read', 'models.read', 'work_orders.read']
    return route.fulfill({ json: answer })
  })
  return { capacity }
}
const panel = (page: Page) => page.getByRole('region', { name: 'Accounts and computers' })
const computer = (page: Page, name: string) => panel(page).getByRole('region', { name: `Computer ${name}` })
async function open(page: Page) {
  await page.goto('/agents')
  await expect(computer(page, 'mbp2607')).toBeVisible()
}
const shots = process.env.AEON499_SHOTS
async function shot(page: Page, name: string) {
  if (!shots) return
  mkdirSync(shots, { recursive: true })
  await panel(page).screenshot({ path: `${shots}/${name}.png`, animations: 'disabled' })
}

test('one card per computer: offline says why and the fix, and never calls an account ready', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1100 })
  await setup(page)
  await open(page)
  const studio = computer(page, 'studio')
  await expect(studio.getByText(/^Offline since /)).toBeVisible()
  await expect(studio.getByRole('status')).toContainText('stopped reporting at')
  await expect(studio.getByRole('status').locator('code')).toHaveText('aeon-agentd status')
  await expect(studio.getByRole('row')).toHaveCount(1)
  await expect(studio.getByRole('row').first()).toContainText('Paused · computer offline')
  await expect(studio.getByText('Ready', { exact: true })).toHaveCount(0)
  // The legend only where a bar is drawn: the online card has bars, the offline one none.
  await expect(studio.getByText('stop here tonight')).toHaveCount(0)
  const mbp = computer(page, 'mbp2607')
  await expect(mbp.getByText(/^Online · seen /)).toBeVisible()
  await expect(mbp.getByText('stop here tonight')).toBeVisible()
  await expect(mbp.getByRole('row').filter({ hasText: 'Spare' })).toContainText('% left')
  await expect(panel(page).getByText(/ of 6 ready · studio offline$/)).toBeVisible()
  // No trash icon on a row: removal lives in the row menu.
  await expect(panel(page).getByRole('button', { name: /^Remove/ })).toHaveCount(0)
  const axe = await new AxeBuilder({ page }).include('.ac').analyze()
  expect(axe.violations.map(v => `${v.id}: ${v.nodes.map(n => n.target.join(' ')).join(', ')}`)).toEqual([])
  await shot(page, 'desktop-offline-readings')
})

test('no reading yet is said once, with Check now; an offline computer has no reading', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1100 })
  await setup(page, { unmeasured: true, unread: true })
  await open(page)
  const pi = computer(page, 'mbp2607').getByRole('row').filter({ hasText: 'Pi on hsb1' })
  await expect(pi).toContainText(/Pi doesn.t report a usage limit|No reading yet/)
  await expect(computer(page, 'studio').getByRole('row').first()).toContainText('No reading while the computer is offline')
  await shot(page, 'desktop-unmeasured')
})

test('pacing is one summary button; the existing settings and editors open from it', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1100 })
  const { capacity } = await setup(page)
  await open(page)
  const button = panel(page).getByRole('button', { name: /5 days · keep auto · no nights/ })
  await expect(button).toBeVisible()
  await button.click()
  const pacing = page.getByRole('dialog', { name: 'Pacing' })
  await expect(pacing.getByRole('radiogroup', { name: 'Work days a week' })).toBeVisible()
  await shot(page, 'desktop-pacing')
  await pacing.getByRole('radio', { name: '7' }).click()
  await expect.poll(() => capacity.puts.length).toBeGreaterThan(0)
  await expect(panel(page).getByRole('button', { name: /7 days · keep auto · no nights/ })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(pacing).toHaveCount(0)
})

test('the row menu carries Sprint, Hold and Remove; the computer menu its own actions', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1100 })
  await setup(page)
  await open(page)
  await computer(page, 'mbp2607').getByRole('button', { name: 'More for Codex Spare' }).click()
  const menu = page.getByRole('menu')
  await expect(menu.getByRole('menuitem', { name: /Sprint Codex until reset/ })).toBeVisible()
  await expect(menu.getByRole('menuitem', { name: /Remove account/ })).toBeVisible()
  await shot(page, 'desktop-row-menu')
  await page.keyboard.press('Escape')
  await expect(menu).toHaveCount(0)
  await computer(page, 'studio').getByRole('button', { name: 'More for studio' }).click()
  await expect(page.getByRole('menu').getByRole('menuitem', { name: /Details/ })).toBeVisible()
})

test('phone: stacked rows, no sideways scroll', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 900 })
  await setup(page)
  await open(page)
  await expect(computer(page, 'studio').getByRole('status')).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true)
  await shot(page, 'phone')
})
