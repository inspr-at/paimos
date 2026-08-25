/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'

vi.mock('@/services/agentMessages', () => ({
  loadIssueAgentMessages: vi.fn(),
}))

import { loadIssueAgentMessages, type AgentMessage } from '@/services/agentMessages'
import IssueAgentMessages from './IssueAgentMessages.vue'

async function flush() {
  for (let i = 0; i < 8; i += 1) {
    await Promise.resolve()
    await nextTick()
  }
}

function message(overrides: Partial<AgentMessage>): AgentMessage {
  return {
    cursor: 1,
    message_id: 'm1',
    context_id: 'PAI',
    task_id: 'PAI-817',
    from: 'paimos:sender',
    to: 'codex:receiver',
    thread_id: 't1',
    hop: 1,
    parts: [{ kind: 'text', text: 'Untrusted message body' }],
    delivered: false,
    held_reason: 'sender not in receiver allowlist',
    is_action_request: false,
    created_at: '2026-08-25T10:00:00Z',
    ...overrides,
  }
}

describe('IssueAgentMessages security surfacing', () => {
  beforeEach(() => {
    vi.mocked(loadIssueAgentMessages).mockReset()
  })

  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('labels explicit action requests and unauthorized messages as held for the human', async () => {
    vi.mocked(loadIssueAgentMessages).mockResolvedValue([
      message({
        message_id: 'action',
        is_action_request: true,
        held_reason: 'action request - requires human approval',
        parts: [{ kind: 'text', text: 'Restart the service' }],
      }),
      message({ message_id: 'unlisted' }),
    ])
    const root = document.createElement('div')
    document.body.appendChild(root)
    const app = createApp(IssueAgentMessages, { issueId: 817 })
    app.mount(root)
    await flush()

    expect(root.textContent).toContain('Action request held: human approval required')
    expect(root.textContent).toContain('Held: sender not in receiver allowlist')
    expect(root.textContent).toContain('Restart the service')
    expect(root.querySelectorAll('.agent-message')).toHaveLength(2)

    app.unmount()
    root.remove()
  })
})
