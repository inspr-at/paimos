// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test } from 'vitest'
import { repositoryState, workflowFile } from '../src/lib/projectDeliverySettings'
import type { MetricSource } from '../src/lib/deliveryNumbers'
import { flatten, mountView } from './webcore-view-harness'

test('AEON-1639: repository status icons render only their selected artwork', () => {
  // Risk: a second conditional chain paints fallback artwork over the icon.
  for (const [name, tags] of [['repo', ['path', 'path']], ['circle', ['circle']], ['signal-off', ['path']]] as const) {
    const view = mountView('../src/components/AppIcon.vue', {}, { name })
    try {
      const shapes = flatten(view.root).filter(node => ['path', 'circle', 'rect'].includes(node.tag))
      expect(shapes.map(node => node.tag), name).toEqual(tags)
      if (name === 'circle') expect(shapes[0]!.props.r).toBe('6')
      else expect(shapes[0]!.props.d, name).toContain(name === 'repo' ? 'M2.8 12.3' : 'M8 12.6')
    } finally { view.app.unmount() }
  }
})

test('AEON-1226: saved repository failures take precedence over history and stale events', () => {
  // Risk: a linked repository with no readable history is presented as live or zero.
  const now = Date.parse('2026-10-12T12:00:00Z')
  const source: MetricSource = { repository: 'inspr-at/aeon', ci_workflow: '.github/workflows/ci.yml', nightly_workflow: '.github/workflows/nightly-full.yml', app_connected: true, backfill: 'done', backfill_since: '2026-09-12T00:00:00Z', backfill_done_at: '2026-10-12T10:00:00Z', covered_since: '2026-09-12T00:00:00Z', last_event_at: '2026-10-12T11:00:00Z' }
  expect(repositoryState(null, now)).toBe('none')
  expect(repositoryState(source, now)).toBe('done')
  expect(repositoryState({ ...source, backfill: 'running' }, now)).toBe('reading')
  expect(repositoryState({ ...source, last_event_at: '2026-10-05T12:00:00Z' }, now)).toBe('done')
  const stale = { ...source, last_event_at: '2026-10-05T11:59:59Z' }
  expect(repositoryState(stale, now)).toBe('stale')
  expect(repositoryState({ ...stale, app_connected: false }, now)).toBe('appoff')
  expect(repositoryState({ ...stale, last_read_error: { reason: 'github_unavailable', permission: null, at: '2026-10-12T11:59:00Z' } }, now)).toBe('unavailable')
  expect(repositoryState({ ...stale, last_read_error: { reason: 'not_installed', permission: null, at: '2026-10-12T11:59:00Z' } }, now)).toBe('appoff')
  expect(repositoryState({ ...stale, app_connected: false, last_read_error: { reason: 'actions_read_missing', permission: 'actions:read', at: '2026-10-12T11:59:00Z' } }, now)).toBe('noactions')
  expect(workflowFile(source.ci_workflow)).toBe('ci.yml')
})
