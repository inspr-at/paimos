// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { test, expect, type Locator, type Page } from '@playwright/test'
import { presentRelease, type ReleaseHistory } from '../src/lib/releases'
import type { PendingChanges } from '../src/lib/releasePending'
import { expectStableControls } from './helpers/stable'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { mockReleases, presentedHistory } from './releases-fixtures'

const NOW = Date.parse('2026-10-03T12:00:00Z')
const SHOTS = 'test-results/aeon-635'
const sheet = (page: Page) => page.locator('dialog.releases')
const detail = (page: Page) => sheet(page).locator('article.detail')
const detailName = (page: Page) => detail(page).getByRole('heading', { level: 2 }).locator('.rn-name')
const rows = (page: Page) => sheet(page).locator('.listbox [role="row"]')
const radio = (page: Page, name: string) => sheet(page).getByRole('radio', { name, exact: true, includeHidden: true })
const panel = (page: Page) => sheet(page).getByRole('complementary', { name: 'Ticket details' })
const row = (page: Page, version: string) => sheet(page).locator(`#release-${version.replaceAll('.', '-')}`)
const pendingPane = (page: Page) => sheet(page).getByRole('region', { name: 'Changes waiting for the next release', exact: true })

test.describe('coarse-pointer version copy (AEON-635 fix3)', () => {
  test.use({ hasTouch: true })
  for (const colorScheme of ['light', 'dark'] as const) {
    for (const width of [390, 1024, 1440]) {
      test(`44px version-copy targets stay within their rows at ${width} ${colorScheme}`, async ({ page, context }) => {
        await context.grantPermissions(['clipboard-read', 'clipboard-write'])
        await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
        await page.emulateMedia({ colorScheme, reducedMotion: 'reduce' })
        const { history, errors } = await open(page)
        expect(await page.evaluate(() => matchMedia('(pointer: coarse)').matches)).toBe(true)
        if (width === 390) await sheet(page).getByRole('button', { name: 'All releases', exact: true }).click()
        const first = row(page, history.releases[0]!.version), second = row(page, history.releases[1]!.version)
        const copy = first.getByRole('button', { name: `Copy version ${history.releases[0]!.version}`, exact: true })
        await first.scrollIntoViewIfNeeded()
        // Measure real button rectangles throughout the list. Touch targets
        // must stay in their own row rather than expand over a neighbour.
        const geometry = await rows(page).evaluateAll(elements => elements.map(element => {
          const bounds = element.getBoundingClientRect()
          const target = element.querySelector<HTMLButtonElement>('.version-copy')!.getBoundingClientRect()
          return { width: target.width, height: target.height,
            inside: target.left >= bounds.left && target.right <= bounds.right && target.top >= bounds.top && target.bottom <= bounds.bottom,
            top: bounds.top, bottom: bounds.bottom }
        }))
        expect(geometry.length).toBe(history.releases.length)
        for (const [index, target] of geometry.entries()) {
          expect(target.width, `row ${index} touch width`).toBeGreaterThanOrEqual(44)
          expect(target.height, `row ${index} touch height`).toBeGreaterThanOrEqual(44)
          expect(target.inside, `row ${index} target stays inside its row`).toBe(true)
          if (index > 0) expect(target.top, `row ${index} does not overlap its neighbour`).toBeGreaterThanOrEqual(geometry[index - 1]!.bottom)
        }
        const selected = await first.getAttribute('aria-selected')
        await expectStableControls({
          controls: { ...headerControls(page, width === 390), liveRow: first, neighbouringRow: second, copy },
          scrollAreas: { sheet: sheet(page), list: sheet(page).locator('.list-pane') },
          interactions: [
            { name: 'tap version copy', run: async () => {
              await copy.tap()
              await expect(copy).toHaveAttribute('data-copy-state', 'copied')
              expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(history.releases[0]!.version)
              await expect(first).toHaveAttribute('aria-selected', selected!)
            } },
            { name: 'German rows', run: async () => {
              await radio(page, 'DE').click()
              await expect(radio(page, 'DE')).toHaveAttribute('aria-checked', 'true')
            } },
          ],
        })
        await shot(page, width, colorScheme, 'fix3-touch-list-de')
        expect(errors).toEqual([])
      })
    }
  }
})

