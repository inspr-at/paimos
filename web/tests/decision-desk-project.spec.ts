// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1057 risk: the person decides an item without seeing that its project
// changed, the project link opens in place of the desk, or P fires while typing.
// AEON-1339 (Markus 10-14): the project appears once, without a dot: its key on the
// folder tab (filled when it changed), its name with the folder icon in the meta.
import { expect, test, type Page } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
import { join } from 'node:path'
import { mockDecisionDesk } from './decision-desk-fixtures'
import { expectStableControls } from './helpers/stable'
// AEON-1209: the Agents page no longer carries the Desk panel; its harness keeps the panel covered until the header preview (AEON-1214).
const DESK_PANEL = '/tests/desk-panel-harness.html'

const AEON = { name: 'Paimos Aeon', key: 'PRJ-35', href: '/p/PRJ-35' }, PHAROS = { name: 'Pharos', key: 'PRJ-17', href: '/p/PRJ-17' }
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
    await expect(page.getByTestId('desk-tab-project')).toHaveText(project.key)
    await expect(page.locator('.desk-dlg .project-dot, .desk-dlg i[aria-hidden="true"]')).toHaveCount(0)
    kinds.push(await page.locator('.ft-kind').innerText())
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

test('the filled key chip follows the item shown before, through J/K, the jump list and Decide & next', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await openRound(page)
  const chip = page.getByTestId('desk-tab-project')
  // Rev 12's extra "Now in" sentence is dropped (Tick 10-14); screen readers still hear the change.
  await expect(page.getByTestId('desk-project-switch')).toHaveCount(0)
  await expect(chip, 'the first item of a round is never marked').not.toHaveClass(/switched/)
  await move(page, 'j', 'Which migration should carry the index?')
  await expect(chip, 'same project').not.toHaveClass(/switched/)
  await move(page, 'j', 'Should the successor continue the review?')
  await expect(chip).toHaveText(PHAROS.key)
  await expect(chip).toHaveClass(/switched/)
  await expect(page.getByTestId('desk-announcement')).toContainText(notice(PHAROS.name, AEON.name))
  await move(page, 'j', 'The reviewer needs guidance before trying again.')
  await expect(chip).toHaveText(AEON.key)
  await expect(chip).toHaveClass(/switched/)
  await move(page, 'k', 'Should the successor continue the review?')
  await expect(chip).toHaveClass(/switched/)

  await page.getByTestId('desk-pager').click()
  const names = await page.getByTestId('desk-jump-project').allInnerTexts()
  expect(names).toEqual([`${AEON.key} · Approval`, `${AEON.key} · Question`, `${PHAROS.key} · Handover question`, `${AEON.key} · Action request`, `${AEON.key} · Rule change`])
  await page.getByTestId('desk-jump-0').click()
  await expect(page.getByTestId('desk-paper').getByRole('heading', { level: 2 })).toHaveText('Allow nodes.read?')
  await expect(chip).toHaveClass(/switched/)

  await move(page, 'j', 'Which migration should carry the index?')
  await expect(chip).not.toHaveClass(/switched/)
  await page.getByTestId('desk-decide').click()
  await expect(page.getByTestId('desk-paper').getByRole('heading', { level: 2 })).toHaveText('Should the successor continue the review?')
  await expect(chip).toHaveText(PHAROS.key)
  await expect(chip).toHaveClass(/switched/)
})

test('the Agents desk panel names each item\'s project with a new-tab link', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const world = await mockDecisionDesk(page)
  world.questions[1]!.project_id = 'p-pharos'
  await page.goto(DESK_PANEL)
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

test('revisiting a question after Decide & next retains its project link, colour and P action', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await openRound(page)
  await move(page, 'j', 'Which migration should carry the index?')
  const link = page.getByTestId('desk-project-link')
  const color = await link.evaluate(element => getComputedStyle(element).color)
  await page.getByTestId('desk-decide').click()
  await expect(page.getByTestId('desk-paper').getByRole('heading', { level: 2 })).toHaveText('Should the successor continue the review?')
  await move(page, 'k', 'Which migration should carry the index?')
  await expect(page.getByTestId('desk-decide')).toContainText('Next')
  await expect(page.getByTestId('desk-stamp')).toHaveAttribute('aria-label', 'Stamped Once')
  await expect(link).toHaveAttribute('href', AEON.href)
  expect(await link.evaluate(element => getComputedStyle(element).color)).toBe(color)
  await page.keyboard.press('p')
  expect(await opened(page)).toEqual([[AEON.href, '_blank']])
})

