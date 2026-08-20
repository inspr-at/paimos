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

// PAI-808 — deterministic EN/DE voice grammar + resolver (pure).
//
// This module is the whole language surface of Agent Mode voice. It has no
// LLM, no fuzzy scoring, no network, no Vue, and no side effects: the same
// text plus the same authorized snapshot always produces the same intent and
// the same resolution.
//
// Three hard contracts live here:
//
//  1. SUPPORTED INTENTS ONLY. Selection, travel, filters, semantic zoom,
//     status read-back, and the internal-note lifecycle. Supervisory verbs
//     (approve / pause / resume / cancel-job / priority) are recognized only
//     so they can be REFUSED — they never become a command and never reach
//     the PAI-809 control surface.
//  2. PRONOUNS ARE EXACT. "this" / "das" resolve to the current persistent
//     authorized selection or to nothing at all. They never guess.
//  3. AMBIGUITY IS BOUNDED AND ORDERED. At most MAX_VOICE_CANDIDATES
//     candidates, taken from the current authorized deliveries in the
//     canonical Agent Mode order, with the true match count reported so the
//     UI can say "3 of 7" instead of silently truncating.
//
// A dictated note body is NEVER re-parsed. Once a note prefix matched, the
// remaining RAW text (original case, diacritics, punctuation) is the body and
// nothing inside it can turn into a command.

import type { Delivery } from '@/services/agentMode'
import { compareDeliveries } from './agentModeOrdering'
import type { HealthFilter } from './agentModeFilters'

/** Clarification never offers more than three authorized deliveries. */
export const MAX_VOICE_CANDIDATES = 3

// ── Normalization ──────────────────────────────────────────────────────
//
// Speech-to-text output is unstable across locales: it emits "nächste" or
// "naechste", "PAI-808" or "PAI 808" or "pai808", and full-width or
// composed Unicode. Everything folds through ONE deterministic pipeline so
// the grammar tables can stay plain ASCII.

const COMBINING_MARKS = /\p{M}+/gu
const LIGATURES: Array<[RegExp, string]> = [
  [/ß/g, 'ss'],
  [/æ/g, 'ae'],
  [/œ/g, 'oe'],
  [/ø/g, 'o'],
  [/đ/g, 'd'],
  [/ł/g, 'l'],
]

/**
 * NFKD + diacritic + ligature folding, then the German transcription fold
 * (`ae`/`oe`/`ue` → `a`/`o`/`u`) so "zurück", "zurueck", and "zuruck" are one
 * word. The fold is applied identically to input and to the vocabulary /
 * delivery fields it is compared against, so it can never introduce a match
 * that the unfolded forms would not have produced on both sides.
 */
function foldCore(value: string): string {
  let out = value.normalize('NFKD').replace(COMBINING_MARKS, '').toLowerCase()
  for (const [pattern, replacement] of LIGATURES) out = out.replace(pattern, replacement)
  return out.replace(/ae/g, 'a').replace(/oe/g, 'o').replace(/ue/g, 'u')
}

/** Folded single word: every non-alphanumeric character is dropped, so
 * "PAI-808" and "pai808" are the same token and "don't" is "dont". */
export function foldVoiceToken(value: string): string {
  return foldCore(value).replace(/[^a-z0-9]+/g, '')
}

/** Folded phrase: punctuation collapses to single spaces, so "PAI-808" is
 * "pai 808" and a title matches word-wise regardless of hyphenation. */
export function foldVoiceText(value: string): string {
  return foldCore(value).replace(/[^a-z0-9]+/g, ' ').trim()
}

export interface VoiceToken {
  /** Original slice, used to reconstruct an untouched note body. */
  raw: string
  /** Folded comparison form. */
  norm: string
  /** Inclusive raw start offset. */
  start: number
  /** Exclusive raw end offset. */
  end: number
}

/** Splits on Unicode whitespace, keeping raw offsets so a note body can be
 * sliced back out of the ORIGINAL text without any normalization damage. */
export function tokenizeVoice(raw: string): VoiceToken[] {
  const tokens: VoiceToken[] = []
  const pattern = /\S+/gu
  let match: RegExpExecArray | null
  while ((match = pattern.exec(raw)) !== null) {
    const norm = foldVoiceToken(match[0])
    if (norm === '') continue
    tokens.push({ raw: match[0], norm, start: match.index, end: match.index + match[0].length })
  }
  return tokens
}

const ISSUE_KEY_ONE_TOKEN = /^([a-z][a-z0-9]{0,9}?)(\d{1,9})$/
const ISSUE_PREFIX = /^[a-z][a-z0-9]{1,9}$/
const ISSUE_NUMBER = /^\d{1,9}$/

function canonicalIssueKey(prefix: string, digits: string): string {
  const number = digits.replace(/^0+(?=\d)/, '')
  return `${prefix.toUpperCase()}-${number}`
}

/**
 * Canonical `PAI-808` from any dictated spelling: `PAI-808`, `pai 808`,
 * `pai808`, `PAI–808` (en dash), `PAI-0808`. Returns null when the text is
 * not an issue key at all.
 */
