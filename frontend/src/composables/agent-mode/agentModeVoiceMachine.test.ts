/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as
 * published by the Free Software Foundation, version 3.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public
 * License along with this program. If not, see <https://www.gnu.org/licenses/>.
 */

import { describe, expect, it } from 'vitest'

import { readFileSync } from 'node:fs'

import { makeFixtureSnapshot } from '@/services/agentModeFixtures'
import { normalizeWireSnapshot } from '@/services/agentModeTransport'
import type { Delivery } from '@/services/agentMode'
import type { VoiceProjectRef } from './agentModeVoiceIntent'
import {
  VOICE_UTTERANCE_LRU_LIMIT,
  buildNoteRequestId,
  initialVoiceState,
  voiceReducer,
  type VoiceEffect,
  type VoiceEvent,
  type VoiceMachineState,
} from './agentModeVoiceMachine'

const snapshot = normalizeWireSnapshot(makeFixtureSnapshot(10), 0)
const fixtures = snapshot.deliveries
const selected = fixtures[0] // dlv-812 / PAI-812
const other = fixtures[3] // dlv-815 / PAI-815

/** Authorized project catalog, straight from the aggregate boundary. */
const CATALOG: VoiceProjectRef[] = (snapshot.aggregates?.projects ?? []).map((project) => ({
  projectId: project.projectId,
  projectKey: project.projectKey,
  projectName: project.projectName,
}))

function d(patch: Partial<Delivery>): Delivery {
  return { ...selected, ...patch }
}

/** The selection with a patched capability set, in row order. */
function withSelectedCapability(comment: boolean | null): Delivery[] {
  return fixtures.map((row) => (
    row.id === selected.id ? { ...row, capabilities: { ...row.capabilities, comment } } : row
  ))
}

/** Machine primed with an authorized snapshot and a selection. */
function primed(overrides: Partial<VoiceMachineState> = {}): VoiceMachineState {
  return voiceReducer(initialVoiceState(overrides), {
    type: 'context',
    projectCatalog: CATALOG,
    deliveries: fixtures,
    selectedId: selected.id,
    selectionEpoch: 'epoch-1',
  }).state
}

interface Run {
  state: VoiceMachineState
  effects: VoiceEffect[]
  /** Effects of the LAST event only. */
  last: VoiceEffect[]
}

function run(state: VoiceMachineState, events: readonly VoiceEvent[]): Run {
  let current = state
  let all: VoiceEffect[] = []
  let last: VoiceEffect[] = []
  for (const event of events) {
    const result = voiceReducer(current, event)
    current = result.state
    last = result.effects
    all = [...all, ...result.effects]
  }
  return { state: current, effects: all, last }
}

function say(text: string, utteranceId = 'u1', sequence?: number): VoiceEvent {
  return { type: 'final', utteranceId, text, sequence }
}

function dictate(state: VoiceMachineState, body = 'fixture 84 fails', utteranceId = 'note-1'): Run {
  return run(state, [say(`note ${body}`, utteranceId)])
}

