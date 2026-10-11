// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page, type TestInfo } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { expectStableControls } from './helpers/stable'
import type { Recurrence, RecurrenceInput } from '../src/lib/recurrences'

// Risk (AEON-1182): creating an agent routine starts work without consent, consents to a
// revision that is no longer shown, lets someone other than the owner decide, keeps a
// stale or revoked draft, says "kept paused" for a resumed routine, checks personal
// routines against the wrong scope, or moves controls while options change.
const OTHER = '22222222-2222-4222-8222-222222222222', AGENT = '58000000-0000-4000-8000-000000000001', CREATED = '58000000-0000-4000-8000-000000000002'
const dates = ['2026-10-12T06:00:00Z', '2026-10-19T06:00:00Z', '2026-10-26T07:00:00Z', '2026-11-02T07:00:00Z']
const waiting = { eligible: false, consent_required: true, wait_reason: 'execution_consent_required', project_revision: 2, consent_revision: 0 }
const assignment = { goal: 'Update npm and Go dependencies; open one pull request.', role: 'build', work_kind_id: 'k-maint', sources: [], allowed_actions: ['work.create', 'pr.open'], runtime_requirements: { needs_native_host: true, needs_browser: false, runtime_class: 'native_coding' }, budget: { mode: 'tokens', token_ceiling: 400_000 } }
const owned = (owner: string): Recurrence => ({ id: AGENT, project_id: 'p-pharos', parent_id: 'p-pharos', template: { name: 'Weekly dependency update', title: 'Update dependencies {{date}}', description: assignment.goal, acceptance_criteria: ['All tests pass'], estimate_hours: 1, priority: 'medium', tags: [], type: 'work' }, trigger: { kind: 'time', rrule: 'FREQ=WEEKLY;BYDAY=MO', time_of_day: '08:00', timezone: 'Europe/Vienna' }, queue_each: true, overlap_policy: 'skip', catch_up_policy: 'one', paused: true, revision: 3, occurrence_count: 0, next_at: dates[0], created_at: dates[0], updated_at: dates[0], definition: { scope: { kind: 'project', project_id: 'p-pharos' }, owner_principal_id: owner, assignment: structuredClone(assignment) as NonNullable<Recurrence['definition']>['assignment'] }, execution_policy: { ...waiting } })
interface Call { method: string; path: string; query: URLSearchParams; body: Record<string, unknown> }

