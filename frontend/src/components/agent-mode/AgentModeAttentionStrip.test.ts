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

import { afterEach, describe, expect, it } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

import { mountComponent } from '@/components/ai/testMount'
import type { Delivery } from '@/services/agentMode'
import type { AgentModeAttentionAggregate } from '@/services/agentModeAggregateSchema'
import { FIXTURE_BASE_TIME, makeFixtureSnapshot } from '@/services/agentModeFixtures'
import { normalizeWireSnapshot } from '@/services/agentModeTransport'
import AgentModeAttentionStrip from './AgentModeAttentionStrip.vue'

const SERVER_NOW = Date.parse(FIXTURE_BASE_TIME)
const base = normalizeWireSnapshot(makeFixtureSnapshot(30), SERVER_NOW).deliveries

function withAttention(delivery: Delivery, level: 0 | 1 = 1): Delivery {
  return {
    ...delivery,
    attention: {
      level,
      reason: level > 0 ? 'Needs review' : null,
      since: level > 0 ? FIXTURE_BASE_TIME : null,
    },
  }
}

const activeDeliveries = base.map((delivery, index) => withAttention(delivery, index < 20 ? 1 : 0))

function aggregate(total = 20): AgentModeAttentionAggregate {
  return {
    total,
    items: activeDeliveries.slice(0, 12).map((delivery) => ({
      deliveryId: delivery.id,
      level: 1,
      primaryReason: 'stale_no_signal',
      flags: ['stale_no_signal'],
      since: FIXTURE_BASE_TIME,
    })),
  }
}

async function mountStrip(
  selectedId: string,
  deliveries: readonly Delivery[] = activeDeliveries,
  authoritative: AgentModeAttentionAggregate = aggregate(),
) {
  setActivePinia(createPinia())
  return mountComponent(AgentModeAttentionStrip, {
    deliveries,
    selectedId,
    serverNowMs: SERVER_NOW,
    locale: 'en-US',
    authoritative,
    max: 12,
  })
}

function itemIds(root: HTMLElement): string[] {
  return [...root.querySelectorAll<HTMLElement>('.am-attention-item')]
    .map((item) => item.dataset.attentionId!)
}

function expectHidden(root: HTMLElement, count: number) {
  expect(root.querySelector('.am-attention-more')?.textContent).toContain(`${count} more`)
}

describe('AgentModeAttentionStrip (PAI-807) — authoritative hidden count', () => {
  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('subtracts an active attentive pinned selection already present in the bounded list', async () => {
    const authoritative = aggregate()
    const selectedId = authoritative.items[0].deliveryId
    const mounted = await mountStrip(selectedId, activeDeliveries, authoritative)

    expect(itemIds(mounted.el)).toEqual(authoritative.items.slice(1).map((item) => item.deliveryId))
    expectHidden(mounted.el, 8)
    await mounted.unmount()
  })

  it('subtracts an active attentive pinned selection even when it ranks outside the bounded list', async () => {
    const authoritative = aggregate()
    const selectedId = activeDeliveries[15].id
    const mounted = await mountStrip(selectedId, activeDeliveries, authoritative)

    expect(itemIds(mounted.el)).toEqual(authoritative.items.map((item) => item.deliveryId))
    expectHidden(mounted.el, 7)
    await mounted.unmount()
  })

  it('does not subtract an active non-attentive selection outside the bounded list', async () => {
    const authoritative = aggregate()
    const selectedId = activeDeliveries[20].id
    const mounted = await mountStrip(selectedId, activeDeliveries, authoritative)

    expect(itemIds(mounted.el)).toEqual(authoritative.items.map((item) => item.deliveryId))
    expectHidden(mounted.el, 8)
    await mounted.unmount()
  })

  it('does not subtract an attentive-looking selected_outside row excluded from active deliveries', async () => {
    const authoritative = aggregate()
    const selectedOutside = withAttention(base[25], 1)
    const activeWithoutOutside = activeDeliveries.filter((delivery) => delivery.id !== selectedOutside.id)
    const mounted = await mountStrip(selectedOutside.id, activeWithoutOutside, authoritative)

    expect(itemIds(mounted.el)).toEqual(authoritative.items.map((item) => item.deliveryId))
    expectHidden(mounted.el, 8)
    await mounted.unmount()
  })

  it('keeps server order, mounts at most twelve items, and derives overflow from the uncapped total', async () => {
    const authoritative = aggregate(100)
    const mounted = await mountStrip(activeDeliveries[20].id, activeDeliveries, authoritative)

    expect(itemIds(mounted.el)).toEqual(authoritative.items.map((item) => item.deliveryId))
    expect(itemIds(mounted.el)).toHaveLength(12)
    expectHidden(mounted.el, 88)
    await mounted.unmount()
  })

  it('announces a positive authoritative total when the server supplies no bounded items', async () => {
    const mounted = await mountStrip(activeDeliveries[20].id, activeDeliveries, { total: 5, items: [] })

    expect(mounted.el.querySelector('.am-attention')).not.toBeNull()
    expect(itemIds(mounted.el)).toHaveLength(0)
    expectHidden(mounted.el, 5)
    await mounted.unmount()
  })

  it('keeps the total accessible when authoritative item identities cannot be looked up safely', async () => {
    const authoritative = aggregate()
    const mounted = await mountStrip(activeDeliveries[20].id, [], {
      total: 2,
      items: authoritative.items.slice(0, 1),
    })

    expect(mounted.el.querySelector('.am-attention')).not.toBeNull()
    expect(itemIds(mounted.el)).toHaveLength(0)
    expectHidden(mounted.el, 2)
    await mounted.unmount()
  })

  it('deduplicates a selected-only bounded item while still announcing other hidden attention', async () => {
    const selectedId = activeDeliveries[0].id
    const authoritative = aggregate()
    const mounted = await mountStrip(selectedId, activeDeliveries, {
      total: 4,
      items: authoritative.items.slice(0, 1),
    })

    expect(mounted.el.querySelector('.am-attention')).not.toBeNull()
    expect(itemIds(mounted.el)).toHaveLength(0)
    expectHidden(mounted.el, 3)
    await mounted.unmount()
  })
})
