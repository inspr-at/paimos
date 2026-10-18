// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1036: what the dial remembers per person and across devices: the info
// area, the harness whose usage is on the right, and which of its folds are
// open. The person's preference on the server is the truth; localStorage only
// paints the first frame.
export const DIAL_KEY = 'ui.agents.dial'
export const FOLDS = ['pace', 'boost'] as const
export type DialFold = typeof FOLDS[number]
export interface DialPrefs { info_open: boolean; selected: string | null; folds: Record<string, boolean> }
export const DEFAULT_DIAL: Readonly<DialPrefs> = Object.freeze({ info_open: false, selected: null, folds: Object.freeze({}) as Record<string, boolean> })
const HARNESS = /^[a-z][a-z0-9_-]{0,31}$/
const FOLD_KEY = /^[a-z][a-z0-9_-]{0,31}:(pace|boost)$/
/** A fold of one harness, e.g. "claude:pace". */
export const foldKey = (harness: string, fold: DialFold) => `${harness}:${fold}`

/** Only known keys with the right type count; anything else falls back to the default. */
export function readDialPrefs(value: unknown): Partial<DialPrefs> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return {}
  const raw = value as Record<string, unknown>, out: Partial<DialPrefs> = {}
  if (typeof raw.info_open === 'boolean') out.info_open = raw.info_open
  if (raw.selected === null || typeof raw.selected === 'string' && HARNESS.test(raw.selected)) out.selected = raw.selected
  if (raw.folds && typeof raw.folds === 'object' && !Array.isArray(raw.folds)) {
    const folds: Record<string, boolean> = {}
    for (const [key, open] of Object.entries(raw.folds as Record<string, unknown>).slice(0, 64)) if (FOLD_KEY.test(key) && typeof open === 'boolean') folds[key] = open
    out.folds = folds
  }
  return out
}
export const withDialDefaults = (...layers: Partial<DialPrefs>[]): DialPrefs => ({ ...DEFAULT_DIAL, folds: {}, ...Object.assign({}, ...layers) })
export const dialCacheKey = (viewer: string) => `aeon:${DIAL_KEY}:${viewer}`

/**
 * The harness shown on the right: the person's last pick, else one over pace or
 * at its limit, else the first that has a daily limit, else the first row. A
 * pick that is no longer a row is ignored, never shown.
 */
export function selectedHarness(rows: string[], picked: string | null, attention: (harness: string) => boolean, limited: (harness: string) => boolean): string | null {
  if (picked && rows.includes(picked)) return picked
  return rows.find(attention) ?? rows.find(limited) ?? rows[0] ?? null
}
