// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import { classifyExtra, collect, correlate, formatDaily, groupIdentity, main, requiredStatus, timelineQuery } from './ci-queue-correlate.mjs'

const fixtureURL = new URL('./testdata/ci-queue-correlation-w2.json', import.meta.url)
const fixture = () => JSON.parse(readFileSync(fixtureURL, 'utf8'))
const window = { from: '2026-10-05T00:00:00Z', to: '2026-10-09T04:00:00Z' }

test('recorded W2 replay preserves 90 inferred ejections, 24 additional runs and every evidence-bound cause', () => {
  // Risk: silently changing the metric population, or inventing a queue cause.
  const snapshot = fixture(), report = correlate(snapshot, window)
  assert.deepEqual(report.baseline, snapshot.provenance.expected_baseline)
  assert.deepEqual(report.coverage, { group_identities: 263, queue_runs: 263, pr_timelines: 263 })
  const item = pr => report.additional_runs.find(run => run.pr === pr)
  assert.equal(item(352).cause, 'manual_removal')
  assert.deepEqual(item(352).evidence.map(event => [event.pr, event.reason, event.head_sha]),
    [[352, 'manual', 'c21e8f225a2318a6a318bc1f13a0aaef46e445aa']])
  assert.equal(item(284).cause, 'predecessor_failure')
  assert.equal(item(284).evidence[0].pr, 276)
  assert.equal(item(284).evidence[0].reason, 'failed_checks')
  assert.equal(item(398).cause, 'unknown')
  assert.match(item(398).unknown_reason, /no causal event/)
  assert.deepEqual(report.summary.rebuilds_by_cause, { predecessor_failure: 12, reordering: 0, manual_removal: 6, unknown: 6 })
  assert.equal(report.daily.reduce((sum, day) => sum + day.extra_runs, 0) + report.extras_before_window, 24)
  assert.equal(report.daily.reduce((sum, day) => sum + day.inferred_ejections_required_red, 0), 90)
  assert.equal(report.summary.runner_wait.n, 263)
  const run = report.runs.find(run => run.id === 37329410325)
  assert.ok(run.group.entries_ahead.some(entry => entry.pr === 276))
  assert.equal(run.group.base_sha, snapshot.commits[run.head_sha].parents[0])
  const withoutEvidence = fixture(); withoutEvidence.commits = {}; withoutEvidence.timelines = {}
  const before = correlate(withoutEvidence, window)
  assert.deepEqual(before.baseline, report.baseline)
  assert.equal(before.summary.rebuilds_by_cause.unknown, 24)
})

test('recorded identities with a replayed order change prove reordering; base drift or changed membership alone cannot', () => {
  // W2 contains no proven reorder. Replay its recorded API shapes with a
  // deterministic parent permutation; never relabel this as a historical fact.
  const snapshot = fixture()
  const previous = snapshot.runs.find(run => groupIdentity(run, snapshot).entries_ahead?.length === 2)
  assert.ok(previous)
  const group = groupIdentity(previous, snapshot), before = group.entries_ahead
  const first = snapshot.commits[before[0].head_sha]
  const a = 'f'.repeat(38) + '01', b = 'f'.repeat(38) + '02', head = 'f'.repeat(38) + '03'
  snapshot.commits[a] = { ...snapshot.commits[before[1].head_sha], sha: a, parents: [first.parents[0], snapshot.commits[before[1].head_sha].parents[1]] }
  snapshot.commits[b] = { ...first, sha: b, parents: [a, first.parents[1]] }
  snapshot.commits[head] = { ...snapshot.commits[previous.head_sha], sha: head, parents: [b, snapshot.commits[previous.head_sha].parents[1]] }
  const replacement = { ...previous, id: 99999999999, head_sha: head,
    head_branch: previous.head_branch.replace(/[a-f0-9]{40}$/, b), created_at: new Date(Date.parse(previous.created_at) + 1000).toISOString() }
  const result = classifyExtra(previous, replacement, snapshot)
  assert.equal(result.cause, 'reordering')
  assert.deepEqual(result.evidence, [{ kind: 'ordered_group_membership', before: before.map(entry => entry.pr), after: before.map(entry => entry.pr).reverse() }])
  const unknown = correlate(snapshot, window).additional_runs.find(item => item.pr === 398)
  assert.equal(classifyExtra(snapshot.runs.find(run => run.id === unknown.run_id),
    snapshot.runs.find(run => run.id === unknown.replacement_run_id), snapshot).cause, 'unknown')
  snapshot.commits[head].parents[1] = 'e'.repeat(40)
  assert.equal(classifyExtra(previous, replacement, snapshot).cause, 'unknown')
})

