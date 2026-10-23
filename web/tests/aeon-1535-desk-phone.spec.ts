// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1535 (Tick's spec v1, Markus: "Full-width rows"). Risk: on a phone the stamp preview squeezes the question
// into a narrow column, the meta reads as mono-caps labels, the stamp choices are small chips whose words cannot be
// read, choosing a stamp moves the rows, an unavailable stamp hides its reason, or the phone pass leaks into the
// 1440 desk.
import { expect, test, type Locator, type Page } from '@playwright/test'
import { writeFile } from 'node:fs/promises'
import type { Question } from '../src/lib/decisionDeskApi'
import { mockEffectivePermissions } from './authz-fixtures'
import { expectStableControls } from './helpers/stable'
import { fixtures, mockWork } from './work-fixtures'

const QUESTION = 'Which migration should carry the index?'
const OUTCOMES = ['once', 'always', 'requirement', 'doctrine'] as const
const DOCTRINE_OFF = 'Not available: the agent didn’t name the rule this answer would change, so there is nothing to propose a change to.'
const heading = (page: Page) => page.getByTestId('desk-paper').getByRole('heading', { level: 2 })
const row = (page: Page, outcome: string) => page.getByTestId(`stamp-${outcome}`)
const box = async (locator: Locator) => (await locator.boundingBox())!

// P1: a question with a recommended option and every stamp available, asked across tickets (Tick's card: no ticket,
// no background, the agent carries on). P2: the same question about AEON-1, whose Doctrine stamp is unavailable.
// Asked a moment ago on the real clock the page also reads ("just now"); no fixed calendar instant.
function question(state: 'P1' | 'P2'): Question {
  const p1 = state === 'P1', asked = new Date().toISOString()
  return {
    id: 'question-1', project_id: 'p-aeon', revision: 1, state: 'open', suggested_outcome: 'once', suggestion_reason: p1 ? 'project_default' : 'ticket_default',
    created_at: asked, updated_at: asked,
    input: { request_id: 'question-1', question: QUESTION, context: p1 ? '' : 'The ticket asks for fast lookup without changing tenant isolation.', findings: '',
      options: [{ id: 'partial', title: 'Use the partial index', description: 'Keep the common path small.', answer: 'Use the partial index.' }, { id: 'full', title: 'Use a full index', description: 'Cover every row.', answer: 'Use a full index.' }],
      recommend: 'partial', why: 'It matches the query that runs most often.',
      ...(p1 ? { meanwhile: 'carries_on' as const, doctrine: { source_id: 'source-1', path: 'docs/AGENTS', rule_key: 'checks', rule_sha256: 'a'.repeat(64) } } : { meanwhile: 'parked' as const, meanwhile_text: 'The agent is writing the read tests.', ticket_id: 'n-a1' }) },
    outcomes: [{ outcome: 'once', available: true, why: '' }, { outcome: 'always', available: true, why: '' }, { outcome: 'requirement', available: true, why: '' },
      p1 ? { outcome: 'doctrine', available: true, why: '', mapping_present: true } : { outcome: 'doctrine', available: false, why: 'Doctrine needs an authorized source, rule and exact base mapping.', mapping_present: false }],
    askers: [{ id: 'asker-1', principal_id: 'agent-1', reply_root_id: 'root-1', comment_node_id: 'n-a1', input: { request_id: 'question-1', question: 'Which index?', options: [], meanwhile: 'parked' } }], pending: [],
  }
}
// A Desk holding this one question. The shared Desk fixture is not imported: its importers sit at the tier planner's
// 300-case browser fan-out bound, so four more cases would turn every change to it into a full run.
async function mockDesk(page: Page, theme: 'light' | 'dark', state: 'P1' | 'P2') {
  const data = fixtures()
  data.projects.find(project => project.id === 'p-aeon')!.title = 'Paimos Aeon'
  data.preferences.theme = { choice: theme }
  await mockWork(page, data)
  const open = question(state)
  const empty = { items: [], has_more: false }
  await page.route('**/api/**', route => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname, method = request.method()
    if (path === '/api/decision-desk/projection') return route.fulfill({ json: { items: [{ id: open.id, kind: 'question', project_id: open.project_id, revision: open.revision, title: open.input.question, created_at: open.created_at, held: open.input.meanwhile !== 'carries_on', href: `/decision-desk?item=q:${open.id}`, source: `/api/questions/${open.id}` }], counts: { open: 1, held: open.input.meanwhile !== 'carries_on' ? 1 : 0, chores: 0 }, has_more: false, as_of: new Date().toISOString() } })
    if (path === '/api/decision-desk') return route.fulfill({ json: { items: !url.searchParams.get('state') || url.searchParams.get('state') === 'open' ? [open] : [], has_more: false } })
    if (path === `/api/questions/${open.id}` && method === 'GET') return route.fulfill({ json: open })
    if (path === '/api/me/permissions') {
      const permissions = mockEffectivePermissions('admin', url.searchParams.get('project_id') ?? undefined)
      permissions.workspace.permissions.push('questions.read', 'questions.decide', 'rules.write', 'approvals.read')
      return route.fulfill({ json: permissions })
    }
    if (path === '/api/me/phone-approvals') return route.fulfill({ status: 404, json: { error: 'Not found' } })
    if (path === '/api/approvals') return route.fulfill({ json: [] })
    if (path === '/api/rules/doctrine/inbox') return route.fulfill({ json: { items: [], pending: 0 } })
    if ((path === '/api/stepup-requests' || path === '/api/key-trim-proposals') && method === 'GET') return route.fulfill({ json: empty })
    if (path === '/api/harness-sessions' && method === 'GET') return route.fulfill({ json: { items: [], next_cursor: null } })
    if (path.endsWith('/messages') && method === 'GET') return route.fulfill({ json: { items: [], next_after: 0 } })
    if (path === '/api/outcomes') return route.fulfill({ json: { outcomes: [] } })
    if (path === '/api/nodes/n-a1/attachments') return route.fulfill({ json: { items: [] } })
    return route.fallback()
  })
}
async function openQuestion(page: Page, options: { width: number; height: number; theme: 'light' | 'dark'; state: 'P1' | 'P2' }) {
  await page.unrouteAll({ behavior: 'ignoreErrors' })
  await page.setViewportSize({ width: options.width, height: options.height })
  await mockDesk(page, options.theme, options.state)
  await page.goto('/decision-desk?item=q:question-1')
  await expect(heading(page)).toHaveText(QUESTION)
  await expect(page.locator('html')).toHaveAttribute('data-theme', options.theme)
  // The permission read has landed: the stamps answer taps and keys.
  await expect(page.getByTestId('desk-decide')).toBeEnabled()
}
async function shot(page: Page, testInfo: { outputPath: (name: string) => string }, name: string) {
  await page.evaluate(() => document.fonts.ready)
  await page.screenshot({ path: testInfo.outputPath(`aeon1535-${name}.png`) })
}
// One rendered line of text: its box is no taller than its line height.
const oneLine = (locator: Locator) => locator.evaluate(element => element.getBoundingClientRect().height <= parseFloat(getComputedStyle(element).lineHeight) + 0.5)

