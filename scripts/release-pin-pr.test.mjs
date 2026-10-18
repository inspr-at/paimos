// SPDX-License-Identifier: AGPL-3.0-only
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { attestationArgs, PIN_PATH, planPin, proposePin, recordProposal, SOURCE, TARGET } from "./release-pin-pr.mjs";

const sourceCommit = "a".repeat(40), tagSHA = "b".repeat(40), baseSHA = "c".repeat(40), headSHA = "d".repeat(40);
const version = "261001120000.0.0", oldVersion = "260930120000.0.0";
const digest = `sha256:${"e".repeat(64)}`, oldDigest = `sha256:${"f".repeat(64)}`;
const original = `# preserved header\n{\n  aeon = {\n    image = "ghcr.io/inspr-at/aeon:${oldVersion}@${oldDigest}"; # preserved comment\n  };\n  unrelated = "unchanged";\n}\n`;
const plan = () => planPin(original, version, digest);
const blob = text => createHash("sha1").update(`blob ${Buffer.byteLength(text)}\0`).update(text).digest("hex");
const env = () => ({
  VERSION: version, DIGEST: digest, GITHUB_SHA: sourceCommit, GITHUB_REPOSITORY: SOURCE,
  GITHUB_EVENT_NAME: "push", GITHUB_REF: `refs/tags/v${version}`, GH_TOKEN: "fixture-source-auth",
  AEON_PIN_APP_ID: "123", AEON_PIN_APP_KEY: "fixture-key-not-parsed", AEON_PIN_BOT_ENABLED: "true",
});
function fixture(overrides = {}) {
  const calls = [];
  const tagRef = { ref: `refs/tags/v${version}`, object: { type: "tag", sha: tagSHA } };
  const tag = { sha: tagSHA, tag: `v${version}`, object: { type: "commit", sha: sourceCommit } };
  const sourceMain = { ref: "refs/heads/main", object: { type: "commit", sha: sourceCommit } };
  const ancestry = { status: "identical", base_commit: { sha: sourceCommit }, merge_base_commit: { sha: sourceCommit } };
  const base = { ref: "refs/heads/main", object: { type: "commit", sha: baseSHA } };
  const content = { type: "file", encoding: "base64", content: Buffer.from(original).toString("base64"), sha: blob(original) };
  const comparison = {
    status: "ahead", base_commit: { sha: baseSHA }, merge_base_commit: { sha: baseSHA }, total_commits: 1,
    commits: [{ sha: headSHA, parents: [{ sha: baseSHA }] }],
    files: [{ filename: PIN_PATH, status: "modified", additions: 1, deletions: 1, changes: 2, sha: blob(plan().text) }],
  };
  const branch = `aeon-pin-v${version}`;
  const pull = {
    state: "open", draft: true, number: 42, html_url: `https://github.com/${TARGET}/pull/42`, created_at: "2026-10-01T12:00:10Z",
    base: { ref: "main", repo: { full_name: TARGET } }, head: { ref: branch, sha: headSHA, repo: { full_name: TARGET } },
  };
  const data = { tagRef, tag, sourceMain, ancestry, base, content, comparison, pull, ...overrides };
  const dependencies = {
    jwt: () => "fixture-jwt",
    verify: async inputs => { calls.push({ method: "VERIFY", args: attestationArgs(inputs.VERSION, inputs.DIGEST, inputs.GITHUB_SHA) }); if (overrides.unattested) throw new Error("pin bot: image attestation verification failed"); },
    request: async (token, method, path, body) => {
      calls.push({ token, method, path, body });
      if (overrides.apiFailure?.(method, path)) return { status: 503, data: {} };
      if (path === "/installation/token" && method === "DELETE") return { status: 204, data: null };
      if (path === `/repos/${SOURCE}/git/ref/tags/v${version}`) return { status: 200, data: data.tagRef };
      if (path === `/repos/${SOURCE}/git/tags/${tagSHA}`) return { status: 200, data: data.tag };
      if (path === `/repos/${SOURCE}/git/ref/heads/main`) return { status: 200, data: data.sourceMain };
      if (path.startsWith(`/repos/${SOURCE}/compare/`)) return { status: 200, data: data.ancestry };
      if (path === `/repos/${TARGET}/installation`) return { status: 200, data: { id: 7 } };
      if (path === "/app/installations/7/access_tokens") return { status: 201, data: { token: "fixture-installation-auth", permissions: overrides.permissions ?? body.permissions } };
      if (path === `/repos/${TARGET}/git/ref/heads/main`) return { status: 200, data: data.base };
      if (path.startsWith(`/repos/${TARGET}/contents/`)) {
        if (method === "GET") return { status: 200, data: data.content };
        return { status: 200, data: { commit: { sha: headSHA } } };
      }
      if (path === `/repos/${TARGET}/git/ref/heads/${branch}`) return overrides.existingBranch
        ? { status: 200, data: { ref: `refs/heads/${branch}`, object: { type: "commit", sha: headSHA } } }
        : { status: 404, data: {} };
      if (path === `/repos/${TARGET}/git/refs`) return { status: 201, data: { ref: body.ref, object: { sha: body.sha } } };
      if (path.startsWith(`/repos/${TARGET}/compare/`)) return { status: 200, data: data.comparison };
      if (path.startsWith(`/repos/${TARGET}/pulls?`)) return { status: 200, data: overrides.pulls ?? [] };
      if (path === `/repos/${TARGET}/pulls` && method === "POST") return { status: 201, data: data.pull };
      throw new Error("unexpected fixture request");
    },
  };
  return { calls, data, dependencies };
}
const changes = f => f.calls.filter(call => ["POST", "PUT", "PATCH"].includes(call.method) && call.path.startsWith(`/repos/${TARGET}/`) && !call.path.endsWith("/installation"));

