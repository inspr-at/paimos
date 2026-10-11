// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1214: the header places end with the Decision Desk as a split place. Its
// name goes to the Desk; the chevron previews the Desk on every page. Risk: the
// preview moves header controls, hides behind the wrong count, or squeezes a
// phone header until the ticket key clips.
import { expect, test, type Page } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
import { join } from 'node:path'
import { expectStableControls } from './helpers/stable'
import { mockDecisionDesk } from './decision-desk-fixtures'
import { businessData, mockBusiness } from './business-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import type { DeskProjectionItem } from '../src/lib/decisionDesk'

async function setup(page: Page, options: Parameters<typeof mockDecisionDesk>[1] = {}) {
  const control = await mockDecisionDesk(page, options)
  await mockBusiness(page, businessData())
  await mockSettings(page, settingsData())
  return control
}
const places = (page: Page) => page.getByRole('navigation', { name: 'Places' })
const deskLink = (page: Page) => places(page).getByRole('link', { name: /^Decision Desk, / })
const chevron = (page: Page) => places(page).getByRole('button', { name: 'Preview the Decision Desk', exact: true })
const preview = (page: Page) => page.getByRole('dialog', { name: 'Decision Desk preview', exact: true })
const order = (page: Page) => page.evaluate(() => [...document.querySelectorAll<HTMLElement>('.places > [data-place]')].map(place => place.dataset.place))

