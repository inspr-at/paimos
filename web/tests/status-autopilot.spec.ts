// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
import { fixtures, mockWork } from './work-fixtures'
import { businessData, mockBusiness } from './business-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { expectStableControls } from './helpers/stable'
import type { AutopilotSettings, AutopilotProposal, AutomaticChange, ProjectOverride } from '../src/lib/statusAutopilot'

async function setup(page: Page, role: 'admin' | 'member' = 'admin') {
  await mockWork(page, fixtures(), { admin: role === 'admin' })
  await mockBusiness(page, businessData({ role }), { role })
  await mockSettings(page, settingsData())
  // The business fixture freezes Date.now(). TicketWorkspace's capture
  // listener and Vue's target listener need an advancing event clock.
  await page.clock.setSystemTime(new Date('2026-10-01T18:00:00Z'))
  await page.route('**/api/settings/model-provider', route => route.fulfill({ json: { enabled: false, base_url: '', chat_model: '', embedding_model: '', features: { crm_note_rewrite: false, embeddings: false }, provider_id: '', revision: 0, has_api_key: false } }))
  const settings: AutopilotSettings = { enabled: true, revision: 0, rules: { new: { enabled: true, days: 7 }, backlog: { enabled: true, days: 90 }, blocked: { enabled: true, days: 14 }, progress: { enabled: true, days: 3 }, done: { enabled: true, days: 14 }, publish: { enabled: true }, accept: { enabled: true, days: 30 } } }
  const changes: AutomaticChange[] = [{ event_id: 41, node_id: 'ticket-1', key: 'ORB-142', title: 'Touch ID sign-in for the desktop app', actor: 'Status autopilot', rule: 'progress', reason: 'No session, branch or PR activity for 3 days.', from: 'in_progress', to: 'open', at: '2026-10-01T17:00:00Z', undone: false, undoable: true }]
  const suggestions: AutomaticChange[] = ['triage_list', 'cancel_suggested', 'blocked_reminder', 'missed_release'].map((flag, i) => ({ ...changes[0]!, event_id: 100 + i, key: `ORB-${i + 1}`, rule: (['new', 'backlog', 'blocked', 'done'] as const)[i]!, from: ['new', 'backlog', 'blocked', 'done'][i]!, to: flag, reason: `Current ${flag}`, at: '2026-01-01T00:00:00Z', undoable: false, changed_since: true }))
  const overrides: Record<string, ProjectOverride> = { 'p-own': { mode: 'off', effective_enabled: false, revision: 2 } }
  const projectWrites: { id: string; mode: string; expected_revision: number }[] = []
  const projectRows = [ { id: 'p-own', key: 'OWN', title: 'Die langfristige Koordination aller Arbeitsbereiche und Freigabeverfahren' }, { id: 'p-follow', key: 'FOLLOW', title: 'Die langfristige Koordination aller Arbeitsbereiche und Freigabeverfahren' }, { id: 'p-search', key: 'SEARCH', title: 'Hidden beyond the first picker page' } ]
  const proposals: AutopilotProposal[] = []
  const resolutions: unknown[] = []
  const writes: unknown[] = []; let conflict = false; let undo = 0
  await page.route('**/api/settings/status-autopilot', async route => {
    if (route.request().method() === 'PUT') {
      const body = route.request().postDataJSON(); writes.push(body)
      if (conflict) return route.fulfill({ status: 409, json: { error: 'settings changed' } })
      Object.assign(settings, { enabled: body.enabled, rules: body.rules, revision: settings.revision + 1 })
      if (body.confirm_upgrade) Object.assign(settings, { suggest_until: null, effective_mode: settings.server_mode === 'suggest' ? 'suggest' : 'on' })
    }
    return route.fulfill({ json: settings })
  })
  await page.route('**/api/projects/*/status-autopilot', async route => {
    const id = route.request().url().split('/').at(-2)!
    const o = overrides[id] ??= { mode: 'inherit', effective_enabled: true, revision: 0 }
    if (route.request().method() === 'PUT') { const body = route.request().postDataJSON(); projectWrites.push({ id, ...body }); if (body.expected_revision !== o.revision) return route.fulfill({ status: 409, json: { error: 'changed' } }); o.mode = body.mode; o.revision++ }
    o.effective_enabled = o.mode === 'on' || o.mode === 'inherit' && settings.enabled
    return route.fulfill({ json: o })
  })
  await page.route('**/api/status-autopilot/projects*', route => {
    const q = new URL(route.request().url()).searchParams, term = q.get('q')?.toLowerCase() ?? ''
    const rows = projectRows.map(p => ({ ...p, override: overrides[p.id] ?? { mode: 'inherit', effective_enabled: settings.enabled, revision: 0 } })).filter(p => (q.get('mode') === 'inherit' ? p.override.mode === 'inherit' : p.override.mode !== 'inherit') && `${p.key} ${p.title}`.toLowerCase().includes(term))
    return route.fulfill({ json: { items: q.get('mode') === 'inherit' && !term ? rows.slice(0, 1) : rows, inherited_count: projectRows.filter(p => !overrides[p.id] || overrides[p.id]!.mode === 'inherit').length, next_cursor: q.get('mode') === 'inherit' && !term && rows.length > 1 ? 'next' : null } })
  })
  await page.route('**/api/status-autopilot/attention*', route => {
    const counts = { proposed: proposals.length, triage: suggestions.filter(i => i.to === 'triage_list').length, cancel: suggestions.filter(i => i.to === 'cancel_suggested').length, blocked: suggestions.filter(i => i.to === 'blocked_reminder').length, missed: suggestions.filter(i => i.to === 'missed_release').length }
    return route.fulfill({ json: { counts, total: Object.values(counts).reduce((a,b) => a+b,0), items: [], next_cursor: null, facets: { projects: [], assignees: [] } } })
  })
  await page.route('**/api/status-autopilot/changes*', route => route.fulfill({ json: { items: route.request().url().includes('suggestions=true') ? suggestions : changes } }))
  await page.route('**/api/status-autopilot/proposals', route => route.fulfill({ json: { items: proposals } }))
  await page.route('**/api/status-autopilot/proposals/*', route => {
    resolutions.push(route.request().postDataJSON())
    const id = Number(route.request().url().split('/').at(-1))
    const index = proposals.findIndex(proposal => proposal.event_id === id)
    if (index >= 0) proposals.splice(index, 1)
    return route.fulfill({ json: {} })
  })
  await page.route('**/api/events/41/undo', route => { undo++; changes[0]!.undone = true; changes[0]!.undoable = false; return route.fulfill({ status: 201, json: { id: 42 } }) })
  return { settings, suggestions, proposals, resolutions, writes, changes, projectWrites, overrides, conflict: () => { conflict = true }, undos: () => undo }
}

