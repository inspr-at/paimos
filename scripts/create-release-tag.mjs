// SPDX-License-Identifier: AGPL-3.0-only
// The coordinator's pre-tag step. Preview by default; --write creates only an
// annotated local tag after the exact-SHA rehearsal check. Never pushes.
import { spawnSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { validCalendarVersion } from './verify-release.mjs';
import { isHotfixBranch } from './check-release-rehearsal.mjs';

export async function createReleaseTag(metadata, { write = false, hotfix } = {}, dependencies = {}) {
  const git = dependencies.git ?? ((args, allowMissing = false) => {
    const result = spawnSync('git', args, { encoding: 'utf8', ...(args[0] === 'fetch' ? { timeout: 60_000, maxBuffer: 16 * 1024 * 1024 } : {}) });
    if (!result.error && result.status === 1 && allowMissing) return '';
    if (result.error || result.status !== 0) throw new Error('Git tag preparation failed');
    return result.stdout.trim();
  });
  const check = dependencies.check ?? (sha => {
    // Use the same gate as release.yml, never a separate weaker lookup.
    const result = spawnSync(process.execPath, ['scripts/check-release-rehearsal.mjs', '--sha', sha], { encoding: 'utf8' });
    if (result.error || result.status !== 0) throw new Error(`No green exact-SHA ${hotfix === undefined ? 'main' : 'hotfix'} rehearsal; tag creation refused`);
    return JSON.parse(result.stdout);
  });
  if (hotfix !== undefined && !isHotfixBranch(hotfix)) throw new Error('Expected a hotfix branch named hotfix/<seq> with at least three digits');
  const branch = hotfix ?? 'main';
  if (!validCalendarVersion(metadata.version)) throw new Error('Invalid release coordinate');
  git(['diff', '--quiet', 'HEAD']);
  const sha = git(['rev-parse', 'HEAD']);
  if (!/^[a-f0-9]{40}$/.test(sha) || git(['branch', '--show-current']) !== branch) throw new Error(`Tag creation requires the exact release commit checked out on ${branch}`);
  const tag = `v${metadata.version}`;
  if (git(['rev-parse', '--verify', '--quiet', `refs/tags/${tag}`], true)) throw new Error('Existing release tag is immutable');
  if (hotfix !== undefined) {
    if (git(['status', '--porcelain', '--untracked-files=normal'])) throw new Error('Tag creation requires a clean hotfix checkout');
    try { git(['fetch', '--no-tags', 'origin', 'refs/heads/main:refs/remotes/origin/main']); }
    catch { throw new Error('Current origin/main fetch failed; hotfix tag creation refused'); }
    try { git(['merge-base', '--is-ancestor', sha, 'refs/remotes/origin/main']); }
    catch { throw new Error('Hotfix release commit is not on origin/main; tag creation refused'); }
  }
  const receipt = await check(sha);
  if (receipt?.sha !== sha) throw new Error('Rehearsal receipt does not bind the checked-out commit');
  // Recheck HEAD after the API lookup. Use the immutable SHA as the tag target.
  if (git(['rev-parse', 'HEAD']) !== sha || git(['branch', '--show-current']) !== branch) throw new Error('Release checkout changed during the rehearsal check');
  git(['diff', '--quiet', 'HEAD']);
  if (hotfix !== undefined && git(['status', '--porcelain', '--untracked-files=normal'])) throw new Error('Tag creation requires a clean hotfix checkout');
  if (write === true) git(['tag', '-a', tag, '-m', `Release ${tag}`, sha]);
  return { mode: write === true ? 'created-local-tag' : 'preview', tag, sha, receipt };
}

export function parseReleaseTagArgs(args) {
  const options = { write: false };
  for (let i = 0; i < args.length; i++) {
    if (args[i] === '--write' && !options.write) options.write = true;
    else if (args[i] === '--hotfix' && options.hotfix === undefined && isHotfixBranch(args[i + 1])) options.hotfix = args[++i];
    else throw new Error('Usage: create-release-tag.mjs [--hotfix hotfix/<seq>] [--write]');
  }
  return options;
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  try {
    const options = parseReleaseTagArgs(process.argv.slice(2));
    for (const script of ['scripts/verify-release.mjs', 'scripts/check-own-release-notes.mjs']) {
      const result = spawnSync(process.execPath, [script, ...(script.includes('verify-release') ? ['--release'] : [])], { stdio: 'ignore' });
      if (result.error || result.status !== 0) throw new Error('Version, presentation or frozen-note validation failed; tag creation refused');
    }
    console.log(JSON.stringify(await createReleaseTag(JSON.parse(readFileSync('version.json', 'utf8')), options)));
  } catch (error) { console.error(error.message); process.exitCode = 1; }
}