test('AEON-1535 P1: the phone question card reads at 22 px and the four stamp rows are full width', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  for (const [width, height, theme] of [[390, 844, 'light'], [390, 844, 'dark'], [320, 568, 'light'], [320, 568, 'dark']] as const) {
    await openQuestion(page, { width, height, theme, state: 'P1' })
    const at = `${width} ${theme}`
    const title = heading(page), meta = page.getByTestId('desk-project-line'), line = page.getByTestId('desk-line'), goes = page.getByTestId('desk-answer-line')
    const head = page.getByTestId('desk-paper').locator('header.memo-head'), decision = page.locator('#desk-h-answer')

    // The title: 22/28 semibold, unclamped, with the full width of the sheet.
    expect(await title.evaluate(element => { const style = getComputedStyle(element); return [style.fontSize, style.lineHeight, style.fontWeight, style.webkitLineClamp || 'none'] }), at).toEqual(['22px', '28px', '600', 'none'])
    // The stamp preview sits on the line above, right-aligned and 32 px high, so nothing narrows the title.
    const stamp = page.getByTestId('desk-stamp'), slot = page.locator('.stamp-slot')
    await expect(stamp).toHaveText('Once')
    expect(await stamp.evaluate(element => (element as HTMLElement).offsetHeight), `${at}: stamp height`).toBe(32)
    const [titleBox, slotBox, headBox, stampBox] = [await box(title), await box(slot), await box(head), await box(stamp)]
    expect(slotBox.y + slotBox.height, `${at}: the stamp line ends above the title`).toBeLessThanOrEqual(titleBox.y + 0.5)
    expect(Math.abs(titleBox.width - headBox.width), `${at}: the title has the head's full width`).toBeLessThanOrEqual(0.5)
    expect(Math.abs(stampBox.x + stampBox.width - (headBox.x + headBox.width)), `${at}: the stamp is right-aligned`).toBeLessThanOrEqual(4)

    // The meta: icons with aria-labels in 14/20 muted ink, no mono-caps labels.
    for (const label of ['Project', 'From', 'About', 'Received']) await expect(meta.getByRole('img', { name: label, exact: true }), `${at}: ${label} icon`).toBeVisible()
    expect(await meta.innerText(), `${at}: no mono-caps labels`).not.toMatch(/PROJECT|FROM|\bRE\b|RECEIVED/)
    await expect(meta.locator('.mt')).toHaveCount(4)
    for (const label of await meta.locator('.mt').all()) await expect(label).toBeHidden()
    expect(await meta.evaluate(element => [getComputedStyle(element).fontSize, getComputedStyle(element).lineHeight]), `${at}: meta type`).toEqual(['14px', '20px'])
    await expect(page.getByTestId('desk-re')).toHaveText('Across tickets')
    // The meanwhile line stays at 15 px; the aside is one muted line in the head, without its eyebrow.
    await expect(line).toHaveText(/carries on meanwhile\.\s*$/)
    expect(await line.evaluate(element => getComputedStyle(element).fontSize)).toBe('15px')
    await expect(goes).toHaveText(/^Answer goes to .+’s inbox·Source\s*$/)
    await expect(goes.getByRole('link', { name: 'Source', exact: true })).toHaveAttribute('href', '/api/questions/question-1')
    await expect(page.getByText('The answer goes', { exact: true })).toBeHidden()
    await expect(page.locator('.desk-dlg aside.margin')).toBeHidden()

    // Tick's rhythm: title → meta 12, meta → meanwhile 12, meanwhile → answer line 8, → hairline 20, hairline → Your decision 16.
    const [metaBox, lineBox, goesBox, decisionBox] = [await box(meta), await box(line), await box(goes), await box(decision)]
    const hairline = headBox.y + headBox.height - 1
    const gaps = { 'title → meta': metaBox.y - (titleBox.y + titleBox.height), 'meta → meanwhile': lineBox.y - (metaBox.y + metaBox.height), 'meanwhile → answer line': goesBox.y - (lineBox.y + lineBox.height), 'answer line → hairline': hairline - (goesBox.y + goesBox.height), 'hairline → Your decision': decisionBox.y - (hairline + 1) }
    const wanted = { 'title → meta': 12, 'meta → meanwhile': 12, 'meanwhile → answer line': 8, 'answer line → hairline': 20, 'hairline → Your decision': 16 }
    for (const [gap, px] of Object.entries(wanted)) expect(Math.abs(gaps[gap as keyof typeof gaps] - px), `${at}: ${gap} is ${gaps[gap as keyof typeof gaps]}`).toBeLessThanOrEqual(0.5)

    // Four full-width rows of equal width, each at least 52 px, label and words on one line.
    const group = page.getByTestId('desk-stamps')
    await expect(group.getByRole('radio')).toHaveCount(4)
    const inner = await group.evaluate(element => element.clientWidth)
    const rows = await Promise.all(OUTCOMES.map(outcome => box(row(page, outcome))))
    for (const [n, rowBox] of rows.entries()) {
      expect(Math.abs(rowBox.width - inner), `${at}: ${OUTCOMES[n]} spans the group`).toBeLessThanOrEqual(0.5)
      expect(rowBox.height, `${at}: ${OUTCOMES[n]} height`).toBeGreaterThanOrEqual(52)
      expect(rowBox.x, `${at}: ${OUTCOMES[n]} left`).toBeCloseTo(rows[0]!.x, 0)
    }
    for (const outcome of OUTCOMES) {
      const label = row(page, outcome).locator('.st-label'), words = page.getByTestId(`stamp-words-${outcome}`)
      expect(await oneLine(label), `${at}: ${outcome} label on one line`).toBe(true)
      expect(await label.evaluate(element => element.scrollWidth <= element.clientWidth + 0.5), `${at}: ${outcome} label uncut`).toBe(true)
      expect(await oneLine(words), `${at}: ${outcome} words on one line`).toBe(true)
      expect(await words.evaluate(element => getComputedStyle(element).textOverflow)).toBe('ellipsis')
      await expect(row(page, outcome)).toHaveAccessibleName(outcome.charAt(0).toUpperCase() + outcome.slice(1))
      await expect(row(page, outcome)).toHaveAttribute('data-tip', /\S/)
    }
    await expect(page.getByTestId('stamp-words-once')).toHaveText('this question only')
    await expect(page.getByTestId('stamp-words-always')).toHaveText('answer it from now on')
    await expect(page.getByTestId('stamp-words-requirement')).toHaveText('add to the ticket')
    await expect(page.getByTestId('stamp-words-doctrine')).toHaveText('propose a rule')
    // The full line is each row's description and its tooltip.
    for (const [outcome, description] of [['once', /^For this question only\. The agent gets it; it stays in Decided\.$/], ['always', /^From now on\. When an agent asks the same again, .+ answers with this and does not ask you\.$/], ['requirement', /^Adds an acceptance criterion to the ticket\.$/], ['doctrine', /^Proposes a rule for .+; you approve it in Rules, as today\.$/]] as const) {
      await expect(row(page, outcome)).toHaveAccessibleDescription(description)
      expect(await row(page, outcome).getAttribute('data-tip')).toContain((await row(page, outcome).evaluate(element => document.getElementById(element.getAttribute('aria-describedby')!)!.textContent))!)
    }
    // At 320 the longer words ellipsize; the description above still says it in full.
    if (width === 320) expect(await page.getByTestId('stamp-words-always').evaluate(element => element.scrollWidth > element.clientWidth), 'Always ellipsizes at 320').toBe(true)
    // Chosen: a tint of the row's tone and a check at its end; the others are untinted, their check's place kept.
    await expect(row(page, 'once')).toHaveAttribute('aria-checked', 'true')
    expect(await row(page, 'once').evaluate(element => getComputedStyle(element).backgroundColor)).not.toBe('rgba(0, 0, 0, 0)')
    expect(await row(page, 'always').evaluate(element => getComputedStyle(element).backgroundColor)).toBe('rgba(0, 0, 0, 0)')
    await expect(row(page, 'once').locator('.st-check')).toBeVisible()
    for (const outcome of ['always', 'requirement', 'doctrine']) expect(await row(page, outcome).locator('.st-check').evaluate(element => getComputedStyle(element).visibility)).toBe('hidden')
    // No edge accents: the rows draw no side or top bar in a tone.
    for (const outcome of OUTCOMES) expect(await row(page, outcome).evaluate(element => { const style = getComputedStyle(element); return [style.borderLeftWidth, style.borderRightWidth, style.boxShadow] })).toEqual(['0px', '0px', 'none'])

    await shot(page, testInfo, `${width}-${theme}-P1`)
    await group.evaluate(element => element.scrollIntoView({ block: 'center' }))
    await shot(page, testInfo, `${width}-${theme}-P1-stamps`)
  }
})

