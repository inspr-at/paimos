// SPDX-License-Identifier: AGPL-3.0-only
// Release timing evidence (AEON-416, deploy-speed-10x §1 and §5 item 10).
//
// cut → live     rollout cut_at → live_at. wall_min is the truncated UTC-minute span,
//                which is how §1 counts 128 / 38 / 84 / 58.
// PR → merge     the release pull request's createdAt → mergedAt.
// gate → live    median of live_at minus each gate/cross-family success (or rollout gate_ok_at).
// rollback       the latest rollout rollback interval, else the latest pin PR whose
//                title says rollback. A forward pin PR is not a rollback.
//
// GitHub access is read-only. Lists are paginated GET gh api calls; a pin's checks
// come from gh pr view. Workflow dispatch, rerun, and release writes are refused.
// Only frozen calls issued by ghRead's fixed operation templates may reach gh.
//
// Rollback is the latest rollout interval, otherwise the latest pin PR whose title
// says rollback. A forward pin stays the forward pin. Disagreeing sources are
// ambiguous. CI counts attempts inside the pull request's open→merge window; a
// rerun without attempt history is unknown. A gate median needs every sample.
// Timestamps need a zone. Reversed intervals are unknown. A list that hits the
// cap is collection.truncated, not evidence that the missing tail was empty.

import { spawnSync } from "node:child_process";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { basename, join } from "node:path";
import { pathToFileURL } from "node:url";

const VERSION_RE = /(\d{12}\.\d+\.\d+)/;
const GH_READ_CALLS = new WeakSet();
const GH_READ_FIELDS = {
  "release-runs": ["repo", "page", "pageSize"],
  "ci-runs": ["repo", "branch", "page", "pageSize"],
  pulls: ["repo", "page", "pageSize"],
  "pin-search": ["repo", "page", "pageSize"],
  jobs: ["repo", "id", "page", "pageSize"],
  statuses: ["repo", "sha", "page", "pageSize"],
  attempt: ["repo", "id", "attempt"],
  "pin-view": ["repo", "number"],
};

// No arbitrary argv, path, method, or flag is accepted. All API calls have no
// body flags and therefore use GET; the sole PR operation has exact JSON flags.
export function ghRead(operation, params = {}) {
  const fields = Object.hasOwn(GH_READ_FIELDS, operation) ? GH_READ_FIELDS[operation] : null;
  if (!fields || Object.keys(params).some((key) => !fields.includes(key))) throw new Error("refusing unapproved gh operation");
  const repo = params.repo;
  if (typeof repo !== "string" || !/^[A-Za-z0-9_-]+\/[A-Za-z0-9_.-]+$/.test(repo) || repo.split("/").some((part) => part === "." || part === "..")) {
    throw new Error("refusing invalid gh repository");
  }
  const positive = (key, max = Number.MAX_SAFE_INTEGER) => {
    const value = params[key];
    if (!/^[1-9]\d*$/.test(String(value)) || !Number.isSafeInteger(Number(value)) || Number(value) > max) throw new Error(`refusing invalid gh ${key}`);
    return String(value);
  };
  let path;
  switch (operation) {
    case "release-runs": path = `repos/${repo}/actions/workflows/release.yml/runs`; break;
    case "ci-runs":
      if (typeof params.branch !== "string" || !params.branch) throw new Error("refusing invalid gh branch");
      path = `repos/${repo}/actions/workflows/ci.yml/runs?branch=${encodeURIComponent(params.branch)}&event=pull_request`;
      break;
    case "pulls": path = `repos/${repo}/pulls?state=closed&sort=updated&direction=desc`; break;
    case "pin-search": path = `search/issues?q=${encodeURIComponent(`repo:${repo} is:pr AEON: pin in:title`)}&sort=updated&order=desc`; break;
    case "jobs": path = `repos/${repo}/actions/runs/${positive("id")}/jobs`; break;
    case "statuses":
      if (typeof params.sha !== "string" || !/^[a-fA-F0-9]{3,64}$/.test(params.sha)) throw new Error("refusing invalid gh SHA");
      path = `repos/${repo}/commits/${params.sha}/statuses`;
      break;
    case "attempt": path = `repos/${repo}/actions/runs/${positive("id")}/attempts/${positive("attempt")}`; break;
    case "pin-view": break;
  }
  if (fields.includes("page")) path += `${path.includes("?") ? "&" : "?"}page=${positive("page")}&per_page=${positive("pageSize", 100)}`;
  const args = Object.freeze(operation === "pin-view"
    ? ["pr", "view", positive("number"), "--repo", repo, "--json", "number,title,createdAt,mergedAt,statusCheckRollup"]
    : ["api", path]);
  GH_READ_CALLS.add(args);
  return args;
}

export function assertReadOnly(args) {
  if (!GH_READ_CALLS.has(args)) throw new Error("refusing unapproved gh command; arbitrary flags, write fields and non-GET methods are forbidden");
}

function redact(text) {
  return String(text || "")
    .replace(/github_pat_[A-Za-z0-9_]+/g, "[redacted]")
    .replace(/gh[pousr]_[A-Za-z0-9_]+/g, "[redacted]")
    .replace(/Bearer\s+\S+/gi, "Bearer [redacted]");
}

export function defaultGh(args) {
  assertReadOnly(args);
  const run = spawnSync("gh", args, { encoding: "utf8", maxBuffer: 20 * 1024 * 1024 });
  if (run.error) throw new Error(redact(run.error.message));
  if (run.status !== 0) throw new Error(redact(run.stderr || `gh exited ${run.status}`).trim());
  const out = run.stdout.trim();
  if (!out) {
    if (args[0] === "api") throw new Error("gh api returned an empty body");
    return [];
  }
  try {
    return JSON.parse(out);
  } catch {
    throw new Error("gh returned non-JSON");
  }
}

function at(obj, ...keys) {
  for (const key of keys) {
    if (obj && obj[key] != null && obj[key] !== "") return obj[key];
  }
  return null;
}

const ZONE_RE = /(?:[zZ]|[+-]\d{2}:\d{2})$/;

function instant(value) {
  if (value == null || value === "") return { ms: null, reason: "missing timestamp" };
  const text = String(value).trim();
  if (!ZONE_RE.test(text)) return { ms: null, reason: "missing timezone" };
  const ms = Date.parse(text);
  if (!Number.isFinite(ms)) return { ms: null, reason: "not a timestamp" };
  return { ms, reason: null };
}

function parseTime(value, label) {
  const parsed = instant(value);
  if (parsed.reason === "missing timezone") throw new Error(`${label} is missing a timezone`);
  if (parsed.ms == null) throw new Error(`${label} is not a timestamp`);
  return parsed.ms;
}

function span(start, end) {
  const interval = validateInterval(validateTime(start), validateTime(end));
  return { seconds: duration(interval), reason: reasonOf(interval) };
}

export function secondsBetween(start, end) {
  return span(start, end).seconds;
}

// §1's published walls are the difference of the timestamps with seconds dropped.
export function wallMin(start, end) {
  return minuteSpan(validateInterval(validateTime(start), validateTime(end)));
}

