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

// PAI-808 — voice state machine (pure event reducer).
//
// Every voice and typed command in Agent Mode goes through THIS function.
// It performs no I/O: it returns the next state plus a list of EFFECTS that
// the composable executes. That is what makes exact-once, offline hold, and
// note invalidation testable without a mic, a socket, or a clock.
//
// The invariants it exists to hold:
//
//  - Partial segments move the visible draft and nothing else. (The current
//    ElevenLabs batch adapter only ever emits finals; the partial path exists
//    so a future streaming adapter needs no reducer change — this release
//    must not claim runtime interim transcription.)
//  - A final executes EXACTLY ONCE per stable utterance id. Repeats are
//    dropped by a bounded LRU; out-of-order finals are additionally dropped by
//    a monotonic sequence high-water mark, so LRU eviction can never
//    resurrect a stale utterance.
//  - A note preview binds delivery, issue, attempt, selection epoch, body,
//    and a stable client request id, and may only ever be raised against the
//    CURRENT selection while the server grants `comment` on it. Any drift in
//    that exact tuple — epoch, authorization, selection, issue rotation,
//    attempt rotation, capability revocation — DISCARDS the preview; a
//    background refresh of the same generation does not.
//  - Offline PRESERVES the note but refuses confirmation. Reconnect does not
//    submit anything: the binding must be reauthorized against a fresh
//    snapshot AND the operator must confirm again explicitly.
//  - Confirm is single-flight and idempotent. A failed submit keeps the SAME
//    client request id so a retry can only ever create one comment.

import type { Delivery } from '@/services/agentMode'
import type { ControlCommand, ControlTarget } from '@/services/agentModeControls'
import {
  parseVoiceCommand,
  resolveVoiceIntent,
  type UnsupportedVoiceControl,
  type VoiceCandidate,
  type VoiceCommand,
  type VoiceProjectRef,
  type VoiceRejection,
  type VoiceResolutionContext,
} from './agentModeVoiceIntent'

/** How many executed utterance ids are remembered for de-duplication. */
export const VOICE_UTTERANCE_LRU_LIMIT = 64

// ── Note binding ───────────────────────────────────────────────────────

export interface VoiceNoteBinding {
  deliveryId: string
  issueId: number
  issueKey: string
  attemptId: string | null
  /** Snapshot/authority epoch the binding was authorized against. */
  selectionEpoch: string
  /** The dictated text, exactly as spoken. Never narrated, never spoken back. */
  body: string
  /** Stable exact-once key; survives retries unchanged. */
  clientRequestId: string
  /** Utterance that produced the draft (also the request-id seed). */
  utteranceId: string
}

export type VoiceNoteStatus =
  | 'preview'
  | 'submitting'
  /** Kept, but not confirmable: the feed is offline. */
  | 'held_offline'
  /** Reconnected; waiting for a fresh authorized snapshot for this binding. */
  | 'awaiting_revalidation'
  /** Submit failed; a retry reuses the same client request id. */
  | 'failed'

export interface VoiceNoteState {
  binding: VoiceNoteBinding
  status: VoiceNoteStatus
}

export type VoiceNoteDiscardReason =
  | 'cancelled'
  | 'replaced'
  | 'submitted'
  | 'selection_changed'
  | 'authority_revoked'
  | 'epoch_changed'
  /** The bound row now reports a different issue. */
  | 'issue_changed'
  /** The bound row rolled over to a different attempt. */
  | 'attempt_changed'
  /** The server withdrew `comment` on the bound row. */
  | 'capability_revoked'
  | 'reset'

// ── Effects ────────────────────────────────────────────────────────────

export type VoiceNoticeCode =
  | VoiceRejection
  | 'no_clarification'
  | 'candidate_unavailable'
  | 'offline_hold'
  | 'awaiting_revalidation'
  | 'note_in_flight'
  | 'note_submit_failed'
  | 'nothing_to_confirm'
  | 'nothing_to_cancel'

export type VoiceEffect =
  /** Call the existing Agent Mode action for this command. */
  | { type: 'execute'; command: VoiceCommand }
  | { type: 'clarify'; candidates: readonly VoiceCandidate[]; matchCount: number; truncated: boolean }
  /** Queue payloads stay opaque so a blocked effect chain cannot retain raw
   * note text after the reducer state is scrubbed. */
  | { type: 'note_preview'; clientRequestId: string }
  | { type: 'submit_note'; clientRequestId: string }
  | { type: 'note_discarded'; reason: VoiceNoteDiscardReason }
  /** A machine-readable code — never prose, never transcript text. */
  | { type: 'notice'; code: VoiceNoticeCode }
  | { type: 'unsupported'; control: UnsupportedVoiceControl }

