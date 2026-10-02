// SPDX-License-Identifier: AGPL-3.0-only
// AEON-305: a backfilled release (102) reads exactly like a current one (105):
// Features and Fixes, one block per ticket with its pill as the heading, the key
// chip, the benefit and the folded commits. No tag message as a title.
import { mkdirSync } from 'node:fs'
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { mockReleases } from './releases-fixtures'
import { historicHistory, NOTES } from './releases-historic-fixtures'

const SHOTS = process.env.AEON_305_HISTORIC_SHOTS
const sheet = (page: Page) => page.getByRole('dialog', { name: 'PAIMOS AEON releases' })
const detail = (page: Page) => sheet(page).locator('article.detail')

const V102 = '260929082208.0.0', V105 = '260929113854.0.0'

// The tickets exist in this workspace, so their keys are links.
function withTickets() {
  const data = fixtures()
  const at = '2026-09-29T06:00:00Z'
  for (const key of [...Object.keys(NOTES), 'AEON-278', 'AEON-285', 'AEON-290']) {
    data.nodes.push({ id: `n-${key}`, key, kind_slug: 'ticket', title: NOTES[key]?.pill_en ?? key, body: '', state: 'done', project: 'p-aeon', fields: { priority: 'high' }, parent_id: 'p-aeon', created_at: at, updated_at: at })
  }
  return data
}

async function open(page: Page, version: string, history = historicHistory(), locale?: string) {
  await mockWork(page, withTickets())
  await mockReleases(page, history)
  if (locale) {
    const profile = { principal_id: '11111111-1111-4111-8111-111111111111', email: 'markus@barta.com', first_name: 'Markus', last_name: 'Barta', preferred_name: '', short_name: 'mba', initials: 'MB', timezone: 'Europe/Vienna', locale, greeting_enabled: false, avatar_color: 'teal', avatar_hashes: {}, week_start: 1, revision: 1 }
    await page.route('**/api/me/profile', route => route.fulfill({ json: profile }))
  }
  await page.goto(`/releases/${version}`)
  await expect(detail(page).getByRole('heading', { level: 2 })).toBeVisible()
  return history
}
const noHorizontalScroll = (page: Page) => page.evaluate(() => {
  const wide = (el: Element | null) => !!el && el.scrollWidth > el.clientWidth + 1
  return !wide(document.querySelector('dialog.releases')) && !wide(document.documentElement)
})
// The blocks of one group: pill heading, key chip, and the folded commit count.
async function blocks(page: Page, group: string) {
  return detail(page).getByRole('region', { name: group }).locator('article.ticket-line').evaluateAll(els => els.map(el => [
    el.querySelector('.pill-title')?.textContent?.trim() ?? '',
    el.querySelector('.line-head a.ticket-link')?.textContent?.trim() ?? '',
    el.querySelector('.benefit') ? 'benefit' : '',
    el.querySelector('details:not([open]) summary')?.textContent?.trim() ?? '',
  ]))
}
// No tag message as a title, anywhere in the sheet.
async function noTagTitle(page: Page, tagMessage: string) {
  await expect(sheet(page).getByText('Historical tag headline')).toHaveCount(0)
  await expect(detail(page).locator('.headline')).toHaveCount(0)
  // Commit subjects may mention the tag ("Reserve stable102 …"); a title would be the message alone.
  await expect(sheet(page).getByText(tagMessage, { exact: true })).toHaveCount(0)
  await expect(sheet(page).getByText(/^release: v\d/i)).toHaveCount(0)
}

