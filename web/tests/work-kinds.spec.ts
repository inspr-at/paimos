// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { expectStableControls } from './helpers/stable'
import type { WorkKind } from '../src/lib/workKinds'

const row = (page: Page, slug = 'design') => page.locator(`[data-kind="${slug}"]`)
const editor = (page: Page) => page.getByRole('dialog', { name: /^(Edit ·|Bearbeiten ·|New kind of work|Neue Art von Arbeit)/ })
function seed(german = false): WorkKind[] {
  const entries = [
    ['design', 'UI design', 'UI-Design', 'Screens and interaction, designed as an HTML mock before any code.', 'Screens und Interaktion, als HTML-Mock gestaltet, bevor es Code gibt.', ['A mock for Settings › Models', 'The empty state of the inbox', 'The ticket view on a phone'], ['Ein Mock für Einstellungen › Modelle', 'Der leere Zustand des Posteingangs', 'Die Ticketansicht auf dem Telefon'], ['ui', 'ux', 'mock'], 41],
    ['frontend', 'Frontend build', 'Frontend-Build', 'Vue code that implements an approved design.', 'Vue-Code, der ein freigegebenes Design umsetzt.', ['Build the Models page from its mock', 'Fix a drop-down that closes too early', 'Add a keyboard shortcut'], ['Die Modelle-Seite nach ihrem Mock bauen', 'Ein Drop-down reparieren, das zu früh schließt', 'Ein Tastenkürzel ergänzen'], ['web', 'vue'], 128],
    ['backend', 'Backend build', 'Backend-Build', 'Go code, APIs, SQL and migrations behind the app.', 'Go-Code, APIs, SQL und Migrationen hinter der App.', ['Add GET /agent-accounts/usage', 'A migration for model rules', 'Fix a slow query'], ['GET /agent-accounts/usage ergänzen', 'Eine Migration für Modellregeln', 'Eine langsame Abfrage reparieren'], ['api', 'db'], 203],
    ['infra', 'Infrastructure', 'Infrastruktur', 'CI, Nix, deploys and the machines agents run on.', 'CI, Nix, Deploys und die Rechner, auf denen Agenten laufen.', ['Pin a new release in nixcfg', 'Speed up the CI cache'], ['Ein neues Release in nixcfg pinnen', 'Den CI-Cache beschleunigen'], ['ci', 'nix', 'deploy'], 57],
    ['docs', 'Docs and copy', 'Doku und Texte', 'Words people read: READMEs, guides, release notes and interface text.', 'Texte, die Menschen lesen: READMEs, Anleitungen, Release Notes und Oberflächentexte.', ['Release notes for 126', 'German labels for Settings'], ['Release Notes für 126', 'Deutsche Beschriftungen für Einstellungen'], ['docs', 'copy'], 34],
    ['security', 'Security', 'Sicherheit', 'Who may do what: sign-in, keys, permissions and audits.', 'Wer was darf: Anmeldung, Schlüssel, Rechte und Prüfprotokolle.', ['Rotate agent keys', 'Tighten a permission check'], ['Agentenschlüssel rotieren', 'Eine Rechteprüfung verschärfen'], ['security'], 19],
    ['other', 'Everything else', 'Alles andere', 'Any ticket no other kind describes, and every kind without its own column.', 'Jedes Ticket, das keine andere Art beschreibt, und jede Art ohne eigene Spalte.', ['Tidy up a script', 'A one-off data fix'], ['Ein Skript aufräumen', 'Eine einmalige Datenkorrektur'], [], 96],
  ] as const
  return entries.map((entry, position) => ({ id: `00000000-0000-4000-8000-${String(position + 1).padStart(12, '0')}`, slug: entry[0], label: entry[german ? 2 : 1], hint: entry[german ? 4 : 3], examples: [...entry[german ? 6 : 5]], labels: [...entry[7]], ticket_count: entry[8], position, ...(entry[0] === 'other' || entry[0] === 'security' ? { system: entry[0] } : {}) }))
}
async function open(page: Page, options: { member?: boolean; agent?: boolean; german?: boolean; fail?: boolean; width?: number; theme?: string; pagination?: boolean } = {}) {
  const data = { kinds: seed(options.german), limits: { small_hours: 2, fix_rounds: 3, revision: 7, set_by: null, set_at: null }, writes: [] as { path: string; method: string; body: unknown }[], fail: options.fail ?? false, manage: !options.member, reads: 0 }
  await page.setViewportSize({ width: options.width ?? 1440, height: 1000 })
  await mockWork(page, fixtures())
  const settings = settingsData(); settings.profile.locale = options.german ? 'de-AT' : 'en-GB'; await mockSettings(page, settings)
  await page.route('**/api/me', route => route.fulfill({ json: { principal: { id: settings.profile.principal_id, name: 'Markus Barta', kind: options.agent ? 'agent' : 'person', roles: ['admin'] }, tenant: { id: 'tenant', slug: 'inspr', name: 'INSPR' } } }))
  await page.route('**/api/me/permissions*', route => {
    const grants = mockEffectivePermissions(options.member ? 'member' : 'admin'); grants.workspace.permissions.push('models.read'); if (data.manage) grants.workspace.permissions.push('model_prefs.manage')
    return route.fulfill({ json: grants })
  })
  await page.route('**/api/work-kinds**', async route => {
    const req = route.request(), url = new URL(req.url()), path = url.pathname, method = req.method()
    if (method === 'GET') {
      data.reads++
      const middle = Math.ceil(data.kinds.length / 2), second = !!url.searchParams.get('cursor')
      return route.fulfill({ json: { items: options.pagination ? data.kinds.slice(second ? middle : 0, second ? undefined : middle) : data.kinds, next_cursor: options.pagination && !second ? data.kinds[middle - 1]!.id : null } })
    }
    const body = req.postData() ? req.postDataJSON() : undefined
    data.writes.push({ path, method, body })
    if (data.fail) return route.fulfill({ status: 500, json: { error: 'storage failed' } })
    if (path === '/api/work-kinds/order') {
      const slugs = body.slugs as string[]
      data.kinds = [...slugs.map((slug, position) => ({ ...data.kinds.find(kind => kind.slug === slug)!, position })), ...data.kinds.filter(kind => kind.archived_at)]
      return route.fulfill({ json: { items: data.kinds.filter(kind => !kind.archived_at), next_cursor: null } })
    }
    if (method === 'POST' && path === '/api/work-kinds') {
      const kind: WorkKind = { ...body, id: '00000000-0000-4000-8000-000000000999', slug: 'data-analysis', ticket_count: 0 }; data.kinds.push(kind)
      return route.fulfill({ status: 201, json: kind })
    }
    const id = path.split('/')[3], kind = data.kinds.find(kind => kind.id === id)!
    if (method === 'PATCH') Object.assign(kind, body)
    if (method === 'DELETE') kind.archived_at = '2026-10-07T13:00:00Z'
    if (path.endsWith('/restore')) kind.archived_at = null
    return route.fulfill({ json: kind })
  })
  await page.route('**/api/model-preferences/situations', route => {
    if (route.request().method() === 'PUT') {
      const body = route.request().postDataJSON(); data.writes.push({ path: '/api/model-preferences/situations', method: 'PUT', body })
      if (data.fail) return route.fulfill({ status: 409, json: { error: 'stale_revision' } })
      Object.assign(data.limits, body, { revision: data.limits.revision + 1 })
    }
    return route.fulfill({ json: data.limits })
  })
  await page.goto('/settings/kinds')
  await expect(row(page)).toContainText(options.german ? 'UI-Design' : 'UI design')
  await expect(page.locator('#sit-concept')).toContainText(options.german ? 'Konzept auf Anfrage' : 'Concept on request')
  await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, options.theme ?? 'light')
  return data
}

