// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, readFileSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import { sha256 } from './build-image-inputs.mjs';
import { layoutSource, manifest, pinnedBase, platformConfig, registryFetch, verifyPushed } from './verify-pushed-image.mjs';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const script = fileURLToPath(new URL('./verify-pushed-image.mjs', import.meta.url));
const smoked = `sha256:${sha256('smoked config')}`;
const other = `sha256:${sha256('another config')}`;
const attestation = { os: 'unknown', architecture: 'unknown' };

// A content-addressed store standing in for a registry or an OCI layout.
function store() {
  const blobs = new Map();
  const put = value => {
    const bytes = Buffer.isBuffer(value) ? value : Buffer.from(JSON.stringify(value));
    const id = `sha256:${sha256(bytes)}`;
    blobs.set(id, bytes);
    return id;
  };
  const fetch = id => {
    if (!blobs.has(id)) throw new Error(`cannot read ${id}: not found`);
    return blobs.get(id);
  };
  return { blobs, put, fetch };
}
const image = (s, config = smoked) => s.put({ schemaVersion: 2, config: { digest: config }, layers: [{ digest: `sha256:${'1'.repeat(64)}` }] });
const descriptor = (digest, platform) => ({ mediaType: 'application/vnd.oci.image.manifest.v1+json', digest, size: 1, ...(platform ? { platform } : {}) });
// What a single-platform push with provenance looks like: the image and its attestation.
function pushed(s, { arch = 'amd64', config = smoked } = {}) {
  const manifestDigest = image(s, config);
  const index = s.put({ schemaVersion: 2, mediaType: 'application/vnd.oci.image.index.v1+json', manifests: [
    descriptor(manifestDigest, { os: 'linux', architecture: arch }),
    descriptor(s.put({ schemaVersion: 2, config: { digest: other }, layers: [], attestation: true }), attestation),
  ] });
  return { index, manifest: manifestDigest };
}

test('the pushed platform manifest must carry the smoked image config', () => {
  for (const arch of ['amd64', 'arm64']) {
    const s = store(), root = pushed(s, { arch });
    assert.deepEqual(verifyPushed(root.index, s.fetch, `linux/${arch}`, smoked), { index: root.index, manifest: root.manifest, config: smoked });
  }
  // arm64 descriptors may carry a variant; it does not change the platform.
  const s = store(), manifestDigest = image(s);
  const index = s.put({ schemaVersion: 2, manifests: [descriptor(manifestDigest, { os: 'linux', architecture: 'arm64', variant: 'v8' })] });
  assert.equal(verifyPushed(index, s.fetch, 'linux/arm64', smoked).manifest, manifestDigest);
});

test('a different config fails with both digests', () => {
  const s = store(), root = pushed(s, { config: other });
  assert.throws(() => verifyPushed(root.index, s.fetch, 'linux/amd64', smoked),
    error => error.message === `pushed image config ${other} differs from the smoked build config ${smoked}`);
  for (const invalid of ['', undefined, 'sha256:short', smoked.toUpperCase(), smoked + '\n']) {
    assert.throws(() => verifyPushed(root.index, s.fetch, 'linux/amd64', invalid), /invalid OCI digest/);
  }
});

test('an index is required at the pushed digest and an image manifest below it', () => {
  const s = store();
  const bare = image(s);
  assert.throws(() => verifyPushed(bare, s.fetch, 'linux/amd64', smoked), /expected an OCI index, found an image manifest/);
  const nested = s.put({ schemaVersion: 2, manifests: [descriptor(pushed(s).index, { os: 'linux', architecture: 'amd64' })] });
  assert.throws(() => verifyPushed(nested, s.fetch, 'linux/amd64', smoked), /expected an image manifest, found an OCI index/);
  const noLayers = s.put({ schemaVersion: 2, manifests: [descriptor(s.put({ schemaVersion: 2, config: { digest: smoked } }), { os: 'linux', architecture: 'amd64' })] });
  assert.throws(() => verifyPushed(noLayers, s.fetch, 'linux/amd64', smoked), /expected an image manifest/);
});

