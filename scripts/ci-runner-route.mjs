// SPDX-License-Identifier: AGPL-3.0-only
import { appendFileSync } from "node:fs";
import { pathToFileURL } from "node:url";

export const trustedEvents = ["push", "merge_group", "workflow_dispatch"];
export const leaseSeconds = 30;

// The NIX-600 controller publishes this value-free lease only while the Linux
// ARM64 JIT pool is online and idle. CI never receives its administration key.
export function routeRunner({ event, repository, availability = "", requiredIdle = 1, now = Date.now() }) {
  const hosted = (reason) => ({ runs_on: ["ubuntu-latest"], runner_class: "hosted", reason });
  if (!trustedEvents.includes(event)) return hosted("untrusted-event");
  if (repository !== "inspr-at/paimos") return hosted("different-repository");
  if (!Number.isSafeInteger(requiredIdle) || requiredIdle < 1) return hosted("invalid-capacity-request");
  if (!availability) return hosted("runner-not-enabled");
  let lease;
  try {
    lease = JSON.parse(availability);
  } catch {
    return hosted("invalid-availability");
  }
  if (!lease || lease.schema !== 1 || lease.repository !== repository ||
      lease.os !== "linux" || lease.arch !== "arm64") return hosted("invalid-availability");
  if (lease.online !== true || lease.busy !== false) return hosted("offline-or-busy");
  const observed = typeof lease.observed_at === "string" ? Date.parse(lease.observed_at) : NaN;
  // Reject future dates too: a clock error must never keep an old lease alive.
  if (!Number.isFinite(observed) || !Number.isFinite(now) || observed > now ||
      now - observed >= leaseSeconds * 1000) return hosted("expired-availability");
  if (!Number.isSafeInteger(lease.idle_runners) || lease.idle_runners < requiredIdle) {
    return hosted("insufficient-idle-capacity");
  }
  return { runs_on: ["self-hosted", "Linux", "ARM64", "mbp2606"], runner_class: "mbp2606", reason: "fresh-idle-lease" };
}

export function writeRoute(route, output, summary) {
  const result = `runs_on=${JSON.stringify(route.runs_on)}\nrunner_class=${route.runner_class}\nreason=${route.reason}\n`;
  if (output) appendFileSync(output, result);
  if (summary) appendFileSync(summary, `Test runner: **${route.runner_class}** (${route.reason}).\n`);
  return result;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const route = routeRunner({
    event: process.env.GITHUB_EVENT_NAME,
    repository: process.env.GITHUB_REPOSITORY,
    availability: process.env.AEON_MBP2606_AVAILABILITY,
    requiredIdle: Number(process.env.AEON_REQUIRED_IDLE_RUNNERS || "1"),
  });
  process.stdout.write(writeRoute(route, process.env.GITHUB_OUTPUT, process.env.GITHUB_STEP_SUMMARY));
}
