// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1497: the dial's quota outlook in words and its chart layout. The risk these guard: the
// headline naming a moment, account or verdict the server's replay did not produce, a default
// range that misses the moment the headline names, labels drawn over each other, and stale or
// missing readings read as present. The fixture is the Go projection's own output
// (TestQuotaOutlookWebFixtureIsCurrent), so these words follow the server's numbers.
import { describe, expect, it } from 'vitest'
import { accountRows, ago, chartGeometry, chartModel, defaultRange, hourInfo, miniGeometry, usageView, type OutlookGroup, type QuotaOutlook } from '../src/lib/quotaOutlook'
import fixture from './fixtures/quota-outlook.json'

const outlooks = fixture as unknown as Record<'fast' | 'slow' | 'expired', QuotaOutlook>
const group = (scenario: 'fast' | 'slow', harness: string) => outlooks[scenario].groups.find(g => g.harness === harness)!
const view = (scenario: 'fast' | 'slow', harness: string) => usageView(harness, group(scenario, harness), outlooks[scenario].anchor_hour, 'UTC')
const measure = (text: string) => text.length * 6.2

describe('the answer in words', () => {
  it('too fast names when the pool runs dry, how long, and which account refills it', () => {
    const v = view('fast', 'claude')
    expect(v.kind).toBe('fast')
    expect(v.head).toBe('Claude lasts until Mon\u00a0~18:20, then 3\u00a0h without until account-b@ refills at 20:59.')
    expect(v.short).toBe('lasts until Mon\u00a018:20 · refill 20:59')
    expect(v.tip).toBe('Claude lasts until Mon\u00a018:20 · refill 20:59')
    expect(v.muted).toBe('77\u00a0% of the pool used · 3 accounts · sampled 11:10')
    expect(v.verdict).toBe('Too fast')
    expect(v.paceLine).toBe('Sustainable: up to 2.0\u00a0%/h (now 2.2\u00a0%/h)')
  })
  it('too slow names the refill that wastes most; on pace names the next refill', () => {
    expect(view('slow', 'claude')).toMatchObject({ kind: 'slow', head: 'About 23\u00a0% will refill unused on Wed. There’s room for more work.', short: '23\u00a0% unused at refill', verdict: 'Too slow' })
    expect(view('fast', 'codex')).toMatchObject({ kind: 'pace', head: 'Enough until the next refill (Tue\u00a009:12).', short: 'enough until refill', tip: 'Codex has enough until the refill Tue\u00a009:12' })
  })
  it('says what is missing instead of inventing it', () => {
    expect(view('fast', 'cursor')).toMatchObject({ kind: 'not_reported', head: 'Quota not available', short: 'Quota not available · 2 accounts', tip: 'Cursor doesn’t report its quota, so agents can’t pace it. The agent count on the dial still caps it.' })
    expect(usageView('grok', undefined, outlooks.fast.anchor_hour, 'UTC')).toMatchObject({ kind: 'none', head: 'No accounts yet', tip: '' })
    const old = structuredClone(group('fast', 'claude'))
    old.pool!.sampled_age_seconds = 3 * 3600
    expect(usageView('claude', old, outlooks.fast.anchor_hour, 'UTC')).toMatchObject({ stale: true, muted: '77\u00a0% of the pool used · 3 accounts · sampled 3\u00a0h ago' })
    // More accounts than the outlook reads: said, never hidden.
    expect(usageView('claude', group('fast', 'claude'), outlooks.fast.anchor_hour, 'UTC', [], 'ready', true).muted).toBe('77\u00a0% of the pool used · 3 accounts · not every account read · sampled 11:10')
    const learning = structuredClone(group('fast', 'claude'))
    Object.assign(learning, { verdict: 'unknown', current: null }); learning.pace.per_hour_24h = null; learning.headline = { ...learning.headline, kind: 'unknown', focus_at: null }
    expect(usageView('claude', learning, outlooks.fast.anchor_hour, 'UTC')).toMatchObject({ kind: 'unknown', verdict: 'Pace not measured yet', focus: null })
    // An expired reading has no pool. The words keep the last measurement and do not claim headroom.
    const expired = structuredClone(group('fast', 'claude'))
    expired.pool = null
    expired.accounts[0]!.freshness = 'expired'
    expired.accounts[0]!.used_percent = 80
    const words = usageView('claude', expired, outlooks.fast.anchor_hour, 'UTC')
    expect(words.head + words.short + words.muted + words.tip).not.toMatch(/100\s%/)
    expect(words.muted.toLowerCase()).toContain('unknown')
  })
  it('before the outlook is read, the plan’s accounts decide: pending rows, or no quota for a harness that never reports it', () => {
    const known = [{ account_id: 'a', label: 'one@' }, { account_id: 'b', label: 'two@' }]
    expect(usageView('claude', undefined, undefined, 'UTC', known, 'loading')).toMatchObject({ kind: 'pending', known })
    expect(usageView('claude', undefined, undefined, 'UTC', known, 'error')).toMatchObject({ kind: 'pending' })
    expect(usageView('cursor', undefined, undefined, 'UTC', known, 'loading')).toMatchObject({ kind: 'not_reported', short: 'Quota not available · 2 accounts' })
    // Read, and still no group: the accounts have no readings, which is not "no accounts".
    expect(usageView('codex', undefined, undefined, 'UTC', known, 'ready')).toMatchObject({ kind: 'not_reported', head: 'Reads after the first run', tip: 'Codex reports its quota during a run, so readings start with the first run on each account.' })
    for (const harness of ['claude', 'codex']) {
      for (const missing of [undefined, { ...group('fast', 'cursor'), harness }]) {
        expect(usageView(harness, missing, undefined, 'UTC', known, 'ready', false, 0, 0)).toMatchObject({ head: 'Reads after the first run' })
        expect(usageView(harness, missing, undefined, 'UTC', known, 'ready', false, 0, 6)).toMatchObject({
          kind: 'not_reported', head: 'Waiting for a quota reading', short: 'Waiting for a quota reading · 2 accounts',
          tip: 'Runs are going, but no quota reading has arrived yet.',
        })
      }
    }
    expect(usageView('codex', undefined, undefined, 'UTC', [], 'loading')).toMatchObject({ kind: 'pending' })
    expect(usageView('codex', undefined, undefined, 'UTC', [], 'error')).toMatchObject({ kind: 'pending' })
    expect(usageView('codex', undefined, undefined, 'UTC', [], 'ready')).toMatchObject({ kind: 'none' })
    expect(usageView('claude', undefined, undefined, 'UTC', [{ ...known[0]!, reading_support: 'every_5_min' }], 'ready')).toMatchObject({ head: 'Waiting for a quota reading' })
    expect(usageView('claude', undefined, undefined, 'UTC', [{ ...known[0]!, reading_support: 'statusline' }], 'ready')).toMatchObject({ head: 'Waiting for the status line' })
    expect(accountRows(null, 'UTC', known).map(r => [r.name, r.state, r.left])).toEqual([['one@', 'not_reported', null], ['two@', 'not_reported', null]])
    const privateGroup = structuredClone(group('fast', 'cursor'))
    privateGroup.accounts.forEach(a => { a.state = 'private' })
    expect(usageView('cursor', privateGroup, undefined)).toMatchObject({ head: 'Quota details are private' })
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
      ['account-a@', 0, 'refills Wed\u00a018:59', 'waits for refill', 'quiet', ''],
      ['account-b@', 0, 'refills Mon\u00a020:59', 'waits for refill', 'quiet', ''],
      ['account-c@', 69, 'refills Fri\u00a000:59', 'empty ~Mon\u00a018:20', 't-fast', 'active'],
    ])
    const slow = accountRows(group('slow', 'claude'), 'UTC')
    expect(slow[0]).toMatchObject({ forecast: '23\u00a0% unused', tone: 't-slow', stateWord: 'active' })
    expect(slow[2]).toMatchObject({ state: 'held', stateWord: 'held', stateIcon: 'lock', forecast: '' })
    expect(accountRows(group('fast', 'cursor'), 'UTC').every(r => r.left === null && r.state === 'not_reported')).toBe(true)
    // A private row may arrive beside retained pool totals, but never names its
    // enrollment or turns those totals into a per-account reading or forecast.
    const privateGroup = structuredClone(group('fast', 'claude'))
    privateGroup.accounts[1]!.state = 'private'
    expect(accountRows(privateGroup, 'UTC')[1]).toMatchObject({ name: 'Private account', left: null, refills: '', forecast: 'details hidden', tone: 'quiet' })
    expect(usageView('claude', privateGroup, outlooks.fast.anchor_hour, 'UTC').head).toBe('Claude lasts until Mon\u00a0~18:20, then 3\u00a0h without until another account refills at 20:59.')
  })
  it('an inactive login keeps its last reading and says its age', () => {
    const g = structuredClone(group('fast', 'codex'))
    g.accounts[1]!.reading_age_seconds = 5 * 3600
    expect(accountRows(g, 'UTC')[1]).toMatchObject({ left: 100, stateWord: '5\u00a0h ago' })
  })
})