for (const width of [1600, 390]) {
  for (const colorScheme of ['light', 'dark'] as const) {
    test(`backfilled release 102 reads like release 105 at ${width} ${colorScheme}`, async ({ page }) => {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
      await page.emulateMedia({ colorScheme, reducedMotion: 'reduce' })
      await open(page, V105)
      expect(await blocks(page, 'Features, 2')).toEqual([
        ['Dead sessions tidy up', 'AEON-291', 'benefit', '10 commits'],
        ['Rules shadow check', 'AEON-251', 'benefit', '8 commits'],
      ])
      expect(await blocks(page, 'Fixes, 3')).toEqual([
        ['Session list on phones', 'AEON-304', 'benefit', '4 commits'],
        ['Clear Done check', 'AEON-303', 'benefit', '3 commits'],
        ['Honest project counts', 'AEON-302', 'benefit', '6 commits'],
      ])
      await expect(detail(page).getByRole('region', { name: 'Other changes, 9' })).toContainText('Sign and notarize darwin paimos-agentd in the release workflow')
      // The missing capture says nothing: no gap, no label, no tag message.
      await expect(detail(page)).not.toContainText('were not captured')
      await noTagTitle(page, 'release: v260929113854.0.0')
      expect(await noHorizontalScroll(page)).toBe(true)

      if (width === 390) await sheet(page).getByRole('button', { name: 'All releases' }).click()
      await sheet(page).getByRole('grid', { name: 'Releases, newest first' }).getByRole('row').nth(1).click()
      await expect(page).toHaveURL(`/releases/${V102}`)
      const features = detail(page).getByRole('region', { name: 'Features, 5' })
      await expect(features.locator('.group-h .g-icon svg')).toHaveCount(1)
      expect(await blocks(page, 'Features, 5')).toEqual([
        ['Deploy target on screen', 'AEON-211', 'benefit', '4 commits'],
        ['Rate agent work', 'AEON-218', 'benefit', '5 commits'],
        ['Instruction provenance', 'AEON-219', 'benefit', '3 commits'],
        ['Merged rules at start', 'AEON-249', 'benefit', '2 commits'],
        ['Read on every device', 'AEON-276', 'benefit', '3 commits'],
      ])
      // AEON-293 is bug-tagged, so its block is a fix.
      expect(await blocks(page, 'Fixes, 1')).toEqual([['Readable risk chip', 'AEON-293', 'benefit', '1 commit']])
      const deploy = features.getByRole('article', { name: 'Deploy target on screen' })
      await expect(deploy.locator('.benefit')).toHaveText('Every deploy approval names the server it goes to, so nothing is approved blind.')
      await expect(deploy.getByText('Preserve strict handoff bytes with optional deployment targets')).toBeHidden()
      await deploy.locator('summary').click()
      await expect(deploy.getByText('Preserve strict handoff bytes with optional deployment targets')).toBeVisible()
      await expect(deploy.getByText('P0.x:')).toHaveCount(0)
      await deploy.locator('summary').click()
      // The hidden ticket's commits stay under Other; the notes say one is hidden.
      await expect(detail(page).getByRole('region', { name: 'Other changes, 3' })).toContainText('Quote OpenAPI flow descriptions so contract pins drop parse artifacts')
      const notes = detail(page).getByRole('region', { name: 'Release notes' })
      await expect(notes.getByText('One ticket is hidden from release notes.', { exact: true })).toBeVisible()
      // Written after the release: one small, muted line, no label or card.
      const after = notes.getByText('Notes written after release', { exact: true })
      await expect(after).toBeVisible()
      expect(await after.evaluate(el => ({ size: getComputedStyle(el).fontSize, transform: getComputedStyle(el).textTransform, tag: el.tagName }))).toEqual({ size: '12.5px', transform: 'none', tag: 'P' })
      await expect(detail(page).locator('.summary')).toHaveCount(0)
      await noTagTitle(page, 'stable102')
      expect(await noHorizontalScroll(page)).toBe(true)
      // Both releases share one renderer: the same block structure.
      await expect(detail(page).locator('article.ticket-line')).toHaveCount(6)
      await expect(detail(page).locator('article.ticket-line .line-head .pill-title + *')).toHaveCount(6)
    })
  }
}

test('a told ticket without commits keeps its block, without a disclosure', async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 1000 })
  const history = historicHistory()
  const release = history.releases.find(r => r.version === V102)! as unknown as { notes: { items: Record<string, string>[] } }
  release.notes.items.push({ id: '00000000-0000-4000-8000-000000000099', key: 'AEON-290', pill_en: 'Older notes filled in', pill_de: 'Ältere Notizen ergänzt', benefit_en: 'Releases from before the notes existed now show what they delivered.', benefit_de: 'Releases aus der Zeit vor den Notizen zeigen jetzt, was sie geliefert haben.' })
  await open(page, V102, history)
  const block = detail(page).getByRole('region', { name: 'Features, 6' }).getByRole('article', { name: 'Older notes filled in' })
  await expect(block.locator('.benefit')).toHaveText('Releases from before the notes existed now show what they delivered.')
  await expect(block.getByRole('link', { name: /AEON-290/ })).toBeVisible()
  await expect(block.locator('details')).toHaveCount(0)
})

test('a German profile reads the backfilled pills and benefits in German', async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 1000 })
  await open(page, V102, historicHistory(), 'de-AT')
  const fixes = detail(page).getByRole('region', { name: 'Fixes, 1' })
  await expect(fixes.getByRole('article', { name: 'Lesbarer Risiko-Chip' }).locator('.benefit')).toHaveText('Der Hochrisiko-Chip in der Deploy-Journey ist im hellen Modus gut lesbar.')
  await expect(fixes.locator('.pill-title')).toHaveAttribute('lang', 'de')
  await expect(detail(page).getByText('Ein Ticket ist in den Release Notes ausgeblendet.')).toBeVisible()
})

test('screenshots of releases 102 and 105', async ({ page }) => {
  test.skip(!SHOTS, 'set AEON_305_HISTORIC_SHOTS to a directory')
  mkdirSync(SHOTS!, { recursive: true })
  for (const width of [1600, 390]) {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    // 105 again with a presentation: the header sits above the same blocks.
    const presented = historicHistory()
    Object.assign(presented.releases[0]!, { presentation: {
      theme_en: 'Sessions that clean up', theme_de: 'Sitzungen, die aufräumen', headline_en: 'Dead sessions end on their own, and phones get a proper session list.', headline_de: '',
      intro_en: 'Sessions whose agent is gone close themselves, and the session menu offers only what works. Project counts and the Done check are honest again.', intro_de: '', revision: 1, updated_at: '2026-09-29T12:10:00Z' } })
    for (const [name, version, history] of [['102', V102, historicHistory()], ['105', V105, historicHistory()], ['105-presented', V105, presented]] as const) {
      for (const colorScheme of ['light', 'dark'] as const) {
        await page.emulateMedia({ colorScheme, reducedMotion: 'reduce' })
        await open(page, version, history)
        await page.screenshot({ path: `${SHOTS}/${name}-${width}-${colorScheme}.png` })
        // The whole detail: a viewport tall enough that the sheet does not scroll.
        const tall = await detail(page).evaluate(el => el.scrollHeight + el.getBoundingClientRect().top + 80)
        await page.setViewportSize({ width, height: Math.ceil(tall) })
        await page.screenshot({ path: `${SHOTS}/${name}-${width}-${colorScheme}-full.png` })
        await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
      }
    }
  }
})
