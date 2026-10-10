// SPDX-License-Identifier: AGPL-3.0-only
import { createHash } from 'node:crypto'
import { spawnSync } from 'node:child_process'
import { writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { maxEvidenceBytes, readJSON, requireText } from './review-selfcheck.mjs'

const sha = /^[a-f0-9]{40}$/
// Canonical family IDs from internal/reviewgate/result.go; harness names are
// not families ("codex" cannot masquerade as a second OpenAI family).
const families = ['openai', 'anthropic', 'xai', 'cursor', 'google', 'local']
// Small pages keep GitHub's full PR objects within the pre-decode byte bound.
// The total scan is bounded too: at most 500 closed PRs / five minutes of reads.
const maxPages = 20, perPage = 25
function repository(value) {
  if (typeof value !== 'string' || value.length > 200 || !/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(value) || value.split('/').some(part => ['.', '..'].includes(part))) throw new Error('invalid_audit_repository')
  return value
}
function instant(value) {
  if (typeof value !== 'string' || !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{3})?Z$/.test(value)) throw new Error('invalid_audit_timestamp')
  const time = new Date(value)
  if (!Number.isFinite(time.getTime()) || time.toISOString().replace('.000Z', 'Z') !== value.replace('.000Z', 'Z')) throw new Error('invalid_audit_timestamp')
  return time.getTime()
}
export function weekWindow(now = new Date()) {
  if (!Number.isFinite(now.getTime())) throw new Error('invalid_audit_clock')
  const end = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate()))
  end.setUTCDate(end.getUTCDate() - (end.getUTCDay() + 6) % 7)
  return { start: new Date(end.getTime() - 7 * 86400000).toISOString(), end: end.toISOString() }
}
function windowTimes(window) {
  const start = instant(window?.start), end = instant(window?.end)
  if (end - start !== 7 * 86400000 || new Date(start).getUTCDay() !== 1 || new Date(start).getUTCHours() !== 0 ||
      new Date(start).getUTCMinutes() !== 0 || new Date(start).getUTCSeconds() !== 0 || new Date(start).getUTCMilliseconds() !== 0) throw new Error('invalid_audit_week')
  return { start, end }
}
export function collectMerged(repo, window, { readPage = githubPage } = {}) {
  repository(repo)
  const { start, end } = windowTimes(window), rows = [], seen = new Set()
  let previousUpdated = Infinity
  for (let page = 1; page <= maxPages; page++) {
    const data = readPage(repo, page)
    if (!Array.isArray(data) || data.length > perPage) throw new Error('audit_page_invalid')
    for (const row of data) {
      const updated = instant(row?.updated_at)
      if (!Number.isInteger(row?.number) || row.number < 1 || row.number > 2147483647 || seen.has(row.number) || updated > previousUpdated) throw new Error('audit_inventory_duplicate_or_unordered')
      seen.add(row.number); previousUpdated = updated
      if (row.merged_at === null) continue
      const merged = instant(row.merged_at)
      if (merged > updated) throw new Error('audit_merge_after_update')
      if (merged < start || merged >= end) continue
      if (row.base?.repo?.full_name !== repo || !sha.test(row.head?.sha ?? '') || !sha.test(row.merge_commit_sha ?? '')) throw new Error('audit_pr_binding_invalid')
      // Retain identities only, never titles, bodies, private prompts or logs.
      rows.push({ number: row.number, head_sha: row.head.sha, merge_sha: row.merge_commit_sha, merged_at: row.merged_at })
    }
    if (data.length < perPage || previousUpdated < start) return { schema: 1, repository: repo, window, complete: true, pulls: rows }
  }
  throw new Error('audit_collection_incomplete_page_cap')
}
function githubPage(repo, page) {
  const result = spawnSync('gh', ['api', `repos/${repo}/pulls?state=closed&sort=updated&direction=desc&per_page=${perPage}&page=${page}`],
    { encoding: 'utf8', timeout: 15_000, maxBuffer: maxEvidenceBytes })
  if (result.error || result.status !== 0) throw new Error('audit_github_read_failed')
  try { return JSON.parse(result.stdout) } catch { throw new Error('audit_github_json_invalid') }
}
function digest(value) { return createHash('sha256').update(value).digest('hex') }
export function auditPlan(inventory, sample = 5) {
  const repo = repository(inventory?.repository), { start, end } = windowTimes(inventory?.window)
  if (inventory.schema !== 1 || inventory.complete !== true || !Array.isArray(inventory.pulls) || inventory.pulls.length > maxPages * perPage ||
      !Number.isInteger(sample) || sample < 1 || sample > 20) throw new Error('audit_inventory_or_sample_invalid')
  const seen = new Set()
  const pulls = inventory.pulls.map(row => {
    if (!Number.isInteger(row?.number) || row.number < 1 || row.number > 2147483647 || seen.has(row.number) || !sha.test(row.head_sha ?? '') || !sha.test(row.merge_sha ?? '')) throw new Error('audit_pr_binding_invalid')
    seen.add(row.number)
    const merged = instant(row.merged_at)
    if (merged < start || merged >= end) throw new Error('audit_pr_outside_week')
    return { number: row.number, head_sha: row.head_sha, merge_sha: row.merge_sha, merged_at: row.merged_at,
      key: `audit:${digest(repo).slice(0, 16)}:${inventory.window.start.slice(0, 10)}:pr-${row.number}`,
      rank: digest(`${repo}:${inventory.window.start}:${row.number}:${row.merge_sha}`) }
  })
  pulls.sort((a, b) => a.rank.localeCompare(b.rank) || a.number - b.number)
  return { schema: 1, repository: repo, window: inventory.window, complete: true, population: pulls.length, sample_size: sample,
    method: 'Lowest SHA-256(repository, week start, PR, merge SHA); all when population is smaller than sample.',
    selected: pulls.slice(0, sample).map(({ rank, ...row }) => row) }
}
export function auditFacts(plan, results, { now = new Date() } = {}) {
  // Recompute selection from the full retained inventory at the CLI boundary.
  // Auditing statements are assertions: the coordinator verifies model/session
  // attribution and attaches the original reports before submitting these facts.
  repository(plan?.repository); windowTimes(plan?.window)
  if (plan.schema !== 1 || plan.complete !== true || !Array.isArray(plan.selected) || !plan.selected.length || plan.selected.length > 20 ||
      results?.schema !== 1 || !Array.isArray(results.audits) || results.audits.length !== plan.selected.length) throw new Error('audit_results_incomplete')
  const seen = new Set(), selected = new Map(plan.selected.map(row => [row.number, row]))
  if (selected.size !== plan.selected.length) throw new Error('audit_selection_duplicate')
  const facts = results.audits.map(row => {
    const target = selected.get(row?.pull_request)
    if (!target || seen.has(row.pull_request) || row.head_sha !== target.head_sha || row.merge_sha !== target.merge_sha) throw new Error('audit_result_snapshot_mismatch')
    seen.add(row.pull_request)
    for (const field of ['auditor', 'original_reviewer', 'evidence', 'follow_up_owner']) requireText(row[field], `audit_${field}`)
    if (!families.includes(row.auditor_family) || !families.includes(row.original_reviewer_family) ||
        row.auditor_family === row.original_reviewer_family ||
        row.auditor === row.original_reviewer) throw new Error('audit_needs_independent_cross_family_reviewer')
    const at = instant(row.at)
    if (!Number.isFinite(now.getTime()) || at < instant(target.merged_at) || at < instant(plan.window.end) || at > now.getTime() + 5 * 60000 || at < now.getTime() - 400 * 86400000) throw new Error('audit_result_time_invalid')
    if (row.status !== 'completed' || !Array.isArray(row.findings) || row.findings.length > 100) throw new Error('audit_result_incomplete')
    let severity = 0
    for (const finding of row.findings) {
      const rank = ['low', 'medium', 'high', 'critical'].indexOf(finding?.severity)
      if (rank < 0) throw new Error('audit_finding_severity_invalid')
      for (const field of ['evidence', 'ticket', 'owner']) requireText(finding[field], `finding_${field}`)
      if (!/^AEON-[1-9][0-9]*$/.test(finding.ticket)) throw new Error('audit_finding_needs_ticket')
      severity = Math.max(severity, rank + 1)
    }
    // Delivery's existing enum has no critical: retain critical in the report,
    // count it as high in this metric and escalate through the coordinator.
    return { kind: 'review_audit', key: target.key, pull_request: row.pull_request, head_sha: row.head_sha,
      at: row.at, outcome: ['clean', 'low', 'medium', 'high', 'high'][severity] }
  })
  return { facts: facts.sort((a, b) => a.pull_request - b.pull_request) }
}
export function auditMain(args, { now = new Date() } = {}) {
  const [command, ...flags] = args, options = {}
  if (!['plan', 'facts'].includes(command)) throw new Error('usage_audit_plan_or_facts')
  for (let i = 0; i < flags.length; i += 2) {
    const key = flags[i]
    if (!['--repository', '--inventory', '--plan', '--results', '--output', '--sample'].includes(key) || options[key] || !flags[i + 1]) throw new Error('invalid_audit_arguments')
    options[key] = flags[i + 1]
  }
  if (!options['--output'] || command === 'facts' && (!options['--plan'] || !options['--results'] || options['--repository'] || options['--inventory'] || options['--sample']) ||
      command === 'plan' && (!!options['--inventory'] === !!options['--repository'] || options['--results'] || options['--plan'])) throw new Error('invalid_audit_arguments')
  let plan, output
  if (command === 'plan') {
    const inventory = options['--inventory'] ? readJSON(options['--inventory']) : collectMerged(options['--repository'], weekWindow(now))
    plan = auditPlan(inventory, options['--sample'] === undefined ? 5 : Number(options['--sample']))
    output = { ...plan, inventory, results_template: { schema: 1, audits: plan.selected.map(row => ({ pull_request: row.number,
      head_sha: row.head_sha, merge_sha: row.merge_sha, auditor: '', auditor_family: '', original_reviewer: '', original_reviewer_family: '',
      at: '', status: 'pending', evidence: '', follow_up_owner: '', findings: [] })) } }
  } else {
    const saved = readJSON(options['--plan'])
    plan = auditPlan(saved.inventory, saved.sample_size)
    if (JSON.stringify(saved.selected) !== JSON.stringify(plan.selected) || saved.repository !== plan.repository ||
        JSON.stringify(saved.window) !== JSON.stringify(plan.window) || saved.population !== plan.population || saved.complete !== true || saved.schema !== 1) throw new Error('audit_saved_plan_mismatch')
    output = auditFacts(plan, readJSON(options['--results']), { now })
  }
  writeFileSync(options['--output'], JSON.stringify(output, null, 2) + '\n', { flag: 'wx', mode: 0o600 })
  return { status: command === 'plan' ? 'awaiting_independent_audits' : 'facts_prepared_not_submitted', selected: plan.selected.length }
}
if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  try { console.log(JSON.stringify(auditMain(process.argv.slice(2)))) }
  catch (error) {
    const reason = /^(?:invalid|audit|evidence|usage)_[a-z_]+$/.test(error.message) ? error.message : 'file_or_setup_error'
    console.error(`Audit incomplete (${reason}): use plan --repository OWNER/REPO (or --inventory PATH), or facts --plan PATH --results PATH; both require --output PATH.`)
    process.exitCode = 1
  }
}
