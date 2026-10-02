// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { existsSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { ASSETS, BINARIES, STATIC_BINARIES, REPOSITORY, complete, prepare, restore, checksumMap } from './release-completion.mjs';

const version = '261002120000.0.0', tag = `v${version}`, source = 'a'.repeat(40), tooling = 'b'.repeat(40);
const digest = `sha256:${'c'.repeat(64)}`;
const env = () => ({ GITHUB_REPOSITORY: REPOSITORY, GITHUB_EVENT_NAME: 'workflow_dispatch', GITHUB_REF: 'refs/heads/main', GITHUB_SHA: tooling, RELEASE_TAG: tag, DIGEST: digest });
const release = { tag, version, digest, source_sha: source };
const sha = bytes => createHash('sha256').update(bytes).digest('hex');
function fixture(names = [], { published = false } = {}) {
  const dir = mkdtempSync(join(tmpdir(), 'aeon-completion-'));
  const bytes = new Map(BINARIES.map(name => [name, Buffer.from(`original signed/source bytes: ${name}`)]));
  bytes.set('SHA256SUMS', Buffer.from(BINARIES.map(name => `${sha(bytes.get(name))}  ${name}\n`).join('')));
  for (const name of BINARIES) writeFileSync(join(dir, name), bytes.get(name));
  const makeAsset = name => ({ id: ASSETS.indexOf(name) + 1, name, state: 'uploaded', size: bytes.get(name).length, digest: `sha256:${sha(bytes.get(name))}` });
  const state = {
    release: names.length ? { id: 99, tag_name: tag, draft: !published, prerelease: false, body: `Digest: ${digest}` } : null,
    assets: names.map(makeAsset), refType: 'tag', tagSource: source, ancestry: 'ahead', indexDigest: digest,
    index: { schemaVersion: 2, mediaType: 'application/vnd.oci.image.index.v1+json', manifests: [] },
  };
  for (const [i, arch] of ['amd64', 'arm64'].entries()) {
    const runtime = `sha256:${String(i + 1).repeat(64)}`;
    state.index.manifests.push({ digest: runtime, size: 10, mediaType: 'application/vnd.oci.image.manifest.v1+json', platform: { os: 'linux', architecture: arch } });
    state.index.manifests.push({ digest: `sha256:${String(i + 3).repeat(64)}`, size: 10, mediaType: 'application/vnd.oci.image.manifest.v1+json', platform: { os: 'unknown', architecture: 'unknown' }, annotations: { 'vnd.docker.reference.type': 'attestation-manifest', 'vnd.docker.reference.digest': runtime } });
  }
  const calls = [];
  const run = (program, args) => {
    calls.push([program, ...args]);
    if (state.before) state.before(program, args);
    const json = value => Buffer.from(JSON.stringify(value));
    if (program === 'docker') return json(state.index);
    if (args[0] === 'attestation') {
      if (state.unattested) throw new Error('fixture attestation rejected');
      return Buffer.from('verified');
    }
    if (args[0] === 'api') {
      const path = args.at(-1);
      if (path.endsWith(`/git/ref/tags/${tag}`)) return json({ ref: `refs/tags/${tag}`, object: { type: state.refType, sha: 'd'.repeat(40) } });
      if (path.includes('/git/tags/')) return json({ sha: 'd'.repeat(40), tag, object: { type: 'commit', sha: state.tagSource } });
      if (path.endsWith('/git/ref/heads/main')) return json({ ref: 'refs/heads/main', object: { type: 'commit', sha: tooling } });
      if (path.includes('/compare/')) return json({ status: state.ancestry, base_commit: { sha: source }, merge_base_commit: { sha: source } });
      if (path.includes('/contents/version.json')) return json({ type: 'file', encoding: 'base64', content: Buffer.from(JSON.stringify({ product: 'PAIMOS AEON', version: state.version ?? version, version_scheme: 'inspr-calver-3', ...(path.endsWith(tooling) ? { unpublished_reservations: state.retired ?? [], withdrawn_releases: state.withdrawn ?? [] } : {}) })).toString('base64') });
      if (path.startsWith('orgs/')) return json([[{ name: state.indexDigest, metadata: { container: { tags: [version] } } }]]);
      if (path.includes('/releases/assets/')) return bytes.get(ASSETS[Number(path.split('/').at(-1)) - 1]);
      if (path.includes('/releases/99/assets')) return json([state.assets]);
      if (path.includes('/releases?')) return json([state.release ? [state.release] : []]);
    }
    if (args[0] === 'release' && args[1] === 'create') {
      state.release = { id: 99, tag_name: tag, draft: true, prerelease: false, body: readFileSync(args[args.indexOf('--notes-file') + 1], 'utf8') };
      return Buffer.alloc(0);
    }
    if (args[0] === 'release' && args[1] === 'upload') {
      const name = args[3].split('/').at(-1);
      assert.ok(!state.assets.some(asset => asset.name === name));
      bytes.set(name, readFileSync(args[3]));
      state.assets.push(makeAsset(name));
      return Buffer.alloc(0);
    }
    throw new Error(`unexpected fixture command: ${program} ${args.join(' ')}`);
  };
  return { state, calls, run, bytes, dir };
}
const writes = f => f.calls.filter(call => call[0] === 'gh' && call[1] === 'release');

