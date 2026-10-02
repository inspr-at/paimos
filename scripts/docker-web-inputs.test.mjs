// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs';
import { basename, dirname, extname, join, posix, relative, resolve } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const web = join(root, 'web');
const sourceExtensions = new Set(['.ts', '.tsx', '.js', '.jsx', '.mjs', '.cjs', '.vue']);

function importSpecifiers(source) {
  // Preserve quoted strings while removing comments, including commented imports.
  const code = source.replace(/'(?:\\.|[^'\\])*'|"(?:\\.|[^"\\])*"|\/\/[^\n]*|\/\*[\s\S]*?\*\//g,
    token => token.startsWith('//') || token.startsWith('/*') ? ' ' : token);
  return [...code.matchAll(/\b(?:from\s*|import\s*(?:\(\s*)?|require\s*\(\s*)['"](\.[^'"]+)['"]/g)]
    .map(match => match[1]);
}

function externalImports(directory = join(web, 'src')) {
  const targets = new Set();
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) {
      for (const target of externalImports(path)) targets.add(target);
    } else if (sourceExtensions.has(extname(path))) {
      for (const specifier of importSpecifiers(readFileSync(path, 'utf8'))) {
        const target = resolve(dirname(path), specifier.split(/[?#]/)[0]);
        const fromWeb = relative(web, target);
        if (fromWeb !== '..' && !fromWeb.startsWith('../')) continue;
        const fromRoot = relative(root, target);
        assert.ok(fromRoot !== '..' && !fromRoot.startsWith('../'), `${path}: import escapes the repository: ${specifier}`);
        // Resolve extensionless modules too; dependencies must be real files.
        const candidates = [target, ...[...sourceExtensions, '.json'].flatMap(extension => [target + extension, join(target, 'index' + extension)])];
        const file = candidates.find(candidate => existsSync(candidate) && statSync(candidate).isFile());
        assert.ok(file, `${path}: external import does not resolve: ${specifier}`);
        targets.add(relative(root, file));
      }
    }
  }
  return targets;
}

function copiedWebInputs(dockerfile) {
  const copies = new Set();
  let inWeb = false;
  for (const line of dockerfile.replace(/\\\r?\n/g, ' ').split(/\r?\n/)) {
    if (/^\s*FROM\s/i.test(line)) {
      if (inWeb) break;
      inWeb = /\sAS\s+web\s*$/i.test(line);
      continue;
    }
    if (!inWeb) continue;
    // A copy in another stage or after the build cannot satisfy its imports.
    if (/^\s*RUN\s+.*\bnpm\s+run\s+build\b/i.test(line)) break;
    const copy = /^\s*COPY\s+(.*)$/i.exec(line);
    if (!copy || copy[1].startsWith('--')) continue;
    const args = copy[1].startsWith('[') ? JSON.parse(copy[1]) : copy[1].trim().split(/\s+/);
    const destination = args.pop();
    for (const source of args) {
      // Require explicit files at their repository-relative /src paths. Broad
      // directory copies would grow the minimal web stage with unrelated code.
      const target = destination.endsWith('/') ? posix.join(destination, basename(source)) : destination;
      if (posix.normalize(target) === posix.join('/src', source)) copies.add(posix.normalize(source));
    }
  }
  return copies;
}

function assertInputsCopied(targets, dockerfile) {
  const copies = copiedWebInputs(dockerfile);
  const missing = [...targets].filter(target => !copies.has(target)).sort();
  assert.deepEqual(missing, [], `Dockerfile web stage must COPY before npm run build: ${missing.join(', ')}`);
}

test('every web/src import outside web/ is explicitly copied into the Dockerfile web stage', () => {
  assertInputsCopied(externalImports(), readFileSync(join(root, 'Dockerfile'), 'utf8'));
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

test('rejects absent, misplaced, broad and late copies, including copies in another stage', () => {
  const input = 'internal/example.json';
  const stage = 'FROM node:24 AS web\nWORKDIR /src/web\n';
  const copy = `COPY ${input} /src/${input}\n`;
  for (const dockerfile of [
    stage + 'RUN npm run build\n',
    stage + `COPY ${input} /src/web/example.json\nRUN npm run build\n`,
    stage + 'COPY internal/ /src/internal/\nRUN npm run build\n',
    stage + 'RUN npm run build\n' + copy,
    stage + 'FROM golang:1.26 AS build\n' + copy,
    stage + `COPY --from=build ${input} /src/${input}\nRUN npm run build\n`,
  ]) assert.throws(() => assertInputsCopied([input], dockerfile), /internal\/example.json/);
  assertInputsCopied([input], stage + copy + 'RUN npm run build\n');
  assertInputsCopied([input], stage + `COPY ["${input}", "/src/internal/"]\nRUN npm run build\n`);
});
