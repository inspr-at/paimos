// SPDX-License-Identifier: AGPL-3.0-only
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { can } from '../../lib/authz'
import { confirmAction } from '../../lib/confirm'
import { removeSession, removeStaleSessions, undoRemoval, type HarnessSession } from '../../lib/agents'
import { removalConsequence } from './sessionActions'
import { toast } from '../../lib/toast'
import { useAgents } from '../../stores/agents'
import { useSession } from '../../stores/session'

// The server's batch eligibility: no accepted heartbeat (or, without one, no
// registration) for 15 minutes. The UI count is a preview; the server decides.
export const STALE_AFTER_MS = 15 * 60_000
export const isStale = (s: HarnessSession, now: number) => !s.archived_at && Date.parse(s.heartbeat_at ?? s.created_at) < now - STALE_AFTER_MS

// Removal hides a registration from Agents without touching its process. People
// with project access may remove any session; agents never can.
export function useSessionRemoval() {
  const agents = useAgents()
  const identity = useSession()
  const router = useRouter()
  const busy = ref(false)
  const isPerson = () => identity.identity?.principal.kind === 'person'
  const canRemove = (s: HarnessSession) => isPerson() && !s.archived_at && can('harness.read', s.project_id)

  async function leaveIfSelected(id: string) {
    if (router.currentRoute.value.params.sessionId === id) await router.replace('/agents')
  }

  // quick: No heartbeat, Lost contact or stopped. One click, then an undo toast.
  // A live session asks first; the process consequence is said only when true.
  async function removeOne(session: HarnessSession, label: string, quick = false) {
    if (busy.value || !canRemove(session)) return
    if (!quick) {
      const ok = await confirmAction({ title: `Remove ${label}?`, body: removalConsequence(session, Date.now()) || undefined, confirmLabel: 'Remove' })
      if (!ok) return
    }
    busy.value = true
    try {
      const result = await removeSession(session, 'Removed from Agents by a person')
      agents.recordRemoval(result.session)
      await leaveIfSelected(session.id)
      // Undo only where the server says this person may undo the event (events.undo).
      const event = result.undoable === true ? result.event_id : undefined
      toast(`Removed ${label}`, event ? { timeout: 8000, action: { label: 'Undo', run: () => void undo(event, label) } } : {})
    } catch (error) {
      toast(error instanceof Error ? error.message : 'Removal failed. Please retry.', { tone: 'error' })
    } finally { busy.value = false }
  }

  async function undo(eventId: number, label: string) {
    try {
      agents.recordRemoval(await undoRemoval(eventId))
      toast(`${label} is back`)
    } catch (error) {
      toast(error instanceof Error ? `Could not undo: ${error.message}` : 'Could not undo the removal.', { tone: 'error' })
    }
  }

  // One confirmation for every stale session the person may remove. Each
  // project answers at most 200 per request; repeat while it reports more.
  async function clearStale(sessions: HarnessSession[]) {
    const eligible = sessions.filter(canRemove)
    if (busy.value || !eligible.length) return
    const n = eligible.length
    const ok = await confirmAction({
      title: `Remove ${n} ${n === 1 ? 'session' : 'sessions'} without a heartbeat for 15 minutes?`,
      body: `${n === 1 ? 'The process is' : 'Processes are'} not stopped; late heartbeats are ignored.`,
      confirmLabel: 'Remove',
    })
    if (!ok) return
    busy.value = true
    let count = 0
    try {
      for (const project of [...new Set(eligible.map(s => s.project_id))]) {
        for (let more = true, rounds = 0; more && rounds < 50; rounds++) {
          const result = await removeStaleSessions(project, 'Removed from Agents: no accepted heartbeat for 15 minutes')
          for (const item of result.items) {
            agents.recordRemoval(item.session)
            count++
            await leaveIfSelected(item.session.id)
          }
          more = !!result.more && result.items.length > 0
        }
      }
      toast(count ? `Removed ${count} ${count === 1 ? 'session' : 'sessions'}. Processes were not stopped.` : 'Nothing was stale any more.')
    } catch (error) {
      toast(`${count ? `Removed ${count}. ` : ''}${error instanceof Error ? error.message : 'Removal failed. Please retry.'}`, { tone: 'error' })
    } finally { busy.value = false }
  }

  return { busy, canRemove, removeOne, clearStale }
}
