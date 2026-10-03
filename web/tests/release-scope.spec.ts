// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { expect, test, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors, mockView } from './work-fixtures'
import { knowledgeWorld, mockKnowledge } from './knowledge-fixtures'
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
      constructor(public url: string) { sources.push(this); queueMicrotask(() => { for (const fn of this.listeners.get('open') ?? []) fn({ data: '' }) }) }
      addEventListener(type: string, fn: (e: { data: string }) => void) { this.listeners.set(type, [...this.listeners.get(type) ?? [], fn]) }
      close() { this.closed = true }
    }
    Object.assign(window, { EventSource: FakeSource, emitDelivery: (event: { id: number; type: string }) => {
      for (const source of sources) if (!source.closed) for (const fn of source.listeners.get(event.type) ?? []) fn({ data: JSON.stringify(event) })
    } })
  })
  const calls: { path: string; query: URLSearchParams }[] = [], state = { reverse: false, mode: 'releases', graphChanged: false, noteDeleted: false, noteArchived: false, noteDetached: false }
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
    if (url.pathname === '/api/knowledge/graph') return route.fulfill({ json: { nodes: [
      { id:'note-release',key:'RUN-1',type:'runbook',kind:'knowledge',slug:'release-plan',title:'Notiz zur langfristigen Release-Planung',status:'active',degree:1,updated_at:'2026-10-03T12:00:00Z' },
      { id:'note-backlog',key:'MEM-1',type:'memory',kind:'knowledge',slug:'backlog-context',title:'Backlog context',status:'proposed',degree:1,updated_at:'2026-10-03T12:00:00Z' },
      { id:'n-1',key:'PHAROS-11',type:'ticket',kind:'ticket',slug:'',title:'Linked release work',status:'open',degree:1,updated_at:'2026-10-03T12:00:00Z' },
    ], edges: state.graphChanged ? [] : [{source:'note-release',target:'n-1',kind:'relation',label:'relates'}],truncated:false } })
    if (url.pathname.endsWith('/learnings')) return route.fulfill({ json: { items: [], truncated: false } })
    if (url.pathname !== '/api/knowledge') return route.fallback()
    calls.push({ path: url.pathname, query })
    let items = [{ id: 'note-release', title: 'Notiz zur langfristigen Release-Planung', type: 'runbook', kind: 'runbook', status: 'active', state: 'active', slug: 'release-plan', key: 'RUN-1', excerpt: '', link_count: 1, project: { id: 'p-pharos', key: 'PHAROS', title: 'Pharos' }, created_at: '2026-10-03T12:00:00Z', updated_at: '2026-10-03T12:00:00Z', updated_by: null, imported: false }, { id: 'note-backlog', title: 'Backlog context', type: 'memory', kind: 'memory', status: 'proposed', state: 'proposed', slug: 'backlog-context', key: 'MEM-1', excerpt: '', link_count: 1, project: { id: 'p-pharos', key: 'PHAROS', title: 'Pharos' }, created_at: '2026-10-03T12:00:00Z', updated_at: '2026-10-03T12:00:00Z', updated_by: null, imported: false }]
    const scope = query.get('ships_in')
    if (scope === id && state.noteDetached) items = items.filter(it => it.id !== 'note-release')
    if (state.noteDeleted) items = items.filter(it => it.id !== 'note-release')
    if (state.noteArchived) items = items.map(it => it.id === 'note-release' ? { ...it, status: 'archived', state: 'archived' } : it)
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
    await expectStableControls({ controls: { header: page.locator('.app-header'), scope: scopeButton(page, width), clear, tabs: page.getByRole('tablist', { name: 'Project sections' }), toolbar: page.locator('.toolbar-wrap'), count: width <= 1024 ? page.locator('.scope-count') : page.locator('.toolbar > .count') }, scrollAreas: { page: page.locator('.project-page') }, interactions: [
      { name: 'release scope', run: async () => { await pick(page, width, longName); await expect(page).toHaveURL(new RegExp(`ships_in=${id}`)); await expect(scopeButton(page, width)).toContainText(longName) } },
      { name: 'Backlog scope', run: async () => { await pick(page, width, 'Backlog'); await expect(page).toHaveURL(/ships_in=none/); await expect(scopeButton(page, width)).toContainText('Backlog') } },
      { name: 'all work', run: async () => { await clear.click(); await expect(page).not.toHaveURL(/ships_in=/); await expect(scopeButton(page, width)).toContainText('All work') } },
    ] })
    await pick(page, width, longName)
    const ticketCount = width <= 1024 ? page.locator('.scope-count') : page.locator('.toolbar > .count')
    await expect(ticketCount).toContainText(`tickets in ${longName}`)
    if (width === 1024) await expect(page.locator('.search-pill')).toHaveCSS('width', '32px')
    mkdirSync('test-results/aeon-596-p6b', { recursive: true }); await page.screenshot({ path: `test-results/aeon-596-p6b/tickets-${width}-${theme}.png` })
    await scopeButton(page, width).click()
    const scopeDialog = page.getByRole('dialog', { name: 'Filters: release scope' })
    await expect(scopeDialog.getByRole('button', { name: 'Refresh', exact: true })).toBeEnabled()
    if (width <= 720) await expect(scopeDialog).toHaveCSS('width', `${width}px`)
    await expectStableControls({ controls: { done: width <= 720 ? scopeDialog.locator('.scope-footer .scope-done') : scopeDialog.locator('header .scope-done'), refresh: scopeDialog.getByRole('button', { name: 'Refresh', exact: true }), options: scopeDialog.locator('.scope-options'), all: scopeDialog.getByRole('button', { name: 'All work', exact: true }), backlog: scopeDialog.getByRole('button', { name: 'Backlog', exact: true }), selected: scopeDialog.getByRole('button', { name: longName, exact: true }), ...(width <= 720 ? { frame: scopeDialog } : {}) }, scrollAreas: { options: scopeDialog.locator('.scope-options') }, interactions: [
      { name: 'refresh scope options', run: async () => { await scopeDialog.getByRole('button', { name: 'Refresh', exact: true }).click(); await expect(scopeDialog.getByRole('button', { name: 'Refresh', exact: true })).toBeEnabled() } },
      { name: 'choose and reopen Backlog', run: async () => { await scopeDialog.getByRole('button', { name: 'Backlog', exact: true }).click(); await expect(page).toHaveURL(/ships_in=none/); await scopeButton(page,width).click(); await expect(scopeDialog.getByRole('button', { name: 'Refresh', exact: true })).toBeEnabled() } },
      { name: 'choose and reopen release', run: async () => { await scopeDialog.getByRole('button', { name: longName, exact: true }).click(); await expect(page).toHaveURL(new RegExp(`ships_in=${id}`)); await scopeButton(page,width).click(); await expect(scopeDialog.getByRole('button', { name: 'Refresh', exact: true })).toBeEnabled() } },
    ] })
    await page.screenshot({ path: `test-results/aeon-596-p6b/picker-${width}-${theme}.png` })
    await (width <= 720 ? scopeDialog.locator('.scope-footer .scope-done') : scopeDialog.locator('header .scope-done')).click()
    await page.getByRole('tab', { name: 'Knowledge', exact: true }).click(); await expect(page).toHaveURL(new RegExp(`/knowledge\\?ships_in=${id}`))
    await expect(page.locator('.k-row')).toHaveCount(1)
    const knowledgeCount = width <= 1024 ? page.locator('.scope-count') : page.locator('.k-count')
    await expect(knowledgeCount).toContainText('1 entry in')
    const statusControl = page.locator('.k-menu-btn:not(.k-sort-btn)'), sortControl = page.locator('.k-sort-btn')
    const filterSearch = page.getByRole('searchbox', { name: 'Search knowledge in Pharos' })
    await expectStableControls({ controls: { header: page.locator('.app-header'), scope: scopeButton(page,width), count: knowledgeCount, status: statusControl, sort: sortControl, search: page.locator('.k-search'), new: page.getByRole('button', { name: 'New knowledge entry', exact: true }), modes: page.getByRole('tablist', { name: 'Knowledge views', exact: true }) }, scrollAreas: { page: page.locator('.project-page') }, interactions: [
      { name: 'archived intersection is empty', run: async () => { await statusControl.click(); await page.getByRole('radio', { name: /Archived/, exact: false }).click(); await expect(page.locator('.k-row')).toHaveCount(0); await expect(knowledgeCount).toContainText('0 entries in') } },
      { name: 'restore Current filter', run: async () => { await statusControl.click(); await page.getByRole('radio', { name: /Current/, exact: false }).click(); await expect(page.locator('.k-row')).toHaveCount(1) } },
      { name: 'change sort without moving neighbouring controls', run: async () => { await sortControl.click(); await page.getByRole('radio', { name: 'Title', exact: true }).click(); await expect(page).toHaveURL(/sort=title/) } },
      { name: 'empty scoped search stays honest', run: async () => { await filterSearch.fill('no matching note'); await expect(page).toHaveURL(/q=no\+matching\+note/); await expect(page.locator('.k-row')).toHaveCount(0) } },
      { name: 'clear scoped search', run: async () => { await filterSearch.fill(''); await expect(page.locator('.k-row')).toHaveCount(1) } },
    ] })
    await page.screenshot({ path: `test-results/aeon-596-p6b/knowledge-${width}-${theme}.png` })
    await page.getByRole('tablist',{name:'Knowledge views'}).getByRole('tab',{name:'Graph',exact:true}).click()
    await expect(page.locator('.kg-canvas')).toHaveAttribute('data-ready','true')
    await expect(page.locator('.graph-heading [role="status"]')).toHaveText('1 entries, 0 links')
    await expectStableControls({controls:{header:page.locator('.app-header'),scope:scopeButton(page,width),count:knowledgeCount,fit:page.getByRole('button',{name:'Fit graph to view'}),linked:page.getByRole('button',{name:'Show linked tickets'})},interactions:[
      {name:'linked satellites remain scoped to visible notes',run:async()=>{await page.getByRole('button',{name:'Show linked tickets'}).click();await expect(page.locator('.graph-heading [role="status"]')).toHaveText('1 entries, 1 link, 1 linked tickets')}},
    ]})
    await page.screenshot({path:`test-results/aeon-596-p6b/knowledge-graph-${width}-${theme}.png`})
    h.state.graphChanged=true
    await page.evaluate(()=>{(window as unknown as {emitDelivery:(event:unknown)=>void}).emitDelivery({id:61,type:'relation.deleted',before:{type:'relates',source_node_id:'note-release',target_node_id:'n-1'}})})
    await expect(page.getByRole('button',{name:/1 change · Apply/})).toBeVisible();await expect(page.locator('.graph-heading [role="status"]')).toHaveText('1 entries, 1 link, 1 linked tickets')
    await page.getByRole('button',{name:/1 change · Apply/}).click();await expect(page.locator('.graph-heading [role="status"]')).toHaveText('1 entries, 0 links')
    await page.getByRole('tablist',{name:'Knowledge views'}).getByRole('tab',{name:'Entries',exact:true}).click();await expect(page.locator('.k-row')).toHaveCount(1)
    await pick(page, width, 'Backlog'); await expect(page.locator('.k-row')).toHaveCount(1); await expect(page.locator('.k-title')).toHaveText('Backlog context')
    await page.getByRole('tab', { name: 'Tickets', exact: true }).click(); await expect(page).toHaveURL(url => url.pathname.endsWith('/tickets') && url.searchParams.get('ships_in') === 'none')
    await page.goBack(); await expect(page).toHaveURL(url => url.pathname.endsWith('/knowledge') && url.searchParams.get('ships_in') === 'none' && url.searchParams.get('sort') === 'title')
    await expect(page.locator('.k-title')).toHaveText('Backlog context')
    expect(h.calls.filter(c => c.path === '/api/knowledge' && c.query.has('ships_in')).every(c => c.query.get('limit') === '200')).toBe(true)
    await page.getByRole('tab', { name: 'Releases', exact: true }).click(); await expect(page.locator('.release-name')).toHaveCount(2)
    await page.screenshot({ path: `test-results/aeon-596-p6b/releases-${width}-${theme}.png` })
    expect(h.errors).toEqual([])
  })
}
for (const change of ['foreign deletion applied', 'own placement committed'] as const) test(`${change} on Tickets refreshes inactive Knowledge with identical filters`, async ({ page }) => {
  const h = await setup(page)
  await page.goto(`/p/PHAROS/knowledge?ships_in=${id}`)
  await expect(page.locator('.k-row')).toHaveCount(1)
  await expect(page.getByRole('button', { name: /^All knowledge/ }).locator('.k-kind-count')).toHaveText('1')
  const reads = () => h.calls.filter(call => call.path === '/api/knowledge').length
  const initialReads = reads()
  await page.getByRole('tab', { name: 'Tickets', exact: true }).click()
  await expect(page).toHaveURL(url => url.pathname.endsWith('/tickets') && url.searchParams.get('ships_in') === id)
  await expect(page.locator('.ticket-row').first()).toBeVisible()
  if (change === 'foreign deletion applied') {
    h.state.noteDeleted = true
    await page.evaluate(() => { (window as unknown as { emitDelivery: (event: unknown) => void }).emitDelivery({ id: 81, type: 'knowledge.deleted', node_changes: [{ id: 'note-release', project_id: 'p-pharos', change: 'deleted' }] }) })
    const apply = page.getByRole('button', { name: /1 change · Apply/ })
    await expect(apply).toBeVisible(); await apply.click()
    await expect(apply).toHaveCount(0)
  } else {
    h.state.noteDetached = true
    await page.locator('.project-page').evaluate(el => {
      const instance = (el as unknown as { __vueParentComponent: { provides: Record<symbol, unknown> } }).__vueParentComponent
      const key = Object.getOwnPropertySymbols(instance.provides).find(key => key.description === 'delivery-actions')!
      const actions = instance.provides[key] as { begin(): string; commit(identity: string, change: unknown): boolean }
      if (!actions.commit(actions.begin(), { kind: 'placement', result: { items: [{ item_id: 'n-1', project_id: 'p-pharos', rank: 'V', revision: 2, expedite: false, due_on: null }], undo_event_id: 82 } })) throw new Error('Own placement was discarded')
    })
    await expect(page.getByRole('button', { name: 'Undo', exact: true })).toBeEnabled()
  }
  expect(reads()).toBe(initialReads)
  await page.getByRole('tab', { name: 'Knowledge', exact: true }).click()
  await expect(page).toHaveURL(url => url.pathname.endsWith('/knowledge') && url.searchParams.get('ships_in') === id && !url.searchParams.has('type') && !url.searchParams.has('q'))
  await expect(page.locator('.k-row')).toHaveCount(0)
  await expect(page.getByRole('button', { name: /^All knowledge/ }).locator('.k-kind-count')).toHaveText('0')
  expect(reads()).toBe(initialReads + 1)
  expect(h.errors).toEqual([])
})
test('foreign Knowledge archive, deletion and Undo remain held until Apply', async ({ page }) => {
  const h = await setup(page); await page.goto(`/p/PHAROS/knowledge?ships_in=${id}`)
  const rows = page.locator('.k-row'), count = page.locator('.k-count')
  const total = page.getByRole('button', { name: /^All knowledge/ }).locator('.k-kind-count')
  await expect(rows).toHaveCount(1); await expect(count).toContainText('1 entry in')
  const send = async (event: unknown) => page.evaluate(event => { (window as unknown as { emitDelivery: (event: unknown) => void }).emitDelivery(event) }, event)
  const changes = [
    { id: 71, type: 'knowledge.updated', archive: true, deleted: false, undo_of: null, change: 'updated', expected: 0 },
    { id: 72, type: 'knowledge.updated', archive: false, deleted: false, undo_of: 71, change: 'updated', expected: 1 },
    { id: 73, type: 'knowledge.deleted', archive: false, deleted: true, undo_of: null, change: 'deleted', expected: 0 },
    { id: 74, type: 'knowledge.deleted', archive: false, deleted: false, undo_of: 73, change: 'created', expected: 1 },
  ]
  let shown = 1
  for (const change of changes) {
    h.state.noteArchived = change.archive; h.state.noteDeleted = change.deleted
    await send({ id: change.id, type: change.type, undo_of: change.undo_of, node_id: 'note-release', node_changes: [{ id: 'note-release', project_id: 'p-pharos', change: change.change }] })
    const apply = page.getByRole('button', { name: /1 change · Apply/ })
    await expect(apply).toBeVisible(); await expect(rows).toHaveCount(shown)
    // Apply occupies the toolbar's count slot by design; the kind rail keeps
    // the displayed population's total and must not adopt the foreign count.
    await expect(count).toContainText('1 change · Apply')
    await expect(total).toHaveText(String(shown))
    await apply.click(); await expect(rows).toHaveCount(change.expected)
    await expect(count).toContainText(`${change.expected} ${change.expected === 1 ? 'entry' : 'entries'} in`)
    await expect(total).toHaveText(String(change.expected))
    shown = change.expected
  }
  expect(h.errors).toEqual([])
})
test('release names scope and expand; chevrons only expand; foreign reorder/adoption wait for Apply', async ({ page }) => {
  const h = await setup(page); await page.goto('/p/PHAROS/releases'); const names = page.locator('.release-name')
  await expect(names).toHaveCount(2)
  await page.getByRole('button', { name: `Expand ${longName}`, exact: true }).click(); await expect(page).not.toHaveURL(/ships_in/)
  await names.first().click(); await expect(page).toHaveURL(new RegExp(`ships_in=${id}`)); await expect(page.getByRole('button', { name: `Collapse ${longName}` })).toBeVisible()
  h.state.reverse = true
  const emit = async (eventId: number, type: string) => page.evaluate(({ eventId, type }) => { (window as unknown as { emitDelivery: (e: unknown) => void }).emitDelivery({ id: eventId, type, node_id: 'p-pharos', after: { project_id: 'p-pharos' } }) }, { eventId, type })
  await emit(7, 'release.reranked'); await expect(page.getByRole('button', { name: /1 change · Apply/ })).toBeVisible(); await expect(names.first()).toContainText(longName)
  await expectStableControls({ controls: { header: page.locator('.app-header'), count: page.locator('.toolbar > .count'), apply: page.getByRole('button', { name: /1 change · Apply/ }), scope: scopeButton(page, 1440), expansion: page.getByRole('toolbar', { name: 'Release expansion and continuation' }) }, interactions: [{ name: 'held echo', run: async () => { await emit(7, 'release.reranked'); await expect(names.first()).toContainText(longName) } }] })
  await page.getByRole('button', { name: /1 change · Apply/ }).click(); await expect(names.first()).toContainText('Audit sweep'); await expect(page.getByRole('button', { name: `Collapse ${longName}` })).toBeVisible()
  await page.goto('/p/PHAROS/releases'); await expect(names).toHaveCount(2); h.state.mode = 'journey'; await emit(8,'delivery.adopted'); await expect(names).toHaveCount(2); await page.keyboard.press('a'); await expect(page.getByText('This project is still using its journey.')).toBeVisible()
  h.state.mode = 'releases'; await emit(9, 'delivery.adopted')
  await expect(page.getByText('This project is still using its journey.')).toBeVisible(); await expect(names).toHaveCount(0)
  await page.keyboard.press('a'); await expect(names).toHaveCount(2)
})
test('ambiguous bookmarks stay a visible repair; missing release refuses without all-work fallback', async ({ page }) => {
  const h = await setup(page); await page.goto(`/p/PHAROS/knowledge?ships_in=none,${id}`)
  await expect(page.getByText('Choose a release scope. This saved view contains multiple or excluded release values.')).toBeVisible()
  expect(h.calls.filter(c => c.path === '/api/knowledge')).toHaveLength(0)
  await pick(page, 1440, 'Backlog'); await expect(page.locator('.k-row')).toHaveCount(1)
  await page.route(`**/api/projects/p-pharos/releases/${id}`, route => route.fulfill({ status: 404, json: { error: 'not found' } }))
  await page.goto(`/p/PHAROS/tickets?ships_in=${id}`); await expect(scopeButton(page,1440)).toContainText('This release scope is unavailable'); await expect(page).toHaveURL(new RegExp(`ships_in=${id}`))
})

