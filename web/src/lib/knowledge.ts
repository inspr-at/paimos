// SPDX-License-Identifier: AGPL-3.0-only
// Knowledge (/api/knowledge): runbooks, guidelines, memory, external systems and
// related projects that agents read before they work. The wire types, the calls,
// and what the screens derive from them (groups, filters, slugs, headings, the
// agent command). Free of Vue for unit tests.
import { api } from './api.ts'

export type KnowledgeType = 'runbook' | 'guideline' | 'memory' | 'external-system' | 'related-project' | 'decision'
export type KnowledgeStatus = 'active' | 'proposed' | 'archived'
export interface KnowledgePerson { id: string; name: string }
export interface KnowledgeProject { id: string; key: string; title: string }
export interface KnowledgeItem {
  id: string; key: string; type: KnowledgeType; kind: string; slug: string; title: string
  status: KnowledgeStatus; state: string; project: KnowledgeProject | null; excerpt: string; link_count: number
  created_at: string; updated_at: string; updated_by: KnowledgePerson | null; imported: boolean
}
export interface KnowledgeLink {
  relation_id: string; type: string; direction: 'out' | 'in'
  node: { id: string; key: string; title: string; state: string; kind: string; project_id: string | null; slug?: string; type?: KnowledgeType }
}
export interface KnowledgeEntry extends KnowledgeItem {
  body: string; metadata: Record<string, unknown>; author: KnowledgePerson | null; links: KnowledgeLink[]
  renamed_from?: string; event_id?: number
}
export interface KnowledgePage {
  items: KnowledgeItem[]; total: number; truncated: boolean
  counts: { type: Partial<Record<KnowledgeType, number>>; status: Partial<Record<KnowledgeStatus, number>> }
}

// ---------- Knowledge kinds ----------
export interface TypeMeta { type: KnowledgeType; label: string; plural: string; icon: 'person-check' | 'runbook' | 'guideline' | 'memory' | 'server' | 'folders'; hint: string }
export const TYPES: readonly TypeMeta[] = [
  { type: 'runbook', label: 'Runbook', plural: 'Runbooks', icon: 'runbook', hint: 'Step-by-step procedures: deploys, rotations, recoveries.' },
  { type: 'guideline', label: 'Guideline', plural: 'Guidelines', icon: 'guideline', hint: 'Rules to follow: conventions, safety, style.' },
  { type: 'memory', label: 'Memory', plural: 'Memory', icon: 'memory', hint: 'What was learned: decisions, pitfalls, preferences.' },
  { type: 'external-system', label: 'External system', plural: 'External systems', icon: 'server', hint: 'Services the work touches: consoles, APIs, vaults.' },
  { type: 'related-project', label: 'Related project', plural: 'Related projects', icon: 'folders', hint: 'Projects this one depends on or feeds.' },
  { type: 'decision', label: 'Decision', plural: 'Decisions', icon: 'person-check', hint: 'Approved Always answers; replacements keep their history.' },
]
// Each kind's hue (tokens.css --kind-*): list icons, graph bubbles and the legend agree.
export const kindToken = (type: KnowledgeType | 'ticket') => `--kind-${type}`
export function typeMeta(type: string): TypeMeta {
  return TYPES.find(t => t.type === type || t.type === type.replace(/_/g, '-')) ?? TYPES[0]
}
export const isKnowledgeType = (value: unknown): value is KnowledgeType => typeof value === 'string' && TYPES.some(t => t.type === value)

export const STATUSES: readonly { status: KnowledgeStatus; label: string; hint: string }[] = [
  { status: 'active', label: 'Active', hint: 'Agents and people rely on it.' },
  { status: 'proposed', label: 'Proposed', hint: 'A draft, often from an agent, waiting for a person.' },
  { status: 'archived', label: 'Archived', hint: 'Kept for the record; agents skip it.' },
]
export const statusLabel = (status: KnowledgeStatus) => STATUSES.find(s => s.status === status)?.label ?? 'Active'

