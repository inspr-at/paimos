// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import { fixtures, mockWork, watchErrors, liveAgent } from './work-fixtures'
import { expectStableControls } from './helpers/stable'
import { colourContrast as contrastRatio, CARD_COLOURS, PORCELAIN, deriveDark, inkOn, roles } from '../src/lib/themeEngine'
import { AGENT_PALETTES } from '../src/lib/agentPalettes'
import type { ThemeValues } from '../src/lib/themes'
const LIGHT_CARD = CARD_COLOURS.light, DARK_CARD = CARD_COLOURS.dark
import { mockSettings, settingsData } from './settings-fixtures'

let shotPath: (name: string) => string
test.beforeEach(async ({}, testInfo) => { shotPath = name => testInfo.outputPath('aeon-644', name) })
const themeRecord = (values: ThemeValues) => ({ id: 'personal', tenant_id: 't1', name: 'Personal', scope: 'personal', owner_principal_id: 'p-person', revision: 1, created_at: '', updated_at: '', values })
const activeTheme = (values: ThemeValues) => ({ theme: themeRecord(values), default_theme_id: 'default', selected_theme_id: 'personal', revision: 1, fallback_notice: null })
const values = (): ThemeValues => ({ ...structuredClone(PORCELAIN), agents: { ...PORCELAIN.agents, palette: 'tritan' }, primary: { light: '#8547b0', dark: null }, secondary: { light: '#bf3d6d', dark: '#f08db1' } })
async function setup(page: Page) {
  await page.addInitScript(() => { Object.defineProperty(navigator, 'language', { get: () => 'de-AT' }) })
  await page.clock.setFixedTime(new Date('2026-09-23T12:00:00Z'))
  const errors = watchErrors(page), data = fixtures()
  data.nodes.find(n => n.id === 'n-1')!.recurrence = { id: '63700000-0000-4000-8000-000000000001', project_id: 'p-pharos', project_key: 'PHAROS', number: 4, retired: true, trigger: { kind: 'time', rrule: 'FREQ=WEEKLY;BYDAY=MO', time_of_day: '09:00', timezone: 'Europe/Vienna' } }
  data.nodes.find(n => n.id === 'n-1')!.title = 'Regelmäßige Prüfung der umfangreichen Zugangsberechtigungen und der Website'
  data.live.push(liveAgent({ project_id: 'p-pharos', principal_id: 'p-agent', name: 'Farbenprüfung', ticket: { id: 'n-1', key: 'PHAROS-11', title: 'Farbenprüfung', project_id: 'p-pharos' } }))
  await mockWork(page, data)
  // Main restores through the shared editor, so both bounded reads need fixtures.
  await page.route('**/api/themes?**', route => route.fulfill({ json: { items: [themeRecord(PORCELAIN)], next_cursor: null } }))
  return errors
}
async function apply(page: Page, theme: ThemeValues) {
  await page.evaluate(async theme => {
    const path = '/src/lib/themeRuntime.ts'
    const module = await import(/* @vite-ignore */ path)
    module.applyTheme(theme)
  }, theme)
}
async function modeChoice(page: Page, choice: 'light' | 'dark') {
  await page.evaluate(async choice => { const path = '/src/lib/theme.ts'; (await import(/* @vite-ignore */ path)).setTheme(choice, false) }, choice)
}
async function markerColours(page: Page) {
  return page.locator('#row-n-1 .recurrence-dot').evaluate(el => ({ fill: getComputedStyle(el).backgroundColor, ink: getComputedStyle(el).color, width: el.getBoundingClientRect().width }))
}
const cssRgb = (hex: string) => `rgb(${[1, 3, 5].map(i => parseInt(hex.slice(i, i + 2), 16)).join(', ')})`

