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

import { makeFixtureSnapshot } from '@/services/agentModeFixtures'
import { normalizeWireSnapshot } from '@/services/agentModeTransport'
import type { Delivery } from '@/services/agentMode'
import {
  MAX_VOICE_CANDIDATES,
  buildControlVoicePhrase,
  foldVoiceText,
  foldVoiceToken,
  normalizeIssueKey,
  parseVoiceCommand,
  resolveVoiceIntent,
  tokenizeVoice,
  type VoiceIntent,
  type VoiceProjectRef,
  type VoiceResolutionContext,
} from './agentModeVoiceIntent'
import type { ControlCommand } from '@/services/agentModeControls'

const snapshot = normalizeWireSnapshot(makeFixtureSnapshot(10), 0)
const fixtures = snapshot.deliveries
const base = fixtures[0]

/** The authorized catalog exactly as the production aggregate boundary
 * projects it — the same list AgentModeFilterBar renders. */
const CATALOG: VoiceProjectRef[] = (snapshot.aggregates?.projects ?? []).map((project) => ({
  projectId: project.projectId,
  projectKey: project.projectKey,
  projectName: project.projectName,
}))

function d(patch: Partial<Delivery>): Delivery {
  return { ...base, ...patch }
}

function ctx(overrides: Partial<VoiceResolutionContext> = {}): VoiceResolutionContext {
  return { deliveries: fixtures, selectedId: null, projectCatalog: CATALOG, ...overrides }
}

function parse(text: string, candidateCount = 0): VoiceIntent {
  return parseVoiceCommand(text, { candidateCount })
}

describe('agentModeVoiceIntent — normalization', () => {
  it('folds NFKD, diacritics, ß, and full-width digits to one comparison form', () => {
    expect(foldVoiceToken('Nächste')).toBe('nachste')
    expect(foldVoiceToken('naechste')).toBe('nachste')
    // Precomposed vs decomposed must not be two different words.
    expect(foldVoiceToken('nächste')).toBe('nachste')
    expect(foldVoiceToken('STRAßE')).toBe('strasse')
    expect(foldVoiceToken('STRASSE')).toBe('strasse')
    expect(foldVoiceToken('ẞ')).toBe('ss')
    expect(foldVoiceToken('８０８')).toBe('808')
    expect(foldVoiceToken('zurück')).toBe(foldVoiceToken('zurueck'))
    expect(foldVoiceToken('Übersicht')).toBe('ubersicht')
  })

  it('keeps word boundaries in phrase folding but drops them inside a token', () => {
    expect(foldVoiceText('PAI-808')).toBe('pai 808')
    expect(foldVoiceToken('PAI-808')).toBe('pai808')
    expect(foldVoiceText('  Access & trust  ')).toBe('access trust')
  })

  it('tokenizes with raw offsets so the original text can be recovered', () => {
    const raw = '  Notiz:   Fixture ist flaky '
    const tokens = tokenizeVoice(raw)
    expect(tokens.map((t) => t.norm)).toEqual(['notiz', 'fixture', 'ist', 'flaky'])
    expect(raw.slice(tokens[1].start)).toBe('Fixture ist flaky ')
    expect(tokenizeVoice('   ')).toEqual([])
    // Punctuation-only tokens carry no meaning and are dropped entirely.
    expect(tokenizeVoice('… — !').map((t) => t.norm)).toEqual([])
  })

  it.each([
    ['PAI-808', 'PAI-808'],
    ['pai 808', 'PAI-808'],
    ['pai808', 'PAI-808'],
    ['PAI–808', 'PAI-808'],
    ['  PAI-0808 ', 'PAI-808'],
    ['ＰＡＩ-808', 'PAI-808'],
  ])('normalizes the issue key alias %s', (input, expected) => {
    expect(normalizeIssueKey(input)).toBe(expected)
  })

  it.each(['808', 'PAI', 'PAI-808 and more', 'not a key', ''])('rejects %s as an issue key', (input) => {
    expect(normalizeIssueKey(input)).toBeNull()
  })
})

