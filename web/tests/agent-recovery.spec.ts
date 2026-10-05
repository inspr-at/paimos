// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { agentData, mockAgents, type AgentWorld } from './agents-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { controlStability } from './control-stability'
import type { HarnessSession } from '../src/lib/agents'

const world: AgentWorld = { me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' }, nodes: {
  'p-pharos': { key: 'PRJ-17', title: 'Pharos' }, 'p-aeon': { key: 'PRJ-35', title: 'Aeon' }, 'p-frozen': { key: 'PRJ-26', title: 'Studio infrastructure' },
  'n-1': { key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning' }, 'n-2': { key: 'PHAROS-12', title: 'Add an Oracle Cloud connector' }, 'n-a1': { key: 'AEON-1', title: 'Aeon foundation' }, 'n-5': { key: 'PHAROS-15', title: 'Beacon health probes' }, 'n-6': { key: 'PHAROS-16', title: 'Retire the old dashboard' },
} }
test.beforeEach(async ({ page }) => { await page.clock.install() })

async function setup(page: Page, action: '' | 'restart' | 'reconnect' = 'restart', admin = true, long = false) {
  await mockWork(page, fixtures(), { admin })
  const data = agentData(world), selected = data.sessions[1]! as unknown as HarnessSession
  const label = long ? 'Wiederherstellung der Agentenverbindung mit geprüftem Prozessbesitz' : 'Recovery worker'
  Object.assign(data.runs[1]!, { status: 'running' })
  Object.assign(selected, { process_observed_at: new Date().toISOString(), process_ownership: { daemon_id: 'fixture', generation: 'a'.repeat(32), process_id: 'b'.repeat(32), root_pid: 1234, group_id: 1234, started_at: new Date().toISOString() }, management_mode: 'managed', harness: 'claude', display_label: label, advertised_capabilities: ['managed_control_v1', 'session_recovery_v1', 'stop', 'inbox'], heartbeat_at: new Date(Date.now() - 180000).toISOString(), agent_recovery: { session_id: selected.id, observed_revision: 'a'.repeat(64), cause: action === 'reconnect' ? 'inbox_not_listening' : action ? 'heartbeat_overdue' : 'host_not_reporting', detail: long ? 'Der gekoppelte Rechner meldet sich weiterhin, aber die Sitzung hat seit mehreren Minuten keinen Heartbeat mehr gemeldet. Ein Neustart wartet auf das bestätigte Prozessende.' : action ? 'The host is reporting but this session needs recovery.' : 'The paired host has not reported recently; its process state is unknown.', action } })
  await mockAgents(page, data)
  // Restart queues a continuation, so it also needs run.create; the admin fixture grants it like the other agent specs.
  if (admin) await page.route('**/api/me/permissions*', route => {
    const answer = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    answer.workspace.permissions = [...answer.workspace.permissions, 'run.create', 'run.read', 'work_orders.read']
    return route.fulfill({ json: answer })
  })
  let request: Record<string, unknown> | undefined
  let outcome: string | null = null
  await page.route('**/recover-agent', async route => {
    request = route.request().postDataJSON()
    await route.fulfill({ status: 201, json: { id: request?.request_id, session_id: selected.id, action, state: 'pending', outcome: null } })
  })
  await page.route('**/recover-agent/*', route => route.fulfill({ json: { id: request?.request_id, session_id: selected.id, action, state: outcome ? 'completed' : 'claimed', outcome, next_run_id: outcome === 'continuation_queued' ? 'continuation-run' : null } }))
  await page.goto(`/agents/${selected.id}`)
  await expect(page.getByRole('complementary', { name: 'Session details' })).toBeVisible()
  return { selected, label, diagnosis: async (detail: string) => { selected.agent_recovery!.detail = detail; await page.clock.runFor(21000); await expect(page.getByRole('complementary', { name: 'Session details' })).toContainText(detail) }, request: () => request, complete: (value: string) => { outcome = value } }
}

test('Restart requests the exact row and keeps destructive actions behind the separator', async ({ page }) => {
  const errors = watchErrors(page), fixture = await setup(page)
  await page.getByRole('button', { name: `Actions for ${fixture.label}`, exact: true }).click()
  const menu = page.getByRole('menu', { name: `Actions for ${fixture.label}` })
  const items = await menu.getByRole('menuitem').allTextContents()
  expect(items.indexOf('Restart')).toBeLessThan(items.findIndex(text => text.includes('Copy session id')))
  const stop = menu.getByRole('menuitem', { name: 'Stop now…', exact: true })
  await expect(stop).toBeVisible()
  expect(await stop.evaluate(el => el.previousElementSibling?.tagName)).toBe('HR')
  await menu.getByRole('menuitem', { name: 'Restart', exact: true }).click()
  await expect(page.getByText(/Recovery worker: Restart requested/)).toBeVisible()
  expect(fixture.request()).toMatchObject({ expected_revision: 'a'.repeat(64), action: 'restart' })
  fixture.complete('continuation_queued')
  await page.clock.runFor(1100)
  await expect(page.getByText(/Continuation queued for normal dispatch/)).toBeVisible()
  expect(errors).toEqual([])
})

