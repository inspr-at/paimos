// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page, type TestInfo } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { expectStableControls } from './helpers/stable'
import type { Recurrence } from '../src/lib/recurrences'
import type { RoutineRun, RunDetail, RunUsage } from '../src/lib/routineRuns'

// Risk (AEON-1192): Agents › Routines lists a routine twice, paints a failed run in the
// Done tint or a created ticket as finished, lets a late page for another routine or
// person alter the logbook on screen, keeps the previous person's routines after a
// switch, offers the wrong logbook filters for the kind of routine, or moves its
// controls while filters, rows and runs change.
const MIRA = '22222222-2222-4222-8222-222222222222'
const id = (n: number) => `59200000-0000-4000-8000-0000000000${String(n).padStart(2, '0')}`
const [DEPS, LINKS, NOTES, TRIAGE, CLEANUP, TESTS] = [1, 2, 3, 4, 5, 6].map(id)
const run = (n: number) => `59300000-0000-4000-8000-0000000000${String(n).padStart(2, '0')}`
const next = '2026-10-12T06:00:00Z'
const usage = (extra: Partial<RunUsage> = {}): RunUsage => ({ tokens: { measured: '164000', known: 1, unknown: 0 }, paid_microusd: '0', estimated_api_usd: '2.40', reserved_tokens: '0', reserved_microusd: '0', reserved_recovery_ms: '0', partial: false, truncated: false, gaps: [], ...extra })
const assignment = (role = 'build', browser = false) => ({ goal: 'Do the work.', role, work_kind_id: 'k-maint', sources: [], allowed_actions: ['work.create', 'work.update', 'pr.open'] as const, runtime_requirements: { needs_native_host: true as const, needs_browser: browser, runtime_class: browser ? 'native_browser' as const : 'native_coding' as const }, budget: { mode: 'tokens' as const, token_ceiling: 400_000 } })
const consented = (revision: number) => ({ eligible: true, consent_required: true, wait_reason: '', project_revision: 2, consent_revision: revision })
function routine(rid: string, name: string, extra: Partial<Recurrence> & { scope?: 'project' | 'personal' | 'workspace'; owner?: string; agent?: ReturnType<typeof assignment> | null } = {}): Recurrence {
  const { scope = 'project', owner = me.id, agent = assignment(), ...rest } = extra
  const project = rest.project_id ?? 'p-pharos'
  return {
    id: rid, project_id: project, parent_id: project, paused: false, revision: 4, occurrence_count: 15, next_at: next, created_at: '2026-09-01T06:00:00Z', updated_at: '2026-10-05T06:00:00Z',
    template: { name, title: `${name} {{date}}`, description: 'Do the work.', acceptance_criteria: ['Done'], estimate_hours: 1, priority: 'medium', tags: [], type: 'work' },
    trigger: { kind: 'time', rrule: 'FREQ=WEEKLY;BYDAY=MO', time_of_day: '08:00', timezone: 'Europe/Vienna' }, queue_each: true, overlap_policy: 'skip', catch_up_policy: 'one',
    definition: { scope: scope === 'project' ? { kind: 'project', project_id: project } : { kind: scope }, owner_principal_id: owner, ...(agent ? { assignment: structuredClone(agent) as NonNullable<Recurrence['definition']>['assignment'] } : {}) },
    execution_policy: agent ? consented(4) : undefined, ...rest,
  } as Recurrence
}
const world = () => [
  routine(DEPS, 'Weekly dependency update', { last_run: { id: run(15), state: 'waiting_for_person', reason: 'A person must decide before execution can continue.', created_at: '2026-10-05T06:00:00Z' } }),
  routine(LINKS, 'Nightly link check', { agent: assignment('scout', true), trigger: { kind: 'time', rrule: 'FREQ=DAILY', time_of_day: '02:00', timezone: 'Europe/Vienna' }, last_run: { id: run(22), state: 'failed', reason: 'Execution failed and has not completed successfully.', created_at: '2026-10-11T00:00:00Z' } }),
  routine(NOTES, 'Release notes check', { scope: 'personal', project_id: 'p-aeon', agent: assignment('scout'), last_run: { id: run(4), state: 'running', reason: 'Execution is in progress.', created_at: '2026-10-11T01:02:00Z' } }),
  routine(TRIAGE, 'Monday triage', { project_id: 'p-aeon', agent: null, last_result: { recurrence_id: TRIAGE, occurrence_key: 'time:1', number: 41, scheduled_at: '2026-10-05T07:00:00Z', created_at: '2026-10-05T07:00:00Z', node_id: 'n-a1', source_event_id: null, outcome: 'created', reason: '', key: 'AEON-1', state: 'backlog' } }),
  routine(CLEANUP, 'Weekly cleanup', { scope: 'workspace', project_id: 'p-aeon', paused: true }),
  routine(TESTS, 'Test review', { project_id: 'p-aeon', owner: MIRA, agent: assignment('build-hard'), paused: true, execution_policy: { eligible: false, consent_required: true, wait_reason: 'execution_consent_required', project_revision: 2, consent_revision: 0 } }),
]
const entry = (rid: string, n: number, state: RoutineRun['state'], at: string, extra: Partial<RoutineRun> = {}): RoutineRun => ({ id: run(n), recurrence_id: rid, definition_revision: 4, owner_principal_id: me.id, state, reason: { created: 'The occurrence was created; execution has not started.', running: 'Execution is in progress.', waiting_for_person: 'A person must decide before execution can continue.', held: 'Execution is held until its required conditions are confirmed.', failed: 'Execution failed and has not completed successfully.', done: '', skipped: 'The run was skipped or cancelled.' }[state], work_node_id: 'n-1', work_order_id: 'n-1', agent_run_id: 'a-1', created_at: at, updated_at: at, usage: usage(), ...extra })
const RUNS: Record<string, RoutineRun[]> = {
  [DEPS]: [
    entry(DEPS, 15, 'waiting_for_person', '2026-10-05T06:00:00Z', { usage: usage({ tokens: { measured: '212000', known: 1, unknown: 0 }, reserved_tokens: '400000', paid_microusd: null, estimated_api_usd: null }) }),
    entry(DEPS, 14, 'done', '2026-09-28T06:00:00Z'),
    entry(DEPS, 13, 'failed', '2026-09-21T06:00:00Z', { usage: usage({ tokens: { measured: null, known: 0, unknown: 1 }, paid_microusd: null, estimated_api_usd: null, reserved_tokens: '80000', partial: true, gaps: ['source_visibility_incomplete', 'measurement_incomplete'] }) }),
    entry(DEPS, 12, 'created', '2026-09-14T06:00:00Z', { agent_run_id: null, usage: usage({ tokens: { measured: null, known: 0, unknown: 1 }, gaps: ['execution_usage_unknown'] }) }),
    entry(DEPS, 11, 'skipped', '2026-09-07T06:00:00Z', { work_node_id: null }),
  ],
  [LINKS]: [entry(LINKS, 22, 'failed', '2026-10-11T00:00:00Z', { usage: usage({ tokens: { measured: '46000', known: 1, unknown: 1 }, partial: true, gaps: ['source_visibility_incomplete'] }) }), entry(LINKS, 21, 'done', '2026-10-10T00:00:00Z')],
  [NOTES]: [entry(NOTES, 4, 'running', '2026-10-11T01:02:00Z', { usage: usage({ tokens: { measured: '64000', known: 1, unknown: 0 } }) })],
}
const attempt = (extra: Record<string, unknown> = {}) => ({ id: 'att-1', role: 'build', state: 'done', reason: '', agent_run_id: 'a-1', exit_confirmed: true, exit_code: 0, created_at: '2026-10-05T06:01:00Z', ...extra })
const action = (extra: Record<string, unknown> = {}) => ({ id: 'act-1', attempt_id: 'att-1', kind: 'pr.open', state: 'succeeded', target_node_id: 'n-1', evaluation_id: 'ev-1', evaluation_result: 'allow', approval_id: null, approval_state: null, effect_id: null, pull_request_number: 498, created_at: '2026-10-05T06:30:00Z', ...extra })
function detail(found: RoutineRun): RunDetail {
  const page = <T>(items: T[]) => ({ items, next_cursor: null })
  const evidence: Record<string, Pick<RunDetail, 'attempts' | 'actions' | 'effects'>> = {
    [run(15)]: { attempts: page([attempt()]), actions: page([action({ state: 'needs_person', evaluation_result: 'needs_person', approval_id: '5a000000-0000-4000-8000-000000000001', approval_state: 'pending', pull_request_number: 530 })]), effects: page([]) },
    [run(14)]: { attempts: page([attempt()]), actions: page([action()]), effects: page([]) },
    [run(13)]: { attempts: page([attempt({ state: 'failed', exit_code: 1 })]), actions: page([action({ state: 'failed', pull_request_number: null })]), effects: page([]) },
    [run(22)]: { attempts: page([attempt({ role: 'scout' })]), actions: page([action({ kind: 'work.create', state: 'failed', pull_request_number: null })]), effects: page([{ id: 'ef-1', action_id: 'act-1', kind: 'work.create', state: 'failed', created_at: '2026-10-11T00:19:00Z' }]) },
  }
  return { run: found, ...(evidence[found.id] ?? { attempts: page([]), actions: page([]), effects: page([]) }) } as RunDetail
}
interface Setup { viewer: 'markus' | 'mira'; olderFails?: boolean }
async function setup(page: Page, options: Partial<Setup> = {}) {
  const errors = watchErrors(page), state: Setup = { viewer: 'markus', ...options }
  // A gate holds one routine's runs page: arrived when the request reaches the mock, open to answer, answered after the attempt.
  const gates = new Map<string, { arrived: () => void; open: Promise<void>; answered: () => void }>()
  await mockWork(page, fixtures(), { admin: true })
  await page.route('**/api/me/permissions**', route => {
    const effective = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') || undefined)
    ;(effective.workspace.permissions as string[]).push('recurrences.manage', 'run.create')
    if (effective.project) effective.project = { ...effective.project, permissions: [...effective.project.permissions, 'recurrences.manage', 'run.create'] }
    return route.fulfill({ json: effective })
  })
  await page.route('**/api/members', route => route.fulfill({ json: { people: [{ principal_id: me.id, name: me.name, avatar_url: null, has_avatar: false }, { principal_id: MIRA, name: 'Mira Holm', avatar_url: null, has_avatar: false }], agents: [], invites: [], imported: [], owner_count: 1 } }))
  await page.route('**/api/models/resolve**', route => route.fulfill({ json: { role: new URL(route.request().url()).searchParams.get('role'), profile: { id: '57000000-0000-4000-8000-000000000001', slug: 'opus', version: '1', harness: 'claude', family: 'anthropic', model: 'claude-opus-5-5', effort: 'xhigh', tier: 'frontier', enabled: true, created_at: next, display_name: 'Claude Opus', short_name: 'Opus', model_version: '5.5' }, owner_required: false } }))
  const visible = () => world().filter(item => state.viewer === 'markus' || item.definition?.scope.kind !== 'personal')
  await page.route('**/api/recurrences**', async route => {
    const url = new URL(route.request().url()), path = url.pathname, parts = path.split('/')
    if (path === '/api/recurrences') {
      // Two pages; the second repeats a definition the first already listed.
      const items = visible()
      return route.fulfill({ json: url.searchParams.get('after') === 'p2' ? { items: [items.find(item => item.id === TESTS), items.find(item => item.id === DEPS)].filter(Boolean), next_cursor: null } : { items: items.filter(item => item.id !== TESTS), next_cursor: 'p2' } })
    }
    const rid = parts[3]!, item = visible().find(row => row.id === rid)
    if (!item) return route.fulfill({ status: 404, json: { error: 'recurrence not found' } })
    if (parts[4] === 'runs' && parts[5]) { const found = (RUNS[rid] ?? []).find(row => row.id === parts[5]); return found ? route.fulfill({ json: detail(found) }) : route.fulfill({ status: 404, json: { error: 'run not found' } }) }
    if (parts[4] === 'runs') {
      const gate = gates.get(rid)
      if (gate) { gates.delete(rid); gate.arrived(); await gate.open; await route.fulfill({ json: { items: RUNS[rid] ?? [], next_cursor: null } }).catch(() => {}); gate.answered(); return }
      if (url.searchParams.get('before')) return state.olderFails ? route.fulfill({ status: 503, json: { error: 'the logbook read timed out' } }) : route.fulfill({ json: { items: [], next_cursor: null } })
      return route.fulfill({ json: { items: RUNS[rid] ?? [], next_cursor: rid === DEPS ? 'older-1' : null } })
    }
    if (parts[4] === 'history') {
      const filter = url.searchParams.get('filter')
      const occurred = (n: number, key: string, at: string) => ({ id: n, type: 'recurrence.occurred', at, actor_name: 'Scheduler', before: null, after: { recurrence_id: rid, occurrence_key: `time:${n}`, number: n, scheduled_at: at, created_at: at, node_id: 'n-a1', source_event_id: null, outcome: 'created', reason: '' }, node: { key, title: 'Monday triage', state: 'backlog' } })
      const skipped = { id: 39, type: 'recurrence.skipped', at: '2026-09-21T07:00:00Z', actor_name: 'Scheduler', before: null, after: { recurrence_id: rid, occurrence_key: 'time:39', number: 39, scheduled_at: '2026-09-21T07:00:00Z', created_at: '2026-09-21T07:00:00Z', node_id: null, source_event_id: null, outcome: 'skipped', reason: 'previous_occurrence_open' }, node: null }
      const change = { id: 9, type: 'recurrence.updated', at: '2026-09-19T09:20:00Z', actor_name: me.name, before: null, after: null, node: null }
      const all = rid === TRIAGE ? [occurred(41, 'AEON-1', '2026-10-05T07:00:00Z'), skipped, change] : [change]
      return route.fulfill({ json: { items: all.filter(row => !filter || filter === 'all' || (filter === 'created' ? row.type === 'recurrence.occurred' : filter === 'skipped' ? row.type === 'recurrence.skipped' : row.type === 'recurrence.updated')), next_cursor: null } })
    }
    return route.fulfill({ json: item })
  })
  function hold(rid: string) {
    let release!: () => void, arrived!: () => void, answered!: () => void
    const open = new Promise<void>(resolve => { release = resolve })
    const seen = new Promise<void>(resolve => { arrived = resolve }), done = new Promise<void>(resolve => { answered = resolve })
    gates.set(rid, { arrived, open, answered })
    return { seen, release, done }
  }
  return { errors, state, hold }
}
const list = (page: Page) => page.getByRole('region', { name: 'Routine list', exact: true })
const logbook = (page: Page) => page.locator('[data-logbook]')
const row = (page: Page, rid: string) => page.locator(`[data-routine="${rid}"]`)
const runButton = (page: Page, n: number) => page.locator(`[data-run="${run(n)}"]`)
// The page scrolls inside main: evidence starts at its top.
async function shot(page: Page, info: TestInfo, name: string) { await page.evaluate(() => document.querySelector('main')?.scrollTo(0, 0)); await page.screenshot({ path: info.outputPath(`${name}.png`) }) }