// ---------- Calls ----------
// A suspected credential in a method learning, as code point offsets.
export interface SensitiveRange { field: 'text' | 'title' | 'lesson' | 'reason'; start: number; end: number }
export class KnowledgeError extends Error {
  readonly status: number
  readonly code: string
  readonly entry: KnowledgeEntry | null
  readonly conflict: KnowledgeItem | null
  readonly ranges: SensitiveRange[]
  constructor(status: number, code: string, message: string, entry: KnowledgeEntry | null = null, conflict: KnowledgeItem | null = null, ranges: SensitiveRange[] = []) {
    super(message); this.status = status; this.code = code; this.entry = entry; this.conflict = conflict; this.ranges = ranges
  }
}
// What went wrong, as people say it.
export function plainError(status: number, code: string, message: string): string {
  if (code === 'person_required') return 'Only a person can accept or dismiss a method learning.'
  if (code === 'learning_sensitive') return 'This looks like a credential — remove it, or confirm it is not one.'
  if (code === 'learning_closed') return 'This learning is no longer open.'
  if (code === 'already_decided') return 'This learning was already accepted or dismissed.'
  if (code === 'learning_changed') return 'This learning changed since you reviewed it. Review it again.'
  if (code === 'rule_unavailable') return 'That rule set is not available.'
  if (code === 'rule_forbidden') return 'You cannot draft rules in that set.'
  if (code === 'already_in_set') return 'This learning is already a rule in that set.'
  if (code === 'revision_conflict') return 'That rule set changed. Open it and try again.'
  if (status === 403) return 'You can read knowledge here but not change it.'
  if (status === 404) return 'This entry no longer exists.'
  if (code === 'slug_taken') return message.replace(/^another/, 'Another')
  if (code === 'stale') return 'Someone else changed this entry.'
  if (status === 422 || status === 400) return message ? message[0].toUpperCase() + message.slice(1) + '.' : 'Something in the entry is not right.'
  if (status >= 500) return 'The server could not do that just now. Please try again.'
  return message || `Request failed (${status})`
}
async function send<T>(path: string, init: { method?: string; body?: unknown; headers?: Record<string, string>; signal?: AbortSignal } = {}): Promise<T> {
  const response = await api(`/knowledge${path}`, {
    method: init.method ?? 'GET',
    ...(init.signal ? { signal: AbortSignal.any([init.signal, AbortSignal.timeout(10_000)]) } : {}),
    headers: { ...(init.body === undefined ? {} : { 'Content-Type': 'application/json' }), ...init.headers },
    ...(init.body === undefined ? {} : { body: JSON.stringify(init.body) }),
  })
  if (!response.ok) {
    const data = await response.json().catch(() => ({})) as { error?: string; code?: string; entry?: KnowledgeEntry; conflict?: KnowledgeItem; ranges?: SensitiveRange[] }
    const code = typeof data.code === 'string' ? data.code : ''
    throw new KnowledgeError(response.status, code, plainError(response.status, code, typeof data.error === 'string' ? data.error : ''), data.entry ?? null, data.conflict ?? null, Array.isArray(data.ranges) ? data.ranges : [])
  }
  return await response.json() as T
}
function query(values: Record<string, string | number | undefined>): string {
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(values)) if (value !== undefined && value !== '') params.set(key, String(value))
  const text = params.toString()
  return text ? `?${text}` : ''
}
export interface ListParams { project_id?: string; q?: string; type?: KnowledgeType[]; status?: KnowledgeStatus[]; sort?: string; limit?: number }
export const listKnowledge = (params: ListParams = {}, signal?: AbortSignal) =>
  send<KnowledgePage>(query({ project_id: params.project_id, q: params.q?.trim(), type: params.type?.join(','), status: params.status?.join(','), sort: params.sort, limit: params.limit }), { signal })
export const getKnowledge = (id: string) => send<KnowledgeEntry>(`/${encodeURIComponent(id)}`)
export const resolveKnowledge = (projectId: string, type: KnowledgeType, slug: string) =>
  send<KnowledgeEntry>(`/resolve${query({ project_id: projectId, type, slug })}`)
