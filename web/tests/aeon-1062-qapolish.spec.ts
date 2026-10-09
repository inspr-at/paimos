// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1062: polish from INSPR QA of release 128. Risks: the chat header squeezes "Stop now…" to an icon's
// width so its label runs over its neighbours; the dark chat Stop renders light with an unreadable Esc keycap;
// the account drawer says "1 computers"; a Models row whose profile left the registry shows a raw id.
import { test, expect, type Locator, type Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents, type AgentWorld } from './agents-fixtures'
import { NOW, TZ, capacityWorld } from './capacity-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { mockModels, registry } from './models-simple-fixtures'
import { openModelsSettings } from './models-settings-page'
import { controlStability } from './control-stability'
import type { PairingView } from '../src/lib/agentPairing'

test.use({ timezoneId: TZ })
const now = Date.parse('2026-09-29T06:00:00Z')
const themes = ['light', 'dark'] as const
const world: AgentWorld = { me: me.id, now, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' }, nodes: {} }

async function chatSetup(page: Page, theme: 'light' | 'dark') {
  await page.clock.install({ time: now })
  const work = fixtures(); work.preferences.theme = { choice: theme }
  await mockWork(page, work, { admin: true })
  const data = agentData(world)
  const worker = data.sessions[0]!
  // A busy managed session that can pause: Pause…, Stop now…, Recover and Remove… share the header row.
  Object.assign(worker, { display_label: 'release-lead', run_id: null, activity: 'busy', management_mode: 'managed', advertised_capabilities: ['managed_control_v1', 'interrupt', 'stop', 'steer', 'inbox'] })
  data.sessions.splice(1); data.runs.splice(0); data.approvals.splice(0); data.messages.splice(1)
  await mockAgents(page, data)
  return worker
}

/** Buttons in one row never overlap, and none is narrower than its own label. */
async function expectNoOverlap(row: Locator) {
  const boxes = await row.locator('button:visible').evaluateAll(buttons => buttons.map(button => {
    const rect = button.getBoundingClientRect()
    return { name: button.textContent?.trim() || button.getAttribute('aria-label') || '', left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom, clipped: button.scrollWidth > button.clientWidth + 1 }
  }))
  expect(boxes.length).toBeGreaterThan(1)
  for (const box of boxes) expect(box.clipped, `${box.name} fits its label`).toBe(false)
  for (const [index, a] of boxes.entries()) for (const b of boxes.slice(index + 1)) {
    const overlap = a.left < b.right - .5 && b.left < a.right - .5 && a.top < b.bottom - .5 && b.top < a.bottom - .5
    expect(overlap, `${a.name} overlaps ${b.name}`).toBe(false)
  }
}

const luminance = (value: number[]) => {
  const [r, g, b] = value.map(c => c <= .04045 ? c / 12.92 : ((c + .055) / 1.055) ** 2.4)
  return .2126 * r! + .7152 * g! + .0722 * b!
}
const contrast = (a: number[], b: number[]) => { const [x, y] = [luminance(a), luminance(b)].sort((m, n) => n - m); return (x! + .05) / (y! + .05) }

async function stopContrast(stop: Locator) {
  return stop.evaluate(button => {
    // Chromium may serialize color-mix as color(srgb ... / alpha), rather than rgba.
    const context = document.createElement('canvas').getContext('2d')!
    const rgba = (value: string) => {
      context.clearRect(0, 0, 1, 1)
      context.fillStyle = value
      context.fillRect(0, 0, 1, 1)
      return Array.from(context.getImageData(0, 0, 1, 1).data, channel => channel / 255)
    }
    const over = (front: number[], back: number[]) => {
      const alpha = front[3]! + back[3]! * (1 - front[3]!)
      return [...front.slice(0, 3).map((channel, i) => alpha ? (channel * front[3]! + back[i]! * back[3]! * (1 - front[3]!)) / alpha : 0), alpha]
    }
    const backgrounds = (element: Element): number[][] => {
      const style = getComputedStyle(element)
      const color = rgba(style.backgroundColor)
      let layers = [color]
      if (style.backgroundImage !== 'none') {
        // Bound the panel's linear gradient and the canvas's radial washes by
        // their composited endpoints; every intermediate shade must also pass.
        const images: string[] = []
        let depth = 0, start = 0
        for (const [i, character] of Array.from(style.backgroundImage).entries()) {
          if (character === '(') depth++
          if (character === ')') depth--
          if (character === ',' && depth === 0) { images.push(style.backgroundImage.slice(start, i).trim()); start = i + 1 }
        }
        images.push(style.backgroundImage.slice(start).trim())
        for (const image of images.reverse()) {
          const stops = image.match(/(?:rgba?|color)\([^()]+\)/g)
          if (!/^(linear|radial)-gradient\(/.test(image) || !stops || stops.length !== 2) throw new Error(`Unsupported background: ${image}`)
          layers = layers.flatMap(back => stops.map(stop => over(rgba(stop), back)))
        }
      }
      if (layers.every(layer => layer[3] === 1)) return layers
      if (!element.parentElement) throw new Error('No opaque background behind Stop')
      return backgrounds(element.parentElement).flatMap(back => layers.map(front => over(front, back)))
    }
    const keycap = button.querySelector('.keycap')!
    return {
      hovered: button.matches(':hover'),
      ink: rgba(getComputedStyle(button).color).slice(0, 3),
      keycapInk: rgba(getComputedStyle(keycap).color).slice(0, 3),
      backgrounds: backgrounds(button).map(color => color.slice(0, 3)),
      keycapBackgrounds: backgrounds(keycap).map(color => color.slice(0, 3)),
    }
  })
}

// 1600 px is QA's case: the docked panel's default width there is about 620 px.
const cases = [{ width: 390 }, { width: 620 }, { width: 1024 }, { width: 1600, panel: 620 }]
for (const { width, panel } of cases) for (const theme of themes) {
  const label = `${width}${panel ? ` panel ${panel}` : ''}`
  test(`chat header ${theme} ${label}: actions wrap without overlap, stay put, and Stop reads in the theme`, async ({ page }, testInfo) => {
    const worker = await chatSetup(page, theme)
    await page.setViewportSize({ width, height: 900 })
    await page.goto(`/agents/${worker.id}?tab=messages`)
    const frame = page.getByRole('complementary', { name: 'Session details' })
    const composer = frame.getByRole('textbox', { name: 'Message to release-lead' })
    await expect(composer).toBeVisible()
    if (panel) expect(Math.abs(await frame.evaluate(el => el.getBoundingClientRect().width) - panel)).toBeLessThanOrEqual(10)
    const pause = frame.getByRole('button', { name: 'Pause…', exact: true }), stopNow = frame.getByRole('button', { name: 'Stop now…', exact: true })
    const stop = frame.locator('.chat-stop')
    await expect(stop).toBeEnabled()
    await expectNoOverlap(frame.locator(width <= 720 ? '.pause-controls' : '.head-actions'))
    // Pointing at and focusing the header actions never moves them or the composer's controls.
    const guard = await controlStability(page, { pause, stopNow, stop, send: frame.locator('.send') })
    await guard.check(() => pause.hover())
    await guard.check(() => stopNow.hover())
    for (const state of ['normal', 'hovered'] as const) {
      await guard.check(() => state === 'normal' ? pause.hover() : stop.hover())
      const look = await stopContrast(stop)
      expect(look.hovered).toBe(state === 'hovered')
      expect(look.keycapInk).toEqual(look.ink)
      const stopRatio = Math.min(...look.backgrounds.map(background => contrast(look.ink, background)))
      const escRatio = Math.min(...look.keycapBackgrounds.map(background => contrast(look.keycapInk, background)))
      await testInfo.attach(`contrast-${state}`, { body: JSON.stringify({ theme, width, state, stopRatio, escRatio }), contentType: 'application/json' })
      expect.soft(stopRatio, `${theme} ${state} Stop on its composited button background`).toBeGreaterThanOrEqual(4.5)
      expect.soft(escRatio, `${theme} ${state} Esc on its composited keycap background`).toBeGreaterThanOrEqual(4.5)
      if (theme === 'dark') expect(Math.max(...look.backgrounds.map(luminance)), 'dark Stop is not a light slab').toBeLessThan(.2)
    }
    guard.done()
    await page.screenshot({ path: testInfo.outputPath(`chat-header-${theme}-${label.replaceAll(' ', '-')}.png`) })
  })
}

async function accountsSetup(page: Page, theme: 'light' | 'dark') {
  await page.clock.setSystemTime(NOW)
  const work = fixtures(); work.preferences.theme = { choice: theme }
  await mockWork(page, work, { admin: true })
  const data = agentData({ ...world, now: NOW })
  const capacity = capacityWorld({})
  const computers = capacity.computers as unknown as PairingView[]
  for (const c of computers) Object.assign(c, { connectivity: 'online' })
  // One account also signed in on the second computer, so the drawer shows both the singular and the plural.
  computers[1]!.enrollments.push({ ...computers[0]!.enrollments[0]! })
  data.accounts = (capacity.accounts as unknown as typeof data.accounts).map(a => ({ ...a, registered_by_principal_id: me.id }))
  await mockAgents(page, data, { capacity })
  await page.route('**/api/settings/quota-warnings', route => route.fulfill({ json: { early_percent: 5, urgent_percent: 2 } }))
  await page.route('**/api/me/permissions*', route => {
    const answer = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    answer.workspace.permissions = [...answer.workspace.permissions, 'account.read', 'account.manage', 'run.read', 'models.read']
    return route.fulfill({ json: answer })
  })
  return capacity
}

for (const width of [390, 620, 1024]) for (const theme of themes) {
  test(`account drawer ${theme} ${width}: shared quota counts computers in the right number`, async ({ page }, testInfo) => {
    const capacity = await accountsSetup(page, theme)
    await page.setViewportSize({ width, height: 900 })
    await page.goto('/settings/accounts')
    const rows = page.locator('section[aria-labelledby="accounts-list-title"] .list-row')
    await expect(rows.first()).toBeVisible()
    const counts: string[] = []
    for (let index = 0; index < await rows.count(); index++) {
      await rows.nth(index).click()
      const line = page.getByRole('dialog').locator('.p-head').filter({ hasText: 'Shared quota' }).locator('span')
      await expect(line).toHaveText(/^Shared by (1 computer|\d+ computers)$/)
      counts.push(await line.innerText())
      if (index === 0) await page.screenshot({ path: testInfo.outputPath(`account-drawer-${theme}-${width}.png`) })
      await page.keyboard.press('Escape')
    }
    // The fixture has an account on one computer and one on several, so both forms are proven.
    expect(capacity.computers.length).toBeGreaterThan(1)
    expect(counts).toContain('Shared by 1 computer')
    expect(counts.some(text => /^Shared by ([2-9]|\d{2,}) computers$/.test(text))).toBe(true)
  })
}

for (const width of [390, 620, 1024]) for (const theme of themes) {
  test(`Models ${theme} ${width}: a pick without a registry profile shows a name and its family mark, never the raw id`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 900 })
    // The member's Concepts row prefers Fable; the registry no longer has a Fable profile.
    await mockModels(page, { member: true })
    await page.route('**/api/models', route => route.request().method() === 'GET'
      ? route.fulfill({ json: registry().filter(profile => !profile.model.includes('fable')) }) : route.fallback())
    await openModelsSettings(page)
    await page.evaluate(choice => { document.documentElement.dataset.theme = choice }, theme)
    const pick = page.locator('[data-pick="concept"]')
    await expect(pick.locator('.pn')).toHaveText('Fable')
    await expect(pick).toHaveAttribute('aria-label', 'Concepts: Fable (not in the model registry). Change')
    // The Anthropic mark, not the placeholder agent icon.
    await expect(pick.locator('.hg .mark > svg')).toHaveAttribute('viewBox', '0 0 39.6037 39.6037')
    await expect(page.locator('[data-models-section]')).not.toContainText(/\bfable\b/)
    await pick.scrollIntoViewIfNeeded()
    await page.screenshot({ path: testInfo.outputPath(`models-${theme}-${width}.png`) })
  })
}
