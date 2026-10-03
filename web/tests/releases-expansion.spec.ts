// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { expect, test, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { expectStableControls } from './helpers/stable'
import type { ItemPage, MatchCounts, PlanningItem, PlanningOverview, PlanningRelease } from '../src/lib/deliveryPlanning'
const rid = (n: number) => `00000000-0000-4000-8000-${String(n).padStart(12, '0')}`
const first = rid(1), second = rid(2), history = rid(3)
const label = 'Verbesserungen für die langfristige und nachvollziehbare Planung gemeinsamer Releases'
const at = '2026-10-03T12:00:00Z'
type Work = PlanningItem & { assignee?: string }
const item = (n: number, release: string | undefined, state = 'backlog', title = `Work ${n}`, rank: string | undefined = String(n)): Work => ({ item_id: rid(1000 + n), project_id: 'p-pharos', release_id: release, key: `PHAROS-${1000 + n}`, title, kind: n % 3 ? 'ticket' : 'task', state, rank, revision: rank ? 1 : 0, node_revision: at, created_at: at, estimated_hours: 1, expedite: false, due_on: null, assignee: n % 2 ? 'mira' : 'lin' })
const release = (n: number, title: string, state: PlanningRelease['state'] = 'planned'): PlanningRelease => ({ release_id: rid(n), project_id: 'p-pharos', title, display_name: title, visibility: n === 2 ? 'internal' : 'published', state, rank: String(n), revision: 1, version: state === 'released' ? '261003120000.0.0' : '', rollup: { units: 3, completed: 1, open_hours: 2 }, build_summary: { budget_outlook: 'unknown' } })
const emptyCounts = (): MatchCounts => ({ matched_count: 0, shown_count: 0, hidden_count: 0, hidden_finished: 0, hidden_exit: 0, incomplete: false })
function barrier() { let release!: () => void; const wait = new Promise<void>(resolve => { release = resolve }); return { wait, release } }
async function setup(page: Page, options: { theme?: string; hold?: { source: string; entered: ReturnType<typeof barrier>; release: ReturnType<typeof barrier> }; capped?: boolean } = {}) {
  const data = fixtures(); data.preferences.theme = { choice: options.theme ?? 'light' }; data.preferences['header-graph'] = { enabled: false }
  await mockWork(page, data)
  const errors = watchErrors(page)
  const calls: { source: string; query: URLSearchParams }[] = []
  const releases = [release(1, label, 'building'), release(2, 'Audit sweep'), ...Array.from({ length: 54 }, (_, i) => release(3 + i, i === 0 ? 'Historical needle marketing name' : `Published story ${i + 1}`, 'released'))]
  const members = [item(1, first, 'in_progress', 'Improve clarity'), item(2, first, 'done', 'Finished work'), item(3, first, 'cancelled', 'Cancelled work'), item(4, second), item(5, undefined, 'done', 'Completed ranked backlog'), item(6, undefined, 'cancelled', 'Cancelled new backlog', undefined)]
  members[5]!.rank = undefined
  members.push(...Array.from({ length: 215 }, (_, i) => item(100 + i, history, i === 214 ? 'done' : 'backlog', i >= 212 ? 'needle member' : `History work ${i + 1}`)))
  const closed = (state: string) => ['done', 'accepted', 'delivered', 'cancelled', 'canceled', 'archived'].includes(state)
  const values = (query: URLSearchParams, key: string) => query.getAll(key).flatMap(v => v.split(','))
  const accepts = (value: string, values: string[]) => (!values.some(v => !v.startsWith('!')) || values.includes(value)) && !values.includes(`!${value}`)
  function matching(query: URLSearchParams, list: Work[]) {
    const q = (query.get('q') ?? '').toLowerCase()
    return list.filter(w => (!q || `${w.key} ${w.title}`.toLowerCase().includes(q)) && accepts(w.state, values(query, 'work_state')) && accepts(w.kind, values(query, 'kind')) && accepts(w.assignee ?? '', values(query, 'assignee')))
  }
  function shown(query: URLSearchParams, list: Work[]) { return query.get('hide_closed') === 'false' ? list : list.filter(w => values(query, 'hide_state').length ? !values(query, 'hide_state').includes(w.state) : !closed(w.state)) }
  function counts(query: URLSearchParams, list: Work[]): MatchCounts {
    const hidden = list.filter(w => !shown(query, [w]).length)
    return { matched_count: list.length, shown_count: list.length - hidden.length, hidden_count: hidden.length, hidden_finished: hidden.filter(w => ['done', 'accepted', 'delivered'].includes(w.state)).length, hidden_exit: hidden.filter(w => ['cancelled', 'archived'].includes(w.state)).length, incomplete: !!options.capped }
  }
  const fingerprint = (query: URLSearchParams) => [...query].filter(([k]) => !['limit', 'cursor', 'released_cursor', 'state', 'part'].includes(k)).map(([k, v]) => `${k}=${v}`).join('&')
  await page.route('**/api/projects/p-pharos/**', async route => {
    const url = new URL(route.request().url()), query = url.searchParams, path = url.pathname
    if (path.endsWith('/delivery')) return route.fulfill({ json: { project_id: 'p-pharos', mode: 'releases', revision: 1 } })
    let source: string
    if (path.endsWith('/overview')) source = 'overview'
    else if (path.endsWith('/items')) source = `release:${path.split('/').at(-2)}`
    else if (path.endsWith('/backlog')) source = `backlog:${query.get('part')}`
    else if (path.endsWith('/releases')) source = query.get('state') ?? 'active'
    else return route.fallback()
    calls.push({ source, query })
    if (options.hold?.source === source) { options.hold.entered.release(); await options.hold.release.wait }
    const q = (query.get('q') ?? '').toLowerCase(), identity = fingerprint(query)
    const cursor = query.get('cursor') ?? query.get('released_cursor') ?? ''
    if (cursor && !cursor.startsWith(`${identity}|`)) return route.fulfill({ status: 400, json: { error: 'Cursor belongs to another query' } })
    const offset = cursor ? Number(cursor.split('|').at(-1)) : 0
    const next = (end: number) => `${identity}|${end}`
    const withCounts = (rows: PlanningRelease[]) => rows.map(r => ({ ...r, matches: counts(query, matching(query, members.filter(w => w.release_id === r.release_id))) }))
    const eligible = (rows: PlanningRelease[]) => withCounts(rows).filter(r => !q || r.title.toLowerCase().includes(q) || r.matches.matched_count > 0)
    const active = eligible(releases.filter(r => r.state !== 'released')), released = eligible(releases.filter(r => r.state === 'released')).reverse()
    const backlog = (part: string) => matching(query, members.filter(w => !w.release_id && (part === 'ranked' ? !!w.rank : !w.rank)))
    let answer: PlanningOverview | ItemPage | { items: PlanningRelease[]; next_cursor?: string }
    if (source === 'overview') answer = { active: active.slice(0, 50), active_next_cursor: active.length > 50 ? next(50) : '', released: { items: released.slice(0, 50), next_cursor: released.length > 50 ? next(50) : '' }, backlog: { ranked: shown(query, backlog('ranked')).length, tail: shown(query, backlog('tail')).length }, backlog_matches: { ranked: counts(query, backlog('ranked')), tail: counts(query, backlog('tail')) }, matches: counts(query, matching(query, members)), abandoned: 2, counts_incomplete: !!options.capped }
    else if (source === 'active' || source === 'released') {
      const rows = source === 'active' ? active : released, limit = Number(query.get('limit'))
      answer = { items: rows.slice(offset, offset + limit), next_cursor: rows.length > offset + limit ? next(offset + limit) : '' }
    } else {
      const matches = source.startsWith('backlog:') ? backlog(source.slice(8)) : matching(query, members.filter(w => w.release_id === source.slice(8)))
      const rows = shown(query, matches), limit = Number(query.get('limit'))
      answer = { items: rows.slice(offset, offset + limit), next_cursor: rows.length > offset + limit ? next(offset + limit) : '', count: rows.length, matches: counts(query, matches), incomplete: !!options.capped }
    }
    await route.fulfill({ json: answer }).catch(() => { /* A cancelled transport is deliberately discarded. */ })
  })
  return { calls, errors, releases, members }
}
const frame = (page: Page) => page.getByRole('region', { name: 'Release planning' })
const block = (page: Page, id: string) => page.locator(`[data-planning-block="${id}"]`)
const expand = (page: Page, name = label) => page.getByRole('button', { name: `Expand ${name}`, exact: true })
const search = (page: Page) => page.getByRole('searchbox', { name: 'Search releases and work in this project' })

