// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1212 risk: a preference hides an urgent ask, or past admission is claimed as current.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { admissionHistory, openingTab, panelModelLine, panelQueryTab } from '../src/lib/leadPanel.ts'
import type { LeadDecision } from '../src/lib/lead.ts'
import { modelNames, sessionExecution } from '../src/components/agents/sessionRow.ts'
import type { SessionView } from '../src/stores/agents.ts'
const NOW = Date.parse('2026-10-11T12:00:00Z')
const input = { lead: true, asksYou: false, held: false, ended: false, workerProblem: false, workerLost: false, pinned: 'session' as const, last: 'lead' as const }
const report = (patch: Partial<LeadDecision> = {}): LeadDecision => ({ event_id: 1, project_id: 'project', session_id: 'lead', recorded_at: '2026-10-11T10:42:00Z', outcome: 'selected', reason_codes: [], results: [], gate_freshness: ['dial','harness','account_room','host_load'].map(kind => ({ kind: kind as 'dial', freshness: 'fresh' })), request: { stage: 'admission', outcome: 'selected', reason_codes: [], attempt: 1 }, ...patch })
const read = { complete: true, error: '', sessionStarted: '2026-10-11T09:00:00Z', now: NOW }

test('AEON-1249: panel model names and effort match the session row for registered and unlisted models', () => {
  const profiles = [{ id: 'sol', slug: 'sol-medium', harness: 'codex', family: 'openai', model: 'gpt-6-sol', effort: 'medium', tier: 'standard', enabled: true, display_name: 'Sol', model_version: '6' }]
  const session = { harness: 'codex', model: 'gpt-6-sol', reasoning_effort: 'medium' }
  assert.equal(panelModelLine(session, profiles), 'GPT-6 Sol · medium')
  assert.equal(panelModelLine({ ...session, model: null }, profiles, 'sol-medium'), 'GPT-6 Sol · medium')
  assert.equal(panelModelLine({ ...session, model: 'gpt-6-sol', reasoning_effort: 'xhigh' }, []), 'GPT-6 Sol · xhigh')
  assert.equal(panelModelLine({ ...session, model: 'sol-medium', reasoning_effort: null }, profiles), 'GPT-6 Sol · medium')
  assert.equal(panelModelLine({ ...session, model: 'vendor-private-id' }, profiles), 'Vendor Private Id · medium')
  assert.equal(panelModelLine({ ...session, harness: 'claude' }, profiles), 'GPT-6 Sol · medium')
  assert.equal(panelModelLine({ ...session, model: null }, profiles), '')
  // AEON-1249 risk: the same session is named differently after a registry miss,
  // or its pinned level disappears in Details. Compare the real row projection.
  const names = modelNames(profiles)
  for (const [model, harness, effort, fallback] of [
    ['gpt-6-sol', 'codex', 'xhigh', 'sol-medium'],
    ['sol-medium', 'codex', '', ''],
    ['gpt-6.1-sol-xhigh', 'codex', '', ''],
    ['claude-opus-5-5', 'claude', 'high', ''],
    ['vendor-private-id', 'codex', 'medium', ''],
    ['gpt-6-sol', 'claude', '', ''],
    ['', 'codex', '', 'sol-medium'],
    ['', 'codex', 'xhigh', ''],
  ]) {
    const row = { session: { harness, model, reasoning_effort: effort }, model: fallback } as SessionView
    assert.equal(panelModelLine(row.session, profiles, row.model), sessionExecution(row, names).modelLine)
  }
})

test('AEON-1212: an explicit link wins; a real ask, held start and worker problem win over the pinned default; otherwise default then last used', () => {
  assert.deepEqual(openingTab({ ...input, asksYou: true }), { tab: 'chat', why: 'it asks you something · Session is your default' })
  assert.equal(openingTab({ ...input, asksYou: true, forced: 'lead' }).tab, 'lead')
  for (const need of ['held','ended','workerProblem','workerLost'] as const) assert.equal(openingTab({ ...input, [need]: true }).tab, 'lead')
  assert.deepEqual(openingTab(input), { tab: 'session', why: 'your default' })
  assert.deepEqual(openingTab({ ...input, pinned: null, last: 'chat' }), { tab: 'chat', why: 'last used · no default set' })
  assert.equal(openingTab({ ...input, lead: false }).tab, 'overview')
  assert.equal(panelQueryTab('messages', true), 'chat')
  assert.equal(panelQueryTab('session', false), 'overview')
})

