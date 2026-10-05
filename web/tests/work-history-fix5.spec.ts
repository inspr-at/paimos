// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { controlStability } from './control-stability'
import { mockEffectivePermissions } from './authz-fixtures'
import type { TicketReview } from '../src/lib/reviews'

const base = 'a'.repeat(40), head = 'b'.repeat(40)
const history: TicketReview = {
  ticket_node_id: 'n-1', work_order_id: 'existing-review', request_id: 'existing-request', run_id: null,
  repository: 'example/review-fixture', base_sha: base, head_sha: head, author_run_id: null, author_family: 'openai',
  reviewer_profile_id: null, reviewer_family: 'anthropic', reviewer_model: 'review-model', reviewer_effort: 'xhigh',
  reviewer_profile_version: 'pinned', effective_model: 'review-model', pull_request: null,
  status: 'completed', gate_open: false, gate_reason: 'Changes requested.', github_status: 'unconfigured',
  result: { verdict: 'changes', reason: '', findings: [{ severity: 'high', file: 'internal/nodes/list.go', line: 10, message: 'Ausführliche Prüfung der übergeordneten Arbeitsschritte und ihrer vorhandenen Ergebnisse.' }] },
  ladder: [], cost_micros: null, duration_seconds: null, created_at: '2026-10-04T10:00:00Z',
}

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark']) {
  test(`migrated work keeps history and review actions ${width} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    await page.clock.setSystemTime(new Date('2026-10-04T10:00:00Z'))
    const data = fixtures(); data.preferences.theme = { choice: theme }
    const node = data.nodes.find(n => n.id === 'n-1')!
    Object.assign(node, { kind_slug: 'work', parent_id: 'p-pharos', is_leaf: true, work_children_count: 0,
      title: 'Arbeitsschritt mit vorhandenen Ergebnissen und ausführlicher unabhängiger Prüfung' })
    const errors = watchErrors(page)
    await mockWork(page, data, { admin: true })
    // Mirror the production grants required by the review reader/requester.
    await page.route('**/api/me/permissions*', route => {
      const project = new URL(route.request().url()).searchParams.get('project_id') ?? undefined
      const grants = mockEffectivePermissions('admin', project)
      grants.workspace.permissions.push('work_orders.read', 'run.create')
      if (grants.project) grants.project.permissions.push('work_orders.read', 'run.create')
      return route.fulfill({ json: grants })
    })
    const reads: string[] = [], writes: { path: string; body: Record<string, unknown> }[] = []
    await page.route('**/api/outcomes*', route => {
      const query = new URL(route.request().url()).searchParams
      reads.push(query.get('ticket_node_id') ?? '')
      return route.fulfill({ json: { outcomes: [{ id: 'existing-outcome', kind: 'review_verdict', ticket_key: node.key,
        rules_version: null, release_title: null, recorded_at: '2026-10-04T09:00:00Z',
        payload: { verdict: 'ok', summary: 'Vorhandene Ergebnisse bleiben nach der Migration unverändert sichtbar.' } }] } })
    })
    await page.route('**/api/nodes/*/reviews', route => {
      if (route.request().method() === 'GET') return route.fulfill({ json: [history] })
      const body = route.request().postDataJSON()
      writes.push({ path: new URL(route.request().url()).pathname, body })
      return route.fulfill({ status: 201, json: { ...history, work_order_id: 'new-review', request_id: body.request_id,
        status: 'queued', result: { verdict: '', reason: '', findings: [] }, gate_reason: 'Awaiting independent review.' } })
    })
    await page.goto(`/p/PHAROS/${node.key}?closed=1`)
    const workspace = page.getByRole('complementary', { name: 'Ticket details' })
    const outcomes = workspace.getByRole('region', { name: 'Outcomes' })
    const reviews = workspace.getByRole('region', { name: 'Cross-family review' })
    await expect(outcomes).toContainText('Review ok')
    await expect(reviews).toContainText('Changes needed')
    expect(reads).toEqual(['n-1'])
    const summary = reviews.getByText('Review a commit range', { exact: true })
    const headerStable = await controlStability(page, { edit: workspace.getByRole('button', { name: 'Edit', exact: true }), summary })
    await headerStable.check(async () => {
      await summary.click()
      await reviews.getByLabel('Repository', { exact: true }).fill(history.repository)
      await reviews.getByLabel('Base commit').fill(base)
      await reviews.getByLabel('Head commit').fill(head)
    })
    headerStable.done()
    const submit = reviews.getByRole('button', { name: 'Request review', exact: true })
    const author = reviews.getByLabel('Author family')
    const choicesStable = await controlStability(page, { summary, author, submit })
    for (const family of ['openai', 'anthropic', 'xai', 'openai']) {
      await choicesStable.check(async () => { await author.selectOption(family) })
    }
    choicesStable.done()
    await reviews.screenshot({ path: `test-results/aeon-655-wn-fix5/review-${width}-${theme}.png` })
    await outcomes.screenshot({ path: `test-results/aeon-655-wn-fix5/outcomes-${width}-${theme}.png` })
    await submit.click()
    await expect(reviews).toContainText('Awaiting independent review.')
    expect(writes).toHaveLength(1)
    expect(writes[0].path).toBe('/api/nodes/n-1/reviews')
    expect(writes[0].body).toMatchObject({ repository: history.repository, base_sha: base, head_sha: head, author_family: 'openai' })
    expect(writes[0].body.request_id).toMatch(/^[0-9a-f-]{36}$/)
    await expect(reviews).toContainText('Changes needed')
    await expect(outcomes).toContainText('Review ok')
    expect(errors).toEqual([])
  })
}
