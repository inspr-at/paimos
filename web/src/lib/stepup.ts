// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1048: native step-up requests on the Decision Desk. The server owns the
// typed target, before/after snapshots, digest binding and the one-use proof.
import { api, APIError } from './api'
import { credentialJSON, requestOptions } from './deskPhoneApproval'

export type StepupState = 'pending' | 'applied' | 'declined' | 'expired' | 'stale' | 'withdrawn' | 'failed'
export type StepupMethod = 'passkey_platform' | 'passkey' | 'oidc_reauth'
export interface StepupRequest {
  id: string; requested_by: string; session_id?: string; project_id?: string; permission: string
  payload: Record<string, unknown>; before: Record<string, unknown>; after: Record<string, unknown>
  before_hash: string; after_hash: string; request_digest: string; created_at: string; expires_at: string
  state: StepupState; revision: number; decided_by?: string; decided_by_name?: string
  decision?: 'approve' | 'decline' | 'withdraw' | 'expire'; method?: StepupMethod; auth_time?: string; applied_at?: string; decided_at?: string
}
interface StepupPage { items: StepupRequest[]; has_more: boolean; next_cursor?: string }
type Options = { method: 'passkey'; challenge_id: string; publicKey: Parameters<typeof requestOptions>[0] } | { method: 'oidc_reauth'; authorize_url: string }
export interface StepupRow { label: string; value: string; changed: boolean }

/** Approve left the page for the fresh sign-in; no outcome is known yet. */
export class StepupNavigation extends Error {
  constructor() { super('Opening the fresh sign-in. The desk returns to this request afterwards.') }
}

const MAX_BYTES = 8 * 1024 * 1024
async function stepupRequest<T>(path: string, input?: unknown): Promise<T> {
  const response = await api(path, input === undefined ? undefined : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(input) })
  if (Number(response.headers.get('Content-Length')) > MAX_BYTES) { await response.body?.cancel(); throw new Error('The step-up response is too large.') }
  if (!response.body) throw new APIError(response.status, 'The step-up response is missing.')
  const reader = response.body.getReader(), decoder = new TextDecoder()
  let bytes = 0, text = ''
  try {
    for (;;) {
      const chunk = await reader.read(); if (chunk.done) break
      bytes += chunk.value.byteLength
      if (bytes > MAX_BYTES) { await reader.cancel(); throw new Error('The step-up response is too large.') }
      text += decoder.decode(chunk.value, { stream: true })
    }
    text += decoder.decode()
  } finally { reader.releaseLock() }
  const result = JSON.parse(text || '{}')
  if (!response.ok) {
    const message = typeof result.error === 'string' && result.error ? result.error : 'the step-up action failed'
    throw new APIError(response.status, `${message[0]!.toUpperCase()}${message.slice(1)}. Nothing changed.`, result)
  }
  return result
}

/** One bounded window per state; truncation is reported, never hidden. */
export async function readStepups(): Promise<{ items: StepupRequest[]; warnings: string[] }> {
  const items: StepupRequest[] = [], warnings: string[] = []
  for (const state of ['pending', 'decided'] as const) {
    const page = await stepupRequest<StepupPage>(`/stepup-requests?state=${state}&limit=100`)
    if (!Array.isArray(page.items) || page.items.length > 100 || typeof page.has_more !== 'boolean') throw new Error('The step-up page is invalid.')
    items.push(...page.items)
    if (page.has_more) warnings.push(state === 'pending' ? 'More step-up approvals are waiting; the first 100 are shown.' : 'Only the newest 100 decided step-up approvals are shown.')
  }
  return { items, warnings }
}

const decision = (request: StepupRequest) => ({ request_digest: request.request_digest, revision: request.revision })
function confirmed(request: StepupRequest, result: StepupRequest): StepupRequest {
  if (result.id !== request.id || result.request_digest !== request.request_digest || result.state === 'pending') throw new Error('The step-up result could not be confirmed. Refresh the desk.')
  return result
}
function navigable(url: string): URL {
  const target = new URL(url)
  if (target.protocol !== 'https:' && !(target.protocol === 'http:' && location.protocol === 'http:')) throw new Error('The sign-in address is not secure. Nothing changed.')
  return target
}
export interface StepupDevices { credentials?: Pick<CredentialsContainer, 'get'>; navigate?: (url: string) => void }

/** Approve: an existing passkey when the person has one, otherwise a fresh
 * OIDC sign-in. The server binds either proof to this person, request digest
 * and decision. Assertions go to the server only and are never kept here. */