export function normalizeIssueKey(value: string): string | null {
  const tokens = tokenizeVoice(value)
  const read = readIssueKey(tokens, 0)
  return read && read.next === tokens.length ? read.key : null
}

function readIssueKey(tokens: readonly VoiceToken[], index: number): { key: string; next: number } | null {
  const token = tokens[index]
  if (!token) return null
  const single = ISSUE_KEY_ONE_TOKEN.exec(token.norm)
  if (single) return { key: canonicalIssueKey(single[1], single[2]), next: index + 1 }
  const next = tokens[index + 1]
  if (next && ISSUE_PREFIX.test(token.norm) && ISSUE_NUMBER.test(next.norm)) {
    return { key: canonicalIssueKey(token.norm, next.norm), next: index + 2 }
  }
  return null
}

// ── Intent model ───────────────────────────────────────────────────────

export type VoiceSelectField = 'issue' | 'title' | 'agent' | 'lane' | 'any'
export type DetailLevelIntent = 1 | 10 | 100

/** Supervisory controls that Agent Mode voice deliberately does NOT own.
 * They are classified so the UI can refuse them explicitly instead of
 * silently doing nothing — and never forwarded anywhere. */
export type UnsupportedVoiceControl = 'approve' | 'pause' | 'resume' | 'cancel_job' | 'priority'

export type VoiceFilterIntent =
  | { type: 'project'; text: string }
  | { type: 'health'; health: Exclude<HealthFilter, 'all'> }
  | { type: 'query'; text: string }

export type VoiceNoteTarget = { kind: 'current' } | { kind: 'issue'; issueKey: string }

export type VoiceIntent =
  | { kind: 'select_issue'; issueKey: string }
  | { kind: 'select_named'; field: VoiceSelectField; text: string }
  | { kind: 'select_current' }
  | { kind: 'step'; direction: 'next' | 'previous' }
  | { kind: 'filter'; filter: VoiceFilterIntent }
  | { kind: 'clear_filters' }
  | { kind: 'detail'; level: DetailLevelIntent }
  | { kind: 'show_details' }
  | { kind: 'read_status'; scope: 'selection' | 'portfolio' }
  | { kind: 'note'; target: VoiceNoteTarget; body: string }
  | { kind: 'confirm_note' }
  | { kind: 'cancel_note' }
  | { kind: 'candidate'; index: number }
  | { kind: 'unsupported'; control: UnsupportedVoiceControl }
  | { kind: 'unknown' }

export interface ParseVoiceOptions {
  /** How many clarification candidates are currently on offer. A bare
   * numeral only means "candidate N" while a clarification is open. */
  candidateCount?: number
}

// ── Vocabulary ─────────────────────────────────────────────────────────
//
// Every entry is stored folded. `phrases()` turns a table into a
// longest-first list so "note that" wins over "note".

function folded(phrase: string): string[] {
  return phrase.split(' ').map(foldVoiceToken).filter((t) => t !== '')
}

interface Phrase {
  tokens: string[]
}

function phrases(list: readonly string[]): Phrase[] {
  return list
    .map((p) => ({ tokens: folded(p) }))
    .filter((p) => p.tokens.length > 0)
    .sort((a, b) => b.tokens.length - a.tokens.length)
}

function matchPhrase(tokens: readonly VoiceToken[], index: number, table: readonly Phrase[]): number | null {
  for (const phrase of table) {
    if (index + phrase.tokens.length > tokens.length) continue
    let ok = true
    for (let i = 0; i < phrase.tokens.length; i += 1) {
      if (tokens[index + i].norm !== phrase.tokens[i]) {
        ok = false
        break
      }
    }
    if (ok) return index + phrase.tokens.length
  }
  return null
}

function hasPhrase(tokens: readonly VoiceToken[], table: readonly Phrase[]): boolean {
  for (let i = 0; i < tokens.length; i += 1) if (matchPhrase(tokens, i, table) !== null) return true
  return false
}

function hasWord(tokens: readonly VoiceToken[], words: ReadonlySet<string>): boolean {
  return tokens.some((t) => words.has(t.norm))
}

const NOTE_PREFIXES = phrases([
  'note that', 'note', 'add a note', 'add note', 'add an internal note', 'internal note',
  'make a note', 'take a note', 'leave a note', 'jot down',
  'interne notiz', 'notiz', 'notiere', 'notier', 'vermerke', 'vermerk',
  'füge eine notiz hinzu', 'schreibe eine notiz', 'notiz hinzufügen', 'halte fest',
])

const NOTE_TARGET_CURRENT = phrases([
  'on this', 'on it', 'for this', 'for it', 'about this', 'about it', 'on this one',
  'zu diesem', 'zu dem', 'dazu', 'hierzu', 'für dieses', 'für das', 'daran',
])

const NOTE_TARGET_PREPOSITIONS = phrases(['on', 'for', 'about', 'zu', 'für', 'an'])

const NOTE_BODY_LEAD_INS = phrases(['that', 'saying', 'dass', 'folgendes'])

