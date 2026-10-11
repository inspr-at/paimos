// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1214: the header places end with the Decision Desk as a split place. Its
// name goes to the Desk; the chevron previews the Desk on every page. Risk: the
// preview moves header controls, hides behind the wrong count, or squeezes a
// phone header until the ticket key clips.
import { expect, test, type Page } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
import { join } from 'node:path'
import { expectStableControls } from './helpers/stable'
import { mockDecisionDesk } from './decision-desk-fixtures'
import { businessData, mockBusiness } from './business-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import type { DeskProjectionItem } from '../src/lib/decisionDesk'

async function setup(page: Page, options: Parameters<typeof mockDecisionDesk>[1] = {}) {
  const control = await mockDecisionDesk(page, options)
  await mockBusiness(page, businessData())
  await mockSettings(page, settingsData())
  return control
}
const places = (page: Page) => page.getByRole('navigation', { name: 'Places' })
const deskLink = (page: Page) => places(page).getByRole('link', { name: /^Decision Desk, / })
const chevron = (page: Page) => places(page).getByRole('button', { name: 'Preview the Decision Desk', exact: true })
const preview = (page: Page) => page.getByRole('dialog', { name: 'Decision Desk preview', exact: true })
const order = (page: Page) => page.evaluate(() => [...document.querySelectorAll<HTMLElement>('.places > [data-place]')].map(place => place.dataset.place))
// The design gate's probes (AEON-1196 check.mjs), on the split and its preview:
// no coloured edge accents, SVG icons only, English only, and on phones 44 px targets.
const designGate = (page: Page, phone: boolean) => page.evaluate(phone => {
  const scope = [...document.querySelectorAll('.places .split, .hm-panel.desk-panel')]
  const all = [...new Set(scope.flatMap(root => [root, ...root.querySelectorAll('*')]))]
  const shown = (el: Element) => { const box = el.getBoundingClientRect(), style = getComputedStyle(el); return box.width > 0 && box.height > 0 && style.visibility !== 'hidden' && style.display !== 'none' }
  const opaque = (color: string) => !!color && color !== 'transparent' && !/rgba\(.*,\s*0\)$/.test(color)
  const edges: string[] = []
  for (const el of all) {
    const style = getComputedStyle(el), width = (side: string) => parseFloat(style.getPropertyValue(`border-${side}-width`)) || 0
    for (const [a, b] of [['left', 'right'], ['top', 'bottom'], ['right', 'left'], ['bottom', 'top']]) {
      if (width(a) >= 2 && width(a) - width(b) >= 1 && opaque(style.getPropertyValue(`border-${a}-color`))) edges.push(`${el.className} border-${a}`)
    }
    for (const part of style.boxShadow === 'none' ? [] : style.boxShadow.split(/,(?![^(]*\))/)) {
      if (!part.includes('inset')) continue
      const [x = 0, y = 0, blur = 0] = (part.replace(/rgba?\([^)]*\)|inset/g, '').match(/-?[\d.]+px/g) ?? []).map(parseFloat)
      if ((Math.abs(x) >= 2 || Math.abs(y) >= 2) && blur === 0) edges.push(`${el.className} inset ${part.trim()}`)
    }
    for (const pseudo of ['::before', '::after']) {
      const p = getComputedStyle(el, pseudo)
      if (p.content === 'none' || p.position !== 'absolute') continue
      const w = parseFloat(p.width) || 0, h = parseFloat(p.height) || 0
      if (w >= 2 && w <= 6 && h >= 16 && opaque(p.backgroundColor)) edges.push(`${el.className} ${pseudo} bar`)
    }
  }
  const text = scope.map(root => (root as HTMLElement).innerText).join(' ')
  const glyphs = text.match(/[←→↑↓↵⏎✓✔✕✖×▾▸▴▲▼◂▶◀★☆●○◉⚠✗✘➜➔⟶]/g) ?? []
  const german = text.match(/[äöüÄÖÜß]|\b(und|der|die|das|nicht|mit|für|oder|ist|Sitzung|Agenten|Projekt|Fehler|Einstellungen|bitte)\b/g) ?? []
  const small = phone ? all.filter(el => el.matches('button, a[href]') && shown(el) && !el.closest('[aria-hidden="true"]')).map(el => el.getBoundingClientRect()).filter(box => box.width < 43.5 || box.height < 43.5).map(box => `${Math.round(box.width)}×${Math.round(box.height)}`) : []
  return { edges, glyphs, german, small }
}, phone)

