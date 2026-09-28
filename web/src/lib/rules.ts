// SPDX-License-Identifier: AGPL-3.0-only
// Agent rules (ADR-004). Types and calls follow the frozen AR1 contract:
// layers, sets, draft replacement, publish, restore and merged preview.
// Permission checks take the caller's grants; project membership is never a grant.
import { api } from './api.ts'
import { sessionGone } from './authz.ts'

export const RULES_BUDGET = 12000
export const PUBLISH_NOTE_MAX = 500
export const MAX_RULES = 100
export const LAYERS = ['company', 'project', 'person', 'agent'] as const
export const ROLES = ['coordinator', 'builder', 'reviewer', 'operator'] as const
export const HARNESSES = ['claude-code', 'codex', 'grok', 'pi', 'cursor'] as const
export type LayerName = typeof LAYERS[number]
export type RoleName = typeof ROLES[number]
export type HarnessName = typeof HARNESSES[number]
export type AgentMode = 'role' | 'named' | 'task'
export type CheckState = 'on' | 'off' | 'mixed'

export const LAYER_LABEL: Record<LayerName, string> = { company: 'Company', project: 'Project', person: 'Person', agent: 'Agent' }
export const ROLE_LABEL: Record<RoleName, string> = { coordinator: 'Coordinator', builder: 'Builder', reviewer: 'Reviewer', operator: 'Operator' }
export const HARNESS_LABEL: Record<HarnessName, string> = { 'claude-code': 'Claude', codex: 'Codex', grok: 'Grok', pi: 'Pi', cursor: 'Cursor' }

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/
const IDENTITY = /^[a-z][a-z0-9._-]{0,95}$/
const VERSION = /^[1-9][0-9]{11}\.0\.0$/

export interface RuleScope {
  layer: LayerName
  project_id?: string
  owner_id?: string
  agent_id?: string
  role?: RoleName
  task_id?: string
}
export interface RuleSource {
  reference: string
  revision?: string
  identity?: string
  edited_here: boolean
}
export interface AgentRule {
  identity: string
  text: string
  why: string
  details?: string
  strength: 'normal' | 'locked'
  enabled: boolean
  expires_at?: string | null
  roles?: RoleName[]
  harnesses?: HarnessName[]
  source: RuleSource
}
export interface RuleLayer { id: string; scope: RuleScope }
export interface RuleSet {
  id: string
  layer_id: string
  scope: RuleScope
  name: string
  revision: number
  rules: AgentRule[]
  published_version: string
}
export interface RuleSnapshot {
  set_id: string
  scope: RuleScope
  name: string
  revision: number
  version: string
  sha256: string
  rules: AgentRule[]
  published_at: string
  note?: string
}
export interface MergedRules {
  context: { tenant_id: string; project_id: string; person_id: string; agent_id?: string; role: string; harness: string; task_id?: string }
  versions: { set_id: string; version: string; sha256: string }[]
  version: string
  sha256: string
  body: string
  byte_size: number
  rules: AgentRule[]
  floor: string
  valid_until: string | null
}
export interface RuleContext {
  projectId: string
  personId: string
  agentId: string
  role: RoleName
  harness: HarnessName
  taskId: string
}
export interface Caller {
  id: string
  kind: 'person' | 'agent'
  allows: (permission: string, projectId?: string) => boolean
}

export class RulesError extends Error {
  readonly status: number
  readonly code: string
  readonly actualBytes?: number
  readonly maxBytes?: number
  constructor(status: number, code: string, message: string, actualBytes?: number, maxBytes?: number) {
    super(message)
    this.status = status
    this.code = code
    this.actualBytes = actualBytes
    this.maxBytes = maxBytes
  }
}

export const isUuid = (value: string) => UUID.test(value)
export const utf8Length = (value: string) => new TextEncoder().encode(value).length

export function calendarVersion(now = new Date()): string {
  const part = (n: number) => String(n).padStart(2, '0')
  const stamp = `${String(now.getUTCFullYear()).slice(2)}${part(now.getUTCMonth() + 1)}${part(now.getUTCDate())}${part(now.getUTCHours())}${part(now.getUTCMinutes())}${part(now.getUTCSeconds())}`
  return `${stamp}.0.0`
}
export const validVersion = (value: string) => VERSION.test(value) && validCalendarDay(value)

