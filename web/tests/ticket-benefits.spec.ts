// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'

test('ticket benefits edit together with a precondition and preserve unrelated fields', async ({ page }) => {
  const errors = watchErrors(page)
  const data = fixtures()
  const target = data.nodes.find(n => n.key === 'PHAROS-12')!
  const prior = { ...target.fields }
  const calls = await mockWork(page, data)
  await page.goto('/p/PHAROS/PHAROS-12')
  const ws = page.getByRole('complementary', { name: 'Ticket details' })
  await expect(ws.getByRole('region', { name: 'User benefit' })).toContainText('Before Done')
  await ws.getByRole('button', { name: 'Edit', exact: true }).click()
  await ws.getByLabel('Pill · English').fill('Clear release notes')
  await ws.getByLabel('Pill · Deutsch').fill('Verständliche Release Notes')
  await ws.getByLabel('Benefit · English').fill('Tickets explain what you gain.')
  await ws.getByLabel('Benefit · Deutsch').fill('Tickets erklären den Nutzen.')
  await ws.getByLabel('Hide from release notes').check()
  await ws.getByRole('button', { name: 'Save', exact: true }).click()
  const benefits = ws.getByRole('region', { name: 'User benefit' })
  await expect(benefits).toContainText('Tickets erklären den Nutzen.')
  await expect(benefits).toContainText('Hidden from release notes')
  await expect(benefits).not.toContainText('Before Done')
  const write = calls.find(c => c.method === 'PATCH' && c.path === `/api/nodes/${target.id}`)!
  expect(write.headers['if-unmodified-since']).toBeTruthy()
  expect(write.body).toEqual({ fields: { ...prior, pill_en: 'Clear release notes', pill_de: 'Verständliche Release Notes', benefit_en: 'Tickets explain what you gain.', benefit_de: 'Tickets erklären den Nutzen.', hide_from_release_notes: true } })
  expect(errors).toEqual([])
})

test('read-only ticket shows missing-language guidance without edit controls', async ({ page }) => {
  const data = fixtures()
  await mockWork(page, data, { readOnly: true })
  await page.goto('/p/PHAROS/PHAROS-12')
  const ws = page.getByRole('complementary', { name: 'Ticket details' })
  await expect(ws.getByRole('region', { name: 'User benefit' })).toContainText('both languages')
  await expect(ws.getByRole('button', { name: 'Edit', exact: true })).toHaveCount(0)
})

for (const state of ['done', 'accepted', 'delivered']) {
  test(`historical ${state} ticket shows completed guidance in display and edit mode`, async ({ page }) => {
    const data = fixtures()
    data.nodes.find(n => n.key === 'PHAROS-12')!.state = state
    await mockWork(page, data)
    await page.goto('/p/PHAROS/PHAROS-12')
    const ws = page.getByRole('complementary', { name: 'Ticket details' })
    const benefits = ws.getByRole('region', { name: 'User benefit' })
    await expect(benefits).toContainText('This completed ticket has incomplete benefit fields.')
    await expect(benefits).not.toContainText('Before Done')
    await ws.getByRole('button', { name: 'Edit', exact: true }).click()
    await expect(benefits).toContainText('This completed ticket has incomplete benefit fields.')
    await expect(benefits).not.toContainText('Before Done')
  })
}

for (const regenerated of [false, true]) {
  test(`${regenerated ? 'regenerated missing-snapshot' : 'v1'} history keeps chips, filters and visible changes without a tag title`, async ({ page }) => {
    const { mockReleases, releaseHistory } = await import('./releases-fixtures')
    const history = releaseHistory()
    const current = history.releases[0]!
    const gap = 'Release membership and bilingual ticket fields were not captured. Git mentions do not establish release membership.'
    if (regenerated) {
      for (const release of history.releases) {
        Object.assign(release, { notes: { source: 'unavailable', snapshot_sha256: '', captured_at: null, release_revision: 0, hidden: 0, items: [], gaps: [gap] } })
      }
    }
    await mockWork(page, fixtures())
    await mockReleases(page, history)
    await page.goto(`/releases/${current.version}`)
    const detail = page.locator('article.detail')
    const options = page.getByRole('listbox', { name: 'Releases, newest first' }).getByRole('option')
    // AEON-305: the tag message is evidence, never a labelled title.
    await expect(options.first()).not.toContainText('Time entry editing')
    await expect(options.first()).toContainText('AEON-75')
    await expect(detail.locator('.headline')).toHaveCount(0)
    await expect(page.getByText('Historical tag headline')).toHaveCount(0)
    await expect(detail.getByText('Notes written after release')).toHaveCount(0)
    await expect(detail.locator('.tickets')).toContainText('AEON-75')
    await expect(detail.locator('.notes')).toContainText('Correct and delete time entries in the Hours view')
    await expect(detail.locator('#release-evidence')).toHaveCount(0)
    // No capture: no header and no note lines, only the changes.
    await expect(detail.locator('.summary')).toHaveCount(0)
    await expect(detail.getByText('Internal changes only.')).toHaveCount(0)
    await expect(detail.getByText(gap, { exact: true })).toHaveCount(0)
    await page.getByRole('group', { name: 'Show only releases with' }).getByRole('button', { name: 'Tickets', exact: true }).click()
    await expect(options).toHaveCount(5)
    await page.getByRole('searchbox', { name: 'Search releases' }).fill('AEON-75')
    await expect(options).toHaveCount(1)
    await expect(options.first()).toContainText('AEON-75')
    await page.getByRole('searchbox', { name: 'Search releases' }).fill('')
    await page.getByRole('group', { name: 'Show only releases with' }).getByRole('button', { name: 'Tickets', exact: true }).click()
    await options.last().click()
    await expect(detail.locator('.headline')).toHaveCount(0)
    await expect(detail.locator('.notes')).toContainText('Projects, tickets and sign-in')
    await expect(detail.getByText(gap, { exact: true })).toHaveCount(0)
  })
}

