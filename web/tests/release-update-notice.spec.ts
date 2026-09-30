// SPDX-License-Identifier: AGPL-3.0-only
// AEON-325: after a deploy the release history shows one notice. An outdated
// page says a newer version is live, including when that version is absent
// from this build's history. A current page still names a version the history
// does not have. Screenshots: AEON_325_SHOTS=<dir>.
import { mkdirSync } from 'node:fs'
import { expect, test, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { mockReleases, releaseHistory, type History } from './releases-fixtures'

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
const notices = (page: Page) => sheet(page).locator('.notice')

async function openHistory(page: Page, history: History, running: string, target: string) {
  await mockWork(page, fixtures())
  await mockReleases(page, history, { running })
  await page.goto(target === 'all' ? '/releases' : `/releases/${target}`)
  await expect(sheet(page)).toBeVisible()
  await expect(sheet(page).getByRole('listbox', { name: 'Releases, newest first' })).toBeVisible()
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
  const notice = notices(page)
  await expect(notice).toContainText('A newer release is live')
  await expect(notice).toContainText('this page still runs')
  await expect(notice).toContainText('the server runs')
  await expect(notice.locator('.calendar-version').nth(0)).toHaveAttribute('aria-label', versionLabel(pageVersion, false))
  await expect(notice.locator('.calendar-version').nth(1)).toHaveAttribute('aria-label', versionLabel(SERVER))
  await expect(notice.getByRole('button', { name: 'Reload' })).toBeVisible()
  await expect(sheet(page).getByText(/not in this build.s release history/)).toHaveCount(0)
  await expect(sheet(page).locator('.running .calendar-version')).toHaveAttribute('aria-label', versionLabel(pageVersion, false))
  await expect(sheet(page).getByRole('option').first()).toHaveAttribute('aria-selected', 'true')
})

test('an outdated page opened on the history still shows only the newer-version notice', async ({ page }) => {
  const { history, pageVersion } = outdatedHistory()
  await openHistory(page, history, pageVersion, 'all')
  await expect(notices(page)).toHaveCount(1)
  await expect(notices(page)).toContainText('A newer release is live')
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
  await expect(notices(page)).toContainText('A newer release is live')
  await expect(notices(page)).toContainText('this page still runs')
  await expect(notices(page).locator('.calendar-version').nth(0)).toHaveAttribute('aria-label', versionLabel(pageVersion, false))
  await expect(notices(page).locator('.calendar-version').nth(1)).toHaveAttribute('aria-label', versionLabel(SERVER))
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
    await expect(notices(page)).toContainText('A newer release is live')
    await expect(notices(page).locator('.calendar-version').nth(0)).toHaveAttribute('aria-label', versionLabel(pageVersion, false))
    await expect(notices(page).locator('.calendar-version').nth(1)).toHaveAttribute('aria-label', versionLabel(SERVER))
    await expect(sheet(page).getByText(/not in this build.s release history/)).toHaveCount(0)
    await expect(sheet(page).locator('.running .calendar-version')).toHaveAttribute('aria-label', versionLabel(pageVersion, false))
    const during = await page.evaluate(() => (window as unknown as { __notices?: string[] }).__notices ?? [])
    expect(during.join('\n')).not.toMatch(/not in this build/)
  } finally {
    releaseRefetch()
  }
  await expect.poll(() => opened).toBe(true)
  await expect(sheet(page).getByRole('listbox', { name: 'Releases, newest first' })).toBeVisible()
  await expect(notices(page)).toHaveCount(1)
  await expect(notices(page)).toContainText('A newer release is live')
  await expect(notices(page).locator('.calendar-version').nth(1)).toHaveAttribute('aria-label', versionLabel(SERVER))
  await expect(sheet(page).getByText(/not in this build.s release history/)).toHaveCount(0)
  await expect(sheet(page).locator('.running .calendar-version')).toHaveAttribute('aria-label', versionLabel(pageVersion, false))
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
          await expect(notices(page)).toContainText('A newer release is live')
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
  expect(await toast.locator('> span').evaluate(el => el.textContent)).not.toContain(SERVER)
})