function validCalendarDay(value: string) {
  const year = 2000 + Number(value.slice(0, 2))
  const month = Number(value.slice(2, 4))
  const day = Number(value.slice(4, 6))
  const date = new Date(Date.UTC(year, month - 1, day))
  return date.getUTCFullYear() === year && date.getUTCMonth() + 1 === month && date.getUTCDate() === day
    && Number(value.slice(6, 8)) <= 23 && Number(value.slice(8, 10)) <= 59 && Number(value.slice(10, 12)) <= 59
}

function oneLine(value: string, max: number, required: boolean): string | null {
  if (required && value.trim() === '') return 'required'
  if (utf8Length(value) > max) return 'too long'
  for (const char of value) {
    const code = char.codePointAt(0) ?? 0
    if (code < 32 || code === 127 || char === '\u2028' || char === '\u2029') return 'one line'
  }
  return null
}

export function normalizeRule(raw: AgentRule): AgentRule {
  return {
    identity: raw.identity,
    text: raw.text ?? '',
    why: raw.why ?? '',
    details: raw.details ?? '',
    strength: raw.strength === 'locked' ? 'locked' : 'normal',
    enabled: raw.strength === 'locked' ? true : !!raw.enabled,
    expires_at: raw.strength === 'locked' ? null : raw.expires_at ?? null,
    roles: [...(raw.roles ?? [])],
    harnesses: [...(raw.harnesses ?? [])],
    source: {
      reference: raw.source?.reference ?? '',
      revision: raw.source?.revision ?? '',
      identity: raw.source?.identity ?? '',
      edited_here: !!raw.source?.edited_here,
    },
  }
}

export function rulePayload(rule: AgentRule): AgentRule {
  const source: RuleSource = { reference: rule.source.reference, edited_here: rule.source.edited_here }
  if (rule.source.revision) source.revision = rule.source.revision
  if (rule.source.identity) source.identity = rule.source.identity
  const payload: AgentRule = {
    identity: rule.identity,
    text: rule.text,
    why: rule.why,
    strength: rule.strength,
    enabled: rule.strength === 'locked' ? true : rule.enabled,
    roles: rule.roles ?? [],
    harnesses: rule.harnesses ?? [],
    source,
  }
  if (rule.details) payload.details = rule.details
  if (rule.strength !== 'locked' && rule.expires_at) payload.expires_at = rule.expires_at
  return payload
}

export function rulesEqual(a: AgentRule[], b: AgentRule[]): boolean {
  return JSON.stringify(a.map(rulePayload)) === JSON.stringify(b.map(rulePayload))
}

export function scopePayload(scope: RuleScope): RuleScope {
  const payload: RuleScope = { layer: scope.layer }
  if (scope.project_id) payload.project_id = scope.project_id
  if (scope.owner_id) payload.owner_id = scope.owner_id
  if (scope.agent_id) payload.agent_id = scope.agent_id
  if (scope.role) payload.role = scope.role
  if (scope.task_id) payload.task_id = scope.task_id
  return payload
}

export function scopeRank(scope: RuleScope): number {
  if (scope.layer === 'company') return 0
  if (scope.layer === 'project') return 1
  if (scope.layer === 'person') return 2
  if (scope.role) return 3
  if (!scope.task_id) return 4
  return 5
}

export function scopeMatches(scope: RuleScope, ctx: RuleContext): boolean {
  if (scope.project_id && scope.project_id !== ctx.projectId) return false
  if (scope.owner_id && scope.owner_id !== ctx.personId) return false
  if (scope.agent_id && scope.agent_id !== ctx.agentId) return false
  if (scope.role && scope.role !== ctx.role) return false
  if (scope.task_id && scope.task_id !== ctx.taskId) return false
  return true
}

export function layerInColumn(scope: RuleScope, column: LayerName, ctx: RuleContext, mode: AgentMode): boolean {
  if (scope.layer !== column) return false
  if (column === 'company') return true
  if (column === 'project') return scope.project_id === ctx.projectId && ctx.projectId !== ''
  if (column === 'person') return scope.owner_id === ctx.personId && ctx.personId !== ''
  if (mode === 'role') return scope.role === ctx.role && !scope.agent_id && !scope.task_id
  if (mode === 'named') return scope.agent_id === ctx.agentId && ctx.agentId !== '' && scope.owner_id === ctx.personId && !scope.task_id
  return scope.task_id === ctx.taskId && ctx.taskId !== '' && scope.agent_id === ctx.agentId && scope.project_id === ctx.projectId && scope.owner_id === ctx.personId
}