for (const width of [390,1024,1440]) for (const theme of ['light','dark']) test(`own rank receipt and refused/successful Undo keep fixed slots ${width} ${theme}`,async ({page})=>{
  await setup(page,theme);await page.setViewportSize({width,height:900});await page.goto('/p/PHAROS/releases')
  const names=page.locator('.release-name');await expect(names).toHaveCount(2)
  const send=async (eventId:number)=>page.evaluate(eventId=>{(window as unknown as {emitDelivery:(event:unknown)=>void}).emitDelivery({id:eventId,type:'release.reranked',after:{project_id:'p-pharos'}})},eventId)
  await send(42);await send(43)
  // Drive P6b's provided receipt boundary; P6c owns the drag/menu authoring UI.
  await page.locator('.project-page').evaluate((el,result)=>{
    const instance=(el as unknown as {__vueParentComponent:{provides:Record<symbol,unknown>}}).__vueParentComponent
    const key=Object.getOwnPropertySymbols(instance.provides).find(key=>key.description==='delivery-actions')!
    const actions=instance.provides[key] as {begin:()=>string;commit:(identity:string,change:unknown)=>boolean}
    if(!actions.commit(actions.begin(),{kind:'rank',result})) throw new Error('Receipt refused')
  },{...row(),rank:'X',revision:2,undo_event_id:42})
  await expect(names.first()).toContainText('Audit sweep')
  const undo=page.getByRole('button',{name:'Undo',exact:true}),apply=page.getByRole('button',{name:/1 change · Apply/})
  await expect(undo).toBeEnabled();await expect(apply).toBeVisible()
  await page.screenshot({path:`test-results/aeon-596-p6b/undo-${width}-${theme}.png`})
  let refused=true
  await page.route('**/api/events/42/undo',route=>route.fulfill(refused?{status:409,json:{error:'Release order changed again'}}:{status:201,json:{id:44,undo_of:42,type:'release.reranked',after:{...row(),revision:3}}}))
  await expectStableControls({controls:{header:page.locator('.app-header'),scope:scopeButton(page,width),count:width<=1024?page.locator('.scope-count'):page.locator('.toolbar > .count'),undo,apply,notice:page.locator('.delivery-notice')},interactions:[
    {name:'exact own echo deduplication',run:async()=>{await send(42);await expect(apply).toBeVisible()}},
    {name:'authoritative Undo refusal',run:async()=>{await undo.click();await expect(page.locator('.delivery-notice')).toContainText('Release order changed again');await expect(undo).toBeEnabled()}},
  ]})
  refused=false;await undo.click();await expect(names.first()).toContainText(longName);await expect(apply).toBeVisible();await expect(page.locator('.delivery-notice button')).toBeDisabled()
})

