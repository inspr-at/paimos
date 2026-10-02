// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { assertDeployable, currentMainPolicy, readPolicy } from './release-withdrawals.mjs';

const version = '261001205522.0.0';
const digest = 'sha256:f478a18877714bf0e62d1ff0f541f55197ef2c80d64bd9c79f8e9ca1163295da';
const next = '261002120000.0.0';
const entry = { version, digest, ticket: 'AEON-530', reason: 'failed before release' };
const raw = JSON.stringify({ product: 'PAIMOS AEON', withdrawn_releases: [entry], unpublished_reservations: ['261001230453.0.0'] });

test('burned coordinate, immutable digest and unpublished coordinate all deny', () => {
  const policy = readPolicy(raw);
  assert.throws(() => assertDeployable(version, undefined, policy), /never deployable/);
  assert.throws(() => assertDeployable(next, digest, policy), /never deployable/);
  assert.throws(() => assertDeployable('261001230453.0.0', undefined, policy), /unpublished/);
  assert.doesNotThrow(() => assertDeployable(next, `sha256:${'a'.repeat(64)}`, policy));
});

test('committed Mild Model denial matches the recorded immutable image', () => {
  const policy = readPolicy(readFileSync(new URL('../version.json', import.meta.url), 'utf8'));
  assert.ok(policy.withdrawn.some(w => w.version === version && w.digest === digest));
  assert.throws(() => assertDeployable(version, digest, policy), /withdrawn/);
});

test('malformed and ambiguous ledgers are rejected', () => {
  for (const invalid of ['{', JSON.stringify({ product: 'other' }), ...[
    { withdrawn_releases: {} }, { unpublished_reservations: ['invalid'] },
    { withdrawn_releases: [entry, entry] },
    { withdrawn_releases: [entry], unpublished_reservations: [version] },
    { withdrawn_releases: [{ ...entry, digest: 'latest' }] },
    { withdrawn_releases: [{ ...entry, version: '260229120000.0.0' }] },
  ].map(fields => JSON.stringify({ product: 'PAIMOS AEON', ...fields }))]) assert.throws(() => readPolicy(invalid), /release policy:/);
});

test('live lookups pin the ledger to exact current main and verify its blob', () => {
  const sha = 'a'.repeat(40), calls = [];
  const blob = createHash('sha1').update(`blob ${Buffer.byteLength(raw)}\0`).update(raw).digest('hex');
  const get = path => {
    calls.push(path);
    return calls.length === 1 ? { ref: 'refs/heads/main', object: { type: 'commit', sha } } : { type: 'file', encoding: 'base64', content: Buffer.from(raw).toString('base64'), sha: blob };
  };
  assert.throws(() => assertDeployable(version, digest, currentMainPolicy(get)), /withdrawn/);
  assert.deepEqual(calls, ['repos/inspr-at/paimos/git/ref/heads/main', `repos/inspr-at/paimos/contents/version.json?ref=${sha}`]);
  assert.throws(() => currentMainPolicy(() => ({})), /unavailable/);
});
