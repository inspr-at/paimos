// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { api, getSession, sessionEnded, type Identity } from '../src/lib/api'
import { can, clearPermissions, ensurePermissions } from '../src/lib/authz'
import { useSession } from '../src/stores/session'

vi.mock('../src/lib/api', () => ({ api: vi.fn(), getSession: vi.fn(), sessionEnded: { blocked: false, handler: null } }))
vi.mock('../src/lib/theme', () => ({ restoreTheme: async () => {} }))
const person = (id: string): Identity => ({ principal: { id, name: id, kind: 'person' }, tenant: { id: 'tenant', name: 'Workspace' } })

beforeEach(() => {
  setActivePinia(createPinia())
  vi.resetAllMocks()
  sessionEnded.blocked = false; sessionEnded.handler = null
  clearPermissions()
  vi.mocked(api).mockImplementation(async () => new Response(JSON.stringify({ workspace: { role: null, permissions: ['account.manage'] }, project: null }), { status: 200 }))
})
afterEach(() => { useSession().$dispose(); sessionEnded.handler = null })

it('retains public sign-in config when the initial 401 invalidates the session before /me returns', async () => {
  const session = useSession()
  sessionEnded.handler = () => session.invalidate()
  vi.mocked(getSession).mockImplementation(async () => { sessionEnded.handler?.('/me'); return { identity: null, devMode: true, oidcDisplayName: 'Acme SSO' } })
  await session.refresh()
  expect(session.identity).toBeNull()
  expect(session.requiresSignIn).toBe(true)
  expect(session.devMode).toBe(true)
  expect(session.oidcDisplayName).toBe('Acme SSO')
  vi.mocked(getSession).mockResolvedValue({ identity: null, devMode: false })
  await session.refresh()
  expect(session.oidcDisplayName).toBe('Acme SSO')
  vi.mocked(getSession).mockResolvedValue({ identity: null, devMode: false, oidcDisplayName: '' })
  await session.refresh()
  expect(session.oidcDisplayName).toBe('')
})

it('a failed sign-out keeps the account available to retry and rechecks its permissions', async () => {
  const session = useSession()
  session.identity = person('ada')
  vi.mocked(api).mockImplementation(async path => new Response(path === '/auth/logout' ? null : JSON.stringify({ workspace: { role: null, permissions: ['account.manage'] }, project: null }), { status: path === '/auth/logout' ? 503 : 200 }))
  await expect(session.signOut()).rejects.toThrow('Sign out failed')
  expect(session.identity?.principal.id).toBe('ada')
  expect(session.requiresSignIn).toBe(false)
  await ensurePermissions()
  expect(can('account.manage')).toBe(true)
})

it('a late anonymous /me response cannot clear the person established by a newer explicit sign-in', async () => {
  const session = useSession()
  session.identity = person('ada'); session.devMode = true
  let finish!: (value: { identity: Identity | null; devMode: boolean }) => void
  vi.mocked(getSession).mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
  const previous = session.refresh()
  await session.devLogin('grace@example.test')
  vi.mocked(getSession).mockResolvedValue({ identity: person('grace'), devMode: true })
  await session.refresh()
  finish({ identity: null, devMode: false })
  await previous
  expect(session.identity?.principal.id).toBe('grace')
  expect(session.devMode).toBe(true)
})
