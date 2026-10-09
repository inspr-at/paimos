// SPDX-License-Identifier: AGPL-3.0-only
// AEON-289 grouped stable100 by linked ticket. AEON-295 shows one benefit
// line per visible ticket in Features and Fixes, with those commits behind
// a disclosure. Without a group, P0.x commits stay under Other.
import { mkdirSync } from 'node:fs'
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork, type Fixtures } from './work-fixtures'
import { mockReleases, releaseHistory } from './releases-fixtures'

const SHOTS = process.env.AEON_295_SHOTS
const sheet = (page: Page) => page.getByRole('dialog', { name: 'PAIMOS AEON releases' })

const NOTES: Record<string, { pill_en: string; pill_de: string; benefit_en: string; benefit_de: string }> = {
  'AEON-224': {
    pill_en: 'Session name and model', pill_de: 'Name und Modell der Sitzung',
    benefit_en: 'You set the session name, model and effort, and the settings migration no longer waits on a lock.',
    benefit_de: 'Du setzt Name, Modell und Aufwand der Sitzung, und die Einstellungs-Migration wartet nicht mehr auf eine Sperre.',
  },
  'AEON-225': {
    pill_en: 'Session requests', pill_de: 'Sitzungsanfragen',
    benefit_en: 'You can rename a session and change its model, and the report stays intact.',
    benefit_de: 'Du kannst eine Sitzung umbenennen und das Modell wechseln, und der Bericht bleibt vollständig.',
  },
  'AEON-273': {
    pill_en: 'Session chat on iPhone', pill_de: 'Sitzungschat auf dem iPhone',
    benefit_en: 'The iPhone session chat keeps its tabs, unread messages and scroll position.',
    benefit_de: 'Der Sitzungschat auf dem iPhone behält Tabs, ungelesene Nachrichten und die Scrollposition.',
  },
  'AEON-274': {
    pill_en: 'Quotes open reliably', pill_de: 'Angebote öffnen zuverlässig',
    benefit_en: 'Showcase quotes open, and a draft copied from an empty list can still be issued.',
    benefit_de: 'Showcase-Angebote öffnen sich, und ein Entwurf aus einer leeren Liste bleibt ausstellbar.',
  },
}

const ROWS: { subject: string; tickets: string[]; group: 'features' | 'fixes' }[] = [
  { subject: 'AEON-273: reserve stable100 session chat on iPhone, managed settings, session requests, quote crash fix', tickets: ['AEON-273'], group: 'features' },
  { subject: 'P0.x: quote OpenAPI descriptions that corrupted the harness-session pin (AEON-225)', tickets: ['AEON-225'], group: 'features' },
  { subject: 'P0.x: harden session requests and preserve reporter payloads (AEON-225)', tickets: ['AEON-225'], group: 'features' },
  { subject: 'P0.x: viewport-fit=cover with safe-area padding for the shell and phone sheet (AEON-273)', tickets: ['AEON-273'], group: 'features' },
  { subject: 'P0.x: bound the managed settings migration\'s lock wait (AEON-224)', tickets: ['AEON-224'], group: 'features' },
  { subject: 'P0.x: drafts copied from null-list snapshots stay issuable (AEON-274)', tickets: ['AEON-274'], group: 'fixes' },
  { subject: 'P0.x: showcase quotes load without crashing (AEON-274)', tickets: ['AEON-274'], group: 'fixes' },
  { subject: 'P0.x: session chat tabs, unread and scroll behaviour (AEON-273)', tickets: ['AEON-273'], group: 'features' },
  { subject: 'P0.x: request unmanaged session rename and model changes (AEON-225)', tickets: ['AEON-225'], group: 'features' },
  { subject: 'P0.x: manage session name, model and effort (AEON-224)', tickets: ['AEON-224'], group: 'features' },
]

