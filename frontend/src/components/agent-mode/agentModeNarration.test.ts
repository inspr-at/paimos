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

import en from '@/i18n/en'
import de from '@/i18n/de'
import { makeFixtureSnapshot } from '@/services/agentModeFixtures'
import { normalizeWireSnapshot } from '@/services/agentModeTransport'
import type { Delivery } from '@/services/agentMode'
import {
  NARRATION_I18N_KEYS,
  NARRATION_SPEECH_TEMPLATES,
  NARRATION_SPEECH_WIRE_FIELDS,
  NARRATION_VISUAL_ONLY_NOTICES,
  buildClarificationSpeech,
  buildDeliveryNarration,
  buildNoteReadySpeech,
  buildStatusSpeech,
  factsExcludeProse,
  isSafeSpeechRequest,
  narrationProseFields,
  toSpeechWire,
  type NarrationFact,
  type NarrationSpeechRequest,
} from './agentModeNarration'

const NOW = Date.parse('2026-08-20T13:48:00Z')

const fixtures = normalizeWireSnapshot(makeFixtureSnapshot(10), 0).deliveries
const base = fixtures[0]

function d(patch: Partial<Delivery>): Delivery {
  return { ...base, ...patch }
}

const healthy = {
  health: 'healthy' as const,
  activity: { kind: 'working' as const, text: 'Writing membership checks', since: null },
  freshness: { state: 'fresh' as const, lastReportAt: '2026-08-20T13:47:00Z' },
}
const trustedProgress = { percent: 64, trusted: true, confidence: 'high' as const, source: null, basis: null, revision: 1 }
const trustedEta = {
  landingAt: '2026-08-20T14:40:00Z',
  optimisticAt: '2026-08-20T14:30:00Z',
  pessimisticAt: '2026-08-20T15:00:00Z',
  trusted: true,
  confidence: 'high' as const,
  basis: null,
  calculatedAt: null,
}

function narrate(delivery: Delivery, degraded = false) {
  return buildDeliveryNarration(delivery, { locale: 'en', serverNowMs: NOW, degraded })
}

function keys(facts: readonly NarrationFact[]): string[] {
  return facts.map((f) => f.key)
}

function factFor(facts: readonly NarrationFact[], kind: NarrationFact['kind']): NarrationFact | undefined {
  return facts.find((f) => f.kind === kind)
}

function lookup(bundle: unknown, key: string): unknown {
  return key.split('.').reduce<unknown>((node, part) => {
    if (node && typeof node === 'object' && part in (node as Record<string, unknown>)) {
      return (node as Record<string, unknown>)[part]
    }
    return undefined
  }, bundle)
}

/** Keys this slice hands off to the i18n owner; everything else is reused. */
const NEW_I18N_KEYS = NARRATION_I18N_KEYS.filter((key) => (
  key.startsWith('agentMode.narration.blockers')
  || key.startsWith('agentMode.narration.suppressed')
  || key.startsWith('agentMode.narration.disagreement')
  || key.startsWith('agentMode.narration.confidence.')
  || key.startsWith('agentMode.narration.reporter.')
  || key === 'agentMode.stageStatus.cancelled'
  || key === 'agentMode.stageStatus.draft_ready'
))

