// Deterministic, synthetic fixture. Imported only by tests, never production views.
import wire from '../../../../../backend/contracts/fixtures/orchestration-v1.json' with { type: 'json' }
import type { OrchestrationSnapshotV1 } from '@/services/orchestrationTypes'
export function habitatFixture(
  zoom = '10',
  projectId: number | null = null,
  empty = false,
): OrchestrationSnapshotV1 {
  const fixture = structuredClone(wire) as OrchestrationSnapshotV1
  const fleet = fixture.fleet
  const limit = zoom.length < 3 ? Number(zoom) : 100
  fleet.zoom = zoom
  fleet.band = (['detail', 'overview', 'aggregate', 'far'] as const)[Math.min(3, zoom.length - 1)]
  fleet.sample_limit = limit
  fixture.coordination_bounds.sample_limit = limit
  fleet.scope = { kind: projectId === null ? 'portfolio' : 'project', project_id: projectId }
  fleet.totals = {
    projects: 1,
    sampled_projects: 1,
    omitted_projects: 0,
    workers: 2,
    sampled_workers: 2,
    omitted_workers: 0,
  }
  fleet.projects[0].total_workers = 2
  fixture.coordination_bounds.total_projects = 1
  fixture.coordination_bounds.omitted_projects = 0
  const root = fleet.workers[0]
  root.liveness.state = 'busy'
  root.liveness.reason = 'active_turn'
  root.liveness.reporter_age_seconds = 1
  root.capabilities.steer = true
  root.phase = 'working'
  fleet.workers[1].parent_harness_session_id = root.harness_session_id
  fleet.workers[1].parent_in_sample = true
  fixture.instance_root = {
    configured_identity: { display_label: 'Fixture coordinator' },
    binding_revision: 1,
    binding_updated_at: fleet.observed_at,
    active_generation: {
      state: 'resolved',
      reason: 'single_active_root_generation',
      session_id: root.harness_session_id,
    },
  }
  fixture.project_coordination[0].coordinator = {
    state: 'resolved',
    reason: 'single_active_coordinator',
    session_id: root.harness_session_id,
  }
  fixture.project_coordination[0].root_binding_revision = 1
  fleet.projects[0].orchestrator = { ...fixture.project_coordination[0].coordinator }
  if (empty) {
    fixture.instance_root = structuredClone(
      wire.instance_root,
    ) as OrchestrationSnapshotV1['instance_root']
    fixture.project_coordination[0].root_binding_revision = null
    fixture.project_coordination[0].coordinator = {
      state: 'unset',
      reason: 'no_active_coordinator',
      session_id: null,
    }
    fleet.workers = []
    fleet.projects = []
    fleet.totals = {
      projects: 0,
      sampled_projects: 0,
      omitted_projects: 0,
      workers: 0,
      sampled_workers: 0,
      omitted_workers: 0,
    }
  } else {
    fleet.workers = fleet.workers.slice(0, limit)
    fleet.totals.sampled_workers = fleet.workers.length
    fleet.totals.omitted_workers = fleet.totals.workers - fleet.workers.length
    fleet.projects[0].sampled_workers = fleet.workers.length
    fleet.projects[0].omitted_workers = fleet.projects[0].total_workers - fleet.workers.length
  }
  fleet.sample_truncated = fleet.totals.omitted_workers > 0
  return fixture
}