export interface KnowledgeCreate { project_id: string; type: KnowledgeType; slug: string; title: string; body?: string; status?: KnowledgeStatus; metadata?: Record<string, unknown>; key_prefix?: string }
export const createKnowledge = (body: KnowledgeCreate) => send<KnowledgeEntry>('', { method: 'POST', body })
export interface KnowledgePatch { title?: string; body?: string; status?: KnowledgeStatus; slug?: string; metadata?: Record<string, unknown> }
// ifUnmodifiedSince is updated_at as read; a newer copy answers 412 with the current entry.
export const updateKnowledge = (id: string, patch: KnowledgePatch, ifUnmodifiedSince?: string) =>
  send<KnowledgeEntry>(`/${encodeURIComponent(id)}`, { method: 'PATCH', body: patch, headers: ifUnmodifiedSince ? { 'If-Unmodified-Since': ifUnmodifiedSince } : {} })
export const deleteKnowledge = (id: string, ifUnmodifiedSince?: string) =>
  send<{ id: string; event_id: number }>(`/${encodeURIComponent(id)}`, { method: 'DELETE', headers: ifUnmodifiedSince ? { 'If-Unmodified-Since': ifUnmodifiedSince } : {} })

// ---------- Method learnings (AEON-275) ----------
// An agent's (or a person's) prepared decision (AEON-788). It decides nothing;
// a person applies it through accept and dismiss.
export interface MethodLearningRecommendation {
  decision: 'accept' | 'dismiss'; knowledge_id?: string; knowledge_title?: string; lesson?: string; reason?: string
  by: KnowledgePerson | null; at: string; event_id: number; stale: boolean; target_missing: boolean
}
export interface MethodLearning {
  id: string; source: 'ticket' | 'comment'; node_id: string; key: string; title: string; text: string
  comment_id?: string; at: string; author: KnowledgePerson | null; href: string
  recommendation?: MethodLearningRecommendation
}
export interface MethodLearningPage { items: MethodLearning[]; truncated: boolean; next_cursor?: string }
export interface MethodLearningDecision {
  id: string; decision: 'accepted' | 'dismissed' | 'drafted'; event_id: number
  knowledge_id?: string; heading?: string; line?: string; entry?: KnowledgeEntry
  rule_set_id?: string; rule_layer_id?: string; rule_identity?: string
}
// cursor is the previous page's next_cursor: the learnings after its last item.
export const listLearnings = (projectId: string, signal?: AbortSignal, cursor?: string) =>
  send<MethodLearningPage>(`/learnings${query({ project_id: projectId, cursor })}`, { signal })
// confirmNotSensitive: a person confirms that text flagged as a credential is not one.
// lesson replaces the learning's own text in the changelog line. learningText
// is the learning's text as the person reviewed it; when it reads differently
// now, the server answers 409 learning_changed and writes nothing.
export const acceptLearning = (id: string, knowledgeId: string, ifUnmodifiedSince?: string, confirmNotSensitive = false, lesson?: string, learningText?: string) =>
  send<MethodLearningDecision>(`/learnings/${encodeURIComponent(id)}/accept`, {
    method: 'POST',
    body: {
      knowledge_id: knowledgeId, ...(lesson ? { lesson } : {}), ...(learningText !== undefined ? { learning_text: learningText } : {}),
      ...(confirmNotSensitive ? { confirm_not_sensitive: true } : {}),
    },
    headers: ifUnmodifiedSince ? { 'If-Unmodified-Since': ifUnmodifiedSince } : {},
  })
export const dismissLearning = (id: string, reason?: string, learningText?: string) =>
  send<MethodLearningDecision>(`/learnings/${encodeURIComponent(id)}/dismiss`, {
    method: 'POST', body: { ...(reason ? { reason } : {}), ...(learningText !== undefined ? { learning_text: learningText } : {}) },
  })
