// SPDX-License-Identifier: AGPL-3.0-only
import type { Page } from '@playwright/test'
import { fixtures, liveAgent, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'

export const indicatorNow = new Date('2026-09-23T12:00:00Z')
export async function mockIndicator(page: Page) {
  await page.clock.setSystemTime(indicatorNow)
  const data = fixtures()
  data.preferences.projects = { view: 'cards' }
  data.live.push(
    liveAgent({ project_id: 'p-aeon', session_id: 'la3-one', principal_id: '44444444-4444-4444-8444-444444444444', name: 'camy', harness: 'codex', ticket: { id: 'n-a1', key: 'AEON-184', title: 'Agent indicator styles', project_id: 'p-aeon' } }),
    liveAgent({ project_id: 'p-aeon', session_id: 'la3-two', principal_id: '55555555-5555-4555-8555-555555555555', name: 'coordinator', role: 'coordinator' }),
    liveAgent({ project_id: 'p-pharos', session_id: 'la3-stale', name: 'scout', heartbeat_at: '2026-09-23T11:00:00Z' }),
    Object.assign(liveAgent({ project_id: 'p-frozen', session_id: 'la3-waiting', name: 'approval-helper' }), { needs_attention: true }),
  )
  const calls = await mockWork(page, data)
  await mockSettings(page, settingsData())
  const agents = agentData({ me: me.id, now: indicatorNow.getTime(), projects: { aeon: 'p-aeon', pharos: 'p-pharos', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' } })
  await mockAgents(page, agents)
  return { data, calls }
}
