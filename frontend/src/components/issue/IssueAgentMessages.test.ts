/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'

vi.mock('@/services/agentMessages', () => ({
  loadIssueAgentMessages: vi.fn(),
  resolveHeldAgentMessage: vi.fn(),
}))

import {
  loadIssueAgentMessages,
  resolveHeldAgentMessage,
  type AgentMessage,
} from '@/services/agentMessages'
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
    expects_reply: false,
    created_at: '2026-08-25T10:00:00Z',
    ...overrides,
  }
}

describe('IssueAgentMessages security surfacing', () => {
  beforeEach(() => {
    vi.mocked(loadIssueAgentMessages).mockReset()
    vi.mocked(resolveHeldAgentMessage).mockReset()
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
    const app = createApp(IssueAgentMessages, { issueId: 817, projectId: 81, canEdit: false })
    app.mount(root)
    await flush()

    expect(root.textContent).toContain('Action request held: not delivered')
    expect(root.textContent).toContain('Human decision required')
    expect(root.textContent).toContain('Held: sender not in receiver allowlist')
    expect(root.textContent).toContain('Restart the service')
    expect(root.textContent).toContain('Project edit access is required to record a decision.')
    expect(root.querySelector('button')).toBeNull()
    expect(root.querySelectorAll('.agent-message')).toHaveLength(2)

    app.unmount()
    root.remove()
  })

  it('records an explicit immutable decision and removes the action controls', async () => {
    vi.mocked(loadIssueAgentMessages).mockResolvedValue([
      message({
        message_id: 'action',
        is_action_request: true,
        held_reason: 'action request - requires human approval',
      }),
    ])
    vi.mocked(resolveHeldAgentMessage).mockResolvedValue({
      message_id: 'action',
      outcome: 'resolved',
    })
    const root = document.createElement('div')
    document.body.appendChild(root)
    const app = createApp(IssueAgentMessages, { issueId: 817, projectId: 81, canEdit: true })
    app.mount(root)
    await flush()

    const buttons = root.querySelectorAll<HTMLButtonElement>('button')
    expect(Array.from(buttons).map((button) => button.textContent?.trim())).toEqual([
      'Mark resolved',
      'Dismiss request',
    ])
    expect(root.textContent).toContain(
      'A decision records disposition only. It does not execute or deliver the request.',
    )
    buttons[0].click()
    await flush()

    expect(resolveHeldAgentMessage).toHaveBeenCalledTimes(1)
    expect(resolveHeldAgentMessage).toHaveBeenCalledWith(
      81,
      'action',
      'resolved',
      expect.any(String),
    )
    expect(root.textContent).toContain('Human decision recorded: resolved')
    expect(root.textContent).toContain('Action request held: not delivered')
    expect(root.textContent).toContain(
      'A decision records disposition only. It does not execute or deliver the request.',
    )
    expect(root.textContent).not.toContain('Human decision required')
    expect(root.querySelector('button')).toBeNull()

    app.unmount()
    root.remove()
  })

  it('uses one retry key and surfaces a fixed content-free failure', async () => {
    const held = message({
      message_id: 'action',
      is_action_request: true,
      held_reason: 'action request - requires human approval',
      parts: [{ kind: 'text', text: 'private-but-authorized issue context' }],
    })
    vi.mocked(loadIssueAgentMessages).mockResolvedValue([held])
    vi.mocked(resolveHeldAgentMessage).mockRejectedValue(
      new Error('server echoed private-but-authorized issue context'),
    )
    const root = document.createElement('div')
    document.body.appendChild(root)
    const app = createApp(IssueAgentMessages, { issueId: 817, projectId: 81, canEdit: true })
    app.mount(root)
    await flush()

    root.querySelector<HTMLButtonElement>('button')!.click()
    await flush()
    root.querySelector<HTMLButtonElement>('button')!.click()
    await flush()

    expect(resolveHeldAgentMessage).toHaveBeenCalledTimes(2)
    const firstKey = vi.mocked(resolveHeldAgentMessage).mock.calls[0][3]
    expect(firstKey).toBeTruthy()
    expect(vi.mocked(resolveHeldAgentMessage).mock.calls[1][3]).toBe(firstKey)
    const alert = root.querySelector('[role="alert"]')
    expect(alert?.textContent).toContain(
      'Decision was not recorded. Check your access and try again.',
    )
    expect(alert?.textContent).not.toContain('private-but-authorized')

    app.unmount()
    root.remove()
  })
})
