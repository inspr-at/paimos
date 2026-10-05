// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Locator } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { expectStableControls } from './helpers/stable'

async function expectHeaderFits(panel: Locator) {
  const header = panel.locator('.panel-bar')
  const measurements = await header.evaluate(element => {
    const frame = element.getBoundingClientRect()
    return [...element.querySelectorAll('button')].flatMap(button => {
      const box = button.getBoundingClientRect()
      return box.width && box.height ? [{ name: button.getAttribute('aria-label') ?? button.textContent,
        left: box.left - frame.left, right: frame.right - box.right }] : []
    })
  })
  expect(measurements.length).toBeGreaterThan(0)
  for (const button of measurements) {
    expect(button.left, `${button.name} starts inside header`).toBeGreaterThanOrEqual(-.5)
    expect(button.right, `${button.name} ends inside header`).toBeGreaterThanOrEqual(-.5)
  }
}

for (const width of [390, 600, 720, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
  test(`work actions fit the ticket panel at ${width} ${theme}`, async ({ page }, testInfo) => {
    const errors = watchErrors(page)
    await page.setViewportSize({ width, height: width <= 600 ? 844 : 1000 })
    const data = fixtures()
    for (const node of data.nodes) node.kind_slug = 'work'
    const target = data.nodes.find(node => node.id === 'n-1')!
    target.title = 'Langfristige Bereitstellung der vollständig dokumentierten Arbeitsaufträge mit sicherer Übergabe'
    data.preferences.theme = { choice: theme }
    await mockWork(page, data)
    await page.route('**/api/nodes/n-1/work-lifecycle', route => route.fulfill({ json: {
      is_leaf: true, busy: false, open_leaves: 1, updated_at: target.updated_at,
      scope_revision: 'a'.repeat(64), pending: null,
    } }))
    await page.goto('/p/PHAROS')
    await page.locator('#row-n-1 .title-text').click()
    const panel = page.getByRole('complementary', { name: 'Ticket details' })
    const header = panel.locator('.panel-bar')
    const more = header.getByRole('button', { name: 'More actions', exact: true })
    const controls: Record<string, Locator> = {
      more, edit: header.getByRole('button', { name: 'Edit', exact: true }),
      close: header.getByRole('button', { name: 'Close ticket details', exact: true }),
      key: header.getByRole('button', { name: 'Copy PHAROS-11', exact: true }),
    }
    if (width > 600) {
      controls.previous = header.getByRole('button', { name: 'Previous ticket', exact: true })
      controls.next = header.getByRole('button', { name: 'Next ticket', exact: true })
    }
    const headerWidth = await header.evaluate(el => {
      const style = getComputedStyle(el)
      return el.clientWidth - parseFloat(style.paddingLeft) - parseFloat(style.paddingRight)
    })
    const wideActions = width > 720 && headerWidth > 420
    if (wideActions) {
      controls.expand = header.getByRole('button', { name: 'Open as full page', exact: true })
      controls.newTab = header.getByRole('button', { name: 'Open in a new tab', exact: true })
    }
    await expect(panel.getByRole('heading', { name: target.title })).toBeVisible()
    await page.screenshot({ path: testInfo.outputPath('header.png') })
    // Exercise the actual drawer, including its list navigation controls.
    // Full-page lifecycle tests alone cannot catch a crowded panel header.
    await expectStableControls({ controls, scrollAreas: { panel, header }, interactions: [{
      name: 'open More actions', run: async () => {
        await more.click()
        await expect(page.getByRole('menuitem', { name: 'Work actions', exact: true })).toBeVisible()
        for (const name of ['Open as full page', 'Open in a new tab']) {
          await expect(page.getByRole('menuitem', { name, exact: true })).toBeVisible()
          if (!wideActions) await expect(header.getByRole('button', { name, exact: true })).toBeHidden()
        }
        await expectHeaderFits(panel)
      },
    }, {
      name: 'open and close the lifecycle sheet', run: async () => {
        await page.getByRole('menuitem', { name: 'Work actions', exact: true }).click()
        const sheet = page.getByRole('dialog', { name: 'Work actions', exact: true })
        await expect(sheet.getByText('This leaf becomes a parent.', { exact: false })).toBeVisible()
        await expect(page.getByRole('menu')).toHaveCount(0)
        await sheet.getByRole('button', { name: /^Close/ }).click()
        await expect(sheet).toBeHidden()
        await expect(more).toBeFocused()
        await expectHeaderFits(panel)
      },
    }] })
    await more.click()
    await page.screenshot({ path: testInfo.outputPath('more-actions.png') })
    await page.getByRole('menuitem', { name: 'Work actions', exact: true }).press('Enter')
    const sheet = page.getByRole('dialog', { name: 'Work actions', exact: true })
    await expect(sheet).toBeVisible()
    await sheet.press('Escape')
    await expect(sheet).toBeHidden()
    await expect(more).toBeFocused()
    expect(errors).toEqual([])
  })
}
