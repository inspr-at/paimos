// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Locator, type Page } from '@playwright/test'
import { controlStability } from './control-stability'
import { mockRegistry, openRegistry, registryWorld, modifier, type RegistryWorld } from './models-registry-fixtures'

// AEON-1012 · Settings › Models › Model registry. Each test names the risk it guards.

const card = (page: Page) => page.locator('[data-model-registry]')
const row = (page: Page, model: string) => page.locator(`[data-reg-row="${model}"]`)
const editor = (page: Page) => page.locator('[data-reg-editor]')
const toasts = (page: Page) => page.locator('.toast')
async function setup(page: Page, over: Partial<RegistryWorld> = {}) {
  const world = await mockRegistry(page, registryWorld(over))
  await openRegistry(page)
  await expect(row(page, 'gpt-6.1-sol')).toBeVisible()
  return world
}
const choose = (select: Locator, label: string) => select.selectOption({ label })

test('the registry lists every model with its note, source, levels and state, newest versions first', async ({ page }) => {
  await setup(page)
  const sol = row(page, 'gpt-6.1-sol')
  await expect(sol.locator('.rg-n')).toContainText('GPT Sol 6.1')
  await expect(sol.locator('.rg-note')).toHaveText('strongest for building')
  await expect(sol.locator('.rg-m')).toHaveText('Codex · OpenAI · gpt-6.1-sol · Auto-discovered · was 6, taken on 3 Oct')
  await expect(sol.locator('.lv span')).toHaveText(['low', 'medium', 'high', 'xhigh'])
  await expect(row(page, 'gpt-6-luna').locator('.tag-ret')).toHaveText('Retiring 31 Oct')
  const fresh = row(page, 'grok-4.8-preview')
  await expect(fresh.locator('.tag-new')).toHaveText('New')
  await expect(fresh).toContainText('off until used')
  await expect(row(page, 'openrouter/qwen/qwen3-coder').locator('.rg-m')).toHaveText('pi · OpenRouter · openrouter/qwen/qwen3-coder · Added by hand')
  await expect(row(page, 'composer-2.5').locator('.rg-m')).toHaveText('Cursor · composer-2.5 · Auto-discovered')
  // A retired version is not listed; the order inside a harness is newest version first, then by name.
  await expect(page.locator('[data-reg-row^="gpt-6"]')).toHaveCount(3)
  await expect(page.locator('[data-reg-row]').first()).toHaveAttribute('data-reg-row', 'gpt-6.1-sol')
  await expect(card(page).locator('[data-reg-when]')).toHaveText(/^Last checked .* · checks every 6 h$/)
})

test('people without models.manage read the registry and get no controls', async ({ page }) => {
  await setup(page, { permissions: ['models.read'] })
  await expect(card(page).getByRole('button', { name: 'Add model' })).toHaveCount(0)
  await expect(card(page).getByRole('switch')).toHaveCount(0)
  await expect(card(page).getByRole('button', { name: /Check now/ })).toHaveCount(0)
  await expect(page.locator('[data-reg-edit]')).toHaveCount(0)
  await expect(card(page).locator('[data-reg-when]')).toHaveText(/^Auto-update is on · last checked .* · checks every 6 h$/)
})

test('a person who cannot read models sees who to ask instead of an empty list', async ({ page }) => {
  await mockRegistry(page, registryWorld({ permissions: [] }))
  await openRegistry(page)
  await expect(card(page).getByRole('status')).toContainText('visible to people who can read models')
})

test('models that cannot load say so in place and Try again recovers', async ({ page }) => {
  const world = await mockRegistry(page, registryWorld({ failList: true }))
  await openRegistry(page)
  await expect(card(page).locator('[data-reg-load-error]')).toContainText('Models couldn’t load. Agents keep running on the last saved choices.')
  world.failList = false
  await card(page).getByRole('button', { name: 'Try again' }).click()
  await expect(row(page, 'gpt-6.1-sol')).toBeVisible()
})

