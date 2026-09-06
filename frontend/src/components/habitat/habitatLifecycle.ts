// Browser lifecycle boundary; the server and agentd remain execution authorities.
import { api, ApiError, parsePermissionsEpochHeader } from '@/api/client'
import { parseIsoInstant } from '@/services/agentModeAggregateSchema'
export interface HabitatRuntime {
  id: string
  project_id: number
  generation: string
  machine_id: string
  account_label: string
  workspaces: { handle: string; identity: string }[]
  profiles: { id: string; version: string }[]
  expires_at: string
  sessions: { session_id: string; generation: string }[]
}
export type HabitatStartRequest = {
  request_key: string
  operation: 'start'
  runtime_id: string
  runtime_generation: string
  account_label: string
  ttl_seconds: number
  workspace_handle: string
  agent_name: string
  dispatch_profile_id: string
  dispatch_profile_version: string
  ticket_id: number | null
  work_shape: 'ship' | 'scout' | 'unknown'
  role: 'worker' | 'coordinator'
  parent_harness_session_id: string | null
}
export type HabitatRepairRequest = {
  request_key: string
  operation: 'repair'
  runtime_id: string
  runtime_generation: string
  account_label: string
  ttl_seconds: number
  repair_layer: 'reporter' | 'listeners'
}
export type HabitatLifecycleRequest = HabitatStartRequest | HabitatRepairRequest
export type IntentState =
  | 'requested'
  | 'claimed'
  | 'executing'
  | 'completed'
  | 'failed'
  | 'expired'
  | 'cancelled'
export interface HabitatIntent {
  id: string
  projectId: number
  state: IntentState
  revision: number
  reason: string
  createdAt: string
  updatedAt: string
  expiresAt: string
  newGeneration: string | null
  resultSessionId: string | null
}
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/
const ACCOUNTS = [
  'chatgpt',
  'api_key',
  'claude_ai_max',
  'claude_ai_pro',
  'claude_ai_team',
  'claude_ai_enterprise',
  'console',
]
const STATES = ['requested', 'claimed', 'executing', 'completed', 'failed', 'expired', 'cancelled']
const invalid = (): never => {
  throw new Error('Invalid lifecycle response')
}
const object = (value: unknown): Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : invalid()
const token = (value: unknown): value is string =>
  typeof value === 'string' && /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/.test(value)
