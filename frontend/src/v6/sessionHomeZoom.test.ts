import { describe, expect, it, vi } from 'vitest'

import { api } from '@/api/client'
import {
  canonicalPaimos6Zoom,
  decrementPaimos6Zoom,
  incrementPaimos6Zoom,
  loadPaimos6SessionZoom,
  paimos6ZoomBand,
  paimos6ZoomSampleLimit,
  parsePaimos6SessionZoom,
  Paimos6SessionZoomContractError,
} from './sessionHomeZoom'

const MANAGED_ID = '17e5d8f7-0b11-4bee-a8a4-a11406de865a'
const ATTENTION_ID = '27e5d8f7-0b11-4bee-a8a4-a11406de865a'
const PAIMOS_ID = '37e5d8f7-0b11-4bee-a8a4-a11406de865a'

function managedRow(id = MANAGED_ID) {
  return {
    product_session_id: id,
    title: 'Managed session',
    summary: 'Live truth',
    revision: 3,
    updated_at: '2026-08-30T12:02:00Z',
    target: { kind: 'project_agent', project_agent_id: 7, agent_name: 'amy', address: 'codex:amy' },
    status: { phase: 'working', reason: 'active' },
    harness: {
      harness: 'codex',
      management_mode: 'managed',
      advertised_capabilities: { inbox: true, status: true, steer: true, interrupt: true, stop: true },
    },
    controls: { steer: 'direct', interrupt: true, stop: true },
    node: null,
    inbox: { unread_count: 1, latest_unread_at: '2026-08-30T11:58:00Z' },
    attention: { required: false, exception_count: 0, action_request_count: 0, reason: null },
  }
}

function attentionRow() {
  return {
    ...managedRow(ATTENTION_ID),
    title: 'Exception target',
    target: { kind: 'project_agent', project_agent_id: 8, agent_name: 'jan', address: 'claude:jan' },
    harness: {
      harness: 'claude',
      management_mode: 'unmanaged',
      advertised_capabilities: { inbox: true, status: true, steer: false, interrupt: false, stop: false },
    },
    controls: { steer: 'paimos_nudge', interrupt: false, stop: false },
    inbox: { unread_count: 0, latest_unread_at: null },
    attention: { required: true, exception_count: 2, action_request_count: 1, reason: 'action_request' },
  }
}

function paimosRow() {
  return {
    ...managedRow(PAIMOS_ID),
    title: 'Paimos session',
    target: { kind: 'paimos', project_agent_id: null, agent_name: null, address: 'paimos' },
    status: { phase: 'paimos', reason: 'paimos_target' },
    harness: null,
    controls: { steer: 'paimos_nudge', interrupt: false, stop: false },
    inbox: { unread_count: 0, latest_unread_at: null },
  }
}

type TestRow = ReturnType<typeof managedRow> | ReturnType<typeof attentionRow> | ReturnType<typeof paimosRow>

function projection(options: {
  zoom?: string
  sessions?: TestRow[]
  selected?: unknown
  totalSessions?: number
} = {}) {
  const zoom = options.zoom ?? '10'
  const sessions = options.sessions ?? [attentionRow(), managedRow()]
  const selected = options.selected ?? null
  const totalSessions = options.totalSessions ?? sessions.length
  const sampledExceptionalTargets = new Set(sessions
    .filter((row) => row.target.kind === 'project_agent' && row.attention.required)
    .map((row) => row.target.project_agent_id))
  return {
    schema_version: 1,
    project_id: 42,
    zoom,
    band: paimos6ZoomBand(zoom),
    sample_limit: paimos6ZoomSampleLimit(zoom),
    sample_truncated: totalSessions > sessions.length,
    sessions,
    selected_session: selected,
    totals: {
      sessions: totalSessions,
      unread: 1,
      attention_sessions: 1,
      exception_messages: 2,
      action_requests: 1,
      exception_targets: 1,
      sampled_exception_targets: sampledExceptionalTargets.size,
    },
  }
}

