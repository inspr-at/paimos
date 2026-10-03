// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { fixtures, mockWork, watchErrors, liveAgent } from './work-fixtures'
import { expectStableControls } from './helpers/stable'
import { PORCELAIN, deriveDark, inkOn, type ThemeValues } from '../src/lib/themeValues'

const shots = join(process.cwd(), 'test-results', 'aeon-644')
const values = (): ThemeValues => ({ ...structuredClone(PORCELAIN), agents: { ...PORCELAIN.agents, palette: 'tritan' }, primary: { light: '#8547b0', dark: null }, secondary: { light: '#bf3d6d', dark: '#f08db1' } })
async function setup(page: Page) {
  await page.addInitScript(() => { Object.defineProperty(navigator, 'language', { get: () => 'de-AT' }) })
  await page.clock.setFixedTime(new Date('2026-09-23T12:00:00Z'))
  const errors = watchErrors(page), data = fixtures()
  data.nodes.find(n => n.id === 'n-1')!.recurrence = { id: '63700000-0000-4000-8000-000000000001', project_id: 'p-pharos', project_key: 'PHAROS', number: 4, retired: true, trigger: { kind: 'time', rrule: 'FREQ=WEEKLY;BYDAY=MO', time_of_day: '09:00', timezone: 'Europe/Vienna' } }
  data.nodes.find(n => n.id === 'n-1')!.title = 'Regelmäßige Prüfung der umfangreichen Zugangsberechtigungen und der Website'
  data.live.push(liveAgent({ project_id: 'p-pharos', principal_id: 'p-agent', name: 'Farbenprüfung', ticket: { id: 'n-1', key: 'PHAROS-11', title: 'Farbenprüfung', project_id: 'p-pharos' } }))
  await mockWork(page, data)
  return errors
}
async function apply(page: Page, theme: ThemeValues) {
  await page.evaluate(async theme => {
    const path = '/src/lib/appearanceTheme.ts'
    const module = await import(/* @vite-ignore */ path)
    module.applyAppearanceTheme(theme)
  }, theme)
}
async function modeChoice(page: Page, choice: 'light' | 'dark') {
  await page.evaluate(async choice => { const path = '/src/lib/theme.ts'; (await import(/* @vite-ignore */ path)).setTheme(choice, false) }, choice)
}
async function markerColours(page: Page) {
  return page.locator('#row-n-1 .recurrence-dot').evaluate(el => ({ fill: getComputedStyle(el).backgroundColor, ink: getComputedStyle(el).color, width: el.getBoundingClientRect().width }))
}
const cssRgb = (hex: string) => `rgb(${[1, 3, 5].map(i => parseInt(hex.slice(i, i + 2), 16)).join(', ')})`

