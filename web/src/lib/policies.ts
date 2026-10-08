// SPDX-License-Identifier: AGPL-3.0-only
import { ref } from 'vue'
import { api } from './api.ts'
import { createScope } from './identityScope.ts'
import { brand } from './brand.ts'
import type { Permission } from './access.ts'

export const POLICY_TABS = [
  { id: 'ladders', label: 'Review ladders', note: 'See models' },
  { id: 'keys', label: 'Agent key limits', note: 'See roles · person session' },
  { id: 'elsewhere', label: 'Rules elsewhere', note: 'Any signed-in person' },
] as const
export type PolicyTab = typeof POLICY_TABS[number]['id']
export const POLICY_ROLES = [
  { id: 'scout', label: 'Scout', detail: 'Read-only discovery and triage.' },
  { id: 'mechanical', label: 'Mechanical', detail: 'Well-specified, reversible work.' },
  { id: 'build', label: 'Build', detail: 'Ordinary implementation with tests.' },
  { id: 'build-hard', label: 'Build hard', detail: 'Complex or sensitive implementation.' },
  { id: 'review-gate', label: 'Review gate', detail: 'Read-only review by a different model family.' },
] as const
export type PolicyRole = typeof POLICY_ROLES[number]['id']
export interface PolicyStep {
  role: PolicyRole; priority: number; profile_id: string; state: string; reason: string; valid_until: string | null
  profile: { id: string; slug: string; display_name?: string; model_version?: string; model: string; family: string; harness: string; effort: string; tier: string; enabled: boolean }
}
export interface PolicyLadder {
  role: PolicyRole; steps: PolicyStep[]; setup: boolean; truncated: boolean
  routes?: Omit<PolicyStep, 'profile'>[]; edit_token?: string | null; can_edit?: boolean
  order_mode?: 'legacy' | 'saved'; managed_fallback_order?: string[]
  dispatch_family_order: string[]; review_floors: string[]
}
export const truncatedLadder = () => `Showing the first 50 steps; the rest are applied by ${brand.value.short_name} but not listed here.`
export const deniedPolicy = (tab: PolicyTab) => `You can't see this: it needs ${tab === 'ladders' ? 'See models' : tab === 'keys' ? 'See roles and a person session' : 'a person session'}.`

