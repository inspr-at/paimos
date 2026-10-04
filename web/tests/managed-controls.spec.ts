// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Locator, type Page } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents, sessionListReads } from './agents-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'

const id = '5e000000-0000-4000-8000-000000000001'
const ownership = { daemon_id: 'fixture', generation: 'a'.repeat(32), process_id: 'b'.repeat(32), root_pid: 1234, group_id: 1234, started_at: '2026-09-29T00:00:00Z' }
async function setup(page: Page, options: { stale?: boolean; denied?: boolean; lostResponse?: boolean; pending?: boolean; reject?: string } = {}) {
  await mockWork(page, fixtures(), { admin: true })
  const data = agentData({ me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' } })
  Object.assign(data.sessions[0]!, { advertised_capabilities: ['status', 'steer', 'interrupt', 'stop', 'managed_control_v1', 'rename', 'model', 'effort'], display_label: 'Focused session', model: 'fixture-model', reasoning_effort: 'high', process_ownership: ownership, process_observed_at: new Date(Date.now() - (options.stale ? 60000 : 1000)).toISOString() })
  const calls = await mockAgents(page, data)
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
  const pauses: { path: string; body: Record<string, unknown> }[] = []
  await page.route('**/api/projects/*/harness-sessions/*/pause', route => {
    const body = route.request().postDataJSON()
    pauses.push({ path: new URL(route.request().url()).pathname, body })
    const target = data.sessions[0]!
    Object.assign(target, { revision: Number(target.revision) + 1, pause: { control_id: 'pause-stop', state: 'requested', level: body.level, note: body.note, stop_requested: body.level === 'stop_now', deliver: true } })
    return route.fulfill({ json: target })
  })
  await page.goto(`/agents/${id}`)
  await expect(page.getByRole('region', { name: 'Session controls' })).toBeVisible()
  return { data, requests, calls, pauses }
}
const controls = (page: Page) => page.getByRole('region', { name: 'Session controls' })
const more = (page: Page) => controls(page).getByRole('button', { name: 'More session controls', exact: true })
const interrupt = (page: Page) => page.getByRole('menuitem', { name: /^Interrupt this step/ })
const stopButton = (page: Page) => page.getByRole('complementary', { name: 'Session details' }).getByRole('button', { name: 'Stop now…', exact: true })
async function sendInterrupt(page: Page) { await more(page).click(); await interrupt(page).click() }

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

test('an accepted control reads the lists again', async ({ page }) => {
  const { calls } = await setup(page)
  const before = sessionListReads(calls)
  await sendInterrupt(page)
  await expect(controls(page).getByRole('status')).toContainText('Interrupt applied')
  await expect.poll(() => sessionListReads(calls)).toBeGreaterThan(before)
})

test('interrupt sends directly, Stop now confirms a durable stop request', async ({ page }) => {
  const { requests, pauses } = await setup(page)
  await sendInterrupt(page)
  await expect(controls(page).getByRole('status')).toContainText('Interrupt applied')
  await stopButton(page).click()
  const dialog = page.getByRole('dialog', { name: 'Stop now Focused session', exact: true })
  expect(requests).toHaveLength(1)
  expect(pauses).toHaveLength(0)
  await dialog.getByRole('button', { name: /^Stop now/ }).click()
  await expect(dialog).toBeHidden()
  await expect(page.locator('.toast').filter({ hasText: '1 pause/stop request sent.' })).toBeVisible()
  expect(requests.map(r => r.kind)).toEqual(['interrupt'])
  expect(pauses).toEqual([{ path: `/api/projects/p-pharos/harness-sessions/${id}/pause`, body: { level: 'stop_now', note: '' } }])
  // Accepted stop requests are not evidence that the process has exited.
  await expect(controls(page).getByRole('status')).not.toContainText('session exited')
})

for (const condition of ['stale', 'denied'] as const) test(`${condition} session cannot send controls`, async ({ page }) => {
  const { requests, pauses } = await setup(page, { [condition]: true })
  await expect(controls(page).getByRole('button', { name: 'Steer', exact: true })).toBeDisabled()
  await more(page).click()
  await expect(interrupt(page)).toBeDisabled()
  for (const field of ['name', 'model', 'effort']) await expect(page.getByRole('menuitem', { name: `Edit ${field}`, exact: true })).toBeDisabled()
  await page.keyboard.press('Escape')
  // Permission denial also hides the separate durable Stop now action.
  if (condition === 'denied') await expect(stopButton(page)).toHaveCount(0)
  expect(requests).toHaveLength(0)
  expect(pauses).toHaveLength(0)
})

test('pending and expired are honest daemon outcomes', async ({ page }) => {
  await setup(page, { reject: 'authorization_expired' })
  await sendInterrupt(page)
  await expect(controls(page).getByRole('status')).toHaveText('Expired before delivery.')
})

test('lost POST response preserves the request id for explicit retry', async ({ page }) => {
  const { requests } = await setup(page, { lostResponse: true })
  await sendInterrupt(page)
  await expect(controls(page).getByRole('status')).toContainText('Outcome unconfirmed')
  await controls(page).getByRole('button', { name: 'Retry same request' }).click()
  await expect(controls(page).getByRole('status')).toContainText('Interrupt applied')
  expect(requests).toHaveLength(2)
  expect(requests[1]).toEqual(requests[0])
})

async function touch(locator: Locator) {
  const box = await locator.boundingBox()
  expect(box?.width).toBeGreaterThanOrEqual(44)
  expect(box?.height).toBeGreaterThanOrEqual(44)
}

test('a lost steer response keeps the receipt and retry inside the phone sheet', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  const { requests } = await setup(page, { lostResponse: true })
  await controls(page).getByRole('button', { name: 'Steer', exact: true }).click()
  const sheet = page.getByRole('dialog', { name: 'Steer' })
  await sheet.getByLabel('What should change?').fill('Keep going.')
  await sheet.getByRole('button', { name: 'Send steer' }).click()
  await expect(sheet).toBeVisible()
  await expect(sheet.getByRole('status')).toContainText('Outcome unconfirmed')
  await expect(sheet.getByRole('alert')).toBeVisible()
  await expect(sheet.getByLabel('What should change?')).toHaveValue('Keep going.')
  await touch(sheet.getByRole('button', { name: 'Check result' }))
  await touch(sheet.getByRole('button', { name: 'Retry same request' }))
  await sheet.getByRole('button', { name: 'Check result' }).click()
  await expect(sheet.getByRole('status')).toContainText('Steer applied')
  await expect(sheet.getByRole('button', { name: 'Check result' })).toHaveCount(0)
  expect(requests).toHaveLength(1)
})

test('phone stop asks before sending, and More reaches Recover and Remove', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  const { requests, pauses } = await setup(page)
  await page.route('**/harness-sessions/*/recovery', route => route.fulfill({ json: {
    session_id: id, host: 'imac0', display_label: 'Focused session', observed_revision: 'c'.repeat(64),
    confirmation: `archive ${id} on imac0`, process_state: 'running', process_scope: 'No process will be signalled.',
    can_archive: true, force_stop_available: false, force_stop_reason: 'Force stop is not available.',
  } }))
  await touch(controls(page).getByRole('button', { name: 'Steer', exact: true }))
  await touch(stopButton(page))
  const more = controls(page).getByRole('button', { name: 'More session controls' })
  await more.click()
  await touch(interrupt(page))
  await page.keyboard.press('Escape')
  await touch(more)
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)

  await stopButton(page).click()
  const stop = page.getByRole('dialog', { name: 'Stop now Focused session', exact: true })
  await expect(stop.getByRole('button', { name: /^Stop now/ })).toBeVisible()
  await touch(stop.getByRole('button', { name: 'Close pause dialog' }))
  expect(requests).toHaveLength(0)
  expect(pauses).toHaveLength(0)
  await page.keyboard.press('Escape')
  await expect(stop).toBeHidden()
  expect(requests).toHaveLength(0)
  expect(pauses).toHaveLength(0)

  await stopButton(page).click()
  await expect(stop.getByRole('button', { name: /^Stop now/ })).toBeVisible()
  await stop.getByRole('button', { name: 'Close pause dialog', exact: true }).click()
  await expect(stop).toBeHidden()
  expect(requests).toHaveLength(0)
  await expect(page).toHaveURL(new RegExp(id))

  await more.click()
  const menu = page.getByRole('menu', { name: 'More session actions' })
  await expect(menu.locator('p')).toHaveCount(0)
  await expect(page.getByText('Tools stay bound to this run and its budget. Other processes running as the same OS user are outside this isolation boundary.', { exact: true })).toBeVisible()
  const recoverItem = menu.getByRole('menuitem', { name: 'Recover', exact: true })
  const removeItem = menu.getByRole('menuitem', { name: 'Remove Focused session' })
  await touch(recoverItem)
  await touch(removeItem)
  await page.keyboard.press('Escape')
  await expect(more).toBeFocused()
  await expect(menu).toBeHidden()

  await more.click()
  await menu.getByRole('menuitem', { name: 'Recover', exact: true }).click()
  const recover = page.getByRole('dialog', { name: 'Recover session' })
  await expect(recover).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(recover).toBeHidden()

  await more.click()
  await menu.getByRole('menuitem', { name: 'Remove Focused session' }).click()
  const confirm = page.getByRole('dialog', { name: 'Remove Focused session?' })
  // A live session with a fresh heartbeat: the consequence is true, so it is said.
  await expect(confirm).toContainText('Its process keeps running')
  await confirm.getByRole('button', { name: 'Cancel' }).click()
  await expect(confirm).toBeHidden()
  await expect(page).toHaveURL(new RegExp(id))
  expect(requests).toHaveLength(0)
  expect(pauses).toHaveLength(0)
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
  await sendInterrupt(page)
  await expect(controls(page).getByRole('status')).toHaveText('Interrupt pending · waiting for the daemon.')
  for (const name of ['Steer', 'More session controls']) await expect(controls(page).getByRole('button', { name, exact: true })).toBeDisabled()
  expect(requests).toHaveLength(1)
})

