// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { businessData, mockBusiness } from './business-fixtures'
import { controlStability } from './control-stability'
import type { AttentionItem, AttentionResult } from '../src/lib/attention'

const shots = '/private/tmp/claude-501/-Users-markus-Code-aithema/af3ab8bf-63f6-4fb5-bccf-086eb11c043e/scratchpad/aeon/shots/aeon-697-set'
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
  const live = new Map(items.map(item => [item.event_id, { ...item }]))
  const actions: { action: string; items: AttentionItem[] }[] = [], queries: URLSearchParams[] = []
  let fail = options.fail ?? false
  await page.route('**/api/status-autopilot/attention**', async route => {
    const request = route.request(), url = new URL(request.url())
    if (url.pathname.endsWith('/actions')) {
      const body = request.postDataJSON() as { action: string; items: AttentionItem[] }; actions.push(body)
      const results: AttentionResult[] = body.items.map(item => {
        if (options.partial && item.event_id === 2) return { event_id: 2, ok: false, error: 'The ticket changed. Reload before trying again.' }
        return { event_id: item.event_id, ok: true, resolution_event_id: body.action === 'undo' ? undefined : item.event_id + 1000, revision: body.action === 'undo' ? '2026-10-05T09:00:00Z' : '2026-10-05T08:00:00Z' }
      })
      options.received?.(); if (options.held) await options.held
      return route.fulfill({ json: { items: results } }).catch(() => {})
    }
    queries.push(url.searchParams)
    if (fail) return route.fulfill({ status: 503, json: { error: 'unavailable' } })
    const q = url.searchParams, filtered = [...live.values()].filter(item => (!q.get('kind') || item.kind === q.get('kind')) && (!q.get('project_id') || item.project_id === q.get('project_id')) && (!q.get('q') || item.title.toLowerCase().includes(q.get('q')!.toLowerCase())))
    const offset = q.has('after') ? 50 : 0
    return route.fulfill({ json: { items: filtered.slice(offset, offset + 50), total: filtered.length, counts: { proposed: 0, triage: items.filter(i => i.kind === 'triage').length, cancel: items.filter(i => i.kind === 'cancel').length, blocked: 0, missed: 0 }, next_cursor: filtered.length > offset + 50 ? 'second-page' : null,
      facets: { projects: [{ id: 'p-aeon', label: 'AEON Aeon' }, { id: 'p-pharos', label: 'PHAROS Pharos' }], assignees: [] }, facets_truncated: false } })
  })
  return { actions, queries, recover: () => { fail = false } }
}
const table = (page: Page) => page.getByRole('table', { name: 'Tickets needing attention' })
const first = (page: Page) => table(page).locator('[data-event-id="1"]')

test('Apply, Dismiss and Undo retain rows and control boxes on desktop and phone, in both themes', async ({ page }) => {
  const errors = watchErrors(page)
  const data = await setup(page)
  await mkdir(shots, { recursive: true })
  for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark']) {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
    await page.goto('/tickets?view=needs-attention')
    await expect(first(page)).toBeVisible()
    await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
    const rowActions = first(page).locator('.action-stack')
    const guard = await controlStability(page, { search: page.getByRole('searchbox', { name: 'Search this view' }), filters: page.locator('.facets'), kind: page.getByRole('button', { name: 'Kind filter', exact: true }), navigation: page.locator('.navigation-band'), clickedRow: first(page), actions: rowActions, nextRow: table(page).locator('[data-event-id="2"]') })
    await guard.check(async () => { await first(page).getByRole('button', { name: /^Apply to/ }).click(); await expect(first(page)).toContainText('Applied'); await expect(first(page).getByRole('button', { name: 'Undo for AEON-10' })).toBeFocused() })
    await guard.check(async () => { await first(page).getByRole('button', { name: 'Undo for AEON-10' }).click(); await expect(first(page).getByRole('button', { name: /^Apply to/ })).toBeVisible() })
    await guard.check(async () => { await first(page).getByRole('button', { name: 'Dismiss for AEON-10' }).click(); await expect(first(page)).toContainText('Dismissed') })
    guard.done()
    await expect(table(page).locator('[data-event-id]')).toHaveCount(3)
    await page.screenshot({ path: `${shots}/attention-${width}-${theme}.png`, fullPage: true })
  }
  expect(data.actions[0]).toMatchObject({ action: 'apply', items: [{ event_id: 1, node_id: 'node-0', revision: '2026-10-04T08:00:00Z' }] })
  expect(data.actions[1].items[0]).toMatchObject({ revision: '2026-10-05T08:00:00Z', resolution_event_id: 1001 })
  expect(errors).toEqual([])
})

