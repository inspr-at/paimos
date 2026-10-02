// SPDX-License-Identifier: AGPL-3.0-only
// Hours: the week grid, fast entry, people and agents, period review and approval.
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { addHistoricalPeriod, businessData, mira, mockBusiness, nova, WEEK39, type BusinessMockOptions } from './business-fixtures'

test.use({ timezoneId: 'Europe/Vienna' })

async function setup(page: Page, options: BusinessMockOptions = {}) {
  await mockWork(page, fixtures())
  const data = businessData(options)
  const calls = await mockBusiness(page, data, options)
  return { data, calls }
}
async function openWeek(page: Page, path = '/business/hours') {
  await page.goto(path)
  await expect(page.getByRole('heading', { name: 'Hours', level: 1 })).toBeVisible()
  await expect(page.getByRole('table', { name: 'Hours per ticket and day' })).toBeVisible()
}
const grid = (page: Page) => page.getByRole('table', { name: 'Hours per ticket and day' })
const form = (page: Page) => page.getByRole('form', { name: 'Log time' })

test('an older week shows existing hours and logs into its existing period', async ({ page }) => {
  const { data, calls } = await setup(page)
  const period = addHistoricalPeriod(data)
  await openWeek(page, '/business/hours?week=2024-09-16')
  await expect(grid(page).locator('tfoot td').last()).toHaveText('6:15')
  await expect(page.getByRole('region', { name: 'Entries' }).getByText('Provisioning spike')).toBeVisible()
  const read = calls.find(c => c.path === '/api/time-periods' && c.query.get('principal_id') === me.id)!
  expect(read.query.get('since')).toBe(period.starts_at)
  expect(read.query.get('until')).toBe(period.ends_at)
  await form(page).getByRole('combobox', { name: 'Cost unit' }).selectOption({ label: 'Development' })
  await form(page).getByRole('button', { name: /^Ticket:/ }).click()
  await page.getByRole('combobox', { name: 'Ticket' }).fill('PHAROS-11')
  await expect(page.getByRole('option', { name: /PHAROS-11/ })).toBeVisible()
  await page.keyboard.press('Enter')
  await form(page).getByRole('radio', { name: 'Tue 17' }).click()
  await form(page).getByRole('textbox', { name: 'Duration' }).fill('15m')
  await form(page).getByRole('textbox', { name: 'Duration' }).press('Enter')
  await expect(page.getByText('Logged 15m on PHAROS-11.')).toBeVisible()
  await expect(grid(page).locator('tfoot td').last()).toHaveText('6:30')
  expect(calls.filter(c => c.path === '/api/time-periods' && c.method === 'POST')).toEqual([])
  expect(calls.find(c => c.path === '/api/time-entries' && c.method === 'POST')?.body).toMatchObject({ period_id: period.id })
  expect(data.entries.filter(e => e.period_id === period.id)).toHaveLength(3)
})

test('open periods older than a year appear on a direct approvals load', async ({ page }) => {
  const { data } = await setup(page)
  addHistoricalPeriod(data)
  await page.goto('/business/hours?view=approvals')
  const waiting = page.getByRole('region', { name: 'Waiting for approval' })
  await expect(waiting.getByRole('button')).toHaveCount(2)
  await expect(waiting.getByRole('button').first()).toContainText('16–22 Sep 2024')
  await expect(waiting.getByRole('button').first()).toContainText('6h 15m')
})

test('open periods older than a year appear in the home waiting count and week badge', async ({ page }) => {
  const { data } = await setup(page)
  addHistoricalPeriod(data)
  await page.goto('/business')
  await expect(page.getByText('8h 30m logged this week · 2 periods to approve · 3 rates in force')).toBeVisible()
  const queue = page.getByRole('list', { name: 'Periods waiting for approval' })
  await expect(queue.getByRole('link')).toHaveCount(2)
  await expect(queue.getByRole('link').first()).toContainText('16–22 Sep 2024')
  await expect(queue.getByRole('link').first()).toContainText('6h 15m')
  await openWeek(page)
  await expect(page.getByRole('radio', { name: /Approvals/ })).toContainText('2')
})

test('the week grid sums per ticket and day with exact amounts', async ({ page }) => {
  const errors = watchErrors(page)
  await setup(page)
  await openWeek(page)
  await expect(page.getByText('Week 39 · 21–27 Sep 2026 · 8h 30m logged')).toBeVisible()
  const rows = grid(page).locator('tbody tr')
  await expect(rows).toHaveCount(3)
  await expect(rows.first()).toContainText('PHAROS-11')
  await expect(rows.first().locator('td')).toHaveText(['2:00', '3:30', '·', '·', '·', '·', '·', '5:30'])
  await expect(grid(page).locator('tfoot td')).toHaveText(['2:00', '3:30', '1:30', '1:30', '·', '·', '·', '8:30'])
  await expect(grid(page).locator('thead th.today')).toContainText('Thu')
  // 5.5h × 95 + 1.5h × 95 + 1.5h × 85, exactly.
  await expect(page.locator('.week-foot')).toContainText('792.50 EUR')
  await expect(page.locator('.period-chip')).toHaveText('21–27 Sep 2026')
  const entries = page.getByRole('region', { name: 'Entries' })
  await expect(entries.getByText('Thu 24')).toBeVisible()
  await expect(entries.locator('.entry').first()).toContainText('08:00–09:30')
  await expect(entries.locator('.entry').first()).toContainText('127.50')
  expect(errors).toEqual([])
})