describe('agentModeVoiceIntent — exact two-utterance controls', () => {
  const command: ControlCommand = {
    commandId: '11111111-1111-4111-8111-111111111111',
    statusRevision: 3,
    action: 'run.cancel.running',
    status: 'pending_confirmation',
    challengeTemplate: 'run_cancel_running',
    expiresAt: '2026-08-21T20:00:00Z',
    display: { issueKey: 'PAI-812', deliveryKey: 'dlv-812', runId: 42 },
    outcome: null,
    reason: null,
  }

  it.each([
    ['en', 'Cancel running run PAI-812'],
    ['de', 'Laufenden Lauf abbrechen PAI-812'],
  ])('parses and resolves the scoped %s phrase from a current server target', (locale, phrase) => {
    expect(buildControlVoicePhrase(command, locale)).toBe(phrase)
    const intent = parse(phrase)
    expect(intent.kind).toBe('control')
    expect(resolveVoiceIntent(intent, ctx({
      selectedId: base.id,
      controlTargets: [{ action: 'run.cancel.running', runId: 42 }],
    }))).toEqual({
      kind: 'command',
      command: {
        type: 'request_control',
        activation: { target: { action: 'run.cancel.running', runId: 42 } },
      },
    })
  })

  it('confirms only the exact visible phrase and exact persisted revision', () => {
    const phrase = buildControlVoicePhrase(command, 'en')
    const context = ctx({
      selectedId: base.id,
      controlTargets: [{ action: 'run.cancel.running', runId: 99 }],
      controlChallenge: { command, issueKey: 'PAI-812', phrase },
    })
    expect(resolveVoiceIntent(parse(phrase), context)).toEqual({
      kind: 'command',
      command: { type: 'confirm_control', commandId: command.commandId, statusRevision: 3 },
    })
    expect(resolveVoiceIntent(parse('Cancel queued run PAI-812'), context))
      .toEqual({ kind: 'rejected', reason: 'control_confirmation_mismatch' })
    expect(resolveVoiceIntent(parse('confirm'), context))
      .toEqual({ kind: 'command', command: { type: 'confirm_note' } })
    expect(resolveVoiceIntent(parse('yes'), context))
      .toEqual({ kind: 'rejected', reason: 'unknown_command' })
  })

  it('invalidates scoped requests on selection drift and ambiguous target replacement', () => {
    const intent = parse('Approve request PAI-812')
    expect(resolveVoiceIntent(intent, ctx({
      selectedId: fixtures[3].id,
      controlTargets: [{
        action: 'input.respond', inputRequestId: '22222222-2222-4222-8222-222222222222',
        inputRequestRevision: 1, inputKind: 'approval',
      }],
    }))).toEqual({ kind: 'rejected', reason: 'no_selection' })
    expect(resolveVoiceIntent(intent, ctx({
      selectedId: base.id,
      controlTargets: [
        { action: 'input.respond', inputRequestId: '22222222-2222-4222-8222-222222222222', inputRequestRevision: 1, inputKind: 'approval' },
        { action: 'input.respond', inputRequestId: '33333333-3333-4333-8333-333333333333', inputRequestRevision: 1, inputKind: 'approval' },
      ],
    }))).toEqual({ kind: 'rejected', reason: 'control_unavailable' })
  })
})