for (const width of [1440, 1024, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`the Desk is the last place; its chevron previews the Desk without moving the header at ${width} ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 900 })
    await setup(page, { theme })
    await page.goto('/')
    await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
    expect(await order(page)).toEqual(['projects', 'agents', 'business', 'desk'])
    await expect(deskLink(page)).toHaveAccessibleName('Decision Desk, 5 open')
    await expect(deskLink(page)).toHaveAttribute('href', '/decision-desk')
    await expect(chevron(page)).toHaveAttribute('aria-haspopup', 'dialog')
    await expect(chevron(page)).toHaveAttribute('aria-expanded', 'false')
    if (width < 600) for (const target of [deskLink(page), chevron(page)]) {
      const box = (await target.boundingBox())!
      expect(Math.round(box.width), 'phone targets are 44 px wide').toBeGreaterThanOrEqual(44)
      expect(Math.round(box.height), 'phone targets are 44 px high').toBeGreaterThanOrEqual(44)
    }
    const dir = testInfo.outputPath('AEON-1214')
    await mkdir(dir, { recursive: true })
    await page.screenshot({ path: join(dir, `header-${width}-${theme}.png`), clip: { x: 0, y: 0, width, height: 120 } })
    const firstRow = preview(page).getByRole('link', { name: /^Approval · nodes\.read, / })
    await expectStableControls({
      controls: {
        places: places(page), desk: deskLink(page), chevron: chevron(page),
        search: page.getByRole('button', { name: 'Search everything' }), gear: page.getByRole('button', { name: /^App and workspace/ }),
      },
      interactions: [
        { name: 'open the preview', run: async () => {
          await chevron(page).click()
          await expect(preview(page)).toBeVisible()
          await expect(preview(page)).toContainText('5 things for you')
          await expect(firstRow).toBeFocused()
          // The tooltip never covers its own open preview.
          await expect(chevron(page)).not.toHaveAttribute('data-tip')
          await expect(chevron(page)).toHaveAttribute('aria-expanded', 'true')
        } },
        { name: 'close it with Escape', run: async () => {
          await page.keyboard.press('Escape')
          await expect(preview(page)).toBeHidden()
          await expect(chevron(page)).toBeFocused()
        } },
        { name: 'open it from the keyboard', run: async () => {
          await page.keyboard.press('ArrowDown')
          await expect(firstRow).toBeFocused()
        } },
      ],
    })
    expect(await designGate(page, width < 600)).toEqual({ edges: [], glyphs: [], german: [], small: [] })
    // Held decisions come first, each row names its project, kind and age.
    await expect(preview(page).getByRole('region', { name: 'Waiting on you' }).getByRole('link')).toHaveCount(4)
    await expect(preview(page).getByRole('region', { name: 'When you’re ready' }).getByRole('link')).toHaveAccessibleName(/^Doctrine change, Paimos Aeon · Rule change · /)
    if (width < 600) {
      await expect(page.locator('.hm-panel.desk-panel')).toHaveClass(/sheet/)
      for (const row of await preview(page).getByRole('link').all()) expect((await row.boundingBox())!.height).toBeGreaterThanOrEqual(44)
      expect(await page.locator('.hm-panel.desk-panel').evaluate(el => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1)
    } else {
      // On wide screens it opens from the chevron, under the header, inside the window.
      const [panel, more, header] = await Promise.all([page.locator('.hm-panel.desk-panel').boundingBox(), chevron(page).boundingBox(), page.locator('.app-header').boundingBox()])
      expect(Math.abs(panel!.x - more!.x)).toBeLessThanOrEqual(0.5)
      expect(Math.abs(panel!.y - (header!.y + header!.height - 1))).toBeLessThanOrEqual(0.5)
      expect(panel!.x + panel!.width).toBeLessThanOrEqual(width - 12)
    }
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width)
    await page.screenshot({ path: join(dir, `preview-${width}-${theme}.png`) })
    // A row opens its item on the Desk, and the preview steps away.
    await firstRow.click()
    await expect(page).toHaveURL('/decision-desk?item=a:approval-1')
    await expect(preview(page)).toBeHidden()
    await expect(page.locator('.places .place.split')).toHaveClass(/active/)
    await page.screenshot({ path: join(dir, `desk-active-${width}-${theme}.png`), clip: { x: 0, y: 0, width, height: 120 } })
  })
}

test('the preview opens on every page and links to the whole Desk', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await setup(page)
  for (const path of ['/agents', '/business', '/settings/personal', '/p/PHAROS/PHAROS-11?view=full', '/decision-desk']) {
    await page.goto(path)
    await expect(chevron(page)).toBeVisible()
    expect(await order(page), path).toEqual(['projects', 'agents', 'business', 'desk'])
    await chevron(page).click()
    await expect(preview(page), path).toContainText('Waiting on you')
    await page.keyboard.press('Escape')
    await expect(preview(page)).toBeHidden()
  }
  await chevron(page).click()
  await preview(page).getByRole('link', { name: 'Open the Decision Desk' }).click()
  await expect(page).toHaveURL('/decision-desk')
  await expect(preview(page)).toBeHidden()
})

test('the preview says when it shows part of the Desk, nothing, or could not read it', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const extra: DeskProjectionItem[] = Array.from({ length: 15 }, (_, n) => ({
    id: `extra-${n}`, kind: 'question', project_id: 'p-aeon', revision: 1, title: `Extra question ${n + 1}`, created_at: '2026-10-02T09:00:00Z', held: false,
    href: `/decision-desk?item=q:extra-${n}`, source: `/api/questions/extra-${n}`,
  }))
  const control = await setup(page, { projectedSources: () => extra })
  await page.goto('/')
  await chevron(page).click()
  // The Desk's own total, never this list's length.
  await expect(preview(page)).toContainText('20 things for you')
  await expect(preview(page).getByRole('link', { name: /^(?!Open the Decision Desk)/ })).toHaveCount(12)
  await expect(preview(page)).toContainText('12 of 20 shown here.')
  await page.keyboard.press('Escape')

  await page.route('**/api/decision-desk/projection*', route => route.fulfill({ json: { items: [], counts: { open: 0, held: 0, chores: 0 }, has_more: false, as_of: new Date().toISOString() } }))
  await page.reload()
  await chevron(page).click()
  await expect(preview(page)).toContainText('Nothing for you')
  await expect(preview(page)).toContainText('Nothing waits on you.')
  await expect(preview(page).getByRole('link', { name: 'Open the Decision Desk' })).toBeFocused()
  await page.keyboard.press('Escape')

  await page.unroute('**/api/decision-desk/projection*')
  control.failProjection = true
  await page.reload()
  await chevron(page).click()
  await expect(preview(page)).toContainText('Count unavailable')
  await expect(preview(page).getByRole('alert')).toContainText('Decision Desk could not be read (403).')
  await expect(deskLink(page)).toHaveAccessibleName('Decision Desk, count unavailable')
  control.failProjection = false
  await preview(page).getByRole('button', { name: 'Retry', exact: true }).click()
  await expect(preview(page)).toContainText('20 things for you')
  await expect(deskLink(page)).toHaveAccessibleName('Decision Desk, 20 open')
})

// Measured, not guessed: on a phone the chevron steps aside before the moon when
// the room runs out, so the ticket key stays whole (AEON-312); the Desk stays a tap away.
for (const width of [375, 390]) test(`on a ${width} px phone the chevron gives way to a ticket key and returns on the home page`, async ({ page }) => {
  await page.setViewportSize({ width, height: 844 })
  await page.emulateMedia({ colorScheme: 'light' })
  await setup(page)
  await page.goto('/p/PHAROS/PHAROS-11?view=full')
  const key = page.getByRole('navigation', { name: 'Breadcrumb' }).locator('.crumb.current')
  await expect(key).toHaveText('PHAROS-11')
  await expect.poll(() => key.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
  await expect(deskLink(page)).toBeVisible()
  await expect(chevron(page)).toBeHidden()
  // It truly would not fit: the free room is less than its 44 px.
  expect(await page.locator('.app-header .spacer').evaluate(el => el.getBoundingClientRect().width)).toBeLessThan(44)
  expect(await page.locator('.app-header').evaluate(el => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(0)
  await places(page).getByRole('link', { name: 'Projects' }).click()
  await expect(page).toHaveURL('/')
  await expect(page.getByRole('button', { name: 'Switch to dark theme' })).toBeVisible()
  if (width === 390) {
    await expect(chevron(page)).toBeVisible()
    await chevron(page).click()
    await expect(preview(page)).toContainText('5 things for you')
  } else await expect(chevron(page)).toBeHidden()
  // Whatever stays, nothing runs past the header's edge.
  const reach = await page.locator('.app-header').evaluate(header => {
    const end = header.getBoundingClientRect().right - parseFloat(getComputedStyle(header).paddingRight)
    return Math.max(...[...header.children].map(child => child.getBoundingClientRect().right)) - end
  })
  expect(reach).toBeLessThanOrEqual(0.5)
})

// AEON-1214 fix round 3: on tablets the split Desk is wider than a plain place, and
// a project's tickets add the density switch (and here Business). Once the
// breadcrumb has nothing left to give, the search pill yields the rest, its keys
// first, and just above phone width the row's gaps tighten, so the header never
// runs past the window. With room to spare the pill keeps its full width and keys.
for (const width of [601, 640, 721, 768, 1024]) test(`on a project's tickets at ${width} the header fits the window and the search pill gives way last`, async ({ page }) => {
  await page.setViewportSize({ width, height: 900 })
  await page.emulateMedia({ colorScheme: 'light' })
  await setup(page)
  await page.goto('/p/PHAROS/tickets')
  await expect(page.locator('.app-header .app-density')).toBeVisible()
  await expect(chevron(page)).toBeVisible()
  await expect(page.getByRole('button', { name: 'Search everything' })).toBeVisible()
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(0)
  const fit = await page.locator('.app-header').evaluate(header => {
    const end = header.getBoundingClientRect().right - parseFloat(getComputedStyle(header).paddingRight)
    const pill = header.querySelector<HTMLElement>('.search-pill')!, box = pill.getBoundingClientRect()
    const shown = [...pill.children].filter(el => getComputedStyle(el).display !== 'none')
    return {
      reach: Math.max(...[...header.children].map(child => child.getBoundingClientRect().right)) - end,
      width: box.width,
      shown: shown.map(el => el.classList.contains('pill-text') ? 'text' : el.classList.contains('pill-keys') ? 'keys' : 'icon'),
      // Whatever the pill still shows lies whole inside its border, and its label is never cut.
      cut: shown.filter(el => { const r = el.getBoundingClientRect(); return r.left < box.left + 0.5 || r.right > box.right - 0.5 || (el instanceof HTMLElement && el.scrollWidth > el.clientWidth) }).length,
      offCentre: (() => { const icon = pill.querySelector('svg')!.getBoundingClientRect(); return Math.abs(icon.left + icon.width / 2 - (box.left + box.width / 2)) })(),
    }
  })
  expect(fit.reach, 'nothing runs past the header').toBeLessThanOrEqual(0.5)
  expect(fit.cut, `the pill shows ${fit.shown.join(', ')} whole`).toBe(0)
  expect(fit.shown[0]).toBe('icon')
  if (width === 1024) {
    expect(fit.width).toBe(200)
    expect(fit.shown).toEqual(['icon', 'text', 'keys'])
  } else {
    expect(fit.width, 'the pill gave way').toBeLessThan(180)
    if (fit.shown.length === 1) expect(fit.offCentre, 'a lone magnifier sits centred').toBeLessThanOrEqual(0.5)
  }
})

