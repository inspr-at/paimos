// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { agentData, mockAgents, type AgentWorld } from './agents-fixtures'

const world: AgentWorld = { me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' }, nodes: {
  'p-pharos': { key: 'PRJ-17', title: 'Pharos' }, 'p-aeon': { key: 'PRJ-35', title: 'Aeon' }, 'p-frozen': { key: 'PRJ-26', title: 'Studio infrastructure' },
  'n-1': { key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning' }, 'n-2': { key: 'PHAROS-12', title: 'Add an Oracle Cloud connector' }, 'n-a1': { key: 'AEON-1', title: 'Aeon foundation' }, 'n-5': { key: 'PHAROS-15', title: 'Beacon health probes' }, 'n-6': { key: 'PHAROS-16', title: 'Retire the old dashboard' },
} }
async function setup(page: Page, admin = true) {
  await mockWork(page, fixtures(), { admin })
  const data = agentData(world)
  await mockAgents(page, data)
  const selected = data.sessions[1]!
  Object.assign(selected, { management_mode: 'unmanaged', display_label: 'ghost-worker', advertised_capabilities: ['status'] })
  const bodies: Record<string, unknown>[] = []
  await page.route('**/harness-sessions/*/remove', async route => {
    bodies.push(route.request().postDataJSON())
    Object.assign(selected, { archived_at: new Date().toISOString(), recovery_process_state: 'unknown', phase: 'stopped', stopped_at: new Date().toISOString(), stop_reason: 'removed_process_unknown' })
    await route.fulfill({ json: { session: selected, message: 'Record removed; process not stopped by removal.', processes_signalled: false, process_state: 'unknown' } })
  })
  return { data, selected, bodies }
}
const row = (page: Page, id: string) => page.locator(`[data-row="s:${id}"]`)
const confirm = (page: Page) => page.getByRole('dialog', { name: 'Remove ghost-worker?' })
const historyButton = (page: Page) => page.getByRole('button', { name: 'Show history: every ended or removed session' })
const shots = process.env.REMOVAL_SCREENSHOT_DIR
// Live row removal: the quiet overflow button, then the menu item, then the one confirmation.
async function removeFromRow(page: Page, id: string) {
  await row(page, id).hover()
  await row(page, id).getByRole('button', { name: 'Actions for ghost-worker' }).click()
  await page.getByRole('menuitem', { name: /^Remove/ }).click()
}

test('row overflow removes a live session; History preserves it and survives refresh', async ({ page }) => {
  const errors = watchErrors(page)
  const { selected, bodies } = await setup(page)
  await page.goto('/agents')
  const summary = page.locator('.summary')
  await expect(summary).toContainText('live')
  const before = await summary.innerText()
  // A live row carries no bin; Live now chips carry no removal.
  await expect(row(page, selected.id).getByRole('button', { name: /^Remove / })).toHaveCount(0)
  await expect(page.locator('.live-now').getByRole('button', { name: /^Remove / })).toHaveCount(0)
  await removeFromRow(page, selected.id)
  // Its heartbeat is fresh, so the process consequence is true and said.
  await expect(confirm(page)).toContainText('Its process keeps running; only the record leaves Agents.')
  await expect(confirm(page).getByRole('button')).toHaveText(['Cancel', 'Remove'])
  await confirm(page).getByRole('button', { name: 'Remove', exact: true }).click()
  await expect(row(page, selected.id)).toHaveCount(0)
  await expect(page.getByRole('button', { name: /^Open ghost-worker,/ })).toHaveCount(0)
  await expect(summary).not.toHaveText(before)
  expect(bodies).toEqual([{ reason: 'Removed from Agents by a person' }])
  await historyButton(page).click()
  await expect(page.getByRole('heading', { name: 'History' })).toBeVisible()
  await expect(row(page, selected.id)).toBeVisible()
  await expect(row(page, selected.id).getByRole('button', { name: /^Remove/ })).toHaveCount(0)
  await row(page, selected.id).getByRole('link').first().click()
  await expect(page.getByRole('complementary', { name: 'Session details' })).toContainText('process state unknown')
  await page.reload()
  await expect(page.getByRole('complementary', { name: 'Session details' })).toContainText('ghost-worker')
  await expect(row(page, selected.id)).toBeVisible()
  await page.getByRole('button', { name: 'Back to sessions' }).click()
  await expect(page.getByRole('heading', { name: 'Sessions', exact: true })).toBeVisible()
  expect(errors).toEqual([])
})

