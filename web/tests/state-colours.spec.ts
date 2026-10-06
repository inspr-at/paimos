// SPDX-License-Identifier: AGPL-3.0-only
// SC1 / AEON-221. Coordinator: npm test -- state-colours.spec.ts --workers=2.
import { test, expect } from '@playwright/test'
import { mockStateColours, states } from './state-colours-fixtures'
import { STATE_LABEL } from '../src/lib/agentSignals'
import { indicatorVariants } from '../src/lib/indicatorVariants'
import { saveAgentTheme } from './agent-theme-fixtures'

for (const theme of ['light', 'dark'] as const) {
  test(`${theme}: every state agrees across /agents, detail, cards and list rows`, async ({ page }) => {
    const { agents } = await mockStateColours(page, theme)
    await page.goto('/agents')
    await page.getByRole('button', { name: /^Ended/ }).click()
    for (const [index, state] of states.entries()) {
      const row = page.locator(`[data-row="s:${agents.sessions[index]!.id}"]`)
      await expect(row).toHaveAttribute('data-state', state)
      await expect(row.locator('.agent-state-label')).toHaveText(STATE_LABEL[state])
      await expect(row.locator('.agent-state-mark').first()).toHaveAttribute('data-mark', state)
      await expect(row.locator('.exec-model')).toContainText('integration-model · xhigh')
      await expect(row.locator('.exec-account')).toContainText('Test account')
      await expect(row.locator('.provider-mark')).toHaveAttribute('data-provider', 'unknown')
      await row.locator('.agent-link').click()
      const panel = page.getByRole('complementary', { name: 'Session details' })
      await expect(panel.locator('.head-top .agent-state-label')).toHaveText(STATE_LABEL[state])
      const setup = panel.locator('[aria-labelledby="setup-title"]')
      for (const value of ['integration-model', 'xhigh', 'Test account', '1.2.3']) await expect(setup).toContainText(value)
      const work = panel.locator('[aria-labelledby="work-title"]')
      for (const value of ['AEON-221', '/Code/aeon-sc1', 'sc1.state-colours', 'abc1234', 'Integrate session states']) await expect(work).toContainText(value)
      await page.getByRole('button', { name: 'Close session details' }).click()
    }
    // The head counts carry each state's mark (AEON-780 replaced the live line).
    for (const state of ['working', 'waiting', 'throttled', 'problem']) await expect(page.locator(`.page-head .head-counts [data-mark="${state}"]`).first()).toBeVisible()
    await page.goto('/')
    for (const layout of ['Cards', 'List']) {
      await page.getByRole('radio', { name: `${layout} view`, exact: true }).click()
      await page.mouse.move(0, 0)
      expect(new Set(await page.locator('[data-project-id]').evaluateAll(elements => elements.map(el => getComputedStyle(el).backgroundColor))).size).toBe(1)
      for (const state of states) {
        const project = page.locator(`[data-project-id="p-sc1-${state}"]`)
        if (state === 'stopped') {
          await expect(project.locator('.live')).toHaveCount(0)
          continue
        }
        await expect(project).toHaveAttribute('data-agent-state', state)
        await expect(project.locator('.chip-state')).toHaveText(STATE_LABEL[state])
        await expect(project.locator('.live-bot')).toHaveAttribute('aria-label', STATE_LABEL[state])
        if (state !== 'working') expect(await project.locator('.live-bot').evaluate(el => el.getAnimations({ subtree: true }).length)).toBe(0)
      }
    }
  })

  test(`${theme}: all state words fit narrow cards and phone list rows`, async ({ page }) => {
    await mockStateColours(page, theme)
    await page.goto('/')
    for (const width of [320, 390]) {
      await page.setViewportSize({ width, height: 844 })
      for (const layout of ['Cards', 'List']) {
        await page.getByRole('radio', { name: `${layout} view`, exact: true }).click()
        for (const state of states) {
          if (state === 'stopped') continue
          const project = page.locator(`[data-project-id="p-sc1-${state}"]`)
          const label = project.locator('.chip-state')
          await expect(label).toHaveText(STATE_LABEL[state])
          const bounds = (await project.boundingBox())!, chip = (await project.locator('.live-chip').boundingBox())!
          const word = (await label.boundingBox())!
          expect(word.x).toBeGreaterThanOrEqual(chip.x)
          expect(word.x + word.width).toBeLessThanOrEqual(chip.x + chip.width)
          expect(chip.x + chip.width).toBeLessThanOrEqual(bounds.x + bounds.width)
          expect(await label.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
        }
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
      }
    }
  })
}

test('saved theme palette/opacity and personal heartbeat thresholds persist across every surface', async ({ page }) => {
  const { data, appearance } = await mockStateColours(page)
  await page.goto('/settings/theme#agents')
  const palette = (name: string) => page.getByRole('radiogroup', { name: 'State colours' }).getByRole('radio', { name, exact: true })
  await palette('Deutan').click()
  const preview = page.locator('#agents .light [data-preview-state="working"] .live-bot')
  expect(await preview.evaluate(el => getComputedStyle(el.querySelector('.agent-state-mark')!).color)).toBe('rgb(0, 117, 204)')
  const opacity = page.getByRole('slider', { name: 'Opacity', exact: true })
  await opacity.press('Home')
  for (let step = 0; step < 30; step++) await opacity.press('ArrowRight')
  expect(appearance.writes).toHaveLength(0)
  await saveAgentTheme(page)
  expect(appearance.saved.values.agents).toMatchObject({ palette: 'deutan', inactive_opacity: 70 })
  await page.reload()
  await expect(palette('Deutan')).toBeChecked()
  await expect(opacity).toHaveValue('70')
  await page.goto('/settings/personal#agents')
  await expect(page.getByRole('slider')).toHaveCount(0)
  await page.getByLabel('Yellow after (minutes)').fill('5')
  await page.getByLabel('Red after (minutes)').fill('12')
  await page.getByLabel('Red after (minutes)').blur()
  await expect.poll(() => data.preferences['agent-state']?.redMinutes).toBe(12)
  await page.reload()
  await expect(page.getByLabel('Yellow after (minutes)')).toHaveValue('5')
  await expect(page.getByLabel('Red after (minutes)')).toHaveValue('12')
  await page.goto('/')
  await expect(page.locator('[data-project-id="p-sc1-stale"] .live-chip')).toHaveCSS('opacity', '0.7')
  await expect(page.locator('[data-project-id="p-sc1-unresponsive"] .live-bot')).toHaveAttribute('data-state', 'awaiting')
  for (const view of ['Cards', 'List']) {
    await page.getByRole('radio', { name: `${view} view`, exact: true }).click()
    await expect(page.locator('[data-project-id="p-sc1-working"] .agent-state-mark')).toHaveCSS('color', 'rgb(0, 117, 204)')
  }
  await page.goto('/settings/theme#agents')
  await page.getByRole('switch', { name: 'Dim inactive' }).uncheck()
  await palette('One colour').click()
  const colours = await page.locator('#agents .light .states .live-bot').evaluateAll(bots => bots.slice(0, 4).map(bot => getComputedStyle(bot.querySelector('.agent-state-mark')!).color))
  expect(new Set(colours).size).toBe(1)
  await saveAgentTheme(page)
  expect(appearance.saved.values.agents).toMatchObject({ palette: 'monochrome', dim_inactive: false })
  await page.goto('/agents')
  await page.getByRole('button', { name: /^Ended/ }).click()
  await expect(page.locator('.row[data-state="stopped"]')).toHaveCSS('opacity', '1')
  await expect(page.locator('.row[data-state="working"] .agent-state-label')).toHaveCSS('--agent-state-color', /.+/)
})

test('a migrated colour-blind choice uses Deutan while legacy appearance and heartbeat data stay intact', async ({ page }) => {
  const { data, appearance } = await mockStateColours(page)
  const legacy = { palette: 'colour-blind', dimInactive: true, inactiveOpacity: 60, yellowMinutes: 4, redMinutes: 11 }
  data.preferences['agent-state'] = { ...legacy }
  appearance.saved.values.agents = { ...appearance.saved.values.agents, palette: 'deutan', dim_inactive: true, inactive_opacity: 60 }
  await page.goto('/settings/theme#agents')
  const palettes = page.getByRole('radiogroup', { name: 'State colours' })
  await expect(palettes.getByRole('radio', { name: 'Deutan', exact: true })).toBeChecked()
  expect(await palettes.getByRole('radio').allTextContents()).toEqual(['Standard', 'Focus', 'Errors only', 'One colour', 'Deutan', 'Protan', 'Tritan'])
  await expect(page.locator('#agents .light [data-preview-state="problem"] .agent-state-mark')).toHaveCSS('color', 'rgb(177, 61, 9)')
  for (const state of ['working', 'waiting', 'problem', 'idle']) {
    await expect(page.locator(`#agents .light [data-preview-state="${state}"] .agent-state-mark`)).toHaveAttribute('data-mark', state)
  }
  expect(data.preferences['agent-state']).toEqual(legacy)
  await palettes.getByRole('radio', { name: 'Tritan', exact: true }).click()
  expect(appearance.writes).toHaveLength(0)
  await saveAgentTheme(page)
  expect(appearance.saved.values.agents).toMatchObject({ palette: 'tritan', dim_inactive: true, inactive_opacity: 60 })
  expect(data.preferences['agent-state']).toEqual(legacy)
  await page.goto('/settings/personal#agents')
  await expect(page.getByLabel('Yellow after (minutes)')).toHaveValue('4')
  await expect(page.getByLabel('Red after (minutes)')).toHaveValue('11')
})

test('all nine variants render every mark and stop problem/inactive motion', async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 1600, height: 1100 })
  await page.emulateMedia({ reducedMotion: 'no-preference' })
  await page.goto('/tests/state-colours-gallery.html')
  for (const variant of indicatorVariants) {
    for (const state of states) {
      const bot = page.locator(`[data-variant="${variant.id}"][data-state="${state}"] .live-bot`)
      await expect(bot).toHaveAttribute('aria-label', STATE_LABEL[state])
      await expect(bot.locator('.agent-state-mark')).toHaveAttribute('data-mark', state)
      if (state !== 'working') expect(await bot.evaluate(el => el.getAnimations({ subtree: true }).length)).toBe(0)
    }
  }
  await page.emulateMedia({ reducedMotion: 'reduce' })
  expect(await page.locator('main').evaluate(el => el.getAnimations({ subtree: true }).length)).toBe(0)
  for (const theme of ['light', 'dark']) {
    await page.goto(`/tests/state-colours-gallery.html?theme=${theme}`)
    await expect(page.locator('.sample .live-bot')).toHaveCount(states.length * indicatorVariants.length)
    await expect(page.locator('.sample[data-variant="robot-5"][data-state="problem"] [data-expression="problem"]')).toBeVisible()
    await page.screenshot({ path: testInfo.outputPath(`sc2-${theme}-all-states.png`), fullPage: true })
  }
})

