// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors, me } from './work-fixtures'
import { businessData, mockBusiness } from './business-fixtures'
import { controlStability } from './control-stability'
import { orderAttentionRows, attentionMoveId, type AttentionItem, type AttentionResult, type AttentionBulkPreview, type AttentionBulkResult } from '../src/lib/attention'

const row = (index: number, extra: Partial<AttentionItem> = {}): AttentionItem => ({
  event_id: index + 1, node_id: `node-${index}`, revision: '2026-10-04T08:00:00Z', key: `AEON-${index + 10}`,
  title: index === 0 ? 'Die vollständigen Abrechnungseinstellungen für sämtliche angeschlossenen Arbeitsbereiche zuverlässig aktualisieren' : `Ticket ${index + 1}`,
  project_id: index % 2 ? 'p-pharos' : 'p-aeon', kind: index % 2 ? 'cancel' : 'triage',
  from: index % 2 ? 'backlog' : 'new', to: index % 2 ? 'cancelled' : 'backlog',
  reason: 'Untouched in this project; review the current suggestion.', at: '2026-10-03T08:00:00Z', editable: true, applicable: true, ...extra,
})
async function setup(page: Page, items = [row(0), row(1), row(2)], options: { fail?: boolean; partial?: boolean; held?: Promise<void>; received?: () => void; groups?: 'absent'; canManage?: boolean } = {}) {
  await mockWork(page, fixtures())
  await mockBusiness(page, businessData({ role: options.canManage ? 'admin' : 'member' }), { role: options.canManage ? 'admin' : 'member' })
  const preferences = new Map<string, unknown>()
  await page.route('**/api/preferences/needs-attention*', async route => {
    const key = new URL(route.request().url()).pathname
    if (route.request().method() === 'PUT') preferences.set(key, route.request().postDataJSON().value)
    await route.fulfill({ json: { value: preferences.get(key) ?? null } })
  })
  const live = new Map(items.map(item => [item.event_id, { ...item }]))
  const overrides = new Map<string, { mode: 'on' | 'off' | 'inherit'; revision: number }>()
  const actions: { action: string; items: AttentionItem[] }[] = [], queries: URLSearchParams[] = [], paths: string[] = []
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
    paths.push(url.pathname)
    queries.push(url.searchParams)
    if (fail) return route.fulfill({ status: 503, json: { error: 'unavailable' } })
    if (options.groups === 'absent' && url.pathname.endsWith('/groups')) return route.fulfill({ status: 404, json: { error: 'not found' } })
    const q = url.searchParams, filtered = [...live.values()].filter(item => (!q.get('kind') || item.kind === q.get('kind')) && (!q.get('project_id') || item.project_id === q.get('project_id')) && (!q.get('q') || item.title.toLowerCase().includes(q.get('q')!.toLowerCase())))
    if (url.pathname.endsWith('/groups')) {
      const by = q.get('by'), ids = [...new Set(filtered.map(item => by === 'kind' ? item.kind : item.project_id))]
      return route.fulfill({ json: { total: filtered.length, truncated: false, groups: ids.map(id => {
        const rows = filtered.filter(item => (by === 'kind' ? item.kind : item.project_id) === id)
        return { id, ...(by === 'kind' ? { kind: id } : { project_id: id, key: id === 'p-aeon' ? 'AEON' : 'PHAROS', title: id === 'p-aeon' ? 'Aeon' : 'Pharos' }), total: rows.length, counts: Object.fromEntries(['proposed', 'triage', 'cancel', 'blocked', 'missed'].map(kind => [kind, rows.filter(row => row.kind === kind).length])), editable: rows.filter(row => row.editable).length, applicable: rows.filter(row => row.applicable && row.editable).length, override_mode: overrides.get(id)?.mode ?? 'on', can_manage: !!options.canManage && rows.some(row => row.editable) }
      }) } })
    }
    const offset = Number(q.get('after') ?? 0)
    return route.fulfill({ json: { items: orderAttentionRows(filtered).slice(offset, offset + 50), total: filtered.length, counts: { proposed: 0, triage: items.filter(i => i.kind === 'triage').length, cancel: items.filter(i => i.kind === 'cancel').length, blocked: 0, missed: 0 }, next_cursor: filtered.length > offset + 50 ? String(offset + 50) : null,
      facets: { projects: [{ id: 'p-aeon', label: 'AEON Aeon' }, { id: 'p-pharos', label: 'PHAROS Pharos' }], assignees: [{ id: 'old-person', label: 'Previous assignee' }] }, facets_truncated: false } })
  })
  return { actions, queries, paths, preferences, live, overrides, recover: () => { fail = false } }
}
const table = (page: Page) => page.getByRole('grid', { name: 'Tickets needing attention' })
const first = (page: Page) => table(page).locator('#row-attention-1')
// R3/R17/R18: whole-group writes, partial results, revision-bound settings
// Undo, and stable controls. Keep real pending rows and resolution history so
// assertions cannot pass by treating aggregate counts as row identities.
async function groupActions(page: Page, items: AttentionItem[]) {
  const data = await setup(page, items, { canManage: true })
  const calls: { path: string; body: Record<string, any>; key: string | undefined }[] = []
  const previews = new Map<string, { body: Record<string, any>; rows: AttentionItem[]; value: AttentionBulkPreview }>()
  const batches = new Map<string, { action: string; rows: AttentionItem[]; result: AttentionBulkResult }>()
  const history: { id: number; actor_principal_id: string; node_id: string; type: string; before: { updated_at: string }; after: { updated_at: string }; undo_of?: number }[] = []
  let counter = 1000, token = 0
  const control = { failEvent: 118, held: undefined as Promise<void> | undefined, received: undefined as (() => void) | undefined, failOverride: false, noise: true, foreign: false }
  const eventQueries: URLSearchParams[] = []
  const offsetSnapshot = (revision: string) => revision.endsWith('Z') ? `${revision.slice(0, -1)}+00:00` : revision
  await page.route('**/api/events?**', route => {
    const q = new URL(route.request().url()).searchParams
    eventQueries.push(new URLSearchParams(q))
    const after = Number(q.get('after'))
    const types = (q.get('type') ?? '').split(',').filter(Boolean)
    const node = q.get('node_id')
    const real = history.filter(event => event.node_id === node && event.id > after && (types.length === 0 || types.includes(event.type)))
    // A busy ticket has more than one oldest-first page before the resolution event.
    const noise = control.noise && types.length === 0 ? Array.from({ length: 201 }, (_, index) => ({
      id: after + 1 + index, actor_principal_id: me.id, node_id: node ?? '', type: 'node.updated',
      before: { updated_at: '2026-10-01T00:00:00+00:00' }, after: { updated_at: '2026-10-01T00:00:01+00:00' },
    })) : []
    const merged = [...noise, ...real].sort((a, b) => a.id - b.id)
    const items = merged.slice(0, 200)
    return route.fulfill({ json: { items, next_after: merged.length > 200 ? items.at(-1)!.id : null } })
  })
  await page.route('**/api/status-autopilot/attention/bulk**', async route => {
    const request = route.request(), path = new URL(request.url()).pathname, body = request.postDataJSON(), key = request.headers()['idempotency-key']
    calls.push({ path, body, key })
    if (path.endsWith('/undo')) {
      const batch = batches.get(path.split('/').at(-2)!)!
      for (const item of batch.rows) {
        const resolution = [...history].reverse().find(event => event.node_id === item.node_id && event.type === `status_autopilot.attention_${batch.action}`)!
        const revision = `2026-10-07T22:00:00.${String(++counter).padStart(6, '0')}Z`
        history.push({ id: counter, actor_principal_id: me.id, node_id: item.node_id, type: 'status_autopilot.attention_undone', before: { updated_at: resolution.after.updated_at }, after: { updated_at: revision }, undo_of: resolution.id })
        data.live.set(item.event_id, { ...item, revision })
      }
      return route.fulfill({ json: { batch_id: path.split('/').at(-2), changed: batch.rows.length, skipped: [], failed: [], completed: true } })
    }
    if (body.dry_run) {
      const rows = [...data.live.values()].filter(row => (!body.scope.project_id || row.project_id === body.scope.project_id) && (!body.scope.kind || row.kind === body.scope.kind))
      const moves = new Map<string, AttentionBulkPreview['moves'][number]>()
      const skip = rows.filter(row => !row.editable || body.action === 'apply' && !row.applicable)
      for (const row of rows.filter(row => !skip.includes(row))) {
        const id = attentionMoveId(row)
        const move = moves.get(id) || { id, kind: row.kind, from: row.from, to: row.to, count: 0, sample_keys: [], release_id: row.release_id, release_title: row.release_title }
        move.count++; if (move.sample_keys.length < 4) move.sample_keys.push(row.key); moves.set(id, move)
      }
      const value: AttentionBulkPreview = { total: rows.length, moves: [...moves.values()], skipped: skip.length ? [{ reason: 'Parent statuses follow their children.', count: skip.length, sample_keys: skip.slice(0, 4).map(row => row.key) }] : [], through_event_id: Math.max(...rows.map(row => row.event_id)), preview_token: `batch-${++token}`, limit: 1000, truncated: false }
      previews.set(value.preview_token, { body, rows: rows.map(row => ({ ...row })), value })
      return route.fulfill({ json: value })
    }
    control.received?.(); if (control.held) await control.held
    const earlier = batches.get(body.preview_token)
    if (earlier) return route.fulfill({ json: earlier.result }).catch(() => {})
    const preview = previews.get(body.preview_token)!
    const candidates = preview.rows.filter(row => row.editable && (body.action === 'dismiss' || row.applicable) && !body.exclude.includes(attentionMoveId(row)))
    const changed = candidates.filter(row => row.event_id !== control.failEvent)
    for (const item of changed) {
      const revision = `2026-10-07T21:00:00.${String(++counter).padStart(6, '0')}Z`
      history.push({ id: counter, actor_principal_id: me.id, node_id: item.node_id, type: `status_autopilot.attention_${body.action}`, before: { updated_at: control.foreign ? '1999-01-01T00:00:00+00:00' : offsetSnapshot(item.revision) }, after: { updated_at: revision } })
      data.live.delete(item.event_id)
    }
    const result: AttentionBulkResult = { batch_id: body.preview_token, changed: changed.length, skipped: preview.value.skipped, failed: candidates.filter(row => row.event_id === control.failEvent).map(row => ({ event_id: row.event_id, key: row.key, error: 'The ticket changed. Reload before trying again.' })), completed: true }
    batches.set(body.preview_token, { action: body.action, rows: changed, result })
    return route.fulfill({ json: result }).catch(() => {})
  })
  await page.route('**/api/projects/*/status-autopilot', route => {
    const request = route.request(), path = new URL(request.url()).pathname, project = path.split('/')[3]!
    const current = data.overrides.get(project) || { mode: 'on' as const, revision: 7 }
    if (request.method() === 'PUT') {
      const body = request.postDataJSON(); calls.push({ path, body, key: undefined })
      if (control.failOverride || body.expected_revision !== current.revision) return route.fulfill({ status: 409, json: { error: 'Another admin changed these settings.' } })
      data.overrides.set(project, { mode: body.mode, revision: current.revision + 1 })
    }
    return route.fulfill({ json: { ...(data.overrides.get(project) || current), effective_enabled: (data.overrides.get(project) || current).mode !== 'off' } })
  })
  let settings = { enabled: true, revision: 11, rules: Object.fromEntries(['new', 'backlog', 'blocked', 'progress', 'done', 'publish', 'accept'].map(rule => [rule, { enabled: true, days: 5 }])) }
  await page.route('**/api/settings/status-autopilot', route => {
    if (route.request().method() === 'PUT') {
      const body = route.request().postDataJSON(); calls.push({ path: '/api/settings/status-autopilot', body, key: undefined })
      if (body.expected_revision !== settings.revision) return route.fulfill({ status: 409, json: { error: 'stale settings' } })
      settings = { enabled: body.enabled, rules: body.rules, revision: settings.revision + 1 }
    }
    return route.fulfill({ json: settings })
  })
  return { ...data, calls, control, eventQueries }
}
// A phone tap opens the ticket. Selection starts with a hold, and only then
// do the round checkboxes exist for the remaining rows.
async function chooseRows(page: Page, ids: number[]) {
  const narrow = (page.viewportSize()?.width ?? 1280) <= 720
  if (!narrow) {
    for (const id of ids) await table(page).locator(`#row-attention-${id}`).getByRole('checkbox').click()
    return
  }
  const row = table(page).locator(`#row-attention-${ids[0]}`)
  await row.evaluate(el => el.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, button: 0, clientX: 24, clientY: 24, pointerId: 1, pointerType: 'touch' })))
  await expect(row.getByRole('checkbox')).toBeVisible()
  await row.evaluate(el => el.dispatchEvent(new PointerEvent('pointerup', { bubbles: true, button: 0, clientX: 24, clientY: 24, pointerId: 1, pointerType: 'touch' })))
  for (const id of ids.slice(1)) await table(page).locator(`#row-attention-${id}`).getByRole('checkbox').click()
}

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
  await chooseRows(page, [1, 2, 3])
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
 await chooseRows(page, [1, 2, 3])
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
  await chooseRows(page, [1, 2, 3])
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
 await expect(aeon.locator('.group-toggle')).toHaveAttribute('aria-expanded', 'true')
 await expect(pharos.locator('.group-toggle')).toHaveAttribute('aria-expanded', 'false')
 await expect(table(page).locator('.ticket-row:not(.fit-row)')).toHaveCount(50)
 expect(data.queries.filter(query => query.get('project_id') === 'p-pharos')).toHaveLength(0)
 const guard = await controlStability(page, { search: page.getByRole('searchbox'), display: page.getByRole('button', { name: /^Display:/ }), header: aeon, toggle: aeon.locator('.group-toggle') })
 await guard.check(async () => { await aeon.getByRole('button', { name: 'Collapse AEON' }).click(); await expect(aeon.locator('.group-toggle')).toHaveAttribute('aria-expanded', 'false') })
 await guard.check(async () => { await aeon.getByRole('button', { name: 'Expand AEON' }).click(); await expect(aeon.locator('.group-toggle')).toHaveAttribute('aria-expanded', 'true') })
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
 await choices.check(async () => { await display.getByRole('radio', { name: 'Kind', exact: true }).click(); await expect(page).toHaveURL(/group=kind/); await expect(page.locator('#row-group-cancel').locator('.group-toggle')).toHaveAttribute('aria-expanded', 'false') })
 await choices.check(async () => { await display.getByRole('radio', { name: 'Project', exact: true }).click(); await expect(page).toHaveURL(/group=project/); await expect(aeon.locator('.group-toggle')).toHaveAttribute('aria-expanded', 'false'); await expect(pharos.locator('.group-toggle')).toHaveAttribute('aria-expanded', 'true') })
 choices.done()
 await page.keyboard.press('Escape')
 await page.getByRole('searchbox').fill('Ticket')
 await expect(aeon.locator('.group-toggle')).toHaveAttribute('aria-expanded', 'true')
 await expect(pharos.locator('.group-toggle')).toHaveAttribute('aria-expanded', 'true')
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
  await key.scrollIntoViewIfNeeded()
  await page.screenshot({ path: testInfo.outputPath(`attention-grid-${width}-${theme}-de.png`), fullPage: true })
 }
})

