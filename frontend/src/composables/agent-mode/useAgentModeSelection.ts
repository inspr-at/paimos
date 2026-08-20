/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as
 * published by the Free Software Foundation, version 3.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public
 * License along with this program. If not, see <https://www.gnu.org/licenses/>.
 */

// PAI-805 — persistent selection composable.
//
// Wraps the pure selection rules with memory (localStorage) and the
// reactive bookkeeping the view needs: exactly one selected id whenever
// deliveries exist, deterministic restore, and announcements when the
// system (not the user) had to move the selection.

import { computed, ref, shallowRef, watch, type Ref } from 'vue'

import type { Delivery } from '@/services/agentMode'
import { resolveSelection, stepSelection, type SelectionOrigin } from './agentModeSelection'

export type SelectionSource = 'user' | 'system'

export interface SelectionChange {
  id: string | null
  previous: string | null
  origin: SelectionOrigin | 'user'
  source: SelectionSource
  at: number
}

export interface UseAgentModeSelectionOptions {
  deliveries: Ref<readonly Delivery[]>
  /** Visual travel order (ids) for arrow-key movement. */
  order: Ref<readonly string[]>
  /** localStorage key (per user). Null disables persistence. */
  storageKey: Ref<string | null>
  /** One-shot preferred id (e.g. `?delivery=` deep link) consulted before memory. */
  preferredId?: Ref<string | null>
  storage?: Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>
  now?: () => number
}

function readRemembered(storage: Pick<Storage, 'getItem'> | null, key: string | null): string | null {
  if (!storage || !key) return null
  try {
    const raw = storage.getItem(key)
    return raw && raw.trim() !== '' ? raw : null
  } catch {
    return null
  }
}

export function useAgentModeSelection(opts: UseAgentModeSelectionOptions) {
  const storage = opts.storage ?? (typeof localStorage !== 'undefined' ? localStorage : null)
  const now = opts.now ?? (() => Date.now())

  const selectedId = ref<string | null>(null)
  const lastChange = shallowRef<SelectionChange | null>(null)
  let preferredConsumed = false

  const selectedDelivery = computed<Delivery | null>(() => {
    const id = selectedId.value
    if (!id) return null
    return opts.deliveries.value.find((d) => d.id === id) ?? null
  })

  function persist(id: string | null) {
    const key = opts.storageKey.value
    if (!storage || !key) return
    try {
      if (id) storage.setItem(key, id)
      else storage.removeItem(key)
    } catch {
      /* storage may be unavailable (private mode) — selection still works in memory */
    }
  }

  function commit(id: string | null, origin: SelectionChange['origin'], source: SelectionSource) {
    const previous = selectedId.value
    if (previous === id && origin !== 'restored' && origin !== 'default') return
    selectedId.value = id
    lastChange.value = { id, previous, origin, source, at: now() }
    persist(id)
  }

  /** Re-resolves against the current deliveries (call after each snapshot). */
  function reconcile() {
    const list = opts.deliveries.value
    let remembered = readRemembered(storage, opts.storageKey.value)
    if (!preferredConsumed && opts.preferredId?.value) {
      // A deep link wins over memory exactly once, and only when authorized.
      if (list.some((d) => d.id === opts.preferredId!.value)) remembered = opts.preferredId.value
      preferredConsumed = list.length > 0
    }
    const choice = resolveSelection(list, selectedId.value, remembered)
    if (choice.origin === 'kept') return
    commit(choice.id, choice.origin, 'system')
  }

  /** User-driven selection. Never silently rejected: unknown ids are ignored. */
  function select(id: string) {
    if (!opts.deliveries.value.some((d) => d.id === id)) return false
    commit(id, 'user', 'user')
    return true
  }

  function step(delta: number) {
    const next = stepSelection(opts.order.value, selectedId.value, delta)
    if (next && next !== selectedId.value) commit(next, 'user', 'user')
    return next
  }

  function selectEdge(edge: 'first' | 'last') {
    const order = opts.order.value
    if (order.length === 0) return null
    const next = edge === 'first' ? order[0] : order[order.length - 1]
    if (next !== selectedId.value) commit(next, 'user', 'user')
    return next
  }

  watch(opts.deliveries, reconcile, { immediate: true })
  watch(opts.storageKey, reconcile)

  const selectedIndex = computed(() => {
    const id = selectedId.value
    return id ? opts.order.value.indexOf(id) : -1
  })

  return {
    selectedId,
    selectedDelivery,
    selectedIndex,
    lastChange,
    select,
    step,
    selectEdge,
    reconcile,
  }
}