export function scopeFor(column: LayerName, ctx: RuleContext, mode: AgentMode): { scope: RuleScope } | { error: string } {
  if (column === 'company') return { scope: { layer: 'company' } }
  if (column === 'project') {
    if (!isUuid(ctx.projectId)) return { error: 'Choose a project with a workspace id before adding project rules.' }
    return { scope: { layer: 'project', project_id: ctx.projectId } }
  }
  if (column === 'person') {
    if (!isUuid(ctx.personId)) return { error: 'Choose a person before adding their rules.' }
    return { scope: { layer: 'person', owner_id: ctx.personId } }
  }
  if (mode === 'role') return { scope: { layer: 'agent', role: ctx.role } }
  if (!isUuid(ctx.personId) || !isUuid(ctx.agentId)) return { error: 'Choose the person and the named agent first.' }
  if (mode === 'named') return { scope: { layer: 'agent', owner_id: ctx.personId, agent_id: ctx.agentId } }
  if (!isUuid(ctx.projectId) || !isUuid(ctx.taskId)) return { error: 'Choose the project and the task first.' }
  return { scope: { layer: 'agent', project_id: ctx.projectId, owner_id: ctx.personId, agent_id: ctx.agentId, task_id: ctx.taskId } }
}

export function writeBlock(caller: Caller | null, scope: RuleScope): string | null {
  if (!caller) return 'Sign in to edit rules.'
  if (scope.layer === 'company') {
    if (caller.kind !== 'person') return 'Agents cannot edit company rules.'
    if (!caller.allows('rules.write')) return 'Editing company rules needs the workspace permission to write rules.'
    if (!caller.allows('rules.publish')) return 'Editing company rules needs the workspace permission to publish rules.'
    return null
  }
  if (scope.layer === 'project') {
    if (!caller.allows('rules.write', scope.project_id)) return 'Editing this project’s rules needs permission to write rules for that project.'
    if (!caller.allows('rules.publish', scope.project_id)) return 'Editing this project’s rules needs permission to publish rules for that project.'
    return null
  }
  if (scope.layer === 'person') {
    if (scope.owner_id !== caller.id) return 'Only that person can edit their rules.'
    if (!caller.allows('rules.write')) return 'Editing your rules needs permission to write rules.'
    return null
  }
  if (scope.role) {
    if (!caller.allows('rules.write')) return 'Editing role rules needs the workspace permission to write rules.'
    if (!caller.allows('rules.publish')) return 'Editing role rules needs the workspace permission to publish rules.'
    return null
  }
  const owns = caller.id === scope.owner_id || (caller.kind === 'agent' && caller.id === scope.agent_id)
  if (!owns) return 'Only the owner of this agent can edit its rules.'
  if (scope.task_id) {
    if (!caller.allows('rules.write', scope.project_id)) return 'Editing task rules needs permission to write rules for that project.'
    return null
  }
  if (!caller.allows('rules.write')) return 'Editing this agent’s rules needs permission to write rules.'
  return null
}

export function publishBlock(caller: Caller | null, scope: RuleScope): string | null {
  if (!caller) return 'Sign in to publish rules.'
  if (caller.kind !== 'person') return 'Only a person can publish rules.'
  if (scope.layer === 'company') {
    if (!caller.allows('rules.publish')) return 'Publishing company rules needs the workspace permission to publish rules.'
    return null
  }
  if (scope.layer === 'project') {
    if (!caller.allows('rules.publish', scope.project_id)) return 'Publishing this project’s rules needs permission to publish rules for that project.'
    return null
  }
  if (scope.layer === 'person') {
    if (scope.owner_id !== caller.id) return 'Only that person can publish their rules.'
    if (!caller.allows('rules.publish')) return 'Publishing your rules needs permission to publish rules.'
    return null
  }
  if (scope.role) {
    if (!caller.allows('rules.publish')) return 'Publishing role rules needs the workspace permission to publish rules.'
    return null
  }
  if (scope.owner_id !== caller.id) return 'Only the owner of this agent can publish its rules.'
  if (scope.task_id) {
    if (!caller.allows('rules.publish', scope.project_id)) return 'Publishing task rules needs permission to publish rules for that project.'
    return null
  }
  if (!caller.allows('rules.publish')) return 'Publishing this agent’s rules needs permission to publish rules.'
  return null
}