test('one row per routine; filters per kind; failed never green; created is not finished; controls stay put', async ({ page }, info) => {
  await page.setViewportSize({ width: 1440, height: 1100 })
  const { errors } = await setup(page, { olderFails: true })
  await page.goto('/agents/routines')
  await expect(page.getByRole('heading', { level: 1, name: 'Routines' })).toBeVisible()
  await expect(list(page).locator('[data-routine]')).toHaveCount(5)
  await expect(page.locator('[data-scope="all"]')).toContainText('5+')
  // More routines appends the next page once per definition.
  await list(page).getByRole('button', { name: 'More routines' }).click()
  await expect(list(page).locator('[data-routine]')).toHaveCount(6)
  await expect(row(page, DEPS)).toHaveCount(1)
  await expect(page.locator('[data-scope="all"]')).toContainText('6')
  // States and their reasons, in words.
  await expect(row(page, LINKS)).toContainText('Failed')
  await expect(row(page, NOTES)).toContainText('Running')
  await expect(row(page, TESTS)).toContainText('Mira Holm decides')
  await expect(row(page, CLEANUP)).toContainText('Stopped')
  await expect(row(page, TRIAGE)).toContainText('Last: AEON-1 · #41')
  await expect(row(page, DEPS)).toContainText('Waiting for a person')

  // The first row by name opens: a ticket-only routine keeps today's filters.
  await expect(logbook(page).getByRole('heading', { level: 2 })).toHaveText('Monday triage')
  await expect(logbook(page).locator('[data-lf]')).toHaveText(['All', 'Created', 'Skipped', 'Changes'])
  await expect(logbook(page)).toContainText('#41 · Created AEON-1 · backlog')

  const tools = { all: page.locator('[data-scope="all"]'), workspace: page.locator('[data-scope="workspace"]'), project: page.locator('#routines-project'), failedCount: page.locator('[data-sf="failed"]') }
  await expectStableControls({
    controls: { ...tools, deps: row(page, DEPS), links: row(page, LINKS) },
    interactions: [
      { name: 'Row: Nightly link check', run: async () => { await row(page, LINKS).click(); await expect(logbook(page).getByRole('heading', { level: 2 })).toHaveText('Nightly link check') } },
      { name: 'Row: Weekly dependency update', run: async () => { await row(page, DEPS).click(); await expect(logbook(page).getByRole('heading', { level: 2 })).toHaveText('Weekly dependency update') } },
    ],
  })
  // An agent routine's logbook: Runs and Changes; the run that waits opens first.
  await expect(logbook(page).locator('[data-lf]')).toHaveText(['Runs', 'Changes'])
  await expect(runButton(page, 15)).toHaveAttribute('aria-expanded', 'true')
  await expect(logbook(page).locator('[data-step="person"]')).toContainText('Open on the Decision Desk')
  await expect(logbook(page).getByRole('link', { name: /Open on the Decision Desk/ })).toHaveAttribute('href', '/decision-desk?item=a:5a000000-0000-4000-8000-000000000001')
  await expect(runButton(page, 12)).toContainText('a created ticket is not a finished run')
  await expect(runButton(page, 13)).toContainText('Usage unknown')
  await expect(runButton(page, 15)).toContainText('212k tokens so far')
  await expectStableControls({
    controls: { first: runButton(page, 15), second: runButton(page, 14), runs: logbook(page).locator('[data-lf="runs"]'), changes: logbook(page).locator('[data-lf="changes"]'), pause: logbook(page).getByRole('button', { name: 'Pause', exact: true }) },
    interactions: [
      { name: 'Run #14 opens, #15 stays open', run: async () => { await runButton(page, 14).click(); await expect(runButton(page, 14)).toHaveAttribute('aria-expanded', 'true'); await expect(runButton(page, 15)).toHaveAttribute('aria-expanded', 'true') } },
      { name: 'Run #13 opens', run: async () => { await runButton(page, 13).click(); await expect(logbook(page).locator('.run.bad.open [data-step="agent"]')).toContainText('stopped with an error (exit 1)') } },
    ],
  })
  // A failed run and its failed steps never use the Done colour.
  const colours = await logbook(page).evaluate(root => {
    const done = getComputedStyle(root.querySelector('.run.ok .r-ic')!).color
    return [...root.querySelectorAll('.run.bad .r-ic, .run.bad .run-t b, .stp.bad .s-ic')].map(element => getComputedStyle(element).color === done)
  })
  expect(colours.length).toBeGreaterThan(2)
  expect(colours.some(Boolean)).toBe(false)
  await shot(page, info, 'routines-1440-light')
  // A failed older page says so and keeps what is shown.
  await logbook(page).locator('[data-older]').click()
  await expect(logbook(page).locator('.lb-foot')).toContainText('Older entries couldn’t be read (the logbook read timed out). Shown: the newest 5.')
  await expect(logbook(page).locator('[data-run]')).toHaveCount(5)
  await expectStableControls({
    controls: { runs: logbook(page).locator('[data-lf="runs"]'), changes: logbook(page).locator('[data-lf="changes"]'), facts: logbook(page).locator('.lb-facts'), pause: logbook(page).getByRole('button', { name: 'Pause', exact: true }) },
    interactions: [
      { name: 'Logbook: Changes', run: async () => { await logbook(page).locator('[data-lf="changes"]').click(); await expect(logbook(page)).toContainText('Changed by Markus Barta') } },
      { name: 'Logbook: Runs', run: async () => { await logbook(page).locator('[data-lf="runs"]').click(); await expect(runButton(page, 15)).toBeVisible() } },
    ],
  })
  // State and scope filters change the rows, never the controls.
  await expectStableControls({
    controls: tools,
    interactions: [
      { name: 'State: failed', run: async () => { await tools.failedCount.click(); await expect(list(page).locator('[data-routine]')).toHaveCount(1); await expect(row(page, LINKS)).toBeVisible() } },
      { name: 'State: all again', run: async () => { await tools.failedCount.click(); await expect(list(page).locator('[data-routine]')).toHaveCount(6) } },
      { name: 'Scope: workspace', run: async () => { await tools.workspace.click(); await expect(list(page).locator('[data-routine]')).toHaveCount(1); await expect(row(page, CLEANUP)).toBeVisible() } },
      { name: 'Scope: all', run: async () => { await tools.all.click(); await expect(list(page).locator('[data-routine]')).toHaveCount(6) } },
    ],
  })
  await expect(page).toHaveURL(/\/agents\/routines\?routine=/)
  await page.emulateMedia({ colorScheme: 'dark' })
  await shot(page, info, 'routines-1440-dark')
  expect(errors).toEqual([])
})

