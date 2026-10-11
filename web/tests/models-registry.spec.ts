// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Locator, type Page } from '@playwright/test'
import { controlStability } from './control-stability'
import { manyProfiles, mockRegistry, openRegistry, registryWorld, modifier, type RegistryWorld } from './models-registry-fixtures'

// AEON-1012 · Settings › Models › Model registry, grouped in AEON-1163 (approved design AEON-1143/1144, W4).
// Providers group the lines; a line is one row, folded by default. Each test names the risk it guards.

const card = (page: Page) => page.locator('[data-model-registry]')
const row = (page: Page, key: string) => page.locator(`[data-reg-row="${key}"]`)
const fold = (page: Page, key: string) => row(page, key).locator('[data-reg-fold]')
const body = (page: Page, key: string) => row(page, key).locator('[data-reg-body]')
const route = (page: Page, key: string, id: string) => body(page, key).locator(`[data-reg-route="${id}"]`)
const editor = (page: Page) => page.locator('[data-reg-editor]')
const confirm = (page: Page, key: string) => row(page, key).locator('[data-reg-confirm]')
const menu = (page: Page) => page.locator('[data-reg-pause-menu]')
const toasts = (page: Page) => page.locator('.toast')
/** One fact row of an open line, by its exact label. */
const fact = (page: Page, key: string, label: string) => body(page, key).locator('.fact').filter({ has: page.locator('.fact-k').getByText(label, { exact: true }) })
const SOL = 'openai:sol', ASTRA = 'openai:astra', LUNA = 'openai:luna', OPUS = 'anthropic:opus', GROK = 'xai:grok', PREVIEW = 'xai:grok-preview', QWEN = 'unknown:openrouter/qwen/qwen3-coder'
async function setup(page: Page, over: Partial<RegistryWorld> = {}) {
  const world = await mockRegistry(page, registryWorld(over))
  await openRegistry(page)
  await expect(row(page, SOL)).toBeVisible()
  return world
}
async function open(page: Page, key: string) {
  await fold(page, key).click()
  await expect(fold(page, key)).toHaveAttribute('aria-expanded', 'true')
  await expect(body(page, key)).toBeVisible()
}
const choose = (select: Locator, label: string) => select.selectOption({ label })
const retires = (world: RegistryWorld) => world.writes.filter(write => write.path.endsWith('/retire'))

test('the registry groups lines by provider, folded by default, each with its source, levels and state, and never says Auto-discovered', async ({ page }) => {
  await setup(page)
  await expect(card(page).locator('.pg-h')).toHaveText([/^OpenAI\s*3 lines$/, /^Anthropic\s*2 lines$/, /^xAI\s*2 lines$/, /^Cursor\s*1 line$/, /^Unknown provider\s*1 line$/])
  await expect(card(page).locator('[data-reg-count]')).toHaveText('9 models · 10 routes · 9 lines')
  await expect(card(page).locator('[data-reg-fold][aria-expanded="true"]')).toHaveCount(0)
  const sol = fold(page, SOL)
  await expect(sol.locator('.ln-name')).toHaveText('GPT Sol')
  await expect(sol.locator('.ln-v')).toHaveText('6.1')
  await expect(sol.locator('.srcb')).toHaveText('From harness')
  await expect(sol.locator('.ln-m')).toHaveText('Codex · strongest for building')
  await expect(sol.locator('.lv span')).toHaveText(['low', 'medium', 'high', 'xhigh'])
  // A version that runs through two harnesses is one row: the routes are counted, never listed as rows.
  await expect(fold(page, OPUS).locator('.ln-m')).toHaveText('2 routes · best for design and concepts')
  await expect(fold(page, LUNA).locator('.tag-ret')).toHaveText('Retiring 31 Oct')
  await expect(fold(page, PREVIEW).locator('.tag-new')).toHaveText('New')
  await expect(fold(page, PREVIEW)).toContainText('off until used')
  await expect(fold(page, QWEN).locator('.srcb')).toHaveText('Added by hand')
  await expect(card(page)).not.toContainText('Auto-discovered')
  // A retired version is not listed.
  await expect(card(page)).not.toContainText('GPT-6 Sol')
  await expect(card(page).locator('[data-reg-when]')).toHaveText(/^Last checked .* · checks every 6 h$/)
})

test('a line opens in place: its routes with their own levels and source, provider, lifecycle and note facts; nothing above it moves', async ({ page }) => {
  await setup(page)
  const above = await controlStability(page, { add: card(page).locator('[data-reg-add]'), check: card(page).locator('[data-reg-check]'), sol: fold(page, SOL), opus: fold(page, OPUS) })
  await above.check(() => open(page, OPUS))
  await above.check(() => fold(page, OPUS).click())
  await above.check(() => open(page, OPUS))
  above.done()
  await expect(route(page, OPUS, 'claude:claude-opus-5-5').locator('.rt-h')).toHaveText('Claude Code')
  await expect(route(page, OPUS, 'claude:claude-opus-5-5').locator('.rt-s')).toHaveText('claude-opus-5-5')
  await expect(route(page, OPUS, 'claude:claude-opus-5-5').locator('.lv span')).toHaveText(['high', 'xhigh', 'max'])
  await expect(route(page, OPUS, 'claude:claude-opus-5-5').locator('.srcb')).toHaveText('From provider')
  await expect(route(page, OPUS, 'pi:anthropic/claude-opus-5-5').locator('.rt-h')).toHaveText('pi via Anthropic')
  await expect(route(page, OPUS, 'pi:anthropic/claude-opus-5-5').locator('.lv span')).toHaveText(['high', 'xhigh'])
  await expect(route(page, OPUS, 'pi:anthropic/claude-opus-5-5').locator('.srcb')).toHaveText('From harness')
  await expect(fact(page, OPUS, 'Provider')).toHaveText(/^Provider\s*Anthropic$/)
  await expect(fact(page, OPUS, 'Lifecycle')).toHaveText(/^Lifecycle\s*Active\s*From provider$/)
  await expect(fact(page, OPUS, 'Note')).toHaveText(/^Note\s*best for design and concepts · shown under its name in every picker$/)
  // A fact nobody reported says so, and a version taken on says what it replaced.
  await open(page, SOL)
  await expect(body(page, SOL).locator('[data-reg-lifecycle]')).toHaveText(/Lifecycle\s*unknown · the provider reports none/)
  await expect(fact(page, SOL, 'Version')).toHaveText(/^Version\s*6\.1 · was 6, taken on 3 Oct$/)
})

