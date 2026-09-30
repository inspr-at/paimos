// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents, type AgentWorld } from './agents-fixtures'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'

const at = (minutes: number) => new Date(Date.now() + minutes * 60_000).toISOString()

function world(mode?: 'both') {
  const data = fixtures()
  const set = (key: string, eta: Record<string, unknown>) => { data.nodes.find(node => node.key === key)!.eta = eta }
  set('PHAROS-11', { eta_ready_at: at(25), progress_pct: 40, ready_by: 'Ada', ready_reported_at: at(-1) })
  set('PHAROS-12', { eta_ready_at: at(-5), progress_pct: 90, ready_by: 'Beau', ready_reported_at: at(-2) })
  set('PHAROS-13', { eta_ready_at: at(10), ready_by: 'Cleo', ready_reported_at: at(-34), ready_stale: true, eta_stale: true })
  if (mode) data.preferences['eta-display'] = { mode }
  return data
}

const row = (page: Page, key: string) => page.locator('tr.ticket-row:not(.ghost)').filter({ has: page.locator('.key', { hasText: new RegExp(`^${key}$`) }) })

test('the ETA column shows fresh, overdue and stale estimates under its header and stays empty when none was reported', async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 900 })
  await mockWork(page, world())
  const sorts: string[] = []
  page.on('request', request => {
    const url = new URL(request.url())
    if (request.method() === 'GET' && url.pathname === '/api/nodes' && url.searchParams.get('sort')?.includes('eta_ready')) sorts.push(url.searchParams.get('sort')!)
  })
  await page.goto('/p/PHAROS')
  const header = page.getByRole('columnheader', { name: 'ETA' })
  await expect(header).toBeVisible()
  const fresh = row(page, 'PHAROS-11').locator('.eta-cell')
  await expect(fresh.locator('.when')).toHaveText(/^~2\d min$/)
  await expect(fresh.locator('.pct')).toHaveText('40%')
  await expect(fresh).toHaveAttribute('data-tip', /Ready at \d{2}:\d{2}, in ~2\d min\nEstimated by Ada at \d{2}:\d{2}\n40% done/)
  const overdue = row(page, 'PHAROS-12').locator('.eta-cell')
  await expect(overdue).toHaveClass(/overdue/)
  await expect(overdue.locator('.when')).toHaveText(/^overdue [45] min$/)
  const stale = row(page, 'PHAROS-13').locator('.eta-cell')
  await expect(stale).toHaveClass(/stale/)
  await expect(stale.locator('svg')).toHaveCount(1)
  await expect(stale).toHaveAttribute('data-tip', /Estimate from \d{2}:\d{2} by Cleo is 3\d min old/)
  await expect(row(page, 'PHAROS-14').locator('.eta-cell')).toHaveCount(0)

  // Values end on the header's right edge, and never spill out of their cell.
  const headerBox = (await header.locator('.th-sort').boundingBox())!
  for (const key of ['PHAROS-11', 'PHAROS-12', 'PHAROS-13']) {
    const cell = row(page, key).locator('td.c-eta')
    const value = (await cell.locator('.eta-cell').boundingBox())!
    expect(Math.abs(value.x + value.width - (headerBox.x + headerBox.width - 6))).toBeLessThan(3)
    expect(await cell.evaluate(td => { const el = td.querySelector('.when > .shown') as HTMLElement; return el.scrollWidth <= el.clientWidth })).toBe(true)
  }

  await header.getByRole('button', { name: 'ETA' }).click()
  await expect.poll(() => sorts.some(sort => sort.split(',').includes('eta_ready'))).toBe(true)

  // Phones show the estimate where Updated sits.
  await page.setViewportSize({ width: 390, height: 844 })
  await expect(row(page, 'PHAROS-11').locator('.c-eta .eta-cell')).toBeVisible()
  await expect(row(page, 'PHAROS-11').locator('.c-updated')).toBeHidden()
  await expect(row(page, 'PHAROS-14').locator('.c-updated')).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true)
})

test('with both forms chosen, the clock time shows on hover', async ({ page }) => {
  await mockWork(page, world('both'))
  await page.goto('/p/PHAROS')
  const fresh = row(page, 'PHAROS-11').locator('.eta-cell')
  await expect(fresh.locator('.shown')).toHaveText(/^~2\d min$/)
  await expect(fresh.locator('.hover')).toHaveText(/^\d{2}:\d{2}$/)
  await expect(fresh.locator('.hover')).toBeHidden()
  await fresh.hover()
  await expect(fresh.locator('.hover')).toBeVisible()
})

