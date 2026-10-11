// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1212 risk: a preference hides an urgent ask, or past admission is claimed as current.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { admissionHistory, openingTab, panelQueryTab } from '../src/lib/leadPanel.ts'
import type { LeadDecision } from '../src/lib/lead.ts'
const NOW = Date.parse('2026-10-11T12:00:00Z')
const input = { lead: true, asksYou: false, held: false, ended: false, workerProblem: false, workerLost: false, pinned: 'session' as const, last: 'lead' as const }
const report = (patch: Partial<LeadDecision> = {}): LeadDecision => ({ event_id: 1, project_id: 'project', session_id: 'lead', recorded_at: '2026-10-11T10:42:00Z', outcome: 'selected', reason_codes: [], results: [], gate_freshness: ['dial','harness','account_room','host_load'].map(kind => ({ kind: kind as 'dial', freshness: 'fresh' })), request: { stage: 'admission', outcome: 'selected', reason_codes: [], attempt: 1 }, ...patch })
const read = { complete: true, error: '', sessionStarted: '2026-10-11T09:00:00Z', now: NOW }

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
  assert.equal(held.rows[0]?.detail, 'Dial held it: dial_full')
  assert.equal(admissionHistory([], read).caption, 'No start yet')
  assert.equal(admissionHistory([], read).rows.length, 0)
  assert.equal(admissionHistory([report()], { ...read, error: 'history denied' }).rows.length, 0)
  assert.equal(admissionHistory([report()], { ...read, error: 'history denied' }).caption, "Couldn't read the last start")
  for (const stale of [{ ...read, sessionStarted: '2026-10-11T11:00:00Z' }, { ...read, now: NOW + 24 * 60 * 60_000 }]) {
    const result = admissionHistory([report()], stale)
    assert.equal(result.tone, 'stale'); assert.equal(result.rows.length, 4); assert.match(result.caption, /may be out of date/)
  }
  const unreadable = admissionHistory([report({ gate_freshness: [], request: { stage: 'admission', outcome: 'wait', reason_codes: ['admission_unavailable'], attempt: 1 } })], read)
  assert.equal(unreadable.tone, 'unreadable'); assert.equal(unreadable.rows.length, 0)
})