test('people without models.manage read the registry and get no controls', async ({ page }) => {
  await setup(page, { permissions: ['models.read'] })
  await expect(card(page).getByRole('button', { name: 'Add model' })).toHaveCount(0)
  await expect(card(page).getByRole('switch', { name: 'Auto-update' })).toHaveCount(0)
  await expect(card(page).getByRole('button', { name: /Check now/ })).toHaveCount(0)
  await open(page, QWEN)
  await expect(body(page, QWEN)).toContainText('pi via OpenRouter')
  await expect(page.locator('[data-reg-edit], [data-reg-pause], [data-reg-remove]')).toHaveCount(0)
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
  await expect(row(page, SOL)).toBeVisible()
})

test('auto-update writes the whole settings object and a failed save puts the switch back', async ({ page }) => {
  const world = await setup(page)
  const auto = card(page).getByRole('switch', { name: 'Auto-update' })
  await expect(auto).toBeChecked()
  await auto.click()
  await expect(auto).not.toBeChecked()
  await expect(card(page).locator('[data-reg-when]')).toHaveText(/^Off · last checked /)
  // The settings the switch does not show (agent reports, discovery, interval) keep their values.
  // AEON-1054: Auto-update is the New model versions rule; it carries the matrix revision.
  await expect.poll(() => world.writes.at(-1)).toEqual({ method: 'PUT', path: '/models/refresh/settings', body: { agent_reports_enabled: true, auto_add_profiles: false, api_enabled: true, interval_minutes: 360, account_use_revision: 7 } })
  world.failSettings = true
  await auto.click()
  await expect(card(page).getByRole('alert')).toHaveText('Auto-update could not be changed. Try again.')
  await expect(auto).not.toBeChecked()
  world.failSettings = false
  await auto.click()
  await expect(auto).toBeChecked()
  await expect(card(page).getByRole('alert')).toHaveCount(0)
  await expect.poll(() => world.settings.auto_add_profiles).toBe(true)
})

test('auto-update without account.use.manage is shown but cannot change; a competing rule change says nothing was saved', async ({ page }) => {
  const world = await setup(page, { permissions: ['models.read', 'models.manage', 'models.refresh'] })
  const auto = card(page).getByRole('switch', { name: 'Auto-update' })
  await expect(auto).toBeChecked()
  await expect(auto).toBeDisabled()
  expect(world.writes.filter(w => w.path === '/models/refresh/settings')).toEqual([])
  world.permissions.push('account.use.manage')
  await page.reload()
  await expect(auto).toBeEnabled()
  // Another person changes the rule between the revision read and the save.
  await page.route('**/api/account-use?*', route => route.fulfill({ json: { rules: { new_accounts: 'ask', new_contexts: 'ask', new_projects: 'default', new_models: 'allow', revision: world.accountUseRevision - 1, enforced_at: null, confirmation_required: false, confirmed_at: null }, accounts: [], contexts: [], cells: [], next_account: null, next_context: null, running_outside: [], running_outside_truncated: false } }))
  await auto.click()
  await expect(card(page).getByRole('alert')).toHaveText('Someone changed the New model versions rule meanwhile. Nothing was saved; try again.')
  await expect(auto).toBeChecked()
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
  await expect(row(page, SOL)).toBeVisible()
  await card(page).locator('[data-reg-check]').click()
  await expect(toasts(page)).toContainText('vendor model lists are off')
})

