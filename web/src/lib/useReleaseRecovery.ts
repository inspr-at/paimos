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
  const revision = ref(initial.revision), room = ref(0)
  let capacityKnown = false
  function setCapacity(occupied: number | undefined) {
    if (!Number.isInteger(occupied) || occupied! < 0 || occupied! > 1000) throw new Error('Release capacity could not be read. Reopen Freeze before including work.')
    room.value = 1000 - occupied!; capacityKnown = true
  }
  if (initial.occupied_rows !== undefined) setCapacity(initial.occupied_rows)
  const placedCount = ref(0)
  const rows = shallowRef<Record<RecoverySource, PlanningItem[]>>({ unplaced: [], later: [] })
  const counts = ref({ unplaced: 0, later: 0 }), cursors = ref({ unplaced: '', later: '' })
  const ready = ref(false), incomplete = ref(false), busy = ref(false), error = ref(''), result = ref(''), all = ref(true)
  const excluded = ref(new Set<string>()), included = new Set<string>()
  const firstPages: Partial<Record<RecoverySource, ItemPage>> = {}
  const archived = shallowRef<Record<RecoverySource, PlanningItem[]>>({ unplaced: [], later: [] })
  const total = computed(() => counts.value.unplaced + counts.value.later)
  const visible = computed(() => [...rows.value.unplaced, ...rows.value.later])
  const selectedRows = (source: RecoverySource) => [...new Map([...(incomplete.value ? [] : archived.value[source]), ...rows.value[source]].filter(row => !included.has(row.item_id) && !excluded.value.has(row.item_id)).map(row => [row.item_id, row])).values()]
  const selected = computed(() => all.value && !incomplete.value ? Math.max(0, total.value - excluded.value.size) : selectedRows('unplaced').length + selectedRows('later').length)
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
      if (!capacityKnown) throw new Error('Release capacity could not be read. Reopen Freeze before including work.')
      const unplaced = await read('unplaced'), later = await read('later')
      firstPages.unplaced = unplaced; firstPages.later = later; archived.value = { unplaced: [], later: [] }
      rows.value = { unplaced: unplaced.items, later: later.items }; cursors.value = { unplaced: unplaced.next_cursor ?? '', later: later.next_cursor ?? '' }; ready.value = true
    } catch (e) { if (options.current()) error.value = refusal(e) }
    finally { busy.value = false }
  }
  function checked(id: string, on: boolean) { const next = new Set(excluded.value); if (on) next.delete(id); else next.add(id); excluded.value = next }
  function toggleAll(on: boolean) { all.value = on; archived.value = { unplaced: [], later: [] }; excluded.value = on ? new Set() : new Set(visible.value.map(row => row.item_id)) }
  // Replace a displayed page; never accumulate more than 200 rendered rows.
  async function more(source: RecoverySource) {
    if (busy.value || !cursors.value[source]) return
    busy.value = true; error.value = ''
    try {
      const page = await read(source, cursors.value[source])
      if (!all.value && !incomplete.value) {
        const retained = selectedRows(source)
        if (retained.length + selectedRows(source === 'unplaced' ? 'later' : 'unplaced').length > 5000) throw new Error('Selection is limited to 5,000 rows.')
        archived.value = { ...archived.value, [source]: retained }
      }
      rows.value = { ...rows.value, [source]: page.items.filter(row => !included.has(row.item_id)) }; cursors.value[source] = page.next_cursor ?? ''
      if (!all.value) for (const row of page.items) checked(row.item_id, false)
    }
    catch (e) { if (options.current()) error.value = refusal(e) }
    finally { busy.value = false }
  }
  async function include() {
    if (busy.value || !ready.value || !selected.value || !options.allowed()) return
    busy.value = true; error.value = ''; result.value = ''
    const requested = selected.value
    let placed = 0, visited = 0, outcomeUnknown = false
    const seen = new Set<string>(), identity = options.actions?.identity()
    try {
      checkCurrent()
      for (const source of ['unplaced', 'later'] as const) {
        const whole = all.value && !incomplete.value
        const local = whole ? [] : selectedRows(source)
        let items = whole ? firstPages[source]?.items ?? [] : local.splice(0,100), cursor = whole ? firstPages[source]?.next_cursor ?? '' : ''
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
              outcomeUnknown = true
              receipt = await request(`/projects/${encodeURIComponent(initial.project_id)}/ships-in/batch`, { method: 'POST', signal: options.signal, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ items: chosen.map(row => ({ id: row.item_id, expected_revision: row.revision })), release_id: initial.release_id, expected_release_revision: revision.value, position: 'append' }) }) as PlacementReceipt
              checkCurrent()
              if (identity && identity !== options.actions?.identity()) throw new Error('The record on screen changed; the result was discarded.')
              if (receipt.items?.length !== chosen.length || new Set(receipt.items.map(row => row.item_id)).size !== chosen.length || receipt.items.some(row => row.project_id !== initial.project_id || row.release_id !== initial.release_id || !chosen.some(item => item.item_id === row.item_id)) || !Number.isSafeInteger(receipt.release_revision) || receipt.release_revision! <= revision.value) throw new Error('Invalid Include receipt. Reload before continuing.')
              outcomeUnknown = false
              // Every page has its own committed event; Include never offers a
              // blanket Undo across pages. Preserve exact own SSE correlation.
              if (captured && options.actions && !options.actions.commit(captured, { kind: 'placement', result: { ...receipt, release_revisions: { ...receipt.release_revisions, [initial.release_id]: receipt.release_revision! } }, undoable: false, recovered: chosen })) throw new Error('The Include result belongs to a previous view.')
            } catch (e) { if (e instanceof APIError && e.status >= 400 && e.status < 500) outcomeUnknown = false; if (captured) options.actions?.failed(captured); throw e }
            revision.value = receipt.release_revision!; room.value -= chosen.length; placed += chosen.length; placedCount.value += chosen.length
            counts.value[source] = Math.max(0, counts.value[source] - chosen.length)
            for (const row of chosen) { included.add(row.item_id); checked(row.item_id, true) }
            rows.value = { ...rows.value, [source]: rows.value[source].filter(row => !included.has(row.item_id)) }
          }
          if (!room.value && placed < requested) throw new Error('This release is full (1,000 items).')
          if (!whole) { if (!local.length) break; items = local.splice(0,100); continue }
          if (!all.value || incomplete.value || !cursor) break
          if (visitedCursors.has(cursor)) throw new Error('The recovery cursor repeated; remaining work stays listed.')
          visitedCursors.add(cursor)
          const page = await read(source, cursor)
          // The read count excludes earlier committed pages; retain that count.
          items = page.items; cursor = page.next_cursor ?? ''; cursors.value[source] = cursor
        }
      }
    } catch (e) { if (options.current()) { error.value = refusal(e); if (outcomeUnknown || e instanceof APIError && [403,409].includes(e.status)) ready.value = false } }
    finally {
      if (options.current()) result.value = outcomeUnknown ? `${placed} confirmed placed · last batch outcome and remaining count unknown. Include stopped; reopen Freeze before continuing.` : `${placed} placed · ${incomplete.value ? 'at least ' : ''}${total.value} remain listed${placed < requested ? ' · Include stopped' : ''}.`
      busy.value = false
    }
  }
  return { revision, room, setCapacity, placedCount, rows, counts, cursors, ready, incomplete, busy, error, result, all, total, visible, selected, excluded, load, more, include, checked, toggleAll }
}
