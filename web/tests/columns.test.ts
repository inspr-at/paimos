// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { adoptedCardWidth, agreedFrameWidth, automaticColumns, CARD_WIDTH_SAMPLE_CAP, cardWidthAfterSamples, cardWidthOnViewportResize, COLUMN_BY_ID, GIVE_WAY, latchParentShare, layoutTableWidth, layoutWidths, loadedFitWidth, nextMeasuredCardWidth, normalizeColumnIds, orderOf, PARENT_TITLE_WIDTH, parentColumnDefault, releaseLabel, resizeSampleWidth, RETURN_ROOM, savedColumnWidth, tagList, TITLE_MIN_TABLE, TITLE_TARGET, titleRoom, visibleColumns, widthOf, withHostColumns, type ColumnDef, type ColumnFit, type ColumnId, type ListPrefs } from '../src/lib/columns.ts'
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
  // Wide tables show Assignee even when nobody is assigned yet. Parent joins only when the caller says so.
  assert.deepEqual(automaticColumns(1600, {}), ['key', 'title', 'status', 'priority', 'assignee', 'created', 'updated'])
  assert.deepEqual(automaticColumns(1600, { assigned: true, estimate: true, release: true, tags: true }), ['key', 'title', 'status', 'priority', 'assignee', 'release', 'tags', 'estimate', 'created', 'updated'])
  assert.deepEqual(automaticColumns(1600, { parent: true }), ['key', 'title', 'status', 'priority', 'assignee', 'parent', 'created', 'updated'])
  // Wide extras step aside before Title gets cramped: 1500px cannot hold them all beside 420px of title.
  const present = { assigned: true, estimate: true, release: true, tags: true }
  assert.deepEqual(ids(1500, { phone: false, present }), ['key', 'title', 'status', 'priority', 'assignee', 'release', 'tags', 'created', 'updated'])
  assert.deepEqual(ids(1700, { phone: false, present }), ['key', 'title', 'status', 'priority', 'assignee', 'release', 'tags', 'estimate', 'created', 'updated'])
  assert.deepEqual(ids(2400, { phone: false, present }), ['key', 'title', 'status', 'priority', 'assignee', 'release', 'tags', 'estimate', 'created', 'updated'])
})

test('a saved choice fixes order and visibility across desktop and phone widths', () => {
  // A saved Epic id is Parent. Phones still leave Parent out of the row.
  const p = { order: ['updated', 'status', 'epic'] as unknown as ColumnId[], visible: ['updated', 'status', 'epic', 'estimate'] as unknown as ColumnId[] }
  assert.deepEqual(ids(2000, { phone: false, prefs: p }), ['key', 'title', 'updated', 'status', 'parent', 'estimate'])
  // All saved columns remain drawn at narrower desktop widths.
  assert.deepEqual(ids(900, { phone: false, prefs: p }), ['key', 'title', 'updated', 'status', 'parent', 'estimate'])
  assert.deepEqual(ids(700, { phone: false, prefs: p }), ['key', 'title', 'updated', 'status', 'parent', 'estimate'])
  assert.equal(visibleColumns(2000, { phone: false, prefs: p }).customised, true)
  assert.deepEqual(ids(390, { phone: true, prefs: p }), ['key', 'title', 'updated', 'status', 'estimate'])
  assert.deepEqual(ids(390, { phone: true, present: { estimate: true } }), ['key', 'title', 'status', 'priority', 'updated', 'estimate'])
  assert.deepEqual(ids(390, { phone: true, present: { eta: true, estimate: true }, prefs: { visible: ['status'] } }), ['key', 'title', 'status'])
  // A saved choice that hides Assignee stays hidden when a live worker is present.
  assert.deepEqual(ids(1600, { phone: false, present: { workers: true }, prefs: { visible: ['status', 'updated'] } }), ['key', 'title', 'status', 'updated'])
  assert.deepEqual(ids(390, { phone: true, present: { workers: true, parent: true }, prefs: p }), ['key', 'title', 'updated', 'status', 'estimate'])
})