for (const width of [390, 1024, 1440]) for (const mode of ['light', 'dark'] as const) {
  test(`theme colours and filled ink stay stable at ${width}px in ${mode}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ colorScheme: mode })
    const errors = await setup(page), chosen = values()
    await page.route('**/api/me/theme', route => route.fulfill({ json: { theme: { values: chosen }, fallback_notice: null } }))
    await page.goto('/p/PHAROS?sort=key&group=none')
    const row = page.locator('#row-n-1'), marker = row.locator('.recurrence-dot')
    await expect(marker).toBeVisible()
    const isDark = mode === 'dark', primary = isDark ? deriveDark(chosen.primary.light) : chosen.primary.light
    expect(await page.locator('html').evaluate(el => getComputedStyle(el).getPropertyValue('--teal').trim())).toBe(primary)
    await expectStableControls({ controls: { 'ticket row': row, 'recurring mark': marker, 'mode button': page.getByRole('button', { name: /Switch to .* theme/ }) }, interactions:
      (['primary', 'secondary', 'neutral', 'custom'] as const).map(source => ({ name: `saved ${source} marker`, run: async () => {
        chosen.recurring_marker = { source, custom: '#8547b0' }
        await apply(page, chosen)
        const expected = source === 'primary' ? primary : source === 'secondary' ? (isDark ? chosen.secondary.dark! : chosen.secondary.light) : source === 'neutral' ? (isDark ? '#8aa3a2' : '#7c8c8d') : isDark ? deriveDark('#8547b0') : '#8547b0'
        const colour = await markerColours(page)
        expect(colour).toEqual({ fill: cssRgb(expected), ink: cssRgb(inkOn(expected)), width: 12 })
      } })),
    })
    await expectStableControls({ controls: { 'mode button': page.getByRole('button', { name: /Switch to .* theme/ }), 'ticket row': row }, interactions: [{ name: 'mode switch on list', run: async () => {
      await page.getByRole('button', { name: /Switch to .* theme/ }).click()
      await expect(page.locator('html')).toHaveAttribute('data-theme', isDark ? 'light' : 'dark')
    } }, { name: 'restore list mode', run: async () => {
      await page.getByRole('button', { name: /Switch to .* theme/ }).click()
      await expect(page.locator('html')).toHaveAttribute('data-theme', mode)
    } }] })
    const button = page.getByRole('button', { name: 'New ticket', exact: true })
    expect(await button.evaluate(el => getComputedStyle(el).color)).toBe(cssRgb(inkOn(primary)))
    const mark = page.locator('.agent-state-mark').first()
    await expect(mark).toBeVisible()
    await expect(mark).toHaveAttribute('data-mark', 'working')
    const stateFill = isDark ? '#4fdca5' : '#0f6b3c'
    expect(await mark.locator('circle').evaluate(el => getComputedStyle(el).fill)).toBe(cssRgb(stateFill))
    expect(await mark.locator('path').evaluate(el => getComputedStyle(el).stroke)).toBe(cssRgb(inkOn(stateFill)))
    mkdirSync(shots, { recursive: true })
    await page.screenshot({ path: join(shots, `list-${width}-${mode}.png`) })
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
    await page.screenshot({ path: join(shots, `ticket-${width}-${mode}.png`) })
    expect(errors).toEqual([])
  })
}

test('a reload waits for the saved colours before rendering the app', async ({ page }) => {
  await setup(page)
  const chosen = values()
  let release!: () => void, requested!: () => void
  const barrier = new Promise<void>(resolve => { release = resolve })
  const called = new Promise<void>(resolve => { requested = resolve })
  await page.route('**/api/me/theme', async route => { requested(); await barrier; await route.fulfill({ json: { theme: { values: chosen }, fallback_notice: null } }) })
  await page.goto('/p/PHAROS?sort=key&group=none', { waitUntil: 'domcontentloaded' })
  await called
  expect(await page.locator('#app').evaluate(el => el.childElementCount)).toBe(0)
  expect(await page.locator('body').evaluate(el => getComputedStyle(el).backgroundImage)).toBe('none')
  release()
  await expect(page.locator('#row-n-1')).toBeVisible()
  expect(await page.locator('html').evaluate(el => el.style.getPropertyValue('--teal'))).toBe(chosen.primary.light)
})

test('system mode follows the OS without losing the chosen colours', async ({ page }) => {
  await setup(page)
  const chosen = values()
  await page.emulateMedia({ colorScheme: 'light' })
  await page.route('**/api/me/theme', route => route.fulfill({ json: { theme: { values: chosen }, fallback_notice: null } }))
  await page.goto('/p/PHAROS?sort=key&group=none')
  await expect(page.locator('#row-n-1')).toBeVisible()
  await page.emulateMedia({ colorScheme: 'dark' })
  await expect.poll(() => page.locator('html').evaluate(el => el.style.getPropertyValue('--teal'))).toBe(deriveDark(chosen.primary.light))
  await expect(page.locator('html')).not.toHaveAttribute('data-theme', /.+/)
})

// Exercise the real robot wrapper: its legacy .clock selector used to overwrite
// the filled state mark. A controlled state keeps that CSS regression independent
// of the live-feed projection and its placeholder workers.
test('waiting glyph ink survives the robot clock styles in both modes', async ({ page }) => {
  await setup(page)
  const chosen = values()
  await page.route('**/api/me/theme', route => route.fulfill({ json: { theme: { values: chosen }, fallback_notice: null } }))
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
    const fill = mode === 'light' ? '#b84a08' : '#f5ae52'
    expect(await mark.locator('circle').evaluate(el => getComputedStyle(el).fill)).toBe(cssRgb(fill))
    expect(await mark.locator('path').evaluate(el => getComputedStyle(el).stroke)).toBe(cssRgb(inkOn(fill)))
  }
})
