// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { latestModelEffortChange, metadataChangeText, metadataChanges } from '../src/components/agents/metadataHistory.ts'
import type { MetadataChange } from '../src/lib/agents.ts'

const at = (minutes: number) => new Date(Date.parse('2026-09-27T10:00:00Z') + minutes * 60_000).toISOString()
const change = (field: MetadataChange['field'], previous: string | null, value: string | null, minutes: number): MetadataChange =>
  ({ field, previous_value: previous, value, at: at(minutes) })

test('short history is newest first and drops entries outside the published fields', () => {
  const history = [
    change('model', 'gpt-6-luna', 'gpt-6-sol', 0),
    { field: 'prompt', previous_value: 'hidden', value: 'hidden', at: at(5) },
    change('reasoning_effort', 'medium', 'xhigh', 3),
    null,
    change('display_label', 'hausv', null, 4),
  ]
  const shown = metadataChanges(history, 2)
  assert.deepEqual(shown.map(entry => entry.field), ['display_label', 'reasoning_effort'])
  assert.equal(metadataChangeText(shown[0]!), 'Name hausv to cleared')
  assert.equal(metadataChangeText(change('model', null, 'gpt-6-sol', 0)), 'Model gpt-6-sol')
  assert.equal(metadataChangeText(change('reasoning_effort', 'xhigh', 'xhigh', 0)), 'Effort xhigh')
  assert.equal(latestModelEffortChange(history), 'Effort medium to xhigh')
  assert.equal(latestModelEffortChange(undefined), '')
  assert.deepEqual(metadataChanges({}), [])
})