const CONFIRM_PHRASES = phrases([
  'confirm', 'confirm it', 'confirm the note', 'confirm note', 'yes confirm', 'send it',
  'post it', 'save the note', 'save it', 'submit it',
  'bestätige', 'bestätigen', 'bestätige es', 'notiz bestätigen', 'absenden', 'abschicken',
  'speichern', 'notiz speichern', 'ja bestätigen',
])

const CANCEL_NOTE_PHRASES = phrases([
  'cancel', 'cancel it', 'cancel the note', 'cancel note', 'discard', 'discard it',
  'discard the note', 'never mind', 'nevermind', 'forget it', 'scratch that', 'delete the note',
  'abbrechen', 'verwerfen', 'notiz verwerfen', 'notiz abbrechen', 'vergiss es', 'doch nicht',
  'verwirf', 'verwirf die notiz',
])

/** Supervisory verbs with no ambiguity — recognizing one is enough. */
const SUPERVISORY_VERBS = new Map<string, UnsupportedVoiceControl>([
  ['approve', 'approve'], ['approved', 'approve'], ['genehmige', 'approve'], ['genehmigen', 'approve'],
  ['freigeben', 'approve'], ['freigabe', 'approve'], ['abnehmen', 'approve'],
  ['pause', 'pause'], ['paused', 'pause'], ['pausiere', 'pause'], ['pausieren', 'pause'],
  ['anhalten', 'pause'],
  ['resume', 'resume'], ['unpause', 'resume'], ['fortsetzen', 'resume'], ['fortfahren', 'resume'],
  ['weitermachen', 'resume'],
  ['priority', 'priority'], ['prioritize', 'priority'], ['prioritise', 'priority'],
  ['priorisieren', 'priority'], ['priorisiere', 'priority'], ['hochstufen', 'priority'],
  // "Priorität" folds to "prioritat".
  [foldVoiceToken('priorität'), 'priority'],
])

const SUPERVISORY_PHRASES: Array<{ phrase: Phrase; control: UnsupportedVoiceControl }> = [
  ...phrases(['wieder aufnehmen', 'weiter machen', 'nimm wieder auf', 'continue the run', 'continue the job'])
    .map((phrase) => ({ phrase, control: 'resume' as const })),
  ...phrases(['halte an', 'halt an', 'hold the run', 'hold it']).map((phrase) => ({ phrase, control: 'pause' as const })),
  ...phrases(['set priority', 'raise the priority', 'bump priority', 'priorität setzen']).map((phrase) => ({ phrase, control: 'priority' as const })),
]

/** "cancel" / "stop" / "abbrechen" mean the NOTE unless a job-ish object is
 * named. This is the single grammar-precedence rule that keeps a supervisory
 * verb from ever being executed as a note cancel and vice versa. */
const STOP_VERBS = new Set([
  'cancel', 'stop', 'kill', 'abort', 'terminate',
  'abbrechen', 'stoppe', 'stopp', 'stoppen', 'brich', 'beende', 'beenden', 'töte',
].map(foldVoiceToken))

const JOB_OBJECTS = new Set([
  'job', 'jobs', 'run', 'runs', 'task', 'tasks', 'build', 'builds', 'deploy', 'deployment',
  'pipeline', 'attempt', 'agent', 'execution', 'lauf', 'laufs', 'laufes', 'auftrag', 'vorgang',
  'durchlauf', 'prozess',
].map(foldVoiceToken))

const CANDIDATE_MARKERS = new Set([
  'number', 'option', 'candidate', 'choice', 'item',
  'nummer', 'kandidat', 'auswahl', 'variante',
].map(foldVoiceToken))

const ORDINALS = new Map<string, number>([
  ['1', 1], ['one', 1], ['first', 1], ['eins', 1], ['ein', 1], ['erste', 1], ['erster', 1], ['erstes', 1], ['ersten', 1],
  ['2', 2], ['two', 2], ['second', 2], ['zwei', 2], ['zweite', 2], ['zweiter', 2], ['zweites', 2], ['zweiten', 2],
  ['3', 3], ['three', 3], ['third', 3], ['drei', 3], ['dritte', 3], ['dritter', 3], ['drittes', 3], ['dritten', 3],
])

const READ_STATUS_WORDS = new Set([
  'status', 'stand', 'statusbericht', 'vorlesen', 'statusreport', 'lagebericht',
].map(foldVoiceToken))

const READ_STATUS_PHRASES = phrases([
  'read status', 'read the status', 'read it out', 'read out', 'status report',
  'wie ist der stand', 'lies den status', 'lies vor', 'sag mir den stand',
])

const PORTFOLIO_WORDS = new Set([
  'portfolio', 'overview', 'übersicht', 'gesamt', 'gesamtstatus', 'everything', 'alles', 'all',
].map(foldVoiceToken))

/** Single-word overview commands that mean Detail 100. `all` / `alles` are
 * deliberately absent: they mean "clear the filters". */
const DETAIL_100_WORDS = new Set(['portfolio', 'overview', 'übersicht'].map(foldVoiceToken))

const DETAIL_MARKERS = new Set([
  'detail', 'details', 'detailstufe', 'detailgrad', 'zoom', 'level', 'stufe', 'ebene', 'detailansicht',
].map(foldVoiceToken))

