// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { fixtures, liveAgent, mockWork } from './work-fixtures'
import { prefsDocument, prefsFixture, PREF_MODELS, prefScope, customKind } from './model-prefs-fixtures'
import type { ModelPreferences, PrefLevel, PrefRow } from '../src/lib/modelPrefs'
import { expectStableControls } from './helpers/stable'

async function setup(page: Page, options: { mode?: ModelPreferences['residency_lock_mode']; readOnly?: boolean; conflict?: boolean; runningOutside?: string[]; beforeWrite?: () => Promise<void>; truncated?: boolean } = {}) {
  const data = fixtures()
  data.preferences['list:p-pharos'] = { visible: ['status', 'model'] }
  const ticket = data.nodes.find(n => n.key === 'PHAROS-11')!
  ticket.fields.area = 'backend'
  ticket.planning = { route: { label: 'Codex Sol', profile: 'model-codex-high', harness: 'codex', model: 'gpt-6.1-sol', effort: 'high', revision: '1' }, tokens: { spent: null, input: 0, output: 0, cached: 0, sessions: 0, unreported: 0, estimated: null } }
  data.live.push(liveAgent({ project_id: 'p-aeon', name: 'worker', session_id: 'prefs-worker' }))
  await mockWork(page, data, { admin: true })
  const state = prefsFixture(), calls: { method: string; path: string; body: unknown }[] = []
  state.residency_lock_mode = options.mode ?? 'warn'
  if (options.readOnly) state.can.edit_default = false
  let conflict = options.conflict ?? false
  await page.route('**/api/models**', route => {
    const url = new URL(route.request().url())
    if (url.pathname.endsWith('/resolve') && url.searchParams.get('ticket') === ticket.id) return route.fulfill({ json: {
      profile: PREF_MODELS[3], role: 'build-hard', owner_required: false, residency: 'any',
      trace: { kind: 'backend', kind_source: 'ticket', complexity: 'L', complexity_source: 'agent', bucket: 'complex', set_by: 'project', mode: 'pinned',
        residency: { value: 'any', set_by: 'person', qualifying_routes: 4, loosened_lock: false }, fallback: 'preferred profile unavailable' },
    } })
    return route.fulfill({ status: 503, json: { error: 'Redundant model catalog and reviewer-ladder requests must not be needed' } })
  })
  await page.route('**/api/model-preferences**', async route => {
    const url = new URL(route.request().url()), method = route.request().method(), path = url.pathname, project = url.searchParams.has('project_id')
    if (method === 'GET') { const doc = prefsDocument(state, project); for (const view of Object.values(doc.views)) if (view) view.choices_truncated = options.truncated ?? false; return route.fulfill({ json: doc }) }
    const body = method === 'PUT' ? route.request().postDataJSON() : undefined
    calls.push({ method, path, body })
    await options.beforeWrite?.()
    if (conflict) { conflict = false; state.levels.person!.revision++; return route.fulfill({ status: 409, json: { error: 'stale_revision', code: 'stale_revision' } }) }
    const level = path.split('/')[4] as PrefLevel, kind = path.split('/')[6], scope = state.levels[level]!
    const revision = method === 'PUT' ? body.revision : Number(url.searchParams.get('revision'))
    if (revision !== scope.revision) return route.fulfill({ status: 409, json: { code: 'stale_revision' } })
    if (kind) {
      scope.rows = scope.rows.filter(row => row.kind_id !== kind)
      if (method === 'PUT') { const inherited = prefsDocument(state, project).views[level]!.rows.find(r => r.kind_id === kind)!; scope.rows.push({ kind_id: kind, locked: body.locked ?? false, normal: body.normal ?? inherited.normal.selector, complex: body.complex ?? inherited.complex.selector } satisfies PrefRow) }
    } else if (method === 'DELETE') state.levels[level] = { ...prefScope(), revision: scope.revision }
    else Object.assign(scope, body)
    state.levels[level]!.revision++
    return route.fulfill({ json: { level: state.levels[level], revision: state.levels[level]!.revision, running_outside: options.runningOutside ?? [], residency: prefsDocument(state, project).views[level]!.residency } })
  })
  await page.route('**/api/work-kinds**', route => {
    const method = route.request().method(), path = new URL(route.request().url()).pathname
    if (method === 'POST') { const body = route.request().postDataJSON(); if (state.kinds.some(k => k.label === body.label)) return route.fulfill({ status: 409, json: { code: 'slug_taken' } }); const kind = customKind('new-kind', body.label, body.project_id); state.kinds.push(kind); return route.fulfill({ status: 201, json: kind }) }
    const kind = state.kinds.find(k => k.id === path.split('/').at(-1))!; kind.archived_at = '2026-10-02T00:00:00Z'; return route.fulfill({ json: kind })
  })
  return { state, calls }
}
const dialog = (page: Page) => page.getByRole('dialog', { name: 'Which models do which work', exact: true })
const row = (page: Page, kind: string) => dialog(page).locator(`[data-kind="${kind}"]`)
async function open(page: Page) { await page.goto('/agents'); await page.getByRole('button', { name: 'Model preferences', exact: true }).click(); await expect(row(page, 'backend')).toBeVisible() }
async function changeModel(page: Page) {
  await row(page, 'backend').getByRole('button', { name: 'Backend, normally: Automatic', exact: true }).click()
  const picker = page.getByRole('dialog', { name: 'Backend · Normally', exact: true })
  await expect(picker).toBeVisible()
  const claude = picker.locator('.picker-line').filter({ has: page.getByRole('heading', { name: /Claude Fable/ }) })
  const body = picker.locator(':scope > .card > .body')
  const fixedFrame = page.viewportSize()!.width <= 600 || await body.evaluate(el => el.scrollHeight > el.clientHeight)
  await expectStableControls({ controls: { automatic: picker.locator('.automatic'), cancel: picker.getByRole('button', { name: 'Cancel Esc' }), efforts: claude.locator('.effort-options'), high: claude.getByRole('button', { name: 'high', exact: true }), versions: claude.locator('.version-options'), row: claude, ...(fixedFrame ? { frame: picker.locator(':scope > .card') } : {}) }, scrollAreas: { body }, interactions: [
    { name: 'extra high effort', run: async () => { await claude.getByRole('button', { name: 'xhigh', exact: true }).click(); await expect(claude.getByRole('button', { name: 'xhigh', exact: true })).toHaveAttribute('aria-pressed', 'true') } },
    { name: 'high effort', run: async () => { await claude.getByRole('button', { name: 'high', exact: true }).click(); await expect(claude.getByRole('button', { name: 'high', exact: true })).toHaveAttribute('aria-pressed', 'true') } },
  ] })
  await claude.getByRole('button', { name: 'Follows new versions', exact: true }).click()
  await expect(row(page, 'backend').getByRole('button', { name: 'Backend: Changed here', exact: true })).toBeVisible()
  await expect(dialog(page).locator('.save-status')).toHaveText('')
}
for (const theme of ['light', 'dark'] as const) for (const width of [1440, 1024, 390]) {
  test(`preferences keep their controls at ${width} in ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme: theme })
    const { calls } = await setup(page)
    await open(page)
    const modal = dialog(page), backend = row(page, 'backend')
    await expectStableControls({ controls: { done: modal.getByRole('button', { name: 'Done Esc' }), resetAll: modal.locator('.reset-all'), tabs: modal.getByRole('tablist'), selectorGroup: modal.getByRole('radiogroup'), any: modal.getByRole('radio', { name: /Any provider/ }), row: backend, normal: backend.locator('.model-chip').first(), complex: backend.locator('.model-chip').nth(1), frame: modal.locator(':scope > .card') }, scrollAreas: { body: modal.locator(':scope > .card > .body') }, interactions: [
      { name: 'change a model', run: () => changeModel(page) },
      { name: 'reset the row', run: async () => { await backend.getByRole('button', { name: 'Backend: Reset to default', exact: true }).click(); await expect(backend.getByRole('button', { name: 'Backend, normally: Automatic', exact: true })).toBeVisible(); await expect(modal.locator('.save-status')).toHaveText('') } },
      { name: 'tighten providers', run: async () => { await modal.getByRole('radio', { name: /EU-hosted only/ }).click(); await expect(modal.getByRole('radio', { name: /EU-hosted only/ })).toHaveAttribute('aria-checked', 'true'); await expect(modal.locator('.save-status')).toHaveText('') } },
      { name: 'local providers', run: async () => { await modal.getByRole('radio', { name: /Local only/ }).click(); await expect(modal.getByRole('radio', { name: /Local only/ })).toHaveAttribute('aria-checked', 'true'); await expect(modal.locator('.save-status')).toHaveText('') } },
      { name: 'restore providers', run: async () => { await modal.getByRole('radio', { name: /Any provider/ }).click(); await expect(modal.getByRole('radio', { name: /Any provider/ })).toHaveAttribute('aria-checked', 'true'); await expect(modal.locator('.save-status')).toHaveText('') } },
    ] })
    await expectStableControls({ controls: { done: modal.getByRole('button', { name: 'Done Esc' }), resetAll: modal.locator('.reset-all'), tabs: modal.getByRole('tablist'), selectorGroup: modal.getByRole('radiogroup'), row: backend, normal: backend.locator('.model-chip').first(), complex: backend.locator('.model-chip').nth(1), frame: modal.locator(':scope > .card') }, scrollAreas: { body: modal.locator(':scope > .card > .body') }, interactions: [
      { name: 'default level', run: async () => { await modal.getByRole('tab', { name: 'Default', exact: true }).click(); await expect(modal.getByRole('tab', { name: 'Default', exact: true })).toHaveAttribute('aria-selected', 'true') } },
      { name: 'your level', run: async () => { await modal.getByRole('tab', { name: 'You', exact: true }).click(); await expect(modal.getByRole('tab', { name: 'You', exact: true })).toHaveAttribute('aria-selected', 'true') } },
    ] })
    expect(calls.filter(c => c.method === 'PUT').length).toBeGreaterThanOrEqual(3)
    const accents = await modal.locator('.kind-row, .kind-row > th, .kind-row > td').evaluateAll(elements => elements.map(el => { const s = getComputedStyle(el); return [s.borderLeftWidth, s.borderTopWidth] }))
    expect(accents.every(widths => widths.every(v => parseFloat(v) <= 1))).toBe(true)
    expect((await new AxeBuilder({ page }).include('.model-prefs-dialog').analyze()).violations).toEqual([])
    if (process.env.MODEL_PREFS_SHOTS) { mkdirSync(process.env.MODEL_PREFS_SHOTS, { recursive: true }); await page.screenshot({ path: join(process.env.MODEL_PREFS_SHOTS, `preferences-${width}-${theme}.png`) }) }
  })
}
test('project gear, project-only kinds, locks, reviews, add/remove and planning explanation', async ({ page }) => {
  await setup(page)
  await page.goto('/')
  await page.locator('[data-project-id="p-aeon"] .live-chip').click()
  await page.getByRole('button', { name: 'Model preferences for Aeon', exact: true }).click()
  const modal = dialog(page)
  await expect(modal.getByRole('tab', { name: 'Project Aeon' })).toHaveAttribute('aria-selected', 'true')
  await expect(row(page, 'firmware')).toBeVisible()
  await modal.getByRole('tab', { name: 'You', exact: true }).click()
  await expect(row(page, 'firmware')).toHaveCount(0)
  await expect(modal.getByRole('button', { name: /Add a kind/ })).toHaveCount(0)
  await row(page, 'backend').getByRole('button', { name: 'Backend: Default', exact: true }).focus()
  await page.keyboard.press('Enter')
  await page.getByRole('menuitem', { name: 'Lock for projects' }).press('Enter')
  await expect(row(page, 'backend').getByRole('button', { name: 'Backend: Locked by you' })).toBeVisible()
  await row(page, 'backend').getByRole('button', { name: 'Backend: Locked by you' }).click({ modifiers: ['Alt'] })
  await expect(row(page, 'backend').getByRole('button', { name: 'Backend: Changed here' })).toBeVisible()
  await row(page, 'review').getByRole('button', { name: 'Reviews, normally: Another family', exact: true }).click()
  const reviews = page.getByRole('dialog', { name: 'Reviews · Normally', exact: true })
  const codex = reviews.locator('.picker-line').filter({ has: page.getByRole('heading', { name: /Codex Sol/ }) })
  await expect(codex.getByRole('button', { name: 'Follows new versions' })).toBeDisabled()
  await expect(codex).toContainText('Codex read-only sandboxing does not isolate inherited MCP tools and startup hooks.')
  await expect(reviews.getByRole('button', { name: '5', exact: true })).toHaveCount(0)
  await reviews.getByRole('button', { name: 'Cancel Esc' }).click()
  await modal.getByRole('tab', { name: 'Default', exact: true }).click()
  await expect(modal.getByRole('button', { name: 'Remove Reviews' })).toHaveCount(0)
  await modal.getByRole('button', { name: /Add a kind/ }).click()
  const field = modal.getByRole('textbox', { name: 'Name', exact: true })
  await field.fill('Data science')
  const mac = await page.evaluate(() => /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent))
  await field.press(mac ? 'Meta+Enter' : 'Control+Enter')
  await expect(row(page, 'data-science')).toBeVisible()
  await row(page, 'data-science').getByRole('button', { name: 'Remove Data science' }).click()
  await modal.getByRole('button', { name: 'Remove kind', exact: true }).click()
  await expect(row(page, 'data-science')).toHaveCount(0)
  await modal.getByRole('button', { name: 'Done Esc' }).click()
  await page.goto('/p/PHAROS')
  const modelLink = page.getByRole('button', { name: /Why this model\?/ }).first()
  await expect(modelLink).toBeVisible()
  await expect(modelLink).toHaveAttribute('tabindex', '-1')
  await modelLink.click()
  await expect(dialog(page).getByRole('heading', { name: 'Why this model?' })).toBeInViewport()
  const why = dialog(page).locator('.why')
  await expect(why).toContainText('If it’s complex (L), set by an agent')
  await expect(why).toContainText('This project selects a pinned model')
  await expect(why).toContainText('Allowed providers: Any provider, set by You')
  await expect(why).toContainText('Automatic fallback: preferred profile unavailable')
  await expect(why).toContainText('Runs Claude Fable 5.1 · xhigh today via claude')
  await expect(row(page, 'backend')).toBeVisible()

})

for (const mode of ['freeze', 'tighten_only', 'warn'] as const) test(`provider locks follow ${mode}`, async ({ page }) => {
  const { state } = await setup(page, { mode })
  state.levels.default!.residency = 'eu'; state.levels.default!.residency_locked = true
  await open(page)
  const modal = dialog(page), any = modal.getByRole('radio', { name: /Any provider/ }), local = modal.getByRole('radio', { name: /Local only/ })
  if (mode === 'warn') {
    await expect(any).toBeEnabled(); await any.click()
    await expect(modal.locator('.providers .warning')).toContainText('Looser than the lock')
    await expect(modal.locator('.providers .warning')).toBeInViewport()
    await expect(any).toBeFocused()
  } else await expect(any).toBeDisabled()
  if (mode === 'freeze') await expect(local).toBeDisabled(); else await expect(local).toBeEnabled()
})
test('conflicts refresh honestly and read-only default keeps its explanation', async ({ page }) => {
  await setup(page, { conflict: true, readOnly: true }); await open(page)
  await dialog(page).getByRole('radio', { name: /EU-hosted only/ }).click()
  await expect(dialog(page).locator('.save-status')).toHaveText('Changed elsewhere, refreshed')
  await dialog(page).getByRole('tab', { name: 'Default', exact: true }).click()
  await expect(dialog(page)).toContainText('Only workspace admins change the default')
  await expect(dialog(page).getByRole('radio', { name: /Any provider/ })).toBeDisabled()
})

test('provider-change notices link to the affected queued run', async ({ page }) => {
  const id = 'ab000000-0000-4000-8000-000000000001'
  await setup(page, { runningOutside: [id] })
  await page.route('**/api/runs?*', route => route.fulfill({ json: { items: [{ id, agent_principal_id: 'agent', work_order_id: 'n-a1', status: 'starting', created_at: new Date().toISOString(), requested_model: 'gpt-6.1-sol' }], next_cursor: null } }))
  await page.route('**/api/harness-sessions?*', route => route.fulfill({ json: { items: [], next_cursor: null } }))
  await open(page)
  await dialog(page).getByRole('radio', { name: /EU-hosted only/ }).click()
  await expect(dialog(page).locator('.providers .outside')).toContainText('1 agent is still running')
  await expect(dialog(page).locator('.outside')).toBeInViewport()
  await expect(dialog(page).locator('.outside')).toContainText('Nothing is stopped automatically')
  await page.evaluate(() => { (window as typeof window & { prefsNavigation: string }).prefsNavigation = 'same document' })
  await dialog(page).getByRole('link', { name: `Show run ${id.slice(0, 8)}`, exact: true }).click()
  await expect(page.locator(`#run-${id}`)).toBeFocused()
  expect(await page.evaluate(() => (window as typeof window & { prefsNavigation: string }).prefsNavigation)).toBe('same document')
})

