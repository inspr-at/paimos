// SPDX-License-Identifier: AGPL-3.0-only
import { spawnSync } from 'node:child_process';
import { lstatSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
import { root, sha256 } from './build-image-inputs.mjs';
import { secondsBetween } from './release-timing.mjs';

export const runtimeRecipe = 'scripts/Dockerfile.runtime';

export function digest(value) {
  if (typeof value !== 'string' || !/^sha256:[a-f0-9]{64}$/.test(value)) throw new Error('invalid OCI digest');
  return value;
}
function jsonFile(path) {
  const info = lstatSync(path);
  if (!info.isFile() || info.size > 4 * 1024 * 1024) throw new Error('invalid or oversized OCI JSON');
  return readFileSync(path);
}
export function runtimeManifest(directory, platform) {
  if (!/^linux\/(amd64|arm64)$/.test(platform)) throw new Error('invalid OCI platform');
  const matches = [];
  let visited = 0;
  function visit(descriptor, depth) {
    if (++visited > 32 || depth > 3) throw new Error('OCI index exceeds bounds');
    const id = digest(descriptor.digest);
    const raw = jsonFile(join(directory, 'blobs/sha256', id.slice(7)));
    if (`sha256:${sha256(raw)}` !== id || raw.length !== descriptor.size) throw new Error('OCI blob digest or size mismatch');
    const value = JSON.parse(raw);
    if (Array.isArray(value.manifests)) {
      for (const child of value.manifests) visit(child, depth + 1);
    } else if (value.config && Array.isArray(value.layers)) {
      // Attestation envelopes deliberately have unknown/unknown platform.
      if (descriptor.platform?.os === 'unknown') return;
      const configID = digest(value.config.digest);
      const configRaw = jsonFile(join(directory, 'blobs/sha256', configID.slice(7)));
      if (`sha256:${sha256(configRaw)}` !== configID || configRaw.length !== value.config.size) throw new Error('OCI config digest or size mismatch');
      const config = JSON.parse(configRaw);
      if (`${config.os}/${config.architecture}` === platform) matches.push({ digest: id, config_digest: configID });
    } else throw new Error('invalid OCI manifest');
  }
  const index = JSON.parse(jsonFile(join(directory, 'index.json')));
  if (!Array.isArray(index.manifests)) throw new Error('missing OCI index manifests');
  for (const entry of index.manifests) visit(entry, 0);
  if (matches.length !== 1) throw new Error('expected exactly one runtime platform manifest');
  return matches[0];
}
// Layer diff IDs hash the uncompressed layers, so they identify the runtime
// bytes independently of how an exporter compressed or wrapped them.
export function runtimeClosure(directory, platform) {
  const manifest = runtimeManifest(directory, platform);
  const config = JSON.parse(jsonFile(join(directory, 'blobs/sha256', manifest.config_digest.slice(7))));
  const layers = config.rootfs?.diff_ids;
  if (!Array.isArray(layers) || !layers.length || layers.length > 127) throw new Error('OCI config lacks layer diff IDs');
  return { ...manifest, diff_ids: layers.map(digest) };
}
// What this build's runtime consists of: the bytes of the prepared layout and
// the recipe they came from. Every build prepares its own closure, so these
// digests are the record of what one release shipped.
export function runtimeEvidence(directory, platform, cwd = root) {
  return { platform, ...runtimeClosure(directory, platform), recipe_sha256: sha256(readFileSync(join(cwd, runtimeRecipe))) };
}
export function runtimeSummary(evidence) {
  return ['', `### Runtime closure (${evidence.platform})`, '',
    `- runtime manifest: \`${evidence.digest}\``, `- config: \`${evidence.config_digest}\``,
    `- layer diff IDs: ${evidence.diff_ids.map(id => `\`${id}\``).join(', ')}`,
    `- recipe \`${runtimeRecipe}\` sha256 \`${evidence.recipe_sha256}\``, ''].join('\n');
}
// Raw registry manifest bytes, bounded in size and time. Read-only.
export function inspectRaw(reference, spawn = spawnSync) {
  if (typeof reference !== 'string' || !/^[a-z0-9][a-z0-9./_-]*(:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?(@sha256:[a-f0-9]{64})?$/.test(reference)) throw new Error('invalid image reference');
  const result = spawn('docker', ['buildx', 'imagetools', 'inspect', '--raw', reference], {
    stdio: ['ignore', 'pipe', 'pipe'], timeout: 60_000, maxBuffer: 4 * 1024 * 1024,
  });
  if (result.error || result.status !== 0) {
    const reason = String(result.error?.code ?? result.stderr ?? '').trim().split('\n').at(-1).slice(0, 200);
    throw new Error(`cannot read ${reference}${reason ? `: ${reason}` : ''}`);
  }
  return Buffer.from(result.stdout);
}
export function timingEvidence(start, end, assembly, smoke, identity = {}) {
  const reasons = [];
  if (assembly !== 'success') reasons.push(`assembly ${assembly || 'missing'}`);
  if (smoke !== 'success') reasons.push(`smoke ${smoke || 'missing'}`);
  let seconds = null;
  try {
    seconds = secondsBetween(start, end);
    if (!Number.isFinite(seconds) || seconds < 0) throw new Error('invalid timing interval');
    // Use release-timing's validation/unknown conventions, but keep subsecond
    // precision so 90.001 s cannot pass a strict 90 s budget by rounding.
    seconds = (Date.parse(end) - Date.parse(start)) / 1000;
    if (!Number.isFinite(seconds) || seconds < 0) throw new Error('invalid timing interval');
  } catch { reasons.push('missing, invalid or reversed interval'); }
  if (reasons.length) seconds = null;
  return {
    schema: 'aeon.image-timing.v1', ...identity, started_at: start || null, completed_at: end || null,
    assembly_outcome: assembly || null, smoke_outcome: smoke || null,
    assembly_smoke_s: seconds, target_s: 90, within_target: seconds == null ? null : seconds <= 90,
    completeness: { state: reasons.length ? 'partial' : 'complete', reasons },
    boundary: 'COPY assembly + image load + immutable-ID resolution + complete smoke (including cleanup)',
    excludes: ['host compilation', 'runtime closure preparation', 'setup', 'reproducibility proof', 'provenance export'],
  };
}
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const [mode, path, platform] = process.argv.slice(2);
    if (mode === 'runtime') {
      const result = runtimeEvidence(path, platform);
      // digest binds the later steps; runtime_<arch> is this leg's job output.
      if (process.env.GITHUB_OUTPUT) writeFileSync(process.env.GITHUB_OUTPUT, `digest=${result.digest}\nruntime_${platform.split('/')[1]}=${result.digest}\n`, { flag: 'a' });
      if (process.env.GITHUB_STEP_SUMMARY) writeFileSync(process.env.GITHUB_STEP_SUMMARY, runtimeSummary(result), { flag: 'a' });
      console.log(JSON.stringify(result));
    } else if (mode === 'start') writeFileSync(path, new Date().toISOString() + '\n');
    else if (mode === 'timing') {
      let start = null;
      try {
        const info = lstatSync(path);
        if (!info.isFile() || info.size > 128) throw new Error('invalid or oversized timing start');
        start = readFileSync(path, 'utf8').trim();
      } catch { /* Unknown, never zero. */ }
      const report = timingEvidence(start, new Date().toISOString(), process.env.ASSEMBLY_OUTCOME, process.env.SMOKE_OUTCOME, {
        platform, sha: process.env.GITHUB_SHA || null, run_id: process.env.GITHUB_RUN_ID || null,
        run_attempt: process.env.GITHUB_RUN_ATTEMPT || null, runner: process.env.RUNNER_ENVIRONMENT || 'local',
      });
      writeFileSync(path + '.json', JSON.stringify(report, null, 2) + '\n');
      if (process.env.GITHUB_STEP_SUMMARY) writeFileSync(process.env.GITHUB_STEP_SUMMARY,
        `\nImage assembly + smoke (${platform}): ${report.assembly_smoke_s ?? 'unknown'} s; target 90 s; within_target=${report.within_target ?? 'unknown'}; evidence ${report.completeness.state}.\n`, { flag: 'a' });
      console.log(JSON.stringify(report));
      if (report.completeness.state !== 'complete') throw new Error(`incomplete image timing evidence: ${report.completeness.reasons.join('; ')}`);
      if (report.within_target === false) console.log(`::warning::Image assembly + smoke (${platform}) took ${report.assembly_smoke_s} s, exceeding the 90 s acceptance target.`);
    } else throw new Error('usage: image-evidence.mjs runtime LAYOUT PLATFORM | start PATH | timing PATH PLATFORM');
  } catch (error) { console.error(error.message); process.exitCode = 1; }
}