test('large queues become five exact counts and filtered links, with only five latest changes', async ({ page }, testInfo) => {
  const world = await setup(page)
  const base = world.suggestions[0]!
  world.suggestions.splice(0, world.suggestions.length, ...Array.from({ length: 250 }, (_, i) => ({ ...base, event_id: 1000 + i, key: `AEON-${1001+i}` })))
  world.changes.push(...Array.from({ length: 49 }, (_, i) => ({ ...world.changes[0]!, event_id: i+200, key: `ORB-${i+200}` })))
  await page.goto('/settings/autopilot')
  const needs = page.getByRole('region', { name: '250 tickets need a person', exact: true })
  await expect(needs.locator('.att-row')).toHaveCount(5)
  await expect(needs.locator('.att-row.calm')).toHaveCount(4)
  await expect(needs.getByText('250 tickets on the triage list')).toBeVisible()
  await expect(needs.locator('a.ticket-link')).toHaveCount(0)
  const latest = page.getByRole('region', { name: 'Latest automatic changes' })
  await expect(latest.locator('.change')).toHaveCount(5)
  await expect(latest.getByRole('link', { name: 'Show all' })).toHaveAttribute('href', '/activity?view=automatic')
  await needs.getByRole('button', { name: 'Open', exact: true }).click()
  await expect(page).toHaveURL('/tickets?kind=triage')
  await page.goto('/settings/autopilot'); await page.getByRole('button', { name: 'Open all', exact: true }).click()
  await expect(page).toHaveURL('/tickets')
  const folder = process.env.STATUS_AUTOPILOT_SHOTS ?? testInfo.outputPath('aeon-696'); await mkdir(folder, { recursive: true })
  for (const width of [390, 1024, 1440]) {
    await page.setViewportSize({ width, height: 900 }); await page.goto('/settings/autopilot')
    await expect(latest.locator('.change')).toHaveCount(5)
    if (width === 390) expect(await latest.getByRole('button', { name: /^Undo:/ }).first().evaluate(button => button.getBoundingClientRect().height)).toBeGreaterThanOrEqual(44)
    for (const theme of ['light','dark']) {
      await page.evaluate(t => { document.documentElement.dataset.theme = t }, theme)
      expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(1)
      const path = `${folder}/autopilot-${width}-${theme}.png`
      await page.screenshot({ path, fullPage: true }); await testInfo.attach(`autopilot-${width}-${theme}`, { path, contentType: 'image/png' })
      await latest.scrollIntoViewIfNeeded()
      await expect(latest).toBeInViewport({ ratio: .25 })
      await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))))
      const lower = `${folder}/overrides-latest-${width}-${theme}.png`
      await page.screenshot({ path: lower }); await testInfo.attach(`overrides-latest-${width}-${theme}`, { path: lower, contentType: 'image/png' })
      await page.getByRole('heading', { name: '250 tickets need a person' }).scrollIntoViewIfNeeded()
    }
  }
})

