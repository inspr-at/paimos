// SPDX-License-Identifier: AGPL-3.0-only
// One trusted, serialized writer. Consumers always read the live variable.
import { appendFileSync, readFileSync, statSync } from 'node:fs';
import { pathToFileURL } from 'node:url';

export const repository = 'inspr-at/paimos';
const oid = value => typeof value === 'string' && /^[a-f0-9]{40}$/.test(value);
const positive = value => Number.isSafeInteger(value) && value > 0;
const requireValue = (value, message) => { if (!value) throw new Error(`release hold: ${message}`); };
const red = new Set(['failure', 'timed_out', 'startup_failure', 'action_required', 'stale']);
const limit = 2 * 1024 * 1024;

// No API bodies, credentials, or fetch errors are included in diagnostics.
export function github(token, { fetcher = fetch, signal = AbortSignal.timeout(120_000) } = {}) {
  requireValue(typeof token === 'string' && token.length > 0, 'dedicated token missing');
  return async (method, path, body) => {
    requireValue(path.startsWith(`/repos/${repository}/`) && !path.includes('..'), 'unapproved API path');
    let response;
    try {
      response = await fetcher(`https://api.github.com${path}`, {
        method, redirect: 'error', signal: AbortSignal.any([signal, AbortSignal.timeout(10_000)]),
        headers: { Authorization: `Bearer ${token}`, Accept: 'application/vnd.github+json',
          'X-GitHub-Api-Version': '2022-11-28', 'User-Agent': 'aeon-release-hold',
          ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
        body: body === undefined ? undefined : JSON.stringify(body),
      });
      requireValue(Number(response.headers.get('content-length') ?? 0) <= limit, 'response too large');
      const chunks = [];
      let size = 0;
      if (response.body) for await (const chunk of response.body) {
        size += chunk.length;
        requireValue(size <= limit, 'response too large');
        chunks.push(Buffer.from(chunk));
      }
      const bytes = Buffer.concat(chunks);
      return { status: response.status, data: bytes.length ? JSON.parse(bytes.toString('utf8')) : null };
    } catch { throw new Error('release hold: bounded API request failed'); }
  };
}

const root = `/repos/${repository}`;
async function get(api, path, method = 'GET', body, status = 200) {
  const response = await api(method, `${root}/${path}`, body);
  requireValue(response.status === status, `API ${method} failed (HTTP ${response.status})`);
  return response.data;
}

export async function readHold(api) {
  // A 404 on a variable is ambiguous (missing variable or denied access).
  // Only an authenticated, complete list may prove the absence of a hold.
  const result = await get(api, 'actions/variables?per_page=30&page=1');
  requireValue(Number.isSafeInteger(result?.total_count) && result.total_count >= 0 && result.total_count <= 30 &&
    Array.isArray(result.variables) && result.variables.length === result.total_count, 'incomplete variable list');
  requireValue(result.variables.every(item => item && !Array.isArray(item) &&
    typeof item.name === 'string' && item.name.length > 0 && typeof item.value === 'string'), 'malformed variable record');
  const matches = result.variables.filter(item => item.name === 'RELEASE_HOLD');
  requireValue(matches.length <= 1, 'ambiguous variable');
  const value = matches.length === 0 ? '' : matches[0].value;
  requireValue(value === '' || oid(value), 'malformed hold');
  return { value, exists: matches.length === 1 };
}

export async function assertReleaseAllowed(api) {
  const hold = await readHold(api);
  requireValue(hold.value === '', `RELEASE_HOLD=${hold.value}; release and rollout refused`);
  return { hold: '', checked: true };
}

function validRun(run, workflow) {
  requireValue(run && positive(run.id) && positive(run.run_attempt) && oid(run.head_sha) &&
    run.repository?.full_name === repository && run.head_repository?.full_name === repository &&
    run.event === 'push' && run.head_branch === 'main' && run.path === '.github/workflows/ci.yml' &&
    run.workflow_id === workflow.id, 'untrusted main CI run');
  requireValue(['queued', 'in_progress', 'completed', 'waiting', 'pending', 'requested'].includes(run.status), 'unknown run status');
  if (run.status === 'completed') requireValue(red.has(run.conclusion) || ['success', 'cancelled', 'skipped', 'neutral'].includes(run.conclusion), 'unknown conclusion');
  return run;
}

async function fullGreen(api, run) {
  const result = await get(api, `actions/runs/${run.id}/attempts/${run.run_attempt}/jobs?per_page=100`);
  requireValue(positive(result?.total_count) && result.total_count <= 100 &&
    Array.isArray(result.jobs) && result.jobs.length === result.total_count, 'incomplete green job list');
  requireValue(result.jobs.every(job => job.status === 'completed' && job.conclusion === 'success'), 'green run contains unfinished or skipped jobs');
  const names = new Set(result.jobs.map(job => job.name));
  for (const name of ['go', 'go-static', 'go-timing', 'web', 'release-check', 'e2e', 'footer-ui',
    'status-help-ui', 'status-autopilot-ui', 'migration-compat', 'runner-route / route']) {
    requireValue(names.has(name), 'green run misses full-suite coverage');
  }
  const shards = result.jobs.filter(job => /^go-test \([1-7]\)$/.test(job.name)).map(job => job.name).sort();
  requireValue([4, 7].includes(shards.length) && shards.every((name, i) => name === `go-test (${i + 1})`), 'green run misses Go shards');
}

// Return -1 for an ancestor, 0 for identical, 1 for a descendant. Diverged
// histories cannot order a hold; neither timestamps nor completion order do.
export function ancestry(api) {
  const cache = new Map();
  return async (a, b) => {
    requireValue(oid(a) && oid(b), 'invalid ancestry input');
    if (a === b) return 0;
    const key = `${a}...${b}`;
    if (cache.has(key)) return cache.get(key);
    const result = await get(api, `compare/${key}?per_page=1`);
    requireValue(result?.base_commit?.sha === a && oid(result.merge_base_commit?.sha), 'invalid comparison');
    let order;
    if (result.status === 'ahead' && result.merge_base_commit.sha === a) order = -1;
    else if (result.status === 'behind' && result.merge_base_commit.sha === b) order = 1;
    else throw new Error('release hold: divergent or incomplete main history');
    cache.set(key, order); cache.set(`${b}...${a}`, -order);
    return order;
  };
}

export async function decideHold(current, runs, compare) {
  requireValue(current === '' || oid(current), 'malformed current hold');
  // Superseded run ids/attempts for the same commit cannot beat a newer run.
  const bySHA = new Map();
  for (const run of runs) {
    if (run.status !== 'completed' || !(red.has(run.conclusion) || run.conclusion === 'success')) continue;
    const previous = bySHA.get(run.head_sha);
    if (!previous || run.id > previous.id || (run.id === previous.id && run.run_attempt > previous.run_attempt)) bySHA.set(run.head_sha, run);
  }
  let latest;
  for (const run of bySHA.values()) if (!latest || await compare(latest.head_sha, run.head_sha) < 0) latest = run;
  if (!latest) return { value: current, action: 'unchanged', run: null };
  if (latest.conclusion === 'success') {
    if (current && await compare(current, latest.head_sha) <= 0) return { value: '', action: 'clear', run: latest };
    return { value: current, action: 'unchanged', run: latest };
  }
  if (!current || await compare(current, latest.head_sha) <= 0) {
    return { value: latest.head_sha, action: current === latest.head_sha ? 'unchanged' : 'engage', run: latest };
  }
  return { value: current, action: 'unchanged', run: latest };
}

async function alert(api, sha, run, closed = false) {
  const title = 'Release hold: main CI';
  const issues = await get(api, 'issues?state=open&per_page=100');
  requireValue(Array.isArray(issues) && issues.length < 100, 'alert inventory incomplete');
  const matches = issues.filter(issue => !issue.pull_request && issue.title === title);
  requireValue(matches.length <= 1, 'ambiguous alert');
  if (closed) {
    if (matches.length) await get(api, `issues/${matches[0].number}`, 'PATCH', { state: 'closed' });
    return;
  }
  if (!matches.length) await get(api, 'issues', 'POST', {
    title, body: `Main CI failed at ${sha}. RELEASE_HOLD blocks tags and rollouts.\n\n` +
      `Run: https://github.com/${repository}/actions/runs/${run.id}\n\n` +
      'Only a successful full main CI run at this commit or a descendant clears the hold. AEON-427.',
  }, 201);
  else if (!matches[0].body?.includes(`Main CI failed at ${sha}.`)) {
    const previous = matches[0].body ?? '';
    requireValue(previous.length <= 8192, 'alert history too large');
    await get(api, `issues/${matches[0].number}`, 'PATCH', { body: `${previous}\n\nMain CI failed at ${sha}.\nRun: https://github.com/${repository}/actions/runs/${run.id}` });
  }
}

export async function reconcileHold(api, { sourceRunID, write = false, now = () => new Date() } = {}) {
  const started = now();
  const workflow = await get(api, 'actions/workflows/ci.yml');
  requireValue(positive(workflow?.id) && workflow.path === '.github/workflows/ci.yml', 'wrong workflow identity');
  const current = await readHold(api);
  const listed = await get(api, `actions/workflows/${workflow.id}/runs?branch=main&event=push&per_page=100`);
  requireValue(Number.isSafeInteger(listed?.total_count) && Array.isArray(listed.workflow_runs) &&
    listed.workflow_runs.length <= 100 && (listed.total_count <= 100 ? listed.workflow_runs.length === listed.total_count : listed.workflow_runs.length === 100), 'incomplete run window');
  const runs = listed.workflow_runs.map(run => validRun(run, workflow));
  if (sourceRunID !== undefined) {
    requireValue(positive(sourceRunID), 'invalid source run id');
    const source = validRun(await get(api, `actions/runs/${sourceRunID}`), workflow);
    const index = runs.findIndex(run => run.id === source.id);
    if (index < 0) runs.push(source); else runs[index] = source;
  }
  const compare = ancestry(api);
  const decision = await decideHold(current.value, runs, compare);
  // A paged-out old failure cannot be cleared on absence; an actual green
  // descendant must prove it is resolved, including a stale variable.
  if (decision.run?.conclusion === 'success') await fullGreen(api, decision.run);
  let observed = started;
  if (write) {
    const before = await readHold(api);
    requireValue(before.value === current.value && before.exists === current.exists, 'variable changed during reconciliation');
    if (decision.action === 'engage') {
      await get(api, current.exists ? 'actions/variables/RELEASE_HOLD' : 'actions/variables',
        current.exists ? 'PATCH' : 'POST', { name: 'RELEASE_HOLD', value: decision.value }, current.exists ? 204 : 201);
    } else if (decision.action === 'clear') await get(api, 'actions/variables/RELEASE_HOLD', 'DELETE', undefined, 204);
    const after = await readHold(api);
    requireValue(after.value === decision.value, 'hold write did not persist');
    observed = now();
    // Engage before alerting: an alert failure must leave the release brake set.
    if (decision.value && decision.run && red.has(decision.run.conclusion) && decision.run.head_sha === decision.value) await alert(api, decision.value, decision.run);
    if (!decision.value && decision.run?.conclusion === 'success') await alert(api, current.value, decision.run, true);
  }
  const completed = decision.run?.updated_at ? Date.parse(decision.run.updated_at) : NaN;
  const elapsed = Number.isFinite(completed) && observed.getTime() >= completed ? (observed.getTime() - completed) / 1000 : null;
  return { schema: 'aeon.release-hold.v1', mode: write ? 'write' : 'preview', ...decision,
    run: decision.run ? { id: decision.run.id, sha: decision.run.head_sha, conclusion: decision.run.conclusion } : null,
    observed_at: observed.toISOString(), engage_after_red_s: decision.action === 'engage' && write ? elapsed : null,
    six_minute_target_met: decision.action === 'engage' && write && elapsed !== null ? elapsed <= 360 : null };
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const args = process.argv.slice(2);
    let result;
    if (args.length === 1 && args[0] === 'check') {
      result = await assertReleaseAllowed(github(process.env.AEON_RELEASE_HOLD_READ_TOKEN));
    } else {
      requireValue(args[0] === 'reconcile' && args.length <= 2 && (args.length === 1 || args[1] === '--write'), 'usage: release-hold.mjs check | reconcile [--write]');
      const write = args[1] === '--write';
      requireValue(!write || (process.env.GITHUB_REPOSITORY === repository && ['workflow_run', 'schedule', 'workflow_dispatch'].includes(process.env.GITHUB_EVENT_NAME) && process.env.GITHUB_REF === 'refs/heads/main'), 'writer must run from trusted main workflow');
      let sourceRunID;
      if (process.env.GITHUB_EVENT_NAME === 'workflow_run') {
        requireValue(statSync(process.env.GITHUB_EVENT_PATH).size <= limit, 'completion event too large');
        const event = JSON.parse(readFileSync(process.env.GITHUB_EVENT_PATH, 'utf8'));
        requireValue(event.action === 'completed' && positive(event.workflow_run?.id), 'invalid completion event');
        sourceRunID = event.workflow_run.id;
      }
      result = await reconcileHold(github(process.env.AEON_RELEASE_HOLD_TOKEN), { sourceRunID, write });
    }
    const json = JSON.stringify(result);
    console.log(json);
    if (process.env.GITHUB_STEP_SUMMARY) appendFileSync(process.env.GITHUB_STEP_SUMMARY, `\nRelease hold observation: ${json}\n`);
  } catch (error) {
    console.error(error.message?.startsWith('release hold:') ? error.message : 'release hold: operation failed');
    process.exitCode = 1;
  }
}
