/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as
 * published by the Free Software Foundation, version 3.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public
 * License along with this program. If not, see <https://www.gnu.org/licenses/>.
 */

// PAI-805 — Agent Mode domain model + data service.
//
// The domain types below are what every Agent Mode component consumes.
// They are deliberately decoupled from the wire payload (see
// `agentModeTransport.ts`) so the PAI-804 integration owner can adapt
// the transport without touching cards, lanes, selection, or trust logic.
//
// There is NO demo fallback here. If the API is unreachable the service
// throws a classified `AgentModeLoadError` and the UI renders an honest
// offline / forbidden / not-found / error state. Deterministic fixtures
// for tests and the DEV-only reference route live in
// `agentModeFixtures.ts` and are never imported by production code paths.

import {
  api,
  ApiError,
  isSessionExpiredError,
  isStalePermissionsEpochError,
  parsePermissionsEpochHeader,
} from '@/api/client'
import {
  buildSnapshotPath,
  normalizeWireSnapshot,
  type AgentModeSnapshotQuery,
} from './agentModeTransport'
import type { AgentModeAggregates, AgentModeAggregateUnavailableReason } from './agentModeAggregateSchema'

export type DeliveryHealth = 'healthy' | 'attention' | 'at_risk' | 'blocked' | 'stale' | 'unknown'
export type ActivityKind =
  | 'working'
  | 'testing'
  | 'deploying'
  | 'verifying'
  | 'waiting'
  | 'blocked'
  | 'idle'
  | 'unknown'
export type StageKey = 'specification' | 'implementation' | 'qa' | 'deployment' | 'verification' | 'unknown'
export type FreshnessState = 'fresh' | 'aging' | 'stale' | 'unknown'
/** 0 = none, 1 = watch, 2 = needs input, 3 = blocked / urgent. */
export type AttentionLevel = 0 | 1 | 2 | 3
export type EstimateConfidence = 'high' | 'medium' | 'low' | 'none'
export type DeliveryStageStatus =
  | 'pending'
  | 'active'
  | 'waiting'
  | 'blocked'
  | 'failed'
  | 'cancelled'
  | 'draft_ready'
  | 'succeeded'
  | 'not_applicable'
  | 'unknown'

export interface DeliveryActor {
  /** Stable machine name (agent name, reporter id). */
  name: string
  /** Human label shown on the card. */
  label: string
  kind: 'agent' | 'external' | 'system' | 'human' | 'unknown'
}

/** Privacy-reviewed schema-v1 trust projection. These are the only lineage
 * identities the backend intentionally exposes; reporter/run-link/provider
 * identities and evidence payloads have no domain field. */
export interface DeliveryTrust {
  schemaVersion: 1
  trustRevision: string
  progressKnown: boolean
  progressPercent: number | null
  confidence: EstimateConfidence
  reporterKind: 'agent_run' | 'external' | 'user' | 'system' | 'unknown'
  sourceKind: 'owner_estimate' | 'stage_evidence' | 'history'
  basis: string | null
  optimisticLandingAt: string | null
  pessimisticLandingAt: string | null
  landingAt: string | null
  rangeOnly: boolean
  suppression: string | null
  scope: {
    attemptId: string
    planId: string
    executionId: string
    authorityId: string
    resetId: string
  } | null
  flags: string[]
}

export interface DeliveryLane {
  /** Stable lane key: `project:<id>/epic:<id>` or `project:<id>/ungrouped`. */
  key: string
  projectId: number
  projectKey: string
  projectName: string
  epicId: number | null
  epicKey: string | null
  epicTitle: string | null
}

export interface DeliveryProgress {
  percent: number | null
  /** Server-side trust verdict (PAI-803). Never show a percent unless true. */
  trusted: boolean
  confidence: EstimateConfidence
  source: string | null
  basis: string | null
  /** Opaque estimate lineage. PAI-803 may use a composite revision. */
  revision: string | number | null
}

export interface DeliveryEta {
  landingAt: string | null
  optimisticAt: string | null
  pessimisticAt: string | null
  /** Server-side trust verdict (PAI-803). Never show a landing time unless true. */
  trusted: boolean
  confidence: EstimateConfidence
  basis: string | null
  calculatedAt: string | null
}

export interface DeliveryEvidence {
  id: string | null
  kind: string
  label: string | null
  summary: string | null
  status: string | null
  reportedAt: string | null
  reporter: DeliveryActor | null
}

export interface DeliveryStage {
  key: StageKey
  label: string | null
  status: DeliveryStageStatus
  required: boolean | null
  owner: DeliveryActor | null
  activity: string | null
  blockers: Array<{ kind: string; text: string }>
  evidence: DeliveryEvidence[]
  startedAt: string | null
  completedAt: string | null
}

export interface DeliveryHandoff {
  id: string | null
  from: DeliveryActor | null
  to: DeliveryActor | null
  status: string | null
  summary: string | null
  reportedAt: string | null
}

/** Server-authoritative capability summary. Null means the backend did not
 * make the claim; callers must not treat it as allowed. */
export interface DeliveryCapabilities {
  viewIssue: boolean | null
  editIssue: boolean | null
  comment: boolean | null
  attach: boolean | null
  liveNote: boolean | null
  oneShotRunActive: boolean | null
}