test('the wrong, a missing, an extra or a duplicated platform is rejected', () => {
  const s = store(), arm = pushed(s, { arch: 'arm64' });
  assert.throws(() => verifyPushed(arm.index, s.fetch, 'linux/amd64', smoked), /unexpected platform linux\/arm64 in a linux\/amd64 image/);
  const only = s.put({ schemaVersion: 2, manifests: [descriptor(image(s), attestation)] });
  assert.throws(() => verifyPushed(only, s.fetch, 'linux/amd64', smoked), /expected exactly one linux\/amd64 manifest, found 0/);
  const unlabelled = s.put({ schemaVersion: 2, manifests: [descriptor(image(s))] });
  assert.throws(() => verifyPushed(unlabelled, s.fetch, 'linux/amd64', smoked), /unexpected platform undefined\/undefined/);
  const both = s.put({ schemaVersion: 2, manifests: [descriptor(image(s), { os: 'linux', architecture: 'amd64' }), descriptor(image(s, other), { os: 'linux', architecture: 'arm64' })] });
  assert.throws(() => verifyPushed(both, s.fetch, 'linux/amd64', smoked), /unexpected platform linux\/arm64/);
  const twice = s.put({ schemaVersion: 2, manifests: [descriptor(image(s), { os: 'linux', architecture: 'amd64' }), descriptor(image(s, other), { os: 'linux', architecture: 'amd64' })] });
  assert.throws(() => verifyPushed(twice, s.fetch, 'linux/amd64', smoked), /expected exactly one linux\/amd64 manifest, found 2/);
  const windows = s.put({ schemaVersion: 2, manifests: [descriptor(image(s), { os: 'windows', architecture: 'amd64' })] });
  assert.throws(() => verifyPushed(windows, s.fetch, 'linux/amd64', smoked), /unexpected platform windows\/amd64/);
  for (const platform of ['linux/riscv64', 'darwin/arm64', 'amd64', '', undefined]) assert.throws(() => verifyPushed(arm.index, s.fetch, platform, smoked), /invalid OCI platform/);
  const many = s.put({ schemaVersion: 2, manifests: Array.from({ length: 65 }, () => descriptor(image(s), attestation)) });
  assert.throws(() => verifyPushed(many, s.fetch, 'linux/amd64', smoked), /exceeds bounds/);
});

test('a platform manifest without a usable config digest is rejected', () => {
  for (const [config, reason] of [[undefined, /lacks a config digest/], [{}, /lacks a config digest/], [{ digest: 7 }, /lacks a config digest/], [{ digest: 'sha256:short' }, /invalid OCI digest/]]) {
    const s = store();
    const index = s.put({ schemaVersion: 2, manifests: [descriptor(s.put({ schemaVersion: 2, config, layers: [] }), { os: 'linux', architecture: 'amd64' })] });
    assert.throws(() => verifyPushed(index, s.fetch, 'linux/amd64', smoked), reason);
  }
});

test('malformed JSON and bytes that do not hash to their digest are rejected', () => {
  for (const text of ['{"manifests":', '<html>', 'null', '[]', '"index"', '']) {
    const s = store(), id = s.put(Buffer.from(text));
    assert.throws(() => verifyPushed(id, s.fetch, 'linux/amd64', smoked), new RegExp(`malformed manifest JSON at ${id}`), JSON.stringify(text));
  }
  // A malformed platform manifest below a valid index is rejected the same way.
  const s = store(), broken = s.put(Buffer.from('{"config":'));
  const index = s.put({ schemaVersion: 2, manifests: [descriptor(broken, { os: 'linux', architecture: 'amd64' })] });
  assert.throws(() => verifyPushed(index, s.fetch, 'linux/amd64', smoked), new RegExp(`malformed manifest JSON at ${broken}`));
  // Content substituted under a digest, at either level.
  const top = store(), good = pushed(top);
  top.blobs.set(good.index, Buffer.concat([top.blobs.get(good.index), Buffer.from(' ')]));
  assert.throws(() => verifyPushed(good.index, top.fetch, 'linux/amd64', smoked), new RegExp(`manifest bytes do not match ${good.index}`));
  // The index is intact, but another image's manifest is served for its entry.
  const below = store(), evil = pushed(below);
  below.blobs.set(evil.manifest, below.blobs.get(image(below, other)));
  assert.throws(() => verifyPushed(evil.index, below.fetch, 'linux/amd64', smoked), new RegExp(`manifest bytes do not match ${evil.manifest}`));
  assert.throws(() => verifyPushed(`sha256:${'9'.repeat(64)}`, s.fetch, 'linux/amd64', smoked), /cannot read .*not found/);
});

