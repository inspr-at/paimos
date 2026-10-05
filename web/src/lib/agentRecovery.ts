// SPDX-License-Identifier: AGPL-3.0-only
import { onScopeDispose, reactive, watch } from 'vue'
import { useSession } from '../stores/session'
import { can } from './authz'
import { readAgentRecovery, requestAgentRecovery, type HarnessSession } from './agents'
import { toast } from './toast'
import { recoveryAction } from '../components/agents/sessionActions'

export function useAgentRecovery(visibleSession?: () => string | undefined) {
  const auth = useSession()
  const busy = reactive<Record<string, boolean>>({})
  let epoch = 0
  let disposed = false
  const timers = new Set<ReturnType<typeof setTimeout>>()
  const clear = () => { epoch++; for (const timer of timers) clearTimeout(timer); timers.clear(); for (const id of Object.keys(busy)) delete busy[id] }
  watch(() => auth.identity?.principal.id, clear)
  if (visibleSession) watch(visibleSession, clear)
  onScopeDispose(() => { disposed = true; clear() })
  const action = (s: HarnessSession) => visibleSession && visibleSession() !== s.id ? '' : recoveryAction(s, auth.identity?.principal.kind === 'person', can)
  async function request(s: HarnessSession, name: string) {
    const kind = action(s)
    if (!kind || busy[s.id] || !s.agent_recovery) return
    // Capture the exact record and observation before sending. A response or
    // toast from another person/mounted workspace cannot affect this one.
    const id = s.id, project = s.project_id, owner = auth.identity?.principal.id, started = epoch
    const current = () => !disposed && epoch === started && auth.identity?.principal.id === owner && (!visibleSession || visibleSession() === id)
    const key = `agent-recovery:${id}`
    const requestID = crypto.randomUUID()
    const observation = s.agent_recovery.observed_revision
    busy[id] = true
    try {
      const accepted = await requestAgentRecovery(project, id, { request_id: requestID, expected_revision: observation, action: kind })
      if (!current()) return
      if (accepted.id !== requestID || accepted.session_id !== id) throw new Error('Recovery response did not match this session.')
      toast(`${name}: ${kind === 'restart' ? 'Restart' : 'Reconnect'} requested. Waiting for the daemon.`, { key })
      const deadline = Date.now() + 50_000
      const poll = async () => {
        if (!current()) return
        try {
          const result = await readAgentRecovery(project, id, requestID)
          if (!current()) return
          if (result.id !== requestID || result.session_id !== id) throw new Error('Recovery response did not match this session.')
          if (result.state === 'completed' || result.state === 'expired') {
            const success = result.outcome === 'reconnected' || result.outcome === 'continuation_queued'
            const message = result.outcome === 'reconnected' ? 'Heartbeat and inbox reporting restored.' : result.outcome === 'continuation_queued' ? 'Previous process exited. Continuation queued for normal dispatch.' : result.outcome === 'unconfirmed' ? 'Recovery outcome is unconfirmed. Refresh before trying again.' : 'Recovery was rejected; no continuation was queued.'
            toast(`${name}: ${message}`, { key, tone: success ? 'info' : 'error' })
            delete busy[id]
            return
          }
          if (Date.now() >= deadline) throw new Error('Recovery outcome is unconfirmed. Refresh before trying again.')
          const timer = setTimeout(() => { timers.delete(timer); void poll() }, 1000)
          timers.add(timer)
        } catch (error) {
          if (current()) { delete busy[id]; toast(`${name}: ${error instanceof Error ? error.message : 'Recovery outcome is unconfirmed.'}`, { key, tone: 'error' }) }
        }
      }
      void poll()
    } catch (error) {
      if (current()) { delete busy[id]; toast(`${name}: ${error instanceof Error ? error.message : 'Recovery request failed.'}`, { key, tone: 'error' }) }
    }
  }
  return { action, busy, request }
}