test('kinds save words, create, order, archive and restore once while preserving ticket areas', async ({ page }) => {
  const data = await open(page, { pagination: true })
  expect(data.reads).toBe(2)
  const edit = row(page).getByRole('button', { name: 'Edit', exact: true }), create = page.getByRole('button', { name: 'New kind of work', exact: true })
  await expectStableControls({ controls: { create, clickedRow: row(page), edit }, interactions: [{ name: 'open editor without moving rows', run: async () => { await edit.click(); await expect(editor(page)).toBeVisible() } }], scrollAreas: { page: page.locator('.settings-page') } })
  await editor(page).locator('[name="hint"]').fill('Screens approved before implementation.')
  await page.keyboard.press(await page.evaluate(() => /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)) ? 'Meta+Enter' : 'Control+Enter')
  await expect(editor(page)).toHaveCount(0); await expect(row(page)).toContainText('Screens approved before implementation.')
  expect(data.writes).toHaveLength(1); expect(data.writes[0]!.body).not.toHaveProperty('slug')
  await page.locator('.toast').getByRole('button', { name: 'Undo', exact: true }).click()
  await expect(row(page)).toContainText('Screens and interaction, designed as an HTML mock before any code.')
  await expectStableControls({ controls: { create, clickedRow: row(page), edit }, interactions: [{ name: 'open the whole row with the keyboard', run: async () => { await row(page).focus(); await row(page).press('Enter'); await expect(editor(page)).toBeVisible() } }] })
  await editor(page).getByRole('button', { name: /^Cancel Esc/ }).click()
  await row(page).getByRole('button', { name: 'Move UI design down' }).click()
  await expect(page.locator('[data-kind]').first()).toHaveAttribute('data-kind', 'frontend')
  expect(data.writes.at(-1)!.body).toEqual({ slugs: ['frontend', 'design', 'backend', 'infra', 'docs', 'security', 'other'] })
  await expect(row(page, 'other').getByRole('button', { name: /Move|Archive/ })).toHaveCount(0)
  await expect(row(page, 'security').getByRole('button', { name: 'Archive' })).toHaveCount(0)
  await row(page).getByRole('button', { name: 'Archive', exact: true }).click()
  const confirm = page.getByRole('dialog', { name: 'Archive UI design?' })
  await expect(confirm).toContainText('41 tickets keep their area value')
  await confirm.getByRole('button', { name: /^Archive/ }).click()
  await expect(row(page)).toHaveCount(0)
  await page.locator('.archived-kind').getByRole('button', { name: 'Restore' }).click()
  await expect(row(page)).toContainText('Ticket area design')
  await create.click(); await editor(page).locator('[name="label"]').fill('Data analysis'); await editor(page).locator('[name="hint"]').fill('Analyse the data behind a decision.')
  await editor(page).getByRole('button', { name: /^Create/ }).click()
  await expect(row(page, 'data-analysis')).toContainText('Data analysis')
  expect(data.writes.filter(write => write.method === 'POST' && write.path === '/api/work-kinds')).toHaveLength(1)
})