for (const width of [1440, 1024, 390]) for (const theme of ['light', 'dark']) {
  test(`expansion frame stays stable at ${width} ${theme}`, async ({ page }) => {
    const world = await setup(page, { theme }); await page.setViewportSize({ width, height: 1000 }); await page.goto('/p/PHAROS/releases')
    await expect(expand(page)).toBeVisible(); expect(world.calls.filter(c => c.source === 'overview')).toHaveLength(1); expect(world.calls.filter(c => c.source.startsWith('release:'))).toHaveLength(0)
    const chevron = block(page, first).locator('.chevron'), row = block(page, first).locator('.release-row')
    await expectStableControls({ controls: { header: page.locator('.project-head'), tabs: page.getByRole('tablist', { name: 'Project sections' }), search: search(page), toolbar: page.getByRole('toolbar', { name: 'Release list controls' }), expandAll: page.getByRole('button', { name: 'Expand all', exact: true }), collapseAll: page.getByRole('button', { name: 'Collapse all', exact: true }), loadMore: page.getByRole('button', { name: 'Load more released releases' }), clickedChevron: chevron, clickedRow: row, name: row.locator('.release-name'), more: row.locator('.release-more') }, scrollAreas: { page: page.locator('.project-page'), frame: frame(page) }, interactions: [
      { name: 'expand first', run: async () => { await chevron.click(); await expect(block(page, first).locator('.ticket-row')).toHaveCount(1); expect((await block(page, first).locator('.planning-work').boundingBox())!.y).toBeGreaterThanOrEqual((await row.boundingBox())!.y + (await row.boundingBox())!.height - .5) } },
      { name: 'expand second', run: async () => { await expand(page, 'Audit sweep').click(); await expect(block(page, second).locator('.ticket-row')).toHaveCount(1) } },
      { name: 'collapse first', run: async () => { await chevron.click(); await expect(block(page, first).locator('.planning-work')).toHaveCount(0) } },
      { name: 'expand all', run: async () => { await page.getByRole('button', { name: 'Expand all', exact: true }).click(); await expect(block(page, first).locator('.ticket-row')).toHaveCount(1) } },
      { name: 'collapse all', run: async () => { await page.getByRole('button', { name: 'Collapse all', exact: true }).click(); await expect(frame(page).locator('.planning-work')).toHaveCount(0) } },
      { name: 'history continuation', run: async () => { await page.getByRole('button', { name: 'Load more released releases' }).click(); await expect(frame(page).locator('.release-row')).toHaveCount(56) } },
    ] })
    await chevron.click(); await expect(block(page, first).locator('.hidden-count')).toHaveText('1 finished, 1 cancelled or archived hidden by Hide closed')
    const releasedName = block(page, rid(56)).locator('.release-name'); await releasedName.focus(); await expect(releasedName.locator('.version')).toHaveCSS('opacity', '1'); await releasedName.hover()
    await search(page).focus(); await search(page).hover(); await expect(block(page, first).locator('.ticket-row')).toHaveCount(1)
    await page.keyboard.press('Meta+A'); await expect(search(page)).toBeFocused()
    await page.keyboard.press('Escape'); await expect(search(page)).not.toBeFocused()
    mkdirSync('test-results/aeon-596-p6a', { recursive: true }); await page.screenshot({ path: `test-results/aeon-596-p6a/releases-${width}-${theme}.png`, fullPage: false })
    expect(world.errors).toEqual([])
  })
}