function stable100(grouped: boolean) {
  const base = releaseHistory(Date.parse('2026-09-29T08:00:00Z'))
  const version = '260929062507.0.0'
  const at = '2026-09-29T06:25:07Z'
  const changes = ROWS.map((row, i) => ({
    commit: (0x289100 + i).toString(16).padEnd(40, 'a'),
    subject: row.subject,
    type: 'other' as const,
    scope: '',
    tickets: row.tickets,
    at,
    ...(grouped ? { group: row.group, linked_tickets: row.tickets.map(key => ({ key, ...NOTES[key] })) } : {}),
  }))
  const older = '260929052224.0.0'
  const plain = {
    ...base.releases[1],
    version: older,
    tag: `v${older}`,
    release_sequence: 99,
    state: 'published' as const,
    headline: 'stable99',
    tickets: [],
    reserved_at: '2026-09-29T05:22:24Z',
    tagged_at: '2026-09-29T05:22:24Z',
    published_at: '2026-09-29T05:22:24Z',
    changes: [{ commit: (0x289099).toString(16).padEnd(40, 'b'), subject: 'P0.x: refresh the release manifest', type: 'other' as const, scope: '', tickets: [] as string[], at: '2026-09-29T05:22:24Z' }],
  }
  const newest = {
    ...base.releases[0],
    version, tag: `v${version}`, release_sequence: 100, state: 'published' as const,
    headline: 'stable100', tickets: ['AEON-224', 'AEON-225', 'AEON-273', 'AEON-274'],
    reserved_at: at, tagged_at: at, published_at: at, changes,
  }
  return { ...base, current: version, releases: [newest, plain] }
}

function withTickets(data: Fixtures) {
  const at = '2026-09-29T06:00:00Z'
  for (const [key, title] of [
    ['AEON-224', 'Session name, model and effort'],
    ['AEON-225', 'Rename and model requests'],
    ['AEON-273', 'Session chat on iPhone'],
    ['AEON-274', 'Quotes open reliably'],
  ] as const) {
    data.nodes.push({ id: `n-${key}`, key, kind_slug: 'ticket', title, body: '', state: 'done', project: 'p-aeon', fields: { priority: 'high' }, parent_id: 'p-aeon', created_at: at, updated_at: at })
  }
}

async function open(page: Page, grouped: boolean) {
  const data = fixtures()
  withTickets(data)
  const history = stable100(grouped)
  await mockWork(page, data)
  await mockReleases(page, history)
  await page.goto(`/releases/${history.current}`)
  // The rail names the pills too (AEON-305), so wait inside the detail.
  const ready = grouped ? 'Quotes open reliably' : 'showcase quotes load without crashing'
  await expect(sheet(page).locator('article.detail').getByText(ready)).toBeVisible()
  return history
}

async function shot(page: Page, name: string) {
  if (!SHOTS) return
  mkdirSync(SHOTS, { recursive: true })
  await page.screenshot({ path: `${SHOTS}/${name}.png` })
}

async function noHorizontalScroll(page: Page) {
  const overflow = await page.evaluate(() => {
    const dialog = document.querySelector('dialog.releases')
    const wide = (el: Element | null) => !!el && el.scrollWidth > el.clientWidth + 1
    return wide(dialog) || wide(document.documentElement)
  })
  expect(overflow).toBe(false)
}