// manageIn grants Manage recurring work in that one project only, never in the workspace.
async function setup(page: Page, role: 'admin' | 'member', options: { items?: Recurrence[]; staleOnce?: boolean; manageIn?: string; consentFails?: string } = {}) {
  const errors = watchErrors(page), calls: Call[] = [], items = options.items ?? []
  const grants = { manage: true }
  await mockWork(page, fixtures(), { admin: role === 'admin' })
  await page.route('**/api/me/permissions**', route => {
    const projectId = new URL(route.request().url()).searchParams.get('project_id') || undefined
    const effective = mockEffectivePermissions(role, projectId)
    if (grants.manage && !options.manageIn) (effective.workspace.permissions as string[]).push('recurrences.manage', 'run.create')
    else if (grants.manage && effective.project && projectId === options.manageIn) effective.project = { ...effective.project, permissions: [...effective.project.permissions, 'recurrences.manage', 'run.create'] }
    return route.fulfill({ json: effective })
  })
  await page.route('**/api/work-kinds**', route => route.fulfill({ json: { items: [{ id: 'k-maint', slug: 'maintenance', label: 'Maintenance', hint: '', position: 1, examples: [], labels: [], ticket_count: 0 }, { id: 'k-docs', slug: 'docs', label: 'Docs', hint: '', position: 2, examples: [], labels: [], ticket_count: 0 }], next_cursor: null } }))
  await page.route('**/api/models/resolve**', route => route.fulfill({ json: { role: new URL(route.request().url()).searchParams.get('role'), profile: { id: '57000000-0000-4000-8000-000000000001', slug: 'opus', version: '1', harness: 'claude', family: 'anthropic', model: 'claude-opus-5-5', effort: 'xhigh', tier: 'frontier', enabled: true, created_at: dates[0], display_name: 'Claude Opus', short_name: 'Opus', model_version: '5.5' }, owner_required: false } }))
  await page.route('**/api/projects/p-pharos/members', route => route.fulfill({ json: [{ principal_id: me.id, name: me.name, avatar_url: null, has_avatar: false, kind: 'person', via: 'workspace', role: { id: 'r', key: 'admin', name: 'Admin' }, workspace_role: null }] }))
  await page.route('**/api/projects/p-pharos/routine-execution-settings', route => route.fulfill({ json: { revision: 2, automatic_launch_enabled: true, consent_enabled: true, qualification_id: '59000000-0000-4000-8000-000000000001', wait_reason: '' } }))
  let staleOnce = !!options.staleOnce
  await page.route('**/api/recurrences**', async route => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname, method = request.method(), body = (request.postDataJSON() || {}) as Record<string, unknown>
    calls.push({ method, path, query: url.searchParams, body })
    const record = items.find(row => path.split('/')[3] === row.id)
    if (path === '/api/recurrences/preview') {
      const draft = body as unknown as RecurrenceInput, a = draft.definition?.assignment
      const wait = !a ? '' : a.runtime_requirements.needs_browser ? 'capability_unqualified' : ['money', 'both'].includes(a.budget?.mode ?? '') ? 'budget_mode_unqualified' : 'execution_consent_required'
      return route.fulfill({ json: { times: dates, trigger_kind: 'time', ...(a ? { execution_policy: { ...waiting, wait_reason: wait } } : {}) } })
    }
    if (path === '/api/recurrences' && method === 'GET') {
      const project = url.searchParams.get('project_id'), scope = url.searchParams.get('scope') ?? 'project'
      return route.fulfill({ json: { items: items.filter(row => (!project || row.project_id === project) && (row.definition?.scope.kind ?? 'project') === scope), next_cursor: null } })
    }
    if (path === '/api/recurrences' && method === 'POST') {
      const created = { ...owned(me.id), ...(body as unknown as RecurrenceInput), id: CREATED, revision: 1, paused: true, execution_policy: { ...waiting } } as Recurrence
      items.push(created); return route.fulfill({ status: 201, json: created })
    }
    if (!record) return route.fulfill({ status: 404, json: { error: 'recurrence not found' } })
    if (path.endsWith('/execution-principals')) {
      // The first page is empty but has a cursor: the picker must keep paging.
      return route.fulfill({ json: url.searchParams.get('after') === 'c1' ? { items: [{ id: 'a-1', kind: 'agent', label: 'PHAROS routines' }, { id: 'a-2', kind: 'agent', label: 'PHAROS docs helper' }], next_cursor: null } : { items: [], next_cursor: 'c1' } })
    }
    if (path.endsWith('/guardrails')) return route.fulfill({ json: { hard_version: 'h1', preset_version: '2026.10', digest: 'd', sources: [], rules: [{ rule: { id: 'hard.harmful_intent_v1', checkpoint: 'both', method: 'template', template: 'harmful_intent_v1', result: 'block' }, level: 'hard', scope: { kind: 'installation', id: 'paimos' }, version: 'h1' }, { rule: { id: 'recommended.sensitive', checkpoint: 'both', method: 'words', values: ['password', 'credential', 'secret', 'passwort'], result: 'needs_person' }, level: 'recommended', scope: { kind: 'installation', id: 'paimos' }, version: '2026.10' }] } })
    if (path.endsWith('/history')) return route.fulfill({ json: { items: [{ id: 9, type: 'recurrence.updated', at: dates[0], actor_name: 'Markus Barta', before: null, after: null, node: null }], next_cursor: null } })
    if (path.endsWith('/runs')) return route.fulfill({ json: { items: [], next_cursor: null } })
    if (path.endsWith('/preview')) return route.fulfill({ json: { times: dates, trigger_kind: 'time' } })
    if (path.endsWith('/resume') || path.endsWith('/pause')) { Object.assign(record, { paused: path.endsWith('/pause'), revision: record.revision + 1 }); return route.fulfill({ json: record }) }
    if (path.endsWith('/execution-consent') && options.consentFails) return route.fulfill({ status: 422, json: { error: options.consentFails } })
    if (path.endsWith('/execution-consent')) { Object.assign(record, { revision: record.revision + 1, execution_policy: { ...waiting, eligible: true, wait_reason: '', consent_revision: record.revision + 1 } }); return route.fulfill({ json: record }) }
    if (method === 'PUT') {
      if (staleOnce) { staleOnce = false; Object.assign(record, { revision: record.revision + 1, trigger: { ...record.trigger, time_of_day: '09:00' } }); return route.fulfill({ status: 409, json: { error: 'recurrence revision changed' } }) }
      Object.assign(record, body, { revision: record.revision + 1 }); return route.fulfill({ json: record })
    }
    return route.fulfill({ json: record })
  })
  return { errors, calls, items, revoke: () => { grants.manage = false } }
}
const editor = (page: Page) => page.getByRole('dialog', { name: /Repeat|New routine|Edit routine|Let agents run it/ })
const radio = (page: Page, name: string) => editor(page).getByRole('radio', { name, exact: true })
async function shot(page: Page, info: TestInfo, name: string) { await page.screenshot({ path: info.outputPath(`${name}.png`) }) }
// The consent step's fold row draws its chevron and edit icon, never an empty box.
async function expectFoldIcons(page: Page) {
  const icons = editor(page).getByRole('button', { name: /^The routine itself/ }).locator('svg')
  await expect(icons).toHaveCount(2)
  for (const icon of await icons.all()) await expect(icon).toBeVisible()
}
async function draftAgentRoutine(page: Page) {
  await page.goto('/p/PHAROS/settings')
  await page.getByRole('button', { name: 'New routine…', exact: true }).click()
  await expect(editor(page)).toBeVisible()
  await editor(page).getByRole('textbox', { name: 'Name', exact: true }).fill('Weekly dependency update')
  await editor(page).getByRole('textbox', { name: 'Title', exact: true }).fill('Update dependencies')
  await editor(page).getByRole('textbox', { name: 'Estimate', exact: true }).fill('1 h')
  await editor(page).getByRole('textbox', { name: 'Criteria', exact: true }).fill('- [ ] All tests pass')
  await expect(editor(page).getByRole('button', { name: /^Create/ })).toBeEnabled()
}

