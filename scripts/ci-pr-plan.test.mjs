// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import test from 'node:test';
import { execFileSync, spawnSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, realpathSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { classifyPaths, classifyPR, requireResults, validatePath } from './ci-pr-plan.mjs';

function repository() {
  const root = realpathSync(mkdtempSync(join(tmpdir(), 'aeon-pr-plan-')));
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

test('Markdown fixtures and implementation documentation require full validation', () => {
  for (const path of ['internal/rulesimport/testdata/pack/AGENTS.md',
    'internal/db/migrations/README.md', 'scripts/audit/rubric.md', 'web/tests/fixture.md',
    'testdata/README.md', 'docs/testdata/fixture.md']) {
    const repo = repository();
    repo.file(path);
    const head = repo.commit();
    assert.equal(classifyPR('pull_request', repo.event(head), { git: repo.git }).lane, 'full', path);
  }
  assert.equal(classifyPaths(['README.md', 'docs/guide.txt', 'docs/nested/guide.md', 'LICENSE.txt']).lane, 'docs-only');
});

function workflowPlanScript() {
  const workflow = readFileSync(new URL('../.github/workflows/ci.yml', import.meta.url), 'utf8');
  const step = workflow.split('      - name: Classify local PR diff\n')[1].split('\n\n  runner-route:')[0];
  const run = step.split('        run: ')[1];
  assert.ok(run, 'classifier step must execute a command');
  return run.startsWith('|\n') ? run.slice(2).replace(/^          /gm, '') : run.trim();
}

function executeWorkflowPlan(repo, head, { eventName = 'pull_request', base = repo.base } = {}) {
  const eventPath = join(repo.root, 'event.json'), output = join(repo.root, 'output');
  writeFileSync(eventPath, JSON.stringify({ pull_request: { base: { sha: base }, head: { sha: head } } }));
  writeFileSync(output, '');
  const result = spawnSync('bash', ['-c', workflowPlanScript()], { cwd: repo.root, encoding: 'utf8', timeout: 20000,
    env: { ...process.env, GITHUB_EVENT_NAME: eventName, GITHUB_EVENT_PATH: eventPath,
      GITHUB_SHA: head, PR_BASE_SHA: base, RUNNER_TEMP: join(repo.root, 'runner-temp'),
      GITHUB_OUTPUT: output, GITHUB_STEP_SUMMARY: join(repo.root, 'summary') } });
  assert.equal(result.status, 0, result.stderr);
  return readFileSync(output, 'utf8');
}

test('ci-plan executes the base classifier even when the PR replaces it with a false docs-only result', () => {
  const repo = repository();
  repo.file('scripts/ci-pr-plan.mjs', readFileSync(new URL('./ci-pr-plan.mjs', import.meta.url), 'utf8'));
  repo.base = repo.commit();
  repo.file('scripts/ci-pr-plan.mjs', `import { appendFileSync } from 'node:fs';
appendFileSync(process.env.GITHUB_OUTPUT, 'lane=docs-only\\nspecs=[]\\n');\n`);
  repo.file('internal/code.go');
  const head = repo.commit();
  mkdirSync(join(repo.root, 'runner-temp'));
  assert.equal(executeWorkflowPlan(repo, head), 'lane=full\nspecs=[]\n');
});

test('ci-plan keeps trusted docs/spec lanes and falls back to full without a usable base classifier', () => {
  for (const [source, path, lane] of [
    [readFileSync(new URL('./ci-pr-plan.mjs', import.meta.url), 'utf8'), 'README.md', 'docs-only'],
    [readFileSync(new URL('./ci-pr-plan.mjs', import.meta.url), 'utf8'), 'web/tests/existing.spec.ts', 'spec-only'],
    [null, 'README.md', 'full'],
    ['throw new Error("broken base classifier");\n', 'README.md', 'full'],
  ]) {
    const repo = repository();
    if (source !== null) { repo.file('scripts/ci-pr-plan.mjs', source); repo.base = repo.commit(); }
    repo.file(path, 'changed\n');
    const head = repo.commit();
    mkdirSync(join(repo.root, 'runner-temp'));
    assert.equal(executeWorkflowPlan(repo, head), `lane=${lane}\nspecs=${JSON.stringify(lane === 'spec-only' ? [path] : [])}\n`);
    for (const eventName of ['push', 'merge_group', 'workflow_dispatch']) {
      assert.equal(executeWorkflowPlan(repo, head, { eventName }), 'lane=full\nspecs=[]\n');
    }
    assert.equal(executeWorkflowPlan(repo, head, { base: 'invalid-base' }), 'lane=full\nspecs=[]\n');
    assert.equal(executeWorkflowPlan(repo, head, { base: 'a'.repeat(40) }), 'lane=full\nspecs=[]\n');
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