describe('agentModeVoiceIntent — English grammar', () => {
  it.each<[string, VoiceIntent]>([
    ['select PAI-808', { kind: 'select_issue', issueKey: 'PAI-808' }],
    ['go to pai 808', { kind: 'select_issue', issueKey: 'PAI-808' }],
    ['PAI-808', { kind: 'select_issue', issueKey: 'PAI-808' }],
    ['select agent codex', { kind: 'select_named', field: 'agent', text: 'codex' }],
    ['select lane Agent Mode', { kind: 'select_named', field: 'lane', text: 'Agent Mode' }],
    ['select the ticket titled Anchor drift', { kind: 'select_named', field: 'title', text: 'Anchor drift' }],
    ['select this', { kind: 'select_current' }],
    ['this one', { kind: 'select_current' }],
    ['next', { kind: 'step', direction: 'next' }],
    ['next delivery please', { kind: 'step', direction: 'next' }],
    ['previous', { kind: 'step', direction: 'previous' }],
    ['go back', { kind: 'step', direction: 'previous' }],
    ['detail 1', { kind: 'detail', level: 1 }],
    ['detail ten', { kind: 'detail', level: 10 }],
    ['detail one hundred', { kind: 'detail', level: 100 }],
    ['zoom 100', { kind: 'detail', level: 100 }],
    ['overview', { kind: 'detail', level: 100 }],
    ['show details', { kind: 'show_details' }],
    ['details', { kind: 'show_details' }],
    ['read status', { kind: 'read_status', scope: 'selection' }],
    ['read the portfolio status', { kind: 'read_status', scope: 'portfolio' }],
    ['show blocked', { kind: 'filter', filter: { type: 'health', health: 'blocked' } }],
    ['needs input', { kind: 'filter', filter: { type: 'health', health: 'attention' } }],
    ['stale', { kind: 'filter', filter: { type: 'health', health: 'stale' } }],
    ['filter project PAI', { kind: 'filter', filter: { type: 'project', text: 'PAI' } }],
    ['search for reconnect', { kind: 'filter', filter: { type: 'query', text: 'reconnect' } }],
    ['clear filters', { kind: 'clear_filters' }],
    ['show all', { kind: 'clear_filters' }],
    ['confirm', { kind: 'confirm_note' }],
    ['confirm the note', { kind: 'confirm_note' }],
    ['cancel', { kind: 'cancel_note' }],
    ['never mind', { kind: 'cancel_note' }],
  ])('parses %s', (text, expected) => {
    expect(parse(text)).toEqual(expected)
  })
})

describe('agentModeVoiceIntent — German grammar', () => {
  it.each<[string, VoiceIntent]>([
    ['wähle PAI-808', { kind: 'select_issue', issueKey: 'PAI-808' }],
    ['wähle PAI 808 aus', { kind: 'select_issue', issueKey: 'PAI-808' }],
    ['gehe zu pai808', { kind: 'select_issue', issueKey: 'PAI-808' }],
    ['wähle Agent Codex', { kind: 'select_named', field: 'agent', text: 'Codex' }],
    ['wähle Epic Agent Mode', { kind: 'select_named', field: 'lane', text: 'Agent Mode' }],
    ['dieses', { kind: 'select_current' }],
    ['das', { kind: 'select_current' }],
    ['nächste', { kind: 'step', direction: 'next' }],
    ['naechste', { kind: 'step', direction: 'next' }],
    ['weiter', { kind: 'step', direction: 'next' }],
    ['zurück', { kind: 'step', direction: 'previous' }],
    ['vorherige', { kind: 'step', direction: 'previous' }],
    ['Detail eins', { kind: 'detail', level: 1 }],
    ['Detailstufe hundert', { kind: 'detail', level: 100 }],
    ['Übersicht', { kind: 'detail', level: 100 }],
    ['zeige Details', { kind: 'show_details' }],
    ['Status', { kind: 'read_status', scope: 'selection' }],
    ['wie ist der Stand', { kind: 'read_status', scope: 'selection' }],
    ['Gesamtstatus vorlesen', { kind: 'read_status', scope: 'portfolio' }],
    ['zeige blockierte', { kind: 'filter', filter: { type: 'health', health: 'blocked' } }],
    ['veraltet', { kind: 'filter', filter: { type: 'health', health: 'stale' } }],
    ['Projekt RUN', { kind: 'filter', filter: { type: 'project', text: 'RUN' } }],
    ['suche nach reconnect', { kind: 'filter', filter: { type: 'query', text: 'reconnect' } }],
    ['Filter zurücksetzen', { kind: 'clear_filters' }],
    ['alle anzeigen', { kind: 'clear_filters' }],
    ['bestätigen', { kind: 'confirm_note' }],
    ['abbrechen', { kind: 'cancel_note' }],
    ['verwerfen', { kind: 'cancel_note' }],
  ])('parses %s', (text, expected) => {
    expect(parse(text)).toEqual(expected)
  })
})

