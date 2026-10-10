// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1020: observational queue evidence; never modifies checks or the queue.
import { execFile } from 'node:child_process'
import { mkdirSync, readFileSync, statSync, writeFileSync } from 'node:fs'
import { promisify } from 'node:util'
import { pathToFileURL } from 'node:url'
import { ghGet } from './ci-queue-measure.mjs'

const execute = promisify(execFile)
const schema = 'aeon.queue-correlation.v1'
const defaults = ['go', 'web', 'release-check', 'e2e', 'migration-compat']
const red = new Set(['failure', 'timed_out', 'cancelled', 'missing', 'action_required', 'startup_failure'])
const causes = ['predecessor_failure', 'reordering', 'manual_removal', 'unknown']
const maxRuns = 5000, maxPages = 20, maxEntries = 100, maxBytes = 64 * 1024 * 1024
const shaPattern = /^[a-f0-9]{40}$/
const time = value => typeof value === 'string' && Number.isFinite(Date.parse(value)) ? Date.parse(value) : null
const inWindow = (at, from, to) => time(at) !== null && time(at) >= time(from) && time(at) < time(to)
const prOf = branch => Number(branch?.match(/^gh-readonly-queue\/.+\/pr-(\d+)-[a-f0-9]{40}$/)?.[1]) || null
const baseOf = branch => branch?.match(/^gh-readonly-queue\/.+\/pr-\d+-([a-f0-9]{40})$/)?.[1] ?? null

function windowBounds(from, to) {
  for (const value of [from, to]) if (!/^\d{4}-\d{2}-\d{2}T.*Z$/.test(value ?? '') || time(value) === null) throw new Error('window must use UTC ISO timestamps')
  if (time(from) >= time(to) || time(to) - time(from) > 30 * 86400_000) throw new Error('window must be positive and at most 30 days')
}

export function requiredStatus(run) {
  const values = Object.values(run.required ?? {})
  if (!values.length) return 'unknown'
  if (values.some(value => red.has(value))) return 'red'
  return values.every(value => ['success', 'skipped'].includes(value)) ? 'green' : 'unknown'
}

// GraphQL uses POST for queries. The transport accepts only our fixed query
// shape, never arbitrary GraphQL or mutations. No token enters arguments/logs.
export function timelineQuery(repository, pr, cursor = null) {
  if (!/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repository) || !Number.isSafeInteger(pr) || pr < 1 ||
      (cursor !== null && (typeof cursor !== 'string' || cursor.length > 1024))) throw new Error('invalid timeline query identity')
  const [owner, name] = repository.split('/')
  return `query { repository(owner:${JSON.stringify(owner)},name:${JSON.stringify(name)}) { pullRequest(number:${pr}) {
    number timelineItems(first:100,after:${JSON.stringify(cursor)},itemTypes:[ADDED_TO_MERGE_QUEUE_EVENT,REMOVED_FROM_MERGE_QUEUE_EVENT]) {
      pageInfo { hasNextPage endCursor } nodes { __typename
        ... on AddedToMergeQueueEvent { id createdAt }
        ... on RemovedFromMergeQueueEvent { id createdAt reason beforeCommit { oid } actor { __typename } }
      }
    }
  } } }`
}

async function ghTimeline(repository, pr, cursor) {
  const { stdout } = await execute('gh', ['api', 'graphql', '-f', `query=${timelineQuery(repository, pr, cursor)}`],
    { timeout: 60_000, maxBuffer: 8 * 1024 * 1024 })
  return JSON.parse(stdout)
}

async function pages(get, endpoint, field) {
  const items = []
  for (let page = 1; page <= maxPages; page++) {
    const body = await get(`${endpoint}${endpoint.includes('?') ? '&' : '?'}per_page=100&page=${page}`)
    const batch = field ? body[field] : body
    if (!Array.isArray(batch) || batch.length > 100) throw new Error('invalid API page')
    items.push(...batch)
    if (batch.length < 100 || (Number.isSafeInteger(body.total_count) && items.length >= body.total_count)) return items
  }
  throw new Error('pagination bound reached; refusing truncated evidence')
}

