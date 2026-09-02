/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 *
 * PAI-884 — secret-free terminal handoff for the explicit orchestrator pin.
 */

const INSTANCE_NAME = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/
const PROJECT_KEY = /^[A-Z][A-Z0-9]{2,9}$/
const AGENT_KEY = /^[a-z][a-z0-9_-]{0,31}$/

type UnknownRecord = Record<string, unknown>

export interface OrchestratorSetupAgent {
  projectId: number
  key: string
}

function record(value: unknown): UnknownRecord | null {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
    ? (value as UnknownRecord)
    : null
}

function unicodeScalarString(value: string): boolean {
  for (let index = 0; index < value.length; index += 1) {
    const unit = value.charCodeAt(index)
    if (unit >= 0xd800 && unit <= 0xdbff) {
      const next = value.charCodeAt(index + 1)
      if (!(next >= 0xdc00 && next <= 0xdfff)) return false
      index += 1
    } else if (unit >= 0xdc00 && unit <= 0xdfff) return false
  }
  return true
}

export function setupInstanceName(value: unknown): string | null {
  const health = record(value)
  if (!health || health.agent_bus_identity_enforced !== true) return null
  const deployment = health.deployment_instance
  const bus = health.agent_bus_instance
  if (
    typeof deployment !== 'string' ||
    deployment === 'default' ||
    deployment !== bus ||
    deployment !== deployment.trim() ||
    !INSTANCE_NAME.test(deployment)
  )
    return null
  return deployment
}

export function setupAgents(value: unknown, projectId: number): OrchestratorSetupAgent[] | null {
  if (!Array.isArray(value)) return null
  const seen = new Set<string>()
  const agents: OrchestratorSetupAgent[] = []
  for (const candidate of value) {
    const agent = record(candidate)
    if (
      !agent ||
      agent.project_id !== projectId ||
      typeof agent.name !== 'string' ||
      !AGENT_KEY.test(agent.name) ||
      agent.name === 'web-ui' ||
      seen.has(agent.name)
    )
      return null
    seen.add(agent.name)
    agents.push({ projectId, key: agent.name })
  }
  return agents
}

export function validSetupDisplayLabel(value: string): boolean {
  return (
    value.length > 0 &&
    value === value.trim() &&
    !/[\u0000-\u001f\u007f]/.test(value) &&
    unicodeScalarString(value) &&
    new TextEncoder().encode(value).byteLength <= 64
  )
}

function shellQuote(value: string): string {
  return `'${value.replace(/'/g, `'"'"'`)}'`
}

export function orchestratorSetupCommand(input: {
  instance: string
  project: string
  agent: string
  displayLabel: string
}): string | null {
  if (
    !INSTANCE_NAME.test(input.instance) ||
    input.instance === 'default' ||
    !PROJECT_KEY.test(input.project) ||
    !AGENT_KEY.test(input.agent) ||
    input.agent === 'web-ui' ||
    !validSetupDisplayLabel(input.displayLabel)
  )
    return null
  return [
    'paimos',
    '--instance',
    shellQuote(input.instance),
    'orchestrator',
    'set',
    '--project',
    shellQuote(input.project),
    '--agent',
    shellQuote(input.agent),
    '--display-label',
    shellQuote(input.displayLabel),
  ].join(' ')
}
