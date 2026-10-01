// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { agentData, mockAgents } from './agents-fixtures'

const now = Date.parse('2026-09-29T12:00:00Z')
const id = (n: number) => `32200000-0000-4000-8000-${String(n).padStart(12, '0')}`
const row = (page: Page, n: number) => page.locator(`[data-row="s:${id(n)}"]`)
async function setup(page: Page, theme = 'light', rights = true, extraLead = false) {
  await page.clock.install({ time: now })
  const work = fixtures(); work.preferences.theme = { choice: theme }
  await mockWork(page, work, { admin: true })
  await page.route('**/api/me/permissions*', route => {
    const grant = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    grant.workspace.permissions = [...grant.workspace.permissions, 'harness.write']
    return route.fulfill({ json: grant })
  })
  const data = agentData({ now, me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' } })
  const base = { ...data.sessions[0]!, run_id: null, ticket_node_id: null, ticket: null, model: 'gpt-6', brief: null, agent: { id: data.sessions[0]!.agent_principal_id, name: 'coordinator' }, parent_harness_session_id: null, phase: 'working', stopped_at: null, heartbeat_at: new Date(now - 1000).toISOString(), can_reparent: rights }
  const session = (n: number, label: string, extra: Record<string, unknown> = {}) => ({ ...base, id: id(n), display_label: label, role: 'coordinator', ...extra }) as unknown as typeof data.sessions[number]
  data.sessions.splice(0, data.sessions.length,
    session(1, 'Original lead', { phase: 'stopped', stopped_at: new Date(now - 50000).toISOString(), handed_over_to_id: id(2), can_reparent: false }),
    session(2, 'Resumed lead'), session(3, 'Release lead'),
    session(4, 'Permission checks', { role: 'worker', parent_harness_session_id: id(2), adopted_from_id: id(1) }),
    session(5, 'Foreign lead', { can_reparent: false }),
    session(6, 'Other project lead', { project_id: 'p-aeon' }),
  )
  if (extraLead) data.sessions.push(session(7, 'Build lead'))
  data.approvals.splice(0); data.messages.splice(0); data.targets.splice(0); data.runs.splice(0)
  await mockAgents(page, data)
  let moves = 0, undos = 0
  let previous: typeof data.sessions[number]
  await page.route('**/api/projects/*/harness-sessions/*/reparent', route => {
    const body = route.request().postDataJSON()
    expect(rights).toBe(true)
    expect(body.parent_harness_session_id).toBe(id(3))
    const worker = data.sessions.find(s => s.id === id(4))!
    previous = { ...worker }; moves++
    Object.assign(worker, { parent_harness_session_id: id(3), adopted_from_id: null, revision: worker.revision + 1 })
    const response = { ...worker }; delete response.adopted_from_id
    return route.fulfill({ json: { session: response, event_id: 322, undoable: true } })
  })
  await page.route('**/api/events/322/undo', route => {
    undos++; Object.assign(data.sessions.find(s => s.id === id(4))!, previous)
    return route.fulfill({ status: 201, json: { after: previous } })
  })
  await page.goto('/agents')
  await expect(row(page, 4)).toBeVisible()
  await page.locator('.group-toggle').filter({ hasText: 'Ended' }).click()
  return { moves: () => moves, undos: () => undos }
}

for (const theme of ['light', 'dark']) for (const width of [390, 1600]) {
  test(`handover and keyboard menu with undo ${theme} ${width}`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: 1000 })
    const calls = await setup(page, theme)
    await expect(row(page, 1)).toContainText('Handed over to Resumed lead')
    await expect(row(page, 1)).not.toContainText('working')
    await expect(row(page, 4)).toContainText('Adopted from Original lead')
    expect((await row(page, 4).locator('.lineage').boundingBox())!.width).toBeGreaterThan(100)
    await row(page, 4).getByRole('button', { name: 'Actions for Permission checks' }).focus()
    await page.keyboard.press('Enter')
    await expect(page.getByRole('menuitem', { name: 'Move to lead…' })).toBeFocused()
    await page.keyboard.press('Enter')
    const menu = page.getByRole('menu', { name: 'Move Permission checks to lead' })
    await expect(menu.getByRole('menuitem')).toHaveCount(1)
    await expect(menu.getByRole('menuitem', { name: 'Release lead' })).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    const dir = process.env.HANDOVER_SHOTS || info.outputDir
    mkdirSync(dir, { recursive: true })
    await page.screenshot({ path: join(dir, `${theme}-${width}-menu.png`), fullPage: true })
    await expect(menu.getByRole('menuitem', { name: 'Release lead' })).toBeFocused()
    await page.keyboard.press('Enter')
    await expect(row(page, 4)).toHaveAttribute('data-parent', id(3))
    await expect(row(page, 4)).not.toContainText('Adopted from')
    expect(calls.moves()).toBe(1)
    await page.getByRole('button', { name: 'Undo', exact: true }).click()
    await expect(row(page, 4)).toHaveAttribute('data-parent', id(2))
    expect(calls.undos()).toBe(1)
    await page.getByRole('button', { name: 'Dismiss', exact: true }).last().click()
    await page.locator('.sessions').screenshot({ path: join(dir, `${theme}-${width}-restored.png`) })
  })
}
test('move menu supports arrow keys and restores focus on Escape', async ({ page }) => {
  await setup(page, 'light', true, true)
  const trigger = row(page, 4).getByRole('button', { name: 'Actions for Permission checks' })
  await trigger.focus()
  await page.keyboard.press('Enter')
  await expect(page.getByRole('menuitem', { name: 'Move to lead…' })).toBeFocused()
  await page.keyboard.press('Enter')
  const menu = page.getByRole('menu', { name: 'Move Permission checks to lead' })
  await expect(menu.getByRole('menuitem')).toHaveCount(2)
  await expect(menu.getByRole('menuitem', { name: 'Release lead' })).toBeFocused()
  await page.keyboard.press('ArrowDown')
  await expect(menu.getByRole('menuitem', { name: 'Build lead' })).toBeFocused()
  await page.keyboard.press('Home')
  await expect(menu.getByRole('menuitem', { name: 'Release lead' })).toBeFocused()
  await page.keyboard.press('Escape')
  await expect(menu).toHaveCount(0)
  await expect(trigger).toBeFocused()
})
test('drag and drop moves only onto an authorized live lead', async ({ page }) => {
  const calls = await setup(page)
  await expect(row(page, 4)).toHaveAttribute('draggable', 'true')
  await expect(row(page, 1)).toHaveAttribute('draggable', 'false')
  const transfer = await page.evaluateHandle(() => new DataTransfer())
  await row(page, 4).dispatchEvent('dragstart', { dataTransfer: transfer })
  await expect(row(page, 3)).toHaveAttribute('data-drop-target', 'true')
  await expect(row(page, 1)).not.toHaveAttribute('data-drop-target', 'true')
  await expect(row(page, 5)).not.toHaveAttribute('data-drop-target', 'true')
  await expect(row(page, 6)).not.toHaveAttribute('data-drop-target', 'true')
  await row(page, 3).dispatchEvent('dragover', { dataTransfer: transfer })
  await row(page, 3).dispatchEvent('drop', { dataTransfer: transfer })
  await expect(row(page, 4)).toHaveAttribute('data-parent', id(3))
  expect(calls.moves()).toBe(1)
  await page.getByRole('button', { name: 'Undo', exact: true }).click()
  await expect(row(page, 4)).toHaveAttribute('data-parent', id(2))
})
test('foreign worker has neither drag permission nor a move menu', async ({ page }) => {
  const calls = await setup(page, 'light', false)
  await expect(row(page, 4)).toHaveAttribute('draggable', 'false')
  await row(page, 4).getByRole('button', { name: 'Actions for Permission checks' }).click()
  await expect(page.getByRole('menuitem', { name: 'Move to lead…' })).toHaveCount(0)
  expect(calls.moves()).toBe(0)
})
