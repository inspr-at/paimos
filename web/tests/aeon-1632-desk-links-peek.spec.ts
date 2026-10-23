// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1632. Risk: a ticket link in the Desk memo leads to the dead /work/<id> route (404), or opening the
// ticket leaves the Desk item so the chosen answer and the typed P.S. are lost. Every link kind (Re key, the
// ticket line's key and Open, Related, the outcome effect's ticket and criterion) opens the ticket as a peek
// over the memo; Esc returns to the same item with its answer as it was, and nothing in the memo moved.
// Esc while one of the ticket's menus is open closes that menu and leaves the ticket up; the next Esc
// returns to the same memo and draft. A ticket this workspace cannot open stays plain and says why.
import { expect, test, type Locator, type Page } from '@playwright/test'
import type { Question } from '../src/lib/decisionDeskApi'
import { mockEffectivePermissions } from './authz-fixtures'
import { fixtures, mockWork } from './work-fixtures'

const OPEN = 'Which migration should carry the index?'
const DECIDED = 'Should the successor continue the review?'
const PS = 'Bitte vor Freitag mit der Mandantentrennung abgleichen.'

function question(id: string, title: string, decided: boolean): Question {
  const asked = new Date().toISOString()
  return {
    id, project_id: 'p-aeon', revision: decided ? 2 : 1, state: decided ? 'answered' : 'open', suggested_outcome: 'once', suggestion_reason: 'ticket_default',
    created_at: asked, updated_at: asked,
    input: { request_id: id, question: title, context: 'The ticket asks for fast lookup without changing tenant isolation.', findings: '',
      options: [{ id: 'partial', title: 'Use the partial index', description: 'Keep the common path small.', answer: 'Use the partial index.' }, { id: 'full', title: 'Use a full index', description: 'Cover every row.', answer: 'Use a full index.' }],
      recommend: 'partial', why: 'It matches the query that runs most often.', meanwhile: 'parked', meanwhile_text: 'The agent is writing the read tests.', ticket_id: 'n-a1' },
    outcomes: [{ outcome: 'once', available: true, why: '' }, { outcome: 'always', available: true, why: '' }, { outcome: 'requirement', available: true, why: '' }, { outcome: 'doctrine', available: false, why: 'Doctrine needs an authorized source, rule and exact base mapping.' }],
    askers: [{ id: 'asker-1', principal_id: 'agent-1', reply_root_id: 'root-1', comment_node_id: 'n-a1', input: { request_id: id, question: 'Which index?', options: [], meanwhile: 'parked' } }],
    ...(decided ? {
      answer: { id: 'answer-2', revision: 2, answer: 'Use the partial index.', option_id: 'partial', outcome: 'requirement' as const, decided_by: 'person', created_at: asked, deliver_after: asked },
      pending: [{ id: 'outcome-1', revision: 2, kind: 'outcome' as const, state: 'failed' as const, deliver_after: '', error_message: 'The earlier criterion was edited.',
        effect_data: { retryable: false, ticket_id: 'n-a1', review_required: [
          { kind: 'criterion' as const, ref: 'n-4', why: 'The earlier criterion needs a person’s review.' },
          { kind: 'criterion' as const, ref: 'n-gone', why: 'A criterion on a ticket that is gone.' },
        ] } }],
    } : { pending: [] }),
  }
}

// The Desk with one open and one decided question about AEON-1, which relates to PHAROS-15. Not the shared Desk
// fixture: its importers sit at the tier planner's browser fan-out bound (see aeon-1535-desk-phone.spec.ts).
async function mockDesk(page: Page, theme: 'light' | 'dark') {
  const data = fixtures()
  data.projects.find(project => project.id === 'p-aeon')!.title = 'Paimos Aeon'
  data.preferences.theme = { choice: theme }
  data.relations.push({ id: 'r-1632', source_node_id: 'n-a1', target_node_id: 'n-5', type: 'relates', created_at: new Date().toISOString() })
  await mockWork(page, data)
  const open = question('question-1', OPEN, false), decided = question('question-2', DECIDED, true)
  const empty = { items: [], has_more: false }
  await page.route('**/api/**', route => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname, method = request.method()
    if (path === '/api/decision-desk/projection') return route.fulfill({ json: { items: [{ id: open.id, kind: 'question', project_id: open.project_id, revision: open.revision, title: OPEN, created_at: open.created_at, held: true, href: `/decision-desk?item=q:${open.id}`, source: `/api/questions/${open.id}` }], counts: { open: 1, held: 1, chores: 0 }, has_more: false, as_of: new Date().toISOString() } })
    if (path === '/api/decision-desk') return route.fulfill({ json: { items: url.searchParams.get('state') === 'answered' ? [decided] : [open], has_more: false } })
    if (path.startsWith('/api/questions/') && method === 'GET') return route.fulfill({ json: path.endsWith(decided.id) ? decided : open })
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
    if (path === '/api/nodes/n-gone') return route.fulfill({ status: 404, json: { error: 'Not found' } })
    return route.fallback()
  })
}