test('a late page for another routine cannot alter the logbook on screen', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const { errors, hold } = await setup(page)
  const gate = hold(LINKS)
  await page.goto(`/agents/routines?routine=${LINKS}`)
  await gate.seen
  // How the held request ends: aborted with its logbook, or answered late.
  const ended = new Promise<string>(resolve => {
    page.on('requestfailed', request => { if (request.url().includes(`/api/recurrences/${LINKS}/runs`)) resolve('aborted') })
    page.on('requestfinished', request => { if (request.url().includes(`/api/recurrences/${LINKS}/runs`)) resolve('answered') })
  })
  await row(page, DEPS).click()
  await expect(logbook(page).getByRole('heading', { level: 2 })).toHaveText('Weekly dependency update')
  await expect(runButton(page, 15)).toBeVisible()
  // Release the earlier routine's page and wait until the mock has answered it.
  gate.release()
  await gate.done
  expect(['aborted', 'answered']).toContain(await ended)
  await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))))
  await expect(logbook(page).getByRole('heading', { level: 2 })).toHaveText('Weekly dependency update')
  await expect(runButton(page, 22)).toHaveCount(0)
  await expect(logbook(page).locator('[data-run]')).toHaveCount(5)
  expect(errors).toEqual([])
})

test('a person switch clears the open logbook, filters and the previous person’s routines', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const { errors, state } = await setup(page)
  await page.goto(`/agents/routines?scope=personal&routine=${NOTES}`)
  await expect(logbook(page).getByRole('heading', { level: 2 })).toHaveText('Release notes check')
  await expect(row(page, NOTES)).toBeVisible()
  // Mira signs in on another tab; the next navigation here finds her session.
  state.viewer = 'mira'
  await page.route('**/api/me', route => route.fulfill({ json: { principal: { id: MIRA, name: 'Mira Holm', kind: 'person', roles: ['admin'] }, tenant: { id: 't1', name: 'INSPR Studio' } } }))
  await page.locator('[data-scope="project"]').click()
  await expect(page.getByText(/Signed in as Mira Holm · the previous person’s open logbook, filters and draft were cleared/)).toBeVisible()
  await expect(page).toHaveURL(/\/agents\/routines$/)
  await expect(page.locator('[data-scope="all"]')).toHaveAttribute('aria-checked', 'true')
  await expect(list(page).locator('[data-routine]')).toHaveCount(4)
  await expect(row(page, NOTES)).toHaveCount(0)
  await expect(page.getByText('Release notes check')).toHaveCount(0)
  expect(errors).toEqual([])
})

