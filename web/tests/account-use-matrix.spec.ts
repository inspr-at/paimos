// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1054 (concept AEON-1044 §2.5, slice S4), calmer in AEON-1173 (design
// AEON-1149): Settings › Accounts › "Where accounts may work". Risk: the screen
// shows a tick, a rule value or an Undo the server did not store, a rule save
// carries new_models, or a control moves while a person works the matrix.
// The mock below keeps the server's contract: every write carries the matrix
// revision, a competing change answers 409 and saves nothing, bulk scopes cover
// every account or context, a rule save names only its rules, and Undo is an
// ordinary revision-checked save.
// AEON1054_SHOTS=1 also writes screenshots to the test's output folder.
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { agentData, mockAgents, type AgentWorld } from './agents-fixtures'
import { ACCOUNTS, NOW, TZ, capacityWorld } from './capacity-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { controlStability } from './control-stability'
import { mockPairing } from './agent-pairing-fixtures'

test.use({ timezoneId: TZ })
const C = { def: 'e0000000-0000-4000-8000-000000000001', acme: 'e0000000-0000-4000-8000-000000000002', beta: 'e0000000-0000-4000-8000-000000000003', hold: 'e0000000-0000-4000-8000-000000000004' }
const RUN = 'a1000000-0000-4000-8000-000000000001'
type Cell = { account_id: string; context_id: string; allowed: boolean }
interface UseWorld {
  rules: { new_accounts: string; new_contexts: string; new_projects: string; new_models: string; revision: number; enforced_at: string | null; confirmation_required: boolean; confirmed_at: string | null }
  accounts: { id: string; label: string; harness: string; plan: string | null; billing_mode: string; owner_id: string | null; awaiting?: boolean }[]
  contexts: { id: string; name: string; kind: string; new_accounts_override: string | null; archived_at: string | null; revision: number }[]
  cells: Set<string>
  running: { run_id: string; account_id: string; node_id: string | null }[]
  truncated: boolean
  /** The next write meets a competing save first: the server moves on and answers 409. */
  conflict: boolean
  writes: { method: string; path: string; body: Record<string, unknown> }[]
  project: { context_id: string } | null
  /** Matrix reads fail (the server is unreachable after a write). */
  failReads: boolean
  holding: { count: number; first: { project_id: string; key: string; name: string }[] }
}
const key = (a: string, c: string) => `${a}:${c}`
function useWorld(over: Partial<UseWorld> = {}): UseWorld {
  return {
    rules: { new_accounts: 'ask', new_contexts: 'ask', new_projects: 'default', new_models: 'allow', revision: 5, enforced_at: null, confirmation_required: false, confirmed_at: null },
    accounts: [
      { id: ACCOUNTS.main, label: 'Main', harness: 'codex', plan: 'Pro', billing_mode: 'subscription', owner_id: null },
      { id: ACCOUNTS.spare, label: 'Spare', harness: 'codex', plan: 'Pro', billing_mode: 'subscription', owner_id: null },
      { id: ACCOUNTS.claude, label: 'markus', harness: 'claude', plan: 'Max', billing_mode: 'subscription', owner_id: null },
    ],
    contexts: [
      { id: C.acme, name: 'Acme client work', kind: 'regular', new_accounts_override: null, archived_at: null, revision: 1 },
      { id: C.def, name: 'Default', kind: 'default', new_accounts_override: null, archived_at: null, revision: 1 },
      { id: C.beta, name: 'Beta', kind: 'regular', new_accounts_override: 'deny', archived_at: null, revision: 1 },
      { id: C.hold, name: 'Unassigned', kind: 'holding', new_accounts_override: null, archived_at: null, revision: 1 },
    ],
    cells: new Set([key(ACCOUNTS.main, C.def), key(ACCOUNTS.main, C.acme), key(ACCOUNTS.main, C.beta), key(ACCOUNTS.spare, C.def)]),
    running: [], truncated: false, conflict: false, writes: [], project: null, failReads: false, holding: { count: 0, first: [] }, ...over,
  }
}
const tickableIds = (w: UseWorld) => w.contexts.filter(c => c.kind !== 'holding' && !c.archived_at).map(c => c.id)
/** One page like the server: independent exclusive id cursors, cells of the rectangle only. Rows keep the fixture's
 *  order (the server sorts by id); paged fixtures list their ids in ascending order wherever a cursor reaches. */
