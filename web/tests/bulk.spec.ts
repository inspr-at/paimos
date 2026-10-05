// SPDX-License-Identifier: AGPL-3.0-only
// U22 (AEON-128): select many tickets with the mouse (checkbox, Shift-click) or the
// keyboard (x, Shift J/K, Cmd/Ctrl A), change status, assignee, priority, labels,
// epic or archive them in one request, and undo the whole change at once.
import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { fixtures, me, mockWork, watchErrors, type Call } from './work-fixtures'

test.beforeEach(async ({ page }) => { await page.clock.setSystemTime(new Date('2026-09-23T12:00:00Z')) })

const grid = (page: Page) => page.getByRole('grid', { name: 'Tickets' })
const rows = (page: Page) => grid(page).locator('tr.ticket-row:not(.ghost)')
const row = (page: Page, key: string) => grid(page).locator('tr.ticket-row').filter({ has: page.locator('.key', { hasText: new RegExp(`^${key}$`) }) })
const bulkBar = (page: Page) => page.getByRole('toolbar', { name: /selected ticket/ })
const bulkCalls = (calls: Call[]) => calls.filter(call => call.path === '/api/nodes/bulk')
// Every row sends the version the list shows (AEON-326): a newer one is a conflict, not an overwrite.
const revisions = (...ids: string[]) => Object.fromEntries(ids.map(id => [id, expect.any(String)]))

test('checkbox and Shift-click select a range; one status change for all of them, undone at once', async ({ page }) => {
  const errors = watchErrors(page)
  const data = fixtures()
  const calls = await mockWork(page, data)
  await page.goto('/p/PHAROS')
  await expect(rows(page)).toHaveCount(5)
  await row(page, 'PHAROS-11').hover()
  await row(page, 'PHAROS-11').getByRole('checkbox', { name: 'Select PHAROS-11' }).check()
  await row(page, 'PHAROS-13').click({ modifiers: ['Shift'] })
  await expect(bulkBar(page)).toContainText('3selected')
  await expect(grid(page).locator('tr.ticket-row.selected .key')).toHaveText(['PHAROS-11', 'PHAROS-12', 'PHAROS-13'])
  // Shift-click selects; it never opens the ticket.
  await expect(page).toHaveURL(/\/p\/PHAROS\/tickets$/)
  await bulkBar(page).getByRole('button', { name: 'Status' }).click()
  await page.getByRole('menu', { name: 'Status of 3 tickets' }).getByRole('menuitemradio', { name: 'Cancelled' }).click()
  await expect(page.getByText('3 tickets are now Cancelled')).toBeVisible()
  expect(bulkCalls(calls)[0].body).toEqual({
    ids: ['n-1', 'n-2', 'n-3'], state: 'cancelled',
    if_unmodified_since: { 'n-1': '2026-09-23T11:00:00.000Z', 'n-2': '2026-09-23T09:00:00.000Z', 'n-3': '2026-09-23T06:00:00.000Z' },
  })
  // Cancelled work leaves the list (closed tickets are hidden), and the selection with it.
  await expect(rows(page)).toHaveCount(2)
  await expect(bulkBar(page)).toHaveCount(0)
  await page.getByRole('button', { name: 'Undo' }).click()
  await expect(page.getByText('Undone: the tickets are as they were')).toBeVisible()
  await expect(rows(page)).toHaveCount(5)
  expect(calls.some(call => call.method === 'POST' && call.path === '/api/events/5000/undo')).toBe(true)
  expect(data.nodes.find(n => n.id === 'n-2')!.state).toBe('backlog')
  expect(errors).toEqual([])
})