export interface VoiceMachineState {
  /** Visible interim text. Cleared as soon as its utterance finalizes. */
  draft: string
  draftUtteranceId: string | null
  /** Bounded LRU of executed utterance ids, oldest first. */
  executed: readonly string[]
  /** Highest executed sequence; older finals are stale by definition. */
  highWaterSequence: number
  candidates: readonly VoiceCandidate[]
  candidateMatchCount: number
  candidateTruncated: boolean
  note: VoiceNoteState | null
  online: boolean
  deliveries: readonly Delivery[]
  /** Authorized project catalog for THIS snapshot — selector-independent,
   * so a project filter can name a project with no visible row. */
  projectCatalog: readonly VoiceProjectRef[]
  /** Current ephemeral server target set. Every grant response replaces it. */
  controlTargets: readonly ControlTarget[]
  /** Visible persisted challenge bound to its original safe issue label. */
  controlChallenge: { command: ControlCommand; issueKey: string; phrase: string } | null
  selectedId: string | null
  selectionEpoch: string
}

export type VoiceEvent =
  /** A new authorized snapshot / selection / ACL epoch. */
  | {
    type: 'context'
    deliveries: readonly Delivery[]
    projectCatalog: readonly VoiceProjectRef[]
    controlTargets?: readonly ControlTarget[]
    controlChallenge?: { command: ControlCommand; issueKey: string; phrase: string } | null
    selectedId: string | null
    selectionEpoch: string
  }
  | { type: 'partial'; utteranceId: string; text: string }
  /** The authorized resolver vocabulary changed while selection/note
   * authority may still be valid. Interim text and numbered candidates are
   * context-relative, so discard only those ephemeral resolver fields. */
  | { type: 'resolver_context_changed' }
  | { type: 'final'; utteranceId: string; text: string; sequence?: number }
  /** Typed fallback — identical path, same de-duplication. */
  | { type: 'typed'; utteranceId: string; text: string }
  | { type: 'connectivity'; online: boolean }
  | { type: 'note_settled'; clientRequestId: string; ok: boolean }
  /** Unmount / teardown. */
  | { type: 'reset' }

export interface VoiceReducerResult {
  state: VoiceMachineState
  effects: VoiceEffect[]
}

export function initialVoiceState(overrides: Partial<VoiceMachineState> = {}): VoiceMachineState {
  return {
    draft: '',
    draftUtteranceId: null,
    executed: [],
    highWaterSequence: 0,
    candidates: [],
    candidateMatchCount: 0,
    candidateTruncated: false,
    note: null,
    online: true,
    deliveries: [],
    projectCatalog: [],
    controlTargets: [],
    controlChallenge: null,
    selectedId: null,
    selectionEpoch: '',
    ...overrides,
  }
}

// ── Exact-once client request id ───────────────────────────────────────

function djb2(value: string): number {
  let hash = 5381
  for (let i = 0; i < value.length; i += 1) hash = ((hash << 5) + hash + value.charCodeAt(i)) >>> 0
  return hash >>> 0
}

function fnv1a(value: string): number {
  let hash = 0x811c9dc5
  for (let i = 0; i < value.length; i += 1) {
    hash ^= value.charCodeAt(i)
    hash = Math.imul(hash, 0x01000193) >>> 0
  }
  return hash >>> 0
}

function hex8(value: number): string {
  return value.toString(16).padStart(8, '0')
}

/**
 * Deterministic exact-once key for the internal-note create.
 *
 * It is a pure function of the utterance, the bound delivery, the authority
 * epoch, and the body, so the SAME preview always retries under the SAME key
 * (one comment) while a different body or a different delivery necessarily
 * produces a different key (never a server-side conflict from us). The body
 * only enters as a hash: the id itself travels to the server and must not
 * carry dictated text.
 */
export function buildNoteRequestId(parts: {
  utteranceId: string
  deliveryId: string
  selectionEpoch: string
  body: string
}): string {
  const seed = [parts.utteranceId, parts.deliveryId, parts.selectionEpoch, parts.body].join('\u0000')
  return `amv1-${hex8(djb2(seed))}${hex8(fnv1a(seed))}`
}

// ── Helpers ────────────────────────────────────────────────────────────

function rememberUtterance(executed: readonly string[], utteranceId: string): string[] {
  const next = executed.filter((id) => id !== utteranceId)
  next.push(utteranceId)
  return next.length > VOICE_UTTERANCE_LRU_LIMIT ? next.slice(next.length - VOICE_UTTERANCE_LRU_LIMIT) : next
}