function data() {
  const history = presentedHistory(NOW) as ReleaseHistory
  history.releases = history.releases.filter(r => r.state === 'published')
  // A two-line name tests the approved row treatment in both languages.
  history.releases[1]!.codename = 'Blue Bot beyond the Interstellar Signal Relay'
  const tickets = fixtures()
  for (const key of ['AEON-305', 'AEON-289', 'AEON-290', 'AEON-301', 'AEON-74', 'AEON-623', 'AEON-626']) {
    tickets.nodes.push({
      id: `n-${key}`, key, kind_slug: 'ticket', title: key === 'AEON-305' ? 'Named releases' : `Release work ${key}`,
      body: 'Captured release work.', state: 'done', project: 'p-aeon', fields: { priority: 'medium' }, parent_id: 'p-aeon',
      created_at: new Date(NOW).toISOString(), updated_at: new Date(NOW).toISOString(),
    })
  }
  return { history, tickets }
}

async function open(page: Page, pendingStatus: 'available' | 'partial' | 'unavailable' = 'available', pendingPage?: (snapshot: PendingChanges, cursor: string | null) => PendingChanges | Promise<PendingChanges>, running?: string) {
  const { history, tickets } = data()
  const errors = watchErrors(page)
  await mockWork(page, tickets)
  await mockReleases(page, history as unknown as Record<string, unknown>, { running })
  const snapshot: PendingChanges = {
    live_version: history.current, base_commit: history.releases[0]!.evidence.source_commit, head_commit: 'c'.repeat(40),
    checked_at: new Date(NOW).toISOString(), source: 'github-main', status: pendingStatus,
    total: pendingStatus === 'available' ? 2 : null, known_total: pendingStatus === 'unavailable' ? 0 : 2,
    changes: pendingStatus === 'unavailable' ? [] : [
      { commit: 'd'.repeat(40), type: 'feat', group: 'features', subject: 'AEON-623: Usage shows every configured account', scope: '', tickets: ['AEON-623'], at: new Date(NOW).toISOString() },
      { commit: 'e'.repeat(40), type: 'fix', group: 'fixes', subject: 'AEON-626: Agents page shows the model in use', scope: '', tickets: ['AEON-626'], at: new Date(NOW).toISOString() },
    ],
    next_cursor: null, unavailable: pendingStatus === 'available' ? [] : ['The main branch could not be read completely.'],
  }
  await page.route('**/api/releases/pending**', async route => route.fulfill({ json: pendingPage ? await pendingPage(snapshot, new URL(route.request().url()).searchParams.get('cursor')) : snapshot }))
  await page.goto(`/releases/${history.current}?release_lang=en&release_view=highlights`)
  await expect(sheet(page)).toBeVisible()
  await expect(detailName(page)).toHaveText(history.releases[0]!.codename!)
  return { history, errors }
}

function headerControls(page: Page, phone: boolean): Record<string, Locator> {
  const root = sheet(page)
  return {
    headerLine: root.locator('.shell > .head .eyebrow'),
    languageGroup: root.getByRole('radiogroup', { name: 'Language', includeHidden: true }),
    viewGroup: root.getByRole('radiogroup', { name: 'View', includeHidden: true }),
    en: radio(page, 'EN'), de: radio(page, 'DE'), highlights: radio(page, 'Highlights'), details: radio(page, 'Details'),
    search: root.locator('input[aria-label="Search releases"]'), compare: root.locator('.compare-btn'), close: root.locator('.close-btn'),
    ...(phone ? { phoneFrame: root, mobileActions: root.locator('.mobile-footer') } : {}),
  }
}

async function shot(page: Page, width: number, theme: string, name: string) {
  mkdirSync(SHOTS, { recursive: true })
  await sheet(page).screenshot({ path: `${SHOTS}/${width}-${theme}-${name}.png` })
}

