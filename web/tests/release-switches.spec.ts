// SPDX-License-Identifier: AGPL-3.0-only
// AEON-323: EN | DE and Highlights | Details in the release-history header.
// Highlights is today's view: benefits, commits folded, Other kept. Details
// lists each ticket's commits open, shows the evidence and hides the benefits.
// Both choices live in the address (release_lang, release_view); the language is
// also remembered per person. A missing translation shows the other language
// with a badge. Search follows what the view shows. 102 (backfilled) and 105
// (current) share the renderer.
import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { fixtures, mockWork } from './work-fixtures'
import { mockReleases, type History } from './releases-fixtures'
import { historicHistory, NOTES } from './releases-historic-fixtures'
import { expectStableControls } from './helpers/stable'

const SHOTS = process.env.AEON_323_SHOTS
const V102 = '260929082208.0.0'
const V105 = '260929113854.0.0'
const esc = (v: string) => v.replaceAll('.', '\\.')

const sheet = (page: Page) => page.getByRole('dialog', { name: 'PAIMOS AEON releases' })
const detail = (page: Page) => sheet(page).locator('article.detail')
const block = (page: Page, name: string) => detail(page).getByRole('article', { name })
const rows = (page: Page) => sheet(page).getByRole('grid', { name: 'Releases, newest first' }).getByRole('row')
const radio = (page: Page, name: 'EN' | 'DE' | 'Highlights' | 'Details') => sheet(page).getByRole('radio', { name, exact: true })
const search = (page: Page) => sheet(page).getByRole('searchbox', { name: 'Search releases' })

function withTickets() {
  const data = fixtures()
  const at = '2026-09-29T06:00:00Z'
  for (const key of Object.keys(NOTES)) {
    data.nodes.push({ id: `n-${key}`, key, kind_slug: 'ticket', title: NOTES[key]!.pill_en, body: '', state: 'done', project: 'p-aeon', fields: { priority: 'high' }, parent_id: 'p-aeon', created_at: at, updated_at: at })
  }
  return data
}

type Linked = { key: string; pill_en: string; pill_de: string; benefit_en: string; benefit_de: string }
type Rel = History['releases'][number] & { presentation?: Record<string, unknown>; notes?: { items: Linked[] } }
// 105 gets a CI run; the fallback variant leaves some texts untranslated; the
// empty variant adds two older releases without any changes.
const V101 = '260929020000.0.0'
const V100 = '260929010000.0.0'
function history(fallback = false, empty = false) {
  const h = structuredClone(historicHistory()) as History
  const [r105, r102] = h.releases as Rel[]
  r105!.headline = 'stable105'
  r102!.headline = 'evidence-only tag message'
  r105!.evidence.ci = { name: 'ci', url: 'https://github.com/inspr-at/aeon/actions/runs/105', status: 'completed', conclusion: 'success' }
  if (empty) {
    for (const [version, sequence, at] of [[V101, 101, '2026-09-29T02:00:00Z'], [V100, 100, '2026-09-29T01:00:00Z']] as const) {
      const { notes: _notes, presentation: _presentation, ...rest } = structuredClone(r102!)
      h.releases.push({ ...rest, version, tag: `v${version}`, release_sequence: sequence, reserved_at: at, tagged_at: at, published_at: at, headline: '', tickets: [], changes: [] } as Rel)
    }
  }
  if (fallback) {
    for (const c of r105!.changes) {
      for (const t of (c as { linked_tickets?: Linked[] }).linked_tickets ?? []) {
        if (t.key === 'AEON-291') Object.assign(t, { pill_de: '', benefit_de: '' })
        if (t.key === 'AEON-251') Object.assign(t, { benefit_de: '' })
      }
    }
    for (const item of r102!.notes!.items) if (item.key === 'AEON-293') Object.assign(item, { pill_en: '', benefit_en: '' })
    r102!.presentation = { theme_en: 'Deploy with eyes open', theme_de: 'Deploy mit offenen Augen', headline_en: 'Every approval names its server.', headline_de: '', intro_en: '', intro_de: '', revision: 1, updated_at: '2026-09-29T12:00:00Z' }
  }
  return h
}

