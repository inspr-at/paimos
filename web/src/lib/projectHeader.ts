// SPDX-License-Identifier: AGPL-3.0-only
export type HeaderDensity = 'comfortable' | 'compact' | 'collapsed'
export type RoomyHeaderDensity = Exclude<HeaderDensity, 'collapsed'>
export interface HeaderPreference { density: HeaderDensity; roomy: RoomyHeaderDensity }
export const DEFAULT_HEADER: HeaderPreference = { density: 'compact', roomy: 'compact' }

export function headerStorageKey(tenant: string, person: string) {
  return `aeon:project-header:${encodeURIComponent(tenant)}:${encodeURIComponent(person)}`
}
export function readHeaderPreference(raw: string | null): HeaderPreference {
  if (raw && raw.length > 512) return { ...DEFAULT_HEADER }
  try {
    const saved: unknown = raw ? JSON.parse(raw) : null
    if (!saved || typeof saved !== 'object') return { ...DEFAULT_HEADER }
    const { density, roomy } = saved as Partial<HeaderPreference>
    if (!['comfortable', 'compact', 'collapsed'].includes(density ?? '')) return { ...DEFAULT_HEADER }
    return { density: density!, roomy: density === 'comfortable' || density === 'compact' ? density : roomy === 'comfortable' ? 'comfortable' : 'compact' }
  } catch { return { ...DEFAULT_HEADER } }
}
export function chooseHeader(current: HeaderPreference, density: HeaderDensity): HeaderPreference {
  return { density, roomy: density === 'collapsed' ? current.roomy : density }
}
export function toggleHeader(current: HeaderPreference): HeaderPreference {
  return chooseHeader(current, current.density === 'collapsed' ? current.roomy : 'collapsed')
}
export function headerShortcut(event: Pick<KeyboardEvent, 'code' | 'metaKey' | 'ctrlKey' | 'shiftKey' | 'altKey' | 'repeat' | 'isComposing' | 'defaultPrevented'>, mac: boolean) {
  return !event.defaultPrevented && !event.repeat && !event.isComposing && !event.altKey && event.shiftKey && event.code === 'Period'
    && (mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey)
}