export function roundMin(seconds) {
  if (seconds == null || !Number.isFinite(seconds)) return null;
  return Math.round(seconds / 60);
}

function fmt(seconds) {
  if (seconds == null || !Number.isFinite(seconds)) return "-";
  const sign = seconds < 0 ? "-" : "";
  const total = Math.abs(Math.round(seconds));
  const m = Math.floor(total / 60);
  const s = total % 60;
  return `${sign}${m}m${String(s).padStart(2, "0")}s`;
}

function fmtMinSec(seconds) {
  const total = Math.abs(Math.round(seconds));
  return `${Math.floor(total / 60)} min ${total % 60} s`;
}

function versionIn(text) {
  const match = VERSION_RE.exec(String(text || ""));
  return match ? match[1] : null;
}

function labelFromRef(ref) {
  const match = /^(?:rel|night)\/r(\d+[a-z]?)$/.exec(ref || "");
  return match ? match[1] : null;
}

function median(values) {
  if (!values.length) return null;
  const sorted = [...values].sort((a, b) => a - b);
  const mid = Math.floor(sorted.length / 2);
  if (sorted.length % 2) return sorted[mid];
  return (sorted[mid - 1] + sorted[mid]) / 2;
}

function asArray(value) {
  return Array.isArray(value) ? value : [];
}

export function normalizeInput(raw) {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) throw new Error("invalid timing input");
  const normalized = {
    pullRequests: asArray(raw.pull_requests ?? raw.pullRequests).map(normalizePullRequest),
    pinPullRequests: asArray(raw.pin_pull_requests ?? raw.pinPullRequests).map(normalizePin),
    runs: asArray(raw.workflow_runs ?? raw.workflowRuns ?? raw.runs).map(normalizeRun),
    statuses: asArray(raw.statuses).map(normalizeStatus),
    rollouts: asArray(raw.rollouts).map(normalizeRollout),
  };
  if (raw.collection && typeof raw.collection === "object") normalized.collection = raw.collection;
  return normalized;
}

function normalizePullRequest(pr) {
  const head = pr.head && typeof pr.head === "object" ? pr.head : null;
  return {
    number: pr.number ?? null,
    title: pr.title || "",
    createdAt: at(pr, "createdAt", "created_at"),
    mergedAt: at(pr, "mergedAt", "merged_at"),
    headRef: at(pr, "headRefName", "headRef", "head_ref") || head?.ref || "",
    mergeSha: pr.mergeCommit?.oid || pr.mergeSha || pr.merge_sha || pr.merge_commit_sha || null,
  };
}

function normalizeCheck(check) {
  return {
    name: check.name || check.context || "",
    startedAt: at(check, "startedAt", "started_at"),
    completedAt: at(check, "completedAt", "completed_at"),
  };
}

function normalizePin(pr) {
  const checks = asArray(pr.checks ?? pr.statusCheckRollup).map(normalizeCheck);
  return { ...normalizePullRequest(pr), checks };
}

function normalizeStep(step) {
  return {
    name: step.name || "",
    startedAt: at(step, "startedAt", "started_at"),
    completedAt: at(step, "completedAt", "completed_at"),
  };
}

function normalizeAttempt(attempt) {
  return {
    number: attempt.number ?? attempt.runAttempt ?? attempt.run_attempt ?? null,
    conclusion: String(attempt.conclusion || "").toLowerCase(),
    startedAt: at(attempt, "startedAt", "started_at", "runStartedAt", "run_started_at"),
    completedAt: at(attempt, "completedAt", "completed_at", "updatedAt", "updated_at"),
  };
}

function normalizeRun(run) {
  const attempt = Number(run.runAttempt ?? run.run_attempt ?? 1);
  return {
    id: run.databaseId ?? run.id ?? null,
    workflow: run.workflowName || run.workflow || "",
    event: run.event || "",
    conclusion: String(run.conclusion || "").toLowerCase(),
    createdAt: at(run, "createdAt", "created_at"),
    updatedAt: at(run, "updatedAt", "updated_at"),
    runAttempt: attempt,
    runStartedAt: at(run, "runStartedAt", "run_started_at"),
    jobsTruncated: Boolean(run.jobsTruncated),
    headBranch: run.headBranch || run.head_branch || "",
    headSha: run.headSha || run.head_sha || "",
    attempts: asArray(run.attempts).map(normalizeAttempt),
    jobs: asArray(run.jobs).map((job) => ({
      name: job.name || "",
      startedAt: at(job, "startedAt", "started_at"),
      completedAt: at(job, "completedAt", "completed_at"),
      steps: asArray(job.steps).map(normalizeStep),
    })),
  };
}

function normalizeStatus(status) {
  return {
    id: status.id ?? null,
    sha: status.sha || "",
    context: status.context || "",
    state: String(status.state || "").toLowerCase(),
    createdAt: at(status, "createdAt", "created_at"),
    updatedAt: at(status, "updatedAt", "updated_at"),
    description: status.description || "",
  };
}

function normalizeRollout(rollout) {
  return {
    id: rollout.id ?? null,
    outcome: rollout.outcome || "",
    direction: rollout.direction || "",
    release: rollout.release || null,
    version: rollout.version || null,
    sequence: Number.isInteger(rollout.sequence) ? rollout.sequence : null,
    cutAt: at(rollout, "cutAt", "cut_at"),
    liveAt: at(rollout, "liveAt", "live_at"),
    rollbackStartedAt: at(rollout, "rollbackStartedAt", "rollback_started_at"),
    rollbackFinishedAt: at(rollout, "rollbackFinishedAt", "rollback_finished_at"),
    tickets: asArray(rollout.tickets).map((ticket) => ({
      key: ticket.key || ticket.ticket || "",
      gateOkAt: at(ticket, "gateOkAt", "gate_ok_at"),
    })),
  };
}

/** @typedef {{state: "complete" | "partial", reasons: string[]}} Completeness */
/** @typedef {{value: string|null, ms: number|null, completeness: Completeness}} Timestamp */
/** @typedef {{start: Timestamp, end: Timestamp, completeness: Completeness}} Interval */

function completeness(reasons = []) {
  const unique = [...new Set(reasons.filter(Boolean))];
  return { state: unique.length ? "partial" : "complete", reasons: unique };
}

function reasonOf(record) {
  const reasons = record?.completeness.reasons || [];
  for (const primary of ["truncated", "attempt history unavailable"]) {
    if (reasons.includes(primary)) return primary;
  }
  return reasons.join("; ") || null;
}

function isComplete(record) {
  return record?.completeness.state === "complete";
}

/** @returns {Timestamp} */
function validateTime(value) {
  const parsed = instant(value);
  return { value: value ?? null, ms: parsed.ms, completeness: completeness([parsed.reason]) };
}

/** @param {Timestamp} start @param {Timestamp} end @returns {Interval} */
function validateInterval(start, end) {
  const reasons = [...start.completeness.reasons, ...end.completeness.reasons];
  if (!reasons.length && end.ms < start.ms) reasons.push("reversed");
  return { start, end, completeness: completeness(reasons) };
}