describe('agentModeVoiceMachine — transcript lifecycle', () => {
  it('lets a partial segment move the visible draft and nothing else', () => {
    const result = run(primed(), [
      { type: 'partial', utteranceId: 'u1', text: 'select PAI' },
      { type: 'partial', utteranceId: 'u1', text: 'select PAI-815' },
    ])
    expect(result.effects).toEqual([])
    expect(result.state.draft).toBe('select PAI-815')
    expect(result.state.draftUtteranceId).toBe('u1')
    expect(result.state.executed).toEqual([])
  })

  it('executes a final exactly once and clears its draft', () => {
    const result = run(primed(), [
      { type: 'partial', utteranceId: 'u1', text: 'select PAI-81' },
      say('select PAI-815'),
    ])
    expect(result.last).toEqual([{ type: 'execute', command: { type: 'select', deliveryId: 'dlv-815' } }])
    expect(result.state.draft).toBe('')
    expect(result.state.draftUtteranceId).toBeNull()
    expect(result.state.executed).toEqual(['u1'])
  })

  it('drops a repeated final for the same utterance id', () => {
    const first = run(primed(), [say('select PAI-815')])
    const second = voiceReducer(first.state, say('select PAI-815'))
    expect(second.effects).toEqual([])
    expect(second.state.executed).toEqual(['u1'])
  })

  it('drops an out-of-order final even after its id fell out of the LRU', () => {
    // Fill the LRU past its limit so 'stale' is definitely evicted.
    const events: VoiceEvent[] = [say('next', 'stale', 1)]
    for (let i = 0; i < VOICE_UTTERANCE_LRU_LIMIT + 5; i += 1) events.push(say('next', `u-${i}`, i + 2))
    const filled = run(primed(), events)
    expect(filled.state.executed).toHaveLength(VOICE_UTTERANCE_LRU_LIMIT)
    expect(filled.state.executed).not.toContain('stale')

    const replay = voiceReducer(filled.state, say('next', 'stale', 1))
    expect(replay.effects).toEqual([])
  })

  it('keeps the LRU bounded and evicts the oldest id first', () => {
    const events: VoiceEvent[] = []
    for (let i = 0; i < VOICE_UTTERANCE_LRU_LIMIT + 3; i += 1) events.push(say('next', `u-${i}`))
    const result = run(primed(), events)
    expect(result.state.executed).toHaveLength(VOICE_UTTERANCE_LRU_LIMIT)
    expect(result.state.executed[0]).toBe('u-3')
    expect(result.state.executed[result.state.executed.length - 1]).toBe(`u-${VOICE_UTTERANCE_LRU_LIMIT + 2}`)
  })

  it('still accepts a later sequence after an out-of-order one was dropped', () => {
    const result = run(primed(), [say('next', 'a', 5), say('next', 'b', 3), say('previous', 'c', 6)])
    expect(result.effects).toEqual([
      { type: 'execute', command: { type: 'step', direction: 'next' } },
      { type: 'execute', command: { type: 'step', direction: 'previous' } },
    ])
    expect(result.state.highWaterSequence).toBe(6)
  })

  it('ignores a late partial for an already-executed utterance', () => {
    const executed = run(primed(), [say('next')])
    const late = voiceReducer(executed.state, { type: 'partial', utteranceId: 'u1', text: 'nex' })
    expect(late.effects).toEqual([])
    expect(late.state.draft).toBe('')
  })

  it('runs the typed path through the same reducer with the same de-duplication', () => {
    const typed = run(primed(), [
      { type: 'typed', utteranceId: 't1', text: 'select PAI-815' },
      { type: 'typed', utteranceId: 't1', text: 'select PAI-815' },
    ])
    expect(typed.effects).toEqual([{ type: 'execute', command: { type: 'select', deliveryId: 'dlv-815' } }])
  })

  it('reports unsupported supervisory verbs without ever emitting a command', () => {
    const result = run(primed(), [say('approve the run')])
    expect(result.last).toEqual([{ type: 'unsupported', control: 'approve' }])
    expect(JSON.stringify(result.effects)).not.toContain('execute')
  })

  it.each<[string, string]>([
    ['select ZZZ-1', 'no_match'],
    ['blah blah', 'unknown_command'],
  ])('turns %s into the %s notice', (text, code) => {
    expect(run(primed(), [say(text)]).last).toEqual([{ type: 'notice', code }])
  })

  it('does not mutate the state it was given', () => {
    const state = primed()
    const snapshot = JSON.stringify(state)
    voiceReducer(state, say('select PAI-815'))
    voiceReducer(state, { type: 'partial', utteranceId: 'x', text: 'hello' })
    voiceReducer(state, { type: 'reset' })
    expect(JSON.stringify(state)).toBe(snapshot)
  })
})

