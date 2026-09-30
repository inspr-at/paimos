// SPDX-License-Identifier: AGPL-3.0-only
import assert from "node:assert/strict";
import { mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";
import { routeRunner, trustedEvents, writeRoute } from "./ci-runner-route.mjs";

const now = Date.parse("2026-09-30T10:00:00Z");
const repository = "inspr-at/paimos";
const available = {
  schema: 1, repository, os: "linux", arch: "arm64", online: true, busy: false,
  observed_at: new Date(now - 1000).toISOString(), idle_runners: 4,
};
function route(event = "push", changes = {}, options = {}) {
  return routeRunner({ event, repository, ref: "refs/heads/main", availability: JSON.stringify({ ...available, ...changes }), now, ...options });
}

test("trusted events can use an online idle Linux ARM64 pool", () => {
  assert.deepEqual(trustedEvents, ["push", "workflow_dispatch"]);
  for (const event of trustedEvents) {
    assert.deepEqual(route(event).runs_on, ["self-hosted", "Linux", "ARM64", "mbp2606"]);
    assert.equal(route(event).runner_class, "mbp2606");
  }
});

test("PRs, privileged PR events, other events and other repositories stay hosted", () => {
  for (const event of ["pull_request", "pull_request_target", "merge_group", "workflow_call", "workflow_run", "schedule", "release", "", undefined]) {
    assert.equal(route(event, {}, { event }).runner_class, "hosted");
  }
  assert.equal(route("push", {}, { repository: "fork/paimos" }).runner_class, "hosted");
});

test("pushes and dispatches outside main stay hosted even with a fresh lease", () => {
  for (const event of trustedEvents) {
    for (const ref of [undefined, "", "refs/heads/work/aeon-438", "refs/tags/v1", "refs/pull/34/merge", "refs/heads/gh-readonly-queue/main/pr-34"]) {
      assert.equal(route(event, {}, { ref }).reason, "untrusted-ref");
    }
  }
});

test("absent, offline, busy and malformed records fall back without waiting", () => {
  for (const availability of ["", "{", "null", "[]", '"mbp2606"']) {
    assert.equal(route("push", {}, { availability }).runner_class, "hosted");
  }
  for (const changes of [
    { online: false }, { online: "true" }, { busy: true }, { busy: undefined },
    { schema: 2 }, { repository: "other/repo" }, { os: "macos" }, { arch: "amd64" },
    { idle_runners: 0 }, { idle_runners: "4" }, { idle_runners: 1.5 },
  ]) assert.equal(route("push", changes).runner_class, "hosted");
});

test("the availability lease expires at 30 seconds and rejects future clocks", () => {
  for (const observed_at of [undefined, "invalid", now, new Date(now - 30000).toISOString(), new Date(now + 1).toISOString()]) {
    assert.equal(route("push", { observed_at }).runner_class, "hosted");
  }
  assert.equal(route("push", { observed_at: new Date(now - 29999).toISOString() }).runner_class, "mbp2606");
});

test("a shard fan-out requires enough idle runners for the whole batch", () => {
  assert.equal(route("push", {}, { requiredIdle: 4 }).runner_class, "mbp2606");
  assert.equal(route("push", { idle_runners: 3 }, { requiredIdle: 4 }).runner_class, "hosted");
  for (const requiredIdle of [0, -1, NaN, 1.5, "4"]) {
    assert.equal(route("push", {}, { requiredIdle }).runner_class, "hosted");
  }
});

test("CLI writes JSON runs-on, rerun attempt and value-free evidence", () => {
  const dir = mkdtempSync(join(tmpdir(), "aeon-runner-route-"));
  const output = join(dir, "output");
  const summary = join(dir, "summary");
  const result = spawnSync(process.execPath, [new URL("./ci-runner-route.mjs", import.meta.url).pathname], {
    encoding: "utf8",
    env: {
      GITHUB_EVENT_NAME: "push", GITHUB_REPOSITORY: repository,
      GITHUB_REF: "refs/heads/main", GITHUB_RUN_ATTEMPT: "2",
      AEON_MBP2606_AVAILABILITY: "invalid value must not be echoed",
      GITHUB_OUTPUT: output, GITHUB_STEP_SUMMARY: summary,
    },
  });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(readFileSync(output, "utf8"), 'runs_on=["ubuntu-latest"]\nrunner_class=hosted\nreason=invalid-availability\nrun_attempt=2\n');
  assert.match(readFileSync(summary, "utf8"), /hosted.*invalid-availability/);
  assert.doesNotMatch(result.stdout, /must not be echoed/);
});

test("router outputs bind each attempt and refuse a missing or invalid attempt", () => {
  for (const attempt of [1, 2, 3]) {
    assert.match(writeRoute(route(), undefined, undefined, attempt), new RegExp(`run_attempt=${attempt}\\n$`));
  }
  for (const attempt of [undefined, 0, -1, NaN, 1.5, "2"]) {
    assert.throws(() => writeRoute(route(), undefined, undefined, attempt), /invalid run attempt/);
  }
});