for (const colorScheme of ['light', 'dark'] as const) {
  for (const width of [390, 1024, 1440]) {
    test(`approved releases keep their controls still at ${width} ${colorScheme} (AEON-635)`, async ({ page }) => {
      test.setTimeout(90_000)
      const phone = width === 390
      await page.setViewportSize({ width, height: phone ? 844 : 1000 })
      await page.emulateMedia({ colorScheme, reducedMotion: 'reduce' })
      const { history, errors } = await open(page)
      const root = sheet(page), first = history.releases[0]!, second = history.releases[1]!
      const controls = headerControls(page, phone)
      const headerText = await root.locator('.shell > .head .eyebrow').innerText()
      const search = root.locator('input[aria-label="Search releases"]')
      const next = root.getByRole('button', { name: 'Next release', exact: true })
      const previous = root.getByRole('button', { name: 'Previous release', exact: true })
      const evidence = root.locator('.evidence-control')
      const actionControls = { ...controls, detailActions: root.locator('.detail-actions'), next, previous, pager: root.locator('.pager'), evidence, contextAction: root.locator('.detail-left') }
      const shown = presentRelease(first)
      await expect(detail(page).locator('.counts')).toHaveText(new RegExp(`^\\s*${shown.features.length} features\\s*·\\s*${shown.fixes.length} fix\\s*·\\s*${shown.other.length} other\\s*$`))
      await expect(root.locator('.detail-action-state')).toHaveText(`1 of ${history.releases.length}`)
      await shot(page, width, colorScheme, 'highlights-en')

      await expectStableControls({
        controls: actionControls,
        scrollAreas: { sheet: root, detail: root.locator('.detail-scroll') },
        interactions: [
          { name: 'Details', run: async () => { await radio(page, 'Details').click(); await expect(radio(page, 'Details')).toHaveAttribute('aria-checked', 'true'); await expect(detail(page).locator('.benefit')).toHaveCount(0) } },
          { name: 'German Details', run: async () => { await radio(page, 'DE').click(); await expect(detail(page).getByRole('article', { name: 'Benannte Releases' })).toBeVisible() } },
          { name: 'German Highlights', run: async () => { await radio(page, 'Highlights').click(); await expect(detail(page).getByRole('article', { name: 'Benannte Releases' }).locator('.benefit')).toContainText('Jedes Release') } },
          { name: 'English Highlights', run: async () => { await radio(page, 'EN').click(); await expect(detail(page).getByRole('article', { name: 'Named releases' })).toBeVisible() } },
          { name: 'next release', run: async () => { await next.click(); await expect(detailName(page)).toHaveText(second.codename!); await expect(root.locator('.detail-left')).toContainText(`1 release behind live · back to ${first.codename}`) } },
          { name: 'previous release', run: async () => { await previous.click(); await expect(detailName(page)).toHaveText(first.codename!); await expect(root.locator('.detail-action-state')).toHaveText(`1 of ${history.releases.length}`) } },
          { name: 'open evidence', run: async () => { await evidence.click(); await expect(detail(page).getByRole('region', { name: 'Evidence', exact: true })).toBeVisible() } },
          { name: 'close evidence', run: async () => { await evidence.click(); await expect(evidence).toHaveAttribute('aria-pressed', 'false') } },
        ],
      })
      await expect(root.locator('.shell > .head .eyebrow')).toHaveText(headerText)
      expect(await root.locator('.shell > .head .eyebrow').evaluate(el => getComputedStyle(el).whiteSpace)).toBe('nowrap')

      if (phone) await root.getByRole('button', { name: 'All releases', exact: true }).click()
      await expect(rows(page)).toHaveCount(history.releases.length)
      await expect(root.locator('.filters .filter-label')).toHaveText('Show')
      await expect(root.locator('.list-pane .result-count')).toHaveCount(0)
      await expect(root.locator('.listbox .keys')).toHaveCount(0)
      await expect(row(page, first.version).locator('.current-tag')).toHaveText('Live')
      await expect(row(page, second.version).locator('.rollback-tag')).toHaveText('Rollback')
      const rowCounts = row(page, first.version).locator('.line1 .counts [role="img"]')
      await expect(rowCounts).toHaveCount(3)
      for (const [index, label] of ['3 features', '1 fix', '1 other change'].entries()) await expect(rowCounts.nth(index)).toHaveAttribute('aria-label', label)
      for (const badge of [row(page, first.version).locator('.current-tag'), row(page, second.version).locator('.rollback-tag')]) {
        await expect(badge.locator('svg, .live-dot, .status-dot')).toHaveCount(0)
        const bounds = await badge.evaluate(el => {
          const rect = el.getBoundingClientRect(), time = el.closest('.time')!.getBoundingClientRect()
          return { width: rect.width, height: rect.height, clippedX: el.scrollWidth > el.clientWidth, clippedY: el.scrollHeight > el.clientHeight, inside: rect.x >= time.x && rect.right <= time.right + 0.5 && rect.y >= time.y && rect.bottom <= time.bottom + 0.5 }
        })
        expect(bounds.width).toBeGreaterThan(0)
        expect(bounds.height).toBeGreaterThan(0)
        expect(bounds.clippedX).toBe(false)
        expect(bounds.clippedY).toBe(false)
        expect(bounds.inside).toBe(true)
      }
      await expect(root.locator('.listbox')).toHaveCount(1)
      await expect(row(page, second.version).locator('.row-name .rn-name')).toHaveCSS('-webkit-line-clamp', '2')
      await expect(row(page, second.version).locator('.row-name')).toHaveAttribute('data-tip', second.codename!)
      const stat = root.getByRole('region', { name: 'Release stats', exact: true })
      const nextStat = stat.getByRole('button', { name: 'Next stat', exact: true })
      await expectStableControls({
        controls: { ...controls, statCard: stat, statActions: stat.locator('.arrows'), nextStat, filters: root.locator('.toggles'), liveRow: row(page, first.version) },
        scrollAreas: { sheet: root, releaseList: root.locator('.list-pane') },
        interactions: Array.from({ length: 7 }, (_, i) => ({ name: `stat ${i + 1}`, run: async () => { await nextStat.click(); await expect(stat.locator('.pos')).toHaveText(`${(i + 1) % 7 + 1} / 7`) } })),
      })
      await expectStableControls({
        controls: { ...controls, filters: root.locator('.toggles'), liveRow: row(page, first.version), olderRow: row(page, second.version) },
        scrollAreas: { sheet: root, releaseList: root.locator('.list-pane') },
        interactions: [
          { name: 'German list', run: async () => { await radio(page, 'DE').click(); await expect(row(page, first.version).locator('.headline:not(.summary-size)')).toHaveText('Releases mit Namen'); if (phone) await row(page, first.version).scrollIntoViewIfNeeded(); await shot(page, width, colorScheme, 'list-de') } },
          { name: 'Details list', run: async () => { await radio(page, 'Details').click(); await expect(radio(page, 'Details')).toHaveAttribute('aria-checked', 'true'); await expect(root.locator('.listbox .subjects')).toHaveCount(0) } },
          { name: 'Highlights list', run: async () => { await radio(page, 'Highlights').click(); await expect(radio(page, 'Highlights')).toHaveAttribute('aria-checked', 'true') } },
          { name: 'English list', run: async () => { await radio(page, 'EN').click(); await expect(row(page, first.version).locator('.headline:not(.summary-size)')).toHaveText('Releases with a name') } },
        ],
      })
      const show = root.getByRole('group', { name: 'Show only releases with', exact: true })
      const features = show.getByRole('button', { name: 'Features', exact: true }), fixes = show.getByRole('button', { name: 'Fixes', exact: true }), other = show.getByRole('button', { name: 'Other', exact: true })
      await expectStableControls({
        controls: { ...controls, filters: show, features, fixes, other, clickedRelease: row(page, first.version) },
        scrollAreas: { sheet: root, releaseList: root.locator('.list-pane') },
        interactions: [
          { name: 'Other filter', run: async () => { await other.click(); await expect(other).toHaveAttribute('aria-pressed', 'true'); await expect(rows(page)).toHaveCount(history.releases.filter(r => presentRelease(r).other.length).length) } },
          { name: 'Features with Other', run: async () => { await features.click(); await expect(rows(page)).toHaveCount(history.releases.filter(r => presentRelease(r).features.length && presentRelease(r).other.length).length) } },
          { name: 'Fixes with Features and Other', run: async () => { await fixes.click(); await expect(rows(page)).toHaveCount(1) } },
          { name: 'clear Fixes', run: async () => { await fixes.click(); await expect(fixes).toHaveAttribute('aria-pressed', 'false') } },
          { name: 'clear Features', run: async () => { await features.click(); await expect(features).toHaveAttribute('aria-pressed', 'false') } },
          { name: 'clear Other', run: async () => { await other.click(); await expect(rows(page)).toHaveCount(history.releases.length) } },
          { name: 'search release name', run: async () => { await search.fill(first.codename!); await expect(rows(page)).toHaveCount(1) } },
          { name: 'clear search', run: async () => { await search.fill(''); await expect(rows(page)).toHaveCount(history.releases.length) } },
        ],
      })
      if (!phone) {
        await expectStableControls({
          controls: { ...actionControls, releaseList: root.locator('.listbox'), clickedOlder: row(page, second.version), clickedLive: row(page, first.version) },
          scrollAreas: { sheet: root, releaseList: root.locator('.list-pane'), detail: root.locator('.detail-scroll') },
          interactions: [
            { name: 'select older row', run: async () => { await row(page, second.version).click(); await expect(detailName(page)).toHaveText(second.codename!) } },
            { name: 'select live row', run: async () => { await row(page, first.version).click(); await expect(detailName(page)).toHaveText(first.codename!) } },
          ],
        })
      } else {
        await row(page, first.version).click()
        await expect(detail(page)).toBeVisible()
      }
      await radio(page, 'DE').click()
      await radio(page, 'Details').click()
      await expect(detail(page).locator('.summary .intro')).toContainText('Die Release-Historie')
      await shot(page, width, colorScheme, 'details-de')

      const ticket = detail(page).getByRole('link', { name: 'AEON-305: Named releases', exact: true }).first()
      // CSS locators keep sampling the inert release beneath the phone sheet.
      await expectStableControls({
        controls: { ...actionControls, releaseContent: detail(page), ...(phone ? {} : { clickedRelease: row(page, first.version), releaseList: root.locator('.listbox') }) },
        scrollAreas: { sheet: root, detail: root.locator('.detail-scroll') },
        interactions: [
          { name: 'open ticket beside release', run: async () => { await ticket.click(); await expect(panel(page).getByRole('heading', { name: 'Named releases', exact: true })).toBeVisible(); await shot(page, width, colorScheme, 'ticket-de') } },
          { name: 'Escape closes only ticket', run: async () => { await panel(page).focus(); await page.keyboard.press('Escape'); await expect(panel(page)).toHaveCount(0); await expect(detail(page)).toBeVisible(); await expect(ticket).toBeFocused() } },
        ],
      })
      await expect(page).toHaveURL(new RegExp(`/releases/${first.version.replaceAll('.', '\\.')}`))
      const waiting = root.locator('.detail-left button')
      await expect(waiting).toContainText('2 changes waiting for the next release')
      await expectStableControls({
        controls: actionControls,
        scrollAreas: { sheet: root, detail: root.locator('.detail-scroll') },
        interactions: [
          { name: 'open waiting changes', run: async () => { await waiting.click(); await expect(pendingPane(page)).toBeVisible(); await expect(pendingPane(page)).toContainText('Usage shows every configured account'); await expect(pendingPane(page)).toContainText('Agents page shows the model in use'); await shot(page, width, colorScheme, 'waiting-de') } },
          { name: 'Escape closes only waiting changes', run: async () => { await page.keyboard.press('Escape'); await expect(pendingPane(page)).toHaveCount(0); await expect(detail(page)).toBeVisible() } },
        ],
      })
      expect(await root.evaluate(el => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1)
      expect(errors).toEqual([])
    })
  }
}

test('partial and unavailable pending data never report an exact zero (AEON-635)', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await open(page, 'partial')
  const context = sheet(page).locator('.detail-left')
  await expect(context).toContainText('2')
  await expect(context).not.toContainText(/^2 changes waiting/)
  await context.locator('button').click()
  await expect(pendingPane(page)).toContainText('The main branch could not be read completely.')
  await page.keyboard.press('Escape')
  await open(page, 'unavailable')
  await expect(context).not.toContainText('0 changes waiting')
  await context.locator('button').click()
  await expect(pendingPane(page)).toContainText('The main branch could not be read completely.')
})

