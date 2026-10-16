// SPDX-License-Identifier: AGPL-3.0-only
// Risk: the person cannot see the exact prepared write, decides another
// recommendation, or loses the native Undo while controls move on a phone.
// AEON-1385: the last lesson ends the round like every other kind: "All decided"
// with Undo for 1.2 s, then the toast with Undo for the rest of the grace.
import { test, expect, type Locator, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { mockDecisionDesk } from './decision-desk-fixtures'
import { knowledgeWorld, mockKnowledge } from './knowledge-fixtures'
import { controlStability } from './control-stability'
import type { MethodLearning } from '../src/lib/knowledge'

const id = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa'
const nativeId = `n-${id}`
const sentence = 'Rehearse recovery before releasing.'
const line = '- 2026-10-14 · Rehearse recovery before releasing. ([AEON-1](/p/AEON/AEON-1))'
async function mockLessons(page: Page, options: { theme?: 'light' | 'dark'; outdated?: boolean; rule?: boolean; noSource?: boolean; empty?: boolean; long?: boolean } = {}) {
  const state = { decided: false, revision: 700, fail: false, undoFail: false, crowded: false, calls: [] as { path: string; body: Record<string, unknown>; match?: string }[] }
  const lessonSentence = options.long ? 'Vor jeder Freigabe wird der vollständige Wiederherstellungsablauf mit den aktuellen Berechtigungen und den dokumentierten Prüfschritten überprüft.' : sentence
  const learning: MethodLearning = {
    id: nativeId, source: 'ticket', node_id: id, key: 'AEON-1', title: options.noSource ? '' : 'Recovery failed during release',
    text: 'The recovery check was missing.', href: options.noSource ? '' : '/p/AEON/AEON-1', author: null, at: '2026-10-14T08:00:00Z',
    recommendation: { decision: 'accept', lesson: lessonSentence, knowledge_id: options.rule ? 'rule-set' : 'k-aeon-deploy', knowledge_title: options.rule ? 'Recovery rules' : 'Ship an Aeon release',
      source_summary: 'The incident exposed an untested recovery path.', line: options.rule ? undefined : line,
      rule_layer_id: options.rule ? 'rule-layer' : undefined, rule_text: options.rule ? sentence : undefined,
      by: { id: 'scout', name: 'Scout' }, at: '2026-10-14T08:30:00Z', event_id: 700, prepared: true, stale: false, target_missing: false },
  }
  const row = () => ({ id, kind: 'lesson' as const, project_id: 'p-aeon', revision: state.revision, title: lessonSentence, created_at: learning.recommendation!.at,
    held: false, can_decide: true, href: `/decision-desk?item=l:${id}`, source: `/api/knowledge/learnings/${nativeId}` })
  await mockDecisionDesk(page, { theme: options.theme })
  const world = knowledgeWorld(), calls = await mockKnowledge(page, world)
  const target = world.entries.find(entry => entry.id === 'k-aeon-deploy')!
  if (options.long) {
    target.title = 'Freigabe und Wiederherstellung mit dokumentierten Berechtigungsprüfungen für alle beteiligten Arbeitsbereiche'
    learning.title = 'Die Wiederherstellung schlug wegen einer fehlenden Berechtigungsprüfung während der Freigabe fehl'
    learning.recommendation!.knowledge_title = target.title
    learning.recommendation!.line = `- 2026-10-14 · ${lessonSentence} ([AEON-1](/p/AEON/AEON-1))`
  }
  target.updated_at = options.outdated ? '2026-10-14T09:00:00Z' : '2026-10-13T08:00:00Z'
  await page.route('**/api/decision-desk/projection*', route => {
    const lessons = state.decided || options.empty ? 0 : 1
    if (state.crowded) {
      const offset = Number(new URL(route.request().url()).searchParams.get('cursor') || 0)
      const rows = Array.from({ length: 100 }, (_, at) => ({ ...row(), id: `question-${offset + at}`, kind: 'question', project_id: 'p-pharos' }))
      return route.fulfill({ json: { items: rows, counts: { open: 1001, held: 0, chores: 0, lessons: { 'p-aeon': lessons } }, has_more: true, next_cursor: String(offset + 100), as_of: '2026-10-14T10:00:00Z' } })
    }
    return route.fulfill({ json: { items: lessons ? [row()] : [], counts: { open: lessons, held: 0, chores: 0, lessons: { 'p-aeon': lessons } }, has_more: false, as_of: '2026-10-14T10:00:00Z' } })
  })
  await page.route('**/api/knowledge/learnings*', route => route.fulfill({ json: {
    items: state.decided || options.empty ? [] : [{ ...learning, recommendation: { ...learning.recommendation, event_id: state.revision } }], truncated: false,
  } }))
  if (options.rule) await page.route('**/api/nodes/rule-set', route => route.fulfill({ json: { id: 'rule-set', title: 'Recovery rules', updated_at: target.updated_at } }))
  await page.route('**/api/knowledge/learnings/*/*', async route => {
    const request = route.request(), path = new URL(request.url()).pathname, body = request.postDataJSON()
    state.calls.push({ path, body, match: request.headers()['if-match'] })
    if (state.fail || request.headers()['if-match'] !== `"${state.revision}"`) return route.fulfill({ status: 409, json: { code: 'learning_changed', error: 'This learning changed since you reviewed it. Review it again.' } })
    state.decided = true
    return route.fulfill({ json: { id: nativeId, decision: path.endsWith('/dismiss') ? 'dismissed' : path.endsWith('/draft') ? 'drafted' : 'accepted', event_id: 800,
      knowledge_id: learning.recommendation!.knowledge_id, line, rule_set_id: 'rule-set', rule_layer_id: 'rule-layer' } })
  })
  await page.route('**/api/events/800/undo', route => {
    state.calls.push({ path: '/api/events/800/undo', body: {} })
    if (state.undoFail) return route.fulfill({ status: 409, json: { error: 'Changed again' } })
    state.decided = false; return route.fulfill({ json: { event_id: 801 } })
  })
  return { state, calls }
}
// Pause the page clock just ahead of its own time: the round's 1.2 s and the grace then run only on runFor.
async function pause(page: Page) { const at = await page.evaluate(() => Date.now()) + 1000; await page.clock.pauseAt(at); return at }
const allDecidedUndo = (page: Page) => page.getByTestId('desk-all-decided').getByRole('button', { name: 'Undo', exact: true })
async function openLesson(page: Page) {
  await page.goto(`/decision-desk?kind=lesson&item=l:${id}`)
  await expect(page.getByRole('dialog', { name: 'Decision Desk memo' })).toBeVisible()
  await expect(page.getByTestId('desk-decide')).toBeEnabled()
}

for (const width of [1440, 1024, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`AEON-1331 lesson memo and row stay stable at ${width} ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 1000 }); await page.clock.install()
    const { state } = await mockLessons(page, { theme, outdated: true })
    await page.goto('/decision-desk?kind=lesson')
    const row = page.getByTestId(`desk-row-l:${id}`)
    await expect(row).toContainText('Lesson to adopt'); await expect(row).toContainText(sentence)
    await page.screenshot({ path: testInfo.outputPath('list-row.png') })
    await row.locator('.row-open').click(); await expect(page.getByTestId('desk-decide')).toBeEnabled()
    await expect(page.getByTestId('lesson-line')).toHaveText(line)
    await expect(page.getByRole('link', { name: 'From Recovery failed during release' })).toHaveAttribute('href', '/p/AEON/AEON-1')
    await expect(page.getByTestId('desk-lesson-details')).toContainText('Adds this line to Ship an Aeon release · Changelog')
    await expect(page.getByTestId('lesson-outdated')).toHaveText('The entry changed since this was prepared. Accept writes against the current version.')
    await page.screenshot({ path: testInfo.outputPath('outdated.png') })
    const guard = await controlStability(page, {
      decide: page.getByTestId('desk-decide'), skip: page.getByTestId('desk-skip'), tab: page.getByTestId('desk-tab'),
      choices: page.getByTestId('desk-choices'), accept: page.getByTestId('choice-0'), dismiss: page.getByTestId('choice-1'),
      clickedRow: page.getByTestId('choice-row-1'), ...(width === 390 ? { frame: page.getByTestId('desk-frame') } : {}),
    })
    await guard.check(() => page.getByTestId('choice-1').click())
    await expect(page.getByLabel('Why not? (optional)')).toBeVisible()
    await guard.check(() => page.getByTestId('choice-0').click())
    await guard.check(() => page.getByTestId('choice-1').click())
    guard.done()
    // AEON-1385: the one lesson ends the round. "All decided" keeps the actions in place, and its
    // Undo restores the lesson open again with the same actions.
    await pause(page)
    const actions = await controlStability(page, {
      decide: page.getByTestId('desk-decide'), tab: page.getByTestId('desk-tab'), close: page.getByTestId('desk-close'),
      ...(width === 390 ? { frame: page.getByTestId('desk-frame') } : {}),
    })
    await actions.check(async () => { await page.getByTestId('desk-decide').click(); await expect(allDecidedUndo(page)).toBeVisible() })
    await expect(page.getByTestId('desk-all-decided')).toContainText('1 lesson decided.')
    expect(state.calls[0]!.path).toContain('/dismiss'); expect(state.calls[0]!.body).toEqual({ learning_text: 'The recovery check was missing.' })
    await page.screenshot({ path: testInfo.outputPath('dismissed.png') })
    await actions.check(async () => { await allDecidedUndo(page).click(); await expect(page.getByTestId('choice-0')).toBeEnabled() })
    await expect(page.getByTestId('desk-status')).toHaveText('Undone.'); expect(state.decided).toBe(false)
    await page.getByTestId('choice-0').click()
    await actions.check(async () => { await page.getByTestId('desk-decide').click(); await expect(allDecidedUndo(page)).toBeVisible() })
    expect(state.calls[2]!.path).toContain('/accept'); expect(state.calls[2]!.match).toBe('"700"')
    expect(state.calls[2]!.body).toEqual({ knowledge_id: 'k-aeon-deploy', lesson: sentence, learning_text: 'The recovery check was missing.' })
    await page.screenshot({ path: testInfo.outputPath('accepted-with-undo.png') }); actions.done()
  })
  test(`AEON-1331 pending lesson and Knowledge link at ${width} ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 1000 }); const { state } = await mockLessons(page, { theme, long: true })
    await openLesson(page); await expect(page.getByTestId('lesson-outdated')).toHaveCount(0)
    await page.screenshot({ path: testInfo.outputPath('pending.png') })
    await page.getByTestId('desk-close').click(); state.crowded = true; await page.goto('/p/AEON/knowledge')
    await expect(page.getByRole('heading', { name: 'Method learnings' })).toHaveCount(0)
    const link = page.getByRole('link', { name: '1 lesson waits on the Decision Desk', exact: true })
    await expect(link.locator('svg')).toHaveCount(1)
    await expect(link).toHaveAttribute('href', '/decision-desk?kind=lesson&project=p-aeon')
    const search = page.getByRole('searchbox', { name: 'Search knowledge in Paimos Aeon' })
    const guard = await controlStability(page, { search, newEntry: page.getByRole('button', { name: 'New knowledge entry' }) })
    await guard.check(() => search.fill('Ship')); guard.done()
    await page.screenshot({ path: testInfo.outputPath('knowledge-with-lessons.png') })
    state.crowded = false
    await link.click(); await expect(page).toHaveURL(/kind=lesson&project=p-aeon/)
    await expect(page.locator('.desk-row')).toHaveCount(1)
  })
  test(`AEON-1331 Knowledge without lessons at ${width} ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 1000 }); await mockLessons(page, { theme, empty: true })
    await page.goto('/p/AEON/knowledge'); await expect(page.locator('.k-frame')).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Method learnings' })).toHaveCount(0)
    await expect(page.getByRole('link', { name: /on the Decision Desk/ })).toHaveCount(0)
    await page.screenshot({ path: testInfo.outputPath('knowledge-without-lessons.png') })
  })
}
// Tick at 33f0a013 read "1 lessons wait on the Decision Desk" with its chevron alone on a
// second line: base.css renders every SVG as a block. Fails on e27c019ac at every width.
async function lessonLine(link: Locator) {
  return link.evaluate(el => {
    const icon = el.querySelector('svg')!, word = document.createRange(); word.selectNodeContents(icon.previousSibling!)
    const last = [...word.getClientRects()].at(-1)!, chevron = icon.getBoundingClientRect()
    return {
      lines: new Set([...el.getClientRects()].filter(rect => rect.width > 0 && rect.height > 0).map(rect => Math.round(rect.top))).size,
      withWord: chevron.top < last.bottom && chevron.bottom > last.top && chevron.left >= last.right - 0.5,
    }
  })
}
for (const width of [1440, 960, 390]) {
  test(`AEON-1363 Knowledge lesson line counts and keeps its chevron with the last word at ${width}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 1000 }); await mockLessons(page)
    let waiting = 1
    await page.route('**/api/decision-desk/projection*', route => route.fulfill({ json: {
      items: [], counts: { open: waiting, held: 0, chores: 0, lessons: { 'p-aeon': waiting } }, has_more: false, as_of: '2026-10-14T10:00:00Z',
    } }))
    for (const [count, name] of [[1, '1 lesson waits on the Decision Desk'], [3, '3 lessons wait on the Decision Desk']] as const) {
      waiting = count; await page.goto('/p/AEON/knowledge')
      const link = page.getByRole('link', { name, exact: true })
      await expect(link).toBeVisible(); await expect(link.locator('svg')).toHaveCount(1)
      expect(await lessonLine(link)).toEqual({ lines: 1, withWord: true })
      await page.screenshot({ path: testInfo.outputPath(`knowledge-${count}-lessons.png`) })
    }
    // A column that ends just past "Desk" wraps the last word together with the chevron.
    const link = page.getByRole('link', { name: '3 lessons wait on the Decision Desk', exact: true })
    await link.evaluate(el => {
      const icon = el.querySelector('svg')!, word = document.createRange(); word.selectNodeContents(icon.previousSibling!)
      const line = el.parentElement!; line.style.width = `${[...word.getClientRects()].at(-1)!.right - line.getBoundingClientRect().left + 1}px`
    })
    expect(await lessonLine(link)).toEqual({ lines: 2, withWord: true })
    await page.screenshot({ path: testInfo.outputPath('knowledge-narrow-column.png') })
  })
}
test('AEON-1331 rule draft, omitted source and keyboard use the native decision', async ({ page }) => {
  await page.clock.install(); const { state } = await mockLessons(page, { rule: true, noSource: true }); await openLesson(page)
  await expect(page.getByTestId('lesson-line')).toHaveText(sentence)
  await expect(page.getByTestId('desk-lesson-details')).toContainText('Adds this line to rule draft Recovery rules')
  await expect(page.getByRole('link', { name: /^From / })).toHaveCount(0)
  const reason = page.getByLabel('Why not? (optional)'); await reason.fill('A recorded note'); await reason.press('Escape')
  await expect(page.getByRole('dialog')).toBeVisible(); await pause(page); await page.getByTestId('desk-paper').press('Enter')
  await expect(allDecidedUndo(page)).toBeVisible()
  expect(state.calls[0]!.path).toContain('/draft'); expect(state.calls[0]!.body).toEqual({ layer_id: 'rule-layer', set_id: 'rule-set' })
})
test('AEON-1331 failed decisions and Undo keep the lesson honest', async ({ page }) => {
  await page.clock.install(); const { state } = await mockLessons(page); await openLesson(page); state.fail = true
  await page.getByTestId('desk-decide').click(); await expect(page.getByTestId('desk-status')).toContainText('changed since')
  await expect(page.getByTestId('desk-undo')).not.toBeVisible(); expect(state.decided).toBe(false)
  await pause(page)
  state.fail = false; await page.getByTestId('desk-decide').click(); await expect(allDecidedUndo(page)).toBeVisible()
  state.undoFail = true; await allDecidedUndo(page).click(); await expect(page.getByTestId('desk-status')).toContainText('cannot be undone')
  await expect(allDecidedUndo(page)).toBeVisible(); expect(state.decided).toBe(true)
  // The failed Undo reported no success: the round still ends by itself and the toast offers Undo again.
  await page.mouse.move(0, 0); await page.clock.runFor(1200); await expect(page.getByTestId('desk-paper')).toBeHidden()
  await expect(page.locator('.toast').filter({ hasText: 'Added to the changelog.' }).getByRole('button', { name: 'Undo' })).toBeVisible()
})
test('AEON-1331 replaced proposal refuses the frozen lesson', async ({ page }) => {
  const { state } = await mockLessons(page); await openLesson(page); state.revision++
  await page.getByTestId('desk-decide').click(); await expect(page.getByTestId('desk-status')).toContainText('changed since')
  expect(state.calls[0]!.match).toBe('"700"'); expect(state.decided).toBe(false)
})
test('AEON-1331 lesson memo has no accessibility violations', async ({ page }) => {
  await mockLessons(page); await openLesson(page)
  expect((await new AxeBuilder({ page }).include('.desk-dlg').analyze()).violations).toEqual([])
})
// Fails on 57da9c59: the desk filtered ?kind=lesson&project= without saying so,
// and an empty lesson list claimed nothing was waiting while other items were.
test('AEON-1331 lesson scope names its filter and Show all restores other waiting items', async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const { state } = await mockLessons(page)
  await page.route('**/api/decision-desk/projection*', route => {
    const lessons = state.decided ? [] : [{ id, kind: 'lesson' as const, project_id: 'p-aeon', revision: state.revision, title: sentence, created_at: '2026-10-14T08:30:00Z', held: false, can_decide: true, href: `/decision-desk?item=l:${id}`, source: `/api/knowledge/learnings/${nativeId}` }]
    const approval = { id: 'approval-1', kind: 'approval' as const, project_id: 'p-aeon', revision: 1, title: 'Approval · nodes.read', created_at: '2026-10-02T08:01:00Z', expires_at: new Date(Date.now() + 3_600_000).toISOString(), held: true, can_decide: true, href: '/decision-desk?item=a:approval-1', source: '/api/approvals/approval-1' }
    const rows = [...lessons, approval]
    return route.fulfill({ json: { items: rows, counts: { open: rows.length, held: 1, chores: 0, lessons: { 'p-aeon': lessons.length } }, has_more: false, as_of: '2026-10-14T10:00:00Z' } })
  })
  await page.goto('/decision-desk?kind=lesson&project=p-aeon')
  const scope = page.getByTestId('desk-scope')
  await expect(scope).toContainText('Showing lessons in Paimos Aeon.')
  const showAll = scope.getByRole('link', { name: 'Show all' })
  await expect(showAll).toHaveAttribute('href', '/decision-desk')
  expect((await showAll.boundingBox())!.height).toBeGreaterThanOrEqual(44)
  await expect(page.getByTestId(`desk-row-l:${id}`)).toBeVisible()
  await expect(page.getByTestId('desk-row-a:approval-1')).toHaveCount(0)
  await expect(page.getByText('Nothing is waiting in the available sources.')).toHaveCount(0)
  await expect(page.getByTestId('desk-tab-open')).toHaveText(/Open\s*1$/)
  await expect(page.locator('.places').getByRole('link', { name: 'Decision Desk, 2 open', exact: true })).toBeVisible()
  await page.screenshot({ path: testInfo.outputPath('scope-filtered.png') })
  // The approved layout puts reload actions beside source errors; sample its
  // persistent header along with both tabs and the round action.
  const guard = await controlStability(page, {
    open: page.getByTestId('desk-tab-open'),
    decided: page.getByTestId('desk-tab-decided'),
    review: page.getByTestId('desk-one-by-one'),
    heading: page.getByRole('heading', { level: 1, name: 'Decision Desk' }),
  })
  await guard.check(async () => {
    await showAll.click()
    await expect(page).not.toHaveURL(/[?&](?:kind|project)=/)
    await expect(page.getByTestId('desk-scope')).toHaveCount(0)
    await expect(page.getByTestId(`desk-row-l:${id}`)).toBeVisible()
    await expect(page.getByTestId('desk-row-a:approval-1')).toBeVisible()
    await expect(page.getByTestId('desk-tab-open')).toHaveText(/Open\s*2$/)
  })
  guard.done()
  await page.screenshot({ path: testInfo.outputPath('scope-restored.png') })
  state.decided = true
  await page.setViewportSize({ width: 390, height: 800 })
  await page.goto('/decision-desk?kind=lesson&project=p-aeon')
  // The desk keeps its loaded rows across a query change. Reload so the
  // projection that no longer includes the lesson is what the empty state reads.
  await page.reload()
  await expect(page.getByTestId('desk-scope')).toContainText('Showing lessons in Paimos Aeon.')
  await expect(page.getByText('No lessons wait in Paimos Aeon.')).toBeVisible()
  await expect(page.getByText('Nothing is waiting in the available sources.')).toHaveCount(0)
  await expect(page.getByTestId('desk-row-a:approval-1')).toHaveCount(0)
  await expect(page.getByTestId('desk-tab-open')).toHaveText(/Open\s*0$/)
  await page.screenshot({ path: testInfo.outputPath('scope-empty-390.png') })
  // AEON-1448: an empty scope renders no round action; it arrives at the tab row's end without moving the tabs.
  await expect(page.getByTestId('desk-one-by-one')).toHaveCount(0)
  const phone = await controlStability(page, {
    open: page.getByTestId('desk-tab-open'),
    decided: page.getByTestId('desk-tab-decided'),
    heading: page.getByRole('heading', { level: 1, name: 'Decision Desk' }),
  })
  await phone.check(async () => {
    await page.getByRole('link', { name: 'Show all' }).click()
    await expect(page.getByTestId('desk-scope')).toHaveCount(0)
    await expect(page.getByTestId('desk-row-a:approval-1')).toBeVisible()
    await expect(page.getByText('No lessons wait in Paimos Aeon.')).toHaveCount(0)
    await expect(page.getByTestId('desk-tab-open')).toHaveText(/Open\s*1$/)
    await expect(page.getByTestId('desk-one-by-one')).toBeVisible()
  })
  phone.done()
})

