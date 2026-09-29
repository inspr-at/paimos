// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'

function world(theme = 'light') {
  const data = fixtures()
  data.preferences.theme = { choice: theme }
  data.preferences['list:p-pharos'] = { visible: ['key', 'title', 'status', 'estimate', 'updated'] }
  const first = data.nodes.find(n => n.id === 'n-1')!
  Object.assign(first.fields, { estimate_hours: 2, estimate_source: 'agent', estimate_by: 'draft-worker', estimate_at: '2026-09-29T10:00:00Z', estimate_confirmed: false })
  first.estimate = { hours: 2, estimated_children: 0, open_children: 0, by: { id: 'draft-worker', name: 'Estimate worker' } }
  const second = data.nodes.find(n => n.id === 'n-2')!
  Object.assign(second.fields, { estimate_hours: .5, estimate_source: 'person', estimate_by: me.id, estimate_at: '2026-09-29T11:00:00Z', estimate_confirmed: true })
  second.estimate = { hours: .5, estimated_children: 0, open_children: 0, by: me }
  data.nodes.find(n => n.id === 'n-epic')!.estimate = { hours: 2.5, estimated_children: 2, open_children: 3 }
  return data
}
const row = (page: Page, key: string) => page.locator('tr.ticket-row:not(.ghost)').filter({ has: page.locator('.key', { hasText: new RegExp(`^${key}$`) }) })
const panel = (page: Page) => page.getByRole('complementary', { name: 'Ticket details' })
const fits = async (page: Page) => expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true)
async function shot(page: Page, name: string) {
  if (process.env.ESTIMATE_SHOTS) await page.screenshot({ path: `${process.env.ESTIMATE_SHOTS}/${name}.png`, fullPage: true })
}

for (const theme of ['light', 'dark']) for (const width of [1600, 390]) {
  test(`estimate display and edit at ${width} in ${theme}`, async ({ page }) => {
    const errors = watchErrors(page)
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    const data = world(theme)
    const calls = await mockWork(page, data)
    await page.goto('/p/PHAROS')
    await expect(row(page, 'PHAROS-11').locator('.title-text')).toBeVisible()
    if (width === 1600) {
      await expect(page.getByRole('columnheader', { name: 'Estimate' })).toBeVisible()
      const draft = row(page, 'PHAROS-11').locator('.c-estimate .mono')
      await expect(draft).toHaveText('~2h est.')
      await expect(draft).toHaveClass(/estimate-draft/)
      await expect(draft).toHaveAttribute('data-tip', /Agent estimate by Estimate worker, /)
      await expect(row(page, 'PHAROS-12').locator('.c-estimate .mono')).toHaveText('30m')
      await expect(row(page, 'PHAROS-12').locator('.c-estimate .mono')).not.toHaveClass(/estimate-draft/)
      await expect(row(page, 'PHAROS-10').locator('.c-estimate .mono')).toHaveAttribute('data-tip', '2 of 3 open children estimated · agent hours')
      await page.getByRole('columnheader', { name: 'Estimate' }).getByRole('button', { name: 'Estimate', exact: true }).click()
      await expect.poll(() => calls.some(c => c.method === 'GET' && c.query.get('sort')?.split(',').includes('estimate'))).toBe(true)
      await expect(page.locator('tr.ticket-row:not(.ghost) .key')).toHaveText(['PHAROS-12', 'PHAROS-11', 'PHAROS-10', 'PHAROS-13', 'PHAROS-14'])
    }
    if (width === 390) {
      const card = row(page, 'PHAROS-11')
      const value = card.locator('.c-estimate .mono')
      await expect(value).toBeVisible()
      await expect(value).toHaveText('~2h est.')
      await expect(card.locator('.c-updated')).toBeVisible()
      await expect(row(page, 'PHAROS-10').locator('.c-estimate .mono')).toHaveText('~2.5h')
      await expect(row(page, 'PHAROS-14').locator('.c-estimate')).toBeHidden()
      const titleBox = await card.locator('.title-text').boundingBox()
      const estimateBox = await value.boundingBox()
      expect(titleBox && estimateBox && titleBox.x + titleBox.width <= estimateBox.x + 1).toBeTruthy()
    }
    await fits(page); await shot(page, `list-${width}-${theme}`)
    await row(page, 'PHAROS-11').locator('.title-text').click()
    const control = panel(page).getByRole('button', { name: 'Estimate: ~2h, agent draft by Estimate worker. Edit estimate' })
    await expect(control).toHaveAccessibleName('Estimate: ~2h, agent draft by Estimate worker. Edit estimate')
    await expect(control).toHaveClass(/estimate-draft/)
    await shot(page, `panel-${width}-${theme}`)
    await control.click()
    const input = panel(page).getByRole('textbox', { name: 'Estimate in agent hours' })
    await expect(input).toBeFocused()
    await input.fill('201h'); await panel(page).getByRole('button', { name: 'Save', exact: true }).click()
    await expect(panel(page).getByRole('alert')).toContainText('up to 200 hours')
    expect(calls.filter(c => c.method === 'PATCH' && c.path === '/api/nodes/n-1')).toHaveLength(0)
    await input.fill('90m'); await expect(panel(page).getByRole('alert')).toHaveCount(0); await shot(page, `edit-${width}-${theme}`)
    await panel(page).getByRole('button', { name: 'Save', exact: true }).click()
    await expect(panel(page).getByRole('button', { name: 'Estimate: ~1.5h. Edit estimate' })).toBeVisible()
    const patch = calls.find(c => c.method === 'PATCH' && c.path === '/api/nodes/n-1')!
    expect(patch.headers['if-unmodified-since']).toBeTruthy()
    expect((patch.body as { fields: Record<string, unknown> }).fields).toMatchObject({ estimate_hours: 1.5, priority: 'high' })
    expect((patch.body as { fields: Record<string, unknown> }).fields).not.toHaveProperty('estimate_by')
    await fits(page); expect(errors).toEqual([])
  })
}

