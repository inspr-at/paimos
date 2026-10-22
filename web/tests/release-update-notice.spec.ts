// SPDX-License-Identifier: AGPL-3.0-only
// AEON-325: after a deploy the release history shows one notice. An outdated
// page says a newer version is live, including when that version is absent
// from this build's history. A current page still names a version the history
// does not have. Screenshots: AEON_325_SHOTS=<dir>.
import { mkdirSync } from 'node:fs'
import { expect, test, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { mockReleases, releaseHistory, type History } from './releases-fixtures'
import { controlStability } from './control-stability'

// AEON-309: the CalVer3 renderer names a version by its canonical value and UTC
// time; an interactive one adds its copy action.
function versionLabel(version: string, copy = true): string {
  const m = /^(\d\d)(\d\d)(\d\d)(\d\d)(\d\d)(\d\d)\.0\.0$/.exec(version)
  if (!m) throw new Error(`not a calendar version: ${version}`)
  return `${version} · 20${m[1]}-${m[2]}-${m[3]} ${m[4]}:${m[5]}:${m[6]} UTC${copy ? ' — Copy version' : ''}`
}

const SHOTS = process.env.AEON_325_SHOTS ?? ''
const SERVER = '991231235959.0.0'
const ABSENT = '260101120000.0.0'

const sheet = (page: Page) => page.getByRole('dialog', { name: 'PAIMOS AEON releases' })
// AEON-488: an outdated page is the title's status line; a missing version keeps its notice.
const notices = (page: Page) => sheet(page).locator('.notice, .status-line.outdated')
const outdated = (page: Page) => sheet(page).locator('.status-line.outdated')
const title = (page: Page) => sheet(page).getByRole('heading', { level: 1 })

async function expectServerVersion(page: Page) {
  // AEON-635 keeps the unnamed hero and its separate version-copy dock.
  // Assert the same server identity and UTC label at their current controls.
  await expect(title(page)).toHaveText(SERVER)
  const copy = sheet(page).locator('.status-dock').getByRole('button', { name: `Copy version ${SERVER}`, exact: true })
  await expect(copy).toBeVisible()
  await expect(copy.locator('.version-pretty')).toHaveAttribute('aria-label', versionLabel(SERVER, false))
  await expect(copy.locator('.version-canonical')).toHaveText(SERVER)
}

async function openHistory(page: Page, history: History, running: string, target: string) {
  await mockWork(page, fixtures())
  await mockReleases(page, history, { running })
  await page.goto(target === 'all' ? '/releases' : `/releases/${target}`)
  await expect(sheet(page)).toBeVisible()
  await expect(sheet(page).getByRole('grid', { name: 'Releases, newest first' })).toBeVisible()
}

function outdatedHistory() {
  const history = releaseHistory()
  const pageVersion = history.current
  history.current = SERVER
  return { history, pageVersion }
}

test('an outdated page shows one newer-version notice when the server version is not in history', async ({ page }) => {
  const { history, pageVersion } = outdatedHistory()
  await openHistory(page, history, pageVersion, SERVER)
  await expect(notices(page)).toHaveCount(1)
  await expect(outdated(page)).toHaveText(/^Live on the server · this page still runs /)
  await expect(outdated(page).locator('.calendar-version')).toHaveAttribute('aria-label', versionLabel(pageVersion, false))
  // The title names what the server runs; this one has no name in the history, so its version.
  await expectServerVersion(page)
  await expect(outdated(page).getByRole('button', { name: 'Reload' })).toBeVisible()
  await expect(sheet(page).getByText(/not in this build.s release history/)).toHaveCount(0)
  await expect(sheet(page).getByRole('row').first()).toHaveAttribute('aria-selected', 'true')
})

test('an outdated page opened on the history still shows only the newer-version notice', async ({ page }) => {
  const { history, pageVersion } = outdatedHistory()
  await openHistory(page, history, pageVersion, 'all')
  await expect(notices(page)).toHaveCount(1)
  await expect(outdated(page)).toContainText('this page still runs')
  await expect(sheet(page).getByText(/not in this build.s release history/)).toHaveCount(0)
})

test('a current page names a missing version and can dismiss it', async ({ page }) => {
  const history = releaseHistory()
  await openHistory(page, history, history.current, ABSENT)
  await expect(notices(page)).toHaveCount(1)
  await expect(notices(page)).toContainText('is not in this build')
  await expect(notices(page).locator('.calendar-version')).toHaveAttribute('aria-label', versionLabel(ABSENT))
  await expect(sheet(page).getByText(/newer version is live/)).toHaveCount(0)
  await notices(page).getByRole('button', { name: 'Dismiss' }).click()
  await expect(notices(page)).toHaveCount(0)
})

test('a current page on a known release shows no notice', async ({ page }) => {
  const history = releaseHistory()
  await openHistory(page, history, history.current, history.current)
  await expect(notices(page)).toHaveCount(0)
})

test('what’s new opens the one notice for a server version this build does not list', async ({ page }) => {
  const history = releaseHistory()
  const pageVersion = history.current
  await mockWork(page, fixtures())
  const state = await mockReleases(page, history, { running: pageVersion })
  await page.goto('/')
  await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
  history.current = SERVER
  state.server = SERVER
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  const toast = page.locator('.toast').filter({ hasText: 'was updated to' })
  await expect(toast).toBeVisible()
  await toast.getByRole('button', { name: 'What’s new' }).click()
  await expect(sheet(page)).toBeVisible()
  await expect(notices(page)).toHaveCount(1)
  await expect(outdated(page)).toContainText('this page still runs')
  await expect(outdated(page).locator('.calendar-version')).toHaveAttribute('aria-label', versionLabel(pageVersion, false))
  await expectServerVersion(page)
  await expect(sheet(page).getByText(/not in this build.s release history/)).toHaveCount(0)
})

test('what’s new with the history cached before the deploy never shows the missing notice', async ({ page }) => {
  const history = releaseHistory()
  const pageVersion = history.current
  await mockWork(page, fixtures())
  const state = await mockReleases(page, history, { running: pageVersion })
  const cached = page.waitForResponse(response => {
    const path = new URL(response.url()).pathname
    return path.endsWith('/api/releases') && response.ok()
  })
  await page.goto('/')
  await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
  await cached
  // The sheet refetches on open. Hold that response so it paints against the cache.
  let releaseRefetch = () => {}
  const held = new Promise<void>(resolve => { releaseRefetch = resolve })
  let opened = false
  await page.route('**/api/releases**', async route => {
    const path = new URL(route.request().url()).pathname
    if (/\/api\/releases\/[^/]+$/.test(path)) { await route.fallback(); return }
    await held
    opened = true
    await route.fallback()
  })
  history.current = SERVER
  state.server = SERVER
  await page.evaluate(() => {
    const seen: string[] = []
    ;(window as unknown as { __notices?: string[] }).__notices = seen
    const read = () => {
      const text = document.querySelector('dialog .notice')?.textContent?.replace(/\s+/g, ' ').trim() ?? ''
      if (text && seen[seen.length - 1] !== text) seen.push(text)
    }
    new MutationObserver(read).observe(document.body, { childList: true, subtree: true, characterData: true })
  })
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  const toast = page.locator('.toast').filter({ hasText: 'was updated to' })
  await expect(toast).toBeVisible()
  try {
    await toast.getByRole('button', { name: 'What’s new' }).click()
    await expect(sheet(page)).toBeVisible()
    await expect(notices(page)).toHaveCount(1)
    await expect(outdated(page)).toContainText('this page still runs')
    await expect(outdated(page).locator('.calendar-version')).toHaveAttribute('aria-label', versionLabel(pageVersion, false))
    await expectServerVersion(page)
    await expect(sheet(page).getByText(/not in this build.s release history/)).toHaveCount(0)
    const during = await page.evaluate(() => (window as unknown as { __notices?: string[] }).__notices ?? [])
    expect(during.join('\n')).not.toMatch(/not in this build/)
  } finally {
    releaseRefetch()
  }
  await expect.poll(() => opened).toBe(true)
  await expect(sheet(page).getByRole('grid', { name: 'Releases, newest first' })).toBeVisible()
  await expect(notices(page)).toHaveCount(1)
  await expect(outdated(page)).toContainText('this page still runs')
  await expectServerVersion(page)
  await expect(sheet(page).getByText(/not in this build.s release history/)).toHaveCount(0)
  await expect(outdated(page).locator('.calendar-version')).toHaveAttribute('aria-label', versionLabel(pageVersion, false))
  const after = await page.evaluate(() => (window as unknown as { __notices?: string[] }).__notices ?? [])
  expect(after.join('\n')).not.toMatch(/not in this build/)
})

test.describe('screenshots', () => {
test.skip(!SHOTS, 'Screenshots run only with AEON_325_SHOTS=<dir>.')

for (const state of ['outdated', 'missing'] as const) {
  for (const theme of ['light', 'dark'] as const) {
    for (const width of [1600, 390]) {
      test(`shot ${state} ${width} ${theme}`, async ({ page }) => {
        mkdirSync(SHOTS, { recursive: true })
        await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
        await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
        if (state === 'outdated') {
          const { history, pageVersion } = outdatedHistory()
          await openHistory(page, history, pageVersion, SERVER)
          await expect(outdated(page)).toContainText('this page still runs')
        } else {
          const history = releaseHistory()
          await openHistory(page, history, history.current, ABSENT)
          await expect(notices(page)).toContainText('is not in this build')
        }
        await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
        await expect(notices(page)).toBeVisible()
        const overflow = await page.evaluate(() => {
          const dialog = document.querySelector('dialog[open]')
          if (!dialog) return ['no dialog']
          const out: string[] = []
          if (document.documentElement.scrollWidth > document.documentElement.clientWidth + 1) out.push('page scrolls sideways')
          for (const el of dialog.querySelectorAll<HTMLElement>('*')) {
            const r = el.getBoundingClientRect()
            if (!r.width || !r.height || getComputedStyle(el).visibility === 'hidden') continue
            if (r.left < -0.5 || r.right > innerWidth + 0.5) out.push(`${el.className || el.tagName} outside ${Math.round(r.left)}..${Math.round(r.right)}`)
          }
          return out
        })
        expect(overflow).toEqual([])
        await page.screenshot({ path: `${SHOTS}/${state}-${width}-${theme}.png`, animations: 'disabled' })
      })
    }
  }
}
})

// AEON-430: the toast leads with the marketing name. /api/version already told this
// page the name, so a failed release-detail fetch must not turn it back into a number.
test('the update toast uses the codename /api/version gave when the release detail fails, and reveals the version on hover and focus', async ({ page }) => {
  const history = releaseHistory()
  await mockWork(page, fixtures())
  const state = await mockReleases(page, history, { running: history.current })
  await page.goto('/')
  await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
  history.current = SERVER
  state.server = SERVER
  state.codename = 'Hinged Hangar'
  // The detail of the new release is not available (the history has none, and this one fails outright).
  await page.route(`**/api/releases/${SERVER}`, route => route.abort())
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  const toast = page.locator('.toast').filter({ hasText: 'was updated to' })
  await expect(toast).toBeVisible()
  await expect(toast).toContainText('was updated to Hinged Hangar')
  await expect(toast.locator('.rn-name')).toHaveText('Hinged Hangar')
  // No calendar version at rest in the toast: the name shows, the stamp waits.
  await expect(toast.locator('.rn-name')).toHaveCSS('opacity', '1')
  await expect(toast.locator('.rn-stamp')).toHaveCSS('opacity', '0')
  // Hover reveals it in the footer's chip style.
  await toast.locator('.release-name').hover()
  await expect(toast.locator('.rn-stamp')).toHaveCSS('opacity', '1')
  await expect(toast.locator('.rn-stamp .calendar-version')).toContainText(/^\d\d·\d\d·\d\d \d\d:\d\d/)
  await page.mouse.move(2, 2)
  await expect(toast.locator('.rn-stamp')).toHaveCSS('opacity', '0')
  // Keyboard focus reveals it too, and the name is described by it.
  await toast.locator('.release-name').focus()
  await expect(toast.locator('.rn-stamp')).toHaveCSS('opacity', '1')
  await expect(toast.locator('.release-name')).toHaveAccessibleDescription(versionLabel(SERVER, false))
  await toast.getByRole('button', { name: 'What’s new' }).click()
  await expect(sheet(page)).toBeVisible()
})

test('an unnamed release reads as its calendar version in the toast, never as a raw number at rest', async ({ page }) => {
  const history = releaseHistory()
  await mockWork(page, fixtures())
  const state = await mockReleases(page, history, { running: history.current })
  await page.goto('/')
  await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
  history.current = SERVER
  state.server = SERVER
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  const toast = page.locator('.toast').filter({ hasText: 'was updated to' })
  await expect(toast).toBeVisible()
  await expect(toast.locator('.calendar-version')).toHaveAttribute('aria-label', versionLabel(SERVER))
  expect(await toast.locator('> span').first().evaluate(el => el.textContent)).not.toContain(SERVER)
  // AEON-1655: a release without public notes shows nothing after its sentence.
  await expect(toast.locator('.toast-counts')).toHaveCount(0)
})

// AEON-1655: the sentence ends at the name, without the gap the version stamp
// used to leave, and the release's public notes follow as counts by kind.
function countedHistory() {
  const history = releaseHistory()
  const newest = history.releases[0] as History['releases'][number] & Record<string, unknown>
  const item = (key: string, group: 'features' | 'fixes', pill: string, benefit: string) => ({ key, group, pill_en: pill, pill_de: '', benefit_en: benefit, benefit_de: '' })
  newest.notes = { source: 'embedded-product-notes', snapshot_sha256: 'a'.repeat(64), captured_at: newest.reserved_at, release_revision: 1, gaps: [], hidden: 0, items: [], public_items: [
    item('AEON-75', 'features', 'Time entry editing', 'Correct and delete time entries in the Hours view.'),
    item('AEON-76', 'features', 'Week totals', 'The week total stays in view while entries change.'),
    item('AEON-78', 'fixes', 'Stable week total', 'Moving an entry to another day keeps the week total right.'),
  ] }
  return { history, newest, older: history.releases[1].version }
}

async function showUpdateToast(page: Page, history: History, running: string) {
  await mockWork(page, fixtures())
  const state = await mockReleases(page, history, { running })
  await page.goto('/')
  await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
  state.server = history.current
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  const toast = page.locator('.toast').filter({ hasText: 'was updated to' })
  await expect(toast).toBeVisible()
  return toast
}

test('the update toast ends at the release name and counts its public notes by kind, without a gap or a shift', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const { history, newest, older } = countedHistory()
  const toast = await showUpdateToast(page, history, older)
  // The sentence ends at the name; the stamp beside it is hidden until hover or focus.
  const sentence = toast.locator('> span').first()
  await expect(sentence.locator('.rn-name')).toHaveText(newest.codename)
  expect(await sentence.evaluate(el => { const copy = el.cloneNode(true) as HTMLElement; copy.querySelector('.rn-stamp')?.remove(); return copy.textContent })).toBe(`PAIMOS AEON was updated to ${newest.codename}`)
  const counts = toast.locator('.toast-counts')
  await expect(counts.locator('.toast-count')).toHaveText(['2 new', '1 fix'])
  await expect(counts.locator('.toast-count svg')).toHaveCount(2)
  await expect(counts.locator('.sr-only')).toHaveText('2 new features, 1 fix')
  await expect(toast.getByRole('button', { name: 'What’s new' })).toBeVisible()
  await expect(toast.getByRole('button', { name: 'Reload' })).toBeVisible()
  // The hidden stamp adds no width; the count row starts 20px after the name.
  const fit = await toast.evaluate(el => {
    const box = (s: string) => el.querySelector(s)!.getBoundingClientRect()
    return { cell: box('.release-name').width, name: box('.rn-name').width, gap: box('.toast-counts').left - box('.rn-name').right }
  })
  expect(Math.abs(fit.cell - fit.name)).toBeLessThanOrEqual(0.5)
  expect(Math.abs(fit.gap - 20)).toBeLessThanOrEqual(0.5)
  // Revealing the calendar version on hover moves nothing.
  const controls = () => toast.evaluate(el => [...el.querySelectorAll('.toast-counts, .toast-count, .toast-action, .toast-close, .release-name')].map(node => {
    const r = node.getBoundingClientRect()
    return [r.left, r.top, r.width, r.height]
  }))
  const before = await controls()
  expect(before.length).toBe(7)
  expect(before.every(([, , w, h]) => w > 0 && h > 0)).toBe(true)
  await toast.locator('.release-name').hover()
  await expect(toast.locator('.rn-stamp')).toHaveCSS('opacity', '1')
  const after = await controls()
  after.forEach((box, i) => box.forEach((v, j) => expect(Math.abs(v - before[i][j])).toBeLessThanOrEqual(0.5)))
  await expect(toast.locator('.release-name')).toHaveAccessibleDescription(versionLabel(history.current, false))
})

