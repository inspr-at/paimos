// SPDX-License-Identifier: AGPL-3.0-only
// The existing PAIMOS reporter invokes this with its authenticated CLI. Hosted
// Actions only publishes evidence; no PAIMOS credential belongs in a workflow.
import { spawnSync } from 'node:child_process'
import { lstatSync, readFileSync, readdirSync, mkdirSync, rmdirSync } from 'node:fs'
import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

const repository = 'inspr-at/paimos'
const workflow = '.github/workflows/nightly-full.yml'
const maxBytes = 2 * 1024 * 1024
const positive = value => /^[1-9][0-9]*$/.test(String(value)) && Number.isSafeInteger(Number(value))
function bounded(value, max = 1024) {
  if (typeof value !== 'string' || !value || value.length > max || /[\x00-\x1f]/.test(value)) throw new Error('Invalid reporter metadata')
  return value
}
export function failureEvidence(directory, run) {
  const cases = new Set(), runners = new Set()
  let files = 0, bytes = 0
  function walk(path, depth) {
    if (depth > 6) throw new Error('Nightly evidence nesting exceeds limit')
    for (const entry of readdirSync(path, { withFileTypes: true })) {
      if (++files > 10_000) throw new Error('Too many nightly evidence files')
      const file = resolve(path, entry.name)
      if (entry.isDirectory()) walk(file, depth + 1)
      else if (entry.isFile() && entry.name.endsWith('-failures.json')) {
        const size = lstatSync(file).size
        if (size > maxBytes || (bytes += size) > 8 * maxBytes) throw new Error('Nightly evidence exceeds byte limit')
        const data = JSON.parse(readFileSync(file, 'utf8'))
        if (data.version !== 1 || String(data.runId) !== String(run.id) ||
          String(data.attempt) !== String(run.run_attempt) || data.sha !== run.head_sha ||
          !Array.isArray(data.cases) || data.cases.length > 20_000 || typeof data.runnerFailure !== 'boolean') throw new Error('Nightly evidence binding mismatch')
        const job = bounded(data.job, 128)
        for (const name of data.cases) cases.add(`${job}: ${bounded(name)}`)
        if (data.runnerFailure) runners.add(job)
      }
    }
  }
  if (directory) walk(resolve(directory), 0)
  return { cases: [...cases].sort(), runners: [...runners].sort() }
}
function validateRun(run) {
  if (run.repository?.full_name !== repository || run.path !== workflow || run.head_branch !== 'main' ||
      !['schedule', 'workflow_dispatch'].includes(run.event) || !positive(run.id) || !positive(run.run_attempt) ||
      !/^[a-f0-9]{40}$/.test(run.head_sha ?? '') || run.html_url !== `https://github.com/${repository}/actions/runs/${run.id}`) throw new Error('Untrusted nightly run identity')
  if (run.status !== 'completed') throw new Error('Nightly run has not completed')
}
export function nightlyTicket(run, jobs, evidence = { cases: [], runners: [] }) {
  validateRun(run)
  if (run.conclusion === 'success') return null
  if (!['failure', 'timed_out', 'action_required', 'startup_failure'].includes(run.conclusion)) return null
  if (!Array.isArray(jobs) || jobs.length > 1000 || jobs.some(job => job.run_id !== run.id || job.head_sha !== run.head_sha || job.status !== 'completed')) throw new Error('Incomplete or mismatched nightly jobs')
  const failures = jobs.filter(job => ['failure', 'timed_out', 'action_required', 'startup_failure'].includes(job.conclusion))
  if (!failures.length) throw new Error('Red nightly has no failed job evidence')
  const names = failures.map(job => `${bounded(job.name, 160)}${(job.steps ?? []).filter(step => step.conclusion === 'failure').map(step => ` / ${bounded(step.name, 160)}`).join('')}`)
  const title = `Nightly full ${run.id} failed (${run.head_sha.slice(0, 12)})`
  const description = [
    `Source: ${run.html_url}`, `Commit: ${run.head_sha}`, `Attempt: ${run.run_attempt}`,
    `Reporter key: nightly:${repository}:${run.id}`, '', 'Failed jobs:', ...names.map(name => `- ${name}`),
    '', 'Failed cases:', ...(evidence.cases.length ? evidence.cases.map(name => `- ${name}`) : ['- No bound case evidence; inspect setup/static/infrastructure logs.']),
    ...(evidence.runners.length ? ['', 'Incomplete runners:', ...evidence.runners.map(name => `- ${name}`)] : []),
    '', 'Follow-up owner: AEON lead (product/tests), OPS-257 (runner or workflow infrastructure).',
    'Reproduce the exact SHA. Preserve every assertion, classification and release gate.',
  ].join('\n')
  if (description.length > 60_000) throw new Error('Nightly ticket exceeds description bound')
  return { title, description }
}
function command(bin, args, input) {
  const result = spawnSync(bin, args, { encoding: 'utf8', timeout: 30_000, maxBuffer: maxBytes, input })
  // Subprocess output may contain private diagnostics: never echo it on failure.
  if (result.error || result.status !== 0) throw new Error('Nightly reporter command failed')
  return result.stdout
}
export function deliverTicket(ticket, { paimos, write = false, invoke = command } = {}) {
  if (!ticket) return { status: 'green-or-inactive' }
  if (!write) return { status: 'dry-run', ticket }
  if (!paimos || !paimos.startsWith('/')) throw new Error('Use the existing reporter’s absolute PAIMOS CLI path')
  const account = invoke(paimos, ['whoami'])
  if (!/^instance: ppm \(https:\/\/aeon\.barta\.cm\)\r?$/m.test(account) || !/^tenant: INSPR \(inspr\)\r?$/m.test(account)) throw new Error('Nightly reporter requires the ppm INSPR account')
  const found = JSON.parse(invoke(paimos, ['issue', 'search', ticket.title, '--project', 'AEON', '--limit', '100', '--json']))
  const rows = Array.isArray(found) ? found : found.items
  if (!Array.isArray(rows) || rows.length >= 100 || found.next_cursor) throw new Error('Nightly ticket deduplication is incomplete')
  const previous = rows.filter(row => row.title === ticket.title)
  if (previous.length > 1) throw new Error('Duplicate nightly tickets need reconciliation')
  if (previous.length) {
    const key = previous[0].issue_key ?? previous[0].key
    if (!/^AEON-\d+$/.test(key ?? '')) throw new Error('Existing nightly ticket identity is invalid')
    return { status: 'already-reported', key }
  }
  const created = JSON.parse(invoke(paimos, ['issue', 'create', '--project', 'AEON', '--title', ticket.title,
    '--description-file', '-', '--priority', 'high', '--bug', '--tags', 'nightly-ci', '--hide-from-release-notes', 'true', '--json'], ticket.description))
  const key = created.issue_key ?? created.key
  if (!/^AEON-\d+$/.test(key ?? '')) throw new Error('Nightly ticket creation was not acknowledged')
  return { status: 'reported', key }
}
export function main(args, { invoke = command } = {}) {
  const options = {}
  for (let i = 0; i < args.length; i++) {
    const flag = args[i]
    if (flag === '--write' && !options.write) options.write = true
    else if (['--run-id', '--evidence', '--paimos', '--state-directory'].includes(flag) && !Object.hasOwn(options, flag.slice(2))) {
      const value = args[++i]
      if (!value || value.startsWith('--')) throw new Error('Missing nightly reporter argument')
      options[flag.slice(2)] = value
    } else throw new Error('Invalid nightly reporter argument')
  }
  const id = options['run-id']
  if (!positive(id)) throw new Error('Expected --run-id ID')
  const run = JSON.parse(invoke('gh', ['api', `repos/${repository}/actions/runs/${id}`]))
  // Validate before constructing any additional GitHub path.
  validateRun(run)
  if (!['failure', 'timed_out', 'action_required', 'startup_failure'].includes(run.conclusion)) return { status: 'green-or-inactive' }
  const jobs = [], seen = new Set()
  let expected
  for (let page = 1; page <= 10; page++) {
    const data = JSON.parse(invoke('gh', ['api', `repos/${repository}/actions/runs/${id}/attempts/${run.run_attempt}/jobs?per_page=100&page=${page}`]))
    if (!Number.isInteger(data.total_count) || data.total_count < 1 || data.total_count > 1000 ||
      !Array.isArray(data.jobs) || data.jobs.length > 100 || expected !== undefined && expected !== data.total_count) throw new Error('Invalid or changing nightly job inventory')
    expected = data.total_count
    for (const job of data.jobs) {
      if (!positive(job.id) || seen.has(job.id)) throw new Error('Duplicate nightly job identity')
      seen.add(job.id); jobs.push(job)
    }
    if (jobs.length === expected) break
    if (!data.jobs.length) throw new Error('Incomplete nightly job inventory')
  }
  if (jobs.length !== expected) throw new Error('Incomplete nightly job inventory')
  const ticket = nightlyTicket(run, jobs, failureEvidence(options.evidence, run))
  if (!options.write) return deliverTicket(ticket)
  // Existing reporter instances share one state directory. Never steal a lock:
  // an uncertain write is reconciled through tracker search on the next run.
  if (!options['state-directory']) throw new Error('Writing requires the existing reporter state directory')
  const state = resolve(options['state-directory'])
  if (!lstatSync(state).isDirectory()) throw new Error('Expected reporter state directory')
  const lock = resolve(state, `nightly-${id}.lock`)
  mkdirSync(lock, { mode: 0o700 })
  try { return deliverTicket(ticket, { paimos: options.paimos, write: true, invoke }) }
  finally { rmdirSync(lock) }
}
if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  try { console.log(JSON.stringify(main(process.argv.slice(2)))) }
  catch (error) { console.error(error.message); process.exitCode = 1 }
}
