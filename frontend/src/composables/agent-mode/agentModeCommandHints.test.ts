import { describe, expect, it } from 'vitest'

import { buildAgentModeCommandHints } from './agentModeCommandHints'

const base = {
  locale: 'en',
  detailLevel: 10 as const,
  selected: true,
  travelCount: 4,
  filtersActive: false,
  health: 'all' as const,
  blockedCount: 1,
  attentionCount: 2,
  staleCount: 1,
  candidateCount: 0,
  notePending: false,
}

describe('Agent Mode contextual command hints', () => {
  it('offers deterministic navigation, status, filter, and zoom commands', () => {
    expect(buildAgentModeCommandHints(base).map((entry) => entry.command)).toEqual([
      'Next',
      'Previous',
      'Read status',
      'Show details',
      'Show blocked',
      'Show attention',
      'Show stale',
      'Overview',
    ])
  })

  it('puts Show all first only while filters are active', () => {
    const hints = buildAgentModeCommandHints({ ...base, filtersActive: true, health: 'blocked' })
    expect(hints[0]).toMatchObject({ id: 'show-all', command: 'Show all' })
    expect(hints.some((entry) => entry.id === 'show-blocked')).toBe(false)
  })

  it('replaces ordinary hints with bounded clarification choices', () => {
    expect(
      buildAgentModeCommandHints({ ...base, candidateCount: 7 }).map((entry) => entry.command),
    ).toEqual(['Candidate 1', 'Candidate 2', 'Candidate 3'])
  })

  it('replaces ordinary hints with the note decision and localizes German', () => {
    expect(buildAgentModeCommandHints({ ...base, locale: 'de-AT', notePending: true })).toEqual([
      { id: 'confirm-note', command: 'Notiz bestätigen', label: 'Notiz bestätigen' },
      { id: 'cancel-note', command: 'Notiz verwerfen', label: 'Notiz verwerfen' },
    ])
  })
})
