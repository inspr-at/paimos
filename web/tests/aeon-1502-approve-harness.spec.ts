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
// A US-English browser in UTC: the German pass proves its 24 h clock against a 12 h browser (Tick round 1).
const BROWSER = { locale: 'en-US', timezoneId: 'UTC' } as const
test.use(BROWSER)
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
    accountLabel: 'Claude Code account',
    // The fixture's verification ends 2026-09-27T20:30Z; the clock stands at 20:00Z.
    deadline: 'until\u00a08:30\u00a0PM',
    units: ['60\u00a0seconds', '1\u00a0request', '1\u00a0run'],
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
    accountLabel: 'Konto für Claude Code',
    deadline: 'bis\u00a020:30',
    units: ['max.\u00a060\u00a0Sekunden', '1\u00a0Anfrage', '1\u00a0Lauf'],
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
  const context = await browser.newContext({ baseURL: test.info().project.use.baseURL, reducedMotion: 'reduce', ...BROWSER })
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

/** The first limits line, and how many lines each number-and-unit pair spans in it. */
async function limitsLine(review: Locator, pairs: readonly string[]) {
  return review.locator('.verify .limits').first().evaluate((el, pairs) => {
    const node = el.firstChild!, text = node.textContent ?? ''
    const lines = pairs.map(pair => {
      const at = text.indexOf(pair)
      if (at < 0) return 0
      const range = document.createRange()
      range.setStart(node, at)
      range.setEnd(node, at + pair.length)
      return new Set([...range.getClientRects()].map(rect => Math.round(rect.top))).size
    })
    return { text, lines }
  }, pairs)
}

const MONO = /monospace/

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
    await expect(review.locator('[data-review-title] .host')).toHaveCSS('font-family', MONO)
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
    // Tick round 1: the language's own clock (German 24 h, never AM/PM), units bound to their numbers, "Konto für".
    await expect(review.locator('.harness-row dt')).toHaveText(copy.accountLabel)
    const limits = await limitsLine(review, copy.units)
    expect(limits.text.endsWith(copy.deadline), limits.text).toBe(true)
    if (language === 'de') expect(limits.text).not.toMatch(/AM|PM/)
    expect(limits.lines).toEqual(copy.units.map(() => 1))
    await shot(page, testInfo, `add-one-details-${language}-${theme}`)
    await review.locator('details.more > summary').click()
    record(`add one ${language} ${theme}`, { viewport: '1280x800', fit, sentences: 2, detailsMovementPx: 0, limits: limits.text, unitLines: limits.lines })

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
    await expect(review.locator('.harness-list li')).toHaveText([/Claude Code\s*ada@example\.com/, /Codex\s*codex@example\.com/])
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
    // AEON-1611: the taller readable computer list must not retain the guide's
    // scroll offset or leave keyboard focus on the form that just disappeared.
    await expect(review.locator('[data-review-title]')).toBeFocused()
    const fit = await fitsWithoutScrolling(page, review)
    expect(fit.scrolled).toBe(0)
    expect(fit.bottom).toBeLessThanOrEqual(fit.viewport)
    expect(await sentences(review)).toEqual([copy.factsOff, copy.role])
    expect(await overflowing(review)).toEqual([])
    const boxes = review.locator('.checklist input[type=checkbox]')
    for (const box of await boxes.all()) await expect(box).toBeInViewport({ ratio: 1 })
    const checklistGuard = await controlStability(page, {
      checklist: review.locator('.checklist'),
      cursor: boxes.first(), codex: boxes.last(),
      pair: review.getByRole('button', { name: copy.pairButton, exact: true }),
      deny: review.getByRole('button', { name: copy.deny, exact: true }),
      details: review.locator('details.more > summary'),
    })
    await checklistGuard.check(() => boxes.last().uncheck())
    await expect(boxes.last()).not.toBeChecked()
    await checklistGuard.check(() => boxes.last().check())
    await expect(boxes.last()).toBeChecked()
    checklistGuard.done()
    // Deliver the repaint after scrolling with the installed browser clock.
    await page.clock.runFor(32)
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

// Fails on 39ea42fe: the checklist carried .who, whose phone touch overlay (base.css) lay over its checkboxes.
test('AEON-1502 phone 390: a pointer reaches each harness checkbox and toggles it', async ({ page }) => {
  const { calls, review } = await open(page, 'en', {}, { width: 390, height: 844 })
  const boxes = review.locator('.checklist input[type=checkbox]')
  await expect(boxes).toHaveCount(2)
  // The element under each checkbox's centre is the checkbox itself, not an overlay.
  for (const box of await boxes.all()) {
    expect(await box.evaluate(el => {
      const r = el.getBoundingClientRect()
      return document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2) === el
    })).toBe(true)
  }
  // A tap on the harness name toggles it through its label, off and on again.
  const codex = review.locator('.checklist li').filter({ hasText: 'Codex' })
  await codex.locator('.name').click()
  await expect(codex.getByRole('checkbox')).not.toBeChecked()
  await codex.locator('.name').click()
  await expect(codex.getByRole('checkbox')).toBeChecked()
  await codex.getByRole('checkbox').click()
  await expect(codex.getByRole('checkbox')).not.toBeChecked()
  await review.getByRole('button', { name: 'Pair', exact: true }).click()
  await expect.poll(() => approvals(calls).length).toBe(1)
  expect((approvals(calls)[0]!.body as { selected_account_keys: string[] }).selected_account_keys).toEqual(['cursor-1'])
})

test("AEON-1502 expired and denied 1280 en and de: one sentence and today's command", async ({ browser, page }, testInfo) => {
  await bothLanguages(browser, page, async (page, language) => {
    const copy = COPY[language]
    const expired = await open(page, language, { ...addOne, state: 'expired' })
    await expect(expired.review.locator('[data-review-title]')).toHaveText(copy.expired)
    await expect(expired.review.locator('[data-review-next]')).toHaveText(copy.expiredNext)
    await expect(expired.review.locator('[data-review-next] code')).toHaveText('aeon-agentd add-harness --harness claude')
    // The computer is set in mono in the sentence and the next step, as in the ask (Tick round 1).
    for (const where of ['[data-review-title]', '[data-review-next]']) {
      await expect(expired.review.locator(`${where} .host`)).toHaveText(HOST)
      await expect(expired.review.locator(`${where} .host`)).toHaveCSS('font-family', MONO)
    }
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
    for (const where of ['[data-review-title]', '[data-review-next]']) {
      await expect(review.locator(`${where} .host`)).toHaveText('studio')
      await expect(review.locator(`${where} .host`)).toHaveCSS('font-family', MONO)
    }
    await shot(page, testInfo, `denied-${language}`)
    record(`expired and denied ${language}`, { expired: copy.expired, denied: copy.denied, computerMono: true })
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
