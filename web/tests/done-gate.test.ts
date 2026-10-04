// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { APIError } from '../src/lib/api.ts'
import { pillWords } from '../src/lib/ticketBenefits.ts'
import { benefitGateError, benefitRequiredCode, benefitRetryFields, benefitSkip, benefitStepSummary, completionFields, gateAction, gateProgress, gateTitle, needsBenefitPrompt, skippedStatusLabel } from '../src/lib/doneGate.ts'

const text = {
  pill_en: 'Clear release notes',
  pill_de: 'Verständliche Release Notes',
  benefit_en: 'Tickets explain what you gain.',
  benefit_de: 'Tickets erklären den Nutzen.',
}

test('needsBenefitPrompt only when a ticket enters completion without the four texts', () => {
  const ticket = { kind_slug: 'ticket', state: 'in-progress', fields: {} as Record<string, unknown> }
  assert.equal(needsBenefitPrompt(ticket, 'done'), true)
  assert.equal(needsBenefitPrompt(ticket, 'accepted'), true)
  assert.equal(needsBenefitPrompt(ticket, 'delivered'), true)
  assert.equal(needsBenefitPrompt(ticket, 'qa'), false)
  assert.equal(needsBenefitPrompt(ticket, 'cancelled'), false)
  assert.equal(needsBenefitPrompt({ ...ticket, kind_slug: 'task' }, 'done'), false)
  assert.equal(needsBenefitPrompt({ ...ticket, kind_slug: 'epic' }, 'done'), false)
  assert.equal(needsBenefitPrompt({ ...ticket, state: 'done' }, 'accepted'), false)
  assert.equal(needsBenefitPrompt({ ...ticket, state: 'accepted' }, 'delivered'), false)
  assert.equal(needsBenefitPrompt({ ...ticket, fields: { hide_from_release_notes: true } }, 'done'), true)
  assert.equal(needsBenefitPrompt({ ...ticket, fields: { ...text, benefit_de: '' } }, 'done'), true)
  assert.equal(needsBenefitPrompt({ ...ticket, fields: text }, 'done'), false)
  assert.equal(needsBenefitPrompt({ ...ticket, fields: null }, 'done'), true)
})

test('benefitGateError recognises the code and the legacy sentence', () => {
  const sentence = 'before done: pill_en is required'
  assert.equal(benefitGateError(new APIError(422, sentence, { error: sentence, code: benefitRequiredCode })), true)
  assert.equal(benefitGateError(new APIError(422, sentence, { error: sentence })), true)
  assert.equal(benefitGateError(new APIError(422, 'State is not allowed here', { error: 'State is not allowed here' })), false)
  assert.equal(benefitGateError(new APIError(500, sentence, { error: sentence, code: benefitRequiredCode })), false)
  assert.equal(benefitGateError(new Error(sentence)), false)
})

test('benefitSkip matches a bulk skip by code or the legacy reason', () => {
  assert.equal(benefitSkip({ code: benefitRequiredCode, reason: 'before done: benefit_de is required' }), true)
  assert.equal(benefitSkip({ reason: 'before done: pill_en is required' }), true)
  assert.equal(benefitSkip({ reason: 'not found' }), false)
  assert.equal(benefitSkip({ code: 'other', reason: 'not found' }), false)
})

test('gate copy uses the state label and counts a batch', () => {
  assert.equal(gateTitle('done'), "Before it's done: what does the user gain?")
  assert.equal(gateTitle('accepted'), "Before it's accepted: what does the user gain?")
  assert.equal(gateTitle('delivered'), "Before it's delivered: what does the user gain?")
  assert.equal(gateAction('done'), 'Mark done')
  assert.equal(gateAction('accepted'), 'Mark accepted')
  assert.equal(gateAction('delivered'), 'Mark delivered')
  assert.equal(gateProgress(1, 1), null)
  assert.equal(gateProgress(2, 5), '2 of 5')
})

test('pill word counts match the server boundaries', () => {
  assert.equal(pillWords(''), 0)
  assert.equal(pillWords('   '), 0)
  assert.equal(pillWords('One'), 1)
  assert.equal(pillWords('One two'), 2)
  assert.equal(pillWords('One two three four'), 4)
  assert.equal(pillWords('One two three four five'), 5)
  assert.equal(pillWords('One\u00a0two'), 2)
})

test('completion fields keep unrelated values and do not invent a hide flag', () => {
  const existing = { priority: 'high', assignee: 'p-1', tags: ['a'] }
  const next = completionFields(existing, { pill_en: '  Clear notes  ', pill_de: 'Klare Notizen', benefit_en: ' Gain. ', benefit_de: 'Nutzen.' })
  assert.equal(next.priority, 'high')
  assert.equal(next.assignee, 'p-1')
  assert.deepEqual(next.tags, ['a'])
  assert.equal(next.pill_en, 'Clear notes')
  assert.equal(next.benefit_en, 'Gain.')
  assert.equal('hide_from_release_notes' in next, false)
  assert.notEqual(next, existing)
  assert.equal(completionFields({ hide_from_release_notes: true }, text).hide_from_release_notes, true)
  assert.equal(completionFields(existing, { ...text, hide_from_release_notes: true }).hide_from_release_notes, true)
  assert.equal('hide_from_release_notes' in completionFields(existing, { ...text, hide_from_release_notes: false }), false)
  assert.equal(completionFields({ hide_from_release_notes: true }, { ...text, hide_from_release_notes: false }).hide_from_release_notes, false)
})

test('a step-through summary says what finished and what stayed', () => {
  assert.equal(benefitStepSummary(2, 1, 'In progress'), '2 done · 1 skipped (still In progress)')
  assert.equal(benefitStepSummary(0, 3, 'New'), '3 skipped (still New)')
  assert.equal(benefitStepSummary(4, 0, 'New'), '4 done')
  assert.equal(skippedStatusLabel(['In progress']), 'In progress')
  assert.equal(skippedStatusLabel(['In progress', 'Backlog']), 'In progress or Backlog')
  assert.equal(skippedStatusLabel(['In progress', 'Backlog', 'New']), 'an earlier status')
})

test('a rejected benefit write is asked again with the typed text', () => {
  const latest = { priority: 'low', pill_en: 'Server text here', tags: ['a'] }
  const typed = completionFields({ priority: 'high', hide_from_release_notes: true }, { ...text, hide_from_release_notes: false })
  const fields = benefitRetryFields(latest, typed)
  assert.equal(fields.pill_en, text.pill_en)
  assert.equal(fields.benefit_de, text.benefit_de)
  assert.equal(fields.priority, 'low')
  assert.deepEqual(fields.tags, ['a'])
  assert.equal(fields.hide_from_release_notes, false)
  const hidden = benefitRetryFields({ priority: 'low' }, completionFields({}, { ...text, hide_from_release_notes: true }))
  assert.equal(hidden.hide_from_release_notes, true)
  assert.equal(hidden.pill_en, text.pill_en)
})

test('work leaves retain completion prompts while parents never wait for benefits', () => {
  const work = { kind_slug: 'work', state: 'in_progress', fields: {} }
  for (const next of ['done', 'accepted', 'delivered']) {
    assert.equal(needsBenefitPrompt(work, next), true)
    assert.equal(needsBenefitPrompt({ ...work, estimate: { is_parent: true } }, next), false)
    assert.equal(needsBenefitPrompt({ ...work, fields: text }, next), false)
  }
})
