// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest'
import { touchIDConfirmation } from '../src/lib/agentPairing'

describe('Touch ID pairing readiness', () => {
  it('requires a server pin and keeps unknown distinct from missing', () => {
    const base = { platform: 'darwin', computer_state: 'connected' as const }
    expect(touchIDConfirmation({ ...base, local_auth_pinned: true })).toBe('Touch ID confirmation: ready (pairing key pinned)')
    expect(touchIDConfirmation({ ...base, local_auth_pinned: false })).toContain('needs pairing upgrade — run aeon-agentd status')
    expect(touchIDConfirmation(base)).toContain('readiness unknown')
    expect(touchIDConfirmation({ ...base, computer_state: 'revoked', local_auth_pinned: true })).toContain('unavailable')
    expect(touchIDConfirmation({ ...base, platform: 'linux' })).toContain('unsupported')
  })
})
