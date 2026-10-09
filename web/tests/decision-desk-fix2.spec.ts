// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
import { join } from 'node:path'
import { mockDecisionDesk } from './decision-desk-fixtures'
import { expectStableControls } from './helpers/stable'

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
  test(`server outcomes and correction keep selectors and actions stable ${width} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    const world = await mockDecisionDesk(page, { long: true, theme }), question = world.questions[0]!
    question.suggested_outcome = 'always'; question.suggestion_reason = 'project_default'
    question.input.context = Array(40).fill('Die Entscheidung gilt für das gesamte Projekt. Die vorhandenen Berechtigungen und die unveränderte Mandantentrennung bleiben bei jeder Korrektur überprüfbar.').join('\n')
    question.input.doctrine = { source_id: 'source-1', path: 'docs/AGENTS', rule_key: 'checks', rule_sha256: 'a'.repeat(64) }
    question.outcomes!.find(row => row.outcome === 'doctrine')!.available = true
    question.outcomes!.find(row => row.outcome === 'doctrine')!.mapping_present = true
    await page.goto('/decision-desk?item=q:question-1')
    await expect(page.getByTestId('stamp-always')).toHaveAttribute('aria-pressed', 'true')
    await expect(page.getByTestId('desk-decide')).toBeEnabled()
    const controls = { decide: page.getByTestId('desk-decide'), skip: page.getByTestId('desk-skip'), close: page.getByTestId('desk-close'), pager: page.getByTestId('desk-pager'),
      selectors: page.getByTestId('desk-stamps'), always: page.getByTestId('stamp-always'), requirement: page.getByTestId('stamp-requirement'), doctrine: page.getByTestId('stamp-doctrine'),
      choices: page.getByTestId('desk-choices'), clicked: page.getByTestId('choice-row-0'), frame: page.getByTestId('desk-frame') }
    await expectStableControls({ controls, scrollAreas: { body: page.getByTestId('desk-body') }, interactions: [
      { name: 'submit the project-default Always without changing its stamp', run: async () => {
        await page.getByTestId('desk-decide').click(); await expect.poll(() => world.calls.length).toBe(1)
        await expect(page.getByTestId('desk-pager')).toContainText('3 of'); await expect(page.getByTestId('desk-decide')).toBeFocused()
      } },
      { name: 'return to the recorded question', run: async () => { await page.getByTestId('desk-paper').press('ArrowLeft'); await expect(page.getByTestId('desk-decide')).toContainText('Next') } },
      ...(['requirement', 'doctrine', 'once'] as const).flatMap((outcome, at) => [
        { name: `select ${outcome}`, run: async () => { await page.getByTestId(`stamp-${outcome}`).click(); await expect(page.getByTestId(`stamp-${outcome}`)).toHaveAttribute('aria-pressed', 'true') } },
        { name: `replace with ${outcome} at the displayed revision`, run: async () => { await page.getByTestId('desk-decide').click(); await expect.poll(() => world.calls.length).toBe(at + 2); await expect(page.getByTestId('desk-pager')).toContainText('3 of'); await expect(page.getByTestId('desk-decide')).toBeFocused() } },
        { name: `revisit ${outcome}`, run: async () => { await page.getByTestId('desk-paper').press('ArrowLeft'); await expect(page.getByTestId(`stamp-${outcome}`)).toHaveAttribute('aria-pressed', 'true') } },
      ]),
    ] })
    expect(world.calls.map(call => call.body.outcome)).toEqual(['always', 'requirement', 'doctrine', 'once'])
    expect(world.calls.map(call => call.body.expected_revision)).toEqual([1, 2, 3, 4])
    expect(world.calls[2]!.body.doctrine).toEqual(question.input.doctrine)
    // The result view shows failure independently of successful delivery.
    await page.getByTestId('desk-close').click()
    question.pending = [
      { id: 'inbox', revision: question.revision, kind: 'inbox', state: 'delivered', receipt_state: 'handed_off', deliver_after: '' },
      { id: 'outcome', revision: question.revision, kind: 'outcome', state: 'failed', deliver_after: '', error_code: 'ticket_revision_conflict', error_message: 'Das Ticket wurde geändert; kein Kriterium wurde überschrieben.', effect_ref: 'criterion/n-a1/answer-2',
        effect_data: { retryable: false, ticket_id: 'n-a1', knowledge_id: 'knowledge-1', doctrine_id: 'draft-1', review_required: [{ kind: 'criterion', ref: 'n-a1', why: 'Das bearbeitete Kriterium benötigt eine Prüfung durch eine Person.' }] }, doctrine_state: 'in_review' },
    ]
    await page.getByRole('button', { name: 'Refresh', exact: true }).click(); await expect(page.getByRole('button', { name: 'Refresh', exact: true })).toBeEnabled()
    await page.getByRole('button', { name: /Decided/ }).click(); await page.getByTestId('desk-row-q:question-1').click()
    const effects = page.getByTestId('desk-outcome-effects')
    await expect(effects).toContainText('Outcome failed')
    await expect(effects).toContainText('Das Ticket wurde geändert')
    await expect(effects).toContainText('Automatic retry is blocked')
    await expect(effects).toContainText('Person review required')
    await expect(effects).toContainText('in_review')
    await expect(page.getByTestId('desk-status')).toContainText('Handed to the agent')
    await expect(effects.getByRole('link', { name: 'Open affected ticket' })).toHaveAttribute('href', '/work/n-a1')
    await expect(effects.getByRole('link', { name: 'Open decision knowledge' })).toHaveAttribute('href', '/api/nodes/knowledge-1')
    await expect(effects.getByRole('link', { name: /Review doctrine draft/ })).toHaveAttribute('href', '/settings/agent-rules')
    await expectStableControls({ controls: { decide: page.getByTestId('desk-decide'), skip: page.getByTestId('desk-skip'), pager: page.getByTestId('desk-pager'), stamps: page.getByTestId('desk-stamps'), clicked: page.getByTestId('stamp-requirement'), frame: page.getByTestId('desk-frame') }, scrollAreas: { body: page.getByTestId('desk-body') }, interactions: [{ name: 'select a corrective outcome while its failed effect stays visible', run: async () => { await page.getByTestId('stamp-requirement').click(); await expect(page.getByTestId('desk-decide')).toContainText('Replace'); await expect(effects).toContainText('Outcome failed') } }] })
    await effects.evaluate(element => element.scrollIntoView({ block: 'start' }))
    const dir = join(process.cwd(), 'test-results', 'aeon-569-fix2'); await mkdir(dir, { recursive: true })
    await page.screenshot({ path: join(dir, `outcome-review-${width}-${theme}.png`) })
  })
}

test('current server restrictions explain and block publishing without sending a write', async ({ page }) => {
  const world = await mockDecisionDesk(page), question = world.questions[0]!
  question.suggested_outcome = 'always'
  const capability = question.outcomes!.find(row => row.outcome === 'always')!
  capability.available = false; capability.why = 'This outcome requires knowledge.write permission.'
  await page.goto('/decision-desk?item=q:question-1')
  await expect(page.getByTestId('stamp-always')).toBeDisabled()
  await expect(page.getByTestId('desk-status')).toContainText(capability.why)
  await page.getByTestId('desk-decide').click()
  await expect(page.getByTestId('desk-status')).toContainText(capability.why)
  expect(world.calls).toHaveLength(0)
  await page.getByTestId('stamp-once').click(); await page.getByTestId('desk-decide').click()
  await expect.poll(() => world.calls.length).toBe(1)
  expect(world.calls[0]!.body.outcome).toBe('once')
})

test('a directly linked canonical tier request beyond 30 sessions and 100 history rows remains actionable', async ({ page }) => {
  const world = await mockDecisionDesk(page, { tier: true })
  const makeSession = (id: string) => ({ id, project_id: 'p-aeon', agent_principal_id: 'agent-1', ticket_node_id: 'n-a1', harness: 'codex', host: 'test', phase: 'working', activity: 'busy', heartbeat_at: new Date().toISOString(), created_at: '2026-10-02T08:00:00Z', management_mode: 'managed', advertised_capabilities: ['service_tier_v1'], process_ownership: world.ownership, revision: 1, row_version: 1 })
  await page.route('**/api/harness-sessions?**', route => route.fulfill({ json: { items: Array.from({ length: 30 }, (_, n) => makeSession(`history-${n}`)), next_cursor: null } }))
  await page.route('**/api/projects/p-aeon/harness-sessions/tier-session', route => route.fulfill({ json: makeSession('tier-session') }))
  await page.route('**/api/projects/p-aeon/harness-sessions/history-*/tier', route => {
    const id = new URL(route.request().url()).pathname.split('/')[5]!
    return route.fulfill({ json: { session_id: id, revision: 2, read_only: false, requests: Array.from({ length: 50 }, (_, n) => ({ id: `${id}-old-${n}`, session_id: id, tier: 'fast', reason: 'Earlier request', state: 'declined', created_at: '' })) } })
  })
  await page.goto('/decision-desk?item=t:tier-request-1')
  await expect(page.getByRole('dialog', { name: 'Decision Desk memo' })).toBeVisible()
  await expect(page.getByTestId('desk-status')).not.toContainText('Source details are unavailable')
  await page.getByTestId('choice-0').click(); await expect(page.getByTestId('desk-decide')).toBeEnabled()
  await page.getByTestId('desk-decide').click(); await expect.poll(() => world.calls.length).toBe(1)
  expect(world.calls[0]!.path).toBe('/api/projects/p-aeon/harness-sessions/tier-session/tier/requests/tier-request-1/decision')
  expect(world.calls[0]!.body).toMatchObject({ expected_revision: 2, expected_ownership: world.ownership })
})
