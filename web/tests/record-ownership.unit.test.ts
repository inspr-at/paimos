// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { reactive, shallowReactive, ref } from 'vue'
import { deferred, flush, setupSource } from './record-source'
import * as CRM from '../src/lib/crm'
import * as Knowledge from '../src/lib/knowledge'
import * as WorkVocabulary from '../src/lib/workVocabulary'
import { APIError } from '../src/lib/api'

const stopped: (() => void)[] = []
function setup(path: string, props: Record<string, unknown>, modules: Record<string, unknown>) {
  const made = setupSource(path, props, modules); stopped.push(made.stop); return made
}
beforeEach(() => {
  const media = { matches: false, addEventListener() {}, removeEventListener() {} }
  vi.stubGlobal('window', { history: { state: {} }, matchMedia: () => media, addEventListener() {}, removeEventListener() {} })
  vi.stubGlobal('navigator', { platform: 'MacIntel' })
  vi.stubGlobal('document', { addEventListener() {}, removeEventListener() {}, getElementById: () => null, querySelector: () => null, visibilityState: 'visible' })
})
afterEach(() => { stopped.splice(0).forEach(stop => stop()); vi.unstubAllGlobals() })

it('S8-001: changing ticket clears description and comment drafts, including a late save', async () => {
  const answer = deferred<string>(), save = vi.fn(() => answer.promise), post = vi.fn(async () => true)
  const props = reactive({ recordId: 'A', title: 'Description', value: 'A saved', editable: true, save })
  const section = setup('components/work/MarkdownSection.vue', props, { '../../lib/confirm': { confirmAction: async () => true } }).state
  await section.start(); section.draft.value = 'A draft'; const saving = section.commit()
  const commentProps = reactive({ recordId: 'A', me: 'Ada', post })
  const composer = setup('components/work/CommentComposer.vue', commentProps, {}).state
  composer.draft.value = 'A comment'
  props.recordId = 'B'; props.value = 'B saved'; commentProps.recordId = 'B'
  await flush()
  expect(section.editing.value).toBe(false)
  expect(section.draft.value).toBe('')
  expect(composer.draft.value).toBe('')
  await composer.submit(); expect(post).not.toHaveBeenCalled()
  await section.start(); section.draft.value = 'B draft'
  answer.resolve('ok'); await saving
  expect(section.editing.value).toBe(true)
  expect(section.draft.value).toBe('B draft')
})

const entry = (project: string, slug = 'same') => ({ id: `${project}-${slug}`, project: { id: project }, type: 'guide', slug, title: project, body: '', metadata: {}, status: 'current', updated_at: '2026-10-02T00:00:00Z' })
it('S8-002: same slug in another project reloads; failed transitions cannot be edited', async () => {
  const resolve = vi.fn(async (project: string, _type: string, slug: string) => entry(project, slug))
  const props = shallowReactive({ project: { id: 'A', routeKey: 'A', title: 'A' }, type: 'guide', slug: 'same', state: { items: ref([]), sequence: ref([]), upsert: vi.fn() }, canWrite: true, canDelete: true, now: 0, listQuery: {}, mode: 'page' })
  const view = setup('components/knowledge/KnowledgeEntryPage.vue', props, {
    'vue-router': { useRoute: () => ({ hash: '', query: {} }), useRouter: () => ({ replace: vi.fn() }) },
    '../../lib/brand': { brand: ref({ product: 'Aeon' }), setPageTitle: vi.fn() }, '../../lib/confirm': {}, '../../lib/crm': CRM,
    '../../lib/knowledge': { ...Knowledge, resolveKnowledge: resolve }, '../../lib/toast': { toast: vi.fn() },
    '../../lib/usePolledData': { usePoller: () => ({ start() {}, stop() {} }) }, '../../lib/work': {}, '../../stores/projects': { useProjects: () => ({}) },
  }).state
  await flush(); expect(view.entry.value.id).toBe('A-same')
  props.project = { id: 'B', routeKey: 'B', title: 'B' }; await flush()
  expect(resolve).toHaveBeenCalledWith('B', 'guide', 'same')
  expect(view.entry.value.id).toBe('B-same')
  resolve.mockRejectedValueOnce(new Error('offline')); props.slug = 'other'; await flush()
  expect(view.error.value).toBe('offline')
  expect(view.writable.value).toBe(false)
  await view.startEdit(); expect(view.editing.value).toBe(false)
})

