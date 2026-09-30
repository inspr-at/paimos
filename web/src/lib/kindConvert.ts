// SPDX-License-Identifier: AGPL-3.0-only
// Which issue kinds can become which, and which children would block that.

const SEEDED = ['epic', 'ticket', 'task'] as const

export interface KindRef {
  slug: string
  icon?: string
  field_schema?: Record<string, unknown> | null
}

function asKind(value: string | KindRef): KindRef {
  return typeof value === 'string' ? { slug: value, icon: value } : value
}

function seeded(name: string | undefined): boolean {
  return !!name && (SEEDED as readonly string[]).includes(name)
}

// A boolean issue_family on the kind schema wins. When it is absent, the seeded
// issue slugs and any kind that still uses one of those icons are the family.
export function isIssueKind(kind: string | KindRef): boolean {
  const row = asKind(kind)
  const flag = row.field_schema && typeof row.field_schema === 'object' ? row.field_schema.issue_family : undefined
  if (typeof flag === 'boolean') return flag
  return seeded(row.slug) || seeded(row.icon)
}

// Targets are the other issue-family kinds this tenant actually has.
export function convertTargets(current: string, configured: readonly (string | KindRef)[]): string[] {
  const rows = configured.map(asKind)
  const mine = rows.find(kind => kind.slug === current) ?? { slug: current }
  if (!isIssueKind(mine)) return []
  return rows.filter(kind => kind.slug !== current && isIssueKind(kind)).map(kind => kind.slug)
}

// null allowed children means any kind. An empty list means none.
export function parentAllows(allowed: readonly string[] | null | undefined, target: string): boolean {
  if (allowed == null) return true
  return allowed.includes(target)
}

export function blockingChildren<T extends { key: string; title: string; kind: string }>(
  allowed: readonly string[] | null | undefined,
  children: readonly T[],
): T[] {
  if (allowed == null) return []
  const ok = new Set(allowed)
  return children.filter(child => !ok.has(child.kind))
}

// Fields the target schema will not keep on the node. They stay in the
// conversion event. An open schema, or one that describes additional
// properties, keeps them.
export function fieldsMovingToHistory(schema: Record<string, unknown> | null | undefined, fields: Record<string, unknown> | null | undefined): string[] {
  if (!schema || schema.additionalProperties !== false) return []
  const declared = schema.properties && typeof schema.properties === 'object' ? new Set(Object.keys(schema.properties as object)) : new Set<string>()
  return Object.keys(fields ?? {}).filter(name => !declared.has(name)).sort()
}
