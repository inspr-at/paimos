// SPDX-License-Identifier: AGPL-3.0-only
// AEON-430: every release has a sci-fi codename from its sequence, and the
// marketing name is front and centre: the row's title, the detail's heading,
// the footer's pill. AEON-488 keeps the list name beside a separate Pretty
// version; the detail and footer retain their name-to-version reveal.
import { test, expect, type Page } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
import { join } from 'node:path'
import AxeBuilder from '@axe-core/playwright'
import { fixtures, me, mockWork } from './work-fixtures'
import { CODENAMES, mockReleases, presentedHistory } from './releases-fixtures'

const sheet = (page: Page) => page.getByRole('dialog', { name: 'PAIMOS AEON releases' })
const options = (page: Page) => page.getByRole('grid', { name: 'Releases, newest first' }).getByRole('row')

async function open(page: Page, version?: string, now?: number) {
  const data = fixtures()
  data.preferences['developer-ui'] = { show_reserved_versions: true }
  await mockWork(page, data)
  const history = presentedHistory(now)
  Object.assign(history.releases.find(r => r.state === 'reserved')!, { release_sequence: 54, codename: 'Bold Booster' })
  await mockReleases(page, history)
  await page.goto(`/releases/${version ?? history.releases[0].version}`)
  await expect(sheet(page)).toBeVisible()
  return history
}

test('release list screenshot', async ({ page }) => {
  test.skip(!process.env.RELEASE_LIST_SHOTS, 'Screenshots are opt-in review artifacts.')
  await page.setViewportSize({ width: 1600, height: 1000 })
  const now = new Date('2026-10-01T14:01:10Z').getTime()
  await page.clock.install({ time: new Date(now) })
  await open(page, undefined, now)
  await mkdir(process.env.RELEASE_LIST_SHOTS!, { recursive: true })
  await page.mouse.move(1, 1)
  await sheet(page).locator('.listbox').focus()
  await expect(sheet(page).locator('.row').first()).toBeInViewport()
  await page.screenshot({ path: join(process.env.RELEASE_LIST_SHOTS!, `${process.env.RELEASE_LIST_SHOT_LABEL ?? 'after'}-desktop.png`) })
})

test('the marketing name leads every row; codename and Pretty version are both visible', async ({ page }) => {
  const history = await open(page)
  // Newest: the name leads, a presented theme is the subtitle.
  const newest = options(page).nth(0)
  await expect(newest.locator('.rn-name')).toHaveText(CODENAMES[5])
  await expect(newest.locator('.headline')).toHaveText('Releases with a name')
  // A reservation keeps its sequence's name beside its state.
  const reserved = options(page).nth(2)
  await expect(reserved.locator('.rn-name')).toHaveText('Bold Booster')
  await expect(reserved.locator('.headline')).toHaveText('Reserved, never published')
  // No brief: the name and version still both appear, including on row hover.
  const untitled = options(page).nth(3)
  await expect(untitled.locator('.rn-name')).toHaveText(CODENAMES[3])
  await expect(untitled.locator('.headline')).toHaveCount(0)
  const version = untitled.getByRole('button', { name: `Copy version ${history.releases[3]!.version}`, exact: true })
  await expect(version).toHaveAttribute('data-version-view', 'pretty')
  await expect(version.locator('.version-pretty')).toContainText(/^\d\d·\d\d·\d\d \d\d:\d\d/)
  await expect(version.locator('[data-version-character="pretty"]').last()).toHaveCSS('opacity', '1')
  await expect(untitled.locator('.rn-stamp')).toHaveCount(0)
  await untitled.hover()
  await expect(untitled.locator('.rn-name')).toHaveCSS('opacity', '1')
  await expect(version).toHaveAttribute('data-version-view', 'pretty')
  for (const row of await options(page).all()) {
    await expect(row.locator('.rn-name')).toBeVisible()
    await expect(row.locator('.row-version .version-pretty')).toBeVisible()
  }
})

