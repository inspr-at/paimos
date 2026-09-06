import { api } from '@/api/client'
import { parseIsoInstant } from '@/services/agentModeAggregateSchema'
import { epoch, fields, invalid, object, uuid } from './habitatBoundary'

export type RuntimeLayerHealth = {
  layer: 'reporter' | 'primary' | 'fallback' | 'attention'
  state: 'healthy' | 'unhealthy' | 'unknown'
  status: 'fresh' | 'stale' | 'offline' | 'unknown'
  reason:
    | 'recovered'
    | 'consumer_crash_loop'
    | 'consumer_authority_unavailable'
    | 'consumer_transport_failed'
    | 'not_reported'
  failure_count: number
  updated_at?: string
}
export type RuntimeHealth = {
  runtime_id: string
  runtime_generation: string
  machine_id: string
  expires_at: string
  status: 'fresh' | 'offline'
  layers: RuntimeLayerHealth[]
}
export type RuntimeHealthPage = {
  schema_version: 1
  observed_at: string
  runtimes: RuntimeHealth[]
}
export function parseRuntimeHealth(value: unknown): RuntimeHealthPage {
  const root = object(value)
  fields(root, ['schema_version', 'observed_at', 'runtimes'])
  if (
    root.schema_version !== 1 ||
    !parseIsoInstant(root.observed_at) ||
    !Array.isArray(root.runtimes) ||
    root.runtimes.length > 32
  )
    invalid()
  const ids = new Set<string>(),
    machines = new Set<string>()
  for (const raw of root.runtimes as unknown[]) {
    const runtime = object(raw)
    fields(runtime, [
      'runtime_id',
      'runtime_generation',
      'machine_id',
      'expires_at',
      'status',
      'layers',
    ])
    if (
      !uuid(runtime.runtime_id) ||
      !uuid(runtime.runtime_generation) ||
      typeof runtime.machine_id !== 'string' ||
      !/^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/.test(runtime.machine_id) ||
      !parseIsoInstant(runtime.expires_at) ||
      !['fresh', 'offline'].includes(String(runtime.status)) ||
      !Array.isArray(runtime.layers) ||
      runtime.layers.length !== 4 ||
      ids.has(String(runtime.runtime_id)) ||
      machines.has(String(runtime.machine_id))
    )
      invalid()
    ids.add(String(runtime.runtime_id))
    machines.add(String(runtime.machine_id))
    const seen = new Set<string>()
    for (const rawLayer of runtime.layers as unknown[]) {
      const layer = object(rawLayer)
      fields(layer, ['layer', 'state', 'status', 'reason', 'failure_count'], ['updated_at'])
      if (
        !['reporter', 'primary', 'fallback', 'attention'].includes(String(layer.layer)) ||
        seen.has(String(layer.layer)) ||
        !['healthy', 'unhealthy', 'unknown'].includes(String(layer.state)) ||
        !['fresh', 'stale', 'offline', 'unknown'].includes(String(layer.status)) ||
        ![
          'recovered',
          'consumer_crash_loop',
          'consumer_authority_unavailable',
          'consumer_transport_failed',
          'not_reported',
        ].includes(String(layer.reason)) ||
        !Number.isSafeInteger(layer.failure_count) ||
        Number(layer.failure_count) < 0 ||
        Number(layer.failure_count) > 10 ||
        (layer.updated_at !== undefined && !parseIsoInstant(layer.updated_at))
      )
        invalid()
      if (
        layer.reason === 'not_reported' &&
        (layer.state !== 'unknown' || layer.updated_at !== undefined || layer.failure_count !== 0)
      )
        invalid()
      if (layer.reason !== 'not_reported' && layer.updated_at === undefined) invalid()
      seen.add(String(layer.layer))
    }
  }
  return root as unknown as RuntimeHealthPage
}
export async function loadRuntimeHealth(projectId: number, signal?: AbortSignal) {
  if (!Number.isSafeInteger(projectId) || projectId < 1) invalid()
  return parseRuntimeHealth(
    epoch(
      await api.getWithMeta<unknown>(`/projects/${projectId}/lifecycle/v1/runtime-health`, {
        signal,
      }),
    ),
  )
}
