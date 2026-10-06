// SPDX-License-Identifier: AGPL-3.0-only
import { hostCapacityReason } from './hostCapacity.ts'
export const WAIT_CODES = ['schedule', 'reserve', 'reading', 'vendor', 'offline', 'sign_in', 'hold', 'approval', 'capacity', 'allowance', 'models', 'state', 'residency'] as const
export interface CapacityWait {
  code: typeof WAIT_CODES[number]
  host_reason?: string
  until?: string
  read_at?: string
  timezone?: string
  run_now_allowed: boolean
}
export function isCapacityWait(value: unknown): value is CapacityWait {
  if (!value || typeof value !== 'object') return false
  const v = value as Record<string, unknown>
  return WAIT_CODES.includes(v.code as CapacityWait['code']) && typeof v.run_now_allowed === 'boolean'
    && ['until', 'read_at'].every(key => v[key] === undefined || typeof v[key] === 'string' && Number.isFinite(Date.parse(v[key] as string)))
    && (v.timezone === undefined || typeof v.timezone === 'string' && v.timezone.length < 100)
}
export function capacityWaitText(wait: CapacityWait, subject = 'Agents', now = Date.now()): string {
  let at = ''
  if (wait.until) {
    const date = new Date(wait.until)
    const options: Intl.DateTimeFormatOptions = { hour: '2-digit', minute: '2-digit', hourCycle: 'h23', timeZone: wait.timezone || undefined }
    if (date.getTime() - now > 24 * 3_600_000) options.weekday = 'short'
    try { at = date.toLocaleTimeString('en-GB', options) } catch { at = date.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }) }
  }
  switch (wait.code) {
    case 'schedule': return at ? `${subject} start at ${at}` : `${subject} wait for scheduled hours`
    case 'reserve': return at ? `Kept for you until ${at}` : 'Kept for you'
    case 'reading':
      if (!wait.read_at && wait.timezone) return 'Waiting for the next allowed run (light by day)'
      return wait.read_at ? `Waiting for a reading · last ${Math.max(0, Math.floor((now - Date.parse(wait.read_at)) / 60_000))} min ago` : 'Waiting for the first run’s reading'
    case 'vendor': return at ? `Vendor says stop until ${at}` : 'Waiting for the vendor to allow work again'
    case 'offline': return 'Waiting for the computer'
    case 'sign_in': return 'Sign in again on the computer'
    case 'hold': return at ? `On hold until ${at}` : 'On hold until you resume'
    case 'approval': return 'Allow agents in Settings / Accounts'
    case 'capacity': return wait.host_reason ? `Waiting for host capacity: ${hostCapacityReason(wait.host_reason)}` : 'Waiting for the current run to finish'
    case 'allowance': return at ? `Capacity available after ${at}` : 'Waiting for capacity'
    case 'residency': return 'Waiting for an account within the allowed providers'
    case 'models': return 'No model is granted for this account'
    // Why and how to resume, not just that it is paused (AEON-402).
    case 'state': return 'Paused in Settings / Accounts; turn “Agents may use it” back on to resume'
  }
}
