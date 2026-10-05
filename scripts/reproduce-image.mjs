// SPDX-License-Identifier: AGPL-3.0-only
// Two independent host compilations, each with its own empty Go build cache,
// and uncached COPY assemblies. They share the runner, the go.sum-verified
// module downloads, the lockfile-verified npm cache and an explicitly frozen
// runtime closure/history fixture; the proof claims nothing beyond that.
import assert from 'node:assert/strict';
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { buildInputs, command, compile, root } from './build-image-inputs.mjs';
import { digest, runtimeManifest } from './image-evidence.mjs';
import { runtimeEvidence } from './runtime-closure.mjs';

export function compareRuns(expected, runs) {
  if (runs.length !== 2) throw new Error('exactly two clean runs required');
  for (const run of runs) assert.deepEqual(run.inputs, expected, 'rebuild inputs differ from smoked inputs');
  for (const run of runs) digest(run.image.digest);
  if (runs[0].image.digest !== runs[1].image.digest) throw new Error('runtime platform image digests differ');
  return runs[0].image.digest;
}
export function assemblyArgs(cwd, runtimePath, runtimeDigest, inputs, output) {
  digest(runtimeDigest);
  return ['buildx', 'build', '--no-cache', '--network=none', '--platform', inputs.platform,
    '--build-context', `aeon-runtime=oci-layout://${resolve(runtimePath)}@${runtimeDigest}`,
    '--build-arg', `VERSION=${inputs.version}`, '--build-arg', `SOURCE_DATE_EPOCH=${inputs.source_date_epoch}`,
    '--provenance=false',
    '--output', `type=oci,dest=${output},tar=false,rewrite-timestamp=true,oci-mediatypes=true`, cwd];
}
export function reproduce(runtimePath, output, cwd = root, values = process.env, run = command, build = compile) {
  const inputs = buildInputs(cwd, values, run);
  if (values.GITHUB_ACTIONS === 'true') {
    digest(values.RUNTIME_DIGEST);
    digest(values.SMOKE_IMAGE);
    digest(values.SMOKE_CONFIG_DIGEST);
  }
  if (run('git', ['status', '--porcelain', '--untracked-files=no'], cwd, {}, true)) throw new Error('proof requires a clean tracked checkout');
  const sourceSHA = run('git', ['rev-parse', 'HEAD'], cwd, {}, true);
  const tree = run('git', ['rev-parse', 'HEAD^{tree}'], cwd, {}, true);
  const runtime = runtimeEvidence(runtimePath, inputs.platform, values, cwd);
  if (values.RUNTIME_DIGEST && runtime.digest !== digest(values.RUNTIME_DIGEST)) throw new Error('runtime closure changed');
  const expected = JSON.parse(readFileSync(join(cwd, 'dist/image-input/inputs.json'), 'utf8'));
  const parent = join(cwd, 'tmp');
  mkdirSync(parent, { recursive: true });
  const temp = mkdtempSync(join(parent, 'image-repro-'));
  const archive = join(temp, 'source.tar');
  // No worktree creation, git metadata copying, network history regeneration,
  // dependency/output copying, or broad copy of ignored files (including secrets).
  run('git', ['archive', '--format=tar', '-o', archive, sourceSHA], cwd);
  // One new, still absent build cache per run: neither run can replay the
  // smoked compilation or the other run, so run 2 is a real second compile.
  const caches = values.RUNNER_TEMP ? mkdtempSync(join(values.RUNNER_TEMP, 'image-repro-go-')) : temp;
  const runs = [];
  let failure = null;
  try {
    for (let n = 1; n <= 2; n++) {
      const snapshot = join(temp, `run-${n}`);
      mkdirSync(snapshot);
      run('tar', ['-xf', archive, '-C', snapshot], cwd);
      copyFileSync(join(cwd, 'internal/releasehistory/data/history.json'), join(snapshot, 'internal/releasehistory/data/history.json'));
      const goCache = join(caches, `go-build-${n}`);
      const built = build(snapshot, { ...values, SOURCE_TREE: tree, SOURCE_DATE_EPOCH: String(inputs.source_date_epoch), GOCACHE: goCache }, run);
      const layout = join(temp, `image-${n}`);
      run('docker', assemblyArgs(snapshot, runtimePath, runtime.digest, inputs, layout), snapshot);
      runs.push({ run: n, go_build_cache: goCache, inputs: built, image: runtimeManifest(layout, inputs.platform) });
    }
    compareRuns(expected, runs);
    // The daemon's runnable ID is resolved separately. Compare BuildKit's
    // config digest here; some Docker stores expose a different runnable ID.
    if (values.SMOKE_CONFIG_DIGEST && runs[0].image.config_digest !== digest(values.SMOKE_CONFIG_DIGEST)) throw new Error('rebuilt image config differs from smoked build config');
  } catch (error) { failure = error.message; }
  const evidence = {
    schema: 'aeon.image-reproducibility.v1', sha: sourceSHA, tree, inputs: expected, runtime_base: runtime,
    smoke_image_id: values.SMOKE_IMAGE || null, smoke_config_digest: values.SMOKE_CONFIG_DIGEST || null,
    buildx: run('docker', ['buildx', 'version'], cwd, {}, true),
    runtime_scope: 'Frozen OCI closure; APK repository resolution occurred once before these two runs',
    proof_scope: 'Same runner, same frozen runtime closure, same history fixture; each run compiles Go in its own empty build cache',
    runs, reproducible: failure ? false : true, failure,
    completeness: { state: failure ? 'partial' : 'complete', reasons: failure ? [failure] : [] },
    retained_fixtures: temp,
  };
  writeFileSync(output, JSON.stringify(evidence, null, 2) + '\n');
  if (failure) throw new Error(failure);
  return evidence;
}
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const [runtime, output] = process.argv.slice(2);
    if (!runtime || !output) throw new Error('usage: reproduce-image.mjs RUNTIME_LAYOUT EVIDENCE.json');
    console.log(JSON.stringify(reproduce(runtime, output)));
  } catch (error) { console.error(error.message); process.exitCode = 1; }
}
