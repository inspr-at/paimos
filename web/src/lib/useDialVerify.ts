// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1036: the dial's Accounts tile names what blocks a harness and offers the
// one existing action, Verify again, in place. The words and the rights come from
// the same sign-in reports as Accounts and computers; nothing new is derived.
import { computed } from 'vue'
import { can } from './authz'
import { DEFAULT_QUOTA_THRESHOLDS, glanceItems } from './accountsGlance'
import { overviewAccounts, type SignInReference } from './accountsOverview'
import { HARNESS_NAME } from './capacity'
import { useVerification } from './useVerification'
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
  /** The first sign-in per harness that cannot start agents until someone acts. */
  const problems = computed<Record<string, DialProblem>>(() => {
    const out: Record<string, DialProblem> = {}
    for (const item of glanceItems(accounts.value, computers.value, DEFAULT_QUOTA_THRESHOLDS, agents.now)) {
      const s = item.signin
      if (!s || (item.kind !== 'verify' && item.kind !== 'attention') || out[s.enrollment.harness]) continue
      const vendor = HARNESS_NAME[s.enrollment.harness] ?? s.enrollment.harness
      out[s.enrollment.harness] = {
        text: item.name.startsWith(`${vendor} `) ? item.name.slice(vendor.length + 1) : item.name,
        signin: s, mayVerify: item.kind === 'verify' && canVerify(s), busy: verification.pending(s), feedback: verification.message(s),
      }
    }
    return out
  })
  return { problems, verify: (s: SignInReference) => verification.request(s) }
}
