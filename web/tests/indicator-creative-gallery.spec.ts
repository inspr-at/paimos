// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Locator, type Page } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
import { resolve } from 'node:path'

// Coordinator owns the runner. Always use --workers=2 on the shared machine:
// npm run test -- --workers=2 tests/indicator-creative-gallery.spec.ts
const fixture = '/tests/indicator-creative-gallery.html'
const variants = ['Orbit', 'Quill', 'Sprite'] as const
const states = ['working', 'waiting', 'stale'] as const
const shots = resolve(process.cwd(), '../.agent-shots')

async function open(page: Page, theme = 'light') {
  await page.route('**/api/**', route => route.fulfill({ json: {} }))
  await page.goto(`${fixture}?theme=${theme}&pulse=7`)
  // Each sample is the indicator art plus the nested state-mark svg.
  await expect(page.locator('.sample > svg.agent-indicator-art')).toHaveCount(18)
  await expect(page.locator('.sample svg.agent-state-mark')).toHaveCount(18)
}

const probe = (page: Page, name: string) => page.getByTestId(`probe-${name}`).locator('svg.agent-indicator-art')
const animations = (locator: Locator) => locator.evaluate(element => element.getAnimations({ subtree: true })
  .filter(animation => animation.playState === 'running').length)

for (const theme of ['light', 'dark']) {
  test(`${theme}: all creative states at 26 and 64 px`, async ({ page }) => {
    await page.setViewportSize({ width: 1040, height: 980 })
    await page.emulateMedia({ reducedMotion: 'reduce' })
    await open(page, theme)
    await mkdir(shots, { recursive: true })
    await page.locator('#contact-sheet').screenshot({ path: resolve(shots, `iv3-${theme}-gallery.png`) })
    for (const name of variants) for (const state of states) for (const size of [26, 64]) {
      const sample = page.locator(`[data-variant="${name}"] .sample[data-state="${state}"][data-size="${size}"]`)
      const art = sample.locator('svg.agent-indicator-art')
      await expect(art).toHaveAttribute('aria-hidden', 'true')
      await expect(art).toHaveAttribute('focusable', 'false')
      await expect(art).toHaveCSS('width', `${size}px`)
      await expect(art).toHaveCSS('height', `${size}px`)
      // Signal colour is the agent state palette, not the brand teal, amber or backlog grey.
      const colors = await art.evaluate((element, state) => {
        const token = state === 'working' ? '--agent-standard-working' : state === 'waiting' ? '--agent-standard-waiting' : '--agent-standard-inactive'
        return {
          actual: getComputedStyle(element).getPropertyValue('--signal').trim(),
          expected: getComputedStyle(document.documentElement).getPropertyValue(token).trim(),
        }
      }, state)
      expect(colors.actual).toBe(colors.expected)
      await expect(art.locator('.clock')).toHaveCount(state === 'waiting' ? 1 : 0)
      expect(await animations(art)).toBe(0)
      await sample.screenshot({ path: resolve(shots, `iv3-${theme}-${name.toLowerCase()}-${state}-${size}.png`) })
    }
  })
}

test('working loops respect lead, seed, waiting, stale and reduced motion', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'no-preference' })
  await open(page)
  for (const name of variants) {
    expect(await animations(probe(page, name))).toBeGreaterThan(0)
    await expect(probe(page, name)).toHaveCSS('width', '26px')
    await expect(probe(page, name)).toHaveCSS('transform', 'none')
  }
  const firstPhase = await probe(page, 'Orbit').evaluate(el => getComputedStyle(el).getPropertyValue('--phase'))
  await page.getByRole('button', { name: 'Change seed', exact: true }).click()
  const nextPhase = await probe(page, 'Orbit').evaluate(el => getComputedStyle(el).getPropertyValue('--phase'))
  expect(nextPhase).not.toBe(firstPhase)
  expect(Math.abs(parseFloat(nextPhase) - parseFloat(firstPhase))).toBeGreaterThan(.2)
  await page.getByRole('button', { name: 'Toggle lead' }).click()
  for (const name of variants) expect(await animations(probe(page, name))).toBe(0)
  await page.getByRole('button', { name: 'Toggle lead' }).click()
  for (const state of ['waiting', 'stale'] as const) {
    await page.getByRole('button', { name: state, exact: true }).click()
    for (const name of variants) {
      const art = probe(page, name)
      await expect(art).toHaveAttribute('data-state', state)
      expect(await animations(art)).toBe(0)
      await expect(art.locator('.clock')).toHaveCount(state === 'waiting' ? 1 : 0)
    }
  }
  await page.getByRole('button', { name: 'working', exact: true }).click()
  await page.emulateMedia({ reducedMotion: 'reduce' })
  for (const name of variants) expect(await animations(probe(page, name))).toBe(0)
  await expect(page.locator('.glint')).toHaveCount(0)
  for (const size of [18, 72]) {
    await page.getByRole('button', { name: `${size} px`, exact: true }).click()
    for (const name of variants) {
      await expect(probe(page, name)).toHaveCSS('width', `${size}px`)
      await expect(probe(page, name)).toHaveCSS('height', `${size}px`)
    }
  }
})

