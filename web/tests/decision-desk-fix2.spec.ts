// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
import { join } from 'node:path'
import { mockDecisionDesk, refreshDesk } from './decision-desk-fixtures'
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
    await expect(page.getByTestId('stamp-always')).toHaveAttribute('aria-checked', 'true')
    await expect(page.getByTestId('desk-decide')).toBeEnabled()
    // AEON-1339: the bar and frame stay put through the series; a decided answer changes through Change.
    const bar = { decide: page.getByTestId('desk-decide'), skip: page.getByTestId('desk-skip'), close: page.getByTestId('desk-close'), pager: page.getByTestId('desk-pager'), frame: page.getByTestId('desk-frame') }
    await expectStableControls({ controls: { ...bar, selectors: page.getByTestId('desk-stamps'), always: page.getByTestId('stamp-always'), requirement: page.getByTestId('stamp-requirement'), doctrine: page.getByTestId('stamp-doctrine'), choices: page.getByTestId('desk-choices'), clicked: page.getByTestId('choice-row-0') },
      scrollAreas: { body: page.getByTestId('desk-body') }, interactions: (['requirement', 'doctrine', 'always'] as const).map(outcome => ({ name: `preview ${outcome}`, run: async () => {
        await page.getByTestId(`stamp-${outcome}`).click(); await expect(page.getByTestId(`stamp-${outcome}`)).toHaveAttribute('aria-checked', 'true'); await expect(page.getByTestId('desk-stamp')).toHaveText(outcome === 'always' ? 'Always' : outcome === 'doctrine' ? 'Doctrine' : 'Requirement')
      } })) })
    await expectStableControls({ controls: bar, scrollAreas: { body: page.getByTestId('desk-body') }, interactions: [
      { name: 'submit the project-default Always without changing its stamp', run: async () => {
        await page.getByTestId('desk-decide').click(); await expect.poll(() => world.calls.length).toBe(1)
        await expect(page.getByTestId('desk-pager')).toContainText('3 of'); await expect(page.getByTestId('desk-decide')).toBeFocused()
      } },
      { name: 'return to the recorded question', run: async () => { await page.getByTestId('desk-paper').press('ArrowLeft'); await expect(page.getByTestId('desk-decide')).toContainText('Next'); await expect(page.getByTestId('desk-stamp')).toHaveAttribute('aria-label', 'Stamped Always') } },
      ...(['requirement', 'doctrine', 'once'] as const).flatMap((outcome, at) => [
        { name: `change to ${outcome}`, run: async () => { await page.getByTestId('desk-skip').click(); await page.getByTestId(`stamp-${outcome}`).click(); await expect(page.getByTestId(`stamp-${outcome}`)).toHaveAttribute('aria-checked', 'true'); await expect(page.getByTestId('desk-decide')).toContainText('Replace') } },
        { name: `replace with ${outcome} at the displayed revision`, run: async () => { await page.getByTestId('desk-decide').click(); await expect.poll(() => world.calls.length).toBe(at + 2); await expect(page.getByTestId('desk-pager')).toContainText('3 of'); await expect(page.getByTestId('desk-decide')).toBeFocused() } },
        { name: `revisit ${outcome}`, run: async () => { await page.getByTestId('desk-paper').press('ArrowLeft'); await expect(page.getByTestId(`stamp-${outcome}`)).toHaveAttribute('aria-checked', 'true') } },
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
    await refreshDesk(page, world)
    await page.getByRole('radio', { name: /Decided/ }).click(); await page.getByTestId('desk-row-q:question-1').click()
    const effects = page.getByTestId('desk-outcome-effects')
    await expect(effects).toContainText('outcome failed; the answer stays recorded')
    await expect(effects).toContainText('Das Ticket wurde geändert')
    await expect(effects).toContainText('It won’t retry by itself')
    await expect(effects).toContainText('Das bearbeitete Kriterium benötigt eine Prüfung durch eine Person.')
    // The delivery itself is one line in the head, apart from the outcome's failure (AEON-1339).
    await expect(page.getByTestId('desk-line')).toContainText('waiting for it to read')
    await expect(effects.getByRole('link', { name: 'Open the ticket' })).toHaveAttribute('href', '/work/n-a1')
    await expect(effects.getByRole('link', { name: 'Open the decision in Knowledge' })).toHaveAttribute('href', '/api/nodes/knowledge-1')
    await expect(effects.getByRole('link', { name: 'Review the doctrine draft' })).toHaveAttribute('href', '/settings/agent-rules')
    await expect(effects.getByRole('link', { name: 'Open the criterion to review' })).toHaveAttribute('href', '/work/n-a1')
    await page.getByTestId('desk-paper').press('c')
    await expectStableControls({ controls: { decide: page.getByTestId('desk-decide'), skip: page.getByTestId('desk-skip'), pager: page.getByTestId('desk-pager'), stamps: page.getByTestId('desk-stamps'), clicked: page.getByTestId('stamp-requirement'), frame: page.getByTestId('desk-frame') }, scrollAreas: { body: page.getByTestId('desk-body') }, interactions: [{ name: 'select a corrective outcome while its failed effect stays visible', run: async () => { await page.getByTestId('stamp-requirement').click(); await expect(page.getByTestId('desk-decide')).toContainText('Replace'); await expect(effects).toContainText('outcome failed') } }] })
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
  await expect(page.getByTestId('stamp-once')).toHaveAttribute('aria-checked', 'true')
  // The quiet note under the stamps; the server's restriction in plain words on the note and the stamp.
  await expect(page.getByTestId('desk-suggest-off')).toHaveText('Suggested: Always · not available for this question')
  await expect(page.getByTestId('desk-suggest-off')).toHaveAttribute('data-tip', 'Not available: you can’t write decisions to Paimos Aeon’s Knowledge.')
  await expect(page.getByTestId('stamp-always')).toHaveAttribute('data-tip', 'Not available: you can’t write decisions to Paimos Aeon’s Knowledge.')
  // Always stays unselected. Its shortcut publishes nothing.
  await page.getByTestId('desk-paper').press('a')
  await expect(page.getByTestId('stamp-always')).toBeDisabled()
  await expect(page.getByTestId('stamp-once')).toHaveAttribute('aria-checked', 'true')
  expect(world.calls).toHaveLength(0)
  await expect(page.getByTestId('desk-decide')).toBeEnabled()
  await page.getByTestId('desk-decide').click()
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
