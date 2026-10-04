// SPDX-License-Identifier: AGPL-3.0-only
import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";
import { validateDispatcher, validateEnvironment, verifyProtectedEnvironment } from "./verify-live-policy.mjs";

const environment = () => ({ deployment_branch_policy: { protected_branches: false, custom_branch_policies: true },
  protection_rules: [{ type: "required_reviewers", prevent_self_review: false,
    reviewers: [{ type: "User", reviewer: { id: 276789, login: "markus-barta" } }] }] });
const branches = () => ({ total_count: 1, branch_policies: [{ name: "main", type: "branch" }] });

test("bootstrap payload requires the actual operator and custom main-only policies", () => {
  const payload = JSON.parse(readFileSync(".github/live-verification-environment.json"));
  assert.deepEqual(payload.reviewers, [{ type: "User", id: 276789 }]);
  assert.deepEqual(payload.deployment_branch_policy, environment().deployment_branch_policy);
  assert.equal(payload.prevent_self_review, false); // Attended operator dispatch/approval.
  validateEnvironment(environment(), branches());
});

for (const [name, edit] of [
  ["no review", (e, b) => e.protection_rules = []],
  ["different reviewer", (e, b) => e.protection_rules[0].reviewers[0].reviewer.id = 1],
  ["login-only impersonation", (e, b) => e.protection_rules[0].reviewers[0].reviewer.login = "other"],
  ["additional approver", (e, b) => e.protection_rules[0].reviewers.push({ type: "Team", reviewer: { id: 9 } })],
  ["protected branches wildcard", (e, b) => e.deployment_branch_policy.protected_branches = true],
  ["all branches", (e, b) => e.deployment_branch_policy = null],
  ["tag rule", (e, b) => b.branch_policies[0].type = "tag"],
  ["wildcard rule", (e, b) => b.branch_policies[0].name = "*"],
  ["extra branch rule", (e, b) => { b.total_count++; b.branch_policies.push({ name: "work/*", type: "branch" }); }],
  ["partial listing", (e, b) => b.total_count = 101],
]) test(`environment rejects ${name}`, () => {
  const e = environment(), b = branches(); edit(e, b);
  assert.throws(() => validateEnvironment(e, b), /policy refused/);
});

test("policy preflight is read-only and rejects API failures", async () => {
  const calls = [];
  const request = async (token, method, path) => {
    calls.push({ method, path });
    return { status: 200, data: path.includes("branch-policies") ? branches() : environment() };
  };
  await verifyProtectedEnvironment("unused fixture", "live-verification", request);
  assert.equal(calls.length, 2); assert.ok(calls.every(c => c.method === "GET"));
  await assert.rejects(verifyProtectedEnvironment("unused fixture", "live-verification", async () => ({ status: 403 })), /policy refused/);
  await assert.rejects(verifyProtectedEnvironment("", "live-verification", request));
});

const dispatcher = { GITHUB_REPOSITORY: "inspr-at/paimos", GITHUB_EVENT_NAME: "workflow_dispatch", GITHUB_REF: "refs/heads/main",
  GITHUB_WORKFLOW_REF: "inspr-at/paimos/.github/workflows/verify-live.yml@refs/heads/main", GITHUB_WORKFLOW_SHA: "a".repeat(40),
  GITHUB_ACTOR: "markus-barta", GITHUB_ACTOR_ID: "276789", GITHUB_TRIGGERING_ACTOR: "markus-barta" };
test("dispatcher binds main workflow and immutable operator id, including reruns", () => {
  validateDispatcher(dispatcher, "live-verification");
  for (const [key, value] of [["GITHUB_ACTOR_ID", "1"], ["GITHUB_TRIGGERING_ACTOR", "writer"], ["GITHUB_REF", "refs/tags/main"],
    ["GITHUB_EVENT_NAME", "release"], ["GITHUB_WORKFLOW_REF", dispatcher.GITHUB_WORKFLOW_REF.replace("main", "work/evil")]]) {
    assert.throws(() => validateDispatcher({ ...dispatcher, [key]: value }, "live-verification"));
  }
});
