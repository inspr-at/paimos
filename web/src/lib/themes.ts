// SPDX-License-Identifier: AGPL-3.0-only
import { api } from './api.ts'
export interface ThemeAccent { light: string; dark: string | null }
export interface ThemeValues {
  primary: ThemeAccent; secondary: ThemeAccent
  recurring_marker: { source: 'primary' | 'secondary' | 'neutral' | 'custom'; custom: string | null }
  agents: { avatar: string; ring: string | null; hover: boolean; size: number | null; palette: string }
}
export interface ThemeRecord {
  id: string; tenant_id: string; name: string; scope: 'default' | 'personal' | 'workspace'; owner_principal_id: string | null
  values: ThemeValues; revision: number; created_at: string; updated_at: string
}
export interface ActiveTheme {
  theme: ThemeRecord; default_theme_id: string; selected_theme_id: string | null; revision: number
  fallback_notice: { deleted_theme_id: string; deleted_theme_name: string } | null
}
export interface ThemesPage { items: ThemeRecord[]; next_cursor: string | null }
async function request<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  const response = await api(path, { method, ...(body === undefined ? {} : { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }) })
  if (!response.ok) throw Object.assign(new Error(response.status === 409 ? 'This theme changed elsewhere. Discard your edits and reload before trying again.' : response.status === 403 ? 'You no longer have permission to change this theme.' : 'The theme operation failed. Please try again.'), { status: response.status })
  return response.status === 204 ? undefined as T : response.json() as Promise<T>
}
export const listThemes = (after?: string) => request<ThemesPage>(`/themes?limit=50${after ? `&after=${encodeURIComponent(after)}` : ''}`)
export const getActiveTheme = () => request<ActiveTheme>('/me/theme')
export const getTheme = (id: string) => request<ThemeRecord>(`/themes/${encodeURIComponent(id)}`)
export const selectTheme = (theme_id: string | null, revision: number) => request<ActiveTheme>('/me/theme', 'PUT', { theme_id, revision })
export const updateTheme = (theme: ThemeRecord) => request<ThemeRecord>(`/themes/${encodeURIComponent(theme.id)}`, 'PATCH', { name: theme.name.trim(), values: theme.values, revision: theme.revision })
export const duplicateTheme = (theme: ThemeRecord, name: string) => request<ThemeRecord>(`/themes/${encodeURIComponent(theme.id)}/duplicate`, 'POST', { name, scope: 'personal', revision: theme.revision })
export const deleteTheme = (theme: Pick<ThemeRecord, 'id' | 'revision'>) => request<void>(`/themes/${encodeURIComponent(theme.id)}?revision=${theme.revision}`, 'DELETE')
