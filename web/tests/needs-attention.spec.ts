// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { businessData, mockBusiness } from './business-fixtures'
import { controlStability } from './control-stability'
import { orderAttentionRows, type AttentionItem, type AttentionResult } from '../src/lib/attention'

const row = (index: number, extra: Partial<AttentionItem> = {}): AttentionItem => ({
  event_id: index + 1, node_id: `node-${index}`, revision: '2026-10-04T08:00:00Z', key: `AEON-${index + 10}`,
  title: index === 0 ? 'Die vollständigen Abrechnungseinstellungen für sämtliche angeschlossenen Arbeitsbereiche zuverlässig aktualisieren' : `Ticket ${index + 1}`,
  project_id: index % 2 ? 'p-pharos' : 'p-aeon', kind: index % 2 ? 'cancel' : 'triage',
  from: index % 2 ? 'backlog' : 'new', to: index % 2 ? 'cancelled' : 'backlog',
  reason: 'Untouched in this project; review the current suggestion.', at: '2026-10-03T08:00:00Z', editable: true, applicable: true, ...extra,
})
async function setup(page: Page, items = [row(0), row(1), row(2)], options: { fail?: boolean; partial?: boolean; held?: Promise<void>; received?: () => void } = {}) {
  await mockWork(page, fixtures())
  await mockBusiness(page, businessData({ role: 'member' }), { role: 'member' })
  const preferences = new Map<string, unknown>()
  await page.route('**/api/preferences/needs-attention*', async route => {
    const key = new URL(route.request().url()).pathname
    if (route.request().method() === 'PUT') preferences.set(key, route.request().postDataJSON().value)
    await route.fulfill({ json: { value: preferences.get(key) ?? null } })
  })
  const live = new Map(items.map(item => [item.event_id, { ...item }]))
  const actions: { action: string; items: AttentionItem[] }[] = [], queries: URLSearchParams[] = []
  let fail = options.fail ?? false
  let releaseRevision = items.find(item => item.release_id)?.release_revision ?? 0
  let projectRevision = items.find(item => item.release_id)?.release_project_revision ?? 0
  await page.route('**/api/status-autopilot/attention**', async route => {
    const request = route.request(), url = new URL(request.url())
    if (url.pathname.endsWith('/actions')) {
      const body = request.postDataJSON() as { action: string; items: AttentionItem[] }; actions.push(body)
      const results: AttentionResult[] = body.items.map(item => {
        if (options.partial && item.event_id === 2) return { event_id: 2, ok: false, error: 'The ticket changed. Reload before trying again.' }
        return { event_id: item.event_id, ok: true, resolution_event_id: body.action === 'undo' ? undefined : item.event_id + 1000, revision: body.action === 'undo' ? '2026-10-05T09:00:00Z' : '2026-10-05T08:00:00Z', ...(item.release_id ? { release_id: item.release_id, previous_release_revision: releaseRevision, release_revision: ++releaseRevision, previous_release_project_revision: projectRevision, release_project_revision: ++projectRevision } : {}) }
      })
      options.received?.(); if (options.held) await options.held
      return route.fulfill({ json: { items: results } }).catch(() => {})
    }
    queries.push(url.searchParams)
    if (fail) return route.fulfill({ status: 503, json: { error: 'unavailable' } })
    const q = url.searchParams, filtered = [...live.values()].filter(item => (!q.get('kind') || item.kind === q.get('kind')) && (!q.get('project_id') || item.project_id === q.get('project_id')) && (!q.get('q') || item.title.toLowerCase().includes(q.get('q')!.toLowerCase())))
    if (url.pathname.endsWith('/groups')) {
      const by = q.get('by'), ids = [...new Set(filtered.map(item => by === 'kind' ? item.kind : item.project_id))]
      return route.fulfill({ json: { total: filtered.length, truncated: false, groups: ids.map(id => {
        const rows = filtered.filter(item => (by === 'kind' ? item.kind : item.project_id) === id)
        return { id, ...(by === 'kind' ? { kind: id } : { project_id: id, key: id === 'p-aeon' ? 'AEON' : 'PHAROS', title: id === 'p-aeon' ? 'Aeon' : 'Pharos' }), total: rows.length, counts: Object.fromEntries(['proposed', 'triage', 'cancel', 'blocked', 'missed'].map(kind => [kind, rows.filter(row => row.kind === kind).length])), editable: rows.filter(row => row.editable).length, applicable: rows.filter(row => row.applicable && row.editable).length, can_manage: false }
      }) } })
    }
    const offset = Number(q.get('after') ?? 0)
    return route.fulfill({ json: { items: orderAttentionRows(filtered).slice(offset, offset + 50), total: filtered.length, counts: { proposed: 0, triage: items.filter(i => i.kind === 'triage').length, cancel: items.filter(i => i.kind === 'cancel').length, blocked: 0, missed: 0 }, next_cursor: filtered.length > offset + 50 ? String(offset + 50) : null,
      facets: { projects: [{ id: 'p-aeon', label: 'AEON Aeon' }, { id: 'p-pharos', label: 'PHAROS Pharos' }], assignees: [{ id: 'old-person', label: 'Previous assignee' }] }, facets_truncated: false } })
  })
  return { actions, queries, preferences, recover: () => { fail = false } }
}
const table = (page: Page) => page.getByRole('grid', { name: 'Tickets needing attention' })
const first = (page: Page) => table(page).locator('#row-attention-1')

