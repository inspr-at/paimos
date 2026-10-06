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
const enabledEvents = "push,workflow_dispatch,pull_request,merge_group";
const eventClasses = { push: "mbp2606-push", workflow_dispatch: "mbp2606-dispatch", pull_request: "mbp2606-pr", merge_group: "mbp2606-mq" };
const switchCases = [
  [undefined, ["push", "workflow_dispatch"]],
  ["", ["push", "workflow_dispatch"]],
  [enabledEvents, Object.keys(eventClasses)],
  ["push,workflow_dispatch", ["push", "workflow_dispatch"]],
  ["pull_request", ["pull_request"]],
  ["merge_group", ["merge_group"]],
  [" pull_request, merge_group, pull_request ", ["pull_request", "merge_group"]],
  ["pull_request_target,schedule,future_event", []],
  ["pull_request_extra,not_merge_group", []],
  ["PULL_REQUEST", []], [" ", []], [",", []], [null, []], [1, []],
];
const available = {
  schema: 1, repository, os: "linux", arch: "arm64", online: true, busy: false,
  observed_at: new Date(now - 1000).toISOString(), idle_runners: 4,
};
function route(event = "push", changes = {}, options = {}) {
  return routeRunner({ event, repository, ref: "refs/heads/main", availability: JSON.stringify({ ...available, ...changes }), now, ...options });
}

test("trusted events use base labels plus exactly their verified event class", () => {
  assert.deepEqual(trustedEvents, Object.keys(eventClasses));
  for (const [event, eventClass] of Object.entries(eventClasses)) {
    const selected = route(event, {}, { poolEvents: enabledEvents, headRepository: repository });
    const labels = selected.runs_on;
    assert.deepEqual(labels, ["self-hosted", "Linux", "ARM64", "mbp2606", eventClass]);
    assert.equal(labels.filter((label) => Object.values(eventClasses).includes(label)).length, 1);
    assert.ok(labels.every((label) => !/^(ubuntu|macos|windows)-/i.test(label)));
    assert.equal(selected.runner_class, "mbp2606");
  }
});

test("unmapped events cannot acquire the dispatch class, even if the allowlist grows", () => {
  const events = ["future_event", "workflow_dispatch_extra", "constructor", "toString", "__proto__"];
  trustedEvents.push(...events);
  try {
    for (const event of [...events, null, undefined, {}, 1]) {
      assert.deepEqual(route(event, {}, { event, poolEvents: events.join(",") }), {
        runs_on: ["ubuntu-latest"], runner_class: "hosted", reason: "untrusted-event",
      });
    }
  } finally {
    trustedEvents.splice(-events.length);
  }
});

test("PRs, privileged PR events, other events and other repositories stay hosted", () => {
  for (const event of ["pull_request", "pull_request_target", "merge_group", "workflow_call", "workflow_run", "schedule", "release", "", undefined]) {
    assert.equal(route(event, {}, { event }).runner_class, "hosted");
  }
  assert.equal(route("push", {}, { repository: "fork/paimos" }).runner_class, "hosted");
});

test("pushes and dispatches outside main stay hosted even with a fresh lease", () => {
  for (const event of ["push", "workflow_dispatch"]) {
    for (const ref of [undefined, "", "refs/heads/work/aeon-438", "refs/tags/v1", "refs/pull/34/merge", "refs/heads/gh-readonly-queue/main/pr-34"]) {
      assert.equal(route(event, {}, { ref }).reason, "untrusted-ref");
    }
  }
});

test("event x switch x head repository x ref retains defaults and opts in only listed event classes", () => {
  for (const [poolEvents, allowed] of switchCases)
    for (const event of [...Object.keys(eventClasses), "pull_request_target", "schedule", "workflow_call", "workflow_run", "release", "future_event"])
      for (const headRepository of [repository, "fork/paimos", "", undefined])
        for (const ref of ["refs/heads/main", "refs/heads/work/aeon-777", "refs/tags/v1", "refs/pull/34/merge", "refs/heads/gh-readonly-queue/main/pr-34", "", undefined]) {
          const admitted = allowed.includes(event) &&
            (!["push", "workflow_dispatch"].includes(event) || ref === "refs/heads/main") &&
            (event !== "pull_request" || headRepository === repository);
          const selected = route(event, {}, { poolEvents, headRepository, ref });
          const label = JSON.stringify({ event, poolEvents, headRepository, ref });
          assert.equal(selected.runner_class, admitted ? "mbp2606" : "hosted", label);
          assert.deepEqual(selected.runs_on, admitted ? ["self-hosted", "Linux", "ARM64", "mbp2606", eventClasses[event]] : ["ubuntu-latest"], label);
        }
  for (const event of Object.keys(eventClasses)) {
    assert.equal(route(event, { repository: "fork/paimos" }, { poolEvents: enabledEvents, repository: "fork/paimos", headRepository: "fork/paimos" }).reason, "different-repository");
  }
});

