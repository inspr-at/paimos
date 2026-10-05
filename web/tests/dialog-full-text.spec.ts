// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { expectStableControls } from './helpers/stable'
import type { FloatingList } from './helpers/floating-lists'

const name = 'Projektübergreifende Entwicklungszusammenarbeit und Qualitätsverantwortung'
const shots = join(process.cwd(), 'test-results', 'aeon-624', 'r5')

async function openList(page: Page, kind: FloatingList, theme: 'light' | 'dark') {
  const data = fixtures()
  data.preferences.theme = { choice: theme }
  const epic = data.nodes.find(node => node.kind_slug === 'epic')!
  epic.title = `${name} 1`
  data.nodes.push(...Array.from({ length: 16 }, (_, index) => ({ ...epic, id: `extra-epic-${index}`, key: `PHAROS-${index + 100}`, title: `${name} ${index + 1}`, state: 'open' })))
  await mockWork(page, data)
  await page.goto('/p/PHAROS')
  await expect(page.getByRole('heading', { name: 'Pharos', exact: true })).toBeVisible()
  await page.keyboard.press('ArrowRight')
  await page.evaluate(async kind => {
    const { mountFloatingList } = await import(/* @vite-ignore */ '/tests/helpers/floating-lists.ts')
    mountFloatingList(kind, true)
  }, kind)
  if (kind === 'facet') await page.getByRole('button', { name: 'Choices', exact: true }).click()
  const panel = page.locator('.floating[role="dialog"]')
  await expect(panel).toBeVisible()
  return panel
}