test('Apply, Dismiss and Undo retain rows and control boxes on desktop and phone, in both themes', async ({ page }, testInfo) => {
  const errors = watchErrors(page)
  const data = await setup(page)
  for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark']) {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
    await page.goto('/tickets?view=needs-attention')
    await expect(first(page)).toBeVisible()
    await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
    const searchBox = page.getByRole('searchbox', { name: 'Search this view' })
    const searchIcon = page.locator('.search-field > svg')
    const inputBounds = await searchBox.boundingBox(), iconBounds = await searchIcon.boundingBox()
    expect(inputBounds?.width).toBeGreaterThan(0)
    expect(iconBounds?.width).toBeGreaterThan(0)
    expect(iconBounds!.x + iconBounds!.width).toBeLessThanOrEqual(inputBounds!.x + await searchBox.evaluate(el => parseFloat(getComputedStyle(el).paddingLeft)))
    const rowActions = first(page).locator('.action-stack')
    const guard = await controlStability(page, { search: page.getByRole('searchbox', { name: 'Search this view' }), filters: page.locator('.facets'), kind: page.getByRole('button', { name: 'Kind filter', exact: true }), navigation: page.locator('.navigation-band'), clickedRow: first(page), actions: rowActions, nextRow: table(page).locator('#row-attention-2') })
    await guard.check(async () => { await first(page).getByRole('button', { name: /^Apply to/ }).click(); await expect(first(page)).toContainText('Applied'); await expect(first(page).getByRole('button', { name: 'Undo for AEON-10' })).toBeFocused() })
    await guard.check(async () => { await first(page).getByRole('button', { name: 'Undo for AEON-10' }).click(); await expect(first(page).getByRole('button', { name: /^Apply to/ })).toBeVisible() })
    await guard.check(async () => { await first(page).getByRole('button', { name: 'Dismiss for AEON-10' }).click(); await expect(first(page)).toContainText('Dismissed') })
    guard.done()
    await expect(table(page).locator('.ticket-row:not(.fit-row)')).toHaveCount(3)
    await page.screenshot({ path: testInfo.outputPath(`attention-${width}-${theme}.png`), fullPage: true })
  }
  expect(data.actions[0]).toMatchObject({ action: 'apply', items: [{ event_id: 1, node_id: 'node-0', revision: '2026-10-04T08:00:00Z' }] })
  expect(data.actions[1].items[0]).toMatchObject({ revision: '2026-10-05T08:00:00Z', resolution_event_id: 1001 })
  expect(errors).toEqual([])
})

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark']) test(`bulk Apply has no glow and keeps visible keyboard focus at ${width}px in ${theme}`, async ({ page }, testInfo) => {
  await setup(page)
  await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
  await page.goto('/tickets?view=needs-attention')
  await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
  for (const id of [1, 2, 3]) await table(page).locator(`#row-attention-${id}`).getByRole('checkbox').click()
  const bar = page.getByRole('toolbar', { name: 'Selected tickets' })
  const apply = bar.getByRole('button', { name: 'Apply 3', exact: true })
  const dismiss = bar.getByRole('button', { name: 'Dismiss 3', exact: true })
  await expect(apply).toBeEnabled()
  await expect(apply).toHaveCSS('box-shadow', 'none')
  const guard = await controlStability(page, { apply, dismiss, clear: bar.getByRole('button', { name: 'Clear the selection' }), selection: bar })
  await guard.check(async () => {
    await apply.hover()
    await expect(apply).toHaveCSS('box-shadow', 'none')
  })
  await guard.check(async () => {
    await dismiss.focus()
    await page.keyboard.press('Shift+Tab')
    await expect(apply).toBeFocused()
    expect(await apply.evaluate(el => el.matches(':focus-visible'))).toBe(true)
    await expect(apply).toHaveCSS('box-shadow', 'none')
    await expect(apply).toHaveCSS('outline-style', 'solid')
    await expect(apply).toHaveCSS('outline-width', '2px')
    const outline = await apply.evaluate(el => getComputedStyle(el).outlineColor)
    expect(outline).not.toBe('transparent')
    expect(outline).not.toBe('rgba(0, 0, 0, 0)')
  })
  guard.done()
  await page.screenshot({ path: testInfo.outputPath(`attention-bulk-focus-${width}-${theme}.png`), fullPage: true })
})

