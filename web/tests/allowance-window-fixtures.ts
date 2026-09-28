// SPDX-License-Identifier: AGPL-3.0-only
import type { Page, Route } from '@playwright/test'
import type { AgentData } from './agents-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { me } from './work-fixtures'

export type WindowMode = 'ok' | 'overlap' | 'forbidden' | 'uncertain'

export interface AllowanceRoutes {
  posts: { path: string; body: unknown }[]
  release: () => void
  setMode: (mode: WindowMode) => void
  setHold: (hold: boolean) => void
}

export async function mockAllowanceRoutes(page: Page, accounts: AgentData['accounts'], options: { manage?: boolean; kind?: 'person' | 'agent'; mode?: WindowMode; hold?: boolean } = {}): Promise<AllowanceRoutes> {
  const posts: { path: string; body: unknown }[] = []
  let mode = options.mode ?? 'ok'
  let hold = options.hold ?? false
  let releaseHold = () => {}
  let gate = new Promise<void>(resolve => { releaseHold = resolve })
  const resetGate = () => { gate = new Promise<void>(resolve => { releaseHold = resolve }) }
  await page.route('**/api/**', async (route: Route) => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname
    if (path === '/api/me/permissions') {
      const body = mockEffectivePermissions('admin')
      const permissions = options.manage ? [...body.workspace.permissions, 'account.manage'] : [...body.workspace.permissions]
      return route.fulfill({ json: { ...body, workspace: { ...body.workspace, permissions } } })
    }
    if (path === '/api/me' && options.kind === 'agent') {
      return route.fulfill({ json: { principal: { id: me.id, name: 'Agent session', kind: 'agent' }, tenant: { id: 't1', name: 'INSPR Studio' } } })
    }
    const windowPath = /^\/api\/agent-accounts\/([^/]+)\/windows$/.exec(path)
    if (windowPath && request.method() === 'POST') {
      const body = request.postDataJSON()
      posts.push({ path, body })
      if (hold) await gate
      if (mode === 'forbidden') return route.fulfill({ status: 403, json: { error: 'permission denied' } })
      if (mode === 'overlap') return route.fulfill({ status: 409, json: { error: 'allowance windows overlap' } })
      if (mode === 'uncertain') return route.fulfill({ status: 503, json: { error: 'gateway' } })
      const accountId = decodeURIComponent(windowPath[1] ?? '')
      const account = accounts.find(item => item.id === accountId)
      const created = { id: `window-${posts.length}`, account_id: accountId, used: 0, reserved: 0, provisional: true, ...body }
      account?.windows.push(created)
      return route.fulfill({ status: 201, json: created })
    }
    return route.fallback()
  })
  return {
    posts,
    release: () => { const done = releaseHold; hold = false; done() },
    setMode: next => { mode = next },
    setHold: next => { hold = next; if (next) resetGate() },
  }
}
