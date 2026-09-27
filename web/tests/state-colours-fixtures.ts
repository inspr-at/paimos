// SPDX-License-Identifier: AGPL-3.0-only
import type { Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { STATE_LABEL, type AgentState } from '../src/lib/agentSignals'

export const stateNow = Date.parse('2026-09-23T12:00:00Z')
export const states: AgentState[] = ['working', 'waiting', 'throttled', 'problem', 'idle', 'stale', 'stopped']
export async function mockStateColours(page: Page, theme: 'light' | 'dark' = 'light') {
  await page.clock.setSystemTime(stateNow)
  const data = fixtures()
  data.preferences.theme = { choice: theme }
  data.preferences.projects = { view: 'cards' }
  data.nodes.splice(0)
  const project = data.projects[0]!
  data.projects.splice(0, data.projects.length, ...states.map((state, index) => ({ ...project,
    id: `p-sc1-${state}`, key: `PRJ-${index + 1}`, classic: `SC${index + 1}`, title: `${STATE_LABEL[state]} team`, description: 'Agent state preview', state: 'active',
  })))
  const agents = agentData({ me: me.id, now: stateNow, projects: { pharos: project.id, aeon: project.id, pai: project.id }, tickets: { fleet: '', restore: '', web: '', release: '', approvals: '' } })
  for (const [index, state] of states.entries()) {
    Object.assign(agents.sessions[index]!, {
      project_id: `p-sc1-${state}`, project: { id: `p-sc1-${state}`, key: `SC${index + 1}`, title: `${STATE_LABEL[state]} team` },
      display_label: `SC1 ${state}`, ticket: null, ticket_node_id: null, run_id: null, parent_harness_session_id: null,
      model: 'integration-model', reasoning_effort: 'xhigh', account_label: 'Test account', harness_version: '1.2.3',
      brief: 'AEON-221', worktree: '/Code/aeon-sc1', branch: 'sc1.state-colours',
      commits: [{ sha: 'abc1234', subject: 'Integrate session states' }],
      phase: state === 'stopped' ? 'stopped' : 'working', activity: state === 'throttled' ? 'throttled' : ['idle', 'stale'].includes(state) ? 'idle' : 'busy',
      heartbeat_at: new Date(stateNow - (state === 'problem' ? 600_000 : state === 'stale' ? 300_000 : 10_000)).toISOString(),
      stopped_at: state === 'stopped' ? new Date(stateNow).toISOString() : null, stop_reason: state === 'stopped' ? 'process_exited' : null,
      needs_attention: state === 'waiting', created_at: new Date(stateNow - 1200_000).toISOString(),
    })
  }
  agents.sessions.splice(states.length)
  const approval = agents.approvals[0]!
  Object.assign(approval, { agent_principal_id: agents.sessions[1]!.agent_principal_id, resource_id: 'p-sc1-waiting', run_id: null, scope: 'nodes.read', risk: 'low', rationale: 'Confirm the next step.' })
  agents.approvals.splice(0, agents.approvals.length, approval)
  agents.runs.splice(0); agents.messages.splice(0); agents.targets.splice(0); agents.accounts.splice(0)
  await mockWork(page, data, { admin: true })
  await mockSettings(page, settingsData())
  await mockAgents(page, agents)
  await page.route('**/api/harness-sessions/live*', route => route.fulfill({ json: {
    at: new Date(stateNow).toISOString(), fresh_seconds: 120, truncated: false,
    items: agents.sessions.map(s => ({ ...s, session_id: s.id, principal_id: s.agent_principal_id, name: s.display_label, since: s.created_at, ticket: null })),
  } }))
  return { data, agents }
}
