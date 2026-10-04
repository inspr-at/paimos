// SPDX-License-Identifier: AGPL-3.0-only
import { api, APIError } from './api.ts'
import type { Approval } from './agents.ts'
import type { AttachReview } from './attachWatch.ts'

export interface PhonePreferences {
  enabled: boolean; time_zone: string; quiet_start: number; quiet_end: number; escalation_minutes: number
}
export interface PhoneItem { id: string; created_at: string }
export interface PhoneSettings {
  available: boolean; push_available: boolean; vapid_public_key: string
  preferences: PhonePreferences; passkeys: PhoneItem[]; subscriptions: PhoneItem[]
}
export type PhoneKind = 'approval' | 'attach'
export interface PhoneReview {
  kind: PhoneKind; request_id: string; request_hash: string; pending: boolean
  approval?: Approval; attach?: AttachReview
}
export interface PhoneDecision { decision: 'approved' | 'denied'; request_hash: string; reason: string }
type Descriptor = Omit<PublicKeyCredentialDescriptor, 'id'> & { id: string }
type RequestOptions = Omit<PublicKeyCredentialRequestOptions, 'challenge' | 'allowCredentials'> & { challenge: string; allowCredentials?: Descriptor[] }
type CreationOptions = Omit<PublicKeyCredentialCreationOptions, 'challenge' | 'user' | 'excludeCredentials'> & {
  challenge: string; user: Omit<PublicKeyCredentialUserEntity, 'id'> & { id: string }; excludeCredentials?: Descriptor[]
}
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
export const phoneSettings = (signal?: AbortSignal) => phoneRequest<PhoneSettings>('/me/phone-approvals', 'GET', undefined, signal)
export const reviewPath = (kind: PhoneKind, id: string) => `/phone-approvals/${kind}/${encodeURIComponent(id)}`
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
export const creationOptions = (options: CreationOptions): PublicKeyCredentialCreationOptions => ({
  ...options, challenge: decodeBytes(options.challenge), user: { ...options.user, id: decodeBytes(options.user.id) },
  excludeCredentials: options.excludeCredentials?.map(c => ({ ...c, id: decodeBytes(c.id) })),
  authenticatorSelection: { ...options.authenticatorSelection, authenticatorAttachment: 'platform', userVerification: 'required' },
})
export function credentialJSON(credential: PublicKeyCredential) {
  const response = credential.response
  const common = { id: credential.id, rawId: encodeBytes(credential.rawId), type: credential.type,
    authenticatorAttachment: credential.authenticatorAttachment, clientExtensionResults: credential.getClientExtensionResults() }
  if ('attestationObject' in response) {
    const registration = response as AuthenticatorAttestationResponse
    return { ...common, response: { clientDataJSON: encodeBytes(response.clientDataJSON),
      attestationObject: encodeBytes(registration.attestationObject), transports: registration.getTransports() } }
  }
  const assertion = response as AuthenticatorAssertionResponse
  return { ...common, response: { clientDataJSON: encodeBytes(response.clientDataJSON),
    authenticatorData: encodeBytes(assertion.authenticatorData), signature: encodeBytes(assertion.signature),
    userHandle: assertion.userHandle ? encodeBytes(assertion.userHandle) : null } }
}
export async function addPhonePasskey(signal?: AbortSignal) {
  const ceremony = await phoneRequest<Ceremony<CreationOptions>>('/me/phone-approvals/passkeys/options', 'POST', undefined, signal)
  signal?.throwIfAborted()
  const credential = await navigator.credentials.create({ publicKey: creationOptions(ceremony.publicKey), signal }) as PublicKeyCredential | null
  if (!credential) throw new Error('Verification cancelled. No passkey was added.')
  return phoneRequest<PhoneItem>('/me/phone-approvals/passkeys', 'POST', { challenge_id: ceremony.challenge_id, credential: credentialJSON(credential) }, signal)
}
export async function decidePhone(review: PhoneReview, decision: PhoneDecision, signal?: AbortSignal) {
  const path = reviewPath(review.kind, review.request_id)
  const ceremony = await phoneRequest<Ceremony<RequestOptions>>(`${path}/options`, 'POST', decision, signal)
  signal?.throwIfAborted()
  const credential = await navigator.credentials.get({ publicKey: requestOptions(ceremony.publicKey), signal }) as PublicKeyCredential | null
  if (!credential) throw new Error('Verification cancelled. No decision was recorded.')
  return phoneRequest<PhoneReview>(`${path}/decision`, 'POST', { ...decision, challenge_id: ceremony.challenge_id, credential: credentialJSON(credential) }, signal)
}
export const phoneError = (error: unknown) => error instanceof DOMException && error.name === 'NotAllowedError'
  ? 'Verification cancelled. No decision was recorded.' : error instanceof Error ? error.message : 'Phone approvals unavailable. Please try again.'
export const minuteTime = (minute: number) => `${String(Math.floor(minute / 60)).padStart(2, '0')}:${String(minute % 60).padStart(2, '0')}`
export const timeMinute = (time: string) => { const [hour, minute] = time.split(':').map(Number); return hour * 60 + minute }
