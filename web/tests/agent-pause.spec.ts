// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page, type Locator } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { agentData, mockAgents, type AgentWorld } from './agents-fixtures'
import { pairingView } from './agent-pairing-fixtures'
import { controlStability } from './control-stability'
const NOW = Date.parse('2026-10-02T09:30:00Z')
const world: AgentWorld = { me: me.id, now: NOW, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' }, nodes: { 'p-pharos': { key: 'PRJ-17', title: 'Pharos' }, 'p-aeon': { key: 'PRJ-35', title: 'Aeon' }, 'p-frozen': { key: 'PRJ-26', title: 'Studio infrastructure' }, 'n-1': { key: 'PHAROS-11', title: 'Provisioning' }, 'n-2': { key: 'PHAROS-12', title: 'Restore' }, 'n-a1': { key: 'AEON-1', title: 'Foundation' } } }
async function setup(page: Page, readonly = false, configure?: (data: ReturnType<typeof agentData>) => void) {
  await page.clock.setSystemTime(NOW)
  await mockWork(page, fixtures(), { admin: !readonly, readOnly: readonly })
  const data = agentData(world), calls: { path: string; body: Record<string, unknown> }[] = [], deletes: string[] = []
  data.approvals = []; data.messages = []; data.accounts = []
  data.sessions = data.sessions.filter(s => s.phase !== 'stopped').slice(0, 3)
  for (const [index, s] of data.sessions.entries()) Object.assign(s, { owner_principal_id: me.id, host: index < 2 ? 'studio' : 'build', display_label: `worker-${index + 1}`, advertised_capabilities: index === 2 ? [] : ['pause', 'inbox'], supported_pause_levels: index === 2 ? ['stop_now'] : ['stop_now', 'pause_quickly', 'pause', 'wrap_up'], row_version: 1, pause_progress: { reported_at: new Date(NOW).toISOString(), next_point_in_min: 3, next_point: 'after tests', finish_in_min: 6, finish_outcome: 'finishes the ticket', interrupt: true }, phase: 'working', stopped_at: null, parent_harness_session_id: null })
  configure?.(data)
  await mockAgents(page, data)
  let leaving: Record<string, unknown> = { deadline_at: null, request_id: null, hosts: 'all', stop_in_flight: false }, ids: string[] = []
  await page.route('**/api/me/host-labels', route => route.fulfill({ json: [{ host: 'studio', label: 'Markus’s studio' }, { host: 'build', label: 'Build machine' }] }))
  await page.route('**/api/me/agent-pause-settings', route => route.fulfill({ json: { default_level: route.request().method() === 'PUT' ? route.request().postDataJSON().default_level : 'pause' } }))
  await page.route('**/api/settings/eta-interval', route => route.fulfill({ json: { interval_minutes: 10 } }))
  await page.route('**/api/me/leaving-at', async route => {
    const method = route.request().method()
    if (method === 'PUT') {
      const body = route.request().postDataJSON(); calls.push({ path: 'leaving', body })
      ids = data.sessions.filter(s => s.owner_principal_id === me.id && s.phase !== 'stopped' && (body.agents ? body.agents.includes(s.id) : body.hosts === 'all' || body.hosts.includes(s.host))).map(s => s.id as string)
      leaving = { ...body, agents: ids, request_id: 'wind-request', stop_in_flight: false }
      for (const s of data.sessions.filter(s => ids.includes(s.id as string))) Object.assign(s, { row_version: Number(s.row_version) + 1, pause: { control_id: `pause-${s.id}`, state: 'requested', level: 'pause', requested_at: new Date(NOW).toISOString(), deadline_at: body.deadline_at, deliver: true, leaving_request_id: 'wind-request' } })
    } else if (method === 'DELETE') { deletes.push(String(leaving.request_id)); leaving = { deadline_at: null, request_id: null, hosts: 'all', stop_in_flight: false }; for (const s of data.sessions) { const p = s.pause as Record<string, unknown> | undefined; if (p?.state === 'requested') Object.assign(s, { row_version: Number(s.row_version) + 1, pause: { ...p, state: 'cancelled' } }) }; ids = [] }
    return route.fulfill({ json: { ...leaving, owner_principal_id: me.id, items: data.sessions.filter(s => ids.includes(s.id as string)) } })
  })
  await page.route(/\/api\/projects\/[^/]+\/harness-sessions\/[^/]+\/(pause|resume)$/, async route => {
    const path = new URL(route.request().url()).pathname, id = path.split('/').at(-2)!, s = data.sessions.find(s => s.id === id)!, body = route.request().postDataJSON(); calls.push({ path, body })
    if (path.endsWith('/resume')) { Object.assign(s, { row_version: Number(s.row_version) + 1, pause: { ...(s.pause as object), state: 'resume_requested' } }); return route.fulfill({ json: { session: s, continuation: { brief: 'Saved handover continuation' } } }) }
    Object.assign(s, { row_version: Number(s.row_version) + 1, pause: { control_id: `pause-${id}`, state: body.level === 'stop_now' ? 'cancelled' : 'requested', level: body.level, note: body.note, requested_at: new Date(NOW).toISOString(), deadline_at: new Date(NOW + 600000).toISOString(), deliver: true, stop_requested: body.level === 'stop_now' } })
    return route.fulfill({ json: s })
  })
  return { data, calls, deletes }
}
async function bounds(locator: Locator) { const b = await locator.boundingBox(); expect(b).not.toBeNull(); return b! }
async function stable(locator: Locator, before: Awaited<ReturnType<typeof bounds>>) { const after = await bounds(locator); for (const key of ['x', 'y', 'width', 'height'] as const) expect(Math.abs(after[key] - before[key]), key).toBeLessThan(1) }
async function screenshot(page: Page, name: string) { if (!process.env.AEON524_SHOTS) return; mkdirSync(process.env.AEON524_SHOTS, { recursive: true }); await page.screenshot({ path: join(process.env.AEON524_SHOTS, `${name}.png`), fullPage: true }) }
for (const width of [1440, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`${width} ${theme}: scope, preview, levels and report controls stay under the pointer`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 }); await page.emulateMedia({ colorScheme: theme })
    const errors = watchErrors(page), mock = await setup(page)
    await page.goto('/agents'); await expect(page.locator('.wind-row')).toBeVisible()
    const sw = page.getByRole('switch', { name: 'Wind down', exact: true }), scope = page.locator('.scope-button'), minutes = page.getByRole('combobox', { name: 'Wind-down time' })
    // Put the whole control row under the pointer before measuring: visibility
    // alone also matches rows below the viewport, which clicks must scroll to.
    await page.locator('.wind-row').evaluate(el => el.scrollIntoView({ block: 'center' }))
    await expect(sw).toBeInViewport({ ratio: 1 }); await expect(minutes).toBeInViewport({ ratio: 1 })
    const positions = [await bounds(sw), await bounds(scope), await bounds(minutes)]
    await expect(page.locator('.time-value')).toHaveText('15 min')
    await expect(minutes.locator('option[value="15"]')).toHaveText(/^15 min · by \d{1,2}:\d{2}(?:\s?[AP]M)?$/)
    await expect(page.locator('.wind-timing .by')).toHaveText(/^by \d{1,2}:\d{2}(?:\s?[AP]M)?$/)
    await sw.check(); await expect(page.locator('.wind-confirm')).toBeVisible(); expect(mock.calls).toHaveLength(0)
    await stable(sw, positions[0]!); await stable(scope, positions[1]!); await stable(minutes, positions[2]!)
    const confirm = page.locator('.wind-confirm'), confirmBounds = await bounds(confirm)
    await minutes.selectOption('5'); await stable(confirm, confirmBounds); await minutes.selectOption('custom'); await page.getByRole('spinbutton', { name: 'Custom minutes' }).fill('15'); await stable(confirm, confirmBounds)
    await page.getByRole('spinbutton', { name: 'Custom minutes' }).press('Escape'); await expect(confirm).toBeVisible()
    await page.keyboard.press('Escape'); await expect(confirm).toHaveCount(0); await sw.check(); await stable(confirm, confirmBounds)
    await scope.click(); const picker = page.getByRole('dialog', { name: 'Hosts to wind down' }); await expect(picker).toBeVisible()
    await expect(picker.getByText('This computer', { exact: true })).toHaveCount(0)
    await expect(picker.getByRole('checkbox').first()).toContainText('Markus’s studio')
    const header = picker.locator('.picker-head'), footer = picker.locator('.picker-foot'), headBounds = await bounds(header), footBounds = await bounds(footer)
    await picker.getByRole('button', { name: 'None', exact: true }).click(); await stable(header, headBounds); await stable(footer, footBounds)
    await picker.getByRole('checkbox').filter({ hasText: 'worker-1' }).click(); await expect(picker.getByRole('checkbox').filter({ hasText: 'Markus’s studio' }).first()).toHaveAttribute('aria-checked', 'mixed')
    await picker.getByRole('radio', { name: 'State', exact: true }).click(); await stable(header, headBounds); await stable(footer, footBounds)
    await expect(picker.getByRole('heading')).toHaveText(['Hosts and agents', 'Running agents'])
    await expect(picker.getByRole('checkbox').filter({ hasText: 'worker-1' })).toHaveAttribute('aria-checked', 'true')
    await picker.getByRole('button', { name: /^Done/ }).click()
    await expect(scope).toHaveText('1 agent on Markus’s studio'); await stable(scope, positions[1]!); await stable(confirm, confirmBounds)
    await scope.click()
    await picker.getByRole('radio', { name: 'Host', exact: true }).click(); await picker.getByRole('checkbox').filter({ hasText: 'Markus’s studio' }).first().click()
    await picker.getByRole('button', { name: /^Done/ }).click(); await stable(scope, positions[1]!); await stable(confirm, confirmBounds)
    await screenshot(page, `${width}-${theme}-preview`)
    await confirm.click(); await expect.poll(() => mock.calls.length).toBe(1)
    expect(mock.calls[0]?.body.hosts).toEqual(['studio']); expect(mock.calls[0]?.body.agents).toHaveLength(2)
    await stable(sw, positions[0]!); await stable(scope, positions[1]!); await stable(minutes, positions[2]!)
    await scope.click(); await expect(picker.getByRole('checkbox').first()).toHaveAttribute('aria-disabled', 'true'); await picker.press('Escape')
    await sw.uncheck(); await expect(page.locator('.wind-confirm')).toHaveCount(0)
    // Withdrawal refreshes the session list; wait for its control eligibility.
    await expect(page.getByRole('button', { name: 'Pause worker-1', exact: true })).toBeVisible()
    await page.locator('.bulk-tools').getByRole('button', { name: 'Pause all…', exact: true }).click()
    const dialog = page.getByRole('dialog', { name: 'Pause all', exact: true }); await expect(dialog).toBeVisible()
    const action = dialog.locator('.pause-actions'), actionBounds = await bounds(action), note = dialog.getByRole('textbox', { name: /Note for/ }), noteBounds = await bounds(note)
    const submit = action.locator('[data-submit]'), cancel = action.getByRole('button', { name: /^Cancel/ }), submitBounds = await bounds(submit), cancelBounds = await bounds(cancel)
    for (const level of ['Pause quickly', 'Wrap up', 'Stop now', 'Pause']) { await dialog.locator(`[data-level="${({ 'Pause quickly': 'pause_quickly', 'Wrap up': 'wrap_up', 'Stop now': 'stop_now', Pause: 'pause' } as Record<string, string>)[level]}"]`).click(); await stable(action, actionBounds); await stable(submit, submitBounds); await stable(cancel, cancelBounds); await stable(note, noteBounds) }
    await dialog.getByRole('combobox', { name: 'Level for worker-1' }).selectOption('wrap_up'); await stable(action, actionBounds)
    await note.fill('Commit work in progress before handing over.'); await note.press('Escape'); await expect(dialog).toBeVisible(); await dialog.press('Escape'); await expect(dialog).not.toBeVisible()
    await page.getByRole('button', { name: 'Pause worker-1', exact: true }).click()
    const single = page.getByRole('dialog', { name: 'Pause worker-1', exact: true }); await expect(single.getByRole('radio', { name: /^Stop now/ })).toHaveCount(0)
    const singleBar = single.locator('.pause-actions'), singleBounds = await bounds(singleBar)
    for (const level of ['Wrap up', 'Pause quickly', 'Pause']) { await single.locator(`[data-level="${({ 'Pause quickly': 'pause_quickly', 'Wrap up': 'wrap_up', Pause: 'pause' } as Record<string, string>)[level]}"]`).click(); await stable(singleBar, singleBounds) }
    await screenshot(page, `${width}-${theme}-pause`)
    await single.locator('[data-submit]').click(); await expect.poll(() => mock.calls.length).toBe(2)
    expect(mock.calls[1]?.body.level).toBe('pause')
    await expect(page.locator('.agents-page .row').filter({ hasText: 'worker-1' })).toContainText('Pausing')
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true); expect(errors).toEqual([])
  })
}
test('deadline alone never announces completion; reload shows durable handovers and resume remains a request', async ({ page }) => {
  const mock = await setup(page); await page.goto('/agents'); const sw = page.getByRole('switch', { name: 'Wind down', exact: true })
  await sw.check(); await page.locator('.wind-confirm').click(); await expect.poll(() => mock.calls.length).toBe(1)
  await page.clock.setSystemTime(NOW + 20 * 60_000); await page.clock.runFor(1000)
  await expect(page.getByText('Deadline reached · awaiting reported outcomes', { exact: true })).toBeVisible(); await expect(page.getByText(/Wind-down done at/)).toHaveCount(0)
  for (const s of mock.data.sessions) Object.assign(s, { row_version: Number(s.row_version) + 1, phase: 'stopped', stopped_at: new Date(NOW + 15 * 60_000).toISOString(), stop_reason: 'paused', pause: { ...(s.pause as object), state: 'paused', handover: { state: 'Tests passed; work committed.', next_steps: ['Continue the feature'], open_questions: ['Which migration?'], worktree_state: 'committed', commit_sha: '1234567' } } })
  await page.reload(); await expect(page.getByRole('region', { name: 'Wind down agents' }).getByText(/Wind-down done at/)).toBeVisible()
  await expect.poll(() => page.locator('.row .c-actions').evaluateAll(cells => cells.every(cell => { const box = cell.getBoundingClientRect(); return [...cell.children].every(child => { const rect = child.getBoundingClientRect(); return rect.left >= box.left - 1 && rect.right <= box.right + 1 }) }))).toBe(true)
  await page.locator('.bulk-tools').getByRole('button', { name: 'Resume all…', exact: true }).click(); const dialog = page.getByRole('dialog', { name: 'Resume all', exact: true })
  const bar = dialog.locator('.pause-actions'), before = await bounds(bar); await dialog.getByRole('button', { name: /Show continuation brief/ }).click(); await stable(bar, before)
  await dialog.getByRole('checkbox', { name: 'Resume worker-3', exact: true }).uncheck(); await stable(bar, before)
  await expect(dialog).toContainText('Tests passed; work committed.'); await dialog.locator('[data-submit]').click()
  await expect.poll(() => mock.calls.filter(c => c.path.endsWith('/resume')).length).toBe(2); await expect(page.getByText(/resume requests saved · awaiting continuation/)).toBeVisible()
})
test('State groups running agents, idle hosts and offline hosts in that order', async ({ page }) => {
  const mock = await setup(page)
  const computers = [
    pairingView({ computer_name: 'idle', computer_id: 'd0000000-0000-4000-8000-000000000001', state: 'redeemed', computer_state: 'connected', connectivity: 'online', setup_state: 'connected' }),
    pairingView({ computer_name: 'offline', computer_id: 'd0000000-0000-4000-8000-000000000002', state: 'redeemed', computer_state: 'connected', connectivity: 'offline', setup_state: 'connected' }),
  ]
  await page.route('**/api/agent-pairing/computers', route => route.fulfill({ json: { computers } }))
  await page.goto('/agents'); await page.locator('.scope-button').click()
  const picker = page.getByRole('dialog', { name: 'Hosts to wind down' })
  await expect(picker.getByRole('checkbox').filter({ hasText: 'idle' }).first()).toBeVisible()
  await picker.getByRole('button', { name: 'None', exact: true }).click()
  await picker.getByRole('checkbox').filter({ hasText: 'worker-1' }).click()
  await picker.getByRole('radio', { name: 'State', exact: true }).click()
  await expect(picker.getByRole('heading')).toHaveText(['Hosts and agents', 'Running agents', 'Idle hosts', 'Offline hosts'])
  await expect(picker.getByRole('checkbox').filter({ hasText: 'worker-1' })).toContainText('Markus’s studio')
  await expect(picker.getByRole('checkbox').filter({ hasText: 'worker-1' })).toHaveAttribute('aria-checked', 'true')
  await picker.getByRole('checkbox').filter({ hasText: 'offline' }).click()
  await picker.getByRole('radio', { name: 'Host', exact: true }).click()
  await expect(picker.getByRole('checkbox').filter({ hasText: 'offline' }).first()).toHaveAttribute('aria-checked', 'true')
  for (const s of mock.data.sessions) Object.assign(s, { phase: 'stopped', stopped_at: new Date(NOW).toISOString(), row_version: Number(s.row_version) + 1 })
  await page.reload(); await page.locator('.scope-button').click()
  await picker.getByRole('radio', { name: 'State', exact: true }).click()
  await expect(picker.getByRole('heading')).toHaveText(['Hosts and agents', 'Idle hosts', 'Offline hosts'])
})
test('wind-down previews only owned work and a reloaded all-host report leaves new agents unticked', async ({ page }) => {
  const mock = await setup(page, false, data => {
    data.sessions[1]!.owner_principal_id = 'other-person'
    data.sessions[2]!.owner_principal_id = null
  })
  await page.route('**/api/me', route => route.fulfill({ json: { principal: { id: '21111111-1111-4111-8111-111111111111', name: me.name, kind: 'person', roles: ['admin'] }, tenant: { id: 't1', name: 'INSPR Studio' } } }))
  let release!: () => void, observed!: () => void, waiting = true
  const barrier = new Promise<void>(resolve => { release = resolve }), requested = new Promise<void>(resolve => { observed = resolve })
  await page.route('**/api/me/leaving-at', async route => {
    if (waiting && route.request().method() === 'GET') {
      waiting = false; observed(); await barrier
      return route.fulfill({ json: { owner_principal_id: me.id, deadline_at: null, request_id: null, hosts: 'all', agents: [], stop_in_flight: false, items: [] } })
    }
    return route.fallback()
  })
  await page.goto('/agents'); await page.getByRole('switch', { name: 'Wind down', exact: true }).check()
  await requested
  await expect(page.locator('.wind-confirm')).toBeDisabled()
  await expect(page.locator('.plan-summary')).toHaveText('Reading owned sessions…')
  release()
  await expect(page.locator('.wind-plan .plan-agent')).toHaveCount(1)
  await expect(page.locator('.plan-summary')).toHaveText('0 hand over · 1 finish · 0 stop, no handover')
  await page.locator('.scope-button').click()
  const picker = page.getByRole('dialog', { name: 'Hosts to wind down' })
  await expect(picker.locator('.picker-foot')).toContainText('1 of 1 agent · 1 of 1 host')
  await expect(picker.getByRole('checkbox').filter({ hasText: 'worker-2' })).toHaveCount(0)
  await picker.press('Escape'); await page.locator('.wind-confirm').click()
  expect(mock.calls[0]?.body).toMatchObject({ hosts: 'all' }); expect(mock.calls[0]?.body.agents).toBeUndefined()
  mock.data.sessions.push({ ...mock.data.sessions[0]!, id: 'new-owned-session', display_label: 'new-worker', pause: undefined })
  await page.reload(); await page.locator('.scope-button').click()
  await expect(picker.getByRole('checkbox').filter({ hasText: 'worker-1' })).toHaveAttribute('aria-checked', 'true')
  await expect(picker.getByRole('checkbox').filter({ hasText: 'new-worker' })).toHaveAttribute('aria-checked', 'false')
  await expect(picker.getByRole('checkbox').filter({ hasText: 'Markus’s studio' }).first()).toHaveAttribute('aria-checked', 'mixed')
  await picker.press('Escape')
  await expect(page.locator('.wind-plan .plan-agent')).toHaveCount(1)
  await expect(page.locator('.wind-plan .untouched')).toContainText('new-worker')
  await expect(page.locator('.wind-plan')).not.toContainText('worker-2')
})
test('dismissing a completed report before its deadline has no withdrawal toast or Undo', async ({ page }) => {
  const mock = await setup(page)
  await page.goto('/agents'); await page.getByRole('switch', { name: 'Wind down', exact: true }).check(); await page.locator('.wind-confirm').click()
  await expect.poll(() => mock.calls.length).toBe(1)
  for (const s of mock.data.sessions) Object.assign(s, { row_version: Number(s.row_version) + 1, phase: 'stopped', stopped_at: new Date(NOW + 5 * 60_000).toISOString(), stop_reason: 'paused', pause: { ...(s.pause as object), state: 'paused' } })
  await page.clock.setSystemTime(NOW + 5 * 60_000); await page.reload()
  await page.getByRole('region', { name: 'Wind down agents' }).getByRole('button', { name: 'Dismiss', exact: true }).click()
  await expect.poll(() => mock.deletes).toEqual(['wind-request'])
  await expect(page.getByRole('switch', { name: 'Wind down', exact: true })).not.toBeChecked()
  await expect(page.getByText(/Pending wind-down requests withdrawn/)).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Undo', exact: true })).toHaveCount(0)
  expect(mock.calls).toHaveLength(1)
})
test('without control permission pause actions and wind-down are hidden', async ({ page }) => { await setup(page, true); await page.goto('/agents'); await expect(page.locator('.agents-page .row').first()).toBeVisible(); await expect(page.getByRole('switch', { name: 'Wind down', exact: true })).toHaveCount(0); await expect(page.getByRole('button', { name: /^Pause worker/ })).toHaveCount(0) })

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
  test(`${width} ${theme}: stop request stays visible until the worker confirms stopped`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
    const errors = watchErrors(page)
    const mock = await setup(page, false, data => {
      data.sessions = [data.sessions[0]!]
      Object.assign(data.sessions[0]!, { management_mode: 'unmanaged', run_id: null, ticket_node_id: null, ticket: null, activity: 'busy', heartbeat_at: new Date(NOW).toISOString(), display_label: 'Datenbankänderungen und Wiederherstellungsprüfung abschließen' })
    })
    const session = mock.data.sessions[0]!
    await page.goto(`/agents/${session.id}`)
    const row = page.locator(`[data-row="s:${session.id}"]`)
    await expect(row.locator('.c-state .state-word')).toHaveText('Working')
    const guard = await controlStability(page, { row, menu: row.locator('.more') })
    await guard.check(async () => {
      await page.getByRole('complementary', { name: 'Session details' }).getByRole('button', { name: 'Stop now…', exact: true }).click()
      const dialog = page.getByRole('dialog', { name: /^Stop now / })
      await dialog.locator('[data-submit]').click()
      await expect(dialog).not.toBeVisible()
      await expect(row.locator('.c-state .state-word')).toHaveText('Stop requested')
      expect(mock.calls).toHaveLength(1)
      expect(mock.calls[0]?.body.level).toBe('stop_now')
      expect(mock.calls[0]?.path).toContain(`/harness-sessions/${session.id}/pause`)
      expect(session.stopped_at).toBeNull()
    })
    await row.screenshot({ path: testInfo.outputPath(`stop-requested-${width}-${theme}.png`) })
    await guard.check(async () => {
      Object.assign(session, { row_version: Number(session.row_version) + 1, phase: 'stopped', stopped_at: new Date(NOW).toISOString(), stop_reason: 'stopped' })
      await page.evaluate(() => window.dispatchEvent(new Event('online')))
      await expect(row.locator('.c-state .state-word')).toHaveText('Stopped')
    })
    guard.done()
    await row.screenshot({ path: testInfo.outputPath(`stopped-${width}-${theme}.png`) })
    await page.reload()
    await expect(row.locator('.c-state .state-word')).toHaveText('Stopped')
    expect(errors).toEqual([])
  })
}
