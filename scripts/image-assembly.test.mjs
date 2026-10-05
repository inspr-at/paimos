// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import { mkdirSync, mkdtempSync, readFileSync, statSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import test from 'node:test';
import { assemble } from './assemble-image.mjs';
import { buildInputs, compile, epoch, filesIn, sha256 } from './build-image-inputs.mjs';
import { digest, runtimeManifest, timingEvidence } from './image-evidence.mjs';
import { assemblyArgs, compareRuns, reproduce } from './reproduce-image.mjs';

const version = '261005120000.0.0';
const values = { ARCH: 'arm64', VERSION: version, SOURCE_DATE_EPOCH: '1791201600' };
function fixture() {
  const path = mkdtempSync(join(tmpdir(), 'aeon-image-test-'));
  for (const [name, bytes] of Object.entries({
    'version.json': JSON.stringify({ version }), 'NOTICE': 'AGPL fixture', 'Dockerfile': 'FROM aeon-runtime',
    'scripts/Dockerfile.runtime': 'FROM alpine@sha256:fixture',
    'internal/releasehistory/data/history.json': '{"generated_at":"fixed","releases":[]}',
  })) { mkdirSync(dirname(join(path, name)), { recursive: true }); writeFileSync(join(path, name), bytes); }
  return path;
}
function compilerStub(calls = []) {
  return (file, args, cwd, extra = {}, capture = false) => {
    calls.push({ file, args, cwd, extra });
    if (file === 'npm' && args[0] === 'run') {
      mkdirSync(join(cwd, 'dist'), { recursive: true });
      writeFileSync(join(cwd, 'dist/index.html'), '<html>fixed</html>');
    }
    if (file === 'go' && args[0] === 'build') {
      const bytes = Buffer.alloc(64); bytes.set([0x7f, 0x45, 0x4c, 0x46]);
      writeFileSync(args[args.indexOf('-o') + 1], bytes);
    }
    if (capture) {
      if (file === 'git') return args[0] === 'log' ? values.SOURCE_DATE_EPOCH : 'a'.repeat(40);
      return `${file} fixed version`;
    }
  };
}
function oci(path, { arch = 'arm64', token = 'same', attest = false, duplicate = false } = {}) {
  mkdirSync(join(path, 'blobs/sha256'), { recursive: true });
  function blob(value) {
    const bytes = Buffer.from(JSON.stringify(value));
    const id = `sha256:${sha256(bytes)}`;
    writeFileSync(join(path, 'blobs/sha256', id.slice(7)), bytes);
    return { digest: id, size: bytes.length };
  }
  const config = blob({ architecture: arch, os: 'linux', token });
  const manifest = blob({ config, layers: [] });
  const manifests = [manifest];
  if (attest) manifests.push({ ...blob({ config: { digest: 'bad' }, layers: [] }), platform: { os: 'unknown', architecture: 'unknown' } });
  if (duplicate) manifests.push(manifest);
  const index = blob({ manifests });
  writeFileSync(join(path, 'index.json'), JSON.stringify({ manifests: [index] }));
  return { digest: manifest.digest, config_digest: config.digest };
}

test('reserved version, architecture and timestamp are bound before compilation', () => {
  const path = fixture();
  assert.deepEqual(buildInputs(path, values), { platform: 'linux/arm64', version, source_date_epoch: +values.SOURCE_DATE_EPOCH });
  assert.equal(buildInputs(path, { ...values, VERSION: 'dev' }).version, 'dev');
  assert.throws(() => buildInputs(path, { ...values, VERSION: '261004120000.0.0' }), /reserved/);
  assert.throws(() => buildInputs(path, { ...values, ARCH: 'x86_64' }), /ARCH/);
  for (const value of ['', '1.2', '-1', '1\n', '1e10', '00', '999999999999999999']) assert.throws(() => epoch(value), /SOURCE_DATE/);
});