test('a missing groups route still groups the list and leaves Group by None on the list endpoint', async ({ page }) => {
 const data = await setup(page, [row(0), row(1), row(2)], { groups: 'absent' })
 await page.goto('/tickets?view=needs-attention&kind=triage')
 await expect(page.locator('#row-group-p-aeon')).toBeVisible()
 await expect(page.locator('#row-group-p-pharos')).toHaveCount(0)
 await expect(page.getByRole('alert')).toHaveCount(0)
 await expect(table(page).locator('.ticket-row:not(.fit-row)')).toHaveCount(2)
 expect(data.paths.some(path => path.endsWith('/groups'))).toBe(true)
 expect(data.queries.some(query => query.get('kind') === 'triage' && !query.has('by'))).toBe(true)
 await page.goto('/tickets?view=needs-attention')
 await expect(page.locator('#row-group-p-aeon')).toBeVisible()
 await expect(page.locator('#row-group-p-pharos')).toBeVisible()
 await expect(table(page).locator('.ticket-row:not(.fit-row)')).toHaveCount(3)
 const before = data.paths.length
 await page.getByRole('button', { name: /^Display:/ }).click()
 await page.getByRole('radio', { name: 'None', exact: true }).click()
 await expect(page).toHaveURL(/group=none/)
 await expect(table(page).locator('.ticket-row:not(.fit-row)')).toHaveCount(3)
 expect(data.paths.slice(before).some(path => path.endsWith('/groups'))).toBe(false)
})

