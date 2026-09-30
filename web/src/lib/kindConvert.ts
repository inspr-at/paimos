// SPDX-License-Identifier: AGPL-3.0-only
// Which issue kinds can become which, and which children would block that.

export const ISSUE_KINDS = ['epic', 'ticket', 'task'] as const

export function isIssueKind(slug: string): boolean {
  return (ISSUE_KINDS as readonly string[]).includes(slug)
}

// Targets are the other issue kinds this tenant actually has.
export function convertTargets(current: string, configured: readonly string[]): string[] {
  if (!isIssueKind(current)) return []
  const have = new Set(configured)
  return ISSUE_KINDS.filter(slug => slug !== current && have.has(slug))
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
