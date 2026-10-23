// SPDX-License-Identifier: AGPL-3.0-only
import { expect } from '@playwright/test'
import { fixtures, mockWork } from '../work-fixtures'
import type { Goal, GoalList } from '../../src/lib/goals'
import { LONG, NOW } from './fixtures'
import type { VisualSetup } from './checkpoint'

export const goals: VisualSetup = async (page, profile, theme) => {
  const data = fixtures()
  data.preferences.theme = { choice: theme }
  await mockWork(page, data)
  const steps: Goal['steps'] = [{ id: 'proof', text: 'Nachweise vollständig prüfen', state: 'done' },
    { id: 'handover', text: 'Übergabe und Freigabe abstimmen', state: 'now' }, { id: 'ship', text: 'Freigabe durch die verantwortliche Person', state: 'later' }]
  const goal: Goal = {
    id: 'visual-goal', key: 'AEON-1670', title: LONG, kind: 'goal', project_id: 'p-aeon', workspace: false,
    expansion: 'full', counts: { outcomes: 3, tickets: 3 }, steps, next_step: steps[1]!,
    projects: [{ id: 'p-aeon', key: 'AEON', name: 'Aeon', group_ids: [] }], waits: { count: 1, question_ids: ['visual-wait'] },
    progress: { pct: 33, computed_pct: 33, source: 'computed', basis: 'count', total: 3, done: 1, items_without_estimate: 0, items_partially_estimated: 0 },
    eta: { source: 'agent', text: 'Wed 23 Sep 13:00', at: new Date(NOW - 3_600_000).toISOString(), partial: false },
    states: { moving: 1, queued: 1, paused: 0, done: 1 }, done: false, passed: true,
    stall: { stalled: false, since: new Date(NOW).toISOString() }, read_through: 10, generation: 1,
    items: ['moving', 'queued', 'done'].map((state, index) => ({ id: `visual-outcome-${index}`, ref: `o${index}`,
      label: `${LONG} · ${index + 1}`, note: '', position: index, keys: [`AEON-${1671 + index}`], state: state as 'moving' | 'queued' | 'done',
      session_ids: [], eta: { source: 'none', partial: false }, passed: false, coverage: { estimated_leaves: 1, total_leaves: 1 }, weight: 1, handed_over: false })),
  }
  const list: GoalList = { items: [goal], totals: goal.states!, totals_partial: false, quiet_projects: { count: 1, keys: ['PHAROS'] },
    truncated: false, read_through: 10, generation: 1 }
  await page.route(/\/api\/goals(?:\?.*)?$/, route => route.fulfill({ json: list, headers: { 'Aeon-Contract': 'goals/1.4' } }))
  await page.route('**/api/goals/*/series?*', route => route.fulfill({ json: { range: '24h', expansion: 'full', points: [
    { t: new Date(NOW - 86_400_000).toISOString(), pct: 20, basis: 'count', delta: 0, keys: [], changed_keys: [], keys_truncated: false },
    { t: new Date(NOW).toISOString(), pct: 33, basis: 'count', delta: 13, keys: ['AEON-1671'], changed_keys: ['AEON-1671'], keys_truncated: false },
  ], read_through: 10, generation: 1 } }))
  await page.route('**/api/goals/*/history*', route => route.fulfill({ json: { items: [], read_through: 10, generation: 1 } }))
  await page.route('**/api/decision-desk/instances', route => route.fulfill({ json: { here: 'ppm', siblings: [] } }))
  await page.route('**/api/decision-desk/projection*', route => route.fulfill({ json: { items: [{ id: 'visual-wait', kind: 'question',
    project_id: 'p-aeon', revision: 1, title: 'Welche Freigabe ist für diese Übergabe erforderlich?', created_at: new Date(NOW).toISOString(),
    held: true, href: '', source: '' }], counts: { open: 1, held: 1, chores: 0 }, has_more: false, as_of: new Date(NOW).toISOString() } }))
  await page.goto('/?view=goals')
  await expect(page.locator('.goal[data-goal-id="visual-goal"]')).toBeVisible()
  const mode = profile === 'desktop' ? 'Expert' : 'Simple'
  await expect(page.getByRole('radio', { name: mode, exact: true }), `Tick fixture prerequisite: Goals v6 ${mode} view`).toBeVisible()
  await page.getByRole('radio', { name: mode, exact: true }).click()
  if (profile === 'desktop') {
    await page.locator('.goal[data-goal-id="visual-goal"] .goal-toggle').click()
    await expect(page.getByText('at this pace', { exact: false })).toBeVisible()
  } else {
    expect((await page.locator('.goal[data-goal-id="visual-goal"]').boundingBox())!.height).toBe(56)
  }
}
