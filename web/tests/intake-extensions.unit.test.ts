// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from '@vue/server-renderer'
import ExtensionData from '../src/components/journey/ExtensionData.vue'

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
