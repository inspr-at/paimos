// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import { sha256 } from './build-image-inputs.mjs';
import { cacheRef, probe, recipe, refreshAge, refreshKey, refreshLimitDays, refreshWarning, resolveCache, restoreRecipe, runtimeEvidence, runtimeSummary, save, saveArgs } from './runtime-closure.mjs';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const script = fileURLToPath(new URL('./runtime-closure.mjs', import.meta.url));
const base = `FROM alpine:3.24@sha256:${'f'.repeat(64)}\n`;
const recipeText = (key = '2026-10-05', run = 'RUN apk add --no-cache tini') => `${base}ARG RUNTIME_REFRESH=${key}\n${run}\n`;
const utc = value => Date.parse(`${value}T12:00:00Z`);
const manifestBytes = Buffer.from(JSON.stringify({ schemaVersion: 2, config: { digest: `sha256:${'c'.repeat(64)}` }, layers: [] }));

function checkout(text = recipeText()) {
  const path = mkdtempSync(join(tmpdir(), 'aeon-runtime-closure-test-'));
  mkdirSync(join(path, 'scripts'));
  writeFileSync(join(path, recipe), text);
  writeFileSync(join(path, restoreRecipe), 'FROM aeon-runtime-closure\n');
  return path;
}
function layout(path, { arch = 'arm64', token = 'runtime' } = {}) {
  mkdirSync(join(path, 'blobs/sha256'), { recursive: true });
  const blob = value => {
    const bytes = Buffer.from(JSON.stringify(value)), id = `sha256:${sha256(bytes)}`;
    writeFileSync(join(path, 'blobs/sha256', id.slice(7)), bytes);
    return { digest: id, size: bytes.length };
  };
  const config = blob({ architecture: arch, os: 'linux', token, rootfs: { type: 'layers', diff_ids: [`sha256:${sha256(token)}`] } });
  const manifest = blob({ config, layers: [] });
  writeFileSync(join(path, 'index.json'), JSON.stringify({ manifests: [manifest] }));
  return { digest: manifest.digest, config_digest: config.digest, diff_ids: [`sha256:${sha256(token)}`] };
}

test('the refresh key is exactly one real calendar date in the recipe', () => {
  assert.equal(refreshKey(recipeText('2026-10-05')), '2026-10-05');
  assert.equal(refreshKey(recipeText('2028-02-29')), '2028-02-29');
  for (const [name, text] of [
    ['missing', base + 'RUN apk add tini\n'],
    ['commented', base + '# ARG RUNTIME_REFRESH=2026-10-05\n'],
    ['indented', base + '  ARG RUNTIME_REFRESH=2026-10-05\n'],
    ['duplicated', recipeText() + 'ARG RUNTIME_REFRESH=2026-11-05\n'],
  ]) assert.throws(() => refreshKey(text), /exactly one ARG RUNTIME_REFRESH/, name);
  for (const key of ['', '2026-10-5', '2026-13-01', '2026-02-30', '2027-02-29', '20261005', '2026-10-05 ', '"2026-10-05"', 'latest']) {
    assert.throws(() => refreshKey(recipeText(key)), /real calendar date/, JSON.stringify(key));
  }
});

test('the committed recipes carry a usable key, use it, and the re-wrap adds nothing', () => {
  const text = readFileSync(join(root, recipe), 'utf8');
  const key = refreshKey(text);
  // A key from the future would silence the staleness warning.
  assert.equal(refreshAge(key, Date.now() + 24 * 60 * 60 * 1000).future, false);
  const instructions = text.replaceAll('\\\n', ' ').split('\n').filter(line => line && !line.startsWith('#'));
  assert.equal(instructions.length, 3);
  assert.match(instructions[0], /^FROM alpine:3\.24@sha256:[a-f0-9]{64}$/);
  assert.equal(instructions[1], `ARG RUNTIME_REFRESH=${key}`);
  // The RUN that resolves APK packages reads the key, so a bump also misses any local layer cache.
  assert.match(instructions[2], /^RUN apk add .*"\$RUNTIME_REFRESH"/);
  const rewrap = readFileSync(join(root, restoreRecipe), 'utf8').split('\n').filter(line => line && !line.startsWith('#'));
  assert.deepEqual(rewrap, ['FROM aeon-runtime-closure']);
});