test('failed writes retain drafts and limits; editor actions and selectors stay still', async ({ page }) => {
  const data = await open(page, { fail: true })
  await row(page).getByRole('button', { name: 'Edit', exact: true }).click()
  const pane = editor(page), save = pane.getByRole('button', { name: /^Save/ }), cancel = pane.getByRole('button', { name: /^Cancel Esc/ }), fields = pane.locator('fieldset')
  await expectStableControls({ controls: { save, cancel, fields }, interactions: [{ name: 'validation appears below fields', run: async () => { await pane.locator('[name="examples"]').fill('one\ntwo\nthree\nfour'); await save.click(); await expect(pane).toContainText('Use a name') } }, { name: 'server failure retains controls and draft', run: async () => { await pane.locator('[name="examples"]').fill('one'); await save.click(); await expect(pane).toContainText('could not be saved'); await expect(pane.locator('[name="examples"]')).toHaveValue('one') } }], scrollAreas: { editor: pane } })
  expect(data.writes).toHaveLength(1)
  await pane.locator('[name="hint"]').focus(); await page.keyboard.press('Escape'); await expect(pane).toBeVisible(); await page.keyboard.press('Escape'); await expect(pane).toHaveCount(0)
  const small = page.getByRole('spinbutton', { name: 'h or less' }), rounds = page.getByRole('spinbutton', { name: 'fix rounds' })
  await small.scrollIntoViewIfNeeded()
  await expectStableControls({ controls: { small, rounds, first: page.locator('#sit-first') }, interactions: [{ name: 'failed limit resets to stored value', run: async () => { await small.fill('4'); await small.press('Tab'); await expect(small).toHaveValue('2'); await expect(page.locator('#k-sits')).toContainText('changed elsewhere') } }] })
  await expect(page.locator('.toast')).toHaveCount(0)
  expect(data.writes.at(-1)!.body).toEqual({ small_hours: 4, fix_rounds: 3, revision: 7 })
})

