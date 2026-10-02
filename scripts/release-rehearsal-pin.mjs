// SPDX-License-Identifier: AGPL-3.0-only
// Production pin-bot dry-run against an in-memory source API and local pin.
// Live tag/attestation checks cannot succeed before publication; keep them live
// in release.yml, and exercise their exact gh arguments without network here.
import { mkdtempSync, writeFileSync, readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { proposePin, attestationArgs, SOURCE } from './release-pin-pr.mjs';
import { checkGH } from './release-rehearsal-gh.mjs';

const version = JSON.parse(readFileSync('version.json', 'utf8')).version;
const sha = process.env.GITHUB_SHA;
const tagSHA = 'b'.repeat(40);
const digest = `sha256:${'c'.repeat(64)}`;
const pinFile = join(mkdtempSync(join(tmpdir(), 'aeon-rehearsal-pin-')), 'snapshot.nix');
writeFileSync(pinFile, `image = "ghcr.io/inspr-at/aeon:260909113550.0.0@sha256:${'d'.repeat(64)}";\n`);
const inputs = { VERSION: version, DIGEST: digest, GITHUB_SHA: sha, GITHUB_REPOSITORY: SOURCE,
  GITHUB_EVENT_NAME: 'push', GITHUB_REF: `refs/tags/v${version}`, GH_TOKEN: 'rehearsal-fixture' };
const result = await proposePin(inputs, { pinFile }, {
  verify: () => checkGH(attestationArgs(version, digest, sha)),
  request: async (_token, method, path) => {
    if (method !== 'GET') throw new Error('Rehearsal refuses API mutations');
    const data = {
      [`/repos/${SOURCE}/git/ref/tags/v${version}`]: { ref: inputs.GITHUB_REF, object: { type: 'tag', sha: tagSHA } },
      [`/repos/${SOURCE}/git/tags/${tagSHA}`]: { sha: tagSHA, tag: `v${version}`, object: { type: 'commit', sha } },
      [`/repos/${SOURCE}/git/ref/heads/main`]: { ref: 'refs/heads/main', object: { type: 'commit', sha } },
      [`/repos/${SOURCE}/compare/${sha}...${sha}`]: { status: 'identical', base_commit: { sha }, merge_base_commit: { sha } },
    }[path];
    if (!data) throw new Error('Rehearsal refuses unexpected API reads');
    return { status: 200, data };
  },
});
if (result.status !== 'dry-run' || !result.changed) throw new Error('Pin dry-run did not produce its proposal');
console.log('Pin-bot dry-run passed; source API, attestation and target pin are fixtures; no writes');
