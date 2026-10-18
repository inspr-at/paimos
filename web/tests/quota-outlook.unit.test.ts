// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1497: the dial's quota outlook in words and its chart layout. The risk these guard: the
// headline naming a moment, account or verdict the server's replay did not produce, a default
// range that misses the moment the headline names, labels drawn over each other, and stale or
// missing readings read as present. The fixture is the Go projection's own output
// (TestQuotaOutlookWebFixtureIsCurrent), so these words follow the server's numbers.
import { describe, expect, it } from 'vitest'
import { accountRows, chartGeometry, chartModel, defaultRange, hourInfo, miniGeometry, usageView, type QuotaOutlook } from '../src/lib/quotaOutlook'
import fixture from './fixtures/quota-outlook.json'

const outlooks = fixture as unknown as Record<'fast' | 'slow', QuotaOutlook>
const group = (scenario: 'fast' | 'slow', harness: string) => outlooks[scenario].groups.find(g => g.harness === harness)!
const view = (scenario: 'fast' | 'slow', harness: string) => usageView(harness, group(scenario, harness), outlooks[scenario].anchor_hour, 'UTC')
const measure = (text: string) => text.length * 6.2

describe('the answer in words', () => {
  it('too fast names when the pool runs dry, how long, and which account refills it', () => {
    const v = view('fast', 'claude')
    expect(v.kind).toBe('fast')
    expect(v.head).toBe('Claude lasts until Mon ~18:20, then 3 h without until account-b@ refills at 20:59.')
    expect(v.short).toBe('lasts until Mon 18:20 · refill 20:59')
    expect(v.tip).toBe('Claude lasts until Mon 18:20 · refill 20:59')
    expect(v.muted).toBe('77 % of the pool used · 3 accounts · sampled 11:10')
    expect(v.verdict).toBe('Too fast')
    expect(v.paceLine).toBe('Sustainable: up to 2.0 %/h (now 2.2 %/h)')
  })
  it('too slow names the refill that wastes most; on pace names the next refill', () => {
    expect(view('slow', 'claude')).toMatchObject({ kind: 'slow', head: 'About 23 % will refill unused on Wed. There’s room for more work.', short: '23 % unused at refill', verdict: 'Too slow' })
    expect(view('fast', 'codex')).toMatchObject({ kind: 'pace', head: 'Enough until the next refill (Tue 09:12).', short: 'enough until refill', tip: 'Codex has enough until the refill Tue 09:12' })
  })
  it('says what is missing instead of inventing it', () => {
    expect(view('fast', 'cursor')).toMatchObject({ kind: 'not_reported', head: 'Quota not reported', short: 'Quota not reported · 2 accounts', tip: '' })
    expect(usageView('grok', undefined, outlooks.fast.anchor_hour, 'UTC')).toMatchObject({ kind: 'none', head: 'No accounts yet', tip: '' })
    const old = structuredClone(group('fast', 'claude'))
    old.pool!.sampled_age_seconds = 3 * 3600
    expect(usageView('claude', old, outlooks.fast.anchor_hour, 'UTC')).toMatchObject({ stale: true, muted: '77 % of the pool used · 3 accounts · sampled 3 h ago' })
    const learning = structuredClone(group('fast', 'claude'))
    Object.assign(learning, { verdict: 'unknown', current: null }); learning.pace.per_hour_24h = null; learning.headline = { ...learning.headline, kind: 'unknown', focus_at: null }
    expect(usageView('claude', learning, outlooks.fast.anchor_hour, 'UTC')).toMatchObject({ kind: 'unknown', verdict: 'Pace not measured yet', focus: null })
  })
  it('the default range always reaches the moment the headline names', () => {
    expect(defaultRange(view('fast', 'claude'), false)).toEqual({ past: 3, ahead: 2 })
    // The wasteful refill is Wed 18:59, 80 h after the anchor: four days ahead.
    expect(defaultRange(view('slow', 'claude'), false)).toEqual({ past: 3, ahead: 4 })
    expect(defaultRange(view('slow', 'claude'), true)).toEqual({ past: 1, ahead: 4 })
    expect(defaultRange(view('fast', 'cursor'), true)).toEqual({ past: 1, ahead: 1 })
  })
})