test('auto-update writes the whole settings object and a failed save puts the switch back', async ({ page }) => {
  const world = await setup(page)
  const auto = card(page).getByRole('switch', { name: 'Auto-update' })
  await expect(auto).toBeChecked()
  await auto.click()
  await expect(auto).not.toBeChecked()
  await expect(card(page).locator('[data-reg-when]')).toHaveText(/^Off · last checked /)
  // The settings the page does not show (agent reports, discovery, interval) keep their values.
  expect(world.writes.at(-1)).toEqual({ method: 'PUT', path: '/models/refresh/settings', body: { agent_reports_enabled: true, auto_add_profiles: false, api_enabled: true, interval_minutes: 360 } })
  world.failSettings = true
  await auto.click()
  await expect(card(page).getByRole('alert')).toHaveText('Auto-update could not be changed. Try again.')
  await expect(auto).not.toBeChecked()
  world.failSettings = false
  await auto.click()
  await expect(auto).toBeChecked()
  await expect(card(page).getByRole('alert')).toHaveCount(0)
  expect(world.settings.auto_add_profiles).toBe(true)
})

test('Check now asks the server, shows the five-minute cooldown on the same button and returns when it ends', async ({ page }) => {
  const world = await setup(page)
  await page.clock.install({ time: new Date('2026-10-09T08:00:00Z') })
  await page.clock.pauseAt(new Date('2026-10-09T08:00:01Z'))
  const check = card(page).locator('[data-reg-check]')
  await expect(check).toHaveAccessibleName('Check now')
  world.check = { status: 200, newLines: ['openai:gpt-7'] }
  await check.click()
  await expect(check).toHaveAccessibleName('Checked · again in 5 min')
  await expect(check).toHaveAttribute('aria-disabled', 'true')
  await expect(toasts(page)).toContainText('1 new model found')
  expect(world.writes.filter(write => write.path === '/models/refresh')).toHaveLength(1)
  // Inside the cooldown a click does not reach the server again.
  await check.click({ force: true })
  expect(world.writes.filter(write => write.path === '/models/refresh')).toHaveLength(1)
  await page.clock.runFor('04:00')
  await expect(check).toHaveAccessibleName('Checked · again in 1 min')
  await page.clock.runFor('01:01')
  await expect(check).toHaveAccessibleName('Check now')
  await expect(check).toHaveAttribute('aria-disabled', 'false')
})

test('Check now shows the wait the server names after a 429, and a failed check says so without a cooldown', async ({ page }) => {
  const world = await setup(page)
  await page.clock.install({ time: new Date('2026-10-09T08:00:00Z') })
  await page.clock.pauseAt(new Date('2026-10-09T08:00:01Z'))
  const check = card(page).locator('[data-reg-check]')
  world.check = { status: 500 }
  await check.click()
  await expect(card(page).getByRole('alert')).toHaveText('The check could not run. Try again.')
  await expect(check).toHaveAccessibleName('Check now')
  world.check = { status: 429, retryAfter: 120 }
  await check.click()
  await expect(check).toHaveAccessibleName('Checked · again in 2 min')
  await expect(card(page).getByRole('alert')).toHaveCount(0)
  await expect(check).toHaveAttribute('data-tip', 'Manual checks wait 5 minutes between runs.')
})

test('Check now says when vendor lists are off, so "nothing new" is not mistaken for "nothing exists"', async ({ page }) => {
  const world = await setup(page)
  world.settings.api_enabled = false
  await page.reload()
  await expect(row(page, 'gpt-6.1-sol')).toBeVisible()
  await card(page).locator('[data-reg-check]').click()
  await expect(toasts(page)).toContainText('vendor model lists are off')
})