test('Reconnect waits for reporting confirmation and rejected recovery stays an error', async ({ page }) => {
  const fixture = await setup(page, 'reconnect')
  const panel = page.getByRole('complementary', { name: 'Session details' })
  await panel.getByRole('button', { name: 'Reconnect', exact: true }).click()
  await expect(page.getByText(/Reconnect requested. Waiting for the daemon/)).toBeVisible()
  fixture.complete('rejected')
  await page.clock.runFor(1100)
  await expect(page.getByText(/Recovery was rejected; no continuation was queued/)).toBeVisible()
  await expect(page.getByText(/reporting restored/)).toHaveCount(0)
  expect(fixture.request()?.action).toBe('reconnect')
})

test('offline and unauthorized sessions offer no Restart or Reconnect', async ({ page }) => {
  await setup(page, '')
  await expect(page.getByRole('button', { name: /^(Restart|Reconnect)$/ })).toHaveCount(0)
  await expect(page.getByRole('complementary', { name: 'Session details' })).toContainText('process state is unknown')
  await setup(page, 'restart', false)
  await expect(page.getByRole('button', { name: /^(Restart|Reconnect)$/ })).toHaveCount(0)
})

for (const theme of ['light', 'dark']) for (const width of [390, 1024, 1440]) {
  test(`recovery actions stay still ${width} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    const fixture = await setup(page, 'restart', true, true)
    await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
    const panel = page.getByRole('complementary', { name: 'Session details' })
    let restart = panel.getByRole('button', { name: 'Restart', exact: true })
    if (width === 390) {
      const more = panel.getByRole('button', { name: 'More session controls', exact: true })
      await more.click()
      restart = page.getByRole('menuitem', { name: 'Restart', exact: true })
      await expect(restart).toBeVisible()
      // The pointer can move between menu entries without changing geometry.
      const guard = await controlStability(page, { restart, remove: page.getByRole('menuitem', { name: `Remove ${fixture.label}`, exact: true }) })
      await guard.check(() => restart.hover())
      guard.done()
      // Picking an action closes the menu. Its trigger and the panel controls
      // remain on screen throughout the request and result transitions.
      const requestControls = await controlStability(page, {
        more, close: panel.getByRole('button', { name: 'Close session details' }),
        overview: panel.getByRole('tab', { name: 'Overview', exact: true }),
        messages: panel.getByRole('tab', { name: /Messages/ }),
      })
      await requestControls.check(() => restart.click())
      await expect(restart).toBeHidden()
      await expect(page.getByText(/Restart requested. Waiting/)).toBeVisible()
      expect(fixture.request()?.action).toBe('restart')
      fixture.complete('continuation_queued')
      await requestControls.check(async () => { await page.clock.runFor(1100); await expect(page.getByText(/Continuation queued for normal dispatch/)).toBeVisible() })
      requestControls.done()
    } else {
      const guard = await controlStability(page, { restart, frame: panel.getByRole('heading').first() })
      await guard.check(() => restart.click()); guard.done()
      await expect(page.getByText(/Restart requested. Waiting/)).toBeVisible()
      expect(fixture.request()?.action).toBe('restart')
    }
    const controls = await controlStability(page, {
      close: panel.getByRole('button', { name: 'Close session details' }),
      overview: panel.getByRole('tab', { name: 'Overview', exact: true }),
      messages: panel.getByRole('tab', { name: /Messages/ }),
      managed: panel.locator('.managed-controls'),
    })
    await controls.check(() => fixture.diagnosis('Reporting is current.'))
    await controls.check(() => fixture.diagnosis('The paired host is reporting, but this exact attached session has not supplied current heartbeat and inbox evidence. Reconnect waits for the daemon to verify the existing live hook binding without changing any external process or credential.'))
    controls.done()
    expect(await panel.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
    await page.screenshot({ path: `/private/tmp/claude-501/-Users-markus-Code-aithema/af3ab8bf-63f6-4fb5-bccf-086eb11c043e/scratchpad/aeon/shots/aeon-731-recover/recovery-${width}-${theme}.png`, fullPage: true })
  })
}
