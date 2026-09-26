// SPDX-License-Identifier: AGPL-3.0-only
// U25 (AEON-145): on wide screens a knowledge entry opens docked beside the list,
// like a ticket's side panel: selection in the address (?entry=), j and k move
// it, Esc closes it, Expand opens the entry's own page, and the divider keeps the
// width the person chose. Narrower screens keep opening the entry's own page.
import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { knowledgeWorld, mockKnowledge, type KnowledgeMockOptions } from './knowledge-fixtures'

const list = '/p/PHAROS/knowledge'
const pane = (page: Page) => page.locator('.entry-page.dock')
const row = (page: Page, title: string) => page.locator('.k-row').filter({ hasText: title })
async function open(page: Page, path: string, width = 1440, options: KnowledgeMockOptions = {}) {
  await page.setViewportSize({ width, height: 900 })
  const calls = await mockWork(page, fixtures())
  const knowledge = await mockKnowledge(page, knowledgeWorld(), options)
  await page.goto(path)
  return { calls, knowledge }
}

test('a click docks the entry beside the list, which stays usable; the selection is in the address', async ({ page }) => {
  const errors = watchErrors(page)
  await open(page, list)
  await row(page, 'Deploy a release to production').click()
  await expect(page).toHaveURL(`${list}?entry=runbook/deploy-release`)
  const dock = page.getByRole('complementary', { name: 'Runbook: Deploy a release to production' })
  await expect(dock).toBeVisible()
  await expect(dock.getByRole('heading', { level: 1 })).toHaveText('Deploy a release to production')
  await expect(dock.locator('.e-facts')).toContainText('deploy-release')
  await expect(dock.locator('.e-facts')).toContainText('camy, kite')
  await expect(dock.locator('.e-body')).toContainText('Revert the pin commit')
  // Status and the actions stay apart: status in the facts, actions in the bar.
  await expect(dock.locator('.e-facts')).toContainText('Active')
  await expect(dock.getByRole('button', { name: /^Edit/ })).toBeVisible()
  await expect(dock.getByRole('button', { name: 'Open as full page' })).toBeVisible()
  await expect(dock.getByRole('button', { name: 'Close the preview' })).toBeVisible()
  // The docked row is marked (tint and ring), the list keeps working beside it.
  await expect(row(page, 'Deploy a release to production')).toHaveAttribute('aria-current', 'true')
  await expect(row(page, 'Deploy a release to production')).toHaveClass(/open/)
  const box = (await page.locator('.k-list').boundingBox())!, dockBox = (await pane(page).boundingBox())!
  expect(box.x + box.width).toBeLessThanOrEqual(dockBox.x)
  expect(box.width).toBeGreaterThanOrEqual(520)
  expect(dockBox.width).toBeGreaterThanOrEqual(560)
  // Another row replaces it: back leaves the pane, not each step.
  await row(page, 'No coloured edge accents').click()
  await expect(page).toHaveURL(`${list}?entry=guideline/no-edge-accents`)
  await expect(pane(page).locator('.e-rule')).toContainText('Never mark selection')
  await page.goBack()
  await expect(page).toHaveURL(list)
  await expect(pane(page)).toHaveCount(0)
  await page.goForward()
  await expect(pane(page).getByRole('heading', { level: 1 })).toHaveText('No coloured edge accents')
  expect(errors).toEqual([])
})

test('j and k move the selection and the pane follows; Esc closes it and keeps the row', async ({ page }) => {
  await open(page, list)
  await expect(row(page, 'Deploy a release to production')).toBeVisible()
  await page.keyboard.press('j')
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(`${list}?entry=runbook/deploy-release`)
  await expect(pane(page).getByRole('heading', { level: 1 })).toHaveText('Deploy a release to production')
  await page.keyboard.press('j')
  await expect(page).toHaveURL(`${list}?entry=runbook/rotate-host-keys`)
  await expect(pane(page).getByRole('heading', { level: 1 })).toHaveText('Rotate the fleet host keys')
  await expect(row(page, 'Rotate the fleet host keys')).toHaveAttribute('aria-current', 'true')
  await page.keyboard.press('j')
  await expect(pane(page).getByRole('heading', { level: 1 })).toHaveText('No coloured edge accents')
  await page.keyboard.press('k')
  await expect(pane(page).getByRole('heading', { level: 1 })).toHaveText('Rotate the fleet host keys')
  // The pane's own previous and next follow the same order.
  await pane(page).getByRole('button', { name: 'Next entry' }).click()
  await expect(page).toHaveURL(`${list}?entry=guideline/no-edge-accents`)
  await page.keyboard.press('Escape')
  await expect(page).toHaveURL(`${list}?view=entries`)
  await expect(pane(page)).toHaveCount(0)
  await expect(row(page, 'No coloured edge accents')).toBeFocused()
  await expect(row(page, 'No coloured edge accents')).toHaveClass(/cursor/)
})