const ME = '11111111-1111-4111-8111-111111111111'
type Options = { locale?: string; fallback?: boolean; empty?: boolean }
async function open(page: Page, path: string, options: Options = {}) {
  await mockWork(page, withTickets())
  await mockReleases(page, history(options.fallback, options.empty))
  if (options.locale) {
    const profile = { principal_id: ME, email: 'markus@barta.com', first_name: 'Markus', last_name: 'Barta', preferred_name: '', short_name: 'mba', initials: 'MB', timezone: 'Europe/Vienna', locale: options.locale, greeting_enabled: false, avatar_color: 'teal', avatar_hashes: {}, week_start: 1, revision: 1 }
    await page.route('**/api/me/profile', route => route.fulfill({ json: profile }))
  }
  await page.goto(path)
  await expect(sheet(page)).toBeVisible()
}
async function openRelease(page: Page, version = V105, query = '', options: Options = {}) {
  await open(page, `/releases/${version}${query}`, options)
  await expect(detail(page).getByRole('heading', { level: 2 })).toBeVisible()
}

const noHorizontalScroll = (page: Page) => page.evaluate(() => {
  const wide = (el: Element | null) => !!el && el.scrollWidth > el.clientWidth + 1
  return !(wide(document.querySelector('dialog.releases')) || wide(document.documentElement))
})

test('Highlights is today\'s view and Details lists the commits open with the evidence, on 105 and 102', async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 1000 })
  await openRelease(page, V105)
  const dead = block(page, 'Dead sessions tidy up')
  // Highlights: the benefit, the commits folded, the evidence behind its toggle.
  await expect(radio(page, 'Highlights')).toHaveAttribute('aria-checked', 'true')
  await expect(dead.locator('.benefit')).toHaveText('Sessions whose agent is gone end on their own, and the session menu offers only what works.')
  await expect(dead.locator('details.commits summary')).toHaveText('10 commits')
  await expect(dead.locator('details.commits')).not.toHaveAttribute('open', '')
  await expect(sheet(page).getByRole('button', { name: /Evidence/ })).toHaveAttribute('aria-pressed', 'false')
  await expect(detail(page).getByRole('region', { name: 'Evidence' })).toHaveCount(0)
  await expect(rows(page).first().locator('.subjects')).toHaveCount(0)

  // Details: no benefit, every commit open with subject, short SHA and link; the evidence open.
  await radio(page, 'Details').click()
  await expect(page).toHaveURL(new RegExp(`/releases/${esc(V105)}\\?release_view=details$`))
  await expect(dead.locator('.benefit')).toHaveCount(0)
  await expect(detail(page).locator('summary')).toHaveCount(0)
  const commits = dead.getByRole('list', { name: '10 commits' }).getByRole('listitem')
  await expect(commits).toHaveCount(10)
  await expect(commits.first().locator('.subject')).toBeVisible()
  const sha = commits.first().getByRole('link', { name: /^Commit [0-9a-f]{7} on GitHub$/ })
  await expect(sha).toHaveText(/^[0-9a-f]{7}$/)
  await expect(sha).toHaveAttribute('href', /^https:\/\/github\.com\/inspr-at\/aeon\/commit\/[0-9a-f]{40}$/)
  const evidence = detail(page).getByRole('region', { name: 'Evidence' })
  await expect(evidence.getByRole('button', { name: /^Copy / })).toHaveCount(3)
  for (const label of ['Version', 'Source commit', 'CI run', 'Image', 'OCI digest', 'GitHub release']) await expect(evidence.getByText(label, { exact: true })).toBeVisible()
  await expect(evidence.getByRole('link', { name: /passed/ })).toHaveAttribute('href', /actions\/runs\/105$/)
  await expect(detail(page).getByRole('region', { name: 'Evidence' })).toContainText('Tag message: stable105')

  // The historic release reads the same, and its Other group stays in both views.
  await rows(page).nth(1).click()
  await expect(page).toHaveURL(new RegExp(`/releases/${esc(V102)}\\?release_view=details$`))
  const deploy = block(page, 'Deploy target on screen')
  await expect(deploy.getByRole('list', { name: '4 commits' }).getByText('Preserve strict handoff bytes with optional deployment targets')).toBeVisible()
  await expect(deploy.getByRole('link', { name: 'Commit e9c460b on GitHub' })).toHaveAttribute('href', 'https://github.com/inspr-at/aeon/commit/e9c460bfea447621ea59142af57ab53be0032578')
  await expect(detail(page).getByRole('region', { name: /^Other\b/ })).toBeVisible()
  await expect(detail(page).getByRole('region', { name: 'Evidence' }).getByText('Tag message: evidence-only tag message')).toBeVisible()
  await radio(page, 'Highlights').click()
  await expect(deploy.locator('.benefit')).toContainText('nothing is approved blind')
  await expect(deploy.locator('details.commits summary')).toHaveText('4 commits')
  await expect(detail(page).getByRole('region', { name: /^Other\b/ })).toBeVisible()
  await expect(detail(page).getByText('Tag message: evidence-only tag message')).toHaveCount(0)
})