function matrixJSON(w: UseWorld, query: URLSearchParams) {
  const limit = Number(query.get('limit') ?? 100), afterA = query.get('after_account') ?? '', afterC = query.get('after_context') ?? ''
  const slice = <T extends { id: string }>(items: T[], after: string) => {
    const rest = items.filter(i => !after || i.id > after)
    return { items: rest.slice(0, limit), next: rest.length > limit ? rest[limit - 1]!.id : null }
  }
  const accounts = slice(w.accounts, afterA), contexts = slice(w.contexts.filter(c => !c.archived_at), afterC)
  const inPage = new Set([...accounts.items.map(a => a.id), ...contexts.items.map(c => c.id)])
  return {
    rules: w.rules, accounts: accounts.items, contexts: contexts.items,
    cells: [...w.cells].map(k => { const [account_id, context_id] = k.split(':'); return { account_id, context_id, allowed: true } }).filter(c => inPage.has(c.account_id!) && inPage.has(c.context_id!)),
    next_account: accounts.next, next_context: w.truncated ? C.hold : contexts.next, running_outside: w.running, running_outside_truncated: w.truncated,
    holding_projects: w.holding,
  }
}
async function mockUse(page: Page, w: UseWorld) {
  const conflictCheck = (expected: unknown) => {
    if (w.conflict) { w.conflict = false; w.rules.revision++; w.cells.delete(key(ACCOUNTS.main, C.acme)); return true }
    return expected !== w.rules.revision
  }
  await page.route('**/api/account-use**', async route => {
    const request = route.request(), url = new URL(request.url()), method = request.method()
    if (method === 'GET') return w.failReads ? route.fulfill({ status: 503, json: { error: 'unavailable' } }) : route.fulfill({ json: matrixJSON(w, url.searchParams) })
    const body = request.postDataJSON() as Record<string, unknown>
    w.writes.push({ method, path: url.pathname, body })
    if (conflictCheck(body.expected_revision)) return route.fulfill({ status: 409, json: { error: 'account_use_revision_conflict' } })
    if (url.pathname === '/api/account-use/cells') {
      let changes = body.changes as Cell[] | undefined
      const bulk = body.bulk as { scope: string; account_id?: string; context_id?: string; allowed: boolean } | undefined
      if (bulk) changes = w.accounts.filter(a => !bulk.account_id || a.id === bulk.account_id).flatMap(a => tickableIds(w).filter(c => !bulk.context_id || c === bulk.context_id).map(c => ({ account_id: a.id, context_id: c, allowed: bulk.allowed })))
      const undo = changes!.map(c => ({ ...c, allowed: w.cells.has(key(c.account_id, c.context_id)) }))
      for (const c of changes!) { if (c.allowed) w.cells.add(key(c.account_id, c.context_id)); else w.cells.delete(key(c.account_id, c.context_id)) }
      for (const a of w.accounts) if (changes!.some(c => c.account_id === a.id)) a.awaiting = false
      w.rules.revision++
      return route.fulfill({ json: { revision: w.rules.revision, changes, undo } })
    }
    if (url.pathname === '/api/account-use/rules') {
      const { expected_revision: _, ...values } = body
      Object.assign(w.rules, values); w.rules.revision++
      return route.fulfill({ json: w.rules })
    }
    if (url.pathname === '/api/account-use/confirm') {
      Object.assign(w.rules, { confirmation_required: false, confirmed_at: new Date(NOW).toISOString() }); w.rules.revision++
      return route.fulfill({ json: w.rules })
    }
    return route.fulfill({ status: 404, json: { error: 'not found' } })
  })
  await page.route('**/api/work-contexts**', async route => {
    const request = route.request(), url = new URL(request.url()), method = request.method()
    const body = request.postDataJSON() as Record<string, unknown>
    w.writes.push({ method, path: url.pathname, body })
    if (conflictCheck(body.expected_revision)) return route.fulfill({ status: 409, json: { error: 'account_use_revision_conflict' } })
    w.rules.revision++
    if (method === 'POST') {
      const context = { id: 'e0000000-0000-4000-8000-000000000009', name: String(body.name), kind: 'regular', new_accounts_override: null, archived_at: null, revision: 1 }
      w.contexts.push(context)
      if (w.rules.new_contexts === 'allow') for (const a of w.accounts) w.cells.add(key(a.id, context.id))
      return route.fulfill({ json: { context, revision: w.rules.revision } })
    }
    const context = w.contexts.find(c => url.pathname.endsWith(c.id))!
    Object.assign(context, { name: body.name, new_accounts_override: body.new_accounts_override ?? null, archived_at: body.archived ? new Date(NOW).toISOString() : null, revision: context.revision + 1 })
    return route.fulfill({ json: { context, revision: w.rules.revision } })
  })
  await page.route('**/api/projects/*/work-context', async route => {
    const request = route.request(), method = request.method()
    if (method === 'GET') return w.project ? route.fulfill({ json: { project_id: 'p-pharos', context_id: w.project.context_id, revision: w.rules.revision } }) : route.fulfill({ status: 404, json: { error: 'account-use resource not found' } })
    const body = request.postDataJSON() as Record<string, unknown>
    w.writes.push({ method, path: new URL(request.url()).pathname, body })
    if (conflictCheck(body.expected_revision)) return route.fulfill({ status: 409, json: { error: 'account_use_revision_conflict' } })
    w.rules.revision++; w.project = { context_id: String(body.context_id) }
    return route.fulfill({ json: { project_id: 'p-pharos', context_id: w.project.context_id, revision: w.rules.revision } })
  })
}
function grant(page: Page, extra: string[] = []) {
  return page.route('**/api/me/permissions*', route => {
    const answer = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    answer.workspace.permissions = [...answer.workspace.permissions, 'account.read', 'account.manage', 'account.use.manage', ...extra]
    return route.fulfill({ json: answer })
  })
}
const world: AgentWorld = { me: me.id, now: NOW, projects: {}, tickets: {}, nodes: {} }
async function setup(page: Page, over: Partial<UseWorld> = {}) {
  await page.clock.setSystemTime(NOW)
  await mockWork(page, fixtures(), { admin: true })
  const data = agentData(world), capacity = capacityWorld({})
  data.accounts = capacity.accounts as unknown as typeof data.accounts
  await mockAgents(page, data, { capacity })
  await grant(page)
  const w = useWorld(over)
  await mockUse(page, w)
  return w
}
const zone = (page: Page) => page.locator('#account-use')
const cell = (page: Page, account: string, context: string) => zone(page).locator(`[data-use-cell="${account}:${context}"]`)
const rowBox = (page: Page, account: string) => zone(page).locator(`[data-use-row="${account}"] th input`)
const colBox = (page: Page, context: string) => zone(page).locator(`[data-use-col="${context}"] input`)
const corner = (page: Page) => zone(page).locator('[data-use-all]')
const sw = (page: Page, title: string) => zone(page).getByRole('radiogroup', { name: title })
const rulesToggle = (page: Page) => zone(page).getByRole('button', { name: /^For new things/ })
const toastUndo = (page: Page, text: string | RegExp) => page.locator('.toast').filter({ hasText: text }).getByRole('button', { name: 'Undo' })
async function open(page: Page) {
  await page.goto('/settings/accounts')
  await expect(zone(page).getByRole('table', { name: 'Where accounts may work' })).toBeVisible()
}
async function openRules(page: Page) {
  await rulesToggle(page).click()
  await expect(rulesToggle(page)).toHaveAttribute('aria-expanded', 'true')
  await expect(sw(page, 'New accounts')).toBeVisible()
}
const indeterminate = (page: Page, account: string) => rowBox(page, account).evaluate(el => (el as HTMLInputElement).indeterminate)
const lastWrite = (w: UseWorld) => w.writes.at(-1)

