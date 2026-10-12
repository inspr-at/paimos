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

// AEON-1214 fix round 4 (Tick's recheck): between phone width and about 720 px the
// wider split place left the trail a few pixels, and it showed a project key cut
// mid-chip ('PH', or a teal sliver after '/'). The trail drops whole segments, a
// separator with its crumb, and never cuts a chip. Probe: every separator, key,
// icon and text run still shown lies whole inside each box that clips it, up to the
// header's padding; a name that ends in an ellipsis keeps three characters' room;
// no separator stands alone.
const trailFit = (page: Page) => page.locator('.app-header').evaluate(header => {
  // In sight: the element and every box up to the header has room; a visually hidden (1 px) box hides all inside it.
  const shown = (el: Element) => {
    for (let at: Element | null = el; at && at !== header; at = at.parentElement) {
      const box = at.getBoundingClientRect(), style = getComputedStyle(at)
      if (box.width <= 1 || box.height <= 1 || style.display === 'none' || style.visibility === 'hidden') return false
    }
    return true
  }
  const trail = header.querySelector<HTMLElement>('.crumbs')
  if (!trail || !shown(trail)) return { parts: [] as string[], cut: [] as string[] }
  const own = header.getBoundingClientRect(), pad = getComputedStyle(header)
  // The room a node is seen in: the header inside its padding, narrowed by every box up from the node that hides its overflow.
  const room = (node: Node) => {
    let left = own.left + parseFloat(pad.paddingLeft), right = own.right - parseFloat(pad.paddingRight)
    for (let el = node.parentElement; el && el !== header; el = el.parentElement) {
      if (getComputedStyle(el).overflowX === 'visible') continue
      const box = el.getBoundingClientRect()
      left = Math.max(left, box.left); right = Math.min(right, box.right)
    }
    return { left, right }
  }
  const outside = (box: { left: number; right: number }, node: Node) => { const r = room(node); return box.left < r.left - 0.5 || box.right > r.right + 0.5 }
  const label = (el: Element) => el.matches('.sep') ? '/' : [...el.childNodes].filter(node => node.nodeType === Node.TEXT_NODE || shown(node as Element)).map(node => node.textContent!.trim()).filter(Boolean).join(' ')
  const cut: string[] = []
  const parts = [...trail.children].filter(shown)
  const range = document.createRange()
  for (const el of [trail, ...trail.querySelectorAll('*')].filter(el => shown(el) && !el.matches('svg *'))) {
    const style = getComputedStyle(el), box = el.getBoundingClientRect()
    const ellipsis = style.textOverflow === 'ellipsis' && style.overflowX !== 'visible'
    if (el.matches('.sep, .key-badge, svg') && (outside(box, el) || (el instanceof HTMLElement && el.scrollWidth > el.clientWidth))) cut.push(`chip ${label(el) || el.tagName}`)
    if (ellipsis && el instanceof HTMLElement) {
      const inner = { left: box.left + parseFloat(style.paddingLeft), right: box.right - parseFloat(style.paddingRight) }
      if (outside(inner, el)) cut.push(`ellipsis box ${label(el)}`)
      if (el.scrollWidth > el.clientWidth && el.clientWidth < 3 * parseFloat(style.fontSize)) cut.push(`sliver ${label(el)} ${el.clientWidth}px`)
      continue
    }
    for (const node of el.childNodes) {
      if (node.nodeType !== Node.TEXT_NODE || !node.textContent!.trim()) continue
      range.selectNodeContents(node)
      if (outside(range.getBoundingClientRect(), node)) cut.push(`text ${node.textContent!.trim()}`)
    }
  }
  parts.forEach((part, index) => {
    if (part.matches('.sep') && !parts[index + 1]?.matches('.crumb')) cut.push('a separator without its crumb')
  })
  if (!parts.some(part => part.matches('.crumb'))) cut.push('a trail without a crumb')
  return { parts: parts.map(label), cut }
})
// The trail element itself, whatever it shows; the probe judges only what is in sight.
const trailOf = (page: Page) => page.locator('.app-header .crumbs')
const settle = (page: Page) => page.evaluate(() => new Promise(done => requestAnimationFrame(() => requestAnimationFrame(done))))
const TRAILS = [
  { slug: 'tickets', path: '/p/PHAROS/tickets', ready: (page: Page) => expect(page.locator('.app-header .app-density')).toBeVisible() },
  { slug: 'ticket', path: '/p/PHAROS/PHAROS-11?view=full', ready: (page: Page) => expect(trailOf(page).locator('.crumb.current')).toHaveText('PHAROS-11') },
  { slug: 'profiles', path: '/settings/business/profiles', ready: (page: Page) => expect(trailOf(page)).toContainText('Document profiles') },
]
for (const width of [601, 640, 721]) for (const theme of ['light', 'dark'] as const) {
  test(`at ${width} ${theme} the trail drops whole segments and never cuts a chip`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 900 })
    await setup(page, { theme })
    const dir = testInfo.outputPath('AEON-1214')
    await mkdir(dir, { recursive: true })
    for (const trail of TRAILS) {
      await page.goto(trail.path)
      await trail.ready(page)
      await expect(trailOf(page)).toContainText(trail.slug === 'profiles' ? 'Settings' : 'Pharos')
      await expect(chevron(page)).toBeVisible()
      await page.evaluate(() => document.fonts.ready.then(() => undefined))
      await settle(page)
      const fit = await trailFit(page)
      expect(fit.cut, `${trail.path}: ${fit.parts.join(' ')}`).toEqual([])
      expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth), 'no sideways scroll').toBeLessThanOrEqual(0)
      await page.screenshot({ path: join(dir, `trail-${trail.slug}-${width}-${theme}.png`), clip: { x: 0, y: 0, width, height: 120 } })
    }
  })
}

