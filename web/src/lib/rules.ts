// SPDX-License-Identifier: AGPL-3.0-only
// Agent rules (ADR-004). Types and calls follow the frozen AR1 contract:
// layers, sets, draft replacement, publish, restore and merged preview.
// Permission checks take the caller's grants; project membership is never a grant.
import { api, RequestFailure, StaleRequestError } from './api.ts'
import { sessionGone } from './authz.ts'

/** The ADR-004 default budget; a workspace may configure its own (GET /rules/budget). */
export const RULES_BUDGET = 12000
export const TLDR_MAX = 300
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
/** A short technical explanation for people (AEON-314). Never sent to agents. */
export interface RuleTLDR {
  en: string
  de?: string
  /** Fingerprint of the text it was written for; the server stamps it when omitted. */
  basis?: string
  /** Derived by the server: the text changed after the explanation was written. */
  check?: boolean
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
  tldr?: RuleTLDR | null
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
  tldr?: RuleTLDR | null
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
  tldr?: RuleTLDR | null
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
  readonly layer?: LayerName
  constructor(status: number, code: string, message: string, actualBytes?: number, maxBytes?: number, layer?: LayerName) {
    super(message)
    this.status = status
    this.code = code
    this.actualBytes = actualBytes
    this.maxBytes = maxBytes
    this.layer = layer
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

/** An explanation as it is stored: trimmed, without the derived check mark. */
export function tldrPayload(tldr: RuleTLDR | null | undefined): RuleTLDR | null {
  if (!tldr || !tldr.en?.trim()) return null
  const out: RuleTLDR = { en: tldr.en.trim() }
  if (tldr.de?.trim()) out.de = tldr.de.trim()
  if (tldr.basis) out.basis = tldr.basis
  return out
}
const tldrKey = (tldr: RuleTLDR | null | undefined) => JSON.stringify(tldrPayload(tldr))
export const tldrEqual = (a: RuleTLDR | null | undefined, b: RuleTLDR | null | undefined) => tldrKey(a) === tldrKey(b)

/** Checks one explanation line; null when it is fine. */
export function validateTLDR(value: string, required: boolean): string | null {
  if (required && !value.trim()) return 'Write a short explanation first.'
  if (utf8Length(value.trim()) > TLDR_MAX) return `Keep it to one line of at most ${TLDR_MAX} bytes.`
  if (/[\r\n\u2028\u2029]/.test(value)) return 'Keep it to one line.'
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
    tldr: raw.tldr?.en ? { ...raw.tldr } : null,
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
  const tldr = tldrPayload(rule.tldr)
  if (tldr) payload.tldr = tldr
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

export interface IdentitySet { has(identity: string): boolean }
export interface CheckGroup { rules: readonly AgentRule[]; held: IdentitySet }

export function canFlip(rule: AgentRule, held: IdentitySet): boolean {
  return rule.strength !== 'locked' && !held.has(rule.identity)
}

/** on when every movable rule is on (or none can move); off when all are off; otherwise mixed. */
export function groupsState(groups: readonly CheckGroup[]): CheckState {
  let any = false
  let movable = 0
  let ons = 0
  let protectedOn = false
  for (const group of groups) {
    for (const rule of group.rules) {
      any = true
      if (!canFlip(rule, group.held)) {
        if (rule.enabled || rule.strength === 'locked') protectedOn = true
        continue
      }
      movable += 1
      if (rule.enabled) ons += 1
    }
  }
  if (!any) return 'off'
  if (!movable) return 'on'
  if (ons === movable) return 'on'
  if (ons === 0 && !protectedOn) return 'off'
  return 'mixed'
}

export function hasMovable(groups: readonly CheckGroup[]): boolean {
  return groups.some(group => group.rules.some(rule => canFlip(rule, group.held)))
}

export function groupState(rules: AgentRule[], held: ReadonlySet<string>): CheckState {
  return groupsState([{ rules, held }])
}

/** A tick turns the group on unless it is already fully on. */
export function tickTarget(state: CheckState): boolean {
  return state !== 'on'
}

export function applyEnabled(rules: AgentRule[], enabled: boolean, held: IdentitySet): AgentRule[] {
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

const COPY_TAIL = / \(copy(?: \d+)?\)$/

/** A set name for a duplicate, still one line, within 128 bytes, and not already used. */
export function copyName(name: string, taken: Iterable<string> = []): string {
  const used = new Set(taken)
  const stem = (name.trim() || 'Set').replace(COPY_TAIL, '').trim() || 'Set'
  const fit = (suffix: string) => {
    const room = 128 - utf8Length(suffix)
    let base = stem
    while (base && utf8Length(base) > room) base = base.slice(0, -1)
    base = base.trimEnd()
    if (!base || utf8Length(base) > room) base = 'Set'
    return `${base}${suffix}`
  }
  for (let n = 1; n <= 99; n++) {
    const candidate = fit(n === 1 ? ' (copy)' : ` (copy ${n})`)
    if (!used.has(candidate)) return candidate
  }
  return fit(' (copy 99)')
}

/** A new rule with its own identity. The wording stays, so agents do not receive a "(copy)" suffix. */
export function duplicateRule(rule: AgentRule, taken: Iterable<string>): AgentRule {
  const copy = normalizeRule(rule)
  return {
    ...copy,
    identity: identityFromText(`${copy.identity}-copy`, new Set(taken)),
    text: copy.text,
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
  if (rule.tldr && (rule.tldr.en?.trim() || rule.tldr.de?.trim())) {
    const issue = validateTLDR(rule.tldr.en ?? '', true) ?? validateTLDR(rule.tldr.de ?? '', false)
    if (issue) return `Explanation: ${issue}`
  }
  for (const role of rule.roles ?? []) if (!ROLES.includes(role)) return 'Unknown role.'
  for (const harness of rule.harnesses ?? []) if (!HARNESSES.includes(harness)) return 'Unknown harness.'
  return null
}

export interface DraftCheck { error: string | null; warning: string | null }

/** Accessible name for one rule switch. The set distinguishes copies that keep the same wording. */
export function ruleSwitchLabel(text: string, setName: string, enabled: boolean, empty = 'Untitled rule'): string {
  const name = text.trim() || empty
  const set = setName.trim()
  const state = enabled ? 'on' : 'off'
  return set ? `${name} in ${set} is ${state}` : `${name} is ${state}`
}

function duplicateTextWarning(rules: AgentRule[]): string | null {
  const seen = new Set<string>()
  for (const rule of rules) {
    if (rule.text.trim() === '') continue
    if (seen.has(rule.text)) return 'This rule appears twice in this set; agents would get it twice.'
    seen.add(rule.text)
  }
  return null
}

export function validateDraft(name: string, rules: AgentRule[]): DraftCheck {
  let error: string | null = null
  if (oneLine(name, 128, true)) error = 'The set name is one line, up to 128 bytes.'
  else if (rules.length > MAX_RULES) error = 'A set holds at most 100 rules.'
  else {
    const seen = new Set<string>()
    for (const rule of rules) {
      const issue = validateRule(rule, seen)
      if (issue) { error = issue; break }
      seen.add(rule.identity)
    }
  }
  // Identical wording warns and still saves: agents would receive that line twice.
  return { error, warning: duplicateTextWarning(rules) }
}

/** One entry of a publication's approval. `about` says what changes: the rule
 *  itself (absent), its TL;DR, the set's TL;DR or the set's name. */
export interface RuleChange {
  kind: 'added' | 'removed' | 'changed'
  label: string
  about?: 'tldr' | 'set-tldr' | 'name'
  /** TL;DR entries: the German line, when there is one. */
  de?: string
  /** A rule's TL;DR: the rule it explains. */
  rule?: string
  /** A TL;DR whose words stay; it was confirmed for the rule's new wording. */
  confirmed?: boolean
}
function tldrChange(before: RuleTLDR | null | undefined, after: RuleTLDR | null | undefined, about: 'tldr' | 'set-tldr', rule?: string): RuleChange | null {
  const old = tldrPayload(before)
  const next = tldrPayload(after)
  if (tldrKey(old) === tldrKey(next)) return null
  const shown = (next ?? old)!
  const change: RuleChange = { kind: !old ? 'added' : !next ? 'removed' : 'changed', label: shown.en, about }
  if (shown.de) change.de = shown.de
  if (rule) change.rule = rule
  if (old && next && old.en === next.en && old.de === next.de) change.confirmed = true
  return change
}
const withoutTLDR = (rules: AgentRule[]) => rules.map(rule => ({ ...rule, tldr: null }))

/** Rule changes, each followed by its own TL;DR change. A rule's TL;DR is an
 *  entry of its own, never folded into the rule; one that leaves with its rule
 *  goes with the removal. */
export function diffRules(before: AgentRule[], after: AgentRule[]): RuleChange[] {
  const prior = new Map(before.map(rule => [rule.identity, rule]))
  const next = new Map(after.map(rule => [rule.identity, rule]))
  const changes: RuleChange[] = []
  for (const rule of after) {
    const old = prior.get(rule.identity)
    const label = rule.text || rule.identity
    if (!old) changes.push({ kind: 'added', label })
    else if (!rulesEqual(withoutTLDR([old]), withoutTLDR([rule]))) changes.push({ kind: 'changed', label })
    const tldr = tldrChange(old?.tldr, rule.tldr, 'tldr', label)
    if (tldr) changes.push(tldr)
  }
  for (const rule of before) if (!next.has(rule.identity)) changes.push({ kind: 'removed', label: rule.text || rule.identity })
  return changes
}

/** Everything a publication changes in one set: its name, its TL;DR, then its rules. */
export function diffSet(before: Pick<RuleSnapshot, 'name' | 'rules' | 'tldr'> | null | undefined, after: Pick<RuleSet, 'name' | 'rules' | 'tldr'>): RuleChange[] {
  const changes: RuleChange[] = []
  if (before && before.name !== after.name) changes.push({ kind: 'changed', label: after.name, about: 'name' })
  const tldr = tldrChange(before?.tldr, after.tldr, 'set-tldr')
  if (tldr) changes.push(tldr)
  return [...changes, ...diffRules(before?.rules ?? [], after.rules)]
}

/** Reset only when an upstream original was actually supplied. The contract has no original text. */
export function resetAvailability(rule: AgentRule, original?: AgentRule | null): { available: true; original: AgentRule } | { available: false; reason: string; show: boolean } {
  if (original && (original.identity === rule.source.identity || original.identity === rule.identity)) return { available: true, original: normalizeRule(original) }
  if (rule.source.identity && rule.source.edited_here) {
    return { available: false, show: true, reason: 'Reset needs the original template wording, which this rule does not store.' }
  }
  return { available: false, show: false, reason: '' }
}

/** Restores the supplied original's wording onto this rule and clears the local edit. */
export function resetRule(rule: AgentRule, original: AgentRule): AgentRule | null {
  const ready = resetAvailability(rule, original)
  if (!ready.available) return null
  return normalizeRule({
    ...rule,
    text: ready.original.text,
    why: ready.original.why,
    details: ready.original.details,
    strength: ready.original.strength,
    enabled: ready.original.enabled,
    expires_at: ready.original.expires_at,
    roles: ready.original.roles,
    harnesses: ready.original.harnesses,
    source: { ...rule.source, edited_here: false },
  })
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
  if (error.code === 'rules_budget_exceeded') {
    // Without a size the file belongs to someone else; the server's words say so.
    if (error.actualBytes === undefined) return error.message || 'A session file for another person or agent would exceed the limit.'
    const fmt = (n: number) => n.toLocaleString('en-US')
    if (error.layer) return `${LAYER_LABEL[error.layer]} rules would take ${fmt(error.actualBytes)} bytes of the session file. Their cap is ${fmt(error.maxBytes ?? 0)}.`
    return `The merged file is ${fmt(error.actualBytes)} bytes. The limit is ${fmt(error.maxBytes ?? RULES_BUDGET)}.`
  }
  // The server words it per operation; only a batch publication may be repeated safely.
  if (error.code === 'outcome_unknown') return error.message || 'The result is unknown. Reload to see the current state before trying again.'
  if (error.code === 'busy') return 'The rules are busy right now. Nothing was changed; try again in a moment.'
  if (error.code === 'ambiguous_identity') return 'Two rules of the same rank share an identity, so the merge stops.'
  if (error.code === 'forbidden') return 'You do not have permission for that.'
  if (error.code === 'not_found') return 'That rules record is no longer there.'
  if (error.code === 'invalid_rule' || error.code === 'invalid_scope' || error.code === 'invalid_version' || error.code === 'invalid_tldr' || error.code === 'invalid_budget' || error.code === 'unknown_rule') return error.message || 'The rules request was not accepted.'
  return error.message || 'The rules request failed.'
}

async function parseError(response: Response): Promise<RulesError> {
  const body = await response.json().catch(() => ({})) as { error?: string; code?: string; actual_bytes?: number; max_bytes?: number; layer?: LayerName }
  return new RulesError(response.status, body.code || '', body.error || `Request failed (${response.status})`, body.actual_bytes, body.max_bytes, body.layer)
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
const cleanSet = (set: RuleSet): RuleSet => ({ ...set, rules: (set.rules ?? []).map(clean) })
/** Writes explanations only (AEON-314). set: undefined keeps, null removes; rules: identity → text or null. */
export const saveTldrs = (setId: string, body: { expected_revision: number; set?: { en: string; de?: string } | null; rules?: Record<string, { en: string; de?: string } | null> }) =>
  send<RuleSet>(`/rules/sets/${encodeURIComponent(setId)}/tldr`, 'PUT', body).then(cleanSet)
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

// ---------- Budget and the explained file (AEON-314) ----------
export type LayerBytes = Partial<Record<LayerName, number>>
export interface RuleBudget { max_bytes: number; layer_max_bytes: LayerBytes }
export interface RuleBudgetView extends RuleBudget { default_bytes: number; min_bytes: number; ceiling_bytes: number; min_layer_bytes: number }
export const DEFAULT_BUDGET: RuleBudgetView = { max_bytes: RULES_BUDGET, layer_max_bytes: {}, default_bytes: RULES_BUDGET, min_bytes: 2000, ceiling_bytes: 12000, min_layer_bytes: 500 }
export const getBudget = () => send<RuleBudgetView>('/rules/budget').then(view => ({ ...view, layer_max_bytes: view.layer_max_bytes ?? {} }))
export const putBudget = (budget: RuleBudget) => send<RuleBudgetView>('/rules/budget', 'PUT', budget).then(view => ({ ...view, layer_max_bytes: view.layer_max_bytes ?? {} }))

export interface ExplainedSet { set_id: string; name: string; scope: RuleScope; version: string; tldr?: RuleTLDR | null; bytes: number }
export interface ExplainedRule { identity: string; text: string; line: string; set_id: string; layer: LayerName; strength: 'normal' | 'locked'; tldr?: RuleTLDR | null; bytes: number }
export interface ExplainedRules {
  context: MergedRules['context']
  version: string
  sha256: string
  body: string
  byte_size: number
  budget: RuleBudget
  usage: LayerBytes
  sets: ExplainedSet[]
  rules: ExplainedRule[]
  problem?: { code: string; error: string; actual_bytes?: number; max_bytes?: number; layer?: LayerName } | null
}
export const explainRules = (query: string) => send<ExplainedRules>(`/rules/explained?${query}`).then(out => ({ ...out, budget: { ...out.budget, layer_max_bytes: out.budget.layer_max_bytes ?? {} }, usage: out.usage ?? {} }))

/** A byte count people read: "840 B", "7.1 kB", "12 kB". */
export function byteSize(n: number): string {
  if (n < 1000) return `${n} B`
  return `${(n / 1000).toLocaleString('en-US', { maximumFractionDigits: 1, minimumFractionDigits: n % 1000 >= 50 ? 1 : 0 })} kB`
}
/** "7.1 of 8 kB" when both read in kB, otherwise "840 B of 8 kB". */
function ofLimit(used: number, limit: number): string {
  const a = byteSize(used)
  const b = byteSize(limit)
  return a.endsWith(' kB') && b.endsWith(' kB') ? `${a.slice(0, -3)} of ${b}` : `${a} of ${b}`
}
/** Per-layer use against the budget, e.g. "Company 7.1 of 8 kB · Project 2.3 kB · total 9.4 of 12 kB". */
export function budgetParts(usage: LayerBytes, total: number, budget: RuleBudget): { label: string; over: boolean }[] {
  const parts: { label: string; over: boolean }[] = []
  for (const layer of LAYERS) {
    const used = usage[layer] ?? 0
    const cap = budget.layer_max_bytes[layer] ?? 0
    if (!used && !cap) continue
    parts.push({ label: cap ? `${LAYER_LABEL[layer]} ${ofLimit(used, cap)}` : `${LAYER_LABEL[layer]} ${byteSize(used)}`, over: !!cap && used > cap })
  }
  parts.push({ label: `total ${ofLimit(total, budget.max_bytes)}`, over: total > budget.max_bytes })
  return parts
}
export const budgetLine = (usage: LayerBytes, total: number, budget: RuleBudget) => budgetParts(usage, total, budget).map(part => part.label).join(' · ')
/** True when the file or one layer is over its limit. */
export function overBudget(usage: LayerBytes, total: number, budget: RuleBudget): boolean {
  return total > budget.max_bytes || LAYERS.some(layer => (budget.layer_max_bytes[layer] ?? 0) > 0 && (usage[layer] ?? 0) > (budget.layer_max_bytes[layer] ?? 0))
}

/** One entry of a batch publication; version is 'auto' or an explicit calendar version. */
export interface BatchItem { set_id: string; expected_revision: number; version: string }
export interface BatchResult { batch_id: string; versions: RuleSnapshot[]; max_bytes: number }
/** Publishes several sets under one person approval (POST /rules/publish). All or nothing. */
export const publishSets = (items: BatchItem[], note?: string) => send<BatchResult>('/rules/publish', 'POST', withNote({ items, note }))
  .then(result => ({ ...result, versions: (result.versions ?? []).map(cleanSnapshot) }))

// ---------- Set state: what is live and what waits ----------
export type SetState = 'new' | 'changed' | 'live'
const byIdentity = (rules: AgentRule[]) => [...rules].sort((a, b) => (a.identity < b.identity ? -1 : a.identity > b.identity ? 1 : 0))
/** new: never published; changed: the saved draft differs from the live version; live: they match. */
export function setState(set: Pick<RuleSet, 'name' | 'rules' | 'published_version' | 'tldr'>, live: Pick<RuleSnapshot, 'name' | 'rules' | 'tldr'> | null | undefined): SetState {
  if (!set.published_version) return 'new'
  if (!live) return 'live'
  return live.name === set.name && tldrEqual(live.tldr, set.tldr) && rulesEqual(byIdentity(live.rules), byIdentity(set.rules)) ? 'live' : 'changed'
}

// ---------- Projected merge: the file size a publication would produce ----------
// The same rules as the server's Merge (internal/rules/merge.go): rank, then set id;
// role and harness selectors; expired rules drop out; the highest rank wins an
// identity; only enabled rules are rendered. The server stays the authority (422).
export interface MergeInput { id: string; scope: RuleScope; rules: AgentRule[] }
export interface MergeContext { projectId: string; personId: string; role: RoleName; harness: HarnessName; agentId?: string; taskId?: string }
// The server's merged file starts with a fixed 22-byte heading line and a blank
// line (merge.go); every rendered rule is "- [identity] text" plus a newline.
export const MERGE_HEADING_BYTES = 22
interface Projected { rule: AgentRule; layer: LayerName }
function projectedWithLayer(sets: MergeInput[], ctx: MergeContext, now: Date): Projected[] {
  const context: RuleContext = { projectId: ctx.projectId, personId: ctx.personId, agentId: ctx.agentId ?? '', role: ctx.role, harness: ctx.harness, taskId: ctx.taskId ?? '' }
  const ordered = sets.filter(set => scopeMatches(set.scope, context))
    .sort((a, b) => scopeRank(a.scope) - scopeRank(b.scope) || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0))
  const chosen = new Map<string, Projected>()
  for (const set of ordered) {
    for (const rule of set.rules) {
      if ((rule.roles?.length && !rule.roles.includes(ctx.role)) || (rule.harnesses?.length && !rule.harnesses.includes(ctx.harness))) continue
      if (rule.expires_at && new Date(rule.expires_at).getTime() <= now.getTime()) continue
      if (!chosen.has(rule.identity)) chosen.set(rule.identity, { rule, layer: set.scope.layer })
    }
  }
  return [...chosen.keys()].sort().map(key => chosen.get(key)!).filter(item => item.rule.enabled || item.rule.strength === 'locked')
}
export function projectedRules(sets: MergeInput[], ctx: MergeContext, now = new Date()): AgentRule[] {
  return projectedWithLayer(sets, ctx, now).map(item => item.rule)
}
const lineBytes = (rule: AgentRule) => utf8Length(`- [${rule.identity}] ${rule.text}\n`)
export function projectedBytes(sets: MergeInput[], ctx: MergeContext, now = new Date()): number {
  return projectedUsage(sets, ctx, now).bytes
}
/** The file's size and each layer's share of it, as the server counts them. */
export function projectedUsage(sets: MergeInput[], ctx: MergeContext, now = new Date()): { bytes: number; usage: LayerBytes } {
  const usage: LayerBytes = {}
  let bytes = MERGE_HEADING_BYTES
  for (const { rule, layer } of projectedWithLayer(sets, ctx, now)) {
    const n = lineBytes(rule)
    bytes += n
    usage[layer] = (usage[layer] ?? 0) + n
  }
  return { bytes, usage }
}
/** How far a file is over its limits (0 when it fits): the larger of the total's and any layer's excess. */
export function budgetExcess(bytes: number, usage: LayerBytes, budget: RuleBudget): number {
  let excess = Math.max(0, bytes - budget.max_bytes)
  for (const layer of LAYERS) {
    const cap = budget.layer_max_bytes[layer] ?? 0
    if (cap > 0) excess = Math.max(excess, (usage[layer] ?? 0) - cap)
  }
  return excess
}
/** The file to show over every role and harness for one project and person: the one furthest over a limit, otherwise the largest. */
export function largestProjected(sets: MergeInput[], projectId: string, personId: string, now = new Date(), budget: RuleBudget = DEFAULT_BUDGET): { bytes: number; usage: LayerBytes; role: RoleName; harness: HarnessName } {
  let best = { bytes: 0, usage: {} as LayerBytes, role: ROLES[0] as RoleName, harness: HARNESSES[0] as HarnessName }
  let bestExcess = 0
  for (const role of ROLES) for (const harness of HARNESSES) {
    const { bytes, usage } = projectedUsage(sets, { projectId, personId, role, harness }, now)
    const excess = budgetExcess(bytes, usage, budget)
    if (excess > bestExcess || (excess === bestExcess && bytes > best.bytes)) { best = { bytes, usage, role, harness }; bestExcess = excess }
  }
  return best
}

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

// Person-operated bulk draft import. The file is data (aeon.rules-draft-import.v1).
// It is parsed locally, checked in full, then written only through the rules APIs.
export const DRAFT_IMPORT_SCHEMA = 'aeon.rules-draft-import.v1'
export const IMPORT_MAX_BYTES = 2 * 1024 * 1024
export const IMPORT_MAX_SETS = 20
export const IMPORT_MAX_RULES = 100

/** What the import does with one set: create it, replace its saved draft, or skip it (identical). */
export type ImportAction = 'create' | 'update' | 'skip'
export interface ImportCounts { added: number; changed: number; unchanged: number; removed: number }
export interface DraftImportSet {
  name: string
  rules: AgentRule[]
  /** Filled by the review against the workspace's current sets. */
  action?: ImportAction
  counts?: ImportCounts
  existing?: { id: string; revision: number; rules: AgentRule[] }
}
export interface DraftImportLayer { scope: RuleScope; sets: DraftImportSet[] }
export interface DraftImportPlan { tenantId: string; layers: DraftImportLayer[] }
/** rulesSaved is true when the draft save was confirmed, false when the server rejected it, and null when the save reply was lost. */
export interface ImportConfirmed { scope: RuleScope; layer: RuleLayer; set: RuleSet; rulesSaved: boolean | null }
export type ImportOutcome =
  | { status: 'imported'; confirmed: ImportConfirmed[]; message: string }
  | { status: 'rejected'; confirmed: ImportConfirmed[]; message: string }
  | { status: 'partial'; confirmed: ImportConfirmed[]; message: string }
  | { status: 'uncertain'; confirmed: ImportConfirmed[]; message: string }
/** One set's outcome, for a truthful per-set report after a partial or uncertain import. */
export interface ImportSetResult { scope: RuleScope; name: string; action: ImportAction; status: 'saved' | 'unchanged' | 'failed' | 'unknown' | 'not-started'; reason?: string }
export type ImportReport = ImportOutcome & { results: ImportSetResult[] }
/** A set the workspace already holds, as the import compares against it. */
export interface ExistingSet { id: string; name: string; revision: number; rules: AgentRule[] }

const IMPORT_KEYS = ['schema', 'tenant_id', 'layers']
const SCOPE_KEYS = ['layer', 'project_id', 'owner_id', 'agent_id', 'role', 'task_id']
const SET_KEYS = ['name', 'rules']
const RULE_KEYS = ['identity', 'text', 'why', 'details', 'strength', 'enabled', 'expires_at', 'roles', 'harnesses', 'source']
const SOURCE_KEYS = ['reference', 'revision', 'identity', 'edited_here']
const INSTANT = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$/

export function scopeKey(scope: RuleScope): string {
  const value = scopePayload(scope)
  return [value.layer, value.project_id ?? '', value.owner_id ?? '', value.agent_id ?? '', value.role ?? '', value.task_id ?? ''].join('\u0000')
}

export function scopeLabel(scope: RuleScope): string {
  if (scope.layer === 'company') return 'Company'
  if (scope.layer === 'project') return `Project ${scope.project_id ?? ''}`
  if (scope.layer === 'person') return `Person ${scope.owner_id ?? ''}`
  if (scope.role) return `Agent role ${ROLE_LABEL[scope.role] ?? scope.role}`
  if (scope.task_id) return `Agent task ${scope.task_id}`
  if (scope.agent_id) return `Named agent ${scope.agent_id}`
  return 'Agent'
}

export function importBlock(caller: Caller | null): string | null {
  if (!caller) return 'Sign in to import drafts.'
  if (caller.kind !== 'person') return 'Only a person can import drafts.'
  return null
}

export function replyUncertain(error: unknown): boolean {
  if (error instanceof RequestFailure || error instanceof StaleRequestError || error instanceof SyntaxError) return true
  // A 5xx can arrive after the write committed, including a reverse proxy 502.
  // busy means the server rolled back before COMMIT: certainly nothing changed.
  return error instanceof RulesError && error.status >= 500 && error.status <= 599 && error.code !== 'busy'
}

function plainRecord(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value)
}

function unknownKey(value: Record<string, unknown>, allowed: string[]): string | null {
  for (const key of Object.keys(value)) if (!allowed.includes(key)) return key
  return null
}

function validInstant(value: string): boolean {
  const match = INSTANT.exec(value)
  if (!match) return false
  const year = Number(match[1])
  const month = Number(match[2])
  const day = Number(match[3])
  const hour = Number(match[4])
  const minute = Number(match[5])
  const second = Number(match[6])
  if (month < 1 || month > 12 || hour > 23 || minute > 59 || second > 59) return false
  const date = new Date(Date.UTC(year, month - 1, day))
  if (date.getUTCFullYear() !== year || date.getUTCMonth() + 1 !== month || date.getUTCDate() !== day) return false
  if (match[8] !== 'Z') {
    const offsetHour = Number(match[8].slice(1, 3))
    const offsetMinute = Number(match[8].slice(4, 6))
    if (offsetHour > 23 || offsetMinute > 59) return false
  }
  return true
}

function scopeIssue(scope: RuleScope): string | null {
  if (scope.layer === 'company') {
    if (scope.project_id || scope.owner_id || scope.agent_id || scope.role || scope.task_id) return 'Company scope stands alone.'
    return null
  }
  if (scope.layer === 'project') {
    if (!scope.project_id) return 'Project scope needs a project id.'
    if (scope.owner_id || scope.agent_id || scope.role || scope.task_id) return 'Project scope only takes a project id.'
    return null
  }
  if (scope.layer === 'person') {
    if (!scope.owner_id) return 'Person scope needs the person’s id.'
    if (scope.project_id || scope.agent_id || scope.role || scope.task_id) return 'Person scope only takes that person’s id.'
    return null
  }
  const roleOnly = !!scope.role && !scope.agent_id && !scope.owner_id && !scope.project_id && !scope.task_id
  const named = !scope.role && !!scope.owner_id && !!scope.agent_id && !scope.project_id && !scope.task_id
  const task = !scope.role && !!scope.owner_id && !!scope.agent_id && !!scope.project_id && !!scope.task_id
  if (roleOnly || named || task) return null
  if ((scope.task_id && !scope.project_id) || (scope.project_id && !scope.task_id)) return 'An agent task needs a project id and a task id.'
  return 'Agent scope must be a role, a named agent, or one task.'
}

function parseScope(raw: unknown): { scope: RuleScope } | { error: string } {
  if (!plainRecord(raw)) return { error: 'Each layer needs a scope object.' }
  const extra = unknownKey(raw, SCOPE_KEYS)
  if (extra) return { error: `Scope has an unknown field “${extra}”.` }
  if (typeof raw.layer !== 'string' || !LAYERS.includes(raw.layer as LayerName)) return { error: 'The scope layer is not company, project, person or agent.' }
  const scope: RuleScope = { layer: raw.layer as LayerName }
  for (const key of ['project_id', 'owner_id', 'agent_id', 'task_id'] as const) {
    const id = raw[key]
    if (id === undefined) continue
    if (typeof id !== 'string' || !isUuid(id)) return { error: 'Scope ids must be lowercase UUIDs.' }
    scope[key] = id
  }
  if (raw.role !== undefined) {
    if (typeof raw.role !== 'string' || !ROLES.includes(raw.role as RoleName)) return { error: 'Unknown role in scope.' }
    scope.role = raw.role as RoleName
  }
  const issue = scopeIssue(scope)
  if (issue) return { error: issue }
  return { scope }
}

function parseStringList<T extends string>(value: unknown, allowed: readonly T[], label: string): { values: T[] } | { error: string } {
  if (value === undefined) return { values: [] }
  if (!Array.isArray(value)) return { error: `The ${label} list must be an array.` }
  const values: T[] = []
  for (const item of value) {
    if (typeof item !== 'string' || !allowed.includes(item as T)) return { error: `Unknown ${label}.` }
    if (values.includes(item as T)) return { error: `Repeated ${label}.` }
    values.push(item as T)
  }
  return { values }
}

function parseImportRule(raw: unknown, seen: ReadonlySet<string>): { rule: AgentRule } | { error: string } {
  if (!plainRecord(raw)) return { error: 'A rule must be an object.' }
  const extra = unknownKey(raw, RULE_KEYS)
  if (extra) return { error: `A rule has an unknown field “${extra}”.` }
  if (typeof raw.identity !== 'string' || typeof raw.text !== 'string' || typeof raw.why !== 'string') return { error: 'Each rule needs identity, text and why strings.' }
  if (raw.strength !== 'normal' && raw.strength !== 'locked') return { error: 'Strength is normal or locked.' }
  if (typeof raw.enabled !== 'boolean') return { error: 'Each rule needs enabled true or false.' }
  if (raw.details !== undefined && typeof raw.details !== 'string') return { error: 'Details must be text.' }
  let expires: string | null = null
  if (raw.expires_at !== undefined && raw.expires_at !== null) {
    if (typeof raw.expires_at !== 'string' || !validInstant(raw.expires_at)) return { error: 'Expiry must be an RFC3339 instant or empty.' }
    expires = raw.expires_at
  }
  const roles = parseStringList(raw.roles, ROLES, 'role')
  if ('error' in roles) return roles
  const harnesses = parseStringList(raw.harnesses, HARNESSES, 'harness')
  if ('error' in harnesses) return harnesses
  if (!plainRecord(raw.source)) return { error: 'Each rule needs a source object.' }
  const sourceExtra = unknownKey(raw.source, SOURCE_KEYS)
  if (sourceExtra) return { error: `A rule source has an unknown field “${sourceExtra}”.` }
  if (typeof raw.source.reference !== 'string') return { error: 'Each rule needs a source reference.' }
  if (raw.source.revision !== undefined && typeof raw.source.revision !== 'string') return { error: 'The source revision must be text.' }
  if (raw.source.identity !== undefined && typeof raw.source.identity !== 'string') return { error: 'The upstream identity must be text.' }
  if (raw.source.edited_here !== undefined && typeof raw.source.edited_here !== 'boolean') return { error: 'edited_here must be true or false.' }
  const rule: AgentRule = {
    identity: raw.identity,
    text: raw.text,
    why: raw.why,
    details: typeof raw.details === 'string' ? raw.details : '',
    strength: raw.strength,
    enabled: raw.enabled,
    expires_at: expires,
    roles: roles.values,
    harnesses: harnesses.values,
    source: {
      reference: raw.source.reference,
      revision: typeof raw.source.revision === 'string' ? raw.source.revision : '',
      identity: typeof raw.source.identity === 'string' ? raw.source.identity : '',
      edited_here: raw.source.edited_here === true,
    },
  }
  const issue = validateRule(rule, seen)
  if (issue) return { error: issue }
  return { rule: normalizeRule(rule) }
}

function reviewDraftImport(plan: DraftImportPlan, tenantId: string, caller: Caller | null): string | null {
  const blocked = importBlock(caller)
  if (blocked) return blocked
  if (plan.tenantId !== tenantId) return 'This file is for a different workspace.'
  if (plan.layers.length === 0) return 'The file has no draft sets.'
  const seenScopes = new Set<string>()
  let sets = 0
  let rules = 0
  for (const layer of plan.layers) {
    const key = scopeKey(layer.scope)
    if (seenScopes.has(key)) return `${scopeLabel(layer.scope)} is listed more than once.`
    seenScopes.add(key)
    const block = writeBlock(caller, layer.scope)
    if (block) return block
    const taken = new Set<string>()
    const seenIds = new Map<string, string>()
    for (const set of layer.sets) {
      if (taken.has(set.name)) return `“${set.name}” is listed twice for ${scopeLabel(layer.scope)}.`
      taken.add(set.name)
      const local = new Set<string>()
      for (const rule of set.rules) {
        if (local.has(rule.identity)) return `“${rule.identity}” is already used in this set.`
        const earlier = seenIds.get(rule.identity)
        if (earlier) return `“${rule.identity}” is already used in “${earlier}” for ${scopeLabel(layer.scope)}. Two sets in one scope cannot share an identity.`
        local.add(rule.identity)
        seenIds.set(rule.identity, set.name)
      }
      sets += 1
      rules += set.rules.length
    }
  }
  if (sets === 0) return 'The file has no draft sets.'
  if (sets > IMPORT_MAX_SETS) return 'An import holds at most 20 sets.'
  if (rules > IMPORT_MAX_RULES) return 'An import holds at most 100 rules.'
  return null
}

function countChanges(before: AgentRule[], after: AgentRule[]): ImportCounts {
  const prior = new Map(before.map(rule => [rule.identity, rule]))
  const next = new Set(after.map(rule => rule.identity))
  const counts: ImportCounts = { added: 0, changed: 0, unchanged: 0, removed: 0 }
  for (const rule of after) {
    const old = prior.get(rule.identity)
    if (!old) counts.added += 1
    else if (rulesEqual([old], [rule])) counts.unchanged += 1
    else counts.changed += 1
  }
  for (const rule of before) if (!next.has(rule.identity)) counts.removed += 1
  return counts
}

/** Compares every set in the file with the set of the same name and scope, if any. */
export function annotateImport(plan: DraftImportPlan, existing: { scope: RuleScope; sets: ExistingSet[] }[]): DraftImportPlan {
  return {
    tenantId: plan.tenantId,
    layers: plan.layers.map(layer => {
      const present = existing.filter(row => scopeKey(row.scope) === scopeKey(layer.scope)).flatMap(row => row.sets)
      return {
        scope: layer.scope,
        sets: layer.sets.map(set => {
          const match = present.find(item => item.name === set.name)
          // An import never erases people's explanations: a rule without one
          // keeps the explanation its stored twin has (AEON-314).
          const kept = new Map((match?.rules ?? []).map(rule => [rule.identity, rule.tldr]))
          const name = set.name
          const rules = set.rules.map(rule => rule.tldr?.en || !kept.get(rule.identity) ? rule : { ...rule, tldr: kept.get(rule.identity) })
          if (!match) return { name, rules, action: 'create' as const, counts: { added: rules.length, changed: 0, unchanged: 0, removed: 0 } }
          const counts = countChanges(match.rules, rules)
          const same = counts.added === 0 && counts.changed === 0 && counts.removed === 0
          return { name, rules, action: same ? 'skip' as const : 'update' as const, counts, existing: { id: match.id, revision: match.revision, rules: match.rules } }
        }),
      }
    }),
  }
}
const importSets = (plan: DraftImportPlan) => plan.layers.flatMap(layer => layer.sets.map(set => ({ scope: layer.scope, set })))
/** Sets the import writes: new ones and changed ones. */
export const importWrites = (plan: DraftImportPlan) => importSets(plan).filter(item => item.set.action !== 'skip').length

/** Project ids named by the scopes of a draft file, so their permissions can be loaded before review. */
export function draftImportProjects(text: string): string[] {
  try {
    const raw = JSON.parse(text) as { layers?: { scope?: { project_id?: unknown } }[] }
    const ids = (Array.isArray(raw?.layers) ? raw.layers : []).map(layer => layer?.scope?.project_id).filter((id): id is string => typeof id === 'string' && isUuid(id))
    return [...new Set(ids)]
  } catch { return [] }
}

export function parseDraftImport(
  text: string,
  byteLength: number,
  tenantId: string,
  caller: Caller | null,
  existing: { scope: RuleScope; sets: ExistingSet[] }[] = [],
): { plan: DraftImportPlan } | { error: string } {
  if (byteLength > IMPORT_MAX_BYTES || utf8Length(text) > IMPORT_MAX_BYTES) return { error: 'The file must be 2 MiB or smaller.' }
  let raw: unknown
  try { raw = JSON.parse(text) } catch { return { error: 'The file is not JSON.' } }
  if (!plainRecord(raw)) return { error: 'The file must be a JSON object.' }
  const extra = unknownKey(raw, IMPORT_KEYS)
  if (extra) return { error: `The file has an unknown field “${extra}”.` }
  if (raw.schema !== DRAFT_IMPORT_SCHEMA) return { error: 'The file must use schema aeon.rules-draft-import.v1.' }
  if (typeof raw.tenant_id !== 'string') return { error: 'The file needs a tenant_id string.' }
  if (raw.tenant_id !== tenantId) return { error: 'This file is for a different workspace.' }
  if (!Array.isArray(raw.layers)) return { error: 'The file needs a layers array.' }
  const layers: DraftImportLayer[] = []
  for (const item of raw.layers) {
    if (!plainRecord(item)) return { error: 'Each layer must be an object.' }
    const layerExtra = unknownKey(item, ['scope', 'sets'])
    if (layerExtra) return { error: `A layer has an unknown field “${layerExtra}”.` }
    const scope = parseScope(item.scope)
    if ('error' in scope) return scope
    if (!Array.isArray(item.sets) || item.sets.length === 0) return { error: 'Each scope needs at least one set.' }
    const sets: DraftImportSet[] = []
    for (const entry of item.sets) {
      if (!plainRecord(entry)) return { error: 'Each set must be an object.' }
      const setExtra = unknownKey(entry, SET_KEYS)
      if (setExtra) return { error: `A set has an unknown field “${setExtra}”.` }
      if (typeof entry.name !== 'string') return { error: 'Each set needs a name.' }
      if (!Array.isArray(entry.rules)) return { error: `“${entry.name}” needs a rules array.` }
      const seen = new Set<string>()
      const rules: AgentRule[] = []
      for (const rule of entry.rules) {
        const parsed = parseImportRule(rule, seen)
        if ('error' in parsed) return { error: `“${entry.name}”: ${parsed.error}` }
        seen.add(parsed.rule.identity)
        rules.push(parsed.rule)
      }
      const issue = validateDraft(entry.name, rules)
      if (issue.error) return { error: issue.error }
      sets.push({ name: entry.name, rules })
    }
    layers.push({ scope: scope.scope, sets })
  }
  const plan: DraftImportPlan = { tenantId: raw.tenant_id, layers }
  const review = reviewDraftImport(plan, tenantId, caller)
  if (review) return { error: review }
  return { plan: annotateImport(plan, existing) }
}

export interface ImportIO {
  listLayers: () => Promise<{ layers: RuleLayer[] }>
  listSets: (layerId: string) => Promise<{ sets: RuleSet[] }>
  createLayer: (scope: RuleScope) => Promise<RuleLayer>
  createSet: (layerId: string, name: string) => Promise<RuleSet>
  saveDraft: (setId: string, body: { expected_revision: number; name: string; rules: AgentRule[] }) => Promise<RuleSet>
}

function rejected(message: string): ImportOutcome {
  return { status: 'rejected', confirmed: [], message }
}

function confirmedLine(confirmed: ImportConfirmed[]): string {
  if (!confirmed.length) return ''
  const parts = confirmed.map(item => {
    const rules = item.rulesSaved === true ? '' : item.rulesSaved === false ? ' (rules not saved)' : ' (rules outcome unknown)'
    return `“${item.set.name}” ${item.set.id} revision ${item.set.revision}${rules}`
  })
  return `Confirmed: ${parts.join('; ')}. `
}

export async function runDraftImport(
  plan: DraftImportPlan,
  tenantId: string,
  caller: Caller | null,
  io: ImportIO = { listLayers, listSets, createLayer, createSet, saveDraft },
): Promise<ImportReport> {
  // Every set starts as not started; each step records what the server confirmed.
  const results: ImportSetResult[] = importSets(plan).map(({ scope, set }) => ({ scope, name: set.name, action: set.action ?? 'create', status: 'not-started' }))
  const resultOf = (scope: RuleScope, name: string) => results.find(item => item.name === name && scopeKey(item.scope) === scopeKey(scope))!
  const done = (outcome: ImportOutcome): ImportReport => ({ ...outcome, results })
  const refuse = (message: string) => done(rejected(message))
  const blocked = importBlock(caller)
  if (blocked) return refuse(blocked)
  let listed: { layer: RuleLayer; sets: RuleSet[] }[]
  try {
    const { layers } = await io.listLayers()
    listed = []
    for (const layer of layers) {
      const { sets } = await io.listSets(layer.id)
      listed.push({ layer, sets })
    }
  } catch (cause) {
    if (replyUncertain(cause)) return refuse('Could not read the current sets, so nothing was imported.')
    return refuse(rulesMessage(cause))
  }
  const review = reviewDraftImport(plan, tenantId, caller)
  if (review) return refuse(review)
  // The workspace must still look as it did when the file was reviewed; a set
  // added or saved elsewhere since then stops the import before any write.
  const fresh = annotateImport(plan, listed.map(row => ({ scope: row.layer.scope, sets: row.sets })))
  for (const [index, { scope, set }] of importSets(fresh).entries()) {
    const reviewed = importSets(plan)[index]!.set
    const action = reviewed.action ?? 'create'
    if (set.action === 'skip' && action === 'skip') continue
    if ((action === 'create') !== (set.action === 'create') || (action !== 'create' && reviewed.existing?.revision !== set.existing?.revision)) {
      return refuse(`“${set.name}” in ${scopeLabel(scope)} changed since the file was reviewed. Nothing was imported; review the file again.`)
    }
  }

  const confirmed: ImportConfirmed[] = []
  const layersByKey = new Map(listed.map(row => [scopeKey(row.layer.scope), row.layer]))
  for (const layer of plan.layers) {
    const writes = layer.sets.filter(set => (set.action ?? 'create') !== 'skip')
    for (const set of layer.sets) if (set.action === 'skip') resultOf(layer.scope, set.name).status = 'unchanged'
    if (!writes.length) continue
    let remote = layersByKey.get(scopeKey(layer.scope))
    if (!remote) {
      try {
        remote = await io.createLayer(layer.scope)
        layersByKey.set(scopeKey(remote.scope), remote)
      } catch (cause) {
        if (replyUncertain(cause)) {
          return done({ status: 'uncertain', confirmed, message: `The workspace did not confirm the layer for ${scopeLabel(layer.scope)}. ${confirmedLine(confirmed)}Check the server before trying again. This import did not retry and did not roll anything back.` })
        }
        for (const set of writes) Object.assign(resultOf(layer.scope, set.name), { status: 'failed', reason: rulesMessage(cause) })
        return done({ status: 'partial', confirmed, message: `${confirmedLine(confirmed)}${scopeLabel(layer.scope)} was not created. ${rulesMessage(cause)} Later sets were not started. Nothing was rolled back or published.` })
      }
    }
    for (const set of writes) {
      const result = resultOf(layer.scope, set.name)
      let target: RuleSet
      if (set.action === 'update' && set.existing) {
        target = { id: set.existing.id, layer_id: remote.id, scope: remote.scope, name: set.name, revision: set.existing.revision, rules: set.existing.rules, published_version: '' }
      } else {
        try {
          target = await io.createSet(remote.id, set.name)
        } catch (cause) {
          if (replyUncertain(cause)) {
            result.status = 'unknown'
            return done({ status: 'uncertain', confirmed, message: `The workspace did not confirm “${set.name}”. ${confirmedLine(confirmed)}Check the server before trying again. This import did not retry and did not roll anything back.` })
          }
          Object.assign(result, { status: 'failed', reason: rulesMessage(cause) })
          return done({ status: 'partial', confirmed, message: `${confirmedLine(confirmed)}“${set.name}” was not created. ${rulesMessage(cause)} Later sets were not started. Confirmed drafts stay on the server. Nothing was rolled back or published.` })
        }
      }
      try {
        const saved = await io.saveDraft(target.id, { expected_revision: target.revision, name: set.name, rules: set.rules })
        confirmed.push({ scope: remote.scope, layer: remote, set: saved, rulesSaved: true })
        result.status = 'saved'
      } catch (cause) {
        const updated = set.action === 'update'
        if (replyUncertain(cause)) {
          if (!updated) confirmed.push({ scope: remote.scope, layer: remote, set: target, rulesSaved: null })
          result.status = 'unknown'
          const what = updated ? `The workspace did not confirm the new draft for “${set.name}”; whether it was saved is unknown.` : `The workspace did not confirm the rules for “${set.name}”. The set “${set.name}” ${target.id} revision ${target.revision} exists; whether its rules were saved is unknown.`
          return done({ status: 'uncertain', confirmed, message: `${what} ${confirmedLine(confirmed)}Check the server before trying again. This import did not retry and did not roll anything back.` })
        }
        if (!updated) confirmed.push({ scope: remote.scope, layer: remote, set: target, rulesSaved: false })
        Object.assign(result, { status: 'failed', reason: rulesMessage(cause) })
        const what = updated ? `The draft for “${set.name}” was not saved.` : `“${set.name}” was created, but its rules were not saved.`
        return done({ status: 'partial', confirmed, message: `${confirmedLine(confirmed)}${what} ${rulesMessage(cause)} Later sets were not started. Confirmed drafts stay on the server. Nothing was rolled back or published.` })
      }
    }
  }
  const count = confirmed.length
  return done({ status: 'imported', confirmed, message: `Imported ${count} draft ${count === 1 ? 'set' : 'sets'}. Nothing was published.` })
}