const DETAIL_NUMBERS = new Map<string, DetailLevelIntent>([
  ['1', 1], ['one', 1], ['eins', 1], ['ein', 1], ['einer', 1],
  ['10', 10], ['ten', 10], ['zehn', 10],
  ['100', 100], ['hundred', 100], ['hundert', 100], ['einhundert', 100], ['onehundred', 100],
])

const SHOW_DETAILS_WORDS = new Set(['details', 'detail', 'detailansicht'].map(foldVoiceToken))

const CLEAR_FILTER_PHRASES = phrases([
  'clear filters', 'clear the filters', 'clear filter', 'reset filters', 'reset the filters',
  'remove filters', 'remove the filters', 'no filters', 'show all', 'show everything', 'all deliveries',
  'filter zurücksetzen', 'filter löschen', 'filter entfernen', 'keine filter', 'alle anzeigen',
  'alles anzeigen', 'zeige alle', 'alle lieferungen',
])

const HEALTH_WORDS = new Map<string, Exclude<HealthFilter, 'all'>>([
  ['attention', 'attention'], ['aufmerksamkeit', 'attention'], ['eingabe', 'attention'],
  ['blocked', 'blocked'], ['blockiert', 'blocked'], ['blockierte', 'blocked'], ['blocker', 'blocked'],
  ['stale', 'stale'], ['veraltet', 'stale'], ['veraltete', 'stale'], ['abgestanden', 'stale'],
].map(([word, health]) => [foldVoiceToken(word), health] as [string, Exclude<HealthFilter, 'all'>]))

const HEALTH_PHRASES: Array<{ phrase: Phrase; health: Exclude<HealthFilter, 'all'> }> = [
  ...phrases(['needs input', 'needs you', 'needs me', 'braucht eingabe', 'braucht mich', 'wartet auf mich'])
    .map((phrase) => ({ phrase, health: 'attention' as const })),
  ...phrases(['no signal', 'kein signal', 'ohne signal']).map((phrase) => ({ phrase, health: 'stale' as const })),
]

const PROJECT_MARKERS = new Set(['project', 'projekt', 'projects', 'projekte'].map(foldVoiceToken))

const QUERY_MARKERS = phrases([
  'search for', 'search', 'find', 'filter for', 'filter by', 'look for',
  'suche nach', 'suche', 'finde', 'filtere nach', 'filter nach',
])

const NEXT_PHRASES = phrases([
  'next', 'next one', 'next delivery', 'go next', 'forward',
  'weiter', 'nächste', 'nächster', 'nächstes', 'vor', 'vorwärts', 'weiter zur nächsten',
])

const PREVIOUS_PHRASES = phrases([
  'previous', 'prev', 'previous one', 'previous delivery', 'go back', 'back',
  'zurück', 'vorherige', 'voriger', 'vorheriges', 'vorherigen', 'davor', 'rückwärts',
])

const STRONG_SELECT_PHRASES = phrases([
  'select', 'select the', 'focus', 'focus on', 'go to', 'jump to', 'switch to', 'take me to',
  'wähle', 'wähle aus', 'wählen', 'auswählen', 'fokussiere', 'gehe zu', 'geh zu', 'springe zu',
  'wechsle zu',
])

/** Neutral display verbs. They are stripped before dispatch so "show
 * details", "show blocked", and "show PAI-808" each reach the right rule. */
const DISPLAY_VERBS = phrases([
  'show me', 'show', 'display', 'anzeigen', 'zeige mir', 'zeig mir', 'zeige', 'zeig', 'zeigen',
  'gib mir', 'lies mir',
])

const LEADING_FILLERS = phrases([
  'please', 'hey', 'ok', 'okay', 'and', 'also', 'can you', 'could you', 'would you',
  'bitte', 'mal', 'kannst du', 'könntest du', 'paimos', 'hallo', 'und',
])

// German separable-verb particles trail the object ("wähle PAI-812 aus").
const TRAILING_FILLERS = new Set(['please', 'bitte', 'mal', 'danke', 'thanks', 'aus'].map(foldVoiceToken))

const ARTICLES = new Set([
  'the', 'a', 'an', 'der', 'die', 'das', 'den', 'dem', 'ein', 'eine', 'einen', 'einem', 'einer',
].map(foldVoiceToken))

const PRONOUN_PHRASES = phrases([
  'this', 'that', 'it', 'this one', 'that one', 'the current one', 'current', 'the selection',
  'the selected one', 'dies', 'dieses', 'diese', 'diesen', 'das', 'es', 'der aktuelle',
  'die aktuelle', 'das aktuelle', 'aktuelle', 'aktuelles', 'ausgewählte', 'die auswahl',
])

const AGENT_MARKERS = phrases(['agent', 'agenten', 'by agent', 'by', 'von', 'vom'])
const LANE_MARKERS = phrases(['lane', 'epic', 'the lane', 'the epic', 'spur', 'epos', 'bahn'])
const TITLE_MARKERS = phrases([
  'titled', 'title', 'named', 'called', 'ticket titled', 'ticket named', 'issue titled', 'delivery titled',
  'titel', 'namens', 'mit titel', 'ticket namens', 'ticket mit titel',
])

