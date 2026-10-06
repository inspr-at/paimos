// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { DOCK_LIST_RESERVE, DOCK_MIN_WIDTH, dockPath, TYPES, isKnowledgeType, typeMeta, entryParam, parseEntryParam, plainError, slugProblem, slugify, suggestSlug, withoutTitle, applySteps, defaultChosen, recommendedSteps, KnowledgeError, type MethodLearning, type MethodLearningDecision } from '../src/lib/knowledge.ts'

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
  assert.equal(plainError(404, 'rule_unavailable', 'missing'), 'That rule set is not available.')
  assert.equal(plainError(403, 'rule_forbidden', 'no'), 'You cannot draft rules in that set.')
  assert.equal(plainError(409, 'already_in_set', 'dup'), 'This learning is already a rule in that set.')
  assert.equal(plainError(409, 'revision_conflict', 'stale'), 'That rule set changed. Open it and try again.')
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

test('Decision entries use the same Knowledge taxonomy, routing and history status', () => {
  assert.equal(isKnowledgeType('decision'), true)
  assert.equal(typeMeta('decision').label, 'Decision')
  assert.equal(TYPES[0].type, 'runbook')
  assert.deepEqual(parseEntryParam('decision/decision-history'), {type:'decision',slug:'decision-history'})
  assert.equal(dockPath('AEON','decision','decision-history'),'/p/AEON/knowledge?entry=decision/decision-history')
})

test('Decisions have a distinct icon from Guidelines in lists and graph legends', () => {
  assert.notEqual(typeMeta('decision').icon, typeMeta('guideline').icon)
})

test('applying recommendations sends only the chosen rows, and a failed row does not stop the rest', async () => {
  const by = { id: 'a1', name: 'Scout' }
  const base = { source: 'ticket' as const, node_id: 'n', title: 't', at: '2026-10-06T10:00:00Z', author: null, href: '/p/X/X-1' }
  const rec = (decision: 'accept' | 'dismiss', extra: object = {}) => ({ decision, by, at: base.at, event_id: 1, stale: false, target_missing: false, ...extra })
  const items: MethodLearning[] = [
    { ...base, id: 'n-1', key: 'X-1', text: 'Plain text', recommendation: rec('accept', { knowledge_id: 'k1', knowledge_title: 'Flywheel', lesson: 'A sharper lesson' }) },
    { ...base, id: 'n-2', key: 'X-2', text: 'Duplicate', recommendation: rec('dismiss', { reason: 'Already recorded' }) },
    { ...base, id: 'n-3', key: 'X-3', text: 'Changed since', recommendation: rec('dismiss', { reason: 'Old view', stale: true }) },
    { ...base, id: 'n-4', key: 'X-4', text: 'Entry gone', recommendation: rec('accept', { knowledge_id: 'k9', target_missing: true }) },
    { ...base, id: 'n-5', key: 'X-5', text: 'Opted out', recommendation: rec('dismiss', { reason: 'Noise' }) },
    { ...base, id: 'n-6', key: 'X-6', text: 'Will fail', recommendation: rec('accept', { knowledge_id: 'k1' }) },
    { ...base, id: 'n-7', key: 'X-7', text: 'No recommendation' },
  ]
  const steps = recommendedSteps(items)
  assert.deepEqual(steps.map(step => step.id), ['n-1', 'n-4', 'n-6', 'n-2', 'n-3', 'n-5'])
  const chosen = defaultChosen(steps)
  assert.deepEqual([...chosen].sort(), ['n-1', 'n-2', 'n-5', 'n-6'])
  chosen.delete('n-5') // the person opts one row out
  chosen.add('n-4') // a missing entry can never be applied, even when chosen
  const sent: string[] = []
  const decision = (id: string): MethodLearningDecision => ({ id, decision: 'accepted', event_id: 1 })
  const results = await applySteps(steps, chosen, {
    accept: async (id, knowledgeId, lesson) => {
      sent.push(`accept ${id} ${knowledgeId} ${lesson}`)
      if (id === 'n-6') throw new KnowledgeError(404, 'not_found', plainError(404, 'not_found', 'gone'))
      return decision(id)
    },
    dismiss: async (id, reason) => { sent.push(`dismiss ${id} ${reason}`); return decision(id) },
  })
  assert.deepEqual(sent, ['accept n-1 k1 A sharper lesson', 'accept n-6 k1 ', 'dismiss n-2 Already recorded'])
  assert.deepEqual(results.map(result => [result.id, result.ok]), [['n-1', true], ['n-6', false], ['n-2', true]])
  assert.equal(results[1].message, 'This entry no longer exists.')
})

test('a stopped run sends nothing after the stop', async () => {
  const steps = recommendedSteps([1, 2, 3].map(i => ({
    id: `n-${i}`, source: 'ticket' as const, node_id: 'n', key: `X-${i}`, title: 't', text: `T${i}`, at: '2026-10-06T10:00:00Z', author: null, href: '/',
    recommendation: { decision: 'dismiss' as const, reason: 'r', by: null, at: '2026-10-06T10:00:00Z', event_id: 1, stale: false, target_missing: false },
  })))
  const sent: string[] = []
  let stop = false
  await applySteps(steps, defaultChosen(steps), {
    accept: async () => { throw new Error('unexpected') },
    dismiss: async id => { sent.push(id); stop = true; return { id, decision: 'dismissed', event_id: 1 } },
  }, () => {}, () => stop)
  assert.deepEqual(sent, ['n-1'])
})