test('project picker searches inheriting projects, adds the opposite value and restores a removed override with Undo', async ({ page }) => {
  const world = await setup(page)
  await page.goto('/settings/autopilot')
  const card = page.getByRole('region', { name: 'Project overrides' })
  const add = card.getByRole('button', { name: 'Add an override' })
  await expect(card.getByRole('radiogroup')).toHaveCount(1)
  await add.click()
  const search = page.getByRole('searchbox', { name: 'Search choose a project' })
  await expect(search).toBeVisible()
  const pick = page.getByRole('option', { name: /Hidden beyond/ })
  await expect(page.getByRole('option').first()).toBeVisible()
  await expectStableControls({ controls: { search, add, selector: page.getByRole('listbox', { name: 'Choose a project' }), row: page.getByRole('option').first() }, interactions: [{ name: 'search another project', run: async () => { await search.fill('SEARCH'); await expect(pick).toBeVisible() } }] })
  await pick.click()
  const group = card.getByRole('radiogroup', { name: 'Status autopilot in Hidden beyond the first picker page' })
  await expect(group.getByRole('radio', { name: 'Off', exact: true })).toHaveAttribute('aria-checked','true')
  await expect(group.getByRole('radio', { name: 'Off', exact: true })).toBeFocused()
  expect(world.projectWrites[0]).toMatchObject({ id: 'p-search', mode: 'off', expected_revision: 0 })
  await card.getByRole('button', { name: 'Remove the override: Hidden beyond the first picker page follows the workspace again' }).click()
  await expect(group).toHaveCount(0)
  await page.locator('.toast').filter({ hasText: 'Hidden beyond the first picker page follows' }).getByRole('button', { name: 'Undo', exact: true }).click()
  await expect(group).toBeVisible()
  expect(world.projectWrites.at(-1)).toMatchObject({ id: 'p-search', mode: 'off', expected_revision: 2 })
  await add.click(); await search.press('Escape'); await expect(search).not.toBeFocused()
  await page.keyboard.press('Escape'); await expect(search).toHaveCount(0); await expect(add).toBeFocused()
})