test('the list version uses the dock crossfade without hiding the codename or shifting the heading', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'no-preference' })
  const history = await open(page)
  const row = options(page).nth(1)
  const version = row.getByRole('button', { name: `Copy version ${history.releases[1]!.version}`, exact: true })
  const canonical = version.locator('[data-version-character="canonical"]')
  const pretty = version.locator('[data-version-character="pretty"]')
  await sheet(page).locator('.shell').evaluate(async el => { await Promise.all(el.getAnimations().map(animation => animation.finished)) })
  await row.evaluate(async el => { await Promise.all(el.getAnimations().map(animation => animation.finished)) })
  // Playwright may scroll the 44 px target into view before hovering it.
  await version.scrollIntoViewIfNeeded()
  const bounds = await version.boundingBox()
  await version.hover()
  await expect(version).toHaveAttribute('data-version-view', 'revealed')
  await expect(canonical.last()).toHaveCSS('opacity', '1')
  await expect(pretty.last()).toHaveCSS('opacity', '0')
  await expect(version.locator('.version-canonical')).toHaveText(history.releases[1]!.version)
  await expect(row.locator('.rn-name')).toHaveCSS('opacity', '1')
  expect(await version.boundingBox()).toEqual(bounds)
  const transitions = await canonical.evaluateAll(nodes => nodes.map(node => {
    const style = getComputedStyle(node)
    return [style.transitionDuration, style.transitionDelay, style.transitionTimingFunction]
  }))
  expect(transitions[0]).toEqual(['0.42s', '0s', 'ease-in-out'])
  expect(transitions.at(-1)).toEqual(['0.42s', '0.58s', 'ease-in-out'])
  await page.mouse.move(1, 1)
  await sheet(page).getByRole('grid').focus()
  await expect(version).toHaveAttribute('data-version-view', 'pretty')
  await expect(canonical.last()).toHaveCSS('opacity', '0')
  await expect(pretty.last()).toHaveCSS('opacity', '1')
})

test('focus reveals the list version; click, Enter and Space copy without selecting that row', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const history = await open(page)
  const row = options(page).nth(3)
  const version = row.getByRole('button', { name: `Copy version ${history.releases[3]!.version}`, exact: true })
  const address = page.url()
  await version.focus()
  await expect(version).toHaveAttribute('data-version-view', 'revealed')
  await expect(version.locator('[data-version-character="canonical"]').last()).toHaveCSS('opacity', '1')
  expect(await version.locator('[data-version-character="canonical"]').evaluateAll(nodes => nodes.every(node => (node as HTMLElement).style.transition === 'none'))).toBe(true)
  for (const action of ['Enter', 'Space', 'click']) {
    if (action === 'click') await version.click()
    else await version.press(action)
    await expect(version).toHaveAttribute('data-copy-state', 'copied')
    await expect(row.getByRole('status')).toHaveText('Version copied')
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(history.releases[3]!.version)
    await expect(row).toHaveAttribute('aria-selected', 'false')
    expect(page.url()).toBe(address)
  }
  // Clicking the persistent name still selects, and j/k still move the list.
  await row.locator('.rn-name').click()
  await expect(row).toHaveAttribute('aria-selected', 'true')
  await expect(page).toHaveURL(`/releases/${history.releases[3]!.version}`)
  await sheet(page).getByRole('grid').focus()
  await page.keyboard.press('k')
  await expect(options(page).nth(2)).toHaveAttribute('aria-selected', 'true')
})

test('release list copy controls have accessible interactive row semantics', async ({ page }) => {
  await open(page)
  const results = await new AxeBuilder({ page }).include('.releases .list-pane').withRules(['nested-interactive', 'aria-required-children', 'aria-required-parent']).analyze()
  expect(results.violations.map(violation => ({ id: violation.id, targets: violation.nodes.map(node => node.target) }))).toEqual([])
})