test('bulk Apply reports a partial result, retains every row, and Undo all reverses only confirmed writes', async ({ page }) => {
  const data = await setup(page, [row(0), row(1), row(2)], { partial: true })
  await page.goto('/tickets?view=needs-attention')
  await page.getByRole('checkbox', { name: 'Select all loaded tickets' }).click()
  const bar = page.getByRole('toolbar', { name: 'Selected tickets' })
  await expect(bar.getByRole('button', { name: 'Apply 3' })).toBeVisible()
  const stable = await controlStability(page, { search: page.getByRole('searchbox'), row: first(page), actions: first(page).locator('.action-stack') })
  await stable.check(async () => { await bar.getByRole('button', { name: 'Apply 3' }).click(); await expect(first(page)).toContainText('Applied'); await expect(table(page).locator('#row-attention-2')).toContainText('changed') })
  stable.done()
  await expect(bar).toContainText('1 selected')
  await expect(page.locator('.toast.error')).toContainText('AEON-11')
  await page.locator('.toast').getByRole('button', { name: 'Undo all', exact: true }).click()
  await expect(first(page).getByRole('button', { name: /^Apply to/ })).toBeVisible()
  expect(data.actions[1].items.map(item => item.event_id).sort()).toEqual([1, 3])
  await expect(table(page).locator('.ticket-row:not(.fit-row)')).toHaveCount(3)
  await bar.getByRole('button', { name: 'Clear the selection' }).click()
  await expect(bar).toHaveCount(0)
})

test('kind counts, project facets and search filter the URL; scrolling loads the next 50', async ({ page }) => {
  const data = await setup(page, Array.from({ length: 54 }, (_, i) => row(i)))
  await page.goto('/tickets?view=needs-attention&group=none')
  await expect(table(page).locator('.ticket-row:not(.fit-row)')).toHaveCount(50)
  await page.getByRole('button', { name: 'Load 50 more' }).scrollIntoViewIfNeeded()
  await expect(table(page).locator('.ticket-row:not(.fit-row)')).toHaveCount(54)
  expect(data.queries.some(query => query.get('after') === '50')).toBe(true)
  await page.evaluate(() => window.scrollTo(0, 0))
  const guard = await controlStability(page, { search: page.getByRole('searchbox'), facets: page.locator('.facets'), kinds: page.getByRole('group', { name: 'Filter by kind' }) })
  await guard.check(async () => { await page.getByRole('button', { name: /27 cancel suggested/ }).click(); await expect(table(page).locator('.ticket-row:not(.fit-row)')).toHaveCount(27) })
  await guard.check(async () => { await page.getByRole('button', { name: 'Project filter', exact: true }).click(); await page.getByRole('menuitemradio', { name: 'PHAROS Pharos' }).click(); await expect(page).toHaveURL(/project_id=p-pharos/) })
  guard.done()
  await page.getByRole('searchbox').fill('Ticket 2')
  await expect(page).toHaveURL(/q=Ticket\+2|q=Ticket%202/)
  await expect(table(page)).toContainText('Ticket 2')
  await page.getByRole('button', { name: 'Clear filters' }).click()
  await expect(page).toHaveURL('/tickets?view=needs-attention&group=none')
})