describe('agentModeVoiceMachine — clarification', () => {
  it('opens a bounded clarification and executes the chosen candidate', () => {
    const asked = run(primed(), [say('select agent codex')])
    expect(asked.last).toHaveLength(1)
    expect(asked.last[0].type).toBe('clarify')
    expect(asked.state.candidates.map((c) => c.index)).toEqual([1, 2, 3])

    const chosen = voiceReducer(asked.state, say('number two', 'u2'))
    expect(chosen.effects).toEqual([{
      type: 'execute',
      command: { type: 'select', deliveryId: asked.state.candidates[1].deliveryId },
    }])
    expect(chosen.state.candidates).toEqual([])
  })

  it('accepts a bare numeral only while the clarification is open', () => {
    const asked = run(primed(), [say('select agent codex')])
    expect(voiceReducer(asked.state, say('two', 'u2')).effects[0].type).toBe('execute')
    // Same words, no open clarification → not a choice.
    expect(run(primed(), [say('two')]).last).toEqual([{ type: 'notice', code: 'unknown_command' }])
  })

  it('refuses a number that was never offered', () => {
    const asked = run(primed(), [say('select lane Agent Mode')])
    expect(asked.state.candidates).toHaveLength(2)
    expect(voiceReducer(asked.state, say('number three', 'u2')).effects)
      .toEqual([{ type: 'notice', code: 'no_clarification' }])
  })

  it('drops every candidate on a snapshot reset', () => {
    const asked = run(primed(), [say('select agent codex')])
    const reset = voiceReducer(asked.state, {
      type: 'context',
      projectCatalog: CATALOG,
      deliveries: fixtures,
      selectedId: selected.id,
      selectionEpoch: 'epoch-2',
    })
    expect(reset.state.candidates).toEqual([])
    expect(reset.state.candidateMatchCount).toBe(0)
    expect(voiceReducer(reset.state, say('number one', 'u2')).effects)
      .toEqual([{ type: 'notice', code: 'no_clarification' }])
  })

  it('drops only de-authorized candidates and never renumbers the survivors', () => {
    const asked = run(primed(), [say('select agent codex')])
    const removed = asked.state.candidates[0].deliveryId
    const surviving = asked.state.candidates.slice(1)
    const acl = voiceReducer(asked.state, {
      type: 'context',
      projectCatalog: CATALOG,
      deliveries: fixtures.filter((x) => x.id !== removed),
      selectedId: selected.id,
      selectionEpoch: 'epoch-1',
    })
    expect(acl.state.candidates).toEqual(surviving)
    expect(acl.state.candidates.map((c) => c.index)).toEqual([2, 3])
    // "two" still means what it meant when it was spoken back.
    expect(voiceReducer(acl.state, say('two', 'u2')).effects).toEqual([
      { type: 'execute', command: { type: 'select', deliveryId: surviving[0].deliveryId } },
    ])
  })

  it('keeps a bare numeral meaningful after an earlier candidate was de-authorized', () => {
    const asked = run(primed(), [say('select agent codex')])
    const removed = asked.state.candidates[0].deliveryId
    const acl = voiceReducer(asked.state, {
      type: 'context',
      projectCatalog: CATALOG,
      deliveries: fixtures.filter((x) => x.id !== removed),
      selectedId: selected.id,
      selectionEpoch: 'epoch-1',
    })
    expect(acl.state.candidates).toHaveLength(2)
    expect(voiceReducer(acl.state, say('three', 'u2')).effects).toEqual([
      { type: 'execute', command: { type: 'select', deliveryId: asked.state.candidates[2].deliveryId } },
    ])
  })

  it('never silently selects a candidate that left the authorized set', () => {
    const asked = run(primed(), [say('select agent codex')])
    const target = asked.state.candidates[0]
    // Authority moved without a context event reaching the machine.
    const stale: VoiceMachineState = {
      ...asked.state,
      deliveries: fixtures.filter((x) => x.id !== target.deliveryId),
    }
    const chosen = voiceReducer(stale, say('number one', 'u2'))
    expect(chosen.effects).toEqual([{ type: 'notice', code: 'candidate_unavailable' }])
    expect(chosen.state.candidates).toEqual([])
  })

  it('ends an open clarification as soon as another command runs', () => {
    const asked = run(primed(), [say('select agent codex')])
    const moved = voiceReducer(asked.state, say('next', 'u2'))
    expect(moved.state.candidates).toEqual([])
    expect(moved.effects).toEqual([{ type: 'execute', command: { type: 'step', direction: 'next' } }])
  })
})

