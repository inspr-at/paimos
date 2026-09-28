// SPDX-License-Identifier: AGPL-3.0-only
// AEON-240: synthetic routes only. Authorized coordinator/Cursor QA:
// python3 /Users/markus/Code/aeon-worktrees/bin/fleet-gate.py -- npm test -- session-attention.spec.ts --workers=2
import { test, expect } from '@playwright/test'
import { mockStateColours, stateNow } from './state-colours-fixtures'

for (const theme of ['light', 'dark'] as const) for (const width of [1440, 390]) {
  test(`${theme} ${width}: shared replies leave five siblings working and Needs you empty`, async ({ page }, testInfo) => {
    const { agents, data } = await mockStateColours(page, theme)
    await page.setViewportSize({ width, height: width === 1440 ? 1000 : 844 })
    const template = agents.sessions[0]!
    const shared = { kind: 'reply', scope: 'shared', actor: 'agent', count: 1, blocking: false, location: 'messages' }
    agents.sessions.splice(0, agents.sessions.length, ...Array.from({ length: 5 }, (_, i) => ({
      ...template, id: `bk5-worker-${i}`, display_label: `Backlog worker ${i + 1}`,
      agent_principal_id: template.agent_principal_id, run_id: null, needs_attention: false,
      attention_reasons: [shared], activity_note: 'Implementing the assigned change',
    })))
    agents.approvals.splice(0)
    data.projects.splice(1)
    await page.goto('/agents')
    const queue = page.getByRole('region', { name: 'Needs you' })
    await expect(queue).toContainText('Nothing waits on you')
    for (const session of agents.sessions) {
      const row = page.locator(`[data-row="s:${session.id}"]`)
      await expect(row).toHaveAttribute('data-state', 'working')
      await expect(row.locator('.agent-state-label')).toHaveText('Working')
    }
    await expect(page.locator('.row[data-state="waiting"]')).toHaveCount(0)
    await page.screenshot({ path: testInfo.outputPath(`aeon-240-${theme}-${width}-working-siblings.png`), fullPage: true })
    await page.locator('[data-row="s:bk5-worker-0"] .agent-link').click()
    const panel = page.getByRole('complementary', { name: 'Session details' })
    await expect(panel.locator('.now-step')).toHaveText('Implementing the assigned change')
    await expect(panel.locator('.head-top .agent-state-label')).toHaveText('Working')
    const inbox = panel.getByRole('region', { name: 'Inbox attention' })
    await expect(inbox).toContainText('Shared inbox')
    await expect(inbox).toContainText('waiting for another agent')
    await expect(inbox).toContainText('No session ownership is recorded')
    await expect(inbox.getByRole('link', { name: 'Shared inbox messages' })).toHaveAttribute('href', '#messages-title')
    await expect(panel.locator('.callout')).toHaveCount(0)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    await page.screenshot({ path: testInfo.outputPath(`aeon-240-${theme}-${width}-shared-inbox.png`), fullPage: true })
    await page.goto('/')
    const project = page.locator('[data-project-id="p-sc1-working"]')
    await expect(project.locator('.chip-state')).toHaveText('Working')
    await expect(project).toHaveAttribute('data-agent-state', 'working')
    // Exact bound-run evidence makes only its owner wait for a person.
    Object.assign(agents.sessions[0]!, { run_id: 'bk5-run', needs_attention: true,
      attention_reasons: [{ kind: 'approval', scope: 'run', actor: 'person', count: 1, blocking: true, location: 'approvals' }, shared] })
    agents.approvals.push({ id: 'bk5-approval', agent_principal_id: template.agent_principal_id, agent_name: 'Shared fixture principal', resource_kind: 'run', resource_id: 'bk5-run', run_id: 'bk5-run', scope: 'nodes.read', rationale: '', risk: 'low', decision: null, decided_by_principal_id: null, expires_at: new Date(stateNow + 60_000).toISOString(), proposed_at: new Date(stateNow).toISOString() })
    await page.goto('/agents/bk5-worker-0')
    await expect(queue).not.toContainText('Nothing waits on you')
    await expect(panel.locator('.head-top .agent-state-label')).toHaveText('Awaiting approval')
    await expect(panel.locator('.now-step')).toContainText('awaiting a person’s decision')
    await expect(panel.getByRole('region', { name: 'Session state evidence' }).getByRole('link', { name: 'Permission requests' })).toHaveAttribute('href', '/agents#needs-title')
    await page.getByRole('button', { name: 'Close session details' }).click()
    await expect(page.locator('[data-row="s:bk5-worker-1"]')).toHaveAttribute('data-state', 'working')
    await page.goto('/')
    await expect(project.locator('.chip-state')).toHaveText('Awaiting approval')
    // A late approval list must not override the fresh explicit false projection.
    Object.assign(agents.sessions[0]!, { needs_attention: false, attention_reasons: [] })
    await page.goto('/agents/bk5-worker-0')
    await expect(panel.locator('.head-top .agent-state-label')).toHaveText('Working')
    await expect(panel.locator('.callout')).toHaveCount(0)
    await expect(panel.getByRole('region', { name: 'Inbox attention' })).toHaveCount(0)
  })
}
