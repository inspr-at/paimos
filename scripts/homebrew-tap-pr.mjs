// SPDX-License-Identifier: AGPL-3.0-only
// After a v* release is published, open a pull request on inspr-at/homebrew-tap
// that updates Formula/aeon-agentd.rb. Authentication is the inspr-homebrew-tap
// GitHub App (contents + pull_requests on that repository only). Secrets stay
// in the homebrew-tap environment; when either is absent this exits 0.
import { createSign } from "node:crypto";
import { pathToFileURL } from "node:url";
import { renderFormula } from "./homebrew-formula.mjs";
import { validCalendarVersion } from "./verify-release.mjs";

const TAP = "inspr-at/homebrew-tap";
const FORMULA_PATH = "Formula/aeon-agentd.rb";

export function appJWT(appId, privateKeyPem, nowSec = Math.floor(Date.now() / 1000)) {
  const header = Buffer.from(JSON.stringify({ alg: "RS256", typ: "JWT" })).toString("base64url");
  const payload = Buffer.from(JSON.stringify({
    iat: nowSec - 60,
    exp: nowSec + 540,
    iss: String(appId),
  })).toString("base64url");
  const data = `${header}.${payload}`;
  const sign = createSign("RSA-SHA256");
  sign.update(data);
  sign.end();
  return `${data}.${sign.sign(privateKeyPem).toString("base64url")}`;
}

function normalize(text) {
  return String(text).replace(/\r\n/g, "\n").replace(/\s+$/, "") + "\n";
}

function githubError(what, status, data) {
  const message = data && typeof data.message === "string" ? data.message : "";
  return new Error(`${what} failed (${status})${message ? `: ${message}` : ""}`);
}

