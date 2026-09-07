/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 */

import { api } from '@/api/client'

export type BaselineClaim = {
  baseline_ref: string
  revision: number
  content_digest: string
  revision_seal: string
  imported_claimed_approved_by: string
  imported_claimed_approved_at: string
  authenticity: string
  stream_ref: string
}

export type Forecast = {
  subject: string
  percent: number
  eta_seconds: number | null
  kind: 'measured' | 'worker_estimate' | 'educated_guess'
  basis: string
  as_of: string
  label: string
  observed: boolean
  fresh: boolean
  educated_eta_seconds?: number | null
  educated_eta_basis?: string
  educated_eta_as_of?: string
  educated_eta_label?: string
}

export type WorkerSelection = {
  worker_name?: string
  account_label?: string
  account_key?: string
  profile_id?: string
  profile_version?: string
  workspace_handle?: string
  runtime_id?: string
  runtime_generation?: string
}

export type ReadinessCheck = { id: string; status: string; reason: string }

export type Readiness = {
  status: string
  basis: string
  contract_version: string
  intent_id?: string
  observed_at?: string
  fresh_until?: string
  blocking_reason?: string
  next_action?: string
  host_kind?: string
  probe_intent_id?: string
  probe_state?: string
  probe_reason?: string
  named_account_proof: boolean
  model_profile_proof: boolean
  workspace_proof: boolean
  client_ready_ignored: boolean
  workspace_identity?: string
  checks: ReadinessCheck[]
}

export type ImpactEstimate = {
  requirement_count: number
  acceptance_criteria_count: number
  constraint_count: number
  unresolved_count: number
  forecast: Forecast
}

export type Draft = {
  id: number
  project_id: number
  revision: number
  status: string
  baseline: BaselineClaim
  requirements: { requirement_ref: string; statement: string }[]
  selected: { requirement_refs: string[]; constraint_refs: string[] }
  unresolved: { kind: string; ref: string; summary: string }[]
  execution_mode: string
  worker: WorkerSelection
  review_id?: number | null
  review_valid: boolean
  impact: ImpactEstimate
}

export type StageView = {
  stage_key: string
  applicability: string
  weight: number
  state: string
  phase: string
  activity: string
  needs_input: boolean
  performed: boolean
  policy_satisfied: boolean
  stale: boolean
  never_signaled: boolean
  last_signal_at?: string
}

export type Progress = {
  stages: StageView[]
  intent_state?: string
  intent_reason?: string
  session_id?: string
  session_phase?: string
  session_activity?: string
  evidence_fresh: boolean
  evidence_observed: boolean
  freshness_as_of?: string
  blocking_reason?: string
}

export type ControlOption = {
  action: string
  available: boolean
  effect?: string
  reason?: string
}

export type Batch = {
  id: number
  batch_key: string
  status: string
  workflow_state: string
  control_state: string
  control_reason?: string
  execution_mode: string
  issue_id: number
  delivery_id?: number | null
  attempt_id?: number | null
  lifecycle_intent_id?: string
  readiness_intent_id?: string
  forecasts: Forecast[]
  progress: Progress
  controls: ControlOption[]
  readiness?: Readiness | null
  baseline: BaselineClaim
  scope: { requirement_refs: string[] }
  started_at: string
}

export type Workflow = {
  project_id: number
  inspr_stream_enabled: boolean
  inspr_gating: boolean
  legacy_unaffected: boolean
  draft: Draft | null
  active_batch: Batch | null
  batches: Batch[]
  readiness?: Readiness | null
  choices: {
    execution_modes: string[]
    runtimes: {
      runtime_id: string
      runtime_generation: string
      account_label: string
      accounts: { key: string; label: string }[]
      profiles: { id: string; version: string }[]
      workspaces: { handle: string; identity: string; label?: string }[]
    }[]
    note: string
  }
}

export function getBaselineWorkflow(projectId: number) {
  return api.get<Workflow>(`/projects/${projectId}/baseline-batches/`)
}

export function setBaselineStreamEnabled(projectId: number, enabled: boolean) {
  return api.post<Workflow>(`/projects/${projectId}/baseline-batches/opt-in`, { enabled })
}

export function importBaselineHandover(projectId: number, handover: unknown, selected?: string[]) {
  return api.post<Draft>(`/projects/${projectId}/baseline-batches/import`, {
    handover,
    selected_requirement_refs: selected ?? [],
  })
}

export function patchBaselineDraft(projectId: number, draftId: number, body: Record<string, unknown>) {
  return api.patch<Draft>(`/projects/${projectId}/baseline-batches/${draftId}`, body)
}

export function reviewBaselineDraft(projectId: number, draftId: number, body: Record<string, unknown>) {
  return api.post<Draft>(`/projects/${projectId}/baseline-batches/${draftId}/review`, body)
}

// Ask the owned daemon to observe this development target. The answer is an
// observation the server accepted, never a claim made in the browser.
export function requestBaselineReadiness(projectId: number, draftId: number) {
  return api.post<Readiness>(`/projects/${projectId}/baseline-batches/${draftId}/readiness`, {})
}

export function startBaselineBatch(projectId: number, draftId: number, body: Record<string, unknown>, idempotencyKey: string) {
  return api.post<Batch>(`/projects/${projectId}/baseline-batches/${draftId}/start`, body, {
    headers: { 'Idempotency-Key': idempotencyKey },
  })
}

export function controlBaselineBatch(projectId: number, batchId: number, action: string, requestKey: string) {
  return api.post<Batch>(`/projects/${projectId}/baseline-batches/batches/${batchId}/control`, {
    action,
    request_key: requestKey,
  })
}