test('server searches unloaded release names and members; name-only and hidden-only matches are honest', async ({ page }) => {
  const world = await setup(page); await page.goto('/p/PHAROS/releases'); await expect(expand(page)).toBeVisible()
  await search(page).fill('Historical needle'); await expect(block(page, history)).toBeVisible(); await expect(block(page, history).locator('.chevron')).toHaveAttribute('aria-expanded', 'false')
  expect(world.calls.filter(c => c.source === `release:${history}`)).toHaveLength(0)
  await search(page).fill('needle member'); await expect(block(page, history).locator('.ticket-row')).toHaveCount(2)
  await expect(block(page, history).locator('.hidden-count')).toHaveText('1 finished hidden by Hide closed')
  expect(world.calls.find(c => c.source === `release:${history}`)?.query.get('q')).toBe('needle member')
  await search(page).fill('Finished work'); await expect(block(page, first).locator('.hidden-count')).toHaveText('1 finished hidden by Hide closed'); await expect(block(page, first).locator('.ticket-row')).toHaveCount(0)
  expect(world.calls.filter(c => c.source === `release:${first}`)).toHaveLength(0)
  await expect(frame(page).locator('[data-planning-block]')).toHaveCount(1)
})

test('chevrons keep scope; release and Backlog names scope and expand without a focused route', async ({ page }) => {
  await setup(page); await page.goto('/p/PHAROS/releases')
  await expand(page).click(); await expect(block(page, first).locator('.ticket-row')).toHaveCount(1)
  expect(new URL(page.url()).searchParams.has('ships_in')).toBe(false)
  await block(page, first).locator('.release-name').click()
  await expect(page).toHaveURL(new RegExp(`/releases\\?ships_in=${first}`))
  await search(page).focus(); await search(page).hover()
  await expect(block(page, first).locator('.chevron')).toBeEnabled()
  await expect(block(page, first).locator('.release-row')).toHaveClass(/scoped/)
  await expect(block(page, first).locator('.ticket-row')).toHaveCount(1)
  const href = await block(page, first).locator('.title-link').getAttribute('href')
  expect(new URL(href!, page.url()).searchParams.get('section')).toBe('releases')
  expect(new URL(href!, page.url()).searchParams.get('ships_in')).toBe(first)
  await block(page, 'backlog').locator('.backlog-name').click(); await expect(page).toHaveURL(/\/releases\?ships_in=none/)
  await search(page).focus(); await search(page).hover()
  await expect(block(page, 'backlog').locator('.chevron')).toHaveAttribute('aria-expanded', 'true')
  await expect(page.getByRole('button', { name: 'Select', exact: true })).toHaveCount(0)
})

