// Browser lifecycle boundary; the server and agentd remain execution authorities.
import { api } from '@/api/client'
import { epoch, fields, invalid, object, positive, uuid } from './habitatBoundary'
import { parseIsoInstant } from '@/services/agentModeAggregateSchema'
export interface HabitatRuntime {
  id: string
  project_id: number
  generation: string
  machine_id: string
  account_label: string
  accounts?: { key: string; label: string }[]
  schema_version?: 2
  workspaces: { handle: string; identity: string; label?: string }[]
  profiles: { id: string; version: string }[]
  expires_at: string
  sessions: { session_id: string; generation: string; workspace_handle?: string }[]
}
export type HabitatStartRequest = {
  request_key: string
  operation: 'start'
  runtime_id: string
  runtime_generation: string
  account_label: string
  account_key?: string
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
export type HabitatExistingRequest = Omit<HabitatStartRequest, 'operation'> & {
  operation: 'attach' | 'reassign' | 'restart'
  session_id: string
  session_generation: string
  expected_revision: number
}
export type HabitatRepairRequest = {
  request_key: string
  operation: 'repair'
  runtime_id: string
  runtime_generation: string
  account_label: string
  account_key?: string
  ttl_seconds: number
  repair_layer: 'reporter' | 'listeners'
}
export type HabitatLifecycleRequest =
  | HabitatStartRequest
  | HabitatExistingRequest
  | HabitatRepairRequest
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
const ACCOUNTS = [
  'chatgpt',
  'api_key',
  'claude_ai_max',
  'claude_ai_pro',
  'claude_ai_team',
  'claude_ai_enterprise',
  'console',
  'pi_context',
  'cursor_context',
]
const STATES = ['requested', 'claimed', 'executing', 'completed', 'failed', 'expired', 'cancelled']
const token = (value: unknown): value is string =>
  typeof value === 'string' && /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/.test(value)
const accountKey = (value: unknown): value is string =>
  token(value) && !ACCOUNTS.includes(value) && value !== 'unknown' && value !== 'local_probe'
/** Validate even locally restored review snapshots. Local storage is untrusted. */
export function parseHabitatRequest(value: unknown): HabitatLifecycleRequest {
  const row = object(value)
  const common = [
    'request_key',
    'operation',
    'runtime_id',
    'runtime_generation',
    'account_label',
    'ttl_seconds',
  ]
  const binding = [
    'workspace_handle',
    'agent_name',
    'dispatch_profile_id',
    'dispatch_profile_version',
    'work_shape',
    'role',
  ]
  const nullable = ['ticket_id', 'parent_harness_session_id']
  const existing = ['session_id', 'session_generation', 'expected_revision']
  if (!['start', 'attach', 'reassign', 'restart', 'repair'].includes(String(row.operation)))
    invalid()
  fields(
    row,
    [
      ...common,
      ...(row.operation === 'repair'
        ? ['repair_layer']
        : [...binding, ...(row.operation === 'start' ? [] : existing)]),
    ],
    row.operation === 'repair' ? ['account_key'] : [...nullable, 'account_key'],
  )
  if (
    !uuid(row.request_key) ||
    !uuid(row.runtime_id) ||
    !uuid(row.runtime_generation) ||
    !ACCOUNTS.includes(String(row.account_label)) ||
    (row.account_key !== undefined && !accountKey(row.account_key)) ||
    !Number.isSafeInteger(row.ttl_seconds) ||
    Number(row.ttl_seconds) < 30 ||
    Number(row.ttl_seconds) > 600
  )
    invalid()
  if (row.operation === 'repair') {
    if (row.repair_layer !== 'reporter' && row.repair_layer !== 'listeners') invalid()
    return { ...row } as HabitatRepairRequest
  }
  const ticket = row.ticket_id ?? null,
    parent = row.parent_harness_session_id ?? null
  if (
    !uuid(row.workspace_handle) ||
    !token(row.agent_name) ||
    String(row.agent_name).length > 64 ||
    !token(row.dispatch_profile_id) ||
    !token(row.dispatch_profile_version) ||
    !['worker', 'coordinator'].includes(String(row.role)) ||
    (ticket === null
      ? row.work_shape !== 'unknown'
      : !positive(ticket) || !['ship', 'scout'].includes(String(row.work_shape))) ||
    (parent !== null && !uuid(parent))
  )
    invalid()
  if (
    row.operation !== 'start' &&
    (!uuid(row.session_id) ||
      !uuid(row.session_generation) ||
      !positive(row.expected_revision) ||
      parent === row.session_id)
  )
    invalid()
  return { ...row, ticket_id: ticket, parent_harness_session_id: parent } as
    | HabitatStartRequest
    | HabitatExistingRequest
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
    ], ['accounts', 'schema_version'])
    const accounts = runtime.accounts
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
      seen.has(String(runtime.id)) ||
      (accounts === undefined
        ? runtime.schema_version !== undefined
        : runtime.schema_version !== 2 ||
          !Array.isArray(accounts) ||
          accounts.length < 1 ||
          accounts.length > 16)
    )
      invalid()
    seen.add(String(runtime.id))
    if (!Array.isArray(runtime.sessions) || runtime.sessions.length > 128) invalid()
    const sessionIds = new Set<string>()
    for (const raw of runtime.sessions as unknown[]) {
      const session = object(raw)
      fields(session, ['session_id', 'generation'], ['workspace_handle'])
      if (
        !uuid(session.session_id) ||
        !uuid(session.generation) ||
        (session.workspace_handle !== undefined && !uuid(session.workspace_handle)) ||
        sessionIds.has(String(session.session_id))
      )
        invalid()
      sessionIds.add(String(session.session_id))
    }
    const workspaceIds = new Set<string>()
    for (const raw of runtime.workspaces as unknown[]) {
      const workspace = object(raw)
      fields(workspace, ['handle', 'identity'], ['label'])
      if (
        !uuid(workspace.handle) ||
        (workspace.label !== undefined &&
          (typeof workspace.label !== 'string' ||
            !/^[A-Za-z0-9 ._-]{1,48}$/.test(workspace.label))) ||
        typeof workspace.identity !== 'string' ||
        !/^[a-f0-9]{64}$/.test(workspace.identity) ||
        workspaceIds.has(workspace.handle)
      )
        invalid()
      workspaceIds.add(String(workspace.handle))
    }
    for (const raw of runtime.sessions as unknown[]) {
      const mapping = object(raw)
      if (
        mapping.workspace_handle !== undefined &&
        !workspaceIds.has(String(mapping.workspace_handle))
      )
        invalid()
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
    if (accounts !== undefined) {
      const keys = new Set<string>()
      const labels = new Set<string>()
      for (const raw of accounts as unknown[]) {
        const choice = object(raw)
        fields(choice, ['key', 'label'])
        if (
          !accountKey(choice.key) ||
          typeof choice.label !== 'string' ||
          !/^[A-Za-z0-9][A-Za-z0-9 ._-]{0,47}$/.test(choice.label) ||
          ACCOUNTS.includes(String(choice.label)) ||
          choice.key === runtime.generation ||
          choice.key === runtime.id ||
          choice.key === runtime.account_label ||
          choice.label === runtime.account_label ||
          [...(runtime.profiles as { id: string; version: string }[])].some(
            (profile) => profile.id === choice.key || profile.version === choice.key,
          ) ||
          keys.has(String(choice.key)) ||
          labels.has(choice.label)
        )
          invalid()
        keys.add(String(choice.key))
        labels.add(String(choice.label))
      }
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
    (root.schema_version !== 1 && root.schema_version !== 2) ||
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
  expected = parseHabitatRequest(expected)
  const request = object(parseHabitatRequest(root.request))
  const named = expected.account_key !== undefined
  if ((named && root.schema_version !== 2) || (!named && root.schema_version !== 1)) invalid()
  // The server omits optional null fields; every other value must match the
  // exact review the user submitted, including generation, account and scope.
  if (Object.keys(request).some((key) => !(key in expected))) invalid()
  for (const [key, value] of Object.entries(expected))
    if ((request[key] ?? null) !== value) invalid()
  if (
    root.state === 'completed' &&
    (root.reason !== 'applied' ||
      (['start', 'restart'].includes(expected.operation) &&
        (!uuid(root.new_generation) || !uuid(root.result_session_id))))
  )
    invalid()
  const creating = expected.operation === 'start' || expected.operation === 'restart'
  if (creating ? !uuid(root.new_generation) : root.new_generation !== undefined) invalid()
  if (
    ['requested', 'claimed', 'executing'].includes(String(root.state)) &&
    (root.reason !== '' || root.result_session_id !== undefined)
  )
    invalid()
  if (
    root.state === 'completed' &&
    (expected.operation === 'attach' || expected.operation === 'reassign') &&
    root.result_session_id !== expected.session_id
  )
    invalid()
  if (root.state !== 'completed' && root.result_session_id !== undefined) invalid()
  if (root.state === 'cancelled' && root.reason !== 'cancelled') invalid()
  if (root.state === 'expired' && !['expired', 'outcome_unknown'].includes(String(root.reason)))
    invalid()
  if (
    root.state === 'failed' &&
    !['failed', 'unsupported', 'ownership_lost', 'outcome_unknown', 'authority_revoked'].includes(
      String(root.reason),
    )
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
  request = parseHabitatRequest(request)
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
  const result = parseHabitatIntent(
    await api.post<unknown>(
      `/projects/${intent.projectId}/lifecycle/v1/intents/${intent.id}/cancel`,
      { expected_revision: intent.revision },
      { signal },
    ),
    intent.projectId,
    request,
  )
  if (result.id !== intent.id || result.revision < intent.revision) invalid()
  return result
}
