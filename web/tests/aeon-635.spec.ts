// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { test, expect, type Locator, type Page } from '@playwright/test'
import { presentRelease, type ReleaseHistory } from '../src/lib/releases'
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

async function open(page: Page, pendingStatus: 'available' | 'partial' | 'unavailable' = 'available') {
  const { history, tickets } = data()
  const errors = watchErrors(page)
  await mockWork(page, tickets)
  await mockReleases(page, history as unknown as Record<string, unknown>)
  await page.route('**/api/releases/pending**', route => route.fulfill({ json: {
    live_version: history.current, base_commit: history.releases[0]!.evidence.source_commit, head_commit: 'c'.repeat(40),
    checked_at: new Date(NOW).toISOString(), source: 'github-main', status: pendingStatus,
    total: pendingStatus === 'available' ? 2 : null, known_total: pendingStatus === 'unavailable' ? 0 : 2,
    changes: pendingStatus === 'unavailable' ? [] : [
      { commit: 'd'.repeat(40), type: 'feat', group: 'features', subject: 'AEON-623: Usage shows every configured account', scope: '', tickets: ['AEON-623'], at: new Date(NOW).toISOString() },
      { commit: 'e'.repeat(40), type: 'fix', group: 'fixes', subject: 'AEON-626: Agents page shows the model in use', scope: '', tickets: ['AEON-626'], at: new Date(NOW).toISOString() },
    ],
    next_cursor: null, unavailable: pendingStatus === 'available' ? [] : ['The main branch could not be read completely.'],
  } }))
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
