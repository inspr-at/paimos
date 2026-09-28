// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { agentData, mockAgents, type AgentWorld } from './agents-fixtures'

const world: AgentWorld = { me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' }, nodes: {
  'p-pharos': { key: 'PRJ-17', title: 'Pharos' }, 'p-aeon': { key: 'PRJ-35', title: 'Aeon' }, 'p-frozen': { key: 'PRJ-26', title: 'Studio infrastructure' },
  'n-1': { key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning' }, 'n-2': { key: 'PHAROS-12', title: 'Add an Oracle Cloud connector' }, 'n-a1': { key: 'AEON-1', title: 'Aeon foundation' }, 'n-5': { key: 'PHAROS-15', title: 'Beacon health probes' }, 'n-6': { key: 'PHAROS-16', title: 'Retire the old dashboard' },
} }
async function setup(page: Page, admin = true) {
  await mockWork(page, fixtures(), { admin })
  const data = agentData(world)
  await mockAgents(page, data)
  const selected = data.sessions[1]!
  Object.assign(selected, { management_mode: 'unmanaged', display_label: 'ghost-worker', advertised_capabilities: ['status'] })
  const bodies: Record<string, unknown>[] = []
  await page.route('**/harness-sessions/*/remove', async route => {
    bodies.push(route.request().postDataJSON())
    Object.assign(selected, { archived_at: new Date().toISOString(), recovery_process_state: 'unknown', phase: 'stopped', stopped_at: new Date().toISOString(), stop_reason: 'removed_process_unknown' })
    await route.fulfill({ json: { session: selected, message: 'Record removed; process not stopped by removal.', processes_signalled: false, process_state: 'unknown' } })
  })
  return { data, selected, bodies }
}
const row = (page: Page, id: string) => page.locator(`[data-row="s:${id}"]`)
const confirm = (page: Page) => page.getByRole('dialog', { name: 'Remove ghost-worker from Agents?' })

test('two clicks remove a live row and card; Removed preserves history and survives refresh', async ({ page }) => {
  const errors = watchErrors(page)
  const { selected, bodies } = await setup(page)
  await page.goto('/agents')
  const sessions = page.getByRole('region', { name: 'Sessions', exact: true })
  const before = await sessions.locator('.sub').innerText()
  await row(page, selected.id).getByRole('button', { name: 'Remove ghost-worker' }).click()
  await expect(confirm(page)).toContainText('The process is not stopped; late heartbeats are ignored.')
  await confirm(page).getByRole('button', { name: 'Remove', exact: true }).click()
  await expect(row(page, selected.id)).toHaveCount(0)
  await expect(page.getByRole('button', { name: /^Open ghost-worker,/ })).toHaveCount(0)
  await expect(sessions.locator('.sub')).not.toHaveText(before)
  expect(bodies).toEqual([{ reason: 'Removed from Agents by a person' }])
  await page.getByRole('button', { name: /^Removed/ }).click()
  await expect(row(page, selected.id)).toBeVisible()
  await row(page, selected.id).getByRole('link').first().click()
  await expect(page.getByRole('complementary', { name: 'Session details' })).toContainText('process state unknown')
  await page.reload()
  await expect(page.getByRole('complementary', { name: 'Session details' })).toContainText('ghost-worker')
  expect(errors).toEqual([])
})

test('panel Remove closes panel after confirmation and Cancel keeps the record', async ({ page }) => {
  const { selected, bodies } = await setup(page)
  await page.goto(`/agents/${selected.id}`)
  const panel = page.getByRole('complementary', { name: 'Session details' })
  await panel.getByRole('button', { name: 'Remove ghost-worker' }).click()
  await confirm(page).getByRole('button', { name: 'Cancel' }).click()
  expect(bodies).toHaveLength(0)
  await panel.getByRole('button', { name: 'Remove ghost-worker' }).click()
  await confirm(page).getByRole('button', { name: 'Remove', exact: true }).click()
  await expect(page).toHaveURL(/\/agents$/)
  await expect(panel).toHaveCount(0)
})

test('card removes in two clicks and mobile overflow also offers Remove', async ({ page }) => {
  const { selected } = await setup(page)
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto('/agents')
  await row(page, selected.id).getByRole('button', { name: 'Actions for ghost-worker' }).click()
  await expect(page.getByRole('menuitem', { name: 'Remove ghost-worker' })).toBeVisible()
  await page.keyboard.press('Escape')
  await page.locator('.live-now .tile').filter({ hasText: 'ghost-worker' }).getByRole('button', { name: 'Remove ghost-worker' }).click()
  await confirm(page).getByRole('button', { name: 'Remove', exact: true }).click()
  await expect(row(page, selected.id)).toHaveCount(0)
})