test.describe('phone', () => {
  test.use({ viewport: { width: 390, height: 860 }, hasTouch: true, isMobile: true })
  for (const theme of ['dark', 'light'] as const) test(`the list, then one logbook with a way back, in ${theme}`, async ({ page }, info) => {
    await page.emulateMedia({ colorScheme: theme })
    const { errors } = await setup(page)
    await page.goto('/agents/routines')
    await expect(list(page).locator('[data-routine]')).toHaveCount(5)
    await expect(logbook(page)).toHaveCount(0)
    // Touch targets in the head and filters are at least 44 px.
    for (const control of [page.locator('[data-scope="all"]'), page.locator('[data-sf="failed"]'), page.locator('#routines-project'), row(page, LINKS)]) {
      const box = (await control.boundingBox())!
      expect(box.height, await control.textContent() ?? '').toBeGreaterThanOrEqual(43.5)
    }
    await shot(page, info, `routines-390-${theme}-list`)
    await row(page, LINKS).tap()
    await expect(list(page)).toBeHidden()
    await expect(logbook(page).getByRole('heading', { level: 2 })).toHaveText('Nightly link check')
    await expect(runButton(page, 22)).toHaveAttribute('aria-expanded', 'true')
    await expect(logbook(page).locator('[data-step="effect"]')).toContainText('nothing was filed')
    const width = await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)
    expect(width).toBeLessThanOrEqual(0)
    await shot(page, info, `routines-390-${theme}-logbook`)
    await logbook(page).getByRole('button', { name: 'All routines' }).tap()
    await expect(list(page)).toBeVisible()
    await expect(logbook(page)).toHaveCount(0)
    await expect(row(page, LINKS)).toBeFocused()
    expect(errors).toEqual([])
  })
})