for (const width of [390, 1024, 1440]) {
  test(`waiting-panel keys keep the selected release and native controls at ${width} (AEON-635 fix2)`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    const { history, errors } = await open(page)
    const root = sheet(page), pane = pendingPane(page)
    const close = pane.getByRole('button', { name: 'Close waiting changes', exact: true })
    const refresh = pane.getByRole('button', { name: 'Refresh', exact: true })
    const url = page.url()
    const evidence = root.locator('.evidence-control')
    const controls = { close, refresh, panelHeader: pane.locator('.pending-head'), panelActions: pane.locator('.pending-controls') }
    await root.locator('.detail-left button').click()
    await expect(close).toBeFocused()
    await expectStableControls({
      controls, scrollAreas: { panel: pane, content: pane.locator('.pending-scroll') },
      interactions: ['/', 'j', 'k', 'ArrowDown', 'ArrowUp', 'Home', 'End', 'c', 'e', '?'].map(key => ({
        name: `waiting key ${key}`, run: async () => {
          await close.focus()
          await page.keyboard.press(key)
          await expect(pane).toBeVisible()
          await expect(close).toBeFocused()
          await expect(detailName(page)).toHaveText(history.releases[0]!.codename!)
          await expect(evidence).toHaveAttribute('aria-pressed', 'false')
          await expect(root.locator('.help-scrim')).toHaveCount(0)
          await expect(root.locator('.compare-hint')).toHaveCount(0)
          expect(page.url()).toBe(url)
        },
      })),
    })
    await close.focus()
    await page.keyboard.press('Tab')
    await expect(refresh).toBeFocused()
    const response = page.waitForResponse(r => new URL(r.url()).pathname === '/api/releases/pending')
    await page.keyboard.press('Enter')
    expect((await response).status()).toBe(200)
    await expect(refresh).toBeEnabled()
    await expect(refresh).toBeFocused()
    // Native modifiers keep their default behavior; no release action consumes them.
    for (const key of ['s', 'r', 'd', 'p', 'a']) {
      expect(await close.evaluate((el, key) => el.dispatchEvent(new KeyboardEvent('keydown', { key, ctrlKey: true, bubbles: true, cancelable: true })), key)).toBe(true)
      expect(await close.evaluate((el, key) => el.dispatchEvent(new KeyboardEvent('keydown', { key, metaKey: true, bubbles: true, cancelable: true })), key)).toBe(true)
    }
    await page.keyboard.press('Escape')
    await expect(pane).toHaveCount(0)
    await expect(detail(page)).toBeVisible()
    await expect(root.locator('.detail-left button')).toBeFocused()
    expect(page.url()).toBe(url)
    expect(errors).toEqual([])
  })
}

