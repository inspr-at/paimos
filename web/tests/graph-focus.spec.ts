// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page } from '@playwright/test'
import { mockTicketGraph } from './ticket-graph-fixtures'

test.use({ viewport: { width: 1600, height: 1000 } })
const canvas = (page: Page) => page.locator('.ticket-graph-canvas')
const ready = (page: Page) => expect(canvas(page)).toHaveAttribute('data-ready', 'true', { timeout: 20_000 })
const dialog = (page: Page) => page.getByRole('dialog', { name: 'Tickets, connected' })

async function fullscreen(page: Page, mode: 'missing' | 'disabled' | 'rejected' | 'ignored' | 'native') {
  await page.addInitScript(mode => {
    let element: Element | null = null
    const calls = { requests: 0, exits: 0 }
    Object.assign(window, { graphFullscreenCalls: calls })
    Object.defineProperty(document, 'fullscreenEnabled', { configurable: true, get: () => mode !== 'disabled' })
    Object.defineProperty(document, 'fullscreenElement', { configurable: true, get: () => element })
    Object.defineProperty(Element.prototype, 'requestFullscreen', { configurable: true, value: mode === 'missing' ? undefined : async function (this: Element) {
      calls.requests++
      if (mode === 'rejected') throw new Error('Fullscreen refused')
      if (mode === 'native') { element = this; document.dispatchEvent(new Event('fullscreenchange')) }
    } })
    Object.defineProperty(document, 'exitFullscreen', { configurable: true, value: async () => {
      calls.exits++; element = null; document.dispatchEvent(new Event('fullscreenchange'))
    } })
  }, mode)
}
const calls = (page: Page) => page.evaluate(() => (window as unknown as { graphFullscreenCalls: { requests: number; exits: number } }).graphFullscreenCalls)

for (const mode of ['missing', 'disabled', 'rejected', 'ignored'] as const) {
  test(`Focus fills the viewport when fullscreen is ${mode}; Escape keeps filters and canvas`, async ({ page }) => {
    await fullscreen(page, mode)
    await mockTicketGraph(page)
    await page.goto('/p/PHAROS/tickets?view=graph&closed=1#capture'); await ready(page)
    const original = await canvas(page).elementHandle()
    await page.getByRole('button', { name: 'Maximize graph' }).click()
    await expect(page).toHaveURL(/focus=1.*#capture$/)
    await expect(dialog(page)).toBeVisible()
    expect(await dialog(page).boundingBox()).toEqual({ x: 0, y: 0, width: 1600, height: 1000 })
    await expect(page.locator('#app')).toHaveJSProperty('inert', true)
    await expect(canvas(page)).toHaveAttribute('data-motion', 'still')
    await expect(canvas(page)).toBeFocused()
    await page.keyboard.press('Tab')
    await expect(page.getByRole('button', { name: 'Fit graph to view' })).toBeFocused()
    await page.keyboard.press('Shift+Tab')
    await expect(canvas(page)).toBeFocused()
    expect((await calls(page)).requests).toBe(mode === 'missing' || mode === 'disabled' ? 0 : 1)
    await page.keyboard.press('Escape')
    await expect(page).toHaveURL('/p/PHAROS/tickets?view=graph&closed=1#capture')
    await expect(dialog(page)).toHaveCount(0)
    await expect(page.locator('#app')).toHaveJSProperty('inert', false)
    await expect(page.getByRole('button', { name: 'Maximize graph' })).toBeFocused()
    expect(await original!.evaluate(el => el.isConnected)).toBe(true)
  })
}

test('focus deep links and reload use in-app focus without requesting native fullscreen', async ({ page }) => {
  await fullscreen(page, 'native')
  await mockTicketGraph(page)
  await page.goto('/p/PHAROS/tickets?view=graph&closed=1&focus=1#capture'); await ready(page)
  await expect(dialog(page)).toBeVisible()
  expect(await dialog(page).boundingBox()).toEqual({ x: 0, y: 0, width: 1600, height: 1000 })
  await expect(canvas(page)).toHaveAttribute('data-motion', 'still')
  expect((await calls(page)).requests).toBe(0)
  await page.reload(); await ready(page)
  await expect(dialog(page)).toBeVisible()
  expect((await calls(page)).requests).toBe(0)
  await page.getByRole('button', { name: 'Exit graph focus mode' }).click()
  await expect(page).toHaveURL('/p/PHAROS/tickets?view=graph&closed=1#capture')
  await expect(page.locator('#app')).toHaveJSProperty('inert', false)
})

test('native fullscreen is preferred; native Escape clears focus and restores the shell', async ({ page }) => {
  await fullscreen(page, 'native')
  await mockTicketGraph(page)
  await page.goto('/p/PHAROS/tickets?view=graph'); await ready(page)
  await page.getByRole('button', { name: 'Maximize graph' }).click()
  await expect(dialog(page)).toBeVisible()
  await expect.poll(() => page.evaluate(() => document.fullscreenElement?.classList.contains('graph-focus'))).toBe(true)
  expect((await calls(page)).requests).toBe(1)
  // Native Escape exits fullscreen before JavaScript receives a key event.
  await page.evaluate(() => document.exitFullscreen())
  await expect(page).toHaveURL('/p/PHAROS/tickets?view=graph')
  await expect(page.locator('#app')).toHaveJSProperty('inert', false)
  await page.getByRole('button', { name: 'Maximize graph' }).click()
  await expect.poll(() => page.evaluate(() => !!document.fullscreenElement)).toBe(true)
  await page.getByRole('button', { name: 'Exit graph focus mode' }).click()
  await expect(page).not.toHaveURL(/focus=/)
  await expect.poll(() => page.evaluate(() => document.fullscreenElement)).toBeNull()
})

test('opening a ticket leaves native focus before showing its panel', async ({ page }) => {
  await fullscreen(page, 'native')
  await mockTicketGraph(page)
  await page.goto('/p/PHAROS/tickets?view=graph'); await ready(page)
  await page.getByRole('button', { name: 'Maximize graph' }).click()
  await expect.poll(() => page.evaluate(() => !!document.fullscreenElement)).toBe(true)
  await canvas(page).press('ArrowRight'); await canvas(page).press('Enter')
  const panel = page.getByRole('complementary', { name: 'Ticket details' })
  await expect(panel).toBeVisible()
  await expect(page).not.toHaveURL(/focus=/)
  await expect.poll(() => page.evaluate(() => document.fullscreenElement)).toBeNull()
  expect(await panel.evaluate(el => !!el.closest('[inert]'))).toBe(false)
})

test('navigation away releases native fullscreen and the inert background', async ({ page }) => {
  await fullscreen(page, 'native')
  await mockTicketGraph(page)
  await page.goto('/p/PHAROS/tickets?view=graph'); await ready(page)
  await page.getByRole('button', { name: 'Maximize graph' }).click()
  await expect.poll(() => page.evaluate(() => !!document.fullscreenElement)).toBe(true)
  await page.evaluate(async () => { const { router } = await import('/src/router.ts'); await router.push('/projects') })
  await expect(page).toHaveURL('/projects')
  await expect(dialog(page)).toHaveCount(0)
  await expect(page.locator('#app')).toHaveJSProperty('inert', false)
  await expect.poll(() => page.evaluate(() => document.fullscreenElement)).toBeNull()
  expect((await calls(page)).exits).toBe(1)
})