for (const width of [1600, 390]) {
  test(`unannotated P0 commits stay under Other at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
    await page.emulateMedia({ colorScheme: 'light', reducedMotion: 'reduce' })
    await open(page, false)
    const changes = sheet(page).locator('.changes')
    await expect(changes.getByRole('region', { name: /^Other,/ })).toBeVisible()
    await expect(changes.getByText('showcase quotes load without crashing')).toBeVisible()
    await expect(changes.getByRole('region', { name: /^Features,/ })).toHaveCount(0)
    await expect(changes.getByRole('region', { name: /^Fixes,/ })).toHaveCount(0)
    await noHorizontalScroll(page)
  })

  test(`server groups stable100 into one benefit line per ticket at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
    await page.emulateMedia({ colorScheme: 'light', reducedMotion: 'reduce' })
    await open(page, true)
    const changes = sheet(page).locator('.changes')
    const features = changes.getByRole('region', { name: 'Features, 3' })
    const fixes = changes.getByRole('region', { name: 'Fixes, 1' })
    await expect(features).toBeVisible()
    await expect(fixes).toBeVisible()
    const chat = features.getByRole('article', { name: 'Session chat on iPhone' })
    const requests = features.getByRole('article', { name: 'Session requests' })
    const settings = features.getByRole('article', { name: 'Session name and model' })
    const quotes = fixes.getByRole('article', { name: 'Quotes open reliably' })
    await expect(chat.getByText('The iPhone session chat keeps its tabs, unread messages and scroll position.')).toBeVisible()
    await expect(requests.getByText('You can rename a session and change its model, and the report stays intact.')).toBeVisible()
    await expect(settings.getByText('the settings migration no longer waits on a lock')).toBeVisible()
    await expect(quotes.getByText('Showcase quotes open, and a draft copied from an empty list can still be issued.')).toBeVisible()
    await expect(chat.getByRole('link', { name: /AEON-273/ })).toBeVisible()
    await expect(quotes.getByRole('link', { name: /AEON-274/ })).toBeVisible()
    await expect(chat.locator('summary')).toHaveText('3 commits')
    await expect(requests.locator('summary')).toHaveText('3 commits')
    await expect(settings.locator('summary')).toHaveText('2 commits')
    await expect(quotes.locator('summary')).toHaveText('2 commits')
    await expect(chat.getByText('Reserve stable100 session chat on iPhone')).toBeHidden()
    await expect(changes.getByText('P0.x:')).toHaveCount(0)
    await expect(changes.getByRole('region', { name: /^Other,/ })).toHaveCount(0)
    await noHorizontalScroll(page)
    await shot(page, `${width}-light`)
    await chat.locator('summary').click()
    await expect(chat.getByText('Reserve stable100 session chat on iPhone')).toBeVisible()
    await expect(chat.getByText('Session chat tabs, unread and scroll behaviour')).toBeVisible()
    await expect(chat.getByText('P0.x:')).toHaveCount(0)
    await expect(chat.getByText('AEON-273:')).toHaveCount(0)
    await noHorizontalScroll(page)
    if (width === 1600 || width === 390) await shot(page, `${width}-light-open`)
    await chat.locator('summary').click()
    await page.emulateMedia({ colorScheme: 'dark', reducedMotion: 'reduce' })
    await expect(fixes).toBeVisible()
    await expect(quotes.getByText('Showcase quotes open, and a draft copied from an empty list can still be issued.')).toBeVisible()
    await noHorizontalScroll(page)
    await shot(page, `${width}-dark`)
    if (width === 1600) {
      const selected = sheet(page).getByRole('row', { selected: true })
      await expect(selected.getByRole('img', { name: '3 features' })).toBeVisible()
      await expect(selected.getByRole('img', { name: '1 fix' })).toBeVisible()
      await sheet(page).getByRole('button', { name: 'Features', exact: true }).click()
      await expect(sheet(page).getByText('1 of 2')).toBeVisible()
      await expect(features).toBeVisible()
      await sheet(page).getByRole('button', { name: 'Features', exact: true }).click()
      await sheet(page).getByRole('button', { name: 'Fixes', exact: true }).click()
      await expect(sheet(page).getByText('1 of 2')).toBeVisible()
      await expect(quotes).toBeVisible()
    }
  })
}

