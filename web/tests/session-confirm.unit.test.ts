// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { reactive, ref } from 'vue'
import { api, sessionEnded, type Identity } from '../src/lib/api'
import { clearPermissions } from '../src/lib/authz'
import { confirmAction, confirmState, settleConfirm } from '../src/lib/confirm'
import { useSession } from '../src/stores/session'
import { deferred, setupSource } from './record-source'
import * as WorkVocabulary from '../src/lib/workVocabulary'

vi.mock('../src/lib/api', () => ({ api: vi.fn(), getSession: vi.fn(), sessionEnded: { blocked: false, handler: null } }))
vi.mock('../src/lib/theme', () => ({ restoreTheme: async () => {}, resetTheme: () => {} }))
const person = (id: string, tenant = 'tenant'): Identity => ({ principal: { id, name: id, kind: 'person' }, tenant: { id: tenant, name: 'Workspace' } })
const stopped: (() => void)[] = []

beforeEach(() => {
  setActivePinia(createPinia())
  vi.resetAllMocks()
  const media = { matches: false, addEventListener() {}, removeEventListener() {} }
  vi.stubGlobal('window', { history: { state: {} }, matchMedia: () => media, addEventListener() {}, removeEventListener() {} })
  vi.stubGlobal('navigator', { platform: 'MacIntel' })
  vi.stubGlobal('document', { addEventListener() {}, removeEventListener() {}, getElementById: () => null, querySelector: () => null, visibilityState: 'visible' })
  sessionEnded.blocked = false; sessionEnded.handler = null
  clearPermissions()
  vi.mocked(api).mockResolvedValue(new Response(null, { status: 200 }))
})
afterEach(() => {
  settleConfirm(false)
  stopped.splice(0).forEach(stop => stop())
  useSession().$dispose()
  sessionEnded.handler = null
  vi.unstubAllGlobals()
})

function deletingTicket() {
  const remove = vi.fn(async () => true), confirmation = vi.fn(confirmAction)
  const item = { id: 'A', key: 'AEON-1', kind_slug: 'ticket', title: 'A', fields: {}, children_count: 0 }
  const props = reactive({ item, project: { id: 'p', routeKey: 'AEON' }, names: new Map(), canDelete: true, people: [], me: null })
  const workspace = setupSource('components/work/TicketWorkspace.vue', props, {
    'vue-router': { useRouter: () => ({}) }, '../../lib/api': {}, '../../lib/confirm': { confirmAction: confirmation },
    '../../lib/rowStore': { rowStore: { row: () => null, adopt: (row: unknown) => row } }, '../../lib/toast': {}, '../../lib/workQueue': {},
    '../../lib/liveNodes': { liveNodes: { state: 'live', onState: () => () => {} } }, '../../lib/eta': { etaFromTicket: () => null },
    '../../lib/useActivity': { useActivity: () => ({}) }, '../../lib/useTicket': { useTicket: () => ({ readOnly: ref(false), gone: ref(false), remove }) },
    '../../lib/work': { kindLabel: () => 'Ticket' }, '../../lib/useAttachments': { useAttachments: () => ({}) }, '../../lib/doneGate': {},
    '../../lib/workVocabulary': WorkVocabulary,
    '../../stores/workVocabulary': { useWorkVocabulary: () => ({ value: { revision: 0, leaf: { name: '', icon: '' }, levels: [] } }) },
    '../../lib/developerSettings': { useDeveloperSettings: () => ({ showExpertStart: ref(false) }) },
    '../../lib/recurrences': {}, '../../lib/useIdentityScope': { useIdentityScope: () => ({ owner: ref(''), reset() {} }) },
    '../../lib/ticketBenefits': { benefitDraft: () => ({}) }, '../../lib/authz': { can: () => false }, '../../lib/releaseAssign': {}, '../../lib/releaseMembership': {},
    '../../stores/workQueue': { useWorkQueue: () => ({ load: async () => {} }) },
    '../../lib/usePolledData': { usePoller: () => ({ start() {}, stop() {} }) }, '../../stores/session': { useSession },
  })
  stopped.push(workspace.stop)
  const deleting = workspace.state.remove()
  expect(confirmState.request?.title).toBe('Delete AEON-1?')
  expect(confirmation).toHaveBeenCalledTimes(1)
  return { remove, deleting, confirmation: confirmation.mock.results[0]!.value as Promise<boolean> }
}

it.each([
  ['principal', person('grace')],
  ['tenant', person('ada', 'other-tenant')],
] as const)('AEON-584: a %s change cancels an open confirm and its captured ticket delete', async (_change, nextIdentity) => {
  const session = useSession()
  session.identity = person('ada')
  const action = deletingTicket()
  session.identity = nextIdentity
  // A late click on the old dialog must not revive the captured action.
  settleConfirm(true)
  await expect(action.confirmation).resolves.toBe(false)
  await action.deleting
  expect(confirmState.request).toBeNull()
  expect(confirmState.resolve).toBeNull()
  expect(action.remove).not.toHaveBeenCalled()
})

it('AEON-584: sign-out cancels an open confirm before the logout response', async () => {
  const session = useSession(), logout = deferred<Response>()
  session.identity = person('ada')
  vi.mocked(api).mockImplementationOnce(() => logout.promise)
  const action = deletingTicket(), signingOut = session.signOut()
  expect(api).toHaveBeenCalledExactlyOnceWith('/auth/logout', { method: 'POST' })
  expect(session.identity?.principal.id).toBe('ada')
  settleConfirm(true)
  // Release the controlled response even when an assertion fails.
  try {
    await expect(action.confirmation).resolves.toBe(false)
    await action.deleting
    expect(confirmState.request).toBeNull()
    expect(confirmState.resolve).toBeNull()
    expect(action.remove).not.toHaveBeenCalled()
  } finally {
    logout.resolve(new Response(null, { status: 200 }))
    await signingOut
  }
})

it('AEON-584: refreshing the same identity keeps confirmation of the captured delete', async () => {
  const session = useSession()
  session.identity = person('ada')
  const action = deletingTicket()
  session.identity = person('ada')
  expect(confirmState.request?.title).toBe('Delete AEON-1?')
  settleConfirm(true)
  await expect(action.confirmation).resolves.toBe(true)
  await action.deleting
  expect(action.remove).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({ id: 'A' }))
})