describe('agentModeVoiceMachine — note binding', () => {
  it('binds delivery, issue, attempt, epoch, body, and a stable request id', () => {
    const result = dictate(primed())
    const clientRequestId = buildNoteRequestId({
      utteranceId: 'note-1',
      deliveryId: selected.id,
      selectionEpoch: 'epoch-1',
      body: 'fixture 84 fails',
    })
    expect(result.last).toEqual([{
      type: 'note_preview',
      clientRequestId,
    }])
    expect(result.state.note?.binding).toEqual({
      deliveryId: selected.id,
      issueId: selected.issueId,
      issueKey: selected.issueKey,
      attemptId: selected.attempt.id,
      selectionEpoch: 'epoch-1',
      body: 'fixture 84 fails',
      clientRequestId,
      utteranceId: 'note-1',
    })
    expect(JSON.stringify(result.last)).not.toContain('fixture 84 fails')
    expect(result.state.note?.status).toBe('preview')
  })

  it('derives a request id that is stable per binding and different per body or delivery', () => {
    const seed = { utteranceId: 'u', deliveryId: 'dlv-1', selectionEpoch: 'e1', body: 'a' }
    expect(buildNoteRequestId(seed)).toBe(buildNoteRequestId({ ...seed }))
    expect(buildNoteRequestId(seed)).not.toBe(buildNoteRequestId({ ...seed, body: 'b' }))
    expect(buildNoteRequestId(seed)).not.toBe(buildNoteRequestId({ ...seed, deliveryId: 'dlv-2' }))
    expect(buildNoteRequestId(seed)).not.toBe(buildNoteRequestId({ ...seed, selectionEpoch: 'e2' }))
    // The key travels to the server, so the dictated body must not be in it.
    expect(buildNoteRequestId({ ...seed, body: 'secret words' })).not.toContain('secret')
    expect(buildNoteRequestId(seed)).toMatch(/^amv1-[0-9a-f]{16}$/)
  })

  it('replaces an earlier preview instead of stacking two confirmable notes', () => {
    const first = dictate(primed())
    const second = voiceReducer(first.state, say('note the retry loop is hot', 'note-2'))
    expect(second.effects.map((e) => e.type)).toEqual(['note_discarded', 'note_preview'])
    expect(second.effects[0]).toEqual({ type: 'note_discarded', reason: 'replaced' })
    expect(second.state.note?.binding.body).toBe('the retry loop is hot')
  })

  it('cancels a preview on request and refuses to cancel nothing', () => {
    const drafted = dictate(primed())
    const cancelled = voiceReducer(drafted.state, say('cancel', 'u2'))
    expect(cancelled.effects).toEqual([{ type: 'note_discarded', reason: 'cancelled' }])
    expect(cancelled.state.note).toBeNull()
    expect(voiceReducer(cancelled.state, say('cancel', 'u3')).effects)
      .toEqual([{ type: 'notice', code: 'nothing_to_cancel' }])
    expect(voiceReducer(primed(), say('confirm')).effects)
      .toEqual([{ type: 'notice', code: 'nothing_to_confirm' }])
  })

  it('refuses to draft a note against a delivery outside the authorized set', () => {
    const drafted = run(primed(), [say('note on ZZZ-1 that the fixture is flaky')])
    expect(drafted.last).toEqual([{ type: 'notice', code: 'no_match' }])
    expect(drafted.state.note).toBeNull()
  })

  it('refuses to draft a note against an authorized delivery that is not selected', () => {
    const drafted = run(primed(), [say(`note on ${other.issueKey} that the fixture is flaky`)])
    expect(drafted.last).toEqual([{ type: 'notice', code: 'note_target_not_selected' }])
    expect(drafted.state.note).toBeNull()
  })

  it.each<[string, boolean | null]>([
    ['withheld', false],
    ['unclaimed', null],
  ])('refuses to draft a note while the comment capability is %s', (_label, comment) => {
    const state = voiceReducer(initialVoiceState(), {
      type: 'context',
      deliveries: withSelectedCapability(comment),
      projectCatalog: CATALOG,
      selectedId: selected.id,
      selectionEpoch: 'epoch-1',
    }).state
    const drafted = dictate(state)
    expect(drafted.last).toEqual([{ type: 'notice', code: 'note_not_permitted' }])
    expect(drafted.state.note).toBeNull()
  })
})