test('an agent routine is created paused, then let run as the picked agent, with every control anchored', async ({ page }, info) => {
  await page.setViewportSize({ width: 1440, height: 1100 })
  const { errors, calls } = await setup(page, 'admin')
  await draftAgentRoutine(page)
  // Ticket-only stays today's editor: the Who row and the sentence, no agent rows.
  await expect(editor(page).getByRole('combobox', { name: 'Kind of work' })).toHaveCount(0)
  await expect(editor(page).locator('.preview-head .eyebrow')).toHaveText('Next')
  const create = editor(page).getByRole('button', { name: /^Create/ }), cancel = editor(page).getByRole('button', { name: /^Cancel/ })
  const body = { body: editor(page).locator('.editor-body') }
  await expectStableControls({
    controls: { ticket: radio(page, 'Just create the ticket'), agent: radio(page, 'An agent works on it'), name: editor(page).getByRole('textbox', { name: 'Name', exact: true }), sentence: editor(page).locator('.ed-sentence'), create, cancel },
    scrollAreas: body,
    interactions: [{ name: 'Who: an agent works on it', run: async () => { await radio(page, 'An agent works on it').click(); await expect(editor(page).getByRole('combobox', { name: 'Kind of work' })).toHaveValue('k-maint') } }],
  })
  await editor(page).getByRole('textbox', { name: 'Description', exact: true }).fill('Update npm and Go dependencies; open one pull request.')
  await expect(editor(page).locator('.preview-head .eyebrow')).toHaveText('Before it runs')
  await expectStableControls({
    controls: { scout: radio(page, 'Look around'), build: radio(page, 'Build'), hard: radio(page, 'Hard build'), kind: editor(page).getByRole('combobox', { name: 'Kind of work' }), rules: editor(page).getByRole('button', { name: /^What it may do/ }), sentence: editor(page).locator('.ed-sentence'), create },
    scrollAreas: body,
    interactions: ['Hard build', 'Look around', 'Build'].map(name => ({ name: `Effort: ${name}`, run: async () => { await radio(page, name).click(); await expect(editor(page).locator('.route')).toContainText('Today Opus 5.5 via Claude Code') } })),
  })
  await expectStableControls({
    controls: { off: radio(page, 'No limit'), money: radio(page, 'Money'), both: radio(page, 'Both'), where: editor(page).getByRole('button', { name: /^Where it runs/ }), create },
    scrollAreas: body,
    interactions: [
      { name: 'Budget: money (not qualified)', run: async () => { await radio(page, 'Money').click(); await expect(editor(page).getByRole('note').filter({ hasText: 'budget_mode_unqualified' })).toBeVisible() } },
      { name: 'Budget: both', run: async () => { await radio(page, 'Both').click(); await expect(editor(page).getByText('whichever comes first')).toBeVisible() } },
      { name: 'Budget: tokens', run: async () => { await radio(page, 'Tokens').click(); await expect(editor(page).locator('.room')).toBeVisible() } },
    ],
  })
  const knowledge = editor(page).getByRole('switch', { name: /Write knowledge entries/ }), browser = editor(page).getByRole('switch', { name: /Needs a browser/ })
  await expectStableControls({
    controls: { knowledge, pr: editor(page).getByRole('switch', { name: /Open a pull request/ }), browser, where: editor(page).getByRole('button', { name: /^Where it runs/ }), create },
    scrollAreas: body,
    interactions: [
      { name: 'Action: knowledge on', run: async () => { await knowledge.click(); await expect(knowledge).toBeChecked() } },
      { name: 'Needs a browser (not qualified)', run: async () => { await browser.click(); await expect(editor(page).getByText('capability_unqualified').first()).toBeVisible() } },
      { name: 'Needs a browser off', run: async () => { await browser.click(); await expect(editor(page).locator('.ok-line')).toBeVisible() } },
    ],
  })
  // Route line and WHO say the registry name and harness label, never a model or profile id.
  await expect(editor(page).locator('.pv-grid')).toContainText('Opus 5.5 via Claude Code builds it')
  for (const part of ['.route', '.pv-grid']) for (const raw of ['claude-opus-5-5', '57000000-']) await expect(editor(page).locator(part)).not.toContainText(raw)
  // Settings paths (Models, Capacity, Computers) draw an SVG chevron, never ›, and each reads on one line.
  await expect(editor(page).locator('.route a')).toHaveAccessibleName('Settings, Models')
  expect(await editor(page).locator('.settings-place').evaluateAll(places => places.map(place => [place.querySelector('.sr-only')?.textContent, place.querySelector('.drawn')?.getClientRects().length]))).toEqual([['Settings, Models', 1], ['Settings, Capacity', 1], ['Settings, Computers', 1], ['Settings, Computers', 1]])
  await shot(page, info, 'editor-agent-1440-light')
  await create.click()
  // Saving starts nothing: paused, unconsented, and the same sheet asks.
  await expect(editor(page)).toHaveAccessibleName('Let agents run it')
  const saved = calls.find(call => call.method === 'POST' && call.path === '/api/recurrences')!.body as unknown as RecurrenceInput
  expect(saved.queue_each).toBe(true)
  expect(saved.definition).toEqual({ scope: { kind: 'project', project_id: 'p-pharos' }, owner_principal_id: me.id, assignment: { goal: 'Update npm and Go dependencies; open one pull request.', sources: [], role: 'build', work_kind_id: 'k-maint', allowed_actions: ['work.create', 'work.update', 'knowledge.write'], runtime_requirements: { needs_native_host: true, needs_browser: false, runtime_class: 'native_coding' }, budget: { mode: 'tokens', token_ceiling: 400_000 } } })
  expect(calls.some(call => /\/(resume|execution-consent)$/.test(call.path))).toBe(false)
  const consent = editor(page).locator('#consent'), letRun = editor(page).getByRole('button', { name: /^Let agents run it/ })
  await expect(consent).toContainText('Saved as revision 1')
  await expect(consent).toContainText('Agents may run routines in PHAROS: on.')
  // Two eligible agents, found past an empty first page: nothing is preselected.
  const runsAs = editor(page).getByRole('button', { name: /^Runs as:/ })
  await expect(runsAs).toContainText('Pick an agent')
  expect(calls.filter(call => call.path.endsWith('/execution-principals')).map(call => call.query.get('after'))).toEqual([null, 'c1'])
  await expect(letRun).toBeDisabled()
  await runsAs.click()
  await expect(page.getByRole('listbox', { name: 'Runs as' }).getByRole('option')).toHaveCount(2)
  // Escape closes the popover first, then (next press) the sheet.
  await page.keyboard.press('Escape')
  await expect(page.getByRole('listbox', { name: 'Runs as' })).toHaveCount(0)
  await expect(editor(page)).toBeVisible()
  await runsAs.click()
  await page.getByRole('option', { name: /PHAROS docs helper/ }).click()
  await expect(runsAs).toContainText('PHAROS docs helper')
  await expectStableControls({
    controls: { form: editor(page).getByRole('button', { name: /^The routine itself/ }), consent, letRun },
    scrollAreas: body,
    interactions: [{ name: 'The routine itself opens', run: async () => { await editor(page).getByRole('button', { name: /^The routine itself/ }).click(); await expect(editor(page).getByRole('textbox', { name: 'Name', exact: true })).toBeVisible() } }],
  })
  // The fold row draws its icons; the owner's hint names the shortcut.
  await expectFoldIcons(page)
  await expect(editor(page).locator('.editor-hint')).toContainText('let agents run it · esc leaves a field, then closes')
  await shot(page, info, 'consent-1440-light')
  await letRun.click()
  await expect(editor(page)).toHaveCount(0)
  await expect(page.getByText(/^Agents may run “Weekly dependency update”/)).toBeVisible()
  // Docs order: resume the shown revision, then consent to the revision it returned.
  const writes = calls.filter(call => /\/(resume|execution-consent)$/.test(call.path))
  expect(writes.map(call => [call.path.split('/').at(-1), call.body])).toEqual([['resume', { expected_revision: 1 }], ['execution-consent', { expected_revision: 2, execute_consent: true, execution_principal_id: 'a-2' }]])
  expect(errors).toEqual([])
})

