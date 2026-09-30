// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { mkdirSync } from 'node:fs'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { mockRules } from './rules-fixtures'
import type { DoctrineFinding } from '../src/lib/doctrine'

const before = { name: 'gate:validation', rules_version: '26.9.28.1', samples: 12, value: .75, from: '2026-09-15T12:00:00Z', until: '2026-09-29T12:00:00Z' }
const findings: DoctrineFinding[] = [
  { id: 'draft', pattern: 'gate:validation', title: 'Recurring validation findings', count: 9, rules_version: '26.9.28.1', harness: 'codex', ticket_kind: 'ticket', status: 'draft', proposal_id: 'draft', pr_url: 'https://github.com/inspr-at/inspr-modules/pull/42', rule_label: 'Reproduce the failure before reporting the fix as complete.', created_at: before.until, before, metrics: [{ ...before, name: 'review_rounds', value: 2.5, samples: 9 }, { ...before, name: 'time_to_done', value: 3600, samples: 7 }],
    evidence: ['AEON-281', 'AEON-293', 'AEON-305'].map((key, i) => ({ id: `outcome-${i}`, ticket_id: `ticket-${i}`, ticket_key: key, href: `/p/AEON/${key}`, kind: 'review_verdict' })) },
  { id: 'measured', pattern: 'fix_rounds', title: 'Repeated fix rounds', count: 6, rules_version: '26.9.25.1', harness: 'claude', ticket_kind: 'ticket', status: 'observed', rule_label: 'Check the whole fix before requesting another review.', created_at: before.until,
    before: { ...before, name: 'fix_rounds', value: 3.5, samples: 6 }, after: { ...before, name: 'fix_rounds', rules_version: '26.9.29.1', value: 1.5, samples: 8 }, delta: -2, evidence: [] },
  { id: 'private', pattern: 'learning:security', title: 'Repeated security pitfalls', count: 3, rules_version: '26.9.28.1', harness: 'codex', ticket_kind: 'ticket', status: 'internal_note', reason: 'Kept internal to protect private instruction text.', created_at: before.until, before: { ...before, name: 'learning:security', value: .5, samples: 6 }, evidence: [] },
]

async function setup(page: Page, items = findings, agent = false) {
  await mockWork(page, fixtures())
  await mockSettings(page, settingsData())
  await mockRules(page)
  await page.route('**/api/me/permissions**', route => {
    const answer = mockEffectivePermissions('admin')
    answer.workspace.permissions.push('rules.read', 'rules.write', 'rules.publish', 'outcome.read')
    return route.fulfill({ json: answer })
  })
  if (agent) await page.route('**/api/me', route => route.fulfill({ json: { principal: { id: 'agent', name: 'Builder', kind: 'agent', roles: ['admin'] }, tenant: { id: 't1', name: 'INSPR Studio' } } }))
  const calls: string[] = []
  await page.route('**/api/rules/doctrine**', route => {
    const path = new URL(route.request().url()).pathname
    calls.push(path)
    if (path.endsWith('/analysis')) return route.fulfill({ json: { findings: items } })
    if (path.endsWith('/proposals')) return route.fulfill({ json: { proposals: [] } })
    return route.fulfill({ json: { proposals_enabled: true, sources: [{ id: 'source', repository: 'inspr-at/inspr-modules', visibility: 'public', ref: 'v26.9.29.1', commit: 'a'.repeat(40), pinned_at: before.until, paths: [], url: 'https://github.com/inspr-at/inspr-modules', state: 'ready', files: [], skipped: [] }] } })
  })
  return calls
}

for (const width of [1600, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`outcome proposals ${width} ${theme}`, async ({ page }) => {
    const errors = watchErrors(page)
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme: theme })
    const calls = await setup(page)
    await page.goto('/settings/agent-rules')
    const list = page.getByLabel('Proposals', { exact: true })
    await expect(list.getByRole('heading', { name: 'Proposals' })).toBeVisible()
    await expect(list.getByText('Proposed', { exact: true })).toBeVisible()
    await expect(list.getByText('Before', { exact: true })).toHaveCount(3)
    await expect(list.getByText('75%', { exact: false })).toBeVisible()
    await expect(list.getByText('−2 rounds', { exact: true })).toBeVisible()
    await expect(list.getByRole('link', { name: 'Open on GitHub' })).toHaveAttribute('href', findings[0]!.pr_url!)
    await expect(list.getByRole('button', { name: /Approve|Publish|Merge/ })).toHaveCount(0)
    await list.getByText('9 affected tickets · Evidence').click()
    await expect(list.getByRole('link', { name: 'AEON-281' })).toHaveAttribute('href', '/p/AEON/AEON-281')
    await list.getByText('3 affected tickets · Evidence').click()
    await expect(list.getByText(/Kept internal to protect/)).toBeVisible()
    await expect(list.getByText(/codex · ticket · Rules/)).toHaveCount(2)
    await expect(list.getByText(/The comparison will appear/).first()).toBeVisible()
    expect(calls.filter(path => path.endsWith('/analysis')).length).toBeGreaterThan(0)
    expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(1)
    const axe = await new AxeBuilder({ page }).include('.proposals').analyze()
    expect(axe.violations.map(v => v.id)).toEqual([])
    expect(errors).toEqual([])
    if (process.env.OUTCOME_PROPOSAL_SHOTS) {
      mkdirSync(process.env.OUTCOME_PROPOSAL_SHOTS, { recursive: true })
      await list.scrollIntoViewIfNeeded()
      await page.screenshot({ path: `${process.env.OUTCOME_PROPOSAL_SHOTS}/proposals-${width}-${theme}.png`, fullPage: true })
    }
  })
}

test('agent principals never request or see the proposals list', async ({ page }) => {
  const calls = await setup(page, findings, true)
  await page.goto('/settings/agent-rules')
  await expect(page.getByRole('heading', { name: 'Doctrine', exact: true })).toBeVisible()
  await expect(page.getByLabel('Proposals', { exact: true })).toHaveCount(0)
  expect(calls.some(path => path.endsWith('/analysis') || path.endsWith('/proposals'))).toBe(false)
})

test('empty analysis gives one quiet line', async ({ page }) => {
  await setup(page, [])
  await page.goto('/settings/agent-rules')
  await expect(page.getByText('No proposals yet.')).toBeVisible()
})
