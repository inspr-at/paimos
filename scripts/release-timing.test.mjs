// SPDX-License-Identifier: AGPL-3.0-only
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { chmodSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import {
  assertReadOnly,
  buildReport,
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
    if (args[0] === "pr" && args[1] === "list" && text.includes("inspr-at/paimos")) {
      return [{
        number: 7, title: "Release", createdAt: "2026-09-29T23:00:00Z", mergedAt: "2026-09-30T00:00:00Z",
        headRefName: "rel/r99", mergeCommit: { oid: "abc" },
      }];
    }
    if (args[0] === "pr" && args[1] === "list") return [];
    if (text.includes("/jobs")) {
      return { jobs: [{ name: "image", steps: [{ name: "Record pushed digest", started_at: "2026-09-30T00:04:00Z", completed_at: "2026-09-30T00:04:30Z" }] }] };
    }
    if (args[0] === "run") return [];
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
  assert.equal(quiet.some((args) => args.includes("--method")), false);
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