test('the Agents page offers Routines in More, and one Routines line only while something waits or failed', async ({ page }, info) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const { errors } = await setup(page)
  const data = agentData({ me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' } })
  data.sessions.splice(1); data.approvals.splice(0); data.messages.splice(0); data.targets.splice(0)
  await mockAgents(page, data)
  let quiet = false
  await page.route(url => url.pathname === '/api/recurrences', route => quiet ? route.fulfill({ json: { items: [world()[3]], next_cursor: null } }) : route.fallback())
  await page.goto('/agents')
  const line = page.locator('.rt-line')
  await expect(line).toContainText('Routines')
  await expect(line).toContainText('1+ wait for a person · 1+ failed · next Mon 12 Oct, 08:00')
  await page.locator('.page-head').getByRole('button', { name: 'More agent actions', exact: true }).click()
  await expect(page.getByRole('menuitem', { name: /^Routines/ })).toHaveAttribute('href', '/agents/routines')
  await shot(page, info, 'agents-routines-line')
  await page.keyboard.press('Escape')
  await line.click()
  await expect(page).toHaveURL(/\/agents\/routines$/)
  quiet = true
  // Absence counts only once the quiet list has been read and the page drew it.
  const read = page.waitForResponse(response => new URL(response.url()).pathname === '/api/recurrences')
  await page.goto('/agents')
  await read
  await expect(page.locator('.agents-page .main-col')).toBeVisible()
  await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))))
  await expect(line).toHaveCount(0)
  expect(errors).toEqual([])
})
