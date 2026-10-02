// SPDX-License-Identifier: AGPL-3.0-only
// A green PR run is diagnostic. Only an exact-SHA main run authorizes release.
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

export const repository = 'inspr-at/paimos';
export const workflow = '.github/workflows/release-image-check.yml';
export const requiredJobs = ['image-dry-run (amd64)', 'image-dry-run (arm64)',
  'agentd-rehearsal (arm64)', 'agentd-rehearsal (amd64)',
  'assets-rehearsal', 'release-rehearsal'];

export async function requireRehearsal(sha, api) {
  if (!/^[a-f0-9]{40}$/.test(sha)) throw new Error('Expected the full release commit SHA');
  const policy = await api(`repos/${repository}/actions/workflows/release-image-check.yml`);
  if (!Number.isSafeInteger(policy.id) || policy.path !== workflow || policy.state !== 'active') {
    throw new Error('Rehearsal workflow identity is unavailable');
  }
  // Do not filter success: the newest eligible attempt can revoke an old success.
  const pages = await api(`repos/${repository}/actions/workflows/${policy.id}/runs?head_sha=${sha}&per_page=100`, true);
  if (!Array.isArray(pages) || pages.some(p => !Array.isArray(p.workflow_runs))) throw new Error('Invalid rehearsal run listing');
  const runs = pages.flatMap(p => p.workflow_runs).filter(r => r.workflow_id === policy.id &&
    r.head_sha === sha && r.head_branch === 'main' && ['push', 'workflow_dispatch'].includes(r.event) &&
    r.path === workflow && r.repository?.full_name === repository && r.head_repository?.full_name === repository);
  runs.sort((a, b) => b.id - a.id);
  const run = runs[0];
  if (!run || !Number.isSafeInteger(run.id) || !Number.isSafeInteger(run.run_attempt) ||
      run.status !== 'completed' || run.conclusion !== 'success') throw new Error('No current green main rehearsal for this exact SHA');
  const jobPages = await api(`repos/${repository}/actions/runs/${run.id}/attempts/${run.run_attempt}/jobs?per_page=100`, true);
  if (!Array.isArray(jobPages) || jobPages.some(p => !Array.isArray(p.jobs))) throw new Error('Invalid rehearsal job listing');
  const jobs = jobPages.flatMap(p => p.jobs);
  if (jobs.length !== requiredJobs.length || requiredJobs.some(name => {
    const matches = jobs.filter(j => j.name === name);
    return matches.length !== 1 || matches[0].status !== 'completed' || matches[0].conclusion !== 'success' ||
      !Array.isArray(matches[0].steps) || matches[0].steps.some(s => s.status !== 'completed' || s.conclusion !== 'success');
  })) throw new Error('Rehearsal jobs or steps are missing, skipped or failed');
  const artifacts = await api(`repos/${repository}/actions/runs/${run.id}/artifacts?per_page=100`, true);
  if (!Array.isArray(artifacts) || artifacts.some(p => !Array.isArray(p.artifacts))) throw new Error('Invalid receipt listing');
  const receipts = artifacts.flatMap(p => p.artifacts).filter(a => a.name === `release-rehearsal-${sha}-${run.run_attempt}`);
  if (receipts.length !== 1 || receipts[0].expired !== false || receipts[0].size_in_bytes <= 0 ||
      receipts[0].workflow_run?.head_sha !== sha) throw new Error('Exact-SHA rehearsal receipt is missing or expired');
  return { sha, run_id: run.id, run_attempt: run.run_attempt, url: run.html_url };
}

function github(path, paginate = false) {
  const result = spawnSync('gh', ['api', ...(paginate ? ['--paginate', '--slurp'] : []), path],
    { encoding: 'utf8', timeout: 60_000, maxBuffer: 16 * 1024 * 1024 });
  if (result.error || result.status !== 0) throw new Error('Rehearsal API read failed; release remains blocked');
  try { return JSON.parse(result.stdout); } catch { throw new Error('Unreadable rehearsal API response'); }
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  try {
    if (process.argv.length !== 4 || process.argv[2] !== '--sha') throw new Error('Usage: check-release-rehearsal.mjs --sha FULL_SHA');
    console.log(JSON.stringify(await requireRehearsal(process.argv[3], github)));
  } catch (error) { console.error(error.message); process.exitCode = 1; }
}
