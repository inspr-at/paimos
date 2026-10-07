// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { boardPerson, boardPin } from './models-board-fixtures'
import { card, col } from './models-board-page'
import { mockModelsSettings, openModelsSettings } from './models-settings-page'
import { controlStability } from './control-stability'
const controls = (page: import('@playwright/test').Page) => ({ modes: page.locator('[data-mode-group]'), templates: page.locator('[data-template-group]'), thinking: page.locator('[data-thinking-group]'), kind: page.locator('[data-next-kind]'), project: page.locator('[data-next-project]'), why: page.locator('[data-next-why]') })

test('Auto templates and Thinking save for the captured person with confirmed Undo and no dialog stack', async ({ page }) => {
  const state = await mockModelsSettings(page); await openModelsSettings(page)
  await expect(page.locator('dialog[open]')).toHaveCount(0)
  await expect(page.locator('[data-model-board]')).toHaveCount(0)
  await expect(page.getByRole('link', { name: /Models.*New/ })).toHaveAttribute('href', '/settings/models')
  const guard = await controlStability(page, controls(page))
  await guard.check(async () => { await page.locator('[data-thinking="deep"]').click(); await expect(page.locator('[data-thinking="deep"]')).toHaveAttribute('aria-pressed', 'true'); await expect(page.locator('[data-next-sentence]')).toContainText('xhigh') })
  expect(state.writes).toHaveLength(1); expect(state.writes[0]).toMatchObject({ path: '/model-preferences/profile', person: boardPerson, body: { revision: 3, thinking: 'deep' } })
  await guard.check(async () => { await page.getByRole('button', { name: 'Undo', exact: true }).click(); await expect(page.locator('[data-thinking="standard"]')).toHaveAttribute('aria-pressed', 'true') })
  expect(state.writes[1]!.body).toEqual({ revision: 4, thinking: null })
  await guard.check(async () => { await page.locator('[data-template="best"]').click(); await expect(page.getByRole('dialog')).toContainText('Now → After'); await expect(page.getByRole('dialog')).toContainText('Columns you ordered yourself stay.'); await page.getByRole('button', { name: /^Apply/ }).click(); await expect(page.locator('[data-template="best"]')).toHaveAttribute('aria-pressed', 'true') }); guard.done()
  expect(state.writes).toHaveLength(4); expect(state.writes[2]!.person).toBe(boardPerson); expect(state.writes[3]!.body).toEqual({ template: 'best', revision: 5 })
  await expect(page.getByText('Arrives with AEON-864', { exact: false })).toBeVisible()
  await expect(page.locator('[aria-label="Usage · read only"]').getByRole('button')).toHaveCount(0)
  await expect(page.locator('dialog[open]')).toHaveCount(0)
  const results = await new AxeBuilder({ page }).include('[data-models-section]').analyze(); expect(results.violations).toEqual([])
})

test('Simple shares one editor: keyboard and drag preserve pins, cross-family exclusions and unplaced lines', async ({ page }) => {
  const state = await mockModelsSettings(page); await openModelsSettings(page); await page.locator('[data-models-mode="simple"]').click()
  await expect(page.locator('[data-board-ready="true"]')).toBeVisible()
  const guard = await controlStability(page, { ...controls(page), header: page.locator('.bhead'), pin: card(page, boardPin.line, 'frontend'), board: page.locator('[data-board-scroll]') })
  await guard.check(async () => { await card(page, 'anthropic:sonnet').press('Alt+ArrowUp'); await expect(col(page).locator('[data-zone="list"] li').first()).toHaveAttribute('data-card', 'anthropic:sonnet') }); guard.done()
  expect(state.writes).toHaveLength(1); expect(state.writes[0]!.person).toBe(boardPerson)
  await expect(col(page, 'design').getByLabel('Can’t do this here')).toContainText('Grok')
  await expect(col(page, 'review:openai').locator('[data-zone="not"] [data-line="openai:sol"]')).toHaveAttribute('aria-disabled', 'true')
  await expect(page.locator('.board [data-line="openai:nova"]')).toHaveCount(0)
  const source = await card(page, 'anthropic:sonnet').boundingBox(), target = await card(page, 'openai:sol').boundingBox()
  await page.mouse.move(source!.x + 60, source!.y + 20); await page.mouse.down(); await page.mouse.move(target!.x + 60, target!.y + 44, { steps: 6 }); await page.mouse.up()
  await expect.poll(() => state.writes.length).toBe(2)
  await expect(col(page).locator('[data-zone="list"] li').first()).toHaveAttribute('data-card', 'openai:sol')
  await page.goto('/tests/models-settings-harness.html'); await expect(page.locator('[data-board-ready="true"]')).toBeVisible()
  await expect(page.locator('[data-models-mode="simple"]')).toHaveAttribute('aria-pressed', 'true')
})

