/* PAIMOS — Copyright (C) 2026 Markus Barta; AGPL-3.0-only. */
import { ApiError, parsePermissionsEpochHeader, type ApiMetaResponse } from '@/api/client'
export const invalid = (): never => {
  throw new Error('Invalid Habitat response')
}
export const object = (value: unknown): Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : invalid()
export const uuid = (value: unknown): value is string =>
  typeof value === 'string' &&
  /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(value)
export const positive = (value: unknown): value is number =>
  Number.isSafeInteger(value) && Number(value) > 0
export function fields(
  value: Record<string, unknown>,
  required: string[],
  optional: string[] = [],
) {
  if (
    required.some((key) => !(key in value)) ||
    Object.keys(value).some((key) => !required.includes(key) && !optional.includes(key))
  )
    invalid()
}
export function epoch<T>(result: ApiMetaResponse<T>): T {
  if (
    result.permissionsEpoch == null ||
    parsePermissionsEpochHeader(result.permissionsEpoch) !== result.permissionsEpoch ||
    !Number.isSafeInteger(result.permissionsEpochGeneration) ||
    result.permissionsEpochGeneration < 0
  )
    throw new ApiError(0, 'Response authority unavailable')
  return result.data
}