describe('agentModeVoiceMachine — confirm is exactly once', () => {
  it('submits once and ignores a double confirm while in flight', () => {
    const drafted = dictate(primed())
    const first = voiceReducer(drafted.state, say('confirm', 'u2'))
    expect(first.effects).toEqual([{
      type: 'submit_note', clientRequestId: drafted.state.note!.binding.clientRequestId,
    }])
    expect(JSON.stringify(first.effects)).not.toContain(drafted.state.note!.binding.body)
    expect(first.state.note?.status).toBe('submitting')

    // A second confirm — different utterance id, so de-duplication cannot
    // be what saves us here.
    const second = voiceReducer(first.state, say('confirm', 'u3'))
    expect(second.effects).toEqual([])
    expect(second.state.note?.status).toBe('submitting')
  })

  it('clears the note when the create succeeds', () => {
    const drafted = dictate(primed())
    const flight = voiceReducer(drafted.state, say('confirm', 'u2'))
    const settled = voiceReducer(flight.state, {
      type: 'note_settled',
      clientRequestId: flight.state.note!.binding.clientRequestId,
      ok: true,
    })
    expect(settled.effects).toEqual([{ type: 'note_discarded', reason: 'submitted' }])
    expect(settled.state.note).toBeNull()
  })

  it('retries a failed create under the SAME client request id', () => {
    const drafted = dictate(primed())
    const requestId = drafted.state.note!.binding.clientRequestId
    const flight = voiceReducer(drafted.state, say('confirm', 'u2'))
    const failed = voiceReducer(flight.state, { type: 'note_settled', clientRequestId: requestId, ok: false })
    expect(failed.effects).toEqual([{ type: 'notice', code: 'note_submit_failed' }])
    expect(failed.state.note?.status).toBe('failed')

    const retry = voiceReducer(failed.state, say('confirm', 'u3'))
    expect(retry.effects).toEqual([{
      type: 'submit_note', clientRequestId: failed.state.note!.binding.clientRequestId,
    }])
    expect(retry.state.note!.binding.clientRequestId).toBe(requestId)
  })

  it('ignores a settlement for a request id it is no longer holding', () => {
    const drafted = dictate(primed())
    const flight = voiceReducer(drafted.state, say('confirm', 'u2'))
    const stray = voiceReducer(flight.state, { type: 'note_settled', clientRequestId: 'amv1-deadbeefdeadbeef', ok: true })
    expect(stray.effects).toEqual([])
    expect(stray.state.note?.status).toBe('submitting')
  })

  it('does not pretend an in-flight create can be cancelled', () => {
    const drafted = dictate(primed())
    const flight = voiceReducer(drafted.state, say('confirm', 'u2'))
    const cancelled = voiceReducer(flight.state, say('cancel', 'u3'))
    expect(cancelled.effects).toEqual([{ type: 'notice', code: 'note_in_flight' }])
    expect(cancelled.state.note?.status).toBe('submitting')
  })

  it('discards the note when the selection moved before the confirm was heard', () => {
    const drafted = dictate(primed())
    const moved: VoiceMachineState = { ...drafted.state, selectedId: other.id }
    const confirmed = voiceReducer(moved, say('confirm', 'u2'))
    expect(confirmed.effects).toEqual([
      { type: 'note_discarded', reason: 'selection_changed' },
      { type: 'notice', code: 'no_match' },
    ])
    expect(confirmed.state.note).toBeNull()
  })

  it('discards the note when authority moved before the confirm was heard', () => {
    const drafted = dictate(primed())
    const stale: VoiceMachineState = { ...drafted.state, selectionEpoch: 'epoch-2' }
    const confirmed = voiceReducer(stale, say('confirm', 'u2'))
    expect(confirmed.effects).toEqual([
      { type: 'note_discarded', reason: 'epoch_changed' },
      { type: 'notice', code: 'no_match' },
    ])
    expect(confirmed.state.note).toBeNull()
  })

  it.each<[string, (rows: readonly Delivery[]) => Delivery[], string]>([
    [
      'the comment capability was withdrawn',
      () => withSelectedCapability(false),
      'capability_revoked',
    ],
    [
      'the row now reports a different issue',
      (rows) => rows.map((r) => (r.id === selected.id ? { ...r, issueId: r.issueId + 1 } : r)),
      'issue_changed',
    ],
    [
      'the row rolled over to a new attempt',
      (rows) => rows.map((r) => (
        r.id === selected.id ? { ...r, attempt: { ...r.attempt, id: 'attempt-812-2', number: 2 } } : r
      )),
      'attempt_changed',
    ],
  ])('refuses the confirm when %s at the same id and epoch', (_label, mutate, reason) => {
    const drafted = dictate(primed())
    // Same delivery id, same authority epoch, same selection: only the exact
    // bound fact moved, and it still cannot be confirmed.
    const moved: VoiceMachineState = { ...drafted.state, deliveries: mutate(fixtures) }
    const confirmed = voiceReducer(moved, say('confirm', 'u2'))
    expect(confirmed.effects).toEqual([
      { type: 'note_discarded', reason },
      { type: 'notice', code: 'no_match' },
    ])
    expect(confirmed.state.note).toBeNull()
    expect(JSON.stringify(confirmed.effects)).not.toContain('submit_note')
  })
})