const quote = (id: string) => ({ id, view: ref({ local: 'clean', baseRevision: 1, working: { profile: { id: 'old', revision: 1 } } }), projection: ref({ quote_node_id: id, state: 'draft', revision: 1, current_version: 0 }), frozen: ref(null), presence: ref(null), recovery: ref(null), error: ref(''), session: { save: vi.fn(async () => {}), reload: vi.fn(async () => {}) }, refresh: vi.fn(async () => {}) })
function quoteSetup(overrides: Record<string, unknown> = {}) {
  const a = quote('A'), b = quote('B'), props = reactive({ quoteId: 'A', layout: 'dock' })
  const notices: { action?: { run: () => void } }[] = []
  const lifecycle = { getVersion: vi.fn(async (id: string) => ({ quote_node_id: id, document: { title: id } })), lifecycleError: (_e: unknown, text: string) => text, finalizeQuote: vi.fn(async () => {}), issueConfirm: () => ({}) }
  const profile = { selectQuoteProfile: vi.fn(async () => {}), listProfiles: async () => [], getProfile: async () => ({ name: 'Old' }) }
  const draft = { getDraft: vi.fn(async () => ({ draft_revision: 1, document_sha256: 'sha' })) }
  const scope = reactive({ identity: { tenant: { id: 't' }, principal: { id: 'p' } } })
  const made = setup('components/business/QuoteWorkspace.vue', props, {
    'vue-router': { onBeforeRouteLeave() {} }, '../../lib/brand': {}, '../../lib/chrome': { headerFolded: ref(false) }, '../../lib/confirm': { confirmAction: async () => true },
    '../../lib/preferences': { usePreference: () => ({ value: ref(null), ready: Promise.resolve(), save() {} }) }, '../../lib/quotePresence': {}, '../../lib/quoteWorkspace': { acquireQuote: (_s: unknown, id: string) => id === 'A' ? a : b, releaseQuote() {}, dropQuote() {} },
    '../../lib/quotes/api': draft, '../../lib/quotes/editor': {}, '../../lib/quotes/lifecycle': lifecycle,
    '../../lib/quotes/list': { statusOf: () => 'draft' }, '../../lib/quotes/profile': profile, '../../lib/quotes/zoom': {}, '../../lib/quotes/saveProblems': {}, '../../lib/quoteRecovery': {},
    '../../lib/toast': { toast: (_text: string, opts: object = {}) => notices.push(opts) }, '../../stores/business': { useBusiness: () => ({ staff: true, admin: true }) },
    '../../stores/quotes': { useQuotes: () => ({ items: [], patch: vi.fn() }) }, '../../stores/session': { useSession: () => scope }, ...overrides,
  })
  return { ...made, a, b, props, lifecycle, profile, draft, notices, scope }
}
it('S8-003: versions belong to their quote; delayed A reads never populate B', async () => {
  const { state, props, lifecycle } = quoteSetup()
  await state.showVersion(1); expect(state.document.value.title).toBe('A')
  props.quoteId = 'B'; await flush(); await state.showVersion(1)
  expect(state.document.value.title).toBe('B')
  props.quoteId = 'A'; await flush(); const pending = deferred<object>()
  lifecycle.getVersion.mockImplementationOnce(() => pending.promise as never)
  const reading = state.showVersion(2); props.quoteId = 'B'; await flush()
  pending.resolve({ document: { title: 'late A' } }); await reading
  expect(state.viewing.value).toBeNull()
  expect(state.versionDocs.value.has(2)).toBe(false)
})
it('S8-004: profile Undo and delayed save cannot write the next quote', async () => {
  const { state, props, profile, notices, a } = quoteSetup()
  await state.chooseProfile('new')
  expect(profile.selectQuoteProfile).toHaveBeenCalledWith('A', 1, 'new')
  props.quoteId = 'B'; await flush(); notices.find(n => n.action)?.action?.run(); await flush()
  expect(profile.selectQuoteProfile.mock.calls.every(call => call[0] === 'A')).toBe(true)
  props.quoteId = 'A'; await flush(); a.view.value.local = 'dirty'
  const saved = deferred(); a.session.save.mockImplementationOnce(() => saved.promise)
  const choosing = state.chooseProfile('newer'); props.quoteId = 'B'; await flush()
  saved.resolve(); await choosing
  expect(a.session.save).toHaveBeenCalledTimes(1)
  expect(profile.selectQuoteProfile).toHaveBeenCalledTimes(1)
  expect(profile.selectQuoteProfile.mock.calls.every(call => call[0] === 'A')).toBe(true)
})
it('S8-004: issue confirmation captures A through navigation and a delayed save', async () => {
  const { state, props, lifecycle, draft, a } = quoteSetup()
  a.view.value.local = 'dirty'; const saved = deferred(); a.session.save.mockImplementationOnce(() => saved.promise)
  const issuing = state.issue(); await flush(); props.quoteId = 'B'; await flush()
  a.view.value.local = 'clean'; saved.resolve(); await issuing
  expect(a.session.save).toHaveBeenCalledTimes(1)
  expect(draft.getDraft).not.toHaveBeenCalled()
  expect(lifecycle.finalizeQuote).not.toHaveBeenCalled()
})

