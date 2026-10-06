// SPDX-License-Identifier: AGPL-3.0-only
// What each screen says in the footer (AEON-785): count · state · the one
// exception, at most three parts. Pure functions of what the screen already
// shows, so a screen never reports more than the person can see on it.
import { ref } from 'vue'
import { count, exception, LOADING, noun, said, type FooterPart, type FooterSummary, type FooterTone } from './footerSummary'
import { relativeTime } from './work'
import type { Release } from './releases'

// aria is the whole sentence, ending in what a click does; without an action it only says what is shown.
function build(tone: FooterTone, full: readonly FooterPart[], short: readonly FooterPart[], aria: string, action?: () => void): FooterSummary {
  return { tone, full, short, aria, ...(action ? { action } : {}) }
}

// ---------- Agents ----------
export interface AgentsFooter {
  loaded: boolean
  total: number
  working: number
  problems: number
  // Requests waiting on this person.
  asks: number
  paused: number
  // Live sessions that are not working. Omitted counts as zero.
  idle?: number
  throttled?: number
  awaiting?: number
  pausing?: number
  wind: { left: number; by: string } | null
  act: { problem: () => void; ask: () => void; wind: () => void; paused: () => void; top: () => void }
}
export function agentsFooter(a: AgentsFooter): FooterSummary {
  if (!a.loaded) return LOADING
  if (!a.total) return build('idle', [said('No agents running')], [said('None running')], 'No agents running.')
  const n = `${a.total} ${noun(a.total, 'agent')}`
  if (a.wind) {
    return build('deliberate', [said('Winding down · '), count(a.wind.left), said(` left · by ${a.wind.by}`)], [said('Winding down')],
      `Winding down, ${a.wind.left} ${noun(a.wind.left, 'agent')} left, done by ${a.wind.by}. Show the wind-down.`, a.act.wind)
  }
  if (a.paused === a.total) {
    return build('idle', [count(a.total), said(` ${noun(a.total, 'agent')} · all paused`)], [said(`${a.total} paused`)], `${n}, all paused. Show them.`, a.act.paused)
  }
  if (a.problems) {
    const x = `${a.problems} ${noun(a.problems, 'problem')}`
    return build('problem', [count(a.total), said(` ${noun(a.total, 'agent')} · ${a.working} working · `), exception(x)], [exception(x)],
      `${n}, ${a.working} working, ${a.problems} with ${a.problems === 1 ? 'a problem' : 'problems'}. Show ${a.problems === 1 ? 'the problem' : 'them'}.`, a.act.problem)
  }
  if (a.asks) {
    const x = `${a.asks} ${a.asks === 1 ? 'asks' : 'ask'} you`
    return build('attention', [count(a.total), said(` ${noun(a.total, 'agent')} · `), exception(x)], [exception(x)],
      `${n}, ${x}. Go to the ${a.asks === 1 ? 'request' : 'requests'}.`, a.act.ask)
  }
  return agentsCalm(a, n)
}

// "All working" is true only when every counted session is working. Anything else is named.
function agentsCalm(a: AgentsFooter, n: string): FooterSummary {
  const agents = ` ${noun(a.total, 'agent')}`
  if (a.working === a.total) return build('calm', [count(a.total), said(`${agents} · all working`)], [said(`${a.total} working`)], `${n}, all working. Back to the top.`, a.act.top)
  // Most urgent remainder is the one exception. Paused is already handled when it is everyone.
  const rest: { n: number; one: string; all: string; tone: FooterTone; warn: boolean }[] = []
  const add = (countOf: number | undefined, one: string, all: string, tone: FooterTone, warn: boolean) => { if (countOf) rest.push({ n: countOf, one, all, tone, warn }) }
  add(a.awaiting, 'awaiting heartbeat', 'all awaiting heartbeat', 'attention', true)
  add(a.throttled, 'throttled', 'all throttled', 'attention', true)
  add(a.pausing, 'pausing', 'all pausing', 'idle', false)
  add(a.paused, 'paused', 'all paused', 'idle', false)
  add(a.idle, 'idle', 'all idle', 'idle', false)
  const only = rest.length === 1 ? rest[0] : undefined
  if (only && a.working === 0 && only.n === a.total) {
    return build(only.tone, [count(a.total), said(`${agents} · ${only.all}`)], [said(`${a.total} ${only.one}`)], `${n}, ${only.all}. Back to the top.`, a.act.top)
  }
  const urgent = rest[0]
  if (urgent && a.working > 0 && a.working + rest.reduce((sum, row) => sum + row.n, 0) === a.total) {
    const phrase = `${urgent.n} ${urgent.one}`
    const tail = urgent.warn ? exception(phrase) : said(phrase)
    return build(urgent.warn ? urgent.tone : 'calm', [count(a.total), said(`${agents} · ${a.working} working · `), tail], [urgent.warn ? exception(phrase) : said(`${a.working} working`)],
      `${n}, ${a.working} working, ${phrase}. Back to the top.`, a.act.top)
  }
  if (urgent) {
    const phrase = `${urgent.n} ${urgent.one}`
    return build(urgent.tone, [count(a.total), said(`${agents} · `), urgent.warn ? exception(phrase) : said(phrase)], [said(phrase)], `${n}, ${phrase}. Back to the top.`, a.act.top)
  }
  return build('calm', [count(a.total), said(`${agents} · ${a.working} working`)], [said(`${a.working} working`)], `${n}, ${a.working} working. Back to the top.`, a.act.top)
}