test('read-only rows and unavailable release targets explain their permissions before acting', async ({ page }) => {
  const data = await setup(page, [row(0, { editable: false, applicable: false }), row(1, { kind: 'missed', from: 'done', to: 'release', applicable: false, unavailable_reason: 'Choose a planning release in the ticket’s project first.' })])
  await page.goto('/tickets?view=needs-attention')
  await expect(first(page).getByRole('checkbox')).toBeDisabled()
  await expect(first(page).getByRole('button', { name: 'Dismiss for AEON-10' })).toBeDisabled()
  await expect(first(page).getByRole('button', { name: 'Dismiss for AEON-10' })).toHaveAttribute('data-tip', 'Editing this ticket needs permission')
  const missed = table(page).locator('#row-attention-2')
  await expect(missed.getByRole('button', { name: /^Apply to/ })).toBeDisabled()
  await expect(missed.getByRole('button', { name: /^Apply to/ })).toHaveAttribute('data-tip', /Choose a planning release/)
  await missed.getByRole('button', { name: 'Dismiss for AEON-11' }).click()
  expect(data.actions).toHaveLength(1)
})

test('a delayed action cannot resolve a different filtered record or offer stale Undo', async ({ page }) => {
  let release!: () => void, received!: () => void
  const held = new Promise<void>(resolve => { release = resolve }), started = new Promise<void>(resolve => { received = resolve })
  await setup(page, [row(0), row(1)], { held, received })
  await page.goto('/tickets?view=needs-attention')
  await first(page).getByRole('button', { name: 'Dismiss for AEON-10' }).click()
  await started
  await page.getByRole('button', { name: /1 cancel suggested/ }).click()
  await expect(first(page)).toHaveCount(0)
  release()
  await expect(table(page).locator('#row-attention-2').getByRole('button', { name: 'Dismiss for AEON-11' })).toBeVisible()
  await expect(page.locator('.toast').getByRole('button', { name: /^Undo/ })).toHaveCount(0)
})

test('an unavailable queue shows an honest error and Retry without moving its controls', async ({ page }) => {
  const data = await setup(page, [row(0)], { fail: true })
  await page.goto('/tickets?view=needs-attention')
  await expect(page.getByRole('alert')).toContainText('could not be loaded')
  const guard = await controlStability(page, { search: page.getByRole('searchbox'), facets: page.locator('.facets'), navigation: page.locator('.navigation-band') })
  data.recover()
  await guard.check(async () => { await page.getByRole('button', { name: 'Retry', exact: true }).click(); await expect(first(page)).toBeVisible() })
  guard.done()
})

for (const width of [390, 1024, 1440]) test(`partial results keep toasts above selection controls at ${width}px`, async ({ page }, testInfo) => {
 await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
 await setup(page, [row(0), row(1), row(2)], { partial: true })
 await page.goto('/tickets?view=needs-attention')
 for (const id of [1, 2, 3]) await table(page).locator(`#row-attention-${id}`).getByRole('checkbox').click()
 const bar = page.getByRole('toolbar', { name: 'Selected tickets' })
 await bar.getByRole('button', { name: 'Apply 3' }).click()
 await expect(bar).toContainText('1 selected')
 await expect(page.locator('.toast.error')).toContainText('AEON-11')
 for (const hidden of [false, true, false]) {
  await page.locator('.app-shell').evaluate((el, hide) => el.classList.toggle('footer-hidden', hide), hidden)
  await expect.poll(async () => {
    const controls = await bar.boundingBox(), toasts = await page.locator('.toast-host').boundingBox()
    if (!controls || !toasts || controls.height <= 0 || toasts.height <= 0) return false
    return toasts.y + toasts.height <= controls.y - 8
   }).toBe(true)
 }
 for (const theme of ['light', 'dark']) {
  await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
  await page.screenshot({ path: testInfo.outputPath(`attention-partial-${width}-${theme}.png`), fullPage: true })
 }
 await expect(bar.getByRole('button', { name: 'Dismiss 1' })).toBeVisible()
 const offset = await page.locator('.toast-host').evaluate(el => parseFloat(getComputedStyle(el).bottom))
 await bar.getByRole('button', { name: 'Clear the selection' }).click()
 await expect(bar).toHaveCount(0)
 await expect.poll(() => page.locator('.toast-host').evaluate(el => parseFloat(getComputedStyle(el).bottom))).toBeLessThan(offset)
})

