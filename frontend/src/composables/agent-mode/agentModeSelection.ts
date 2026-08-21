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

// PAI-805 — selection invariants (pure).
//
// Exactly one delivery is selected whenever at least one exists. Restore
// the last selection when it is still in the authorized snapshot;
// otherwise pick the highest-attention delivery, then the soonest
// trusted landing, with a stable tie-break (issue key, then id).

import type { Delivery } from '@/services/agentMode'
import { compareDeliveries } from './agentModeOrdering'

export type SelectionOrigin = 'restored' | 'default' | 'kept' | 'none'

export interface SelectionChoice {
  id: string | null
  origin: SelectionOrigin
}

/** Deterministic default: attention ↓, trusted landing ↑, issue key ↑, id ↑. */
export function pickDefaultSelection(deliveries: readonly Delivery[]): string | null {
  if (deliveries.length === 0) return null
  let best = deliveries[0]
  for (let i = 1; i < deliveries.length; i += 1) {
    if (compareDeliveries(deliveries[i], best) < 0) best = deliveries[i]
  }
  return best.id
}

/**
 * Resolves the selection for a (new) snapshot.
 * @param current  the selection held in memory (wins when still present)
 * @param remembered the persisted last selection (restored when authorized)
 */
export function resolveSelection(
  deliveries: readonly Delivery[],
  current: string | null,
  remembered: string | null,
): SelectionChoice {
  if (deliveries.length === 0) return { id: null, origin: 'none' }
  if (current && deliveries.some((d) => d.id === current)) return { id: current, origin: 'kept' }
  if (remembered && deliveries.some((d) => d.id === remembered)) return { id: remembered, origin: 'restored' }
  return { id: pickDefaultSelection(deliveries), origin: 'default' }
}

/** Moves the selection along an ordered id list; clamps at the ends. */
export function stepSelection(order: readonly string[], current: string | null, delta: number): string | null {
  if (order.length === 0) return null
  const index = current ? order.indexOf(current) : -1
  if (index === -1) return delta >= 0 ? order[0] : order[order.length - 1]
  const next = Math.max(0, Math.min(order.length - 1, index + delta))
  return order[next]
}
