// SPDX-License-Identifier: AGPL-3.0-only
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { chmodSync, existsSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import {
  assertReadOnly,
  buildReport,
  defaultGh,
  fetchInputs,
  ghRead,
  normalizeInput,
  parseArgs,
  validateEvidence,
} from "./release-timing.mjs";

const fixturePath = join(dirname(fileURLToPath(import.meta.url)), "testdata/release-timing/section1.json");
const fixture = JSON.parse(readFileSync(fixturePath, "utf8"));
const reviewFixture = JSON.parse(readFileSync(join(dirname(fixturePath), "review-r2.json"), "utf8"));
const finalReviewFixture = JSON.parse(readFileSync(join(dirname(fixturePath), "review-r3.json"), "utf8"));
const now = new Date("2026-10-01T00:00:00Z");

function report(opts = {}, input = fixture) {
  return buildReport(input, { now, ...opts });
}

function byLabel(rows) {
  return Object.fromEntries(rows.map((row) => [row.label, row]));
}

test("section 1 walls, CI, digest and pin durations", () => {
  const rows = byLabel(report().releases);
  assert.equal(rows["12"].elapsed_s, 7648);
  assert.equal(rows["12"].wall_min, 128);
  assert.equal(rows["12"].section1.wall_match, true);
  assert.equal(rows["12"].pr_to_merge_s, 659);
  assert.equal(rows["12"].ci.success_s, 644);
  assert.equal(rows["12"].ci.failed_s, null);
  assert.ok(rows["12"].notes.some((note) => note.includes("127 min 28 s") && note.includes("128 min")));

  assert.equal(rows["12b"].wall_min, 38);
  assert.equal(rows["12b"].section1.published_wall_min, 38);
  assert.equal(rows["12b"].ci.success_s, 557);
  assert.equal(rows["12b"].pr_to_merge_s, 686);
  assert.equal(rows["12b"].pr_open_to_live_s, 2242);
  assert.equal(rows["12b"].pr_open_to_live_wall_min, 38);
  assert.ok(rows["12b"].notes.some((note) => note.includes("9.3 min") && note.includes("not PR→merge")));

  assert.equal(rows["12c"].wall_min, 84);
  assert.equal(rows["12c"].elapsed_s, 5009);
  assert.equal(rows["12c"].ci.failure_and_fix_s, 2241);
  assert.equal(rows["12c"].ci.cut_to_live_without_failure_s, 2768);
  assert.equal(rows["12c"].ci.cut_to_live_without_failure_min, 46);
  assert.ok(rows["12c"].notes.some((note) => note.includes("46 min 8 s") && note.includes("84−37")));

  assert.equal(rows["12d"].elapsed_s, 3477);
  assert.equal(rows["12d"].wall_min, 58);
  assert.equal(rows["12d"].section1.wall_match, true);
  assert.equal(rows["12d"].pr_to_merge_s, 1372);
  assert.equal(rows["12d"].ci.failed_s, 642);
  assert.equal(rows["12d"].ci.success_s, 666);
  assert.equal(rows["12d"].ci.failure_and_fix_s, 679);
  assert.equal(rows["12d"].ci.cut_to_live_without_failure_min, 47);
  assert.equal(rows["12d"].digest_after_tag_s, 641);
  assert.equal(rows["12d"].pin.number, 881);
  assert.equal(rows["12d"].pin.open_to_merge_s, 646);
  assert.deepEqual(rows["12d"].pin.long_pole, { name: "Pharos fleet release compatibility", s: 559 });
  assert.ok(rows["12d"].notes.some((note) => note.includes("10.7 min") && note.includes("641s")));
  assert.ok(rows["12d"].notes.some((note) => note.includes("9 min 19 s") && note.includes("9.4 min")));
  assert.ok(rows["12d"].notes.some((note) => note.includes("47 min")));
});

test("gate-ok and rollback baselines are explained when unmeasured", () => {
  const result = report();
  assert.equal(result.releases.every((row) => row.gate_ok_to_live_s == null), true);
  assert.equal(result.releases.every((row) => row.rollback_s == null), true);
  const gate = result.gaps.find((gap) => gap.id === "gate-ok");
  const rollback = result.gaps.find((gap) => gap.id === "rollback");
  assert.ok(gate.text.includes("3-5 h"));
  assert.ok(rollback.text.includes("15 min"));
  assert.ok(rollback.text.includes("646-656s"));
});

test("--since and --release filters do not treat 12 as a prefix", () => {
  assert.deepEqual(report({ release: "12" }).releases.map((row) => row.label), ["12"]);
  assert.deepEqual(report({ release: "12d" }).releases.map((row) => row.label), ["12d"]);
  assert.deepEqual(report({ release: "v260929232203.0.0" }).releases.map((row) => row.label), ["12d"]);
  assert.deepEqual(report({ release: "stable110" }).releases.map((row) => row.label), ["12d"]);
  assert.deepEqual(report({ since: "2026-09-29T23:00:00Z" }).releases.map((row) => row.label), ["12d"]);
  assert.deepEqual(report({ since: "2026-09-29T21:00:00Z" }).releases.map((row) => row.label), ["12c", "12d"]);
});

test("gate median uses rollout tickets, and a rollback interval is the rollout span", () => {
  const result = buildReport({
    workflow_runs: [{
      id: "fixture-release",
      workflowName: "Release",
      event: "push",
      conclusion: "success",
      createdAt: "2026-09-30T00:00:00Z",
      updatedAt: "2026-09-30T00:10:00Z",
      headBranch: "v260930000000.0.0",
      headSha: "abc",
      jobs: [{ name: "image", steps: [{ name: "Record pushed digest", started_at: "2026-09-30T00:04:30Z", completed_at: "2026-09-30T00:04:30Z" }] }],
    }],
    pull_requests: [],
    pin_pull_requests: [],
    statuses: [{ id: "fixture-status-1", sha: "abc", context: "gate/cross-family", state: "success", updated_at: "2026-09-29T20:00:00Z" }],
    rollouts: [{
      direction: "forward",
      release: "99",
      version: "260930000000.0.0",
      sequence: 99,
      cut_at: "2026-09-29T21:00:00Z",
      live_at: "2026-09-30T01:00:00Z",
      rollback_started_at: "2026-09-30T02:00:00Z",
      rollback_finished_at: "2026-09-30T02:15:00Z",
      tickets: [
        { key: "AEON-1", gate_ok_at: "2026-09-29T20:00:00Z" },
        { key: "AEON-2", gate_ok_at: "2026-09-29T22:00:00Z" },
        { key: "AEON-3", gate_ok_at: "2026-09-29T21:00:00Z" },
      ],
    }],
  }, { now });
  const row = result.releases[0];
  assert.equal(row.digest_after_tag_s, 270);
  assert.equal(row.rollback_s, 900);
  assert.equal(row.rollback_source, "rollout");
  // live 01:00 minus 22:00, 21:00 and 20:00 is 3h, 4h, 5h. Median is 4h.
  assert.equal(row.gate_ok_to_live_s, 4 * 3600);
  assert.equal(row.gate_samples, 3);
  assert.equal(result.gaps.length, 0);
});

test("a commit status supplies gate-ok when the rollout has no ticket times", () => {
  const result = buildReport({
    workflow_runs: [{
      id: "fixture-release",
      workflowName: "Release", event: "push", conclusion: "success",
      createdAt: "2026-09-30T00:00:00Z", updatedAt: "2026-09-30T00:10:00Z",
      headBranch: "v260930000000.0.0", headSha: "abc",
    }],
    pull_requests: [],
    pin_pull_requests: [{ number: 1, title: "rollback v260930000000.0.0", createdAt: "2026-09-30T03:00:00Z", mergedAt: "2026-09-30T03:12:00Z", checks: [] }],
    statuses: [
      { id: "fixture-status-2", sha: "abc", context: "gate/cross-family", state: "success", updated_at: "2026-09-29T22:00:00Z" },
      { id: "fixture-status-3", sha: "other", context: "gate/cross-family", state: "success", updated_at: "2026-09-29T10:00:00Z" },
      { id: "fixture-status-4", sha: "abc", context: "ci", state: "success", updated_at: "2026-09-29T23:00:00Z" },
    ],
    rollouts: [{ direction: "forward", release: "99", version: "260930000000.0.0", cut_at: "2026-09-29T23:00:00Z", live_at: "2026-09-30T01:00:00Z" }],
  }, { now });
  assert.equal(result.releases[0].gate_ok_to_live_s, 3 * 3600);
  assert.equal(result.releases[0].rollback_s, 12 * 60);
  assert.equal(result.releases[0].rollback_source, "pin");
});

test("gh commands that write are refused", () => {
  assert.throws(() => assertReadOnly(["workflow", "run", "release.yml"]), /refusing/);
  assert.throws(() => assertReadOnly(["run", "rerun", "1"]), /refusing/);
  assert.throws(() => assertReadOnly(["release", "edit", "v1"]), /refusing/);
  assert.throws(() => assertReadOnly(["api", "--method", "POST", "repos/inspr-at/paimos/dispatches"]), /non-GET/);
  assert.doesNotThrow(() => assertReadOnly(ghRead("statuses", { repo: "inspr-at/paimos", sha: "abc", page: 1, pageSize: 100 })));
  assert.doesNotThrow(() => assertReadOnly(ghRead("pin-view", { repo: "inspr-at/paimos", number: 1 })));
});

test("fetch asks GitHub for lists and GET details only", () => {
  const calls = [];
  const gh = (args) => {
    calls.push(args);
    const text = args.join(" ");
    if (text.includes("release.yml")) {
      return [{
        databaseId: 1, workflowName: "Release", event: "push", conclusion: "success",
        createdAt: "2026-09-30T00:00:00Z", updatedAt: "2026-09-30T00:10:00Z",
        headBranch: "v260930000000.0.0", headSha: "abc",
      }];
    }
    if (text.includes("/pulls?") || (args[0] === "pr" && args[1] === "list" && text.includes("inspr-at/paimos"))) {
      return [{
        number: 7, title: "Release", createdAt: "2026-09-29T23:00:00Z", mergedAt: "2026-09-30T00:00:00Z",
        headRefName: "rel/r99", mergeCommit: { oid: "abc" },
      }];
    }
    if (text.includes("search/issues") || (args[0] === "pr" && args[1] === "list")) return { total_count: 0, items: [] };
    if (text.includes("/jobs")) {
      return { jobs: [{ id: 301, name: "image", steps: [{ name: "Record pushed digest", started_at: "2026-09-30T00:04:00Z", completed_at: "2026-09-30T00:04:30Z" }] }] };
    }
    if (text.includes("ci.yml") || args[0] === "run") return { total_count: 0, workflow_runs: [] };
    if (text.includes("/status")) return { statuses: [] };
    throw new Error(`unexpected gh ${text}`);
  };
  const raw = fetchInputs({ release: "99", gh });
  for (const args of calls) assertReadOnly(args);
  assert.ok(calls.some((args) => args[0] === "api" && args.length === 2));
  const row = buildReport(raw, { now, release: "99" }).releases[0];
  assert.equal(row.digest_after_tag_s, 270);
  assert.equal(row.pr_to_merge_s, 3600);
  assert.equal(row.cut_to_live_s, null);

  const quiet = [];
  fetchInputs({
    release: "missing",
    gh: (args) => {
      quiet.push(args);
      if (args.includes("release.yml")) return [];
      return [];
    },
  });
  assert.equal(quiet.some((args) => /\/jobs|\/statuses|\/attempts/.test(args.join(" ")) || (args[0] === "pr" && args[1] === "view")), false);
});

test("parseArgs rejects a missing value", () => {
  assert.throws(() => parseArgs(["--since"]), /missing value/);
  assert.throws(() => parseArgs(["--nope"]), /unknown argument/);
});

test("fixture mode prints JSON and does not call gh", () => {
  const bin = mkdtempSync(join(tmpdir(), "aeon-416-"));
  writeFileSync(join(bin, "gh"), "#!/bin/sh\necho gh-called >&2\nexit 99\n");
  chmodSync(join(bin, "gh"), 0o755);
  const run = spawnSync(process.execPath, ["scripts/release-timing.mjs", "--fixture", fixturePath, "--release", "12d", "--json"], {
    encoding: "utf8",
    env: { PATH: `${bin}:${process.env.PATH}` },
  });
  assert.equal(run.status, 0, run.stderr);
  assert.equal(run.stderr.includes("gh-called"), false);
  const body = JSON.parse(run.stdout);
  assert.equal(body.schema, "aeon.release-timing.v1");
  assert.equal(body.sources.fixture, true);
  assert.equal(body.sources.github, false);
  assert.equal(body.releases.length, 1);
  assert.equal(body.releases[0].wall_min, 58);
  assert.equal(body.releases[0].digest_after_tag_s, 641);
});

function releaseInput(extra = {}) {
  return {
    workflow_runs: [{
      id: "fixture-release",
      workflowName: "Release",
      event: "push",
      conclusion: "success",
      createdAt: "2026-09-30T00:00:00Z",
      updatedAt: "2026-09-30T00:10:00Z",
      headBranch: "v260930000000.0.0",
      headSha: "abc",
    }],
    pull_requests: [{
      number: 7,
      title: "Release v260930000000.0.0",
      createdAt: "2026-09-30T01:00:00Z",
      mergedAt: "2026-09-30T02:00:00Z",
      headRefName: "rel/r99",
      mergeCommit: { oid: "abc" },
    }],
    pin_pull_requests: [],
    statuses: [],
    rollouts: [{
      direction: "forward",
      release: "99",
      version: "260930000000.0.0",
      sequence: 99,
      cut_at: "2026-09-30T01:00:00Z",
      live_at: "2026-09-30T01:30:00Z",
    }],
    ...extra,
  };
}

test("r3 live rollback records cannot enter the forward set, in either order", () => {
  const forward = finalReviewFixture.forward;
  for (const direction of [undefined, "rollback"]) {
    const rollback = { ...finalReviewFixture.live_rollback, direction };
    for (const rollouts of [[forward, rollback], [rollback, forward]]) {
      const row = buildReport(releaseInput({ rollouts }), { now }).releases[0];
      assert.equal(row.elapsed_s, 1800);
      assert.equal(row.cut_at, forward.cut_at);
      assert.equal(row.rollback_s, 900);
    }
  }
});

test("r3 a sole full-schema rollback or overlapping forward is unknown", () => {
  for (const direction of [undefined, "rollback", "forward"]) {
    const rollout = { ...finalReviewFixture.live_rollback, direction };
    const row = buildReport(releaseInput({ rollouts: [rollout] }), { now }).releases[0];
    assert.equal(row.elapsed_s, null);
    assert.equal(row.cut_at, null);
    assert.equal(row.live_at, null);
    assert.ok(row.reasons.cut_to_live);
  }
});

test("r3 outcome alone never establishes forward or rollback direction", () => {
  for (const outcome of ["live", "rollback", "failed", ""]) {
    const rollout = { ...finalReviewFixture.forward, direction: undefined, outcome };
    const row = buildReport(releaseInput({ rollouts: [rollout] }), { now }).releases[0];
    assert.equal(row.elapsed_s, null);
    assert.match(row.reasons.cut_to_live, /direction/);
    assert.equal(row.rollback_s, null);
  }
});

test("r3 complete forward intervals take precedence over incomplete siblings", () => {
  const forward = finalReviewFixture.forward;
  for (const bounds of finalReviewFixture.incomplete_forwards) {
    const incomplete = { ...forward, ...bounds };
    for (const rollouts of [[forward, incomplete], [incomplete, forward]]) {
      const row = buildReport(releaseInput({ rollouts }), { now }).releases[0];
      assert.equal(row.elapsed_s, 1800);
      assert.equal(row.cut_at, forward.cut_at);
      assert.equal(row.evidence.cut_to_live.state, "complete");
    }
  }
});

test("r3 conflicting complete forward records stay ambiguous", () => {
  const forward = finalReviewFixture.forward;
  const other = { ...forward, cut_at: "2026-09-30T00:00:00Z" };
  for (const rollouts of [[forward, other], [other, forward]]) {
    const row = buildReport(releaseInput({ rollouts }), { now }).releases[0];
    assert.equal(row.elapsed_s, null);
    assert.equal(row.live_at, null);
    assert.equal(row.pr_open_to_live_s, null);
    assert.match(row.reasons.cut_to_live, /ambiguous/);
  }
});

test("r3 reruns require numbered coverage before any CI arithmetic", () => {
  const base = releaseInput();
  for (const numbered of [false, true]) {
    const rerun = structuredClone(finalReviewFixture.unnumbered_rerun);
    if (numbered) rerun.attempts.forEach((attempt, i) => { attempt.number = i + 1; });
    const row = buildReport(releaseInput({ workflow_runs: [...base.workflow_runs, rerun] }), { now }).releases[0];
    assert.equal(row.ci.completeness.state, numbered ? "complete" : "partial");
    assert.equal(row.ci.success_s, numbered ? 300 : null);
    assert.equal(row.ci.attempts, numbered ? 3 : undefined);
    if (!numbered) assert.match(row.ci.reason, /attempt history unavailable/);
  }
});

function statusPagesInput(pages, calls, options = {}) {
  const fallback = pageFixtureGh(calls, { ids: [9] });
  return fetchInputs({
    pageSize: 2, listCap: 10, rollouts: [finalReviewFixture.forward], ...options,
    gh: (args) => {
      const url = new URL(args.at(-1), "https://api.github.com/");
      if (!url.pathname.endsWith("/statuses")) return fallback(args);
      calls.push(args);
      return { total_count: 3, statuses: pages[Number(url.searchParams.get("page")) - 1] || [] };
    },
  });
}

test("r3 repeated id-less pages stay partial and cannot establish a gate median", () => {
  const calls = [];
  const raw = statusPagesInput(finalReviewFixture.idless_status_pages, calls);
  assert.ok(raw.collection.truncated.includes("statuses"));
  assert.ok(calls.some((args) => args.at(-1).includes("/statuses?page=3&per_page=2")));
  const row = buildReport(raw, { now }).releases[0];
  assert.equal(row.gate_ok_to_live_s, null);
  assert.equal(row.gate_reason, "truncated");
});

test("r3 pagination counts deduplicated stable ids before claiming completeness", () => {
  const calls = [];
  const raw = statusPagesInput(finalReviewFixture.stable_status_pages, calls);
  assert.deepEqual(raw.statuses.map((status) => status.id), [1, 2, 3]);
  assert.equal(raw.collection.truncated.includes("statuses"), false);
  assert.equal(buildReport(raw, { now }).releases[0].gate_ok_to_live_s, 4800);
});

test("r3 fixture rows without stable ids remain partial after normalization", () => {
  const raw = releaseInput({
    rollouts: [finalReviewFixture.forward],
    statuses: finalReviewFixture.idless_status_pages[0].map((status) => ({ ...status, sha: "abc" })),
  });
  for (const input of [raw, normalizeInput(raw)]) {
    const row = buildReport(input, { now }).releases[0];
    assert.equal(row.gate_ok_to_live_s, null);
    assert.equal(row.gate_reason, "truncated");
  }
});

test("r3 an unknown rollout remains visible under its release label", () => {
  const result = buildReport({ rollouts: [{ ...finalReviewFixture.forward, cut_at: null }] }, { now, release: "99" });
  assert.equal(result.releases.length, 1);
  assert.equal(result.releases[0].elapsed_s, null);
  assert.match(result.releases[0].reasons.cut_to_live, /missing/);
});

test("r3 id-less CI rows cannot produce durations or attempt counts", () => {
  const base = releaseInput();
  const rerun = structuredClone(finalReviewFixture.unnumbered_rerun);
  delete rerun.id;
  rerun.attempts.forEach((attempt, i) => { attempt.number = i + 1; });
  const row = buildReport(releaseInput({ workflow_runs: [...base.workflow_runs, rerun] }), { now }).releases[0];
  assert.equal(row.ci.completeness.state, "partial");
  assert.equal(row.ci.success_s, null);
  assert.equal(row.ci.attempts, undefined);
  assert.equal(row.ci.reason, "truncated");
});

test("r3 id-less PR rows cannot establish PR durations", () => {
  const base = releaseInput();
  const pr = { ...base.pull_requests[0], number: null };
  const row = buildReport(releaseInput({ pull_requests: [pr] }), { now }).releases[0];
  assert.equal(row.pr_to_merge_s, null);
  assert.equal(row.pr_open_to_live_s, null);
  assert.equal(row.reasons.pr_to_merge, "truncated");
});

test("r3 an explicit forward interval can carry a later unfinished rollback", () => {
  const row = buildReport(releaseInput({ rollouts: [{
    ...finalReviewFixture.forward, rollback_started_at: "2026-09-30T02:00:00Z",
  }] }), { now }).releases[0];
  assert.equal(row.elapsed_s, 1800);
  assert.equal(row.rollback_s, null);
  assert.match(row.rollback_reason, /missing/);
});

test("r3 explicit rollback direction scopes its cut/live interval to rollback", () => {
  const row = buildReport(releaseInput({ rollouts: [{
    ...finalReviewFixture.forward, direction: "rollback", outcome: "live",
  }] }), { now }).releases[0];
  assert.equal(row.elapsed_s, null);
  assert.equal(row.live_at, null);
  assert.equal(row.rollback_s, 1800);
});

test("r2 attached field flags are rejected before the GitHub executor", () => {
  for (const flag of reviewFixture.write_flags) {
    assert.throws(() => assertReadOnly(["api", flag, "repos/inspr-at/paimos/pulls"]), /refusing/);
    assert.throws(() => defaultGh(["api", flag, "repos/inspr-at/paimos/pulls"]), /refusing/);
  }
});

test("r2 rollback cut/live timestamps cannot overwrite the coherent forward rollout", () => {
  const forward = releaseInput().rollouts[0];
  const rollback = reviewFixture.rollback_with_forward_times;
  for (const rollouts of [[forward, rollback], [rollback, forward]]) {
    const row = buildReport(releaseInput({ rollouts }), { now }).releases[0];
    assert.equal(row.elapsed_s, 1800);
    assert.equal(row.cut_at, forward.cut_at);
    assert.equal(row.live_at, forward.live_at);
    assert.equal(row.rollback_s, 900);
  }
});

test("r2 newest rollback attempt is selected even when unfinished", () => {
  const forward = releaseInput().rollouts[0];
  const older = { ...reviewFixture.rollback_with_forward_times, rollback_finished_at: "2026-09-30T03:10:00Z" };
  const latest = reviewFixture.unfinished_rollback;
  for (const rollouts of [[forward, older, latest], [latest, older, forward]]) {
    const row = buildReport(releaseInput({ rollouts }), { now }).releases[0];
    assert.equal(row.rollback_s, null);
    assert.match(row.rollback_reason, /incomplete|missing/i);
  }
});

test("r2 CI association requires zoned, valid and ordered PR bounds", () => {
  const base = releaseInput();
  for (const bounds of reviewFixture.invalid_pr_bounds) {
    const row = buildReport(releaseInput({
      pull_requests: [{ ...base.pull_requests[0], ...bounds }],
      workflow_runs: [...base.workflow_runs, ...reviewFixture.incompatible_stall],
    }), { now }).releases[0];
    assert.equal(row.ci.success_s, null);
    assert.equal(row.ci.failed_s, null);
    assert.equal(row.ci.attempts, undefined);
    assert.match(row.ci.reason, /PR lifetime/);
  }
});

test("r2 a supplied latest attempt does not imply complete rerun history", () => {
  const base = releaseInput();
  const row = buildReport(releaseInput({
    workflow_runs: [...base.workflow_runs, reviewFixture.partial_rerun],
  }), { now }).releases[0];
  assert.equal(row.ci.success_s, null);
  assert.equal(row.ci.failed_s, null);
  assert.equal(row.ci.attempts, undefined);
  assert.equal(row.ci.observed_attempts, 1);
  assert.match(row.ci.reason, /attempt history/);
});

test("r2 status gate medians account for every missing or invalid sample", () => {
  for (const invalid of reviewFixture.partial_statuses.slice(1)) {
    const row = buildReport(releaseInput({ statuses: [reviewFixture.partial_statuses[0], invalid] }), { now }).releases[0];
    assert.equal(row.gate_ok_to_live_s, null);
    assert.equal(row.gate_samples, 1);
    assert.equal(row.gate_missing, 1);
    assert.equal(row.gate_reason, "incomplete");
  }
});

test("r2 a stall outside the rollout cannot yield a negative adjusted duration", () => {
  const base = releaseInput();
  const row = buildReport(releaseInput({ workflow_runs: [...base.workflow_runs, ...reviewFixture.incompatible_stall] }), { now }).releases[0];
  assert.equal(row.ci.failure_and_fix_s, 2940);
  assert.equal(row.ci.cut_to_live_without_failure_s, null);
  assert.equal(row.ci.cut_to_live_without_failure_min, null);
  assert.match(row.ci.adjustment_reason, /rollout window/);
});

function pageFixtureGh(calls, overrides = {}) {
  return (args) => {
    calls.push(args);
    if (args[0] === "pr") return { number: 7, title: "AEON: pin rollback v260930000000.0.0", createdAt: "2026-09-30T03:00:00Z", mergedAt: "2026-09-30T03:10:00Z", statusCheckRollup: [] };
    const path = args.at(-1);
    const url = new URL(path, "https://api.github.com/");
    const page = Number(url.searchParams.get("page"));
    const width = Number(url.searchParams.get("per_page"));
    if (path.includes("release.yml")) {
      const ids = overrides.ids || reviewFixture.pagination_ids;
      const items = ids.slice((page - 1) * width, page * width).map((id) => ({
        id, workflowName: "Release", event: "push", conclusion: "success", run_attempt: 1,
        created_at: "2026-09-30T00:00:00Z", updated_at: "2026-09-30T00:10:00Z",
        head_branch: "v260930000000.0.0", head_sha: "abc",
      }));
      return { total_count: ids.length, workflow_runs: items };
    }
    if (path.includes("search/issues")) return overrides.search || { total_count: 0, items: [] };
    if (path.includes("/jobs")) return { total_count: 0, jobs: [] };
    if (path.includes("ci.yml")) return { total_count: 0, workflow_runs: [] };
    return [];
  };
}

test("r2 pagination keeps constant width, applies the cap locally and reports the tail", () => {
  const calls = [];
  const raw = fetchInputs({ gh: pageFixtureGh(calls), pageSize: 2, listCap: 3 });
  assert.deepEqual(raw.workflow_runs.map((run) => run.id), [9, 10, 11]);
  assert.ok(raw.collection.truncated.includes("workflow_runs"));
  const releaseCalls = calls.filter((args) => args.at(-1).includes("release.yml"));
  assert.equal(releaseCalls.length, 2);
  assert.ok(releaseCalls.every((args) => args.at(-1).includes("per_page=2")));
});

test("r2 pagination de-duplicates stable ids across pages", () => {
  const raw = fetchInputs({ gh: pageFixtureGh([], { ids: [9, 10, 10, 11] }), pageSize: 2, listCap: 10 });
  assert.deepEqual(raw.workflow_runs.map((run) => run.id), [9, 10, 11]);
});

test("r2 incomplete search invalidates pin and rollback completeness", () => {
  const raw = fetchInputs({
    gh: pageFixtureGh([], { search: reviewFixture.incomplete_search }),
    rollouts: releaseInput().rollouts,
  });
  assert.ok(raw.collection.truncated.includes("pin_pull_requests"));
  const row = buildReport(raw, { now }).releases[0];
  assert.equal(row.rollback_s, null);
  assert.equal(row.rollback_reason, "truncated");
  assert.equal(row.pin, null);
  assert.equal(row.pin_reason, "truncated");
});

test("the allowlist accepts only builder-issued immutable templates", () => {
  const commands = [
    ghRead("release-runs", { repo: "inspr-at/paimos", page: 1, pageSize: 100 }),
    ghRead("ci-runs", { repo: "inspr-at/paimos", branch: "rel/r99&event=push", page: 2, pageSize: 2 }),
    ghRead("pulls", { repo: "inspr-at/paimos", page: 1, pageSize: 100 }),
    ghRead("pin-search", { repo: "markus-barta/nixcfg", page: 1, pageSize: 100 }),
    ghRead("jobs", { repo: "inspr-at/paimos", id: 7, page: 1, pageSize: 100 }),
    ghRead("statuses", { repo: "inspr-at/paimos", sha: "abc", page: 1, pageSize: 100 }),
    ghRead("attempt", { repo: "inspr-at/paimos", id: 7, attempt: 2 }),
    ghRead("pin-view", { repo: "markus-barta/nixcfg", number: 7 }),
  ];
  for (const command of commands) {
    assert.doesNotThrow(() => assertReadOnly(command));
    assert.equal(Object.isFrozen(command), true);
    assert.throws(() => command.push("-fbody=hi"), TypeError);
    assert.throws(() => defaultGh([...command]), /refusing/);
  }
  const url = new URL(commands[1][1], "https://api.github.com/");
  assert.equal(url.searchParams.get("branch"), "rel/r99&event=push");
  assert.equal(url.searchParams.get("event"), "pull_request");
  for (const operation of ["dispatch", "api", "toString", "__proto__"]) {
    assert.throws(() => ghRead(operation, { repo: "inspr-at/paimos" }), /refusing/);
  }
  for (const params of [
    { repo: "--hostname", page: 1, pageSize: 100 },
    { repo: "inspr-at/../dispatches", page: 1, pageSize: 100 },
    { repo: "inspr-at/paimos", page: 0, pageSize: 100 },
    { repo: "inspr-at/paimos", page: 1, pageSize: 101 },
    { repo: "inspr-at/paimos", page: 1, pageSize: 100, method: "POST" },
    { repo: "inspr-at/paimos", page: 1, pageSize: 100, flags: ["-Fn=1"] },
  ]) assert.throws(() => ghRead("release-runs", params), /refusing/);
});

test("validation cannot be bypassed by normalized input and records expose completeness", () => {
  assert.deepEqual(buildReport(normalizeInput(fixture), { now }), buildReport(fixture, { now }));
  const raw = releaseInput();
  raw.pull_requests[0].createdAt = "2026-09-30T01:00:00";
  const validated = validateEvidence(raw);
  assert.equal(validated.pullRequests[0].completeness.state, "partial");
  assert.equal(validated.pullRequests[0].times.createdAt.ms, null);
  assert.ok(validated.pullRequests[0].completeness.reasons.includes("missing timezone"));
  assert.equal(validated.rollouts[0].interval.completeness.state, "complete");
  assert.equal(buildReport(normalizeInput(raw), { now }).releases[0].ci.success_s, null);
});

test("a standalone rollback never supplies forward cut/live evidence", () => {
  const row = buildReport(releaseInput({ rollouts: [reviewFixture.rollback_with_forward_times] }), { now }).releases[0];
  assert.equal(row.elapsed_s, null);
  assert.equal(row.cut_at, null);
  assert.equal(row.live_at, null);
  assert.equal(row.rollback_s, 900);
});

test("forward intervals stay coherent and cannot splice timestamps from two records", () => {
  const base = releaseInput().rollouts[0];
  const cut = { ...base, live_at: null };
  const live = { ...base, cut_at: null };
  for (const rollouts of [[cut, live], [live, cut]]) {
    const row = buildReport(releaseInput({ rollouts }), { now }).releases[0];
    assert.equal(row.elapsed_s, null);
    assert.match(row.reasons.cut_to_live, /missing/);
  }
});

test("a newer unfinished pin rollback takes precedence over an older measured rollback", () => {
  const row = buildReport(releaseInput({
    rollouts: [releaseInput().rollouts[0], reviewFixture.rollback_with_forward_times],
    pin_pull_requests: [{ number: 8, title: "AEON: pin rollback v260930000000.0.0", createdAt: "2026-09-30T04:00:00Z", mergedAt: null }],
  }), { now }).releases[0];
  assert.equal(row.rollback_s, null);
  assert.match(row.rollback_reason, /missing/);
});

test("rerun coverage needs unique numbered attempts and valid sample intervals", () => {
  const base = releaseInput();
  const attempts = [
    { run_attempt: 1, conclusion: "failure", startedAt: "2026-09-30T01:05:00Z", completedAt: "2026-09-30T01:10:00Z" },
    { run_attempt: 2, conclusion: "success", startedAt: "2026-09-30T01:15:00Z", completedAt: "2026-09-30T01:20:00Z" },
  ];
  for (const observed of [
    [attempts[0], { ...attempts[1], run_attempt: 1 }],
    [attempts[0], { ...attempts[1], completedAt: "2026-09-30T01:20:00" }],
  ]) {
    const row = buildReport(releaseInput({ workflow_runs: [...base.workflow_runs, { ...reviewFixture.partial_rerun, runAttempt: 2, attempts: observed }] }), { now }).releases[0];
    assert.equal(row.ci.success_s, null);
    assert.equal(row.ci.failed_s, null);
    assert.equal(row.ci.completeness.state, "partial");
    assert.ok(row.ci.reason);
  }
  const row = buildReport(releaseInput({ workflow_runs: [...base.workflow_runs, { ...reviewFixture.partial_rerun, runAttempt: 2, attempts }] }), { now }).releases[0];
  assert.equal(row.ci.success_s, 300);
  assert.equal(row.ci.failed_s, 300);
  assert.equal(row.ci.attempts, 2);
});

test("an outside stall stays unknown even if subtracting it would be positive", () => {
  const base = releaseInput();
  const row = buildReport(releaseInput({
    workflow_runs: [...base.workflow_runs, ...reviewFixture.incompatible_stall],
    rollouts: [{ ...base.rollouts[0], cut_at: "2026-09-30T02:00:00Z", live_at: "2026-09-30T03:00:00Z" }],
  }), { now }).releases[0];
  assert.equal(row.ci.failure_and_fix_s, 2940);
  assert.equal(row.ci.cut_to_live_without_failure_s, null);
  assert.match(row.ci.adjustment_reason, /rollout window/);
});

test("rollback is the latest matching deployment and does not replace the forward pin", () => {
  const forward = {
    number: 10,
    title: "AEON: pin v260930000000.0.0",
    createdAt: "2026-09-30T03:00:00Z",
    mergedAt: "2026-09-30T03:10:00Z",
    checks: [{ name: "Pharos fleet release compatibility", startedAt: "2026-09-30T03:00:00Z", completedAt: "2026-09-30T03:09:19Z" }],
  };
  const rollback = {
    number: 11,
    title: "rollback v260930000000.0.0",
    createdAt: "2026-09-30T04:00:00Z",
    mergedAt: "2026-09-30T04:15:00Z",
    checks: [],
  };
  const earlierRollback = {
    number: 9,
    title: "rollback v260930000000.0.0",
    createdAt: "2026-09-30T02:30:00Z",
    mergedAt: "2026-09-30T02:40:00Z",
    checks: [],
  };

  for (const pins of [[forward, rollback], [rollback, forward]]) {
    const row = buildReport(releaseInput({ pin_pull_requests: pins }), { now }).releases[0];
    assert.equal(row.rollback_s, 900);
    assert.equal(row.rollback_source, "pin");
    assert.equal(row.pin.number, 10);
    assert.equal(row.pin.open_to_merge_s, 600);
  }

  const latest = buildReport(releaseInput({
    pin_pull_requests: [earlierRollback, forward, rollback],
  }), { now }).releases[0];
  assert.equal(latest.rollback_s, 900);
  assert.equal(latest.pin.number, 10);

  const laterRollout = buildReport(releaseInput({
    rollouts: [
      {
        direction: "forward",
        release: "99",
        version: "260930000000.0.0",
        sequence: 99,
        cut_at: "2026-09-30T01:00:00Z",
        live_at: "2026-09-30T01:30:00Z",
      },
      {
        version: "260930000000.0.0",
        rollback_started_at: "2026-09-30T05:00:00Z",
        rollback_finished_at: "2026-09-30T05:15:00Z",
      },
    ],
    pin_pull_requests: [forward],
  }), { now }).releases[0];
  assert.equal(laterRollout.elapsed_s, 1800);
  assert.equal(laterRollout.rollback_s, 900);
  assert.equal(laterRollout.rollback_source, "rollout");
  assert.equal(laterRollout.pin.number, 10);

  const ambiguous = buildReport(releaseInput({
    pin_pull_requests: [
      forward,
      { ...forward, number: 12, createdAt: "2026-09-30T03:20:00Z", mergedAt: "2026-09-30T03:30:00Z" },
    ],
  }), { now }).releases[0];
  assert.equal(ambiguous.pin, null);
  assert.equal(ambiguous.pin_reason, "ambiguous");

  const disagree = buildReport(releaseInput({
    rollouts: [{
      direction: "forward",
      release: "99",
      version: "260930000000.0.0",
      cut_at: "2026-09-30T01:00:00Z",
      live_at: "2026-09-30T01:30:00Z",
      rollback_started_at: "2026-09-30T05:00:00Z",
      rollback_finished_at: "2026-09-30T05:10:00Z",
    }],
    pin_pull_requests: [forward, rollback],
  }), { now }).releases[0];
  assert.equal(disagree.rollback_s, null);
  assert.equal(disagree.rollback_reason, "ambiguous");
  assert.equal(disagree.pin.number, 10);
});

test("CI stays inside the pull request lifetime and counts rerun attempts", () => {
  const opened = "2026-09-30T01:00:00Z";
  const merged = "2026-09-30T02:00:00Z";
  const base = releaseInput();
  base.pull_requests[0].createdAt = opened;
  base.pull_requests[0].mergedAt = merged;

  const rerun = buildReport(releaseInput({
    pull_requests: base.pull_requests,
    workflow_runs: [
      ...base.workflow_runs,
      {
        id: "fixture-ci-1",
        workflowName: "CI",
        event: "pull_request",
        conclusion: "success",
        runAttempt: 2,
        createdAt: "2026-09-30T01:00:00Z",
        runStartedAt: "2026-09-30T01:11:19Z",
        updatedAt: "2026-09-30T01:22:25Z",
        headBranch: "rel/r99",
        headSha: "def",
      },
    ],
  }), { now }).releases[0];
  // A latest-attempt start is an observed sample, not a complete rerun history.
  assert.equal(rerun.ci.success_s, null);
  assert.equal(rerun.ci.observed_attempts, 1);
  assert.equal(rerun.ci.reason, "attempt history unavailable");

  const history = buildReport(releaseInput({
    pull_requests: base.pull_requests,
    workflow_runs: [
      ...base.workflow_runs,
      {
        id: "fixture-ci-2",
        workflowName: "CI",
        event: "pull_request",
        conclusion: "success",
        runAttempt: 2,
        createdAt: "2026-09-30T01:00:00Z",
        updatedAt: "2026-09-30T01:22:25Z",
        headBranch: "rel/r99",
        headSha: "def",
        attempts: [
          { number: 1, conclusion: "failure", startedAt: "2026-09-30T01:05:00Z", completedAt: "2026-09-30T01:10:00Z" },
          { number: 2, conclusion: "success", startedAt: "2026-09-30T01:11:19Z", completedAt: "2026-09-30T01:22:25Z" },
        ],
      },
    ],
  }), { now }).releases[0];
  assert.equal(history.ci.failed_s, 300);
  assert.equal(history.ci.success_s, 666);
  assert.equal(history.ci.attempts, 2);
  assert.ok(history.ci.failure_and_fix_s > 0);

  const unknownAttempt = buildReport(releaseInput({
    pull_requests: base.pull_requests,
    workflow_runs: [
      ...base.workflow_runs,
      {
        id: "fixture-ci-3",
        workflowName: "CI",
        event: "pull_request",
        conclusion: "success",
        runAttempt: 2,
        createdAt: "2026-09-30T01:00:00Z",
        updatedAt: "2026-09-30T01:22:25Z",
        headBranch: "rel/r99",
        headSha: "def",
      },
    ],
  }), { now }).releases[0];
  assert.equal(unknownAttempt.ci.success_s, null);
  assert.equal(unknownAttempt.ci.reason, "attempt history unavailable");

  const bounded = buildReport(releaseInput({
    pull_requests: base.pull_requests,
    workflow_runs: [
      ...base.workflow_runs,
      {
        id: "fixture-ci-4",
        workflowName: "CI", event: "pull_request", conclusion: "failure",
        createdAt: "2026-09-29T00:00:00Z", updatedAt: "2026-09-29T00:10:00Z",
        headBranch: "rel/r99", headSha: "old",
      },
      {
        id: "fixture-ci-5",
        workflowName: "CI", event: "pull_request", conclusion: "failure",
        createdAt: "2026-09-30T01:05:00Z", updatedAt: "2026-09-30T01:15:00Z",
        headBranch: "rel/r99", headSha: "mid",
      },
      {
        id: "fixture-ci-6",
        workflowName: "CI", event: "pull_request", conclusion: "success",
        createdAt: "2026-09-30T01:40:00Z", updatedAt: "2026-09-30T02:10:00Z",
        headBranch: "rel/r99", headSha: "late",
      },
    ],
  }), { now }).releases[0];
  assert.equal(bounded.ci.failed_s, 600);
  assert.equal(bounded.ci.success_s, null);
  assert.equal(bounded.ci.failure_and_fix_s, null);
  assert.equal(bounded.ci.cut_to_live_without_failure_s, null);
  assert.ok(bounded.ci.cut_to_live_without_failure_s == null || bounded.ci.cut_to_live_without_failure_s >= 0);
});

test("a partial gate sample is missing, not a complete median", () => {
  const result = buildReport(releaseInput({
    rollouts: [{
      direction: "forward",
      release: "99",
      version: "260930000000.0.0",
      sequence: 99,
      cut_at: "2026-09-30T01:00:00Z",
      live_at: "2026-09-30T02:00:00Z",
      tickets: [
        { key: "AEON-1", gate_ok_at: "2026-09-30T00:00:00Z" },
        { key: "AEON-2" },
      ],
    }],
  }), { now });
  const row = result.releases[0];
  assert.equal(row.gate_ok_to_live_s, null);
  assert.notEqual(row.gate_ok_to_live_s, 7200);
  assert.equal(row.gate_samples, 1);
  assert.equal(row.gate_missing, 1);
  assert.equal(row.gate_reason, "incomplete");
  const gap = result.gaps.find((item) => item.id === "gate-ok");
  assert.ok(gap);
  assert.match(gap.text, /incomplete|missing/i);
});

test("timestamps need a zone and reversed events stay unknown", () => {
  const zoned = buildReport(releaseInput({
    rollouts: [{
      direction: "forward",
      release: "99",
      version: "260930000000.0.0",
      cut_at: "2026-09-29T23:20:59+02:00",
      live_at: "2026-09-30T00:18:56+02:00",
    }],
  }), { now }).releases[0];
  assert.equal(zoned.elapsed_s, 3477);

  const zoneless = buildReport(releaseInput({
    rollouts: [{
      direction: "forward",
      release: "99",
      version: "260930000000.0.0",
      cut_at: "2026-09-29T23:20:59",
      live_at: "2026-09-30T00:18:56Z",
    }],
  }), { now }).releases[0];
  assert.equal(zoneless.elapsed_s, null);
  assert.equal(zoneless.wall_min, null);
  assert.equal(zoneless.reasons.cut_to_live, "missing timezone");

  const reversed = buildReport(releaseInput({
    rollouts: [{
      direction: "forward",
      release: "99",
      version: "260930000000.0.0",
      cut_at: "2026-09-30T02:00:00Z",
      live_at: "2026-09-30T01:00:00Z",
    }],
  }), { now }).releases[0];
  assert.equal(reversed.elapsed_s, null);
  assert.equal(reversed.wall_min, null);
  assert.equal(reversed.reasons.cut_to_live, "reversed");
  assert.throws(() => buildReport(releaseInput(), { now, since: "2026-09-29T23:00:00" }), /timezone/);
});

test("lists are paginated and truncation is not reported as absence", () => {
  const digestJob = {
    id: 401,
    name: "image",
    steps: [{ name: "Record pushed digest", started_at: "2026-09-30T00:04:00Z", completed_at: "2026-09-30T00:04:30Z" }],
  };
  const releaseRun = {
    id: 1,
    event: "push",
    conclusion: "success",
    created_at: "2026-09-30T00:00:00Z",
    updated_at: "2026-09-30T00:10:00Z",
    head_branch: "v260930000000.0.0",
    head_sha: "abc",
    run_attempt: 1,
  };
  const calls = [];
  const gh = (args) => {
    calls.push(args);
    const text = args.join(" ");
    const page = Number((/page=(\d+)/.exec(text) || [])[1] || 1);
    if (page > 2 && !text.includes("view")) {
      if (text.includes("release.yml") || text.includes("ci.yml")) return { total_count: 2, workflow_runs: [] };
      if (text.includes("search/issues")) return { total_count: 2, items: [] };
      if (text.includes("/jobs")) return { total_count: 31, jobs: [] };
      return [];
    }
    if (text.includes("release.yml")) {
      if (page === 1) return { total_count: 2, workflow_runs: [{ id: 9, event: "push", conclusion: "success", created_at: "2026-09-30T00:00:00Z", updated_at: "2026-09-30T00:01:00Z", head_branch: "v260101000000.0.0", head_sha: "other", run_attempt: 1 }] };
      return { total_count: 2, workflow_runs: [releaseRun] };
    }
    if (text.includes("/pulls?")) {
      if (page === 1) return [{ number: 1, title: "unrelated", created_at: "2026-09-30T00:00:00Z", merged_at: "2026-09-30T00:05:00Z", merge_commit_sha: "nope", head: { ref: "rel/rother" } }];
      return [{ number: 7, title: "Release v260930000000.0.0", created_at: "2026-09-29T23:00:00Z", merged_at: "2026-09-30T00:00:00Z", merge_commit_sha: "abc", head: { ref: "rel/r99" } }];
    }
    if (text.includes("search/issues")) {
      if (page === 1) return { total_count: 2, items: [{ number: 3, title: "AEON: pin v260101000000.0.0", created_at: "2026-09-30T00:00:00Z" }] };
      return { total_count: 2, items: [{ number: 4, title: "rollback v260930000000.0.0", created_at: "2026-09-30T03:00:00Z" }] };
    }
    if (text.includes("/jobs")) {
      if (page === 1) return { total_count: 31, jobs: Array.from({ length: 30 }, (_, i) => ({ id: i + 1, name: `job-${i}`, steps: [] })) };
      return { total_count: 31, jobs: [digestJob] };
    }
    if (page > 2) {
      if (text.includes("release.yml") || text.includes("ci.yml")) return { total_count: 2, workflow_runs: [] };
      if (text.includes("search/issues")) return { total_count: 2, items: [] };
      if (text.includes("/jobs")) return { total_count: 31, jobs: [] };
      return [];
    }
    if (text.includes("ci.yml")) {
      if (page === 1) {
        return { total_count: 2, workflow_runs: [{ id: 6, event: "pull_request", conclusion: "failure", created_at: "2026-09-29T22:00:00Z", updated_at: "2026-09-29T22:10:00Z", head_branch: "rel/r99", head_sha: "old", run_attempt: 1 }] };
      }
      return { total_count: 2, workflow_runs: [{ id: 8, event: "pull_request", conclusion: "success", created_at: "2026-09-29T23:10:00Z", updated_at: "2026-09-29T23:20:00Z", head_branch: "rel/r99", head_sha: "def", run_attempt: 1 }] };
    }
    if (text.includes("/statuses")) {
      if (page === 1) return [{ id: 1, context: "ci", state: "success", updated_at: "2026-09-29T22:00:00Z", created_at: "2026-09-29T22:00:00Z" }];
      return [{ id: 2, context: "gate/cross-family", state: "success", updated_at: "2026-09-29T22:00:00Z", created_at: "2026-09-29T22:00:00Z" }];
    }
    if (text.includes("pr") && text.includes("view")) {
      return { number: 4, title: "rollback v260930000000.0.0", createdAt: "2026-09-30T03:00:00Z", mergedAt: "2026-09-30T03:15:00Z", statusCheckRollup: [] };
    }
    throw new Error(`unexpected gh ${text}`);
  };

  const raw = fetchInputs({
    release: "99",
    gh,
    pageSize: 1,
    listCap: 50,
    rollouts: [{
      direction: "forward",
      release: "99",
      version: "260930000000.0.0",
      sequence: 99,
      cut_at: "2026-09-29T23:00:00Z",
      live_at: "2026-09-30T01:00:00Z",
    }],
  });
  for (const args of calls) assertReadOnly(args);
  assert.ok(calls.some((args) => args.join(" ").includes("page=2")));
  const row = buildReport(raw, { now, release: "99" }).releases[0];
  assert.equal(row.digest_after_tag_s, 270);
  assert.equal(row.pr_to_merge_s, 3600);
  assert.equal(row.rollback_s, 900);
  assert.equal(row.rollback_source, "pin");
  assert.equal(row.ci.failed_s, null);
  assert.equal(row.ci.success_s, 600);
  assert.equal(row.gate_ok_to_live_s, 3 * 3600);

  const truncatedCalls = [];
  const truncatedRaw = fetchInputs({
    release: "99",
    pageSize: 1,
    listCap: 1,
    rollouts: releaseInput().rollouts,
    gh: (args) => {
      truncatedCalls.push(args);
      const text = args.join(" ");
      const page = Number((/page=(\d+)/.exec(text) || [])[1] || 1);
      if (page > 1) throw new Error(`page ${page} was read after the cap`);
      if (text.includes("release.yml")) return { total_count: 1, workflow_runs: [{ id: 1, event: "push", conclusion: "success", created_at: "2026-09-30T00:00:00Z", updated_at: "2026-09-30T00:10:00Z", head_branch: "v260930000000.0.0", head_sha: "abc", run_attempt: 1 }] };
      if (text.includes("/pulls?")) return [{ number: 7, title: "Release v260930000000.0.0", created_at: "2026-09-30T01:00:00Z", merged_at: "2026-09-30T02:00:00Z", merge_commit_sha: "abc", head: { ref: "rel/r99" } }];
      if (text.includes("search/issues")) return { total_count: 2, items: [{ number: 3, title: "AEON: pin v260101000000.0.0", created_at: "2026-09-30T00:00:00Z" }] };
      if (text.includes("ci.yml")) return { total_count: 2, workflow_runs: [{ id: 8, event: "pull_request", conclusion: "failure", created_at: "2026-09-29T23:00:00Z", updated_at: "2026-09-29T23:10:00Z", head_branch: "rel/r99", head_sha: "def", run_attempt: 1 }] };
      if (text.includes("/statuses")) return [{ id: 1, context: "ci", state: "success", updated_at: "2026-09-29T22:00:00Z", created_at: "2026-09-29T22:00:00Z" }];
      if (text.includes("/jobs")) return { total_count: 2, jobs: [{ name: "other", steps: [] }] };
      return [];
    },
  });
  const truncated = buildReport(truncatedRaw, { now, release: "99" });
  assert.ok(truncated.collection.truncated.length > 0);
  const gate = truncated.gaps.find((item) => item.id === "gate-ok");
  const rollback = truncated.gaps.find((item) => item.id === "rollback");
  assert.match(gate.text, /truncat|incomplete collection/i);
  assert.doesNotMatch(gate.text, /no successful gate/);
  assert.match(rollback.text, /truncat|incomplete collection/i);
  assert.doesNotMatch(rollback.text, /no pin PR marked as a rollback/);
  assert.equal(truncated.releases[0].ci.reason, "truncated");
  assert.equal(truncated.releases[0].ci.failed_s, null);
  assert.equal(truncated.releases[0].digest_reason, "truncated");
  assert.equal(truncated.releases[0].rollback_reason, "truncated");
});

test("the read-only guard rejects every write form before gh runs", () => {
  assert.throws(() => assertReadOnly(["api", "-X", "POST", "repos/inspr-at/paimos/dispatches"]), /non-GET/);
  assert.throws(() => assertReadOnly(["api", "-XPOST", "repos/inspr-at/paimos/dispatches"]), /non-GET/);
  assert.throws(() => assertReadOnly(["api", "--method=POST", "repos/inspr-at/paimos/dispatches"]), /non-GET/);
  assert.throws(() => assertReadOnly(["api", "--method=DELETE", "repos/x"]), /non-GET/);
  assert.throws(() => assertReadOnly(["api", "--field", "body=hi", "repos/x"]), /write/);
  assert.throws(() => assertReadOnly(["api", "--field=body=hi", "repos/x"]), /write/);
  assert.throws(() => assertReadOnly(["api", "-f", "body=hi", "repos/x"]), /write/);
  assert.throws(() => assertReadOnly(["api", "-F", "n=1", "repos/x"]), /write/);
  assert.throws(() => assertReadOnly(["api", "--input", "body.json", "repos/x"]), /write/);
  assert.throws(() => assertReadOnly(["api", "--input=body.json", "repos/x"]), /write/);
  assert.throws(() => assertReadOnly(["api", "--method=GET", "repos/x"]), /refusing/);
  assert.throws(() => assertReadOnly(["api", "-X", "GET", "repos/x"]), /refusing/);

  const bin = mkdtempSync(join(tmpdir(), "aeon-416-gh-"));
  const marker = join(bin, "called");
  writeFileSync(join(bin, "gh"), `#!/bin/sh\necho called > ${JSON.stringify(marker)}\necho github_pat_TESTONLY >&2\nexit 1\n`);
  chmodSync(join(bin, "gh"), 0o755);
  const previous = process.env.PATH;
  process.env.PATH = `${bin}:${previous}`;
  try {
    assert.throws(() => defaultGh(["api", "-X", "POST", "repos/x"]), /non-GET/);
    assert.throws(() => defaultGh(["api", "--method=POST", "repos/x"]), /non-GET/);
    assert.throws(() => defaultGh(["api", "--field", "a=b", "repos/x"]), /write/);
    assert.throws(() => defaultGh(["api", "-f", "a=b", "repos/x"]), /write/);
    assert.throws(() => defaultGh(["api", "--input", "body.json", "repos/x"]), /write/);
    assert.equal(existsSync(marker), false);
  } finally {
    process.env.PATH = previous;
  }

  writeFileSync(join(bin, "gh"), "#!/bin/sh\necho github_pat_TESTONLY ghp_TESTONLY >&2\nexit 1\n");
  process.env.PATH = `${bin}:${previous}`;
  try {
    assert.throws(() => defaultGh(ghRead("release-runs", { repo: "inspr-at/paimos", page: 1, pageSize: 100 })), (error) => {
      assert.equal(error.message.includes("github_pat_"), false);
      assert.equal(error.message.includes("ghp_"), false);
      assert.match(error.message, /redacted/);
      return true;
    });
  } finally {
    process.env.PATH = previous;
  }
});
