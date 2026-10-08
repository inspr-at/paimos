// SPDX-License-Identifier: AGPL-3.0-only
import { appendFileSync } from "node:fs";
import { pathToFileURL } from "node:url";

const eventClasses = new Map([
  ["push", "mbp2606-push"],
  ["workflow_dispatch", "mbp2606-dispatch"],
  ["pull_request", "mbp2606-pr"],
  ["merge_group", "mbp2606-mq"],
]);
export const trustedEvents = [...eventClasses.keys()];
const defaultEvents = ["push", "workflow_dispatch"];
export const leaseSeconds = 30;

// The NIX-600 controller publishes this value-free lease only while the Linux
// ARM64 JIT pool has available capacity behind its runner-side admission boundary.
// CI never receives the controller's administration key.
export function routeRunner({ event, repository, ref, headRepository, poolEvents = "", availability = "", requiredIdle = 1, now = Date.now() }) {
  const hosted = (reason) => ({ runs_on: ["ubuntu-latest"], runner_class: "hosted", reason });
  const eventClass = eventClasses.get(event);
  // An empty switch preserves the original main push/dispatch route. A supplied
  // list replaces that default; unknown events never gain an event class.
  const allowedEvents = poolEvents === "" ? defaultEvents :
    typeof poolEvents === "string" ? poolEvents.split(",").map((value) => value.trim()) : [];
  if (!eventClass || !allowedEvents.includes(event)) return hosted("untrusted-event");
  if (repository !== "inspr-at/paimos") return hosted("different-repository");
  if (defaultEvents.includes(event) && ref !== "refs/heads/main") return hosted("untrusted-ref");
  if (event === "pull_request" && headRepository !== repository) return hosted("different-head-repository");
  if (!Number.isSafeInteger(requiredIdle) || requiredIdle < 1) return hosted("invalid-capacity-request");
  if (!availability) return hosted("runner-not-enabled");
  let lease;
  try {
    lease = JSON.parse(availability);
  } catch {
    return hosted("invalid-availability");
  }
  if (!lease || ![1, 2].includes(lease.schema) || lease.repository !== repository ||
      lease.os !== "linux" || lease.arch !== "arm64") return hosted("invalid-availability");
  // PR/merge-group support must be advertised by the upgraded controller. An
  // opt-in alone must never send those jobs to the legacy push/dispatch pool.
  // Push/dispatch retain their schema-1 behavior, including with schema 2.
  if (!defaultEvents.includes(event)) {
    if (lease.schema !== 2 || !Array.isArray(lease.events) ||
        lease.events.length > eventClasses.size ||
        !lease.events.every((value) => eventClasses.has(value)) ||
        new Set(lease.events).size !== lease.events.length ||
        !lease.events.includes(event)) return hosted("unsupported-pool-event");
  }
  if (lease.online !== true || lease.busy !== false) return hosted("offline-or-busy");
  const observed = typeof lease.observed_at === "string" ? Date.parse(lease.observed_at) : NaN;
  // Reject future dates too: a clock error must never keep an old lease alive.
  if (!Number.isFinite(observed) || !Number.isFinite(now) || observed > now ||
      now - observed >= leaseSeconds * 1000) return hosted("expired-availability");
  if (!Number.isSafeInteger(lease.idle_runners) || lease.idle_runners < requiredIdle) {
    return hosted("insufficient-idle-capacity");
  }
  return { runs_on: ["self-hosted", "Linux", "ARM64", "mbp2606", eventClass], runner_class: "mbp2606", reason: "fresh-idle-lease" };
}

export function writeRoute(route, output, summary, runAttempt) {
  // Consumers compare this with github.run_attempt before using runs_on, so a
  // failed-job rerun cannot reuse a previous successful router's lease.
  if (!Number.isSafeInteger(runAttempt) || runAttempt < 1) throw new Error("invalid run attempt");
  const result = `runs_on=${JSON.stringify(route.runs_on)}\nrunner_class=${route.runner_class}\nreason=${route.reason}\nrun_attempt=${runAttempt}\n`;
  if (output) appendFileSync(output, result);
  if (summary) appendFileSync(summary, `Test runner: **${route.runner_class}** (${route.reason}).\n`);
  return result;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const route = routeRunner({
    event: process.env.GITHUB_EVENT_NAME,
    repository: process.env.GITHUB_REPOSITORY,
    ref: process.env.GITHUB_REF,
    headRepository: process.env.AEON_PR_HEAD_REPOSITORY,
    poolEvents: process.env.AEON_POOL_EVENTS,
    availability: process.env.AEON_MBP2606_AVAILABILITY,
    requiredIdle: Number(process.env.AEON_REQUIRED_IDLE_RUNNERS || "1"),
  });
  process.stdout.write(writeRoute(route, process.env.GITHUB_OUTPUT, process.env.GITHUB_STEP_SUMMARY, Number(process.env.GITHUB_RUN_ATTEMPT)));
}
