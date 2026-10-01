// SPDX-License-Identifier: AGPL-3.0-only
import assert from "node:assert/strict";
import { mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";
import { checkEvent, checkPullRequest, createGit, githubAPI, resolveMergeGroup, trustedVerdict, validateConfig } from "./cross-family-gate.mjs";

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
    const tree = run(["mktree"], Object.entries(files).sort(([a], [b]) => a.localeCompare(b)).map(([path, value]) => {
      const { content, mode } = typeof value === "string" ? { content: value, mode: "100644" } : value;
      return `${mode} blob ${run(["hash-object", "-w", "--stdin"], content)}\t${path}\n`;
    }).join(""));
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
  return { git, commit, merge, files, base, reviewed, main, head, pr, group };
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

test("merge groups fail on empty, ambiguous or changed heads and synthetic edits", () => {
  const merge = f.merge(f.main, f.reviewed);
  const pulls = [f.pr()];
  const resolve = (group, changes = {}) => resolveMergeGroup({ group, pulls, git: f.git, config, repository, ...changes });
  assert.throws(() => resolve(f.group(f.main)), /empty/);
  assert.throws(() => resolve(f.group(merge), { pulls: [...pulls, f.pr(f.reviewed, 412)] }), /ambiguous/);
  assert.throws(() => resolve(f.group(merge), { pulls: [f.pr(f.head)] }), /changed/);
  assert.throws(() => resolve({ ...f.group(merge), base_ref: "refs/heads/other" }), /configured main/);
  assert.throws(() => resolve(f.group(f.reviewed)), /merge commits required/);
  const evil = f.commit({ ...f.files, feature: "synthetic tampering\n", upstream: "main one\n" }, [f.main, f.reviewed]);
  assert.throws(() => resolve(f.group(evil)), /changes the reviewed branch diff/);
});

test("PR events reject stale heads and head changes, while main may advance independently", async () => {
  const pr = f.pr();
  const event = { repository: { full_name: repository }, pull_request: pr };
  const api = { pull: async () => pr, statuses };
  const options = { eventName: "pull_request", event, config, repository, git: f.git, api };
  assert.equal((await checkEvent(options))[0].reviewed, f.reviewed);
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

test("required check is PR/queue-only, hosted and uses base policy with a read-only token", () => {
  const workflow = readFileSync(new URL("../.github/workflows/ci.yml", import.meta.url), "utf8");
  const gate = workflow.split("  cross-family:\n")[1].split("\n  runner-route:")[0];
  assert.match(gate, /name: gate\/cross-family\n/);
  assert.match(gate, /if: \$\{\{ github.event_name == 'pull_request' \|\| github.event_name == 'merge_group' \}\}/);
  assert.match(gate, /runs-on: ubuntu-latest/);
  assert.match(gate, /statuses: read/);
  assert.match(gate, /pull-requests: read/);
  assert.match(gate, /persist-credentials: false/);
  assert.match(gate, /fetch-depth: 0/);
  assert.match(gate, /git show "\$GATE_BASE_SHA:scripts\/cross-family-gate.mjs"/);
  assert.match(gate, /git show "\$GATE_BASE_SHA:.github\/gate-posters.json"/);
  assert.doesNotMatch(gate, /: write|continue-on-error|node --test|pull_request_target/);
});
