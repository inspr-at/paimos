// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it, vi } from 'vitest'
import * as Vue from 'vue'
import { renderToString } from '@vue/server-renderer'
import { draftFor, type DeskItem, type DeskOutcomeAvailability } from '../src/lib/decisionDesk'
import DecisionDeskMemo from '../src/components/agents/DecisionDeskMemo.vue'

afterEach(() => vi.unstubAllGlobals())

function outcomes(once: string): DeskOutcomeAvailability[] {
  return [
    { outcome: 'once', available: !once, why: once },
    { outcome: 'always', available: true, why: '' },
    { outcome: 'requirement', available: false, why: 'Requirement needs a linked editable ticket.' },
    { outcome: 'doctrine', available: false, why: 'Doctrine needs an authorized source, rule and exact base mapping.', mapping_present: false },
  ]
}
function question(onceWhy = ''): DeskItem {
  return {
    id: 'q:1', kind: 'question', projectId: 'p', projectName: 'Aeon', projectKey: 'AEON', title: 'Which doctrine?',
    context: '', findings: '', meanwhile: 'parked', destination: 'ticket comment', why: '', suggestion: 'Suggested by the agent.',
    choices: [{ id: '1', title: 'Ship', description: 'Now', answer: 'Ship it.' }], outcome: 'doctrine', revision: 1, createdAt: '', held: true, decided: false,
    outcomes: outcomes(onceWhy),
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

it('preselects Once for an unavailable suggestion and disables Decide with the reason', async () => {
  const open = question()
  expect(draftFor(open).outcome).toBe('once')
  const html = await htmlFor(open)
  expect(html).toContain('<strong class="stamp-once large-stamp"')
  expect(tag(html, 'stamp-once')).toContain('aria-pressed="true"')
  expect(tag(html, 'stamp-once')).not.toContain('disabled')
  expect(tag(html, 'stamp-doctrine')).toContain('disabled')
  expect(tag(html, 'stamp-doctrine')).toContain('aria-pressed="false"')
  const status = tag(html, 'desk-status')
  expect(status).toContain('title="Doctrine needs an authorized source, rule and exact base mapping."')
  expect(html.slice(html.indexOf(status), html.indexOf(status) + status.length + 80)).toContain('Suggested: Doctrine · not available for this question')
  expect(html).not.toContain('data-testid="desk-decide-reason"')
  expect(tag(html, 'desk-decide')).not.toContain('disabled')

  const blocked = await htmlFor(question('A signed-in person chooses this outcome.'))
  const decide = tag(blocked, 'desk-decide')
  expect(decide).toContain('disabled')
  expect(decide).toContain('is-blocked')
  expect(decide).toContain('title="A signed-in person chooses this outcome."')
  const reason = tag(blocked, 'desk-decide-reason')
  expect(reason).toContain('title="A signed-in person chooses this outcome."')
  expect(blocked.slice(blocked.indexOf(reason), blocked.indexOf(reason) + reason.length + 80)).toContain('A signed-in person chooses this outcome.')
  expect(blocked).toContain('>A signed-in person chooses this outcome.</div>')
})
