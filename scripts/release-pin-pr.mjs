// SPDX-License-Identifier: AGPL-3.0-only
// AEON-413: propose exactly one deployment pin through docs/RELEASE.md's PR
// path. Default is read-only; --write requires the coordinator's enable flag.
import { createHash, randomUUID } from "node:crypto";
import { spawnSync } from "node:child_process";
import { appendFileSync, readFileSync } from "node:fs";
import { pathToFileURL } from "node:url";
import { appJWT } from "./homebrew-tap-pr.mjs";
import { validCalendarVersion } from "./verify-release.mjs";

export const SOURCE = "inspr-at/paimos";
export const TARGET = "markus-barta/nixcfg";
export const PIN_PATH = "hosts/csb1/docker/compose-spec.nix";
const IMAGE = "ghcr.io/inspr-at/aeon";
const sha = value => typeof value === "string" && /^[a-f0-9]{40}$/.test(value);
const blobSHA = text => createHash("sha1").update(`blob ${Buffer.byteLength(text)}\0`).update(text).digest("hex");
const fail = message => { throw new Error(`pin bot: ${message}`); };

export function planPin(text, version, digest) {
  if (!validCalendarVersion(version) || !/^sha256:[a-f0-9]{64}$/.test(digest)) fail("invalid release coordinate or digest");
  const lines = text.split("\n");
  const candidates = lines.map((line, i) => line.includes(IMAGE) ? i : -1).filter(i => i >= 0);
  if (candidates.length !== 1) fail("expected exactly one Aeon image pin");
  const line = candidates[0];
  const before = lines[line];
  const match = /^([ \t]*image = ")(ghcr\.io\/inspr-at\/aeon:([^"@]+)@(sha256:[a-f0-9]{64}))(";[ \t]*(?:#[\x20-\x7e]*)?\r?)$/.exec(before);
  if (!match || !validCalendarVersion(match[3])) fail("unsupported deployment pin shape");
  const previous = match[2];
  const image = `${IMAGE}:${version}@${digest}`;
  if (version < match[3]) fail("refusing a deployment pin downgrade");
  if (version === match[3] && image !== previous) fail("published coordinate has a conflicting digest");
  lines[line] = `${match[1]}${image}${match[5]}`;
  const changed = image !== previous;
  return {
    previous, image, changed, text: lines.join("\n"),
    patch: changed ? `--- a/${PIN_PATH}\n+++ b/${PIN_PATH}\n@@ -${line + 1} +${line + 1} @@\n-${before}\n+${lines[line]}\n` : "",
  };
}

