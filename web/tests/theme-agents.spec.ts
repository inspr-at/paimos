// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import { resolve } from 'node:path'
import { fixtures, mockWork } from './work-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { expectStableControls } from './helpers/stable'
import { indicatorVariants } from '../src/lib/indicatorVariants'
import type { ThemeRecord } from '../src/lib/themes'

const shots = resolve('test-results/aeon-643')
const initial = (): ThemeRecord => ({
  id: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', tenant_id: 't1', name: 'Porcelain', scope: 'default', owner_principal_id: null, revision: 1, created_at: '', updated_at: '',
  values: { primary: { light: '#0e6f6c', dark: '#a4e5df' }, secondary: { light: '#d69b31', dark: '#e2b45a' }, recurring_marker: { source: 'secondary', custom: null }, agents: { avatar: 'robot-1', ring: null, size: null, hover: false, palette: 'standard' } },
})
async function mock(page: Page, admin = true) {
  const data = fixtures()
  data.preferences['agent-indicator'] = { style: 'robot-5', hovering: true }
  data.preferences['agent-state'] = { palette: 'protan', yellowMinutes: 7, redMinutes: 19 }
  await mockWork(page, data, { admin })
  await mockSettings(page, settingsData())
  const state = { theme: initial(), items: [initial()], selectionRevision: 0, selectedThemeId: null as string | null, writes: [] as ThemeRecord[], fail: false }
  await page.route('**/api/**', async route => {
    const path = new URL(route.request().url()).pathname
    const method = route.request().method()
    const active = () => ({ theme: state.theme, default_theme_id: initial().id, selected_theme_id: state.selectedThemeId, revision: state.selectionRevision, fallback_notice: null })
    if (path === '/api/me/theme') {
      if (method === 'PUT') {
        const body = route.request().postDataJSON()
        expect(body.revision).toBe(state.selectionRevision)
        const target = state.items.find(item => item.id === body.theme_id)
        expect(target).toBeDefined()
        state.theme = target!; state.selectedThemeId = target!.id; state.selectionRevision++
      }
      return route.fulfill({ json: active() })
    }
    if (path === '/api/themes') return route.fulfill({ json: { items: state.items.map(item => item.id === state.theme.id ? state.theme : item), next_cursor: null } })
    if (path.endsWith('/duplicate') && method === 'POST') {
      const source = state.items.find(item => path === `/api/themes/${item.id}/duplicate`)
      expect(source).toBeDefined()
      const created = { ...structuredClone(source!), id: 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb', name: route.request().postDataJSON().name, scope: 'personal' as const, owner_principal_id: 'p1', revision: 1 }
      state.items.push(created)
      return route.fulfill({ json: created })
    }
    if (path === `/api/themes/${state.theme.id}`) {
      if (method === 'PATCH') {
        state.writes.push(route.request().postDataJSON())
        if (state.fail) return route.fulfill({ status: 500, json: { error: 'write failed' } })
        const body = route.request().postDataJSON()
        state.theme = { ...state.theme, name: body.name, values: body.values, revision: state.theme.revision + 1 }
      }
      return route.fulfill({ json: state.theme })
    }
    return route.fallback()
  })
  return { state, data }
}

async function captureAgents(page: Page, width: number, mode: string) {
  const card = page.locator('#agents')
  mkdirSync(shots, { recursive: true })
  // The app scrolls inside main. A card taller than that scrollport cannot be
  // captured as one element without clipping; capture visible sections instead.
  for (const [part, selector] of Object.entries({ avatars: '.avatar-field', motion: '.motion-field', palettes: '.palette-field', previews: '.previews' })) {
    await card.locator(selector).screenshot({ path: resolve(shots, `agents-${width}-${mode}-${part}.png`), animations: 'disabled' })
  }
  await card.locator('.card-head').scrollIntoViewIfNeeded()
  await page.screenshot({ path: resolve(shots, `agents-${width}-${mode}.png`), animations: 'disabled' })
}

for (const width of [390, 1024, 1440]) for (const mode of ['light', 'dark'] as const) {
  test(`${width} ${mode}: nine avatars, stable controls, isolated state previews and saved appearance`, async ({ page }) => {
    const { state, data } = await mock(page)
    data.preferences.theme = { choice: mode }
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ reducedMotion: 'no-preference', colorScheme: mode })
    await page.goto('/settings/theme#agents')
    const card = page.locator('#agents')
    await expect(card.getByRole('radio', { name: 'Robot 1', exact: true })).toBeChecked()
    await expect(card.locator('.avatar-tile')).toHaveCount(9)
    await expect(card.locator('.states .live-bot')).toHaveCount(10)
    // All actual renderers load; none of these tiles uses fragment sketch art.
    await expect(card.locator('.avatar-tile .agent-indicator-art')).toHaveCount(9)
    const robot = card.getByRole('radio', { name: 'Robot 5', exact: true })
    const hover = card.getByRole('switch', { name: 'Hover', exact: true })
    const size = card.getByRole('slider', { name: 'Size', exact: true })
    const dim = card.getByRole('switch', { name: 'Dim inactive', exact: true })
    const reset = card.getByRole('button', { name: 'Use drawn ring and sizes' })
    const moving = card.getByRole('radio', { name: 'Moving', exact: true })
    await expectStableControls({
      controls: { avatarGroup: card.getByRole('radiogroup', { name: 'Agent avatar' }), clickedAvatar: robot, ringGroup: card.getByRole('radiogroup', { name: 'Ring', exact: true }), moving, hover, size, reset, dim, opacity: card.getByRole('slider', { name: 'Opacity' }), paletteGroup: card.getByRole('radiogroup', { name: 'State colours' }) },
      scrollAreas: { card, page: page.locator('html') },
      interactions: [
        ...indicatorVariants.map(variant => ({ name: `avatar ${variant.id}`, run: async () => { await card.getByRole('radio', { name: variant.name, exact: true }).click(); await expect(card.locator('.live-row .live-bot').first()).toHaveAttribute('data-style', variant.id) } })),
        ...['Off', 'Still', 'Moving'].map(name => ({ name: `ring ${name}`, run: async () => { await card.getByRole('radio', { name, exact: true }).click(); await expect(card.locator('.live-row .agent-indicator-art').first()).toHaveAttribute('data-ring', name.toLowerCase()) } })),
        { name: 'hover on', run: () => hover.check() },
        { name: 'size chosen', run: async () => { await size.focus(); await size.press('Home'); await size.press('ArrowRight'); await expect(size).toHaveValue('31') } },
        { name: 'drawn geometry', run: () => reset.click() },
        { name: 'dim off', run: () => dim.uncheck() },
        { name: 'dim on', run: () => dim.check() },
        ...['Protan', 'Deutan', 'Tritan', 'Monochrome', 'Standard'].map(name => ({ name: `palette ${name}`, run: () => card.getByRole('radio', { name, exact: true }).click() })),
      ],
    })
    expect(state.writes).toHaveLength(0)
    await expect(page.getByRole('region', { name: 'Unsaved theme changes' })).toBeVisible()
    await robot.click()
    await card.getByRole('radio', { name: 'Tritan', exact: true }).click()
    // Both modes render their own state tokens regardless of the page mode.
    const colors = await card.locator('.live-row .live-bot').evaluateAll(nodes => nodes.map(el => getComputedStyle(el.querySelector('svg')!).getPropertyValue('--agent-state-color').trim()))
    expect(colors[0]).not.toEqual(colors[1])
    for (const mode of ['light', 'dark']) for (const state of ['waiting', 'throttled', 'problem', 'idle']) {
      expect(await card.locator(`.${mode} [data-preview-state="${state}"] .live-bot`).evaluate(el => el.getAnimations({ subtree: true }).filter(a => a.effect?.getTiming().iterations === Infinity).length)).toBe(0)
    }
    expect(await card.locator('.light [data-preview-state="working"] .live-bot').evaluate(el => el.getAnimations({ subtree: true }).filter(a => a.effect?.getTiming().iterations === Infinity).length)).toBeGreaterThan(0)
    // Draft changes never escape to the signed-in viewer's shared appearance.
    const runtime = await page.evaluate(async () => (await import('/src/lib/agentTheme.ts')).agentTheme.value)
    expect(runtime?.avatar).toBe('robot-1')
    await page.getByRole('button', { name: /^Save/ }).click()
    await expect(page.getByRole('region', { name: 'Unsaved theme changes' })).toHaveCount(0)
    expect(state.writes).toHaveLength(1)
    expect(state.theme.values.agents).toMatchObject({ avatar: 'robot-5', palette: 'tritan', hover: true, ring: null, size: null })
    await expect.poll(() => page.evaluate(async () => (await import('/src/lib/agentTheme.ts')).agentTheme.value?.avatar)).toBe('robot-5')
    mkdirSync(shots, { recursive: true })
    await captureAgents(page, width, mode)
    // Long text stays readable and is captured in each target viewport/mode.
    state.theme.name = 'Agentendarstellung für den gemeinsamen Arbeitsbereich und die persönliche Ansicht'
    await page.reload()
    await expect(card.getByRole('heading')).toContainText(state.theme.name)
    await card.locator('.card-head').screenshot({ path: resolve(shots, `agents-long-${width}-${mode}.png`), animations: 'disabled' })
    await page.goto('/settings/personal#agents')
    await expect(page.locator('#agents').getByRole('slider')).toHaveCount(0)
    await expect(page.getByRole('spinbutton', { name: 'Yellow after (minutes)' })).toHaveValue('7')
    await expect(page.getByRole('spinbutton', { name: 'Red after (minutes)' })).toHaveValue('19')
    await page.locator('#agents').screenshot({ path: resolve(shots, `personal-${width}-${mode}.png`) })
  })
}