test("every enabled event retains lease and whole-batch capacity checks", () => {
  for (const event of Object.keys(eventClasses)) {
    const options = { poolEvents: enabledEvents, headRepository: repository, requiredIdle: 4 };
    assert.equal(route(event, {}, options).runner_class, "mbp2606");
    for (const [changes, reason] of [
      [{ online: false }, "offline-or-busy"], [{ busy: true }, "offline-or-busy"],
      [{ idle_runners: 3 }, "insufficient-idle-capacity"],
      [{ observed_at: new Date(now - 30000).toISOString() }, "expired-availability"],
      [{ observed_at: new Date(now + 1).toISOString() }, "expired-availability"],
    ]) assert.equal(route(event, changes, options).reason, reason, event);
    assert.equal(route(event, {}, { ...options, availability: "" }).reason, "runner-not-enabled");
    assert.equal(route(event, {}, { ...options, availability: "{" }).reason, "invalid-availability");
    assert.equal(route(event, {}, { ...options, requiredIdle: 0 }).reason, "invalid-capacity-request");
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

test("CLI reads the pool event switch and PR head repository from workflow environment", () => {
  for (const [event, ref] of [["pull_request", "refs/pull/34/merge"], ["merge_group", "refs/heads/gh-readonly-queue/main/pr-34"]])
    for (const poolEvents of ["", enabledEvents]) for (const headRepository of [repository, "fork/paimos", ""]) {
      const result = spawnSync(process.execPath, [new URL("./ci-runner-route.mjs", import.meta.url).pathname], {
        encoding: "utf8",
        env: {
          GITHUB_EVENT_NAME: event, GITHUB_REPOSITORY: repository, GITHUB_REF: ref, GITHUB_RUN_ATTEMPT: "1",
          AEON_POOL_EVENTS: poolEvents, AEON_PR_HEAD_REPOSITORY: headRepository,
          AEON_MBP2606_AVAILABILITY: JSON.stringify({ ...available, observed_at: new Date().toISOString() }),
          AEON_REQUIRED_IDLE_RUNNERS: "4",
        },
      });
      assert.equal(result.status, 0, result.stderr);
      const admitted = poolEvents !== "" && (event !== "pull_request" || headRepository === repository);
      assert.match(result.stdout, new RegExp(`runner_class=${admitted ? "mbp2606" : "hosted"}\\n`));
      if (admitted) assert.ok(result.stdout.includes(`"${eventClasses[event]}"`));
    }
});

test("router outputs bind each attempt and refuse a missing or invalid attempt", () => {
  for (const attempt of [1, 2, 3]) {
    assert.match(writeRoute(route(), undefined, undefined, attempt), new RegExp(`run_attempt=${attempt}\\n$`));
  }
  for (const attempt of [undefined, 0, -1, NaN, 1.5, "2"]) {
    assert.throws(() => writeRoute(route(), undefined, undefined, attempt), /invalid run attempt/);
  }
});

test("CI expressions honor the router switch, fork boundary and attempt for runner and shard selection", () => {
  const workflow = readFileSync(new URL("../.github/workflows/ci.yml", import.meta.url), "utf8");
  const shardJob = workflow.split("  go-test:\n")[1].split("\n  go-timing:")[0];
  const runnerExpression = shardJob.match(/^    runs-on: (.+)$/m)[1];
  const shardExpression = shardJob.match(/^        shard: (.+)$/m)[1];
  // Evaluate the checked-in expressions, not a separate implementation of the
  // workflow decision. These expressions use the common JS/Actions operators.
  function evaluate(expression, github, outputs, tierMode="essential") {
    const source = expression.slice(3, -2).replaceAll("needs.runner-route.outputs.", "outputs.").replaceAll("needs.tier-plan.outputs.mode", "tierMode");
    return Function("github", "outputs", "contains", "fromJSON", "tierMode", `return (${source});`)(
      github, outputs, (values, value) => values.includes(value), JSON.parse, tierMode,
    );
  }
  const cases = [
    ["push", "refs/heads/main", true], ["workflow_dispatch", "refs/heads/main", true],
    ["push", "refs/heads/work/aeon-777", false], ["workflow_dispatch", "refs/tags/v1", false],
    ["pull_request", "refs/pull/34/merge", true], ["merge_group", "refs/heads/gh-readonly-queue/main/pr-34", true],
    ["pull_request_target", "refs/heads/main", false], ["schedule", "refs/heads/main", false],
  ];
  for (const [poolEvents, allowed] of switchCases) for (const [event, ref, validRef] of cases)
    for (const headRepository of [repository, "fork/paimos", ""])
      for (const run_attempt of [1, 2]) for (const forged of [false, true]) {
        const selected = forged ? route("push") : route(event, {}, { poolEvents, ref, headRepository });
        const outputs = { ...selected, runs_on: JSON.stringify(selected.runs_on), run_attempt: "1" };
        const github = { event_name: event, ref, run_attempt, repository, event: { pull_request: { head: { repo: { full_name: headRepository } } } } };
        const admitted = validRef && (forged || allowed.includes(event)) && run_attempt === 1 &&
          (event !== "pull_request" || headRepository === repository);
        const label = JSON.stringify({ poolEvents, event, ref, headRepository, run_attempt, forged });
        assert.deepEqual(evaluate(runnerExpression, github, outputs), admitted ? selected.runs_on : ["ubuntu-latest"], label);
        assert.deepEqual(evaluate(shardExpression, github, outputs), admitted ? [1, 2, 3, 4] :
          event === "pull_request" ? [1, 2] : [1, 2, 3, 4, 5, 6, 7], label);
        assert.deepEqual(evaluate(shardExpression, github, outputs, "full"), admitted ? [1, 2, 3, 4] : [1, 2, 3, 4, 5, 6, 7], label);
      }
  for (const [event, ref] of cases.filter(([, , valid]) => valid)) {
    const github = { event_name: event, ref, run_attempt: 1, repository, event: { pull_request: { head: { repo: { full_name: repository } } } } };
    for (const changes of [{ online: false }, { busy: true }, { idle_runners: 3 }, { observed_at: new Date(now - 30000).toISOString() }]) {
      const selected = route(event, changes, { requiredIdle: 4, poolEvents: enabledEvents, headRepository: repository, ref });
      const outputs = { ...selected, runs_on: JSON.stringify(selected.runs_on), run_attempt: "1" };
      assert.deepEqual(evaluate(runnerExpression, github, outputs), ["ubuntu-latest"]);
      assert.deepEqual(evaluate(shardExpression, github, outputs, "full"), [1, 2, 3, 4, 5, 6, 7]);
    }
  }
});

test("key dialog CI uses the hosted shards and covers the shared access markup", () => {
  const workflow = readFileSync(new URL("../.github/workflows/ci.yml", import.meta.url), "utf8");
  const units = workflow.split("  web-unit:\n")[1].split("\n  web-shard:")[0];
  assert.match(units, /sudo apt-get install -y -qq fish zsh/);
  const install = units.indexOf("Install shells used by command round-trip unit tests");
  const execute = units.indexOf("Run selected web units without retries");
  assert.ok(install >= 0 && execute > install, 'Unit runners must install shells before running tests');
  assert.match(units, /cli\.mjs run web --unit/);
  assert.doesNotMatch(workflow, /Key dialog layout regression|playwright .*tests\/key-layout\.spec\.ts/);
  const shardJob = workflow.split("  web-shard:\n")[1].split("\n  web:")[0];
  assert.match(shardJob, /runs-on: ubuntu-latest/);
  assert.match(shardJob, /cli\.mjs run web --shard/);
  const manifest = JSON.parse(readFileSync(new URL("../web/ci-web-shards.json", import.meta.url), "utf8"));
  const layoutGroups = manifest.groups.filter(group => group.specs.some(spec => spec.file === "tests/key-layout.spec.ts"));
  assert.equal(layoutGroups.length, 1);
  const layout = layoutGroups[0];
  assert.notEqual(layout.gate, false);
  assert.equal(layout.hostedOnly, true);
  assert.equal(layout.config, "playwright.ui.config.ts");
  assert.deepEqual(layout.flags, ["--workers=1"]);
  const access = manifest.groups.find(group => group.id === "access-dialogs");
  assert.equal(access.hostedOnly, true);
  for (const file of ["tests/access.spec.ts", "tests/key-dialog.spec.ts"]) {
    assert.ok(access.specs.some(spec => spec.file === file));
  }
});