test('Hide off returns completed ranked and cancelled tail Backlog; URL filters intersect every planning read', async ({ page }) => {
  const world = await setup(page); await page.goto('/p/PHAROS/releases?closed=1')
  await expand(page, 'Backlog').click(); await expect(block(page, 'backlog').locator('.ticket-row')).toHaveCount(2)
  await expect(block(page, 'backlog').getByText('New · oldest first')).toBeVisible(); await expect(block(page, 'backlog').getByText('Completed ranked backlog')).toBeVisible(); await expect(block(page, 'backlog').getByText('Cancelled new backlog')).toBeVisible()
  for (const call of world.calls.filter(c => c.source.startsWith('backlog:'))) { expect(call.query.get('view')).toBe('planning'); expect(call.query.get('hide_closed')).toBe('false') }
  await page.goto('/p/PHAROS/releases?q=Work&status=backlog,!cancelled&type=ticket,!epic&assignee=mira,!lin&closed=1')
  await expect.poll(() => world.calls.filter(c => c.source === 'overview').at(-1)?.query.get('q')).toBe('Work')
  await expect(block(page, second)).toHaveCount(0)
  const query = world.calls.filter(c => c.source === 'overview').at(-1)!.query
  expect(query.getAll('work_state')).toContain('backlog'); expect(query.getAll('work_state')).toContain('!cancelled'); expect(query.getAll('assignee')).toEqual(['mira', '!lin']); expect(query.has('ships_in')).toBe(false)
})

test('child continuation retains server counts and query cursor; published versions reveal without resizing', async ({ page }) => {
  const world = await setup(page); await page.goto('/p/PHAROS/releases?closed=1')
  await page.getByRole('button', { name: 'Load more released releases' }).click()
  const name = block(page, history).locator('.release-name'); await expect(name).toBeVisible(); await block(page, history).locator('.chevron').click(); await expect(block(page, history).locator('.ticket-row')).toHaveCount(200)
  await expect(block(page, history).locator('.work-summary')).toHaveText('200 of 215 matching work loaded · more available')
  await page.getByRole('combobox', { name: 'Work to continue loading' }).selectOption(`release:${history}`); await page.getByRole('button', { name: 'Load work', exact: true }).click(); await expect(block(page, history).locator('.ticket-row')).toHaveCount(215)
  const reads = world.calls.filter(c => c.source === `release:${history}`); expect(reads).toHaveLength(2); expect(reads[1]!.query.get('cursor')).toContain('|200'); expect(reads[1]!.query.get('hide_closed')).toBe('false')
  await expectStableControls({ controls: { name, row: block(page, history).locator('.release-row') }, interactions: [{ name: 'focus version', run: () => name.focus() }, { name: 'hover version', run: () => name.hover() }] })
})

test('delayed insertion cannot move the hovered or focused downstream chevron; leaving applies it', async ({ page }) => {
  const entered = barrier(), release = barrier(); await setup(page, { hold: { source: `release:${first}`, entered, release } }); await page.goto('/p/PHAROS/releases')
  await expand(page).click(); await entered.wait
  const downstream = block(page, second).locator('.chevron'); await downstream.focus(); await downstream.hover()
  await expectStableControls({ controls: { downstream, expandAll: page.getByRole('button', { name: 'Expand all', exact: true }) }, interactions: [{ name: 'passive load waits', run: async () => { release.release(); await expect(frame(page).locator('.planning-feedback')).toContainText('pages ready'); await expect(block(page, first).locator('.ticket-row')).toHaveCount(0) } }] })
  await search(page).focus(); await search(page).hover(); await expect(block(page, first).locator('.ticket-row')).toHaveCount(1)
  await block(page, first).locator('.chevron').click(); await expect(block(page, first).locator('.planning-work')).toHaveCount(0)
})

test('count truncation and failed refresh stay visible instead of becoming empty success', async ({ page }) => {
  await setup(page, { capped: true }); await page.goto('/p/PHAROS/releases'); await expect(frame(page).locator('.planning-feedback')).toContainText('lower bound')
  await page.route('**/api/projects/p-pharos/delivery/overview?**', route => route.fulfill({ status: 403, json: { error: 'Access changed' } }))
  await search(page).fill('new query'); await expect(frame(page).locator('.planning-feedback')).toContainText('Access changed'); await expect(block(page, first)).toBeVisible(); await expect(block(page, first).locator('.chevron')).toBeDisabled()
})
