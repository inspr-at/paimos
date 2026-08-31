import { createApp, defineComponent, h } from 'vue'
import { afterEach, describe, expect, it } from 'vitest'

import Paimos6KnowledgeCompact from './Paimos6KnowledgeCompact.vue'
import type { StructuredKnowledgeSnapshot } from '@/v6/structuredKnowledge'

const compactId = '11111111-1111-4111-8111-111111111111'
const cleanups: Array<() => void> = []

afterEach(() => {
  while (cleanups.length) cleanups.pop()?.()
})

function mountCompact(state: 'loading' | 'ready' | 'empty' | 'unavailable', value: StructuredKnowledgeSnapshot | null) {
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp(defineComponent({
    setup: () => () => h(Paimos6KnowledgeCompact, { state, snapshot: value }),
  }))
  app.mount(root)
  cleanups.push(() => { app.unmount(); root.remove() })
  return root
}

const snapshot: StructuredKnowledgeSnapshot = {
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
    purpose: 'Keep links authoritative.',
    short_body: 'One canonical row.',
    authored_product_session_id: compactId,
    revision: 1,
    links: [{
      link_id: 1,
      relation: 'parent',
      target_issue_id: 8,
      target_knowledge: true,
      target_type: 'memory',
      target_slug: 'foundation',
      target_title: 'Foundation',
    }],
    validation: { flags: [], likely_duplicate_ids: [], body_bytes: 18, short_body_limit_bytes: 1200 },
    created_at: '2026-08-31T10:00:00Z',
    updated_at: '2026-08-31T10:00:00Z',
  }],
  legacy: [{
    knowledge_id: 9,
    type: 'runbook',
    slug: 'legacy',
    title: 'Legacy runbook',
    body_bytes: 9000,
    validation: { flags: ['essay', 'legacy_unstructured'], likely_duplicate_ids: [], body_bytes: 9000, short_body_limit_bytes: 1200 },
    updated_at: '2026-08-31T09:00:00Z',
  }],
  proposals: [{
    proposal_id: '22222222-2222-4222-8222-222222222222',
    source_kind: 'remember',
    product_session_id: compactId,
    type: 'memory',
    slug: 'candidate',
    title: 'Candidate',
    purpose: 'Review first.',
    candidate_body: 'Amy: note\nUser: okay',
    state: 'proposed',
    promoted_knowledge_id: null,
    validation: { flags: ['chat_note_prose'], likely_duplicate_ids: [], body_bytes: 20, short_body_limit_bytes: 1200 },
    created_at: '2026-08-31T10:01:00Z',
    updated_at: '2026-08-31T10:01:00Z',
  }],
}

describe('Paimos6KnowledgeCompact', () => {
  it('names the Compact product session and keeps proposals and legacy rows honest', () => {
    const mounted = mountCompact('ready', snapshot)
    expect(mounted.textContent).toContain('Product session · 11111111')
    expect(mounted.textContent).toContain('Single graph truth')
    expect(mounted.textContent).toContain('remember proposal')
    expect(mounted.textContent).toContain('Explicitly unstructured')
    expect(mounted.textContent).not.toContain('Amy: note')
  })

  it('clears stale facts behind an unavailable state', () => {
    const mounted = mountCompact('unavailable', null)
    expect(mounted.querySelector('[role="alert"]')?.textContent).toContain('No previous project data is retained')
    expect(mounted.textContent).not.toContain('Single graph truth')
  })
})
