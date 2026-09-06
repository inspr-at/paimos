import type { OrchestrationSnapshotV1, WorkerFleetSnapshotV2 } from '@/services/orchestrationTypes'

export type HabitatWorker = WorkerFleetSnapshotV2['workers'][number]
export type HabitatProject = OrchestrationSnapshotV1['project_coordination'][number]
export type WorkerRow = { worker: HabitatWorker; depth: number; children: number; hidden: number }

export function humanize(value: string | null | undefined): string {
  return value ? value.replace(/_/g, ' ') : 'Unknown'
}

/** Parent links come only from the same-project authoritative sample. */
export function workerRows(workers: HabitatWorker[], expanded: ReadonlySet<string>): WorkerRow[] {
  const ids = new Set(workers.map((w) => w.harness_session_id))
  const children = (worker: HabitatWorker) =>
    workers.filter(
      (w) =>
        w.project.id === worker.project.id &&
        w.parent_harness_session_id === worker.harness_session_id,
    )
  const count = (worker: HabitatWorker): number =>
    children(worker).reduce((n, child) => n + 1 + count(child), 0)
  const rows: WorkerRow[] = []
  function visit(worker: HabitatWorker, depth: number) {
    const descendants = count(worker)
    const open = expanded.has(worker.harness_session_id)
    rows.push({ worker, depth, children: children(worker).length, hidden: open ? 0 : descendants })
    if (open) children(worker).forEach((child) => visit(child, depth + 1))
  }
  workers
    .filter((w) => !w.parent_harness_session_id || !ids.has(w.parent_harness_session_id))
    .forEach((w) => visit(w, 0))
  return rows
}

export function workerNeedsAttention(worker: HabitatWorker): boolean {
  return (
    ['unknown', 'dead'].includes(worker.liveness.state) ||
    ['waiting_on_human', 'blocked', 'terminal_failed'].includes(worker.delivery_trust.reason) ||
    worker.recent_communication.some(
      (message) => message.error_code !== null || message.fallback_code !== null,
    )
  )
}

export function canControlWorker(
  worker: HabitatWorker,
  capability: keyof HabitatWorker['capabilities'],
  editable: boolean,
  fresh: boolean,
): boolean {
  return (
    editable &&
    fresh &&
    worker.management_mode === 'managed' &&
    worker.runtime_provenance_trust === 'managed_reporter' &&
    worker.liveness.source === 'agentd_reporter' &&
    worker.liveness.reporter_age_seconds !== null &&
    ['busy', 'idle'].includes(worker.liveness.state) &&
    !['stopping', 'stopped'].includes(worker.phase) &&
    worker.capabilities[capability]
  )
}

export function recentCommunication(worker: HabitatWorker, now: number): boolean {
  return worker.recent_communication.some((event) => {
    const age = now - Date.parse(event.occurred_at)
    return age >= 0 && age <= 60_000
  })
}
