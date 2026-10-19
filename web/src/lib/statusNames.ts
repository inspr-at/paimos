// SPDX-License-Identifier: AGPL-3.0-only
// A workspace's own words for its statuses (AEON-1401). Display only: stored
// states, transitions, checks, the API and --json keep the canonical values.
// Every screen reads the names through statusMeta (work.ts) or statusName; the
// status names store fills STATUS_NAMES for the signed-in workspace, so a live
// rename re-renders what people read without a reload. The rules mirror
// internal/statusnames: at most 24 characters, unique per locale ignoring case.
import { reactive } from 'vue'

export type StatusNameKey = 'new' | 'backlog' | 'open' | 'blocked' | 'in_progress' | 'qa' | 'done' | 'delivered' | 'accepted' | 'cancelled' | 'archived' | 'queued'
export type StatusLocale = 'en' | 'de'
export interface StatusName { en?: string; de?: string }
export type StatusNameMap = Partial<Record<StatusNameKey, StatusName>>
export interface StatusNames { revision: number; names: StatusNameMap }

export const STATUS_NAME_MAX = 24
export const STATUS_LOCALES: readonly StatusLocale[] = ['en', 'de']
// Workflow order, then Queued: lists show it like a status. Recurring is a marker, not renamable.
export const STATUS_NAME_KEYS: readonly StatusNameKey[] = ['new', 'backlog', 'open', 'blocked', 'in_progress', 'qa', 'done', 'delivered', 'accepted', 'cancelled', 'archived', 'queued']
export const BUILT_IN_STATUS_NAMES: Readonly<Record<StatusNameKey, Readonly<Record<StatusLocale, string>>>> = {
  new: { en: 'New', de: 'Neu' }, backlog: { en: 'Backlog', de: 'Backlog' }, open: { en: 'Open', de: 'Offen' },
  blocked: { en: 'Blocked', de: 'Blockiert' }, in_progress: { en: 'In progress', de: 'In Arbeit' }, qa: { en: 'QA', de: 'QA' },
  done: { en: 'Done', de: 'Erledigt' }, delivered: { en: 'Delivered', de: 'Ausgeliefert' }, accepted: { en: 'Accepted', de: 'Abgenommen' },
  cancelled: { en: 'Cancelled', de: 'Abgebrochen' }, archived: { en: 'Archived', de: 'Archiviert' }, queued: { en: 'Queued', de: 'Eingereiht' },
}

export const STATUS_NAMES = reactive<{ names: StatusNameMap }>({ names: {} })

// Known statuses with text names only: an unexpected answer reads as no renames.
function copy(names: unknown): StatusNameMap {
  const out: StatusNameMap = {}
  if (!names || typeof names !== 'object') return out
  for (const key of STATUS_NAME_KEYS) {
    const name = (names as Record<string, unknown>)[key] as Record<string, unknown> | undefined
    const en = typeof name?.en === 'string' ? name.en : '', de = typeof name?.de === 'string' ? name.de : ''
    if (en || de) out[key] = { ...(en ? { en } : {}), ...(de ? { de } : {}) }
  }
  return out
}
export function setStatusNames(names: unknown): void { STATUS_NAMES.names = copy(names) }
export function readStatusNames(value: unknown): StatusNames {
  const revision = (value as { revision?: unknown } | null)?.revision
  return { revision: typeof revision === 'number' && Number.isInteger(revision) && revision >= 0 ? revision : 0, names: copy((value as { names?: unknown } | null)?.names) }
}

// The live event a save writes. The tab's live stream (liveNodes.ts) reports it;
// the status names store reads the names again. Events carry no shown values.
export const STATUS_NAMES_EVENT = 'tenant.status_names_updated'
const changeListeners = new Set<() => void>()
export function onStatusNamesChanged(listener: () => void): () => void { changeListeners.add(listener); return () => { changeListeners.delete(listener) } }
export function statusNamesChanged(): void { for (const listener of [...changeListeners]) listener() }

// Imported data spells some states differently ("in-progress", "active", "canceled").
export function statusNameKey(state: string): StatusNameKey | null {
  const normal = state.trim().toLowerCase().replace(/[\s-]+/g, '_')
  const key = normal === 'active' ? 'in_progress' : normal === 'canceled' ? 'cancelled' : normal
  return (STATUS_NAME_KEYS as readonly string[]).includes(key) ? key as StatusNameKey : null
}
/** The workspace's own name in a locale, or '' when the status keeps its built-in name there. */
export function renamedStatus(key: StatusNameKey, locale: StatusLocale = 'en', names: StatusNameMap = STATUS_NAMES.names): string { return names[key]?.[locale] ?? '' }
/** What a person reads for a status: the workspace's name, else the built-in one. */
export function statusName(key: StatusNameKey, locale: StatusLocale = 'en', names: StatusNameMap = STATUS_NAMES.names): string {
  return renamedStatus(key, locale, names) || BUILT_IN_STATUS_NAMES[key][locale]
}

export type StatusNameDrafts = Partial<Record<StatusNameKey, Partial<Record<StatusLocale, string>>>>
export type StatusNameProblem = { code: 'too_long' | 'taken'; name: string }
export const statusNameField = (key: StatusNameKey, locale: StatusLocale) => `${key}.${locale}`

/** Trimmed names that differ from their built-in; empty ones read as the built-in. */
export function cleanStatusNames(drafts: StatusNameDrafts): StatusNameMap {
  const out: StatusNameMap = {}
  for (const key of STATUS_NAME_KEYS) for (const locale of STATUS_LOCALES) {
    const value = (drafts[key]?.[locale] ?? '').trim()
    if (value && value !== BUILT_IN_STATUS_NAMES[key][locale]) out[key] = { ...out[key], [locale]: value }
  }
  return out
}
/** Refused fields by `key.locale`: over 24 characters, or a name another status reads as in that locale.
 *  In a clash the field being edited carries the refusal; without one every renamed status in it does. */
export function statusNameProblems(drafts: StatusNameDrafts, editing = ''): Map<string, StatusNameProblem> {
  const problems = new Map<string, StatusNameProblem>(), names = cleanStatusNames(drafts)
  for (const locale of STATUS_LOCALES) {
    const owners = new Map<string, StatusNameKey[]>()
    for (const key of STATUS_NAME_KEYS) {
      const name = statusName(key, locale, names), fold = name.toLowerCase()
      owners.set(fold, [...(owners.get(fold) ?? []), key])
      if ([...name].length > STATUS_NAME_MAX) problems.set(statusNameField(key, locale), { code: 'too_long', name })
    }
    for (const keys of owners.values()) {
      if (keys.length < 2) continue
      const renamed = keys.filter(key => renamedStatus(key, locale, names)).map(key => statusNameField(key, locale))
      for (const field of renamed.includes(editing) ? [editing] : renamed) {
        if (!problems.has(field)) problems.set(field, { code: 'taken', name: statusName(field.split('.')[0] as StatusNameKey, locale, names) })
      }
    }
  }
  return problems
}
