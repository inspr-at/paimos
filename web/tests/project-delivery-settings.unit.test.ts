// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, vi } from 'vitest'
import { readProjectDeliverySettings, repositoryState, workflowFile } from '../src/lib/projectDeliverySettings'
import { historyReadReason, type MetricSource } from '../src/lib/deliveryNumbers'
import { deliveryText } from '../src/lib/deliveryNumbersText'
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


test('AEON-1710: history copy names each known reason and its rate reset in English and German', () => {
  // Risk: a precise API diagnosis still becomes a bare unavailable notice.
  const reset = new Date(2026, 9, 24, 20, 14).toISOString()
  const source: MetricSource = { repository: 'example/delivery', ci_workflow: '.github/workflows/ci.yml', nightly_workflow: '.github/workflows/nightly.yml', app_connected: true, backfill: 'done', backfill_since: null, backfill_done_at: null, covered_since: null }
  const cases = [
    ['rate_limited', '20:14', '20:14'], ['timed_out', 'in time', 'rechtzeitig'],
    ['github_error', 'HTTP 503', 'HTTP 503'], ['unreachable', 'network connection', 'Netzwerkverbindung'],
    ['actions_read_missing', 'Actions read access', 'Actions-Lesezugriff'], ['not_installed', 'not connected', 'nicht mit'],
  ] as const
  for (const [reason, en, de] of cases) {
    const failed: MetricSource = { ...source, last_read_error: { reason, at: reset, permission: null, status_code: 503, reset_at: reset } }
    expect(historyReadReason(failed, 'en')).toContain(en)
    expect(historyReadReason(failed, 'de')).toContain(de)
    expect(historyReadReason(failed, 'en')).not.toContain('unavailable')
    expect(repositoryState(failed, Date.parse(reset))).toBe(reason === 'actions_read_missing' ? 'noactions' : reason === 'not_installed' ? 'appoff' : 'unavailable')
  }
  const limited: MetricSource = { ...source, last_read_error: { reason: 'rate_limited', at: reset, permission: null }, retry_at: reset }
  expect(historyReadReason(limited, 'en')).toContain('20:14')
  expect(historyReadReason({ ...limited, retry_at: null }, 'en')).toContain('rate-limited')
  expect(historyReadReason({ ...limited, retry_at: null }, 'de')).toContain('begrenzt')
  expect(deliveryText('en').historyRead.kept).toBe('The last stored history is kept.')
  expect(deliveryText('de').historyRead.kept).toBe('Die zuletzt gespeicherte Historie bleibt erhalten.')
})

test('AEON-1710: a settings revision race is reread once without losing sparse overrides', async () => {
  // Risk: two individually valid GETs straddle a user edit and show a false
  // loading failure, or combine an effective value and another revision's draft.
  const replies = [
    { revision: '1', body: { required_checks: ['inherited'], deadlines: { reviewed: 30 } } },
    { revision: '2', body: { deadlines: { reviewed: 45 } } },
    { revision: '2', body: { required_checks: ['inherited'], deadlines: { reviewed: 45 } } },
    { revision: '2', body: { deadlines: { reviewed: 45 } } },
  ]
  const calls: { url: string; signal?: AbortSignal | null }[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
    calls.push({ url, signal: init.signal })
    const answer = replies[calls.length - 1]!
    return new Response(JSON.stringify(answer.body), { headers: { ETag: `"${answer.revision}"` } })
  }))
  try {
    const controller = new AbortController()
    const result = await readProjectDeliverySettings('project one', controller.signal)
    expect(calls).toHaveLength(4)
    expect(calls.every(call => call.url.startsWith('/api/projects/project%20one/delivery-settings') && call.signal && !call.signal.aborted)).toBe(true)
    expect(result).toEqual({ revision: '"2"', effective: replies[2]!.body, overrides: replies[3]!.body })
    expect(result.overrides.required_checks).toBeUndefined()
  } finally { vi.unstubAllGlobals() }
})

test('AEON-1710: continued edits stay bounded and a missing revision is not called a concurrent edit', async () => {
  // Risk: a moving editor loops indefinitely, or absent metadata is presented
  // as somebody changing settings. Neither may return a mixed snapshot.
  for (const missing of [false, true]) {
    let calls = 0
    vi.stubGlobal('fetch', vi.fn(async () => {
      calls++
      return new Response('{}', { headers: missing ? {} : { ETag: `"${calls}"` } })
    }))
    try {
      await expect(readProjectDeliverySettings('project')).rejects.toThrow(missing ? 'revision could not be read' : 'changed while loading')
      expect(calls).toBe(4)
    } finally { vi.unstubAllGlobals() }
  }
  const controller = new AbortController()
  let calls = 0
  vi.stubGlobal('fetch', vi.fn(async () => {
    calls++
    if (calls === 2) controller.abort()
    return new Response('{}', { headers: { ETag: `"${calls}"` } })
  }))
  try {
    await expect(readProjectDeliverySettings('old-project', controller.signal)).rejects.toThrow()
    expect(calls).toBe(2)
  } finally { vi.unstubAllGlobals() }
})
