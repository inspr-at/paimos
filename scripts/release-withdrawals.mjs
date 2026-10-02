// SPDX-License-Identifier: AGPL-3.0-only
// AEON-530: deployment must consult the current approved source-main ledger,
// even when checking an older tag/rollback. Coordinates and digests both deny.
import { readFileSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { pathToFileURL } from 'node:url';
import { validCalendarVersion } from './release-calendar.mjs';

const fail = message => { throw new Error(`release policy: ${message}`); };
export const policyPath = new URL('../version.json', import.meta.url);

export function readPolicy(raw = readFileSync(policyPath, 'utf8')) {
  let policy;
  try { policy = JSON.parse(raw); } catch { fail('unreadable withdrawal ledger'); }
  if (policy?.product !== 'PAIMOS AEON') fail('withdrawal ledger belongs to another product');
  const withdrawn = policy.withdrawn_releases ?? [];
  const unpublished = policy.unpublished_reservations ?? [];
  if (!Array.isArray(withdrawn) || !Array.isArray(unpublished)) fail('invalid withdrawal ledger');
  const seen = new Set();
  for (const w of withdrawn) {
    if (!validCalendarVersion(w?.version) || !/^sha256:[a-f0-9]{64}$/.test(w?.digest) ||
        !/^AEON-[1-9][0-9]*$/.test(w?.ticket) || typeof w.reason !== 'string' || !w.reason.trim() || seen.has(w.version)) fail('invalid or duplicate withdrawal');
    seen.add(w.version);
  }
  for (const entry of unpublished) {
    const version = typeof entry === 'string' ? entry.replace(/^v/, '') : '';
    if (!validCalendarVersion(version) || seen.has(version)) fail('invalid or duplicate unpublished reservation');
    seen.add(version);
  }
  return { withdrawn, unpublished: unpublished.map(v => v.replace(/^v/, '')) };
}

export function assertDeployable(version, digest, policy = readPolicy()) {
  if (!validCalendarVersion(version) || (digest !== undefined && !/^sha256:[a-f0-9]{64}$/.test(digest))) fail('invalid coordinate or digest');
  if (policy.unpublished.includes(version)) fail('unpublished coordinate is never deployable');
  if (policy.withdrawn.some(w => w.version === version || (digest !== undefined && w.digest === digest))) fail('withdrawn coordinate or image digest is never deployable');
}

// Fetch a coherent source-main snapshot; an old tag must not supply the ledger.
export function currentMainPolicy(get = path => {
  const result = spawnSync('gh', ['api', '--method', 'GET', path], { encoding: 'utf8', timeout: 30_000, stdio: ['ignore', 'pipe', 'ignore'] });
  if (result.error || result.status !== 0) fail('current main lookup failed');
  try { return JSON.parse(result.stdout); } catch { fail('current main response unreadable'); }
}) {
  const main = get('repos/inspr-at/paimos/git/ref/heads/main');
  if (main.ref !== 'refs/heads/main' || main.object?.type !== 'commit' || !/^[a-f0-9]{40}$/.test(main.object.sha)) fail('current main is unavailable');
  const file = get(`repos/inspr-at/paimos/contents/version.json?ref=${main.object.sha}`);
  if (file.type !== 'file' || file.encoding !== 'base64' || typeof file.content !== 'string') fail('current main ledger is unavailable');
  const bytes = Buffer.from(file.content, 'base64');
  const text = bytes.toString('utf8');
  const blob = createHash('sha1').update(`blob ${bytes.length}\0`).update(bytes).digest('hex');
  if (!bytes.equals(Buffer.from(text)) || blob !== file.sha) fail('current main ledger blob mismatch');
  return readPolicy(text);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const [mode, version, ...args] = process.argv.slice(2);
    if (!['coordinate', 'image'].includes(mode) || !version || args.length !== (mode === 'image' ? 2 : 1)) fail('usage: release-withdrawals.mjs coordinate VERSION LEDGER | image VERSION DIGEST LEDGER (LEDGER may be --current-main)');
    const [digest, file] = mode === 'image' ? args : [undefined, args[0]];
    const policy = file === '--current-main' ? currentMainPolicy() : readPolicy(readFileSync(file, 'utf8'));
    assertDeployable(version, digest, policy);
    console.log('release policy: coordinate and digest are not withdrawn');
  } catch (error) { console.error(error.message); process.exitCode = 1; }
}