test('failed saves preserve the draft and saved appearance; discard restores it', async ({ page }) => {
  const { state } = await mock(page)
  await page.goto('/settings/theme#agents')
  const card = page.locator('#agents')
  await card.getByRole('radio', { name: 'Sprite', exact: true }).click()
  state.fail = true
  await page.getByRole('button', { name: /^Save/ }).click()
  await expect(page.getByRole('alert')).toContainText('theme operation failed')
  await expect(card.getByRole('radio', { name: 'Sprite', exact: true })).toBeChecked()
  expect(state.theme.values.agents.avatar).toBe('robot-1')
  await page.getByRole('button', { name: 'Discard', exact: true }).click()
  await expect(card.getByRole('radio', { name: 'Robot 1', exact: true })).toBeChecked()
  await expect(page.getByRole('region', { name: 'Unsaved theme changes' })).toHaveCount(0)
})

test('members can preview a workspace theme but cannot change it; reduced motion stills all artwork', async ({ page }) => {
  await mock(page, false)
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await page.goto('/settings/theme#agents')
  const card = page.locator('#agents')
  await expect(card.getByRole('radio', { name: 'Robot 1', exact: true })).toBeDisabled()
  await expect(card.getByRole('switch', { name: 'Hover' })).toBeDisabled()
  await expect(card.getByRole('slider', { name: 'Size', exact: true })).toBeDisabled()
  await expect(card).toContainText('read-only')
  await expect(card.locator('.avatar-tile .agent-indicator-art')).toHaveCount(9)
  expect(await card.evaluate(el => el.getAnimations({ subtree: true }).filter(a => a.effect?.getTiming().iterations === Infinity).length)).toBe(0)
})