for (const status of [409, 428]) test(`a ${status} refusal stays on the Models page and never claims a save`, async ({ page }) => {
  const state = await mockModelsSettings(page, { fail: status }); await openModelsSettings(page)
  await page.locator('[data-thinking="deep"]').click()
  await expect(page.locator('.models-card [role="alert"]')).toContainText(status === 409 ? 'not saved' : 'identity could not be confirmed')
  await expect(page.locator('[data-thinking="standard"]')).toHaveAttribute('aria-pressed', 'true')
  await expect(page.getByRole('button', { name: 'Undo', exact: true })).toHaveCount(0)
  await expect(page).toHaveURL(/\/settings\/models/); await expect(page.locator('dialog[open]')).toHaveCount(0); expect(state.writes).toHaveLength(1)
})

test('Why uses the server trace, proof stays honest when empty, and full screen restores scroll and focus', async ({ page }) => {
  await mockModelsSettings(page); await openModelsSettings(page, '?kind=backend')
  await page.locator('[data-next-why]').click(); const panel = page.getByRole('dialog', { name: 'Why this model?' })
  await expect(panel).toContainText('Account at its floor')
  const guard = await controlStability(page, { close: panel.getByRole('button', { name: /Close details/ }) })
  await guard.check(async () => { await panel.locator('.pane-body').evaluate(element => { element.scrollTop = 100 }); }); guard.done()
  await page.keyboard.press('Escape'); await expect(page.locator('[data-next-why]')).toBeFocused()
  await page.locator('[data-proof-fold]').click(); await page.getByRole('button', { name: 'What actually ran', exact: true }).click()
  await expect(page.getByText('No PAIMOS-dispatched runs recorded yet', { exact: true })).toBeVisible()
  await expect(page.getByText('The Lead’s dispatcher does not, until Engine Wave 2', { exact: true })).toBeVisible()
  await page.locator('[data-models-mode="simple"]').click(); await expect(page.locator('[data-board-ready="true"]')).toBeVisible()
  const main = page.locator('.harness-scroll'); await main.evaluate(element => { element.scrollTop = 180 })
  await page.locator('[data-board-scroll]').evaluate(element => { element.scrollLeft = 100 })
  const top = await main.evaluate(element => element.scrollTop)
  await page.locator('[data-models-fullscreen]').click(); await expect(page.locator('.fullboard [data-board-ready="true"]')).toBeVisible()
  await expect(page.locator('dialog[open]')).toHaveCount(0); expect(await main.evaluate(element => (element as HTMLElement).inert)).toBe(true)
  await page.locator('[data-board-done]').click(); await expect(page.locator('[data-models-fullscreen]')).toBeFocused()
  expect(await main.evaluate(element => element.scrollTop)).toBe(top); expect(await page.locator('[data-board-scroll]').evaluate(element => element.scrollLeft)).toBe(100)
})

test('person changes discard held writes and reset mode, proof folds and pending template confirmation', async ({ page }) => {
  const state = await mockModelsSettings(page); await openModelsSettings(page, '?identity=1')
  const release = state.holdNext(); await page.locator('[data-thinking="deep"]').click(); await expect.poll(() => state.writes.length).toBe(1)
  await page.locator('[data-models-mode="simple"]').click(); await page.locator('[data-proof-fold]').click(); await page.locator('[data-change-person]').click()
  await expect(page.locator('[data-models-mode="auto"]')).toHaveAttribute('aria-pressed', 'true')
  release(); await expect(page.locator('[data-models-section]')).toHaveAttribute('aria-busy', 'false')
  await expect(page.locator('[data-proof-fold]')).toHaveAttribute('aria-expanded', 'false'); await expect(page.getByRole('button', { name: 'Undo', exact: true })).toHaveCount(0)
})