// AEON-1214 fix round 2: inside the Desk place the preview must not inherit the
// place's one-line label, so a long request wraps beside its Open, never under it.
const LONG_TITLE = 'Decide whether the release train may ship the tenant isolation migration together with the reporter contract bump before Monday: https://github.com/inspr-at/paimos/pull/181/files#diff-0123456789abcdef0123456789abcdef0123456789abcdef'
for (const width of [1024, 1440]) test(`a long request wraps inside the preview at ${width}`, async ({ page }) => {
  await page.setViewportSize({ width, height: 900 })
  await setup(page, { projectedSources: () => [{
    id: 'long-1', kind: 'question', project_id: 'p-aeon', revision: 1, title: LONG_TITLE, created_at: '2026-10-02T09:00:00Z', held: false,
    href: '/decision-desk?item=q:long-1', source: '/api/questions/long-1',
  }] })
  await page.goto('/')
  await chevron(page).click()
  const row = page.getByTestId('desk-preview-item-q:long-1')
  await expect(row).toBeVisible()
  const [title, open, box] = await Promise.all([row.locator('.dp-t b').boundingBox(), row.locator('.dp-open').boundingBox(), row.boundingBox()])
  const line = await row.locator('.dp-t b').evaluate(el => parseFloat(getComputedStyle(el).lineHeight))
  expect(title!.height, 'the title runs over several lines').toBeGreaterThan(2 * line)
  expect(title!.x + title!.width, 'the title ends before Open').toBeLessThanOrEqual(open!.x)
  expect(open!.x + open!.width, 'Open stays inside its row').toBeLessThanOrEqual(box!.x + box!.width + 0.5)
  for (const target of [row, page.locator('.hm-panel.desk-panel'), preview(page).locator('.dp-body')]) {
    expect(await target.evaluate(el => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(0)
  }
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width)
})

// AEON-541: the preview's own controls stay put while the Desk is read, fails,
// is read again and a poll adds a row. Wide screens keep the link above the rows;
// phones pin it at the foot of a full-height sheet.
for (const width of [1440, 390]) test(`the preview's controls stay put through reading, a failure, Retry and a refresh at ${width}`, async ({ page }) => {
  await page.setViewportSize({ width, height: width < 600 ? 844 : 900 })
  await page.clock.install({ time: new Date('2026-10-11T10:00:00Z') })
  const added: DeskProjectionItem[] = []
  const control = await setup(page, { projectedSources: () => added })
  // The first read waits until the preview is open.
  let release!: () => void
  const firstRead = new Promise<void>(resolve => { release = resolve })
  await page.route('**/api/decision-desk/projection*', async route => { await firstRead; await route.fallback() })
  await page.goto('/')
  await chevron(page).click()
  await expect(preview(page)).toContainText('Reading the Desk…')
  const heading = preview(page).getByRole('heading', { name: 'Decision Desk' })
  const go = preview(page).getByRole('link', { name: 'Open the Decision Desk' })
  const body = preview(page).locator('.dp-body')
  await expectStableControls({
    controls: { heading, go },
    interactions: [
      { name: 'the first read arrives', run: async () => {
        release()
        await expect(preview(page)).toContainText('5 things for you')
      } },
      { name: 'a poll fails', run: async () => {
        control.failProjection = true
        await page.clock.runFor(30_000)
        await expect(preview(page).getByRole('alert')).toContainText('Decision Desk could not be read (403).')
      } },
      { name: 'Retry reads it again', run: async () => {
        control.failProjection = false
        await preview(page).getByRole('button', { name: 'Retry', exact: true }).click()
        await expect(preview(page)).toContainText('5 things for you')
      } },
    ],
    scrollAreas: { body },
  })
  // A poll that adds a row moves neither the rows already there nor the link.
  const firstRow = preview(page).getByRole('link', { name: /^Approval · nodes\.read, / })
  await expectStableControls({
    controls: { heading, go, firstRow },
    interactions: [{ name: 'a poll adds a row', run: async () => {
      added.push({ id: 'late-1', kind: 'question', project_id: 'p-aeon', revision: 1, title: 'A late question', created_at: '2026-10-11T09:59:00Z', held: false, href: '/decision-desk?item=q:late-1', source: '/api/questions/late-1' })
      await page.clock.runFor(30_000)
      await expect(preview(page)).toContainText('6 things for you')
      await expect(page.getByTestId('desk-preview-item-q:late-1')).toBeVisible()
    } }],
    scrollAreas: { body },
  })
  if (width < 600) {
    // The full-height sheet: its link sits at the bottom, clear of the safe area.
    const [sheet, link] = await Promise.all([page.locator('.hm-panel.desk-panel').boundingBox(), go.boundingBox()])
    expect(Math.round(sheet!.y + sheet!.height)).toBe(844)
    expect(sheet!.y).toBeLessThanOrEqual(8.5)
    expect(link!.height).toBeGreaterThanOrEqual(44)
    expect(sheet!.y + sheet!.height - (link!.y + link!.height)).toBeLessThanOrEqual(24)
  }
})

// Where the chevron has stepped aside on a phone, the avatar sheet opens the
// preview instead, and Escape returns to the avatar; where it fits, only the chevron does.
for (const [width, where, path, away] of [[375, 'home', '/', true], [375, 'a ticket', '/p/PHAROS/PHAROS-11?view=full', true], [390, 'a ticket', '/p/PHAROS/PHAROS-11?view=full', true], [390, 'home', '/', false]] as const) {
  test(`on ${where} at ${width} px the preview is ${away ? 'one entry away in the avatar sheet' : 'on its chevron, not in the avatar sheet'}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 844 })
    await page.emulateMedia({ colorScheme: 'light' })
    await setup(page)
    await page.goto(path)
    if (path !== '/') await expect(page.getByRole('navigation', { name: 'Breadcrumb' }).locator('.crumb.current')).toHaveText('PHAROS-11')
    else await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
    const avatar = page.getByRole('button', { name: /^Account for / })
    const entry = page.getByRole('menu', { name: 'Account' }).getByRole('menuitem', { name: 'Preview the Decision Desk' })
    if (!away) {
      await expect(chevron(page)).toBeVisible()
      await avatar.click()
      await expect(page.getByRole('menu', { name: 'Account' })).toBeVisible()
      await expect(entry).toHaveCount(0)
      return
    }
    await expect(chevron(page)).toBeHidden()
    await avatar.click()
    await expect(entry).toBeVisible()
    const box = (await entry.boundingBox())!
    expect(box.height, 'a 44 px target').toBeGreaterThanOrEqual(44)
    await entry.click()
    await expect(page.getByRole('menu', { name: 'Account' })).toBeHidden()
    await expect(preview(page)).toContainText('5 things for you')
    await expect(preview(page).getByRole('link', { name: /^Approval · nodes\.read, / })).toBeFocused()
    await expect(page.locator('.hm-panel.desk-panel')).toHaveClass(/sheet/)
    expect(await designGate(page, true)).toEqual({ edges: [], glyphs: [], german: [], small: [] })
    await page.keyboard.press('Escape')
    await expect(preview(page)).toBeHidden()
    await expect(avatar).toBeFocused()
    // From there, a row opens its item on the Desk.
    await avatar.click()
    await entry.click()
    await preview(page).getByRole('link', { name: /^Approval · nodes\.read, / }).click()
    await expect(page).toHaveURL('/decision-desk?item=a:approval-1')
    await expect(preview(page)).toBeHidden()
  })
}
