// SPDX-License-Identifier: AGPL-3.0-only
// Release timing evidence (AEON-416, deploy-speed-10x §1 and §5 item 10).
//
// cut → live     rollout cut_at → live_at. wall_min is the truncated UTC-minute span,
//                which is how §1 counts 128 / 38 / 84 / 58.
// PR → merge     the release pull request's createdAt → mergedAt.
// gate → live    median of live_at minus each gate/cross-family success (or rollout gate_ok_at).
// rollback       rollout rollback_started_at → rollback_finished_at, else a pin PR
//                whose title says rollback. A forward pin PR is not a rollback.
//
// GitHub access is read-only: gh pr/run list and view, and GET gh api. No workflow
// dispatch, rerun, or release write.

import { spawnSync } from "node:child_process";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { basename, join } from "node:path";
import { pathToFileURL } from "node:url";

const VERSION_RE = /(\d{12}\.\d+\.\d+)/;
const READ_ONLY = new Set(["pr list", "pr view", "run list", "run view", "api"]);

export function assertReadOnly(args) {
  if (!Array.isArray(args) || args.length < 1) throw new Error("empty gh command");
  const head = args[0] === "pr" || args[0] === "run" ? `${args[0]} ${args[1]}` : args[0];
  if (!READ_ONLY.has(head)) throw new Error(`refusing gh ${args.slice(0, 2).join(" ")}`);
  if (args[0] !== "api") return;
  const methodAt = args.indexOf("--method");
  const method = methodAt >= 0 ? String(args[methodAt + 1] || "").toUpperCase() : "GET";
  if (method !== "GET") throw new Error("refusing non-GET gh api");
  if (args.some((arg) => arg === "-f" || arg === "-F" || arg === "--input" || arg === "--raw-field")) {
    throw new Error("refusing gh api write fields");
  }
}