test('overflow button is quiet until hover or focus and keeps the column width', async ({ page }) => {
  const { selected } = await setup(page)
  await page.setViewportSize({ width: 1600, height: 1000 })
  await page.goto('/agents')
  const more = row(page, selected.id).getByRole('button', { name: 'Actions for ghost-worker' })
  await page.mouse.move(0, 0)
  await expect(more).toHaveCSS('opacity', '0')
  await row(page, selected.id).hover()
  await expect(more).toHaveCSS('opacity', '1')
  const cell = await row(page, selected.id).locator('.c-actions').boundingBox()
  const running = await row(page, selected.id).locator('.c-elapsed').boundingBox()
  // The actions track keeps night/r1's fixed 76px.
  expect(await page.locator('.sessions .table').evaluate(el => getComputedStyle(el).gridTemplateColumns.split(' ').at(-1))).toBe('76px')
  for (const button of await row(page, selected.id).locator('.c-actions button').all()) {
    const box = (await button.boundingBox())!
    expect(box.x).toBeGreaterThanOrEqual(running!.x + running!.width)
    expect(box.x + box.width).toBeLessThanOrEqual(cell!.x + cell!.width + 0.5)
  }
  // Keyboard focus reveals it too.
  await page.mouse.move(0, 0)
  await more.focus()
  await expect(more).toHaveCSS('opacity', '1')
})

test('stopped rows carry a bin; their menu offers only what works', async ({ page }) => {
  const { data } = await setup(page)
  const stopped = data.sessions.find(s => s.phase === 'stopped' && s.ticket_node_id)!
  Object.assign(stopped, { display_label: 'ghost-worker' })
  await page.goto('/agents')
  await page.getByRole('button', { name: /^Stopped/ }).click()
  await expect(row(page, stopped.id).getByRole('button', { name: 'Remove ghost-worker' })).toBeVisible()
  await row(page, stopped.id).getByRole('button', { name: 'Actions for ghost-worker' }).click()
  // No Interrupt or Stop and no disabled excuses: only actions that work.
  await expect(page.getByRole('menuitem')).toHaveText([/^Open PHAROS-12/, 'Copy session id', 'Remove'])
  await expect(page.locator('[role="menuitem"][aria-disabled="true"], [role="menuitem"]:disabled')).toHaveCount(0)
})

test('panel Remove sits with the controls, closes the panel after confirmation, and Cancel keeps the record', async ({ page }) => {
  const { selected, bodies } = await setup(page)
  await page.goto(`/agents/${selected.id}`)
  const panel = page.getByRole('complementary', { name: 'Session details' })
  const remove = panel.getByRole('button', { name: 'Remove ghost-worker' })
  await expect(remove).toHaveText('Remove…')
  await remove.click()
  await confirm(page).getByRole('button', { name: 'Cancel' }).click()
  expect(bodies).toHaveLength(0)
  await remove.click()
  await confirm(page).getByRole('button', { name: 'Remove', exact: true }).click()
  await expect(page).toHaveURL(/\/agents$/)
  await expect(panel).toHaveCount(0)
})

test('phones show the overflow button on every row; Live now chips carry no removal', async ({ page }) => {
  const { selected } = await setup(page)
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto('/agents')
  const more = row(page, selected.id).getByRole('button', { name: 'Actions for ghost-worker' })
  await expect(more).toHaveCSS('opacity', '1')
  await expect(page.locator('.live-now').getByRole('button', { name: /Remove/ })).toHaveCount(0)
  await expect(more).toHaveCSS('width', '44px')
  await more.click()
  await page.getByRole('menuitem', { name: /^Remove/ }).click()
  await confirm(page).getByRole('button', { name: 'Remove', exact: true }).click()
  await expect(row(page, selected.id)).toHaveCount(0)
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
})