test('EN and DE switch every text, round-trip in the address with the view, and keep ?view= of the page behind', async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 1000 })
  await openRelease(page, V102)
  await radio(page, 'DE').click()
  await radio(page, 'Details').click()
  await expect(page).toHaveURL(new RegExp(`/releases/${esc(V102)}\\?release_lang=de&release_view=details$`))
  // The labels read the same in both languages.
  for (const name of ['EN', 'DE', 'Highlights', 'Details'] as const) await expect(radio(page, name)).toHaveText(name)
  await expect(block(page, 'Deploy-Ziel im Blick')).toBeVisible()
  await expect(block(page, 'Deploy-Ziel im Blick')).toHaveAttribute('aria-labelledby', /change-features-AEON-211/)
  await expect(detail(page).getByText('Notizen nach dem Release geschrieben')).toBeVisible()
  await expect(detail(page).getByText('Ein Ticket ist in den Release Notes ausgeblendet.')).toBeVisible()
  await expect(rows(page).nth(1).locator('.headline')).toContainText('Deploy-Ziel im Blick')

  // A link opens the same view; the version changes and both choices stay.
  await page.reload()
  await expect(radio(page, 'DE')).toHaveAttribute('aria-checked', 'true')
  await expect(radio(page, 'Details')).toHaveAttribute('aria-checked', 'true')
  await rows(page).first().click()
  await expect(page).toHaveURL(new RegExp(`/releases/${esc(V105)}\\?release_lang=de&release_view=details$`))
  await expect(block(page, 'Tote Sitzungen räumen auf').getByRole('list', { name: '10 commits' })).toBeVisible()
  // Two choices in the same moment both land.
  await sheet(page).evaluate(dialog => {
    for (const name of ['EN', 'Highlights']) [...dialog.querySelectorAll<HTMLButtonElement>('[role="radio"]')].find(b => b.textContent?.trim() === name)!.click()
  })
  await expect(page).toHaveURL(new RegExp(`/releases/${esc(V105)}\\?release_lang=en&release_view=highlights$`))
  await expect(radio(page, 'EN')).toHaveAttribute('aria-checked', 'true')
  await expect(radio(page, 'Highlights')).toHaveAttribute('aria-checked', 'true')

  // Over a page that has its own ?view=: the sheet's keys never touch it, and closing drops only theirs.
  await page.goto(`/?view=board&releases=${V105}`)
  await expect(sheet(page)).toBeVisible()
  await radio(page, 'Details').click()
  await expect(page).toHaveURL(/[?&]view=board(&|$)/)
  await expect(page).toHaveURL(/release_view=details/)
  await sheet(page).getByRole('button', { name: 'Close release history' }).click()
  await expect(sheet(page)).toHaveCount(0)
  await expect(page).toHaveURL(/[?&]view=board(&|$)/)
  await expect(page).not.toHaveURL(/release_/)
})