test('owner explicitly confirms upgrade while server Suggest remains the cap', async ({ page }) => {
  const world = await setup(page)
  await page.route('**/api/me/permissions*', route => route.fulfill({ json: { workspace: { role: { id: 'owner', key: 'owner', name: 'Owner' }, permissions: ['settings.manage', 'ownership.transfer', 'nodes.read', 'nodes.write'] }, project: null } }))
  Object.assign(world.settings, { server_mode: 'on', effective_mode: 'suggest', suggest_until: '2026-10-02T18:00:00Z' })
  await page.goto('/settings/autopilot')
  const button = page.getByRole('button', { name: 'Enable automatic changes', exact: true })
  await button.click()
  await expect(button).toHaveCount(0)
  expect(world.writes).toHaveLength(1)
  expect(world.writes[0]).toMatchObject({ confirm_upgrade: true, expected_revision: 0 })
  Object.assign(world.settings, { server_mode: 'suggest', effective_mode: 'suggest', suggest_until: '2026-10-02T18:00:00Z' })
  await page.reload()
  await expect(button).toBeDisabled()
  await expect(page.getByText('The server operator requires Suggest mode. Proposed changes wait for Apply or Dismiss.')).toBeVisible()
})

test('rules validate and save, master off disables all seven, and latest Undo stays audited', async ({ page }) => {
  const world = await setup(page)
  await page.goto('/settings/autopilot')
  const card = page.getByRole('region', { name: 'Status autopilot', exact: true })
  await expect(card.getByRole('switch', { name: 'Status autopilot', exact: true })).toBeChecked()
  expect(await card.locator('input[type=number]').evaluateAll(inputs => inputs.map(input => (input as HTMLInputElement).value))).toEqual(['7', '90', '14', '3', '14', '30'])
  await expect(card.getByText('On publish', { exact: true })).toBeVisible()
  const accept = card.getByLabel('Days delivered without objection before Accepted')
  await accept.fill('366'); await accept.blur()
  await expect(accept).toHaveAttribute('aria-invalid', 'true'); expect(world.writes).toHaveLength(0)
  await accept.fill('45'); await accept.blur()
  await expect(card.getByText(/Accepted 45 days later/)).toBeVisible()
  await card.getByRole('switch', { name: 'Delivered to Accepted' }).uncheck()
  await expect(accept).toBeDisabled()
  await card.getByRole('switch', { name: 'Delivered to Accepted' }).check()
  await expect(accept).toBeEnabled()
  const master = card.getByRole('switch', { name: 'Status autopilot', exact: true })
  for (const width of [390, 1024, 1440]) {
    await page.setViewportSize({ width, height: 900 })
    const add = page.getByRole('button', { name: 'Add an override' })
    const all = page.getByRole('link', { name: 'Show all' })
    await expectStableControls({ controls: { master: master.locator('..'), accept, rules: card.locator('.rules'), row: card.locator('.rule').last(), add, all }, scrollAreas: { card }, interactions: [{ name: 'master off', run: async () => { await master.uncheck(); await expect(accept).toBeDisabled() } }, { name: 'master on', run: async () => { await master.check(); await expect(accept).toBeEnabled() } }] })
  }
  await master.uncheck()
  await expect(accept).toBeDisabled()
  await expect(card.getByText('Off: nothing moves on its own. Suggestions are still listed.')).toBeVisible()
  const recent = page.getByRole('region', { name: 'Latest automatic changes' })
  await expect(recent.getByText('Status autopilot', { exact: true })).toBeVisible()
  await expect(recent.getByText('No session, branch or PR activity for 3 days.')).toBeVisible()
  await recent.getByRole('button', { name: /Undo: put ORB-142 back/ }).click()
  await expect(recent.getByText('Undone · back to In progress')).toBeVisible(); expect(world.undos()).toBe(1)
})

test('stale setting writes explain recovery and keep the saved limits', async ({ page }) => {
  const world = await setup(page); await page.goto('/settings/autopilot'); world.conflict()
  const card = page.getByRole('region', { name: 'Status autopilot', exact: true })
  await card.getByLabel('Days delivered without objection before Accepted').fill('60')
  await card.getByLabel('Days delivered without objection before Accepted').blur()
  await expect(page.locator('.toast.error')).toContainText('Another admin changed these settings')
  await page.locator('.autopilot').getByRole('button', { name: 'Reload', exact: true }).click()
  await expect(card.getByLabel('Days delivered without objection before Accepted')).toHaveValue('30')
})