for (const width of [320, 390, 600, 1600]) {
  test(`the list version stays on the name line and right-aligned at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    await open(page)
    if (width <= 760) {
      // Phone detail hides the grid, but keeps its selected row for Back.
      await expect(sheet(page).getByRole('row', { selected: true, includeHidden: true })).toHaveCount(1)
      await sheet(page).getByRole('button', { name: 'All releases' }).click()
    }
    const row = options(page).first()
    const name = row.locator('.row-name')
    const version = row.locator('.row-version')
    const heading = row.locator('.line1')
    await expect(row.locator('.row-identity .current-tag')).toBeVisible()
    const [nameBounds, versionBounds, headingBounds] = await Promise.all([name.boundingBox(), version.boundingBox(), heading.boundingBox()])
    expect(Math.abs(versionBounds!.x + versionBounds!.width - headingBounds!.x - headingBounds!.width)).toBeLessThanOrEqual(1)
    expect(versionBounds!.y).toBe(nameBounds!.y)
    const button = version.getByRole('button')
    await sheet(page).getByRole('grid').focus()
    await page.keyboard.press('Tab')
    await expect(button).toBeFocused()
    await expect(button.locator('[data-version-character="canonical"]').last()).toHaveCSS('opacity', '1')
    expect((await button.boundingBox())!.height).toBeGreaterThanOrEqual(44)
    expect(await sheet(page).evaluate(el => el.scrollWidth - el.clientWidth)).toBe(0)
    expect(await row.evaluate(el => el.scrollWidth - el.clientWidth)).toBe(0)
    if (process.env.RELEASE_LIST_SHOTS && width <= 390) {
      await mkdir(process.env.RELEASE_LIST_SHOTS, { recursive: true })
      await page.screenshot({ path: join(process.env.RELEASE_LIST_SHOTS, `after-${width}px.png`) })
    }
  })
}

// AEON-576: real renderer layers, badges and long names share a single line.
// Keep reservations opted in so this also works after AEON-556 lands.
for (const width of [1440, 1024, 390, 320]) {
  for (const theme of ['light', 'dark'] as const) {
    test(`release row geometry at ${width}px in ${theme}`, async ({ page }) => {
      await page.setViewportSize({ width, height: 1000 })
      await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
      const data = fixtures()
      data.preferences['developer-ui'] = { show_reserved_versions: true }
      data.preferences.theme = { choice: theme }
      const history = presentedHistory(Date.parse('2026-10-02T12:00:00Z'))
      await page.clock.setFixedTime(new Date('2026-10-02T12:00:00Z'))
      Object.assign(history.releases[0]!, { codename: 'Sunlit Sonde' })
      Object.assign(history.releases[1]!, { codename: 'Rugged Ratio' })
      Object.assign(history.releases[2]!, { codename: 'Pure Probe', release_sequence: 54 })
      Object.assign(history.releases[3]!, { codename: 'Deliberately Long Code Name That Must Truncate Before The Version Or Badge' })
      data.preferences.releases = { last_seen: history.releases[4]!.version }
      await mockWork(page, data)
      await mockReleases(page, history)
      await page.goto('/releases/all?release_lang=en')
      await expect(options(page)).toHaveCount(history.releases.length)
      await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
      await expect(options(page).nth(0).locator('.current-tag')).toBeVisible()
      await expect(options(page).nth(1).locator('.tag').filter({ hasText: 'Rollback target' })).toBeVisible()
      await expect(options(page).nth(2)).toContainText('Reserved, never published')
      await expect(options(page).nth(0).locator('.new-tag')).toBeVisible()
      await page.evaluate(() => document.fonts.ready)

      for (const index of [0, 1, 2, 3]) {
        const row = options(page).nth(index)
        await row.scrollIntoViewIfNeeded()
        const name = row.locator('.row-name')
        const version = row.locator('.row-version')
        const button = version.getByRole('button')
        const before = await button.boundingBox()
        const geometry = await row.evaluate(el => {
          const name = el.querySelector<HTMLElement>('.row-name')!
          const text = name.querySelector<HTMLElement>('.rn-name')!
          const version = el.querySelector<HTMLElement>('.row-version')!
          const layers = version.querySelector<HTMLElement>('.version-layers')!
          const heading = el.querySelector<HTMLElement>('.line1')!
          const baseline = (node: Element) => {
            const marker = document.createElement('span')
            marker.style.cssText = 'display:inline-block;width:0;height:0;vertical-align:baseline'
            node.append(marker)
            const top = marker.getBoundingClientRect().top
            marker.remove()
            return top
          }
          const rect = (node: Element) => {
            const box = node.getBoundingClientRect()
            return { top: box.top, right: box.right, left: box.left, height: box.height, width: box.width }
          }
          const canvas = document.createElement('canvas').getContext('2d')!
          canvas.font = getComputedStyle(text).font
          canvas.letterSpacing = getComputedStyle(text).letterSpacing
          const identity = el.querySelector<HTMLElement>('.row-identity')!
          const badges = [...identity.querySelectorAll<HTMLElement>('.tag')].map(tag => {
            const label = tag.querySelector<HTMLElement>('.tag-label')!
            // Measure intrinsic text without changing the badge on screen.
            const full = tag.cloneNode(true) as HTMLElement
            full.classList.remove('compact')
            full.style.cssText = 'position:absolute;visibility:hidden;width:max-content'
            identity.append(full)
            const fullWidth = full.getBoundingClientRect().width
            full.remove()
            return {
              ...rect(tag), fullWidth, label: rect(label),
              labelPosition: getComputedStyle(label).position,
              baseline: getComputedStyle(label).position === 'absolute' ? null : baseline(label),
            }
          })
          return {
            name: rect(name), text: rect(text), version: rect(version), layers: rect(layers), heading: rect(heading),
            identity: rect(identity), badgeGap: parseFloat(getComputedStyle(identity).columnGap), badges,
            nameBaseline: baseline(text),
            versionBaseline: baseline(version.querySelector('.version-pretty')!),
            renderers: [...layers.children].map(rect),
            ellipsis: getComputedStyle(text).textOverflow,
            ellipsisWidth: canvas.measureText('…').width,
            truncated: text.scrollWidth > text.clientWidth,
            nowrap: getComputedStyle(text).whiteSpace,
            overflow: el.scrollWidth - el.clientWidth,
          }
        })
        const label = `${width} ${theme} row ${index}`
        expect(geometry.version.top, label).toBe(geometry.name.top)
        expect(geometry.layers.top, label).toBe(geometry.text.top)
        expect(geometry.versionBaseline, `${label} text baseline`).toBe(geometry.nameBaseline)
        expect(geometry.heading.height, label).toBe(geometry.version.height)
        expect(geometry.version.right, label).toBeCloseTo(geometry.heading.right, 0)
        expect(geometry.text.right, label).toBeLessThanOrEqual(geometry.version.left)
        expect(geometry.ellipsis, label).toBe('ellipsis')
        expect(geometry.nowrap, label).toBe('nowrap')
        if (index === 3) expect(geometry.truncated, label).toBe(true)
        if (geometry.truncated) expect(geometry.text.width, `${label} ellipsis fits`).toBeGreaterThanOrEqual(geometry.ellipsisWidth)
        expect(geometry.overflow, label).toBe(0)
        for (const badge of geometry.badges) {
          const otherWidth = geometry.badges.reduce((sum, other) => sum + (other === badge ? 0 : other.width), 0)
          const available = geometry.identity.width - geometry.ellipsisWidth - geometry.badgeGap * geometry.badges.length - otherWidth
          if (badge.fullWidth <= available) {
            expect(badge.labelPosition, `${label} fitting badge stays in flow`).toBe('static')
          } else {
            expect(badge.labelPosition, `${label} overflowing badge uses its dot`).toBe('absolute')
          }
          if (badge.labelPosition === 'static') {
            expect(badge.baseline, `${label} badge baseline`).toBe(geometry.nameBaseline)
            expect(badge.label.width, `${label} readable badge`).toBeGreaterThan(1)
            expect(badge.label.left, label).toBeGreaterThanOrEqual(badge.left)
            expect(badge.label.right, label).toBeLessThanOrEqual(badge.right)
          }
          expect(badge.left, label).toBeGreaterThanOrEqual(geometry.text.right)
          expect(badge.right, label).toBeLessThanOrEqual(geometry.version.left)
          expect(badge.top, label).toBeGreaterThanOrEqual(geometry.name.top)
          expect(badge.top + badge.height, label).toBeLessThanOrEqual(geometry.name.top + geometry.name.height)
        }
        for (const renderer of geometry.renderers) {
          expect(renderer.left, label).toBeGreaterThanOrEqual(geometry.version.left)
          expect(renderer.right, label).toBeLessThanOrEqual(geometry.version.right)
          expect(renderer.height, label).toBe(geometry.text.height)
        }
        if (index === 0 && (width === 1024 || width === 390)) {
          expect(geometry.badges[0]!.labelPosition, `${label} Current stays readable`).toBe('static')
        }
        await row.hover()
        expect(await button.boundingBox(), `${label} row hover`).toEqual(before)
        await button.hover()
        await expect(button).toHaveAttribute('data-version-view', 'revealed')
        await expect(button.locator('[data-version-character="canonical"]').last()).toHaveCSS('opacity', '1')
        expect(await button.boundingBox(), `${label} version hover`).toEqual(before)
        await button.focus()
        expect(await button.boundingBox(), `${label} keyboard focus`).toEqual(before)
        expect((await button.boundingBox())!.height, label).toBeGreaterThanOrEqual(44)
        const bounds = await name.boundingBox()
        expect(bounds!.y, `${label} revealed`).toBe((await version.boundingBox())!.y)
        await sheet(page).getByRole('grid').focus()
        await page.mouse.move(1, 1)
      }
      // Keyboard selection keeps the list visible on phones, unlike opening detail.
      const row = options(page).nth(1)
      const copy = row.locator('.row-version').getByRole('button')
      await row.scrollIntoViewIfNeeded()
      await sheet(page).getByRole('grid').focus()
      const before = await copy.boundingBox()
      await page.keyboard.press('Home')
      await page.keyboard.press('j')
      await expect(row).toHaveAttribute('aria-selected', 'true')
      expect(await copy.boundingBox()).toEqual(before)
      expect(await sheet(page).evaluate(el => el.scrollWidth - el.clientWidth)).toBe(0)
      if (process.env.RELEASE_LIST_SHOTS) {
        await options(page).first().evaluate(el => el.scrollIntoView({ block: 'center' }))
        await mkdir(process.env.RELEASE_LIST_SHOTS, { recursive: true })
        await page.screenshot({ path: join(process.env.RELEASE_LIST_SHOTS, `row-${width}-${theme}.png`) })
      }
      if (width === 390) {
        const current = options(page).first()
        await page.setViewportSize({ width: 1440, height: 1000 })
        await expect(current.locator('.new-tag .tag-label')).toHaveCSS('position', 'static')
        await page.setViewportSize({ width, height: 1000 })
        await expect(current.locator('.new-tag .tag-label')).toHaveCSS('position', 'absolute')
        await expect(current.locator('.current-tag .tag-label')).toHaveCSS('position', 'static')
      }
    })
  }
}

test('the version track grows with wider monospace glyphs without covering a badge', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await open(page)
  const row = options(page).first()
  await row.locator('.version-copy').evaluate(el => { el.style.fontSize = '16px' })
  await expect.poll(async () => {
    const [identity, version, button, heading] = await Promise.all([
      row.locator('.row-identity').boundingBox(), row.locator('.row-version').boundingBox(),
      row.locator('.version-copy').boundingBox(), row.locator('.line1').boundingBox(),
    ])
    return identity!.x + identity!.width <= version!.x && version!.width === button!.width && version!.x + version!.width <= heading!.x + heading!.width
  }).toBe(true)
  expect((await row.locator('.row-version').boundingBox())!.width).toBeGreaterThan(146)
  const version = row.locator('.row-version')
  const before = await version.boundingBox()
  await version.getByRole('button').hover()
  await expect(version.locator('[data-version-character="canonical"]').last()).toHaveCSS('opacity', '1')
  expect(await version.boundingBox()).toEqual(before)
  expect(await row.evaluate(el => el.scrollWidth - el.clientWidth)).toBe(0)
})

test('the detail heading is the name; the number is a quiet line in the notes; search finds a name', async ({ page }) => {
  const history = await open(page)
  const detail = sheet(page).locator('.detail')
  await expect(detail.locator('h2 .rn-name')).toHaveText(CODENAMES[5])
  // No "Release 113"-style number in the header; Details shows it in the technical line (AEON-488).
  await expect(detail.locator('.eyebrow.top')).not.toContainText(/\d/)
  await expect(detail.locator('.tech')).toHaveCount(0)
  await sheet(page).getByRole('radio', { name: 'Details' }).click()
  await expect(detail.locator('.tech')).toContainText(`PAIMOS 7 · Release ${history.releases[0].release_sequence} · stable · ${history.releases[0].version}`)
  await sheet(page).getByRole('radio', { name: 'Highlights' }).click()
  await page.getByRole('searchbox', { name: 'Search releases' }).fill('cyan')
  await expect(options(page)).toHaveCount(1)
  await expect(options(page).first().locator('mark')).toHaveText('Cyan')
  await expect(sheet(page).locator('.detail h2 .rn-name')).toHaveText('Cyan Cell')
})

test('the stamp is reachable by keyboard and described to a screen reader', async ({ page }) => {
  await open(page)
  const heading = sheet(page).locator('.detail h2 .release-name')
  await expect(heading).toHaveAttribute('aria-describedby', /.+/)
  const tip = sheet(page).locator(`[id="${await heading.getAttribute('aria-describedby')}"]`)
  await expect(tip).toHaveAttribute('role', 'tooltip')
  await expect(tip.locator('.calendar-version')).toHaveAttribute('aria-label', /^\d{12}\.0\.0 · 20\d\d-/)
  await heading.focus()
  await expect(tip).toHaveCSS('opacity', '1')
})

test('phones show the name without horizontal scroll', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await open(page)
  await page.getByRole('button', { name: 'All releases' }).click()
  await expect(options(page).nth(3).locator('.rn-name')).toHaveText(CODENAMES[3])
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
  expect(overflow).toBeLessThanOrEqual(0)
})

test('the footer shows both codename and Pretty version; hover shows seconds in a card', async ({ page }) => {
  await mockWork(page, fixtures())
  const history = presentedHistory()
  await mockReleases(page, history, { codename: CODENAMES[5] })
  await page.goto('/')
  const pill = page.locator('footer.app-footer .version-pill')
  await expect(page.locator('footer.app-footer .footer-name')).toHaveText('PAIMOS AEON')
  await expect(pill.locator('.footer-codename')).toHaveText(CODENAMES[5])
  await expect(pill).toHaveAccessibleName(new RegExp(`${CODENAMES[5]}, version ${history.current.replace(/\./g, '\\.')}`))
  const version = pill.locator('.calendar-version')
  await expect(version).toHaveAttribute('data-version-view', 'pretty')
  await pill.hover()
  await expect(page.locator('.release-hover-card .calendar-version')).toHaveAttribute('data-version-view', 'revealed')
  await expect(version).toHaveAttribute('data-version-view', 'pretty')
  await expect(pill.locator('.footer-codename')).toBeVisible()
  await page.mouse.move(5, 5)
  await expect(version).toHaveAttribute('data-version-view', 'pretty')
  await pill.click()
  await expect(sheet(page)).toBeVisible()
})

// The calendar version a stamp describes: canonical, then its UTC date-time.
const STAMP = /^\d{12}\.0\.0 · 20\d\d-\d\d-\d\d \d\d:\d\d:\d\d UTC$/

// AEON-430 fix round 1: the surfaces that still drew the calendar version at rest.
async function openNamed(page: Page) {
  await mockWork(page, fixtures())
  const history = presentedHistory()
  await mockReleases(page, history, { codename: CODENAMES[5] })
  await page.goto('/')
  await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
  await expect(page.locator('footer.app-footer .version-pill .footer-codename')).toHaveText(CODENAMES[5])
  return history
}
const accountMenu = async (page: Page) => {
  await page.getByRole('button', { name: `Account for ${me.name}` }).click()
  const menu = page.getByRole('menu', { name: 'Account' })
  await expect(menu).toBeVisible()
  return menu
}

test('the account menu names the release; focus reveals its version, and the item still copies it', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const history = await openNamed(page)
  const menu = await accountMenu(page)
  const item = menu.getByRole('menuitem', { name: new RegExp(`^${CODENAMES[5]}, version ${history.current.replace(/\./g, '\\.')} — Copy version$`) })
  await expect(item).toBeVisible()
  await expect(item.locator('.rn-name')).toHaveText(CODENAMES[5])
  // No calendar version at rest: the name shows, the stamp waits.
  await expect(item.locator('.rn-name')).toHaveCSS('opacity', '1')
  await expect(item.locator('.rn-stamp')).toHaveCSS('opacity', '0')
  // It is a menu item: roving focus reaches it with the arrow keys, and focus reveals the stamp.
  await menu.getByRole('menuitem', { name: 'Sign out' }).focus()
  await page.keyboard.press('ArrowDown')
  await expect(item).toBeFocused()
  await expect(item.locator('.rn-stamp')).toHaveCSS('opacity', '1')
  await expect(item.locator('.rn-stamp .calendar-version')).toContainText(/^\d\d·\d\d·\d\d \d\d:\d\d/)
  // The control, not the text inside it, is described by the stamp.
  await expect(item).toHaveAccessibleDescription(STAMP)
  await page.keyboard.press('Enter')
  await expect(item).toHaveAttribute('data-copy-state', 'copied')
  await expect(item.locator('[role="status"]')).toHaveText('Copied')
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(history.current)
  // Menu navigation is unchanged: the next item is the history.
  await page.keyboard.press('ArrowDown')
  await expect(menu.getByRole('menuitem', { name: 'Release history' })).toBeFocused()
  await expect(item.locator('.rn-stamp')).toHaveCSS('opacity', '0')
  // Hover reveals too.
  await item.hover()
  await expect(item.locator('.rn-stamp')).toHaveCSS('opacity', '1')
})

test('the footer names the release; the detail stamp and independent list copy remain accessible', async ({ page }) => {
  const history = await openNamed(page)
  const pill = page.locator('footer.app-footer .version-pill')
  await expect(pill).toHaveAccessibleName(new RegExp(`${CODENAMES[5]}, version`))
  await pill.click()
  await expect(sheet(page)).toBeVisible()
  const row = options(page).nth(0)
  await expect(row.locator('.rn-stamp')).toHaveCount(0)
  await expect(row.getByRole('button', { name: `Copy version ${history.current}`, exact: true })).toBeVisible()
  // Standing alone, the name is focusable and carries its own description.
  const heading = sheet(page).locator('.detail h2 .release-name')
  await expect(heading).toHaveAttribute('tabindex', '0')
  await expect(heading).toHaveAccessibleDescription(STAMP)
  // The stamp describes; it is not part of the name (a heading is read by its content).
  await expect(sheet(page).locator('#release-detail-title')).toHaveAccessibleName(CODENAMES[5])
  expect(history.current).toBeTruthy()
})

test('a description the control already had stays beside the stamp, and goes with the name', async ({ page }) => {
  await openNamed(page)
  const menu = await accountMenu(page)
  const pill = menu.getByRole('menuitem', { name: / — Copy version$/ })
  const stampId = await pill.evaluate(el => el.getAttribute('aria-describedby'))
  expect(stampId).toMatch(/\S+/)
  await pill.evaluate(el => el.setAttribute('aria-describedby', 'owner-hint'))
  // The pill's owner rewrote the attribute; the stamp is put back next to it.
  await expect.poll(() => pill.evaluate(el => el.getAttribute('aria-describedby'))).toBe(`owner-hint ${stampId}`)
})
