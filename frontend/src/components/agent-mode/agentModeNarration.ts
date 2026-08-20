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

// PAI-808 — structured, safe narration contract (pure).
//
// Two outputs, deliberately different in power:
//
//  FACTS — what the conversation column may SHOW. Every fact is an i18n key
//  plus enum/count/formatted-value parameters derived from server-authored
//  structured fields: stage and stage status, activity KIND, freshness,
//  attention reason, blocker count, trust/suppression, and the estimate —
//  and the estimate only through the one shared `estimateView` seam, which
//  already withholds every exact value while the snapshot is degraded and
//  reduces low confidence to a range.
//
//  SPEECH — what may be SPOKEN. A speech request carries an enum template
//  plus opaque identities (delivery, delivery revision, trust revision,
//  candidate ids) and NOTHING else. The server reauthorizes those ids and
//  reloads the facts itself, so no caller-authored text — and in particular
//  no dictated note body and no free-text activity prose — can ever reach
//  TTS. There is no template that claims "on track" or promises completion.
//
// Free-text server prose (activity text, status text, blocker text, evidence
// summaries, ticket titles) is NEVER a narration parameter. `factsExcludeProse`
// enforces that at runtime as well as in tests.

import type { Delivery, EstimateConfidence } from '@/services/agentMode'
import { estimateView, relativeReported } from './agentModePresentation'

export type NarrationLocale = 'en' | 'de'

export type NarrationFactKind =
  | 'stage'
  | 'stage_position'
  | 'stage_status'
  | 'activity'
  | 'freshness'
  | 'reported'
  | 'attention'
  | 'blockers'
  | 'estimate_percent'
  | 'estimate_landing'
  | 'estimate_range'
  | 'estimate_withheld'
  | 'confidence'
  | 'reporter'
  | 'suppression'
  | 'disagreement'
  | 'retained'

export interface NarrationFact {
  kind: NarrationFactKind
  /** i18n message key; the view owns rendering. */
  key: string
  params: Readonly<Record<string, string | number>>
}

/** The complete set of TTS templates. Each one is a server-authored,
 * fact-loading message; none of them can express reassurance, an "on track"
 * claim, or a completion promise. */
export const NARRATION_SPEECH_TEMPLATES = [
  'delivery_status',
  'portfolio_status',
  'clarify_candidates',
  'note_ready',
  'note_saved',
  'note_cancelled',
  'note_offline_hold',
  'command_unsupported',
  'command_not_understood',
  'no_match',
] as const

export type NarrationSpeechTemplate = (typeof NARRATION_SPEECH_TEMPLATES)[number]

export interface NarrationSpeechRequest {
  template: NarrationSpeechTemplate
  locale: NarrationLocale
  /** Opaque authorized identities only — never text. */
  deliveryId: string | null
  deliveryRevision: string | null
  trustRevision: string | null
  candidateIds: readonly string[]
}

export interface DeliveryNarration {
  facts: NarrationFact[]
  /** Null when nothing may safely be spoken (e.g. a degraded snapshot). */
  speech: NarrationSpeechRequest | null
}

export interface NarrationOptions {
  locale: NarrationLocale
  /** Server clock (browser now + skew) used for relative/remaining values. */
  serverNowMs: number
  /** Retained-not-current snapshot: no exact estimate values at all. */
  degraded?: boolean
  /** Display locale for date/number formatting; defaults to `locale`. */
  displayLocale?: string
}

// ── Safety ─────────────────────────────────────────────────────────────

/** Formatted values only: words, numbers, enum tokens, and the punctuation
 * the shared formatters emit ("16:30–17:00", "1 h 20 min", "<1 min",
 * "−12 min", "owner_override"). Anything longer or stranger is prose. */