test('bulk, tri-state corner, rows and columns, single cells and Undo all save with the matrix revision', async ({ page }) => {
  const errors = watchErrors(page)
  const w = await setup(page)
  await open(page)
  // Columns: Default first, then by name; Unassigned never gets a column. The footnotes explain both.
  await expect(zone(page).locator('thead th[data-use-col] b')).toHaveText(['Default', 'Acme client work', 'Beta'])
  await expect(zone(page).locator('.foot-notes')).toContainText('Default also covers work without a project.')
  await expect(zone(page).locator('[data-use-holding]')).toHaveText('Unassigned holds projects waiting for a context; no account works there.')
  // The column sub-lines are gone except "new accounts: never".
  await expect(zone(page).locator('thead th[data-use-col] small')).toHaveText(['new accounts: never'])
  expect(await corner(page).evaluate(el => (el as HTMLInputElement).indeterminate)).toBe(true)
  await expect(rowBox(page, ACCOUNTS.main)).toBeChecked()
  expect(await indeterminate(page, ACCOUNTS.spare)).toBe(true)
  await expect(rowBox(page, ACCOUNTS.claude)).not.toBeChecked()
  expect(await indeterminate(page, ACCOUNTS.claude)).toBe(false)
  await expect(zone(page).locator(`[data-use-row="${ACCOUNTS.main}"] small`)).toHaveText('Pro · Subscription · No owner linked')

  // A part-ticked row resolves to allowed everywhere for that account.
  await rowBox(page, ACCOUNTS.spare).click()
  await expect.poll(() => lastWrite(w)).toEqual({ method: 'PATCH', path: '/api/account-use/cells', body: { expected_revision: 5, bulk: { scope: 'account', account_id: ACCOUNTS.spare, allowed: true } } })
  await expect(rowBox(page, ACCOUNTS.spare)).toBeChecked()
  expect(await indeterminate(page, ACCOUNTS.spare)).toBe(false)

  // One cell; the column becomes fully allowed.
  await cell(page, ACCOUNTS.claude, C.acme).click()
  await expect.poll(() => lastWrite(w)?.body).toEqual({ expected_revision: 6, changes: [{ account_id: ACCOUNTS.claude, context_id: C.acme, allowed: true }] })
  await expect(colBox(page, C.acme)).toBeChecked()

  // Undo in the save's message sends the inverse with the revision its own save returned.
  await expect(zone(page).getByRole('button', { name: /^Undo/ })).toHaveCount(0)
  await toastUndo(page, 'Allowed markus in Acme client work').click()
  await expect.poll(() => lastWrite(w)?.body).toEqual({ expected_revision: 7, changes: [{ account_id: ACCOUNTS.claude, context_id: C.acme, allowed: false }] })
  await expect(cell(page, ACCOUNTS.claude, C.acme)).not.toBeChecked()
  // Undone once: U finds nothing more to undo.
  const undone = w.writes.length
  await page.locator('body').press('u')
  await expect(page.locator('.toast').filter({ hasText: 'Undone: Allowed markus in Acme client work' })).toBeVisible()
  expect(w.writes.length).toBe(undone)

  // A fully ticked column resolves to "no account" for that context.
  await colBox(page, C.def).click()
  await expect.poll(() => lastWrite(w)?.body).toEqual({ expected_revision: 8, bulk: { scope: 'context', context_id: C.def, allowed: true } })
  await expect(colBox(page, C.def)).toBeChecked()
  await colBox(page, C.def).click()
  await expect.poll(() => lastWrite(w)?.body).toEqual({ expected_revision: 9, bulk: { scope: 'context', context_id: C.def, allowed: false } })
  await expect(colBox(page, C.def)).not.toBeChecked()

  // The corner box replaces Allow all and Allow none: part-ticked resolves to every account everywhere.
  await expect(zone(page).getByRole('button', { name: /^Allow (all|none)$/ })).toHaveCount(0)
  const before = [...w.cells].sort()
  await corner(page).click()
  await expect.poll(() => lastWrite(w)?.body).toEqual({ expected_revision: 10, bulk: { scope: 'all', allowed: true } })
  await expect(zone(page).locator('[data-use-cell]:checked')).toHaveCount(9)
  await expect(corner(page)).toBeChecked()
  // Undo from the toast: every cell comes back as stored before.
  await toastUndo(page, 'Allowed every account everywhere').click()
  await expect.poll(() => [...w.cells].sort()).toEqual(before)
  await expect.poll(() => lastWrite(w)?.body.expected_revision).toBe(11)
  await expect(zone(page).locator('[data-use-cell]:checked')).toHaveCount(before.length)

  // Fully ticked, the corner allows no account anywhere; U (outside a text field) undoes it.
  await corner(page).click()
  await expect(corner(page)).toBeChecked()
  await corner(page).click()
  await expect.poll(() => lastWrite(w)?.body).toEqual({ expected_revision: 13, bulk: { scope: 'all', allowed: false } })
  await expect(zone(page).locator('[data-use-cell]:checked')).toHaveCount(0)
  await page.locator('body').press('u')
  await expect.poll(() => w.cells.size).toBe(9)
  await expect(zone(page).locator('[data-use-cell]:checked')).toHaveCount(9)
  expect(errors).toEqual([])
})

