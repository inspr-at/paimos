// SPDX-License-Identifier: AGPL-3.0-only
// AEON-785: the footer's centre says what the screen shows, one screen at a time.
import { effectScope, nextTick, ref } from 'vue'
import { describe, expect, it } from 'vitest'
import { accessFooter, agentsFooter, atRisk, knowledgeFooter, releasesFooter, settingsFooter, ticketsFooter } from '../src/lib/footerProviders'
import { footerSummary, landed, pingGate, plain, useFooterSummary, type FooterSummary } from '../src/lib/footerSummary'

const noop = () => undefined
const said = (summary: FooterSummary | null) => summary ? { tone: summary.tone, full: plain(summary.full), short: plain(summary.short), plainText: !summary.action } : null
const agents = { loaded: true, total: 11, working: 10, problems: 0, asks: 0, paused: 0, wind: null, act: { problem: noop, ask: noop, wind: noop, paused: noop, top: noop } }

describe('what each screen says', () => {
  it.each([
    ['agents with a problem', agentsFooter({ ...agents, problems: 1 }), { tone: 'problem', full: '11 agents · 10 working · 1 problem', short: '1 problem', plainText: false }],
    ['agents that ask', agentsFooter({ ...agents, working: 9, asks: 2 }), { tone: 'attention', full: '11 agents · 2 ask you', short: '2 ask you', plainText: false }],
    ['agents all working', agentsFooter(agents), { tone: 'calm', full: '11 agents · all working', short: '11 working', plainText: false }],
    ['agents winding down', agentsFooter({ ...agents, wind: { left: 7, by: '19:44' } }), { tone: 'deliberate', full: 'Winding down · 7 left · by 19:44', short: 'Winding down', plainText: false }],
    ['agents all paused', agentsFooter({ ...agents, paused: 11 }), { tone: 'idle', full: '11 agents · all paused', short: '11 paused', plainText: false }],
    ['no agents', agentsFooter({ ...agents, total: 0, working: 0 }), { tone: 'idle', full: 'No agents running', short: 'None running', plainText: true }],
    ['tickets with blocked', ticketsFooter({ loaded: true, total: 62, withAgents: 8, blocked: 2, filtered: false, act: { blocked: noop, top: noop, clear: noop } }), { tone: 'attention', full: '62 tickets · 8 with agents · 2 blocked', short: '2 blocked', plainText: false }],
    ['tickets calm', ticketsFooter({ loaded: true, total: 62, withAgents: 8, blocked: 0, filtered: false, act: { blocked: noop, top: noop, clear: noop } }), { tone: 'calm', full: '62 tickets · 8 with agents', short: '62 tickets', plainText: false }],
    ['tickets filtered to none', ticketsFooter({ loaded: true, total: 0, withAgents: 0, blocked: 0, filtered: true, act: { blocked: noop, top: noop, clear: noop } }), { tone: 'idle', full: 'No tickets match', short: 'No matches', plainText: false }],
    ['releases at risk', releasesFooter({ loaded: true, planned: 3, atRisk: 1, act: { risk: noop } }), { tone: 'attention', full: '3 planned · 1 at risk', short: '1 at risk', plainText: false }],
    ['nothing planned', releasesFooter({ loaded: true, planned: 0, atRisk: 0, act: { risk: noop } }), { tone: 'idle', full: 'Nothing planned', short: 'Nothing planned', plainText: true }],
    ['knowledge to review', knowledgeFooter({ loaded: true, entries: 84, toReview: 3, updatedAt: null, now: 0, act: { review: noop, sort: noop } }), { tone: 'attention', full: '84 entries · 3 to review', short: '3 to review', plainText: false }],
    ['knowledge calm', knowledgeFooter({ loaded: true, entries: 84, toReview: 0, updatedAt: Date.parse('2026-10-06T10:48:00Z'), now: Date.parse('2026-10-06T11:00:00Z'), act: { review: noop, sort: noop } }), { tone: 'calm', full: '84 entries · updated 12 min ago', short: '84 entries', plainText: false }],
    ['access with an expiring key', accessFooter({ loaded: true, people: 8, keys: 14, expiring: 1, act: { expiring: noop, top: noop, invite: noop } }), { tone: 'attention', full: '8 people · 14 agent keys · 1 key expires soon', short: '1 key expires', plainText: false }],
    ['access, only you', accessFooter({ loaded: true, people: 1, keys: 0, expiring: null, act: { expiring: noop, top: noop, invite: null } }), { tone: 'idle', full: 'Only you so far', short: 'Only you', plainText: true }],
    ['settings that need you', settingsFooter({ saving: false, failed: false, needs: 1, act: { retry: noop, needs: noop } }), { tone: 'attention', full: '1 needs you', short: '1 needs you', plainText: false }],
    ['settings saving', settingsFooter({ saving: true, failed: false, needs: 1, act: { retry: noop, needs: noop } }), { tone: 'idle', full: 'Saving…', short: 'Saving…', plainText: true }],
    ['settings not saved wins', settingsFooter({ saving: true, failed: true, needs: 1, act: { retry: noop, needs: noop } }), { tone: 'attention', full: 'Not saved · Try again', short: 'Not saved', plainText: false }],
  ])('%s', (_name, summary, expected) => {
    expect(said(summary)).toEqual(expected)
    // Count noun · state · the one exception: never more than three parts.
    expect(plain(summary?.full ?? []).split(' · ').length).toBeLessThanOrEqual(3)
  })

  it('says nothing when settings are calm, and a skeleton while a screen loads', () => {
    expect(settingsFooter({ saving: false, failed: false, needs: 0, act: { retry: noop, needs: noop } })).toBeNull()
    expect(agentsFooter({ ...agents, loaded: false }).loading).toBe(true)
    expect(ticketsFooter({ loaded: true, total: null, withAgents: 0, blocked: 0, filtered: false, act: { blocked: noop, top: noop, clear: noop } }).loading).toBe(true)
  })

  it('counts a release as at risk only while it is on its way and a run failed', () => {
    const run = (conclusion: string) => ({ name: 'ci', url: '', status: 'completed', conclusion })
    const evidence = (ci: ReturnType<typeof run> | null) => ({ source_commit: '', source_url: '', image: null, ci, release_run: null, release_url: '', unavailable: [] })
    expect(atRisk({ state: 'candidate', evidence: evidence(run('failure')) })).toBe(true)
    expect(atRisk({ state: 'candidate', evidence: evidence(run('success')) })).toBe(false)
    expect(atRisk({ state: 'published', evidence: evidence(run('failure')) })).toBe(false)
  })
})

