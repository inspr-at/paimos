// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { controlStability } from './control-stability'
import type { WorkspaceActivityItem } from '../src/lib/workspaceActivity'

const title = 'Projektübergreifende Entwicklungszusammenarbeit und langfristige Qualitätssicherung der gemeinsam betriebenen Infrastruktur'
const reason = 'Seit drei Tagen gibt es weder eine aktive Sitzung noch eine Änderung am Arbeitszweig oder eine offene Prüfung. Der Vorgang ist wieder zur Bearbeitung verfügbar.'
function responseBarrier() {
  let started!: () => void, release!: () => void
  const began = new Promise<void>(resolve => { started = resolve })
  const wait = new Promise<void>(resolve => { release = resolve })
  return { started, release, began, wait }
}
function entry(id: number, patch: Partial<WorkspaceActivityItem> = {}): WorkspaceActivityItem {
  return { event_id: id, node_id: `n-${id}`, project_id: 'p-pharos', key: `PHAROS-${id}`, title, actor: 'Status autopilot', type: 'status_autopilot.changed', rule: 'progress', reason, from: 'in_progress', to: 'open', at: '2026-10-04T12:00:00Z', revision: '2026-10-04T12:00:00Z', automatic: true, undone: false, changed_since: false, undoable: true, requires_preview: false, ...patch }
}
async function setup(page: Page, theme = 'light') {
  const errors = watchErrors(page), data = fixtures({ admin: true })
  data.preferences.theme = { choice: theme }
  await mockWork(page, data, { admin: true })
  await mockSettings(page, settingsData())
  const entries = [entry(11), entry(12, { rule: 'accept', from: 'delivered', to: 'accepted' }), entry(13, { type: 'node.updated', automatic: false, actor: 'Markus Barta', undoable: false, rule: '', reason: '' })]
  const older = entry(14, { at: '2026-10-03T08:00:00Z', undoable: false, changed_since: true })
  const calls: URLSearchParams[] = [], undoCalls: number[] = []
  let failOlder = false
  let olderBarrier: ReturnType<typeof responseBarrier> | undefined
  let held: { id: number; started: () => void; wait: Promise<void>; finished: () => void } | undefined
  await page.route('**/api/events/activity?*', async route => {
    const q = new URL(route.request().url()).searchParams
    calls.push(q)
    if (q.get('cursor')) {
      if (olderBarrier) { olderBarrier.started(); await olderBarrier.wait }
      if (failOlder) return route.fulfill({ status: 503, json: { code: 'read_timeout' } })
      return route.fulfill({ json: { items: [older], next_cursor: null } })
    }
    const visible = entries.filter(item => (!q.get('rule') || item.rule === q.get('rule')) && (q.get('view') !== 'automatic' || item.automatic) && (q.get('view') !== 'people' || !item.automatic) && (q.get('view') !== 'agents' || item.automatic) && (!q.get('q') || `${item.title} ${item.key}`.includes(q.get('q')!)))
    return route.fulfill({ json: { items: visible, next_cursor: q.get('rule') || q.get('q') ? null : 'older-bound-to-filters' } })
  })
  await page.route('**/api/events/*/undo', async route => {
    const id = Number(/events\/(\d+)\//.exec(route.request().url())?.[1])
    undoCalls.push(id)
    if (held?.id === id) { held.started(); await held.wait }
    if (id === 12) return route.fulfill({ status: 409, json: { code: 'conflict' } })
    const row = entries.find(item => item.event_id === id)!
    row.undone = true; row.undoable = false
    await route.fulfill({ status: 201, json: { id: 100, undo_of: id } })
    if (held?.id === id) held.finished()
  })
  return { calls, undoCalls, errors, failOlder: (value: boolean) => { failOlder = value }, holdOlder: (value: typeof olderBarrier) => { olderBarrier = value }, holdUndo: (value: typeof held) => { held = value } }
}

for (const theme of ['light', 'dark']) for (const width of [390, 1024, 1440]) {
  test(`Activity matches the approved responsive layout and holds controls still at ${width} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme: theme })
    const mock = await setup(page, theme)
    await page.goto('/activity?view=automatic')
    const activity = page.locator('.activity-page'), views = page.getByRole('navigation', { name: 'Activity views' })
    const row = activity.locator('[data-event-id="11"]'), undo = row.getByRole('button', { name: /^Undo:/ })
    await expect(undo).toBeVisible()
    await page.evaluate(() => document.fonts.ready)
    await page.screenshot({ path: `test-results/aeon-698-nb/activity-${width}-${theme}.png`, fullPage: true })
    const guard = await controlStability(page, {
      views, everything: views.getByRole('button', { name: 'Everything', exact: true }), automatic: views.getByRole('button', { name: 'Automatic changes', exact: true }),
      search: page.getByRole('searchbox', { name: 'Search activity' }), rule: page.getByRole('combobox', { name: 'Rule', exact: true }), project: page.getByRole('combobox', { name: 'Project', exact: true }), reload: activity.getByRole('button', { name: 'Reload', exact: true }),
    })
    await guard.check(async () => { await page.getByRole('combobox', { name: 'Rule', exact: true }).selectOption('progress'); await expect(activity.locator('[data-event-id="12"]')).toHaveCount(0) })
    await guard.check(async () => { await page.getByRole('combobox', { name: 'Project', exact: true }).selectOption('p-pharos'); await expect.poll(() => mock.calls.at(-1)?.get('project_id')).toBe('p-pharos') })
    await guard.check(async () => { await views.getByRole('button', { name: 'People', exact: true }).click(); await expect(activity.getByText('No changes match this view.')).toBeVisible() })
    await guard.check(async () => { await views.getByRole('button', { name: 'Automatic changes', exact: true }).click(); await expect(undo).toBeVisible() })
    await guard.check(async () => { await page.getByRole('searchbox', { name: 'Search activity' }).fill('No matching ticket'); await expect(activity.getByText('No changes match this view.')).toBeVisible() })
    await guard.check(async () => { await page.getByRole('searchbox', { name: 'Search activity' }).fill(''); await expect(undo).toBeVisible() })
    guard.done()
    const undoGuard = await controlStability(page, { row, action: row.locator('.undo-stack') })
    await undoGuard.check(async () => { await undo.click(); await expect(row.getByRole('status')).toHaveText('Undone') })
    undoGuard.done()
    expect(mock.undoCalls).toEqual([11])
    if (width === 390) expect((await row.locator('.undo-stack').boundingBox())!.height).toBeGreaterThanOrEqual(44)
    await page.goto('/settings/autopilot')
    const showAll = page.getByRole('link', { name: 'Show all', exact: true })
    await expect(showAll).toBeVisible()
    await showAll.scrollIntoViewIfNeeded()
    await page.screenshot({ path: `test-results/aeon-698-nb/settings-show-all-${width}-${theme}.png` })
    expect(mock.errors).toEqual([])
  })
}

for (const width of [390, 1024, 1440]) test(`older-page failure and retry keep retained rows and Undo still at ${width}`, async ({ page }) => {
  await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
  const mock = await setup(page)
  const failure = responseBarrier()
  mock.holdOlder(failure)
  mock.failOlder(true)
  await page.goto('/activity?view=automatic')
  await failure.began
  await expect(page.locator('[data-event-id="11"]')).toBeVisible()
  await page.evaluate(() => document.fonts.ready)
  const first = page.locator('[data-event-id="11"]'), second = page.locator('[data-event-id="12"]')
  const pageGuard = await controlStability(page, {
    firstRow: first, firstUndo: first.getByRole('button', { name: /^Undo:/ }),
    secondRow: second, secondUndo: second.getByRole('button', { name: /^Undo:/ }),
    loadOlder: page.getByRole('button', { name: 'Load older entries', exact: true }),
  })
  await pageGuard.check(async () => {
    failure.release()
    await expect(page.getByRole('alert')).toContainText('Retry the same page')
  })
  expect(mock.calls.filter(q => q.get('cursor')).map(q => q.get('cursor'))).toEqual(['older-bound-to-filters'])
  const recovery = responseBarrier()
  mock.holdOlder(recovery)
  mock.failOlder(false)
  await pageGuard.check(async () => {
    await page.getByRole('button', { name: 'Retry', exact: true }).click()
    await recovery.began
    await expect(page.getByRole('alert')).toHaveCount(0)
    await expect(page.getByText('Loading older entries…', { exact: true })).toBeVisible()
  })
  pageGuard.done()
  // The pagination control is removed at the end of history; retained rows
  // and their Undo actions must stay still when the recovered page appends.
  const retainedGuard = await controlStability(page, {
    firstRow: first, firstUndo: first.getByRole('button', { name: /^Undo:/ }),
    secondRow: second, secondUndo: second.getByRole('button', { name: /^Undo:/ }),
  })
  await retainedGuard.check(async () => { recovery.release(); await expect(page.locator('[data-event-id="14"]')).toBeVisible() })
  retainedGuard.done()
  expect(mock.calls.filter(q => q.get('cursor')).map(q => q.get('cursor'))).toEqual(['older-bound-to-filters', 'older-bound-to-filters'])
  const row = page.locator('[data-event-id="12"]')
  const nextRow = page.locator('[data-event-id="14"]')
  const guard = await controlStability(page, { row, action: row.locator('.undo-stack'), nextRow, nextAction: nextRow.locator('.undo-stack') })
  await guard.check(async () => {
    await row.getByRole('button', { name: /^Undo:/ }).click()
    await expect(page.locator('.toast.error')).toHaveText('Undo for PHAROS-12: The ticket changed since this move. Undo was not applied.')
  })
  guard.done()
  await expect(row.locator('.undo-state')).toHaveText('Changed since')
  await expect(row.getByRole('status')).toHaveCount(0)
  expect(mock.undoCalls).toEqual([12]); expect(mock.errors).toEqual([])
})

for (const width of [390, 1024, 1440]) test(`delayed project-filter failure keeps retained rows and Undo still at ${width}`, async ({ page }) => {
  await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
  const mock = await setup(page)
  const failure = responseBarrier()
  await page.route('**/api/projects', async route => {
    failure.started()
    await failure.wait
    await route.fulfill({ status: 503, json: { code: 'read_timeout' } })
  })
  await page.goto('/activity?view=automatic&rule=progress')
  await failure.began
  const row = page.locator('[data-event-id="11"]')
  await expect(row).toBeVisible()
  await page.evaluate(() => document.fonts.ready)
  const guard = await controlStability(page, {
    row, undo: row.getByRole('button', { name: /^Undo:/ }),
    project: page.getByRole('combobox', { name: 'Project', exact: true }),
    reload: page.getByRole('button', { name: 'Reload', exact: true }),
  })
  await guard.check(async () => {
    failure.release()
    await expect(page.getByRole('alert')).toHaveText('Project filters could not be loaded.')
  })
  guard.done()
  expect(mock.errors).toEqual([])
})

test('Show all opens the automatic view and a delayed Undo never marks a different view', async ({ page }) => {
  const mock = await setup(page)
  await page.goto('/settings/autopilot')
  const showAll = page.getByRole('link', { name: 'Show all', exact: true })
  await expect(showAll).toBeVisible()
  await showAll.click()
  await expect(page).toHaveURL(/\/activity\?view=automatic/)
  let release!: () => void, began!: () => void, sent!: () => void
  const wait = new Promise<void>(resolve => { release = resolve }), started = new Promise<void>(resolve => { began = resolve }), finished = new Promise<void>(resolve => { sent = resolve })
  mock.holdUndo({ id: 11, wait, started: began, finished: sent })
  await page.locator('[data-event-id="11"]').getByRole('button', { name: /^Undo:/ }).click()
  await started
  await page.getByRole('navigation', { name: 'Activity views' }).getByRole('button', { name: 'People', exact: true }).click()
  await expect(page.locator('[data-event-id="13"]')).toBeVisible()
  release()
  await finished
  await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => resolve())))
  await expect(page.locator('.activity-page').getByRole('status')).toHaveCount(0)
  await expect(page.locator('[data-event-id="13"]')).not.toContainText('Undone')
  expect(mock.undoCalls).toEqual([11]); expect(mock.errors).toEqual([])
})