describe('agentModeVoiceMachine — selection, authority, and refresh', () => {
  it('clears only resolver-relative draft and candidates on a semantic context change', () => {
    const noted = dictate(primed()).state
    const partial = voiceReducer({
      ...noted,
      candidates: [{ index: 1, deliveryId: other.id, issueKey: other.issueKey, title: other.title }],
      candidateMatchCount: 2,
      candidateTruncated: true,
    }, {
      type: 'partial', utteranceId: 'partial-private', text: 'note that PRIVATE_CANARY',
    }).state

    const changed = voiceReducer(partial, { type: 'resolver_context_changed' })
    expect(changed.effects).toEqual([])
    expect(changed.state.draft).toBe('')
    expect(changed.state.draftUtteranceId).toBeNull()
    expect(changed.state.candidates).toEqual([])
    expect(changed.state.candidateMatchCount).toBe(0)
    expect(changed.state.candidateTruncated).toBe(false)
    expect(changed.state.note).toEqual(noted.note)
    expect(changed.state.executed).toEqual(noted.executed)
    expect(changed.state.selectionEpoch).toBe(noted.selectionEpoch)
  })

  it('scrubs partial text on a selection epoch change but preserves it on an ordinary refresh', () => {
    const partial = voiceReducer(primed(), {
      type: 'partial', utteranceId: 'partial-private', text: 'note that PRIVATE_CANARY',
    }).state
    const same = voiceReducer(partial, {
      type: 'context', projectCatalog: CATALOG, deliveries: fixtures,
      selectedId: selected.id, selectionEpoch: 'epoch-1',
    }).state
    expect(same.draft).toBe('note that PRIVATE_CANARY')
    expect(same.draftUtteranceId).toBe('partial-private')

    const changed = voiceReducer(same, {
      type: 'context', projectCatalog: CATALOG, deliveries: fixtures,
      selectedId: other.id, selectionEpoch: 'epoch-2',
    }).state
    expect(changed.draft).toBe('')
    expect(changed.draftUtteranceId).toBeNull()
  })

  it('keeps the note through a background refresh of the same generation', () => {
    const drafted = dictate(primed())
    const refreshed = voiceReducer(drafted.state, {
      type: 'context',
      projectCatalog: CATALOG,
      deliveries: fixtures.map((x) => d({ ...x, updatedAt: '2026-08-20T14:00:00Z' })),
      selectedId: selected.id,
      selectionEpoch: 'epoch-1',
    })
    expect(refreshed.effects).toEqual([])
    expect(refreshed.state.note?.binding.body).toBe('fixture 84 fails')
  })

  it.each<[string, Partial<{ selectedId: string | null; epoch: string; drop: boolean }>, string]>([
    ['the selection moves', { selectedId: other.id }, 'selection_changed'],
    ['the delivery is revoked', { drop: true }, 'authority_revoked'],
    ['the authority epoch changes', { epoch: 'epoch-2' }, 'epoch_changed'],
  ])('discards the note when %s', (_label, patch, reason) => {
    const drafted = dictate(primed())
    const next = voiceReducer(drafted.state, {
      type: 'context',
      projectCatalog: CATALOG,
      deliveries: patch.drop ? fixtures.filter((x) => x.id !== selected.id) : fixtures,
      selectedId: patch.drop ? null : patch.selectedId ?? selected.id,
      selectionEpoch: patch.epoch ?? 'epoch-1',
    })
    expect(next.effects).toEqual([{ type: 'note_discarded', reason }])
    expect(next.state.note).toBeNull()
  })

  it.each<[string, (rows: readonly Delivery[]) => Delivery[], string]>([
    ['comment is withdrawn', () => withSelectedCapability(false), 'capability_revoked'],
    ['comment becomes unclaimed', () => withSelectedCapability(null), 'capability_revoked'],
    [
      'the issue rotates under the same row',
      (rows) => rows.map((r) => (r.id === selected.id ? { ...r, issueId: r.issueId + 1 } : r)),
      'issue_changed',
    ],
    [
      'the attempt rotates under the same row',
      (rows) => rows.map((r) => (
        r.id === selected.id ? { ...r, attempt: { ...r.attempt, id: 'attempt-812-2', number: 2 } } : r
      )),
      'attempt_changed',
    ],
  ])('discards the note when %s in a same-epoch refresh', (_label, mutate, reason) => {
    const drafted = dictate(primed())
    const next = voiceReducer(drafted.state, {
      type: 'context',
      projectCatalog: CATALOG,
      deliveries: mutate(fixtures),
      selectedId: selected.id,
      selectionEpoch: 'epoch-1',
    })
    expect(next.effects).toEqual([{ type: 'note_discarded', reason }])
    expect(next.state.note).toBeNull()
  })

  it('resolves a project filter against the catalog, not the visible rows', () => {
    // Only project PAI rows are on screen; "project Agent runtime" still
    // resolves, and it clears the lane exactly as the filter bar does.
    const narrowed = voiceReducer(initialVoiceState(), {
      type: 'context',
      deliveries: fixtures.filter((x) => x.lane.projectId === 6),
      projectCatalog: CATALOG,
      selectedId: selected.id,
      selectionEpoch: 'epoch-1',
    }).state
    expect(narrowed.deliveries.some((x) => x.lane.projectId === 9)).toBe(false)
    expect(run(narrowed, [say('filter project Agent runtime')]).last).toEqual([{
      type: 'execute',
      command: { type: 'set_filters', patch: { projectId: 9, laneKey: null } },
    }])
  })

  it('has no project oracle at all when the snapshot carries no catalog', () => {
    const uncatalogued = voiceReducer(initialVoiceState(), {
      type: 'context',
      deliveries: fixtures,
      projectCatalog: [],
      selectedId: selected.id,
      selectionEpoch: 'epoch-1',
    }).state
    expect(run(uncatalogued, [say('filter project Agent runtime')]).last)
      .toEqual([{ type: 'notice', code: 'no_project_catalog' }])
  })
})