for (const width of [390, 1024, 1440]) test(`partial errors leave adjacent actions unobscured and stationary at ${width}px`, async ({ page }, testInfo) => {
 // Keep the two asserted rows adjacent under the new kind/time group ordering.
 const data = await setup(page, [0, 1, 2].map(index => row(index, { project_id: 'p-aeon', kind: 'triage', at: `2026-10-0${6 - index}T08:00:00Z` })), { partial: true })
 await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
 for (const theme of ['light', 'dark']) {
  await page.goto('/tickets?view=needs-attention')
  await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
  const failedRow = table(page).locator('#row-attention-2'), adjacentRow = table(page).locator('#row-attention-3')
  for (const id of [1, 2, 3]) await table(page).locator(`#row-attention-${id}`).getByRole('checkbox').click()
  await adjacentRow.evaluate(el => el.scrollIntoView({ block: 'center' }))
  const guard = await controlStability(page, { failedRow, failedActions: failedRow.locator('.action-stack'), adjacentRow, adjacentActions: adjacentRow.locator('.action-stack') })
  await guard.check(async () => {
   await page.getByRole('toolbar', { name: 'Selected tickets' }).getByRole('button', { name: 'Apply 3' }).click()
   await expect(page.locator('.toast.error')).toContainText('AEON-11: The ticket changed. Reload before trying again.')
   await expect(failedRow).toContainText('The ticket changed. Reload before trying again.')
   await expect(adjacentRow.getByRole('button', { name: 'Undo for AEON-12' })).toBeEnabled()
  })
  const undo = adjacentRow.getByRole('button', { name: 'Undo for AEON-12' })
  await page.screenshot({ path: testInfo.outputPath(`attention-error-actions-${width}-${theme}.png`), fullPage: true })
  // Hit-test the actual control; visibility alone misses feedback painted over it.
  for (const control of [failedRow.getByRole('button', { name: /^Apply to/ }), failedRow.getByRole('button', { name: 'Dismiss for AEON-11' }), undo]) {
   const hit = await control.evaluate(el => {
    const box = el.getBoundingClientRect()
    return [0.25, 0.5, 0.75].map(fraction => { const target = document.elementFromPoint(box.x + box.width * fraction, box.y + box.height / 2); return { ok: !!target && el.contains(target), target: target?.outerHTML.slice(0, 250), x: box.x, y: box.y, width: box.width, height: box.height } })
   })
   expect(hit.every(sample => sample.ok), `adjacent actions receive pointer hits while the error is visible: ${JSON.stringify(hit)}`).toBe(true)
  }
  const before = data.actions.length
  await guard.check(async () => {
   await undo.click({ timeout: 2000 })
   await expect(adjacentRow.getByRole('button', { name: /^Apply to/ })).toBeVisible()
  })
  guard.done()
  expect(data.actions[before]).toMatchObject({ action: 'undo', items: [{ event_id: 3, node_id: 'node-2', resolution_event_id: 1003 }] })
  expect(data.actions[before].items).toHaveLength(1)
 }
})

for (const change of ['person', 'workspace'] as const) test(`${change} switch clears previous facets while the replacement read waits and fails`, async ({ page }) => {
 await setup(page)
 await page.goto('/tickets?view=needs-attention')
 await expect(first(page)).toBeVisible()
 await page.getByRole('button', { name: 'Project filter', exact: true }).click()
 await expect(page.getByRole('menuitemradio', { name: 'PHAROS Pharos' })).toBeVisible()
 await page.keyboard.press('Escape')
 await page.getByRole('button', { name: 'Assignee filter', exact: true }).click()
 await expect(page.getByRole('menuitemradio', { name: 'Previous assignee' })).toBeVisible()
 let release!: () => void, received!: () => void
 const held = new Promise<void>(resolve => { release = resolve }), started = new Promise<void>(resolve => { received = resolve })
 await page.route('**/api/status-autopilot/attention?*', async route => { received(); await held; await route.fulfill({ status: 503, json: { error: 'unavailable' } }) })
 await page.evaluate(async kind => {
  // @ts-expect-error Vite serves the application's module in the isolated browser.
  const { useSession } = await import('/src/stores/session.ts')
  const session = useSession(), who = session.identity!
  session.identity = kind === 'person' ? { ...who, principal: { ...who.principal, id: 'replacement-person' } } : { ...who, tenant: { ...who.tenant, id: 'replacement-workspace' } }
 }, change)
 await started
 await expect(page.getByRole('menuitemradio', { name: 'Previous assignee' })).toHaveCount(0)
 for (const name of ['Project', 'Assignee']) {
  await page.getByRole('button', { name: `${name} filter`, exact: true }).click()
  await expect(page.getByRole('menuitemradio', { name: 'PHAROS Pharos' })).toHaveCount(0)
  await expect(page.getByRole('menuitemradio', { name: 'Previous assignee' })).toHaveCount(0)
  await page.keyboard.press('Escape')
 }
 release()
 await expect(page.getByRole('alert')).toContainText('could not be loaded')
 await page.getByRole('button', { name: 'Project filter', exact: true }).click()
 await expect(page.getByRole('menuitemradio')).toHaveCount(1)
 await page.keyboard.press('Escape')
 await page.getByRole('button', { name: 'Assignee filter', exact: true }).click()
 await expect(page.getByRole('menuitemradio')).toHaveCount(2)
})

