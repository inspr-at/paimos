// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, extname, join, relative, resolve } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import { checkWebReproducibility } from './check-web-reproducibility.mjs';
import { parseRuntimeApkPins } from './runtime-apk-pins.mjs';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const web = join(root, 'web');
const sourceExtensions = new Set(['.ts', '.tsx', '.js', '.jsx', '.mjs', '.cjs', '.vue']);

test('AEON-1276 web reproducibility guard rejects changed names and bytes and propagates build failures', t => {
  const cwd = mkdtempSync(join(tmpdir(), 'aeon-web-repro-'));
  t.after(() => rmSync(cwd, { recursive: true, force: true }));
  const dist = join(cwd, 'web/dist');
  const seed = () => {
    rmSync(dist, { recursive: true, force: true });
    mkdirSync(join(dist, 'assets'), { recursive: true });
    writeFileSync(join(dist, 'index.html'), '<script src="/assets/app-1.js"></script>');
    writeFileSync(join(dist, 'assets/app-1.js'), 'export const value = 1;');
  };
  seed();
  let calls = 0;
  assert.equal(checkWebReproducibility(cwd, (file, args, directory) => {
    calls++;
    assert.equal(file, 'npx');
    assert.deepEqual(args, ['--no-install', 'vite', 'build']);
    assert.equal(directory, join(cwd, 'web'));
    seed();
  }), 2);
  assert.equal(calls, 1, 'only one extra bundle, without another dependency install');
  assert.deepEqual(readFileSync(join(cwd, 'tmp/web-reproducibility/first.json')),
    readFileSync(join(cwd, 'tmp/web-reproducibility/second.json')));
  for (const mutation of [
    () => writeFileSync(join(dist, 'assets/app-1.js'), 'export const value = 2;'),
    () => {
      rmSync(join(dist, 'assets/app-1.js'));
      writeFileSync(join(dist, 'assets/app-2.js'), 'export const value = 1;');
    },
    () => writeFileSync(join(dist, 'assets/extra.css'), '.extra { color: red; }'),
  ]) {
    seed();
    assert.throws(() => checkWebReproducibility(cwd, mutation), /web asset filenames or bytes differ/);
    assert.equal(readFileSync(join(cwd, 'tmp/web-reproducibility/first/assets/app-1.js'), 'utf8'), 'export const value = 1;');
  }
  seed();
  assert.throws(() => checkWebReproducibility(cwd, () => { throw new Error('vite build failed'); }), /vite build failed/);
  rmSync(join(dist, 'index.html'));
  assert.throws(() => checkWebReproducibility(cwd, () => assert.fail('must not build without a baseline')), /web build lacks index.html/);
});

function importSpecifiers(source) {
  // Preserve quoted strings while removing comments, including commented imports.
  const code = source.replace(/'(?:\\.|[^'\\])*'|"(?:\\.|[^"\\])*"|\/\/[^\n]*|\/\*[\s\S]*?\*\//g,
    token => token.startsWith('//') || token.startsWith('/*') ? ' ' : token);
  return [...code.matchAll(/\b(?:from\s*|import\s*(?:\(\s*)?|require\s*\(\s*)['"](\.[^'"]+)['"]/g)]
    .map(match => match[1]);
}

function lockedPackageProblem(entry) {
  if (entry.link || entry.inBundle || !/^https:\/\/registry\.npmjs\.org\//.test(entry.resolved ?? '')) return 'not a registry tarball';
  if (!/^sha512-[A-Za-z0-9+/]+=*$/.test(entry.integrity ?? '')) return 'missing sha512 integrity';
  return null;
}

function externalImports(directory = join(web, 'src')) {
  const targets = new Set();
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) {
      for (const target of externalImports(path)) targets.add(target);
    } else if (sourceExtensions.has(extname(path))) {
      for (const specifier of importSpecifiers(readFileSync(path, 'utf8'))) {
        const target = externalTarget(path, specifier);
        if (target) targets.add(target);
      }
    }
  }
  return targets;
}

