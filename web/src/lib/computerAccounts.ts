// SPDX-License-Identifier: AGPL-3.0-only
// Accounts and computers on /agents (AEON-499): one card per computer, its
// accounts nested inside with exactly one readiness state each, and capacity
// shown only as the server measured it. An offline computer never says
// "ready"; missing readings distinguish supported readers from harnesses
// that do not report a limit, and the legend
// appears only where a bar is drawn. Everything here is a pure mapping of the
// capacity projection, the pairing list and the person's schedule.
import { agentUpdateAdvice, describeEnrollmentDiagnostic, describeEnrollmentStatus, describeHarnessFix, describeHarnessHint, platformCaption, type PairingEnrollment, type PairingView } from './agentPairing.ts'
import {
  HARNESS_NAME, LOGIN_COMMAND, accountPlan, activeOverride, estimateLabel, gauge as gaugeOf, hidesCapacityLimit, hourLabel, pct, unreportedCapacity, when, type AccountRow, type CapacitySchedule, type CapacityWindow, type Gauge,
} from './capacity.ts'
import { capacityWaitText } from './capacityWait.ts'

export type Tone = 'ok' | 'warn' | 'mute'
export type ReadinessKind = 'ready' | 'offline' | 'signin' | 'limit' | 'kept' | 'hold' | 'hours' | 'paused' | 'attention' | 'setup' | 'unavailable' | 'waiting' | 'busy'
export interface Readiness {
  kind: ReadinessKind
  text: string
  tone: Tone
  /** The one command that fixes it, to copy and run on the computer. */
  command?: string
  /** A longer sentence for the tooltip and screen readers. */
  tip?: string
  /** One short sentence the state and command alone do not say (from the computer's report). */
  hint?: string
}
export type CapacityCell =
  | { kind: 'bar'; left: number; resets: string; window: string; five: number | null; gauge: Gauge; dim: boolean; source: string; label: string; note: string }
  | { kind: 'none' }
  | { kind: 'offline' }
  | { kind: 'quiet'; text: string }
export interface AccountLine {
  id: string
  harness: string
  /** The vendor's name: Codex, Claude, Cursor. */
  vendor: string
  /** The full sign-in identity, e.g. admin@augmentoring.com. */
  identity: string
  /** Its group when kept separate from the vendor's pool ("Client"). */
  group: string
  readiness: Readiness
  capacity: CapacityCell
  row: AccountRow
}
export interface ComputerNotice { title: string; body: string; command: string }
export interface ComputerCard {
  key: string
  name: string
  /** The pairing record; null for accounts whose computer this person cannot list. */
  computer: PairingView | null
  status: { text: string; tone: Tone; live: boolean } | null
  caption: string
  agent: string
  /** The computer's helper needs an update before new work starts there. */
  advice: string
  notice: ComputerNotice | null
  accounts: AccountLine[]
  /** Draw the bar key: only when a bar is shown on this card. */
  legend: boolean
  offline: boolean
}

const ago = (iso: string | null | undefined, now: number) => {
  const at = iso ? Date.parse(iso) : NaN
  if (!Number.isFinite(at)) return ''
  const minutes = Math.max(0, Math.round((now - at) / 60_000))
  if (minutes < 1) return 'just now'
  if (minutes < 60) return `${minutes} min ago`
  const hours = Math.round(minutes / 60)
  return hours < 48 ? `${hours} h ago` : `${Math.round(hours / 24)} d ago`
}

/** "macOS · Apple silicon · ~/Code": the platform as people say it, the home folder as ~. */
export function computerCaption(view: Pick<PairingView, 'platform' | 'arch' | 'workspace_path'>): string {
  const mac = view.platform === 'darwin'
  const chip = mac && /^(arm64|aarch64)$/.test(view.arch) ? 'Apple silicon' : mac && /^(amd64|x86_64)$/.test(view.arch) ? 'Intel' : ''
  const where = chip ? `macOS · ${chip}` : platformCaption(view.platform, view.arch)
  const path = (view.workspace_path || '').replace(/^\/(Users|home)\/[^/]+(?=\/|$)/, '~')
  return [where, path].filter(Boolean).join(' · ')
}

