// SPDX-License-Identifier: AGPL-3.0-only
// Install this bootstrap from an independently reviewed main revision. Run it
// outside Actions against a dedicated bare mirror, never a worker's checkout.
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { spawnSync } from "node:child_process";
import { pathToFileURL } from "node:url";
import { statusTarget, statusWriter } from "./post-cross-family-gate.mjs";

export async function withPendingStatus({ eventName, event, write, publish, run }) {
  const target = statusTarget(eventName, event);
  // The independently installed bootstrap revokes old success BEFORE a fetch,
  // extraction or subprocess can fail. A crash thereafter leaves pending.
  const body = (state, description) => ({ context: "gate/cross-family", state, description });
  if (write) await publish(target, body("pending", "Refreshing trusted main gate policy"));
  try {
    return await run();
  } catch (error) {
    if (write) await publish(target, body("failure", "Trusted main gate bootstrap failed closed"));
    throw error;
  }
}

function gitRead(cwd, args) {
  const result = spawnSync("git", args, { cwd, encoding: "utf8", timeout: 30000, maxBuffer: 16 * 1024 * 1024 });
  if (result.status !== 0 || result.error) throw new Error("trusted gate Git operation failed");
  return result.stdout;
}

export function extractPolicy(cwd, main) {
  if (!/^[a-f0-9]{40}$/.test(main ?? "")) throw new Error("invalid trusted main SHA");
  const files = ["scripts/cross-family-gate.mjs", "scripts/post-cross-family-gate.mjs", ".github/gate-posters.json"];
  // Read ALL required blobs before creating an executable snapshot. Missing
  // bootstrap policy fails closed; never fall back to the PR or working tree.
  const blobs = files.map((file) => gitRead(cwd, ["show", `${main}:${file}`]));
  const dir = mkdtempSync(join(tmpdir(), "aeon-trusted-gate-"));
  files.forEach((file, i) => writeFileSync(join(dir, file.split("/").at(-1)), blobs[i], { mode: 0o600 }));
  return dir;
}

export function refreshMain(cwd) {
  if (gitRead(cwd, ["rev-parse", "--is-bare-repository"]).trim() !== "true") {
    throw new Error("external gate requires a dedicated bare mirror");
  }
  const origin = gitRead(cwd, ["remote", "get-url", "origin"]).trim();
  if (origin !== "https://github.com/inspr-at/paimos.git") throw new Error("untrusted gate origin");
  gitRead(cwd, ["fetch", "--no-tags", "origin", "refs/heads/main:refs/remotes/origin/main"]);
  return gitRead(cwd, ["rev-parse", "refs/remotes/origin/main^{commit}"]).trim();
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const [mirror, eventName, eventPath, flag] = process.argv.slice(2);
    if (!eventPath || (flag !== undefined && flag !== "--write") || process.argv.length > 6 || process.env.GITHUB_ACTIONS) {
      throw new Error("usage outside Actions: trusted-cross-family-poster.mjs BARE_MIRROR EVENT_NAME EVENT_PATH [--write]");
    }
    const absoluteEventPath = resolve(eventPath);
    const result = await withPendingStatus({ eventName, event: JSON.parse(readFileSync(absoluteEventPath, "utf8")),
      write: flag === "--write", publish: flag ? statusWriter({ token: process.env.GATE_APP_TOKEN }) : undefined,
      run: () => {
        const main = refreshMain(mirror);
        const dir = extractPolicy(mirror, main);
        // Candidate commits are fetched only as Git objects by createGit; no PR
        // workflow, module, action, package install, hook or build is executed.
        const child = spawnSync(process.execPath, [join(dir, "post-cross-family-gate.mjs"),
          join(dir, "gate-posters.json"), eventName, absoluteEventPath, ...(flag ? [flag] : [])],
        { cwd: mirror, env: process.env, stdio: "inherit", timeout: 600000 });
        if (child.error || child.status !== 0) throw new Error("trusted poster did not approve");
        return child.status;
      } });
    process.exitCode = result;
  } catch {
    console.error("trusted gate bootstrap failed closed; main policy and dedicated mirror are required");
    process.exitCode = 1;
  }
}