export const draftLearning = (id: string, body: { layer_id: string; set_id: string; confirm_not_sensitive?: true }) =>
  send<MethodLearningDecision>(`/learnings/${encodeURIComponent(id)}/draft`, { method: 'POST', body })
// ---------- Apply recommendations (AEON-788) ----------
// One row of the review list, frozen when the person opens it: applying sends
// exactly the lesson, entry and reason that were on screen.
export interface RecommendedStep {
  id: string; key: string; href: string; decision: 'accept' | 'dismiss'
  text: string; lesson: string; knowledgeId: string; knowledgeTitle: string; reason: string
  by: string
  // stale: the learning changed after the recommendation; left out until
  // chosen. target: the entry is gone; it cannot be applied.
  blocked: '' | 'stale' | 'target'
}
// changed: the learning reads differently now than on screen; it needs a new review.
export interface StepResult { id: string; ok: boolean; message: string; decision?: MethodLearningDecision; changed?: boolean }

export function recommendedSteps(items: readonly MethodLearning[]): RecommendedStep[] {
  const steps = items.flatMap((item): RecommendedStep[] => {
    const rec = item.recommendation
    if (!rec) return []
    const accept = rec.decision === 'accept'
    return [{
      id: item.id, key: item.key, href: item.href, decision: rec.decision, text: item.text,
      lesson: accept ? (rec.lesson || item.text) : '', knowledgeId: accept ? (rec.knowledge_id ?? '') : '',
      knowledgeTitle: accept ? (rec.knowledge_title ?? '') : '', reason: rec.reason ?? '', by: rec.by?.name ?? '',
      blocked: accept && (rec.target_missing || !rec.knowledge_id) ? 'target' : rec.stale ? 'stale' : '',
    }]
  })
  return [...steps.filter(step => step.decision === 'accept'), ...steps.filter(step => step.decision === 'dismiss')]
}
// Chosen by default: every step that is neither stale nor missing its entry.
export const defaultChosen = (steps: readonly RecommendedStep[]) => new Set(steps.filter(step => !step.blocked).map(step => step.id))

// text is the learning as the person reviewed it; the server refuses the
// step when the learning reads differently now.
export interface StepCalls {
  accept: (id: string, knowledgeId: string, lesson: string, text: string) => Promise<MethodLearningDecision>
  dismiss: (id: string, reason: string, text: string) => Promise<MethodLearningDecision>
}
// Applies the chosen steps one by one through the person-only accept and
// dismiss routes, each with exactly the lesson it showed. A failure is
// reported for its row and the rest continue; nothing outside chosen is
// sent. stop() ends the run before the next step.
export async function applySteps(steps: readonly RecommendedStep[], chosen: ReadonlySet<string>, calls: StepCalls,
  onResult: (result: StepResult) => void = () => {}, stop: () => boolean = () => false): Promise<StepResult[]> {
  const results: StepResult[] = []
  for (const step of steps) {
    if (!chosen.has(step.id) || step.blocked === 'target') continue
    if (stop()) break
    let result: StepResult
    try {
      const decision = step.decision === 'accept'
        ? await calls.accept(step.id, step.knowledgeId, step.lesson, step.text)
        : await calls.dismiss(step.id, step.reason, step.text)
      result = { id: step.id, ok: true, message: step.decision === 'accept' ? 'Added to the changelog' : 'Dismissed', decision }
    } catch (e) {
      const sensitive = e instanceof KnowledgeError && e.code === 'learning_sensitive'
      const changed = e instanceof KnowledgeError && e.code === 'learning_changed'
      result = { id: step.id, ok: false, message: sensitive ? 'Looks like a credential — accept it on its own to confirm.' : e instanceof Error ? e.message : 'That did not work.', ...(changed ? { changed } : {}) }
    }
    results.push(result)
    onResult(result)
  }
  return results
}