test('main tooling proves annotated tag source, current index and original hosted signer', () => {
  const f = fixture();
  assert.deepEqual(prepare(env(), f.run), release);
  const verify = f.calls.find(call => call[1] === 'attestation');
  for (const flag of ['--source-digest', '--signer-digest']) assert.equal(verify[verify.indexOf(flag) + 1], source);
  assert.equal(verify[verify.indexOf('--source-ref') + 1], `refs/tags/${tag}`);
  assert.ok(verify.includes('--deny-self-hosted-runners'));
  assert.ok(!verify.includes(tooling));
  assert.deepEqual(writes(f), []);
});

for (const [name, mutate] of [
  ['branch dispatch', input => input.GITHUB_REF = 'refs/heads/work/bad'],
  ['PR', input => input.GITHUB_EVENT_NAME = 'pull_request'],
  ['foreign repository', input => input.GITHUB_REPOSITORY = 'other/repo'],
  ['invalid tag', input => input.RELEASE_TAG = 'v260231120000.0.0'],
  ['input injection', input => input.RELEASE_TAG = 'v261002120000.0.0\nx=y'],
  ['missing digest', input => input.DIGEST = ''],
]) test(`${name} fails before any command`, () => {
  const f = fixture(), input = env(); mutate(input);
  assert.throws(() => prepare(input, f.run));
  assert.equal(f.calls.length, 0);
});

for (const [name, mutate] of [
  ['lightweight tag', state => state.refType = 'commit'],
  ['different source', state => state.tagSource = tooling],
  ['off-main source', state => state.ancestry = 'diverged'],
  ['wrong version.json', state => state.version = '261001120000.0.0'],
  ['changed index', state => state.indexDigest = `sha256:${'f'.repeat(64)}`],
  ['missing architecture', state => state.index.manifests.pop()],
  ['unattested index', state => state.unattested = true],
  ['retired coordinate', state => state.retired = [version]],
  ['withdrawn coordinate', state => state.withdrawn = [{ version, digest }]],
  ['withdrawn digest under another tag', state => state.withdrawn = [{ version: '261002110000.0.0', digest }]],
  ['malformed withdrawal policy', state => state.withdrawn = [{ version: 'bad', digest }]],
]) test(`${name} cannot reach asset writes`, () => {
  const f = fixture(); mutate(f.state);
  assert.throws(() => prepare(env(), f.run));
  assert.deepEqual(writes(f), []);
});

test('normal tag caller is supported, with exact push SHA required', () => {
  const f = fixture();
  const input = { ...env(), GITHUB_EVENT_NAME: 'push', GITHUB_REF: `refs/tags/${tag}`, GITHUB_SHA: source };
  assert.deepEqual(prepare(input, f.run), release);
  assert.throws(() => prepare({ ...input, GITHUB_SHA: tooling }, f.run), /source differs/);
});

test('read failures never become absence', () => {
  const f = fixture(); f.state.before = () => { throw new Error('fixture API down'); };
  assert.throws(() => prepare(env(), f.run));
  assert.throws(() => complete(release, f.dir, { write: true }, f.run));
  assert.deepEqual(writes(f), []);
});

test('missing release is planned read-only; opt-in creates a draft and all nine assets', () => {
  const f = fixture();
  assert.equal(complete(release, f.dir, {}, f.run).status, 'planned');
  assert.deepEqual(writes(f), []);
  assert.equal(complete(release, f.dir, { write: true }, f.run).status, 'complete');
  const create = writes(f)[0];
  for (const flag of ['--draft', '--verify-tag', '--target', source, '--notes-file']) assert.ok(create.includes(flag));
  assert.equal(writes(f).filter(call => call[2] === 'upload').length, 9);
  assert.equal(f.state.release.draft, true);
  const count = writes(f).length;
  assert.equal(complete(release, f.dir, { write: true }, f.run).status, 'complete');
  assert.equal(writes(f).length, count);
});