test.describe('update toast counts', () => {
  for (const width of [390, 1024, 1440]) {
    for (const theme of ['light', 'dark'] as const) {
      test(`toast ${width} ${theme}`, async ({ page }, testInfo) => {
        await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
        await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
        const { history, older } = countedHistory()
        const toast = await showUpdateToast(page, history, older)
        await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
        await expect(toast.locator('.toast-count')).toHaveText(['2 new', '1 fix'])
        const geo = await toast.evaluate(el => {
          const edges = (r: DOMRect) => ({ left: r.left, right: r.right, top: r.top, bottom: r.bottom })
          const box = (s: string) => edges(el.querySelector(s)!.getBoundingClientRect())
          return {
            toast: edges(el.getBoundingClientRect()), sentence: box(':scope > span'), name: box('.rn-name'), counts: box('.toast-counts'),
            countItems: [...el.querySelectorAll('.toast-count')].map(node => edges(node.getBoundingClientRect())),
            action: box('.toast-action'), scroll: document.documentElement.scrollWidth - document.documentElement.clientWidth,
          }
        })
        expect(geo.scroll).toBeLessThanOrEqual(1)
        expect(geo.toast.left).toBeGreaterThanOrEqual(0)
        expect(geo.toast.right).toBeLessThanOrEqual(width)
        if (width === 390) {
          // Phones: the counts wrap under the sentence, the actions under the counts.
          expect(geo.counts.top).toBeGreaterThanOrEqual(geo.sentence.bottom - 0.5)
          expect(Math.abs(geo.counts.left - geo.sentence.left)).toBeLessThanOrEqual(0.5)
          expect(geo.action.top).toBeGreaterThanOrEqual(geo.counts.bottom - 0.5)
          expect(geo.counts.right).toBeLessThanOrEqual(geo.toast.right)
        } else {
          // Wider screens: one line, the counts between the sentence and the actions.
          expect(Math.abs(geo.countItems[0].left - geo.name.right - 20)).toBeLessThanOrEqual(0.5)
          geo.countItems.slice(1).forEach((item, i) => expect(Math.abs(item.left - geo.countItems[i].right - 12)).toBeLessThanOrEqual(0.5))
          expect(geo.action.left).toBeGreaterThan(geo.counts.right)
          expect(Math.abs((geo.counts.top + geo.counts.bottom) / 2 - (geo.sentence.top + geo.sentence.bottom) / 2)).toBeLessThanOrEqual(2)
        }
        const guard = await controlStability(page, {
          releaseName: toast.locator('.release-name'), counts: toast.locator('.toast-counts'),
          newCount: toast.locator('.toast-count[data-kind="features"]'), fixCount: toast.locator('.toast-count[data-kind="fixes"]'),
          whatsNew: toast.getByRole('button', { name: 'What’s new' }), reload: toast.getByRole('button', { name: 'Reload' }),
          dismiss: toast.getByRole('button', { name: 'Dismiss' }),
        })
        await guard.check(async () => {
          await toast.locator('.release-name').hover()
          await expect(toast.locator('.rn-stamp')).toHaveCSS('opacity', '1')
        })
        await guard.check(async () => {
          await page.mouse.move(2, 2)
          await expect(toast.locator('.rn-stamp')).toHaveCSS('opacity', '0')
        })
        guard.done()
        await page.screenshot({ path: testInfo.outputPath(`update-toast-${width}-${theme}.png`), animations: 'disabled' })
        await toast.screenshot({ path: testInfo.outputPath(`update-toast-${width}-${theme}-toast.png`), animations: 'disabled' })
      })
    }
  }
})
