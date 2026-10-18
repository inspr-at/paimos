// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { imageSources, verifyImageIndex, verifyRuntimeIdentity } from './release-image-index.mjs';

const digest = character => `sha256:${character.repeat(64)}`;
const exportBytes = name => readFileSync(new URL(`./releaseworkflow/testdata/runtime-identity/${name}.json`, import.meta.url));
const runtimeFixture = name => JSON.parse(exportBytes(name));

test('clock-only changes in two real exports retain runtime identity', () => {
  const smoked = runtimeFixture('smoked'), published = runtimeFixture('clock-only');
  const provenance = runtimeFixture('provenance');
  for (const name of ['smoked', 'clock-only', 'different-rootfs']) {
    assert.equal(createHash('sha256').update(exportBytes(name)).digest('hex'), provenance.exports[name].config_sha256);
  }
  assert.notEqual(smoked.created, published.created);
  assert.notEqual(provenance.exports.smoked.config_sha256, provenance.exports['clock-only'].config_sha256);
  assert.deepEqual(smoked.rootfs, published.rootfs);
  assert.deepEqual(smoked.config, published.config);
  const withoutClocks = image => ({ ...image, created: null, history: image.history.map(item => ({ ...item, created: null })) });
  assert.deepEqual(withoutClocks(smoked), withoutClocks(published), 'only injected timestamps may differ');
  verifyRuntimeIdentity(smoked, published);
});

test('a different rootfs in a real export fails despite matching runtime settings', () => {
  const smoked = runtimeFixture('smoked'), published = runtimeFixture('different-rootfs');
  assert.deepEqual(smoked.config, published.config);
  assert.notDeepEqual(smoked.rootfs.diff_ids, published.rootfs.diff_ids);
  assert.throws(() => verifyRuntimeIdentity(smoked, published), /differs/);
});

test('runtime changes and malformed identity inputs fail closed', () => {
  const smoked = runtimeFixture('smoked');
  verifyRuntimeIdentity(smoked, structuredClone(smoked));
  for (const [field, value] of Object.entries({ User: '0', Env: ['AEON_FIXTURE=changed'], Entrypoint: ['/other'], Cmd: ['other'], WorkingDir: '/other', ExposedPorts: { '80/tcp': {} }, Labels: { changed: 'yes' }, StopSignal: 'SIGKILL', Healthcheck: { Test: ['CMD', '/other'] } })) {
    const published = structuredClone(smoked);
    published.config[field] = value;
    assert.throws(() => verifyRuntimeIdentity(smoked, published), /differs/, field);
  }
  for (const mutate of [image => image.architecture = 'amd64', image => image.os = 'windows', image => image.variant = 'v7']) {
    const published = structuredClone(smoked);
    mutate(published);
    assert.throws(() => verifyRuntimeIdentity(smoked, published), /differs/);
  }
  const twoLayers = structuredClone(smoked);
  twoLayers.rootfs.diff_ids.push(digest('f'));
  const reversed = structuredClone(twoLayers);
  reversed.rootfs.diff_ids.reverse();
  assert.throws(() => verifyRuntimeIdentity(twoLayers, reversed), /differs/, 'layer order is runtime identity');
  for (const invalid of [null, [], '', digest('a'), {}, { ...smoked, config: null }, { ...smoked, rootfs: { type: 'layers', diff_ids: [] } }, { ...smoked, rootfs: { type: 'layers', diff_ids: ['sha256:short'] } }]) {
    assert.throws(() => verifyRuntimeIdentity(smoked, invalid), /Invalid/);
    assert.throws(() => verifyRuntimeIdentity(invalid, smoked), /Invalid/);
  }
});
function fixture() {
  const descriptor = (character, platform, annotations) => ({
    mediaType: 'application/vnd.oci.image.manifest.v1+json', digest: digest(character), size: 100, platform, annotations,
  });
  return {
    schemaVersion: 2, mediaType: 'application/vnd.oci.image.index.v1+json',
    manifests: [
      descriptor('a', { os: 'linux', architecture: 'amd64' }),
      descriptor('b', { os: 'linux', architecture: 'arm64' }),
      ...['a', 'b'].map((character, i) => descriptor(['c', 'd'][i], { os: 'unknown', architecture: 'unknown' }, {
        'vnd.docker.reference.type': 'attestation-manifest', 'vnd.docker.reference.digest': digest(character),
      })),
    ],
  };
}

test('accepts both runtimes with separate bound provenance descriptors', () => verifyImageIndex(fixture()));
for (const [name, mutate] of [
  ['missing arm64', index => index.manifests.splice(1, 1)],
  ['missing provenance', index => index.manifests.pop()],
  ['cross-bound provenance', index => index.manifests[3].annotations['vnd.docker.reference.digest'] = digest('a')],
  ['unbound provenance', index => index.manifests[3].annotations['vnd.docker.reference.digest'] = digest('e')],
  ['duplicate architecture', index => index.manifests[1].platform.architecture = 'amd64'],
  ['unexpected platform', index => index.manifests[1].platform.os = 'darwin'],
  ['malformed digest', index => index.manifests[1].digest = 'latest'],
  ['duplicate descriptor', index => index.manifests.push(index.manifests[0])],
  ['nested index', index => index.manifests[1].mediaType = index.mediaType],
]) {
  test(`rejects ${name} before publication`, () => {
    const index = fixture();
    mutate(index);
    assert.throws(() => verifyImageIndex(index));
  });
}

test('requires both successful digest artifacts and rejects extras and unsafe references', () => {
  const directory = mkdtempSync(join(tmpdir(), 'aeon-index-test-'));
  writeFileSync(join(directory, 'amd64.txt'), `${digest('a')}\n`);
  assert.throws(() => imageSources(directory), /exactly/);
  writeFileSync(join(directory, 'arm64.txt'), `${digest('b')}\n`);
  assert.deepEqual(imageSources(directory), ['a', 'b'].map(character => `ghcr.io/inspr-at/aeon@${digest(character)}`));
  for (const invalid of [digest('a'), 'latest', `${digest('b')}\n${digest('c')}`, 'sha256:short']) {
    writeFileSync(join(directory, 'arm64.txt'), invalid);
    assert.throws(() => imageSources(directory), /distinct immutable/);
  }
  writeFileSync(join(directory, 'extra.txt'), digest('c'));
  assert.throws(() => imageSources(directory), /exactly/);
});