test('the language is remembered per person and defaults to the profile', async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 1000 })
  await openRelease(page, V102, '', { locale: 'de-AT' })
  await expect(radio(page, 'DE')).toHaveAttribute('aria-checked', 'true')
  await expect(block(page, 'Lesbarer Risiko-Chip')).toBeVisible()
  await expect(page).not.toHaveURL(/release_lang=/)
  // EN chosen once stays EN on a fresh open without it in the address.
  await radio(page, 'DE').focus()
  await page.keyboard.press('ArrowLeft')
  await expect(radio(page, 'EN')).toHaveAttribute('aria-checked', 'true')
  await expect(radio(page, 'EN')).toBeFocused()
  await page.goto(`/releases/${V102}`)
  await expect(radio(page, 'EN')).toHaveAttribute('aria-checked', 'true')
  await expect(block(page, 'Readable risk chip')).toBeVisible()
  // Another person on this device starts from their own profile.
  await page.route('**/api/me', route => route.fulfill({ json: { principal: { id: '33333333-3333-4333-8333-333333333333', name: 'Ola Nordmann', kind: 'person', roles: ['member'] }, tenant: { id: 't1', name: 'INSPR Studio' } } }))
  await page.goto(`/releases/${V102}`)
  await expect(radio(page, 'DE')).toHaveAttribute('aria-checked', 'true')
})

test('a missing translation shows the other language with a small badge, never blank', async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 1000 })
  await openRelease(page, V105, '?release_lang=de', { fallback: true })
  // No German pill or benefit: the English one, one badge on the title.
  const dead = block(page, 'Dead sessions tidy up')
  await expect(dead.locator('h4')).toHaveAttribute('lang', 'en')
  await expect(dead.locator('.lang-badge')).toHaveCount(1)
  await expect(dead.locator('h4 .lang-badge')).toHaveText('EN')
  await expect(dead.locator('.benefit')).toHaveAttribute('lang', 'en')
  // Only the benefit missing: the badge sits on the benefit.
  const shadow = block(page, 'Regel-Schattenvergleich')
  await expect(shadow.locator('h4 .lang-badge')).toHaveCount(0)
  await expect(shadow.locator('.benefit .lang-badge')).toHaveText('EN')
  await expect(block(page, 'Regel-Schattenvergleich').locator('.benefit')).toHaveAttribute('lang', 'en')
  await expect(dead.locator('h4 .lang-badge')).toHaveAttribute('data-tip', 'Not translated yet, shown in English')

  // English chosen, only German written: the German text with a DE badge. The header's headline falls back alone.
  await rows(page).nth(1).click()
  await radio(page, 'EN').click()
  const chip = block(page, 'Lesbarer Risiko-Chip')
  await expect(chip.locator('h4')).toHaveAttribute('lang', 'de')
  await expect(chip.locator('h4 .lang-badge')).toHaveText('DE')
  await expect(detail(page).locator('.summary .lang-badge')).toHaveCount(0)
  await radio(page, 'DE').click()
  await expect(detail(page).locator('.summary .kicker')).toHaveText('Deploy mit offenen Augen')
  await expect(detail(page).locator('.summary .headline .lang-badge')).toHaveText('EN')
  await expect(detail(page).locator('.summary .lang-badge')).toHaveCount(1)
  // Nothing badged when the text is in the chosen language.
  await expect(block(page, 'Deploy-Ziel im Blick').locator('.lang-badge')).toHaveCount(0)
})