test('logging time: ticket, duration like 1h30, Enter', async ({ page }) => {
  const { data, calls } = await setup(page)
  await openWeek(page)
  await form(page).getByRole('combobox', { name: 'Cost unit' }).selectOption({ label: 'Development' })
  await form(page).getByRole('button', { name: /^Ticket:/ }).click()
  await page.getByRole('combobox', { name: 'Ticket' }).fill('oracle')
  await expect(page.getByRole('option', { name: /PHAROS-12/ })).toBeVisible()
  await page.keyboard.press('Enter')
  await expect(form(page).getByRole('textbox', { name: 'Duration' })).toBeFocused()
  await page.keyboard.type('1h30')
  await expect(form(page)).toContainText('1h 30m · 142.50 EUR')
  await page.keyboard.press('Enter')
  await expect(page.getByText('Logged 1h 30m on PHAROS-12.')).toBeVisible()
  const post = calls.find(c => c.path === '/api/time-entries' && c.method === 'POST')!
  // Thursday, after the day's last entry (08:00–09:30), in the open week period.
  expect(post.body).toEqual({
    period_id: 'p-me-39', cost_unit_node_id: 'cu-dev', currency: 'EUR', principal_id: me.id, node_id: 'n-2',
    started_at: '2026-09-24T07:30:00.000Z', ended_at: '2026-09-24T09:00:00.000Z', note: '', source: 'manual',
  })
  await expect(grid(page).locator('tfoot td').last()).toHaveText('10:00')
  // The ticket, cost unit and day stay; the duration is cleared for the next entry.
  await expect(form(page).getByRole('textbox', { name: 'Duration' })).toHaveValue('')
  await expect(form(page).getByRole('button', { name: /^Ticket: PHAROS-12/ })).toBeVisible()
  expect(data.entries.length).toBe(8)
})

test('a new week opens its period with the first entry; bad input never posts', async ({ page }) => {
  const { calls } = await setup(page)
  await openWeek(page, '/business/hours?week=2026-09-28')
  await expect(page.getByText('No period for this week yet. The first entry opens one, Monday to Sunday.')).toBeVisible()
  await form(page).getByRole('button', { name: /^Ticket:/ }).click()
  await page.getByRole('combobox', { name: 'Ticket' }).fill('PHAROS-11')
  await expect(page.getByRole('option', { name: /PHAROS-11/ })).toBeVisible()
  await page.keyboard.press('Enter')
  await page.keyboard.type('45')
  await page.keyboard.press('Enter')
  await expect(form(page)).toContainText('Choose a cost unit.')
  await form(page).getByRole('combobox', { name: 'Cost unit' }).selectOption({ label: 'Design' })
  await form(page).getByRole('textbox', { name: 'Duration' }).fill('45')
  await form(page).getByRole('textbox', { name: 'Duration' }).press('Enter')
  await expect(form(page)).toContainText('Use a duration like 1h30, 90m or 1.5')
  expect(calls.some(c => c.method === 'POST')).toBe(false)
  await form(page).getByRole('radio', { name: 'Mon 28' }).click()
  await form(page).getByRole('textbox', { name: 'Start time (optional)' }).fill('14:00')
  await form(page).getByRole('textbox', { name: 'Duration' }).fill('45m')
  await form(page).getByRole('textbox', { name: 'Duration' }).press('Enter')
  await expect(page.getByText('Logged 45m on PHAROS-11.')).toBeVisible()
  const period = calls.find(c => c.path === '/api/time-periods' && c.method === 'POST')!
  expect(period.body).toEqual({ principal_id: me.id, starts_at: '2026-09-27T22:00:00.000Z', ends_at: '2026-10-04T22:00:00.000Z' })
  const entry = calls.find(c => c.path === '/api/time-entries' && c.method === 'POST')!
  expect(entry.body).toMatchObject({ started_at: '2026-09-28T12:00:00.000Z', ended_at: '2026-09-28T12:45:00.000Z', cost_unit_node_id: 'cu-design' })
})

test('weeks move with the arrow keys; l focuses the entry line', async ({ page }) => {
  await setup(page)
  await openWeek(page)
  await page.keyboard.press('ArrowLeft')
  await expect(page).toHaveURL(/week=2026-09-14/)
  await expect(page.getByText('Week 38 · 14–20 Sep 2026 · 6h 15m logged')).toBeVisible()
  await page.keyboard.press('t')
  await expect(page).not.toHaveURL(/week=/)
  await expect(page.getByText('Week 39 · 21–27 Sep 2026 · 8h 30m logged')).toBeVisible()
  await page.keyboard.press('l')
  await expect(form(page).getByRole('button', { name: /^Ticket:/ })).toBeFocused()
})

