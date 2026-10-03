// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { agentData, mockAgents, sessionListReads, type AgentWorld } from './agents-fixtures'

const world: AgentWorld = { me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' }, nodes: {
  'p-pharos': { key: 'PRJ-17', title: 'Pharos' }, 'p-aeon': { key: 'PRJ-35', title: 'Aeon' }, 'p-frozen': { key: 'PRJ-26', title: 'Studio infrastructure' },
  'n-1': { key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning' }, 'n-2': { key: 'PHAROS-12', title: 'Add an Oracle Cloud connector' }, 'n-a1': { key: 'AEON-1', title: 'Aeon foundation' }, 'n-5': { key: 'PHAROS-15', title: 'Beacon health probes' }, 'n-6': { key: 'PHAROS-16', title: 'Retire the old dashboard' },
} }
async function setup(page: Page, admin = true) {
  await mockWork(page, fixtures(), { admin })
  const data = agentData(world)
  const calls = await mockAgents(page, data)
  const selected = data.sessions[1]!
  Object.assign(selected, { management_mode: 'unmanaged', host: 'workstation-offline', display_label: 'ops', advertised_capabilities: ['status'] })
  const preview = { session_id: selected.id, host: selected.host, display_label: 'ops', observed_revision: 'a'.repeat(64), confirmation: `archive ${selected.id} on ${selected.host}`, process_state: 'unknown', process_scope: 'No process will be signalled. This action archives this registration only; other sessions and child processes are unaffected.', can_archive: true, force_stop_available: false, force_stop_reason: 'Force stop requires a live daemon with verified ownership of this exact process generation.' }
  await page.route('**/harness-sessions/*/recovery', route => route.fulfill({ json: preview }))
  await page.goto(`/agents/${selected.id}`)
  await expect(page.getByRole('complementary', { name: 'Session details' })).toBeVisible()
  return { data, selected, preview, calls }
}
const dialog = (page: Page) => page.getByRole('dialog', { name: 'Recover session' })

test('archive requires exact confirmation and keeps unknown process status honest', async ({ page }) => {
  const errors = watchErrors(page)
  const { data, selected, preview } = await setup(page)
  const bodies: Record<string, unknown>[] = []
  await page.route('**/harness-sessions/*/archive', async route => {
    bodies.push(route.request().postDataJSON())
    Object.assign(selected, { archived_at: new Date().toISOString(), recovery_process_state: 'unknown', phase: 'stopped', stopped_at: new Date().toISOString(), stop_reason: 'archived_process_unknown' })
    data.sessions.splice(data.sessions.indexOf(selected), 1)
    await route.fulfill({ json: selected })
  })
  await page.getByRole('button', { name: 'Recover', exact: true }).click()
  await expect(dialog(page)).toContainText('workstation-offline')
  await expect(dialog(page)).toContainText('No force stop')
  await expect(dialog(page).getByRole('button', { name: 'Archive session' })).toBeDisabled()
  await dialog(page).getByLabel('Reason for recovery').fill('Heartbeat loop has exited; close the stale record')
  await dialog(page).getByLabel('Type the exact confirmation').fill('archive ops')
  await expect(dialog(page).getByRole('button', { name: 'Archive session' })).toBeDisabled()
  await dialog(page).getByLabel('Type the exact confirmation').fill(preview.confirmation)
  await dialog(page).getByRole('button', { name: 'Archive session' }).click()
  await expect(page).toHaveURL(/\/agents$/)
  await expect(page.getByText('Session archived. History is retained. Process state is unknown; no process was stopped.')).toBeVisible()
  await expect(page.getByText('Session not found', { exact: true })).toHaveCount(0)
  expect(bodies).toHaveLength(1)
  expect(bodies[0]?.expected_revision).toBe(preview.observed_revision)
  expect(errors).toEqual([])
})

