// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
import { join } from 'node:path'
import { mockDecisionDesk } from './decision-desk-fixtures'
import { expectStableControls } from './helpers/stable'
import { mockSettings, settingsData } from './settings-fixtures'
import { mockRules } from './rules-fixtures'

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
  test(`doctrine draft effect and required review open the exact pending draft ${width} ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 1000 })
    const world = await mockDecisionDesk(page, { long: true, theme }), question = world.questions[0]!
    world.rule.pr_url = '' // An unpublished draft exists only in the native inbox.
    question.state = 'answered'; question.revision = 2
    question.answer = { id: 'answer-2', revision: 2, outcome: 'doctrine', answer: 'Authorize inside the mutation transaction.', option_id: 'partial', decided_by: 'person', created_at: '', deliver_after: '' }
    question.pending = [{ id: 'effect-2', revision: 2, kind: 'outcome', state: 'delivered', deliver_after: '', doctrine_state: 'pending',
      effect_data: { doctrine_id: world.rule.id, review_required: [{ kind: 'doctrine', ref: world.rule.id, why: 'Die frühere Regeländerung bleibt bis zur Prüfung durch eine Person erhalten.' }] } }]
    world.rule.why = 'Die vorhandenen Berechtigungen und die Mandantentrennung müssen bei jeder Änderung erhalten bleiben.'
    await page.goto('/decision-desk?item=q:question-1')
    const effects = page.getByTestId('desk-outcome-effects')
    const draftLink = effects.getByRole('link', { name: `Review doctrine draft ${world.rule.id}`, exact: true })
    const reviewLink = effects.getByRole('link', { name: `Review doctrine (${world.rule.id})`, exact: true })
    const target = `/decision-desk?item=r:${world.rule.id}`
    await expect(draftLink).toHaveAttribute('href', target)
    await expect(reviewLink).toHaveAttribute('href', target)
    const dir = testInfo.outputPath('aeon-569-fix3'); await mkdir(dir, { recursive: true })
    await effects.evaluate(element => element.scrollIntoView({ block: 'start' }))
    await page.screenshot({ path: join(dir, `doctrine-effect-${width}-${theme}.png`) })
    const openDraft = async () => {
      await expect(page).toHaveURL(new RegExp('item=r:' + world.rule.id + '$'))
      await expect(page.getByTestId('desk-paper').getByRole('heading', { level: 2 })).toHaveText(world.rule.label)
      await expect(page.getByTestId('desk-body')).toContainText(world.rule.proposed)
      await expect(page.getByTestId('desk-body')).toContainText(world.rule.why)
      await expect(page.getByTestId('desk-status')).not.toContainText('Source details are unavailable')
      await expect(page.getByTestId('choice-0')).toBeEnabled()
      expect(world.calls).toEqual([])
    }
    await draftLink.click(); await openDraft()
    await expectStableControls({ controls: { decide: page.getByTestId('desk-decide'), skip: page.getByTestId('desk-skip'), close: page.getByTestId('desk-close'), pager: page.getByTestId('desk-pager'),
      choices: page.getByTestId('desk-choices'), clicked: page.getByTestId('choice-row-0'), stamps: page.getByTestId('desk-stamps'), once: page.getByTestId('stamp-once'), ...(width === 390 ? { frame: page.getByTestId('desk-frame') } : {}) },
      scrollAreas: { body: page.getByTestId('desk-body') }, interactions: [{ name: 'choose the exact linked draft without moving the controls', run: async () => {
        await page.getByTestId('choice-0').click(); await expect(page.getByTestId('desk-decide')).toBeEnabled()
      } }] })
    await page.screenshot({ path: join(dir, `doctrine-linked-${width}-${theme}.png`) })
    await page.goto('/decision-desk?item=q:question-1')
    await reviewLink.click(); await openDraft()
  })
}

test('published doctrine effects keep their proposal review while an earlier pending draft opens in the Desk', async ({ page }, testInfo) => {
  const world = await mockDecisionDesk(page), question = world.questions[0]!
  await mockSettings(page, settingsData()); await mockRules(page)
  await page.route('**/api/rules/doctrine', route => route.fulfill({ json: { sources: [], proposals_enabled: true } }))
  await page.route('**/api/rules/doctrine/proposals', route => route.fulfill({ json: { proposals: [{ id: 'published-1', repository: 'inspr-at/inspr-modules', state: 'in_review', pr_number: 123, pr_url: 'https://github.com/inspr-at/inspr-modules/pull/123' }] } }))
  question.state = 'answered'; question.revision = 2
  question.answer = { id: 'answer-2', revision: 2, outcome: 'doctrine', answer: 'Use the rule.', option_id: 'partial', decided_by: 'person', created_at: '', deliver_after: '' }
  question.pending = [{ id: 'effect-2', revision: 2, kind: 'outcome', state: 'delivered', deliver_after: '', doctrine_state: 'in_review',
    effect_data: { doctrine_id: 'published-1', review_required: [{ kind: 'doctrine', ref: world.rule.id, why: 'The earlier edited draft still needs review.' }] } }]
  await page.goto('/decision-desk?item=q:question-1')
  const effects = page.getByTestId('desk-outcome-effects'), published = effects.getByRole('link', { name: 'Review doctrine draft published-1', exact: true })
  await expect(published).toHaveAttribute('href', '/settings/agent-rules')
  const pending = effects.getByRole('link', { name: `Review doctrine (${world.rule.id})`, exact: true })
  await expect(pending).toHaveAttribute('href', `/decision-desk?item=r:${world.rule.id}`)
  await pending.click()
  await expect(page.getByTestId('desk-paper').getByRole('heading', { level: 2 })).toHaveText(world.rule.label)
  await page.goto('/decision-desk?item=q:question-1'); await published.click()
  await expect(page).toHaveURL(/\/settings\/agent-rules$/)
  await expect(page.getByRole('link', { name: 'inspr-modules · PR #123', exact: true })).toBeVisible()
  expect(world.calls).toEqual([])
})

for (const decision of ['approve', 'decline'] as const) test(`revision-zero native tier request can ${decision} with exact ownership`, async ({ page }, testInfo) => {
  const world = await mockDecisionDesk(page, { tier: true }); world.tier.revision = 0
  await page.goto('/decision-desk?item=t:tier-request-1')
  await expect(page.getByRole('dialog', { name: 'Decision Desk memo' })).toBeVisible()
  await expect(page.getByTestId('desk-status')).not.toContainText('Source details are unavailable')
  await expect(page.getByTestId(`choice-${decision === 'approve' ? 0 : 1}`)).toBeEnabled()
  await page.getByTestId(`choice-${decision === 'approve' ? 0 : 1}`).click()
  await expect(page.getByTestId('desk-decide')).toBeEnabled()
  await page.getByTestId('desk-decide').click(); await expect.poll(() => world.calls.length).toBe(1)
  expect(world.calls[0]!.path).toBe('/api/projects/p-aeon/harness-sessions/tier-session/tier/requests/tier-request-1/decision')
  expect(world.calls[0]!.body).toMatchObject({ decision, expected_revision: 0, expected_ownership: world.ownership })
  expect(world.tier.requests[0]!.state).toBe(decision === 'approve' ? 'approved' : 'declined')
})
