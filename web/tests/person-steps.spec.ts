// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page } from '@playwright/test'
import { fixtures, me, mockWork, type Fixtures } from './work-fixtures'
import { mockDecisionDesk } from './decision-desk-fixtures'
import { controlStability } from './control-stability'
import type { PersonStep } from '../src/lib/personSteps'
import type { EntryChecks } from '../src/lib/entryChecks'

const mia = { id: '22222222-2222-4222-8222-222222222222', name: 'Mia Weber' }
const definitionRevision = '2026-10-14T10:00:00Z'
const longLabel = 'Freigabetext und dokumentierte Wiederherstellung mit den aktuellen Berechtigungen prüfen'

async function installSteps(page: Page, data: Fixtures, mode: 'owner' | 'admin' | 'member' | 'agent' = 'owner') {
  const manualNode = data.nodes.find(node => node.id === 'n-4')!, humanNode = data.nodes.find(node => node.id === 'n-5')!
  manualNode.state = 'qa'; manualNode.is_leaf = true; manualNode.title = 'Freigabe mit dokumentierter Wiederherstellung und nachvollziehbarer Prüfung'
  humanNode.state = 'done'; humanNode.is_leaf = true; humanNode.human_check = 'Die veröffentlichte Freigabe und die Wiederherstellung auf einem Telefon prüfen'
  const state = { owner: mode === 'owner' ? { ...me } : { ...mia }, humanOwner: mode === 'owner' ? { ...me } : { ...mia }, checked: false, humanChecked: false, fail: false, calls: [] as { path: string; body: Record<string, unknown> }[] }
  const mayTick = (owner = state.owner) => mode === 'owner' && owner.id === me.id || mode === 'admin'
  const steps = (): PersonStep[] => [
    { id: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', node_id: manualNode.id, check_id: 'manual:release-reviewed', label: longLabel, to_status: 'done', owner: state.owner, checked: state.checked, active: !state.checked, can_tick: mayTick(), revision: manualNode.updated_at, definition_id: 'dddddddd-dddd-4ddd-8ddd-dddddddddddd', definition_revision: definitionRevision, created_at: manualNode.created_at, ticket_key: manualNode.key, ticket_title: manualNode.title, project_id: manualNode.project },
    { id: 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb', node_id: humanNode.id, check_id: 'human', label: humanNode.human_check ?? String((humanNode.fields.human_check_completed as { text?: string } | undefined)?.text ?? 'Human check'), to_status: 'delivered', owner: state.humanOwner, checked: state.humanChecked, active: !state.humanChecked, can_tick: mayTick(state.humanOwner), revision: humanNode.updated_at, definition_id: null, definition_revision: null, created_at: humanNode.created_at, ticket_key: humanNode.key, ticket_title: humanNode.title, project_id: humanNode.project },
  ]
  const waiting = () => { for (const node of [manualNode, humanNode]) node.person_waiting = steps().filter(step => step.node_id === node.id && step.active).map(step => ({ check_id: step.check_id, label: step.label, owner: step.owner })) }
  waiting()
  await page.route('**/api/**', async route => {
    const request = route.request(), path = decodeURIComponent(new URL(request.url()).pathname), method = request.method()
    const node = [manualNode, humanNode].find(node => path.startsWith(`/api/nodes/${node.id}`))
    const json = (body: unknown, status = 200) => route.fulfill({ status, json: body })
    if (path === '/api/decision-desk/projection') {
      const rows = steps().filter(step => step.active && step.owner.id === me.id).map(step => ({ id: step.id, kind: 'action_request', revision: 1, project_id: step.project_id, title: `Confirm: ${step.label}`, created_at: step.created_at, held: false, can_decide: step.can_tick, source: `/api/nodes/${step.node_id}/person-steps/${step.check_id}`, href: `/decision-desk?item=m:${step.id}`, person_step: step }))
      return json({ items: rows, counts: { open: rows.length, held: 0, chores: 0 }, has_more: false, as_of: definitionRevision })
    }
    if (!node) return route.fallback()
    if (path.endsWith('/person-step-owners')) return json({ items: [me, mia].filter(person => person.name.toLowerCase().includes((new URL(request.url()).searchParams.get('q') ?? '').toLowerCase())), next_cursor: null })
    if (path.endsWith('/person-steps')) return json({ node_id: node.id, revision: node.updated_at, steps: steps().filter(step => step.node_id === node.id) })
    if (path.endsWith('/entry-checks')) {
      const step = steps().find(step => step.node_id === node.id)!
      const checks: EntryChecks = { node_id: node.id, revision: node.updated_at, definition: { id: 'dddddddd-dddd-4ddd-8ddd-dddddddddddd', revision: definitionRevision, source: 'project', checks: {} }, blocks: [{ to_status: 'done', total: 2, passed: state.checked ? 2 : 1, checks: [
        { id: 'release_copy_set', label: 'Release text set (EN + DE)', applicable: true, passed: true, can_tick: false, reason: '' },
        { id: 'manual:release-reviewed', label: longLabel, applicable: true, passed: state.checked, can_tick: step.can_tick, owner: step.owner, reason: `A person must tick this: ${step.owner.name}` },
      ] }], entries: node.id === humanNode.id ? [{ to_status: 'done', passed: 2, total: 2, checks: [], at: definitionRevision, by: me, override_reason: null, definition_id: 'dddddddd-dddd-4ddd-8ddd-dddddddddddd', definition_revision: definitionRevision }] : [], latest_refusal: null }
      return json(checks)
    }
    if (path.includes('/person-steps/') && method === 'PUT') {
      const body = request.postDataJSON() as Record<string, unknown>; state.calls.push({ path, body })
      if (state.fail || body.expected_revision !== node.updated_at) return json({ error: 'The ticket changed. Refresh before trying again.' }, 412)
      if (path.endsWith('/owner')) state.owner = body.owner_id === me.id ? { ...me } : { ...mia }
      else if (node.id === manualNode.id) state.checked = body.checked === true
      else { state.humanChecked = body.checked === true; if (state.humanChecked) { node.fields.human_check_completed = { text: node.human_check, by: me.id }; node.human_check = null } else { node.human_check = String((node.fields.human_check_completed as { text: string }).text); delete node.fields.human_check_completed } }
      node.updated_at = new Date(Date.parse(node.updated_at) + 1000).toISOString(); waiting()
      return json({ node_id: node.id, revision: node.updated_at, steps: steps().filter(step => step.node_id === node.id), undo_reassignment: path.endsWith('/owner') ? 701 : undefined })
    }
    if (path === `/api/nodes/${node.id}` && method === 'GET') return json({ ...node, kind_id: 'k-ticket', position: '0', deleted_at: null })
    return route.fallback()
  })
  return { state, steps, manualNode, humanNode, waiting }
}

// Risk: ownership text, overlays or the waiting mark move controls or list
// rows. Run the approved views with long German record text at all widths and
// in both themes, measuring positive-size controls around real interactions.
test('AEON-1318 owner controls and person-waiting views stay stable', async ({ browser }, testInfo) => {
  test.setTimeout(180_000)
  for (const width of [1440, 1024, 390]) for (const theme of ['light', 'dark']) {
    const context = await browser.newContext({ baseURL: testInfo.project.use.baseURL, viewport: { width, height: 1000 }, hasTouch: width === 390, reducedMotion: 'reduce' })
    try {
      const page = await context.newPage(), data = fixtures()
      await mockWork(page, data)
      const { manualNode, humanNode } = await installSteps(page, data)
      await page.goto('/p/PHAROS?sort=key&closed=1')
      await page.evaluate(value => document.documentElement.dataset.theme = value, theme)
      const row = page.locator('tr.ticket-row').filter({ hasText: manualNode.key })
      await expect(row.locator('.person-waiting svg')).toBeVisible()
      await expect(row.locator('.person-waiting')).toHaveAttribute('data-tip', `Waiting for ${me.name}: ${longLabel}`)
      const rows = await controlStability(page, { row, otherRow: page.locator('tr.ticket-row').filter({ hasText: humanNode.key }) })
      await rows.check(() => row.locator('.person-waiting').hover()); rows.done()
      await page.screenshot({ path: testInfo.outputPath(`AEON-1318-list-${width}-${theme}.png`) })
      await row.locator('.title-text').click()
      const workspace = page.getByRole('complementary', { name: 'Ticket details' }), line = workspace.getByRole('button', { name: 'Done 1/2 · waiting for you', exact: true })
      await expect(line).toBeVisible(); await line.click()
      const panel = page.getByRole('dialog', { name: `${manualNode.key}: Done checks` })
      const who = panel.getByRole('button', { name: `Who checks this: ${me.name}. Ask someone else` })
      const guard = await controlStability(page, { line: workspace.locator('[data-entry-slot]'), row: panel.locator('.nr-item.manual'), who, tick: panel.getByRole('button', { name: 'Mark checked' }), close: panel.getByRole('button', { name: 'Close checklist' }) })
      await guard.check(() => who.hover())
      await guard.check(async () => { await who.click(); await expect(page.getByRole('dialog', { name: 'Who checks this', exact: true }).getByRole('button', { name: mia.name })).toBeVisible() })
      await guard.check(() => page.getByRole('dialog', { name: 'Who checks this', exact: true }).getByRole('button', { name: 'Cancel', exact: true }).click())
      guard.done()
      await page.screenshot({ path: testInfo.outputPath(`AEON-1318-checklist-${width}-${theme}.png`) })
      await panel.getByRole('button', { name: 'Close checklist' }).click()
      await page.goto(`/p/PHAROS/${humanNode.key}`)
      const human = page.getByRole('note', { name: 'Needs a human check' })
      const humanWho = human.getByRole('button', { name: `Who checks this: ${me.name}. Ask someone else` })
      await expect(human.getByRole('button', { name: 'Mark checked' })).toHaveClass(/primary/)
      const humanGuard = await controlStability(page, { owner: humanWho, tick: human.getByRole('button', { name: 'Mark checked' }) })
      await humanGuard.check(() => humanWho.hover()); humanGuard.done()
      await page.evaluate(value => document.documentElement.dataset.theme = value, theme)
      await page.screenshot({ path: testInfo.outputPath(`AEON-1318-human-${width}-${theme}.png`) })
    } finally { await context.close() }
  }
})

// Risk: a failed action looks checked, resolving removes the row under the
// pointer, reassignment leaves the badge stale, or Undo targets another step.
test('AEON-1318 inline Desk checks and reassignment settle in place', async ({ browser }, testInfo) => {
  test.setTimeout(180_000)
  for (const width of [1440, 1024, 390]) for (const theme of ['light', 'dark']) {
    const context = await browser.newContext({ baseURL: testInfo.project.use.baseURL, viewport: { width, height: 1000 }, hasTouch: width === 390, reducedMotion: 'reduce' })
    try {
      const page = await context.newPage(), data = fixtures()
      await mockDecisionDesk(page)
      const { state, steps, manualNode } = await installSteps(page, data)
      await page.goto('/decision-desk')
      await page.evaluate(value => document.documentElement.dataset.theme = value, theme)
      const row = page.getByTestId(`desk-row-m:${steps()[0]!.id}`), otherRow = page.getByTestId(`desk-row-m:${steps()[1]!.id}`)
      const open = page.locator('.desk-summary .view-tabs button').first()
      const badge = page.getByRole('navigation', { name: 'Places' }).getByRole('link', { name: /^Decision Desk, / })
      await expect(open).toHaveText('Open 2'); await expect(badge).toHaveAccessibleName(/2/)
      const guard = await controlStability(page, { row, otherRow, open })
      state.fail = true
      await guard.check(async () => { await row.getByRole('button', { name: 'Mark checked' }).click(); await expect(row.getByRole('alert')).toContainText('The ticket changed'); await expect(open).toHaveText('Open 2') })
      state.fail = false
      await guard.check(async () => { await row.getByRole('button', { name: 'Mark checked' }).click(); await expect(row.getByText('Checked by you · moves to Decided')).toBeVisible(); await expect(open).toHaveText('Open 1') })
      await expect(badge).toHaveAccessibleName(/1/)
      expect(state.calls.at(-1)?.body).toMatchObject({ checked: true, expected_revision: state.calls[0]!.body.expected_revision, definition_revision: definitionRevision })
      await guard.check(async () => { await row.getByRole('button', { name: 'Undo' }).click(); await expect(row.getByRole('button', { name: 'Mark checked' })).toBeVisible(); await expect(open).toHaveText('Open 2') })
      const more = row.getByRole('button', { name: /^More for/ })
      await guard.check(() => more.hover())
      const revisionBeforeAssign = manualNode.updated_at
      await more.click(); await page.getByRole('menuitem', { name: 'Ask someone else…' }).click()
      const picker = page.getByRole('dialog', { name: 'Who checks this', exact: true })
      await expect(picker.getByRole('button', { name: mia.name })).toBeVisible()
      await guard.check(async () => { await picker.getByRole('button', { name: mia.name }).click(); await expect(row.getByText(`Asked ${mia.name} · it left your Desk`)).toBeVisible(); await expect(open).toHaveText('Open 1') })
      guard.done()
      const assignment = state.calls.at(-1)!
      expect(assignment.body).toMatchObject({ owner_id: mia.id, expected_revision: revisionBeforeAssign, definition_revision: definitionRevision })
      await page.screenshot({ path: testInfo.outputPath(`AEON-1318-desk-${width}-${theme}.png`) })
      await row.getByRole('button', { name: 'Undo' }).click()
      await expect(open).toHaveText('Open 2')
      expect(state.calls.at(-1)?.body).toMatchObject({ owner_id: me.id, undo_reassignment: 701 })
      await row.getByRole('button', { name: 'Mark checked' }).click(); await expect(open).toHaveText('Open 1')
      await page.reload(); await expect(page.getByTestId(`desk-row-m:${steps()[0]!.id}`)).toHaveCount(0)
    } finally { await context.close() }
  }
})

test('AEON-1318 tick controls distinguish owner, project admin, member and agent', async ({ browser }, testInfo) => {
  for (const mode of ['owner', 'admin', 'member', 'agent'] as const) {
    const context = await browser.newContext({ baseURL: testInfo.project.use.baseURL })
    try {
      const page = await context.newPage(), data = fixtures()
      await mockWork(page, data, { principalKind: mode === 'agent' ? 'agent' : 'person', admin: mode !== 'member' })
      const { manualNode, humanNode } = await installSteps(page, data, mode)
      await page.goto(`/p/PHAROS/${humanNode.key}`)
      const human = page.getByRole('note', { name: 'Needs a human check' }), tick = human.getByRole('button', { name: 'Mark checked' })
      await expect(human).toContainText(mode === 'owner' ? me.name : mia.name)
      if (mode === 'member' || mode === 'agent') await expect(tick).toHaveCount(0)
      else { await expect(tick).toBeVisible(); if (mode === 'owner') await expect(tick).toHaveClass(/primary/); else await expect(tick).not.toHaveClass(/primary/) }
      await page.goto(`/p/PHAROS/${manualNode.key}`)
      await page.getByRole('button', { name: `Done 1/2 · waiting for ${mode === 'owner' ? 'you' : mia.name}`, exact: true }).click()
      const checklist = page.getByRole('dialog', { name: `${manualNode.key}: Done checks` })
      if (mode === 'member' || mode === 'agent') { await expect(checklist.getByRole('button', { name: 'Mark checked' })).toHaveCount(0); await expect(checklist.getByText(mode === 'agent' ? `A person must tick this: ${mia.name}` : `${mia.name} or a project admin ticks this`)).toBeVisible() }
      else await expect(checklist.getByRole('button', { name: 'Mark checked' })).toBeVisible()
    } finally { await context.close() }
  }
})
