// SPDX-License-Identifier: AGPL-3.0-only
import type { PrefProfile } from './modelPrefs'
import type { BoardLock } from './modelsBoard'

export interface ModelResolution {
  profile: PrefProfile | null; role: string; owner_required: boolean
  trace: { kind?: string; column?: string; situation?: string; card_index?: number; preference_of?: { person: string | null; source: string }; lock?: BoardLock; held?: { line: string; reason: string }[]; blocked?: string; fallback?: string; person_id?: string | null; set_by?: string; residency?: { value: string } }
}
export interface BoardCoverage { consumers: { consumer: string; reads_board: boolean; agreement: string }[] }
export interface BoardEvidence {
  items: { id: string; kind: string; for_person: string | null; preference: ModelResolution['trace']; at: string; source: string; actual_profile_id?: string | null; agreement?: 'matches' | 'differs' | 'unplanned' | 'pending' }[]
  next_cursor: string | null
}
export function preferenceFailure(status: number, german = false) {
  if (status === 409) return german ? 'Andernorts geändert. Die Änderung wurde nicht gespeichert. Neu laden und erneut versuchen.' : 'Changed elsewhere. The change was not saved. Reload and try again.'
  if (status === 428) return german ? 'Die Person konnte nicht bestätigt werden. Die Änderung wurde nicht gespeichert. Neu laden und erneut versuchen.' : 'Your identity could not be confirmed. The change was not saved. Reload and try again.'
  if (status === 403) return german ? 'Dafür fehlt die Berechtigung. Die Änderung wurde nicht gespeichert.' : 'You do not have permission to do this. The change was not saved.'
  return german ? 'Konnte nicht gespeichert werden. Erneut versuchen.' : 'Could not save. Try again.'
}
export function modelsSettingsLink(context: { project?: { id: string }; level?: string; kind?: string; ticket?: string; why?: boolean } = {}) {
  const query = new URLSearchParams()
  if (context.project) query.set('project_id', context.project.id)
  query.set('layer', context.level === 'project' ? 'rules' : context.level === 'default' ? 'default' : 'mine')
  if (context.kind) query.set('kind', context.kind)
  if (context.ticket) query.set('ticket', context.ticket)
  if (context.why) query.set('why', '1')
  return `/settings/models?${query}`
}
export function resolutionLabel(resolution: ModelResolution | null, german: boolean) {
  const profile = resolution?.profile
  if (!profile) return german ? 'wartet' : 'waits'
  return [profile.display_name || profile.model, profile.model_version, profile.effort].filter(Boolean).join(' · ')
}