function sessionAt(events, at) {
  let added = null
  for (const event of [...events].sort((a, b) => time(a.createdAt) - time(b.createdAt))) {
    if (time(event.createdAt) > time(at)) break
    if (event.__typename === 'AddedToMergeQueueEvent') added = event.id
    if (event.__typename === 'RemovedFromMergeQueueEvent') added = null
  }
  return added
}

function validateSnapshot(snapshot) {
  if (snapshot.schema !== schema || !Array.isArray(snapshot.runs) || snapshot.runs.length > maxRuns ||
      !Array.isArray(snapshot.merged_prs) || snapshot.merged_prs.length > 2000) throw new Error('invalid correlation snapshot')
  if (new Set(snapshot.runs.map(run => run.id)).size !== snapshot.runs.length) throw new Error('duplicate run IDs')
  if (new Set(snapshot.merged_prs.map(pr => pr.number)).size !== snapshot.merged_prs.length) throw new Error('duplicate PR numbers')
  for (const run of snapshot.runs) {
    if (!Number.isSafeInteger(run.id) || run.id < 1 || !shaPattern.test(run.head_sha) || time(run.created_at) === null ||
        run.event !== 'merge_group' || (run.pr !== null && run.pr !== prOf(run.head_branch))) throw new Error('invalid run identity')
  }
  if (Object.keys(snapshot.commits ?? {}).length > 10_000 || Object.keys(snapshot.timelines ?? {}).length > 2000) throw new Error('evidence bound reached')
  for (const [sha, commit] of Object.entries(snapshot.commits ?? {})) {
    if (!shaPattern.test(sha) || commit.sha !== sha || !Array.isArray(commit.parents) || commit.parents.length > 2 ||
        commit.parents.some(parent => !shaPattern.test(parent))) throw new Error('invalid commit evidence')
  }
  for (const events of Object.values(snapshot.timelines ?? {})) {
    if (!Array.isArray(events) || events.length > 2000 || new Set(events.map(event => event.id)).size !== events.length ||
        events.some(event => !event.id || time(event.createdAt) === null ||
          !['AddedToMergeQueueEvent', 'RemovedFromMergeQueueEvent'].includes(event.__typename) ||
          (event.beforeCommit && !shaPattern.test(event.beforeCommit.oid)))) throw new Error('invalid timeline evidence')
  }
}

// A queue branch's suffix is its immediate base, which may itself be a queue
// head. Walk first parents only while that ancestor's PR was still queued at
// this run's creation. Already-merged commits are main, not entries ahead.
export function groupIdentity(run, snapshot) {
  const base = baseOf(run.head_branch)
  const head = snapshot.commits?.[run.head_sha]
  const group = { base_sha: base, head_sha: run.head_sha, entries_ahead: null, identity_complete: false, warnings: [] }
  if (!base || !head || head.pr !== run.pr || head.parents?.[0] !== base) {
    group.warnings.push('group commit/branch identity unavailable or inconsistent')
    return group
  }
  const ahead = [], seen = new Set([run.head_sha])
  let current = base
  for (let depth = 0; depth <= maxEntries; depth++) {
    if (seen.has(current)) { group.warnings.push('cyclic group ancestry'); return group }
    seen.add(current)
    const commit = snapshot.commits?.[current]
    if (!commit) { group.warnings.push('ancestor commit unavailable'); return group }
    if (commit.pr === null) { group.entries_ahead = ahead.reverse(); group.identity_complete = true; return group }
    const events = snapshot.timelines?.[commit.pr]
    if (!events) { group.warnings.push('ancestor queue timeline unavailable'); return group }
    if (!sessionAt(events, run.created_at)) {
      // Absence of an enqueue alone is not proof this is main. Require the
      // merge event for this exact commit, before the run started.
      const merged = events.some(event => event.reason === 'merged' && event.beforeCommit?.oid === current && time(event.createdAt) <= time(run.created_at))
      if (!merged) { group.warnings.push('ancestor not proven merged or queued'); return group }
      group.entries_ahead = ahead.reverse(); group.identity_complete = true; return group
    }
    if (depth === maxEntries || commit.parents.length !== 2 || commit.pr === run.pr) break
    ahead.push({ pr: commit.pr, head_sha: current })
    current = commit.parents[0]
  }
  group.warnings.push('group ancestry bound or invalid parent shape')
  return group
}