for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) {
  test(`SC2 ${theme} ${width}: neutral surfaces and grounded state detail`, async ({ page }, testInfo) => {
    const { appearance, agents } = await mockStateColours(page, theme)
    appearance.saved.values.agents = { ...appearance.saved.values.agents, avatar: 'robot-5', hover: false }
    await page.setViewportSize({ width, height: width === 1600 ? 1000 : 844 })
    await page.goto('/agents')
    await expect(page.locator('.row[data-state="awaiting"] .agent-state-label')).toHaveText('Awaiting heartbeat')
    await expect(page.locator('.group-row').filter({ hasText: 'Needs attention' })).toBeVisible()
    await expect(page.getByRole('group', { name: 'Show sessions by state' })).toContainText('working')
    // Closing a session retains the page keyboard cursor and returns to the list.
    await page.locator('[data-state="working"] .agent-link').click()
    await page.getByRole('button', { name: 'Close session details' }).click()
    await expect(page).toHaveURL(/\/agents$/)
    const main = (await page.locator('.main-col').boundingBox())!
    const layout = (await page.locator('.layout').boundingBox())!
    expect(Math.abs(main.width - layout.width)).toBeLessThan(2)
    // Accounts sit above the sessions now (AEON-299); account management is in Settings.
    const sessions = (await page.locator('.sessions').boundingBox())!
    const accounts = (await page.locator('.ac').boundingBox())!
    expect(accounts.y + accounts.height).toBeLessThanOrEqual(sessions.y)
    await page.locator('h1').scrollIntoViewIfNeeded()
    await page.mouse.move(0, 0)
    // Head counts share one neutral surface and the colour is in the mark; only a problem or a request is tinted.
    const counts = page.locator('.page-head .head-counts .count:not(.hot):not(.ask)')
    const surfaces = await counts.evaluateAll(elements => elements.map(el => getComputedStyle(el).backgroundColor))
    expect(new Set(surfaces).size).toBe(1)
    const rows = page.locator('.row[data-state]:not(.active):not(.selected)')
    expect(new Set(await rows.evaluateAll(elements => elements.map(el => getComputedStyle(el).backgroundColor))).size).toBe(1)
    await expect(page.locator('.page-head .head-counts [data-mark="problem"]').first()).toBeVisible()
    await page.screenshot({ path: testInfo.outputPath(`sc2-${theme}-${width}-agents.png`), fullPage: true })
    const problem = page.locator('.row[data-state="problem"]').first()
    await problem.locator('.agent-link').click()
    const panel = page.getByRole('complementary', { name: 'Session details' })
    await expect(panel.locator('.now-step')).toHaveText('Reported stop reason: worker failed: exit 2')
    if (width === 1600) {
      const listBounds = (await page.locator('.main-col').boundingBox())!
      const panelBounds = (await panel.boundingBox())!
      expect(listBounds.x + listBounds.width).toBeLessThanOrEqual(panelBounds.x)
      expect(panelBounds.x - listBounds.x - listBounds.width).toBeLessThan(40)
    }
    await expect(panel.getByRole('region', { name: 'Session state evidence' })).toContainText('Last heartbeat:')
    await page.screenshot({ path: testInfo.outputPath(`sc2-${theme}-${width}-problem.png`), fullPage: true })
    await page.getByRole('button', { name: 'Close session details' }).click()
    const missing = agents.sessions.find(item => item.display_label === 'SC1 unresponsive')!
    missing.heartbeat_at = null
    await page.goto(`/agents/${missing.id}`)
    const evidence = panel.getByRole('region', { name: 'Session state evidence' })
    await expect(panel.locator('.head-top .agent-state-label')).toHaveText('No heartbeat')
    await expect(panel.locator('.now-step')).toHaveText('No heartbeat has been received since this session registered.')
    await expect(evidence).toContainText('does not establish that the worker failed')
    await expect(evidence).toContainText('Registered')
    await expect(evidence).toContainText('flagged at 10m')
    await expect(panel.locator('.now-step')).not.toHaveText('Check the session details')
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    await page.screenshot({ path: testInfo.outputPath(`sc2-${theme}-${width}-no-heartbeat.png`), fullPage: true })
  })
}

test('saved thresholds load before sessions can flash a default heartbeat warning', async ({ page }) => {
  const { agents } = await mockStateColours(page)
  const delayed = agents.sessions.find(item => item.display_label === 'SC1 unresponsive')!
  let release!: () => void
  const ready = new Promise<void>(resolve => { release = resolve })
  await page.route('**/api/preferences/agent-state', async route => {
    await ready
    await route.fulfill({ json: { value: { yellowMinutes: 15, redMinutes: 20 } } })
  })
  await page.goto('/agents')
  await expect(page.locator(`[data-row="s:${delayed.id}"]`)).toHaveCount(0)
  release()
  await expect(page.locator(`[data-row="s:${delayed.id}"]`)).toHaveAttribute('data-state', 'working')
})