test('bulk Apply reports a partial result, retains every row, and Undo all reverses only confirmed writes', async ({ page }) => {
  const data = await setup(page, [row(0), row(1), row(2)], { partial: true })
  await page.goto('/tickets?view=needs-attention')
  await page.getByRole('checkbox', { name: 'Select every ticket shown' }).click()
  const bar = page.getByRole('toolbar', { name: 'Selected tickets' })
  await expect(bar.getByRole('button', { name: 'Apply 3' })).toBeVisible()
  const stable = await controlStability(page, { search: page.getByRole('searchbox'), row: first(page), actions: first(page).locator('.action-stack') })
  await stable.check(async () => { await bar.getByRole('button', { name: 'Apply 3' }).click(); await expect(first(page)).toContainText('Applied'); await expect(table(page).locator('[data-event-id="2"]')).toContainText('changed') })
  stable.done()
  await expect(bar).toContainText('1 selected')
  await expect(page.locator('.toast.error')).toContainText('AEON-11')
  await page.locator('.toast').getByRole('button', { name: 'Undo all', exact: true }).click()
  await expect(first(page).getByRole('button', { name: /^Apply to/ })).toBeVisible()
  expect(data.actions[1].items.map(item => item.event_id)).toEqual([1, 3])
  await expect(table(page).locator('[data-event-id]')).toHaveCount(3)
  await bar.getByRole('button', { name: 'Clear the selection' }).click()
  await expect(bar).toHaveCount(0)
})

test('kind counts, project facets and search filter the URL; scrolling loads the next 50', async ({ page }) => {
  const data = await setup(page, Array.from({ length: 54 }, (_, i) => row(i)))
  await page.goto('/tickets?view=needs-attention')
  await expect(table(page).locator('[data-event-id]')).toHaveCount(50)
  await page.getByRole('button', { name: 'Load 50 more' }).scrollIntoViewIfNeeded()
  await expect(table(page).locator('[data-event-id]')).toHaveCount(54)
  expect(data.queries.some(query => query.get('after') === 'second-page')).toBe(true)
  await page.evaluate(() => window.scrollTo(0, 0))
  const guard = await controlStability(page, { search: page.getByRole('searchbox'), facets: page.locator('.facets'), kinds: page.getByRole('group', { name: 'Filter by kind' }) })
  await guard.check(async () => { await page.getByRole('button', { name: /27 cancel suggested/ }).click(); await expect(table(page).locator('[data-event-id]')).toHaveCount(27) })
  await guard.check(async () => { await page.getByRole('button', { name: 'Project filter', exact: true }).click(); await page.getByRole('menuitemradio', { name: 'PHAROS Pharos' }).click(); await expect(page).toHaveURL(/project_id=p-pharos/) })
  guard.done()
  await page.getByRole('searchbox').fill('Ticket 2')
  await expect(page).toHaveURL(/q=Ticket\+2|q=Ticket%202/)
  await expect(table(page)).toContainText('Ticket 2')
  await page.getByRole('button', { name: 'Clear filters' }).click()
  await expect(page).toHaveURL('/tickets?view=needs-attention')
})

test('read-only rows and unavailable release targets explain their permissions before acting', async ({ page }) => {
  const data = await setup(page, [row(0, { editable: false, applicable: false }), row(1, { kind: 'missed', from: 'done', to: 'release', applicable: false, unavailable_reason: 'Choose a planning release in the ticket’s project first.' })])
  await page.goto('/tickets?view=needs-attention')
  await expect(first(page).getByRole('checkbox')).toBeDisabled()
  await expect(first(page).getByRole('button', { name: 'Dismiss for AEON-10' })).toBeDisabled()
  await expect(first(page).getByRole('button', { name: 'Dismiss for AEON-10' })).toHaveAttribute('data-tip', 'Editing this ticket needs permission')
  const missed = table(page).locator('[data-event-id="2"]')
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
  await expect(table(page).locator('[data-event-id="2"]').getByRole('button', { name: 'Dismiss for AEON-11' })).toBeVisible()
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