// Arithmetic consumes only validated intervals. Missing or invalid evidence has
// a completeness reason before any duration, median, or adjustment is computed.
function duration(interval) {
  return isComplete(interval) ? Math.round((interval.end.ms - interval.start.ms) / 1000) : null;
}

function minuteSpan(interval) {
  return isComplete(interval) ? Math.floor(interval.end.ms / 60000) - Math.floor(interval.start.ms / 60000) : null;
}

function validateRecord(record, fields, start, end) {
  const times = Object.fromEntries(fields.map((key) => [key, validateTime(record[key])]));
  const interval = validateInterval(times[start], times[end]);
  const reasons = [...interval.completeness.reasons];
  for (const key of fields) {
    if (record[key] != null) reasons.push(...times[key].completeness.reasons);
  }
  return { ...record, times, interval, completeness: completeness(reasons) };
}

function uniqueRecords(records, keyOf) {
  const seen = new Set();
  return records.filter((record) => {
    const key = keyOf(record);
    if (key == null) return true;
    if (seen.has(String(key))) return false;
    seen.add(String(key));
    return true;
  });
}

function validateRun(run) {
  const record = validateRecord(run, ["createdAt", "updatedAt", "runStartedAt"], "createdAt", "updatedAt");
  if (run.runStartedAt) {
    record.completeness = completeness([
      ...record.completeness.reasons,
      ...validateInterval(record.times.createdAt, record.times.runStartedAt).completeness.reasons,
      ...validateInterval(record.times.runStartedAt, record.times.updatedAt).completeness.reasons,
    ]);
  }
  const supplied = run.attempts.length > 0;
  const attempts = supplied ? run.attempts : [{
    number: run.runAttempt,
    conclusion: run.conclusion,
    startedAt: run.runStartedAt || (run.runAttempt === 1 ? run.createdAt : null),
    completedAt: run.updatedAt,
  }];
  const historyReasons = [];
  if (!Number.isSafeInteger(run.runAttempt) || run.runAttempt < 1) historyReasons.push("invalid runAttempt");
  if (run.runAttempt > 1 && (!supplied || attempts.length !== run.runAttempt)) historyReasons.push("attempt history unavailable");
  // Legacy bundles may list attempts in order without numbers. When GitHub
  // supplies numbers, require exactly 1..runAttempt with no duplicates or gaps.
  if (supplied && (attempts.length !== run.runAttempt || attempts.some((item) => item.number != null))) {
    const numbers = attempts.map((item) => Number(item.number)).sort((a, b) => a - b);
    if (numbers.length !== run.runAttempt || numbers.some((n, i) => n !== i + 1)) historyReasons.push("attempt history unavailable");
  }
  let previousEnd = null;
  record.attempts = attempts.map((attempt) => {
    const validated = validateRecord(attempt, ["startedAt", "completedAt"], "startedAt", "completedAt");
    if (isComplete(validated)) {
      if (previousEnd != null && validated.interval.start.ms < previousEnd) historyReasons.push("attempt intervals overlap or are unordered");
      previousEnd = validated.interval.end.ms;
      if (isComplete(record.interval) && (validated.interval.start.ms < record.interval.start.ms || validated.interval.end.ms > record.interval.end.ms)) {
        historyReasons.push("attempt outside workflow lifetime");
      }
    }
    if (!["success", "failure", "cancelled", "skipped", "timed_out", "action_required", "neutral", "startup_failure", "stale"].includes(attempt.conclusion)) {
      validated.completeness = completeness([...validated.completeness.reasons, "attempt conclusion unavailable"]);
    }
    historyReasons.push(...validated.completeness.reasons);
    return validated;
  });
  record.history = { completeness: completeness([...record.completeness.reasons, ...historyReasons]) };
  record.completeness = record.history.completeness;
  record.jobs = run.jobs.map((job) => ({
    ...validateRecord(job, ["startedAt", "completedAt"], "startedAt", "completedAt"),
    steps: job.steps.map((step) => validateRecord(step, ["startedAt", "completedAt"], "startedAt", "completedAt")),
  }));
  return record;
}

function stableKey(record) {
  return String(record.id ?? record.number ?? JSON.stringify(record));
}

function byTime(items, timeOf, newest = false) {
  return [...items].sort((a, b) => {
    const x = timeOf(a).ms;
    const y = timeOf(b).ms;
    // An unorderable candidate cannot establish that another is the latest.
    if (x == null || y == null) {
      if (x == null && y != null) return -1;
      if (y == null && x != null) return 1;
    } else if (x !== y) return newest ? (x > y ? -1 : 1) : (x < y ? -1 : 1);
    return stableKey(a).localeCompare(stableKey(b));
  });
}

function selectReleaseRun(runs, version) {
  const tagged = runs.filter((run) => run.workflow === "Release" && versionIn(run.headBranch) === version);
  const pushed = tagged.filter((run) => run.conclusion === "success" && run.event === "push");
  return byTime(pushed.length ? pushed : tagged, (run) => run.times.createdAt)[0] || null;
}

function validateCI(runs, pr, truncated) {
  const empty = { samples: [], observedAttempts: 0, completeness: completeness(["missing PR lifetime"]), stall: null };
  if (!pr || !isComplete(pr.interval)) return { ...empty, completeness: completeness([`invalid PR lifetime: ${reasonOf(pr?.interval) || "missing timestamp"}`]) };
  if (truncated) return { ...empty, completeness: completeness(["truncated"]) };
  if (!pr.headRef) return { ...empty, completeness: completeness(["missing PR branch"]) };
  const samples = [];
  const reasons = [];
  let observedAttempts = 0;
  for (const run of runs) {
    if (run.workflow !== "CI" || run.event !== "pull_request" || run.headBranch !== pr.headRef) continue;
    if (isComplete(run.interval) && (run.interval.end.ms < pr.interval.start.ms || run.interval.start.ms > pr.interval.end.ms)) continue;
    reasons.push(...run.history.completeness.reasons);
    for (const attempt of run.attempts) {
      if (!isComplete(attempt)) continue;
      if (attempt.interval.start.ms < pr.interval.start.ms || attempt.interval.end.ms > pr.interval.end.ms) continue;
      observedAttempts++;
      samples.push(attempt);
    }
  }
  samples.sort((a, b) => a.interval.start.ms - b.interval.start.ms);
  if (!samples.length && !reasons.length) reasons.push("no CI attempts in PR lifetime");
  const failure = samples.find((item) => item.conclusion === "failure") || null;
  const success = failure
    ? samples.find((item) => item.conclusion === "success" && item.interval.start.ms >= failure.interval.start.ms) || null
    : [...samples].reverse().find((item) => item.conclusion === "success") || null;
  const stall = failure && success ? validateInterval(failure.interval.start, success.interval.start) : null;
  return { samples, failure, success, stall, observedAttempts, completeness: completeness(reasons) };
}

