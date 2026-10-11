// SPDX-License-Identifier: AGPL-3.0-only
// Row copy for the session list. Uses only fields the session already carries.
// A heartbeat, activity flag or work shape is not an intended result.
import type { ModelProfile } from '../../lib/agents'
import { fullName, lineFallback } from '../../lib/modelsSimple'
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
  kind: 'ai' | 'media' | 'terminal'
  /** The reported or launched model id: search and the provider mark read it, the row never shows it. */
  model: string
  /** What the row shows: the registry's name for the model ("Opus 5.5"), or the run's label for media and terminal. */
  name: string
  effort: string
  modelLine: string
  account: string
  accountLine: string
  provider: ModelProvider
  providerLabel: string
}

// ---------- Model names (AEON-1211) ----------
// The row names a model the way Settings › Models does, never by its raw id: the registry's
// display name and version ("Opus 5.5", "GPT-6.1 Sol"), then the level ("Opus 5.5 · xhigh").
export interface ModelNames { bySlug: Map<string, ModelProfile>; byModel: Map<string, ModelProfile> }
const modelKey = (harness: string, model: string) => `${harness}\u0000${model}`
export function modelNames(profiles: readonly ModelProfile[]): ModelNames {
  const names: ModelNames = { bySlug: new Map(), byModel: new Map() }
  for (const profile of profiles) {
    if (profile.slug) names.bySlug.set(profile.slug, profile)
    const key = modelKey(profile.harness, profile.model)
    if (!names.byModel.has(key)) names.byModel.set(key, profile)
  }
  return names
}
const LEVEL_SUFFIX = /^(.+?)[-_](minimal|low|medium|high|xhigh|max)$/i
// A model the registry does not list still reads as a name (Settings › Models, AEON-1062): "gpt-6.1-sol" →
// "GPT-6.1 Sol", "claude-opus-5-5" → "Claude Opus 5.5". A trailing level becomes the level.
function unlistedName(id: string): { name: string; level: string } {
  const suffix = LEVEL_SUFFIX.exec(id)
  const base = suffix ? suffix[1]! : id
  const gpt = fullName({ id: '', harness: 'codex', family: '', model: base, effort: '', tier: '', enabled: true })
  return { name: gpt !== base ? gpt : lineFallback(base.replace(/(\d)[-_](?=\d)/g, '$1.')), level: suffix ? suffix[2]!.toLowerCase() : '' }
}
// A profile slug (the run's pinned profile) names the model and its level; a model id names the model only.
// A profile's "default" level says nothing, so it is left out.
function nameOf(model: string, harness: string, names: ModelNames | undefined): { name: string; level: string } {
  const pinned = names?.bySlug.get(model)
  if (pinned) return { name: fullName(pinned), level: pinned.effort === 'default' ? '' : pinned.effort }
  const listed = names?.byModel.get(modelKey(harness, model))
  return listed ? { name: fullName(listed), level: '' } : unlistedName(model)
}

// Unknown fields are omitted, never filled with "unknown" words. Line 2 is the account
// ("Claude Max · markus"); a session without one names its harness there.
export function sessionExecution(view: SessionView, names?: ModelNames): SessionExecution {
  const harness = view.session.harness
  if (harness === 'media' || harness === 'terminal') {
    const label = (harness === 'media' ? view.session.generator : view.session.command)?.trim() ?? ''
    return { kind: harness, model: label, name: label, effort: '', modelLine: label, account: '', accountLine: harness === 'media' ? 'Media' : 'Terminal', provider: 'unknown', providerLabel: harness === 'media' ? 'Media' : 'Terminal' }
  }
  const reported = view.session.model?.trim() ?? ''
  const model = reported || view.model?.trim() || ''
  const named = model ? nameOf(model, harness, names) : { name: '', level: '' }
  const effort = model ? (view.session.reasoning_effort?.trim() || named.level) : ''
  const account = view.session.account_label?.trim() || view.account?.trim() || ''
  const provider = modelProvider(model)
  return {
    kind: 'ai',
    model,
    name: named.name,
    effort,
    modelLine: [named.name, effort].filter(Boolean).join(' · '),
    account,
    accountLine: account || view.harness,
    provider,
    providerLabel: PROVIDER_LABEL[provider],
  }
}
