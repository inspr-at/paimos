// SPDX-License-Identifier: AGPL-3.0-only
// Hours corrections: an entry is edited in place with the same line as a new one,
// deleted with a short server-side undo, refused with the newer entry on a
// conflict, and locked with its reason once its period is approved.
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { businessData, mira, mockBusiness, nova, REVISION, WEEK39, type BusinessMockOptions } from './business-fixtures'

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
  await expect(page.getByRole('region', { name: 'Entries' })).toBeVisible()
}
const entries = (page: Page) => page.getByRole('region', { name: 'Entries' })
const row = (page: Page, id: string) => entries(page).locator(`[data-entry="${id}"]`)
const editor = (page: Page) => page.getByRole('form', { name: 'Edit entry' })
const grid = (page: Page) => page.getByRole('table', { name: 'Hours per ticket and day' })

test('an entry is corrected in place; only what changed is sent, against its revision', async ({ page }) => {
  const errors = watchErrors(page)
  const { data, calls } = await setup(page)
  await openWeek(page)
  await expect(entries(page).getByText('Entries are final once logged')).toHaveCount(0)
  await expect(entries(page)).toContainText('Click an entry to correct it while its period is open.')
  await row(page, 'e-4').getByText('Connector sketch').click()
  const form = editor(page)
  await expect(form.getByRole('button', { name: /^Ticket: PHAROS-12/ })).toBeVisible()
  await expect(form.getByRole('combobox', { name: 'Cost unit' })).toHaveValue('cu-design')
  await expect(form.getByRole('radio', { name: 'Thu 24' })).toHaveAttribute('aria-checked', 'true')
  await expect(form.getByRole('textbox', { name: 'Start time' })).toHaveValue('08:00')
  await expect(form.getByRole('textbox', { name: 'Note (optional)' })).toHaveValue('Connector sketch')
  // The duration is focused and selected, so typing replaces it.
  const duration = form.getByRole('textbox', { name: 'Duration' })
  await expect(duration).toBeFocused()
  await expect(duration).toHaveValue('1:30')
  await page.keyboard.type('2h')
  await expect(form).toContainText('2h · 170.00 EUR')
  // The page keeps one primary action: the new-entry line steps back while editing.
  await expect(page.getByRole('form', { name: 'Log time' }).getByRole('button', { name: 'Log' })).not.toHaveClass(/primary/)
  await page.keyboard.press('Enter')
  await expect(page.getByText('Saved 2h on PHAROS-12.')).toBeVisible()
  const patch = calls.find(c => c.method === 'PATCH')!
  expect(patch.path).toBe('/api/time-entries/e-4')
  expect(patch.body).toEqual({ duration_seconds: 7200 })
  await expect(editor(page)).toHaveCount(0)
  await expect(row(page, 'e-4')).toContainText('08:00–10:00')
  await expect(row(page, 'e-4')).toContainText('170.00')
  await expect(row(page, 'e-4')).toBeFocused()
  await expect(grid(page).locator('tfoot td').last()).toHaveText('9:00')
  expect(data.entries.find(e => e.id === 'e-4')?.amount).toBe('170.0000')
  expect(errors).toEqual([])
})

test('the precondition is the revision exactly as read', async ({ page }) => {
  await setup(page)
  await openWeek(page)
  const request = page.waitForRequest(r => r.method() === 'PATCH')
  await row(page, 'e-4').getByText('Connector sketch').click()
  await editor(page).getByRole('textbox', { name: 'Note (optional)' }).fill('Connector sketch, reviewed')
  await editor(page).getByRole('textbox', { name: 'Note (optional)' }).press('Enter')
  const sent = await request
  expect(sent.headers()['if-unmodified-since']).toBe(REVISION)
  expect(sent.postDataJSON()).toEqual({ note: 'Connector sketch, reviewed' })
  await expect(row(page, 'e-4')).toContainText('Connector sketch, reviewed')
})

