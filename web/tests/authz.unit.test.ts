// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, describe, expect, it, vi } from 'vitest'
import { accessChanged, can, clearPermissions, myPermissions, myWorkspaceRole, permissionsAvailable, permissionsKnown, refreshPermissions, revokePermissions } from '../src/lib/authz'
import { sessionEnded } from '../src/lib/api'

afterEach(() => { clearPermissions(); sessionEnded.blocked = false; vi.unstubAllGlobals() })

describe('can()', () => {
  it('effective grants decide actions independently of admin, viewer and guest role labels', async () => {
    for (const [label, permissions, expected] of [
      ['admin', [], false], ['viewer', ['nodes.write'], true], ['guest', ['nodes.read', 'comments.write'], false],
    ] as const) {
      clearPermissions()
      vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, json: async () => ({
        workspace: { role: { id: `role-${label}`, key: label, name: label }, permissions }, project: null,
      }) })))
      await refreshPermissions()
      expect(can('nodes.write'), label).toBe(expected)
      expect(can('keys.manage'), `${label} never implies key management`).toBe(false)
      expect(can('comments.write'), label).toBe(permissions.includes('comments.write' as never))
    }
  })
  it('fails closed until the server returns effective grants', async () => {
    const fetch = vi.fn(async () => ({ ok: true, json: async () => ({ workspace: { role: null, permissions: ['nodes.read'] }, project: null }) }))
    vi.stubGlobal('fetch', fetch)
    expect(can('nodes.read')).toBe(false)
    await refreshPermissions()
    expect(can('nodes.read')).toBe(true)
    expect(can('nodes.write')).toBe(false)
  })
  it('unions project and workspace permissions only for that project', async () => {
    const fetch = vi.fn(async (url: string) => ({ ok: true, json: async () => ({
      workspace: { role: null, permissions: ['nodes.read'] },
      project: url.includes('project_id=a') ? { id: 'a', role: null, permissions: ['comments.write'] } : null,
    }) }))
    vi.stubGlobal('fetch', fetch)
    await refreshPermissions('a')
    expect(can('nodes.read', 'a')).toBe(true)
    expect(can('comments.write', 'a')).toBe(true)
    expect(can('comments.write')).toBe(false)
    await refreshPermissions('b')
    expect(can('comments.write', 'b')).toBe(false)
  })
  it('drops revoked access after an access change or failed refresh', async () => {
    let allowed = true
    vi.stubGlobal('fetch', vi.fn(async () => allowed
      ? { ok: true, json: async () => ({ workspace: { role: null, permissions: ['keys.manage'] }, project: null }) }
      : { ok: false }))
    await refreshPermissions()
    expect(can('keys.manage')).toBe(true)
    allowed = false
    await accessChanged()
    expect(can('keys.manage')).toBe(false)
  })
  it('keeps the current answer while an access change is asked again (no flicker)', async () => {
    let finish!: (value: { ok: boolean; json: () => Promise<unknown> }) => void
    const fetch = vi.fn()
      .mockImplementationOnce(async () => ({ ok: true, json: async () => ({ workspace: { role: null, permissions: ['members.read'] }, project: null }) }))
      .mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    vi.stubGlobal('fetch', fetch)
    await refreshPermissions()
    const pending = accessChanged()
    expect(can('members.read')).toBe(true)
    finish({ ok: true, json: async () => ({ workspace: { role: null, permissions: [] }, project: null }) })
    await pending
    expect(can('members.read')).toBe(false)
  })
  it('ignores an answer started before sign-out', async () => {
    let finishOld!: (value: { ok: boolean; json: () => Promise<unknown> }) => void
    const old = new Promise<{ ok: boolean; json: () => Promise<unknown> }>(resolve => { finishOld = resolve })
    const fetch = vi.fn().mockImplementationOnce(() => old).mockImplementationOnce(async () => ({ ok: true, json: async () => ({ workspace: { role: null, permissions: [] }, project: null }) }))
    vi.stubGlobal('fetch', fetch)
    const pending = refreshPermissions()
    clearPermissions()
    await refreshPermissions()
    finishOld({ ok: true, json: async () => ({ workspace: { role: null, permissions: ['keys.manage'] }, project: null }) })
    await pending
    expect(can('keys.manage')).toBe(false)
  })
  it('Access reads my role and permissions from the same answer', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, json: async () => ({ workspace: { role: { id: 'r', key: 'member', name: 'Member' }, permissions: ['nodes.read', 'members.read'] }, project: null }) })))
    expect(permissionsKnown()).toBe(false)
    await refreshPermissions()
    expect(permissionsKnown()).toBe(true)
    expect(permissionsAvailable()).toBe(true)
    expect(myWorkspaceRole()?.name).toBe('Member')
    expect([...myPermissions()].sort()).toEqual(['members.read', 'nodes.read'])
  })
  it('a server without the endpoint is known, unavailable and grants nothing', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: false })))
    await refreshPermissions()
    expect(permissionsKnown()).toBe(true)
    expect(permissionsAvailable()).toBe(false)
    expect(myPermissions().size).toBe(0)
  })
  it('review #1: an answer still in flight when access changes never lands', async () => {
    let finishOld!: (value: { ok: boolean; json: () => Promise<unknown> }) => void
    const fetch = vi.fn()
      .mockImplementationOnce(() => new Promise(resolve => { finishOld = resolve }))
      .mockImplementationOnce(async () => ({ ok: true, json: async () => ({ workspace: { role: null, permissions: [] }, project: { id: 'p', role: null, permissions: [] } }) }))
      .mockImplementation(async () => ({ ok: true, json: async () => ({ workspace: { role: null, permissions: [] }, project: null }) }))
    vi.stubGlobal('fetch', fetch)
    expect(can('members.manage', 'p')).toBe(false) // asks for project p; the answer is slow
    const change = accessChanged() // my role was reduced meanwhile
    finishOld({ ok: true, json: async () => ({ workspace: { role: null, permissions: ['members.manage'] }, project: { id: 'p', role: null, permissions: ['members.manage'] } }) })
    await change
    expect(can('members.manage', 'p')).toBe(false)
  })
  it('review #2: a session that ended grants nothing and asks nothing until refreshed', async () => {
    const fetch = vi.fn(async () => ({ ok: true, json: async () => ({ workspace: { role: null, permissions: ['members.read'] }, project: null }) }))
    vi.stubGlobal('fetch', fetch)
    await refreshPermissions()
    expect(can('members.read')).toBe(true)
    revokePermissions()
    const asked = fetch.mock.calls.length
    expect(can('members.read')).toBe(false)
    expect(can('members.read', 'other')).toBe(false)
    expect(fetch.mock.calls.length).toBe(asked)
    await accessChanged()
    expect(fetch.mock.calls.length).toBe(asked)
    expect(can('members.read')).toBe(false)
    clearPermissions() // an explicit new session starts with fresh grants
    await refreshPermissions()
    expect(can('members.read')).toBe(true)
  })
  it('review r2: a 401 latches until a later 200, and never refetches by itself', async () => {
    let status = 401
    const fetch = vi.fn(async () => status === 200
      ? { ok: true, status, json: async () => ({ workspace: { role: null, permissions: ['members.read'] }, project: null }) }
      : { ok: false, status, json: async () => ({}) })
    vi.stubGlobal('fetch', fetch)
    await refreshPermissions()
    const asked = fetch.mock.calls.length
    expect(can('members.read')).toBe(false)
    expect(permissionsKnown()).toBe(true)
    await refreshPermissions('p')
    expect(fetch.mock.calls.length).toBe(asked) // latched: no self-refetch
    await accessChanged() // focus cannot retry an ended session
    expect(can('members.read')).toBe(false)
    status = 200
    await accessChanged()
    expect(fetch.mock.calls.length).toBe(asked)
    expect(can('members.read')).toBe(false)
    sessionEnded.blocked = false // explicit sign-in, followed by a fresh grant read
    clearPermissions()
    await refreshPermissions()
    expect(can('members.read')).toBe(true)
  })
})
