// SPDX-License-Identifier: AGPL-3.0-only
// Row copy for the session list. Uses only fields the session already carries.
// A heartbeat, activity flag or work shape is not an intended result.
import type { SessionView } from '../../stores/agents'

export type ModelProvider = 'openai' | 'anthropic' | 'xai' | 'google' | 'cursor' | 'unknown'

const PROVIDER_LABEL: Record<ModelProvider, string> = {
  openai: 'OpenAI',
  anthropic: 'Anthropic',
  xai: 'xAI',
  google: 'Google',
  cursor: 'Cursor',
  unknown: 'Unknown provider',
}

// ETA guidance is actionable only while a session speaks for a bound ticket.
// Both the list and detail panel use the same eligibility rule.
export function sessionEtaEligible(view: SessionView): boolean {
  return !!view.ticket && view.session.phase !== 'stopped' && !view.session.stopped_at && !view.session.archived_at
}

// A phrase is an explicit outcome. A ticket key or file token is not.
export function explicitOutcome(brief: string | null | undefined) {
  const text = brief?.trim() ?? ''
  return text && /\s/.test(text) ? text : ''
}

// Phrase brief, else the bound ticket's real title, else the brief token, else the existing label.
export function intendedResult(view: SessionView) {
  const outcome = explicitOutcome(view.session.brief)
  if (outcome) return outcome
  const title = view.ticket?.title?.trim()
  if (title) return title
  const brief = view.session.brief?.trim()
  if (brief) return brief
  return view.name
}

// The muted second line is the session identity, or host/project when the name is already the result.
export function sessionContext(view: SessionView, primary: string) {
  const name = view.name.trim()
  if (primary !== name) return name
  const host = view.session.host?.trim()
  if (host && host !== name) return host
  const project = view.projectTitle?.trim() || view.projectKey?.trim()
  if (project && project !== name) return project
  return name
}

// Known public model-name prefixes only. Harness identity never implies a provider.
export function modelProvider(model: string): ModelProvider {
  const value = model.trim().toLowerCase()
  if (!value) return 'unknown'
  if (/^(gpt|chatgpt|openai)|(?:^|[^a-z])o[134](?:[.-]|$)/.test(value) || value.includes('openai')) return 'openai'
  if (value.startsWith('claude') || value.includes('anthropic')) return 'anthropic'
  if (value.startsWith('grok') || /\bxai\b/.test(value)) return 'xai'
  if (value.startsWith('gemini') || value.startsWith('gemma')) return 'google'
  if (value.startsWith('cursor') || value.startsWith('composer')) return 'cursor'
  return 'unknown'
}

export interface SessionExecution {
  model: string
  effort: string
  modelLine: string
  account: string
  accountLine: string
  provider: ModelProvider
  providerLabel: string
}

// Unknown fields are omitted, never filled with "unknown" words.
export function sessionExecution(view: SessionView): SessionExecution {
  const reported = view.session.model?.trim() ?? ''
  const model = reported || view.model?.trim() || ''
  const effort = model ? (view.session.reasoning_effort?.trim() || '') : ''
  const account = view.session.account_label?.trim() || view.account?.trim() || ''
  const provider = modelProvider(model)
  return {
    model,
    effort,
    modelLine: [model, effort].filter(Boolean).join(' · '),
    account,
    accountLine: [view.harness, account].filter(Boolean).join(' · '),
    provider,
    providerLabel: PROVIDER_LABEL[provider],
  }
}