test('visible screenshots and Personal agent behaviour controls stay stable in all viewport modes', async ({ page }) => {
  const { state, data } = await mock(page)
  for (const width of [390, 1024, 1440]) for (const mode of ['light', 'dark']) {
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ colorScheme: mode, reducedMotion: 'reduce' })
    data.preferences.theme = { choice: mode }
    state.theme.name = 'Porcelain'
    state.theme.values.agents = { avatar: 'robot-5', ring: null, size: null, hover: true, palette: 'tritan' }
    await page.goto('/settings/theme#agents')
    const card = page.locator('#agents')
    await expect(card.locator('.avatar-tile .agent-indicator-art')).toHaveCount(9)
    await expect(card.locator('.states .agent-indicator-art')).toHaveCount(10)
    await captureAgents(page, width, mode)
    state.theme.name = 'Agentendarstellung für den gemeinsamen Arbeitsbereich und die persönliche Ansicht'
    await page.reload()
    await expect(card.getByRole('heading')).toContainText(state.theme.name)
    await card.locator('.card-head').screenshot({ path: resolve(shots, `agents-long-${width}-${mode}.png`), animations: 'disabled' })
    await page.goto('/settings/personal#agents')
    const personal = page.locator('#agents')
    const yellow = personal.getByRole('spinbutton', { name: 'Yellow after (minutes)' })
    const red = personal.getByRole('spinbutton', { name: 'Red after (minutes)' })
    const estimates = personal.getByRole('radiogroup', { name: 'Estimates show as' })
    const relative = estimates.getByRole('radio', { name: 'Relative', exact: true })
    await expectStableControls({
      controls: { yellow, red, estimates, relative, appearanceLink: personal.getByRole('link', { name: 'Avatar, motion, size and state colours in Theme' }) },
      scrollAreas: { personal },
      interactions: [
        { name: 'yellow threshold', run: async () => { await yellow.fill('8'); await yellow.blur(); await expect(yellow).toHaveValue('8') } },
        { name: 'red threshold', run: async () => { await red.fill('20'); await red.blur(); await expect(red).toHaveValue('20') } },
        ...['Clock', 'Both', 'Relative'].map(name => ({ name: `estimates ${name}`, run: async () => { await estimates.getByRole('radio', { name, exact: true }).click(); await expect(estimates.getByRole('radio', { name, exact: true })).toBeChecked() } })),
      ],
    })
    await personal.screenshot({ path: resolve(shots, `personal-${width}-${mode}.png`), animations: 'disabled' })
  }
})

