// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page } from '@playwright/test'
import { boardPerson } from './models-board-fixtures'
import { card, col } from './models-board-page'
import { mockModelsSettings, openModelsSettings } from './models-settings-page'
import { controlStability } from './control-stability'
const menuChoice = (page: Page, name: string) => page.getByRole('menuitem').filter({ has: page.locator('.t').filter({ hasText: new RegExp(`^${name}$`) }) })
const thinking = (page: Page) => page.locator('[data-column-thinking="backend"]')
const controls = (page: Page) => ({ modes: page.locator('[data-mode-group]'), situation: page.locator('[data-board-situation]'), show: page.locator('[data-board-show]'), providers: page.locator('[data-board-providers]'), thinking: thinking(page), row: card(page, 'openai:sol'), fullscreen: page.locator('[data-models-fullscreen]') })
async function situation(page: Page, name: string) { await page.locator('[data-board-situation]').click(); await menuChoice(page, name).click(); await expect(page.locator('[data-board-ready="true"]')).toBeVisible() }
async function chooseThinking(page: Page, name: string) { await thinking(page).click(); await menuChoice(page, name).click(); await expect(page.locator('[data-model-board]')).toHaveAttribute('aria-busy', 'false') }

test('Expert switches sparse situation boards without writes and keeps selectors and rows still', async ({ page }) => {
  const state = await mockModelsSettings(page); await openModelsSettings(page, '?mode=expert')
  await expect(page.locator('[data-models-mode="expert"]')).toHaveAttribute('aria-pressed', 'true')
  const guard = await controlStability(page, controls(page))
  await guard.check(async () => { await situation(page, 'Fix rounds 1–4'); await expect(col(page).locator('.bc-state')).toContainText('Follows First build'); await expect(thinking(page)).toContainText('Lean'); await expect(thinking(page)).toContainText('auto') })
  await guard.check(async () => { await situation(page, 'Stuck'); await expect(thinking(page)).toContainText('Deep') })
  await guard.check(async () => { await situation(page, 'First build'); await expect(thinking(page)).toContainText('Standard') }); guard.done()
  expect(state.writes).toHaveLength(0)
  await expect(page.locator('[data-column-thinking-readonly="review:openai"]')).toContainText('xhigh, always')
  await expect(page).toHaveURL(/mode=expert/)
  await page.goto('/tests/models-settings-harness.html'); await expect(page.locator('[data-models-mode="expert"]')).toHaveAttribute('aria-pressed', 'true')
})

test('column thinking saves one captured situation, keeps inherited rank, and Undo restores auto', async ({ page }) => {
  const state = await mockModelsSettings(page); await openModelsSettings(page, '?mode=expert&situation=fix')
  const order = await col(page).locator('[data-zone="list"] li').evaluateAll(rows => rows.map(row => row.getAttribute('data-card')))
  const guard = await controlStability(page, controls(page))
  await guard.check(async () => { await chooseThinking(page, 'Max'); await expect(thinking(page)).toContainText('Max'); await expect(thinking(page).locator('small')).toBeHidden(); await expect(col(page).locator('.bc-state')).toContainText('Follows First build') })
  expect(state.writes).toHaveLength(1); expect(state.writes[0]).toMatchObject({ path: '/model-preferences/orders/backend/fix/thinking', person: boardPerson, body: { thinking: 'max', revision: 3 } })
  expect(await col(page).locator('[data-zone="list"] li').evaluateAll(rows => rows.map(row => row.getAttribute('data-card')))).toEqual(order)
  await guard.check(async () => { await page.getByRole('button', { name: 'Undo', exact: true }).click(); await expect(thinking(page)).toContainText('Lean'); await expect(thinking(page).locator('small')).toBeVisible() }); guard.done()
  expect(state.writes[1]).toMatchObject({ path: '/model-preferences/orders/backend/fix/thinking', body: { thinking: null, revision: 4 } })
  await situation(page, 'First build'); await expect(thinking(page)).toContainText('Standard')
})

