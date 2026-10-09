// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1038: real rendered launch-off controls, cancellable waits and stable actions.
import { expect, test } from '@playwright/test'
import { expectStableControls } from './helpers/stable'
import { mockLeadFlow } from './lead-flow-fixtures'

const OFF = 'Automatic launch is not enabled for this workspace yet.'
const leadName = 'Projektverantwortlicher'
const submit = process.platform === 'darwin' ? 'Meta+Enter' : 'Control+Enter'
for (const width of [390, 1024, 1440]) for (const look of ['light', 'dark'] as const) {
  test(`AEON-1038: launch-off controls stay honest and still at ${width} ${look}`, async ({ page }, info) => {
    const capture = async (name: string) => {
      // Each full navigation restores the person's saved theme. Apply the
      // requested skin again so every artifact really covers its named theme.
      await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, look)
      await expect(page.locator('html')).toHaveAttribute('data-theme', look)
      await page.screenshot({ path: info.outputPath(`${name}-${width}-${look}.png`) })
    }
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    let mocked = await mockLeadFlow(page, { lead: 'none', launchEnabled: false, longText: true, leadName })
    await page.goto('/p/PHAROS')
    await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, look)
    const card = page.locator('section.lead'), start = card.locator('[data-act="main"]'), fold = card.locator('[data-act="fold"]')
    await expect(start).toHaveAttribute('aria-disabled', 'true')
    await expect(card.locator('[data-launch-reason]')).toHaveText(OFF)
    await expectStableControls({ controls: { start, fold }, scrollAreas: { page: page.locator('html') }, interactions: [
      { name: 'unavailable Start ignores a pointer event', run: async () => { await start.dispatchEvent('click'); await expect(page.locator('dialog.lead-sheet')).toHaveCount(0) } },
      { name: 'fold the unavailable card', run: async () => { await fold.click(); await expect(fold).toHaveAttribute('aria-expanded', 'false') } },
      { name: 'unfold the unavailable card', run: async () => { await fold.click(); await expect(fold).toHaveAttribute('aria-expanded', 'true') } },
    ] })
    await capture('lead-card-off')

    await page.goto('/p/PHAROS/PHAROS-14')
    const ticket = page.getByRole('complementary', { name: 'Ticket details' }), ticketStart = ticket.locator('.tline').getByRole('button', { name: /^Start / })
    await expect(ticketStart).toHaveAttribute('aria-disabled', 'true')
    await expect(ticket.locator('.tline')).toContainText(OFF)
    await expectStableControls({ controls: { start: ticketStart, close: ticket.getByRole('button', { name: 'Close ticket details', exact: true }) }, scrollAreas: { ticket }, interactions: [
      { name: 'unavailable ticket Start ignores activation', run: async () => { await ticketStart.dispatchEvent('click'); await expect(page.locator('dialog.lead-sheet')).toHaveCount(0) } },
    ] })
    await capture('ticket-lead-off')

    await page.goto('/agents')
    const row = page.locator('[data-project="PHAROS"].lead-row'), add = page.getByRole('button', { name: /^New: / })
    await expect(row).toHaveAttribute('aria-disabled', 'true')
    await expect(row).toContainText(OFF)
    await expectStableControls({ controls: { row, add }, scrollAreas: { page: page.locator('html') }, interactions: [
      { name: 'unavailable row ignores activation', run: async () => { await row.dispatchEvent('click'); await expect(page.locator('dialog.lead-sheet')).toHaveCount(0) } },
      { name: 'open New', run: async () => { await add.click(); await expect(page.getByRole('menuitem', { name: /^Start / })).toHaveAttribute('aria-disabled', 'true') } },
    ] })
    const menuStart = page.getByRole('menuitem', { name: /^Start / })
    await expectStableControls({ controls: { start: menuStart, add }, interactions: [
      { name: 'disabled menu item keeps the menu open', run: async () => { await menuStart.dispatchEvent('click'); await expect(menuStart).toBeVisible(); await expect(page.locator('dialog.lead-sheet')).toHaveCount(0) } },
    ] })
    await capture('agents-lead-off')
    expect(mocked.state.calls.filter(c => c.method === 'POST' || c.method === 'PUT')).toEqual([])

    // Simulate stale launch-on lead data, followed by fresh launch-off settings.
    // Every entry route still converges on the sheet's own launch check.
    await page.unrouteAll({ behavior: 'wait' })
    mocked = await mockLeadFlow(page, { lead: 'none', launchEnabled: true, leadName })
    mocked.state.settings.automatic_launch_enabled = false
    await page.goto('/p/PHAROS')
    await start.click()
    const sheet = page.getByRole('dialog', { name: `Start ${leadName.toLowerCase()} for PHAROS` }), sheetStart = sheet.locator('[data-act="start"]'), cancel = sheet.locator('[data-act="cancel"]'), change = sheet.locator('[data-act="host"]')
    await expect(sheetStart).toHaveAttribute('aria-disabled', 'true')
    await expect(sheet.locator('[data-launch-reason]')).toHaveText(OFF)
    await expectStableControls({ controls: { start: sheetStart, cancel, change, ...(width === 390 ? { frame: sheet } : {}) }, scrollAreas: { body: sheet.locator('.sheet-body') }, interactions: [
      { name: 'reveal host choices', run: async () => { await change.click(); await expect(sheet.getByRole('radiogroup', { name: 'Host' })).toBeVisible() } },
    ] })
    const hosts = sheet.getByRole('radiogroup', { name: 'Host' }), fixed = hosts.getByRole('radio', { name: /mbp2607/ }), removed = hosts.getByRole('radio', { name: /mbp2606/ }), auto = hosts.getByRole('radio', { name: /Automatic/ })
    await expectStableControls({ controls: { start: sheetStart, cancel, change, hosts, fixed, removed, auto }, scrollAreas: { body: sheet.locator('.sheet-body') }, interactions: [
      { name: 'select a fixed host', run: async () => { await fixed.click(); await expect(fixed).toHaveAttribute('aria-checked', 'true') } },
      { name: 'removed host explains itself in place', run: async () => { await removed.dispatchEvent('click'); await expect(sheet.locator('.choice-note')).toContainText('being removed') } },
      { name: 'pointer cannot submit', run: async () => { await sheetStart.dispatchEvent('click'); await expect(sheet).toBeVisible() } },
      { name: 'keyboard cannot submit', run: async () => { await sheetStart.focus(); await page.keyboard.press(submit); await expect(sheet).toBeVisible() } },
    ] })
    await capture('start-sheet-off')
    expect(mocked.state.calls.filter(c => c.method === 'POST' || c.method === 'PUT')).toEqual([])
    await cancel.click()

    await page.unrouteAll({ behavior: 'wait' })
    mocked = await mockLeadFlow(page, { lead: 'paused', processActive: false, launchEnabled: false, leadName })
    await page.goto('/p/PHAROS')
    await expect(start).toHaveAttribute('aria-disabled', 'true')
    await card.getByRole('button', { name: /PHAROS .*: open details/ }).click()
    const panel = page.getByRole('complementary', { name: `PHAROS ${leadName.toLowerCase()}` }), resume = panel.locator('[data-act="resume"]'), close = panel.getByRole('button', { name: 'Close details' })
    await expect(resume).toHaveAttribute('aria-disabled', 'true')
    await expect(panel.locator('.pane-foot')).toContainText(OFF)
    await expectStableControls({ controls: { resume, close }, scrollAreas: { body: panel.locator('.pane-body') }, interactions: [
      { name: 'Resume cannot submit', run: async () => { await resume.dispatchEvent('click'); await expect(resume).toHaveAttribute('aria-disabled', 'true') } },
    ] })
    await capture('lead-panel-off')
    expect(mocked.state.calls.filter(c => c.method === 'POST' || c.method === 'PUT')).toEqual([])
  })
}