describe('the subscriptions', () => {
  it('each battery says what is left, when it refills and its own forecast; only the account that empties the pool warns', () => {
    const rows = accountRows(group('fast', 'claude'), 'UTC')
    expect(rows.map(r => [r.name, r.left, r.refills, r.forecast, r.tone, r.stateWord])).toEqual([
      ['account-a@', 0, 'refills Wed 18:59', 'waits for refill', 'quiet', ''],
      ['account-b@', 0, 'refills Mon 20:59', 'waits for refill', 'quiet', ''],
      ['account-c@', 69, 'refills Fri 00:59', 'empty ~Mon 18:20', 't-fast', 'active'],
    ])
    const slow = accountRows(group('slow', 'claude'), 'UTC')
    expect(slow[0]).toMatchObject({ forecast: '23 % unused', tone: 't-slow', stateWord: 'active' })
    expect(slow[2]).toMatchObject({ state: 'held', stateWord: 'held', stateIcon: 'lock', forecast: '' })
    expect(accountRows(group('fast', 'cursor'), 'UTC').every(r => r.left === null && r.state === 'not_reported')).toBe(true)
  })
  it('an inactive login keeps its last reading and says its age', () => {
    const g = structuredClone(group('fast', 'codex'))
    g.accounts[1]!.reading_age_seconds = 5 * 3600
    expect(accountRows(g, 'UTC')[1]).toMatchObject({ left: 100, stateWord: '5 h ago' })
  })
})

describe('the chart', () => {
  const data = outlooks.fast, g = group('fast', 'claude'), m = chartModel(g, data.generated_at, data.anchor_hour)
  it('labels the empty stretch under the axis and the refill above, never on top of each other', () => {
    const geo = chartGeometry(m, 640, 226, 3, 2, measure, 'UTC')
    expect(geo.emptyText.map(t => t.text)).toContain('empty 18:20–20:59')
    expect(geo.emptyText.every(t => t.y > geo.frame.y0)).toBe(true)
    const texts = geo.markText.map(t => t.text)
    expect(texts).toContain('now')
    expect(texts).toContain('account-b@ refills')
    for (const [i, a] of geo.markText.entries()) for (const b of geo.markText.slice(i + 1)) {
      if (a.y !== b.y) continue
      expect(a.x + measure(a.text) <= b.x || b.x + measure(b.text) <= a.x).toBe(true)
    }
    expect(geo.pastPath && geo.curPath && geo.safePath).toBeTruthy()
    // Ahead 0 hides both projections and their markers.
    const none = chartGeometry(m, 640, 226, 3, 0, measure, 'UTC')
    expect([none.curPath, none.safePath, none.emptyText.length, none.markText.filter(t => t.text.endsWith('refills')).length]).toEqual(['', '', 0, 0])
  })
  it('one hour in words: measured in the past, projected ahead', () => {
    const geo = chartGeometry(m, 640, 226, 3, 2, measure, 'UTC')
    const words = (h: number) => hourInfo(m, geo, h, 'Claude', 'UTC').map(p => p.text).join('')
    expect(words(-5)).toMatch(/^Sun 06:00 · \d+ % left · [\d.]+ %\/h · measured · account-c@ [\d.]+$/)
    expect(words(10)).toMatch(/^Sun 21:00 · \d+ % left · [\d.]+ %\/h · projected/)
    expect(words(32)).toMatch(/^Mon 19:00 · 0 % left · empty · projected$/)
  })
  it('the folded mini line ends amber only when the pool runs dry before the refill', () => {
    expect(miniGeometry(m, 40)).toMatchObject({ dry: true, w: 40, h: 14 })
    const codex = group('fast', 'codex')
    expect(miniGeometry(chartModel(codex, data.generated_at, data.anchor_hour), 40)?.dry).toBe(false)
  })
})
