import { describe, expect, it } from 'vitest'

import { parseStructuredKnowledgeSnapshot, StructuredKnowledgeContractError } from './structuredKnowledge'

const compactId = '11111111-1111-4111-8111-111111111111'

function validation(body: string, flags: string[] = [], duplicateIds: number[] = []) {
  return {
    flags,
    likely_duplicate_ids: duplicateIds,
    body_bytes: new TextEncoder().encode(body).length,
    short_body_limit_bytes: 1200,
  }
}

function snapshot() {
  return {
    schema_version: 1,
    project_id: 42,
    short_body_limit_bytes: 1200,
    compact_product_session_id: compactId,
    entries: [{
      knowledge_id: 7,
      level: 'project',
      type: 'memory',
      slug: 'single-graph-truth',
      title: 'Single graph truth',
      purpose: 'Keep every link mutation authoritative.',
      short_body: 'Typed links live in one canonical table.',
      authored_product_session_id: compactId,
      revision: 1,
      links: [{
        link_id: 3,
        relation: 'about',
        target_issue_id: 55,
        target_knowledge: false,
        target_type: 'ticket',
        target_slug: '',
        target_title: 'Structured knowledge',
      }],
      validation: validation('Typed links live in one canonical table.'),
      created_at: '2026-08-31T10:00:00Z',
      updated_at: '2026-08-31T10:00:00Z',
    }],
    legacy: [{
      knowledge_id: 8,
      type: 'runbook',
      slug: 'old-runbook',
      title: 'Old runbook',
      body_bytes: 14,
      validation: {
        flags: ['legacy_unstructured'],
        likely_duplicate_ids: [],
        body_bytes: 14,
        short_body_limit_bytes: 1200,
      },
      updated_at: '2026-08-31T09:00:00Z',
    }],
    proposals: [{
      proposal_id: '22222222-2222-4222-8222-222222222222',
      source_kind: 'remember',
      product_session_id: compactId,
      type: 'memory',
      slug: 'candidate',
      title: 'Candidate',
      purpose: 'Review this before durability.',
      candidate_body: 'Amy: remember this\nUser: maybe',
      state: 'proposed',
      promoted_knowledge_id: null,
      validation: validation('Amy: remember this\nUser: maybe', ['chat_note_prose']),
      created_at: '2026-08-31T10:01:00Z',
      updated_at: '2026-08-31T10:01:00Z',
    }],
  }
}

describe('structured knowledge v1 contract', () => {
  it('accepts compact sessions, explicit legacy rows, canonical links, and remember proposals', () => {
    const parsed = parseStructuredKnowledgeSnapshot(snapshot(), 42)
    expect(parsed.compact_product_session_id).toBe(compactId)
    expect(parsed.entries[0].links[0].relation).toBe('about')
    expect(parsed.legacy[0].validation.flags).toContain('legacy_unstructured')
    expect(parsed.proposals[0].state).toBe('proposed')
  })

  it('accepts a project with only safely identified legacy rows before Compact is bound', () => {
    const raw = snapshot()
    raw.compact_product_session_id = null as unknown as string
    raw.entries = []
    raw.proposals = []
    expect(parseStructuredKnowledgeSnapshot(raw, 42).legacy).toHaveLength(1)
  })

  it.each([
    ['foreign project', (raw: ReturnType<typeof snapshot>) => { raw.project_id = 43 }],
    ['unknown root property', (raw: ReturnType<typeof snapshot>) => { Object.assign(raw, { instance_id: 'ppm' }) }],
    ['body larger than declared limit', (raw: ReturnType<typeof snapshot>) => { raw.entries[0].short_body = 'x'.repeat(1201); raw.entries[0].validation = validation('x'.repeat(1201), ['essay']) }],
    ['widened product body contract', (raw: ReturnType<typeof snapshot>) => {
      raw.short_body_limit_bytes = 1201
      raw.entries[0].validation.short_body_limit_bytes = 1201
      raw.legacy[0].validation.short_body_limit_bytes = 1201
      raw.proposals[0].validation.short_body_limit_bytes = 1201
    }],
    ['proposal above the locked candidate cap', (raw: ReturnType<typeof snapshot>) => {
      const body = 'x'.repeat(65537)
      raw.proposals[0].candidate_body = body
      raw.proposals[0].validation = validation(body, ['essay'])
    }],
    ['legacy body smuggling', (raw: ReturnType<typeof snapshot>) => { Object.assign(raw.legacy[0], { body: 'unbounded prose' }) }],
    ['metadata graph smuggling', (raw: ReturnType<typeof snapshot>) => { Object.assign(raw.entries[0], { metadata: { depends_on: ['x'] } }) }],
    ['multiline durable title', (raw: ReturnType<typeof snapshot>) => { raw.entries[0].title = 'Two\nlines' }],
    ['non-project inherited row', (raw: ReturnType<typeof snapshot>) => { raw.entries[0].level = 'instance' }],
    ['proposal made durable implicitly', (raw: ReturnType<typeof snapshot>) => { raw.proposals[0].state = 'promoted'; raw.proposals[0].promoted_knowledge_id = null }],
    ['proposal from another session', (raw: ReturnType<typeof snapshot>) => { raw.proposals[0].product_session_id = '33333333-3333-4333-8333-333333333333' }],
    ['duplicate identity', (raw: ReturnType<typeof snapshot>) => { raw.legacy[0].knowledge_id = 7 }],
  ])('rejects %s', (_name, mutate) => {
    const raw = snapshot()
    mutate(raw)
    expect(() => parseStructuredKnowledgeSnapshot(raw, 42)).toThrow(StructuredKnowledgeContractError)
  })
})
