// SPDX-License-Identifier: AGPL-3.0-only
// Trusted completion tooling runs separately from the immutable tagged source.
import { createHash } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import { appendFileSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
import { validCalendarVersion, schemeError } from './verify-release.mjs';
import { verifyImageIndex } from './release-image-index.mjs';
import { attestationArgs } from './release-pin-pr.mjs';

export const REPOSITORY = 'inspr-at/paimos';
export const BINARIES = [
  'paimos-agentd-darwin-arm64', 'paimos-agentd-darwin-amd64',
  'paimos-agentd-linux-arm64', 'paimos-agentd-linux-amd64',
  'aeon-cli-darwin-arm64', 'aeon-cli-darwin-amd64',
  'aeon-cli-linux-amd64', 'aeon-cli-linux-arm64',
];
export const ASSETS = [...BINARIES, 'SHA256SUMS'];
const digestOK = value => /^sha256:[a-f0-9]{64}$/.test(value ?? '');
const commitOK = value => /^[a-f0-9]{40}$/.test(value ?? '');
const hash = bytes => createHash('sha256').update(bytes).digest('hex');
const fail = message => { throw new Error(`release completion: ${message}`); };

// Never print command stderr, API bodies or resolved credentials.
export function command(program, args) {
  const result = spawnSync(program, args, { encoding: null, maxBuffer: 128 * 1024 * 1024, timeout: 120_000 });
  if (result.error || result.status !== 0) fail(`${program} operation failed`);
  return result.stdout;
}
export function api(path, paginate = false, run = command) {
  return JSON.parse(run('gh', ['api', '--method', 'GET', ...(paginate ? ['--paginate', '--slurp'] : []), path]).toString());
}
export function list(path, run = command) {
  const pages = api(`${path}${path.includes('?') ? '&' : '?'}per_page=100`, true, run);
  if (!Array.isArray(pages) || pages.some(page => !Array.isArray(page))) fail('invalid paginated lookup');
  return pages.flat();
}
export function images(run = command) {
  const records = list('orgs/inspr-at/packages/container/aeon/versions', run);
  if (records.some(image => !digestOK(image?.name) || !Array.isArray(image.metadata?.container?.tags) || image.metadata.container.tags.some(tag => typeof tag !== 'string'))) fail('invalid registry lookup');
  return records;
}
export function releases(run = command) {
  const records = list(`repos/${REPOSITORY}/releases`, run);
  if (records.some(release => typeof release?.tag_name !== 'string' || typeof release.draft !== 'boolean')) fail('invalid release lookup');
  return records;
}

export function invocation(env) {
  const tag = env.RELEASE_TAG;
  const version = tag?.startsWith('v') ? tag.slice(1) : '';
  const tagPush = env.GITHUB_EVENT_NAME === 'push' && env.GITHUB_REF === `refs/tags/${tag}`;
  const mainDispatch = env.GITHUB_EVENT_NAME === 'workflow_dispatch' && env.GITHUB_REF === 'refs/heads/main';
  if (env.GITHUB_REPOSITORY !== REPOSITORY || (!tagPush && !mainDispatch) || !validCalendarVersion(version) || !digestOK(env.DIGEST)) fail('expected trusted tag push or main dispatch, exact tag and index digest');
  return { tag, version, digest: env.DIGEST };
}

export function prepare(env, run = command, { inspect = true } = {}) {
  const release = invocation(env);
  const ref = api(`repos/${REPOSITORY}/git/ref/tags/${release.tag}`, false, run);
  if (ref.ref !== `refs/tags/${release.tag}` || ref.object?.type !== 'tag' || !commitOK(ref.object.sha)) fail('tag must be annotated');
  const tag = api(`repos/${REPOSITORY}/git/tags/${ref.object.sha}`, false, run);
  if (tag.sha !== ref.object.sha || tag.tag !== release.tag || tag.object?.type !== 'commit' || !commitOK(tag.object.sha)) fail('tag must directly name a source commit');
  release.source_sha = tag.object.sha;
  if (env.GITHUB_EVENT_NAME === 'push' && env.GITHUB_SHA !== release.source_sha) fail('tag push source differs');
  const main = api(`repos/${REPOSITORY}/git/ref/heads/main`, false, run);
  if (main.ref !== 'refs/heads/main' || main.object?.type !== 'commit' || !commitOK(main.object.sha)) fail('main lookup failed');
  const comparison = api(`repos/${REPOSITORY}/compare/${release.source_sha}...${main.object.sha}`, false, run);
  if (!['ahead', 'identical'].includes(comparison.status) || comparison.base_commit?.sha !== release.source_sha || comparison.merge_base_commit?.sha !== release.source_sha) fail('tagged source is not on main');
  const policyFile = api(`repos/${REPOSITORY}/contents/version.json?ref=${main.object.sha}`, false, run);
  if (policyFile.type !== 'file' || policyFile.encoding !== 'base64' || typeof policyFile.content !== 'string') fail('current reservation policy missing');
  const policy = JSON.parse(Buffer.from(policyFile.content, 'base64'));
  if (policy.unpublished_reservations !== undefined && !Array.isArray(policy.unpublished_reservations)) fail('invalid retired reservation policy');
  if (policy.unpublished_reservations?.includes(release.version)) fail('coordinate was retired; completion is forbidden');
  const metadata = api(`repos/${REPOSITORY}/contents/version.json?ref=${release.source_sha}`, false, run);
  if (metadata.type !== 'file' || metadata.encoding !== 'base64' || typeof metadata.content !== 'string') fail('tagged version source missing');
  const version = JSON.parse(Buffer.from(metadata.content, 'base64'));
  if (version.version !== release.version || version.product !== 'PAIMOS AEON' || schemeError(version.version_scheme, version.version)) fail('tag does not match version.json');
  // The live coordinate resolves to precisely the coordinator-supplied digest.
  const matches = images(run).filter(image => image.metadata.container.tags.includes(release.version));
  if (matches.length !== 1 || matches[0].name !== release.digest) fail('published index digest changed or is absent');
  if (inspect) verifyImageIndex(JSON.parse(run('docker', ['buildx', 'imagetools', 'inspect', '--raw', `ghcr.io/inspr-at/aeon@${release.digest}`])));
  run('gh', attestationArgs(release.version, release.digest, release.source_sha));
  return release;
}

export function findRelease(release, run = command) {
  const matches = releases(run).filter(item => item.tag_name === release.tag);
  if (matches.length > 1) fail('ambiguous release lookup');
  const existing = matches[0];
  if (!existing) return null;
  if (!Number.isSafeInteger(existing.id) || existing.id <= 0 || existing.prerelease !== false || typeof existing.draft !== 'boolean' || typeof existing.body !== 'string') fail('invalid existing release');
  const digests = existing.body.split(/\r?\n/).filter(line => line.startsWith('Digest: '));
  if (digests.length !== 1 || digests[0] !== `Digest: ${release.digest}`) fail('existing release names a different index');
  // Read all assets explicitly; embedded release.assets can be incomplete.
  existing.assets = list(`repos/${REPOSITORY}/releases/${existing.id}/assets`, run);
  const seen = new Set();
  for (const asset of existing.assets) {
    if (!ASSETS.includes(asset.name) || seen.has(asset.name) || !Number.isSafeInteger(asset.id) || asset.id <= 0 || asset.state !== 'uploaded' || !Number.isSafeInteger(asset.size) || asset.size <= 0) fail('unexpected, duplicate or unfinished asset');
    seen.add(asset.name);
  }
  return existing;
}

export function assetBytes(asset, run = command) {
  const bytes = run('gh', ['api', '--method', 'GET', '-H', 'Accept: application/octet-stream', `repos/${REPOSITORY}/releases/assets/${asset.id}`]);
  if (bytes.length !== asset.size || (asset.digest != null && asset.digest !== `sha256:${hash(bytes)}`)) fail('existing asset integrity failed');
  return bytes;
}

export function restore(release, name, directory, run = command) {
  if (!BINARIES.includes(name)) fail('invalid binary name');
  const existing = findRelease(release, run);
  const asset = existing?.assets.find(item => item.name === name);
  if (!asset) {
    if (existing && !existing.draft) fail('cannot repair a published release');
    return false;
  }
  mkdirSync(directory, { recursive: true });
  writeFileSync(join(directory, name), assetBytes(asset, run), { mode: 0o755 });
  return true;
}

export function checksumMap(text) {
  const sums = new Map();
  for (const line of text.trimEnd().split('\n')) {
    const match = /^([a-f0-9]{64})  (\S+)$/.exec(line);
    if (!match || !BINARIES.includes(match[2]) || sums.has(match[2])) fail('invalid SHA256SUMS');
    sums.set(match[2], match[1]);
  }
  if (sums.size !== BINARIES.length) fail('incomplete SHA256SUMS');
  return sums;
}

export function complete(release, directory, { write = false, pinEvidence = '' } = {}, run = command) {
  let existing = findRelease(release, run);
  const bytes = new Map();
  // Preserve original uploaded bytes, including nondeterministic Apple signatures.
  for (const asset of existing?.assets ?? []) bytes.set(asset.name, assetBytes(asset, run));
  const missing = ASSETS.filter(name => !bytes.has(name));
  if (existing && !existing.draft && missing.length) fail('cannot add assets to a published release');
  for (const name of BINARIES) if (!bytes.has(name)) bytes.set(name, readFileSync(join(directory, name)));
  if (!bytes.has('SHA256SUMS')) bytes.set('SHA256SUMS', Buffer.from(BINARIES.map(name => `${hash(bytes.get(name))}  ${name}\n`).join('')));
  const sums = checksumMap(bytes.get('SHA256SUMS').toString());
  for (const name of BINARIES) {
    if (hash(bytes.get(name)) !== sums.get(name)) fail(`immutable checksum mismatch for ${name}; recover original bytes or reserve a new coordinate`);
  }
  if (!write) return { missing, status: missing.length ? 'planned' : 'complete' };
  // No edits to release metadata; no clobber/delete/upload of existing assets.
  if (!existing) {
    const notes = join(directory, 'completion-notes.txt');
    writeFileSync(notes, [
      `Signed/notarized Darwin paimos-agentd and static Linux paimos-agentd / aeon-cli ${release.version}.`,
      'Verify SHA256SUMS before installing. Native qualification and publication remain coordinator gates.',
      `Container: ghcr.io/inspr-at/aeon:${release.version}`, `Digest: ${release.digest}`,
      `Source commit: ${release.source_sha}`, '', pinEvidence,
    ].join('\n'));
    run('gh', ['release', 'create', release.tag, '--repo', REPOSITORY, '--draft', '--verify-tag', '--target', release.source_sha, '--title', release.tag, '--notes-file', notes]);
    existing = findRelease(release, run);
    if (!existing?.draft) fail('draft creation did not produce a draft');
  }
  for (const name of missing) {
    // Recheck before each mutation. A race fails closed; gh upload has no --clobber.
    const current = findRelease(release, run);
    if (!current?.draft || current.id !== existing.id || current.assets.some(asset => asset.name === name)) fail('release changed during completion; retry after inspection');
    writeFileSync(join(directory, name), bytes.get(name), { mode: name === 'SHA256SUMS' ? 0o644 : 0o755 });
    run('gh', ['release', 'upload', release.tag, join(directory, name), '--repo', REPOSITORY]);
  }
  // Verify persisted bytes, not just upload exit codes.
  const current = findRelease(release, run);
  if (!current || current.id !== existing.id || current.assets.length !== ASSETS.length) fail('release is still incomplete');
  for (const asset of current.assets) if (!assetBytes(asset, run).equals(bytes.get(asset.name))) fail('uploaded asset differs');
  return { missing, status: 'complete' };
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const [mode, ...args] = process.argv.slice(2);
    // The hosted prepare job checks the index structure; Mac jobs have no Docker.
    // Every operation still rechecks the tag, live digest and original attestation.
    const release = prepare(process.env, command, { inspect: mode === 'prepare' });
    if (mode === 'prepare' && args.length === 0) {
      if (process.env.GITHUB_OUTPUT) for (const [key, value] of Object.entries(release)) appendFileSync(process.env.GITHUB_OUTPUT, `${key}=${value}\n`);
      console.log(JSON.stringify(release));
    } else if (mode === 'restore' && args.length === 2) {
      const restored = restore(release, args[0], args[1]);
      if (process.env.GITHUB_OUTPUT) appendFileSync(process.env.GITHUB_OUTPUT, `restored=${restored}\n`);
    } else if (mode === 'complete' && args.length >= 1 && args.length <= 2 && (!args[1] || args[1] === '--write')) {
      console.log(JSON.stringify(complete(release, args[0], { write: args[1] === '--write', pinEvidence: process.env.PIN_EVIDENCE ?? '' })));
    } else fail('usage: prepare | restore ASSET DIST | complete DIST [--write]');
  } catch (error) {
    console.error(error.message?.startsWith('release completion:') ? error.message : 'release completion: operation failed');
    process.exitCode = 1;
  }
}