test('AEON-1535 P2: an unavailable stamp row is disabled and reads its reason', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
    await openQuestion(page, { width, height: 1000, theme, state: 'P2' })
    const doctrine = row(page, 'doctrine'), words = page.getByTestId('stamp-words-doctrine')
    await expect(doctrine).toHaveAttribute('aria-disabled', 'true')
    await expect(doctrine).toBeDisabled()
    await expect(doctrine).toHaveAccessibleDescription(DOCTRINE_OFF)
    await expect(words).toHaveText(width === 390 ? 'Not available: the agent didn’t name the rule to change.' : DOCTRINE_OFF)
    await expect(page.getByTestId('stamp-words-requirement')).toHaveText('add to AEON-1')
    for (const outcome of ['once', 'always', 'requirement']) await expect(row(page, outcome)).not.toHaveAttribute('aria-disabled', /.*/)
    const controls = {
      decide: page.getByTestId('desk-decide'), skip: page.getByTestId('desk-skip'), stamps: page.getByTestId('desk-stamps'),
      once: row(page, 'once'), always: row(page, 'always'), doctrine,
    }
    await page.getByTestId('desk-stamps').scrollIntoViewIfNeeded()
    const before = await row(page, 'always').boundingBox()
    await expectStableControls({ controls, scrollAreas: { body: page.getByTestId('desk-body') }, interactions: [
      { name: 'focus the unavailable stamp', run: async () => {
        await doctrine.focus()
        if (width === 390) {
          await expect(doctrine).not.toHaveAttribute('data-tip', /.*/)
          await expect(page.getByRole('tooltip')).toHaveCount(0)
          const lines = await words.evaluate(el => el.getBoundingClientRect().height / parseFloat(getComputedStyle(el).lineHeight))
          expect(lines, 'the full inline reason wraps to two lines').toBe(2)
          expect(await words.evaluate(el => el.scrollWidth - el.clientWidth)).toBe(0)
        } else await expect(doctrine).toHaveAttribute('data-tip', DOCTRINE_OFF)
      } },
      { name: 'tap the unavailable stamp', run: async () => {
        await doctrine.click({ force: true })
        if (width === 390) await expect(page.locator('.dlg-toast')).toHaveText(DOCTRINE_OFF)
        await expect(row(page, 'once')).toHaveAttribute('aria-checked', 'true')
        await expect(doctrine).toHaveAttribute('aria-checked', 'false')
      } },
    ] })
    expect(await row(page, 'always').boundingBox(), 'disabled stamp focus moves its neighbour by 0 px').toEqual(before)
    await page.screenshot({ path: testInfo.outputPath(`AEON-1688/desk-${width}-${theme}.png`) })
  }
})

