// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest'
import { fitColumns, fitMenu, maxMenuColumns, menuBodyWidth, menuPanelWidth, MENU_COLUMN_GAP, MENU_COLUMN_WIDTH, MENU_FRAME, MENU_MAX_COLUMNS } from '../src/lib/menuColumns'

// AEON-1028: the collapsed header's Display menu never scrolls. Its column count is
// the fewest whose balanced height fits the room under the button, capped by the
// window's width. The browser lays the height out (Playwright proves that); these
// cases pin the decision itself, with a content model whose height falls with the
// columns.
describe('collapsed header menu columns', () => {
  const total = 1500
  const balanced = (columns: number) => Math.ceil(total / columns)

  it('stays at one column while the content fits the room, however much room there is', () => {
    expect(fitColumns(balanced, 1500, 3)).toBe(1)
    expect(fitColumns(balanced, 4000, 3)).toBe(1)
  })

  it('splits into the fewest columns that fit, never more', () => {
    expect(fitColumns(balanced, 1499, 3)).toBe(2)
    expect(fitColumns(balanced, 750, 3)).toBe(2)
    expect(fitColumns(balanced, 749, 3)).toBe(3)
    expect(fitColumns(balanced, 500, 3)).toBe(3)
  })

  it('asks the layout only as often as needed', () => {
    const asked: number[] = []
    fitColumns(columns => { asked.push(columns); return balanced(columns) }, 800, 3)
    expect(asked).toEqual([1, 2])
  })

  it('takes the most columns the window holds when none fits, and at least one', () => {
    expect(fitColumns(balanced, 100, 3)).toBe(3)
    expect(fitColumns(balanced, 100, 2)).toBe(2)
    expect(fitColumns(balanced, 100, 1)).toBe(1)
    expect(fitColumns(balanced, 100, 0)).toBe(1)
  })

  it('does not trust an unbreakable block: a column count that still overflows is skipped', () => {
    // The columns list cannot be cut, so two columns are no shorter than 900.
    const stuck = (columns: number) => columns === 1 ? 1500 : columns === 2 ? 900 : 640
    expect(fitColumns(stuck, 800, 3)).toBe(3)
    expect(fitColumns(stuck, 900, 3)).toBe(2)
  })

  it('lets the long list break across columns only when whole blocks cannot fit at any count', () => {
    // Whole blocks cannot go below 640 (the column list); broken, three columns reach 500.
    const heightAt = (columns: number, split: boolean) => split ? Math.ceil(1500 / columns) : Math.max(640, Math.ceil(1500 / columns))
    expect(fitMenu(heightAt, 800, 3)).toEqual({ columns: 2, split: false })
    expect(fitMenu(heightAt, 640, 3)).toEqual({ columns: 3, split: false })
    expect(fitMenu(heightAt, 600, 3)).toEqual({ columns: 3, split: true })
    expect(fitMenu(heightAt, 500, 3)).toEqual({ columns: 3, split: true })
  })

  it('still gives up gracefully when even a broken list does not fit', () => {
    const heightAt = (columns: number, split: boolean) => Math.ceil((split ? 1500 : 1600) / columns)
    expect(fitMenu(heightAt, 100, 3)).toEqual({ columns: 3, split: true })
    expect(fitMenu(heightAt, 100, 1)).toEqual({ columns: 1, split: true })
  })

  it('counts the columns a window can hold beside each other, with its margin', () => {
    expect(maxMenuColumns(320)).toBe(1)
    expect(maxMenuColumns(0)).toBe(1)
    const two = menuPanelWidth(2) + 16, three = menuPanelWidth(3) + 16
    expect(maxMenuColumns(two - 1)).toBe(1)
    expect(maxMenuColumns(two)).toBe(2)
    expect(maxMenuColumns(three - 1)).toBe(2)
    expect(maxMenuColumns(three)).toBe(3)
    expect(maxMenuColumns(1440)).toBe(MENU_MAX_COLUMNS)
    expect(maxMenuColumns(5000)).toBe(MENU_MAX_COLUMNS)
  })

  it('sizes the body and the panel from one column width and gap', () => {
    expect(menuBodyWidth(1)).toBe(MENU_COLUMN_WIDTH)
    expect(menuBodyWidth(3)).toBe(3 * MENU_COLUMN_WIDTH + 2 * MENU_COLUMN_GAP)
    expect(menuPanelWidth(2)).toBe(menuBodyWidth(2) + MENU_FRAME)
  })
})