test('order keeps Key and Title first and appends unknown or missing columns', () => {
  assert.deepEqual(orderOf({ order: ['title', 'created', 'bogus' as never, 'created'] }).slice(0, 4), ['key', 'title', 'created', 'status'])
  assert.equal(orderOf(null).length, 18)
  assert.deepEqual(automaticColumns(1600, { eta: true }), ['key', 'title', 'status', 'priority', 'assignee', 'created', 'updated', 'eta'])
  assert.deepEqual(automaticColumns(1600, { eta: true, progress: true }), ['key', 'title', 'status', 'priority', 'assignee', 'created', 'updated', 'progress', 'eta'])
  assert.deepEqual(automaticColumns(1200, { progress: true, eta: true }), ['key', 'title', 'status', 'priority', 'updated', 'progress', 'eta'])
  assert.deepEqual(ids(390, { phone: true, present: { eta: true } }), ['key', 'title', 'status', 'priority', 'updated', 'eta'])
  assert.deepEqual(ids(390, { phone: true, present: { eta: true, progress: true } }), ['key', 'title', 'status', 'priority', 'updated', 'progress', 'eta'])
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
  const wide = ['key', 'title', 'status', 'priority', 'assignee', 'parent', 'created', 'updated'] as const
  const sum = (w: Partial<Record<string, number>>) => Object.values(w).reduce((a: number, b) => a + (b ?? 0), 0)
  // Narrow: nothing grows, Title takes the rest.
  assert.deepEqual(layoutWidths([...wide], 1400), { key: 118, status: 138, priority: 112, assignee: 156, parent: 160, created: 104, updated: 104 })
  // 2460px: Parent and Assignee reach their maximum. The spare that remains goes back to Title.
  const at2460 = layoutWidths([...wide], 2460)
  assert.equal(at2460.status, 138)
  assert.equal(at2460.parent, 360)
  assert.equal(at2460.assignee, 320)
  assert.equal(2460 - sum(at2460), 1204)
  // Between: growth is proportional and Title sits at the target.
  const at2000 = layoutWidths([...wide], 2000)
  assert.ok(at2000.parent! > 160 && at2000.parent! < 360 && at2000.assignee! > 156 && at2000.assignee! < 320)
  assert.ok(Math.abs(2000 - sum(at2000) - TITLE_TARGET) <= 2)
  // A width the person gave wins: Parent stays, Title's own width becomes its target.
  const sized = layoutWidths([...wide], 2000, { widths: { parent: 200, title: 700 } })
  assert.equal(sized.parent, 200)
  assert.ok(Math.abs(2000 - sum(sized) - 700) <= 2 || sized.assignee === 320)
  // A saved Epic width is that Parent width, and it is not grown away.
  assert.equal(layoutWidths([...wide], 2000, { widths: { epic: 240 } }).parent, 240)
  assert.equal(savedColumnWidth('parent', { widths: { parent: 180, epic: 240 } }), 180)
  assert.equal(layoutWidths([...wide], 2000, { widths: { parent: 180, epic: 240 } }).parent, 180)
  // Live drag widths count as sized too.
  assert.equal(layoutWidths([...wide], 2000, null, { assignee: 180 }).assignee, 180)
})