describe('agentModeNarration — trusted structured facts', () => {
  it('narrates stage, status, activity kind, freshness, and exact estimate values', () => {
    const { facts } = narrate(d({ ...healthy, progress: trustedProgress, eta: trustedEta }))
    expect(keys(facts)).toContain('agentMode.stage.specification')
    expect(keys(facts)).toContain('agentMode.activity.working')
    expect(keys(facts)).toContain('agentMode.freshness.fresh')
    expect(factFor(facts, 'stage_position')?.params).toEqual({ i: 1, n: 5 })
    expect(factFor(facts, 'estimate_percent')).toEqual({
      kind: 'estimate_percent',
      key: 'agentMode.estimate.percent',
      params: { n: 64 },
    })
    const landing = factFor(facts, 'estimate_landing')
    expect(landing?.key).toBe('agentMode.narration.selectionEta')
    expect(landing?.params.remaining).toBe('52 min')
    expect(keys(facts)).not.toContain('agentMode.card.retained')
  })

  it('narrates the current stage row status and the trust reporter', () => {
    const { facts } = narrate(d({ ...healthy }))
    expect(factFor(facts, 'stage_status')?.key).toBe(`agentMode.stageStatus.${base.stages[0].status}`)
    expect(factFor(facts, 'reporter')?.key).toBe(`agentMode.narration.reporter.${base.trust.reporterKind}`)
  })

  it('emits a speech request that carries identities and no text at all', () => {
    const delivery = d({ ...healthy, progress: trustedProgress, eta: trustedEta })
    const { speech } = narrate(delivery)
    // Asserted whole: the request has exactly these fields, so a trust
    // revision cannot reappear on the way to TTS.
    expect(speech).toEqual({
      template: 'status',
      locale: 'en',
      deliveryId: delivery.id,
      deliveryRevision: delivery.deliveryRevision,
      candidateIds: [],
    })
    expect(Object.keys(speech!).sort()).toEqual([
      'candidateIds', 'deliveryId', 'deliveryRevision', 'locale', 'template',
    ])
    expect(isSafeSpeechRequest(speech!)).toBe(true)
    const serialized = JSON.stringify(speech)
    expect(serialized).not.toContain(delivery.title)
    expect(serialized).not.toContain('Writing membership checks')
    expect(serialized).not.toContain(delivery.trustRevision!)
  })

  it('stays silent when the delivery carries no read-model revision', () => {
    // Without a revision the server cannot reauthorize what it is about to
    // speak, so there is nothing safe to say.
    expect(narrate(d({ ...healthy, deliveryRevision: null })).speech).toBeNull()
    expect(narrate(d({ ...healthy, deliveryRevision: '' })).speech).toBeNull()
  })
})

describe('agentModeNarration — withheld values', () => {
  it('reduces a low-confidence estimate to a range and withholds the percent', () => {
    const { facts } = narrate(d({
      ...healthy,
      progress: { ...trustedProgress, confidence: 'low' },
      eta: { ...trustedEta, confidence: 'low' },
    }))
    expect(factFor(facts, 'estimate_range')?.key).toBe('agentMode.narration.selectionRange')
    expect(factFor(facts, 'estimate_landing')).toBeUndefined()
    expect(factFor(facts, 'estimate_percent')).toBeUndefined()
    expect(keys(facts)).toContain('agentMode.estimate.withheld.low_confidence')
    // No point landing and no derived remaining duration leak through.
    expect(JSON.stringify(facts)).not.toContain('52 min')
  })

  it.each<[string, Partial<Delivery>, string]>([
    ['blocked', { health: 'blocked' }, 'blocked'],
    ['waiting', { activity: { kind: 'waiting', text: null, since: null } }, 'waiting'],
    ['stale', { freshness: { state: 'stale', lastReportAt: null } }, 'stale'],
    ['suppressed by policy', { suppressionCodes: ['owner_override'] }, 'suppressed'],
  ])('withholds every exact value while %s', (_label, patch, reason) => {
    const { facts } = narrate(d({ ...healthy, progress: trustedProgress, eta: trustedEta, ...patch }))
    expect(factFor(facts, 'estimate_percent')).toBeUndefined()
    expect(factFor(facts, 'estimate_landing')).toBeUndefined()
    expect(factFor(facts, 'estimate_range')).toBeUndefined()
    expect(keys(facts)).toContain(`agentMode.estimate.withheld.${reason}`)
    expect(JSON.stringify(facts)).not.toContain('64')
  })

  it('withholds every exact value and stays silent while the snapshot is degraded', () => {
    const delivery = d({ ...healthy, progress: trustedProgress, eta: trustedEta })
    const { facts, speech } = narrate(delivery, true)
    expect(factFor(facts, 'estimate_percent')).toBeUndefined()
    expect(factFor(facts, 'estimate_landing')).toBeUndefined()
    expect(keys(facts)).toContain('agentMode.estimate.withheld.offline')
    expect(keys(facts)).toContain('agentMode.card.retained')
    // A retained snapshot is never spoken: the server would voice current
    // facts this client cannot vouch for.
    expect(speech).toBeNull()
  })

  it('reports one withheld reason when percent and ETA share it, and two when they differ', () => {
    const shared = narrate(d({ ...healthy, progress: null, eta: null })).facts
      .filter((f) => f.kind === 'estimate_withheld')
    expect(shared.map((f) => f.key)).toEqual(['agentMode.estimate.withheld.none'])

    const split = narrate(d({
      ...healthy,
      progress: { ...trustedProgress, trusted: false },
      eta: { ...trustedEta, confidence: 'none' },
    })).facts.filter((f) => f.kind === 'estimate_withheld')
    expect(split.map((f) => f.key)).toEqual([
      'agentMode.estimate.withheld.untrusted',
      'agentMode.estimate.withheld.low_confidence',
    ])
  })

  it('reports attention and blocker counts as facts, never as their text', () => {
    const { facts } = narrate(d({
      attention: { level: 3, reason: 'blocked', since: null },
      blockers: [
        { kind: 'dependency', text: 'permissions fixture fails on case 84' },
        { kind: 'review', text: 'waiting on a human decision' },
      ],
    }))
    expect(factFor(facts, 'attention')?.key).toBe('agentMode.aggregate.reason.blocked')
    expect(factFor(facts, 'blockers')).toEqual({
      kind: 'blockers',
      key: 'agentMode.narration.blockers',
      params: { n: 2 },
    })
    expect(JSON.stringify(facts)).not.toContain('case 84')
  })

  it('maps an unrecognized attention reason to the neutral bucket', () => {
    const { facts } = narrate(d({ attention: { level: 2, reason: 'because the vibes are off', since: null } }))
    expect(factFor(facts, 'attention')?.key).toBe('agentMode.aggregate.reason.other')
  })

  it('keeps only enum-shaped suppression and disagreement codes', () => {
    const { facts } = narrate(d({
      suppressionCodes: ['owner_override', 'Reporter said it is fine, honestly'],
      disagreementCodes: ['stage_vs_owner'],
    }))
    expect(factFor(facts, 'suppression')?.params).toEqual({ n: 1, codes: 'owner_override' })
    expect(factFor(facts, 'disagreement')?.params).toEqual({ n: 1 })
    expect(JSON.stringify(facts)).not.toContain('honestly')
  })

  it('omits the suppression fact entirely when no code survives', () => {
    const { facts } = narrate(d({ suppressionCodes: ['Not an enum at all'], disagreementCodes: [] }))
    expect(factFor(facts, 'suppression')).toBeUndefined()
    expect(factFor(facts, 'disagreement')).toBeUndefined()
  })
})

