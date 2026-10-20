// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1502 (Tick spec v1 §6): approving a harness or a first pairing is one
// sentence plus Add or Deny; everything else sits behind Details.
import { expect, test, type Browser, type Locator, type Page, type TestInfo } from '@playwright/test'
import { mkdirSync, readdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { controlStability } from './control-stability'
import { mockPairing, type PairingCall } from './agent-pairing-fixtures'
import { fixtures, mockWork, watchErrors } from './work-fixtures'

type Language = 'en' | 'de'
const COMPUTER = '33333333-3333-4333-8333-333333333333'
const DIGEST = 'ab'.repeat(32)
// Neutral fixtures: public sources name no operator host and no business domain.
const ACCOUNT = 'ada@example.com'
const CODEX_ACCOUNT = 'codex@example.com'
const HOST = 'desk'
const claude = { account_key: 'claude-1', harness: 'claude', label: ACCOUNT }
const codex = { account_key: 'codex-1', harness: 'codex', label: CODEX_ACCOUNT }
const verifiable = {
  claude: { supported: true, policy: 'no_tools', reason: '' },
  codex: { supported: false, policy: 'unavailable', reason: 'Codex verification is unavailable.' },
}
// An add-harness request for an already paired computer (existing_computer_id).
const addOne = { existing_computer_id: COMPUTER, computer_name: HOST, requested_accounts: [claude], verification_capabilities: verifiable }
const addTwo = { ...addOne, requested_accounts: [claude, codex] }

const COPY = {
  en: {
    addOne: `Add Claude Code (${ACCOUNT}) to ${HOST}?`,
    addTwo: `Add Claude Code and Codex to ${HOST}?`,
    pair: 'Pair studio with INSPR?',
    factsOn: 'Verification on · Agents enabled after connecting · change in Details',
    factsOff: 'Verification off · Agents enabled after connecting · change in Details',
    role: 'Admins can start runs on this computer',
    add: 'Add', pairButton: 'Pair', deny: 'Deny',
    expired: `The request to add Claude Code to ${HOST} expired.`,
    expiredNext: `Run aeon-agentd add-harness --harness claude on ${HOST} to ask again.`,
    denied: 'Pairing studio with INSPR was denied.',
    deniedNext: "Run aeon-agentd pair --url 'https://aeon.example' on studio to ask again.",
  },
  de: {
    addOne: `Claude Code (${ACCOUNT}) zu ${HOST} hinzufügen?`,
    addTwo: `Claude Code und Codex zu ${HOST} hinzufügen?`,
    pair: 'studio mit INSPR koppeln?',
    factsOn: 'Prüfung an · Agents nach dem Verbinden aktiv · in Details änderbar',
    factsOff: 'Prüfung aus · Agents nach dem Verbinden aktiv · in Details änderbar',
    role: 'Admins können auf diesem Computer Läufe starten',
    add: 'Hinzufügen', pairButton: 'Koppeln', deny: 'Ablehnen',
    expired: `Die Anfrage, Claude Code zu ${HOST} hinzuzufügen, ist abgelaufen.`,
    expiredNext: `Zum erneuten Anfragen aeon-agentd add-harness --harness claude auf ${HOST} ausführen.`,
    denied: 'Das Koppeln von studio mit INSPR wurde abgelehnt.',
    deniedNext: "Zum erneuten Anfragen aeon-agentd pair --url 'https://aeon.example' auf studio ausführen.",
  },
} as const

// The app reads in English today (displayLanguage.ts, AEON-998). The German pass
// serves the helper's German choice to the real view, so the German strings are
// measured in the page that ships them.
async function speak(page: Page, language: Language) {
  if (language === 'de') await page.route(/\/src\/lib\/displayLanguage\.ts(\?.*)?$/, route => route.fulfill({
    contentType: 'text/javascript', body: "export function displayLanguage() { return 'de' }\n",
  }))
}

async function open(page: Page, language: Language, overrides: Record<string, unknown> = {}, viewport = { width: 1280, height: 800 }, theme: 'light' | 'dark' = 'light') {
  await page.setViewportSize(viewport)
  await page.emulateMedia({ colorScheme: theme })
  await page.clock.install({ time: new Date('2026-09-27T20:00:00.000Z') })
  await speak(page, language)
  await mockWork(page, fixtures())
  const calls = await mockPairing(page, overrides)
  await page.goto('/agents/register-agent')
  await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
  await page.getByLabel('Pairing code').fill('123-456-789')
  await page.getByRole('button', { name: 'Look up code' }).click()
  const review = page.getByRole('region', { name: 'Pairing review' })
  await expect(review.locator('[data-review-title]')).toBeVisible()
  return { calls, review }
}

const approvals = (calls: PairingCall[]) => calls.filter(call => /\/agent-pairing\/requests\/[^/]+\/approve$/.test(call.path))

// One checks.json per run: every test leaves its own file and the last worker to finish merges them.
const checks: Record<string, unknown>[] = []
function record(name: string, values: Record<string, unknown>) { checks.push({ name, ...values }) }
test.afterEach(async ({}, testInfo) => {
  if (!checks.length) return
  const dir = join(testInfo.project.outputDir, 'aeon-1502', 'checks')
  mkdirSync(dir, { recursive: true })
  writeFileSync(join(dir, `${testInfo.testId}.json`), JSON.stringify(checks.splice(0), null, 2))
})
test.afterAll(async ({}, testInfo) => {
  const dir = join(testInfo.project.outputDir, 'aeon-1502', 'checks')
  let files: string[] = []
  try { files = readdirSync(dir).filter(file => file.endsWith('.json')) } catch { return }
  const merged = files.flatMap(file => JSON.parse(readFileSync(join(dir, file), 'utf8')) as unknown[])
  writeFileSync(join(testInfo.project.outputDir, 'aeon-1502', 'checks.json'), JSON.stringify(merged, null, 2))
})

async function shot(page: Page, testInfo: TestInfo, name: string) {
  await page.screenshot({ path: testInfo.outputPath(`aeon-1502-${name}.png`) })
}

// English on the test's page, German in a fresh context of its own: one case covers both languages.
async function bothLanguages(browser: Browser, page: Page, run: (page: Page, language: Language) => Promise<void>) {
  await run(page, 'en')
  const context = await browser.newContext({ baseURL: test.info().project.use.baseURL, reducedMotion: 'reduce' })
  try { await run(await context.newPage(), 'de') } finally { await context.close() }
}

/** Every part of the ask (title through the Details summary) lies inside the viewport, unscrolled. */
async function fitsWithoutScrolling(page: Page, review: Locator) {
  return review.evaluate(card => {
    const parts = [...card.querySelectorAll('[data-review-title], [data-review-facts], [data-review-role], .actions .go, details.more > summary')]
    const bottom = Math.max(...parts.map(el => el.getBoundingClientRect().bottom))
    const top = Math.min(...parts.map(el => el.getBoundingClientRect().top))
    let scrolled = window.scrollY
    for (let parent = card.parentElement; parent; parent = parent.parentElement) scrolled += parent.scrollTop
    return { parts: parts.length, top, bottom, viewport: window.innerHeight, scrolled }
  })
}

/** Visible paragraphs on the default path, outside Details and the action status line. */
async function sentences(review: Locator) {
  return review.evaluate(card => [...card.querySelectorAll('p')]
    .filter(p => !p.closest('details') && p.getBoundingClientRect().width > 1 && p.textContent?.trim())
    .map(p => p.textContent!.trim()))
}

/** Text that does not fit its box: German labels must never overflow (scrollWidth ≤ clientWidth). */
async function overflowing(review: Locator) {
  return review.evaluate(card => [...card.querySelectorAll('h1, p, button, summary, dt, dd, label, li, span')]
    .filter(el => el.clientWidth > 0 && el.scrollWidth > el.clientWidth + 0.5)
    .map(el => el.textContent?.trim()))
}

async function detailsHold(page: Page, review: Locator, primary: string) {
  const summary = review.locator('details.more > summary')
  const guard = await controlStability(page, {
    title: review.locator('[data-review-title]'),
    facts: review.locator('[data-review-facts]'),
    role: review.locator('[data-review-role]'),
    primary: review.getByRole('button', { name: primary, exact: true }),
    deny: review.locator('.actions .go').nth(1),
    details: summary,
  })
  await guard.check(() => summary.click())
  await expect(review.locator('details.more')).toHaveJSProperty('open', true)
  // Choices inside Details change the facts line in place; nothing above them moves.
  const after = review.locator('#after-connecting')
  await guard.check(() => after.selectOption('paused'))
  await guard.check(() => after.selectOption('use'))
  const verify = review.locator('.verify input[type=checkbox]')
  if (await verify.isEnabled()) {
    await guard.check(() => verify.uncheck())
    await guard.check(() => verify.check())
  }
  // Leaving every harness out shows its reason in the reserved status line; nothing moves.
  const reason = review.locator('#connect-reason')
  const firstChoice = review.locator('.harness-row dd').first().locator('select, input[type=checkbox]').first()
  if (await firstChoice.evaluate(el => el.tagName === 'SELECT') && await review.locator('.harness-row').count() === 1) {
    const kept = await firstChoice.inputValue()
    await guard.check(() => firstChoice.selectOption(''))
    await expect(reason).not.toHaveText('')
    await guard.check(() => firstChoice.selectOption(kept))
    await expect(reason).toHaveText('')
  }
  await guard.check(() => summary.click())
  await expect(review.locator('details.more')).toHaveJSProperty('open', false)
  guard.done()
}

for (const [language, theme] of [['en', 'light'], ['de', 'light'], ['de', 'dark']] as const) {
  test(`AEON-1502 add one harness 1280x800 ${language} ${theme}: one sentence, Add or Deny, today's payload`, async ({ page }, testInfo) => {
    const errors = watchErrors(page)
    const copy = COPY[language]
    const { calls, review } = await open(page, language, addOne, undefined, theme)
    await expect(review.locator('[data-review-title]')).toHaveText(copy.addOne)
    await expect(review.locator('[data-review-title] .acct')).toHaveText(`(${ACCOUNT})`)
    await expect(review.locator('[data-review-title] .host')).toHaveText(HOST)
    await expect(review.locator('[data-review-facts]')).toHaveText(copy.factsOn)
    await expect(review.locator('[data-review-role]')).toHaveText(copy.role)
    await expect(review.locator('details.more')).toHaveJSProperty('open', false)
    await expect(review.locator('.actions .go')).toHaveText([copy.add, copy.deny])
    await expect(review.locator('.actions .btn.primary')).toHaveCount(1)
    const fit = await fitsWithoutScrolling(page, review)
    expect(fit.parts).toBe(6)
    expect(fit.scrolled).toBe(0)
    expect(fit.top).toBeGreaterThanOrEqual(0)
    expect(fit.bottom).toBeLessThanOrEqual(fit.viewport)
    expect(await sentences(review)).toEqual([copy.factsOn, copy.role])
    expect(await overflowing(review)).toEqual([])
    await shot(page, testInfo, `add-one-${language}-${theme}`)
    await detailsHold(page, review, copy.add)
    await review.locator('details.more > summary').click()
    expect(await overflowing(review)).toEqual([])
    await shot(page, testInfo, `add-one-details-${language}-${theme}`)
    await review.locator('details.more > summary').click()
    record(`add one ${language} ${theme}`, { viewport: '1280x800', fit, sentences: 2, detailsMovementPx: 0 })

    // One click on Add sends exactly the payload main sends today for this request:
    // the one account selected, verification on because Claude Code can be verified.
    await review.getByRole('button', { name: copy.add, exact: true }).click()
    await expect.poll(() => approvals(calls).length).toBe(1)
    expect(approvals(calls)[0]!.body).toEqual({ request_digest: DIGEST, verification: 'one_per_harness', selected_account_keys: ['claude-1'] })
    expect(errors.filter(error => !/not connected yet/.test(error))).toEqual([])
  })
}

test('AEON-1502 several harnesses 1280x800 en and de: one row per harness under the title', async ({ browser, page }, testInfo) => {
  await bothLanguages(browser, page, async (page, language) => {
    const copy = COPY[language]
    const { review } = await open(page, language, addTwo)
    await expect(review.locator('[data-review-title]')).toHaveText(copy.addTwo)
    await expect(review.locator('.who li')).toHaveText([/Claude Code\s*ada@example\.com/, /Codex\s*codex@example\.com/])
    const fit = await fitsWithoutScrolling(page, review)
    expect(fit.bottom).toBeLessThanOrEqual(fit.viewport)
    expect(await sentences(review)).toEqual([copy.factsOn, copy.role])
    expect(await overflowing(review)).toEqual([])
    await shot(page, testInfo, `add-two-${language}`)
    record(`several harnesses ${language}`, { viewport: '1280x800', fit })
  })
})

test("AEON-1502 first pairing 1280x800 en and de: Pair or Deny with the harness checklist, today's payload", async ({ browser, page }, testInfo) => {
  await bothLanguages(browser, page, async (page, language) => {
    const copy = COPY[language]
    const { calls, review } = await open(page, language)
    await expect(review.locator('[data-review-title]')).toHaveText(copy.pair)
    await expect(review.locator('.checklist input[type=checkbox]')).toHaveCount(2)
    for (const box of await review.locator('.checklist input[type=checkbox]').all()) await expect(box).toBeChecked()
    // Neither Cursor nor Codex can be verified: today's default is verification off.
    await expect(review.locator('[data-review-facts]')).toHaveText(copy.factsOff)
    await expect(review.locator('.actions .go')).toHaveText([copy.pairButton, copy.deny])
    const fit = await fitsWithoutScrolling(page, review)
    expect(fit.scrolled).toBe(0)
    expect(fit.bottom).toBeLessThanOrEqual(fit.viewport)
    expect(await sentences(review)).toEqual([copy.factsOff, copy.role])
    expect(await overflowing(review)).toEqual([])
    await shot(page, testInfo, `first-pairing-${language}`)
    await detailsHold(page, review, copy.pairButton)
    record(`first pairing ${language}`, { viewport: '1280x800', fit, sentences: 2, detailsMovementPx: 0 })

    await review.getByRole('button', { name: copy.pairButton, exact: true }).click()
    await expect.poll(() => approvals(calls).length).toBe(1)
    expect(approvals(calls)[0]!.body).toEqual({ request_digest: DIGEST, verification: 'connect_only', selected_account_keys: ['cursor-1', 'codex-1'] })
    // Today's default after connecting: agents may use both accounts.
    await expect.poll(() => calls.filter(call => call.path.endsWith('/capacity/approve')).length).toBe(2)
  })
})

test('AEON-1502 phone 390 en and de: Add and Deny full width, 44 px, no overflow', async ({ browser, page }, testInfo) => {
  await bothLanguages(browser, page, async (page, language) => {
    const copy = COPY[language]
    const { review } = await open(page, language, addOne, { width: 390, height: 844 })
    const add = review.getByRole('button', { name: copy.add, exact: true }), deny = review.getByRole('button', { name: copy.deny, exact: true })
    const inner = await review.locator('.actions').evaluate(el => el.getBoundingClientRect().width)
    for (const button of [add, deny]) {
      const box = (await button.boundingBox())!
      expect(box.height).toBeGreaterThanOrEqual(44)
      expect(Math.abs(box.width - inner)).toBeLessThanOrEqual(0.5)
    }
    expect((await add.boundingBox())!.y).toBeLessThan((await deny.boundingBox())!.y)
    expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(0)
    expect(await overflowing(review)).toEqual([])
    await shot(page, testInfo, `add-one-390-${language}`)
    await detailsHold(page, review, copy.add)
    await review.locator('details.more > summary').click()
    expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(0)
    expect(await overflowing(review)).toEqual([])
    record(`phone 390 ${language}`, { addAndDenyFullWidth: true, minHeight: 44, horizontalOverflow: 0 })
  })
})

test("AEON-1502 expired and denied 1280 en and de: one sentence and today's command", async ({ browser, page }, testInfo) => {
  await bothLanguages(browser, page, async (page, language) => {
    const copy = COPY[language]
    const expired = await open(page, language, { ...addOne, state: 'expired' })
    await expect(expired.review.locator('[data-review-title]')).toHaveText(copy.expired)
    await expect(expired.review.locator('[data-review-next]')).toHaveText(copy.expiredNext)
    await expect(expired.review.locator('[data-review-next] code')).toHaveText('aeon-agentd add-harness --harness claude')
    await expect(expired.review.getByRole('button', { name: copy.add, exact: true })).toHaveCount(0)
    await shot(page, testInfo, `expired-${language}`)

    await page.getByRole('button', { name: language === 'de' ? 'Anderen Code verwenden' : 'Use a different code' }).click()
    await page.unroute('**/api/agent-pairing/**')
    const calls = await mockPairing(page)
    await page.getByLabel('Pairing code').fill('123-456-789')
    await page.getByRole('button', { name: 'Look up code' }).click()
    await page.getByRole('button', { name: copy.deny, exact: true }).click()
    await expect.poll(() => calls.some(call => call.path.endsWith('/deny'))).toBe(true)
    const review = page.getByRole('region', { name: 'Pairing review' })
    await expect(review.locator('[data-review-title]')).toHaveText(copy.denied)
    await expect(review.locator('[data-review-next]')).toHaveText(copy.deniedNext)
    await shot(page, testInfo, `denied-${language}`)
    record(`expired and denied ${language}`, { expired: copy.expired, denied: copy.denied })
  })
})

test('AEON-1502 a harness that is not signed in shows disabled and is never checked', async ({ page }) => {
  const { calls, review } = await open(page, 'en', {
    requested_accounts: [{ account_key: 'cursor-1', harness: 'cursor', label: 'Cursor work' }, claude],
    harness_statuses: { claude: 'login_required' },
  })
  const row = review.locator('.checklist li').filter({ hasText: 'Claude Code' })
  await expect(row).toContainText('Not signed in')
  await expect(row.getByRole('checkbox')).toBeDisabled()
  await expect(row.getByRole('checkbox')).not.toBeChecked()
  await review.getByRole('button', { name: 'Pair', exact: true }).click()
  await expect.poll(() => approvals(calls).length).toBe(1)
  expect((approvals(calls)[0]!.body as { selected_account_keys: string[] }).selected_account_keys).toEqual(['cursor-1'])
})