function eventsBetween(snapshot, prs, start, end) {
  return [...new Set(prs)].flatMap(pr => (snapshot.timelines?.[pr] ?? []).filter(event =>
    time(event.createdAt) >= time(start) && time(event.createdAt) <= time(end)).map(event => ({ pr, ...event })))
}

export function classifyExtra(previous, replacement, snapshot) {
  const group = previous.group ?? groupIdentity(previous, snapshot)
  const nextGroup = replacement.group ?? groupIdentity(replacement, snapshot)
  const result = { cause: 'unknown', evidence: [], unknown_reason: null }
  if (!group.identity_complete || !nextGroup.identity_complete || !snapshot.timelines?.[previous.pr]) {
    result.unknown_reason = 'incomplete group or PR timeline evidence'; return result
  }
  if (!sessionAt(snapshot.timelines[previous.pr], previous.created_at)) {
    result.unknown_reason = 'original PR queue session unavailable'; return result
  }
  const ahead = group.entries_ahead
  const events = eventsBetween(snapshot, [previous.pr, ...ahead.map(entry => entry.pr)], previous.created_at, replacement.created_at)
  const candidates = new Set()
  for (const event of events) {
    if (event.__typename !== 'RemovedFromMergeQueueEvent') continue
    const own = event.pr === previous.pr && event.beforeCommit?.oid === previous.head_sha
    const predecessor = ahead.find(entry => entry.pr === event.pr && entry.head_sha === event.beforeCommit?.oid)
    if (!own && !predecessor) continue
    if (event.reason === 'manual') {
      candidates.add('manual_removal'); result.evidence.push({ kind: 'queue_removal', pr: event.pr, event_id: event.id, at: event.createdAt, reason: event.reason, head_sha: event.beforeCommit.oid })
    }
    if (predecessor && event.reason === 'failed_checks' &&
        snapshot.commits[previous.head_sha].parents[1] === snapshot.commits[replacement.head_sha].parents[1]) {
      candidates.add('predecessor_failure'); result.evidence.push({ kind: 'queue_removal', pr: event.pr, event_id: event.id, at: event.createdAt, reason: event.reason, head_sha: event.beforeCommit.oid })
    }
    if (own && ['reordered', 'queue_reordered'].includes(event.reason)) {
      candidates.add('reordering'); result.evidence.push({ kind: 'queue_removal', pr: event.pr, event_id: event.id, at: event.createdAt, reason: event.reason, head_sha: event.beforeCommit.oid })
    }
  }
  const before = ahead.map(entry => entry.pr), after = nextGroup.entries_ahead.map(entry => entry.pr)
  if (before.length > 1 && before.length === after.length && before.every(pr => after.includes(pr)) &&
      before.some((pr, index) => pr !== after[index]) &&
      snapshot.commits[previous.head_sha].parents[1] === snapshot.commits[replacement.head_sha].parents[1] &&
      sessionAt(snapshot.timelines[previous.pr], previous.created_at) === sessionAt(snapshot.timelines[previous.pr], replacement.created_at) &&
      !events.some(event => event.__typename === 'RemovedFromMergeQueueEvent')) {
    candidates.add('reordering'); result.evidence.push({ kind: 'ordered_group_membership', before, after })
  }
  if (candidates.size === 1) { result.cause = [...candidates][0]; return result }
  result.unknown_reason = candidates.size ? 'conflicting causes in rebuild interval' : 'no causal event bound to this group; base movement or cancellation alone is insufficient'
  return result
}

