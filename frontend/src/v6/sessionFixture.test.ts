import { describe, expect, it } from 'vitest'

import { PAIMOS6_PREVIEW_CONTRACT, PAIMOS6_SESSION_FIXTURES } from './sessionFixture'

describe('Paimos 6 deterministic preview contract (PAI-854)', () => {
  it('is explicitly local, non-live, nullable, responsive-web-only, and mutation-free', () => {
    expect(PAIMOS6_PREVIEW_CONTRACT).toEqual({
      source: 'deterministic-local-fixture',
      live: false,
      initialSelection: null,
      mutationMode: 'local-noop',
      mobileSurface: 'responsive-web-stub',
      nativeAvailable: false,
      pushAvailable: false,
    })
  })

  it('covers attached and loose sessions, attention, inbox, status, mode, agent, and capabilities', () => {
    expect(PAIMOS6_SESSION_FIXTURES.some((session) => session.node === null)).toBe(true)
    expect(PAIMOS6_SESSION_FIXTURES.some((session) => session.node !== null)).toBe(true)
    expect(PAIMOS6_SESSION_FIXTURES.some((session) => session.attention)).toBe(true)
    expect(PAIMOS6_SESSION_FIXTURES.some((session) => session.unread > 0)).toBe(true)
    expect(PAIMOS6_SESSION_FIXTURES.every((session) => session.statusLabel && session.agent)).toBe(true)
    expect(PAIMOS6_SESSION_FIXTURES.some((session) => session.mode === 'managed')).toBe(true)
    expect(PAIMOS6_SESSION_FIXTURES.some((session) => session.mode === 'unmanaged')).toBe(true)
    expect(PAIMOS6_SESSION_FIXTURES.every((session) => session.capabilities)).toBe(true)
  })

  it('never advertises owned direct controls for the unmanaged CLI fixture', () => {
    const unmanaged = PAIMOS6_SESSION_FIXTURES.find((session) => session.mode === 'unmanaged')!
    expect(unmanaged.capabilities.directSteer).toBe(false)
    expect(unmanaged.capabilities.interrupt).toBe(false)
    expect(unmanaged.capabilities.stop).toBe(false)
    expect(unmanaged.capabilities.paimosSteer).toBe(true)
  })
})