test('a phone tap opens the ticket', async ({ page }) => {
 await page.setViewportSize({ width: 390, height: 844 })
 await setup(page)
 await page.goto('/tickets?view=needs-attention')
 await expect(first(page)).toBeVisible()
 await first(page).locator('.attention-title').click()
 await expect(page).toHaveURL(/\/projects\/AEON\/tickets\/AEON-10/)
 await expect(page.getByRole('toolbar', { name: 'Selected tickets' })).toHaveCount(0)
})

// R18: a held selection must keep all phone actions reachable, expose the
// complete key and leave the scroll viewport above the pinned selection bar.
test('phone selection keeps rows reachable, keys whole and zero-apply controls absent', async ({ page }, testInfo) => {
 await page.setViewportSize({ width: 390, height: 844 })
 const data = await setup(page, Array.from({ length: 20 }, (_, index) => row(index, { project_id: 'p-aeon', applicable: false, key: index === 0 ? 'VERY-LONG-PROJECT-KEY-123456789' : `AEON-${index + 10}` })))
 await page.goto('/tickets?view=needs-attention')
 await expect(first(page)).toBeVisible()
 const head = page.locator('#row-group-p-aeon')
 await expect.soft(head.getByRole('button', { name: 'Apply all in AEON' })).toHaveCount(0)
 const initial = await controlStability(page, { clickedRow: first(page), key: first(page).locator('.key-btn'), actions: first(page).locator('.action-stack') })
 await initial.check(async () => { await chooseRows(page, [1]) })
 initial.done()
 await table(page).locator('#row-attention-2').getByRole('checkbox').click()
 const bar = page.getByRole('toolbar', { name: 'Selected tickets' })
 await expect(bar).toContainText('2 selected')
 await expect.soft(bar.getByRole('button', { name: /^Apply/ })).toHaveCount(0)
 const clear = bar.getByRole('button', { name: 'Clear the selection' })
 // The dock is fixed to the viewport. Its DOM ancestors scroll, so sample its
 // viewport box directly; the shared guard measures the rows in scroll space.
 const dock = (await bar.boundingBox())!, clearBox = (await clear.boundingBox())!
 const guard = await controlStability(page, { clickedRow: first(page), rowActions: first(page).locator('.action-stack') })
 await guard.check(async () => { await table(page).locator('#row-attention-3').getByRole('checkbox').click(); await expect(bar).toContainText('3 selected') })
 guard.done()
 for (const [control, before] of [[bar, dock], [clear, clearBox]] as const) {
  const after = (await control.boundingBox())!
  for (const dimension of ['x', 'y', 'width', 'height'] as const) expect(Math.abs(after[dimension] - before[dimension]), `selection ${dimension}`).toBeLessThanOrEqual(.5)
 }
 const key = first(page).getByRole('button', { name: 'Copy VERY-LONG-PROJECT-KEY-123456789' })
 expect(await key.evaluate(el => { const box = el.getBoundingClientRect(), cell = el.closest('td')!.getBoundingClientRect(); return el.scrollWidth <= el.clientWidth + 1 && box.right <= cell.right + 1 })).toBe(true)
 for (const target of [key, first(page).getByRole('checkbox'), head.locator('.group-toggle'), head.locator('.group-check-target')]) {
  const box = (await target.boundingBox())!
  expect(box.width).toBeGreaterThanOrEqual(44)
  expect(box.height).toBeGreaterThanOrEqual(44)
 }
 await table(page).locator('#row-attention-20').scrollIntoViewIfNeeded()
 const barTop = (await bar.boundingBox())!.y
 expect.soft(await page.locator('#main').evaluate(el => el.getBoundingClientRect().bottom)).toBeLessThanOrEqual(barTop + .5)
 const last = (await table(page).locator('#row-attention-20').boundingBox())!
 expect.soft(last.y + last.height).toBeLessThanOrEqual(barTop + .5)
 await page.screenshot({ path: testInfo.outputPath(`attention-phone-selection.png`), fullPage: true })
 await clear.click()
 expect(data.actions).toEqual([])
 await head.getByRole('button', { name: 'Collapse AEON' }).click()
 await expect(head.locator('.group-toggle')).toHaveAttribute('aria-expanded', 'false')
 await groupActions(page, [row(0, { project_id: 'p-aeon' })])
 await page.goto('/tickets?view=needs-attention')
 await head.getByRole('button', { name: 'Apply all in AEON' }).click()
 const preview = page.getByRole('dialog', { name: 'Apply all in AEON' })
 const cancel = preview.getByRole('button', { name: 'Cancel', exact: true }), moves = preview.getByRole('group', { name: 'Proposed changes', exact: true })
 const previewGuard = await controlStability(page, { frame: preview, cancel, moves, clickedMove: moves.locator('label') })
 await previewGuard.check(async () => { await moves.getByRole('checkbox').uncheck() })
 previewGuard.done()
 await expect.soft(preview.locator('.run')).toBeHidden()
 await page.screenshot({ path: testInfo.outputPath(`attention-phone-no-moves.png`), fullPage: true })
 await cancel.click()
})