function customerSetup(getCustomer = vi.fn(async (id: string) => ({ ...CRM.blankCustomer(id), id, revision: 1 }))) {
  const route = reactive({ params: { id: 'A' }, path: '/business/customers/A' }), updateGuard: Function[] = [], removed: string[] = []
  const confirmation = vi.fn(async () => false)
  const view = setup('views/business/CustomerView.vue', {}, {
    'vue-router': { useRoute: () => route, useRouter: () => ({ push: vi.fn() }), onBeforeRouteLeave() {}, onBeforeRouteUpdate: (guard: Function) => updateGuard.push(guard) },
    '../../lib/crm': { ...CRM, getCustomer, listContacts: async () => [], getRelated: async () => ({ projects: [], quotes: [], hours: [] }) },
    '../../lib/brand': { setPageTitle: vi.fn() }, '../../lib/confirm': { confirmAction: confirmation }, '../../stores/session': { useSession: () => ({ identity: { principal: { id: 'p' }, tenant: { id: 't' } } }) },
    '../../lib/toast': { toast: vi.fn(), captureToastOwner: () => () => true }, '../../stores/business': { useBusiness: () => ({ admin: true }) }, '../../stores/customers': { useCustomers: () => ({ items: [], upsert: vi.fn(), remove: (id: string) => removed.push(id) }) },
  })
  return { ...view, route, updateGuard, confirmation, removed }
}
it('S8-006: a customer parameter change prompts before discarding a dirty draft', async () => {
  const { state, updateGuard, confirmation } = customerSetup(); await flush()
  await state.startEdit(); state.draft.name = 'Unsaved A'
  expect(updateGuard).toHaveLength(1)
  expect(await updateGuard[0]({ params: { id: 'B' }, path: '/business/customers/B' }, { params: { id: 'A' } })).toBe(false)
  expect(confirmation).toHaveBeenCalled()
  expect(state.editing.value).toBe(true)
})
it('S8-006: late customer success/404 and related reads cannot replace or remove B', async () => {
  for (const missing of [false, true]) {
    const answer = deferred<object>()
    const get = vi.fn((id: string) => id === 'A' ? answer.promise : Promise.resolve({ ...CRM.blankCustomer('B'), id: 'B', revision: 1 }))
    const { state, route, removed } = customerSetup(get)
    route.params.id = 'B'; route.path = '/business/customers/B'; await flush()
    if (missing) answer.reject(new CRM.CRMError(404, '', 'missing'))
    else answer.resolve({ ...CRM.blankCustomer('A'), id: 'A', revision: 1 })
    await flush()
    expect(state.customer.value.id).toBe('B')
    expect(removed).not.toContain('B')
  }
})

it('S8-005: approval captures the displayed period snapshot before confirmation', async () => {
  const confirmation = deferred<boolean>(), approved: unknown[][] = []
  const period = { id: 'A', principal_id: 'p', starts_at: '2026-09-01', ends_at: '2026-09-08', state: 'open', revision: 1 }
  const rows = [{ id: 'seen', period_id: 'A', duration_seconds: 3600, currency: 'EUR', amount: '1', started_at: '2026-09-01', node_id: 't' }]
  const props = reactive({ period, entries: rows, nodes: new Map(), loading: false })
  const panel = setup('components/business/PeriodPanel.vue', props, {
    '../../lib/api': { APIError }, '../../lib/business': { getPeriod: async () => ({ period: { ...period, revision: 2 }, digest: 'unseen' }), getPeriodSnapshot: async () => ({ period, entries: rows, digest: 'seen' }), approvePeriod: async (...args: unknown[]) => { approved.push(args); return period } },
    '../../lib/confirm': { confirmAction: () => confirmation.promise }, '../../lib/toast': { toast: vi.fn() }, '../../lib/work': {},
    '../../lib/week': { periodLabel: () => 'week' }, './duration': { formatSpan: () => '1h' }, './money': {},
    '../../stores/business': { useBusiness: () => ({ nameOf: () => 'Ada', principals: [] }) },
    '../../stores/session': { useSession: () => ({ identity: { tenant: { id: 't' }, principal: { id: 'p' } }, authenticationCurrent: () => true }) },
  }).state
  await flush(); const approving = panel.approve()
  props.period = { ...period, id: 'B', revision: 9 }; await flush()
  confirmation.resolve(true); await approving
  expect(approved).toEqual([])
})

