// SPDX-License-Identifier: AGPL-3.0-only
// AEON-423: reuse only an API-verified full merge-group suite at this exact SHA.
import { appendFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';

export const workflowPath = '.github/workflows/ci.yml';
const oid = /^[a-f0-9]{40}$/;
const repositoryPattern = /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/;
const positive = value => Number.isSafeInteger(value) && value > 0;
const ensure = (value, message) => { if (!value) throw new Error(message); };

// A green aggregate is insufficient. These execution steps must have run.
export const executionSteps = {
  'ci-plan': 'Classify local PR diff',
  'go-static': 'Check prepared rules bootstrap drift',
  'go-timing': 'Timing budgets, alone',
  'web-setup': 'Build web',
  'release-check-run': 'Enforce test runner trust boundary',
  'e2e-run': 'Start server and run smoke',
  'migration-compat': 'Previous binary on the candidate schema',
};
export const requiredJobs = [
  'tier-plan', 'tier-measurements',
  'runner-route / route', 'go', 'web', 'release-check', 'e2e',
  ...Object.keys(executionSteps),
  ...Array.from({ length: 7 }, (_, i) => `go-test (${i + 1})`),
  ...Array.from({ length: 4 }, (_, i) => `web-unit (${i + 1})`),
  ...Array.from({ length: 12 }, (_, i) => `web-shard (${i + 1})`),
];

// Bound the entire operation, every response and redirects before decoding.
export function transport(token, actor, signal, fetcher = fetch) {
  return async (url, options = {}) => {
    const parsed = new URL(url);
    ensure(parsed.protocol === 'https:' && parsed.hostname === 'api.github.com' && !parsed.username && !parsed.password, 'unapproved endpoint');
    const response = await fetcher(url, { ...options, signal, redirect: 'error', headers: {
      Authorization: `Bearer ${token}`, Accept: 'application/vnd.github+json', 'X-GitHub-Api-Version': '2022-11-28',
      'User-Agent': actor, ...options.headers,
    } });
    ensure(response.ok, 'remote request failed');
    const limit = 2 * 1024 * 1024;
    ensure(Number(response.headers.get('content-length') || 0) <= limit, 'response too large');
    const chunks = [];
    let size = 0;
    if (response.body) {
      for await (const chunk of response.body) {
        size += chunk.length;
        if (size > limit) { await response.body.cancel().catch(() => {}); throw new Error('response too large'); }
        chunks.push(Buffer.from(chunk));
      }
    }
    const bytes = Buffer.concat(chunks);
    return { headers: response.headers, json: () => JSON.parse(bytes.toString('utf8')) };
  };
}

export function github(repository, request) {
  ensure(repositoryPattern.test(repository), 'invalid repository');
  return async (path, options) => (await request(`https://api.github.com/repos/${repository}/${path}`, options)).json();
}

// Read the latest attempt, never a mix of retained jobs from earlier attempts.
// No commit/tree lookup, registry record or source checkout is involved.
export async function verifyRun(repository, runID, api, sha) {
  ensure(positive(runID) && oid.test(sha), 'invalid run or SHA');
  const [run, workflow] = await Promise.all([
    api(`actions/runs/${runID}`), api('actions/workflows/ci.yml'),
  ]);
  ensure(run.id === runID && run.repository?.full_name === repository && run.head_repository?.full_name === repository && positive(run.repository?.id), 'wrong repository');
  ensure(workflow.name === 'CI' && run.name === 'CI' && workflow.path === workflowPath && run.path === workflowPath && run.workflow_id === workflow.id && positive(workflow.id), 'wrong workflow');
  ensure(run.status === 'completed' && run.conclusion === 'success' && positive(run.run_attempt), 'run not successful');
  ensure(run.event === 'merge_group' && run.head_sha === sha, 'not the exact merge-group SHA');
  const jobs = await api(`actions/runs/${runID}/attempts/${run.run_attempt}/jobs?per_page=100`);
  ensure(positive(jobs.total_count) && jobs.total_count <= 100 && jobs.jobs?.length === jobs.total_count, 'incomplete job list');
  ensure(jobs.jobs.every(job => job.run_attempt === run.run_attempt && job.status === 'completed' && (job.conclusion === 'success' || (['cache-prime', 'tree-reuse'].includes(job.name) && job.conclusion === 'skipped'))), 'incomplete suite');
  // The only optional/skipped jobs are push-only proof and cache priming. Unknown or
  // duplicate jobs cannot silently change the suite this proof recognizes.
  ensure(jobs.jobs.every(job => requiredJobs.includes(job.name) || ['cache-prime', 'tree-reuse'].includes(job.name)), 'unknown suite job');
  ensure(new Set(jobs.jobs.map(job => job.name)).size === jobs.jobs.length, 'duplicate suite job');
  for (const name of requiredJobs) {
    const job = jobs.jobs.find(job => job.name === name);
    ensure(job?.conclusion === 'success', 'missing or failed suite job');
    const stepName = executionSteps[name] || (name.startsWith('go-test (') ? 'Test this shard (essential plus changed area, or full on main)' : name.startsWith('web-unit (') ? 'Run selected web units without retries' : name.startsWith('web-shard (') ? 'Run selected UI cases without retries' : undefined);
    if (stepName) {
      const steps = job.steps?.filter(step => step.name === stepName);
      ensure(steps?.length === 1 && steps[0].status === 'completed' && steps[0].conclusion === 'success', 'missing full execution evidence');
    }
    if(/^(go-test|web-unit|web-shard) \(/.test(name)) {
      const full=job.steps?.filter(step=>step.name==='Confirm full tier execution');
      ensure(full?.length===1&&full[0].status==='completed'&&full[0].conclusion==='success','missing full tier execution evidence');
    }
    ensure(!job.steps?.some(step => step.name === 'Reuse the verified merge-group run' && step.conclusion !== 'skipped'), 'reused suite is not execution proof');
  }
  return { run: runID, attempt: run.run_attempt, sha };
}

export async function decide({ event, ref, killSwitch, sha, repository, api }) {
  if (event !== 'push' || ref !== 'refs/heads/main' || killSwitch === 'off') return { reuse: 'none', reason: 'disabled or not a main push' };
  try {
    ensure(oid.test(sha), 'invalid push SHA');
    const found = await api(`actions/workflows/ci.yml/runs?event=merge_group&status=success&head_sha=${sha}&per_page=20`);
    ensure(Number.isSafeInteger(found.total_count) && found.total_count >= 0 && Array.isArray(found.workflow_runs) && found.workflow_runs.length <= 20, 'invalid run list');
    // Bounded discovery: absent/truncated older evidence simply runs full CI.
    for (const candidate of found.workflow_runs) {
      if (candidate.head_sha !== sha || candidate.event !== 'merge_group') continue;
      try {
        const verified = await verifyRun(repository, candidate.id, api, sha);
        return { reuse: 'merge_group', run: verified.run, reason: `reused merge_group run ${verified.run}` };
      } catch { /* Failed, cancelled, partial rerun or stale candidate: try next. */ }
    }
    return { reuse: 'none', reason: `no verified successful merge_group run for this SHA in the bounded candidate list${found.total_count > found.workflow_runs.length ? '; search truncated' : ''}` };
  } catch {
    return { reuse: 'none', reason: 'merge_group lookup unavailable; full CI required' };
  }
}

export async function main(vars, fetcher = fetch) {
  let decision = { reuse: 'none', reason: 'disabled or not a main push' };
  if (vars.GITHUB_EVENT_NAME === 'push' && vars.GITHUB_REF === 'refs/heads/main' && vars.CI_TREE_REUSE !== 'off') {
    const request = transport(vars.GH_TOKEN, vars.GITHUB_ACTOR, AbortSignal.timeout(10000), fetcher);
    decision = await decide({ event: vars.GITHUB_EVENT_NAME, ref: vars.GITHUB_REF, killSwitch: vars.CI_TREE_REUSE, sha: vars.GITHUB_SHA, repository: vars.GITHUB_REPOSITORY, api: github(vars.GITHUB_REPOSITORY, request) });
  }
  if (vars.GITHUB_OUTPUT) appendFileSync(vars.GITHUB_OUTPUT, `reuse=${decision.reuse}\nrun=${decision.run || ''}\n`);
  const note = `reuse=${decision.reuse}; ${decision.reason}`;
  if (vars.GITHUB_STEP_SUMMARY) appendFileSync(vars.GITHUB_STEP_SUMMARY, `${note}\n`);
  console.log(note);
  return decision;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main(process.env).catch(() => { console.error('Reuse proof unavailable; outputs unset, full CI required'); process.exitCode = 1; });
}