test('Add model: a pi OpenRouter slug is stored with its route, in place errors guide, and ⌘↵ saves with Undo', async ({ page }) => {
  const world = await setup(page)
  await card(page).getByRole('button', { name: 'Add model' }).click()
  const form = editor(page)
  await expect(form).toBeVisible()
  await expect(form.getByLabel('Name', { exact: true })).toBeFocused()
  await expect(form.getByLabel('Harness').locator('option')).toHaveText(['Codex', 'Claude Code', 'Grok', 'Gemini', 'Cursor', 'pi', 'OpenCode'])
  await expect(form.getByLabel('Route or provider').locator('option')).toHaveText(['OpenRouter', 'Anthropic', 'OpenAI', 'Local'])
  await expect(form).toContainText('Any OpenRouter slug, as pi passes it on. The provider is read from it.')
  // Every mistake is named next to its field and focus goes to the first one.
  await form.getByRole('button', { name: /^Add/ }).click()
  await expect(form.getByText('Give it a name.')).toBeVisible()
  await expect(form.getByText('The model slug is needed.')).toBeVisible()
  await expect(form.getByText('Add at least one level (e.g. off).')).toBeVisible()
  await expect(form.getByLabel('Name', { exact: true })).toBeFocused()
  await form.getByLabel('Name', { exact: true }).fill('Qwen3 Max')
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
  expect(post.body).toMatchObject({ harness: 'pi', family: 'unknown', model: 'openrouter/qwen/qwen3-max', effort: 'off', tier: 'standard', display_name: 'Qwen3 Max', short_name: 'Qwen3 Max', note: 'trial for scripts' })
  expect((post.body as { slug: string }).slug).toMatch(/^[a-z][a-z0-9_-]*$/)
  expect((post.body as { version: string }).version).toMatch(/^\d{8}T\d{6}\.\d{3}$/)
  const added = 'unknown:openrouter/qwen/qwen3-max'
  await expect(fold(page, added).locator('.srcb')).toHaveText('Added by hand')
  await open(page, added)
  await expect(route(page, added, 'pi:openrouter/qwen/qwen3-max')).toContainText('pi via OpenRouter')
  await expect(toasts(page)).toContainText('Saved')
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
  // The first level exists, so the form is now that route's Edit, under its line, with every level still in it.
  await expect(row(page, 'local:qwen')).toBeVisible()
  await expect(row(page, 'local:qwen').locator('[data-reg-editor]')).toHaveAccessibleName('Edit Qwen local, OpenCode route')
  await expect(editor(page).getByRole('button', { name: 'Remove level max' })).toBeVisible()
  world.failPut = false
  await editor(page).getByRole('button', { name: /^Save/ }).click()
  await expect(editor(page)).toHaveCount(0)
  await expect(fold(page, 'local:qwen').locator('.lv span')).toHaveText(['low', 'high', 'max'])
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
  await expect(row(page, 'google:gemini-flash')).toBeVisible()
  expect(world.writes[0]!.body).toMatchObject({ harness: 'gemini', family: 'google', model: 'gemini-3-flash' })
  await toasts(page).getByRole('button', { name: 'Undo' }).click()
  await expect(row(page, 'google:gemini-flash')).toHaveCount(0)
  expect(world.writes.at(-1)).toMatchObject({ method: 'POST', body: { reason: 'Undone in Settings › Models' } })
  await expect(toasts(page).filter({ hasText: 'Undone' })).toBeVisible()
})

test('provider and harness facts are read-only: only a route added by hand has Edit', async ({ page }) => {
  await setup(page)
  await open(page, SOL)
  await expect(body(page, SOL).locator('[data-reg-edit]')).toHaveCount(0)
  await expect(body(page, SOL).locator('[data-reg-pause]')).toHaveText('Pause…')
  await expect(body(page, SOL).locator('[data-reg-remove]')).toHaveText('Remove…')
  await open(page, OPUS)
  await expect(body(page, OPUS).locator('[data-reg-edit]')).toHaveCount(0)
  await open(page, QWEN)
  await expect(body(page, QWEN).locator('[data-reg-edit]')).toHaveText(['Edit pi route'])
})

test('Edit of a hand-added route can change its slug and route, and a server refusal is shown in the form', async ({ page }) => {
  const world = await setup(page)
  await open(page, QWEN)
  await body(page, QWEN).getByRole('button', { name: 'Edit pi route' }).click()
  const form = editor(page)
  await expect(form).toHaveAccessibleName('Edit Qwen3 Coder, pi route')
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
  expect(world.writes.at(-1)).toEqual({ method: 'PUT', path: '/models/lines/pi/openrouter/qwen/qwen3-coder', body: { display_name: 'Qwen3 Coder', note: 'trial for small scripts (AEON-1003)', efforts: ['off'], route: 'anthropic', model: 'anthropic/claude-haiku-5', revision: 'r1' } })
  await open(page, 'unknown:haiku')
  await expect(route(page, 'unknown:haiku', 'pi:anthropic/claude-haiku-5')).toContainText('pi via Anthropic')
  await expect(route(page, 'unknown:haiku', 'pi:anthropic/claude-haiku-5').locator('.srcb')).toHaveText('Added by hand')
})

test('Remove… asks where every route of the version is used before anything is removed, removes every level of every route, and Undo restores them', async ({ page }) => {
  const world = await setup(page, { usage: { incomplete: false, used_by: [
    { column: 'other', layer: 'default', replacement: { line: 'openai:astra', effort: 'xhigh', harness: 'codex', model: 'gpt-6-astra' } },
    { column: 'backend', layer: 'workspace', replacement: { line: 'openai:astra', effort: 'xhigh', harness: 'codex', model: 'gpt-6-astra' } },
  ] } })
  await open(page, OPUS)
  await body(page, OPUS).getByRole('button', { name: 'Remove…' }).click()
  const ask = confirm(page, OPUS)
  await expect(ask).toContainText('Opus 5.5 is in use: Default · all work (for everyone) — GPT-6 Astra at xhigh takes over; Backend build (for everyone) — GPT-6 Astra at xhigh takes over. Auto-update won’t add it back.')
  expect(world.reads).toEqual(expect.arrayContaining(['/models/lines/claude/claude-opus-5-5/usage', '/models/lines/pi/anthropic/claude-opus-5-5/usage']))
  expect(world.writes).toHaveLength(0)
  // Keep it closes the question, removes nothing and returns to Remove….
  await ask.getByRole('button', { name: 'Keep it' }).click()
  await expect(ask).toHaveCount(0)
  await expect(body(page, OPUS).getByRole('button', { name: 'Remove…' })).toBeFocused()
  expect(world.writes).toHaveLength(0)
  await body(page, OPUS).getByRole('button', { name: 'Remove…' }).click()
  await confirm(page, OPUS).getByRole('button', { name: 'Remove', exact: true }).click()
  await expect(row(page, OPUS)).toHaveCount(0)
  expect(retires(world).map(write => `${write.method} ${write.path.replace(/[0-9a-f-]{36}/, ':id')}`)).toEqual(Array(5).fill('POST /models/:id/retire'))
  expect(retires(world).every(write => (write.body as { reason: string }).reason === 'Removed in Settings › Models')).toBe(true)
  await expect(toasts(page)).toContainText('Removed Opus 5.5')
  await toasts(page).getByRole('button', { name: 'Undo' }).click()
  await expect(fold(page, OPUS).locator('.ln-m')).toHaveText('2 routes · best for design and concepts')
  expect(world.writes.slice(5).map(write => write.method)).toEqual(Array(5).fill('DELETE'))
})

