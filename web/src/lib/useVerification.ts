// SPDX-License-Identifier: AGPL-3.0-only
import { computed, onBeforeUnmount, ref, watch, type ComputedRef } from 'vue'
import { api } from './api'
import { describeEnrollmentStatus, verificationFailureText } from './agentPairing'
import type { SignInReference } from './accountsOverview'

export const signinKey = (s: SignInReference) => `${s.computer.computer_id}/${s.enrollment.account_id}`
export const verificationActive = (s: SignInReference) => ['queued', 'starting', 'running', 'waiting', 'ownership_lost'].includes(s.enrollment.verification_state ?? '')
export function verificationWords(s: SignInReference): string {
  const e = s.enrollment
  if (e.verification_stalled) return 'Verification stalled. The helper must confirm process exit and settle its reports.'
  switch (e.verification_state) {
    case 'queued': {
      if (s.computer.connectivity !== 'online') return 'Verifying… Waiting for this computer to come online.'
      const readiness = describeEnrollmentStatus(s.computer, { ...e, verification_state: 'not_selected' })
      if (readiness && readiness !== 'Ready') return `Verification is queued. Waiting for this computer: ${readiness}. Check the sign-in or local setup on this computer.`
      return 'Verifying… Waiting for the computer to start the check.'
    }
    case 'starting': return 'Verifying… Starting the safe check.'
    case 'running': return 'Verifying… Check running.'
    case 'waiting': return 'Verifying… Waiting for the check to finish.'
    case 'ownership_lost': return 'Verification needs attention. Process exit has not been confirmed.'
    case 'completed': return 'Verification passed.'
    case 'unavailable': {
      const causes: Record<string, string> = { adapter_unsupported: 'the installed adapter cannot enforce safe verification', binding_incomplete: 'the verification binding is incomplete or unsafe', local_binding_missing: 'the approved account has no usable local binding' }
      return `Verification could not run: ${causes[e.verification_reason ?? ''] ?? 'safe verification is unavailable'}. Check the sign-in and helper on this computer.`
    }
    case 'failed': return verificationFailureText(e) || 'Verification failed. Check the sign-in on this computer.'
    case 'cancelled': return 'Verification cancelled. No successful result was confirmed.'
    case 'expired': return 'Verification expired. Verify the sign-in again.'
    case 'not_selected': return 'Not checked yet'
    default: return 'Not reported'
  }
}

// Every response remains bound to the person, screen, computer revision and
// reviewed run. Retain the acted-on row through queue/progress/result changes.
export function useVerification(scope: ComputedRef<string>, signins: ComputedRef<SignInReference[]>, allowed: (s: SignInReference) => boolean, refresh: () => Promise<{ ok: boolean }>) {
  const attempts = ref<Record<string, { prior: string | null | undefined; revision: number; run?: string; requesting: boolean; error?: string; refreshFailed?: boolean }>>({})
  let generation = 0
  const controllers = new Set<AbortController>()
  function reset() { generation++; for (const c of controllers) c.abort(); controllers.clear(); attempts.value = {} }
  watch(scope, reset)
  onBeforeUnmount(reset)
  watch(signins, current => {
    for (const [key, a] of Object.entries(attempts.value)) {
      const s = current.find(s => signinKey(s) === key)
      if (!s || s.computer.revision !== a.revision || !allowed(s) || !a.requesting && a.run && s.enrollment.verification_run_id !== a.prior && s.enrollment.verification_run_id !== a.run) delete attempts.value[key]
      else if (a.refreshFailed && s.enrollment.verification_run_id === a.run) { delete a.error; delete a.refreshFailed }
    }
  })
  const retained = computed(() => signins.value.filter(s => !!attempts.value[signinKey(s)]))
  function pending(s: SignInReference) {
    const a = attempts.value[signinKey(s)]
    return !!a?.requesting || !!a?.run && a.run !== s.enrollment.verification_run_id || verificationActive(s)
  }
  function message(s: SignInReference) {
    const a = attempts.value[signinKey(s)]
    if (a?.requesting) return 'Verifying… Requesting the safe check.'
    if (a?.error) return a.error
    if (a?.run && a.run !== s.enrollment.verification_run_id) return 'Verifying… Awaiting the computer report.'
    return a || verificationActive(s) ? verificationWords(s) : ''
  }
  async function request(s: SignInReference) {
    const key = signinKey(s), current = signins.value.find(s => signinKey(s) === key)
    if (!current || !allowed(current) || pending(current)) return
    const { computer: c, enrollment: e } = current, revision = c.revision, prior = e.verification_run_id
    if (!c.computer_id || !revision) return
    const owner = scope.value, turn = generation, controller = new AbortController()
    controllers.add(controller)
    const timer = setTimeout(() => controller.abort(), 10_000)
    attempts.value[key] = { prior, revision, requesting: true }
    const stillCurrent = (run?: string) => {
      const now = signins.value.find(s => signinKey(s) === key)
      return scope.value === owner && generation === turn && now?.computer.revision === revision && allowed(now) && (now.enrollment.verification_run_id === prior || !!run && now.enrollment.verification_run_id === run)
    }
    try {
      const response = await api(`/agent-pairing/computers/${c.computer_id}/enrollments/${e.account_id}/verify`, { method: 'POST', signal: controller.signal, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ expected_revision: revision, expected_verification_run_id: prior }) })
      if (!response.ok) {
        const problem = await response.json().catch(() => ({})) as { code?: string }
        throw new Error(response.status === 403 ? 'Only the account owner may verify it again.' : problem.code === 'verification_active' ? 'The helper must confirm that the previous check stopped and settle its reports before another can start.' : response.status === 409 ? 'The account or verification changed. Refresh before verifying again.' : 'Verification could not be requested.')
      }
      const created = await response.json() as { account_id?: string; run_id?: string }
      if (created.account_id !== e.account_id || typeof created.run_id !== 'string' || !/^[0-9a-f-]{36}$/i.test(created.run_id)) throw new Error('Verification response could not be confirmed. Refresh before trying again.')
      if (!stillCurrent(created.run_id)) return
      attempts.value[key] = { prior, revision, run: created.run_id, requesting: false }
      const result = await refresh()
      if (stillCurrent(created.run_id) && !result.ok) Object.assign(attempts.value[key]!, { refreshFailed: true, error: 'Verification was requested, but its report could not refresh. Refresh before trying again.' })
    } catch (error) {
      if (stillCurrent()) attempts.value[key] = { prior, revision, requesting: false, error: controller.signal.aborted ? 'Verification request timed out. Refresh to confirm whether it was accepted.' : error instanceof Error ? error.message : 'Verification could not be requested.' }
    } finally {
      clearTimeout(timer); controllers.delete(controller)
      if (scope.value === owner && generation === turn && attempts.value[key]) attempts.value[key]!.requesting = false
    }
  }
  return { retained, pending, message, request, reset }
}