// ---------- Tickets ----------
export interface TicketsFooter {
  loaded: boolean
  // The list as filtered; null while it is still being counted.
  total: number | null
  withAgents: number
  // The with-agents figure is only the loaded page of a filtered list.
  withAgentsPartial?: boolean
  blocked: number
  filtered: boolean
  act: { blocked: () => void; top: () => void; clear: () => void }
}
export function ticketsFooter(t: TicketsFooter): FooterSummary {
  if (!t.loaded || t.total === null) return LOADING
  if (!t.total) {
    return t.filtered
      ? build('idle', [said('No tickets match')], [said('No matches')], 'No tickets match. Clear the filters.', t.act.clear)
      : build('idle', [said('No tickets yet')], [said('No tickets')], 'No tickets yet.')
  }
  const agents = t.withAgents || t.withAgentsPartial ? `${t.withAgents} with ${noun(t.withAgents, 'agent')}${t.withAgentsPartial ? ' loaded' : ''}` : ''
  const mid = agents ? ` ${noun(t.total, 'ticket')} · ${agents}` : ` ${noun(t.total, 'ticket')}`
  const aria = `${t.total} ${noun(t.total, 'ticket')}${agents ? `, ${agents}` : ''}`
  if (t.blocked) {
    const x = `${t.blocked} blocked`
    return build('attention', [count(t.total), said(`${mid} · `), exception(x)], [exception(x)], `${aria}, ${x}. Show the blocked tickets.`, t.act.blocked)
  }
  return build('calm', [count(t.total), said(mid)], [said(`${t.total.toLocaleString('en-GB')} ${noun(t.total, 'ticket')}`)], `${aria}. Back to the top of the list.`, t.act.top)
}

// A filtered list's total is every match. The with-agents figure is partial while a later page is not loaded.
export function withAgentsIsPartial(filtered: boolean, loaded: number, total: number | null, hasMore: boolean) {
  return filtered && (hasMore || total === null || loaded < total)
}

// ---------- Releases ----------
const FAILED = new Set(['failure', 'timed_out', 'startup_failure'])
// A release on its way (reserved or tagged, not yet published) whose checks or release run failed.
export const planned = (r: Pick<Release, 'state'>) => r.state === 'reserved' || r.state === 'candidate'
export function atRisk(r: Pick<Release, 'state' | 'evidence'>) {
  return planned(r) && [r.evidence.ci, r.evidence.release_run].some(run => !!run && FAILED.has(run.conclusion))
}
export interface ReleasesFooter { loaded: boolean; planned: number; atRisk: number; act: { risk: () => void } }
export function releasesFooter(r: ReleasesFooter): FooterSummary {
  if (!r.loaded) return LOADING
  if (!r.planned) return build('idle', [said('Nothing planned')], [said('Nothing planned')], 'Nothing planned.')
  const mid = ` planned`
  if (r.atRisk) {
    const x = `${r.atRisk} at risk`
    return build('attention', [count(r.planned), said(`${mid} · `), exception(x)], [exception(x)], `${r.planned} planned, ${x}. Open the release at risk.`, r.act.risk)
  }
  return build('calm', [count(r.planned), said(mid)], [said(`${r.planned} planned`)], `${r.planned} planned.`)
}

