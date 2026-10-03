// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { expectStableControls } from './helpers/stable'

const shots = 'test-results/aeon-645'
const countRoot = (page: Page) => page.getByRole('group', { name: 'Filter tickets by status', exact: true })
const group = (page: Page, id: string) => countRoot(page).locator(`[data-group="${id}"] .group-count`)
const status = (page: Page, state: string) => countRoot(page).locator(`[data-state="${state}"]`)
const rows = (page: Page) => page.getByRole('grid', { name: 'Tickets' }).locator('tr.ticket-row:not(.ghost)')
const row = (page: Page, key: string) => rows(page).filter({ has: page.locator('.key', { hasText: new RegExp(`^${key}$`) }) })
const hide = (page: Page) => page.getByRole('checkbox', { name: 'Hide closed', exact: true })
const density = (page: Page, name: string) => page.getByRole('radio', { name: `${name} project header`, exact: true })
async function settled(page: Page) { await page.evaluate(async () => { await document.fonts.ready; await new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))) }) }
async function screenshot(page: Page, name: string) { for (const close of await page.locator('.toast-close').all()) await close.click(); await expect(page.locator('.toast')).toHaveCount(0); await settled(page); mkdirSync(shots, { recursive: true }); await page.screenshot({ path: `${shots}/${name}.png` }) }
async function roomy(page: Page, width: number) {
  if (width > 600) { await density(page, 'Comfortable').click(); return }
  await page.getByRole('button', { name: 'Filters', exact: true }).click()
  const sheet = page.getByRole('dialog', { name: 'Filters', exact: true })
  await sheet.getByRole('radiogroup', { name: 'Project header density' }).getByRole('radio', { name: 'Comfortable', exact: true }).click()
  await sheet.locator('footer button').click()
}
async function setup(page: Page, width = 1440, theme = 'light', extraDoing = 0) {
  await page.setViewportSize({ width, height: 900 })
  const data = fixtures()
  data.preferences.theme = { choice: theme }
  data.preferences['list:display'] = { headerGraph: false }
  data.projects[0]!.title = 'Pharos · Betriebsübersicht'
  data.projects[0]!.description = 'Überprüfung der mandantenübergreifenden Berechtigungsverwaltung und außergewöhnlich langer Projektbeschreibungen für sämtliche verantwortlichen Personen und Agenten.'
  const seed = data.nodes.find(n => n.id === 'n-5')!
  for (const [index, state] of ['delivered', 'accepted', 'archived', 'blocked', 'open', 'inprogress', 'canceled'].entries()) {
    data.nodes.push({ ...seed, fields: {}, id: `extra-${index}`, key: `PHAROS-${30 + index}`, state, title: `Überprüfung der Berechtigungsverwaltung ${state}` })
  }
  for (let index = 0; index < extraDoing; index++) data.nodes.push({ ...seed, fields: {}, id: `growth-${index}`, key: `PHAROS-${50 + index}`, state: 'qa', title: `QA growth ${index}` })
  let summaryRequests = 0
  page.on('request', request => { if (new URL(request.url()).pathname === '/api/projects') summaryRequests++ })
  await mockWork(page, data)
  await page.route('**/api/queue?*', route => route.fulfill({ json: { items: [], manual_order: false, capacity: { queued_hours: 0, parallel_runs: 0, work_hours: 0, warning: false } } }))
  await page.goto('/p/PHAROS')
  await expect(row(page, 'PHAROS-11')).toBeVisible()
  return { data, summaryRequests: () => summaryRequests }
}

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark']) {
  test(`status filters and stable grid at ${width}px ${theme}`, async ({ page }) => {
    const errors: string[] = []; page.on('pageerror', error => errors.push(error.message))
    const probe = await setup(page, width, theme)
    const controls = { open: group(page, 'open'), doing: group(page, 'in_progress'), done: group(page, 'done'), new: page.getByRole('button', { name: 'New ticket', exact: true }) }
    await expectStableControls({ controls, scrollAreas: { page: page.locator('.project-page') }, interactions: [
      { name: 'Open group', run: async () => { await group(page, 'open').click(); await expect(group(page, 'open')).toHaveAttribute('aria-pressed', 'true'); await expect(row(page, 'PHAROS-14')).toBeVisible(); await expect(row(page, 'PHAROS-11')).toHaveCount(0) } },
      { name: 'clear Open', run: async () => { await group(page, 'open').click(); await expect(page).not.toHaveURL(/status=/); await expect(row(page, 'PHAROS-11')).toBeVisible() } },
      { name: 'Doing includes aliases and QA', run: async () => { await group(page, 'in_progress').click(); await expect(rows(page)).toHaveCount(3); await expect(row(page, 'PHAROS-35')).toBeVisible() } },
      { name: 'Done shows finished work', run: async () => { await group(page, 'done').click(); await expect(rows(page)).toHaveCount(3); await expect(page).toHaveURL(/closed=1/); await expect(group(page, 'done')).toHaveAttribute('aria-pressed', 'true') } },
      { name: 'clear Done restores Hide', run: async () => { await group(page, 'done').click(); await expect(page).not.toHaveURL(/closed=1/); await expect(row(page, 'PHAROS-15')).toHaveCount(0) } },
    ] })
    await screenshot(page, `${width}-${theme}-compact`)
    await roomy(page, width)
    await expect(page.locator('.project-page')).toHaveClass(/header-comfortable/)
    if (width === 390) {
      await expect(countRoot(page).locator('.status-count').first()).toBeHidden()
      await expect(group(page, 'closed')).toBeHidden()
    } else {
      await expect(countRoot(page).locator('.status-count')).toHaveCount(11)
      for (const id of ['open', 'in_progress', 'done', 'closed']) {
        const items = countRoot(page).locator(`[data-group="${id}"] .status-items`)
        expect(await items.evaluate(element => getComputedStyle(element).gridTemplateColumns.split(' ').length)).toBe(2)
      }
      await expect(status(page, 'done')).toHaveClass(/is-hidden/)
      await expect(status(page, 'cancelled').locator('b')).toHaveText('2')
      await expectStableControls({ controls: { ...controls, grid: countRoot(page), accepted: status(page, 'accepted'), cancelled: status(page, 'cancelled'), archived: status(page, 'archived'), hide: hide(page), display: page.locator('.project-navigation').getByRole('button', { name: 'Display: Display', exact: true }) },
        scrollAreas: { page: page.locator('.project-page') }, interactions: [
          { name: 'Cancelled includes canceled', run: async () => { await status(page, 'cancelled').click(); await expect(rows(page)).toHaveCount(2); await expect(hide(page)).not.toBeChecked(); await expect(status(page, 'cancelled')).toHaveAttribute('aria-pressed', 'true') } },
          { name: 'Accepted replaces selection', run: async () => { await status(page, 'accepted').click(); await expect(rows(page)).toHaveCount(1); await expect(row(page, 'PHAROS-31')).toBeVisible() } },
          { name: 'Archived', run: async () => { await status(page, 'archived').click(); await expect(row(page, 'PHAROS-32')).toBeVisible(); await expect(rows(page)).toHaveCount(1) } },
          { name: 'clear with toolbar removal', run: async () => { await page.getByRole('button', { name: 'Remove Status filter' }).click(); await expect(hide(page)).toBeChecked(); await expect(page).not.toHaveURL(/status=/) } },
        ] })
    }
    await screenshot(page, `${width}-${theme}-comfortable`)
    if (width === 390) await page.locator('.phone-header-fold').click(); else await density(page, 'Collapsed').click()
    await expect(countRoot(page)).toBeHidden()
    await screenshot(page, `${width}-${theme}-collapsed`)
    expect(probe.summaryRequests(), 'one summary, no per-status requests').toBe(1)
    expect(errors).toEqual([])
  })
}