// Undo one knowledge write through the event log.
export async function undoKnowledge(eventId: number): Promise<void> {
  const response = await api(`/events/${eventId}/undo`, { method: 'POST' })
  if (!response.ok) throw new Error(response.status === 409 ? 'It changed again since, so it cannot be undone.' : response.status === 403 ? 'Only the person who made the change, or an admin, can undo it.' : 'Undo did not work. Please try again.')
}

// ---------- Slugs ----------
export const SLUG = /^[a-z][a-z0-9_-]*$/
export const SLUG_MAX = 64
const RESERVED_MEMORY = new Set(['references', 'stale', 'proposed', 'needs-review'])
export function slugProblem(type: KnowledgeType, slug: string): string {
  if (!slug) return 'A slug is needed; agents find the entry by it.'
  if (slug.length > SLUG_MAX) return `At most ${SLUG_MAX} characters.`
  if (/[^a-z0-9_-]/.test(slug)) return 'Use lower-case letters, digits, - and _ only.'
  if (!/^[a-z]/.test(slug)) return 'Start with a letter.'
  if (type === 'memory' && RESERVED_MEMORY.has(slug)) return `“${slug}” is reserved for memory.`
  return ''
}
// A slug from a title: lower case, ASCII, words joined by hyphens, starting with a letter.
export function slugify(text: string, type: KnowledgeType = 'runbook'): string {
  const ascii = text.toLowerCase()
    .replace(/ä/g, 'ae').replace(/ö/g, 'oe').replace(/ü/g, 'ue').replace(/ß/g, 'ss')
    .normalize('NFKD').replace(/[̀-ͯ]/g, '')
  let slug = ascii.replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '')
  if (slug && !/^[a-z]/.test(slug)) slug = `${type[0]}-${slug}`
  if (slug.length > SLUG_MAX) slug = slug.slice(0, SLUG_MAX).replace(/-[^-]*$/, '') || slug.slice(0, SLUG_MAX)
  return slug.replace(/-+$/g, '')
}
// The slug a new entry would get: the title's, made unique among the taken ones.
export function suggestSlug(title: string, type: KnowledgeType, taken: Iterable<string>): string {
  const base = slugify(title, type)
  if (!base) return ''
  const used = new Set(taken)
  if (!used.has(base) && !slugProblem(type, base)) return base
  for (let n = 2; n < 100; n++) {
    const candidate = `${base.slice(0, SLUG_MAX - String(n).length - 1)}-${n}`
    if (!used.has(candidate)) return candidate
  }
  return base
}

// ---------- Groups, filters, sort ----------
export type SortBy = 'updated' | 'title' | 'slug' | 'relevance'
export const SORTS: readonly { value: SortBy; label: string }[] = [
  { value: 'relevance', label: 'Best match' },
  { value: 'updated', label: 'Recently updated' },
  { value: 'title', label: 'Title' },
  { value: 'slug', label: 'Slug' },
]
export interface KnowledgeGroup<T extends KnowledgeItem = KnowledgeItem> { type: KnowledgeType; meta: TypeMeta; items: T[] }
export function sortItems<T extends KnowledgeItem>(items: T[], by: SortBy): T[] {
  const time = (value: string) => Date.parse(value) || 0
  const out = [...items]
  if (by === 'relevance') return out
  return out.sort((a, b) => by === 'title' ? a.title.localeCompare(b.title, 'en', { sensitivity: 'base' }) || a.slug.localeCompare(b.slug)
    : by === 'slug' ? a.slug.localeCompare(b.slug)
    : time(b.updated_at) - time(a.updated_at) || a.slug.localeCompare(b.slug))
}
// Groups in the kinds' order; empty groups are left out.
export function groupItems<T extends KnowledgeItem>(items: T[], by: SortBy): KnowledgeGroup<T>[] {
  return TYPES.map(meta => ({ type: meta.type, meta, items: sortItems(items.filter(item => item.type === meta.type), by) })).filter(group => group.items.length)
}
export function filterItems<T extends KnowledgeItem>(items: T[], types: KnowledgeType[], statuses: KnowledgeStatus[]): T[] {
  return items.filter(item => (!types.length || types.includes(item.type)) && (!statuses.length || statuses.includes(item.status)))
}
export function countBy<T extends KnowledgeItem>(items: T[]) {
  const type: Partial<Record<KnowledgeType, number>> = {}, status: Partial<Record<KnowledgeStatus, number>> = {}
  for (const item of items) { type[item.type] = (type[item.type] ?? 0) + 1; status[item.status] = (status[item.status] ?? 0) + 1 }
  return { type, status }
}

