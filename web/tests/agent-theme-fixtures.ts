// SPDX-License-Identifier: AGPL-3.0-only
import type { Page } from '@playwright/test'
import type { AgentThemeAppearance } from '../src/lib/agentTheme'
import type { ThemeRecord } from '../src/lib/themes'
import { me } from './work-fixtures'

// Saved appearance is served by the Theme API. Legacy preferences stay in the
// work fixture to prove that theme edits don't rewrite heartbeat/rollback data.
export async function mockAgentTheme(page: Page, agents: Partial<AgentThemeAppearance> = {}) {
  // Appearance scenarios read controlled HTTP snapshots. A quiet stream keeps
  // an unmocked SSE disconnect from invalidating the initial live-feed read.
  await page.addInitScript(() => {
    class QuietStream extends EventTarget { close() {} }
    Object.assign(window, { EventSource: QuietStream })
  })
  const state = {
    saved: {
      id: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', tenant_id: 't1', name: 'My agent appearance', scope: 'personal', owner_principal_id: me.id,
      revision: 1, created_at: '', updated_at: '',
      values: { primary: { light: '#0e6f6c', dark: '#a4e5df' }, secondary: { light: '#d69b31', dark: '#e2b45a' }, recurring_marker: { source: 'secondary', custom: null },
        agents: { avatar: 'robot-1', ring: null, size: null, hover: false, palette: 'standard', dim_inactive: true, inactive_opacity: 55, ...agents } },
    } as ThemeRecord,
    writes: [] as { name: string; values: ThemeRecord['values']; revision: number }[], fail: false,
  }
  await page.route('**/api/**', async route => {
    const path = new URL(route.request().url()).pathname
    if (path === '/api/plugins') return route.fulfill({ json: [] })
    if (path === '/api/releases') return route.fulfill({ json: { schema: 'inspr.release-history.v1', product: 'PAIMOS AEON', repository: 'inspr-at/paimos', version_scheme: 'inspr-calendar-v2', generated_at: '', source: 'none', current: '260923120000.0.0', live_since: '', releases: [] } })
    if (path === '/api/me/theme') return route.fulfill({ json: { theme: state.saved, default_theme_id: 'default', selected_theme_id: state.saved.id, revision: 1, fallback_notice: null } })
    if (path === '/api/themes') return route.fulfill({ json: { items: [state.saved], next_cursor: null } })
    if (path === `/api/themes/${state.saved.id}`) {
      if (route.request().method() === 'PATCH') {
        const body = route.request().postDataJSON()
        state.writes.push(body)
        if (body.revision !== state.saved.revision) return route.fulfill({ status: 409, json: { error: 'conflict' } })
        if (state.fail) return route.fulfill({ status: 503, json: { error: 'unavailable' } })
        state.saved = { ...state.saved, name: body.name, values: body.values, revision: state.saved.revision + 1 }
      }
      return route.fulfill({ json: state.saved })
    }
    return route.fallback()
  })
  return state
}
export async function saveAgentTheme(page: Page) {
  await page.getByRole('button', { name: /^Save/ }).click()
  await page.getByRole('region', { name: 'Unsaved theme changes' }).waitFor({ state: 'hidden' })
}
