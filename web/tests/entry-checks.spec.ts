// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test } from '@playwright/test'
import type { EntryBlock, EntryCheck, EntryChecks, EntryRecord } from '../src/lib/entryChecks'
import { fixtures, me, mockWork } from './work-fixtures'
import { controlStability } from './control-stability'

// Risk: the usual definition arrives after the ticket is already interactive.
// Hold each read to measure the reserved slot and the controls below it before
// the first result, after next-ticket navigation, and during a saved refresh.
test('AEON-1315 defined entry row reserves space before reads and keeps the ticket stable', async ({ browser }, testInfo) => {
  for (const width of [1440, 1024, 390]) {
    const touch = width === 390
    const context = await browser.newContext({ baseURL: testInfo.project.use.baseURL, viewport: { width, height: 1000 }, hasTouch: touch, reducedMotion: 'reduce' })
    try {
      const page = await context.newPage(), data = fixtures()
      const nodes = ['n-4', 'n-5'].map(id => data.nodes.find(node => node.id === id)!)
      for (const node of nodes) {
        node.state = 'open'
        node.title = 'Freigaberegeln für lang beschriebene Arbeit vor Beginn, Qualitätsprüfung und Abschluss nachvollziehbar anzeigen'
        node.fields.acceptance_criteria = '- [ ] The work satisfies the approved criteria'
      }
      await mockWork(page, data)
      await page.route(url => url.pathname === '/api/queue', route => route.fulfill({ json: {
        items: nodes.map((node, position) => ({ node_id: node.id, project_id: node.project, key: node.key, title: node.title, state: node.state, priority: 'medium', estimate_hours: 2,
          queued: { run_id: `run-${node.id}`, position, by: { ...me, kind: 'person' }, at: node.created_at, target_agent_id: null, expected_agent_id: null, model_profile_id: null, expected_start_at: null, waiting: false, wait_reason: '' } })),
        manual_order: false, capacity: { queued_hours: 4, parallel_runs: 1, work_hours: 4, warning: false },
      } }))
      const pending: { id: string; release: () => void }[] = []
      await page.route('**/api/nodes/*/entry-checks', async route => {
        const id = new URL(route.request().url()).pathname.split('/')[3]!
        const node = data.nodes.find(item => item.id === id)!
        const result: EntryChecks = { node_id: id, revision: node.updated_at,
          definition: { id: '00000000-0000-0000-0000-000000000000', revision: '2026-10-13T00:00:00Z', source: 'workspace', checks: {} },
          blocks: [{ to_status: 'in_progress', passed: node.fields.estimate_hours ? 2 : 1, total: 2, checks: [
            { id: 'estimate_set', label: 'Estimate set', applicable: true, passed: !!node.fields.estimate_hours, reason: '', can_tick: false },
            { id: 'acceptance_present', label: 'Acceptance criteria written', applicable: true, passed: true, reason: '', can_tick: false },
          ] }], entries: [], latest_refusal: null,
        }
        await new Promise<void>(release => pending.push({ id, release }))
        await route.fulfill({ json: result })
      })
      await page.goto('/p/PHAROS?sort=key&closed=1')
      await page.locator('tr.ticket-row').filter({ hasText: 'PHAROS-14' }).locator('.title-text').click()
      const workspace = page.getByRole('complementary', { name: 'Ticket details' }), slot = workspace.locator('[data-entry-slot]')
      const controls = { details: workspace.locator('.meta').first(), queue: workspace.locator('.q-card'), previous: workspace.getByRole('button', { name: 'Previous ticket', exact: true }), next: workspace.getByRole('button', { name: 'Next ticket', exact: true }) }
      await expect.poll(() => pending.length).toBe(1)
      expect(pending[0]!.id).toBe(nodes[0]!.id)
      await expect(slot).toBeVisible()
      await expect(slot.getByRole('button')).toHaveCount(0)
      expect((await slot.boundingBox())?.height).toBe(touch ? 44 : 28)
      const initial = await controlStability(page, { slot, ...controls })
      await initial.check(async () => {
        pending[0]!.release()
        await expect(slot.getByRole('button', { name: 'Ready 1/2', exact: true })).toBeVisible()
      })
      initial.done()
      await controls.next.click()
      await expect.poll(() => pending.length).toBe(2)
      expect(pending[1]!.id).toBe(nodes[1]!.id)
      await expect(slot).toBeVisible()
      await expect(slot.getByRole('button')).toHaveCount(0)
      const next = await controlStability(page, { slot, ...controls })
      await next.check(async () => {
        pending[1]!.release()
        await expect(slot.getByRole('button', { name: 'Ready 1/2', exact: true })).toBeVisible()
      })
      next.done()
      await workspace.getByRole('button', { name: 'Add estimate', exact: true }).click()
      await workspace.getByRole('textbox', { name: 'Estimate in agent hours' }).fill('2h')
      await workspace.getByRole('button', { name: 'Save', exact: true }).click()
      await expect.poll(() => pending.length).toBe(3)
      expect(pending[2]!.id).toBe(nodes[1]!.id)
      await expect(slot.getByRole('button', { name: 'Ready 1/2', exact: true })).toBeVisible()
      const refreshed = await controlStability(page, { slot, ...controls })
      await refreshed.check(async () => {
        pending[2]!.release()
        await expect(slot.getByRole('button', { name: 'Ready 2/2', exact: true })).toBeVisible()
      })
      refreshed.done()
      for (const theme of ['light', 'dark']) {
        await page.evaluate(value => document.documentElement.dataset.theme = value, theme)
        await page.screenshot({ path: testInfo.outputPath(`entry-loading-${width}-${theme}.png`) })
      }
    } finally { await context.close() }
  }
})