test('host compilation records binary/web/history identities and normalizes mtimes', () => {
  const calls = [], path = fixture();
  const result = compile(path, values, compilerStub(calls));
  const build = calls.find(call => call.file === 'go' && call.args[0] === 'build');
  assert.deepEqual(build.extra, { CGO_ENABLED: '0', GOOS: 'linux', GOARCH: 'arm64', GOAMD64: 'v1', GOARM64: 'v8.0', GOTOOLCHAIN: 'local', GOFLAGS: '', GOWORK: 'off', SOURCE_DATE_EPOCH: values.SOURCE_DATE_EPOCH });
  for (const flag of ['-trimpath', '-buildvcs=false', 'webembed']) assert.ok(build.args.includes(flag));
  assert.equal(build.args[build.args.indexOf('-ldflags') + 1], `-X github.com/inspr-at/paimos/internal/version.Version=${version}`);
  assert.equal(calls.filter(call => call.file === 'docker').length, 0);
  assert.deepEqual(calls.filter(call => call.file === 'npm').slice(0, 2).map(call => [call.args, call.cwd]),
    [[['ci', '--no-audit', '--no-fund'], join(path, 'web')], [['run', 'build'], join(path, 'web')]]);
  assert.equal(result.history_sha256, sha256(readFileSync(join(path, 'internal/releasehistory/data/history.json'))));
  assert.equal(statSync(join(path, 'dist/image-input/paimos')).mtimeMs, +values.SOURCE_DATE_EPOCH * 1000);
  assert.equal(statSync(join(path, 'web/dist/index.html')).mtimeMs, +values.SOURCE_DATE_EPOCH * 1000);
  assert.equal(statSync(join(path, 'dist/image-input/paimos')).mode & 0o777, 0o755);
  assert.deepEqual(JSON.parse(readFileSync(join(path, 'dist/image-input/inputs.json'))), result);
});

test('a failed host build cannot emit a success manifest or continue to image assembly', () => {
  const path = fixture(), calls = [], stub = compilerStub(calls);
  assert.throws(() => compile(path, values, (file, args, ...rest) => {
    if (file === 'npm' && args[0] === 'run') throw new Error('synthetic typecheck failure');
    return stub(file, args, ...rest);
  }), /synthetic typecheck/);
  assert.ok(!calls.some(call => call.file === 'go'));
  assert.throws(() => readFileSync(join(path, 'dist/image-input/inputs.json')), { code: 'ENOENT' });
});

test('embedded web enumeration rejects symlinks', () => {
  const path = fixture();
  mkdirSync(join(path, 'output'));
  symlinkSync(join(path, 'NOTICE'), join(path, 'output/link'));
  assert.throws(() => filesIn(join(path, 'output')), /non-regular/);
});

test('OCI evidence selects the actual platform manifest rather than provenance index/config', () => {
  const path = fixture(), expected = oci(path, { attest: true });
  assert.deepEqual(runtimeManifest(path, 'linux/arm64'), expected);
  assert.throws(() => runtimeManifest(path, 'linux/amd64'), /exactly one/);
  assert.throws(() => runtimeManifest(path, 'darwin/arm64'), /platform/);
  writeFileSync(join(path, 'blobs/sha256', expected.digest.slice(7)), '{}');
  assert.throws(() => runtimeManifest(path, 'linux/arm64'), /digest or size/);
});

test('OCI evidence fails closed on duplicates, oversized JSON and unsafe digests', () => {
  const path = fixture(); oci(path, { duplicate: true });
  assert.throws(() => runtimeManifest(path, 'linux/arm64'), /exactly one/);
  writeFileSync(join(path, 'index.json'), ' '.repeat(4 * 1024 * 1024 + 1));
  assert.throws(() => runtimeManifest(path, 'linux/arm64'), /oversized/);
  for (const value of ['sha256:../../file', 'sha256:' + 'g'.repeat(64), 'sha256:' + 'a'.repeat(64) + '\n', 'abc']) assert.throws(() => digest(value), /digest/);
});

test('timing reports only successful bounded evidence; failures/skips/missing timestamps stay unknown', () => {
  const start = '2026-10-05T12:00:00.000Z', end = '2026-10-05T12:01:30.000Z';
  assert.equal(timingEvidence(start, end, 'success', 'success').within_target, true);
  assert.equal(timingEvidence(start, '2026-10-05T12:01:31Z', 'success', 'success').within_target, false);
  assert.equal(timingEvidence(start, '2026-10-05T12:01:30.001Z', 'success', 'success').within_target, false);
  for (const args of [[null, end, 'success', 'success'], [start, end, 'success', 'skipped'],
    [start, end, 'failure', 'success'], [end, start, 'success', 'success'], [start, '2026-10-05T12:01:00', 'success', 'success']]) {
    const result = timingEvidence(...args);
    assert.equal(result.assembly_smoke_s, null);
    assert.equal(result.within_target, null);
    assert.equal(result.completeness.state, 'partial');
  }
});