test("pin diff changes one line, preserving comments, whitespace, newline and unrelated bytes", () => {
  const result = plan();
  assert.equal(result.text, original.replace(`${oldVersion}@${oldDigest}`, `${version}@${digest}`));
  assert.equal(result.previous, `ghcr.io/inspr-at/aeon:${oldVersion}@${oldDigest}`);
  assert.equal(result.patch.split("\n").filter(line => /^[-+][^-+]/.test(line)).length, 2);
  const crlf = planPin(original.replaceAll("\n", "\r\n"), version, digest);
  assert.equal(crlf.text, result.text.replaceAll("\n", "\r\n"));
  assert.equal(planPin(result.text, version, digest).changed, false);
});
for (const [name, text, nextVersion, nextDigest] of [
  ["absent", "{}", version, digest], ["duplicate", original + original, version, digest],
  ["floating", original.replace(`@${oldDigest}`, ""), version, digest],
  ["comment instead of pin", original.replace("    image =", "    # image ="), version, digest],
  ["invalid old date", original.replace(oldVersion, "260231120000.0.0"), version, digest],
  ["invalid new date", original, "260231120000.0.0", digest],
  ["downgrade", original, "260929120000.0.0", digest],
  ["same version changed digest", original, oldVersion, digest],
  ["bad digest", original, version, "latest"],
]) test(`rejects ${name}`, () => assert.throws(() => planPin(text, nextVersion, nextDigest), /pin bot:/));

test("default dry run verifies source then reads target with a restricted token, creates no branch or PR", async () => {
  const f = fixture();
  const result = await proposePin(env(), {}, f.dependencies);
  assert.equal(result.status, "dry-run");
  assert.equal(result.patch, plan().patch);
  assert.deepEqual(changes(f), []);
  assert.deepEqual(f.calls.find(call => call.path === "/app/installations/7/access_tokens").body, { repositories: ["nixcfg"], permissions: { contents: "read" } });
  assert.ok(f.calls.findIndex(call => call.method === "VERIFY") < f.calls.findIndex(call => call.path === `/repos/${TARGET}/installation`));
  assert.equal(f.calls.at(-1).path, "/installation/token");
  assert.equal(f.calls.at(-1).method, "DELETE");
});

