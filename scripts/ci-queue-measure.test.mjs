// SPDX-License-Identifier: AGPL-3.0-only
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { spawnSync } from "node:child_process";
import test from "node:test";
import { analyze, collectRuns, formatReport, main, measureRun, percentile, rulesetPlan } from "./ci-queue-measure.mjs";

const fixtureURL = new URL("./testdata/ci-queue-measure.json", import.meta.url);
const fixture = () => JSON.parse(readFileSync(fixtureURL, "utf8"));
const rulesetURL = new URL("./testdata/ci-queue-ruleset.json", import.meta.url);
const ruleset = () => JSON.parse(readFileSync(rulesetURL, "utf8"));

test("initial runner wait, execution and retry gaps use attempt-specific jobs, never updated_at", () => {
  const report = analyze(fixture(), "2026-10-03T12:00:00Z");
  assert.deepEqual(report.runs.map((r) => r.id), [101, 102, 103, 104]);
  const retried = report.runs[1];
  assert.equal(retried.initial_queue_seconds, 60);
  assert.equal(retried.run_seconds, 300);
  assert.equal(retried.elapsed_seconds, 1500);
  assert.deepEqual(retried.attempts.map((a) => a.duration_seconds), [240, 300]);
  assert.equal(report.runs[2].initial_queue_seconds, 60); // Skipped job is excluded.
  assert.equal(report.runs[2].run_seconds, 360);
  assert.equal(report.runs[2].elapsed_seconds, 420);
  assert.equal(report.runs[3].run_seconds, null); // Active run is not a zero-length success.
  assert.equal(report.after.duration_samples, 1);
  assert.equal(report.after.queue_samples, 2);
  assert.equal(report.before.run_p50_seconds, 300);
  assert.equal(report.before.run_p95_seconds, 600);
});

test("failure occurrences and observed restarts group by exact SHA and retain failed earlier attempts", () => {
  const report = analyze(fixture());
  const group = report.groups.find((g) => g.head_sha.startsWith("a"));
  assert.deepEqual(group.run_ids, [102, 103]);
  assert.equal(group.failed_attempts, 1);
  assert.equal(group.reruns, 1);
  assert.equal(group.repeated_runs, 1);
  assert.equal(group.restarts, 2);
  assert.deepEqual(report.failed_jobs, [{ name: "web", failed_attempts: 2 }, { name: "go-test (2)", failed_attempts: 1 }]);
  assert.equal(report.groups.length, 3);
  assert.match(formatReport(report), /older\/newer half/);
  assert.match(formatReport(report), /not PR residence/);
});

test("empty windows, absent jobs, cancellation and invalid timestamp bounds are honest", () => {
  assert.equal(percentile([], .5), null);
  const empty = analyze({ schema: 1, runs: [] });
  assert.equal(empty.before.run_p50_seconds, null);
  assert.match(formatReport(empty), /n\/a/);
  const r = fixture().runs[3];
  r.attempts[0].jobs = [];
  r.attempts[0].conclusion = "cancelled";
  const measured = measureRun(r);
  assert.equal(measured.initial_queue_seconds, null);
  assert.equal(measured.run_seconds, null);
  assert.equal(measured.warnings.length, 1);
  const report = analyze({ schema: 1, runs: [r] });
  assert.equal(report.after.failed, 0);
  assert.equal(report.after.cancelled, 1);
  assert.equal(report.groups[0].cancelled_attempts, 1);
  r.attempts[0].jobs = [{ name: "bad clock", started_at: "2026-10-03T10:02:00Z", completed_at: "2026-10-03T10:01:00Z" }];
  assert.equal(measureRun(r).run_seconds, null);
  r.attempts[0].jobs.push({ name: "unfinished", started_at: "2026-10-03T10:03:00Z", completed_at: null });
  assert.equal(measureRun(r).elapsed_seconds, null);
});