/** Online, offline since a time, disconnecting, or still setting up: never two at once. */
export function computerStatus(view: PairingView, now: number): ComputerCard['status'] {
  if (view.computer_state === 'draining') return { text: 'Disconnecting', tone: 'warn', live: false }
  if (view.computer_state !== 'connected') return { text: 'Not connected yet', tone: 'mute', live: false }
  if (view.connectivity === 'offline') {
    const seen = view.last_seen_at ? when(view.last_seen_at, now) : ''
    return { text: seen ? `Offline since ${seen}` : 'Offline', tone: 'warn', live: false }
  }
  // A live heartbeat is online whatever the setup flag says; that flag is computer-wide.
  if (view.connectivity === 'online') return { text: `Online · seen ${ago(view.last_seen_at, now) || 'recently'}`, tone: 'ok', live: true }
  if (view.setup_state !== 'connected') return { text: 'Setting up', tone: 'mute', live: false }
  return { text: 'No recent report', tone: 'mute', live: false }
}

/** The reason and the one fix for an offline computer. */
export function offlineNotice(view: PairingView, now: number): ComputerNotice | null {
  if (view.computer_state !== 'connected' || view.connectivity !== 'offline') return null
  const thing = view.platform === 'darwin' ? 'Mac' : 'computer'
  const seen = view.last_seen_at ? when(view.last_seen_at, now) : ''
  return {
    title: seen ? `The agent on this ${thing} stopped reporting at ${seen}.` : `The agent on this ${thing} isn't reporting.`,
    body: `Agents can't start here until it's back. On ${view.computer_name}, run:`,
    command: 'aeon-agentd status',
  }
}

/**
 * One state per account. The computer comes first (offline, disconnecting,
 * setup), then the account (signed out, needs attention on its computer,
 * paused), then the server's routing wait (limit, kept for you, hold, hours).
 */