test("write creates only a pin branch and a draft PR, recording rollback and verification evidence", async () => {
  const f = fixture();
  const result = await proposePin(env(), { write: true }, f.dependencies);
  assert.equal(result.status, "proposed");
  assert.equal(result.url, `https://github.com/${TARGET}/pull/42`);
  assert.deepEqual(changes(f).map(call => [call.method, call.path]), [
    ["POST", `/repos/${TARGET}/git/refs`], ["PUT", `/repos/${TARGET}/contents/${PIN_PATH}`], ["POST", `/repos/${TARGET}/pulls`],
  ]);
  assert.deepEqual(changes(f)[0].body, { ref: `refs/heads/aeon-pin-v${version}`, sha: baseSHA });
  const update = changes(f)[1];
  assert.equal(update.body.branch, `aeon-pin-v${version}`);
  assert.equal(update.body.sha, blob(original));
  assert.equal(Buffer.from(update.body.content, "base64").toString(), plan().text);
  const pull = changes(f)[2];
  assert.equal(pull.body.draft, true);
  assert.equal(pull.body.base, "main");
  for (const bound of [sourceCommit, baseSHA, digest, plan().previous, "database backup", "neither merges nor deploys"]) assert.ok(pull.body.body.includes(bound));
  assert.deepEqual(f.calls.find(call => call.path === "/app/installations/7/access_tokens").body.permissions, { contents: "write", pull_requests: "write" });
  assert.equal(f.calls.at(-1).method, "DELETE");
  assert.equal(f.calls.filter(call => call.path?.includes("/merge") || call.path?.includes("/reviews")).length, 0);
});

for (const [name, override] of [
  ["lightweight tag", { tagRef: { ref: `refs/tags/v${version}`, object: { type: "commit", sha: sourceCommit } } }],
  ["different tagged commit", { tag: { sha: tagSHA, tag: `v${version}`, object: { type: "commit", sha: headSHA } } }],
  ["off-main tag", { ancestry: { status: "diverged", base_commit: { sha: sourceCommit }, merge_base_commit: { sha: headSHA } } }],
  ["unattested image", { unattested: true }],
  ["source API failure", { apiFailure: (_method, path) => path.startsWith(`/repos/${SOURCE}/`) }],
]) test(`${name} cannot authenticate to or change nixcfg`, async () => {
  const f = fixture(override);
  await assert.rejects(proposePin(env(), { write: true }, f.dependencies));
  assert.equal(f.calls.some(call => call.path?.startsWith(`/repos/${TARGET}/`)), false);
  assert.deepEqual(changes(f), []);
});

for (const [name, mutate] of [
  ["PR event", input => input.GITHUB_EVENT_NAME = "pull_request"],
  ["foreign repo", input => input.GITHUB_REPOSITORY = "augmentoring-team/agm-nixcfg"],
  ["branch ref", input => input.GITHUB_REF = "refs/heads/main"],
  ["invalid digest", input => input.DIGEST = "latest"],
  ["disabled write", input => input.AEON_PIN_BOT_ENABLED = "false"],
]) test(`${name} is refused before network access`, async () => {
  const f = fixture(), input = env(); mutate(input);
  await assert.rejects(proposePin(input, { write: true }, f.dependencies), /pin bot:/);
  assert.equal(f.calls.length, 0);
});

test("missing credentials hold a dry run and fail an enabled write without target writes", async () => {
  const input = { ...env(), AEON_PIN_APP_KEY: "" };
  const f = fixture();
  assert.equal((await proposePin(input, {}, f.dependencies)).status, "held");
  await assert.rejects(proposePin(input, { write: true }, f.dependencies), /credentials are missing/);
  assert.deepEqual(changes(f), []);
});

test("an exact existing draft is reused without changing the branch or PR", async () => {
  const f = fixture({ existingBranch: true });
  f.dependencies.request = ((request) => async (...args) => {
    const result = await request(...args);
    if (args[2].startsWith(`/repos/${TARGET}/pulls?`)) result.data = [f.data.pull];
    return result;
  })(f.dependencies.request);
  assert.equal((await proposePin(env(), { write: true }, f.dependencies)).status, "proposed");
  assert.deepEqual(changes(f), []);
});