test('Remove of an unused hand-added line says nothing uses it; other people’s uses are reported as not shown', async ({ page }) => {
  const world = await setup(page)
  await open(page, QWEN)
  await body(page, QWEN).getByRole('button', { name: 'Remove…' }).click()
  await expect(confirm(page, QWEN)).toHaveText(/^Remove Qwen3 Coder\? Nothing uses it\.\s*Keep it\s*Remove$/)
  await confirm(page, QWEN).getByRole('button', { name: 'Keep it' }).click()
  world.usage = { incomplete: true, used_by: [] }
  await body(page, QWEN).getByRole('button', { name: 'Remove…' }).click()
  await expect(confirm(page, QWEN)).toContainText('Nothing you can see uses it. Other people’s choices are not shown.')
})

test('Remove cannot go ahead without its preview, and a partly failed removal says how far it got', async ({ page }) => {
  const world = await setup(page, { failUsage: true })
  await open(page, ASTRA)
  await body(page, ASTRA).getByRole('button', { name: 'Remove…' }).click()
  const ask = confirm(page, ASTRA)
  await expect(ask).toContainText('It could not be checked where GPT-6 Astra is used, so it can’t be removed yet.')
  await expect(ask.getByRole('button', { name: 'Remove', exact: true })).toHaveAttribute('aria-disabled', 'true')
  await ask.getByRole('button', { name: 'Remove', exact: true }).click({ force: true })
  expect(world.writes).toHaveLength(0)
  world.failUsage = false
  world.failRetireAfter = 2
  await ask.getByRole('button', { name: 'Try again' }).click()
  await expect(confirm(page, ASTRA)).toContainText('Nothing uses it.')
  await confirm(page, ASTRA).getByRole('button', { name: 'Remove', exact: true }).click()
  await expect(confirm(page, ASTRA).locator('[data-reg-error]')).toHaveText('Only 2 of 4 levels of GPT-6 Astra were removed: retire failed. Remove it again to finish.')
  // The list is the server's: two levels are gone, two remain.
  await expect(fold(page, ASTRA).locator('.lv span')).toHaveCount(2)
})

test('a removal that finishes after the person moved on leaves the form they are in now alone', async ({ page }) => {
  const world = await setup(page)
  let release!: () => void
  world.gateRetire = new Promise<void>(resolve => { release = resolve })
  await open(page, ASTRA)
  await body(page, ASTRA).getByRole('button', { name: 'Remove…' }).click()
  await confirm(page, ASTRA).getByRole('button', { name: 'Remove', exact: true }).click()
  // The removal is held on the server; the person opens the editor of a route added by hand and types.
  await open(page, QWEN)
  await body(page, QWEN).getByRole('button', { name: 'Edit pi route' }).click()
  await expect(editor(page)).toHaveAccessibleName('Edit Qwen3 Coder, pi route')
  await editor(page).getByLabel('Note', { exact: true }).fill('half-typed note')
  release()
  await expect(row(page, ASTRA)).toHaveCount(0)
  await expect(toasts(page)).toContainText('Removed GPT-6 Astra')
  await expect(editor(page)).toHaveAccessibleName('Edit Qwen3 Coder, pi route')
  await expect(editor(page).getByLabel('Note', { exact: true })).toHaveValue('half-typed note')
})

test('Esc in the editor leaves a field, then closes it and returns focus to its Edit; Esc in the remove question returns focus to Remove…', async ({ page }) => {
  await setup(page)
  await open(page, QWEN)
  const edit = body(page, QWEN).getByRole('button', { name: 'Edit pi route' })
  await edit.click()
  const name = editor(page).getByLabel('Name', { exact: true })
  await expect(name).toBeFocused()
  await name.press('Escape')
  await expect(name).not.toBeFocused()
  await expect(editor(page)).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(editor(page)).toHaveCount(0)
  await expect(edit).toBeFocused()
  const remove = body(page, QWEN).getByRole('button', { name: 'Remove…' })
  await remove.click()
  await expect(confirm(page, QWEN)).toContainText('Nothing uses it.')
  await confirm(page, QWEN).getByRole('button', { name: 'Keep it' }).focus()
  await page.keyboard.press('Escape')
  await expect(confirm(page, QWEN)).toHaveCount(0)
  await expect(remove).toBeFocused()
})

