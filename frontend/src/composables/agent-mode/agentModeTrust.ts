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

// PAI-805 — estimate trust policy (client side of PAI-803).
//
// The server decides `trusted`; the client only narrows further. Percent
// and landing time are shown ONLY when:
//   - the server marked the estimate trusted,
//   - confidence is high or medium for a point estimate, or low with an
//     explicit valid optimistic/pessimistic range,
//   - the delivery is not blocked / waiting,
//   - the report is not stale / unknown-fresh.
// Otherwise the UI shows an explicit "no estimate" with the reason.

import type { Delivery } from '@/services/agentMode'
import { parseIsoInstant } from '@/services/agentModeAggregateSchema'

export type EstimateSuppression =
  | 'ok'
  | 'blocked'
  | 'waiting'
  | 'stale'
  | 'low_confidence'
  | 'suppressed'
  | 'invalid'
  | 'untrusted'
  | 'none'

export interface EstimatePresentation {
  /** Whether a percent may be shown at all. */
  showPercent: boolean
  percent: number | null
  /** Whether an approximate landing time may be shown at all. */
  showEta: boolean
  /** Low-confidence ETA is eligible only as explicit bounds; it never
   * exposes the point landing or a derived midpoint/remaining duration. */
  rangeOnly: boolean
  landingAt: string | null
  optimisticAt: string | null
  pessimisticAt: string | null
  /** Why the estimate is withheld (or 'ok'). Percent and ETA share the
   * structural reasons; confidence/trust reasons are evaluated per field. */
  percentReason: EstimateSuppression
  etaReason: EstimateSuppression
}

function structuralSuppression(d: Delivery): EstimateSuppression | null {
  if (d.health === 'blocked' || d.activity.kind === 'blocked') return 'blocked'
  if (d.activity.kind === 'waiting') return 'waiting'
  if (d.freshness.state === 'stale' || d.freshness.state === 'unknown') return 'stale'
  if (d.suppressionCodes.length > 0) return 'suppressed'
  return null
}

function validInstant(value: string | null): boolean {
  return parseIsoInstant(value) != null
}

function validRange(optimisticAt: string | null, pessimisticAt: string | null): boolean {
  if (!validInstant(optimisticAt) || !validInstant(pessimisticAt)) return false
  return Date.parse(optimisticAt!) < Date.parse(pessimisticAt!)
}

export function estimatePresentation(d: Delivery): EstimatePresentation {
  const structural = structuralSuppression(d)

  let percentReason: EstimateSuppression
  if (!d.progress || d.progress.percent == null) percentReason = 'none'
  else if (structural) percentReason = structural
  else if (!d.progress.trusted) percentReason = 'untrusted'
  else if (d.progress.confidence === 'low' || d.progress.confidence === 'none') percentReason = 'low_confidence'
  else percentReason = 'ok'

  let etaReason: EstimateSuppression
  let rangeOnly = false
  if (!d.eta) etaReason = 'none'
  else if (structural) etaReason = structural
  else if (!d.eta.trusted) etaReason = 'untrusted'
  else if (d.eta.confidence === 'low') {
    if (validRange(d.eta.optimisticAt, d.eta.pessimisticAt)) {
      etaReason = 'ok'
      rangeOnly = true
    } else {
      etaReason = d.eta.optimisticAt || d.eta.pessimisticAt ? 'invalid' : 'low_confidence'
    }
  } else if (d.eta.confidence === 'none') etaReason = 'low_confidence'
  else if (!d.eta.landingAt) etaReason = 'none'
  else if (!validInstant(d.eta.landingAt)) etaReason = 'invalid'
  else etaReason = 'ok'

  return {
    showPercent: percentReason === 'ok',
    percent: percentReason === 'ok' ? d.progress!.percent : null,
    showEta: etaReason === 'ok',
    rangeOnly,
    landingAt: etaReason === 'ok' && !rangeOnly ? d.eta!.landingAt : null,
    optimisticAt: etaReason === 'ok' ? d.eta!.optimisticAt : null,
    pessimisticAt: etaReason === 'ok' ? d.eta!.pessimisticAt : null,
    percentReason,
    etaReason,
  }
}

/** Remaining milliseconds until landing, computed against the server
 * clock (serverNowMs = browser now + server offset). Null when unknown. */
export function remainingMs(landingAt: string | null, serverNowMs: number): number | null {
  if (!landingAt) return null
  const ms = Date.parse(landingAt)
  if (!Number.isFinite(ms)) return null
  return ms - serverNowMs
}