it('S8-005: an unchanged review approves exactly the displayed revision and digest', async () => {
  const period = { id: 'A', principal_id: 'p', starts_at: '2026-09-01', ends_at: '2026-09-08', state: 'open', revision: 3 }
  const approvePeriod = vi.fn(async () => period)
  const panel = setup('components/business/PeriodPanel.vue', reactive({ period, entries: [], nodes: new Map(), loading: false }), {
    '../../lib/api': { APIError }, '../../lib/business': { getPeriodSnapshot: async () => ({ period, entries: [], digest: 'displayed' }), approvePeriod },
    '../../lib/confirm': { confirmAction: async () => true }, '../../lib/toast': { toast: vi.fn() }, '../../lib/work': {},
    '../../lib/week': { periodLabel: () => 'week' }, './duration': { formatSpan: () => '0h' }, './money': {},
    '../../stores/business': { useBusiness: () => ({ nameOf: () => 'Ada', principals: [] }) },
    '../../stores/session': { useSession: () => ({ identity: { tenant: { id: 't' }, principal: { id: 'p' } }, authenticationCurrent: () => true }) },
  }).state
  await flush(); await panel.approve()
  expect(approvePeriod).toHaveBeenCalledExactlyOnceWith('A', 3, 'displayed')
})

it('S8-013: a delayed session-ended send error for A cannot disable B', async () => {
  const pending = deferred(), props = reactive({ view: { session: { id: 'A', project_id: 'p', agent_principal_id: 'agent', advertised_capabilities: [], management_mode: 'unmanaged' } }, now: 0, canWrite: true, active: false })
  const helpers = await import('../src/components/agents/sessionChat')
  const state = setup('components/agents/SessionChat.vue', props, {
    '../../lib/api': { APIError }, '../../lib/agents': {}, '../../lib/agentRows': { readSessionMarker: async () => null },
    '../../stores/agents': { useAgents: () => ({ thread: () => [], addressOf: () => 'agent', refreshThread: async () => {}, send: () => pending.promise }) },
    '../../stores/session': { useSession: () => ({ identity: { principal: { id: 'viewer', kind: 'agent' } } }) },
    './sessionMessages': { collapseMessages: () => [] }, './sessionChat': helpers,
  }).state
  await flush(); state.draft.value = 'To A'; const sending = state.send()
  props.view.session = { ...props.view.session, id: 'B' }; await flush(); state.draft.value = 'To B'
  pending.reject(new APIError(409, 'ended', { code: 'session_ended' })); await sending
  expect(state.sendError.value).toBe('')
  expect(state.composeBlock.value).toBe('')
  expect(state.draft.value).toBe('To B')
  expect(state.sending.value).toBe(false)
})

it('S8-007: confirming deletion of A passes A even after selection moves to B', async () => {
  const answer = deferred<boolean>(), remove = vi.fn(async () => true)
  const a = { id: 'A', key: 'AEON-1', kind_slug: 'ticket', title: 'A', fields: {}, children_count: 0 }
  const props = reactive({ item: a, project: { id: 'p', routeKey: 'AEON' }, names: new Map(), canDelete: true, people: [], me: null })
  const state = setup('components/work/TicketWorkspace.vue', props, {
    'vue-router': { useRouter: () => ({}) }, '../../lib/api': {}, '../../lib/confirm': { confirmAction: () => answer.promise },
    '../../lib/rowStore': { rowStore: { row: () => null, adopt: (row: unknown) => row } }, '../../lib/toast': {}, '../../lib/workQueue': {},
    '../../lib/liveNodes': { liveNodes: { state: 'live', onState: () => () => {} } }, '../../lib/eta': { etaFromTicket: () => null },
    '../../lib/useActivity': { useActivity: () => ({}) }, '../../lib/useTicket': { useTicket: () => ({ readOnly: ref(false), gone: ref(false), remove }) },
    '../../lib/work': { kindLabel: () => 'Ticket' }, '../../lib/useAttachments': { useAttachments: () => ({}) }, '../../lib/doneGate': {},
    '../../lib/workVocabulary': WorkVocabulary,
    '../../stores/workVocabulary': { useWorkVocabulary: () => ({ value: { revision: 0, leaf: { name: '', icon: '' }, levels: [] } }) },
    '../../lib/recurrences': {}, '../../lib/useIdentityScope': { useIdentityScope: () => ({ owner: ref(''), reset() {} }) },
    '../../lib/ticketBenefits': { benefitDraft: () => ({}) }, '../../lib/authz': { can: () => false }, '../../lib/releaseAssign': {}, '../../lib/releaseMembership': {},
    '../../stores/workQueue': { useWorkQueue: () => ({ load: async () => {} }) },
    '../../lib/usePolledData': { usePoller: () => ({ start() {}, stop() {} }) }, '../../stores/session': { useSession: () => ({}) },
  }).state
  const deleting = state.remove(); props.item = { ...a, id: 'B', key: 'AEON-2' }; await flush()
  answer.resolve(true); await deleting
  expect(remove).toHaveBeenCalledWith(expect.objectContaining({ id: 'A' }))
})
