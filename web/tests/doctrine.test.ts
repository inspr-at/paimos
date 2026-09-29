// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { RequestFailure } from '../src/lib/api.ts'
import {
  DoctrineError, doctrineMessage, proposalState, fileName, fileSummary, groupRules, lineLabel, parsePaths, pathsText, pinInput, pinLine, repoName, setHeading, shortSha, sourceText, stateLine, outcomeMetric, outcomeDelta,
  type DoctrineRule, type DoctrineFinding, type DoctrineMetric,
} from '../src/lib/doctrine.ts'

const COMMIT = '21b814057825c06b7f1e93f9deacdb4c549e11c6'
const rule = (patch: Partial<DoctrineRule> = {}): DoctrineRule => ({
  key: 't-1', identity: 'inspr-at/inspr-modules/docs/AGENTS-KERNEL.md#secrets', set: 'hard-safety/secrets', heading_path: 'Kernel / Hard safety / Secrets', anchor: 'secrets',
  start_line: 7, end_line: 7, source: '- 🔴 **NEVER** run `env`.\n', sha256: 'a'.repeat(64), text: '🔴 **NEVER** run `env`.', strength: 'locked',
  url: `https://github.com/inspr-at/inspr-modules/blob/${COMMIT}/docs/AGENTS-KERNEL.md?plain=1#L7`, ...patch,
})

test('the pin names the release, the commit and since when', () => {
  const line = pinLine({ ref: 'v260922101217.0.0', commit: COMMIT, pinned_at: '2026-09-29T10:00:00Z' })
  assert.match(line, /^v260922101217\.0\.0 · 21b8140 · pinned since /)
  assert.match(pinLine({ commit: COMMIT, pinned_at: '2026-09-29T10:00:00Z' }), /^commit 21b8140 · pinned since /)
  assert.equal(shortSha(COMMIT), '21b8140')
  assert.equal(repoName('inspr-at/inspr-doctrine-private'), 'inspr-doctrine-private')
  assert.equal(fileName('docs/AGENTS-KERNEL.md'), 'AGENTS-KERNEL.md')
})

test('rules show their exact lines and link label', () => {
  assert.equal(lineLabel(rule()), 'L7')
  assert.equal(lineLabel(rule({ end_line: 9 })), 'L7–9')
  // Only the final line break goes; CRLF inside stays byte for byte.
  assert.equal(sourceText(rule({ source: 'a\r\n  Why: b\r\n' })), 'a\r\n  Why: b')
  assert.equal(sourceText(rule({ source: 'no break' })), 'no break')
})

test('rules group under their heading in document order', () => {
  const groups = groupRules({
    sets: [{ set: 'git', title: 'Git', anchor: 'git' }, { set: 'hard-safety/secrets', title: 'Hard safety / Secrets', anchor: 'secrets' }, { set: 'empty', title: 'Empty', anchor: 'empty' }],
    rules: [rule({ key: 'a' }), rule({ key: 'b', set: 'git' }), rule({ key: 'c', set: 'orphan' })],
  })
  assert.deepEqual(groups.map(group => [group.set.set, group.rules.map(item => item.key)]), [['git', ['b']], ['hard-safety/secrets', ['a']], ['orphan', ['c']]])
  assert.equal(setHeading('Hard safety / Secrets'), 'Secrets')
  assert.equal(setHeading('Git'), 'Git')
  assert.equal(fileSummary({ rules: [rule(), rule({ strength: 'normal' })] }), '2 rules · 1 locked')
  assert.equal(fileSummary({ rules: [rule({ strength: 'normal' })] }), '1 rule')
})

test('a source without files says why in one line', () => {
  assert.equal(stateLine({ state: 'ready', files: [{} as never] }), null)
  assert.equal(stateLine({ state: 'not_indexed', files: [] }), 'Not read yet.')
  assert.equal(stateLine({ state: 'failed', error: 'GitHub answered 502 for the file list', files: [] }), 'Could not read this commit: GitHub answered 502 for the file list')
  assert.match(stateLine({ state: 'ready', files: [] }) ?? '', /No doctrine file matches/)
})

test('the form reads a pin and paths as typed', () => {
  assert.deepEqual(pinInput(` ${COMMIT} `), { commit: COMMIT })
  assert.deepEqual(pinInput('v260922101217.0.0'), { ref: 'v260922101217.0.0' })
  assert.deepEqual(pinInput('21b8140'), { ref: '21b8140' })
  assert.deepEqual(parsePaths('docs/AGENTS-*.md\n\n AGENTS.md , docs/AGENTS-*.md'), ['docs/AGENTS-*.md', 'AGENTS.md'])
  assert.equal(pathsText(['a', 'b']), 'a\nb')
})

test('failures read as one plain line', () => {
  assert.equal(doctrineMessage(new DoctrineError(422, 'git_unavailable', 'credential doctrine-private-read is not provisioned on this server')), 'credential doctrine-private-read is not provisioned on this server')
  assert.equal(doctrineMessage(new DoctrineError(403, 'forbidden', 'permission denied')), 'You do not have permission for that.')
  assert.match(doctrineMessage(new RequestFailure('timeout')), /Still reading the repository/)
})

test('proposal states distinguish release requests and reported machine pins', () => {
  assert.equal(proposalState({ state: 'in_review', pinned_machines: 0, draft: true }), 'Draft')
  assert.equal(proposalState({ state: 'merged', pinned_machines: 0 }), 'Merged')
  assert.equal(proposalState({ state: 'released', pinned_machines: 0 }), 'Released')
  assert.equal(proposalState({ state: 'pinned', pinned_machines: 1 }), 'Pinned on 1 reported machine')
  assert.equal(proposalState({ state: 'pinned', pinned_machines: 3 }), 'Pinned on 3 reported machines')
  assert.equal(proposalState({ state: 'proposed', pinned_machines: 0, orphaned: true }), 'Branch left on GitHub')
})

test('outcome rates and deltas retain their units and do not invent missing measurements', () => {
  const before = { name: 'gate:validation', value: .75 } as DoctrineMetric
  assert.equal(outcomeMetric(before), '75%')
  assert.equal(outcomeMetric({ ...before, name: 'fix_rounds', value: 2.5 }), '2.5 rounds')
  assert.equal(outcomeMetric({ ...before, name: 'time_to_done', value: 180 }), '3 min')
  assert.equal(outcomeDelta({ before, delta: -.25 } as DoctrineFinding), '−25 percentage points')
  assert.equal(outcomeDelta({ before, delta: 0 } as DoctrineFinding), '0 percentage points')
  assert.equal(outcomeDelta({ before } as DoctrineFinding), '')
})
