// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { automaticColumns, COLUMN_BY_ID, layoutWidths, loadedFitWidth, moveColumn, orderOf, releaseLabel, tagList, TITLE_TARGET, titleRoom, visibleColumns, widthOf, withHostColumns, type ColumnDef, type ColumnId } from '../src/lib/columns.ts'
import { groupRows, selectLoadedGroup, filtersFromQuery, filtersToQuery, type TicketRow } from '../src/lib/ticketList.ts'
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

test('a saved choice fixes order and visibility across desktop and phone widths', () => {
  const prefs = { order: ['updated', 'status', 'epic'] as const, visible: ['updated', 'status', 'epic', 'estimate'] as const }
  const p = { order: [...prefs.order], visible: [...prefs.visible] }
  assert.deepEqual(ids(2000, { phone: false, prefs: p }), ['key', 'title', 'updated', 'status', 'epic', 'estimate'])
  // All saved columns remain drawn at narrower desktop widths.
  assert.deepEqual(ids(900, { phone: false, prefs: p }), ['key', 'title', 'updated', 'status', 'epic', 'estimate'])
  assert.deepEqual(ids(700, { phone: false, prefs: p }), ['key', 'title', 'updated', 'status', 'epic', 'estimate'])
  assert.equal(visibleColumns(2000, { phone: false, prefs: p }).customised, true)
  assert.deepEqual(ids(390, { phone: true, prefs: p }), ['key', 'title', 'updated', 'status', 'epic', 'estimate'])
  assert.deepEqual(ids(390, { phone: true, present: { estimate: true } }), ['key', 'title', 'status', 'priority', 'updated', 'estimate'])
  assert.deepEqual(ids(390, { phone: true, present: { eta: true, estimate: true }, prefs: { visible: ['status'] } }), ['key', 'title', 'status'])
  // A saved choice that hides Assignee stays hidden when a live worker is present.
  assert.deepEqual(ids(1600, { phone: false, present: { workers: true }, prefs: { visible: ['status', 'updated'] } }), ['key', 'title', 'status', 'updated'])
  assert.deepEqual(ids(390, { phone: true, present: { workers: true }, prefs: p }), ['key', 'title', 'updated', 'status', 'epic', 'estimate'])
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

test('a saved choice retains progress and ETA when space gets tight', () => {
  const prefs = { visible: ['status', 'updated', 'estimate', 'progress', 'eta'] as const }
  const shown = ['key', 'title', 'status', 'estimate', 'updated', 'progress', 'eta'] as const
  const width = (names: readonly string[]) => names.reduce((sum, id) => sum + (id === 'title' ? 240 : COLUMN_BY_ID.get(id as ColumnId)!.width), 0)
  const p = { visible: [...prefs.visible] }
  assert.deepEqual(ids(width(shown), { phone: false, prefs: p }), [...shown])
  assert.deepEqual(ids(width(shown) - 1, { phone: false, prefs: p }), [...shown])
  const kept = shown.filter(id => id !== 'eta')
  assert.deepEqual(ids(width(kept) - 1, { phone: false, prefs: p }), [...shown])
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

test('planning joins Automatic when filled; saved ticks retain empty columns', () => {
  const all = { model: true, tokens: true, list_cost: true }
  assert.deepEqual(automaticColumns(1600, all), ['key', 'title', 'status', 'priority', 'assignee', 'epic', 'model', 'tokens', 'list_cost', 'created', 'updated'])
  assert.deepEqual(automaticColumns(1200, all), ['key', 'title', 'status', 'priority', 'updated'])
  assert.deepEqual(orderOf(null).slice(9, 14), ['estimate', 'model', 'suggested', 'tokens', 'list_cost'])
  const prefs = { visible: ['status', 'tokens', 'list_cost', 'paid', 'model'] as ColumnId[] }
  const saved = ['key', 'title', 'status', 'model', 'tokens', 'list_cost']
  assert.deepEqual(ids(1600, { phone: false, prefs, present: {} }), saved)
  assert.deepEqual(ids(700, { phone: false, prefs, present: all }), saved)
  assert.deepEqual(ids(1600, { phone: false, prefs, present: all, costAllowed: false }), saved.filter(id => id !== 'list_cost'))
  assert.deepEqual(ids(390, { phone: true, prefs, present: all }), saved)
  assert.equal(COLUMN_BY_ID.get('list_cost')!.label, 'Cost')
  assert.equal(COLUMN_BY_ID.get('list_cost')!.width, 128)
  assert.equal(COLUMN_BY_ID.has('paid'), false)
})

// AEON-628: the phone picker must change the actual cells, including an empty
// saved choice and cost visibility. Legacy ids and order use desktop rules.
test('phone cards honor saved optional columns and cost access', () => {
  const prefs = { order: ['paid', 'cost', 'assignee'] as ColumnId[], visible: ['paid', 'cost', 'assignee'] as ColumnId[] }
  assert.deepEqual(ids(390, { phone: true, prefs, costAllowed: true }), ['key', 'title', 'list_cost', 'cost', 'assignee'])
  assert.deepEqual(ids(390, { phone: true, prefs, costAllowed: false }), ['key', 'title', 'cost', 'assignee'])
  assert.equal(visibleColumns(390, { phone: true, prefs }).customised, true)
  assert.deepEqual(ids(390, { phone: true, prefs: { visible: [] }, present: { estimate: true, eta: true, progress: true } }), ['key', 'title'])
  assert.deepEqual(prefs.visible, ['paid', 'cost', 'assignee'])
})

// AEON-913 risk: one host must never alter the global picker or another host's widths.
test('AEON-913: host column definitions preserve the picker and size within their bounds', () => {
  const base = [COLUMN_BY_ID.get('key')!, COLUMN_BY_ID.get('title')!]
  const extra: ColumnDef = { id: 'suggestion', label: 'Suggestion', sort: null, width: 180, min: 120, max: 340 }
  const columns = withHostColumns(base, [extra, extra, { ...extra, id: 'status' }, { ...extra, id: 'bad"selector' }])
  assert.deepEqual(columns.map(column => column.id), ['key', 'title', 'suggestion'])
  assert.equal(COLUMN_BY_ID.has('suggestion' as ColumnId), false)
  const definitions = new Map<string, ColumnDef>(columns.map(column => [column.id, column]))
  assert.equal(widthOf('suggestion', { widths: { suggestion: 900 } }, definitions), 340)
  assert.equal(widthOf('suggestion', { widths: { suggestion: 1 } }, definitions), 120)
  const widths = layoutWidths(['key', 'title', 'suggestion'], 1000, null, { suggestion: 260 }, definitions)
  assert.equal(widths.suggestion, 260)
  assert.equal(1000 - Object.values(widths).reduce<number>((sum, value) => sum + (value ?? 0), 0), 622)
  const fitted = { key: 190, suggestion: 220 }
  assert.deepEqual(layoutWidths(['key', 'title', 'suggestion'], 1000, null, {}, definitions, fitted), fitted)
  // Fitted widths remain flexible for a deliberate Title resize; saved widths win.
  assert.deepEqual(layoutWidths(['key', 'title', 'suggestion'], 1000, { widths: { title: 600, suggestion: 260 } }, {}, definitions, fitted), { key: 140, suggestion: 260 })
  assert.deepEqual(base.map(column => column.id), ['key', 'title'])
})

test('AEON-913: project grouping retains metadata, loaded totals, held placement and URL state', () => {
  const project = { id: 'p-a', key: 'AEON', title: 'Aeon' }
  const row = (id: string, p: typeof project | null, project_key?: string) => ({ id, key: `WORK-${id}`, project: p, project_key }) as TicketRow
  const rows = [row('a', project), row('b', { id: 'p-z', key: 'ZED', title: 'Zed' }), row('c', project), row('d', null, 'HOST')]
  const groups = groupRows(rows, 'project', { 'p-a': 120 })
  assert.deepEqual(groups.map(group => group.project?.key), ['AEON', 'HOST', 'ZED'])
  assert.deepEqual(groups[0].project, project)
  assert.deepEqual(groups[0].rows, [rows[0], rows[2]])
  assert.equal(groups[0].total, 120)
  assert.equal(groups[0].loaded, 2)
  assert.equal(groups[0].hasMore, true)
  assert.equal(groups[2].hasMore, false)
  const held = groupRows([rows[0]], 'project', {}, { layout: row => ({ ...row, project: rows[1].project }) })
  assert.equal(held[0].project?.key, 'ZED')
  assert.equal(held[0].rows[0], rows[0])
  assert.equal(filtersToQuery(filtersFromQuery({ group: 'project' })).group, 'project')
})

// AEON-913: a content fit replaces defaults. On a normal list the table stays
// at the card width, so those fits must give width back until Title keeps its
// minimum. A saved or dragged width stays. Reproduced at 1000px: the fit used
// to leave Title at 188px; the same defaults leave it at 372px.
test('AEON-913: automatic fits give width back until Title keeps its minimum', () => {
  const shown = ['key', 'title', 'status', 'priority', 'assignee', 'updated'] as const
  const table = 1000
  const titleMin = COLUMN_BY_ID.get('title')!.min
  const sum = (w: Partial<Record<string, number>>) => Object.values(w).reduce((total: number, value) => total + (value ?? 0), 0)
  const titleOf = (w: Partial<Record<string, number>>) => table - sum(w)
  assert.equal(titleMin, 240)
  assert.equal(titleOf(layoutWidths([...shown], table)), 372)
  const modest = { key: 140, status: 140, priority: 112, assignee: 160, updated: 100 }
  assert.equal(titleOf(layoutWidths([...shown], table, null, {}, COLUMN_BY_ID, modest)), 348)
  assert.deepEqual(layoutWidths([...shown], table, null, {}, COLUMN_BY_ID, modest), modest)
  const fitted = { key: 180, status: 160, priority: 112, assignee: 240, updated: 120 }
  const widths = layoutWidths([...shown], table, null, {}, COLUMN_BY_ID, fitted)
  assert.equal(titleOf(widths), titleMin)
  // The column after Title gives way first and stops at its own minimum.
  assert.deepEqual(widths, { key: 180, status: 108, priority: 112, assignee: 240, updated: 120 })
  const saved = layoutWidths(['key', 'title', 'assignee', 'status', 'updated'], table, { widths: { assignee: 300 } }, {}, COLUMN_BY_ID, { key: 200, status: 200, updated: 180 })
  assert.equal(saved.assignee, 300)
  assert.equal(saved.status, COLUMN_BY_ID.get('status')!.min)
  assert.equal(saved.updated, 176)
  assert.equal(saved.key, 200)
  assert.equal(titleOf(saved), titleMin)
  const dragged = layoutWidths(['key', 'title', 'status', 'assignee'], table, null, { assignee: 800 }, COLUMN_BY_ID, { key: 84, status: 84 })
  assert.deepEqual(dragged, { key: 84, status: 84, assignee: 800 })
  assert.equal(titleOf(dragged), 32)
})

// The model column's designed 176px slot already holds its marks and name.
// A loaded measurement of that row is narrower, and the pixels move with the
// font (152 here, 156 in CI). Keep 176 until the value is wider.
test('AEON-913: a loaded fit keeps the designed width until the value is wider', () => {
  const model = COLUMN_BY_ID.get('model')!
  assert.equal(loadedFitWidth(model, 152), 176)
  assert.equal(loadedFitWidth(model, 156), 176)
  assert.equal(loadedFitWidth(model, 200), 200)
  assert.equal(loadedFitWidth(model, 400), model.max)
  assert.equal(loadedFitWidth(model, Number.NaN), model.width)
})

test('AEON-915: the ticket grid keeps a narrow loaded fit at the designed width', () => {
  const source = readFileSync(new URL('../src/components/work/TicketTable.vue', import.meta.url), 'utf8')
  assert.match(source, /next\[id\] = loadedFitWidth\(definitions\.value\.get\(id\)!/)
  assert.equal(COLUMN_BY_ID.get('key')!.max, 480)
})

test('AEON-913: group selection caps loaded rows at 100 and preserves other groups', () => {
  const rows = Array.from({ length: 120 }, (_, id) => ({ id: `row-${id}` }) as TicketRow)
  const group = { key: 'a', label: 'A', rows, total: 500, loaded: 120, hasMore: true }
  const selected = new Set(['other'])
  const next = selectLoadedGroup(group, selected, true)
  assert.equal(next.size, 100)
  assert.equal(next.has('other'), true)
  assert.equal(next.has('row-98'), true)
  assert.equal(next.has('row-99'), false)
  assert.deepEqual([...selectLoadedGroup(group, next, false)], ['other'])
  assert.deepEqual([...selected], ['other'])
})