test('keyboard: x toggles, Shift J grows, Cmd or Ctrl A selects all, s opens the bulk status menu, Esc clears', async ({ page }) => {
  const calls = await mockWork(page, fixtures())
  await page.goto('/p/PHAROS')
  await expect(rows(page)).toHaveCount(5)
  // Focusing the list puts the cursor on its first row.
  await grid(page).focus()
  await page.keyboard.press('x')
  await expect(bulkBar(page)).toContainText('1selected')
  await page.keyboard.press('Shift+J')
  await page.keyboard.press('Shift+J')
  await expect(grid(page).locator('tr.ticket-row.selected .key')).toHaveText(['PHAROS-11', 'PHAROS-12', 'PHAROS-13'])
  await page.keyboard.press('x')
  await expect(bulkBar(page)).toContainText('2selected')
  await page.keyboard.press('ControlOrMeta+a')
  await expect(bulkBar(page)).toContainText('5selected')
  await expect(grid(page).getByRole('checkbox', { name: 'Clear the selection' })).toBeChecked()
  await page.keyboard.press('Escape')
  await expect(bulkBar(page)).toHaveCount(0)
  await page.keyboard.press('x')
  await page.keyboard.press('s')
  const menu = page.getByRole('menu', { name: 'Status of 1 ticket' })
  await expect(menu).toBeVisible()
  await page.keyboard.press('5')
  await expect.poll(() => bulkCalls(calls).length).toBe(1)
  expect(bulkCalls(calls)[0].body).toMatchObject({ state: 'qa' })
})

test('assignee, priority, labels and parent change in one request each; stale tickets say why', async ({ page }) => {
  const data = fixtures()
  data.nodes.find(n => n.id === 'n-4')!.fields.tags = [{ name: 'BUG', color: 'red' }]
  const calls = await mockWork(page, data)
  await page.goto('/p/PHAROS')
  await expect(rows(page)).toHaveCount(5)
  const pick = async (...keys: string[]) => {
    for (const key of keys) { await row(page, key).hover(); await row(page, key).getByRole('checkbox').check() }
  }
  await pick('PHAROS-12', 'PHAROS-14')
  // Assignee: Unassigned is a choice, sent as null.
  await bulkBar(page).getByRole('button', { name: 'Assignee' }).click()
  await page.getByRole('menu', { name: 'Assignee of 2 tickets' }).getByRole('menuitemradio', { name: /Markus Barta/ }).click()
  await expect(page.getByText('Assigned 2 tickets to Markus Barta')).toBeVisible()
  expect(bulkCalls(calls).at(-1)!.body).toEqual({ ids: ['n-2', 'n-4'], assignee: me.id, if_unmodified_since: revisions('n-2', 'n-4') })
  await expect(row(page, 'PHAROS-14').locator('.c-assignee')).toContainText('Markus Barta')
  await bulkBar(page).getByRole('button', { name: 'Priority' }).click()
  await page.getByRole('menu', { name: 'Priority of 2 tickets' }).getByRole('menuitemradio', { name: 'No priority' }).click()
  expect(bulkCalls(calls).at(-1)!.body).toMatchObject({ priority: null })
  // Labels: one carries BUG, so it shows as mixed; Apply sends adds and removes together.
  await bulkBar(page).getByRole('button', { name: 'Labels' }).click()
  const labels = page.getByRole('dialog', { name: 'Labels of 2 tickets' })
  await expect(labels.getByRole('checkbox', { name: /BUG/ })).toHaveAttribute('aria-checked', 'mixed')
  await labels.getByRole('checkbox', { name: /BUG/ }).click()
  await expect(labels.getByRole('checkbox', { name: /BUG/ })).toHaveAttribute('aria-checked', 'true')
  await labels.getByRole('checkbox', { name: /BUG/ }).click()
  await expect(labels.getByRole('checkbox', { name: /BUG/ })).toHaveAttribute('aria-checked', 'false')
  await labels.getByLabel('Find or add a label').fill('release-blocker')
  await page.keyboard.press('Enter')
  await labels.getByRole('button', { name: 'Apply' }).click()
  await expect(page.getByText('Changed the labels of 2 tickets')).toBeVisible()
  expect(bulkCalls(calls).at(-1)!.body).toEqual({ ids: ['n-2', 'n-4'], tags_add: [{ name: 'release-blocker' }], tags_remove: ['BUG'], if_unmodified_since: revisions('n-2', 'n-4') })
  // Parent: nesting is unrestricted by the retired kinds. A newer revision
  // refuses only the stale item and reports why without overwriting it.
  await pick('PHAROS-13')
  const stale = data.nodes.find(n => n.id === 'n-3')!
  stale.updated_at = '2026-09-23T13:00:00.000Z'
  await bulkBar(page).getByRole('button', { name: 'Move' }).click()
  const picker = page.getByRole('dialog', { name: 'Parent for 3 tickets' })
  await picker.getByRole('option', { name: /Guarded multi-cloud provisioning/ }).click()
  await expect(page.getByText('Moved 1 ticket to Guarded multi-cloud provisioning', { exact: true })).toBeVisible()
  await expect(page.getByText('PHAROS-13 was changed elsewhere meanwhile and kept its newer version.')).toBeVisible()
  expect(stale.parent_id).toBe('n-2')
  expect(stale.updated_at).toBe('2026-09-23T13:00:00.000Z')
  expect(bulkCalls(calls).at(-1)!.body).toMatchObject({ parent_id: 'n-epic' })
})

