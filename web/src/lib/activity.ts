// SPDX-License-Identifier: AGPL-3.0-only
// Turns the B2 activity feed (newest first, page by page) into a readable,
// chronological timeline: comments stay as they are; field changes by the same
// person within a few minutes collapse into one line.
import type { ActivityChange, ActivityItem } from './api.ts'
import { kindLabel, normaliseState, priorityLabel, statusMeta } from './work.ts'

export const COLLAPSE_MS = 5 * 60_000
export const EDIT_WINDOW_MS = 15 * 60_000

export type TimelineEntry =
  | { kind: 'automatic'; id: string; at: string; author: ActivityItem['author']; change: NonNullable<ActivityItem['automatic_change']> }
  | { kind: 'comment'; id: string; at: string; author: ActivityItem['author']; body: string }
  | { kind: 'changes'; id: string; at: string; author: ActivityItem['author']; changes: ActivityChange[] }
  | { kind: 'created'; id: string; at: string; author: ActivityItem['author'] }

function sameAuthor(a: ActivityItem['author'], b: ActivityItem['author']) {
  return a.id && b.id ? a.id === b.id : a.name === b.name
}
function sameValue(field: ActivityChange['field'], a: string | null, b: string | null) {
  return field === 'status' ? normaliseState(a ?? '') === normaliseState(b ?? '') : (a ?? '') === (b ?? '')
}
function merge(into: ActivityChange[], next: ActivityChange[]): ActivityChange[] {
  const out = into.map(change => ({ ...change }))
  for (const change of next) {
    const existing = out.find(item => item.field === change.field)
    if (existing) existing.to = change.to
    else out.push({ ...change })
  }
  return out.filter(change => change.field === 'parent' || !sameValue(change.field, change.from, change.to))
}

// items: newest first, as the API pages them. Returns oldest first.
export function buildTimeline(items: ActivityItem[], windowMs = COLLAPSE_MS): TimelineEntry[] {
  const out: TimelineEntry[] = []
  for (const item of [...items].reverse()) {
    if (item.automatic_change) { out.push({ kind: 'automatic', id: item.id, at: item.at, author: item.author, change: item.automatic_change }); continue }
    if (item.type === 'comment') { out.push({ kind: 'comment', id: item.id, at: item.at, author: item.author, body: item.body_markdown ?? '' }); continue }
    if (item.type === 'created') { out.push({ kind: 'created', id: item.id, at: item.at, author: item.author }); continue }
    const changes = item.changes ?? []
    const last = out[out.length - 1]
    if (last?.kind === 'changes' && sameAuthor(last.author, item.author) && !last.author.automatic && !item.author.automatic && Date.parse(item.at) - Date.parse(last.at) <= windowMs) {
      last.changes = merge(last.changes, changes)
      last.at = item.at
      if (!last.changes.length) out.pop()
      continue
    }
    const fresh = merge([], changes)
    if (fresh.length) out.push({ kind: 'changes', id: item.id, at: item.at, author: item.author, changes: fresh })
  }
  return out
}

const FIELD_LABEL: Record<ActivityChange['field'], string> = { status: 'status', priority: 'priority', assignee: 'assignee', title: 'title', parent: 'parent', tags: 'labels', kind: 'the type', human_check: 'the human check' }
export function changeValue(field: ActivityChange['field'], value: string | null): string {
  if (field === 'status') return value ? statusMeta(value).label : '—'
  if (field === 'priority') return priorityLabel(value)
  if (field === 'assignee') return value || 'nobody'
  if (field === 'title') return value ? `“${value.length > 60 ? `${value.slice(0, 57)}…` : value}”` : '—'
  if (field === 'tags') return value || 'no labels'
  return value ?? '—'
}
export function describeChange(change: ActivityChange): { label: string; from?: string; to?: string } {
  if (change.field === 'human_check' && !change.from) return { label: 'added a human check', to: change.to ?? '—' }
  if (change.field === 'human_check' && !change.to) return { label: 'completed the human check', to: change.from ?? '—' }
  if (change.field === 'kind') return { label: 'changed the type', from: change.from ? kindLabel(change.from) : '—', to: change.to ? kindLabel(change.to) : '—' }
  if (change.field === 'parent') return { label: 'moved it to another parent' }
  if (change.field === 'assignee' && !change.from) return { label: 'assigned it to', to: changeValue('assignee', change.to) }
  if (change.field === 'assignee' && !change.to) return { label: 'unassigned', from: changeValue('assignee', change.from) }
  if (change.field === 'tags' && !change.from) return { label: 'added the label', to: change.to ?? '—' }
  if (change.field === 'tags' && !change.to) return { label: 'cleared the labels', from: changeValue('tags', change.from) }
  return { label: `changed ${FIELD_LABEL[change.field]}`, from: changeValue(change.field, change.from), to: changeValue(change.field, change.to) }
}

export function commentEditable(entry: { at: string; author: { id: string | null } }, me: string | undefined, now = Date.now()): boolean {
  return !!me && entry.author.id === me && now - Date.parse(entry.at) < EDIT_WINDOW_MS
}

// Agent attribution markers ("I work on this — session: <name> (<id>); role: <role>;
// [model: <model>;] started: <iso>") become one compact system line; any text after
// the marker still reads as a normal comment.
export interface WorkerMarker { session: string; sessionId: string; role: string; started: string; extras: { key: string; value: string }[]; line: string; rest: string }
const MARKER = /^I work on this\s*[—–-]+\s*session:\s*(.+?)\s*\(([^()]*)\)\s*;\s*role:\s*([^;]+?)\s*;((?:\s*[\w-]+:\s*[^;]+?;)*?)\s*started:\s*(\S+?)[.;,]?(?=\s|$)(.*)$/i
// The compact line shows the role's first words; the full text stays in the expansion.
export function shortRole(role: string): string {
  const head = role.split(/\s*[(,]/)[0].trim()
  return head.length > 32 ? `${head.slice(0, 30).trimEnd()}…` : head
}
export function parseWorkerMarker(body: string): WorkerMarker | null {
  const text = body.replace(/\r/g, '').trimStart()
  const newline = text.indexOf('\n')
  const first = newline === -1 ? text : text.slice(0, newline)
  const match = MARKER.exec(first)
  if (!match) return null
  const [, session, sessionId, role, extraText, started, tail] = match
  const extras = [...extraText.matchAll(/([\w-]+):\s*([^;]+?);/g)].map(([, key, value]) => ({ key, value: value.trim() }))
  const rest = [tail.trim().replace(/^[.;,:]\s*/, ''), newline === -1 ? '' : text.slice(newline + 1)].filter(part => part.trim()).join('\n\n').trim()
  return { session: session.trim(), sessionId: sessionId.trim(), role: role.trim().toLowerCase(), started, extras, line: first.slice(0, first.length - tail.length).trim(), rest }
}
