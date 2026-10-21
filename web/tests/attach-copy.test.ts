// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { attachCopy, deadlineMoment, pairingCopy, type TitlePart } from '../src/lib/attachCopy.ts'

test('pairing and a session link stay separate in the app language for English and German profiles', () => {
  const en = attachCopy('My Studio', 'en-GB'), de = attachCopy('My Studio', 'de-AT')
  assert.equal(en.computerPaired, 'Computer paired')
  assert.equal(en.sessionUnlinked, 'This session not yet linked')
  assert.equal(en.sessionLinked, 'This session linked')
  assert.match(en.pairingHelp, /each running session separately/)
  assert.match(en.codeHelp, /After the local check, approval opens in your browser/)
  assert.equal(de.computerPaired, 'Computer paired')
  assert.equal(de.sessionUnlinked, 'This session not yet linked')
  assert.equal(de.sessionLinked, 'This session linked')
  assert.match(de.pairingHelp, /each running session separately/)
  assert.match(de.codeHelp, /After the local check/)
  assert.deepEqual(de, en)
  assert.equal(attachCopy('My Studio', 'fr').computerPaired, en.computerPaired)
  assert.match(en.pairingHelp, /to My Studio/)
  assert.match(de.pairingHelp, /to My Studio/)
})

// Fails on bc1bde3d, whose where-lines said "Accounts in Settings" (Tick round 1: the separator is always ›).
test('AEON-1502 where-lines name Settings › Accounts with the › separator', () => {
  const en = pairingCopy('en', 'PAIMOS'), de = pairingCopy('de', 'PAIMOS')
  const lines = [
    en.whereNowhere,
    en.whereSome(['Default', 'Acme client work'], 0, false),
    en.whereSome(['Alpha'], 2, true),
    en.whereSome(['Alpha'], 0, true),
    en.whereRule,
    en.whereFailed,
    de.whereNowhere,
    de.whereSome(['Alpha', 'Beta'], 0, false),
    de.whereSome(['Alpha'], 2, true),
    de.whereRule,
    de.whereFailed,
  ]
  assert.deepEqual(lines, [
    'Nowhere yet · allow in Settings › Accounts before first use',
    'Default and Acme client work · change in Settings › Accounts',
    'Alpha and 2+\u00a0more · change in Settings › Accounts',
    'Alpha and more · change in Settings › Accounts',
    'The workspace rule decides · Settings › Accounts',
    'The workspace rule could not be read; it still applies · check Settings › Accounts after connecting',
    'Noch nirgends · vor der ersten Nutzung in Einstellungen › Konten erlauben',
    'Alpha und Beta · in Einstellungen › Konten änderbar',
    'Alpha und 2+\u00a0weitere · in Einstellungen › Konten änderbar',
    'Die Regel des Arbeitsbereichs entscheidet · Einstellungen › Konten',
    'Regel nicht lesbar; sie gilt trotzdem · nach dem Verbinden in Einstellungen › Konten prüfen',
  ])
  assert.equal(lines.some(line => /Accounts in Settings|Konten in Einstellungen/.test(line)), false)
})

// Fails on 39ea42fe, which said "Nowhere yet" when only the first page of contexts was read.
test('AEON-1502 an unread rest of contexts is named, never claimed as nowhere', () => {
  const en = pairingCopy('en', 'PAIMOS'), de = pairingCopy('de', 'PAIMOS')
  assert.equal(en.whereNoneRead(200), 'Not in the first 200\u00a0contexts; the rest were not read · check Settings › Accounts')
  assert.equal(de.whereNoneRead(200), 'In keinem der ersten 200\u00a0Kontexte; weitere nicht gelesen · in Einstellungen › Konten prüfen')
  for (const line of [en.whereNoneRead(200), de.whereNoneRead(200)]) assert.doesNotMatch(line, /Nowhere|nirgends/)
})

// Fails on bc1bde3d, which formatted the deadline with the browser's clock: "bis 08:30 PM" in German.
test('AEON-1502 the verification deadline reads in the language’s own clock', () => {
  // Local wall-clock instants, so the assertion holds in every time zone.
  const now = new Date(2026, 8, 27, 20, 0).getTime()
  const evening = new Date(2026, 8, 27, 20, 30).toISOString(), morning = new Date(2026, 8, 27, 21, 5).toISOString()
  const tomorrow = new Date(2026, 8, 28, 8, 5).toISOString()
  for (const browser of ['en-US', 'en-GB', 'de-AT', 'fr-FR', '']) {
    assert.equal(deadlineMoment(evening, 'de', now, browser), '20:30', browser)
    assert.equal(deadlineMoment(morning, 'de', now, browser), '21:05', browser)
    assert.doesNotMatch(deadlineMoment(tomorrow, 'de', now, browser), /AM|PM/, browser)
  }
  // English keeps an English region's own clock; any other region reads plain English.
  assert.equal(deadlineMoment(evening, 'en', now, 'en-US'), '8:30\u00a0PM')
  assert.equal(deadlineMoment(evening, 'en', now, 'en-GB'), '20:30')
  assert.equal(deadlineMoment(evening, 'en', now, 'de-AT'), '8:30\u00a0PM')
  // Another day names the date; the moment never wraps.
  assert.match(deadlineMoment(tomorrow, 'de', now, 'de-AT'), /^28\.\u00a0Sep[^\s]*,\u00a008:05$/)
  assert.equal(/\s/.test(deadlineMoment(tomorrow, 'en', now, 'en-US').replace(/\u00a0/g, '')), false)
  assert.equal(deadlineMoment('not a date', 'de', now, 'de-AT'), 'not a date')
})

