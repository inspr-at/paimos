// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { resolve } from 'node:path'
import { expect, test, type Locator } from '@playwright/test'
import { fixtures, liveAgent, mockWork } from './work-fixtures'

const shots = resolve('../.agent-shots')
const longName = 'Alexandria-the-project-coordinator-with-a-long-name'
const longKey = 'AEON-LONG-PROJECT-202'

async function contained(chip: Locator, labels = true) {
  const geometry = await chip.evaluate(element => {
    const box = element.getBoundingClientRect()
    const children = [...element.querySelectorAll('.face, .count, .name, .key, .chip-state')].filter(el => el.getBoundingClientRect().width)
    return {
      width: box.width,
      children: children.map(el => {
        const rect = el.getBoundingClientRect(), css = getComputedStyle(el)
        return { kind: el.classList.contains('live-bot') ? 'face' : el.className, left: rect.left - box.left, right: box.right - rect.right, width: rect.width, clipped: el.scrollWidth > el.clientWidth, ellipsis: css.textOverflow }
      }),
    }
  })
  for (const child of geometry.children) {
    expect(child.left, `${child.kind} inside left inset`).toBeGreaterThanOrEqual(5)
    expect(child.right, `${child.kind} inside right inset`).toBeGreaterThanOrEqual(7)
    if (child.kind.includes('chip-state')) expect(child.clipped).toBe(false)
    if (child.kind.includes('name') || child.kind.includes('key')) {
      expect(child.width).toBeGreaterThan(12)
      expect(child.ellipsis).toBe('ellipsis')
    }
  }
  if (labels) {
    await expect(chip.locator('.name')).toContainText(longName)
    await expect(chip.locator('.key')).toContainText(longKey)
    await expect(chip.locator('.chip-state')).toHaveText('Working')
  }
  return geometry
}

for (const theme of ['light', 'dark'] as const) {
  for (const style of ['robot-1', 'robot-5']) {
    test(`chip content at 260–420px and list widths: ${theme}, ${style}`, async ({ page }) => {
      mkdirSync(shots, { recursive: true })
      await page.clock.setSystemTime(new Date('2026-09-23T12:00:00Z'))
      await page.emulateMedia({ colorScheme: theme })
      await page.setViewportSize({ width: 1440, height: 1100 })
      const data = fixtures()
      data.preferences.projects = { view: 'cards' }
      data.preferences['agent-indicator'] = { style, hovering: false }
      data.projects = [1, 2, 4].map(count => ({ ...data.projects[0]!, id: `p-layout-${count}`, key: `PRJ-${count}`, classic: `LIVE${count}`, title: `${count} agents · long labels`, last: '2026-09-23T11:57:00Z' }))
      data.live = [1, 2, 4].flatMap(count => Array.from({ length: count }, (_, i) => liveAgent({ project_id: `p-layout-${count}`, session_id: `session-${count}-${i}`, principal_id: `44444444-4444-4444-8444-44444444444${i}`, name: `${longName}-${i}`, ticket: { id: `ticket-${i}`, key: longKey, title: 'Layout regression', project_id: `p-layout-${count}` } }, 10)))
      await mockWork(page, data)
      await page.goto('/')
      await expect(page.locator('.card .live-chip')).toHaveCount(3)
      await page.addStyleTag({ content: '.card-grid { grid-template-columns: repeat(3, var(--test-card-width)) !important; } .card { width: var(--test-card-width) !important; }' })
      for (const width of [260, 280, 300, 320, 340, 360, 380, 400, 420]) {
        await page.evaluate(width => document.documentElement.style.setProperty('--test-card-width', `${width}px`), width)
        for (const count of [1, 2, 4]) {
          const card = page.locator(`.card[data-project-id="p-layout-${count}"]`)
          const chip = card.locator('.live-chip')
          expect((await card.boundingBox())!.width).toBe(width)
          await contained(chip)
          const box = (await chip.boundingBox())!, time = (await card.locator('.activity').boundingBox())!
          expect(time.x - box.x - box.width).toBeGreaterThanOrEqual(9.9)
          await expect(card.locator('.activity')).toHaveText('3 min ago')
        }
        if ([260, 340, 420].includes(width)) await page.locator('.cards-view').screenshot({ path: resolve(shots, `iv1-chips-${theme}-${style}-${width}.png`) })
      }
      await page.getByRole('radio', { name: 'List view' }).click()
      for (const width of [1440, 900, 420, 390, 320]) {
        await page.setViewportSize({ width, height: 1100 })
        for (const count of [1, 2, 4]) {
          const row = page.locator(`.project-item[data-project-id="p-layout-${count}"]`)
          const chip = row.locator('.live-chip')
          await contained(chip, false)
          const box = (await chip.boundingBox())!, bounds = (await row.boundingBox())!
          expect(box.x + box.width).toBeLessThanOrEqual(bounds.x + bounds.width)
        }
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
        if ([1440, 390].includes(width)) await page.screenshot({ path: resolve(shots, `iv1-rows-${theme}-${style}-${width}.png`), fullPage: true })
      }
    })
  }
}