test('Pause… offers tomorrow, a week or until resumed; every route pauses, the row says so, and Undo puts back what was there', async ({ page }) => {
  await page.clock.install({ time: new Date('2026-10-11T08:00:00Z') })
  const world = await setup(page)
  await page.clock.pauseAt(new Date('2026-10-11T08:00:01Z'))
  await open(page, OPUS)
  const pause = body(page, OPUS).locator('[data-reg-pause]')
  await pause.click()
  await expect(menu(page).getByRole('menuitem')).toHaveText([/Until tomorrow, 04:00\s*It runs again by itself/, /For a week\s*Until 18 Oct/, /Until I resume it\s*No end date/])
  // The keyboard moves through the menu; Esc closes it and returns to Pause….
  await expect(menu(page).getByRole('menuitem').first()).toBeFocused()
  await page.keyboard.press('ArrowDown')
  await expect(menu(page).getByRole('menuitem').nth(1)).toBeFocused()
  await page.keyboard.press('Escape')
  await expect(menu(page)).toHaveCount(0)
  await expect(pause).toBeFocused()
  expect(world.writes).toHaveLength(0)
  await pause.click()
  await menu(page).getByRole('menuitem', { name: /For a week/ }).click()
  await expect(toasts(page)).toContainText('Paused Opus 5.5 until 18 Oct')
  const pauses = world.writes.filter(write => write.path.endsWith('/pause'))
  expect(pauses.map(write => `${write.method} ${write.path}`)).toEqual(['PUT /models/lines/claude/claude-opus-5-5/pause', 'PUT /models/lines/pi/anthropic/claude-opus-5-5/pause'])
  for (const write of pauses) {
    expect((write.body as { reason: string }).reason).toBe('Paused in Settings › Models')
    expect(Date.parse((write.body as { until: string }).until) - Date.parse('2026-10-11T08:00:01Z')).toBe(7 * 86_400_000)
  }
  await expect(fold(page, OPUS).locator('[data-reg-paused]')).toHaveText('Paused until 18 Oct')
  await expect(pause).toHaveText('Paused until 18 Oct')
  await expect(pause).toBeFocused()
  await expect(route(page, OPUS, 'claude:claude-opus-5-5').locator('.rt-note.warn')).toHaveText('Paused until 18 Oct')
  // Undo removes the pauses it added, nothing else.
  await toasts(page).filter({ hasText: 'Paused Opus 5.5' }).getByRole('button', { name: 'Undo' }).click()
  await expect(fold(page, OPUS).locator('[data-reg-paused]')).toHaveCount(0)
  expect(world.writes.slice(-2).map(write => `${write.method} ${write.path}`)).toEqual(['DELETE /models/lines/claude/claude-opus-5-5/pause', 'DELETE /models/lines/pi/anthropic/claude-opus-5-5/pause'])
  // Until resumed, then Resume now from the same menu.
  await pause.click()
  await menu(page).getByRole('menuitem', { name: /Until I resume it/ }).click()
  await expect(fold(page, OPUS).locator('[data-reg-paused]')).toHaveText('Paused until resumed')
  await pause.click()
  await expect(menu(page).getByRole('menuitem').first()).toHaveText(/Resume now/)
  await menu(page).getByRole('menuitem', { name: /Resume now/ }).click()
  await expect(fold(page, OPUS).locator('[data-reg-paused]')).toHaveCount(0)
  await expect(toasts(page).filter({ hasText: 'Resumed Opus 5.5' })).toBeVisible()
})

test('a pause refused on one route says how far it got and never reports the version paused', async ({ page }) => {
  const world = await setup(page, { failPause: 'anthropic/claude-opus-5-5' })
  await open(page, OPUS)
  await body(page, OPUS).locator('[data-reg-pause]').click()
  await menu(page).getByRole('menuitem', { name: /Until I resume it/ }).click()
  await expect(toasts(page).filter({ hasText: 'Paused only 1 of its routes: model_pause_limit. Try again to finish.' })).toBeVisible()
  await expect(toasts(page).filter({ hasText: /^Paused Opus 5\.5/ })).toHaveCount(0)
  await expect(fold(page, OPUS).locator('[data-reg-paused]')).toHaveText('Claude Code paused')
  expect(world.profiles.filter(profile => profile.model === 'claude-opus-5-5').every(profile => profile.pause?.until === null)).toBe(true)
})

test('an agent key never gets registry controls, even with the permissions', async ({ page }) => {
  await mockRegistry(page, registryWorld())
  await openRegistry(page, '?agent=1')
  await expect(row(page, SOL)).toBeVisible()
  await open(page, QWEN)
  await expect(page.locator('[data-reg-edit], [data-reg-add], [data-reg-check], [data-reg-pause], [data-reg-remove]')).toHaveCount(0)
  await expect(card(page).getByRole('switch')).toHaveCount(0)
})

test('controls stay put: the strip, an opened line\'s actions and the editor\'s buttons do not move (±0.5 px)', async ({ page }) => {
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
  // An open line: its own row and actions stay while the remove question, the pause menu and a fold below open and close.
  await open(page, SOL)
  const actions = { ...strip, sol: fold(page, SOL), pause: body(page, SOL).locator('[data-reg-pause]'), remove: body(page, SOL).locator('[data-reg-remove]') }
  const line = await controlStability(page, actions)
  await line.check(() => actions.remove.click())
  await line.check(() => expect(confirm(page, SOL)).toContainText('GPT-6 Astra at xhigh takes over'))
  await line.check(() => confirm(page, SOL).getByRole('button', { name: 'Keep it' }).click())
  await line.check(() => actions.pause.click())
  await line.check(() => page.keyboard.press('Escape'))
  await line.check(() => open(page, LUNA))
  await line.check(() => fold(page, LUNA).click())
  line.done()
  // The editor of a route added by hand: its buttons stay through errors and level changes.
  await open(page, QWEN)
  await body(page, QWEN).getByRole('button', { name: 'Edit pi route' }).click()
  const buttons = { ...strip, edit: body(page, QWEN).locator('[data-reg-edit]'), cancel: editor(page).getByRole('button', { name: /^Cancel/ }), save: editor(page).getByRole('button', { name: /^Save/ }) }
  const inside = await controlStability(page, buttons)
  await inside.check(() => editor(page).getByLabel('Name', { exact: true }).fill(''))
  await inside.check(() => buttons.save.click())
  await inside.check(() => editor(page).getByLabel('Name', { exact: true }).fill('Qwen3 Coder'))
  await inside.check(() => editor(page).getByLabel('Note', { exact: true }).fill('a longer note for the same model'))
  await inside.check(async () => {
    await editor(page).getByLabel('Add a thinking level (its own name)').fill('extended-level')
    await editor(page).getByLabel('Add a thinking level (its own name)').press('Enter')
    await expect(editor(page).getByRole('button', { name: 'Remove level extended-level' })).toBeVisible()
  })
  await inside.check(() => editor(page).getByRole('button', { name: 'Remove level extended-level' }).click())
  await inside.check(() => editor(page).getByLabel('Name', { exact: true }).press('Escape'))
  inside.done()
  expect(world.writes.some(write => write.path.endsWith('/retire') || write.path.endsWith('/pause'))).toBe(false)
})

