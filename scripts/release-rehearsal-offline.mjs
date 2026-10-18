// SPDX-License-Identifier: AGPL-3.0-only
// Run production index/attestation/draft shell bodies with strict offline
// adapters. Only local files are produced; gh uses a closed loopback proxy.
import { spawnSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

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
    ['assets', 'Create draft GitHub release with signed assets'],
  ]) {
    const source = spawnSync('go', ['run', 'scripts/rehearsal-step.go', job, name], { encoding: 'utf8' });
    if (source.error || source.status !== 0) throw new Error('Cannot read production step');
    const result = spawnSync('bash', ['-c', source.stdout], { env, stdio: 'inherit', timeout: 120_000 });
    if (result.error || result.status !== 0) throw new Error(`Production step rehearsal failed: ${job}/${name}`);
  }
  console.log('Exact production index, attestation and draft invocations rehearsed offline; no publication');
}