// Never surface API bodies, CLI stderr, keys or tokens in errors/logs.
async function github(token, method, path, body) {
  let response;
  try {
    response = await fetch(`https://api.github.com${path}`, {
      method, redirect: "error", signal: AbortSignal.timeout(10_000),
      headers: {
        Authorization: `Bearer ${token}`, Accept: "application/vnd.github+json",
        "User-Agent": "aeon-pin-bot", "X-GitHub-Api-Version": "2022-11-28",
        ...(body === undefined ? {} : { "Content-Type": "application/json" }),
      },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch { fail("GitHub request failed"); }
  if (response.status === 204) return { status: 204, data: null };
  let data;
  try { data = await response.json(); } catch { fail("unreadable GitHub response"); }
  return { status: response.status, data };
}

export function attestationArgs(version, digest, commit) {
  return ["attestation", "verify", `oci://${IMAGE}@${digest}`, "--repo", SOURCE,
    "--signer-workflow", `${SOURCE}/.github/workflows/release.yml`,
    "--signer-digest", commit,
    "--source-ref", `refs/tags/v${version}`, "--source-digest", commit,
    "--deny-self-hosted-runners"];
}

function verifyAttestation(env) {
  const result = spawnSync("gh", attestationArgs(env.VERSION, env.DIGEST, env.GITHUB_SHA), {
    env: { ...process.env, GH_TOKEN: env.GH_TOKEN }, stdio: "ignore", timeout: 30_000,
  });
  if (result.error || result.status !== 0) fail("image attestation verification failed");
}

export async function proposePin(env = process.env, options = {}, dependencies = {}) {
  const request = dependencies.request ?? github;
  const verify = dependencies.verify ?? verifyAttestation;
  const jwt = dependencies.jwt ?? appJWT;
  const { version, digest, commit } = { version: env.VERSION, digest: env.DIGEST, commit: env.GITHUB_SHA };
  const write = options.write === true;
  if (env.GITHUB_REPOSITORY !== SOURCE || env.GITHUB_EVENT_NAME !== "push" ||
      env.GITHUB_REF !== `refs/tags/v${version}` || !validCalendarVersion(version) ||
      !/^sha256:[a-f0-9]{64}$/.test(digest) || !sha(commit) || !env.GH_TOKEN) fail("invalid release invocation");
  if (write && (env.AEON_PIN_BOT_ENABLED !== "true" || options.pinFile)) fail("write mode requires the approved nixcfg review path and live base");
  if (env.INDEX_PUSHED_AT && !/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$/.test(env.INDEX_PUSHED_AT)) fail("invalid index push observation");
  const get = async (token, path, expected = 200, method = "GET", body) => {
    const result = await request(token, method, path, body);
    if (result.status !== expected) fail(`GitHub ${method} failed (HTTP ${result.status})`);
    return result.data;
  };
  const sourcePath = `/repos/${SOURCE}`;
  const tagName = `v${version}`;
  const tagRef = await get(env.GH_TOKEN, `${sourcePath}/git/ref/tags/${tagName}`);
  if (tagRef.ref !== `refs/tags/${tagName}` || tagRef.object?.type !== "tag" || !sha(tagRef.object.sha)) fail("release tag must be annotated");
  const tag = await get(env.GH_TOKEN, `${sourcePath}/git/tags/${tagRef.object.sha}`);
  if (tag.tag !== tagName || tag.sha !== tagRef.object.sha || tag.object?.type !== "commit" || tag.object.sha !== commit) fail("annotated tag does not name the release commit");
  const main = await get(env.GH_TOKEN, `${sourcePath}/git/ref/heads/main`);
  if (main.ref !== "refs/heads/main" || main.object?.type !== "commit" || !sha(main.object.sha)) fail("source main is unavailable");
  const ancestry = await get(env.GH_TOKEN, `${sourcePath}/compare/${commit}...${main.object.sha}`);
  if (!["ahead", "identical"].includes(ancestry.status) || ancestry.base_commit?.sha !== commit || ancestry.merge_base_commit?.sha !== commit) fail("release commit is not on main");
  await verify(env);
  const evidence = [
    `Verified index: ${IMAGE}@${digest}`,
    `Annotated tag: ${SOURCE}@refs/tags/${tagName}; commit ${commit} is on main.`,
    "GitHub build-provenance verified for release.yml, exact source ref/commit and hosted runners.",
  ];
  let token;
  try {
    if (options.pinFile) {
      const plan = planPin(readFileSync(options.pinFile, "utf8"), version, digest);
      return { status: "dry-run", ...plan, evidence: evidence.join("\n") };
    }
    const appId = String(env.AEON_PIN_APP_ID ?? "");
    const key = String(env.AEON_PIN_APP_KEY ?? "");
    if (!appId || !key.trim()) {
      if (write) fail("pin App credentials are missing");
      return { status: "held", evidence: [...evidence, "Pin proposal held: App credentials absent; lead must supply a pin snapshot for the dry-run diff."].join("\n") };
    }
    if (!/^[1-9][0-9]*$/.test(appId)) fail("invalid pin App id");
    let signed;
    try { signed = jwt(appId, key); } catch { fail("pin App key rejected"); }
    const installed = await get(signed, `/repos/${TARGET}/installation`);
    if (!Number.isSafeInteger(installed.id) || installed.id < 1) fail("invalid nixcfg installation");
    const permissions = write ? { contents: "write", pull_requests: "write" } : { contents: "read" };
    const minted = await get(signed, `/app/installations/${installed.id}/access_tokens`, 201, "POST", { repositories: ["nixcfg"], permissions });
    if (typeof minted.token !== "string" || !minted.token) fail("pin installation token unavailable");
    token = minted.token;
    for (const [name, value] of Object.entries(permissions)) {
      if (minted.permissions?.[name] !== value) fail("pin token lacks its requested permissions");
    }
    for (const [name, value] of Object.entries(minted.permissions ?? {})) {
      if ((name === "metadata" && value === "read") || permissions[name] === value) continue;
      fail("pin token has unexpected permissions");
    }
    const targetPath = `/repos/${TARGET}`;
    const base = await get(token, `${targetPath}/git/ref/heads/main`);
    if (base.ref !== "refs/heads/main" || base.object?.type !== "commit" || !sha(base.object.sha)) fail("nixcfg main is unavailable");
    const baseSHA = base.object.sha;
    const current = await get(token, `${targetPath}/contents/${PIN_PATH}?ref=${baseSHA}`);
    if (current.type !== "file" || current.encoding !== "base64" || typeof current.content !== "string" || !sha(current.sha)) fail("pin is not a regular file");
    const bytes = Buffer.from(current.content, "base64");
    const text = bytes.toString("utf8");
    if (!bytes.equals(Buffer.from(text)) || blobSHA(text) !== current.sha) fail("pin content does not match its blob");
    const plan = planPin(text, version, digest);
    const recorded = [...evidence, `Previous pin (rollback): ${plan.previous}`, `nixcfg base/backup reference: ${baseSHA}:${PIN_PATH}`];
    if (!plan.changed || !write) return { status: plan.changed ? "dry-run" : "current", ...plan, baseSHA, evidence: recorded.join("\n") };
    const branch = `aeon-pin-v${version}`;
    const branchPath = `${targetPath}/git/ref/heads/${branch}`;
    const existing = await request(token, "GET", branchPath);
    let headSHA;
    if (existing.status === 404) {
      const created = await get(token, `${targetPath}/git/refs`, 201, "POST", { ref: `refs/heads/${branch}`, sha: baseSHA });
      if (created.ref !== `refs/heads/${branch}` || created.object?.sha !== baseSHA) fail("pin branch creation mismatch");
      const updated = await get(token, `${targetPath}/contents/${PIN_PATH}`, 200, "PUT", {
        message: `AEON-413: pin Aeon ${version}`, branch, sha: current.sha,
        content: Buffer.from(plan.text).toString("base64"),
      });
      headSHA = updated.commit?.sha;
    } else if (existing.status === 200 && existing.data.ref === `refs/heads/${branch}` && existing.data.object?.type === "commit") {
      // Never overwrite an existing branch, including another worker's edits.
      headSHA = existing.data.object.sha;
    } else fail(`pin branch lookup failed (HTTP ${existing.status})`);
    if (!sha(headSHA)) fail("pin branch has no commit");
    const comparison = await get(token, `${targetPath}/compare/${baseSHA}...${headSHA}`);
    const files = comparison.files;
    if (comparison.status !== "ahead" || comparison.base_commit?.sha !== baseSHA || comparison.merge_base_commit?.sha !== baseSHA ||
        comparison.total_commits !== 1 || comparison.commits?.length !== 1 || comparison.commits[0].sha !== headSHA ||
        comparison.commits[0].parents?.length !== 1 || comparison.commits[0].parents[0].sha !== baseSHA ||
        !Array.isArray(files) || files.length !== 1 || files[0].filename !== PIN_PATH || files[0].status !== "modified" ||
        files[0].additions !== 1 || files[0].deletions !== 1 || files[0].changes !== 2 || files[0].sha !== blobSHA(plan.text)) fail("pin branch is not the exact one-line proposal from main");
    const listed = await get(token, `${targetPath}/pulls?head=${encodeURIComponent(`markus-barta:${branch}`)}&state=all&per_page=100`);
    if (!Array.isArray(listed) || listed.length > 1) fail("ambiguous pin pull requests");
    const pull = listed.length ? listed[0] : await get(token, `${targetPath}/pulls`, 201, "POST", {
      title: `AEON-413: pin Aeon ${version}`, head: branch, base: "main", draft: true, maintainer_can_modify: false,
      body: [...recorded, "", "Proposal only. The coordinator must complete nixcfg checks/review and record a validated database backup before any merge or rollout.",
        "Rollback uses the previous immutable image pin above through a separately reviewed pin PR; this bot neither merges nor deploys."].join("\n"),
    });
    if (pull.state !== "open" || pull.draft !== true || pull.base?.ref !== "main" || pull.base.repo?.full_name !== TARGET ||
        pull.head?.ref !== branch || pull.head.sha !== headSHA || pull.head.repo?.full_name !== TARGET ||
        !Number.isSafeInteger(pull.number) || pull.html_url !== `https://github.com/${TARGET}/pull/${pull.number}`) fail("pin PR identity or draft state changed");
    const timing = [];
    if (env.INDEX_PUSHED_AT) {
      const pushed = Date.parse(env.INDEX_PUSHED_AT), opened = Date.parse(pull.created_at);
      if (!Number.isFinite(pushed) || !Number.isFinite(opened) || opened < pushed) fail("pin PR timing evidence is invalid");
      const seconds = (opened - pushed) / 1000;
      timing.push(`Index push observed: ${env.INDEX_PUSHED_AT}; draft PR created: ${new Date(opened).toISOString()}; elapsed: ${seconds}s (target <=30s).`);
    }
    return { status: "proposed", ...plan, baseSHA, url: pull.html_url, evidence: [...recorded, `Draft pin PR: ${pull.html_url}`, ...timing, "Merge and deployment remain coordinator gates."].join("\n") };
  } finally {
    if (token) await get(token, "/installation/token", 204, "DELETE");
  }
}

export function recordProposal(result, env = process.env) {
  const evidence = result.evidence;
  console.log(evidence);
  if (result.patch && result.status === "dry-run") console.log(result.patch);
  if (env.GITHUB_STEP_SUMMARY) appendFileSync(env.GITHUB_STEP_SUMMARY, `\n### Deployment pin proposal (${result.status})\n\n${evidence}\n${result.status === "dry-run" && result.patch ? `\n\`\`\`diff\n${result.patch}\`\`\`\n` : ""}`);
  if (env.GITHUB_OUTPUT) {
    const delimiter = `aeon_pin_${randomUUID()}`;
    appendFileSync(env.GITHUB_OUTPUT, `pin_status=${result.status}\npin_pr=${result.url ?? ""}\npin_evidence<<${delimiter}\n${evidence}\n${delimiter}\n`);
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const args = process.argv.slice(2);
  const options = {};
  try {
    while (args.length) {
      const arg = args.shift();
      if (arg === "--write" && !options.write) options.write = true;
      else if (arg === "--pin-file" && !options.pinFile && args[0] && !args[0].startsWith("--")) options.pinFile = args.shift();
      else fail("usage: release-pin-pr.mjs [--write | --pin-file SNAPSHOT.nix]");
    }
    await proposePin(process.env, options).then(result => recordProposal(result));
  } catch (error) {
    console.error(error?.message?.startsWith("pin bot:") ? error.message : "pin bot: operation failed");
    process.exitCode = 1;
  }
}
