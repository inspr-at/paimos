import { describe, expect, it } from 'vitest'

import { userDisplayName, userInitials } from './userDisplay'

describe('user display identity', () => {
  it('uses the first and last word of a human display name', () => {
    const user = {
      username: 'dev_admin',
      nickname: 'Mara Ellis',
      first_name: 'Mara',
      last_name: 'Ellis',
    }

    expect(userDisplayName(user)).toBe('Mara Ellis')
    expect(userInitials(user)).toBe('ME')
  })

  it('turns a segmented login handle into two initials', () => {
    expect(userInitials({ username: 'dev_admin' })).toBe('DA')
    expect(userInitials({ username: 'debug-admin' })).toBe('DA')
    expect(
      userInitials({ username: 'dev_editor', first_name: 'dev_editor', last_name: 'Dev' }),
    ).toBe('DE')
  })

  it('caps a single-word identity at two characters', () => {
    expect(userInitials({ username: 'developer' })).toBe('DE')
  })

  it('returns the requested fallback for an absent identity', () => {
    expect(userInitials(null)).toBe('?')
    expect(userInitials(undefined, '··')).toBe('··')
  })
})