const agentsWorld: AgentWorld = {
  me: me.id,
  projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' },
  tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' },
  nodes: {
    'p-pharos': { key: 'PRJ-17', title: 'Pharos' }, 'p-aeon': { key: 'PRJ-35', title: 'Aeon' }, 'p-frozen': { key: 'PRJ-26', title: 'Studio infrastructure' },
    'n-1': { key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning' }, 'n-2': { key: 'PHAROS-12', title: 'Add an Oracle Cloud connector' },
    'n-a1': { key: 'AEON-1', title: 'Aeon foundation' }, 'n-5': { key: 'PHAROS-15', title: 'Beacon health probes' }, 'n-6': { key: 'PHAROS-16', title: 'Retire the old dashboard' },
  },
}
const sessionRow = (page: Page, n: number) => page.locator(`[data-row="s:5e000000-0000-4000-8000-0000000000${String(n).padStart(2, '0')}"]`)

for (const width of [1600, 1100, 390]) {
  test(`the sessions table carries the bound ticket's estimate without overflowing its columns at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 })
    await mockWork(page, fixtures(), { admin: true })
    const data = agentData({ ...agentsWorld })
    Object.assign(data.sessions[1]!, { eta_ready_at: at(25), progress_pct: 40, eta_reported_at: at(-2) })
    Object.assign(data.sessions[2]!, { eta_ready_at: at(10), progress_pct: 70, eta_reported_at: at(-34), eta_stale: true })
    Object.assign(data.sessions[3]!, { eta_ready_at: at(-5), progress_pct: 90, eta_reported_at: at(-3) })
    Object.assign(data.sessions[6]!, { eta_ready_at: at(30), progress_pct: 100 })
    await mockAgents(page, data)
    await page.goto('/agents')
    await expect(page.getByRole('heading', { name: 'Agents', level: 1 })).toBeVisible()
    await expect(sessionRow(page, 2).locator('.eta-cell .when')).toHaveText(/^~2\d min$/)
    await expect(sessionRow(page, 3).locator('.eta-cell')).toHaveClass(/stale/)
    await expect(sessionRow(page, 4).locator('.eta-cell')).toHaveClass(/overdue/)
    await expect(sessionRow(page, 4).locator('.eta-cell .when')).toHaveText(/^overdue [45] min$/)
    // Stopped sessions stay empty; working sessions without a time show a quiet hint.
    await expect(page.locator('.row .eta-cell .when')).toHaveCount(3)
    await expect(sessionRow(page, 1).locator('.eta-cell')).toHaveText('no ETA')
    await expect(sessionRow(page, 7).locator('.eta-cell')).toHaveCount(0)
    for (const n of [2, 3, 4]) {
      const fits = await sessionRow(page, n).evaluate(row => [...row.querySelectorAll<HTMLElement>(':scope > [role="cell"]')].every(cell => cell.scrollWidth <= cell.clientWidth + 1)
        && [...row.querySelectorAll<HTMLElement>('.when > .shown')].every(el => el.scrollWidth <= el.clientWidth))
      expect(fits).toBe(true)
    }
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true)
  })
}

test('the session panel names the estimate next to the running time', async ({ page }) => {
  await mockWork(page, fixtures(), { admin: true })
  const data = agentData({ ...agentsWorld })
  Object.assign(data.sessions[1]!, { eta_ready_at: at(25), progress_pct: 40, eta_reported_at: at(-2) })
  await mockAgents(page, data)
  await page.goto('/agents/5e000000-0000-4000-8000-000000000002')
  const panel = page.getByRole('complementary', { name: 'Session details' })
  const eta = panel.locator('.eta-cell')
  await expect(eta.locator('.kind')).toHaveText('Ready')
  await expect(eta.locator('.when')).toHaveText(/^~2\d min$/)
  await expect(eta.locator('.pct')).toHaveText('40%')
})

for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) {
  test(`missing ETA and estimate hints remain quiet at ${width}px in ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    const data = fixtures()
    data.preferences.theme = { choice: theme }
    data.preferences['list:p-pharos'] = { visible: ['key', 'title', 'status', 'estimate', 'updated', 'progress', 'eta'] }
    data.nodes.find(node => node.key === 'PHAROS-14')!.eta = { has_working_session: true }
    data.nodes.find(node => node.key === 'PHAROS-13')!.eta = { has_working_session: true, progress_pct: 0 }
    await mockWork(page, data, { admin: true })
    await page.goto('/p/PHAROS')
    await expect(row(page, 'PHAROS-14').locator('.eta-cell')).toHaveText('no ETA')
    await expect(row(page, 'PHAROS-13').locator('.eta-cell')).toHaveText(/0%\s*no ETA/)
    await expect(row(page, 'PHAROS-13').locator('.eta-cell')).toHaveAttribute('data-tip', /0% done\nNo ETA reported/)
    await expect(row(page, 'PHAROS-13').locator('.eta-cell .sr-only')).toContainText('No ETA reported')
    if (width === 1600) await expect(row(page, 'PHAROS-14').locator('.c-estimate .empty')).toHaveAttribute('data-tip', 'No estimate yet')
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true)
    const shots = process.env.AEON_443_SHOTS
    if (shots) { mkdirSync(shots, { recursive: true }); await page.screenshot({ path: join(shots, `tickets-${width}-${theme}.png`), fullPage: true }) }
    await mockAgents(page, agentData({ ...agentsWorld }))
    await page.goto('/agents')
    await expect(sessionRow(page, 1).locator('.eta-cell')).toHaveText('no ETA')
    await expect(sessionRow(page, 7).locator('.eta-cell')).toHaveCount(0)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true)
    if (shots) { await sessionRow(page, 1).scrollIntoViewIfNeeded(); await page.screenshot({ path: join(shots, `agents-${width}-${theme}.png`) }) }
  })
}
