// SPDX-License-Identifier: AGPL-3.0-only
import { expect, it } from 'vitest'
import { reactive, ref } from 'vue'
import { flatten, mountView, settle, textOf, type RenderNode } from './webcore-view-harness'
import { leadBand, type LeadCandidates, type ProjectLead } from '../src/lib/lead'
import * as leadAPI from '../src/lib/lead'

const candidate = { id: 'running', display_label: 'AEON-LEAD', harness: 'codex', host: 'build-7', management_mode: 'unmanaged', reported_at: '2026-10-09T11:50:00Z' }
const deferred = <T>() => { let resolve!: (v: T) => void; const promise = new Promise<T>(r => { resolve = r }); return { promise, resolve } }
const click = async (node: RenderNode) => { await (node.props.onClick as () => unknown)(); await settle() }
function adoption(read: () => Promise<LeadCandidates> = async () => ({ items: [candidate], next_cursor: null })) {
  const identity = reactive({ identity: { tenant: { id: 'tenant' }, principal: { id: 'person', kind: 'person' } } })
  const writes: unknown[][] = []
  const props = reactive({ projectId: 'project', revision: 4 })
  const view = mountView('../src/components/lead/LeadAdoption.vue', {
    '../../lib/authz': { can: () => true }, '../../lib/lead': { readLeadCandidates: read },
    '../../directives/clipTip': { vClipTip: {} }, '../../stores/session': { useSession: () => identity },
    '../../stores/projectLeads': { useProjectLeads: () => ({ adopt: async (...args: unknown[]) => { writes.push(args); return { reason: 'adoption_pending' } } }) },
  }, props)
  const button = (act: string) => view.find(n => n.props['data-act'] === act)
  return { ...view, identity, writes, props, button, option: () => view.find(n => n.props.role === 'radio') }
}

it('the inline card confirms the selected running session with the revision displayed when opened', async () => {
  const view = adoption()
  try {
    await click(view.button('adopt-open'))
    expect(textOf(view.root)).toContain('AEON-LEAD'); expect(textOf(view.root)).toContain('build-7')
    expect(view.button('adopt-confirm').props['aria-disabled']).toBe(true)
    await click(view.option()); expect(view.option().props['aria-checked']).toBe(true)
    await click(view.button('adopt-confirm'))
    expect(view.writes).toEqual([['project', 4, 'running']])
    expect(textOf(view.root)).toContain('must now prove its existing lease')
  } finally { view.app.unmount() }
})

it('a candidate read cannot restore another person’s choices and reopening after a cancelled read works', async () => {
  const waiting = deferred<{ items: typeof candidate[]; next_cursor: null }>()
  let reads = 0
  const view = adoption(() => ++reads === 1 ? waiting.promise : Promise.resolve({ items: [candidate], next_cursor: null }))
  try {
    await click(view.button('adopt-open')); await click(view.button('adopt-cancel')); await click(view.button('adopt-open'))
    expect(reads).toBe(2); expect(textOf(view.root)).toContain('AEON-LEAD')
    view.identity.identity.principal.id = 'other-person'; await settle()
    waiting.resolve({ items: [candidate], next_cursor: null }); await settle()
    expect(flatten(view.root).filter(n => n.props.role === 'radio')).toHaveLength(0)
    expect(view.writes).toEqual([])
  } finally { view.app.unmount() }
})

it('candidate errors and empty lists are explicit and a changed lead cannot be confirmed', async () => {
  const failed = adoption(async () => { throw new Error('Sessions unavailable') })
  try { await click(failed.button('adopt-open')); expect(textOf(failed.root)).toContain('Sessions unavailable') } finally { failed.app.unmount() }
  const empty = adoption(async () => ({ items: [], next_cursor: null }))
  try { await click(empty.button('adopt-open')); expect(textOf(empty.root)).toContain('No eligible running coordinator') } finally { empty.app.unmount() }
  const changed = adoption()
  try {
    await click(changed.button('adopt-open')); await click(changed.option()); (changed.app as unknown as { _instance: { props: { revision: number } } })._instance.props.revision = 5; await settle()
    await click(changed.button('adopt-confirm')); expect(changed.writes).toEqual([])
    expect(textOf(changed.root)).toContain('The lead changed')
  } finally { changed.app.unmount() }
})