test('the Agents doctrine row resolves its project through the authorized ticket lookup', async ({ page }, testInfo) => {
  for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
    await page.setViewportSize({ width, height: 1000 })
    const world = await mockDecisionDesk(page, { theme })
    world.rule.ticket = 'AEON-1'
    await page.goto(DESK_PANEL)
    const panel = page.getByRole('region', { name: 'Decision Desk', exact: true })
    const row = panel.getByRole('listitem').filter({ hasText: 'Doctrine change' })
    const link = row.getByTestId('agents-desk-project')
    await expect(link).toHaveText(AEON.name)
    await expect(link).toHaveAttribute('href', AEON.href)
    await expect(link).toHaveAttribute('target', '_blank')
    await expect(link).toHaveAttribute('rel', /\bnoopener\b/)
    expect(world.reads.some(path => path.startsWith('/api/nodes/lookup?') && path.includes('AEON-1'))).toBe(true)
    await expectStableControls({
      controls: { review: panel.getByTestId('agents-desk-review'), history: panel.getByTestId('agents-desk-history'), project: link, row },
      interactions: [{ name: 'hover the doctrine project', run: async () => { await link.hover() } }],
    })
    await panel.screenshot({ path: testInfo.outputPath(`agents-project-${width}-${theme}.png`) })
    await row.getByRole('link', { name: 'Doctrine change', exact: true }).click()
    await expect(page).toHaveURL(/item=r:rule-1$/)
  }
})

// Risk: a poll removes a project link under the pointer while its ticket is read again.
test('the Agents doctrine project link stays put through a second desk refresh without repeated lookups', async ({ page }, testInfo) => {
  for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
    await page.setViewportSize({ width, height: 1000 })
    const world = await mockDecisionDesk(page, { theme })
    await page.goto(DESK_PANEL)
    const panel = page.getByRole('region', { name: 'Decision Desk', exact: true })
    const row = panel.getByRole('listitem').filter({ hasText: 'Doctrine change' })
    const link = row.getByTestId('agents-desk-project'), title = row.getByRole('link', { name: 'Doctrine change', exact: true })
    await expect(link).toHaveAttribute('href', AEON.href)
    const lookups = world.reads.filter(path => path.startsWith('/api/nodes/lookup?')).length
    let repeatedInbox = 0, repeatedLookup = 0, release!: () => void
    const held = new Promise<void>(resolve => { release = resolve })
    // Hold redundant reads, so the old link cannot disappear and return between samples.
    await page.route('**/api/rules/doctrine/inbox', async route => { repeatedInbox++; await held; await route.fallback() })
    await page.route('**/api/nodes/lookup?**', async route => { repeatedLookup++; await held; await route.fallback() })
    try {
      await expectStableControls({
        controls: { review: panel.getByTestId('agents-desk-review'), history: panel.getByTestId('agents-desk-history'), project: link, title, row },
        scrollAreas: { panel },
        interactions: [{ name: 'refresh the same doctrine row', run: async () => {
          await page.evaluate(async () => {
            const app = document.querySelector('#app') as HTMLElement & { __vue_app__: { config: { globalProperties: { $pinia: { _s: Map<string, { refresh: () => Promise<void> }> } } } } }
            await app.__vue_app__.config.globalProperties.$pinia._s.get('decisionDesk')!.refresh()
          })
          expect(await link.count(), 'the resolved project stays present while redundant reads are held').toBe(1)
          await expect(link).toHaveAttribute('href', AEON.href)
        } }],
      })
      expect(repeatedInbox, 'refresh needs no doctrine inbox read for a resolved rule').toBe(0)
      expect(repeatedLookup, 'refresh needs no ticket lookup for a resolved rule').toBe(0)
      expect(world.reads.filter(path => path.startsWith('/api/nodes/lookup?'))).toHaveLength(lookups)
      await panel.screenshot({ path: testInfo.outputPath(`agents-project-refresh-${width}-${theme}.png`) })
    } finally { release() }
  }
})

