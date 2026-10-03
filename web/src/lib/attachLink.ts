// SPDX-License-Identifier: AGPL-3.0-only

// The link `aeon-agentd attach` prints is /agents#attach=<nine digits>. The code
// rides in the fragment, which is never sent to a server or a referrer; only nine
// digits are accepted, so nothing else can be injected into the lookup field.
export function attachCodeFromHash(hash: string): string | null {
  return /^#attach=(\d{9})$/.exec(hash)?.[1] ?? null
}
export const formatAttachCode = (code: string) => `${code.slice(0, 3)} ${code.slice(3, 6)} ${code.slice(6)}`
export const hasAttachFragment = (hash: string) => hash.startsWith('#attach=')

// The one place a path loses an attach fragment. A return path is sent to the
// server when the sign-in page reloads, so the code must never be part of one.
export const stripAttachCode = (path: string) => path.replace(/#attach=[^]*$/, '')

// The router takes the code off the address bar before anything else can copy it
// (a sign-in return, a feedback path, an error report) and leaves it here, in
// memory only, until the page that opens the review takes it. A code nobody takes
// before a sign-in redirect is dropped: it never outlives the session it was for.
// It also stays with the person it arrived for: a code held while one person was
// signed in is never offered to another. One that arrived before anybody was signed
// in (a fresh tab) goes to whoever that tab signs in.
// The page hears about it once the address is clean and the navigation is over, so
// the session refresh that navigation starts cannot close the review it opens.
let held: { code: string; owner: string } | null = null
const listeners = new Set<() => void>()
export function holdAttachCode(code: string, owner: string): void { held = { code, owner } }
export function announceAttachCode(): void { if (held) for (const listener of [...listeners]) listener() }
export function takeAttachCode(owner: string): string | null {
  const taken = held
  held = null
  return taken && owner && (!taken.owner || taken.owner === owner) ? taken.code : null
}
export function dropAttachCode(): void { held = null }
export function onAttachCode(listener: () => void): () => void { listeners.add(listener); return () => listeners.delete(listener) }