test('project member can remove without force permissions', async ({ page }) => {
  const { selected } = await setup(page, false)
  await page.goto(`/agents/${selected.id}`)
  await expect(page.getByRole('complementary').getByRole('button', { name: 'Remove ghost-worker' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Recover', exact: true })).toHaveCount(0)
})

test('agents never receive Remove controls', async ({ page }) => {
  const { selected } = await setup(page)
  await page.route('**/api/me', route => route.fulfill({ json: { principal: { id: me.id, name: me.name, kind: 'agent', roles: ['admin'] }, tenant: { id: 't1', name: 'INSPR Studio' } } }))
  await page.goto(`/agents/${selected.id}`)
  await expect(page.getByRole('complementary', { name: 'Session details' })).toBeVisible()
  await expect(page.getByRole('button', { name: /^Remove / })).toHaveCount(0)
})

test('failed removal keeps the row and allows retry', async ({ page }) => {
  const { selected } = await setup(page)
  await page.route('**/harness-sessions/*/remove', route => route.fulfill({ status: 503, json: { error: 'Please retry removal' } }))
  await page.goto('/agents')
  await row(page, selected.id).getByRole('button', { name: 'Remove ghost-worker' }).click()
  await confirm(page).getByRole('button', { name: 'Remove', exact: true }).click()
  await expect(page.getByText('Please retry removal', { exact: true })).toBeVisible()
  await expect(row(page, selected.id)).toBeVisible()
})

test('batch removal confirms once and applies returned records immediately', async ({ page }) => {
  const { data, selected } = await setup(page)
  selected.heartbeat_at = new Date(Date.now() - 16 * 60_000).toISOString()
  const calls: string[] = []
  await page.route('**/harness-sessions/remove-stale', route => {
    calls.push(route.request().url())
    const items = data.sessions.filter(s => route.request().url().includes(`/projects/${s.project_id}/`) && Date.parse(s.heartbeat_at ?? s.created_at) < Date.now() - 15 * 60_000).map(s => {
      Object.assign(s, { archived_at: new Date().toISOString(), stopped_at: new Date().toISOString(), phase: 'stopped' })
      return { session: s, message: 'Record removed; process not stopped by removal.', processes_signalled: false, process_state: 'unknown' }
    })
    return route.fulfill({ json: { items, cutoff: new Date(Date.now() - 15 * 60_000).toISOString() } })
  })
  await page.goto('/agents')
  await page.getByRole('button', { name: 'Remove all stopped/stale', exact: true }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Remove all stopped/stale', exact: true }).click()
  await expect(row(page, selected.id)).toHaveCount(0)
  expect(calls.length).toBeGreaterThan(0)
})

for (const theme of ['light', 'dark']) for (const width of [1600, 390]) {
  test(`removal visual ${width} ${theme}`, async ({ page }) => {
    const { selected } = await setup(page)
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.goto('/agents')
    await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
    const shotDir = `${process.env.REMOVAL_SCREENSHOT_DIR ?? '../.agent-artifacts/session-removal'}/${process.env.REMOVAL_ITERATION ?? 'iter1'}`
    await expect(row(page, selected.id)).toBeVisible()
    await page.screenshot({ path: `${shotDir}/agents-${width}-${theme}.png`, fullPage: true })
    await row(page, selected.id).getByRole('button', { name: 'Remove ghost-worker' }).click()
    await expect(confirm(page)).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    expect(await confirm(page).evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
    await page.screenshot({ path: `${shotDir}/remove-${width}-${theme}.png` })
  })
}

for (const width of [600, 800, 1000]) {
  test(`row removal fits at ${width}px`, async ({ page }) => {
    const { selected } = await setup(page)
    await page.setViewportSize({ width, height: 900 })
    await page.goto('/agents')
    const button = row(page, selected.id).getByRole('button', { name: 'Remove ghost-worker' })
    await expect(button).toBeVisible()
    const rect = await button.boundingBox()
    expect(rect!.x).toBeGreaterThanOrEqual(0)
    expect(rect!.x + rect!.width).toBeLessThanOrEqual(width)
    await button.click()
    await expect(confirm(page)).toBeVisible()
  })
}