// ── Parser ─────────────────────────────────────────────────────────────

function stripLeading(tokens: readonly VoiceToken[], table: readonly Phrase[]): VoiceToken[] {
  let out = [...tokens]
  for (;;) {
    const next = matchPhrase(out, 0, table)
    if (next === null || next >= out.length) return out
    out = out.slice(next)
  }
}

function stripTrailingFillers(tokens: readonly VoiceToken[]): VoiceToken[] {
  let end = tokens.length
  while (end > 1 && TRAILING_FILLERS.has(tokens[end - 1].norm)) end -= 1
  return tokens.slice(0, end)
}

function stripArticles(tokens: readonly VoiceToken[]): VoiceToken[] {
  let out = [...tokens]
  while (out.length > 1 && ARTICLES.has(out[0].norm)) out = out.slice(1)
  return out
}

function rawFrom(raw: string, tokens: readonly VoiceToken[], index: number): string {
  const token = tokens[index]
  if (!token) return ''
  return raw.slice(token.start).trim()
}

function parseNote(raw: string, tokens: readonly VoiceToken[]): VoiceIntent | null {
  const afterPrefix = matchPhrase(tokens, 0, NOTE_PREFIXES)
  if (afterPrefix === null) return null

  let index = afterPrefix
  let target: VoiceNoteTarget = { kind: 'current' }

  const afterCurrent = matchPhrase(tokens, index, NOTE_TARGET_CURRENT)
  if (afterCurrent !== null) {
    index = afterCurrent
  } else {
    // "note on PAI-808 that …" binds the note to a named delivery; the
    // resolver still has to authorize it.
    // Only an explicit preposition may retarget the note. Without one every
    // word stays in the body, so "note case84 fails" is never mistaken for a
    // binding to a non-existent CASE-84.
    const afterPreposition = matchPhrase(tokens, index, NOTE_TARGET_PREPOSITIONS)
    const key = afterPreposition === null ? null : readIssueKey(tokens, afterPreposition)
    if (key) {
      target = { kind: 'issue', issueKey: key.key }
      index = key.next
    }
  }

  const afterLeadIn = matchPhrase(tokens, index, NOTE_BODY_LEAD_INS)
  if (afterLeadIn !== null) index = afterLeadIn

  return { kind: 'note', target, body: rawFrom(raw, tokens, index) }
}

function parseSupervisory(tokens: readonly VoiceToken[]): VoiceIntent | null {
  for (const token of tokens) {
    const control = SUPERVISORY_VERBS.get(token.norm)
    if (control) return { kind: 'unsupported', control }
  }
  for (const entry of SUPERVISORY_PHRASES) {
    if (hasPhrase(tokens, [entry.phrase])) return { kind: 'unsupported', control: entry.control }
  }
  if (hasWord(tokens, STOP_VERBS) && hasWord(tokens, JOB_OBJECTS)) {
    return { kind: 'unsupported', control: 'cancel_job' }
  }
  return null
}

function parseCandidate(tokens: readonly VoiceToken[], candidateCount: number): VoiceIntent | null {
  for (let i = 0; i < tokens.length; i += 1) {
    if (!CANDIDATE_MARKERS.has(tokens[i].norm)) continue
    const value = ORDINALS.get(tokens[i + 1]?.norm ?? '')
    if (value !== undefined) return { kind: 'candidate', index: value }
  }
  const compact = tokens.filter((t) => !ARTICLES.has(t.norm))
  if (compact.length === 1) {
    const value = ORDINALS.get(compact[0].norm)
    // A bare numeral is only a choice while a clarification is open, and only
    // for a slot that actually exists.
    if (value !== undefined && candidateCount > 0 && value <= candidateCount) {
      return { kind: 'candidate', index: value }
    }
  }
  return null
}

function parseReadStatus(tokens: readonly VoiceToken[]): VoiceIntent | null {
  if (!hasWord(tokens, READ_STATUS_WORDS) && !hasPhrase(tokens, READ_STATUS_PHRASES)) return null
  return { kind: 'read_status', scope: hasWord(tokens, PORTFOLIO_WORDS) ? 'portfolio' : 'selection' }
}

function parseDetail(tokens: readonly VoiceToken[]): VoiceIntent | null {
  const hasMarker = hasWord(tokens, DETAIL_MARKERS)
  if (hasMarker) {
    for (let i = 0; i < tokens.length; i += 1) {
      const pair = `${tokens[i].norm}${tokens[i + 1]?.norm ?? ''}`
      const level = DETAIL_NUMBERS.get(pair) ?? DETAIL_NUMBERS.get(tokens[i].norm)
      if (level !== undefined) return { kind: 'detail', level }
    }
  }
  const compact = tokens.filter((t) => !ARTICLES.has(t.norm))
  if (compact.length === 1 && DETAIL_100_WORDS.has(compact[0].norm)) return { kind: 'detail', level: 100 }
  if (hasMarker && hasWord(tokens, SHOW_DETAILS_WORDS)) return { kind: 'show_details' }
  return null
}

