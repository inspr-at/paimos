/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 */

import { api } from '@/api/client'

export type WorkerShape = 'unknown' | 'ship' | 'scout'
type DispatchEffort = 'low' | 'medium' | 'high' | 'xhigh' | 'max'
type WorkspaceKind = 'directory' | 'git_primary' | 'git_worktree'
type WorkspaceMode = 'exclusive' | 'shared'

export interface WorkerFleetContext {
  harnessSessionId: string
  agentName: string
  machineId: string | null
  workspace: {
    kind: WorkspaceKind
    mode: WorkspaceMode
  } | null
  dispatch: { model: string; effort: DispatchEffort } | null
  accountLabel:
    | 'unknown'
    | 'chatgpt'
    | 'api_key'
    | 'claude_ai_max'
    | 'claude_ai_pro'
    | 'claude_ai_team'
    | 'claude_ai_enterprise'
    | 'console'
  managementMode: 'managed' | 'unmanaged'
  runtimeProvenanceTrust: 'managed_reporter' | 'untrusted'
  shape: WorkerShape
  outputKind: 'unclassified' | 'delivery' | 'investigation_evidence'
}

export interface WorkerFleetTicketContext {
  workers: WorkerFleetContext[]
  sampleTruncated: boolean
}

const SHAPES = new Set<WorkerShape>(['unknown', 'ship', 'scout'])
const WORKSPACE_KINDS = new Set<WorkspaceKind>(['directory', 'git_primary', 'git_worktree'])
const WORKSPACE_MODES = new Set<WorkspaceMode>(['exclusive', 'shared'])
const EFFORTS = new Set<DispatchEffort>(['low', 'medium', 'high', 'xhigh', 'max'])
const ACCOUNT_LABELS = new Set([
  'unknown',
  'chatgpt',
  'api_key',
  'claude_ai_max',
  'claude_ai_pro',
  'claude_ai_team',
  'claude_ai_enterprise',
  'console',
])
const OUTPUT_KINDS = new Set(['unclassified', 'delivery', 'investigation_evidence'])
const MANAGEMENT_MODES = new Set(['managed', 'unmanaged'])
const RUNTIME_TRUST = new Set(['managed_reporter', 'untrusted'])

function record(value: unknown): Record<string, unknown> | null {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null
}

function token(value: unknown): string | null {
  return typeof value === 'string' && value.length > 0 && value.length <= 128 ? value : null
}

function parseWorker(value: unknown, ticketId: number): WorkerFleetContext | null {
  const worker = record(value)
  if (!worker) throw new Error('invalid worker fleet response')
  if (worker.ticket === null) return null
  const ticket = record(worker.ticket)
  if (!ticket || !Number.isSafeInteger(ticket.id) || (ticket.id as number) <= 0) {
    throw new Error('invalid worker fleet response')
  }
  if (ticket.id !== ticketId) return null

  const harnessSessionId = token(worker.harness_session_id)
  const agentName = token(record(worker.agent)?.name)
  const machineId = worker.machine_id === null ? null : token(worker.machine_id)
  const shape = worker.work_shape
  const accountLabel = worker.account_label
  const managementMode = worker.management_mode
  const runtimeProvenanceTrust = worker.runtime_provenance_trust
  const contract = record(worker.work_contract)
  const outputKind = contract?.output_kind
  if (
    !harnessSessionId ||
    !agentName ||
    !SHAPES.has(shape as WorkerShape) ||
    contract?.shape !== shape ||
    !OUTPUT_KINDS.has(outputKind as string) ||
    !ACCOUNT_LABELS.has(accountLabel as string) ||
    !MANAGEMENT_MODES.has(managementMode as string) ||
    !RUNTIME_TRUST.has(runtimeProvenanceTrust as string)
  ) {
    throw new Error('invalid worker fleet response')
  }

  let workspace: WorkerFleetContext['workspace'] = null
  if (worker.workspace_provenance !== null) {
    const raw = record(worker.workspace_provenance)
    if (
      !raw ||
      !WORKSPACE_KINDS.has(raw.kind as WorkspaceKind) ||
      !WORKSPACE_MODES.has(raw.mode as WorkspaceMode)
    ) {
      throw new Error('invalid worker fleet response')
    }
    workspace = { kind: raw.kind as WorkspaceKind, mode: raw.mode as WorkspaceMode }
  }

  let dispatch: WorkerFleetContext['dispatch'] = null
  if (worker.dispatch_profile !== null) {
    const raw = record(worker.dispatch_profile)
    const model = token(raw?.model)
    if (!raw || !model || !EFFORTS.has(raw.effort as DispatchEffort)) {
      throw new Error('invalid worker fleet response')
    }
    dispatch = { model, effort: raw.effort as DispatchEffort }
  }

  if (
    (runtimeProvenanceTrust === 'managed_reporter' &&
      (managementMode !== 'managed' || !machineId || workspace === null)) ||
    (runtimeProvenanceTrust === 'untrusted' &&
      (machineId !== null || workspace !== null || dispatch !== null || accountLabel !== 'unknown'))
  ) {
    throw new Error('invalid worker fleet response')
  }

  return {
    harnessSessionId,
    agentName,
    machineId,
    workspace,
    dispatch,
    accountLabel: accountLabel as WorkerFleetContext['accountLabel'],
    managementMode: managementMode as WorkerFleetContext['managementMode'],
    runtimeProvenanceTrust: runtimeProvenanceTrust as WorkerFleetContext['runtimeProvenanceTrust'],
    shape: shape as WorkerShape,
    outputKind: outputKind as WorkerFleetContext['outputKind'],
  }
}

/** Reads only the authorized, bounded project companion projection. */
export async function loadWorkerFleetTicket(
  projectId: number,
  ticketId: number,
): Promise<WorkerFleetTicketContext> {
  if (
    !Number.isSafeInteger(projectId) ||
    projectId <= 0 ||
    !Number.isSafeInteger(ticketId) ||
    ticketId <= 0
  ) {
    throw new Error('invalid worker fleet identity')
  }
  const source = record(
    await api.get<unknown>(`/agent-mode/projects/${projectId}/worker-fleet/v2?zoom=100`),
  )
  if (
    !source ||
    source.schema_version !== 2 ||
    typeof source.sample_truncated !== 'boolean' ||
    !Array.isArray(source.workers) ||
    source.workers.length > 100
  ) {
    throw new Error('invalid worker fleet response')
  }
  const workers: WorkerFleetContext[] = []
  for (const value of source.workers) {
    const worker = parseWorker(value, ticketId)
    if (worker) workers.push(worker)
  }
  return { workers, sampleTruncated: source.sample_truncated }
}
