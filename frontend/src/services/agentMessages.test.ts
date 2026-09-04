/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 */
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/api/client', () => ({
  api: { get: vi.fn(), post: vi.fn() },
}))

import { api } from '@/api/client'
import { resolveHeldAgentMessage } from './agentMessages'

describe('held agent message resolution transport', () => {
  beforeEach(() => vi.clearAllMocks())

  it('posts one explicit outcome and caller-owned idempotency key', async () => {
    vi.mocked(api.post).mockResolvedValue({ message_id: 'message/id', outcome: 'dismissed' })

    await resolveHeldAgentMessage(42, 'message/id', 'dismissed', 'retry-key')

    expect(api.post).toHaveBeenCalledWith(
      '/projects/42/messages/message%2Fid/resolution',
      { outcome: 'dismissed' },
      { headers: { 'Idempotency-Key': 'retry-key' } },
    )
  })
})