const SAFE_PARAM = /^[\p{L}\p{N} _.,:%()+\-–—−<>/·']{1,64}$/u
/** Server tokens (suppression / disagreement codes) are enum-shaped. */
const SAFE_CODE = /^[a-z][a-z0-9_]{0,31}$/

const MAX_SPEECH_CANDIDATES = 3
const SAFE_IDENTITY = /^[A-Za-z0-9._:@|~_-]{1,128}$/

function paramsAreSafe(params: Readonly<Record<string, string | number>>): boolean {
  return Object.values(params).every((value) => {
    if (typeof value === 'number') return Number.isFinite(value)
    return SAFE_PARAM.test(value)
  })
}

/** Every free-text field the backend may fill with human or agent prose. */
export function narrationProseFields(d: Delivery): string[] {
  const values = [
    d.title,
    d.statusText ?? '',
    d.activity.text ?? '',
    d.attention.reason ?? '',
    ...d.blockers.map((b) => b.text),
    ...d.stages.flatMap((s) => [s.activity ?? '', s.label ?? '', ...s.blockers.map((b) => b.text)]),
    ...d.evidence.flatMap((e) => [e.summary ?? '', e.label ?? '']),
    ...d.handoffs.map((h) => h.summary ?? ''),
    d.trust.basis ?? '',
    d.progress?.basis ?? '',
    d.eta?.basis ?? '',
  ]
  return values.map((v) => v.trim()).filter((v) => v !== '')
}

function containsProse(params: Readonly<Record<string, string | number>>, prose: readonly string[]): boolean {
  const haystack = Object.values(params)
    .filter((v): v is string => typeof v === 'string')
    .map((v) => v.toLowerCase())
  return prose.some((text) => haystack.some((value) => value.includes(text.toLowerCase())))
}

/** True when no fact parameter carries server prose. Exported so the view and
 * the tests can assert the invariant on any fact list. */
export function factsExcludeProse(facts: readonly NarrationFact[], d: Delivery): boolean {
  const prose = narrationProseFields(d)
  return facts.every((fact) => !containsProse(fact.params, prose))
}

/** Structural guard for anything about to be sent to `/voice/speak`. */
export function isSafeSpeechRequest(request: NarrationSpeechRequest): boolean {
  if (!(NARRATION_SPEECH_TEMPLATES as readonly string[]).includes(request.template)) return false
  if (request.locale !== 'en' && request.locale !== 'de') return false
  if (request.candidateIds.length > MAX_SPEECH_CANDIDATES) return false
  const identities = [request.deliveryId, request.deliveryRevision, request.trustRevision, ...request.candidateIds]
  return identities.every((value) => value === null || SAFE_IDENTITY.test(value))
}

// ── Known enum vocabularies ────────────────────────────────────────────

const ATTENTION_REASONS = new Set([
  'blocked', 'waiting_needs_input', 'failed_needs_retry', 'stale_no_signal',
  'unknown_reporter', 'deployed_unverified', 'unverified', 'other',
])

const CONFIDENCES: readonly EstimateConfidence[] = ['high', 'medium', 'low', 'none']

const STAGE_KEYS = ['specification', 'implementation', 'qa', 'deployment', 'verification', 'unknown'] as const
const STAGE_STATUSES = [
  'pending', 'active', 'waiting', 'blocked', 'failed', 'cancelled', 'draft_ready',
  'succeeded', 'not_applicable', 'unknown',
] as const
const ACTIVITY_KINDS = [
  'working', 'testing', 'deploying', 'verifying', 'waiting', 'blocked', 'idle', 'unknown',
] as const
const FRESHNESS_STATES = ['fresh', 'aging', 'stale', 'unknown'] as const
const WITHHELD_REASONS = [
  'blocked', 'waiting', 'stale', 'low_confidence', 'suppressed', 'invalid', 'untrusted', 'none', 'offline',
] as const
const REPORTER_KINDS = ['agent_run', 'external', 'user', 'system', 'unknown'] as const

/**
 * Every i18n key this module can emit. The view/i18n owner wires these; the
 * suite asserts nothing outside this list is ever produced.
 *
 * Keys under `agentMode.narration.blockers|suppressed|disagreement|
 * confidence.*|reporter.*`, plus `agentMode.stageStatus.cancelled|draft_ready`,
 * do not exist in the PAI-806 bundle yet and are this slice's hand-off to the
 * i18n owner; every other key is reused as-is.
 */
export const NARRATION_I18N_KEYS: readonly string[] = [
  ...STAGE_KEYS.map((k) => `agentMode.stage.${k}`),
  'agentMode.detail.position',
  ...STAGE_STATUSES.map((k) => `agentMode.stageStatus.${k}`),
  ...ACTIVITY_KINDS.map((k) => `agentMode.activity.${k}`),
  ...FRESHNESS_STATES.map((k) => `agentMode.freshness.${k}`),
  'agentMode.live.updated',
  ...[...ATTENTION_REASONS].map((k) => `agentMode.aggregate.reason.${k}`),
  'agentMode.narration.blockers',
  'agentMode.estimate.percent',
  'agentMode.estimate.lands',
  'agentMode.narration.selectionEta',
  'agentMode.narration.selectionRange',
  ...WITHHELD_REASONS.map((k) => `agentMode.estimate.withheld.${k}`),
  ...CONFIDENCES.map((k) => `agentMode.narration.confidence.${k}`),
  ...REPORTER_KINDS.map((k) => `agentMode.narration.reporter.${k}`),
  'agentMode.narration.suppressed',
  'agentMode.narration.disagreement',
  'agentMode.card.retained',
]

const KNOWN_KEYS = new Set(NARRATION_I18N_KEYS)

// ── Fact building ──────────────────────────────────────────────────────

function fact(kind: NarrationFactKind, key: string, params: Record<string, string | number> = {}): NarrationFact {
  return { kind, key, params }
}

function currentStageStatus(d: Delivery): (typeof STAGE_STATUSES)[number] | null {
  const row = d.stages.find((s) => s.key === d.stage.key)
  if (!row) return null
  return (STAGE_STATUSES as readonly string[]).includes(row.status) ? row.status : 'unknown'
}

function safeCodes(codes: readonly string[]): string[] {
  return codes.filter((code) => SAFE_CODE.test(code))
}

function estimateFacts(d: Delivery, opts: NarrationOptions): NarrationFact[] {
  const view = estimateView(d, opts.displayLocale ?? opts.locale, opts.serverNowMs, opts.degraded ?? false)
  const { presentation } = view
  const facts: NarrationFact[] = []

  if (presentation.showPercent && presentation.percent != null) {
    facts.push(fact('estimate_percent', 'agentMode.estimate.percent', { n: presentation.percent }))
  }
  if (presentation.showEta && presentation.rangeOnly && view.rangeLabel) {
    // Low confidence is bounds-only: never a point landing, never a midpoint.
    facts.push(fact('estimate_range', 'agentMode.narration.selectionRange', { range: view.rangeLabel }))
  } else if (presentation.showEta && view.landingLabel) {
    facts.push(view.remainingLabel
      ? fact('estimate_landing', 'agentMode.narration.selectionEta', {
        time: view.landingLabel,
        remaining: view.remainingLabel,
      })
      : fact('estimate_landing', 'agentMode.estimate.lands', { time: view.landingLabel }))
  }

  const percentWithheld = presentation.percentReason !== 'ok' ? presentation.percentReason : null
  const etaWithheld = presentation.etaReason !== 'ok' ? presentation.etaReason : null
  const reasons = percentWithheld && percentWithheld === etaWithheld
    ? [percentWithheld]
    : [percentWithheld, etaWithheld].filter((r): r is NonNullable<typeof r> => r != null)
  for (const reason of reasons) {
    facts.push(fact('estimate_withheld', `agentMode.estimate.withheld.${reason}`))
  }
  return facts
}

/**
 * Narration facts for one delivery, in reading order. Only server-authored
 * template facts; the caller renders them through i18n.
 */
export function buildDeliveryNarration(d: Delivery, opts: NarrationOptions): DeliveryNarration {
  const degraded = opts.degraded ?? false
  const facts: NarrationFact[] = []

  facts.push(fact('stage', `agentMode.stage.${d.stage.key}`))
  if (d.stage.index != null && d.stage.total != null && d.stage.total > 0) {
    facts.push(fact('stage_position', 'agentMode.detail.position', { i: d.stage.index, n: d.stage.total }))
  }
  const stageStatus = currentStageStatus(d)
  if (stageStatus) facts.push(fact('stage_status', `agentMode.stageStatus.${stageStatus}`))

  facts.push(fact('activity', `agentMode.activity.${d.activity.kind}`))
  facts.push(fact('freshness', `agentMode.freshness.${d.freshness.state}`))

  const reported = relativeReported(d, opts.displayLocale ?? opts.locale, opts.serverNowMs)
  if (reported) facts.push(fact('reported', 'agentMode.live.updated', { when: reported }))

  if (d.attention.level > 0) {
    const reason = d.attention.reason && ATTENTION_REASONS.has(d.attention.reason) ? d.attention.reason : 'other'
    facts.push(fact('attention', `agentMode.aggregate.reason.${reason}`))
  }
  if (d.blockers.length > 0) {
    // The COUNT is a fact; the blocker text is prose and stays out.
    facts.push(fact('blockers', 'agentMode.narration.blockers', { n: d.blockers.length }))
  }

  facts.push(...estimateFacts(d, opts))

  if (d.trust.confidence !== 'none') {
    facts.push(fact('confidence', `agentMode.narration.confidence.${d.trust.confidence}`))
  }
  facts.push(fact('reporter', `agentMode.narration.reporter.${d.trust.reporterKind}`))

  const suppression = safeCodes([...d.suppressionCodes, ...(d.trust.suppression ? [d.trust.suppression] : [])])
  if (suppression.length > 0) {
    facts.push(fact('suppression', 'agentMode.narration.suppressed', {
      n: suppression.length,
      codes: suppression.join(' · '),
    }))
  }
  const disagreement = safeCodes(d.disagreementCodes)
  if (disagreement.length > 0) {
    facts.push(fact('disagreement', 'agentMode.narration.disagreement', { n: disagreement.length }))
  }
  if (degraded) facts.push(fact('retained', 'agentMode.card.retained'))

  const prose = narrationProseFields(d)
  const safe = facts.filter((f) => KNOWN_KEYS.has(f.key) && paramsAreSafe(f.params) && !containsProse(f.params, prose))

  // A degraded snapshot is never spoken: the server would voice current
  // facts that this client cannot vouch for.
  const speech = degraded
    ? null
    : {
      template: 'delivery_status' as const,
      locale: opts.locale,
      deliveryId: d.id,
      deliveryRevision: d.deliveryRevision,
      trustRevision: d.trustRevision ?? d.trust.trustRevision,
      candidateIds: [],
    }
  return { facts: safe, speech: speech && isSafeSpeechRequest(speech) ? speech : null }
}

// ── Speech requests ────────────────────────────────────────────────────

function speech(
  template: NarrationSpeechTemplate,
  locale: NarrationLocale,
  parts: Partial<Omit<NarrationSpeechRequest, 'template' | 'locale'>> = {},
): NarrationSpeechRequest | null {
  const request: NarrationSpeechRequest = {
    template,
    locale,
    deliveryId: parts.deliveryId ?? null,
    deliveryRevision: parts.deliveryRevision ?? null,
    trustRevision: parts.trustRevision ?? null,
    candidateIds: parts.candidateIds ?? [],
  }
  return isSafeSpeechRequest(request) ? request : null
}

export function buildPortfolioSpeech(locale: NarrationLocale): NarrationSpeechRequest | null {
  return speech('portfolio_status', locale)
}

/**
 * Clarification read-back. Only the candidate IDENTITIES travel; the server
 * loads the keys it speaks. Titles stay in the visual numbered list.
 */
export function buildClarificationSpeech(
  candidates: readonly { deliveryId: string }[],
  locale: NarrationLocale,
): NarrationSpeechRequest | null {
  if (candidates.length === 0) return null
  return speech('clarify_candidates', locale, {
    candidateIds: candidates.slice(0, MAX_SPEECH_CANDIDATES).map((c) => c.deliveryId),
  })
}

export type NoteSpeechEvent = 'ready' | 'saved' | 'cancelled' | 'offline_hold'

const NOTE_TEMPLATES: Record<NoteSpeechEvent, NarrationSpeechTemplate> = {
  ready: 'note_ready',
  saved: 'note_saved',
  cancelled: 'note_cancelled',
  offline_hold: 'note_offline_hold',
}

/**
 * Note read-back — "Internal note ready for PAI-808; confirm or cancel" is
 * assembled by the SERVER from the delivery id. The dictated body is not a
 * parameter here and cannot become one: the request has no text field.
 */
export function buildNoteSpeech(
  event: NoteSpeechEvent,
  note: { deliveryId: string },
  locale: NarrationLocale,
): NarrationSpeechRequest | null {
  return speech(NOTE_TEMPLATES[event], locale, { deliveryId: note.deliveryId })
}

export type NarrationNotice = 'unsupported' | 'not_understood' | 'no_match'

const NOTICE_TEMPLATES: Record<NarrationNotice, NarrationSpeechTemplate> = {
  unsupported: 'command_unsupported',
  not_understood: 'command_not_understood',
  no_match: 'no_match',
}

export function buildNoticeSpeech(notice: NarrationNotice, locale: NarrationLocale): NarrationSpeechRequest | null {
  return speech(NOTICE_TEMPLATES[notice], locale)
}
