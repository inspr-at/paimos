// SPDX-License-Identifier: AGPL-3.0-only
// Settings: the sections, who sees them, and the server calls they read. Personal
// settings are everyone's; the rest are for workspace admins.
import { api } from './api.ts'
import { sessionGone } from './authz.ts'

export type SectionId = 'personal' | 'theme' | 'developer' | 'agents' | 'agent-rules' | 'accounts' | 'workspace' | 'access' | 'business' | 'projects' | 'portal'
// permission: the section shows to whoever holds it (can()), instead of by role.
// permission: one, or any of several (Access opens for See members or, alone, the access log).
export interface SettingsSection { id: SectionId; label: string; summary: string; admin: boolean; permission?: string | string[]; deniedTitle?: string; denied?: string }
export const anyOf = (permission: string | string[], allowed: (permission: string) => boolean) => (Array.isArray(permission) ? permission : [permission]).some(allowed)
export const SETTINGS_SECTIONS: readonly SettingsSection[] = [
  { id: 'personal', label: 'Personal', summary: 'Appearance, greeting and keys', admin: false },
  { id: 'theme', label: 'Theme', summary: 'Colours and agent appearance', admin: false },
  { id: 'developer', label: 'Developer', summary: 'For people working on Paimos itself', admin: false },
  { id: 'agents', label: 'Agents', summary: 'Activity, estimates and models', admin: true },
  { id: 'agent-rules', label: 'Agent rules', summary: 'Rules for every agent', admin: false, permission: 'rules.read', deniedTitle: 'Agent rules need permission to read them', denied: 'Reading agent rules needs the rules read permission. Project membership alone does not open this page.' },
  { id: 'accounts', label: 'Accounts', summary: 'Agent accounts and limits', admin: false, permission: 'account.read', deniedTitle: 'Accounts need permission to read them', denied: 'Agent accounts and their limits are visible to people who can read accounts.' },
  { id: 'workspace', label: 'Workspace', summary: 'Name, brand and your role', admin: true },
  { id: 'access', label: 'Access', summary: 'People, roles and agents', admin: true, permission: ['members.read', 'audit.read'] },
  { id: 'business', label: 'Business', summary: 'Parts and quote settings', admin: true },
  { id: 'projects', label: 'Projects', summary: 'Ticket types', admin: true },
  { id: 'portal', label: 'Product portal', summary: 'Public catalog', admin: true },
]
export function sectionOf(param: unknown): SectionId {
  const value = Array.isArray(param) ? param[0] : param
  return SETTINGS_SECTIONS.find(section => section.id === value)?.id ?? 'personal'
}
export const visibleSections = (admin: boolean, allowed: (permission: string) => boolean = () => false) =>
  SETTINGS_SECTIONS.filter(section => section.permission ? anyOf(section.permission, allowed) : admin || !section.admin)
// Deep links into a section: /settings/business#quotes.
export const settingsLink = (section: SectionId, anchor?: string) => `/settings/${section}${anchor ? `#${anchor}` : ''}`

async function read<T>(path: string): Promise<T> {
  const response = await api(path)
  if (response.status === 401) sessionGone()
  if (!response.ok) {
    const body = await response.json().catch(() => ({}))
    throw Object.assign(new Error(typeof body?.error === 'string' ? body.error : `Request failed (${response.status})`), { status: response.status })
  }
  return response.json() as Promise<T>
}
export const statusOf = (error: unknown) => (error as { status?: number })?.status ?? 0

// ---------- Agent keys (GET /api/agent-keys, admins) ----------
export interface AgentKey {
  id: string; principal_id: string; name: string; prefix: string; scopes: string[]
  created_at: string; expires_at: string | null; last_used_at: string | null; revoked_at: string | null
}
export const listAgentKeys = async () => (await read<{ keys: AgentKey[] }>('/agent-keys')).keys
export type KeyState = 'active' | 'expired' | 'revoked'
export function keyState(key: AgentKey, now = Date.now()): KeyState {
  if (key.revoked_at) return 'revoked'
  if (key.expires_at && Date.parse(key.expires_at) <= now) return 'expired'
  return 'active'
}

