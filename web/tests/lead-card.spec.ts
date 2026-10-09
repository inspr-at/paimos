// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1027: the project page's "No lead" card folds to one line, keeps Start lead, is remembered
// per person and project, sits one card gap above the section bar, and folds by height alone.
import { expect, test, type Locator, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { expectStableControls } from './helpers/stable'
import { mockLeadFlow } from './lead-flow-fixtures'

const KEY = 'ui.project.lead-card'
const card = (page: Page) => page.locator('section.lead')
const fold = (page: Page) => card(page).locator('[data-act="fold"]')
const start = (page: Page) => card(page).getByRole('button', { name: 'Start lead' })
const details = (page: Page) => card(page).locator('.empty-lead')
const bar = (page: Page) => page.locator('.project-navigation')
const mac = process.platform === 'darwin'
const submit = mac ? 'Meta+Enter' : 'Control+Enter'
async function theme(page: Page, value: 'light' | 'dark') { await page.evaluate(choice => { document.documentElement.dataset.theme = choice }, value) }
async function box(locator: Locator) {
  const rect = await locator.boundingBox()
  expect(rect, 'the element has a box').not.toBeNull()
  return rect!
}
async function open(page: Page, width: number, options: Parameters<typeof mockLeadFlow>[1] = {}, route = 'PHAROS') {
  await page.setViewportSize({ width, height: width < 600 ? 844 : 1000 })
  const mocked = await mockLeadFlow(page, { lead: 'none', queue: [], ...options })
  await page.goto(`/p/${route}`)
  await expect(card(page).getByRole('heading', { name: `No lead in ${route}` })).toBeVisible()
  return mocked
}
async function folded(page: Page) {
  await fold(page).click()
  await expect(fold(page)).toHaveAttribute('aria-expanded', 'false')
  await expect(details(page)).toBeHidden()
}

for (const width of [1440, 400]) for (const look of ['light', 'dark'] as const) {
  test(`the card folds to one line with Start lead at ${width} ${look}`, async ({ page }, info) => {
    await open(page, width)
    await theme(page, look)
    await expect(fold(page)).toHaveAttribute('aria-expanded', 'true')
    await expect(details(page).locator('li')).toHaveCount(3)
    const openHeight = (await box(card(page))).height
    await page.screenshot({ path: info.outputPath(`card-open-${width}-${look}.png`) })
    await folded(page)
    await page.screenshot({ path: info.outputPath(`card-folded-${width}-${look}.png`) })
    // The line still says what the card said, and the primary action stays on it.
    await expect(card(page)).toContainText('No lead in PHAROS')
    await expect(card(page)).toContainText('Nothing is queued yet')
    await expect(start(page)).toBeVisible()
    await expect(start(page)).toHaveAttribute('aria-disabled', 'false')
    await expect(card(page).locator('.lead-foot')).toBeHidden()
    // Wide: name and state share the line, with a separator. Phone: they stay stacked, with none.
    if (width > 600) await expect(card(page).locator('.lead-sep')).toBeVisible()
    else await expect(card(page).locator('.lead-sep')).toBeHidden()
    const slim = await box(card(page))
    expect(slim.height, 'folded card is a slim line').toBeLessThan(openHeight / 2)
    if (width > 600) {
      expect(slim.height, 'one line at desktop width').toBeLessThanOrEqual(80)
      const title = await box(card(page).getByRole('heading', { name: 'No lead in PHAROS' })), status = await box(card(page).locator('.lead-state'))
      expect(Math.abs(title.y + title.height / 2 - (status.y + status.height / 2)), 'title and status share one line').toBeLessThanOrEqual(2)
      expect(status.x, 'status follows the title').toBeGreaterThan(title.x + title.width - 1)
    }
    // Start lead still opens its sheet from the folded card.
    await start(page).click()
    await expect(page.getByRole('dialog', { name: 'Start lead for PHAROS' })).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(page.getByRole('dialog', { name: 'Start lead for PHAROS' })).toHaveCount(0)
    if (look === 'light') expect((await new AxeBuilder({ page }).include('section.lead').analyze()).violations).toEqual([])
  })
}

test('the fold is remembered for this person and project, and a lead clears it', async ({ page }) => {
  const { data, state } = await open(page, 1440)
  await folded(page)
  await expect.poll(() => data.preferences[KEY]).toEqual({ collapsed: ['p-pharos'] })
  // A reload and a visit from another page open it folded at once: no open card first.
  await page.reload()
  await expect(card(page).getByRole('heading', { name: 'No lead in PHAROS' })).toBeVisible()
  await expect(fold(page)).toHaveAttribute('aria-expanded', 'false')
  await expect(details(page)).toBeHidden()
  // Another project keeps its own card open.
  await page.goto('/p/AEON')
  await expect(card(page).getByRole('heading', { name: 'No lead in AEON' })).toBeVisible()
  await expect(fold(page)).toHaveAttribute('aria-expanded', 'true')
  await expect(details(page)).toBeVisible()
  await page.goto('/p/PHAROS')
  await expect(fold(page)).toHaveAttribute('aria-expanded', 'false')
  // Starting a lead from the folded card replaces it; the project forgets the fold.
  await start(page).click()
  const sheet = page.getByRole('dialog', { name: 'Start lead for PHAROS' })
  await expect(sheet).toBeVisible()
  await page.keyboard.press(submit)
  await expect(sheet).toHaveCount(0)
  expect(state.calls.find(c => c.method === 'POST' && c.path === '/api/projects/p-pharos/lead')?.body).toEqual({ expected_revision: 0 })
  await expect(card(page)).toContainText('Requested · waiting for its session')
  await expect(fold(page)).toHaveCount(0)
  await expect.poll(() => data.preferences[KEY]).toEqual({ collapsed: [] })
  // The lead ends (the project has none again): the card is open, as it was never folded.
  state.leads['p-pharos'] = { project_id: 'p-pharos', revision: 0, generation: 0, session_id: null, state: 'none', reason: '', process_active: false }
  await page.reload()
  await expect(fold(page)).toHaveAttribute('aria-expanded', 'true')
  await expect(details(page)).toBeVisible()
})

for (const width of [1440, 1024, 400]) {
  test(`the card, open or folded, keeps one card gap above the section bar at ${width}`, async ({ page }) => {
    await open(page, width)
    const gap = async () => (await box(bar(page))).y - ((await box(card(page))).y + (await box(card(page))).height)
    expect(await gap(), 'open').toBeCloseTo(16, 0)
    await folded(page)
    expect(await gap(), 'folded').toBeCloseTo(16, 0)
    // The bar itself does not change: only its distance to the top follows the card.
    await fold(page).click()
    await expect(details(page)).toBeVisible()
    expect(await gap(), 'unfolded again').toBeCloseTo(16, 0)
  })
}

const sectionBar = (page: Page) => page.getByRole('tablist', { name: 'Project sections' })
for (const width of [1440, 400]) for (const look of ['light', 'dark'] as const) {
  test(`the lead keeps one card gap above the section bar on Tickets, Knowledge and Settings at ${width} ${look}`, async ({ page }) => {
    await open(page, width)
    await theme(page, look)
    for (const name of ['Tickets', 'Knowledge', 'Settings']) {
      await sectionBar(page).getByRole('tab', { name, exact: true }).click()
      await expect(sectionBar(page).getByRole('tab', { name, exact: true })).toHaveAttribute('aria-selected', 'true')
      await expect(card(page).getByRole('heading', { name: 'No lead in PHAROS' })).toBeVisible()
      // The 18px tab margin must be computed as 0. A dropped stylesheet rule leaves 18px on Knowledge and Settings.
      expect(await sectionBar(page).evaluate(element => getComputedStyle(element).marginTop), `${name} computed margin above the section bar`).toBe('0px')
      const gap = (await box(bar(page))).y - ((await box(card(page))).y + (await box(card(page))).height)
      expect(gap, `${name} card gap`).toBeCloseTo(16, 0)
    }
  })
}

test.describe('stability', () => {
  test.use({ reducedMotion: 'no-preference' })
  for (const width of [1440, 400]) {
    test(`folding never moves a control at ${width}`, async ({ page }) => {
      await open(page, width)
      const head = page.locator('#project-title'), toggle = fold(page), primary = start(page)
      const top = (await box(card(page))).y
      // The header's view switch (Comfortable · Compact · Collapsed) sits above the card on wide screens.
      const switches = width > 600 ? { 'header view switch': page.getByRole('radio', { name: 'Comfortable project header' }), 'header view switch collapsed': page.getByRole('radio', { name: 'Collapsed project header' }) } : {}
      await expectStableControls({ controls: { title: head, start: primary, fold: toggle, ...switches }, interactions: [
        { name: 'fold with the pointer', run: async () => { await toggle.click(); await expect(toggle).toHaveAttribute('aria-expanded', 'false'); await expect(details(page)).toBeHidden() } },
        { name: 'unfold with the pointer', run: async () => { await toggle.click(); await expect(toggle).toHaveAttribute('aria-expanded', 'true'); await expect(details(page)).toBeVisible() } },
        { name: 'fold with Enter', run: async () => { await toggle.focus(); await page.keyboard.press('Enter'); await expect(toggle).toHaveAttribute('aria-expanded', 'false') } },
        { name: 'unfold with Space', run: async () => { await page.keyboard.press('Space'); await expect(toggle).toHaveAttribute('aria-expanded', 'true') } },
      ] })
      expect((await box(card(page))).y, 'the card top never moves').toBeCloseTo(top, 0)
      await expect(toggle).toBeFocused()
    })
  }
})

test('keyboard: Tab reaches Start lead and the fold, folded details leave the tab order', async ({ page }) => {
  await open(page, 1440)
  const toggle = fold(page)
  await expect(toggle).toHaveAccessibleName('What the lead does')
  await toggle.focus()
  await page.keyboard.press('Space')
  await expect(toggle).toHaveAttribute('aria-expanded', 'false')
  await expect(toggle).toBeFocused()
  expect(await toggle.getAttribute('aria-controls')).toBe('lead-fold')
  await expect(page.locator('#lead-fold')).toHaveAttribute('inert', '')
  await start(page).focus()
  await page.keyboard.press('Tab')
  await expect(toggle).toBeFocused()
  await page.keyboard.press('Enter')
  await expect(page.locator('#lead-fold')).not.toHaveAttribute('inert', '')
  await expect(details(page)).toBeVisible()
  // The control is an SVG chevron, not a text glyph.
  await expect(toggle.locator('svg')).toHaveCount(1)
  await expect(toggle).toHaveText('')
})

test('on a phone the fold and Start lead are 44 px targets that do not overlap', async ({ page }) => {
  await open(page, 400)
  await folded(page)
  const a = await box(fold(page)), b = await box(start(page))
  for (const [name, rect] of [['fold', a], ['start', b]] as const) {
    expect(rect.width, `${name} width`).toBeGreaterThanOrEqual(44)
    expect(rect.height, `${name} height`).toBeGreaterThanOrEqual(44)
  }
  expect(a.x >= b.x + b.width || b.x >= a.x + a.width || a.y >= b.y + b.height || b.y >= a.y + a.height, 'targets do not overlap').toBe(true)
  const scroll = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
  expect(scroll, 'no horizontal overflow').toBeLessThanOrEqual(1)
})

test.describe('motion', () => {
  test.use({ reducedMotion: 'no-preference' })
  test('folding animates the card’s height and nothing else', async ({ page }) => {
    await open(page, 1440)
    const settled = async () => { await expect(card(page)).not.toHaveClass(/moving/) }
    const animated = () => page.evaluate(() => [...document.querySelectorAll('section.lead *')].flatMap(element => element.getAnimations().flatMap(animation => animation instanceof CSSTransition ? [animation.transitionProperty] : [])))
    // The preference is read before the card shows, so opening plays nothing.
    expect(await animated()).toEqual([])
    await fold(page).click()
    await expect(card(page)).toHaveClass(/moving/)
    const during = await animated()
    expect(during, 'the height of the details, and the chevron turning').toEqual(expect.arrayContaining(['grid-template-rows']))
    // Hover paint on the pressed chevron may fade; no other geometry moves.
    const paint = ['color', 'background-color', 'border-color', 'box-shadow', 'opacity']
    expect(during.filter(property => !['grid-template-rows', 'visibility', 'transform', ...paint].includes(property)), 'no other geometry animates').toEqual([])
    await settled()
    await expect(details(page)).toBeHidden()
    await fold(page).click()
    await expect(card(page)).toHaveClass(/moving/)
    await settled()
    await expect(details(page)).toBeVisible()
    // The open card is not left clipping: focus rings inside it stay whole.
    expect(await page.locator('.lead-fold-inner').evaluate(element => getComputedStyle(element).overflow)).toBe('visible')
  })
})

test.describe('reduced motion', () => {
  test.use({ reducedMotion: 'reduce' })
  test('folding shows no animation at all', async ({ page }) => {
    await open(page, 1440)
    const before = (await box(card(page))).height
    await fold(page).click()
    // No intermediate state: the fold is final at once and nothing is left transitioning.
    await expect(card(page)).not.toHaveClass(/moving/)
    const after = await page.evaluate(() => ({
      running: [...document.querySelectorAll('section.lead *')].flatMap(element => element.getAnimations()).length,
      height: document.querySelector('section.lead')!.getBoundingClientRect().height,
      duration: getComputedStyle(document.querySelector('.lead-fold')!).transitionDuration,
    }))
    expect(after.running).toBe(0)
    expect(after.duration).toBe('0s')
    expect(after.height).toBeLessThan(before / 2)
  })
})