test('search matches what the chosen language and view show', async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 1000 })
  await openRelease(page, V102)
  const none = sheet(page).getByRole('heading', { name: 'No release matches' })
  // A benefit is only on screen in Highlights.
  await search(page).fill('approved blind')
  await expect(rows(page)).toHaveCount(1)
  await expect(block(page, 'Deploy target on screen').locator('.benefit mark')).toHaveText('approved blind')
  await radio(page, 'Details').click()
  await expect(none).toBeVisible()
  // A commit subject is on screen in Details, marked.
  await search(page).fill('contract pins')
  await expect(rows(page)).toHaveCount(1)
  await expect(block(page, 'Deploy target on screen').getByRole('list', { name: '4 commits' }).locator('mark')).toHaveText('contract pins')
  // The tag message is evidence: Details shows it, Highlights does not.
  await search(page).fill('evidence-only tag message')
  await expect(rows(page)).toHaveCount(1)
  await radio(page, 'Highlights').click()
  await expect(none).toBeVisible()
  // German words match only in German.
  await search(page).fill('Deploy-Ziel')
  await expect(none).toBeVisible()
  await radio(page, 'DE').click()
  await expect(rows(page)).toHaveCount(1)
  await expect(block(page, 'Deploy-Ziel im Blick').locator('h4 mark')).toHaveText('Deploy-Ziel')
})

test('Compare follows both switches', async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 1000 })
  await openRelease(page, V105)
  await sheet(page).getByRole('button', { name: 'Compare', exact: true }).click()
  await expect(page).toHaveURL(new RegExp(`/releases/${esc(V105)}$`))
  const compare = sheet(page).locator('section.compare')
  await expect(compare).toBeVisible()
  const dead = compare.getByRole('article', { name: 'Dead sessions tidy up' })
  await expect(dead.locator('.benefit')).toBeVisible()
  await expect(dead.locator('details.commits summary')).toHaveText('10 commits')
  await radio(page, 'Details').click()
  await expect(dead.locator('.benefit')).toHaveCount(0)
  await expect(dead.getByRole('list', { name: '10 commits' }).getByRole('listitem')).toHaveCount(10)
  await expect(compare.locator('summary')).toHaveCount(0)
  await radio(page, 'DE').click()
  await expect(compare.getByRole('article', { name: 'Tote Sitzungen räumen auf' })).toBeVisible()
  await expect(compare.getByRole('article', { name: 'Deploy-Ziel im Blick' })).toHaveCount(0) // 102 is the older end, not in the range
  await expect(page).toHaveURL(/release_lang=de/)
  await expect(page).toHaveURL(/release_view=details/)
})

test('a choice that moves a filtered selection lands in the address with it, and survives a reload', async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 1000 })
  for (const [choice, key] of [['DE', 'release_lang=de'], ['Details', 'release_view=details']] as const) {
    await openRelease(page, V105, '?release_lang=en&release_view=highlights')
    // "file" is on both in English Highlights; after either choice only 102 has it.
    await search(page).fill('file')
    await expect(rows(page)).toHaveCount(2)
    await radio(page, choice).click()
    await expect(rows(page)).toHaveCount(1)
    await expect(page).toHaveURL(new RegExp(`/releases/${esc(V102)}\\?.*${key}`))
    await page.reload()
    await expect(detail(page).getByRole('heading', { level: 2 })).toBeVisible()
    await expect(radio(page, choice)).toHaveAttribute('aria-checked', 'true')
    await expect(page).toHaveURL(new RegExp(`/releases/${esc(V102)}\\?.*${key}`))
  }
  // The other way round: a release and a choice in the same moment both land.
  await openRelease(page, V105)
  await sheet(page).evaluate(dialog => {
    dialog.querySelectorAll<HTMLElement>('[role="row"]')[1]!.click()
    ;[...dialog.querySelectorAll<HTMLButtonElement>('[role="radio"]')].find(b => b.textContent?.trim() === 'Details')!.click()
  })
  await expect(page).toHaveURL(new RegExp(`/releases/${esc(V102)}\\?release_view=details$`))
  await expect(radio(page, 'Details')).toHaveAttribute('aria-checked', 'true')
})

