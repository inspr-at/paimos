/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 *
 * PAI-861 — nullable, principal/project-scoped Paimos 6 selection.
 *
 * Selection is deliberately independent of attention and visual order. The
 * only inputs that can change it are an explicit user choice, an authorized
 * deep link, project-scoped session memory, or loss of authorization.
 */

export interface Paimos6SelectionStorage {
  getItem(key: string): string | null
  setItem(key: string, value: string): void
  removeItem(key: string): void
}

export interface Paimos6SelectionScope {
  principalId: number
  projectId: number
}

export interface ResolvePaimos6SelectionInput {
  scope: Paimos6SelectionScope
  authorizedIds: readonly string[]
  /** A query value is preferred when present. Null means no deep link. */
  deepLinkedId: string | null
  currentId: string | null
  storage?: Paimos6SelectionStorage | null
}

export interface ResolvedPaimos6Selection {
  id: string | null
  source: 'current' | 'deep-link' | 'stored' | 'none'
  /** The URL carried an id that is not in this authorized projection. */
  clearInvalidDeepLink: boolean
}

export function paimos6SelectionStorageKey(scope: Paimos6SelectionScope): string {
  return `paimos6:session-home:${scope.principalId}:project:${scope.projectId}:selected-session`
}

function readStoredSelection(
  storage: Paimos6SelectionStorage | null | undefined,
  key: string,
): string | null {
  if (!storage) return null
  try {
    const value = storage.getItem(key)
    return value && value.trim() !== '' ? value : null
  } catch {
    return null
  }
}

export function persistPaimos6Selection(
  storage: Paimos6SelectionStorage | null | undefined,
  scope: Paimos6SelectionScope,
  id: string | null,
): void {
  if (!storage) return
  const key = paimos6SelectionStorageKey(scope)
  try {
    if (id === null) storage.removeItem(key)
    else storage.setItem(key, id)
  } catch {
    // sessionStorage may be disabled. In-memory and URL state still work.
  }
}

export function resolvePaimos6Selection(
  input: ResolvePaimos6SelectionInput,
): ResolvedPaimos6Selection {
  const authorized = new Set(input.authorizedIds)

  // A still-authorized current selection survives refreshes, attention
  // changes, and row reordering. A newly pasted deep link is handled by the
  // caller after clearing currentId so it cannot be mistaken for a rerender.
  if (input.currentId && authorized.has(input.currentId)) {
    return { id: input.currentId, source: 'current', clearInvalidDeepLink: false }
  }

  if (input.deepLinkedId !== null) {
    if (authorized.has(input.deepLinkedId)) {
      return { id: input.deepLinkedId, source: 'deep-link', clearInvalidDeepLink: false }
    }
    return { id: null, source: 'none', clearInvalidDeepLink: true }
  }

  const stored = readStoredSelection(
    input.storage,
    paimos6SelectionStorageKey(input.scope),
  )
  if (stored && authorized.has(stored)) {
    return { id: stored, source: 'stored', clearInvalidDeepLink: false }
  }
  if (stored) persistPaimos6Selection(input.storage, input.scope, null)

  return { id: null, source: 'none', clearInvalidDeepLink: false }
}