const box = async (locator: Locator) => { const b = (await locator.boundingBox())!; return { x: b.x, y: b.y, width: b.width, height: b.height } }
function near(after: { x: number; y: number; width: number; height: number }, before: typeof after, name: string) {
  for (const side of ['x', 'y', 'width', 'height'] as const) expect(Math.abs(after[side] - before[side]), `${name} ${side} moved`).toBeLessThanOrEqual(0.5)
}

// Opens the link's peek over the memo and closes it with Esc; the Desk item, its answer and the memo's controls stay.
async function peekAndBack(page: Page, link: Locator, ticket: { key: string; title: string; href: string }, item: { heading: string; check: () => Promise<void> }, shot?: string) {
  await expect(link).toHaveAttribute('href', ticket.href)
  await link.scrollIntoViewIfNeeded()
  const address = page.url(), decide = page.getByTestId('desk-decide'), frame = page.getByTestId('desk-frame')
  const before = { decide: await box(decide), linkBox: await box(link) }
  await link.click()
  const peek = page.getByTestId('desk-peek')
  await expect(peek).toBeVisible()
  await expect(peek).toContainText(ticket.key)
  await expect(peek).toContainText(ticket.title)
  if (shot) { await page.evaluate(() => document.fonts.ready); await page.screenshot({ path: shot }) }
  // The memo stays mounted under the peek, and the address does not change (any change closes the memo).
  expect(page.url()).toBe(address)
  await expect(frame).toBeAttached()
  await page.keyboard.press('Escape')
  await expect(peek).toHaveCount(0)
  await expect(page.getByTestId('desk-paper').getByRole('heading', { level: 2 })).toHaveText(item.heading)
  await expect(link).toBeFocused()
  await item.check()
  near(await box(decide), before.decide, 'decide')
  near(await box(link), before.linkBox, 'link')
}

