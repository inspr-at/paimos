// SPDX-License-Identifier: AGPL-3.0-only
import { ref, type Ref } from 'vue'

// The quote editor can fold the app header away to give the page more room (its
// chevron beside PDF). Only pages that say so in their route (meta.foldHeader)
// honour it, so the header is always back everywhere else.
export const headerFolded = ref(false)

// AEON-1459 (design AEON-1279 r2, D1): one compact rule for every part of the app's chrome.
// Phones in portrait (under 720 px wide) and short screens (500 px high or less, a phone turned
// or a short desktop window) get the 44 px header, the one-line list toolbars and no footer bar.
// CSS media queries cannot read a variable: the stylesheets spell these three out the same way.
export const COMPACT_QUERY = '(max-width: 719px), (max-height: 500px)'
// Short landscape: the header scrolls away with the page and only the list's toolbar sticks (D7).
export const SHORT_QUERY = '(max-height: 500px) and (min-width: 720px)'
// The narrowest phones (320): Settings moves into the avatar menu (R2.1).
export const NARROWEST_QUERY = '(max-width: 359px)'

const watched = new Map<string, Ref<boolean>>()
// One shared, live answer per query; it follows rotations and window resizes.
function media(query: string): Ref<boolean> {
  let value = watched.get(query)
  if (value) return value
  const list = typeof window !== 'undefined' && typeof window.matchMedia === 'function' ? window.matchMedia(query) : null
  value = ref(!!list?.matches)
  const target = value
  list?.addEventListener('change', event => { target.value = event.matches })
  watched.set(query, value)
  return value
}
export const compactChrome = () => media(COMPACT_QUERY)
// Under 380 px the toolbar's status slot steps aside (D3).
export const statusSlotAside = () => media('(max-width: 379px)')
// How many list toolbars show the footer's summary in their status slot right now.
export const statusSlots = ref(0)
export const shortScreen = () => media(SHORT_QUERY)
export const narrowestPhone = () => media(NARROWEST_QUERY)
