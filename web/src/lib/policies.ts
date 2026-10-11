// SPDX-License-Identifier: AGPL-3.0-only
// Settings › Policies, rules only (AEON-1143/1165): who may do what. Which model
// runs is decided on Models, so nothing here names a model.
import type { IconName } from '../components/AppIcon.vue'

export type AskRisk = 'high' | 'medium' | 'low'
export interface AgentAsk { title: string; keys: readonly string[] }
export const ASK_RISKS: readonly { risk: AskRisk; label: string }[] = [
  { risk: 'high', label: 'High risk' },
  { risk: 'medium', label: 'Medium risk' },
  { risk: 'low', label: 'Low risk' },
]
// Every permission the registry marks as never grantable to an agent key, in
// plain words, grouped by the registry's risk. policies.test.ts holds this list
// to internal/authz, so a new exclusion fails the build until it is named here.
export const AGENT_ASKS: Readonly<Record<AskRisk, readonly AgentAsk[]>> = {
  high: [
    { title: 'Approve or deny what agents ask for', keys: ['approvals.decide', 'approvals.decide_high'] },
    { title: 'Answer questions agents ask', keys: ['questions.decide'] },
    { title: 'Change roles and members', keys: ['roles.manage', 'members.manage'] },
    { title: 'Transfer workspace ownership', keys: ['ownership.transfer'] },
    { title: 'Create and revoke API keys', keys: ['keys.manage'] },
    { title: 'Change model choices and locks', keys: ['model_prefs.manage'] },
    { title: 'Change how accounts are used', keys: ['account.use.manage'] },
    { title: 'Publish agent rules', keys: ['rules.publish'] },
    { title: 'Change workspace settings', keys: ['settings.manage'] },
    { title: 'Stop, recover or watch a harness', keys: ['harness.force_stop', 'harness.recover', 'harness.watch'] },
  ],
  medium: [
    { title: 'Connect a chat', keys: ['chat.bind'] },
    { title: 'Edit the portal profile', keys: ['profile.portal_write'] },
    { title: 'Accept a quote in the portal', keys: ['quotes.portal_accept'] },
  ],
  low: [
    { title: 'Read the audit log', keys: ['audit.read'] },
    { title: 'See API keys', keys: ['keys.read'] },
    { title: 'Read the portal profile and quotes', keys: ['profile.portal_read', 'quotes.portal_read'] },
  ],
}
/** The folded group's one-line preview: its rows as one sentence. */
export const askPreview = (items: readonly AgentAsk[]) => items.map((item, index) => index ? item.title.charAt(0).toLowerCase() + item.title.slice(1) : item.title).join(', ')