test('agent readers have no preference writes, and Auto remains readable', async ({ page }) => {
  const state = await mockModelsSettings(page, { manage: false }); await openModelsSettings(page, '?agent=1')
  await expect(page.locator('[data-template-group] button, [data-thinking-group] button')).toHaveCount(0)
  await page.locator('[data-models-mode="simple"]').click(); await card(page, 'anthropic:sonnet').press('Alt+ArrowUp'); expect(state.writes).toHaveLength(0)
})

test('run evidence shows the actual catalog model and compares with the recorded choice in bounded pages', async ({ page }) => {
  await mockModelsSettings(page)
  await page.route('**/api/models', route => route.fulfill({ json: [{ id: 'actual-model', display_name: 'GPT-6.1 Sol', model: 'gpt-6.1-sol', effort: 'high', family: 'openai', harness: 'codex' }] }))
  const reads: URL[] = []
  await page.route('**/api/model-preferences/evidence?**', route => {
    const url = new URL(route.request().url()); reads.push(url)
    return route.fulfill({ json: { items: [{ id: url.searchParams.has('cursor') ? 'second' : 'first', kind: 'backend', for_person: boardPerson, at: '2026-10-07T10:00:00Z', source: 'run', actual_profile_id: 'actual-model', preference: { preference_of: { person: boardPerson, source: 'person' } }, agreement: url.searchParams.has('cursor') ? 'differs' : 'matches' }], next_cursor: url.searchParams.has('cursor') ? null : '33333333-3333-4333-8333-333333333333' } })
  })
  await openModelsSettings(page); await page.locator('[data-proof-fold]').click(); await page.getByRole('button', { name: 'What actually ran', exact: true }).click()
  await expect(page.locator('.runs')).toContainText('GPT-6.1 Sol · high'); await expect(page.locator('.runs')).toContainText('for Markus')
  await expect(page.locator('.runs')).toContainText('Agrees with the recorded choice')
  await expect(page.getByText('More records are available. This page shows up to 50.', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Next 50 records', exact: true }).click(); await expect(page.locator('.runs')).toContainText('Differs')
  expect(reads).toHaveLength(2); expect(reads.every(url => url.searchParams.get('limit') === '50')).toBe(true)
  expect(reads[1]!.searchParams.get('cursor')).toBe('33333333-3333-4333-8333-333333333333')
  await expect(page.locator('.runs li')).toHaveCount(1)
})

test('approved desktop and German phone evidence in light and dark', async ({ page }, testInfo) => {
  test.setTimeout(180_000)
  for (const width of [1440, 1280, 1024, 390]) for (const theme of ['light', 'dark'] as const) {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1100 }); await page.emulateMedia({ colorScheme: theme })
    await page.addInitScript(value => { document.addEventListener('DOMContentLoaded', () => { document.documentElement.dataset.theme = value }) }, theme)
    await mockModelsSettings(page, { german: true }); await openModelsSettings(page, '?lang=de&kind=backend')
    await page.locator('[data-models-mode="auto"]').click()
    const auto = await controlStability(page, controls(page)); await auto.check(async () => { await page.locator('[data-thinking="deep"]').click(); await expect(page.locator('[data-thinking="deep"]')).toHaveAttribute('aria-pressed', 'true') }); auto.done()
    await page.getByRole('button', { name: 'Dismiss', exact: true }).click(); await expect(page.locator('.toast')).toHaveCount(0); await page.screenshot({ path: testInfo.outputPath(`auto-${width}-${theme}-de.png`), fullPage: false })
    await page.locator('[data-models-mode="simple"]').click(); await expect(page.locator('[data-board-ready="true"]')).toBeVisible()
    const simple = await controlStability(page, { ...controls(page), pin: card(page, boardPin.line, 'frontend') }); await simple.check(async () => { await card(page, 'anthropic:sonnet').press('Enter'); await page.keyboard.press('Escape') }); simple.done()
    await page.locator('[data-models-mode="simple"]').focus(); await page.screenshot({ path: testInfo.outputPath(`simple-${width}-${theme}-de.png`), fullPage: false })
    await page.locator('[data-next-why]').click(); await expect(page.getByRole('dialog')).toBeVisible(); await page.screenshot({ path: testInfo.outputPath(`why-${width}-${theme}-de.png`), fullPage: false }); await page.keyboard.press('Escape')
  }
})
