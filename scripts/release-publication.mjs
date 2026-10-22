// SPDX-License-Identifier: AGPL-3.0-only
// Read-only tag-run preflight. Existing coordinates are verified, never repaired
// or republished; partial completion stays on its protected manual retry path.
import { appendFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';
import { validCalendarVersion } from './verify-release.mjs';
import { ASSETS, BINARIES, REPOSITORY, assetBytes, checksumMap, command, findRelease, images, prepare, releases } from './release-completion.mjs';

const fail = message => { throw new Error(`release publication: ${message}`); };

export function publication(env, run = command) {
  const tag = env.RELEASE_TAG;
  const version = tag?.startsWith('v') ? tag.slice(1) : '';
  if (env.GITHUB_REPOSITORY !== REPOSITORY || env.GITHUB_EVENT_NAME !== 'push' ||
      env.GITHUB_REF !== `refs/tags/${tag}` || !validCalendarVersion(version) ||
      !/^[a-f0-9]{40}$/.test(env.GITHUB_SHA ?? '')) fail('expected an exact release tag push');

  // Paginated lookups include drafts and images left by partial runs. Command
  // output and time are bounded by the shared completion command adapter.
  const existing = releases(run).filter(item => item.tag_name === tag);
  const indexes = images(run).filter(item => item.metadata.container.tags.includes(version));
  if (existing.length > 1 || indexes.length > 1) fail('ambiguous release coordinate');
  if (!existing.length && !indexes.length) return { publish: true, tag, version };
  if (!existing.length || !indexes.length) fail('partial publication; inspect the original run and use release-completion.yml with its verified digest, or reserve a new coordinate');

  // Reuse the same annotated-tag, current-main withdrawal, source, index-shape
  // and original hosted signer checks as the protected completion workflow.
  const release = prepare({ ...env, DIGEST: indexes[0].name }, run);
  const record = findRelease(release, run);
  if (!record || record.assets.length !== ASSETS.length) fail('incomplete release assets; use the protected release-completion.yml retry');
  const sources = record.body.split(/\r?\n/).filter(line => line.startsWith('Source commit: '));
  if (sources.length !== 1 || sources[0] !== `Source commit: ${release.source_sha}`) fail('existing release names a different source');
  const checksums = record.assets.find(asset => asset.name === 'SHA256SUMS');
  if (checksums.size > 64 * 1024) fail('oversized SHA256SUMS');
  if (!/^sha256:[a-f0-9]{64}$/.test(checksums.digest ?? '')) fail('existing asset checksum differs or is unavailable');
  const sums = checksumMap(assetBytes(checksums, run).toString());
  // GitHub's uploaded asset digest binds each binary without downloading the
  // entire release again. Missing digest metadata fails closed as well.
  for (const name of BINARIES) {
    if (record.assets.find(asset => asset.name === name).digest !== `sha256:${sums.get(name)}`) fail('existing asset checksum differs or is unavailable');
  }
  return { publish: false, ...release };
}

export function recordPublication(result, env) {
  if (env.GITHUB_OUTPUT) appendFileSync(env.GITHUB_OUTPUT, `publish=${result.publish}\n`);
  if (env.GITHUB_STEP_SUMMARY) appendFileSync(env.GITHUB_STEP_SUMMARY,
    result.publish ? `Release ${result.tag}: fresh coordinate; publication required.\n` :
      `Release ${result.tag}: verified existing release, checksums and attested index ${result.digest}; duplicate trigger performs no publication or completion writes.\n`);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    if (process.argv.length !== 2) fail('usage: release-publication.mjs');
    const result = publication(process.env);
    recordPublication(result, process.env);
    console.log(JSON.stringify(result));
  } catch (error) {
    console.error(error.message?.startsWith('release publication:') || error.message?.startsWith('release completion:') ? error.message : 'release publication: verification failed');
    process.exitCode = 1;
  }
}