test('one appended newline is tolerated when hashing, and nothing else', () => {
  const bytes = Buffer.from('{"schemaVersion":2}'), id = `sha256:${sha256(bytes)}`;
  assert.deepEqual(manifest(bytes, id), { schemaVersion: 2 });
  assert.deepEqual(manifest(Buffer.concat([bytes, Buffer.from('\n')]), id), { schemaVersion: 2 });
  // Registry bytes that really end in a newline hash with it.
  const ending = Buffer.from('{"schemaVersion":2}\n'), endingID = `sha256:${sha256(ending)}`;
  assert.deepEqual(manifest(ending, endingID), { schemaVersion: 2 });
  for (const suffix of ['\n\n', ' ', '\r\n', '\n ']) assert.throws(() => manifest(Buffer.concat([bytes, Buffer.from(suffix)]), id), /do not match/);
  assert.throws(() => manifest(bytes.toString('utf8'), id), /invalid or oversized manifest/);
  assert.throws(() => manifest(Buffer.alloc(4 * 1024 * 1024 + 1), id), /invalid or oversized manifest/);
  assert.throws(() => manifest(bytes, 'sha256:short'), /invalid OCI digest/);
});

test('a public base index is walked without the single-platform restriction', () => {
  const s = store(), amd = image(s, other), arm = image(s);
  const index = s.put({ schemaVersion: 2, manifests: [
    descriptor(amd, { os: 'linux', architecture: 'amd64' }), descriptor(arm, { os: 'linux', architecture: 'arm64', variant: 'v8' }),
    descriptor(image(s), { os: 'linux', architecture: 'riscv64' }), descriptor(image(s), attestation),
  ] });
  assert.deepEqual(platformConfig(index, s.fetch, 'linux/arm64', { exclusive: false }), { index, manifest: arm, config: smoked });
  assert.deepEqual(platformConfig(index, s.fetch, 'linux/amd64', { exclusive: false }), { index, manifest: amd, config: other });
  assert.throws(() => platformConfig(index, s.fetch, 'linux/amd64'), /unexpected platform linux\/arm64/);
});

test('the rehearsal probe target is the one digest-pinned base of the committed runtime recipe', () => {
  const base = pinnedBase(readFileSync(join(root, 'scripts/Dockerfile.runtime'), 'utf8'));
  assert.equal(base.name, 'alpine');
  assert.match(base.root, /^sha256:[a-f0-9]{64}$/);
  const pin = `sha256:${'a'.repeat(64)}`;
  assert.deepEqual(pinnedBase(`# FROM ignored@${pin}\nFROM ghcr.io/library/alpine:3.24@${pin}\nRUN true\n`), { name: 'ghcr.io/library/alpine', root: pin });
  assert.deepEqual(pinnedBase(`FROM alpine@${pin}\n`), { name: 'alpine', root: pin });
  for (const text of ['FROM alpine:3.24\n', `FROM alpine@${pin}\nFROM alpine@${pin}\n`, `FROM alpine@${pin} AS base\n`, '']) assert.throws(() => pinnedBase(text), /exactly one digest-pinned base/);
});

test('registry reads address every manifest by repository and digest', () => {
  const calls = [], id = `sha256:${'a'.repeat(64)}`;
  const fetch = registryFetch('ghcr.io/inspr-at/aeon', reference => { calls.push(reference); return Buffer.from('{}'); });
  assert.deepEqual(fetch(id), Buffer.from('{}'));
  assert.deepEqual(calls, [`ghcr.io/inspr-at/aeon@${id}`]);
  assert.throws(() => fetch('latest'), /invalid OCI digest/);
  for (const name of ['', '--help', 'ghcr.io/inspr-at/aeon:tag', 'GHCR.io/x', `ghcr.io/x@${id}`]) assert.throws(() => registryFetch(name), /invalid image repository/);
});

