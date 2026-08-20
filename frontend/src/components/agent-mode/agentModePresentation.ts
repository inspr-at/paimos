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

// PAI-805 — presentation helpers shared by cards, focus, and narration.
// Pure functions; i18n happens at the call site via the returned keys.

import type { Delivery } from '@/services/agentMode'
import { formatRelativeTimeWithLocale, formatTimeWithLocale, formatShortDateTimeWithLocale } from '@/composables/useDateFormat'
import { estimatePresentation, remainingMs, type EstimatePresentation } from '@/composables/agent-mode/agentModeTrust'

export function actorInitials(d: Delivery): string {
  const label = d.actor?.label ?? ''
  if (!label) return '?'
  const parts = label.split(/[\s_-]+/).filter(Boolean)
  if (parts.length >= 2) return (parts[0][0] + parts[1][0]).toUpperCase()
  return label.slice(0, 2).toUpperCase()
}

export function stageLabelKey(d: Delivery): string {
  return `agentMode.stage.${d.stage.key}`
}

/** "2 / 5" when the API reports position, otherwise empty. */
export function stagePosition(d: Delivery): string {
  if (d.stage.index == null || d.stage.total == null || d.stage.total <= 0) return ''
  return `${d.stage.index}/${d.stage.total}`
}

export function healthKey(d: Delivery): string {
  return `agentMode.health.${d.health}`
}

export function activityKey(d: Delivery): string {
  return `agentMode.activity.${d.activity.kind}`
}

export function freshnessKey(d: Delivery): string {
  return `agentMode.freshness.${d.freshness.state}`
}

export function relativeReported(d: Delivery, locale: string, nowMs: number): string | null {
  if (!d.freshness.lastReportAt) return null
  return formatRelativeTimeWithLocale(d.freshness.lastReportAt, locale, nowMs)
}

export interface EstimateView {
  presentation: EstimatePresentation
  /** Approximate landing time (locale), only when trusted. */
  landingLabel: string | null
  /** Human remaining duration ("52 min", "1 h 20 min", "overdue"). */
  remainingLabel: string | null
  rangeLabel: string | null
}

export function formatDuration(ms: number): string {
  const totalMinutes = Math.round(Math.abs(ms) / 60_000)
  if (totalMinutes < 1) return '<1 min'
  const hours = Math.floor(totalMinutes / 60)
  const minutes = totalMinutes % 60
  if (hours >= 24) {
    const days = Math.floor(hours / 24)
    const restHours = hours % 24
    return restHours ? `${days} d ${restHours} h` : `${days} d`
  }
  if (hours > 0) return minutes ? `${hours} h ${minutes} min` : `${hours} h`
  return `${minutes} min`
}

function sameLocalDay(aMs: number, bMs: number): boolean {
  const a = new Date(aMs)
  const b = new Date(bMs)
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate()
}

export function estimateView(d: Delivery, locale: string, serverNowMs: number): EstimateView {
  const presentation = estimatePresentation(d)
  let landingLabel: string | null = null
  let remainingLabel: string | null = null
  let rangeLabel: string | null = null
  if (presentation.showEta && presentation.landingAt) {
    const landingMs = Date.parse(presentation.landingAt)
    landingLabel = Number.isFinite(landingMs) && sameLocalDay(landingMs, serverNowMs)
      ? formatTimeWithLocale(presentation.landingAt, locale)
      : formatShortDateTimeWithLocale(presentation.landingAt, locale)
    const rem = remainingMs(presentation.landingAt, serverNowMs)
    if (rem != null) remainingLabel = rem < -60_000 ? `−${formatDuration(rem)}` : formatDuration(rem)
    if (presentation.optimisticAt && presentation.pessimisticAt) {
      rangeLabel = `${formatTimeWithLocale(presentation.optimisticAt, locale)}–${formatTimeWithLocale(presentation.pessimisticAt, locale)}`
    }
  }
  return { presentation, landingLabel, remainingLabel, rangeLabel }
}

/** Health classes are color-independent: each health also carries an icon + word. */
export function healthIcon(d: Delivery): string {
  switch (d.health) {
    case 'healthy':
      return 'circle-check'
    case 'attention':
      return 'hourglass'
    case 'at_risk':
      return 'alert-triangle'
    case 'blocked':
      return 'octagon-alert'
    default:
      return 'circle'
  }
}
