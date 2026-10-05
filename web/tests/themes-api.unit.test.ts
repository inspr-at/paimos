// SPDX-License-Identifier: AGPL-3.0-only
import { beforeEach, expect, it, vi } from 'vitest'
vi.mock('../src/lib/api.ts', () => ({ api: vi.fn() }))
import { api } from '../src/lib/api.ts'
import { deleteTheme, duplicateTheme, listThemes, selectTheme, updateTheme, type ThemeRecord } from '../src/lib/themes.ts'
const theme = { id: 'theme-id', name: ' Copper ', revision: 9, values: { primary: { light: '#0e6f6c', dark: null }, secondary: { light: '#d69b31', dark: '#e2b45a' }, recurring_marker: { source: 'custom', custom: '#bf3d6d' }, agents: { avatar: 'robot-5', ring: 'still', size: 90, hover: true, palette: 'deutan' } } } as ThemeRecord
beforeEach(() => { vi.resetAllMocks(); vi.mocked(api).mockImplementation(async () => new Response('{}', { status: 200 })) })
it('client sends complete values, nullable derivation and captured CAS without altering Agents', async () => {
  await updateTheme(theme)
  expect(api).toHaveBeenCalledWith('/themes/theme-id', expect.objectContaining({ method: 'PATCH', body: JSON.stringify({ name: 'Copper', values: theme.values, revision: 9 }) }))
  await selectTheme(null, 23)
  expect(api).toHaveBeenLastCalledWith('/me/theme', expect.objectContaining({ method: 'PUT', body: JSON.stringify({ theme_id: null, revision: 23 }) }))
  await duplicateTheme(theme, 'Copper copy')
  expect(api).toHaveBeenLastCalledWith('/themes/theme-id/duplicate', expect.objectContaining({ method: 'POST', body: JSON.stringify({ name: 'Copper copy', scope: 'personal', revision: 9 }) }))
})
it('list requests bounded pages and encodes the cursor; delete handles 204', async () => {
  await listThemes('cursor&limit=1000')
  expect(api).toHaveBeenCalledWith('/themes?limit=50&after=cursor%26limit%3D1000', { method: 'GET' })
  vi.mocked(api).mockResolvedValue(new Response(null, { status: 204 }))
  await expect(deleteTheme(theme)).resolves.toBeUndefined()
  expect(api).toHaveBeenLastCalledWith('/themes/theme-id?revision=9', { method: 'DELETE' })
})
it('permission, revision and server failures are rejected with their actual status', async () => {
  for (const status of [403, 409, 500]) {
    vi.mocked(api).mockResolvedValue(new Response('{}', { status }))
    await expect(updateTheme(theme)).rejects.toMatchObject({ status })
  }
})