test('extreme saved accents keep personal appearance links readable in both modes', async ({ page }) => {
  await setup(page)
  await mockSettings(page, settingsData())
  const chosen = values()
  chosen.primary = { light: '#ffffff', dark: '#000000' }
  chosen.secondary = { light: '#fefefe', dark: '#010101' }
  await page.route('**/api/me/theme', route => route.fulfill({ json: activeTheme(chosen) }))
  await page.goto('/settings/personal#agents')
  const link = page.getByRole('link', { name: 'Avatar, motion, size and state colours in Theme' })
  await expect(link).toBeVisible()
  await expectStableControls({
    controls: { link, yellow: page.getByRole('spinbutton', { name: 'Yellow after (minutes)' }), red: page.getByRole('spinbutton', { name: 'Red after (minutes)' }) },
    interactions: (['light', 'dark'] as const).map(mode => ({ name: `extreme ${mode} accent`, run: async () => {
      await modeChoice(page, mode)
      const tokens = await page.locator('html').evaluate(el => {
        const style = getComputedStyle(el)
        return ['--primary', '--primary-ink', '--secondary-ink'].map(name => style.getPropertyValue(name).trim())
      })
      expect(tokens[0]).toBe(mode === 'light' ? '#ffffff' : '#000000')
      for (const text of tokens.slice(1)) expect(contrastRatio(text!, mode === 'light' ? LIGHT_CARD : DARK_CARD)).toBeGreaterThanOrEqual(4.5)
      expect(await link.evaluate(el => getComputedStyle(el).color)).toBe(cssRgb(tokens[1]!))
    } })),
  })
})

