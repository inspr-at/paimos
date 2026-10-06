// SPDX-License-Identifier: AGPL-3.0-only
// The three places of the header, in order of use, and the g-sequences that go
// there (g p, g a, g b). Free of Vue for unit tests.

export type PlaceId = 'projects' | 'agents' | 'business'
export interface Place { id: PlaceId; label: string; to: string; key: 'p' | 'a' | 'b' }
export const PLACES: readonly Place[] = [
  { id: 'projects', label: 'Projects', to: '/', key: 'p' },
  { id: 'agents', label: 'Agents', to: '/agents', key: 'a' },
  { id: 'business', label: 'Business', to: '/business', key: 'b' },
]

// Which place a page belongs to; settings and the like belong to none.
export function placeOf(path: string): PlaceId | null {
  if (path === '/' || path.startsWith('/p/') || path === '/knowledge' || path === '/releases' || path.startsWith('/releases/')) return 'projects'
  if (path === '/agents' || path.startsWith('/agents/')) return 'agents'
  if (path === '/business' || path.startsWith('/business/')) return 'business'
  return null
}

// A place shows only with its module and permission: Projects for everyone signed
// in, Agents for everyone signed in, Business while one of its parts is open.
export function visiblePlaces(access: { signedIn: boolean; business: boolean }): Place[] {
  if (!access.signedIn) return []
  return PLACES.filter(place => place.id !== 'business' || access.business)
}

// "g" then a place key within the window goes to that place.
export const SEQUENCE_MS = 1500
export function sequence(window = SEQUENCE_MS) {
  let armed: number | null = null
  return (key: string, now: number, places: readonly Place[]): Place | 'armed' | null => {
    const lower = key.toLowerCase()
    if (armed !== null && now - armed <= window) {
      armed = null
      return places.find(place => place.key === lower) ?? null
    }
    armed = lower === 'g' ? now : null
    return armed === null ? null : 'armed'
  }
}

// The armed chord may finish through the release menu g just opened, and only
// while that menu is the only layer and the arm is still inside the window.
// Any other menu or dialog, or an expired arm, does not.
export function releaseChordOpen(labels: readonly string[], armedAt: number | null, now: number, window = SEQUENCE_MS): boolean {
  return armedAt !== null && now - armedAt <= window && labels.length === 1 && labels[0].startsWith('Release for ')
}