test('staleness is counted in whole UTC days from an injected clock', () => {
  assert.deepEqual(refreshAge('2026-10-05', utc('2026-10-05')), { days: 0, stale: false, future: false });
  assert.deepEqual(refreshAge('2026-10-05', Date.parse('2026-11-09T23:59:59Z')), { days: refreshLimitDays, stale: false, future: false });
  assert.deepEqual(refreshAge('2026-10-05', Date.parse('2026-11-10T00:00:00Z')), { days: 36, stale: true, future: false });
  assert.deepEqual(refreshAge('2026-10-05', Date.parse('2026-10-04T23:59:59Z')), { days: -1, stale: false, future: true });
  assert.throws(() => refreshAge('2026-10-05', NaN), /clock/);
  assert.equal(refreshLimitDays, 35);
});

test('the cache reference follows the recipe bytes, the refresh key and the architecture', () => {
  const bytes = Buffer.from(recipeText());
  const ref = cacheRef(bytes, 'buildcache');
  assert.equal(ref, `ghcr.io/inspr-at/aeon:buildcache-runtime-2026-10-05-${sha256(bytes).slice(0, 16)}`);
  assert.equal(cacheRef(bytes, 'buildcache'), ref);
  assert.equal(cacheRef(bytes, 'buildcache-arm64'), ref.replace('buildcache-', 'buildcache-arm64-'));
  const others = [recipeText('2026-10-06'), recipeText('2026-10-05', 'RUN apk add --no-cache tini chromium'), recipeText().replace('f'.repeat(64), 'e'.repeat(64)), recipeText() + '# note\n']
    .map(text => cacheRef(Buffer.from(text), 'buildcache'));
  assert.equal(new Set([ref, ...others]).size, 5, 'every recipe change needs its own closure');
  for (const name of ['', 'buildcache-runtime', '261005120000.0.0', 'latest', 'buildcache:x', undefined]) assert.throws(() => cacheRef(bytes, name), /unknown runtime cache name/);
  // The saved closure can never land on a release coordinate.
  assert.doesNotMatch(ref.split(':')[1], /^\d{12}\.\d+\.\d+$/);
});