test('situation order edits leave First build intact and reviews stay on their own column', async ({ page }) => {
  const state = await mockModelsSettings(page); await openModelsSettings(page, '?mode=expert&situation=fix')
  await card(page, 'anthropic:sonnet').press('Alt+ArrowUp'); await expect(col(page).locator('[data-zone="list"] li').first()).toHaveAttribute('data-card', 'anthropic:sonnet')
  expect(state.writes[0]!.path).toBe('/model-preferences/orders/backend/fix')
  await situation(page, 'First build'); await expect(col(page).locator('[data-zone="list"] li').first()).toHaveAttribute('data-card', 'openai:sol')
  await situation(page, 'Stuck'); await card(page, 'anthropic:opus', 'review:openai').press('Alt+ArrowUp')
  await expect.poll(() => state.writes.length).toBe(2)
  expect(state.writes[1]!.path).toBe('/model-preferences/orders/review%3Aopenai/first')
})

test('a refused or held stale thinking write reports honestly and cannot create Undo on a new situation', async ({ page }) => {
  const state = await mockModelsSettings(page, { fail: 428 }); await openModelsSettings(page, '?mode=expert')
  await chooseThinking(page, 'Deep'); await expect(page.locator('.models-card [role="alert"]').first()).toContainText('identity could not be confirmed')
  await expect(thinking(page)).toContainText('Standard'); await expect(page.getByRole('button', { name: 'Undo', exact: true })).toHaveCount(0)
  state.setFail(); const release = state.holdNext(); await thinking(page).click(); await menuChoice(page, 'Max').click()
  await expect.poll(() => state.writes.length).toBe(2); await situation(page, 'Stuck'); release()
  await expect(thinking(page)).toContainText('Deep'); await expect(page.getByRole('button', { name: 'Undo', exact: true })).toHaveCount(0)
})

test('agent readers see Expert and live situation definitions with no thinking write controls', async ({ page }) => {
  const state = await mockModelsSettings(page, { manage: false }); await openModelsSettings(page, '?agent=1&mode=expert')
  await expect(page.locator('[data-column-thinking]')).toHaveCount(0)
  await page.locator('[data-board-situation]').click(); await expect(page.getByRole('menu')).toContainText('After 4 fix rounds review still fails')
  await menuChoice(page, 'Fix rounds 1–4').click(); await expect(col(page).locator('.bc-state')).toContainText('Follows First build')
  expect(state.writes).toHaveLength(0)
})

test('Expert full screen and German light-dark evidence preserve controls across every thinking option', async ({ page }, testInfo) => {
  test.setTimeout(180_000)
  for (const width of [1440, 1280, 1024, 390]) for (const theme of ['light', 'dark'] as const) {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1100 }); await page.emulateMedia({ colorScheme: theme })
    await page.addInitScript(value => { document.addEventListener('DOMContentLoaded', () => { document.documentElement.dataset.theme = value }) }, theme)
    await mockModelsSettings(page, { german: true }); await openModelsSettings(page, '?mode=expert&lang=de&situation=fix')
    const guard = await controlStability(page, controls(page))
    for (const name of ['Knapp', 'Standard', 'Gründlich', 'Maximal', 'Auto, vom Regler']) await guard.check(async () => { await chooseThinking(page, name) })
    await guard.check(async () => { await situation(page, 'Festgefahren'); await expect(thinking(page)).toContainText('Gründlich') }); guard.done()
    while (await page.getByRole('button', { name: 'Dismiss', exact: true }).count()) await page.getByRole('button', { name: 'Dismiss', exact: true }).first().click(); await expect(page.locator('.toast')).toHaveCount(0)
    await page.screenshot({ path: testInfo.outputPath(`expert-${width}-${theme}-de.png`), fullPage: false })
    await page.locator('[data-models-fullscreen]').click(); await expect(page.locator('.fullboard [data-board-ready="true"]')).toBeVisible()
    const full = await controlStability(page, { done: page.locator('[data-board-done]'), situation: page.locator('[data-board-situation]'), thinking: thinking(page) })
    await full.check(async () => { await situation(page, 'Erster Build'); await expect(thinking(page)).toContainText('Standard') }); full.done()
    await page.screenshot({ path: testInfo.outputPath(`expert-full-${width}-${theme}-de.png`), fullPage: false })
    await page.locator('[data-board-done]').click(); await expect(page.locator('[data-models-fullscreen]')).toBeFocused(); await expect(page.locator('[data-models-mode="expert"]')).toHaveAttribute('aria-pressed', 'true')
  }
})