test('release receipts refresh the retained row and siblings for subsequent Apply and Undo', async ({ page }) => {
 const data = await setup(page, [row(0, { kind: 'missed', to: 'release', release_id: 'planning-release', release_revision: 7, release_project_revision: 4 }), row(1, { kind: 'missed', to: 'release', release_id: 'planning-release', release_revision: 7, release_project_revision: 4 })])
 await page.goto('/tickets?view=needs-attention')
 await first(page).getByRole('button', { name: /^Apply to/ }).click()
 await expect(first(page)).toContainText('Applied')
 await first(page).getByRole('button', { name: 'Undo for AEON-10' }).click()
 await expect(first(page).getByRole('button', { name: /^Apply to/ })).toBeVisible()
 expect(data.actions[1].items[0]).toMatchObject({ release_id: 'planning-release', release_revision: 8, release_project_revision: 5 })
 await first(page).getByRole('button', { name: /^Apply to/ }).click()
 await expect(first(page)).toContainText('Applied')
 expect(data.actions[2].items[0]).toMatchObject({ release_id: 'planning-release', release_revision: 9, release_project_revision: 6 })
 await table(page).locator('#row-attention-2').getByRole('button', { name: /^Apply to/ }).click()
 await expect(table(page).locator('#row-attention-2')).toContainText('Applied')
 expect(data.actions[3].items[0]).toMatchObject({ release_id: 'planning-release', release_revision: 10, release_project_revision: 7 })
})

test.describe('coarse-pointer attention controls', () => {
 test.use({ hasTouch: true })
 for (const width of [390, 768, 1024, 1440]) for (const theme of ['light', 'dark']) {
  test(`expanded attention targets activate without moving controls at ${width}px in ${theme}`, async ({ page }, testInfo) => {
   await page.setViewportSize({ width: width === 768 ? 1024 : width, height: 1000 })
   await setup(page)
   await page.goto('/tickets?view=needs-attention')
   await expect(first(page)).toBeVisible()
   await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
   await page.evaluate(() => document.fonts.ready)
   if (width === 768) {
    await page.setViewportSize({ width, height: 1000 })
    await expect(page.locator('.stat').first()).toBeVisible()
   }
   expect(await page.evaluate(() => matchMedia('(pointer: coarse)').matches)).toBe(true)
   const targets = page.locator('.stat, .section-tabs a, .view-tabs a, .facet-button, .reset')
   const boxes = await targets.evaluateAll(elements => elements.map(el => {
    const rect = el.getBoundingClientRect(), hit = getComputedStyle(el, '::before')
    const width = parseFloat(hit.width) || rect.width, height = parseFloat(hit.height) || rect.height
    const x = rect.x + (rect.width - width) / 2, y = rect.y + (parseFloat(hit.top) || rect.height / 2) - height / 2
    return { label: el.textContent, x, y, width, height, visualHeight: rect.height,
     edgesHit: [[x + 1, y + height / 2], [x + width - 1, y + height / 2], [x + width / 2, y + 1], [x + width / 2, y + height - 1]].every(([px, py]) => el.contains(document.elementFromPoint(px!, py!))) }
   }))
   expect(boxes).toHaveLength(13)
   for (const box of boxes) {
    expect(box.width, `${box.label} hit width`).toBeGreaterThanOrEqual(44)
    expect(box.height, `${box.label} hit height`).toBeGreaterThanOrEqual(44)
    if (box.x >= 0 && box.x + box.width <= width) expect(box.edgesHit, `${box.label} all target edges activate the control`).toBe(true)
   }
   for (let i = 0; i < boxes.length; i++) for (const b of boxes.slice(i + 1)) {
    const a = boxes[i]!
    const overlapX = Math.min(a.x + a.width, b.x + b.width) - Math.max(a.x, b.x)
    const overlapY = Math.min(a.y + a.height, b.y + b.height) - Math.max(a.y, b.y)
    expect(overlapX > .5 && overlapY > .5, `${a.label} and ${b.label} targets must not overlap`).toBe(false)
   }
   const stat = page.getByRole('button', { name: /1 cancel suggested/ })
   const guard = await controlStability(page, { stat, kinds: page.locator('.stat-line'), sections: page.locator('.section-tabs'), savedViews: page.locator('.view-tabs'), facets: page.locator('.facets'), kind: page.getByRole('button', { name: 'Kind filter', exact: true }), reset: page.getByRole('button', { name: 'Clear filters' }), search: page.getByRole('searchbox') })
   await page.getByRole('button', { name: /1 cancel suggested/ }).scrollIntoViewIfNeeded()
   const cancel = await stat.boundingBox(); expect(cancel).not.toBeNull()
   await guard.check(async () => {
    await page.touchscreen.tap(cancel!.x + cancel!.width / 2, cancel!.y + 1)
    await expect(page).toHaveURL(/kind=cancel/)
    await expect(stat).toHaveAttribute('aria-pressed', 'true')
   })
   await guard.check(async () => {
    await page.getByRole('button', { name: 'Clear filters' }).tap()
    await expect(page).toHaveURL('/tickets?view=needs-attention')
    await expect(stat).toHaveAttribute('aria-pressed', 'false')
   })
   await guard.check(async () => {
    await page.getByRole('button', { name: 'Kind filter', exact: true }).tap()
    await expect(page.getByRole('menu', { name: 'Kind filter' })).toBeVisible()
   })
   await guard.check(async () => { await page.keyboard.press('Escape'); await expect(page.getByRole('menu')).toHaveCount(0) })
   guard.done()
   await page.screenshot({ path: testInfo.outputPath(`attention-touch-${width}-${theme}.png`), fullPage: true })
   const knowledge = boxes[7]!
   await page.touchscreen.tap(knowledge.x + knowledge.width / 2, knowledge.y + 1)
   await expect(page).toHaveURL('/knowledge')
  })
 }
})

