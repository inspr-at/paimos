// SPDX-License-Identifier: AGPL-3.0-only
import type { MetadataChange } from '../../lib/agents.ts'

const FIELD_LABEL: Record<MetadataChange['field'], string> = {
  display_label: 'Name',
  model: 'Model',
  reasoning_effort: 'Effort',
}

function isMetadataChange(value: unknown): value is MetadataChange {
  if (!value || typeof value !== 'object') return false
  const entry = value as MetadataChange
  return (entry.field === 'display_label' || entry.field === 'model' || entry.field === 'reasoning_effort')
    && (entry.previous_value === null || typeof entry.previous_value === 'string')
    && (entry.value === null || typeof entry.value === 'string')
    && typeof entry.at === 'string'
}

function stamp(value: string) {
  const parsed = Date.parse(value)
  return Number.isNaN(parsed) ? 0 : parsed
}

// Detail history is newest first. Sorting again keeps a short list stable if a
// payload arrives out of order, and drops entries outside the published fields.
export function metadataChanges(history: unknown, limit = 8): MetadataChange[] {
  if (!Array.isArray(history)) return []
  return history.filter(isMetadataChange).sort((a, b) => stamp(b.at) - stamp(a.at)).slice(0, limit)
}

export function metadataChangeText(entry: MetadataChange): string {
  const label = FIELD_LABEL[entry.field]
  const next = entry.value?.trim() || 'cleared'
  const previous = entry.previous_value?.trim()
  if (!previous) return next === 'cleared' ? `${label} cleared` : `${label} ${next}`
  if (previous === next) return `${label} ${next}`
  return `${label} ${previous} to ${next}`
}

// The session list has no detail fetch. It shows the newest model or effort
// change only when that array is already on the row.
export function latestModelEffortChange(history: unknown): string {
  const entry = metadataChanges(history, 20).find(item => item.field === 'model' || item.field === 'reasoning_effort')
  return entry ? metadataChangeText(entry) : ''
}