for (const [name, mutate] of [
  ["extra file", data => data.comparison.files.push({ filename: "foreign.nix" })],
  ["extra line", data => data.comparison.files[0].additions = 2],
  ["changed content", data => data.comparison.files[0].sha = sourceCommit],
  ["changed base", data => data.comparison.merge_base_commit.sha = headSHA],
  ["extra commit", data => data.comparison.total_commits = 2],
  ["merge commit", data => data.comparison.commits[0].parents.push({ sha: sourceCommit })],
]) test(`existing branch with ${name} is never overwritten or proposed`, async () => {
  const f = fixture({ existingBranch: true }); mutate(f.data);
  await assert.rejects(proposePin(env(), { write: true }, f.dependencies), /exact one-line proposal/);
  assert.deepEqual(changes(f), []);
  assert.equal(f.calls.at(-1).method, "DELETE");
});

for (const [name, mutate] of [
  ["closed", pull => pull.state = "closed"], ["made ready", pull => pull.draft = false],
  ["wrong target", pull => pull.base.repo.full_name = "other/repo"], ["changed head", pull => pull.head.sha = sourceCommit],
]) test(`existing PR ${name} remains untouched and fails closed`, async () => {
  const f = fixture({ existingBranch: true }); mutate(f.data.pull);
  const request = f.dependencies.request;
  f.dependencies.request = async (...args) => {
    const result = await request(...args);
    if (args[2].startsWith(`/repos/${TARGET}/pulls?`)) result.data = [f.data.pull];
    return result;
  };
  await assert.rejects(proposePin(env(), { write: true }, f.dependencies), /identity or draft state/);
  assert.deepEqual(changes(f), []);
});

test("token overprivilege and target API failures revoke credentials and leave no PR", async () => {
  for (const override of [
    { permissions: { contents: "write", pull_requests: "write", administration: "write" } },
    { apiFailure: (_method, path) => path.startsWith(`/repos/${TARGET}/contents/`) },
    { apiFailure: (_method, path) => path.startsWith(`/repos/${TARGET}/compare/`) },
  ]) {
    const f = fixture(override);
    await assert.rejects(proposePin(env(), { write: true }, f.dependencies));
    assert.equal(changes(f).some(call => call.path.endsWith("/pulls")), false);
    assert.equal(f.calls.at(-1).path, "/installation/token");
  }
});

test("snapshot dry run cannot mint credentials or be used as a write base", async () => {
  const directory = mkdtempSync(join(tmpdir(), "aeon-pin-fixture-"));
  const pinFile = join(directory, "snapshot.nix"); writeFileSync(pinFile, original);
  const f = fixture();
  assert.equal((await proposePin(env(), { pinFile }, f.dependencies)).patch, plan().patch);
  assert.equal(f.calls.some(call => call.path?.startsWith(`/repos/${TARGET}/`)), false);
  await assert.rejects(proposePin(env(), { pinFile, write: true }, f.dependencies), /live base/);
});

test("attestation args bind the image, signer workflow and exact source", () => {
  assert.deepEqual(attestationArgs(version, digest, sourceCommit), [
    "attestation", "verify", `oci://ghcr.io/inspr-at/aeon@${digest}`, "--repo", "inspr-at/paimos",
    "--signer-workflow", "inspr-at/paimos/.github/workflows/release.yml",
    "--signer-digest", sourceCommit, "--source-ref", `refs/tags/v${version}`,
    "--source-digest", sourceCommit, "--deny-self-hosted-runners",
  ]);
});

test("attestation args never combine mutually exclusive gh identity flags", () => {
  const identityFlags = ["--cert-identity", "--cert-identity-regex", "--signer-repo", "--signer-workflow"];
  const args = attestationArgs(version, digest, sourceCommit);
  assert.deepEqual(args.filter(arg => identityFlags.includes(arg)), ["--signer-workflow"]);
});

