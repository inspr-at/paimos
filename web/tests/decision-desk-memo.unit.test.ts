// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it, vi } from 'vitest'
import * as Vue from 'vue'
import { renderToString } from '@vue/server-renderer'
import { draftFor, type DeskItem, type DeskOutcomeAvailability } from '../src/lib/decisionDesk'
import DecisionDeskMemo from '../src/components/agents/DecisionDeskMemo.vue'

afterEach(() => vi.unstubAllGlobals())

function outcomes(unusable: string): DeskOutcomeAvailability[] {
  return [
    { outcome: 'once', available: !unusable, why: unusable },
    { outcome: 'always', available: !unusable, why: unusable },
    { outcome: 'requirement', available: false, why: unusable || 'Requirement needs a linked editable ticket.' },
    { outcome: 'doctrine', available: false, why: unusable || 'Doctrine needs an authorized source, rule and exact base mapping.', mapping_present: false },
  ]
}
function question(unusable = ''): DeskItem {
  return {
    id: 'q:1', kind: 'question', projectId: 'p', projectName: 'Aeon', projectKey: 'AEON', title: 'Which doctrine?',
    context: '', findings: '', meanwhile: 'parked', destination: 'ticket comment', why: '', suggestion: 'Suggested by the agent.',
    choices: [{ id: '1', title: 'Ship', description: 'Now', answer: 'Ship it.' }], outcome: 'doctrine', suggested: 'doctrine', suggestedBy: 'agent', revision: 1, createdAt: '', held: true, decided: false,
    outcomes: outcomes(unusable),
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
async function htmlFor(item: DeskItem) {
  stubBrowser()
  const app = Vue.createSSRApp({ render: () => Vue.h(DecisionDeskMemo, {
    items: [item], round: [item.id], start: item.id, arrivalsCount: 0, allowed: () => true, decide: vi.fn(async () => item),
  }) })
  app.component('RouterLink', { props: ['to'], setup: (_, { slots }) => () => slots.default?.() })
  return renderToString(app)
}
function tag(html: string, testId: string): string {
  const marker = `data-testid="${testId}"`
  const at = html.indexOf(marker)
  if (at < 0) throw new Error(`missing ${testId}`)
  return html.slice(html.lastIndexOf('<', at), html.indexOf('>', at) + 1)
}
const text = (html: string, testId: string) => { const open = tag(html, testId), at = html.indexOf(open) + open.length; return html.slice(at, html.indexOf('</', at)) }

// AEON-1343 + AEON-1339 risk: the memo starts on a stamp that cannot be used, so Decide does nothing.
it('starts an unavailable suggestion on the scope default and keeps Decide decidable, with the reason in plain words', async () => {
  const open = question()
  // No ticket: the scope default is Always (the approved design: suggestion, then scope default, then Once).
  expect(draftFor(open).outcome).toBe('always')
  const html = await htmlFor(open)
  expect(tag(html, 'stamp-always')).toContain('aria-checked="true"')
  expect(tag(html, 'stamp-always')).not.toContain('aria-disabled')
  expect(tag(html, 'stamp-doctrine')).toContain('aria-disabled="true"')
  expect(tag(html, 'stamp-doctrine')).toContain('aria-checked="false"')
  expect(tag(html, 'stamp-doctrine')).toContain('data-tip="Not available: the agent didn’t name the rule this answer would change, so there is nothing to propose a change to."')
  expect(text(html, 'desk-suggest-off')).toBe('Suggested: Doctrine · not available for this question')
  expect(tag(html, 'desk-suggest-off')).toContain('didn’t name the rule')
  expect(text(html, 'desk-stamp')).toBe('Always')
  expect(tag(html, 'desk-decide')).not.toContain('aria-disabled')
  expect(tag(html, 'desk-decide')).not.toContain(' disabled')
  // A round of one: no pager, and the primary reads Decide.
  expect(html).not.toContain('data-testid="desk-pager"')
  expect(html.slice(html.indexOf(tag(html, 'desk-decide')))).toMatch(/class="lbl"[^>]*>Decide</)
})

it('names a lasting restriction beside a muted Decide when no stamp can be used', async () => {
  const blocked = await htmlFor(question('A signed-in person chooses this outcome.'))
  const decide = tag(blocked, 'desk-decide')
  // aria-disabled, never disabled: a click still answers with the reason.
  expect(decide).toContain('aria-disabled="true"')
  expect(decide).not.toContain(' disabled=')
  expect(decide).toContain('aria-describedby="desk-status"')
  expect(text(blocked, 'desk-status')).toContain('A signed-in person chooses this outcome.')
  for (const outcome of ['once', 'always', 'requirement', 'doctrine']) expect(tag(blocked, `stamp-${outcome}`)).toContain('aria-disabled="true"')
})
