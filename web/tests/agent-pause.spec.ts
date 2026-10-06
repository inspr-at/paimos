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
const pageNow = (page: Page) => page.evaluate(() => Date.now())
const clockOf = (page: Page, ms: number) => page.evaluate(t => { const d = new Date(t); return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}` }, ms)
const head = (page: Page) => page.locator('.agents-page .page-head')
const windButton = (page: Page) => head(page).getByRole('button', { name: 'Wind down', exact: true })
const chipButton = (page: Page) => head(page).getByRole('button', { name: /^(Winding down|Wound down)/ })
const formPopover = (page: Page) => page.getByRole('dialog', { name: 'Wind down', exact: true })
const statusPopover = (page: Page) => page.getByRole('dialog', { name: 'Wind-down', exact: true })
async function startWindDown(page: Page) { await windButton(page).click(); await formPopover(page).locator('button[type=submit]').click() }
for (const width of [1440, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`${width} ${theme}: the Wind down popover keeps its controls still, starts, shows and cancels a wind-down`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 }); await page.emulateMedia({ colorScheme: theme })
    const errors = watchErrors(page), mock = await setup(page)
    await page.goto('/agents')
    const open = windButton(page); await expect(open).toBeVisible()
    if (width === 390) { await expect(open.locator('.wl')).toBeHidden(); expect((await bounds(open)).width).toBeGreaterThanOrEqual(44); expect((await bounds(open)).height).toBeGreaterThanOrEqual(44) } else await expect(open.locator('.wl')).toBeVisible()
    await open.click()
    const pop = formPopover(page); await expect(pop).toBeVisible(); await expect(open).toHaveAttribute('aria-expanded', 'true')
    const where = pop.getByLabel('Where'), by = pop.getByLabel('By', { exact: true }), seg = pop.getByRole('radiogroup', { name: 'Done in' }), go = pop.locator('button[type=submit]'), quit = pop.getByRole('button', { name: /^Cancel/ })
    await expect(where).toBeFocused()
    await expect(where.locator('option')).toHaveText(['All computers · 3 agents', 'Markus’s studio · 2 agents', 'Build machine · 1 agent', 'Chosen agents…'])
    await expect(seg.getByRole('radio', { name: '15 min', exact: true })).toHaveAttribute('aria-checked', 'true')
    await expect(by).toHaveValue(await clockOf(page, await pageNow(page) + 15 * 60_000))
    await screenshot(page, `${width}-${theme}-form`)
    // Presets, typed times, errors and the preview never move the controls.
    const guard = await controlStability(page, { open, where, seg, by, go, quit })
    await guard.check(async () => { await seg.getByRole('radio', { name: '5 min', exact: true }).click(); await expect(seg.getByRole('radio', { name: '5 min', exact: true })).toHaveAttribute('aria-checked', 'true') })
    await guard.check(async () => { await by.fill(await clockOf(page, await pageNow(page) + 40 * 60_000)); await expect(seg.getByRole('radio', { checked: true })).toHaveCount(0); await expect(pop.getByText('in 40 min', { exact: true }).or(pop.getByText('in 41 min', { exact: true }))).toBeVisible() })
    await guard.check(async () => { await by.fill('25:00'); await expect(by).toHaveAttribute('aria-invalid', 'true'); await expect(pop.locator('#wd-hint')).toHaveText('Type a time like 19:44.') })
    await guard.check(async () => { await by.fill(await clockOf(page, await pageNow(page) + 60_000)); await expect(pop.locator('#wd-hint')).toContainText('Pick a time between') })
    await guard.check(async () => { await by.fill(await clockOf(page, await pageNow(page) + 13 * 60 * 60_000)); await expect(by).toHaveAttribute('aria-invalid', 'true') })
    await guard.check(async () => { await go.click(); await expect(by).toBeFocused() })
    expect(mock.calls).toHaveLength(0)
    await guard.check(async () => { await seg.getByRole('radio', { name: '30 min', exact: true }).click(); await expect(by).not.toHaveAttribute('aria-invalid', 'true') })
    await guard.check(async () => { await where.selectOption({ label: 'Markus’s studio · 2 agents' }); await expect(pop.locator('.wdf-prev')).toContainText('2 agents on Markus’s studio') })
    guard.done()
    // Chosen agents opens a checklist below the selector; the selector stays where it is.
    const whereBounds = await bounds(where), openBounds = await bounds(open)
    await where.selectOption('pick'); const picks = pop.getByRole('group', { name: 'Agents to wind down' }).getByRole('checkbox')
    await expect(picks).toHaveCount(3); await stable(where, whereBounds); await stable(open, openBounds)
    await picks.nth(2).uncheck(); await expect(pop.locator('.wdf-prev')).toContainText('2 agents you chose')
    await pop.getByRole('group', { name: 'Agents to wind down' }).getByRole('checkbox').first().uncheck(); await pop.getByRole('group', { name: 'Agents to wind down' }).getByRole('checkbox').nth(1).uncheck()
    await go.click(); await expect(pop.locator('#wd-hint')).toHaveText('Choose at least one agent.'); expect(mock.calls).toHaveLength(0)
    await picks.nth(0).check(); await picks.nth(1).check()
    await screenshot(page, `${width}-${theme}-pick`)
    // Escape leaves the field first, then closes; the keyboard submits.
    await by.focus(); await page.keyboard.press('Escape'); await expect(pop).toBeVisible(); await expect(by).not.toBeFocused()
    await where.selectOption({ label: 'Markus’s studio · 2 agents' }); await seg.getByRole('radio', { name: '30 min', exact: true }).click()
    await page.keyboard.press('Escape'); await page.keyboard.press('Escape'); await expect(pop).toHaveCount(0); await expect(open).toBeFocused()
    await open.click(); await expect(where).toHaveValue('all')
    await where.selectOption({ label: 'Markus’s studio · 2 agents' }); await seg.getByRole('radio', { name: '30 min', exact: true }).click()
    const expectedBy = await by.inputValue()
    await by.focus(); await page.keyboard.press('ControlOrMeta+Enter')
    await expect.poll(() => mock.calls.length).toBe(1)
    expect(mock.calls[0]?.body.hosts).toEqual(['studio']); expect(mock.calls[0]?.body.agents).toHaveLength(2)
    expect(Date.parse(String(mock.calls[0]?.body.deadline_at)) - await pageNow(page)).toBeGreaterThan(29 * 60_000)
    // The control becomes the chip; focus follows it.
    const chip = chipButton(page); await expect(chip).toBeVisible(); await expect(chip).toBeFocused()
    await expect(chip).toContainText(`Winding down`); await expect(open).toHaveCount(0)
    if (width > 720) await expect(chip).toContainText(`· 2 left · by ${expectedBy}`)
    expect((await bounds(chip)).height).toBeGreaterThanOrEqual(width === 390 ? 44 : 30)
    await chip.click(); const status = statusPopover(page); await expect(status).toBeVisible()
    await expect(status.getByRole('heading')).toContainText(`Winding down · by ${expectedBy}`)
    await expect(status).toContainText('0 of 2 handed over'); await expect(status.getByText('Markus’s studio', { exact: true })).toBeVisible()
    await expect(status.getByText('Keep running, untouched')).toBeVisible(); await expect(status.locator('.wd-un')).toContainText('worker-3')
    await screenshot(page, `${width}-${theme}-status`)
    // Cancel from the status popover withdraws, with Undo.
    await status.getByRole('button', { name: 'Cancel the wind-down', exact: true }).click()
    await expect.poll(() => mock.deletes).toEqual(['wind-request']); await expect(page.getByRole('button', { name: 'Undo', exact: true })).toBeVisible()
    await expect(windButton(page)).toBeVisible(); await expect(windButton(page)).toBeFocused()
    // The remembered Where and Done in come back for this person.
    await windButton(page).click(); await expect(where).toHaveValue('h:studio'); await expect(seg.getByRole('radio', { name: '30 min', exact: true })).toHaveAttribute('aria-checked', 'true')
    await page.keyboard.press('Escape'); await page.keyboard.press('Escape'); await expect(pop).toHaveCount(0)
    // The × on the chip cancels in one step.
    await startWindDown(page); await expect(chipButton(page)).toBeVisible()
    await head(page).getByRole('button', { name: 'Cancel the wind-down', exact: true }).click()
    await expect.poll(() => mock.deletes.length).toBe(2); await expect(windButton(page)).toBeFocused()
    // Pause all… and Resume all… sit under "Right now" in the same popover.
    await expect(page.getByRole('button', { name: 'Pause worker-1', exact: true })).toBeVisible()
    await windButton(page).click(); const rightNow = formPopover(page)
    await expect(rightNow.getByRole('button', { name: /^Resume all…/ })).toHaveAttribute('aria-disabled', 'true'); await expect(rightNow).toContainText('Nothing is paused.')
    await rightNow.getByRole('button', { name: /^Pause all…/ }).click(); await expect(rightNow).toHaveCount(0)
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
    await single.locator('[data-submit]').click(); await expect.poll(() => mock.calls.length).toBe(3)
    expect(mock.calls[2]?.body.level).toBe('pause')
    await expect(page.locator('.agents-page .row').filter({ hasText: 'worker-1' })).toContainText('Pausing')
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true); expect(errors).toEqual([])
  })
}

test('deadline alone never announces completion; reload shows durable handovers and resume remains a request', async ({ page }) => {
  const mock = await setup(page); await page.goto('/agents')
  await startWindDown(page); await expect.poll(() => mock.calls.length).toBe(1)
  await page.clock.setSystemTime(NOW + 20 * 60_000); await page.clock.runFor(1000)
  const chip = chipButton(page); await expect(chip).toContainText('Winding down'); await expect(chip).toContainText('deadline reached')
  await chip.click(); await expect(statusPopover(page)).toContainText('deadline reached, awaiting reported outcomes'); await expect(page.getByText(/Wound down at/)).toHaveCount(0)
  await page.keyboard.press('Escape')
  for (const s of mock.data.sessions) Object.assign(s, { row_version: Number(s.row_version) + 1, phase: 'stopped', stopped_at: new Date(NOW + 15 * 60_000).toISOString(), stop_reason: 'paused', pause: { ...(s.pause as object), state: 'paused', handover: { state: 'Tests passed; work committed.', next_steps: ['Continue the feature'], open_questions: ['Which migration?'], worktree_state: 'committed', commit_sha: '1234567' } } })
  await page.reload(); await expect(chipButton(page)).toContainText('Wound down'); await chipButton(page).click()
  await expect(statusPopover(page).getByRole('heading')).toContainText(/Wound down at/)
  await expect.poll(() => page.locator('.row .c-actions').evaluateAll(cells => cells.every(cell => { const box = cell.getBoundingClientRect(); return [...cell.children].every(child => { const rect = child.getBoundingClientRect(); return rect.left >= box.left - 1 && rect.right <= box.right + 1 }) }))).toBe(true)
  // A finished wind-down keeps Pause all… and Resume all… one click away.
  await statusPopover(page).getByRole('button', { name: /^Resume all…/ }).click(); const dialog = page.getByRole('dialog', { name: 'Resume all', exact: true })
  const bar = dialog.locator('.pause-actions'), before = await bounds(bar); await dialog.getByRole('button', { name: /Show continuation brief/ }).click(); await stable(bar, before)
  await dialog.getByRole('checkbox', { name: 'Resume worker-3', exact: true }).uncheck(); await stable(bar, before)
  await expect(dialog).toContainText('Tests passed; work committed.'); await dialog.locator('[data-submit]').click()
  await expect.poll(() => mock.calls.filter(c => c.path.endsWith('/resume')).length).toBe(2); await expect(page.getByText(/resume requests saved · awaiting continuation/)).toBeVisible()
})
test('Where lists computers without running agents and refuses them instead of starting nothing', async ({ page }) => {
  const mock = await setup(page)
  const computers = [
    pairingView({ computer_name: 'idle', computer_id: 'd0000000-0000-4000-8000-000000000001', state: 'redeemed', computer_state: 'connected', connectivity: 'online', setup_state: 'connected' }),
    pairingView({ computer_name: 'offline', computer_id: 'd0000000-0000-4000-8000-000000000002', state: 'redeemed', computer_state: 'connected', connectivity: 'offline', setup_state: 'connected' }),
  ]
  await page.route('**/api/agent-pairing/computers', route => route.fulfill({ json: { computers } }))
  await page.goto('/agents'); await windButton(page).click()
  const pop = formPopover(page), where = pop.getByLabel('Where')
  await expect(where.locator('option')).toHaveText(['All computers · 3 agents', 'Markus’s studio · 2 agents', 'Build machine · 1 agent', 'idle · 0 agents', 'offline · 0 agents', 'Chosen agents…'])
  await where.selectOption({ label: 'idle · 0 agents' }); await pop.locator('button[type=submit]').click()
  await expect(pop.locator('#wd-hint')).toHaveText('Nothing is running on that computer.'); expect(mock.calls).toHaveLength(0)
  for (const s of mock.data.sessions) Object.assign(s, { phase: 'stopped', stopped_at: new Date(NOW).toISOString(), row_version: Number(s.row_version) + 1 })
  await page.reload(); await windButton(page).click()
  await expect(formPopover(page).getByLabel('Where').locator('option')).toHaveText(['All computers · 0 agents', 'idle · 0 agents', 'Build machine · 0 agents', 'Markus’s studio · 0 agents', 'offline · 0 agents', 'Chosen agents…'])
  await formPopover(page).locator('button[type=submit]').click(); await expect(formPopover(page).locator('#wd-hint')).toHaveText('Nothing is running.')
})
test('wind-down previews only owned work and a reloaded all-computer report leaves new agents untouched', async ({ page }) => {
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
  await page.goto('/agents'); await windButton(page).click()
  const pop = formPopover(page), submit = pop.locator('button[type=submit]')
  await requested
  await expect(submit).toBeDisabled(); await expect(pop.locator('#wd-hint')).toHaveText('Reading owned sessions…')
  release()
  await expect(submit).toBeEnabled()
  await expect(pop.getByLabel('Where').locator('option')).toHaveText(['All computers · 1 agent', 'Chosen agents…'])
  await expect(pop.locator('.wdf-prev')).toContainText('1 agent on all computers'); await expect(pop.locator('.wdf-prev')).toContainText('0 hand over · 1 finish · 0 stop, no handover')
  await submit.click(); await expect.poll(() => mock.calls.length).toBe(1)
  expect(mock.calls[0]?.body).toMatchObject({ hosts: 'all' }); expect(mock.calls[0]?.body.agents).toBeUndefined()
  mock.data.sessions.push({ ...mock.data.sessions[0]!, id: 'new-owned-session', display_label: 'new-worker', pause: undefined })
  await page.reload(); await chipButton(page).click()
  const status = statusPopover(page)
  await expect(status.locator('.wd-plan li')).toHaveCount(1); await expect(status.locator('.wd-plan')).toContainText('worker-1')
  await expect(status.locator('.wd-un')).toContainText('new-worker'); await expect(status).not.toContainText('worker-2')
})
test('dismissing a completed report before its deadline has no withdrawal toast or Undo', async ({ page }) => {
  const mock = await setup(page)
  await page.goto('/agents'); await startWindDown(page)
  await expect.poll(() => mock.calls.length).toBe(1)
  for (const s of mock.data.sessions) Object.assign(s, { row_version: Number(s.row_version) + 1, phase: 'stopped', stopped_at: new Date(NOW + 5 * 60_000).toISOString(), stop_reason: 'paused', pause: { ...(s.pause as object), state: 'paused' } })
  await page.clock.setSystemTime(NOW + 5 * 60_000); await page.reload()
  await expect(chipButton(page)).toContainText('Wound down')
  await head(page).getByRole('button', { name: 'Dismiss', exact: true }).click()
  await expect.poll(() => mock.deletes).toEqual(['wind-request'])
  await expect(windButton(page)).toBeVisible()
  await expect(page.getByText(/Pending wind-down requests withdrawn/)).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Undo', exact: true })).toHaveCount(0)
  expect(mock.calls).toHaveLength(1)
})
test('a failed status read says so with a Retry instead of hiding the wind-down', async ({ page }) => {
  await setup(page); let failing = true
  await page.route('**/api/me/leaving-at', route => failing && route.request().method() === 'GET' ? route.fulfill({ status: 500, json: { error: 'status unavailable' } }) : route.fallback())
  await page.goto('/agents')
  const open = head(page).getByRole('button', { name: 'Wind down', exact: true }); await expect(open).toHaveAttribute('title', 'The wind-down status could not be read')
  await open.click(); const pop = formPopover(page); await expect(pop.locator('#wd-hint')).toContainText('status unavailable'); await expect(pop.locator('button[type=submit]')).toBeDisabled()
  failing = false; await pop.getByRole('button', { name: 'Retry', exact: true }).click()
  await expect(pop.locator('button[type=submit]')).toBeEnabled(); await expect(pop.locator('#wd-hint')).toHaveText('')
})
test('without control permission pause actions and wind-down are hidden', async ({ page }) => { await setup(page, true); await page.goto('/agents'); await expect(page.locator('.agents-page .row').first()).toBeVisible(); await expect(windButton(page)).toHaveCount(0); await expect(page.getByRole('button', { name: /^Pause worker/ })).toHaveCount(0) })

test('AEON-783: chosen agents keeps Done in, By, Cancel and Submit still', async ({ page }) => {
  await setup(page)
  await page.goto('/agents')
  for (const width of [1440, 390]) {
    await page.setViewportSize({ width, height: 1000 })
    const open = windButton(page)
    if (await formPopover(page).count()) await formPopover(page).getByRole('button', { name: /^Cancel/ }).click()
    await open.click()
    const pop = formPopover(page)
    const where = pop.getByLabel('Where'), by = pop.getByLabel('By', { exact: true }), seg = pop.getByRole('radiogroup', { name: 'Done in' })
    const go = pop.locator('button[type=submit]'), quit = pop.getByRole('button', { name: /^Cancel/ })
    const pauseAll = pop.getByRole('button', { name: /^Pause all…/ }), resumeAll = pop.getByRole('button', { name: /^Resume all…/ })
    const guard = await controlStability(page, { where, seg, by, go, quit, pauseAll, resumeAll })
    await guard.check(async () => { await where.selectOption({ label: 'Markus’s studio · 2 agents' }); await expect(pop.locator('.wdf-prev')).toContainText('2 agents on Markus’s studio') })
    await guard.check(async () => { await where.selectOption('pick'); await expect(pop.getByRole('group', { name: 'Agents to wind down' })).toBeVisible() })
    await guard.check(async () => { await pop.getByRole('group', { name: 'Agents to wind down' }).getByRole('checkbox').nth(2).uncheck(); await expect(pop.locator('.wdf-prev')).toContainText('2 agents you chose') })
    await guard.check(async () => { await where.selectOption({ label: 'All computers · 3 agents' }); await expect(pop.getByRole('group', { name: 'Agents to wind down' })).toHaveCount(0) })
    await guard.check(async () => { await where.selectOption({ label: 'Build machine · 1 agent' }); await expect(pop.locator('.wdf-prev')).toContainText('1 agent on Build machine') })
    guard.done()
    await quit.click(); await expect(pop).toHaveCount(0)
  }
})

test('AEON-783: plain Enter in By does not start the wind-down', async ({ page }) => {
  const mock = await setup(page)
  await page.goto('/agents')
  await windButton(page).click()
  const pop = formPopover(page), by = pop.getByLabel('By', { exact: true })
  await expect(pop.locator('button[type=submit]')).toBeEnabled()
  await by.focus()
  await page.keyboard.press('Enter')
  await expect(pop).toBeVisible()
  expect(mock.calls).toHaveLength(0)
  await page.keyboard.press('Shift+Enter')
  expect(mock.calls).toHaveLength(0)
  await page.keyboard.press('Alt+Enter')
  expect(mock.calls).toHaveLength(0)
  await page.keyboard.press('ControlOrMeta+Enter')
  await expect.poll(() => mock.calls.length).toBe(1)
  expect(mock.calls[0]?.body.hosts).toBe('all')
})

test('AEON-783: preset arrows focus the selected time', async ({ page }) => {
  const errors = watchErrors(page)
  // The DOM clears currentTarget when a listener returns. This Chromium keeps it,
  // so the radiogroup listener clears it on return. The handler must capture the
  // group before that, or the deferred focus reads null and throws.
  await page.addInitScript(() => {
    const orig = EventTarget.prototype.addEventListener
    EventTarget.prototype.addEventListener = function (type, listener, options) {
      if (type !== 'keydown' || typeof listener !== 'function') return orig.call(this, type, listener, options)
      return orig.call(this, type, function (event) {
        const result = listener.call(this, event)
        const role = event.currentTarget && event.currentTarget.getAttribute && event.currentTarget.getAttribute('role')
        if (role === 'radiogroup') Object.defineProperty(event, 'currentTarget', { configurable: true, get: () => null })
        return result
      }, options)
    }
  })
  await setup(page)
  await page.goto('/agents')
  await windButton(page).click()
  const seg = formPopover(page).getByRole('radiogroup', { name: 'Done in' })
  const fifteen = seg.getByRole('radio', { name: '15 min', exact: true })
  const thirty = seg.getByRole('radio', { name: '30 min', exact: true })
  await fifteen.focus()
  await expect(fifteen).toBeFocused()
  await page.keyboard.press('ArrowRight')
  await expect(thirty).toHaveAttribute('aria-checked', 'true')
  await expect(thirty).toBeFocused()
  await page.keyboard.press('ArrowLeft')
  await expect(fifteen).toHaveAttribute('aria-checked', 'true')
  await expect(fifteen).toBeFocused()
  await page.keyboard.press('ArrowDown')
  await expect(thirty).toBeFocused()
  await page.keyboard.press('ArrowUp')
  await expect(fifteen).toBeFocused()
  expect(errors).toEqual([])
})

test('AEON-783: new starts remain allowed until enforcement exists', async ({ page }) => {
  await setup(page)
  await page.goto('/agents')
  await windButton(page).click()
  const form = formPopover(page)
  await expect(form).toContainText('New starts on those computers remain allowed until enforcement exists.')
  await expect(form).not.toContainText('Nothing new starts')
  await form.locator('button[type=submit]').click()
  await chipButton(page).click()
  const status = statusPopover(page)
  await expect(status).toContainText('New starts remain allowed until enforcement exists')
  await expect(status).not.toContainText('Nothing new starts')
})

test('AEON-783: progress distinguishes handover, confirmed stop and lost contact', async ({ page }) => {
  const mock = await setup(page)
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.goto('/agents')
  await startWindDown(page)
  await expect.poll(() => mock.calls.length).toBe(1)
  const [saved, stopped, lost] = mock.data.sessions
  const handover = { state: 'Tests passed; work committed.', next_steps: ['Continue the feature'], open_questions: [], worktree_state: 'committed', commit_sha: 'abc1234' }
  Object.assign(saved!, { row_version: Number(saved!.row_version) + 1, phase: 'stopped', stopped_at: new Date(NOW + 5 * 60_000).toISOString(), stop_reason: 'paused', finished: false, pause: { ...(saved!.pause as object), state: 'paused', handover } })
  Object.assign(stopped!, { row_version: Number(stopped!.row_version) + 1, phase: 'stopped', stopped_at: new Date(NOW + 6 * 60_000).toISOString(), stop_reason: 'stopped', finished: false, pause: { ...(stopped!.pause as object), state: 'cancelled', level: 'stop_now', stop_requested: true } })
  Object.assign(lost!, { row_version: Number(lost!.row_version) + 1, phase: 'stopped', stopped_at: new Date(NOW + 7 * 60_000).toISOString(), stop_reason: 'heartbeat_lost', finished: false, pause: { ...(lost!.pause as object), state: 'requested' } })
  await page.reload()
  const chip = chipButton(page)
  await expect(chip).toContainText('Winding down')
  await expect(chip).toContainText('· 1 left')
  await expect(chip).not.toContainText('· 0 left')
  const dash = await chip.locator('circle.val').getAttribute('stroke-dasharray')
  expect(dash, 'progress ring').toBeTruthy()
  const [drawn, circ] = dash!.split(' ').map(Number)
  expect(drawn / circ).toBeGreaterThan(0.6)
  expect(drawn / circ).toBeLessThan(0.73)
  await chip.click()
  const status = statusPopover(page)
  await expect(status).toContainText('1 of 3 handed over')
  await expect(status).toContainText('1 confirmed terminal outcome')
  await expect(status).toContainText('1 lost contact, exit unconfirmed')
  await expect(status).not.toContainText('2 of 3 handed over')
  await expect(status).not.toContainText('3 of 3 handed over')
  await expect(status.locator('.wd-plan li').filter({ hasText: 'worker-1' })).toContainText('Handed over · handover saved')
  await expect(status.locator('.wd-plan li').filter({ hasText: 'worker-2' })).toContainText('Ended')
  await expect(status.locator('.wd-plan li').filter({ hasText: 'worker-2' })).not.toContainText('Handed over')
  await expect(status.locator('.wd-plan li').filter({ hasText: 'worker-3' })).toContainText('Lost contact · exit unconfirmed')
  const ratio = await status.locator('.bar i').evaluate(el => {
    const track = el.parentElement?.getBoundingClientRect().width ?? 0
    return track ? el.getBoundingClientRect().width / track : 1
  })
  expect(ratio).toBeGreaterThan(0.6)
  expect(ratio).toBeLessThan(0.73)
})

test('AEON-783: live outcome text keeps wind-down status actions still', async ({ page }, testInfo) => {
  const mock = await setup(page)
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.emulateMedia({ colorScheme: 'light', reducedMotion: 'reduce' })
  await page.goto('/agents')
  await startWindDown(page)
  await expect.poll(() => mock.calls.length).toBe(1)
  await chipButton(page).click()
  const status = statusPopover(page)
  await expect(status).toBeVisible()
  await expect(status).toContainText('0 of 3 handed over')
  await expect(status).not.toContainText('confirmed terminal')
  await expect(status).not.toContainText('lost contact')
  const close = status.getByRole('button', { name: 'Close', exact: true })
  const cancel = status.getByRole('button', { name: 'Cancel the wind-down', exact: true })
  const pauseAll = status.getByRole('button', { name: /^Pause all…/ })
  const resumeAll = status.getByRole('button', { name: /^Resume all…/ })
  const sentence = status.locator('p').filter({ hasText: 'handed over' })
  const beforeSentence = await bounds(sentence)
  const guard = await controlStability(page, { close, cancel, pauseAll, resumeAll })
  await guard.check(async () => {
    const [, stopped, lost] = mock.data.sessions
    Object.assign(stopped!, { row_version: Number(stopped!.row_version) + 1, phase: 'stopped', stopped_at: new Date(NOW + 6 * 60_000).toISOString(), stop_reason: 'stopped', finished: false, pause: { ...(stopped!.pause as object), state: 'cancelled', level: 'stop_now', stop_requested: true } })
    Object.assign(lost!, { row_version: Number(lost!.row_version) + 1, phase: 'stopped', stopped_at: new Date(NOW + 7 * 60_000).toISOString(), stop_reason: 'heartbeat_lost', finished: false, pause: { ...(lost!.pause as object), state: 'requested' } })
    await page.evaluate(() => window.dispatchEvent(new Event('online')))
    await expect(status).toBeVisible()
    await expect(status).toContainText('0 of 3 handed over')
    await expect(status).toContainText('1 confirmed terminal outcome')
    await expect(status).toContainText('1 lost contact, exit unconfirmed')
    await expect(status).not.toContainText('2 of 3 handed over')
  })
  const afterSentence = await bounds(sentence)
  expect(afterSentence.height, 'outcome sentence grew while the status stayed open').toBeGreaterThan(beforeSentence.height + 8)
  guard.done()
  await status.screenshot({ path: testInfo.outputPath('wind-down-status-live-1440-light.png') })
  await page.emulateMedia({ colorScheme: 'dark' })
  await status.screenshot({ path: testInfo.outputPath('wind-down-status-live-1440-dark.png') })
  for (const width of [390, 1024]) {
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ colorScheme: 'light' })
    await expect(status).toBeVisible()
    await status.screenshot({ path: testInfo.outputPath(`wind-down-status-live-${width}-light.png`) })
    await page.emulateMedia({ colorScheme: 'dark' })
    await expect(status).toBeVisible()
    await status.screenshot({ path: testInfo.outputPath(`wind-down-status-live-${width}-dark.png`) })
  }
})

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
    const shotTarget = width === 390 ? page.getByRole('complementary', { name: 'Session details' }) : row
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
    await shotTarget.screenshot({ path: testInfo.outputPath(`stop-requested-${width}-${theme}.png`) })
    await guard.check(async () => {
      Object.assign(session, { row_version: Number(session.row_version) + 1, phase: 'stopped', stopped_at: new Date(NOW).toISOString(), stop_reason: 'stopped' })
      await page.evaluate(() => window.dispatchEvent(new Event('online')))
      await expect(row.locator('.c-state .state-word')).toHaveText('Stopped')
    })
    guard.done()
    await shotTarget.screenshot({ path: testInfo.outputPath(`stopped-${width}-${theme}.png`) })
    await page.reload()
    await expect(row.locator('.c-state .state-word')).toHaveText('Stopped')
    expect(errors).toEqual([])
  })
}
