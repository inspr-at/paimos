// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { resolve } from 'node:path'
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
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
  await mockAgents(page, data)
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
  return { worker, controls, submissions }
}

for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) {
  test(`${theme} ${width}: request stays pending until session completion`, async ({ page }) => {
    const { worker, controls, submissions } = await setup(page, theme)
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.goto(`/agents/${worker.id}`)
    const panel = page.getByRole('complementary', { name: 'Session details' })
    const requests = panel.getByRole('region', { name: 'Session requests' })
    await requests.getByText('Ask this session to…').click()
    await requests.getByRole('textbox', { name: 'Requested session name' }).fill('Ready for release')
    await requests.getByRole('button', { name: 'Send request' }).click()
    await expect(requests).toContainText('Requested · waiting for the session')
    await expect(panel.getByRole('heading', { name: 'Release checks', exact: true })).toBeVisible()
    expect(submissions[0]?.expected_generation).toBe(worker.id)
    expect(submissions[0]?.kind).toBe('rename_request')
    await requests.getByText('Ask this session to…').click()
    await requests.getByRole('combobox', { name: 'Change to request' }).selectOption('model_request')
    await requests.getByRole('combobox', { name: 'Model', exact: true }).selectOption('Catalog model')
    await requests.getByRole('combobox', { name: 'Effort', exact: true }).selectOption(profileID)
    await requests.getByRole('button', { name: 'Send request' }).click()
    await expect(requests.getByText('Requested · waiting for the session', { exact: true })).toHaveCount(2)
    expect(submissions[1]?.account_id).toBe(accountID)
    expect(submissions[1]?.model_profile_id).toBe(profileID)
    await expect(panel.locator('.fact').filter({ has: page.locator('dt', { hasText: 'Model' }) })).toContainText('Current model')
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

test('a managed or stopped session has no request composer', async ({ page }) => {
  const { worker } = await setup(page)
  worker.management_mode = 'managed'
  await page.goto(`/agents/${worker.id}`)
  await expect(page.getByRole('complementary', { name: 'Session details' })).toBeVisible()
  await expect(page.getByText('Ask this session to…')).toHaveCount(0)
  worker.management_mode = 'unmanaged'; worker.phase = 'stopped'; worker.stopped_at = new Date(now).toISOString()
  await page.reload()
  await expect(page.getByRole('complementary', { name: 'Session details' })).toBeVisible()
  await expect(page.getByText('Ask this session to…')).toHaveCount(0)
})

test('a reader cannot send session requests', async ({ page }) => {
  const { worker } = await setup(page, 'light', false)
  await page.goto(`/agents/${worker.id}`)
  await expect(page.getByRole('complementary', { name: 'Session details' })).toBeVisible()
  await expect(page.getByText('Ask this session to…')).toHaveCount(0)
})
