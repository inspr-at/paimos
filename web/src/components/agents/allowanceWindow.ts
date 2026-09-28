// SPDX-License-Identifier: AGPL-3.0-only
// Person allowance drafts for an enrolled account. Dates stay in the browser's
// local zone until save, and a lost response is not posted again.
import { APIError, RequestFailure } from '../../lib/api.ts'
import { listAccounts, type AllowanceWindow, type AllowanceWrite } from '../../lib/agents.ts'

export const ALLOWANCE_UNITS = ['requests', 'tokens', 'cost_micros'] as const
export const ALLOWANCE_PACES = ['steady', 'frontload', 'unrestricted'] as const
export const UNIT_OPTIONS = [
  { value: 'requests', label: 'Requests' },
  { value: 'tokens', label: 'Tokens' },
  { value: 'cost_micros', label: 'Cost in micros' },
] as const
export const PACE_OPTIONS = [
  { value: 'steady', label: 'Steady' },
  { value: 'frontload', label: 'Front-loaded' },
  { value: 'unrestricted', label: 'Unrestricted' },
] as const

export const INVALID_ALLOWANCE = 'Enter valid start and end times, a positive whole allowance, and a burst ratio from 0 to 1.'
export const IMPOSSIBLE_DATE = 'That date does not exist. Enter the start and end you mean.'
export const NORMALISED_DATE = 'That time does not exist in your local time zone. Enter the time you mean. It is not adjusted for you.'
export const ENDED_WINDOW = 'This window has already ended. Choose an end after now. Past times are not moved forward.'
export const UNCERTAIN_ALLOWANCE = 'The allowance may already be saved. Check this account before sending it again. It was not sent again.'
export const ABSENT_ALLOWANCE = 'This allowance is not on the account. You can send it again.'

const LOCAL_TIME = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2}))?$/
const ISO_INSTANT = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/

export interface CivilTime {
  year: number
  month: number
  day: number
  hour: number
  minute: number
  second: number
}

export interface AllowanceDraft {
  startsLocal: string
  endsLocal: string
  allowance: number | null
  unit: AllowanceWrite['unit']
  pace: AllowanceWrite['pace_model']
  burst: number | null
}

export interface AllowanceReviewOk {
  ok: true
  startsAt: string
  endsAt: string
  startsInPast: boolean
  overlap: boolean
  body: AllowanceWrite
}

export interface AllowanceReviewBad {
  ok: false
  message: string
}

export type AllowanceReview = AllowanceReviewOk | AllowanceReviewBad

export interface KnownWindow {
  starts_at: string
  ends_at: string
  unit: string
}

export function emptyAllowanceDraft(): AllowanceDraft {
  return { startsLocal: '', endsLocal: '', allowance: null, unit: 'requests', pace: 'unrestricted', burst: 0 }
}

export function localZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'Local time'
  } catch {
    return 'Local time'
  }
}

export function formatInstant(iso: string, zone: string): string {
  const date = new Date(iso)
  if (!Number.isFinite(date.getTime())) return iso
  try {
    return new Intl.DateTimeFormat('en-GB', {
      day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit', timeZone: zone, hourCycle: 'h23',
    }).format(date)
  } catch {
    return iso
  }
}

export function readLocalCivil(date: Date): CivilTime {
  return {
    year: date.getFullYear(), month: date.getMonth() + 1, day: date.getDate(),
    hour: date.getHours(), minute: date.getMinutes(), second: date.getSeconds(),
  }
}

export function localDate(civil: CivilTime): Date {
  return new Date(civil.year, civil.month - 1, civil.day, civil.hour, civil.minute, civil.second, 0)
}

function daysInMonth(year: number, month: number): number {
  return new Date(Date.UTC(year, month, 0)).getUTCDate()
}

