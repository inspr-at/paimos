// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { test } from 'vitest'
import type { ModelProfile } from '../src/lib/agents'
import type { SessionView } from '../src/stores/agents'
import { explicitOutcome, intendedResult, modelNames, modelProvider, sessionExecution, sessionEtaEligible, sessionSearchText } from '../src/components/agents/sessionRow'

function view(partial: Partial<SessionView> & { session?: Partial<SessionView['session']> } = {}): SessionView {
  const session = {
    id: 's1', harness: 'cursor', host: 'mba', model: null, reasoning_effort: null, account_label: null, brief: null,
    role: 'worker', heartbeat_at: new Date().toISOString(), activity: 'busy', phase: 'working',
    ...partial.session,
  } as SessionView['session']
  return {
    session, status: { group: 'working', tone: 'busy', label: 'Working', state: 'working' },
    name: 'hausv', harness: 'Cursor', account: '', model: '', projectKey: 'AEON', projectTitle: 'Aeon', ticket: null,
    ...partial, session,
  }
}

test('ETA guidance is limited to running sessions with a bound ticket in rows and panels', () => {
  const ticket = { id: 'n', key: 'AEON-443', title: 'Estimates and ETAs', href: '/p/AEON/AEON-443' }
  for (const role of ['worker', 'coordinator'] as const) {
    assert.equal(sessionEtaEligible(view({ session: { role } })), false)
    assert.equal(sessionEtaEligible(view({ ticket, session: { role } })), true)
    assert.equal(sessionEtaEligible(view({ ticket, session: { role, stopped_at: '2026-09-30T12:00:00Z' } })), false)
    assert.equal(sessionEtaEligible(view({ ticket, session: { role, archived_at: '2026-09-30T12:00:00Z' } })), false)
    assert.equal(sessionEtaEligible(view({ ticket, session: { role, phase: 'stopped' } })), false)
  }
  assert.equal(sessionEtaEligible(view({ session: { eta_ready_at: '2026-09-30T12:25:00Z', progress_pct: 0 } })), false)
  assert.equal(sessionEtaEligible(view({ ticket, session: { progress_pct: 0 } })), true)
})

test('intended result prefers an explicit phrase, then the bound ticket title, then the existing label', () => {
  const titled = view({ ticket: { id: 'n', key: 'AEON-211', title: 'Deploy approvals show the target server', href: '/p/AEON/AEON-211' }, session: { brief: 'AEON-211' } })
  assert.equal(intendedResult(titled), 'Deploy approvals show the target server')
  const phrase = view({ session: { brief: 'Release membership stays consistent' }, ticket: titled.ticket })
  assert.equal(explicitOutcome(phrase.session.brief), 'Release membership stays consistent')
  assert.equal(intendedResult(phrase), 'Release membership stays consistent')
  const keyOnly = view({ session: { brief: 'AEON-221' }, name: 'SC1 working' })
  assert.equal(intendedResult(keyOnly), 'AEON-221')
  // The name is the session's own label, never its host (AEON-1211: the host has its own column).
  const plain = view({ name: 'amy', session: { host: 'prod-1', activity_note: 'Quiet · heartbeat only' } as SessionView['session'] })
  assert.equal(intendedResult(plain), 'amy')
  assert.equal(intendedResult(plain).includes('heartbeat'), false)
})

test('execution names the reported model and effort, and omits what is not reported', () => {
  const known = view({ harness: 'Codex', session: { harness: 'codex', model: 'gpt-6-sol', reasoning_effort: 'xhigh', account_label: 'Codex Pro' } })
  const exec = sessionExecution(known)
  assert.equal(exec.modelLine, 'GPT-6 Sol · xhigh')
  assert.equal(exec.model, 'gpt-6-sol')
  assert.equal(exec.accountLine, 'Codex Pro')
  assert.equal(exec.provider, 'openai')
  const cursorGrok = view({ harness: 'Cursor', model: 'grok-4.7', session: { harness: 'cursor', model: 'grok-4.7', reasoning_effort: 'high' } })
  assert.equal(sessionExecution(cursorGrok).provider, 'xai')
  assert.equal(modelProvider('cursor-composer-2'), 'cursor')
  assert.equal(modelProvider('pi-anthropic-sonnet-high'), 'anthropic')
  assert.equal(modelProvider('integration-model'), 'unknown')
  assert.equal(modelProvider('codex-astra-xhigh'), 'unknown')
  const missing = view({ harness: 'Grok', model: 'claude-fable-high', account: 'Claude Max', session: { harness: 'grok' } })
  const fallback = sessionExecution(missing)
  assert.equal(fallback.modelLine, 'Fable · high')
  assert.equal(fallback.accountLine, 'Claude Max')
  assert.equal(fallback.provider, 'anthropic')
  const none = view({ harness: 'Grok', session: { harness: 'grok', heartbeat_at: new Date().toISOString() } })
  const empty = sessionExecution(none)
  assert.equal(empty.modelLine, '')
  assert.equal(empty.accountLine, 'Grok')
  assert.equal(empty.provider, 'unknown')
})

