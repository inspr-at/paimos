// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { expect, test, type Locator, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { businessData, mockBusiness } from './business-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { PROFILE, mockProfiles, profileWorld } from './profile-fixtures'
import { mockRules, RULE_PROJECT } from './rules-fixtures'
import { expectStableControls } from './helpers/stable'

const shots = 'test-results/aeon-631-headings'
const longName = 'Österreichische Forschungskoordination für generationsübergreifende Infrastruktur und außergewöhnliche Zusammenarbeit'
const unbroken = 'Forschungsinfrastrukturzusammenarbeitskoordinationsverantwortliche'
const longTitle = `${longName} – ${unbroken} – vollständiger Schluss`
const description = 'Diese ausführliche Projektbeschreibung erklärt sämtliche Zusammenhänge und bleibt auch auf schmalen Geräten vollständig erreichbar.'

async function scene(page: Page, width: number, theme: 'light' | 'dark') {
  await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
  await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
}
async function screenshot(page: Page, name: string, width: number, theme: string) {
  mkdirSync(shots, { recursive: true })
  await page.screenshot({ path: `${shots}/${name}-${width}-${theme}.png` })
}
async function fullText(locator: Locator, text: string) {
  await expect(locator).toHaveText(text)
  await expect.poll(() => locator.evaluate(el => Math.max(
    el.scrollWidth - el.clientWidth, el.scrollHeight - el.clientHeight,
  ))).toBeLessThanOrEqual(1)
  expect(await locator.evaluate(el => getComputedStyle(el).whiteSpace)).toBe('normal')
}
async function noOverflow(page: Page) {
  expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(1)
}

