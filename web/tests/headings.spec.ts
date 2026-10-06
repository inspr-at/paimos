// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { expect, test, type Locator, type Page } from '@playwright/test'
import { fixtures, me, mockView, mockWork, watchErrors } from './work-fixtures'
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
async function fittingTip(tip: Locator, text: string) {
  await expect(tip).toHaveText(text)
  await expect.poll(() => tip.evaluate(el => Math.max(el.scrollWidth - el.clientWidth, el.scrollHeight - el.clientHeight))).toBeLessThanOrEqual(1)
  await expect.poll(() => tip.evaluate(el => {
    const box = el.getBoundingClientRect()
    const range = document.createRange()
    range.selectNodeContents(el)
    const rects = Array.from(range.getClientRects())
    return box.left >= 0 && box.top >= 0 && box.right <= innerWidth && box.bottom <= innerHeight &&
      rects.length > 0 && rects.every(rect => rect.left >= box.left - 1 && rect.right <= box.right + 1 && rect.top >= box.top - 1 && rect.bottom <= box.bottom + 1)
  })).toBe(true)
}
async function twoLineTouchTarget(locator: Locator) {
  expect((await locator.boundingBox())!.height).toBeGreaterThanOrEqual(44)
  const extra = await locator.evaluate(el => Math.abs(el.clientHeight - 2 * parseFloat(getComputedStyle(el).lineHeight)))
  expect(extra, 'a two-line identity must not expose part of a third line').toBeLessThanOrEqual(1)
}
async function profileHeading(page: Page, text: string) {
  const heading = page.locator('#profiles-title')
  await expect(heading).toHaveText(text)
  expect(await heading.evaluate(el => getComputedStyle(el).whiteSpace)).toBe('normal')
  if (await heading.evaluate(el => el.scrollHeight > el.clientHeight + 1)) {
    await expect(heading).toHaveAttribute('data-heading-tip', text)
    await page.keyboard.press('Tab')
    await heading.focus()
    await fittingTip(page.locator('.tooltip'), text)
  } else await fullText(heading, text)
}
async function noOverflow(page: Page) {
  expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(1)
}

