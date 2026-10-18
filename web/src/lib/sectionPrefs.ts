// SPDX-License-Identifier: AGPL-3.0-only
// Which Agents page sections are open (AEON-781). The person's own preference
// on the server is the truth; localStorage only paints the first frame.
export const SECTIONS_KEY = 'ui.agents.sections'
/** The dial's fold before AEON-781; read only when the new preference has no dial entry. */
export const LEGACY_DIAL_KEY = 'agents.working.display'
export const SECTION_KEYS = ['dial', 'accounts', 'sessions', 'queued'] as const
export type SectionKey = typeof SECTION_KEYS[number]
export type SectionsOpen = Record<SectionKey, boolean>
/** Dial open on the first visit, Accounts folded, Sessions and Queued open. */
export const DEFAULT_SECTIONS: Readonly<SectionsOpen> = Object.freeze({ dial: true, accounts: false, sessions: true, queued: true })

/** Only known keys with boolean values count; anything else falls back to the default. */
export function readSections(value: unknown): Partial<SectionsOpen> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return {}
  const out: Partial<SectionsOpen> = {}
  for (const key of SECTION_KEYS) {
    const open = (value as Record<string, unknown>)[key]
    if (typeof open === 'boolean') out[key] = open
  }
  return out
}

/** The legacy dial preference stored folded: true/false; it maps to the dial's open state. */
export function readLegacyDial(value: unknown): Partial<SectionsOpen> {
  const folded = value && typeof value === 'object' ? (value as Record<string, unknown>).folded : undefined
  return typeof folded === 'boolean' ? { dial: !folded } : {}
}

export const withDefaults = (...layers: Partial<SectionsOpen>[]): SectionsOpen => Object.assign({}, DEFAULT_SECTIONS, ...layers)

export const cacheKey = (viewer: string) => `aeon:${SECTIONS_KEY}:${viewer}`