// ---------- Quote settings (GET /api/quotes/settings, staff while Quotes is on) ----------
export interface QuoteSettings {
  revision: number; numbering_time_zone: string; default_currency: string
  sender: Record<string, string>; defaults?: Record<string, unknown>; layout?: Record<string, unknown>
  smtp_confirmation_enabled: boolean; smtp_configured: boolean
  default_profile_id?: string
}
export const getQuoteSettings = () => read<QuoteSettings>('/quotes/settings')
// PATCH /api/quotes/settings (admins) replaces the whole settings record; texts
// and layout travel along unchanged until the quote editor edits them.
export async function saveQuoteSettings(current: QuoteSettings, change: { sender: Record<string, string>; default_currency: string; numbering_time_zone: string; default_profile_id?: string }): Promise<QuoteSettings> {
  // The default document profile rides along unchanged unless this change sets it;
  // the server reads a missing one as "none", so an empty choice is left out.
  const profile = change.default_profile_id ?? current.default_profile_id ?? ''
  const body = {
    expected_revision: current.revision, numbering_time_zone: change.numbering_time_zone, default_currency: change.default_currency,
    sender: change.sender, defaults: current.defaults ?? {}, layout: current.layout ?? {}, smtp_confirmation_enabled: false,
    ...(profile ? { default_profile_id: profile } : {}),
  }
  const response = await api('/quotes/settings', { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
  if (!response.ok) {
    const error = await response.json().catch(() => ({}))
    const message = typeof error?.error === 'string' ? error.error : ''
    throw Object.assign(new Error(senderError(response.status, message)), { status: response.status })
  }
  return response.json() as Promise<QuoteSettings>
}
export function senderError(status: number, message: string) {
  if (status === 409) return 'The quote settings changed elsewhere. The newer version is loaded; your edits are kept, save again to replace it.'
  if (status === 403) return 'Only a workspace admin can change the quote settings.'
  if (/currency/i.test(message)) return 'Use a three-letter currency code, such as EUR.'
  if (/time zone/i.test(message)) return 'Use a time zone like Europe/Vienna.'
  if (status === 400) return 'Some fields were not accepted. Please check them.'
  return 'The quote settings were not saved. Please try again.'
}
// The sender fields quotes print, in the order the form shows them.
export const SENDER_FIELDS = [
  { key: 'company', label: 'Company', group: 'company', wide: true, auto: 'organization' },
  { key: 'contact_person', label: 'Contact person', group: 'company', auto: 'name' },
  { key: 'email', label: 'Email', group: 'company', auto: 'email' },
  { key: 'phone', label: 'Phone', group: 'company', auto: 'tel' },
  { key: 'website', label: 'Website', group: 'company', auto: 'url' },
  { key: 'street', label: 'Street', group: 'address', wide: true, auto: 'street-address' },
  { key: 'postal_code', label: 'Postal code', group: 'address', auto: 'postal-code' },
  { key: 'city', label: 'City', group: 'address', auto: 'address-level2' },
  { key: 'country', label: 'Country', group: 'address', wide: true, auto: 'country-name' },
  { key: 'uid', label: 'VAT ID', group: 'legal', mono: true },
  { key: 'register_no', label: 'Company register', group: 'legal', mono: true },
  { key: 'register_court', label: 'Register court', group: 'legal' },
  { key: 'bank_name', label: 'Bank', group: 'bank', wide: true },
  { key: 'iban', label: 'IBAN', group: 'bank', mono: true },
  { key: 'bic', label: 'BIC', group: 'bank', mono: true },
] as const
export type SenderKey = typeof SENDER_FIELDS[number]['key']
// The sender as saved: the fields shown here (trimmed, empty ones left out) plus
// the ones this form does not edit (the logo), kept as they were.
export function senderWrite(before: Record<string, string>, form: Record<string, string>): Record<string, string> {
  const out: Record<string, string> = {}
  for (const [key, value] of Object.entries(before)) if (!SENDER_FIELDS.some(f => f.key === key) && typeof value === 'string') out[key] = value
  for (const field of SENDER_FIELDS) { const value = (form[field.key] ?? '').trim(); if (value) out[field.key] = field.key === 'iban' ? value.toUpperCase() : value }
  return out
}
// The sender as one line: company, city and country, the parts that are set.
export function senderLine(sender: Record<string, unknown> | null | undefined) {
  if (!sender) return ''
  return ['company', 'city', 'country'].map(key => sender[key]).filter((v): v is string => typeof v === 'string' && v.trim() !== '').join(' · ')
}
