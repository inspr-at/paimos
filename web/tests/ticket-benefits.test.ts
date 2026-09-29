// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { benefitDraft, benefitIssues, completedTicketState, firstBenefitGap } from '../src/lib/ticketBenefits.ts'
import { displayHeadline, emptyNotesLine, hasUsableNotes, historicalTagFallback, localizedNote, noteLocale, ticketsOf, matches, writtenAfterRelease, WRITTEN_AFTER_LABEL, type Release } from '../src/lib/releases.ts'
const fields = { pill_en: 'Clear release notes', pill_de: 'Verständliche Release Notes', benefit_en: 'Tickets explain what you gain.', benefit_de: 'Tickets erklären den Nutzen.' }
test('benefit completion covers product completion states without guessing custom or cancelled states', () => {
  for (const state of ['done', 'accepted', 'delivered']) assert.equal(completedTicketState(state), true, state)
  for (const state of ['open', 'new', 'qa', 'cancelled', 'canceled', 'archived', 'custom-complete']) assert.equal(completedTicketState(state), false, state)
})
test('benefit drafts warn without inventing translations, including hidden tickets', () => {
  assert.equal(benefitIssues({ hide_from_release_notes: true }).length, 4)
  assert.deepEqual(benefitIssues(fields), [])
  assert.equal(benefitIssues({ ...fields, pill_en: 'One two three four five' }).length, 1)
  assert.equal(benefitIssues({ ...fields, pill_en: 'One\u00a0two' }).length, 0)
  assert.equal(benefitIssues({ ...fields, benefit_de: ' \n ' }).length, 1)
  assert.equal(benefitDraft({ benefit_en: 'Kept' }).benefit_de, '')
  assert.equal(firstBenefitGap({})?.line, 'Pill · English is required.')
  assert.equal(firstBenefitGap({ ...fields, benefit_de: ' ' })?.line, 'Benefit · Deutsch is required.')
  assert.equal(firstBenefitGap({ ...fields, pill_de: 'Eins' })?.line, 'Pill · Deutsch needs 2–4 words.')
  assert.equal(firstBenefitGap(fields), null)
})
test('snapshot notes displace Git headlines and ticket guesses while archives stay readable', () => {
  const r = { version: '260928120000.0.0', headline: 'Invented headline', tickets: ['TEST-999'], changes: [], notes: { source: 'snapshot', snapshot_sha256: '', captured_at: null, release_revision: 1, hidden: 1, gaps: [], items: [{ id: 'one', key: 'TEST-7', ...fields }] } } as unknown as Release
  assert.equal(hasUsableNotes(r), true)
  assert.equal(noteLocale('de-AT'), 'de')
  assert.equal(noteLocale('en-GB'), 'en')
  assert.equal(noteLocale(null), 'en')
  assert.equal(displayHeadline(r), fields.pill_en)
  assert.equal(displayHeadline(r, 'de-AT'), fields.pill_de)
  const german = localizedNote({ ...fields, pill_de: '  ', benefit_de: '' }, 'de-DE')
  assert.equal(german.pill, fields.pill_en)
  assert.equal(german.benefit, fields.benefit_en)
  assert.equal(german.pillLang, 'en')
  assert.equal(localizedNote(fields, 'de-CH').benefitLang, 'de')
  assert.equal(historicalTagFallback(r), false)
  assert.deepEqual(ticketsOf(r), ['TEST-7'])
  assert.equal(matches(r, { q: 'erklären', features: false, fixes: false, tickets: false }, 'de'), true)
  assert.equal(matches(r, { q: 'erklären', features: false, fixes: false, tickets: false }, 'en'), false)
  r.notes!.items = []; r.notes!.gaps = ['Missing membership']
  assert.equal(displayHeadline(r), 'Release notes unavailable')
  assert.deepEqual(ticketsOf(r), [])
  assert.equal(hasUsableNotes(r), true) // An incomplete snapshot cannot fall back to Git benefits.
  r.notes!.gaps = []
  assert.equal(displayHeadline(r), 'Invented headline')
  assert.equal(emptyNotesLine(), 'Internal changes only.')
  assert.equal(emptyNotesLine('de-AT'), 'Nur interne Änderungen.')
  assert.equal(historicalTagFallback(r), false) // A known internal release does not claim its snapshot is missing.
  assert.deepEqual(ticketsOf(r), []) // Hidden-only/empty membership stays authoritative.
  r.notes = { source: 'unavailable', fallback: 'historical-tag-headline', snapshot_sha256: '', captured_at: null, release_revision: 0, hidden: 0, items: [], gaps: ['missing'] }
  assert.equal(historicalTagFallback(r), true)
  assert.equal(displayHeadline(r), 'Invented headline')
  delete r.notes
  assert.equal(hasUsableNotes(r), false)
  assert.equal(historicalTagFallback(r), true)
  assert.equal(displayHeadline(r), 'Invented headline')
  assert.deepEqual(ticketsOf(r), ['TEST-999'])
})
test('a backfilled snapshot says the notes were written after the release, and a historical headline does not', () => {
  const notes = { source: 'tag:release-notes/synthetic.json', snapshot_sha256: 'a'.repeat(64), captured_at: '2026-09-29T08:00:00Z', release_revision: 2, hidden: 0, gaps: [], items: [{ id: 'one', key: 'AEON-75', ...fields }] }
  const r = { version: '260115100000.0.0', headline: 'Git headline', tickets: [], changes: [], notes } as unknown as Release
  assert.equal(WRITTEN_AFTER_LABEL, 'Notes written after release')
  assert.equal(writtenAfterRelease(r), false)
  notes.written_after_release = true
  assert.equal(writtenAfterRelease(r), true)
  assert.equal(historicalTagFallback(r), false)
  const historical = { headline: 'Git headline', notes: { source: 'unavailable', fallback: 'historical-tag-headline', snapshot_sha256: '', captured_at: null, release_revision: 0, hidden: 0, items: [], gaps: ['missing'], written_after_release: true } } as unknown as Release
  assert.equal(writtenAfterRelease(historical), false)
  assert.equal(historicalTagFallback(historical), true)
})