test("incomplete histories, duplicate IDs and malformed splits fail instead of partial reporting", () => {
  const s = fixture();
  s.runs[2].attempts.pop();
  assert.throws(() => analyze(s), /incomplete attempt history/);
  const duplicate = fixture();
  duplicate.runs.push(duplicate.runs[0]);
  assert.throws(() => analyze(duplicate), /duplicate run IDs/);
  assert.throws(() => analyze(fixture(), "2026-10-03"), /ISO timestamp/);
});

test("last N workflow runs are sorted, event-filtered and read across each attempt and job page", async () => {
  const runs = fixture().runs;
  const calls = [];
  const get = async (endpoint) => {
    calls.push(endpoint);
    if (endpoint.includes("/workflows/")) return { total_count: 5, workflow_runs: [runs[3], { ...runs[0], id: 999, event: "push" }, runs[2], runs[1], runs[0]] };
    const match = endpoint.match(/runs\/(\d+)\/attempts\/(\d+)(\/jobs\?per_page=100&page=(\d+))?$/);
    assert.ok(match, endpoint);
    const attempt = runs.find((r) => r.id === Number(match[1])).attempts[Number(match[2]) - 1];
    if (!match[3]) return attempt;
    if (Number(match[1]) === 102 && Number(match[2]) === 1 && match[4] === "1") return { total_count: 101, jobs: Array.from({ length: 100 }, (_, i) => ({ id: i, name: "skipped", conclusion: "skipped", run_attempt: 1 })) };
    return { total_count: Number(match[1]) === 102 && Number(match[2]) === 1 ? 101 : attempt.jobs.length, jobs: attempt.jobs.slice(0, 1) };
  };
  const snapshot = await collectRuns({ count: 3, get });
  assert.deepEqual(snapshot.runs.map((r) => r.id), [104, 103, 102]);
  assert.equal(snapshot.runs[2].attempts.length, 2);
  assert.ok(calls.includes("repos/inspr-at/paimos/actions/runs/102/attempts/1/jobs?per_page=100&page=2"));
  assert.equal(snapshot.runs[2].attempts[0].jobs.length, 101);
  assert.ok(calls.every((c) => c.startsWith("repos/inspr-at/paimos/actions/")));
});

test("bounded pagination and identity checks reject truncated/mismatched API data", async () => {
  const run = fixture().runs[0];
  const tooMany = async (endpoint) => endpoint.includes("/workflows/") ? { workflow_runs: [run] }
    : endpoint.includes("/jobs?") ? { total_count: 1001, jobs: Array.from({ length: 100 }, () => ({ run_attempt: 1 })) }
    : run.attempts[0];
  await assert.rejects(collectRuns({ count: 1, get: tooMany }), /pagination limit/);
  await assert.rejects(collectRuns({ count: 1, get: async (endpoint) => endpoint.includes("/workflows/") ? { workflow_runs: [run] } : { id: 999, run_attempt: 1 } }), /identity mismatch/);
});

test("workflow pagination retrieves the last N unique runs before sorting the sample", async () => {
  const template = fixture().runs[0];
  const calls = [];
  const all = Array.from({ length: 101 }, (_, i) => ({ ...template, id: 1000 + i,
    created_at: new Date(Date.parse(template.created_at) + i * 1000).toISOString() })).reverse();
  const get = async (endpoint) => {
    calls.push(endpoint);
    if (endpoint.includes("/workflows/")) return { total_count: 101, workflow_runs: endpoint.endsWith("page=1") ? all.slice(0, 100) : all.slice(100) };
    if (endpoint.includes("/jobs?")) return { total_count: 0, jobs: [] };
    return { id: Number(endpoint.match(/runs\/(\d+)/)[1]), run_attempt: 1, status: "queued", conclusion: null };
  };
  const collected = await collectRuns({ count: 101, get });
  assert.equal(collected.runs.length, 101);
  assert.deepEqual(collected.runs.map((r) => r.id), all.map((r) => r.id));
  assert.equal(calls.filter((c) => c.includes("/workflows/")).length, 2);
});