function validateGate(rollout, statuses, sha, live, truncated) {
  const tickets = rollout?.tickets || [];
  const records = tickets.length ? tickets : statuses.filter((status) => status.sha === sha && status.context === "gate/cross-family" && status.state === "success");
  const intervals = records.map((record) => {
    const interval = validateInterval(tickets.length ? record.gateTime : record.times.updatedAt, live);
    interval.completeness = completeness([...interval.completeness.reasons, ...record.completeness.reasons]);
    return interval;
  });
  const missing = intervals.filter((item) => !isComplete(item)).length;
  const reasons = [];
  if (!tickets.length && truncated) reasons.push("truncated");
  if (missing) reasons.push("incomplete");
  if (!intervals.length && !reasons.length) reasons.push("no gate evidence");
  return { intervals, missing, completeness: completeness(reasons) };
}

function isRollbackRollout(rollout) {
  return Boolean(rollout.rollbackStartedAt || rollout.rollbackFinishedAt || /rollback/i.test(`${rollout.outcome} ${rollout.direction}`));
}

function validateRollback(rollouts, pins, truncated) {
  const rollout = byTime(rollouts.filter((item) => item.rollbackStartedAt || item.rollbackFinishedAt), (item) => item.times.rollbackStartedAt, true)[0] || null;
  const pin = byTime(pins, (item) => item.times.createdAt, true)[0] || null;
  if (truncated) return { interval: null, source: null, completeness: completeness(["truncated"]) };
  // Select the attempt before checking completeness; never fall back to an
  // older measured duration when the newest attempt has not finished.
  const candidates = [rollout && { interval: rollout.rollback, source: "rollout" }, pin && { interval: pin.interval, source: "pin" }].filter(Boolean);
  const latest = byTime(candidates, (item) => item.interval.start, true)[0];
  if (!latest) return { interval: null, source: null, completeness: completeness(["no rollback evidence"]) };
  if (!isComplete(latest.interval)) return { ...latest, completeness: latest.interval.completeness };
  if (candidates.length === 2 && candidates.every((item) => isComplete(item.interval))) {
    // Both sources describe the same attempt only when their boundaries agree.
    const [a, b] = candidates.map((item) => item.interval);
    if (a.start.ms !== b.start.ms || a.end.ms !== b.end.ms) return { interval: null, source: null, completeness: completeness(["ambiguous"]) };
    return { ...candidates[0], completeness: completeness() };
  }
  return { ...latest, completeness: completeness() };
}

function validateRelease(input, version) {
  const run = selectReleaseRun(input.runs, version);
  const matches = input.rollouts.filter((item) => item.version === version);
  const forward = matches.filter((item) => (item.cutAt || item.liveAt) && (
    !isRollbackRollout(item) || item.outcome === "live"
    || (matches.length === 1 && (item.release || item.sequence != null) && !/rollback/i.test(`${item.outcome} ${item.direction}`))
  ));
  // A rollout with an embedded rollback still carries its own forward interval;
  // standalone rollback records must never overwrite another forward record.
  const rollout = byTime(forward, (item) => item.times.cutAt)[0] || null;
  const prMatches = input.pullRequests.filter((pr) => run && pr.mergeSha && pr.mergeSha === run.headSha);
  const prs = prMatches.length ? prMatches : input.pullRequests.filter((pr) => versionIn(pr.title) === version);
  const pr = byTime(prs, (item) => item.times.createdAt)[0] || null;
  const pins = input.pinPullRequests.filter((item) => versionIn(item.title) === version);
  const forwardPins = pins.filter((item) => !isRollbackTitle(item.title));
  const names = input.collection.truncated;
  const pinReason = names.includes("pin_pull_requests") ? "truncated" : forwardPins.length > 1 ? "ambiguous" : null;
  const pin = pinReason ? null : forwardPins[0] || null;
  const cut = rollout?.interval || validateInterval(validateTime(null), validateTime(null));
  const live = rollout?.times.liveAt || validateTime(null);
  const prInterval = pr?.interval || validateInterval(validateTime(null), validateTime(null));
  const prLive = validateInterval(pr?.times.createdAt || validateTime(null), live);
  const ci = validateCI(input.runs, pr, names.includes("pull_requests") || names.includes("ci") || input.collection.ciTruncatedBranches.includes(pr?.headRef));
  const adjustmentReasons = [];
  if (!isComplete(cut)) adjustmentReasons.push(reasonOf(cut));
  if (!isComplete(ci)) adjustmentReasons.push(reasonOf(ci));
  if (!ci.stall) adjustmentReasons.push("no complete CI stall");
  else if (!isComplete(ci.stall)) adjustmentReasons.push(reasonOf(ci.stall));
  else if (isComplete(cut) && (ci.stall.start.ms < cut.start.ms || ci.stall.end.ms > cut.end.ms)) adjustmentReasons.push("CI stall outside rollout window");
  const adjustment = { completeness: completeness(adjustmentReasons) };
  const gate = validateGate(rollout, input.statuses, run?.headSha || "", live, input.collection.statusTruncatedShas.includes(run?.headSha || "") || names.includes("statuses") || names.includes("workflow_runs"));
  const rollback = validateRollback(matches, pins.filter((item) => isRollbackTitle(item.title)), names.includes("pin_pull_requests"));
  const digestSteps = run?.jobs.flatMap((job) => job.steps).filter((step) => /^record pushed digest$/i.test(step.name)) || [];
  const digest = digestSteps.length === 1 ? validateInterval(run.times.createdAt, digestSteps[0].times.completedAt) : validateInterval(validateTime(null), validateTime(null));
  const digestReasons = [...digest.completeness.reasons];
  if (digestSteps.length > 1) digestReasons.push("ambiguous digest evidence");
  if (run?.jobsTruncated || names.includes("workflow_runs")) digestReasons.push("truncated");
  if (run && !isComplete(run)) digestReasons.push(...run.completeness.reasons);
  if (run && digestSteps[0]) digestReasons.push(...validateInterval(digestSteps[0].times.completedAt, run.times.updatedAt).completeness.reasons);
  if (digestSteps[0] && !isComplete(digestSteps[0])) digestReasons.push(...digestSteps[0].completeness.reasons);
  digest.completeness = completeness(digestReasons);
  const checks = pin?.checks || [];
  const checksCompleteness = { completeness: completeness(checks.length ? checks.flatMap((check) => check.completeness.reasons) : ["no check evidence"]) };
  const start = rollout?.cutAt ? rollout.times.cutAt : pr?.createdAt ? pr.times.createdAt : run?.times.createdAt || validateTime(null);
  return { version, start, run, rollout, pr, pin, pinReason, cut, prInterval, prLive, ci, adjustment, gate, rollback, digest, checksCompleteness };
}

