// SPDX-License-Identifier: AGPL-3.0-only
// Run production index/attestation/draft shell bodies with strict offline
// adapters. Only local files are produced; gh uses a closed loopback proxy.
import { spawnSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { ASSETS, complete } from './release-completion.mjs';
import { createHash } from 'node:crypto';
import { checkGH } from './release-rehearsal-gh.mjs';

const self = fileURLToPath(import.meta.url);
const digest = c => `sha256:${c.repeat(64)}`;
const index = { schemaVersion: 2, mediaType: 'application/vnd.oci.image.index.v1+json', manifests: [
  ...['amd64', 'arm64'].map((architecture, i) => ({ mediaType: 'application/vnd.oci.image.manifest.v1+json',
    digest: digest(String(i + 1)), size: 100, platform: { os: 'linux', architecture } })),
  ...['amd64', 'arm64'].map((_, i) => ({ mediaType: 'application/vnd.oci.image.manifest.v1+json',
    digest: digest(String(i + 3)), size: 100, platform: { os: 'unknown', architecture: 'unknown' },
    annotations: { 'vnd.docker.reference.type': 'attestation-manifest', 'vnd.docker.reference.digest': digest(String(i + 1)) } })),
] };

if (process.argv[2] === 'docker') {
  const args = process.argv.slice(3);
  const sources = [1, 2].map(i => `ghcr.io/inspr-at/aeon@${digest(String(i))}`);
  if (JSON.stringify(args) === JSON.stringify(['buildx', 'imagetools', 'create', '--dry-run', ...sources]) ||
      JSON.stringify(args) === JSON.stringify(['buildx', 'imagetools', 'inspect', '--raw', `ghcr.io/inspr-at/aeon@${digest('5')}`])) {
    console.log(JSON.stringify(index));
  } else if (JSON.stringify(args) === JSON.stringify(['buildx', 'imagetools', 'create', '--tag',
    `ghcr.io/inspr-at/aeon:${process.env.VERSION}`, '--metadata-file', `${process.env.RUNNER_TEMP}/image-index-metadata.json`, ...sources])) {
    writeFileSync(args[6], JSON.stringify({ 'containerimage.descriptor': { digest: digest('5') } }));
  } else { console.error('Offline Docker adapter refuses unexpected arguments'); process.exitCode = 1; }
} else {
  const directory = mkdtempSync(join(tmpdir(), 'aeon-rehearsal-offline-'));
  const bin = join(directory, 'bin'); mkdirSync(bin);
  // Use JSON string literals in JS, not interpolated shell arguments.
  writeFileSync(join(bin, 'docker'), `#!/usr/bin/env node\nimport { spawnSync } from 'node:child_process';\nconst r=spawnSync('node',[${JSON.stringify(self)},'docker',...process.argv.slice(2)],{stdio:'inherit'});process.exit(r.status??1);\n`, { mode: 0o700 });
  const realGH = spawnSync('which', ['gh'], { encoding: 'utf8' }).stdout.trim();
  if (!realGH) throw new Error('gh is required for command rehearsal');
  writeFileSync(join(bin, 'gh'), `#!/usr/bin/env node\nimport { spawnSync } from 'node:child_process';\nconst r=spawnSync('node',[${JSON.stringify(resolve('scripts/release-rehearsal-gh.mjs'))},...process.argv.slice(2)],{stdio:'inherit'});process.exit(r.status??1);\n`, { mode: 0o700 });
  mkdirSync(join(directory, 'image-digests'));
  for (const [i, arch] of ['amd64', 'arm64'].entries()) writeFileSync(join(directory, 'image-digests', `${arch}.txt`), digest(String(i + 1)));
  const version = JSON.parse(readFileSync('version.json', 'utf8')).version;
  const env = { ...process.env, PATH: `${bin}:${process.env.PATH}`, AEON_REHEARSAL_REAL_GH: realGH,
    RUNNER_TEMP: directory, GITHUB_OUTPUT: join(directory, 'output'), GITHUB_STEP_SUMMARY: join(directory, 'summary'),
    GITHUB_REF: `refs/tags/v${version}`, GITHUB_REPOSITORY: 'inspr-at/paimos', VERSION: version, DIGEST: digest('5'),
    PIN_EVIDENCE: 'Offline rehearsal: no pin proposal was published.',
    RUNTIME_AMD64: digest('6'), RUNTIME_ARM64: digest('7') };
  for (const [job, name] of [
    ['image', 'Publish multi-arch index'], ['image', 'Verify pushed image attestation'],
  ]) {
    const source = spawnSync('go', ['run', 'scripts/rehearsal-step.go', job, name], { encoding: 'utf8' });
    if (source.error || source.status !== 0) throw new Error('Cannot read production step');
    const result = spawnSync('bash', ['-c', source.stdout], { env, stdio: 'inherit', timeout: 120_000 });
    if (result.error || result.status !== 0) throw new Error(`Production step rehearsal failed: ${job}/${name}`);
  }
  // Execute the exact additive completion against built rehearsal bytes. The
  // adapter only models this draft in memory and never makes a network write.
  const coordinate = { tag: `v${version}`, version, digest: digest('5'), source_sha: 'a'.repeat(40) };
  let draft = null;
  const assets = [], uploaded = new Map();
  const fixture = (program, args) => {
    if (program !== 'gh') throw new Error('Offline completion refuses this program');
    const json = value => Buffer.from(JSON.stringify(value));
    if (args[0] === 'api') {
      const path = args.at(-1);
      if (path === 'repos/inspr-at/paimos/releases?per_page=100') return json([draft ? [draft] : []]);
      if (path === 'repos/inspr-at/paimos/releases/1') {
        if (!draft) throw new Error('release completion: gh operation failed');
        return json(draft);
      }
      if (path === 'repos/inspr-at/paimos/releases/1/assets?per_page=100') return json([assets]);
      if (/^repos\/inspr-at\/paimos\/releases\/assets\/[1-9][0-9]*$/.test(path)) {
        const asset = assets.find(a => a.id === Number(path.split('/').at(-1)));
        if (asset) return uploaded.get(asset.name);
      }
    } else if (args[0] === 'release' && args[1] === 'create' && !draft) {
      checkGH(args, realGH);
      const body = readFileSync(args[args.indexOf('--notes-file') + 1], 'utf8');
      draft = { id: 1, tag_name: coordinate.tag, draft: true, prerelease: false, body };
      return Buffer.from('https://api.github.com/repos/inspr-at/paimos/releases/1\n');
    } else if (args[0] === 'release' && args[1] === 'upload' && args[2] === coordinate.tag && draft) {
      const name = args[3].split('/').at(-1);
      if (!ASSETS.includes(name) || uploaded.has(name)) throw new Error('Unexpected rehearsal upload');
      const bytes = readFileSync(args[3]);
      uploaded.set(name, bytes);
      assets.push({ id: ASSETS.indexOf(name) + 1, name, size: bytes.length, state: 'uploaded', digest: `sha256:${createHash('sha256').update(bytes).digest('hex')}` });
      return Buffer.alloc(0);
    }
    throw new Error('Unexpected offline completion invocation');
  };
  const options = { write: true, pinEvidence: env.PIN_EVIDENCE, runtimeAMD64: env.RUNTIME_AMD64, runtimeARM64: env.RUNTIME_ARM64 };
  complete(coordinate, 'dist', options, fixture);
  complete(coordinate, 'dist', options, fixture);
  const checksums = spawnSync('sha256sum', ['--check', 'SHA256SUMS'], { cwd: 'dist', stdio: 'inherit' });
  if (checksums.error || checksums.status !== 0) throw new Error('Rehearsal checksum verification failed');
  const completion = spawnSync('node', ['--test', 'scripts/release-completion.test.mjs', 'scripts/release-retry.test.mjs'], { stdio: 'inherit', timeout: 120_000 });
  if (completion.error || completion.status !== 0) throw new Error('Completion/retry rehearsal failed');
  console.log('Exact production index, attestation and draft invocations rehearsed offline; no publication');
}