for (const width of [390, 768, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
  test(`project description and Done gate headings (${width} ${theme})`, async ({ page }) => {
    const errors = watchErrors(page)
    await scene(page, width, theme)
    const data = fixtures()
    const projectDescription = width === 390 ? description : 'ProjektbeschreibungMitAußergewöhnlichLangemZusammenhängendemBezeichner'
    Object.assign(data.projects[0]!, { title: longTitle, description: projectDescription })
    await mockWork(page, data)
    await page.goto('/p/PHAROS/tickets')
    await fullText(page.locator('#project-title'), longTitle)
    const desc = page.locator('#project-description')
    await expect(desc).toHaveText(projectDescription)
    if (width === 390) {
      const more = page.getByRole('button', { name: 'More', exact: true })
      await expect(more).toBeVisible()
      await expectStableControls({
        controls: { more: page.locator('.description-more') },
        scrollAreas: { heading: page.locator('.head-main') },
        interactions: [
          { name: 'expand description downward', run: async () => { await more.click(); await fullText(desc, description) } },
          { name: 'collapse description', run: async () => { await page.getByRole('button', { name: 'Less', exact: true }).click(); await expect(more).toHaveAttribute('aria-expanded', 'false') } },
        ],
      })
      await more.click()
      await screenshot(page, 'project-expanded', width, theme)
      // Navigation resets the reveal state to the project now on screen.
      await page.goto('/p/AEON/tickets')
      await expect(page.locator('.description-more')).toHaveCount(0)
      await page.goto('/p/PHAROS/tickets')
      await expect(page.locator('.description-more')).toHaveAttribute('aria-expanded', 'false')
    } else {
      // Measure actual clipping, including a short (<120 chars) description.
      const short = projectDescription
      expect(short.length).toBeLessThan(120)
      await desc.evaluate(el => { el.style.maxWidth = '100px' })
      await expect(desc).toHaveAttribute('data-tip', short)
      await page.keyboard.press('Tab')
      await desc.focus()
      await expect(page.locator('.tooltip')).toHaveText(short)
      await desc.evaluate(el => { el.style.maxWidth = 'none' })
      await expect(desc).not.toHaveAttribute('data-tip')
      await desc.blur()
    }
    await screenshot(page, 'project', width, theme)
    await noOverflow(page)

    // Drive the real series API without changing fixtures or relying on timing.
    const ask = async (title: string, index: number) => page.evaluate(async ({ title, index }) => {
      const path = '/src/lib/doneGateAsk.ts'
      const { askDoneGate } = await import(path)
      void askDoneGate({ key: `AEON-${index}`, title, state: 'done', fields: {} }, { index, total: 2 })
    }, { title, index })
    await ask(longTitle, 1)
    const gate = page.locator('dialog.gate')
    await expect(gate).toBeVisible()
    const name = gate.locator('.name')
    await expect(name).toHaveAttribute('data-tip', longTitle)
    await page.keyboard.press('Tab')
    await name.focus()
    await expect(gate.locator('.tooltip')).toHaveText(longTitle)
    await screenshot(page, 'done-gate', width, theme)
    await expectStableControls({
      controls: { cancel: gate.getByRole('button', { name: 'Not now' }), submit: gate.getByRole('button', { name: 'Mark done' }), ...(width === 390 ? { frame: gate.locator('.card') } : {}) },
      scrollAreas: { card: gate.locator('.card'), body: gate.locator('.scroll') },
      interactions: [
        { name: 'series advances to short title', run: async () => { await ask('Kurzer Titel', 2); await expect(name).toHaveText('Kurzer Titel'); await expect(name).not.toHaveAttribute('data-tip') } },
        { name: 'series returns to long title', run: async () => { await ask(longTitle, 1); await expect(name).toHaveAttribute('data-tip', longTitle) } },
        { name: 'form validation', run: async () => { await gate.getByRole('button', { name: 'Mark done' }).click(); await expect(gate.getByLabel('Pill · English')).toHaveAttribute('aria-invalid', 'true') } },
      ],
    })
    expect(errors).toEqual([])
  })

  test(`session and account identities (${width} ${theme})`, async ({ page }) => {
    const errors = watchErrors(page)
    await scene(page, width, theme)
    await mockWork(page, fixtures(), { admin: true })
    const data = agentData({ me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' } })
    Object.assign(data.sessions[0]!, { display_label: longTitle })
    data.accounts[0]!.label = longName
    await mockAgents(page, data)
    await page.route('**/api/me/permissions*', route => {
      const permissions = mockEffectivePermissions('admin')
      permissions.workspace.permissions.push('account.read', 'account.manage')
      return route.fulfill({ json: permissions })
    })
    await page.goto(`/agents/${data.sessions[0]!.id}`)
    const panel = page.getByRole('complementary', { name: 'Session details' })
    await expect(panel).toBeVisible()
    await fullText(panel.locator('.name'), longTitle)
    const close = panel.getByRole('button', { name: 'Close session details' })
    await expectStableControls({ controls: { close }, scrollAreas: { panel }, interactions: [{ name: 'hover wrapped session name', run: () => panel.locator('.name').hover() }] })
    await screenshot(page, 'session', width, theme)
    await noOverflow(page)
    await page.goto('/settings/accounts')
    await page.getByRole('button', { name: `Details for ${longName}`, exact: true }).click()
    const name = page.locator('.detail .nm')
    await fullText(name, longName)
    const rename = page.getByRole('button', { name: `Rename ${longName}`, exact: true })
    await expectStableControls({ controls: { rename }, scrollAreas: { details: page.locator('.detail') }, interactions: [{ name: 'hover wrapped account name', run: () => name.hover() }] })
    await screenshot(page, 'account', width, theme)
    await noOverflow(page)
    expect(errors).toEqual([])
  })

  test(`document profile and rules preview headings (${width} ${theme})`, async ({ page }) => {
    const errors = watchErrors(page)
    await scene(page, width, theme)
    await mockWork(page, fixtures())
    await mockBusiness(page, businessData({ enabled: ['business_costs', 'business_crm', 'business_quotes', 'business_hours'] }))
    await mockSettings(page, settingsData())
    const world = profileWorld()
    for (const revision of world.profiles[0]!.revisions) revision.name = longName
    await mockProfiles(page, world)
    await page.goto(`/settings/business/profiles/${PROFILE.steel}`)
    await fullText(page.locator('#profiles-title'), longName)
    const field = page.getByLabel('Name', { exact: true })
    await expect(field).toBeVisible()
    await expectStableControls({
      controls: { save: page.locator('.head-actions .primary'), archive: page.locator('.head-actions [aria-label="Archive"]'), back: page.getByRole('link', { name: 'Back to Business settings' }), ...(width < 860 ? { switch: page.locator('.pane-switch') } : {}) },
      scrollAreas: { header: page.locator('.profiles-head') },
      interactions: [
        { name: 'short live profile name', run: async () => { await field.fill('Kurz'); await expect(page.locator('#profiles-title')).toHaveText('Kurz') } },
        { name: 'long unbroken live profile name', run: async () => { await field.fill(unbroken); await fullText(page.locator('#profiles-title'), unbroken) } },
      ],
    })
    await field.fill(longName.slice(0, 100))
    await screenshot(page, 'profile', width, theme)
    await noOverflow(page)

    await mockRules(page)
    await page.route('**/api/projects*', route => route.fulfill({ json: { items: [{ id: RULE_PROJECT, key: 'AEON', title: longTitle, state: 'active', open: 1, in_progress: 0, done: 0, cancelled: 0, total: 1, last_activity: '2026-09-28T10:00:00Z', people: [] }] } }))
    await page.goto('/settings/agent-rules')
    await page.getByRole('button', { name: 'Preview', exact: true }).click()
    const preview = page.getByRole('dialog', { name: 'What agents receive' })
    // Wait for RulesDialog's scheduled autofocus before testing keyboard tips.
    await expect(preview.locator('.for .btn')).toBeFocused()
    const identity = preview.locator('.for-value')
    const forText = `${longTitle} · Markus Barta · Builder`
    await expect(identity).toHaveText(forText)
    const clipped = await identity.evaluate(el => el.scrollHeight > el.clientHeight + 1)
    if (clipped) {
      await expect(identity).toHaveAttribute('data-tip', forText)
      await page.keyboard.press('Tab')
      await identity.focus()
      await expect(preview.locator('.tooltip')).toHaveText(forText)
    } else await fullText(identity, forText)
    await expectStableControls({
      controls: { change: preview.locator('.for .btn'), close: preview.getByRole('button', { name: 'Close what agents receive' }) },
      scrollAreas: { body: preview.locator('.body') },
      interactions: [
        { name: 'show identity selectors', run: async () => { await preview.locator('.for .btn').click(); await expect(preview.getByRole('group', { name: 'Preview for' })).toBeVisible() } },
        { name: 'close identity selectors', run: async () => { await preview.locator('.for .btn').click(); await expect(preview.getByRole('group', { name: 'Preview for' })).toHaveCount(0) } },
      ],
    })
    await screenshot(page, 'rules', width, theme)
    await noOverflow(page)
    expect(errors).toEqual([])
  })

  test(`release subtitles wrap and carousel controls stay still (${width} ${theme})`, async ({ page }) => {
    await scene(page, width, theme)
    await mockWork(page, fixtures())
    await page.goto('/p/PHAROS/tickets')
    // Mount the actual card at its narrow desktop width, with app tokens/fonts.
    await page.evaluate(async text => {
      const vuePath = '/node_modules/.vite/deps/vue.js'
      const cardPath = '/src/components/releases/ReleaseStatCard.vue'
      const { createApp, h } = await import(vuePath)
      const { default: Card } = await import(cardPath)
      document.querySelector<HTMLElement>('#app')!.style.display = 'none'
      const root = document.createElement('div')
      root.style.cssText = 'width:min(340px,calc(100vw - 32px));margin:32px auto'
      document.body.append(root)
      createApp({ render: () => h(Card, { stats: [
        { key: 'since', label: 'Seit der Veröffentlichung', value: '12 Stunden', sub: `${text} · 3. Oktober 2026, 09:41:23 Uhr`, chips: [], viz: { kind: 'stacked', bars: [{ features: 1, fixes: 0 }], from: 'Oktober' } },
        { key: 'features', label: 'Verbesserungen', value: '4', sub: 'Kurzer Untertitel', chips: [], viz: { kind: 'stacked', bars: [{ features: 1, fixes: 0 }], from: 'Oktober' } },
      ] }) }).mount(root)
    }, longName)
    const card = page.getByRole('region', { name: 'Release stats' })
    const subtitle = card.locator('.sub')
    await fullText(subtitle, `${longName} · 3. Oktober 2026, 09:41:23 Uhr`)
    const prev = card.getByRole('button', { name: 'Previous stat' }), next = card.getByRole('button', { name: 'Next stat' }), pause = card.getByRole('button', { name: /automatic rotation/ })
    await card.hover()
    await expect(next).toHaveCSS('opacity', '1')
    await screenshot(page, 'release-stat', width, theme)
    await expectStableControls({
      controls: { prev, next, pause }, scrollAreas: { card },
      interactions: [
        { name: 'next shorter subtitle', run: async () => { await next.click(); await expect(subtitle).toHaveText('Kurzer Untertitel') } },
        { name: 'previous full date and time', run: async () => { await prev.click(); await expect(subtitle).toContainText('09:41:23 Uhr') } },
      ],
    })
    await noOverflow(page)
  })
}
