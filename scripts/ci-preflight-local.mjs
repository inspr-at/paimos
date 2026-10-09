// SPDX-License-Identifier: AGPL-3.0-only
import { spawnSync } from 'node:child_process';
import { hostname } from 'node:os';
import { mkdirSync, writeFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { fixedEnvironment } from './ci-static.mjs';
import { localChecks, shaPattern } from './ci-preflight-result.mjs';

function git(root, args) {
  const result = spawnSync('git', args, { cwd: root, encoding: 'utf8', timeout: 30000, maxBuffer: 1024 * 1024 });
  if (result.error || result.status !== 0) throw new Error('preflight_git_unavailable');
  return result.stdout.trim();
}
export function boundCheckout(root, sha) {
  if (!shaPattern.test(sha ?? '') || git(root, ['rev-parse', 'HEAD']) !== sha ||
      git(root, ['status', '--porcelain', '--untracked-files=normal']) !== '') throw new Error('preflight_sha_or_tree_changed');
}
function boundBase(root, base) {
  if (git(root, ['rev-parse', 'refs/remotes/origin/main']) !== base) throw new Error('preflight_base_changed');
  git(root, ['merge-base', '--is-ancestor', base, 'HEAD']);
}
export function testEnvironment(source = process.env) {
  const database = source.AEON_TEST_DATABASE_URL;
  let url;
  try { url = new URL(database); } catch { throw new Error('preflight_test_database_required'); }
  if (!['postgres:', 'postgresql:'].includes(url.protocol) || !['127.0.0.1', 'localhost', '[::1]'].includes(url.hostname) ||
      !/^\/aeon_(?:run|preflight)_[a-f0-9]{10,40}$/.test(url.pathname)) throw new Error('preflight_isolated_test_database_required');
  return { ...fixedEnvironment(source), GOMAXPROCS: '4', AEON_TEST_DATABASE_URL: database };
}
// Deleted packages, global inputs, root Go or uncertain paths widen to all
// packages. Other changes run every test in each touched Go package.
export function goPackages(paths) {
  if (!Array.isArray(paths) || paths.length > 20000 || paths.some(path => typeof path !== 'string' || path.includes('\n') || path.includes('..'))) throw new Error('preflight_diff_invalid');
  if (paths.some(path => /^(?:go\.(?:mod|sum)|vendor\/|internal\/db\/migrations\/|api\/|scripts\/test-tiers\/)/.test(path) || /^[^/]+\.go$/.test(path))) return ['./...'];
  const packages = [...new Set(paths.filter(path => path.endsWith('.go')).map(path => './' + dirname(path)))].sort();
  return packages.length > 64 ? ['./...'] : packages;
}
export async function preflight({ root = process.cwd(), sha, base, runner = hostname() }, {
  bind = boundCheckout, verifyBase = boundBase,
  diff = () => git(root, ['diff', '--name-only', '--no-renames', `${base}...${sha}`]).split('\n').filter(Boolean),
  execute = (bin, args, cwd) => {
    // Without an explicit isolated test DB, dbtest would skip integration
    // behavior and an affected package could misleadingly appear green.
    const env = bin === 'go' && args[0] === 'test' ? testEnvironment() : { ...fixedEnvironment(), GOMAXPROCS: '4' };
    const result = spawnSync(bin, args, { cwd, env,
      timeout: 30 * 60 * 1000, maxBuffer: 1024 * 1024, stdio: 'ignore' });
    return !result.error && !result.signal && result.status === 0;
  },
} = {}) {
  if (!/^mbp2606(?:\..*)?$/.test(runner) || !shaPattern.test(base ?? '')) throw new Error('preflight_requires_mbp2606_and_base_sha');
  bind(root, sha);
  verifyBase(root, base);
  const packages = goPackages(diff());
  const tasks = [
    ['node', ['scripts/ci-static.mjs', '--here', '--jobs', '2'], root],
    ['node', ['scripts/test-tiers/cli.mjs', 'check', 'go', '--strict'], root],
    ['go', ['test', '-count=1', '-timeout', '20m', ...packages], root],
    ['node', ['scripts/test-tiers/cli.mjs', 'run', 'web', '--unit', '--full', '--event', 'workflow_dispatch', '--job', 'preflight-unit'], root],
    ['node', ['scripts/test-tiers/cli.mjs', 'check', 'web', '--strict'], root],
  ];
  const checks = [];
  let changed = false;
  for (let i = 0; i < tasks.length; i++) {
    let status = 'not_run';
    if (!changed) {
      try {
        bind(root, sha);
        status = i === 2 && packages.length === 0 ? 'passed' : await execute(...tasks[i]) ? 'passed' : 'failed';
        bind(root, sha);
      } catch { status = 'failed'; changed = true; }
    }
    checks.push({ id: localChecks[i], status });
  }
  return { schema: 1, kind: 'local', sha, base_sha: base, runner_class: 'mbp2606', checks,
    status: checks.every(row => row.status === 'passed') ? 'passed' : 'failed' };
}
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const [sha, base, output] = process.argv.slice(2);
    if (!output || process.argv.length !== 5) throw new Error('Usage: ci-preflight-local.mjs SHA BASE_SHA OUTPUT_JSON (on mbp2606)');
    const result = await preflight({ sha, base });
    const destination = resolve(output);
    mkdirSync(dirname(destination), { recursive: true });
    writeFileSync(destination, JSON.stringify(result) + '\n', { mode: 0o600 });
    console.log(JSON.stringify({ sha, status: result.status, checks: result.checks }));
    process.exitCode = result.status === 'passed' ? 0 : 1;
  } catch (error) { console.error(error.message); process.exitCode = 1; }
}