test('busy waiting refresh retains keyboard focus and rejects repeated activation (AEON-635 fix2)', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  let requests = 0, entered!: () => void, resume!: () => void
  const started = new Promise<void>(resolve => { entered = resolve })
  const held = new Promise<void>(resolve => { resume = resolve })
  await open(page, 'available', async snapshot => {
    if (++requests === 2) { entered(); await held }
    return snapshot
  })
  await sheet(page).locator('.detail-left button').click()
  const pane = pendingPane(page), refresh = pane.getByRole('button', { name: 'Refresh', exact: true })
  await expect(refresh).toBeEnabled()
  await refresh.focus()
  const response = page.waitForResponse(r => new URL(r.url()).pathname === '/api/releases/pending')
  try {
    await page.keyboard.press('Enter')
    await started
    await expect(refresh).toBeFocused()
    await expect(refresh).toBeDisabled()
    await page.keyboard.press('Enter')
    resume()
    expect((await response).status()).toBe(200)
    await expect(refresh).toBeEnabled()
    await expect(refresh).toBeFocused()
    expect(requests).toBe(2)
    await page.keyboard.press('Escape')
    await expect(pane).toHaveCount(0)
    await expect(detail(page)).toBeVisible()
  } finally { resume(); await response.catch(() => {}) }
})

