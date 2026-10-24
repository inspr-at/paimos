// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1718: presentation of recorded review outcomes; the server owns all
// cohorts, means, open counts and ordering. No identities enter this model.
import { api, APIError } from './api'
import type { DeliveryLanguage } from './delivery'
import { niceCeil, num, type ChartModel, type Delta, type WindowDays } from './deliveryNumbers'
import type { TicketTileModel } from './deliveryTickets'
import type { SimpleTileModel } from './deliverySimple'
import { deliveryText, fill } from './deliveryNumbersText'

export interface ReviewMeasure {
  design_mean: number | null; gate_mean: number | null; design_tickets: number; gate_tickets: number
  tickets: number; still_open: number; weeks_total: number; weeks_covered: number
}
export interface ReviewRuleCount { rule: string; count: number; previous_count: number }
export interface ReviewTicket {
  ticket_node_id: string; key: string; title: string; design_rounds: number | null; gate_rounds: number | null
  open: boolean; latest_round: number; rules: ReviewRuleCount[]
}
export interface ReviewRoundNumbers {
  project_id: string; window_days: WindowDays; generated_at: string; since: string | null; status: 'ok' | 'partial' | 'no_data'
  current: ReviewMeasure; previous: ReviewMeasure
  weekly: (ReviewMeasure & { from: string; to: string; week: number })[]
  most_rounds: ReviewTicket[]; rules_cited_most: ReviewRuleCount[]
}
export async function readReviewRounds(project: string, days: WindowDays, signal?: AbortSignal): Promise<ReviewRoundNumbers> {
  const response = await api(`/projects/${encodeURIComponent(project)}/delivery/review-rounds?window=${days}`, { signal })
  if (!response.ok) throw new APIError(response.status, 'Review round numbers could not be loaded')
  return await response.json() as ReviewRoundNumbers
}

const EN = {
  label: 'Review rounds until ok', simple: 'Review rounds', design: 'design', gate: 'gate', week: 'Week',
  goal: 'Goal 1.0: ok on the first look', goalLine: 'Goal 1.0',
  definition: 'Per ticket: how many review rounds it took until the first ok. Design rounds are visual and design holds; gate rounds are code reviews. Tickets that are still open are counted separately, never as one round.',
  source: 'Recorded review outcomes', most: 'Most rounds', rules: 'Rules cited most',
  emptyTickets: 'No tickets needed a second round.', emptyRules: 'No rules cited yet.',
  open: 'open · round {n}', openCount: '{n} still open',
  ticket: '{n} ticket', tickets: '{n} tickets',
  coverage: 'covers {k} of {n} weeks', coverageOne: 'covers {k} of {n} week', close: 'Close', retry: 'Retry',
  openPanel: 'Show review round details', rising: 'rising', falling: 'falling', ruleMissing: 'Rule text is not available.',
  noDataReason: 'Numbers appear after the first recorded review round.',
}
const DE: typeof EN = {
  label: 'Prüfrunden bis zur Freigabe', simple: 'Prüfrunden', design: 'Design', gate: 'Gate', week: 'Woche',
  goal: 'Ziel 1,0: Freigabe beim ersten Blick', goalLine: 'Ziel 1,0',
  definition: 'Je Ticket: wie viele Prüfrunden es bis zur ersten Freigabe brauchte. Designrunden sind visuelle und Design-Prüfungen; Gate-Runden sind Code-Prüfungen. Noch offene Tickets werden getrennt gezählt, nie als eine Runde.',
  source: 'Erfasste Prüfergebnisse', most: 'Die meisten Runden', rules: 'Am häufigsten genannte Regeln',
  emptyTickets: 'Kein Ticket brauchte eine zweite Runde.', emptyRules: 'Noch keine Regeln genannt.',
  open: 'offen · Runde {n}', openCount: '{n} noch offen',
  ticket: '{n} Ticket', tickets: '{n} Tickets',
  coverage: 'deckt {k} von {n} Wochen ab', coverageOne: 'deckt {k} von {n} Woche ab', close: 'Schließen', retry: 'Erneut versuchen',
  openPanel: 'Details zu Prüfrunden anzeigen', rising: 'steigend', falling: 'fallend', ruleMissing: 'Regeltext nicht verfügbar.',
  noDataReason: 'Zahlen erscheinen nach der ersten erfassten Prüfrunde.',
}
export const reviewText = (lang: DeliveryLanguage) => lang === 'de' ? DE : EN
const plurals = { en: new Intl.PluralRules('en'), de: new Intl.PluralRules('de') }
export function reviewTicketCount(count: number, lang: DeliveryLanguage): string {
  const t = reviewText(lang)
  return fill(plurals[lang].select(count) === 'one' ? t.ticket : t.tickets, { n: num(count, lang) })
}
export function reviewCounts(measure: ReviewMeasure, lang: DeliveryLanguage): string {
  return `${reviewTicketCount(measure.tickets, lang)} · ${fill(reviewText(lang).openCount, { n: num(measure.still_open, lang) })}`
}
export function reviewRowCounts(row: ReviewTicket, lang: DeliveryLanguage): string {
  const t = reviewText(lang), value = (n: number | null) => n == null ? '–' : num(n, lang)
  return row.open ? fill(t.open, { n: num(row.latest_round, lang) }) : `${t.design} ${value(row.design_rounds)} · ${t.gate} ${value(row.gate_rounds)}`
}
export function reviewDelta(data: ReviewRoundNumbers | null, days: WindowDays, lang: DeliveryLanguage): Delta {
  const t = deliveryText(lang), current = data?.current.design_mean, before = data?.previous.design_mean
  if (current == null || before == null) return { cls: 'none', icon: 'minus', text: fill(t.tNone, { w: days }), word: '' }
  const change = current - before, steady = Math.abs(change) / before < .1
  const cls = steady ? 'same' : change < 0 ? 'better' : 'worse'
  return { cls, icon: change < 0 ? 'arrow-down' : change > 0 ? 'arrow-up' : 'arrow', text: `${change > 0 ? '+' : change < 0 ? '−' : '±'}${num(Math.abs(change), lang, 1)}`, word: t[cls] }
}