test('stale observation clears confirmation and refreshes; uncertain retry keeps request identity', async ({ page }) => {
  const { preview } = await setup(page)
  const bodies: Record<string, unknown>[] = []
  await page.route('**/harness-sessions/*/archive', async route => {
    bodies.push(route.request().postDataJSON())
    const status = bodies.length === 1 ? 409 : 503
    await route.fulfill({ status, json: { error: status === 409 ? 'session changed' : 'Host connection unavailable; result unknown' } })
  })
  await page.getByRole('button', { name: 'Recover', exact: true }).click()
  await dialog(page).getByLabel('Reason for recovery').fill('Stale registration')
  await dialog(page).getByLabel('Type the exact confirmation').fill(preview.confirmation)
  await dialog(page).getByRole('button', { name: 'Archive session' }).click()
  await expect(dialog(page).getByRole('alert')).toContainText('session changed')
  await dialog(page).getByRole('button', { name: 'Refresh details' }).click()
  await expect(dialog(page).getByLabel('Type the exact confirmation')).toHaveValue('')
  await dialog(page).getByLabel('Type the exact confirmation').fill(preview.confirmation)
  await dialog(page).getByRole('button', { name: 'Archive session' }).click()
  await expect(dialog(page).getByRole('alert')).toContainText('result unknown')
  await dialog(page).getByRole('button', { name: 'Archive session' }).click()
  await expect.poll(() => bodies.length).toBe(3)
  expect(bodies[0]?.request_id).not.toBe(bodies[1]?.request_id)
  expect(bodies[1]).toEqual(bodies[2])
})

test('members do not receive record recovery controls', async ({ page }) => {
  await setup(page, false)
  await expect(page.getByRole('button', { name: 'Recover', exact: true })).toHaveCount(0)
})

for (const theme of ['light', 'dark']) for (const width of [1600, 390]) {
  test(`recovery visual ${width} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await setup(page)
    await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
    await page.getByRole('button', { name: 'Recover', exact: true }).click()
    await expect(dialog(page).getByRole('button', { name: 'Archive session' })).toBeVisible()
    await expect(dialog(page).getByRole('button', { name: 'Cancel' })).toBeFocused()
    expect(await dialog(page).evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
    await page.screenshot({ path: `../.agent-artifacts/recovery-${width}-${theme}.png` })
    await page.keyboard.press('Escape')
    await expect(dialog(page)).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Recover', exact: true })).toBeFocused()
  })
}

async function managed(page: Page) {
  const { selected, preview, calls } = await setup(page)
  Object.assign(preview, { force_stop_available: true, force_confirmation: `force stop ${selected.id} on ${selected.host} group 1234`, process_ownership: { daemon_id: 'workstation-agentd', generation: '1'.repeat(32), process_id: '2'.repeat(32), root_pid: 1234, group_id: 1234, started_at: '2026-09-27T10:00:00Z' } })
  await page.getByRole('button', { name: 'Recover', exact: true }).click()
  await dialog(page).getByRole('radio', { name: 'Force stop process' }).click()
  return { selected, calls, preview: preview as typeof preview & { force_confirmation: string } }
}

test('force stop shows the exact group and waits for verified daemon outcome', async ({ page }) => {
  const { preview } = await managed(page)
  let outcome = 'pending'
  let body: Record<string, unknown> | undefined
  const control = () => ({ id: 'force-control', state: outcome === 'pending' ? 'claimed' : 'completed', outcome: outcome === 'pending' ? null : 'applied', reason: outcome === 'pending' ? null : 'owned_group_signalled_root_exited' })
  await page.route('**/controls/force-stop', async route => { body = route.request().postDataJSON(); await route.fulfill({ status: 201, json: control() }) })
  await page.route('**/controls/force-control', route => route.fulfill({ json: control() }))
  await expect(dialog(page)).toContainText('Root PID 1234')
  await expect(dialog(page)).toContainText('Unsaved work may be lost')
  await expect(dialog(page).getByRole('button', { name: 'Force stop session' })).toBeDisabled()
  await dialog(page).getByLabel('Reason for recovery').fill('Owned process ignored normal stop')
  await dialog(page).getByLabel('Type the exact confirmation').fill(preview.force_confirmation)
  await dialog(page).getByRole('button', { name: 'Force stop session' }).click()
  const result = page.getByRole('dialog', { name: 'Force stop requested' })
  await expect(result).toContainText('Process exit has not been confirmed')
  expect(body?.confirmation).toBe(preview.force_confirmation)
  outcome = 'applied'
  await expect(page).toHaveURL(/\/agents$/)
  await expect(page.getByText('The daemon signalled the owned process group and verified that its root process exited. Processes outside that group were not targeted.')).toBeVisible()
})

test('an accepted force stop reads the lists again before the daemon answers', async ({ page }) => {
  const { preview, calls } = await managed(page)
  const control = { id: 'force-control', state: 'claimed', outcome: null, reason: null }
  await page.route('**/controls/force-stop', route => route.fulfill({ status: 201, json: control }))
  await page.route('**/controls/force-control', route => route.fulfill({ json: control }))
  await dialog(page).getByLabel('Reason for recovery').fill('Owned process ignored normal stop')
  await dialog(page).getByLabel('Type the exact confirmation').fill(preview.force_confirmation)
  const before = sessionListReads(calls)
  await dialog(page).getByRole('button', { name: 'Force stop session' }).click()
  await expect(page.getByRole('dialog', { name: 'Force stop requested' })).toContainText('Process exit has not been confirmed')
  await expect.poll(() => sessionListReads(calls)).toBeGreaterThan(before)
})

for (const theme of ['light', 'dark']) for (const width of [1600, 390]) {
  test(`force confirmation visual ${width} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await managed(page)
    await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
    await expect(dialog(page)).toContainText('Root PID 1234')
    expect(await dialog(page).evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
    await page.screenshot({ path: `../.agent-artifacts/force-${width}-${theme}.png` })
  })
}


