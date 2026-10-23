// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { decimalString, formatTokenCount, formatUsd, formatWorkDuration, integerString, parseTicketAgentWork } from '../src/lib/ticketAgentWork.ts'
import * as agentWork from '../src/lib/ticketAgentWork.ts'
import { flatten, mountView, settle, textOf } from './webcore-view-harness.ts'

test('estimated cost stays an exact decimal string and never a float', () => {
  assert.equal(formatUsd('1.500000000000'), '$1.50')
  assert.equal(formatUsd('1.000000000000'), '$1')
  assert.equal(formatUsd('0.012345000000'), '$0.012345')
  assert.equal(formatUsd('0.005000000000'), '$0.005')
  assert.equal(formatUsd('0.000000000000'), '$0')
  assert.equal(formatUsd('1234567.500000000000'), '$1,234,567.50')
  assert.equal(formatUsd(null), null)
  assert.equal(formatUsd('01'), null)
  assert.equal(formatUsd('1.5e1'), null)
  assert.equal(formatUsd('-1.00'), null)
  assert.equal(decimalString(1.5), null)
  assert.equal(integerString(1.5), null)
  assert.equal(formatTokenCount('1200'), '1,200')
  assert.equal(formatTokenCount('0'), '0')
  assert.equal(formatWorkDuration(150, 'known'), '2m 30s')
  assert.equal(formatWorkDuration(90, 'ongoing'), '1m 30s so far')
  assert.equal(formatWorkDuration(null, 'unknown'), 'Time unknown')
})

test('a numeric cost in the payload is unknown, not displayed', () => {
  const parsed = parseTicketAgentWork({
    node_id: 'n-1', kind: 'ticket', currency: 'USD', usage_available: true, includes_descendants: false,
    scope_truncated: false, list_truncated: false,
    sessions: [{
      id: 's-1', ticket_node_id: 'n-1', ticket_key: 'TW-1', ticket_title: 'Work', harness: 'codex',
      label: null, model: 'gpt-test', model_state: 'known', effort: null, effort_state: 'missing',
      phase: 'stopped', started_at: '2020-01-01T00:00:00Z', ended_at: '2020-01-01T00:01:00Z',
      duration_seconds: 60, duration_state: 'known', usage_reported: true, models: [{
        model: 'gpt-test', input_tokens: 12, output_tokens: '4', cached_input_tokens: null,
        tokens_state: 'known', cached_state: 'unknown', estimated_cost_usd: 1.5, cost_state: 'estimated',
        provisional: false, price_version: 3, billing_mode: 'api', subscription_label: null,
      }], models_truncated: false,
      input_tokens: 12, output_tokens: '4', cached_input_tokens: null, tokens_state: 'known', cached_state: 'unknown',
      estimated_cost_usd: 1.5, cost_state: 'estimated', unknown_token_models: 0, unknown_cost_models: 0,
    }],
    totals: {
      session_count: 1, input_tokens: 12, output_tokens: '4', cached_input_tokens: null,
      tokens_state: 'known', cached_state: 'unknown', estimated_cost_usd: 1.5, cost_state: 'estimated',
      currency: 'USD', duration_seconds: 60, duration_state: 'known', unknown_token_sessions: 0, unknown_cost_sessions: 0,
      unknown_token_models: 0, unknown_cost_models: 0,
    },
  })
  assert.equal(parsed.sessions[0].estimated_cost_usd, null)
  assert.equal(parsed.sessions[0].cost_state, 'unknown')
  assert.equal(parsed.sessions[0].tokens_state, 'unknown')
  assert.equal(parsed.sessions[0].models[0].estimated_cost_usd, null)
  assert.equal(parsed.sessions[0].models[0].price_version, null)
  assert.equal(parsed.totals.estimated_cost_usd, null)
  assert.equal(parsed.totals.cost_state, 'unknown')
  assert.equal(parsed.totals.tokens_state, 'unknown')
})