test('a competing change answers 409: nothing is claimed as saved and the server state is shown', async ({ page }) => {
  const w = await setup(page)
  await open(page)
  await expect(cell(page, ACCOUNTS.main, C.acme)).toBeChecked()
  w.conflict = true
  await cell(page, ACCOUNTS.claude, C.beta).click()
  await expect(page.locator('.toast').filter({ hasText: 'Someone changed the matrix meanwhile' })).toContainText('was not saved')
  // The optimistic tick is gone and the competing save (Main leaves Acme) is shown.
  await expect(cell(page, ACCOUNTS.claude, C.beta)).not.toBeChecked()
  await expect(cell(page, ACCOUNTS.main, C.acme)).not.toBeChecked()
  // Nothing was saved, so there is nothing to undo.
  const writes = w.writes.length
  await page.locator('body').press('u')
  expect(w.writes.length).toBe(writes)
  expect(w.cells.has(key(ACCOUNTS.claude, C.beta))).toBe(false)
  // The next save uses the revision the server now has.
  await cell(page, ACCOUNTS.claude, C.beta).click()
  await expect.poll(() => lastWrite(w)?.body.expected_revision).toBe(6)
  await expect(cell(page, ACCOUNTS.claude, C.beta)).toBeChecked()
})

test('a refused write takes its tick back at once, even when the matrix cannot be read again', async ({ page }) => {
  const w = await setup(page)
  await open(page)
  w.conflict = true; w.failReads = true
  await cell(page, ACCOUNTS.claude, C.beta).click()
  await expect(page.locator('.toast').filter({ hasText: 'Someone changed the matrix meanwhile' })).toContainText('was not saved')
  // Nothing was stored, so the tick is gone although the server state could not be read.
  await expect(cell(page, ACCOUNTS.claude, C.beta)).not.toBeChecked()
  await expect(page.locator('.toast').filter({ hasText: 'The matrix could not be read again' })).toContainText('may be out of date')
  expect(w.cells.has(key(ACCOUNTS.claude, C.beta))).toBe(false)
})

test('archive sends the revision the menu opened on: a rename meanwhile is refused, never overwritten', async ({ page }) => {
  const w = await setup(page)
  await open(page)
  await zone(page).getByRole('button', { name: 'More for Acme client work' }).click()
  await page.getByRole('menuitem', { name: 'Archive…' }).click()
  const dialog = page.getByRole('dialog', { name: 'Archive Acme client work?' })
  await expect(dialog).toBeVisible()
  // Someone else renames the context and sets "never"; the matrix is read again behind the dialog.
  Object.assign(w.contexts.find(c => c.id === C.acme)!, { name: 'Acme renamed', new_accounts_override: 'deny', revision: 2 }); w.rules.revision++
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect(zone(page).locator(`[data-use-col="${C.acme}"] b`)).toHaveText('Acme renamed')
  await dialog.getByRole('button', { name: 'Archive' }).click()
  await expect.poll(() => lastWrite(w)).toEqual({ method: 'PATCH', path: `/api/work-contexts/${C.acme}`, body: { expected_revision: 5, name: 'Acme client work', new_accounts_override: null, archived: true } })
  await expect(page.locator('.toast').filter({ hasText: 'Someone changed the matrix meanwhile' })).toBeVisible()
  expect(w.contexts.find(c => c.id === C.acme)).toMatchObject({ name: 'Acme renamed', new_accounts_override: 'deny', archived_at: null })
})

