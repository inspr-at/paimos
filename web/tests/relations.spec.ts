// SPDX-License-Identifier: AGPL-3.0-only
// U27 (AEON-154): relations in the web UI. The picker on the side panel and the
// full page chooses a relation, finds the other ticket with the palette's search
// and links it; the server's refusals (a loop, an existing link) read in words;
// removing asks first and Undo brings the link back. Keyboard first, both
// themes, phone.
import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { mkdirSync } from 'node:fs'
import { expectStableControls } from './helpers/stable'
import { fixtures, mockWork, watchErrors, type Call } from './work-fixtures'

test.beforeEach(async ({ page }) => { await page.clock.setSystemTime(new Date('2026-09-23T12:00:00Z')) })

const panel = (page: Page) => page.getByRole('complementary', { name: 'Ticket details' })
const relations = (page: Page) => page.getByRole('region', { name: 'Relations' }).locator('visible=true')
const picker = (page: Page, key: string) => page.getByRole('dialog', { name: `Link ${key} to another ticket` })
const linkWrites = (calls: Call[], method: string) => calls.filter(call => call.method === method && call.path.startsWith('/api/relations'))
const toast = (page: Page) => page.locator('.toast')

async function axe(page: Page) {
  for (const pop of await page.locator('.floating').all()) await expect(pop).toBeInViewport()
  const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).exclude('.version-coordinate').exclude('.calendar-version').analyze()
  const summary = results.violations.map(v => `${v.id}: ${v.help} ${v.nodes.slice(0, 3).map(n => n.target.join(' ')).join(' | ')}`)
  expect(summary, summary.join('\n')).toEqual([])
}

test('r opens the picker; the relation and the ticket are chosen by keyboard; Undo takes the link back', async ({ page }) => {
  const errors = watchErrors(page)
  const calls = await mockWork(page, fixtures())
  await page.goto('/p/PHAROS/PHAROS-12')
  await expect(panel(page).getByRole('heading', { name: 'Add an Oracle Cloud connector' })).toBeVisible()
  // No links yet: the section says so and offers Link.
  await expect(relations(page)).toContainText('No links yet')
  await panel(page).focus()
  await page.keyboard.press('r')
  const dialog = picker(page, 'PHAROS-12')
  await expect(dialog).toBeVisible()
  const search = dialog.getByRole('combobox')
  await expect(search).toHaveAccessibleName('Find the ticket PHAROS-12 relates to')
  await expect(search).toBeFocused()
  // Shift Tab reaches the relation; arrows choose it, as radios do.
  await page.keyboard.press('Shift+Tab')
  await expect(dialog.getByRole('radio', { name: 'Relates to' })).toBeFocused()
  await page.keyboard.press('ArrowLeft')
  await page.keyboard.press('ArrowLeft')
  await expect(dialog.getByRole('radio', { name: 'Blocked by' })).toHaveAttribute('aria-checked', 'true')
  await expect(dialog).toContainText('waits for the other to finish')
  await page.keyboard.press('Tab')
  await expect(search).toBeFocused()
  await expect(search).toHaveAccessibleName('Find the ticket PHAROS-12 blocked by')
  // A key is a key lookup, as in the palette; the open ticket is never offered.
  await page.keyboard.type('PHAROS-1')
  const options = dialog.getByRole('option')
  await expect(options.first()).toContainText('PHAROS-1')
  await expect(options.filter({ hasText: 'PHAROS-12' })).toHaveCount(0)
  await search.fill('PHAROS-14')
  await expect(options).toHaveCount(1)
  await expect(options.first()).toContainText('Visual acceptance of the version pill')
  await page.keyboard.press('Enter')
  await expect(dialog).toHaveCount(0)
  // The other ticket blocks this one: it is the source.
  expect(linkWrites(calls, 'POST').at(-1)!.body).toEqual({ source_node_id: 'n-4', target_node_id: 'n-2', type: 'blocks' })
  await expect(relations(page).getByRole('button', { name: /^Blocked by PHAROS-14/ })).toBeVisible()
  await expect(relations(page).getByRole('button', { name: 'Link', exact: true })).toBeFocused()
  await expect(toast(page).filter({ hasText: 'PHAROS-14 now blocks PHAROS-12' })).toBeVisible()
  await toast(page).filter({ hasText: 'PHAROS-14 now blocks PHAROS-12' }).getByRole('button', { name: 'Undo' }).click()
  await expect(relations(page).getByRole('button', { name: /^Blocked by PHAROS-14/ })).toHaveCount(0)
  expect(linkWrites(calls, 'DELETE').at(-1)!.path).toMatch(/^\/api\/relations\/r-new-/)
  await expect(toast(page).filter({ hasText: 'PHAROS-14 no longer blocks PHAROS-12' })).toBeVisible()
  expect(errors).toEqual([])
})