test('project member can remove without force permissions', async ({ page }) => {
  const { selected } = await setup(page, false)
  await page.goto(`/agents/${selected.id}`)
  await expect(page.getByRole('complementary').getByRole('button', { name: 'Remove ghost-worker' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Recover', exact: true })).toHaveCount(0)
})

test('agents never receive Remove controls', async ({ page }) => {
  const { data, selected } = await setup(page)
  await page.route('**/api/me', route => route.fulfill({ json: { principal: { id: me.id, name: me.name, kind: 'agent', roles: ['admin'] }, tenant: { id: 't1', name: 'INSPR Studio' } } }))
  await page.goto(`/agents/${selected.id}`)
  await expect(page.getByRole('complementary', { name: 'Session details' })).toBeVisible()
  await expect(page.getByRole('button', { name: /^Remove / })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Clear stale' })).toHaveCount(0)
  await row(page, selected.id).getByRole('button', { name: 'Actions for ghost-worker' }).click()
  await expect(page.getByRole('menuitem', { name: /^Remove/ })).toHaveCount(0)
  await page.keyboard.press('Escape')
  // A stopped row offers an agent no bin and no Remove.
  const stopped = data.sessions.find(s => s.phase === 'stopped')!
  await page.getByRole('button', { name: /^Stopped/ }).click()
  await expect(row(page, stopped.id).getByRole('button', { name: /^Remove/ })).toHaveCount(0)
})

test('failed removal keeps the row and allows retry', async ({ page }) => {
  const { selected } = await setup(page)
  await page.route('**/harness-sessions/*/remove', route => route.fulfill({ status: 503, json: { error: 'Please retry removal' } }))
  await page.goto('/agents')
  await removeFromRow(page, selected.id)
  await confirm(page).getByRole('button', { name: 'Remove', exact: true }).click()
  await expect(page.getByText('Please retry removal', { exact: true })).toBeVisible()
  await expect(row(page, selected.id)).toBeVisible()
})

test('Clear stale names the count, confirms once and repeats while the server reports more', async ({ page }) => {
  const { data, selected } = await setup(page)
  selected.heartbeat_at = new Date(Date.now() - 16 * 60_000).toISOString()
  const stale = data.sessions.filter(s => Date.parse(s.heartbeat_at ?? s.created_at) < Date.now() - 15 * 60_000)
  const calls: string[] = []
  await page.route('**/harness-sessions/remove-stale', route => {
    const url = route.request().url()
    calls.push(url)
    // Answer one record per request to exercise the batch cap loop.
    const eligible = data.sessions.filter(s => url.includes(`/projects/${s.project_id}/`) && !s.archived_at && Date.parse(s.heartbeat_at ?? s.created_at) < Date.now() - 15 * 60_000)
    const items = eligible.slice(0, 1).map(s => {
      Object.assign(s, { archived_at: new Date().toISOString(), stopped_at: s.stopped_at ?? new Date().toISOString(), phase: 'stopped' })
      return { session: s, message: 'Record removed; process not stopped by removal.', processes_signalled: false, process_state: 'unknown' }
    })
    return route.fulfill({ json: { items, cutoff: new Date(Date.now() - 15 * 60_000).toISOString(), more: eligible.length > 1 } })
  })
  await page.goto('/agents')
  await page.getByRole('button', { name: 'Clear stale', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: `Remove ${stale.length} sessions without a heartbeat for 15 minutes?` })
  await expect(dialog).toContainText('Processes are not stopped; late heartbeats are ignored.')
  await dialog.getByRole('button', { name: 'Remove', exact: true }).click()
  await expect(row(page, selected.id)).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Clear stale', exact: true })).toHaveCount(0)
  expect(calls.length).toBe(stale.length)
  await historyButton(page).click()
  await expect(row(page, selected.id)).toBeVisible()
})

for (const theme of ['light', 'dark']) for (const width of [1600, 390]) {
  test(`removal visual ${width} ${theme}`, async ({ page }) => {
    const { data, selected } = await setup(page)
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme: theme as 'light' | 'dark' })
    await page.goto('/agents')
    await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
    await expect(row(page, selected.id)).toBeVisible()
    if (shots) await page.screenshot({ path: `${shots}/agents-${width}-${theme}.png`, fullPage: true })
    if (width > 600) {
      // A managed row shows its two controls and the overflow on hover.
      const managed = data.sessions.find(s => s.management_mode === 'managed' && s.phase === 'working')!
      await row(page, managed.id).hover()
      if (shots) await row(page, managed.id).screenshot({ path: `${shots}/row-hover-${width}-${theme}.png` })
    }
    await row(page, selected.id).scrollIntoViewIfNeeded()
    await row(page, selected.id).hover()
    await row(page, selected.id).getByRole('button', { name: 'Actions for ghost-worker' }).click()
    await expect(page.getByRole('menuitem', { name: /^Remove/ })).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    if (shots) await page.screenshot({ path: `${shots}/menu-${width}-${theme}.png` })
    await page.getByRole('menuitem', { name: /^Remove/ }).click()
    await expect(confirm(page)).toBeVisible()
    expect(await confirm(page).evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
    if (shots) await page.screenshot({ path: `${shots}/dialog-${width}-${theme}.png` })
    await page.keyboard.press('Escape')
    await row(page, selected.id).click()
    await expect(page.getByRole('complementary', { name: 'Session details' }).getByRole('button', { name: 'Remove ghost-worker' })).toBeVisible()
    if (shots) await page.screenshot({ path: `${shots}/panel-${width}-${theme}.png` })
    await page.getByRole('complementary', { name: 'Session details' }).getByRole('button', { name: 'Remove ghost-worker' }).click()
    await confirm(page).getByRole('button', { name: 'Remove', exact: true }).click()
    await historyButton(page).click()
    await expect(row(page, selected.id)).toBeVisible()
    if (shots) await page.locator('.sessions').screenshot({ path: `${shots}/removed-${width}-${theme}.png` })
  })
}

for (const width of [600, 800, 1000, 1280]) {
  test(`row overflow fits at ${width}px`, async ({ page }) => {
    const { selected } = await setup(page)
    await page.setViewportSize({ width, height: 900 })
    await page.goto('/agents')
    await row(page, selected.id).hover()
    const button = row(page, selected.id).getByRole('button', { name: 'Actions for ghost-worker' })
    await expect(button).toBeVisible()
    const rect = await button.boundingBox()
    expect(rect!.x).toBeGreaterThanOrEqual(0)
    expect(rect!.x + rect!.width).toBeLessThanOrEqual(width)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    await button.click()
    await page.getByRole('menuitem', { name: /^Remove/ }).click()
    await expect(confirm(page)).toBeVisible()
  })
}
