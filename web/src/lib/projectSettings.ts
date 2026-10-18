// SPDX-License-Identifier: AGPL-3.0-only
import { inject, type InjectionKey } from 'vue'
import type { IconName } from '../components/AppIcon.vue'

export const PROJECT_SETTINGS_SECTIONS = [
  { id: 'general', label: 'General', summary: 'Name, key, lifecycle', icon: 'folder' },
  { id: 'delivery', label: 'Repository & delivery', summary: 'Repository, checks, deadlines', icon: 'git-branch' },
  { id: 'definitions', label: 'Definitions', summary: 'Ready, QA, Done', icon: 'circle-check' },
  { id: 'agents', label: 'Agents', summary: 'Lead, work context, review', icon: 'agent' },
  { id: 'access', label: 'Access', summary: 'People and agents', icon: 'users' },
  { id: 'routines', label: 'Routines', summary: 'Recurring work', icon: 'repeat' },
] as const satisfies readonly { id: string; label: string; summary: string; icon: IconName }[]
export type ProjectSettingsSection = typeof PROJECT_SETTINGS_SECTIONS[number]['id']
export function projectSettingsSection(value: unknown): ProjectSettingsSection {
  return PROJECT_SETTINGS_SECTIONS.find(section => section.id === value)?.id ?? 'general'
}
export function projectSettingsPath(key: string, section: ProjectSettingsSection = 'general'): string {
  return `/p/${encodeURIComponent(key)}/settings${section === 'general' ? '' : `/${section}`}`
}
// German letters are spelt out first (ä → AE, ß → SS), so "für" stays one word
// and "Vorbestell-App für die Bäckerei" suggests VAFDB, not VAFRDB.
const SPELT: Record<string, string> = { ä: 'ae', ö: 'oe', ü: 'ue', Ä: 'Ae', Ö: 'Oe', Ü: 'Ue', ß: 'ss', ẞ: 'SS' }
export function suggestProjectKey(name: string): string {
  const words = name.replace(/[äöüÄÖÜßẞ]/g, letter => SPELT[letter]!).toUpperCase().replace(/[^A-Z0-9 ]+/g, ' ').trim().split(/\s+/).filter(Boolean)
  if (!words.length) return ''
  if (words.length >= 3) return words.map(word => word[0]).join('').slice(0, 6)
  return words[0]!.length <= 6 ? words[0]! : words[0]!.slice(0, 3)
}
export function projectKeyProblem(key: string): string {
  if (!key) return 'Enter a key.'
  return /^[A-Z][A-Z0-9]{1,9}$/.test(key) ? '' : '2 to 10 letters or digits, starting with a letter.'
}
// Preserve the hash and query: the existing cards still own routine selection and consent.
export function legacyProjectSettingsSection(hash: string, query: Record<string, unknown>): ProjectSettingsSection | null {
  if (hash === '#project-work-context') return 'agents'
  if (hash === '#recurring-work' || typeof query.recurrence === 'string' || query.consent === '1') return 'routines'
  if (hash === '#lifecycle' || hash === '#f-name') return 'general'
  if (['#repository', '#checks', '#advanced'].includes(hash)) return 'delivery'
  if (hash === '#definitions') return 'definitions'
  if (['#lead', '#review'].includes(hash)) return 'agents'
  if (hash === '#members') return 'access'
  return null
}

export interface ProjectSettingsOverlay {
  element: () => HTMLElement | undefined
  close: () => void
  submit?: () => void
}
export const PROJECT_SETTINGS_OVERLAY: InjectionKey<(overlay: ProjectSettingsOverlay) => () => void> = Symbol('project settings overlay')
export function useProjectSettingsOverlay() {
  return inject(PROJECT_SETTINGS_OVERLAY, () => () => {})
}