// ---------- Knowledge ----------
export interface KnowledgeFooter {
  loaded: boolean
  entries: number
  // Proposed entries waiting for a person.
  toReview: number
  // Latest write, in ms; null when unknown.
  updatedAt: number | null
  now: number
  act: { review: () => void; sort?: () => void }
}
export function knowledgeFooter(k: KnowledgeFooter): FooterSummary {
  if (!k.loaded) return LOADING
  if (!k.entries) return build('idle', [said('No entries yet')], [said('No entries')], 'No entries yet.')
  const head = `${k.entries.toLocaleString('en-GB')} ${noun(k.entries, 'entry', 'entries')}`
  if (k.toReview) {
    const x = `${k.toReview} to review`
    return build('attention', [count(k.entries), said(` ${noun(k.entries, 'entry', 'entries')} · `), exception(x)], [exception(x)], `${head}, ${x}. Show the entries to review.`, k.act.review)
  }
  const when = k.updatedAt === null ? '' : ` · updated ${relativeTime(new Date(k.updatedAt).toISOString(), { now: k.now, long: true })}`
  return build('calm', [count(k.entries), said(` ${noun(k.entries, 'entry', 'entries')}${when}`)], [said(head)], `${head}${when.replace(' · ', ', ')}.${k.act.sort ? ' Sort by last updated.' : ''}`, k.act.sort)
}

// ---------- Settings ----------
// How many things need this person in Accounts and computers; that section keeps it while it is open.
export const settingsNeeds = ref(0)
export interface SettingsFooter { saving: boolean; failed: boolean; needs: number; act: { retry: () => void; needs: () => void } }
// Calm says nothing: the centre stays empty.
export function settingsFooter(s: SettingsFooter): FooterSummary | null {
  // Saving and a failed save come and go. The phone release stays as the summary
  // underneath left it, including while needs you is waiting under the save line.
  const phone = s.needs > 0
  if (s.failed) return { ...build('attention', [exception('Not saved'), said(' · Try again')], [exception('Not saved')], 'Not saved. Save again.', s.act.retry), transient: true, phone }
  if (s.saving) return { ...build('idle', [said('Saving…')], [said('Saving…')], 'Saving.'), transient: true, phone }
  if (s.needs) {
    const x = `${s.needs} ${s.needs === 1 ? 'needs' : 'need'} you`
    return build('attention', [exception(x)], [exception(x)], `${x}. Open Accounts and computers at Needs you.`, s.act.needs)
  }
  return null
}

// ---------- Access ----------
export interface AccessFooter {
  loaded: boolean
  people: number
  keys: number
  // Keys that expire soon; null when this person cannot see keys.
  expiring: number | null
  act: { expiring: () => void; top: () => void; invite: (() => void) | null }
}
export function accessFooter(a: AccessFooter): FooterSummary {
  if (!a.loaded) return LOADING
  if (a.people <= 1 && !a.keys) return build('idle', [said('Only you so far')], [said('Only you')], a.act.invite ? 'Only you so far. Invite someone.' : 'Only you so far.', a.act.invite ?? undefined)
  const people = ` ${noun(a.people, 'person', 'people')}`, keys = ` agent ${noun(a.keys, 'key')}`
  const head = `${a.people.toLocaleString('en-GB')}${people}, ${a.keys.toLocaleString('en-GB')}${keys}`
  if (a.expiring) {
    const x = `${a.expiring} ${noun(a.expiring, 'key')} ${a.expiring === 1 ? 'expires' : 'expire'} soon`
    return build('attention', [count(a.people), said(`${people} · ${a.keys.toLocaleString('en-GB')}${keys} · `), exception(x)], [exception(x.replace(' soon', ''))], `${head}, ${x}. Show the expiring keys.`, a.act.expiring)
  }
  return build('calm', [count(a.people), said(`${people} · ${a.keys.toLocaleString('en-GB')}${keys}`)], [said(`${a.people.toLocaleString('en-GB')}${people}`)], `${head}. Back to the top.`, a.act.top)
}