test('Add model: a pi OpenRouter slug is stored with its route, in place errors guide, and ⌘↵ saves with Undo', async ({ page }) => {
  const world = await setup(page)
  await card(page).getByRole('button', { name: 'Add model' }).click()
  const form = editor(page)
  await expect(form).toBeVisible()
  await expect(form.getByLabel('Name', { exact: true })).toBeFocused()
  await expect(form.getByLabel('Harness').locator('option')).toHaveText(['Codex', 'Claude', 'Grok', 'Gemini', 'Cursor', 'pi', 'OpenCode'])
  await expect(form.getByLabel('Route or provider').locator('option')).toHaveText(['OpenRouter', 'Anthropic', 'OpenAI', 'Local'])
  await expect(form).toContainText('Any OpenRouter slug, as pi passes it on.')
  // Every mistake is named next to its field and focus goes to the first one.
  await form.getByRole('button', { name: /^Add/ }).click()
  await expect(form.getByText('Give it a name.')).toBeVisible()
  await expect(form.getByText('The model slug is needed.')).toBeVisible()
  await expect(form.getByText('Add at least one level (e.g. off).')).toBeVisible()
  await expect(form.getByLabel('Name', { exact: true })).toBeFocused()
  await form.getByLabel('Name', { exact: true }).fill('Qwen3 Coder')
  await expect(form.getByText('Give it a name.')).toBeHidden()
  await form.getByLabel('Model slug').fill('bad slug')
  await form.getByRole('button', { name: /^Add/ }).click()
  await expect(form.getByText('Letters, digits and . _ : / - only.')).toBeVisible()
  await form.getByLabel('Model slug').fill('qwen')
  await form.getByRole('button', { name: /^Add/ }).click()
  await expect(form.getByText('OpenRouter slugs look like openrouter/vendor/model.')).toBeVisible()
  await form.getByLabel('Model slug').fill('qwen/qwen3-max')
  await form.getByLabel('Add a thinking level (its own name)').fill('off')
  await form.getByLabel('Add a thinking level (its own name)').press('Enter')
  await expect(form.getByRole('button', { name: 'Remove level off' })).toBeVisible()
  await form.getByLabel('Note', { exact: true }).fill('trial for scripts')
  expect(world.writes).toHaveLength(0)
  await form.getByLabel('Note', { exact: true }).press(`${modifier}+Enter`)
  await expect(editor(page)).toHaveCount(0)
  expect(world.writes).toHaveLength(1)
  const post = world.writes[0]!
  expect(post.path).toBe('/models')
  expect(post.body).toMatchObject({ harness: 'pi', family: 'unknown', model: 'openrouter/qwen/qwen3-max', effort: 'off', tier: 'standard', display_name: 'Qwen3 Coder', short_name: 'Qwen3 Coder', note: 'trial for scripts' })
  expect((post.body as { slug: string }).slug).toMatch(/^[a-z][a-z0-9_-]*$/)
  expect((post.body as { version: string }).version).toMatch(/^\d{8}T\d{6}\.\d{3}$/)
  await expect(row(page, 'openrouter/qwen/qwen3-max').locator('.rg-m')).toHaveText('pi · OpenRouter · openrouter/qwen/qwen3-max · Added by hand')
  await expect(toasts(page)).toContainText('Saved')
  await expect(page.getByRole('button', { name: 'Add model' })).toBeFocused()
})

test('Add model with several levels registers them in order, and a failure after the first level is never reported as saved', async ({ page }) => {
  const world = await setup(page)
  await card(page).getByRole('button', { name: 'Add model' }).click()
  const form = editor(page)
  await form.getByLabel('Name', { exact: true }).fill('Qwen local')
  await choose(form.getByLabel('Harness'), 'OpenCode')
  await expect(form.getByLabel('Route or provider').locator('option')).toHaveText(['OpenRouter', 'Local'])
  await choose(form.getByLabel('Route or provider'), 'Local')
  await form.getByLabel('Model slug').fill('qwen3')
  await form.getByLabel('Add a thinking level (its own name)').fill('low, high  max')
  world.failPut = true
  await form.getByRole('button', { name: /^Add/ }).click()
  await expect(form.getByRole('alert')).toContainText('Added with its first level only. The rest could not be saved')
  expect(world.writes.map(write => `${write.method} ${write.path}`)).toEqual(['POST /models', 'PUT /models/lines/opencode/ollama/qwen3'])
  expect(world.writes[0]!.body).toMatchObject({ harness: 'opencode', family: 'local', model: 'ollama/qwen3', effort: 'low' })
  expect(world.writes[1]!.body).toEqual({ display_name: 'Qwen local', note: '', efforts: ['low', 'high', 'max'], route: 'ollama' })
  // The first level exists, so the form is now that entry's Edit with every level still in it.
  await expect(row(page, 'ollama/qwen3')).toBeVisible()
  await expect(editor(page)).toHaveAccessibleName('Edit Qwen local')
  await expect(editor(page).getByRole('button', { name: 'Remove level max' })).toBeVisible()
  world.failPut = false
  await editor(page).getByRole('button', { name: /^Save/ }).click()
  await expect(editor(page)).toHaveCount(0)
  await expect(row(page, 'ollama/qwen3').locator('.lv span')).toHaveText(['low', 'high', 'max'])
})

