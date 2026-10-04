// SPDX-License-Identifier: AGPL-3.0-only
// Read-only preflight. The coordinator installs server-side protection BEFORE
// adding environment secrets. A check inside a workflow cannot protect secrets
// from a replacement workflow on an untrusted ref.
import { pathToFileURL } from "node:url";
import { gh } from "./homebrew-tap-pr.mjs";

const REPO = "inspr-at/paimos";
const OWNER_ID = 276789;
const ENVIRONMENTS = ["live-verification", "homebrew-tap"];
const refuse = () => { throw new Error("protected main environment or dispatcher policy refused"); };

export function validateDispatcher(env, workflow) {
  if (!ENVIRONMENTS.includes(workflow) || env.GITHUB_REPOSITORY !== REPO ||
      env.GITHUB_EVENT_NAME !== "workflow_dispatch" || env.GITHUB_REF !== "refs/heads/main" ||
      env.GITHUB_WORKFLOW_REF !== `${REPO}/.github/workflows/${workflow === "live-verification" ? "verify-live" : workflow}.yml@refs/heads/main` ||
      !/^[a-f0-9]{40}$/.test(env.GITHUB_WORKFLOW_SHA ?? "") ||
      env.GITHUB_ACTOR !== "markus-barta" || env.GITHUB_ACTOR_ID !== String(OWNER_ID) ||
      env.GITHUB_TRIGGERING_ACTOR !== "markus-barta") refuse();
}

export function validateEnvironment(environment, branches) {
  const policy = environment?.deployment_branch_policy;
  const rules = environment?.protection_rules;
  const review = rules?.filter(r => r.type === "required_reviewers");
  if (policy?.protected_branches !== false || policy.custom_branch_policies !== true ||
      !Array.isArray(review) || review.length !== 1 || review[0].prevent_self_review !== false ||
      review[0].reviewers?.length !== 1 || review[0].reviewers[0].type !== "User" ||
      review[0].reviewers[0].reviewer?.id !== OWNER_ID ||
      review[0].reviewers[0].reviewer?.login !== "markus-barta" ||
      branches?.total_count !== 1 || branches.branch_policies?.length !== 1 ||
      branches.branch_policies[0].name !== "main" || branches.branch_policies[0].type !== "branch") refuse();
}

export async function verifyProtectedEnvironment(token, name, request = gh) {
  if (!ENVIRONMENTS.includes(name) || !token) refuse();
  const path = `/repos/${REPO}/environments/${name}`;
  const environment = await request(token, "GET", path);
  const branches = await request(token, "GET", `${path}/deployment-branch-policies?per_page=100`);
  if (environment.status !== 200 || branches.status !== 200) refuse();
  validateEnvironment(environment.data, branches.data);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const name = process.argv[2];
    validateDispatcher(process.env, name);
    await verifyProtectedEnvironment(process.env.GH_TOKEN, name);
    console.log("protected main environment and dispatcher policy verified");
  } catch {
    console.error("Environment preflight refused; coordinator must install and verify main-only required-reviewer policy before adding App secrets.");
    process.exitCode = 1;
  }
}
