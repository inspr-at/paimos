// SPDX-License-Identifier: AGPL-3.0-only
// AEON-423 owns verified tree reuse; this policy applies to its queue fallback.
import { execFileSync, spawnSync } from 'node:child_process';
import { appendFileSync, readFileSync, writeFileSync, statSync } from 'node:fs';
import { pathToFileURL } from 'node:url';

export function planLane(eventName, event, enabled) {
  if (enabled !== 'on' || eventName !== 'merge_group') return { lane: 'full', base: '', reason: 'full PR/main/manual validation' };
  const group = event?.merge_group;
  if (event.action !== 'checks_requested' || group?.base_ref !== 'refs/heads/main' ||
      !/^[a-f0-9]{40}$/.test(group.base_sha ?? '') || !/^[a-f0-9]{40}$/.test(group.head_sha ?? '')) {
    throw new Error('Invalid merge-group lane binding');
  }
  return { lane: 'impacted', base: group.base_sha, head: group.head_sha, reason: 'queue fallback after tree reuse' };
}

export function currentLane(env = process.env, git = args => execFileSync('git', args, { encoding: 'utf8', timeout: 15_000, maxBuffer: 2 * 1024 * 1024 }).trim()) {
  if (env.GITHUB_EVENT_NAME === 'merge_group' && statSync(env.GITHUB_EVENT_PATH).size > 2 * 1024 * 1024) throw new Error('Queue event too large');
  const event = env.GITHUB_EVENT_NAME === 'merge_group' ? JSON.parse(readFileSync(env.GITHUB_EVENT_PATH, 'utf8')) : {};
  const plan = planLane(env.GITHUB_EVENT_NAME, event, env.CI_IMPACTED_TESTS);
  const head = git(['rev-parse', 'HEAD']);
  const tree = git(['rev-parse', 'HEAD^{tree}']);
  if (!/^[a-f0-9]{40}$/.test(head) || !/^[a-f0-9]{40}$/.test(tree)) throw new Error('Invalid checkout identity');
  if (plan.lane === 'impacted') {
    if (head !== plan.head) throw new Error('Queue checkout differs from the event');
    // Full history is required. Never query a main ref that may have moved.
    git(['merge-base', '--is-ancestor', plan.base, head]);
  }
  return { schema: 'aeon.ci.lane.v1', ...plan, sha: head, tree, event: env.GITHUB_EVENT_NAME };
}

export function playwrightArgs(command, plan, files) {
  const allowed = (command[0] === 'npx' && command[1] === 'playwright' && command[2] === 'test') ||
    (command[0] === 'npm' && command[1] === 'test' && command[2] === '--') ||
    (command[0] === 'npm' && command[1] === 'run' && command[2] === 'e2e');
  if (!allowed || command.some(arg => arg.startsWith('--only-changed'))) throw new Error('Unsupported Playwright invocation');
  // Playwright's dependency graph does not see API behavior, Vite application
  // imports, build configuration or fixture removals. Those run full coverage.
  const specOnly = files.length > 0 && files.every(file => /^web\/tests\/[^/]+\.spec\.ts$/.test(file));
  const changed = plan.lane === 'impacted' && specOnly;
  const args = [...command];
  if (changed) {
    if (command[0] === 'npm' && command[1] === 'run' && !args.includes('--')) args.push('--');
    args.push(`--only-changed=${plan.base}`, '--pass-with-no-tests');
  }
  return { args, selection: changed ? 'only-changed' : 'full', reason: changed ? 'spec-only change' : 'shared inputs or full lane' };
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const args = process.argv.slice(2);
    const plan = currentLane();
    if (args[0] === 'record' && args.length === 1) {
      writeFileSync('ci-lane.json', `${JSON.stringify(plan)}\n`);
      if (process.env.GITHUB_OUTPUT) appendFileSync(process.env.GITHUB_OUTPUT, `lane=${plan.lane}\nbase=${plan.base}\n`);
      if (process.env.GITHUB_STEP_SUMMARY) appendFileSync(process.env.GITHUB_STEP_SUMMARY, `\nCI lane: ${JSON.stringify(plan)}\n`);
      console.log(JSON.stringify(plan));
    } else if (args[0] === 'playwright' && args[1] === '--') {
      const files = plan.lane === 'impacted' ? execFileSync('git', ['diff', '--name-only', '-z', plan.base, plan.sha],
        { encoding: 'utf8', timeout: 15_000, maxBuffer: 2 * 1024 * 1024 }).split('\0').filter(Boolean) : [];
      if (plan.lane === 'impacted' && execFileSync('git', ['diff', '--name-only', '--diff-filter=D', plan.base, plan.sha],
        { encoding: 'utf8', timeout: 15_000, maxBuffer: 2 * 1024 * 1024 }).trim()) files.push('deleted input');
      const selected = playwrightArgs(args.slice(2), plan, files);
      console.log(`Playwright selection: ${selected.selection}; ${selected.reason}; base=${plan.base || 'full'}`);
      const child = spawnSync(selected.args[0], selected.args.slice(1), { stdio: 'inherit' });
      if (child.error || child.signal) throw new Error('Playwright process did not complete');
      process.exitCode = child.status ?? 1;
    } else throw new Error('Usage: ci-lane.mjs record | playwright -- COMMAND');
  } catch { console.error('CI lane selection or execution failed'); process.exitCode = 1; }
}