test('For new things folds into one line; opened, New model versions is read-only with a link to Models (desktop and phone)', async ({ page }) => {
  const w = await setup(page, { rules: { ...useWorld().rules, new_models: 'shipped_only' } })
  for (const size of [{ width: 1440, height: 1000 }, { width: 390, height: 844 }]) {
    await page.setViewportSize(size)
    await open(page)
    // Folded: one quiet line that says what the three rules give.
    await expect(rulesToggle(page)).toHaveAttribute('aria-expanded', 'false')
    await expect(rulesToggle(page)).toContainText('accounts ask first · contexts start empty · projects go to Default')
    await expect(sw(page, 'New accounts')).toBeHidden()
    const head = zone(page).locator('#account-use-title')
    const top = () => head.evaluate(el => {
      let y = el.getBoundingClientRect().top + window.scrollY
      for (let parent = el.parentElement; parent; parent = parent.parentElement) y += parent.scrollTop
      return y
    })
    await rulesToggle(page).scrollIntoViewIfNeeded()
    const above = await top()
    await openRules(page)
    expect(Math.abs(await top() - above), 'the fold moved what is above it').toBeLessThanOrEqual(.5)
    await expect(zone(page).locator('[data-use-rule]')).toHaveCount(4)
    await expect(zone(page).locator('[data-use-say="new_accounts"]')).toHaveText('A new login runs nothing until you tick where it may work. For example, you sign in to Claude with another login on one of your computers, and it waits in the table below, unticked.', { useInnerText: true })
    // The model rule is shown as stored, never as a switch; Models is its editor.
    await expect(sw(page, 'New model versions')).toHaveCount(0)
    const models = zone(page).locator('[data-use-rule="new_models"]')
    await expect(models.locator('[data-use-models]')).toHaveText('Only with PAIMOS updates')
    await expect(models.getByRole('link', { name: 'set in Models' })).toHaveAttribute('href', '/settings/models#model-refresh')
    expect(w.writes).toEqual([])
  }
  w.rules.new_models = 'deny'
  await open(page); await openRules(page)
  await expect(zone(page).locator('[data-use-models]')).toHaveText('Ask first')
})

test('a rule switch saves only its own rule with the revision, never new_models; keyboard moves within it and Undo restores it', async ({ page }) => {
  const w = await setup(page)
  await open(page)
  await openRules(page)
  await expect(sw(page, 'New accounts').getByRole('radio')).toHaveText(['Automatic', 'Ask first'])
  await expect(sw(page, 'New accounts').getByRole('radio', { name: 'Ask first' })).toHaveAttribute('aria-checked', 'true')

  await sw(page, 'New accounts').getByRole('radio', { name: 'Automatic' }).click()
  await expect.poll(() => lastWrite(w)).toEqual({ method: 'PUT', path: '/api/account-use/rules', body: { expected_revision: 5, new_accounts: 'allow' } })
  await expect(sw(page, 'New accounts').getByRole('radio', { name: 'Automatic' })).toHaveAttribute('aria-checked', 'true')
  await expect(zone(page).locator('[data-use-say="new_accounts"]')).toContainText('A new login may work in every context at once.')
  await expect(page.locator('.toast').filter({ hasText: 'New accounts: Automatic. Saved and recorded in the audit log.' })).toBeVisible()

  // Keyboard: arrows move the choice inside the focused switch.
  await sw(page, 'New projects').getByRole('radio', { name: 'Automatic' }).focus()
  await page.keyboard.press('ArrowRight')
  await expect.poll(() => lastWrite(w)?.body).toEqual({ expected_revision: 6, new_projects: 'holding' })
  await expect(sw(page, 'New projects').getByRole('radio', { name: 'Ask first' })).toHaveAttribute('aria-checked', 'true')
  await expect(sw(page, 'New projects').getByRole('radio', { name: 'Ask first' })).toBeFocused()
  await expect(zone(page).locator('[data-use-say="new_projects"]')).toContainText('A new project waits in Unassigned')

  // Undo in the message puts the rule back, again naming only that rule.
  await toastUndo(page, 'New projects: Ask first').click()
  await expect.poll(() => lastWrite(w)?.body).toEqual({ expected_revision: 7, new_projects: 'default' })
  await expect(sw(page, 'New projects').getByRole('radio', { name: 'Automatic' })).toHaveAttribute('aria-checked', 'true')

  // A stale rule change is refused and the shown value stays the stored one.
  w.conflict = true
  await sw(page, 'New contexts').getByRole('radio', { name: 'Automatic' }).click()
  await expect(page.locator('.toast').filter({ hasText: 'Someone changed the matrix meanwhile' })).toBeVisible()
  await expect(sw(page, 'New contexts').getByRole('radio', { name: 'Ask first' })).toHaveAttribute('aria-checked', 'true')
  expect(w.rules.new_contexts).toBe('ask')
  const ruleWrites = w.writes.filter(x => x.path === '/api/account-use/rules')
  expect(ruleWrites).toHaveLength(4)
  for (const x of ruleWrites) expect(Object.keys(x.body), 'a rule save never carries new_models').not.toContain('new_models')
  expect(w.rules.new_models).toBe('allow')
})

test('grid keyboard: arrows move between boxes and Space toggles; U never fires inside a text field', async ({ page }) => {
  const w = await setup(page)
  await open(page)
  await cell(page, ACCOUNTS.main, C.def).focus()
  await page.keyboard.press('ArrowDown')
  await expect(cell(page, ACCOUNTS.spare, C.def)).toBeFocused()
  await page.keyboard.press('ArrowRight')
  await expect(cell(page, ACCOUNTS.spare, C.acme)).toBeFocused()
  await page.keyboard.press('ArrowLeft'); await page.keyboard.press('ArrowLeft')
  await expect(rowBox(page, ACCOUNTS.spare)).toBeFocused()
  await page.keyboard.press('ArrowRight'); await page.keyboard.press('ArrowRight')
  await page.keyboard.press('Space')
  await expect.poll(() => lastWrite(w)?.body).toEqual({ expected_revision: 5, changes: [{ account_id: ACCOUNTS.spare, context_id: C.acme, allowed: true }] })
  await expect(cell(page, ACCOUNTS.spare, C.acme)).toBeChecked()

  // Add a context from the keyboard: typing "u" in the name is text, ⌘/Ctrl+Enter saves.
  const writes = w.writes.length
  await zone(page).getByRole('button', { name: 'Add context' }).click()
  const name = page.getByLabel('Name of the new context')
  await expect(name).toBeFocused()
  await name.fill('Studio u')
  await name.press('u')
  await name.press(process.platform === 'darwin' ? 'Meta+Enter' : 'Control+Enter')
  await expect.poll(() => lastWrite(w)).toEqual({ method: 'POST', path: '/api/work-contexts', body: { expected_revision: 6, name: 'Studio uu' } })
  expect(w.writes.length, 'typing u in the name field must not undo').toBe(writes + 1)
  await expect(zone(page).locator('thead th[data-use-col] b')).toHaveText(['Default', 'Acme client work', 'Beta', 'Studio uu'])
})