test('Add model: Undo removes what was added', async ({ page }) => {
  const world = await setup(page)
  await card(page).getByRole('button', { name: 'Add model' }).click()
  const form = editor(page)
  await form.getByLabel('Name', { exact: true }).fill('Gemini Flash')
  await choose(form.getByLabel('Harness'), 'Gemini')
  await form.getByLabel('Model slug').fill('gemini-3-flash')
  await form.getByLabel('Add a thinking level (its own name)').fill('low')
  await form.getByRole('button', { name: /^Add/ }).click()
  await expect(row(page, 'gemini-3-flash')).toBeVisible()
  expect(world.writes[0]!.body).toMatchObject({ harness: 'gemini', family: 'google', model: 'gemini-3-flash' })
  await toasts(page).getByRole('button', { name: 'Undo' }).click()
  await expect(row(page, 'gemini-3-flash')).toHaveCount(0)
  expect(world.writes.at(-1)).toMatchObject({ method: 'POST', body: { reason: 'Undone in Settings › Models' } })
  await expect(toasts(page).filter({ hasText: 'Undone' })).toBeVisible()
})

test('Edit creates a new version with only the changed fields; an auto-discovered entry keeps harness, route and slug', async ({ page }) => {
  const world = await setup(page)
  await row(page, 'gpt-6.1-sol').getByRole('button', { name: 'Edit GPT Sol 6.1' }).click()
  const form = editor(page)
  await expect(form).toHaveAccessibleName('Edit GPT Sol 6.1')
  await expect(form.getByLabel('Name', { exact: true })).toHaveValue('GPT Sol')
  await expect(form.getByLabel('Harness')).toBeDisabled()
  await expect(form.getByLabel('Route or provider')).toBeDisabled()
  await expect(form.getByLabel('Model slug')).toBeDisabled()
  await expect(form).toContainText('From auto-discovery.')
  // Nothing changed: closing through Save writes nothing.
  await form.getByRole('button', { name: /^Save/ }).click()
  await expect(editor(page)).toHaveCount(0)
  expect(world.writes).toHaveLength(0)
  await row(page, 'gpt-6.1-sol').getByRole('button', { name: 'Edit GPT Sol 6.1' }).click()
  await editor(page).getByLabel('Note', { exact: true }).fill('best for building')
  await editor(page).getByRole('button', { name: 'Remove level low' }).click()
  await editor(page).getByLabel('Add a thinking level (its own name)').fill('max')
  await editor(page).getByRole('button', { name: /^Save/ }).click()
  await expect(editor(page)).toHaveCount(0)
  expect(world.writes).toHaveLength(1)
  expect(world.writes[0]).toEqual({ method: 'PUT', path: '/models/lines/codex/gpt-6.1-sol', body: { display_name: 'GPT Sol', note: 'best for building', efforts: ['medium', 'high', 'xhigh', 'max'], route: 'openai' } })
  await expect(row(page, 'gpt-6.1-sol').locator('.rg-note')).toHaveText('best for building')
  await expect(row(page, 'gpt-6.1-sol').locator('.lv span')).toHaveText(['medium', 'high', 'xhigh', 'max'])
  await expect(page.getByRole('button', { name: 'Edit GPT Sol 6.1' })).toBeFocused()
  // Undo writes the earlier values back as one more version.
  await toasts(page).getByRole('button', { name: 'Undo' }).click()
  await expect(row(page, 'gpt-6.1-sol').locator('.lv span')).toHaveText(['low', 'medium', 'high', 'xhigh'])
  expect(world.writes.at(-1)).toEqual({ method: 'PUT', path: '/models/lines/codex/gpt-6.1-sol', body: { display_name: 'GPT Sol', note: 'strongest for building', efforts: ['low', 'medium', 'high', 'xhigh'], route: 'openai' } })
})

