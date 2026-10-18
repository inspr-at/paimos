// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, writeFileSync, symlinkSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { spawnSync } from 'node:child_process'
import { draftSelfcheck, validateSelfcheck, snapshot, selfcheckMain, readJSON, maxEvidenceBytes } from '../../scripts/review-selfcheck.mjs'
import { weekWindow, collectMerged, auditPlan, auditFacts, auditMain } from '../../scripts/review-audit.mjs'

const head = 'b'.repeat(40), base = 'a'.repeat(40), merge = 'c'.repeat(40)
const state = { head_sha: head, base_sha: base, paths: ['scripts/example.mjs'] }
const window = { start: '2026-09-28T00:00:00.000Z', end: '2026-10-05T00:00:00.000Z' }
const now = new Date('2026-10-05T06:00:00Z')
function completeSelfcheck(binding = state) {
  const evidence = draftSelfcheck(binding)
  Object.assign(evidence, { ticket: 'AEON-1021', builder: 'builder-session', baseline: { observation: 'No self-check producer.', evidence: 'base: scripts inventory' },
    instrumentation: { measure: 'review changes share', evidence: 'Delivery metric review_changes_share' },
    tests: [{ risk: 'stale evidence', command: 'node --test web/tests/review-tooling.test.ts', status: 'passed', evidence: 'test report' }],
    follow_up: { owner: 'coordinator', measure: 'review changes and audit findings', due: '7 days after deployment' } })
  evidence.checks.forEach(row => Object.assign(row, { status: 'pass', evidence: 'Reviewed changed paths and corresponding behavior test.' }))
  return evidence
}
function inventory(count = 7) {
  return { schema: 1, repository: 'example/aeon', window, complete: true, pulls: Array.from({ length: count }, (_, index) => ({
    number: index + 1, head_sha: head, merge_sha: merge, merged_at: '2026-10-02T00:00:00Z' })) }
}
function results(plan) {
  return { schema: 1, audits: plan.selected.map(row => ({ pull_request: row.number, head_sha: row.head_sha, merge_sha: row.merge_sha,
    auditor: 'audit-session', auditor_family: 'openai', original_reviewer: 'review-session', original_reviewer_family: 'anthropic',
    at: now.toISOString(), status: 'completed', evidence: 'ticket attachment: independent full diff review', follow_up_owner: 'coordinator', findings: [] })) }
}
function temporary(t) {
  const root = mkdtempSync(join(tmpdir(), 'aeon-review-tooling-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  return root
}

test('builder self-check refuses missing, stale, failed and bypassed evidence before review', () => {
  // Risk: partial builder assertions are treated as review or CI approval.
  assert.equal(validateSelfcheck(completeSelfcheck(), state).status, 'ready_for_review')
  assert.throws(() => validateSelfcheck(draftSelfcheck(state), state), /invalid_ticket/)
  const mutations = [
    [value => value.head_sha = base, /snapshot_mismatch/],
    [value => value.base_sha = head, /snapshot_mismatch/],
    [value => value.paths = [], /snapshot_mismatch/],
    [value => value.baseline.evidence = '', /invalid_baseline_evidence/],
    [value => value.instrumentation.measure = '', /invalid_instrumentation_measure/],
    [value => value.follow_up.owner = '', /invalid_follow_up_owner/],
    [value => value.follow_up.due = 'later', /invalid_follow_up_due/],
    [value => value.tests = [], /tests_missing/],
    [value => value.tests[0].status = 'skipped', /test_not_passed/],
    [value => value.tests[0].risk = '', /invalid_test_risk/],
    [value => value.checks.pop(), /rules_incomplete/],
    [value => value.checks[1] = value.checks[0], /duplicate/],
    [value => value.checks[0].status = 'fail', /rule_unresolved/],
    [value => value.checks[0].evidence = '', /invalid_rule_evidence/],
    [value => value.checks.find(row => row.id === 'gates').status = 'not_applicable', /rule_unresolved/],
  ]
  for (const [mutate, reason] of mutations) {
    const value = completeSelfcheck(); mutate(value)
    assert.throws(() => validateSelfcheck(value, state), reason)
  }
  const value = completeSelfcheck()
  value.checks[0].status = 'not_applicable'; value.checks[0].evidence = 'Read-only offline tools have no database mutation.'
  assert.equal(validateSelfcheck(value, state).status, 'ready_for_review')
})

test('self-check CLI binds real commits and refuses dirty, untracked or moved snapshots', t => {
  // Risk: a receipt for an earlier SHA authorizes modified work.
  const root = temporary(t)
  const git = (...args) => {
    const result = spawnSync('git', args, { cwd: root, encoding: 'utf8' })
    assert.equal(result.status, 0, result.stderr); return result.stdout.trim()
  }
  git('init', '-q'); git('config', 'user.name', 'Review fixture'); git('config', 'user.email', 'fixture@example.invalid')
  writeFileSync(join(root, '.gitignore'), '/tmp/\n'); git('add', '.gitignore'); git('commit', '-qm', 'baseline'); git('branch', 'baseline')
  writeFileSync(join(root, 'changed.mjs'), '// fixture\n'); git('add', 'changed.mjs'); git('commit', '-qm', 'change')
  mkdirSync(join(root, 'tmp'))
  const binding = snapshot(root, 'baseline'), file = join(root, 'tmp/evidence.json')
  assert.deepEqual(binding.paths, ['changed.mjs'])
  assert.equal(selfcheckMain(['init', '--base', 'baseline', '--output', file], root).status, 'draft')
  assert.throws(() => selfcheckMain(['init', '--base', 'baseline', '--output', file], root), /EEXIST/)
  writeFileSync(file, JSON.stringify(completeSelfcheck(binding)))
  assert.equal(selfcheckMain(['check', '--base', 'baseline', '--input', file], root).status, 'ready_for_review')
  writeFileSync(join(root, 'changed.mjs'), '// modified\n')
  assert.throws(() => snapshot(root, 'baseline'), /clean_committed_tree/)
  git('add', 'changed.mjs'); git('commit', '-qm', 'new head')
  assert.throws(() => selfcheckMain(['check', '--base', 'baseline', '--input', file], root), /snapshot_mismatch/)
  writeFileSync(join(root, 'untracked.mjs'), '// fixture')
  assert.throws(() => snapshot(root, 'baseline'), /clean_committed_tree/)
  assert.throws(() => snapshot(root, '--bad-ref'), /invalid_base_ref/)
})

test('evidence input refuses oversized, malformed and symlinked files without echoing contents', t => {
  // Risk: evidence readers allocate unbounded data or follow a credential link.
  const root = temporary(t), file = join(root, 'evidence.json')
  writeFileSync(file, 'x'.repeat(maxEvidenceBytes + 1))
  assert.throws(() => readJSON(file), /oversized/)
  writeFileSync(file, 'malformed private fixture')
  assert.throws(() => readJSON(file), /evidence_json_invalid/)
  symlinkSync(file, join(root, 'link.json'))
  assert.throws(() => readJSON(join(root, 'link.json')), /ELOOP/)
})

test('weekly audit collection paginates, checks completeness and uses an exclusive UTC week end', () => {
  // Risk: an incomplete inventory hides merged PRs and biases the audit sample.
  assert.deepEqual(weekWindow(new Date('2026-10-05T06:00:00Z')), window)
  assert.deepEqual(weekWindow(new Date('2026-10-11T23:59:59Z')), window)
  const row = number => ({ number, updated_at: '2026-10-06T00:00:00Z', merged_at: '2026-10-02T00:00:00Z',
    base: { repo: { full_name: 'example/aeon' } }, head: { sha: head }, merge_commit_sha: merge, body: 'must not be retained' })
  const pages = []
  const collected = collectMerged('example/aeon', window, { readPage: (_, page) => {
    pages.push(page)
    return page === 1 ? Array.from({ length: 25 }, (_, index) => row(index + 1)) : [
      { ...row(101), merged_at: window.end }, { ...row(102), merged_at: window.start }, { ...row(103), merged_at: null }]
  } })
  assert.deepEqual(pages, [1, 2]); assert.equal(collected.pulls.length, 26)
  assert.equal(collected.pulls.some(row => row.number === 101), false)
  assert.equal(JSON.stringify(collected).includes('must not be retained'), false)
  assert.throws(() => collectMerged('example/aeon', window, { readPage: (_, page) => Array.from({ length: 25 }, (_, i) => row(page * 25 + i)) }), /incomplete_page_cap/)
  assert.throws(() => collectMerged('example/aeon', window, { readPage: () => [row(1), row(1)] }), /duplicate_or_unordered/)
  assert.throws(() => collectMerged('example/aeon', window, { readPage: () => { throw new Error('read_refused') } }), /read_refused/)
  assert.throws(() => collectMerged('example/aeon', window, { readPage: () => [{ ...row(1), head: { sha: '' } }] }), /binding_invalid/)
})

test('weekly audit sampling is deterministic and refuses truncated or ambiguous inventories', () => {
  // Risk: order changes, duplicates or truncation let the coordinator cherry-pick a sample.
  const source = inventory(), plan = auditPlan(source)
  assert.equal(plan.population, 7); assert.equal(plan.selected.length, 5)
  assert.deepEqual(auditPlan({ ...source, pulls: [...source.pulls].reverse() }), plan)
  assert.equal(auditPlan(inventory(2)).selected.length, 2)
  assert.equal(auditPlan(inventory(0)).selected.length, 0)
  assert.throws(() => auditPlan({ ...source, complete: false }), /inventory_or_sample_invalid/)
  assert.throws(() => auditPlan({ ...source, pulls: [source.pulls[0], source.pulls[0]] }), /binding_invalid/)
  assert.throws(() => auditPlan({ ...source, pulls: [{ ...source.pulls[0], merged_at: window.end }] }), /outside_week/)
  assert.throws(() => auditPlan(source, 21), /inventory_or_sample_invalid/)
  assert.throws(() => auditPlan({ ...source, window: { ...window, start: '2026-02-30T00:00:00Z' } }), /timestamp/)
})

test('audit facts require completed independent reports and retain the highest finding severity', () => {
  // Risk: a missing/same-family/stale audit becomes clean or loses serious findings.
  const plan = auditPlan(inventory(2)), source = results(plan)
  source.audits[0].findings = ['low', 'critical'].map(severity => ({ severity, evidence: 'verified changed path', ticket: 'AEON-1021', owner: 'coordinator' }))
  const facts = auditFacts(plan, source, { now }).facts
  assert.equal(facts.find(row => row.pull_request === source.audits[0].pull_request).outcome, 'high')
  assert.equal(source.audits[0].findings[1].severity, 'critical')
  assert.equal(facts.find(row => row.pull_request === source.audits[1].pull_request).outcome, 'clean')
  assert.deepEqual(auditFacts(plan, source, { now }), auditFacts(plan, source, { now }))
  const mutations = [
    [value => value.audits.pop(), /results_incomplete/],
    [value => value.audits[0].status = 'pending', /result_incomplete/],
    [value => value.audits[0].findings = null, /result_incomplete/],
    [value => value.audits[0].auditor_family = 'anthropic', /cross_family/],
    [value => value.audits[0].auditor_family = 'codex', /cross_family/],
    [value => value.audits[0].auditor = 'review-session', /cross_family/],
    [value => value.audits[0].head_sha = base, /snapshot_mismatch/],
    [value => value.audits[0].merge_sha = base, /snapshot_mismatch/],
    [value => value.audits[1] = value.audits[0], /snapshot_mismatch/],
    [value => value.audits[0].at = '2026-10-06T00:00:00Z', /time_invalid/],
    [value => value.audits[0].evidence = '', /invalid_audit_evidence/],
    [value => value.audits[0].findings[0].ticket = '', /invalid_finding_ticket/],
  ]
  for (const [mutate, reason] of mutations) {
    const value = structuredClone(source); mutate(value)
    assert.throws(() => auditFacts(plan, value, { now }), reason)
  }
  assert.throws(() => auditFacts(auditPlan(inventory(0)), { schema: 1, audits: [] }, { now }), /results_incomplete/)
})

test('offline audit CLI prepares API-compatible facts and rejects tampered selection and overwrites', t => {
  // Risk: editing a plan substitutes an unaudited PR or silently rewrites evidence.
  const root = temporary(t), source = inventory(), sourcePath = join(root, 'inventory.json'), planPath = join(root, 'plan.json')
  writeFileSync(sourcePath, JSON.stringify(source))
  assert.equal(auditMain(['plan', '--inventory', sourcePath, '--output', planPath]).status, 'awaiting_independent_audits')
  const plan = readJSON(planPath)
  assert.equal(plan.results_template.audits.every(row => row.status === 'pending'), true)
  const completed = results(plan)
  const resultPath = join(root, 'results.json'), factsPath = join(root, 'facts.json')
  writeFileSync(resultPath, JSON.stringify(completed))
  assert.equal(auditMain(['facts', '--plan', planPath, '--results', resultPath, '--output', factsPath], { now }).status, 'facts_prepared_not_submitted')
  const payload = readJSON(factsPath)
  assert.deepEqual(Object.keys(payload), ['facts']); assert.equal(payload.facts.length, 5)
  assert.deepEqual(Object.keys(payload.facts[0]), ['kind', 'key', 'pull_request', 'head_sha', 'at', 'outcome'])
  assert.throws(() => auditMain(['facts', '--plan', planPath, '--results', resultPath, '--output', factsPath], { now }), /EEXIST/)
  plan.selected[0].number = 999
  writeFileSync(planPath, JSON.stringify(plan))
  assert.throws(() => auditMain(['facts', '--plan', planPath, '--results', resultPath, '--output', join(root, 'tampered.json')], { now }), /saved_plan_mismatch/)
})
