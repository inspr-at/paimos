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
  verifyJobs(jobs, run.run_attempt);
  return { run: runID, attempt: run.run_attempt, sha };
}

// Shared execution obligations: never substitute green aggregate conclusions.
export function verifyJobs(jobs, attempt) {
  ensure(positive(jobs.total_count) && jobs.total_count <= 100 && jobs.jobs?.length === jobs.total_count, 'incomplete job list');
  ensure(jobs.jobs.every(job => job.run_attempt === attempt && job.status === 'completed' && (job.conclusion === 'success' || (['cache-prime', 'tree-reuse'].includes(job.name) && job.conclusion === 'skipped'))), 'incomplete suite');
  // The only optional/skipped jobs are reuse lookup and main cache priming. Unknown or
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
}

// The API-visible aggregate name binds the PR checkout, rather than the mutable
// pulls/N/merge ref. It is produced inline by CI, with no artifact or registry.
export const prProofPrefix = 'tier-measurements / pr-tree-v1:';
export function parsePRProof(name) {
  ensure(typeof name === 'string' && name.startsWith(prProofPrefix), 'missing PR tree proof');
  const fields = name.slice(prProofPrefix.length).split(':');
  ensure(fields.length === 6 && fields.slice(0, 2).every(x => /^[1-9][0-9]*$/.test(x)) && fields.slice(2).every(x => oid.test(x)), 'malformed PR tree proof');
  const [run, attempt] = fields.slice(0, 2).map(Number);
  ensure(positive(run) && positive(attempt), 'unsafe PR proof identity');
  return { run, attempt, head: fields[2], base: fields[3], commit: fields[4], tree: fields[5] };
}

export function verifyPRFacts({ repository, run, workflow, jobs, commit, tree }) {
  ensure(run.repository?.full_name === repository && run.head_repository?.full_name === repository && positive(run.repository?.id), 'wrong repository');
  ensure(workflow.name === 'CI' && run.name === 'CI' && workflow.path === workflowPath && run.path === workflowPath && run.workflow_id === workflow.id && positive(workflow.id), 'wrong workflow');
  ensure(run.event === 'pull_request' && run.status === 'completed' && run.conclusion === 'success' && oid.test(run.head_sha), 'not a green PR run');
  // Reject every rerun, including retained jobs stamped with a newer attempt.
  // Conservative relative to main reuse: a fresh run is needed for PR evidence.
  ensure(run.run_attempt === 1, 'PR reruns are not execution proof');
  ensure(Array.isArray(jobs.jobs), 'missing jobs');
  ensure(positive(jobs.total_count) && jobs.total_count <= 100 && jobs.jobs.length === jobs.total_count, 'incomplete job list');
  const proofJobs = jobs.jobs.filter(job => job.name?.startsWith(prProofPrefix));
  ensure(proofJobs.length === 1, 'missing or duplicate PR proof');
  const proof = parsePRProof(proofJobs[0].name);
  ensure(proof.run === run.id && proof.attempt === run.run_attempt && proof.head === run.head_sha, 'PR proof does not match run');
  ensure(proofJobs[0].steps?.filter(s => s.name === 'Confirm PR tree proof' && s.status === 'completed' && s.conclusion === 'success').length === 1, 'PR proof was not emitted by full aggregate');
  ensure(commit.sha === proof.commit && commit.tree?.sha === proof.tree && commit.parents?.length === 2 && commit.parents[0].sha === proof.base && commit.parents[1].sha === proof.head, 'PR checkout is not the proven merge');
  ensure(oid.test(tree) && proof.tree === tree, 'different tested tree');
  verifyJobs({ ...jobs, jobs: jobs.jobs.map(job => job === proofJobs[0] ? { ...job, name: 'tier-measurements' } : job) }, run.run_attempt);
  return proof;
}

export async function verifyPRRun(repository, runID, api, sha) {
  ensure(repositoryPattern.test(repository) && positive(runID) && oid.test(sha), 'invalid PR lookup');
  const [run, workflow, candidate] = await Promise.all([
    api(`actions/runs/${runID}`), api('actions/workflows/ci.yml'), api(`git/commits/${sha}`),
  ]);
  ensure(run.id === runID && candidate.sha === sha && oid.test(candidate.tree?.sha), 'wrong run or merge-group commit');
  ensure(run.run_attempt === 1, 'PR reruns are not execution proof');
  const jobs = await api(`actions/runs/${runID}/attempts/1/jobs?per_page=100`);
  const proofJob = jobs.jobs?.find(job => job.name?.startsWith(prProofPrefix));
  const proof = parsePRProof(proofJob?.name);
  const commit = await api(`git/commits/${proof.commit}`);
  const verified = verifyPRFacts({ repository, run, workflow, jobs, commit, tree: candidate.tree.sha });
  // Catch a rerun/cancellation started while evidence was being inspected.
  const latest = await api(`actions/runs/${runID}`);
  ensure(latest.id === runID && latest.run_attempt === verified.attempt && latest.status === 'completed' && latest.conclusion === 'success' && latest.head_sha === verified.head, 'PR evidence changed during lookup');
  return verified;
}

export async function decideMergeGroup({ event, killSwitch, sha, repository, api }) {
  if (event !== 'merge_group' || killSwitch !== 'on') return { reuse: 'none', reason: 'merge-group reuse disabled' };
  try {
    ensure(oid.test(sha), 'invalid merge-group SHA');
    const found = await api('actions/workflows/ci.yml/runs?event=pull_request&status=success&per_page=20');
    ensure(Number.isSafeInteger(found.total_count) && found.total_count >= 0 && Array.isArray(found.workflow_runs) && found.workflow_runs.length <= 20, 'invalid PR run list');
    for (const candidate of found.workflow_runs) {
      if (candidate?.event !== 'pull_request') continue;
      try {
        const proof = await verifyPRRun(repository, candidate.id, api, sha);
        return { reuse: 'pull_request', run: proof.run, reason: `reused pull_request run ${proof.run}; exact tested tree ${proof.tree}` };
      } catch { /* Every uncertain candidate retains full validation. */ }
    }
    return { reuse: 'none', reason: `no verified full PR tree in bounded candidate list${found.total_count > found.workflow_runs.length ? '; search truncated' : ''}` };
  } catch {
    return { reuse: 'none', reason: 'PR lookup unavailable; full CI required' };
  }
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
  if (vars.GITHUB_EVENT_NAME === 'merge_group' && vars.CI_MG_REUSE === 'on') {
    const request = transport(vars.GH_TOKEN, vars.GITHUB_ACTOR, AbortSignal.timeout(10000), fetcher);
    const api = github(vars.GITHUB_REPOSITORY, request);
    if (vars.CONFIRM_PR_RUN) {
      ensure(/^[1-9][0-9]*$/.test(vars.CONFIRM_PR_RUN), 'invalid confirmation run');
      const proof = await verifyPRRun(vars.GITHUB_REPOSITORY, Number(vars.CONFIRM_PR_RUN), api, vars.GITHUB_SHA);
      decision = { reuse: 'pull_request', run: proof.run, reason: 'PR execution proof confirmed for aggregate' };
    } else {
      decision = await decideMergeGroup({ event: vars.GITHUB_EVENT_NAME, killSwitch: vars.CI_MG_REUSE, sha: vars.GITHUB_SHA, repository: vars.GITHUB_REPOSITORY, api });
    }
  } else if (vars.CONFIRM_PR_RUN) {
    throw new Error('PR proof confirmation disabled or wrong event');
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