test('a readable saved closure is restored; anything else prepares cold and keeps the reason', () => {
  const cwd = checkout(), calls = [];
  const expected = cacheRef(Buffer.from(recipeText()), 'buildcache-arm64');
  const hit = resolveCache('linux/arm64', 'buildcache-arm64', { cwd, now: utc('2026-10-20'), inspect: ref => { calls.push(ref); return manifestBytes; } });
  assert.deepEqual(calls, [expected]);
  assert.deepEqual(hit, { platform: 'linux/arm64', ref: expected, refresh: '2026-10-05', days: 15, stale: false, future: false,
    recipe_sha256: sha256(recipeText()), hit: true, manifest_sha256: sha256(manifestBytes), reason: null,
    file: restoreRecipe, build_contexts: `aeon-runtime-closure=docker-image://${expected}` });
  assert.equal(refreshWarning(hit), null);
  const index = Buffer.from(JSON.stringify({ schemaVersion: 2, manifests: [] }));
  assert.equal(resolveCache('linux/arm64', 'buildcache-arm64', { cwd, now: utc('2026-10-20'), inspect: () => index }).hit, true);
  for (const [name, inspect, reason] of [
    ['absent', () => { throw new Error('cannot read ref: not found'); }, 'cannot read ref: not found'],
    ['not JSON', () => Buffer.from('<html>'), 'cached reference is not an image manifest'],
    ['JSON null', () => Buffer.from('null'), 'cached reference is not an image manifest'],
    ['other document', () => Buffer.from('{"schemaVersion":2,"errors":[]}'), 'cached reference is not an image manifest'],
    ['old schema', () => Buffer.from('{"schemaVersion":1,"manifests":[]}'), 'cached reference is not an image manifest'],
  ]) {
    const miss = resolveCache('linux/amd64', 'buildcache', { cwd, now: utc('2026-10-20'), inspect });
    assert.deepEqual([miss.hit, miss.reason, miss.manifest_sha256, miss.file, miss.build_contexts], [false, reason, null, recipe, ''], name);
  }
  assert.deepEqual(probe(expected, () => manifestBytes), { hit: true, manifest_sha256: sha256(manifestBytes) });
  for (const [platform, name] of [['linux/arm64', 'buildcache'], ['linux/amd64', 'buildcache-arm64']]) {
    assert.throws(() => resolveCache(platform, name, { cwd, inspect: () => manifestBytes }), /does not match the platform/);
  }
  assert.throws(() => resolveCache('linux/riscv64', 'buildcache', { cwd, inspect: () => manifestBytes }), /invalid OCI platform/);
  assert.throws(() => resolveCache('linux/amd64', 'buildcache', { cwd: checkout(base), inspect: () => manifestBytes }), /exactly one ARG RUNTIME_REFRESH/);
});

test('an old or future refresh key produces a warning text and still resolves', () => {
  const cwd = checkout();
  const stale = resolveCache('linux/amd64', 'buildcache', { cwd, now: utc('2026-11-10'), inspect: () => manifestBytes });
  assert.deepEqual([stale.days, stale.stale, stale.hit], [36, true, true]);
  assert.match(refreshWarning(stale), /^Runtime refresh key 2026-10-05 is 36 days old \(limit 35\).*docs\/RELEASE\.md/);
  assert.equal(refreshWarning(resolveCache('linux/amd64', 'buildcache', { cwd, now: utc('2026-11-09'), inspect: () => manifestBytes })), null);
  assert.match(refreshWarning(resolveCache('linux/amd64', 'buildcache', { cwd, now: utc('2026-10-01'), inspect: () => manifestBytes })), /is in the future/);
});

test('runtime evidence names the bytes, the recipe, the key and where the closure came from', () => {
  const cwd = checkout(), path = join(cwd, 'runtime'), closure = layout(path);
  const ref = cacheRef(Buffer.from(recipeText()), 'buildcache-arm64');
  const identity = { platform: 'linux/arm64', ...closure, recipe_sha256: sha256(recipeText()), refresh: '2026-10-05' };
  const restored = runtimeEvidence(path, 'linux/arm64', { RUNTIME_CACHE_REF: ref, RUNTIME_CACHE_HIT: 'true', RUNTIME_CACHE_MANIFEST: 'a'.repeat(64) }, cwd);
  assert.deepEqual(restored, { ...identity, source: 'restored', cache: { ref, hit: true, manifest_sha256: 'a'.repeat(64) } });
  const cold = runtimeEvidence(path, 'linux/arm64', { RUNTIME_CACHE_REF: ref, RUNTIME_CACHE_HIT: 'false', RUNTIME_CACHE_MANIFEST: '' }, cwd);
  assert.deepEqual(cold, { ...identity, source: 'cold preparation', cache: { ref, hit: false, manifest_sha256: null } });
  // Outside the workflows nothing is known about a cache, and nothing is claimed.
  assert.deepEqual(runtimeEvidence(path, 'linux/arm64', {}, cwd), { ...identity, source: 'unknown', cache: { ref: null, hit: null, manifest_sha256: null } });
  assert.throws(() => runtimeEvidence(path, 'linux/arm64', { RUNTIME_CACHE_REF: 'ghcr.io/inspr-at/aeon:261005120000.0.0' }, cwd), /invalid runtime cache reference/);
  assert.throws(() => runtimeEvidence(path, 'linux/arm64', { RUNTIME_CACHE_MANIFEST: 'sha256:' + 'a'.repeat(64) }, cwd), /invalid runtime cache manifest hash/);
  assert.throws(() => runtimeEvidence(path, 'linux/amd64', {}, cwd), /exactly one runtime platform manifest/);
  for (const [evidence, source] of [[restored, `restored from \`${ref}\` (manifest sha256 \`${'a'.repeat(64)}\`)`], [cold, 'cold preparation; APK repositories were resolved in this run']]) {
    const summary = runtimeSummary(evidence);
    for (const fragment of ['### Runtime closure (linux/arm64)', `runtime manifest: \`${closure.digest}\``, `config: \`${closure.config_digest}\``, closure.diff_ids[0], 'refresh key `2026-10-05`', `source: ${source}`]) {
      assert.ok(summary.includes(fragment), fragment);
    }
  }
});