test('media and terminal use their execution label and never infer an AI vendor', () => {
  for (const [harness, label] of [['media', 'higgsfield/kling3_0'], ['terminal', 'ffmpeg']] as const) {
    const exec = sessionExecution(view({ model: 'claude-fable', account: 'Claude Max', session: { harness, generator: harness === 'media' ? label : null, command: harness === 'terminal' ? label : null, model: 'gpt-fixture', reasoning_effort: 'high' } }))
    assert.equal(exec.kind, harness)
    assert.equal(exec.modelLine, label)
    assert.equal(exec.provider, 'unknown')
    assert.equal(exec.effort, '')
    assert.equal(exec.account, '')
  }
})

test('heartbeat model changes refresh the same session row over a stale launch model', () => {
  const row = view({ harness: 'Claude', model: 'claude-sonnet', session: { harness: 'claude', model: 'claude-sonnet', reasoning_effort: 'low' } })
  assert.equal(sessionExecution(row).modelLine, 'Sonnet · low')
  const sessionID = row.session.id
  row.session = { ...row.session, model: 'claude-opus', reasoning_effort: 'high' }
  assert.equal(row.session.id, sessionID)
  assert.equal(sessionExecution(row).modelLine, 'Opus · high')
  assert.equal(sessionExecution(row).provider, 'anthropic')
})

// Risk (AEON-1211): the row shows a raw model id ("claude-opus-high", "gpt-6.1-sol") instead of the
// name Settings › Models gives it. Registry fixtures here are synthetic, never a production catalog.
test('execution uses the registry display name and level, and never a raw id', () => {
  const profile = (fields: Partial<ModelProfile>): ModelProfile => ({ id: 'p', slug: '', harness: 'claude', family: 'anthropic', model: '', effort: 'high', tier: 'standard', enabled: true, ...fields })
  const names = modelNames([
    profile({ id: 'p1', slug: 'claude-opus-high', model: 'claude-opus-5-5', effort: 'high', display_name: 'Claude Opus', short_name: 'Opus', model_version: '5.5' }),
    profile({ id: 'p2', slug: 'codex-sol-xhigh', harness: 'codex', family: 'openai', model: 'gpt-6.1-sol', effort: 'xhigh', display_name: 'Codex Sol', short_name: 'Sol', model_version: '6.1' }),
  ])
  // The run's pinned profile (its slug) names the model and its level.
  const pinned = sessionExecution(view({ harness: 'Claude', model: 'claude-opus-high', account: 'Claude Max · markus', session: { harness: 'claude' } }), names)
  assert.equal(pinned.modelLine, 'Opus 5.5 · high')
  assert.equal(pinned.accountLine, 'Claude Max · markus')
  // A reported model id is named by the registry; the reported level wins over the launch profile's.
  const reported = sessionExecution(view({ harness: 'Codex', model: 'claude-opus-high', session: { harness: 'codex', model: 'gpt-6.1-sol', reasoning_effort: 'xhigh' } }), names)
  assert.equal(reported.modelLine, 'GPT-6.1 Sol · xhigh')
  // A model the registry does not list still reads as a name.
  const unlisted = sessionExecution(view({ harness: 'Cursor', model: 'cursor-composer-2-5-fast', session: { harness: 'cursor' } }), names)
  assert.equal(unlisted.modelLine, 'Cursor Composer 2.5 Fast')
  for (const exec of [pinned, reported, unlisted]) assert.doesNotMatch(exec.modelLine, /[a-z]+-[a-z0-9]+-/)
})

// Risk (AEON-1211 fix round 4): the row says "Opus 5.5 · high", but the list's search reads only raw
// ids, so searching what the row says hides it; a registry line's own name fails the same way.
test('search reads the model as the row names it, and keeps the raw ids', () => {
  const profile = (fields: Partial<ModelProfile>): ModelProfile => ({ id: 'p', slug: '', harness: 'claude', family: 'anthropic', model: '', effort: 'high', tier: 'standard', enabled: true, ...fields })
  const names = modelNames([
    profile({ id: 'p1', slug: 'claude-opus-high', model: 'claude-opus-5-5', display_name: 'Claude Opus', short_name: 'Opus', model_version: '5.5' }),
    profile({ id: 'p2', slug: 'claude-house-high', model: 'claude-house', display_name: 'Haus Modell', model_version: '' }),
  ])
  const finds = (text: string, query: string) => query.toLowerCase().split(/\s+/).every(word => text.includes(word))
  const pinned = view({ harness: 'Claude', model: 'claude-opus-high', session: { harness: 'claude' } })
  assert.equal(sessionExecution(pinned, names).modelLine, 'Opus 5.5 · high')
  for (const query of ['Opus 5.5', 'opus 5.5 · high', 'claude-opus-high']) assert.ok(finds(sessionSearchText(pinned, names), query), query)
  // The reported model is named and searchable by its id; the launch profile's id still matches.
  const reported = view({ harness: 'Claude', model: 'claude-opus-high', session: { harness: 'claude', model: 'claude-opus-5-5', reasoning_effort: 'xhigh' } })
  for (const query of ['Opus 5.5 xhigh', 'claude-opus-5-5', 'claude-opus-high']) assert.ok(finds(sessionSearchText(reported, names), query), query)
  const custom = view({ harness: 'Claude', model: 'claude-house-high', session: { harness: 'claude' } })
  assert.equal(sessionExecution(custom, names).modelLine, 'Haus Modell · high')
  assert.ok(finds(sessionSearchText(custom, names), 'Haus Modell'))
  assert.equal(finds(sessionSearchText(custom, names), 'Opus'), false)
})
