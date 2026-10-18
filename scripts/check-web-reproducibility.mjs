// SPDX-License-Identifier: AGPL-3.0-only
// Compare the existing CI build with one fresh Vite build using the same install.
import assert from 'node:assert/strict';
import { copyFileSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { pathToFileURL } from 'node:url';
import { command, filesIn, root, sha256 } from './build-image-inputs.mjs';

export function assetManifest(directory) {
  const files = filesIn(directory);
  if (!files.some(file => file.relative === 'index.html')) throw new Error('web build lacks index.html');
  return files.map(file => ({ path: file.relative, sha256: sha256(readFileSync(file.path)) }));
}

export function checkWebReproducibility(cwd = root, run = command) {
  const dist = join(cwd, 'web/dist');
  const evidence = join(cwd, 'tmp/web-reproducibility');
  const first = assetManifest(dist);
  // Keep the first bytes as well as the manifests, so a failure can be diagnosed.
  for (const file of first) {
    const target = join(evidence, 'first', file.path);
    mkdirSync(dirname(target), { recursive: true });
    copyFileSync(join(dist, file.path), target);
  }
  writeFileSync(join(evidence, 'first.json'), JSON.stringify(first, null, 2) + '\n');
  // Vite clears dist. Typechecking and release verification already ran in Build web.
  run('npx', ['--no-install', 'vite', 'build'], join(cwd, 'web'));
  const second = assetManifest(dist);
  writeFileSync(join(evidence, 'second.json'), JSON.stringify(second, null, 2) + '\n');
  assert.deepEqual(second, first, 'web asset filenames or bytes differ between builds; see tmp/web-reproducibility');
  return first.length;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try { console.log(`PASS: ${checkWebReproducibility()} web files are byte-identical across two builds`); }
  catch (error) { console.error(error.message); process.exitCode = 1; }
}
