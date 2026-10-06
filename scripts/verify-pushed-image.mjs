// SPDX-License-Identifier: AGPL-3.0-only
// The push is a second export of the smoked inputs. Before anything is
// attested, walk the pushed digest to its platform manifest and require the
// image config that was smoked and reproduced. The rehearsal runs the same
// walk over its local provenance export, and over a pinned public index to
// prove the registry reads; only a tag build can check a pushed digest.
import { lstatSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
import { root, sha256 } from './build-image-inputs.mjs';
import { digest, inspectRaw } from './image-evidence.mjs';

const limit = 4 * 1024 * 1024;

// Registry bytes must hash to the digest they were requested by. imagetools
// prints them unchanged; a single appended newline is tolerated and nothing else.
export function manifest(bytes, expected) {
  const id = digest(expected);
  if (!Buffer.isBuffer(bytes) || bytes.length > limit) throw new Error('invalid or oversized manifest');
  const exact = [bytes, bytes.at(-1) === 0x0a ? bytes.subarray(0, -1) : null]
    .find(candidate => candidate && `sha256:${sha256(candidate)}` === id);
  if (!exact) throw new Error(`manifest bytes do not match ${id}`);
  try {
    const value = JSON.parse(exact.toString('utf8'));
    if (value === null || typeof value !== 'object' || Array.isArray(value)) throw new Error('not an object');
    return value;
  } catch { throw new Error(`malformed manifest JSON at ${id}`); }
}
// exclusive: the index may hold only this platform and its attestations, as
// a single-platform release export does. A public base index is not exclusive.
export function platformConfig(rootDigest, fetch, platform, { exclusive = true } = {}) {
  const match = /^linux\/(amd64|arm64)$/.exec(platform ?? '');
  if (!match) throw new Error('invalid OCI platform');
  const index = manifest(fetch(digest(rootDigest)), rootDigest);
  if (!Array.isArray(index.manifests)) throw new Error('expected an OCI index, found an image manifest');
  if (index.manifests.length > 64) throw new Error('OCI index exceeds bounds');
  const matches = [];
  for (const descriptor of index.manifests) {
    const os = descriptor?.platform?.os, architecture = descriptor?.platform?.architecture;
    if (os === 'linux' && architecture === match[1]) matches.push(descriptor);
    else if (exclusive && !(os === 'unknown' && architecture === 'unknown')) throw new Error(`unexpected platform ${os}/${architecture} in a ${platform} image`);
  }
  if (matches.length !== 1) throw new Error(`expected exactly one ${platform} manifest, found ${matches.length}`);
  const image = manifest(fetch(digest(matches[0].digest)), matches[0].digest);
  if (Array.isArray(image.manifests) || !Array.isArray(image.layers)) throw new Error('expected an image manifest, found an OCI index');
  if (typeof image.config?.digest !== 'string') throw new Error('platform manifest lacks a config digest');
  return { index: rootDigest, manifest: matches[0].digest, config: digest(image.config.digest) };
}
export function verifyPushed(rootDigest, fetch, platform, smoked) {
  const expected = digest(smoked);
  const pushed = platformConfig(rootDigest, fetch, platform);
  if (pushed.config !== expected) throw new Error(`pushed image config ${pushed.config} differs from the smoked build config ${expected}`);
  return pushed;
}
export function registryFetch(name, inspect = inspectRaw) {
  if (!/^[a-z0-9][a-z0-9./_-]*$/.test(name)) throw new Error('invalid image repository');
  return id => inspect(`${name}@${digest(id)}`);
}
// A local OCI layout: one root descriptor, blobs addressed by digest.
export function layoutSource(directory) {
  function file(path) {
    const info = lstatSync(path);
    if (!info.isFile() || info.size > limit) throw new Error('invalid or oversized OCI JSON');
    return readFileSync(path);
  }
  const index = JSON.parse(file(join(directory, 'index.json')));
  if (!Array.isArray(index.manifests) || index.manifests.length !== 1) throw new Error('expected exactly one root descriptor in the OCI layout');
  return { root: digest(index.manifests[0].digest), fetch: id => file(join(directory, 'blobs/sha256', digest(id).slice(7))) };
}
// The digest-pinned base of the runtime recipe: a public index whose digest is
// already in this repository, so a rehearsal can prove the registry walk.
export function pinnedBase(recipeText) {
  const bases = [...recipeText.matchAll(/^FROM ([a-z0-9./_-]+)(?::[A-Za-z0-9_.-]+)?@(sha256:[a-f0-9]{64})$/gm)];
  if (bases.length !== 1) throw new Error('runtime recipe needs exactly one digest-pinned base');
  return { name: bases[0][1], root: bases[0][2] };
}
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const [mode, ...args] = process.argv.slice(2);
    let result;
    if (mode === 'registry' && args.length === 2) {
      const [name, id, extra] = args[0].split('@');
      if (extra !== undefined) throw new Error('invalid image reference');
      result = verifyPushed(id, registryFetch(name), args[1], process.env.SMOKE_CONFIG_DIGEST);
    } else if (mode === 'layout' && args.length === 2) {
      const source = layoutSource(args[0]);
      result = verifyPushed(source.root, source.fetch, args[1], process.env.SMOKE_CONFIG_DIGEST);
    } else if (mode === 'probe' && args.length === 1) {
      const base = pinnedBase(readFileSync(join(root, 'scripts/Dockerfile.runtime'), 'utf8'));
      result = platformConfig(base.root, registryFetch(base.name), args[0], { exclusive: false });
    } else throw new Error('usage: verify-pushed-image.mjs registry NAME@DIGEST PLATFORM | layout DIRECTORY PLATFORM | probe PLATFORM');
    console.log(JSON.stringify({ mode, platform: args.at(-1), ...result }));
  } catch (error) {
    console.error(error.message);
    console.log(`::error::${error.message}`);
    process.exitCode = 1;
  }
}
