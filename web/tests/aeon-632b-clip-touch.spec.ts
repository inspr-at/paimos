// SPDX-License-Identifier: AGPL-3.0-only

import { expect, test, type Locator } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'

import { expectStableControls } from './helpers/stable'
import { groupsWorld, mockProjectGroups } from './project-groups-fixtures'
import { GROUP, capture } from './aeon-632b-clip-fixtures'

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
  test(`stationary touch explains a restricted shared group ${width} ${theme}`, async ({ browser }) => {
    const context = await browser.newContext({ viewport: { width, height: 900 }, hasTouch: true, isMobile: width === 390, reducedMotion: 'reduce' })
    try {
      const page = await context.newPage()
      await page.emulateMedia({ colorScheme: theme })
      const errors = watchErrors(page)
      const data = fixtures()
      data.preferences.theme = { choice: theme }
      data.preferences['project-groups'] = { groups: [{ id: 'g:mine', name: 'Persönliche Ablage' }] }
      const calls = await mockWork(page, data, { admin: false })
      const world = groupsWorld({ clients: ['p-aeon'] })
      world.groups[0]!.name = GROUP
      await mockProjectGroups(page, data, world, { admin: false })
      await page.goto('/')
      await page.locator('[data-project-id="p-pharos"] .item-link').focus()
      await page.keyboard.press('m')
      const picker = page.getByRole('dialog', { name: 'Move Pharos to a group' })
      const search = picker.getByRole('combobox')
      const rows = picker.getByRole('option')
      const restricted = rows.filter({ hasText: GROUP })
      const note = picker.locator('.option-note')
      const reason = 'Only a workspace admin can add projects to a shared group.'
      await expect(restricted).toHaveAttribute('aria-disabled', 'true')
      await expect(restricted).toHaveAttribute('aria-selected', 'false')
      await expect(note).toBeEmpty()
      const controls = { search, selector: picker.locator('.options'), explanation: note,
        ...Object.fromEntries((await rows.all()).map((row, index) => [`row-${index}`, row])) }
      const tap = async (row: Locator) => {
        const bounds = (await row.boundingBox())!
        // A real stationary touch bypasses Playwright's aria-disabled action
        // guard without a hover/pointermove that would mask the regression.
        await page.touchscreen.tap(bounds.x + bounds.width / 2, bounds.y + bounds.height / 2)
      }
      await expectStableControls({ controls, interactions: [
        { name: 'restricted row touch discloses reason', run: async () => {
          await tap(restricted)
          await expect(note).toHaveText(reason)
          await expect(restricted).toHaveAttribute('aria-selected', 'true')
          await expect(search).toHaveAttribute('aria-activedescendant', (await restricted.getAttribute('id'))!)
          await expect(picker).toBeVisible()
        } },
        { name: 'Enter retains restriction and explanation', run: async () => {
          await search.press('Enter')
          await expect(note).toHaveText(reason)
          await expect(restricted).toHaveAttribute('aria-selected', 'true')
          await expect(picker).toBeVisible()
        } },
        { name: 'repeat stationary touch keeps explanation', run: async () => {
          await tap(restricted)
          await expect(note).toHaveText(reason)
        } },
      ], scrollAreas: { picker: picker.locator('.options') } })
      expect(world.calls.filter(call => call.method !== 'GET')).toEqual([])
      expect(calls.filter(call => call.method !== 'GET' && call.path.includes('project-groups'))).toEqual([])
      expect(world.groups[0]!.project_ids).toEqual(['p-aeon'])
      await capture(page, 'restricted-group-touch', width, theme)
      await tap(rows.filter({ hasText: 'Persönliche Ablage' }))
      await expect(picker).toHaveCount(0)
      await expect.poll(() => data.preferences['project-groups']?.place).toEqual({ 'p-pharos': 'g:mine' })
      expect(world.calls.filter(call => call.method !== 'GET')).toEqual([])
      expect(errors).toEqual([])
    } finally { await context.close() }
  })
}
