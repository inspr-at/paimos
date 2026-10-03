// SPDX-License-Identifier: AGPL-3.0-only
// Pure helpers for the ticket list and project pages: product words, status and
// priority semantics, time formatting, sorting and highlighting. No Vue here, so
// tests/work.test.ts can run them under plain Node.

export type StatusKey = 'open' | 'new' | 'backlog' | 'blocked' | 'progress' | 'qa' | 'done' | 'delivered' | 'accepted' | 'cancelled' | 'archived' | 'other'
export interface StatusMeta { key: StatusKey; label: string; closed: boolean; order: number }

const STATUS: Record<Exclude<StatusKey, 'other'>, Omit<StatusMeta, 'key'>> = {
  open: { label: 'Open', closed: false, order: 2 },
  new: { label: 'New', closed: false, order: 0 },
  backlog: { label: 'Backlog', closed: false, order: 1 },
  blocked: { label: 'Blocked', closed: false, order: 3 },
  progress: { label: 'In progress', closed: false, order: 4 },
  qa: { label: 'QA', closed: false, order: 5 },
  accepted: { label: 'Accepted', closed: true, order: 8 },
  delivered: { label: 'Delivered', closed: true, order: 7 },
  done: { label: 'Done', closed: true, order: 6 },
  cancelled: { label: 'Cancelled', closed: true, order: 9 },
  archived: { label: 'Archived', closed: true, order: 10 },
}

// Imported data spells some states with hyphens ("in-progress"); treat spellings alike.
export function normaliseState(state: string): string {
  return state.trim().toLowerCase().replace(/[\s-]+/g, '_')
}

export function sentenceCase(value: string): string {
  const words = value.replace(/[_-]+/g, ' ').trim()
  return words ? words[0].toUpperCase() + words.slice(1).toLowerCase() : '—'
}

export function statusMeta(state: string): StatusMeta {
  const normal = normaliseState(state)
  const key: StatusKey = normal === 'in_progress' || normal === 'active' ? 'progress'
    : normal === 'canceled' ? 'cancelled'
    : normal in STATUS ? normal as StatusKey : 'other'
  if (key === 'other') return { key, label: sentenceCase(state), closed: false, order: 11 }
  return { key, ...STATUS[key] }
}

// States a person can set from the list, in workflow order. The in-progress value
// follows the tenant's existing spelling so imported and new data stay consistent.
export function statusOptions(knownStates: Iterable<string> = []): { value: string; meta: StatusMeta }[] {
  const known = new Set(knownStates)
  const progress = known.has('in-progress') && !known.has('in_progress') ? 'in-progress' : 'in_progress'
  return ['new', 'backlog', 'open', 'blocked', progress, 'qa', 'done', 'delivered', 'accepted', 'cancelled', 'archived'].map(value => ({ value, meta: statusMeta(value) }))
}

export const PRIORITIES = [
  { value: 'high', label: 'High' },
  { value: 'medium', label: 'Medium' },
  { value: 'low', label: 'Low' },
] as const
export function priorityLabel(priority: string | null | undefined): string {
  if (!priority || priority === 'none') return 'No priority'
  return PRIORITIES.find(p => p.value === priority)?.label ?? sentenceCase(priority)
}

export const KINDS = [
  { value: 'epic', label: 'Epic' },
  { value: 'ticket', label: 'Ticket' },
  { value: 'task', label: 'Task' },
] as const
export function kindLabel(slug: string): string {
  return KINDS.find(k => k.value === slug)?.label ?? sentenceCase(slug)
}

export function initials(name: string | null | undefined): string {
  const parts = (name ?? '').trim().split(/[\s._-]+/).filter(Boolean)
  if (!parts.length) return '?'
  if (parts.length === 1) return parts[0].slice(0, 2).toUpperCase()
  return (parts[0][0] + parts[parts.length - 1][0]).toUpperCase()
}

