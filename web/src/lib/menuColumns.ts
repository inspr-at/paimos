// SPDX-License-Identifier: AGPL-3.0-only

// The collapsed project header's Display menu never scrolls: when its content is
// taller than the room under the button, it flows into more columns. The count is
// decided once when the menu opens (and when the window resizes), never while the
// person works in it.
export const MENU_COLUMN_WIDTH = 296
export const MENU_COLUMN_GAP = 28
// Across the popover: its own padding and border (6 + 6 + 2) and the menu's side padding (8 + 8).
export const MENU_FRAME = 30
const WINDOW_MARGIN = 16
// Wider than this the menu reads as a page of its own; past it, a window too short falls back to scrolling.
export const MENU_MAX_COLUMNS = 3

export function menuBodyWidth(columns: number) {
  return columns * MENU_COLUMN_WIDTH + (columns - 1) * MENU_COLUMN_GAP
}

export function menuPanelWidth(columns: number) {
  return menuBodyWidth(columns) + MENU_FRAME
}

// The most columns that fit the window beside each other, up to MENU_MAX_COLUMNS.
export function maxMenuColumns(windowWidth: number) {
  const fits = Math.floor((windowWidth - WINDOW_MARGIN - MENU_FRAME + MENU_COLUMN_GAP) / (MENU_COLUMN_WIDTH + MENU_COLUMN_GAP))
  return Math.min(MENU_MAX_COLUMNS, Math.max(1, fits))
}

// The fewest columns whose balanced height fits `room`; the most that fit the
// window when none does. `heightAt` lays the content out with n columns.
export function fitColumns(heightAt: (columns: number) => number, room: number, most: number) {
  for (let columns = 1; columns < most; columns++) if (heightAt(columns) <= room) return columns
  return Math.max(1, most)
}

// A short window may not fit even with every column the window holds, because the
// longest list is one block. Then that list may break across columns: the same
// search again, with the break allowed. `heightAt` lays the content out with n
// columns, whole blocks (`split` false) or with the long list broken (`split` true).
export function fitMenu(heightAt: (columns: number, split: boolean) => number, room: number, most: number) {
  const whole = fitColumns(columns => heightAt(columns, false), room, most)
  if (heightAt(whole, false) <= room) return { columns: whole, split: false }
  return { columns: fitColumns(columns => heightAt(columns, true), room, most), split: true }
}
