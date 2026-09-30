// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { automaticColumns, COLUMN_BY_ID, layoutWidths, moveColumn, orderOf, releaseLabel, tagList, TITLE_TARGET, titleRoom, visibleColumns, widthOf, type ColumnId } from '../src/lib/columns.ts'
import { byPosition, positionBetween, positionOf, type Attachment } from '../src/lib/attachments.ts'

const ids = (width: number, options: Parameters<typeof visibleColumns>[1]) => visibleColumns(width, options).columns.map(c => c.id)

test('automatic columns follow the table width and the data', () => {
  assert.deepEqual(automaticColumns(700, { assigned: true }), ['key', 'title', 'status', 'priority'])
  assert.deepEqual(automaticColumns(800, { assigned: true }), ['key', 'title', 'status', 'priority', 'updated'])
  assert.deepEqual(automaticColumns(1200, { assigned: true }), ['key', 'title', 'status', 'priority', 'assignee', 'updated'])
  assert.deepEqual(automaticColumns(1200, { workers: true }), ['key', 'title', 'status', 'priority', 'assignee', 'updated'])
  assert.deepEqual(automaticColumns(800, { workers: true }), ['key', 'title', 'status', 'priority', 'updated'])
  assert.deepEqual(automaticColumns(1200, {}), ['key', 'title', 'status', 'priority', 'updated'])
  // Wide tables show Assignee even when nobody is assigned yet.
  assert.deepEqual(automaticColumns(1600, {}), ['key', 'title', 'status', 'priority', 'assignee', 'epic', 'created', 'updated'])
  assert.deepEqual(automaticColumns(1600, { assigned: true, estimate: true, release: true, tags: true }), ['key', 'title', 'status', 'priority', 'assignee', 'epic', 'release', 'tags', 'estimate', 'created', 'updated'])
  // Wide extras step aside before Title gets cramped: 1500px cannot hold them all beside 420px of title.
  const present = { assigned: true, estimate: true, release: true, tags: true }
  assert.deepEqual(ids(1500, { phone: false, present }), ['key', 'title', 'status', 'priority', 'assignee', 'epic', 'created', 'updated'])
  assert.deepEqual(ids(1700, { phone: false, present }), ['key', 'title', 'status', 'priority', 'assignee', 'epic', 'release', 'tags', 'created', 'updated'])
  assert.deepEqual(ids(2400, { phone: false, present }), ['key', 'title', 'status', 'priority', 'assignee', 'epic', 'release', 'tags', 'estimate', 'created', 'updated'])
})

test('a saved choice fixes order and visibility; what cannot fit steps aside', () => {
  const prefs = { order: ['updated', 'status', 'epic'] as const, visible: ['updated', 'status', 'epic', 'estimate'] as const }
  const p = { order: [...prefs.order], visible: [...prefs.visible] }
  assert.deepEqual(ids(2000, { phone: false, prefs: p }), ['key', 'title', 'updated', 'status', 'epic', 'estimate'])
  // 118 key + 240 title + 104 + 138 + 220 + 96 = 916: estimate goes first, then epic.
  assert.deepEqual(ids(900, { phone: false, prefs: p }), ['key', 'title', 'updated', 'status', 'epic'])
  assert.deepEqual(ids(700, { phone: false, prefs: p }), ['key', 'title', 'updated', 'status'])
  assert.equal(visibleColumns(2000, { phone: false, prefs: p }).customised, true)
  assert.deepEqual(ids(390, { phone: true, prefs: p }), ['key', 'title', 'status', 'priority', 'updated'])
  assert.deepEqual(ids(390, { phone: true, present: { estimate: true } }), ['key', 'title', 'status', 'priority', 'updated', 'estimate'])
  assert.deepEqual(ids(390, { phone: true, present: { eta: true, estimate: true }, prefs: { visible: ['status'] } }), ['key', 'title', 'status', 'priority', 'updated', 'eta', 'estimate'])
  // A saved choice that hides Assignee stays hidden when a live worker is present.
  assert.deepEqual(ids(1600, { phone: false, present: { workers: true }, prefs: { visible: ['status', 'updated'] } }), ['key', 'title', 'status', 'updated'])
  assert.deepEqual(ids(390, { phone: true, present: { workers: true }, prefs: p }), ['key', 'title', 'status', 'priority', 'updated'])
})

