// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import test from 'node:test';
import { execFileSync, spawnSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { classifyPaths, classifyPR, requireResults, validatePath } from './ci-pr-plan.mjs';

function repository() {
  const root = mkdtempSync(join(tmpdir(), 'aeon-pr-plan-'));
  const git = args => execFileSync('git', args, { cwd: root, encoding: 'utf8' });
  const file = (name, value = 'fixture\n') => {
    mkdirSync(dirname(join(root, name)), { recursive: true });
    writeFileSync(join(root, name), value);
    git(['add', name]);
  };
  const commit = () => { git(['-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', 'commit', '-qm', 'fixture']); return git(['rev-parse', 'HEAD']).trim(); };
  git(['init', '-q']);
  file('README.md'); file('web/tests/existing.spec.ts');
  const base = commit();
  return { root, git, file, commit, base,
    event: head => ({ pull_request: { base: { sha: base }, head: { sha: head } } }) };
}

test('real three-dot Git diff classifies docs, mixed code and specs including an unlisted spec', () => {
  for (const [paths, lane] of [
    [['README.md', 'docs/guide.txt', 'LICENSE'], 'docs-only'],
    [['README.md', 'internal/code.go'], 'full'],
    [['web/tests/existing.spec.ts', 'web/tests/not-in-map.spec.ts'], 'spec-only'],
    [['web/tests/helpers/shared.ts'], 'full'],
    [['web/tests/nested/example.spec.ts'], 'full'],
  ]) {
    const repo = repository();
    for (const path of paths) repo.file(path, 'changed\n');
    const head = repo.commit();
    const calls = [];
    const plan = classifyPR('pull_request', repo.event(head), { checkout: head,
      git: args => { calls.push(args); return repo.git(args); } });
    assert.equal(plan.lane, lane);
    assert.ok(calls.some(args => args[0] === 'diff' && args.includes(`${repo.base}...${head}`)));
    assert.ok(calls.some(args => args[0] === 'merge-base' && args[1] === '--is-ancestor'));
    assert.deepEqual(plan.specs, lane === 'spec-only' ? paths.sort() : []);
  }
});

test('real rename/delete diff widens specs and includes a rename source outside docs', () => {
  for (const operation of ['rename-spec', 'delete-spec', 'code-to-doc', 'rename-doc']) {
    const repo = repository();
    if (operation === 'rename-spec') repo.git(['mv', 'web/tests/existing.spec.ts', 'web/tests/renamed.spec.ts']);
    if (operation === 'delete-spec') repo.git(['update-index', '--force-remove', 'web/tests/existing.spec.ts']);
    if (operation === 'code-to-doc') repo.git(['mv', 'web/tests/existing.spec.ts', 'docs.md']);
    if (operation === 'rename-doc') repo.git(['mv', 'README.md', 'renamed.md']);
    const head = repo.commit();
    assert.equal(classifyPR('pull_request', repo.event(head), { git: repo.git }).lane, operation === 'rename-doc' ? 'docs-only' : 'full');
  }
});

test('diverged PR uses merge-base rather than requiring base to be an ancestor of head', () => {
  const repo = repository();
  repo.git(['checkout', '-qb', 'topic']);
  repo.file('README.md', 'topic'); const head = repo.commit();
  repo.git(['checkout', '--detach', repo.base]);
  repo.file('main-only.go', 'base moved'); const base = repo.commit();
  const event = { pull_request: { base: { sha: base }, head: { sha: head } } };
  assert.equal(classifyPR('pull_request', event, { git: repo.git }).lane, 'docs-only');
  repo.git(['merge', '--no-commit', '--no-ff', head]); const checkout = repo.commit();
  assert.equal(classifyPR('pull_request', event, { git: repo.git, checkout }).lane, 'docs-only');
});

test('path checks reject actual traversal components and absolute paths, allowing harmless dots', () => {
  for (const path of ['../docs.md', 'docs/../guide.md', '/docs/a.md', 'C:/docs/a.md', '\\docs\\a.md',
    'docs//a.md', 'docs/./a.md', 'docs/a\n.md', 'web/tests/..spec.ts/../a.spec.ts']) {
    assert.throws(() => classifyPaths([path]), /Invalid repository path/);
  }
  assert.equal(validatePath('docs/version..md'), 'docs/version..md');
  assert.equal(classifyPaths(['docs/version..md']).lane, 'docs-only');
  assert.equal(classifyPaths([]).lane, 'full');
  assert.equal(classifyPaths(['web/tests/..spec.ts']).lane, 'spec-only');
  assert.equal(classifyPaths(['LICENSE-helper.sh']).lane, 'full');
  assert.equal(classifyPaths(['LICENSE.sh']).lane, 'full');
});

test('main, queue and manual classification do not read PR input or Git', () => {
  for (const name of ['push', 'merge_group', 'workflow_dispatch']) {
    assert.equal(classifyPR(name, {}, { git: () => { throw new Error('unexpected git'); } }).lane, 'full');
  }
});

test('real CLI falls back to full on missing history, malformed input and wrong checkout', () => {
  const repo = repository(); repo.file('README.md', 'changed'); const head = repo.commit();
  const script = resolve('scripts/ci-pr-plan.mjs');
  for (const [event, checkout, lane] of [
    [repo.event(head), head, 'docs-only'],
    [repo.event('a'.repeat(40)), head, 'full'],
    [repo.event(head), repo.base, 'full'],
    [{}, head, 'full'],
  ]) {
    const eventPath = join(repo.root, 'event.json'), output = join(repo.root, `output-${lane}-${checkout}`);
    writeFileSync(eventPath, JSON.stringify(event)); writeFileSync(output, '');
    const result = spawnSync(process.execPath, [script], { cwd: repo.root, encoding: 'utf8',
      env: { ...process.env, GITHUB_EVENT_NAME: 'pull_request', GITHUB_EVENT_PATH: eventPath,
        GITHUB_SHA: checkout, GITHUB_OUTPUT: output, GITHUB_STEP_SUMMARY: join(repo.root, 'summary') } });
    assert.equal(result.status, 0, result.stderr);
    assert.equal(JSON.parse(result.stdout).lane, lane);
    assert.match(readFileSync(output, 'utf8'), new RegExp(`^lane=${lane}\\n`));
  }
});

test('required gates reject failures, cancelled work and unexpected skips rather than claiming success', () => {
  for (const lane of ['docs-only', 'spec-only', 'full']) {
    const expected = lane === 'full' ? 'success' : 'skipped';
    assert.match(requireResults(lane, 'success', [expected]), lane === 'full' ? /passed/ : new RegExp(lane));
    for (const result of ['failure', 'cancelled', lane === 'full' ? 'skipped' : 'success']) {
      assert.throws(() => requireResults(lane, 'success', [result]), /Expected .* CI dependency/);
    }
    assert.throws(() => requireResults(lane, 'failure', [expected]), /classification failed/);
  }
  const result = spawnSync(process.execPath, [resolve('scripts/ci-pr-plan.mjs'), 'gate', 'full', 'success', 'failure'], { encoding: 'utf8' });
  assert.equal(result.status, 1); assert.match(result.stderr, /Expected success CI dependency, got failure/);
});