function layout(s, rootDescriptors) {
  const directory = mkdtempSync(join(tmpdir(), 'aeon-pushed-layout-test-'));
  mkdirSync(join(directory, 'blobs/sha256'), { recursive: true });
  for (const [id, bytes] of s.blobs) writeFileSync(join(directory, 'blobs/sha256', id.slice(7)), bytes);
  writeFileSync(join(directory, 'index.json'), JSON.stringify({ schemaVersion: 2, manifests: rootDescriptors }));
  return directory;
}

test('a local provenance export is verified through the same walk', () => {
  const s = store(), root = pushed(s, { arch: 'arm64' });
  const directory = layout(s, [descriptor(root.index)]);
  const source = layoutSource(directory);
  assert.equal(source.root, root.index);
  assert.deepEqual(verifyPushed(source.root, source.fetch, 'linux/arm64', smoked), { index: root.index, manifest: root.manifest, config: smoked });
  assert.throws(() => verifyPushed(source.root, source.fetch, 'linux/arm64', other), /differs from the smoked build config/);
  assert.throws(() => layoutSource(layout(s, [])), /exactly one root descriptor/);
  assert.throws(() => layoutSource(layout(s, [descriptor(root.index), descriptor(root.manifest)])), /exactly one root descriptor/);
  assert.throws(() => layoutSource(layout(s, [{ digest: 'sha256:../../etc/passwd' }])), /invalid OCI digest/);
  // An export without provenance has no index to bind; production always has one.
  const flat = layoutSource(layout(s, [descriptor(root.manifest)]));
  assert.throws(() => verifyPushed(flat.root, flat.fetch, 'linux/arm64', smoked), /expected an OCI index, found an image manifest/);
  // Blobs are regular bounded files; a tampered one fails its hash.
  writeFileSync(join(directory, 'blobs/sha256', root.manifest.slice(7)), s.blobs.get(image(s, other)));
  assert.throws(() => verifyPushed(source.root, source.fetch, 'linux/arm64', smoked), /manifest bytes do not match/);
  const linked = layout(s, [descriptor(root.index)]);
  const target = join(linked, 'blobs/sha256', root.index.slice(7));
  writeFileSync(target + '.real', s.blobs.get(root.index));
  spawnSync('mv', [target, target + '.moved']);
  symlinkSync(target + '.real', target);
  const viaLink = layoutSource(linked);
  assert.throws(() => verifyPushed(viaLink.root, viaLink.fetch, 'linux/arm64', smoked), /invalid or oversized OCI JSON/);
});

// Run the CLI with a fake docker that serves manifests from files, as
// `docker buildx imagetools inspect --raw` would from a registry.
function cli(args, { s = store(), smokedConfig = smoked, newline = false } = {}) {
  const directory = mkdtempSync(join(tmpdir(), 'aeon-pushed-cli-test-'));
  const bin = join(directory, 'bin'), served = join(directory, 'served');
  mkdirSync(bin); mkdirSync(served);
  for (const [id, bytes] of s.blobs) writeFileSync(join(served, id.slice(7)), newline ? Buffer.concat([bytes, Buffer.from('\n')]) : bytes);
  writeFileSync(join(bin, 'docker'), `#!/bin/sh
printf '%s\\n' "$*" >> "${join(directory, 'docker.log')}"
[ "$1 $2 $3 $4" = "buildx imagetools inspect --raw" ] || exit 64
file="${served}/\${5##*@sha256:}"
[ -f "$file" ] || { echo "ERROR: $5: not found" >&2; exit 1; }
cat "$file"
`, { mode: 0o700 });
  const env = { PATH: `${bin}:${process.env.PATH}` };
  if (smokedConfig !== null) env.SMOKE_CONFIG_DIGEST = smokedConfig;
  const run = spawnSync(process.execPath, [script, ...args], { encoding: 'utf8', timeout: 20_000, env });
  assert.ifError(run.error);
  let log = '';
  try { log = readFileSync(join(directory, 'docker.log'), 'utf8'); } catch { /* No registry read. */ }
  return { run, log };
}