test('Escape leaves a field before closing and browser shortcuts remain native', async ({ page }) => {
  await setup(page); await open(page)
  const modal = dialog(page)
  await modal.getByRole('tab', { name: 'Default', exact: true }).click()
  await modal.getByRole('button', { name: /Add a kind/ }).click()
  const field = modal.getByRole('textbox', { name: 'Name', exact: true })
  await field.fill('Native shortcuts')
  const mac = await page.evaluate(() => /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent))
  const mod = mac ? 'Meta' : 'Control'
  await field.press(`${mod}+a`)
  expect(await field.evaluate(el => { const input = el as HTMLInputElement; return input.selectionEnd! - input.selectionStart! })).toBe('Native shortcuts'.length)
  await page.evaluate(() => {
    const target = document.activeElement!
    ;(window as typeof window & { prefsNative: boolean[] }).prefsNative = ['s', 'r', 'd', 'p'].map(key => target.dispatchEvent(new KeyboardEvent('keydown', { key, metaKey: /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent), ctrlKey: !/Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent), bubbles: true, cancelable: true })))
  })
  expect(await page.evaluate(() => (window as typeof window & { prefsNative: boolean[] }).prefsNative)).toEqual([true, true, true, true])
  await field.press('Escape'); await expect(field).not.toBeFocused(); await expect(modal).toBeVisible()
  await page.keyboard.press('Escape'); await expect(modal).toHaveCount(0)
})