function percentile(values, fraction) {
  const sorted = values.filter(Number.isFinite).sort((a, b) => a - b)
  if (!sorted.length) return null
  const index = (sorted.length - 1) * fraction, lower = Math.floor(index)
  return sorted[lower] + (sorted[Math.ceil(index)] - sorted[lower]) * (index - lower)
}

function tally(runs, extras) {
  const counts = Object.fromEntries(causes.map(cause => [cause, extras.filter(extra => extra.cause === cause).length]))
  const waits = runs.map(run => run.worst_job_wait_seconds).filter(Number.isFinite)
  return { runs: runs.length, inferred_ejections_required_red: runs.filter(run => run.counted && requiredStatus(run) === 'red').length,
    extra_runs: extras.length, rebuilds_by_cause: counts, unclassified_share: extras.length ? counts.unknown / extras.length : null,
    runner_wait: { n: waits.length, p50_seconds: percentile(waits, .5), p90_seconds: percentile(waits, .9), max_seconds: waits.length ? Math.max(...waits) : null } }
}

export function correlate(snapshot, { from, to } = {}) {
  validateSnapshot(snapshot); windowBounds(from, to)
  if (time(snapshot.lookback) === null || time(snapshot.coverage_to) === null || time(from) < time(snapshot.lookback) ||
      time(to) > time(snapshot.coverage_to)) throw new Error('requested window exceeds snapshot coverage')
  const runs = snapshot.runs.map(run => ({ ...run, pr: prOf(run.head_branch), group: groupIdentity(run, snapshot),
    worst_job_wait_seconds: Number.isFinite(run.worst_job_wait_min) ? run.worst_job_wait_min * 60 : null,
    counted: inWindow(run.created_at, from, to) && time(run.a1_completed) !== null && time(run.a1_completed) < time(to) && !!run.a1_conclusion && run.a1_conclusion !== 'cancelled',
    timeline_events: (snapshot.timelines?.[run.pr] ?? []).filter(event => inWindow(event.createdAt, snapshot.lookback ?? from, to)) }))
    .sort((a, b) => time(a.created_at) - time(b.created_at) || a.id - b.id)
  const extra = [], ownRed = [], perPR = []
  for (const pr of snapshot.merged_prs.filter(pr => inWindow(pr.merged_at, from, to))) {
    const history = runs.filter(run => run.pr === pr.number && time(run.created_at) <= time(pr.merged_at))
    if (!history.length) continue
    perPR.push({ pr: pr.number, runs: history.length })
    for (let i = 0; i < history.length - 1; i++) {
      const previous = history[i], next = history[i + 1]
      const pair = { pr: pr.number, run_id: previous.id, replacement_run_id: next.id, at: previous.created_at }
      if (previous.a1_conclusion !== 'cancelled' && requiredStatus(previous) === 'red') ownRed.push(pair)
      else extra.push({ ...pair, ...classifyExtra(previous, next, snapshot) })
    }
  }
  const selected = runs.filter(run => inWindow(run.created_at, from, to))
  const days = []
  for (let day = Math.floor(time(from) / 86400_000) * 86400_000; day < time(to); day += 86400_000) {
    const date = new Date(day).toISOString().slice(0, 10)
    days.push({ date, ...tally(selected.filter(run => run.created_at.slice(0, 10) === date), extra.filter(item => item.at.slice(0, 10) === date)) })
  }
  const removals = Object.entries(snapshot.timelines ?? {}).flatMap(([pr, events]) => events.filter(event =>
    event.__typename === 'RemovedFromMergeQueueEvent' && event.reason !== 'merged' && inWindow(event.createdAt, from, to))
    .map(event => ({ pr: Number(pr), event_id: event.id, at: event.createdAt, reason: event.reason ?? 'unknown', head_sha: event.beforeCommit?.oid ?? null })))
  return { schema, repository: snapshot.repository, workflow: snapshot.workflow, required_checks: snapshot.required_checks, window: { from, to },
    definitions: { inferred_ejections: 'Attempt-1 required-red, non-cancelled queue runs created and completed within the window; inferred, not proven removals.',
      additional_runs: 'Earlier queue runs, other than required-red non-cancelled runs, for PRs merged in the window; history includes lookback.',
      observed_removals: 'Non-merged removal events for the PR timelines collected in this snapshot; a different population from inferred ejections.',
      runner_wait: 'Maximum job created_at → started_at per attempt-1 run, excluding skipped jobs; not PR queue residence.',
      daily: 'Runs/rebuilds bucketed by original run creation UTC date; window extras before from appear separately.' },
    baseline: { inferred_ejections_required_red: selected.filter(run => run.counted && requiredStatus(run) === 'red').length,
      additional_non_required_red_runs_cause_unclassified: extra.length, extra_runs_after_required_red: ownRed.length,
      merged_prs_with_queue_runs: perPR.length },
    summary: { ...tally(selected, extra), observed_removals_by_reason: Object.fromEntries([...new Set(removals.map(event => event.reason))].sort()
      .map(reason => [reason, removals.filter(event => event.reason === reason).length])) }, daily: days, observed_removals: removals,
    extras_before_window: extra.filter(item => time(item.at) < time(from)).length,
    coverage: { group_identities: selected.filter(run => run.group.identity_complete).length, queue_runs: selected.length,
      pr_timelines: selected.filter(run => snapshot.timelines?.[run.pr]).length },
    runs: selected, additional_runs: extra, extra_runs_after_required_red: ownRed, per_pr: perPR }
}

