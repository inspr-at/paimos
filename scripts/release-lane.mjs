// SPDX-License-Identifier: AGPL-3.0-only
// Diagnostic release metrics only. These records never authorize a release.
import { execFileSync } from 'node:child_process';
import { appendFileSync, writeFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';
import { repository } from './release-hold.mjs';

export async function releaseLane(sha, api) {
  if (!/^[a-f0-9]{40}$/.test(sha)) throw new Error('Invalid release SHA');
  const workflow = await api('actions/workflows/ci.yml');
  const list = await api(`actions/workflows/${workflow.id}/runs?head_sha=${sha}&per_page=100`);
  if (workflow.path !== '.github/workflows/ci.yml' || !Number.isSafeInteger(workflow.id) || !Array.isArray(list.workflow_runs) ||
      list.total_count > 100 || list.workflow_runs.length !== list.total_count) throw new Error('Incomplete release lane inventory');
  const runs = list.workflow_runs.filter(run => run.head_sha === sha && run.repository?.full_name === repository &&
    run.head_repository?.full_name === repository && run.workflow_id === workflow.id && run.path === workflow.path &&
    run.status === 'completed' && run.conclusion === 'success' &&
    (run.event === 'merge_group' || (run.event === 'push' && run.head_branch === 'main')));
  // The successful queue lane is the release-path observation; the subsequent
  // full main run is the post-merge backstop, not a second queue lane.
  runs.sort((a, b) => Number(b.event === 'merge_group') - Number(a.event === 'merge_group') || b.id - a.id);
  const queue = runs.filter(run => run.event === 'merge_group');
  for (const run of (queue.length ? queue : runs).slice(0, 5)) {
    const result = await api(`actions/runs/${run.id}/artifacts?per_page=100`);
    if (!Array.isArray(result.artifacts) || result.total_count > 100 || result.artifacts.length !== result.total_count) throw new Error('Incomplete lane artifact inventory');
    const records = result.artifacts.filter(item => !item.expired &&
      new RegExp(`^ci-lane-(full|impacted|tree)-${sha}-${run.run_attempt}$`).test(item.name) &&
      item.workflow_run?.id === run.id && item.workflow_run?.head_sha === sha);
    if (records.length > 1) throw new Error('Ambiguous release lane');
    if (records.length) {
      let lane = records[0].name.split('-')[2];
      // AEON-423's successful reuse step overrides the fallback lane artifact.
      const jobs = await api(`actions/runs/${run.id}/attempts/${run.run_attempt}/jobs?per_page=100`);
      if (!Array.isArray(jobs.jobs) || jobs.total_count > 100 || jobs.jobs.length !== jobs.total_count) throw new Error('Incomplete queue lane jobs');
      if (run.event === 'merge_group' && jobs.jobs.some(job => job.steps?.some(step =>
        step.name === 'Reuse the verified successful tree' && step.conclusion === 'success'))) lane = 'tree';
      if (run.event === 'push' && lane !== 'full') throw new Error('Main lane must be full');
      return { schema: 'aeon.release-lane.v1', sha, lane, source_run: run.id, source_attempt: run.run_attempt,
        artifact_id: records[0].id, evidence: 'CI diagnostic; not authorization' };
    }
  }
  return { schema: 'aeon.release-lane.v1', sha, lane: 'unknown', source_run: null,
    reason: 'No exact-commit queue/main lane receipt (including older workflows or squash merges)' };
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    if (process.argv.length !== 2) throw new Error('Unexpected arguments');
    const api = path => JSON.parse(execFileSync('gh', ['api', '--method', 'GET', `repos/${repository}/${path}`],
      { encoding: 'utf8', timeout: 15_000, maxBuffer: 2 * 1024 * 1024, stdio: ['ignore', 'pipe', 'ignore'] }));
    const result = await releaseLane(process.env.GITHUB_SHA, api);
    const json = JSON.stringify(result);
    writeFileSync('release-lane.json', `${json}\n`);
    if (process.env.GITHUB_STEP_SUMMARY) appendFileSync(process.env.GITHUB_STEP_SUMMARY, `\nRelease CI lane: ${json}\n`);
    console.log(json);
  } catch { console.error('Release lane measurement incomplete'); process.exitCode = 1; }
}