test('a loop and an existing link are refused with the server reason in words; the picker stays open', async ({ page }) => {
  const errors = watchErrors(page)
  const data = fixtures()
  // PHAROS-12 blocks PHAROS-14, which blocks PHAROS-11.
  data.relations.push({ id: 'r-3', source_node_id: 'n-2', target_node_id: 'n-4', type: 'blocks', created_at: '2026-09-20T10:00:00Z' })
  const calls = await mockWork(page, data)
  await page.goto('/p/PHAROS/PHAROS-11')
  await relations(page).getByRole('button', { name: 'Link', exact: true }).click()
  const dialog = picker(page, 'PHAROS-11')
  await dialog.getByRole('radio', { name: 'Blocks' }).click()
  await expect(dialog.getByRole('combobox')).toBeFocused()
  // Words search the titles, as in the palette.
  await page.keyboard.type('Visual acceptance')
  await expect(dialog.getByRole('option').first()).toContainText('PHAROS-14')
  await page.keyboard.press('Enter')
  const alert = dialog.getByRole('alert')
  await expect(alert).toHaveText('PHAROS-11 cannot block PHAROS-14: PHAROS-14 already blocks PHAROS-11, so this link would make a loop.')
  await expect(dialog.getByRole('combobox')).toBeFocused()
  // A longer loop spells out its chain.
  // Enter right after typing waits for the answer to what was typed.
  await dialog.getByRole('combobox').fill('PHAROS-12')
  await page.keyboard.press('Enter')
  await expect(alert).toHaveText('PHAROS-11 cannot block PHAROS-12: PHAROS-12 blocks PHAROS-14, which blocks PHAROS-11, so this link would make a loop.')
  // Typing again clears the reason; a ticket already linked this way is marked and not sent.
  await dialog.getByRole('radio', { name: 'Relates to' }).click()
  await dialog.getByRole('combobox').fill('Beacon')
  const beacon = dialog.getByRole('option').filter({ hasText: 'PHAROS-15' })
  await expect(beacon).toContainText('Linked')
  await expect(beacon).toHaveAttribute('aria-disabled', 'true')
  await expect(dialog.getByRole('alert')).toHaveCount(0)
  const posts = linkWrites(calls, 'POST').length
  await expect(beacon).toHaveAttribute('aria-selected', 'true')
  await page.keyboard.press('Enter')
  await expect(dialog.getByRole('alert')).toHaveText('PHAROS-11 is already linked to PHAROS-15 as “Relates to”.')
  expect(linkWrites(calls, 'POST')).toHaveLength(posts)
  // Escape closes and hands focus back to Link.
  await page.keyboard.press('Escape')
  await expect(dialog).toHaveCount(0)
  await expect(relations(page).getByRole('button', { name: 'Link', exact: true })).toBeFocused()
  expect(errors).toEqual([])
})

test('removing a link asks first, keeps focus in the list, and Undo brings it back', async ({ page }) => {
  const errors = watchErrors(page)
  const calls = await mockWork(page, fixtures())
  await page.goto('/p/PHAROS/PHAROS-11')
  const list = relations(page)
  await list.getByRole('button', { name: 'Remove link: Blocked by PHAROS-14' }).click()
  const confirm = page.getByRole('dialog', { name: 'Remove the link to PHAROS-14?' })
  await expect(confirm).toContainText('“Blocked by PHAROS-14” goes from PHAROS-11 and from PHAROS-14.')
  await confirm.getByRole('button', { name: 'Cancel' }).click()
  expect(linkWrites(calls, 'DELETE')).toHaveLength(0)
  await list.getByRole('button', { name: 'Remove link: Blocked by PHAROS-14' }).click()
  await confirm.getByRole('button', { name: 'Remove link' }).click()
  await expect(list.getByRole('button', { name: /^Blocked by PHAROS-14/ })).toHaveCount(0)
  expect(linkWrites(calls, 'DELETE').map(call => call.path)).toEqual(['/api/relations/r-1'])
  // The neighbouring chip takes focus.
  await expect(list.getByRole('button', { name: /^Relates to PHAROS-15/ })).toBeFocused()
  await toast(page).filter({ hasText: 'PHAROS-14 no longer blocks PHAROS-11' }).getByRole('button', { name: 'Undo' }).click()
  await expect(list.getByRole('button', { name: /^Blocked by PHAROS-14/ })).toBeVisible()
  expect(linkWrites(calls, 'POST').at(-1)!.body).toEqual({ source_node_id: 'n-4', target_node_id: 'n-1', type: 'blocks' })
  // Delete on a focused chip removes it the same way.
  await list.getByRole('button', { name: /^Relates to PHAROS-15/ }).focus()
  await page.keyboard.press('Delete')
  await page.getByRole('dialog', { name: 'Remove the link to PHAROS-15?' }).getByRole('button', { name: 'Remove link' }).click()
  await expect(list.getByRole('button', { name: /^Relates to PHAROS-15/ })).toHaveCount(0)
  await expect(toast(page).filter({ hasText: 'PHAROS-11 no longer relates to PHAROS-15' })).toBeVisible()
  expect(errors).toEqual([])
})