test('the list and Compare badge a name shown in the other language', async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 1000 })
  await openRelease(page, V105, '?release_lang=de', { fallback: true })
  // 105 names AEON-291, which has no German pill: English, with the badge.
  const rail105 = rows(page).first().locator('.rail-name')
  await expect(rail105.locator('.headline')).toContainText('Dead sessions tidy up')
  await expect(rail105.locator('.headline')).toHaveAttribute('lang', 'en')
  // Beside the two clamped lines, so a long name never hides it.
  await expect(rail105.locator('.lang-badge')).toBeVisible()
  await expect(rail105.locator('.lang-badge')).toHaveText('EN')
  await expect(rail105.locator('.lang-badge')).toHaveAttribute('data-tip', 'Not translated yet, shown in English')
  // 102's German theme needs none.
  await expect(rows(page).nth(1).locator('.headline')).toHaveText('Deploy mit offenen Augen')
  await expect(rows(page).nth(1).locator('.lang-badge')).toHaveCount(0)
  // Compare's list of releases in the range says the same.
  await sheet(page).getByRole('button', { name: 'Compare', exact: true }).click()
  const included = sheet(page).locator('section.compare .included li')
  await expect(included).toHaveCount(1)
  await expect(included.locator('.inc-headline')).toContainText('Dead sessions tidy up')
  await expect(included.locator('.inc-headline')).toHaveAttribute('lang', 'en')
  await expect(included.locator('.lang-badge')).toHaveText('EN')
  // In English nothing fell back.
  await radio(page, 'EN').click()
  await expect(included.locator('.lang-badge')).toHaveCount(0)
  await expect(rows(page).locator('.lang-badge')).toHaveCount(0)
})

test('empty and compare lines switch language with the rest', async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 1000 })
  await openRelease(page, V101, '?release_lang=de', { empty: true })
  const none = detail(page).locator('p.none')
  await expect(none).toHaveText('Zwischen diesem und dem vorherigen Release sind keine Änderungen verzeichnet.')
  await expect(none).toHaveAttribute('lang', 'de')
  await radio(page, 'EN').click()
  await expect(none).toHaveText('No changes are recorded between this release and the one before it.')

  // Compare suggests 100 as the other end: nothing between them.
  await sheet(page).getByRole('button', { name: 'Compare', exact: true }).click()
  const compare = sheet(page).locator('section.compare')
  await expect(compare.locator('p.none')).toHaveText('No changes are recorded between these releases.')
  await expect(sheet(page).locator('.compare-hint')).toContainText('Comparing from')
  await radio(page, 'DE').click()
  await expect(compare.locator('p.none')).toHaveText('Zwischen diesen Releases sind keine Änderungen verzeichnet.')
  await expect(sheet(page).locator('.compare-hint')).toContainText('Vergleich ab')
  await expect(sheet(page).locator('.compare-hint')).toContainText('oder einem Klick wählen.')
  await compare.getByRole('button', { name: 'Done' }).click()

  // A search without hits says so in the chosen language.
  await search(page).fill('zzzz')
  await expect(sheet(page).getByRole('heading', { name: 'Kein Release passt' })).toBeVisible()
  await expect(sheet(page).getByText('Nichts in 4 Releases passt zu „zzzz“.')).toBeVisible()
  await radio(page, 'EN').click()
  await expect(sheet(page).getByRole('heading', { name: 'No release matches' })).toBeVisible()
  await sheet(page).getByRole('button', { name: 'Clear search and filters' }).click()
  await expect(rows(page)).toHaveCount(4)
})

