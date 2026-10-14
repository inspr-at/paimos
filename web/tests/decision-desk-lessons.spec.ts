// SPDX-License-Identifier: AGPL-3.0-only
// Risk: the person cannot see the exact prepared write, decides another
// recommendation, or loses the native Undo while controls move on a phone.
import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { mockDecisionDesk } from './decision-desk-fixtures'
import { knowledgeWorld, mockKnowledge } from './knowledge-fixtures'
import { controlStability } from './control-stability'
import type { MethodLearning } from '../src/lib/knowledge'

const id = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa'
const nativeId = `n-${id}`
const sentence = 'Rehearse recovery before releasing.'
const line = '- 2026-10-14 · Rehearse recovery before releasing. ([AEON-1](/p/AEON/AEON-1))'
async function mockLessons(page: Page, options: { theme?: 'light' | 'dark'; outdated?: boolean; rule?: boolean; noSource?: boolean; empty?: boolean } = {}) {
  const state = { decided: false, revision: 700, fail: false, undoFail: false, calls: [] as { path: string; body: Record<string, unknown>; match?: string }[] }
  const learning: MethodLearning = {
    id: nativeId, source: 'ticket', node_id: id, key: 'AEON-1', title: options.noSource ? '' : 'Recovery failed during release',
    text: 'The recovery check was missing.', href: options.noSource ? '' : '/p/AEON/AEON-1', author: null, at: '2026-10-14T08:00:00Z',
    recommendation: { decision: 'accept', lesson: sentence, knowledge_id: options.rule ? 'rule-set' : 'k-aeon-deploy', knowledge_title: options.rule ? 'Recovery rules' : 'Ship an Aeon release',
      source_summary: 'The incident exposed an untested recovery path.', line: options.rule ? undefined : line,
      rule_layer_id: options.rule ? 'rule-layer' : undefined, rule_text: options.rule ? sentence : undefined,
      by: { id: 'scout', name: 'Scout' }, at: '2026-10-14T08:30:00Z', event_id: 700, prepared: true, stale: false, target_missing: false },
  }
  const row = () => ({ id, kind: 'lesson' as const, project_id: 'p-aeon', revision: state.revision, title: sentence, created_at: learning.recommendation!.at,
    held: false, can_decide: true, href: `/decision-desk?item=l:${id}`, source: `/api/knowledge/learnings/${nativeId}` })
  await mockDecisionDesk(page, { theme: options.theme })
  const world = knowledgeWorld(), calls = await mockKnowledge(page, world)
  const target = world.entries.find(entry => entry.id === 'k-aeon-deploy')!
  target.updated_at = options.outdated ? '2026-10-14T09:00:00Z' : '2026-10-13T08:00:00Z'
  await page.route('**/api/decision-desk/projection*', route => route.fulfill({ json: {
    items: state.decided || options.empty ? [] : [row()], counts: { open: state.decided || options.empty ? 0 : 1, held: 0, chores: 0 }, has_more: false, as_of: '2026-10-14T10:00:00Z',
  } }))
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
async function openLesson(page: Page) {
  await page.goto(`/decision-desk?kind=lesson&item=l:${id}`)
  await expect(page.getByRole('dialog', { name: 'Decision Desk memo' })).toBeVisible()
  await expect(page.getByTestId('desk-decide')).toBeEnabled()
}

for (const width of [1440, 1024, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`AEON-1331 lesson memo and row stay stable at ${width} ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 1000 })
    const { state } = await mockLessons(page, { theme, outdated: true })
    await page.goto('/decision-desk?kind=lesson')
    const row = page.getByTestId(`desk-row-l:${id}`)
    await expect(row).toContainText('Lesson to adopt'); await expect(row).toContainText(sentence)
    await page.screenshot({ path: testInfo.outputPath('list-row.png') })
    await row.click(); await expect(page.getByTestId('desk-decide')).toBeEnabled()
    await expect(page.getByTestId('lesson-line')).toHaveText(line)
    await expect(page.getByRole('link', { name: 'From Recovery failed during release' })).toHaveAttribute('href', '/p/AEON/AEON-1')
    await expect(page.getByTestId('desk-lesson-details')).toContainText('Adds this line to Ship an Aeon release › Changelog')
    await expect(page.getByTestId('lesson-outdated')).toHaveText('The entry changed since this was prepared. Accept writes against the current version.')
    await page.screenshot({ path: testInfo.outputPath('outdated.png') })
    const guard = await controlStability(page, {
      decide: page.getByTestId('desk-decide'), skip: page.getByTestId('desk-skip'), pager: page.getByTestId('desk-pager'),
      choices: page.getByTestId('desk-choices'), accept: page.getByTestId('choice-0'), dismiss: page.getByTestId('choice-1'),
      clickedRow: page.getByTestId('choice-row-1'), ...(width === 390 ? { frame: page.getByTestId('desk-frame') } : {}),
    })
    await guard.check(() => page.getByTestId('choice-1').click())
    await expect(page.getByLabel('Why not? (optional)')).toBeVisible()
    await guard.check(async () => { await page.getByTestId('desk-decide').click(); await expect(page.getByTestId('desk-undo')).toBeVisible() })
    expect(state.calls[0]!.path).toContain('/dismiss'); expect(state.calls[0]!.body).toEqual({ learning_text: 'The recovery check was missing.' })
    await page.screenshot({ path: testInfo.outputPath('dismissed.png') })
    await guard.check(async () => { await page.getByTestId('desk-undo').click(); await expect(page.getByTestId('choice-0')).toBeEnabled() })
    await guard.check(() => page.getByTestId('choice-0').click())
    await guard.check(async () => { await page.getByTestId('desk-decide').click(); await expect(page.getByTestId('desk-undo')).toBeVisible() })
    expect(state.calls[2]!.path).toContain('/accept'); expect(state.calls[2]!.match).toBe('"700"')
    expect(state.calls[2]!.body).toEqual({ knowledge_id: 'k-aeon-deploy', lesson: sentence, learning_text: 'The recovery check was missing.' })
    await page.screenshot({ path: testInfo.outputPath('accepted-with-undo.png') }); guard.done()
  })
  test(`AEON-1331 pending lesson and Knowledge link at ${width} ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 1000 }); await mockLessons(page, { theme })
    await openLesson(page); await expect(page.getByTestId('lesson-outdated')).toHaveCount(0)
    await page.screenshot({ path: testInfo.outputPath('pending.png') })
    await page.getByTestId('desk-close').click(); await page.goto('/p/AEON/knowledge')
    await expect(page.getByRole('heading', { name: 'Method learnings' })).toHaveCount(0)
    const link = page.getByRole('link', { name: '1 lessons wait on the Decision Desk ›' })
    await expect(link).toHaveAttribute('href', '/decision-desk?kind=lesson&project=p-aeon')
    const search = page.getByRole('searchbox', { name: 'Search knowledge in Paimos Aeon' })
    const guard = await controlStability(page, { search, newEntry: page.getByRole('button', { name: 'New knowledge entry' }) })
    await guard.check(() => search.fill('Ship')); guard.done()
    await page.screenshot({ path: testInfo.outputPath('knowledge-with-lessons.png') })
    await link.click(); await expect(page).toHaveURL(/kind=lesson&project=p-aeon/)
    await expect(page.locator('.desk-row')).toHaveCount(1)
  })
  test(`AEON-1331 Knowledge without lessons at ${width} ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 1000 }); await mockLessons(page, { theme, empty: true })
    await page.goto('/p/AEON/knowledge'); await expect(page.locator('.k-frame')).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Method learnings' })).toHaveCount(0)
    await expect(page.getByRole('link', { name: /lessons wait on the Decision Desk/ })).toHaveCount(0)
    await page.screenshot({ path: testInfo.outputPath('knowledge-without-lessons.png') })
  })
}
test('AEON-1331 rule draft, omitted source and keyboard use the native decision', async ({ page }) => {
  const { state } = await mockLessons(page, { rule: true, noSource: true }); await openLesson(page)
  await expect(page.getByTestId('lesson-line')).toHaveText(sentence)
  await expect(page.getByTestId('desk-lesson-details')).toContainText('Adds this line to rule draft Recovery rules')
  await expect(page.getByRole('link', { name: /^From / })).toHaveCount(0)
  const reason = page.getByLabel('Why not? (optional)'); await reason.fill('A recorded note'); await reason.press('Escape')
  await expect(page.getByRole('dialog')).toBeVisible(); await page.getByTestId('desk-paper').press('Enter')
  await expect(page.getByTestId('desk-undo')).toBeVisible()
  expect(state.calls[0]!.path).toContain('/draft'); expect(state.calls[0]!.body).toEqual({ layer_id: 'rule-layer', set_id: 'rule-set' })
})
test('AEON-1331 failed decisions and Undo keep the lesson honest', async ({ page }) => {
  const { state } = await mockLessons(page); await openLesson(page); state.fail = true
  await page.getByTestId('desk-decide').click(); await expect(page.getByTestId('desk-status')).toContainText('changed since')
  await expect(page.getByTestId('desk-undo')).not.toBeVisible(); expect(state.decided).toBe(false)
  state.fail = false; await page.getByTestId('desk-decide').click(); await expect(page.getByTestId('desk-undo')).toBeVisible()
  state.undoFail = true; await page.getByTestId('desk-undo').click(); await expect(page.getByTestId('desk-status')).toContainText('cannot be undone')
  await expect(page.getByTestId('desk-undo')).toBeVisible(); expect(state.decided).toBe(true)
})
test('AEON-1331 replaced proposal refuses the frozen lesson', async ({ page }) => {
  const { state } = await mockLessons(page); await openLesson(page); state.revision++
  await page.getByTestId('desk-decide').click(); await expect(page.getByTestId('desk-status')).toContainText('changed since')
  expect(state.calls[0]!.match).toBe('"700"'); expect(state.decided).toBe(false)
})
test('AEON-1331 lesson memo has no accessibility violations', async ({ page }) => {
  await mockLessons(page); await openLesson(page)
  expect((await new AxeBuilder({ page }).include('.desk-dialog').analyze()).violations).toEqual([])
})