test('Enter and Space expand the commit disclosure and leave the release where it is', async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 900 })
  await open(page, true)
  const chat = sheet(page).locator('.changes').getByRole('article', { name: 'Session chat on iPhone' })
  const summary = chat.locator('summary')
  const subject = 'Reserve stable100 session chat on iPhone'
  await summary.focus()
  await page.keyboard.press('Enter')
  await expect(chat.getByText(subject)).toBeVisible()
  await expect(summary).toBeFocused()
  await page.keyboard.press('Space')
  await expect(chat.getByText(subject)).toBeHidden()
  await expect(summary).toBeFocused()
})

test('a search that matches only a commit subject opens that disclosure', async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 900 })
  await open(page, true)
  const changes = sheet(page).locator('.changes')
  const chat = changes.getByRole('article', { name: 'Session chat on iPhone' })
  const requests = changes.getByRole('article', { name: 'Session requests' })
  const search = sheet(page).getByRole('searchbox', { name: 'Search releases' })
  await search.fill('scroll position')
  await expect(chat.getByText('unread messages and scroll position')).toBeVisible()
  await expect(chat.getByText('Reserve stable100 session chat on iPhone')).toBeHidden()
  await search.fill('viewport-fit')
  await expect(chat.getByText('safe-area padding for the shell and phone sheet')).toBeVisible()
  await expect(requests.getByText('Harden session requests and preserve reporter payloads')).toBeHidden()
})

test('a German locale uses the German pill and benefit, and an empty German field falls back to English', async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 900 })
  const profile = { principal_id: '11111111-1111-4111-8111-111111111111', email: 'markus@barta.com', first_name: 'Markus', last_name: 'Barta', preferred_name: '', short_name: 'mba', initials: 'MB', timezone: 'Europe/Vienna', locale: 'de-AT', greeting_enabled: false, avatar_color: 'teal', avatar_hashes: {}, week_start: 1, revision: 1 }
  const data = fixtures()
  withTickets(data)
  const history = stable100(true)
  await mockWork(page, data)
  await mockReleases(page, history)
  await page.route('**/api/me/profile', route => route.fulfill({ json: profile }))
  await page.goto(`/releases/${history.current}`)
  const features = sheet(page).locator('.changes').getByRole('region', { name: 'Features, 3' })
  await expect(features.getByRole('article', { name: 'Sitzungschat auf dem iPhone' })).toBeVisible()
  await expect(features.getByText('Der Sitzungschat auf dem iPhone behält Tabs, ungelesene Nachrichten und die Scrollposition.')).toBeVisible()
  const partial = stable100(true)
  const first = partial.releases[0].changes.find(change => change.tickets?.includes('AEON-273') && change.linked_tickets)
  first!.linked_tickets = first!.linked_tickets!.map(note => note.key === 'AEON-273' ? { ...note, pill_de: '', benefit_de: ' ' } : note)
  await page.unroute('**/api/releases**')
  await page.route('**/api/releases**', route => route.fulfill({ json: partial }))
  await page.reload()
  await expect(sheet(page).getByRole('article', { name: 'Session chat on iPhone' })).toBeVisible()
  await expect(sheet(page).getByText('The iPhone session chat keeps its tabs, unread messages and scroll position.')).toBeVisible()
})

// AEON-305: a commit that names two bug tickets carries one group, fixes, and
// each ticket stays a fix: in the detail, the filters and compare, whether a
// snapshot tells the release or the linked tickets do (with or without the
// server's per-ticket group).
function sharedBugs(snapshot: boolean) {
  const history = stable100(true)
  const newest = history.releases[0] as typeof history.releases[0] & Record<string, unknown>
  const keys = ['AEON-274', 'AEON-225']
  newest.changes = [{
    ...newest.changes[5], subject: 'P0.x: quotes open and session requests recover (AEON-274, AEON-225)', tickets: keys, group: 'fixes',
    linked_tickets: keys.map(key => ({ key, ...NOTES[key], ...(snapshot ? { group: 'fixes' as const } : {}) })),
  }]
  if (snapshot) newest.notes = { source: 'database-snapshot', snapshot_sha256: 'd4'.repeat(32), captured_at: '2026-09-29T06:30:00Z', release_revision: 1, gaps: [], hidden: 0, items: keys.map(key => ({ id: key, key, ...NOTES[key] })) }
  return history
}

