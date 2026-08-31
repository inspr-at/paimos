import { describe, expect, it } from 'vitest'

import {
  paimos6SelectionStorageKey,
  persistPaimos6Selection,
  resolvePaimos6Selection,
  type Paimos6SelectionScope,
} from './sessionHomeSelection'

function memoryStorage() {
  const values = new Map<string, string>()
  return {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => void values.set(key, value),
    removeItem: (key: string) => void values.delete(key),
    dump: () => Object.fromEntries(values),
  }
}

const scope: Paimos6SelectionScope = { principalId: 7, projectId: 41 }
const ids = ['session-a', 'session-b', 'session-c']

describe('Paimos 6 session-home selection (PAI-861)', () => {
  it('stays null without an authorized deep link or stored choice', () => {
    expect(resolvePaimos6Selection({
      scope,
      authorizedIds: ids,
      deepLinkedId: null,
      currentId: null,
      storage: memoryStorage(),
    })).toEqual({ id: null, source: 'none', clearInvalidDeepLink: false })
  })

  it('prefers a valid deep link to a stored selection', () => {
    const storage = memoryStorage()
    persistPaimos6Selection(storage, scope, 'session-a')

    expect(resolvePaimos6Selection({
      scope,
      authorizedIds: ids,
      deepLinkedId: 'session-c',
      currentId: null,
      storage,
    })).toEqual({ id: 'session-c', source: 'deep-link', clearInvalidDeepLink: false })
  })

  it('restores project-scoped session memory only when no deep link exists', () => {
    const storage = memoryStorage()
    persistPaimos6Selection(storage, scope, 'session-b')

    expect(resolvePaimos6Selection({
      scope,
      authorizedIds: ids,
      deepLinkedId: null,
      currentId: null,
      storage,
    })).toEqual({ id: 'session-b', source: 'stored', clearInvalidDeepLink: false })
  })

  it('clears invalid deep links and invalid stored choices instead of defaulting', () => {
    const storage = memoryStorage()
    persistPaimos6Selection(storage, scope, 'not-authorized')

    expect(resolvePaimos6Selection({
      scope,
      authorizedIds: ids,
      deepLinkedId: 'also-not-authorized',
      currentId: null,
      storage,
    })).toEqual({ id: null, source: 'none', clearInvalidDeepLink: true })

    expect(resolvePaimos6Selection({
      scope,
      authorizedIds: ids,
      deepLinkedId: null,
      currentId: null,
      storage,
    }).id).toBeNull()
    expect(storage.dump()).toEqual({})
  })

  it('keeps an authorized current selection across arbitrary row reorder', () => {
    expect(resolvePaimos6Selection({
      scope,
      authorizedIds: [...ids].reverse(),
      deepLinkedId: null,
      currentId: 'session-b',
      storage: memoryStorage(),
    })).toEqual({ id: 'session-b', source: 'current', clearInvalidDeepLink: false })
  })

  it('names storage by both principal and project and clearing is scoped', () => {
    const storage = memoryStorage()
    const otherProject = { ...scope, projectId: 99 }
    const otherPrincipal = { ...scope, principalId: 8 }
    persistPaimos6Selection(storage, scope, 'session-a')
    persistPaimos6Selection(storage, otherProject, 'session-b')
    persistPaimos6Selection(storage, otherPrincipal, 'session-c')

    persistPaimos6Selection(storage, scope, null)

    expect(storage.dump()).toEqual({
      [paimos6SelectionStorageKey(otherProject)]: 'session-b',
      [paimos6SelectionStorageKey(otherPrincipal)]: 'session-c',
    })
  })
})