test('keyboard: arrows move between entries, e edits, Escape cancels, Enter saves a moved start', async ({ page }) => {
  const { calls } = await setup(page)
  await openWeek(page)
  await row(page, 'e-4').focus()
  await page.keyboard.press('ArrowDown')
  await expect(row(page, 'e-3')).toBeFocused()
  await page.keyboard.press('ArrowUp')
  await expect(row(page, 'e-4')).toBeFocused()
  await page.keyboard.press('e')
  await expect(editor(page)).toBeVisible()
  await page.keyboard.type('3h')
  await page.keyboard.press('Escape')
  await expect(editor(page)).toHaveCount(0)
  await expect(row(page, 'e-4')).toBeFocused()
  await expect(row(page, 'e-4')).toContainText('08:00–09:30')
  expect(calls.some(c => c.method === 'PATCH')).toBe(false)
  // Wednesday at 10:00, same length.
  await page.keyboard.press('e')
  await editor(page).getByRole('radio', { name: 'Wed 23' }).click()
  await editor(page).getByRole('textbox', { name: 'Start time' }).fill('10:00')
  await editor(page).getByRole('textbox', { name: 'Start time' }).press('Enter')
  await expect(page.getByText('Saved 1h 30m on PHAROS-12.')).toBeVisible()
  expect(calls.find(c => c.method === 'PATCH')!.body).toEqual({ started_at: '2026-09-23T08:00:00.000Z' })
  await expect(row(page, 'e-4')).toBeFocused()
  await expect(row(page, 'e-4')).toContainText('10:00–11:30')
  await expect(entries(page).locator('.day-group').filter({ hasText: 'Wed 23' }).locator('.entry')).toHaveCount(2)
})

test('a correction has to fit: past midnight or outside the period never sends', async ({ page }) => {
  const { calls } = await setup(page)
  await openWeek(page)
  await row(page, 'e-4').focus()
  await page.keyboard.press('e')
  await editor(page).getByRole('textbox', { name: 'Start time' }).fill('23:00')
  await editor(page).getByRole('textbox', { name: 'Start time' }).press('Enter')
  await expect(editor(page)).toContainText('That would run past midnight. Split it across two days.')
  await editor(page).getByRole('textbox', { name: 'Start time' }).fill('')
  await editor(page).getByRole('textbox', { name: 'Start time' }).press('Enter')
  await expect(editor(page)).toContainText('Enter a start time, like 9:30.')
  expect(calls.some(c => c.method === 'PATCH')).toBe(false)
})

test('Delete removes an entry with a six-second undo that reverses it on the server', async ({ page }) => {
  const errors = watchErrors(page)
  const { data, calls } = await setup(page)
  await openWeek(page)
  await row(page, 'e-4').focus()
  await page.keyboard.press('Delete')
  await expect(row(page, 'e-4')).toHaveCount(0)
  await expect(page.getByText('Deleted 1h 30m on PHAROS-12.')).toBeVisible()
  // Focus stays in the list, on the next entry.
  await expect(row(page, 'e-3')).toBeFocused()
  await expect(grid(page).locator('tfoot td').last()).toHaveText('7:00')
  await expect.poll(() => data.entries.some(e => e.id === 'e-4')).toBe(false)
  const del = calls.find(c => c.method === 'DELETE')!
  expect(del.path).toBe('/api/time-entries/e-4')
  await page.getByRole('button', { name: 'Undo' }).click()
  await expect(row(page, 'e-4')).toBeVisible()
  await expect(page.getByText('Restored 1h 30m on PHAROS-12.')).toBeVisible()
  expect(calls.filter(c => c.path === '/api/events')).toEqual([])
  expect(calls.some(c => c.path === '/api/events/500/undo' && c.method === 'POST')).toBe(true)
  await expect.poll(() => data.entries.some(e => e.id === 'e-4')).toBe(true)
  await expect(row(page, 'e-4')).toBeFocused()
  await expect(grid(page).locator('tfoot td').last()).toHaveText('8:30')
  // Backspace deletes too; the restored entry carries its new revision.
  await page.keyboard.press('Backspace')
  await expect(row(page, 'e-4')).toHaveCount(0)
  expect(calls.filter(c => c.method === 'DELETE')).toHaveLength(2)
  expect(errors).toEqual([])
})