export interface Delivery {
  /** Stable delivery identity across snapshots (selection key). */
  id: string
  issueId: number
  issueKey: string
  title: string
  lane: DeliveryLane
  attempt: {
    id: string | null
    number: number | null
    planRevision: string | null
    status: string | null
  }
  /** Opaque read-model and trust lineages retained across semantic zoom. */
  deliveryRevision: string | null
  trustRevision: string | null
  /** Authoritative safe trust projection. In particular, `suppression`
   * gates precision even though a known progress fact is marked trusted. */
  trust: DeliveryTrust
  suppressionCodes: string[]
  disagreementCodes: string[]
  /** Supplemental only — never used for lane structure. */
  tags: string[]
  actor: DeliveryActor | null
  activity: { kind: ActivityKind; text: string | null; since: string | null }
  stage: { key: StageKey; label: string | null; index: number | null; total: number | null }
  stages: DeliveryStage[]
  evidence: DeliveryEvidence[]
  handoffs: DeliveryHandoff[]
  capabilities: DeliveryCapabilities
  health: DeliveryHealth
  attention: { level: AttentionLevel; reason: string | null; since: string | null }
  freshness: { state: FreshnessState; lastReportAt: string | null }
  blockers: Array<{ kind: string; text: string }>
  progress: DeliveryProgress | null
  eta: DeliveryEta | null
  statusText: string | null
  updatedAt: string | null
}

export interface AgentModeSnapshot {
  /** Server clock at calculation time (ISO). Null when the API omits it. */
  serverTime: string | null
  /** Stable revision / sequence for reconnect + replay (PAI-804). */
  revision: string | null
  /** Opaque SSE/reconnect cursor from PAI-804. */
  cursor: string | null
  deliveries: Delivery[]
  /** Authorized persistent selection returned outside active filters/counts. */
  selectedOutsideResults: Delivery | null
  /** Why the separately returned selection is outside active rows/counts. */
  selectedOutsideReason: 'filter_excluded' | 'terminal' | 'active_fallback' | 'terminal_fallback' | null
  selectedDeliveryId: string | null
  /** Strictly parsed PAI-804 aggregate schema v1. Null is never interpreted
   * as an all-zero portfolio; Detail 100 renders `aggregateUnavailableReason`. */
  aggregates: AgentModeAggregates | null
  aggregateUnavailableReason: AgentModeAggregateUnavailableReason | null
  /** Browser clock when the payload was received — paired with serverTime for skew. */
  receivedAt: number
}

export type AgentModeLoadErrorKind = 'offline' | 'forbidden' | 'not-found' | 'error'

export class AgentModeLoadError extends Error {
  constructor(
    public readonly kind: AgentModeLoadErrorKind,
    message: string,
    public readonly status: number | null = null,
    public readonly cause?: unknown,
  ) {
    super(message)
    this.name = 'AgentModeLoadError'
  }
}

/** Maps transport failures to the honest UI states. Session expiry is
 * re-thrown untouched so the global SessionExpiredModal stays the single
 * surface for that condition. */
export function classifyLoadError(e: unknown): AgentModeLoadError {
  if (e instanceof AgentModeLoadError) return e
  if (e instanceof ApiError) {
    if (e.status === 403) return new AgentModeLoadError('forbidden', e.message, 403, e)
    if (e.status === 404) return new AgentModeLoadError('not-found', e.message, 404, e)
    if (e.status === 0 || e.status === 502 || e.status === 503 || e.status === 504) {
      return new AgentModeLoadError('offline', e.message, e.status, e)
    }
    return new AgentModeLoadError('error', e.message, e.status, e)
  }
  if (e instanceof TypeError) {
    // fetch() rejects with TypeError on network failure / DNS / CORS.
    return new AgentModeLoadError('offline', e.message, 0, e)
  }
  const message = e instanceof Error ? e.message : 'request failed'
  return new AgentModeLoadError('error', message, null, e)
}

export type AgentModeSnapshotLoader = (
  query: AgentModeSnapshotQuery,
  opts?: {
    signal?: AbortSignal
    /** Response-local authority proof. Production always supplies it before
     * the snapshot can be committed; deterministic fixture loaders may omit. */
    onResponseMeta?: (meta: { permissionsEpoch: string; permissionsEpochGeneration: number }) => void
  },
) => Promise<AgentModeSnapshot>

/** Production loader: one ACL-filtered request, normalized at the edge. */
export const fetchAgentModeSnapshot: AgentModeSnapshotLoader = async (query, opts) => {
  try {
    const response = await api.getWithMeta<unknown>(buildSnapshotPath(query), { signal: opts?.signal })
    if (
      response.permissionsEpoch == null
      || parsePermissionsEpochHeader(response.permissionsEpoch) !== response.permissionsEpoch
      || !Number.isSafeInteger(response.permissionsEpochGeneration)
      || response.permissionsEpochGeneration < 0
    ) {
      throw new ApiError(response.status, 'Agent Mode snapshot is missing its authority epoch')
    }
    opts?.onResponseMeta?.({
      permissionsEpoch: response.permissionsEpoch,
      permissionsEpochGeneration: response.permissionsEpochGeneration,
    })
    return normalizeWireSnapshot(response.data, Date.now())
  } catch (e) {
    if (isSessionExpiredError(e) || isStalePermissionsEpochError(e)) throw e
    throw classifyLoadError(e)
  }
}