test('order keeps Key and Title first and appends unknown or missing columns', () => {
  assert.deepEqual(orderOf({ order: ['title', 'created', 'bogus' as never, 'created'] }).slice(0, 4), ['key', 'title', 'created', 'status'])
  assert.equal(orderOf(null).length, 18)
  assert.deepEqual(automaticColumns(1600, { eta: true }), ['key', 'title', 'status', 'priority', 'assignee', 'epic', 'created', 'updated', 'eta'])
  assert.deepEqual(automaticColumns(1600, { eta: true, progress: true }), ['key', 'title', 'status', 'priority', 'assignee', 'epic', 'created', 'updated', 'progress', 'eta'])
  assert.deepEqual(automaticColumns(1200, { progress: true, eta: true }), ['key', 'title', 'status', 'priority', 'updated', 'progress', 'eta'])
  assert.deepEqual(ids(390, { phone: true, present: { eta: true } }), ['key', 'title', 'status', 'priority', 'updated', 'eta'])
  assert.deepEqual(ids(390, { phone: true, present: { eta: true, progress: true } }), ['key', 'title', 'status', 'priority', 'updated', 'progress', 'eta'])
  const order = orderOf(null)
  assert.deepEqual(moveColumn(order, 'priority', -1).slice(0, 4), ['key', 'title', 'priority', 'status'])
  assert.deepEqual(moveColumn(order, 'status', -1), order)
  assert.deepEqual(moveColumn(order, 'key', 1), order)
})

test('progress leaves before Estimate when a saved choice cannot fit', () => {
  const prefs = { visible: ['status', 'updated', 'estimate', 'progress', 'eta'] as const }
  const shown = ['key', 'title', 'status', 'estimate', 'updated', 'progress', 'eta'] as const
  const width = (names: readonly string[]) => names.reduce((sum, id) => sum + (id === 'title' ? 240 : COLUMN_BY_ID.get(id as ColumnId)!.width), 0)
  const p = { visible: [...prefs.visible] }
  assert.deepEqual(ids(width(shown), { phone: false, prefs: p }), [...shown])
  assert.deepEqual(ids(width(shown) - 1, { phone: false, prefs: p }), shown.filter(id => id !== 'eta'))
  const kept = shown.filter(id => id !== 'eta')
  assert.deepEqual(ids(width(kept) - 1, { phone: false, prefs: p }), ['key', 'title', 'status', 'estimate', 'updated'])
})

test('widths clamp to each column’s bounds', () => {
  assert.equal(widthOf('status', null), 138)
  assert.equal(widthOf('status', { widths: { status: 20 } }), 84)
  assert.equal(widthOf('status', { widths: { status: 9999 } }), 260)
  assert.equal(widthOf('status', { widths: { status: Number.NaN } }), 138)
})

test('wide tables stop Title near 960px and give the spare width to the text columns', () => {
  const wide = ['key', 'title', 'status', 'priority', 'assignee', 'epic', 'created', 'updated'] as const
  const sum = (w: Partial<Record<string, number>>) => Object.values(w).reduce((a: number, b) => a + (b ?? 0), 0)
  // Narrow: nothing grows, Title takes the rest.
  assert.deepEqual(layoutWidths([...wide], 1400), { key: 118, status: 138, priority: 112, assignee: 156, epic: 220, created: 104, updated: 104 })
  // 2460px: Epic and Assignee grow (up to their maximum), Title keeps about its target.
  const at2460 = layoutWidths([...wide], 2460)
  assert.equal(at2460.status, 138)
  assert.equal(at2460.epic, 480)
  assert.equal(at2460.assignee, 320)
  const title = 2460 - sum(at2460)
  assert.ok(title >= TITLE_TARGET && title < TITLE_TARGET + 140, `title ${title}`)
  // Between: growth is proportional and Title sits at the target.
  const at2000 = layoutWidths([...wide], 2000)
  assert.ok(at2000.epic! > 220 && at2000.epic! < 480 && at2000.assignee! > 156 && at2000.assignee! < 320)
  assert.ok(Math.abs(2000 - sum(at2000) - TITLE_TARGET) <= 2)
  // A width the person gave wins: Epic stays, Title's own width becomes its target.
  const sized = layoutWidths([...wide], 2000, { widths: { epic: 200, title: 700 } })
  assert.equal(sized.epic, 200)
  assert.ok(Math.abs(2000 - sum(sized) - 700) <= 2 || sized.assignee === 320)
  // Live drag widths count as sized too.
  assert.equal(layoutWidths([...wide], 2000, null, { assignee: 180 }).assignee, 180)
})

