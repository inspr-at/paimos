// SPDX-License-Identifier: AGPL-3.0-only
// Risk: confirmation moves actions or removes the wrong lead; failures must stay visible.
import { expect, test } from '@playwright/test'
import { expectStableControls } from './helpers/stable'
import { mockLeadFlow } from './lead-flow-fixtures'

for (const width of [390, 1024, 1440]) for (const look of ['light', 'dark'] as const) {
  test(`AEON-1040: remove confirmation stays still on card and Leads at ${width} ${look}`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    const { state } = await mockLeadFlow(page, { lead: 'paused', queue: [], decisions: false, launchEnabled: false, leadName: 'Projektverantwortlicher' })
    const unstarted = { ...state.leads['p-pharos']!, generation: 0, session_id: null, process_active: false, revision: 2 }
    state.leads['p-pharos'] = { ...unstarted }
    for (const view of ['card', 'list']) {
      state.leads['p-pharos'] = { ...unstarted }
      await page.goto(view === 'card' ? '/p/PHAROS' : '/agents')
      await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, look)
      const frame = view === 'card' ? page.locator('section.lead') : page.locator('.lead-list-item').filter({ has: page.locator('[data-project="PHAROS"]') })
      const trigger = frame.locator('[data-act="lead-menu"]')
      const row = view === 'card' ? frame.locator('[data-act="main"]') : frame.locator('.lead-row')
      await expectStableControls({ controls: { trigger, row }, scrollAreas: { page: page.locator('html') }, interactions: [
        { name: 'open removal menu', run: async () => { await trigger.click(); await expect(page.getByRole('dialog', { name: /More PHAROS .* actions/ })).toBeVisible() } },
      ] })
      const menu = page.getByRole('dialog', { name: /More PHAROS .* actions/ }), remove = menu.getByRole('button', { name: 'Remove', exact: true }), cancel = menu.getByRole('button', { name: 'Cancel', exact: true })
      await expectStableControls({ controls: { trigger, row, remove }, scrollAreas: { menu }, interactions: [
        { name: 'inline confirmation', run: async () => { await remove.click(); await expect(menu).toContainText('Remove the PHAROS projektverantwortlicher?'); await expect(cancel).toBeVisible() } },
      ] })
      await page.screenshot({ path: info.outputPath(`lead-remove-${view}-${width}-${look}.png`) })
      await expectStableControls({ controls: { trigger, row, remove, cancel }, scrollAreas: { menu }, interactions: [
        { name: 'server conflict stays inline', run: async () => {
          await page.route('**/api/projects/p-pharos/lead', async route => {
            if (route.request().method() === 'DELETE') await route.fulfill({ status: 409, json: { error: 'lead revision conflict' } })
            else await route.fallback()
          })
          await remove.click(); await expect(menu.getByRole('alert')).toHaveText('lead revision conflict')
          await expect(frame).toBeVisible()
          await page.unroute('**/api/projects/p-pharos/lead')
        } },
      ] })
      await cancel.click()
      await expect(menu.getByRole('alert')).toHaveCount(0)
      await remove.click()
      await remove.click()
      if (view === 'card') await expect(frame).toContainText('No projektverantwortlicher')
      else await expect(page.locator('[data-project="PHAROS"].lead-row')).toHaveCount(0)
      expect(state.leads['p-pharos']?.state).toBe('none')
    }
    expect(state.calls.filter(c => c.method === 'DELETE').map(c => c.body)).toEqual([{ expected_revision: 2 }, { expected_revision: 2 }])
    expect(state.queue).toEqual([])
  })
}

test('AEON-1040: a previously started lead has no Remove control', async ({ page }) => {
  await mockLeadFlow(page, { lead: 'paused', processActive: false })
  await page.goto('/p/PHAROS')
  await page.locator('section.lead [data-act="lead-menu"]').click()
  await expect(page.getByRole('button', { name: 'Open session', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Remove', exact: true })).toHaveCount(0)
})
