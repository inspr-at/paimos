// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { expect, test, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors, mockView } from './work-fixtures'
import { expectStableControls } from './helpers/stable'
import type { PlanningRelease } from '../src/lib/deliveryPlanning'
const id = '11111111-1111-4111-8111-111111111112'
const other = '11111111-1111-4111-8111-111111111113'
const longName = 'Verbesserungen für die langfristige und nachvollziehbare Planung gemeinsamer Releases'
const row = (release_id = id, title = longName): PlanningRelease => ({ project_id: 'p-pharos', release_id, title, display_name: title, visibility: 'internal', state: 'planned', rank: release_id === id ? 'V' : 'W', revision: 1, rollup: { units: 2, completed: 0, open_hours: 3 }, build_summary: { budget_outlook: 'unknown', agents: 3, waiting: 2 } })
const counts = (n: number) => ({ matched_count: n, shown_count: n, hidden_count: 0, hidden_finished: 0, hidden_exit: 0, incomplete: false })
async function setup(page: Page, theme = 'light') {
  const data = fixtures(); data.preferences.theme = { choice: theme }; data.preferences['header-graph'] = { enabled: false }
  data.views.push(mockView({ id: '22222222-2222-4222-8222-222222222222', name: 'Saved Backlog', filters: { ships_in: 'none', status: 'open' } }))
  await mockWork(page, data)
  await page.addInitScript(() => {
    const sources: FakeSource[] = []
    class FakeSource {
      readyState = 1; onerror = null; listeners = new Map<string, ((e: { data: string }) => void)[]>(); closed = false
      constructor(public url: string) { sources.push(this) }
      addEventListener(type: string, fn: (e: { data: string }) => void) { this.listeners.set(type, [...this.listeners.get(type) ?? [], fn]) }
      close() { this.closed = true }
    }
    Object.assign(window, { EventSource: FakeSource, emitDelivery: (event: { id: number; type: string }) => {
      for (const source of sources) if (!source.closed) for (const fn of source.listeners.get(event.type) ?? []) fn({ data: JSON.stringify(event) })
    } })
  })
  const calls: { path: string; query: URLSearchParams }[] = [], state = { reverse: false, mode: 'releases' }
  await page.route('**/api/projects/p-pharos/**', async route => {
    const url = new URL(route.request().url()), path = url.pathname, query = url.searchParams
    calls.push({ path, query })
    if (path.endsWith('/delivery')) return route.fulfill({ json: { project_id: 'p-pharos', mode: state.mode, revision: 1 } })
    if (path.endsWith('/overview')) return route.fulfill({ json: { active: state.reverse ? [row(other, 'Audit sweep'), row()] : [row(), row(other, 'Audit sweep')], released: { items: [] }, backlog: { ranked: 1, tail: 1 }, abandoned: 0, counts_incomplete: false, matches: counts(4) } })
    if (path.endsWith('/items') || path.endsWith('/backlog')) return route.fulfill({ json: { items: [], count: 0, incomplete: false } })
    if (path.endsWith('/releases')) return route.fulfill({ json: { items: [row(), row(other, 'Audit sweep')] } })
    if (path.endsWith(`/releases/${id}`)) return route.fulfill({ json: row() })
    if (path.endsWith(`/releases/${other}`)) return route.fulfill({ json: row(other, 'Audit sweep') })
    return route.fallback()
  })
  await page.route('**/api/nodes?**', async route => {
    const url = new URL(route.request().url()), scope = url.searchParams.get('ships_in')
    if (!scope || url.searchParams.get('kind') === 'project') return route.fallback()
    calls.push({ path: url.pathname, query: new URLSearchParams(url.searchParams) })
    url.searchParams.delete('ships_in'); url.searchParams.set('ids', scope === 'none' ? 'n-3,n-4' : 'n-1,n-2')
    return route.fallback({ url: url.toString() })
  })
  await page.route('**/api/knowledge**', async route => {
    const url = new URL(route.request().url()), query = url.searchParams
    if (url.pathname.endsWith('/learnings')) return route.fulfill({ json: { items: [], truncated: false } })
    if (url.pathname !== '/api/knowledge') return route.fallback()
    calls.push({ path: url.pathname, query })
    let items = [{ id: 'note-release', title: 'Notiz zur langfristigen Release-Planung', type: 'runbook', kind: 'runbook', status: 'active', state: 'active', slug: 'release-plan', key: 'RUN-1', excerpt: '', link_count: 1, project: { id: 'p-pharos', key: 'PHAROS', title: 'Pharos' }, created_at: '2026-10-03T12:00:00Z', updated_at: '2026-10-03T12:00:00Z', updated_by: null, imported: false }, { id: 'note-backlog', title: 'Backlog context', type: 'memory', kind: 'memory', status: 'proposed', state: 'proposed', slug: 'backlog-context', key: 'MEM-1', excerpt: '', link_count: 1, project: { id: 'p-pharos', key: 'PHAROS', title: 'Pharos' }, created_at: '2026-10-03T12:00:00Z', updated_at: '2026-10-03T12:00:00Z', updated_by: null, imported: false }]
    const scope = query.get('ships_in')
    if (scope) items = items.filter(it => it.id === (scope === 'none' ? 'note-backlog' : 'note-release'))
    if (query.get('type')) items = items.filter(it => it.type === query.get('type'))
    if (query.get('status')) items = items.filter(it => query.get('status')!.split(',').includes(it.status))
    if (query.get('q')) items = items.filter(it => it.title.toLowerCase().includes(query.get('q')!.toLowerCase()))
    return route.fulfill({ json: { items, total: items.length, truncated: false, counts: { type: Object.fromEntries(items.map(it => [it.type, 1])), status: Object.fromEntries(items.map(it => [it.status, 1])) } } })
  })
  return { data, calls, state, errors: watchErrors(page) }
}
const scopeButton = (page: Page, width: number) => page.getByRole('button', { name: width <= 720 ? 'Filters: release scope' : 'Choose release scope', exact: true })
async function pick(page: Page, width: number, label: string) {
  await scopeButton(page, width).click()
  const dialog = page.getByRole('dialog', { name: 'Filters: release scope' })
  await expect(dialog.getByRole('button', { name: 'Audit sweep', exact: true })).toBeVisible()
  await dialog.getByRole('button', { name: label, exact: true }).click()
  await expect(dialog).not.toBeVisible()
}
for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark']) {
  test(`scope across tabs and fixed controls ${width} ${theme}`, async ({ page }) => {
    const h = await setup(page, theme); await page.setViewportSize({ width, height: 900 }); await page.goto('/p/PHAROS/tickets?ships_in=none&closed=1')
    await expect(scopeButton(page, width)).toBeVisible(); await expect(page.locator('.ticket-row').first()).toBeVisible()
    const clear = page.getByRole('button', { name: width <= 720 ? 'Clear scope' : 'Clear release scope', exact: true })
    await expectStableControls({ controls: { header: page.locator('.app-header'), scope: scopeButton(page, width), clear, tabs: page.getByRole('tablist', { name: 'Project sections' }), toolbar: page.locator('.toolbar-wrap'), count: width <= 1024 ? page.locator('.scope-count') : page.locator('.count') }, scrollAreas: { page: page.locator('.project-page') }, interactions: [
      { name: 'release scope', run: async () => { await pick(page, width, longName); await expect(page).toHaveURL(new RegExp(`ships_in=${id}`)); await expect(scopeButton(page, width)).toContainText(longName) } },
      { name: 'Backlog scope', run: async () => { await pick(page, width, 'Backlog'); await expect(page).toHaveURL(/ships_in=none/); await expect(scopeButton(page, width)).toContainText('Backlog') } },
      { name: 'all work', run: async () => { await clear.click(); await expect(page).not.toHaveURL(/ships_in=/); await expect(scopeButton(page, width)).toContainText('All work') } },
    ] })
    await pick(page, width, longName)
    const ticketCount = width <= 1024 ? page.locator('.scope-count') : page.locator('.count')
    await expect(ticketCount).toContainText(`tickets in ${longName}`)
    if (width === 1024) await expect(page.locator('.search-pill')).toHaveCSS('width', '32px')
    mkdirSync('test-results/aeon-596-p6b', { recursive: true }); await page.screenshot({ path: `test-results/aeon-596-p6b/tickets-${width}-${theme}.png` })
    await page.getByRole('tab', { name: 'Knowledge', exact: true }).click(); await expect(page).toHaveURL(new RegExp(`/knowledge\\?ships_in=${id}`))
    await expect(page.locator('.k-row')).toHaveCount(1)
    const knowledgeCount = width <= 1024 ? page.locator('.scope-count') : page.locator('.k-count')
    await expect(knowledgeCount).toContainText(`1 entry${width <= 1024 ? 'ies' : ''} in`.replace('entryies','entries'))
    await page.screenshot({ path: `test-results/aeon-596-p6b/knowledge-${width}-${theme}.png` })
    await pick(page, width, 'Backlog'); await expect(page.locator('.k-row')).toHaveCount(1); await expect(page.locator('.k-title')).toHaveText('Backlog context')
    await page.getByRole('tab', { name: 'Tickets', exact: true }).click(); await expect(page).toHaveURL(/ships_in=none/)
    await page.goBack(); await expect(page).toHaveURL(/knowledge\?ships_in=none/)
    expect(h.calls.filter(c => c.path === '/api/knowledge' && c.query.has('ships_in')).every(c => c.query.get('limit') === '200')).toBe(true)
    expect(h.errors).toEqual([])
  })
}
test('release names scope and expand; chevrons only expand; foreign reorder/adoption wait for Apply', async ({ page }) => {
  const h = await setup(page); await page.goto('/p/PHAROS/releases'); const names = page.locator('.release-name')
  await expect(names).toHaveCount(2)
  await page.getByRole('button', { name: `Expand ${longName}`, exact: true }).click(); await expect(page).not.toHaveURL(/ships_in/)
  await names.first().click(); await expect(page).toHaveURL(new RegExp(`ships_in=${id}`)); await expect(page.getByRole('button', { name: `Collapse ${longName}` })).toBeVisible()
  h.state.reverse = true
  const emit = async (eventId: number, type: string) => page.evaluate(({ eventId, type }) => { (window as unknown as { emitDelivery: (e: unknown) => void }).emitDelivery({ id: eventId, type, node_id: 'p-pharos', after: { project_id: 'p-pharos' } }) }, { eventId, type })
  await emit(7, 'release.reranked'); await expect(page.getByRole('button', { name: /1 change · Apply/ })).toBeVisible(); await expect(names.first()).toContainText(longName)
  await expectStableControls({ controls: { header: page.locator('.app-header'), count: page.locator('.count'), apply: page.getByRole('button', { name: /1 change · Apply/ }), scope: scopeButton(page, 1440), expansion: page.getByRole('toolbar', { name: 'Release expansion and continuation' }) }, interactions: [{ name: 'held echo', run: async () => { await emit(7, 'release.reranked'); await expect(names.first()).toContainText(longName) } }] })
  await page.getByRole('button', { name: /1 change · Apply/ }).click(); await expect(names.first()).toContainText('Audit sweep'); await expect(page.getByRole('button', { name: `Collapse ${longName}` })).toBeVisible()
  await page.goto('/p/PHAROS/releases'); await expect(names).toHaveCount(2); h.state.mode = 'journey'; await emit(8,'delivery.adopted'); await expect(names).toHaveCount(2); await page.keyboard.press('a'); await expect(page.getByText('This project is still using its journey.')).toBeVisible()
})
test('ambiguous bookmarks stay a visible repair; missing release refuses without all-work fallback', async ({ page }) => {
  const h = await setup(page); await page.goto(`/p/PHAROS/knowledge?ships_in=none,${id}`)
  await expect(page.getByText('Choose a release scope. This saved view contains multiple or excluded release values.')).toBeVisible()
  expect(h.calls.filter(c => c.path === '/api/knowledge')).toHaveLength(0)
  await pick(page, 1440, 'Backlog'); await expect(page.locator('.k-row')).toHaveCount(1)
  await page.route(`**/api/projects/p-pharos/releases/${id}`, route => route.fulfill({ status: 404, json: { error: 'not found' } }))
  await page.goto(`/p/PHAROS/tickets?ships_in=${id}`); await expect(scopeButton(page,1440)).toContainText('This release scope is unavailable'); await expect(page).toHaveURL(new RegExp(`ships_in=${id}`))
})
