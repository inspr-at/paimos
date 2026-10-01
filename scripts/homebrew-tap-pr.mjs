// SPDX-License-Identifier: AGPL-3.0-only
// Trusted main dispatch only, after an immutable v* release is published.
// Open a pull request on inspr-at/homebrew-tap
// that updates Formula/aeon-agentd.rb. Authentication is the inspr-homebrew-tap
// GitHub App (contents + pull_requests on that repository only). Secrets stay
// in the homebrew-tap environment; when either is absent this exits 0.
import { createSign, createHash } from "node:crypto";
import { pathToFileURL } from "node:url";
import { renderFormula, parseChecksums } from "./homebrew-formula.mjs";
import { validCalendarVersion } from "./verify-release.mjs";

const TAP = "inspr-at/homebrew-tap";
const FORMULA_PATH = "Formula/aeon-agentd.rb";
export const RELEASE_ASSETS = ["aeon-cli", "paimos-agentd"].flatMap(name =>
  ["darwin-arm64", "darwin-amd64", "linux-arm64", "linux-amd64"].map(platform => `${name}-${platform}`));
const sha256 = bytes => createHash("sha256").update(bytes).digest("hex");
export class TapPendingError extends Error {}

function refuseTapDowngrade(current, version) {
  if (!current) return;
  const installed = /^  version "([^"]+)"$/m.exec(current.text)?.[1];
  if (!validCalendarVersion(installed) || installed > version) throw new Error("tap base version mismatch or downgrade refused");
}

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
  // Remote response text may contain credentials or signed URLs.
  return new Error(`${what} failed (${status})`);
}