test('an explicit title width resizes its neighbours and never collapses a fixed column', () => {
  const ids = ['key', 'title', 'status', 'priority', 'assignee', 'updated'] as const
  const table = 1200
  const sum = (w: Partial<Record<string, number>>) => Object.values(w).reduce((a: number, b) => a + (b ?? 0), 0)
  const wideTitle = layoutWidths([...ids], table, { widths: { title: 700 } })
  assert.ok(Math.abs(table - sum(wideTitle) - 700) <= 1)
  // The next column gives way first, and stops at its minimum.
  assert.equal(wideTitle.status, COLUMN_BY_ID.get('status')!.min)
  for (const id of ids) if (id !== 'title') {
    const def = COLUMN_BY_ID.get(id)!
    assert.ok(wideTitle[id]! >= def.min && wideTitle[id]! <= def.max, `${id} ${wideTitle[id]}`)
  }
  const narrow = layoutWidths([...ids], table, { widths: { title: 320 } })
  assert.ok(Math.abs(table - sum(narrow) - 320) <= 1)
  assert.equal(narrow.status, COLUMN_BY_ID.get('status')!.max)
  // A column the person already sized stays put; the slack comes from the others.
  const locked = layoutWidths([...ids], table, { widths: { title: 700, status: 200 } })
  assert.equal(locked.status, 200)
  for (const id of ['key', 'priority', 'assignee', 'updated'] as const) {
    const def = COLUMN_BY_ID.get(id)!
    assert.ok(locked[id]! >= def.min && locked[id]! <= def.max, `${id} ${locked[id]}`)
  }
  const room = titleRoom([...ids], table, null)
  assert.equal(room.min, COLUMN_BY_ID.get('title')!.min)
  assert.equal(room.max, table - ids.filter(id => id !== 'title').reduce((total, id) => total + COLUMN_BY_ID.get(id)!.min, 0))
})

test('release and tags read the classic fields', () => {
  assert.equal(releaseLabel({ release: { id: 1, label: ' v4.7.8 ' } }), 'v4.7.8')
  assert.equal(releaseLabel({ release: '1.10.0' }), '1.10.0')
  assert.equal(releaseLabel({ release: null }), '')
  assert.equal(releaseLabel(undefined), '')
  assert.deepEqual(tagList({ tags: [{ id: 16, name: 'CUSTOMERPORTAL', color: 'blue' }, 'hsb8', { name: ' ' }, 7] }), [{ name: 'CUSTOMERPORTAL', color: 'blue' }, { name: 'hsb8', color: '' }])
  assert.deepEqual(tagList({ tags: null }), [])
})

test('attachment positions are decimal strings the server accepts', () => {
  assert.equal(positionBetween(undefined, undefined), '1024')
  assert.equal(positionBetween(1, 2), '1.5')
  assert.equal(positionBetween(undefined, 1), '-1023')
  assert.equal(positionBetween(3, undefined), '1027')
  assert.match(positionBetween(1, 1.000000000001), /^-?\d{1,14}(\.\d{1,15})?$/)
  const a = (id: string, position: string) => ({ id, position }) as Attachment
  assert.deepEqual([a('b', '2.000000000000000'), a('a', '1.5'), a('c', '1.5')].sort(byPosition).map(x => x.id), ['a', 'c', 'b'])
  assert.equal(positionOf(a('x', 'nope')), 0)
})

test('planning columns join wide tables when filled, leave first, and stay hidden while empty', () => {
  const all = { model: true, tokens: true, list_cost: true, paid: true }
  assert.deepEqual(automaticColumns(1600, all), ['key', 'title', 'status', 'priority', 'assignee', 'epic', 'model', 'tokens', 'list_cost', 'paid', 'created', 'updated'])
  assert.deepEqual(automaticColumns(1200, all), ['key', 'title', 'status', 'priority', 'updated'])
  // Planning columns follow Estimate in the default order.
  assert.deepEqual(orderOf(null).slice(9, 14), ['estimate', 'model', 'tokens', 'list_cost', 'paid'])
  // A 1700px table cannot hold every wide extra beside a readable title: the planning ones go first.
  assert.deepEqual(ids(1700, { phone: false, present: { ...all, estimate: true } }).filter(id => ['model', 'tokens', 'list_cost', 'paid', 'estimate'].includes(id)), ['estimate', 'model'])
  assert.deepEqual(ids(2600, { phone: false, present: { ...all, estimate: true } }).filter(id => ['model', 'tokens', 'list_cost', 'paid', 'estimate'].includes(id)), ['estimate', 'model', 'tokens', 'list_cost', 'paid'])
  // Chosen but empty (or cost the caller may not see): hidden.
  const prefs = { visible: ['status', 'tokens', 'list_cost', 'paid', 'model'] as ColumnId[] }
  assert.deepEqual(ids(1600, { phone: false, prefs, present: { tokens: true, model: true } }), ['key', 'title', 'status', 'model', 'tokens'])
  assert.deepEqual(ids(1600, { phone: false, prefs, present: {} }), ['key', 'title', 'status'])
  // Space runs out: Paid leaves before Tokens, Tokens before Model, all before Status.
  assert.deepEqual(ids(700, { phone: false, prefs, present: all }), ['key', 'title', 'status', 'model'])
  // Phones keep their card.
  assert.deepEqual(ids(390, { phone: true, prefs, present: all }), ['key', 'title', 'status', 'priority', 'updated'])
  assert.equal(COLUMN_BY_ID.get('list_cost')!.label, '≈ Cost')
  assert.equal(COLUMN_BY_ID.get('tokens')!.end, true)
})
