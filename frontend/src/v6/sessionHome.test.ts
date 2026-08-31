import { describe, expect, it, vi } from 'vitest'

import { api } from '@/api/client'
import {
  Paimos6SessionHomeContractError,
  loadPaimos6SessionHome,
  parsePaimos6SessionHome,
  toPaimos6SessionViewModel,
} from './sessionHome'

function managedRow(id = '17e5d8f7-0b11-4bee-a8a4-a11406de865a') {
  return {
    product_session_id: id,
    title: 'Shape the six home',
    summary: 'Checking the live session seam.',
    revision: 1,
    updated_at: '2026-08-30T12:02:00.000Z',
    target: {
      kind: 'project_agent',
      project_agent_id: 7,
      agent_name: 'amy',
      address: 'codex:amy',
    },
    status: { phase: 'working', reason: 'active' },
    harness: {
      harness: 'codex',
      management_mode: 'managed',
      advertised_capabilities: {
        inbox: true,
        status: true,
        steer: true,
        interrupt: true,
        stop: true,
      },
    },
    controls: { steer: 'direct', interrupt: true, stop: true },
    node: { node_id: 854, node_key: 'PAI-854', label: 'PAI-854 · Paimos 6.0 cut' },
    inbox: { unread_count: 1, latest_unread_at: '2026-08-30T11:58:00Z' },
    attention: { required: false, exception_count: 0, action_request_count: 0, reason: null },
  }
}

function unmanagedRow() {
  return {
    ...managedRow('27e5d8f7-0b11-4bee-a8a4-a11406de865a'),
    title: 'Second session on the same node',
    updated_at: '2026-08-30T12:01:00Z',
    target: {
      kind: 'project_agent',
      project_agent_id: 8,
      agent_name: 'amy',
      address: 'claude:amy',
    },
    harness: {
      harness: 'claude',
      management_mode: 'unmanaged',
      advertised_capabilities: {
        inbox: true,
        status: true,
        steer: false,
        interrupt: false,
        stop: false,
      },
    },
    controls: { steer: 'paimos_nudge', interrupt: false, stop: false },
    inbox: { unread_count: 1, latest_unread_at: '2026-08-30T11:58:00Z' },
    attention: { required: true, exception_count: 2, action_request_count: 1, reason: 'action_request' },
  }
}

function sharedManagedRow() {
  return {
    ...managedRow('27e5d8f7-0b11-4bee-a8a4-a11406de865a'),
    title: 'Second session for the same canonical target',
    updated_at: '2026-08-30T12:01:00Z',
  }
}

function unavailableRow() {
  return {
    ...managedRow(),
    target: {
      kind: 'project_agent',
      project_agent_id: 7,
      agent_name: 'amy',
      address: null,
    },
    status: { phase: 'unavailable', reason: 'stale_harness' },
    harness: null,
    controls: { steer: 'paimos_nudge', interrupt: false, stop: false },
  }
}

function paimosRow() {
  return {
    ...managedRow('37e5d8f7-0b11-4bee-a8a4-a11406de865a'),
    title: 'Loose Paimos session',
    summary: '',
    updated_at: '2026-08-30T12:00:00Z',
    target: { kind: 'paimos', project_agent_id: null, agent_name: null, address: 'paimos' },
    status: { phase: 'paimos', reason: 'paimos_target' },
    harness: null,
    controls: { steer: 'paimos_nudge', interrupt: false, stop: false },
    node: null,
    inbox: { unread_count: 0, latest_unread_at: null },
  }
}

interface ProjectionTestRow {
  target: { kind: string; project_agent_id: number | null }
  inbox: { unread_count: number }
  attention: { required: boolean }
}

function projection(sessions: ProjectionTestRow[] = [managedRow(), unmanagedRow(), paimosRow()]) {
  const inboxByTarget = new Map<string, number>()
  for (const row of sessions) {
    const targetKey = row.target.kind === 'paimos' ? 'paimos' : `project-agent:${row.target.project_agent_id}`
    inboxByTarget.set(targetKey, row.inbox.unread_count)
  }
  return {
    schema_version: 1,
    project_id: 42,
    sessions,
    totals: {
      sessions: sessions.length,
      unread: [...inboxByTarget.values()].reduce((sum, unread) => sum + unread, 0),
      attention: sessions.filter((row) => row.attention.required).length,
    },
  }
}

