// SPDX-License-Identifier: AGPL-3.0-only
// Exercise gh's real parser with the exact release argv. All network attempts
// go to a closed loopback proxy and use a synthetic token in an empty config.
import { spawnSync } from 'node:child_process';
import { mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

export function checkGH(args, binary = 'gh') {
  const command = args.slice(0, 2).join(' ');
  if (!['release create', 'attestation verify'].includes(command)) throw new Error('Rehearsal refuses this gh command');
  if (command === 'release create' && (!args.includes('--draft') || !args.includes('--verify-tag'))) {
    throw new Error('Release draft and existing-tag guards are required');
  }
  const directory = mkdtempSync(join(tmpdir(), 'aeon-rehearsal-gh-'));
  const result = spawnSync(binary, args, { encoding: 'utf8', timeout: 20_000,
    env: { PATH: process.env.PATH, HOME: directory, GH_CONFIG_DIR: directory,
      GH_TOKEN: 'rehearsal-fixture-not-a-credential', GH_REPO: 'inspr-at/paimos',
      GH_HOST: 'github.com', GH_PROMPT_DISABLED: '1',
      HTTPS_PROXY: 'http://127.0.0.1:1', HTTP_PROXY: 'http://127.0.0.1:1',
      ALL_PROXY: 'http://127.0.0.1:1', NO_PROXY: '',
      https_proxy: 'http://127.0.0.1:1', http_proxy: 'http://127.0.0.1:1', no_proxy: '' } });
  // A parse/flag/file error must fail. Only reaching the deliberately blocked
  // network proves the actual CLI accepted the invocation. Never print bodies.
  const blocked = /127\.0\.0\.1:1.*connection refused/s.test(result.stderr) ||
    /error creating Sigstore verifier: no valid Sigstore verifiers could be initialized/.test(result.stderr);
  if (result.error || result.status === 0 || !blocked) {
    throw new Error(`gh ${command} did not reach the blocked-network boundary (flags or inputs rejected)`);
  }
  return command;
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  try { console.log(`Rehearsed ${checkGH(process.argv.slice(2), process.env.AEON_REHEARSAL_REAL_GH || 'gh')}; network disabled`); }
  catch (error) { console.error(error.message); process.exitCode = 1; }
}