export function readiness(row: AccountRow, computer: PairingView | null, now: number): Readiness {
  const enrollment = computer?.enrollments.find(e => e.account_id === row.id && e.state !== 'revoked')
  if (computer?.computer_state === 'connected' && computer.connectivity === 'offline') return { kind: 'offline', text: 'Paused · computer offline', tone: 'warn', tip: `Agents can't start on ${computer.computer_name} until it reports again.` }
  if (!computer && row.state === 'offline') return { kind: 'offline', text: 'Paused · computer offline', tone: 'warn', tip: `Agents can't start on ${row.host} until it reports again.` }
  if (computer?.computer_state === 'draining' || enrollment?.state === 'draining' || row.disconnecting) return { kind: 'paused', text: 'Paused · disconnecting', tone: 'mute', tip: `Disconnecting from ${computer?.computer_name ?? row.host}: agents start nothing new on it.` }
  if (computer && computer.computer_state === 'connected' && computer.setup_state !== 'connected' && computer.connectivity !== 'online') return { kind: 'setup', text: 'Waiting for setup', tone: 'mute', tip: `${computer.computer_name} has not confirmed that setup finished.` }
  const diagnostic = computer && enrollment ? describeEnrollmentDiagnostic(computer, enrollment) : null
  if (diagnostic?.hint) {
    const signedOut = /sign in/i.test(describeEnrollmentStatus(computer!, enrollment!))
    return { kind: signedOut ? 'signin' : 'attention', text: signedOut ? 'Signed out' : 'Sign-in check failed', tone: 'warn', ...diagnostic }
  }
  if (row.state === 'signin') return { kind: 'signin', text: 'Signed out', tone: 'warn', command: LOGIN_COMMAND[row.harness] ?? '', tip: `Sign in again on ${computer?.computer_name ?? row.host}; the password stays with the vendor.` }
  if (computer && enrollment) {
    const label = describeEnrollmentStatus(computer, enrollment)
    if (label && label !== 'Ready') {
      const soleAccount = computer.enrollments.filter(e => e.harness === row.harness && e.state !== 'revoked').length === 1
      const command = /^Verif/.test(label) ? '' : diagnostic?.command ?? (soleAccount ? describeHarnessFix(computer, row.harness) : '')
      const signin = /sign in/i.test(label)
      const waiting = /^(Verification queued|Verifying account)/.test(label)
      return { kind: waiting ? 'waiting' : signin ? 'signin' : 'attention', text: signin ? 'Signed out' : label, tone: waiting ? 'mute' : 'warn', ...(command ? { command } : {}), tip: `${HARNESS_NAME[row.harness] ?? row.harness} on ${computer.computer_name}: ${label}.` }
    }
  }
  if (row.state === 'paused') return { kind: 'paused', text: 'Paused in Settings', tone: 'mute', tip: 'Turn “Agents may use it” back on in Settings / Accounts to resume.' }
  if (row.state === 'unavailable') return { kind: 'unavailable', text: "Couldn't check", tone: 'warn', tip: `Could not check this account on ${computer?.computer_name ?? row.host}; agents skip it until the next check succeeds.` }
  const wait = row.routing?.rank === 0 ? row.routing.wait : undefined
  const at = (iso?: string) => (iso ? when(iso, now) : '')
  if (wait) {
    const tip = capacityWaitText(wait, 'Agents', now)
    switch (wait.code) {
      case 'vendor': case 'allowance': return { kind: 'limit', text: wait.until ? `At limit until ${at(wait.until)}` : 'At limit', tone: 'warn', tip }
      case 'reserve': return { kind: 'kept', text: wait.until ? `Kept for you until ${at(wait.until)}` : 'Kept for you', tone: 'mute', tip }
      case 'hold': return { kind: 'hold', text: wait.until ? `On hold until ${at(wait.until)}` : 'On hold', tone: 'mute', tip }
      case 'schedule': return { kind: 'hours', text: wait.until ? `Starts ${at(wait.until)}` : 'Outside your hours', tone: 'mute', tip }
      case 'offline': return { kind: 'offline', text: 'Paused · computer offline', tone: 'warn', tip }
      case 'sign_in': return { kind: 'signin', text: 'Signed out', tone: 'warn', command: LOGIN_COMMAND[row.harness] ?? '', tip }
      case 'state': return { kind: 'paused', text: 'Paused in Settings', tone: 'mute', tip }
      case 'approval': return { kind: 'paused', text: 'Not allowed for agents', tone: 'mute', tip }
      case 'models': return { kind: 'attention', text: 'No model granted', tone: 'warn', tip }
      case 'residency': return { kind: 'attention', text: 'Outside allowed providers', tone: 'warn', tip }
      // The server starts nothing here until its run ends or a reading arrives: not ready, nothing to fix.
      case 'capacity': return { kind: 'busy', text: 'Busy · run in progress', tone: 'mute', tip }
      case 'reading': return { kind: 'waiting', text: 'Waiting for a reading', tone: 'mute', tip }
    }
  }
  // Without routing advice, the pool's own Hold still pauses it.
  const override = activeOverride(row.schedule, now)
  if (override === 'hold') {
    const until = row.schedule?.override_until
    return { kind: 'hold', text: until ? `On hold until ${at(until)}` : 'On hold', tone: 'mute', tip: `Agents leave ${HARNESS_NAME[row.harness] ?? row.harness} alone${until ? ` until ${at(until)}` : ' until you resume'}. Running steps finish.` }
  }
  // Without routing advice, a measured window at zero is still the limit.
  const spent = [row.primary, row.five].filter((w): w is CapacityWindow => !!w && w.remaining_percent < 1)
    .sort((a, b) => Date.parse(b.reading.resets_at) - Date.parse(a.reading.resets_at))[0]
  if (spent) return { kind: 'limit', text: `At limit until ${at(spent.reading.resets_at)}`, tone: 'warn', tip: `${HARNESS_NAME[row.harness] ?? row.harness} reported its limit used up; it resets ${at(spent.reading.resets_at)}.` }
  return { kind: 'ready', text: 'Ready', tone: 'ok' }
}

const WINDOW_WORD: Record<string, string> = { weekly: 'this week', monthly: 'this month', '5h': 'this window', other: 'this period' }

