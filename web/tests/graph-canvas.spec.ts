// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Route } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'

test('the shared canvas consumes a ticket adapter and honors its FPS prop', async ({ page }) => {
  await mockWork(page, fixtures())
  await page.goto('/projects')
  await page.evaluate(async () => { const { mountTicketGraph } = await import('/tests/graph-canvas-harness.ts'); await mountTicketGraph() })
  const graph = page.locator('#ticket-graph-harness .graph-surface')
  await expect(graph).toHaveAttribute('data-ready', 'true', { timeout: 15000 })
  await expect(graph).toHaveAttribute('data-fps', '30')
  await graph.press('ArrowRight'); await graph.press('Enter')
  await expect(page.getByLabel('Opened ticket')).toHaveText('/p/AEON/AEON-194')
  await page.getByRole('button', { name: 'Set parent FPS' }).click()
  await expect(graph).toHaveAttribute('data-fps', '60')
})

test('a glimpse renderer takes its host size when its engine loads after the first resize pass', async ({ page }) => {
  await mockWork(page, fixtures())
  await page.goto('/projects')
  // Barrier: hold the graph engine until the canvas's resize observer has
  // already run with no renderer. Only a measure after start sizes it then.
  let release = false, requested!: () => void
  const held: Route[] = [], seen = new Promise<void>(resolve => { requested = resolve })
  await page.route(/\/node_modules\/\.vite\/deps\/(?:3d-)?force-graph\.js(?:\?|$)/, route => {
    if (release) return route.continue()
    held.push(route); requested()
  })
  await page.evaluate(async () => { const { mountGlimpseGraph } = await import('/tests/graph-canvas-harness.ts'); await mountGlimpseGraph() })
  await seen
  await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))))
  release = true
  for (const route of held) await route.continue()
  const graph = page.locator('#glimpse-graph-harness .graph-surface')
  await expect(graph).toHaveAttribute('data-ready', 'true', { timeout: 15000 })
  const size = () => graph.evaluate(el => {
    const canvas = el.querySelector('canvas')?.getBoundingClientRect()
    return { host: [el.clientWidth, el.clientHeight], canvas: canvas ? [Math.round(canvas.width), Math.round(canvas.height)] : null }
  })
  expect((await size()).host, 'the harness box').toEqual([480, 140])
  await expect.poll(async () => (await size()).canvas, { message: 'canvas takes the host size, not the window size' }).toEqual([480, 140])
})