test('two model rows stay exact when summed text is already decimal', () => {
  const parsed = parseTicketAgentWork({
    node_id: 'n-1', kind: 'epic', currency: 'USD', usage_available: true, includes_descendants: true,
    scope_truncated: false, list_truncated: false,
    sessions: [{
      id: 's-1', ticket_node_id: 'n-1', ticket_key: 'TW-1', ticket_title: 'Work', harness: 'codex',
      label: 'Harbor', model: 'gpt-test', model_state: 'known', effort: 'high', effort_state: 'known',
      phase: 'stopped', started_at: '2020-01-01T00:00:00Z', ended_at: '2020-01-01T00:02:30Z',
      duration_seconds: 150, duration_state: 'known', usage_reported: true,
      models: [
        { model: 'gpt-test', input_tokens: '1000', output_tokens: '200', cached_input_tokens: '40', tokens_state: 'known', cached_state: 'known', estimated_cost_usd: '1.000000000000', cost_state: 'estimated', provisional: false, price_version: '1', billing_mode: 'api', subscription_label: null },
        { model: 'gpt-test-mini', input_tokens: '5', output_tokens: '1', cached_input_tokens: '0', tokens_state: 'known', cached_state: 'known', estimated_cost_usd: '0.250000000000', cost_state: 'estimated', provisional: false, price_version: '1', billing_mode: 'api', subscription_label: null },
      ],
      models_truncated: false, input_tokens: '1005', output_tokens: '201', cached_input_tokens: '40',
      tokens_state: 'known', cached_state: 'known', estimated_cost_usd: '1.250000000000', cost_state: 'estimated',
      unknown_token_models: 0, unknown_cost_models: 0,
    }],
    totals: {
      session_count: 1, input_tokens: '1005', output_tokens: '201', cached_input_tokens: '40',
      tokens_state: 'known', cached_state: 'known', estimated_cost_usd: '1.250000000000', cost_state: 'estimated',
      currency: 'USD', duration_seconds: 150, duration_state: 'known', unknown_token_sessions: 0, unknown_cost_sessions: 0,
      unknown_token_models: 0, unknown_cost_models: 0,
    },
  })
  assert.equal(parsed.sessions[0].models.length, 2)
  assert.equal(parsed.sessions[0].estimated_cost_usd, '1.250000000000')
  assert.equal(parsed.totals.estimated_cost_usd, '1.250000000000')
  assert.equal(formatUsd(parsed.totals.estimated_cost_usd), '$1.25')
})

test('P03: agent work renders optional, capped, partial and withheld usage without invented zeroes', async () => {
  for (const variant of ['empty', 'capped', 'unknown', 'partial', 'forbidden']) {
    const report = parseTicketAgentWork({
      node_id: 'parent', kind: 'epic', currency: 'USD', usage_available: variant !== 'unknown', includes_descendants: true,
      scope_truncated: variant === 'capped', list_truncated: variant === 'partial',
      sessions: ['unknown', 'partial'].includes(variant) ? [{
        id: 'session-1', ticket_node_id: 'leaf', ticket_key: 'P03-1', ticket_title: 'Leaf', harness: 'codex', label: 'Harbor worker',
        model: null, model_state: 'missing', effort: null, effort_state: 'missing', phase: 'working', started_at: '', ended_at: null,
        duration_seconds: 45, duration_state: 'ongoing', usage_reported: false, models: [], models_truncated: false,
        input_tokens: null, output_tokens: null, cached_input_tokens: null, tokens_state: 'unknown', cached_state: 'unknown',
        estimated_cost_usd: null, cost_state: 'unknown', unknown_token_models: 0, unknown_cost_models: 0,
      }] : [],
      totals: { session_count: 1, input_tokens: '1210', output_tokens: '82', cached_input_tokens: '15', tokens_state: 'partial', cached_state: 'partial',
        estimated_cost_usd: '1.750000000000', cost_state: 'partial', currency: 'USD', duration_seconds: 195, duration_state: 'partial',
        unknown_token_sessions: 1, unknown_cost_sessions: 1, unknown_token_models: 0, unknown_cost_models: 0 },
    })
    const reads: string[] = []
    const view = mountView('../src/components/work/TicketAgentWork.vue', {
      '../../lib/ticketAgentWork': { ...agentWork, loadTicketAgentWork: async (id: string) => { reads.push(id); if (variant === 'forbidden') throw new Error('Agent work is not permitted'); return report } },
      '../../lib/work': { kindLabel: () => 'Epic' }, '../../lib/workVocabulary': { workNoun: (value: string) => value.toLowerCase() },
      '../../stores/agents': { useAgents: () => ({ loaded: true, sessionById: () => undefined }) },
      '../../lib/deliveryRating': { loadNodeRatings: async () => [] },
    }, { nodeId: 'parent', kind: 'epic' })
    try {
      await settle()
      assert.deepEqual(reads, ['parent'])
      assert.equal(flatten(view.root).filter(node => node.props['aria-label'] === 'Agent work').length, variant === 'empty' ? 0 : 1)
      const text = textOf(view.root)
      if (variant === 'capped') {
        assert.match(text, /No agent sessions are recorded in the included items/)
        assert.match(text, /Some items under this epic were left out of the query/)
      }
      if (variant === 'partial') {
        assert.match(text, /Totals count reported figures only/)
        assert.match(text, /Showing the 1 most recent sessions/)
        assert.match(text, /\$1.75, incomplete/)
        assert.match(text, /1,210 in/)
      }
      if (variant === 'unknown' || variant === 'partial') {
        assert.match(text, /45s so far/)
        const row = view.find(node => node.tag === 'li')
        assert.doesNotMatch(textOf(row), /unknown|\$0|0 in|0 out/)
        assert.equal(view.find(node => node.tag === 'a').props.to, '/agents/session-1')
      }
      if (variant === 'unknown') assert.doesNotMatch(text, /\$1.75|1,210 in/)
      if (variant === 'forbidden') {
        assert.match(text, /Agent work is not permitted/)
        assert.equal(view.find(node => node.props.role === 'alert').tag, 'p')
        assert.equal(view.find(node => node.tag === 'button').text, 'Try again')
      }
    } finally { view.app.unmount() }
  }
})