// A phone: touch input, so the tap is a real tap.
test.describe('touch', () => {
  test.use({ hasTouch: true })
  test('AEON-1535 P3: O, A, R and D and a tap choose the right row, and choosing moves nothing', async ({ page }, testInfo) => {
    test.setTimeout(60_000)
    await openQuestion(page, { width: 390, height: 844, theme: 'light', state: 'P1' })
    const checked = () => page.getByTestId('desk-stamps').getByRole('radio', { checked: true })
    const choose = (name: string, act: () => Promise<unknown>, outcome: string, word: string) => ({ name, run: async () => {
      await act()
      await expect(row(page, outcome)).toHaveAttribute('aria-checked', 'true')
      await expect(checked()).toHaveCount(1)
      await expect(page.getByTestId('desk-stamp')).toHaveText(word)
      await expect(row(page, outcome).locator('.st-check')).toBeVisible()
    } })
    const controls: Record<string, Locator> = {
      group: page.getByTestId('desk-stamps'), choices: page.getByTestId('desk-choices'), 'first choice': page.getByTestId('choice-row-0'),
      title: heading(page), decide: page.getByTestId('desk-decide'), skip: page.getByTestId('desk-skip'), close: page.getByTestId('desk-close'), tab: page.getByTestId('desk-tab'),
    }
    for (const outcome of OUTCOMES) { controls[outcome] = row(page, outcome); controls[`${outcome} label`] = row(page, outcome).locator('.st-label') }
    await expectStableControls({ controls, scrollAreas: { body: page.getByTestId('desk-body') }, interactions: [
      choose('key A', () => page.getByTestId('desk-paper').press('a'), 'always', 'Always'),
      choose('key R', () => page.keyboard.press('r'), 'requirement', 'Requirement'),
      choose('key D', () => page.keyboard.press('d'), 'doctrine', 'Doctrine'),
      choose('key O', () => page.keyboard.press('o'), 'once', 'Once'),
      choose('tap Always', () => row(page, 'always').tap(), 'always', 'Always'),
      choose('click Requirement', () => row(page, 'requirement').click(), 'requirement', 'Requirement'),
    ] })
    await page.getByTestId('desk-stamps').evaluate(element => element.scrollIntoView({ block: 'center' }))
    await shot(page, testInfo, '390-light-P3')
  })
})