for (const width of [390, 1024]) for (const theme of ['light', 'dark'] as const) {
  test(`long name disclosure can be read and exited by keyboard at ${width} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 844 })
    const errors = watchErrors(page)
    await openList(page, 'option', theme)
    await page.evaluate(async text => {
      const { mountFloatingList } = await import(/* @vite-ignore */ '/tests/helpers/floating-lists.ts')
      mountFloatingList('option', text)
    }, name.repeat(100))
    const panel = page.locator('.floating[role="dialog"]')
    const read = panel.getByRole('button', { name: /^Read full name:/ }).first()
    const text = (await read.getAttribute('aria-label'))!.replace(/^Read full name: /, '')
    const disclosure = page.getByRole('region', { name: 'Full name', exact: true })
    await page.keyboard.press('ArrowRight')
    await read.focus()
    await expect(disclosure).toHaveText(text)
    expect(await disclosure.evaluate(el => el.scrollHeight - el.clientHeight)).toBeGreaterThan(1)
    const bounds = (await disclosure.boundingBox())!
    expect(bounds.x).toBeGreaterThanOrEqual(0)
    expect(bounds.y).toBeGreaterThanOrEqual(0)
    expect(bounds.x + bounds.width).toBeLessThanOrEqual(width)
    expect(bounds.y + bounds.height).toBeLessThanOrEqual(844)
    await expectStableControls({
      controls: { panel, read, row: panel.getByRole('menuitemradio').first(), search: panel.locator('input'), group: panel.locator('.menu') },
      scrollAreas: { panel, disclosure },
      interactions: [{ name: 'read overflowing text with the keyboard', run: async () => {
        await read.press('Tab')
        await expect(disclosure).toBeFocused()
        await disclosure.press('End')
        await expect.poll(() => disclosure.evaluate(el => el.scrollTop)).toBeGreaterThan(0)
        await expect(disclosure).toHaveText(text)
        await disclosure.press('Shift+Tab')
        await expect(read).toBeFocused()
        await expect(disclosure).toHaveText(text)
      } }],
    })
    mkdirSync(shots, { recursive: true })
    await page.screenshot({ path: join(shots, `long-name-${width}-${theme}.png`) })
    await read.press('Tab')
    await expect(disclosure).toBeFocused()
    await disclosure.press('Tab')
    await expect(panel).toHaveCount(0)
    await expect(disclosure).toHaveCount(0)
    expect(await events(page)).toEqual([])
    expect(errors).toEqual([])
  })
}

async function events(page: Page) {
  return page.evaluate(() => (window as unknown as { __floatingEvents: unknown[] }).__floatingEvents)
}

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
  test.describe(`${width} ${theme}`, () => {
    test.use({ viewport: { width, height: 1000 }, colorScheme: theme })

    for (const kind of ['facet', 'group', 'business'] as const) {
      test(`keyboard full text: ${kind}`, async ({ page }) => {
        const errors = watchErrors(page)
        const panel = await openList(page, kind, theme)
        const search = panel.locator('input:not([type="checkbox"])')
        await expect(search).toBeFocused()
        const rows = panel.locator(kind === 'facet' ? '.option' : '[role="option"]')
        const first = rows.first(), second = rows.nth(1)
        const firstName = first.locator(kind === 'facet' ? '.option-label' : kind === 'group' ? '.name' : '.opt-label')
        // Main wraps phone group names to two lines; clipping can occur on either axis.
        expect(await firstName.evaluate(el => Math.max(el.scrollWidth - el.clientWidth, el.scrollHeight - el.clientHeight)), 'the keyboard regression must exercise a clipped name').toBeGreaterThan(1)
        const controls = { panel, search, group: panel.locator('.options, .picker-list'), first, second }
        await expectStableControls({ controls, scrollAreas: { panel }, interactions: [
          { name: 'navigate to a clipped name', run: async () => {
            await search.press('ArrowDown')
            if (kind === 'facet') {
              await expect(first.getByRole('checkbox')).toBeFocused()
              await expect(page.locator('.tooltip')).toHaveText(`${name} 1`)
            } else {
              await expect(search).toBeFocused()
              await expect(second).toHaveAttribute('aria-selected', 'true')
              await expect(search).toHaveAttribute('aria-activedescendant', await second.getAttribute('id') as string)
              await expect(page.locator('.tooltip')).toHaveText(`${name} 2`)
            }
          } },
          { name: 'navigate again without selecting', run: async () => {
            if (kind === 'facet') {
              await first.getByRole('checkbox').press('ArrowDown')
              await expect(second.getByRole('checkbox')).toBeFocused()
              await expect(page.locator('.tooltip')).toHaveText(`${name} 2`)
            } else {
              await search.press('ArrowUp')
              await expect(search).toBeFocused()
              await expect(first).toHaveAttribute('aria-selected', 'true')
              await expect(search).toHaveAttribute('aria-activedescendant', await first.getAttribute('id') as string)
              await expect(page.locator('.tooltip')).toHaveText(`${name} 1`)
            }
          } },
        ] })
        expect(await events(page)).toEqual([])
        mkdirSync(shots, { recursive: true })
        await page.screenshot({ path: join(shots, `keyboard-${kind}-${width}-${theme}.png`) })
        expect(errors).toEqual([])
      })
    }

    for (const kind of ['group', 'epic', 'option', 'relation', 'business'] as const) {
      test(`touch full text before closing selection: ${kind}`, async ({ browser }) => {
        const context = await browser.newContext({ viewport: { width, height: 1000 }, hasTouch: true, colorScheme: theme, reducedMotion: 'reduce' })
        const page = await context.newPage()
        const errors = watchErrors(page)
        try {
          const panel = await openList(page, kind, theme)
          const row = panel.locator('[role="option"], [role="menuitemradio"]').first()
          const read = panel.getByRole('button', { name: /^Read full name:/ }).first()
          await expect(read).toBeVisible()
          const text = (await read.getAttribute('aria-label'))!.replace(/^Read full name: /, '')
          expect(text).toContain(`${name} 1`)
          const nameElement = row.locator('.name, .title, .label, .opt-label')
          await expect(nameElement).toHaveAttribute('data-clip-tip', '')
          const readBox = (await read.boundingBox())!, rowBox = (await row.boundingBox())!
          expect(readBox.width).toBeGreaterThanOrEqual(44)
          expect(readBox.height).toBeGreaterThanOrEqual(44)
          expect(rowBox.height).toBeGreaterThanOrEqual(44)
          expect(rowBox.x + rowBox.width).toBeLessThanOrEqual(readBox.x + .5)
          await expectStableControls({
            controls: { panel, row, read, search: panel.locator('input:not([type="checkbox"])'), group: panel.locator('.options, .menu, .picker-list') },
            scrollAreas: { panel },
            interactions: [{ name: 'tap the separate name disclosure', run: async () => {
              await read.tap()
              await expect(page.getByRole('region', { name: 'Full name', exact: true })).toHaveText(text as string)
              expect(await events(page)).toEqual([])
              await expect(panel).toBeVisible()
            } }, { name: 'read it again without committing a choice', run: async () => {
              await read.tap()
              await expect(page.getByRole('region', { name: 'Full name', exact: true })).toHaveText(text as string)
              expect(await events(page)).toEqual([])
            } }, { name: 'native disclosure keys do not select the row', run: async () => {
              await expect(read).toBeFocused()
              for (const key of ['Enter', 'Space']) {
                await read.press(key)
                await expect(page.getByRole('region', { name: 'Full name', exact: true })).toHaveText(text as string)
                expect(await events(page)).toEqual([])
                await expect(panel).toBeVisible()
              }
            } }, { name: 'Escape closes only the name disclosure', run: async () => {
              await read.press('Escape')
              await expect(page.getByRole('region', { name: 'Full name', exact: true })).toHaveCount(0)
              await expect(read).toBeFocused()
              await expect(panel).toBeVisible()
              expect(await events(page)).toEqual([])
              await read.press('Enter')
              await expect(page.getByRole('region', { name: 'Full name', exact: true })).toHaveText(text as string)
            } }],
          })
          mkdirSync(shots, { recursive: true })
          await page.screenshot({ path: join(shots, `touch-${kind}-${width}-${theme}.png`) })
          await row.tap()
          await expect.poll(async () => (await events(page)).length).toBe(1)
          const selected = (await events(page))[0]
          if (kind === 'group' || kind === 'option') expect(selected).toBe('choice-0')
          else if (kind === 'business') expect(selected).toMatchObject({ value: 'choice-0', label: `${name} 1` })
          else if (kind === 'epic') expect(selected).toMatchObject({ title: `${name} 1` })
          else expect((selected as unknown[])[1]).toMatchObject({ key: 'PHAROS-100', title: `${name} 1` })
          await expect(panel).toHaveCount(0)
          await expect(page.locator('.tooltip')).toHaveCount(0)
          await expect(page.getByRole('region', { name: 'Full name', exact: true })).toHaveCount(0)
          expect(errors).toEqual([])
        } finally { await context.close() }
      })
    }
  })
}
