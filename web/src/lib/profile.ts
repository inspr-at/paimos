// SPDX-License-Identifier: AGPL-3.0-only
// The signed-in person's profile (GET/PATCH /api/me/profile), their avatar
// (POST/DELETE /api/me/avatar) and the page-load greeting (GET /api/me/greeting).
import { api, sessionEnded } from './api.ts'
import { uploadError } from './avatar.ts'

export interface Profile {
  principal_id: string; email: string | null; first_name: string; last_name: string; preferred_name: string; short_name: string
  initials: string; timezone: string; locale: string; greeting_enabled: boolean; avatar_color: string
  avatar_hashes: Record<string, string>; week_start: number; revision: number
  /** The default pace (AEON-1175): percentage points a day, 1–50; null follows the built-in 10. */
  daily_points_per_day?: number | null
}
export type ProfileField = 'first_name' | 'last_name' | 'preferred_name' | 'short_name' | 'initials' | 'timezone' | 'locale' | 'greeting_enabled' | 'daily_points_per_day'
export type ProfilePatch = Partial<Pick<Profile, ProfileField>> & { initials?: string; /** Applies only while the profile is at this revision (AEON-1175). */ expected_revision?: number }

// A refused change: the server's messages keyed by field (400), or the taken short name (409).
export class ProfileError extends Error {
  readonly status: number
  readonly errors: Record<string, string>
  constructor(status: number, errors: Record<string, string>) { super(Object.values(errors)[0] ?? 'Your change could not be saved.'); this.status = status; this.errors = errors }
}

/** A guarded patch refused: the profile changed since the revision it carried (409, errors.revision). */
export const profileChanged = (error: unknown) => error instanceof ProfileError && error.status === 409 && !!error.errors.revision

export async function getProfile(): Promise<Profile> {
  const response = await api('/me/profile')
  if (!response.ok) throw new ProfileError(response.status, {})
  return response.json()
}
export async function patchProfile(fields: ProfilePatch): Promise<Profile> {
  const response = await api('/me/profile', { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(fields) })
  if (!response.ok) {
    const body = await response.json().catch(() => ({}))
    throw new ProfileError(response.status, body && typeof body.errors === 'object' ? body.errors : {})
  }
  return response.json()
}

// The server's validation messages, as people say them.
const FIELD_WORDS: Record<string, (message: string) => string> = {
  short_name: message => /already used/.test(message) ? 'Someone in this workspace already uses this handle.' : 'Use 2 to 24 lowercase letters, digits, dots, dashes or underscores.',
  initials: () => 'Use up to 3 characters.',
  timezone: () => 'Choose a time zone from the list.',
  locale: () => 'Choose a language from the list.',
  first_name: () => 'Use up to 100 characters, without spaces at either end.',
  last_name: () => 'Use up to 100 characters, without spaces at either end.',
  preferred_name: () => 'Use up to 100 characters, without spaces at either end.',
  daily_points_per_day: () => 'Use a whole number from 1 to 50.',
}
export const fieldMessage = (field: string, message: string) => FIELD_WORDS[field]?.(message) ?? message
export function shortNameProblem(value: string) {
  if (value === '') return ''
  if (/[A-Z]/.test(value)) return 'Use lowercase letters.'
  if (/[^a-z0-9._-]/.test(value)) return 'Only letters, digits, dots, dashes and underscores.'
  if (value.length < 2) return 'At least 2 characters.'
  if (value.length > 24) return 'At most 24 characters.'
  return ''
}

// ---------- Avatar ----------
// Uploads with progress (fetch has no upload progress), resolving to the updated profile.
export function uploadAvatar(file: Blob, crop: { x: number; y: number; size: number }, onProgress: (fraction: number) => void): { done: Promise<Profile>; abort: () => void } {
  const xhr = new XMLHttpRequest()
  if (sessionEnded.blocked) return { done: Promise.reject(new Error('Your session has ended.')), abort: () => {} }
  const done = new Promise<Profile>((resolve, reject) => {
    const form = new FormData()
    form.append('crop', JSON.stringify(crop))
    form.append('file', file)
    xhr.open('POST', '/api/me/avatar')
    xhr.withCredentials = true
    xhr.setRequestHeader('Accept', 'application/json')
    xhr.upload.onprogress = event => { if (event.lengthComputable) onProgress(event.loaded / event.total) }
    xhr.onload = () => {
      if (xhr.status === 401) { sessionEnded.blocked = true; sessionEnded.handler?.('/me/avatar') }
      let body: { error?: string } & Partial<Profile> = {}
      try { body = JSON.parse(xhr.responseText) } catch { body = {} }
      if (xhr.status >= 200 && xhr.status < 300) resolve(body as Profile)
      else reject(new Error(uploadError(xhr.status, typeof body.error === 'string' ? body.error : '')))
    }
    xhr.onerror = () => reject(new Error(uploadError(0, '')))
    xhr.onabort = () => reject(Object.assign(new Error('Upload cancelled.'), { aborted: true }))
    xhr.send(form)
  })
  return { done, abort: () => xhr.abort() }
}
export async function removeAvatar(): Promise<Profile> {
  const response = await api('/me/avatar', { method: 'DELETE' })
  if (!response.ok) throw new Error('Your photo could not be removed. Please try again.')
  return response.json()
}

// ---------- Greeting ----------
export interface Greeting { salutation: string; name: string; message: string; id: string }
// Drawn once per page load: each draw is remembered on the server so lines do not repeat.
let greeting: Promise<Greeting | null> | null = null
export function pageGreeting(): Promise<Greeting | null> {
  return greeting ??= (async () => {
    try {
      const response = await api('/me/greeting', { headers: { 'X-Timezone': Intl.DateTimeFormat().resolvedOptions().timeZone } })
      if (!response.ok) return null
      const body = await response.json()
      return typeof body?.salutation === 'string' && typeof body?.message === 'string' ? body as Greeting : null
    } catch { return null }
  })()
}