test('estimate edit preserves the draft after a revision conflict, supports cancel and clearing', async ({ page }) => {
  const data = world()
  const calls = await mockWork(page, data, { conflictOn: 'n-1' })
  await page.goto('/p/PHAROS/PHAROS-11')
  await panel(page).getByRole('button', { name: 'Estimate: ~2h, agent draft by Estimate worker. Edit estimate' }).click()
  const input = panel(page).getByRole('textbox', { name: 'Estimate in agent hours' })
  await input.fill('3h'); await panel(page).getByRole('button', { name: 'Save', exact: true }).click()
  await expect(input).toHaveValue('3h')
  await expect(panel(page).getByRole('alert')).toContainText('Changed elsewhere')
  await panel(page).getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(input).toHaveCount(0)
  await panel(page).getByRole('button', { name: 'Estimate: ~2h, agent draft by Estimate worker. Edit estimate' }).click()
  await input.fill(''); await panel(page).getByRole('button', { name: 'Save', exact: true }).click()
  await expect(panel(page).getByRole('button', { name: 'Add estimate' })).toBeVisible()
  const patches = calls.filter(c => c.method === 'PATCH' && c.path === '/api/nodes/n-1')
  expect((patches.at(-1)!.body as { fields: Record<string, unknown> }).fields.estimate_hours).toBeNull()
})

test('an epic with open children and no hours shows coverage on the empty estimate', async ({ page }) => {
  const data = world()
  const epic = data.nodes.find(node => node.id === 'n-epic')!
  epic.estimate = { hours: null, estimated_children: 0, open_children: 3 }
  await mockWork(page, data)
  await page.setViewportSize({ width: 1600, height: 900 })
  await page.goto('/p/PHAROS')
  const empty = row(page, 'PHAROS-10').locator('.c-estimate .empty')
  await expect(empty).toBeVisible()
  await expect(empty).toHaveAttribute('data-tip', '0 of 3 open children estimated · agent hours')
  await expect(empty).toHaveAttribute('aria-label', '0 of 3 open children estimated · agent hours')
  await page.setViewportSize({ width: 390, height: 844 })
  await expect(row(page, 'PHAROS-10').locator('.c-estimate')).toBeHidden()
  await expect(row(page, 'PHAROS-11').locator('.c-estimate .mono')).toBeVisible()
  await fits(page)
})

test('read-only work and epic rollups have no estimate editor', async ({ page }) => {
  await mockWork(page, world(), { readOnly: true })
  await page.goto('/p/PHAROS/PHAROS-11')
  await expect(panel(page).locator('.estimate-prop')).toContainText('~2h')
  await expect(panel(page).getByRole('button', { name: /Edit estimate|Add estimate/ })).toHaveCount(0)
  await page.goto('/p/PHAROS/PHAROS-10')
  await expect(panel(page).locator('.estimate-prop')).toContainText('~2.5h')
  await expect(panel(page).getByRole('button', { name: /Edit estimate|Add estimate/ })).toHaveCount(0)
})
