// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import test from 'node:test';
import { checkGH } from './release-rehearsal-gh.mjs';
import { attestationArgs } from './release-pin-pr.mjs';

const args = attestationArgs('261002004358.0.0', `sha256:${'a'.repeat(64)}`, 'b'.repeat(40));
test('real gh accepts the pin-bot attestation argv before blocked network', () => {
  assert.equal(checkGH(args), 'attestation verify');
});
test('the AEON-526 mutually exclusive identity flags fail the real parser', () => {
  assert.throws(() => checkGH([...args, '--cert-identity', 'fixture']), /flags or inputs rejected/);
});
test('unknown flags and missing assets fail instead of becoming rehearsal success', () => {
  assert.throws(() => checkGH([...args, '--aeon-invalid-flag']), /flags or inputs rejected/);
  assert.throws(() => checkGH(['release', 'create', 'v261002004358.0.0', '/aeon-missing-file', '--draft', '--verify-tag']), /flags or inputs rejected/);
});
test('publication, upload, tagging and missing release guards are refused', () => {
  for (const argv of [['release', 'edit', '--draft=false'], ['release', 'upload'], ['api', '-X', 'POST'],
    ['release', 'create', '--draft'], ['release', 'create', '--verify-tag']]) assert.throws(() => checkGH(argv), /refuses|required/);
});