// Fails on bc1bde3d: numbers and units were joined by a breaking space, so "60 Sekunden" could wrap apart.
test('AEON-1502 a number and its unit never break apart on the card', () => {
  const terms = { policy: 'read_only', allowance: 1, unit: 'requests', runs_per_account: 1, max_parallel_runs: 1, max_duration_seconds: 60 }
  const de = pairingCopy('de', 'PAIMOS'), en = pairingCopy('en', 'PAIMOS')
  const [deLine] = de.limits(terms, '20:30'), [enLine] = en.limits(terms, '8:30\u00a0PM')
  for (const pair of ['max.\u00a060\u00a0Sekunden', '1\u00a0Anfrage', '1\u00a0Lauf', 'bis\u00a020:30']) assert.ok(deLine!.includes(pair), pair)
  for (const pair of ['60\u00a0seconds', '1\u00a0request', '1\u00a0run', 'until\u00a08:30\u00a0PM']) assert.ok(enLine!.includes(pair), pair)
  const many = { ...terms, allowance: 3, runs_per_account: 2, max_parallel_runs: 2 }
  assert.ok(de.limits(many, '20:30')[0]!.includes('3\u00a0Anfragen pro Harness\u00a0· 2\u00a0Läufe'))
  assert.ok(en.limits(many, '20:30')[0]!.includes('3\u00a0requests per harness\u00a0· 2\u00a0runs'))
  // The separator stays with the item before it: a wrapped line never starts with "·".
  for (const line of [deLine!, enLine!]) assert.doesNotMatch(line, / ·/)
  for (const sentence of [de.limitsDiffer, en.limitsDiffer]) assert.doesNotMatch(sentence, /\d (Anfrage|Lauf|Sekunden|request|run|second)/)
  assert.equal(de.accounts(2), '2\u00a0Konten')
  assert.equal(en.accounts(2), '2\u00a0accounts')
})

// Fails on bc1bde3d, whose German Details label was "Claude Code-Konto".
test('AEON-1502 the German account label reads Konto für the harness', () => {
  assert.equal(pairingCopy('de', 'PAIMOS').account('Claude Code'), 'Konto für Claude Code')
  assert.equal(pairingCopy('de', 'PAIMOS').account('Grok CLI'), 'Konto für Grok CLI')
  assert.equal(pairingCopy('en', 'PAIMOS').account('Claude Code'), 'Claude Code account')
})

// Fails on bc1bde3d, whose expired and denied lines were plain strings: the computer was not set in mono.
test('AEON-1502 expired and denied lines mark the computer for mono, as the ask does', () => {
  const computers = (parts: TitlePart[]) => parts.filter(part => part.kind === 'computer').map(part => part.text)
  const text = (parts: TitlePart[]) => parts.map(part => part.text).join('')
  for (const language of ['en', 'de'] as const) {
    const words = pairingCopy(language, 'PAIMOS')
    for (const parts of [words.expiredAdd(['Claude Code'], 'desk'), words.expiredPair('desk', 'INSPR'), words.deniedAdd(['Claude Code'], 'desk'), words.deniedPair('desk', 'INSPR'), words.runAgain('aeon-agentd add-harness --harness claude', 'desk')]) {
      assert.deepEqual(computers(parts), ['desk'], `${language}: ${text(parts)}`)
    }
    assert.deepEqual(words.runAgain('aeon-agentd add-harness', 'desk').filter(part => part.kind === 'command').map(part => part.text), ['aeon-agentd add-harness'])
  }
  assert.equal(text(pairingCopy('en', 'PAIMOS').expiredAdd(['Claude Code'], 'desk')), 'The request to add Claude Code to desk expired.')
  assert.equal(text(pairingCopy('de', 'PAIMOS').deniedPair('desk', 'INSPR')), 'Das Koppeln von desk mit INSPR wurde abgelehnt.')
  assert.equal(text(pairingCopy('de', 'PAIMOS').runAgain('aeon-agentd add-harness --harness claude', 'desk')), 'Zum erneuten Anfragen aeon-agentd add-harness --harness claude auf desk ausführen.')
})
