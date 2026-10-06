// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import test from 'node:test';
import { execFileSync, spawnSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, realpathSync, chmodSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { classifyPaths, classifyPR, requireResults, validatePath } from './ci-pr-plan.mjs';

function repository() {
  const root = realpathSync(mkdtempSync(join(tmpdir(), 'aeon-pr-plan-')));
  const home = mkdtempSync(join(tmpdir(), 'aeon-pr-plan-home-'));
  // Every Git call, including workflow/CLI children and no-commit merges,
  // uses fixture identity and cannot inherit the host's Git configuration.
  const env = { PATH: process.env.PATH, HOME: home, XDG_CONFIG_HOME: home,
    GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: '/dev/null',
    GIT_AUTHOR_NAME: 'Fixture', GIT_AUTHOR_EMAIL: 'fixture@example.invalid',
    GIT_COMMITTER_NAME: 'Fixture', GIT_COMMITTER_EMAIL: 'fixture@example.invalid' };
  const git = args => execFileSync('git', args, { cwd: root, encoding: 'utf8', env });
  const file = (name, value = 'fixture\n') => {
    mkdirSync(dirname(join(root, name)), { recursive: true });
    writeFileSync(join(root, name), value);
    git(['add', name]);
  };
  const commit = () => { git(['commit', '-qm', 'fixture']); return git(['rev-parse', 'HEAD']).trim(); };
  git(['init', '-q']);
  file('README.md'); file('web/tests/existing.spec.ts');
  const base = commit();
  return { root, env, git, file, commit, base,
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
  const step = workflow.split('      - name: Classify local PR diff\n')[1].split(/\n      - |\n\n  [a-z][a-z-]*:/)[0];
  const run = step.split('        run: ')[1];
  assert.ok(run, 'classifier step must execute a command');
  return run.startsWith('|\n') ? run.slice(2).replace(/^          /gm, '') : run.trim();
}

function executeWorkflowPlan(repo, head, { eventName = 'pull_request', base = repo.base } = {}) {
  const eventPath = join(repo.root, 'event.json'), output = join(repo.root, 'output');
  writeFileSync(eventPath, JSON.stringify({ pull_request: { base: { sha: base }, head: { sha: head } } }));
  writeFileSync(output, '');
  const result = spawnSync('bash', ['-c', workflowPlanScript()], { cwd: repo.root, encoding: 'utf8', timeout: 20000,
    env: { ...repo.env, GITHUB_EVENT_NAME: eventName, GITHUB_EVENT_PATH: eventPath,
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
  assert.equal(executeWorkflowPlan(repo, head), 'lane=full\nspecs=[]\nnix_vendor=true\n');
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
    assert.equal(executeWorkflowPlan(repo, head), `lane=${lane}\nspecs=${JSON.stringify(lane === 'spec-only' ? [path] : [])}\nnix_vendor=${lane === 'full'}\n`);
    for (const eventName of ['push', 'merge_group', 'workflow_dispatch']) {
      assert.equal(executeWorkflowPlan(repo, head, { eventName }), 'lane=full\nspecs=[]\nnix_vendor=true\n');
    }
    assert.equal(executeWorkflowPlan(repo, head, { base: 'invalid-base' }), 'lane=full\nspecs=[]\nnix_vendor=true\n');
    assert.equal(executeWorkflowPlan(repo, head, { base: 'a'.repeat(40) }), 'lane=full\nspecs=[]\nnix_vendor=true\n');
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

test('Nix hash checks follow dependency and guard inputs, including deletions and rename sources', () => {
  for (const path of ['go.mod', 'go.sum', 'flake.nix', 'flake.lock',
    '.github/workflows/ci.yml', 'scripts/ci-pr-plan.mjs', 'scripts/check-nix-vendor-hash.sh']) {
    assert.equal(classifyPaths([path]).nixVendor, true, path);
    assert.equal(classifyPaths(['internal/other.go'], [path]).nixVendor, true, `deleted ${path}`);
  }
  for (const path of ['README.md', 'docs/release.txt', 'web/tests/example.spec.ts',
    'internal/nodes/module.go', 'web/src/App.vue']) {
    assert.equal(classifyPaths([path]).nixVendor, false, path);
  }
  assert.equal(classifyPaths([]).nixVendor, true);
  for (const event of ['push', 'merge_group', 'workflow_dispatch']) {
    assert.equal(classifyPR(event, {}).nixVendor, true, event);
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
      env: { ...repo.env, GITHUB_EVENT_NAME: 'pull_request', GITHUB_EVENT_PATH: eventPath,
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

function requiredJob(id) {
  const workflow = readFileSync(new URL('../.github/workflows/ci.yml', import.meta.url), 'utf8');
  const match = new RegExp(`^  ${id}:\\n([\\s\\S]*?)(?=^  [a-z][a-z0-9-]*:|$(?![\\s\\S]))`, 'm').exec(workflow);
  assert.ok(match, `Missing required job ${id}`);
  return match[1];
}

test('required aggregate checks never execute the checked-out classifier script', () => {
  for (const id of ['go', 'web', 'release-check', 'e2e']) {
    const job = requiredJob(id);
    assert.doesNotMatch(job, /scripts\/ci-pr-plan\.mjs/, `${id} must not execute PR-owned gate code`);
    assert.match(job, /ref: \$\{\{ github.event.merge_group.base_sha \}\}/, `${id} must check out only trusted base proof code`);
    assert.match(job, /if: needs.tree-reuse.outputs.reuse == 'pull_request'/, `${id} must check out only for PR proof revalidation`);
  }
});

for (const id of ['release-check', 'e2e']) {
  test(`${id} rejects failed dependencies even when the PR makes its gate script always succeed`, () => {
    const repo = repository();
    repo.file('scripts/ci-pr-plan.mjs', 'process.exit(0);\n');
    repo.commit();
    const run = requiredJob(id).split('        run: |\n')[1];
    assert.ok(run, `${id} must have an executable gate`);
    const script = run.replace(/^          /gm, '');
    const execute = (lane, result, plan = 'success') => {
      const execution = spawnSync('bash', ['-c', script], { cwd: repo.root, encoding: 'utf8', timeout: 10000,
        env: { ...repo.env, REUSE: 'none', CI_LANE: lane, CI_PLAN: plan, CHECK_RESULT: result,
          GITHUB_STEP_SUMMARY: join(repo.root, 'summary') } });
      assert.ifError(execution.error);
      assert.equal(execution.signal, null, 'gate must finish without a signal');
      return execution;
    };
    for (const lane of ['docs-only', 'spec-only', 'full']) {
      const expected = lane === 'full' ? 'success' : 'skipped';
      const positive = execute(lane, expected);
      assert.equal(positive.status, 0, `${lane}: ${positive.stderr}`);
      for (const result of ['failure', 'cancelled', '', 'pending', expected === 'success' ? 'skipped' : 'success']) {
        assert.equal(execute(lane, result).status, 1, `${lane} must reject CHECK_RESULT=${result}`);
      }
      for (const plan of ['failure', 'cancelled', 'skipped', '']) {
        assert.equal(execute(lane, expected, plan).status, 1, `${lane} must reject CI_PLAN=${plan}`);
      }
    }
    for (const lane of ['', 'unknown']) {
      assert.equal(execute(lane, 'success').status, 1, `gate must reject CI_LANE=${lane}`);
    }
  });
}

// Exercise the merged YAML commands with process stubs: no browser or build.
// A new unclassified spec must still reach the spec-only command, while a
// full-lane run uses tiers. Both branches must retain a child failure.
test('merged web commands keep spec-only independent of tiers and propagate failures in both lanes', () => {
  const workflow = readFileSync(new URL('../.github/workflows/ci.yml', import.meta.url), 'utf8');
  const script = (id, name) => {
    const match = new RegExp(`^  ${id}:\\n([\\s\\S]*?)(?=^  [a-z][a-z-]*:|$(?![\\s\\S]))`, 'm').exec(workflow);
    assert.ok(match, `Missing job: ${id}`);
    const job = match[1];
    const remainder = job.split(`      - name: ${name}\n`)[1];
    assert.ok(remainder, `Missing step: ${id}/${name}`);
    const step = remainder.split(/\n      - /)[0];
    return step.split('        run: |\n')[1].replace(/^          /gm, '')
      .replaceAll('${{ matrix.shard }}', '1').replaceAll('${{ strategy.job-total }}', '2');
  };
  const home = realpathSync(mkdtempSync(join(tmpdir(), 'aeon-681-lanes-'))), bin = join(home, 'bin');
  mkdirSync(bin);
  for (const tool of ['npm', 'node']) {
    const path = join(bin, tool);
    writeFileSync(path, `#!/bin/bash
printf '%s\\n' '${tool} '"$*" >> "$CALL_LOG"
if [ '${tool} '"$*" = "$TARGET_COMMAND" ]; then exit "$CHILD_CODE"; fi
exit 0
`);
    chmodSync(path, 0o755);
  }
  for (const lane of ['spec-only', 'full']) {
    for (const code of ['0', '7']) {
      for (const [id, name] of [['web-unit', 'Run selected web units without retries'], ['web-shard', 'Run selected UI cases without retries']]) {
        const target = lane === 'spec-only'
          ? id === 'web-unit' ? 'npm run test:unit:core' : 'npm --prefix web run ci:web:shard -- 1/1 --reporter=line,json'
          : id === 'web-unit' ? 'node ../scripts/test-tiers/cli.mjs run web --unit --shard 1/2 --job web-unit-1'
            : 'node scripts/test-tiers/cli.mjs run web --shard 1/2 --job web-shard-1';
        const log = join(home, `${id}-${lane}-${code}`);
        writeFileSync(log, '');
        const result = spawnSync('/bin/bash', ['-e', '-c', script(id, name)], { encoding: 'utf8', cwd: home,
          env: { HOME: home, PATH: `${bin}:/usr/bin:/bin`, CI_LANE: lane, CHILD_CODE: code, TARGET_COMMAND: target, CALL_LOG: log,
            CI_CHANGED_SPECS: '["web/tests/new-unclassified.spec.ts"]' } });
        assert.equal(result.status, Number(code), result.stderr);
        const calls = readFileSync(log, 'utf8');
        assert.ok(calls.split('\n').includes(target), `${id}/${lane} must reach the target command before returning ${code}: ${calls}`);
        if (lane === 'spec-only') {
          assert.doesNotMatch(calls, /test-tiers|aeon-681-ci/);
          if (id === 'web-shard') assert.match(calls, /npm --prefix web run ci:web:shard -- 1\/1 --reporter=line,json/);
          else assert.match(calls, /npm run test:unit:core/);
        } else {
          assert.match(calls, /test-tiers\/cli\.mjs run web/);
        }
      }
    }
  }
});