for (const snapshot of [false, true]) {
  test(`two bugs on one commit are both fixes, in the detail, the filters and compare (${snapshot ? 'snapshot' : 'current'})`, async ({ page }) => {
    await page.setViewportSize({ width: 1600, height: 900 })
    const data = fixtures()
    withTickets(data)
    await mockWork(page, data)
    await mockReleases(page, sharedBugs(snapshot))
    await page.goto('/releases')
    const detail = sheet(page).locator('article.detail .changes')
    const fixes = detail.getByRole('region', { name: 'Fixes, 2' })
    await expect(fixes.getByRole('article', { name: 'Quotes open reliably' })).toBeVisible()
    await expect(fixes.getByRole('article', { name: 'Session requests' })).toBeVisible()
    await expect(detail.getByRole('region', { name: /^Features,/ })).toHaveCount(0)
    await expect(sheet(page).getByRole('row', { selected: true }).getByRole('img', { name: '2 fixes' })).toBeVisible()
    const toggles = sheet(page).getByRole('group', { name: 'Show only releases with' })
    await toggles.getByRole('button', { name: 'Fixes' }).click()
    await expect(sheet(page).getByText('1 of 2')).toBeVisible()
    await toggles.getByRole('button', { name: 'Fixes' }).click()
    await toggles.getByRole('button', { name: 'Features' }).click()
    await expect(sheet(page).getByText('0 of 2')).toBeVisible()
    await toggles.getByRole('button', { name: 'Features' }).click()
    await expect(sheet(page).getByRole('grid', { name: 'Releases, newest first' }).getByRole('row')).toHaveCount(2)
    await sheet(page).getByRole('grid', { name: 'Releases, newest first' }).focus()
    await page.keyboard.press('c')
    const compare = sheet(page).locator('.compare')
    await expect(compare.locator('.facts')).toContainText('1 release')
    const compared = compare.locator('.changes').getByRole('region', { name: 'Fixes, 2' })
    await expect(compared.getByRole('article', { name: 'Quotes open reliably' })).toBeVisible()
    await expect(compared.getByRole('article', { name: 'Session requests' })).toBeVisible()
    await expect(compare.locator('.changes').getByRole('region', { name: /^Features,/ })).toHaveCount(0)
  })
}