for (const width of [1440, 1024, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`the Desk is the last place; its chevron previews the Desk without moving the header at ${width} ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 900 })
    await setup(page, { theme })
    await page.goto('/')
    await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
    expect(await order(page)).toEqual(['projects', 'agents', 'business', 'desk'])
    await expect(deskLink(page)).toHaveAccessibleName('Decision Desk, 5 open')
    await expect(deskLink(page)).toHaveAttribute('href', '/decision-desk')
    await expect(chevron(page)).toHaveAttribute('aria-haspopup', 'dialog')
    await expect(chevron(page)).toHaveAttribute('aria-expanded', 'false')
    if (width < 600) for (const target of [deskLink(page), chevron(page)]) {
      const box = (await target.boundingBox())!
      expect(Math.round(box.width), 'phone targets are 44 px wide').toBeGreaterThanOrEqual(44)
      expect(Math.round(box.height), 'phone targets are 44 px high').toBeGreaterThanOrEqual(44)
    }
    const dir = testInfo.outputPath('AEON-1214')
    await mkdir(dir, { recursive: true })
    await page.screenshot({ path: join(dir, `header-${width}-${theme}.png`), clip: { x: 0, y: 0, width, height: 120 } })
    const firstRow = preview(page).getByRole('link', { name: /^Approval · nodes\.read, / })
    await expectStableControls({
      controls: {
        places: places(page), desk: deskLink(page), chevron: chevron(page),
        search: page.getByRole('button', { name: 'Search everything' }), gear: page.getByRole('button', { name: /^App and workspace/ }),
      },
      interactions: [
        { name: 'open the preview', run: async () => {
          await chevron(page).click()
          await expect(preview(page)).toBeVisible()
          await expect(preview(page)).toContainText('5 things for you')
          await expect(firstRow).toBeFocused()
          // The tooltip never covers its own open preview.
          await expect(chevron(page)).not.toHaveAttribute('data-tip')
          await expect(chevron(page)).toHaveAttribute('aria-expanded', 'true')
        } },
        { name: 'close it with Escape', run: async () => {
          await page.keyboard.press('Escape')
          await expect(preview(page)).toBeHidden()
          await expect(chevron(page)).toBeFocused()
        } },
        { name: 'open it from the keyboard', run: async () => {
          await page.keyboard.press('ArrowDown')
          await expect(firstRow).toBeFocused()
        } },
      ],
    })
    // Held decisions come first, each row names its project, kind and age.
    await expect(preview(page).getByRole('region', { name: 'Waiting on you' }).getByRole('link')).toHaveCount(4)
    await expect(preview(page).getByRole('region', { name: 'When you’re ready' }).getByRole('link')).toHaveAccessibleName(/^Doctrine change, Paimos Aeon · Rule change · /)
    if (width < 600) {
      await expect(page.locator('.hm-panel.desk-panel')).toHaveClass(/sheet/)
      for (const row of await preview(page).getByRole('link').all()) expect((await row.boundingBox())!.height).toBeGreaterThanOrEqual(44)
      expect(await page.locator('.hm-panel.desk-panel').evaluate(el => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1)
    } else {
      // On wide screens it opens from the chevron, under the header, inside the window.
      const [panel, more, header] = await Promise.all([page.locator('.hm-panel.desk-panel').boundingBox(), chevron(page).boundingBox(), page.locator('.app-header').boundingBox()])
      expect(Math.abs(panel!.x - more!.x)).toBeLessThanOrEqual(0.5)
      expect(Math.abs(panel!.y - (header!.y + header!.height - 1))).toBeLessThanOrEqual(0.5)
      expect(panel!.x + panel!.width).toBeLessThanOrEqual(width - 12)
    }
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width)
    await page.screenshot({ path: join(dir, `preview-${width}-${theme}.png`) })
    // A row opens its item on the Desk, and the preview steps away.
    await firstRow.click()
    await expect(page).toHaveURL('/decision-desk?item=a:approval-1')
    await expect(preview(page)).toBeHidden()
    await expect(page.locator('.places .place.split')).toHaveClass(/active/)
    await page.screenshot({ path: join(dir, `desk-active-${width}-${theme}.png`), clip: { x: 0, y: 0, width, height: 120 } })
  })
}

test('the preview opens on every page and links to the whole Desk', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await setup(page)
  for (const path of ['/agents', '/business', '/settings/personal', '/p/PHAROS/PHAROS-11?view=full', '/decision-desk']) {
    await page.goto(path)
    await expect(chevron(page)).toBeVisible()
    expect(await order(page), path).toEqual(['projects', 'agents', 'business', 'desk'])
    await chevron(page).click()
    await expect(preview(page), path).toContainText('Waiting on you')
    await page.keyboard.press('Escape')
    await expect(preview(page)).toBeHidden()
  }
  await chevron(page).click()
  await preview(page).getByRole('link', { name: 'Open the Decision Desk' }).click()
  await expect(page).toHaveURL('/decision-desk')
  await expect(preview(page)).toBeHidden()
})

test('the preview says when it shows part of the Desk, nothing, or could not read it', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const extra: DeskProjectionItem[] = Array.from({ length: 15 }, (_, n) => ({
    id: `extra-${n}`, kind: 'question', project_id: 'p-aeon', revision: 1, title: `Extra question ${n + 1}`, created_at: '2026-10-02T09:00:00Z', held: false,
    href: `/decision-desk?item=q:extra-${n}`, source: `/api/questions/extra-${n}`,
  }))
  const control = await setup(page, { projectedSources: () => extra })
  await page.goto('/')
  await chevron(page).click()
  // The Desk's own total, never this list's length.
  await expect(preview(page)).toContainText('20 things for you')
  await expect(preview(page).getByRole('link', { name: /^(?!Open the Decision Desk)/ })).toHaveCount(12)
  await expect(preview(page)).toContainText('12 of 20 shown here.')
  await page.keyboard.press('Escape')

  await page.route('**/api/decision-desk/projection*', route => route.fulfill({ json: { items: [], counts: { open: 0, held: 0, chores: 0 }, has_more: false, as_of: new Date().toISOString() } }))
  await page.reload()
  await chevron(page).click()
  await expect(preview(page)).toContainText('Nothing for you')
  await expect(preview(page)).toContainText('Nothing waits on you.')
  await expect(preview(page).getByRole('link', { name: 'Open the Decision Desk' })).toBeFocused()
  await page.keyboard.press('Escape')

  await page.unroute('**/api/decision-desk/projection*')
  control.failProjection = true
  await page.reload()
  await chevron(page).click()
  await expect(preview(page)).toContainText('Count unavailable')
  await expect(preview(page).getByRole('alert')).toContainText('Decision Desk could not be read (403).')
  await expect(deskLink(page)).toHaveAccessibleName('Decision Desk, count unavailable')
  control.failProjection = false
  await preview(page).getByRole('button', { name: 'Retry', exact: true }).click()
  await expect(preview(page)).toContainText('20 things for you')
  await expect(deskLink(page)).toHaveAccessibleName('Decision Desk, 20 open')
})

// Measured, not guessed: on a phone the chevron steps aside before the moon when
// the room runs out, so the ticket key stays whole (AEON-312); the Desk stays a tap away.
for (const width of [375, 390]) test(`on a ${width} px phone the chevron gives way to a ticket key and returns on the home page`, async ({ page }) => {
  await page.setViewportSize({ width, height: 844 })
  await page.emulateMedia({ colorScheme: 'light' })
  await setup(page)
  await page.goto('/p/PHAROS/PHAROS-11?view=full')
  const key = page.getByRole('navigation', { name: 'Breadcrumb' }).locator('.crumb.current')
  await expect(key).toHaveText('PHAROS-11')
  await expect.poll(() => key.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
  await expect(deskLink(page)).toBeVisible()
  await expect(chevron(page)).toBeHidden()
  // It truly would not fit: the free room is less than its 44 px.
  expect(await page.locator('.app-header .spacer').evaluate(el => el.getBoundingClientRect().width)).toBeLessThan(44)
  expect(await page.locator('.app-header').evaluate(el => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(0)
  await places(page).getByRole('link', { name: 'Projects' }).click()
  await expect(page).toHaveURL('/')
  await expect(page.getByRole('button', { name: 'Switch to dark theme' })).toBeVisible()
  if (width === 390) {
    await expect(chevron(page)).toBeVisible()
    await chevron(page).click()
    await expect(preview(page)).toContainText('5 things for you')
  } else await expect(chevron(page)).toBeHidden()
  // Whatever stays, nothing runs past the header's edge.
  const reach = await page.locator('.app-header').evaluate(header => {
    const end = header.getBoundingClientRect().right - parseFloat(getComputedStyle(header).paddingRight)
    return Math.max(...[...header.children].map(child => child.getBoundingClientRect().right)) - end
  })
  expect(reach).toBeLessThanOrEqual(0.5)
})
