// SPDX-License-Identifier: AGPL-3.0-only
import { readFileSync } from 'node:fs'
import { expect, test, type Page } from '@playwright/test'

const tokens = readFileSync(new URL('../src/styles/tokens.css', import.meta.url), 'utf8')
const base = readFileSync(new URL('../src/styles/base.css', import.meta.url), 'utf8').replace(/@font-face\s*\{[^}]*\}/g, '')
const lightbox = readFileSync(new URL('../src/components/work/AttachmentLightbox.vue', import.meta.url), 'utf8')
const agents = readFileSync(new URL('../src/components/settings/ThemeAgentsCard.vue', import.meta.url), 'utf8')

function lightboxBackground() {
  const rule = lightbox.match(/\.lightbox\s*\{([^}]*)\}/)
  if (!rule) throw new Error('lightbox rule missing')
  const background = rule[1].match(/(?:^|;)\s*background:\s*([^;]+);/)
  if (!background) throw new Error('lightbox background missing')
  return background[1].trim()
}

function agentsStyle() {
  const style = agents.match(/<style scoped>([\s\S]*?)<\/style>/)
  if (!style) throw new Error('agents card style missing')
  return style[1]
}

function pageCss() {
  return `${tokens}\n${base}\n${agentsStyle()}\n#stage { background: ${lightboxBackground()}; width: 240px; height: 160px; }`
}

async function mount(page: Page) {
  await page.setContent(`<!DOCTYPE html><style>${pageCss()}</style>
    <label class="switch" id="card"><input type="checkbox" role="switch" checked><span>On</span></label>
    <label class="switch"><input id="plain" type="checkbox" role="switch" checked></label>
    <label class="switch"><input id="wide" type="checkbox" role="switch" checked style="width:52px;height:28px"></label>
    <label class="switch"><input id="off" type="checkbox" role="switch" style="width:52px;height:28px"></label>
    <label class="switch"><input id="mixed" type="checkbox" role="switch" style="width:52px;height:28px"></label>
    <div id="stage"></div>`)
  await page.locator('#mixed').evaluate((el: HTMLInputElement) => { el.indeterminate = true })
}

async function knob(page: Page, selector: string) {
  return page.locator(selector).evaluate(el => {
    const track = el.getBoundingClientRect()
    const after = getComputedStyle(el, '::after')
    const knobW = parseFloat(after.width)
    const knobH = parseFloat(after.height)
    const matrix = new DOMMatrixReadOnly(after.transform)
    const x = parseFloat(after.left) + matrix.m41
    const y = parseFloat(after.top) + matrix.m42
    return {
      trackW: track.width, trackH: track.height, knobW, knobH,
      leftGap: x, rightGap: track.width - x - knobW,
      topGap: y, bottomGap: track.height - y - knobH,
      shift: matrix.m41,
    }
  })
}

function expectInset(measured: Awaited<ReturnType<typeof knob>>, edge: 'leftGap' | 'rightGap', label: string) {
  expect(measured.knobW, `${label} knob is square ${JSON.stringify(measured)}`).toBeCloseTo(measured.knobH, 1)
  expect(measured.knobH, `${label} knob height`).toBeCloseTo(measured.trackH - 4, 1)
  expect(measured.topGap, `${label} top inset`).toBeCloseTo(2, 1)
  expect(measured.bottomGap, `${label} bottom inset`).toBeCloseTo(2, 1)
  expect(measured[edge], `${label} ${edge} ${JSON.stringify(measured)}`).toBeCloseTo(2, 1)
}

test('the shared switch centres its knob on any track and the agents label keeps a 44px hit target', async ({ page }) => {
  await mount(page)
  const card = page.locator('#card')
  const cardBox = await card.boundingBox()
  const cardInput = await card.locator('input').boundingBox()
  expect(cardBox!.width, 'label hit width').toBeGreaterThanOrEqual(44)
  expect(cardBox!.height, 'label hit height').toBeGreaterThanOrEqual(44)
  expect(cardInput!.width, 'agents track uses the shared width').toBeCloseTo(34, 0)
  expect(cardInput!.height, 'agents track uses the shared height').toBeCloseTo(20, 0)

  const plain = await knob(page, '#plain')
  expectInset(plain, 'rightGap', 'default on')
  expect(plain.shift, 'default travel stays 14px so mixed and on reads keep their matrix').toBeCloseTo(14, 1)

  const wide = await knob(page, '#wide')
  expectInset(wide, 'rightGap', 'wide on')
  expect(wide.trackW).toBeCloseTo(52, 0)
  expect(wide.trackH).toBeCloseTo(28, 0)

  const off = await knob(page, '#off')
  expectInset(off, 'leftGap', 'wide off')

  const mixed = await knob(page, '#mixed')
  expect(mixed.knobW, `mixed knob ${JSON.stringify(mixed)}`).toBeCloseTo(mixed.knobH, 1)
  expect(Math.abs(mixed.leftGap - mixed.rightGap), `mixed knob centred ${JSON.stringify(mixed)}`).toBeLessThanOrEqual(0.5)
})