// AEON-386: the response groups AEON-99 and AEON-98 as fixes and AEON-97 as a
// feature, with no pill or benefit. The shared AEON-21/98 commit is fixes.
// Compare shows those groups. Frozen text stays; nothing live is added.
test('compare places server groups that have no note text', async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 900 })
  const base = releaseHistory(Date.parse('2026-09-26T12:00:00Z'))
  const at = '2026-09-26T12:00:00Z'
  const sha = (id: string) => id.padEnd(40, 'a')
  const note = (key: string, group: 'features' | 'fixes', pill: string, benefit: string) => ({ key, group, pill_en: pill, pill_de: '', benefit_en: benefit, benefit_de: '' })
  const commit = (id: string, subject: string, tickets: string[], group: 'features' | 'fixes', linked?: ReturnType<typeof note>[]) => ({
    commit: sha(id), subject, type: 'other' as const, scope: '', tickets, at, group, ...(linked?.length ? { linked_tickets: linked } : {}),
  })
  const changes = [
    commit('g', 'AEON-20: frozen group', ['AEON-20'], 'fixes', [note('AEON-20', 'fixes', 'Frozen kept', 'Stays a fix.')]),
    commit('h', 'AEON-99: outside a grouped capture', ['AEON-99'], 'fixes'),
    commit('i', 'AEON-97: benefit outside a grouped capture', ['AEON-97'], 'features'),
    commit('j', 'AEON-98: hidden member of a grouped capture', ['AEON-98'], 'fixes'),
    commit('k', 'AEON-21: shared with a hidden bug', ['AEON-21', 'AEON-98'], 'fixes', [note('AEON-21', 'features', 'Frozen feature', 'Stays a feature.')]),
  ]
  const newest = {
    ...base.releases[0],
    headline: 'grouped capture',
    tickets: ['AEON-20', 'AEON-21'],
    changes,
    notes: {
      source: 'database-snapshot', snapshot_sha256: 'ab'.repeat(32), captured_at: at, release_revision: 1, gaps: [], hidden: 1,
      items: [
        { id: '20', key: 'AEON-20', group: 'fixes' as const, pill_en: 'Frozen kept', pill_de: '', benefit_en: 'Stays a fix.', benefit_de: '' },
        { id: '21', key: 'AEON-21', group: 'features' as const, pill_en: 'Frozen feature', pill_de: '', benefit_en: 'Stays a feature.', benefit_de: '' },
      ],
    },
  }
  const history = { ...base, releases: [newest, base.releases[1]] }
  await mockWork(page, fixtures())
  await mockReleases(page, history)
  const pending = page.waitForResponse(resp => new URL(resp.url()).pathname === '/api/releases')
  await page.goto('/releases')
  const body = await (await pending).json()
  const served = body.releases[0]
  const byCommit = (id: string) => served.changes.find((change: { commit: string }) => change.commit === sha(id))
  expect(byCommit('h').group).toBe('fixes')
  expect(byCommit('j').group).toBe('fixes')
  expect(byCommit('i').group).toBe('features')
  expect(byCommit('k').group).toBe('fixes')
  expect(byCommit('k').tickets).toEqual(['AEON-21', 'AEON-98'])
  expect(JSON.stringify(body)).not.toMatch(/LIVE|HIDDEN/)
  await expect(sheet(page)).toBeVisible()
  await sheet(page).getByRole('grid', { name: 'Releases, newest first' }).focus()
  await page.keyboard.press('c')
  const changesPane = sheet(page).locator('.compare .changes')
  const features = changesPane.getByRole('region', { name: 'Features, 1' })
  const fixes = changesPane.getByRole('region', { name: 'Fixes, 4' })
  await expect(features.getByRole('article', { name: 'AEON-97', exact: true })).toBeVisible()
  await expect(fixes.getByRole('article', { name: 'AEON-99', exact: true })).toBeVisible()
  await expect(fixes.getByRole('article', { name: 'AEON-98', exact: true })).toBeVisible()
  await expect(fixes.getByRole('article', { name: 'Frozen feature', exact: true })).toBeVisible()
  await expect(fixes.getByRole('article', { name: 'Frozen feature', exact: true })).toContainText('Stays a feature.')
  await expect(features.getByText('Frozen feature')).toHaveCount(0)
  await expect(features.getByText('AEON-21')).toHaveCount(0)
  await expect(features.getByText('AEON-98')).toHaveCount(0)
  await expect(features.getByText('AEON-99')).toHaveCount(0)
  await expect(changesPane.getByRole('region', { name: /^Other changes/ })).toHaveCount(0)
  await expect(fixes.getByRole('article', { name: 'AEON-98', exact: true }).locator('summary')).toHaveText('2 commits')
  await sheet(page).getByRole('radio', { name: 'Details', exact: true }).click()
  // The shared commit is listed on both fixes it names, and on no feature.
  await expect(fixes.getByText('Shared with a hidden bug')).toHaveCount(2)
  await expect(features.getByText('Shared with a hidden bug')).toHaveCount(0)
  await expect(fixes.getByText('Outside a grouped capture')).toBeVisible()
  await expect(features.getByText('Benefit outside a grouped capture')).toBeVisible()
  await expect(sheet(page).locator('section.compare')).not.toContainText('LIVE')
})
