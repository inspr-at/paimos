// SPDX-License-Identifier: AGPL-3.0-only
import { api, APIError } from './api.ts'
import type { Approval } from './agents.ts'

export interface PhoneReview {
  kind: 'approval'; request_id: string; request_hash: string; pending: boolean
  approval?: Approval
}
export interface PhoneDecision { decision: 'approved' | 'denied'; request_hash: string; reason: string }
type Descriptor = Omit<PublicKeyCredentialDescriptor, 'id'> & { id: string }
type RequestOptions = Omit<PublicKeyCredentialRequestOptions, 'challenge' | 'allowCredentials'> & { challenge: string; allowCredentials?: Descriptor[] }
export interface Ceremony<T> { challenge_id: string; publicKey: T }

export async function phoneRequest<T>(path: string, method = 'GET', body?: unknown, signal?: AbortSignal): Promise<T> {
  signal?.throwIfAborted()
  const response = await api(path, { method, signal, ...(body === undefined ? {} : {
    headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  }) })
  if (!response.ok) {
    const data = await response.json().catch(() => ({}))
    throw new APIError(response.status, typeof data.error === 'string' ? data.error : 'Phone approvals unavailable', data)
  }
  return response.status === 204 ? undefined as T : response.json()
}
// AEON-455 advertises availability through its existing settings read. A 404
// means the package is absent; other errors must not enable an unverified write.
export async function phoneVerificationAvailable(): Promise<boolean> { return (await phoneCapability()).available }
// AEON-1048: step-up Approve uses an existing passkey when the person has one,
// otherwise a fresh sign-in. The same read tells the desk which one to name.
export async function phoneCapability(): Promise<{ available: boolean; passkeys: number }> {
  try {
    const settings = await phoneRequest<{ available: boolean; passkeys?: unknown[] }>('/me/phone-approvals')
    return { available: settings.available === true, passkeys: Array.isArray(settings.passkeys) ? settings.passkeys.length : 0 }
  } catch (cause) { if (cause instanceof APIError && cause.status === 404) return { available: false, passkeys: 0 }; throw cause }
}
export const reviewPath = (kind: 'approval', id: string) => `/phone-approvals/${kind}/${encodeURIComponent(id)}`
export function decodeBytes(value: string): ArrayBuffer {
  const raw = atob(value.replace(/-/g, '+').replace(/_/g, '/'))
  return Uint8Array.from(raw, ch => ch.charCodeAt(0)).buffer
}
export function encodeBytes(value: ArrayBuffer): string {
  return btoa(String.fromCharCode(...new Uint8Array(value))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}
export const requestOptions = (options: RequestOptions): PublicKeyCredentialRequestOptions => ({
  ...options, challenge: decodeBytes(options.challenge), userVerification: 'required',
  allowCredentials: options.allowCredentials?.map(c => ({ ...c, id: decodeBytes(c.id) })),
})
export function credentialJSON(credential: PublicKeyCredential) {
  const response = credential.response
  const common = { id: credential.id, rawId: encodeBytes(credential.rawId), type: credential.type,
    authenticatorAttachment: credential.authenticatorAttachment, clientExtensionResults: credential.getClientExtensionResults() }
  const assertion = response as AuthenticatorAssertionResponse
  return { ...common, response: { clientDataJSON: encodeBytes(response.clientDataJSON),
    authenticatorData: encodeBytes(assertion.authenticatorData), signature: encodeBytes(assertion.signature),
    userHandle: assertion.userHandle ? encodeBytes(assertion.userHandle) : null } }
}
export async function decidePhone(review: PhoneReview, decision: PhoneDecision, signal?: AbortSignal) {
  const path = reviewPath(review.kind, review.request_id)
  const ceremony = await phoneRequest<Ceremony<RequestOptions>>(`${path}/options`, 'POST', decision, signal)
  signal?.throwIfAborted()
  const credential = await navigator.credentials.get({ publicKey: requestOptions(ceremony.publicKey), signal }) as PublicKeyCredential | null
  if (!credential) throw new Error('Verification cancelled. No decision was recorded.')
  return phoneRequest<PhoneReview>(`${path}/decision`, 'POST', { ...decision, challenge_id: ceremony.challenge_id, credential: credentialJSON(credential) }, signal)
}
