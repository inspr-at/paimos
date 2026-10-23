// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createSSRApp, h, reactive, ref } from 'vue'
import { renderToString } from '@vue/server-renderer'
import ExtensionData from '../src/components/work/ExtensionData.vue'
import { api, APIError } from '../src/lib/api'
import { getIntake } from '../src/lib/releaseData'
import { deferred, flush, setupSource } from './record-source'

vi.mock('../src/lib/api', async importOriginal => ({ ...await importOriginal<typeof import('../src/lib/api')>(), api: vi.fn() }))
afterEach(() => vi.resetAllMocks())

describe('Aithema extension disclosures', () => {
  it('retains unknown namespaces and coexisting majors as plain text', async () => {
    const evidence = '  e\u0301\r\n<script>verbatim</script>  '
    const html = await renderToString(createSSRApp({ render: () => h(ExtensionData, { extensions: {
      'x-unregistered.constraints@1': { version: '1.2', data: { evidence, score: 0 } },
      'x-unregistered.constraints@2': { version: '2.0', data: null },
    } }) }))
    expect(html).toContain('Extension data')
    expect(html).toContain('x-unregistered.constraints · version 1.2')
    expect(html).toContain('x-unregistered.constraints · version 2.0')
    expect(html).toContain('&lt;script&gt;verbatim&lt;/script&gt;')
    expect(html).not.toContain('<script>')
    expect(html).toContain('e\u0301\\r\\n')
    expect(html).toContain('null</pre>')
  })
  it('adds no UI for legacy drafts and empty maps', async () => {
    for (const extensions of [undefined, {}]) {
      const html = await renderToString(createSSRApp({ render: () => h(ExtensionData, { extensions }) }))
      expect(html).not.toContain('<section')
    }
  })
})

function ticketHistory() {
  const props = reactive({ projectId: 'project-a', nodeId: 'node-a' }), allowed = ref(true)
  const component = setupSource('components/work/TicketExtensions.vue', props, {
    '../../lib/api': { APIError }, '../../lib/authz': { can: () => allowed.value }, '../../lib/releaseData': { getIntake },
  })
  return { ...component, props, allowed }
}
const response = (drafts: object[], cursor?: string) => new Response(JSON.stringify({ sources: [], turns: [], drafts }), {
  headers: cursor ? { 'X-Next-Cursor': cursor } : {},
})
const draft = (id: string) => ({ id, extensions: { 'x-p03.evidence@1': { version: '1.0', data: { id } } } })

it('P03: held intake pages and errors cannot populate a different ticket, project or revoked grant', async () => {
  for (const phase of ['first', 'older']) for (const change of ['ticket', 'project', 'permission', 'unmount']) for (const fails of [false, true]) {
    vi.mocked(api).mockReset()
    const held = deferred<Response>()
    if (phase === 'first') vi.mocked(api).mockReturnValueOnce(held.promise)
    else vi.mocked(api).mockResolvedValueOnce(response([draft('first')], 'older'))
    const h = ticketHistory()
    try {
      await flush()
      let loading: Promise<void> | undefined
      if (phase === 'older') {
        expect(h.state.drafts.value.map((row: { id: string }) => row.id)).toEqual(['first'])
        vi.mocked(api).mockReturnValueOnce(held.promise)
        loading = h.state.loadOlder()
        expect(h.state.loading.value).toBe(true)
      }
      const calls = vi.mocked(api).mock.calls.length
      vi.mocked(api).mockResolvedValueOnce(response([]))
      if (change === 'ticket') h.props.nodeId = 'node-b'
      if (change === 'project') h.props.projectId = 'project-b'
      if (change === 'permission') h.allowed.value = false
      if (change === 'unmount') h.stop()
      await flush()
      expect(api).toHaveBeenCalledTimes(calls + (change === 'ticket' || change === 'project' ? 1 : 0))
      const before = { drafts: [...h.state.drafts.value], error: h.state.error.value, next: h.state.nextCursor.value, loading: h.state.loading.value }
      if (fails) held.reject(new APIError(503, 'late A error'))
      else held.resolve(response([draft('late-a')], 'late-a-cursor'))
      await loading; await flush()
      expect({ drafts: [...h.state.drafts.value], error: h.state.error.value, next: h.state.nextCursor.value, loading: h.state.loading.value }, `${phase}/${change}/${fails}`).toEqual(before)
    } finally { h.stop() }
  }
})