export interface ElsewhereRule { icon: IconName; title: string; text: string; owner: string; status: 'Enforced' | 'Advisory'; to?: string; permissions?: readonly string[] }
// Each rule is set and enforced where it lives; a link shows only when that
// place opens for the person, otherwise the owner is named without a link.
export const ELSEWHERE_RULES: readonly ElsewhereRule[] = [
  { icon: 'columns', title: 'Which model runs', text: 'Default, exceptions, fallback and locks.', owner: 'Models', status: 'Enforced', to: '/settings/models', permissions: ['models.read'] },
  { icon: 'gauge', title: 'Daily caps', text: 'Each provider’s cap; at the cap, the fallback or Wait.', owner: 'Agents', status: 'Enforced', to: '/agents', permissions: ['harness.read'] },
  { icon: 'key', title: 'Account use', text: 'Which accounts agents may use, and how hard.', owner: 'Accounts', status: 'Enforced', to: '/settings/accounts', permissions: ['account.read'] },
  { icon: 'inbox', title: 'Approvals in force', text: 'Exceptions a person granted; they expire.', owner: 'Decision Desk', status: 'Enforced', to: '/decision-desk', permissions: ['approvals.read'] },
  { icon: 'users', title: 'Who holds which permission', text: 'Roles decide what people and agent keys may do.', owner: 'Access', status: 'Enforced', to: '/settings/access/roles', permissions: ['members.read', 'roles.read'] },
  { icon: 'play', title: 'Autopilot', text: 'When tickets move on by themselves.', owner: 'Autopilot', status: 'Enforced', to: '/settings/autopilot', permissions: ['settings.manage'] },
  { icon: 'shield', title: 'Routine guardrails', text: 'A hard floor no routine can lift, plus scoped rules.', owner: 'Routines', status: 'Enforced', to: '/settings/routines', permissions: [] },
  { icon: 'list', title: 'Kinds of work', text: 'What each kind means; Models routes by them.', owner: 'Kinds of work', status: 'Enforced', to: '/settings/kinds', permissions: ['models.read'] },
  { icon: 'book', title: 'Agent rules and doctrine', text: 'Guides agents; it never authorises a write.', owner: 'Agent rules', status: 'Advisory', to: '/settings/agent-rules', permissions: ['rules.read'] },
  { icon: 'history', title: 'Access log', text: 'Every access change is recorded.', owner: 'Access', status: 'Enforced', to: '/settings/access/audit?category=access', permissions: ['audit.read'] },
  { icon: 'pause', title: 'Fix rounds and lane pause', text: 'Lane coordination owns them; no editor yet.', owner: 'Lane coordination', status: 'Advisory' },
  { icon: 'code', title: 'Merge queue and code owners', text: 'GitHub rules, outside this workspace.', owner: 'GitHub', status: 'Advisory' },
]
/** Where the owner opens for this person, or null when it would not open. */
export const ruleLink = (rule: ElsewhereRule, allowed: (permission: string) => boolean) => rule.to && rule.permissions?.every(allowed) ? rule.to : null

// "How reviewers are chosen" says the review-policy read fields in words.
const PROVIDER: Record<string, string> = { openai: 'OpenAI', xai: 'xAI', anthropic: 'Anthropic', cursor: 'Cursor', google: 'Google', local: 'local models' }
const providerName = (family: string) => PROVIDER[family] ?? family
const EFFORT: Record<number, string> = { 1: 'low', 2: 'medium', 3: 'high', 4: 'xhigh', 5: 'max' }
const either = (words: string[]) => words.length < 2 ? words.join('') : `${words.slice(0, -1).join(', ')} or ${words.at(-1)}`
const article = (word: string) => /^[aeiou]/i.test(word) ? 'an' : 'a'
export interface ReviewerRules { family_order: string[]; floor: { tiers: string[]; min_effort_level: number }; security_excludes: string[] }
export const familyOrderWords = (rules: ReviewerRules) => rules.family_order.map(providerName).join(', ')
export function floorSentence(rules: ReviewerRules) {
  const tiers = either(rules.floor.tiers)
  const level = EFFORT[rules.floor.min_effort_level]
  const lead = `Reviews run on ${article(tiers)} ${tiers} model at its highest level`
  return level === 'xhigh' ? `${lead}: xhigh, or max where a model has it.` : level ? `${lead}, at least ${level}.` : `${lead}.`
}
export function securitySentence(rules: ReviewerRules) {
  const names = rules.security_excludes.map(providerName)
  return names.length ? `Security work is never reviewed by ${article(names[0]!)} ${either(names)} model, nor by the author’s family.` : 'Security work is never reviewed by the author’s family.'
}
export function reviewerRules(value: Partial<ReviewerRules> | null | undefined): ReviewerRules | null {
  if (!value || !Array.isArray(value.family_order) || !Array.isArray(value.security_excludes) || !value.floor || !Array.isArray(value.floor.tiers) || !Number.isInteger(value.floor.min_effort_level)) return null
  return { family_order: [...value.family_order], floor: { tiers: [...value.floor.tiers], min_effort_level: value.floor.min_effort_level }, security_excludes: [...value.security_excludes] }
}