test('saving pushes exactly the verified layout and never rebuilds the recipe', () => {
  const ref = cacheRef(Buffer.from(recipeText()), 'buildcache');
  const id = `sha256:${'d'.repeat(64)}`;
  assert.deepEqual(saveArgs(ref, '/tmp/aeon-runtime', id, 'linux/amd64', '/tmp/context', '/tmp/metadata.json', '/repo'), ['buildx', 'build', '--platform', 'linux/amd64', '--provenance=false',
    '--build-context', `aeon-runtime-closure=oci-layout:///tmp/aeon-runtime@${id}`, '--build-arg', 'SOURCE_DATE_EPOCH=0',
    '--file', `/repo/${restoreRecipe}`, '--metadata-file', '/tmp/metadata.json',
    '--output', `type=image,name=${ref},push=true,rewrite-timestamp=true,oci-mediatypes=true`, '/tmp/context']);
  for (const release of ['ghcr.io/inspr-at/aeon:261005120000.0.0', 'ghcr.io/inspr-at/aeon:latest', 'ghcr.io/inspr-at/aeon', 'ghcr.io/other/aeon:buildcache-runtime-2026-10-05-' + 'a'.repeat(16), ref + ',push-by-digest=true']) {
    assert.throws(() => saveArgs(release, '/tmp/aeon-runtime', id, 'linux/amd64', '/tmp/context', '/tmp/metadata.json'), /invalid runtime cache reference/);
  }
  assert.throws(() => saveArgs(ref, '/tmp/aeon-runtime', 'sha256:short', 'linux/amd64', '/tmp/context', '/tmp/metadata.json'), /invalid OCI digest/);
});

function saveFixture({ pushed = `sha256:${'e'.repeat(64)}`, present = false } = {}) {
  const cwd = checkout(), path = join(cwd, 'runtime'), closure = layout(path, { arch: 'amd64' });
  const ref = cacheRef(Buffer.from(recipeText()), 'buildcache');
  const calls = [], probes = [];
  const run = (file, args) => {
    calls.push({ file, args });
    if (pushed) writeFileSync(args[args.indexOf('--metadata-file') + 1], JSON.stringify({ 'containerimage.digest': pushed }));
  };
  const inspect = target => { probes.push(target); if (present) return manifestBytes; throw new Error('not found'); };
  const values = { RUNNER_TEMP: mkdtempSync(join(tmpdir(), 'aeon-runtime-save-test-')), RUNTIME_DIGEST: closure.digest, RUNTIME_CACHE_REF: ref, RUNTIME_CACHE_HIT: 'false' };
  return { cwd, path, closure, ref, calls, probes, values, options: { cwd, run, inspect } };
}