test('Details search finds the commit SHAs and the evidence it shows', async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 1000 })
  await openRelease(page, V105, '?release_view=details')
  // 5a622e2 is 105's source commit under Evidence.
  await search(page).fill('5a622e2')
  await expect(rows(page)).toHaveCount(1)
  await expect(rows(page).first()).toHaveAttribute('id', `release-${V105.replaceAll('.', '-')}`)
  await expect(detail(page).getByRole('region', { name: 'Evidence' })).toContainText('Tag message: stable105')
  // A commit listed under a ticket: found by its short SHA, which is marked.
  await search(page).fill('')
  const sha = (await block(page, 'Dead sessions tidy up').getByRole('link', { name: /^Commit [0-9a-f]{7} on GitHub$/ }).nth(3).textContent())!.trim()
  await search(page).fill(sha)
  await expect(rows(page)).toHaveCount(1)
  await expect(block(page, 'Dead sessions tidy up').locator('.commit mark')).toHaveText(sha)
  // Highlights shows the same commits folded and opens them on the hit.
  await radio(page, 'Highlights').click()
  await expect(rows(page)).toHaveCount(1)
  await expect(block(page, 'Dead sessions tidy up').locator('details.commits')).toHaveAttribute('open', '')
  // Evidence fields only where Details shows them: 102's snapshot hash and image.
  const none = sheet(page).getByRole('heading', { name: 'No release matches' })
  for (const q of ['c1c1c1c1', `ghcr.io/inspr-at/aeon:${V102}`]) {
    await search(page).fill(q)
    await expect(none).toBeVisible()
    await radio(page, 'Details').click()
    await expect(rows(page)).toHaveCount(1)
    await expect(page).toHaveURL(new RegExp(`/releases/${esc(V102)}\\?`))
    await radio(page, 'Highlights').click()
  }
  // A word inside a hash is no hit.
  await radio(page, 'Details').click()
  await search(page).fill(sha.slice(2))
  await expect(none).toBeVisible()
})

test('arrow keys move focus and choice in both radio groups, all four directions', async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 1000 })
  await openRelease(page, V105, '?release_lang=en')
  for (const [first, second] of [['EN', 'DE'], ['Highlights', 'Details']] as const) {
    await radio(page, first).focus()
    for (const [key, now] of [['ArrowDown', second], ['ArrowDown', first], ['ArrowUp', second], ['ArrowUp', first], ['ArrowRight', second], ['ArrowLeft', first], ['End', second], ['Home', first]] as const) {
      await page.keyboard.press(key)
      await expect(radio(page, now)).toHaveAttribute('aria-checked', 'true')
      await expect(radio(page, now)).toBeFocused()
      await expect(radio(page, now)).toHaveAttribute('tabindex', '0')
    }
  }
  // The keys stay in the group: the release list's selection does not move.
  await expect(page).toHaveURL(new RegExp(`/releases/${esc(V105)}\\?`))
  await expect(rows(page).first()).toHaveAttribute('aria-selected', 'true')
})

test('the switches sit with search and Compare at 36 px on desktop and take their own row on phones', async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 1000 })
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await openRelease(page, V105)
  const seg = sheet(page).getByRole('radiogroup', { name: 'View' })
  const box = (await seg.boundingBox())!
  const field = (await search(page).boundingBox())!
  const compareBox = (await sheet(page).getByRole('button', { name: 'Compare', exact: true }).boundingBox())!
  expect(box.height).toBe(field.height)
  expect(box.height).toBe(36)
  expect(Math.abs((box.y + box.height / 2) - (field.y + field.height / 2))).toBeLessThan(1)
  expect(box.x + box.width).toBeLessThanOrEqual(field.x)
  expect(compareBox.x).toBeGreaterThan(field.x)

  // The current tablet toolbar fits one line; title and controls keep their room.
  await page.setViewportSize({ width: 1024, height: 800 })
  const title = sheet(page).getByRole('heading', { level: 1 })
  expect(await title.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
  const mid = (await seg.boundingBox())!
  expect(mid.y).toBe((await search(page).boundingBox())!.y)
  await expectStableControls({ controls: { view: seg, language: sheet(page).getByRole('radiogroup', { name: 'Language' }), search: search(page), compare: sheet(page).getByRole('button', { name: 'Compare', exact: true }) }, interactions: [
    { name: 'German', run: async () => { await radio(page, 'DE').click(); await expect(radio(page, 'DE')).toHaveAttribute('aria-checked', 'true') } },
    { name: 'Details', run: async () => { await radio(page, 'Details').click(); await expect(radio(page, 'Details')).toHaveAttribute('aria-checked', 'true') } },
  ] })
  expect(await noHorizontalScroll(page)).toBe(true)
  if (SHOTS) { await page.screenshot({ path: test.info().outputPath(`header-1024-light.png`) }) }

  await page.setViewportSize({ width: 390, height: 844 })
  const lang = (await sheet(page).getByRole('radiogroup', { name: 'Language' }).boundingBox())!
  const phoneField = (await sheet(page).getByRole('heading', { level: 1 }).boundingBox())!
  expect(lang.y).toBeGreaterThan(phoneField.y + phoneField.height)
  expect(await noHorizontalScroll(page)).toBe(true)
})