const benefitNotes = () => ({ source: 'tag:release-notes/synthetic.json', snapshot_sha256: 'a'.repeat(64), captured_at: '2026-09-28T12:00:00Z', release_revision: 2, hidden: 1, gaps: ['AEON-99: benefit_de is required'], items: [{ id: 'note-one', key: 'AEON-75', pill_en: 'Clear release notes', pill_de: 'Verständliche Release Notes', benefit_en: 'Tickets explain what you gain.', benefit_de: 'Tickets erklären den Nutzen.' }] })

async function prepareNotedRelease(page: import('@playwright/test').Page, notes = benefitNotes()) {
  const { mockReleases, releaseHistory } = await import('./releases-fixtures')
  const history = releaseHistory()
  const current = history.releases[0]!
  Object.assign(current, { notes })
  await mockWork(page, fixtures())
  await mockReleases(page, history)
  return { history, current }
}
async function openNotedRelease(page: import('@playwright/test').Page, notes = benefitNotes()) {
  const opened = await prepareNotedRelease(page, notes)
  await page.goto(`/releases/${opened.current.version}`)
  return opened
}

test('release notes use benefit text and do not fall back to the Git headline', async ({ page }) => {
  const { current } = await openNotedRelease(page)
  const notes = page.getByRole('region', { name: 'Release notes' })
  const detail = page.locator('article.detail')
  await expect(detail.locator('.headline')).toHaveCount(0)
  await expect(detail.getByText('Historical tag headline')).toHaveCount(0)
  // AEON-305: the captured ticket is a block like any other, with its commits folded.
  await expect(page.getByRole('listbox', { name: 'Releases, newest first' }).getByRole('option').first()).toContainText('Clear release notes')
  const block = notes.getByRole('region', { name: 'Features, 1' }).getByRole('article', { name: 'Clear release notes' })
  await expect(notes.getByRole('article')).toHaveCount(1)
  await expect(block.locator('.benefit')).toHaveText('Tickets explain what you gain.')
  await expect(block.locator('.line-head').getByText('AEON-75', { exact: true })).toBeVisible()
  await expect(block.locator('summary')).toHaveText('3 commits')
  await expect(notes.getByRole('region', { name: /^Other changes,/ })).toHaveCount(0)
  await expect(notes).toContainText('benefit_de is required')
  await expect(notes).toContainText('One ticket is hidden from release notes.')
  await expect(notes).not.toContainText(current.headline)
  await expect(notes.getByText('Notes written after release')).toHaveCount(0)
  await expect(notes.getByRole('button', { name: 'Deutsch', exact: true })).toHaveCount(0)
})

test('a German language setting shows the German pill and sentence, and an empty German field falls back to English', async ({ page }) => {
  const profile = { principal_id: '11111111-1111-4111-8111-111111111111', email: 'markus@barta.com', first_name: 'Markus', last_name: 'Barta', preferred_name: '', short_name: 'mba', initials: 'MB', timezone: 'Europe/Vienna', locale: 'de-AT', greeting_enabled: false, avatar_color: 'teal', avatar_hashes: {}, week_start: 1, revision: 1 }
  const show = async (notes = benefitNotes()) => {
    const opened = await prepareNotedRelease(page, notes)
    // Registered after the work mock so this route, not the mock's 404, answers.
    await page.route('**/api/me/profile', route => route.fulfill({ json: profile }))
    await page.goto(`/releases/${opened.current.version}`)
  }
  await show()
  const notes = page.getByRole('region', { name: 'Release notes' })
  await expect(notes.locator('.pill-title')).toHaveText('Verständliche Release Notes')
  await expect(notes.locator('.benefit')).toHaveText('Tickets erklären den Nutzen.')
  await expect(notes).toContainText('Ein Ticket ist in den Release Notes ausgeblendet.')
  profile.locale = 'de-DE'
  const partial = benefitNotes()
  partial.items[0]!.pill_de = ''
  partial.items[0]!.benefit_de = ' '
  await show(partial)
  // The English text carries one small badge on the title (AEON-323).
  await expect(notes.locator('.pill-title')).toHaveText('Clear release notesEN')
  await expect(notes.locator('.pill-title .lang-badge')).toHaveText('EN')
  await expect(notes.locator('.benefit')).toHaveText('Tickets explain what you gain.')
})

