// SPDX-License-Identifier: AGPL-3.0-only
import { appendFileSync, readFileSync } from "node:fs";
import { spawnSync } from "node:child_process";
import { pathToFileURL } from "node:url";

const shaPattern = /^[a-f0-9]{40}$/;
function sha(value) {
  if (!shaPattern.test(value ?? "")) throw new Error("missing or invalid commit SHA");
  return value;
}

export function validateConfig(config) {
  if (config?.schema !== 1 || config.base_branch !== "main" ||
      !Array.isArray(config.posters) || config.posters.length === 0 ||
      config.posters.some((poster) => !/^[a-z0-9][a-z0-9-]*(?:\[bot\])?$/i.test(poster.login ?? "") ||
        !Number.isSafeInteger(poster.id) || poster.id < 1)) {
    throw new Error("invalid gate poster configuration");
  }
  return config;
}

export function trustedVerdict(statuses, posters) {
  if (!Array.isArray(statuses)) throw new Error("invalid commit statuses response");
  // Ignore untrusted posts entirely, including an attempted denial of service.
  // A later trusted failure/pending status revokes an earlier trusted success.
  return statuses.filter((status) => status.context === "gate/verdict" &&
    posters.some((poster) => poster.id === status.creator?.id &&
      poster.login.toLowerCase() === status.creator?.login?.toLowerCase()))
    .sort((a, b) => b.id - a.id)[0];
}

export function createGit(cwd = process.cwd()) {
  function run(args, allowed = [0]) {
    const result = spawnSync("git", args, {
      cwd, encoding: "utf8", timeout: 30000, maxBuffer: 16 * 1024 * 1024,
    });
    if (result.error || !allowed.includes(result.status)) {
      // Do not print subprocess stderr: transport diagnostics can contain credentials.
      throw new Error(`git ${args[0]} failed`);
    }
    return { text: result.stdout.trim(), status: result.status };
  }
  return {
    parents: (commit) => run(["show", "-s", "--format=%P", sha(commit)]).text.split(" ").filter(Boolean),
    firstParents: (commit) => run(["rev-list", "--first-parent", sha(commit)]).text.split("\n"),
    tree: (commit) => run(["rev-parse", `${sha(commit)}^{tree}`]).text,
    mergeTree: (first, second) => run(["merge-tree", "--write-tree", sha(first), sha(second)], [0, 1]),
    ensure: (commit) => {
      if (run(["cat-file", "-e", `${sha(commit)}^{commit}`], [0, 1, 128]).status !== 0) {
        run(["fetch", "--no-tags", "origin", sha(commit)]);
      }
    },
  };
}

function unchangedMerge(git, commit, parents) {
  const expected = git.mergeTree(parents[0], parents[1]);
  // A conflict resolution needs a new review, even if it looks innocuous.
  if (expected.status !== 0 || expected.text !== git.tree(commit)) {
    throw new Error(`merge ${commit} changes the reviewed branch diff or resolves conflicts`);
  }
}

function validatePull(pr, repository, config) {
  if (!Number.isSafeInteger(pr?.number) || pr.number < 1 || pr.state !== "open" ||
      pr.base?.ref !== config.base_branch || pr.base?.repo?.full_name !== repository) {
    throw new Error("pull request is not open against the configured repository/main");
  }
}

export async function checkPullRequest({ pr, repository, config, mainSha, git, statuses }) {
  validatePull(pr, repository, config);
  const head = sha(pr.head?.sha);
  git.ensure(head);
  const mainCommits = new Set(git.firstParents(sha(mainSha)));
  let current = head;
  let merges = 0;
  for (; merges <= 100; merges++) {
    const verdict = trustedVerdict(await statuses(current), config.posters);
    if (verdict) {
      if (verdict.state !== "success" ||
          !/^model=([^;\s][^;]*); route=([^;\s][^;]*); review=(\S+)$/.test(verdict.description ?? "")) {
        throw new Error(`PR #${pr.number}: trusted gate/verdict at ${current} is not a valid ok verdict`);
      }
      return { number: pr.number, head, reviewed: current, merges, description: verdict.description };
    }
    const parents = git.parents(current);
    if (parents.length !== 2 || !mainCommits.has(parents[1])) {
      throw new Error(`PR #${pr.number}: no trusted gate/verdict; a new branch commit requires review (${current})`);
    }
    unchangedMerge(git, current, parents);
    current = sha(parents[0]);
  }
  throw new Error(`PR #${pr.number}: too many follow-up merges`);
}

export function resolveMergeGroup({ group, pulls, git, config, repository }) {
  if (group?.base_ref !== `refs/heads/${config.base_branch}` ||
      !group.head_ref?.startsWith(`refs/heads/gh-readonly-queue/${config.base_branch}/`)) {
    throw new Error("merge group is not for the configured main queue");
  }
  const base = sha(group.base_sha);
  let current = sha(group.head_sha);
  git.ensure(base);
  git.ensure(current);
  const selected = [];
  // With merge commits, each first-parent step from group head to group base
  // names exactly one PR head as its second parent. No ref-name guessing or
  // single anchor PR can silently omit another PR in a batched group.
  while (current !== base && selected.length < 100) {
    const parents = git.parents(current);
    if (parents.length !== 2) throw new Error("unresolved merge-group commit (merge commits required)");
    const matches = pulls.filter((pr) => pr.state === "open" && pr.base?.ref === config.base_branch &&
      pr.base?.repo?.full_name === repository && pr.head?.sha === parents[1]);
    if (matches.length === 0) {
      throw new Error("merge group contains an unresolved or changed PR head");
    }
    unchangedMerge(git, current, parents);
    // Several open PRs (including forks) may name the same immutable commit.
    // Check its verdict once, but revalidate every matching PR before passing.
    selected.push(matches);
    current = sha(parents[0]);
  }
  if (current !== base || selected.length === 0) throw new Error("empty or incomplete merge group");
  return [...new Map(selected.reverse().flat().map((pr) => [pr.number, pr])).values()];
}