export function parseCivilTime(value: string): { ok: true; civil: CivilTime } | { ok: false; message: string } {
  const match = LOCAL_TIME.exec(value)
  if (!match) return { ok: false, message: value.trim() === '' ? INVALID_ALLOWANCE : IMPOSSIBLE_DATE }
  const civil: CivilTime = {
    year: Number(match[1]), month: Number(match[2]), day: Number(match[3]),
    hour: Number(match[4]), minute: Number(match[5]), second: Number(match[6] ?? 0),
  }
  if (civil.month < 1 || civil.month > 12 || civil.hour > 23 || civil.minute > 59 || civil.second > 59) {
    return { ok: false, message: IMPOSSIBLE_DATE }
  }
  if (civil.day < 1 || civil.day > daysInMonth(civil.year, civil.month)) return { ok: false, message: IMPOSSIBLE_DATE }
  return { ok: true, civil }
}

function sameCivil(a: CivilTime, b: CivilTime): boolean {
  return a.year === b.year && a.month === b.month && a.day === b.day && a.hour === b.hour && a.minute === b.minute && a.second === b.second
}

export function instantFromCivil(
  civil: CivilTime,
  toDate: (civil: CivilTime) => Date = localDate,
  read: (date: Date) => CivilTime = readLocalCivil,
): { ok: true; date: Date } | { ok: false; message: string } {
  const date = toDate(civil)
  if (!Number.isFinite(date.getTime()) || !sameCivil(civil, read(date))) return { ok: false, message: NORMALISED_DATE }
  return { ok: true, date }
}

export function validBurst(value: number | null): value is number {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < 0 || value > 1) return false
  return Math.abs(Math.round(value * 10000) / 10000 - value) < 1e-8
}

export function validAllowance(value: number | null): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 1
}

export function windowsOverlap(windows: readonly KnownWindow[], unit: string, startsAt: number, endsAt: number): boolean {
  return windows.some(window => {
    if (window.unit !== unit) return false
    const start = Date.parse(window.starts_at)
    const end = Date.parse(window.ends_at)
    return Number.isFinite(start) && Number.isFinite(end) && start < endsAt && end > startsAt
  })
}

export function reviewAllowance(
  draft: AllowanceDraft,
  windows: readonly KnownWindow[],
  now: number,
  toDate?: (civil: CivilTime) => Date,
  read?: (date: Date) => CivilTime,
): AllowanceReview {
  if (!(ALLOWANCE_UNITS as readonly string[]).includes(draft.unit) || !(ALLOWANCE_PACES as readonly string[]).includes(draft.pace)) {
    return { ok: false, message: INVALID_ALLOWANCE }
  }
  if (!validAllowance(draft.allowance) || !validBurst(draft.burst)) return { ok: false, message: INVALID_ALLOWANCE }
  const starts = parseCivilTime(draft.startsLocal)
  const ends = parseCivilTime(draft.endsLocal)
  if (!starts.ok) return starts
  if (!ends.ok) return ends
  const startInstant = instantFromCivil(starts.civil, toDate, read)
  const endInstant = instantFromCivil(ends.civil, toDate, read)
  if (!startInstant.ok) return startInstant
  if (!endInstant.ok) return endInstant
  if (endInstant.date.getTime() <= startInstant.date.getTime()) return { ok: false, message: INVALID_ALLOWANCE }
  if (endInstant.date.getTime() <= now) return { ok: false, message: ENDED_WINDOW }
  const body: AllowanceWrite = {
    starts_at: startInstant.date.toISOString(),
    ends_at: endInstant.date.toISOString(),
    unit: draft.unit,
    allowance: draft.allowance,
    pace_model: draft.pace,
    burst_ratio: draft.burst,
  }
  return {
    ok: true,
    startsAt: body.starts_at,
    endsAt: body.ends_at,
    startsInPast: startInstant.date.getTime() < now,
    overlap: windowsOverlap(windows, body.unit, startInstant.date.getTime(), endInstant.date.getTime()),
    body,
  }
}