test('closing the pane leaves focus where the user moved it', async ({ page }) => {
  await open(page, `${list}?entry=guideline/no-edge-accents`)
  await expect(pane(page).getByRole('heading', { level: 1 })).toHaveText('No coloured edge accents')
  // The restore runs on the frame after the pane leaves. Move focus first, as a
  // keypress in that frame would, and the row must not take it back.
  await page.evaluate(() => {
    const native = window.requestAnimationFrame.bind(window)
    window.requestAnimationFrame = callback => native(() => {
      document.querySelector<HTMLInputElement>('input[type="search"]')?.focus()
      callback(performance.now())
    })
  })
  await pane(page).getByRole('button', { name: 'Close the preview' }).click()
  const search = page.getByRole('searchbox', { name: 'Search knowledge in Pharos' })
  await expect(search).toBeFocused()
  await page.evaluate(() => new Promise<void>(resolve => { requestAnimationFrame(() => requestAnimationFrame(() => resolve())) }))
  await expect(search).toBeFocused()
  await expect(pane(page)).toHaveCount(0)
})

test('the address restores the pane on reload, keeps it while the list is searched, and follows a rename', async ({ page }) => {
  await open(page, `${list}?entry=guideline/no-edge-accents`)
  await expect(pane(page).getByRole('heading', { level: 1 })).toHaveText('No coloured edge accents')
  await expect(row(page, 'No coloured edge accents')).toHaveClass(/cursor/)
  await page.reload()
  await expect(pane(page).getByRole('heading', { level: 1 })).toHaveText('No coloured edge accents')
  await page.getByRole('searchbox', { name: 'Search knowledge in Pharos' }).fill('deploy')
  await expect(page).toHaveURL(`${list}?view=entries&q=deploy&entry=guideline/no-edge-accents`)
  await expect(pane(page)).toBeVisible()
  // An old slug lands on the entry that took its place.
  await page.goto(`${list}?entry=runbook/deploy-flow`)
  await expect(page).toHaveURL(`${list}?entry=runbook/deploy-release`)
  await expect(pane(page).getByRole('heading', { level: 1 })).toHaveText('Deploy a release to production')
  await page.goto(`${list}?entry=runbook/nope`)
  await expect(pane(page).getByRole('heading', { name: 'No runbook called “nope” in Pharos' })).toBeVisible()
  await pane(page).getByRole('button', { name: 'Close', exact: true }).click()
  await expect(page).toHaveURL(`${list}?view=entries`)
})

test('Expand opens the entry’s own page; Esc there returns to the list with the pane', async ({ page }) => {
  await open(page, `${list}?entry=runbook/deploy-release`)
  await expect(pane(page).locator('.e-body')).toBeVisible()
  await pane(page).getByRole('button', { name: 'Open as full page' }).click()
  await expect(page).toHaveURL('/p/PHAROS/knowledge/runbook/deploy-release?view=entries')
  await expect(page.getByRole('navigation', { name: 'On this page' })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page).toHaveURL(`${list}?entry=runbook/deploy-release`)
  await expect(pane(page).getByRole('heading', { level: 1 })).toHaveText('Deploy a release to production')
})

