// SPDX-License-Identifier: AGPL-3.0-only
import { spawnSync } from 'node:child_process'
import { closeSync, constants, fstatSync, openSync, readSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

export const maxEvidenceBytes = 1024 * 1024
export const reviewRules = [
  ['authorization', 'RequireTx checks the current target inside the final write transaction under the access-change lock.'],
  ['locks', 'Tenant → tree → rows → sorted blob batch → event counter last; FK-compatible fences.'],
  ['bounds', 'Size and time bounds precede body/list/page/decode/diff/external-response work; bounded keyset lists.'],
  ['identity', 'Actions capture record id/revision; stale async results, Undo and drafts are discarded; person state resets.'],
  ['honesty', 'Failed or partial writes, searches, syncs and deliveries expose errors and truncation.'],
  ['tests', 'Behavior tests name the risk; barriers/clocks prove interleavings; fixtures/assertions reject wrong failures.'],
  ['layout', 'Shared UI conventions in both themes; controls stay put (±0.5 px), short frames stay short.'],
  ['compatibility', 'Additive authored API contract, expand-only migrations, DSAR and strict route/schema inventories.'],
  ['ownership', 'Only assigned scope changed; new source and tests have ownership and test classification.'],
  ['gates', 'No test, review, required CI, human approval or release gate is weakened or bypassed.'],
].map(([id, rule]) => ({ id, rule }))

export function requireText(value, label, max = 4096) {
  if (typeof value !== 'string' || !value.trim() || value.length > max || /[\u0000-\u0008\u000b\u000c\u000e-\u001f]/.test(value)) throw new Error(`invalid_${label}`)
  return value
}
function readBoundedJSON(fd, buffer) {
  let bytes = 0, count
  while (bytes < buffer.length && (count = readSync(fd, buffer, bytes, buffer.length - bytes, null))) bytes += count
  if (bytes > maxEvidenceBytes) throw new Error('evidence_file_invalid_or_oversized')
  try { return JSON.parse(buffer.subarray(0, bytes).toString('utf8')) }
  catch { throw new Error('evidence_json_invalid') }
}

function git(root, args) {
  const result = spawnSync('git', args, { cwd: root, encoding: 'utf8', timeout: 15_000, maxBuffer: maxEvidenceBytes })
  // Do not echo child output: a remote error or repository path may be private.
  if (result.error || result.status !== 0) throw new Error('selfcheck_git_failed')
  return result.stdout.replace(/\n$/, '')
}
export function snapshot(root = process.cwd(), base = 'origin/main') {
  if (typeof base !== 'string' || !/^[A-Za-z0-9][A-Za-z0-9/._-]{0,199}$/.test(base)) throw new Error('invalid_base_ref')
  const head_sha = git(root, ['rev-parse', '--verify', 'HEAD^{commit}'])
  const base_sha = git(root, ['rev-parse', '--verify', `${base}^{commit}`])
  if (git(root, ['status', '--porcelain', '--untracked-files=normal'])) throw new Error('selfcheck_requires_clean_committed_tree')
  const paths = git(root, ['diff', '--no-renames', '--name-only', '-z', `${base_sha}...${head_sha}`, '--']).split('\0').filter(Boolean).sort()
  if (paths.length > 5000) throw new Error('selfcheck_diff_oversized')
  if (head_sha !== git(root, ['rev-parse', '--verify', 'HEAD^{commit}']) ||
      base_sha !== git(root, ['rev-parse', '--verify', `${base}^{commit}`]) ||
      git(root, ['status', '--porcelain', '--untracked-files=normal'])) throw new Error('selfcheck_snapshot_changed_during_read')
  return { head_sha, base_sha, paths }
}
export function draftSelfcheck(state) {
  return { schema: 1, ...state, ticket: '', builder: '', baseline: { observation: '', evidence: '' },
    instrumentation: { measure: '', evidence: '' }, tests: [{ risk: '', command: '', status: 'pending', evidence: '' }],
    follow_up: { owner: '', measure: '', due: '7 days after deployment' },
    checks: reviewRules.map(row => ({ ...row, status: 'pending', evidence: '' })) }
}
export function validateSelfcheck(input, state) {
  if (input?.schema !== 1 || input.head_sha !== state.head_sha || input.base_sha !== state.base_sha ||
      JSON.stringify(input.paths) !== JSON.stringify(state.paths)) throw new Error('selfcheck_snapshot_mismatch')
  if (!/^AEON-[1-9][0-9]*$/.test(input.ticket ?? '')) throw new Error('invalid_ticket')
  requireText(input.builder, 'builder', 200)
  for (const [section, fields] of [['baseline', ['observation', 'evidence']], ['instrumentation', ['measure', 'evidence']], ['follow_up', ['owner', 'measure']]]) {
    for (const field of fields) requireText(input[section]?.[field], `${section}_${field}`)
  }
  if (input.follow_up.due !== '7 days after deployment') throw new Error('invalid_follow_up_due')
  if (!Array.isArray(input.tests) || !input.tests.length || input.tests.length > 100) throw new Error('selfcheck_tests_missing_or_oversized')
  for (const row of input.tests) {
    for (const field of ['risk', 'command', 'evidence']) requireText(row?.[field], `test_${field}`)
    if (row.status !== 'passed') throw new Error('selfcheck_test_not_passed')
  }
  if (!Array.isArray(input.checks) || input.checks.length !== reviewRules.length) throw new Error('selfcheck_rules_incomplete')
  const seen = new Set()
  for (const row of input.checks) {
    if (!reviewRules.some(rule => rule.id === row?.id) || seen.has(row.id)) throw new Error('selfcheck_rule_invalid_or_duplicate')
    seen.add(row.id)
    requireText(row.evidence, 'rule_evidence')
    if (!['pass', 'not_applicable'].includes(row.status) || ['tests', 'ownership', 'gates'].includes(row.id) && row.status !== 'pass') throw new Error('selfcheck_rule_unresolved')
  }
  return { schema: 1, status: 'ready_for_review', ticket: input.ticket, ...state,
    scope: 'Builder assertions validated for completeness; independent review and all existing gates still required.' }
}

export function selfcheckMain(args, root = process.cwd()) {
  const [command, ...flags] = args, options = {}
  if (!['init', 'check'].includes(command)) throw new Error('usage_selfcheck_init_or_check')
  for (let i = 0; i < flags.length; i += 2) {
    const key = flags[i]
    if (!['--input', '--output', '--base'].includes(key) || options[key] || !flags[i + 1]) throw new Error('invalid_selfcheck_arguments')
    options[key] = flags[i + 1]
  }
  const state = snapshot(root, options['--base'])
  if (command === 'init') {
    if (!options['--output'] || options['--input']) throw new Error('selfcheck_init_requires_output')
    writeFileSync(resolve(root, options['--output']), JSON.stringify(draftSelfcheck(state), null, 2) + '\n', { flag: 'wx', mode: 0o600 })
    return { status: 'draft', ...state }
  }
  if (!options['--input'] || options['--output']) throw new Error('selfcheck_check_requires_input')
  const evidence = readJSON(resolve(root, options['--input']))
  return validateSelfcheck(evidence, snapshot(root, options['--base']))
}
if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  try { console.log(JSON.stringify(selfcheckMain(process.argv.slice(2)))) }
  catch (error) {
    const reason = /^(?:invalid|selfcheck|evidence|usage)_[a-z_]+$/.test(error.message) ? error.message : 'file_or_setup_error'
    console.error(`Self-check incomplete (${reason}): use init --output PATH or check --input PATH [--base REF]; commit changes and complete current-SHA evidence.`)
    process.exitCode = 1
  }
}
export function readJSON(file) {
  // Bound and inspect the opened descriptor before reading; never follow a
  // symlink into credential files. Inputs are sanitized evidence, not logs.
  const fd = openSync(file, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK)
  try {
    const stat = fstatSync(fd)
    if (!stat.isFile() || stat.size > maxEvidenceBytes) throw new Error('evidence_file_invalid_or_oversized')
    const buffer = Buffer.alloc(maxEvidenceBytes + 1)
    // readFileSync would allow a file that grew after fstat to allocate without
    // a bound. A capped read also detects that race.
    return readBoundedJSON(fd, buffer)
  } finally { closeSync(fd) }
}
