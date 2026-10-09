// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1057 risk: the person decides an item without seeing that its project
// changed, the project link opens in place of the desk, or P fires while typing.
import { expect, test, type Page } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
import { join } from 'node:path'
import { mockDecisionDesk } from './decision-desk-fixtures'
import { expectStableControls } from './helpers/stable'

const AEON = { name: 'Paimos Aeon', href: '/p/PRJ-35' }, PHAROS = { name: 'Pharos', href: '/p/PRJ-17' }
const notice = (now: string, before: string) => `Now in ${now}. The item before was in ${before}.`

// The round: approval (Aeon), question (Aeon), handover question (Pharos), action request (Aeon), rule change (Aeon, through its ticket).
async function openRound(page: Page, theme?: 'light' | 'dark') {
  const world = await mockDecisionDesk(page, { theme })
  world.questions[1]!.project_id = 'p-pharos'
  await page.addInitScript(() => {
    const opened: string[][] = []
    Object.assign(window, { __opened: opened })
    window.open = ((...args: unknown[]) => { opened.push(args.map(String)); return null }) as typeof window.open
  })
  await page.goto('/decision-desk')
  await expect(page.getByTestId('desk-row-q:question-2').getByTestId('desk-row-project')).toHaveText(PHAROS.name)
  await page.getByTestId('desk-row-a:approval-1').click()
  await expect(page.getByTestId('desk-paper').getByRole('heading', { level: 2 })).toHaveText('Allow nodes.read?')
  return world
}
const opened = (page: Page) => page.evaluate(() => (window as unknown as { __opened: string[][] }).__opened)
async function move(page: Page, key: 'j' | 'k', title: string) {
  await page.keyboard.press(key)
  await expect(page.getByTestId('desk-paper').getByRole('heading', { level: 2 })).toHaveText(title)
}

test('every kind names its project with a new-tab link; P opens it, but not while typing', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await openRound(page)
  const link = page.getByTestId('desk-project-link'), kinds: string[] = []
  for (const [at, project] of [AEON, AEON, PHAROS, AEON, AEON].entries()) {
    await expect(link).toHaveText(new RegExp(`^${project.name}`))
    await expect(link).toHaveAttribute('href', project.href)
    await expect(link).toHaveAttribute('target', '_blank')
    await expect(link).toHaveAttribute('rel', /\bnoopener\b/)
    await expect(page.getByTestId('desk-tab-project')).toHaveText(project.name)
    kinds.push(await page.locator('.pager-kind').innerText())
    if (at < 4) { const before = await page.getByTestId('desk-paper').getByRole('heading', { level: 2 }).innerText(); await page.keyboard.press('j'); await expect(page.getByTestId('desk-paper').getByRole('heading', { level: 2 })).not.toHaveText(before) }
  }
  expect(kinds).toEqual(['Approval', 'Question', 'Handover question', 'Action request', 'Rule change'])

  await page.keyboard.press('p')
  expect(await opened(page)).toEqual([[AEON.href, '_blank']])
  await move(page, 'j', 'Allow nodes.read?')
  await move(page, 'j', 'Which migration should carry the index?')
  const reason = page.locator('[data-field="reason"]')
  await reason.click(); await page.keyboard.type('plan')
  await expect(reason).toHaveValue('plan')
  expect(await opened(page), 'P while typing stays text').toEqual([[AEON.href, '_blank']])
  await page.keyboard.press('Escape'); await expect(reason).not.toBeFocused()
  await page.keyboard.press('p')
  expect(await opened(page)).toEqual([[AEON.href, '_blank'], [AEON.href, '_blank']])
})