describe('agentModeNarration — no prose, no reassurance', () => {
  const noisy = d({
    ...healthy,
    title: 'Workspace-level access controls',
    statusText: 'Decision requested 12 min ago',
    activity: { kind: 'working', text: 'Investigating 3 failed assertions', since: null },
    attention: { level: 2, reason: 'Someone has to look at this soon', since: null },
    blockers: [{ kind: 'dependency', text: 'permissions fixture fails on case 84' }],
    progress: { ...trustedProgress, basis: 'owner said so' },
    eta: { ...trustedEta, basis: 'gut feeling' },
  })

  it('never lets server prose become a narration parameter', () => {
    const { facts } = narrate(noisy)
    expect(factsExcludeProse(facts, noisy)).toBe(true)
    const serialized = JSON.stringify(facts)
    for (const prose of narrationProseFields(noisy)) expect(serialized).not.toContain(prose)
  })

  it('collects every free-text field as prose, including the basis strings', () => {
    const prose = narrationProseFields(noisy)
    expect(prose).toContain('Investigating 3 failed assertions')
    expect(prose).toContain('Decision requested 12 min ago')
    expect(prose).toContain('permissions fixture fails on case 84')
    expect(prose).toContain('owner said so')
    expect(prose).toContain('gut feeling')
    expect(prose).toContain('Workspace-level access controls')
  })

  it('detects a fact that smuggles prose in', () => {
    const smuggled: NarrationFact[] = [
      { kind: 'activity', key: 'agentMode.activity.working', params: { text: 'gut feeling' } },
    ]
    expect(factsExcludeProse(smuggled, noisy)).toBe(false)
  })

  it('emits only keys from the declared contract', () => {
    const declared = new Set(NARRATION_I18N_KEYS)
    for (const delivery of fixtures) {
      for (const degraded of [false, true]) {
        for (const fact of buildDeliveryNarration(delivery, { locale: 'de', serverNowMs: NOW, degraded }).facts) {
          expect(declared.has(fact.key)).toBe(true)
        }
      }
    }
  })

  it('reuses existing EN and DE message keys for everything but the declared hand-off', () => {
    const handoff = new Set(NEW_I18N_KEYS)
    expect(NEW_I18N_KEYS.length).toBeGreaterThan(0)
    for (const key of NARRATION_I18N_KEYS) {
      if (handoff.has(key)) continue
      expect(lookup(en, key), `${key} missing from en`).toBeDefined()
      expect(lookup(de, key), `${key} missing from de`).toBeDefined()
    }
  })

  it('has no speech template that could promise completion or reassure', () => {
    const forbidden = [
      'on_track', 'ontrack', 'almost', 'soon', 'guarantee', 'fine', 'all_good', 'no_problem',
      'will_finish', 'complete_soon', 'success', 'nearly', 'safe',
    ]
    for (const template of NARRATION_SPEECH_TEMPLATES) {
      for (const word of forbidden) expect(template).not.toContain(word)
    }
  })
})

