// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { benefitDraft, benefitIssues } from '../src/lib/ticketBenefits.ts'
import { displayHeadline, ticketsOf, matches, type Release } from '../src/lib/releases.ts'
const fields = { pill_en: 'Clear release notes', pill_de: 'Verständliche Release Notes', benefit_en: 'Tickets explain what you gain.', benefit_de: 'Tickets erklären den Nutzen.' }
test('benefit drafts warn without inventing translations, including hidden tickets', () => {
  assert.equal(benefitIssues({ hide_from_release_notes: true }).length, 4)
  assert.deepEqual(benefitIssues(fields), [])
  assert.equal(benefitIssues({ ...fields, pill_en: 'One two three four five' }).length, 1)
  assert.equal(benefitIssues({ ...fields, pill_en: 'One\u00a0two' }).length, 0)
  assert.equal(benefitIssues({ ...fields, benefit_de: ' \n ' }).length, 1)
  assert.equal(benefitDraft({ benefit_en: 'Kept' }).benefit_de, '')
})
test('snapshot notes displace Git headlines and ticket guesses while archives stay readable', () => {
  const r = { version: '260928120000.0.0', headline: 'Invented headline', tickets: ['TEST-999'], changes: [], notes: { source: 'snapshot', snapshot_sha256: '', captured_at: null, release_revision: 1, hidden: 1, gaps: [], items: [{ id: 'one', key: 'TEST-7', ...fields }] } } as unknown as Release
  assert.equal(displayHeadline(r), fields.pill_en)
  assert.deepEqual(ticketsOf(r), ['TEST-7'])
  assert.equal(matches(r, { q: 'erklären', features: false, fixes: false, tickets: false }), true)
  r.notes!.items = []; r.notes!.gaps = ['Missing membership']
  assert.equal(displayHeadline(r), 'Release notes unavailable')
  assert.deepEqual(ticketsOf(r), [])
  delete r.notes
  assert.equal(displayHeadline(r), 'Invented headline')
  assert.deepEqual(ticketsOf(r), ['TEST-999'])
})
