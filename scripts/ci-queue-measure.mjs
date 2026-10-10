// SPDX-License-Identifier: AGPL-3.0-only
// OPS-257: dependency-free, read-only GitHub CI measurement and merge-queue plan.
import { execFile } from "node:child_process";
import { readFileSync, statSync, writeFileSync } from "node:fs";
import { promisify } from "node:util";
import { pathToFileURL } from "node:url";

const execute = promisify(execFile);
const planURL = new URL("./ci-queue-ruleset.patch.json", import.meta.url);
const failureConclusions = new Set(["failure", "timed_out", "action_required", "startup_failure", "stale"]);
const perPage = 100;
const maxPages = 10;
const maxAttempts = 51; // GitHub permits 50 reruns in addition to the first attempt.

function readJSON(file) {
  if (statSync(file).size > 64 * 1024 * 1024) throw new Error("JSON input exceeds 64 MiB");
  return JSON.parse(readFileSync(file, "utf8"));
}

export async function ghGet(endpoint) {
  // No shell, mutations, credentials in arguments, or unlimited API output.
  const { stdout } = await execute("gh", ["api", "--method", "GET", endpoint,
    "-H", "Accept: application/vnd.github+json"], { timeout: 60_000, maxBuffer: 8 * 1024 * 1024 });
  return JSON.parse(stdout);
}

function integer(value, name, maximum) {
  const n = Number(value);
  if (!Number.isSafeInteger(n) || n < 1 || n > maximum) throw new Error(`${name} must be 1..${maximum}`);
  return n;
}
function timestamp(value) {
  return typeof value === "string" && Number.isFinite(Date.parse(value)) ? Date.parse(value) : null;
}
function seconds(start, end) {
  const a = timestamp(start), b = timestamp(end);
  return a !== null && b !== null && b >= a ? (b - a) / 1000 : null;
}
function earliest(values) {
  return values.filter((v) => timestamp(v) !== null).sort((a, b) => timestamp(a) - timestamp(b))[0] ?? null;
}
function latest(values) {
  return values.filter((v) => timestamp(v) !== null).sort((a, b) => timestamp(b) - timestamp(a))[0] ?? null;
}

async function pages(get, endpoint, field) {
  const items = [];
  for (let page = 1; page <= maxPages; page++) {
    const body = await get(`${endpoint}${endpoint.includes("?") ? "&" : "?"}per_page=${perPage}&page=${page}`);
    const batch = field ? body[field] : body;
    if (!Array.isArray(batch)) throw new Error(`invalid API ${field ?? "array"} response`);
    items.push(...batch);
    if (batch.length < perPage || (Number.isSafeInteger(body.total_count) && items.length >= body.total_count)) return items;
  }
  throw new Error(`pagination limit (${maxPages} pages) reached; refusing a truncated result`);
}

export async function collectRuns({ repository = "inspr-at/paimos", workflow = "ci.yml", count = 20, get = ghGet } = {}) {
  integer(count, "count", 1000);
  const base = `repos/${repository}/actions`;
  const found = new Map();
  for (let page = 1; page <= maxPages; page++) {
    const response = await get(`${base}/workflows/${encodeURIComponent(workflow)}/runs?event=merge_group&per_page=${perPage}&page=${page}`);
    if (!Array.isArray(response.workflow_runs)) throw new Error("invalid workflow runs response");
    for (const run of response.workflow_runs) {
      if (run.event === "merge_group") found.set(run.id, run);
    }
    if (found.size >= count || response.workflow_runs.length < perPage || found.size >= response.total_count) break;
    if (page === maxPages) throw new Error("run pagination limit reached; refusing a truncated sample");
  }
  const selected = [...found.values()].sort((a, b) => timestamp(b.created_at) - timestamp(a.created_at)).slice(0, count);
  const runs = [];
  for (const run of selected) {
    const attempts = [];
    const attemptCount = integer(run.run_attempt ?? 1, "run_attempt", maxAttempts);
    for (let number = 1; number <= attemptCount; number++) {
      const endpoint = `${base}/runs/${run.id}/attempts/${number}`;
      const metadata = await get(endpoint);
      if (metadata.id !== run.id || metadata.run_attempt !== number) throw new Error("attempt identity mismatch");
      const jobs = await pages(get, `${endpoint}/jobs`, "jobs");
      if (jobs.some((job) => job.run_attempt !== undefined && job.run_attempt !== number)) throw new Error("job attempt mismatch");
      attempts.push({ ...metadata, jobs });
    }
    runs.push({ ...run, attempts });
  }
  return { schema: 1, repository, workflow, requested_count: count, runs };
}