test("real CLI verifier binds the image and provenance; its output never enters logs", async (t) => {
  const directory = mkdtempSync(join(tmpdir(), "aeon-pin-verifier-"));
  const log = join(directory, "args");
  writeFileSync(join(directory, "gh"), `#!/bin/sh\nprintf '%s\\n' "$@" > '${log}'\necho untrusted-verifier-output >&2\nexit 1\n`, { mode: 0o700 });
  const previousPath = process.env.PATH;
  process.env.PATH = `${directory}:${previousPath}`;
  t.after(() => { process.env.PATH = previousPath; });
  const f = fixture(); delete f.dependencies.verify;
  await assert.rejects(proposePin(env(), { write: true }, f.dependencies), /image attestation verification failed/);
  assert.deepEqual(readFileSync(log, "utf8").trim().split("\n"), [
    "attestation", "verify", `oci://ghcr.io/inspr-at/aeon@${digest}`, "--repo", "inspr-at/paimos",
    "--signer-workflow", "inspr-at/paimos/.github/workflows/release.yml",
    "--signer-digest", sourceCommit, "--source-ref", `refs/tags/v${version}`,
    "--source-digest", sourceCommit, "--deny-self-hosted-runners",
  ]);
  assert.equal(f.calls.some(call => call.path?.startsWith(`/repos/${TARGET}/`)), false);
});

test("tag and rehearsal keep every Buildx setup pinned and immediately checked", () => {
  const release = readFileSync(new URL("../.github/workflows/release.yml", import.meta.url), "utf8");
  const rehearsal = readFileSync(new URL("../.github/workflows/release-image-check.yml", import.meta.url), "utf8");
  const pins = (workflow) => workflow.match(/^  AEON_BUILD(?:X_VERSION|X_SHA256_AMD64|X_SHA256_ARM64|KIT_VERSION|KIT_IMAGE): .+$/gm);
  assert.equal(pins(release)?.length, 5);
  assert.deepEqual(pins(release), pins(rehearsal));
  const setup = /^      - uses: docker\/setup-buildx-action@[a-f0-9]{40} # v4\n        with:\n          version: \$\{\{ env\.AEON_BUILDX_VERSION \}\}\n          cache-binary: false\n          driver: docker-container\n          driver-opts: image=\$\{\{ env\.AEON_BUILDKIT_IMAGE \}\}\n      - name: Assert pinned Buildx and BuildKit versions\n/gm;
  for (const [workflow, count] of [[release, 2], [rehearsal, 1]]) {
    assert.equal([...workflow.matchAll(setup)].length, count);
    assert.equal([...workflow.matchAll(/uses: docker\/setup-buildx-action@/g)].length, count);
  }
  const platform = release.split("\n  image-platform:\n")[1].split("\n  image:\n")[0];
  const dry = rehearsal.split("\n  image-dry-run:\n")[1].split("\n  agentd-rehearsal:\n")[0];
  assert.match(platform, /^    timeout-minutes: 45$/m);
  assert.match(dry, /^    timeout-minutes: 45$/m);
});

test("release records the digest before the non-blocking pin proposal and preserves asset dependencies", () => {
  const workflow = readFileSync(new URL("../.github/workflows/release.yml", import.meta.url), "utf8");
  const image = workflow.split("\n  image:\n")[1].split("\n  assets:\n")[0];
  const record = image.indexOf("      - name: Record pushed digest\n");
  const proposal = image.indexOf("      - name: Propose verified nixcfg deployment pin\n");
  assert.ok(record >= 0 && proposal > record);
  assert.match(image.slice(proposal), /^        continue-on-error: true$/m);
  assert.match(image, /^      digest: \$\{\{ steps.push.outputs.digest \}\}$/m);
  assert.match(workflow.split("\n  assets:\n")[1], /^    needs: \[agentd-darwin, image\]$/m);
});

