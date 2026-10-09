// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
import { fixtures, mockWork } from './work-fixtures'

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark']) {
  test(`derived status refresh at unchanged edit revision · ${width} · ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 })
    const data = fixtures()
    const node = data.nodes.find(n => n.id === 'n-2')!
    const revision = node.updated_at
    const children = data.nodes.filter(n => n.parent_id === node.id)
    expect(children.length).toBeGreaterThan(0)
    // This route is the event stream. The fixture's quiet EventSource never
    // requests it, so the derived refresh would never arrive.
    await mockWork(page, data, { nativeEvents: true })
    let changed = false
    let connected = false
    await page.route('**/api/events/stream**', route => {
      connected = true
      const event = { id: 702, actor_principal_id: 'system', node_id: node.id, type: 'status_autopilot.derived', before: null, after: null, at: revision, undo_of: null, node_changes: [{ id: node.id, project_id: node.project, change: 'updated', fields: ['state'], revision }] }
      const lines = ['retry: 150', '', 'id: 700', 'event: stream.ready', 'data: {"after":700,"resumed":true}', '']
      if (changed) {
        const childEvent = { ...event, id: 701, actor_principal_id: 'person', node_id: children[0]!.id, type: 'node.updated', node_changes: children.map(child => ({ id: child.id, project_id: child.project, change: 'updated', fields: ['state'], revision: child.updated_at })) }
        lines.push('id: 701', 'event: node.updated', `data: ${JSON.stringify(childEvent)}`, '', 'id: 702', 'event: status_autopilot.derived', `data: ${JSON.stringify(event)}`, '')
      }
      return route.fulfill({ status: 200, headers: { 'content-type': 'text/event-stream' }, body: `${lines.join('\n')}\n` })
    })
    await page.goto('/p/PHAROS/PHAROS-12')
    await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
    const panel = page.getByRole('complementary', { name: 'Ticket details' })
    await expect(panel.getByRole('heading', { name: 'Add an Oracle Cloud connector' })).toBeVisible()
    await expect.poll(() => connected).toBe(true)
    const edit = panel.getByRole('button', { name: 'Edit', exact: true })
    const before = await edit.boundingBox()
    expect(before && before.width > 0 && before.height > 0).toBeTruthy()
    for (const child of children) { child.state = 'done'; child.updated_at = new Date(Date.parse(child.updated_at) + 1000).toISOString() }
    node.state = 'done'
    changed = true
    await expect(panel.getByRole('button', { name: /Status: Done/ })).toBeVisible()
    expect(node.updated_at).toBe(revision)
    await expect(panel.getByText('Changed elsewhere meanwhile')).toHaveCount(0)
    const after = await edit.boundingBox()
    for (const key of ['x', 'y', 'width', 'height'] as const) expect(Math.abs(after![key] - before![key])).toBeLessThanOrEqual(0.5)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    const folder = 'test-results/aeon-650-wn'
    await mkdir(folder, { recursive: true })
    await page.screenshot({ path: `${folder}/derived-parent-${width}-${theme}.png`, fullPage: true })
  })
}