// ---------- Places ----------
export function entryPath(routeKey: string, type: KnowledgeType, slug: string, heading = ''): string {
  return `/p/${encodeURIComponent(routeKey)}/knowledge/${type}/${encodeURIComponent(slug)}${heading ? `#${heading}` : ''}`
}
// ---------- The docked preview (U25) ----------
// From this width the list keeps at least 560px beside a reading pane of 560px
// (page gutter 28px, gap 22px, window margin 10px); below it an entry opens on
// its own page, as before.
export const DOCK_MIN_WIDTH = 1200
export const DOCK_MEDIA = `(min-width: ${DOCK_MIN_WIDTH}px)`
// The list keeps this much of the window beside the pane (560px plus the margins).
export const DOCK_LIST_RESERVE = 620
// The pane's entry in the list's address: ?entry=<type>/<slug>.
export function entryParam(type: KnowledgeType, slug: string): string { return `${type}/${slug}` }
export function parseEntryParam(value: unknown): { type: KnowledgeType; slug: string } | null {
  if (typeof value !== 'string') return null
  const at = value.indexOf('/')
  if (at <= 0) return null
  const type = value.slice(0, at), slug = value.slice(at + 1)
  return isKnowledgeType(type) && slug ? { type, slug } : null
}
export function dockPath(routeKey: string, type: KnowledgeType, slug: string): string {
  // Written like the router writes it (the slash stays readable).
  return `/p/${encodeURIComponent(routeKey)}/knowledge?entry=${type}/${encodeURIComponent(slug)}`
}

// How an agent reads the entry. The command is the product's CLI in its classic mode.
export function cliCommand(product: string, routeKey: string, type: KnowledgeType, slug: string): string {
  return `${product.toLowerCase()} knowledge get ${type} ${slug} --project ${routeKey}`
}

// ---------- Reading ----------
// Bodies often open with the entry's own title as a heading; the page shows it already.
// A heading that is the title's start ("ADR-001 · Foundation" for "ADR-001 · Foundation (accepted)") counts too.
export function withoutTitle(body: string, title: string): string {
  const match = /^\s*#\s+(.+?)\s*#*\s*(?:\n|$)/.exec(body)
  if (!match) return body
  const norm = (value: string) => value.toLowerCase().replace(/[^a-z0-9äöüß]+/g, ' ').trim()
  const heading = norm(match[1]), name = norm(title)
  const same = heading === name || (heading.length >= 8 && name.startsWith(`${heading} `))
  return same ? body.slice(match[0].length).replace(/^\s*\n/, '') : body
}
export function readingMinutes(body: string): number {
  const words = body.replace(/```[\s\S]*?```/g, ' ').split(/\s+/).filter(Boolean).length
  return Math.max(1, Math.round(words / 220))
}
// Heading anchors: the same words-to-slug as entry slugs, unique within one page.
export function headingSlug(text: string, used: Map<string, number>): string {
  const base = text.toLowerCase()
    .replace(/ä/g, 'ae').replace(/ö/g, 'oe').replace(/ü/g, 'ue').replace(/ß/g, 'ss')
    .normalize('NFKD').replace(/[̀-ͯ]/g, '')
    .replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '') || 'section'
  const seen = used.get(base) ?? 0
  used.set(base, seen + 1)
  return seen ? `${base}-${seen + 1}` : base
}
export interface Heading { level: number; text: string; id: string }
// A table of contents helps from three headings on, or two in a long entry.
export function wantsToc(headings: Heading[], body: string): boolean {
  const sections = headings.filter(h => h.level >= 2 && h.level <= 3)
  return sections.length >= 3 || (sections.length >= 2 && body.length > 2400)
}