export function canFlip(rule: AgentRule, held: ReadonlySet<string>): boolean {
  return rule.strength !== 'locked' && !held.has(rule.identity)
}

export function groupState(rules: AgentRule[], held: ReadonlySet<string>): CheckState {
  if (!rules.length) return 'off'
  const movable = rules.filter(rule => canFlip(rule, held))
  const protectedOn = rules.some(rule => !canFlip(rule, held))
  if (!movable.length) return 'on'
  const ons = movable.filter(rule => rule.enabled).length
  if (ons === movable.length) return 'on'
  if (ons === 0 && !protectedOn) return 'off'
  return 'mixed'
}

export function applyEnabled(rules: AgentRule[], enabled: boolean, held: ReadonlySet<string>): AgentRule[] {
  return rules.map(rule => {
    if (!canFlip(rule, held) || rule.enabled === enabled) return rule
    const next = normalizeRule({ ...rule, enabled })
    if (rule.source.identity) next.source.edited_here = true
    return next
  })
}

export function identityFromText(text: string, taken: ReadonlySet<string>): string {
  const slug = text.toLowerCase().replace(/[^a-z0-9._-]+/g, '-').replace(/^[^a-z]+/, '').replace(/-+/g, '-').replace(/-$/g, '')
  const base = (slug || 'rule').slice(0, 90)
  let identity = base
  let n = 2
  while (taken.has(identity) || !IDENTITY.test(identity)) {
    const suffix = `-${n++}`
    identity = `${base.slice(0, 96 - suffix.length)}${suffix}`
  }
  return identity
}

export function blankRule(taken: Iterable<string>): AgentRule {
  return {
    identity: identityFromText('new-rule', new Set(taken)),
    text: '', why: '', details: '', strength: 'normal', enabled: true, expires_at: null,
    roles: [...ROLES], harnesses: [...HARNESSES],
    source: { reference: '', edited_here: false },
  }
}

export function duplicateRule(rule: AgentRule, taken: Iterable<string>): AgentRule {
  const copy = normalizeRule(rule)
  const suffix = ' (copy)'
  const room = Math.max(0, 512 - utf8Length(suffix))
  let text = copy.text
  while (utf8Length(text) > room) text = text.slice(0, -1)
  return {
    ...copy,
    identity: identityFromText(`${copy.identity}-copy`, new Set(taken)),
    text: `${text}${suffix}`,
    strength: 'normal',
    enabled: true,
    expires_at: null,
    source: { reference: copy.source.reference, edited_here: false },
  }
}

export type RulePatch = Omit<Partial<AgentRule>, 'source'> & { source?: Partial<RuleSource> }
export function touchRule(rule: AgentRule, patch: RulePatch): AgentRule {
  const next = normalizeRule({
    ...rule,
    ...patch,
    source: { ...rule.source, ...patch.source },
  })
  const changed = rule.text !== next.text || rule.why !== next.why || (rule.details ?? '') !== (next.details ?? '')
    || rule.strength !== next.strength || rule.enabled !== next.enabled || (rule.expires_at ?? null) !== (next.expires_at ?? null)
    || JSON.stringify(rule.roles ?? []) !== JSON.stringify(next.roles ?? [])
    || JSON.stringify(rule.harnesses ?? []) !== JSON.stringify(next.harnesses ?? [])
    || rule.source.reference !== next.source.reference
  if (changed && rule.source.identity) next.source.edited_here = true
  if (next.strength === 'locked') { next.enabled = true; next.expires_at = null }
  return next
}