export async function collect({ repository = 'inspr-at/paimos', workflow = 'ci.yml', from, to, requiredChecks = defaults,
  get = ghGet, timeline = ghTimeline, now = () => Date.now() } = {}) {
  windowBounds(from, to)
  if (!/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repository) || !/^[A-Za-z0-9_.-]+$/.test(workflow)) throw new Error('invalid repository/workflow')
  if (!requiredChecks.length || requiredChecks.length > 50 || requiredChecks.some(name => typeof name !== 'string' || !name || name.length > 100)) throw new Error('invalid required checks')
  const deadline = now() + 20 * 60_000
  const guard = () => { if (now() >= deadline) throw new Error('collection deadline reached; refusing partial evidence') }
  const read = async endpoint => { guard(); return get(endpoint) }
  const snapshot = { schema, repository, workflow, required_checks: requiredChecks,
    lookback: new Date(time(from) - 86400_000).toISOString(), coverage_to: to, runs: [], merged_prs: [], commits: {}, timelines: {} }
  const base = `repos/${repository}`
  const found = new Map()
  for (let day = Math.floor(time(snapshot.lookback) / 86400_000) * 86400_000; day < time(to); day += 86400_000) {
    const date = new Date(day).toISOString().slice(0, 10)
    const rows = await pages(read, `${base}/actions/workflows/${encodeURIComponent(workflow)}/runs?event=merge_group&created=${date}`, 'workflow_runs')
    if (rows.length >= 1000) throw new Error('Actions search cap reached; narrow the window')
    for (const run of rows) if (run.event === 'merge_group' && inWindow(run.created_at, snapshot.lookback, to)) found.set(run.id, run)
    if (found.size > maxRuns) throw new Error('run bound reached; narrow the window')
  }
  for (const run of found.values()) {
    const attempt = await read(`${base}/actions/runs/${run.id}/attempts/1`)
    if (attempt.id !== run.id || attempt.run_attempt !== 1 || attempt.head_sha !== run.head_sha) throw new Error('attempt identity mismatch')
    const jobs = await pages(read, `${base}/actions/runs/${run.id}/attempts/1/jobs`, 'jobs')
    if (jobs.some(job => job.run_attempt !== undefined && job.run_attempt !== 1)) throw new Error('job attempt mismatch')
    const required = Object.fromEntries(requiredChecks.map(name => {
      const matching = jobs.filter(job => job.name === name)
      if (matching.length > 1) throw new Error('ambiguous required check')
      return [name, matching[0]?.conclusion ?? (attempt.status === 'completed' ? 'missing' : null)]
    }))
    const active = jobs.filter(job => job.conclusion !== 'skipped')
    const waits = active.filter(job => time(job.created_at) !== null && time(job.started_at) !== null && time(job.started_at) >= time(job.created_at))
      .map(job => (time(job.started_at) - time(job.created_at)) / 60_000)
    const completed = active.length && active.every(job => time(job.completed_at) !== null) ? new Date(Math.max(...active.map(job => time(job.completed_at)))).toISOString() : null
    snapshot.runs.push({ id: run.id, event: 'merge_group', head_sha: run.head_sha, head_branch: run.head_branch,
      pr: prOf(run.head_branch), created_at: run.created_at, a1_conclusion: attempt.conclusion, a1_completed: completed,
      required, worst_job_wait_min: waits.length ? Math.max(...waits) : null })
  }
  // Closed PRs ordered by updated_at include merged PRs. Stop only once all
  // remaining rows are older than the window, and fail on the page cap.
  let complete = false
  for (let page = 1; page <= maxPages; page++) {
    const prs = await read(`${base}/pulls?state=closed&sort=updated&direction=desc&per_page=100&page=${page}`)
    if (!Array.isArray(prs) || prs.length > 100) throw new Error('invalid PR page')
    for (const pr of prs) if (inWindow(pr.merged_at, from, to)) snapshot.merged_prs.push({ number: pr.number, created_at: pr.created_at, merged_at: pr.merged_at })
    if (prs.length < 100 || prs.some(pr => time(pr.updated_at) !== null && time(pr.updated_at) < time(from))) { complete = true; break }
  }
  if (!complete) throw new Error('PR pagination bound reached; refusing truncated evidence')
  async function queueEvents(pr) {
    if (snapshot.timelines[pr]) return snapshot.timelines[pr]
    if (Object.keys(snapshot.timelines).length >= 2000) throw new Error('timeline PR bound reached')
    const events = []; let cursor = null
    for (let page = 1; page <= maxPages; page++) {
      guard(); const body = await timeline(repository, pr, cursor)
      const pull = body.data?.repository?.pullRequest, connection = pull?.timelineItems
      if (body.errors?.length || pull?.number !== pr || !Array.isArray(connection?.nodes) || connection.nodes.length > 100) throw new Error('incomplete GraphQL timeline')
      events.push(...connection.nodes)
      if (connection.pageInfo?.hasNextPage === false) return snapshot.timelines[pr] = events
      const next = connection.pageInfo?.endCursor
      if (typeof next !== 'string' || !next || next === cursor || next.length > 1024) throw new Error('invalid timeline cursor')
      cursor = next
    }
    throw new Error('timeline pagination bound reached; refusing truncated evidence')
  }
  async function commit(sha) {
    if (snapshot.commits[sha]) return snapshot.commits[sha]
    if (!shaPattern.test(sha) || Object.keys(snapshot.commits).length >= 10_000) throw new Error('invalid/bounded commit read')
    const body = await read(`${base}/git/commits/${sha}`)
    if (body.sha !== sha || !Array.isArray(body.parents) || body.parents.length > 2) throw new Error('commit identity mismatch')
    const pr = Number(body.message?.match(/^Merge pull request #(\d+) from /)?.[1]) || null
    return snapshot.commits[sha] = { sha, pr, parents: body.parents.map(parent => parent.sha) }
  }
  for (const run of snapshot.runs) {
    if (run.pr) await queueEvents(run.pr)
    await commit(run.head_sha)
    let sha = baseOf(run.head_branch); const seen = new Set()
    for (let depth = 0; sha && depth <= maxEntries; depth++) {
      if (seen.has(sha)) throw new Error('cyclic commit ancestry')
      seen.add(sha)
      const ancestor = await commit(sha)
      if (ancestor.pr === null) break
      const events = await queueEvents(ancestor.pr)
      if (!sessionAt(events, run.created_at)) break
      if (depth === maxEntries) throw new Error('queue ancestry bound reached')
      sha = ancestor.parents[0]
    }
  }
  guard(); validateSnapshot(snapshot)
  return snapshot
}

export function formatDaily(report) {
  const lines = [`Queue correlation ${report.repository}: ${report.window.from} → ${report.window.to}`,
    `Baseline: ${report.baseline.inferred_ejections_required_red} inferred required-red ejections; ${report.baseline.additional_non_required_red_runs_cause_unclassified} additional runs before classification.`,
    `Group identity coverage: ${report.coverage.group_identities}/${report.coverage.queue_runs}. Unknown causes remain explicit.`, '',
    '| UTC date | Runs | Inferred ejections | Predecessor failure | Reordering | Manual removal | Unknown | Worst-job wait p90 s (n) |',
    '|---|---:|---:|---:|---:|---:|---:|---:|']
  for (const day of report.daily) lines.push(`| ${day.date} | ${day.runs} | ${day.inferred_ejections_required_red} | ${day.rebuilds_by_cause.predecessor_failure} | ${day.rebuilds_by_cause.reordering} | ${day.rebuilds_by_cause.manual_removal} | ${day.rebuilds_by_cause.unknown} | ${day.runner_wait.p90_seconds?.toFixed(1) ?? 'n/a'} (${day.runner_wait.n}) |`)
  lines.push('', `Additional runs before window: ${report.extras_before_window}. Runner wait is job dispatch delay, not PR queue residence.`)
  return lines.join('\n') + '\n'
}

export async function main(argv = process.argv.slice(2), dependencies = {}) {
  const opts = {}
  const values = new Set(['--from', '--to', '--repo', '--workflow', '--required-checks', '--input', '--out'])
  for (let i = 0; i < argv.length; i++) {
    const key = argv[i]
    if (key === '--help') return 'Usage: node scripts/ci-queue-correlate.mjs --from UTC --to UTC [--repo owner/name] [--workflow ci.yml] [--required-checks go,web,release-check,e2e,migration-compat] [--input snapshot.json] [--out directory]\nRead-only GitHub REST GETs and fixed GraphQL queries; no queue/check/ruleset writes. Offline input makes no network calls.\n'
    if (!values.has(key) || opts[key] !== undefined || !argv[i + 1] || argv[i + 1].startsWith('--')) throw new Error('unknown, repeated or missing option')
    opts[key] = argv[++i]
  }
  windowBounds(opts['--from'], opts['--to'])
  let snapshot
  if (opts['--input']) {
    if (statSync(opts['--input']).size > maxBytes) throw new Error('input exceeds 64 MiB')
    snapshot = JSON.parse(readFileSync(opts['--input'], 'utf8'))
  } else snapshot = await collect({ ...dependencies, from: opts['--from'], to: opts['--to'],
    repository: opts['--repo'], workflow: opts['--workflow'], requiredChecks: opts['--required-checks']?.split(',') })
  const report = correlate(snapshot, { from: opts['--from'], to: opts['--to'] })
  if (opts['--out']) {
    const files = { 'snapshot.json': JSON.stringify(snapshot), 'report.json': JSON.stringify(report), 'report.md': formatDaily(report) }
    for (const value of Object.values(files)) if (Buffer.byteLength(value) > maxBytes) throw new Error('output exceeds 64 MiB')
    mkdirSync(opts['--out'], { recursive: true })
    for (const [name, value] of Object.entries(files)) writeFileSync(`${opts['--out']}/${name}`, value + '\n')
  }
  return formatDaily(report)
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().then(output => process.stdout.write(output)).catch(() => { process.stderr.write('Queue correlation failed; no complete report is available.\n'); process.exitCode = 1 })
}