test('project groups page independently, remember folds per grouping and keep the clicked header still', async ({ page }) => {
 const data = await setup(page, Array.from({ length: 112 }, (_, index) => row(index)))
 await page.goto('/tickets?view=needs-attention')
 const aeon = page.locator('#row-group-p-aeon'), pharos = page.locator('#row-group-p-pharos')
 await expect(aeon).toHaveAttribute('aria-expanded', 'true')
 await expect(pharos).toHaveAttribute('aria-expanded', 'false')
 await expect(table(page).locator('.ticket-row:not(.fit-row)')).toHaveCount(50)
 expect(data.queries.filter(query => query.get('project_id') === 'p-pharos')).toHaveLength(0)
 const guard = await controlStability(page, { search: page.getByRole('searchbox'), display: page.getByRole('button', { name: /^Display:/ }), header: aeon, toggle: aeon.locator('.group-toggle') })
 await guard.check(async () => { await aeon.getByRole('button', { name: 'Collapse AEON' }).click(); await expect(aeon).toHaveAttribute('aria-expanded', 'false') })
 await guard.check(async () => { await aeon.getByRole('button', { name: 'Expand AEON' }).click(); await expect(aeon).toHaveAttribute('aria-expanded', 'true') })
 guard.done()
 await page.getByRole('button', { name: 'Show 50 more in AEON' }).click()
 await expect(table(page).locator('.ticket-row:not(.fit-row)')).toHaveCount(56)
 await pharos.getByRole('button', { name: 'Expand PHAROS' }).click()
 await expect(table(page).locator('.ticket-row:not(.fit-row)')).toHaveCount(106)
 await expect(page.getByRole('button', { name: 'Show 50 more in PHAROS' })).toBeVisible()
 await aeon.getByRole('button', { name: 'Collapse AEON' }).click()
 await expect.poll(() => data.preferences.get('/api/preferences/needs-attention%3Afolds')).toMatchObject({ project: { 'p-aeon': true, 'p-pharos': false } })
 await page.getByRole('button', { name: /^Display:/ }).click()
 const display = page.getByRole('dialog', { name: 'Display options' })
 const choices = await controlStability(page, { project: display.getByRole('radio', { name: 'Project', exact: true }), kind: display.getByRole('radio', { name: 'Kind', exact: true }), none: display.getByRole('radio', { name: 'None', exact: true }), expand: display.getByRole('button', { name: 'Expand all' }), collapse: display.getByRole('button', { name: 'Collapse all' }) })
 await choices.check(async () => { await display.getByRole('radio', { name: 'Kind', exact: true }).click(); await expect(page).toHaveURL(/group=kind/); await expect(page.locator('#row-group-cancel')).toHaveAttribute('aria-expanded', 'false') })
 await choices.check(async () => { await display.getByRole('radio', { name: 'Project', exact: true }).click(); await expect(page).toHaveURL(/group=project/); await expect(aeon).toHaveAttribute('aria-expanded', 'false'); await expect(pharos).toHaveAttribute('aria-expanded', 'true') })
 choices.done()
 await page.keyboard.press('Escape')
 await page.getByRole('searchbox').fill('Ticket')
 await expect(aeon).toHaveAttribute('aria-expanded', 'true')
 await expect(pharos).toHaveAttribute('aria-expanded', 'true')
})

