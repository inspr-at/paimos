// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test } from '@playwright/test'
import { expectStableControls } from './helpers/stable'
import { LEAD_AGENT, LEAD_SESSION, mockLeadFlow } from './lead-flow-fixtures'

for (const width of [1440, 1024, 400, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`person confirms the running coordinator without moving controls at ${width} ${theme}`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: width < 600 ? 844 : 1100 })
    const { data, state } = await mockLeadFlow(page, { lead: 'none', queue: ['n-4'], longText: true })
    const candidates = [
      { id: LEAD_SESSION, display_label: 'AEON-LEAD · Bestehender Projektkontext und gespeicherte Arbeitsnotizen bleiben erhalten', host: 'build-7', harness: 'codex', management_mode: 'unmanaged', reported_at: new Date().toISOString() },
      { id: 'second-session', display_label: 'Projektkoordination mit einer anderen laufenden Sitzung', host: 'build-6', harness: 'claude', management_mode: 'unmanaged', reported_at: new Date().toISOString() },
    ]
    const writes: unknown[] = []
    await page.route('**/api/projects/p-pharos/lead/*', async route => {
      const path = new URL(route.request().url()).pathname
      if (path.endsWith('/candidates')) return route.fulfill({ json: { items: candidates, next_cursor: null } })
      if (!path.endsWith('/adopt')) return route.fallback()
      const body = route.request().postDataJSON()
      writes.push(body)
      state.leads['p-pharos'] = { ...state.leads['p-pharos']!, project_id: 'p-pharos', revision: 1, generation: 0, session_id: body.session_id, state: 'waiting_for_room', reason: 'adoption_pending', process_active: true }
      return route.fulfill({ json: state.leads['p-pharos'] })
    })
    await page.goto('/p/PHAROS')
    await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
    const card = page.locator('section.lead'), toggle = card.locator('[data-act="adopt-open"]')
    await expect(toggle).toBeVisible(); await toggle.click()
    const options = card.getByRole('radiogroup', { name: 'Running coordinator sessions' }), first = options.getByRole('radio').nth(0), second = options.getByRole('radio').nth(1)
    const confirm = card.getByRole('button', { name: 'Confirm adoption' }), cancel = card.locator('[data-act="adopt-cancel"]')
    await expect(first).toContainText('build-7'); await expect(first).toContainText('Fresh heartbeat')
    await expect(confirm).toHaveAttribute('aria-disabled', 'true')
    await expectStableControls({ controls: { toggle, confirm, cancel, options, 'first clicked row': first, 'second clicked row': second }, scrollAreas: { options }, interactions: [
      { name: 'select the first root coordinator', run: async () => { await first.click(); await expect(first).toHaveAttribute('aria-checked', 'true'); await expect(confirm).toHaveAttribute('aria-disabled', 'false') } },
      { name: 'select the second root coordinator', run: async () => { await second.click(); await expect(second).toHaveAttribute('aria-checked', 'true') } },
      { name: 'return to the original coordinator with the keyboard', run: async () => { await first.focus(); await page.keyboard.press('Space'); await expect(first).toHaveAttribute('aria-checked', 'true') } },
    ] })
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
    expect(overflow).toBeLessThanOrEqual(1)
    await page.screenshot({ path: info.outputPath(`selection-${width}-${theme}.png`) })
    await confirm.click()
    await expect(card).toContainText('Adoption confirmed · waiting for session proof')
    expect(writes).toEqual([{ expected_revision: 0, session_id: LEAD_SESSION }])
    expect(state.calls.filter(call => call.method === 'POST' && call.path === '/api/projects/p-pharos/lead')).toHaveLength(0)
    await page.screenshot({ path: info.outputPath(`pending-${width}-${theme}.png`) })
    await expect(card.getByRole('button', { name: 'Cancel', exact: true })).toBeVisible()
    await expect(card.getByRole('button', { name: 'Pause…', exact: true })).toHaveCount(0)
    await expect(card.getByRole('button', { name: 'Resume', exact: true })).toHaveCount(0)
    await card.getByRole('button', { name: 'PHAROS lead: open details' }).click()
    const pendingPanel = page.locator('.lead-panel')
    await expect(pendingPanel.locator('[data-act="pause"]')).toHaveCount(0)
    await expect(pendingPanel.locator('[data-act="resume"]')).toHaveCount(0)
    const cancelAdoption = pendingPanel.locator('[data-act="cancel-adoption"]')
    await expect(cancelAdoption).toBeVisible()
    await page.screenshot({ path: info.outputPath(`pending-panel-${width}-${theme}.png`) })
    const closePending = pendingPanel.getByRole('button', { name: 'Close details' })
    await expectStableControls({ controls: { close: closePending, cancel: cancelAdoption }, scrollAreas: { body: pendingPanel.locator('.pane-body') }, interactions: [
      { name: 'read the pending adoption without moving Cancel', run: async () => { await pendingPanel.locator('.pane-body').evaluate(el => { el.scrollTop = el.scrollHeight }) } },
    ] })
    await closePending.click()
    await expect(pendingPanel).toHaveCount(0)
    // The server suite proves the lease transition. Feed its resulting snapshot
    // to the card and retain the existing session ID/context, queue and history.
    state.leads['p-pharos'] = { ...state.leads['p-pharos']!, revision: 2, generation: 1, state: 'working', reason: '' }
    data.live.push({ project_id: 'p-pharos', session_id: LEAD_SESSION, principal_id: LEAD_AGENT, name: 'AEON-LEAD', harness: 'codex', management_mode: 'unmanaged', role: 'coordinator', phase: 'working', activity: 'busy', ticket: null, since: new Date().toISOString(), heartbeat_at: new Date().toISOString(), finished: false })
    await page.reload()
    await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
    await expect(card).toContainText('Unmanaged: steering limited to messages and pause')
    await expect(card.getByRole('button', { name: 'Pause…', exact: true })).toBeVisible()
    await expect(card.getByRole('button', { name: /Start lead|Cancel start|Resume/ })).toHaveCount(0)
    await expect(card).toContainText('at the gate'); await expect(card).toContainText('merged today')
    await page.screenshot({ path: info.outputPath(`unmanaged-${width}-${theme}.png`) })
    await card.getByRole('button', { name: 'PHAROS lead: open details' }).click()
    const panel = page.locator('.lead-panel')
    await expect(panel).toContainText('Unmanaged: steering limited to messages and pause')
    const close = panel.getByRole('button', { name: 'Close details' }), pause = panel.locator('[data-act="pause"]')
    await expectStableControls({ controls: { close, pause }, scrollAreas: { body: panel.locator('.pane-body') }, interactions: [
      { name: 'read longer history without moving the footer', run: async () => { await panel.locator('.pane-body').evaluate(el => { el.scrollTop = el.scrollHeight }) } },
    ] })
    await page.screenshot({ path: info.outputPath(`unmanaged-panel-${width}-${theme}.png`) })
  })
}

