// SPDX-License-Identifier: AGPL-3.0-only
import { api, APIError } from './api.ts'

export interface AccountLinkReview {
  request_id: string; tenant_id: string; tenant_name: string; account_id: string
  harness: string; account_label: string; computer_name: string
  person_id: string; person_name: string; revision: number
  state: 'pending' | 'linked'; expires_at: string; request_digest: string
}
export const accountLinkCode = (value: string) => value.replace(/[ -]/g, '')
export const validAccountLinkCode = (value: string) => /^\d{6}$/.test(accountLinkCode(value))

async function request(path: string, method: string, body?: unknown, signal?: AbortSignal) {
  const response = await api(`/agent-pairing${path}`, {
    method, signal, ...(body === undefined ? {} : { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }),
  })
  if (!response.ok) {
    const messages: Record<number, string> = {
      401: 'Sign in again, then enter the code.', 403: 'Only a signed-in person can link their own account.',
      404: 'No account uses that code in this workspace.', 409: 'The account or session changed. Enter the code again.',
      410: 'This code expired or was used. Request a fresh code in the agent window.',
      429: 'Too many attempts. Try again in ten minutes.',
    }
    throw new APIError(response.status, messages[response.status] ?? 'Account linking is unavailable. Try again.')
  }
  return response.status === 204 ? undefined : response.json() as Promise<unknown>
}
function parse(value: unknown): AccountLinkReview {
  const row = value as Partial<AccountLinkReview> | null
  const fields = ['request_id', 'tenant_id', 'tenant_name', 'account_id', 'harness', 'account_label', 'computer_name', 'person_id', 'person_name', 'expires_at', 'request_digest'] as const
  if (!row || fields.some(key => typeof row[key] !== 'string' || !row[key] || row[key]!.length > 512) || !Number.isSafeInteger(row.revision) || (row.revision ?? -1) < 0 || !['pending', 'linked'].includes(row.state ?? '') || !Number.isFinite(Date.parse(row.expires_at ?? '')) || !/^[a-f0-9]{64}$/.test(row.request_digest ?? '')) throw new Error('The account review is incomplete. Enter the code again.')
  return row as AccountLinkReview
}
export async function lookupAccountLink(code: string, signal?: AbortSignal) {
  if (!validAccountLinkCode(code)) throw new Error('Enter the six-digit code from the agent window.')
  return parse(await request('/account-link/lookup', 'POST', { user_code: accountLinkCode(code) }, signal))
}
export async function approveAccountLink(review: AccountLinkReview, code: string, signal?: AbortSignal) {
  if (!validAccountLinkCode(code)) throw new Error('Enter the six-digit code from the agent window.')
  return parse(await request(`/account-link/${encodeURIComponent(review.request_id)}/approve`, 'POST', {
    tenant_id: review.tenant_id, person_id: review.person_id, expected_revision: review.revision, request_digest: review.request_digest,
    user_code: accountLinkCode(code),
  }, signal))
}
export async function listAccountLinks(signal?: AbortSignal): Promise<AccountLinkReview[]> {
  const data = await request('/account-links', 'GET', undefined, signal)
  if (!Array.isArray(data)) throw new Error('Linked accounts are unavailable.')
  return data.map(parse)
}
export const unlinkAccount = (review: AccountLinkReview, signal?: AbortSignal) => request(`/account-links/${encodeURIComponent(review.account_id)}/unlink`, 'POST', { expected_revision: review.revision }, signal)