function parseFilters(tokens: readonly VoiceToken[], raw: string): VoiceIntent | null {
  if (hasPhrase(tokens, CLEAR_FILTER_PHRASES)) return { kind: 'clear_filters' }

  for (let i = 0; i < tokens.length; i += 1) {
    if (!PROJECT_MARKERS.has(tokens[i].norm)) continue
    const rest = stripArticles(tokens.slice(i + 1))
    if (rest.length === 0) continue
    return { kind: 'filter', filter: { type: 'project', text: rawFrom(raw, rest, 0) } }
  }

  for (const entry of HEALTH_PHRASES) {
    if (hasPhrase(tokens, [entry.phrase])) return { kind: 'filter', filter: { type: 'health', health: entry.health } }
  }
  for (const token of tokens) {
    const health = HEALTH_WORDS.get(token.norm)
    if (health) return { kind: 'filter', filter: { type: 'health', health } }
  }

  for (let i = 0; i < tokens.length; i += 1) {
    const after = matchPhrase(tokens, i, QUERY_MARKERS)
    if (after === null) continue
    const rest = stripArticles(tokens.slice(after))
    if (rest.length === 0) continue
    return { kind: 'filter', filter: { type: 'query', text: rawFrom(raw, rest, 0) } }
  }
  return null
}

function parseSelectBody(raw: string, tokens: readonly VoiceToken[]): VoiceIntent {
  const body = stripArticles(tokens)
  if (body.length === 0) return { kind: 'select_current' }

  if (matchPhrase(body, 0, PRONOUN_PHRASES) === body.length) return { kind: 'select_current' }

  const key = readIssueKey(body, 0)
  if (key && key.next === body.length) return { kind: 'select_issue', issueKey: key.key }

  const qualifiers: Array<{ table: readonly Phrase[]; field: VoiceSelectField }> = [
    { table: AGENT_MARKERS, field: 'agent' },
    { table: LANE_MARKERS, field: 'lane' },
    { table: TITLE_MARKERS, field: 'title' },
  ]
  for (const qualifier of qualifiers) {
    const after = matchPhrase(body, 0, qualifier.table)
    if (after === null || after >= body.length) continue
    const rest = stripArticles(body.slice(after))
    if (rest.length === 0) continue
    return { kind: 'select_named', field: qualifier.field, text: rawFrom(raw, rest, 0) }
  }

  if (key) return { kind: 'select_issue', issueKey: key.key }
  return { kind: 'select_named', field: 'any', text: rawFrom(raw, body, 0) }
}

/**
 * Deterministic EN/DE grammar.
 *
 * Precedence, highest first — this order IS the contract:
 *   1. note prefix (the rest of the utterance is an opaque body)
 *   2. supervisory verbs → explicitly unsupported
 *   3. note confirm / cancel
 *   4. clarification candidate
 *   5. explicit select verb
 *   6. read status
 *   7. detail level / show details
 *   8. clear filters / filters
 *   9. next / previous
 *  10. bare issue key or pronoun → select
 *  11. unknown
 */
export function parseVoiceCommand(raw: string, opts: ParseVoiceOptions = {}): VoiceIntent {
  const all = tokenizeVoice(raw)
  if (all.length === 0) return { kind: 'unknown' }

  // 1 — a note body is never re-parsed.
  const note = parseNote(raw, all)
  if (note) return note

  const tokens = stripArticles(stripTrailingFillers(stripLeading(all, LEADING_FILLERS)))
  if (tokens.length === 0) return { kind: 'unknown' }

  // 2 — supervisory verbs must be refused before "cancel" can mean the note.
  const supervisory = parseSupervisory(tokens)
  if (supervisory) return supervisory

  // 3 — note lifecycle.
  if (matchPhrase(tokens, 0, CONFIRM_PHRASES) === tokens.length) return { kind: 'confirm_note' }
  if (matchPhrase(tokens, 0, CANCEL_NOTE_PHRASES) === tokens.length) return { kind: 'cancel_note' }

  // 4 — clarification.
  const candidate = parseCandidate(tokens, opts.candidateCount ?? 0)
  if (candidate) return candidate

  // 5 — an explicit select verb outranks the filter heuristics.
  const afterSelect = matchPhrase(tokens, 0, STRONG_SELECT_PHRASES)
  if (afterSelect !== null) return parseSelectBody(raw, tokens.slice(afterSelect))

  // "show all" / "zeige alle" clear the filters, so the clear check runs
  // before the display verb is stripped away.
  if (hasPhrase(tokens, CLEAR_FILTER_PHRASES)) return { kind: 'clear_filters' }

  const display = stripLeading(tokens, DISPLAY_VERBS)

  // 6 / 7 — read-back and semantic zoom.
  const status = parseReadStatus(display)
  if (status) return status
  const detail = parseDetail(display)
  if (detail) return detail

  // 8 — filters.
  const filter = parseFilters(display, raw)
  if (filter) return filter

  // 9 — travel.
  if (matchPhrase(display, 0, NEXT_PHRASES) === display.length) return { kind: 'step', direction: 'next' }
  if (matchPhrase(display, 0, PREVIOUS_PHRASES) === display.length) return { kind: 'step', direction: 'previous' }

  // 10 — bare key or pronoun.
  const key = readIssueKey(display, 0)
  if (key && key.next === display.length) return { kind: 'select_issue', issueKey: key.key }
  if (display.length > 0 && matchPhrase(display, 0, PRONOUN_PHRASES) === display.length) {
    return { kind: 'select_current' }
  }

  return { kind: 'unknown' }
}