test('context menu sets "never" for new accounts; confirmation and running work outside stay explicit', async ({ page }) => {
  const w = await setup(page, {
    rules: { ...useWorld().rules, confirmation_required: true }, truncated: true,
    running: [{ run_id: RUN, account_id: ACCOUNTS.main, node_id: null }],
  })
  await open(page)
  const confirm = zone(page).getByRole('region', { name: 'Confirm the matrix' })
  await expect(confirm).toContainText('Looks right? The matrix was carried over so every account kept working as before.')
  const guard = await controlStability(page, { rules: rulesToggle(page), corner: corner(page), firstCell: cell(page, ACCOUNTS.main, C.def) })
  await guard.check(() => confirm.getByRole('button', { name: 'Looks right' }).click())
  await expect(confirm).toContainText('Confirmed')
  await expect.poll(() => lastWrite(w)).toEqual({ method: 'POST', path: '/api/account-use/confirm', body: { expected_revision: 5 } })
  guard.done()

  const outside = zone(page).getByRole('region', { name: 'Running outside the matrix' })
  await expect(outside).toContainText('keeps running')
  await expect(outside.locator(`[data-use-outside="${RUN}"]`)).toContainText('Main')
  await expect(outside).toContainText('Only the first 200 runs are listed.')
  await expect(zone(page).getByText('Showing the first 200 accounts and contexts. The corner box, rows and columns still apply to all of them.')).toBeVisible()

  await zone(page).getByRole('button', { name: 'More for Acme client work' }).click()
  await page.getByRole('menuitemcheckbox', { name: 'New accounts: never' }).click()
  await expect.poll(() => lastWrite(w)).toEqual({ method: 'PATCH', path: `/api/work-contexts/${C.acme}`, body: { expected_revision: 6, name: 'Acme client work', new_accounts_override: 'deny', archived: false } })
  await expect(zone(page).locator(`[data-use-col="${C.acme}"] small`)).toHaveText('new accounts: never')
  await zone(page).getByRole('button', { name: 'More for Default' }).click()
  await expect(page.getByRole('menuitem', { name: 'Archive…' })).toHaveCount(0)
  await page.keyboard.press('Escape')
})

test('controls never move while cells, rows, the corner, rule switches and Undo are used (desktop and phone)', async ({ page }) => {
  await setup(page)
  for (const size of [{ width: 1440, height: 1000 }, { width: 390, height: 844 }]) {
    await page.setViewportSize(size)
    await open(page)
    await openRules(page)
    const controls = {
      rules: rulesToggle(page), corner: corner(page), add: zone(page).getByRole('button', { name: 'Add context' }),
      accounts: sw(page, 'New accounts'), contexts: sw(page, 'New contexts'), projects: sw(page, 'New projects'), models: zone(page).getByRole('link', { name: 'set in Models' }),
      row: rowBox(page, ACCOUNTS.spare), column: colBox(page, C.acme), cell: cell(page, ACCOUNTS.claude, C.beta), lastCell: cell(page, ACCOUNTS.claude, C.def),
    }
    for (const control of Object.values(controls)) await control.scrollIntoViewIfNeeded()
    const guard = await controlStability(page, controls)
    await guard.check(() => controls.cell.click())
    await guard.check(() => controls.row.click())
    await guard.check(async () => { await page.locator('body').press('u'); await expect(page.locator('.toast').filter({ hasText: 'Undone:' })).toBeVisible() })
    // Switching a rule moves nothing: its sentence changes in place, the matrix below stays where it was.
    await guard.check(() => controls.accounts.getByRole('radio', { name: 'Automatic' }).click())
    await guard.check(() => controls.projects.getByRole('radio', { name: 'Ask first' }).click())
    await guard.check(() => controls.contexts.getByRole('radio', { name: 'Automatic' }).click())
    await guard.check(() => controls.column.click())
    await guard.check(() => controls.corner.click())
    guard.done()
  }
})

