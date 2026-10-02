// SPDX-License-Identifier: AGPL-3.0-only
import { beforeEach, expect, it, vi } from 'vitest'
import { api } from '../src/lib/api'
import { clearPermissions, revokePermissions } from '../src/lib/authz'
import { canFeature, clearFeatures, refreshFeatures, setFeatureOverride, type FeatureKey } from '../src/lib/features'

vi.mock('../src/lib/api', async original => ({ ...await original<typeof import('../src/lib/api')>(), api: vi.fn() }))
const response = (enabled: unknown, project: string | null = null) => new Response(JSON.stringify({ project_id: project, items: [{ key: 'workspace-summary', label: 'Workspace summary', description: '', enabled, source: 'tenant' }] }))
beforeEach(() => {
  vi.resetAllMocks(); clearPermissions(); clearFeatures()
  vi.mocked(api).mockResolvedValue(response(false))
})

it('starts dark, keeps project scopes separate and never enables unknown keys', async () => {
  expect(canFeature('workspace-summary')).toBe(false)
  await refreshFeatures()
  expect(canFeature('workspace-summary')).toBe(false)
  vi.mocked(api).mockImplementation(async path => response(path.includes('project_id=one'), path.includes('project_id=') ? path.split('project_id=')[1]! : null))
  await refreshFeatures('one')
  await refreshFeatures('two')
  expect(canFeature('workspace-summary', 'one')).toBe(true)
  expect(canFeature('workspace-summary', 'two')).toBe(false)
  expect(canFeature('unshipped' as FeatureKey, 'one')).toBe(false)
})

it('releases and disables on the same page after a revisioned write', async () => {
  let enabled = false
  vi.mocked(api).mockImplementation(async (path, init) => {
    if (init?.method === 'PUT') {
      enabled = JSON.parse(String(init.body)).enabled
      return new Response(JSON.stringify({ feature: { key: 'workspace-summary', override: enabled, revision: 1 }, event_id: 3 }))
    }
    return response(enabled, path.includes('project_id=one') ? 'one' : null)
  })
  await refreshFeatures(); await refreshFeatures('one')
  await setFeatureOverride('workspace-summary', true, 0)
  expect(canFeature('workspace-summary')).toBe(true)
  expect(canFeature('workspace-summary', 'one')).toBe(true)
  expect(vi.mocked(api).mock.calls.find(([, init]) => init?.method === 'PUT')?.[1]?.body).toBe('{"enabled":true,"expected_revision":0}')
  await setFeatureOverride('workspace-summary', false, 1)
  expect(canFeature('workspace-summary')).toBe(false)
  expect(canFeature('workspace-summary', 'one')).toBe(false)
})

it('drops a slower older answer after a newer evaluation', async () => {
  let finish!: (answer: Response) => void
  vi.mocked(api).mockImplementationOnce(() => new Promise(resolve => { finish = resolve })).mockResolvedValueOnce(response(false))
  const old = refreshFeatures()
  await refreshFeatures(undefined, true)
  finish(response(true)); await old
  expect(canFeature('workspace-summary')).toBe(false)
})

it('discards old tenant and revoked-session responses', async () => {
  let finish!: (answer: Response) => void
  vi.mocked(api).mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
  const old = refreshFeatures()
  clearPermissions()
  finish(response(true)); await old
  expect(canFeature('workspace-summary')).toBe(false)
  await refreshFeatures()
  vi.mocked(api).mockResolvedValueOnce(response(true))
  await refreshFeatures(undefined, true)
  expect(canFeature('workspace-summary')).toBe(true)
  revokePermissions()
  expect(canFeature('workspace-summary')).toBe(false)
  const calls = vi.mocked(api).mock.calls.length
  await refreshFeatures()
  expect(vi.mocked(api).mock.calls).toHaveLength(calls)
})

it('fails closed on failed, malformed and wrong-scope evaluations', async () => {
  for (const answer of [response('true'), response(true, 'other-project'), new Response(null, { status: 503 }), new Response('{not json')]) {
    vi.mocked(api).mockResolvedValueOnce(answer)
    await refreshFeatures(undefined, true)
    expect(canFeature('workspace-summary')).toBe(false)
  }
  vi.mocked(api).mockRejectedValueOnce(new TypeError('offline'))
  await refreshFeatures(undefined, true)
  expect(canFeature('workspace-summary')).toBe(false)
})
