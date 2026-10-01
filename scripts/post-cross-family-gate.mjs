// SPDX-License-Identifier: AGPL-3.0-only
// External coordinator only: installed from trusted main, never from a PR checkout.
import { readFileSync } from "node:fs";
import { pathToFileURL } from "node:url";
import { checkEvent, createGit, githubAPI } from "./cross-family-gate.mjs";

export const repository = "inspr-at/paimos";
export const statusApp = { id: 5134402, login: "inspr-mbp2606-runner[bot]" };
const context = "gate/cross-family";

export function statusTarget(eventName, event) {
  if (event?.repository?.full_name !== repository) throw new Error("event repository mismatch");
  let target;
  if (eventName === "pull_request" &&
      ["opened", "synchronize", "reopened"].includes(event.action) &&
      event.pull_request?.base?.ref === "main" &&
      event.pull_request.base.repo?.full_name === repository) {
    target = event.pull_request.head?.sha;
  } else if (eventName === "merge_group" && event.action === "checks_requested" &&
             event.merge_group?.base_ref === "refs/heads/main") {
    target = event.merge_group.head_sha;
  }
  if (!/^[a-f0-9]{40}$/.test(target ?? "")) {
    throw new Error("poster only accepts main PR or merge-group check requests with exact SHAs");
  }
  return target;
}

export function statusWriter({ token, fetchImpl = fetch }) {
  if (!token) throw new Error("missing external App installation token");
  return async (target, body) => {
    if (!/^[a-f0-9]{40}$/.test(target ?? "") || body.context !== context ||
        !["pending", "failure", "success"].includes(body.state)) throw new Error("invalid gate status");
    const response = await fetchImpl(`https://api.github.com/repos/${repository}/statuses/${target}`, {
      method: "POST", redirect: "error", signal: AbortSignal.timeout(30000),
      headers: { Accept: "application/vnd.github+json", Authorization: `Bearer ${token}`,
        "Content-Type": "application/json", "X-GitHub-Api-Version": "2022-11-28" },
      body: JSON.stringify(body),
    });
    if (!response.ok) throw new Error(`GitHub status write failed (HTTP ${response.status})`);
    const result = await response.json();
    if (result.creator?.login !== statusApp.login || result.creator?.type !== "Bot" ||
        result.context !== body.context || result.state !== body.state) {
      throw new Error("status was not posted by the configured external App");
    }
  };
}

export async function postGate({ eventName, event, config, git, api, write = false, publish, actions = false }) {
  if (actions) throw new Error("external gate poster must not run in GitHub Actions");
  const target = statusTarget(eventName, event);
  const status = (state, description) => ({ state, context, description });
  // Revoke a previous pass before attempting reads. A timeout/crash remains pending.
  if (write) await publish(target, status("pending", "Verifying trusted cross-family review evidence"));
  let body;
  let results = [];
  try {
    results = await checkEvent({ eventName, event, config, repository, git, api });
    body = status("success", "Trusted main policy verified every PR head and merge tree");
  } catch {
    // Transport errors and API response bodies may contain credentials; never emit them.
    body = status("failure", "Trusted main policy refused missing, revoked or changed review evidence");
  }
  if (write) await publish(target, body);
  return { target, ...body, results, written: write };
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const [configPath, eventName, eventPath, flag] = process.argv.slice(2);
    if (!eventPath || (flag !== undefined && flag !== "--write") || process.argv.length > 6) {
      throw new Error("usage: post-cross-family-gate.mjs CONFIG EVENT_NAME EVENT_PATH [--write]");
    }
    const token = process.env.GATE_APP_TOKEN;
    const result = await postGate({
      eventName, event: JSON.parse(readFileSync(eventPath, "utf8")),
      config: JSON.parse(readFileSync(configPath, "utf8")), git: createGit(),
      api: githubAPI({ repository, token }), actions: !!process.env.GITHUB_ACTIONS,
      write: flag === "--write", publish: flag === "--write" ? statusWriter({ token }) : undefined,
    });
    console.log(JSON.stringify(result));
    if (result.state !== "success") process.exitCode = 1;
  } catch {
    console.error("external cross-family poster failed closed; inspect trusted configuration and App permissions");
    process.exitCode = 1;
  }
}