test("ruleset patch resolves the actual rule position and preserves every unrelated setting", () => {
  const current = ruleset();
  const { candidate, rollback, diff } = rulesetPlan(current);
  assert.deepEqual(diff, [
    { op: "replace", path: "/rules/2/parameters/max_entries_to_build", before: 1, value: 2 },
    { op: "replace", path: "/rules/2/parameters/max_entries_to_merge", before: 1, value: 5 },
    { op: "replace", path: "/rules/2/parameters/min_entries_to_merge_wait_minutes", before: 5, value: 1 },
  ]);
  assert.deepEqual(candidate.rules.slice(0, 2), current.rules.slice(0, 2));
  assert.deepEqual(candidate.conditions, current.conditions);
  assert.deepEqual(candidate.bypass_actors, current.bypass_actors);
  assert.equal(candidate.rules[2].parameters.grouping_strategy, "ALLGREEN");
  assert.equal(candidate.rules[2].parameters.check_response_timeout_minutes, 60);
  assert.equal(candidate.rules[2].parameters.merge_method, "SQUASH");
  assert.equal(candidate.rules[2].parameters.min_entries_to_merge, 1);
  assert.equal(candidate.id, undefined);
  assert.equal(candidate.created_at, undefined);
  assert.deepEqual(rollback.rules, current.rules);
  assert.equal(current.rules[2].parameters.max_entries_to_build, 1);
  assert.deepEqual(rulesetPlan(candidate).diff, []);
});

test("ruleset must exist at repository scope; unsupported changes fail closed", () => {
  const current = ruleset();
  assert.throws(() => rulesetPlan({ ...current, source_type: "Organization" }), /inherited/);
  assert.throws(() => rulesetPlan({ ...current, rules: [] }), /exactly one/);
  assert.throws(() => rulesetPlan({ ...current, rules: [...current.rules, current.rules[2]] }), /exactly one/);
  assert.throws(() => rulesetPlan(current, { schema: 1, select: { rule_type: "merge_queue" }, patch: [{ op: "replace", path: "/grouping_strategy", value: "HEADGREEN" }] }), /unsupported/);
});

test("live dry-run makes exactly one read; no CLI apply or mutation path exists", async () => {
  const calls = [];
  const output = await main(["--ruleset", "42", "--dry-run"], async (endpoint) => { calls.push(endpoint); return ruleset(); });
  assert.deepEqual(calls, ["repos/inspr-at/paimos/rulesets/42"]);
  assert.equal(JSON.parse(output).length, 3);
  await assert.rejects(main(["--apply"]), /unknown option/);
  await assert.rejects(main(["--ruleset-payload", "--ruleset", "42"]), /saved snapshot/);
  await assert.rejects(main(["--ruleset", "42", "--count", "3"]), /cannot be combined/);
});

test("offline CLI table, JSON, candidate and rollback work without gh or dependencies", () => {
  const cli = new URL("./ci-queue-measure.mjs", import.meta.url).pathname;
  const invoke = (...args) => spawnSync(process.execPath, [cli, ...args], { encoding: "utf8", env: { PATH: "/nonexistent" } });
  const table = invoke("--input", fixtureURL.pathname, "--split-at", "2026-10-03T12:00:00Z");
  assert.equal(table.status, 0, table.stderr);
  assert.match(table.stdout, /\| Before \| 2 \| 2 \| 1/);
  const json = invoke("--input", fixtureURL.pathname, "--json");
  assert.equal(json.status, 0, json.stderr);
  assert.equal(JSON.parse(json.stdout).observed_count, 4);
  for (const [mode, concurrency] of [["--ruleset-payload", 2], ["--rollback-payload", 1]]) {
    const result = invoke(mode, "--snapshot", rulesetURL.pathname);
    assert.equal(result.status, 0, result.stderr);
    assert.equal(JSON.parse(result.stdout).rules[2].parameters.max_entries_to_build, concurrency);
  }
});