for (const width of [1440, 390]) for (const theme of ['light'] as const) {
  test(`AEON-1632: Desk ticket links open the peek and Esc returns to the same item ${width} ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: width < 600 ? 844 : 1000 })
    await mockDesk(page, theme)
    await page.goto('/decision-desk?item=q:question-1')
    await expect(page.getByTestId('desk-paper').getByRole('heading', { level: 2 })).toHaveText(OPEN)
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
    await expect(page.getByTestId('desk-decide')).toBeEnabled()
    // The person's answer in progress: the second option and a typed P.S.
    await page.getByTestId('choice-1').click()
    const note = page.getByRole('textbox', { name: 'P.S. for the agent' })
    await note.fill(PS); await note.press('Enter')
    const answerKept = async () => {
      await expect(page.getByTestId('choice-1')).toHaveAttribute('aria-checked', 'true')
      await expect(page.getByTestId('choice-0')).toHaveAttribute('aria-checked', 'false')
      await expect(note).toHaveValue(PS)
      await expect(page.getByTestId('desk-pager')).toHaveCount(0)
    }
    const aeon1 = { key: 'AEON-1', title: 'Aeon foundation', href: '/p/AEON/AEON-1' }
    const openItem = { heading: OPEN, check: answerKept }
    await peekAndBack(page, page.getByTestId('desk-re-link'), aeon1, openItem)
    await peekAndBack(page, page.getByTestId('desk-ticket-key'), aeon1, openItem, testInfo.outputPath(`aeon1632-peek-${width}-${theme}.png`))
    await peekAndBack(page, page.getByTestId('desk-ticket-open'), aeon1, openItem)
    await peekAndBack(page, page.getByTestId('desk-related-link'), { key: 'PHAROS-15', title: 'Beacon health probes', href: '/p/PHAROS/PHAROS-15' }, openItem)
    await page.screenshot({ path: testInfo.outputPath(`aeon1632-open-${width}-${theme}.png`) })

    // A decided item: the outcome effect names its tickets by id; each opens as a peek.
    await page.goto('/decision-desk?view=decided&item=q:question-2')
    await expect(page.getByTestId('desk-paper').getByRole('heading', { level: 2 })).toHaveText(DECIDED)
    const stamped = async () => { await expect(page.getByTestId('desk-stamp')).toHaveAttribute('aria-label', 'Stamped Requirement') }
    const decidedItem = { heading: DECIDED, check: stamped }
    const effects = page.getByTestId('desk-outcome-effects')
    await peekAndBack(page, effects.getByRole('link', { name: 'Open the ticket' }), aeon1, decidedItem)
    await peekAndBack(page, effects.getByRole('link', { name: 'Open the criterion to review' }), { key: 'PHAROS-14', title: 'Visual acceptance of the version pill', href: '/p/PHAROS/PHAROS-14' }, decidedItem)
    // A ticket this workspace cannot read: no link, and the reason in its tip.
    const gone = effects.getByTestId('desk-effect-criterion').filter({ hasNotText: 'Open the criterion' })
    await expect(gone).toHaveText('The ticket')
    await expect(gone).toHaveAttribute('data-tip', 'This ticket isn’t one you can open in this workspace')
    await expect(effects.locator('a[href^="/work/"]')).toHaveCount(0)
    await page.screenshot({ path: testInfo.outputPath(`aeon1632-decided-${width}-${theme}.png`) })
  })
}

test('AEON-1632: Escape closes an open peek menu before the ticket and keeps the memo draft', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await mockDesk(page, 'light')
  await page.goto('/decision-desk?item=q:question-1')
  await expect(page.getByTestId('desk-paper').getByRole('heading', { level: 2 })).toHaveText(OPEN)
  await page.getByTestId('choice-1').click()
  const note = page.getByRole('textbox', { name: 'P.S. for the agent' })
  await note.fill(PS)
  await note.press('Enter')
  const address = page.url()
  await page.getByTestId('desk-ticket-open').click()
  const peek = page.getByTestId('desk-peek')
  await expect(peek).toContainText('AEON-1')
  await expect(peek).toContainText('Aeon foundation')
  const status = peek.getByRole('button', { name: /Status:/ })
  await expect(status).toBeEnabled()
  await status.click()
  const menu = page.getByRole('menu', { name: 'Status of AEON-1' })
  await expect(menu).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(menu).toHaveCount(0)
  await expect(peek).toBeVisible()
  await expect(peek).toContainText('Aeon foundation')
  await expect(page.getByTestId('choice-1')).toHaveAttribute('aria-checked', 'true')
  await expect(note).toHaveValue(PS)
  await page.keyboard.press('Escape')
  await expect(peek).toHaveCount(0)
  expect(page.url()).toBe(address)
  await expect(page.getByTestId('desk-paper').getByRole('heading', { level: 2 })).toHaveText(OPEN)
  await expect(page.getByTestId('choice-1')).toHaveAttribute('aria-checked', 'true')
  await expect(page.getByTestId('choice-0')).toHaveAttribute('aria-checked', 'false')
  await expect(note).toHaveValue(PS)
})

test('AEON-1632: the peek opens the ticket’s full page at its canonical address', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await mockDesk(page, 'light')
  await page.goto('/decision-desk?item=q:question-1')
  await expect(page.getByTestId('desk-decide')).toBeEnabled()
  await page.getByTestId('desk-ticket-key').click()
  const peek = page.getByTestId('desk-peek')
  await expect(peek).toContainText('Aeon foundation')
  await peek.getByRole('button', { name: 'Open in project' }).click()
  await expect(page).toHaveURL(/\/p\/AEON\/AEON-1$/)
})
