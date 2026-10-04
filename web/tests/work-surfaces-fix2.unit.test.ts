// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { effectScope, nextTick, ref, type EffectScope } from 'vue'
import type { ListItem } from '../src/lib/api'
import { rowStore } from '../src/lib/rowStore'
import { filtersFromQuery } from '../src/lib/ticketList'
import { LiveNodeStore } from '../src/lib/liveNodes'
const api = vi.hoisted(() => ({ listNodes: vi.fn(), getNode: vi.fn(), getKinds: vi.fn(async () => ({ items: [{ id: 'wk', slug: 'work', label: 'Work' }] })), getRelations: vi.fn(async () => ({ items: [] })), lookupNodes: vi.fn(async () => ({ items: [] })) }))
vi.mock('../src/lib/api', async original => ({ ...await original<typeof import('../src/lib/api')>(), ...api }))
const { useOutline } = await import('../src/lib/useOutline')
const { useTicket } = await import('../src/lib/useTicket')
const scopes: EffectScope[] = []
const scope = () => { const s = effectScope(); scopes.push(s); return s }
const settle = async () => { for (let i = 0; i < 100; i++) await Promise.resolve(); await nextTick() }
const row = (id: string, parent_id = 'project', over: Partial<ListItem> = {}): ListItem => ({ id, parent_id, kind_id: 'wk', kind_slug: 'work', kind_label: 'Work', key: `PRJ-${id}`, title: id, state: 'open', body: '', fields: {}, position: '1', created_at: '2026-10-04T10:00:00Z', updated_at: '2026-10-04T10:00:00Z', priority: null, assignee: null, parent: null, project: { id: 'project', key: 'PRJ', title: 'Project' }, children_count: 0, ...over })
function outline(items: ListItem[] = [], filtered = false) {
  const rows = ref<ListItem[]>([])
  const result = scope().run(() => useOutline(ref('project'), ref(filtersFromQuery(filtered ? { q: 'match', closed: '1' } : { closed: '1' })), ref(true), { rows, loading: ref(false), names: new Map() }, { store: new LiveNodeStore({ open: () => null, rows: rowStore }) }))!
  rows.value = items
  return result
}
beforeEach(() => { rowStore.clear(); api.listNodes.mockReset(); api.getNode.mockReset() })
afterEach(() => { for (const s of scopes.splice(0)) s.stop() })
it('loads one unified root page and advances its cursor only on request', async () => {
  api.listNodes.mockImplementation(async q => ({ items: [row(q.cursor ? 'second' : 'first')], next_cursor: q.cursor ? null : 'next' }))
  const o = outline(); await settle()
  expect(api.listNodes).toHaveBeenCalledTimes(1)
  expect(o.rows.value.map(r => r.id)).toEqual(['first'])
  expect(o.hasMoreRoot.value).toBe(true)
  expect(o.entries.value.some(e => e.type === 'group')).toBe(false)
  o.loadMoreRoot(); await settle()
  expect(api.listNodes).toHaveBeenCalledTimes(2)
  expect(api.listNodes.mock.calls[1]![0]).toMatchObject({ parent_id: 'project', cursor: 'next', kind: ['work', 'epic', 'ticket', 'task'] })
  expect(o.rows.value.map(r => r.id)).toEqual(['first', 'second'])
  expect(o.hasMoreRoot.value).toBe(false)
})
it('retains a complete filtered path with twelve ancestors', async () => {
  api.getNode.mockImplementation(async id => row(id, Number(id.slice(1)) === 1 ? 'project' : `a${Number(id.slice(1)) - 1}`))
  const o = outline([row('match', 'a12')], true); await settle()
  expect(o.rows.value.map(r => r.id)).toEqual([...Array.from({ length: 12 }, (_, i) => `a${i + 1}`), 'match'])
  expect(o.error.value).toBe('')
})
it('reports incomplete filtered paths when an ancestor cannot be read', async () => {
  api.getNode.mockRejectedValue(new Error('forbidden'))
  const o = outline([row('match', 'hidden')], true); await settle()
  expect(o.error.value).toMatch(/incomplete/i)
  expect(o.rows.value.map(r => r.id)).toEqual(['match'])
})
it('detail progress counts ten completed descendant leaves and one open leaf', async () => {
  const target = row('parent', 'project', { is_leaf: false, children_count: 2 })
  api.getNode.mockResolvedValue(target)
  api.listNodes.mockImplementation(async q => q.facets ? { items: [], next_cursor: null, facets: { state: { done: 10, open: 1, cancelled: 3 } } } : { items: [row('group', 'parent', { state: 'done', is_leaf: false, children_count: 10 }), row('open', 'parent')], next_cursor: null })
  const ticket = scope().run(() => useTicket(ref(target), { names: new Map(), onCreated: () => {}, onRemoved: () => {}, onMoved: () => {}, live: { busy: ref(false), me: () => null, store: new LiveNodeStore({ open: () => null, rows: rowStore }) } }))!
  await settle()
  expect(ticket.childProgress()).toMatchObject({ done: 10, total: 11, percent: 91 })
  expect(api.listNodes).toHaveBeenCalledWith(expect.objectContaining({ within: 'parent', shape: ['leaf'], facets: ['state'], limit: 1 }))
})
it('stops at the lazy item budget while retaining an honest incomplete result', async () => {
  const reached = new Promise<void>(resolve => api.listNodes.mockImplementation(async q => {
    const offset = Number(q.cursor ?? 0)
    const count = Math.min(q.limit, 5001 - offset)
    const page = { items: Array.from({ length: count }, (_, i) => row(String(offset + i))), next_cursor: offset + count < 5001 ? String(offset + count) : null }
    if (offset + count === 5000) resolve()
    return page
  }))
  const o = outline(); await settle()
  for (let i = 0; i < 24; i++) await o.loadMoreRoot()
  await reached
  expect(o.rows.value).toHaveLength(5000)
  const reads = api.listNodes.mock.calls.length
  await o.loadMoreRoot()
  expect(api.listNodes).toHaveBeenCalledTimes(reads)
  expect(o.error.value).toMatch(/incomplete.*5000/i)
  expect(o.hasMoreRoot.value).toBe(true)
})
it.each(['insert', 'child expansion'])('preserves the root cursor page size after %s consumes retention capacity', async action => {
  api.listNodes.mockImplementation(async q => {
    if (q.ids || q.facets) return { items: [], next_cursor: null }
    if (q.parent_id === '0') return { items: [row('child', '0')], next_cursor: null }
    if (q.cursor && q.limit !== 200) throw new Error('cursor does not match this query')
    const offset = Number(q.cursor ?? 0)
    return { items: Array.from({ length: 200 }, (_, i) => row(String(offset + i))), next_cursor: String(offset + 200) }
  })
  const o = outline(); await settle()
  for (let i = 0; i < 23; i++) await o.loadMoreRoot()
  expect(o.loadedRows.value).toHaveLength(4800)
  if (action === 'insert') { o.insert(row('inserted')); await settle() }
  else await o.loadChildren('0')
  expect(o.loadedRows.value).toHaveLength(4801)
  await o.loadMoreRoot()
  expect(api.listNodes).toHaveBeenCalledWith(expect.objectContaining({ parent_id: 'project', cursor: '4800', limit: 200 }))
  expect(o.loadedRows.value).toHaveLength(5000)
  expect(o.node('4998')).toBeDefined()
  expect(o.node('4999')).toBeUndefined()
  expect(o.error.value).toMatch(/incomplete.*5000/i)
  expect(o.error.value).not.toContain('cursor does not match')
  expect(o.hasMoreRoot.value).toBe(true)
})
it('keeps a project-root drop destination through nesting and unnesting in the unified Outline', async () => {
  const data = new Map([row('parent', 'project', { children_count: 1 }), row('moving')].map(r => [r.id, r]))
  api.listNodes.mockImplementation(async q => ({ items: q.facets ? [] : [...data.values()].filter(r => q.ids ? q.ids.includes(r.id) : r.parent_id === q.parent_id), next_cursor: null }))
  const o = outline(); await settle()
  const destination = () => o.entries.value.find(e => e.type === 'root')
  expect(destination()).toMatchObject({ type: 'root', parentId: 'project', label: 'Project root' })
  const nested = row('moving', 'parent', { updated_at: '2026-10-04T10:01:00Z' })
  data.set(nested.id, nested)
  rowStore.adopt(nested, undefined, { show: true })
  o.relocate(nested, 'project', null)
  o.setExpanded('parent', true); await settle()
  expect(o.entries.value.find(e => e.type === 'row' && e.row.id === 'moving')).toMatchObject({ tree: { depth: 1, parentId: 'parent' } })
  expect(destination()).toMatchObject({ parentId: 'project' })
  const returned = row('moving', 'project', { updated_at: '2026-10-04T10:02:00Z' })
  data.set(returned.id, returned)
  rowStore.adopt(returned, undefined, { show: true })
  o.relocate(returned, 'parent', 'parent'); await settle()
  expect(o.entries.value.filter(e => e.type === 'row' && e.row.id === 'moving')).toHaveLength(1)
  expect(o.entries.value.find(e => e.type === 'row' && e.row.id === 'moving')).toMatchObject({ tree: { depth: 0, parentId: 'project' } })
  expect(destination()).toMatchObject({ parentId: 'project' })
})
it('detects a filtered ancestor cycle without repeated requests', async () => {
  api.getNode.mockImplementation(async id => row(id, id === 'a' ? 'b' : 'a'))
  const o = outline([row('match', 'a')], true); await settle()
  expect(api.getNode).toHaveBeenCalledTimes(2)
  expect(o.error.value).toMatch(/incomplete.*cycle/i)
})
it('does not replace progress for the current record with a delayed old aggregate', async () => {
  let release!: (page: { items: ListItem[]; next_cursor: null; facets: { state: Record<string, number> } }) => void
  let started!: () => void
  const readStarted = new Promise<void>(resolve => { started = resolve })
  const held = new Promise<Parameters<typeof release>[0]>(resolve => { release = resolve })
  api.getNode.mockImplementation(async id => row(id, 'project', { is_leaf: false, children_count: 1 }))
  api.listNodes.mockImplementation(async q => {
    if (!q.facets) return { items: [], next_cursor: null }
    if (q.within === 'old') { started(); return held }
    return { items: [], next_cursor: null, facets: { state: { open: 3 } } }
  })
  const target = ref(row('old', 'project', { is_leaf: false, children_count: 1 }))
  const ticket = scope().run(() => useTicket(target, { names: new Map(), onCreated: () => {}, onRemoved: () => {}, onMoved: () => {}, live: { busy: ref(false), me: () => null, store: new LiveNodeStore({ open: () => null, rows: rowStore }) } }))!
  await readStarted
  target.value = row('current', 'project', { is_leaf: false, children_count: 1 }); await settle()
  expect(ticket.childProgress()).toMatchObject({ done: 0, total: 3 })
  release({ items: [], next_cursor: null, facets: { state: { done: 99 } } }); await settle()
  expect(ticket.childProgress()).toMatchObject({ done: 0, total: 3 })
})
it('reports failed leaf aggregates without falling back to grouping rows', async () => {
  const target = row('parent', 'project', { is_leaf: false, children_count: 1 })
  api.getNode.mockResolvedValue(target)
  api.listNodes.mockImplementation(async q => {
    if (q.facets) throw new Error('aggregate limit exceeded')
    return { items: [row('group', 'parent', { state: 'done', is_leaf: false })], next_cursor: null }
  })
  const ticket = scope().run(() => useTicket(ref(target), { names: new Map(), onCreated: () => {}, onRemoved: () => {}, onMoved: () => {} }))!
  await settle()
  expect(ticket.childProgress()).toBeNull()
  expect(ticket.progressError.value).toBe('aggregate limit exceeded')
})