export function validateRule(rule: AgentRule, seen: ReadonlySet<string>): string | null {
  if (!IDENTITY.test(rule.identity)) return 'The identity must start with a letter and use only lowercase letters, digits, dots, underscores and hyphens.'
  if (seen.has(rule.identity)) return `“${rule.identity}” is already used in this set.`
  if (oneLine(rule.text, 512, true)) return 'The rule text is one line, up to 512 bytes.'
  if (oneLine(rule.why, 1024, true)) return 'The reason is one line, up to 1024 bytes.'
  if ((rule.details ?? '').includes('\u0000') || utf8Length(rule.details ?? '') > 16384) return 'Details must stay within 16384 bytes.'
  if (rule.strength !== 'normal' && rule.strength !== 'locked') return 'Strength is normal or locked.'
  if (rule.strength === 'locked' && (!rule.enabled || rule.expires_at)) return 'A locked rule stays on and does not expire.'
  if (oneLine(rule.source.reference, 512, true)) return 'Add a source, such as a ticket or an incident.'
  if (rule.source.revision && oneLine(rule.source.revision, 128, false)) return 'The source revision is one line, up to 128 bytes.'
  if (rule.source.identity && oneLine(rule.source.identity, 96, false)) return 'The upstream identity is one line, up to 96 bytes.'
  for (const role of rule.roles ?? []) if (!ROLES.includes(role)) return 'Unknown role.'
  for (const harness of rule.harnesses ?? []) if (!HARNESSES.includes(harness)) return 'Unknown harness.'
  return null
}

export function validateDraft(name: string, rules: AgentRule[]): string | null {
  if (oneLine(name, 128, true)) return 'The set name is one line, up to 128 bytes.'
  if (rules.length > MAX_RULES) return 'A set holds at most 100 rules.'
  const seen = new Set<string>()
  for (const rule of rules) {
    const issue = validateRule(rule, seen)
    if (issue) return issue
    seen.add(rule.identity)
  }
  return null
}

export interface RuleChange { kind: 'added' | 'removed' | 'changed'; label: string }
export function diffRules(before: AgentRule[], after: AgentRule[]): RuleChange[] {
  const prior = new Map(before.map(rule => [rule.identity, rule]))
  const next = new Map(after.map(rule => [rule.identity, rule]))
  const changes: RuleChange[] = []
  for (const rule of after) {
    const old = prior.get(rule.identity)
    if (!old) changes.push({ kind: 'added', label: rule.text || rule.identity })
    else if (!rulesEqual([old], [rule])) changes.push({ kind: 'changed', label: rule.text || rule.identity })
  }
  for (const rule of before) if (!next.has(rule.identity)) changes.push({ kind: 'removed', label: rule.text || rule.identity })
  return changes
}

/** Reset only when an upstream original was actually supplied. The contract has no original text. */
export function resetAvailability(rule: AgentRule, original?: AgentRule | null): { available: true; original: AgentRule } | { available: false; reason: string; show: boolean } {
  if (original && (original.identity === rule.source.identity || original.identity === rule.identity)) return { available: true, original: normalizeRule(original) }
  if (rule.source.identity) {
    return { available: false, show: true, reason: 'Reset needs the original wording. This rule records an upstream identity, not the original text.' }
  }
  return { available: false, show: false, reason: '' }
}

export function mergeQuery(ctx: RuleContext): { query: string } | { error: string } {
  if (!isUuid(ctx.projectId) || !isUuid(ctx.personId)) return { error: 'Choose a project and a person whose ids the workspace can send.' }
  if (!ROLES.includes(ctx.role) || !HARNESSES.includes(ctx.harness)) return { error: 'Choose a role and a harness.' }
  if (ctx.taskId && !ctx.agentId) return { error: 'A task preview also needs the named agent.' }
  if (ctx.agentId && !isUuid(ctx.agentId)) return { error: 'Choose a named agent.' }
  if (ctx.taskId && !isUuid(ctx.taskId)) return { error: 'Choose a task.' }
  const params = new URLSearchParams({ project_id: ctx.projectId, person_id: ctx.personId, role: ctx.role, harness: ctx.harness })
  if (ctx.agentId) params.set('agent_id', ctx.agentId)
  if (ctx.taskId) params.set('task_id', ctx.taskId)
  return { query: params.toString() }
}

export function heldIdentities(sets: { scope: RuleScope; rules: AgentRule[] }[], ctx: RuleContext, belowRank: number): Set<string> {
  const held = new Set<string>()
  for (const set of sets) {
    if (scopeRank(set.scope) >= belowRank || !scopeMatches(set.scope, ctx)) continue
    for (const rule of set.rules) if (rule.strength === 'locked') held.add(rule.identity)
  }
  return held
}

