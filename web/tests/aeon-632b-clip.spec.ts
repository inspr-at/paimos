// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { expect, test, type Locator } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { knowledgeWorld, mockKnowledge } from './knowledge-fixtures'
import { mockJourney, journeyWorld, PROJECT } from './journey-fixtures'
import { expectStableControls } from './helpers/stable'
import { groupsWorld, mockProjectGroups } from './project-groups-fixtures'
import { NAME, GROUP, SLUG, shots, setup, capture, disclosure } from './aeon-632b-clip-fixtures'

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
  test(`stationary touch explains a restricted shared group ${width} ${theme}`, async ({ browser }) => {
    const context = await browser.newContext({ viewport: { width, height: 900 }, hasTouch: true, isMobile: width === 390, reducedMotion: 'reduce' })
    try {
      const page = await context.newPage()
      await page.emulateMedia({ colorScheme: theme })
      const errors = watchErrors(page)
      const data = fixtures()
      data.preferences.theme = { choice: theme }
      data.preferences['project-groups'] = { groups: [{ id: 'g:mine', name: 'Persönliche Ablage' }] }
      const calls = await mockWork(page, data, { admin: false })
      const world = groupsWorld({ clients: ['p-aeon'] })
      world.groups[0]!.name = GROUP
      await mockProjectGroups(page, data, world, { admin: false })
      await page.goto('/')
      await page.locator('[data-project-id="p-pharos"] .item-link').focus()
      await page.keyboard.press('m')
      const picker = page.getByRole('dialog', { name: 'Move Pharos to a group' })
      const search = picker.getByRole('combobox')
      const rows = picker.getByRole('option')
      const restricted = rows.filter({ hasText: GROUP })
      const note = picker.locator('.option-note')
      const reason = 'Only a workspace admin can add projects to a shared group.'
      await expect(restricted).toHaveAttribute('aria-disabled', 'true')
      await expect(restricted).toHaveAttribute('aria-selected', 'false')
      await expect(note).toBeEmpty()
      const controls = { search, selector: picker.locator('.options'), explanation: note,
        ...Object.fromEntries((await rows.all()).map((row, index) => [`row-${index}`, row])) }
      const tap = async (row: Locator) => {
        const bounds = (await row.boundingBox())!
        // A real stationary touch bypasses Playwright's aria-disabled action
        // guard without a hover/pointermove that would mask the regression.
        await page.touchscreen.tap(bounds.x + bounds.width / 2, bounds.y + bounds.height / 2)
      }
      await expectStableControls({ controls, interactions: [
        { name: 'restricted row touch discloses reason', run: async () => {
          await tap(restricted)
          await expect(note).toHaveText(reason)
          await expect(restricted).toHaveAttribute('aria-selected', 'true')
          await expect(search).toHaveAttribute('aria-activedescendant', (await restricted.getAttribute('id'))!)
          await expect(picker).toBeVisible()
        } },
        { name: 'Enter retains restriction and explanation', run: async () => {
          await search.press('Enter')
          await expect(note).toHaveText(reason)
          await expect(restricted).toHaveAttribute('aria-selected', 'true')
          await expect(picker).toBeVisible()
        } },
        { name: 'repeat stationary touch keeps explanation', run: async () => {
          await tap(restricted)
          await expect(note).toHaveText(reason)
        } },
      ], scrollAreas: { picker: picker.locator('.options') } })
      expect(world.calls.filter(call => call.method !== 'GET')).toEqual([])
      expect(calls.filter(call => call.method !== 'GET' && call.path.includes('project-groups'))).toEqual([])
      expect(world.groups[0]!.project_ids).toEqual(['p-aeon'])
      await capture(page, 'restricted-group-touch', width, theme)
      await tap(rows.filter({ hasText: 'Persönliche Ablage' }))
      await expect(picker).toHaveCount(0)
      await expect.poll(() => data.preferences['project-groups']?.place).toEqual({ 'p-pharos': 'g:mine' })
      expect(world.calls.filter(call => call.method !== 'GET')).toEqual([])
      expect(errors).toEqual([])
    } finally { await context.close() }
  })
}

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

    const journey = journeyWorld('inspire')
    for (const source of journey.intake.sources as { label: string }[]) source.label = NAME
    await mockJourney(page, journey)
    await page.goto('/p/PHAROS?view=journey&stage=inspire')
    await disclosure(page, page.locator('.src-rows .grow').first(), { steps: page.locator('.stages') })
    await capture(page, 'journey-sources', width, theme)
    const imported = journeyWorld('build', { derived: true })
    imported.projectBody = 'Synthetischer Projektkontext.'
    imported.knowledge = [{ id: 'clip-origin', key: 'PHAROS-40', type: 'runbook', kind: 'runbook', slug: SLUG, title: NAME, status: 'active', state: 'active', project: { id: PROJECT, key: 'PHAROS', title: NAME }, excerpt: '', link_count: 0, created_at: '2026-10-03T10:00:00Z', updated_at: '2026-10-03T10:00:00Z', updated_by: null, imported: true }]
    await mockJourney(page, imported)
    await page.goto('/p/PHAROS?view=journey&stage=inspire')
    await disclosure(page, page.locator('.origin .grow'), { steps: page.locator('.stages') })
    await capture(page, 'journey-imported', width, theme)
    expect(errors).toEqual([])
  })
}