test('Check now reports a partial or stale discovery, and a taken-on version is not called a new model', async ({ page }) => {
  const world = await setup(page)
  await page.clock.install({ time: new Date('2026-10-09T08:00:00Z') })
  await page.clock.pauseAt(new Date('2026-10-09T08:00:01Z'))
  const check = card(page).locator('[data-reg-check]')
  const last = () => toasts(page).last()
  world.check = { status: 200, sources: [{ state: 'stale', vendor: 'openai' }] }
  await check.click()
  await expect(last()).toContainText('Discovery was incomplete: some model lists could not be checked')
  await expect(last()).not.toContainText('Up to date')
  await expect(last()).not.toContainText('nothing new')

  await page.clock.runFor('05:02')
  await expect(check).toHaveAccessibleName('Check now')
  world.check = { status: 200, added: 4, sources: [{ state: 'fresh' }] }
  await check.click()
  await expect(last()).toContainText('4 profiles were added')
  await expect(last()).not.toContainText('taken on')
  await expect(last()).not.toContainText('newer version')
  await expect(last()).not.toContainText('new model')
  await expect(last()).not.toContainText('nothing new')

  await page.clock.runFor('05:02')
  world.check = { status: 200, added: 4, acceptedModels: ['openai:sol'], sources: [{ state: 'fresh' }] }
  await check.click()
  await expect(last()).toContainText('A newer version already in use was taken on')
  await expect(last()).not.toContainText('4 newer')
  await expect(last()).not.toContainText('4 profiles')
  await expect(last()).not.toContainText('new model')

  await page.clock.runFor('05:02')
  world.check = { status: 200, newLines: ['openai:gpt-7'], added: 1, sources: [{ state: 'limited' }] }
  await check.click()
  await expect(last()).toContainText('1 new model found')
  await expect(last()).toContainText('Discovery was partial: not every model could be read')
  await expect(last()).not.toContainText('taken on')
  await expect(last()).not.toContainText('newer version')

  await page.clock.runFor('05:02')
  await expect(check).toHaveAccessibleName('Check now')
  world.check = { status: 200, sources: [{ state: 'limited' }, { state: 'stale' }] }
  await check.click()
  await expect(last()).toContainText('Discovery was partial: some lists were incomplete and some could not be checked')
  await expect(last()).not.toContainText('Up to date')
})

test('Edit binds the revision to the line read with it when that line changed before the revision was captured', async ({ page }) => {
  const world = await setup(page)
  let release = () => {}
  world.gateUsage = new Promise<void>(resolve => { release = resolve })
  await open(page, QWEN)
  const opening = body(page, QWEN).getByRole('button', { name: 'Edit pi route' }).click()
  await expect.poll(() => world.reads.some(path => path.endsWith('/models/lines/pi/openrouter/qwen/qwen3-coder/usage'))).toBe(true)
  const qwen = world.profiles.find(profile => profile.model === 'openrouter/qwen/qwen3-coder' && !profile.retired)!
  qwen.note = 'changed before capture'
  world.profiles.push({ ...qwen, id: '00000000-0000-4000-8000-0000000000aa', effort: 'low', effort_level: 1 })
  world.usageRevision = 'r-captured'
  release()
  await opening
  const note = editor(page).getByLabel('Note', { exact: true })
  await expect(note).toHaveValue('changed before capture')
  await expect(editor(page).getByRole('button', { name: 'Remove level low' })).toBeVisible()
  await editor(page).getByLabel('Name', { exact: true }).fill('Qwen3 Coder renamed')
  await editor(page).getByRole('button', { name: /^Save/ }).click()
  await expect(editor(page)).toHaveCount(0)
  expect(world.writes[0]).toEqual({ method: 'PUT', path: '/models/lines/pi/openrouter/qwen/qwen3-coder', body: { display_name: 'Qwen3 Coder renamed', note: 'changed before capture', efforts: ['off', 'low'], route: 'openrouter', revision: 'r-captured' } })
  await expect(toasts(page).filter({ hasText: 'Saved' }).last()).toBeVisible()
  await expect(toasts(page).filter({ hasText: 'Saved' }).last()).not.toContainText('changed somewhere else')
})

