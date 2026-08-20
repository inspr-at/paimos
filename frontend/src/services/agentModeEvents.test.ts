/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 */

import { describe, expect, it, vi } from 'vitest'

import {
  AGENT_MODE_EVENTS_PATH,
  agentModeStreamBindingKey,
  buildAgentModeEventsURL,
  openAgentModeEventStream,
  type AgentModeEventSourceLike,
  type AgentModeMessageEvent,
} from './agentModeEvents'

class FakeEventSource implements AgentModeEventSourceLike {
  readonly listeners = new Map<string, Array<(event: AgentModeMessageEvent) => void>>()
  onerror: ((event: unknown) => void) | null = null
  close = vi.fn()

  addEventListener(type: string, listener: (event: AgentModeMessageEvent) => void) {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener])
  }

  emit(type: string, data: unknown, lastEventId = 'A'.repeat(211)) {
    for (const listener of this.listeners.get(type) ?? []) listener({ data, lastEventId })
  }
}

function callbacks() {
  return {
    onOpen: vi.fn(),
    onRefetch: vi.fn(),
    onCheckpoint: vi.fn(),
    onReset: vi.fn(),
    onInvalid: vi.fn(),
    onError: vi.fn(),
  }
}

describe('agentModeEvents — dedicated invalidation stream', () => {
  it('uses the absolute /api endpoint and excludes selection from binding and URL', () => {
    const query = {
      projectId: 6,
      states: ['pending', 'active', 'active'] as const,
      health: 'blocked' as const,
      q: 'ship',
      selectedDelivery: 'issue:42',
    }
    expect(AGENT_MODE_EVENTS_PATH).toBe('/api/agent-mode/deliveries/events')
    expect(agentModeStreamBindingKey(query))
      .toBe('/api/agent-mode/deliveries/events?project_id=6&state=active&state=pending&health=blocked&q=ship')
    const cursor = `${'C'.repeat(210)}A`
    expect(buildAgentModeEventsURL(query, cursor))
      .toBe(`/api/agent-mode/deliveries/events?project_id=6&state=active&state=pending&health=blocked&q=ship&cursor=${cursor}`)
    expect(() => buildAgentModeEventsURL(query, 'legacy-cursor')).toThrow(/cursor/)
    expect(() => buildAgentModeEventsURL(query, `${'C'.repeat(210)}B`)).toThrow(/cursor/)
  })

  it('validates refetch batches and treats their bounded hints only as a fetch signal', () => {
    const source = new FakeEventSource()
    const cb = callbacks()
    openAgentModeEventStream({}, `${'B'.repeat(210)}A`, cb, () => source)
    source.emit('refetch', JSON.stringify({
      schema_version: 1,
      hints: [{ delivery_id: 'issue:1', delivery_revision: 8, change_sequence: 12 }],
    }))
    expect(cb.onRefetch).toHaveBeenCalledWith([
      { deliveryId: 'issue:1', deliveryRevision: 8, changeSequence: 12 },
    ])
    expect(cb.onInvalid).not.toHaveBeenCalled()
  })

  it('accepts the same optional-hints batch for checkpoint without fetching', () => {
    const source = new FakeEventSource()
    const cb = callbacks()
    openAgentModeEventStream({}, `${'B'.repeat(210)}A`, cb, () => source)
    source.emit('checkpoint', JSON.stringify({
      schema_version: 1,
      hints: [{ delivery_id: 'issue:1', delivery_revision: 8, change_sequence: 12 }],
    }))
    expect(cb.onCheckpoint).toHaveBeenCalledOnce()
    expect(cb.onRefetch).not.toHaveBeenCalled()
    expect(cb.onInvalid).not.toHaveBeenCalled()
  })

  it('accepts a generic reset even when native EventSource carries the prior lastEventId', () => {
    const source = new FakeEventSource()
    const cb = callbacks()
    openAgentModeEventStream({}, `${'B'.repeat(210)}A`, cb, () => source)
    source.emit('reset', JSON.stringify({ schema_version: 1, reason: 'resync_required' }), 'Z'.repeat(211))
    expect(cb.onReset).toHaveBeenCalledOnce()
    expect(cb.onInvalid).not.toHaveBeenCalled()
  })

  it('fails closed on malformed registered/default events but ignores unknown named events', () => {
    const source = new FakeEventSource()
    const cb = callbacks()
    openAgentModeEventStream({}, `${'B'.repeat(210)}A`, cb, () => source)
    source.emit('refetch', JSON.stringify({ schema_version: 1, secret: true }))
    source.emit('checkpoint', JSON.stringify({ schema_version: 1, hints: [{ delivery_id: 'bad id', delivery_revision: 1, change_sequence: 1 }] }))
    source.emit('refetch', JSON.stringify({ schema_version: 1, hints: [{ delivery_id: ' issue:1 ', delivery_revision: 1, change_sequence: 1 }] }))
    source.emit('reset', JSON.stringify({ schema_version: 1, reason: 'other' }))
    source.emit('message', '{}')
    source.emit('refetch', JSON.stringify({ schema_version: 1 }), `${'A'.repeat(210)}B`)
    source.emit('future-event', '{malformed')
    expect(cb.onInvalid).toHaveBeenCalledTimes(6)
  })

  it('generation-fences every callback after idempotent close', () => {
    const source = new FakeEventSource()
    const cb = callbacks()
    const stream = openAgentModeEventStream({}, `${'B'.repeat(210)}A`, cb, () => source)
    stream.close()
    stream.close()
    source.emit('refetch', JSON.stringify({ schema_version: 1 }))
    source.emit('reset', JSON.stringify({ schema_version: 1, reason: 'resync_required' }))
    source.onerror?.(new Error('late'))
    expect(source.close).toHaveBeenCalledOnce()
    expect(cb.onRefetch).not.toHaveBeenCalled()
    expect(cb.onReset).not.toHaveBeenCalled()
    expect(cb.onError).not.toHaveBeenCalled()
  })

  it('shares exhaustive canonical terminal-bit validation for refetch and checkpoint ids', () => {
    const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_'
    const source = new FakeEventSource()
    const cb = callbacks()
    openAgentModeEventStream({}, `${'A'.repeat(210)}A`, cb, () => source)
    for (let index = 0; index < alphabet.length; index += 4) {
      const canonical = `${'A'.repeat(210)}${alphabet[index]}`
      source.emit('refetch', JSON.stringify({ schema_version: 1 }), canonical)
      source.emit('checkpoint', JSON.stringify({ schema_version: 1 }), canonical)
      for (let aliasOffset = 1; aliasOffset <= 3; aliasOffset += 1) {
        const alias = `${'A'.repeat(210)}${alphabet[index + aliasOffset]}`
        source.emit('refetch', JSON.stringify({ schema_version: 1 }), alias)
        source.emit('checkpoint', JSON.stringify({ schema_version: 1 }), alias)
      }
    }
    expect(cb.onRefetch).toHaveBeenCalledTimes(16)
    expect(cb.onCheckpoint).toHaveBeenCalledTimes(16)
    expect(cb.onInvalid).toHaveBeenCalledTimes(96)
  })
})
