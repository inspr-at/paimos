// SPDX-License-Identifier: AGPL-3.0-only
// IV1 / AEON-184. Coordinator: run with --workers=2 after merging variants.
import { existsSync, mkdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { expect, test, type Locator, type Page } from '@playwright/test'
import { indicatorVariants } from '../src/lib/indicatorVariants'
import { mockIndicator } from './agent-indicator-fixtures'

const available = indicatorVariants.filter(v => existsSync(new URL(`../src/components/indicators/${v.file}`, import.meta.url)))
const missing = indicatorVariants.filter(v => !available.includes(v))
const project = '[data-project-id="p-aeon"]'
async function settings(page: Page) {
  await page.goto('/settings/personal#agents')
  await expect(page.getByRole('radiogroup', { name: 'Agent indicator' })).toBeVisible()
}
const radio = (page: Page, name: string) => page.getByRole('radio', { name, exact: true })
const loops = (locator: Locator) => locator.evaluate(el => el.getAnimations({ subtree: true }).filter(a => a.effect?.getTiming().iterations === Infinity).length)

test('nine icons keep their order; missing files are quiet disabled placeholders', async ({ page }) => {
  await mockIndicator(page)
  await settings(page)
  const tiles = page.getByRole('radiogroup', { name: 'Agent indicator' }).getByRole('radio')
  await expect(tiles).toHaveCount(9)
  expect(await tiles.evaluateAll(elements => elements.map(el => el.getAttribute('data-variant')))).toEqual(indicatorVariants.map(v => v.id))
  await expect(radio(page, 'Robot 1')).toBeChecked()
  for (const option of available) {
    await expect(radio(page, option.name)).toBeEnabled()
    await expect(radio(page, option.name).locator('.live-bot svg').first()).toBeVisible()
  }
  for (const option of missing) {
    await expect(radio(page, option.name)).toBeDisabled()
    await expect(radio(page, option.name)).toContainText('Coming soon')
    await expect(radio(page, option.name).locator('.live-bot')).toHaveCount(0)
  }
  await page.setViewportSize({ width: 375, height: 850 })
  const boxes = await tiles.evaluateAll(elements => elements.slice(0, 3).map(el => { const r = el.getBoundingClientRect(); return { top: r.top, left: r.left, right: r.right } }))
  expect(boxes[0]!.top).toBe(boxes[1]!.top)
  expect(boxes[1]!.top).toBe(boxes[2]!.top)
  expect(boxes[2]!.right).toBeLessThanOrEqual(375)
})

test('arrows browse the wrapping row in order, Enter/Space select, and Tab leaves the group', async ({ page }) => {
  const { data } = await mockIndicator(page)
  await settings(page)
  const first = available[0]!, last = available.at(-1)!
  await radio(page, 'Robot 1').focus()
  await page.keyboard.press('ArrowLeft')
  await expect(radio(page, 'Pulse')).toBeFocused()
  await expect(radio(page, 'Robot 1')).toBeChecked()
  await page.keyboard.press('Space')
  await expect(radio(page, 'Pulse')).toBeChecked()
  await expect.poll(() => data.preferences['agent-indicator']).toEqual({ style: 'pulse', hovering: false })
  await page.keyboard.press('ArrowDown')
  await expect(radio(page, 'Robot 1')).toBeFocused()
  await page.keyboard.press('ArrowUp')
  await expect(radio(page, 'Pulse')).toBeFocused()
  await page.keyboard.press('ArrowLeft')
  await expect(radio(page, last.name)).toBeFocused()
  await page.keyboard.press('ArrowRight')
  await expect(radio(page, first.name)).toBeFocused()
  await page.keyboard.press('End')
  await expect(radio(page, last.name)).toBeFocused()
  await page.keyboard.press('Home')
  await expect(radio(page, first.name)).toBeFocused()
  await page.keyboard.press('ArrowRight')
  await expect(radio(page, 'Robot 1')).toBeFocused()
  await page.keyboard.press('Enter')
  await expect(radio(page, 'Robot 1')).toBeChecked()
  await page.keyboard.press('Tab')
  await expect(page.getByRole('radiogroup', { name: 'Activity ring' }).getByRole('radio', { name: 'Moving' })).toBeFocused()
})

for (const option of available) {
  test(`${option.name}: persists and follows cards, rows, popovers and /agents`, async ({ page }) => {
    await page.emulateMedia({ reducedMotion: 'no-preference' })
    const { data } = await mockIndicator(page)
    await settings(page)
    await radio(page, option.name).click()
    await page.getByRole('switch', { name: 'Hovering' }).check()
    await expect.poll(() => data.preferences['agent-indicator']).toEqual({ style: option.id, hovering: true })
    await page.reload()
    await expect(radio(page, option.name)).toBeChecked()
    await expect(page.getByRole('switch', { name: 'Hovering' })).toBeChecked()
    await page.goto('/')
    for (const view of ['Cards', 'List']) {
      await page.getByRole('radio', { name: `${view} view`, exact: true }).click()
      const chip = page.locator(`${project} .live-chip`), bot = chip.locator('.live-bot').first()
      await expect(chip.locator('.live-bot')).toHaveCount(2)
      await expect(bot).toHaveAttribute('data-style', option.id)
      const hoverArt = option.id === 'robot-5' ? '.bob' : option.id === 'robot-1' ? '.robot' : '.indicator-art'
      await expect(bot.locator(hoverArt)).not.toHaveCSS('animation-name', 'none')
      if (!['robot-1', 'robot-5'].includes(option.id)) await expect(chip.locator('.live-bot').nth(1).locator(hoverArt)).toHaveCSS('animation-name', 'none')
      await expect(bot.locator('svg').first()).toBeVisible()
      await chip.hover()
      const pop = page.getByRole('dialog', { name: 'Active agents on Aeon' })
      await expect(pop).toBeVisible()
      await expect(pop.locator('.live-bot').first()).toHaveAttribute('data-style', option.id)
      await page.keyboard.press('Escape')
      await page.mouse.move(0, 0)
    }
    await page.goto('/agents')
    for (const state of ['working', 'waiting', 'problem']) {
      const bot = page.locator(`.agents-page .live-bot[data-state="${state}"]`).first()
      await expect(bot).toHaveAttribute('data-style', option.id)
      await expect(bot.locator('svg').first()).toBeVisible()
      if (state !== 'working') {
        await expect(bot.locator('.indicator-art')).toHaveCSS('animation-name', 'none')
        expect(await loops(bot)).toBe(0)
      }
    }
    await page.emulateMedia({ reducedMotion: 'reduce' })
    expect(await loops(page.locator('.agents-page .live-bot[data-state="working"]').first())).toBe(0)
    await settings(page)
    await page.getByRole('switch', { name: 'Hovering' }).uncheck()
    await expect.poll(() => data.preferences['agent-indicator']).toEqual({ style: option.id, hovering: false })
    await page.emulateMedia({ reducedMotion: 'no-preference' })
    await page.goto('/')
    const stationary = page.locator(`${project} .live-chip .live-bot`).first()
    await expect(stationary.locator('.indicator-art')).toHaveCSS('animation-name', 'none')
    await expect.poll(() => loops(stationary)).toBeGreaterThan(0)
  })
}

for (const [old, current] of [['calm', 'robot-1'], ['playful', 'robot-5']] as const) {
  test(`legacy ${old} migrates without losing Hovering`, async ({ page }) => {
    const { data } = await mockIndicator(page)
    data.preferences['agent-indicator'] = { style: old, hovering: true }
    await settings(page)
    await expect(page.locator(`[data-variant="${current}"]`)).toBeChecked()
    await page.getByRole('switch', { name: 'Hovering' }).uncheck()
    await expect.poll(() => data.preferences['agent-indicator']).toEqual({ style: current, hovering: false })
  })
}

if (missing.length) test('saved unavailable choice falls back without overwriting the account', async ({ page }) => {
  const { data } = await mockIndicator(page)
  data.preferences['agent-indicator'] = { style: missing[0]!.id, hovering: false }
  await page.goto('/')
  await expect(page.locator(`${project} .live-bot`).first()).toHaveAttribute('data-style', 'robot-1')
  expect(data.preferences['agent-indicator']).toEqual({ style: missing[0]!.id, hovering: false })
})

test('failed saves are visible and can be retried', async ({ page }) => {
  const { data } = await mockIndicator(page)
  let fail = true
  await page.route('**/api/preferences/agent-indicator', route => route.request().method() === 'PUT' && fail ? route.fulfill({ status: 503, json: { error: 'unavailable' } }) : route.fallback())
  await settings(page)
  await radio(page, 'Robot 5').click()
  await expect(page.getByRole('alert')).toContainText('agent indicator setting could not be saved')
  fail = false
  await page.getByRole('button', { name: 'Try again', exact: true }).click()
  await expect.poll(() => data.preferences['agent-indicator']).toEqual({ style: 'robot-5', hovering: false })
  await expect(page.locator('.save-error')).toHaveCount(0)
})

test('IV1 renderers: event-only glints, paused states, quiet stacks and reduced motion', async ({ page }) => {
  await page.clock.install()
  await page.emulateMedia({ reducedMotion: 'no-preference' })
  await page.goto('/tests/indicator-harness.html')
  const sheet = page.locator('.sheet')
  await expect(sheet.locator('.indicator')).toHaveCount(18)
  await expect(sheet.locator('.glint')).toHaveCount(0) // mounted with pulse=7
  for (const state of ['waiting', 'stale']) expect(await loops(sheet.locator(`[data-state="${state}"]`).first())).toBe(0)
  const working = sheet.locator('[data-state="working"][data-size="26"]')
  const full = await Promise.all([0, 1, 2].map(i => loops(working.nth(i))))
  await page.getByRole('checkbox', { name: 'Lead animation' }).uncheck()
  const quiet = await Promise.all([0, 1, 2].map(i => loops(working.nth(i))))
  expect(quiet.every((count, i) => count <= full[i]!)).toBe(true)
  expect(quiet[2]).toBeLessThan(full[2]!)
  await page.getByRole('button', { name: 'Real event' }).click()
  await expect(sheet.locator('.glint')).toHaveCount(6)
  await expect(sheet.locator('[data-state="stale"] .glint')).toHaveCount(0)
  await page.clock.fastForward(650)
  await expect(sheet.locator('.glint')).toHaveCount(0)
  await page.getByRole('button', { name: 'Real event' }).click()
  await expect(sheet.locator('.glint')).toHaveCount(6)
  await page.emulateMedia({ reducedMotion: 'reduce' })
  expect(await loops(sheet)).toBe(0)
  await expect(sheet.locator('.glint').first()).toHaveCSS('animation-name', /event-opacity/)
  await page.clock.fastForward(650)
  await expect(sheet.locator('.glint')).toHaveCount(0)
})

for (const theme of ['light', 'dark']) test(`IV1 craft sheet: ${theme}, three states at 26 and 64 px`, async ({ page }) => {
  await page.setViewportSize({ width: 1024, height: 950 })
  await page.goto(`/tests/indicator-harness.html?theme=${theme}`)
  await expect(page.locator('.sheet .indicator')).toHaveCount(18)
  const directory = new URL('../../.agent-shots/', import.meta.url)
  mkdirSync(directory, { recursive: true })
  await page.screenshot({ path: fileURLToPath(new URL(`iv1-verified-${theme}.png`, directory)), fullPage: true })
})