test('Save reconciles appearance after navigating away from Theme while the response is pending', async ({ page }) => {
  const { state } = await mock(page)
  let release!: () => void, reached!: () => void
  const held = new Promise<void>(resolve => { release = resolve })
  const requested = new Promise<void>(resolve => { reached = resolve })
  await page.route(`**/api/themes/${state.theme.id}`, async route => {
    if (route.request().method() === 'PATCH') { reached(); await held }
    await route.fallback()
  })
  await page.goto('/settings/theme#agents')
  await page.getByRole('radio', { name: 'Sprite', exact: true }).click()
  await page.getByRole('button', { name: /^Save/ }).click()
  await requested
  await page.getByRole('link', { name: 'Projects', exact: true }).click()
  await expect(page.locator('.theme-section')).toHaveCount(0)
  expect(await page.evaluate(async () => (await import('/src/lib/agentTheme.ts')).agentTheme.value?.avatar)).toBe('robot-1')
  release()
  await expect.poll(() => state.theme.values.agents.avatar).toBe('sprite')
  await expect.poll(() => page.evaluate(async () => (await import('/src/lib/agentTheme.ts')).agentTheme.value?.avatar)).toBe('sprite')
  expect(state.writes).toHaveLength(1)
})

for (const duplicate of [false, true]) {
  test(`${duplicate ? 'Duplicate-and-select' : 'Selection'} reconciles appearance after navigating away while the response is pending`, async ({ page }) => {
    const { state } = await mock(page)
    const source: ThemeRecord = { ...initial(), id: 'cccccccc-cccc-4ccc-8ccc-cccccccccccc', name: 'Sprite theme', scope: 'personal', owner_principal_id: 'p1', values: { ...initial().values, agents: { ...initial().values.agents, avatar: 'sprite' } } }
    state.items.push(source)
    let release!: () => void, reached!: () => void
    const held = new Promise<void>(resolve => { release = resolve })
    const requested = new Promise<void>(resolve => { reached = resolve })
    await page.route('**/api/me/theme', async route => {
      if (route.request().method() === 'PUT') { reached(); await held }
      await route.fallback()
    })
    await page.goto('/settings/theme#agents')
    await expect(page.getByRole('radio', { name: 'Robot 1', exact: true })).toBeChecked()
    await page.getByRole('button', { name: `${duplicate ? 'Duplicate' : 'Use'} Sprite theme`, exact: true }).click()
    await requested
    await page.getByRole('link', { name: 'Projects', exact: true }).click()
    await expect(page.locator('.theme-section')).toHaveCount(0)
    expect(await page.evaluate(async () => (await import('/src/lib/agentTheme.ts')).agentTheme.value?.avatar)).toBe('robot-1')
    release()
    await expect.poll(() => state.theme.values.agents.avatar).toBe('sprite')
    await expect.poll(() => page.evaluate(async () => (await import('/src/lib/agentTheme.ts')).agentTheme.value?.avatar)).toBe('sprite')
    expect(state.selectedThemeId).toBe(duplicate ? 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb' : source.id)
    expect(state.selectionRevision).toBe(1)
  })
}