test('Edit of a hand-added entry can change its slug and route, and a server refusal is shown in the form', async ({ page }) => {
  const world = await setup(page)
  await row(page, 'openrouter/qwen/qwen3-coder').getByRole('button', { name: /^Edit/ }).click()
  const form = editor(page)
  await expect(form.getByLabel('Harness')).toBeDisabled()
  await expect(form.getByLabel('Model slug')).toBeEnabled()
  await expect(form.getByLabel('Route or provider')).toBeEnabled()
  await choose(form.getByLabel('Route or provider'), 'Anthropic')
  // Changing the route changes the first segment of the slug with it.
  await expect(form.getByLabel('Model slug')).toHaveValue('anthropic/qwen/qwen3-coder')
  await form.getByLabel('Model slug').fill('anthropic/claude-haiku-5')
  world.failPut = true
  await form.getByRole('button', { name: /^Save/ }).click()
  await expect(form.getByRole('alert')).toContainText('It could not be saved: provider route does not match model namespace.')
  await expect(editor(page)).toBeVisible()
  world.failPut = false
  await form.getByRole('button', { name: /^Save/ }).click()
  await expect(editor(page)).toHaveCount(0)
  expect(world.writes.at(-1)).toEqual({ method: 'PUT', path: '/models/lines/pi/openrouter/qwen/qwen3-coder', body: { display_name: 'Qwen3 Coder', note: 'trial for small scripts (AEON-1003)', efforts: ['off'], route: 'anthropic', model: 'anthropic/claude-haiku-5' } })
  await expect(row(page, 'anthropic/claude-haiku-5').locator('.rg-m')).toContainText('pi · Anthropic · anthropic/claude-haiku-5 · Added by hand')
})

test('Remove shows where the model is used and what takes over before anything is removed, and Undo restores every level', async ({ page }) => {
  const world = await setup(page, { usage: { incomplete: false, used_by: [
    { column: 'other', layer: 'default', replacement: { line: 'openai:astra', effort: 'xhigh', harness: 'codex', model: 'gpt-6-astra' } },
    { column: 'backend', layer: 'workspace', replacement: { line: 'openai:astra', effort: 'xhigh', harness: 'codex', model: 'gpt-6-astra' } },
  ] } })
  await row(page, 'gpt-6.1-sol').getByRole('button', { name: /^Edit/ }).click()
  await editor(page).getByRole('button', { name: 'Remove model' }).click()
  const ask = editor(page).locator('[data-reg-confirm]')
  await expect(ask).toContainText('GPT Sol 6.1 is in use: Default · all work (for everyone), Backend build (for everyone). GPT Astra 6 takes over there. Auto-update won’t add it back.')
  expect(world.reads).toContain('/models/lines/codex/gpt-6.1-sol/usage')
  expect(world.writes).toHaveLength(0)
  // Keep it closes the question and removes nothing.
  await ask.getByRole('button', { name: 'Keep it' }).click()
  await expect(ask).toHaveCount(0)
  expect(world.writes).toHaveLength(0)
  await editor(page).getByRole('button', { name: 'Remove model' }).click()
  await editor(page).locator('[data-reg-confirm]').getByRole('button', { name: 'Remove' }).click()
  await expect(row(page, 'gpt-6.1-sol')).toHaveCount(0)
  expect(world.writes.map(write => `${write.method} ${write.path.replace(/[0-9a-f-]{36}/, ':id')}`)).toEqual(Array(4).fill('POST /models/:id/retire'))
  expect(world.writes.every(write => (write.body as { reason: string }).reason === 'Removed in Settings › Models')).toBe(true)
  await expect(toasts(page)).toContainText('Removed GPT Sol 6.1')
  await toasts(page).getByRole('button', { name: 'Undo' }).click()
  await expect(row(page, 'gpt-6.1-sol').locator('.lv span')).toHaveText(['low', 'medium', 'high', 'xhigh'])
  expect(world.writes.slice(4).map(write => write.method)).toEqual(['DELETE', 'DELETE', 'DELETE', 'DELETE'])
})

test('Remove of an unused hand-added entry says nothing uses it; other people’s uses are reported as not shown', async ({ page }) => {
  const world = await setup(page)
  await row(page, 'openrouter/qwen/qwen3-coder').getByRole('button', { name: /^Edit/ }).click()
  await editor(page).getByRole('button', { name: 'Remove model' }).click()
  await expect(editor(page).locator('[data-reg-confirm]')).toHaveText(/^Remove Qwen3 Coder\? Nothing uses it\.\s*Keep it\s*Remove$/)
  await editor(page).getByRole('button', { name: 'Keep it' }).click()
  world.usage = { incomplete: true, used_by: [] }
  await editor(page).getByRole('button', { name: 'Remove model' }).click()
  await expect(editor(page).locator('[data-reg-confirm]')).toContainText('Nothing you can see uses it. Other people’s choices are not shown.')
})

