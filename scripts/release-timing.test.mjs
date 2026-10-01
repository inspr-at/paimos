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
  parseArgs,
} from "./release-timing.mjs";

const fixturePath = join(dirname(fileURLToPath(import.meta.url)), "testdata/release-timing/section1.json");
const fixture = JSON.parse(readFileSync(fixturePath, "utf8"));
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
    statuses: [{ sha: "abc", context: "gate/cross-family", state: "success", updated_at: "2026-09-29T20:00:00Z" }],
    rollouts: [{
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
      workflowName: "Release", event: "push", conclusion: "success",
      createdAt: "2026-09-30T00:00:00Z", updatedAt: "2026-09-30T00:10:00Z",
      headBranch: "v260930000000.0.0", headSha: "abc",
    }],
    pull_requests: [],
    pin_pull_requests: [{ number: 1, title: "rollback v260930000000.0.0", createdAt: "2026-09-30T03:00:00Z", mergedAt: "2026-09-30T03:12:00Z", checks: [] }],
    statuses: [
      { sha: "abc", context: "gate/cross-family", state: "success", updated_at: "2026-09-29T22:00:00Z" },
      { sha: "other", context: "gate/cross-family", state: "success", updated_at: "2026-09-29T10:00:00Z" },
      { sha: "abc", context: "ci", state: "success", updated_at: "2026-09-29T23:00:00Z" },
    ],
    rollouts: [{ release: "99", version: "260930000000.0.0", cut_at: "2026-09-29T23:00:00Z", live_at: "2026-09-30T01:00:00Z" }],
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
  assert.doesNotThrow(() => assertReadOnly(["api", "--method", "GET", "repos/inspr-at/paimos/commits/abc/status"]));
  assert.doesNotThrow(() => assertReadOnly(["pr", "list", "--repo", "inspr-at/paimos"]));
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
      return { jobs: [{ name: "image", steps: [{ name: "Record pushed digest", started_at: "2026-09-30T00:04:00Z", completed_at: "2026-09-30T00:04:30Z" }] }] };
    }
    if (text.includes("ci.yml") || args[0] === "run") return { total_count: 0, workflow_runs: [] };
    if (text.includes("/status")) return { statuses: [] };
    throw new Error(`unexpected gh ${text}`);
  };
  const raw = fetchInputs({ release: "99", gh });
  for (const args of calls) assertReadOnly(args);
  assert.ok(calls.some((args) => args.includes("--method") && args[args.indexOf("--method") + 1] === "GET"));
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
      release: "99",
      version: "260930000000.0.0",
      sequence: 99,
      cut_at: "2026-09-30T01:00:00Z",
      live_at: "2026-09-30T01:30:00Z",
    }],
    ...extra,
  };
}

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
  assert.equal(rerun.ci.success_s, 666);
  assert.notEqual(rerun.ci.success_s, 1345);

  const history = buildReport(releaseInput({
    pull_requests: base.pull_requests,
    workflow_runs: [
      ...base.workflow_runs,
      {
        workflowName: "CI",
        event: "pull_request",
        conclusion: "success",
        runAttempt: 2,
        createdAt: "2026-09-30T01:00:00Z",
        updatedAt: "2026-09-30T01:22:25Z",
        headBranch: "rel/r99",
        headSha: "def",
        attempts: [
          { conclusion: "failure", startedAt: "2026-09-30T01:05:00Z", completedAt: "2026-09-30T01:10:00Z" },
          { conclusion: "success", startedAt: "2026-09-30T01:11:19Z", completedAt: "2026-09-30T01:22:25Z" },
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
        workflowName: "CI", event: "pull_request", conclusion: "failure",
        createdAt: "2026-09-29T00:00:00Z", updatedAt: "2026-09-29T00:10:00Z",
        headBranch: "rel/r99", headSha: "old",
      },
      {
        workflowName: "CI", event: "pull_request", conclusion: "failure",
        createdAt: "2026-09-30T01:05:00Z", updatedAt: "2026-09-30T01:15:00Z",
        headBranch: "rel/r99", headSha: "mid",
      },
      {
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
      release: "99",
      version: "260930000000.0.0",
      cut_at: "2026-09-29T23:20:59+02:00",
      live_at: "2026-09-30T00:18:56+02:00",
    }],
  }), { now }).releases[0];
  assert.equal(zoned.elapsed_s, 3477);

  const zoneless = buildReport(releaseInput({
    rollouts: [{
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
      if (page === 1) return { total_count: 31, jobs: Array.from({ length: 30 }, (_, i) => ({ name: `job-${i}`, steps: [] })) };
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
      if (page === 1) return [{ context: "ci", state: "success", updated_at: "2026-09-29T22:00:00Z", created_at: "2026-09-29T22:00:00Z" }];
      return [{ context: "gate/cross-family", state: "success", updated_at: "2026-09-29T22:00:00Z", created_at: "2026-09-29T22:00:00Z" }];
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
      if (text.includes("/statuses")) return [{ context: "ci", state: "success", updated_at: "2026-09-29T22:00:00Z", created_at: "2026-09-29T22:00:00Z" }];
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
  assert.doesNotThrow(() => assertReadOnly(["api", "--method=GET", "repos/x"]));
  assert.doesNotThrow(() => assertReadOnly(["api", "-X", "GET", "repos/x"]));

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
    assert.throws(() => defaultGh(["api", "--method", "GET", "repos/x"]), (error) => {
      assert.equal(error.message.includes("github_pat_"), false);
      assert.equal(error.message.includes("ghp_"), false);
      assert.match(error.message, /redacted/);
      return true;
    });
  } finally {
    process.env.PATH = previous;
  }
});
