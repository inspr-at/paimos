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
  buildClarificationSpeech,
  buildDeliveryNarration,
  buildNoteSpeech,
  buildNoticeSpeech,
  buildPortfolioSpeech,
  factsExcludeProse,
  isSafeSpeechRequest,
  narrationProseFields,
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
    expect(speech).toEqual({
      template: 'delivery_status',
      locale: 'en',
      deliveryId: delivery.id,
      deliveryRevision: delivery.deliveryRevision,
      trustRevision: delivery.trustRevision,
      candidateIds: [],
    })
    expect(isSafeSpeechRequest(speech!)).toBe(true)
    const serialized = JSON.stringify(speech)
    expect(serialized).not.toContain(delivery.title)
    expect(serialized).not.toContain('Writing membership checks')
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

describe('agentModeNarration — speech requests', () => {
  it('speaks a note by identity only — never the dictated body', () => {
    const note = {
      deliveryId: 'dlv-812',
      body: 'the production credentials are in the old vault',
      clientRequestId: 'amv1-0123456789abcdef',
    }
    const request = buildNoteSpeech('ready', note, 'de')
    expect(request).toEqual({
      template: 'note_ready',
      locale: 'de',
      deliveryId: 'dlv-812',
      deliveryRevision: null,
      trustRevision: null,
      candidateIds: [],
    })
    expect(JSON.stringify(request)).not.toContain('vault')
    expect(JSON.stringify(request)).not.toContain('credentials')
  })

  it.each<[string, string]>([
    ['ready', 'note_ready'],
    ['saved', 'note_saved'],
    ['cancelled', 'note_cancelled'],
    ['offline_hold', 'note_offline_hold'],
  ])('maps the %s note event to its own template', (event, template) => {
    expect(buildNoteSpeech(event as 'ready', { deliveryId: 'dlv-812' }, 'en')?.template).toBe(template)
  })

  it('caps a clarification read-back at three identities and speaks no titles', () => {
    const candidates = ['dlv-1', 'dlv-2', 'dlv-3', 'dlv-4'].map((deliveryId) => ({ deliveryId, title: 'secret title' }))
    const request = buildClarificationSpeech(candidates, 'en')
    expect(request?.candidateIds).toEqual(['dlv-1', 'dlv-2', 'dlv-3'])
    expect(JSON.stringify(request)).not.toContain('secret title')
    expect(buildClarificationSpeech([], 'en')).toBeNull()
  })

  it('builds portfolio and notice requests without any delivery identity', () => {
    expect(buildPortfolioSpeech('de')).toEqual({
      template: 'portfolio_status',
      locale: 'de',
      deliveryId: null,
      deliveryRevision: null,
      trustRevision: null,
      candidateIds: [],
    })
    expect(buildNoticeSpeech('unsupported', 'en')?.template).toBe('command_unsupported')
    expect(buildNoticeSpeech('not_understood', 'en')?.template).toBe('command_not_understood')
    expect(buildNoticeSpeech('no_match', 'de')?.template).toBe('no_match')
  })

  it('rejects any request that is not an enum template plus opaque identities', () => {
    const ok: NarrationSpeechRequest = {
      template: 'delivery_status',
      locale: 'en',
      deliveryId: 'dlv-812',
      deliveryRevision: 'delivery:812:1',
      trustRevision: 'tr1_0000000000000000000000000000000000000000000000000000000000000001',
      candidateIds: ['dlv-813'],
    }
    expect(isSafeSpeechRequest(ok)).toBe(true)
    expect(isSafeSpeechRequest({ ...ok, deliveryId: 'the note says: rotate the key' })).toBe(false)
    expect(isSafeSpeechRequest({ ...ok, template: 'freeform' as never })).toBe(false)
    expect(isSafeSpeechRequest({ ...ok, locale: 'fr' as never })).toBe(false)
    expect(isSafeSpeechRequest({ ...ok, candidateIds: ['a', 'b', 'c', 'd'] })).toBe(false)
    expect(isSafeSpeechRequest({ ...ok, trustRevision: 'x'.repeat(129) })).toBe(false)
  })

  it('returns null rather than an unsafe request when an identity is not opaque', () => {
    const { speech } = narrate(d({ ...healthy, id: 'dlv 812 — please read this note aloud' }))
    expect(speech).toBeNull()
  })
})