test('partial draft reuses the original signed bytes and adds only missing assets', () => {
  const name = BINARIES[0], f = fixture([name]);
  const original = Buffer.from(f.bytes.get(name));
  writeFileSync(join(f.dir, name), 'new nondeterministic signature');
  assert.equal(restore(release, name, f.dir, f.run), true);
  assert.ok(readFileSync(join(f.dir, name)).equals(original));
  writeFileSync(join(f.dir, name), 'rebuilt differently');
  complete(release, f.dir, { write: true }, f.run);
  assert.ok(f.bytes.get(name).equals(original));
  assert.equal(writes(f).length, 8);
  assert.ok(writes(f).every(call => call[2] === 'upload' && !call.includes('--clobber') && !call[4].endsWith(`/${name}`)));
});

for (const name of STATIC_BINARIES) {
  for (const withChecksum of [false, true]) test(`pre-seeded ${name} differing from tagged rebuild is refused${withChecksum ? ' even with matching uploaded checksums' : ''}`, () => {
    const f = fixture(withChecksum ? [name, 'SHA256SUMS'] : [name]);
    // Simulate a completed upload with valid API integrity and attacker-chosen bytes.
    f.bytes.set(name, Buffer.from(`pre-seeded unsigned binary: ${name}`));
    f.bytes.set('SHA256SUMS', Buffer.from(BINARIES.map(binary => `${sha(f.bytes.get(binary))}  ${binary}\n`).join('')));
    for (const asset of f.state.assets) {
      asset.size = f.bytes.get(asset.name).length;
      asset.digest = `sha256:${sha(f.bytes.get(asset.name))}`;
    }
    const rebuilt = readFileSync(join(f.dir, name));
    assert.throws(() => complete(release, f.dir, { write: true }, f.run), /existing static asset differs from tagged rebuild/);
    assert.deepEqual(writes(f), []);
    assert.equal(existsSync(join(f.dir, 'SHA256SUMS')), false);
    assert.ok(readFileSync(join(f.dir, name)).equals(rebuilt));
  });
}

test('all six existing static assets byte-match and completion only adds missing assets', () => {
  const f = fixture(STATIC_BINARIES);
  assert.equal(STATIC_BINARIES.length, 6);
  assert.equal(complete(release, f.dir, { write: true }, f.run).status, 'complete');
  assert.equal(writes(f).length, 3);
  assert.ok(writes(f).every(call => !STATIC_BINARIES.includes(call[4].split('/').at(-1))));
});

test('a prior checksum file permits only identical rebuilt missing bytes', () => {
  const f = fixture(['SHA256SUMS']);
  writeFileSync(join(f.dir, BINARIES[0]), 'different signed bytes');
  assert.throws(() => complete(release, f.dir, { write: true }, f.run), /immutable checksum mismatch/);
  assert.deepEqual(writes(f), []);
});

test('published complete release verifies without mutations; published incomplete release fails', () => {
  const f = fixture(ASSETS, { published: true });
  assert.equal(complete(release, f.dir, { write: true }, f.run).status, 'complete');
  assert.deepEqual(writes(f), []);
  f.state.assets.pop();
  assert.throws(() => complete(release, f.dir, { write: true }, f.run), /published release/);
  assert.deepEqual(writes(f), []);
});

for (const [name, mutate] of [
  ['wrong release digest', state => state.release.body = 'Digest: sha256:wrong'],
  ['duplicate asset', state => state.assets.push(state.assets[0])],
  ['unknown asset', state => state.assets[0].name = '../untrusted'],
  ['interrupted upload', state => state.assets[0].state = 'starter'],
  ['corrupt asset', state => state.assets[0].digest = `sha256:${'f'.repeat(64)}`],
]) test(`${name} prevents all writes`, () => {
  const f = fixture([BINARIES[0]]); mutate(f.state);
  assert.throws(() => complete(release, f.dir, { write: true }, f.run));
  assert.deepEqual(writes(f), []);
});

test('publication between planning and upload fails closed', () => {
  const f = fixture([BINARIES[0]]);
  let reads = 0;
  f.state.before = (_program, args) => {
    if (args.at(-1)?.includes('/releases?') && ++reads === 2) f.state.release.draft = false;
  };
  assert.throws(() => complete(release, f.dir, { write: true }, f.run), /changed during completion/);
  assert.deepEqual(writes(f), []);
});

test('checksum parser rejects duplicates and unexpected entries', () => {
  const f = fixture();
  const text = f.bytes.get('SHA256SUMS').toString();
  assert.equal(checksumMap(text).size, 8);
  assert.throws(() => checksumMap(text + text.split('\n')[0] + '\n'));
  assert.throws(() => checksumMap(text.replace(BINARIES[0], '../bad')));
});
