// SPDX-License-Identifier: AGPL-3.0-only
import type { ActiveTheme, ThemeValues } from './themes.ts'
import { installAgentTheme } from './agentTheme.ts'
import { indicatorRing } from './indicatorVariants.ts'
import { themeCss } from './themeEngine.ts'

export const THEME_CACHE_KEY = 'aeon.theme.v1'
let person: string | null = null
function cachedPerson(): string | null {
  try {
    const raw = localStorage.getItem(THEME_CACHE_KEY)
    if (!raw || raw.length > 32768) return null
    const value: unknown = JSON.parse(raw)
    return value && typeof value === 'object' && 'principal' in value && typeof value.principal === 'string' ? value.principal : null
  } catch { return null }
}
/** Bind only after session authentication. A first load keeps this person's bootstrap CSS. */
export function resetTheme(identity = '') {
  const keepBoot = person === null && !!identity && cachedPerson() === identity
  person = identity
  if (typeof document !== 'undefined' && document.documentElement) {
    delete document.documentElement.dataset.agentRing
    delete document.documentElement.dataset.agentFloat
  }
  if (keepBoot) return
  if (typeof document !== 'undefined') document.getElementById('aeon-theme')?.remove()
  try { localStorage.removeItem(THEME_CACHE_KEY) } catch { /* storage may be disabled */ }
}
/** The only runtime stylesheet writer; modes are selected entirely by CSS. */
export function applyTheme(values: ThemeValues) {
  const css = themeCss(values)
  if (typeof document !== 'undefined') {
    let style = document.getElementById('aeon-theme') as HTMLStyleElement | null
    if (!style) {
      style = document.createElement('style')
      style.id = 'aeon-theme'
      style.nonce = (document.getElementById('aeon-theme-boot') as HTMLScriptElement | null)?.nonce ?? ''
    }
    style.textContent = css
    // Vite dev injects token/base styles; production bundles them into a link.
    // Moving the same element after them keeps both cascades deterministic.
    document.head.append(style)
  }
  if (typeof document !== 'undefined' && document.documentElement) {
    document.documentElement.dataset.agentRing = indicatorRing(values.agents.avatar, values.agents.ring ?? undefined)
    document.documentElement.dataset.agentFloat = String(values.agents.hover)
  }
  installAgentTheme(values.agents)
  if (person) {
    try { localStorage.setItem(THEME_CACHE_KEY, JSON.stringify({ principal: person, css })) } catch { /* storage may be disabled */ }
  }
}
/** Answers for another identity never reach CSS, its cache, or agent appearance. */
export function publishTheme(identity: string, active: ActiveTheme): boolean {
  if (!identity || identity !== person) return false
  applyTheme(active.theme.values)
  return true
}
