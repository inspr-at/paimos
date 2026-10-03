// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { test } from 'node:test'
import type { SessionView } from '../src/stores/agents.ts'
import { explicitOutcome, intendedResult, modelProvider, sessionContext, sessionExecution, sessionEtaEligible } from '../src/components/agents/sessionRow.ts'

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
  assert.equal(sessionContext(titled, intendedResult(titled)), 'hausv')
  const phrase = view({ session: { brief: 'Release membership stays consistent' }, ticket: titled.ticket })
  assert.equal(explicitOutcome(phrase.session.brief), 'Release membership stays consistent')
  assert.equal(intendedResult(phrase), 'Release membership stays consistent')
  const keyOnly = view({ session: { brief: 'AEON-221' }, name: 'SC1 working' })
  assert.equal(intendedResult(keyOnly), 'AEON-221')
  assert.equal(sessionContext(keyOnly, 'AEON-221'), 'SC1 working')
  const plain = view({ name: 'amy', session: { host: 'csb1', activity_note: 'Quiet · heartbeat only' } as SessionView['session'] })
  assert.equal(intendedResult(plain), 'amy')
  assert.equal(sessionContext(plain, 'amy'), 'csb1')
  assert.equal(intendedResult(plain).includes('heartbeat'), false)
})

test('execution names the reported model and effort, and omits what is not reported', () => {
  const known = view({ harness: 'Codex', session: { harness: 'codex', model: 'gpt-6-sol', reasoning_effort: 'xhigh', account_label: 'Codex Pro' } })
  const exec = sessionExecution(known)
  assert.equal(exec.modelLine, 'gpt-6-sol · xhigh')
  assert.equal(exec.accountLine, 'Codex · Codex Pro')
  assert.equal(exec.provider, 'openai')
  const cursorGrok = view({ harness: 'Cursor', model: 'grok-4.7', session: { harness: 'cursor', model: 'grok-4.7', reasoning_effort: 'high' } })
  assert.equal(sessionExecution(cursorGrok).provider, 'xai')
  assert.equal(modelProvider('cursor-composer-2'), 'cursor')
  assert.equal(modelProvider('pi-anthropic-sonnet-high'), 'anthropic')
  assert.equal(modelProvider('integration-model'), 'unknown')
  assert.equal(modelProvider('codex-astra-xhigh'), 'unknown')
  const missing = view({ harness: 'Grok', model: 'claude-fable-high', account: 'Claude Max', session: { harness: 'grok' } })
  const fallback = sessionExecution(missing)
  assert.equal(fallback.modelLine, 'claude-fable-high')
  assert.equal(fallback.accountLine, 'Grok · Claude Max')
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
  assert.equal(sessionExecution(row).modelLine, 'claude-sonnet · low')
  const sessionID = row.session.id
  row.session = { ...row.session, model: 'claude-opus', reasoning_effort: 'high' }
  assert.equal(row.session.id, sessionID)
  assert.equal(sessionExecution(row).modelLine, 'claude-opus · high')
  assert.equal(sessionExecution(row).provider, 'anthropic')
})