// P4 risk: the phone pass leaks into the 1440 desk. The shapes the approved rev 11 desk draws stay: the 24 px title, mono-caps
// meta labels, the tilted stamp in its own 210 px column, four stamp chips on one line, the aside with "The answer goes".
test('AEON-1535 P4: the 1440 desk keeps its geometry', async ({ page }, testInfo) => {
  test.setTimeout(60_000)
  await openQuestion(page, { width: 1440, height: 1000, theme: 'light', state: 'P2' })
  const title = heading(page), meta = page.getByTestId('desk-project-line'), stamps = page.getByTestId('desk-stamps')
  expect(await title.evaluate(element => { const style = getComputedStyle(element); return [style.fontSize, style.lineHeight, style.fontWeight] })).toEqual(['24px', '30px', '500'])
  await expect(meta.getByRole('img', { name: /^(Project|From|About|Received)$/ })).toHaveCount(0)
  for (const label of await meta.locator('dt .mt').all()) {
    await expect(label).toBeVisible()
    expect(await label.evaluate(element => [getComputedStyle(element).textTransform, getComputedStyle(element).fontSize])).toEqual(['uppercase', '10px'])
  }
  expect(await meta.innerText()).toMatch(/PROJECT[\s\S]*FROM[\s\S]*RE[\s\S]*RECEIVED/)
  await expect(page.getByTestId('desk-answer-line')).toBeHidden()
  await expect(page.getByTestId('desk-answer-goes')).toBeVisible()
  await expect(page.getByText('The answer goes', { exact: true })).toBeVisible()
  // The stamp keeps its tilted slot to the right of the head text.
  const [slot, text, stamp] = [await box(page.locator('.stamp-slot')), await box(page.locator('.memo-headtext')), page.getByTestId('desk-stamp')]
  expect(slot.width).toBeCloseTo(210, 0)
  expect(slot.x).toBeGreaterThanOrEqual(text.x + text.width)
  expect(await stamp.evaluate(element => { const matrix = new DOMMatrix(getComputedStyle(element).transform); return Math.round(Math.atan2(matrix.b, matrix.a) * 180 / Math.PI) })).toBe(-8)
  // Four chips on one line, 36 px high, no row words or checks.
  const chips = await Promise.all(OUTCOMES.map(outcome => box(row(page, outcome))))
  for (const chip of chips) { expect(chip.y).toBeCloseTo(chips[0]!.y, 0); expect(chip.height).toBeCloseTo(36, 0); expect(chip.width).toBeLessThan(200) }
  for (const outcome of OUTCOMES) { await expect(page.getByTestId(`stamp-words-${outcome}`)).toBeHidden(); await expect(row(page, outcome).locator('.st-check')).toBeHidden() }
  await expect(row(page, 'doctrine')).toHaveAccessibleDescription(DOCTRINE_OFF)
  // Background stays beside the decision.
  const [answer, aside] = [await box(page.locator('.memo-answer')), await box(page.locator('aside.margin'))]
  expect(aside.x).toBeGreaterThan(answer.x + answer.width)
  // The geometry, for comparison with the same run on main.
  const geometry: Record<string, unknown> = {}
  for (const [name, locator] of Object.entries({ tab: page.getByTestId('desk-tab'), actions: page.getByTestId('desk-actions').locator('.action-buttons'), close: page.getByTestId('desk-close'), title, meta, stampSlot: page.locator('.stamp-slot'), line: page.getByTestId('desk-line'), stamps, ...Object.fromEntries(OUTCOMES.map(outcome => [outcome, row(page, outcome)])), stampDesc: page.getByTestId('desk-stamp-desc'), choices: page.getByTestId('desk-choices'), aside: page.locator('aside.margin') })) {
    const { x, y, width, height } = await box(locator)
    geometry[name] = [x, y, width, height].map(value => Math.round(value * 100) / 100)
  }
  await writeFile(testInfo.outputPath('aeon1535-1440-light-P4-geometry.md'), `\`\`\`json\n${JSON.stringify(geometry, null, 1)}\n\`\`\`\n`)
  await shot(page, testInfo, '1440-light-P4')
  await expectStableControls({
    controls: { tab: page.getByTestId('desk-tab'), title, meta, stampSlot: page.locator('.stamp-slot'), stamps, ...Object.fromEntries(OUTCOMES.map(outcome => [outcome, row(page, outcome)])) },
    interactions: (['always', 'requirement', 'once'] as const).map(outcome => ({ name: `stamp ${outcome}`, run: async () => { await row(page, outcome).click(); await expect(row(page, outcome)).toHaveAttribute('aria-checked', 'true') } })),
  })
})