export interface ElsewhereRule { title: string; text: string; owner: string; status: 'Enforced' | 'Advisory'; to?: string; permissions?: string[] }
export const ELSEWHERE_RULES: readonly ElsewhereRule[] = [
  { title: 'Who holds which permission', text: 'Role bindings determine what a person or agent may do.', owner: 'Access', status: 'Enforced', to: '/settings/access/roles', permissions: ['members.read', 'roles.read'] },
  { title: 'Access log', text: 'Access changes are recorded in the audit log.', owner: 'Access', status: 'Enforced', to: '/settings/access/audit?category=access', permissions: ['audit.read'] },
  { title: 'Approvals in force', text: 'An agent proposes an exception; a person decides. Grants are scoped, expire and can be revoked.', owner: 'Agents', status: 'Enforced', to: '/agents', permissions: ['approvals.read'] },
  { title: 'Working dial', text: 'The working dial is managed on the Agents screen.', owner: 'Agents', status: 'Advisory', to: '/agents', permissions: ['harness.read'] },
  { title: 'Agent rules', text: 'Prose guides agents. It does not authorize a write. Doctrine stays in git.', owner: 'Agent rules', status: 'Advisory', to: '/settings/agent-rules', permissions: ['rules.read'] },
  { title: 'Parallel limit and accounts', text: 'Account admission checks the account’s parallel limit.', owner: 'Accounts', status: 'Enforced', to: '/settings/accounts', permissions: ['account.read'] },
  { title: 'Model preferences', text: 'Model rules live on the Models board: pin to top, pin to bottom, not allowed. Projects may tighten workspace rules.', owner: 'Model preferences', status: 'Enforced' },
  { title: 'Review ladder editing', text: 'Model registry owns the configured steps. Change one complete role order on the first tab.', owner: 'Model registry', status: 'Enforced' },
  { title: 'Fix rounds and lane pause', text: 'Lane coordination owns fix rounds and budgets; this page carries no copied limits.', owner: 'Lane coordination · no editor yet', status: 'Advisory' },
  { title: 'Question decisions', text: 'The decision handler requires a person session without an Authorization header. Bearer keys are refused earlier by the middleware.', owner: 'Questions', status: 'Enforced' },
  { title: 'Publishing agent rules', text: 'Publishing and restoring require a person, in the handler and the transaction helper. Bearer keys are refused by the middleware.', owner: 'Agent rules', status: 'Enforced' },
  { title: 'Release planning and membership', text: 'Release writes require a person. Agent keys can read allowed release routes; the middleware refuses planning and membership writes.', owner: 'Releases', status: 'Enforced' },
  { title: 'Locked doctrine changes', text: 'The proposal handler accepts an agent principal for unlocked rules. A change to or from a locked rule requires a person without a key creator. Bearer keys cannot reach the proposal handler.', owner: 'Doctrine', status: 'Enforced' },
  { title: 'Merge queue and repository ownership', text: 'GitHub rulesets and code owners govern repository changes outside this workspace.', owner: 'GitHub', status: 'Advisory' },
]
export const ownerLinks = (allowed: (permission: string) => boolean) => ELSEWHERE_RULES.filter(row => row.to && row.permissions?.every(allowed))
export function keyLimits(registry: Permission[]) {
  return (['high', 'medium', 'low'] as const).map(risk => ({ risk, items: registry.filter(p => p.agent_grantable === false && p.risk === risk) })).filter(group => group.items.length)
}
export function stepName(step: PolicyStep) { return [step.profile.display_name || step.profile.model || step.profile.slug, step.profile.model_version].filter(Boolean).join(' ') }
export function stepState(step: PolicyStep, now: number) {
  if (step.state === 'available') return 'Available'
  if (step.valid_until && Date.parse(step.valid_until) <= now) return 'Suspension expired · eligible again'
  const state = ({ unavailable: 'Unavailable', conserved: 'Conserved', budget_limited: 'Budget limited' } as Record<string, string>)[step.state] ?? step.state
  const expiry = step.valid_until ? new Date(step.valid_until) : null
  const time = expiry && new Intl.DateTimeFormat('en-GB', { timeZone: 'UTC', hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }).format(expiry)
  const date = expiry && new Intl.DateTimeFormat('en-GB', { timeZone: 'UTC', day: 'numeric', month: 'short', year: 'numeric' }).format(expiry)
  return `${state}${step.reason ? ` · ${step.reason}` : ''}${expiry ? ` · until ${time} UTC (${date})` : ''}`
}

type PolicyAnswer = PolicyLadder | Permission[]
type PolicyRead = (path: string, signal: AbortSignal) => Promise<PolicyAnswer>
async function readPolicy(path: string, signal: AbortSignal): Promise<PolicyAnswer> {
  const response = await api(path, { signal })
  if (!response.ok) throw Object.assign(new Error('The rules could not be loaded.'), { status: response.status })
  return response.json()
}
export function createPoliciesReader(owner: () => string, read: PolicyRead = readPolicy) {
  const scope = createScope(owner), lane = scope.lane()
  const state = ref<'loading' | 'loaded' | 'denied' | 'failed'>('loading')
  const ladder = ref<PolicyLadder | null>(null), registry = ref<Permission[]>([])
  function reset() { scope.reset(); ladder.value = null; registry.value = []; state.value = 'loading' }
  async function load(tab: PolicyTab, role: PolicyRole, allowed: boolean) {
    lane.cancel(); ladder.value = null; registry.value = []
    if (!owner() || !allowed) { state.value = 'denied'; return }
    if (tab === 'elsewhere') { state.value = 'loaded'; return }
    state.value = 'loading'
    await lane.run(async ({ after, signal }) => {
      const path = tab === 'ladders' ? `/models/routes?role=${encodeURIComponent(role)}` : '/authz/permissions'
      await after(read(path, signal), answer => {
        if (tab === 'ladders') {
          const data = answer as PolicyLadder
          if (data.role !== role || !Array.isArray(data.steps) || data.steps.length > 50 || typeof data.setup !== 'boolean' || typeof data.truncated !== 'boolean' || !Array.isArray(data.dispatch_family_order) || !Array.isArray(data.review_floors)) throw new Error('Invalid ladder answer')
          ladder.value = data
        } else {
          if (!Array.isArray(answer)) throw new Error('Invalid permission answer')
          registry.value = answer
        }
        state.value = 'loaded'
      })
    }, { failed: error => { state.value = (error as { status?: number })?.status === 403 ? 'denied' : 'failed' } })
  }
  return { state, ladder, registry, reset, load, dispose: scope.dispose }
}