describe('Paimos 6 semantic-zoom strict boundary (PAI-864)', () => {
  it('keeps canonical zoom arithmetic lexical through the 64-digit far band', () => {
    const far = '9'.repeat(64)
    expect(canonicalPaimos6Zoom(far)).toEqual({ zoom: far, replace: false })
    expect(paimos6ZoomBand(far)).toBe('far')
    expect(paimos6ZoomSampleLimit(far)).toBe(100)
    expect(incrementPaimos6Zoom(far)).toBe(far)
    expect(incrementPaimos6Zoom('999')).toBe('1000')
    expect(decrementPaimos6Zoom('1000')).toBe('999')
    expect(paimos6ZoomSampleLimit('99')).toBe(99)
  })

  it.each([null, '', '0', '01', '+1', '1.5', '1e3', ' 10', '1'.repeat(65), ['10']])(
    'canonicalizes invalid route zoom %j to one replace value',
    (raw) => expect(canonicalPaimos6Zoom(raw)).toEqual({ zoom: '10', replace: true }),
  )

  it('accepts bounded samples and a separately hydrated selected row', () => {
    const wire = projection({ selected: paimosRow(), totalSessions: 9 })
    const parsed = parsePaimos6SessionZoom(wire, 42, '10', PAIMOS_ID)
    expect(parsed.sessions.map((row) => row.product_session_id)).toEqual([ATTENTION_ID, MANAGED_ID])
    expect(parsed.selected_session?.product_session_id).toBe(PAIMOS_ID)
    expect(parsed.sample_truncated).toBe(true)
  })

  it('requires byte-equivalent normalized truth when selection is already sampled', () => {
    const exact = projection({ selected: attentionRow() })
    expect(parsePaimos6SessionZoom(exact, 42, '10', ATTENTION_ID).selected_session).toEqual(attentionRow())
    const contradictory = projection({ selected: { ...attentionRow(), title: 'Contradictory copy' } })
    expect(() => parsePaimos6SessionZoom(contradictory, 42, '10', ATTENTION_ID))
      .toThrow(Paimos6SessionZoomContractError)
  })

  it.each([
    ['extra root key', () => ({ ...projection(), extra: true })],
    ['wrong band', () => ({ ...projection(), band: 'far' })],
    ['wrong limit', () => ({ ...projection(), sample_limit: 99 })],
    ['too many sampled rows', () => projection({ zoom: '1', sessions: [attentionRow(), managedRow()] })],
    ['lying truncation', () => ({ ...projection({ totalSessions: 3 }), sample_truncated: false })],
    ['sample count above total', () => projection({ totalSessions: 1 })],
    ['lying sampled targets', () => ({
      ...projection(),
      totals: { ...projection().totals, sampled_exception_targets: 0 },
    })],
    ['unsafe total', () => ({ ...projection(), totals: { ...projection().totals, sessions: Number.MAX_SAFE_INTEGER + 1 } })],
    ['Paimos message counts', () => {
      const corrupt = { ...paimosRow(), inbox: { unread_count: 1, latest_unread_at: '2026-08-30T11:58:00Z' } }
      return projection({ sessions: [corrupt as unknown as TestRow], totalSessions: 1 })
    }],
  ])('rejects %s before exposing rows', (_label, mutate) => {
    expect(() => parsePaimos6SessionZoom(mutate(), 42, '10', null))
      .toThrow(Paimos6SessionZoomContractError)
  })

  it('rejects target contradictions across the deduplicated visible union', () => {
    const selected = {
      ...managedRow('47e5d8f7-0b11-4bee-a8a4-a11406de865a'),
      inbox: { unread_count: 2, latest_unread_at: '2026-08-30T11:59:00Z' },
    }
    expect(() => parsePaimos6SessionZoom(
      projection({ selected, totalSessions: 3 }),
      42,
      '10',
      selected.product_session_id,
    )).toThrow(Paimos6SessionZoomContractError)
  })

  it('calls only the exact zoom endpoint and preserves a far-out string', async () => {
    const zoom = '1234567890123456789012345678901234567890'
    const selected = paimosRow()
    const get = vi.spyOn(api, 'get').mockResolvedValue(projection({
      zoom,
      selected,
      totalSessions: 9,
    }) as never)
    const controller = new AbortController()
    await expect(loadPaimos6SessionZoom(42, zoom, PAIMOS_ID, controller.signal))
      .resolves.toMatchObject({ projectId: 42, zoom, selectedSession: { id: PAIMOS_ID } })
    expect(get).toHaveBeenCalledWith(
      `/projects/42/session-home/zoom/v1?zoom=${zoom}&selected_session_id=${PAIMOS_ID}`,
      { signal: controller.signal },
    )
  })
})
