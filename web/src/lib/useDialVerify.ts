// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1036: the dial's Accounts tile names what blocks a harness and offers the
// one existing action, Verify again, in place. The words and the rights come from
// the same sign-in reports as Accounts and computers; nothing new is derived.
import { computed } from 'vue'
import { can } from './authz'
import { DEFAULT_QUOTA_THRESHOLDS, glanceItems } from './accountsGlance'
import { overviewAccounts, type SignInReference } from './accountsOverview'
import { HARNESS_NAME } from './capacity'
import { signinKey, useVerification } from './useVerification'
import { useAgents } from '../stores/agents'
import { useCapacity } from '../stores/capacity'
import { useSession } from '../stores/session'

export interface DialProblem { text: string; signin: SignInReference; mayVerify: boolean; busy: boolean; feedback: string }
export function useDialVerify() {
  const agents = useAgents(), capacity = useCapacity(), session = useSession()
  const mayManage = computed(() => session.identity?.principal.kind === 'person' && can('account.manage'))
  const identityKey = computed(() => `${session.identity?.tenant.id ?? ''}/${session.identity?.principal.id ?? ''}`)
  const computers = computed(() => capacity.computers.filter(c => c.computer_id && !c.archived_at))
  const accounts = computed(() => overviewAccounts(agents.accounts, capacity.rows, computers.value))
  const signins = computed(() => accounts.value.flatMap(a => a.signins))
  const canVerify = (s: SignInReference) => mayManage.value && s.computer.computer_state === 'connected' && s.enrollment.state === 'connected' && s.enrollment.can_verify === true
    && s.computer.verification_capabilities?.[s.enrollment.harness]?.supported === true
  const verification = useVerification(identityKey, signins, canVerify, () => capacity.refreshComputers())
  /** What the tile said about a sign-in when it was acted on, kept while the check runs. */
  const said = new Map<string, string>()
  /** The first sign-in per harness that cannot start agents until someone acts, and one whose check is running. */
  const problems = computed<Record<string, DialProblem>>(() => {
    const out: Record<string, DialProblem> = {}
    const add = (s: SignInReference, text: string, mayVerify: boolean) => {
      const harness = s.enrollment.harness
      if (!out[harness]) out[harness] = { text, signin: s, mayVerify, busy: verification.pending(s), feedback: verification.message(s) }
    }
    for (const item of glanceItems(accounts.value, computers.value, DEFAULT_QUOTA_THRESHOLDS, agents.now)) {
      const s = item.signin
      if (!s || (item.kind !== 'verify' && item.kind !== 'attention')) continue
      const vendor = HARNESS_NAME[s.enrollment.harness] ?? s.enrollment.harness
      add(s, item.name.startsWith(`${vendor} `) ? item.name.slice(vendor.length + 1) : item.name, item.kind === 'verify' && canVerify(s))
    }
    // A check that was asked for stays visible through queue, progress and result, as on Accounts and computers.
    for (const s of verification.retained.value) add(s, said.get(signinKey(s)) ?? 'needs verifying', canVerify(s))
    return out
  })
  function verify(problem: DialProblem) {
    said.set(signinKey(problem.signin), problem.text)
    return verification.request(problem.signin)
  }
  return { problems, verify }
}
