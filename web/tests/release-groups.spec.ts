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
  const ready = grouped ? 'Quotes open reliably' : 'showcase quotes load without crashing'
  await expect(sheet(page).getByText(ready)).toBeVisible()
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
    await expect(changes.getByRole('region', { name: /^Other changes,/ })).toBeVisible()
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
    await expect(changes.getByRole('region', { name: /^Other changes,/ })).toHaveCount(0)
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
      const selected = sheet(page).getByRole('option', { selected: true })
      await expect(selected.getByRole('img', { name: '3 features' })).toBeVisible()
      await expect(selected.getByRole('img', { name: '1 fix' })).toBeVisible()
      await sheet(page).getByRole('button', { name: 'Fixes', exact: true }).click()
      await expect(sheet(page).getByText('1 of 2')).toBeVisible()
      await expect(quotes).toBeVisible()
    }
  })
}

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