test('a filtered empty queue says nothing matches and keeps Clear filters', async ({ page }) => {
 await setup(page, [])
 await page.goto('/tickets?view=needs-attention')
 await expect(page.locator('.state h2')).toHaveText('Nothing here needs attention.')
 await page.goto('/tickets?view=needs-attention&q=zzzz-no-match')
 const state = page.locator('.state')
 await expect(state.locator('h2')).toHaveText('No tickets match these filters')
 await expect(state.getByRole('button', { name: 'Clear filters' })).toBeVisible()
 await state.getByRole('button', { name: 'Clear filters' }).click()
 await expect(page).toHaveURL('/tickets?view=needs-attention')
 await expect(page.locator('.state h2')).toHaveText('Nothing here needs attention.')
})

test('group preview, partial bulk Undo and project/rule Undo preserve scope, rights and control positions', async ({ page }, testInfo) => {
  test.setTimeout(120000)
  const errors = watchErrors(page)
  const kinds = ['proposed', 'triage', 'cancel', 'blocked', 'missed'] as const
  const items = Array.from({ length: 120 }, (_, index) => row(index, { project_id: 'p-aeon', kind: kinds[index % 5], from: index % 5 === 0 ? 'open' : index % 5 === 1 ? 'new' : 'backlog', to: index % 5 === 4 ? 'release' : index % 5 === 2 ? 'cancelled' : 'backlog', ...(index % 5 === 4 ? { release_id: 'r-plan', release_title: 'Zuverlässige Zusammenarbeit für sämtliche Arbeitsbereiche', release_revision: 3, release_project_revision: 4 } : {}), ...(index === 3 ? { applicable: false, unavailable_reason: 'Parent statuses follow their children.' } : {}) }))
  items.push(row(120, { project_id: 'p-pharos', editable: false, applicable: false }))
  const data = await groupActions(page, items)
  const head = page.locator('#row-group-p-aeon')
  const more = () => head.getByRole('button', { name: 'More for AEON' })
  const apply = () => head.getByRole('button', { name: 'Apply all in AEON' })
  const preview = () => page.getByRole('dialog', { name: 'Apply all in AEON' })
  await page.goto('/tickets?view=needs-attention&assignee=none')
  await expect(first(page)).toBeVisible()
  const loaded = await table(page).locator('.ticket-row:not(.fit-row)').count()
  expect(loaded).toBe(50)
  // Binding brief requires these artifacts. Stability is measured for every
  // move selector and disclosure; there is no screenshot/pixel equality gate.
  for (const width of [1440, 1280, 1024, 400, 390]) for (const theme of ['light', 'dark']) {
    await page.setViewportSize({ width, height: 1000 })
    await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
    await apply().click()
    const pop = preview()
    await expect(pop).toBeVisible()
    const selectors = pop.getByRole('group', { name: 'Proposed changes', exact: true })
    const triage = pop.getByRole('checkbox', { name: /^triage:/ })
    const guard = await controlStability(page, { ...(width <= 600 ? { frame: pop } : {}), apply: pop.locator('.run'), cancel: pop.getByRole('button', { name: 'Cancel' }), selectors, clickedMove: triage.locator('..'), more: more() })
    await guard.check(async () => { await triage.uncheck(); await expect(pop.locator('.run')).toHaveText('Apply95') })
    await guard.check(async () => { await pop.locator('summary').click(); await expect(pop).toContainText('Parent statuses follow their children.') })
    guard.done()
    await page.screenshot({ path: testInfo.outputPath(`attention-group-preview-${width}-${theme}.png`), fullPage: true })
    await page.keyboard.press('Escape')
    await expect(apply()).toBeFocused()
    await more().click()
    await page.screenshot({ path: testInfo.outputPath(`attention-group-menu-${width}-${theme}.png`), fullPage: true })
    await page.getByRole('menuitem', { name: /Turn autopilot off in AEON/ }).click()
    const offPop = page.getByRole('dialog', { name: 'Turn autopilot off in AEON?' })
    await expect(offPop.getByRole('button', { name: 'Turn off', exact: true })).toBeEnabled()
    const offStable = await controlStability(page, { ...(width <= 600 ? { frame: offPop } : {}), turnOff: offPop.getByRole('button', { name: 'Turn off', exact: true }), cancel: offPop.getByRole('button', { name: 'Cancel' }), checkbox: offPop.getByRole('checkbox').locator('..') })
    await offStable.check(async () => { await offPop.getByRole('checkbox').check() })
    offStable.done()
    await page.screenshot({ path: testInfo.outputPath(`attention-group-autopilot-off-${width}-${theme}.png`), fullPage: true })
    await page.keyboard.press('Escape')
  }
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.evaluate(() => { document.documentElement.dataset.theme = 'light' })
  await apply().click()
  await preview().getByRole('checkbox', { name: /^triage:/ }).uncheck()
  let release!: () => void, received!: () => void
  data.control.held = new Promise<void>(resolve => { release = resolve })
  const arrived = new Promise<void>(resolve => { received = resolve })
  data.control.received = received
  const guard = await controlStability(page, { more: more(), slot: head.locator('.group-slot'), clickedRow: first(page), rowActions: first(page).locator('.action-stack') })
  await preview().locator('.run').click()
  await arrived
  await expect(head.getByRole('status')).toHaveText('Applying…')
  await expect(first(page).getByRole('button', { name: /^Apply to/ })).toBeDisabled()
  const readOnly = page.locator('#row-group-p-pharos')
  await expect(readOnly).toContainText('View only')
  await expect(readOnly.getByRole('button', { name: 'More for PHAROS' })).toBeEnabled()
  release(); data.control.held = undefined; data.control.received = undefined
  await guard.check(async () => { await expect(first(page).locator('.action-stack')).toHaveAttribute('data-resolved', 'apply'); await expect(more()).toBeEnabled() })
  guard.done()
  await expect(table(page).locator('#row-attention-118')).toContainText('The ticket changed')
  await expect(table(page).locator('#row-attention-2 .action-stack')).not.toHaveAttribute('data-resolved', 'apply')
  await expect(table(page).locator('.ticket-row:not(.fit-row)')).toHaveCount(loaded)
  const run = data.calls.find(call => call.body.dry_run === false)!
  expect(run.body).toMatchObject({ action: 'apply', scope: { project_id: 'p-aeon', assignee: 'none' }, exclude: ['triage'], through_event_id: 120 })
  expect(run.key).toBeTruthy()
  await page.locator('.toast:not(.toast-leave-active)').getByRole('button', { name: 'Undo all', exact: true }).click()
  await expect(first(page).getByRole('button', { name: /^Apply to/ })).toBeVisible()
  await expect(more()).toBeEnabled()
  expect(data.calls.some(call => /\/bulk\/batch-\d+\/undo$/.test(call.path))).toBe(true)
  data.control.failEvent = 0
  await apply().focus()
  await page.keyboard.press('d')
  const dismissPreview = page.getByRole('dialog', { name: 'Dismiss all in AEON' })
  await expect(dismissPreview).toContainText('The tickets stay as they are.')
  await dismissPreview.locator('.run').click()
  await expect(head).toContainText('120 dismissed')
  await expect(more()).toBeEnabled()
  await head.getByRole('button', { name: 'Undo', exact: true }).click()
  await expect(first(page).getByRole('button', { name: /^Apply to/ })).toBeVisible()
  await expect(more()).toBeEnabled()
  await readOnly.getByRole('button', { name: 'More for PHAROS' }).click()
  await expect(page.getByRole('menu').getByRole('menuitem')).toHaveCount(1)
  await page.getByRole('menuitem', { name: 'Open PHAROS tickets' }).click()
  await expect(page).toHaveURL(/\/p\/PHAROS\/tickets\?assignee=none/)
  await page.goto('/tickets?view=needs-attention&assignee=none')
  await expect(first(page)).toBeVisible()

  await more().click()
  await page.getByRole('menuitem', { name: /Turn autopilot off in AEON/ }).click()
  const off = page.getByRole('dialog', { name: 'Turn autopilot off in AEON?' })
  const also = off.getByRole('checkbox')
  await expect(also).not.toBeChecked()
  const offGuard = await controlStability(page, { turnOff: off.getByRole('button', { name: 'Turn off', exact: true }), cancel: off.getByRole('button', { name: 'Cancel' }), checkbox: also.locator('..') })
  await offGuard.check(async () => { await also.check() })
  await offGuard.check(async () => { await also.uncheck() })
  offGuard.done()
  const bulkBefore = data.calls.filter(call => call.body.dry_run === false).length
  await off.getByRole('button', { name: 'Turn off', exact: true }).click()
  await expect(head).toContainText('Autopilot off')
  await expect(more()).toBeEnabled()
  expect(data.calls.filter(call => call.body.dry_run === false)).toHaveLength(bulkBefore)
  expect(data.calls.find(call => call.body.mode === 'off')?.body.expected_revision).toBe(7)
  await page.locator('.toast:not(.toast-leave-active)').getByRole('button', { name: 'Undo', exact: true }).click()
  await expect(head).not.toContainText('Autopilot off')
  expect(data.calls.filter(call => call.body.mode).at(-1)?.body).toMatchObject({ mode: 'on', expected_revision: 8 })

  await more().click()
  await page.getByRole('menuitem', { name: /Turn autopilot off in AEON/ }).click()
  await off.getByRole('checkbox').check()
  await off.getByRole('button', { name: 'Turn off', exact: true }).click()
  await expect(head).toContainText('120 dismissed')
  await expect(more()).toBeEnabled()
  await expect(first(page).locator('.action-stack')).toHaveAttribute('data-resolved', 'dismiss')
  await page.locator('.toast:not(.toast-leave-active)').getByRole('button', { name: 'Undo', exact: true }).click()
  await expect(first(page).getByRole('button', { name: /^Apply to/ })).toBeVisible()
  await expect(head).not.toContainText('Autopilot off')
  await expect(more()).toBeEnabled()
  // Failed settings writes retain the actual mode and do not dismiss flags.
  data.control.failOverride = true
  await more().click()
  await page.getByRole('menuitem', { name: /Turn autopilot off in AEON/ }).click()
  await off.getByRole('button', { name: 'Turn off', exact: true }).click()
  await expect(page.locator('.toast.error')).toContainText('Another admin changed')
  await expect(head).not.toContainText('Autopilot off')
  data.control.failOverride = false
  await more().click()
  await page.getByRole('menuitem', { name: /Turn autopilot off in AEON/ }).click()
  await off.getByRole('button', { name: 'Turn off', exact: true }).click()
  await expect(more()).toBeEnabled()
  await more().click()
  await page.getByRole('menuitem', { name: 'Follow the workspace again' }).click()
  await expect(head).not.toContainText('Autopilot off')
  expect(data.calls.filter(call => call.body.mode).at(-1)?.body.mode).toBe('inherit')
  await page.locator('.toast:not(.toast-leave-active)').filter({ hasText: 'AEON follows the workspace again.' }).getByRole('button', { name: 'Undo', exact: true }).click()
  await expect(head).toContainText('Autopilot off')

  await page.goto('/tickets?view=needs-attention&group=kind')
  const triageHead = page.locator('#row-group-triage')
  await expect(triageHead).toBeVisible()
  await triageHead.getByRole('button', { name: 'More for Triage list' }).click()
  await page.getByRole('menuitem', { name: /Stop listing for triage/ }).click()
  await expect(page.locator('.toast:not(.toast-leave-active)').filter({ hasText: 'Status autopilot saved.' })).toBeVisible()
  const rule = data.calls.filter(call => call.path === '/api/settings/status-autopilot').at(-1)!
  expect(rule.body).toMatchObject({ expected_revision: 11, enabled: true, rules: { new: { enabled: false }, backlog: { enabled: true } } })
  await page.locator('.toast:not(.toast-leave-active)').getByRole('button', { name: 'Undo', exact: true }).click()
  await expect(page.locator('.toast:not(.toast-leave-active)').filter({ hasText: 'The previous rule setting was restored.' })).toBeVisible()
  expect(data.calls.filter(call => call.path === '/api/settings/status-autopilot').at(-1)?.body).toMatchObject({ expected_revision: 12, rules: { new: { enabled: true } } })
  data.overrides.set('p-aeon', { mode: 'on', revision: 20 })
  await page.addInitScript(() => { const observer = new MutationObserver(() => { if (document.documentElement) { document.documentElement.lang = 'de'; observer.disconnect() } }); observer.observe(document, { childList: true, subtree: true }) })
  for (const width of [1440, 1280, 1024, 400, 390]) for (const theme of ['light', 'dark']) {
    await page.setViewportSize({ width, height: 1000 })
    await page.goto('/tickets?view=needs-attention')
    await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
    await head.getByRole('button', { name: 'Alles anwenden in AEON' }).click()
    const dePreview = page.getByRole('dialog', { name: 'Alles anwenden in AEON' })
    await expect(dePreview).toBeVisible()
    const stability = await controlStability(page, { ...(width <= 600 ? { frame: dePreview } : {}), run: dePreview.locator('.run'), cancel: dePreview.getByRole('button', { name: 'Abbrechen' }), moves: dePreview.getByRole('group', { name: 'Vorgeschlagene Änderungen', exact: true }) })
    await stability.check(async () => { await dePreview.getByRole('checkbox', { name: /^triage:/ }).uncheck() })
    stability.done()
    await page.screenshot({ path: testInfo.outputPath(`attention-group-preview-${width}-${theme}-de.png`), fullPage: true })
    await page.keyboard.press('Escape')
    await head.getByRole('button', { name: 'Mehr zu AEON' }).click()
    await expect(page.getByRole('menu')).toBeVisible()
    await page.screenshot({ path: testInfo.outputPath(`attention-group-menu-${width}-${theme}-de.png`), fullPage: true })
    await page.getByRole('menuitem', { name: /Autopilot in AEON ausschalten/ }).click()
    const deOff = page.getByRole('dialog', { name: 'Autopilot in AEON ausschalten?' })
    await expect(deOff.getByRole('button', { name: 'Ausschalten', exact: true })).toBeEnabled()
    const offStable = await controlStability(page, { ...(width <= 600 ? { frame: deOff } : {}), turnOff: deOff.getByRole('button', { name: 'Ausschalten', exact: true }), cancel: deOff.getByRole('button', { name: 'Abbrechen' }), checkbox: deOff.getByRole('checkbox').locator('..') })
    await offStable.check(async () => { await deOff.getByRole('checkbox').check() })
    offStable.done()
    await page.screenshot({ path: testInfo.outputPath(`attention-group-autopilot-off-${width}-${theme}-de.png`), fullPage: true })
    await page.keyboard.press('Escape')
  }
  expect(errors).toEqual([])
})

