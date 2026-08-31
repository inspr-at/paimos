/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 *
 * PAI-866 — strict command-palette settings, shortcut, and search boundary.
 */

import { api } from '@/api/client'

export type CommandShortcutSource = 'default' | 'instance' | 'user'
export type CommandShortcutValidation = 'ok' | 'invalid_shortcut' | 'shortcut_collision'

export interface CommandPaletteSettingsWire {
  schema_version: 1
  default_shortcut: string
  instance_shortcut: string | null
  user_shortcut: string | null
  effective_shortcut: string
  source: CommandShortcutSource
}

export interface CommandPaletteSessionResult {
  product_session_id: string
  title: string
  summary: string
  updated_at: string
}

export interface CommandPaletteNodeResult {
  node_id: number
  node_key: string
  title: string
  type: string
  type_label: string
  status: string
  updated_at: string
}

export interface CommandPaletteKnowledgeResult {
  knowledge_id: number
  type: string
  type_label: string
  slug: string
  title: string
  updated_at: string
}

export interface CommandPaletteSearchWire {
  schema_version: 1
  query: string
  sessions: CommandPaletteSessionResult[]
  nodes: CommandPaletteNodeResult[]
  knowledge: CommandPaletteKnowledgeResult[]
}

export interface ParsedCommandChord {
  modifiers: readonly ('Mod' | 'Ctrl' | 'Meta' | 'Alt' | 'Shift')[]
  code: string
}

type UnknownRecord = Record<string, unknown>

const MODIFIERS = ['Mod', 'Ctrl', 'Meta', 'Alt', 'Shift'] as const
const KEY_CODE = /^(?:Key[A-Z]|Digit[0-9]|Space|Slash|Backslash|BracketLeft|BracketRight|Semicolon|Quote|Comma|Period|Minus|Equal|Backquote)$/
const RESERVED_CODE = /^(?:Key[RLWQTNPF]|Digit[1-9])$/
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/
const ROUTE_TOKEN = /^[a-z0-9]+(?:-[a-z0-9]+)*$/
const STATUS_TOKEN = /^[a-z][a-z0-9_-]*$/

export class CommandPaletteContractError extends Error {
  constructor() {
    super('invalid command palette contract')
    this.name = 'CommandPaletteContractError'
  }
}

function record(value: unknown): UnknownRecord | null {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
    ? value as UnknownRecord
    : null
}

function exactKeys(value: UnknownRecord, expected: readonly string[]): boolean {
  const actual = Object.keys(value)
  return actual.length === expected.length
    && expected.every((key) => Object.prototype.hasOwnProperty.call(value, key))
}

function text(value: unknown, nullable = false): string | null | undefined {
  if (nullable && value === null) return null
  if (typeof value !== 'string' || value.length === 0 || value !== value.trim() || /[\u0000-\u001f\u007f]/.test(value)) return undefined
  return value
}

function timestamp(value: unknown): string | null {
  if (typeof value !== 'string') return null
  const match = value.match(/^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d{1,9})?(?:Z|[+-](\d{2}):(\d{2}))$/)
  if (!match) return null
  const [, yearRaw, monthRaw, dayRaw, hourRaw, minuteRaw, secondRaw, offsetHourRaw, offsetMinuteRaw] = match
  const year = Number(yearRaw)
  const month = Number(monthRaw)
  const day = Number(dayRaw)
  const leap = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0)
  const days = [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31]
  if (month < 1 || month > 12 || day < 1 || day > days[month - 1]
    || Number(hourRaw) > 23 || Number(minuteRaw) > 59 || Number(secondRaw) > 59
    || Number(offsetHourRaw ?? 0) > 23 || Number(offsetMinuteRaw ?? 0) > 59) return null
  return Number.isFinite(Date.parse(value)) ? value : null
}

export function parseCommandShortcut(value: unknown): ParsedCommandChord | null {
  if (typeof value !== 'string') return null
  const tokens = value.split('+')
  if (tokens.length < 2) return null
  const code = tokens[tokens.length - 1]
  const modifiers = tokens.slice(0, -1)
  if (!KEY_CODE.test(code) || modifiers.length === 0) return null
  let prior = -1
  for (const modifier of modifiers) {
    const index = MODIFIERS.indexOf(modifier as typeof MODIFIERS[number])
    if (index < 0 || index <= prior) return null
    prior = index
  }
  if (modifiers.includes('Mod') && (modifiers.includes('Ctrl') || modifiers.includes('Meta'))) return null
  if (!modifiers.some((modifier) => ['Mod', 'Ctrl', 'Meta', 'Alt'].includes(modifier))) return null
  return { modifiers: modifiers as ParsedCommandChord['modifiers'], code }
}