test("release pin shell reports failures in dry-run and write modes without hiding the failed outcome", () => {
  const workflow = readFileSync(new URL("../.github/workflows/release.yml", import.meta.url), "utf8");
  const proposal = workflow.split("      - name: Propose verified nixcfg deployment pin\n")[1].split("\n  assets:\n")[0];
  const script = proposal.split("        run: |\n")[1].split("\n").map(line => line.replace(/^          /, "")).join("\n");
  const directory = mkdtempSync(join(tmpdir(), "aeon-pin-workflow-"));
  const argsLog = join(directory, "args"), summary = join(directory, "summary");
  writeFileSync(join(directory, "node"), '#!/bin/sh\nprintf "%s\\n" "$@" > "$ARGS_LOG"\nexit "$PROPOSAL_EXIT"\n', { mode: 0o700 });
  for (const enabled of ["", "true"]) {
    for (const exit of [0, 1]) {
      writeFileSync(summary, "");
      const result = spawnSync("/bin/bash", ["-c", script], {
        encoding: "utf8",
        env: { PATH: directory, ARGS_LOG: argsLog, PROPOSAL_EXIT: String(exit), AEON_PIN_BOT_ENABLED: enabled, GITHUB_STEP_SUMMARY: summary },
      });
      assert.equal(result.status, exit, result.stderr);
      assert.deepEqual(readFileSync(argsLog, "utf8").trim().split("\n"), ["scripts/release-pin-pr.mjs", ...(enabled === "true" ? ["--write"] : [])]);
      if (exit) {
        assert.match(result.stdout, /::warning::Deployment pin proposal failed/);
        assert.match(readFileSync(summary, "utf8"), /Deployment pin proposal failed; release assets will still be built\./);
      } else {
        assert.equal(result.stdout, "");
        assert.equal(readFileSync(summary, "utf8"), "");
      }
    }
  }
});

test("recorded release evidence carries the verified digest and proposal without private content", () => {
  const directory = mkdtempSync(join(tmpdir(), "aeon-pin-record-"));
  const output = join(directory, "output"), summary = join(directory, "summary");
  const evidence = `Verified index: ghcr.io/inspr-at/aeon@${digest}\nDraft pin PR: https://github.com/${TARGET}/pull/42`;
  recordProposal({ status: "proposed", url: `https://github.com/${TARGET}/pull/42`, evidence }, { GITHUB_OUTPUT: output, GITHUB_STEP_SUMMARY: summary });
  assert.ok(readFileSync(output, "utf8").includes(`pin_pr=https://github.com/${TARGET}/pull/42`));
  assert.ok(readFileSync(output, "utf8").includes(evidence));
  assert.ok(readFileSync(summary, "utf8").includes(evidence));
});

test("the production HTTP adapter confines requests to GitHub and redacts transport errors", async (t) => {
  const f = fixture(), request = f.dependencies.request;
  delete f.dependencies.request;
  t.mock.method(globalThis, "fetch", async (url, options) => {
    assert.ok(url.startsWith("https://api.github.com/"));
    assert.equal(options.redirect, "error");
    const result = await request(options.headers.Authorization.slice(7), options.method,
      url.slice("https://api.github.com".length), options.body ? JSON.parse(options.body) : undefined);
    return result.status === 204 ? new Response(null, { status: 204 }) : Response.json(result.data, { status: result.status });
  });
  assert.equal((await proposePin(env(), { write: true }, f.dependencies)).status, "proposed");
  t.mock.method(globalThis, "fetch", async () => { throw new Error("untrusted raw transport response"); });
  await assert.rejects(proposePin(env(), { write: true }, f.dependencies), error => error.message === "pin bot: GitHub request failed");
});

test("a rejected App key is reported without echoing its parser error", async () => {
  const f = fixture();
  f.dependencies.jwt = () => { throw new Error("untrusted key parser response"); };
  await assert.rejects(proposePin(env(), { write: true }, f.dependencies), error => error.message === "pin bot: pin App key rejected");
  assert.deepEqual(changes(f), []);
});

test("proposal timing binds the push observation to GitHub's PR creation timestamp", async () => {
  const f = fixture();
  const result = await proposePin({ ...env(), INDEX_PUSHED_AT: "2026-10-01T12:00:00Z" }, { write: true }, f.dependencies);
  assert.ok(result.evidence.includes("elapsed: 10s (target <=30s)"));
});

test("missing requested token permissions and malformed pin blobs fail before branch writes", async () => {
  for (const override of [{ permissions: {} }, { content: { type: "symlink" } }]) {
    const f = fixture(override);
    await assert.rejects(proposePin(env(), { write: true }, f.dependencies));
    assert.deepEqual(changes(f), []);
    assert.equal(f.calls.at(-1).method, "DELETE");
  }
});