test('the phone sheet scrolls under a pinned footer, and switching who moves nothing', async ({ page }, info) => {
  await page.setViewportSize({ width: 390, height: 860 }); await page.emulateMedia({ colorScheme: 'dark' })
  const { errors } = await setup(page, 'admin')
  await draftAgentRoutine(page)
  const sheet = editor(page), foot = sheet.locator('.editor-foot'), create = foot.getByRole('button', { name: /^Create/ })
  expect((await sheet.boundingBox())!.width).toBe(390)
  await radio(page, 'An agent works on it').scrollIntoViewIfNeeded()
  await expectStableControls({
    controls: { sheet, create, cancel: sheet.getByRole('button', { name: /^Cancel/ }), ticket: radio(page, 'Just create the ticket'), agent: radio(page, 'An agent works on it') },
    scrollAreas: { body: sheet.locator('.editor-body') },
    interactions: [{ name: 'Who: an agent works on it', run: async () => { await radio(page, 'An agent works on it').click(); await expect(sheet.getByRole('combobox', { name: 'Kind of work' })).toBeVisible() } }],
  })
  const before = await sheet.evaluate(el => ({ head: el.querySelector('.editor-head')!.getBoundingClientRect().top, foot: el.querySelector('.editor-foot')!.getBoundingClientRect().bottom, scrolls: el.querySelector('.editor-body')!.scrollHeight > el.querySelector('.editor-body')!.clientHeight + 100 }))
  expect(before.scrolls).toBe(true)
  expect(Math.abs(before.foot - 860)).toBeLessThanOrEqual(1)
  await sheet.locator('.editor-body').evaluate(el => { el.scrollTop = 900 })
  await expect.poll(() => sheet.locator('.editor-body').evaluate(el => el.scrollTop)).toBeGreaterThan(0)
  const after = await sheet.evaluate(el => ({ head: el.querySelector('.editor-head')!.getBoundingClientRect().top, foot: el.querySelector('.editor-foot')!.getBoundingClientRect().bottom }))
  expect(after).toEqual({ head: before.head, foot: before.foot })
  await expect(create).toBeVisible()
  await shot(page, info, 'editor-agent-390-dark')
  expect(errors).toEqual([])
})

