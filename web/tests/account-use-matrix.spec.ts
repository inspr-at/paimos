// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1054 (concept AEON-1044 §2.5, slice S4): Settings › Accounts › "Where
// accounts may work". Risk: the screen shows a tick, a switch value or an Undo
// the server did not store, or a control moves while a person works the matrix.
// The mock below keeps the server's contract: every write carries the matrix
// revision, a competing change answers 409 and saves nothing, bulk scopes cover
// every account or context, and Undo is an ordinary revision-checked save.
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
  accounts: { id: string; label: string; harness: string; plan: string | null; billing_mode: string; owner_id: string | null }[]
  contexts: { id: string; name: string; kind: string; new_accounts_override: string | null; archived_at: string | null; revision: number }[]
  cells: Set<string>
  running: { run_id: string; account_id: string; node_id: string | null }[]
  truncated: boolean
  /** The next write meets a competing save first: the server moves on and answers 409. */
  conflict: boolean
  writes: { method: string; path: string; body: Record<string, unknown> }[]
  project: { context_id: string } | null
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
    running: [], truncated: false, conflict: false, writes: [], project: null, ...over,
  }
}
const tickableIds = (w: UseWorld) => w.contexts.filter(c => c.kind !== 'holding' && !c.archived_at).map(c => c.id)
function matrixJSON(w: UseWorld) {
  return {
    rules: w.rules, accounts: w.accounts, contexts: w.contexts.filter(c => !c.archived_at),
    cells: [...w.cells].map(k => { const [account_id, context_id] = k.split(':'); return { account_id, context_id, allowed: true } }),
    next_account: null, next_context: w.truncated ? C.hold : null, running_outside: w.running, running_outside_truncated: w.truncated,
  }
}
async function mockUse(page: Page, w: UseWorld) {
  const conflictCheck = (expected: unknown) => {
    if (w.conflict) { w.conflict = false; w.rules.revision++; w.cells.delete(key(ACCOUNTS.main, C.acme)); return true }
    return expected !== w.rules.revision
  }
  await page.route('**/api/account-use**', async route => {
    const request = route.request(), url = new URL(request.url()), method = request.method()
    if (method === 'GET') return route.fulfill({ json: matrixJSON(w) })
    const body = request.postDataJSON() as Record<string, unknown>
    w.writes.push({ method, path: url.pathname, body })
    if (conflictCheck(body.expected_revision)) return route.fulfill({ status: 409, json: { error: 'account_use_revision_conflict' } })
    if (url.pathname === '/api/account-use/cells') {
      let changes = body.changes as Cell[] | undefined
      const bulk = body.bulk as { scope: string; account_id?: string; context_id?: string; allowed: boolean } | undefined
      if (bulk) changes = w.accounts.filter(a => !bulk.account_id || a.id === bulk.account_id).flatMap(a => tickableIds(w).filter(c => !bulk.context_id || c === bulk.context_id).map(c => ({ account_id: a.id, context_id: c, allowed: bulk.allowed })))
      const undo = changes!.map(c => ({ ...c, allowed: w.cells.has(key(c.account_id, c.context_id)) }))
      for (const c of changes!) { if (c.allowed) w.cells.add(key(c.account_id, c.context_id)); else w.cells.delete(key(c.account_id, c.context_id)) }
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
const sw = (page: Page, title: string) => zone(page).getByRole('radiogroup', { name: title })
async function open(page: Page) {
  await page.goto('/settings/accounts')
  await expect(zone(page).getByRole('table', { name: 'Where accounts may work' })).toBeVisible()
}
const indeterminate = (page: Page, account: string) => rowBox(page, account).evaluate(el => (el as HTMLInputElement).indeterminate)
const lastWrite = (w: UseWorld) => w.writes.at(-1)

test('bulk, tri-state rows and columns, single cells and Undo all save with the matrix revision', async ({ page }) => {
  const errors = watchErrors(page)
  const w = await setup(page)
  await open(page)
  // Columns: Default first, then by name; Unassigned never gets a column.
  await expect(zone(page).locator('thead th[data-use-col] b')).toHaveText(['Default', 'Acme client work', 'Beta'])
  await expect(zone(page).getByText('Unassigned holds projects waiting for a decision.')).toBeVisible()
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

  // Undo sends the inverse with the revision its own save returned.
  const undo = zone(page).getByRole('button', { name: /^Undo/ })
  await expect(undo).toBeEnabled()
  await undo.click()
  await expect.poll(() => lastWrite(w)?.body).toEqual({ expected_revision: 7, changes: [{ account_id: ACCOUNTS.claude, context_id: C.acme, allowed: false }] })
  await expect(cell(page, ACCOUNTS.claude, C.acme)).not.toBeChecked()
  await expect(undo).toBeDisabled()

  // A fully ticked column resolves to "no account" for that context.
  await colBox(page, C.def).click()
  await expect.poll(() => lastWrite(w)?.body).toEqual({ expected_revision: 8, bulk: { scope: 'context', context_id: C.def, allowed: true } })
  await expect(colBox(page, C.def)).toBeChecked()
  await colBox(page, C.def).click()
  await expect.poll(() => lastWrite(w)?.body).toEqual({ expected_revision: 9, bulk: { scope: 'context', context_id: C.def, allowed: false } })
  await expect(colBox(page, C.def)).not.toBeChecked()

  // Allow none, then Undo from the toast: every cell comes back as stored before.
  const before = [...w.cells].sort()
  await zone(page).getByRole('button', { name: 'Allow none' }).click()
  await expect.poll(() => lastWrite(w)?.body).toEqual({ expected_revision: 10, bulk: { scope: 'all', allowed: false } })
  await expect(zone(page).locator('[data-use-cell]:checked')).toHaveCount(0)
  await page.locator('.toast').filter({ hasText: 'Allow none' }).getByRole('button', { name: 'Undo' }).click()
  await expect.poll(() => [...w.cells].sort()).toEqual(before)
  await expect.poll(() => lastWrite(w)?.body.expected_revision).toBe(11)
  await expect(zone(page).locator('[data-use-cell]:checked')).toHaveCount(before.length)

  // Allow all, then U (outside a text field) undoes it.
  await zone(page).getByRole('button', { name: 'Allow all' }).click()
  await expect(zone(page).locator('[data-use-cell]:checked')).toHaveCount(9)
  await page.locator('body').press('u')
  await expect.poll(() => [...w.cells].sort()).toEqual(before)
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
  await expect(zone(page).getByRole('button', { name: /^Undo/ })).toBeDisabled()
  expect(w.cells.has(key(ACCOUNTS.claude, C.beta))).toBe(false)
  // The next save uses the revision the server now has.
  await cell(page, ACCOUNTS.claude, C.beta).click()
  await expect.poll(() => lastWrite(w)?.body.expected_revision).toBe(6)
  await expect(cell(page, ACCOUNTS.claude, C.beta)).toBeChecked()
})

test('switch changes save all four persisted rules with the revision; keyboard moves within a switch', async ({ page }) => {
  const w = await setup(page, { rules: { ...useWorld().rules, new_models: 'shipped_only' } })
  await open(page)
  await expect(sw(page, 'New accounts').getByRole('radio', { name: 'Ask first' })).toHaveAttribute('aria-checked', 'true')
  // A migrated workspace shows its third model value; it is not a fresh choice elsewhere.
  await expect(sw(page, 'New model versions').getByRole('radio')).toHaveText(['Allow automatically', 'Ask first', 'Only with PAIMOS updates'])
  await expect(sw(page, 'New model versions').getByRole('radio', { name: 'Only with PAIMOS updates' })).toHaveAttribute('aria-checked', 'true')
  await expect(sw(page, 'New accounts').getByRole('radio')).toHaveCount(2)

  await sw(page, 'New accounts').getByRole('radio', { name: 'Allow automatically' }).click()
  await expect.poll(() => lastWrite(w)).toEqual({ method: 'PUT', path: '/api/account-use/rules', body: { expected_revision: 5, new_accounts: 'allow', new_contexts: 'ask', new_projects: 'default', new_models: 'shipped_only' } })
  await expect(sw(page, 'New accounts').getByRole('radio', { name: 'Allow automatically' })).toHaveAttribute('aria-checked', 'true')
  await expect(page.locator('.toast').filter({ hasText: 'New accounts: Allow automatically. Saved and recorded in the audit log.' })).toBeVisible()

  // Keyboard: arrows move the choice inside the focused switch.
  await sw(page, 'New projects').getByRole('radio', { name: 'Follow automatically' }).focus()
  await page.keyboard.press('ArrowRight')
  await expect.poll(() => lastWrite(w)?.body).toEqual({ expected_revision: 6, new_accounts: 'allow', new_contexts: 'ask', new_projects: 'holding', new_models: 'shipped_only' })
  await expect(sw(page, 'New projects').getByRole('radio', { name: 'Ask first' })).toHaveAttribute('aria-checked', 'true')
  await expect(sw(page, 'New projects').getByRole('radio', { name: 'Ask first' })).toBeFocused()

  // A stale switch change is refused and the shown value stays the stored one.
  w.conflict = true
  await sw(page, 'New contexts').getByRole('radio', { name: 'Allow automatically' }).click()
  await expect(page.locator('.toast').filter({ hasText: 'Someone changed the matrix meanwhile' })).toBeVisible()
  await expect(sw(page, 'New contexts').getByRole('radio', { name: 'Ask first' })).toHaveAttribute('aria-checked', 'true')
  expect(w.rules.new_contexts).toBe('ask')
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
  const guard = await controlStability(page, { newAccounts: sw(page, 'New accounts'), allowAll: zone(page).getByRole('button', { name: 'Allow all' }), firstCell: cell(page, ACCOUNTS.main, C.def) })
  await guard.check(() => confirm.getByRole('button', { name: 'Looks right' }).click())
  await expect(confirm).toContainText('Confirmed')
  await expect.poll(() => lastWrite(w)).toEqual({ method: 'POST', path: '/api/account-use/confirm', body: { expected_revision: 5 } })
  guard.done()

  const outside = zone(page).getByRole('region', { name: 'Running outside the matrix' })
  await expect(outside).toContainText('keeps running')
  await expect(outside.locator(`[data-use-outside="${RUN}"]`)).toContainText('Main')
  await expect(outside).toContainText('Only the first 200 runs are listed.')
  await expect(zone(page).getByText('Showing the first 200 accounts and contexts.')).toBeVisible()

  await zone(page).getByRole('button', { name: 'More for Acme client work' }).click()
  await page.getByRole('menuitemcheckbox', { name: 'New accounts: never' }).click()
  await expect.poll(() => lastWrite(w)).toEqual({ method: 'PATCH', path: `/api/work-contexts/${C.acme}`, body: { expected_revision: 6, name: 'Acme client work', new_accounts_override: 'deny', archived: false } })
  await expect(zone(page).locator(`[data-use-col="${C.acme}"] small`)).toHaveText('New accounts: never')
  await zone(page).getByRole('button', { name: 'More for Default' }).click()
  await expect(page.getByRole('menuitem', { name: 'Archive…' })).toHaveCount(0)
  await page.keyboard.press('Escape')
})

test('controls never move while cells, rows, switches and Undo are used (desktop and phone)', async ({ page }) => {
  await setup(page)
  for (const size of [{ width: 1440, height: 1000 }, { width: 390, height: 844 }]) {
    await page.setViewportSize(size)
    await open(page)
    const controls = {
      allowAll: zone(page).getByRole('button', { name: 'Allow all' }), allowNone: zone(page).getByRole('button', { name: 'Allow none' }),
      undo: zone(page).getByRole('button', { name: /^Undo/ }), add: zone(page).getByRole('button', { name: 'Add context' }),
      accounts: sw(page, 'New accounts'), contexts: sw(page, 'New contexts'), projects: sw(page, 'New projects'), models: sw(page, 'New model versions'),
      row: rowBox(page, ACCOUNTS.spare), column: colBox(page, C.acme), cell: cell(page, ACCOUNTS.claude, C.beta),
    }
    for (const control of Object.values(controls)) await control.scrollIntoViewIfNeeded()
    const guard = await controlStability(page, controls)
    await guard.check(() => controls.cell.click())
    await guard.check(() => controls.row.click())
    await guard.check(() => controls.undo.click())
    await guard.check(() => controls.accounts.getByRole('radio', { name: 'Allow automatically' }).click())
    await guard.check(() => controls.models.getByRole('radio', { name: 'Ask first' }).click())
    await guard.check(() => controls.column.click())
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
