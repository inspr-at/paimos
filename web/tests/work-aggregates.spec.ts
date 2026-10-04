// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { expectStableControls } from './helpers/stable'

for (const theme of ['light', 'dark']) for (const width of [390, 1024, 1440]) {
  test(`parent leaf totals at ${width} ${theme}`, async ({ page }) => {
    const errors = watchErrors(page)
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.clock.setSystemTime(new Date('2026-10-04T10:00:00Z'))
    const data = fixtures()
    data.preferences.theme = { choice: theme }
    data.preferences['list:p-pharos'] = { visible: ['key', 'title', 'status', 'estimate', 'progress', 'eta'] }
    const parent = data.nodes.find(n => n.id === 'n-1')!
    parent.title = 'Zuverlässige Fortschrittsberechnung für verschachtelte Arbeitsbereiche und langfristige Lieferplanung'
    parent.fields.estimate_hours = 40
    parent.estimate = { hours: 32, planned_hours: 40, is_parent: true, estimated_children: 6, open_children: 9, leaf_count: 9, estimated_leaves: 6 }
    parent.eta = { finished: false, progress_pct: 63, leaf_count: 9, estimated_leaves: 6, progress_basis: 'estimate', open_leaves: 5, ready_leaves: 3, live_leaves: 0, ready_partial: true, eta_ready_at: '2026-10-04T11:00:00Z', ready_reported_at: '2026-10-04T09:59:00Z' }
    await mockWork(page, data)
    await page.goto('/p/PHAROS')
    const row = page.locator('tr.ticket-row:not(.ghost)').filter({ has: page.locator('.key', { hasText: /^PHAROS-11$/ }) })
    const estimate = row.locator('.c-estimate .mono')
    await expect(estimate).toHaveText(/~32h leaves.*~40h planned/)
    await expect(estimate).toHaveAttribute('data-tip', /6 of 9 leaves estimated/)
    await expect(row.locator('.progress-read')).toHaveAccessibleName(/63% done.*6 of 9 leaves estimated/)
    await expect(row.locator('.eta-cell')).toHaveAttribute('data-tip', /Partial ETA/)
    const title = row.locator('.title-text')
    await expectStableControls({ controls: { 'parent row': row, 'open parent': title }, scrollAreas: { document: page.locator('html') }, interactions: [{ name: 'read leaf coverage', run: async () => { await estimate.hover(); await expect(estimate).toHaveAttribute('data-tip', /6 of 9 leaves/); await page.mouse.move(0, 0) } }] })
    await page.screenshot({ path: `test-results/aeon-651-wn/list-${width}-${theme}.png`, fullPage: true })
    await title.click()
    const panel = page.getByRole('complementary', { name: 'Ticket details' })
    const control = panel.getByRole('button', { name: 'Parent estimate: ~32h from leaves · ~40h planned. Edit planned estimate' })
    await expect(control).toBeVisible()
    // Drawer resize may compress the list: estimate text must stay in its cell.
    const cellFit = await estimate.evaluate(el => { const a=el.getBoundingClientRect(), c=el.closest('td')!.getBoundingClientRect(); return a.left>=c.left && a.right<=c.right })
    expect(cellFit).toBe(true)
    await expectStableControls({ controls: { 'parent plan': control }, scrollAreas: { panel }, interactions: [{ name: 'read parent estimate', run: async () => { await control.hover(); await expect(control).toHaveAttribute('data-tip', /kept separately/); await page.mouse.move(0, 0) } }] })
    await page.screenshot({ path: `test-results/aeon-651-wn/parent-${width}-${theme}.png`, fullPage: true })
    await control.click()
    const input = panel.getByRole('textbox', { name: 'Estimate in agent hours' })
    await expect(input).toHaveValue('40')
    const save = panel.getByRole('button', { name: /^Save/ }), cancel = panel.getByRole('button', { name: /^Cancel/ })
    await expectStableControls({ controls: { 'save plan': save, 'cancel plan': cancel, 'planned hours': input }, scrollAreas: { panel }, interactions: [
      { name: 'invalid plan feedback', run: async () => { await input.fill('201'); await save.click(); await expect(panel.getByRole('alert')).toContainText('up to 200 hours') } },
      { name: 'correct plan', run: async () => { await input.fill('40'); await expect(panel.getByRole('alert')).toHaveCount(0) } },
    ] })
    await input.press('Enter'); await expect(input).toBeVisible()
    let release!: () => void
    const response = new Promise<void>(resolve => { release = resolve })
    await page.route('**/api/nodes/n-1', async route => {
      if (route.request().method() !== 'PATCH') return route.fallback()
      await response
      await route.fulfill({ status: 503, json: { error: 'temporarily_unavailable' } })
    })
    await expectStableControls({ controls: { 'save plan': save, 'cancel plan': cancel, 'planned hours': input }, scrollAreas: { panel }, interactions: [
      { name: 'pending save', run: async () => { await save.click(); await expect(save).toBeDisabled(); await expect(save).toHaveText(/Saving/) } },
      { name: 'failed save feedback', run: async () => { release(); await expect(panel.getByRole('alert')).toContainText('not saved'); await expect(save).toBeEnabled() } },
    ] })
    await input.press('Escape'); await expect(input).not.toBeFocused(); await expect(input).toBeVisible()
    await cancel.click()
    expect(errors).toEqual([])
  })
}