test('offline force request expires with an unconfirmed outcome and stops polling', async ({ page }) => {
  const { preview } = await managed(page)
  await page.clock.install()
  let polls = 0
  const control = { id: 'offline-force', state: 'claimed', outcome: null, reason: null, expires_at: new Date(Date.now() + 45000).toISOString() }
  await page.route('**/controls/force-stop', route => route.fulfill({ status: 201, json: control }))
  let releasePoll!: () => void
  const unreachable = new Promise<void>(resolve => { releasePoll = resolve })
  await page.route('**/controls/offline-force', async route => { polls++; await unreachable; await route.fulfill({ json: control }) })
  await dialog(page).getByLabel('Reason for recovery').fill('Host stopped responding')
  await dialog(page).getByLabel('Type the exact confirmation').fill(preview.force_confirmation)
  await dialog(page).getByRole('button', { name: 'Force stop session' }).click()
  const result = page.getByRole('dialog', { name: 'Force stop requested' })
  await expect(result).toContainText('Process exit has not been confirmed')
  await page.clock.fastForward(46000)
  await expect(result).toContainText('Process exit is unconfirmed')
  releasePoll()
  const stoppedPolls = polls
  await page.clock.fastForward(120000)
  expect(polls).toBe(stoppedPolls)
  await result.getByRole('button', { name: 'Refresh details' }).click()
  await expect(dialog(page).getByLabel('Type the exact confirmation')).toHaveValue('')
  await expect(dialog(page).getByRole('button', { name: 'Force stop session' })).toBeDisabled()
})


test('completed force retry retains its verified outcome after authorization expiry', async ({ page }) => {
 const { preview } = await managed(page)
 await page.route('**/controls/force-stop', route => route.fulfill({status:201,json:{id:'completed-force',state:'completed',outcome:'applied',reason:'owned_group_signalled_root_exited',expires_at:'2020-01-01T00:00:00Z'}}))
 await dialog(page).getByLabel('Reason for recovery').fill('Retry exact accepted command')
 await dialog(page).getByLabel('Type the exact confirmation').fill(preview.force_confirmation)
 await dialog(page).getByRole('button',{name:'Force stop session'}).click()
 await expect(page).toHaveURL(/\/agents$/)
 await expect(page.getByText('The daemon signalled the owned process group and verified that its root process exited. Processes outside that group were not targeted.')).toBeVisible()
 await expect(page.getByText('The confirmation expired before a verified result was received.',{exact:false})).toHaveCount(0)
})
