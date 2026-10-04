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
    for (const node of data.nodes) node.kind_slug = 'work'
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

for (const type of ['status_autopilot.changed', 'status_autopilot.undone', 'import.node_updated', 'import.node_created', 'import.parent_changed']) {
  test(`live leaf aggregates refresh from ${type} with unchanged parent status`, async ({ page }) => {
    // An isolated stream delivers exactly one ready envelope and the named
    // change. No heartbeat, polling replay or parent derivation can mask it.
    await page.addInitScript(() => {
      const sources: EventTarget[] = []
      class QuietSource extends EventTarget {
        readyState = 1
        onerror = null
        constructor(readonly url: string) {
          super()
          if (url.includes('/api/events/stream')) {
            sources.push(this)
            queueMicrotask(() => this.dispatchEvent(new MessageEvent('stream.ready', { data: JSON.stringify({ after: 700, resumed: false }) })))
          }
        }
        close() { this.readyState = 2 }
      }
      Object.assign(window, { EventSource: QuietSource, aggregateEvent: (event: { id: number; type: string }) => {
        for (const source of sources) source.dispatchEvent(new MessageEvent(event.type, { data: JSON.stringify(event), lastEventId: String(event.id) }))
      } })
    })
    const errors = watchErrors(page)
    const data = fixtures()
    for (const node of data.nodes) node.kind_slug = 'work'
    data.preferences['list:p-pharos'] = { visible: ['key', 'title', 'status', 'estimate', 'progress', 'eta'] }
    const parent = data.nodes.find(node => node.id === 'n-1')!
    const leaf = data.nodes.find(node => node.id === 'n-4')!
    leaf.parent_id = parent.id
    parent.estimate = { hours: 10, planned_hours: 40, is_parent: true, leaf_count: 2, estimated_leaves: 2, estimated_children: 2, open_children: 2 }
    parent.eta = { finished: false, progress_pct: 20, leaf_count: 2, estimated_leaves: 2, progress_basis: 'estimate', open_leaves: 1 }
    const revision = parent.updated_at, state = parent.state
    const calls = await mockWork(page, data)
    await page.goto('/p/PHAROS')
    const row = page.locator('#row-n-1')
    await expect(row.locator('.progress-read')).toHaveAccessibleName(/20% done/)
    await row.locator('.title-text').click()
    const panel = page.getByRole('complementary', { name: 'Ticket details' })
    const plan = panel.getByRole('button', { name: /^Parent estimate:/ })
    await expect(plan).toHaveAccessibleName(/~10h from leaves/)
    const listReads = calls.filter(call => call.path === '/api/nodes' && call.method === 'GET').length
    await expectStableControls({ controls: { 'parent plan': plan }, scrollAreas: { panel }, interactions: [{ name: `receive ${type}`, run: async () => {
      parent.estimate!.hours = 18
      parent.eta!.progress_pct = 60
      await page.evaluate(({ type, leaf, revision }) => {
        const send = (window as unknown as { aggregateEvent: (event: unknown) => void }).aggregateEvent
        send({ id: 701, type, actor_principal_id: 'agent', node_changes: [{ id: leaf.id, project_id: leaf.project,
          change: type === 'import.node_created' ? 'created' : 'updated', fields: type === 'import.parent_changed' ? ['parent_id'] : ['state', 'fields.estimate_hours'], revision }] })
      }, { type, leaf, revision })
      await expect(plan).toHaveAccessibleName(/~18h from leaves/)
      await expect(row.locator('.progress-read')).toHaveAccessibleName(/60% done/)
    } }] })
    expect(parent.updated_at).toBe(revision)
    expect(parent.state).toBe(state)
    expect(calls.filter(call => call.path === '/api/nodes' && call.method === 'GET').length).toBeGreaterThan(listReads)
    expect(errors).toEqual([])
  })
}