test('project settings choose the context; an unmapped project says no account works there', async ({ page }) => {
  const errors = watchErrors(page)
  const w = await setup(page)
  await page.route('**/api/recurrences**', route => route.fulfill({ json: { items: [], next_cursor: null } }))
  await page.goto('/p/PRJ-17/settings')
  const card = page.locator('#project-work-context')
  await expect(card.getByText('This project has no context yet, so no account may work on it. Choose one.')).toBeVisible()
  const select = card.getByLabel('Context')
  await expect(select.locator('option:checked')).toHaveText('Not chosen · no account works here')
  await expect(select.locator('option')).toHaveText(['Not chosen · no account works here', 'Default (default)', 'Acme client work', 'Beta', 'Unassigned · no account works here'])
  const guard = await controlStability(page, { select })
  await guard.check(() => select.selectOption(C.acme))
  guard.done()
  await expect.poll(() => lastWrite(w)).toEqual({ method: 'PUT', path: '/api/projects/p-pharos/work-context', body: { expected_revision: 5, context_id: C.acme } })
  await expect(card.getByText('1 account may work here: Main.')).toBeVisible()
  // A competing change: the select returns to the stored context.
  w.conflict = true
  await select.selectOption(C.hold)
  await expect(page.locator('.toast').filter({ hasText: 'Someone changed the matrix meanwhile' })).toBeVisible()
  await expect(select).toHaveValue(C.acme)
  expect(errors).toEqual([])
})

test('project settings find a context past the first page and count its accounts on every page', async ({ page }) => {
  const errors = watchErrors(page)
  const id = (prefix: string, i: number) => `${prefix}-0000-4000-8000-${String(i).padStart(12, '0')}`
  const extra = Array.from({ length: 246 }, (_, i) => ({ id: id('e1000000', i), name: `Client ${String(i).padStart(3, '0')}`, kind: 'regular', new_accounts_override: null, archived_at: null, revision: 1 }))
  const many = Array.from({ length: 230 }, (_, i) => ({ id: id('c1000000', i), label: `Extra ${i}`, harness: 'codex', plan: null, billing_mode: 'api', owner_id: null }))
  const late = extra[240]!.id
  const base = useWorld()
  const w = await setup(page, {
    contexts: [...base.contexts, ...extra], accounts: [...base.accounts, ...many],
    cells: new Set([...base.cells, key(ACCOUNTS.main, late), key(many[225]!.id, late)]), project: { context_id: late },
  })
  await page.route('**/api/recurrences**', route => route.fulfill({ json: { items: [], next_cursor: null } }))
  await page.goto('/p/PRJ-17/settings')
  const card = page.locator('#project-work-context'), select = card.getByLabel('Context')
  // Context 245 of 250 sits on the second page; Extra 225 on the second account page.
  await expect(card.getByText('2 accounts may work here: Main, Extra 225.')).toBeVisible()
  await expect(select).toHaveValue(late)
  await expect(select.locator('option:checked')).toHaveText('Client 240')
  await expect(select.locator('option')).toHaveCount(250)
  await expect(card.getByText('Only the first')).toHaveCount(0)
  expect(w.writes).toEqual([])
  expect(errors).toEqual([])
})

test('the pairing review shows the rows the rule pre-fills', async ({ page }) => {
  await page.clock.install({ time: new Date('2026-09-27T20:00:00.000Z') })
  await mockWork(page, fixtures())
  await mockPairing(page)
  await grant(page)
  const w = useWorld({ rules: { ...useWorld().rules, new_accounts: 'allow' } })
  await mockUse(page, w)
  await page.goto('/agents/register-agent')
  await page.getByLabel('Pairing code').fill('123-456-789')
  await page.getByRole('button', { name: 'Look up code' }).click()
  const preview = page.getByRole('region', { name: 'Pairing review' }).locator('[data-pairing-use]')
  await expect(preview).toContainText('New accounts: allow automatically.')
  await expect(preview.locator('li')).toHaveText([/^Default\s*Allowed$/, /^Acme client work\s*Allowed$/, /^Beta\s*Never for new accounts$/])
  expect(w.writes).toEqual([])
})

test('screenshots of the matrix, light and dark', async ({ page }, testInfo) => {
  test.skip(!process.env.AEON1054_SHOTS, 'evidence run only')
  await setup(page, { rules: { ...useWorld().rules, confirmation_required: true }, running: [{ run_id: RUN, account_id: ACCOUNTS.main, node_id: null }] })
  for (const theme of ['light', 'dark'] as const) for (const width of [390, 1024, 1440]) {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await open(page)
    await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
    await zone(page).scrollIntoViewIfNeeded()
    await zone(page).screenshot({ path: testInfo.outputPath(`account-use-${width}-${theme}.png`) })
  }
})