describe('Paimos 6 session-home strict boundary (PAI-861)', () => {
  it('accepts the exact closed wire shape and preserves loose/many-per-node identity', () => {
    const parsed = parsePaimos6SessionHome(projection(), 42)
    expect(parsed.sessions).toHaveLength(3)
    expect(parsed.sessions[0].node?.node_id).toBe(854)
    expect(parsed.sessions[1].node?.node_id).toBe(854)
    expect(parsed.sessions[0].product_session_id).not.toBe(parsed.sessions[1].product_session_id)
    expect(parsed.sessions[2].node).toBeNull()
  })

  it('maps only returned truth into the card view model', () => {
    const parsed = parsePaimos6SessionHome(projection(), 42)
    const managed = toPaimos6SessionViewModel(parsed.sessions[0])
    const unmanaged = toPaimos6SessionViewModel(parsed.sessions[1])
    const paimos = toPaimos6SessionViewModel(parsed.sessions[2])

    expect(managed.capabilities).toEqual({ directSteer: true, interrupt: true, stop: true, paimosSteer: false })
    expect(unmanaged.mode).toBe('unmanaged')
    expect(unmanaged.capabilities).toEqual({ directSteer: false, interrupt: false, stop: false, paimosSteer: true })
    expect(unmanaged.attentionReason).toContain('action request')
    expect(paimos).toMatchObject({ agent: 'Paimos', mode: 'paimos', summary: '' })
  })

  it.each([
    ['unknown root key', () => ({ ...projection(), extra: true })],
    ['wrong project', () => ({ ...projection(), project_id: 43 })],
    ['lying totals', () => ({ ...projection(), totals: { sessions: 99, unread: 1, attention: 1 } })],
    ['wrong ordering', () => projection([unmanagedRow(), managedRow(), paimosRow()])],
    ['duplicate product identity', () => projection([managedRow(), managedRow()])],
  ])('rejects %s before exposing rows', (_label, mutate) => {
    expect(() => parsePaimos6SessionHome(mutate(), 42)).toThrow(Paimos6SessionHomeContractError)
  })

  it('rejects controls that exceed harness management/capability truth', () => {
    const corrupt = unmanagedRow()
    corrupt.controls.interrupt = true
    expect(() => parsePaimos6SessionHome(projection([corrupt]), 42)).toThrow(Paimos6SessionHomeContractError)
  })

  it('requires one coherent canonical project-agent address when a harness is present', () => {
    const contradictoryAddress = managedRow()
    contradictoryAddress.target.address = 'claude:other'
    expect(() => parsePaimos6SessionHome(projection([contradictoryAddress]), 42))
      .toThrow(Paimos6SessionHomeContractError)

    const contradictoryHarness = managedRow()
    contradictoryHarness.harness.harness = 'claude'
    expect(() => parsePaimos6SessionHome(projection([contradictoryHarness]), 42))
      .toThrow(Paimos6SessionHomeContractError)

    const missingCanonicalAddress = {
      ...managedRow(),
      target: { ...managedRow().target, address: null },
    }
    expect(() => parsePaimos6SessionHome(projection([missingCanonicalAddress]), 42))
      .toThrow(Paimos6SessionHomeContractError)
  })

  it('accepts exact unavailable null identity and rejects non-null or Paimos contradictions', () => {
    expect(parsePaimos6SessionHome(projection([unavailableRow()]), 42).sessions[0])
      .toMatchObject({ target: { address: null }, harness: null, status: { phase: 'unavailable' } })

    const unavailableWithAddress = {
      ...unavailableRow(),
      target: { ...unavailableRow().target, address: 'codex:amy' },
    }
    expect(() => parsePaimos6SessionHome(projection([unavailableWithAddress]), 42))
      .toThrow(Paimos6SessionHomeContractError)

    const contradictoryPaimos = {
      ...paimosRow(),
      target: { ...paimosRow().target, address: 'codex:paimos' },
    }
    expect(() => parsePaimos6SessionHome(projection([contradictoryPaimos]), 42))
      .toThrow(Paimos6SessionHomeContractError)

    const paimosWithHarness = { ...paimosRow(), harness: managedRow().harness }
    expect(() => parsePaimos6SessionHome(projection([paimosWithHarness]), 42))
      .toThrow(Paimos6SessionHomeContractError)
  })

  it('counts a shared target inbox once and rejects divergent copies', () => {
    const parsed = parsePaimos6SessionHome(projection([managedRow(), sharedManagedRow()]), 42)
    expect(parsed.totals.unread).toBe(1)
    const divergent = sharedManagedRow()
    divergent.inbox = { unread_count: 2, latest_unread_at: '2026-08-30T11:58:00Z' }
    expect(() => parsePaimos6SessionHome(projection([managedRow(), divergent]), 42))
      .toThrow(Paimos6SessionHomeContractError)
  })

  it('accepts an exact empty projection', () => {
    expect(parsePaimos6SessionHome(projection([]), 42)).toEqual({
      schema_version: 1,
      project_id: 42,
      sessions: [],
      totals: { sessions: 0, unread: 0, attention: 0 },
    })
  })

  it('loads only the exact project-scoped v1 endpoint', async () => {
    const get = vi.spyOn(api, 'get').mockResolvedValue(projection() as never)
    const controller = new AbortController()
    await expect(loadPaimos6SessionHome(42, controller.signal)).resolves.toMatchObject({ projectId: 42 })
    expect(get).toHaveBeenCalledWith('/projects/42/session-home/v1', { signal: controller.signal })
    get.mockRestore()
  })
})