function clearedCandidates(state: VoiceMachineState): VoiceMachineState {
  if (state.candidates.length === 0 && state.candidateMatchCount === 0 && !state.candidateTruncated) return state
  return { ...state, candidates: [], candidateMatchCount: 0, candidateTruncated: false }
}

function contextOf(state: VoiceMachineState): VoiceResolutionContext {
  return {
    deliveries: state.deliveries,
    selectedId: state.selectedId,
    projectCatalog: state.projectCatalog,
    controlTargets: state.controlTargets,
    controlChallenge: state.controlChallenge,
  }
}

function findDelivery(state: VoiceMachineState, deliveryId: string): Delivery | null {
  return state.deliveries.find((d) => d.id === deliveryId) ?? null
}

function notice(state: VoiceMachineState, code: VoiceNoticeCode): VoiceReducerResult {
  return { state, effects: [{ type: 'notice', code }] }
}

/**
 * The single exact-authority check for a note binding.
 *
 * A preview is confirmable only while ALL SIX facts it was raised under still
 * hold: the authority epoch, the row's authorization, the persistent
 * selection, the row's issue, the row's attempt, and the server-granted
 * `comment` capability. Confirm and every incoming snapshot run the same
 * check, so an operator can never confirm something a refresh would have
 * thrown away. The order is fixed so the reported reason is deterministic.
 */
function bindingMismatch(
  binding: VoiceNoteBinding,
  deliveries: readonly Delivery[],
  selectedId: string | null,
  selectionEpoch: string,
): VoiceNoteDiscardReason | null {
  if (binding.selectionEpoch !== selectionEpoch) return 'epoch_changed'
  const delivery = deliveries.find((d) => d.id === binding.deliveryId) ?? null
  if (!delivery) return 'authority_revoked'
  if (selectedId !== binding.deliveryId) return 'selection_changed'
  if (delivery.issueId !== binding.issueId) return 'issue_changed'
  if (delivery.attempt.id !== binding.attemptId) return 'attempt_changed'
  if (delivery.capabilities.comment !== true) return 'capability_revoked'
  return null
}

// ── Command handling ───────────────────────────────────────────────────

function draftNote(
  state: VoiceMachineState,
  utteranceId: string,
  deliveryId: string,
  body: string,
): VoiceReducerResult {
  // A draft_note command only ever names the CURRENT selection, and only for
  // a delivery the server grants `comment` on: `resolveVoiceIntent` refuses
  // anything else outright, and `bindingMismatch` re-checks that exact tuple
  // at confirm and on every snapshot. This lookup is the null guard for the
  // row those two agree on.
  const delivery = findDelivery(state, deliveryId)
  if (!delivery) return notice(clearedCandidates(state), 'no_match')

  const binding: VoiceNoteBinding = {
    deliveryId: delivery.id,
    issueId: delivery.issueId,
    issueKey: delivery.issueKey,
    attemptId: delivery.attempt.id,
    selectionEpoch: state.selectionEpoch,
    body,
    clientRequestId: buildNoteRequestId({
      utteranceId,
      deliveryId: delivery.id,
      selectionEpoch: state.selectionEpoch,
      body,
    }),
    utteranceId,
  }
  const effects: VoiceEffect[] = []
  // A second dictation replaces the first: the operator only ever confirms
  // the preview they can currently see.
  if (state.note && state.note.status !== 'submitting') effects.push({ type: 'note_discarded', reason: 'replaced' })
  effects.push({ type: 'note_preview', clientRequestId: binding.clientRequestId })
  return {
    state: {
      ...clearedCandidates(state),
      note: { binding, status: state.online ? 'preview' : 'held_offline' },
    },
    effects,
  }
}

function confirmNote(state: VoiceMachineState): VoiceReducerResult {
  const note = state.note
  if (!note) return notice(state, 'nothing_to_confirm')
  // Single-flight: a double confirm cannot produce a second request.
  if (note.status === 'submitting') return { state, effects: [] }
  if (note.status === 'awaiting_revalidation') return notice(state, 'awaiting_revalidation')
  if (!state.online) {
    return {
      state: { ...state, note: { ...note, status: 'held_offline' } },
      effects: [{ type: 'notice', code: 'offline_hold' }],
    }
  }
  // Defensive re-check of the exact binding: selection, authority, attempt,
  // or capability may have moved without a context event having reached us.
  const mismatch = bindingMismatch(note.binding, state.deliveries, state.selectedId, state.selectionEpoch)
  if (mismatch) {
    return {
      state: { ...state, note: null },
      effects: [
        { type: 'note_discarded', reason: mismatch },
        { type: 'notice', code: 'no_match' },
      ],
    }
  }
  return {
    state: { ...state, note: { ...note, status: 'submitting' } },
    effects: [{ type: 'submit_note', clientRequestId: note.binding.clientRequestId }],
  }
}