export async function gh(token, method, path, body, headers = {}) {
  const res = await fetch(`https://api.github.com${path}`, {
    method,
    redirect: "error",
    signal: AbortSignal.timeout(30_000),
    headers: {
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      Accept: "application/vnd.github+json",
      "User-Agent": "aeon-homebrew-tap",
      "X-GitHub-Api-Version": "2022-11-28",
      ...(body === undefined ? {} : { "Content-Type": "application/json" }),
      ...headers,
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
  return { status: res.status, data, etag: res.headers.get("etag") };
}

export async function verifyPublishedTag(version) {
  if (!validCalendarVersion(version)) throw new Error("VERSION is not a calendar coordinate");
  const ref = await gh("", "GET", `/repos/inspr-at/paimos/git/ref/tags/v${version}`);
  if (ref.status !== 200 || ref.data?.object?.type !== "tag" || !/^[a-f0-9]{40}$/.test(ref.data.object.sha ?? "")) throw new Error("annotated release tag required");
  const tag = await gh("", "GET", `/repos/inspr-at/paimos/git/tags/${ref.data.object.sha}`);
  if (tag.status !== 200 || tag.data?.tag !== `v${version}` || tag.data.object?.type !== "commit" || !/^[a-f0-9]{40}$/.test(tag.data.object.sha ?? "")) throw new Error("release tag source mismatch");
  const main = await gh("", "GET", `/repos/inspr-at/paimos/compare/${tag.data.object.sha}...main`);
  if (main.status !== 200 || !["ahead", "identical"].includes(main.data?.status)) throw new Error("release source is not on main");
}

export async function downloadReleaseAsset(token, asset) {
  if (!Number.isSafeInteger(asset?.id) || asset.id < 1 || !Number.isSafeInteger(asset.size) ||
      asset.size < 1 || asset.size > 200_000_000 || !/^sha256:[a-f0-9]{64}$/.test(asset.digest ?? "")) throw new Error("invalid release asset metadata");
  let response = await fetch(`https://api.github.com/repos/inspr-at/paimos/releases/assets/${asset.id}`, {
    redirect: "manual", signal: AbortSignal.timeout(120_000),
    headers: { ...(token ? { Authorization: `Bearer ${token}` } : {}), Accept: "application/octet-stream", "X-GitHub-Api-Version": "2022-11-28" },
  });
  if (response.status === 302) {
    const location = new URL(response.headers.get("location"));
    if (location.protocol !== "https:" || location.username || location.password || location.port ||
        location.hostname !== "release-assets.githubusercontent.com") throw new Error("asset redirect refused");
    response = await fetch(location, { redirect: "error", signal: AbortSignal.timeout(120_000) });
  }
  if (!response.ok) throw new Error("release asset download failed");
  const chunks = []; let size = 0;
  for await (const chunk of response.body) {
    size += chunk.length;
    if (size > asset.size) throw new Error("release asset size mismatch");
    chunks.push(chunk);
  }
  if (size !== asset.size) throw new Error("release asset size mismatch");
  const bytes = Buffer.concat(chunks);
  if (`sha256:${sha256(bytes)}` !== asset.digest) throw new Error("release asset digest mismatch");
  return bytes;
}

export async function fetchPublishedChecksums(version) {
  if (!validCalendarVersion(version)) throw new Error("VERSION is not a calendar coordinate");
  // Deliberately unauthenticated: consumers must not see draft assets, even if
  // the caller has a token that could read them. Check before minting a tap token.
  const release = await fetch(`https://api.github.com/repos/inspr-at/paimos/releases/tags/v${version}`, {
    redirect: "error",
    signal: AbortSignal.timeout(30_000),
    headers: {
      Accept: "application/vnd.github+json",
      "User-Agent": "aeon-homebrew-tap",
      "X-GitHub-Api-Version": "2022-11-28",
    },
  });
  if (!release.ok) throw new Error(`published release lookup failed (HTTP ${release.status})`);
  const metadata = await release.json();
  if (metadata?.tag_name !== `v${version}` || metadata.draft !== false ||
      metadata.prerelease !== false || typeof metadata.published_at !== "string" ||
      !Number.isFinite(Date.parse(metadata.published_at))) {
    throw new Error("Homebrew requires the exact published, non-draft, non-prerelease release");
  }
  if (metadata.immutable !== true) throw new Error("immutable published release required");
  const assets = metadata.assets;
  if (!Array.isArray(assets) || JSON.stringify(assets.map(a => a.name).sort()) !== JSON.stringify([...RELEASE_ASSETS, "SHA256SUMS"].sort()) ||
      new Set(assets.map(a => a.id)).size !== assets.length || assets.some(a => a.state !== "uploaded" || !/^sha256:[a-f0-9]{64}$/.test(a.digest ?? ""))) throw new Error("complete digest-pinned release required");
  await verifyPublishedTag(version);
  const asset = assets.find(a => a.name === "SHA256SUMS");
  if (asset.size > 1_000_000) throw new Error("SHA256SUMS is too large");
  const text = (await downloadReleaseAsset("", asset)).toString("utf8");
  const sums = parseChecksums(text);
  if (JSON.stringify([...sums.keys()].sort()) !== JSON.stringify([...RELEASE_ASSETS].sort()) ||
      RELEASE_ASSETS.some(name => assets.find(a => a.name === name).digest !== `sha256:${sums.get(name)}`)) throw new Error("release checksum metadata mismatch");
  return text;
}

export async function revokeInstallationToken(token) {
  const result = await gh(token, "DELETE", "/installation/token");
  if (result.status !== 204) throw new Error("installation token revocation failed");
}

export async function installationToken(appId, privateKey, repository = TAP, access = "tap-write") {
  if (![TAP, "inspr-at/paimos"].includes(repository)) throw new Error("unsupported App repository");
  const profiles = repository === TAP ? {
    "tap-read": { contents: "read", pull_requests: "read", checks: "read", statuses: "read" },
    "tap-write": { contents: "write", pull_requests: "write" },
    "tap-merge": { contents: "write", pull_requests: "write", checks: "read", statuses: "read" },
  } : { attestation: { attestations: "read" }, "release-write": { contents: "write" } };
  if (!Object.hasOwn(profiles, access)) throw new Error("unsupported App permission profile");
  const permissions = { ...profiles[access], metadata: "read" };
  const jwt = appJWT(appId, privateKey);
  const found = await gh(jwt, "GET", `/repos/${repository}/installation`);
  if (found.status !== 200 || !Number.isInteger(found.data?.id)) throw githubError("tap installation lookup", found.status, found.data);
  const minted = await gh(jwt, "POST", `/app/installations/${found.data.id}/access_tokens`, {
    repositories: [repository.split("/")[1]],
    permissions,
  });
  if ((minted.status !== 201 && minted.status !== 200) || typeof minted.data?.token !== "string" || minted.data.token === "") {
    throw githubError("installation token", minted.status, minted.data);
  }
  if (!minted.data.permissions || Object.keys(minted.data.permissions).length !== Object.keys(permissions).length ||
      Object.entries(permissions).some(([key, value]) => minted.data.permissions[key] !== value)) {
    try { await revokeInstallationToken(minted.data.token); } catch { /* Refusal remains fail-closed. */ }
    throw new Error("unexpected App token permissions refused");
  }
  return minted.data.token;
}

// Coordinator opt-in only. Reuse the published-release/formula path and merge
// exactly one verified formula change, at the observed head, without admin bypass.
export async function mergeHomebrewTap(token, version, checksums, apply = false) {
  const formula = renderFormula(version, checksums);
  const repo = await gh(token, "GET", `/repos/${TAP}`);
  if (repo.status !== 200 || typeof repo.data?.default_branch !== "string") throw new Error("tap repository lookup failed");
  const base = repo.data.default_branch;
  const current = await readFormula(token, base);
  if (current && normalize(current.text) === normalize(formula)) return { state: "current" };
  refuseTapDowngrade(current, version);
  const branch = `aeon-agentd-v${version}`;
  const listed = await gh(token, "GET", `/repos/${TAP}/pulls?head=${encodeURIComponent(`inspr-at:${branch}`)}&state=open&per_page=100`);
  if (listed.status !== 200 || !Array.isArray(listed.data) || listed.data.length > 1) throw new Error("exactly one tap PR is required");
  if (!listed.data.length) throw new TapPendingError("tap PR is pending");
  const number = listed.data[0].number;
  if (!Number.isSafeInteger(number) || number < 1) throw new Error("invalid tap PR number");
  const path = `/repos/${TAP}/pulls/${number}`;
  const pull = await gh(token, "GET", path);
  const pr = pull.data;
  if (pull.status !== 200 || pr?.state !== "open" || pr.draft !== false ||
      pr.head?.repo?.full_name !== TAP || pr.head?.ref !== branch ||
      pr.base?.repo?.full_name !== TAP || pr.base?.ref !== base ||
      !/^[a-f0-9]{40}$/.test(pr.head?.sha ?? "")) throw new Error("tap PR identity mismatch");
  const sha = pr.head.sha;
  const files = await gh(token, "GET", `${path}/files?per_page=100`);
  if (files.status !== 200 || !Array.isArray(files.data) || files.data.length !== 1 ||
      files.data[0].filename !== FORMULA_PATH || !["added", "modified"].includes(files.data[0].status)) {
    throw new Error("tap PR must change only the formula");
  }
  const candidate = await readFormula(token, sha);
  if (!candidate || normalize(candidate.text) !== normalize(formula)) throw new Error("tap formula checksum mismatch");
  // Never treat an empty or partial check listing as a green gate. GitHub also
  // enforces the tap's own required checks and review policy at the merge API.
  const checks = await gh(token, "GET", `/repos/${TAP}/commits/${sha}/check-runs?per_page=100&filter=latest`);
  const statuses = await gh(token, "GET", `/repos/${TAP}/commits/${sha}/status?per_page=100`);
  if (checks.status !== 200 || !Number.isSafeInteger(checks.data?.total_count) ||
      checks.data.total_count < 0 || checks.data.total_count >= 100 ||
      checks.data.check_runs?.length !== checks.data.total_count ||
      checks.data.check_runs.some(c => c.head_sha !== sha || (c.status === "completed" && c.conclusion !== "success")) ||
      statuses.status !== 200 || !Number.isSafeInteger(statuses.data?.total_count) ||
      statuses.data.total_count < 0 || statuses.data.total_count >= 100 || statuses.data.statuses?.length !== statuses.data.total_count ||
      statuses.data.statuses.some(s => !["success", "pending"].includes(s.state))) throw new Error("tap checks are incomplete or not green");
  if (!checks.data.total_count || checks.data.check_runs.some(c => c.status !== "completed") ||
      statuses.data.statuses.some(s => s.state === "pending")) throw new TapPendingError("tap checks are pending");
  const fresh = await gh(token, "GET", path);
  if (fresh.status !== 200 || fresh.data?.head?.sha !== sha || fresh.data?.state !== "open" ||
      fresh.data?.base?.sha !== pr.base.sha || fresh.data?.draft !== false) throw new Error("tap PR changed during verification");
  if (!apply) return { state: "verified", number, sha };
  const merged = await gh(token, "PUT", `${path}/merge`, { sha, merge_method: "merge" });
  if (merged.status !== 200 || merged.data?.merged !== true) throw new Error("tap merge refused by repository policy");
  const installed = await readFormula(token, base);
  if (!installed || normalize(installed.text) !== normalize(formula)) throw new Error("merged tap formula mismatch");
  return { state: "merged", number, sha };
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
  if (existing.status !== 200 && existing.status !== 404) throw githubError("read branch", existing.status, existing.data);
  const baseRef = await gh(token, "GET", `/repos/${TAP}/git/ref/heads/${encodeURIComponent(base)}`);
  if (baseRef.status !== 200 || !/^[a-f0-9]{40}$/.test(baseRef.data?.object?.sha ?? "")) throw githubError("read default branch", baseRef.status, baseRef.data);
  // A retry after branch creation but before the formula commit is safe when
  // the branch is still the exact base, with no unrelated work to overwrite.
  if (existing.status === 200) {
    if (!/^[a-f0-9]{40}$/.test(existing.data?.object?.sha ?? "")) throw new Error("invalid tap branch head");
    return { sha: existing.data.object.sha, atBase: existing.data.object.sha === baseRef.data.object.sha };
  }
  const created = await gh(token, "POST", `/repos/${TAP}/git/refs`, {
    ref: `refs/heads/${branch}`,
    sha: baseRef.data.object.sha,
  });
  if (created.status !== 201) throw githubError("create branch", created.status, created.data);
  return { sha: baseRef.data.object.sha, atBase: true };
}

async function writeFormula(token, branch, version, formula, parent) {
  const commit = await gh(token, "GET", `/repos/${TAP}/git/commits/${parent}`);
  if (commit.status !== 200 || !/^[a-f0-9]{40}$/.test(commit.data?.tree?.sha ?? "")) throw new Error("tap parent tree lookup failed");
  const tree = await gh(token, "POST", `/repos/${TAP}/git/trees`, { base_tree: commit.data.tree.sha,
    tree: [{ path: FORMULA_PATH, mode: "100644", type: "blob", content: formula }] });
  if (tree.status !== 201 || !/^[a-f0-9]{40}$/.test(tree.data?.sha ?? "")) throw new Error("tap formula tree creation failed");
  const candidate = await gh(token, "POST", `/repos/${TAP}/git/commits`, {
    message: `aeon-agentd ${version}\n\nInstall the signed, notarized darwin release bytes from SHA256SUMS.\n`,
    tree: tree.data.sha, parents: [parent],
  });
  if (candidate.status !== 201 || !/^[a-f0-9]{40}$/.test(candidate.data?.sha ?? "")) throw new Error("tap formula commit creation failed");
  // The new commit has exactly the observed branch tip as its parent. A
  // concurrent advance makes this non-fast-forward and GitHub must reject it.
  const res = await gh(token, "PATCH", `/repos/${TAP}/git/refs/heads/${encodeURIComponent(branch)}`, { sha: candidate.data.sha, force: false });
  if (res.status !== 200 || res.data?.object?.sha !== candidate.data.sha) throw new Error("tap branch changed during formula update");
}

async function ensurePull(token, base, branch, version) {
  const listed = await gh(token, "GET", `/repos/${TAP}/pulls?head=${encodeURIComponent(`inspr-at:${branch}`)}&state=open&per_page=20`);
  if (listed.status !== 200 || !Array.isArray(listed.data)) throw githubError("list pull requests", listed.status, listed.data);
  if (listed.data.length > 0) {
    const url = listed.data[0].html_url || listed.data[0].url;
    console.log(/^https:\/\/github\.com\/inspr-at\/homebrew-tap\/pull\/\d+$/.test(url ?? "") ? `homebrew tap pull request: ${url}` : "homebrew tap pull request already open");
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
  if (opened.status !== 201 || !/^https:\/\/github\.com\/inspr-at\/homebrew-tap\/pull\/\d+$/.test(opened.data?.html_url ?? "")) throw githubError("open pull request", opened.status, opened.data);
  console.log(`homebrew tap pull request: ${opened.data.html_url}`);
}

export async function bumpHomebrewTap(env = process.env, verifiedChecksums) {
  const appId = String(env.HOMEBREW_TAP_APP_ID || "").trim();
  const key = String(env.HOMEBREW_TAP_APP_KEY || "");
  if (!appId || !key.trim()) {
    console.log("homebrew tap bump skipped: app secrets absent");
    return;
  }
  if (!/^[0-9]+$/.test(appId)) throw new Error("HOMEBREW_TAP_APP_ID is not numeric");
  const version = String(env.VERSION || "").trim();
  if (!validCalendarVersion(version)) throw new Error("VERSION is not an inspr-calendar-v2 coordinate");
  const checksums = verifiedChecksums === undefined ? await fetchPublishedChecksums(version) : verifiedChecksums.text;
  if (verifiedChecksums !== undefined && (!/^[a-f0-9]{64}$/.test(verifiedChecksums.sha256 ?? "") ||
      typeof checksums !== "string" || sha256(checksums) !== verifiedChecksums.sha256)) throw new Error("verified checksum bytes mismatch");
  const formula = renderFormula(version, checksums);
  const token = await installationToken(appId, key);
  try {
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
    refuseTapDowngrade(current, version);
    const branch = `aeon-agentd-v${version}`;
    const head = await ensureBranch(token, base, branch);
    const onBranch = await readFormula(token, head.sha);
    if (!head.atBase && (!onBranch || normalize(onBranch.text) !== normalize(formula))) throw new Error("existing tap branch checksum mismatch");
    if (!onBranch || normalize(onBranch.text) !== normalize(formula)) {
      refuseTapDowngrade(onBranch, version);
      await writeFormula(token, branch, version, formula, head.sha);
    }
    await ensurePull(token, base, branch, version);
  } finally { await revokeInstallationToken(token); }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  // Preserve the harmless unconfigured skip; otherwise only trusted main code
  // may receive App credentials. No tag checkout or tag-controlled script runs.
  (async () => {
    if (process.env.HOMEBREW_TAP_APP_ID && process.env.HOMEBREW_TAP_APP_KEY) {
      const { validateDispatcher, verifyProtectedEnvironment } = await import("./verify-live-policy.mjs");
      validateDispatcher(process.env, "homebrew-tap");
      await verifyProtectedEnvironment(process.env.WORKFLOW_TOKEN, "homebrew-tap");
    }
    await bumpHomebrewTap();
  })().catch(() => {
    console.error("homebrew tap bump failed; coordinator must inspect trusted release and policy evidence");
    process.exitCode = 1;
  });
}
