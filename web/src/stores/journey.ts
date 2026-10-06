// SPDX-License-Identifier: AGPL-3.0-only
import { defineStore } from 'pinia'
import { ref } from 'vue'
import { APIError } from '../lib/api'
import { agreeRequirements, getJourney, GATE_OF_ACTION, matchesJourneyConfirmation, offeredApproval, postAction, putProfile, type ActionKey, type Journey, type JourneyConfirmation, type Profile } from '../lib/journey'
import { useAgents } from './agents'

// The journey projection per project (the footer's flow pill and the
// Journey view share it). Every write carries the revision it was made on; a
// stale revision reloads the projection and says so instead of retrying.
const RELEASE_ACTIONS: ActionKey[] = ['start_build', 'mark_candidate', 'approve_candidate', 'reject_candidate', 'approve_deploy', 'renew_candidate', 'renew_deploy', 'renew_permit', 'retry_deploy', 'approve_permit', 'plan_next_release']
export class StaleJourney extends Error {}

export const useJourney = defineStore('journey', () => {
  const agents = useAgents()
  const journeys = ref<Record<string, Journey>>({})
  const errors = ref<Record<string, string>>({})
  const loading = ref<Record<string, boolean>>({})
  const busy = ref(false)
  const inflight = new Map<string, Promise<Journey | null>>()

  async function load(projectId: string, force = false): Promise<Journey | null> {
    if (!force && journeys.value[projectId]) return journeys.value[projectId]
    const running = inflight.get(projectId)
    if (running) return running
    const task = (async () => {
      loading.value = { ...loading.value, [projectId]: true }
      try {
        const journey = await getJourney(projectId)
        journeys.value = { ...journeys.value, [projectId]: journey }
        errors.value = { ...errors.value, [projectId]: '' }
        return journey
      } catch (e) {
        errors.value = { ...errors.value, [projectId]: e instanceof APIError && e.status === 404 ? 'This project has no journey.' : e instanceof Error ? e.message : 'The journey could not be loaded.' }
        return null
      } finally {
        loading.value = { ...loading.value, [projectId]: false }
        inflight.delete(projectId)
      }
    })()
    inflight.set(projectId, task)
    return task
  }
  // A 409 on the revision means someone else moved the journey: show the new state.
  async function guard<T>(projectId: string, write: () => Promise<T>): Promise<T> {
    busy.value = true
    try { return await write() } catch (e) {
      if (e instanceof APIError && e.status === 409 && /revision/i.test(e.message)) {
        await load(projectId, true)
        throw new StaleJourney('The journey changed meanwhile. It now shows the newest state; check it and try again.')
      }
      throw e
    } finally { busy.value = false }
  }
  function set(projectId: string, journey: Journey) { journeys.value = { ...journeys.value, [projectId]: journey } }

  function assertConfirmation(confirmation: JourneyConfirmation) {
    if (!matchesJourneyConfirmation(confirmation, journeys.value[confirmation.projectId], agents.approvals)) {
      throw new StaleJourney('The journey or gate changed while you were confirming. Review the current decision and confirm again.')
    }
  }
  async function prepareConfirmation(confirmation: JourneyConfirmation) {
    assertConfirmation(confirmation)
    const requested = confirmation.approval
    if (!requested) return
    const gate = GATE_OF_ACTION[confirmation.nextKey]
    const current = gate && offeredApproval(agents.approvals, journeys.value[confirmation.projectId], gate, Date.now())
    if (!current || current.id !== requested.id) throw new Error('The gate is no longer available. Refresh before taking this step.')
    if (current.decision !== requested.decision) throw new StaleJourney('The gate decision changed while you were confirming. Review the current decision and confirm again.')
    if (requested.decision === null) await agents.decide(requested, 'approved', '')
  }
  async function act(confirmation: JourneyConfirmation<ActionKey>, options: { reason?: string } = {}) {
    const { projectId, action, revision, releaseId, approval } = confirmation
    const next = await guard(projectId, async () => {
      await prepareConfirmation(confirmation)
      // An in-flight refresh may finish during approval; recheck before the
      // action and always submit the revision the person actually confirmed.
      assertConfirmation(confirmation)
      return postAction(projectId, {
        action, expected_revision: revision, idempotency_key: crypto.randomUUID(),
        approval_request_id: approval?.id ?? null,
        ...(RELEASE_ACTIONS.includes(action) ? { release_id: releaseId } : {}),
        ...(options.reason ? { reason: options.reason } : {}),
      })
    })
    set(projectId, next)
    return next
  }
  async function agree(confirmation: JourneyConfirmation<'approve_requirements'>) {
    const { projectId, revision, approval } = confirmation
    if (!approval) throw new Error('The requirements gate is not available.')
    const result = await guard(projectId, async () => {
      await prepareConfirmation(confirmation)
      assertConfirmation(confirmation)
      return agreeRequirements(projectId, { expected_revision: revision, approval_request_id: approval.id, idempotency_key: crypto.randomUUID() })
    })
    await load(projectId, true)
    return result
  }
  async function profile(projectId: string, value: Profile) {
    const journey = journeys.value[projectId]
    if (!journey || journey.profile === value) return journey
    const next = await guard(projectId, () => putProfile(projectId, value, journey.revision))
    set(projectId, next)
    return next
  }
  return { journeys, errors, loading, busy, load, act, agree, profile, set }
})
