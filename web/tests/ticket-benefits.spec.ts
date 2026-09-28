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
  test(`${regenerated ? 'regenerated missing-snapshot' : 'v1'} history keeps headlines, chips, filters and visible changes`, async ({ page }) => {
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
    await expect(options.first()).toContainText('Time entry editing')
    await expect(options.first()).toContainText('AEON-75')
    await expect(detail.locator('.headline')).toHaveText('Time entry editing')
    await expect(detail.getByText('Historical tag headline')).toBeVisible()
    await expect(detail.locator('.tickets')).toContainText('AEON-75')
    await expect(detail.locator('.changes-block')).toContainText('Correct and delete time entries in the Hours view')
    await expect(detail.locator('#release-evidence')).toHaveCount(0)
    await expect(detail.getByRole('region', { name: 'Release notes' })).toHaveCount(0)
    await expect(detail.getByText(gap, { exact: true })).toHaveCount(regenerated ? 1 : 0)
    await page.getByRole('group', { name: 'Show only releases with' }).getByRole('button', { name: 'Tickets', exact: true }).click()
    await expect(options).toHaveCount(5)
    await page.getByRole('searchbox', { name: 'Search releases' }).fill('AEON-75')
    await expect(options).toHaveCount(1)
    await expect(options.first()).toContainText('Time entry editing')
    await page.getByRole('searchbox', { name: 'Search releases' }).fill('')
    await page.getByRole('group', { name: 'Show only releases with' }).getByRole('button', { name: 'Tickets', exact: true }).click()
    await options.last().click()
    await expect(detail.locator('.headline')).toHaveText('First release')
    await expect(detail.getByText('Historical tag headline')).toBeVisible()
    await expect(detail.locator('.changes-block')).toContainText('Projects, tickets and sign-in')
    await expect(detail.getByText(gap, { exact: true })).toHaveCount(regenerated ? 1 : 0)
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
  await expect(detail.locator('.changes')).toHaveCount(0)
  await expect(page.getByRole('listbox', { name: 'Releases, newest first' }).getByRole('option').first()).toContainText('Clear release notes')
  await expect(notes.locator('.note-item')).toHaveCount(1)
  await expect(notes.locator('.note-pill')).toHaveText('Clear release notes')
  await expect(notes.locator('.note-benefit')).toHaveText('Tickets explain what you gain.')
  await expect(notes).toContainText('benefit_de is required')
  await expect(notes).toContainText('One ticket is hidden from release notes.')
  await expect(notes).not.toContainText(current.headline)
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
  await expect(notes.locator('.note-pill')).toHaveText('Verständliche Release Notes')
  await expect(notes.locator('.note-benefit')).toHaveText('Tickets erklären den Nutzen.')
  await expect(notes).toContainText('Ein Ticket ist in den Release Notes ausgeblendet.')
  profile.locale = 'de-DE'
  const partial = benefitNotes()
  partial.items[0]!.pill_de = ''
  partial.items[0]!.benefit_de = ' '
  await show(partial)
  await expect(notes.locator('.note-pill')).toHaveText('Clear release notes')
  await expect(notes.locator('.note-benefit')).toHaveText('Tickets explain what you gain.')
})

for (const width of [1600, 390]) {
  test(`the release sheet shows one pill and one sentence at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await openNotedRelease(page)
    const notes = page.getByRole('region', { name: 'Release notes' })
    const sheet = page.getByRole('dialog', { name: 'PAIMOS AEON releases' })
    await expect(notes.locator('.note-pill')).toHaveText('Clear release notes')
    await expect(notes.locator('.note-benefit')).toHaveText('Tickets explain what you gain.')
    await expect(notes.locator('.note-pill')).toBeInViewport({ ratio: 1 })
    const overflow = await sheet.evaluate(el => el.scrollWidth > el.clientWidth + 1)
    expect(overflow).toBeFalsy()
    await page.evaluate(() => { document.documentElement.dataset.theme = 'dark' })
    await expect(notes.locator('.note-benefit')).toBeVisible()
    if (process.env.SHOTS) {
      await page.screenshot({ path: `${process.env.SHOTS}/${width}-dark.png` })
      await page.evaluate(() => { document.documentElement.dataset.theme = 'light' })
      await page.screenshot({ path: `${process.env.SHOTS}/${width}-light.png` })
    }
  })
}