export function measureRun(run) {
  if (!Array.isArray(run.attempts) || run.attempts.length !== integer(run.run_attempt ?? 1, "run_attempt", maxAttempts)) {
    throw new Error(`run ${run.id}: incomplete attempt history`);
  }
  if (timestamp(run.created_at) === null || !run.head_sha) throw new Error(`run ${run.id}: missing creation time or head SHA`);
  const warnings = [];
  const attempts = run.attempts.map((attempt, index) => {
    if (attempt.run_attempt !== index + 1 || !Array.isArray(attempt.jobs)) throw new Error(`run ${run.id}: invalid attempt/jobs`);
    // Skipped jobs can have timestamps but did not occupy a runner.
    const active = attempt.jobs.filter((job) => job.conclusion !== "skipped" && timestamp(job.started_at) !== null && timestamp(job.started_at) >= timestamp(run.created_at));
    const start = earliest(active.map((job) => job.started_at));
    const finish = latest(active.map((job) => job.completed_at));
    const completeTimes = active.every((job) => seconds(job.started_at, job.completed_at) !== null);
    const duration = attempt.status === "completed" && completeTimes ? seconds(start, finish) : null;
    if (attempt.status === "completed" && duration === null) warnings.push(`attempt ${index + 1}: no usable job execution span`);
    const failures = attempt.jobs.filter((job) => failureConclusions.has(job.conclusion)).map((job) => job.name);
    return { number: index + 1, status: attempt.status, conclusion: attempt.conclusion, start, finish,
      duration_seconds: duration, failed_jobs: failures };
  });
  const final = attempts.at(-1);
  return {
    id: run.id, head_sha: run.head_sha, head_branch: run.head_branch, created_at: run.created_at,
    status: final.status, conclusion: final.conclusion, attempts,
    // Initial dispatch/runner delay only. The Actions REST response does not
    // expose a rerun-request timestamp or PR queue-enqueue timestamp.
    initial_queue_seconds: seconds(run.created_at, attempts[0].start),
    run_seconds: final.duration_seconds,
    elapsed_seconds: final.duration_seconds !== null ? seconds(run.created_at, final.finish) : null,
    reruns: attempts.length - 1, warnings,
  };
}

export function percentile(values, fraction) {
  const sorted = values.filter((v) => Number.isFinite(v)).sort((a, b) => a - b);
  return sorted.length ? sorted[Math.max(0, Math.ceil(sorted.length * fraction) - 1)] : null;
}

function summarize(runs) {
  const queues = runs.map((r) => r.initial_queue_seconds).filter((v) => v !== null);
  const durations = runs.map((r) => r.run_seconds).filter((v) => v !== null);
  return { runs: runs.length, completed: runs.filter((r) => r.status === "completed").length,
    failed: runs.filter((r) => failureConclusions.has(r.conclusion)).length,
    cancelled: runs.filter((r) => r.conclusion === "cancelled").length,
    reruns: runs.reduce((n, r) => n + r.reruns, 0), queue_samples: queues.length, duration_samples: durations.length,
    queue_p50_seconds: percentile(queues, 0.5), queue_p95_seconds: percentile(queues, 0.95),
    run_p50_seconds: percentile(durations, 0.5), run_p95_seconds: percentile(durations, 0.95) };
}