describe('agentModeVoiceIntent — unsupported supervisory verbs', () => {
  it.each<[string, string]>([
    ['approve this', 'approve'],
    ['approve the run', 'approve'],
    ['genehmige das', 'approve'],
    ['freigeben', 'approve'],
    ['pause', 'pause'],
    ['pausiere den Lauf', 'pause'],
    ['hold the run', 'pause'],
    ['resume', 'resume'],
    ['fortsetzen', 'resume'],
    ['weiter machen', 'resume'],
    ['continue the run', 'resume'],
    ['cancel the job', 'cancel_job'],
    ['stop the run', 'cancel_job'],
    ['kill the deployment', 'cancel_job'],
    ['brich den Job ab', 'cancel_job'],
    ['Lauf abbrechen', 'cancel_job'],
    ['raise the priority', 'priority'],
    ['Priorität setzen', 'priority'],
    ['priorisieren', 'priority'],
  ])('refuses %s without ever producing a command', (text, control) => {
    const intent = parse(text)
    expect(intent).toEqual({ kind: 'unsupported', control })
    const resolution = resolveVoiceIntent(intent, ctx({ selectedId: base.id }))
    expect(resolution).toEqual({ kind: 'unsupported', control })
    expect(resolution).not.toHaveProperty('command')
  })

  it('keeps the supervisory / note-lifecycle boundary exact', () => {
    // Bare cancel is the note; a named job object makes it supervisory.
    expect(parse('cancel')).toEqual({ kind: 'cancel_note' })
    expect(parse('cancel it')).toEqual({ kind: 'cancel_note' })
    expect(parse('cancel the job')).toEqual({ kind: 'unsupported', control: 'cancel_job' })
    expect(parse('abbrechen')).toEqual({ kind: 'cancel_note' })
    expect(parse('Job abbrechen')).toEqual({ kind: 'unsupported', control: 'cancel_job' })
    // "weiter" travels; "weitermachen" is the resume control.
    expect(parse('weiter')).toEqual({ kind: 'step', direction: 'next' })
    expect(parse('weitermachen')).toEqual({ kind: 'unsupported', control: 'resume' })
  })
})

describe('agentModeVoiceIntent — grammar precedence', () => {
  it('never re-parses a dictated note body', () => {
    const intent = parse('note that we should approve the run and cancel the job, detail 100')
    expect(intent).toEqual({
      kind: 'note',
      target: { kind: 'current' },
      body: 'we should approve the run and cancel the job, detail 100',
    })
  })

  it('preserves the body verbatim — case, diacritics, and punctuation', () => {
    expect(parse('Notiz: Der Fixture-Lauf hängt bei „Fall 84“.')).toEqual({
      kind: 'note',
      target: { kind: 'current' },
      body: 'Der Fixture-Lauf hängt bei „Fall 84“.',
    })
  })

  it.each([
    ['note the retry loop is hot', 'the retry loop is hot'],
    ['add a note on this: fixture 84 fails', 'fixture 84 fails'],
    ['internal note fixture 84 fails', 'fixture 84 fails'],
    ['notiere den Ausfall', 'den Ausfall'],
    ['interne Notiz: Deployment hängt', 'Deployment hängt'],
  ])('binds %s to the current selection', (text, body) => {
    expect(parse(text)).toEqual({ kind: 'note', target: { kind: 'current' }, body })
  })

  it('retargets a note only through an explicit preposition', () => {
    expect(parse('note on PAI-808 that the fixture is flaky')).toEqual({
      kind: 'note',
      target: { kind: 'issue', issueKey: 'PAI-808' },
      body: 'the fixture is flaky',
    })
    // Without a preposition every word stays in the body, so a word that
    // merely looks like a key cannot silently retarget the note.
    expect(parse('note case84 fails on retry')).toEqual({
      kind: 'note',
      target: { kind: 'current' },
      body: 'case84 fails on retry',
    })
  })

  it('reports an empty body instead of inventing one', () => {
    expect(parse('note')).toEqual({ kind: 'note', target: { kind: 'current' }, body: '' })
    expect(resolveVoiceIntent(parse('note'), ctx({ selectedId: base.id })))
      .toEqual({ kind: 'rejected', reason: 'empty_note' })
  })

  it('lets an explicit select verb outrank the filter vocabulary', () => {
    expect(parse('select PAI-815')).toEqual({ kind: 'select_issue', issueKey: 'PAI-815' })
    expect(parse('blocked')).toEqual({ kind: 'filter', filter: { type: 'health', health: 'blocked' } })
    expect(parse('select blocked')).toEqual({ kind: 'select_named', field: 'any', text: 'blocked' })
  })

  it('separates detail level from show-details and overview from portfolio status', () => {
    expect(parse('detail 100')).toEqual({ kind: 'detail', level: 100 })
    expect(parse('detail')).toEqual({ kind: 'show_details' })
    expect(parse('overview')).toEqual({ kind: 'detail', level: 100 })
    expect(parse('status overview')).toEqual({ kind: 'read_status', scope: 'portfolio' })
  })

  it('strips fillers and display verbs without changing the intent', () => {
    expect(parse('please show me PAI-808')).toEqual({ kind: 'select_issue', issueKey: 'PAI-808' })
    expect(parse('bitte zeig mir die Details')).toEqual({ kind: 'show_details' })
    expect(parse('hey paimos, next please')).toEqual({ kind: 'step', direction: 'next' })
  })

  it.each(['', '   ', '…', 'make me a sandwich', 'blah blah blah'])('returns unknown for %s', (text) => {
    expect(parse(text).kind).toBe('unknown')
    expect(resolveVoiceIntent(parse(text), ctx())).toEqual({ kind: 'rejected', reason: 'unknown_command' })
  })
})

