// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { decimalString, formatTokenCount, formatUsd, formatWorkDuration, integerString, parseTicketAgentWork } from '../src/lib/ticketAgentWork.ts'

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
