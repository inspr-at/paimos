// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it, vi } from 'vitest'
import { APIError } from '../src/lib/api'
import { actionReason, deadlineValue, localDeadline, type ReleaseRecord, type ReleaseRequest, type ReleaseRights } from '../src/lib/releaseActions'
import { useReleaseRecovery } from '../src/lib/useReleaseRecovery'
import type { ItemPage, PlanningItem } from '../src/lib/deliveryPlanning'
import type { DeliveryActions, DeliveryCommit } from '../src/lib/deliveryChanges'

const release: ReleaseRecord = { release_id: 'release', project_id: 'project', title: 'Work', visibility: 'internal', state: 'planned', rank: 'B', revision: 7, entry_closes_at: null, rollup: { units: 10, completed: 2, open_hours: 4 }, build_summary: {} }
const rights: ReleaseRights = { person: true, read: true, write: true, deploy: true, product: false }
const item = (id: string, later = false): PlanningItem => ({ item_id: id, project_id: 'project', release_id: later ? 'later' : undefined, revision: 2, rank: 'V', node_revision: '2026-10-03T12:00:00Z', key: `KEY-${id}`, title: 'Completed work', state: 'done', kind: 'ticket', created_at: '', estimated_hours: 1, expedite: false, due_on: null })
function harness(options: { unplaced?: number; later?: number; room?: number; incomplete?: boolean; failBatch?: number; failRead?: boolean } = {}) {
  let current = true, allowed = true, version = 7
  const writes: Record<string, unknown>[] = [], reads: string[] = [], commits: DeliveryCommit[] = []
  const unplaced = Array.from({ length: options.unplaced ?? 150 }, (_, i) => item(String(i))), later = Array.from({ length: options.later ?? 53 }, (_, i) => item(`later-${i}`, true))
  const actions: DeliveryActions = { identity: () => current ? 'owner' : 'different', begin: () => 'owner', failed: vi.fn(), receipt: vi.fn(() => true), commit: vi.fn((identity, change) => { if (!current || identity !== 'owner') return false; commits.push(change); return true }) }
  const request: ReleaseRequest = async (path, init) => {
    if (init?.method === 'POST') {
      const body = JSON.parse(String(init.body)); writes.push(body)
      if (options.failBatch === writes.length) throw new APIError(409, 'Entry changed during Include', { code: 'entry_closed' })
      expect(body.expected_release_revision).toBe(version)
      const ids = new Set(body.items.map((row: { id: string }) => row.id))
      const picked = [...unplaced, ...later].filter(row => ids.has(row.item_id))
      expect(picked).toHaveLength(body.items.length)
      for (const list of [unplaced, later]) for (let i = list.length - 1; i >= 0; i--) if (ids.has(list[i]!.item_id)) list.splice(i, 1)
      return { items: picked.map(row => ({ ...row, release_id: 'release', revision: row.revision + 1 })), release_revision: ++version, undo_event_id: writes.length }
    }
    reads.push(path)
    if (options.failRead) throw new Error('Recovery could not be read')
    const query = new URL(path, 'http://local').searchParams, list = query.has('completed_later') ? later : unplaced
    expect(query.has('view')).toBe(false); expect(query.get('limit')).toBe('100')
    // Keyset fixture retains members being asserted; removing a prior page
    // cannot shift the continuation as an offset fixture would.
    const cursor = query.get('cursor'), start = cursor ? list.findIndex(row => row.item_id === cursor) + 1 : 0
    const rows = list.slice(start, start + 100)
    const end = start + rows.length
    return { items: rows.map(row => ({ ...row })), count: list.length, incomplete: !!options.incomplete, next_cursor: end < list.length ? rows.at(-1)!.item_id : '' } satisfies ItemPage
  }
  const state = useReleaseRecovery({ release: { ...release, rollup: { ...release.rollup, units: 1000 - (options.room ?? 990) } }, signal: new AbortController().signal, current: () => current, allowed: () => allowed, actions, request })
  return { state, writes, reads, commits, actions, request, ownerChanged: () => { current = false }, revoke: () => { allowed = false } }
}
describe('release rights and deadline', () => {
  it('allows deploy agents to Freeze/Cut and product Publish, never Include/settings/abandon or project attestation', () => {
    const agent = { ...rights, person: false }
    expect(actionReason('freeze', release, agent)).toBe('')
    expect(actionReason('settings', release, agent)).toContain('Only a person')
    expect(actionReason('abandon', release, agent)).toContain('Only a person')
    const cut = { ...release, visibility: 'published' as const, state: 'frozen' as const, version: '261004120000.0.0', cut_at: '2026-10-04T12:00:00Z' }
    expect(actionReason('publish', cut, agent)).toContain('attest')
    expect(actionReason('publish', cut, { ...agent, product: true })).toBe('')
    expect(actionReason('unfreeze', cut, rights)).toContain('Already cut')
    expect(actionReason('cut', cut, rights)).toContain('Already cut')
    expect(actionReason('top', cut, rights)).toContain('Published releases')
  })
  it('uses current permission and fenced state rather than treating lifecycle as authorization', () => {
    expect(actionReason('freeze', release, { ...rights, deploy: false })).toContain('deployment permission')
    expect(actionReason('abandon', release, { ...rights, deploy: false })).toBe('')
    expect(actionReason('abandon', { ...release, state: 'building' }, { ...rights, deploy: false })).toContain('deployment permission')
    expect(actionReason('close', { ...release, state: 'frozen' }, rights)).toBe('')
    expect(actionReason('building', { ...release, state: 'released' }, rights)).toContain('finished')
  })
  it('clears deadlines explicitly and roundtrips local minutes without inventing a timezone', () => {
    expect(deadlineValue('')).toBeNull()
    const instant = '2026-10-04T14:23:00.000Z'
    expect(deadlineValue(localDeadline(instant))).toBe(instant)
    expect(() => deadlineValue('invalid')).toThrow('valid entry deadline')
  })
})
describe('Freeze recovery', () => {
  it('includes both headings in bounded atomic pages, carries destination CAS and offers no blanket Undo', async () => {
    const h = harness(); await h.state.load()
    expect(h.reads).toHaveLength(2); expect(h.state.visible.value).toHaveLength(153); expect(h.state.selected.value).toBe(203)
    h.state.checked('0', false); await h.state.include()
    expect(h.writes.map(row => (row.items as unknown[]).length)).toEqual([99,50,53])
    expect(h.writes.map(row => row.expected_release_revision)).toEqual([7,8,9])
    expect(h.writes.every(row => row.release_id === 'release' && row.position === 'append')).toBe(true)
    expect(h.commits.every(change => change.kind === 'placement' && change.undoable === false && change.result.undo_event_id)).toBe(true)
    expect(h.state.result.value).toBe('202 placed · 1 remain listed.')
    expect(h.state.revision.value).toBe(10)
    expect(h.state.rows.value.unplaced.map(row => row.item_id)).toEqual(['0'])
  })
  it('stops on the first entry/freeze conflict and reports the actual committed prefix', async () => {
    const h = harness({ failBatch: 2 }); await h.state.load(); await h.state.include()
    expect(h.writes).toHaveLength(2); expect(h.commits).toHaveLength(1)
    expect(h.state.result.value).toBe('100 placed · 103 remain listed · Include stopped.')
    expect(h.state.error.value).toContain('Entry has closed'); expect(h.state.ready.value).toBe(false)
    await h.state.include(); expect(h.writes).toHaveLength(2)
  })
  it('stops at the remaining capacity, preserving unplaced and later omissions', async () => {
    const h = harness({ room: 110 }); await h.state.load(); await h.state.include()
    expect(h.writes.map(row => (row.items as unknown[]).length)).toEqual([100,10])
    expect(h.state.result.value).toBe('110 placed · 93 remain listed · Include stopped.')
    expect(h.state.error.value).toContain('full'); expect(h.state.room.value).toBe(0)
  })
  it('never requests unseen pages when All is off or the count is incomplete', async () => {
    for (const incomplete of [false,true]) {
      const h = harness({ incomplete }); await h.state.load()
      if (!incomplete) { h.state.toggleAll(false); h.state.checked('0', true) }
      await h.state.include()
      expect(h.reads).toHaveLength(2)
      expect(h.writes.map(row => (row.items as unknown[]).length)).toEqual(incomplete ? [100,53] : [1])
      if (incomplete) { expect(h.state.all.value).toBe(false); expect(h.state.result.value).toContain('at least 50') }
    }
  })
  it('All includes the first page even after browsing a continuation', async () => {
    const h = harness(); await h.state.load(); await h.state.more('unplaced')
    expect(h.state.rows.value.unplaced[0]?.item_id).toBe('100')
    await h.state.include()
    expect(h.writes.flatMap(row => (row.items as { id: string }[]).map(item => item.id))).toHaveLength(203)
    expect(h.state.result.value).toBe('203 placed · 0 remain listed.')
  })
  it('retains explicitly checked revisions across pages with All off', async () => {
    const h = harness(); await h.state.load(); h.state.toggleAll(false); h.state.checked('0',true)
    await h.state.more('unplaced'); h.state.checked('100',true)
    expect(h.state.visible.value.length).toBeLessThanOrEqual(200); expect(h.state.selected.value).toBe(2)
    await h.state.include()
    expect(h.writes.flatMap(row => row.items)).toEqual([{ id:'0', expected_revision:2 }, { id:'100', expected_revision:2 }])
    expect(h.state.result.value).toBe('2 placed · 201 remain listed.')
  })
  it('drops late responses and sends no second page after project/person identity changes', async () => {
    const h = harness(); let resolve!: (value: unknown) => void
    const gate = new Promise<unknown>(done => { resolve = done })
    const request: ReleaseRequest = (path, init) => init?.method === 'POST' ? gate : h.request(path, init)
    let current = true
    const state = useReleaseRecovery({ release, current: () => current, allowed: () => true, signal: new AbortController().signal, request, actions: h.actions })
    await state.load(); const pending = state.include(); current = false
    resolve({ items: state.rows.value.unplaced.map(row => ({ ...row, release_id: 'release' })), release_revision: 8, undo_event_id: 1 })
    await pending
    expect(state.revision.value).toBe(7); expect(state.result.value).toBe(''); expect(h.actions.commit).not.toHaveBeenCalled(); expect(h.reads).toHaveLength(2)
  })
  it('refuses permission loss, failed reads and cross-project recovery rows', async () => {
    const h = harness(); await h.state.load(); h.revoke(); await h.state.include(); expect(h.writes).toEqual([])
    const failed = harness({ failRead: true }); await failed.state.load(); expect(failed.state.ready.value).toBe(false); expect(failed.state.error.value).toContain('could not be read')
    const state = useReleaseRecovery({ release, current: () => true, allowed: () => true, signal: new AbortController().signal, request: async () => ({ items: [{ ...item('bad'), project_id: 'other' }], count: 1, incomplete: false }) })
    await state.load(); expect(state.error.value).toContain('identity'); expect(state.ready.value).toBe(false)
  })
  it('never claims that an unacknowledged mutation placed zero rows or that remaining counts are current', async () => {
    const h = harness(), request: ReleaseRequest = (path,init) => init?.method === 'POST' ? Promise.reject(new Error('Connection lost after sending')) : h.request(path,init)
    const state = useReleaseRecovery({release,current:()=>true,allowed:()=>true,signal:new AbortController().signal,request})
    await state.load(); await state.include()
    expect(state.result.value).toBe('0 confirmed placed · last batch outcome and remaining count unknown. Include stopped; reopen Freeze before continuing.')
    expect(state.ready.value).toBe(false); expect(state.revision.value).toBe(7)
  })
})
