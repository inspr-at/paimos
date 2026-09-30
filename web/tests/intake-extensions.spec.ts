// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
import { join } from 'node:path'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { journeyWorld, mockJourney } from './journey-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'

const evidence = '  e\u0301\r\n<script>verbatim evidence</script>  '
const extensions = {
  'x-unregistered.constraints@1': { version: '1.2', data: { evidence, ready: false, score: 0 } },
  'x-unregistered.constraints@2': { version: '2.0', data: ['Later major', null] },
  [`x-demo.${'long-namespace-'.repeat(7)}end@1`]: { version: '1.0', data: { evidence: 'x'.repeat(2000) } },
}

for (const width of [1600, 390]) for (const theme of ['light', 'dark'] as const) {
  for (const stage of ['inspire', 'shape', 'requirements'] as const) {
    test(`${stage} retains extension versions at ${width} ${theme}`, async ({ page }) => {
      const errors = watchErrors(page)
      await page.setViewportSize({ width, height: 1000 })
      await page.emulateMedia({ colorScheme: theme })
      await mockWork(page, fixtures())
      const world = journeyWorld(stage)
      Object.assign(world.intake.drafts[0]!, { extensions })
      if (stage === 'requirements') Object.assign(world.intake.drafts[0]!, { kind: 'requirement', status: 'accepted', target_node_id: 'q-1' })
      await mockJourney(page, world)
      await page.goto('/p/PHAROS/journey')
      const block = page.getByRole('region', { name: 'Extension data' })
      await expect(block).toBeVisible()
      await expect(block.locator('details')).toHaveCount(3)
      const disclosure = block.getByLabel('x-unregistered.constraints · version 1.2', { exact: true })
      await disclosure.focus()
      await page.keyboard.press('Enter')
      const data = block.getByLabel('x-unregistered.constraints version 1.2 data', { exact: true })
      await expect(data).toBeVisible()
      expect(JSON.parse((await data.textContent())!)).toEqual(extensions['x-unregistered.constraints@1'].data)
      await expect(block.locator('script')).toHaveCount(0)
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
      const folder = process.env.EXTENSION_SHOTS
      if (folder) {
        await mkdir(folder, { recursive: true })
        await page.screenshot({ path: join(folder, `${stage}-${width}-${theme}.png`), fullPage: true, animations: 'disabled' })
        await block.screenshot({ path: join(folder, `${stage}-${width}-${theme}-data.png`), animations: 'disabled' })
      }
      expect(errors).toEqual([])
    })
  }
  test(`ticket shows originating extension data at ${width} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ colorScheme: theme })
    await mockWork(page, fixtures())
    await page.route('**/api/me/permissions?*', route => {
      const result = mockEffectivePermissions('admin', 'p-pharos')
      result.workspace.permissions.push('intake.read')
      return route.fulfill({ json: result })
    })
    let asked = ''
    await page.route('**/api/projects/p-pharos/intake?*', route => {
      asked = new URL(route.request().url()).searchParams.get('node_id') ?? ''
      return route.fulfill({ json: { sources: [], turns: [], drafts: [{ id: 'origin-draft', extensions }] } })
    })
    await page.goto('/p/PHAROS/PHAROS-12')
    const workspace = page.getByRole('complementary', { name: 'Ticket details' })
    const block = workspace.getByRole('region', { name: 'Extension data' })
    await expect(block).toBeVisible()
    expect(asked).toBe('n-2')
    await block.getByLabel('x-unregistered.constraints · version 1.2', { exact: true }).click()
    expect(JSON.parse((await block.locator('pre').first().textContent())!)).toEqual(extensions['x-unregistered.constraints@1'].data)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
    if (process.env.EXTENSION_SHOTS) {
      await mkdir(process.env.EXTENSION_SHOTS, { recursive: true })
      await page.screenshot({ path: join(process.env.EXTENSION_SHOTS, `ticket-${width}-${theme}.png`), fullPage: true, animations: 'disabled' })
      await block.screenshot({ path: join(process.env.EXTENSION_SHOTS, `ticket-${width}-${theme}-data.png`), animations: 'disabled' })
    }
  })
}

test('ticket extension reads respect intake permission', async ({ page }) => {
  await mockWork(page, fixtures(), { readOnly: true })
  let calls = 0
  await page.route('**/api/projects/*/intake?*', route => { calls++; return route.fulfill({ json: { sources: [], turns: [], drafts: [] } }) })
  await page.goto('/p/PHAROS/PHAROS-12')
  await expect(page.getByRole('complementary', { name: 'Ticket details' })).toBeVisible()
  await expect(page.getByRole('region', { name: 'Extension data' })).toHaveCount(0)
  expect(calls).toBe(0)
})