function cancelNote(state: VoiceMachineState): VoiceReducerResult {
  const note = state.note
  if (!note) return notice(state, 'nothing_to_cancel')
  // An in-flight create cannot be un-posted from the client; saying so is
  // more honest than pretending the preview never existed.
  if (note.status === 'submitting') return notice(state, 'note_in_flight')
  return { state: { ...state, note: null }, effects: [{ type: 'note_discarded', reason: 'cancelled' }] }
}

function applyCommand(state: VoiceMachineState, utteranceId: string, command: VoiceCommand): VoiceReducerResult {
  switch (command.type) {
    case 'draft_note':
      return draftNote(state, utteranceId, command.deliveryId, command.body)
    case 'confirm_note':
      return confirmNote(state)
    case 'cancel_note':
      return cancelNote(state)
    default:
      // Any other command ends an open clarification.
      return { state: clearedCandidates(state), effects: [{ type: 'execute', command }] }
  }
}

function chooseCandidate(state: VoiceMachineState, index: number): VoiceReducerResult {
  if (state.candidates.length === 0) return notice(state, 'no_clarification')
  const candidate = state.candidates.find((c) => c.index === index)
  if (!candidate) return notice(state, 'no_clarification')
  // A candidate that left the authorized set is never silently selected.
  if (!findDelivery(state, candidate.deliveryId)) return notice(clearedCandidates(state), 'candidate_unavailable')
  return {
    state: clearedCandidates(state),
    effects: [{ type: 'execute', command: { type: 'select', deliveryId: candidate.deliveryId } }],
  }
}

function interpret(state: VoiceMachineState, utteranceId: string, text: string): VoiceReducerResult {
  // Bare numerals are gated on the highest OFFERED number, not on how many
  // candidates survived an ACL change — "three" keeps meaning candidate 3.
  const highestCandidate = state.candidates.reduce((max, c) => Math.max(max, c.index), 0)
  const intent = parseVoiceCommand(text, { candidateCount: highestCandidate })
  if (intent.kind === 'candidate') return chooseCandidate(state, intent.index)

  const resolution = resolveVoiceIntent(intent, contextOf(state))
  switch (resolution.kind) {
    case 'command':
      return applyCommand(state, utteranceId, resolution.command)
    case 'clarify':
      return {
        state: {
          ...state,
          candidates: resolution.candidates,
          candidateMatchCount: resolution.matchCount,
          candidateTruncated: resolution.truncated,
        },
        effects: [{
          type: 'clarify',
          candidates: resolution.candidates,
          matchCount: resolution.matchCount,
          truncated: resolution.truncated,
        }],
      }
    case 'unsupported':
      // Never crosses into PAI-809 controls; the refusal is the whole effect.
      return { state, effects: [{ type: 'unsupported', control: resolution.control }] }
    case 'rejected':
      return notice(state, resolution.reason)
  }
}

// ── Event handling ─────────────────────────────────────────────────────

function handleContext(
  state: VoiceMachineState,
  deliveries: readonly Delivery[],
  projectCatalog: readonly VoiceProjectRef[],
  controlTargets: readonly ControlTarget[],
  controlChallenge: { command: ControlCommand; issueKey: string; phrase: string } | null,
  selectedId: string | null,
  selectionEpoch: string,
): VoiceReducerResult {
  const epochChanged = selectionEpoch !== state.selectionEpoch
  const authorized = new Set(deliveries.map((d) => d.id))
  const effects: VoiceEffect[] = []

  // Candidates belong to one snapshot generation. A reset drops them all;
  // otherwise only the rows that lost authorization disappear — surviving
  // rows KEEP their spoken number so "two" never changes meaning mid-choice.
  const candidates = epochChanged ? [] : state.candidates.filter((c) => authorized.has(c.deliveryId))

  let note = state.note
  if (note) {
    const discard = bindingMismatch(note.binding, deliveries, selectedId, selectionEpoch)
    if (discard) {
      note = null
      effects.push({ type: 'note_discarded', reason: discard })
    } else if (note.status === 'awaiting_revalidation') {
      // Reauthorized against a fresh snapshot — still requires a NEW confirm.
      note = { ...note, status: 'preview' }
    }
  }

  return {
    state: {
      ...state,
      deliveries,
      projectCatalog,
      controlTargets,
      controlChallenge,
      selectedId,
      selectionEpoch,
      // Interim text is target-relative. A selection/authority epoch change
      // must never let a partial note or command begun under A appear under B.
      draft: epochChanged ? '' : state.draft,
      draftUtteranceId: epochChanged ? null : state.draftUtteranceId,
      candidates,
      candidateMatchCount: candidates.length === 0 ? 0 : state.candidateMatchCount,
      candidateTruncated: candidates.length === 0 ? false : state.candidateTruncated,
      note,
    },
    effects,
  }
}