test('a held group run cannot write a new visit or leave its Undo active there', async ({ page }) => {
  const data = await groupActions(page, [row(0), row(1)])
  await page.goto('/tickets?view=needs-attention')
  await page.locator('#row-group-p-aeon').getByRole('button', { name: 'Apply all in AEON' }).click()
  const preview = page.getByRole('dialog', { name: 'Apply all in AEON' })
  await expect(preview).toBeVisible()
  let release!: () => void, arrived!: () => void
  data.control.held = new Promise<void>(resolve => { release = resolve })
  const received = new Promise<void>(resolve => { arrived = resolve })
  data.control.received = arrived
  await preview.locator('.run').click()
  await received
  await page.getByRole('button', { name: /1 cancel suggested/ }).click()
  await expect(page.locator('#row-group-p-aeon')).toHaveCount(0)
  release()
  await expect(page.locator('#row-group-p-pharos')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Undo all', exact: true })).toHaveCount(0)
  await expect(page.locator('#row-attention-2 .action-stack')).not.toHaveAttribute('data-resolved', 'apply')
})

test('a group apply binds an offset snapshot after more than 200 later events and offers Undo', async ({ page }) => {
  const data = await groupActions(page, [row(0)])
  await page.goto('/tickets?view=needs-attention')
  await page.locator('#row-group-p-aeon').getByRole('button', { name: 'Apply all in AEON' }).click()
  await page.getByRole('dialog', { name: 'Apply all in AEON' }).locator('.run').click()
  await expect(first(page).locator('.action-stack')).toHaveAttribute('data-resolved', 'apply')
  await expect(first(page).getByRole('button', { name: 'Undo for AEON-10' })).toBeVisible()
  expect(data.eventQueries.some(query => query.get('type') === 'status_autopilot.attention_apply,status_autopilot.attention_dismiss,status_autopilot.attention_undone' && query.get('limit') === '200')).toBe(true)
})

test('a group row with no matching resolution reports the refresh failure instead of staying on Apply', async ({ page }) => {
  const data = await groupActions(page, [row(0)])
  data.control.noise = false
  data.control.foreign = true
  await page.goto('/tickets?view=needs-attention')
  await page.locator('#row-group-p-aeon').getByRole('button', { name: 'Apply all in AEON' }).click()
  await page.getByRole('dialog', { name: 'Apply all in AEON' }).locator('.run').click()
  await expect(first(page)).toContainText('could not be refreshed')
  await expect(first(page).locator('.action-stack')).not.toHaveAttribute('data-resolved', 'apply')
  await expect(first(page).getByRole('button', { name: 'Undo for AEON-10' })).toBeHidden()
})