// AEON-1385 risk: a round that ends on a lesson closes on other timing, another toast or another Undo
// than a round that ends on an answer. Both kinds are decided at a paused page time: the 1.2 s and
// the rest of the grace are counted by runFor, never slept.
async function roundEnd(page: Page, toastText: string | RegExp) {
  const done = page.getByTestId('desk-all-decided')
  await expect(done).toContainText('All decided'); await expect(allDecidedUndo(page)).toBeVisible()
  await page.clock.runFor(1199); await expect(done).toBeVisible()
  await page.clock.runFor(1); await expect(page.getByTestId('desk-paper')).toBeHidden()
  const toast = page.locator('.toast').filter({ hasText: toastText })
  await expect(toast).toBeVisible(); await expect(toast.getByRole('button', { name: 'Undo', exact: true })).toBeVisible()
  return toast
}
async function graceEnds(page: Page, toast: Locator) {
  await page.clock.runFor(7999); await expect(toast).toBeVisible()
  await page.clock.runFor(1); await expect(toast).toHaveCount(0)
}
for (const width of [1440, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`AEON-1385 a round that ends on a lesson closes like one that ends on an answer at ${width} ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 }); await page.clock.install()
    // The answer: decided at the paused time, so its ten-second grace runs on the page clock.
    const world = await mockDecisionDesk(page, { theme })
    world.approval.decision = 'approved'; world.rule.state = 'proposed'; world.questions.splice(1)
    world.questions[0]!.input.source_request_id = world.action.id
    await page.goto('/decision-desk'); await page.getByTestId('desk-one-by-one').click()
    await expect(page.getByTestId('desk-decide')).toBeEnabled()
    world.answerTime = await pause(page); await page.getByTestId('desk-decide').click()
    await graceEnds(page, await roundEnd(page, /Decided\. It goes to .+ in a few seconds\./))

    // The lesson: the same 1.2 s, the same toast length.
    await page.clock.resume(); await page.unrouteAll({ behavior: 'ignoreErrors' })
    const { state } = await mockLessons(page, { theme }); await openLesson(page)
    await pause(page); await page.getByTestId('desk-decide').click()
    const lesson = await roundEnd(page, 'Added to the changelog.')
    await page.screenshot({ path: testInfo.outputPath(`lesson-toast-${width}-${theme}.png`) })
    await graceEnds(page, lesson)

    // Undo on "All decided" restores the lesson in place; Undo in the toast puts it back on the list.
    state.decided = false; await page.clock.resume(); await openLesson(page)
    await pause(page); await page.getByTestId('desk-decide').click()
    await expect(page.getByTestId('desk-all-decided')).toContainText('1 lesson decided.')
    await page.screenshot({ path: testInfo.outputPath(`lesson-all-decided-${width}-${theme}.png`) })
    await allDecidedUndo(page).click(); await expect(page.getByTestId('choice-0')).toBeEnabled()
    await expect(page.getByTestId('desk-status')).toHaveText('Undone.'); expect(state.decided).toBe(false)
    await page.getByTestId('desk-decide').click()
    const again = await roundEnd(page, 'Added to the changelog.')
    await again.getByRole('button', { name: 'Undo', exact: true }).click()
    await expect(page.getByTestId(`desk-row-l:${id}`)).toBeVisible(); expect(state.decided).toBe(false)
    expect(state.calls.map(call => call.path.split('/').at(-1))).toEqual(['accept', 'accept', 'undo', 'accept', 'undo'])
  })
}