/** The capacity cell: a bar only for a real reading, otherwise one honest sentence. */
export function capacityCell(row: AccountRow, ready: Readiness, now: number, sharedWith = ''): CapacityCell {
  if (sharedWith) return { kind: 'quiet', text: `Shares quota with ${sharedWith}` }
  const w = row.primary
  if (!w) return ready.kind === 'offline' ? { kind: 'offline' } : hidesCapacityLimit(row.harness) ? { kind: 'quiet', text: unreportedCapacity(row.harness) } : { kind: 'none' }
  const plan = accountPlan(row, now)
  const g = gaugeOf(row, plan)
  const left = Math.max(0, Math.min(100, w.remaining_percent))
  const stale = w.freshness === 'stale' || w.freshness === 'expired'
  const read = ago(w.reading.read_at, now)
  const source = ready.kind === 'offline' ? `Last reading ${read}` : stale ? `Reading is old · ${read}` : w.reading.source === 'estimate' ? estimateLabel(w.reading) : ''
  const five = row.five ? Math.round(row.five.remaining_percent) : null
  const override = plan?.override ?? ''
  const until = row.schedule?.override_until
  const note = override === 'sprint' ? `Sprint: all of it until ${until ? when(until, now) : 'the reset'}` : override === 'away' ? "Away: all of it until you're back" : plan?.atReserve ? 'Kept for you while you work' : ''
  const label = `${row.name}: ${pct(left)} left ${WINDOW_WORD[w.reading.window_kind] ?? ''}, resets ${when(w.reading.resets_at, now)}${plan && g.tick !== null ? `; today's share ${pct(plan.budget)}` : ''}`
  return { kind: 'bar', left, resets: when(w.reading.resets_at, now), window: WINDOW_WORD[w.reading.window_kind] ?? '', five, gauge: g, dim: ready.kind !== 'ready' || stale, source, label, note }
}

/** "7 days · keep 10% · nights 22–08": the pacing in one line for its button. */
export function pacingSummary(s: Pick<CapacitySchedule, 'week' | 'reserve' | 'reserve_percent' | 'nights' | 'model' | 'night'>): string {
  const days = s.week.filter(d => d.on).length
  const keep = s.reserve === 'off' ? 'keep nothing' : s.reserve === 'fixed' ? `keep ${s.reserve_percent ?? 0}%` : 'keep auto'
  const nights = !s.nights ? 'no nights' : s.model === 'shifts' ? 'nights in 3 shifts' : s.model === 'blocks' ? 'nights custom' : `nights ${hourLabel(s.night.start)}–${hourLabel(s.night.end)}`
  return `${days} ${days === 1 ? 'day' : 'days'} · ${keep} · ${nights}`
}

/**
 * The cards in order: computers with something to fix first, then by name.
 * Accounts on a computer this person cannot list are grouped by their host.
 */