export function analyze(snapshot, splitAt) {
  if (snapshot.schema !== 1 || !Array.isArray(snapshot.runs) || snapshot.runs.length > 1000) throw new Error("invalid measurement snapshot");
  const runs = snapshot.runs.filter((r) => r.event === "merge_group").map(measureRun).sort((a, b) => timestamp(a.created_at) - timestamp(b.created_at));
  if (new Set(runs.map((r) => r.id)).size !== runs.length) throw new Error("duplicate run IDs in snapshot");
  let before, after;
  if (splitAt) {
    if (!/^\d{4}-\d{2}-\d{2}T.*(?:Z|[+-]\d{2}:\d{2})$/.test(splitAt) || timestamp(splitAt) === null) throw new Error("split-at must be an ISO timestamp with timezone");
    before = runs.filter((r) => timestamp(r.created_at) < timestamp(splitAt));
    after = runs.filter((r) => timestamp(r.created_at) >= timestamp(splitAt));
  } else {
    const middle = Math.floor(runs.length / 2);
    before = runs.slice(0, middle); after = runs.slice(middle);
  }
  const groups = new Map(), failures = new Map();
  for (const run of runs) {
    // An exact SHA is an observable merge-group identity. Rebuilt queue heads
    // with a different SHA are different groups; do not invent a restart cause.
    const group = groups.get(run.head_sha) ?? { head_sha: run.head_sha, branches: [], run_ids: [], failed_attempts: 0, cancelled_attempts: 0, reruns: 0 };
    group.branches = [...new Set([...group.branches, run.head_branch].filter(Boolean))];
    group.run_ids.push(run.id); group.reruns += run.reruns;
    for (const attempt of run.attempts) {
      if (failureConclusions.has(attempt.conclusion)) group.failed_attempts++;
      if (attempt.conclusion === "cancelled") group.cancelled_attempts++;
      for (const name of attempt.failed_jobs) failures.set(name, (failures.get(name) ?? 0) + 1);
    }
    groups.set(run.head_sha, group);
  }
  return { repository: snapshot.repository, workflow: snapshot.workflow,
    requested_count: snapshot.requested_count ?? snapshot.runs.length, observed_count: runs.length,
    comparison: splitAt ? `Before/after ${splitAt} (by run creation time)` : "Before/after = older/newer half of sample; no deployment boundary supplied",
    before: summarize(before), after: summarize(after), runs,
    groups: [...groups.values()].map((g) => ({ ...g, repeated_runs: g.run_ids.length - 1, restarts: g.reruns + g.run_ids.length - 1 })),
    failed_jobs: [...failures].map(([name, failed_attempts]) => ({ name, failed_attempts })).sort((a, b) => b.failed_attempts - a.failed_attempts || a.name.localeCompare(b.name)) };
}

const minutes = (value) => value === null ? "n/a" : (value / 60).toFixed(2);
function safeCell(value) { return String(value ?? "").replace(/[|\r\n]/g, " "); }
export function formatReport(report) {
  const rows = [
    `${report.repository} / ${report.workflow}: last ${report.observed_count}/${report.requested_count} merge_group CI runs`,
    report.comparison,
    "Queue = initial run creation → first executing job (dispatch/runner delay, not PR residence in the merge queue).",
    "Run = first → last executing job in the latest attempt; elapsed includes initial queue and rerun gaps. No updated_at completion proxy.",
    "Rerun queue wait and cross-SHA rebuild causes are unavailable from these REST endpoints. Restarts below are within this sample only.",
    "",
    "| Sample | Runs | Completed | Failed | Cancelled | Reruns | Queue n | Queue p50/p95 min | Run n | Run p50/p95 min |",
    "|---|---:|---:|---:|---:|---:|---:|---|---:|---|",
  ];
  for (const [name, s] of [["Before", report.before], ["After", report.after]]) {
    rows.push(`| ${name} | ${s.runs} | ${s.completed} | ${s.failed} | ${s.cancelled} | ${s.reruns} | ${s.queue_samples} | ${minutes(s.queue_p50_seconds)} / ${minutes(s.queue_p95_seconds)} | ${s.duration_samples} | ${minutes(s.run_p50_seconds)} / ${minutes(s.run_p95_seconds)} |`);
  }
  rows.push("", "| Run | Conclusion | Initial queue min | Latest run min | Elapsed min | Reruns |", "|---|---|---:|---:|---:|---:|");
  for (const r of report.runs) rows.push(`| ${r.id} | ${safeCell(r.conclusion ?? r.status)} | ${minutes(r.initial_queue_seconds)} | ${minutes(r.run_seconds)} | ${minutes(r.elapsed_seconds)} | ${r.reruns} |`);
  rows.push("", "| Exact group SHA | Run IDs | Failed attempts | Cancelled attempts | Reruns | Repeated runs | Restarts |", "|---|---|---:|---:|---:|---:|---:|");
  for (const g of report.groups) rows.push(`| ${safeCell(g.head_sha)} | ${g.run_ids.join(", ")} | ${g.failed_attempts} | ${g.cancelled_attempts} | ${g.reruns} | ${g.repeated_runs} | ${g.restarts} |`);
  rows.push("", "Failed jobs (attempt occurrences, not individual tests):");
  for (const f of report.failed_jobs) rows.push(`- ${safeCell(f.name)}: ${f.failed_attempts}`);
  if (!report.failed_jobs.length) rows.push("- None observed.");
  for (const r of report.runs) for (const warning of r.warnings) rows.push(`Warning: run ${r.id}: ${warning}`);
  return rows.join("\n") + "\n";
}

