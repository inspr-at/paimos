// SPDX-License-Identifier: AGPL-3.0-only
// The existing ships_in bookmark value is the sole project scope. Keep raw
// legacy values for a visible repair; never turn an ambiguous view into all work.
export type ReleaseScope = { kind: 'all' } | { kind: 'backlog' } | { kind: 'release'; id: string } | { kind: 'repair'; values: string[] }
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
export function releaseScope(values: readonly string[]): ReleaseScope {
  if (!values.length) return { kind: 'all' }
  if (values.length === 1 && values[0] === 'none') return { kind: 'backlog' }
  if (values.length === 1 && UUID.test(values[0])) return { kind: 'release', id: values[0].toLowerCase() }
  return { kind: 'repair', values: [...values] }
}
export function scopeParameter(scope: ReleaseScope): string | undefined {
  if (scope.kind === 'repair') throw new Error('Choose a release scope')
  return scope.kind === 'all' ? undefined : scope.kind === 'backlog' ? 'none' : scope.id
}

export function scopeValuesFromQuery(raw: unknown): string[] {
  const values = Array.isArray(raw) ? raw : [raw]
  const text = values.filter((v): v is string => typeof v === 'string').join(',')
  if (new TextEncoder().encode(text).length > 6400 || text.split(',').length > 100) return ['invalid_scope']
  return [...new Set(text.split(',').map(v => v.trim()).filter(Boolean))]
}
// P6b consumes the same project/person/scope identity for Tickets and Knowledge.
export function scopeReadIdentity(project: string, person: string, scope: ReleaseScope): string {
  return JSON.stringify([project, person, scope])
}
export function scopeQuery(scope: ReleaseScope): Record<string, string> {
  const value = scopeParameter(scope)
  return value ? { ships_in: value } : {}
}