for (const theme of ['light', 'dark'] as const) {
  test(`tablet toolbar stays visible and stable on opening and resizing ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 768, height: 1000 })
    await openRound(page, theme)
    const actions = { pager: page.getByTestId('desk-pager'), decide: page.getByTestId('desk-decide'), skip: page.getByTestId('desk-skip'), close: page.getByTestId('desk-close') }
    const fits = async () => {
      const dialog = await page.locator('dialog.desk-dlg').boundingBox()
      expect(dialog).not.toBeNull()
      for (const [name, control] of Object.entries(actions)) {
        const box = await control.boundingBox()
        expect(box, name).not.toBeNull()
        expect(box!.width, name).toBeGreaterThan(0)
        expect(box!.x, name).toBeGreaterThanOrEqual(dialog!.x)
        expect(box!.x + box!.width, `${name} fits inside the dialog`).toBeLessThanOrEqual(dialog!.x + dialog!.width)
      }
      expect(await page.locator('.desk-toolbar').evaluate(element => element.scrollWidth - element.clientWidth)).toBeLessThanOrEqual(1)
    }
    await fits()
    await expectStableControls({ controls: { ...actions, frame: page.getByTestId('desk-frame') }, scrollAreas: { body: page.getByTestId('desk-body') }, interactions: [
      { name: 'question in the same project', run: async () => { await move(page, 'j', 'Which migration should carry the index?'); await fits() } },
      { name: 'handover in another project', run: async () => { await move(page, 'j', 'Should the successor continue the review?'); await fits() } },
      { name: 'rule with its native action', run: async () => { await move(page, 'j', 'The reviewer needs guidance before trying again.'); await move(page, 'j', 'Keep mutation checks in the transaction'); await fits() } },
    ] })
    await page.screenshot({ path: testInfo.outputPath(`tablet-${theme}.png`) })
    await page.setViewportSize({ width: 1440, height: 1000 })
    await fits()
    await page.setViewportSize({ width: 768, height: 1000 })
    await fits()
  })
}

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
    // The bar, the folder tab and the phone sheet stay still; the key chip changes in place.
    await expectStableControls({
      controls: { pager: page.getByTestId('desk-pager'), decide: page.getByTestId('desk-decide'), skip: page.getByTestId('desk-skip'), close: page.getByTestId('desk-close'), tab: page.getByTestId('desk-tab'),
        ...(width === 390 ? { frame: page.getByTestId('desk-frame') } : {}) },
      scrollAreas: { body: page.getByTestId('desk-body') },
      interactions: [
        { name: 'J into another project', run: async () => {
          await page.keyboard.press('j'); await expect(page.getByTestId('desk-tab-project')).toHaveText(PHAROS.key); await expect(page.getByTestId('desk-tab-project')).toHaveClass(/switched/)
          await page.screenshot({ path: join(dir, `desk-switch-${width}-${theme}.png`) })
        } },
        { name: 'J back into the first project', run: async () => {
          await page.keyboard.press('j'); await expect(page.getByTestId('desk-tab-project')).toHaveText(AEON.key); await expect(page.getByTestId('desk-tab-project')).toHaveClass(/switched/)
        } },
        { name: 'K returns to the other project', run: async () => {
          await page.keyboard.press('k'); await expect(page.getByTestId('desk-tab-project')).toHaveText(PHAROS.key)
        } },
      ],
    })
    await page.getByTestId('desk-pager').click()
    await expect(page.getByTestId('desk-jump-head')).toContainText('open')
    // A long title stays on one line, so the project column and every row keep their place.
    const titleHeight = await page.getByTestId('desk-jump-2').locator(':scope > span').nth(2).evaluate(element => element.getBoundingClientRect().height)
    expect(titleHeight).toBeLessThan(20)
    await page.screenshot({ path: join(dir, `desk-jump-${width}-${theme}.png`) })
  })
}
