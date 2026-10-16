// SPDX-License-Identifier: AGPL-3.0-only
// Read model for GET /plugins and the gates the shell displays.
// A displayed "open" state is not authority: the server checks every action.

import { api } from '../../lib/api.ts'
import { pluginLabel, type BusinessArea } from './areas.ts'

const pluginIdRe = /^[a-z][a-z0-9_]*$/
const digestRe = /^[0-9a-f]{64}$/

export interface BusinessPlugin {
  id: string
  version: string
  digest_sha256: string
  owner: string
  permissions: string[]
  // The kinds the plugin declares, with the field schema its records use.
  node_kinds: { slug: string; field_schema?: Record<string, unknown> }[]
  views: { id: string }[]
  installation: {
    manifest_digest_sha256: string
    enabled: boolean
    permissions: string[]
    plugin_id: string
    version: string
    updated_at: string
  }
}

export interface InstallationWrite {
  manifest_digest_sha256: string
  enabled: boolean
  permissions: string[]
}

export type GateState =
  | { state: 'absent' }
  | { state: 'unpinned' }
  | { state: 'disabled' }
  | { state: 'digest_mismatch' }
  | { state: 'under_granted'; missing: string[] }
  | { state: 'open' }

export interface AreaAvailability {
  gate: GateState
  blockedBy: readonly string[]
  open: boolean
  reason: string
}

