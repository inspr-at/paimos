// SPDX-License-Identifier: AGPL-3.0-only
// Runtime closure cache: key, refresh age, restore decision, evidence and save.
// The closure is stored as an ordinary image under a tag derived from the
// recipe and its refresh key, so a changed recipe or a bumped key can only
// miss and prepare cold. A miss is always safe: whatever gets prepared is bound
// by digest, smoked and proven before it ships.
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { command, root, sha256 } from './build-image-inputs.mjs';
import { digest, inspectRaw, runtimeClosure, runtimeManifest } from './image-evidence.mjs';

export const repository = 'ghcr.io/inspr-at/aeon';
export const recipe = 'scripts/Dockerfile.runtime';
export const restoreRecipe = 'scripts/Dockerfile.runtime-cache';
export const refreshLimitDays = 35;
const cacheRefPattern = /^ghcr\.io\/inspr-at\/aeon:buildcache(-arm64)?-runtime-\d{4}-\d{2}-\d{2}-[a-f0-9]{16}$/;
const day = 24 * 60 * 60 * 1000;

function platformArch(platform) {
  const match = /^linux\/(amd64|arm64)$/.exec(platform ?? '');
  if (!match) throw new Error('invalid OCI platform');
  return match[1];
}
export function refreshKey(text) {
  const keys = [...text.matchAll(/^ARG RUNTIME_REFRESH=(.*)$/gm)].map(match => match[1]);
  if (keys.length !== 1) throw new Error('runtime recipe needs exactly one ARG RUNTIME_REFRESH=<YYYY-MM-DD>');
  const time = /^\d{4}-\d{2}-\d{2}$/.test(keys[0]) ? Date.parse(`${keys[0]}T00:00:00Z`) : NaN;
  if (!Number.isFinite(time) || new Date(time).toISOString().slice(0, 10) !== keys[0]) throw new Error('RUNTIME_REFRESH must be a real calendar date, YYYY-MM-DD');
  return keys[0];
}
// Whole UTC days since the key. Staleness warns; it never fails a release.
export function refreshAge(key, now) {
  const days = Math.floor((now - Date.parse(`${key}T00:00:00Z`)) / day);
  if (!Number.isFinite(days)) throw new Error('invalid refresh clock');
  return { days, stale: days > refreshLimitDays, future: days < 0 };
}
// The cache name selects the architecture; the tag can never be a release
// coordinate, so saving a closure cannot publish or replace a release.
export function cacheRef(recipeBytes, cacheName) {
  if (!['buildcache', 'buildcache-arm64'].includes(cacheName)) throw new Error('unknown runtime cache name');
  const ref = `${repository}:${cacheName}-runtime-${refreshKey(recipeBytes.toString('utf8'))}-${sha256(recipeBytes).slice(0, 16)}`;
  if (!cacheRefPattern.test(ref)) throw new Error('invalid runtime cache reference');
  return ref;
}
function expectedCache(platform, cacheName) {
  if ((platformArch(platform) === 'arm64') !== (cacheName === 'buildcache-arm64')) throw new Error('runtime cache name does not match the platform');
}
// Any failure to read the cached closure is a miss with its reason kept.
export function probe(ref, inspect = inspectRaw) {
  let raw;
  try { raw = inspect(ref); } catch (error) { return { hit: false, reason: error.message }; }
  try {
    const value = JSON.parse(raw.toString('utf8'));
    if (value?.schemaVersion !== 2 || !(Array.isArray(value.manifests) || (value.config && Array.isArray(value.layers)))) throw new Error('unexpected document');
  } catch { return { hit: false, reason: 'cached reference is not an image manifest' }; }
  return { hit: true, manifest_sha256: sha256(raw) };
}
export function resolveCache(platform, cacheName, { cwd = root, now = Date.now(), inspect = inspectRaw } = {}) {
  expectedCache(platform, cacheName);
  const bytes = readFileSync(join(cwd, recipe));
  const refresh = refreshKey(bytes.toString('utf8'));
  const ref = cacheRef(bytes, cacheName);
  const found = probe(ref, inspect);
  return {
    platform, ref, refresh, ...refreshAge(refresh, now), recipe_sha256: sha256(bytes),
    hit: found.hit, manifest_sha256: found.manifest_sha256 ?? null, reason: found.reason ?? null,
    file: found.hit ? restoreRecipe : recipe,
    build_contexts: found.hit ? `aeon-runtime-closure=docker-image://${ref}` : '',
  };
}
export function refreshWarning(state) {
  if (state.future) return `Runtime refresh key ${state.refresh} is in the future; correct ARG RUNTIME_REFRESH in ${recipe}.`;
  if (state.stale) return `Runtime refresh key ${state.refresh} is ${state.days} days old (limit ${refreshLimitDays}): Alpine and Chromium fixes are not reaching releases. Follow "Runtime refresh" in docs/RELEASE.md.`;
  return null;
}
// What a release's runtime consists of, and where this run got it from.
export function runtimeEvidence(layout, platform, values = process.env, cwd = root) {
  const bytes = readFileSync(join(cwd, recipe));
  const hit = { true: true, false: false }[values.RUNTIME_CACHE_HIT] ?? null;
  const ref = values.RUNTIME_CACHE_REF || null, manifest = values.RUNTIME_CACHE_MANIFEST || null;
  if (ref && !cacheRefPattern.test(ref)) throw new Error('invalid runtime cache reference');
  if (manifest && !/^[a-f0-9]{64}$/.test(manifest)) throw new Error('invalid runtime cache manifest hash');
  return {
    platform, ...runtimeClosure(layout, platform), recipe_sha256: sha256(bytes), refresh: refreshKey(bytes.toString('utf8')),
    source: hit === true ? 'restored' : hit === false ? 'cold preparation' : 'unknown',
    cache: { ref, hit, manifest_sha256: manifest },
  };
}
export function runtimeSummary(evidence) {
  const source = evidence.source === 'restored'
    ? `restored from \`${evidence.cache.ref}\` (manifest sha256 \`${evidence.cache.manifest_sha256 ?? 'unknown'}\`)`
    : evidence.source === 'cold preparation' ? 'cold preparation; APK repositories were resolved in this run' : 'unknown';
  return ['', `### Runtime closure (${evidence.platform})`, '',
    `- runtime manifest: \`${evidence.digest}\``, `- config: \`${evidence.config_digest}\``,
    `- layer diff IDs: ${evidence.diff_ids.map(id => `\`${id}\``).join(', ')}`,
    `- recipe \`${recipe}\` sha256 \`${evidence.recipe_sha256}\`, refresh key \`${evidence.refresh}\``,
    `- source: ${source}`, ''].join('\n');
}
// Re-wrap the verified layout as an image. Nothing is rebuilt: the only input
// is the layout at the digest that was smoked, proven and shipped.
export function saveArgs(ref, layout, runtimeDigest, platform, context, metadata, cwd = root) {
  if (!cacheRefPattern.test(ref)) throw new Error('invalid runtime cache reference');
  platformArch(platform);
  return ['buildx', 'build', '--platform', platform, '--provenance=false',
    '--build-context', `aeon-runtime-closure=oci-layout://${resolve(layout)}@${digest(runtimeDigest)}`,
    '--build-arg', 'SOURCE_DATE_EPOCH=0', '--file', join(cwd, restoreRecipe), '--metadata-file', metadata,
    '--output', `type=image,name=${ref},push=true,rewrite-timestamp=true,oci-mediatypes=true`, context];
}
export function save(layout, platform, cacheName, values = process.env, { cwd = root, run = command, inspect = inspectRaw } = {}) {
  expectedCache(platform, cacheName);
  const ref = cacheRef(readFileSync(join(cwd, recipe)), cacheName);
  if (values.RUNTIME_CACHE_REF !== ref) throw new Error('runtime recipe changed after the cache reference was resolved');
  const expected = digest(values.RUNTIME_DIGEST);
  if (values.RUNTIME_CACHE_HIT === 'true') return { status: 'reused', ref, digest: expected };
  if (values.RUNTIME_CACHE_HIT !== 'false') throw new Error('unknown runtime cache state');
  // Blob hashes of the index, manifest and config are rechecked by this walk.
  if (runtimeManifest(layout, platform).digest !== expected) throw new Error('runtime layout differs from the verified closure');
  // Never replace a closure another run already saved under this key.
  if (probe(ref, inspect).hit) return { status: 'exists', ref, digest: expected };
  const work = mkdtempSync(join(values.RUNNER_TEMP || tmpdir(), 'runtime-save-'));
  const metadata = join(work, 'metadata.json'), context = join(work, 'context');
  mkdirSync(context);
  run('docker', saveArgs(ref, layout, expected, platform, context, metadata, cwd), cwd);
  let pushed = null;
  try { pushed = digest(JSON.parse(readFileSync(metadata, 'utf8'))['containerimage.digest']); } catch { /* Unknown, never invented. */ }
  return { status: 'saved', ref, digest: expected, pushed };
}
function append(path, text) { if (path) writeFileSync(path, text, { flag: 'a' }); }
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const [mode, ...args] = process.argv.slice(2), env = process.env;
  try {
    if (mode === 'resolve' && args.length === 2) {
      const state = resolveCache(args[0], args[1]);
      append(env.GITHUB_OUTPUT, ['ref', 'hit', 'file', 'build_contexts', 'refresh'].map(key => `${key}=${state[key]}\n`).join('') + `manifest_sha256=${state.manifest_sha256 ?? ''}\n`);
      const warning = refreshWarning(state);
      if (warning) console.log(`::warning::${warning}`);
      append(env.GITHUB_STEP_SUMMARY, `\nRuntime closure cache (${state.platform}): \`${state.ref}\` ${state.hit ? 'found; restoring it' : `not used (${state.reason}); preparing cold`}. Refresh key \`${state.refresh}\`, ${state.days} days old.${warning ? ` **${warning}**` : ''}\n`);
      console.log(JSON.stringify(state));
    } else if (mode === 'bind' && args.length === 2) {
      const evidence = runtimeEvidence(args[0], args[1]);
      append(env.GITHUB_OUTPUT, `digest=${evidence.digest}\nruntime_${platformArch(args[1])}=${evidence.digest}\n`);
      append(env.GITHUB_STEP_SUMMARY, runtimeSummary(evidence));
      console.log(JSON.stringify(evidence));
    } else if (mode === 'save' && args.length === 3) {
      try {
        const result = save(...args);
        const line = result.status === 'saved'
          ? `Runtime closure cache saved: \`${result.ref}\` from verified layout \`${result.digest}\` (pushed manifest \`${result.pushed ?? 'unknown'}\`).`
          : `Runtime closure cache not rewritten (${result.status === 'reused' ? 'this run restored it' : 'already saved by another run'}): \`${result.ref}\`.`;
        append(env.GITHUB_STEP_SUMMARY, `\n${line}\n`);
        console.log(JSON.stringify(result));
      } catch (error) {
        // A cache problem never blocks the release; the next run prepares cold.
        console.log(`::warning::Runtime closure cache was not saved: ${error.message}`);
        append(env.GITHUB_STEP_SUMMARY, `\nRuntime closure cache was **not saved**: ${error.message}. The release is unaffected; the next run prepares the closure cold.\n`);
        process.exitCode = 1;
      }
    } else throw new Error('usage: runtime-closure.mjs resolve PLATFORM CACHE | bind LAYOUT PLATFORM | save LAYOUT PLATFORM CACHE');
  } catch (error) { console.error(error.message); process.exitCode = 1; }
}
