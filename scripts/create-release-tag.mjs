// SPDX-License-Identifier: AGPL-3.0-only
// The coordinator's pre-tag step. Preview by default; --write creates only an
// annotated local tag after the exact-main-SHA rehearsal check. Never pushes.
import { spawnSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { validCalendarVersion } from './verify-release.mjs';

export async function createReleaseTag(metadata, { write = false } = {}, dependencies = {}) {
  const git = dependencies.git ?? ((args, allowMissing = false) => {
    const result = spawnSync('git', args, { encoding: 'utf8' });
    if (!result.error && result.status === 1 && allowMissing) return '';
    if (result.error || result.status !== 0) throw new Error('Git tag preparation failed');
    return result.stdout.trim();
  });
  const check = dependencies.check ?? (sha => {
    // Use the same gate as release.yml, never a separate weaker lookup.
    const result = spawnSync(process.execPath, ['scripts/check-release-rehearsal.mjs', '--sha', sha], { encoding: 'utf8' });
    if (result.error || result.status !== 0) throw new Error('No green exact-SHA main rehearsal; tag creation refused');
    return JSON.parse(result.stdout);
  });
  if (!validCalendarVersion(metadata.version)) throw new Error('Invalid release coordinate');
  git(['diff', '--quiet', 'HEAD']);
  const sha = git(['rev-parse', 'HEAD']);
  if (!/^[a-f0-9]{40}$/.test(sha) || git(['branch', '--show-current']) !== 'main') throw new Error('Tag creation requires the exact release commit checked out on main');
  const tag = `v${metadata.version}`;
  if (git(['rev-parse', '--verify', '--quiet', `refs/tags/${tag}`], true)) throw new Error('Existing release tag is immutable');
  const receipt = await check(sha);
  if (receipt?.sha !== sha) throw new Error('Rehearsal receipt does not bind the checked-out commit');
  // Recheck HEAD after the API lookup. Use the immutable SHA as the tag target.
  if (git(['rev-parse', 'HEAD']) !== sha || git(['branch', '--show-current']) !== 'main') throw new Error('Release checkout changed during the rehearsal check');
  git(['diff', '--quiet', 'HEAD']);
  if (write === true) git(['tag', '-a', tag, '-m', `Release ${tag}`, sha]);
  return { mode: write === true ? 'created-local-tag' : 'preview', tag, sha, receipt };
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  try {
    const args = process.argv.slice(2);
    if (args.length > 1 || (args.length === 1 && args[0] !== '--write')) throw new Error('Usage: create-release-tag.mjs [--write]');
    for (const script of ['scripts/verify-release.mjs', 'scripts/check-own-release-notes.mjs']) {
      const result = spawnSync(process.execPath, [script, ...(script.includes('verify-release') ? ['--release'] : [])], { stdio: 'ignore' });
      if (result.error || result.status !== 0) throw new Error('Version, presentation or frozen-note validation failed; tag creation refused');
    }
    console.log(JSON.stringify(await createReleaseTag(JSON.parse(readFileSync('version.json', 'utf8')), { write: args[0] === '--write' })));
  } catch (error) { console.error(error.message); process.exitCode = 1; }
}
