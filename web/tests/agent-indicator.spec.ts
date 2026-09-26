// SPDX-License-Identifier: AGPL-3.0-only
// LA3 / AEON-184. Coordinator runs these after merging the /agents wrapper.
import { expect, test, type Locator, type Page } from '@playwright/test'
import { mockIndicator } from './agent-indicator-fixtures'

const preference = 'agent-indicator'
const project = '[data-project-id="p-aeon"]'
const motionPart = (bot: Locator) => bot.locator('.robot, .bob')
async function settings(page: Page) { await page.goto('/settings/personal#appearance'); await expect(page.getByRole('group', { name: 'Agent indicator' })).toBeVisible() }
async function choose(page: Page, style: 'Calm' | 'Playful', hovering: boolean) {
  await page.getByRole('radio', { name: new RegExp(`^${style}`) }).check()
  await page.getByRole('switch', { name: 'Hovering' }).setChecked(hovering)
}

test('defaults, live previews, keyboard selection and account persistence', async ({ page }) => {
  const { data } = await mockIndicator(page)
  await settings(page)
  await expect(page.getByRole('radio', { name: /^Calm/ })).toBeChecked()
  await expect(page.getByRole('switch', { name: 'Hovering' })).not.toBeChecked()
  await expect(page.locator('#appearance .live-bot[data-style="calm"]')).toHaveCount(1)
  await expect(page.locator('#appearance .live-bot[data-style="playful"]')).toHaveCount(1)
  await page.getByRole('radio', { name: /^Calm/ }).focus()
  await page.keyboard.press('ArrowRight')
  await expect(page.getByRole('radio', { name: /^Playful/ })).toBeChecked()
  await page.getByRole('switch', { name: 'Hovering' }).check()
  await expect.poll(() => data.preferences[preference]).toEqual({ style: 'playful', hovering: true })
  await page.reload()
  await expect(page.getByRole('radio', { name: /^Playful/ })).toBeChecked()
  await expect(page.getByRole('switch', { name: 'Hovering' })).toBeChecked()
  await choose(page, 'Calm', true)
  await expect.poll(() => data.preferences[preference]).toEqual({ style: 'calm', hovering: true })
})

