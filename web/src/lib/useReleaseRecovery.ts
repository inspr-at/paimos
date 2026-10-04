// SPDX-License-Identifier: AGPL-3.0-only
import { computed, ref, shallowRef } from 'vue'
import { APIError } from './api'
import type { DeliveryActions, PlacementReceipt } from './deliveryChanges'
import type { ItemPage, PlanningItem } from './deliveryPlanning'
import { refusal } from './deliveryMoves'
import { releasePath, requestRelease, type ReleaseRecord, type ReleaseRequest } from './releaseActions'

type RecoverySource = 'unplaced' | 'later'
export function useReleaseRecovery(options: { release: ReleaseRecord; current: () => boolean; allowed: () => boolean; actions?: DeliveryActions; request?: ReleaseRequest; signal: AbortSignal }) {
  const request = options.request ?? requestRelease, initial = { ...options.release }
  const revision = ref(initial.revision), room = ref(Math.max(0, 1000 - initial.rollup.units))
  const rows = shallowRef<Record<RecoverySource, PlanningItem[]>>({ unplaced: [], later: [] })
  const counts = ref({ unplaced: 0, later: 0 }), cursors = ref({ unplaced: '', later: '' })
  const ready = ref(false), incomplete = ref(false), busy = ref(false), error = ref(''), result = ref(''), all = ref(true)
  const excluded = ref(new Set<string>()), included = new Set<string>()
  const total = computed(() => counts.value.unplaced + counts.value.later)
  const visible = computed(() => [...rows.value.unplaced, ...rows.value.later])
  const selected = computed(() => all.value && !incomplete.value ? Math.max(0, total.value - excluded.value.size) : visible.value.filter(row => !excluded.value.has(row.item_id)).length)
  function checkCurrent() { if (!options.current() || options.signal.aborted) throw new Error('The project, person or release changed. Reopen Freeze.') }
  const pagePath = (source: RecoverySource, cursor: string) => {
    if (cursor.length > 2048) throw new Error('Invalid recovery cursor.')
    const query = new URLSearchParams({ limit: '100', [source === 'unplaced' ? 'completed_unplaced' : 'completed_later']: '1' })
    if (cursor) query.set('cursor', cursor)
    return source === 'unplaced' ? `/projects/${encodeURIComponent(initial.project_id)}/backlog?${query}` : `${releasePath(initial.project_id, initial.release_id)}/items?${query}`
  }
  async function read(source: RecoverySource, cursor = '') {
    checkCurrent()
    const page = await request(pagePath(source, cursor), { signal: options.signal }) as ItemPage
    checkCurrent()
    if (!Array.isArray(page.items) || page.items.length > 100 || !Number.isInteger(page.count) || page.count < 0 || page.count > 5000 || typeof page.incomplete !== 'boolean' || (page.next_cursor && (page.next_cursor.length > 2048 || page.next_cursor === cursor))) throw new Error('Invalid recovery page.')
    if (page.items.some(row => row.project_id !== initial.project_id || !row.item_id || !Number.isInteger(row.revision) || row.revision < 0 || !['ticket', 'task'].includes(row.kind) || !['done', 'accepted', 'delivered'].includes(row.state))) throw new Error('Recovery work changed identity or state.')
    incomplete.value ||= page.incomplete || counts.value[source === 'unplaced' ? 'later' : 'unplaced'] + page.count > 5000
    if (incomplete.value) all.value = false
    counts.value[source] = Math.min(5000, page.count)
    return page
  }
  async function load() {
    if (busy.value) return
    busy.value = true; ready.value = false; error.value = ''
    try {
      const unplaced = await read('unplaced'), later = await read('later')
      rows.value = { unplaced: unplaced.items, later: later.items }; cursors.value = { unplaced: unplaced.next_cursor ?? '', later: later.next_cursor ?? '' }; ready.value = true
    } catch (e) { if (options.current()) error.value = refusal(e) }
    finally { busy.value = false }
  }
  function checked(id: string, on: boolean) { const next = new Set(excluded.value); if (on) next.delete(id); else next.add(id); excluded.value = next }
  function toggleAll(on: boolean) { all.value = on; excluded.value = on ? new Set() : new Set(visible.value.map(row => row.item_id)) }
  // Replace a displayed page; never accumulate more than 200 rendered rows.
  async function more(source: RecoverySource) {
    if (busy.value || !cursors.value[source]) return
    busy.value = true; error.value = ''
    try { const page = await read(source, cursors.value[source]); rows.value = { ...rows.value, [source]: page.items.filter(row => !included.has(row.item_id)) }; cursors.value[source] = page.next_cursor ?? ''; if (!all.value) for (const row of page.items) checked(row.item_id, false) }
    catch (e) { if (options.current()) error.value = refusal(e) }
    finally { busy.value = false }
  }
  async function include() {
    if (busy.value || !ready.value || !selected.value || !options.allowed()) return
    busy.value = true; error.value = ''; result.value = ''
    const requested = selected.value
    let placed = 0, visited = 0
    const seen = new Set<string>(), identity = options.actions?.identity()
    try {
      checkCurrent()
      for (const source of ['unplaced', 'later'] as const) {
        let items = rows.value[source], cursor = cursors.value[source]
        const visitedCursors = new Set<string>()
        for (;;) {
          checkCurrent()
          const chosen = items.filter(row => !excluded.value.has(row.item_id) && !included.has(row.item_id) && !seen.has(row.item_id)).slice(0, room.value)
          for (const row of items) seen.add(row.item_id)
          visited += items.length
          if (visited > 5000) throw new Error('Recovery reached the 5,000-row bound; remaining work stays listed.')
          if (chosen.length) {
            if (!options.allowed() || identity && identity !== options.actions?.identity()) throw new Error('Release edit rights or the record on screen changed.')
            const captured = options.actions?.begin()
            let receipt: PlacementReceipt
            try {
              receipt = await request(`/projects/${encodeURIComponent(initial.project_id)}/ships-in/batch`, { method: 'POST', signal: options.signal, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ items: chosen.map(row => ({ id: row.item_id, expected_revision: row.revision })), release_id: initial.release_id, expected_release_revision: revision.value, position: 'append' }) }) as PlacementReceipt
              checkCurrent()
              if (identity && identity !== options.actions?.identity()) throw new Error('The record on screen changed; the result was discarded.')
              if (receipt.items?.length !== chosen.length || receipt.items.some(row => row.project_id !== initial.project_id || row.release_id !== initial.release_id || !chosen.some(item => item.item_id === row.item_id)) || !Number.isSafeInteger(receipt.release_revision) || receipt.release_revision! <= revision.value) throw new Error('Invalid Include receipt. Reload before continuing.')
              // Every page has its own committed event; Include never offers a
              // blanket Undo across pages. Preserve exact own SSE correlation.
              if (captured && options.actions && !options.actions.commit(captured, { kind: 'placement', result: receipt, undoable: false })) throw new Error('The Include result belongs to a previous view.')
            } catch (e) { if (captured) options.actions?.failed(captured); throw e }
            revision.value = receipt.release_revision!; room.value -= chosen.length; placed += chosen.length
            counts.value[source] = Math.max(0, counts.value[source] - chosen.length)
            for (const row of chosen) { included.add(row.item_id); checked(row.item_id, true) }
            rows.value = { ...rows.value, [source]: rows.value[source].filter(row => !included.has(row.item_id)) }
          }
          if (!room.value && placed < requested) throw new Error('This release is full (1,000 items).')
          if (!all.value || incomplete.value || !cursor) break
          if (visitedCursors.has(cursor)) throw new Error('The recovery cursor repeated; remaining work stays listed.')
          visitedCursors.add(cursor)
          const page = await read(source, cursor)
          // The read count excludes earlier committed pages; retain that count.
          items = page.items; cursor = page.next_cursor ?? ''; cursors.value[source] = cursor
        }
      }
    } catch (e) { if (options.current()) { error.value = refusal(e); if (e instanceof APIError && [403,409].includes(e.status)) ready.value = false } }
    finally {
      if (options.current()) result.value = `${placed} placed · ${incomplete.value ? 'at least ' : ''}${total.value} remain listed${placed < requested ? ' · Include stopped' : ''}.`
      busy.value = false
    }
  }
  return { revision, room, rows, counts, cursors, ready, incomplete, busy, error, result, all, total, visible, selected, excluded, load, more, include, checked, toggleAll }
}
