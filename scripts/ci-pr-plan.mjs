// SPDX-License-Identifier: AGPL-3.0-only
import { execFileSync } from 'node:child_process';
import { appendFileSync, readFileSync, statSync } from 'node:fs';
import { pathToFileURL } from 'node:url';

const limit = 2 * 1024 * 1024;
const full = reason => ({ lane: 'full', specs: [], reason });
// The documentation allowlist lives here, including root licence notices.
export const docsPaths = [/\.md$/i, /^docs\//, /^(?:LICENSE|LICENCE|COPYING|NOTICE)(?:\.(?:txt|md|rst))?$/i];

export function validatePath(file) {
  if (typeof file !== 'string' || !file || file.length > 4096 || file.startsWith('/') ||
      /^[a-zA-Z]:/.test(file) || file.includes('\\') || /[\x00-\x1f\x7f]/.test(file) ||
      file.split('/').some(part => ['', '.', '..'].includes(part))) throw new Error('Invalid repository path');
  return file;
}

export function classifyPaths(files, deleted = []) {
  if (!files.length || files.length > 10000) return full('empty or oversized diff');
  files.forEach(validatePath);
  deleted.forEach(validatePath);
  if (files.every(file => docsPaths.some(pattern => pattern.test(file)))) {
    return { lane: 'docs-only', specs: [], reason: 'documentation allowlist' };
  }
  // A rename includes its removed source with --no-renames. Removals require full validation.
  if (!deleted.length && files.every(file => /^web\/tests\/[^/]+\.spec\.ts$/.test(file))) {
    return { lane: 'spec-only', specs: [...new Set(files)].sort(), reason: 'changed Playwright specs' };
  }
  return full('shared inputs, rename or deletion');
}

export function classifyPR(eventName, event, { git = args => execFileSync('git', args,
  { encoding: 'utf8', timeout: 15000, maxBuffer: limit }), checkout } = {}) {
  if (eventName !== 'pull_request') return full('main, merge queue and manual runs retain full coverage');
  const base = event?.pull_request?.base?.sha, head = event?.pull_request?.head?.sha;
  if (![base, head].every(sha => /^[a-f0-9]{40}$/.test(sha ?? ''))) throw new Error('Invalid PR commit binding');
  const ancestor = git(['merge-base', base, head]).trim();
  if (!/^[a-f0-9]{40}$/.test(ancestor)) throw new Error('Invalid merge base');
  for (const sha of [base, head]) git(['merge-base', '--is-ancestor', ancestor, sha]);
  if (checkout) {
    if (git(['rev-parse', 'HEAD']).trim() !== checkout) throw new Error('Checkout differs from CI commit');
    for (const sha of [base, head]) git(['merge-base', '--is-ancestor', sha, checkout]);
  }
  const range = `${base}...${head}`;
  const paths = args => {
    const output = git(args);
    if (Buffer.byteLength(output) > limit || (output && !output.endsWith('\0'))) throw new Error('Invalid or oversized diff');
    return output.split('\0').filter(Boolean);
  };
  return classifyPaths(paths(['diff', '--name-only', '--no-renames', '-z', range]),
    paths(['diff', '--name-only', '--no-renames', '--diff-filter=D', '-z', range]));
}

export function requireResults(lane, classifier, results) {
  if (classifier !== 'success' || !['full', 'docs-only', 'spec-only'].includes(lane)) throw new Error('CI classification failed');
  const expected = lane === 'full' ? 'success' : 'skipped';
  for (const result of results) if (result !== expected) throw new Error(`Expected ${expected} CI dependency, got ${result}`);
  return lane === 'full' ? 'Full CI passed' : `${lane}: heavy checks intentionally skipped`;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    if (process.argv[2] === 'gate') {
      const note = requireResults(...process.argv.slice(3, 5), process.argv.slice(5));
      console.log(note);
      if (process.env.GITHUB_STEP_SUMMARY) appendFileSync(process.env.GITHUB_STEP_SUMMARY, `${note}\n`);
    } else if (process.argv.length === 2) {
      const name = process.env.GITHUB_EVENT_NAME;
      let plan;
      try {
        let event = {};
        if (name === 'pull_request') {
          if (statSync(process.env.GITHUB_EVENT_PATH).size > limit) throw new Error('PR event too large');
          event = JSON.parse(readFileSync(process.env.GITHUB_EVENT_PATH, 'utf8'));
        }
        plan = classifyPR(name, event, { checkout: process.env.GITHUB_SHA });
      } catch { plan = full('classification unavailable; full validation required'); }
      console.log(JSON.stringify(plan));
      if (process.env.GITHUB_OUTPUT) appendFileSync(process.env.GITHUB_OUTPUT, `lane=${plan.lane}\nspecs=${JSON.stringify(plan.specs)}\n`);
      if (process.env.GITHUB_STEP_SUMMARY) appendFileSync(process.env.GITHUB_STEP_SUMMARY, `CI: ${plan.lane}; ${plan.reason}\n`);
    } else throw new Error('Usage: ci-pr-plan.mjs [gate LANE CLASSIFIER RESULT...]');
  } catch (error) { console.error(error.message); process.exitCode = 1; }
}