test('candidate errors and rejected confirmations remain visible with no false adoption', async ({ page }) => {
  const { state } = await mockLeadFlow(page, { lead: 'none', queue: [] })
  let unavailable = true
  await page.route('**/api/projects/p-pharos/lead/*', route => {
    if (new URL(route.request().url()).pathname.endsWith('/candidates')) return route.fulfill(unavailable ? { status: 503, json: { error: 'Running sessions unavailable' } } : { json: { items: [{ id: LEAD_SESSION, display_label: 'AEON-LEAD', host: 'build-7', harness: 'codex', management_mode: 'unmanaged', reported_at: new Date().toISOString() }], next_cursor: null } })
    return route.fulfill({ status: 409, json: { error: 'The selected coordinator is no longer reporting' } })
  })
  await page.goto('/p/PHAROS')
  const card = page.locator('section.lead')
  await card.getByRole('button', { name: 'Adopt a running session' }).click()
  await expect(card).toContainText('Running sessions unavailable')
  await card.getByRole('button', { name: 'Cancel', exact: true }).click(); unavailable = false
  await card.getByRole('button', { name: 'Adopt a running session' }).click()
  await card.getByRole('radio').click(); await card.getByRole('button', { name: 'Confirm adoption' }).click()
  await expect(card).toContainText('The selected coordinator is no longer reporting')
  expect(state.leads['p-pharos']?.state).toBe('none')
})

test('cancelling an unclaimed adoption clears only the selection', async ({ page }) => {
  const { state } = await mockLeadFlow(page, { lead: 'none', queue: [] })
  const cancels: unknown[] = []
  const pauses: string[] = []
  await page.route('**/api/projects/p-pharos/lead/**', async route => {
    const path = new URL(route.request().url()).pathname
    if (path.endsWith('/candidates')) return route.fulfill({ json: { items: [{ id: LEAD_SESSION, display_label: 'AEON-LEAD', host: 'build-7', harness: 'codex', management_mode: 'unmanaged', reported_at: new Date().toISOString() }], next_cursor: null } })
    if (path.endsWith('/adopt/cancel')) {
      cancels.push(route.request().postDataJSON())
      const current = state.leads['p-pharos']!
      state.leads['p-pharos'] = { ...current, revision: current.revision + 1, session_id: null, reason: 'selection_cleared', state: 'waiting_for_room', generation: 0, process_active: false }
      return route.fulfill({ json: state.leads['p-pharos'] })
    }
    if (path.endsWith('/pause')) { pauses.push(route.request().method()); return route.fulfill({ status: 500, json: { error: 'pause must not run' } }) }
    if (!path.endsWith('/adopt')) return route.fallback()
    const body = route.request().postDataJSON()
    state.leads['p-pharos'] = { ...state.leads['p-pharos']!, project_id: 'p-pharos', revision: 1, generation: 0, session_id: body.session_id, state: 'waiting_for_room', reason: 'adoption_pending', process_active: true }
    return route.fulfill({ json: state.leads['p-pharos'] })
  })
  await page.goto('/p/PHAROS')
  const card = page.locator('section.lead')
  await card.getByRole('button', { name: 'Adopt a running session' }).click()
  await card.getByRole('radio').click()
  await card.getByRole('button', { name: 'Confirm adoption' }).click()
  await expect(card).toContainText('Adoption confirmed · waiting for session proof')
  await card.getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect.poll(() => cancels).toEqual([{ expected_revision: 1 }])
  expect(pauses).toEqual([])
  expect(state.calls.filter(call => call.method === 'POST' && call.path.endsWith('/pause'))).toEqual([])
  await expect(card).toContainText('No session selected')
  await expect(card).toContainText('The previous choice was cleared. That session kept running.')
  await card.getByRole('button', { name: 'Start lead', exact: true }).click()
  const sheet = page.getByRole('dialog', { name: 'Start lead for PHAROS' })
  await expect(sheet).toBeVisible()
  await expect(sheet).not.toContainText('already has')
  await sheet.locator('[data-act="start"]').click()
  await expect.poll(() => state.calls.filter(call => call.method === 'POST' && call.path === '/api/projects/p-pharos/lead')).toHaveLength(1)
  expect(pauses).toEqual([])
})