// ---------- Type-specific details ----------
export interface DetailField { key: string; label: string; kind: 'text' | 'url' | 'choice' | 'list'; placeholder?: string; choices?: string[]; hint?: string }
export const DETAIL_FIELDS: Record<KnowledgeType, DetailField[]> = {
  decision: [],
  runbook: [{ key: 'related_agents', label: 'Agents that run it', kind: 'list', placeholder: 'camy, kite', hint: 'Agent names, separated by commas.' }],
  guideline: [{ key: 'rule', label: 'The rule in one line', kind: 'text', placeholder: 'Never mark state with a coloured edge.', hint: 'Agents put this line into their prompts.' }],
  memory: [{ key: 'confidence', label: 'Confidence', kind: 'choice', choices: ['high', 'medium', 'low'] }],
  'external-system': [
    { key: 'url', label: 'Address', kind: 'url', placeholder: 'https://console.example.com' },
    { key: 'purpose', label: 'What it is for', kind: 'text', placeholder: 'Provisioning and DNS' },
    { key: 'secret_path', label: 'Where its secret lives', kind: 'text', placeholder: '1Password: Studio / Hetzner API', hint: 'A path or vault item, never the secret itself.' },
  ],
  'related-project': [
    { key: 'instance_url', label: 'Address', kind: 'url', placeholder: 'https://pm.example.com' },
    { key: 'key', label: 'Project key', kind: 'text', placeholder: 'PHAROS' },
    { key: 'relationship', label: 'Relationship', kind: 'text', placeholder: 'Deploys this project' },
  ],
}
export function detailText(value: unknown): string {
  if (Array.isArray(value)) return value.filter(v => typeof v === 'string' || typeof v === 'number').join(', ')
  if (typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean') return String(value)
  return ''
}
export function validUrl(value: string): boolean {
  if (!value.trim()) return true
  try { const url = new URL(value.trim()); return (url.protocol === 'http:' || url.protocol === 'https:') && !!url.host } catch { return false }
}
// Metadata from the edit form's text values; keys the form does not know are kept.
export function mergeDetails(type: KnowledgeType, current: Record<string, unknown>, values: Record<string, string>): Record<string, unknown> {
  const out: Record<string, unknown> = { ...current }
  for (const field of DETAIL_FIELDS[type]) {
    const text = (values[field.key] ?? '').trim()
    if (!text) { delete out[field.key]; continue }
    out[field.key] = field.kind === 'list' ? text.split(',').map(part => part.trim()).filter(Boolean) : text
  }
  return out
}

// ---------- Search highlights ----------
export interface Segment { text: string; match: boolean }
// Every word of the search (two letters or more) marked where it occurs.
export function highlightWords(text: string, q: string): Segment[] {
  const words = q.toLowerCase().split(/\s+/).map(w => w.replace(/^["'(]+|["')]+$/g, '')).filter(w => w.length >= 2)
  if (!words.length || !text) return [{ text, match: false }]
  const hay = text.toLowerCase()
  const ranges: [number, number][] = []
  for (const word of words) for (let at = hay.indexOf(word); at !== -1; at = hay.indexOf(word, at + word.length)) ranges.push([at, at + word.length])
  if (!ranges.length) return [{ text, match: false }]
  ranges.sort((a, b) => a[0] - b[0])
  const merged: [number, number][] = []
  for (const range of ranges) { const last = merged[merged.length - 1]; if (last && range[0] <= last[1]) last[1] = Math.max(last[1], range[1]); else merged.push([...range]) }
  const out: Segment[] = []
  let at = 0
  for (const [start, end] of merged) { if (start > at) out.push({ text: text.slice(at, start), match: false }); out.push({ text: text.slice(start, end), match: true }); at = end }
  if (at < text.length) out.push({ text: text.slice(at), match: false })
  return out
}