test('archive hides the tickets; a change made elsewhere since makes Undo refuse and change nothing', async ({ page }) => {
  const data = fixtures()
  await mockWork(page, data)
  await page.goto('/p/PHAROS')
  await expect(rows(page)).toHaveCount(5)
  await row(page, 'PHAROS-14').hover()
  await row(page, 'PHAROS-14').getByRole('checkbox').check()
  await bulkBar(page).getByRole('button', { name: 'Archive' }).click()
  await expect(page.getByText('Archived 1 ticket')).toBeVisible()
  await expect(rows(page)).toHaveCount(4)
  data.nodes.find(n => n.id === 'n-4')!.updated_at = '2026-09-23T12:30:00.000Z'
  await page.getByRole('button', { name: 'Undo' }).click()
  await expect(page.getByText('Some of them changed since, so nothing was undone.')).toBeVisible()
  expect(data.nodes.find(n => n.id === 'n-4')!.state).toBe('archived')
})

test('read-only people see no selection', async ({ page }) => {
  await mockWork(page, fixtures(), { readOnly: true })
  await page.goto('/p/PHAROS')
  await expect(rows(page)).toHaveCount(5)
  await expect(grid(page).getByRole('checkbox')).toHaveCount(0)
  await grid(page).focus()
  await page.keyboard.press('j')
  await page.keyboard.press('x')
  await expect(bulkBar(page)).toHaveCount(0)
})

test('on a phone a long press starts a selection, taps add to it, and the bar fits', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await mockWork(page, fixtures())
  await page.goto('/p/PHAROS')
  await expect(rows(page)).toHaveCount(5)
  await expect(grid(page).getByRole('checkbox').first()).toBeHidden()
  await row(page, 'PHAROS-12').dispatchEvent('contextmenu')
  await expect(bulkBar(page)).toBeVisible()
  await row(page, 'PHAROS-14').click()
  await expect(page).toHaveURL(/\/p\/PHAROS\/tickets$/)
  await expect(bulkBar(page)).toContainText('2')
  const box = (await bulkBar(page).boundingBox())!
  expect(box.x).toBeGreaterThanOrEqual(0)
  expect(box.x + box.width).toBeLessThanOrEqual(390)
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  await bulkBar(page).getByRole('button', { name: 'Clear the selection' }).click()
  await expect(bulkBar(page)).toHaveCount(0)
})

test('the bulk bar and its menus have no axe violations in light and dark', async ({ page }) => {
  await mockWork(page, fixtures())
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme, reducedMotion: 'reduce' })
    await page.goto('/p/PHAROS')
    await expect(rows(page)).toHaveCount(5)
    await row(page, 'PHAROS-11').hover()
    await row(page, 'PHAROS-11').getByRole('checkbox').check()
    await row(page, 'PHAROS-13').click({ modifiers: ['Shift'] })
    for (const open of [null, 'Labels', 'Assignee'] as const) {
      if (open) await bulkBar(page).getByRole('button', { name: open }).click()
      await page.waitForTimeout(150)
      const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).exclude('.version-coordinate').exclude('.calendar-version').analyze()
      const summary = results.violations.map(v => `${v.id}: ${v.help} ${v.nodes.slice(0, 3).map(n => n.target.join(' ')).join(' | ')}`)
      expect(summary, summary.join('\n')).toEqual([])
      if (open) await page.keyboard.press('Escape')
    }
  }
})
