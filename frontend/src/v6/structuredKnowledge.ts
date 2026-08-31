/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 *
 * PAI-863 — strict Paimos 6 structured-knowledge projection. No legacy body,
 * foreign instance identity, metadata graph, or cache object crosses this seam.
 */

import { api } from '@/api/client'

export type StructuredKnowledgeType = 'memory' | 'runbook' | 'external_system' | 'related_project' | 'guideline'
export type StructuredKnowledgeLevel = 'project' | 'instance' | 'kernel' | 'vision'
export type StructuredKnowledgeRelation = 'parent' | 'child' | 'about' | 'see_also' | 'supersedes'
export type StructuredKnowledgeValidationFlag = 'essay' | 'likely_duplicate' | 'chat_note_prose' | 'legacy_unstructured'

export interface StructuredKnowledgeValidation {
  flags: StructuredKnowledgeValidationFlag[]
  likely_duplicate_ids: number[]
  body_bytes: number
  short_body_limit_bytes: number
}

export interface StructuredKnowledgeLink {
  link_id: number
  relation: StructuredKnowledgeRelation
  target_issue_id: number
  target_knowledge: boolean
  target_type: string
  target_slug: string
  target_title: string
}

export interface StructuredKnowledgeEntry {
  knowledge_id: number
  level: StructuredKnowledgeLevel
  type: StructuredKnowledgeType
  slug: string
  title: string
  purpose: string
  short_body: string
  authored_product_session_id: string
  revision: number
  links: StructuredKnowledgeLink[]
  validation: StructuredKnowledgeValidation
  created_at: string
  updated_at: string
}

export interface LegacyStructuredKnowledgeEntry {
  knowledge_id: number
  type: StructuredKnowledgeType
  slug: string
  title: string
  body_bytes: number
  validation: StructuredKnowledgeValidation
  updated_at: string
}

export interface StructuredKnowledgeProposal {
  proposal_id: string
  source_kind: 'remember'
  product_session_id: string
  type: StructuredKnowledgeType
  slug: string
  title: string
  purpose: string
  candidate_body: string
  state: 'proposed' | 'dismissed' | 'promoted'
  promoted_knowledge_id: number | null
  validation: StructuredKnowledgeValidation
  created_at: string
  updated_at: string
}

export interface StructuredKnowledgeSnapshot {
  schema_version: 1
  project_id: number
  short_body_limit_bytes: number
  compact_product_session_id: string | null
  entries: StructuredKnowledgeEntry[]
  legacy: LegacyStructuredKnowledgeEntry[]
  proposals: StructuredKnowledgeProposal[]
}

type UnknownRecord = Record<string, unknown>

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/
const RFC3339 = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/
const SLUG = /^[a-z][a-z0-9_-]{0,63}$/
const TYPES = new Set<StructuredKnowledgeType>(['memory', 'runbook', 'external_system', 'related_project', 'guideline'])
const LEVELS = new Set<StructuredKnowledgeLevel>(['project', 'instance', 'kernel', 'vision'])
const RELATIONS = new Set<StructuredKnowledgeRelation>(['parent', 'child', 'about', 'see_also', 'supersedes'])
const FLAGS = new Set<StructuredKnowledgeValidationFlag>(['essay', 'likely_duplicate', 'chat_note_prose', 'legacy_unstructured'])
const PROPOSAL_STATES = new Set(['proposed', 'dismissed', 'promoted'])
const SHORT_BODY_LIMIT_BYTES = 1200
const PROPOSAL_BODY_LIMIT_BYTES = 64 * 1024

export class StructuredKnowledgeContractError extends Error {
  constructor() {
    super('invalid Paimos 6 structured-knowledge contract')
    this.name = 'StructuredKnowledgeContractError'
  }
}

function record(value: unknown): UnknownRecord | null {
  return value !== null && typeof value === 'object' && !Array.isArray(value) ? value as UnknownRecord : null
}