test('limits save current revisions and update situation definitions without moving selectors', async ({ page }) => {
  const data = await open(page)
  const small = page.getByRole('spinbutton', { name: 'h or less' }), rounds = page.getByRole('spinbutton', { name: 'fix rounds' })
  await small.scrollIntoViewIfNeeded()
  await expectStableControls({ controls: { small, rounds, first: page.locator('#sit-first') }, interactions: [{ name: 'save small work', run: async () => { await small.fill('3'); await small.press('Tab'); await expect.poll(() => data.limits.revision).toBe(8) } }, { name: 'save fix rounds', run: async () => { await rounds.fill('6'); await rounds.press('Tab'); await expect(page.locator('#sit-fix')).toContainText('Rounds 1 to 6'); await expect(page.locator('#sit-stuck')).toContainText('After 6 fix rounds') } }] })
  expect(data.writes.map(write => write.body)).toEqual([{ small_hours: 3, fix_rounds: 3, revision: 7 }, { small_hours: 3, fix_rounds: 6, revision: 8 }])
})

for (const identity of ['member', 'agent']) test(`${identity} reads kinds and situations with write controls absent`, async ({ page }) => {
  const data = await open(page, { member: identity === 'member', agent: identity === 'agent' })
  await expect(page.locator('#k-kinds button')).toHaveCount(0); await expect(page.locator('#k-sits input')).toHaveCount(0)
  await expect(page.locator('.kinds-section .who')).toContainText('you can read them')
  expect(data.writes).toHaveLength(0)
})

for (const width of [390, 1024, 1280, 1440]) for (const theme of ['light', 'dark']) test(`kinds evidence and German control stability ${width} ${theme}`, async ({ page }, testInfo) => {
  await open(page, { width, theme, german: true })
  await expect(page.locator('.section-nav a[aria-current="page"]')).toContainText('Arten von Arbeit')
  await page.screenshot({ path: testInfo.outputPath(`kinds-${width}-${theme}.png`), fullPage: true })
  const edit = row(page).getByRole('button', { name: 'Bearbeiten', exact: true })
  await expectStableControls({ controls: { clickedRow: row(page), edit }, interactions: [{ name: 'open German editor', run: async () => { await edit.click(); await expect(editor(page)).toBeVisible() } }] })
  const pane = editor(page), save = pane.getByRole('button', { name: /^Speichern/ }), cancel = pane.getByRole('button', { name: /^Abbrechen Esc/ })
  await expectStableControls({ controls: { save, cancel, ...(width === 390 ? { sheet: pane } : {}) }, interactions: [{ name: 'long German input does not move actions', run: async () => { await pane.locator('[name="hint"]').fill('Eine verständliche Beschreibung für Menschen und Agenten, die auch mit langen deutschen Begriffen lesbar bleibt.'); await pane.locator('[name="examples"]').fill('Ein umfangreiches Beispiel für die Modellkonfiguration\nDie Beschreibung einer vollständigen Benutzeroberfläche\nDie unveränderte Position der Bedienelemente') } }], scrollAreas: { pane } })
  await page.screenshot({ path: testInfo.outputPath(`kind-editor-${width}-${theme}.png`) })
  await pane.getByRole('button', { name: /^Abbrechen Esc/ }).click()
  await page.locator('#k-sits').scrollIntoViewIfNeeded()
  await page.screenshot({ path: testInfo.outputPath(`situations-${width}-${theme}.png`) })
  expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(1)
})