test('wrong SHA, late events, missing reasons, incomplete ancestry and conflicting causes stay unknown', () => {
  const snapshot = fixture(), report = correlate(snapshot, window)
  const pair = report.additional_runs.find(item => item.pr === 352)
  const previous = snapshot.runs.find(run => run.id === pair.run_id), next = snapshot.runs.find(run => run.id === pair.replacement_run_id)
  for (const edit of [event => { event.beforeCommit.oid = 'a'.repeat(40) }, event => { event.reason = null },
    event => { event.createdAt = '2026-10-09T03:00:00Z' }]) {
    const altered = fixture(), manual = altered.timelines[352].find(event => event.reason === 'manual')
    edit(manual)
    assert.equal(classifyExtra(previous, next, altered).cause, 'unknown')
  }
  const missing = fixture(); delete missing.commits[previous.head_sha]
  assert.equal(classifyExtra(previous, next, missing).cause, 'unknown')
  const candidate = report.additional_runs.find(item => item.cause === 'predecessor_failure')
  const prior = snapshot.runs.find(run => run.id === candidate.run_id), replacement = snapshot.runs.find(run => run.id === candidate.replacement_run_id)
  snapshot.timelines[prior.pr].push({ __typename: 'RemovedFromMergeQueueEvent', id: 'replay-manual', createdAt: candidate.evidence[0].at,
    reason: 'manual', beforeCommit: { oid: prior.head_sha } })
  assert.equal(classifyExtra(prior, replacement, snapshot).cause, 'unknown')
  assert.match(classifyExtra(prior, replacement, snapshot).unknown_reason, /conflicting/)
})

test('offline publication includes daily unknowns, samples and coverage; invalid windows cannot report success', async t => {
  const directory = mkdtempSync(join(tmpdir(), 'aeon-queue-report-'))
  t.after(() => rmSync(directory, { recursive: true, force: true }))
  const output = await main(['--input', fixtureURL.pathname, '--from', window.from, '--to', window.to, '--out', directory],
    { get: () => assert.fail('offline mode made a network read') })
  assert.match(output, /90 inferred required-red ejections; 24 additional runs/)
  assert.equal(JSON.parse(readFileSync(join(directory, 'report.json'))).schema, 'aeon.queue-correlation.v1')
  assert.equal(JSON.parse(readFileSync(join(directory, 'snapshot.json'))).runs.length, 301)
  const empty = fixture(); empty.runs = []; empty.merged_prs = []; empty.timelines = {}
  const emptyReport = correlate(empty, window)
  assert.equal(emptyReport.summary.unclassified_share, null)
  assert.equal(emptyReport.summary.runner_wait.p90_seconds, null)
  assert.match(formatDaily(emptyReport), /n\/a \(0\)/)
  const duplicate = fixture(); duplicate.runs.push(duplicate.runs[0])
  assert.throws(() => correlate(duplicate, window), /duplicate run IDs/)
  assert.throws(() => correlate(fixture(), { ...window, to: '2026-10-10T00:00:00Z' }), /coverage/)
  assert.throws(() => correlate(fixture(), { from: window.to, to: window.from }), /positive/)
  await assert.rejects(main(['--apply']), /unknown/)
  assert.equal(requiredStatus({ required: { go: null } }), 'unknown')
  assert.equal(requiredStatus({ required: { go: 'missing' } }), 'red')
  assert.equal(requiredStatus({ required: { go: 'skipped' } }), 'green')
  assert.match(timelineQuery('inspr-at/paimos', 352), /^query /)
  assert.throws(() => timelineQuery('inspr-at/paimos', '1) { mutation'), /invalid/)
})

