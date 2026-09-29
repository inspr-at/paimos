// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'

const id = '5e000000-0000-4000-8000-000000000001'
const ownership = { daemon_id: 'fixture', generation: 'a'.repeat(32), process_id: 'b'.repeat(32), root_pid: 1234, group_id: 1234, started_at: '2026-09-29T00:00:00Z' }
async function setup(page: Page, options: { stale?: boolean; denied?: boolean; lostResponse?: boolean; pending?: boolean; reject?: string } = {}) {
  await mockWork(page, fixtures(), { admin: true })
  const data = agentData({ me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' } })
  Object.assign(data.sessions[0]!, { advertised_capabilities: ['status', 'steer', 'interrupt', 'stop', 'managed_control_v1', 'rename', 'model', 'effort'], display_label: 'Focused session', model: 'fixture-model', reasoning_effort: 'high', process_ownership: ownership, process_observed_at: new Date(Date.now() - (options.stale ? 60000 : 1000)).toISOString() })
  await mockAgents(page, data)
  if (options.denied) await page.route('**/api/me/permissions*', route => {
    const effective = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') || undefined)
    effective.workspace.permissions = effective.workspace.permissions.filter(p => p !== 'harness.control')
    if (effective.project) effective.project.permissions = effective.project.permissions.filter(p => p !== 'harness.control')
    return route.fulfill({ json: effective })
  })
  await page.route('**/api/projects/*/harness-sessions/*/managed-settings', route => route.fulfill({ json: { models: [{ model: 'fixture-model', efforts: ['low', 'high'] }, { model: 'other-model', efforts: ['high'] }] } }))
  const requests: Record<string, unknown>[] = []
  let control: Record<string, unknown> | undefined
  await page.route('**/api/projects/*/harness-sessions/*/managed-controls', async route => {
    const body = route.request().postDataJSON()
    requests.push(body)
    control = { id: body.request_id, session_id: id, kind: body.kind, state: 'pending', outcome: null, reason: null, expires_at: new Date(Date.now() + 45000).toISOString() }
    if (options.lostResponse && requests.length === 1) return route.abort('failed')
    await route.fulfill({ status: 201, json: control })
  })
  await page.route('**/api/projects/*/harness-sessions/*/controls/*', route => {
    if (!control) return route.fallback()
    return route.fulfill({ json: { ...control, ...(options.pending ? {} : { state: 'completed', outcome: options.reject ? 'rejected' : 'applied', reason: options.reject || (control.kind === 'steer' ? 'queued_next_turn' : control.kind === 'stop' ? 'native_close_exited' : control.kind === 'effort' ? 'setting_applied_next_turn' : control.kind === 'model' || control.kind === 'rename' ? 'setting_applied' : 'native_interrupt_acknowledged') }) } })
  })
  await page.goto(`/agents/${id}`)
  await expect(page.getByRole('region', { name: 'Session controls' })).toBeVisible()
  return { data, requests }
}
const controls = (page: Page) => page.getByRole('region', { name: 'Session controls' })

test('steer is session-bound, ephemeral input with a real queued receipt', async ({ page }) => {
  const { requests } = await setup(page)
  await controls(page).getByRole('button', { name: 'Steer', exact: true }).click()
  await controls(page).getByLabel('What should change?').fill('Run the focused fixtures next.')
  await controls(page).getByRole('button', { name: 'Send steer' }).click()
  await expect(controls(page).getByRole('status')).toHaveText('Steer applied · queued for the next turn.')
  expect(requests).toHaveLength(1)
  await expect(page.locator('.composer')).toHaveCount(0)
  expect(requests[0]).toMatchObject({ kind: 'steer', text: 'Run the focused fixtures next.', expected_ownership: ownership })
  expect(requests[0]!.request_id).toMatch(/^[a-f0-9-]{36}$/)
  await controls(page).getByRole('button', { name: 'Steer', exact: true }).click()
  await expect(controls(page).getByLabel('What should change?')).toHaveValue('')
})

test('interrupt sends directly, stop requires its local confirmation', async ({ page }) => {
  const { requests } = await setup(page)
  await controls(page).getByRole('button', { name: 'Interrupt', exact: true }).click()
  await expect(controls(page).getByRole('status')).toContainText('Interrupt applied')
  await controls(page).getByRole('button', { name: 'Stop', exact: true }).click()
  expect(requests).toHaveLength(1)
  await controls(page).getByRole('button', { name: 'Confirm stop' }).click()
  await expect(controls(page).getByRole('status')).toHaveText('Stop applied · session exited.')
  expect(requests.map(r => r.kind)).toEqual(['interrupt', 'stop'])
})

for (const condition of ['stale', 'denied'] as const) test(`${condition} session cannot send controls`, async ({ page }) => {
  const { requests } = await setup(page, { [condition]: true })
  for (const name of ['Steer', 'Interrupt', 'Stop']) await expect(controls(page).getByRole('button', { name, exact: true })).toBeDisabled()
  expect(requests).toHaveLength(0)
})

test('pending and expired are honest daemon outcomes', async ({ page }) => {
  await setup(page, { reject: 'authorization_expired' })
  await controls(page).getByRole('button', { name: 'Interrupt' }).click()
  await expect(controls(page).getByRole('status')).toHaveText('Expired before delivery.')
})

test('lost POST response preserves the request id for explicit retry', async ({ page }) => {
  const { requests } = await setup(page, { lostResponse: true })
  await controls(page).getByRole('button', { name: 'Interrupt' }).click()
  await expect(controls(page).getByRole('status')).toContainText('Outcome unconfirmed')
  await controls(page).getByRole('button', { name: 'Retry same request' }).click()
  await expect(controls(page).getByRole('status')).toContainText('Interrupt applied')
  expect(requests).toHaveLength(2)
  expect(requests[1]).toEqual(requests[0])
})

test('navigation clears private drafts', async ({ page }) => {
  await setup(page, { pending: true })
  await controls(page).getByRole('button', { name: 'Steer', exact: true }).click()
  await controls(page).getByLabel('What should change?').fill('Private draft')
  await page.locator('[data-row="s:5e000000-0000-4000-8000-000000000002"] .agent-link').click()
  await expect(controls(page)).toHaveCount(0)
  await page.locator(`[data-row="s:${id}"] .agent-link`).click()
  await controls(page).getByRole('button', { name: 'Steer', exact: true }).click()
  await expect(controls(page).getByLabel('What should change?')).toHaveValue('')
})

test('pending controls block a second action until the daemon responds', async ({ page }) => {
  const { requests } = await setup(page, { pending: true })
  await controls(page).getByRole('button', { name: 'Interrupt', exact: true }).click()
  await expect(controls(page).getByRole('status')).toHaveText('Interrupt pending · waiting for the daemon.')
  for (const name of ['Steer', 'Interrupt', 'Stop']) await expect(controls(page).getByRole('button', { name, exact: true })).toBeDisabled()
  expect(requests).toHaveLength(1)
})

test('a late submission receipt cannot follow navigation to another session', async ({ page }) => {
  await setup(page)
  let respond: (() => Promise<void>) | undefined
  await page.route('**/api/projects/*/harness-sessions/*/managed-controls', route => {
    const body = route.request().postDataJSON()
    respond = () => route.fulfill({ status: 201, json: { id: body.request_id, session_id: id, kind: body.kind, state: 'pending', expires_at: new Date(Date.now() + 45000).toISOString() } })
  })
  await controls(page).getByRole('button', { name: 'Interrupt', exact: true }).click()
  await expect(controls(page).getByRole('status')).toHaveText('Request pending · waiting for the server.')
  await expect.poll(() => !!respond).toBe(true)
  await page.locator('[data-row="s:5e000000-0000-4000-8000-000000000002"] .agent-link').click()
  await respond!()
  await page.locator(`[data-row="s:${id}"] .agent-link`).click()
  await expect(controls(page).getByRole('status')).toHaveCount(0)
  await expect(controls(page).getByRole('button', { name: 'Interrupt', exact: true })).toBeEnabled()
})

for (const width of [1600, 390]) for (const theme of ['light', 'dark']) test(`managed steer ${width} ${theme}`, async ({ page }) => {
  await page.setViewportSize({ width, height: 1000 })
  await page.emulateMedia({ colorScheme: theme as 'light' | 'dark' })
  await setup(page)
  await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
  await controls(page).getByRole('button', { name: 'Steer', exact: true }).click()
  await controls(page).getByLabel('What should change?').fill('Keep the change focused. Check the tenant isolation fixtures before committing.')
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  if (process.env.MANAGED_CONTROL_SHOTS) {
    await mkdir(process.env.MANAGED_CONTROL_SHOTS, { recursive: true })
    await page.screenshot({ path: `${process.env.MANAGED_CONTROL_SHOTS}/${width}-${theme}.png`, fullPage: true })
  }
})

for (const [field, kind, value] of [['name', 'rename', 'Review tenant isolation'], ['model', 'model', 'other-model'], ['effort', 'effort', 'low']]) test(`session ${field} uses typed acknowledged controls`, async ({ page }) => {
  const { requests } = await setup(page)
  await controls(page).getByRole('button', { name: `Edit ${field}` }).click()
  const input = controls(page).getByLabel(`${field![0]!.toUpperCase()}${field!.slice(1)}`, { exact: true })
  if (field === 'name') await input.fill(value!)
  else await input.selectOption(value!)
  await controls(page).getByRole('button', { name: `Save ${field}` }).click()
  await expect(controls(page).getByRole('status')).toContainText(`${field![0]!.toUpperCase()}${field!.slice(1)} applied`)
  if (kind === 'effort') await expect(controls(page).getByRole('status')).toContainText('takes effect next turn')
  expect(requests).toHaveLength(1)
  expect(requests[0]).toMatchObject({ kind, value, expected_ownership: ownership })
  expect(requests[0]).not.toHaveProperty('text')
})

test('setting rejected keeps reported values and surfaces the receipt', async ({ page }) => {
  await setup(page, { reject: 'setting_rejected' })
  await controls(page).getByRole('button', { name: 'Edit model' }).click()
  await controls(page).getByLabel('Model', { exact: true }).selectOption('other-model')
  await controls(page).getByRole('button', { name: 'Save model' }).click()
  await expect(controls(page).getByRole('status')).toHaveText('Model rejected by the harness.')
  await expect(controls(page).getByRole('button', { name: 'Edit model' })).toContainText('fixture-model')
})

test('setting pending blocks competing settings and process controls', async ({ page }) => {
  await setup(page, { pending: true })
  await controls(page).getByRole('button', { name: 'Edit name' }).click()
  await controls(page).getByLabel('Name', { exact: true }).fill('New name')
  await controls(page).getByRole('button', { name: 'Save name' }).click()
  await expect(controls(page).getByRole('status')).toHaveText('Name pending · waiting for the daemon.')
  for (const name of ['Edit name', 'Edit model', 'Edit effort', 'Steer', 'Interrupt', 'Stop']) await expect(controls(page).getByRole('button', { name, exact: true })).toBeDisabled()
})

for (const width of [1600, 390]) for (const theme of ['light', 'dark']) test(`managed settings ${width} ${theme}`, async ({ page }) => {
  await page.setViewportSize({ width, height: 1000 })
  await setup(page)
  await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
  await controls(page).getByRole('button', { name: 'Edit effort' }).click()
  await expect(controls(page).getByLabel('Effort', { exact: true })).toBeEnabled()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  if (process.env.MANAGED_CONTROL_SHOTS) {
    await mkdir(process.env.MANAGED_CONTROL_SHOTS, { recursive: true })
    await page.screenshot({ path: `${process.env.MANAGED_CONTROL_SHOTS}/settings-${width}-${theme}.png`, fullPage: true })
  }
})