// These checks also run against 96593019: the existing tooltip must first
// contain the exact identity, then fail specifically on viewport containment.
test.describe('bounded heading reader', () => {
  test.use({ hasTouch: true })
  for (const surface of ['done-gate', 'rules', 'profile'] as const) {
    for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
      for (const input of (width === 390 ? ['keyboard', 'touch'] : ['keyboard']) as ('keyboard' | 'touch')[]) {
        test(`short viewport ${surface} heading stays readable by ${input} (${width} ${theme})`, async ({ page, context }) => {
          await scene(page, width, theme)
          await page.setViewportSize({ width, height: 400 })
          await mockWork(page, fixtures())
          const text = `${longTitle.repeat(12)} Vollständiger Schluss.`
          let heading: Locator, controls: Record<string, Locator>
          let shown = text
          if (surface === 'done-gate') {
            await page.goto('/p/PHAROS/tickets')
            await expect(page.locator('dialog.gate')).toBeAttached()
            await page.evaluate(async title => {
              const path = '/src/lib/doneGateAsk.ts'
              const { askDoneGate } = await import(path)
              void askDoneGate({ key: 'AEON-631', title, state: 'done', fields: {} })
            }, text)
            const gate = page.locator('dialog.gate')
            await expect(gate).toBeVisible()
            await expect(gate.getByLabel('Pill · English')).toBeFocused()
            heading = gate.locator('.name')
            controls = { heading, cancel: gate.getByRole('button', { name: 'Not now' }), submit: gate.getByRole('button', { name: 'Mark done' }) }
          } else if (surface === 'rules') {
            await mockRules(page)
            await page.route('**/api/projects*', route => route.fulfill({ json: { items: [{ id: RULE_PROJECT, key: 'AEON', title: text, state: 'active', open: 1, in_progress: 0, done: 0, cancelled: 0, total: 1, last_activity: '2026-09-28T10:00:00Z', people: [] }] } }))
            await page.goto('/settings/agent-rules')
            await page.getByRole('button', { name: 'Preview', exact: true }).click()
            const preview = page.getByRole('dialog', { name: 'What agents receive' })
            await expect(preview.locator('.for .btn')).toBeFocused()
            await expect(preview.locator('.summary')).toBeVisible()
            heading = preview.locator('.for-value')
            controls = { heading, change: preview.locator('.for .btn'), close: preview.getByRole('button', { name: 'Close what agents receive' }) }
            shown = `${text} · Markus Barta · Builder`
          } else {
            await mockBusiness(page, businessData({ enabled: ['business_costs', 'business_crm', 'business_quotes', 'business_hours'] }))
            await mockSettings(page, settingsData())
            const world = profileWorld()
            for (const revision of world.profiles[0]!.revisions) revision.name = text
            await mockProfiles(page, world)
            await page.goto(`/settings/business/profiles/${PROFILE.steel}`)
            heading = page.locator('#profiles-title')
            controls = { heading, save: page.locator('.head-actions .primary'), back: page.getByRole('link', { name: 'Back to Business settings' }) }
          }
          await expect(heading).toHaveText(shown)
          await heading.evaluate(async () => { await document.fonts.ready })
          await page.mouse.move(0, 0)
          if (input === 'keyboard') { await page.keyboard.press('Tab'); await heading.focus() }
          const reader = page.locator('.tooltip')
          await expectStableControls({ controls, interactions: [
            { name: 'reveal complete identity', run: async () => {
              if (input === 'touch') await heading.tap()
              else await heading.focus()
              await expect(reader).toHaveText(shown)
              await expect.poll(() => reader.evaluate(el => {
                const box = el.getBoundingClientRect()
                return box.top >= 8 && box.bottom <= innerHeight - 8 && box.left >= 8 && box.right <= innerWidth - 8
              }), { message: 'full heading disclosure must remain inside the short viewport' }).toBe(true)
              expect(await reader.evaluate(el => el.scrollHeight)).toBeGreaterThan(await reader.evaluate(el => el.clientHeight))
              await expect(reader).toHaveAttribute('tabindex', '0')
            } },
            { name: `${input} scroll reaches the last words`, run: async () => {
              if (input === 'keyboard') {
                await page.keyboard.press('ArrowDown')
                await expect(reader).toBeFocused()
                await page.keyboard.press('ArrowDown')
                await expect(reader).toHaveText(shown)
                await expect.poll(() => reader.evaluate(el => el.scrollTop)).toBeGreaterThan(0)
                await page.keyboard.press('End')
              } else {
                const cdp = await context.newCDPSession(page)
                try {
                  const box = (await reader.boundingBox())!, x = box.x + box.width / 2
                  const from = box.y + box.height - 20, to = box.y + 20
                  await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x, y: from }] })
                  for (let step = 1; step <= 8; step++) {
                    await cdp.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x, y: from + (to - from) * step / 8 }] })
                  }
                  await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] })
                } finally { await cdp.detach() }
                await expect(reader).toHaveText(shown)
                await expect.poll(() => reader.evaluate(el => el.scrollTop)).toBeGreaterThan(0)
                await reader.evaluate(el => { el.scrollTop = el.scrollHeight })
              }
              await expect.poll(() => reader.evaluate(el => Math.abs(el.scrollHeight - el.scrollTop - el.clientHeight))).toBeLessThanOrEqual(1)
              expect(await reader.evaluate(el => {
                const range = document.createRange(), node = el.firstChild!
                range.setStart(node, (node.textContent?.length ?? 0) - 40)
                range.setEnd(node, node.textContent?.length ?? 0)
                const tail = range.getBoundingClientRect(), box = el.getBoundingClientRect()
                return tail.top >= box.top && tail.bottom <= box.bottom && tail.right <= box.right
              }), 'last words must be visible after End').toBe(true)
              await screenshot(page, `${surface}-short-viewport-${input}`, width, theme)
            } },
            { name: 'Escape returns to the heading', run: async () => {
              await page.keyboard.press('Escape')
              await expect(reader).toHaveCount(0)
              await expect(heading).toBeFocused()
            } },
            ...(input === 'keyboard' ? [{ name: 'hover also stays within the viewport', run: async () => {
              await heading.blur()
              await heading.hover()
              await expect(reader).toHaveText(shown)
              expect(await reader.evaluate(el => {
                const box = el.getBoundingClientRect()
                return box.top >= 8 && box.bottom <= innerHeight - 8 && box.left >= 8 && box.right <= innerWidth - 8
              })).toBe(true)
              await page.mouse.move(0, 0)
              await expect(reader).toHaveCount(0)
            } }] : []),
          ] })
          await noOverflow(page)
        })
      }
    }
  }
})
function projectControls(page: Page, width: number) {
  return {
    tabs: page.getByRole('tablist', { name: 'Project sections' }),
    views: page.getByRole('navigation', { name: 'Saved views' }),
    modes: page.getByRole('tablist', { name: 'Ticket views' }),
    search: page.getByRole('searchbox', { name: 'Search tickets in this project' }),
    ...(width > 900 ? { display: page.getByRole('button', { name: /^Display:/ }) } : { filters: page.getByRole('button', { name: 'Filters', exact: true }) }),
    add: page.getByRole('button', { name: 'New ticket', exact: true }),
  }
}

