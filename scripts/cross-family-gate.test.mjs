// SPDX-License-Identifier: AGPL-3.0-only
import assert from "node:assert/strict";
import { existsSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";
import { pathToFileURL } from "node:url";
import { checkEvent, checkPullRequest, createGit, githubAPI, resolveMergeGroup, trustedVerdict, validateConfig } from "./cross-family-gate.mjs";
import { postGate, statusApp, statusTarget, statusWriter } from "./post-cross-family-gate.mjs";
import { extractPolicy, refreshMain, withPendingStatus } from "./trusted-cross-family-poster.mjs";

const repository = "inspr-at/paimos";
const config = { schema: 1, base_branch: "main", posters: [{ login: "markus-barta", id: 276789 }] };
const description = "model=fixture-reviewer; route=fixture-cli; review=reviews/aeon-411.txt";
const verdict = (changes = {}) => ({ id: 10, state: "success", context: "gate/verdict", description, creator: config.posters[0], ...changes });

// Real Git object fixtures exercise graph topology, blob bytes, modes and merge
// conflict behaviour rather than mocking the tree comparison being tested.
function fixture() {
  const cwd = mkdtempSync(join(tmpdir(), "aeon-gate-fixture-"));
  let counter = 0;
  function run(args, input) {
    const result = spawnSync("git", args, { cwd, input, encoding: "utf8" });
    assert.equal(result.status, 0, result.stderr);
    return result.stdout.trim();
  }
  run(["init", "--quiet", "--initial-branch=main"]);
  function commit(files, parents = []) {
    const root = {};
    for (const [path, value] of Object.entries(files)) {
      const parts = path.split("/");
      let dir = root;
      for (const part of parts.slice(0, -1)) dir = dir[part] ??= {};
      dir[parts.at(-1)] = { value };
    }
    function treeOf(dir) {
      return run(["mktree"], Object.entries(dir).sort(([a], [b]) => a.localeCompare(b)).map(([path, entry]) => {
        if (!("value" in entry)) return `040000 tree ${treeOf(entry)}\t${path}\n`;
        const { content, mode } = typeof entry.value === "string" ? { content: entry.value, mode: "100644" } : entry.value;
        return `${mode} blob ${run(["hash-object", "-w", "--stdin"], content)}\t${path}\n`;
      }).join(""));
    }
    const tree = treeOf(root);
    return run(["-c", "user.name=Gate test fixture", "-c", "user.email=fixture@example.invalid", "commit-tree", tree,
      ...parents.flatMap((parent) => ["-p", parent]), "-m", `Fixture ${++counter}`]);
  }
  const git = createGit(cwd);
  function merge(first, second) {
    const result = git.mergeTree(first, second);
    assert.equal(result.status, 0, result.text);
    return run(["-c", "user.name=Gate test fixture", "-c", "user.email=fixture@example.invalid", "commit-tree", result.text,
      "-p", first, "-p", second, "-m", `Fixture merge ${++counter}`]);
  }
  const files = { shared: "original\n" };
  const base = commit(files);
  const reviewed = commit({ ...files, feature: "reviewed\n" }, [base]);
  const main = commit({ ...files, upstream: "main one\n" }, [base]);
  const head = merge(reviewed, main);
  const pr = (sha = reviewed, number = 411) => ({
    number, state: "open", head: { sha }, base: { sha: main, ref: "main", repo: { full_name: repository } },
  });
  const group = (headSha, baseSha = main) => ({
    head_sha: headSha, base_sha: baseSha, base_ref: "refs/heads/main", head_ref: "refs/heads/gh-readonly-queue/main/pr-411-fixture",
  });
  return { cwd, run, git, commit, merge, files, base, reviewed, main, head, pr, group };
}
const f = fixture();
const statuses = async (commit) => commit === f.reviewed ? [verdict()] : [];
const check = (head, changes = {}) => checkPullRequest({ pr: f.pr(head), repository, config, mainSha: f.main, git: f.git, statuses, ...changes });

test("an exact reviewed head passes", async () => {
  assert.deepEqual(await check(f.reviewed), { number: 411, head: f.reviewed, reviewed: f.reviewed, merges: 0, description });
});

test("only automatic merges of main preserve the reviewed branch diff", async () => {
  assert.equal((await check(f.head)).merges, 1);
  const main2 = f.commit({ ...f.files, upstream: "main two\n" }, [f.main]);
  const head2 = f.merge(f.head, main2);
  assert.equal((await check(head2, { mainSha: main2 })).merges, 2);
});

test("a new branch commit after review fails, even if empty or reverted", async () => {
  const files = { ...f.files, feature: "reviewed\n" };
  const extra = f.commit({ ...files, feature: "unreviewed\n" }, [f.reviewed]);
  const reverted = f.commit(files, [extra]);
  const empty = f.commit(files, [f.reviewed]);
  for (const head of [extra, reverted, empty, f.merge(extra, f.main)]) {
    await assert.rejects(check(head), /new branch commit requires review/);
  }
});

test("wrong posters, missing creators and check runs cannot authorize a verdict", async () => {
  for (const changes of [
    { creator: { login: "intruder", id: 999 } }, { creator: { login: "markus-barta", id: 999 } },
    { creator: { login: "intruder", id: 276789 } }, { creator: null }, { context: "gate/cross-family" },
  ]) await assert.rejects(check(f.reviewed, { statuses: async () => [verdict(changes)] }), /no trusted gate\/verdict/);
});

test("latest trusted verdict governs; untrusted posts cannot revoke it", async () => {
  const revoked = [verdict(), verdict({ id: 11, state: "failure" })];
  await assert.rejects(check(f.reviewed, { statuses: async () => revoked }), /not a valid ok verdict/);
  const untrusted = verdict({ id: 12, state: "failure", creator: { login: "intruder", id: 999 } });
  assert.equal(trustedVerdict([...revoked, untrusted], config.posters).state, "failure");
  assert.equal((await check(f.reviewed, { statuses: async () => [verdict(), untrusted] })).reviewed, f.reviewed);
  await assert.rejects(check(f.head, { statuses: async (sha) => sha === f.head ? revoked : statuses(sha) }), /not a valid ok verdict/);
});

test("success without reviewer model, route and review file is refused", async () => {
  for (const description of ["VERDICT: ok", "", "model=x; route=y", "model=; route=y; review=z", "model=x; route=; review=z", "model=x; route=y; review="]) {
    await assert.rejects(check(f.reviewed, { statuses: async () => [verdict({ description })] }), /not a valid ok verdict/);
  }
  for (const state of ["pending", "error", "failure"]) {
    await assert.rejects(check(f.reviewed, { statuses: async () => [verdict({ state })] }), /not a valid ok verdict/);
  }
});

test("extra blob, file mode or symlink changes hidden in a merge fail", async () => {
  const correct = { ...f.files, feature: "reviewed\n", upstream: "main one\n" };
  for (const feature of ["tampered\n", { content: "reviewed\n", mode: "100755" }, { content: "shared", mode: "120000" }]) {
    const evil = f.commit({ ...correct, feature }, [f.reviewed, f.main]);
    await assert.rejects(check(evil), /changes the reviewed branch diff/);
  }
});

test("manual conflict resolutions and merges from another branch fail", async () => {
  const conflictMain = f.commit({ ...f.files, feature: "main conflicting\n" }, [f.main]);
  const resolved = f.commit({ ...f.files, feature: "resolved\n", upstream: "main one\n" }, [f.reviewed, conflictMain]);
  await assert.rejects(check(resolved, { mainSha: conflictMain }), /resolves conflicts/);
  const other = f.commit({ ...f.files, other: "not main\n" }, [f.base]);
  await assert.rejects(check(f.merge(f.reviewed, other)), /new branch commit/);
  await assert.rejects(check(f.merge(f.main, f.reviewed)), /new branch commit/);
});

test("a two-PR merge group resolves and checks every PR", async () => {
  const second = f.commit({ ...f.files, feature2: "second reviewed\n" }, [f.base]);
  const firstMerge = f.merge(f.main, f.head);
  const groupHead = f.merge(firstMerge, second);
  const pulls = [f.pr(f.head), f.pr(second, 412)];
  const api = {
    pulls: async () => pulls, pull: async (number) => pulls.find((pr) => pr.number === number),
    statuses: async (sha) => sha === second ? [verdict()] : statuses(sha),
  };
  const event = { repository: { full_name: repository }, action: "checks_requested", merge_group: f.group(groupHead) };
  const result = await checkEvent({ eventName: "merge_group", event, config, repository, git: f.git, api });
  assert.deepEqual(result.map((pr) => [pr.number, pr.merges]), [[411, 1], [412, 0]]);
  await assert.rejects(checkEvent({ eventName: "merge_group", event, config, repository, git: f.git, api: { ...api, statuses } }), /PR #412/);
  assert.throws(() => resolveMergeGroup({ group: event.merge_group, pulls: [pulls[0]], git: f.git, config, repository }), /unresolved/);
});

test("merge groups fail on empty or changed heads and synthetic edits", () => {
  const merge = f.merge(f.main, f.reviewed);
  const pulls = [f.pr()];
  const resolve = (group, changes = {}) => resolveMergeGroup({ group, pulls, git: f.git, config, repository, ...changes });
  assert.throws(() => resolve(f.group(f.main)), /empty/);
  assert.throws(() => resolve(f.group(merge), { pulls: [f.pr(f.head)] }), /changed/);
  assert.throws(() => resolve({ ...f.group(merge), base_ref: "refs/heads/other" }), /configured main/);
  assert.throws(() => resolve(f.group(f.reviewed)), /merge commits required/);
  const evil = f.commit({ ...f.files, feature: "synthetic tampering\n", upstream: "main one\n" }, [f.main, f.reviewed]);
  assert.throws(() => resolve(f.group(evil)), /changes the reviewed branch diff/);
});

test("fork duplicates of the exact head do not stall the queue; each PR stays open", async () => {
  const duplicate = { ...f.pr(f.reviewed, 412), head: { sha: f.reviewed, repo: { full_name: "fork/paimos" } } };
  const pulls = [f.pr(), duplicate];
  const event = { repository: { full_name: repository }, action: "checks_requested", merge_group: f.group(f.merge(f.main, f.reviewed)) };
  let reads = 0;
  const api = { pulls: async () => pulls, pull: async (number) => pulls.find((pr) => pr.number === number),
    statuses: async (sha) => { reads++; return statuses(sha); } };
  const options = { eventName: "merge_group", event, config, repository, git: f.git, api };
  assert.deepEqual((await checkEvent(options)).map((pr) => pr.number), [411, 412]);
  assert.equal(reads, 1, "one verdict check per immutable SHA");
  const repeated = f.merge(event.merge_group.head_sha, f.reviewed);
  assert.deepEqual((await checkEvent({ ...options, event: { ...event, merge_group: f.group(repeated) } })).map((pr) => pr.number), [411, 412]);
  for (const change of [{ state: "closed" }, { head: { sha: f.head } }, { base: { ...duplicate.base, ref: "other" } },
    { base: { ...duplicate.base, repo: { full_name: "other/repo" } } }]) {
    await assert.rejects(checkEvent({ ...options, api: { ...api, pull: async (number) => number === 412 ? { ...duplicate, ...change } : pulls[0] } }), /PR #412 changed/);
  }
  await assert.rejects(checkEvent({ ...options, api: { ...api, statuses: async () => [] } }), /no trusted/);
  const altered = f.commit({ ...f.files, feature: "synthetic edit\n", upstream: "main one\n" }, [f.main, f.reviewed]);
  await assert.rejects(checkEvent({ ...options, event: { ...event, merge_group: f.group(altered) } }), /changes the reviewed branch diff/);
});

test("PR events reject stale heads and head changes, while main may advance independently", async () => {
  const pr = f.pr();
  const event = { repository: { full_name: repository }, pull_request: pr };
  const api = { pull: async () => pr, statuses };
  const options = { eventName: "pull_request", event, config, repository, git: f.git, api };
  assert.equal((await checkEvent(options))[0].reviewed, f.reviewed);
  await assert.rejects(checkEvent({ ...options, api: { ...api, statuses: async () => [] } }),
    /PR #411: no trusted gate\/verdict; a new branch commit requires review/);
  await assert.rejects(checkEvent({ ...options, event: { ...event, pull_request: f.pr(f.head) } }), /changed since/);
  assert.equal((await checkEvent({ ...options, api: { ...api, pull: async () => ({ ...pr, base: { ...pr.base, sha: f.base } }) } }))[0].reviewed, f.reviewed);
  let calls = 0;
  await assert.rejects(checkEvent({ ...options, api: { ...api, pull: async () => ++calls === 1 ? pr : f.pr(f.head) } }), /changed during/);
  for (const change of [{ state: "closed" }, { base: { ...pr.base, ref: "other" } }, { base: { ...pr.base, repo: { full_name: "other/repo" } } }]) {
    await assert.rejects(check(f.reviewed, { pr: { ...pr, ...change } }), /not open against/);
  }
  await assert.rejects(checkEvent({ ...options, eventName: "push" }), /only accepts/);
});

test("GitHub reads paginate and never accept partial status or PR evidence", async () => {
  const seen = [];
  const api = githubAPI({ repository, token: "fixture-token", fetchImpl: async (url, options) => {
    seen.push(url);
    assert.equal(options.method, "GET");
    assert.equal(options.redirect, "error");
    return { ok: true, json: async () => url.endsWith("page=1") ? Array.from({ length: 100 }, (_, i) => verdict({ id: i + 20 })) : [verdict({ id: 120 })] };
  } });
  assert.equal((await api.statuses(f.reviewed)).length, 101);
  assert.equal((await api.pulls("main")).length, 101);
  assert.equal(seen.length, 4);
  const failing = githubAPI({ repository, token: "fixture-token", fetchImpl: async () => ({ ok: false, status: 403 }) });
  await assert.rejects(failing.statuses(f.reviewed), /HTTP 403/);
  const incomplete = githubAPI({ repository, token: "fixture-token", fetchImpl: async () => ({ ok: true, json: async () => Array(100).fill(verdict()) }) });
  await assert.rejects(incomplete.pulls("main"), /pagination limit/);
});

test("invalid policy, IDs and SHAs fail closed", async () => {
  for (const policy of [null, {}, { ...config, schema: 2 }, { ...config, posters: [] }, { ...config, posters: [{ login: "markus-barta" }] }]) {
    assert.throws(() => validateConfig(policy), /invalid gate poster/);
  }
  await assert.rejects(check("main; echo fixture"), /invalid commit SHA/);
  assert.throws(() => githubAPI({ repository: "https://other.example", token: "fixture-token" }), /missing GitHub/);
});

test("diagnostic workflow is PR/queue-only, unconditional and cannot emit the App-bound context", () => {
  const gate = readFileSync(new URL("../.github/workflows/cross-family-preview.yml", import.meta.url), "utf8");
  assert.match(gate, /name: gate\/policy-preview\n/);
  assert.match(gate, /on:\n  pull_request:\n  merge_group:\n    types: \[checks_requested\]/);
  assert.doesNotMatch(gate, /\n\s+(?:if|push|workflow_dispatch|needs):|name: gate\/cross-family/);
  assert.match(gate, /runs-on: ubuntu-latest/);
  assert.match(gate, /statuses: read/);
  assert.match(gate, /pull-requests: read/);
  assert.match(gate, /persist-credentials: false/);
  assert.match(gate, /fetch-depth: 0/);
  assert.match(gate, /git show "\$GATE_BASE_SHA:scripts\/cross-family-gate.mjs"/);
  assert.match(gate, /git show "\$GATE_BASE_SHA:.github\/gate-posters.json"/);
  assert.match(gate, /non-required preview reports missing PR verdicts until review is posted/);
  assert.doesNotMatch(gate, /: write|continue-on-error|node --test|pull_request_target/);
  const ci = readFileSync(new URL("../.github/workflows/ci.yml", import.meta.url), "utf8");
  assert.doesNotMatch(ci, /gate\/cross-family/);
});

test("diagnostic preview skips absent base policy and preserves installed gate results", () => {
  const workflow = readFileSync(new URL("../.github/workflows/cross-family-preview.yml", import.meta.url), "utf8");
  const block = workflow.match(/        run: \|\n((?:          [^\n]*\n)+)/);
  assert.ok(block, "preview shell step must be present");
  const shell = block[1].replace(/^          /gm, "");
  const message = "cross-family gate bootstrap pending; preview skipped";
  const policyPath = ".github/gate-posters.json";
  const checkerPath = "scripts/cross-family-gate.mjs";
  const policy = JSON.stringify({ ...config, fixtureExitCode: 0 });
  const checker = `import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
const policy = JSON.parse(readFileSync(process.argv[2], 'utf8'));
assert.deepEqual(policy.posters, ${JSON.stringify(config.posters)});
console.log('trusted base gate ran');
process.exit(policy.fixtureExitCode);
`;
  function run(files, baseOverride, eventName = "pull_request", ref = "refs/pull/411/merge") {
    const runner = mkdtempSync(join(tmpdir(), "aeon-preview-runner-"));
    const summary = join(runner, "summary.txt");
    writeFileSync(summary, "existing summary\n");
    const base = f.commit(files, [f.main]);
    const result = spawnSync("bash", ["-c", shell], {
      cwd: f.cwd, encoding: "utf8", timeout: 10_000,
      env: { PATH: process.env.PATH, GATE_BASE_SHA: baseOverride ?? base, RUNNER_TEMP: runner, GITHUB_STEP_SUMMARY: summary,
        GITHUB_EVENT_NAME: eventName, GITHUB_REF: ref },
    });
    assert.ifError(result.error);
    assert.equal(result.signal, null);
    return { ...result, summary: readFileSync(summary, "utf8"), runner };
  }
  // Either missing file skips only the diagnostic; no extracted gate runs.
  for (const files of [{}, { [checkerPath]: checker }, { [policyPath]: policy }]) {
    const result = run(files);
    assert.equal(result.status, 0, result.stderr);
    assert.equal(result.stdout, `::notice::${message}\n`);
    assert.equal(result.summary, `existing summary\n${message}\n`);
    assert.equal(existsSync(join(result.runner, "cross-family-gate.mjs")), false);
    assert.equal(existsSync(join(result.runner, "gate-posters.json")), false);
  }
  // An installed checker executes from the trusted base and retains its exit status.
  for (const fixtureExitCode of [0, 7]) {
    const result = run({ [checkerPath]: checker, [policyPath]: JSON.stringify({ ...config, fixtureExitCode }) });
    assert.equal(result.status, fixtureExitCode, result.stderr);
    assert.equal(result.stdout, "trusted base gate ran\n");
    assert.equal(result.summary, "existing summary\n");
    assert.equal(readFileSync(join(result.runner, "cross-family-gate.mjs"), "utf8"), checker);
    assert.deepEqual(JSON.parse(readFileSync(join(result.runner, "gate-posters.json"), "utf8")), { ...config, fixtureExitCode });
  }
  const invalid = run({}, "invalid-base");
  assert.equal(invalid.status, 1, invalid.stderr);
  assert.equal(invalid.stdout, "Invalid gate base commit\n");
  assert.equal(invalid.summary, "existing summary\n");
  // A main push has no PR base SHA. Skip it explicitly before base-policy reads,
  // even if the installed checker would otherwise report a missing verdict.
  const pushNotice = "gate/policy-preview: main push skipped; review verdicts are checked on PR and merge-group heads (non-required diagnostic)";
  const installed = { [checkerPath]: checker, [policyPath]: JSON.stringify({ ...config, fixtureExitCode: 7 }) };
  const skipped = run(installed, "", "push", "refs/heads/main");
  assert.equal(skipped.status, 0, skipped.stderr);
  assert.equal(skipped.stdout, `::notice::${pushNotice}\n`);
  assert.equal(skipped.summary, `existing summary\n${pushNotice}\n`);
  assert.equal(existsSync(join(skipped.runner, "cross-family-gate.mjs")), false);
  assert.equal(existsSync(join(skipped.runner, "gate-posters.json")), false);
  // A main ref alone, a push to another branch, and queue events cannot skip
  // an installed checker's diagnostic failure.
  for (const [eventName, ref] of [["pull_request", "refs/heads/main"], ["push", "refs/heads/feature"],
    ["merge_group", "refs/heads/gh-readonly-queue/main/pr-411-fixture"]]) {
    const checked = run(installed, undefined, eventName, ref);
    assert.equal(checked.status, 7, checked.stderr);
    assert.equal(checked.stdout, "trusted base gate ran\n");
    assert.equal(checked.summary, "existing summary\n");
  }
  // The actual CLI also handles main-push invocations without config, Git or
  // read credentials; it emits a skip notice, never claims a gate passed.
  const runner = mkdtempSync(join(tmpdir(), "aeon-preview-cli-"));
  const eventPath = join(runner, "event.json");
  const summary = join(runner, "summary.txt");
  const configPath = join(runner, "policy.json");
  const push = { repository: { full_name: repository }, ref: "refs/heads/main", after: f.merge(f.main, f.reviewed) };
  writeFileSync(eventPath, JSON.stringify(push));
  writeFileSync(summary, "existing summary\n");
  const env = { PATH: process.env.PATH, GITHUB_EVENT_NAME: "push", GITHUB_EVENT_PATH: eventPath,
    GITHUB_REPOSITORY: repository, GITHUB_STEP_SUMMARY: summary };
  const runCLI = (changes = {}) => spawnSync(process.execPath,
    [new URL("./cross-family-gate.mjs", import.meta.url).pathname, configPath],
    { cwd: runner, encoding: "utf8", timeout: 10_000, env: { ...env, ...changes } });
  const cli = runCLI();
  assert.ifError(cli.error);
  assert.equal(cli.status, 0, cli.stderr);
  assert.equal(cli.stdout, `::notice::${pushNotice}\n`);
  assert.equal(cli.stderr, "");
  assert.equal(readFileSync(summary, "utf8"), `existing summary\n${pushNotice}\n`);
  assert.equal(existsSync(configPath), false);
  const withoutSummary = runCLI({ GITHUB_STEP_SUMMARY: "" });
  assert.ifError(withoutSummary.error);
  assert.equal(withoutSummary.status, 0, withoutSummary.stderr);
  assert.equal(withoutSummary.stdout, `::notice::${pushNotice}\n`);
  // The skip is confined to main pushes from the configured repository.
  writeFileSync(configPath, JSON.stringify(config));
  for (const [event, eventName, expected] of [
    [{ ...push, ref: "refs/heads/feature" }, "push", /only accepts pull_request or merge_group/],
    [push, "workflow_dispatch", /only accepts pull_request or merge_group/],
    [{ ...push, repository: { full_name: "fork/paimos" } }, "push", /event repository mismatch/],
  ]) {
    writeFileSync(eventPath, JSON.stringify(event));
    const rejected = runCLI({ GITHUB_EVENT_NAME: eventName, GH_TOKEN: "fixture-token" });
    assert.ifError(rejected.error);
    assert.equal(rejected.status, 1, rejected.stderr);
    assert.equal(rejected.stdout, "");
    assert.match(rejected.stderr, expected);
    assert.equal(readFileSync(summary, "utf8"), `existing summary\n${pushNotice}\n`);
  }
});

test("required status binds to the external App, preserving the existing ruleset separately", () => {
  const ruleset = JSON.parse(readFileSync(new URL("../.github/cross-family-ruleset.json", import.meta.url), "utf8"));
  assert.deepEqual(ruleset.conditions.ref_name, { include: ["refs/heads/main"], exclude: [] });
  const checks = ruleset.rules.find((rule) => rule.type === "required_status_checks");
  assert.deepEqual(checks.parameters.required_status_checks, [{ context: "gate/cross-family", integration_id: statusApp.id }]);
  assert.notEqual(statusApp.id, 15368);
  assert.equal(ruleset.rules.find((rule) => rule.type === "pull_request").parameters.require_code_owner_review, true);
  const owners = readFileSync(new URL("../.github/CODEOWNERS", import.meta.url), "utf8");
  for (const path of ["/.github/CODEOWNERS", "/.github/workflows/", "/.github/gate-posters.json", "/.github/cross-family-ruleset.json",
    "/scripts/cross-family-gate*", "/scripts/post-cross-family-gate*", "/scripts/trusted-cross-family-poster*", "/scripts/ci-runner-guard/"]) {
    assert.ok(owners.includes(`${path} @markus-barta`), path);
  }
});

test("PR and fork workflow skip/exit-zero edits cannot replace executable main policy", async () => {
  const fixturePolicy = { ...f.files, ".github/gate-posters.json": JSON.stringify(config) };
  for (const file of ["cross-family-gate.mjs", "post-cross-family-gate.mjs"]) {
    fixturePolicy[`scripts/${file}`] = readFileSync(new URL(file, import.meta.url), "utf8");
  }
  const main = f.commit(fixturePolicy, [f.main]);
  const marker = join(f.cwd, "attacker-code-executed");
  const evilCode = `import { writeFileSync } from 'node:fs'; writeFileSync(${JSON.stringify(marker)}, 'bypass'); process.exit(0);`;
  for (const workflow of ["if: false", "steps: [{run: 'exit 0'}]"]) {
    const head = f.commit({ ...fixturePolicy, ".github/workflows/ci.yml": `on: pull_request\njobs:\n  forged:\n    name: gate/cross-family\n    ${workflow}\n`,
      "scripts/cross-family-gate.mjs": evilCode, "scripts/post-cross-family-gate.mjs": evilCode,
      ".github/gate-posters.json": JSON.stringify({ ...config, posters: [{ login: "intruder", id: 999 }] }) }, [main]);
    const dir = extractPolicy(f.cwd, main);
    const trusted = await import(pathToFileURL(join(dir, "post-cross-family-gate.mjs")).href);
    const pr = { ...f.pr(head), base: { ...f.pr().base, sha: main }, head: { sha: head, repo: { full_name: "fork/paimos" } } };
    const event = { repository: { full_name: repository }, action: "opened", pull_request: pr };
    const emitted = [];
    const result = await trusted.postGate({ eventName: "pull_request", event, config: JSON.parse(readFileSync(join(dir, "gate-posters.json"), "utf8")),
      git: f.git, api: { pull: async () => pr, statuses: async () => [verdict({ context: "gate/cross-family" }), verdict({ creator: { login: "intruder", id: 999 } })] },
      write: true, publish: async (sha, body) => emitted.push({ sha, ...body }) });
    assert.equal(result.state, "failure");
    assert.deepEqual(emitted.map((status) => [status.sha, status.state]), [[head, "pending"], [head, "failure"]]);
    assert.equal(existsSync(marker), false);
  }
  assert.throws(() => extractPolicy(f.cwd, f.base), /Git operation failed/);
  assert.throws(() => refreshMain(f.cwd), /bare mirror/);
});

test("external poster revokes stale success before reads and covers the exact queue SHA", async () => {
  const pr = f.pr();
  const event = { repository: { full_name: repository }, action: "opened", pull_request: pr };
  const emitted = [];
  const publish = async (sha, body) => emitted.push({ sha, ...body });
  const options = { eventName: "pull_request", event, config, git: f.git, api: { pull: async () => pr, statuses }, publish };
  assert.equal((await postGate(options)).state, "success");
  assert.equal(emitted.length, 0, "dry run is the default");
  assert.equal((await postGate({ ...options, write: true })).state, "success");
  assert.deepEqual(emitted.map((status) => [status.sha, status.state]), [[f.reviewed, "pending"], [f.reviewed, "success"]]);
  emitted.length = 0;
  assert.equal((await postGate({ ...options, write: true, api: { ...options.api, statuses: async () => { throw new Error("fixture transport failure"); } } })).state, "failure");
  assert.deepEqual(emitted.map((status) => status.state), ["pending", "failure"]);
  const queueHead = f.merge(f.main, f.reviewed);
  emitted.length = 0;
  const queued = await postGate({ ...options, write: true, eventName: "merge_group",
    event: { repository: event.repository, action: "checks_requested", merge_group: f.group(queueHead) },
    api: { ...options.api, pulls: async () => [pr] } });
  assert.equal(queued.state, "success");
  assert.deepEqual(emitted.map((status) => status.sha), [queueHead, queueHead]);
  await assert.rejects(postGate({ ...options, write: true, publish: async () => { throw new Error("fixture denied write"); } }), /denied write/);
});

test("trusted bootstrap fetch/extraction failures revoke an earlier success before reading main", async () => {
  const event = { repository: { full_name: repository }, action: "opened", pull_request: f.pr() };
  for (const failure of ["fetch refused", "main policy missing", "poster crashed"]) {
    const statuses = [];
    await assert.rejects(withPendingStatus({ eventName: "pull_request", event, write: true,
      publish: async (target, body) => statuses.push({ target, ...body }),
      run: () => { assert.equal(statuses[0].state, "pending"); throw new Error(failure); } }), new RegExp(failure));
    assert.deepEqual(statuses.map((status) => [status.target, status.state]), [[f.reviewed, "pending"], [f.reviewed, "failure"]]);
  }
});

test("push/dispatch/Actions/invalid events cannot publish even a skipped or successful required status", async () => {
  const pr = f.pr();
  const event = { repository: { full_name: repository }, action: "opened", pull_request: pr };
  let writes = 0;
  const options = { eventName: "pull_request", event, config, git: f.git, api: { pull: async () => pr, statuses }, write: true,
    publish: async () => { writes++; } };
  for (const eventName of ["push", "workflow_dispatch", "pull_request_target"]) {
    await assert.rejects(postGate({ ...options, eventName }), /only accepts/);
  }
  await assert.rejects(postGate({ ...options, actions: true }), /must not run in GitHub Actions/);
  for (const change of [{ action: "closed" }, { repository: { full_name: "fork/paimos" } },
    { pull_request: { ...pr, head: { sha: "main" } } }]) assert.throws(() => statusTarget("pull_request", { ...event, ...change }));
  assert.equal(writes, 0);
});

test("status writer accepts only the external App and never follows redirects", async () => {
  const body = { context: "gate/cross-family", state: "success", description: "fixture" };
  const writer = (creator, response = {}) => statusWriter({ token: "fixture-token", fetchImpl: async (url, options) => {
    assert.equal(url, `https://api.github.com/repos/${repository}/statuses/${f.reviewed}`);
    assert.equal(options.method, "POST");
    assert.equal(options.redirect, "error");
    assert.deepEqual(JSON.parse(options.body), body);
    return { ok: true, json: async () => ({ ...body, creator }), ...response };
  } });
  await writer({ login: statusApp.login, type: "Bot" })(f.reviewed, body);
  for (const creator of [{ login: "markus-barta", type: "User" }, { login: "github-actions[bot]", type: "Bot" }]) {
    await assert.rejects(writer(creator)(f.reviewed, body), /not posted by/);
  }
  await assert.rejects(writer(null, { ok: false, status: 403 })(f.reviewed, body), /HTTP 403/);
});