test('AEON-1416: the first Hours Undo cannot reverse a later deletion cycle', async ({ page }) => {
  const { data, calls } = await setup(page)
  await openWeek(page)
  await row(page, 'e-4').focus()
  await page.keyboard.press('Delete')
  await expect.poll(() => data.events.length).toBe(1)
  const first = data.events[0]
  const toast = page.locator('.toast').filter({ hasText: 'Deleted 1h 30m on PHAROS-12.' })
  await expect(toast).toBeVisible()
  // Another actor restored and deleted the same entry while this toast stayed
  // open. Its old event is already undone; only the later deletion is live.
  data.events.push({ ...first, id: 501, before: null, after: first.before, undo_of: first.id })
  data.events.push({ ...first, id: 502 })
  await toast.getByRole('button', { name: 'Undo' }).click()
  await expect(page.getByText('The entry could not be restored: its period was approved or it changed meanwhile.')).toBeVisible()
  await expect(row(page, 'e-4')).toHaveCount(0)
  expect(data.entries.some(e => e.id === 'e-4')).toBe(false)
  expect(calls.filter(c => c.path === '/api/events')).toEqual([])
  expect(calls.filter(c => c.path.endsWith('/undo')).map(c => c.path)).toEqual(['/api/events/500/undo'])
})

test('a conflict shows the newer entry and keeps the draft', async ({ page }) => {
  const { data, calls } = await setup(page)
  await openWeek(page)
  data.meanwhile['e-4'] = { note: 'Sketch v2 by Mira' }
  await row(page, 'e-4').focus()
  await page.keyboard.press('e')
  await page.keyboard.type('2h')
  await page.keyboard.press('Enter')
  const alert = editor(page).locator('..').getByRole('alert')
  await expect(alert).toContainText('This entry changed while you were editing.')
  await expect(alert).toContainText('Sketch v2 by Mira')
  await expect(editor(page).getByRole('textbox', { name: 'Duration' })).toHaveValue('2h')
  // Saving again applies the draft's change on top of the newer entry.
  await editor(page).getByRole('button', { name: 'Save mine' }).click()
  await expect(page.getByText('Saved 2h on PHAROS-12.')).toBeVisible()
  const patches = calls.filter(c => c.method === 'PATCH')
  expect(patches).toHaveLength(2)
  expect(patches[1].body).toEqual({ duration_seconds: 7200 })
  await expect(row(page, 'e-4')).toContainText('Sketch v2 by Mira')
  await expect(row(page, 'e-4')).toContainText('08:00–10:00')
})

test('after a conflict, the newer entry can be taken instead', async ({ page }) => {
  const { data } = await setup(page)
  await openWeek(page)
  data.meanwhile['e-4'] = { note: 'Sketch v2 by Mira' }
  await row(page, 'e-4').getByText('Connector sketch').click()
  await page.keyboard.type('2h')
  await page.keyboard.press('Enter')
  await page.getByRole('button', { name: 'Use the newer entry' }).click()
  await expect(editor(page)).toHaveCount(0)
  await expect(row(page, 'e-4')).toContainText('Sketch v2 by Mira')
  await expect(row(page, 'e-4')).toContainText('08:00–09:30')
  await expect(row(page, 'e-4')).toBeFocused()
})