function recordedAPI() {
  const snapshot = fixture(), calls = []
  const selected = snapshot.runs.filter(run => [37541568521, 37547955166].includes(run.id))
  const required = snapshot.required_checks
  const get = async endpoint => {
    calls.push(endpoint)
    if (endpoint.includes('/workflows/')) return { total_count: selected.length, workflow_runs: selected }
    const attempt = endpoint.match(/runs\/(\d+)\/attempts\/1(\/jobs\?.*)?$/)
    if (attempt) {
      const run = selected.find(run => run.id === Number(attempt[1]))
      if (!attempt[2]) return { ...run, run_attempt: 1, status: 'completed', conclusion: run.a1_conclusion }
      return { total_count: required.length, jobs: required.map((name, index) => ({ name, run_attempt: 1,
        conclusion: run.required[name], created_at: run.created_at,
        started_at: new Date(Date.parse(run.created_at) + (index ? 10_000 : 90_000)).toISOString(), completed_at: run.a1_completed })) }
    }
    if (endpoint.includes('/pulls?')) return [snapshot.merged_prs.find(pr => pr.number === 352)]
    if (endpoint.includes('/git/commits/')) {
      const commit = snapshot.commits[endpoint.split('/').at(-1)]
      assert.ok(commit, endpoint)
      return { ...commit, message: commit.pr ? `Merge pull request #${commit.pr} from fixture/branch` : 'base', parents: commit.parents.map(sha => ({ sha })) }
    }
    assert.fail(endpoint)
  }
  const timeline = async (repository, pr, cursor) => {
    calls.push({ repository, pr, cursor })
    assert.equal(cursor, null)
    return { data: { repository: { pullRequest: { number: pr, timelineItems: {
      nodes: snapshot.timelines[pr], pageInfo: { hasNextPage: false, endCursor: null } } } } } }
  }
  return { get, timeline, calls }
}

test('collector joins attempt-specific jobs, exact commits and paginated queue events using read-only API surfaces', async () => {
  const api = recordedAPI(), bounds = { from: '2026-10-06T00:00:00Z', to: '2026-10-07T00:00:00Z' }
  const snapshot = await collect({ ...api, ...bounds })
  const report = correlate(snapshot, bounds)
  assert.equal(report.additional_runs.length, 1)
  assert.equal(report.additional_runs[0].cause, 'manual_removal')
  assert.equal(report.summary.runner_wait.p90_seconds, 90)
  assert.equal(snapshot.runs.find(run => run.id === 37541568521).a1_completed, '2026-10-06T22:49:50.000Z')
  assert.ok(api.calls.every(call => typeof call !== 'string' || /^repos\/inspr-at\/paimos\/(actions|pulls|git\/commits)/.test(call)))
  const twoPages = recordedAPI(); let timelinePages = 0
  const original = twoPages.timeline
  twoPages.timeline = async (repo, pr, cursor) => {
    timelinePages++
    const body = await original(repo, pr, null)
    const connection = body.data.repository.pullRequest.timelineItems
    const split = connection.nodes.length - 1
    connection.nodes = cursor ? connection.nodes.slice(split) : connection.nodes.slice(0, split)
    connection.pageInfo = { hasNextPage: !cursor, endCursor: cursor ? null : 'recorded-cursor' }
    return body
  }
  assert.equal(correlate(await collect({ ...twoPages, ...bounds }), bounds).additional_runs[0].cause, 'manual_removal')
  assert.ok(timelinePages >= 4)
})

test('truncated API pages, GraphQL errors, attempt drift and deadlines fail instead of publishing partial evidence', async () => {
  const bounds = { from: '2026-10-06T00:00:00Z', to: '2026-10-07T00:00:00Z' }
  const api = recordedAPI()
  await assert.rejects(collect({ ...api, ...bounds, timeline: async () => ({ errors: [{ message: 'denied' }] }) }), /incomplete GraphQL/)
  await assert.rejects(collect({ ...api, ...bounds, get: async endpoint => endpoint.includes('/jobs?')
    ? { total_count: 2001, jobs: Array.from({ length: 100 }, () => ({ run_attempt: 1 })) } : api.get(endpoint) }), /pagination bound/)
  await assert.rejects(collect({ ...api, ...bounds, get: async endpoint => {
    const body = await api.get(endpoint)
    return /\/attempts\/1$/.test(endpoint) ? { ...body, head_sha: 'a'.repeat(40) } : body
  } }), /attempt identity/)
  let now = 0
  await assert.rejects(collect({ ...api, ...bounds, now: () => now += 20 * 60_000 }), /deadline/)
})
