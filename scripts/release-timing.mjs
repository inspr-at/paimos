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
// -X/--method other than GET, --field/-f/-F, and --input are refused before gh runs.
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
const READ_ONLY = new Set(["pr list", "pr view", "run list", "run view", "api"]);

function ghApiMethod(args) {
  let method = "GET";
  for (let i = 0; i < args.length; i++) {
    const arg = args[i];
    if (arg === "--method" || arg === "-X") {
      method = String(args[i + 1] || "").toUpperCase();
      i++;
    } else if (arg.startsWith("--method=")) {
      method = arg.slice("--method=".length).toUpperCase();
    } else if (arg.startsWith("-X") && arg.length > 2) {
      method = arg.slice(2).toUpperCase();
    }
  }
  return method;
}

function ghApiWrites(args) {
  return args.some((arg) => arg === "-f" || arg === "-F" || arg === "--input" || arg === "--field" || arg === "--raw-field"
    || arg.startsWith("--input=") || arg.startsWith("--field=") || arg.startsWith("--raw-field="));
}

export function assertReadOnly(args) {
  if (!Array.isArray(args) || args.length < 1) throw new Error("empty gh command");
  const head = args[0] === "pr" || args[0] === "run" ? `${args[0]} ${args[1]}` : args[0];
  if (!READ_ONLY.has(head)) throw new Error(`refusing gh ${args.slice(0, 2).join(" ")}`);
  if (args[0] !== "api") return;
  if (ghApiMethod(args) !== "GET") throw new Error("refusing non-GET gh api");
  if (ghApiWrites(args)) throw new Error("refusing gh api write fields");
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
  if (value == null || value === "") return { ms: null, reason: null };
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
  if (start == null || start === "" || end == null || end === "") return { seconds: null, reason: null };
  const a = instant(start);
  const b = instant(end);
  if (a.reason === "missing timezone" || b.reason === "missing timezone") return { seconds: null, reason: "missing timezone" };
  if (a.ms == null || b.ms == null) return { seconds: null, reason: a.reason || b.reason };
  if (b.ms < a.ms) return { seconds: null, reason: "reversed" };
  return { seconds: Math.round((b.ms - a.ms) / 1000), reason: null };
}

export function secondsBetween(start, end) {
  return span(start, end).seconds;
}