export class BusinessAPIError extends Error {
  readonly status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

export function parseCatalog(body: unknown): BusinessPlugin[] {
  if (!Array.isArray(body)) throw new Error('Plugin list was not usable.')
  const seen = new Set<string>()
  return body.map(value => parsePlugin(value, seen))
}

export function pluginGate(item: BusinessPlugin | undefined): GateState {
  if (!item) return { state: 'absent' }
  if (!item.installation.updated_at || item.installation.updated_at.startsWith('0001-01-01')) return { state: 'unpinned' }
  if (item.installation.manifest_digest_sha256 !== item.digest_sha256) return { state: 'digest_mismatch' }
  if (!item.installation.enabled) return { state: 'disabled' }
  const granted = new Set(item.installation.permissions)
  const missing = item.permissions.filter(permission => !granted.has(permission))
  if (missing.length) return { state: 'under_granted', missing }
  return { state: 'open' }
}

export function availability(area: BusinessArea, catalog: readonly BusinessPlugin[]): AreaAvailability {
  if (!area.pluginId) return { gate: { state: 'open' }, blockedBy: [], open: true, reason: '' }
  const gate = pluginGate(catalog.find(item => item.id === area.pluginId))
  const blockedBy = area.requires.filter(id => pluginGate(catalog.find(item => item.id === id)).state !== 'open')
  const open = gate.state === 'open' && blockedBy.length === 0
  return { gate, blockedBy, open, reason: open ? '' : reasonFor(gate, blockedBy) }
}

export function statusLabel(state: AreaAvailability): string {
  if (state.open) return 'Enabled'
  if (state.gate.state === 'open') return 'Waiting'
  switch (state.gate.state) {
    case 'absent': return 'Not in this build'
    case 'unpinned': return 'Not enabled'
    case 'disabled': return 'Disabled'
    case 'digest_mismatch': return 'Digest mismatch'
    case 'under_granted': return 'Permissions incomplete'
    default: return 'Closed'
  }
}

export function disablePermissions(item: BusinessPlugin): string[] {
  const allowed = new Set(item.permissions)
  return item.installation.permissions.filter(permission => allowed.has(permission))
}

// Enabling always pins the compiled digest and the manifest's declared permissions.
// Disabling keeps the granted subset and still sends the compiled digest.
export function installationWrite(item: BusinessPlugin, enabled: boolean): InstallationWrite {
  return {
    manifest_digest_sha256: item.digest_sha256,
    enabled,
    permissions: enabled ? [...item.permissions] : disablePermissions(item),
  }
}

export function shortDigest(digest: string): string {
  if (!digestRe.test(digest)) return 'unavailable'
  return `${digest.slice(0, 8)}…${digest.slice(-8)}`
}

export async function listPlugins(): Promise<BusinessPlugin[]> {
  const response = await api('/plugins')
  if (!response.ok) throw new BusinessAPIError(response.status, await errorMessage(response))
  return parseCatalog(await response.json())
}

export async function configurePlugin(pluginId: string, body: InstallationWrite): Promise<void> {
  const response = await api(`/plugins/${encodeURIComponent(pluginId)}/installation`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!response.ok) throw new BusinessAPIError(response.status, await errorMessage(response))
}

function reasonFor(gate: GateState, blockedBy: readonly string[]): string {
  const parts: string[] = []
  const own = gateReason(gate)
  if (own) parts.push(own)
  if (blockedBy.length) parts.push(`${own ? 'Also waiting on' : 'Waiting on'} ${joinLabels(blockedBy)}.`)
  return parts.join(' ')
}

function gateReason(gate: GateState): string {
  switch (gate.state) {
    case 'open': return ''
    case 'absent': return 'This plugin is not registered in this build.'
    case 'unpinned': return 'A tenant admin has not enabled it.'
    case 'disabled': return 'A tenant admin disabled it.'
    case 'digest_mismatch': return 'The pinned digest does not match this build.'
    case 'under_granted': return `Missing ${gate.missing.join(', ')}.`
    default: return 'This section is closed.'
  }
}

function joinLabels(ids: readonly string[]): string {
  const labels = ids.map(pluginLabel)
  if (labels.length <= 1) return labels[0] ?? ''
  if (labels.length === 2) return `${labels[0]} and ${labels[1]}`
  return `${labels.slice(0, -1).join(', ')} and ${labels[labels.length - 1]}`
}

function parsePlugin(value: unknown, seen: Set<string>): BusinessPlugin {
  const row = object(value)
  const id = row.id
  if (typeof id !== 'string' || !pluginIdRe.test(id) || seen.has(id)) throw new Error('Plugin list was not usable.')
  seen.add(id)
  const digest = row.digest_sha256
  if (typeof digest !== 'string' || !digestRe.test(digest)) throw new Error('Plugin list was not usable.')
  return {
    id,
    version: typeof row.version === 'string' ? row.version : '',
    digest_sha256: digest,
    owner: typeof row.owner === 'string' ? row.owner : '',
    permissions: stringList(row.permissions),
    node_kinds: parseKinds(row.node_kinds),
    views: parseViews(row.views),
    installation: parseInstallation(row.installation, id),
  }
}

function parseInstallation(value: unknown, pluginId: string): BusinessPlugin['installation'] {
  const row = object(value)
  const digest = row.manifest_digest_sha256
  if (typeof digest !== 'string' || !digestRe.test(digest)) throw new Error('Plugin list was not usable.')
  if (typeof row.enabled !== 'boolean') throw new Error('Plugin list was not usable.')
  if (row.plugin_id !== undefined && row.plugin_id !== pluginId) throw new Error('Plugin list was not usable.')
  if (typeof row.updated_at !== 'string' || row.updated_at === '') throw new Error('Plugin list was not usable.')
  return {
    manifest_digest_sha256: digest,
    enabled: row.enabled,
    permissions: stringList(row.permissions),
    plugin_id: pluginId,
    version: typeof row.version === 'string' ? row.version : '',
    updated_at: row.updated_at,
  }
}

function parseKinds(value: unknown): BusinessPlugin['node_kinds'] {
  if (!Array.isArray(value)) return []
  return value.flatMap(entry => {
    if (!entry || typeof entry !== 'object') return []
    const { slug, field_schema: schema } = entry as { slug?: unknown; field_schema?: unknown }
    if (typeof slug !== 'string' || !slug) return []
    return schema && typeof schema === 'object' && !Array.isArray(schema) ? [{ slug, field_schema: schema as Record<string, unknown> }] : [{ slug }]
  })
}

function parseViews(value: unknown): { id: string }[] {
  if (!Array.isArray(value)) return []
  return value.flatMap(entry => {
    if (!entry || typeof entry !== 'object') return []
    const id = (entry as { id?: unknown }).id
    return typeof id === 'string' && id ? [{ id }] : []
  })
}

function stringList(value: unknown): string[] {
  if (value == null) return []
  if (!Array.isArray(value) || value.some(item => typeof item !== 'string' || item === '' || /\s/.test(item))) {
    throw new Error('Plugin list was not usable.')
  }
  return value
}

function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('Plugin list was not usable.')
  return value as Record<string, unknown>
}

async function errorMessage(response: Response): Promise<string> {
  const data = await response.json().catch(() => null)
  if (data && typeof data === 'object') {
    const row = data as { message?: unknown; error?: unknown }
    if (typeof row.message === 'string' && row.message) return row.message
    if (typeof row.error === 'string' && row.error) return row.error
  }
  return `Request failed (${response.status})`
}