test('someone else’s routine says who decides, a stale save keeps unsaved text, and revocation closes the sheet', async ({ page }, info) => {
  await page.setViewportSize({ width: 1440, height: 1100 })
  const { errors, calls, revoke } = await setup(page, 'member', { items: [owned(OTHER)], staleOnce: true })
  // The Desk link-out opens the editor at the consent step; only the owner decides.
  await page.goto(`/p/PHAROS/settings?recurrence=${AGENT}&consent=1`)
  await expect(editor(page)).toHaveAccessibleName('Let agents run it')
  await expect(editor(page).locator('#consent')).toContainText('A PHAROS admin decides.')
  await expect(editor(page).getByRole('button', { name: /^Let agents run it/ })).toHaveCount(0)
  // No primary, so no ⌘↵ hint: only Esc.
  await expect(editor(page).locator('.editor-hint')).toHaveText('esc leaves a field, then closes')
  await expect(editor(page).locator('.editor-hint kbd')).toHaveCount(1)
  await shot(page, info, 'consent-member-1440-light')
  await editor(page).getByRole('button', { name: /^Close/ }).click()
  await expect(editor(page)).toHaveCount(0)
  const row = page.getByRole('row', { name: 'Weekly dependency update', exact: true })
  await expect(row).toContainText('Waiting for a person')
  await row.getByRole('button', { name: /^More/ }).click()
  await page.getByRole('menuitem', { name: 'Edit…', exact: true }).click()
  await expect(editor(page)).toHaveAccessibleName('Edit routine')
  await expect(editor(page).getByText('A PHAROS admin', { exact: true })).toBeVisible()
  const description = editor(page).getByRole('textbox', { name: 'Description', exact: true })
  await description.fill('Only npm this time; open one pull request.')
  await editor(page).getByRole('button', { name: /^Save/ }).click()
  // 409: the record as it is now; the changed time takes the server value with a tint, the typed text stays.
  await expect(editor(page).getByRole('status').filter({ hasText: 'Changed elsewhere; shown as it is now.' })).toContainText('Markus Barta changed it')
  await expect(description).toHaveValue('Only npm this time; open one pull request.')
  await expect(editor(page).getByLabel('At', { exact: true })).toHaveValue('09:00')
  await expect(editor(page).getByLabel('At', { exact: true })).toHaveClass(/changed/)
  await expect(editor(page).getByRole('button', { name: /^Save/ })).toBeEnabled()
  expect(calls.filter(call => call.method === 'PUT').map(call => call.body.expected_revision)).toEqual([3])
  await shot(page, info, 'stale-1440-light')
  // Losing the right to manage closes the sheet; the box says nothing was saved.
  revoke()
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect(editor(page)).toHaveCount(0)
  await expect(page.getByRole('status').filter({ hasText: 'The routine editor closed:' })).toContainText('Nothing was saved')
  expect(calls.filter(call => call.method === 'PUT')).toHaveLength(1)
  expect(errors).toEqual([])
})

