// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { expect, test, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { expectStableControls } from './helpers/stable'
import type { PlanningItem } from '../src/lib/deliveryPlanning'
import type { ReleaseRecord } from '../src/lib/releaseActions'
test.use({ timezoneId: 'Europe/Vienna' })
const id = (n: number) => `00000000-0000-4000-8000-${String(n).padStart(12, '0')}`
const first = id(1), later = id(2), frozen = id(3), cut = id(4), short = id(5), abandoned = id(6), internalFrozen = id(7), building = id(8)
const long = 'Langfristige Verbesserungen für nachvollziehbare und gemeinsame Releaseplanung'
const make = (release_id: string, title: string, state: ReleaseRecord['state'] = 'planned', visibility: ReleaseRecord['visibility'] = 'internal'): ReleaseRecord => ({ release_id, project_id: 'p-pharos', title, display_name: title, body: 'Plan changes in the open.', state, visibility, revision: 1, rank: release_id.slice(-3) + 'V', entry_closes_at: null, rollup: { units: 2, completed: 1, open_hours: 1 }, build_summary: { budget_outlook: 'unknown' } })
const defaults = { budget_agent_hours: 10, max_agents: 3, largest_ticket_hours: 4, window: { timezone: 'Europe/Vienna', slots: [{ days: [1,2,3,4,5], from: '20:00', to: '02:00' }] } }
async function setup(page: Page, options: { width?: number; theme?: string; agent?: boolean; deploy?: boolean; failBatch?: number; failState?: boolean; incomplete?: boolean; failRead?: boolean; staleDetail?: boolean; product?: boolean; missingNotes?: boolean } = {}) {
  await page.setViewportSize({ width: options.width ?? 1440, height: 1000 })
  const data = fixtures(); data.preferences.theme = { choice: options.theme ?? 'light' }; data.preferences['header-graph'] = { enabled: false }
  await mockWork(page, data, { principalKind: options.agent ? 'agent' : 'person' })
  const errors = watchErrors(page), writes: { path: string; body: Record<string, any> }[] = []
  const releases = [make(first, long), make(later, 'Silver Signal'), make(frozen, 'Copper Crown', 'frozen', 'published'), { ...make(cut, 'Amber Atlas', 'frozen', 'published'), version_scheme: 'legacy', version: '2.3.0', cut_at: '2026-10-04T12:00:00Z' }, make(short, 'Audit', 'planned'), make(abandoned, 'Abandoned documentation pass', 'abandoned')]
  releases.push(make(internalFrozen, 'Documentation pass', 'frozen'), make(building, 'Research pass', 'building'))
  const candidates: (PlanningItem & { source: 'unplaced' | 'later'; index: number; placed?: boolean })[] = Array.from({ length: 203 }, (_, i) => ({ item_id: id(1000 + i), key: `PHAROS-${1000+i}`, project_id: 'p-pharos', release_id: i < 120 ? undefined : later, rank: i < 120 ? undefined : `${i}V`, revision: i < 120 ? 0 : 2, node_revision: '2026-10-04T12:00:00Z', title: `${long} · ${i+1}`, kind: 'ticket', state: 'done', created_at: '2026-10-04T12:00:00Z', estimated_hours: 1, expedite: false, due_on: null, source: i < 120 ? 'unplaced' : 'later', index: i }))
  await page.route('**/api/me/permissions**', route => {
    const project = new URL(route.request().url()).searchParams.get('project_id') ?? undefined
    const permissions = mockEffectivePermissions('member', project)
    permissions.workspace.permissions.push('releases.read', ...(options.deploy === false ? [] : ['releases.deploy']))
    return route.fulfill({ json: permissions })
  })
  await page.route('**/api/projects/p-pharos/**', async route => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname, query = url.searchParams
    if (request.method() !== 'GET') {
      const body = request.postDataJSON(); writes.push({ path, body })
      if (path.endsWith('/ships-in/batch')) {
        const batch = writes.filter(row => row.path.endsWith('/ships-in/batch')).length
        if (options.failBatch === batch) return route.fulfill({ status: 409, json: { code: 'entry_closed', error: 'Entry closed during Include.' } })
        const target = releases.find(row => row.release_id === body.release_id)!
        if (body.expected_release_revision !== target.revision) return route.fulfill({ status: 409, json: { code: 'revision_changed', error: 'Release revision changed.' } })
        const selected = candidates.filter(row => body.items.some((it: { id: string }) => it.id === row.item_id))
        expect(selected).toHaveLength(body.items.length); expect(selected.length).toBeLessThanOrEqual(100)
        for (const row of selected) row.placed = true
        target.revision++; target.rollup.units += selected.length; target.rollup.completed += selected.length
        return route.fulfill({ json: { items: selected.map(row => ({ item_id: row.item_id, project_id: row.project_id, release_id: target.release_id, revision: row.revision+1, rank: 'ZV', expedite: false, due_on: null })), release_revision: target.revision, release_revisions: { [target.release_id]: target.revision }, undo_event_id: 700 + batch } })
      }
      const target = releases.find(row => row.release_id === path.split('/').at(request.method() === 'PATCH' ? -1 : -2))
      if (path.endsWith('/releases')) {
        const planned = make(id(9), body.title, 'planned', body.visibility); planned.entry_closes_at = body.entry_closes_at; releases.push(planned); return route.fulfill({ json: planned })
      }
      if (!target) return route.fulfill({ status: 404, json: { error: 'Missing fixture release' } })
      if (body.expected_revision !== target.revision || options.failState && path.endsWith('/state')) return route.fulfill({ status: 409, json: { code: 'revision_changed', error: 'This release changed; reopen its actions.' } })
      if (request.method() === 'PATCH') Object.assign(target, { title: body.title, body: body.body, ...(body.visibility ? { visibility: body.visibility } : {}), ...(body.entry_closes_at !== undefined ? { entry_closes_at: body.entry_closes_at } : {}) })
      if (path.endsWith('/state')) target.state = body.to
      if (path.endsWith('/cut')) Object.assign(target, { version: body.version, version_scheme: body.version_scheme, cut_at: '2026-10-04T13:00:00Z' })
      if (path.endsWith('/close') || path.endsWith('/publish')) target.state = 'released'
      target.revision++
      const omitted = candidates.filter(row => !row.placed)
      return route.fulfill({ json: { ...target, undo_event_id: null, recovery: { completed_unplaced: omitted.filter(row => row.source === 'unplaced').length, completed_later: omitted.filter(row => row.source === 'later').length, incomplete: false } } })
    }
    if (path.endsWith('/delivery')) return route.fulfill({ json: { project_id: 'p-pharos', mode: 'releases', revision: 1, product_project: options.product === true, build_defaults: defaults } })
    if (path.endsWith('/overview')) return route.fulfill({ json: { active: releases.filter(row => ['planned','building','frozen'].includes(row.state)), released: { items: releases.filter(row => row.state === 'released') }, abandoned: 1, backlog: { ranked: 0, tail: 0 }, counts_incomplete: false } })
    if (path.endsWith('/releases')) return route.fulfill({ json: { items: releases.filter(row => query.get('state') === 'abandoned' ? row.state === 'abandoned' : row.state !== 'abandoned') } })
    if (path.endsWith('/note-snapshot')) return route.fulfill({ json: options.missingNotes ? { source: 'unavailable', gaps: ['Membership was never captured.'], items: [] } : { schema: 'aeon.release-note-snapshot.v1', project_node_id: 'p-pharos', release_node_id: path.split('/').at(-2), frozen: false, tickets: [{ key: 'PHAROS-51', fields: { pill_en: 'Clear context', benefit_en: 'Plans keep the work visible.' } }, { key: 'PHAROS-52', fields: { hide_from_release_notes: true, pill_en: 'Hidden pill', benefit_en: 'Hidden benefit' } }] } })
    if (path.endsWith('/items') || path.endsWith('/backlog')) {
      if (options.failRead) return route.fulfill({ status: 503, json: { error: 'Recovery read failed; try again.' } })
      const source = query.has('completed_later') ? 'later' : 'unplaced'
      const rows = candidates.filter(row => !row.placed && row.source === source), cursor = Number(query.get('cursor') ?? -1), limit = Number(query.get('limit') ?? 100)
      const pageRows = rows.filter(row => row.index > cursor).slice(0, limit)
      const next = pageRows.length && rows.some(row => row.index > pageRows.at(-1)!.index) ? String(pageRows.at(-1)!.index) : ''
      return route.fulfill({ json: { items: pageRows, count: rows.length, incomplete: !!options.incomplete, next_cursor: next } })
    }
    const target = releases.find(row => row.release_id === path.split('/').at(-1))
    if (target) return route.fulfill({ json: { ...target, revision: target.revision + Number(!!options.staleDetail), build_settings: {}, resolved_build_settings: defaults, setting_sources: {} } })
    return route.fallback()
  })
  await page.goto('/p/pharos?section=releases')
  await expect(page.getByRole('button', { name: `Actions for ${long}`, exact: true })).toBeEnabled()
  return { writes, releases, candidates, errors }
}
async function open(page: Page, release = first) {
  await page.locator(`[data-release-id="${release}"]`).getByRole('button', { name: /^Actions for / }).click()
  const dialog = page.locator('dialog.release-sheet'); await expect(dialog).toBeVisible(); return dialog
}
async function choose(page: Page, name: string, release = first) {
  const dialog = await open(page, release); await dialog.getByRole('button', { name, exact: true }).click()
  if (name === 'Settings…') await expect(dialog.getByLabel('Release name', { exact: true })).toBeEnabled()
  if (name === 'Freeze') await expect(dialog.getByRole('button', { name: /^Include \(/ })).toBeEnabled()
  return dialog
}
async function shot(page: Page, name: string, width: number, theme: string) {
  mkdirSync('test-results/aeon-596-p6d', { recursive: true })
  await page.screenshot({ path: `test-results/aeon-596-p6d/${name}-${width}-${theme}.png` })
}
for (const width of [390,1024,1440]) for (const theme of ['light','dark']) test(`settings, lifecycle and Freeze stability ${width} ${theme}`, async ({ page }) => {
  const world = await setup(page, { width, theme })
  let dialog = await open(page)
  await expectStableControls({ controls: { done: dialog.getByRole('button', { name: /^Done/ }), options: dialog.getByRole('group', { name: 'Release actions', exact: true }), settings: dialog.getByRole('button', { name: 'Settings…', exact: true }), ...(width === 390 ? { frame: dialog } : {}) }, scrollAreas: { body: dialog.locator('.sheet-body'), dialog }, interactions: [{ name: 'refusal explanation', run: () => dialog.getByRole('button', { name: 'Build', exact: true }).focus() }, { name: 'permitted explanation', run: () => dialog.getByRole('button', { name: 'Settings…', exact: true }).focus() }] })
  await shot(page, 'menu', width, theme)
  await dialog.getByRole('button', { name: 'Settings…', exact: true }).click(); await expect(dialog.getByLabel('Release name', { exact: true })).toBeEnabled()
  await expectStableControls({ controls: { save: dialog.getByRole('button', { name: /^Save / }), cancel: dialog.getByRole('button', { name: /^Cancel/ }), kind: dialog.getByLabel('Release kind', { exact: true }), deadline: dialog.getByLabel('Entry closes', { exact: true }), name: dialog.getByLabel('Release name', { exact: true }), budget: dialog.getByLabel('Budget', { exact: true }), reset: dialog.getByRole('button', { name: 'Reset Budget', exact: true }), group: dialog.getByRole('group', { name: 'Release settings', exact: true }), ...(width === 390 ? { frame: dialog } : {}) }, scrollAreas: { body: dialog.locator('.sheet-body'), dialog }, interactions: [{ name: 'override budget', run: () => dialog.getByLabel('Override Budget', { exact: true }).check() }, { name: 'tighten budget', run: () => dialog.getByLabel('Budget', { exact: true }).fill('5') }, { name: 'reset budget', run: () => dialog.getByRole('button', { name: 'Reset Budget', exact: true }).click() }, { name: 'published kind', run: () => dialog.getByLabel('Release kind', { exact: true }).selectOption('published') }, { name: 'internal kind', run: () => dialog.getByLabel('Release kind', { exact: true }).selectOption('internal') }] })
  await dialog.getByLabel('Entry closes', { exact: true }).fill('2026-10-05T20:00'); await shot(page, 'settings', width, theme)
  await dialog.getByRole('button', { name: /^Cancel/ }).click()
  dialog = await choose(page, 'Freeze')
  await expect(dialog.getByRole('heading', { name: /^Completed, not in any release/ })).toBeAttached()
  await expect(dialog.getByRole('heading', { name: /^Completed in a later release/ })).toBeAttached()
  expect(await dialog.locator('.recovery-row').count()).toBeLessThanOrEqual(200)
  const firstRow = dialog.locator('.recovery-row').first(), firstCheck = firstRow.locator('input')
  await expectStableControls({ controls: { frame: dialog, include: dialog.getByRole('button', { name: /^Include \(/ }), freeze: dialog.getByRole('button', { name: /^Freeze / }), cancel: dialog.getByRole('button', { name: /^Cancel/ }), choices: dialog.getByRole('group', { name: 'Recovery choices', exact: true }), all: dialog.getByLabel('All completed work', { exact: true }), row: firstRow }, scrollAreas: { body: dialog.locator('.sheet-body'), dialog }, interactions: [{ name: 'exclude one visible row', run: () => firstCheck.uncheck() }, { name: 'include row again', run: () => firstCheck.check() }, { name: 'all off', run: () => dialog.getByLabel('All completed work', { exact: true }).uncheck() }, { name: 'all on', run: () => dialog.getByLabel('All completed work', { exact: true }).check() }] })
  await shot(page, 'freeze', width, theme)
  await expectStableControls({ controls: { frame: dialog, include: dialog.getByRole('button', { name: /^Include \(/ }), freeze: dialog.getByRole('button', { name: /^Freeze / }), cancel: dialog.getByRole('button', { name: /^Cancel/ }) }, scrollAreas: { body: dialog.locator('.sheet-body'), dialog }, interactions: [{ name: 'scroll the recovery body', run: () => dialog.locator('.sheet-body').evaluate(el => { el.scrollTop = el.scrollHeight }) }] })
  await dialog.getByRole('button', { name: /^Cancel/ }).click()
  dialog = await choose(page, 'Abandon')
  const actionBox = await dialog.getByRole('group', { name: 'Release sheet actions', exact: true }).boundingBox()
  await shot(page, 'abandon', width, theme); await dialog.getByRole('button', { name: /^Cancel/ }).click()
  dialog = await choose(page, 'Abandon', short)
  const nextBox = await dialog.getByRole('group', { name: 'Release sheet actions', exact: true }).boundingBox()
  for (const axis of ['x','y','width','height'] as const) expect(Math.abs(nextBox![axis] - actionBox![axis])).toBeLessThanOrEqual(.5)
  await dialog.getByRole('button', { name: /^Cancel/ }).click()
  dialog = await choose(page, 'Cut', frozen)
  await expectStableControls({ controls: { scheme: dialog.getByLabel('Version scheme', { exact: true }), version: dialog.getByLabel('Reserved version', { exact: true }), confirm: dialog.getByRole('button', { name: /^Cut / }), cancel: dialog.getByRole('button', { name: /^Cancel/ }), choices: dialog.getByRole('group', { name: 'Cut version', exact: true }), ...(width === 390 ? { frame: dialog } : {}) }, scrollAreas: { body: dialog.locator('.sheet-body'), dialog }, interactions: [{ name: 'legacy', run: () => dialog.getByLabel('Version scheme', { exact: true }).selectOption('legacy') }, { name: 'calendar v1', run: () => dialog.getByLabel('Version scheme', { exact: true }).selectOption('inspr-calendar-v1') }, { name: 'calendar v2', run: () => dialog.getByLabel('Version scheme', { exact: true }).selectOption('inspr-calendar-v2') }] })
  await shot(page, 'cut', width, theme); await dialog.getByRole('button', { name: /^Cancel/ }).click()
  dialog = await choose(page, 'Publish', cut); await shot(page, 'publish', width, theme); await dialog.getByRole('button', { name: /^Cancel/ }).click()
  for (const [action, rid, file] of [['Close',internalFrozen,'close'], ['Unfreeze',internalFrozen,'unfreeze'], ['Mark building',first,'mark-building'], ['Return to planned',building,'return-planned']] as const) {
    dialog = await choose(page, action, rid)
    await expectStableControls({ controls: { confirm: dialog.locator('.sheet-actions .primary'), cancel: dialog.getByRole('button', { name: /^Cancel/ }), group: dialog.getByRole('group', { name: 'Release sheet actions', exact: true }), ...(width === 390 ? { frame: dialog } : {}) }, scrollAreas: { dialog }, interactions: [{ name: 'focus confirmation', run: () => dialog.locator('.sheet-actions .primary').focus() }] })
    await shot(page, file, width, theme); await dialog.getByRole('button', { name: /^Cancel/ }).click()
  }
  dialog = await choose(page, 'Notes', frozen); await expect(dialog.getByText('Plans keep the work visible.')).toBeVisible()
  await expectStableControls({ controls: { done: dialog.getByRole('button', { name: /^Done/ }) }, scrollAreas: { dialog }, interactions: [{ name: 'focus notes', run: () => dialog.getByRole('button', { name: /^Done/ }).focus() }] })
  await shot(page, 'notes', width, theme); await dialog.getByRole('button', { name: /^Done/ }).click()
  await page.getByRole('button', { name: 'Abandoned', exact: true }).click(); dialog = page.locator('dialog.release-sheet'); await expect(dialog.getByText('Abandoned documentation pass', { exact: true })).toBeVisible()
  await expectStableControls({ controls: { done: dialog.getByRole('button', { name: /^Done/ }), load: dialog.getByRole('button', { name: 'Load more', exact: true }), retry: dialog.getByRole('button', { name: 'Retry', exact: true }) }, scrollAreas: { dialog }, interactions: [{ name: 'report refresh', run: async () => { await dialog.getByRole('button', { name: 'Retry', exact: true }).click(); await expect(dialog.getByRole('button', { name: 'Retry', exact: true })).toBeEnabled() } }] })
  await shot(page, 'abandoned', width, theme); await dialog.getByRole('button', { name: /^Done/ }).click()
  await page.locator('.toolbar-wrap').getByRole('button', { name: /^New/ }).click(); dialog = page.locator('dialog.release-sheet'); await expect(dialog).toHaveAttribute('aria-label','Plan a release')
  await expectStableControls({ controls: { kind: dialog.getByLabel('Release kind', { exact: true }), name: dialog.getByLabel('Release name', { exact: true }), deadline: dialog.getByLabel('Entry closes', { exact: true }), save: dialog.locator('.sheet-actions .primary'), cancel: dialog.getByRole('button', { name: /^Cancel/ }), ...(width === 390 ? { frame: dialog } : {}) }, scrollAreas: { dialog }, interactions: [{ name: 'internal plan', run: () => dialog.getByLabel('Release kind', { exact: true }).selectOption('internal') }, { name: 'published plan', run: () => dialog.getByLabel('Release kind', { exact: true }).selectOption('published') }] })
  await dialog.getByLabel('Release name', { exact: true }).fill(long); await shot(page, 'plan', width, theme); await dialog.getByRole('button', { name: /^Cancel/ }).click()
  expect(world.writes).toEqual([]); expect(world.errors).toEqual([])
})
test('paged Include commits only the prefix before a refusal, then Freeze uses its new revision and clears Undo', async ({ page }) => {
  const world = await setup(page, { failBatch: 2 }), dialog = await choose(page, 'Freeze')
  await dialog.getByRole('button', { name: /^Include \(203\)/ }).click()
  await expect(dialog.locator('.sheet-feedback')).toContainText('Entry has closed')
  // Error and committed counts must both remain visible.
  await expect(dialog).toContainText('100 placed · 103 remain listed · Include stopped')
  expect(world.writes.map(row => row.body.items?.length)).toEqual([100,20])
  expect(world.writes.map(row => row.body.expected_release_revision)).toEqual([1,2])
  await expect(page.locator('.delivery-notice button')).toBeDisabled()
  await dialog.getByRole('button', { name: /^Freeze / }).click(); await expect(dialog).not.toBeVisible()
  expect(world.writes.at(-1)?.body).toEqual({ expected_revision: 2, to: 'frozen' })
  await expect(page.locator(`[data-release-id="${first}"] .release-status`)).toHaveText('Frozen')
  await expect(page.locator('.delivery-notice button')).toBeDisabled()
})
test('All paginates both reads in batches at most 100 and keeps omissions unchecked', async ({ page }) => {
  const world = await setup(page), dialog = await choose(page, 'Freeze')
  await dialog.getByLabel('Include PHAROS-1000', { exact: true }).uncheck()
  await dialog.getByRole('button', { name: /^Include \(202\)/ }).click()
  await expect(dialog).toContainText('202 placed · 1 remain listed.')
  expect(world.writes.map(row => row.body.items.length)).toEqual([99,20,83])
  expect(world.writes.flatMap(row => row.body.items).some(row => row.id === id(1000))).toBe(false)
  await expect(dialog.getByLabel('Include PHAROS-1000', { exact: true })).not.toBeChecked()
})
test('deadline save/clear is explicit, native shortcuts stay native, Escape leaves a field first', async ({ page }) => {
  const world = await setup(page), dialog = await choose(page, 'Settings…')
  await dialog.getByLabel('Entry closes', { exact: true }).fill('2026-10-05T20:00')
  await dialog.getByLabel('Release name', { exact: true }).focus(); await page.keyboard.press('Control+a'); await page.keyboard.press('s')
  expect(world.writes).toEqual([])
  await page.keyboard.press('Escape'); await expect(dialog).toBeVisible(); await page.keyboard.press('Escape'); await expect(dialog).not.toBeVisible()
  await choose(page, 'Settings…'); await dialog.getByLabel('Entry closes', { exact: true }).fill('2026-10-05T20:00'); await dialog.getByRole('button', { name: /^Save / }).click(); await expect(dialog).not.toBeVisible()
  expect(world.writes[0]?.body.entry_closes_at).toBe('2026-10-05T18:00:00.000Z'); expect(world.writes[0]?.body.expected_revision).toBe(1)
  await choose(page, 'Settings…'); await dialog.getByLabel('Entry closes', { exact: true }).fill(''); await dialog.getByRole('button', { name: /^Save / }).click(); await expect(dialog).not.toBeVisible()
  expect(world.writes[1]?.body.entry_closes_at).toBeNull(); expect(world.writes[1]?.body.expected_revision).toBe(2)
})
test('stale detail and final Freeze conflict report errors without success or retargeting', async ({ page }) => {
  const world = await setup(page, { staleDetail: true }); let dialog = await open(page)
  await dialog.getByRole('button', { name: 'Settings…', exact: true }).click(); await expect(dialog.locator('.sheet-feedback')).toContainText('changed')
  await expect(dialog.getByRole('button', { name: /^Save / })).toBeDisabled(); expect(world.writes).toEqual([])
  await dialog.getByRole('button', { name: /^Cancel/ }).click()
  const next = await setup(page, { failState: true }); dialog = await choose(page, 'Freeze')
  await dialog.getByRole('button', { name: /^Freeze / }).click(); await expect(dialog.locator('.sheet-feedback')).toContainText('changed')
  await expect(page.locator(`[data-release-id="${first}"] .release-status`)).toHaveText('Planned')
  expect(next.writes).toHaveLength(1); expect(next.writes[0]?.path).toContain(first)
})
test('agent sees person-only refusals but may freeze without including completed work', async ({ page }) => {
  const world = await setup(page, { agent: true }), dialog = await open(page)
  await expect(dialog.getByRole('button', { name: 'Settings…', exact: true })).toHaveAttribute('aria-disabled','true')
  await dialog.getByRole('button', { name: 'Settings…', exact: true }).focus(); await expect(dialog).toContainText('Only a person')
  await dialog.getByRole('button', { name: 'Freeze', exact: true }).click()
  await expect(dialog.getByRole('button', { name: /^Freeze / })).toBeEnabled(); await expect(dialog.getByRole('button', { name: /^Include \(/ })).toBeDisabled()
  await expect(dialog.getByLabel('All completed work', { exact: true })).toBeDisabled()
  await dialog.getByRole('button', { name: /^Freeze / }).click(); await expect(dialog).not.toBeVisible()
  expect(world.writes).toHaveLength(1); expect(world.writes[0]?.body.to).toBe('frozen'); await expect(page.locator('.move-feedback')).toContainText('203 completed items left out')
})
test('deploy permission is required; capped and failed recovery reads are honest', async ({ page }) => {
  const world = await setup(page, { deploy: false }), dialog = await open(page)
  await expect(dialog.getByRole('button', { name: 'Freeze', exact: true })).toHaveAttribute('aria-disabled','true')
  await dialog.getByRole('button', { name: 'Freeze', exact: true }).focus(); await expect(dialog).toContainText('deployment permission')
  await dialog.getByRole('button', { name: /^Done/ }).click(); expect(world.writes).toEqual([])
  await setup(page, { incomplete: true }); await choose(page, 'Freeze'); await expect(dialog.getByLabel('All completed work', { exact: true })).toBeDisabled(); await expect(dialog).toContainText('incomplete list')
  await dialog.getByRole('button', { name: /^Cancel/ }).click()
  await setup(page, { failRead: true }); await open(page); await dialog.getByRole('button', { name: 'Freeze', exact: true }).click(); await expect(dialog.locator('.sheet-feedback')).toContainText('Recovery read failed'); await expect(dialog.getByRole('button', { name: /^Include \(/ })).toBeDisabled()
})

test('notes omit hidden fields and distinguish uncaptured historical notes', async ({ page }) => {
  await setup(page)
  let dialog = await choose(page, 'Notes', frozen)
  await expect(dialog.getByText('Plans keep the work visible.')).toBeVisible()
  await expect(dialog).not.toContainText('Hidden benefit')
  await expect(dialog).not.toContainText('PHAROS-52')
  await dialog.getByRole('button', { name: /^Done/ }).click()
  await setup(page, { missingNotes: true })
  dialog = await choose(page, 'Notes', frozen)
  await expect(dialog).toContainText('Notes are unavailable.')
  await expect(dialog).toContainText('Membership was never captured.')
  await expect(dialog).not.toContainText('Draft')
})
test('product publication authority is explicit; another project needs a person’s attestation', async ({ page }) => {
  await setup(page, { agent: true, product: false })
  let dialog = await open(page, cut)
  await expect(dialog.getByRole('button', { name: 'Publish', exact: true })).toHaveAttribute('aria-disabled', 'true')
  await dialog.getByRole('button', { name: /^Done/ }).click()
  const world = await setup(page, { agent: true, product: true })
  dialog = await choose(page, 'Publish', cut)
  await expect(dialog.getByLabel('Reservation reference', { exact: true })).toHaveCount(0)
  await dialog.getByRole('button', { name: /^Publish / }).click()
  await expect(dialog).not.toBeVisible()
  expect(world.writes).toHaveLength(1)
  expect(world.writes[0]?.body.reservation_ref).toBeUndefined()
})

test('settings save preserves an unchanged precise deadline and sends explicit overrides and resets', async ({ page }) => {
  const world = await setup(page)
  world.releases[0]!.entry_closes_at = '2026-10-05T18:00:37Z'
  const dialog = await choose(page, 'Settings…')
  await expect(dialog.getByLabel('Entry closes', { exact: true })).toHaveValue('2026-10-05T20:00')
  await dialog.getByLabel('Override Budget', { exact: true }).check()
  await dialog.getByLabel('Budget', { exact: true }).fill('5')
  await dialog.getByLabel('Override Most agents at once', { exact: true }).check()
  await dialog.getByRole('button', { name: 'Reset Most agents at once', exact: true }).click()
  await dialog.getByLabel('Override Build window', { exact: true }).check()
  await dialog.getByLabel('Build from', { exact: true }).fill('21:00')
  await dialog.getByRole('button', { name: /^Save / }).click()
  await expect(dialog).not.toBeVisible()
  expect(world.writes[0]?.body.entry_closes_at).toBeUndefined()
  expect(world.writes[0]?.body.build_settings).toEqual({ budget_agent_hours: 5, window: { timezone: 'Europe/Vienna', slots: [{ days: [1,2,3,4,5], from: '21:00', to: '02:00' }] } })
})
