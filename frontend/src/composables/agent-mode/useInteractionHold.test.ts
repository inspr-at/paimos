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

import { describe, expect, it, vi } from 'vitest'

import { useInteractionHold } from './useInteractionHold'

describe('useInteractionHold (PAI-805: no reorder under pointer / focus / 500 ms)', () => {
  it('holds while the pointer is inside and for 500 ms after it leaves', () => {
    vi.useFakeTimers()
    try {
      const hold = useInteractionHold()
      expect(hold.held.value).toBe(false)
      hold.onPointerEnter()
      expect(hold.held.value).toBe(true)
      vi.advanceTimersByTime(5_000)
      expect(hold.held.value).toBe(true)
      hold.onPointerLeave()
      expect(hold.held.value).toBe(true)
      vi.advanceTimersByTime(499)
      expect(hold.held.value).toBe(true)
      vi.advanceTimersByTime(2)
      expect(hold.held.value).toBe(false)
      hold.dispose()
    } finally {
      vi.useRealTimers()
    }
  })

  it('extends the window on every interaction and tracks focus separately', () => {
    vi.useFakeTimers()
    try {
      const hold = useInteractionHold()
      hold.markInteraction()
      vi.advanceTimersByTime(400)
      hold.markInteraction()
      vi.advanceTimersByTime(400)
      expect(hold.held.value).toBe(true)
      vi.advanceTimersByTime(101)
      expect(hold.held.value).toBe(false)

      hold.onFocusIn()
      expect(hold.held.value).toBe(true)
      hold.onFocusOut()
      expect(hold.held.value).toBe(true)
      vi.advanceTimersByTime(501)
      expect(hold.held.value).toBe(false)
      hold.dispose()
    } finally {
      vi.useRealTimers()
    }
  })
})