test('AEON-1212: admission uses four real gates and a timestamp; no report, unreadable, stale and held states never claim current passes', () => {
  const passed = admissionHistory([report()], read)
  assert.equal(passed.rows.length, 4)
  assert.match(passed.caption, /^All passed at the last start · \d\d:\d\d$/)
  const held = admissionHistory([report({ reason_codes: ['dial_full'] })], read)
  assert.match(held.caption, /^1 held the last start/)
  assert.equal(held.rows[0]?.detail, 'Dial held it: the dial was full')
  assert.equal(held.rows[0]?.reason, 'dial_full')
  for (const [code, kind, words] of [
    ['dial_full', 'dial', 'the dial was full'],
    ['harness_full', 'harness', 'a harness limit was reached'],
    ['daily_limit', 'account_room', 'the daily cap was reached'],
    ['daily_cap', 'account_room', 'the daily cap was reached'],
    ['account_full', 'account_room', 'no account had room'],
    ['account_room_full', 'account_room', 'no account had room'],
    ['capacity_full', 'account_room', 'no account had room'],
    ['host_full', 'host_load', 'the computer was too busy'],
    ['host_load_full', 'host_load', 'the computer was too busy'],
  ] as const) {
    const row = admissionHistory([report({ reason_codes: [code] })], read).rows.find(row => row.kind === kind)!
    assert.equal(row.state, 'held'); assert.equal(row.detail, `${row.label} held it: ${words}`); assert.equal(row.reason, code)
  }
  const unknown = report({ outcome: 'wait', reason_codes: ['future_check'], request: { stage: 'admission', outcome: 'wait', reason_codes: ['future_check'], attempt: 1, gates: [{ kind: 'dial', state: 'full' }] } })
  assert.equal(admissionHistory([unknown], read).rows[0]?.detail, 'Dial held it: a check said no')
  assert.equal(admissionHistory([unknown], read).rows[0]?.reason, 'future_check')
  const daily = admissionHistory([report({ reason_codes: ['account_room_full'], request: { stage: 'admission', outcome: 'wait', reason_codes: ['daily_limit'], attempt: 1 } })], read)
  assert.equal(daily.rows.find(row => row.kind === 'account_room')?.detail, 'Account room held it: the daily cap was reached')
  const unknownWithoutGate = admissionHistory([report({ outcome: 'wait', reason_codes: ['future_check'] })], read)
  assert.equal(unknownWithoutGate.rows[0]?.detail, 'Dial: a check said no')
  assert.doesNotMatch(unknownWithoutGate.caption, /All passed/)
  assert.equal(admissionHistory([], read).caption, 'No start yet')
  assert.equal(admissionHistory([], read).rows.length, 0)
  assert.equal(admissionHistory([report()], { ...read, error: 'history denied' }).rows.length, 0)
  assert.equal(admissionHistory([report()], { ...read, error: 'history denied' }).caption, "Couldn't read the last start")
  for (const stale of [{ ...read, sessionStarted: '2026-10-11T11:00:00Z' }, { ...read, now: NOW + 24 * 60 * 60_000 }]) {
    const result = admissionHistory([report()], stale)
    assert.equal(result.tone, 'stale'); assert.equal(result.rows.length, 4); assert.match(result.caption, /may be out of date/)
  }
  const absent = admissionHistory([report({ outcome: 'wait', reason_codes: ['admission_not_configured'], request: { stage: 'admission', outcome: 'wait', reason_codes: ['admission_not_configured'], attempt: 1 } })], read)
  assert.notEqual(absent.caption, "Couldn't read the last start")
  assert.match(absent.rows.map(row => row.detail).join(' '), /not configured/)
  const unreadable = admissionHistory([report({ gate_freshness: [], request: { stage: 'admission', outcome: 'wait', reason_codes: ['admission_unavailable'], attempt: 1 } })], read)
  assert.equal(unreadable.tone, 'unreadable'); assert.equal(unreadable.rows.length, 0)
  const partiallyUnreadable = admissionHistory([report({ reason_codes: ['host_unavailable'] })], read)
  assert.equal(partiallyUnreadable.caption, "Couldn't read the last start")
  assert.equal(partiallyUnreadable.rows.length, 0)
  assert.match(partiallyUnreadable.note, /Host load/)
})