const uuid = (value: unknown): value is string => typeof value === 'string' && UUID.test(value)
function fields(value: Record<string, unknown>, required: string[], optional: string[] = []) {
  if (
    required.some((key) => !(key in value)) ||
    Object.keys(value).some((key) => !required.includes(key) && !optional.includes(key))
  )
    invalid()
}
function epoch<T>(result: { data: T; permissionsEpoch: string | null }): T {
  if (
    !result.permissionsEpoch ||
    parsePermissionsEpochHeader(result.permissionsEpoch) !== result.permissionsEpoch
  )
    throw new ApiError(0, 'Lifecycle response authority unavailable')
  return result.data
}
export function parseHabitatRuntimes(value: unknown, projectId: number): HabitatRuntime[] {
  const root = object(value)
  fields(root, ['schema_version', 'runtimes'])
  if (root.schema_version !== 1 || !Array.isArray(root.runtimes) || root.runtimes.length > 32)
    invalid()
  const seen = new Set<string>()
  return (root.runtimes as unknown[]).map((value) => {
    const runtime = object(value)
    fields(runtime, [
      'id',
      'project_id',
      'generation',
      'machine_id',
      'account_label',
      'workspaces',
      'profiles',
      'expires_at',
      'sessions',
    ])
    if (
      !uuid(runtime.id) ||
      !uuid(runtime.generation) ||
      runtime.project_id !== projectId ||
      !token(runtime.machine_id) ||
      !ACCOUNTS.includes(String(runtime.account_label)) ||
      !parseIsoInstant(runtime.expires_at) ||
      !Array.isArray(runtime.workspaces) ||
      !Array.isArray(runtime.profiles) ||
      runtime.workspaces.length > 64 ||
      runtime.profiles.length > 128 ||
      seen.has(String(runtime.id))
    )
      invalid()
    seen.add(String(runtime.id))
    if (!Array.isArray(runtime.sessions) || runtime.sessions.length > 128) invalid()
    const sessionIds = new Set<string>()
    for (const raw of runtime.sessions as unknown[]) {
      const session = object(raw)
      fields(session, ['session_id', 'generation'])
      if (
        !uuid(session.session_id) ||
        !uuid(session.generation) ||
        sessionIds.has(String(session.session_id))
      )
        invalid()
      sessionIds.add(String(session.session_id))
    }
    const workspaceIds = new Set<string>()
    for (const raw of runtime.workspaces as unknown[]) {
      const workspace = object(raw)
      fields(workspace, ['handle', 'identity'])
      if (
        !uuid(workspace.handle) ||
        typeof workspace.identity !== 'string' ||
        !/^[a-f0-9]{64}$/.test(workspace.identity) ||
        workspaceIds.has(workspace.handle)
      )
        invalid()
      workspaceIds.add(String(workspace.handle))
    }
    const profileIds = new Set<string>()
    for (const raw of runtime.profiles as unknown[]) {
      const profile = object(raw)
      fields(profile, ['id', 'version'])
      if (
        !token(profile.id) ||
        !token(profile.version) ||
        profileIds.has(`${profile.id}@${profile.version}`)
      )
        invalid()
      profileIds.add(`${profile.id}@${profile.version}`)
    }
    return runtime as unknown as HabitatRuntime
  })
}
export function parseHabitatIntent(
  value: unknown,
  projectId: number,
  expected: HabitatLifecycleRequest,
): HabitatIntent {
  const root = object(value)
  fields(
    root,
    [
      'schema_version',
      'id',
      'project_id',
      'request',
      'state',
      'revision',
      'reason',
      'created_at',
      'updated_at',
      'expires_at',
    ],
    ['new_generation', 'result_session_id'],
  )
  if (
    root.schema_version !== 1 ||
    !uuid(root.id) ||
    root.project_id !== projectId ||
    !STATES.includes(String(root.state)) ||
    !Number.isSafeInteger(root.revision) ||
    Number(root.revision) < 1 ||
    typeof root.reason !== 'string' ||
    ![
      '',
      'applied',
      'failed',
      'unsupported',
      'ownership_lost',
      'outcome_unknown',
      'expired',
      'cancelled',
      'authority_revoked',
    ].includes(root.reason) ||
    ![root.created_at, root.updated_at, root.expires_at].every((value) => parseIsoInstant(value)) ||
    (root.new_generation !== undefined && !uuid(root.new_generation)) ||
    (root.result_session_id !== undefined && !uuid(root.result_session_id))
  )
    invalid()
  const request = object(root.request)
  // The server omits optional null fields; every other value must match the
  // exact review the user submitted, including generation, account and scope.
  if (Object.keys(request).some((key) => !(key in expected))) invalid()
  for (const [key, value] of Object.entries(expected))
    if ((request[key] ?? null) !== value) invalid()
  if (
    root.state === 'completed' &&
    (root.reason !== 'applied' ||
      (expected.operation === 'start' &&
        (!uuid(root.new_generation) || !uuid(root.result_session_id))))
  )
    invalid()
  return {
    id: String(root.id),
    projectId,
    state: root.state as IntentState,
    revision: Number(root.revision),
    reason: String(root.reason),
    createdAt: String(root.created_at),
    updatedAt: String(root.updated_at),
    expiresAt: String(root.expires_at),
    newGeneration: (root.new_generation as string | undefined) ?? null,
    resultSessionId: (root.result_session_id as string | undefined) ?? null,
  }
}
export async function loadHabitatRuntimes(projectId: number, signal?: AbortSignal) {
  return parseHabitatRuntimes(
    epoch(
      await api.getWithMeta<unknown>(`/projects/${projectId}/lifecycle/v1/runtimes`, { signal }),
    ),
    projectId,
  )
}
export async function submitHabitatIntent(
  projectId: number,
  request: HabitatLifecycleRequest,
  signal?: AbortSignal,
) {
  // api.post owns CSRF and authentication/permission-generation fencing.
  return parseHabitatIntent(
    await api.post<unknown>(`/projects/${projectId}/lifecycle/v1/intents`, request, { signal }),
    projectId,
    request,
  )
}
export async function loadHabitatIntent(
  projectId: number,
  id: string,
  request: HabitatLifecycleRequest,
  signal?: AbortSignal,
) {
  if (!uuid(id)) invalid()
  const result = parseHabitatIntent(
    epoch(
      await api.getWithMeta<unknown>(`/projects/${projectId}/lifecycle/v1/intents/${id}`, {
        signal,
      }),
    ),
    projectId,
    request,
  )
  if (result.id !== id) invalid()
  return result
}
export async function cancelHabitatIntent(
  intent: HabitatIntent,
  request: HabitatLifecycleRequest,
  signal?: AbortSignal,
) {
  return parseHabitatIntent(
    await api.post<unknown>(
      `/projects/${intent.projectId}/lifecycle/v1/intents/${intent.id}/cancel`,
      { expected_revision: intent.revision },
      { signal },
    ),
    intent.projectId,
    request,
  )
}
