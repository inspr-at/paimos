// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test } from '@playwright/test'
import type { EntryBlock, EntryCheck, EntryChecks, EntryRecord } from '../src/lib/entryChecks'
import { fixtures, me, mockWork } from './work-fixtures'
import { controlStability } from './control-stability'

// Risks: moving controls, status refusal disappearing behind a toast, stale
// ticket writes, and a manual check looking actionable to an agent.
test('AEON-1315 entry checklist preserves controls and refuses or audits status moves', async ({ page }, testInfo) => {
  const data = fixtures(), node = data.nodes.find(n => n.id === 'n-4')!
  node.state = 'open'; node.fields.acceptance_criteria = '- [ ] The work satisfies the approved criteria'
  node.title = 'Freigaberegeln für lang beschriebene Arbeit vor Beginn, Qualitätsprüfung und Abschluss nachvollziehbar anzeigen'
  await mockWork(page, data)
  let defined = true, agent = false, checked = false
  const entries: EntryRecord[] = [], moves: { state: string; override_reason?: string }[] = []
  const check = (id: string, label: string, passed: boolean, extra: Partial<EntryCheck> = {}): EntryCheck => ({ id, label, applicable: true, passed, reason: passed ? '' : 'Required before this status', can_tick: false, ...extra })
  const block = (to_status: string, checks: EntryCheck[]): EntryBlock => ({ to_status, checks, total: checks.filter(c => c.applicable).length, passed: checks.filter(c => c.applicable && c.passed).length })
  const snapshot = (): EntryChecks => ({ node_id: node.id, revision: node.updated_at,
    definition: defined ? { id: 'definition', revision: node.created_at, source: 'project', checks: {} } : null,
    blocks: [block('in_progress', [check('estimate_set', 'Estimate set', !!node.fields.estimate_hours), check('acceptance_present', 'Acceptance criteria written', true), check('blocker_named', 'Blocker named', false, { applicable: false, condition: 'blocked' })]),
      block('qa', [check('ci_green_latest_main', 'Full CI green on latest main', false), check('review_ok_cross_family', 'Cross-family review ok', false)]),
      block('done', [check('release_copy_set', 'Release text set (EN + DE)', true), check('manual:release-reviewed', 'Release notes reviewed', checked, { owner: me, can_tick: !agent, checked_by: checked ? me : null, checked_at: checked ? node.updated_at : null, reason: checked ? '' : `A person must tick this · waiting for ${me.name}` })])],
    entries, latest_refusal: { to_status: 'qa', failed: [check('ci_green_latest_main', 'Full CI green on latest main', false)], at: node.updated_at, by: { id: 'sol', name: 'Sol' }, count: 2 },
  })
  const wire = () => ({ ...node, kind_id: 'k-ticket', position: '0', deleted_at: null })
  const forward = () => { node.updated_at = new Date(Date.parse(node.updated_at) + 1000).toISOString() }
  await page.route('**/api/**', async route => {
    const request = route.request(), path = new URL(request.url()).pathname
    const json = (value: unknown, status = 200) => route.fulfill({ status, json: value })
    if (path === '/api/me' && agent) return json({ principal: { ...me, kind: 'agent', roles: ['admin'] }, tenant: { id: 'tenant', name: 'INSPR' }, identity: null })
    if (/\/entry-checks$/.test(path)) return json(path.includes(node.id) ? snapshot() : { ...snapshot(), node_id: path.split('/')[3], definition: null, entries: [], latest_refusal: null })
    if (path === `/api/nodes/${node.id}/entry-checks/manual%3Arelease-reviewed` || path === `/api/nodes/${node.id}/entry-checks/manual:release-reviewed`) { checked = !!request.postDataJSON().checked; forward(); return json(snapshot()) }
    if (path === `/api/queue/${node.id}/readiness`) return json({ queueable: true, ready: !!node.fields.estimate_hours, missing: node.fields.estimate_hours ? [] : ['estimate'], suggested_estimate_hours: 3, security_review_required: false })
    if (path === `/api/queue/${node.id}/estimate`) { node.fields.estimate_hours = request.postDataJSON().estimate_hours; forward(); return json({ queueable: true, ready: true, missing: [], suggested_estimate_hours: 3, security_review_required: false }) }
    if (path === `/api/nodes/${node.id}` && request.method() === 'PATCH') {
      const body = request.postDataJSON() as { state?: string; override_reason?: string; fields?: Record<string, unknown> }
      const result = snapshot().blocks.find(b => b.to_status === body.state), missing = result?.checks.filter(c => c.applicable && !c.passed) ?? []
      if (missing.length && (!body.override_reason?.trim() || agent)) return json({ error: 'entry_checks_failed', code: 'entry_checks_failed', to_status: body.state, failed: missing, override_allowed: !agent }, 409)
      if (body.state) { moves.push({ state: body.state, override_reason: body.override_reason }); node.state = body.state; if (result) entries.push({ ...result, by: me, at: node.updated_at, override_reason: missing.length ? body.override_reason! : null, definition_id: 'definition', definition_revision: node.created_at }) }
      if (body.fields) node.fields = body.fields
      forward(); return json(wire())
    }
    return route.fallback()
  })
  await page.setViewportSize({ width: 1440, height: 1000 })
  const open = async () => { await page.goto('/p/PHAROS?sort=key&closed=1'); await page.locator('tr.ticket-row').filter({ hasText: 'PHAROS-14' }).locator('.title-text').click() }
  await open()
  const workspace = page.getByRole('complementary', { name: 'Ticket details' }), slot = workspace.locator('[data-entry-slot]')
  await expect(workspace.getByRole('button', { name: 'Ready 1/2' })).toBeVisible()
  await workspace.getByRole('button', { name: 'Ready 1/2' }).click()
  const panel = page.getByRole('dialog', { name: 'PHAROS-14: Ready checks' })
  await expect(panel.getByText('Only for blocked tickets')).toBeVisible()
  await expect(panel.getByText(/Sol couldn’t move/)).toBeVisible()
  const guard = await controlStability(page, { slot, close: panel.getByRole('button', { name: 'Close checklist' }), estimate: panel.getByRole('button', { name: 'Suggest estimate' }), moveAnyway: panel.getByRole('button', { name: 'Move to In progress anyway…' }) })
  await guard.check(async () => { await panel.getByRole('button', { name: 'Suggest estimate' }).click(); await expect(workspace.getByRole('button', { name: 'Ready 2/2' })).toBeVisible() })
  await guard.check(async () => { await panel.getByRole('button', { name: 'Move to In progress anyway…' }).click(); await expect(panel.getByRole('textbox', { name: 'Why move it now?' })).toBeVisible() })
  guard.done()
  await panel.getByRole('button', { name: 'Close checklist' }).click()
  await workspace.getByRole('button', { name: /^Status:.*Change status/ }).click()
  const menu = page.getByRole('dialog', { name: 'Status of PHAROS-14' })
  await expect(menu.getByRole('menuitemradio', { name: /QA/ })).toContainText('0 of 2')
  await menu.getByRole('menuitemradio', { name: /QA/ }).click()
  const refused = page.getByRole('dialog', { name: 'PHAROS-14: QA checks' })
  const move = refused.getByRole('button', { name: /^Move to QA / })
  await expect(refused.getByRole('textbox', { name: 'Why move it now?' })).toBeVisible()
  await expect(move).toBeDisabled()
  await refused.getByRole('textbox', { name: 'Why move it now?' }).fill('Time-sensitive review; evidence follows the approved work')
  await move.click()
  expect(moves.at(-1)).toEqual({ state: 'qa', override_reason: 'Time-sensitive review; evidence follows the approved work' })
  await expect(workspace.getByRole('button', { name: 'Done 1/2 · waiting for you' })).toBeVisible()
  for (const width of [1440, 1024, 390]) for (const theme of ['light', 'dark']) {
    await page.setViewportSize({ width, height: 1000 }); await page.evaluate(value => document.documentElement.dataset.theme = value, theme)
    await page.screenshot({ path: testInfo.outputPath(`entry-line-${width}-${theme}.png`) })
  }
  await page.setViewportSize({ width: 1440, height: 1000 })
  agent = true; await open()
  await workspace.getByRole('button', { name: 'Done 1/2 · waiting for you' }).click()
  const human = page.getByRole('dialog', { name: 'PHAROS-14: Done checks' })
  await expect(human.getByRole('button', { name: 'Mark checked' })).toHaveCount(0)
  await expect(human.getByText(/A person must tick this/)).toBeVisible()
  agent = false; await open()
  await workspace.getByRole('button', { name: 'Done 1/2 · waiting for you' }).click()
  await human.getByRole('button', { name: 'Mark checked' }).click()
  await expect(workspace.getByRole('button', { name: 'Done 2/2' })).toBeVisible()
  await human.getByRole('button', { name: 'Close checklist' }).click()
  await workspace.getByRole('button', { name: /^Status:.*Change status/ }).click()
  await menu.getByRole('menuitemradio', { name: /^Done/ }).click()
  await workspace.getByRole('button', { name: 'Done 2/2' }).click()
  await expect(human.getByText('Passed on entry').first()).toBeVisible()
  await expect(human.getByRole('button', { name: /Move to|Mark checked|Suggest estimate|Undo/ })).toHaveCount(0)
  entries.length = 0; await open()
  await expect(slot).toBeVisible(); await expect(slot.getByRole('button')).toHaveCount(0)
  defined = false; await open(); await expect(slot).toHaveCount(0)
})
