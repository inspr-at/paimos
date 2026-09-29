// SPDX-License-Identifier: AGPL-3.0-only
// Placement for the journey flow pill in the footer centre. The version pill
// keeps its width on the right. The wordmark stays whole, or drops out when
// the only way to keep the next action readable is to give that room to the pill.

export interface Box { left: number; right: number }

// Null leaves the wordmark at its natural width. 0 hides it.
export function flowNameBudget(contentWidth: number, trailingWidth: number, leadingNatural: number, pillNatural: number, gap = 8): number | null {
  if (contentWidth <= 0) return 0
  const room = contentWidth - trailingWidth - gap * 2
  if (room - leadingNatural >= pillNatural) return null
  return 0
}

// `left` is relative to the footer's border edge. `width` is the pill's used
// width: its natural width when that fits on the page centre, otherwise the
// gap between the two side controls.
export function placeFlowPill(footer: Box, leading: Box, trailing: Box, pillWidth: number, gap = 8): { left: number; width: number } {
  const leftLimit = leading.right + gap
  const rightLimit = trailing.left - gap
  const available = Math.max(0, rightLimit - leftLimit)
  const width = Math.min(Math.max(0, pillWidth), available)
  const center = (footer.left + footer.right) / 2
  let left = center - width / 2
  if (left < leftLimit) left = leftLimit
  if (left + width > rightLimit) left = Math.max(leftLimit, rightLimit - width)
  return { left: left - footer.left, width }
}