for (const ordering of ['echo first', 'response first']) test(`own Knowledge archive and Undo correlate receipts (${ordering})`, async ({ page }) => {
  const h = await setup(page), world = knowledgeWorld()
  await mockKnowledge(page, world)
  const note = world.entries.find(entry => entry.id === 'k-deploy')!
  const full = (event_id: number) => ({ ...note, project: { id: 'p-pharos', key: 'PHAROS', title: 'Pharos' }, kind: 'runbook', state: note.status, excerpt: '', link_count: note.links.length, event_id })
  const emit = (id: number, undo_of?: number) => page.evaluate(event => {
    (window as unknown as { emitDelivery(event: unknown): void }).emitDelivery(event)
  }, { id, type: 'knowledge.updated', undo_of, actor_principal_id: 'me', node_changes: [{ id: note.id, project_id: 'p-pharos', change: 'updated' }] })
  await page.route('**/api/knowledge/k-deploy', async route => {
    if (route.request().method() !== 'PATCH') return route.fallback()
    note.status = 'archived'; note.updated_at = '2026-10-04T00:00:00Z'
    if (ordering === 'echo first') await emit(101)
    await route.fulfill({ json: full(101) })
  })
  await page.route('**/api/events/101/undo', async route => {
    note.status = 'active'; note.updated_at = '2026-10-04T00:01:00Z'
    if (ordering === 'echo first') await emit(102, 101)
    await route.fulfill({ status: 201, json: { id: 102, undo_of: 101 } })
  })
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(`/p/PHAROS/knowledge?ships_in=${id}&entry=runbook/deploy-release`)
  await expect(page.locator('.e-body')).toBeVisible()
  await page.getByRole('button', { name: 'More actions', exact: true }).click()
  await page.getByRole('menuitem', { name: 'Archive', exact: true }).click()
  await expect(page.locator('.e-note').filter({ hasText: 'Archived:' })).toBeVisible()
  if (ordering === 'response first') await emit(101)
  const apply = page.locator('.k-count').getByRole('button', { name: /change.*Apply/, includeHidden: true })
  await expect(apply).toHaveCount(0)
  await page.locator('.toast').getByRole('button', { name: 'Undo', exact: true }).click()
  await expect(page.locator('.e-note').filter({ hasText: 'Archived:' })).toHaveCount(0)
  if (ordering === 'response first') await emit(102, 101)
  await expect(apply).toHaveCount(0)
  await emit(103)
  await expect(apply).toHaveCount(1)
  await page.getByRole('button', { name: 'Close the preview', exact: true }).click()
  await expect(apply).toBeVisible()
  expect(h.errors).toEqual([])
})