test('successful pagination retains earlier incomplete evidence until a full refresh (AEON-635 fix2)', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  let refreshed = false
  const reason = 'Ticket classification on the first page was incomplete.'
  const { errors } = await open(page, 'available', (snapshot, cursor) => {
    if (refreshed) return snapshot
    return { ...snapshot, status: cursor ? 'available' : 'partial', total: cursor ? 2 : null,
      changes: [snapshot.changes[cursor ? 1 : 0]!], next_cursor: cursor ? null : 'page-two', unavailable: cursor ? [] : [reason] }
  })
  await sheet(page).locator('.detail-left button').click()
  const pane = pendingPane(page)
  await expect(pane.getByRole('status')).toHaveText('At least 2 changes waiting for the next release')
  await expect(pane.locator('.pending-list li')).toHaveCount(1)
  await expect(pane).toContainText(reason)
  await pane.getByRole('button', { name: 'Show more', exact: true }).click()
  await expect(pane.locator('.pending-list li')).toHaveCount(2)
  await expect(pane.getByRole('status')).toHaveText('At least 2 changes waiting for the next release')
  await expect(pane).toContainText(reason)
  await expect(pane.getByRole('button', { name: 'Show more', exact: true })).toHaveCount(0)
  await expect(pane.getByRole('button', { name: 'Close waiting changes', exact: true })).toBeFocused()
  refreshed = true
  await pane.getByRole('button', { name: 'Refresh', exact: true }).click()
  await expect(pane.getByRole('status')).toHaveText('2 changes waiting for the next release')
  await expect(pane.locator('.pending-list li')).toHaveCount(2)
  await expect(pane).not.toContainText(reason)
  expect(errors).toEqual([])
})