export function rulesMessage(error: unknown): string {
  if (!(error instanceof RulesError)) return error instanceof Error ? error.message : 'The rules request failed.'
  if (error.code === 'revision_conflict') return 'This set was saved elsewhere. Your draft is still here; save again to replace that version.'
  if (error.code === 'version_conflict') return 'That version is already used. Confirm again to take a new one.'
  if (error.code === 'rules_budget_exceeded') return `The merged file is ${error.actualBytes ?? 'over'} bytes. The limit is ${error.maxBytes ?? RULES_BUDGET}.`
  if (error.code === 'ambiguous_identity') return 'Two rules of the same rank share an identity, so the merge stops.'
  if (error.code === 'forbidden') return 'You do not have permission for that.'
  if (error.code === 'not_found') return 'That rules record is no longer there.'
  if (error.code === 'invalid_rule' || error.code === 'invalid_scope' || error.code === 'invalid_version') return error.message || 'The rules request was not accepted.'
  return error.message || 'The rules request failed.'
}

async function parseError(response: Response): Promise<RulesError> {
  const body = await response.json().catch(() => ({})) as { error?: string; code?: string; actual_bytes?: number; max_bytes?: number }
  return new RulesError(response.status, body.code || '', body.error || `Request failed (${response.status})`, body.actual_bytes, body.max_bytes)
}

async function send<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  const response = await api(path, {
    method,
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (response.status === 401) sessionGone()
  if (!response.ok) throw await parseError(response)
  return response.json() as Promise<T>
}

const clean = (rule: AgentRule) => normalizeRule(rule)
export const listLayers = () => send<{ layers: RuleLayer[] }>('/rules/layers')
export const createLayer = (scope: RuleScope) => send<RuleLayer>('/rules/layers', 'POST', scopePayload(scope))
export const listSets = async (layerId: string) => {
  const body = await send<{ sets: RuleSet[] }>(`/rules/sets?layer_id=${encodeURIComponent(layerId)}`)
  return { sets: body.sets.map(set => ({ ...set, rules: (set.rules ?? []).map(clean) })) }
}
export const createSet = (layerId: string, name: string) => send<RuleSet>('/rules/sets', 'POST', { layer_id: layerId, name }).then(set => ({ ...set, rules: (set.rules ?? []).map(clean) }))
export const getSet = (setId: string) => send<RuleSet>(`/rules/sets/${encodeURIComponent(setId)}`).then(set => ({ ...set, rules: (set.rules ?? []).map(clean) }))
export const saveDraft = (setId: string, body: { expected_revision: number; name: string; rules: AgentRule[] }) => send<RuleSet>(`/rules/sets/${encodeURIComponent(setId)}/draft`, 'PUT', {
  expected_revision: body.expected_revision, name: body.name, rules: body.rules.map(rulePayload),
}).then(set => ({ ...set, rules: (set.rules ?? []).map(clean) }))
export const publishSet = (setId: string, body: { expected_revision: number; version: string; note?: string }) => send<RuleSnapshot>(`/rules/sets/${encodeURIComponent(setId)}/publish`, 'POST', withNote(body)).then(cleanSnapshot)
export const listVersions = async (setId: string) => {
  const body = await send<{ versions: RuleSnapshot[] }>(`/rules/sets/${encodeURIComponent(setId)}/versions`)
  return { versions: (body.versions ?? []).map(cleanSnapshot) }
}
export const getVersion = (setId: string, version: string) => send<RuleSnapshot>(`/rules/sets/${encodeURIComponent(setId)}/versions/${encodeURIComponent(version)}`).then(cleanSnapshot)
export const restoreSet = (setId: string, body: { expected_revision: number; version: string; new_version: string; note?: string }) => send<RuleSnapshot>(`/rules/sets/${encodeURIComponent(setId)}/restore`, 'POST', withNote(body)).then(cleanSnapshot)
export const mergeRules = (query: string) => send<MergedRules>(`/rules/merged?${query}`)

function withNote<T extends { note?: string }>(body: T): T {
  const note = body.note?.trim()
  if (!note) {
    const rest = { ...body }
    delete rest.note
    return rest
  }
  return { ...body, note }
}

function cleanSnapshot(snapshot: RuleSnapshot): RuleSnapshot {
  const note = snapshot.note?.trim()
  return { ...snapshot, note: note || undefined, rules: (snapshot.rules ?? []).map(clean) }
}