describe('agentModeNarration — the frozen speech contract', () => {
  const subject = { id: 'dlv-812', deliveryRevision: 'delivery:812:1' }

  it('offers exactly three templates and nothing else', () => {
    expect([...NARRATION_SPEECH_TEMPLATES]).toEqual(['status', 'note_ready', 'clarification'])
  })

  it('keeps every other notice visual — none of them has a template', () => {
    const templates = new Set<string>(NARRATION_SPEECH_TEMPLATES)
    for (const notice of NARRATION_VISUAL_ONLY_NOTICES) expect(templates.has(notice)).toBe(false)
  })

  it('anchors status and note_ready on the current delivery and its revision', () => {
    expect(buildStatusSpeech(subject, 'de')).toEqual({
      template: 'status',
      locale: 'de',
      deliveryId: 'dlv-812',
      deliveryRevision: 'delivery:812:1',
      candidateIds: [],
    })
    expect(buildNoteReadySpeech(subject, 'en')?.template).toBe('note_ready')
    // No delivery revision → no speech, for either template.
    expect(buildStatusSpeech({ id: 'dlv-812', deliveryRevision: null }, 'en')).toBeNull()
    expect(buildNoteReadySpeech({ id: 'dlv-812', deliveryRevision: null }, 'en')).toBeNull()
    expect(buildNoteReadySpeech({ id: '', deliveryRevision: 'delivery:812:1' }, 'en')).toBeNull()
  })

  it('speaks a note by identity only — never the dictated body', () => {
    const request = buildNoteReadySpeech(subject, 'de')
    expect(JSON.stringify(request)).not.toContain('vault')
    expect(JSON.stringify({
      note: 'the production credentials are in the old vault',
      request,
    })).toContain('vault')
    expect(Object.keys(request!)).not.toContain('body')
    expect(Object.keys(request!)).not.toContain('text')
  })

  it('carries one to three current candidate ids on a clarification, and speaks no titles', () => {
    const candidates = ['dlv-1', 'dlv-2', 'dlv-3'].map((deliveryId) => ({ deliveryId, title: 'secret title' }))
    expect(buildClarificationSpeech(subject, candidates, 'en')).toEqual({
      template: 'clarification',
      locale: 'en',
      deliveryId: 'dlv-812',
      deliveryRevision: 'delivery:812:1',
      candidateIds: ['dlv-1', 'dlv-2', 'dlv-3'],
    })
    expect(JSON.stringify(buildClarificationSpeech(subject, candidates, 'en'))).not.toContain('secret title')
    // Nothing to choose between, and more than the operator was shown, both
    // fail closed rather than speaking a silently truncated list.
    expect(buildClarificationSpeech(subject, [], 'en')).toBeNull()
    expect(buildClarificationSpeech(subject, [...candidates, { deliveryId: 'dlv-4' }], 'en')).toBeNull()
    // A clarification still needs the current delivery it is anchored on.
    expect(buildClarificationSpeech({ id: 'dlv-812', deliveryRevision: null }, candidates, 'en')).toBeNull()
  })

  it('rejects any request that is not an enum template plus opaque identities', () => {
    const ok: NarrationSpeechRequest = {
      template: 'status',
      locale: 'en',
      deliveryId: 'dlv-812',
      deliveryRevision: 'delivery:812:1',
      candidateIds: [],
    }
    expect(isSafeSpeechRequest(ok)).toBe(true)
    expect(isSafeSpeechRequest({ ...ok, deliveryId: 'the note says: rotate the key' })).toBe(false)
    expect(isSafeSpeechRequest({ ...ok, deliveryId: '' })).toBe(false)
    expect(isSafeSpeechRequest({ ...ok, deliveryRevision: '' })).toBe(false)
    expect(isSafeSpeechRequest({ ...ok, deliveryId: 'x'.repeat(129) })).toBe(false)
    // The revision has its own, longer bound — see the parity suite.
    expect(isSafeSpeechRequest({ ...ok, deliveryRevision: 'x'.repeat(257) })).toBe(false)
    expect(isSafeSpeechRequest({ ...ok, template: 'delivery_status' as never })).toBe(false)
    expect(isSafeSpeechRequest({ ...ok, template: 'portfolio_status' as never })).toBe(false)
    expect(isSafeSpeechRequest({ ...ok, locale: 'fr' as never })).toBe(false)
    // Candidates belong to the clarification template and only there.
    expect(isSafeSpeechRequest({ ...ok, candidateIds: ['dlv-813'] })).toBe(false)
    const clarify: NarrationSpeechRequest = { ...ok, template: 'clarification', candidateIds: ['dlv-813'] }
    expect(isSafeSpeechRequest(clarify)).toBe(true)
    expect(isSafeSpeechRequest({ ...clarify, candidateIds: [] })).toBe(false)
    expect(isSafeSpeechRequest({ ...clarify, candidateIds: ['a', 'b', 'c', 'd'] })).toBe(false)
    expect(isSafeSpeechRequest({ ...clarify, candidateIds: ['dlv 813 — read this aloud'] })).toBe(false)
  })

  it('returns null rather than an unsafe request when an identity is not opaque', () => {
    const { speech } = narrate(d({ ...healthy, id: 'dlv 812 — please read this note aloud' }))
    expect(speech).toBeNull()
  })
})