describe('agentModeVoiceIntent — clarification candidates', () => {
  it.each<[string, number]>([
    ['number one', 1],
    ['option 2', 2],
    ['candidate three', 3],
    ['Nummer zwei', 2],
    ['Kandidat 3', 3],
  ])('parses the explicit choice %s', (text, index) => {
    expect(parse(text, 3)).toEqual({ kind: 'candidate', index })
  })

  it('reads a bare numeral as a choice only while a clarification is open', () => {
    expect(parse('two', 3)).toEqual({ kind: 'candidate', index: 2 })
    expect(parse('zwei', 3)).toEqual({ kind: 'candidate', index: 2 })
    expect(parse('two', 0).kind).toBe('unknown')
    // A slot that does not exist is never invented.
    expect(parse('three', 2).kind).toBe('unknown')
  })

  it('leaves the resolution of a chosen number to the state machine', () => {
    expect(resolveVoiceIntent(parse('number one', 3), ctx()))
      .toEqual({ kind: 'rejected', reason: 'unknown_command' })
  })

  it('does not mistake a detail level for a choice', () => {
    expect(parse('detail 1', 3)).toEqual({ kind: 'detail', level: 1 })
    expect(parse('detail 100', 3)).toEqual({ kind: 'detail', level: 100 })
  })
})