test('a cold, verified closure is saved once from its layout', () => {
  const f = saveFixture();
  assert.deepEqual(save(f.path, 'linux/amd64', 'buildcache', f.values, f.options), { status: 'saved', ref: f.ref, digest: f.closure.digest, pushed: `sha256:${'e'.repeat(64)}` });
  assert.deepEqual(f.probes, [f.ref]);
  assert.equal(f.calls.length, 1);
  const args = f.calls[0].args, context = args.at(-1);
  assert.equal(f.calls[0].file, 'docker');
  assert.deepEqual(args, saveArgs(f.ref, f.path, f.closure.digest, 'linux/amd64', context, args[args.indexOf('--metadata-file') + 1], f.cwd));
  assert.ok(context.startsWith(f.values.RUNNER_TEMP + '/runtime-save-'));
  // The recipe is never an input to the save: nothing can be re-resolved.
  assert.ok(!args.some(arg => arg.includes(recipe) && !arg.includes(restoreRecipe)));
  assert.ok(!args.some(arg => /cache-(to|from)/.test(arg)));
  const unknown = saveFixture({ pushed: null });
  assert.equal(save(unknown.path, 'linux/amd64', 'buildcache', unknown.values, unknown.options).pushed, null);
});

test('a restored or already saved closure is never rewritten', () => {
  const reused = saveFixture();
  assert.deepEqual(save(reused.path, 'linux/amd64', 'buildcache', { ...reused.values, RUNTIME_CACHE_HIT: 'true' }, reused.options), { status: 'reused', ref: reused.ref, digest: reused.closure.digest });
  assert.deepEqual([reused.calls, reused.probes], [[], []]);
  const exists = saveFixture({ present: true });
  assert.deepEqual(save(exists.path, 'linux/amd64', 'buildcache', exists.values, exists.options), { status: 'exists', ref: exists.ref, digest: exists.closure.digest });
  assert.deepEqual([exists.calls, exists.probes], [[], [exists.ref]]);
});

test('nothing is pushed unless the layout still is the verified closure for this recipe', () => {
  for (const [name, change, reason] of [
    ['other digest', f => ({ ...f.values, RUNTIME_DIGEST: `sha256:${'0'.repeat(64)}` }), /runtime layout differs from the verified closure/],
    ['missing digest', f => ({ ...f.values, RUNTIME_DIGEST: '' }), /invalid OCI digest/],
    ['unknown cache state', f => ({ ...f.values, RUNTIME_CACHE_HIT: '' }), /unknown runtime cache state/],
    ['reference from another recipe', f => ({ ...f.values, RUNTIME_CACHE_REF: f.ref.replace('2026-10-05', '2026-09-01') }), /recipe changed after the cache reference was resolved/],
    ['no reference', f => ({ ...f.values, RUNTIME_CACHE_REF: undefined }), /recipe changed after the cache reference was resolved/],
  ]) {
    const f = saveFixture();
    assert.throws(() => save(f.path, 'linux/amd64', 'buildcache', change(f), f.options), reason, name);
    assert.deepEqual(f.calls, [], name);
  }
  // A layout altered after verification fails its blob hashes before any push.
  const f = saveFixture();
  writeFileSync(join(f.path, 'blobs/sha256', f.closure.digest.slice(7)), '{}');
  assert.throws(() => save(f.path, 'linux/amd64', 'buildcache', f.values, f.options), /digest or size mismatch/);
  assert.throws(() => save(f.path, 'linux/arm64', 'buildcache', f.values, f.options), /does not match the platform/);
  assert.deepEqual(f.calls, []);
  // A failed push is an error for the caller to report, never a silent success.
  const failing = saveFixture();
  assert.throws(() => save(failing.path, 'linux/amd64', 'buildcache', failing.values, { ...failing.options, run: () => { throw new Error('docker failed (1)'); } }), /docker failed/);
});