function exactKeys(value: UnknownRecord, keys: readonly string[]): boolean {
  const actual = Object.keys(value)
  return actual.length === keys.length && keys.every((key) => Object.prototype.hasOwnProperty.call(value, key))
}

function positiveInteger(value: unknown): number | null {
  return typeof value === 'number' && Number.isSafeInteger(value) && value > 0 ? value : null
}

function nonNegativeInteger(value: unknown): number | null {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : null
}

function plainText(value: unknown, nonEmpty = false): string | null {
  if (typeof value !== 'string' || /[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/.test(value)) return null
  return !nonEmpty || value.trim() !== '' ? value : null
}

function boundedLine(value: unknown, maxBytes: number): string | null {
  const text = plainText(value, true)
  return text !== null && text.trim() === text && !/[\t\r\n]/.test(text) && utf8Bytes(text) <= maxBytes ? text : null
}

function instant(value: unknown): string | null {
  return typeof value === 'string' && RFC3339.test(value) && !Number.isNaN(Date.parse(value)) ? value : null
}

function enumValue<T extends string>(value: unknown, values: ReadonlySet<T>): T | null {
  return typeof value === 'string' && values.has(value as T) ? value as T : null
}

function utf8Bytes(value: string): number {
  return new TextEncoder().encode(value).length
}

function parseValidation(value: unknown, limit: number): StructuredKnowledgeValidation | null {
  const raw = record(value)
  if (!raw || !exactKeys(raw, ['flags', 'likely_duplicate_ids', 'body_bytes', 'short_body_limit_bytes'])
    || raw.short_body_limit_bytes !== limit || !Array.isArray(raw.flags) || !Array.isArray(raw.likely_duplicate_ids)) return null
  const flags = raw.flags.map((flag) => enumValue(flag, FLAGS))
  const duplicateIds = raw.likely_duplicate_ids.map(positiveInteger)
  const bodyBytes = nonNegativeInteger(raw.body_bytes)
  if (flags.some((flag) => flag === null) || new Set(flags).size !== flags.length
    || duplicateIds.some((id) => id === null) || new Set(duplicateIds).size !== duplicateIds.length || bodyBytes === null) return null
  if (flags.includes('essay') !== (bodyBytes > limit)
    || flags.includes('likely_duplicate') !== (duplicateIds.length > 0)) return null
  return {
    flags: flags as StructuredKnowledgeValidationFlag[],
    likely_duplicate_ids: duplicateIds as number[],
    body_bytes: bodyBytes,
    short_body_limit_bytes: limit,
  }
}

function parseLink(value: unknown): StructuredKnowledgeLink | null {
  const raw = record(value)
  if (!raw || !exactKeys(raw, [
    'link_id', 'relation', 'target_issue_id', 'target_knowledge', 'target_type', 'target_slug', 'target_title',
  ])) return null
  const linkId = positiveInteger(raw.link_id)
  const relation = enumValue(raw.relation, RELATIONS)
  const targetIssueId = positiveInteger(raw.target_issue_id)
  const targetType = plainText(raw.target_type, true)
  const targetSlug = plainText(raw.target_slug)
  const targetTitle = plainText(raw.target_title, true)
  if (!linkId || !relation || !targetIssueId || typeof raw.target_knowledge !== 'boolean'
    || targetType === null || targetSlug === null || targetTitle === null) return null
  if (raw.target_knowledge && (!TYPES.has(targetType as StructuredKnowledgeType) || !SLUG.test(targetSlug))) return null
  return {
    link_id: linkId,
    relation,
    target_issue_id: targetIssueId,
    target_knowledge: raw.target_knowledge,
    target_type: targetType,
    target_slug: targetSlug,
    target_title: targetTitle,
  }
}

function parseEntry(value: unknown, limit: number): StructuredKnowledgeEntry | null {
  const raw = record(value)
  if (!raw || !exactKeys(raw, [
    'knowledge_id', 'level', 'type', 'slug', 'title', 'purpose', 'short_body',
    'authored_product_session_id', 'revision', 'links', 'validation', 'created_at', 'updated_at',
  ]) || !Array.isArray(raw.links)) return null
  const knowledgeId = positiveInteger(raw.knowledge_id)
  const level = enumValue(raw.level, LEVELS)
  const type = enumValue(raw.type, TYPES)
  const slug = typeof raw.slug === 'string' && SLUG.test(raw.slug) ? raw.slug : null
  const title = boundedLine(raw.title, 240)
  const purpose = boundedLine(raw.purpose, 400)
  const shortBody = plainText(raw.short_body, true)
  const sessionId = typeof raw.authored_product_session_id === 'string' && UUID.test(raw.authored_product_session_id)
    ? raw.authored_product_session_id : null
  const revision = positiveInteger(raw.revision)
  const validation = parseValidation(raw.validation, limit)
  const createdAt = instant(raw.created_at)
  const updatedAt = instant(raw.updated_at)
  const links = raw.links.map(parseLink)
  if (!knowledgeId || !level || !type || !slug || title === null || purpose === null || shortBody === null
    || !sessionId || !revision || !validation || !createdAt || !updatedAt || links.some((link) => link === null)
    || utf8Bytes(shortBody) !== validation.body_bytes || validation.body_bytes > limit
    || validation.flags.includes('essay') || new Set(links.map((link) => link!.link_id)).size !== links.length) return null
  return {
    knowledge_id: knowledgeId,
    level,
    type,
    slug,
    title,
    purpose,
    short_body: shortBody,
    authored_product_session_id: sessionId,
    revision,
    links: links as StructuredKnowledgeLink[],
    validation,
    created_at: createdAt,
    updated_at: updatedAt,
  }
}

function parseLegacy(value: unknown, limit: number): LegacyStructuredKnowledgeEntry | null {
  const raw = record(value)
  if (!raw || !exactKeys(raw, ['knowledge_id', 'type', 'slug', 'title', 'body_bytes', 'validation', 'updated_at'])) return null
  const knowledgeId = positiveInteger(raw.knowledge_id)
  const type = enumValue(raw.type, TYPES)
  const slug = typeof raw.slug === 'string' && SLUG.test(raw.slug) ? raw.slug : null
  const title = boundedLine(raw.title, 240)
  const bodyBytes = nonNegativeInteger(raw.body_bytes)
  const validation = parseValidation(raw.validation, limit)
  const updatedAt = instant(raw.updated_at)
  if (!knowledgeId || !type || !slug || title === null || bodyBytes === null || !validation || !updatedAt
    || bodyBytes !== validation.body_bytes || !validation.flags.includes('legacy_unstructured')) return null
  return { knowledge_id: knowledgeId, type, slug, title, body_bytes: bodyBytes, validation, updated_at: updatedAt }
}

function parseProposal(value: unknown, limit: number): StructuredKnowledgeProposal | null {
  const raw = record(value)
  if (!raw || !exactKeys(raw, [
    'proposal_id', 'source_kind', 'product_session_id', 'type', 'slug', 'title', 'purpose',
    'candidate_body', 'state', 'promoted_knowledge_id', 'validation', 'created_at', 'updated_at',
  ])) return null
  const proposalId = typeof raw.proposal_id === 'string' && UUID.test(raw.proposal_id) ? raw.proposal_id : null
  const sessionId = typeof raw.product_session_id === 'string' && UUID.test(raw.product_session_id) ? raw.product_session_id : null
  const type = enumValue(raw.type, TYPES)
  const slug = typeof raw.slug === 'string' && SLUG.test(raw.slug) ? raw.slug : null
  const title = boundedLine(raw.title, 240)
  const purpose = boundedLine(raw.purpose, 400)
  const candidateBody = plainText(raw.candidate_body, true)
  const state = typeof raw.state === 'string' && PROPOSAL_STATES.has(raw.state) ? raw.state as StructuredKnowledgeProposal['state'] : null
  const promotedKnowledgeId = raw.promoted_knowledge_id === null ? null : positiveInteger(raw.promoted_knowledge_id)
  const validation = parseValidation(raw.validation, limit)
  const createdAt = instant(raw.created_at)
  const updatedAt = instant(raw.updated_at)
  if (!proposalId || raw.source_kind !== 'remember' || !sessionId || !type || !slug || title === null || purpose === null
    || candidateBody === null || !state || promotedKnowledgeId === null && raw.promoted_knowledge_id !== null
    || !validation || utf8Bytes(candidateBody) !== validation.body_bytes
    || validation.body_bytes > PROPOSAL_BODY_LIMIT_BYTES || !createdAt || !updatedAt
    || (state === 'promoted') !== (promotedKnowledgeId !== null)) return null
  return {
    proposal_id: proposalId,
    source_kind: 'remember',
    product_session_id: sessionId,
    type,
    slug,
    title,
    purpose,
    candidate_body: candidateBody,
    state,
    promoted_knowledge_id: promotedKnowledgeId,
    validation,
    created_at: createdAt,
    updated_at: updatedAt,
  }
}

export function parseStructuredKnowledgeSnapshot(value: unknown, expectedProjectId: number): StructuredKnowledgeSnapshot {
  const raw = record(value)
  if (!raw || !exactKeys(raw, [
    'schema_version', 'project_id', 'short_body_limit_bytes', 'compact_product_session_id', 'entries', 'legacy', 'proposals',
  ]) || raw.schema_version !== 1 || raw.project_id !== expectedProjectId
    || !Array.isArray(raw.entries) || !Array.isArray(raw.legacy) || !Array.isArray(raw.proposals)) {
    throw new StructuredKnowledgeContractError()
  }
  const limit = positiveInteger(raw.short_body_limit_bytes)
  const compact = raw.compact_product_session_id === null
    ? null
    : typeof raw.compact_product_session_id === 'string' && UUID.test(raw.compact_product_session_id)
      ? raw.compact_product_session_id : null
  if (limit !== SHORT_BODY_LIMIT_BYTES || compact === null && raw.compact_product_session_id !== null) {
    throw new StructuredKnowledgeContractError()
  }
  const entries = raw.entries.map((entry) => parseEntry(entry, limit))
  const legacy = raw.legacy.map((entry) => parseLegacy(entry, limit))
  const proposals = raw.proposals.map((proposal) => parseProposal(proposal, limit))
  if (entries.some((entry) => entry === null) || legacy.some((entry) => entry === null) || proposals.some((proposal) => proposal === null)) {
    throw new StructuredKnowledgeContractError()
  }
  const identities = [
    ...entries.map((entry) => entry!.knowledge_id),
    ...legacy.map((entry) => entry!.knowledge_id),
  ]
  if (new Set(identities).size !== identities.length
    || new Set(proposals.map((proposal) => proposal!.proposal_id)).size !== proposals.length
    || (entries.length > 0 || proposals.length > 0) && compact === null
    || entries.some((entry) => entry!.level !== 'project' || entry!.authored_product_session_id !== compact)
    || proposals.some((proposal) => proposal!.product_session_id !== compact)) {
    throw new StructuredKnowledgeContractError()
  }
  return {
    schema_version: 1,
    project_id: expectedProjectId,
    short_body_limit_bytes: limit,
    compact_product_session_id: compact,
    entries: entries as StructuredKnowledgeEntry[],
    legacy: legacy as LegacyStructuredKnowledgeEntry[],
    proposals: proposals as StructuredKnowledgeProposal[],
  }
}

export async function loadStructuredKnowledgeSnapshot(projectId: number, signal?: AbortSignal): Promise<StructuredKnowledgeSnapshot> {
  const raw = await api.get<unknown>(`/projects/${projectId}/structured-knowledge/v1`, { signal })
  return parseStructuredKnowledgeSnapshot(raw, projectId)
}
