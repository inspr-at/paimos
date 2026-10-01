// SPDX-License-Identifier: AGPL-3.0-only
// Fail closed before publishing the release coordinate and after reading it back.
import { readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const digestPattern = /^sha256:[a-f0-9]{64}$/;
const architectures = ['amd64', 'arm64'];

export function imageSources(directory) {
  const files = readdirSync(directory).sort();
  if (JSON.stringify(files) !== JSON.stringify(architectures.map(arch => `${arch}.txt`))) {
    throw new Error('Expected exactly amd64.txt and arm64.txt from successful platform jobs');
  }
  const digests = architectures.map(arch => readFileSync(join(directory, `${arch}.txt`), 'utf8').trim());
  if (digests.some(digest => !digestPattern.test(digest)) || new Set(digests).size !== 2) {
    throw new Error('Expected two distinct immutable platform digests');
  }
  return digests.map(digest => `ghcr.io/inspr-at/aeon@${digest}`);
}

export function verifyImageIndex(index) {
  if (index.schemaVersion !== 2 || index.mediaType !== 'application/vnd.oci.image.index.v1+json' || !Array.isArray(index.manifests)) {
    throw new Error('Expected an OCI image index');
  }
  const runtime = new Map();
  const attestations = [];
  const seen = new Set();
  for (const manifest of index.manifests) {
    if (!digestPattern.test(manifest.digest) || seen.has(manifest.digest) || !Number.isInteger(manifest.size) || manifest.size <= 0 || manifest.mediaType !== 'application/vnd.oci.image.manifest.v1+json') {
      throw new Error('Invalid or duplicate image descriptor');
    }
    seen.add(manifest.digest);
    const platform = manifest.platform;
    if (platform?.os === 'linux' && architectures.includes(platform.architecture)) {
      if (runtime.has(platform.architecture)) throw new Error('Duplicate runtime architecture');
      runtime.set(platform.architecture, manifest.digest);
    } else if (platform?.os === 'unknown' && platform.architecture === 'unknown' && manifest.annotations?.['vnd.docker.reference.type'] === 'attestation-manifest') {
      attestations.push(manifest.annotations['vnd.docker.reference.digest']);
    } else {
      throw new Error('Unexpected platform in release index');
    }
  }
  if (runtime.size !== 2 || attestations.length !== 2 || new Set(attestations).size !== 2 || [...runtime.values()].some(digest => !attestations.includes(digest))) {
    throw new Error('Both linux/amd64 and linux/arm64 require their own BuildKit provenance descriptor');
  }
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  try {
    const [command, path] = process.argv.slice(2);
    if (command === 'sources' && path) console.log(imageSources(path).join('\n'));
    else if (command === 'verify' && path) verifyImageIndex(JSON.parse(readFileSync(path, 'utf8')));
    else throw new Error('Usage: release-image-index.mjs sources <directory> | verify <index.json>');
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
