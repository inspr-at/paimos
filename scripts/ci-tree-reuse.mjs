// SPDX-License-Identifier: AGPL-3.0-only
// Records are hints. Only repository-scoped, successful ci.yml runs prove a tree.
import { createHash } from 'node:crypto';
import { appendFileSync, openSync, readSync, closeSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { pathToFileURL } from 'node:url';

export const workflowPath = '.github/workflows/ci.yml';
const schema = 'aeon.ci.tree-green.v1';
const manifestType = 'application/vnd.oci.image.manifest.v1+json';
const emptyType = 'application/vnd.oci.empty.v1+json';
const recordType = 'application/vnd.aeon.ci.tree-green.v1+json';
const oid = /^[a-f0-9]{40}$/;
const repositoryPattern = /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/;
const positive = value => Number.isSafeInteger(value) && value > 0;
const digest = bytes => `sha256:${createHash('sha256').update(bytes).digest('hex')}`;
const ensure = (value, message) => { if (!value) throw new Error(message); };

// Bound the entire operation, every response and redirects before decoding.
export function transport(token, actor, signal, fetcher = fetch) {
  return async (url, options = {}) => {
    const parsed = new URL(url);
    ensure(parsed.protocol === 'https:' && ['api.github.com', 'ghcr.io'].includes(parsed.hostname) && !parsed.username && !parsed.password, 'unapproved endpoint');
    const response = await fetcher(url, { ...options, signal, redirect: 'error', headers: {
      ...(parsed.hostname === 'api.github.com' ? { Authorization: `Bearer ${token}`, Accept: 'application/vnd.github+json', 'X-GitHub-Api-Version': '2022-11-28' } : {}),
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

// PR run.head_sha is the branch head, not GITHUB_SHA's synthetic merge commit.
// An ancestor base proves the merge tree is the head tree without trusting PR artifacts.
export async function verifyRun(repository, runID, api) {
  ensure(positive(runID), 'invalid run');
  const [run, workflow] = await Promise.all([
    api(`actions/runs/${runID}`), api('actions/workflows/ci.yml'),
  ]);
  ensure(run.id === runID && run.repository?.full_name === repository && run.head_repository?.full_name === repository && positive(run.repository?.id), 'wrong repository');
  ensure(workflow.path === workflowPath && run.path === workflowPath && run.workflow_id === workflow.id && positive(workflow.id), 'wrong workflow');
  ensure(run.status === 'completed' && run.conclusion === 'success' && positive(run.run_attempt), 'run not successful');
  ensure(['push', 'pull_request'].includes(run.event) && oid.test(run.head_sha), 'ineligible event');
  if (run.event === 'push') ensure(run.head_branch === 'main', 'ineligible push');
  if (run.event === 'pull_request') {
    ensure(run.pull_requests?.length === 1, 'missing PR binding');
    const pr = run.pull_requests[0];
    ensure(pr.head?.sha === run.head_sha && pr.head?.repo?.id === run.repository.id && pr.base?.repo?.id === run.repository.id && oid.test(pr.base?.sha), 'wrong PR binding');
    const comparison = await api(`compare/${pr.base.sha}...${run.head_sha}?per_page=1`);
    ensure(['ahead', 'identical'].includes(comparison.status) && comparison.merge_base_commit?.sha === pr.base.sha, 'PR merge tree differs from head');
  }
  const commit = await api(`git/commits/${run.head_sha}`);
  ensure(commit.sha === run.head_sha && oid.test(commit.tree?.sha), 'missing source tree');
  // A green aggregate alone is insufficient: reject skipped or incomplete suites.
  const jobs = await api(`actions/runs/${runID}/attempts/${run.run_attempt}/jobs?per_page=100`);
  ensure(positive(jobs.total_count) && jobs.total_count <= 100 && jobs.jobs?.length === jobs.total_count, 'incomplete job list');
  ensure(jobs.jobs.every(job => job.status === 'completed' && job.conclusion === 'success'), 'incomplete suite');
  const names = new Set(jobs.jobs.map(job => job.name));
  for (const name of ['tree-reuse', 'go', 'go-static', 'go-timing', 'web', 'release-check', 'e2e', 'footer-ui', 'status-help-ui', 'status-autopilot-ui', 'migration-compat', 'runner-route / route']) ensure(names.has(name), 'missing suite job');
  const shards = jobs.jobs.filter(job => /^go-test \([1-7]\)$/.test(job.name)).map(job => job.name).sort();
  ensure([4, 7].includes(shards.length) && shards.every((name, index) => name === `go-test (${index + 1})`), 'missing Go shard');
  return { schema, repository, repository_id: run.repository.id, workflow_id: workflow.id, workflow: workflowPath, run: run.id, attempt: run.run_attempt, sha: run.head_sha, tree: commit.tree.sha };
}

export async function decide({ event, killSwitch, tree, repository, readRecord, api }) {
  if (event !== 'merge_group' || killSwitch === 'off') return { reuse: 'none', reason: 'disabled or not a merge group' };
  try {
    ensure(oid.test(tree), 'invalid target tree');
    const record = await readRecord(tree);
    ensure(record?.schema === schema && record.repository === repository && record.workflow === workflowPath && record.tree === tree && oid.test(record.sha) && positive(record.run), 'invalid record');
    const verified = await verifyRun(repository, record.run, api);
    for (const key of Object.keys(verified)) ensure(record[key] === verified[key], 'record provenance mismatch');
    return { reuse: 'tree', tree, run: verified.run, reason: 'verified successful full suite' };
  } catch {
    // A missing record, timeout, revoked run, malformed or forged record all run CI.
    return { reuse: 'none', reason: 'no verified tree record' };
  }
}

export async function registry(repository, token, actor, request, write = false) {
  ensure(repositoryPattern.test(repository), 'invalid repository');
  const packageName = `${repository.toLowerCase()}-ci-green`;
  const scope = `repository:${packageName}:${write ? 'pull,push' : 'pull'}`;
  const authentication = await request(`https://ghcr.io/token?service=ghcr.io&scope=${encodeURIComponent(scope)}`, {
    headers: { Authorization: `Basic ${Buffer.from(`${actor}:${token}`).toString('base64')}` },
  });
  const bearer = authentication.json().token;
  ensure(typeof bearer === 'string' && bearer.length > 0 && bearer.length < 32768, 'invalid registry authentication');
  const headers = { Authorization: `Bearer ${bearer}` };
  const base = `https://ghcr.io/v2/${packageName}`;
  return {
    async read(tree) {
      const response = await request(`${base}/manifests/tree-${tree}`, { headers: { ...headers, Accept: manifestType } });
      const manifest = response.json();
      ensure(manifest.schemaVersion === 2 && manifest.mediaType === manifestType && manifest.artifactType === recordType, 'invalid registry manifest');
      return JSON.parse(manifest.annotations?.['cm.barta.aeon.ci.tree-green']);
    },
    async publish(record) {
      ensure(write, 'registry is read-only');
      const empty = Buffer.from('{}');
      const hash = digest(empty);
      const upload = await request(`${base}/blobs/uploads/`, { method: 'POST', headers });
      const location = new URL(upload.headers.get('location'), base);
      ensure(location.origin === 'https://ghcr.io' && location.pathname.startsWith(`/v2/${packageName}/blobs/uploads/`), 'invalid upload destination');
      location.searchParams.set('digest', hash);
      await request(location.href, { method: 'PUT', headers: { ...headers, 'Content-Type': 'application/octet-stream' }, body: empty });
      await request(`${base}/manifests/tree-${record.tree}`, { method: 'PUT', headers: { ...headers, 'Content-Type': manifestType }, body: JSON.stringify({
        schemaVersion: 2, mediaType: manifestType, artifactType: recordType,
        config: { mediaType: emptyType, digest: hash, size: empty.length }, layers: [],
        annotations: { 'org.opencontainers.image.source': `https://github.com/${repository}`, 'cm.barta.aeon.ci.tree-green': JSON.stringify(record) },
      }) });
    },
  };
}

export async function publish(record, api, store) {
  const repository = record.repository;
  // Registry first. Any partial write fails this publisher; consumers always verify.
  await store.publish(record);
  await api(`statuses/${record.sha}`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({
    state: 'success', context: 'ci/tree-green', description: `tree=${record.tree} run=${record.run}`,
    target_url: `https://github.com/${repository}/actions/runs/${record.run}`,
  }) });
  return record;
}

async function main(mode, vars) {
  ensure(['check', 'publish'].includes(mode), 'usage: ci-tree-reuse.mjs check|publish');
  const { GITHUB_REPOSITORY: repository, GH_TOKEN: token, GITHUB_ACTOR: actor, CI_TREE_REUSE: killSwitch } = vars;
  if (mode === 'check') {
    let decision = { reuse: 'none', reason: 'disabled or not a merge group' };
    if (vars.GITHUB_EVENT_NAME === 'merge_group' && killSwitch !== 'off') {
      try {
        const tree = execFileSync('git', ['rev-parse', 'HEAD^{tree}'], { timeout: 2000, maxBuffer: 128, encoding: 'utf8' }).trim();
        const request = transport(token, actor, AbortSignal.timeout(10000));
        const api = github(repository, request);
        const store = await registry(repository, token, actor, request);
        decision = await decide({ event: vars.GITHUB_EVENT_NAME, killSwitch, tree, repository, readRecord: store.read, api });
      } catch { decision = { reuse: 'none', reason: 'tree lookup unavailable' }; }
    }
    if (vars.GITHUB_OUTPUT) appendFileSync(vars.GITHUB_OUTPUT, `reuse=${decision.reuse}\n`);
    console.log(`reuse=${decision.reuse}; ${decision.reason}${decision.run ? `; tree=${decision.tree} run=${decision.run}` : ''}`);
    return;
  }
  ensure(vars.GITHUB_EVENT_NAME === 'workflow_run', 'publishing requires workflow completion');
  if (killSwitch === 'off') { console.log('Tree record publication disabled'); return; }
  // The publisher executes only the default branch; source commits are API data.
  const file = openSync(vars.GITHUB_EVENT_PATH, 'r');
  const buffer = Buffer.alloc(2 * 1024 * 1024 + 1);
  let length = 0;
  try {
    while (length < buffer.length) {
      const count = readSync(file, buffer, length, buffer.length - length, null);
      if (!count) break;
      length += count;
    }
  } finally { closeSync(file); }
  ensure(length < buffer.length, 'event too large');
  const bytes = buffer.subarray(0, length);
  const event = JSON.parse(bytes.toString('utf8'));
  ensure(event.action === 'completed' && event.workflow_run?.conclusion === 'success', 'unsuccessful completion');
  const request = transport(token, actor, AbortSignal.timeout(45000));
  const api = github(repository, request);
  // Diverged/fork PRs are ineligible, not failed writes. Establish eligibility first.
  let record;
  try { record = await verifyRun(repository, event.workflow_run.id, api); }
  catch { console.log('No eligible successful full-suite tree to publish'); return; }
  const store = await registry(repository, token, actor, request, true);
  await publish(record, api, store);
  console.log(`Recorded tree=${record.tree} run=${record.run}`);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main(process.argv[2], process.env).catch(() => { console.error('Tree record publication failed; no complete record publication claimed'); process.exitCode = 1; });
}
