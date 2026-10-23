// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest'
import { reactive } from 'vue'
import { QuoteEditor } from '../src/lib/quotes/editor.ts'
import { documentTotal, positionTotal } from '../src/lib/quotes/layout.ts'
import type { QuoteDocumentData, QuotePosition } from '../src/lib/quotes/types.ts'
import type { TextNode } from '../src/lib/quotes/types.ts'
import * as prose from '../src/lib/quotes/prose.ts'
import { APIError } from '../src/lib/api.ts'
import { sourceModule } from './record-source'

const sectionId = '11111111-1111-4111-8111-111111111111'
const fixture = (): QuoteDocumentData => ({
  schema_version: 1, minimum_writer_version: 1, title: 'Quote', subtitle: '', project_ref: '',
  offer_date: '2026-09-24', valid_until: '2026-10-24', currency: 'EUR',
  sender: {}, recipient: {}, legal: {}, layout: {},
  sections: [{ id: sectionId, heading: 'Scope', body: 'Before', nodes: [{ id: '22222222-2222-4222-8222-222222222222', kind: 'paragraph', text: 'Before' }] }],
  positions: [], net_total_cents: 0,
})

describe('QuoteEditor snapshots', () => {
  it('hydrates nested reactive document data and preserves independent undo history', () => {
    const source = reactive(fixture())
    const editor = new QuoteEditor(source)
    expect(editor.document).toEqual(fixture())
    editor.editSection(sectionId, { heading: 'After' })
    source.sections[0]!.heading = 'Changed outside editor'
    expect(editor.document.sections[0]!.heading).toBe('After')
    editor.undo()
    expect(editor.document.sections[0]!.heading).toBe('Scope')
    editor.redo()
    expect(editor.document.sections[0]!.heading).toBe('After')
    editor.replaceDocument(reactive(fixture()))
    expect(editor.document.sections[0]!.heading).toBe('Scope')
  })

  it('persists section settings and restores them through undo and redo', () => {
    const editor = new QuoteEditor(reactive(fixture()))
    editor.setSectionSettings(sectionId, { numberingStyle: 'upper-roman', pageBreakBefore: true, keepTogether: false, spacingBeforeMm: '3.5', spacingAfterMm: '2' })
    expect(editor.document.sections[0]).toMatchObject({ numbering_style: 'upper-roman', page_break_before: true, keep_together: false, spacing_before_mm: '3.5', spacing_after_mm: '2.0' })
    editor.undo()
    expect(editor.document.sections[0]!.numbering_style).toBeUndefined()
    editor.redo()
    expect(editor.document.sections[0]!.spacing_after_mm).toBe('2.0')
    expect(() => editor.setSectionSettings(sectionId, { spacingBeforeMm: '40.1' })).toThrow(/range/)
  })
})

const position = (quantity: string, unitPriceCents: number): QuotePosition => ({
  id: '33333333-3333-4333-8333-333333333333', pricing_source: 'manual', short_text: 'Work', long_text: '',
  quantity, unit_label: 'item', unit_price_cents: unitPriceCents, total_cents: 0, currency: 'EUR',
})

describe('cent half-up totals', () => {
  it('rounds an exact half cent up and refuses a float quantity or price', () => {
    expect(positionTotal(position('1.00', 100))).toBe(100)
    expect(positionTotal(position('1.50', 9999))).toBe(14999)
    expect(positionTotal(position('0.01', 50))).toBe(1)
    expect(positionTotal(position('0.01', 49))).toBe(0)
    expect(positionTotal(position('2.25', 10))).toBe(23)
    expect(documentTotal([position('0.01', 50), position('1.00', 100)])).toBe(101)
    expect(() => positionTotal(position('1.005', 100))).toThrow(/quantity/)
    expect(() => positionTotal({ ...position('1.00', 100), unit_price_cents: 10.5 })).toThrow(/minor-unit/)
  })
})