function redact(text) {
  return String(text || "")
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

function parseTime(value, label) {
  const ms = Date.parse(value);
  if (!Number.isFinite(ms)) throw new Error(`${label} is not a timestamp`);
  return ms;
}

export function secondsBetween(start, end) {
  if (!start || !end) return null;
  const ms = Date.parse(end) - Date.parse(start);
  if (!Number.isFinite(ms)) return null;
  return Math.round(ms / 1000);
}

// §1's published walls are the difference of the timestamps with seconds dropped.
export function wallMin(start, end) {
  if (!start || !end) return null;
  const s = Date.parse(start);
  const e = Date.parse(end);
  if (!Number.isFinite(s) || !Number.isFinite(e)) return null;
  return Math.floor(e / 60000) - Math.floor(s / 60000);
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
  return {
    pullRequests: asArray(raw.pull_requests ?? raw.pullRequests).map(normalizePullRequest),
    pinPullRequests: asArray(raw.pin_pull_requests ?? raw.pinPullRequests).map(normalizePin),
    runs: asArray(raw.workflow_runs ?? raw.workflowRuns).map(normalizeRun),
    statuses: asArray(raw.statuses).map(normalizeStatus),
    rollouts: asArray(raw.rollouts).map(normalizeRollout),
  };
}

function normalizePullRequest(pr) {
  return {
    number: pr.number ?? null,
    title: pr.title || "",
    createdAt: at(pr, "createdAt", "created_at"),
    mergedAt: at(pr, "mergedAt", "merged_at"),
    headRef: at(pr, "headRefName", "headRef", "head_ref") || "",
    mergeSha: pr.mergeCommit?.oid || pr.mergeSha || pr.merge_sha || null,
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

function normalizeRun(run) {
  return {
    id: run.databaseId ?? run.id ?? null,
    workflow: run.workflowName || run.workflow || "",
    event: run.event || "",
    conclusion: String(run.conclusion || "").toLowerCase(),
    createdAt: at(run, "createdAt", "created_at"),
    updatedAt: at(run, "updatedAt", "updated_at"),
    headBranch: run.headBranch || run.head_branch || "",
    headSha: run.headSha || run.head_sha || "",
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

function ciMetrics(runs, branch, mergedAt) {
  const empty = { failed_s: null, success_s: null, failure_and_fix_s: null };
  if (!branch) return empty;
  let rows = runs.filter((run) => run.workflow === "CI" && run.event === "pull_request" && run.headBranch === branch);
  if (mergedAt) rows = rows.filter((run) => Date.parse(run.createdAt) <= Date.parse(mergedAt));
  rows.sort((a, b) => Date.parse(a.createdAt) - Date.parse(b.createdAt));
  const failure = rows.find((run) => run.conclusion === "failure");
  const success = failure
    ? rows.find((run) => run.conclusion === "success" && Date.parse(run.createdAt) >= Date.parse(failure.createdAt))
    : [...rows].reverse().find((run) => run.conclusion === "success");
  return {
    failed_s: failure ? secondsBetween(failure.createdAt, failure.updatedAt) : null,
    success_s: success ? secondsBetween(success.createdAt, success.updatedAt) : null,
    failure_and_fix_s: failure && success ? secondsBetween(failure.createdAt, success.createdAt) : null,
  };
}

function gateSeconds(liveAt, rollout, statuses, sha) {
  if (!liveAt) return { gate_ok_to_live_s: null, gate_samples: 0 };
  const stamps = [];
  const tickets = (rollout?.tickets || []).filter((ticket) => ticket.gateOkAt);
  if (tickets.length) {
    for (const ticket of tickets) stamps.push(ticket.gateOkAt);
  } else {
    for (const status of statuses) {
      if (status.sha !== sha) continue;
      if (status.context !== "gate/cross-family" || status.state !== "success" || !status.updatedAt) continue;
      stamps.push(status.updatedAt);
    }
  }
  const samples = stamps
    .map((stamp) => secondsBetween(stamp, liveAt))
    .filter((value) => value != null);
  return { gate_ok_to_live_s: median(samples), gate_samples: samples.length };
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

function rowFor(input, version) {
  const run = chooseReleaseRun(input.runs, version);
  const rollout = input.rollouts.find((item) => item.version === version) || null;
  const pullRequest = input.pullRequests.find((pr) => run && pr.mergeSha && pr.mergeSha === run.headSha)
    || input.pullRequests.find((pr) => versionIn(pr.title) === version)
    || null;
  const pin = input.pinPullRequests.find((pr) => versionIn(pr.title) === version) || null;
  const label = rollout?.release || labelFromRef(pullRequest?.headRef) || null;
  const cutAt = rollout?.cutAt || null;
  const liveAt = rollout?.liveAt || null;
  const elapsed = secondsBetween(cutAt, liveAt);
  const ci = ciMetrics(input.runs, pullRequest?.headRef, pullRequest?.mergedAt);
  const without = elapsed != null && ci.failure_and_fix_s != null ? elapsed - ci.failure_and_fix_s : null;
  const gate = gateSeconds(liveAt, rollout, input.statuses, run?.headSha || "");
  let rollbackS = null;
  let rollbackSource = null;
  if (rollout?.rollbackStartedAt && rollout?.rollbackFinishedAt) {
    rollbackS = secondsBetween(rollout.rollbackStartedAt, rollout.rollbackFinishedAt);
    rollbackSource = "rollout";
  } else if (pin && /rollback/i.test(pin.title)) {
    rollbackS = secondsBetween(pin.createdAt, pin.mergedAt);
    rollbackSource = "pin";
  }
  const prOpenToLive = secondsBetween(pullRequest?.createdAt, liveAt);
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
    ci: {
      ...ci,
      cut_to_live_without_failure_s: without,
      cut_to_live_without_failure_min: roundMin(without),
    },
    digest_after_tag_s: digestSeconds(run),
    pin: pin ? {
      number: pin.number,
      created_at: pin.createdAt,
      merged_at: pin.mergedAt,
      open_to_merge_s: secondsBetween(pin.createdAt, pin.mergedAt),
      long_pole: longPole(pin.checks),
    } : null,
    ...gate,
    rollback_s: rollbackS,
    rollback_source: rollbackSource,
  };
  row.notes = explain(row);
  row.section1 = section1(row);
  return row;
}

function gapText(rows) {
  const gaps = [];
  if (rows.length && rows.every((row) => row.gate_ok_to_live_s == null)) {
    gaps.push({
      id: "gate-ok",
      text: "gate-ok → live is empty: no successful gate/cross-family status and no rollout gate_ok_at. §1's 3-5 h median is the published estimate for 12c/12d. Those verdicts were ticket comments, and this issue API returns untimed comment strings, so the median is not recomputed.",
    });
  }
  if (rows.length && rows.every((row) => row.rollback_s == null)) {
    const pins = rows.map((row) => row.pin?.open_to_merge_s).filter((value) => value != null);
    let measured = "No forward pin PR was matched";
    if (pins.length === 1) measured = `The matched forward pin PR merged in ${pins[0]}s`;
    else if (pins.length > 1) measured = `Forward pin PRs in this report merged in ${Math.min(...pins)}-${Math.max(...pins)}s`;
    gaps.push({
      id: "rollback",
      text: `rollback is empty: no rollout rollback interval and no pin PR marked as a rollback. §1's ≈15 min is an estimate for a rollback pin PR, not an observed rollback (the published failure count was 0 of 12). ${measured}.`,
    });
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
      const start = releaseStart(row);
      return start && Date.parse(start) >= since;
    });
  }
  if (opts.release) rows = rows.filter((row) => matchesRelease(row, opts.release));
  rows.sort((a, b) => Date.parse(releaseStart(a) || 0) - Date.parse(releaseStart(b) || 0));
  const now = opts.now instanceof Date ? opts.now : new Date(opts.now || Date.now());
  return {
    schema: "aeon.release-timing.v1",
    generated_at: now.toISOString(),
    filters: { since: opts.since || null, release: opts.release || null },
    releases: rows,
    gaps: gapText(rows),
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

const LIST_JSON = "databaseId,workflowName,event,conclusion,createdAt,updatedAt,headBranch,headSha";
const PR_JSON = "number,title,createdAt,mergedAt,mergeCommit,headRefName";

export function fetchInputs(opts) {
  const gh = (args) => {
    assertReadOnly(args);
    return opts.gh(args);
  };
  const repo = opts.repo || "inspr-at/paimos";
  const pinRepo = opts.pinRepo || "markus-barta/nixcfg";
  const runs = gh(["run", "list", "--repo", repo, "--workflow", "release.yml", "--limit", "40", "--json", LIST_JSON]);
  const pullRequests = gh(["pr", "list", "--repo", repo, "--state", "merged", "--limit", "60", "--json", PR_JSON]);
  const pins = gh(["pr", "list", "--repo", pinRepo, "--state", "merged", "--limit", "40", "--search", "AEON: pin in:title sort:updated-desc", "--json", "number,title,createdAt,mergedAt"]);
  const listed = { workflow_runs: runs, pull_requests: pullRequests, pin_pull_requests: pins, statuses: [], rollouts: opts.rollouts || [] };
  const wanted = new Set(buildReport(listed, opts).releases.map((row) => row.version));
  const enrichedRuns = runs.map((run) => {
    const version = versionIn(run.headBranch);
    if (!wanted.has(version)) return run;
    const body = gh(["api", "--method", "GET", `repos/${repo}/actions/runs/${run.databaseId}/jobs`]);
    return { ...run, jobs: body.jobs || [] };
  });
  const branches = new Set();
  const shas = new Set();
  for (const run of enrichedRuns) {
    if (!wanted.has(versionIn(run.headBranch))) continue;
    if (run.headSha) shas.add(run.headSha);
    const pr = pullRequests.find((item) => item.mergeCommit?.oid === run.headSha);
    if (pr?.headRefName) branches.add(pr.headRefName);
  }
  const ciRuns = [];
  for (const branch of branches) {
    ciRuns.push(...gh(["run", "list", "--repo", repo, "--workflow", "CI", "--branch", branch, "--limit", "20", "--json", LIST_JSON]));
  }
  const statuses = [];
  for (const sha of shas) {
    const body = gh(["api", "--method", "GET", `repos/${repo}/commits/${sha}/status`]);
    for (const status of body.statuses || []) statuses.push({ ...status, sha });
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
Reads GitHub with gh (list/view and GET only) and rollout JSON. --fixture runs from a recorded bundle and does not call gh.`;
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