// §1's published walls are the difference of the timestamps with seconds dropped.
export function wallMin(start, end) {
  const timed = span(start, end);
  if (timed.seconds == null) return null;
  return Math.floor(instant(end).ms / 60000) - Math.floor(instant(start).ms / 60000);
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
    runs: asArray(raw.workflow_runs ?? raw.workflowRuns).map(normalizeRun),
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
    runAttempt: Number.isFinite(attempt) && attempt > 0 ? attempt : 1,
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

function chooseReleaseRun(runs, version) {
  const tagged = runs.filter((run) => run.workflow === "Release" && versionIn(run.headBranch) === version);
  const pushed = tagged.filter((run) => run.conclusion === "success" && run.event === "push");
  const pool = pushed.length ? pushed : tagged;
  pool.sort((a, b) => Date.parse(a.createdAt) - Date.parse(b.createdAt));
  return pool[0] || null;
}

function digestSeconds(run) {
  if (!run) return null;
  for (const job of run.jobs) {
    for (const step of job.steps) {
      if (/^record pushed digest$/i.test(step.name) && step.completedAt) {
        return secondsBetween(run.createdAt, step.completedAt);
      }
    }
  }
  return null;
}

function longPole(checks) {
  let best = null;
  for (const check of checks) {
    const duration = secondsBetween(check.startedAt, check.completedAt);
    if (duration == null) continue;
    if (!best || duration > best.s) best = { name: check.name, s: duration };
  }
  return best;
}

function attemptIntervals(run) {
  if (run.attempts.length) {
    return run.attempts.map((attempt) => ({
      conclusion: attempt.conclusion,
      start: attempt.startedAt,
      end: attempt.completedAt,
    }));
  }
  if (run.runAttempt > 1 && !run.runStartedAt) return { unavailable: true };
  return [{
    conclusion: run.conclusion,
    start: run.runAttempt > 1 ? run.runStartedAt : run.createdAt,
    end: run.updatedAt,
  }];
}

function overlapsWindow(start, end, opened, merged) {
  if (opened != null && (start == null || start < opened)) return false;
  if (merged != null && (end == null || end > merged)) return false;
  return start != null && end != null;
}

function ciMetrics(runs, branch, openedAt, mergedAt, opts = {}) {
  const empty = { failed_s: null, success_s: null, failure_and_fix_s: null, attempts: null, reason: null };
  if (!branch) return empty;
  if (opts.truncated) return { ...empty, reason: "truncated" };
  const opened = openedAt ? instant(openedAt).ms : null;
  const merged = mergedAt ? instant(mergedAt).ms : null;
  const rows = runs.filter((run) => run.workflow === "CI" && run.event === "pull_request" && run.headBranch === branch);
  const samples = [];
  let unavailable = false;
  for (const run of rows) {
    const attempts = attemptIntervals(run);
    if (attempts.unavailable) {
      const created = instant(run.createdAt).ms;
      const updated = instant(run.updatedAt).ms;
      const couldMatter = (merged == null || (created != null && created <= merged))
        && (opened == null || (updated != null && updated >= opened));
      if (couldMatter) unavailable = true;
      continue;
    }
    for (const attempt of attempts) {
      const timed = span(attempt.start, attempt.end);
      if (timed.seconds == null) continue;
      const startMs = instant(attempt.start).ms;
      const endMs = instant(attempt.end).ms;
      if (!overlapsWindow(startMs, endMs, opened, merged)) continue;
      samples.push({
        conclusion: attempt.conclusion,
        start: attempt.start,
        startMs,
        seconds: timed.seconds,
      });
    }
  }
  if (unavailable) return { ...empty, reason: "attempt history unavailable" };
  samples.sort((a, b) => a.startMs - b.startMs);
  const failure = samples.find((item) => item.conclusion === "failure");
  const success = failure
    ? samples.find((item) => item.conclusion === "success" && item.startMs >= failure.startMs)
    : [...samples].reverse().find((item) => item.conclusion === "success");
  return {
    failed_s: failure ? failure.seconds : null,
    success_s: success ? success.seconds : null,
    failure_and_fix_s: failure && success ? span(failure.start, success.start).seconds : null,
    attempts: samples.length || null,
    reason: null,
  };
}

function gateResult(seconds, samples, missing, reason) {
  const result = { gate_ok_to_live_s: seconds, gate_samples: samples };
  if (missing) result.gate_missing = missing;
  if (reason) result.gate_reason = reason;
  return result;
}

function gateSeconds(liveAt, rollout, statuses, sha, opts = {}) {
  if (!liveAt) return gateResult(null, 0, 0, null);
  const tickets = rollout?.tickets || [];
  if (tickets.length) {
    const samples = [];
    let missing = 0;
    for (const ticket of tickets) {
      if (!ticket.gateOkAt) {
        missing++;
        continue;
      }
      const timed = span(ticket.gateOkAt, liveAt);
      if (timed.seconds == null) {
        missing++;
        continue;
      }
      samples.push(timed.seconds);
    }
    if (missing > 0) return gateResult(null, samples.length, missing, "incomplete");
    return gateResult(median(samples), samples.length, 0, null);
  }
  if (opts.statusesTruncated) return gateResult(null, 0, 0, "truncated");
  const samples = [];
  for (const status of statuses) {
    if (status.sha !== sha) continue;
    if (status.context !== "gate/cross-family" || status.state !== "success" || !status.updatedAt) continue;
    const timed = span(status.updatedAt, liveAt);
    if (timed.seconds == null) continue;
    samples.push(timed.seconds);
  }
  return gateResult(median(samples), samples.length, 0, null);
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

function releaseStart(row) {
  return row.cut_at || row.pull_request?.created_at || row.tag_at || null;
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

function latestBy(items, stampOf) {
  let best = null;
  let bestMs = null;
  items.forEach((item, index) => {
    const ms = instant(stampOf(item)).ms;
    const later = !best || index >= best.index;
    if (!best || (ms != null && (bestMs == null || ms > bestMs || (ms === bestMs && later))) || (ms == null && bestMs == null && later)) {
      best = { item, index };
      bestMs = ms;
    }
  });
  return best ? best.item : null;
}

function mergeRollouts(matches) {
  if (!matches.length) return null;
  const merged = {
    release: null,
    version: matches[0].version,
    sequence: null,
    cutAt: null,
    liveAt: null,
    rollbackStartedAt: null,
    rollbackFinishedAt: null,
    tickets: [],
  };
  for (const item of matches) {
    if (item.release) merged.release = item.release;
    if (item.sequence != null) merged.sequence = item.sequence;
    if (item.cutAt) merged.cutAt = item.cutAt;
    if (item.liveAt) merged.liveAt = item.liveAt;
    if (item.tickets.length) merged.tickets = item.tickets;
  }
  const rollback = latestBy(
    matches.filter((item) => item.rollbackStartedAt && item.rollbackFinishedAt),
    (item) => item.rollbackStartedAt,
  );
  if (rollback) {
    merged.rollbackStartedAt = rollback.rollbackStartedAt;
    merged.rollbackFinishedAt = rollback.rollbackFinishedAt;
  }
  return merged;
}

function rollbackOf(rollout, rollbackPins, pinTruncated) {
  const measured = Boolean(rollout?.rollbackStartedAt && rollout?.rollbackFinishedAt);
  const rolloutSpan = measured ? span(rollout.rollbackStartedAt, rollout.rollbackFinishedAt) : null;
  if (pinTruncated) {
    if (rolloutSpan?.seconds != null && rollbackPins.length === 0) {
      return { rollback_s: rolloutSpan.seconds, rollback_source: "rollout" };
    }
    return { rollback_s: null, rollback_source: null, rollback_reason: "truncated" };
  }
  const rollbackPin = latestBy(rollbackPins, (pr) => pr.createdAt || pr.mergedAt);
  const pinSpan = rollbackPin ? span(rollbackPin.createdAt, rollbackPin.mergedAt) : null;
  if (measured && rollbackPin) {
    if (rolloutSpan.seconds != null && pinSpan.seconds != null && rolloutSpan.seconds === pinSpan.seconds) {
      return { rollback_s: rolloutSpan.seconds, rollback_source: "rollout" };
    }
    return { rollback_s: null, rollback_source: null, rollback_reason: "ambiguous" };
  }
  if (rolloutSpan?.seconds != null) return { rollback_s: rolloutSpan.seconds, rollback_source: "rollout" };
  if (measured && rolloutSpan?.reason) return { rollback_s: null, rollback_source: null, rollback_reason: rolloutSpan.reason };
  if (pinSpan?.seconds != null) return { rollback_s: pinSpan.seconds, rollback_source: "pin" };
  if (rollbackPin && pinSpan?.reason) return { rollback_s: null, rollback_source: null, rollback_reason: pinSpan.reason };
  return { rollback_s: null, rollback_source: null };
}

function rowFor(input, version) {
  const run = chooseReleaseRun(input.runs, version);
  const rollout = mergeRollouts(input.rollouts.filter((item) => item.version === version));
  const pullRequest = input.pullRequests.find((pr) => run && pr.mergeSha && pr.mergeSha === run.headSha)
    || input.pullRequests.find((pr) => versionIn(pr.title) === version)
    || null;
  const versionPins = input.pinPullRequests.filter((pr) => versionIn(pr.title) === version);
  const forwardPins = versionPins.filter((pr) => !isRollbackTitle(pr.title));
  const rollbackPins = versionPins.filter((pr) => isRollbackTitle(pr.title));
  const pin = forwardPins.length === 1 ? forwardPins[0] : null;
  const pinReason = forwardPins.length > 1 ? "ambiguous" : null;
  const label = rollout?.release || labelFromRef(pullRequest?.headRef) || null;
  const cutAt = rollout?.cutAt || null;
  const liveAt = rollout?.liveAt || null;
  const cutSpan = span(cutAt, liveAt);
  const elapsed = cutSpan.seconds;
  const names = Array.isArray(input.collection?.truncated) ? input.collection.truncated : [];
  const ciTruncated = (input.collection?.ciTruncatedBranches || []).includes(pullRequest?.headRef);
  const ci = ciMetrics(input.runs, pullRequest?.headRef, pullRequest?.createdAt, pullRequest?.mergedAt, { truncated: ciTruncated });
  const without = elapsed != null && ci.failure_and_fix_s != null ? elapsed - ci.failure_and_fix_s : null;
  const statusesTruncated = (input.collection?.statusTruncatedShas || []).includes(run?.headSha || "");
  const gate = gateSeconds(liveAt, rollout, input.statuses, run?.headSha || "", { statusesTruncated });
  const rollback = rollbackOf(rollout, rollbackPins, names.includes("pin_pull_requests"));
  const digestReason = run?.jobsTruncated ? "truncated" : null;
  const prOpenToLive = secondsBetween(pullRequest?.createdAt, liveAt);
  const ciBody = {
    failed_s: ci.failed_s,
    success_s: ci.success_s,
    failure_and_fix_s: ci.failure_and_fix_s,
    cut_to_live_without_failure_s: without,
    cut_to_live_without_failure_min: roundMin(without),
  };
  if (ci.attempts != null) ciBody.attempts = ci.attempts;
  if (ci.reason) ciBody.reason = ci.reason;
  const row = {
    label,
    version,
    sequence: rollout?.sequence ?? null,
    cut_at: cutAt,
    live_at: liveAt,
    cut_source: cutAt ? "rollout" : null,
    live_source: liveAt ? "rollout" : null,
    elapsed_s: elapsed,
    wall_min: wallMin(cutAt, liveAt),
    cut_to_live_s: elapsed,
    tag_at: run?.createdAt || null,
    pull_request: pullRequest ? {
      number: pullRequest.number,
      created_at: pullRequest.createdAt,
      merged_at: pullRequest.mergedAt,
      head: pullRequest.headRef,
      pr_to_merge_s: secondsBetween(pullRequest.createdAt, pullRequest.mergedAt),
    } : null,
    pr_to_merge_s: pullRequest ? secondsBetween(pullRequest.createdAt, pullRequest.mergedAt) : null,
    pr_open_to_live_s: prOpenToLive,
    pr_open_to_live_wall_min: wallMin(pullRequest?.createdAt, liveAt),
    ci: ciBody,
    digest_after_tag_s: digestReason ? null : digestSeconds(run),
    pin: pin ? {
      number: pin.number,
      created_at: pin.createdAt,
      merged_at: pin.mergedAt,
      open_to_merge_s: secondsBetween(pin.createdAt, pin.mergedAt),
      long_pole: longPole(pin.checks),
    } : null,
    ...gate,
    rollback_s: rollback.rollback_s,
    rollback_source: rollback.rollback_source,
  };
  if (pinReason) row.pin_reason = pinReason;
  if (rollback.rollback_reason) row.rollback_reason = rollback.rollback_reason;
  if (digestReason) row.digest_reason = digestReason;
  if (cutSpan.reason) row.reasons = { cut_to_live: cutSpan.reason };
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
      text = "gate-ok → live is incomplete: a rollout ticket is missing gate_ok_at, so no median is taken from that partial sample.";
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
  const source = input.pullRequests ? input : normalizeInput(input);
  let rows = versionsOf(source).map((version) => rowFor(source, version));
  rows = rows.filter((row) => row.label || row.version);
  if (opts.since) {
    const since = parseTime(opts.since, "--since");
    rows = rows.filter((row) => {
      const start = instant(releaseStart(row)).ms;
      return start != null && start >= since;
    });
  }
  if (opts.release) rows = rows.filter((row) => matchesRelease(row, opts.release));
  rows.sort((a, b) => (instant(releaseStart(a)).ms ?? 0) - (instant(releaseStart(b)).ms ?? 0));
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
  if (json && json.version && (json.cut_at || json.cutAt || json.live_at || json.liveAt)) return [json];
  throw new Error(`rollout file has no rollouts: ${basename(file)}`);
}

const DEFAULT_PAGE_SIZE = 100;
const DEFAULT_LIST_CAP = 1000;

function unpackPage(body, keys) {
  if (Array.isArray(body)) return { items: body, total: null };
  if (!body || typeof body !== "object") return { items: [], total: null };
  const total = Number.isFinite(body.total_count) ? body.total_count : null;
  for (const key of keys) {
    if (Array.isArray(body[key])) return { items: body[key], total };
  }
  return { items: [], total };
}

function collectPages(gh, path, keys, pageSize, listCap) {
  const items = [];
  let page = 1;
  let total = null;
  let complete = false;
  const sep = path.includes("?") ? "&" : "?";
  while (items.length < listCap) {
    const perPage = Math.min(pageSize, listCap - items.length);
    const body = gh(["api", "--method", "GET", `${path}${sep}page=${page}&per_page=${perPage}`]);
    const unpacked = unpackPage(body, keys);
    if (unpacked.total != null) total = unpacked.total;
    if (unpacked.items.length === 0) {
      complete = true;
      break;
    }
    items.push(...unpacked.items);
    if (total != null && items.length >= total) {
      complete = true;
      break;
    }
    if (total == null && unpacked.items.length < perPage) {
      complete = true;
      break;
    }
    page += 1;
  }
  return { items, truncated: !complete || (total != null && items.length < total) };
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
  if (!Number.isFinite(count) || count <= 1 || Array.isArray(run.attempts)) return run;
  const id = run.databaseId ?? run.id;
  const attempts = [];
  for (let n = 1; n <= count; n++) {
    attempts.push(gh(["api", "--method", "GET", `repos/${repo}/actions/runs/${id}/attempts/${n}`]));
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
  const pageSize = Math.max(1, Number(opts.pageSize) || DEFAULT_PAGE_SIZE);
  const listCap = Math.max(1, Number(opts.listCap) || DEFAULT_LIST_CAP);
  const truncated = [];
  const releasePages = collectPages(gh, `repos/${repo}/actions/workflows/release.yml/runs`, ["workflow_runs"], pageSize, listCap);
  noteTruncation(truncated, "workflow_runs", releasePages.truncated);
  const runs = releasePages.items.map((run) => ({
    ...run,
    workflowName: run.workflowName || run.workflow || "Release",
  }));
  const pullPages = collectPages(
    gh,
    `repos/${repo}/pulls?state=closed&sort=updated&direction=desc`,
    ["items"],
    pageSize,
    listCap,
  );
  noteTruncation(truncated, "pull_requests", pullPages.truncated);
  const pullRequests = pullPages.items.filter((pr) => at(pr, "mergedAt", "merged_at"));
  const pinQuery = encodeURIComponent(`repo:${pinRepo} is:pr is:merged AEON: pin in:title`);
  const pinPages = collectPages(gh, `search/issues?q=${pinQuery}&sort=updated&order=desc`, ["items"], pageSize, listCap);
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
    const jobPages = collectPages(gh, `repos/${repo}/actions/runs/${id}/jobs`, ["jobs"], pageSize, listCap);
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
      `repos/${repo}/actions/workflows/ci.yml/runs?branch=${encodeURIComponent(branch)}&event=pull_request`,
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
    const statusPages = collectPages(gh, `repos/${repo}/commits/${sha}/statuses`, ["statuses"], pageSize, listCap);
    noteTruncation(truncated, "statuses", statusPages.truncated);
    if (statusPages.truncated) statusTruncatedShas.push(sha);
    for (const status of statusPages.items) statuses.push({ ...status, sha });
  }
  const pinDetails = pins.map((pin) => {
    if (!wanted.has(versionIn(pin.title))) return pin;
    return gh(["pr", "view", String(pin.number), "--repo", pinRepo, "--json", "number,title,createdAt,mergedAt,statusCheckRollup"]);
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
Reads GitHub with paginated GET gh api and gh pr view, plus rollout JSON. A truncated list is named in collection.truncated. --fixture runs from a recorded bundle and does not call gh.`;
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