describe('agentModeVoiceIntent — resolver', () => {
  it('resolves an exact issue key against the authorized snapshot only', () => {
    expect(resolveVoiceIntent({ kind: 'select_issue', issueKey: 'PAI-812' }, ctx()))
      .toEqual({ kind: 'command', command: { type: 'select', deliveryId: 'dlv-812' } })
    expect(resolveVoiceIntent({ kind: 'select_issue', issueKey: 'PAI-999' }, ctx()))
      .toEqual({ kind: 'rejected', reason: 'no_match' })
    // Present in the world, absent from THIS authorized set → no match.
    const narrowed = ctx({ deliveries: fixtures.filter((x) => x.issueKey !== 'PAI-812') })
    expect(resolveVoiceIntent({ kind: 'select_issue', issueKey: 'PAI-812' }, narrowed))
      .toEqual({ kind: 'rejected', reason: 'no_match' })
  })

  it('resolves an exact title, agent, and lane', () => {
    expect(resolveVoiceIntent({ kind: 'select_named', field: 'title', text: 'Anchor drift diagnostics' }, ctx()))
      .toEqual({ kind: 'command', command: { type: 'select', deliveryId: 'dlv-819' } })
    expect(resolveVoiceIntent({ kind: 'select_named', field: 'agent', text: 'Janus' }, ctx()).kind).toBe('clarify')
    expect(resolveVoiceIntent({ kind: 'select_named', field: 'lane', text: 'Access & trust' }, ctx()))
      .toEqual({ kind: 'command', command: { type: 'select', deliveryId: 'dlv-821' } })
  })

  it('prefers an exact match in a later field over a partial match in an earlier one', () => {
    const list = [
      d({ id: 'dlv-a', issueKey: 'AAA-1', title: 'Codex harness notes', actor: { name: 'janus', label: 'Janus', kind: 'system' } }),
      d({ id: 'dlv-b', issueKey: 'AAA-2', title: 'Something else', actor: { name: 'codex', label: 'Codex', kind: 'agent' } }),
    ]
    // "codex" is only a word inside dlv-a's title, but it is dlv-b's exact agent.
    expect(resolveVoiceIntent({ kind: 'select_named', field: 'any', text: 'codex' }, ctx({ deliveries: list })))
      .toEqual({ kind: 'command', command: { type: 'select', deliveryId: 'dlv-b' } })
  })

  it('matches a spoken issue number but never an arbitrary key fragment', () => {
    expect(resolveVoiceIntent({ kind: 'select_named', field: 'any', text: '819' }, ctx()))
      .toEqual({ kind: 'command', command: { type: 'select', deliveryId: 'dlv-819' } })
    expect(resolveVoiceIntent({ kind: 'select_named', field: 'any', text: 'PA' }, ctx()))
      .toEqual({ kind: 'rejected', reason: 'no_match' })
  })

  it('offers at most three candidates, in canonical order, and reports the true count', () => {
    const many = [
      d({ id: 'dlv-1', issueKey: 'AAA-1', title: 'Retrieval evaluation', attention: { level: 0, reason: null, since: null }, eta: null }),
      d({ id: 'dlv-2', issueKey: 'AAA-2', title: 'Retrieval evaluation', attention: { level: 3, reason: 'blocked', since: null }, eta: null }),
      d({ id: 'dlv-3', issueKey: 'AAA-3', title: 'Retrieval evaluation', attention: { level: 1, reason: 'unverified', since: null }, eta: null }),
      d({ id: 'dlv-4', issueKey: 'AAA-4', title: 'Retrieval evaluation', attention: { level: 3, reason: 'blocked', since: null }, eta: null }),
      d({ id: 'dlv-5', issueKey: 'AAA-5', title: 'Retrieval evaluation', attention: { level: 0, reason: null, since: null }, eta: null }),
    ]
    const resolution = resolveVoiceIntent(
      { kind: 'select_named', field: 'title', text: 'retrieval evaluation' },
      ctx({ deliveries: many }),
    )
    expect(resolution.kind).toBe('clarify')
    if (resolution.kind !== 'clarify') return
    expect(resolution.candidates).toHaveLength(MAX_VOICE_CANDIDATES)
    // Attention ↓, then stable id ↑ — never source order, never a guess.
    expect(resolution.candidates.map((c) => c.deliveryId)).toEqual(['dlv-2', 'dlv-4', 'dlv-3'])
    expect(resolution.candidates.map((c) => c.index)).toEqual([1, 2, 3])
    expect(resolution.matchCount).toBe(5)
    expect(resolution.truncated).toBe(true)
    // Reversing the input must not reorder the offer.
    const reversed = resolveVoiceIntent(
      { kind: 'select_named', field: 'title', text: 'retrieval evaluation' },
      ctx({ deliveries: [...many].reverse() }),
    )
    expect(reversed).toEqual(resolution)
  })

  it('does not mark an exhaustive two-way ambiguity as truncated', () => {
    const resolution = resolveVoiceIntent({ kind: 'select_named', field: 'lane', text: 'Agent Mode' }, ctx())
    expect(resolution.kind).toBe('clarify')
    if (resolution.kind !== 'clarify') return
    // Same attention level, so the soonest trusted landing leads.
    expect(resolution.candidates.map((c) => c.issueKey)).toEqual(['PAI-818', 'PAI-812'])
    expect(resolution.matchCount).toBe(2)
    expect(resolution.truncated).toBe(false)
  })

  it('resolves pronouns only to the current authorized selection', () => {
    expect(resolveVoiceIntent({ kind: 'select_current' }, ctx({ selectedId: 'dlv-815' })))
      .toEqual({ kind: 'command', command: { type: 'select', deliveryId: 'dlv-815' } })
    expect(resolveVoiceIntent({ kind: 'select_current' }, ctx({ selectedId: null })))
      .toEqual({ kind: 'rejected', reason: 'no_selection' })
    // A remembered id that is no longer authorized is not a selection.
    expect(resolveVoiceIntent({ kind: 'select_current' }, ctx({ selectedId: 'dlv-gone' })))
      .toEqual({ kind: 'rejected', reason: 'no_selection' })
  })

  it.each<VoiceIntent>([
    { kind: 'select_current' },
    { kind: 'show_details' },
    { kind: 'read_status', scope: 'selection' },
    { kind: 'note', target: { kind: 'current' }, body: 'anything' },
  ])('refuses %o without a selection', (intent) => {
    expect(resolveVoiceIntent(intent, ctx({ selectedId: null })))
      .toEqual({ kind: 'rejected', reason: 'no_selection' })
  })

  it('reads portfolio status without a selection but never invents a delivery', () => {
    expect(resolveVoiceIntent({ kind: 'read_status', scope: 'portfolio' }, ctx({ selectedId: null })))
      .toEqual({ kind: 'command', command: { type: 'read_status', scope: 'portfolio', deliveryId: null } })
  })

  it('never lets a filter steal the selection', () => {
    for (const text of ['show blocked', 'stale', 'filter project PAI', 'search for reconnect', 'clear filters']) {
      const resolution = resolveVoiceIntent(parse(text), ctx({ selectedId: 'dlv-812' }))
      expect(resolution.kind).toBe('command')
      if (resolution.kind !== 'command') return
      expect(['set_filters', 'clear_filters']).toContain(resolution.command.type)
      expect(JSON.stringify(resolution.command)).not.toContain('dlv-')
    }
  })

  it('resolves a project filter by key or name and always clears the lane with it', () => {
    // The patch is asserted whole: projectId and laneKey travel together,
    // exactly as AgentModeFilterBar's own project change does.
    expect(resolveVoiceIntent({ kind: 'filter', filter: { type: 'project', text: 'RUN' } }, ctx()))
      .toEqual({ kind: 'command', command: { type: 'set_filters', patch: { projectId: 9, laneKey: null } } })
    expect(resolveVoiceIntent({ kind: 'filter', filter: { type: 'project', text: 'Agent runtime' } }, ctx()))
      .toEqual({ kind: 'command', command: { type: 'set_filters', patch: { projectId: 9, laneKey: null } } })
    expect(resolveVoiceIntent({ kind: 'filter', filter: { type: 'project', text: 'nope' } }, ctx()))
      .toEqual({ kind: 'rejected', reason: 'no_match' })
  })

  it('resolves an authorized project that has no row in the current result set', () => {
    // Filtering to a project you cannot currently see is the whole point of
    // the command, so the catalog — not the visible rows — is the oracle.
    const onlyProjectPai = fixtures.filter((x) => x.lane.projectId === 6)
    expect(onlyProjectPai.every((x) => x.lane.projectId !== 9)).toBe(true)
    expect(resolveVoiceIntent(
      { kind: 'filter', filter: { type: 'project', text: 'Agent runtime' } },
      ctx({ deliveries: onlyProjectPai }),
    )).toEqual({ kind: 'command', command: { type: 'set_filters', patch: { projectId: 9, laneKey: null } } })
  })

  it('never falls back to the visible rows when the catalog is empty', () => {
    // Every visible row names project RUN, and it still does not resolve:
    // there is no second, hidden project oracle.
    expect(resolveVoiceIntent(
      { kind: 'filter', filter: { type: 'project', text: 'Agent runtime' } },
      ctx({ deliveries: fixtures.filter((x) => x.lane.projectId === 9), projectCatalog: [] }),
    )).toEqual({ kind: 'rejected', reason: 'no_project_catalog' })
  })

  it('refuses to guess between two catalog projects that share a word', () => {
    const ambiguous: VoiceProjectRef[] = [
      { projectId: 1, projectKey: 'AAA', projectName: 'Platform core' },
      { projectId: 2, projectKey: 'BBB', projectName: 'Platform edge' },
    ]
    expect(resolveVoiceIntent(
      { kind: 'filter', filter: { type: 'project', text: 'platform' } },
      ctx({ projectCatalog: ambiguous }),
    )).toEqual({ kind: 'rejected', reason: 'ambiguous_project' })
    // An exact key still wins outright over the shared partial.
    expect(resolveVoiceIntent(
      { kind: 'filter', filter: { type: 'project', text: 'BBB' } },
      ctx({ projectCatalog: ambiguous }),
    )).toEqual({ kind: 'command', command: { type: 'set_filters', patch: { projectId: 2, laneKey: null } } })
  })

  it('passes health and query filters through unchanged', () => {
    expect(resolveVoiceIntent({ kind: 'filter', filter: { type: 'health', health: 'attention' } }, ctx()))
      .toEqual({ kind: 'command', command: { type: 'set_filters', patch: { health: 'attention' } } })
    expect(resolveVoiceIntent({ kind: 'filter', filter: { type: 'query', text: '  reconnect  ' } }, ctx()))
      .toEqual({ kind: 'command', command: { type: 'set_filters', patch: { query: 'reconnect' } } })
    expect(resolveVoiceIntent({ kind: 'filter', filter: { type: 'query', text: '   ' } }, ctx()))
      .toEqual({ kind: 'rejected', reason: 'no_match' })
  })

  it('binds a note to the current selection and trims only the outer whitespace', () => {
    expect(resolveVoiceIntent(
      { kind: 'note', target: { kind: 'current' }, body: '  fixture 84  fails  ' },
      ctx({ selectedId: 'dlv-812' }),
    )).toEqual({
      kind: 'command',
      command: { type: 'draft_note', deliveryId: 'dlv-812', body: 'fixture 84  fails' },
    })
  })

  it('accepts a named note target only when it IS the current selection', () => {
    expect(resolveVoiceIntent(
      { kind: 'note', target: { kind: 'issue', issueKey: 'PAI-812' }, body: 'x' },
      ctx({ selectedId: 'dlv-812' }),
    )).toEqual({ kind: 'command', command: { type: 'draft_note', deliveryId: 'dlv-812', body: 'x' } })
  })

  it('never silently rebinds a note to a delivery other than the selection', () => {
    // Authorized, but not selected: answered with "select it first", because
    // a note landing on the wrong ticket is the one mistake worth being loud
    // about.
    expect(resolveVoiceIntent(
      { kind: 'note', target: { kind: 'issue', issueKey: 'PAI-815' }, body: 'x' },
      ctx({ selectedId: 'dlv-812' }),
    )).toEqual({ kind: 'rejected', reason: 'note_target_not_selected' })
    // Not authorized at all: indistinguishable from not existing.
    expect(resolveVoiceIntent(
      { kind: 'note', target: { kind: 'issue', issueKey: 'ZZZ-1' }, body: 'x' },
      ctx({ selectedId: 'dlv-812' }),
    )).toEqual({ kind: 'rejected', reason: 'no_match' })
  })

  it.each<[string, boolean | null]>([
    ['withheld', false],
    ['unclaimed', null],
  ])('refuses to draft a note when the comment capability is %s', (_label, comment) => {
    const list = [d({ capabilities: { ...base.capabilities, comment } }), ...fixtures.slice(1)]
    expect(resolveVoiceIntent(
      { kind: 'note', target: { kind: 'current' }, body: 'the fixture is flaky' },
      ctx({ deliveries: list, selectedId: base.id }),
    )).toEqual({ kind: 'rejected', reason: 'note_not_permitted' })
  })

  it('checks the comment capability of the SELECTION, not of the named target', () => {
    // PAI-812 is selected and may not be commented on; naming the delivery
    // that may be does not launder the refusal.
    const list = [d({ capabilities: { ...base.capabilities, comment: false } }), ...fixtures.slice(1)]
    expect(resolveVoiceIntent(
      { kind: 'note', target: { kind: 'issue', issueKey: 'PAI-815' }, body: 'x' },
      ctx({ deliveries: list, selectedId: base.id }),
    )).toEqual({ kind: 'rejected', reason: 'note_target_not_selected' })
    expect(resolveVoiceIntent(
      { kind: 'note', target: { kind: 'issue', issueKey: base.issueKey }, body: 'x' },
      ctx({ deliveries: list, selectedId: base.id }),
    )).toEqual({ kind: 'rejected', reason: 'note_not_permitted' })
  })

  it('is pure — resolving never mutates the snapshot or the selection', () => {
    const list = [...fixtures]
    const snapshot = JSON.stringify(list)
    const context = ctx({ deliveries: list, selectedId: 'dlv-812' })
    resolveVoiceIntent(parse('select agent codex'), context)
    resolveVoiceIntent(parse('note something'), context)
    resolveVoiceIntent(parse('show blocked'), context)
    expect(JSON.stringify(list)).toBe(snapshot)
    expect(context.selectedId).toBe('dlv-812')
  })
})