export function validateCommandShortcut(value: unknown): CommandShortcutValidation {
  const chord = parseCommandShortcut(value)
  if (!chord) return 'invalid_shortcut'
  const primary = chord.modifiers.some((modifier) => ['Mod', 'Ctrl', 'Meta'].includes(modifier))
  const onlyPrimaryAndShift = chord.modifiers.every((modifier) => ['Mod', 'Ctrl', 'Meta', 'Shift'].includes(modifier))
  return primary && onlyPrimaryAndShift && RESERVED_CODE.test(chord.code)
    ? 'shortcut_collision'
    : 'ok'
}

export function isAppleCommandPlatform(userAgent = globalThis.navigator?.userAgent ?? ''): boolean {
  return /Macintosh|Mac OS X|iPhone|iPad|iPod/i.test(userAgent)
}

export function commandShortcutLabel(value: string, apple = isAppleCommandPlatform()): string {
  const chord = parseCommandShortcut(value)
  if (!chord) return value
  const codeLabel: Record<string, string> = {
    Space: 'Space', Slash: '/', Backslash: '\\', BracketLeft: '[', BracketRight: ']',
    Semicolon: ';', Quote: "'", Comma: ',', Period: '.', Minus: '-', Equal: '=', Backquote: '`',
  }
  const modifierLabels = chord.modifiers.map((modifier) => {
    if (modifier === 'Mod') return apple ? 'Cmd' : 'Ctrl'
    if (modifier === 'Meta') return apple ? 'Cmd' : 'Meta'
    return modifier
  })
  const key = chord.code.startsWith('Key') ? chord.code.slice(3)
    : chord.code.startsWith('Digit') ? chord.code.slice(5)
      : codeLabel[chord.code] ?? chord.code
  return [...modifierLabels, key].join('+')
}

export function commandShortcutMatches(event: KeyboardEvent, value: string, apple = isAppleCommandPlatform()): boolean {
  const chord = parseCommandShortcut(value)
  if (!chord || event.code !== chord.code) return false
  const expected = {
    ctrl: chord.modifiers.includes('Ctrl') || chord.modifiers.includes('Mod') && !apple,
    meta: chord.modifiers.includes('Meta') || chord.modifiers.includes('Mod') && apple,
    alt: chord.modifiers.includes('Alt'),
    shift: chord.modifiers.includes('Shift'),
  }
  return event.ctrlKey === expected.ctrl && event.metaKey === expected.meta
    && event.altKey === expected.alt && event.shiftKey === expected.shift
}

export function parseCommandPaletteSettings(value: unknown): CommandPaletteSettingsWire {
  const root = record(value)
  if (!root || !exactKeys(root, [
    'schema_version', 'default_shortcut', 'instance_shortcut', 'user_shortcut', 'effective_shortcut', 'source',
  ]) || root.schema_version !== 1 || root.default_shortcut !== 'Mod+KeyK'
    || !['default', 'instance', 'user'].includes(String(root.source))) throw new CommandPaletteContractError()
  const instance = root.instance_shortcut === null ? null : typeof root.instance_shortcut === 'string' ? root.instance_shortcut : undefined
  const user = root.user_shortcut === null ? null : typeof root.user_shortcut === 'string' ? root.user_shortcut : undefined
  if (instance === undefined || user === undefined || typeof root.effective_shortcut !== 'string'
    || [root.default_shortcut, instance, user, root.effective_shortcut]
      .filter((shortcut): shortcut is string => shortcut !== null)
      .some((shortcut) => validateCommandShortcut(shortcut) !== 'ok')) throw new CommandPaletteContractError()
  const source = root.source as CommandShortcutSource
  const expected = source === 'user' ? user : source === 'instance' ? instance : root.default_shortcut
  if (expected === null || root.effective_shortcut !== expected
    || source === 'default' && (instance !== null || user !== null)
    || source === 'instance' && (instance === null || user !== null)
    || source === 'user' && user === null) throw new CommandPaletteContractError()
  return {
    schema_version: 1,
    default_shortcut: 'Mod+KeyK',
    instance_shortcut: instance,
    user_shortcut: user,
    effective_shortcut: root.effective_shortcut,
    source,
  }
}

function parseSession(value: unknown): CommandPaletteSessionResult | null {
  const row = record(value)
  if (!row || !exactKeys(row, ['product_session_id', 'title', 'summary', 'updated_at'])) return null
  const title = text(row.title)
  const summary = typeof row.summary === 'string' && !/[\u0000-\u001f\u007f]/.test(row.summary) ? row.summary : null
  const updatedAt = timestamp(row.updated_at)
  return typeof title === 'string' && summary !== null && updatedAt && typeof row.product_session_id === 'string' && UUID.test(row.product_session_id)
    ? { product_session_id: row.product_session_id, title, summary, updated_at: updatedAt }
    : null
}

