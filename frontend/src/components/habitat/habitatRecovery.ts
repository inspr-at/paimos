/* PAIMOS — Copyright (C) 2026 Markus Barta; AGPL-3.0-only. */
import { ssHabitatIntentKey, SS_HABITAT_INTENT_PREFIX } from '@/constants/storage'
import { fields, invalid, object, positive, uuid } from './habitatBoundary'
import { parseHabitatRequest, type HabitatLifecycleRequest } from './habitatLifecycle'
export interface RecoveryScope {
  origin: string
  instance: string
  principalId: number
  projectId: number
}
export interface IntentRecovery {
  intentId: string | null
  request: HabitatLifecycleRequest
  savedAt: number
}
const MAX_BYTES = 4096,
  MAX_ENTRIES = 12,
  MAX_AGE = 24 * 60 * 60 * 1000
const key = (scope: RecoveryScope) =>
  ssHabitatIntentKey(scope.origin, scope.instance, scope.principalId, scope.projectId)
export function readIntentRecovery(scope: RecoveryScope): IntentRecovery | null {
  try {
    const raw = sessionStorage.getItem(key(scope))
    if (!raw) return null
    if (raw.length > MAX_BYTES) invalid()
    const row = object(JSON.parse(raw))
    fields(row, ['schema_version', 'scope', 'intentId', 'request', 'savedAt'])
    const storedScope = object(row.scope)
    fields(storedScope, ['origin', 'instance', 'principalId', 'projectId'])
    if (
      row.schema_version !== 1 ||
      Object.entries(scope).some(([key, value]) => storedScope[key] !== value) ||
      !positive(row.savedAt) ||
      row.savedAt > Date.now() ||
      Date.now() - row.savedAt > MAX_AGE ||
      (row.intentId !== null && !uuid(row.intentId))
    )
      invalid()
    return {
      intentId: row.intentId as string | null,
      request: parseHabitatRequest(row.request),
      savedAt: row.savedAt as number,
    }
  } catch {
    clearIntentRecovery(scope)
    return null
  }
}
export function writeIntentRecovery(scope: RecoveryScope, value: IntentRecovery): boolean {
  try {
    const data = JSON.stringify({
      schema_version: 1,
      scope,
      ...value,
      request: parseHabitatRequest(value.request),
    })
    if (
      data.length > MAX_BYTES ||
      !positive(scope.principalId) ||
      !positive(scope.projectId) ||
      !scope.origin ||
      !scope.instance
    )
      return false
    const keys = Array.from({ length: sessionStorage.length }, (_, index) =>
      sessionStorage.key(index),
    ).filter(
      (value): value is string =>
        !!value?.startsWith(SS_HABITAT_INTENT_PREFIX) && value !== key(scope),
    )
    while (keys.length >= MAX_ENTRIES) sessionStorage.removeItem(keys.shift()!)
    sessionStorage.setItem(key(scope), data)
    return true
  } catch {
    return false
  }
}
export function clearIntentRecovery(scope: RecoveryScope) {
  try {
    sessionStorage.removeItem(key(scope))
  } catch {
    /* unavailable */
  }
}

/** Unsubmitted lifecycle review drafts only; never matches a live server intent receipt. */
export function findLifecycleDraftForSession(
  principalId: number,
  projectId: number,
  sessionId: string,
): { scope: RecoveryScope; recovery: IntentRecovery } | null {
  if (!positive(principalId) || !positive(projectId) || !uuid(sessionId)) return null
  try {
    for (let index = 0; index < sessionStorage.length; index++) {
      const storageKey = sessionStorage.key(index)
      if (!storageKey?.startsWith(SS_HABITAT_INTENT_PREFIX)) continue
      const raw = sessionStorage.getItem(storageKey)
      if (!raw || raw.length > MAX_BYTES) continue
      const row = object(JSON.parse(raw))
      fields(row, ['schema_version', 'scope', 'intentId', 'request', 'savedAt'])
      if (row.schema_version !== 1 || row.intentId !== null || !positive(row.savedAt)) continue
      const storedScope = object(row.scope)
      fields(storedScope, ['origin', 'instance', 'principalId', 'projectId'])
      if (storedScope.principalId !== principalId || storedScope.projectId !== projectId) continue
      const request = parseHabitatRequest(row.request)
      if ('session_id' in request && request.session_id === sessionId) {
        return {
          scope: {
            origin: String(storedScope.origin),
            instance: String(storedScope.instance),
            principalId: Number(storedScope.principalId),
            projectId: Number(storedScope.projectId),
          },
          recovery: {
            intentId: null,
            request,
            savedAt: row.savedAt as number,
          },
        }
      }
    }
  } catch {
    return null
  }
  return null
}
