// SPDX-License-Identifier: AGPL-3.0-only
import { expect, it, vi } from 'vitest'
import { reactive } from 'vue'
import { outcomeLine, type OutcomeEvent } from '../src/lib/ticketOutcomes'
import { deferred, flush, setupSource } from './record-source'
import { flatten, mountView, settle, textOf } from './webcore-view-harness'

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
  expect(outcomeLine(event('ticket_done', { to_state: 'done', elapsed_seconds: 90 })).detail).toBe('1 minute 30 seconds')
  expect(outcomeLine(event('ticket_done', { to_state: 'done', elapsed_seconds: 0 })).detail).toBe('0 seconds')
  expect(outcomeLine(event('ticket_done', { to_state: 'done' })).detail).toBe('')
  const released = outcomeLine(event('released', { version: '260929120000.0.0', release_node_id: '8f000000-0000-4000-8000-000000000099' }, { release_title: 'September release' }))
  expect(released.title).toBe('Released in September release')
  expect(released.detail).toBe('260929120000.0.0')
  expect(released.full).not.toContain('8f000000')
  expect(outcomeLine(event('released', {})).title).toBe('Released')
  const versioned = outcomeLine(event('fix_round', { round: 1 }, { rules_version: 'draft-3' }))
  expect(versioned.detail).toBe('rules draft-3')
  expect(versioned.full).toBe('Fix round 1. rules draft-3')
})

it('P03: optional outcomes render recorded evidence and full long text, with honest empty/error states', async () => {
  const summary = 'One naming mismatch in the release note; the complete explanation stays available. '.repeat(8)
  for (const result of ['empty', 'recorded', 'error']) {
    const loadTicketOutcomes = vi.fn(async () => {
      if (result === 'error') throw new Error('Outcome history unavailable')
      return result === 'empty' ? [] : [
        event('review_verdict', { verdict: 'pass', summary }),
        event('ci_result', { result: 'fail', name: 'web' }),
        event('released', { version: '260929120000.0.0', release_node_id: 'raw-release-id' }, { release_title: 'September release' }),
      ]
    })
    const view = mountView('../src/components/work/TicketOutcomes.vue', {
      '../../lib/ticketOutcomes': { loadTicketOutcomes, outcomeLine },
      '../../lib/work': { relativeTime: () => 'recorded time', absoluteTime: (value: string) => value },
    }, { nodeId: 'ticket-a' })
    try {
      await settle()
      expect(loadTicketOutcomes).toHaveBeenCalledExactlyOnceWith('ticket-a', expect.any(AbortSignal))
      const sections = flatten(view.root).filter(node => node.props['aria-label'] === 'Outcomes')
      expect(sections).toHaveLength(result === 'empty' ? 0 : 1)
      if (result === 'recorded') {
        expect(textOf(view.root)).toContain('Review passed')
        expect(textOf(view.root)).toContain('CI failed')
        expect(textOf(view.root)).toContain('Released in September release')
        expect(textOf(view.root)).not.toContain('raw-release-id')
        expect(flatten(view.root).find(node => node.props.class === 'detail')?.props.title).toBe(`Review passed. ${summary.trim()}`)
      }
      if (result === 'error') {
        expect(view.find(node => node.props.role === 'alert')).toBeDefined()
        expect(textOf(view.root)).toContain('Outcome history unavailable')
        expect(view.find(node => node.tag === 'button')?.text).toBe('Try again')
      }
    } finally { view.app.unmount() }
  }
})

it('P03: a late outcome read or failure for A cannot replace B', async () => {
  for (const fails of [false, true]) {
    const pending = deferred<OutcomeEvent[]>()
    const loadTicketOutcomes = vi.fn().mockReturnValueOnce(pending.promise).mockResolvedValueOnce([event('ci_result', { result: 'pass', name: 'B only' })])
    const props = reactive({ nodeId: 'A' })
    const component = setupSource('components/work/TicketOutcomes.vue', props, {
      '../../lib/ticketOutcomes': { loadTicketOutcomes, outcomeLine }, '../../lib/work': {},
    })
    try {
      expect(loadTicketOutcomes).toHaveBeenCalledWith('A', expect.any(AbortSignal))
      props.nodeId = 'B'; await flush()
      expect(loadTicketOutcomes).toHaveBeenCalledTimes(2)
      expect(loadTicketOutcomes.mock.calls[0][1].aborted).toBe(true)
      if (fails) pending.reject(new Error('late A failure'))
      else pending.resolve([event('ci_result', { result: 'fail', name: 'A only' })])
      await flush()
      expect(component.state.rows.value.map((row: { detail: string }) => row.detail)).toEqual(['B only'])
      expect(component.state.error.value).toBe('')
      expect(component.state.loading.value).toBe(false)
    } finally { component.stop() }
  }
})