test('attention keys select a range, preserve native field shortcuts, skip inapplicable Apply and retain confirmed Undo', async ({ page }) => {
 const data = await setup(page, [row(0), row(1, { applicable: false }), row(2)])
 await page.goto('/tickets?view=needs-attention&group=none')
 await expect(first(page)).toBeVisible()
 await table(page).focus()
 await page.keyboard.press('j')
 await expect(table(page)).toHaveAttribute('aria-activedescendant', 'row-attention-3')
 await page.keyboard.press('x')
 await first(page).getByRole('checkbox').click({ modifiers: ['Shift'] })
 await expect(page.getByRole('toolbar', { name: 'Selected tickets' })).toContainText('2 selected')
 await page.keyboard.press('Escape')
 await expect(first(page).getByRole('checkbox')).toBeFocused()
 await page.keyboard.press(process.platform === 'darwin' ? 'Meta+a' : 'Control+a')
 await expect(page.getByRole('toolbar', { name: 'Selected tickets' })).toContainText('3 selected')
 await expect(page.getByRole('toolbar', { name: 'Selected tickets' })).toContainText('1 cannot be applied')
 await page.keyboard.press('a')
 await expect(first(page)).toContainText('Applied')
 expect(data.actions[0].items.map(item => item.event_id)).toEqual([3, 1])
 await expect(page.getByRole('toolbar', { name: 'Selected tickets' })).toContainText('1 selected')
 await page.locator('.toast').getByRole('button', { name: 'Undo all', exact: true }).click()
 await expect(first(page).getByRole('button', { name: /^Apply to/ })).toBeVisible()
 const before = data.actions.length
 await page.getByRole('searchbox').fill('ad')
 await page.keyboard.press(process.platform === 'darwin' ? 'Meta+a' : 'Control+a')
 expect(await page.getByRole('searchbox').evaluate(el => (el as HTMLInputElement).selectionEnd! - (el as HTMLInputElement).selectionStart!)).toBe(2)
 await page.keyboard.press('d')
 expect(data.actions).toHaveLength(before)
 await page.keyboard.press('Escape')
 await expect(page.getByRole('searchbox')).not.toBeFocused()
})

test('shared attention columns keep long keys whole and Since exposes the exact flag time', async ({ page }, testInfo) => {
 await page.addInitScript(() => { const observer = new MutationObserver(() => { if (document.documentElement) { document.documentElement.lang = 'de'; observer.disconnect() } }); observer.observe(document, { childList: true, subtree: true }) })
 await setup(page, [row(0, { key: 'VERY-LONG-PROJECT-KEY-123456' }), row(1), row(2)])
 for (const width of [400, 390, 1024, 1280, 1440]) for (const theme of ['light', 'dark']) {
  await page.setViewportSize({ width, height: width <= 400 ? 1000 : 900 })
  await page.goto('/tickets?view=needs-attention')
  await expect(first(page)).toBeVisible()
  await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
  const key = first(page).getByRole('button', { name: 'Copy VERY-LONG-PROJECT-KEY-123456' })
  expect(await key.evaluate(el => el.scrollWidth <= el.clientWidth + 1)).toBe(true)
  expect(await key.evaluate(el => { const key = el.getBoundingClientRect(), cell = el.closest('td')!.getBoundingClientRect(); return key.right <= cell.right + 1 })).toBe(true)
  await expect(first(page).locator('time')).toHaveAttribute('data-tip', /2026|Oct|October/)
  expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(1)
  await page.screenshot({ path: testInfo.outputPath(`attention-grid-${width}-${theme}-de.png`), fullPage: true })
 }
})