test('the switch notice follows the item shown before, through J/K, the jump list and Decide & next', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await openRound(page)
  const switched = page.getByTestId('desk-project-switch'), chip = page.getByTestId('desk-tab-project')
  await expect(switched, 'the first item of a round never shows it').toHaveCount(0)
  await move(page, 'j', 'Which migration should carry the index?')
  await expect(switched, 'same project').toHaveCount(0)
  await expect(chip).not.toHaveClass(/switched/)
  await move(page, 'j', 'Should the successor continue the review?')
  await expect(switched).toHaveText(notice(PHAROS.name, AEON.name))
  await expect(chip).toHaveClass(/switched/)
  await expect(page.getByTestId('desk-announcement')).toContainText(notice(PHAROS.name, AEON.name))
  await move(page, 'j', 'An agent needs your steer')
  await expect(switched).toHaveText(notice(AEON.name, PHAROS.name))
  await move(page, 'k', 'Should the successor continue the review?')
  await expect(switched).toHaveText(notice(PHAROS.name, AEON.name))

  await page.getByTestId('desk-pager').click()
  await expect(page.getByTestId('desk-jump-head')).toContainText('· 2 projects')
  const names = await page.getByTestId('desk-jump-project').allInnerTexts()
  expect(names).toEqual([AEON.name, AEON.name, PHAROS.name, AEON.name, AEON.name])
  for (const [at, change] of [false, false, true, true, false].entries()) {
    if (change) await expect(page.getByTestId(`desk-jump-${at}`)).toHaveClass(/project-change/)
    else await expect(page.getByTestId(`desk-jump-${at}`)).not.toHaveClass(/project-change/)
  }
  await page.getByTestId('desk-jump-0').click()
  await expect(page.getByTestId('desk-paper').getByRole('heading', { level: 2 })).toHaveText('Allow nodes.read?')
  await expect(switched).toHaveText(notice(AEON.name, PHAROS.name))

  await move(page, 'j', 'Which migration should carry the index?')
  await expect(switched).toHaveCount(0)
  await page.getByTestId('desk-decide').click()
  await expect(page.getByTestId('desk-paper').getByRole('heading', { level: 2 })).toHaveText('Should the successor continue the review?')
  await expect(switched).toHaveText(notice(PHAROS.name, AEON.name))
})

test('the Agents desk panel names each item\'s project with a new-tab link', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const world = await mockDecisionDesk(page)
  world.questions[1]!.project_id = 'p-pharos'
  await page.goto('/agents')
  const panel = page.getByRole('region', { name: 'Decision Desk', exact: true })
  const row = panel.getByRole('listitem').filter({ hasText: 'Should the successor continue the review?' })
  const link = row.getByTestId('agents-desk-project')
  await expect(link).toHaveText(PHAROS.name)
  await expect(link).toHaveAttribute('href', PHAROS.href)
  await expect(link).toHaveAttribute('target', '_blank')
  await expect(link).toHaveAttribute('rel', /\bnoopener\b/)
  await row.getByRole('link', { name: 'Should the successor continue the review?' }).click()
  await expect(page).toHaveURL(/item=q:question-2$/)
})

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
  test(`the desk controls stay still across a change of project ${width} ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 900 })
    const world = await mockDecisionDesk(page, { theme })
    world.questions[1]!.project_id = 'p-pharos'
    world.questions[1]!.input.question = 'Soll die Nachfolgesitzung die Prüfung der mandantenbezogenen Berechtigungen fortsetzen?'
    const dir = testInfo.outputPath('aeon-1057'); await mkdir(dir, { recursive: true })
    await page.goto('/decision-desk')
    await expect(page.getByTestId('desk-row-q:question-2').getByTestId('desk-row-project')).toHaveText(PHAROS.name)
    await page.screenshot({ path: join(dir, `desk-list-${width}-${theme}.png`) })
    await page.getByTestId('desk-row-q:question-1').click()
    const title = page.getByTestId('desk-paper').getByRole('heading', { level: 2 })
    await expect(title).toHaveText('Which migration should carry the index?')
    await expectStableControls({
      controls: { pager: page.getByTestId('desk-pager'), decide: page.getByTestId('desk-decide'), skip: page.getByTestId('desk-skip'), close: page.getByTestId('desk-close'),
        stamps: page.getByTestId('desk-stamps'), once: page.getByTestId('stamp-once'), ...(width === 390 ? { frame: page.getByTestId('desk-frame') } : {}) },
      scrollAreas: { body: page.getByTestId('desk-body') },
      interactions: [
        { name: 'J into another project', run: async () => {
          await page.keyboard.press('j'); await expect(page.getByTestId('desk-project-switch')).toHaveText(notice(PHAROS.name, AEON.name))
          await page.screenshot({ path: join(dir, `desk-switch-${width}-${theme}.png`) })
        } },
        { name: 'J back into the first project', run: async () => {
          await page.keyboard.press('j'); await expect(page.getByTestId('desk-project-switch')).toHaveText(notice(AEON.name, PHAROS.name))
        } },
        { name: 'K returns to the other project', run: async () => {
          await page.keyboard.press('k'); await expect(page.getByTestId('desk-project-switch')).toHaveText(notice(PHAROS.name, AEON.name))
        } },
      ],
    })
    await page.getByTestId('desk-pager').click()
    await expect(page.getByTestId('desk-jump-head')).toContainText('· 2 projects')
    // A long title stays on one line, so the project column and every row keep their place.
    const titleHeight = await page.getByTestId('desk-jump-2').locator(':scope > span').nth(2).evaluate(element => element.getBoundingClientRect().height)
    expect(titleHeight).toBeLessThan(20)
    await page.screenshot({ path: join(dir, `desk-jump-${width}-${theme}.png`) })
  })
}
