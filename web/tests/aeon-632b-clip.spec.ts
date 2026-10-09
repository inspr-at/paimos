// SPDX-License-Identifier: AGPL-3.0-only

import { expect, test } from '@playwright/test'
import { watchErrors } from './work-fixtures'
import { knowledgeWorld, mockKnowledge } from './knowledge-fixtures'
import { expectStableControls } from './helpers/stable'

import { NAME, GROUP, SLUG, setup, capture, disclosure } from './aeon-632b-clip-fixtures'

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
