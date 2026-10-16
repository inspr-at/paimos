// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { resolve } from 'node:path'
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents, sessionListReads } from './agents-fixtures'
import { controlStability } from './control-stability'
import type { SessionChangeRequest } from '../src/lib/agents'

const now = Date.parse('2026-09-29T06:00:00Z')
const accountID = 'd0000000-0000-4000-8000-000000000001'
const profileID = 'd0000000-0000-4000-8000-000000000002'
async function setup(page: Page, theme: 'light' | 'dark' = 'light', admin = true) {
  await page.clock.install({ time: now })
  const work = fixtures(); work.preferences.theme = { choice: theme }
  await mockWork(page, work, { admin, readOnly: !admin })
  const data = agentData({ now, me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' }, nodes: {} })
  const worker = data.sessions[1]!
  Object.assign(worker, { management_mode: 'unmanaged', display_label: 'Release checks', model: 'Current model', reasoning_effort: 'medium', account_label: 'Codex Pro', run_id: null, parent_harness_session_id: null })
  data.sessions.splice(0, data.sessions.length, worker); data.runs.splice(0); data.approvals.splice(0); data.messages.splice(0)
  const calls = await mockAgents(page, data)
  const controls: SessionChangeRequest[] = []
  const submissions: Record<string, unknown>[] = []
  const path = `**/api/projects/${worker.project_id}/harness-sessions/${worker.id}`
  await page.route(path, route => route.fulfill({ json: { ...worker, controls } }))
  await page.route(`${path}/requests`, route => {
    const body = route.request().postDataJSON(); submissions.push(body)
    const request: SessionChangeRequest = {
      id: body.request_id, session_id: worker.id, expected_generation: body.expected_generation, kind: body.kind,
      state: 'pending', sequence: controls.length + 1, outcome: null, reason: null, expires_at: new Date(now + 600_000).toISOString(),
      request_payload: body.kind === 'rename_request' ? { display_label: body.display_label } : { model: 'Catalog model', reasoning_effort: 'high', account_id: body.account_id, model_profile_id: body.model_profile_id },
    }
    controls.push(request)
    return route.fulfill({ status: 201, json: request })
  })
  await page.route('**/api/agent-accounts/catalog?*', route => route.fulfill({ json: {
    as_of: new Date(now).toISOString(), role: 'build', hosts: [{ daemon_id: 'workstation', label: 'Workstation', harnesses: [{ harness: worker.harness, default_account_id: accountID, accounts: [{
      id: accountID, label: 'Codex Pro', plan: '', registered_by_principal_id: worker.agent_principal_id, state: 'available', available: true,
      last_probe_at: null, last_probe_ok: null, unavailable_reasons: [], remaining_fraction: null, windows: [], default_model_profile_id: profileID,
      models: [{ model: 'Catalog model', family: 'openai', efforts: [{ effort: 'high', model_profile_id: profileID, version: 'v1' }] }],
    }] }] }],
  } }))
  return { worker, controls, submissions, calls }
}

// AEON-1438: requests open from the panel's ⋯ menu as a small form; Details
// lists them with their outcome. The chat composer carries no second composer.
const panelOf = (page: Page) => page.getByRole('complementary', { name: 'Session details' })
async function openRequest(page: Page, item: 'Rename session…' | 'Change model or effort…') {
  await panelOf(page).getByRole('button', { name: 'More session actions', exact: true }).click()
  await page.getByRole('menuitem', { name: new RegExp(`^${item}`) }).click()
  return page.getByRole('dialog', { name: item.replace('…', '') })
}

for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) {
  test(`${theme} ${width}: request stays pending until session completion`, async ({ page }) => {
    const { worker, controls, submissions } = await setup(page, theme)
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.goto(`/agents/${worker.id}?tab=messages`)
    const panel = panelOf(page)
    await expect(panel.getByRole('textbox', { name: 'Message to Release checks' })).toBeVisible()
    await expect(panel.getByText('Ask this session to…')).toHaveCount(0)
    const rename = await openRequest(page, 'Rename session…')
    const name = rename.getByRole('textbox', { name: 'New session name' })
    await expect(name).toHaveValue('Release checks')
    await expect(rename.getByRole('button', { name: 'Send request' })).toBeDisabled()
    await name.fill('Ready for release')
    await rename.getByRole('button', { name: 'Send request' }).click()
    await expect(rename).toHaveCount(0)
    await expect(panel.getByRole('button', { name: 'More session actions', exact: true })).toBeFocused()
    await panel.getByRole('tab', { name: 'Details', exact: true }).click()
    const requests = panel.getByRole('region', { name: 'Session requests' })
    await expect(requests).toContainText('Rename to Ready for release')
    await expect(requests).toContainText('Requested · waiting for the session')
    // The pending rename must not replace the current name. AEON-1212 keeps that
    // name in the identity span; the heading's accessible name also includes status.
    await expect(panel.locator('h2.pane-title .name')).toHaveText('Release checks')
    await expect(panel.locator('h2.pane-title')).not.toContainText('Ready for release')
    expect(submissions[0]?.expected_generation).toBe(worker.id)
    expect(submissions[0]?.kind).toBe('rename_request')
    const model = await openRequest(page, 'Change model or effort…')
    await expect(model.getByRole('combobox', { name: 'Account catalog' })).toHaveValue(accountID)
    await model.getByRole('combobox', { name: 'Model', exact: true }).selectOption('Catalog model')
    await model.getByRole('combobox', { name: 'Effort', exact: true }).selectOption(profileID)
    await model.getByRole('button', { name: 'Send request' }).click()
    await expect(model).toHaveCount(0)
    await expect(requests.getByText('Requested · waiting for the session', { exact: true })).toHaveCount(2)
    expect(submissions[1]?.account_id).toBe(accountID)
    expect(submissions[1]?.model_profile_id).toBe(profileID)
    // AEON-1240 / #612 formats display names; a pending request must not replace the current model.
    await expect(panel.locator('.fact').filter({ has: page.locator('dt', { hasText: /^Model$/ }) })).toContainText('Current Model · medium')
    await requests.scrollIntoViewIfNeeded()
    expect(await panel.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    const dir = process.env.SESSION_REQUEST_SHOTS
    if (dir) { mkdirSync(dir, { recursive: true }); await page.screenshot({ path: resolve(dir, `${theme}-${width}.png`), fullPage: true }) }
    Object.assign(controls[0]!, { state: 'completed', outcome: 'applied', reason: 'renamed_in_harness' })
    Object.assign(controls[1]!, { state: 'completed', outcome: 'rejected', reason: 'unsupported_by_harness' })
    await page.clock.runFor(6000)
    await expect(requests.getByText('Applied', { exact: true })).toBeVisible()
    await expect(requests.getByText('Rejected', { exact: true })).toBeVisible()
    await expect(requests.getByText('unsupported by harness', { exact: true })).toBeVisible()
    Object.assign(controls[1]!, { reason: 'request_expired' })
    await page.clock.runFor(6000)
    await expect(requests.getByText('Expired', { exact: true })).toBeVisible()
  })
}

test('the request form keeps its actions still while the catalog loads', async ({ page }) => {
  const { worker } = await setup(page)
  let release!: () => void
  const held = new Promise<void>(resolve => { release = resolve })
  await page.route('**/api/agent-accounts/catalog?*', async route => { await held; await route.fallback() })
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.goto(`/agents/${worker.id}?tab=messages`)
  const model = await openRequest(page, 'Change model or effort…')
  await expect(model.getByText('Loading models…')).toBeVisible()
  const guard = await controlStability(page, { cancel: model.getByRole('button', { name: 'Cancel' }), send: model.getByRole('button', { name: 'Send request' }), account: model.getByRole('combobox', { name: 'Account catalog' }) })
  await guard.check(async () => { release(); await expect(model.getByText('Loading models…')).toHaveCount(0) })
  guard.done()
  await page.keyboard.press('Escape')
  await page.keyboard.press('Escape')
  await expect(model).toHaveCount(0)
})

test('an accepted request reads the lists again', async ({ page }) => {
  const { worker, calls } = await setup(page)
  await page.goto(`/agents/${worker.id}?tab=messages`)
  const rename = await openRequest(page, 'Rename session…')
  await rename.getByRole('textbox', { name: 'New session name' }).fill('Ready for release')
  const before = sessionListReads(calls)
  await rename.getByRole('button', { name: 'Send request' }).click()
  await expect(page.getByText('Rename requested')).toBeVisible()
  await page.clock.runFor(100)
  await expect.poll(() => sessionListReads(calls)).toBeGreaterThan(before)
})

test('a managed or stopped session has no request composer', async ({ page }) => {
  const { worker } = await setup(page)
  worker.management_mode = 'managed'
  await page.goto(`/agents/${worker.id}?tab=messages`)
  await expect(panelOf(page)).toBeVisible()
  await panelOf(page).getByRole('button', { name: 'More session actions', exact: true }).click()
  await expect(page.getByRole('menuitem', { name: /^Rename session…/ })).toHaveCount(0)
  await page.keyboard.press('Escape')
  worker.management_mode = 'unmanaged'; worker.phase = 'stopped'; worker.stopped_at = new Date(now).toISOString()
  await page.reload()
  await expect(panelOf(page)).toBeVisible()
  await panelOf(page).getByRole('button', { name: 'More session actions', exact: true }).click()
  await expect(page.getByRole('menuitem', { name: /^Rename session…/ })).toHaveCount(0)
  await expect(page.getByText('Ask this session to…')).toHaveCount(0)
})

test('a reader cannot send session requests', async ({ page }) => {
  const { worker } = await setup(page, 'light', false)
  await page.goto(`/agents/${worker.id}?tab=messages`)
  await expect(panelOf(page)).toBeVisible()
  await expect(page.getByText('Ask this session to…')).toHaveCount(0)
  await panelOf(page).getByRole('button', { name: 'More session actions', exact: true }).click()
  await expect(page.getByRole('menu', { name: 'More session actions' })).toBeVisible()
  await expect(page.getByRole('menuitem', { name: /^Rename session…/ })).toHaveCount(0)
  await expect(page.getByRole('menuitem', { name: /^Change model or effort…/ })).toHaveCount(0)
})