describe('agentModeVoiceMachine — offline hold and reconnect', () => {
  function offlineHeld() {
    const drafted = dictate(primed())
    return run(drafted.state, [{ type: 'connectivity', online: false }])
  }

  it('preserves the note while offline but refuses to confirm it', () => {
    const held = offlineHeld()
    expect(held.last).toEqual([{ type: 'notice', code: 'offline_hold' }])
    expect(held.state.note?.status).toBe('held_offline')
    expect(held.state.note?.binding.body).toBe('fixture 84 fails')

    const confirmed = voiceReducer(held.state, say('confirm', 'u2'))
    expect(confirmed.effects).toEqual([{ type: 'notice', code: 'offline_hold' }])
    expect(JSON.stringify(confirmed.effects)).not.toContain('submit_note')
  })

  it('holds the note when a confirm arrives first and the feed is already offline', () => {
    const drafted = dictate(primed())
    const offline: VoiceMachineState = { ...drafted.state, online: false }
    const confirmed = voiceReducer(offline, say('confirm', 'u2'))
    expect(confirmed.effects).toEqual([{ type: 'notice', code: 'offline_hold' }])
    expect(confirmed.state.note?.status).toBe('held_offline')
  })

  it('creates a newly typed offline note as held and never confirm-ready', () => {
    const offline = { ...primed(), online: false }
    const drafted = dictate(offline)
    expect(drafted.state.note?.status).toBe('held_offline')
    expect(drafted.last).toEqual([{
      type: 'note_preview',
      clientRequestId: drafted.state.note!.binding.clientRequestId,
    }])
    const confirmed = voiceReducer(drafted.state, say('confirm', 'offline-confirm'))
    expect(confirmed.effects).toEqual([{ type: 'notice', code: 'offline_hold' }])
    expect(JSON.stringify(confirmed.effects)).not.toContain('submit_note')
  })

  it('never auto-submits on reconnect: it reauthorizes and waits for a fresh confirm', () => {
    const held = offlineHeld()
    const back = voiceReducer(held.state, { type: 'connectivity', online: true })
    expect(back.effects).toEqual([{ type: 'notice', code: 'awaiting_revalidation' }])
    expect(back.state.note?.status).toBe('awaiting_revalidation')

    // Confirming before revalidation still does nothing.
    const early = voiceReducer(back.state, say('confirm', 'u2'))
    expect(early.effects).toEqual([{ type: 'notice', code: 'awaiting_revalidation' }])

    // A fresh authorized snapshot for the exact binding revalidates it —
    // silently, because the operator must confirm again themselves.
    const revalidated = voiceReducer(back.state, {
      type: 'context',
      projectCatalog: CATALOG,
      deliveries: fixtures,
      selectedId: selected.id,
      selectionEpoch: 'epoch-1',
    })
    expect(revalidated.effects).toEqual([])
    expect(revalidated.state.note?.status).toBe('preview')

    const confirmed = voiceReducer(revalidated.state, say('confirm', 'u3'))
    expect(confirmed.effects).toEqual([{
      type: 'submit_note', clientRequestId: revalidated.state.note!.binding.clientRequestId,
    }])
  })

  it.each<[string, (rows: readonly Delivery[]) => Delivery[], string]>([
    ['the comment capability is gone', () => withSelectedCapability(false), 'capability_revoked'],
    [
      'the attempt rotated while offline',
      (rows) => rows.map((r) => (
        r.id === selected.id ? { ...r, attempt: { ...r.attempt, id: 'attempt-812-2', number: 2 } } : r
      )),
      'attempt_changed',
    ],
  ])('refuses to revalidate on reconnect when %s', (_label, mutate, reason) => {
    const held = offlineHeld()
    const back = voiceReducer(held.state, { type: 'connectivity', online: true })
    expect(back.state.note?.status).toBe('awaiting_revalidation')
    const revalidated = voiceReducer(back.state, {
      type: 'context',
      deliveries: mutate(fixtures),
      projectCatalog: CATALOG,
      selectedId: selected.id,
      selectionEpoch: 'epoch-1',
    })
    expect(revalidated.effects).toEqual([{ type: 'note_discarded', reason }])
    expect(revalidated.state.note).toBeNull()
    // And a later confirm has nothing left to submit.
    expect(voiceReducer(revalidated.state, say('confirm', 'u9')).effects)
      .toEqual([{ type: 'notice', code: 'nothing_to_confirm' }])
  })

  it('discards a held note when reconnect brings a different authority', () => {
    const held = offlineHeld()
    const back = voiceReducer(held.state, { type: 'connectivity', online: true })
    const changed = voiceReducer(back.state, {
      type: 'context',
      projectCatalog: CATALOG,
      deliveries: fixtures,
      selectedId: selected.id,
      selectionEpoch: 'epoch-2',
    })
    expect(changed.effects).toEqual([{ type: 'note_discarded', reason: 'epoch_changed' }])
    expect(changed.state.note).toBeNull()
  })

  it('ignores a repeated connectivity event of the same value', () => {
    const held = offlineHeld()
    expect(voiceReducer(held.state, { type: 'connectivity', online: false }).effects).toEqual([])
  })
})