const MINUTE = 60_000, HOUR = 60 * MINUTE, DAY = 24 * HOUR
const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']
const monthDay = { format: (time: number) => { const d = new Date(time); return `${d.getDate()} ${MONTHS[d.getMonth()]}` } }
const monthDayYear = { format: (time: number) => { const d = new Date(time); return `${d.getDate()} ${MONTHS[d.getMonth()]} ${d.getFullYear()}` } }
const absolute = new Intl.DateTimeFormat('en-GB', { weekday: 'short', day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit' })

// Compact relative time for dense columns ("5m", "3h", "2d", "12 Sep"); long form for prose.
export function relativeTime(iso: string, options: { now?: number; long?: boolean } = {}): string {
  const time = Date.parse(iso)
  if (Number.isNaN(time)) return '—'
  const now = options.now ?? Date.now()
  const diff = Math.max(0, now - time)
  const long = options.long ?? false
  if (diff < MINUTE) return 'just now'
  if (diff < HOUR) { const m = Math.floor(diff / MINUTE); return long ? `${m} min ago` : `${m}m ago` }
  if (diff < DAY) { const h = Math.floor(diff / HOUR); return long ? `${h} ${h === 1 ? 'hour' : 'hours'} ago` : `${h}h ago` }
  const days = Math.floor(diff / DAY)
  if (days === 1) return 'yesterday'
  if (days < 7) return long ? `${days} days ago` : `${days}d ago`
  return new Date(time).getFullYear() === new Date(now).getFullYear() ? monthDay.format(time) : monthDayYear.format(time)
}
export function absoluteTime(iso: string): string {
  const time = Date.parse(iso)
  return Number.isNaN(time) ? '' : absolute.format(time)
}

// First meaningful prose line of a Markdown description, without Markdown syntax.
export function descriptionLine(markdown: string | null | undefined): string {
  if (!markdown) return ''
  const blocks = markdown.replace(/\r/g, '').split(/\n\s*\n/)
  for (const block of blocks) {
    const lines = block.split('\n').map(line => line.trim()).filter(line => line && !/^#{1,6}\s/.test(line) && !/^(```|---|\*\*\*|>\s*$)/.test(line))
    if (!lines.length) continue
    const text = lines.join(' ')
      .replace(/!\[[^\]]*\]\([^)]*\)/g, '')
      .replace(/\[([^\]]+)\]\([^)]*\)/g, '$1')
      .replace(/(\*\*|__|\*|_|`)/g, '')
      .replace(/^([-*+]|\d+\.)\s+/, '')
      .replace(/^>\s*/, '')
      .replace(/\s+/g, ' ')
      .trim()
    if (text) return text
  }
  return ''
}

export interface Segment { text: string; match: boolean }
export function highlight(text: string, query: string): Segment[] {
  const needle = query.trim().toLowerCase()
  if (!needle) return [{ text, match: false }]
  const out: Segment[] = []
  const hay = text.toLowerCase()
  let at = 0
  for (let found = hay.indexOf(needle); found !== -1; found = hay.indexOf(needle, at)) {
    if (found > at) out.push({ text: text.slice(at, found), match: false })
    out.push({ text: text.slice(found, found + needle.length), match: true })
    at = found + needle.length
  }
  if (at < text.length) out.push({ text: text.slice(at), match: false })
  return out
}

// Sort keys map one-to-one to the list API ("state,-updated_at").
export type SortField = 'order' | 'key' | 'title' | 'state' | 'priority' | 'updated_at' | 'created_at' | 'kind' | 'assignee' | 'eta_ready' | 'progress' | 'estimate' | 'model' | 'tokens' | 'list_cost' | 'paid'
export interface SortKey { field: SortField; desc: boolean }
export const SORT_FIELDS: SortField[] = ['order', 'key', 'title', 'state', 'priority', 'updated_at', 'created_at', 'kind', 'assignee', 'eta_ready', 'progress', 'estimate', 'model', 'tokens', 'list_cost', 'paid']
// What each sort key is called in the Display menu.
export const SORT_LABELS: Record<SortField, string> = { order: 'Release order', updated_at: 'Updated', created_at: 'Created', key: 'Key', title: 'Title', state: 'Status', priority: 'Priority', kind: 'Type', assignee: 'Assignee', eta_ready: 'Ready', progress: 'Percent done', estimate: 'Estimate', model: 'Model', tokens: 'Tokens', list_cost: 'Cost', paid: 'Paid' }
export function parseSort(raw: string | null | undefined): SortKey[] {
  if (!raw) return []
  const out: SortKey[] = []
  for (const part of raw.split(',')) {
    const desc = part.startsWith('-')
    const field = (desc ? part.slice(1) : part) as SortField
    if (SORT_FIELDS.includes(field) && !out.some(key => key.field === field)) out.push({ field, desc })
  }
  return out
}
export function serializeSort(keys: SortKey[]): string {
  return keys.map(key => (key.desc ? '-' : '') + key.field).join(',')
}
// Header click: ascending, then descending, then off. Shift adds or cycles a secondary key.
export function cycleSort(keys: SortKey[], field: SortField, additive: boolean): SortKey[] {
  const current = keys.find(key => key.field === field)
  const next: SortKey | null = !current ? { field, desc: false } : !current.desc ? { field, desc: true } : null
  if (!additive) return next ? [next] : []
  if (!current) return [...keys, next!]
  return next ? keys.map(key => key.field === field ? next : key) : keys.filter(key => key.field !== field)
}
export const DEFAULT_SORT: SortKey[] = [{ field: 'updated_at', desc: true }]

// The route key of a project: its classic key when imported (PHAROS), else the node key.
export function projectRouteKey(nodeKey: string, fields: Record<string, unknown> | null | undefined): string {
  const classic = fields?.classic
  const key = classic && typeof classic === 'object' ? (classic as Record<string, unknown>).key : undefined
  return typeof key === 'string' && key.trim() ? key.trim() : nodeKey
}
export function projectDescription(body: string, fields: Record<string, unknown> | null | undefined): string {
  const classic = fields?.classic
  const description = classic && typeof classic === 'object' ? (classic as Record<string, unknown>).description : undefined
  return descriptionLine(typeof description === 'string' && description.trim() ? description : body)
}

export function plural(count: number, one: string, many = `${one}s`): string {
  return `${count.toLocaleString('en-GB')} ${count === 1 ? one : many}`
}
