// SPDX-License-Identifier: AGPL-3.0-only
import { harnessLabel } from '../../lib/agentState'
import type { useAgents } from '../../stores/agents'

// Who asks for a gate, by name. A principal without a name reads as its session
// ("Claude coordinator") or plainly "An agent"; the raw ID only goes in the tip.
export interface Asker { name: string; harness: string; tip: string }
export function askerOf(agents: ReturnType<typeof useAgents>, principalId: string, agentName?: string | null): Asker {
  const found = agents.askerName(principalId, agentName)
  if (found.name !== `Agent ${principalId.slice(0, 8)}`) return { ...found, tip: '' }
  const session = agents.sessions.find(s => s.agent_principal_id === principalId)
  const tip = `Agent ${principalId}`
  if (session?.display_label?.trim()) return { name: session.display_label.trim(), harness: '', tip }
  if (session) return { name: `${harnessLabel(session.harness)} ${session.role}`, harness: '', tip }
  return { name: 'An agent', harness: '', tip }
}
