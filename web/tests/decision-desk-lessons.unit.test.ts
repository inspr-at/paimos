// SPDX-License-Identifier: AGPL-3.0-only
// c4415f38 named the Changelog and the Knowledge link with the text chevron ›.
import { readFileSync } from 'node:fs'
import { expect, it, vi } from 'vitest'
import * as Vue from 'vue'
import { renderToString } from '@vue/server-renderer'
import { lessonItem } from '../src/lib/decisionDeskApi'
import type { DeskItem, DeskProjectionItem } from '../src/lib/decisionDesk'
import type { MethodLearning } from '../src/lib/knowledge'
import DecisionDeskMemo from '../src/components/agents/DecisionDeskMemo.vue'

const CHEVRON = '›'
const row: DeskProjectionItem = {
  id: 'learn-1', kind: 'lesson', project_id: 'p-aeon', revision: 700, title: 'Write the note in the same turn.',
  created_at: '2026-10-14T00:00:00Z', held: false, href: '/decision-desk?item=l:learn-1', source: '/api/knowledge/learnings/learn-1',
}
function learning(title: string, rule = false): MethodLearning {
  return {
    id: 'learn-1', source: 'ticket', node_id: 'n1', key: 'AEON-1', title: 'Recovery failed during release', text: 'The recovery check was missing.',
    at: '2026-10-14T00:00:00Z', author: null, href: '',
    recommendation: {
      decision: 'accept', knowledge_id: rule ? 'rule-set' : 'k-aeon-deploy', knowledge_title: title,
      lesson: 'Write the note in the same turn.', by: null, at: '2026-10-14T00:00:00Z', event_id: 700,
      stale: false, target_missing: false, source_summary: 'The check was missing.', line: 'The recovery check was missing.',
      ...(rule ? { rule_layer_id: 'rule-layer', rule_text: 'Write the note in the same turn.' } : {}),
    },
  }
}
function stubBrowser() {
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} unobserve() {} })
  vi.stubGlobal('HTMLElement', class {})
  vi.stubGlobal('navigator', { platform: 'MacIntel', userAgent: 'Mac' })
  vi.stubGlobal('document', { activeElement: null, fonts: undefined })
  vi.stubGlobal('window', {
    innerWidth: 1400,
    matchMedia: () => ({ matches: false, addEventListener() {}, removeEventListener() {} }),
    addEventListener() {}, removeEventListener() {},
  })
}
async function memoHtml(item: DeskItem) {
  stubBrowser()
  const app = Vue.createSSRApp({ render: () => Vue.h(DecisionDeskMemo, {
    items: [item], round: [item.id], start: item.id, arrivalsCount: 0, allowed: () => true, decide: vi.fn(async () => item),
  }) })
  app.component('RouterLink', { props: ['to'], setup: (_, { slots }) => () => slots.default?.() })
  return renderToString(app)
}

it('names a changelog lesson with the entry title and no text chevron', async () => {
  const item = lessonItem(row, learning('Ship an Aeon release'), 'Paimos Aeon')
  expect(item.destination).toBe('Ship an Aeon release · Changelog')
  expect(item.destination).not.toContain(CHEVRON)
  expect(item.choices.map(choice => choice.description)).toEqual(['Add the prepared line to the changelog.', 'Nothing is written.'])
  const html = await memoHtml(item)
  expect(html).toContain('Adds this line to Ship an Aeon release · Changelog')
  expect(html).toContain('Where the answer goes')
  expect(html).not.toContain(CHEVRON)
})

it('keeps a rule-draft lesson on the draft and off the changelog chevron', () => {
  const item = lessonItem(row, learning('Recovery rules', true), 'Paimos Aeon')
  expect(item.destination).toBe('Rule draft Recovery rules')
  expect(item.destination).not.toContain(CHEVRON)
  expect(item.choices[0]?.description).toBe('Add the prepared line to the rule draft.')
})

it('draws the Knowledge lesson link with an SVG chevron', () => {
  const source = readFileSync(new URL('../src/components/knowledge/KnowledgeTab.vue', import.meta.url), 'utf8')
  expect(source).toContain('lessons wait on the Decision Desk<AppIcon name="chevron-right" :size="12" class="lesson-go" />')
  expect(source).not.toContain(CHEVRON)
})