test('Remove cannot go ahead without its preview, and a partly failed removal says how far it got', async ({ page }) => {
  const world = await setup(page, { failUsage: true })
  await row(page, 'gpt-6-astra').getByRole('button', { name: /^Edit/ }).click()
  await editor(page).getByRole('button', { name: 'Remove model' }).click()
  const ask = editor(page).locator('[data-reg-confirm]')
  await expect(ask).toContainText('It could not be checked where GPT Astra 6 is used, so it can’t be removed yet.')
  await expect(ask.getByRole('button', { name: 'Remove' })).toHaveAttribute('aria-disabled', 'true')
  await ask.getByRole('button', { name: 'Remove' }).click({ force: true })
  expect(world.writes).toHaveLength(0)
  world.failUsage = false
  world.failRetireAfter = 2
  await ask.getByRole('button', { name: 'Try again' }).click()
  await expect(editor(page).locator('[data-reg-confirm]')).toContainText('Nothing uses it.')
  await editor(page).locator('[data-reg-confirm]').getByRole('button', { name: 'Remove' }).click()
  await expect(editor(page).getByRole('alert')).toContainText('Only 2 of 4 levels were removed: retire failed. Remove it again to finish.')
  // The list is the server's: two levels are gone, two remain.
  await expect(row(page, 'gpt-6-astra').locator('.lv span')).toHaveCount(2)
})

test('a removal that finishes after the person moved on leaves the form they are in now alone', async ({ page }) => {
  const world = await setup(page)
  let release!: () => void
  world.gateRetire = new Promise<void>(resolve => { release = resolve })
  await row(page, 'gpt-6-astra').getByRole('button', { name: /^Edit/ }).click()
  await editor(page).getByRole('button', { name: 'Remove model' }).click()
  await editor(page).locator('[data-reg-confirm]').getByRole('button', { name: 'Remove' }).click()
  // The removal is held on the server; the person cancels and opens another entry.
  await editor(page).getByRole('button', { name: /^Cancel/ }).click()
  await row(page, 'grok-4.7').getByRole('button', { name: /^Edit/ }).click()
  await expect(editor(page)).toHaveAccessibleName('Edit Grok 4.7')
  await editor(page).getByLabel('Note', { exact: true }).fill('half-typed note')
  release()
  await expect(row(page, 'gpt-6-astra')).toHaveCount(0)
  await expect(toasts(page)).toContainText('Removed GPT Astra 6')
  await expect(editor(page)).toHaveAccessibleName('Edit Grok 4.7')
  await expect(editor(page).getByLabel('Note', { exact: true })).toHaveValue('half-typed note')
})

test('Esc leaves a field, then closes the remove question, then the editor and returns focus to its Edit', async ({ page }) => {
  await setup(page)
  const edit = row(page, 'grok-4.7').getByRole('button', { name: /^Edit/ })
  await edit.click()
  const name = editor(page).getByLabel('Name', { exact: true })
  await expect(name).toBeFocused()
  await name.press('Escape')
  await expect(name).not.toBeFocused()
  await expect(editor(page)).toBeVisible()
  await editor(page).getByRole('button', { name: 'Remove model' }).click()
  await expect(editor(page).locator('[data-reg-confirm]')).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(editor(page).locator('[data-reg-confirm]')).toHaveCount(0)
  await expect(editor(page)).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(editor(page)).toHaveCount(0)
  await expect(edit).toBeFocused()
})

test('an agent key never gets registry controls, even with the permissions', async ({ page }) => {
  await mockRegistry(page, registryWorld())
  await openRegistry(page, '?agent=1')
  await expect(row(page, 'gpt-6.1-sol')).toBeVisible()
  await expect(page.locator('[data-reg-edit], [data-reg-add], [data-reg-check]')).toHaveCount(0)
  await expect(card(page).getByRole('switch')).toHaveCount(0)
})

