// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { expect, test, type Locator } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { knowledgeWorld, mockKnowledge } from './knowledge-fixtures'
import { expectStableControls } from './helpers/stable'
import { groupsWorld, mockProjectGroups } from './project-groups-fixtures'
import { NAME, GROUP, SLUG, shots, setup, capture, disclosure } from './aeon-632b-clip-fixtures'

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

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark']) {
  test(`planning names ${width} ${theme}`, async ({ page }) => {
    test.setTimeout(90_000)
    await page.setViewportSize({ width, height: 900 })
    await setup(page, theme)
    const errors = watchErrors(page)
    await page.goto('/')
    await disclosure(page, page.locator('[data-project-id="p-pharos"] .name'))
    await disclosure(page, page.locator('.group-head .name').filter({ hasText: GROUP }), { action: page.getByRole('button', { name: `Actions for group ${GROUP}` }) })
    await capture(page, 'project-rows-groups', width, theme)
    await page.locator('[data-project-id="p-pharos"] .item-link').focus()
    await page.keyboard.press('m')
    const picker = page.getByRole('dialog', { name: /to a group$/ })
    await expect(picker).toBeVisible()
    const options = picker.getByRole('option')
    const search = picker.getByRole('combobox')
    expect(await options.count()).toBeGreaterThanOrEqual(3)
    const initial = await options.evaluateAll(rows => rows.findIndex(row => row.getAttribute('aria-selected') === 'true'))
    const heights = await options.evaluateAll(rows => rows.map(row => row.getBoundingClientRect().height))
    expect(new Set(heights).size).toBe(1)
    const pickerControls = { search, options: picker.locator('.options'), ...Object.fromEntries((await options.all()).map((row, index) => [`option-${index + 1}`, row])) }
    await expectStableControls({ controls: pickerControls, interactions: [
      { name: 'next group', run: async () => { await search.press('ArrowDown'); await expect(options.nth((initial + 1) % heights.length)).toHaveAttribute('aria-selected', 'true') } },
      { name: 'previous group', run: async () => { await search.press('ArrowUp'); await expect(options.nth(initial)).toHaveAttribute('aria-selected', 'true') } },
    ], scrollAreas: { picker: picker.locator('.options') } })
    await expect(search).toBeFocused()
    const activeName = options.nth(initial).locator('.name')
    if (await activeName.evaluate(el => el.scrollWidth > el.clientWidth + 1 || el.scrollHeight > el.clientHeight + 1)) {
      await expect(page.locator('.tooltip')).toHaveText((await activeName.getAttribute('data-tip'))!)
    }
    await disclosure(page, options.locator('.name').filter({ hasText: GROUP }), pickerControls, { tab: false })
    await capture(page, 'move-to-group', width, theme)
    await page.keyboard.press('Escape')
    await page.getByRole('radio', { name: 'Cards view', exact: true }).click()
    await disclosure(page, page.locator('[data-project-id="p-pharos"] .card-name'))
    await capture(page, 'project-cards', width, theme)

    const knowledge = knowledgeWorld()
    knowledge.entries[0]!.title = NAME; knowledge.entries[0]!.slug = SLUG
    await mockKnowledge(page, knowledge)
    await page.goto('/knowledge')
    await disclosure(page, page.locator('.kp-group h2').first())
    await disclosure(page, page.locator('.kp-title').filter({ hasText: NAME }).first())
    await disclosure(page, page.locator('.kp-slug').filter({ hasText: SLUG }).first(), {}, { twoLines: false })
    await capture(page, 'knowledge', width, theme)

    expect(errors).toEqual([])
  })
}