for (const width of [1600, 390]) {
  test(`the release sheet shows one pill and one sentence at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await openNotedRelease(page)
    const notes = page.getByRole('region', { name: 'Release notes' })
    const sheet = page.getByRole('dialog', { name: 'PAIMOS AEON releases' })
    await expect(notes.locator('.pill-title')).toHaveText('Clear release notes')
    await expect(notes.locator('.benefit')).toHaveText('Tickets explain what you gain.')
    await expect(notes.locator('.pill-title')).toBeInViewport({ ratio: 1 })
    const overflow = await sheet.evaluate(el => el.scrollWidth > el.clientWidth + 1)
    expect(overflow).toBeFalsy()
    await page.evaluate(() => { document.documentElement.dataset.theme = 'dark' })
    await expect(notes.locator('.benefit')).toBeVisible()
    if (process.env.SHOTS) {
      await page.screenshot({ path: `${process.env.SHOTS}/${width}-dark.png` })
      await page.evaluate(() => { document.documentElement.dataset.theme = 'light' })
      await page.screenshot({ path: `${process.env.SHOTS}/${width}-light.png` })
    }
  })
}

for (const width of [1600, 390]) {
  test(`backfilled notes say they were written after the release at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    const notesBody = benefitNotes()
    Object.assign(notesBody, { written_after_release: true })
    await openNotedRelease(page, notesBody)
    const sheet = page.getByRole('dialog', { name: 'PAIMOS AEON releases' })
    const detail = page.locator('article.detail')
    const hint = detail.getByRole('region', { name: 'Release notes' }).getByText('Notes written after release', { exact: true })
    await expect(hint).toBeVisible()
    await expect(hint).toBeInViewport({ ratio: 1 })
    await expect(detail.getByText('Historical tag headline')).toHaveCount(0)
    const detailStyle = await hint.evaluate(el => {
      const s = getComputedStyle(el)
      return { size: s.fontSize, transform: s.textTransform }
    })
    expect(detailStyle).toEqual({ size: '12.5px', transform: 'none' })
    // AEON-305: the list row stays version, date and theme; the hint lives in the detail.
    if (width === 1600) await expect(page.getByRole('listbox', { name: 'Releases, newest first' }).getByRole('option').first().getByText('Notes written after release', { exact: true })).toHaveCount(0)
    const overflow = await sheet.evaluate(el => el.scrollWidth > el.clientWidth + 1)
    expect(overflow).toBeFalsy()
    await page.evaluate(() => { document.documentElement.dataset.theme = 'dark' })
    await expect(hint).toBeVisible()
    if (process.env.SHOTS) {
      await page.screenshot({ path: `${process.env.SHOTS}/backfill-${width}-dark.png` })
      await page.evaluate(() => { document.documentElement.dataset.theme = 'light' })
      await page.screenshot({ path: `${process.env.SHOTS}/backfill-${width}-light.png` })
    }
  })
}

for (const width of [1600, 390]) {
  test(`internal-only releases show no tag title and a quiet detail at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    // Cover both a hidden-only capture and an empty internal capture.
    for (const hidden of [2, 0]) {
      const { current } = await openNotedRelease(page, { ...benefitNotes(), hidden, items: [], gaps: [] })
      const sheet = page.getByRole('dialog', { name: 'PAIMOS AEON releases' })
      const row = page.getByRole('listbox', { name: 'Releases, newest first' }).getByRole('option').first()
      const notes = page.getByRole('region', { name: 'Release notes' })
      await expect(notes.getByText('Internal changes only.', { exact: true })).toBeVisible()
      await expect(notes.getByRole('article')).toHaveCount(0)
      await expect(sheet).not.toContainText('No public release notes')
      await expect(notes).not.toContainText('is required')
      if (width === 390) await sheet.getByRole('button', { name: 'All releases' }).click()
      // An empty capture names no benefits, so the row carries no headline line at all: the codename is the row title (AEON-430), never the Git headline.
      await expect(row.locator('.headline')).toHaveCount(0)
      await expect(row.locator('.row-name .rn-name')).toHaveText(current.codename!)
      await expect(row.getByText('Historical tag headline')).toHaveCount(0)
      if (process.env.SHOTS && hidden && width === 390) {
        for (const theme of ['light', 'dark']) {
          await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
          await page.screenshot({ path: `${process.env.SHOTS}/internal-list-${width}-${theme}.png` })
        }
      }
      if (width === 390) await row.click()
      for (const theme of ['light', 'dark']) {
        await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
        await expect(notes.getByText('Internal changes only.', { exact: true })).toBeVisible()
        expect(await sheet.evaluate(el => el.scrollWidth > el.clientWidth + 1)).toBeFalsy()
        if (process.env.SHOTS && hidden) {
          await page.screenshot({ path: `${process.env.SHOTS}/internal-${width}-${theme}.png` })
        }
      }
    }
  })
}