it('P03: intake continuation retains evidence, deduplicates drafts and retries the same bounded cursor', async () => {
  vi.mocked(api).mockResolvedValueOnce(response(Array.from({ length: 200 }, (_, i) => draft(`draft-${i}`)), 'opaque+/cursor'))
  const h = ticketHistory()
  try {
    await flush()
    expect(h.state.drafts.value).toHaveLength(200)
    vi.mocked(api).mockRejectedValueOnce(new APIError(503, 'temporarily unavailable'))
    await h.state.loadOlder()
    expect(h.state.error.value).toBe(true)
    expect(h.state.drafts.value).toHaveLength(200)
    expect(h.state.nextCursor.value).toBe('opaque+/cursor')
    vi.mocked(api).mockResolvedValueOnce(response([draft('draft-199'), { id: 'no-extensions' }, draft('older-evidence')]))
    await h.state.loadOlder()
    expect(h.state.error.value).toBe(false)
    expect(h.state.loading.value).toBe(false)
    expect(h.state.drafts.value).toHaveLength(201)
    expect(h.state.drafts.value.at(-1).id).toBe('older-evidence')
    expect(h.state.nextCursor.value).toBeNull()
    const queries = vi.mocked(api).mock.calls.slice(-2).map(([url]) => new URL(url, 'http://localhost').searchParams)
    expect(queries.map(q => [q.get('node_id'), q.get('after')])).toEqual([['node-a', 'opaque+/cursor'], ['node-a', 'opaque+/cursor']])
    const calls = vi.mocked(api).mock.calls.length
    await h.state.loadOlder()
    expect(api).toHaveBeenCalledTimes(calls)
  } finally { h.stop() }
})

it('P03: denied intake reads stay absent; unavailable history exposes Retry without invented evidence', async () => {
  for (const status of [403, 404, 503]) {
    vi.mocked(api).mockReset()
    vi.mocked(api).mockRejectedValueOnce(new APIError(status, 'unavailable'))
    const h = ticketHistory()
    try {
      await flush()
      expect(h.state.drafts.value).toEqual([])
      expect(h.state.error.value).toBe(status === 503)
      vi.mocked(api).mockResolvedValueOnce(response([draft('recovered')]))
      await h.state.load()
      expect(h.state.drafts.value.map((row: { id: string }) => row.id)).toEqual(['recovered'])
      expect(h.state.error.value).toBe(false)
      h.allowed.value = false; await flush()
      expect(h.state.drafts.value).toEqual([])
      const calls = vi.mocked(api).mock.calls.length
      await h.state.load()
      expect(api).toHaveBeenCalledTimes(calls)
    } finally { h.stop() }
  }
})

describe('intake history continuation', () => {
  it('keeps older drafts accessible and preserves the node filter on the next page', async () => {
    const first = Array.from({ length: 200 }, (_, i) => ({ id: `draft-${i}` }))
    vi.mocked(api).mockResolvedValueOnce(new Response(JSON.stringify({ sources: [], turns: [], drafts: first }), { headers: { 'X-Next-Cursor': 'opaque+/cursor' } }))
    vi.mocked(api).mockResolvedValueOnce(new Response(JSON.stringify({ sources: [], turns: [], drafts: [{ id: 'draft-200' }] })))
    const page = await getIntake('project', 'node')
    expect(page.drafts).toHaveLength(200)
    expect(page.nextCursor).toBe('opaque+/cursor')
    const older = await getIntake('project', 'node', page.nextCursor!)
    expect(older.drafts.map(d => d.id)).toEqual(['draft-200'])
    expect(older.nextCursor).toBeNull()
    const query = new URL(vi.mocked(api).mock.calls[1][0], 'http://localhost').searchParams
    expect(query.get('node_id')).toBe('node')
    expect(query.get('after')).toBe('opaque+/cursor')
  })

  it('reports a continuation failure instead of returning an empty successful page', async () => {
    vi.mocked(api).mockResolvedValue(new Response(JSON.stringify({ error: 'Intake unavailable', code: 'unavailable' }), { status: 503 }))
    await expect(getIntake('project', 'node', 'cursor')).rejects.toMatchObject({ status: 503, message: 'Intake unavailable' })
  })
})
