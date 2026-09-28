// SPDX-License-Identifier: AGPL-3.0-only
// Coordinator: npx playwright test -c playwright.ui.config.ts tests/indicator-gallery.spec.ts --workers=2
import { expect, test, type Page } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
import { resolve } from 'node:path'

const newRobots = '.robot-2, .robot-3, .robot-4'
async function gallery(page: Page) {
  await page.route('**/api/**', route => route.fulfill({ json: { value: { style: 'calm', hovering: false } } }))
  await page.setViewportSize({ width: 1200, height: 950 })
  await page.goto('/tests/indicator-gallery.html')
  await expect(page.locator('.sample')).toHaveCount(15)
}

for (const theme of ['light', 'dark'] as const) {
  test(`robot family gallery ${theme}`, async ({ page }) => {
    await gallery(page)
    await page.getByRole('button', { name: theme === 'light' ? 'Light' : 'Dark', exact: true }).click()
    for (const variant of [2, 3, 4]) {
      for (const state of ['working', 'waiting', 'stale']) {
        const cell = page.locator(`[data-variant="${variant}"][data-state="${state}"]`)
        for (const size of [26, 64]) {
          const svg = cell.locator(`[data-size="${size}"] > svg`)
          await expect(svg).toHaveAttribute('aria-hidden', 'true')
          await expect(svg).toHaveAttribute('focusable', 'false')
          expect(await svg.boundingBox()).toMatchObject({ width: size, height: size })
          await expect(svg.locator('.clock')).toHaveCount(state === 'waiting' ? 1 : 0)
          await expect(svg.locator('.glint')).toHaveCount(0)
        }
      }
    }
    const directory = resolve('..', '.agent-shots')
    await mkdir(directory, { recursive: true })
    await page.screenshot({ path: resolve(directory, `iv2-robots-${theme}.png`), fullPage: true })
  })
}

test('events glint once, restart on new evidence, and never replay on mount or state changes', async ({ page }) => {
  await gallery(page)
  const working = page.locator('[data-variant="3"][data-state="working"] [data-size="26"] > svg')
  await expect(working.locator('.glint')).toHaveCount(0)
  await page.getByRole('button', { name: 'Send event', exact: true }).click()
  await expect(working.locator('.glint')).toHaveCSS('fill', 'rgb(201, 162, 74)')
  const first = await working.locator('.glint').elementHandle()
  await page.getByRole('button', { name: 'Send event', exact: true }).click()
  expect(await first!.evaluate(node => node.isConnected)).toBe(false)
  await expect(page.locator('.stale .glint')).toHaveCount(0)
  await expect(working.locator('.glint')).toHaveCount(0, { timeout: 1500 })
  await page.getByRole('button', { name: 'Reset counter', exact: true }).click()
  await expect(working.locator('.glint')).toHaveCount(0)
  await page.getByRole('button', { name: 'Send event', exact: true }).click()
  await expect(working.locator('.glint')).toHaveCount(1)
  await page.getByRole('button', { name: 'Set stale', exact: true }).click()
  await expect(page.locator('.size-sweep .glint')).toHaveCount(0)
  await page.getByRole('button', { name: 'Set waiting', exact: true }).click()
  await expect(page.locator('.size-sweep .glint')).toHaveCount(0)
})

test('motion belongs to working leads, with quiet followers and stationary waiting/stale states', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'no-preference' })
  await gallery(page)
  for (const variant of [2, 3, 4]) {
    const cell = page.locator(`[data-variant="${variant}"][data-state="working"]`)
    const count = (selector: string) => cell.locator(selector).evaluate(node => node.getAnimations({ subtree: true }).filter(a => a.effect?.getTiming().iterations === Infinity).length)
    const full = await count('[data-size="26"] > svg')
    const quiet = await count('[data-quiet] > svg')
    expect(full).toBeGreaterThan(quiet)
    // Robot 2's mouth is a still smile, so a quiet follower has no loop.
    expect(quiet).toBe(variant === 2 ? 0 : 1)
    for (const state of ['waiting', 'stale']) {
      expect(await page.locator(`[data-variant="${variant}"][data-state="${state}"] > .sizes svg`).evaluateAll(nodes => nodes.flatMap(node => node.getAnimations({ subtree: true })).length)).toBe(0)
    }
  }
  const phases = await page.locator('.state-row[aria-label="working"] .sizes [data-size="26"] > svg.robot-indicator').evaluateAll(nodes => nodes.map(node => (node as SVGElement).style.getPropertyValue('--phase')))
  expect(new Set(phases).size).toBe(3)
  await page.getByRole('button', { name: 'Set waiting', exact: true }).click()
  expect(await page.locator('.size-sweep').evaluate(node => node.getAnimations({ subtree: true }).length)).toBe(0)
})

test('reduced motion has no loops and event feedback changes only opacity', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await gallery(page)
  expect(await page.locator(newRobots).evaluateAll(nodes => nodes.flatMap(node => node.getAnimations({ subtree: true })).length)).toBe(0)
  await page.getByRole('button', { name: 'Send event', exact: true }).click()
  const glint = page.locator('.robot-4.working .glint').first()
  await expect(glint).toHaveCSS('transform', 'none')
  await expect(glint).toHaveCSS('animation-duration', '0.6s')
  expect(await page.locator('.robot-4 .pleased').first().evaluate(node => getComputedStyle(node).opacity)).toBe('0')
  await expect(glint).toHaveCount(0, { timeout: 1500 })
})

test('working motion and event glint visual beats', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'no-preference' })
  await gallery(page)
  const directory = resolve('..', '.agent-shots')
  await mkdir(directory, { recursive: true })
  // Freeze actual CSS animations at two points to inspect hand alignment and
  // glances without timing-dependent captures on a busy shared workstation.
  for (const time of [0, 450]) {
    await page.locator(newRobots).evaluateAll((nodes, at) => {
      for (const node of nodes) for (const animation of node.getAnimations({ subtree: true })) {
        animation.pause()
        const delay = Number(animation.effect?.getTiming().delay ?? 0)
        animation.currentTime = at - delay
      }
    }, time)
    await page.screenshot({ path: resolve(directory, `iv2-motion-${time}.png`), fullPage: true })
  }
  for (const theme of ['Light', 'Dark']) {
    await page.getByRole('button', { name: theme, exact: true }).click()
    await page.getByRole('button', { name: 'Send event', exact: true }).click()
    await page.locator(newRobots).evaluateAll(nodes => {
      for (const node of nodes) for (const animation of node.getAnimations({ subtree: true })) {
        if (animation.effect?.getTiming().iterations === 1) {
          animation.pause()
          animation.currentTime = 180
        }
      }
    })
    await page.screenshot({ path: resolve(directory, `iv2-event-${theme.toLowerCase()}.png`), fullPage: true })
  }
})
