// SPDX-License-Identifier: AGPL-3.0-only
import { beforeEach, expect, it, vi } from 'vitest'
import { reactive } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { useAgentPause } from '../src/stores/agentPause'
import type { HarnessSessionRow } from '../src/lib/agentRows'
import type { HarnessSession } from '../src/lib/agents'
import { toasts } from '../src/lib/toast'
const calls = vi.hoisted(() => ({ pause: vi.fn(), resume: vi.fn(), leaving: vi.fn() }))
vi.mock('../src/lib/agentRows', () => ({ pauseSession: calls.pause, resumeSession: calls.resume, leavingReport: calls.leaving }))
vi.mock('../src/lib/authz', () => ({ can: () => true }))
vi.mock('../src/lib/agents', () => ({ message: (e: unknown) => e instanceof Error ? e.message : 'failed', readPauseDefault: async () => ({ default_level: 'pause' }), readEstimateInterval: async () => ({ interval_minutes: 10 }), savePauseDefault: async (level: string) => ({ default_level: level }) }))
let identity: { identity: { principal: { id: string; kind: 'person' }; tenant: { id: string } } }
let sessions: HarnessSession[]
const agents = { get sessions() { return sessions }, sessionById: (id: string) => sessions.find(s => s.id === id), recordSession: (row: HarnessSessionRow) => { sessions = sessions.map(s => s.id === row.id ? row as HarnessSession : s) }, afterWrite: async () => {}, admitSessions: (rows: HarnessSessionRow[]) => rows as HarnessSession[] }
vi.mock('../src/stores/agents', () => ({ useAgents: () => agents }))
vi.mock('../src/stores/session', () => ({ useSession: () => identity }))
const row = (id: string, fields: Partial<HarnessSessionRow> = {}) => ({ id, project_id: 'project', run_id: null, host: 'host', display_label: id, role: 'worker', advertised_capabilities: ['pause'], management_mode: 'unmanaged', phase: 'working', stopped_at: null, ...fields }) as HarnessSession
const trigger = { focus: vi.fn() } as unknown as HTMLElement
beforeEach(() => { setActivePinia(createPinia()); identity = reactive({ identity: { principal: { id: 'person', kind: 'person' }, tenant: { id: 'tenant' } } }); sessions = [row('a'), row('b')]; toasts.splice(0); vi.clearAllMocks(); calls.pause.mockImplementation(async (_project, id) => ({ ...sessions.find(s => s.id === id), pause: { control_id: 'accepted', state: 'requested' } })) })
it('reports partial batch results and retries only failed targets', async () => {
  const pause = useAgentPause(); calls.pause.mockRejectedValueOnce(new Error('not owned'))
  pause.open('pause-all', sessions, trigger); await pause.submit()
  expect(pause.error).toContain('1 accepted; 1 failed'); expect(pause.targets.map(t => t.id)).toEqual(['a'])
  await pause.submit(); expect(calls.pause.mock.calls.map(call => call[1])).toEqual(['a', 'b', 'a'])
  expect(pause.mode).toBeNull()
})
it('a run binding changed after the dialog opened prevents a mutation', async () => {
  const pause = useAgentPause(); pause.open('pause', [sessions[0]!], trigger)
  sessions[0] = row('a', { run_id: 'different-run' }); await pause.submit()
  expect(calls.pause).not.toHaveBeenCalled(); expect(pause.error).toContain('session changed')
})
it('changing identity while a batch waits discards the receipt and never sends the next mutation', async () => {
  let release!: (value: unknown) => void
  calls.pause.mockImplementationOnce(() => new Promise(resolve => { release = resolve }))
  const pause = useAgentPause(); pause.open('pause-all', sessions, trigger)
  const writing = pause.submit(); identity.identity.principal.id = 'other-person'; await Promise.resolve()
  release(row('a')); await writing
  expect(calls.pause).toHaveBeenCalledTimes(1); expect(pause.mode).toBeNull(); expect(toasts).toHaveLength(0)
})
it('stop-only agents keep running unless Stop now is explicitly chosen', async () => {
  sessions = [row('a', { advertised_capabilities: [], supported_pause_levels: ['stop_now'] })]
  const pause = useAgentPause(); pause.open('pause-all', sessions, trigger); await pause.submit()
  expect(calls.pause).not.toHaveBeenCalled()
  pause.open('pause-all', sessions, trigger); pause.overrides.a = 'stop_now'; await pause.submit()
  expect(calls.pause).toHaveBeenCalledWith('project', 'a', 'stop_now', '')
})
it('resume requests are issued to leads first and success copy only confirms saved requests', async () => {
  sessions = [row('worker', { phase: 'stopped', pause: { state: 'paused', control_id: 'w' } as HarnessSessionRow['pause'] }), row('lead', { phase: 'stopped', role: 'coordinator', pause: { state: 'paused', control_id: 'l' } as HarnessSessionRow['pause'] })]
  calls.resume.mockImplementation(async (_project, id) => ({ session: { ...sessions.find(s => s.id === id), pause: { state: 'resume_requested' } } }))
  const pause = useAgentPause(); pause.open('resume-all', sessions, trigger); await pause.submit()
  expect(calls.resume.mock.calls.map(c => c[1])).toEqual(['lead', 'worker']); expect(toasts.at(-1)?.message).toContain('saved · awaiting continuation')
})
it('Resume all sends only the selected paused generations', async () => {
  sessions = sessions.map(s => row(s.id, { phase: 'stopped', pause: { state: 'paused', control_id: s.id } as HarnessSessionRow['pause'] }))
  calls.resume.mockImplementation(async (_project, id) => ({ session: sessions.find(s => s.id === id) }))
  const pause = useAgentPause(); pause.open('resume-all', sessions, trigger); pause.resumeIds = ['b']; await pause.submit()
  expect(calls.resume.mock.calls.map(c => c[1])).toEqual(['b'])
})