// Run the CLI against the committed recipe with a fixed clock and a fake docker.
function cli(args, { now = '2026-10-20T12:00:00.000Z', docker = 'exit 1', env = {} } = {}) {
  const directory = mkdtempSync(join(tmpdir(), 'aeon-runtime-cli-test-'));
  const bin = join(directory, 'bin'); mkdirSync(bin);
  writeFileSync(join(bin, 'docker'), `#!/bin/sh\nprintf '%s\\n' "$*" >> "${join(directory, 'docker.log')}"\n${docker}\n`, { mode: 0o700 });
  const clock = `const RealDate = Date;
    globalThis.Date = class extends RealDate {
      constructor(...args) { super(...(args.length ? args : [${JSON.stringify(now)}])); }
      static now() { return RealDate.parse(${JSON.stringify(now)}); }
    };`;
  const output = join(directory, 'output'), summary = join(directory, 'summary');
  const run = spawnSync(process.execPath, ['--import', `data:text/javascript,${encodeURIComponent(clock)}`, script, ...args], {
    encoding: 'utf8', timeout: 20_000,
    env: { PATH: `${bin}:${process.env.PATH}`, GITHUB_OUTPUT: output, GITHUB_STEP_SUMMARY: summary, RUNNER_TEMP: directory, ...env },
  });
  assert.ifError(run.error);
  const read = path => { try { return readFileSync(path, 'utf8'); } catch { return ''; } };
  return { run, directory, output: read(output), summary: read(summary), docker: read(join(directory, 'docker.log')) };
}
const committed = readFileSync(join(root, recipe));
const committedKey = refreshKey(committed.toString('utf8'));
const later = days => new Date(Date.parse(`${committedKey}T12:00:00Z`) + days * 24 * 60 * 60 * 1000).toISOString();

test('resolve CLI: a miss selects the recipe, a stale key warns, and neither fails the step', () => {
  const ref = cacheRef(committed, 'buildcache');
  const fresh = cli(['resolve', 'linux/amd64', 'buildcache'], { now: later(refreshLimitDays) });
  assert.equal(fresh.run.status, 0, fresh.run.stderr);
  assert.equal(fresh.output, `ref=${ref}\nhit=false\nfile=${recipe}\nbuild_contexts=\nrefresh=${committedKey}\nmanifest_sha256=\n`);
  assert.equal(fresh.docker, `buildx imagetools inspect --raw ${ref}\n`);
  assert.doesNotMatch(fresh.run.stdout, /::warning::/);
  assert.match(fresh.summary, /not used \(cannot read .*\); preparing cold\. Refresh key `\d{4}-\d{2}-\d{2}`, 35 days old\.\n$/);
  const stale = cli(['resolve', 'linux/amd64', 'buildcache'], { now: later(refreshLimitDays + 1) });
  assert.equal(stale.run.status, 0, 'an old refresh key warns; it never fails a release');
  assert.match(stale.run.stdout, /^::warning::Runtime refresh key \d{4}-\d{2}-\d{2} is 36 days old \(limit 35\)/m);
  assert.match(stale.summary, /36 days old\. \*\*Runtime refresh key .* is 36 days old/);
  assert.equal(stale.output, fresh.output);
});

test('resolve CLI: a readable saved closure selects the re-wrap recipe and its image', () => {
  const ref = cacheRef(committed, 'buildcache-arm64');
  const hit = cli(['resolve', 'linux/arm64', 'buildcache-arm64'], { now: later(1), docker: `printf '%s' '${manifestBytes}'` });
  assert.equal(hit.run.status, 0, hit.run.stderr);
  assert.equal(hit.output, `ref=${ref}\nhit=true\nfile=${restoreRecipe}\nbuild_contexts=aeon-runtime-closure=docker-image://${ref}\nrefresh=${committedKey}\nmanifest_sha256=${sha256(manifestBytes)}\n`);
  assert.match(hit.summary, /found; restoring it\. Refresh key/);
  const usage = cli(['resolve', 'linux/arm64']);
  assert.equal(usage.run.status, 1);
  assert.match(usage.run.stderr, /usage: runtime-closure\.mjs/);
  assert.equal(usage.output, '');
});