describe('agentModeNarration — backend identity and revision parity', () => {
  const request: NarrationSpeechRequest = {
    template: 'status',
    locale: 'en',
    deliveryId: 'dlv-812',
    deliveryRevision: 'delivery:812:1',
    candidateIds: [],
  }

  // `^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$` — deliveryKeyPattern in
  // backend/agentmode/filters.go, safeOpaqueKey in backend/delivery/store.go,
  // and the delivery_key CHECK constraint in backend/db/db.go.
  const ACCEPTED_IDS: Array<[string, string]> = [
    ['a single alphanumeric', 'a'],
    ['the production shape', 'dlv-812'],
    ['a slash, which a lane-shaped key needs', 'project:6/epic:4655'],
    ['every allowed punctuation after an alphanumeric first character', 'a0._:/-'],
    ['exactly 128 characters', `d${'x'.repeat(127)}`],
  ]
  const REJECTED_IDS: Array<[string, string]> = [
    ['empty', ''],
    ['a leading hyphen', '-dlv-812'],
    ['a leading dot', '.dlv-812'],
    ['a leading underscore', '_dlv-812'],
    ['a leading colon', ':dlv-812'],
    ['an at sign', 'dlv@812'],
    ['a pipe', 'dlv|812'],
    ['a tilde', 'dlv~812'],
    ['a plus', 'dlv+812'],
    ['a space', 'dlv 812'],
    ['129 characters', `d${'x'.repeat(128)}`],
  ]

  it.each(ACCEPTED_IDS)('accepts a delivery id with %s', (_label, deliveryId) => {
    expect(isSafeSpeechRequest({ ...request, deliveryId })).toBe(true)
  })

  it.each(REJECTED_IDS)('rejects a delivery id with %s', (_label, deliveryId) => {
    expect(isSafeSpeechRequest({ ...request, deliveryId })).toBe(false)
  })

  it.each(ACCEPTED_IDS)('accepts a candidate id with %s', (_label, candidateId) => {
    expect(isSafeSpeechRequest({ ...request, template: 'clarification', candidateIds: [candidateId] })).toBe(true)
  })

  it.each(REJECTED_IDS)('rejects a candidate id with %s', (_label, candidateId) => {
    expect(isSafeSpeechRequest({ ...request, template: 'clarification', candidateIds: [candidateId] })).toBe(false)
  })

  // A revision is not an id: printable ASCII, 1..256, built by
  // deliveryRevision() in backend/agentmode/trust.go as
  // `delivery:<identity>:<revision>:<sequence>`.
  it.each<[string, string]>([
    ['the production shape', 'delivery:812:1'],
    ['a revision over a long delivery key', `delivery:${'k'.repeat(128)}:12:34`],
    ['the lowest printable ASCII character', '!'],
    ['the highest printable ASCII character', '~'],
    ['punctuation an id may not carry', 'delivery:a@b|c+d=e:12:34'],
    ['exactly 256 characters', '!'.repeat(256)],
  ])('accepts a delivery revision with %s', (_label, deliveryRevision) => {
    expect(isSafeSpeechRequest({ ...request, deliveryRevision })).toBe(true)
  })

  it.each<[string, string]>([
    ['nothing at all', ''],
    ['257 characters', '!'.repeat(257)],
    ['a space', 'delivery:812 1'],
    ['a tab', 'delivery:812\t1'],
    ['a newline', 'delivery:812\n1'],
    ['a non-ASCII character', 'delivery:812:é'],
  ])('rejects a delivery revision with %s', (_label, deliveryRevision) => {
    expect(isSafeSpeechRequest({ ...request, deliveryRevision })).toBe(false)
  })

  it('validates ids and revisions with separate rules, not one shared guard', () => {
    // A 200-character revision is legitimate; a 200-character id is not.
    const long = `delivery:${'k'.repeat(150)}:12:34`
    expect(long.length).toBeGreaterThan(128)
    expect(isSafeSpeechRequest({ ...request, deliveryRevision: long })).toBe(true)
    expect(isSafeSpeechRequest({ ...request, deliveryId: long })).toBe(false)
    // …and the id rule is the stricter one on shape, not just on length.
    expect(isSafeSpeechRequest({ ...request, deliveryId: '@dlv' })).toBe(false)
    expect(isSafeSpeechRequest({ ...request, deliveryRevision: '@dlv' })).toBe(true)
  })

  it('carries the real fixture identities all the way to the wire', () => {
    const { speech } = narrate(d({ ...healthy }))
    expect(speech).not.toBeNull()
    expect(toSpeechWire(speech!)).toEqual({
      template: 'status',
      locale: 'en',
      delivery_id: base.id,
      delivery_revision: base.deliveryRevision,
      candidate_ids: [],
    })
  })
})

