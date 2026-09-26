// SPDX-License-Identifier: AGPL-3.0-only
import { inflateSync } from 'node:zlib'
import { mkdirSync } from 'node:fs'
import { test, expect, type Page } from '@playwright/test'
import { mockTicketGraph, ticketGraphWorld } from './ticket-graph-fixtures'
import type { TicketGraphLink } from '../src/lib/ticketGraph'

test.use({
  viewport: { width: 1600, height: 1000 },
  reducedMotion: 'no-preference',
  launchOptions: { args: ['--use-gl=angle', '--use-angle=swiftshader', '--enable-unsafe-swiftshader'] },
})
test.setTimeout(60_000)

const surface = (page: Page) => page.locator('.header-glimpse-canvas')
const ready = (page: Page) => expect(surface(page)).toHaveAttribute('data-ready', 'true', { timeout: 20_000 })

function sized(count: number, withLinks: boolean) {
  const world = ticketGraphWorld()
  world.graph.nodes = world.graph.nodes.slice(0, count)
  const ids = new Set(world.graph.nodes.map(node => node.id))
  const linked = world.graph.links.filter(link => ids.has(link.source) && ids.has(link.target))
  const links: TicketGraphLink[] = withLinks ? (linked.length ? linked : [{ source: world.graph.nodes[0].id, target: world.graph.nodes[1].id, kind: 'relates' }]) : []
  world.graph.links = links
  return world
}

test('a wide project with tickets and a link shows a labelless 60fps glimpse', async ({ page }) => {
  await mockTicketGraph(page)
  await page.goto('/p/PHAROS/tickets')
  await ready(page)
  await expect(surface(page)).toHaveAttribute('data-fps', '60')
  await expect(surface(page)).toHaveAttribute('data-labels', 'off')
  await expect(surface(page)).toHaveAttribute('data-glimpse', '')
  await expect(page.locator('.glimpse-col .graph-label')).toHaveCount(0)
  const layer = page.locator('.glimpse-canvas')
  await expect(layer).toHaveCSS('opacity', '0.35')
  const mask = await layer.evaluate(el => getComputedStyle(el).maskImage || getComputedStyle(el).webkitMaskImage)
  expect(mask).toContain('gradient')
  const hit = await page.evaluate(() => {
    const title = document.getElementById('project-title')!
    const rect = title.getBoundingClientRect()
    return document.elementFromPoint(rect.left + 12, rect.top + rect.height / 2)?.id ?? ''
  })
  expect(hit).toBe('project-title')
})