for (const width of [1600, 390]) {
  for (const colorScheme of ['light', 'dark'] as const) {
    test(`switches fit ${width} ${colorScheme} in both views, and Details passes axe`, async ({ page }) => {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
      await page.emulateMedia({ colorScheme, reducedMotion: 'reduce' })
      await openRelease(page, V105, '', { fallback: true })
      expect(await noHorizontalScroll(page)).toBe(true)
      await radio(page, 'Details').click()
      await radio(page, 'DE').click()
      await expect(block(page, 'Dead sessions tidy up').getByRole('list', { name: '10 commits' })).toBeVisible()
      expect(await noHorizontalScroll(page)).toBe(true)
      await page.waitForTimeout(250)
      // The calendar version is the vendored INSPR display (pinned presentation), as in releases.spec.
      const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).exclude('.calendar-version').analyze()
      const summary = results.violations.map(v => `${v.id} (${v.impact}): ${v.help}\n${v.nodes.slice(0, 4).map(n => `    ${n.target.join(' ')}: ${n.failureSummary}`).join('\n')}`)
      expect(summary, summary.join('\n')).toEqual([])
      if (width === 390) await sheet(page).getByRole('button', { name: 'All releases' }).click()
      await expect(rows(page).first()).toBeVisible()
      expect(await noHorizontalScroll(page)).toBe(true)
    })
  }
}

test('screenshots of both switches on 102 and 105', async ({ page }) => {
  test.skip(!SHOTS, 'AEON_323_SHOTS enables design evidence')
  test.setTimeout(180_000)
  for (const width of [1600, 1440, 1024, 390]) {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    for (const colorScheme of ['light', 'dark'] as const) {
      await page.emulateMedia({ colorScheme, reducedMotion: 'reduce' })
      const shot = (name: string) => page.screenshot({ path: test.info().outputPath(`${name}-${width}-${colorScheme}.png`) })
      const list = async () => {
        if (width === 390 && await sheet(page).getByRole('button', { name: 'All releases' }).isVisible()) await sheet(page).getByRole('button', { name: 'All releases' }).click()
      }
      await openRelease(page, V105, '?release_lang=en', { fallback: true })
      await shot('105-highlights-en')
      await radio(page, 'Details').click()
      await expect(detail(page).getByRole('region', { name: 'Evidence' })).toBeVisible()
      await shot('105-details-en')
      await detail(page).getByRole('region', { name: 'Evidence' }).scrollIntoViewIfNeeded()
      await shot('105-details-evidence-en')
      await detail(page).getByRole('heading', { level: 2 }).scrollIntoViewIfNeeded()
      await radio(page, 'DE').click()
      await radio(page, 'Highlights').click()
      await shot('105-highlights-de-fallback')
      await list()
      await shot('list-highlights-de')
      await radio(page, 'Details').click()
      await shot('list-details-de')
      await rows(page).nth(1).click()
      await expect(block(page, 'Deploy-Ziel im Blick')).toBeVisible()
      await shot('102-details-de')
      await radio(page, 'Highlights').click()
      await shot('102-highlights-de')
      await list()
      await sheet(page).getByRole('button', { name: 'Compare', exact: true }).click()
      await rows(page).first().click()
      await expect(sheet(page).locator('section.compare')).toBeVisible()
      await shot('compare-highlights-de')
      await radio(page, 'Details').click()
      await shot('compare-details-de')
    }
  }
})