// ── Resolver ───────────────────────────────────────────────────────────

export interface VoiceResolutionContext {
  /** The current authorized, selectable deliveries. Nothing outside this
   * list can be selected, narrated, noted, or offered as a candidate. */
  deliveries: readonly Delivery[]
  /** The current persistent authorized selection. */
  selectedId: string | null
}

export interface VoiceCandidate {
  /** 1-based position spoken back to the operator. */
  index: number
  deliveryId: string
  issueKey: string
  /** Server-authored ticket title — for the VISUAL numbered list only. */
  title: string
}

export interface VoiceFilterPatch {
  projectId?: number | null
  health?: HealthFilter
  query?: string
}

export type VoiceCommand =
  | { type: 'select'; deliveryId: string }
  | { type: 'step'; direction: 'next' | 'previous' }
  | { type: 'set_filters'; patch: VoiceFilterPatch }
  | { type: 'clear_filters' }
  | { type: 'set_detail'; level: DetailLevelIntent }
  | { type: 'show_details'; deliveryId: string }
  | { type: 'read_status'; scope: 'selection' | 'portfolio'; deliveryId: string | null }
  | { type: 'draft_note'; deliveryId: string; body: string }
  | { type: 'confirm_note' }
  | { type: 'cancel_note' }

export type VoiceRejection =
  | 'no_match'
  | 'no_selection'
  | 'empty_note'
  /** A note named an authorized delivery that is not the current selection. */
  | 'note_target_not_selected'
  | 'ambiguous_project'
  | 'unknown_command'

export type VoiceResolution =
  | { kind: 'command'; command: VoiceCommand }
  | { kind: 'clarify'; candidates: VoiceCandidate[]; matchCount: number; truncated: boolean; field: VoiceSelectField }
  | { kind: 'unsupported'; control: UnsupportedVoiceControl }
  | { kind: 'rejected'; reason: VoiceRejection }

function selectedDelivery(ctx: VoiceResolutionContext): Delivery | null {
  if (!ctx.selectedId) return null
  return ctx.deliveries.find((d) => d.id === ctx.selectedId) ?? null
}

function padded(value: string): string {
  return ` ${value} `
}

function fieldValues(d: Delivery, field: Exclude<VoiceSelectField, 'any'>): string[] {
  switch (field) {
    case 'issue':
      return [foldVoiceText(d.issueKey)]
    case 'title':
      return [foldVoiceText(d.title)]
    case 'agent':
      return [d.actor?.name ?? '', d.actor?.label ?? ''].filter((v) => v !== '').map(foldVoiceText)
    case 'lane':
      return [d.lane.epicKey ?? '', d.lane.epicTitle ?? '', d.lane.projectKey, d.lane.projectName]
        .filter((v) => v !== '')
        .map(foldVoiceText)
  }
}

function matchesExact(d: Delivery, field: Exclude<VoiceSelectField, 'any'>, query: string): boolean {
  if (field === 'issue') {
    const key = normalizeIssueKey(query)
    return key != null && key === d.issueKey.toUpperCase()
  }
  return fieldValues(d, field).some((value) => value === query)
}

function matchesPartial(d: Delivery, field: Exclude<VoiceSelectField, 'any'>, query: string): boolean {
  if (field === 'issue') {
    // "select 808" is a legitimate spoken shorthand for the numeric part;
    // arbitrary substrings of a key are not.
    if (!/^\d{1,9}$/.test(query)) return false
    const digits = query.replace(/^0+(?=\d)/, '')
    return d.issueKey.toUpperCase().endsWith(`-${digits}`)
  }
  return fieldValues(d, field).some((value) => padded(value).includes(padded(query)))
}

const ANY_FIELD_ORDER: Array<Exclude<VoiceSelectField, 'any'>> = ['issue', 'title', 'agent', 'lane']

function buildCandidates(matches: readonly Delivery[]): VoiceCandidate[] {
  return [...matches]
    .sort(compareDeliveries)
    .slice(0, MAX_VOICE_CANDIDATES)
    .map((d, i) => ({ index: i + 1, deliveryId: d.id, issueKey: d.issueKey, title: d.title }))
}

function resolveNamed(field: VoiceSelectField, text: string, ctx: VoiceResolutionContext): VoiceResolution {
  const query = foldVoiceText(text)
  if (query === '') return { kind: 'rejected', reason: 'no_match' }
  const fields = field === 'any' ? ANY_FIELD_ORDER : [field]

  // Two ordered passes: every exact field first, then every partial field.
  // An exact title always beats a partial agent, whatever the field order.
  for (const predicate of [matchesExact, matchesPartial]) {
    for (const candidateField of fields) {
      const matches = ctx.deliveries.filter((d) => predicate(d, candidateField, query))
      if (matches.length === 1) return { kind: 'command', command: { type: 'select', deliveryId: matches[0].id } }
      if (matches.length > 1) {
        return {
          kind: 'clarify',
          candidates: buildCandidates(matches),
          matchCount: matches.length,
          truncated: matches.length > MAX_VOICE_CANDIDATES,
          field: candidateField,
        }
      }
    }
  }
  return { kind: 'rejected', reason: 'no_match' }
}

