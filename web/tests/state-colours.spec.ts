// SPDX-License-Identifier: AGPL-3.0-only
// SC1 / AEON-221. Coordinator: npm test -- state-colours.spec.ts --workers=2.
import { test, expect } from '@playwright/test'
import { mockStateColours, states } from './state-colours-fixtures'
import { STATE_LABEL } from '../src/lib/agentSignals'
import { indicatorVariants } from '../src/lib/indicatorVariants'

for (const theme of ['light', 'dark'] as const) {
  test(`${theme}: every state agrees across /agents, detail, cards and list rows`, async ({ page }) => {
    const { agents } = await mockStateColours(page, theme)
    await page.goto('/agents')
    await page.getByRole('button', { name: /^Stopped/ }).click()
    for (const [index, state] of states.entries()) {
      const row = page.locator(`[data-row="s:${agents.sessions[index]!.id}"]`)
      await expect(row).toHaveAttribute('data-state', state)
      await expect(row.locator('.agent-state-label')).toHaveText(STATE_LABEL[state])
      await expect(row.locator('.agent-state-mark').first()).toHaveAttribute('data-mark', state)
      await row.locator('.agent-link').click()
      await expect(page.getByRole('complementary', { name: 'Session details' }).locator('.head-top .agent-state-label')).toHaveText(STATE_LABEL[state])
      await page.getByRole('button', { name: 'Close session details' }).click()
    }
    for (const state of ['working', 'waiting', 'throttled', 'problem']) await expect(page.locator(`.live-now .tile[data-state="${state}"]`)).toBeVisible()
    await expect(page.locator('.queue .agent-state-label').first()).toHaveText('Needs something')
    await page.goto('/')
    for (const layout of ['Cards', 'List']) {
      await page.getByRole('radio', { name: `${layout} view`, exact: true }).click()
      for (const state of states) {
        const project = page.locator(`[data-project-id="p-sc1-${state}"]`)
        await expect(project).toHaveAttribute('data-agent-state', state)
        await expect(project.locator('.chip-state')).toHaveText(STATE_LABEL[state])
        await expect(project.locator('.live-bot')).toHaveAttribute('aria-label', STATE_LABEL[state])
        if (state !== 'working') expect(await project.locator('.live-bot').evaluate(el => el.getAnimations({ subtree: true }).length)).toBe(0)
      }
    }
  })
}

test('per-viewer palettes, opacity and heartbeat thresholds persist and reach every surface', async ({ page }) => {
  const { data } = await mockStateColours(page)
  await page.goto('/settings/personal#agents')
  await page.getByLabel('Palette', { exact: true }).selectOption('colour-blind')
  await expect.poll(() => data.preferences['agent-state']?.palette).toBe('colour-blind')
  const preview = page.locator('.state-preview .agent-state-label[data-state="working"]')
  await expect(preview).toHaveCSS('color', 'rgb(33, 99, 174)')
  const opacity = page.getByLabel('Inactive opacity', { exact: true })
  await opacity.press('Home')
  for (let step = 0; step < 30; step++) await opacity.press('ArrowRight')
  await expect.poll(() => data.preferences['agent-state']?.inactiveOpacity).toBe(70)
  await page.getByLabel('Yellow after (minutes)').fill('5')
  await page.getByLabel('Red after (minutes)').fill('12')
  await page.getByLabel('Red after (minutes)').blur()
  await expect.poll(() => data.preferences['agent-state']?.redMinutes).toBe(12)
  await page.reload()
  await expect(page.getByLabel('Palette', { exact: true })).toHaveValue('colour-blind')
  await expect(page.getByLabel('Inactive opacity', { exact: true })).toHaveValue('70')
  await page.goto('/')
  await expect(page.locator('[data-project-id="p-sc1-stopped"] .live-chip')).toHaveCSS('opacity', '0.7')
  // The ten-minute heartbeat is amber with the saved twelve-minute red threshold.
  await expect(page.locator('[data-project-id="p-sc1-problem"] .live-bot')).toHaveAttribute('data-state', 'waiting')
  await page.goto('/settings/personal#agents')
  await page.getByRole('switch', { name: 'Dim inactive' }).uncheck()
  await expect.poll(() => data.preferences['agent-state']?.dimInactive).toBe(false)
  await page.getByLabel('Palette', { exact: true }).selectOption('monochrome')
  const colours = await page.locator('.state-preview .agent-state-label').evaluateAll(labels => labels.slice(0, 4).map(label => getComputedStyle(label).color))
  expect(new Set(colours).size).toBe(1)
  await page.goto('/agents')
  await page.getByRole('button', { name: /^Stopped/ }).click()
  await expect(page.locator('.row[data-state="stopped"]')).toHaveCSS('opacity', '1')
})

test('all nine variants render every mark and stop problem/inactive motion', async ({ page }) => {
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
})
