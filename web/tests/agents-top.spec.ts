// SPDX-License-Identifier: AGPL-3.0-only
// AEON-299, reduced by AEON-782: the top of /agents. The compact live line and
// Needs you (only when something waits) stay here. Accounts and computers is a
// status line since AEON-782 (accounts-computers.spec.ts); its pacing, plan
// card, account menu and editors live in Settings (settings-account-pacing.spec.ts).
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
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
interface Setup extends CapacityOptions { needs?: boolean; manage?: boolean }
async function setup(page: Page, options: Setup = {}) {
  await page.clock.setSystemTime(NOW)
  await mockWork(page, fixtures(), { admin: true })
  const data = agentData(world)
  const capacity = capacityWorld(options)
  data.accounts = capacity.accounts as unknown as typeof data.accounts
  if (!options.needs) {
    // Nothing waits: every request is decided, no held action request.
    data.approvals = data.approvals.filter(a => a.decision)
    data.messages = data.messages.filter(m => !m.is_action_request)
  }
  const calls = await mockAgents(page, data, { capacity })
  await page.route('**/api/me/permissions*', route => {
    const answer = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    answer.workspace.permissions = [...answer.workspace.permissions, 'account.read', ...(options.manage === false ? [] : ['account.manage']), 'run.create', 'run.read', 'models.read', 'work_orders.read']
    return route.fulfill({ json: answer })
  })
  return { data, capacity, calls }
}
const cap = (page: Page) => page.getByRole('region', { name: 'Accounts and computers' })
async function open(page: Page) {
  await page.goto('/agents')
  await expect(page.getByRole('heading', { name: 'Agents', level: 1 })).toBeVisible()
  await expect(cap(page).locator('.fs-sum')).not.toContainText('Reading')
}
const noScroll = (page: Page) => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)

test('nothing waits: no Needs you, a compact live line, and Accounts and computers as one status line', async ({ page }) => {
  const errors = watchErrors(page)
  await setup(page)
  await open(page)
  await expect(page.getByRole('region', { name: 'Needs you' })).toHaveCount(0)
  await expect(page.getByRole('heading', { name: 'Live now' })).toHaveCount(0)
  const line = page.getByRole('group', { name: 'Show sessions by state' })
  await expect(line.getByRole('button', { name: /^\d+ working/ })).toBeVisible()
  await expect(cap(page).locator('.fs-sum')).toContainText(/\d+ of 6 ready/)
  // No coloured edge accents anywhere in the new top (AGENTS.md rule 11).
  await cap(page).getByRole('button', { name: 'Accounts and computers', exact: true }).click()
  const edges = await page.locator('.acc-section, .acc-section *, .head-counts, .head-counts *').evaluateAll(els => els.filter(el => { const s = getComputedStyle(el); return ['Left', 'Top'].some(side => parseFloat(s[`border${side}Width` as 'borderLeftWidth']) >= 3 && s[`border${side}Style` as 'borderLeftStyle'] !== 'none') }).length)
  expect(edges).toBe(0)
  expect(await noScroll(page)).toBe(true)
  expect(errors).toEqual([])
})

test('Needs you appears only when something waits, with the sign-in and its command', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  await setup(page, { needs: true, signin: true })
  await open(page)
  const needs = page.getByRole('region', { name: 'Needs you' })
  await expect(needs).toBeVisible()
  const signin = needs.getByRole('listitem', { name: 'Cursor needs a new sign-in on mbp2607' })
  await expect(signin).toContainText('Run cursor-agent login there · agents skip this account until then')
  await signin.getByRole('button', { name: 'Copy command' }).click()
  await expect(page.getByText('Copied: cursor-agent login — run it on mbp2607.')).toBeVisible()
})

// AEON-299 review 2: a computer-wide login flag or a check that could not run
// is not a sign-in prompt; only a confirmed sign-out for that account is.
test('sign-in prompts only for a confirmed sign-out of that account', async ({ page }) => {
  await setup(page, { computerLogin: true, unavailable: true })
  await open(page)
  await expect(page.getByRole('region', { name: 'Needs you' })).toHaveCount(0)
})

test('Manage opens Settings / Accounts', async ({ page }) => {
  await setup(page, { stale: true })
  await open(page)
  await cap(page).getByRole('link', { name: /Manage/ }).click()
  await expect(page).toHaveURL('/settings/accounts')
  await expect(page.locator('#agent-accounts')).toContainText('Spare')
})