test('hover shows Open graph and Pause; clicking the glimpse opens the graph', async ({ page }) => {
  await mockTicketGraph(page)
  await page.goto('/p/PHAROS/tickets')
  await ready(page)
  const column = page.locator('[data-header-glimpse="on"]')
  const controls = page.locator('.glimpse-controls')
  await expect.poll(() => controls.evaluate(el => getComputedStyle(el).opacity)).toBe('0')
  await column.hover()
  await expect.poll(() => controls.evaluate(el => getComputedStyle(el).opacity)).toBe('1')
  await expect(page.getByRole('button', { name: 'Open graph', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Pause motion', exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Pause motion', exact: true }).click()
  await expect(page).not.toHaveURL(/view=graph/)
  await expect(surface(page)).toHaveAttribute('data-motion', 'still')
  await expect(page.getByRole('button', { name: 'Resume motion', exact: true })).toBeVisible()
  const box = (await column.boundingBox())!
  await page.mouse.click(box.x + box.width / 2, box.y + 10)
  await expect(page).toHaveURL('/p/PHAROS/tickets?view=graph')
  await expect(page.locator('[data-header-glimpse="on"]')).toHaveCount(0)
})

test('the glimpse waits until the page is idle', async ({ page }) => {
  await page.addInitScript(() => {
    const pending: IdleRequestCallback[] = []
    window.requestIdleCallback = (cb: IdleRequestCallback) => { pending.push(cb); return pending.length }
    window.cancelIdleCallback = () => {}
    ;(window as unknown as { __releaseIdle: () => void }).__releaseIdle = () => {
      for (const cb of pending.splice(0)) cb({ didTimeout: false, timeRemaining: () => 30 } as IdleDeadline)
    }
  })
  await mockTicketGraph(page)
  await page.goto('/p/PHAROS/tickets')
  await expect(page.getByRole('heading', { name: 'Pharos', exact: true })).toBeVisible()
  await expect(surface(page)).toHaveCount(0)
  await page.evaluate(() => (window as unknown as { __releaseIdle: () => void }).__releaseIdle())
  await ready(page)
})

test('narrow viewports and small or unlinked graphs stay empty', async ({ page }) => {
  await mockTicketGraph(page, sized(7, true))
  await page.goto('/p/PHAROS/tickets')
  await expect(page.getByRole('heading', { name: 'Pharos', exact: true })).toBeVisible()
  await expect.poll(() => page.locator('[data-header-glimpse]').getAttribute('data-header-glimpse')).toBe('off')
  await expect(surface(page)).toHaveCount(0)
})

test('eight tickets without a link stay empty', async ({ page }) => {
  const { calls } = await mockTicketGraph(page, sized(8, false))
  await page.goto('/p/PHAROS/tickets')
  await expect.poll(() => calls.length).toBeGreaterThan(0)
  await page.waitForTimeout(400)
  await expect(page.locator('[data-header-glimpse="on"]')).toHaveCount(0)
})

test('eight linked tickets appear once the viewport reaches 1280', async ({ page }) => {
  await page.setViewportSize({ width: 1279, height: 900 })
  await mockTicketGraph(page, sized(8, true))
  await page.goto('/p/PHAROS/tickets')
  await expect(page.locator('[data-header-glimpse="off"]')).toBeAttached()
  await expect(surface(page)).toHaveCount(0)
  await page.setViewportSize({ width: 1280, height: 900 })
  await ready(page)
  await page.setViewportSize({ width: 390, height: 844 })
  await expect(page.locator('[data-header-glimpse="on"]')).toHaveCount(0)
})

test.describe('reduced motion', () => {
  test.use({ reducedMotion: 'reduce' })
  test('shows nothing', async ({ page }) => {
    await mockTicketGraph(page)
    await page.goto('/p/PHAROS/tickets')
    await expect(page.locator('[data-header-glimpse="off"]')).toBeAttached()
    await page.waitForTimeout(1600)
    await expect(surface(page)).toHaveCount(0)
  })
})

test('the Display switch hides the glimpse and is remembered', async ({ page }) => {
  const world = await mockTicketGraph(page)
  await page.goto('/p/PHAROS/tickets')
  await ready(page)
  await page.getByRole('button', { name: /^Display:/ }).click()
  const toggle = page.getByRole('checkbox', { name: 'Graph in project header' })
  await expect(toggle).toBeChecked()
  await toggle.uncheck()
  await expect(page.locator('[data-header-glimpse="on"]')).toHaveCount(0)
  await expect.poll(() => (world.work.preferences['list:display'] as { headerGraph?: boolean } | undefined)?.headerGraph).toBe(false)
  await page.reload()
  await expect(page.getByRole('heading', { name: 'Pharos', exact: true })).toBeVisible()
  await expect(page.locator('[data-header-glimpse="on"]')).toHaveCount(0)
  await page.getByRole('button', { name: /^Display:/ }).click()
  await page.getByRole('checkbox', { name: 'Graph in project header' }).check()
  await ready(page)
})

test('leaving the project releases the WebGL context', async ({ page }) => {
  await page.addInitScript(() => {
    const live = new Set<WebGLRenderingContext | WebGL2RenderingContext>()
    ;(window as unknown as { __aeonLiveGL: typeof live }).__aeonLiveGL = live
    const proto = HTMLCanvasElement.prototype
    const original = proto.getContext
    proto.getContext = function (this: HTMLCanvasElement, type: string, attributes?: CanvasRenderingContext2DSettings) {
      const ctx = original.call(this, type, attributes)
      if (ctx && (type === 'webgl' || type === 'webgl2' || type === 'experimental-webgl')) live.add(ctx as WebGLRenderingContext)
      return ctx
    } as typeof proto.getContext
  })
  await mockTicketGraph(page)
  await page.goto('/p/PHAROS/tickets')
  await ready(page)
  await expect.poll(() => page.evaluate(() => (window as unknown as { __aeonLiveGL: Set<unknown> }).__aeonLiveGL.size)).toBeGreaterThan(0)
  await page.getByRole('link', { name: 'PAIMOS AEON home' }).click()
  await expect(page).toHaveURL('/')
  await expect(page.locator('[data-header-glimpse="on"] canvas')).toHaveCount(0)
  await expect.poll(() => page.evaluate(() => [...(window as unknown as { __aeonLiveGL: Set<WebGLRenderingContext> }).__aeonLiveGL].filter(gl => !gl.isContextLost()).length)).toBe(0)
})

function pngPixels(buf: Buffer) {
  let offset = 8, width = 0, height = 0
  const idat: Buffer[] = []
  while (offset + 8 < buf.length) {
    const len = buf.readUInt32BE(offset)
    const type = buf.toString('ascii', offset + 4, offset + 8)
    const data = buf.subarray(offset + 8, offset + 8 + len)
    if (type === 'IHDR') { width = data.readUInt32BE(0); height = data.readUInt32BE(4) }
    else if (type === 'IDAT') idat.push(data)
    else if (type === 'IEND') break
    offset += 12 + len
  }
  const raw = inflateSync(Buffer.concat(idat))
  const stride = width * 4
  const out = new Uint8Array(width * height * 4)
  const paeth = (a: number, b: number, c: number) => {
    const p = a + b - c, pa = Math.abs(p - a), pb = Math.abs(p - b), pc = Math.abs(p - c)
    return pa <= pb && pa <= pc ? a : pb <= pc ? b : c
  }
  let src = 0
  let prev = new Uint8Array(stride)
  for (let y = 0; y < height; y++) {
    const filter = raw[src++]
    const row = raw.subarray(src, src + stride)
    src += stride
    const cur = new Uint8Array(stride)
    for (let i = 0; i < stride; i++) {
      const left = i >= 4 ? cur[i - 4] : 0, up = prev[i], ul = i >= 4 ? prev[i - 4] : 0, v = row[i]
      cur[i] = filter === 0 ? v : filter === 1 ? (v + left) & 255 : filter === 2 ? (v + up) & 255 : filter === 3 ? (v + ((left + up) >> 1)) & 255 : (v + paeth(left, up, ul)) & 255
    }
    out.set(cur, y * stride)
    prev = cur
  }
  return { width, height, data: out }
}
function contrast(a: [number, number, number], b: [number, number, number]) {
  const lin = (c: number) => { const s = c / 255; return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4 }
  const L = (rgb: [number, number, number]) => 0.2126 * lin(rgb[0]) + 0.7152 * lin(rgb[1]) + 0.0722 * lin(rgb[2])
  const [hi, lo] = [L(a), L(b)].sort((x, y) => y - x)
  return (hi + 0.05) / (lo + 0.05)
}
async function textContrast(page: Page, selector: string) {
  const box = (await page.locator(selector).first().boundingBox())!
  const shot = await page.screenshot({ clip: { x: box.x, y: box.y, width: Math.max(8, box.width), height: Math.max(8, box.height) } })
  const { width, height, data } = pngPixels(shot)
  let dark = 255, light = 0
  let darkPx: [number, number, number] = [0, 0, 0], lightPx: [number, number, number] = [255, 255, 255]
  const lum = (r: number, g: number, b: number) => 0.2126 * r + 0.7152 * g + 0.0722 * b
  for (let i = 0; i < width * height; i++) {
    const px: [number, number, number] = [data[i * 4], data[i * 4 + 1], data[i * 4 + 2]]
    const value = lum(px[0], px[1], px[2])
    if (value < dark) { dark = value; darkPx = px }
    if (value > light) { light = value; lightPx = px }
  }
  return contrast(darkPx, lightPx)
}

for (const scheme of ['light', 'dark'] as const) {
  test.describe(`header shots ${scheme}`, () => {
    test.use({ colorScheme: scheme })
    test(`keeps AA contrast at 1600, 1280 and 390`, async ({ page }) => {
      await mockTicketGraph(page)
      const dir = 'test-results/hg1-header'
      mkdirSync(dir, { recursive: true })
      for (const width of [1600, 1280, 390]) {
        await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
        await page.goto('/p/PHAROS/tickets')
        if (width >= 1280) await ready(page)
        else await expect(page.getByRole('heading', { name: 'Pharos', exact: true })).toBeVisible()
        await page.waitForTimeout(width >= 1280 ? 3200 : 200)
        const header = page.locator('.project-head')
        await header.screenshot({ path: `${dir}/header-${width}-${scheme}.png` })
        await page.screenshot({ path: `${dir}/page-${width}-${scheme}.png` })
        for (const selector of ['#project-title', '.description', '.stat b']) {
          expect(await textContrast(page, selector), `${selector} ${width} ${scheme}`).toBeGreaterThanOrEqual(4.5)
        }
      }
    })
  })
}
