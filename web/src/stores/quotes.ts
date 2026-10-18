// SPDX-License-Identifier: AGPL-3.0-only
import { defineStore } from 'pinia'
import { ref } from 'vue'
import { blankFilter, fetchQuotes, type QuoteFilter, type QuoteRow } from '../lib/quotes/list'

// Quotes for the list and its docked workspace: loaded once and kept while you
// move between the list, a customer and the full page, so going back finds the
// list as you left it (search, filters, the row you were on, the scroll).
export const useQuotes = defineStore('quotes', () => {
  const items = ref<QuoteRow[] | null>(null)
  const error = ref('')
  const status = ref(0)
  const loading = ref(false)
  const filter = ref<QuoteFilter>(blankFilter())
  const cursor = ref<string | null>(null)
  const scroll = ref(0)
  let request: Promise<void> | null = null
  // reset() retires this generation so a read already in flight cannot refill the list.
  let generation = 0

  function load(force = false): Promise<void> {
    if (request) return request
    if (items.value && !force) return Promise.resolve()
    loading.value = true
    const run = generation
    request = (async () => {
      try {
        const list = await fetchQuotes()
        if (run !== generation) return
        items.value = list; error.value = ''; status.value = 0
      } catch (e) {
        if (run !== generation) return
        error.value = e instanceof Error ? e.message : 'Quotes could not be loaded.'
        status.value = typeof (e as { status?: unknown }).status === 'number' ? (e as { status: number }).status : 0
      } finally { if (run === generation) { loading.value = false; request = null } }
    })()
    return request
  }
  // A change made in a workspace shows in the list at once; the next load confirms it.
  function patch(id: string, change: Partial<QuoteRow>) {
    if (items.value) items.value = items.value.map(row => row.quote_node_id === id ? { ...row, ...change } : row)
  }
  function reset() {
    generation++; request = null; loading.value = false
    items.value = null; error.value = ''; status.value = 0; filter.value = blankFilter(); cursor.value = null; scroll.value = 0
  }
  return { items, error, status, loading, filter, cursor, scroll, load, patch, reset }
})