test('closing after a failed consent says the routine is not paused, never “Kept paused”', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1100 })
  const { errors, calls } = await setup(page, 'admin', { items: [owned(me.id)], consentFails: 'execution_identity_unavailable' })
  await page.goto(`/p/PHAROS/settings?recurrence=${AGENT}&consent=1`)
  await expect(editor(page)).toHaveAccessibleName('Let agents run it')
  await editor(page).getByRole('button', { name: /^Runs as:/ }).click()
  await page.getByRole('option', { name: /PHAROS routines/ }).click()
  await editor(page).getByRole('button', { name: /^Let agents run it/ }).click()
  // The resume landed, then the consent failed: said in words, with Pause again.
  await expect(editor(page).getByRole('alert').filter({ hasText: 'Agents may not run it:' })).toContainText('It is resumed')
  expect(calls.filter(call => /\/(resume|execution-consent)$/.test(call.path)).map(call => call.path.split('/').at(-1))).toEqual(['resume', 'execution-consent'])
  await editor(page).getByRole('button', { name: /^Close/ }).click()
  await expect(editor(page)).toHaveCount(0)
  await expect(page.getByText(/^Not paused\. Weekly dependency update creates its tickets when due; they wait until you let agents run it\.$/)).toBeVisible()
  await expect(page.getByText(/Kept paused/)).toHaveCount(0)
  // The row agrees: not paused, so it offers Pause.
  await expect(page.getByRole('row', { name: 'Weekly dependency update', exact: true }).getByRole('button', { name: 'Pause', exact: true })).toBeEnabled()
  expect(errors).toEqual([])
})

