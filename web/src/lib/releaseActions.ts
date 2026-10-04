// SPDX-License-Identifier: AGPL-3.0-only
import { api, APIError } from './api'
import type { PlanningRelease } from './deliveryPlanning'

export type ReleaseAction = 'settings' | 'freeze' | 'unfreeze' | 'cut' | 'publish' | 'close' | 'abandon' | 'building' | 'planned' | 'notes' | 'top' | 'after' | 'build'
export interface ReleaseRights { person: boolean; read: boolean; write: boolean; deploy: boolean; product: boolean }
export type ReleaseRecord = PlanningRelease & { body?: string; cut_at?: string | null; reservation_basis?: string; reservation_ref?: string; released_by?: string }
export function actionReason(action: ReleaseAction, release: ReleaseRecord, rights: ReleaseRights): string {
  const terminal = ['released', 'abandoned'].includes(release.state)
  if (action === 'build') return 'Build settings and Start arrive with the build work.'
  if (action === 'notes') return rights.read ? '' : 'Release read permission is required.'
  if (terminal) return 'This release is finished; its history stays intact.'
  if (['settings', 'top', 'after', 'abandon', 'building', 'planned'].includes(action) && !rights.person) return 'Only a person can do this.'
  if (['top', 'after'].includes(action) && release.visibility === 'published') return 'Published releases keep their order.'
  const edit = ['settings', 'top', 'after', 'building', 'planned'].includes(action) || action === 'abandon' && release.state === 'planned'
  if (edit ? !rights.write : !rights.deploy) return edit ? 'Release edit permission is required.' : 'Release deployment permission is required.'
  if (action === 'settings') return ''
  if (action === 'freeze' && !['planned', 'building'].includes(release.state)) return 'Only a planned or building release can freeze.'
  if (['unfreeze', 'cut', 'publish', 'close'].includes(action) && release.state !== 'frozen') return 'Freeze this release first.'
  if (['unfreeze', 'cut'].includes(action) && (release.cut_at || release.version)) return 'Already cut. Only Publish or Abandon can follow.'
  if (['cut', 'publish'].includes(action) && release.visibility !== 'published') return 'Internal work has no version or publication.'
  if (action === 'close' && release.visibility !== 'internal') return 'Published releases use Cut and Publish.'
  if (action === 'publish' && !(release.cut_at || release.version)) return 'Cut this release first.'
  if (action === 'publish' && !rights.product && !rights.person) return 'A person must attest this project’s reservation.'
  if (action === 'building' && release.state !== 'planned' || action === 'planned' && release.state !== 'building') return 'This lifecycle change is unavailable.'
  return ''
}
export const lifecycleCopy: Partial<Record<ReleaseAction, string>> = {
  freeze: 'Once frozen, nobody can add work. Freezing clears build authorization.',
  unfreeze: 'Return to building with no spending authorization. A person must use Start before a build can run.',
  building: 'Change the lifecycle to building. This starts no build and grants no spending authorization.',
  planned: 'Return to planned and clear any build authorization.',
  cut: 'Reserve one version permanently. Unfinished work moves to the next open published release of the same project. A closed entry deadline there needs a person with edit rights. Cut cannot be undone or repeated with another version.',
  publish: 'Store the cumulative release notes and publish. The product’s exact reservation is checked against this build’s history; other projects require a person’s attestation.',
  close: 'Keep finished work here and move unfinished work to the next open internal release. If nothing is finished, nothing changes. No version, tag or announcement is made.',
  abandon: 'Move finished and unfinished work to the next open release of the same kind. Cancelled work goes to Backlog. The name and any cut version stay reserved.',
}
export type ReleaseRequest = (path: string, init?: RequestInit) => Promise<unknown>
export const requestRelease: ReleaseRequest = async (path, init = {}) => {
  const response = await api(path, { ...init, signal: init.signal ?? AbortSignal.timeout(60_000) }, 60_000)
  const result = await response.json().catch(() => { throw new Error('The server returned an unreadable release response.') })
  if (!response.ok) throw new APIError(response.status, result.error || result.message || `Release request failed (${response.status})`, result)
  return result
}
export const releasePath = (project: string, release?: string) => `/projects/${encodeURIComponent(project)}/releases${release ? `/${encodeURIComponent(release)}` : ''}`
export function localDeadline(value?: string | null) {
  if (!value) return ''
  const date = new Date(value)
  return new Date(date.getTime() - date.getTimezoneOffset() * 60_000).toISOString().slice(0, 16)
}
export function deadlineValue(value: string) {
  if (!value) return null
  const date = new Date(value)
  if (!Number.isFinite(date.getTime())) throw new Error('Choose a valid entry deadline.')
  return date.toISOString()
}