function parseNode(value: unknown): CommandPaletteNodeResult | null {
  const row = record(value)
  if (!row || !exactKeys(row, ['node_id', 'node_key', 'title', 'type', 'type_label', 'status', 'updated_at'])) return null
  const nodeKey = text(row.node_key)
  const title = text(row.title)
  const typeLabel = text(row.type_label)
  const updatedAt = timestamp(row.updated_at)
  return Number.isSafeInteger(row.node_id) && (row.node_id as number) > 0
    && typeof nodeKey === 'string' && typeof title === 'string' && typeof typeLabel === 'string'
    && typeof row.type === 'string' && ROUTE_TOKEN.test(row.type)
    && typeof row.status === 'string' && STATUS_TOKEN.test(row.status) && updatedAt
    ? { node_id: row.node_id as number, node_key: nodeKey, title, type: row.type, type_label: typeLabel, status: row.status, updated_at: updatedAt }
    : null
}

function parseKnowledge(value: unknown): CommandPaletteKnowledgeResult | null {
  const row = record(value)
  if (!row || !exactKeys(row, ['knowledge_id', 'type', 'type_label', 'slug', 'title', 'updated_at'])) return null
  const typeLabel = text(row.type_label)
  const slug = text(row.slug)
  const title = text(row.title)
  const updatedAt = timestamp(row.updated_at)
  return Number.isSafeInteger(row.knowledge_id) && (row.knowledge_id as number) > 0
    && typeof row.type === 'string' && ROUTE_TOKEN.test(row.type)
    && typeof typeLabel === 'string' && typeof slug === 'string' && typeof title === 'string' && updatedAt
    ? { knowledge_id: row.knowledge_id as number, type: row.type, type_label: typeLabel, slug, title, updated_at: updatedAt }
    : null
}

export function parseCommandPaletteSearch(value: unknown, expectedQuery: string, limit: number): CommandPaletteSearchWire {
  const root = record(value)
  if (!root || !exactKeys(root, ['schema_version', 'query', 'sessions', 'nodes', 'knowledge'])
    || root.schema_version !== 1 || root.query !== expectedQuery
    || !Array.isArray(root.sessions) || !Array.isArray(root.nodes) || !Array.isArray(root.knowledge)
    || root.sessions.length > limit || root.nodes.length > limit || root.knowledge.length > limit) {
    throw new CommandPaletteContractError()
  }
  const sessions = root.sessions.map(parseSession)
  const nodes = root.nodes.map(parseNode)
  const knowledge = root.knowledge.map(parseKnowledge)
  if (sessions.some((row) => row === null) || nodes.some((row) => row === null) || knowledge.some((row) => row === null)) {
    throw new CommandPaletteContractError()
  }
  if (new Set(sessions.map((row) => row!.product_session_id)).size !== sessions.length
    || new Set(nodes.map((row) => row!.node_id)).size !== nodes.length
    || new Set(knowledge.map((row) => row!.knowledge_id)).size !== knowledge.length) throw new CommandPaletteContractError()
  return { schema_version: 1, query: expectedQuery, sessions: sessions as CommandPaletteSessionResult[], nodes: nodes as CommandPaletteNodeResult[], knowledge: knowledge as CommandPaletteKnowledgeResult[] }
}

export async function loadCommandPaletteSettings(signal?: AbortSignal): Promise<CommandPaletteSettingsWire> {
  return parseCommandPaletteSettings(await api.get<unknown>('/command-palette/v1/settings', { signal }))
}

export async function saveUserCommandShortcut(shortcut: string | null): Promise<CommandPaletteSettingsWire> {
  return parseCommandPaletteSettings(await api.put<unknown>('/command-palette/v1/settings', { shortcut }))
}

export async function saveInstanceCommandShortcut(shortcut: string | null): Promise<CommandPaletteSettingsWire> {
  return parseCommandPaletteSettings(await api.put<unknown>('/command-palette/v1/instance-settings', { shortcut }))
}

export async function loadCommandPaletteSearch(projectId: number, query: string, limit = 8, signal?: AbortSignal): Promise<CommandPaletteSearchWire> {
  const trimmed = query.trim()
  if (!Number.isSafeInteger(projectId) || projectId <= 0 || trimmed !== query
    || new TextEncoder().encode(query).byteLength < 1 || new TextEncoder().encode(query).byteLength > 128
    || !Number.isSafeInteger(limit) || limit < 1 || limit > 20) throw new CommandPaletteContractError()
  const wire = await api.get<unknown>(`/projects/${projectId}/command-palette/v1?q=${encodeURIComponent(query)}&limit=${limit}`, { signal })
  return parseCommandPaletteSearch(wire, query, limit)
}