for (const width of [1440, 1024, 390]) test(`confirmations stay by their actions at ${width}`, async ({ page }) => {
  await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
  const { state } = await setup(page)
  state.levels.person!.rows = [{ kind_id: 'backend', locked: false, normal: { mode: 'pinned', profile_id: PREF_MODELS[2]!.id }, complex: { mode: 'auto' } }]
  await open(page)
  const modal = dialog(page), footer = modal.locator('.prefs-footer'), body = modal.locator(':scope > .card > .body')
  await expectStableControls({ controls: { reset: footer.locator('.reset-all'), finalAction: footer.locator('.done'), tabs: modal.getByRole('tablist'), frame: modal.locator(':scope > .card') }, scrollAreas: { body }, interactions: [
    { name: 'show reset confirmation', run: async () => {
      await footer.getByRole('button', { name: 'Reset all your changes', exact: true }).click()
      await expect(footer.getByRole('button', { name: 'Confirm reset' })).toBeInViewport()
      await expect(footer.getByRole('button', { name: 'Keep' })).toBeFocused()
    } },
    { name: 'keep current settings', run: async () => {
      await footer.getByRole('button', { name: 'Keep' }).click()
      await expect(footer.getByRole('button', { name: 'Reset all your changes' })).toBeFocused()
    } },
  ] })
  const done = footer.getByRole('button', { name: 'Done Esc' })
  expect(await done.evaluate(el => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1)
  await modal.getByRole('tab', { name: 'Default', exact: true }).click()
  const backend = row(page, 'backend')
  await expectStableControls({ controls: { row: backend, remove: backend.getByRole('button', { name: 'Remove Backend', exact: true }), models: backend.locator('.model-chip').first(), footer, frame: modal.locator(':scope > .card') }, scrollAreas: { body }, interactions: [
    { name: 'show inline removal', run: async () => {
      await backend.getByRole('button', { name: 'Remove Backend', exact: true }).click()
      const confirmation = backend.locator('+ .remove-confirmation')
      await expect(confirmation).toBeInViewport()
      await expect(confirmation.getByRole('button', { name: 'Keep' })).toBeFocused()
    } },
    { name: 'keep the kind', run: async () => {
      await modal.locator('.remove-confirmation').getByRole('button', { name: 'Keep' }).click()
      await expect(backend.getByRole('button', { name: 'Remove Backend', exact: true })).toBeFocused()
    } },
  ] })
})

test('saving retains keyboard focus and guards controls while the write is held', async ({ page }) => {
  let release!: () => void
  let held = new Promise<void>(done => { release = done })
  const { calls } = await setup(page, { beforeWrite: () => held })
  await open(page)
  const modal = dialog(page), radio = modal.getByRole('radio', { name: /EU-hosted only/ })
  await radio.focus(); await radio.press('Space')
  await expect(radio).toBeFocused(); await expect(radio).toHaveAttribute('aria-disabled', 'true')
  const tab = modal.getByRole('tab', { name: 'You', exact: true })
  await tab.focus(); await tab.press('Enter'); await expect(tab).toBeFocused()
  await expect(tab).toHaveAttribute('aria-selected', 'true')
  release(); await expect(radio).toHaveAttribute('aria-disabled', 'false'); await expect(tab).toBeFocused()
  // Restore unrestricted providers, then hold the model save and its opener.
  await modal.getByRole('radio', { name: /Any provider/ }).click(); await expect(modal.locator('.save-status')).toHaveText('')
  held = new Promise<void>(done => { release = done })
  const chip = row(page, 'backend').locator('.model-chip').first()
  await chip.focus(); await chip.press('Enter')
  const picker = page.getByRole('dialog', { name: 'Backend · Normally', exact: true })
  const claude = picker.locator('.picker-line').filter({ has: page.getByRole('heading', { name: /Claude Fable/ }) })
  await claude.getByRole('button', { name: '5.1', exact: true }).press('Enter')
  await expect(chip).toHaveAttribute('aria-disabled', 'true'); await expect(chip).toBeFocused()
  release(); await expect(chip).toHaveAttribute('aria-disabled', 'false'); await expect(chip).toBeFocused()
  await chip.press('Enter')
  await expect(picker.getByRole('button', { name: '5.1', exact: true })).toHaveAttribute('aria-pressed', 'true')
  await picker.getByRole('button', { name: 'Cancel Esc' }).click()
  held = new Promise<void>(done => { release = done })
  const badge = row(page, 'backend').locator('.set-by')
  await badge.focus(); await badge.press('Enter')
  await page.getByRole('menuitem', { name: 'Lock for projects' }).press('Enter')
  await expect(badge).toHaveAttribute('aria-disabled', 'true'); await expect(badge).toBeFocused()
  release(); await expect(badge).toHaveAttribute('aria-disabled', 'false'); await expect(badge).toBeFocused()
  expect(calls.filter(c => c.method === 'PUT')).toHaveLength(4)
})

test('ticket explanation uses the resolved complex column rather than the viewer matrix', async ({ page }) => {
  await setup(page); await page.goto('/p/PHAROS')
  await page.getByRole('button', { name: /Why this model\?/ }).first().click()
  const why = dialog(page).locator('.why')
  await expect(why).toContainText('If it’s complex (L), set by an agent')
  await expect(why).toContainText('This project selects a pinned model')
  await expect(why).toContainText('Automatic fallback: preferred profile unavailable')
  await expect(why).toContainText('Runs Claude Fable 5.1 · xhigh today via claude')
  await expect(why).toBeInViewport()
})

test('review picker works from server evidence when auxiliary catalogs are unavailable', async ({ page }) => {
  await setup(page); await open(page)
  await row(page, 'review').locator('.model-chip').first().click()
  const picker = page.getByRole('dialog', { name: 'Reviews · Normally', exact: true })
  const claude = picker.locator('.picker-line').filter({ has: page.getByRole('heading', { name: /Claude Fable/ }) })
  await expect(claude.getByRole('button', { name: 'Follows new versions' })).toBeEnabled()
  await expect(claude.getByRole('button', { name: '5.1', exact: true })).toBeEnabled()
})

test('remove confirmation is inline, visible and focused on Keep', async ({ page }) => {
  await setup(page); await open(page)
  const modal = dialog(page)
  await modal.getByRole('tab', { name: 'Default', exact: true }).click()
  const backend = row(page, 'backend')
  await backend.getByRole('button', { name: 'Remove Backend', exact: true }).click()
  await expect(backend.locator('+ .remove-confirmation')).toBeInViewport()
  await expect(backend.locator('+ .remove-confirmation').getByRole('button', { name: 'Keep' })).toBeFocused()
})

test('picker marks the selected pinned version', async ({ page }) => {
  const { state } = await setup(page)
  state.levels.person!.rows = [{ kind_id: 'backend', locked: false, normal: { mode: 'pinned', profile_id: PREF_MODELS[2]!.id }, complex: { mode: 'auto' } }]
  await open(page); await row(page, 'backend').locator('.model-chip').first().click()
  const picker = page.getByRole('dialog', { name: 'Backend · Normally', exact: true })
  await expect(picker.getByRole('button', { name: '5.1', exact: true })).toHaveAttribute('aria-pressed', 'true')
})

test('Done and its Escape keycap fit in the footer', async ({ page }) => {
  await setup(page); await open(page)
  const done = dialog(page).getByRole('button', { name: 'Done Esc' })
  expect(await done.evaluate(el => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1)
  const button = await done.boundingBox(), keycap = await done.locator('kbd').boundingBox()
  expect(button!.width).toBeGreaterThan(0); expect(keycap!.width).toBeGreaterThan(0)
  expect(keycap!.x + keycap!.width).toBeLessThanOrEqual(button!.x + button!.width - 5)
})

test('running-run links retain the document and use distinct run labels', async ({ page }) => {
  const id = 'ab000000-0000-4000-8000-000000000001'
  await setup(page, { runningOutside: [id] }); await open(page)
  await dialog(page).getByRole('radio', { name: /EU-hosted only/ }).click()
  const link = dialog(page).locator('.outside a').first()
  await page.evaluate(() => { (window as typeof window & { prefsNavigation: string }).prefsNavigation = 'same document' })
  await link.click(); await expect(page).toHaveURL(new RegExp(`run=${id}`))
  expect(await page.evaluate(() => (window as typeof window & { prefsNavigation: string }).prefsNavigation)).toBe('same document')
})

test('truncated picker evidence is explicit and does not claim that work waits', async ({ page }) => {
  await setup(page, { truncated: true }); await open(page)
  await dialog(page).getByRole('radio', { name: /EU-hosted only/ }).click()
  await expect(dialog(page).locator('.route-count')).toContainText('among the first 256 profiles; more choices exist')
  await expect(dialog(page).locator('.route-count')).not.toContainText('Work waits')
  await row(page, 'backend').locator('.model-chip').first().click()
  await expect(page.getByRole('dialog', { name: 'Backend · Normally', exact: true })).toContainText('Automatic still uses the full registry')
})

test('picker restores keyboard focus to the model cell during and after its save', async ({ page }) => {
  let release!: () => void
  const held = new Promise<void>(done => { release = done })
  const { calls } = await setup(page, { beforeWrite: () => held })
  try {
    await open(page)
    const chip = row(page, 'backend').locator('.model-chip').first()
    await chip.focus(); await chip.press('Enter')
    const picker = page.getByRole('dialog', { name: 'Backend · Normally', exact: true })
    await picker.getByRole('button', { name: '5.1', exact: true }).press('Enter')
    await expect.poll(() => calls.length).toBe(1)
    await expect(chip).toBeFocused()
    release(); await expect(dialog(page).locator('.save-status')).toHaveText('')
    await expect(chip).toBeFocused()
  } finally { release() }
})
