// SPDX-License-Identifier: AGPL-3.0-only
// AEON-289: release changes group by the server's linked-ticket group.
// Subjects are the commits of stable100 (v260929062507.0.0). Without group,
// P0.x commits all sit under Other. With group, bug tickets are Fixes and
// tickets that carry a public release note are Features.
import { mkdirSync } from 'node:fs'
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork, type Fixtures } from './work-fixtures'
import { mockReleases, releaseHistory, type History } from './releases-fixtures'

const SHOTS = process.env.AEON_289_SHOTS
const sheet = (page: Page) => page.getByRole('dialog', { name: 'PAIMOS AEON releases' })

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

function stable100(grouped: boolean): History {
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
    ...(grouped ? { group: row.group } : {}),
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
  await expect(sheet(page).getByText('showcase quotes load without crashing')).toBeVisible()
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
    await shot(page, `before-${width}-light`)
    await page.emulateMedia({ colorScheme: 'dark', reducedMotion: 'reduce' })
    await shot(page, `before-${width}-dark`)
  })

  test(`server groups stable100 into Features and Fixes at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
    await page.emulateMedia({ colorScheme: 'light', reducedMotion: 'reduce' })
    await open(page, true)
    const changes = sheet(page).locator('.changes')
    const features = changes.getByRole('region', { name: 'Features, 8' })
    const fixes = changes.getByRole('region', { name: 'Fixes, 2' })
    await expect(features).toBeVisible()
    await expect(fixes).toBeVisible()
    await expect(features.getByText('session chat tabs, unread and scroll behaviour')).toBeVisible()
    await expect(fixes.getByText('showcase quotes load without crashing')).toBeVisible()
    await expect(fixes.getByText('drafts copied from null-list snapshots stay issuable')).toBeVisible()
    await expect(changes.getByRole('region', { name: /^Other changes,/ })).toHaveCount(0)
    await noHorizontalScroll(page)
    await shot(page, `after-${width}-light`)
    await page.emulateMedia({ colorScheme: 'dark', reducedMotion: 'reduce' })
    await expect(fixes).toBeVisible()
    await shot(page, `after-${width}-dark`)
    if (width === 1600) {
      const selected = sheet(page).getByRole('option', { selected: true })
      await expect(selected.getByRole('img', { name: '8 features' })).toBeVisible()
      await expect(selected.getByRole('img', { name: '2 fixes' })).toBeVisible()
      await sheet(page).getByRole('button', { name: 'Fixes', exact: true }).click()
      await expect(sheet(page).getByText('1 of 2')).toBeVisible()
      await expect(fixes.getByText('showcase quotes load without crashing')).toBeVisible()
    }
  })
}