// A content fit used to freeze Parent, so the title never appeared at 2800px.
// The fit is the start, not a ceiling: spare beyond Title's target still widens it.
test('AEON-1470: a content-fitted Parent still grows once Title is at its target', () => {
  const ids = ['key', 'title', 'status', 'priority', 'assignee', 'parent', 'release', 'tags', 'estimate', 'suggested', 'created', 'updated'] as const
  const fits: Partial<Record<string, number>> = {}
  for (const id of ids) if (id !== 'title') fits[id] = COLUMN_BY_ID.get(id)!.width
  const sum = (w: Partial<Record<string, number>>) => Object.values(w).reduce((total: number, value) => total + (value ?? 0), 0)
  const narrow = layoutWidths([...ids], 2450, null, {}, COLUMN_BY_ID, fits)
  assert.ok(narrow.parent! < PARENT_TITLE_WIDTH, `parent ${narrow.parent}`)
  const wide = layoutWidths([...ids], 2690, null, {}, COLUMN_BY_ID, fits)
  assert.ok(wide.parent! >= PARENT_TITLE_WIDTH, `parent ${wide.parent}`)
  const title = 2690 - sum(wide)
  assert.ok(title > 900 && title < 1140, `title ${title}`)
  // Fits wider than the design (status +35 and the other measured columns)
  // eat the surplus. The 2560px card stays under 200; the 2800px card still
  // crosses it, and Title stays near its target.
  const inflated: Partial<Record<string, number>> = {
    ...fits, key: fits.key! + 20, status: fits.status! + 35, priority: fits.priority! + 15,
    assignee: fits.assignee! + 20, release: fits.release! + 10, tags: fits.tags! + 40,
    estimate: fits.estimate! + 10, suggested: fits.suggested! + 20, created: fits.created! + 12, updated: fits.updated! + 12,
  }
  const narrowScreen = layoutWidths([...ids], 2464, null, {}, COLUMN_BY_ID, inflated)
  assert.ok(narrowScreen.parent! < PARENT_TITLE_WIDTH, `inflated 2464 parent ${narrowScreen.parent}`)
  const wideScreen = layoutWidths([...ids], 2704, null, {}, COLUMN_BY_ID, inflated)
  assert.ok(wideScreen.parent! >= PARENT_TITLE_WIDTH, `inflated 2704 parent ${wideScreen.parent}`)
  const wideTitle = 2704 - sum(wideScreen)
  assert.ok(wideTitle > 900 && wideTitle < 1140, `inflated title ${wideTitle}`)
})