// AEON-1173 (design AEON-1149, acc/check.mjs on the built page): the fold grows downward and moves nothing
// above it; a rule switch changes its sentence in place; a new account that asks first is tinted with a New
// tag and "Choose where" brings its row; projects in Unassigned are named with a way to choose their context;
// every control has a name, touch targets are at least 44 px and nothing scrolls sideways.
const WAITING = { count: 3, first: [{ project_id: 'f0000000-0000-4000-8000-000000000001', key: 'WEB-1', name: 'Website relaunch' }] }
for (const [width, touch] of [[1440, false], [1024, false], [390, true]] as const) for (const theme of ['light', 'dark'] as const) {
  test.describe(`where ${width} ${theme}`, () => {
    test.use({ viewport: { width, height: width > 600 ? 1000 : 860 }, hasTouch: touch, isMobile: touch, colorScheme: theme })
    test(`where ${width} ${theme}: the design's checks hold on the built page`, async ({ page }, info) => {
      const errors = watchErrors(page)
      const w = await setup(page, { holding: WAITING })
      w.accounts.find(a => a.id === ACCOUNTS.claude)!.awaiting = true
      await open(page)
      const z = zone(page), newRow = z.locator(`[data-use-row="${ACCOUNTS.claude}"]`)
      await expect(newRow).toHaveClass(/\bnew\b/)
      await expect(newRow.locator('[data-use-new]')).toHaveText('New · ask first')
      await expect(z.locator(`[data-use-row="${ACCOUNTS.main}"] [data-use-new]`)).toHaveCount(0)
      await expect(z.locator('[data-use-holding]')).toContainText('Waiting now: Website relaunch and 2 more · Choose their contexts')
      await expect(z.locator('[data-use-holding]').getByRole('link', { name: 'Choose their contexts' })).toHaveAttribute('href', '/p/WEB-1/settings#project-work-context')
      expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth), 'sideways scroll').toBeLessThanOrEqual(0)
      // Evidence for the visual review: the whole card, in a window tall enough to hold it (the width, and so the layout, stays).
      const shot = async (name: string) => {
        const size = page.viewportSize()!
        await page.setViewportSize({ width: size.width, height: 3000 })
        await z.screenshot({ path: info.outputPath(`${name}-${width}-${theme}.png`) })
        await page.setViewportSize(size)
      }
      await shot('where-folded')

      // The fold grows downward: nothing above it moves (measured in scroll-container coordinates).
      const top = (locator: ReturnType<Page['locator']>) => locator.evaluate(el => {
        let y = el.getBoundingClientRect().top + window.scrollY
        for (let parent = el.parentElement; parent; parent = parent.parentElement) y += parent.scrollTop
        return y
      })
      const above = [page.locator('#accounts-list-title'), z.locator('#account-use-title'), rulesToggle(page)]
      await rulesToggle(page).scrollIntoViewIfNeeded()
      const before = await Promise.all(above.map(top))
      await openRules(page)
      const after = await Promise.all(above.map(top))
      before.forEach((y, i) => expect(Math.abs(after[i]! - y), `fold moved ${i}`).toBeLessThanOrEqual(.5))
      expect((await z.locator('#use-rules').boundingBox())!.height, 'fold opened').toBeGreaterThan(100)
      await shot('where-open')

      // A rule switch changes its sentence in place; the switch and the matrix below stay put.
      const radio = sw(page, 'New accounts').getByRole('radio', { name: 'Automatic' }), first = cell(page, ACCOUNTS.main, C.def)
      await radio.scrollIntoViewIfNeeded()
      const guard = await controlStability(page, { radio, ask: sw(page, 'New accounts').getByRole('radio', { name: 'Ask first' }), first, add: z.getByRole('button', { name: 'Add context' }) })
      await guard.check(async () => { await radio.click(); await expect(z.locator('[data-use-say="new_accounts"]')).toContainText('A new login may work in every context at once.') })
      guard.done()
      expect(lastWrite(w)?.body).toEqual({ expected_revision: 5, new_accounts: 'allow' })

      const unnamed = await z.evaluate(root => [...root.querySelectorAll('button, input, a, [role="radio"]')]
        .filter(e => (e as HTMLElement).offsetParent !== null)
        .filter(e => !(e.getAttribute('aria-label') || e.getAttribute('aria-labelledby') || e.textContent?.trim() || e.closest('label') || (e as HTMLElement).title))
        .map(e => e.outerHTML.slice(0, 90)))
      expect(unnamed, 'every control has a name').toEqual([])
      if (touch) {
        const small = await z.evaluate(root => [...root.querySelectorAll('button, a, input')]
          .filter(e => (e as HTMLElement).offsetParent !== null)
          .map(e => { const box = ((e as HTMLInputElement).type === 'checkbox' ? e.closest('label') ?? e : e).getBoundingClientRect(); return { e: e.outerHTML.slice(0, 70), w: box.width, h: box.height } })
          .filter(r => r.w && r.h && (r.w < 43.5 || r.h < 43.5)))
        expect(small, 'touch targets of at least 44 px').toEqual([])
      }
      expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth), 'sideways scroll').toBeLessThanOrEqual(0)
      expect(errors).toEqual([])
    })
  })
}

test('"Choose where" from Needs you opens For new things and brings the new account\'s row, its first box focused (desktop and phone)', async ({ page }) => {
  const errors = watchErrors(page)
  const w = await setup(page)
  w.accounts.find(a => a.id === ACCOUNTS.claude)!.awaiting = true
  for (const size of [{ width: 1440, height: 1000 }, { width: 390, height: 844 }]) {
    await page.setViewportSize(size)
    await open(page)
    const choose = page.getByRole('button', { name: 'Choose where' })
    await expect(page.getByText('New account found: markus')).toBeVisible()
    await choose.click()
    await expect(rulesToggle(page)).toHaveAttribute('aria-expanded', 'true')
    const row = zone(page).locator(`[data-use-row="${ACCOUNTS.claude}"]`)
    await expect(row).toBeInViewport()
    await expect(row).toHaveClass(/\bflash\b/)
    await expect(cell(page, ACCOUNTS.claude, C.def)).toBeFocused()
    expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth), 'sideways scroll').toBeLessThanOrEqual(0)
  }
  // A tick is the person's choice: the tag goes and Needs you no longer names the account.
  await cell(page, ACCOUNTS.claude, C.def).click()
  await expect(zone(page).locator(`[data-use-row="${ACCOUNTS.claude}"] [data-use-new]`)).toHaveCount(0)
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect(page.getByText('New account found: markus')).toHaveCount(0)
  expect(errors).toEqual([])
})
