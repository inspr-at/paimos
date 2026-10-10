// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from '@vue/server-renderer'
import ExtensionData from '../src/components/work/ExtensionData.vue'
import { api } from '../src/lib/api'
import { getIntake } from '../src/lib/releaseData'

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