test('manual Hide toggles stick when a status is cleared; visible selection restores only automatic overrides', async ({ page }) => {
  await setup(page); await roomy(page, 1440)
  await status(page, 'done').click(); await expect(hide(page)).not.toBeChecked()
  await status(page, 'new').click(); await expect(hide(page)).toBeChecked(); await expect(page).not.toHaveURL(/closed=1/)
  await status(page, 'done').click(); await expect(hide(page)).not.toBeChecked()
  await hide(page).check(); await expect(rows(page)).toHaveCount(0)
  await hide(page).uncheck(); await expect(row(page, 'PHAROS-15')).toBeVisible()
  await status(page, 'done').click(); await expect(page).not.toHaveURL(/status=/); await expect(hide(page)).not.toBeChecked()
  await expect(row(page, 'PHAROS-15')).toBeVisible()
})

test('canonical status and bucket filters survive reload, Outline and Clear all', async ({ page }) => {
  await setup(page); await group(page, 'done').click(); await expect(page).toHaveURL(/status_scope=done/)
  await page.reload(); await expect(group(page, 'done')).toHaveAttribute('aria-pressed', 'true'); await expect(row(page, 'PHAROS-15')).toBeVisible()
  await page.getByRole('tab', { name: 'Outline', exact: true }).click(); await expect(page).toHaveURL(/status_scope=done/)
  await page.getByRole('tab', { name: 'List', exact: true }).click(); await expect(rows(page)).toHaveCount(3)
  await page.getByRole('button', { name: 'Clear all', exact: true }).click(); await expect(page).not.toHaveURL(/status_scope=/); await expect(hide(page)).toBeChecked(); await expect(page).not.toHaveURL(/hide_restore=|closed=1/)
})

test('older and partial summary detail is labelled honestly', async ({ page }) => {
  await setup(page)
  // Override the fixture route directly: group totals remain available.
  await page.route('**/api/projects**', route => route.fulfill({ json: { items: [{ id: 'p-pharos', key: 'PRJ-17', title: 'Pharos', state: 'active', open: 4, in_progress: 2, done: 3, cancelled: 1, total: 10, last_activity: '2026-10-03T12:00:00Z', status_counts: [], status_counts_truncated: true }] } }))
  await page.reload(); await roomy(page, 1440)
  await expect(countRoot(page).getByText('Status counts are partial; group totals are complete.')).toBeVisible()
  await expect(status(page, 'done').locator('b')).toHaveText('—')
  await expect(group(page, 'done').locator('b')).toHaveText('3')
})

for (const width of [1024, 1440]) test(`live count digit growth stays still at ${width}px`, async ({ page }) => {
  await setup(page, width, 'light', 6); await roomy(page, width)
  await expect(group(page, 'in_progress').locator('b')).toHaveText('9')
  await expectStableControls({ controls: { grid: countRoot(page), open: group(page, 'open'), doing: group(page, 'in_progress'), done: group(page, 'done'), qa: status(page, 'qa'), accepted: status(page, 'accepted'), hide: hide(page), new: page.getByRole('button', { name: 'New ticket', exact: true }) },
    scrollAreas: { page: page.locator('.project-page') }, interactions: [{ name: 'confirmed QA write refreshes 9 to 10', run: async () => {
      await row(page, 'PHAROS-14').locator('.status-btn').click()
      await page.getByRole('menuitemradio', { name: 'QA', exact: true }).click()
      await expect(group(page, 'in_progress').locator('b')).toHaveText('10')
      await expect(status(page, 'qa').locator('b')).toHaveText('8')
    } }] })
})