// Every width from just above phone width to a wide desktop, places labelled from
// 1181 px: the trail never shows a cut chip, and it returns whole when the window
// widens. What steps aside stays in the trail for screen readers, out of the tab order.
test('from 601 to 1440 px the trail never cuts a chip and returns whole when the window widens', async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.emulateMedia({ colorScheme: 'light' })
  await setup(page)
  const dir = testInfo.outputPath('AEON-1214')
  await mkdir(dir, { recursive: true })
  for (const trail of TRAILS.slice(0, 2)) {
    await page.setViewportSize({ width: 1440, height: 900 })
    await page.goto(trail.path)
    await trail.ready(page)
    await expect(trailOf(page)).toContainText('Pharos')
    await page.evaluate(() => document.fonts.ready.then(() => undefined))
    const seen: string[] = []
    for (let width = 1440; width >= 601; width -= 7) {
      await page.setViewportSize({ width, height: 900 })
      await settle(page)
      const fit = await trailFit(page)
      expect(fit.cut, `${trail.path} at ${width}: ${fit.parts.join(' ')}`).toEqual([])
      // Each step the trail takes, for the reviewer's eye.
      if (fit.parts.join(' ') !== seen.at(-1)) await page.screenshot({ path: join(dir, `trail-step-${trail.slug}-${width}.png`), clip: { x: 0, y: 0, width, height: 64 } })
      seen.push(fit.parts.join(' '))
    }
    // The sweep saw the trail whole and saw whole segments go; they come back with the room.
    expect(new Set(seen).size, seen.join(' | ')).toBeGreaterThan(1)
    await page.setViewportSize({ width: 1024, height: 900 })
    await settle(page)
    const back = await trailFit(page)
    expect(back.cut).toEqual([])
    expect(back.parts[0]).toBe('/')
    expect(back.parts, `${trail.path} at 1024 after the sweep`).toContain(trail.slug === 'tickets' ? 'PHAROS Pharos' : 'PHAROS-11')
    // Screen readers keep the whole trail at every width; what is out of sight is out of the tab order.
    await page.setViewportSize({ width: 640, height: 900 })
    await settle(page)
    const crumbs = page.getByRole('navigation', { name: 'Breadcrumb' })
    await expect(crumbs.getByRole('link', { name: 'PHAROS Pharos', exact: true })).toHaveAttribute('href', '/p/PHAROS')
    expect(await trailOf(page).evaluate(trail => [...trail.querySelectorAll<HTMLElement>('a[href], button')].filter(el => el.getBoundingClientRect().width <= 1 && el.tabIndex >= 0 && getComputedStyle(el).display !== 'none').length)).toBe(0)
  }
})