function handleConnectivity(state: VoiceMachineState, online: boolean): VoiceReducerResult {
  if (online === state.online) return { state, effects: [] }
  const next: VoiceMachineState = { ...state, online }
  const note = state.note
  if (!note || note.status === 'submitting') return { state: next, effects: [] }

  if (!online) {
    if (note.status === 'held_offline') return { state: next, effects: [] }
    // The note survives the outage; only confirmation is withheld.
    return {
      state: { ...next, note: { ...note, status: 'held_offline' } },
      effects: [{ type: 'notice', code: 'offline_hold' }],
    }
  }
  if (note.status !== 'held_offline') return { state: next, effects: [] }
  // Reconnect NEVER auto-submits: reauthorize the exact binding first.
  return {
    state: { ...next, note: { ...note, status: 'awaiting_revalidation' } },
    effects: [{ type: 'notice', code: 'awaiting_revalidation' }],
  }
}

function handleFinal(
  state: VoiceMachineState,
  utteranceId: string,
  text: string,
  sequence: number | undefined,
): VoiceReducerResult {
  const cleared: VoiceMachineState = state.draftUtteranceId === utteranceId
    ? { ...state, draft: '', draftUtteranceId: null }
    : state

  // Replay of an already-executed utterance.
  if (state.executed.includes(utteranceId)) return { state: cleared, effects: [] }
  // Out-of-order / late final from before the last executed one. This holds
  // even after the id fell out of the LRU.
  if (sequence !== undefined && sequence < state.highWaterSequence) return { state: cleared, effects: [] }

  const marked: VoiceMachineState = {
    ...cleared,
    executed: rememberUtterance(cleared.executed, utteranceId),
    highWaterSequence: sequence !== undefined ? Math.max(state.highWaterSequence, sequence) : state.highWaterSequence,
  }
  return interpret(marked, utteranceId, text)
}

/**
 * The one voice/typed reducer. Pure: no I/O, no clock, no randomness.
 * Returns the next state plus the effects the caller must perform.
 */
export function voiceReducer(state: VoiceMachineState, event: VoiceEvent): VoiceReducerResult {
  switch (event.type) {
    case 'context':
      return handleContext(
        state,
        event.deliveries,
        event.projectCatalog,
        event.controlTargets ?? state.controlTargets,
        event.controlChallenge === undefined ? state.controlChallenge : event.controlChallenge,
        event.selectedId,
        event.selectionEpoch,
      )

    case 'partial': {
      // Visible draft only — a partial can never execute anything, and a
      // late partial for an already-executed utterance is ignored outright.
      if (state.executed.includes(event.utteranceId)) return { state, effects: [] }
      return { state: { ...state, draft: event.text, draftUtteranceId: event.utteranceId }, effects: [] }
    }

    case 'resolver_context_changed':
      return {
        state: {
          ...clearedCandidates(state),
          draft: '',
          draftUtteranceId: null,
        },
        effects: [],
      }

    case 'final':
      return handleFinal(state, event.utteranceId, event.text, event.sequence)

    case 'typed':
      return handleFinal(state, event.utteranceId, event.text, undefined)

    case 'connectivity':
      return handleConnectivity(state, event.online)

    case 'note_settled': {
      const note = state.note
      if (!note || note.binding.clientRequestId !== event.clientRequestId) return { state, effects: [] }
      if (event.ok) {
        return { state: { ...state, note: null }, effects: [{ type: 'note_discarded', reason: 'submitted' }] }
      }
      // Retryable under the SAME client request id, so a retry that races a
      // slow success still yields exactly one comment.
      return {
        state: { ...state, note: { ...note, status: 'failed' } },
        effects: [{ type: 'notice', code: 'note_submit_failed' }],
      }
    }

    case 'reset': {
      const effects: VoiceEffect[] = state.note ? [{ type: 'note_discarded', reason: 'reset' }] : []
      return { state: initialVoiceState(), effects }
    }
  }
}
