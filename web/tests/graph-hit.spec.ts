// SPDX-License-Identifier: AGPL-3.0-only
// AEON-447: the graph test hooks. Settled means one painted frame, pickability is
// answered per node, a resize or camera move asks for a fresh paint, and a click
// reaches the app only through force-graph's own dispatch.
import { test, expect, type Locator, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import type { GraphData, GraphNode } from '../src/lib/graphRenderer'

const dot = (id: string, weight = 0): GraphNode => ({ id, label: id, group: 'g', color: '--teal', weight })
const link = (source: string, target: string) => ({ source, target, kind: 'relates', directed: false })
const chain: GraphData = { nodes: [dot('a'), dot('b'), dot('c')], links: [link('a', 'b'), link('b', 'c')] }

async function mount(page: Page, data: GraphData, width: number, height: number, camera: 'fit' | 'fixed' = 'fit') {
  await mockWork(page, fixtures())
  await page.goto('/projects')
  await page.evaluate(async ([d, w, h, c]) => { const { mountHitGraph } = await import('/tests/graph-hit-harness.ts'); await mountHitGraph(d as GraphData, w as number, h as number, c as 'fit' | 'fixed') }, [data, width, height, camera] as const)
  const host = page.locator('#hit-graph')
  await expect(host).toHaveAttribute('data-settled', 'true', { timeout: 15_000 })
  return host
}
const marker = (host: Locator, id: string) => host.locator(`[data-node-id="${id}"]`)
const events = (page: Page) => page.evaluate(() => window.__hit!.events.slice())
const selects = async (page: Page) => (await events(page)).filter(e => e.startsWith('select:'))
const frames = (page: Page, count = 4) => page.evaluate(n => new Promise<void>(done => { const step = (left: number) => left ? requestAnimationFrame(() => step(left - 1)) : done(); step(n) }), count)

// Move onto a node and wait for force-graph's own hover of it, as a person would.
// The pointer leaves every node first, so the hover is always a fresh one.
async function hover(page: Page, host: Locator, id: string) {
  await expect(host).toHaveAttribute('data-settled', 'true', { timeout: 15_000 })
  const box = (await host.locator('canvas').first().boundingBox())!
  const x = Number(await marker(host, id).getAttribute('data-node-x')), y = Number(await marker(host, id).getAttribute('data-node-y'))
  await page.mouse.move(box.x + 2, box.y + 2)
  await frames(page)
  await page.evaluate(() => { window.__hit!.events.length = 0 })
  await page.mouse.move(box.x + x, box.y + y)
  await expect.poll(() => events(page), { timeout: 10_000 }).toContain(`hover:${id}`)
  return { x: box.x + x, y: box.y + y }
}

test('a covered node does not hold back the settle, and only it is not pickable', async ({ page }) => {
  // The big node is painted last and lies over the small one's centre.
  const host = await mount(page, { nodes: [dot('small'), dot('big', 300)], links: [] }, 640, 420)
  await expect(marker(host, 'big')).toHaveAttribute('data-node-pickable', 'true')
  await expect(marker(host, 'small')).toHaveAttribute('data-node-pickable', 'false')
  // The node that can be picked still selects through the library.
  const at = await hover(page, host, 'big')
  await page.mouse.down(); await page.mouse.up()
  await expect.poll(() => selects(page)).toEqual(['select:big'])
  expect(at.x).toBeGreaterThan(0)
})

test('an offscreen node does not hold back the settle, and only it is not pickable', async ({ page }) => {
  // A fixed camera on a tiny canvas leaves most of a long chain outside it.
  const ids = ['a', 'b', 'c', 'd', 'e', 'f', 'g', 'h']
  const host = await mount(page, { nodes: ids.map(id => dot(id)), links: ids.slice(1).map((id, i) => link(ids[i], id)) }, 160, 100, 'fixed')
  const rows = await host.locator('[data-node-id]').evaluateAll(els => els.map(el => ({ id: (el as HTMLElement).dataset.nodeId!, x: Number((el as HTMLElement).dataset.nodeX), y: Number((el as HTMLElement).dataset.nodeY), pickable: (el as HTMLElement).dataset.nodePickable })))
  const inside = rows.filter(r => r.x > 0 && r.y > 0 && r.x < 160 && r.y < 100)
  const outside = rows.filter(r => !inside.includes(r))
  expect(inside.length).toBeGreaterThan(0)
  expect(outside.length).toBeGreaterThan(0)
  for (const row of outside) expect(row.pickable).toBe('false')
  for (const row of inside) expect(row.pickable).toBe('true')
})

test('a resize asks for a fresh paint, even by one pixel', async ({ page }) => {
  const host = await mount(page, chain, 640, 420)
  const after = await page.evaluate(async () => (await import('/tests/graph-hit-harness.ts')).resizeHitGraph(1, 0))
  // Synchronously after the resize, before any frame could repaint it.
  expect(after.settled).toBe('false')
  expect(after.pickable).toEqual(['false', 'false', 'false'])
  await expect(host).toHaveAttribute('data-settled', 'true', { timeout: 15_000 })
  await hover(page, host, 'b')
  await page.mouse.down(); await page.mouse.up()
  await expect.poll(() => selects(page)).toEqual(['select:b'])
})

test('a camera move asks for a fresh paint', async ({ page }) => {
  const host = await mount(page, chain, 640, 420)
  const after = await page.evaluate(async () => (await import('/tests/graph-hit-harness.ts')).moveHitCamera('a'))
  expect(after.settled).toBe('false')
  expect(after.pickable).toEqual(['false', 'false', 'false'])
  await expect(host).toHaveAttribute('data-settled', 'true', { timeout: 15_000 })
  await hover(page, host, 'a')
  await page.mouse.down(); await page.mouse.up()
  await expect.poll(() => selects(page)).toEqual(['select:a'])
})

test('a press that moves is not a click: nothing but force-graph can select', async ({ page }) => {
  const host = await mount(page, chain, 640, 420)
  const at = await hover(page, host, 'b')
  // force-graph treats a move while the button is down as a drag and drops the click.
  await page.mouse.down(); await page.mouse.move(at.x + 3, at.y); await page.mouse.up()
  await frames(page, 6)
  expect(await selects(page)).toEqual([])
  // The plain click after it still selects exactly once.
  await hover(page, host, 'b')
  await page.mouse.down(); await page.mouse.up()
  await expect.poll(() => selects(page)).toEqual(['select:b'])
  await frames(page, 6)
  expect(await selects(page)).toEqual(['select:b'])
})