export function rulesetPlan(current, plan = readJSON(planURL)) {
  if (current.source_type && current.source_type !== "Repository") throw new Error("inherited ruleset: use its owning scope, not this repository tool");
  if (current.target !== "branch" || !Array.isArray(current.rules) || typeof current.name !== "string" || !current.enforcement) throw new Error("invalid branch ruleset");
  const indexes = current.rules.flatMap((r, i) => r.type === "merge_queue" ? [i] : []);
  if (indexes.length !== 1) throw new Error("expected exactly one existing merge_queue rule");
  if (plan.schema !== 1 || plan.select?.rule_type !== "merge_queue" || !Array.isArray(plan.patch)) throw new Error("invalid ruleset patch plan");
  const writable = ["name", "target", "enforcement", "bypass_actors", "conditions", "rules"];
  const rollback = Object.fromEntries(writable.filter((key) => Object.hasOwn(current, key)).map((key) => [key, structuredClone(current[key])]));
  const candidate = structuredClone(rollback), index = indexes[0], diff = [];
  const allowed = new Set(["max_entries_to_build", "max_entries_to_merge", "min_entries_to_merge", "min_entries_to_merge_wait_minutes"]);
  const parameters = candidate.rules[index].parameters;
  if (!parameters) throw new Error("merge_queue parameters missing");
  for (const operation of plan.patch) {
    const key = operation.path?.slice(1);
    if (operation.op !== "replace" || operation.path !== `/${key}` || !allowed.has(key) || !Object.hasOwn(parameters, key)) throw new Error("unsupported or missing merge_queue patch path");
    if (!Number.isSafeInteger(operation.value) || operation.value < 1 || operation.value > 100) throw new Error("invalid merge_queue patch value");
    const old = parameters[key];
    if (old !== operation.value) diff.push({ op: "replace", path: `/rules/${index}/parameters/${key}`, before: old, value: operation.value });
    parameters[key] = operation.value;
  }
  if (parameters.min_entries_to_merge > parameters.max_entries_to_merge) throw new Error("merge_queue min exceeds max");
  return { candidate, rollback, diff };
}

const help = `Usage (Node 22+, authenticated gh; every network call is GET):
  node scripts/ci-queue-measure.mjs [--count 20] [--split-at ISO] [--json]
    [--repo inspr-at/paimos] [--workflow ci.yml] [--save snapshot.json]
  node scripts/ci-queue-measure.mjs --input snapshot.json [--split-at ISO] [--json]
  node scripts/ci-queue-measure.mjs --ruleset ID --dry-run
  node scripts/ci-queue-measure.mjs --dry-run --snapshot current-ruleset.json
  node scripts/ci-queue-measure.mjs --ruleset-payload --snapshot current-ruleset.json
  node scripts/ci-queue-measure.mjs --rollback-payload --snapshot current-ruleset.json

--count is unique workflow runs, including all attempts (max 1000 runs/51 attempts).
--split-at is the change's UTC/offset timestamp; omit for an older/newer-half comparison.
--save writes the raw measurement snapshot for reproducible offline analysis.
Ruleset mode defaults to dry-run and prints only changed JSON paths. Payload modes
emit complete PUT bodies locally; they never invoke PUT. Read the patch's _comment
for operator backup/apply/rollback commands. No --apply mode exists.
`;