test('AEON-1038: old waiting starts remain cancellable; actual failures and pickup timeout are distinct', async ({ page }) => {
  const { state } = await mockLeadFlow(page, { lead: 'waiting_for_room', launchEnabled: false, decisions: false })
  state.leads['p-pharos'] = { project_id: 'p-pharos', revision: 1, generation: 0, session_id: null, state: 'waiting_for_room', reason: 'start_checks_unavailable', process_active: false, automatic_launch_enabled: false }
  await page.goto('/p/PHAROS')
  const card = page.locator('section.lead'), action = card.locator('[data-act="main"]')
  await expect(card).toContainText('Requested · waiting for a runtime')
  await expect(card).toContainText(OFF)
  await expect(action).toHaveText('Cancel start')
  await expect(action).toHaveAttribute('aria-disabled', 'false')
  await action.click()
  expect(state.calls.find(c => c.method === 'POST' && c.path.endsWith('/lead/pause'))?.body).toEqual({ expected_revision: 1, generation: 0 })
  await expect(card).toContainText('Paused before it started')
  state.leads['p-pharos'] = { ...state.leads['p-pharos']!, state: 'waiting_for_room', reason: 'host_unavailable', revision: 3 }
  await page.reload()
  await expect(card).toContainText('host load can’t be read')
  await expect(card).not.toContainText('waiting for a runtime')
  state.leads['p-pharos'] = { ...state.leads['p-pharos']!, reason: 'runtime_pickup_timeout', automatic_launch_enabled: true }
  await page.reload()
  await expect(card).toContainText('Nothing picked this up')
  await expect(action).toHaveText('Cancel start')
})