for (const width of [390, 1024, 1440]) for (const mode of ['light', 'dark'] as const) {
  test(`theme colours and filled ink stay stable at ${width}px in ${mode}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ colorScheme: mode })
    const errors = await setup(page), chosen = values()
    await page.route('**/api/me/theme', route => route.fulfill({ json: activeTheme(chosen) }))
    await page.goto('/p/PHAROS?sort=key&group=none')
    const row = page.locator('#row-n-1'), marker = row.locator('.recurrence-dot')
    const modeControl = width === 390 ? page.getByRole('button', { name: /^Account for/ }) : page.getByRole('button', { name: /Switch to .* theme/ })
    async function toggleMode() {
      await modeControl.click()
      if (width === 390) {
        await page.getByRole('menuitem', { name: /Switch to .* theme/ }).click()
        await page.keyboard.press('Escape')
      }
    }
    await expect(marker).toBeVisible()
    const isDark = mode === 'dark', primary = isDark ? deriveDark(chosen.primary.light) : chosen.primary.light
    const primaryFill = roles(primary, mode).fill
    expect(await page.locator('html').evaluate(el => getComputedStyle(el).getPropertyValue('--teal').trim())).toBe(primary)
    await expectStableControls({ controls: { 'ticket row': row, 'recurring mark': marker, 'mode button': modeControl }, interactions:
      (['primary', 'secondary', 'neutral', 'custom'] as const).map(source => ({ name: `saved ${source} marker`, run: async () => {
        chosen.recurring_marker = { source, custom: '#8547b0' }
        await apply(page, chosen)
        const expected = source === 'primary' ? primary : source === 'secondary' ? (isDark ? chosen.secondary.dark! : chosen.secondary.light) : source === 'neutral' ? (isDark ? '#8aa3a2' : '#7c8c8d') : isDark ? deriveDark('#8547b0') : '#8547b0'
        const colour = await markerColours(page)
        expect(colour).toEqual({ fill: cssRgb(expected), ink: cssRgb(inkOn(expected)), width: 12 })
      } })),
    })
    await expectStableControls({ controls: { 'mode button': modeControl, 'ticket row': row }, interactions: [{ name: 'mode switch on list', run: async () => {
      await toggleMode()
      await expect(page.locator('html')).toHaveAttribute('data-theme', isDark ? 'light' : 'dark')
    } }, { name: 'restore list mode', run: async () => {
      await toggleMode()
      await expect(page.locator('html')).toHaveAttribute('data-theme', mode)
    } }] })
    const button = page.getByRole('button', { name: 'New ticket', exact: true })
    expect(await button.evaluate(el => getComputedStyle(el).color)).toBe(cssRgb(inkOn(primaryFill)))
    const mark = page.locator('.agent-state-mark').first()
    await expect(mark).toBeVisible()
    await expect(mark).toHaveAttribute('data-mark', 'working')
    const stateFill = AGENT_PALETTES.find(p => p.id === 'tritan')![mode][0]!
    expect(await mark.locator('circle').evaluate(el => getComputedStyle(el).fill)).toBe(cssRgb(stateFill))
    expect(await mark.locator('path').evaluate(el => getComputedStyle(el).stroke)).toBe(cssRgb(inkOn(stateFill)))
    mkdirSync(shotPath(''), { recursive: true })
    await page.screenshot({ path: shotPath(`list-${width}-${mode}.png`) })
    await page.reload()
    await expect(marker).toBeVisible()
    expect((await markerColours(page)).width).toBe(12)
    // The header pill inherits the marker choice too, including the heavier glyph.
    await page.goto('/p/PHAROS/PHAROS-11')
    const pill = page.locator('.ticket-ws .recurring-pill')
    await expect(pill).toBeVisible()
    const headerBadge = pill.locator('.recurring-pill-mark')
    expect(await headerBadge.evaluate(el => ({ width: el.getBoundingClientRect().width, color: getComputedStyle(el).color }))).toEqual({ width: 12, color: cssRgb(inkOn(primary)) })
    await expectStableControls({ controls: { 'recurring pill': pill, 'ticket actions': page.locator('.ticket-ws').getByRole('button', { name: 'More actions', exact: true }), 'ticket key': page.locator('.ticket-ws .key-chip') }, interactions: [{ name: 'change light/dark mode', run: async () => {
      await modeChoice(page, isDark ? 'light' : 'dark')
      await expect(page.locator('html')).toHaveAttribute('data-theme', isDark ? 'light' : 'dark')
    } }, { name: 'restore mode', run: async () => {
      await modeChoice(page, mode)
      await expect(page.locator('html')).toHaveAttribute('data-theme', mode)
    } }] })
    await page.screenshot({ path: shotPath(`ticket-${width}-${mode}.png`) })
    expect(errors).toEqual([])
  })
}

test('a reload waits for the saved colours before rendering the app', async ({ page }) => {
  await setup(page)
  const chosen = values()
  let release!: () => void, requested!: () => void
  const barrier = new Promise<void>(resolve => { release = resolve })
  const called = new Promise<void>(resolve => { requested = resolve })
  await page.route('**/api/me/theme', async route => { requested(); await barrier; await route.fulfill({ json: activeTheme(chosen) }) })
  await page.goto('/p/PHAROS?sort=key&group=none', { waitUntil: 'domcontentloaded' })
  await called
  expect(await page.locator('#app').evaluate(el => el.childElementCount)).toBe(0)
  expect(await page.locator('body').evaluate(el => getComputedStyle(el).backgroundImage)).toBe('none')
  release()
  await expect(page.locator('#row-n-1')).toBeVisible()
  expect(await page.locator('html').evaluate(el => getComputedStyle(el).getPropertyValue('--teal').trim())).toBe(chosen.primary.light)
})

test('system mode follows the OS without losing the chosen colours', async ({ page }) => {
  await setup(page)
  const chosen = values()
  await page.emulateMedia({ colorScheme: 'light' })
  await page.route('**/api/me/theme', route => route.fulfill({ json: activeTheme(chosen) }))
  await page.goto('/p/PHAROS?sort=key&group=none')
  await expect(page.locator('#row-n-1')).toBeVisible()
  await page.emulateMedia({ colorScheme: 'dark' })
  await expect.poll(() => page.locator('html').evaluate(el => getComputedStyle(el).getPropertyValue('--teal').trim())).toBe(deriveDark(chosen.primary.light))
  await expect(page.locator('html')).not.toHaveAttribute('data-theme', /.+/)
})

// Exercise the real robot wrapper: its legacy .clock selector used to overwrite
// the filled state mark. A controlled state keeps that CSS regression independent
// of the live-feed projection and its placeholder workers.
test('waiting glyph ink survives the robot clock styles in both modes', async ({ page }) => {
  await setup(page)
  const chosen = values()
  await page.route('**/api/me/theme', route => route.fulfill({ json: activeTheme(chosen) }))
  await page.goto('/p/PHAROS?sort=key&group=none')
  await expect(page.locator('#row-n-1')).toBeVisible()
  await page.evaluate(async () => {
    const componentPath = '/src/components/projects/LiveBot.vue', vuePath = '/node_modules/.vite/deps/vue.js'
    const [{ default: Bot }, { createApp, h }] = await Promise.all([import(/* @vite-ignore */ componentPath), import(/* @vite-ignore */ vuePath)])
    const host = document.createElement('div'); host.id = 'theme-clock'; document.body.append(host)
    createApp({ render: () => h(Bot, { state: 'waiting', size: 44 }) }).mount(host)
  })
  const mark = page.locator('#theme-clock .agent-state-mark')
  await expect(mark).toHaveAttribute('data-mark', 'waiting')
  for (const mode of ['light', 'dark'] as const) {
    await modeChoice(page, mode)
    const fill = AGENT_PALETTES.find(p => p.id === 'tritan')![mode][1]!
    expect(await mark.locator('circle').evaluate(el => getComputedStyle(el).fill)).toBe(cssRgb(fill))
    expect(await mark.locator('path').evaluate(el => getComputedStyle(el).stroke)).toBe(cssRgb(inkOn(fill)))
  }
})
