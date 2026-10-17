// SPDX-License-Identifier: AGPL-3.0-only
// Risk (AEON-1453): Delivery's explicit default, saved after its preference has loaded,
// follows the person who owns the page when the write resumes. The previous person's
// patch must not clear the next person's stored default. No sleeps.
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import { sessionEnded } from '../src/lib/api'
import * as deliveryNumbers from '../src/lib/deliveryNumbers'
import * as preferences from '../src/lib/preferences'
import * as statusTiming from '../src/lib/statusTiming'
import * as viewSwitch from '../src/lib/viewSwitch'
import { preferenceOwner, setPreferenceOwner } from '../src/lib/preferences'
import { flush, setupSource } from './record-source'

const who = (person: string) => ({ tenant: { id: 't' }, principal: { id: person } })
const stopped: (() => void)[] = []
const memory = {
  A: { level: 'simple', window: 30, defaults: { level: 'expert', window: 90 } },
  B: { level: 'expert', window: 7, defaults: { level: 'simple', window: 365 } },
}
let saved = new Map<string, unknown>()
let writes: { owner: string; key: string; value: unknown }[] = []
let reads: string[] = []

beforeEach(() => {
  sessionEnded.blocked = false
  saved = new Map<string, unknown>([
    ['t/A/delivery:numbers', memory.A],
    ['t/B/delivery:numbers', memory.B],
    ['t/A/view:delivery', { last: 'numbers' }],
    ['t/B/view:delivery', { last: 'numbers' }],
  ])
  writes = []
  reads = []
  setPreferenceOwner(who('A'))
  vi.stubGlobal('document', { addEventListener() {}, removeEventListener() {} })
  vi.stubGlobal('window', { addEventListener() {}, removeEventListener() {}, innerWidth: 1440, innerHeight: 900 })
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const key = decodeURIComponent(String(input).split('/api/preferences/')[1] ?? '')
    const id = `${preferenceOwner.value}/${key}`
    if ((init?.method ?? 'GET').toUpperCase() === 'PUT') {
      const { value } = JSON.parse(String(init?.body)) as { value: unknown }
      saved.set(id, value); writes.push({ owner: preferenceOwner.value, key, value })
      return new Response('{}')
    }
    reads.push(id)
    return new Response(JSON.stringify({ key, value: saved.get(id) ?? null }))
  }))
})
afterEach(() => { stopped.splice(0).forEach(stop => stop()); setPreferenceOwner(null); vi.unstubAllGlobals() })

async function drain() {
  await flush()
  await new Promise<void>(resolve => {
    const channel = new MessageChannel()
    channel.port1.onmessage = () => { channel.port1.close(); channel.port2.close(); resolve() }
    channel.port2.postMessage(undefined)
  })
  await flush()
}

it('a delayed delivery default never writes one person\'s choice into the next person\'s preferences', async () => {
  const route = { query: {}, path: '/p/AEON/delivery', hash: '' }
  const made = setupSource('components/delivery/DeliveryView.vue', { project: { id: 'p1', routeKey: 'AEON', title: 'Aeon' } }, {
    'vue-router': { useRoute: () => route, useRouter: () => ({ replace: vi.fn() }) },
    '../../lib/delivery': { deliveryLanguage: () => 'en' },
    '../../lib/deliveryNumbers': deliveryNumbers,
    '../../lib/deliveryNumbersText': { deliveryText: () => ({ views: { numbers: 'Numbers', flow: 'Flow' }, viewsLabel: 'View' }), fill: (value: string) => value },
    '../../lib/deliveryFlowModes': { FLOW_MODES: ['live', 'replay', 'compare'] },
    '../../lib/deliveryFlow': { timeLabel: () => '' },
    '../../lib/deliveryFlowText': { flowText: () => ({}), put: () => '' },
    '../../lib/useDeliveryFlow': { useDeliveryFlow: () => ({ data: ref(null) }) },
    '../../lib/preferences': preferences,
    '../../lib/viewSwitch': viewSwitch,
    '../../lib/usePolledData': { usePoller: () => ({ start() {}, stop() {}, restart() {} }) },
    '../../directives/clipTip': { vClipTip: {} },
    '../../stores/profile': { useProfile: () => ({ profile: null, load: async () => {} }) },
    '../../lib/authz': { can: () => false },
    '../../lib/projectSettings': { projectSettingsPath: () => '/settings' },
    '../../lib/projectDeliverySettings': { repositoryState: () => 'ready' },
    '../../lib/deliveryTickets': { ticketsModel: () => ({}) },
    '../../lib/statusTiming': statusTiming,
    '../../lib/useTicketTiming': { useTicketTiming: () => ({ data: ref(null), status: ref('loading'), busy: ref(false), final: ref(null), retry() {} }) },
    '../../lib/workFigures': { shortDay: () => '' },
  })
  stopped.push(made.stop)
  for (let turn = 0; turn < 8 && made.state.prefs.value.window !== 90; turn++) await flush()
  expect(made.state.prefs.value).toEqual({ level: 'expert', window: 90 })
  made.state.setDefault({ level: null })
  queueMicrotask(() => setPreferenceOwner(who('B')))
  await drain()
  for (let turn = 0; turn < 8 && made.state.prefs.value.window !== 365 && !writes.some(write => write.owner === 't/B'); turn++) await flush()
  expect(saved.get('t/B/delivery:numbers')).toEqual(memory.B)
  expect(writes.filter(write => write.owner === 't/B')).toEqual([])
  expect(made.state.prefs.value).toEqual({ level: 'simple', window: 365 })
})