test('your routines need the right only in their own project, and losing it there closes the sheet', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1100 })
  await mockSettings(page, settingsData())
  const mine = owned(me.id)
  mine.definition!.scope = { kind: 'personal' }
  const { errors, calls, revoke } = await setup(page, 'member', { items: [mine], manageIn: 'p-pharos' })
  await page.goto('/settings/personal')
  // No workspace grant: each row is checked against its own output project, as the server does.
  const row = page.getByRole('row', { name: 'Weekly dependency update', exact: true })
  await expect(row.getByRole('button', { name: 'Resume', exact: true })).toBeEnabled()
  await expect(row.getByRole('button', { name: 'Run now', exact: true })).toBeEnabled()
  await page.getByRole('button', { name: 'New routine…', exact: true }).click()
  await expect(editor(page)).toHaveAccessibleName('New routine')
  const project = editor(page).getByRole('combobox', { name: 'Project', exact: true })
  // Picking a project without the right only says so; the sheet stays open.
  await project.selectOption('p-aeon')
  await expect(editor(page).getByText(/^You can’t add routines to /)).toBeVisible()
  await project.selectOption('p-pharos')
  await expect(editor(page).getByText(/^You can’t add routines to /)).toHaveCount(0)
  await editor(page).getByRole('textbox', { name: 'Name', exact: true }).fill('My weekly sweep')
  // Losing it in the chosen project closes the sheet and discards the draft.
  revoke()
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect(editor(page)).toHaveCount(0)
  await expect(page.getByRole('status').filter({ hasText: 'The routine editor closed:' })).toContainText('your rights in its project changed. Nothing was saved; your draft was discarded.')
  await expect(row.getByRole('button', { name: 'Resume', exact: true })).toBeDisabled()
  expect(calls.filter(call => call.method !== 'GET' && call.path !== '/api/recurrences/preview')).toEqual([])
  expect(errors).toEqual([])
})

for (const [width, theme] of [[1440, 'dark'], [390, 'light']] as const) {
  test(`the Routines box and editor render in ${theme} at ${width}px`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: width > 600 ? 1000 : 860 }); await page.emulateMedia({ colorScheme: theme })
    const { errors } = await setup(page, 'admin', { items: [owned(me.id)] })
    await page.goto('/p/PHAROS/settings')
    await expect(page.getByRole('row', { name: 'Weekly dependency update', exact: true })).toContainText('Waiting for a person')
    await expect(page.getByText('Agents may run routines in PHAROS')).toBeVisible()
    // An agent routine's history is its logbook (AEON-1192), named a routine like the box.
    await expect(page.locator('[data-logbook] .lb-head .eyebrow')).toHaveText('Routine · PHAROS')
    await shot(page, info, `box-${width}-${theme}`)
    await draftAgentRoutine(page)
    await radio(page, 'An agent works on it').click()
    await expect(editor(page).getByRole('combobox', { name: 'Kind of work' })).toHaveValue('k-maint')
    await shot(page, info, `editor-agent-${width}-${theme}`)
    await editor(page).getByRole('textbox', { name: 'Description', exact: true }).fill('Update npm and Go dependencies; open one pull request.')
    await editor(page).getByRole('button', { name: /^Create/ }).click()
    await expect(editor(page)).toHaveAccessibleName('Let agents run it')
    await expectFoldIcons(page)
    await editor(page).getByRole('button', { name: /^The routine itself/ }).scrollIntoViewIfNeeded()
    await shot(page, info, `consent-${width}-${theme}`)
    expect(errors).toEqual([])
  })
}
