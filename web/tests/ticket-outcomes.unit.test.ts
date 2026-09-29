// SPDX-License-Identifier: AGPL-3.0-only
import { expect, it } from 'vitest'
import { outcomeLine, type OutcomeEvent } from '../src/lib/ticketOutcomes'

function event(kind: string, payload: Record<string, unknown>, extra: Partial<OutcomeEvent> = {}): OutcomeEvent {
  return {
    id: kind, kind, ticket_key: 'AEON-286', rules_version: null, release_title: null,
    payload, recorded_at: '2026-09-29T08:00:00Z', ...extra,
  }
}

it('describes each outcome without raw ids', () => {
  expect(outcomeLine(event('review_verdict', { verdict: 'pass', route: 'backend', reviewer_model: 'codex', round: 2, findings: 0, summary: 'Clean' })).detail)
    .toBe('backend · codex · round 2 · 0 findings · Clean')
  expect(outcomeLine(event('review_verdict', { verdict: 'ok', author_family: 'grok', blocking_count: 2 })).detail)
    .toBe('grok · 2 blocking')
  expect(outcomeLine(event('review_verdict', { verdict: 'ok' })).title).toBe('Review ok')
  expect(outcomeLine(event('review_verdict', { verdict: 'changes' })).title).toBe('Changes requested')
  expect(outcomeLine(event('review_verdict', { verdict: 'fail' })).title).toBe('Review failed')
  expect(outcomeLine(event('fix_round', { round: 2, summary: 'Renamed the field' }))).toMatchObject({ title: 'Fix round 2', detail: 'Renamed the field' })
  expect(outcomeLine(event('ci_result', { result: 'fail', name: 'web' }))).toMatchObject({ title: 'CI failed', detail: 'web' })
  expect(outcomeLine(event('ci_result', { result: 'fail', repo: 'inspr-at/paimos', number: 286, name: 'web' })).detail)
    .toBe('inspr-at/paimos #286 · web')
  expect(outcomeLine(event('ci_result', { result: 'pass' })).title).toBe('CI passed')
  expect(outcomeLine(event('revert', { summary: 'Restored the gate', target: 'main' })).detail).toBe('main · Restored the gate')
  expect(outcomeLine(event('ticket_done', { from_state: 'open', to_state: 'accepted' })).title).toBe('Marked accepted')
  const included = outcomeLine(event('release_included', { release_node_id: '8f000000-0000-4000-8000-000000000099' }, { release_title: 'September release' }))
  expect(included.title).toBe('Included in September release')
  expect(included.full).not.toContain('8f000000')
  expect(outcomeLine(event('release_included', {})).title).toBe('Included in a release')
  const versioned = outcomeLine(event('fix_round', { round: 1 }, { rules_version: 'draft-3' }))
  expect(versioned.detail).toBe('rules draft-3')
  expect(versioned.full).toBe('Fix round 1. rules draft-3')
})