test('bind CLI: hands the digest to later steps and prints the closure per architecture', () => {
  const directory = mkdtempSync(join(tmpdir(), 'aeon-runtime-bind-test-'));
  const closure = layout(directory, { arch: 'amd64' });
  const ref = cacheRef(committed, 'buildcache');
  const bound = cli(['bind', directory, 'linux/amd64'], { env: { RUNTIME_CACHE_REF: ref, RUNTIME_CACHE_HIT: 'false', RUNTIME_CACHE_MANIFEST: '' } });
  assert.equal(bound.run.status, 0, bound.run.stderr);
  assert.equal(bound.output, `digest=${closure.digest}\nruntime_amd64=${closure.digest}\n`);
  for (const fragment of ['### Runtime closure (linux/amd64)', closure.digest, closure.config_digest, closure.diff_ids[0], `refresh key \`${committedKey}\``, 'source: cold preparation']) assert.ok(bound.summary.includes(fragment), fragment);
  assert.equal(JSON.parse(bound.run.stdout).digest, closure.digest);
  const wrong = cli(['bind', directory, 'linux/arm64']);
  assert.equal(wrong.run.status, 1);
  assert.match(wrong.run.stderr, /exactly one runtime platform manifest/);
  assert.deepEqual([wrong.output, wrong.summary], ['', '']);
});

test('save CLI: every outcome is written to the summary and a failure exits non-zero with a warning', () => {
  const directory = mkdtempSync(join(tmpdir(), 'aeon-runtime-save-cli-test-'));
  const closure = layout(directory, { arch: 'amd64' });
  const ref = cacheRef(committed, 'buildcache');
  const env = { RUNTIME_DIGEST: closure.digest, RUNTIME_CACHE_REF: ref, RUNTIME_CACHE_HIT: 'false' };
  const args = ['save', directory, 'linux/amd64', 'buildcache'];
  // The fake registry has no such reference and accepts the push.
  const saved = cli(args, { env, docker: 'case "$*" in *imagetools*) exit 1 ;; esac' });
  assert.equal(saved.run.status, 0, saved.run.stderr + saved.run.stdout);
  assert.match(saved.summary, new RegExp(`Runtime closure cache saved: \`${ref}\` from verified layout \`${closure.digest}\` \\(pushed manifest \`unknown\`\\)`));
  const lines = saved.docker.trim().split('\n');
  assert.equal(lines.length, 2);
  assert.equal(lines[0], `buildx imagetools inspect --raw ${ref}`);
  assert.match(lines[1], new RegExp(`^buildx build --platform linux/amd64 --provenance=false --build-context aeon-runtime-closure=oci-layout://.*@${closure.digest} `));
  assert.ok(lines[1].includes(`type=image,name=${ref},push=true,`));
  const reused = cli(args, { env: { ...env, RUNTIME_CACHE_HIT: 'true' } });
  assert.equal(reused.run.status, 0);
  assert.match(reused.summary, /not rewritten \(this run restored it\)/);
  assert.equal(reused.docker, '');
  const present = cli(args, { env, docker: `printf '%s' '${manifestBytes}'` });
  assert.equal(present.run.status, 0);
  assert.match(present.summary, /not rewritten \(already saved by another run\)/);
  assert.equal(present.docker.trim().split('\n').length, 1);
  for (const [name, options, reason] of [
    ['push failure', { env }, /docker failed/],
    ['unverified layout', { env: { ...env, RUNTIME_DIGEST: `sha256:${'0'.repeat(64)}` } }, /runtime layout differs from the verified closure/],
  ]) {
    const failed = cli(args, options);
    assert.equal(failed.run.status, 1, name);
    assert.match(failed.run.stdout, /^::warning::Runtime closure cache was not saved: /m, name);
    assert.match(failed.run.stdout, reason, name);
    assert.match(failed.summary, /Runtime closure cache was \*\*not saved\*\*: .*The release is unaffected/, name);
  }
});