// A docked panel leaves the list a narrow card; a table that grew to its
// minimums slid under the panel. Wide tables still scroll rather than cut Title.
test('AEON-1470: a docked table stays inside its card; from 721 px the never-leaving minimums may scroll', () => {
  assert.equal(TITLE_MIN_TABLE, 721)
  // The routed panel at 1000 px leaves about 466 px; key, Title, Status and Priority need more.
  assert.equal(layoutTableWidth(466, 509), 466)
  assert.equal(layoutTableWidth(720, 760), 720)
  // Needs attention at 1024: a long key and four host columns that never leave.
  assert.equal(layoutTableWidth(976, 1056), 1056)
  assert.equal(layoutTableWidth(721, 760), 760)
  assert.equal(layoutTableWidth(1200, 900), 1200)
  // Phone cards always take the card's width.
  assert.equal(layoutTableWidth(360, 509, true), 360)
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
  assert.deepEqual(tagList({ tags: [{ id: 16, name: 'CUSTOMERPORTAL', color: 'blue' }, 'worker-8', { name: ' ' }, 7] }), [{ name: 'CUSTOMERPORTAL', color: 'blue' }, { name: 'worker-8', color: '' }])
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
  assert.deepEqual(automaticColumns(1600, all), ['key', 'title', 'status', 'priority', 'assignee', 'model', 'tokens', 'list_cost', 'created', 'updated'])
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
  // AEON-1470: a dragged width gives way too once the fits are at their minimum; Title keeps its own.
  const dragged = layoutWidths(['key', 'title', 'status', 'assignee'], table, null, { assignee: 800 }, COLUMN_BY_ID, { key: 84, status: 84 })
  assert.deepEqual(dragged, { key: 84, status: 84, assignee: 592 })
  assert.equal(titleOf(dragged), titleMin)
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

// ---------- AEON-1470: Title keeps room at every desktop width ----------
// Risk: Title squeezed under its 240 px minimum by saved, dragged or fitted widths or by too many columns; columns that
// leave in the wrong order, take the sorted column with them, go below their minimum, flap at a threshold, or change
// with anything but the width, the chosen columns and the sort. The ticket grid's own minimums: the first cell's slots
// (108 px when grouped and selectable) and Title's person-waiting slot (240 + 24 px).
const GRID_MINS = { key: 108, title: 264 }
const GRID_DEFS = new Map<string, ColumnDef>([...COLUMN_BY_ID, ['key', { ...COLUMN_BY_ID.get('key')!, min: GRID_MINS.key }], ['title', { ...COLUMN_BY_ID.get('title')!, min: GRID_MINS.title }]])
const SET_1388: ColumnId[] = ['status', 'priority', 'assignee', 'estimate', 'updated']
const ALL: ColumnId[] = COLUMN_BY_ID.size ? [...COLUMN_BY_ID.keys()].filter(id => id !== 'key' && id !== 'title') : []
const WIDE_SAVED = { status: 260, priority: 200, assignee: 320, estimate: 180, updated: 200, key: 300 }
// The grid at one table width: which columns show, which left, every width, and Title's (the rest).
function grid(width: number, visible: ColumnId[], options: { widths?: ListPrefs['widths']; fits?: Partial<Record<string, number>>; fit?: ColumnFit } = {}) {
  const prefs: ListPrefs = { visible, widths: options.widths }
  const out = visibleColumns(width, { phone: false, prefs, fit: { mins: GRID_MINS, ...options.fit } })
  const ids = out.columns.map(column => column.id)
  const widths = layoutWidths(ids, width, prefs, {}, GRID_DEFS, options.fits ?? {})
  return { ids, dropped: out.dropped, widths, title: width - Object.values(widths).reduce<number>((sum, value) => sum + (value ?? 0), 0) }
}

test('AEON-1470 Title is never under 240 px at 760–1440 px, for the 1388 set, every column and wide saved widths', () => {
  // Content fits as wide as their columns allow: the worst a loaded list can measure.
  const fits = Object.fromEntries(ALL.map(id => [id, COLUMN_BY_ID.get(id)!.max]))
  for (const width of [721, 760, 900, 1000, 1280, 1440]) {
    for (const [name, visible, options] of [['1388 set', SET_1388, { fits }], ['all columns', ALL, { fits }], ['wide saved widths', SET_1388, { widths: WIDE_SAVED }], ['all, saved', ALL, { widths: WIDE_SAVED, fits }]] as const) {
      const at = grid(width, [...visible], options)
      assert.ok(at.title >= 240 && at.title >= GRID_MINS.title, `${name} at ${width}: title ${at.title}`)
      // No column below its minimum, and the grid fills the table exactly.
      for (const [id, value] of Object.entries(at.widths)) assert.ok(value! >= GRID_DEFS.get(id)!.min, `${name} at ${width}: ${id} ${value}`)
      for (const id of ['key', 'title', 'status', 'priority'] as const) assert.ok(at.ids.includes(id), `${name} at ${width} keeps ${id}`)
    }
  }
  // The saved widths themselves stay as they were; only the layout narrows them.
  assert.deepEqual(WIDE_SAVED, { status: 260, priority: 200, assignee: 320, estimate: 180, updated: 200, key: 300 })
  // 1440 px holds the 1388 set at its saved widths with room to spare: nothing gives way.
  const roomy = grid(1440, SET_1388, { widths: { assignee: 300 } })
  assert.equal(roomy.widths.assignee, 300)
  assert.deepEqual(roomy.dropped, [])
})

test('AEON-1470 saved widths shrink in proportion, after the fits and never below their minimum', () => {
  // 1000 px, 1388 set: fits give way first (nearest Title first), saved widths keep theirs while that is enough.
  const mixed = grid(1000, SET_1388, { widths: { assignee: 300 }, fits: { key: 118, status: 138, priority: 112, estimate: 96, updated: 104 } })
  assert.equal(mixed.widths.assignee, 300)
  assert.equal(mixed.title, GRID_MINS.title)
  // Every width saved wide: each gives up the same share of its room above its minimum.
  const saved = grid(1000, SET_1388, { widths: WIDE_SAVED })
  const share = (id: keyof typeof WIDE_SAVED) => (WIDE_SAVED[id] - saved.widths[id]!) / (WIDE_SAVED[id] - GRID_DEFS.get(id)!.min)
  for (const id of ['status', 'priority', 'assignee', 'estimate', 'updated', 'key'] as const) assert.ok(Math.abs(share(id) - share('status')) < .02, `${id} ${share(id)}`)
  assert.ok(saved.title >= GRID_MINS.title && saved.title <= GRID_MINS.title + 6, `title ${saved.title}`)
})

test('AEON-1470 whole columns leave in the give-way order; Key, Title, Status, Priority and the sorted column stay', () => {
  assert.deepEqual(GIVE_WAY, ['updated', 'created', 'estimate', 'tokens', 'list_cost', 'paid', 'suggested', 'model', 'eta', 'progress', 'tags', 'cost', 'release', 'parent', 'assignee'])
  // Every column chosen, the table narrowing a pixel at a time: each step adds the next column of the order, never another.
  const left: ColumnId[] = []
  for (let width = 2600; width >= 721; width--) {
    const { dropped } = grid(width, ALL)
    if (dropped.length !== left.length) { assert.deepEqual(dropped.slice(0, left.length), left, `at ${width}`); left.splice(0, left.length, ...dropped) }
  }
  assert.deepEqual(left, GIVE_WAY.filter(id => id !== 'paid').slice(0, left.length))
  assert.ok(left.length >= 12, `left ${left.join()}`)
  // 1000 px: the 1388 set fits at its minimums; every column leaves updated, created, estimate … until the rest fits.
  assert.deepEqual(grid(1000, SET_1388).dropped, [])
  assert.deepEqual(grid(1000, ALL).dropped, ['updated', 'created', 'estimate', 'tokens', 'list_cost', 'suggested', 'model', 'eta', 'progress'])
  // Sorted by Updated (also as a second key): Updated stays and the next one in the order leaves instead.
  const sorted = grid(1000, ALL, { fit: { sorted: ['updated_at'] } })
  assert.ok(sorted.ids.includes('updated'))
  assert.deepEqual(sorted.dropped, ['created', 'estimate', 'tokens', 'list_cost', 'suggested', 'model', 'eta', 'progress', 'tags'])
  assert.deepEqual(grid(1000, ALL, { fit: { sorted: ['state', 'updated_at'] } }).dropped, sorted.dropped)
  assert.ok(grid(1000, SET_1388, { fit: { sorted: ['updated_at'] } }).ids.includes('updated'))
  // A host column never leaves; its minimum is taken from the room.
  assert.deepEqual(grid(1000, SET_1388, { fit: { reserve: 120 } }).dropped, [])
  assert.deepEqual(grid(880, SET_1388, { fit: { reserve: 120 } }).dropped, ['updated'])
})

test('AEON-1470 the set depends only on width, chosen columns, minimums and sort; a column that left returns with 16 px to spare', () => {
  // Widths never decide it: saved, dragged or fitted widths leave the same set.
  for (const width of [760, 1000, 1280]) {
    const plain = grid(width, ALL).ids
    assert.deepEqual(grid(width, ALL, { widths: WIDE_SAVED }).ids, plain)
    assert.deepEqual(grid(width, ALL, { fits: { assignee: 320, parent: 360, status: 260 } }).ids, plain)
  }
  // The 1388 set needs 108 + 264 + 84 + 72 + 96 + 72 + 80 = 776 px at its minimums.
  const need = 776
  assert.equal(RETURN_ROOM, 16)
  assert.deepEqual(grid(need, SET_1388).dropped, [])
  const out = grid(need - 1, SET_1388)
  assert.deepEqual(out.dropped, ['updated'])
  // Left out: ±8 px around that threshold keeps it out.
  for (const width of [need - 1 - 8, need - 1 + 8]) assert.deepEqual(grid(width, SET_1388, { fit: { previous: out.dropped } }).dropped, ['updated'], `${width}`)
  // It returns once 16 px are spare, and ±8 px around that threshold keeps it in.
  const back = need + RETURN_ROOM
  assert.deepEqual(grid(back - 1, SET_1388, { fit: { previous: out.dropped } }).dropped, ['updated'])
  assert.deepEqual(grid(back, SET_1388, { fit: { previous: out.dropped } }).dropped, [])
  for (const width of [back - 8, back + 8]) assert.deepEqual(grid(width, SET_1388, { fit: { previous: [] } }).dropped, [], `${width}`)
  // Several columns come back one at a time as the width allows.
  const narrow = grid(721, ALL)
  const wider = grid(1000, ALL, { fit: { previous: narrow.dropped } })
  assert.ok(wider.dropped.length < narrow.dropped.length && narrow.dropped.slice(0, wider.dropped.length).join() === wider.dropped.join())
})

test('AEON-1570 a growing card is adopted after a disagreeing frame pair, and a shrink ignores a later wide pair', () => {
  assert.equal(CARD_WIDTH_SAMPLE_CAP, 16)
  assert.equal(agreedFrameWidth(760, 792), null)
  assert.equal(agreedFrameWidth(null, 792), null)
  assert.equal(agreedFrameWidth(792, null), null)
  assert.equal(agreedFrameWidth(792, 0), null)
  // mid is not required to be positive: only next <= 0 refuses.
  assert.equal(agreedFrameWidth(0, 0.4), 0.4)
  assert.equal(agreedFrameWidth(700, 700), 700)
  assert.equal(agreedFrameWidth(700, 700.5), 700.5)
  assert.equal(agreedFrameWidth(700, 700.6), null)
  assert.equal(cardWidthAfterSamples([], 760, true), null)
  assert.equal(cardWidthAfterSamples([792], 760, true), null)
  assert.equal(cardWidthAfterSamples([760, 792], 760, true), null)
  assert.equal(cardWidthAfterSamples([700, 700], 760, false), 700)
  // The old settle inspected the first pair and gave up, which left Updated hidden after the card had grown.
  assert.equal(cardWidthAfterSamples([760, 792, 792, 792], 760, true), 792)
  assert.equal(cardWidthAfterSamples([760, 792, 791.4, 792], 760, true), null)
  assert.equal(cardWidthAfterSamples([null, null, 792, 792], 760, true), 792)
  // The first agreement wins. A later pair does not replace it.
  assert.equal(cardWidthAfterSamples([700, 700, 800, 800], 700, true), 700)
  // A later wide pair during a shrink is the previous frame. The first agreeing pair still replaces a stale narrow box.
  assert.equal(cardWidthAfterSamples([760, 792, 792, 792], 760, false), null)
  assert.equal(cardWidthAfterSamples([1000, 1000], 760, false), 1000)
  // A wider live card is this resize when the window grew, and ignored when it shrank.
  assert.equal(nextMeasuredCardWidth(760, 792, true), 792)
  assert.equal(nextMeasuredCardWidth(800, 900, false), 800)
  assert.equal(nextMeasuredCardWidth(800, 780, false), 780)
  assert.equal(nextMeasuredCardWidth(800, 780, true), 780)
  assert.equal(nextMeasuredCardWidth(800, 0, true), 800)
})

// AEON-1600. Risk: a shrink whose content box is still the previous frame keeps the wide column widths.
// Title has no col width, so a 903 px card still wearing the 788 px design set measures 115 (CI on 8c375702).
test('AEON-1600 a shrink lays out at the viewport delta while the content box is still the previous frame', () => {
  const shown = ['key', 'title', 'status', 'priority', 'assignee', 'parent', 'updated']
  const wide = 1300
  const card = 903
  const worn = Object.values(layoutWidths(shown, wide)).reduce((sum, width) => sum + (width ?? 0), 0)
  assert.equal(worn, 788)
  assert.equal(card - worn, 115)
  assert.equal(nextMeasuredCardWidth(wide, wide, false), wide)
  assert.equal(cardWidthOnViewportResize(wide, wide, card - wide), card)
  assert.equal(cardWidthOnViewportResize(wide, card, card - wide), card)
  assert.equal(cardWidthOnViewportResize(wide, card + 6, card - wide), card + 6)
  const fitted = Object.values(layoutWidths(shown, card)).reduce((sum, width) => sum + (width ?? 0), 0)
  assert.ok(card - fitted >= 240, `title ${card - fitted}`)
  assert.equal(cardWidthOnViewportResize(card, card, wide - card), card)
  assert.equal(cardWidthOnViewportResize(card, wide, wide - card), wide)
  assert.equal(cardWidthOnViewportResize(wide, null, card - wide), card)
  assert.equal(cardWidthOnViewportResize(0, null, -10), 0)
  // The frame pair that still shows the wide card must not replace the delta.
  assert.equal(resizeSampleWidth(wide, card), null)
  assert.equal(resizeSampleWidth(card, card), card)
  assert.equal(resizeSampleWidth(card - 6, card), card - 6)
  // A gutter swing can leave the shrunk box 40 px above the viewport delta.
  assert.equal(resizeSampleWidth(card + 48, card), card + 48)
  assert.equal(resizeSampleWidth(card + 49, card), null)
  assert.equal(resizeSampleWidth(wide, null), wide)
  assert.equal(resizeSampleWidth(null, card), null)
  assert.equal(resizeSampleWidth(0, card), null)
})

// AEON-1594. Risk: the first agreeing pair of a shrink is the previous wide frame. Adopting it lays that
// column set into the card that has already narrowed, and a later column measures 0.
test('AEON-1594 a shrink does not adopt a sample wider than the card now', () => {
  assert.equal(adoptedCardWidth(null, 760, false), null)
  assert.equal(adoptedCardWidth(0, 760, false), null)
  assert.equal(adoptedCardWidth(1000, 760, false), null)
  assert.equal(adoptedCardWidth(760.6, 760, false), null)
  // The live card replaced a stale narrow box, including when the window itself did not grow.
  assert.equal(adoptedCardWidth(1000, 1000, false), 1000)
  assert.equal(adoptedCardWidth(760, 760, false), 760)
  assert.equal(adoptedCardWidth(700, 760, false), 700)
  assert.equal(adoptedCardWidth(760.4, 760, false), 760.4)
  assert.equal(adoptedCardWidth(1000, null, false), 1000)
  // A grow may adopt the wider pair while the content box is still the previous frame.
  assert.equal(adoptedCardWidth(1000, 760, true), 1000)
})

// AEON-1470 (fix 7). Risk: Automatic's wide extras stepping aside outside the give-way, so the column the list is sorted
// by goes too, Display is never told what the width hides, and the set flaps at the extras' threshold.
test('AEON-1470 Automatic wide extras give way in the same pipeline: the sorted one stays, every one that leaves is reported, and returns with 16 px to spare', () => {
  // The 420 px wide-table target must not hide Assignee on a narrower table:
  // its widths can shrink and still leave Title at its minimum. This also
  // restores Assignee after it stepped aside in the wide-table pipeline.
  const narrowPresent = { assigned: true, parent: true }
  for (const width of [976, 1100, 1184, 1440, 1499]) for (const previous of [[], ['assignee']]) {
    const narrow = visibleColumns(width, { phone: false, present: narrowPresent, fit: { mins: GRID_MINS, previous } })
    assert.deepEqual(narrow.columns.map(column => column.id), ['key', 'title', 'status', 'priority', 'assignee', 'parent', 'updated'], `${width}, previously hidden: ${previous}`)
    assert.deepEqual(narrow.dropped, [])
  }
  const present = { assigned: true, parent: true, estimate: true, release: true, tags: true, model: true, tokens: true, list_cost: true, suggested: true, progress: true, eta: true }
  const at = (width: number, fit: ColumnFit = {}, shown: typeof present | { assigned: true; estimate: true; release: true; tags: true } = present) => {
    const out = visibleColumns(width, { phone: false, present: shown, costAllowed: true, fit: { mins: GRID_MINS, ...fit } })
    return { ids: out.columns.map(column => column.id), dropped: out.dropped }
  }
  const chosen = [...automaticColumns(1800, present)].sort()
  // 1800 px: every minimum fits beside Title's, yet Title keeps its automatic room; the extras leave in their order.
  const plain = at(1800)
  assert.deepEqual(plain.dropped, ['suggested', 'list_cost', 'tokens', 'model', 'estimate', 'tags'])
  assert.deepEqual([...plain.ids, ...plain.dropped].sort(), chosen, 'every chosen column is either shown or reported')
  // Sorted by Tokens: Tokens stays, the next extra leaves instead, and each hidden one is still reported.
  const sorted = at(1800, { sorted: ['tokens'] })
  assert.ok(sorted.ids.includes('tokens'), sorted.ids.join())
  assert.deepEqual(sorted.dropped, ['suggested', 'list_cost', 'model', 'estimate', 'tags'])
  assert.deepEqual([...sorted.ids, ...sorted.dropped].sort(), chosen)
  // Legacy Paid sorts keep Cost the same way.
  assert.ok(at(1800, { sorted: ['paid'] }).ids.includes('list_cost'))
  // Estimate returns at 1560 px beside 420 px of Title; once it left, ±8 px around there keeps it out until 16 px spare.
  const small = { assigned: true, estimate: true, release: true, tags: true } as const
  assert.deepEqual(at(1559, {}, small).dropped, ['estimate'])
  assert.deepEqual(at(1560, {}, small).dropped, [])
  for (const width of [1552, 1568, 1575]) assert.deepEqual(at(width, { previous: ['estimate'] }, small).dropped, ['estimate'], `${width}`)
  assert.deepEqual(at(1576, { previous: ['estimate'] }, small).dropped, [])
  // A saved choice has no automatic extras: only the minimums decide, so 1800 px keeps every chosen column.
  const saved = visibleColumns(1800, { phone: false, costAllowed: true, prefs: { visible: chosen.filter(id => id !== 'key' && id !== 'title') as ColumnId[] }, fit: { mins: GRID_MINS } })
  assert.deepEqual(saved.dropped, [])
})

// AEON-1470 Parent: the direct parent is a column. Risk: a saved Epic choice lost or resized, the 30% default flipping
// after the first reading, Parent crowding Title under 240 px, or Parent drawn on a phone, in a parent group, or in the outline.
test('AEON-1470 Parent migrates Epic, latches at 30 percent, and leaves phones, groups and the outline', () => {
  const parent = COLUMN_BY_ID.get('parent')!
  assert.equal(parent.label, 'Parent')
  assert.deepEqual([parent.width, parent.min, parent.max], [160, 96, 360])
  assert.equal(orderOf(null).length, 18)
  assert.equal(orderOf(null)[orderOf(null).indexOf('assignee') + 1], 'parent')
  assert.deepEqual(normalizeColumnIds(['epic', 'paid', 'epic', 'parent']), ['parent', 'list_cost'])
  assert.equal(savedColumnWidth('parent', { widths: { epic: 240 } }), 240)
  assert.equal(savedColumnWidth('parent', { widths: { parent: 200, epic: 240 } }), 200)
  assert.equal(parentColumnDefault(10, 4), true)
  assert.equal(parentColumnDefault(10, 3), true)
  assert.equal(parentColumnDefault(10, 1), false)
  assert.equal(parentColumnDefault(0, 0), false)
  assert.equal(latchParentShare(null, undefined, undefined), null)
  assert.equal(latchParentShare(null, 10, 4), true)
  assert.equal(latchParentShare(null, 10, 1), false)
  assert.equal(latchParentShare(true, 10, 1), true)
  assert.equal(latchParentShare(false, 10, 4), false)
  const on = ids(1440, { phone: false, present: { parent: true, assigned: true } })
  const hidden = ids(1440, { phone: false, present: { parent: true, assigned: true }, hideParent: true })
  assert.deepEqual(on, ['key', 'title', 'status', 'priority', 'assignee', 'parent', 'updated'])
  assert.deepEqual(hidden, on.filter(id => id !== 'parent'))
  const saved = { visible: ['status', 'priority', 'parent', 'assignee', 'updated'] as ColumnId[] }
  const laid = visibleColumns(1200, { phone: false, prefs: saved, hideParent: true })
  assert.deepEqual(laid.columns.map(column => column.id), ['key', 'title', 'status', 'priority', 'assignee', 'updated'])
  assert.deepEqual(laid.dropped, [])
  assert.deepEqual(ids(390, { phone: true, present: { parent: true }, prefs: { visible: ['epic', 'status'] as unknown as ColumnId[] } }), ['key', 'title', 'status'])
  const at1024 = visibleColumns(1024, { phone: false, present: { parent: true, assigned: true }, fit: { mins: GRID_MINS } })
  const shown = at1024.columns.map(column => column.id)
  assert.ok(shown.includes('parent'))
  const widths = layoutWidths(shown, 1024, null, {}, GRID_DEFS)
  const title = 1024 - Object.values(widths).reduce<number>((sum, value) => sum + (value ?? 0), 0)
  assert.ok(title >= 240, `title ${title}`)
  const source = readFileSync(new URL('../src/components/work/TicketTable.vue', import.meta.url), 'utf8')
  assert.doesNotMatch(source, /parent-chip/)
  assert.match(source, /padding-right: 58px/)
  assert.match(source, /\.parent-glyph \{[^}]*color: var\(--ink-3\)/)
  assert.match(source, /\.parent-key \{[^}]*var\(--mono\)/)
  assert.doesNotMatch(source, /\.c-parent \{[^}]*display:\s*none/)
})
