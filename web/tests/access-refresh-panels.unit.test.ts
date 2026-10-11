// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1195 fix 2: focus must retry an invalidated ticket lookup and replace
// server-owned proposal snapshots without dropping unlisted local results.
import { afterEach, expect, it, vi } from 'vitest'
import { defineComponent, h } from 'vue'
import * as authz from '../src/lib/authz'
import * as ticketLinks from '../src/lib/ticketLinks'
import * as doctrine from '../src/lib/doctrine'
import { lookupNodeKeys } from '../src/lib/api'
import { mountView, settle, textOf } from './webcore-view-harness'

vi.mock('../src/lib/api', () => ({
  api: vi.fn(async () => new Response(JSON.stringify({ workspace: { permissions: ['rules.write', 'rules.publish'] }, project: null }))),
  sessionEnded: {}, lookupNodeKeys: vi.fn(),
}))
vi.mock('../src/lib/doctrine', async original => ({
  ...await original<typeof import('../src/lib/doctrine')>(),
  getDoctrineProposals: vi.fn(), getDoctrineFindings: vi.fn(async () => []),
  refreshDoctrineProposal: vi.fn(), approveDoctrineProposal: vi.fn(),
}))

const session = { identity: { tenant: { id: 'tenant' }, principal: { id: 'person', name: 'Reviewer', kind: 'person' } } }
const sessionModule = { useSession: () => session }
const deferred = <T,>() => { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done }); return { promise, resolve } }
const views: ReturnType<typeof mountView>[] = []
afterEach(() => { for (const view of views.splice(0)) view.app.unmount(); authz.clearPermissions(); vi.clearAllMocks() })

it('AEON-1195: focus during a held ticket-key lookup retries it and never reports a false missing ticket', async () => {
  const item = { id: 'ticket', key: 'AEON-1195', title: 'Fokus erhält die sichtbare Arbeit', state: 'in_progress', fields: {} }
  const answer = { items: [{ ...item, requested_key: item.key, project_id: 'project' }] } as Awaited<ReturnType<typeof lookupNodeKeys>>
  const old = deferred<typeof answer>(), fresh = deferred<typeof answer>()
  vi.mocked(lookupNodeKeys).mockReturnValueOnce(old.promise).mockReturnValueOnce(fresh.promise)
  const listNodes = vi.fn(async () => ({ items: [item] }))
  const ticket = defineComponent({
    props: ['item', 'resolving', 'resolveError'],
    setup: props => () => h('section', {}, props.resolveError || (props.resolving ? 'Loading ticket' : props.item?.title)),
  })
  const view = mountView('../src/components/releases/ReleaseTicketPanel.vue', {
    '../../lib/api': { listNodes }, '../../lib/authz': authz,
    '../../lib/confirm': {}, '../../lib/ticketList': { EMPTY_FILTERS: {}, WORK_KINDS: ['work'] },
    '../../lib/ticketLinks': ticketLinks,
    '../../lib/useTicketList': { useTicketList: () => ({ names: new Map(), resolveNames: async () => {} }) },
    '../../stores/projects': { useProjects: () => ({ load: async () => {}, byId: () => ({ routeKey: 'AEON' }) }) },
    '../../stores/session': sessionModule,
    '../work/TicketWorkspace.vue': { __esModule: true, default: ticket },
  }, { ticketKey: item.key, now: 0 })
  views.push(view)
  expect(lookupNodeKeys).toHaveBeenCalledOnce()
  expect(textOf(view.root)).toContain('Loading ticket')
  await authz.accessChanged()
  old.resolve(answer)
  await settle()
  expect(textOf(view.root)).not.toContain('is not a ticket in this workspace')
  expect(lookupNodeKeys).toHaveBeenCalledTimes(2)
  expect(textOf(view.root)).toContain('Loading ticket')
  fresh.resolve(answer)
  await settle()
  expect(textOf(view.root)).toContain(item.title)
  const visible = view.find(node => node.tag === 'section')
  const reads = listNodes.mock.calls.length
  await authz.accessChanged(); await settle()
  expect(view.find(node => node.tag === 'section')).toBe(visible)
  expect(textOf(view.root)).toContain(item.title)
  expect(listNodes).toHaveBeenCalledTimes(reads)
})

it('AEON-1195: proposal refresh replaces state and approval head, removes server entries and retains only unlisted local upserts', async () => {
  const proposal = (id: string, pr_number: number): doctrine.DoctrineProposal => ({
    id, source_id: 'source', repository: 'example/doctrine', path: 'AGENTS.md', rule_key: 'work', state: 'proposed',
    head_sha: '1'.repeat(40), pr_number, pr_url: `https://github.com/example/doctrine/pull/${pr_number}`,
    gate_ready: true, pinned_machines: 0, created_at: '2026-10-11T08:00:00Z',
  })
  const first = proposal('first', 12), removed = proposal('removed', 13), local = proposal('local', 14)
  vi.mocked(doctrine.getDoctrineProposals).mockResolvedValue([first, removed])
  await authz.refreshPermissions()
  const view = mountView('../src/components/rules/DoctrineProposals.vue', {
    '../../lib/authz': authz, '../../stores/session': sessionModule, '../../lib/doctrine': doctrine,
  }, { latest: local })
  views.push(view)
  await settle()
  expect(textOf(view.root)).toContain('PR #12')
  expect(textOf(view.root)).toContain('PR #13')
  expect(textOf(view.root)).toContain('PR #14')
  expect(textOf(view.root)).not.toContain('In review')
  const held = deferred<doctrine.DoctrineProposal[]>()
  vi.mocked(doctrine.getDoctrineProposals).mockReturnValueOnce(held.promise)
  await authz.accessChanged()
  expect(textOf(view.root)).toContain('PR #13') // visible until replacement arrives
  const changed = { ...first, state: 'in_review' as const, head_sha: '2'.repeat(40) }
  held.resolve([changed]); await settle()
  expect(textOf(view.root)).toContain('In review')
  expect(textOf(view.root)).not.toContain('PR #13')
  expect(textOf(view.root)).toContain('PR #14')
  vi.mocked(doctrine.approveDoctrineProposal).mockResolvedValue({ ...changed, state: 'merged' })
  await (view.find(node => String(node.props['aria-label']).startsWith('Approve & merge')).props.onClick as () => Promise<void>)()
  expect(doctrine.approveDoctrineProposal).toHaveBeenCalledWith(first.id, changed.head_sha)
  // A result from update() also survives until the list acknowledges it.
  vi.mocked(doctrine.getDoctrineProposals).mockResolvedValue([])
  await authz.accessChanged(); await settle()
  expect(textOf(view.root)).toContain('PR #12')
  expect(textOf(view.root)).toContain('PR #14')
  vi.mocked(doctrine.getDoctrineProposals).mockResolvedValue([changed, local])
  await authz.accessChanged(); await settle()
  vi.mocked(doctrine.getDoctrineProposals).mockResolvedValue([])
  await authz.accessChanged(); await settle()
  expect(textOf(view.root)).toContain('No proposals yet.')
  expect(textOf(view.root)).not.toContain('PR #')
  // A pre-reset read cannot restore another person's proposal or local result.
  const stale = deferred<doctrine.DoctrineProposal[]>()
  vi.mocked(doctrine.getDoctrineProposals).mockReturnValueOnce(stale.promise)
  await authz.accessChanged()
  authz.clearPermissions()
  expect(textOf(view.root)).not.toContain('PR #')
  stale.resolve([first, local]); await settle()
  expect(textOf(view.root)).not.toContain('PR #')
})