describe('PAI-808 owned sources — hygiene', () => {
  const OWNED = [
    '../../composables/agent-mode/agentModeVoiceIntent.ts',
    '../../composables/agent-mode/agentModeVoiceIntent.test.ts',
    '../../composables/agent-mode/agentModeVoiceMachine.ts',
    '../../composables/agent-mode/agentModeVoiceMachine.test.ts',
    '../../components/agent-mode/agentModeNarration.ts',
    '../../components/agent-mode/agentModeNarration.test.ts',
  ]

  function sourceOf(relative: string): Buffer {
    return readFileSync(new URL(relative, import.meta.url))
  }

  it.each(OWNED)('keeps %s plain reviewable UTF-8 text', (relative) => {
    const bytes = sourceOf(relative)
    // A single NUL anywhere makes git classify the blob as binary: no diff,
    // no review. It is what bb39414 had to undo.
    expect(bytes.includes(0)).toBe(false)
    // Round-trips through UTF-8 unchanged, so no lone surrogate or invalid
    // sequence is hiding in there either.
    expect(Buffer.from(bytes.toString('utf8'), 'utf8').equals(bytes)).toBe(true)
    // No stray C0 control characters beyond tab / newline / carriage return.
    expect(/[\u0001-\u0008\u000b\u000c\u000e-\u001f]/.test(bytes.toString('utf8'))).toBe(false)
  })

  it('writes the request-id separator as an escape, not as a raw byte', () => {
    const source = sourceOf('../../composables/agent-mode/agentModeVoiceMachine.ts').toString('utf8')
    expect(source).toContain(String.raw`join('\u0000')`)
    // The runtime string is still a real NUL, so every derived request id is
    // byte-for-byte what it was before the escape.
    expect(['a', 'b'].join('\u0000').charCodeAt(1)).toBe(0)
  })
})

describe('agentModeVoiceMachine — teardown', () => {
  it('clears transcript, candidates, and note text on reset', () => {
    const busy = run(primed(), [
      { type: 'partial', utteranceId: 'p1', text: 'note the secret' },
      say('select agent codex', 'u1'),
      say('note the fixture leaks', 'u2'),
    ])
    expect(busy.state.note).not.toBeNull()

    const cleared = voiceReducer(busy.state, { type: 'reset' })
    expect(cleared.effects).toEqual([{ type: 'note_discarded', reason: 'reset' }])
    expect(cleared.state).toEqual(initialVoiceState())
    expect(JSON.stringify(cleared.state)).not.toContain('fixture leaks')
    expect(JSON.stringify(cleared.state)).not.toContain('secret')
  })

  it('resets cleanly when there was nothing pending', () => {
    expect(voiceReducer(primed(), { type: 'reset' })).toEqual({ state: initialVoiceState(), effects: [] })
  })
})