export function validateEvidence(raw) {
  const input = normalizeInput(raw);
  input.collection = {
    truncated: asArray(input.collection?.truncated),
    ciTruncatedBranches: asArray(input.collection?.ciTruncatedBranches),
    statusTruncatedShas: asArray(input.collection?.statusTruncatedShas),
  };
  input.pullRequests = uniqueRecords(input.pullRequests, (pr) => pr.number).map((pr) => validateRecord(pr, ["createdAt", "mergedAt"], "createdAt", "mergedAt"));
  input.pinPullRequests = uniqueRecords(input.pinPullRequests, (pr) => pr.number).map((pr) => ({
    ...validateRecord(pr, ["createdAt", "mergedAt"], "createdAt", "mergedAt"),
    checks: pr.checks.map((check) => validateRecord(check, ["startedAt", "completedAt"], "startedAt", "completedAt")),
  }));
  input.runs = uniqueRecords(input.runs, (run) => run.id).map(validateRun);
  input.statuses = uniqueRecords(input.statuses, (status) => status.id).map((status) => {
    const times = { createdAt: validateTime(status.createdAt), updatedAt: validateTime(status.updatedAt) };
    const interval = status.createdAt ? validateInterval(times.createdAt, times.updatedAt) : validateInterval(times.updatedAt, times.updatedAt);
    return { ...status, times, interval, completeness: interval.completeness };
  });
  input.rollouts = uniqueRecords(input.rollouts, (rollout) => rollout.id).map((rollout) => {
    const record = validateRecord(rollout, ["cutAt", "liveAt", "rollbackStartedAt", "rollbackFinishedAt"], "cutAt", "liveAt");
    record.rollback = validateInterval(record.times.rollbackStartedAt, record.times.rollbackFinishedAt);
    record.tickets = rollout.tickets.map((ticket) => {
      const gateTime = validateTime(ticket.gateOkAt);
      return { ...ticket, gateTime, completeness: gateTime.completeness };
    });
    return record;
  });
  // Complete every association and cross-record interval before rowFor performs
  // any duration arithmetic. Consumers cannot bypass this stage with camelCase.
  input.releases = versionsOf(input).map((version) => validateRelease(input, version));
  return input;
}

function ciMetrics(evidence) {
  const empty = { failed_s: null, success_s: null, failure_and_fix_s: null, attempts: null, observed_attempts: evidence.observedAttempts, reason: reasonOf(evidence) };
  empty.reasons = {
    ...(!isComplete(evidence) || !evidence.failure ? { failed_s: reasonOf(evidence) || "no failed CI attempt in PR lifetime" } : {}),
    ...(!isComplete(evidence) || !evidence.success ? { success_s: reasonOf(evidence) || "no successful CI attempt in PR lifetime" } : {}),
    ...(!isComplete(evidence) || !evidence.stall ? { failure_and_fix_s: reasonOf(evidence) || "no complete CI stall" } : {}),
  };
  if (!isComplete(evidence)) return empty;
  return {
    ...empty,
    failed_s: duration(evidence.failure?.interval),
    success_s: duration(evidence.success?.interval),
    failure_and_fix_s: duration(evidence.stall),
    attempts: evidence.samples.length,
  };
}

function gateMetrics(evidence) {
  const samples = evidence.intervals.filter(isComplete);
  return {
    gate_ok_to_live_s: isComplete(evidence) ? median(samples.map(duration)) : null,
    gate_samples: samples.length,
    ...(evidence.missing ? { gate_missing: evidence.missing } : {}),
    ...(reasonOf(evidence) ? { gate_reason: reasonOf(evidence) } : {}),
  };
}

function longPole(pin, checksCompleteness) {
  if (!pin || !isComplete(checksCompleteness)) return null;
  const checks = pin.checks.map((check) => ({ name: check.name, s: duration(check.interval) })).sort((a, b) => b.s - a.s);
  return checks[0] || null;
}

function versionsOf(input) {
  const versions = new Set();
  for (const run of input.runs) {
    if (run.workflow === "Release") {
      const version = versionIn(run.headBranch);
      if (version) versions.add(version);
    }
  }
  for (const rollout of input.rollouts) if (rollout.version) versions.add(rollout.version);
  for (const pin of input.pinPullRequests) {
    const version = versionIn(pin.title);
    if (version) versions.add(version);
  }
  for (const pr of input.pullRequests) {
    const version = versionIn(pr.title);
    if (version) versions.add(version);
  }
  return [...versions];
}

function explain(row) {
  const notes = [];
  if (row.label === "12" && row.elapsed_s === 7648 && row.wall_min === 128) {
    notes.push("Elapsed 7648s (127 min 28 s) from 2026-09-29T17:59:39Z to 2026-09-29T20:07:07Z. §1's 128 min is the truncated UTC-minute span 17:59→20:07.");
  }
  if (row.label === "12" && row.ci.success_s === 644) {
    notes.push("PR CI is 644s: 10.7 min to one decimal, and 11 min to the nearest minute, which is §1's PR CI figure for release 12.");
  }
  if (row.label === "12b" && row.ci.success_s === 557 && row.pull_request?.pr_to_merge_s === 686) {
    notes.push("Pull-request CI is 557s (9.3 min). PR→merge is 686s (11 min 26 s). §1's 9.3 min is the CI run, not PR→merge.");
  }
  if (row.label === "12b" && row.pr_open_to_live_s === 2242 && row.pr_open_to_live_wall_min === 38) {
    notes.push("Hotfix PR open 2026-09-29T20:31:39Z → live is 2242s (37 min 22 s). Truncated minutes 20:31→21:09 = 38, the same minute as the progress start 2026-09-29T20:31:14Z, which is §1's 38 min.");
  }
  if (row.label === "12c" && row.ci.failure_and_fix_s === 2241) {
    notes.push("Failure-and-fix is 2241s (37 min 21 s), from the failed PR CI created_at to the next successful PR CI created_at. §1's 37 min matches the nearest minute.");
  }
  if (row.label === "12c" && row.ci.cut_to_live_without_failure_s === 2768) {
    notes.push('Elapsed 5009s (83 min 29 s) minus that stall is 2768s (46 min 8 s). §1\'s "~47" is 84−37 on the displayed minute counts, not this remainder.');
  }
  if (row.label === "12d" && row.elapsed_s === 3477 && row.wall_min === 58) {
    notes.push("Elapsed 3477s (57 min 57 s) from 2026-09-29T23:20:59Z to 2026-09-30T00:18:56Z. Truncated minutes 23:20→00:18 = 58. §1 also prints nearest-minute labels 23:21→00:19, which is the same 58 min.");
  } else if (row.label === "12d" && row.elapsed_s == null) {
    notes.push("§1's 58 min is the progress-log span 2026-09-29T23:20:59Z→2026-09-30T00:18:56Z. This run has no rollout cut_at/live_at, so cut→live is not taken from the pull request or the pin merge.");
  }
  if (row.label === "12d" && row.ci.failed_s === 642 && row.ci.success_s === 666 && row.ci.failure_and_fix_s === 679) {
    notes.push("Failed PR CI 642s = 10.7 min. Successful PR CI 666s = 11.1 min. Failure-and-fix 679s = 11 min 19 s, which §1 counts as 11 min.");
  }
  if (row.label === "12d" && row.ci.cut_to_live_without_failure_s === 2798) {
    notes.push("Cut→live without that 679s stall is 2798s (46 min 38 s), which rounds to §1's 47 min.");
  }
  if (row.label === "12d" && row.digest_after_tag_s === 641) {
    notes.push("Digest step completed 641s after the tag workflow started, which is 10 min 41 s and §1's 10.7 min.");
  }
  if (row.label === "12d" && row.pin?.long_pole?.s === 559) {
    notes.push("Pharos fleet release compatibility is 559s (9 min 19 s). §1's table says 9.4 min for the progress-log span; the same paragraph records the check as 9m19s.");
  }
  if (row.elapsed_s != null && row.wall_min != null && !notes.some((note) => note.startsWith("Elapsed"))) {
    notes.push(`Elapsed ${row.elapsed_s}s (${fmtMinSec(row.elapsed_s)}); truncated UTC-minute span is ${row.wall_min} min.`);
  }
  return notes;
}