for (const theme of ['light', 'dark']) for (const width of [390, 1024, 1440]) {
  test(`unfinished weighted 100% parent retains ETA at ${width} ${theme}`, async ({ page }) => {
    const errors = watchErrors(page)
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.clock.setSystemTime(new Date('2026-10-04T10:00:00Z'))
    // Report staleness is the subject here; keep the connection healthy so
    // reconnect rendering cannot replace the relative ETA or its tooltip.
    await page.addInitScript(() => {
      class HealthySource extends EventTarget {
        readyState = 1
        onopen: (() => void) | null = null
        onerror: (() => void) | null = null
        private heartbeat: ReturnType<typeof setInterval>
        constructor(readonly url: string) {
          super()
          queueMicrotask(() => {
            if (this.readyState === 2) return
            this.onopen?.()
            this.dispatchEvent(new MessageEvent('stream.ready', { data: JSON.stringify({ after: 700, resumed: false }), lastEventId: '700' }))
          })
          this.heartbeat = setInterval(() => this.dispatchEvent(new MessageEvent('stream.ping', { data: '{}' })), 10_000)
        }
        close() { this.readyState = 2; clearInterval(this.heartbeat) }
      }
      Object.assign(window, { EventSource: HealthySource })
    })
    const data = fixtures()
    for (const node of data.nodes) node.kind_slug = 'work'
    data.preferences.theme = { choice: theme }
    data.preferences['list:p-pharos'] = { visible: ['key', 'title', 'status', 'estimate', 'progress', 'eta'] }
    const parent = data.nodes.find(node => node.id === 'n-1')!
    parent.title = 'Zuverlässige Fortschrittsberechnung für verschachtelte Arbeitsbereiche und langfristige Lieferplanung'
    parent.estimate = { hours: 32, planned_hours: 40, is_parent: true, estimated_children: 1, open_children: 2, leaf_count: 2, estimated_leaves: 1 }
    parent.eta = { finished: false, progress_pct: 100, leaf_count: 2, estimated_leaves: 1, progress_basis: 'estimate', open_leaves: 1, ready_leaves: 0, ready_partial: true, eta_ready_at: '2026-10-04T12:00:00Z', ready_reported_at: '2026-10-04T08:00:00Z', ready_stale: true }
    await mockWork(page, data)
    await page.goto('/p/PHAROS')
    await expect(page.locator('.list-freshness')).toHaveText('Live')
    const row = page.locator('#row-n-1')
    const eta = row.locator('.eta-cell')
    await expect(eta.locator('.shown')).toHaveText('~2 h · partial')
    await expect(eta).toHaveClass(/stale/)
    await expect(eta).toHaveAttribute('data-tip', /work remains/)
    await expect(eta).not.toHaveAttribute('data-tip', /100% done/)
    const title = row.locator('.title-text')
    await expectStableControls({ controls: { 'parent row': row, 'open parent': title }, scrollAreas: { document: page.locator('html') }, interactions: [{ name: 'read unfinished ETA', run: async () => {
      await eta.hover()
      await expect(eta).toHaveAttribute('data-tip', /Partial ETA/)
      await page.mouse.move(0, 0)
    } }] })
    await page.screenshot({ path: `test-results/aeon-651-fix6/unfinished-list-${width}-${theme}.png`, fullPage: true })
    await title.click()
    const panel = page.getByRole('complementary', { name: 'Ticket details' })
    const plan = panel.getByRole('button', { name: /^Parent estimate:/ })
    await expect(plan).toBeVisible()
    await expect(panel.locator('.eta-cell .shown')).toContainText('~2 h · partial')
    await expectStableControls({ controls: { 'parent plan': plan }, scrollAreas: { panel }, interactions: [{ name: 'read parent plan', run: async () => {
      await plan.hover()
      await expect(plan).toHaveAttribute('data-tip', /kept separately/)
      await page.mouse.move(0, 0)
    } }] })
    await page.screenshot({ path: `test-results/aeon-651-fix6/unfinished-parent-${width}-${theme}.png`, fullPage: true })
    expect(errors).toEqual([])
  })
}