test('controls stay put: the strip, the entry that was used and its form buttons do not move (±0.5 px)', async ({ page }) => {
  const world = await setup(page, { usage: { incomplete: false, used_by: [{ column: 'other', layer: 'default', replacement: { line: 'openai:astra', effort: 'xhigh', harness: 'codex', model: 'gpt-6-astra' } }] } })
  await page.clock.install({ time: new Date('2026-10-09T08:00:00Z') })
  await page.clock.pauseAt(new Date('2026-10-09T08:00:01Z'))
  const strip = { add: card(page).getByRole('button', { name: 'Add model' }), auto: card(page).getByRole('switch', { name: 'Auto-update' }), check: card(page).locator('[data-reg-check]') }
  // Add: the form opens under the strip; the strip's controls stay while typing, validating and toggling.
  const add = await controlStability(page, strip)
  await add.check(() => strip.add.click())
  await add.check(() => editor(page).getByRole('button', { name: /^Add/ }).click())
  await add.check(() => editor(page).getByLabel('Name', { exact: true }).fill('A model with a rather long name that wraps the way a long German name would wrap in the card'))
  await add.check(() => editor(page).getByLabel('Add a thinking level (its own name)').fill('low medium high xhigh'))
  await add.check(() => strip.auto.click())
  await add.check(() => strip.check.click())
  await add.check(() => page.clock.runFor('06:00'))
  await add.check(() => strip.add.click())
  add.done()
  // Edit: everything above the row and the row's own Edit stay; inside the form the buttons stay through errors and the remove question.
  const edit = row(page, 'gpt-6.1-sol').getByRole('button', { name: /^Edit/ })
  const above = await controlStability(page, { ...strip, edit })
  await above.check(() => edit.click())
  const buttons = { ...strip, edit, remove: editor(page).getByRole('button', { name: 'Remove model' }), cancel: editor(page).getByRole('button', { name: /^Cancel/ }), save: editor(page).getByRole('button', { name: /^Save/ }) }
  const inside = await controlStability(page, buttons)
  await inside.check(() => editor(page).getByLabel('Name', { exact: true }).fill(''))
  await inside.check(() => buttons.save.click())
  await inside.check(() => editor(page).getByLabel('Name', { exact: true }).fill('GPT Sol'))
  await inside.check(() => editor(page).getByLabel('Note', { exact: true }).fill('a longer note for the same model'))
  await inside.check(() => buttons.remove.click())
  await inside.check(() => expect(editor(page).locator('[data-reg-confirm]')).toContainText('GPT Astra 6 takes over there'))
  await inside.check(() => editor(page).getByRole('button', { name: 'Keep it' }).click())
  await inside.check(() => editor(page).getByLabel('Name', { exact: true }).press('Escape'))
  inside.done(); above.done()
  expect(world.writes.some(write => write.path.endsWith('/retire'))).toBe(false)
})

test('screenshots of the card at 1440 and 400 px, light and dark', async ({ page }, testInfo) => {
  await setup(page, { usage: { incomplete: false, used_by: [{ column: 'other', layer: 'default', replacement: { line: 'openai:astra', effort: 'xhigh', harness: 'codex', model: 'gpt-6-astra' } }, { column: 'backend', layer: 'workspace', replacement: { line: 'openai:astra', effort: 'xhigh', harness: 'codex', model: 'gpt-6-astra' } }] } })
  // Tall enough for the whole card: the harness scrolls inside its own frame.
  for (const [width, height] of [[1440, 1700], [400, 3300]] as const) {
    for (const scheme of ['light', 'dark'] as const) {
      await page.setViewportSize({ width, height }); await page.emulateMedia({ colorScheme: scheme, reducedMotion: 'reduce' })
      const shot = async (name: string) => card(page).screenshot({ path: testInfo.outputPath(`${name}-${width}-${scheme}.png`) })
      await shot('list')
      await card(page).getByRole('button', { name: 'Add model' }).click()
      await editor(page).getByRole('button', { name: /^Add/ }).click()
      await shot('add-errors')
      await card(page).getByRole('button', { name: 'Add model' }).click()
      await row(page, 'gpt-6.1-sol').getByRole('button', { name: /^Edit/ }).click()
      await editor(page).getByRole('button', { name: 'Remove model' }).click()
      await expect(editor(page).locator('[data-reg-confirm]')).toContainText('takes over there')
      await shot('edit-remove')
      await page.keyboard.press('Escape'); await page.keyboard.press('Escape')
      await expect(editor(page)).toHaveCount(0)
    }
  }
})