test('Edit sends the revision captured with the form, Undo sends the revision the edit returned, and a conflict reloads', async ({ page }) => {
  const world = await setup(page)
  await open(page, QWEN)
  await body(page, QWEN).getByRole('button', { name: 'Edit pi route' }).click()
  await editor(page).getByLabel('Note', { exact: true }).fill('best for scripts')
  await editor(page).getByRole('button', { name: /^Save/ }).click()
  await expect(editor(page)).toHaveCount(0)
  expect(world.writes[0]).toEqual({ method: 'PUT', path: '/models/lines/pi/openrouter/qwen/qwen3-coder', body: { display_name: 'Qwen3 Coder', note: 'best for scripts', efforts: ['off'], route: 'openrouter', revision: 'r1' } })
  await toasts(page).getByRole('button', { name: 'Undo' }).click()
  await expect(fold(page, QWEN).locator('.ln-m')).toHaveText('pi · trial for small scripts (AEON-1003)')
  const undone = world.writes.at(-1)!
  expect(undone).toMatchObject({ method: 'PUT', path: '/models/lines/pi/openrouter/qwen/qwen3-coder', body: { note: 'trial for small scripts (AEON-1003)', revision: 'r2' } })

  const lists = () => world.reads.filter(path => path === '/models').length
  await body(page, QWEN).getByRole('button', { name: 'Edit pi route' }).click()
  await editor(page).getByLabel('Note', { exact: true }).fill('changed elsewhere')
  const before = lists()
  const savedBefore = await toasts(page).filter({ hasText: 'Saved' }).count()
  world.conflictPut = true
  await editor(page).getByRole('button', { name: /^Save/ }).click()
  await expect(toasts(page).last()).toContainText('This model changed somewhere else. The list was reloaded; review it before saving again.')
  expect(await toasts(page).filter({ hasText: 'Saved' }).count()).toBe(savedBefore)
  await expect(editor(page)).toHaveCount(0)
  await expect(fold(page, QWEN).locator('.ln-m')).toHaveText('pi · trial for small scripts (AEON-1003)')
  expect(lists()).toBe(before + 1)
  expect(world.writes.at(-1)).toMatchObject({ method: 'PUT', body: { note: 'changed elsewhere', revision: 'r1' } })
})

test('Remove names each use’s own replacement and effort, and says when there is no qualified fallback', async ({ page }) => {
  await setup(page, { usage: { incomplete: false, used_by: [
    { column: 'other', layer: 'default', replacement: { line: 'openai:astra', effort: 'xhigh', harness: 'codex', model: 'gpt-6-astra' } },
    { column: 'backend', layer: 'workspace', replacement: { line: null, effort: null } },
    { column: 'concept', layer: 'workspace', replacement: { line: 'anthropic:opus', effort: 'high', harness: 'claude', model: 'claude-opus-5-5' } },
  ] } })
  await open(page, SOL)
  await body(page, SOL).getByRole('button', { name: 'Remove…' }).click()
  const ask = confirm(page, SOL)
  await expect(ask).toContainText('GPT-6.1 Sol is in use: Default · all work (for everyone) — GPT-6 Astra at xhigh takes over; Backend build (for everyone) — no qualified fallback; Concepts (for everyone) — Opus 5.5 at high takes over. Auto-update won’t add it back.')
  await expect(ask).not.toContainText('The next model in line')
})

test('thinking levels growing or shrinking do not move the input, note or actions at desktop or phone width', async ({ page }) => {
  await setup(page)
  const level = () => editor(page).locator('[data-reg-level]')
  const note = () => editor(page).locator('[data-reg-note]')
  const cancel = () => editor(page).getByRole('button', { name: /^Cancel/ })
  const save = () => editor(page).getByRole('button', { name: /^Save|^Add/ })
  const commit = async (name: string) => {
    await level().fill(name)
    await level().press('Enter')
    await expect(editor(page).getByRole('button', { name: `Remove level ${name}` })).toBeVisible()
  }
  await card(page).getByRole('button', { name: 'Add model' }).click()
  const desktopAdd = await controlStability(page, { level: level(), note: note(), cancel: cancel(), save: save() })
  await desktopAdd.check(() => commit('very-long-level-name-one'))
  await desktopAdd.check(() => commit('very-long-level-name-two'))
  await desktopAdd.check(() => commit('very-long-level-name-three'))
  await desktopAdd.check(() => editor(page).getByRole('button', { name: 'Remove level very-long-level-name-one' }).click())
  await desktopAdd.check(() => editor(page).getByRole('button', { name: 'Remove level very-long-level-name-three' }).click())
  desktopAdd.done()

  await cancel().click()
  await open(page, QWEN)
  await body(page, QWEN).getByRole('button', { name: 'Edit pi route' }).click()
  const desktopEdit = await controlStability(page, { level: level(), note: note(), cancel: cancel(), save: save() })
  await desktopEdit.check(() => commit('desktop-extra-level'))
  await desktopEdit.check(() => editor(page).getByRole('button', { name: 'Remove level off' }).click())
  await desktopEdit.check(() => editor(page).getByRole('button', { name: 'Remove level desktop-extra-level' }).click())
  desktopEdit.done()

  await page.setViewportSize({ width: 400, height: 1200 })
  const phone = await controlStability(page, { level: level(), note: note(), cancel: cancel(), save: save() })
  await phone.check(() => commit('phone-level-alpha'))
  await phone.check(() => commit('phone-level-beta-long'))
  await phone.check(() => editor(page).getByRole('button', { name: 'Remove level phone-level-alpha' }).click())
  await phone.check(() => editor(page).getByRole('button', { name: 'Remove level phone-level-beta-long' }).click())
  phone.done()
})