// Risk: a loading-only row briefly displaces the ticket below its properties,
// both on first open and after a save. Response barriers and a DOM observer
// retain transient insertions that a settled-state assertion would miss.
test('AEON-1315 entry row stays absent without a definition during reads and saves', async ({ page }) => {
  const data = fixtures(), node = data.nodes.find(n => n.id === 'n-4')!
  await mockWork(page, data)
  await page.addInitScript(() => {
    const observed = { slots: 0 }
    Object.assign(window, { entryRowObserved: observed })
    new MutationObserver(records => {
      for (const record of records) for (const added of record.addedNodes) {
        if (added instanceof Element && (added.matches('[data-entry-slot]') || added.querySelector('[data-entry-slot]'))) observed.slots++
      }
    }).observe(document, { childList: true, subtree: true })
  })
  const pending: (() => void)[] = []
  await page.route(`**/api/nodes/${node.id}/entry-checks`, async route => {
    const revision = node.updated_at
    await new Promise<void>(resolve => pending.push(resolve))
    await route.fulfill({ json: { node_id: node.id, revision, definition: null, blocks: [], entries: [], latest_refusal: null } })
  })
  await page.goto('/p/PHAROS?sort=key&closed=1')
  await page.locator('tr.ticket-row').filter({ hasText: 'PHAROS-14' }).locator('.title-text').click()
  const workspace = page.getByRole('complementary', { name: 'Ticket details' }), slot = workspace.locator('[data-entry-slot]')
  await expect.poll(() => pending.length).toBe(1)
  await expect(slot).toHaveCount(0)
  const stable = await controlStability(page, { details: workspace.locator('.meta').first() })
  await stable.check(async () => {
    const response = page.waitForResponse(`**/api/nodes/${node.id}/entry-checks`)
    pending[0]()
    await response
    await expect(slot).toHaveCount(0)
  })
  await workspace.getByRole('button', { name: 'Add estimate', exact: true }).click()
  await workspace.getByRole('textbox', { name: 'Estimate in agent hours' }).fill('2h')
  const before = node.updated_at
  await workspace.getByRole('button', { name: 'Save', exact: true }).click()
  await expect.poll(() => pending.length).toBe(2)
  expect(node.updated_at).not.toBe(before)
  await expect(slot).toHaveCount(0)
  await stable.check(async () => {
    const response = page.waitForResponse(`**/api/nodes/${node.id}/entry-checks`)
    pending[1]()
    await response
    await expect(slot).toHaveCount(0)
  })
  stable.done()
  expect(await page.evaluate(() => (window as unknown as { entryRowObserved: { slots: number } }).entryRowObserved.slots)).toBe(0)
})

// Risks: moving controls, status refusal disappearing behind a toast, stale
// ticket writes, and a manual check looking actionable to an agent.
test('AEON-1315 entry checklist preserves controls and refuses or audits status moves', async ({ page }, testInfo) => {
  page.on('pageerror', error => { throw error })
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
  const move = refused.getByRole('button', { name: /^Move to QA (Command|Ctrl) Enter$/ })
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