const PUBLISHED_WALL = { "12": 128, "12b": 38, "12c": 84, "12d": 58 };

function section1(row) {
  const published = PUBLISHED_WALL[row.label];
  if (published == null) return null;
  return {
    published_wall_min: published,
    wall_match: row.wall_min === published,
  };
}

function isRollbackTitle(title) {
  return /rollback/i.test(title || "");
}

function rowFor(evidence) {
  const { version, run, rollout, pr: pullRequest, pin, pinReason, cut, prInterval, prLive, ci: ciEvidence, adjustment, gate: gateEvidence, rollback, digest, checksCompleteness } = evidence;
  const label = rollout?.release || labelFromRef(pullRequest?.headRef) || null;
  const cutAt = rollout?.cutAt || null;
  const liveAt = rollout?.liveAt || null;
  const elapsed = duration(cut);
  const ci = ciMetrics(ciEvidence);
  const without = isComplete(adjustment) ? elapsed - ci.failure_and_fix_s : null;
  const ciBody = {
    failed_s: ci.failed_s,
    success_s: ci.success_s,
    failure_and_fix_s: ci.failure_and_fix_s,
    cut_to_live_without_failure_s: without,
    cut_to_live_without_failure_min: roundMin(without),
    observed_attempts: ci.observed_attempts,
    completeness: ciEvidence.completeness,
    reasons: ci.reasons,
  };
  if (ci.attempts != null) ciBody.attempts = ci.attempts;
  if (ci.reason) ciBody.reason = ci.reason;
  if (reasonOf(adjustment)) ciBody.adjustment_reason = reasonOf(adjustment);
  const reasons = {};
  for (const [name, interval] of Object.entries({ cut_to_live: cut, pr_to_merge: prInterval, pr_open_to_live: prLive })) {
    if (reasonOf(interval)) reasons[name] = reasonOf(interval);
  }
  const row = {
    label,
    version,
    sequence: rollout?.sequence ?? null,
    cut_at: cutAt,
    live_at: liveAt,
    cut_source: cutAt ? "rollout" : null,
    live_source: liveAt ? "rollout" : null,
    elapsed_s: elapsed,
    wall_min: minuteSpan(cut),
    cut_to_live_s: elapsed,
    tag_at: run?.createdAt || null,
    pull_request: pullRequest ? {
      number: pullRequest.number,
      created_at: pullRequest.createdAt,
      merged_at: pullRequest.mergedAt,
      head: pullRequest.headRef,
      pr_to_merge_s: duration(prInterval),
      completeness: pullRequest.completeness,
    } : null,
    pr_to_merge_s: duration(prInterval),
    pr_open_to_live_s: duration(prLive),
    pr_open_to_live_wall_min: minuteSpan(prLive),
    ci: ciBody,
    digest_after_tag_s: duration(digest),
    pin: pin ? {
      number: pin.number,
      created_at: pin.createdAt,
      merged_at: pin.mergedAt,
      open_to_merge_s: duration(pin.interval),
      long_pole: longPole(pin, checksCompleteness),
      completeness: pin.completeness,
      ...(reasonOf(pin.interval) ? { reason: reasonOf(pin.interval) } : {}),
      ...(reasonOf(checksCompleteness) ? { long_pole_reason: reasonOf(checksCompleteness) } : {}),
    } : null,
    ...gateMetrics(gateEvidence),
    rollback_s: isComplete(rollback) ? duration(rollback.interval) : null,
    rollback_source: isComplete(rollback) ? rollback.source : null,
    evidence: {
      cut_to_live: cut.completeness,
      pr_to_merge: prInterval.completeness,
      pr_open_to_live: prLive.completeness,
      ci: ciEvidence.completeness,
      adjustment: adjustment.completeness,
      gate: gateEvidence.completeness,
      rollback: rollback.completeness,
      digest: digest.completeness,
    },
  };
  if (pinReason) row.pin_reason = pinReason;
  if (reasonOf(rollback)) row.rollback_reason = reasonOf(rollback);
  if (reasonOf(digest)) row.digest_reason = reasonOf(digest);
  if (Object.keys(reasons).length) row.reasons = reasons;
  row.notes = explain(row);
  row.section1 = section1(row);
  return row;
}

function gapText(rows) {
  const gaps = [];
  if (rows.length && rows.every((row) => row.gate_ok_to_live_s == null)) {
    let text = "gate-ok → live is empty: no successful gate/cross-family status and no rollout gate_ok_at. §1's 3-5 h median is the published estimate for 12c/12d. Those verdicts were ticket comments, and this issue API returns untimed comment strings, so the median is not recomputed.";
    if (rows.some((row) => row.gate_reason === "truncated")) {
      text = "gate-ok → live is empty because the status list was truncated. An incomplete collection is not evidence that the gate succeeded or failed.";
    } else if (rows.some((row) => row.gate_reason === "incomplete")) {
      text = "gate-ok → live is incomplete: a rollout ticket or successful status has a missing or invalid timestamp, so no median is taken from that partial sample.";
    }
    gaps.push({ id: "gate-ok", text });
  }
  if (rows.length && rows.every((row) => row.rollback_s == null)) {
    const pins = rows.map((row) => row.pin?.open_to_merge_s).filter((value) => value != null);
    let measured = "No forward pin PR was matched";
    if (pins.length === 1) measured = `The matched forward pin PR merged in ${pins[0]}s`;
    else if (pins.length > 1) measured = `Forward pin PRs in this report merged in ${Math.min(...pins)}-${Math.max(...pins)}s`;
    let text = `rollback is empty: no rollout rollback interval and no pin PR marked as a rollback. §1's ≈15 min is an estimate for a rollback pin PR, not an observed rollback (the published failure count was 0 of 12). ${measured}.`;
    if (rows.some((row) => row.rollback_reason === "truncated")) {
      text = "rollback is empty because the pin list was truncated. An incomplete collection is not an observed absence.";
    } else if (rows.some((row) => row.rollback_reason === "ambiguous")) {
      text = "rollback is ambiguous: the rollout interval and the rollback pin PR disagree, so neither duration is reported.";
    } else if (rows.some((row) => row.rollback_reason && row.rollback_reason !== "no rollback evidence")) {
      text = "rollback is unknown: the newest attempt has an incomplete or invalid interval; no older duration is substituted.";
    }
    gaps.push({ id: "rollback", text });
  }
  return gaps;
}