export async function approveStepup(request: StepupRequest, devices: StepupDevices = {}): Promise<StepupRequest> {
  const path = `/stepup-requests/${encodeURIComponent(request.id)}`
  const options = await stepupRequest<Options>(`${path}/options`, decision(request))
  if (options.method === 'oidc_reauth') {
    if (typeof options.authorize_url !== 'string') throw new Error('The fresh sign-in could not start. Nothing changed.')
    const target = navigable(options.authorize_url)
    ;(devices.navigate ?? (url => window.location.assign(url)))(target.href)
    throw new StepupNavigation()
  }
  if (options.method !== 'passkey' || typeof options.challenge_id !== 'string' || !options.publicKey) throw new Error('The passkey check could not start. Nothing changed.')
  let credential: PublicKeyCredential | null
  try {
    credential = await (devices.credentials ?? navigator.credentials).get({ publicKey: requestOptions(options.publicKey) }) as PublicKeyCredential | null
  } catch (cause) {
    if (cause instanceof DOMException && (cause.name === 'NotAllowedError' || cause.name === 'AbortError')) credential = null
    else throw cause
  }
  if (!credential) throw new Error('The passkey check was cancelled. Nothing changed. Approve to try again.')
  return confirmed(request, await stepupRequest<StepupRequest>(`${path}/approve`, { ...decision(request), challenge_id: options.challenge_id, credential: credentialJSON(credential) }))
}
export async function declineStepup(request: StepupRequest): Promise<StepupRequest> {
  return confirmed(request, await stepupRequest<StepupRequest>(`/stepup-requests/${encodeURIComponent(request.id)}/decline`, decision(request)))
}

const clock = (value?: string) => { const at = value ? Date.parse(value) : NaN; return Number.isFinite(at) ? new Intl.DateTimeFormat(undefined, { timeStyle: 'short' }).format(at) : 'an unknown time' }
export function methodLabel(request: Pick<StepupRequest, 'method' | 'auth_time'>): string {
  if (request.method === 'oidc_reauth') return `fresh sign-in at ${clock(request.auth_time)}`
  if (request.method === 'passkey_platform') return 'device passkey'
  return request.method === 'passkey' ? 'passkey' : 'method not recorded'
}
/** The Decided answer. It names who decided and, for approvals, the method. */
export function stepupAnswer(request: StepupRequest): string | undefined {
  const who = request.decided_by_name || 'a person'
  switch (request.state) {
    case 'pending': return undefined
    case 'applied': return `Approved · ${who} · ${methodLabel(request)}`
    case 'declined': return `Declined · ${who}`
    case 'withdrawn': return 'Withdrawn by the agent'
    case 'expired': return 'Expired without a decision'
    case 'stale': return 'Not applied · changed meanwhile'
    case 'failed': return 'Not applied · the change failed'
  }
}
export function stepupDelivery(request: StepupRequest): string | undefined {
  switch (request.state) {
    case 'applied': return `Applied at ${clock(request.applied_at ?? request.decided_at)}, once. Nothing else changed.`
    case 'declined': return 'Nothing changed.'
    case 'withdrawn': return 'The agent withdrew the request before a decision. Nothing changed.'
    case 'expired': return 'Expired after 15 minutes. Nothing changed.'
    case 'stale': return 'The target changed after the agent asked, so nothing was applied. The agent can ask again.'
    case 'failed': return 'The change could not be applied and was rolled back. Nothing changed.'
    default: return undefined
  }
}
/** A settled response that is not this person's own decision is never shown
 * as recorded: first decision wins, and expiry or withdrawal can come first. */
export function settledElsewhere(result: StepupRequest, personId: string): string | undefined {
  if (result.state === 'expired' || result.state === 'withdrawn') return `${stepupAnswer(result)}. Nothing changed.`
  if (result.decided_by && result.decided_by !== personId) return `Decided by ${result.decided_by_name || 'another person'} first: ${stepupAnswer(result)}. Nothing for you to do.`
  return undefined
}

const feature = (value: unknown) => value === true ? 'On' : value === false ? 'Off' : 'Inherited'
const shown = (value: unknown) => value === null || value === undefined ? 'None' : typeof value === 'string' ? value : JSON.stringify(value)
/** Before → After rows. Known targets get plain labels; others list fields. */
export function stepupRows(request: StepupRequest, scope: string): { before: StepupRow[]; after: StepupRow[] } {
  const { before, after } = request
  if (request.payload.kind === 'feature') {
    const changed = before.override !== after.override
    const rows = (side: Record<string, unknown>) => [
      { label: 'Feature', value: shown(side.key), changed: false },
      { label: 'Applies to', value: scope, changed: false },
      { label: 'Setting', value: feature(side.override), changed },
    ]
    return { before: rows(before), after: rows(after) }
  }
  const keys = [...new Set([...Object.keys(before), ...Object.keys(after)])].filter(key => key !== 'revision').sort()
  const rows = (side: Record<string, unknown>) => keys.map(key => ({ label: key, value: shown(side[key]), changed: JSON.stringify(before[key]) !== JSON.stringify(after[key]) }))
  return { before: rows(before), after: rows(after) }
}
export function stepupTitle(request: StepupRequest, scope: string): string {
  if (request.payload.kind === 'feature') {
    const key = shown(request.after.key ?? request.payload.key), where = scope === 'Workspace' ? 'the workspace' : scope
    return request.after.override === true ? `Turn on ${key} for ${where}?` : request.after.override === false ? `Turn off ${key} for ${where}?` : `Let ${key} inherit again for ${where}?`
  }
  return `Allow one protected change (${request.permission})?`
}