// AEON-1163 acceptance: about 35 routes show as at most 20 rows; Cursor Grok 4.7 is one route; no row says
// "Auto-discovered"; provider facts are read-only. The world is the live registry's spread (36 harness/model ids).
test('many models stay calm: about 35 routes as at most 20 rows, Cursor\'s Grok levels as one route, older versions folded with their levels', async ({ page }) => {
  await page.clock.install({ time: new Date('2026-10-11T08:00:00Z') })
  const world = await mockRegistry(page, registryWorld({ profiles: manyProfiles() }))
  await openRegistry(page)
  await expect(row(page, GROK)).toBeVisible()
  expect(new Set(world.profiles.map(profile => `${profile.harness}/${profile.model}`)).size).toBe(36)
  await expect(page.locator('[data-reg-row]')).toHaveCount(18)
  await expect(card(page).locator('[data-reg-count]')).toHaveText('23 models · 30 routes · 18 lines')
  await expect(card(page)).not.toContainText('Auto-discovered')
  await expect(fold(page, GROK).locator('.tag-oldn')).toHaveText('+2 older')
  await open(page, GROK)
  await expect(body(page, GROK).locator('[data-reg-route]')).toHaveCount(2)
  const cursor = route(page, GROK, 'cursor:grok-4.7-‹level›')
  await expect(cursor.locator('.rt-s')).toHaveText('grok-4.7-‹level›')
  await expect(cursor.locator('.lv span')).toHaveText(['low', 'medium', 'high', 'xhigh'])
  await expect(cursor).toContainText('4 ids in Cursor, one per level')
  const older = body(page, GROK).locator('[data-reg-older-version]')
  await expect(older.locator('b')).toHaveText(['Grok 4.6', 'Grok 4.5'])
  await expect(older.first().locator('.lv span')).toHaveText(['high', 'xhigh'])
  await expect(older.first()).toContainText('Grok · Newer version in the same line')
  // Only routes added by hand can be edited; a paused model says until when.
  await expect(body(page, GROK).locator('[data-reg-edit]')).toHaveCount(0)
  await open(page, QWEN)
  await expect(body(page, QWEN).locator('[data-reg-edit]')).toHaveText(['Edit pi route'])
  await expect(fold(page, 'google:gemini-flash').locator('[data-reg-paused]')).toHaveText('Paused until 14 Oct')
  await open(page, 'google:gemini-pro')
  await expect(route(page, 'google:gemini-pro', 'gemini:gemini-2.5-pro').locator('.lv')).toHaveText(/1024\s*8192\s*24576\s*32768\s*tokens/)
})

test('How new models are found: a quiet fold whose settings never touch auto-update, with vendor keys and models that wait', async ({ page }) => {
  const world = await setup(page, {
    permissions: ['models.read', 'models.manage', 'models.refresh', 'account.use.manage', 'account.read'],
    accounts: [{ id: 'acc-codex', label: 'Codex main', harness: 'codex' }, { id: 'acc-cursor', label: 'Cursor', harness: 'cursor' }],
    observations: [{ harness: 'codex', model: 'gpt-7-sol', effort: 'high', pending: true, failures: 0, suppressed_until: null, last_working_at: null }],
  })
  const toggle = card(page).locator('[data-reg-discovery-fold]')
  await expect(toggle).toHaveText(/How new models are found\s*agent reports on · vendor lists on · every 6 h/)
  await expect(card(page).locator('[data-reg-discovery-body]')).toHaveCount(0)
  // The old Model catalog form and its second auto-update writer are gone.
  await expect(page.getByText('Automatically record discovered profiles')).toHaveCount(0)
  await toggle.click()
  const panel = card(page).locator('[data-reg-discovery-body]')
  await panel.getByRole('switch', { name: 'Vendor model lists' }).click()
  await expect.poll(() => world.writes.at(-1)).toEqual({ method: 'PUT', path: '/models/refresh/settings', body: { agent_reports_enabled: true, auto_add_profiles: true, api_enabled: false, interval_minutes: 360 } })
  await panel.getByLabel('Check every').selectOption({ label: '12 h' })
  await expect.poll(() => world.writes.at(-1)).toEqual({ method: 'PUT', path: '/models/refresh/settings', body: { agent_reports_enabled: true, auto_add_profiles: true, api_enabled: false, interval_minutes: 720 } })
  await expect(card(page).locator('[data-reg-when]')).toHaveText(/checks every 12 h$/)
  // A failed save puts the switch back and says so.
  world.failSettings = true
  await panel.getByRole('switch', { name: 'Models agents report' }).click()
  await expect(panel.locator('[data-reg-discovery-error]')).toHaveText('It could not be saved. Try again.')
  await expect(panel.getByRole('switch', { name: 'Models agents report' })).toBeChecked()
  world.failSettings = false
  // A vendor key for the account's vendor is sent once and never shown again.
  await expect(panel.getByLabel('Account').locator('option')).toHaveText(['Codex main · Codex'])
  await panel.getByLabel('OpenAI API key').fill('test-key-value')
  await panel.getByRole('button', { name: 'Save key' }).click()
  await expect(panel.getByText('A key is saved for this account. It is never shown again.')).toBeVisible()
  expect(world.writes.find(write => write.path === '/models/refresh/credentials/acc-codex')).toEqual({ method: 'PUT', path: '/models/refresh/credentials/acc-codex', body: { vendor: 'openai', api_key: 'test-key-value' } })
  await expect(panel.getByLabel('OpenAI API key')).toHaveValue('')
  await panel.getByRole('button', { name: 'Remove key' }).click()
  await expect(panel.getByText('No key is saved for this account.')).toBeVisible()
  // A model a harness reported waits for a person; Accept lets it run.
  await expect(panel.locator('[data-reg-waiting]')).toHaveText([/Codex · gpt-7-sol · high\s*Accept/])
  await panel.getByRole('button', { name: 'Accept' }).click()
  await expect(panel.locator('[data-reg-waiting]')).toHaveCount(0)
  expect(world.writes.find(write => write.path === '/models/proposals/accept')?.body).toEqual({ harness: 'codex', model: 'gpt-7-sol', effort: 'high' })
  expect(world.writes.filter(write => write.path === '/models/refresh/settings').every(write => !('account_use_revision' in (write.body as object)))).toBe(true)
})