export function githubAPI({ repository, token, fetchImpl = fetch }) {
  if (!/^[a-zA-Z0-9_.-]+\/[a-zA-Z0-9_.-]+$/.test(repository ?? "") || !token) {
    throw new Error("missing GitHub repository or read token");
  }
  async function get(path) {
    const response = await fetchImpl(`https://api.github.com/repos/${repository}/${path}`, {
      method: "GET", redirect: "error", signal: AbortSignal.timeout(30000),
      headers: {
        Accept: "application/vnd.github+json", Authorization: `Bearer ${token}`,
        "X-GitHub-Api-Version": "2022-11-28",
      },
    });
    if (!response.ok) throw new Error(`GitHub read failed (HTTP ${response.status})`);
    return response.json();
  }
  async function list(path) {
    const all = [];
    for (let page = 1; page <= 100; page++) {
      const items = await get(`${path}${path.includes("?") ? "&" : "?"}per_page=100&page=${page}`);
      if (!Array.isArray(items)) throw new Error("invalid GitHub list response");
      all.push(...items);
      if (items.length < 100) return all;
    }
    throw new Error("GitHub list pagination limit exceeded; gate cannot accept incomplete evidence");
  }
  return {
    pull: (number) => {
      if (!Number.isSafeInteger(number) || number < 1) throw new Error("invalid pull request number");
      return get(`pulls/${number}`);
    },
    pulls: (base) => list(`pulls?state=open&base=${encodeURIComponent(base)}`),
    statuses: (commit) => list(`commits/${sha(commit)}/statuses`),
  };
}

export async function checkEvent({ eventName, event, config, repository, git, api }) {
  validateConfig(config);
  if (event.repository?.full_name !== repository) throw new Error("event repository mismatch");
  let pulls;
  let mainSha;
  if (eventName === "pull_request") {
    const pr = await api.pull(event.pull_request?.number);
    if (pr.head?.sha !== event.pull_request?.head?.sha) {
      throw new Error("pull request changed since this run; run CI on the current head");
    }
    mainSha = sha(event.pull_request.base.sha);
    pulls = [pr];
  } else if (eventName === "merge_group" && event.action === "checks_requested") {
    mainSha = sha(event.merge_group?.base_sha);
    pulls = resolveMergeGroup({ group: event.merge_group, pulls: await api.pulls(config.base_branch), git, config, repository });
  } else {
    throw new Error("gate only accepts pull_request or merge_group checks_requested events");
  }
  git.ensure(mainSha);
  const results = [];
  const checkedHeads = new Map();
  for (const pr of pulls) {
    validatePull(pr, repository, config);
    let result = checkedHeads.get(pr.head.sha);
    if (!result) {
      result = await checkPullRequest({ pr, repository, config, mainSha, git, statuses: api.statuses });
      checkedHeads.set(pr.head.sha, result);
    }
    results.push({ ...result, number: pr.number });
    const latest = await api.pull(pr.number);
    if (latest.state !== "open" || latest.head?.sha !== pr.head.sha ||
        latest.base?.ref !== config.base_branch || latest.base?.repo?.full_name !== repository) {
      throw new Error(`PR #${pr.number} changed during gate verification`);
    }
  }
  return results;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    if (process.argv.length !== 3) throw new Error("usage: node cross-family-gate.mjs CONFIG_PATH");
    const event = JSON.parse(readFileSync(process.env.GITHUB_EVENT_PATH, "utf8"));
    const repository = process.env.GITHUB_REPOSITORY;
    // Only the diagnostic CLI skips main pushes. checkEvent and the external
    // App poster keep rejecting push events and enforcing PR/queue evidence.
    if (process.env.GITHUB_EVENT_NAME === "push" && event.ref === "refs/heads/main" &&
        event.repository?.full_name === repository && /^[a-zA-Z0-9_.-]+\/[a-zA-Z0-9_.-]+$/.test(repository ?? "")) {
      const notice = "gate/policy-preview: main push skipped; review verdicts are checked on PR and merge-group heads (non-required diagnostic)";
      console.log(`::notice::${notice}`);
      if (process.env.GITHUB_STEP_SUMMARY) appendFileSync(process.env.GITHUB_STEP_SUMMARY, `${notice}\n`);
    } else {
      const config = JSON.parse(readFileSync(process.argv[2], "utf8"));
      const results = await checkEvent({
        eventName: process.env.GITHUB_EVENT_NAME, event, config, repository,
        git: createGit(), api: githubAPI({ repository, token: process.env.GH_TOKEN }),
      });
      const evidence = results.map((pr) => `PR #${pr.number}: head ${pr.head}; reviewed ${pr.reviewed}; main merges ${pr.merges}`).join("\n");
      console.log(evidence);
      if (process.env.GITHUB_STEP_SUMMARY) appendFileSync(process.env.GITHUB_STEP_SUMMARY, `Cross-family gate passed:\n\n${evidence}\n`);
    }
  } catch (error) {
    // Never emit request headers, API bodies, environment or transport errors.
    console.error(error instanceof Error && !["TypeError", "SyntaxError"].includes(error.name)
      ? error.message : "cross-family gate could not read valid evidence");
    process.exitCode = 1;
  }
}
