// SPDX-License-Identifier: AGPL-3.0-only
// Settings › Models, minimal (AEON-1011, approved AEON-999 v3). The stand-in server follows the real revision rules.
import { expect, test, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { mockModels, simplePerson, type MockOptions } from './models-simple-fixtures'
import { openModelsSettings } from './models-settings-page'
import { controlStability } from './control-stability'

const row = (page: Page, key: string) => page.locator(`[data-row="${key}"]`)
const pick = (page: Page, key: string) => page.locator(`[data-pick="${key}"]`)
const option = (page: Page, line: string) => page.locator(`.mdl-pop [data-line="${line}"]`)
const toast = (page: Page, text: string) => page.locator('.toast').filter({ hasText: text }).first()
async function open(page: Page, options: MockOptions = {}, query = '') { const state = await mockModels(page, options); await openModelsSettings(page, query); return state }

test('the default leads, overrides read as exceptions, reviews pick themselves and one line says what runs next', async ({ page }) => {
  await open(page)
  // Admins open on Just me; nothing here is stored for them yet, so the workspace default shows through.
  await expect(page.getByRole('radio', { name: 'Just me' })).toHaveAttribute('aria-checked', 'true')
  await expect(row(page, 'all')).toContainText('Default · all work'); await expect(row(page, 'all')).toContainText('Used unless a row below overrides it.')
  await expect(pick(page, 'all')).toContainText('GPT-6.1 Sol · xhigh')
  await expect(page.locator('#ex-h')).toHaveText('Except for')
  await expect(row(page, 'design')).toContainText('UI design'); await expect(pick(page, 'design')).toContainText('Opus 5.5 · xhigh')
  await expect(row(page, 'concept')).toContainText('Concepts'); await expect(pick(page, 'concept')).toContainText('Opus 5.5 · high')
  await expect(page.locator('[data-reviews]')).toContainText('Automatic · always another family')
  await expect(page.locator('[data-reviews] a')).toHaveAttribute('href', '/settings/policies')
  await expect(page.locator('[data-next-line]')).toContainText('The next Backend build runs on GPT-6.1 Sol · xhigh, reviewed by Grok 4.7.')
  await expect(page.getByRole('button', { name: 'Why?' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Different model for…' })).toBeVisible()
  await expect(page.locator('[data-models-section] dialog')).toHaveCount(0)
})

test('the picker lists every model by harness with its own thinking levels, and a click on a level picks both', async ({ page }) => {
  const state = await open(page)
  await pick(page, 'all').click()
  await expect(page.locator('.mdl-pop [role="group"]')).toHaveCount(5)
  expect(await page.locator('.mdl-pop [role="group"]').evaluateAll(groups => groups.map(group => group.getAttribute('aria-label')))).toEqual(['Codex', 'Claude', 'Grok', 'Cursor', 'Pi'])
  await expect(page.locator('.mdl-pop [role="group"][aria-label="Codex"] [role="option"]')).toHaveCount(3)
  await expect(option(page, 'openai:sol')).toContainText('Codex · strongest for building')
  await expect(option(page, 'openai:luna')).toContainText('retiring 31 Oct')
  expect(await option(page, 'anthropic:opus').locator('.lvc').allTextContents()).toEqual(['high', 'xhigh', 'max'])
  await expect(option(page, 'cursor:composer').locator('.lvp.one')).toHaveText('default')
  await expect(option(page, 'unknown:openrouter/qwen/qwen3-coder')).toContainText('Pi · OpenRouter · trial for small scripts (AEON-1003)')
  await expect(option(page, 'openai:sol')).toHaveAttribute('aria-selected', 'true')
  // Nothing is filtered out by what it can do: Grok has no tools, so it says so and stays pickable.
  await page.getByRole('combobox').fill('grok')
  await expect(page.locator('.mdl-pop [role="option"]')).toHaveCount(1); await expect(page.getByRole('combobox')).toHaveAttribute('aria-activedescendant', /grok/)
  await page.getByRole('combobox').fill('nothing like it'); await expect(page.locator('.mdl-pop .pm-none')).toContainText('No model matches “nothing like it”.')
  await page.getByRole('combobox').fill('')
  await option(page, 'anthropic:sonnet').locator('.lvc[data-eff="max"]').click()
  await expect(page.locator('.mdl-pop')).toHaveCount(0)
  await expect(pick(page, 'all')).toContainText('Sonnet 5.5 · max'); await expect(pick(page, 'all')).toBeFocused()
  await expect(row(page, 'all').locator('.mine')).toContainText('yours · reset')
  // A new order for the person, then its native level: two writes, each carrying the person and the revision it read.
  expect(state.writes.map(write => `${write.method} ${write.path}${write.search}`)).toEqual(['PUT /model-preferences/orders/other/first?for=me', 'PUT /model-preferences/orders/other/first/thinking?for=me'])
  expect(state.writes.every(write => write.person === simplePerson)).toBe(true)
  expect(state.writes[0]!.body).toMatchObject({ revision: 3, not: [] }); expect((state.writes[0]!.body!.rank as string[]).slice(0, 3)).toEqual(['anthropic:sonnet', 'openai:sol', 'anthropic:opus'])
  expect(state.writes[1]!.body).toEqual({ effort: 'max', revision: 4 })
  await expect(toast(page, 'Saved')).toBeVisible()
  await page.getByRole('button', { name: 'Undo', exact: true }).last().click()
  await expect(pick(page, 'all')).toContainText('GPT-6.1 Sol · xhigh'); await expect(row(page, 'all').locator('.mine')).toHaveCount(0)
  expect(state.writes.at(-1)).toMatchObject({ method: 'DELETE', path: '/model-preferences/orders/other/first', search: '?for=default&revision=5'.replace('default', 'me'), person: simplePerson })
})

test('the keyboard picks without the mouse: arrows choose the model and its level, Enter keeps both, Esc gives focus back', async ({ page }) => {
  const state = await open(page)
  await pick(page, 'all').focus(); await page.keyboard.press('Enter')
  await expect(page.getByRole('combobox')).toBeFocused()
  await page.keyboard.press('ArrowDown'); await expect(option(page, 'openai:astra')).toHaveClass(/\bon\b/)
  await page.keyboard.press('ArrowLeft'); await expect(option(page, 'openai:astra').locator('.lvc[data-on]')).toHaveText('high')
  await page.keyboard.press('Escape'); await expect(page.locator('.mdl-pop')).toHaveCount(0); await expect(pick(page, 'all')).toBeFocused()
  expect(state.writes).toHaveLength(0)
  await page.keyboard.press('Enter'); await page.keyboard.press('ArrowDown'); await page.keyboard.press('ArrowLeft'); await page.keyboard.press('Enter')
  await expect(pick(page, 'all')).toContainText('GPT-6 Astra · high'); await expect(pick(page, 'all')).toBeFocused()
  expect(state.writes.map(write => write.body)).toEqual([expect.objectContaining({ revision: 3 }), { effort: 'high', revision: 4 }])
})

test('admins switch between For everyone and Just me; the lock lives in the picker and follows the row', async ({ page }) => {
  const state = await open(page)
  await page.getByRole('radio', { name: 'For everyone' }).click()
  await expect(row(page, 'design').locator('[data-lock]')).toBeVisible(); await expect(row(page, 'design').locator('[data-remove]')).toBeVisible()
  await expect(row(page, 'all').locator('.mine')).toHaveCount(0)
  await expect(row(page, 'design').locator('[data-lock]')).toHaveAttribute('data-tip', /You locked this for everyone · 2 Oct · Design mocks stay on Opus/)
  // Changing a locked pick for everyone moves its pin with it, so the lock never holds the old model.
  await pick(page, 'design').click()
  const lock = page.locator('.mdl-pop .pm-lock input'); await expect(lock).toBeChecked(); await expect(page.locator('.mdl-pop')).toContainText('Members can’t change this')
  await option(page, 'anthropic:sonnet').locator('.lvc[data-eff="xhigh"]').click()
  await expect(pick(page, 'design')).toContainText('Sonnet 5.5 · xhigh'); await expect(row(page, 'design').locator('[data-lock]')).toBeVisible()
  expect(state.writes.map(write => `${write.method} ${write.path}`)).toEqual(['PUT /model-preferences/orders/design/first', 'PUT /model-rules/workspace/design'])
  expect(state.writes.some(write => write.person)).toBe(false)
  expect(state.writes[0]!.search).toBe('?for=default'); expect(state.writes[1]!.body).toMatchObject({ top: [{ line: 'anthropic:sonnet', why: 'Design mocks stay on Opus while the design gate is tuned (AEON-912).' }], bottom: [], not: {}, revision: 2 })
  await expect(toast(page, 'Saved for everyone')).toBeVisible()
  await page.getByRole('button', { name: 'Undo', exact: true }).last().click()
  await expect(pick(page, 'design')).toContainText('Opus 5.5 · xhigh')
  expect(state.rules.map(rule => rule.line)).toEqual(['anthropic:opus'])
  // Unlocking is one switch and one rule write; locking another row writes a generated reason.
  await pick(page, 'design').click(); await page.locator('.mdl-pop .pm-lock input').click()
  await expect(row(page, 'design').locator('[data-lock]')).toHaveCount(0); expect(state.rules).toHaveLength(0)
  await pick(page, 'concept').click(); await page.locator('.mdl-pop .pm-lock input').click()
  await expect(row(page, 'concept').locator('[data-lock]')).toBeVisible()
  expect(state.rules.map(rule => [rule.column, rule.line, rule.why])).toEqual([['concept', 'anthropic:opus', 'Locked in Settings › Models by Markus']])
  // Back on Just me the locked row reads as plain text with its lock; the pick is not editable there.
  await page.getByRole('radio', { name: 'Just me' }).click()
  await expect(row(page, 'concept').locator('[data-lock]')).toBeVisible(); expect(await pick(page, 'concept').evaluate(element => element.tagName)).toBe('SPAN')
  await expect(row(page, 'concept').locator('[data-lock]')).toHaveAttribute('data-tip', /You locked this for everyone .* Change it under For everyone\./)
})

test('a member sees locked rows as text, resets their own picks and drops rows only they have', async ({ page }) => {
  const state = await open(page, { member: true })
  await expect(page.locator('[data-scope-group]')).toHaveCount(0)
  expect(await pick(page, 'design').evaluate(element => element.tagName)).toBe('SPAN'); await expect(row(page, 'design').locator('[data-lock]')).toBeVisible()
  await expect(row(page, 'design').locator('.mine')).toHaveCount(0)
  await expect(pick(page, 'all')).toContainText('GPT-6.1 Sol · high'); await expect(row(page, 'all').locator('.mine')).toContainText('yours · reset')
  await expect(row(page, 'concept').locator('.mine')).toContainText('yours · reset'); await expect(row(page, 'concept').locator('[data-remove]')).toHaveCount(0)
  await expect(row(page, 'docs').locator('.mine')).toContainText('yours'); await expect(row(page, 'docs').locator('[data-reset]')).toHaveCount(0)
  await expect(row(page, 'docs').locator('[data-remove]')).toHaveAccessibleName('Remove Docs and copy; it uses the default again')
  await row(page, 'concept').getByRole('button', { name: 'Reset Concepts to the workspace default' }).click()
  await expect(pick(page, 'concept')).toContainText('Opus 5.5 · high'); await expect(row(page, 'concept').locator('.mine')).toHaveCount(0)
  expect(state.writes[0]).toMatchObject({ method: 'DELETE', path: '/model-preferences/orders/concept/first', search: '?for=me&revision=3', person: simplePerson })
  await row(page, 'docs').locator('[data-remove]').click()
  await expect(row(page, 'docs')).toHaveCount(0)
  expect(state.writes[1]).toMatchObject({ method: 'DELETE', path: '/model-preferences/orders/docs/first', search: '?for=me&revision=4' })
  await page.getByRole('button', { name: 'Undo', exact: true }).last().click()
  await expect(row(page, 'docs')).toBeVisible(); await expect(pick(page, 'docs')).toContainText('Sonnet 5.5')
  expect(state.writes.slice(2).map(write => write.method)).toEqual(['PUT', 'PUT'])
})

test('a different model for a kind starts from the default with the picker open; Esc takes it away and Enter keeps it', async ({ page }) => {
  const state = await open(page)
  await page.getByRole('button', { name: 'Different model for…' }).click()
  expect(await page.locator('.mdl-pop [role="option"]').allTextContents()).toEqual(['Frontend build', 'Backend build', 'Infrastructure', 'Docs and copy', 'Security'])
  await expect(page.locator('.mdl-pop')).toContainText('Settings › Kinds of work')
  await page.getByRole('option', { name: 'Backend build' }).click()
  await expect(pick(page, 'backend')).toContainText('GPT-6.1 Sol · xhigh'); await expect(page.getByRole('combobox')).toBeFocused()
  expect(state.writes).toHaveLength(0)
  await page.keyboard.press('Escape'); await expect(row(page, 'backend')).toHaveCount(0); expect(state.writes).toHaveLength(0)
  await page.getByRole('button', { name: 'Different model for…' }).click(); await page.getByRole('option', { name: 'Backend build' }).click()
  await expect(page.getByRole('combobox')).toBeFocused(); await page.keyboard.press('Enter')
  await expect(row(page, 'backend')).toBeVisible(); await expect(row(page, 'backend').locator('[data-remove]')).toBeVisible(); await expect(pick(page, 'backend')).toBeFocused()
  expect(state.writes.map(write => `${write.method} ${write.path}`)).toEqual(['PUT /model-preferences/orders/backend/first', 'PUT /model-preferences/orders/backend/first/thinking'])
  expect(state.writes[1]!.body).toEqual({ effort: 'xhigh', revision: 4 })
  await row(page, 'backend').locator('[data-remove]').click(); await expect(row(page, 'backend')).toHaveCount(0)
  expect(state.writes.at(-1)).toMatchObject({ method: 'DELETE', path: '/model-preferences/orders/backend/first' })
  await page.getByRole('button', { name: 'Undo', exact: true }).last().click(); await expect(row(page, 'backend')).toBeVisible()
})

test('a pick that cannot run says so in words under its row, and Why? shows the skipped step', async ({ page }) => {
  await open(page, { down: true })
  const note = row(page, 'design').locator('[data-unavailable]')
  await expect(note).toContainText('Can’t run right now · Sonnet 5.5 · xhigh runs instead · the Claude account is tied to another profile (AEON-1000)')
  await expect(note.locator('svg')).toBeVisible()
  await expect(page.locator('[data-next-line]')).toContainText('The next UI design runs on Sonnet 5.5 · xhigh instead of Opus 5.5, reviewed by GPT-6.1 Sol.')
  await page.getByRole('button', { name: 'Why?' }).click()
  const why = page.getByRole('dialog', { name: 'Why Sonnet 5.5?' })
  await expect(why).toBeVisible()
  expect(await why.locator('li').allTextContents()).toEqual([
    'UI design has its own model: Opus 5.5 · xhigh.',
    'Skipped Opus 5.5: the Claude account is tied to another profile (AEON-1000).',
    'Next in its order that can do UI design: Sonnet 5.5 · xhigh.',
    'Reviews always use another family: GPT-6.1 Sol.',
  ])
  await page.keyboard.press('Escape'); await expect(why).toHaveCount(0); await expect(page.getByRole('button', { name: 'Why?' })).toBeFocused()
})

test('a pick that has no tools here is still offered, with its reason, and the row says what runs instead', async ({ page }) => {
  await open(page, { member: true })
  await pick(page, 'docs').click()
  await expect(option(page, 'xai:grok')).toContainText('Grok · No tools in PAIMOS')
  await expect(option(page, 'xai:grok')).not.toHaveAttribute('aria-disabled', 'true')
  await option(page, 'xai:grok').locator('.lvc[data-eff="high"]').click()
  await expect(pick(page, 'docs')).toContainText('Grok 4.7 · high')
  await expect(row(page, 'docs').locator('[data-unavailable]')).toContainText('Can’t run right now · GPT-6.1 Sol · high runs instead · no tools in PAIMOS')
})

test('a new model is one line with Use it for… and Not now; it asks for no capability filter', async ({ page }) => {
  const state = await open(page, { fresh: true })
  const line = page.locator('[data-news]')
  await expect(line).toContainText('Grok 4.8 preview is new.')
  await page.getByRole('button', { name: 'Use it for…' }).click()
  expect(await page.locator('.mdl-pop [role="option"]').allTextContents()).toEqual(['Concepts'])
  await expect(page.locator('.mdl-pop .pm-why')).toContainText('UI design, Frontend build, Backend build, Infrastructure, Docs and copy, Security aren’t offered: no tools in PAIMOS.')
  await page.getByRole('option', { name: 'Concepts' }).click()
  await expect(pick(page, 'concept')).toContainText('Grok 4.8 preview · xhigh'); await expect(line).toHaveCount(0)
  expect(state.writes[0]).toMatchObject({ method: 'PUT', path: '/model-preferences/orders/concept/first', person: simplePerson })
  expect((state.writes[0]!.body!.rank as string[])[0]).toBe('xai:grok-preview')
  await page.getByRole('button', { name: 'Undo', exact: true }).last().click(); await expect(pick(page, 'concept')).toContainText('Opus 5.5 · high')
  await expect(line).toBeVisible()
  await page.getByRole('button', { name: 'Not now' }).click(); await expect(line).toHaveCount(0)
  expect(state.writes.at(-1)).toMatchObject({ method: 'POST', path: '/model-preferences/tray/xai%3Agrok-preview/dismiss', search: '?for=me', person: simplePerson })
  await page.getByRole('button', { name: 'Undo', exact: true }).last().click(); await expect(line).toBeVisible()
  expect(state.writes.at(-1)!.body).toMatchObject({ dismissed_lines: [] })
})

for (const status of [409, 403]) test(`a ${status} refusal says so, keeps what was shown and never offers Undo`, async ({ page }) => {
  const state = await open(page, { fail: status })
  await pick(page, 'all').click(); await option(page, 'anthropic:opus').locator('.lvc[data-eff="max"]').click()
  await expect(page.locator('[data-models-error]')).toContainText(status === 409 ? 'Changed elsewhere. The page was refreshed; the change was not saved.' : 'You do not have permission to do this. The change was not saved.')
  await expect(pick(page, 'all')).toContainText('GPT-6.1 Sol · xhigh'); await expect(page.getByRole('button', { name: 'Undo', exact: true })).toHaveCount(0)
  expect(state.writes).toHaveLength(1)
})

test('a failing read shows one honest line with Try again, in place of the card', async ({ page }) => {
  await mockModels(page)
  let broken = true
  await page.route('**/api/model-preferences/simple*', route => broken ? route.fulfill({ status: 500, json: { error: 'down' } }) : route.fallback())
  await page.goto('/tests/models-settings-harness.html')
  await expect(page.locator('.m-err')).toContainText('Models couldn’t load. Agents keep running on the last saved choices.')
  await expect(page.locator('[data-pick]')).toHaveCount(0)
  broken = false; await page.getByRole('button', { name: 'Try again' }).click()
  await expect(pick(page, 'all')).toContainText('GPT-6.1 Sol · xhigh')
})

test('old links still land on the page: ?why=1 opens the trace, the full-screen board redirects', async ({ page }) => {
  await open(page, { down: true }, '?why=1')
  await expect(page.getByRole('dialog', { name: 'Why Sonnet 5.5?' })).toBeVisible()
  await page.goto('/settings/models/board'); await expect(page).toHaveURL(/\/settings\/models$/)
})

test('controls stay still while menus open, picks change, rows come and go and the scope switches (AEON-541)', async ({ page }) => {
  await open(page)
  const controls = { scope: page.locator('[data-scope-group]'), forEveryone: page.locator('[data-scope="default"]'), defaultPick: pick(page, 'all'), designPick: pick(page, 'design'), conceptPick: pick(page, 'concept') }
  const guard = await controlStability(page, controls)
  await guard.check(async () => { await pick(page, 'all').click(); await expect(page.locator('.mdl-pop')).toBeVisible(); await page.keyboard.press('ArrowDown'); await page.keyboard.press('ArrowLeft'); await page.keyboard.press('Escape'); await expect(page.locator('.mdl-pop')).toHaveCount(0) })
  await guard.check(async () => { await pick(page, 'concept').click(); await option(page, 'anthropic:fable').locator('.lvc[data-eff="max"]').click(); await expect(pick(page, 'concept')).toContainText('Fable 5.1 · max') })
  await guard.check(async () => { await page.getByRole('button', { name: 'Undo', exact: true }).last().click(); await expect(pick(page, 'concept')).toContainText('Opus 5.5 · high') })
  await guard.check(async () => { await page.getByRole('button', { name: 'Different model for…' }).click(); await page.getByRole('option', { name: 'Backend build' }).click(); await expect(page.getByRole('combobox')).toBeFocused(); await page.keyboard.press('Escape'); await expect(row(page, 'backend')).toHaveCount(0) })
  await guard.check(async () => { await page.getByRole('radio', { name: 'For everyone' }).click(); await expect(row(page, 'design').locator('[data-lock]')).toBeVisible() })
  await guard.check(async () => { await page.getByRole('radio', { name: 'Just me' }).click(); await expect(row(page, 'all').locator('.mine')).toHaveCount(0) }); guard.done()
  // The options of the picker keep their height and place while one is active, selected or has its level stepped.
  await pick(page, 'all').click()
  const options = await controlStability(page, { first: option(page, 'openai:sol'), second: option(page, 'openai:astra'), third: option(page, 'anthropic:opus'), filter: page.getByRole('combobox') })
  await options.check(async () => { await page.keyboard.press('ArrowDown'); await page.keyboard.press('ArrowDown'); await page.keyboard.press('ArrowDown'); await page.keyboard.press('ArrowRight'); await expect(option(page, 'anthropic:opus')).toHaveClass(/\bon\b/) })
  await options.check(async () => { await option(page, 'openai:astra').hover(); await expect(option(page, 'openai:astra')).toHaveClass(/\bon\b/) }); options.done()
})

test('the card holds its layout on a phone', async ({ page }) => {
  await page.setViewportSize({ width: 400, height: 900 })
  await open(page, { down: true })
  const controls = { scope: page.locator('[data-scope-group]'), defaultPick: pick(page, 'all'), conceptPick: pick(page, 'concept') }
  const guard = await controlStability(page, controls)
  await guard.check(async () => { await pick(page, 'all').click(); await expect(page.locator('.mdl-pop')).toBeVisible(); await page.keyboard.press('Escape') })
  await guard.check(async () => { await pick(page, 'concept').click(); await option(page, 'anthropic:sonnet').locator('.lvc[data-eff="max"]').click(); await expect(row(page, 'concept').locator('.mine')).toContainText('yours · reset') })
  await guard.check(async () => { await page.getByRole('button', { name: 'Undo', exact: true }).last().click(); await expect(row(page, 'concept').locator('.mine')).toHaveCount(0) }); guard.done()
  await page.getByRole('radio', { name: 'For everyone' }).click(); await expect(row(page, 'design').locator('[data-remove]')).toBeVisible()
  for (const key of ['all', 'design', 'concept']) { const box = await pick(page, key).boundingBox(); expect(box!.height, key).toBeGreaterThanOrEqual(44) }
  expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(1)
})

test('the card and its picker have no accessibility violations in light and dark', async ({ page }) => {
  await open(page, { down: true, fresh: true })
  for (const theme of ['light', 'dark']) {
    await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
    await pick(page, 'all').click(); await expect(page.locator('.mdl-pop')).toBeVisible()
    const results = await new AxeBuilder({ page }).include('[data-models-section]').include('.mdl-pop').analyze()
    expect(results.violations.map(violation => violation.id), theme).toEqual([])
    await page.keyboard.press('Escape')
  }
})

test('the catalog settings stay one fold away until the registry card replaces them', async ({ page }) => {
  await open(page, {}, '#model-refresh')
  await expect(page.locator('[data-catalog-fold]')).toHaveAttribute('aria-expanded', 'true')
  await expect(page.locator('#model-refresh')).toBeVisible()
})