test('registry CLI: reads the pushed digest and its platform manifest, then compares configs', () => {
  for (const newline of [false, true]) {
    const s = store(), root = pushed(s);
    const ok = cli(['registry', `ghcr.io/inspr-at/aeon@${root.index}`, 'linux/amd64'], { s, newline });
    assert.equal(ok.run.status, 0, ok.run.stderr);
    assert.deepEqual(JSON.parse(ok.run.stdout), { mode: 'registry', platform: 'linux/amd64', index: root.index, manifest: root.manifest, config: smoked });
    assert.equal(ok.log, `buildx imagetools inspect --raw ghcr.io/inspr-at/aeon@${root.index}\nbuildx imagetools inspect --raw ghcr.io/inspr-at/aeon@${root.manifest}\n`);
  }
  const s = store(), root = pushed(s, { config: other });
  const reference = `ghcr.io/inspr-at/aeon@${root.index}`;
  const mismatch = cli(['registry', reference, 'linux/amd64'], { s });
  assert.equal(mismatch.run.status, 1);
  const message = `pushed image config ${other} differs from the smoked build config ${smoked}`;
  assert.equal(mismatch.run.stderr.trim(), message);
  assert.equal(mismatch.run.stdout.trim(), `::error::${message}`);
  for (const [name, args, options, reason] of [
    ['no smoked digest', ['registry', reference, 'linux/amd64'], { s, smokedConfig: null }, /invalid OCI digest/],
    ['empty smoked digest', ['registry', reference, 'linux/amd64'], { s, smokedConfig: '' }, /invalid OCI digest/],
    ['wrong platform', ['registry', reference, 'linux/arm64'], { s }, /unexpected platform linux\/amd64 in a linux\/arm64 image/],
    ['absent digest', ['registry', `ghcr.io/inspr-at/aeon@sha256:${'9'.repeat(64)}`, 'linux/amd64'], { s }, /cannot read .*not found/],
    ['tag instead of digest', ['registry', 'ghcr.io/inspr-at/aeon:latest', 'linux/amd64'], { s }, /invalid/],
    ['two digests', ['registry', `${reference}@${root.index}`, 'linux/amd64'], { s }, /invalid image reference/],
    ['no mode', [], { s }, /usage: verify-pushed-image\.mjs/],
  ]) {
    const failed = cli(args, options);
    assert.equal(failed.run.status, 1, name);
    assert.match(failed.run.stderr, reason, name);
  }
});

test('layout CLI: the rehearsal export passes only with the smoked config', () => {
  const s = store(), root = pushed(s, { arch: 'arm64' });
  const directory = layout(s, [descriptor(root.index)]);
  const ok = cli(['layout', directory, 'linux/arm64']);
  assert.equal(ok.run.status, 0, ok.run.stderr);
  assert.deepEqual(JSON.parse(ok.run.stdout), { mode: 'layout', platform: 'linux/arm64', index: root.index, manifest: root.manifest, config: smoked });
  assert.equal(ok.log, '', 'a local export is verified without any registry read');
  const mismatch = cli(['layout', directory, 'linux/arm64'], { smokedConfig: other });
  assert.equal(mismatch.run.status, 1);
  assert.match(mismatch.run.stderr, new RegExp(`pushed image config ${smoked} differs from the smoked build config ${other}`));
  assert.equal(cli(['layout', join(directory, 'missing'), 'linux/arm64']).run.status, 1);
});

test('probe CLI: walks the pinned base index with real registry-style reads and compares nothing', () => {
  const base = pinnedBase(readFileSync(join(root, 'scripts/Dockerfile.runtime'), 'utf8'));
  // Serve a stand-in index under the committed digest's name; its bytes cannot hash to it.
  const s = store(), manifestDigest = image(s);
  s.blobs.set(base.root, Buffer.from(JSON.stringify({ schemaVersion: 2, manifests: [descriptor(manifestDigest, { os: 'linux', architecture: 'amd64' })] })));
  const substituted = cli(['probe', 'linux/amd64'], { s, smokedConfig: null });
  assert.equal(substituted.run.status, 1);
  assert.match(substituted.run.stderr, new RegExp(`manifest bytes do not match ${base.root}`));
  assert.equal(substituted.log, `buildx imagetools inspect --raw ${base.name}@${base.root}\n`);
});