// Substitute translations at the compiled-module boundary: the real component
// still owns both labels and every state change, without adding test-only props.
for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
  for (const language of ['English', 'German'] as const) {
    test(`Rules Preview toggle fits intrinsic labels (${width} ${theme} ${language})`, async ({ page }) => {
      const errors = watchErrors(page)
      await scene(page, width, theme)
      await mockWork(page, fixtures())
      await mockRules(page)
      const change = language === 'German' ? 'Vorschau ändern' : 'Change'
      const done = language === 'German' ? 'Auswahl übernehmen' : 'Done'
      if (language === 'German') {
        await page.route('**/src/components/rules/RulesPreview.vue', async route => {
          const response = await route.fetch()
          const body = (await response.text())
            .replace(/(['"])Change\1/g, JSON.stringify(change))
            .replace(/(['"])Done\1/g, JSON.stringify(done))
          await route.fulfill({ response, body })
        })
      }
      await page.goto('/settings/agent-rules')
      await page.getByRole('button', { name: 'Preview', exact: true }).click()
      const preview = page.getByRole('dialog', { name: 'What agents receive' })
      const toggle = preview.locator('.for .btn')
      await expect(toggle).toBeFocused()
      await expect(preview.locator('.summary')).toBeVisible()
      await toggle.evaluate(async () => { await document.fonts.ready })
      const labelFits = async (label: string, expanded: boolean) => {
        // Accessibility must expose only the current label, including after
        // toggling; text ranges prove its glyphs fit inside the padded button.
        await expect(toggle).toHaveAccessibleName(label)
        await expect(toggle).toHaveAttribute('aria-expanded', String(expanded))
        expect(await toggle.evaluate(el => {
          const box = el.getBoundingClientRect(), style = getComputedStyle(el)
          const left = box.left + parseFloat(style.borderLeftWidth) + parseFloat(style.paddingLeft)
          const right = box.right - parseFloat(style.borderRightWidth) - parseFloat(style.paddingRight)
          const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT)
          const rects: DOMRect[] = []
          while (walker.nextNode()) {
            const node = walker.currentNode
            if (!node.textContent?.trim() || getComputedStyle(node.parentElement!).visibility === 'hidden') continue
            const range = document.createRange()
            range.selectNodeContents(node)
            rects.push(...Array.from(range.getClientRects()))
          }
          return rects.length > 0 && rects.every(rect => rect.width > 0 && rect.height > 0 &&
            rect.left >= left - 1 && rect.right <= right + 1 && rect.top >= box.top && rect.bottom <= box.bottom)
        }), 'the complete active label must fit inside the button padding').toBe(true)
      }
      await labelFits(change, false)
      await screenshot(page, `rules-toggle-${language.toLowerCase()}-change`, width, theme)
      await expectStableControls({
        controls: {
          toggle, identity: preview.locator('.for-value'), identityRow: preview.locator('.for'),
          close: preview.getByRole('button', { name: 'Close what agents receive' }),
        },
        scrollAreas: { body: preview.locator('.body') },
        interactions: [
          { name: 'show identity selectors', run: async () => {
            await toggle.click()
            await expect(preview.getByRole('group', { name: 'Preview for' })).toBeVisible()
            await labelFits(done, true)
            await screenshot(page, `rules-toggle-${language.toLowerCase()}-done`, width, theme)
          } },
          { name: 'close identity selectors by keyboard', run: async () => {
            await expect(toggle).toBeFocused()
            await page.keyboard.press('Space')
            await expect(preview.getByRole('group', { name: 'Preview for' })).toHaveCount(0)
            await labelFits(change, false)
          } },
        ],
      })
      await noOverflow(page)
      expect(errors).toEqual([])
    })
  }
}

for (const width of [390, 768, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
  test(`project description and Done gate headings (${width} ${theme})`, async ({ page }) => {
    const errors = watchErrors(page)
    await scene(page, width, theme)
    const data = fixtures()
    data.views.push(mockView({ id: 'heading-view', name: 'Saved heading view' }))
    const projectDescription = width === 390 ? description : 'ProjektbeschreibungMitLangemBezeichner'
    Object.assign(data.projects[0]!, { title: longTitle, description: projectDescription })
    await mockWork(page, data)
    await page.goto('/p/PHAROS/tickets')
    // AEON-639's compact title keeps two lines and exposes the full identity.
    const title = page.locator('#project-title')
    await expect(title).toHaveAttribute('data-tip', longTitle)
    await page.keyboard.press('Tab')
    await title.focus()
    await fittingTip(page.locator('.tooltip'), longTitle)
    await title.blur()
    // The wide reading layout gives this clipping fixture its original room.
    if (width !== 390) await page.getByRole('radio', { name: 'Comfortable project header', exact: true }).click()
    const desc = page.locator('#project-description')
    await expect(desc).toHaveText(projectDescription)
    if (width === 390) {
      const more = page.getByRole('button', { name: 'More', exact: true })
      await expect(more).toBeVisible()
      await expectStableControls({
        controls: { more: page.locator('.description-more'), ...projectControls(page, width) },
        scrollAreas: { heading: page.locator('.head-main') },
        interactions: [
          { name: 'reveal full description', run: async () => { await more.click(); await expect(page.locator('.description-more')).toHaveAttribute('aria-expanded', 'true') } },
          { name: 'collapse description', run: async () => { await page.getByRole('button', { name: 'Less', exact: true }).click(); await expect(more).toHaveAttribute('aria-expanded', 'false') } },
        ],
      })
      await more.click()
      const revealed = page.locator('#project-description-full')
      await fullText(revealed, description)
      expect(await revealed.evaluate(el => {
        const box = el.getBoundingClientRect()
        return el.contains(document.elementFromPoint(box.left + box.width / 2, box.top + box.height / 2))
      }), 'full description must paint above project stats').toBe(true)
      await screenshot(page, 'project-expanded', width, theme)
      await page.keyboard.press('Escape')
      await expect(revealed).toHaveCount(0)
      await expect(more).toBeFocused()
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
      await fittingTip(page.locator('.tooltip'), short)
      await desc.evaluate(el => { el.style.maxWidth = 'none' })
      // Compact keeps a narrow summary slot even after the test width is removed.
      // Disclosure follows actual clipping, including the full-width narrow layout.
      const stillClipped = await desc.evaluate(el => el.scrollWidth > el.clientWidth + 1)
      if (stillClipped) await expect(desc).toHaveAttribute('data-tip', short)
      else await expect(desc).not.toHaveAttribute('data-tip')
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
    await expect(name).toHaveAttribute('data-heading-tip', longTitle)
    await page.keyboard.press('Tab')
    await name.focus()
    await fittingTip(gate.locator('.tooltip'), longTitle)
    await screenshot(page, 'done-gate', width, theme)
    await expectStableControls({
      controls: { cancel: gate.getByRole('button', { name: 'Not now' }), submit: gate.getByRole('button', { name: 'Mark done' }), ...(width === 390 ? { frame: gate.locator('.card') } : {}) },
      scrollAreas: { card: gate.locator('.card'), body: gate.locator('.scroll') },
      interactions: [
        { name: 'series advances to short title', run: async () => { await ask('Kurzer Titel', 2); await expect(name).toHaveText('Kurzer Titel'); await expect(name).not.toHaveAttribute('data-heading-tip') } },
        { name: 'series returns to long title', run: async () => { await ask(longTitle, 1); await expect(name).toHaveAttribute('data-heading-tip', longTitle) } },
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
    // AEON-686: an account's logins open in its docked panel.
    await page.locator(`.list-row[data-accounts~="${data.accounts[0]!.id}"]`).click()
    await page.locator('section.pane').getByRole('button', { name: `Details for ${longName}`, exact: true }).click()
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
    await profileHeading(page, longName)
    const field = page.getByLabel('Name', { exact: true })
    await expect(field).toBeVisible()
    await expectStableControls({
      controls: { name: field, sections: page.getByRole('navigation', { name: 'Profile sections' }), basics: page.getByRole('button', { name: 'Basics', exact: true }), save: page.locator('.head-actions .primary'), archive: page.locator('.head-actions [aria-label="Archive"]'), back: page.getByRole('link', { name: 'Back to Business settings' }), ...(width < 860 ? { switch: page.locator('.pane-switch') } : {}) },
      scrollAreas: { header: page.locator('.profiles-head') },
      interactions: [
        { name: 'short live profile name', run: async () => { await field.fill('Kurz'); await expect(page.locator('#profiles-title')).toHaveText('Kurz') } },
        { name: 'long unbroken live profile name', run: async () => { await field.fill(unbroken); await profileHeading(page, unbroken) } },
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
    await expect(preview.locator('.summary')).toBeVisible()
    await preview.evaluate(async el => {
      await document.fonts.ready
      await Promise.all(el.getAnimations({ subtree: true }).map(animation => animation.finished.catch(() => {})))
    })
    // Keep incidental mouse hover from competing with this keyboard scenario.
    await page.mouse.move(0, 0)
    const identity = preview.locator('.for-value')
    const forText = `${longTitle} · Markus Barta · Builder`
    await expect(identity).toHaveText(forText)
    const clipped = await identity.evaluate(el => el.scrollHeight > el.clientHeight + 1)
    if (clipped) {
      await expect(identity).toHaveAttribute('data-heading-tip', forText)
      // The identity immediately precedes Change in the real tab order.
      await page.keyboard.press('Shift+Tab')
      await expect(identity).toBeFocused()
      await expect(identity).toHaveAttribute('data-clip-tip', '')
      await fittingTip(preview.locator('.tooltip'), forText)
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

// A focused name can become clipped after a responsive layout change. The
// shared host must observe the disclosure appearing without a second focusin.
for (const theme of ['light', 'dark'] as const) {
  test(`Rules Preview refreshes a keyboard-focused identity after clipping (${theme})`, async ({ page }) => {
    await scene(page, 390, theme)
    await mockWork(page, fixtures())
    await mockRules(page)
    await page.route('**/api/projects*', route => route.fulfill({ json: { items: [{ id: RULE_PROJECT, key: 'AEON', title: longTitle, state: 'active', open: 1, in_progress: 0, done: 0, cancelled: 0, total: 1, last_activity: '2026-09-28T10:00:00Z', people: [] }] } }))
    await page.goto('/settings/agent-rules')
    await page.getByRole('button', { name: 'Preview', exact: true }).click()
    const preview = page.getByRole('dialog', { name: 'What agents receive' })
    const identity = preview.locator('.for-value'), tip = preview.locator('.tooltip')
    await expect(preview.locator('.for .btn')).toBeFocused()
    await expect(preview.locator('.summary')).toBeVisible()
    await page.mouse.move(0, 0)
    await identity.evaluate(el => { el.style.height = 'auto'; el.style.webkitLineClamp = 'unset' })
    await expect(identity).not.toHaveAttribute('data-heading-tip')
    await page.keyboard.press('Shift+Tab')
    await expect(identity).toBeFocused()
    await expect(tip).toHaveCount(0)
    await identity.evaluate(el => { el.style.removeProperty('height'); el.style.removeProperty('-webkit-line-clamp') })
    await expect(identity).toHaveAttribute('data-heading-tip', `${longTitle} · Markus Barta · Builder`)
    await fittingTip(tip, `${longTitle} · Markus Barta · Builder`)
    await expect(identity).toBeFocused()
  })
}

// Real touch input does not synthesize keyboard :focus-visible or mouse hover.
test.describe('touch full-text reveal', () => {
  test.use({ hasTouch: true })
  for (const width of [768, 1024]) for (const theme of ['light', 'dark'] as const) {
    test(`project description tap reveals without moving controls (${width} ${theme})`, async ({ page }) => {
      const errors = watchErrors(page)
      await scene(page, width, theme)
      const data = fixtures()
      data.views.push(mockView({ id: 'heading-view', name: 'Saved heading view' }))
      const text = `${description} ${description} ${description}`
      data.projects[0]!.description = text
      await mockWork(page, data)
      await page.goto('/p/PHAROS/tickets')
      const desc = page.locator('#project-description'), full = page.locator('#project-description-full')
      const more = page.locator('.description-more')
      await expect(more).toBeVisible()
      expect((await desc.boundingBox())!.height).toBeGreaterThanOrEqual(44)
      await expectStableControls({
        controls: { more, description: desc, ...projectControls(page, width) },
        scrollAreas: { header: page.locator('.head-main') },
        interactions: [
          { name: 'tap More', run: async () => { await more.tap(); await fullText(full, text); await screenshot(page, 'project-touch', width, theme) } },
          { name: 'tap Less to dismiss', run: async () => { await more.tap(); await expect(full).toHaveCount(0) } },
          { name: 'tap then outside', run: async () => { await more.tap(); await fullText(full, text); await page.locator('#project-title').tap(); await expect(full).toHaveCount(0) } },
        ],
      })
      await noOverflow(page)
      expect(errors).toEqual([])
    })
  }
  // Native touch input proves that pointercancel from scrolling is safe.
  for (const width of [768, 1024]) for (const theme of ['light', 'dark'] as const) {
    for (const input of ['touch', 'keyboard'] as const) {
      test(`viewport-long description keeps ${input} scrolling (${width} ${theme})`, async ({ page, context }) => {
        await scene(page, width, theme)
        const data = fixtures(), text = `${description.repeat(90)} Vollständiger Schluss.`
        data.views.push(mockView({ id: 'heading-view', name: 'Saved heading view' }))
        data.projects[0]!.description = text
        await mockWork(page, data)
        await page.goto('/p/PHAROS/tickets')
        const more = page.locator('.description-more'), desc = page.locator('#project-description')
        await expect(desc).toHaveText(text)
        // Wait for the font/layout and ResizeObserver measurement, so loading
        // cannot be mistaken for the baseline's deliberately hidden More.
        await page.evaluate(async () => {
          await document.fonts.ready
          await new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
        })
        // Exercise the old tooltip too: baseline failures must come from lost
        // scrolling, rather than the absence of the new disclosure button.
        if (await more.isVisible()) {
          if (input === 'touch') await more.tap()
          else { await page.keyboard.press('Tab'); await more.focus(); await page.keyboard.press('Enter') }
        } else if (input === 'touch') await desc.tap()
        else { await page.keyboard.press('Tab'); await desc.focus() }
        const full = page.locator('#project-description-full, .tooltip')
        await expect(full).toHaveText(text)
        expect(await full.evaluate(el => el.scrollHeight)).toBeGreaterThan(await page.evaluate(() => innerHeight))
        expect(await full.evaluate(el => el.scrollHeight)).toBeGreaterThan(await full.evaluate(el => el.clientHeight))
        await expectStableControls({
          controls: projectControls(page, width),
          interactions: [{ name: `${input} scroll preserves disclosure`, run: async () => {
            if (input === 'touch') {
              await full.evaluate(el => {
                el.dataset.cancels = '0'
                el.addEventListener('pointercancel', () => { el.dataset.cancels = String(Number(el.dataset.cancels) + 1) })
              })
              const cdp = await context.newCDPSession(page)
              try {
                const box = (await full.boundingBox())!, x = box.x + box.width / 2
                const from = box.y + box.height - 20, to = box.y + 20
                await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x, y: from }] })
                for (let step = 1; step <= 8; step++) {
                  await cdp.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x, y: from + (to - from) * step / 8 }] })
                }
                await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] })
              } finally { await cdp.detach() }
              await expect(full).toHaveText(text)
              await expect.poll(() => full.evaluate(el => el.scrollTop)).toBeGreaterThan(0)
              expect(await full.getAttribute('data-cancels')).not.toBe('0')
            } else {
              await full.focus()
              await page.keyboard.press('ArrowDown')
              await expect(full).toHaveText(text)
              await expect.poll(() => full.evaluate(el => el.scrollTop)).toBeGreaterThan(0)
              await page.keyboard.press('End')
              await expect(full).toHaveText(text)
              await expect.poll(() => full.evaluate(el => Math.abs(el.scrollHeight - el.scrollTop - el.clientHeight))).toBeLessThanOrEqual(1)
              const tailVisible = await full.evaluate(el => {
                const node = el.firstChild!, range = document.createRange()
                range.setStart(node, (node.textContent?.length ?? 0) - 'Vollständiger Schluss.'.length)
                range.setEnd(node, node.textContent?.length ?? 0)
                const tail = range.getBoundingClientRect(), frame = el.getBoundingClientRect()
                return tail.top >= frame.top && tail.bottom <= frame.bottom && tail.right <= frame.right
              })
              expect(tailVisible, 'the last words must be on screen after End').toBe(true)
            }
            await screenshot(page, `project-long-${input}`, width, theme)
          } }],
        })
        await noOverflow(page)
      })
    }
  }
  for (const theme of ['light', 'dark'] as const) {
    test(`phone long description scrolls without moving controls (${theme})`, async ({ page }) => {
      await scene(page, 390, theme)
      const data = fixtures(), text = `${description.repeat(30)} Vollständiger Schluss.`
      data.views.push(mockView({ id: 'heading-view', name: 'Saved heading view' }))
      data.projects[0]!.description = text
      await mockWork(page, data)
      await page.goto('/p/PHAROS/tickets')
      const more = page.locator('.description-more'), full = page.locator('#project-description-full')
      await expectStableControls({
        controls: { more, ...projectControls(page, 390) },
        interactions: [
          { name: 'tap More', run: async () => {
            await more.tap()
            await expect(full).toHaveText(text)
            expect(await full.evaluate(el => {
              const rect = el.getBoundingClientRect()
              return el.scrollHeight > el.clientHeight && rect.bottom <= innerHeight && el.scrollWidth <= el.clientWidth + 1
            })).toBe(true)
          } },
          { name: 'scroll full description', run: async () => {
            await full.evaluate(el => { el.scrollTop = el.scrollHeight })
            expect(await full.evaluate(el => Math.abs(el.scrollHeight - el.scrollTop - el.clientHeight))).toBeLessThanOrEqual(1)
            await screenshot(page, 'project-phone-long', 390, theme)
          } },
          { name: 'tap Less', run: async () => { await more.tap(); await expect(full).toHaveCount(0) } },
          { name: 'tap outside', run: async () => { await more.tap(); await expect(full).toBeVisible(); await page.locator('#project-title').tap(); await expect(full).toHaveCount(0) } },
        ],
      })
    })
    test(`done gate tap reveals and dismisses the title (${theme})`, async ({ page }) => {
      await scene(page, 390, theme)
      await mockWork(page, fixtures())
      await page.goto('/p/PHAROS/tickets')
      // The global host is loaded asynchronously; ask only after its watcher exists.
      await expect(page.locator('dialog.gate')).toBeAttached()
      await page.evaluate(async title => {
        const path = '/src/lib/doneGateAsk.ts'
        const { askDoneGate } = await import(path)
        void askDoneGate({ key: 'AEON-631', title, state: 'done', fields: {} }, { index: 1, total: 2 })
      }, longTitle)
      const gate = page.locator('dialog.gate'), name = gate.locator('.name'), tip = gate.locator('.tooltip')
      await expect(gate).toBeVisible()
      await expect(name).toHaveAttribute('data-heading-tip', longTitle)
      await expectStableControls({
        controls: { title: name, cancel: gate.getByRole('button', { name: 'Not now' }), submit: gate.getByRole('button', { name: 'Mark done' }), field: gate.getByLabel('Pill · English'), frame: gate.locator('.card') },
        scrollAreas: { body: gate.locator('.scroll') },
        interactions: [
          { name: 'tap full title', run: async () => { await name.tap(); await fittingTip(tip, longTitle); await twoLineTouchTarget(name); await screenshot(page, 'done-gate-touch', 390, theme) } },
          { name: 'tap heading to dismiss', run: async () => { await gate.locator('h2').tap(); await expect(tip).toHaveCount(0) } },
          { name: 'tap title and then elsewhere', run: async () => { await name.tap(); await fittingTip(tip, longTitle); await gate.locator('h2').tap(); await expect(tip).toHaveCount(0) } },
        ],
      })
      await noOverflow(page)
    })
    test(`profile title tap reveals without moving the editor (${theme})`, async ({ page }) => {
      await scene(page, 390, theme)
      await mockWork(page, fixtures())
      await mockBusiness(page, businessData({ enabled: ['business_costs', 'business_crm', 'business_quotes', 'business_hours'] }))
      await mockSettings(page, settingsData())
      const world = profileWorld()
      for (const revision of world.profiles[0]!.revisions) revision.name = longName
      await mockProfiles(page, world)
      await page.goto(`/settings/business/profiles/${PROFILE.steel}`)
      const heading = page.locator('#profiles-title'), tip = page.locator('.tooltip'), field = page.getByLabel('Name', { exact: true })
      await expect(heading).toHaveAttribute('data-heading-tip', longName)
      await expectStableControls({
        controls: { name: field, sections: page.getByRole('navigation', { name: 'Profile sections' }), save: page.locator('.head-actions .primary'), back: page.getByRole('link', { name: 'Back to Business settings' }) },
        scrollAreas: { header: page.locator('.profiles-head') },
        interactions: [
          { name: 'tap full profile name', run: async () => { await heading.tap(); await fittingTip(tip, longName); await twoLineTouchTarget(heading); await screenshot(page, 'profile-touch', 390, theme) } },
          { name: 'dismiss full name', run: async () => { await field.tap(); await expect(field).toBeFocused(); await expect(tip).toHaveCount(0) } },
          { name: 'short live name', run: async () => { await field.fill('Kurz'); await expect(heading).toHaveText('Kurz'); await expect(heading).not.toHaveAttribute('data-heading-tip') } },
          { name: 'long live name', run: async () => { await field.fill(longName.slice(0, 100)); await expect(heading).toHaveAttribute('data-heading-tip', longName.slice(0, 100)); await heading.tap(); await fittingTip(tip, longName.slice(0, 100)) } },
        ],
      })
      await noOverflow(page)
    })
    test(`rules preview tap reveals and dismisses the identity (${theme})`, async ({ page }) => {
      await scene(page, 390, theme)
      await mockWork(page, fixtures())
      await mockRules(page)
      await page.route('**/api/projects*', route => route.fulfill({ json: { items: [{ id: RULE_PROJECT, key: 'AEON', title: longTitle, state: 'active', open: 1, in_progress: 0, done: 0, cancelled: 0, total: 1, last_activity: '2026-09-28T10:00:00Z', people: [] }] } }))
      await page.goto('/settings/agent-rules')
      await page.getByRole('button', { name: 'Preview', exact: true }).tap()
      const preview = page.getByRole('dialog', { name: 'What agents receive' })
      await expect(preview.locator('.for .btn')).toBeFocused()
      const identity = preview.locator('.for-value'), tip = preview.locator('.tooltip')
      const text = `${longTitle} · Markus Barta · Builder`
      await expect(identity).toHaveAttribute('data-heading-tip', text)
      await expectStableControls({
        controls: { identity, change: preview.locator('.for .btn'), close: preview.getByRole('button', { name: 'Close what agents receive' }) },
        scrollAreas: { body: preview.locator('.body') },
        interactions: [
          { name: 'tap full identity', run: async () => { await identity.tap(); await fittingTip(tip, text); await twoLineTouchTarget(identity); await screenshot(page, 'rules-touch', 390, theme) } },
          { name: 'tap For label to dismiss', run: async () => { await preview.locator('.for-label').tap(); await expect(tip).toHaveCount(0) } },
          { name: 'tap identity and then elsewhere', run: async () => { await identity.tap(); await fittingTip(tip, text); await preview.locator('.for-label').tap(); await expect(tip).toHaveCount(0) } },
        ],
      })
      await noOverflow(page)
    })
  }
})
