// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { controlStability } from './control-stability'

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark']) {
  test(`unified Outline nests and unnests work ${width} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    await page.clock.setSystemTime(new Date('2026-10-04T10:00:00Z'))
    const data = fixtures(); data.preferences.theme = { choice: theme }
    const template = data.nodes.find(n => n.id === 'n-epic')!
    data.nodes = [
      { ...template, id: 'group', key: 'PHAROS-1', parent_id: 'p-pharos', title: 'Vorhaben mit ausführlicher Beschreibung für verschachtelte Arbeit', kind_slug: 'work', is_leaf: false, work_children_count: 1 },
      { ...template, id: 'child', key: 'PHAROS-2', parent_id: 'group', title: 'Bereits verschachtelter Arbeitsschritt', kind_slug: 'work', is_leaf: true },
      { ...template, id: 'moving', key: 'PHAROS-3', parent_id: 'p-pharos', title: 'Arbeitsschritt verschachteln und wieder zur Projektebene zurückführen', kind_slug: 'work', is_leaf: true },
    ]
    await page.addInitScript(() => {
      window.EventSource = class {
        static CONNECTING = 0; static OPEN = 1; static CLOSED = 2
        readyState = 1
        addEventListener() {}
        close() { this.readyState = 2 }
      } as unknown as typeof EventSource
    })
    const errors = watchErrors(page), calls = await mockWork(page, data, { admin: true })
    await page.goto('/p/PHAROS/tickets?view=outline&closed=1&sort=key')
    const tree = page.getByRole('treegrid', { name: 'Ticket outline' })
    const row = (id: string) => tree.locator(`#row-${id}`)
    const moving = row('moving'), destination = tree.getByRole('row', { name: 'Project root drop destination' })
    await expect(moving).toHaveAttribute('aria-level', '1')
    const stable = await controlStability(page, { create: page.getByRole('button', { name: 'New work item', exact: true }), destination })
    await stable.check(async () => {
      await moving.dragTo(row('group'))
      await expect(moving).toHaveAttribute('aria-level', '2')
    })
    const first = calls.filter(c => c.method === 'POST' && c.path === '/api/nodes/moving/move')
    expect(first).toHaveLength(1)
    expect(first[0].body).toEqual({ parent_id: 'group', before_id: null })
    expect(first[0].headers['if-unmodified-since']).toBe(template.updated_at)
    const nestedRevision = data.nodes.find(n => n.id === 'moving')!.updated_at
    await stable.check(async () => {
      await moving.dragTo(destination)
      await expect(moving).toHaveAttribute('aria-level', '1')
    })
    stable.done()
    const moves = calls.filter(c => c.method === 'POST' && c.path === '/api/nodes/moving/move')
    expect(moves).toHaveLength(2)
    expect(moves[1].body).toEqual({ parent_id: 'p-pharos', before_id: null })
    expect(moves[1].headers['if-unmodified-since']).toBe(nestedRevision)
    expect(data.nodes.find(n => n.id === 'moving')!.parent_id).toBe('p-pharos')
    await expect(moving).toHaveCount(1)
    await expect(tree.getByText('No epic', { exact: true })).toHaveCount(0)
    // A root item already at this destination must not issue another write.
    await moving.dragTo(destination)
    expect(calls.filter(c => c.method === 'POST' && c.path === '/api/nodes/moving/move')).toHaveLength(2)
    expect(errors).toEqual([])
    await page.screenshot({ path: `test-results/aeon-655-wn-fix3/root-drop-${width}-${theme}.png`, fullPage: true })
  })
}
