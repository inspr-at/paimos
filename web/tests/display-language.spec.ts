// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page, type TestInfo } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { mockModels } from './models-simple-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { expectStableControls } from './helpers/stable'

const longGerman = 'Die vollständigen Berechtigungseinstellungen für sämtliche angeschlossenen Arbeitsbereiche zuverlässig prüfen.'
async function profile(page: Page) {
  const settings = settingsData(); settings.profile.locale = 'de-AT'; settings.profile.principal_id = me.id
  await mockSettings(page, settings)
  // Wait for the real profile response: an unloaded English default cannot prove this regression.
  const loaded = page.waitForResponse(response => response.url().endsWith('/api/me/profile') && response.ok())
  return { loaded }
}
async function capture(page: Page, info: TestInfo, view: string, interaction: () => Promise<void>) {
  for (const width of [1440, 1024, 390]) for (const theme of ['light', 'dark']) {
    await page.setViewportSize({ width, height: 1000 })
    await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
    await interaction()
    await page.screenshot({ path: info.outputPath(`${view}-${width}-${theme}.png`), fullPage: true })
  }
}

test('German profile: Kinds of work and its Settings navigation use English together', async ({ page }, info) => {
  await mockWork(page, fixtures(), { admin: true })
  const { loaded } = await profile(page)
  const words = { label: 'UI design', hint: 'Screens and interaction, designed as an HTML mock before any code.', examples: ['A settings screen'], labels: ['ui'] }
  await page.route('**/api/work-kinds**', route => route.fulfill({ json: { items: [{ id: 'kind-design', slug: 'design', ...words, words_de: { label: 'UI-Design', hint: longGerman, examples: ['Ein Einstellungsbildschirm'] }, position: 0, ticket_count: 14 }], next_cursor: null } }))
  await page.route('**/api/model-preferences/situations*', route => route.fulfill({ json: { small_hours: 2, fix_rounds: 3, revision: 1, set_by: null, set_at: null } }))
  await page.goto('/settings/kinds?lang=de'); await loaded
  await expect(page.locator('[data-kind="design"]')).toContainText('UI design')
  await expect(page.locator('.settings-page')).toContainText('Kinds of work')
  await expect(page.locator('.settings-page')).not.toContainText('Arten von Arbeit')
  const advanced = page.getByRole('button', { name: 'Advanced', exact: true })
  await capture(page, info, 'kinds', async () => {
    await expectStableControls({ controls: { advanced, models: page.getByRole('link', { name: 'Open Models', exact: true }) }, interactions: [{ name: 'toggle advanced', run: () => advanced.click() }], scrollAreas: { page: page.locator('.settings-page') } })
  })
})

test('German profile: Models remains English in its Settings frame', async ({ page }, info) => {
  await mockWork(page, fixtures(), { admin: true }); await mockModels(page, { fallback: true })
  const { loaded } = await profile(page)
  await page.goto('/settings/models?lang=de'); await loaded
  await expect(page.locator('[data-pick="all"]')).toBeVisible()
  await expect(page.locator('.settings-page')).toContainText('Which model does what')
  await expect(page.locator('.settings-page')).not.toContainText('Welches Modell was macht')
  const everyone = page.getByRole('button', { name: 'For everyone', exact: true }), mine = page.getByRole('button', { name: 'For me', exact: true })
  await capture(page, info, 'models', async () => {
    await expectStableControls({ controls: { everyone, mine }, interactions: [{ name: 'select everyone', run: () => everyone.click() }, { name: 'select me', run: () => mine.click() }], scrollAreas: { page: page.locator('.settings-page') } })
  })
})

test('German profile: session tabs, composer and code controls use the app language', async ({ page }, info) => {
  await mockWork(page, fixtures(), { admin: true })
  const data = agentData({ me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' }, nodes: {} })
  const worker = data.sessions[0]!
  Object.assign(worker, { display_label: 'release-lead', run_id: null, activity: 'idle' })
  data.sessions.splice(1); data.runs.splice(0); data.approvals.splice(0)
  data.messages[1]!.body = `${longGerman}\n\n\`\`\`go\nfmt.Println("ok")\n\`\`\``
  await mockAgents(page, data)
  const { loaded } = await profile(page)
  await page.goto(`/agents/${worker.id}?tab=messages`); await loaded
  const panel = page.getByRole('complementary', { name: 'Session details' })
  const composer = panel.getByRole('textbox', { name: 'Message to release-lead' })
  await expect(composer).toBeVisible()
  await expect(panel.getByRole('tab', { name: /Messages/ })).toBeVisible()
  await expect(panel.getByRole('button', { name: 'Copy', exact: true })).toBeVisible()
  await capture(page, info, 'session-chat', async () => {
    await expectStableControls({ controls: { composer, send: panel.getByRole('button', { name: 'Send', exact: true }) }, interactions: [{ name: 'write German message', run: async () => { await composer.fill(longGerman); await composer.blur() } }, { name: 'clear draft', run: async () => { await composer.fill(''); await composer.blur() } }], scrollAreas: { panel } })
  })
})

test('German profile: ticket table controls and recurring markers stay English', async ({ page }, info) => {
  await mockWork(page, fixtures(), { admin: true })
  const { loaded } = await profile(page)
  await page.goto('/tickets'); await loaded
  const search = page.getByRole('searchbox', { name: 'Search this view' })
  await expect(search).toBeVisible()
  await expect(page.getByRole('button', { name: /^Display:/ })).toBeVisible()
  await capture(page, info, 'tickets', async () => {
    await expectStableControls({ controls: { search, display: page.getByRole('button', { name: /^Display:/ }) }, interactions: [{ name: 'search', run: async () => { await search.fill('PHAROS'); await search.blur() } }, { name: 'clear search', run: async () => { await search.fill(''); await search.blur() } }], scrollAreas: { page: page.locator('main') } })
  })
})

test('German profile: Needs attention uses English through filters, group choices and table actions', async ({ page }, info) => {
  await mockWork(page, fixtures(), { admin: true })
  const { loaded } = await profile(page)
  const item = { event_id: 1, node_id: 'n-1', revision: '2026-10-04T08:00:00Z', key: 'PHAROS-10', title: longGerman, project_id: 'p-pharos', kind: 'triage', from: 'new', to: 'backlog', reason: 'Review the current suggestion.', at: '2026-10-03T08:00:00Z', editable: true, applicable: true }
  await page.route('**/api/views/needs-attention**', route => route.fulfill({ json: { items: [item], total: 1, counts: { triage: 1 }, next_cursor: null, facets: { projects: [{ id: 'p-pharos', label: 'PHAROS' }], assignees: [] }, facets_truncated: false } }))
  await page.goto('/tickets?view=needs-attention&group=none'); await loaded
  const search = page.getByRole('searchbox', { name: 'Search this view' })
  await expect(page.getByRole('grid', { name: 'Tickets needing attention' })).toBeVisible()
  await expect(page.locator('.attention-page')).toContainText('Needs attention collects')
  await expect(page.locator('.attention-page')).not.toContainText('Braucht Aufmerksamkeit')
  await capture(page, info, 'needs-attention', async () => {
    await expectStableControls({ controls: { search, kinds: page.getByRole('group', { name: 'Filter by kind' }) }, interactions: [{ name: 'search German title', run: async () => { await search.fill('Berechtigung'); await search.blur() } }, { name: 'clear search', run: async () => { await search.fill(''); await search.blur() } }], scrollAreas: { page: page.locator('.attention-page') } })
  })
})