test('admins switch to a colleague or an agent; agents have no manual entry', async ({ page }) => {
  await setup(page)
  await openWeek(page)
  await page.getByRole('button', { name: 'Hours of You. Choose a person or agent' }).click()
  const picker = page.getByRole('combobox', { name: 'Hours of' })
  await expect(page.getByRole('listbox', { name: 'Hours of' }).getByRole('option')).toHaveText([/You/, /Mira Holm/, /Nova/])
  await picker.fill('mira')
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(new RegExp(`person=${mira.id}`))
  await expect(page.getByText(/Approved by Markus Barta\. Closed for new entries/)).toHaveCount(0)
  await page.goto(`/business/hours?person=${mira.id}&week=2026-09-14`)
  await expect(page.getByText('Approved by Markus Barta. Closed for new entries and corrections.')).toBeVisible()
  await expect(form(page)).toHaveCount(0)
  await page.goto(`/business/hours?person=${nova.id}`)
  await expect(page.getByText('An agent’s time comes from its finished runs, one entry per run.')).toBeVisible()
  await expect(form(page)).toHaveCount(0)
})

test('members see only their own week, without approvals', async ({ page }) => {
  await setup(page, { role: 'member' })
  await openWeek(page, `/business/hours?person=${mira.id}`)
  await expect(page.getByRole('radio', { name: /Approvals/ })).toHaveCount(0)
  await expect(page.getByRole('button', { name: /^Hours of/ })).toHaveCount(0)
  await expect(grid(page).locator('tbody tr')).toHaveCount(3)
})

test('approving a period sends the reviewed revision and digest', async ({ page }) => {
  const errors = watchErrors(page)
  const { data, calls } = await setup(page)
  await page.goto('/business/hours?view=approvals')
  const waiting = page.getByRole('region', { name: 'Waiting for approval' })
  await expect(waiting.getByRole('button')).toHaveCount(1)
  await expect(waiting.getByRole('button').first()).toContainText('6h 15m')
  await expect(page.getByRole('radio', { name: /Approvals/ })).toContainText('1')
  await waiting.getByRole('button').first().click()
  const panel = page.getByRole('complementary', { name: 'Period review' })
  await expect(panel.getByRole('heading', { name: 'Markus Barta' })).toBeVisible()
  await expect(panel.getByText('14–20 Sep 2026')).toBeVisible()
  await expect(panel.locator('.metric').first()).toContainText('6h 15m')
  await expect(panel.locator('.metric').last()).toContainText('593.75 EUR')
  await panel.getByRole('button', { name: 'Approve period' }).click()
  const dialog = page.getByRole('dialog', { name: /Approve Markus Barta’s hours for 14–20 Sep 2026\?/ })
  await expect(dialog).toContainText('2 entries, 6h 15m')
  await dialog.getByRole('button', { name: 'Approve period' }).click()
  await expect(page.getByText('Approved Markus Barta’s hours for 14–20 Sep 2026.')).toBeVisible()
  const approve = calls.find(c => c.path.endsWith('/approve'))!
  expect(approve.body).toEqual({ expected_revision: 3, expected_entries_sha256: '03'.repeat(32) })
  expect(data.periods.find(p => p.id === 'p-me-38')?.state).toBe('approved')
  await expect(panel.getByText('Approved', { exact: true })).toBeVisible()
  await expect(panel.getByRole('button', { name: 'Approve period' })).toHaveCount(0)
  await page.keyboard.press('Escape')
  await expect(panel).toHaveCount(0)
  expect(errors).toEqual([])
})

test('a changed period refuses the approval and says so', async ({ page }) => {
  await setup(page, { approveConflict: true })
  await page.goto('/business/hours?view=approvals&period=p-me-38')
  const panel = page.getByRole('complementary', { name: 'Period review' })
  await panel.getByRole('button', { name: 'Approve period' }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Approve period' }).click()
  await expect(page.getByText('Entries changed since you opened this period. The latest entries are shown; review them again.')).toBeVisible()
  await expect(panel.getByRole('button', { name: 'Approve period' })).toBeVisible()
})

test('a running period can be reviewed from the week strip', async ({ page }) => {
  await setup(page)
  await openWeek(page)
  await page.getByRole('button', { name: 'Review' }).click()
  const panel = page.getByRole('complementary', { name: 'Period review' })
  await expect(panel.getByText('Running', { exact: true })).toBeVisible()
  await expect(panel.getByText('Still running: approving closes it now.')).toBeVisible()
  expect(WEEK39.starts_at).toBe('2026-09-20T22:00:00.000Z')
})