test('only new event counters glint, including consecutive events; stale cancels', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'no-preference' })
  await open(page)
  const freeze = await page.addStyleTag({ content: '.probes .glint { animation-play-state: paused !important; }' })
  await expect(page.locator('.glint')).toHaveCount(0)
  await page.getByRole('button', { name: 'Repeat counter' }).click()
  await expect(page.locator('.glint')).toHaveCount(0)
  await page.getByRole('button', { name: 'Real event', exact: true }).click()
  for (const name of variants) {
    const glint = probe(page, name).locator('.glint')
    await expect(glint).toHaveCount(1)
    await expect(glint).toHaveCSS('fill', 'rgb(201, 162, 74)')
    await expect(glint).toHaveCSS('animation-duration', '0.6s')
    await expect(glint).toHaveCSS('animation-iteration-count', '1')
  }
  const old = await probe(page, 'Orbit').locator('.glint').elementHandle()
  await page.getByRole('button', { name: 'Real event', exact: true }).click()
  expect(await old!.evaluate(element => element.isConnected)).toBe(false)
  await expect(page.locator('.glint')).toHaveCount(3)
  await freeze.evaluate(element => { element.parentNode?.removeChild(element) })
  await expect(page.locator('.glint')).toHaveCount(0)
  await page.getByRole('button', { name: 'Reset counter' }).click()
  await page.getByRole('button', { name: 'Real event', exact: true }).click()
  await expect(page.locator('.glint')).toHaveCount(0)
  await page.getByRole('button', { name: 'waiting', exact: true }).click()
  // Remount with the reset counter as baseline; remount itself must not flash.
  await page.getByRole('button', { name: 'Remount', exact: true }).click()
  await page.getByRole('button', { name: 'Remount', exact: true }).click()
  await expect(page.locator('.glint')).toHaveCount(0)
  // Waiting remembers the event and does not flash; the flash belongs to working.
  await page.getByRole('button', { name: 'Real event', exact: true }).click()
  await expect(page.locator('.glint')).toHaveCount(0)
  await page.getByRole('button', { name: 'working', exact: true }).click()
  await expect(page.locator('.glint')).toHaveCount(0)
  await page.getByRole('button', { name: 'Real event', exact: true }).click()
  await expect(page.locator('.glint')).toHaveCount(3)
  await page.getByRole('button', { name: 'stale', exact: true }).click()
  await expect(page.locator('.glint')).toHaveCount(0)
  await page.getByRole('button', { name: 'Real event', exact: true }).click()
  await expect(page.locator('.glint')).toHaveCount(0)
  await page.getByRole('button', { name: 'working', exact: true }).click()
  await expect(page.locator('.glint')).toHaveCount(0)
})

test('reduced motion keeps the event glint to opacity only', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await open(page)
  const freeze = await page.addStyleTag({ content: '.probes .glint { animation-play-state: paused !important; }' })
  await page.getByRole('button', { name: 'Real event', exact: true }).click()
  for (const name of variants) {
    const art = probe(page, name)
    const glint = art.locator('.glint')
    await expect(glint).toHaveCount(1)
    const frames = await glint.evaluate(element => element.getAnimations().flatMap(animation =>
      (animation.effect as KeyframeEffect).getKeyframes()))
    expect(frames.length).toBeGreaterThan(0)
    expect(frames.every(frame => !('transform' in frame))).toBe(true)
    expect(await art.evaluate(element => element.getAnimations({ subtree: true })
      .some(animation => animation.effect?.getTiming().iterations === Infinity))).toBe(false)
  }
  await freeze.evaluate(element => { element.parentNode?.removeChild(element) })
  await expect(page.locator('.glint')).toHaveCount(0)
})