// Risk: ownership feedback is missing, turns an ineligible session into a
// choice, or survives a person/project change in the adoption panel.
it('an empty adoption list explains missing person ownership without offering the session', async () => {
  const view = adoption(async () => ({ items: [], next_cursor: null, empty_reason: { code: 'person_owner_missing', display_label: 'AEON-LEAD', harness: 'claude', host: 'build-7' } }))
  try {
    await click(view.button('adopt-open'))
    expect(textOf(view.root)).toContain('AEON-LEAD on build-7 is registered without a person owner; register it with a key you created.')
    expect(textOf(view.root)).not.toContain('No eligible running coordinator')
    expect(flatten(view.root).filter(n => n.props.role === 'radio')).toHaveLength(0)
    expect(view.button('adopt-confirm').props['aria-disabled']).toBe(true)
    await click(view.button('adopt-confirm'))
    expect(view.writes).toEqual([])
    view.identity.identity.principal.id = 'other-person'; await settle()
    expect(textOf(view.root)).not.toContain('AEON-LEAD on build-7')
  } finally { view.app.unmount() }
})

it('an unmanaged lead card offers cooperative pause and hides restart controls after exit', async () => {
  for (const state of ['starting', 'working', 'paused'] as const) {
    const lead: ProjectLead = { project_id: 'project', revision: 4, generation: 1, session_id: 'running', state, reason: '', process_active: state !== 'paused' }
    const summary = { lead: ref(lead), band: ref(leadBand(lead, 'AEON', 1)), leadSession: ref(state === 'paused' ? null : { management_mode: 'unmanaged' }), workers: ref([]), stations: ref([]), questions: ref([]), questionsPartial: ref(false) }
    const view = mountView('../src/components/lead/LeadBand.vue', {
      '../../lib/authz': { can: () => true }, 'vue-router': { useRouter: () => ({}) },
      '../../lib/lead': { ...leadAPI, canPause: () => true, canResume: () => true, LEAD_WORDS: { l: 'lead' }, unmanagedLeadCopy: 'Unmanaged: steering limited to messages and pause' },
      '../../lib/leadCardFold': { useLeadCardFold: () => ({ ready: ref(true), collapsed: ref(false), moving: ref(false) }) },
      '../../lib/leadOverlay': {}, '../../lib/toast': {}, '../../lib/usePolledData': { usePoller: () => ({ start() {}, stop() {} }) },
      '../../lib/useLeadSummary': { useLeadSummary: () => summary },
      '../../stores/projectLeads': { useProjectLeads: () => ({ views: { project: { principal: { session: 'running', management: 'unmanaged' } } }, busy: {}, load() {} }) },
      '../../stores/session': { useSession: () => ({ identity: { tenant: { id: 'tenant' }, principal: { id: 'person', kind: 'person' } } }) },
      '../../stores/workQueue': { useWorkQueue: () => ({ load() {} }) },
    }, { projectId: 'project', projectKey: 'AEON', routeKey: 'AEON' })
    try {
      expect(textOf(view.root)).toContain('Unmanaged: steering limited to messages and pause')
      const main = flatten(view.root).filter(n => n.props['data-act'] === 'main')
      if (state === 'paused') expect(main).toHaveLength(0)
      else expect(textOf(main[0]!)).toContain('Pause…')
      expect(textOf(view.root)).not.toContain('Cancel start')
      expect(textOf(view.root)).not.toContain('Resume')
    } finally { view.app.unmount() }
  }
})
