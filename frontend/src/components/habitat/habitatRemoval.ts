/* PAIMOS — Copyright (C) 2026 Markus Barta; AGPL-3.0-only. */
import type { HabitatWorker } from './habitatModel'
import { api } from '@/api/client'
import { parseIsoInstant } from '@/services/agentModeAggregateSchema'
import { epoch, fields, invalid, object, positive, uuid } from './habitatBoundary'

export type RemovalMode = 'finish_then_remove' | 'stop_now' | 'handoff' | 'draft_only'

export const FINISH_THEN_REMOVE_UNAVAILABLE =
  'Finish-then-remove requires a current managed reporter generation with owned stop capability.'

export const HANDOFF_UNAVAILABLE =
  'Authorized handoff with successor acceptance is not available. Binding transfer alone cannot settle unfinished work and is not offered here.'

/** Browser-safe contract. The worker-lease drain endpoint is never called here. */
export const RETIRE_AFTER_WORK_CONTROL = {
  method: 'POST',
  path: '/projects/{id}/harness-sessions/{sessionID}/controls/v1/retire-after-work',
  request: {
    expected_revision: 'positive int64 — same CAS as controls/v1/stop',
    request_key: 'uuid — deterministic idempotency key',
  },
  response: {
    schema_version: 1,
    retirement: 'generation-fenced retirement outcome',
    state: 'requested | finishing | stopping | completed | failed | outcome_unknown',
  },
  semantics:
    'Human browser session only. Reauthorize the current project editor and exact session revision, persist one idempotent generation-fenced retirement, reject new work admission, let already accepted work settle, then require an owned stop receipt plus exact stopped-generation proof. Public idle or stopped state alone never completes the request.',
} as const

export interface HabitatRetirement {
  id: string
  kind: 'retire_after_work'
  state: 'requested' | 'finishing' | 'stopping' | 'completed' | 'failed' | 'outcome_unknown'
  outcome: 'applied' | 'rejected' | null
  reason: string | null
}

function parseRetirement(
  value: unknown,
  projectId: number,
  sessionId: string,
  expectedId?: string,
): HabitatRetirement {
  const row = object(value)
  fields(
    row,
    [
      'id',
      'project_id',
      'harness_session_id',
      'correlation_id',
      'kind',
      'requested_revision',
      'state',
      'requested_at',
      'owned_stop_receipt',
      'stopped_generation_proof',
    ],
    ['reason', 'claimed_at', 'stopping_at', 'completed_at'],
  )
  const state = String(row.state)
  const terminal = ['completed', 'failed', 'outcome_unknown'].includes(state)
  const appliedStopPending =
    state === 'stopping' &&
    row.reason === 'applied' &&
    typeof row.completed_at === 'string' &&
    row.owned_stop_receipt === true &&
    row.stopped_generation_proof === false
  if (
    !uuid(row.id) ||
    row.project_id !== projectId ||
    row.harness_session_id !== sessionId ||
    row.correlation_id !== row.id ||
    (expectedId !== undefined && row.id !== expectedId) ||
    row.kind !== 'retire_after_work' ||
    !positive(row.requested_revision) ||
    !['requested', 'finishing', 'stopping', 'completed', 'failed', 'outcome_unknown'].includes(
      state,
    ) ||
    !parseIsoInstant(row.requested_at) ||
    typeof row.owned_stop_receipt !== 'boolean' ||
    typeof row.stopped_generation_proof !== 'boolean' ||
    ['claimed_at', 'stopping_at', 'completed_at'].some(
      (key) => row[key] !== undefined && !parseIsoInstant(row[key]),
    ) ||
    (terminal
      ? !row.completed_at || !row.reason
      : row.reason !== undefined && !appliedStopPending) ||
    (state !== 'requested' && !row.claimed_at) ||
    (['stopping', 'completed'].includes(state) && !row.stopping_at) ||
    (state === 'completed' && (!row.owned_stop_receipt || !row.stopped_generation_proof)) ||
    ((state === 'failed' || state === 'outcome_unknown') && row.owned_stop_receipt) ||
    (state === 'outcome_unknown' && row.reason !== 'outcome_unknown') ||
    (state === 'requested' && (row.claimed_at || row.stopping_at || row.completed_at))
  )
    invalid()
  return {
    id: row.id as string,
    kind: 'retire_after_work',
    state: state as HabitatRetirement['state'],
    outcome: state === 'completed' ? 'applied' : terminal ? 'rejected' : null,
    reason: (row.reason as string | undefined) ?? null,
  }
}

export function parseHabitatRetirementReceipt(
  value: unknown,
  projectId: number,
  sessionId: string,
) {
  const root = object(value)
  fields(root, ['schema_version', 'retirement'])
  if (root.schema_version !== 1) invalid()
  return parseRetirement(root.retirement, projectId, sessionId)
}

export async function requestHabitatRetirement(
  projectId: number,
  sessionId: string,
  revision: number,
  requestKey: string,
  signal?: AbortSignal,
) {
  if (!positive(projectId) || !positive(revision) || !uuid(sessionId) || !uuid(requestKey))
    invalid()
  return parseHabitatRetirementReceipt(
    await api.post<unknown>(
      `/projects/${projectId}/harness-sessions/${sessionId}/controls/v1/retire-after-work`,
      { expected_revision: revision, request_key: requestKey },
      { signal },
    ),
    projectId,
    sessionId,
  )
}

export async function loadHabitatRetirement(
  projectId: number,
  sessionId: string,
  retirement: HabitatRetirement,
  signal?: AbortSignal,
) {
  return parseRetirement(
    epoch(
      await api.getWithMeta<unknown>(
        `/projects/${projectId}/harness-sessions/${sessionId}/controls/v1/retire-after-work/${retirement.id}`,
        { signal },
      ),
    ),
    projectId,
    sessionId,
    retirement.id,
  )
}

function removalEvidenceBase(worker: HabitatWorker, editable: boolean, fresh: boolean): boolean {
  return (
    editable &&
    fresh &&
    worker.management_mode === 'managed' &&
    worker.runtime_provenance_trust === 'managed_reporter' &&
    worker.liveness.source === 'agentd_reporter' &&
    worker.liveness.reporter_age_seconds !== null &&
    !['stopping', 'stopped'].includes(worker.phase)
  )
}

export function canStopNowRemove(
  worker: HabitatWorker,
  editable: boolean,
  fresh: boolean,
): boolean {
  return (
    removalEvidenceBase(worker, editable, fresh) &&
    ['busy', 'idle'].includes(worker.liveness.state) &&
    worker.capabilities.stop
  )
}

export function canFinishThenRemove(
  worker: HabitatWorker,
  editable: boolean,
  fresh: boolean,
): boolean {
  return canStopNowRemove(worker, editable, fresh)
}

export function canOpenRemovalDialog(
  worker: HabitatWorker,
  editable: boolean,
  fresh: boolean,
  hasLifecycleDraft: boolean,
): boolean {
  return (
    removalEvidenceBase(worker, editable, fresh) &&
    (canFinishThenRemove(worker, editable, fresh) ||
      canStopNowRemove(worker, editable, fresh) ||
      hasLifecycleDraft)
  )
}

/** @deprecated use canOpenRemovalDialog or canStopNowRemove */
export function canRemoveWorker(worker: HabitatWorker, editable: boolean, fresh: boolean): boolean {
  return canStopNowRemove(worker, editable, fresh)
}
