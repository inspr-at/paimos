// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { DOCK_LIST_RESERVE, DOCK_MIN_WIDTH, dockPath, entryParam, parseEntryParam, plainError, slugProblem, slugify, suggestSlug, withoutTitle } from '../src/lib/knowledge.ts'

test('the docked entry lives in ?entry=<type>/<slug>, and only real kinds and slugs count', () => {
  assert.equal(entryParam('guideline', 'adr-001-foundation'), 'guideline/adr-001-foundation')
  assert.deepEqual(parseEntryParam('guideline/adr-001-foundation'), { type: 'guideline', slug: 'adr-001-foundation' })
  assert.deepEqual(parseEntryParam('external-system/hetzner'), { type: 'external-system', slug: 'hetzner' })
  for (const bad of ['recipe/x', 'runbook/', '/x', 'runbook', '', 42, null, ['runbook/x']]) assert.equal(parseEntryParam(bad), null, String(bad))
  assert.equal(dockPath('PHAROS', 'runbook', 'deploy-release'), '/p/PHAROS/knowledge?entry=runbook/deploy-release')
})

test('the docking width leaves the list 560px beside a 560px pane', () => {
  assert.equal(DOCK_LIST_RESERVE, 560 + 28 + 22 + 10)
  assert.ok(DOCK_MIN_WIDTH - DOCK_LIST_RESERVE >= 560)
})

test('a body heading that repeats the title, or starts it, is not shown twice', () => {
  assert.equal(withoutTitle('# Deploy flow\n\nBuild it.', 'Deploy flow'), 'Build it.')
  assert.equal(withoutTitle('# ADR-001 · Aeon foundation\n\nStatus: accepted.', 'ADR-001 · Aeon foundation (accepted)'), 'Status: accepted.')
  assert.equal(withoutTitle('# Steps\n\nFirst.', 'Steps to take'), '# Steps\n\nFirst.')
  assert.equal(withoutTitle('# Rollback\n\nRevert.', 'Deploy flow'), '# Rollback\n\nRevert.')
})

test('method-learning errors stay specific when the status is also 403 or 404', () => {
  assert.equal(plainError(403, 'person_required', 'only a person'), 'Only a person can accept or dismiss a method learning.')
  assert.equal(plainError(404, 'learning_closed', 'closed'), 'This learning is no longer open.')
  assert.equal(plainError(409, 'already_decided', 'done'), 'This learning was already accepted or dismissed.')
  assert.equal(plainError(403, 'forbidden', 'no'), 'You can read knowledge here but not change it.')
  assert.equal(plainError(404, 'not_found', 'gone'), 'This entry no longer exists.')
})

test('slugs: suggestions from titles, unique, and the rules agents rely on', () => {
  assert.equal(slugify('Über die Brücke: Deploy (v2)!'), 'ueber-die-bruecke-deploy-v2')
  assert.equal(slugify('2026 plan', 'memory'), 'm-2026-plan')
  assert.equal(suggestSlug('Plain words', 'guideline', ['plain-words']), 'plain-words-2')
  assert.equal(slugProblem('runbook', 'Ship It'), 'Use lower-case letters, digits, - and _ only.')
  assert.equal(slugProblem('runbook', '9lives'), 'Start with a letter.')
  assert.match(slugProblem('memory', 'stale'), /reserved/)
  assert.equal(slugProblem('runbook', 'ok_slug-2'), '')
})