function matchesRelease(row, filter) {
  if (!filter) return true;
  const want = String(filter).replace(/^v/, "");
  const fields = [row.label, row.version, row.sequence != null ? String(row.sequence) : null];
  if (row.sequence != null) fields.push(`stable${row.sequence}`);
  return fields.some((field) => field != null && String(field) === want);
}

export function buildReport(input, opts = {}) {
  const source = validateEvidence(input);
  let evidence = source.releases;
  if (opts.since) {
    const since = parseTime(opts.since, "--since");
    evidence = evidence.filter((item) => isComplete(item.start) && item.start.ms >= since);
  }
  evidence.sort((a, b) => (a.start.ms ?? 0) - (b.start.ms ?? 0) || a.version.localeCompare(b.version));
  let rows = evidence.map(rowFor).filter((row) => row.label || row.version);
  if (opts.release) rows = rows.filter((row) => matchesRelease(row, opts.release));
  const now = opts.now instanceof Date ? opts.now : new Date(opts.now || Date.now());
  const truncated = Array.isArray(source.collection?.truncated) ? [...source.collection.truncated] : [];
  return {
    schema: "aeon.release-timing.v1",
    generated_at: now.toISOString(),
    filters: { since: opts.since || null, release: opts.release || null },
    releases: rows,
    gaps: gapText(rows),
    collection: { truncated },
  };
}

function pad(value, width) {
  const text = value == null || value === "" ? "-" : String(value);
  return text.length >= width ? text : text + " ".repeat(width - text.length);
}

export function formatTable(report) {
  const header = ["release", "version", "cut→live", "pr→merge", "gate→live", "rollback"];
  const body = report.releases.map((row) => [
    row.label || "-",
    row.version,
    row.wall_min == null ? "-" : `${row.wall_min} min`,
    fmt(row.pr_to_merge_s),
    fmt(row.gate_ok_to_live_s),
    fmt(row.rollback_s),
  ]);
  const widths = header.map((name, index) => Math.max(name.length, ...body.map((line) => line[index].length)));
  const paint = (cells) => cells.map((cell, index) => pad(cell, widths[index])).join("  ");
  const lines = [paint(header), ...body.map(paint)];
  if (report.releases.some((row) => row.digest_after_tag_s != null || row.ci.success_s != null || row.pin)) {
    lines.push("");
    lines.push("release  digest   ci-fail  ci-ok    stall    pin      long-pole");
    for (const row of report.releases) {
      const pole = row.pin?.long_pole ? `${fmt(row.pin.long_pole.s)} ${row.pin.long_pole.name}` : "-";
      lines.push([
        pad(row.label || "-", 8),
        pad(fmt(row.digest_after_tag_s), 8),
        pad(fmt(row.ci.failed_s), 8),
        pad(fmt(row.ci.success_s), 8),
        pad(fmt(row.ci.failure_and_fix_s), 8),
        pad(fmt(row.pin?.open_to_merge_s), 8),
        pole,
      ].join(" "));
    }
  }
  for (const row of report.releases) {
    for (const note of row.notes) lines.push(`${row.label}: ${note}`);
    for (const [metric, evidence] of Object.entries(row.evidence)) {
      if (evidence.state === "partial") lines.push(`${row.label || row.version}: ${metric} unknown (${evidence.reasons.join("; ")}).`);
    }
  }
  for (const gap of report.gaps) lines.push(gap.text);
  return lines.join("\n");
}

export function readRollouts(path) {
  const info = statSync(path);
  const files = info.isDirectory()
    ? readdirSync(path).filter((name) => name.endsWith(".json") && !name.startsWith(".")).map((name) => join(path, name))
    : [path];
  const rollouts = [];
  for (const file of files) {
    const name = basename(file);
    if (name === ".env" || name.endsWith(".key") || name.endsWith(".age")) throw new Error("refusing to read secrets");
    rollouts.push(...rolloutsFrom(JSON.parse(readFileSync(file, "utf8")), file));
  }
  return rollouts;
}

function rolloutsFrom(json, file) {
  if (Array.isArray(json)) return json;
  if (json && Array.isArray(json.rollouts)) return json.rollouts;
  if (json && json.version && (json.cut_at || json.cutAt || json.live_at || json.liveAt || json.rollback_started_at || json.rollbackStartedAt || json.rollback_finished_at || json.rollbackFinishedAt)) return [json];
  throw new Error(`rollout file has no rollouts: ${basename(file)}`);
}

const DEFAULT_PAGE_SIZE = 100;
const DEFAULT_LIST_CAP = 1000;

function unpackPage(body, keys) {
  if (Array.isArray(body)) return { items: body, total: null, incomplete: false };
  if (!body || typeof body !== "object") return { items: [], total: null, incomplete: true };
  const total = Number.isFinite(body.total_count) ? body.total_count : null;
  for (const key of keys) {
    if (Array.isArray(body[key])) return { items: body[key], total, incomplete: Boolean(body.incomplete_results) };
  }
  return { items: [], total, incomplete: true };
}

function collectPages(gh, operation, params, keys, pageSize, listCap) {
  const items = [];
  const seen = new Set();
  let page = 1;
  let total = null;
  let complete = false;
  let incomplete = false;
  let scanned = 0;
  // Keep the remote width fixed. Bound scanned rows (including duplicates) so
  // an unstable endpoint cannot loop forever or silently exhaust the local cap.
  while (scanned < listCap && items.length < listCap) {
    const body = gh(ghRead(operation, { ...params, page, pageSize }));
    const unpacked = unpackPage(body, keys);
    incomplete ||= unpacked.incomplete;
    if (unpacked.total != null) total = unpacked.total;
    if (unpacked.items.length === 0) {
      complete = true;
      break;
    }
    scanned += unpacked.items.length;
    let omitted = false;
    for (const item of unpacked.items) {
      const id = item.id ?? item.databaseId ?? item.number ?? null;
      if (id != null && seen.has(String(id))) continue;
      if (id != null) seen.add(String(id));
      if (items.length < listCap) items.push(item);
      else omitted = true;
    }
    incomplete ||= omitted;
    if (total != null && scanned >= total) {
      complete = true;
      break;
    }
    if (total == null && unpacked.items.length < pageSize) {
      complete = true;
      break;
    }
    page += 1;
  }
  return { items, truncated: incomplete || !complete || (total != null && items.length < total) };
}

function noteTruncation(names, name, truncated) {
  if (truncated && !names.includes(name)) names.push(name);
}

function rawVersion(run) {
  return versionIn(run.headBranch || run.head_branch);
}

function rawSha(run) {
  return run.headSha || run.head_sha || "";
}

function rawMerge(pr) {
  return pr.mergeCommit?.oid || pr.merge_commit_sha || pr.mergeSha || pr.merge_sha || null;
}

function rawBranch(pr) {
  return pr.headRefName || pr.head?.ref || pr.head_ref || "";
}