export function buildComputerCards(input: { computers: PairingView[]; rows: AccountRow[]; now: number }): ComputerCard[] {
  const { now } = input
  const live = input.computers.filter(c => c.computer_state !== 'revoked' && c.computer_id)
  const owner = new Map<string, PairingView>()
  for (const c of live) for (const e of c.enrollments) if (e.state !== 'revoked') owner.set(e.account_id, c)
  // A stale accounts read must not resurrect a revoked binding as an unpaired
  // live account. A newer active binding wins; unrelated unpaired rows remain.
  const revoked = new Set(input.computers.flatMap(c => c.enrollments.filter(e => c.computer_state === 'revoked' || e.state === 'revoked').map(e => e.account_id)))
  const rows = input.rows.filter(row => !revoked.has(row.id) || owner.has(row.id))
  const byId = new Map(rows.map(r => [r.id, r]))
  const line = (row: AccountRow, computer: PairingView | null): AccountLine => {
    const state = readiness(row, computer, now)
    // The harness hint, but not its "n of m accounts" summary: each row says its own state.
    const hint = computer && !state.kind.match(/^(offline|paused|setup)$/) ? describeHarnessHint(computer, row.harness) : ''
    if (!state.hint && hint && !/accounts? needs? attention$/.test(hint)) state.hint = hint
    const shared = row.sameQuotaAs && byId.has(row.sameQuotaAs) && byId.get(row.sameQuotaAs)!.primary ? byId.get(row.sameQuotaAs)! : null
    const sharedWith = shared ? `${shared.name}${shared.host && shared.host !== row.host ? ` on ${shared.host}` : ''}` : ''
    return { id: row.id, harness: row.harness, vendor: HARNESS_NAME[row.harness] ?? row.harness, identity: row.name, group: row.groupName, readiness: state, capacity: capacityCell(row, state, now, sharedWith), row }
  }
  const cards: ComputerCard[] = live.map(computer => {
    const own = rows.filter(r => owner.get(r.id) === computer)
    // An account the pairing names but the account list does not (no access to it,
    // or not read yet) still shows, judged by its computer alone.
    const known = new Set(own.map(r => r.id))
    const fromPairing = computer.enrollments.filter(e => e.state !== 'revoked' && !known.has(e.account_id)).map(e => enrollmentRow(e, computer))
    const accounts = [...own, ...fromPairing].map(r => line(r, computer))
    return {
      key: computer.computer_id!, name: computer.computer_name, computer, status: computerStatus(computer, now), caption: computerCaption(computer),
      agent: computer.agent_release?.version ? `aeon-agentd · ${computer.agent_release.version}` : '', advice: agentUpdateAdvice(computer),
      notice: offlineNotice(computer, now), accounts, legend: accounts.some(a => a.capacity.kind === 'bar' && a.capacity.gauge.tick !== null),
      offline: computer.computer_state === 'connected' && computer.connectivity === 'offline',
    }
  })
  const loose = new Map<string, AccountRow[]>()
  for (const row of rows) if (!owner.has(row.id)) loose.set(row.host || 'Unknown computer', [...(loose.get(row.host || 'Unknown computer') ?? []), row])
  for (const [host, rows] of loose) {
    const accounts = rows.map(r => line(r, null))
    // Accounts from an older pairing of a listed computer stay on its card.
    const same = cards.find(c => c.computer && c.name === host)
    if (same) { same.accounts.push(...accounts); same.legend ||= accounts.some(a => a.capacity.kind === 'bar' && a.capacity.gauge.tick !== null); continue }
    const offline = rows.every(r => r.state === 'offline')
    cards.push({
      key: `host:${host}`, name: host, computer: null, status: offline ? { text: 'Offline', tone: 'warn', live: false } : null, caption: '', agent: '', advice: '', notice: null,
      accounts, legend: accounts.some(a => a.capacity.kind === 'bar' && a.capacity.gauge.tick !== null), offline,
    })
  }
  const order = (vendor: string) => { const i = ['Codex', 'Claude', 'Grok', 'Cursor', 'Pi'].indexOf(vendor); return i < 0 ? 9 : i }
  for (const card of cards) card.accounts.sort((a, b) => order(a.vendor) - order(b.vendor) || a.identity.localeCompare(b.identity))
  const attention = (c: ComputerCard) => (c.offline || c.accounts.some(a => a.readiness.tone === 'warn') ? 0 : 1)
  return cards.sort((a, b) => attention(a) - attention(b) || a.name.localeCompare(b.name))
}

/** An account known only from its computer's pairing: no reading, no routing. */
function enrollmentRow(e: PairingEnrollment, computer: PairingView): AccountRow {
  return {
    id: e.account_id, name: e.label, host: computer.computer_name, harness: e.harness, state: 'unread', primary: null, five: null, schedule: null, plan: '',
    limitingReset: '', awaitingReading: false, fingerprint: '', groupId: '', groupName: '', hosts: [computer.computer_name], sameQuotaAs: '',
    ...(e.state === 'draining' ? { disconnecting: true } : {}),
  }
}

/** The header pill: "2 of 2 ready", "0 of 2 ready · mbp2607 offline". */
export function readySummary(cards: ComputerCard[]): { text: string; tone: Tone } | null {
  const lines = cards.flatMap(c => c.accounts)
  if (!lines.length) return null
  const ready = lines.filter(l => l.readiness.kind === 'ready').length
  const offline = cards.filter(c => c.offline && c.accounts.length)
  const tail = offline.length === 1 ? ` · ${offline[0].name} offline` : offline.length > 1 ? ` · ${offline.length} computers offline` : ''
  // Busy or waiting is not a problem: the pill warns only when something needs fixing.
  const tone: Tone = ready === lines.length ? 'ok' : offline.length || lines.some(l => l.readiness.tone === 'warn') ? 'warn' : 'mute'
  return { text: `${ready} of ${lines.length} ready${tail}`, tone }
}

/**
 * Middle ellipsis for an identity that does not fit: the start and the domain
 * stay readable ("admin@augmen…ring.com"). Short ones are returned whole.
 */
export function middleEllipsis(text: string, max: number): string {
  if (text.length <= max || max < 5) return text
  const keep = max - 1
  const tail = Math.ceil(keep / 2)
  return `${text.slice(0, keep - tail)}…${text.slice(text.length - tail)}`
}
