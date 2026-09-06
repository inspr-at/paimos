/* PAIMOS — Copyright (C) 2026 Markus Barta; AGPL-3.0-only. */
import { describe, expect, it, vi } from 'vitest'
import fixture from '../../../backend/contracts/fixtures/orchestration-v1.json'
import { loadOrchestration, parseOrchestrationSnapshot } from './orchestration'
import { api } from '@/api/client'
vi.mock('@/api/client', async (original) => ({
  ...(await original<typeof import('@/api/client')>()),
  api: { getWithMeta: vi.fn() },
}))
const copy = () => structuredClone(fixture)

describe('orchestration v1 boundary', () => {
  it('reads the closed fleet v2 fixture without dropping runtime or work truth', () => {
    const parsed = parseOrchestrationSnapshot(copy())
    expect(parsed.fleet.schema_version).toBe(2)
    expect(parsed.fleet.workers.length).toBeGreaterThan(0)
    expect(parsed).toEqual(fixture)
  })

  it('preserves configured roots with uncertain and resolved generations', () => {
    const candidate = parseOrchestrationSnapshot(copy())
    candidate.instance_root.configured_identity = { display_label: 'Root' }
    candidate.instance_root.binding_revision = 1
    candidate.instance_root.binding_updated_at = candidate.fleet.observed_at
    for (const row of candidate.project_coordination) row.root_binding_revision = 1
    candidate.instance_root.active_generation = {
      state: 'unknown',
      reason: 'root_generation_unknown',
      session_id: null,
    }
    expect(parseOrchestrationSnapshot(candidate).instance_root.active_generation.state).toBe(
      'unknown',
    )
    const coordinator = candidate.fleet.workers.find((worker) => worker.role === 'coordinator')!
    coordinator.liveness.state = 'busy'
    coordinator.liveness.source = 'agentd_reporter'
    candidate.instance_root.active_generation = {
      state: 'resolved',
      reason: 'single_active_root_generation',
      session_id: coordinator.harness_session_id,
    }
    const project = candidate.fleet.projects.find(
      (project) => project.id === coordinator.project.id,
    )!
    project.orchestrator = {
      state: 'resolved',
      reason: 'single_active_coordinator',
      session_id: coordinator.harness_session_id,
    }
    candidate.project_coordination.find((row) => row.project.id === project.id)!.coordinator =
      project.orchestrator
    expect(parseOrchestrationSnapshot(candidate).instance_root.active_generation.session_id).toBe(
      coordinator.harness_session_id,
    )
    coordinator.parent_harness_session_id = coordinator.harness_session_id
    coordinator.parent_in_sample = true
    expect(() => parseOrchestrationSnapshot(candidate)).toThrow()
  })

  it('rejects unknown private fields at every populated object boundary', () => {
    const objects = (value: unknown, path: (string | number)[] = []): (string | number)[][] => {
      if (value === null || typeof value !== 'object') return []
      if (Array.isArray(value)) return value.flatMap((child, i) => objects(child, [...path, i]))
      return [
        path,
        ...Object.entries(value).flatMap(([key, child]) => objects(child, [...path, key])),
      ]
    }
    for (const path of objects(fixture)) {
      const candidate = copy()
      let object: unknown = candidate
      for (const key of path) object = (object as Record<string | number, unknown>)[key]
      ;(object as Record<string, unknown>).body = 'fixture-private-content'
      expect(() => parseOrchestrationSnapshot(candidate)).toThrow('invalid orchestration response')
    }
  })

  it.each([
    (s: ReturnType<typeof copy>) => {
      s.schema_version = 2
    },
    (s: ReturnType<typeof copy>) => {
      s.fleet.schema_version = 1
    },
    (s: ReturnType<typeof copy>) => {
      s.fleet.totals.omitted_workers++
    },
    (s: ReturnType<typeof copy>) => {
      s.coordination_bounds.total_projects++
    },
    (s: ReturnType<typeof copy>) => {
      s.fleet.workers.push(s.fleet.workers[0]!)
    },
    (s: ReturnType<typeof copy>) => {
      s.fleet.workers[0]!.liveness.observed_at = '2026-02-31T00:00:00Z'
    },
    (s: ReturnType<typeof copy>) => {
      s.fleet.workers[0]!.work_shape = 'scout'
    },
    (s: ReturnType<typeof copy>) => {
      s.fleet.workers[0]!.runtime_provenance_trust = 'untrusted'
    },
    (s: ReturnType<typeof copy>) => {
      s.fleet.workers[0]!.recent_communication = Array(5).fill(
        s.fleet.workers[0]!.recent_communication[0],
      )
    },
    (s: ReturnType<typeof copy>) => {
      s.instance_root.active_generation.state = 'resolved'
    },
    (s: ReturnType<typeof copy>) => {
      s.project_coordination[0]!.coordinator.state = 'resolved'
    },
    (s: ReturnType<typeof copy>) => {
      s.project_coordination[0]!.relationship = 'parent_session'
    },
  ])('refuses malformed bounds, freshness, authority, and version claims', (mutate) => {
    const candidate = copy()
    mutate(candidate)
    expect(() => parseOrchestrationSnapshot(candidate)).toThrow()
  })

  it('validates request scope and zoom before issuing a request', async () => {
    vi.mocked(api.getWithMeta).mockClear()
    await expect(loadOrchestration({ projectId: -1 })).rejects.toThrow()
    await expect(loadOrchestration({ zoom: '01' })).rejects.toThrow()
    expect(api.getWithMeta).not.toHaveBeenCalled()
    vi.mocked(api.getWithMeta).mockResolvedValue({
      data: copy(),
      status: 200,
      etag: null,
      lastModified: null,
      permissionsEpoch: '1',
      permissionsEpochGeneration: 0,
    })
    const result = await loadOrchestration({ zoom: fixture.fleet.zoom })
    expect(result).toEqual(fixture)
    expect(api.getWithMeta).toHaveBeenCalledWith(
      `/agent-mode/orchestration/v1?zoom=${fixture.fleet.zoom}`,
      { signal: undefined },
    )
    await expect(loadOrchestration({ projectId: 999, zoom: fixture.fleet.zoom })).rejects.toThrow()
  })
})