test('a late submission receipt cannot follow navigation to another session', async ({ page }) => {
  await setup(page)
  let respond: (() => Promise<void>) | undefined
  await page.route('**/api/projects/*/harness-sessions/*/managed-controls', route => {
    const body = route.request().postDataJSON()
    respond = () => route.fulfill({ status: 201, json: { id: body.request_id, session_id: id, kind: body.kind, state: 'pending', expires_at: new Date(Date.now() + 45000).toISOString() } })
  })
  await sendInterrupt(page)
  await expect(controls(page).getByRole('status')).toHaveText('Request pending · waiting for the server.')
  await expect.poll(() => !!respond).toBe(true)
  await page.locator('[data-row="s:5e000000-0000-4000-8000-000000000002"] .agent-link').click()
  await respond!()
  await page.locator(`[data-row="s:${id}"] .agent-link`).click()
  await expect(controls(page).getByRole('status')).toHaveCount(0)
  await more(page).click()
  await expect(interrupt(page)).toBeEnabled()
})

for (const width of [1600, 390]) for (const theme of ['light', 'dark']) test(`managed steer ${width} ${theme}`, async ({ page }) => {
  await page.setViewportSize({ width, height: 1000 })
  await page.emulateMedia({ colorScheme: theme as 'light' | 'dark' })
  await setup(page)
  await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
  if (process.env.MANAGED_CONTROL_SHOTS) {
    await mkdir(process.env.MANAGED_CONTROL_SHOTS, { recursive: true })
    await page.screenshot({ path: `${process.env.MANAGED_CONTROL_SHOTS}/resting-${width}-${theme}.png`, fullPage: true })
    if (width === 390) {
      await controls(page).getByRole('button', { name: 'More session controls' }).click()
      await page.screenshot({ path: `${process.env.MANAGED_CONTROL_SHOTS}/menu-${width}-${theme}.png`, fullPage: true })
      await page.keyboard.press('Escape')
    }
  }
  await controls(page).getByRole('button', { name: 'Steer', exact: true }).click()
  const steer = width === 390 ? page.getByRole('dialog', { name: 'Steer' }) : controls(page)
  await steer.getByLabel('What should change?').fill('Keep the change focused. Check the tenant isolation fixtures before committing.')
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
  for (const name of ['Edit name', 'Edit model', 'Edit effort', 'Steer', 'More session controls']) await expect(controls(page).getByRole('button', { name, exact: true })).toBeDisabled()
})

for (const width of [1600, 390]) for (const theme of ['light', 'dark']) test(`managed settings ${width} ${theme}`, async ({ page }) => {
  await page.setViewportSize({ width, height: 1000 })
  await setup(page)
  await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
  if (width === 390) {
    await controls(page).getByRole('button', { name: 'More session controls' }).click()
    await page.getByRole('menuitem', { name: 'Edit effort' }).click()
    await expect(page.getByRole('dialog', { name: 'Effort' }).getByLabel('Effort', { exact: true })).toBeEnabled()
  } else {
    await controls(page).getByRole('button', { name: 'Edit effort' }).click()
    await expect(controls(page).getByLabel('Effort', { exact: true })).toBeEnabled()
  }
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  if (process.env.MANAGED_CONTROL_SHOTS) {
    await mkdir(process.env.MANAGED_CONTROL_SHOTS, { recursive: true })
    await page.screenshot({ path: `${process.env.MANAGED_CONTROL_SHOTS}/settings-${width}-${theme}.png`, fullPage: true })
  }
})
