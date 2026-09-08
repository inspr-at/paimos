/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 */

import { api } from '@/api/client'

export const FLOW_STATE_PATH = (projectId: number) =>
  `/projects/${projectId}/baseline-batches/flow-state`

export const FLOW_INTENT_PATH = (projectId: number) =>
  `/projects/${projectId}/baseline-batches/flow-intents`

export const FLOW_OVERVIEW_LOCATION = (projectId: number) =>
  `/projects/${projectId}?tab=overview#baseline-batch`

export type FlowIdentityContext = {
  contract_version: string
  host_id: string
  principal_kind: string
  principal_ref: string
  binding_ref: string
  organization_ref: string | null
  project_ref: string
  actor_kind: string
  issued_at: string
  expires_at: string
  fresh_until: string
  context_revision: string
  display?: {
    user_label?: string
    user_initials?: string
    project_label?: string
    fixture_label?: string
  }
}

export type FlowHostState = {
  evaluatedAt: string
  header: {
    appName?: string
    instanceLabel?: string
    version?: string
    userInitials?: string
    userLabel?: string
    projectName?: string
    projectSubtitle?: string
  }
  health?: Record<string, unknown>
  delivery?: Record<string, unknown>
  prerequisites?: Record<string, unknown>
  progress?: Record<string, unknown>
  executionModes?: string[]
  selectedExecutionMode?: string
  selectedAction?: string
  identityContext: FlowIdentityContext
}

export type FlowIntentResult = {
  executed: boolean
  unsupported?: boolean
  routed?: string | null
  location?: string
  reason?: string
  notice?: string
  error?: string
  issues?: string[]
}

export type FlowIntentBinding = {
  status: string
  principalRef: string
  projectRef: string
  actorKind: string
  bindingRef: string
  contextRevision: string
  issuedAt: string
  expiresAt: string
  freshUntil: string
}

export function isUsableFlowState(value: unknown): value is FlowHostState {
  if (!value || typeof value !== 'object') return false
  const state = value as FlowHostState
  const identity = state.identityContext
  return (
    !!identity &&
    identity.host_id === 'paimos' &&
    identity.principal_kind === 'local_host' &&
    identity.contract_version === 'inspr.flow-identity/0.1-draft' &&
    typeof identity.principal_ref === 'string' &&
    typeof identity.project_ref === 'string' &&
    typeof identity.context_revision === 'string'
  )
}

export function identityBindingFrom(state: FlowHostState): FlowIntentBinding {
  const identity = state.identityContext
  return {
    status: 'present',
    principalRef: identity.principal_ref,
    projectRef: identity.project_ref,
    actorKind: identity.actor_kind,
    bindingRef: identity.binding_ref,
    contextRevision: identity.context_revision,
    issuedAt: identity.issued_at,
    expiresAt: identity.expires_at,
    freshUntil: identity.fresh_until,
  }
}

export async function getFlowHostState(projectId: number): Promise<FlowHostState | null> {
  if (!Number.isSafeInteger(projectId) || projectId < 1) return null
  try {
    const payload = await api.get<unknown>(FLOW_STATE_PATH(projectId))
    return isUsableFlowState(payload) ? payload : null
  } catch {
    return null
  }
}

export async function postFlowHostIntent(
  projectId: number,
  type: string,
  identity: FlowIntentBinding,
  extra: Record<string, unknown> = {},
): Promise<FlowIntentResult> {
  return api.post<FlowIntentResult>(FLOW_INTENT_PATH(projectId), {
    type,
    identity,
    ...extra,
  })
}