export function writtenAllowanceError(body: AllowanceWrite, now: number): string | null {
  if (!ISO_INSTANT.test(body.starts_at) || !ISO_INSTANT.test(body.ends_at)) return INVALID_ALLOWANCE
  const starts = Date.parse(body.starts_at)
  const ends = Date.parse(body.ends_at)
  if (!Number.isFinite(starts) || !Number.isFinite(ends) || ends <= starts || ends <= now) return ends <= now && ends > starts ? ENDED_WINDOW : INVALID_ALLOWANCE
  if (!(ALLOWANCE_UNITS as readonly string[]).includes(body.unit) || !(ALLOWANCE_PACES as readonly string[]).includes(body.pace_model)) return INVALID_ALLOWANCE
  if (!validAllowance(body.allowance) || !validBurst(body.burst_ratio)) return INVALID_ALLOWANCE
  return null
}

export function beginSave(input: {
  busy: boolean
  editingId: string
  accountId: string
  generation: number
  body: AllowanceWrite
  now: number
}): { action: 'ignore' } | { action: 'invalid'; message: string } | { action: 'send'; accountId: string; generation: number; body: AllowanceWrite } {
  if (input.busy || input.editingId !== input.accountId) return { action: 'ignore' }
  const message = writtenAllowanceError(input.body, input.now)
  if (message) return { action: 'invalid', message }
  return { action: 'send', accountId: input.accountId, generation: input.generation, body: input.body }
}

export interface SaveSettlement {
  refresh: boolean
  closeForm: boolean
  uncertain: boolean
  formMessage: string | null
  status: string | null
}

export function settleSave(input: {
  outcome: 'saved' | 'uncertain' | 'rejected'
  message: string
  accountName: string
  startedAccountId: string
  startedGeneration: number
  currentAccountId: string
  currentGeneration: number
}): SaveSettlement {
  const same = input.startedAccountId === input.currentAccountId && input.startedGeneration === input.currentGeneration
  if (input.outcome === 'saved') {
    return { refresh: true, closeForm: same, uncertain: false, formMessage: null, status: `Allowance window saved for ${input.accountName}.` }
  }
  if (!same) {
    return {
      refresh: false, closeForm: false, uncertain: false, formMessage: null,
      status: input.outcome === 'uncertain' ? `The allowance for ${input.accountName} may already be saved. Check that account before sending it again. It was not sent again.` : null,
    }
  }
  return {
    refresh: false, closeForm: false, uncertain: input.outcome === 'uncertain',
    formMessage: input.outcome === 'uncertain' ? UNCERTAIN_ALLOWANCE : input.message,
    status: null,
  }
}

export function allowanceFailure(error: unknown): { kind: 'uncertain' | 'forbidden' | 'overlap' | 'rejected'; message: string } {
  if (error instanceof APIError) {
    if (error.status === 403) return { kind: 'forbidden', message: error.message || 'permission denied' }
    if (error.status === 409) return { kind: 'overlap', message: error.message || 'allowance windows overlap' }
    if (error.status === 408 || error.status === 429 || error.status >= 500) return { kind: 'uncertain', message: UNCERTAIN_ALLOWANCE }
    return { kind: 'rejected', message: error.message || 'The allowance was not saved.' }
  }
  if (error instanceof RequestFailure) return { kind: 'uncertain', message: UNCERTAIN_ALLOWANCE }
  return { kind: 'uncertain', message: UNCERTAIN_ALLOWANCE }
}

export function sameAllowance(window: AllowanceWindow, body: AllowanceWrite): boolean {
  return window.unit === body.unit
    && window.allowance === body.allowance
    && window.pace_model === body.pace_model
    && Math.abs(window.burst_ratio - body.burst_ratio) < 0.00005
    && Math.abs(Date.parse(window.starts_at) - Date.parse(body.starts_at)) < 1000
    && Math.abs(Date.parse(window.ends_at) - Date.parse(body.ends_at)) < 1000
}

export async function findSavedAllowance(accountId: string, body: AllowanceWrite, list: typeof listAccounts = listAccounts): Promise<'saved' | 'absent' | 'unknown'> {
  try {
    const accounts = await list()
    const account = accounts.find(item => item.id === accountId)
    if (!account) return 'unknown'
    return (account.windows ?? []).some(window => sameAllowance(window, body)) ? 'saved' : 'absent'
  } catch {
    return 'unknown'
  }
}