for (const colorScheme of ['light', 'dark'] as const) {
  test(`header status stays on one line from 600px and stacks cleanly on phones ${colorScheme} (AEON-635 fix2)`, async ({ page }) => {
    await page.emulateMedia({ colorScheme, reducedMotion: 'reduce' })
    await page.clock.setFixedTime(NOW + 30 * 60_000)
    await page.setViewportSize({ width: 1440, height: 1000 })
    const { errors } = await open(page)
    for (const width of [1440, 1280, 1181, 1024, 761, 760, 600, 390]) {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
      const dock = sheet(page).locator('.status-dock')
      await expect(dock).toBeVisible()
      const layout = await dock.evaluate(el => {
        const status = el.querySelector('.status-line')!, version = el.querySelector('.dock-version')!
        const range = document.createRange()
        range.selectNodeContents(status.querySelector('span:last-child')!)
        const lines = Array.from(range.getClientRects()).filter(r => r.width > 0 && r.height > 0)
        const a = status.getBoundingClientRect(), b = version.getBoundingClientRect(), box = el.getBoundingClientRect()
        const dialog = el.closest('dialog')!
        return { lines: lines.length, sameLine: a.top < b.bottom && b.top < a.bottom,
          inside: Math.min(a.left, b.left) >= box.left - 0.5 && Math.max(a.right, b.right) <= box.right + 0.5,
          divider: parseFloat(getComputedStyle(version).borderLeftWidth), overflow: dialog.scrollWidth - dialog.clientWidth }
      })
      expect(layout.inside, `dock contents at ${width}`).toBe(true)
      expect(layout.overflow, `sheet overflow at ${width}`).toBeLessThanOrEqual(1)
      if (width >= 600) {
        expect(layout.lines, `status text lines at ${width}`).toBe(1)
        expect(layout.sameLine, `status and version at ${width}`).toBe(true)
      } else expect(layout.divider).toBe(0)
      if ([390, 1024, 1440].includes(width)) {
        await expectStableControls({ controls: headerControls(page, width === 390), scrollAreas: { sheet: sheet(page) },
          interactions: [
            { name: 'German header', run: async () => { await radio(page, 'DE').click(); await expect(radio(page, 'DE')).toHaveAttribute('aria-checked', 'true') } },
            { name: 'Details header', run: async () => { await radio(page, 'Details').click(); await expect(radio(page, 'Details')).toHaveAttribute('aria-checked', 'true') } },
          ] })
        await shot(page, width, colorScheme, 'fix2-header-de')
      }
    }
    expect(errors).toEqual([])
  })
}

test('longer outdated-page status can wrap while Reload stays inside the sheet (AEON-635 fix2)', async ({ page }) => {
  await page.setViewportSize({ width: 600, height: 1000 })
  await open(page, 'available', undefined, data().history.releases[2]!.version)
  const dock = sheet(page).locator('.status-dock')
  await expect(dock.locator('.outdated')).toContainText('this page still runs')
  for (const width of [600, 390, 1024, 1440]) {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await expect(dock.getByRole('button', { name: 'Reload', exact: true })).toBeVisible()
    for (const area of [dock, sheet(page)]) expect(await area.evaluate(el => el.scrollWidth - el.clientWidth), `update status overflow at ${width}`).toBeLessThanOrEqual(1)
  }
})