test('same-input proof rejects changed version/history/epoch/toolchain/web/binary and image digests', () => {
  const inputs = compile(fixture(), values, compilerStub());
  const image = { digest: 'sha256:' + 'a'.repeat(64) };
  assert.equal(compareRuns(inputs, [{ inputs, image }, { inputs, image }]), image.digest);
  for (const key of ['version', 'history_sha256', 'source_date_epoch', 'go', 'node', 'npm', 'binary_sha256', 'web']) {
    assert.throws(() => compareRuns(inputs, [{ inputs, image }, { inputs: { ...inputs, [key]: 'changed' }, image }]), /smoked inputs/);
  }
  assert.throws(() => compareRuns(inputs, [{ inputs, image }, { inputs, image: { digest: 'sha256:' + 'b'.repeat(64) } }]), /digests differ/);
  assert.throws(() => compareRuns(inputs, [{ inputs, image }]), /two/);
});

test('proof assembly disables cache/network, freezes base/epoch and exports runtime OCI digests', () => {
  const args = assemblyArgs('/tmp/source', '/tmp/base', 'sha256:' + 'a'.repeat(64), buildInputs(fixture(), values), '/tmp/image');
  for (const flag of ['--no-cache', '--network=none', '--provenance=false', `SOURCE_DATE_EPOCH=${values.SOURCE_DATE_EPOCH}`]) assert.ok(args.includes(flag));
  assert.ok(args.includes('aeon-runtime=oci-layout:///tmp/base@sha256:' + 'a'.repeat(64)));
  assert.ok(args.includes('type=oci,dest=/tmp/image,tar=false,rewrite-timestamp=true,oci-mediatypes=true'));
  assert.ok(!args.includes('--push'));
});

test('standalone smoke counterpart compiles externally and loads one caller-tagged image', () => {
  const path = fixture(), calls = [], stub = compilerStub(calls);
  const run = (file, args, cwd, ...rest) => {
    if (file === 'docker') {
      calls.push({ file, args, cwd });
      const out = args[args.indexOf('--output') + 1];
      if (out.startsWith('type=oci')) oci(out.match(/dest=([^,]+)/)[1]);
      return '';
    }
    return stub(file, args, cwd, ...rest);
  };
  assemble('aeon-smoke:fixture', { ...values, AEON_SMOKE_VERSION: version }, run, compile, path);
  const builds = calls.filter(call => call.file === 'docker');
  assert.equal(builds.length, 2);
  assert.ok(builds[0].args.includes('scripts/Dockerfile.runtime'));
  assert.ok(builds[1].args.includes('aeon-smoke:fixture'));
  assert.ok(builds[1].args.includes('type=docker,rewrite-timestamp=true,oci-mediatypes=true'));
  assert.ok(builds.every(call => !call.args.includes('--push')));
});

test('proof uses two fresh source snapshots, frozen generated history and detects mismatch with smoked image', () => {
  const path = fixture(), runtime = join(path, 'runtime'), base = oci(runtime), stub = compilerStub();
  const inputs = compile(path, values, stub);
  const calls = [], snapshots = [];
  const runner = (file, args, cwd, ...rest) => {
    calls.push({ file, args, cwd });
    if (file === 'git' && args[0] === 'status') return '';
    if (file === 'tar') {
      const target = args.at(-1);
      mkdirSync(join(target, 'internal/releasehistory/data'), { recursive: true });
      snapshots.push(target);
      return '';
    }
    if (file === 'docker' && args[1] === 'version') return 'buildx fixture version';
    if (file === 'docker') { oci(args[args.indexOf('--output') + 1].match(/dest=([^,]+)/)[1]); return ''; }
    return stub(file, args, cwd, ...rest);
  };
  const builder = cwd => {
    assert.equal(readFileSync(join(cwd, 'internal/releasehistory/data/history.json'), 'utf8'), readFileSync(join(path, 'internal/releasehistory/data/history.json'), 'utf8'));
    assert.throws(() => statSync(join(cwd, 'web/dist')), { code: 'ENOENT' });
    return inputs;
  };
  const out = join(path, 'proof.json');
  const result = reproduce(runtime, out, path, { ...values, RUNTIME_DIGEST: base.digest, SMOKE_CONFIG_DIGEST: base.config_digest }, runner, builder);
  assert.equal(result.reproducible, true);
  assert.equal(snapshots.length, 2);
  assert.notEqual(snapshots[0], snapshots[1]);
  assert.equal(calls.filter(call => call.file === 'git' && call.args[0] === 'archive').length, 1);
  assert.equal(calls.filter(call => call.file === 'docker' && call.args[1] === 'build').length, 2);
  assert.throws(() => reproduce(runtime, out, path, { ...values, SMOKE_CONFIG_DIGEST: 'sha256:' + 'b'.repeat(64) }, runner, builder), /smoked build config/);
  assert.equal(JSON.parse(readFileSync(out)).reproducible, false);
  assert.throws(() => reproduce(runtime, out, path, { ...values, GITHUB_ACTIONS: 'true' }, runner, builder), /OCI digest/);
});