describe('AEON-1682: persisted editor vectors', () => {
  it('keeps six numbered levels, marks, measurements, IDs, order and exact money through history', () => {
    const document = fixture()
    document.sections[0]!.nodes[0]!.text = 'A😀B'
    document.sections[0]!.nodes[0]!.marks = [{ start: 1, end: 3, bold: true }]
    document.positions = [position('1.25', 105)]
    document.positions[0]!.total_cents = 131
    document.net_total_cents = 131
    const editor = new QuoteEditor(document)
    const firstId = document.sections[0]!.nodes[0]!.id
    editor.select({ sectionId, text: { sectionId, anchor: { nodeId: firstId, offset: 1 }, focus: { nodeId: firstId, offset: 3 } } })
    editor.setMarks('italic', true)
    editor.setListMode('numbered')
    let nodes = editor.document.sections[0]!.nodes
    let at = { nodeId: firstId, offset: 4 }
    for (let depth = 1; depth <= 5; depth++) {
      const inserted = prose.replaceText(nodes, at, at, `\nLevel ${depth}`)
      nodes = prose.setListMode(inserted.nodes, [inserted.caret.nodeId], 'numbered')
      at = inserted.caret
      nodes[nodes.length - 1]!.depth = depth
    }
    editor.editSection(sectionId, { nodes })
    editor.select({ sectionId, text: { sectionId, anchor: { nodeId: firstId, offset: 0 }, focus: { nodeId: firstId, offset: 0 } } })
    editor.setNumbering({ mode: 'section' })
    editor.setOffsets({ markerX: '-1.5', markerY: '0.5', textStart: '2.0' })
    const newSection = editor.insertSection(sectionId)
    editor.moveSection(newSection, 0)
    const oldPosition = document.positions[0]!.id
    const added = editor.addPosition(oldPosition)
    editor.editPosition(added, { short_text: 'Second', quantity: '2.50', unit_price_cents: 199 })
    editor.movePosition(added, 0)
    const saved = JSON.stringify(editor.document)
    editor.undo(); editor.redo()
    expect(JSON.stringify(JSON.parse(JSON.stringify(editor.document)))).toBe(saved)
    expect(editor.document.sections.map(section => section.id)).toEqual([newSection, sectionId])
    expect(editor.document.positions.map(position => position.id)).toEqual([added, oldPosition])
    const ids = editor.document.sections.flatMap(section => [section.id, ...section.nodes.map(node => node.id)])
    expect(new Set(ids).size).toBe(ids.length)
    expect(prose.markerLabels(nodes, 1)).toHaveLength(6)
    expect(prose.markerLabels(nodes, 1)[5]).toBe('1.1.1.1.1.1')
    expect(editor.document.sections[1]!.nodes[0]).toMatchObject({ id: firstId,
      marks: [{ start: 1, end: 3, bold: true, italic: true }], marker_x_mm: '-1.5', marker_y_mm: '0.5', text_start_mm: '2.0' })
    expect(editor.document.net_total_cents).toBe(629)
  })

  it('numbers restart, explicit start and continuation across prose with section binding', () => {
    const ids = Array.from({ length: 9 }, (_, i) => `line-${i}`)
    let nodes: TextNode[] = ids.map((id, i) => ({ id, kind: i === 3 ? 'paragraph' : 'item', text: `Line ${i}`,
      ...(i === 3 ? {} : { marker: 'decimal' as const, numbering: 'outline' as const, depth: i === 2 || i === 4 ? 1 : 0 }) }))
    nodes = prose.setNumbering(nodes, [ids[0]!], { mode: 'start', start: 3 })
    nodes = prose.setNumbering(nodes, [ids[2]!], { mode: 'continue' })
    nodes = prose.setNumbering(nodes, [ids[4]!], { mode: 'continue' })
    nodes = prose.setNumbering(nodes, [ids[5]!], { mode: 'restart' })
    nodes = prose.setNumbering(nodes, [ids[5]!], { mode: 'section' })
    nodes = prose.setNumbering(nodes, [ids[6]!], { mode: 'continue', sectionBound: true })
    nodes = prose.setNumbering(nodes, [ids[7]!], { mode: 'start', start: 7 })
    nodes = prose.setNumbering(nodes, [ids[8]!], { mode: 'continue' })
    expect(prose.markerLabels(nodes, 2)).toEqual(['3', '4', '4.1', '', '4.2', '2.1', '2.2', '7', '8'])
  })

  it('sends CAS and mutation identity, normalizes legacy measurements without changing money or the source', async () => {
    const calls: { path: string; init?: RequestInit }[] = []
    const document = fixture()
    Object.assign(document.sections[0]!.nodes[0]!, { kind: 'item', marker: 'decimal', marker_x_mm: 3, marker_y_mm: -1, text_start_mm: 1.5 })
    document.positions = [position('1.25', 105)]
    const original = JSON.stringify(document)
    const client = sourceModule<typeof import('../src/lib/quotes/api')>('lib/quotes/api.ts', {
      '../api': { APIError, api: async (path: string, init?: RequestInit) => {
        calls.push({ path, init })
        return new Response(JSON.stringify(init ? { acknowledged_revision: 4 } : { draft_revision: 3, document }))
      } },
    })
    const draft = await client.getDraft('quote/1')
    const saved = await client.saveDraft('quote/1', draft.draft_revision, draft.document, 'session-1', 'mutation-1')
    expect(saved.acknowledged_revision).toBe(4)
    expect(calls.map(call => call.path)).toEqual(['/quotes/quote%2F1/draft', '/quotes/quote%2F1/draft'])
    expect(calls[1]!.init).toMatchObject({ method: 'PATCH', headers: { 'If-Match': '"qd-3"' } })
    const body = JSON.parse(calls[1]!.init!.body as string)
    expect(body).toMatchObject({ client_session_id: 'session-1', mutation_id: 'mutation-1', writer_version: 2 })
    expect(body.document.sections[0].nodes[0]).toMatchObject({ marker_x_mm: '3', marker_y_mm: '-1', text_start_mm: '1.5' })
    expect(body.document.positions[0].unit_price_cents).toBe(105)
    expect(JSON.stringify(document)).toBe(original)
    await expect(client.saveDraft('quote/1', 0, document, 'session-1', 'mutation-2')).rejects.toThrow('Invalid draft revision')
    expect(calls).toHaveLength(2)
  })
})