// AEON-1240 (Tick's pre-cut pass on 07717bdb4; AEON-1213 fix7 at 320 px): on phones a key, the project's chip or a
// ticket's, shows whole or not at all. It was clipped to "PHARO" at 390 and to "P" at 400, half under Search. Probe:
// every key in sight lies whole in its slot, ends in no ellipsis and sits under no other header control; every
// control stays inside the header's padding; neither the header nor the page scrolls sideways; and a refit at the
// same width moves nothing (no shift).
const PHONE_PAGES = [
  { slug: 'tickets', path: '/p/PHAROS/tickets' },
  { slug: 'settings', path: '/p/PHAROS/settings' },
  { slug: 'delivery', path: '/p/PHAROS/delivery' },
  { slug: 'ticket', path: '/p/PHAROS/PHAROS-11?view=full' },
  { slug: 'agents', path: '/agents' },
  { slug: 'desk', path: '/decision-desk' },
]
const phoneFit = (page: Page) => page.locator('.app-header').evaluate(header => {
  const shown = (el: Element) => {
    for (let at: Element | null = el; at && at !== header.parentElement; at = at.parentElement) {
      const box = at.getBoundingClientRect(), style = getComputedStyle(at)
      if (box.width <= 1 || box.height <= 1 || style.display === 'none' || style.visibility === 'hidden') return false
    }
    return true
  }
  const own = header.getBoundingClientRect(), pad = getComputedStyle(header)
  const start = own.left + parseFloat(pad.paddingLeft), end = own.right - parseFloat(pad.paddingRight)
  const name = (el: Element) => el.getAttribute('aria-label') || (el as HTMLElement).innerText.trim() || el.className
  const controls = [...header.querySelectorAll('a[href], button')].filter(el => shown(el) && !el.closest('.crumbs'))
  const slot = header.querySelector('.crumbs')?.getBoundingClientRect()
  const keys = [...header.querySelectorAll<HTMLElement>('.crumbs .key-badge, .crumbs .mono-crumb')].filter(shown)
  const cut: string[] = []
  for (const key of keys) {
    const r = key.getBoundingClientRect(), text = key.textContent!.trim()
    if (slot && (r.left < slot.left - 0.5 || r.right > slot.right + 0.5)) cut.push(`${text} outside its slot`)
    if (key.scrollWidth > key.clientWidth) cut.push(`${text} ends in an ellipsis`)
    for (let at = key.parentElement; at && at !== header; at = at.parentElement) {
      if (getComputedStyle(at).overflowX === 'visible') continue
      const b = at.getBoundingClientRect()
      if (r.left < b.left - 0.5 || r.right > b.right + 0.5) cut.push(`${text} clipped by ${at.className}`)
    }
    for (const control of controls) {
      const c = control.getBoundingClientRect()
      if (Math.min(r.right, c.right) - Math.max(r.left, c.left) > 0.5 && Math.min(r.bottom, c.bottom) - Math.max(r.top, c.top) > 0.5) cut.push(`${text} under ${name(control)}`)
    }
  }
  const outside = controls.filter(el => { const r = el.getBoundingClientRect(); return r.left < start - 0.5 || r.right > end + 0.5 }).map(name)
  return { keys: keys.map(key => key.textContent!.trim()), cut, outside, header: header.scrollWidth - header.clientWidth, page: document.documentElement.scrollWidth - innerWidth, away: header.dataset.away ?? '' }
})
for (const width of [320, 360, 390, 400, 420, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
  test(`at ${width} ${theme} the header shows a key whole or not at all and fits the window (AEON-1240)`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 844 })
    await setup(page, { theme })
    const dir = testInfo.outputPath('AEON-1240')
    await mkdir(dir, { recursive: true })
    for (const target of PHONE_PAGES) {
      await page.goto(target.path)
      await expect(places(page)).toBeVisible()
      if (target.slug !== 'agents' && target.slug !== 'desk') await expect(page.getByRole('navigation', { name: 'Breadcrumb' })).toHaveCount(1)
      await page.evaluate(() => document.fonts.ready.then(() => undefined))
      await settle(page)
      const fit = await phoneFit(page)
      const where = `${target.path} at ${width} (keys ${fit.keys.join(', ') || 'none'}; aside ${fit.away || 'nothing'})`
      expect(fit.cut, where).toEqual([])
      expect(fit.outside, `${where}: controls past the header`).toEqual([])
      expect(fit.header, `${where}: header scrolls sideways`).toBeLessThanOrEqual(0)
      expect(fit.page, `${where}: page scrolls sideways`).toBeLessThanOrEqual(0)
      // The key leaves only when it can't show whole: from 390 the project's chip fits once the Desk's chevron is aside.
      if (width >= 390 && (target.slug === 'settings' || target.slug === 'delivery')) expect(fit.keys, where).toContain('PHAROS')
      const key = page.locator('.app-header .crumbs').locator('.key-badge, .mono-crumb').filter({ visible: true })
      const controls = { places: places(page), search: page.getByRole('button', { name: 'Search everything' }), account: page.locator('.app-header .account'), ...(fit.keys.length ? { key: key.first() } : {}) }
      await expectStableControls({ controls, interactions: [
        { name: 'a font swap refits the header', run: async () => { await page.evaluate(() => document.fonts.dispatchEvent(new Event('loadingdone'))); await settle(page) } },
        { name: 'hover the places', run: async () => { await places(page).hover(); await settle(page) } },
      ] })
      expect((await phoneFit(page)).keys, `${where}: the same keys after a refit`).toEqual(fit.keys)
      await page.mouse.move(0, 400)
      await page.screenshot({ path: join(dir, `header-${target.slug}-${width}-${theme}.png`), clip: { x: 0, y: 0, width, height: 72 } })
    }
  })
}

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