describe('agentModeNarration — the /voice/speak wire', () => {
  const request: NarrationSpeechRequest = {
    template: 'clarification',
    locale: 'de',
    deliveryId: 'dlv-812',
    deliveryRevision: 'delivery:812:1',
    candidateIds: ['dlv-813', 'dlv-815'],
  }

  it('maps exactly the five wire fields, in snake_case', () => {
    const wire = toSpeechWire(request)
    expect(wire).toEqual({
      template: 'clarification',
      locale: 'de',
      delivery_id: 'dlv-812',
      delivery_revision: 'delivery:812:1',
      candidate_ids: ['dlv-813', 'dlv-815'],
    })
    expect(Object.keys(wire!).sort()).toEqual([...NARRATION_SPEECH_WIRE_FIELDS].sort())
  })

  it('copies field by field, so nothing on the request object can ride along', () => {
    // A spread would carry every one of these to the server.
    const polluted = {
      ...request,
      trustRevision: 'tr1_dead',
      trust_revision: 'tr1_dead',
      text: 'the production credentials are in the old vault',
      body: 'the production credentials are in the old vault',
      transcript: 'note that the fixture fails',
    } as unknown as NarrationSpeechRequest
    const wire = toSpeechWire(polluted)
    expect(Object.keys(wire!).sort()).toEqual([...NARRATION_SPEECH_WIRE_FIELDS].sort())
    const serialized = JSON.stringify(wire)
    expect(serialized).not.toContain('tr1_dead')
    expect(serialized).not.toContain('vault')
    expect(serialized).not.toContain('fixture fails')
  })

  it('never carries a trust revision under any name', () => {
    const wire = toSpeechWire(request)!
    expect('trustRevision' in wire).toBe(false)
    expect('trust_revision' in wire).toBe(false)
    expect(JSON.stringify(wire)).not.toContain('trust')
  })

  it('produces no body at all for a request the guard rejects', () => {
    expect(toSpeechWire({ ...request, candidateIds: [] })).toBeNull()
    expect(toSpeechWire({ ...request, deliveryRevision: '' })).toBeNull()
    expect(toSpeechWire({ ...request, template: 'note_saved' as never })).toBeNull()
  })

  it('detaches the candidate array from the caller', () => {
    const candidateIds = ['dlv-813']
    const wire = toSpeechWire({ ...request, candidateIds })!
    candidateIds.push('dlv-999')
    expect(wire.candidate_ids).toEqual(['dlv-813'])
  })
})