for (const style of ['Calm', 'Playful'] as const) {
  test(`${style}: hovering follows cards, rows and popovers; reduced motion wins`, async ({ page }) => {
    await page.emulateMedia({ reducedMotion: 'no-preference' })
    const { data } = await mockIndicator(page)
    await settings(page)
    await choose(page, style, true)
    await expect.poll(() => data.preferences[preference]).toEqual({ style: style.toLowerCase(), hovering: true })
    await page.goto('/')
    const waiting = page.locator('[data-project-id="p-frozen"] .live-bot')
    await expect(waiting).toHaveAttribute('data-state', 'waiting')
    await expect(waiting.locator('.clock, .waiting-clock')).toBeVisible()
    for (const view of ['Cards', 'List']) {
      await page.getByRole('radio', { name: `${view} view`, exact: true }).click()
      const chip = page.locator(`${project} .live-chip`), bot = chip.locator('.live-bot').first()
      await expect(chip.locator('.live-bot')).toHaveCount(2)
      await expect(bot).toHaveAttribute('data-style', style.toLowerCase())
      await expect(motionPart(bot)).not.toHaveCSS('animation-name', 'none')
      if (style === 'Playful') {
        const lags = await chip.locator('.playful-bot').evaluateAll(bots => bots.map(bot => getComputedStyle(bot).getPropertyValue('--lag')))
        expect(new Set(lags).size).toBe(2)
      }
      await chip.hover()
      const pop = page.getByRole('dialog', { name: 'Agents working on Aeon' })
      await expect(pop).toBeVisible()
      await expect(pop.locator('.live-bot').first()).toHaveAttribute('data-style', style.toLowerCase())
      if (style === 'Playful') await expect(bot.locator('.happy')).toHaveCSS('opacity', '1')
      await page.keyboard.press('Escape')
      await page.mouse.move(0, 0)
    }
    await page.emulateMedia({ reducedMotion: 'reduce' })
    const bot = page.locator(`${project} .live-chip .live-bot`).first()
    await expect(motionPart(bot)).toHaveCSS('animation-name', 'none')
    if (style === 'Calm') await expect(bot.locator('.ring-sweep')).toHaveCSS('animation-name', 'none')
    else await expect(bot.locator('.blink')).toHaveCSS('animation-name', 'none')
    await settings(page)
    await choose(page, style, false)
    await expect.poll(() => data.preferences[preference]).toEqual({ style: style.toLowerCase(), hovering: false })
    await page.emulateMedia({ reducedMotion: 'no-preference' })
    await page.goto('/')
    const stationary = page.locator(`${project} .live-chip .live-bot`).first()
    await expect(motionPart(stationary)).toHaveCSS('animation-name', 'none')
    // Hovering is independent: the activity ring or LA1 blink still runs.
    await expect(stationary.locator(style === 'Calm' ? '.ring-sweep' : '.blink')).not.toHaveCSS('animation-name', 'none')
    const stale = page.locator('[data-project-id="p-pharos"] .live-bot')
    await expect(stale).toHaveAttribute('data-state', 'stale')
    await expect(motionPart(stale)).toHaveCSS('animation-name', 'none')
  })

  // AM1 owns /agents' wrapper and is merged by the coordinator. This deliberately
  // requires real LiveBot consumers there; it must not silently skip a missing wrapper.
  test(`${style}: /agents wrappers inherit style, hovering and reduced motion`, async ({ page }) => {
    await page.emulateMedia({ reducedMotion: 'no-preference' })
    const { data } = await mockIndicator(page)
    await settings(page)
    await choose(page, style, true)
    await expect.poll(() => data.preferences[preference]).toEqual({ style: style.toLowerCase(), hovering: true })
    await page.goto('/agents')
    const working = page.locator('.agents-page .live-bot[data-state="working"]').first()
    await expect(working).toHaveAttribute('data-style', style.toLowerCase())
    await expect(motionPart(working)).not.toHaveCSS('animation-name', 'none')
    const waiting = page.locator('.agents-page .live-bot[data-state="waiting"]').first()
    await expect(waiting).toHaveAttribute('data-style', style.toLowerCase())
    await expect(waiting.locator('.clock, .waiting-clock')).toBeVisible()
    const stale = page.locator('.agents-page .live-bot[data-state="stale"]').first()
    await expect(motionPart(stale)).toHaveCSS('animation-name', 'none')
    await page.emulateMedia({ reducedMotion: 'reduce' })
    await expect(motionPart(working)).toHaveCSS('animation-name', 'none')
    await settings(page)
    await choose(page, style, false)
    await expect.poll(() => data.preferences[preference]).toEqual({ style: style.toLowerCase(), hovering: false })
    await page.emulateMedia({ reducedMotion: 'no-preference' })
    await page.goto('/agents')
    await expect(motionPart(page.locator('.agents-page .live-bot[data-state="working"]').first())).toHaveCSS('animation-name', 'none')
  })
}

test('failed saves are visible and can be retried', async ({ page }) => {
  const { data } = await mockIndicator(page)
  let fail = true
  await page.route('**/api/preferences/agent-indicator', route => route.request().method() === 'PUT' && fail ? route.fulfill({ status: 503, json: { error: 'unavailable' } }) : route.fallback())
  await settings(page)
  await choose(page, 'Playful', false)
  await expect(page.getByRole('alert')).toContainText('agent indicator setting could not be saved')
  fail = false
  await page.getByRole('button', { name: 'Try again', exact: true }).click()
  await expect.poll(() => data.preferences[preference]).toEqual({ style: 'playful', hovering: false })
  await expect(page.locator('.save-error')).toHaveCount(0)
})