function channels(color: string) {
  const srgb = color.match(/color\(\s*srgb\s+([\d.]+)\s+([\d.]+)\s+([\d.]+)(?:\s*\/\s*([\d.]+))?\s*\)/)
  const rgb = color.match(/rgba?\(\s*([\d.]+)[,\s]+([\d.]+)[,\s]+([\d.]+)(?:\s*[,/]\s*([\d.]+))?\s*\)/)
  const match = srgb ?? rgb
  if (!match) return { color, r: Number.NaN, g: Number.NaN, b: Number.NaN, a: Number.NaN, lum: Number.NaN }
  const scale = srgb ? 255 : 1
  const r = Number(match[1]) * scale, g = Number(match[2]) * scale, b = Number(match[3]) * scale
  const a = match[4] === undefined ? 1 : Number(match[4])
  return { color, r, g, b, a, lum: 0.2126 * r + 0.7152 * g + 0.0722 * b }
}

function gradientStops(image: string) {
  const stops: Array<[number, number, number]> = []
  for (const match of image.matchAll(/color\(\s*srgb\s+([\d.]+)\s+([\d.]+)\s+([\d.]+)/g))
    stops.push([Number(match[1]) * 255, Number(match[2]) * 255, Number(match[3]) * 255])
  for (const match of image.matchAll(/rgba?\(\s*([\d.]+)[,\s]+([\d.]+)[,\s]+([\d.]+)/g))
    stops.push([Number(match[1]), Number(match[2]), Number(match[3])])
  return stops
}

async function stagePaint(page: Page) {
  const raw = await page.evaluate(() => {
    const read = (name: string) => {
      const probe = document.createElement('div')
      probe.style.backgroundColor = `var(${name})`
      document.body.append(probe)
      const color = getComputedStyle(probe).backgroundColor
      probe.remove()
      return color
    }
    return {
      image: getComputedStyle(document.getElementById('stage')!).backgroundImage,
      canvas: read('--canvas'),
      lo: read('--canvas-lo'),
    }
  })
  return { image: raw.image, canvas: channels(raw.canvas), lo: channels(raw.lo) }
}

test('the attachment stage keeps a radial wash in every theme even if canvas-lo is missing', async ({ page }) => {
  const themes = [
    { theme: 'light' as const, scheme: 'light' as const, attribute: 'light' },
    { theme: 'dark' as const, scheme: 'dark' as const, attribute: 'dark' },
    { theme: 'system-dark' as const, scheme: 'dark' as const, attribute: '' },
  ]
  for (const theme of themes) {
    await page.emulateMedia({ colorScheme: theme.scheme, reducedMotion: 'reduce' })
    await mount(page)
    await page.evaluate(attribute => {
      if (attribute) document.documentElement.dataset.theme = attribute
      else delete document.documentElement.dataset.theme
    }, theme.attribute)
    const paint = await stagePaint(page)
    expect(paint.image, `${theme.theme} stage ${paint.image}`).toContain('radial-gradient')
    expect(paint.lo!.a, `${theme.theme} canvas-lo ${paint.lo!.color}`).toBe(1)
    expect(paint.canvas!.a, `${theme.theme} canvas ${paint.canvas!.color}`).toBe(1)
    expect(paint.lo!.lum, `${theme.theme} canvas-lo is darker than canvas`).toBeLessThan(paint.canvas!.lum - 1)

    await page.locator('body').evaluate(() => {
      const kill = document.createElement('style')
      // Match every selector that defines --canvas-lo. A bare :root loses to
      // :root[data-theme="light"] and the dark media-query rule.
      kill.textContent = `
        :root,
        :root[data-theme="light"],
        :root[data-theme="dark"],
        :root:not([data-theme="light"]),
        .agent-theme-preview,
        .agent-theme-preview.light,
        .agent-theme-preview.dark { --canvas-lo: initial; }
        @media (prefers-color-scheme: dark) {
          :root:not([data-theme="light"]) { --canvas-lo: initial; }
        }`
      document.body.append(kill)
    })
    const fallback = await stagePaint(page)
    expect(fallback.lo!.a, `${theme.theme} invalid canvas-lo must be observable (${fallback.lo!.color})`).toBe(0)
    expect(fallback.image, `${theme.theme} missing canvas-lo must not drop the stage`).toContain('radial-gradient')
    const usesCanvas = gradientStops(fallback.image).some(stop =>
      Math.abs(stop[0] - fallback.canvas!.r) < 1.5 && Math.abs(stop[1] - fallback.canvas!.g) < 1.5 && Math.abs(stop[2] - fallback.canvas!.b) < 1.5)
    expect(usesCanvas, `${theme.theme} fallback uses canvas ${fallback.image}`).toBe(true)
  }
})