test('an approved period is locked, with the reason, and nothing can change it', async ({ page }) => {
  const { calls } = await setup(page)
  await openWeek(page, `/business/hours?person=${mira.id}&week=2026-09-14`)
  await expect(page.getByText('Approved by Markus Barta. Closed for new entries and corrections.')).toBeVisible()
  await expect(entries(page).locator('.card-head')).toContainText('Locked: approved by Markus Barta, so these entries can’t change.')
  await expect(entries(page).getByRole('button', { name: /^Edit/ })).toHaveCount(0)
  await expect(entries(page).getByRole('button', { name: /^Delete/ })).toHaveCount(0)
  await row(page, 'e-7').focus()
  await page.keyboard.press('e')
  await expect(editor(page)).toHaveCount(0)
  await page.keyboard.press('Delete')
  await expect(row(page, 'e-7')).toBeVisible()
  await expect(page.locator('.toast').filter({ hasText: 'Locked: approved by Markus Barta.' }).first()).toBeVisible()
  expect(calls.some(c => c.method === 'PATCH' || c.method === 'DELETE')).toBe(false)
  await expect(page.locator('.hint')).not.toContainText('edit entry')
})

test('approval meanwhile: the correction is refused and the week shows it locked', async ({ page }) => {
  const { data } = await setup(page)
  await openWeek(page)
  await row(page, 'e-4').focus()
  await page.keyboard.press('e')
  data.periods.find(p => p.id === 'p-me-39')!.state = 'approved'
  await page.keyboard.type('2h')
  await page.keyboard.press('Enter')
  await expect(page.getByText('The period was approved meanwhile, so this entry is locked now.')).toBeVisible()
  await expect(editor(page)).toHaveCount(0)
  await expect(entries(page).locator('.card-head')).toContainText('Locked: approved, so these entries can’t change.')
})

test('an admin corrects an agent run’s cost unit and note; its ticket and time stay as measured', async ({ page }) => {
  const { data, calls } = await setup(page)
  data.periods.push({ id: 'p-nova-39', principal_id: nova.id, ...WEEK39, state: 'open', revision: 1, approval: null })
  data.entries.push({ ...data.entries.find(e => e.id === 'e-3')!, id: 'e-run', period_id: 'p-nova-39', principal_id: nova.id, source: 'agent_run', agent_run_id: 'run-1', note: 'Run 1' })
  await openWeek(page, `/business/hours?person=${nova.id}`)
  await row(page, 'e-run').focus()
  await page.keyboard.press('e')
  const form = editor(page)
  await expect(form.getByRole('button', { name: /^Ticket:/ })).toBeDisabled()
  await expect(form.getByRole('textbox', { name: 'Duration' })).toBeDisabled()
  await expect(form.getByRole('textbox', { name: 'Start time' })).toBeDisabled()
  await expect(form.getByRole('radio', { name: 'Mon 21' })).toBeDisabled()
  await expect(form).toContainText('The ticket and time come from the agent run.')
  await expect(form.getByRole('textbox', { name: 'Note (optional)' })).toBeFocused()
  await form.getByRole('combobox', { name: 'Cost unit' }).selectOption({ label: 'Design' })
  await expect(form).toContainText('1h 30m · 127.50 EUR')
  await form.getByRole('textbox', { name: 'Note (optional)' }).fill('Run 1, design review')
  await form.getByRole('textbox', { name: 'Note (optional)' }).press('Enter')
  await expect(page.getByText('Saved 1h 30m on AEON-1.')).toBeVisible()
  expect(calls.find(c => c.method === 'PATCH')!.body).toEqual({ cost_unit_node_id: 'cu-design', note: 'Run 1, design review' })
})

test.describe('phone', () => {
  test.use({ viewport: { width: 390, height: 844 } })
  test('a tap opens the editor; Delete entry sits inside it', async ({ page }) => {
    const errors = watchErrors(page)
    const { calls } = await setup(page)
    await openWeek(page)
    await row(page, 'e-2').getByText('Cleanup job').click()
    await expect(editor(page)).toBeVisible()
    await editor(page).getByRole('button', { name: 'Delete entry' }).click()
    await expect(row(page, 'e-2')).toHaveCount(0)
    await expect(page.getByText('Deleted 3h 30m on PHAROS-11.')).toBeVisible()
    expect(calls.filter(c => c.method === 'DELETE')).toHaveLength(1)
    const width = await page.evaluate(() => document.documentElement.scrollWidth)
    expect(width).toBeLessThanOrEqual(390)
    expect(errors).toEqual([])
  })
})