function externalTarget(path, specifier) {
  const target = resolve(dirname(path), specifier.split(/[?#]/)[0]);
  const fromWeb = relative(web, target);
  if (fromWeb !== '..' && !fromWeb.startsWith('../')) return null;
  const fromRoot = relative(root, target);
  assert.ok(fromRoot !== '..' && !fromRoot.startsWith('../'), `${path}: import escapes the repository: ${specifier}`);
  const candidates = [target, ...[...sourceExtensions, '.json'].flatMap(extension => [target + extension, join(target, 'index' + extension)])];
  const file = candidates.find(candidate => existsSync(candidate) && statSync(candidate).isFile());
  assert.ok(file, `${path}: external import does not resolve: ${specifier}`);
  return relative(root, file);
}

// AEON-983: web/src imports the composed module. The three legacy JSON files
// stay in the tree for other readers, but they are not Docker web inputs.
const requiredSharedImports = [
  'internal/agentactivity/privacy.json',
  'internal/authz/permission_data.mjs',
  'internal/nodes/status_definitions.json',
];

test('production external web compilation resolves every shared import in the full checkout', () => {
  const targets = externalImports();
  for (const input of requiredSharedImports) assert.ok(targets.has(input), `missing shared import ${input}`);
});

test('AEON-983 permission data is one shared module import', () => {
  const legacy = [
    'internal/authz/permission_labels.json',
    'internal/authz/project_self_permissions.json',
    'internal/authz/builtin_agent_exclusions.json',
  ];
  assert.deepEqual(requiredSharedImports, [
    'internal/agentactivity/privacy.json',
    'internal/authz/permission_data.mjs',
    'internal/nodes/status_definitions.json',
  ]);
  const targets = externalImports();
  assert.ok(targets.has('internal/authz/permission_data.mjs'), 'missing shared import internal/authz/permission_data.mjs');
  for (const input of legacy) {
    assert.equal(requiredSharedImports.includes(input), false, input);
    assert.equal(targets.has(input), false, `web/src still imports ${input}`);
  }
});

test('assembly Dockerfile only copies prebuilt production artifacts into the frozen runtime', () => {
  const dockerfile = readFileSync(join(root, 'Dockerfile'), 'utf8');
  assert.match(dockerfile, /^FROM aeon-runtime$/m);
  assert.doesNotMatch(dockerfile, /^RUN /m);
  assert.match(dockerfile, /^COPY dist\/image-input\/paimos \/paimos$/m);
  assert.match(dockerfile, /^COPY NOTICE \/usr\/share\/doc\/aeon\/NOTICE$/m);
  assert.match(dockerfile, /^USER 65532:65532$/m);
  const runtime = readFileSync(join(root, 'scripts/Dockerfile.runtime'), 'utf8');
  const smoke = readFileSync(join(root, 'scripts/smoke-image.sh'), 'utf8');
  assert.match(runtime, /^FROM alpine:3\.24@sha256:[a-f0-9]{64}$/m);
  assert.match(dockerfile, /chromium allow-list/);
  assert.match(dockerfile, /scripts\/Dockerfile\.runtime/);
  assert.doesNotMatch(dockerfile, /chromium=[0-9]/);
  assert.doesNotMatch(smoke, /152\.0\.7977\.82|155\.0\.8059\.39/);
  assert.match(smoke, /parseRuntimeApkPins/);
  assert.match(smoke, /\/etc\/aeon-chromium-apk/);
  // Versions come only from the pins= literal. A build arg must not be able
  // to change the closure, and chromium is never installed without a version.
  const instructions = runtime.replaceAll('\\\n', ' ').split('\n').filter(line => line && !line.startsWith('#'));
  assert.equal(instructions.length, 3);
  assert.match(instructions[0], /^FROM alpine:3\.24@sha256:[a-f0-9]{64}$/);
  assert.match(instructions[1], /^RUN apk add --no-cache ca-certificates "chromium=/);
  assert.match(instructions[2], /^RUN set -o pipefail; apk list --installed chromium /);
  assert.equal(runtime.includes('$'), false, 'a shell variable would let a build arg change the closure');
  assert.doesNotMatch(runtime, /^ARG /m);
  assert.match(runtime, /\|\| apk add --no-cache ca-certificates "chromium=/);
  assert.match(runtime, /no allow-listed chromium version is in the Alpine index/);
  assert.match(runtime, /\/etc\/aeon-chromium-apk/);
  assert.doesNotMatch(runtime, /apk add[^\n]*\bchromium(?!=)/);
  const pins = parseRuntimeApkPins(runtime);
  assert.deepEqual(pins, { chromium: ['155.0.8059.39-r0', '152.0.7977.82-r0'], tini: '0.19.0-r3' });
});

test('AEON-1450 runtime APK allow-list rejects empty, floating, duplicate and oldest-first pins', () => {
  const valid = '"chromium=155.0.8059.39-r0" "tini=0.19.0-r3"';
  assert.deepEqual(parseRuntimeApkPins(valid), { chromium: ['155.0.8059.39-r0'], tini: '0.19.0-r3' });
  for (const [text, reason] of [
    ['', /runtime APK pins block missing/],
    ['"tini=0.19.0-r3"', /allow-list is empty/],
    ['"chromium=152.0.7977.82-r0" "tini=0.19.0-r3" "chromium=155.0.8059.39-r0" "tini=0.19.0-r3"', /newest first/],
    ['"chromium=155.0.8059.39-r0" "tini=0.19.0-r3" "chromium=155.0.8059.39-r0" "tini=0.19.0-r3"', /duplicate/],
    ['"chromium=155.0.8059.39" "tini=0.19.0-r3"', /not an exact version/],
    ['"chromium=latest" "tini=0.19.0-r3"', /not an exact version/],
    ['"chromium=155.0.8059.39-r0" "tini=0.19.0-r4"', /tini pin changed/],
    ['"chromium=155.0.8059.39-r0"', /tini pin changed/],
    ['"vim=9.0.0-r0" "tini=0.19.0-r3"', /unknown package/],
  ]) assert.throws(() => parseRuntimeApkPins(text), reason, text);
});

// The release keeps the restored npm cache only because npm ci verifies every
// tarball against the lockfile. A locked package without a hash would be
// installed from the cache unverified.
test('every locked web dependency is a registry tarball with a sha512 integrity', () => {
  const lock = JSON.parse(readFileSync(join(web, 'package-lock.json'), 'utf8'));
  assert.equal(lock.lockfileVersion, 3);
  const packages = Object.entries(lock.packages).filter(([name]) => name !== '');
  assert.ok(packages.length > 0);
  for (const [name, entry] of packages) assert.equal(lockedPackageProblem(entry), null, name);
  assert.equal(lockedPackageProblem({ resolved: 'https://registry.npmjs.org/a/-/a-1.0.0.tgz' }), 'missing sha512 integrity');
  assert.equal(lockedPackageProblem({ resolved: 'https://registry.npmjs.org/a/-/a-1.0.0.tgz', integrity: 'sha1-AAAA' }), 'missing sha512 integrity');
  assert.equal(lockedPackageProblem({ resolved: 'git+https://example.invalid/a.git', integrity: 'sha512-AAAA' }), 'not a registry tarball');
  assert.equal(lockedPackageProblem({ link: true, resolved: '../a' }), 'not a registry tarball');
  assert.equal(lockedPackageProblem({ inBundle: true }), 'not a registry tarball');
});

test('assembly recipes leave single-platform result selection to the native Buildx target', () => {
  for (const file of ['Dockerfile', 'scripts/Dockerfile.runtime']) {
    const source = readFileSync(join(root, file), 'utf8');
    assert.doesNotMatch(source, /^ARG BUILDKIT_MULTI_PLATFORM/m, `${file}: recipe must not force a manifest list for the Docker smoke exporter`);
  }
  const standalone = readFileSync(join(root, 'scripts/assemble-image.mjs'), 'utf8');
  assert.doesNotMatch(standalone, /BUILDKIT_MULTI_PLATFORM/);
  assert.match(standalone, /'--provenance=false', '--load', '--tag', tag/);
});

test('recognizes multiline, side-effect, re-export and dynamic relative imports without comments', () => {
  assert.deepEqual(importSpecifiers(`
    // import ignored from '../../../ignored.json'
    /* export { ignored } from '../../../ignored-too.json' */
    import data\n      from '../../../static.json' with { type: 'json' }
    import '../../../side-effect.js'
    export { value } from '../../../export.js'
    const lazy = import('../../../dynamic.js')
  `), ['../../../static.json', '../../../side-effect.js', '../../../export.js', '../../../dynamic.js']);
});

test('rejects missing shared dependencies and imports escaping the full production checkout', () => {
  const importer = join(web, 'src/fixture.ts');
  assert.throws(() => externalTarget(importer, '../../internal/aeon-422-missing.json'), /external import does not resolve/);
  assert.throws(() => externalTarget(importer, '../../../aeon-422-outside.json'), /escapes the repository/);
  assert.equal(externalTarget(importer, '../../internal/agentactivity/privacy.json'), 'internal/agentactivity/privacy.json');
});