async function gh(token, method, path, body) {
  const res = await fetch(`https://api.github.com${path}`, {
    method,
    redirect: "error",
    signal: AbortSignal.timeout(30_000),
    headers: {
      Authorization: `Bearer ${token}`,
      Accept: "application/vnd.github+json",
      "User-Agent": "aeon-homebrew-tap",
      "X-GitHub-Api-Version": "2022-11-28",
      ...(body === undefined ? {} : { "Content-Type": "application/json" }),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  let data = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = { message: "unreadable response" };
    }
  }
  return { status: res.status, data };
}

async function fetchChecksums(version) {
  const url = `https://github.com/inspr-at/paimos/releases/download/v${version}/SHA256SUMS`;
  let last = "no response";
  for (let attempt = 0; attempt < 5; attempt++) {
    if (attempt > 0) await new Promise((resolve) => setTimeout(resolve, 3000));
    const res = await fetch(url, { redirect: "follow", signal: AbortSignal.timeout(30_000) });
    if (!res.ok) {
      last = `HTTP ${res.status}`;
      continue;
    }
    const text = await res.text();
    if (text.length > 1_000_000) throw new Error("SHA256SUMS is too large");
    return text;
  }
  throw new Error(`SHA256SUMS download failed (${last})`);
}

async function installationToken(appId, privateKey) {
  const jwt = appJWT(appId, privateKey);
  const found = await gh(jwt, "GET", `/repos/${TAP}/installation`);
  if (found.status !== 200 || !Number.isInteger(found.data?.id)) throw githubError("tap installation lookup", found.status, found.data);
  const minted = await gh(jwt, "POST", `/app/installations/${found.data.id}/access_tokens`, {
    repositories: ["homebrew-tap"],
  });
  if ((minted.status !== 201 && minted.status !== 200) || typeof minted.data?.token !== "string" || minted.data.token === "") {
    throw githubError("installation token", minted.status, minted.data);
  }
  return minted.data.token;
}

async function readFormula(token, ref) {
  const res = await gh(token, "GET", `/repos/${TAP}/contents/${FORMULA_PATH}?ref=${encodeURIComponent(ref)}`);
  if (res.status === 404) return null;
  if (res.status !== 200 || typeof res.data?.content !== "string" || typeof res.data?.sha !== "string") {
    throw githubError("read formula", res.status, res.data);
  }
  return { sha: res.data.sha, text: Buffer.from(res.data.content, "base64").toString("utf8") };
}

async function ensureBranch(token, base, branch) {
  const existing = await gh(token, "GET", `/repos/${TAP}/git/ref/heads/${encodeURIComponent(branch)}`);
  if (existing.status === 200) return;
  if (existing.status !== 404) throw githubError("read branch", existing.status, existing.data);
  const baseRef = await gh(token, "GET", `/repos/${TAP}/git/ref/heads/${encodeURIComponent(base)}`);
  if (baseRef.status !== 200 || typeof baseRef.data?.object?.sha !== "string") throw githubError("read default branch", baseRef.status, baseRef.data);
  const created = await gh(token, "POST", `/repos/${TAP}/git/refs`, {
    ref: `refs/heads/${branch}`,
    sha: baseRef.data.object.sha,
  });
  if (created.status !== 201) throw githubError("create branch", created.status, created.data);
}

async function writeFormula(token, branch, version, formula, blobSha) {
  const body = {
    message: `aeon-agentd ${version}\n\nInstall the signed, notarized darwin release bytes from SHA256SUMS.\n`,
    content: Buffer.from(formula).toString("base64"),
    branch,
  };
  if (blobSha) body.sha = blobSha;
  const res = await gh(token, "PUT", `/repos/${TAP}/contents/${FORMULA_PATH}`, body);
  if (res.status !== 200 && res.status !== 201) throw githubError("update formula", res.status, res.data);
}

async function ensurePull(token, base, branch, version) {
  const listed = await gh(token, "GET", `/repos/${TAP}/pulls?head=${encodeURIComponent(`inspr-at:${branch}`)}&state=open&per_page=20`);
  if (listed.status !== 200 || !Array.isArray(listed.data)) throw githubError("list pull requests", listed.status, listed.data);
  if (listed.data.length > 0) {
    const url = listed.data[0].html_url || listed.data[0].url;
    console.log(url ? `homebrew tap pull request: ${url}` : "homebrew tap pull request already open");
    return;
  }
  const opened = await gh(token, "POST", `/repos/${TAP}/pulls`, {
    title: `aeon-agentd ${version}`,
    head: branch,
    base,
    body: [
      `Update Formula/aeon-agentd.rb to ${version}.`,
      "",
      "The formula installs the exact signed and notarized darwin arm64 and amd64 release assets. sha256 values come from that release's SHA256SUMS. There is no source build and no re-sign.",
      "",
    ].join("\n"),
  });
  if (opened.status !== 201 || typeof opened.data?.html_url !== "string") throw githubError("open pull request", opened.status, opened.data);
  console.log(`homebrew tap pull request: ${opened.data.html_url}`);
}

export async function bumpHomebrewTap(env = process.env) {
  const appId = String(env.HOMEBREW_TAP_APP_ID || "").trim();
  const key = String(env.HOMEBREW_TAP_APP_KEY || "");
  if (!appId || !key.trim()) {
    console.log("homebrew tap bump skipped: app secrets absent");
    return;
  }
  if (!/^[0-9]+$/.test(appId)) throw new Error("HOMEBREW_TAP_APP_ID is not numeric");
  const version = String(env.VERSION || "").trim();
  if (!validCalendarVersion(version)) throw new Error("VERSION is not an inspr-calendar-v2 coordinate");
  const formula = renderFormula(version, await fetchChecksums(version));
  const token = await installationToken(appId, key);
  const repo = await gh(token, "GET", `/repos/${TAP}`);
  if (repo.status !== 200 || typeof repo.data?.default_branch !== "string" || !/^[A-Za-z0-9._/-]+$/.test(repo.data.default_branch)) {
    throw githubError("read tap repo", repo.status, repo.data);
  }
  const base = repo.data.default_branch;
  const current = await readFormula(token, base);
  if (current && normalize(current.text) === normalize(formula)) {
    console.log(`homebrew tap formula is current for ${version}`);
    return;
  }
  const branch = `aeon-agentd-v${version}`;
  await ensureBranch(token, base, branch);
  const onBranch = await readFormula(token, branch);
  if (!onBranch || normalize(onBranch.text) !== normalize(formula)) {
    await writeFormula(token, branch, version, formula, onBranch ? onBranch.sha : undefined);
  }
  await ensurePull(token, base, branch, version);
}

function report(err) {
  const message = err && err.message ? err.message : "homebrew tap bump failed";
  if (message.includes("PRIVATE") || message.includes("BEGIN ")) {
    console.error("homebrew tap bump failed: app key was rejected");
    return;
  }
  console.error(message);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  bumpHomebrewTap().catch((err) => {
    report(err);
    process.exitCode = 1;
  });
}