test('editing in the pane is the entry page’s edit, with its save, rename and undo', async ({ page }) => {
  const { knowledge } = await open(page, `${list}?entry=guideline/plain-words`)
  await expect(pane(page).locator('.e-body')).toBeVisible()
  await page.keyboard.press('e')
  const form = pane(page).getByRole('form', { name: 'Edit plain-words' })
  await expect(form.getByLabel('Title')).toBeFocused()
  await form.getByLabel('Slug').fill('say-it-plainly')
  await expect(form.locator('.e-rename')).toContainText('paimos knowledge get guideline plain-words --project PHAROS')
  await form.getByRole('textbox', { name: 'Text, Markdown' }).fill('Name the thing, then the next step.')
  await page.keyboard.press('ControlOrMeta+Enter')
  await expect(page).toHaveURL(`${list}?entry=guideline/say-it-plainly`)
  await expect(pane(page).locator('.e-body')).toHaveText('Name the thing, then the next step.')
  await expect(row(page, 'Write for people, not for the log')).toContainText('say-it-plainly')
  expect(knowledge.find(call => call.method === 'PATCH')?.body).toEqual({ body: 'Name the thing, then the next step.', slug: 'say-it-plainly' })
  await page.getByRole('button', { name: 'Undo' }).click()
  await expect(page).toHaveURL(`${list}?entry=guideline/plain-words`)
  // Leaving an unsaved draft for another row asks first.
  await page.keyboard.press('e')
  await pane(page).getByRole('form', { name: 'Edit plain-words' }).getByLabel('Title').fill('Changed')
  await row(page, 'No coloured edge accents').click()
  const ask = page.getByRole('dialog', { name: 'Discard unsaved changes?' })
  await expect(ask).toBeVisible()
  await ask.getByRole('button', { name: 'Cancel' }).click()
  await expect(page).toHaveURL(`${list}?entry=guideline/plain-words`)
})

test('the divider resizes the pane and remembers the width; the list keeps its room', async ({ page }) => {
  const { calls } = await open(page, `${list}?entry=runbook/deploy-release`)
  await expect(pane(page).locator('.e-body')).toBeVisible()
  const before = (await pane(page).boundingBox())!.width
  const divider = page.getByRole('separator', { name: 'Resize the panel' })
  await divider.focus()
  await page.keyboard.press('Shift+ArrowLeft')
  await expect.poll(async () => (await pane(page).boundingBox())!.width).toBeGreaterThan(before + 40)
  await expect.poll(() => calls.some(call => call.path === '/api/preferences/layout' && call.method === 'PUT' && typeof (call.body as { value?: { knowledgePanel?: number } })?.value?.knowledgePanel === 'number')).toBe(true)
  // However far it is dragged, the list keeps 560px.
  for (let i = 0; i < 12; i++) await page.keyboard.press('Shift+ArrowLeft')
  await expect.poll(async () => (await page.locator('.k-list').boundingBox())!.width).toBeGreaterThanOrEqual(520)
  await page.keyboard.press('Home')
  await expect.poll(async () => Math.round((await pane(page).boundingBox())!.width)).toBe(Math.round(before))
})

test('below the docking width an entry opens its own page, and a docked link does too', async ({ page }) => {
  await open(page, list, 1100)
  await row(page, 'Deploy a release to production').click()
  await expect(page).toHaveURL('/p/PHAROS/knowledge/runbook/deploy-release')
  await expect(pane(page)).toHaveCount(0)
  await page.goto(`${list}?q=deploy&entry=runbook/deploy-release`)
  await expect(page).toHaveURL('/p/PHAROS/knowledge/runbook/deploy-release?q=deploy')
  await expect(page.getByRole('heading', { level: 1, name: 'Deploy a release to production' })).toBeVisible()
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto(`${list}?entry=guideline/no-edge-accents`)
  await expect(page).toHaveURL('/p/PHAROS/knowledge/guideline/no-edge-accents')
})

test('the docking edge: from 1200px the pane opens; at 1199px the page does', async ({ page }) => {
  await open(page, `${list}?entry=runbook/deploy-release`, 1200)
  await expect(pane(page)).toBeVisible()
  expect((await page.locator('.k-list').boundingBox())!.width).toBeGreaterThanOrEqual(520)
  await page.setViewportSize({ width: 1199, height: 900 })
  await expect(page).toHaveURL('/p/PHAROS/knowledge/runbook/deploy-release?view=entries')
})

for (const scheme of ['light', 'dark'] as const) {
  test(`the docked entry passes axe in ${scheme}`, async ({ page }) => {
    await page.emulateMedia({ colorScheme: scheme })
    await open(page, `${list}?entry=runbook/deploy-release`)
    await expect(pane(page).locator('.e-body')).toBeVisible()
    const scan = async () => {
      const result = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).exclude('.version-coordinate').exclude('.calendar-version').analyze()
      expect(result.violations.map(v => `${v.id}: ${v.nodes.map(n => n.target.join(' ')).join(', ')}`)).toEqual([])
    }
    await scan()
    await page.goto(`${list}?status=all&entry=memory/hetzner-token-expiry`)
    await expect(pane(page).locator('.e-note.proposed')).toBeVisible()
    await scan()
    await page.keyboard.press('e')
    await expect(pane(page).getByRole('form')).toBeVisible()
    await scan()
  })
}