test('hidden journey stages reveal their labels at 768px without moving the rail', async ({ page }) => {
  await page.setViewportSize({ width: 768, height: 900 })
  await setup(page, 'light')
  await mockJourney(page, journeyWorld('inspire'))
  await page.goto('/p/PHAROS?view=journey&stage=inspire')
  const stage = page.locator('.step').last()
  await expect(stage.locator('.t')).toBeHidden()
  await expectStableControls({ controls: { stage, rail: page.locator('.stages') }, interactions: [
    { name: 'keyboard stage label', run: async () => { await page.keyboard.press('Tab'); await stage.focus(); await expect(page.locator('.tooltip')).toContainText('Live') } },
  ] })
  await capture(page, 'journey-rail', 768, 'light')
})

test('a phone tap opens its project immediately without revealing text', async ({ browser }) => {
  const context = await browser.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })
  try {
    const page = await context.newPage()
    await setup(page, 'dark')
    await page.goto('/')
    const name = page.locator('[data-project-id="p-pharos"] .name')
    await expect(name).toHaveAttribute('data-tip', NAME)
    await name.tap()
    await expect(page).toHaveURL(/\/p\/PHAROS/)
    await expect(page.locator('.tooltip')).toHaveCount(0)
  } finally { await context.close() }
})

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
  for (const view of ['Rows', 'Cards'] as const) {
    test(`long press reads ${view} before native project navigation ${width} ${theme}`, async ({ browser }) => {
      const context = await browser.newContext({ viewport: { width, height: 900 }, hasTouch: true, isMobile: width === 390, reducedMotion: 'reduce' })
      try {
        const page = await context.newPage()
        const errors = watchErrors(page)
        const data = await setup(page, theme)
        data.projects[1]!.title = NAME
        await page.goto('/')
        await page.getByRole('radio', { name: view === 'Rows' ? 'List view' : 'Cards view', exact: true }).click()
        const project = page.locator('[data-project-id="p-pharos"]')
        const link = project.locator('.item-link')
        const name = project.locator(view === 'Rows' ? '.name' : '.card-name')
        const other = page.locator('[data-project-id="p-aeon"]').locator(view === 'Rows' ? '.name' : '.card-name')
        await expect(name).toHaveAttribute('data-tip', NAME)
        await expect(other).toHaveAttribute('data-tip', NAME)
        await page.clock.install()
        const cdp = await context.newCDPSession(page)
        const hold = async (target: Locator) => {
          // CDP sends viewport coordinates and does not scroll like tap().
          // Finish intentional scrolling before starting the timed gesture.
          await target.scrollIntoViewIfNeeded()
          await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))))
          await page.clock.pauseAt(await page.evaluate(() => Date.now() + 1000))
          const box = (await target.boundingBox())!
          expect(await target.evaluate((element, box) => element.contains(document.elementFromPoint(box.x + box.width / 2, box.y + box.height / 2)), box), 'the real touch must hit the clipped name').toBe(true)
          await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x: box.x + box.width / 2, y: box.y + box.height / 2 }] })
          await page.clock.runFor(499)
          await expect(page.locator('.tooltip')).toHaveCount(0)
          await page.clock.runFor(1)
          await expect(page.locator('.tooltip')).toHaveText(NAME)
          await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] })
          await page.clock.resume()
          await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => resolve())))
          await expect(page).toHaveURL(/\/$/)
          await expect(page.locator('.tooltip')).toHaveText(NAME)
        }
        try {
          await expectStableControls({ controls: { link, name, menu: project.getByRole('button', { name: /^Actions for/ }) }, interactions: [
            { name: '500 ms hold reveals without navigation', run: () => hold(name) },
            { name: 'Escape dismisses and a new hold reveals', run: async () => {
              await page.keyboard.press('Escape')
              await expect(page.locator('.tooltip')).toHaveCount(0)
              await hold(name)
            } },
            { name: 'equal text on another record remains bound to its source', run: async () => {
              await hold(other)
              await hold(name)
            } },
          ], scrollAreas: { content: page.locator('#main') } })
          const bounds = (await page.locator('.tooltip').boundingBox())!
          expect(bounds.x).toBeGreaterThanOrEqual(8)
          expect(bounds.x + bounds.width).toBeLessThanOrEqual(width - 8)
          mkdirSync(shots, { recursive: true })
          await page.screenshot({ path: `${shots}/touch-project-${view.toLowerCase()}-${width}-${theme}.png` })
          await name.tap()
          await expect(page).toHaveURL(/\/p\/PHAROS/)
          await expect(page.locator('.tooltip')).toHaveCount(0)
          expect(errors).toEqual([])
        } finally { await page.clock.resume(); await cdp.detach() }
      } finally { await context.close() }
    })
  }
}

test('unclipped touch, mouse and keyboard project activation stay immediate', async ({ browser }) => {
  const context = await browser.newContext({ viewport: { width: 390, height: 900 }, hasTouch: true, isMobile: true })
  try {
    const page = await context.newPage()
    const data = await setup(page, 'light')
    data.projects[0]!.title = 'Kurz'
    await page.goto('/')
    const link = page.locator('[data-project-id="p-pharos"] .item-link')
    const name = link.locator('.name')
    await expect(name).not.toHaveAttribute('data-clip-tip')
    await name.tap()
    await expect(page).toHaveURL(/\/p\/PHAROS/)
    data.projects[0]!.title = NAME
    await page.goto('/')
    await expect(name).toHaveAttribute('data-tip', NAME)
    await name.click()
    await expect(page).toHaveURL(/\/p\/PHAROS/)
    await page.goto('/')
    await link.focus()
    await link.press('Enter')
    await expect(page).toHaveURL(/\/p\/PHAROS/)
  } finally { await context.close() }
})
