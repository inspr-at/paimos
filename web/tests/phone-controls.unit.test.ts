// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest'
import { createSSRApp } from 'vue'
import { renderToString } from '@vue/server-renderer'
import BulkBar from '../src/components/work/BulkBar.vue'
import SortEditor from '../src/components/work/SortEditor.vue'

const bulk = (extra: Record<string, unknown> = {}) => renderToString(createSSRApp(BulkBar, {
  count: 2, loaded: 2, total: 5, busy: false, canWrite: true, ...extra,
}))

describe('matching selection in the phone dock', () => {
  it('offers unloaded matches only after all loaded tickets are selected', async () => {
    expect(await bulk()).toContain('Select all 5')
    for (const state of [{ count: 1 }, { count: 5, loaded: 5 }, { total: null }]) {
      const html = await bulk(state)
      expect(html).not.toContain('Select all ')
      expect(html).toContain('selection-slot')
      expect(html).toContain('aria-label="Status"')
      expect(html).toContain('aria-label="Clear the selection"')
    }
  })

  it('keeps deletion feedback honest and blocks select-all during a bulk write', async () => {
    const deleted = await bulk({ deleted: 1 })
    expect(deleted).toContain('1 selected ticket was deleted')
    expect(deleted).not.toContain('Select all ')
    expect(deleted).toContain('aria-label="Status"')
    expect(await bulk({ busy: true })).toMatch(/class="link" disabled[^>]*>Select all 5/)
  })
})

it('uses a constant Add label in the sheet while preserving desktop sort wording', async () => {
  for (const sort of [[], [{ field: 'title', desc: false }]] as const) {
    const sheet = await renderToString(createSSRApp(SortEditor, { stable: true, sort: [...sort] }))
    expect(sheet).toContain('Add sort key')
    const desktop = await renderToString(createSSRApp(SortEditor, { sort: [...sort] }))
    expect(desktop).toContain(sort.length ? 'Then by' : 'Sort by')
  }
})