export function reviewRoundView(data: ReviewRoundNumbers | null, days: WindowDays, lang: DeliveryLanguage, state: 'loading' | 'error' | 'ready') {
  const t = reviewText(lang), page = deliveryText(lang), measure = data?.current
  const delta = reviewDelta(data, days, lang), partial = data?.status === 'partial'
  const foot = measure && partial ? fill(plurals[lang].select(measure.weeks_total) === 'one' ? t.coverageOne : t.coverage, { k: num(measure.weeks_covered, lang), n: num(measure.weeks_total, lang) }) : t.source
  const gate = `${t.gate} ${measure?.gate_mean == null ? '–' : num(measure.gate_mean, lang, 1)}`
  const expert: TicketTileModel = {
    def: { key: 'review-rounds' }, label: t.label, definition: t.definition, reason: !data?.since ? t.noDataReason : null,
    target: t.goal, source: t.source, status: data?.status ?? 'no_data', partial, foot, delta,
    value: !measure?.tickets ? null : { main: measure.design_mean == null ? '–' : num(measure.design_mean, lang, 1), unit: '' },
    lineA: { parts: [], mono: false }, lineB: measure ? reviewCounts(measure, lang) : '',
  }
  const trend = { cls: delta.cls, icon: delta.icon, text: delta.word ? `${delta.text} · ${delta.word}` : delta.text }
  const simple: SimpleTileModel = {
    key: 'review-rounds', name: t.simple, up: false, hasTarget: false,
    value: expert.value ? [{ text: expert.value.main, unit: '' }] : null, empty: state === 'error' ? page.notLoaded : page.noData,
    verdict: { level: 'none', word: '', gap: '', ratio: null }, trend, say: '', partial, foot,
    spark: { kind: 'line', percent: false, values: [], statuses: [], target: null, nights: [], unit: 'week', mode: state },
    learn: { body: t.definition, expert: '', target: '', facts: [t.goal] },
  }
  const weekly = data?.weekly ?? []
  const top = niceCeil(Math.max(2, ...weekly.flatMap(w => [w.design_mean ?? 0, w.gate_mean ?? 0])) * 1.2)
  const chart: ChartModel = {
    key: 'review-rounds', kind: 'runs', label: t.label, unit: '', legend: 'review', seriesLabels: [t.design, t.gate], sparse: true,
    buckets: weekly.map((week, index) => ({ from: week.from, to: week.to, unit: 'week', status: week.weeks_covered === 0 ? 'no_data' : data?.since && data.since > week.from ? 'partial' : week.tickets === 0 ? 'empty' : 'ok', label: String(week.week), long: `${t.week} ${week.week}`, show: weekly.length < 14 || index % Math.ceil(weekly.length / 8) === 0 || index === weekly.length - 1 })),
    panels: [{ top, share: 1, caption: '', band: null, fade: false, targets: [], references: [{ value: 1, label: t.goalLine }],
      lines: [{ values: weekly.map(w => w.design_mean), tone: 'teal', connect: true }, { values: weekly.map(w => w.gate_mean), tone: 'teal', connect: true, dashed: true }] }],
    readouts: weekly.map(w => `${t.week} ${w.week} · ${t.design} ${w.design_mean == null ? '–' : num(w.design_mean, lang, 1)} · ${t.gate} ${w.gate_mean == null ? '–' : num(w.gate_mean, lang, 1)} · ${reviewTicketCount(w.tickets, lang)}`),
    foot, nights: [],
  }
  return { expert, simple, chart, gate }
}