test('members have no workspace autopilot controls', async ({ page }) => {
  await setup(page, 'member'); await page.goto('/settings/autopilot')
  await expect(page.getByRole('region', { name: 'Status autopilot', exact: true })).toHaveCount(0)
})

test('ticket Activity shows the autopilot reason, automatic filter and guarded Undo', async ({ page }) => {
  await setup(page)
  const change: AutomaticChange = { event_id: 41, node_id: 'n-1', key: 'PHR-12', title: 'Ticket', actor: 'Status autopilot', rule: 'progress', reason: 'No session, branch or PR activity for 3 days.', from: 'in_progress', to: 'open', at: '2026-10-01T17:00:00Z', undone: false, undoable: true }
  let undoCalls = 0
  await page.route('**/api/events/41/undo', route => { undoCalls++; change.undone = true; change.undoable = false; return route.fulfill({ status: 201, json: { id: 42 } }) })
  await page.route('**/api/nodes/n-1/activity*', route => route.fulfill({ json: { items: [{ id: '41', type: 'change', at: change.at, author: { id: 'system', name: 'System', automatic: true, job: 'status-autopilot', reason: change.reason }, changes: [{ field: 'status', from: 'in_progress', to: 'open' }], automatic_change: change }], next_cursor: null } }))
  await page.goto('/p/PHAROS/PHAROS-11')
  const activity = page.getByRole('region', { name: 'Activity', exact: true })
  await expect(activity.getByText('Status autopilot', { exact: true })).toBeVisible()
  await expect(activity.getByText(change.reason)).toBeVisible()
  await activity.getByRole('radio', { name: 'Automatic', exact: true }).click()
  await expect(activity.getByRole('radio', { name: 'Automatic', exact: true })).toBeChecked()
  await expect(activity.getByText(change.reason)).toBeVisible()
  await activity.getByRole('button', { name: /Undo: put PHR-12 back/ }).click()
  await expect.poll(() => undoCalls).toBe(1)
  await expect(activity.getByText('Undone · back to In progress')).toBeVisible()
})

test('a save finishing after session expiry cannot update controls or offer Undo', async ({ page }) => {
  const world = await setup(page)
  await page.goto('/settings/autopilot')
  const master = page.getByRole('switch', { name: 'Status autopilot', exact: true })
  await expect(master).toBeChecked()
  const identity = await page.evaluate(async () => (await import('/src/stores/session.ts')).useSession().identity)
  const accept = page.getByLabel('Days delivered without objection before Accepted')
  await accept.fill('366')
  let release!: () => void, received!: () => void, finished!: () => void
  const completed = new Promise<void>(resolve => { finished = resolve })
  const held = new Promise<void>(resolve => { release = resolve }), started = new Promise<void>(resolve => { received = resolve })
  await page.route('**/api/settings/status-autopilot', async route => {
    if (route.request().method() !== 'PUT') return route.fallback()
    received(); await held
    await route.fulfill({ json: { ...world.settings, enabled: false, revision: 1 } }).catch(() => {}); finished()
  })
  await master.uncheck(); await started
  await page.evaluate(async () => {
    const [{ useSession }, { revokePermissions }] = await Promise.all([import('/src/stores/session.ts'), import('/src/lib/authz.ts')])
    useSession().identity = null; revokePermissions()
  })
  release(); await completed
  await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => resolve())))
  await expect(master).toBeChecked(); await expect(master).toBeDisabled()
  await expect(page.locator('.toast').filter({ hasText: 'Status autopilot saved.' })).toHaveCount(0)
  world.settings.enabled = false; world.settings.rules.accept.days = 60
  await page.evaluate(async who => {
    const [{ useSession }, { clearPermissions, refreshPermissions }] = await Promise.all([import('/src/stores/session.ts'), import('/src/lib/authz.ts')])
    clearPermissions()
    useSession().identity = { ...who!, principal: { ...who!.principal, id: 'another-person' } }
    await refreshPermissions()
  }, identity)
  await expect(master).toBeEnabled(); await expect(master).not.toBeChecked()
  await expect(accept).toHaveValue('60'); await expect(accept).toHaveAttribute('aria-invalid', 'false')
})
