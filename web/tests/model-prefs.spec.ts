// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { fixtures, liveAgent, mockWork } from './work-fixtures'
import { prefsDocument, prefsFixture, PREF_MODELS, prefScope, customKind } from './model-prefs-fixtures'
import type { ModelPreferences, PrefLevel, PrefRow } from '../src/lib/modelPrefs'
import { expectStableControls } from './helpers/stable'

async function setup(page: Page, options: { mode?: ModelPreferences['residency_lock_mode']; readOnly?: boolean; conflict?: boolean } = {}) {
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
  await page.route('**/api/models**', route => route.fulfill({ json: new URL(route.request().url()).pathname.endsWith('/resolve') ? { ladder: PREF_MODELS.filter(p => p.effort === 'xhigh').map(p => ({ profile_id: p.id, selected: false, skip_reasons: [] })) } : PREF_MODELS }))
  await page.route('**/api/model-preferences**', async route => {
    const url = new URL(route.request().url()), method = route.request().method(), path = url.pathname, project = url.searchParams.has('project_id')
    if (method === 'GET') return route.fulfill({ json: prefsDocument(state, project) })
    const body = method === 'PUT' ? route.request().postDataJSON() : undefined
    calls.push({ method, path, body })
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
    return route.fulfill({ json: { level: state.levels[level], revision: state.levels[level]!.revision, running_outside: [], residency: prefsDocument(state, project).views[level]!.residency } })
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
    const accents = await modal.locator('.kind-row').evaluateAll(elements => elements.map(el => { const s = getComputedStyle(el); return [s.borderLeftWidth, s.borderTopWidth] }))
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
  await expect(codex).toContainText('Codex is not qualified')
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
  await modelLink.click()
  await expect(dialog(page).getByRole('heading', { name: 'Why this model?' })).toBeVisible()
  await expect(row(page, 'backend')).toBeVisible()

})

for (const mode of ['freeze', 'tighten_only', 'warn'] as const) test(`provider locks follow ${mode}`, async ({ page }) => {
  const { state } = await setup(page, { mode })
  state.levels.default!.residency = 'eu'; state.levels.default!.residency_locked = true
  await open(page)
  const modal = dialog(page), any = modal.getByRole('radio', { name: /Any provider/ }), local = modal.getByRole('radio', { name: /Local only/ })
  if (mode === 'warn') {
    await expect(any).toBeEnabled(); await any.click()
    await expect(modal.locator('.warning')).toContainText('Looser than the lock')
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
