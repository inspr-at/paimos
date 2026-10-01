// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { imageSources, verifyImageIndex } from './release-image-index.mjs';

const digest = character => `sha256:${character.repeat(64)}`;
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