export async function main(argv = process.argv.slice(2), get = ghGet) {
  const opts = {};
  const flags = new Set(["--json", "--dry-run", "--ruleset-payload", "--rollback-payload", "--help"]);
  const values = new Set(["--count", "--split-at", "--repo", "--workflow", "--input", "--save", "--ruleset", "--snapshot"]);
  for (let i = 0; i < argv.length; i++) {
    const key = argv[i];
    if (flags.has(key)) opts[key] = true;
    else if (values.has(key) && argv[i + 1] && !argv[i + 1].startsWith("--")) opts[key] = argv[++i];
    else throw new Error(`unknown option or missing value: ${key}`);
  }
  if (opts["--help"]) return help;
  const repository = opts["--repo"] ?? "inspr-at/paimos";
  if (!/^[\w.-]+\/[\w.-]+$/.test(repository)) throw new Error("invalid owner/repository");
  const rulesetMode = ["--ruleset", "--snapshot", "--dry-run", "--ruleset-payload", "--rollback-payload"].some((key) => opts[key]);
  if (rulesetMode) {
    if (["--input", "--save", "--count", "--split-at", "--workflow", "--json"].some((key) => opts[key])) throw new Error("measurement options cannot be combined with ruleset mode");
    if (["--dry-run", "--ruleset-payload", "--rollback-payload"].filter((key) => opts[key]).length > 1) throw new Error("choose one ruleset output mode");
    if (opts["--snapshot"] && opts["--ruleset"]) throw new Error("choose a local snapshot or live ruleset ID");
    if ((opts["--ruleset-payload"] || opts["--rollback-payload"]) && !opts["--snapshot"]) throw new Error("payload preparation requires a saved snapshot for rollback");
    const current = opts["--snapshot"] ? readJSON(opts["--snapshot"])
      : await get(`repos/${repository}/rulesets/${integer(opts["--ruleset"], "ruleset", Number.MAX_SAFE_INTEGER)}`);
    const plan = rulesetPlan(current);
    if (opts["--ruleset-payload"]) return JSON.stringify(plan.candidate, null, 2) + "\n";
    if (opts["--rollback-payload"]) return JSON.stringify(plan.rollback, null, 2) + "\n";
    return JSON.stringify(plan.diff, null, 2) + "\n";
  }
  if (opts["--input"] && opts["--save"]) throw new Error("save is only available for live collection");
  if (opts["--input"] && ["--count", "--repo", "--workflow"].some((key) => opts[key])) throw new Error("input snapshot already defines the sample, repository and workflow");
  const snapshot = opts["--input"] ? readJSON(opts["--input"]) : await collectRuns({
    repository, workflow: opts["--workflow"] ?? "ci.yml", count: integer(opts["--count"] ?? 20, "count", 1000), get,
  });
  const report = analyze(snapshot, opts["--split-at"]);
  if (opts["--save"]) writeFileSync(opts["--save"], JSON.stringify(snapshot, null, 2) + "\n", { flag: "wx" });
  return opts["--json"] ? JSON.stringify(report, null, 2) + "\n" : formatReport(report);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().then((output) => process.stdout.write(output)).catch((error) => {
    // Child process stderr may contain user-controlled content. Keep errors
    // value-free; do not forward gh's resolved authentication diagnostics.
    process.stderr.write(`ci-queue-measure: ${error.cmd ? "gh GET failed (network/authentication/API); no report generated" : error.message}\n`);
    process.exitCode = 1;
  });
}