function withAttempts(gh, repo, run) {
  const count = Number(run.run_attempt ?? run.runAttempt ?? 1);
  if (!Number.isSafeInteger(count) || count <= 1 || Array.isArray(run.attempts)) return run;
  const id = run.databaseId ?? run.id;
  const attempts = [];
  for (let n = 1; n <= count; n++) {
    attempts.push(gh(ghRead("attempt", { repo, id, attempt: n })));
  }
  return { ...run, attempts };
}

export function fetchInputs(opts) {
  const gh = (args) => {
    assertReadOnly(args);
    return opts.gh(args);
  };
  const repo = opts.repo || "inspr-at/paimos";
  const pinRepo = opts.pinRepo || "markus-barta/nixcfg";
  const pageSize = opts.pageSize ?? DEFAULT_PAGE_SIZE;
  const listCap = opts.listCap ?? DEFAULT_LIST_CAP;
  if (!Number.isSafeInteger(pageSize) || pageSize < 1 || pageSize > 100 || !Number.isSafeInteger(listCap) || listCap < 1) throw new Error("invalid pagination bounds");
  const truncated = [];
  const releasePages = collectPages(gh, "release-runs", { repo }, ["workflow_runs"], pageSize, listCap);
  noteTruncation(truncated, "workflow_runs", releasePages.truncated);
  const runs = releasePages.items.map((run) => ({
    ...run,
    workflowName: run.workflowName || run.workflow || "Release",
  }));
  const pullPages = collectPages(
    gh,
    "pulls", { repo },
    ["items"],
    pageSize,
    listCap,
  );
  noteTruncation(truncated, "pull_requests", pullPages.truncated);
  const pullRequests = pullPages.items.filter((pr) => at(pr, "mergedAt", "merged_at"));
  const pinPages = collectPages(gh, "pin-search", { repo: pinRepo }, ["items"], pageSize, listCap);
  noteTruncation(truncated, "pin_pull_requests", pinPages.truncated);
  const pins = pinPages.items;
  const listed = {
    workflow_runs: runs,
    pull_requests: pullRequests,
    pin_pull_requests: pins,
    statuses: [],
    rollouts: opts.rollouts || [],
    collection: { truncated },
  };
  const wanted = new Set(buildReport(listed, opts).releases.map((row) => row.version));
  const enrichedRuns = runs.map((run) => {
    if (!wanted.has(rawVersion(run))) return run;
    const id = run.databaseId ?? run.id;
    const jobPages = collectPages(gh, "jobs", { repo, id }, ["jobs"], pageSize, listCap);
    noteTruncation(truncated, "jobs", jobPages.truncated);
    return { ...run, jobs: jobPages.items, jobsTruncated: jobPages.truncated };
  });
  const branches = new Set();
  const shas = new Set();
  for (const run of enrichedRuns) {
    if (!wanted.has(rawVersion(run))) continue;
    const sha = rawSha(run);
    if (sha) shas.add(sha);
    const pr = pullRequests.find((item) => rawMerge(item) && rawMerge(item) === sha);
    const branch = pr ? rawBranch(pr) : "";
    if (branch) branches.add(branch);
  }
  const ciRuns = [];
  const ciTruncatedBranches = [];
  for (const branch of branches) {
    const ciPages = collectPages(
      gh,
      "ci-runs", { repo, branch },
      ["workflow_runs"],
      pageSize,
      listCap,
    );
    noteTruncation(truncated, "ci", ciPages.truncated);
    if (ciPages.truncated) ciTruncatedBranches.push(branch);
    for (const run of ciPages.items) {
      const tagged = { ...run, workflowName: "CI" };
      ciRuns.push(ciPages.truncated ? tagged : withAttempts(gh, repo, tagged));
    }
  }
  const statuses = [];
  const statusTruncatedShas = [];
  for (const sha of shas) {
    const statusPages = collectPages(gh, "statuses", { repo, sha }, ["statuses"], pageSize, listCap);
    noteTruncation(truncated, "statuses", statusPages.truncated);
    if (statusPages.truncated) statusTruncatedShas.push(sha);
    for (const status of statusPages.items) statuses.push({ ...status, sha });
  }
  const pinDetails = pins.map((pin) => {
    if (!wanted.has(versionIn(pin.title))) return pin;
    return gh(ghRead("pin-view", { repo: pinRepo, number: pin.number }));
  });
  return {
    workflow_runs: [...enrichedRuns, ...ciRuns],
    pull_requests: pullRequests,
    pin_pull_requests: pinDetails,
    statuses,
    rollouts: opts.rollouts || [],
    collection: { truncated, ciTruncatedBranches, statusTruncatedShas },
  };
}

export function parseArgs(argv) {
  const opts = {
    since: null,
    release: null,
    repo: "inspr-at/paimos",
    pinRepo: "markus-barta/nixcfg",
    rollout: null,
    fixture: null,
    json: false,
    help: false,
  };
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    const next = () => {
      const value = argv[++i];
      if (!value || value.startsWith("--")) throw new Error(`missing value for ${arg}`);
      return value;
    };
    if (arg === "--json") opts.json = true;
    else if (arg === "--help" || arg === "-h") opts.help = true;
    else if (arg === "--since") opts.since = next();
    else if (arg === "--release") opts.release = next();
    else if (arg === "--repo") opts.repo = next();
    else if (arg === "--pin-repo") opts.pinRepo = next();
    else if (arg === "--rollout") opts.rollout = next();
    else if (arg === "--fixture") opts.fixture = next();
    else throw new Error(`unknown argument ${arg}`);
  }
  return opts;
}

function usage() {
  return `usage: node scripts/release-timing.mjs [--since ISO] [--release LABEL|VERSION|SEQUENCE] [--rollout PATH] [--fixture PATH] [--json]
Reads GitHub through fixed GET API templates and one PR-view template, plus rollout JSON. Lists use a fixed page width and de-duplicate ids. Truncation and incomplete search results are named in collection.truncated. Timing evidence is validated before arithmetic; partial records report unknown with reasons. --fixture runs from a recorded bundle and does not call gh.`;
}

export function reportFromArgs(argv, gh = defaultGh) {
  const opts = parseArgs(argv);
  if (opts.help) return { help: usage(), report: null };
  const extra = opts.rollout ? readRollouts(opts.rollout) : [];
  const raw = opts.fixture
    ? JSON.parse(readFileSync(opts.fixture, "utf8"))
    : fetchInputs({ ...opts, gh, rollouts: extra });
  if (opts.fixture) raw.rollouts = [...asArray(raw.rollouts), ...extra];
  const report = buildReport(raw, { since: opts.since, release: opts.release, now: new Date() });
  report.sources = {
    fixture: Boolean(opts.fixture),
    github: !opts.fixture,
    repo: opts.repo,
    pin_repo: opts.pinRepo,
    rollouts: asArray(raw.rollouts).length,
  };
  return { help: null, report };
}

function main(argv) {
  const { help, report } = reportFromArgs(argv);
  if (help) {
    console.log(help);
    return;
  }
  if (parseArgs(argv).json) console.log(JSON.stringify(report, null, 2));
  else console.log(`${formatTable(report)}\n---\n${JSON.stringify(report, null, 2)}`);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    main(process.argv.slice(2));
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
