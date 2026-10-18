// SPDX-License-Identifier: AGPL-3.0-only
// Standalone smoke counterpart: all compilation remains outside Docker.
import { existsSync, mkdirSync, mkdtempSync } from 'node:fs';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
import { buildInputs, command, compile, root } from './build-image-inputs.mjs';
import { runtimeManifest } from './image-evidence.mjs';

export function assemble(tag, values = process.env, run = command, build = compile, cwd = root) {
  if (!/^[a-z0-9][a-z0-9_.:/-]*$/.test(tag || '')) throw new Error('invalid local image tag');
  const arch = values.ARCH || run('go', ['env', 'GOARCH'], cwd, {}, true);
  const config = { ...values, ARCH: arch, VERSION: values.AEON_SMOKE_VERSION || 'dev' };
  const inputs = buildInputs(cwd, config, run);
  if (!existsSync(join(cwd, 'internal/releasehistory/data/history.json'))) {
    run('go', ['run', './internal/releasehistory/generate', '-repo', '.', '-repository', 'inspr-at/paimos', '-offline'], cwd);
  }
  build(cwd, { ...config, SOURCE_DATE_EPOCH: String(inputs.source_date_epoch) }, run);
  mkdirSync(join(cwd, 'tmp'), { recursive: true });
  const runtimePath = mkdtempSync(join(cwd, 'tmp/image-runtime-'));
  run('docker', ['buildx', 'build', '--file', 'scripts/Dockerfile.runtime', '--platform', inputs.platform,
    '--provenance=false', '--build-arg', 'SOURCE_DATE_EPOCH=0',
    '--output', `type=oci,dest=${runtimePath},tar=false,rewrite-timestamp=true`, '.'], cwd);
  const runtime = runtimeManifest(runtimePath, inputs.platform);
  run('docker', ['buildx', 'build', '--network=none', '--platform', inputs.platform,
    '--build-context', `aeon-runtime=oci-layout://${runtimePath}@${runtime.digest}`,
    '--build-arg', `VERSION=${inputs.version}`, '--build-arg', `SOURCE_DATE_EPOCH=${inputs.source_date_epoch}`,
    '--provenance=false', '--load', '--tag', tag,
    '--output', 'type=docker,rewrite-timestamp=true,oci-mediatypes=true', '.'], cwd);
  return inputs;
}
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try { assemble(process.argv[2]); }
  catch (error) { console.error(error.message); process.exitCode = 1; }
}