function resolveProjectFilter(text: string, ctx: VoiceResolutionContext): VoiceResolution {
  const query = foldVoiceText(text)
  if (query === '') return { kind: 'rejected', reason: 'no_match' }
  const exact = new Set<number>()
  const partial = new Set<number>()
  for (const d of ctx.deliveries) {
    const key = foldVoiceText(d.lane.projectKey)
    const name = foldVoiceText(d.lane.projectName)
    if (key === query || name === query) exact.add(d.lane.projectId)
    else if (padded(name).includes(padded(query)) || padded(key).includes(padded(query))) partial.add(d.lane.projectId)
  }
  const chosen = exact.size > 0 ? exact : partial
  if (chosen.size === 0) return { kind: 'rejected', reason: 'no_match' }
  // A filter must never guess between projects — and never touches selection.
  if (chosen.size > 1) return { kind: 'rejected', reason: 'ambiguous_project' }
  return { kind: 'command', command: { type: 'set_filters', patch: { projectId: [...chosen][0] } } }
}

/**
 * Turns an intent into an executable command against the CURRENT authorized
 * snapshot, or into a bounded clarification, an explicit refusal, or an
 * honest rejection. Never mutates its inputs.
 */
export function resolveVoiceIntent(intent: VoiceIntent, ctx: VoiceResolutionContext): VoiceResolution {
  switch (intent.kind) {
    case 'unsupported':
      return { kind: 'unsupported', control: intent.control }

    case 'unknown':
      return { kind: 'rejected', reason: 'unknown_command' }

    case 'step':
      return { kind: 'command', command: { type: 'step', direction: intent.direction } }

    case 'detail':
      return { kind: 'command', command: { type: 'set_detail', level: intent.level } }

    case 'clear_filters':
      return { kind: 'command', command: { type: 'clear_filters' } }

    case 'confirm_note':
      return { kind: 'command', command: { type: 'confirm_note' } }

    case 'cancel_note':
      return { kind: 'command', command: { type: 'cancel_note' } }

    case 'select_current': {
      const current = selectedDelivery(ctx)
      if (!current) return { kind: 'rejected', reason: 'no_selection' }
      return { kind: 'command', command: { type: 'select', deliveryId: current.id } }
    }

    case 'show_details': {
      const current = selectedDelivery(ctx)
      if (!current) return { kind: 'rejected', reason: 'no_selection' }
      return { kind: 'command', command: { type: 'show_details', deliveryId: current.id } }
    }

    case 'read_status': {
      if (intent.scope === 'portfolio') {
        return { kind: 'command', command: { type: 'read_status', scope: 'portfolio', deliveryId: null } }
      }
      const current = selectedDelivery(ctx)
      if (!current) return { kind: 'rejected', reason: 'no_selection' }
      return { kind: 'command', command: { type: 'read_status', scope: 'selection', deliveryId: current.id } }
    }

    case 'select_issue': {
      const match = ctx.deliveries.find((d) => d.issueKey.toUpperCase() === intent.issueKey)
      if (!match) return { kind: 'rejected', reason: 'no_match' }
      return { kind: 'command', command: { type: 'select', deliveryId: match.id } }
    }

    case 'select_named':
      return resolveNamed(intent.field, intent.text, ctx)

    case 'filter': {
      if (intent.filter.type === 'project') return resolveProjectFilter(intent.filter.text, ctx)
      if (intent.filter.type === 'health') {
        return { kind: 'command', command: { type: 'set_filters', patch: { health: intent.filter.health } } }
      }
      const query = intent.filter.text.trim()
      if (query === '') return { kind: 'rejected', reason: 'no_match' }
      return { kind: 'command', command: { type: 'set_filters', patch: { query } } }
    }

    case 'note': {
      // A note always binds to the CURRENT selection. Naming another delivery
      // is answered with "select it first" rather than a silent rebind — a
      // note on the wrong ticket is the one mistake worth being loud about.
      const body = intent.body.trim()
      const noteTarget = intent.target
      const current = selectedDelivery(ctx)
      if (!current) return { kind: 'rejected', reason: 'no_selection' }
      if (noteTarget.kind === 'issue') {
        const named = ctx.deliveries.find((d) => d.issueKey.toUpperCase() === noteTarget.issueKey) ?? null
        if (!named) return { kind: 'rejected', reason: 'no_match' }
        if (named.id !== current.id) return { kind: 'rejected', reason: 'note_target_not_selected' }
      }
      if (body === '') return { kind: 'rejected', reason: 'empty_note' }
      return { kind: 'command', command: { type: 'draft_note', deliveryId: current.id, body } }
    }

    case 'candidate':
      // Candidate selection is resolved by the state machine, which owns the
      // open clarification. The grammar only reports the number.
      return { kind: 'rejected', reason: 'unknown_command' }
  }
}
