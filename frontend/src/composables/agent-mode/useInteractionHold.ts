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

// PAI-805 — interaction hold.
//
// Lane and in-lane order must not change while the pointer is over the
// canvas, while keyboard focus is inside it, or within HOLD_MS of the last
// interaction. `held` is the single signal the layout uses to decide
// whether a fresh canonical order may be applied now or must wait.

import { computed, getCurrentInstance, onBeforeUnmount, ref } from 'vue'

export const INTERACTION_HOLD_MS = 500

export interface InteractionHoldOptions {
  holdMs?: number
  now?: () => number
  setTimer?: (fn: () => void, ms: number) => ReturnType<typeof setTimeout>
  clearTimer?: (handle: ReturnType<typeof setTimeout>) => void
}

export function useInteractionHold(opts: InteractionHoldOptions = {}) {
  const holdMs = opts.holdMs ?? INTERACTION_HOLD_MS
  const now = opts.now ?? (() => Date.now())
  const setTimer = opts.setTimer ?? ((fn, ms) => setTimeout(fn, ms))
  const clearTimer = opts.clearTimer ?? ((h) => clearTimeout(h))

  const pointerInside = ref(false)
  const focusInside = ref(false)
  const recentInteraction = ref(false)
  let lastInteractionAt = 0
  let timer: ReturnType<typeof setTimeout> | null = null

  function armRelease() {
    if (timer !== null) clearTimer(timer)
    const elapsed = now() - lastInteractionAt
    const wait = Math.max(0, holdMs - elapsed)
    timer = setTimer(() => {
      timer = null
      if (now() - lastInteractionAt >= holdMs) recentInteraction.value = false
      else armRelease()
    }, wait)
  }

  /** Call on any pointer/keyboard interaction with a card target. */
  function markInteraction() {
    lastInteractionAt = now()
    recentInteraction.value = true
    armRelease()
  }

  function onPointerEnter() {
    pointerInside.value = true
  }
  function onPointerLeave() {
    pointerInside.value = false
    markInteraction()
  }
  function onFocusIn() {
    focusInside.value = true
  }
  function onFocusOut(event?: FocusEvent) {
    // Focus moving between cards keeps the hold; leaving the canvas
    // starts the 500 ms release window.
    const target = event?.currentTarget as HTMLElement | null | undefined
    const next = event?.relatedTarget as Node | null | undefined
    if (target && next && target.contains(next)) return
    focusInside.value = false
    markInteraction()
  }

  const held = computed(() => pointerInside.value || focusInside.value || recentInteraction.value)

  function dispose() {
    if (timer !== null) clearTimer(timer)
    timer = null
  }
  if (getCurrentInstance()) onBeforeUnmount(dispose)

  return {
    held,
    pointerInside,
    focusInside,
    markInteraction,
    onPointerEnter,
    onPointerLeave,
    onFocusIn,
    onFocusOut,
    dispose,
  }
}