describe('the ping and who speaks', () => {
  it('rings at most once per two seconds, on fresh data of the same screen only', () => {
    let now = 10_000
    const gate = pingGate(2000, () => now)
    expect(gate()).toBe(true)
    now += 1999
    expect(gate()).toBe(false)
    now += 1
    expect(gate()).toBe(true)
    const a = Symbol('a'), b = Symbol('b')
    expect(landed(null, { source: a, at: 5 })).toBe(false)
    expect(landed({ source: a, at: null }, { source: a, at: 5 })).toBe(false)
    expect(landed({ source: a, at: 5 }, { source: b, at: 9 })).toBe(false)
    expect(landed({ source: a, at: 5 }, { source: a, at: 5 })).toBe(false)
    expect(landed({ source: a, at: 5 }, { source: a, at: 9 })).toBe(true)
  })

  it('lets a sheet speak over its page and gives the centre back when it closes', async () => {
    const page = effectScope(), sheet = effectScope()
    const pageText = ref('62 tickets')
    const make = (text: () => string): FooterSummary => ({ tone: 'calm', full: [{ text: text() }], short: [], aria: text() })
    page.run(() => useFooterSummary(() => make(() => pageText.value)))
    expect(plain(footerSummary.value?.full ?? [])).toBe('62 tickets')
    sheet.run(() => useFooterSummary(() => make(() => '3 planned')))
    pageText.value = '63 tickets'
    await nextTick()
    expect(plain(footerSummary.value?.full ?? [])).toBe('3 planned')
    sheet.stop()
    expect(plain(footerSummary.value?.full ?? [])).toBe('63 tickets')
    page.stop()
    expect(footerSummary.value).toBeNull()
  })
})
