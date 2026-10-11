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
// Console errors and Vue warnings are failures of the flows below that expect none.
async function open(page: Page, options: MockOptions = {}, query = '') {
  const problems: string[] = []
  page.on('console', message => { if (['error', 'warning'].includes(message.type())) problems.push(message.text()) }); page.on('pageerror', error => problems.push(String(error)))
  const state = await mockModels(page, options); await openModelsSettings(page, query)
  return Object.assign(state, { problems })
}

test('the default leads, overrides read as exceptions, reviews pick themselves and one line says what runs next', async ({ page }) => {
  const state = await open(page)
  // Admins open on Just me; nothing here is stored for them yet, so the workspace default shows through.
  await expect(page.getByRole('button', { name: 'Just me' })).toHaveAttribute('aria-pressed', 'true')
  await expect(row(page, 'all')).toContainText('Default · all work'); await expect(row(page, 'all')).toContainText('Used unless a row below overrides it.')
  await expect(pick(page, 'all')).toContainText('GPT-6.1 Sol · xhigh')
  await expect(page.locator('#ex-h')).toHaveText('Except for')
  await expect(row(page, 'design')).toContainText('UI design'); await expect(pick(page, 'design')).toContainText('Opus 5.5 · xhigh')
  await expect(row(page, 'concept')).toContainText('Concepts'); await expect(pick(page, 'concept')).toContainText('Opus 5.5 · high')
  await expect(page.locator('[data-reviews]')).toContainText('Automatic · always another family')
  await expect(page.locator('[data-reviews] a')).toHaveAttribute('href', '/settings/policies#cross-family-review')
  await expect(page.locator('[data-next-line]')).toContainText('The next Backend build runs on GPT-6.1 Sol · xhigh, reviewed by Grok 4.7.')
  await expect(page.getByRole('button', { name: 'Why?' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Different model for…' })).toBeVisible()
  await expect(page.locator('[data-models-section] dialog')).toHaveCount(0)
  expect(state.problems).toEqual([])
})

test('the picker lists every model by provider and line with its own levels, and a click on a level picks both', async ({ page }) => {
  const state = await open(page)
  await pick(page, 'all').click()
  await expect(page.locator('.mdl-pop [role="group"]')).toHaveCount(5)
  expect(await page.locator('.mdl-pop [role="group"]').evaluateAll(groups => groups.map(group => group.getAttribute('aria-label')))).toEqual(['OpenAI', 'Anthropic', 'xAI', 'Cursor', 'Unknown provider'])
  await expect(page.locator('.mdl-pop [role="group"][aria-label="OpenAI"] [role="option"]')).toHaveCount(3)
  // Nothing older is registered here, so no provider shows an "Older versions" line.
  await expect(page.locator('.mdl-pop .opt.more')).toHaveCount(0)
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
  expect(state.writes.at(-1)).toMatchObject({ method: 'DELETE', path: '/model-preferences/orders/other/first', search: '?for=me&revision=5', person: simplePerson })
  expect(state.problems).toEqual([])
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
  await page.getByRole('button', { name: 'For everyone' }).click()
  await expect(row(page, 'design').locator('[data-lock]')).toBeVisible(); await expect(row(page, 'design').locator('[data-remove]')).toBeVisible()
  await expect(row(page, 'all').locator('.mine')).toHaveCount(0)
  await expect(row(page, 'design').locator('[data-lock]')).toHaveAttribute('data-tip', /You locked this for everyone · 2 Oct · Design mocks stay on Opus/)
  // Changing a locked pick for everyone moves its pin with it, so the lock never holds the old model.
  await pick(page, 'design').click()
  const lock = page.locator('.mdl-pop .pm-lock input'); await expect(lock).toBeChecked(); await expect(page.locator('.mdl-pop')).toContainText('Members can’t change this')
  await option(page, 'anthropic:sonnet').locator('.lvc[data-eff="xhigh"]').click()
  await expect(pick(page, 'design')).toContainText('Sonnet 5.5 · xhigh'); await expect(row(page, 'design').locator('[data-lock]')).toBeVisible()
  expect(state.writes.map(write => `${write.method} ${write.path}`)).toEqual(['PUT /model-rules/workspace/design', 'PUT /model-preferences/orders/design/first'])
  expect(state.writes.some(write => write.person)).toBe(false)
  expect(state.writes[1]!.search).toBe('?for=default'); expect(state.writes[0]!.body).toMatchObject({ top: [{ line: 'anthropic:sonnet', why: 'Design mocks stay on Opus while the design gate is tuned (AEON-912).' }], bottom: [], not: {}, revision: 2 })
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
  expect(state.problems).toEqual([])
  // Back on Just me the locked row reads as plain text with its lock; the pick is not editable there.
  await page.getByRole('button', { name: 'Just me' }).click()
  await expect(row(page, 'concept').locator('[data-lock]')).toBeVisible(); expect(await pick(page, 'concept').evaluate(element => element.tagName)).toBe('SPAN')
  await expect(row(page, 'concept').locator('[data-lock]')).toHaveAttribute('data-tip', /You locked this for everyone .* Change it under For everyone\./)
})

test('a lock set by someone else names them, and says "an admin" when the member list is not for this person', async ({ page }) => {
  await open(page, { member: true, lockedByOther: true })
  await expect(row(page, 'design').locator('[data-lock]')).toHaveAttribute('data-tip', 'Ada Admin set this for everyone · 2 Oct · Design mocks stay on Opus while the design gate is tuned (AEON-912).')
})
test('a member sees locked rows as text, resets their own picks and drops rows only they have', async ({ page }) => {
  const state = await open(page, { member: true })
  await expect(page.locator('[data-scope-group]')).toHaveCount(0)
  expect(await pick(page, 'design').evaluate(element => element.tagName)).toBe('SPAN'); await expect(row(page, 'design').locator('[data-lock]')).toBeVisible()
  await expect(row(page, 'design').locator('.mine')).toHaveCount(0)
  await expect(row(page, 'design').locator('[data-lock]')).toHaveAttribute('data-tip', 'You locked this for everyone · 2 Oct · Design mocks stay on Opus while the design gate is tuned (AEON-912).')
  await expect(pick(page, 'all')).toContainText('GPT-6.1 Sol · high'); await expect(row(page, 'all').locator('.mine')).toContainText('yours · reset')
  await expect(row(page, 'concept').locator('.mine')).toContainText('yours · reset'); await expect(row(page, 'concept').locator('[data-remove]')).toHaveCount(0)
  await expect(row(page, 'docs').locator('.mine')).toContainText('yours'); await expect(row(page, 'docs').locator('[data-reset]')).toHaveCount(0)
  await expect(row(page, 'docs').locator('[data-remove]')).toHaveAccessibleName('Remove Docs and copy; it uses the default again')
  await row(page, 'concept').getByRole('button', { name: 'Reset Concepts to the workspace default' }).click()
  await expect(pick(page, 'concept')).toContainText('Opus 5.5 · high'); await expect(row(page, 'concept').locator('.mine')).toHaveCount(0); await expect(pick(page, 'concept')).toBeFocused()
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
  await row(page, 'backend').locator('[data-remove]').click(); await expect(row(page, 'backend')).toHaveCount(0); await expect(page.locator('#add')).toBeFocused()
  expect(state.writes.at(-1)).toMatchObject({ method: 'DELETE', path: '/model-preferences/orders/backend/first' })
  await page.getByRole('button', { name: 'Undo', exact: true }).last().click(); await expect(row(page, 'backend')).toBeVisible()
})

test('clicking elsewhere takes a row that was never picked away again, and an agent only reads', async ({ page }) => {
  const state = await open(page)
  await page.getByRole('button', { name: 'Different model for…' }).click(); await page.getByRole('option', { name: 'Docs and copy' }).click()
  await expect(row(page, 'docs')).toBeVisible(); await expect(page.getByRole('combobox')).toBeFocused()
  await page.mouse.click(6, 500)
  await expect(row(page, 'docs')).toHaveCount(0); await expect(page.locator('.mdl-pop')).toHaveCount(0); expect(state.writes).toHaveLength(0)
  await page.goto('/tests/models-settings-harness.html?agent=1')
  await expect(pick(page, 'all')).toContainText('GPT-6.1 Sol · xhigh'); await expect(page.locator('button[data-pick]')).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Different model for…' })).toHaveCount(0); await expect(page.locator('[data-scope-group]')).toHaveCount(0)
})

test('a pick that cannot run says one sentence from one cause under its row, and Why? shows the skipped route', async ({ page }) => {
  await open(page, { down: true })
  // Approved state "a pick can't run": the Claude account is at its daily cap. Opus takes its Cursor route; Fable waits.
  const notes = page.locator('[data-unavailable]')
  await expect(notes).toHaveCount(2)
  await expect(row(page, 'design').locator('[data-unavailable]')).toHaveText(/^Claude account is at its daily cap until \d\d:\d\d, so Opus 5\.5 runs through Cursor instead\.$/)
  await expect(row(page, 'concept').locator('[data-unavailable]')).toHaveText(/^Claude account is at its daily cap until \d\d:\d\d, so work waits until then\.$/)
  for (const text of await notes.allTextContents()) { expect(text).not.toMatch(/allowance|;|_|\b(\w+) \1\b/); expect(text.match(/\d\d:\d\d/g)).toHaveLength(1) }
  await expect(row(page, 'design').locator('[data-unavailable] > svg')).toBeVisible()
  await expect(page.locator('[data-next-line]')).toContainText('The next UI design runs on Opus 5.5 · max through Cursor, reviewed by GPT-6.1 Sol.')
  await page.getByRole('button', { name: 'Why?' }).click()
  const why = page.getByRole('dialog', { name: 'Why Opus 5.5?' })
  await expect(why).toBeVisible()
  const steps = await why.locator('li').allTextContents()
  expect(steps).toHaveLength(4)
  expect(steps[0]).toBe('UI design has its own model: Opus 5.5 · xhigh.'); expect(steps[1]).toMatch(/^Skipped Opus 5\.5 through Claude Code: Claude account is at its daily cap until \d\d:\d\d\.$/)
  expect(steps.slice(2)).toEqual(['It takes its next route, Cursor: Opus 5.5 · max.', 'Reviews always use another family: GPT-6.1 Sol.'])
  await page.keyboard.press('Escape'); await expect(why).toHaveCount(0); await expect(page.getByRole('button', { name: 'Why?' })).toBeFocused()
})

test('a pick that has no tools here is still offered, with its reason, and the row says what runs instead', async ({ page }) => {
  await open(page, { member: true })
  await pick(page, 'docs').click()
  await expect(option(page, 'xai:grok')).toContainText('Grok · No tools in PAIMOS')
  await expect(option(page, 'xai:grok')).not.toHaveAttribute('aria-disabled', 'true')
  await option(page, 'xai:grok').locator('.lvc[data-eff="high"]').click()
  await expect(pick(page, 'docs')).toContainText('Grok 4.7 · high')
  await expect(row(page, 'docs').locator('[data-unavailable]')).toHaveText('Grok 4.7 has no tools in PAIMOS, so GPT-6.1 Sol · high runs instead.')
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

test('a save refusal and its recovery leave every model control where it was', async ({ page }) => {
  const state = await open(page)
  const controls = { scope: page.locator('[data-scope-group]'), catalog: page.locator('[data-catalog-fold]'), defaultPick: pick(page, 'all'), designPick: pick(page, 'design'), conceptPick: pick(page, 'concept'), why: page.getByRole('button', { name: 'Why?' }) }
  const guard = await controlStability(page, controls)
  await guard.check(async () => {
    state.setFail(409)
    await pick(page, 'all').click(); await option(page, 'anthropic:opus').locator('.lvc[data-eff="max"]').click()
    await expect(page.locator('[data-models-error]')).toContainText('Changed elsewhere. The page was refreshed; the change was not saved.')
  })
  await guard.check(async () => {
    state.setFail(undefined)
    await pick(page, 'all').click(); await option(page, 'anthropic:opus').locator('.lvc[data-eff="max"]').click()
    await expect(page.locator('[data-models-error]')).toHaveCount(0)
    await expect(pick(page, 'all')).toContainText('Opus 5.5 · max')
  })
  guard.done()
  // Chromium reports the refused write. Anything else on the console is a real fault.
  expect(state.problems).toEqual(['Failed to load resource: the server responded with a status of 409 (Conflict)'])
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
  await expect(page.getByRole('dialog', { name: 'Why Opus 5.5?' })).toBeVisible()
  await page.goto('/settings/models/board'); await expect(page).toHaveURL(/\/settings\/models$/)
})

test('a ticket’s “Why this model?” explains that ticket, never the next queued one (Tick, AEON-1138)', async ({ page }) => {
  const state = await open(page, {}, '?why=1&ticket=PHAROS-12')
  const why = page.getByRole('dialog', { name: 'Why Opus 5.5 for PHAROS-12?' })
  await expect(why).toBeVisible()
  expect(await why.locator('li').allTextContents()).toEqual(['Skipped Sonnet 5.5: the account is at its daily cap.', 'PHAROS-12 runs on Opus 5.5 · xhigh.'])
  expect(state.calls).toContain('GET /models/resolve?ticket=PHAROS-12')
  await expect(page.getByRole('dialog', { name: 'Why GPT-6.1 Sol?' })).toHaveCount(0)
  await page.keyboard.press('Escape'); await expect(why).toHaveCount(0)
  // The queued ticket itself opens the queued trace and asks nothing more.
  await openModelsSettings(page, '?why=1&ticket=AEON-1011')
  await expect(page.getByRole('dialog', { name: 'Why GPT-6.1 Sol?' })).toBeVisible()
  expect(state.calls.filter(call => call.startsWith('GET /models/resolve'))).toHaveLength(1)
  expect(state.problems).toEqual([])
})

// A query-only navigation, as an in-app link makes it: the page and its section stay mounted.
async function navigate(page: Page, to: string) {
  await page.evaluate(async path => {
    const root = document.querySelector('#app') as { __vue_app__?: { config: { globalProperties: { $router?: { push: (path: string) => Promise<unknown> } } } } } | null
    const router = root?.__vue_app__?.config.globalProperties.$router
    if (!router) throw new Error('no router on the page')
    await router.push(path)
  }, to)
}

test('a ticket’s Why follows the link on screen: another link closes the last answer, and a late answer is dropped (code health rule 4)', async ({ page }) => {
  const state = await open(page, {}, '?why=1&ticket=PHAROS-12')
  const why = (ticket: string) => page.getByRole('dialog', { name: `Why Opus 5.5 for ${ticket}?` })
  const resolves = (ticket: string) => state.calls.filter(call => call === `GET /models/resolve?ticket=${ticket}`).length
  await expect(why('PHAROS-12')).toBeVisible()
  // Another ticket's link closes the answer for the last one at once and asks about the new ticket.
  const releaseB = state.holdResolve()
  await navigate(page, '/settings/models?why=1&ticket=PHAROS-13')
  await expect(why('PHAROS-12')).toHaveCount(0)
  await expect.poll(() => resolves('PHAROS-13')).toBe(1)
  // A third link while PHAROS-13's answer is still held: that answer arrives first, and only PHAROS-14's shows.
  const releaseC = state.holdResolve()
  await navigate(page, '/settings/models?why=1&ticket=PHAROS-14')
  await expect.poll(() => resolves('PHAROS-14')).toBe(1)
  const lateB = page.waitForResponse(response => response.url().endsWith('/api/models/resolve?ticket=PHAROS-13'))
  releaseB(); await (await lateB).finished()
  releaseC()
  await expect(why('PHAROS-14')).toBeVisible()
  expect(await why('PHAROS-14').locator('li').last().textContent()).toBe('PHAROS-14 runs on Opus 5.5 · xhigh.')
  await expect(why('PHAROS-13')).toHaveCount(0); await expect(page.locator('[data-models-error]')).toHaveCount(0)
  expect(resolves('PHAROS-12') + resolves('PHAROS-13') + resolves('PHAROS-14')).toBe(3)
  expect(state.problems).toEqual([])
})

test('a short menu low in the window keeps its side and its filter when older versions open (AEON-541)', async ({ page }) => {
  const state = await open(page, { few: true })
  const menu = page.locator('.mdl-pop'), trigger = pick(page, 'concept'), older = menu.locator('[data-older="anthropic"]')
  // Measure the folded menu, then end the window just under it: it opens below its trigger, the opened fold needs
  // more room than is left there, and more is left above.
  await trigger.click(); await expect(older).toHaveText('Older versions · 2')
  const folded = await menu.evaluate(el => el.getBoundingClientRect().height)
  await page.keyboard.press('Escape'); await expect(menu).toHaveCount(0)
  const box = (await trigger.boundingBox())!
  await page.setViewportSize({ width: page.viewportSize()!.width, height: Math.ceil(box.y + box.height + folded + 32) })
  await trigger.click(); await expect(older).toBeVisible()
  const placed = await page.evaluate(() => {
    const panel = document.querySelector('.mdl-pop')!.getBoundingClientRect(), anchor = document.querySelector('[data-pick="concept"]')!.getBoundingClientRect()
    return { below: panel.top >= anchor.bottom, roomBelow: innerHeight - anchor.bottom - 18, roomAbove: anchor.top - 18, height: panel.height }
  })
  expect(placed.below, 'the short menu opens below its trigger').toBe(true)
  expect(placed.roomAbove, 'more room above than below').toBeGreaterThan(placed.roomBelow)
  const guard = await controlStability(page, { filter: page.getByRole('combobox'), sol: option(page, 'openai:sol'), opus: option(page, 'anthropic:opus'), trigger })
  await guard.check(async () => { await older.click(); await expect(menu.locator('[data-provider="anthropic"] .opt.is-old')).toHaveCount(2) }); guard.done()
  // The opened fold needed more room than was left below: the list scrolls inside the menu, which ends in the window.
  expect(await menu.locator('.pm-l').evaluate(el => el.scrollHeight > el.clientHeight)).toBe(true)
  expect(await menu.evaluate(el => el.getBoundingClientRect().bottom <= innerHeight)).toBe(true)
  expect(state.problems).toEqual([])
})

test('hovering a half-hidden option never scrolls the list under the pointer; keys still bring it into view (AEON-541)', async ({ page }) => {
  const state = await open(page, { many: true })
  await pick(page, 'all').click()
  const list = page.locator('.mdl-pop .pm-l')
  // Scroll the list so the first option below the fold straddles its lower edge, then aim at the option's visible part.
  const target = await list.evaluate(el => {
    const box = el.getBoundingClientRect()
    const option = [...el.querySelectorAll<HTMLElement>('.opt')].find(item => item.getBoundingClientRect().top >= box.bottom)
    if (!option) return null
    const rect = option.getBoundingClientRect()
    el.scrollTop += rect.top + rect.height / 2 - box.bottom
    const now = option.getBoundingClientRect()
    return now.top < box.bottom && now.bottom > box.bottom ? { id: option.id, x: now.left + now.width / 3, y: (now.top + box.bottom) / 2 } : null
  })
  expect(target, 'an option straddles the list’s lower edge').not.toBeNull()
  const frames = () => page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))))
  await frames()
  const before = await list.evaluate(el => el.scrollTop)
  await page.mouse.move(target!.x, target!.y)
  await expect(page.locator(`#${target!.id}`)).toHaveClass(/\bon\b/)
  await frames()
  expect(await list.evaluate(el => el.scrollTop), 'hover scrolled the list').toBe(before)
  // The keyboard moves on and brings the next option fully into view.
  await page.keyboard.press('ArrowDown')
  await frames()
  const shown = await page.locator('.mdl-pop .opt.on').evaluate(option => {
    const box = option.closest('.pm-l')!.getBoundingClientRect(), rect = option.getBoundingClientRect()
    return rect.top >= box.top - .5 && rect.bottom <= box.bottom + .5
  })
  expect(shown, 'the active option is in view after ArrowDown').toBe(true)
  expect(state.problems).toEqual([])
})

test('many models stay calm: one option per line, older versions fold per provider and are never picked', async ({ page }) => {
  const state = await open(page, { many: true })
  await pick(page, 'all').click()
  const menu = page.locator('.mdl-pop')
  expect(await menu.locator('[role="group"]').evaluateAll(groups => groups.map(group => group.getAttribute('aria-label')))).toEqual(['OpenAI', 'Anthropic', 'xAI', 'Google', 'Cursor', 'Unknown provider'])
  await expect(menu.locator('.opt.is-old')).toHaveCount(0)
  await expect(option(page, 'anthropic:opus')).toContainText('Claude Code, Cursor · best for design and concepts')
  const older = menu.locator('[data-older="anthropic"]')
  await expect(older).toHaveText('Older versions · 3'); await expect(older).toHaveAttribute('aria-label', 'Show 3 older Anthropic versions')
  // The fold opens in place: the filter and the options above it stay where they were (AEON-541).
  const guard = await controlStability(page, { filter: page.getByRole('combobox'), sol: option(page, 'openai:sol'), opus: option(page, 'anthropic:opus') })
  await guard.check(async () => { await older.click(); await expect(menu.locator('[data-provider="anthropic"] .opt.is-old')).toHaveCount(3) }); guard.done()
  await expect(menu.locator('[data-provider="anthropic"] .pm-oh')).toHaveText('Older versions')
  const opus48 = menu.locator('[data-old="anthropic:opus@4.8"]')
  await expect(opus48).toContainText('Claude Code · replaced by Opus 5.5'); await expect(opus48).toHaveAttribute('aria-disabled', 'true')
  // A person can still click it: nothing is picked and the menu stays (Playwright needs force for aria-disabled).
  await opus48.click({ force: true }); await expect(menu).toBeVisible(); expect(state.writes).toHaveLength(0)
  // Typing searches older versions too; Enter on one says why it isn't picked, Enter on the newest picks it.
  await page.keyboard.press('Escape'); await expect(menu).toHaveCount(0); await page.keyboard.press('Enter'); await expect(page.getByRole('combobox')).toBeFocused()
  await page.keyboard.type('opus')
  await expect(menu.locator('[role="option"]')).toHaveCount(2)
  await page.keyboard.press('ArrowDown'); await expect(opus48).toHaveClass(/\bon\b/)
  await page.keyboard.press('Enter'); await expect(menu.locator('[aria-live="polite"]')).toHaveText('Opus 4.8 is an older version. Its line runs Opus 5.5; pick that to use it.')
  expect(state.writes).toHaveLength(0)
  await page.keyboard.press('ArrowUp'); await page.keyboard.press('Enter')
  await expect(menu).toHaveCount(0); await expect(pick(page, 'all')).toContainText('Opus 5.5 · xhigh')
  expect(state.writes[0]).toMatchObject({ method: 'PUT', path: '/model-preferences/orders/other/first' }); expect((state.writes[0]!.body!.rank as string[])[0]).toBe('anthropic:opus')
  expect(state.problems).toEqual([])
})

test('controls stay still while menus open, picks change, rows come and go and the scope switches (AEON-541)', async ({ page }) => {
  await open(page)
  const controls = { scope: page.locator('[data-scope-group]'), catalog: page.locator('[data-catalog-fold]'), forEveryone: page.locator('[data-scope="default"]'), defaultPick: pick(page, 'all'), designPick: pick(page, 'design'), conceptPick: pick(page, 'concept') }
  const guard = await controlStability(page, controls)
  await guard.check(async () => { await pick(page, 'all').click(); await expect(page.locator('.mdl-pop')).toBeVisible(); await page.keyboard.press('ArrowDown'); await page.keyboard.press('ArrowLeft'); await page.keyboard.press('Escape'); await expect(page.locator('.mdl-pop')).toHaveCount(0) })
  await guard.check(async () => { await pick(page, 'concept').click(); await option(page, 'anthropic:fable').locator('.lvc[data-eff="max"]').click(); await expect(pick(page, 'concept')).toContainText('Fable 5.1 · max') })
  await guard.check(async () => { await page.getByRole('button', { name: 'Undo', exact: true }).last().click(); await expect(pick(page, 'concept')).toContainText('Opus 5.5 · high') })
  await guard.check(async () => { await page.getByRole('button', { name: 'Different model for…' }).click(); await page.getByRole('option', { name: 'Backend build' }).click(); await expect(page.getByRole('combobox')).toBeFocused(); await page.keyboard.press('Escape'); await expect(row(page, 'backend')).toHaveCount(0) })
  await guard.check(async () => { await page.getByRole('button', { name: 'For everyone' }).click(); await expect(row(page, 'design').locator('[data-lock]')).toBeVisible() })
  await guard.check(async () => { await page.getByRole('button', { name: 'Just me' }).click(); await expect(row(page, 'all').locator('.mine')).toHaveCount(0) }); guard.done()
  // The catalog toggle stays put while it opens and closes; its settings grow below it.
  const catalog = await controlStability(page, { catalog: page.locator('[data-catalog-fold]') })
  await catalog.check(async () => { await page.locator('[data-catalog-fold]').click(); await expect(page.locator('#catalog-body')).toBeVisible() })
  await catalog.check(async () => { await page.locator('[data-catalog-fold]').click(); await expect(page.locator('#catalog-body')).toHaveCount(0) }); catalog.done()
  // The options of the picker keep their height and place while one is active, selected or has its level stepped.
  await pick(page, 'all').click()
  const options = await controlStability(page, { first: option(page, 'openai:sol'), second: option(page, 'openai:astra'), third: option(page, 'anthropic:opus'), filter: page.getByRole('combobox') })
  await options.check(async () => { await page.keyboard.press('ArrowDown'); await page.keyboard.press('ArrowDown'); await page.keyboard.press('ArrowDown'); await page.keyboard.press('ArrowRight'); await expect(option(page, 'anthropic:opus')).toHaveClass(/\bon\b/) })
  await options.check(async () => { await option(page, 'openai:astra').hover(); await expect(option(page, 'openai:astra')).toHaveClass(/\bon\b/) }); options.done()
})

test('the card holds its layout on a phone', async ({ page }) => {
  await page.setViewportSize({ width: 400, height: 900 })
  await open(page, { down: true })
  const controls = { scope: page.locator('[data-scope-group]'), catalog: page.locator('[data-catalog-fold]'), defaultPick: pick(page, 'all'), conceptPick: pick(page, 'concept') }
  const guard = await controlStability(page, controls)
  await guard.check(async () => { await pick(page, 'all').click(); await expect(page.locator('.mdl-pop')).toBeVisible(); await page.keyboard.press('Escape') })
  await guard.check(async () => { await pick(page, 'concept').click(); await option(page, 'anthropic:sonnet').locator('.lvc[data-eff="max"]').click(); await expect(row(page, 'concept').locator('.mine')).toContainText('yours · reset') })
  await guard.check(async () => { await page.getByRole('button', { name: 'Undo', exact: true }).last().click(); await expect(row(page, 'concept').locator('.mine')).toHaveCount(0) }); guard.done()
  await page.getByRole('button', { name: 'For everyone' }).click(); await expect(row(page, 'design').locator('[data-remove]')).toBeVisible()
  for (const key of ['all', 'design', 'concept']) { const box = await pick(page, key).boundingBox(); expect(box!.height, key).toBeGreaterThanOrEqual(44) }
  expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(1)
})

for (const width of [1440, 400]) test(`a very long kind name wraps and keeps every picker in line at ${width}`, async ({ page }) => {
  await page.setViewportSize({ width, height: 900 })
  await open(page, { longLabels: true, bare: false })
  await expect(row(page, 'design')).toContainText('UI design for the customer-facing marketing site and every campaign landing page')
  const boxes = await Promise.all(['design', 'concept'].map(key => pick(page, key).boundingBox()))
  if (width > 560) expect(Math.abs(boxes[0]!.x - boxes[1]!.x)).toBeLessThanOrEqual(.5)
  expect(Math.abs(boxes[0]!.width - boxes[1]!.width)).toBeLessThanOrEqual(width > 560 ? .5 : 1)
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
