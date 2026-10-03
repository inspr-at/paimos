// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { expectStableControls } from './helpers/stable'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import type { Recurrence, RecurrenceInput, RecurrenceResult } from '../src/lib/recurrences'

const id = '57000000-0000-4000-8000-000000000001', eventId = '57000000-0000-4000-8000-000000000002'
const dates = ['2026-10-19T07:00:00Z', '2026-10-26T08:00:00Z', '2026-11-02T08:00:00Z', '2026-11-09T08:00:00Z']
const result = (recurrenceId = id): RecurrenceResult => ({ recurrence_id: recurrenceId, occurrence_key: 'manual:previous', number: 1, scheduled_at: dates[0], created_at: dates[0], node_id: 'n-1', source_event_id: null, outcome: 'created', reason: '', key: 'PHAROS-11', title: 'Sweep', state: 'open' })
const definition = (event = false): Recurrence => ({ id: event ? eventId : id, project_id: 'p-pharos', parent_id: 'p-pharos', template: { name: event ? 'Release notes' : 'Weekly tool sweep', title: event ? 'Notes for {{release_name}}' : 'Sweep {{date}} #{{occurrence}}', description: 'Keep the work queue tidy.', acceptance_criteria: ['Record the outcome'], estimate_hours: 0.5, priority: 'medium', tags: [], type: 'ticket' }, trigger: event ? { kind: 'event', event: 'release.published', event_start: 'now', event_timezone: 'Europe/Vienna' } : { kind: 'time', rrule: 'FREQ=WEEKLY;BYDAY=MO', time_of_day: '09:00', timezone: 'Europe/Vienna', start_date: '2026-10-02' }, queue_each: false, overlap_policy: 'skip', catch_up_policy: 'one', paused: false, revision: 1, occurrence_count: 1, next_at: event ? null : dates[0], created_at: dates[0], updated_at: dates[0], last_result: result(event ? eventId : id), open_previous: result(event ? eventId : id) })
async function setup(page: Page, readOnly = false) {
  const errors = watchErrors(page), world = fixtures(), calls: { method: string; path: string; body: Record<string, unknown> }[] = []
  world.nodes[1].fields.recurrence_id = id; world.nodes[1].fields.occurrence_number = 1
  world.nodes[1].recurrence = { id, project_id: 'p-pharos', project_key: 'PRJ-17', number: 1, retired: false, trigger: definition().trigger }
  await mockWork(page, world, { readOnly })
  await page.route('**/api/me/permissions?*', route => {
    const grants = mockEffectivePermissions(readOnly ? 'viewer' : 'member', new URL(route.request().url()).searchParams.get('project_id') || undefined)
    if (!readOnly) grants.workspace.permissions.push('recurrences.manage', 'run.create')
    return route.fulfill({ json: grants })
  })
  const items = [definition(), definition(true)]
  await page.route('**/api/recurrences**', async route => {
    const req = route.request(), url = new URL(req.url()), path = url.pathname, method = req.method(), body = req.postDataJSON() || {}
    calls.push({ method, path, body })
    const rec = items.find(row => path.split('/')[3] === row.id)
    if (path === '/api/recurrences/preview') return route.fulfill({ json: { times: body.trigger.kind === 'event' ? [] : dates, trigger_kind: body.trigger.kind } })
    if (path === '/api/recurrences' && method === 'GET') return route.fulfill({ json: { items: url.searchParams.get('project_id') === 'p-pharos' ? items : [], next_cursor: null } })
    if (path === '/api/recurrences' && method === 'POST') {
      const created = { ...definition(), ...(body as unknown as RecurrenceInput), id: '57000000-0000-4000-8000-000000000003', occurrence_count: 0, last_result: undefined, open_previous: undefined }
      items.push(created); return route.fulfill({ status: 201, json: created })
    }
    if (!rec) return route.fulfill({ status: 404, json: { error: 'recurrence not found' } })
    if (path.endsWith('/preview')) return route.fulfill({ json: { times: rec.trigger.kind === 'event' ? [] : dates, trigger_kind: rec.trigger.kind } })
    if (path.endsWith('/history')) return route.fulfill({ json: { items: [{ id: 3, type: 'recurrence.occurred', at: dates[0], actor_name: 'Markus Barta', before: null, after: result(rec.id), node: { key: 'PHAROS-11', title: 'Sweep', state: 'open' } }], next_cursor: null } })
    if (path.endsWith('/releases')) return route.fulfill({ json: { items: [{ key: 'p-pharos/version:261002081219.0.0', name: 'Sunlit Sonde', version: '261002081219.0.0', published_at: dates[0] }, { key: 'p-pharos/version:260930090000.0.0', name: 'Gentle Grove', version: '260930090000.0.0', published_at: dates[0], receipt: result(rec.id) }], truncated: false } })
    if (path.endsWith('/pause') || path.endsWith('/resume')) { rec.paused = path.endsWith('/pause'); rec.revision++; return route.fulfill({ json: rec }) }
    if (path.endsWith('/run-now')) { rec.occurrence_count++; return route.fulfill({ status: 201, json: { ...result(rec.id), number: rec.occurrence_count, occurrence_key: 'manual:new' } }) }
    if (method === 'PUT') { Object.assign(rec, body, { revision: rec.revision + 1 }); return route.fulfill({ json: rec }) }
    if (method === 'DELETE') { items.splice(items.indexOf(rec), 1); return route.fulfill({ status: 204 }) }
    return route.fulfill({ json: rec })
  })
  return { errors, calls, items }
}
const editor = (page: Page) => page.getByRole('dialog', { name: /Repeat|Edit recurring work/ })
const row = (page: Page, name = 'Weekly tool sweep') => page.getByRole('row', { name, exact: true })
async function settings(page: Page) { await page.goto('/p/PHAROS/settings'); await expect(row(page)).toBeVisible() }
async function newEditor(page: Page) {
  await page.getByRole('button', { name: 'New…', exact: true }).click()
  await editor(page).getByRole('textbox', { name: 'Name', exact: true }).fill('Weekly inspection')
  await editor(page).getByRole('textbox', { name: 'Title', exact: true }).fill('Inspect ')
  await editor(page).getByRole('button', { name: 'Date', exact: true }).click()
  await page.keyboard.insertText('#')
  await editor(page).getByRole('button', { name: 'Number', exact: true }).click()
  await expect(editor(page).locator('[data-variable]')).toHaveCount(2)
  await editor(page).getByRole('textbox', { name: 'Estimate', exact: true }).fill('20 min')
  await editor(page).getByRole('textbox', { name: 'Criteria', exact: true }).fill('- [ ] Record the outcome')
  await expect(editor(page).getByRole('button', { name: /^Create/ })).toBeEnabled()
}
for (const width of [1440, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`controls stay anchored at ${width}px in ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1100 }); await page.emulateMedia({ colorScheme: theme })
    const { errors } = await setup(page); await settings(page)
    await expectStableControls({
      controls: { actions: row(page).locator('.row-actions'), toggle: row(page).locator('.toggle'), run: row(page).locator('.run') },
      interactions: [
        { name: 'Pause', run: async () => { await row(page).getByRole('button', { name: 'Pause', exact: true }).click(); await expect(row(page).getByRole('button', { name: 'Resume', exact: true })).toBeEnabled() } },
        { name: 'Undo pause', run: async () => { await page.getByRole('button', { name: 'Undo', exact: true }).click(); await expect(row(page).getByRole('button', { name: 'Pause', exact: true })).toBeEnabled() } },
      ],
    })
    await newEditor(page)
    if (width === 390) expect((await editor(page).boundingBox())!.width).toBe(390)
    const controls = {
      cancel: editor(page).getByRole('button', { name: /^Cancel/ }), create: editor(page).getByRole('button', { name: /^Create/ }),
      name: editor(page).getByRole('textbox', { name: 'Name', exact: true }), parent: editor(page).getByRole('button', { name: 'Parent', exact: true }),
      trigger: editor(page).getByRole('radiogroup', { name: 'Trigger', exact: true }),
      time: editor(page).getByRole('radio', { name: 'Schedule', exact: true }), event: editor(page).getByRole('radio', { name: 'Event', exact: true }),
      queue: editor(page).locator('.option').nth(0), skip: editor(page).locator('.option').nth(1), preview: editor(page).locator('.preview-head'),
      ...(width === 390 ? { sheet: editor(page) } : {}),
    }
    const scrollAreas = { body: editor(page).locator('.editor-body') }
    await expectStableControls({
      controls: { ...controls, frequency: editor(page).getByRole('radiogroup', { name: 'Repeat', exact: true }), ...Object.fromEntries(['Daily', 'Monthly', 'Weekly'].map(name => [name, editor(page).getByRole('radio', { name, exact: true })])) },
      scrollAreas,
      interactions: ['Daily', 'Monthly', 'Weekly'].map(name => ({ name, run: async () => { await editor(page).getByRole('radio', { name, exact: true }).evaluate(el => (el as HTMLElement).click()); await expect(controls.create).toBeEnabled() } })),
    })
    await expectStableControls({ controls, scrollAreas, interactions: [{ name: 'Event trigger', run: async () => { await controls.event.evaluate(el => (el as HTMLElement).click()); await expect(controls.create).toBeEnabled() } }] })
    await expectStableControls({
      controls: { ...controls, start: editor(page).getByRole('radiogroup', { name: 'Start', exact: true }), ...Object.fromEntries(['Right away', '1 hour later', 'Next morning'].map(name => [name, editor(page).getByRole('radio', { name, exact: true })])) },
      scrollAreas,
      interactions: [
        ...['Next morning', '1 hour later', 'Right away'].map(name => ({ name, run: async () => { await editor(page).getByRole('radio', { name, exact: true }).evaluate(el => (el as HTMLElement).click()); await expect(controls.create).toBeEnabled() } })),
        ...['Put each one into the work queue', 'Skip while the previous one is still open'].map(name => ({ name, run: async () => { await editor(page).getByRole('checkbox', { name: new RegExp(name) }).evaluate(el => (el as HTMLElement).click()); await expect(controls.create).toBeEnabled() } })),
      ],
    })
    const shots = process.env.AEON_RECURRENCE_SHOTS || 'test-results/recurrences'
    mkdirSync(shots, { recursive: true })
    await page.screenshot({ path: join(shots, `editor-${width}-${theme}.png`) })
    await editor(page).getByRole('button', { name: /^Cancel/ }).click(); expect(errors).toEqual([])
  })
}
test('monthly last-day input, token insertion and platform save use the contract', async ({ page }) => {
  const { calls } = await setup(page); await settings(page); await newEditor(page)
  await editor(page).getByRole('radio', { name: 'Monthly', exact: true }).click()
  await editor(page).getByRole('combobox', { name: 'Every', exact: true }).selectOption('3')
  await editor(page).getByRole('combobox', { name: 'Day of the month', exact: true }).selectOption('-1')
  await editor(page).getByRole('combobox', { name: 'Time zone', exact: true }).selectOption('Europe/Vienna')
  await editor(page).getByRole('checkbox', { name: /Put each one into the work queue/ }).check()
  await expect(editor(page).getByRole('button', { name: /^Create/ })).toBeEnabled()
  await editor(page).getByRole('textbox', { name: 'Name', exact: true }).press('ControlOrMeta+Enter')
  await expect(editor(page)).toHaveCount(0)
  const saved = calls.find(call => call.method === 'POST' && call.path === '/api/recurrences')!.body as unknown as RecurrenceInput
  expect(saved.trigger).toMatchObject({ rrule: 'FREQ=MONTHLY;INTERVAL=3;BYMONTHDAY=-1', timezone: 'Europe/Vienna' })
  expect(saved.template).toMatchObject({ title: 'Inspect {{date}} #{{occurrence}} ', estimate_hours: 1 / 3, acceptance_criteria: ['Record the outcome'] }); expect(saved.queue_each).toBe(true)
})
test('Run now confirms the open ticket, uses the codename picker and sends a bound release', async ({ page }) => {
  const { calls } = await setup(page); await settings(page)
  await row(page).getByRole('button', { name: 'Run now', exact: true }).click()
  const run = page.getByRole('dialog', { name: 'Run recurring work now' })
  await expect(run).toContainText('is still open. Create another anyway?')
  await run.getByRole('button', { name: /^Create #2/ }).click(); await expect(run).toHaveCount(0)
  expect(calls.find(call => call.path.endsWith(`${id}/run-now`))?.body).toMatchObject({ expected_revision: 1, force_overlap: true })
  await row(page, 'Release notes').getByRole('button', { name: 'Run now', exact: true }).click()
  await expect(run.getByRole('button', { name: /^Create #2/ })).toBeDisabled()
  await expect(run.getByRole('radio', { name: /Gentle Grove/ })).toBeDisabled()
  const release = run.getByRole('radio', { name: /Sunlit Sonde/ })
  await expect(release).toHaveAttribute('title', '261002081219.0.0')
  await expect(release).not.toContainText('261002081219.0.0'); await release.click()
  await run.getByRole('button', { name: /^Create #2/ }).click(); await expect(run).toHaveCount(0)
  expect(calls.find(call => call.path.endsWith(`${eventId}/run-now`))?.body).toMatchObject({ expected_revision: 1, release_key: 'p-pharos/version:261002081219.0.0', force_overlap: true })
})
test('read-only people can see history and provenance, but cannot repeat or mutate', async ({ page }) => {
  const { calls } = await setup(page, true); await settings(page)
  await expect(page.getByRole('button', { name: 'New…', exact: true })).toBeDisabled()
  await expect(row(page).getByRole('button', { name: 'Pause', exact: true })).toBeDisabled()
  await expect(page.getByRole('region', { name: 'Recurrence history' })).toContainText('PHAROS-11')
  await row(page).getByRole('button', { name: /^More/ }).click()
  await expect(page.getByRole('menuitem', { name: 'Edit…', exact: true })).toBeDisabled()
  await expect(page.getByRole('menuitem', { name: 'Delete…', exact: true })).toBeDisabled()
  await page.keyboard.press('Escape'); await page.goto('/p/PHAROS/PHAROS-11')
  await expect(page.locator('.recurrence-provenance')).toContainText('Weekly tool sweep')
  await page.getByRole('button', { name: 'More actions' }).click()
  await expect(page.getByRole('menuitem', { name: /Repeat/ })).toBeDisabled()
  await expect(page.getByRole('menuitem', { name: 'Edit Weekly tool sweep…', exact: true })).toBeDisabled()
  expect(calls.filter(call => call.method !== 'GET')).toEqual([])
})
test('created tickets edit their recurrence, and a stale Undo does not resume a newer revision', async ({ page }) => {
  const { calls } = await setup(page); await settings(page)
  await row(page).getByRole('button', { name: 'Pause', exact: true }).click()
  await expect(row(page).getByRole('button', { name: 'Resume', exact: true })).toBeEnabled()
  await row(page).getByRole('button', { name: /^More/ }).click()
  await page.getByRole('menuitem', { name: 'Edit…', exact: true }).click()
  await editor(page).getByRole('textbox', { name: 'Name', exact: true }).fill('Updated inspection')
  await expect(editor(page).getByRole('button', { name: /^Save/ })).toBeEnabled()
  await editor(page).getByRole('button', { name: /^Save/ }).click(); await expect(editor(page)).toHaveCount(0)
  await page.getByRole('button', { name: 'Undo', exact: true }).click()
  expect(calls.filter(call => call.path.endsWith('/resume'))).toHaveLength(0)
  await page.goto('/p/PHAROS/PHAROS-11')
  await expect(page.locator('.recurrence-provenance')).toContainText('Updated inspection')
  await page.getByRole('complementary', { name: 'Ticket details' }).getByRole('button', { name: 'More actions' }).click()
  await page.getByRole('menuitem', { name: 'Edit Updated inspection…', exact: true }).click()
  await expect(editor(page)).toHaveAttribute('aria-labelledby', /heading/)
  await expect(editor(page).getByRole('textbox', { name: 'Name', exact: true })).toHaveValue('Updated inspection')
  await expect(editor(page).getByRole('button', { name: 'Parent', exact: true })).toBeDisabled()
})
test('a delayed Run now read is discarded when the project changes', async ({ page }) => {
  const { calls } = await setup(page); await settings(page)
  let entered!: () => void, release!: () => void
  const arrived = new Promise<void>(resolve => { entered = resolve }), allowed = new Promise<void>(resolve => { release = resolve })
  await page.route(`**/api/recurrences/${id}`, async route => { entered(); await allowed; await route.fulfill({ json: definition() }).catch(() => {}) })
  await row(page).getByRole('button', { name: 'Run now', exact: true }).click(); await arrived
  await page.getByRole('navigation', { name: 'Places' }).getByRole('link', { name: 'Projects', exact: true }).click()
  await page.locator('a[href="/p/AEON"]').first().click(); await page.getByRole('tab', { name: 'Settings', exact: true }).click()
  release()
  await expect(page.getByText('No recurring work yet. Use New…, or Repeat… on a ticket.', { exact: true })).toBeVisible()
  await expect(page.getByRole('dialog', { name: 'Run recurring work now' })).toHaveCount(0)
  expect(calls.filter(call => call.path.endsWith('/run-now'))).toHaveLength(0)
})
test('renaming legacy monthly and event definitions leaves their trigger unchanged', async ({ page }) => {
  const { items, calls } = await setup(page)
  items[0].trigger.rrule = 'FREQ=MONTHLY;BYMONTHDAY=1'
  items[1].trigger = { kind: 'event', event: 'release.published' }
  const triggers = items.map(item => ({ ...item.trigger }))
  await settings(page)
  for (const [index, name] of ['Weekly tool sweep', 'Release notes'].entries()) {
    await row(page, name).getByRole('button', { name: /^More/ }).click()
    await page.getByRole('menuitem', { name: 'Edit…', exact: true }).click()
    await editor(page).getByRole('textbox', { name: 'Name', exact: true }).fill(`${name} renamed`)
    await expect(editor(page).getByRole('button', { name: /^Save/ })).toBeEnabled()
    await editor(page).getByRole('button', { name: /^Save/ }).click(); await expect(editor(page)).toHaveCount(0)
    expect(calls.filter(call => call.method === 'PUT').at(-1)?.body.trigger).toEqual(triggers[index])
  }
})
test('Repeat copies the epic parent, Esc leaves a field first, and browser modifiers stay free', async ({ page }) => {
  await setup(page); await page.goto('/p/PHAROS/PHAROS-10')
  const panel = page.getByRole('complementary', { name: 'Ticket details' })
  await expect(panel).toBeVisible(); await panel.getByRole('button', { name: 'More actions' }).focus()
  await page.keyboard.press('Shift+R'); await expect(editor(page)).toBeVisible()
  await expect(editor(page).getByRole('button', { name: 'Parent', exact: true })).toContainText('PHAROS-10')
  await editor(page).getByRole('textbox', { name: 'Name', exact: true }).focus()
  await page.keyboard.press('Escape'); await expect(editor(page)).toBeVisible()
  await expect(editor(page).locator('.editor-body')).toBeFocused()
  const prevented = await editor(page).evaluate(el => { const event = new KeyboardEvent('keydown', { key: 'f', metaKey: true, ctrlKey: true, bubbles: true, cancelable: true }); el.dispatchEvent(event); return event.defaultPrevented })
  expect(prevented).toBe(false); await page.keyboard.press('Escape'); await expect(editor(page)).toHaveCount(0)
})
test('an Undo toast cannot mutate a different project and a failed pause stays honest', async ({ page }) => {
  const { calls } = await setup(page); await settings(page)
  await row(page).getByRole('button', { name: 'Pause', exact: true }).click()
  await expect(row(page).getByRole('button', { name: 'Resume', exact: true })).toBeEnabled()
  await page.getByRole('link', { name: 'Projects', exact: true }).first().click()
  await page.locator('a[href="/p/AEON"]').first().click(); await page.getByRole('tab', { name: 'Settings', exact: true }).click()
  await page.getByRole('button', { name: 'Undo', exact: true }).click()
  expect(calls.filter(call => call.path.endsWith('/resume'))).toHaveLength(0)
  await settings(page)
  await page.route(`**/api/recurrences/${id}/resume`, route => route.fulfill({ status: 409, json: { message: 'recurrence revision changed', code: 'conflict' } }))
  await row(page).getByRole('button', { name: 'Resume', exact: true }).click()
  await expect(page.getByText('recurrence revision changed', { exact: true })).toBeVisible()
  await expect(row(page).getByRole('button', { name: 'Resume', exact: true })).toBeEnabled()
})
