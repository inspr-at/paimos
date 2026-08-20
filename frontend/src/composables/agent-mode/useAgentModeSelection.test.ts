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

import { describe, expect, it } from 'vitest'
import { nextTick, ref } from 'vue'

import { makeFixtureSnapshot } from '@/services/agentModeFixtures'
import { normalizeWireSnapshot } from '@/services/agentModeTransport'
import type { Delivery } from '@/services/agentMode'
import { useAgentModeSelection } from './useAgentModeSelection'

const ten = normalizeWireSnapshot(makeFixtureSnapshot(10), 0).deliveries

function memoryStorage() {
  const m = new Map<string, string>()
  return {
    getItem: (k: string) => m.get(k) ?? null,
    setItem: (k: string, v: string) => void m.set(k, v),
    removeItem: (k: string) => void m.delete(k),
    dump: () => Object.fromEntries(m),
  }
}

describe('useAgentModeSelection (PAI-805)', () => {
  it('selects exactly one delivery by default and persists it', async () => {
    const storage = memoryStorage()
    const deliveries = ref<readonly Delivery[]>(ten)
    const order = ref(ten.map((d) => d.id))
    const sel = useAgentModeSelection({ deliveries, order, storageKey: ref('k'), storage })
    expect(sel.selectedId.value).not.toBeNull()
    expect(sel.lastChange.value?.origin).toBe('default')
    expect(storage.dump().k).toBe(sel.selectedId.value)
    await nextTick()
    expect(sel.selectedDelivery.value?.id).toBe(sel.selectedId.value)
  })

  it('restores the remembered selection when authorized, otherwise defaults', () => {
    const storage = memoryStorage()
    storage.setItem('k', ten[7].id)
    const sel = useAgentModeSelection({ deliveries: ref(ten), order: ref(ten.map((d) => d.id)), storageKey: ref('k'), storage })
    expect(sel.selectedId.value).toBe(ten[7].id)
    expect(sel.lastChange.value?.origin).toBe('restored')

    const storage2 = memoryStorage()
    storage2.setItem('k', 'someone-elses-delivery')
    const sel2 = useAgentModeSelection({ deliveries: ref(ten), order: ref(ten.map((d) => d.id)), storageKey: ref('k'), storage: storage2 })
    expect(sel2.selectedId.value).not.toBe('someone-elses-delivery')
    expect(sel2.lastChange.value?.origin).toBe('default')
  })

  it('prefers a deep-linked id once, only when present', () => {
    const storage = memoryStorage()
    storage.setItem('k', ten[1].id)
    const sel = useAgentModeSelection({ deliveries: ref(ten), order: ref(ten.map((d) => d.id)), storageKey: ref('k'), storage, preferredId: ref(ten[4].id) })
    expect(sel.selectedId.value).toBe(ten[4].id)
    const sel2 = useAgentModeSelection({ deliveries: ref(ten), order: ref(ten.map((d) => d.id)), storageKey: ref('k'), storage: memoryStorage(), preferredId: ref('nope') })
    expect(sel2.selectedId.value).not.toBe('nope')
    expect(sel2.selectedId.value).not.toBeNull()
  })

  it('keeps the user selection across snapshots and re-picks deterministically when it disappears', async () => {
    const deliveries = ref<readonly Delivery[]>(ten)
    const sel = useAgentModeSelection({ deliveries, order: ref(ten.map((d) => d.id)), storageKey: ref('k'), storage: memoryStorage() })
    const pick = ten.find((d) => d.id !== sel.selectedId.value)!
    expect(sel.select(pick.id)).toBe(true)
    expect(sel.selectedId.value).toBe(pick.id)
    expect(sel.lastChange.value?.source).toBe('user')

    deliveries.value = [...ten].reverse()
    await nextTick()
    expect(sel.selectedId.value).toBe(pick.id)

    deliveries.value = ten.filter((d) => d.id !== pick.id)
    await nextTick()
    expect(sel.selectedId.value).not.toBe(pick.id)
    expect(sel.selectedId.value).not.toBeNull()
    expect(sel.lastChange.value?.origin).toBe('default')
    expect(sel.lastChange.value?.previous).toBe(pick.id)

    deliveries.value = []
    await nextTick()
    expect(sel.selectedId.value).toBeNull()
  })

  it('adopts the server fallback only while selection is absent and never lets refresh steal an extant selection', async () => {
    const deliveries = ref<readonly Delivery[]>(ten)
    const fallbackId = ref<string | null>(ten[2].id)
    const sel = useAgentModeSelection({
      deliveries,
      order: ref(ten.map((delivery) => delivery.id)),
      storageKey: ref('k'),
      storage: memoryStorage(),
      fallbackId,
    })
    expect(sel.selectedId.value).toBe(ten[2].id)

    const explicit = ten[5].id
    sel.select(explicit)
    fallbackId.value = ten[8].id
    await nextTick()
    expect(sel.selectedId.value).toBe(explicit)

    deliveries.value = ten.filter((delivery) => delivery.id !== explicit)
    await nextTick()
    expect(sel.selectedId.value).toBe(ten[8].id)
    expect(sel.lastChange.value?.source).toBe('system')
  })

  it('rejects unknown ids and steps along the provided order', () => {
    const order = ref([ten[2].id, ten[0].id, ten[1].id])
    const sel = useAgentModeSelection({ deliveries: ref(ten), order, storageKey: ref('k'), storage: memoryStorage() })
    expect(sel.select('not-here')).toBe(false)
    sel.select(ten[2].id)
    expect(sel.step(1)).toBe(ten[0].id)
    expect(sel.step(1)).toBe(ten[1].id)
    expect(sel.step(1)).toBe(ten[1].id)
    expect(sel.selectEdge('first')).toBe(ten[2].id)
    expect(sel.selectedIndex.value).toBe(0)
  })
})

describe('useAgentModeSelection — live-only keyboard travel (PAI-805 corrections)', () => {
  it('steps and jumps only across ids that exist in the current snapshot', () => {
    // A frozen layout may still list ids that left the authorized set.
    const gone = 'dlv-gone'
    const order = ref([ten[0].id, gone, ten[1].id, ten[2].id, 'dlv-gone-2'])
    const deliveries = ref<readonly Delivery[]>(ten)
    const sel = useAgentModeSelection({ deliveries, order, storageKey: ref('k'), storage: memoryStorage() })
    sel.select(ten[0].id)
    expect(sel.step(1)).toBe(ten[1].id)
    expect(sel.selectedDelivery.value?.id).toBe(ten[1].id)
    expect(sel.step(-1)).toBe(ten[0].id)
    expect(sel.selectEdge('last')).toBe(ten[2].id)
    expect(sel.selectedDelivery.value).not.toBeNull()
    expect(sel.selectEdge('first')).toBe(ten[0].id)
    // The selection never lands on a gone id, so selectedDelivery is never null while deliveries exist.
    for (let i = 0; i < 10; i += 1) {
      sel.step(1)
      expect(sel.selectedDelivery.value).not.toBeNull()
    }
  })
})