test('a reader sees relations without Link or remove', async ({ page }) => {
  await mockWork(page, fixtures(), { readOnly: true })
  await page.goto('/p/PHAROS/PHAROS-11')
  await expect(relations(page).getByRole('button', { name: /^Blocked by PHAROS-14/ })).toBeVisible()
  await expect(relations(page).getByRole('button', { name: 'Link', exact: true })).toHaveCount(0)
  await expect(relations(page).getByRole('button', { name: /^Remove link/ })).toHaveCount(0)
  await panel(page).focus()
  await page.keyboard.press('r')
  await expect(page.locator('.floating')).toHaveCount(0)
  // A ticket with no links shows no empty section to a reader.
  await page.goto('/p/PHAROS/PHAROS-12')
  await expect(panel(page).getByRole('heading', { name: 'Add an Oracle Cloud connector' })).toBeVisible()
  await expect(page.getByRole('region', { name: 'Relations' })).toHaveCount(0)
})

test('on the full page r anchors the picker to the visible Link', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await mockWork(page, fixtures())
  await page.goto('/p/PHAROS/PHAROS-11?view=full')
  await expect(page.getByRole('heading', { name: 'Connect Hetzner Cloud for managed provisioning' })).toBeVisible()
  await expect(relations(page)).toHaveCount(1)
  await page.keyboard.press('r')
  const dialog = picker(page, 'PHAROS-11')
  await expect(dialog).toBeInViewport()
  const link = await relations(page).getByRole('button', { name: 'Link', exact: true }).boundingBox()
  const box = await dialog.boundingBox()
  expect(Math.abs(box!.y - (link!.y + link!.height + 6)) < 2 || box!.y + box!.height <= link!.y).toBe(true)
})

for (const colorScheme of ['light', 'dark'] as const) {
  test(`the picker and the list pass axe in ${colorScheme}, and fit a phone`, async ({ page }) => {
    const data = fixtures()
    data.nodes.find(node => node.id === 'n-4')!.title = 'Verlässliche Zusammenarbeit für österreichische Entwicklungsprojekte mit vollständiger Dokumentation und Freigaben'
    data.relations.push({ id: 'r-3', source_node_id: 'n-1', target_node_id: 'n-a1', type: 'implements', created_at: '2026-09-20T10:00:00Z' })
    await mockWork(page, data)
    await page.emulateMedia({ colorScheme, reducedMotion: 'reduce' })
    for (const width of [1440, 1024, 390]) {
      await page.setViewportSize({ width, height: 844 })
      await page.goto('/p/PHAROS/PHAROS-11')
      const list = relations(page)
      await expect(list.getByRole('button', { name: /^Implements AEON-1/ })).toBeVisible()
      await axe(page)
      if (width === 390) {
        // A finger's reach on every control, nothing clipped, no sideways scroll.
        for (const control of await list.getByRole('button').all()) expect((await control.boundingBox())!.height).toBeGreaterThanOrEqual(36)
        expect((await list.getByRole('button', { name: 'Link', exact: true }).boundingBox())!.height).toBeGreaterThanOrEqual(40)
      }
      await list.getByRole('button', { name: 'Link', exact: true }).click()
      const dialog = picker(page, 'PHAROS-11')
      await dialog.getByRole('radio', { name: 'Blocks' }).click()
      await dialog.getByRole('combobox').fill('PHAROS-14')
      await expect(dialog.getByRole('option')).toHaveCount(1)
      const read = dialog.getByRole('button', { name: /^Read full name: PHAROS-14/ })
      await expectStableControls({
        controls: { search: dialog.getByRole('combobox'), choice: dialog.getByRole('radio', { name: 'Blocks', exact: true }), option: dialog.getByRole('option'), read },
        scrollAreas: { options: dialog.locator('#relation-options') },
        interactions: [
          { name: 'read complete ticket name', run: async () => { await read.click(); await expect(dialog.getByRole('region', { name: 'Full name' })).toBeVisible() } },
          { name: 'close complete ticket name', run: async () => { await page.keyboard.press('Escape'); await expect(dialog.getByRole('region', { name: 'Full name' })).toHaveCount(0) } },
        ],
      })
      if (process.env.RELATION_SHOTS) {
        mkdirSync(process.env.RELATION_SHOTS, { recursive: true })
        await dialog.screenshot({ path: `${process.env.RELATION_SHOTS}/${width}-${colorScheme}.png` })
      }
      await dialog.getByRole('combobox').focus()
      await page.keyboard.press('Enter')
      await expect(dialog.getByRole('alert')).toContainText('would make a loop')
      await axe(page)
      const box = (await dialog.boundingBox())!
      expect(box.x).toBeGreaterThanOrEqual(0)
      expect(box.x + box.width).toBeLessThanOrEqual(width)
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width)
      if (width === 390) for (const radio of await dialog.getByRole('radio').all()) expect((await radio.boundingBox())!.height).toBeGreaterThanOrEqual(40)
      await page.keyboard.press('Escape')
    }
  })
}
