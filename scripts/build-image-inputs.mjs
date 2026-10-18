// SPDX-License-Identifier: AGPL-3.0-only
// Host compilation, shared by production, rehearsal and clean rebuild proofs.
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { chmodSync, lstatSync, mkdirSync, readFileSync, readdirSync, utimesSync, writeFileSync } from 'node:fs';
import { isAbsolute, join, relative, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

export const root = resolve(fileURLToPath(new URL('..', import.meta.url)));
export const sha256 = bytes => createHash('sha256').update(bytes).digest('hex');
export function command(file, args, cwd = root, extraEnv = {}, capture = false) {
  const result = spawnSync(file, args, {
    cwd, env: { ...process.env, ...extraEnv }, stdio: capture ? ['ignore', 'pipe', 'inherit'] : 'inherit',
    encoding: 'utf8', timeout: 10 * 60_000, maxBuffer: 4 * 1024 * 1024,
  });
  if (result.error || result.status !== 0) throw new Error(`${file} failed (${result.status ?? result.error.code})`);
  return capture ? result.stdout.trim() : '';
}
export function epoch(value) {
  if (!/^(0|[1-9]\d{0,10})$/.test(String(value)) || !Number.isSafeInteger(Number(value))) throw new Error('invalid SOURCE_DATE_EPOCH');
  return Number(value);
}
export function buildInputs(cwd = root, values = process.env, run = command) {
  const arch = values.ARCH;
  if (!['amd64', 'arm64'].includes(arch)) throw new Error('ARCH must be amd64 or arm64');
  const version = values.VERSION || 'dev';
  const reserved = JSON.parse(readFileSync(join(cwd, 'version.json'), 'utf8')).version;
  if (version !== 'dev' && version !== reserved) throw new Error('VERSION differs from reserved version.json');
  const stamp = epoch(values.SOURCE_DATE_EPOCH ?? run('git', ['log', '-1', '--format=%ct'], cwd, {}, true));
  return { platform: `linux/${arch}`, version, source_date_epoch: stamp };
}
// The go command never re-verifies a build-cache entry, and the caches that
// actions/setup-go restores are also written by CI jobs on the self-hosted
// pool. A shipped binary therefore compiles cold: in GitHub Actions the caller
// must pass GOCACHE and GOMODCACHE inside RUNNER_TEMP (empty at job start,
// never a cache-restore target) and GOCACHE must still be empty. Local runs
// keep whatever caches they have.
export function goCaches(values = process.env) {
  const hosted = values.GITHUB_ACTIONS === 'true';
  const caches = {};
  for (const name of ['GOCACHE', 'GOMODCACHE']) {
    const value = values[name];
    if (!value) {
      if (hosted) throw new Error(`${name} must be a directory inside RUNNER_TEMP for a shipped build`);
      continue;
    }
    if (!isAbsolute(value)) throw new Error(`${name} must be an absolute path`);
    if (hosted) {
      const temp = values.RUNNER_TEMP, inside = temp && isAbsolute(temp) ? relative(resolve(temp), resolve(value)) : '';
      if (!inside || inside === '..' || inside.startsWith('../') || isAbsolute(inside)) throw new Error(`${name} must be a directory inside RUNNER_TEMP for a shipped build`);
    }
    caches[name] = resolve(value);
  }
  if (caches.GOCACHE && caches.GOCACHE === caches.GOMODCACHE) throw new Error('GOCACHE and GOMODCACHE must differ');
  if (hosted) {
    let entries = [];
    try { entries = readdirSync(caches.GOCACHE); } catch (error) { if (error.code !== 'ENOENT') throw new Error('GOCACHE is not a usable directory'); }
    if (entries.length) throw new Error('GOCACHE is not empty; a shipped binary needs a cold build cache');
  }
  return caches;
}
export function filesIn(directory, prefix = '') {
  return readdirSync(directory).sort().flatMap(name => {
    const path = join(directory, name), relative = prefix + name;
    const info = lstatSync(path);
    if (info.isDirectory()) return filesIn(path, relative + '/');
    if (!info.isFile()) throw new Error(`non-regular build artifact: ${relative}`);
    return [{ path, relative }];
  });
}
export function webBuild(cwd = root, run = command) {
  // Work from the full repository: shared JSON imports retain their relative
  // paths. npm ci cleans dependencies, Vite clears dist, and clean snapshots
  // also discard incremental tsc state.
  // Unlike the Go build cache, the restored npm cache may stay: it holds only
  // downloaded tarballs, package-lock.json pins a sha512 for each of them and
  // npm ci checks that hash whether the bytes come from the cache or the
  // registry. Altered cache content fails the install instead of shipping.
  // The prebuild test below requires that integrity on every locked package.
  run('node', ['--test', 'scripts/docker-web-inputs.test.mjs'], cwd);
  run('npm', ['ci', '--no-audit', '--no-fund'], join(cwd, 'web'));
  run('npm', ['run', 'build'], join(cwd, 'web'));
}
export function compile(cwd = root, values = process.env, run = command) {
  const inputs = buildInputs(cwd, values, run);
  // Before any work: a used build cache must stop the build, not be noticed late.
  const go = { ...goCaches(values), GOTOOLCHAIN: 'local', GOFLAGS: '-mod=readonly', GOWORK: 'off' };
  const stamp = inputs.source_date_epoch;
  const history = readFileSync(join(cwd, 'internal/releasehistory/data/history.json'));
  webBuild(cwd, run);
  const web = filesIn(join(cwd, 'web/dist'));
  if (!web.some(file => file.relative === 'index.html')) throw new Error('web/dist lacks index.html');
  for (const file of web) utimesSync(file.path, stamp, stamp);
  const out = join(cwd, 'dist/image-input');
  mkdirSync(out, { recursive: true });
  // Downloads are checked against go.sum; verify then re-hashes every module
  // zip and extracted tree, and the build compares both with go.sum again.
  // An empty module cache gives verify nothing to check, hence the download.
  run('go', ['mod', 'download'], cwd, go);
  run('go', ['mod', 'verify'], cwd, go);
  run('go', ['build', '-trimpath', '-buildvcs=false', '-tags', 'webembed', '-ldflags',
    `-X github.com/inspr-at/paimos/internal/version.Version=${inputs.version}`, '-o', join(out, 'paimos'), './cmd/aeon'],
  cwd, { ...go, CGO_ENABLED: '0', GOOS: 'linux', GOARCH: inputs.platform.split('/')[1], GOAMD64: 'v1', GOARM64: 'v8.0', SOURCE_DATE_EPOCH: String(stamp) });
  const binary = readFileSync(join(out, 'paimos'));
  if (binary.length < 64 || !binary.subarray(0, 4).equals(Buffer.from([0x7f, 0x45, 0x4c, 0x46]))) throw new Error('image binary is not ELF');
  chmodSync(join(out, 'paimos'), 0o755);
  utimesSync(join(out, 'paimos'), stamp, stamp);
  const manifest = {
    schema: 'aeon.image-inputs.v1', ...inputs,
    tree: values.SOURCE_TREE || run('git', ['rev-parse', 'HEAD^{tree}'], cwd, {}, true),
    history_sha256: sha256(history), notice_sha256: sha256(readFileSync(join(cwd, 'NOTICE'))),
    dockerfile_sha256: sha256(readFileSync(join(cwd, 'Dockerfile'))),
    runtime_recipe_sha256: sha256(readFileSync(join(cwd, 'scripts/Dockerfile.runtime'))),
    go: run('go', ['version'], cwd, {}, true), node: run('node', ['--version'], cwd, {}, true),
    npm: run('npm', ['--version'], cwd, {}, true), binary_sha256: sha256(binary),
    web: web.map(file => ({ path: file.relative, sha256: sha256(readFileSync(file.path)) })),
  };
  writeFileSync(join(out, 'inputs.json'), JSON.stringify(manifest, null, 2) + '\n');
  return manifest;
}
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const mode = process.argv[2] || 'build';
    if (mode === 'web') webBuild();
    else if (mode === 'build') console.log(JSON.stringify(compile()));
    else if (mode === 'inputs') {
      const inputs = buildInputs();
      if (process.env.GITHUB_OUTPUT) writeFileSync(process.env.GITHUB_OUTPUT, `epoch=${inputs.source_date_epoch}\n`, { flag: 'a' });
      console.log(JSON.stringify(inputs));
    } else throw new Error('usage: build-image-inputs.mjs [inputs|build|web]');
  } catch (error) { console.error(error.message); process.exitCode = 1; }
}
