// SPDX-License-Identifier: AGPL-3.0-only
import type { AgentThemeAppearance } from './agentTheme.ts'
import { api } from './api.ts'
import { isAgentPalette } from './agentPalettes.ts'
import { themeCss } from './themeEngine.ts'
export interface ThemeAccent { light: string; dark: string | null }
export interface ThemeValues {
  primary: ThemeAccent; secondary: ThemeAccent
  recurring_marker: { source: 'primary' | 'secondary' | 'neutral' | 'custom'; custom: string | null }
  agents: AgentThemeAppearance
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
// Preserve the bounded initial-theme read through the shared editor/runtime.
export async function getActiveTheme(): Promise<ActiveTheme> {
  const signal = AbortSignal.timeout(5000)
  const response = await api('/me/theme', { signal }, 5000)
  if (!response.ok) throw Object.assign(new Error('Your theme could not be loaded. Default colours are shown.'), { status: response.status })
  const limit = 16 * 1024
  if (Number(response.headers.get('Content-Length')) > limit) {
    await response.body?.cancel()
    throw new Error('Theme response is too large')
  }
  if (!response.body) throw new Error('Empty theme response')
  const reader = response.body.getReader(), chunks: Uint8Array[] = []
  let size = 0
  const cancel = () => { void reader.cancel(signal.reason).catch(() => undefined) }
  signal.addEventListener('abort', cancel, { once: true })
  try {
    for (;;) {
      signal.throwIfAborted()
      const chunk = await reader.read()
      signal.throwIfAborted()
      if (chunk.done) break
      size += chunk.value.byteLength
      if (size > limit) { await reader.cancel(); throw new Error('Theme response is too large') }
      chunks.push(chunk.value)
    }
  } finally { signal.removeEventListener('abort', cancel); reader.releaseLock() }
  const bytes = new Uint8Array(size)
  let offset = 0
  for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.byteLength }
  const active = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(bytes)) as ActiveTheme
  if (!active?.theme?.values || !isAgentPalette(active.theme.values.agents?.palette)) throw new Error('Invalid theme response')
  // Validate all colours, including Custom states, before the editor publishes.
  themeCss(active.theme.values)
  return active
}
export const getTheme = (id: string) => request<ThemeRecord>(`/themes/${encodeURIComponent(id)}`)
export const selectTheme = (theme_id: string | null, revision: number) => request<ActiveTheme>('/me/theme', 'PUT', { theme_id, revision })
export const updateTheme = (theme: ThemeRecord) => request<ThemeRecord>(`/themes/${encodeURIComponent(theme.id)}`, 'PATCH', { name: theme.name.trim(), values: theme.values, revision: theme.revision })
export const duplicateTheme = (theme: ThemeRecord, name: string) => request<ThemeRecord>(`/themes/${encodeURIComponent(theme.id)}/duplicate`, 'POST', { name, scope: 'personal', revision: theme.revision })
export const deleteTheme = (theme: Pick<ThemeRecord, 'id' | 'revision'>) => request<void>(`/themes/${encodeURIComponent(theme.id)}?revision=${theme.revision}`, 'DELETE')
