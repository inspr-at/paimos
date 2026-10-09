// SPDX-License-Identifier: AGPL-3.0-only
// Risk: a disabled control still opens/submits a start, or cancelling an existing
// intent gets blocked by launch policy. Exercise the real component handlers.
import { afterEach, expect, it, vi } from 'vitest'
import { computed, reactive, ref } from 'vue'
import * as leadAPI from '../src/lib/lead'
import { flush, setupSource } from './record-source'

const scopes: { stop(): void }[] = []
afterEach(() => { scopes.splice(0).forEach(scope => scope.stop()); vi.unstubAllGlobals() })
const fresh = (patch: Partial<leadAPI.ProjectLead> = {}): leadAPI.ProjectLead => ({ project_id: 'p1', state: 'none', reason: '', revision: 0, generation: 0, session_id: null, process_active: false, automatic_launch_enabled: false, ...patch })
function environment(initial = fresh()) {
  const current = ref(initial)
  const start = vi.fn(), pause = vi.fn(), open = vi.fn()
  const leads = { views: reactive({ p1: { lead: current } }), busy: {}, load: async () => {}, loadLead: async () => {}, loadMany: async () => {}, start, pause }
  const session = { identity: { tenant: { id: 't1' }, principal: { id: 'person', kind: 'person' } } }
  const modules = {
    'vue-router': { useRouter: () => ({ push: vi.fn() }) },
    '../../stores/session': { useSession: () => session },
    '../../stores/projectLeads': { useProjectLeads: () => leads },
    '../../stores/workQueue': { useWorkQueue: () => ({ load: async () => {}, snapshots: {} }) },
    '../../lib/authz': { can: () => true },
    '../../lib/lead': leadAPI,
    '../../lib/leadOverlay': { openStartLead: open, openLeadPause: vi.fn(), openLeadPanel: vi.fn(), closeLeadSheet: vi.fn(), leadOverlay: reactive({ start: {} }) },
    '../../lib/toast': { toast: vi.fn() },
    '../../lib/usePolledData': { usePoller: () => ({ start() {}, stop() {} }) },
    '../../lib/useLeadSummary': { useLeadSummary: () => ({ lead: current, band: computed(() => leadAPI.leadBand(current.value, 'AIT', 0)), leadSession: ref(null), workers: ref([]), stations: ref([]), questions: ref([]), questionsPartial: ref(false) }) },
    '../../lib/leadCardFold': { useLeadCardFold: () => ({ ready: ref(true), collapsed: ref(false), moving: ref(false), toggle() {}, settle() {} }) },
  }
  return { current, start, pause, open, modules }
}

it('AEON-1038: the card disables Start and Resume with a reason, preserves Cancel, and names actual check failures', async () => {
  const env = environment()
  const component = setupSource('components/lead/LeadBand.vue', { projectId: 'p1', projectKey: 'AIT', routeKey: 'AIT' }, env.modules)
  scopes.push(component)
  expect(component.state.action.value).toMatchObject({ disabled: true, label: 'Start lead', tip: leadAPI.LEAD_LAUNCH_OFF })
  await component.state.act({ currentTarget: {} })
  expect(env.open).not.toHaveBeenCalled()
  env.current.value = fresh({ state: 'waiting_for_room', revision: 1, reason: 'start_checks_unavailable' })
  expect(component.state.band.value.now).toContain(leadAPI.LEAD_LAUNCH_OFF)
  expect(component.state.action.value).toMatchObject({ disabled: false, label: 'Cancel start' })
  await component.state.act({ currentTarget: {} })
  expect(env.pause).toHaveBeenCalledWith('p1')
  env.current.value = fresh({ state: 'waiting_for_room', revision: 2, reason: 'host_unavailable' })
  expect(component.state.band.value.status).toContain('host load can’t be read')
  env.current.value = fresh({ state: 'waiting_for_room', reason: 'runtime_pickup_timeout', revision: 1, automatic_launch_enabled: true })
  expect(component.state.band.value.status).toBe('Nothing picked this up')
  env.current.value = fresh({ state: 'paused', revision: 2 })
  expect(component.state.action.value).toMatchObject({ disabled: true, label: 'Resume', tip: leadAPI.LEAD_LAUNCH_OFF })
  await component.state.act({ currentTarget: {} })
  expect(env.start).not.toHaveBeenCalled()
  env.current.value = fresh({ automatic_launch_enabled: undefined })
  expect(component.state.action.value.disabled).toBe(true)
  expect(component.state.action.value.tip).toContain('could not be read')
  const list = setupSource('components/lead/LeadsList.vue', {}, {
    ...env.modules,
    '../../stores/projects': { useProjects: () => ({ projects: [{ id: 'p1', routeKey: 'AIT' }] }) },
    '../../stores/agents': { useAgents: () => ({ views: [] }) },
    '../../lib/workQueue': { queueRequest: async () => ({ items: [] }) },
  })
  scopes.push(list)
  expect(list.state.launchAvailable.value).toBe(false)
  expect(list.state.launchReason.value).toContain('could not be read')
  env.current.value = fresh()
  expect(list.state.launchReason.value).toBe(leadAPI.LEAD_LAUNCH_OFF)
})

it('AEON-1038: a sheet with launch off rejects pointer and keyboard submissions before any settings write', async () => {
  vi.stubGlobal('navigator', { platform: 'MacIntel', userAgent: 'test' })
  // A stale snapshot can open the sheet; fresh settings must still close the gate.
  const env = environment(fresh({ automatic_launch_enabled: true }))
  const write = vi.fn()
  const component = setupSource('components/lead/StartLeadSheet.vue', { projects: ['p1'] }, {
    ...env.modules,
    '../../lib/lead': { ...leadAPI, readLeadSettings: async () => ({ revision: 1, details_redacted: false, automatic_launch_enabled: false, overrides: {} }), writeLeadSettings: write },
    '../../lib/api': { api: async () => new Response(JSON.stringify({ total: 5 })) },
    '../../lib/agentPairing': { listPairingComputers: async () => [] },
    '../../stores/projects': { useProjects: () => ({ byId: () => ({ routeKey: 'AIT' }) }) },
  })
  scopes.push(component)
  await component.state.load(); await flush()
  component.state.host.value = 'host-1'
  expect(component.state.launchAvailable.value).toBe(false)
  expect(component.state.launchReason.value).toBe(leadAPI.LEAD_LAUNCH_OFF)
  await component.state.start()
  component.state.keydown({ key: 'Enter', metaKey: true, target: { matches: () => false }, preventDefault() {}, stopPropagation() {} })
  await flush()
  expect(write).not.toHaveBeenCalled()
  expect(env.start).not.toHaveBeenCalled()
})