describe('no line break inside a number or a moment', () => {
  // A plain space may break ("then 3 / h without" on a phone). Every string the headline, the muted
  // line, the verdict and the rows show joins a number to its unit and a weekday to its time.
  const breakable = /\d (?:%|h\b|min\b|d\b|days\b)|\b(?:Mon|Tue|Wed|Thu|Fri|Sat|Sun) ~?\d/
  const variant = (scenario: 'fast' | 'slow', harness: string, edit: (g: OutlookGroup) => void) => {
    const g = structuredClone(group(scenario, harness)); edit(g)
    return usageView(harness, g, outlooks[scenario].anchor_hour, 'UTC')
  }
  it('joins units, percentages and weekday times with a no-break space in every branch', () => {
    const out = variant('fast', 'claude', g => { g.headline.at = g.pool!.sampled_at })
    const endless = variant('fast', 'claude', g => { g.headline.until = null })
    const minutes = variant('fast', 'claude', g => { g.headline.until = '2026-10-19T18:58:00Z' })
    const week = variant('fast', 'codex', g => { g.headline.at = null })
    const learning = variant('fast', 'claude', g => { g.verdict = 'unknown' })
    const stale = variant('fast', 'claude', g => { g.pool!.sampled_age_seconds = 3 * 3600 })
    expect(out.head).toBe('Claude is out of quota until account-b@ refills at Mon\u00a020:59.')
    expect(endless.head).toBe('Claude lasts until Mon\u00a0~18:20; nothing refills within 7\u00a0days.')
    expect(minutes.head).toBe('Claude lasts until Mon\u00a0~18:20, then 40\u00a0min without until account-b@ refills at 18:58.')
    expect(week.head).toBe('Enough for the next 7\u00a0days.')
    expect(learning.head).toBe('23\u00a0% of the pool left. The pace appears after an hour of readings.')
    expect([ago(45 * 60), ago(3 * 86400)]).toEqual(['45\u00a0min ago', '3\u00a0d ago'])
    const views = [out, endless, minutes, week, learning, stale, ...(['fast', 'slow'] as const).flatMap(s => ['claude', 'codex', 'cursor'].map(h => view(s, h)))]
    const aged = structuredClone(group('fast', 'codex'))
    aged.accounts[1]!.reading_age_seconds = 5 * 3600
    const rows = [...accountRows(group('fast', 'claude'), 'UTC'), ...accountRows(group('slow', 'claude'), 'UTC'), ...accountRows(aged, 'UTC')]
    const texts = [...views.flatMap(v => [v.head, v.muted, v.verdict, v.paceLine, v.short, v.tip]), ...rows.flatMap(r => [r.refills, r.forecast, r.stateWord])]
    expect(texts.filter(t => breakable.test(t))).toEqual([])
    // The sweep holds the joined forms, so an empty pass cannot hide a regression.
    for (const joined of [/then 3\u00a0h without/, /2\.0\u00a0%\/h/, /77\u00a0% of the pool/, /empty ~Mon\u00a018:20/, /refills Fri\u00a000:59/, /23\u00a0% unused/, /5\u00a0h ago/]) expect(texts.some(t => joined.test(t)), String(joined)).toBe(true)
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
    // The server's expired-evidence response must not render a claimed refill.
    const expired = outlooks.expired
    const old = chartModel(expired.groups[0]!, expired.generated_at, expired.anchor_hour)
    const oldGeo = chartGeometry(old, 640, 226, 3, 2, measure, 'UTC')
    expect(oldGeo.refillMarks.filter(r => r.past)).toEqual([])
    expect(oldGeo.markText.map(t => t.text).join(' ')).not.toContain('refilled')
  })
  it('one hour in words: measured in the past, projected ahead', () => {
    const geo = chartGeometry(m, 640, 226, 3, 2, measure, 'UTC')
    const words = (h: number) => hourInfo(m, geo, h, 'Claude', 'UTC').map(p => p.text).join('')
    expect(words(-5)).toMatch(/^Sun 06:00 · \d+ % left · [\d.]+ %\/h · measured · account-c@ [\d.]+$/)
    expect(words(10)).toMatch(/^Sun 21:00 · \d+ % left · [\d.]+ %\/h · projected/)
    expect(words(32)).toMatch(/^Mon 19:00 · 0 % left · empty · projected$/)
    const hidden = structuredClone(g)
    hidden.accounts[2]!.state = 'private'
    const privateModel = chartModel(hidden, data.generated_at, data.anchor_hour)
    const privateGeo = chartGeometry(privateModel, 640, 226, 3, 2, measure, 'UTC')
    expect(privateGeo.privateBand).toMatchObject({ x: privateGeo.frame.L, w: 640 - privateGeo.frame.L })
    expect(privateGeo.bars.some(b => b.i === 2)).toBe(false)
    const privateWords = hourInfo(privateModel, privateGeo, -5, 'Claude', 'UTC').map(p => p.text).join('')
    expect(privateWords).not.toContain('account-c@')
    expect(privateWords).toContain('private')
  })
  it('the folded mini line ends amber only when the pool runs dry before the refill', () => {
    expect(miniGeometry(m, 40)).toMatchObject({ dry: true, w: 40, h: 14 })
    const codex = group('fast', 'codex')
    expect(miniGeometry(chartModel(codex, data.generated_at, data.anchor_hour), 40)?.dry).toBe(false)
  })
})
