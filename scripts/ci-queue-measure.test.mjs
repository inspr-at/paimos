// SPDX-License-Identifier: AGPL-3.0-only
import assert from "node:assert/strict";
import { readFileSync, writeFileSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";
import { analyze, collectRuns, formatReport, main, measureRun, percentile, rulesetPlan, analyzeSlotDemand, collectSlotDemand, jobPhases } from "./ci-queue-measure.mjs";
import { fullWebShards } from "./test-tiers/core.mjs";

const fixtureURL = new URL("./testdata/ci-queue-measure.json", import.meta.url);
const fixture = () => JSON.parse(readFileSync(fixtureURL, "utf8"));
const rulesetURL = new URL("./testdata/ci-queue-ruleset.json", import.meta.url);
const ruleset = () => JSON.parse(readFileSync(rulesetURL, "utf8"));

const slotFrom = '2026-10-17T12:00:00Z', slotTo = '2026-10-17T12:10:00Z';
const at = seconds => new Date(Date.parse(slotFrom)+seconds*1000).toISOString();
function slotFixture() {
  let id=1;
  const job=(name,created,end)=>({id:id++,name,run_attempt:1,status:'completed',conclusion:'success',
    created_at:at(created),started_at:at(created+5),completed_at:at(end),labels:['ubuntu-latest'],runner_id:id,full_tier_execution:true});
  const matrix=[...Array.from({length:7},(_,i)=>`go-test (${i+1})`),...Array.from({length:4},(_,i)=>`web-unit (${i+1})`),...fullWebShards.map(i=>`web-shard (${i})`)];
  const runs=Array.from({length:5},(_,i)=>({id:100+i,repository:'fixture/aeon',event:'merge_group',path:'.github/workflows/ci.yml',
    created_at:at(10+i*100),head_sha:'a'.repeat(40),head_branch:`gh-readonly-queue/main/pr-${i+1}-${'b'.repeat(40)}`,run_attempt:1,
    attempts:[{number:1,jobs:matrix.map(name=>job(name,10+i*100,50+i*100))}]}));
  for(const [repo,event,path] of [['fixture/other','push','.github/workflows/build.yml'],['fixture/aeon','pull_request','.github/workflows/ci.yml'],['fixture/aeon','schedule','.github/workflows/nightly-full.yml']])
    runs.push({id:id++,repository:repo,event,path,created_at:at(0),run_attempt:1,attempts:[{number:1,jobs:[job('competing',0,600)]}]});
  return {schema:'aeon.ci-slot-demand.v1',organization:'fixture',repository:'fixture/aeon',repositories:['fixture/aeon','fixture/other'],from:slotFrom,to:slotTo,lookback:at(-86400),runs,
    timelines:Object.fromEntries(Array.from({length:5},(_,i)=>[i+1,[{id:`enqueued-${i}`,__typename:'AddedToMergeQueueEvent',createdAt:at(5+i*100)}]]))};
}

test('AEON-1337 slot budget uses simultaneous organization demand including PR and nightly work and separates the three time phases',()=>{
  const snapshot=slotFixture(),report=analyzeSlotDemand(snapshot);
  const width=7+4+fullWebShards.length;
  assert.equal(report.hosted_running.peak,width+3);
  assert.equal(report.hosted_waiting.peak,width);
  assert.equal(report.competing_demand.p95,3);
  assert.equal(report.full_build_samples,5);
  assert.equal(report.merge_build_slots_p95,width);
  assert.equal(report.recommendation.max_entries_to_build,Math.floor((60-3-2)/width));
  assert.deepEqual(report.phases.admission_seconds,{samples:5,p50:5,p95:5});
  assert.equal(report.phases.runner_queue_seconds.p95,5);
  assert.equal(report.phases.execution_seconds.p50,35);
  assert.equal(report.jobs.length,5*width+3);
  assert.equal(snapshot.runs[0].attempts[0].jobs[0].created_at,at(10),'analysis leaves the captured evidence intact');
  const larger=analyzeSlotDemand(snapshot,{slots:100,safetySlots:5});
  assert.equal(larger.recommendation.max_entries_to_build,Math.floor(92/width));
});

test('AEON-1337 missing full builds, legacy shard layouts and saturated budgets never invent a concurrency recommendation',()=>{
  const snapshot=slotFixture();
  assert.equal(analyzeSlotDemand({...snapshot,runs:snapshot.runs.slice(0,4)}).recommendation.max_entries_to_build,null);
  assert.equal(analyzeSlotDemand(snapshot,{slots:10}).recommendation.max_entries_to_build,null);
  for(const run of snapshot.runs.filter(run=>run.event==='merge_group'))run.attempts[0].jobs=run.attempts[0].jobs.filter(job=>job.name!=='web-shard (17)');
  assert.equal(analyzeSlotDemand(snapshot).full_build_samples,0,'a previous twelve-shard layout is not the current full build');
  const unknown=slotFixture(); unknown.runs[0].attempts[0].jobs[0].labels=['unknown-large-runner'];
  assert.throws(()=>analyzeSlotDemand(unknown),/unknown runner class/);
  const partial=slotFixture();partial.runs[0].attempts=[];
  assert.throws(()=>analyzeSlotDemand(partial),/incomplete or duplicate slot run/);
  const duplicate=slotFixture();duplicate.runs[0].attempts[0].jobs.push(duplicate.runs[0].attempts[0].jobs[0]);
  assert.throws(()=>analyzeSlotDemand(duplicate),/invalid or duplicate slot job/);
  const clock=slotFixture();clock.runs[0].attempts[0].jobs[0].started_at=at(1);
  assert.throws(()=>analyzeSlotDemand(clock),/invalid slot job timestamps/);
});

test('AEON-1337 admission is sampled once per proven queue session and never substituted with runner delay',()=>{
  const snapshot=slotFixture(),run=snapshot.runs[0],rebuild=structuredClone(run);
  rebuild.id=900;rebuild.created_at=at(70);
  rebuild.attempts[0].jobs.forEach(job=>{job.id+=10000;job.created_at=at(70);job.started_at=at(75);job.completed_at=at(90)});
  snapshot.runs.push(rebuild);
  const report=analyzeSlotDemand(snapshot);
  assert.equal(report.phases.admission_seconds.samples,5);
  assert.equal(report.builds.find(build=>build.run===900).admission.seconds,null);
  assert.match(report.builds.find(build=>build.run===900).admission.reason,/rebuild/);
  delete snapshot.timelines[1];
  assert.equal(analyzeSlotDemand(snapshot).phases.admission_seconds.samples,4);
  snapshot.timelines[1]=[{id:'old',__typename:'AddedToMergeQueueEvent',createdAt:at(-90000)}];
  assert.match(analyzeSlotDemand(snapshot).builds[0].admission.reason,/predates/);
  snapshot.timelines[1]=[{id:'add',__typename:'AddedToMergeQueueEvent',createdAt:at(1)},{id:'remove',__typename:'RemovedFromMergeQueueEvent',createdAt:at(2)}];
  assert.match(analyzeSlotDemand(snapshot).builds[0].admission.reason,/no proven active/);
});

test('AEON-1337 phase samples exclude planned skips, queued cancellations and unfinished execution without zero-duration success',()=>{
  const job=slotFixture().runs[0].attempts[0].jobs[0];
  assert.deepEqual(jobPhases(job),{runner_queue_seconds:5,execution_seconds:35});
  for(const skipped of [{...job,conclusion:'skipped'},{...job,conclusion:'cancelled',runner_id:0,steps:[]}])
    assert.deepEqual(jobPhases(skipped),{runner_queue_seconds:null,execution_seconds:null});
  assert.deepEqual(jobPhases({...job,status:'in_progress',completed_at:null}),{runner_queue_seconds:5,execution_seconds:null});
  assert.deepEqual(jobPhases({...job,created_at:null,completed_at:at(1)}),{runner_queue_seconds:null,execution_seconds:null});
  const snapshot=slotFixture();
  snapshot.runs.at(-1).attempts[0].jobs[0].labels=['self-hosted','linux'];
  const report=analyzeSlotDemand(snapshot);
  assert.equal(report.self_hosted_jobs,1);
  assert.equal(report.competing_demand.p95,2);
});

test('AEON-1337 slot collection reads every organization repository, workflow and attempt with bounded pagination and queue timelines',async()=>{
  const calls=[],run={id:10,repository:'fixture/aeon',event:'merge_group',path:'.github/workflows/ci.yml',created_at:at(10),head_branch:`gh-readonly-queue/main/pr-1-${'a'.repeat(40)}`,run_attempt:2};
  const get=async endpoint=>{
    calls.push(endpoint);
    if(endpoint.startsWith('orgs/'))return [{full_name:'fixture/aeon'},{full_name:'fixture/other'}];
    if(endpoint.includes('/attempts/')) {
      const number=Number(endpoint.match(/attempts\/(\d+)/)[1]);
      return {total_count:1,jobs:[{...slotFixture().runs[0].attempts[0].jobs[0],run_id:10,run_attempt:number,id:number,steps:[{name:'Confirm full tier execution',status:'completed',conclusion:'success'}]}]};
    }
    return {total_count:1,workflow_runs:endpoint.includes('/fixture/aeon/')&&endpoint.includes('created=2026-10-17')?[run]:[]};
  };
  const timeline=async(repository,pr,cursor)=>{assert.equal(repository,'fixture/aeon');assert.equal(pr,1);assert.equal(cursor,null);return {data:{repository:{pullRequest:{number:1,timelineItems:{nodes:slotFixture().timelines[1],pageInfo:{hasNextPage:false}}}}}}};
  const result=await collectSlotDemand({organization:'fixture',repository:'fixture/aeon',from:slotFrom,to:slotTo,get,timeline});
  assert.deepEqual(result.repositories,['fixture/aeon','fixture/other']);
  assert.equal(result.runs[0].attempts.length,2);
  assert.equal(result.runs[0].attempts[0].jobs[0].full_tier_execution,true);
  assert.ok(calls.includes('repos/fixture/aeon/actions/runs/10/attempts/2/jobs?per_page=100&page=1'));
  assert.ok(calls.some(call=>call.startsWith('repos/fixture/other/actions/runs?created=')));
  await assert.rejects(collectSlotDemand({organization:'fixture',repository:'fixture/aeon',from:slotFrom,to:slotTo,get:async()=>Array.from({length:100},(_,i)=>({full_name:`fixture/r${i}`})),timeline}),/pagination limit/);
  let tick=0;
  await assert.rejects(collectSlotDemand({organization:'fixture',repository:'fixture/aeon',from:slotFrom,to:slotTo,get,timeline,now:()=>tick++*20*60_000}),/deadline/);
  await assert.rejects(collectSlotDemand({organization:'fixture',repository:'fixture/aeon',from:slotFrom,to:slotTo,get,timeline:async()=>({errors:[{message:'incomplete'}]})}),/incomplete queue timeline/);
});

test('AEON-1337 offline slot evidence is reproducible and cannot be combined with ruleset mutation or unrelated options',async t=>{
  const directory=mkdtempSync(join(tmpdir(),'aeon-slot-cli-'));t.after(()=>rmSync(directory,{recursive:true,force:true}));
  const input=join(directory,'snapshot.json');writeFileSync(input,JSON.stringify(slotFixture()));
  const get=()=>assert.fail('offline analysis must not make an API request');
  const report=JSON.parse(await main(['--slots','--input',input,'--capacity','60'],get));
  assert.equal(report.full_build_samples,5);
  for(const args of [['--slots','--ruleset','42'],['--slots','--input',input,'--to',slotTo],['--capacity','60'],['--slots','--input',input,'--safety-slots','0']])
    await assert.rejects(main(args,get),/cannot be combined|already defines|require --slots|must be/);
});

test("initial runner wait, execution and retry gaps use attempt-specific jobs, never updated_at", () => {
  const report = analyze(fixture(), "2026-10-03T12:00:00Z");
  assert.deepEqual(report.runs.map((r) => r.id), [101, 102, 103, 104]);
  const retried = report.runs[1];
  assert.equal(retried.initial_queue_seconds, 60);
  assert.equal(retried.run_seconds, 300);
  assert.equal(retried.elapsed_seconds, 1500);
  assert.deepEqual(retried.attempts.map((a) => a.duration_seconds), [240, 300]);
  assert.equal(report.runs[2].initial_queue_seconds, 60); // Skipped job is excluded.
  assert.equal(report.runs[2].run_seconds, 360);
  assert.equal(report.runs[2].elapsed_seconds, 420);
  assert.equal(report.runs[3].run_seconds, null); // Active run is not a zero-length success.
  assert.equal(report.after.duration_samples, 1);
  assert.equal(report.after.queue_samples, 2);
  assert.equal(report.before.run_p50_seconds, 300);
  assert.equal(report.before.run_p95_seconds, 600);
});

test("failure occurrences and observed restarts group by exact SHA and retain failed earlier attempts", () => {
  const report = analyze(fixture());
  const group = report.groups.find((g) => g.head_sha.startsWith("a"));
  assert.deepEqual(group.run_ids, [102, 103]);
  assert.equal(group.failed_attempts, 1);
  assert.equal(group.reruns, 1);
  assert.equal(group.repeated_runs, 1);
  assert.equal(group.restarts, 2);
  assert.deepEqual(report.failed_jobs, [{ name: "web", failed_attempts: 2 }, { name: "go-test (2)", failed_attempts: 1 }]);
  assert.equal(report.groups.length, 3);
  assert.match(formatReport(report), /older\/newer half/);
  assert.match(formatReport(report), /not PR residence/);
});

test("empty windows, absent jobs, cancellation and invalid timestamp bounds are honest", () => {
  assert.equal(percentile([], .5), null);
  const empty = analyze({ schema: 1, runs: [] });
  assert.equal(empty.before.run_p50_seconds, null);
  assert.match(formatReport(empty), /n\/a/);
  const r = fixture().runs[3];
  r.attempts[0].jobs = [];
  r.attempts[0].conclusion = "cancelled";
  const measured = measureRun(r);
  assert.equal(measured.initial_queue_seconds, null);
  assert.equal(measured.run_seconds, null);
  assert.equal(measured.warnings.length, 1);
  const report = analyze({ schema: 1, runs: [r] });
  assert.equal(report.after.failed, 0);
  assert.equal(report.after.cancelled, 1);
  assert.equal(report.groups[0].cancelled_attempts, 1);
  r.attempts[0].jobs = [{ name: "bad clock", started_at: "2026-10-03T10:02:00Z", completed_at: "2026-10-03T10:01:00Z" }];
  assert.equal(measureRun(r).run_seconds, null);
  r.attempts[0].jobs.push({ name: "unfinished", started_at: "2026-10-03T10:03:00Z", completed_at: null });
  assert.equal(measureRun(r).elapsed_seconds, null);
});

test("incomplete histories, duplicate IDs and malformed splits fail instead of partial reporting", () => {
  const s = fixture();
  s.runs[2].attempts.pop();
  assert.throws(() => analyze(s), /incomplete attempt history/);
  const duplicate = fixture();
  duplicate.runs.push(duplicate.runs[0]);
  assert.throws(() => analyze(duplicate), /duplicate run IDs/);
  assert.throws(() => analyze(fixture(), "2026-10-03"), /ISO timestamp/);
});

test("last N workflow runs are sorted, event-filtered and read across each attempt and job page", async () => {
  const runs = fixture().runs;
  const calls = [];
  const get = async (endpoint) => {
    calls.push(endpoint);
    if (endpoint.includes("/workflows/")) return { total_count: 5, workflow_runs: [runs[3], { ...runs[0], id: 999, event: "push" }, runs[2], runs[1], runs[0]] };
    const match = endpoint.match(/runs\/(\d+)\/attempts\/(\d+)(\/jobs\?per_page=100&page=(\d+))?$/);
    assert.ok(match, endpoint);
    const attempt = runs.find((r) => r.id === Number(match[1])).attempts[Number(match[2]) - 1];
    if (!match[3]) return attempt;
    if (Number(match[1]) === 102 && Number(match[2]) === 1 && match[4] === "1") return { total_count: 101, jobs: Array.from({ length: 100 }, (_, i) => ({ id: i, name: "skipped", conclusion: "skipped", run_attempt: 1 })) };
    return { total_count: Number(match[1]) === 102 && Number(match[2]) === 1 ? 101 : attempt.jobs.length, jobs: attempt.jobs.slice(0, 1) };
  };
  const snapshot = await collectRuns({ count: 3, get });
  assert.deepEqual(snapshot.runs.map((r) => r.id), [104, 103, 102]);
  assert.equal(snapshot.runs[2].attempts.length, 2);
  assert.ok(calls.includes("repos/inspr-at/paimos/actions/runs/102/attempts/1/jobs?per_page=100&page=2"));
  assert.equal(snapshot.runs[2].attempts[0].jobs.length, 101);
  assert.ok(calls.every((c) => c.startsWith("repos/inspr-at/paimos/actions/")));
});

test("bounded pagination and identity checks reject truncated/mismatched API data", async () => {
  const run = fixture().runs[0];
  const tooMany = async (endpoint) => endpoint.includes("/workflows/") ? { workflow_runs: [run] }
    : endpoint.includes("/jobs?") ? { total_count: 1001, jobs: Array.from({ length: 100 }, () => ({ run_attempt: 1 })) }
    : run.attempts[0];
  await assert.rejects(collectRuns({ count: 1, get: tooMany }), /pagination limit/);
  await assert.rejects(collectRuns({ count: 1, get: async (endpoint) => endpoint.includes("/workflows/") ? { workflow_runs: [run] } : { id: 999, run_attempt: 1 } }), /identity mismatch/);
});

test("workflow pagination retrieves the last N unique runs before sorting the sample", async () => {
  const template = fixture().runs[0];
  const calls = [];
  const all = Array.from({ length: 101 }, (_, i) => ({ ...template, id: 1000 + i,
    created_at: new Date(Date.parse(template.created_at) + i * 1000).toISOString() })).reverse();
  const get = async (endpoint) => {
    calls.push(endpoint);
    if (endpoint.includes("/workflows/")) return { total_count: 101, workflow_runs: endpoint.endsWith("page=1") ? all.slice(0, 100) : all.slice(100) };
    if (endpoint.includes("/jobs?")) return { total_count: 0, jobs: [] };
    return { id: Number(endpoint.match(/runs\/(\d+)/)[1]), run_attempt: 1, status: "queued", conclusion: null };
  };
  const collected = await collectRuns({ count: 101, get });
  assert.equal(collected.runs.length, 101);
  assert.deepEqual(collected.runs.map((r) => r.id), all.map((r) => r.id));
  assert.equal(calls.filter((c) => c.includes("/workflows/")).length, 2);
});

test("ruleset patch resolves the actual rule position and preserves every unrelated setting", () => {
  const current = ruleset();
  const { candidate, rollback, diff } = rulesetPlan(current);
  assert.deepEqual(diff, [
    { op: "replace", path: "/rules/2/parameters/max_entries_to_build", before: 1, value: 2 },
    { op: "replace", path: "/rules/2/parameters/max_entries_to_merge", before: 1, value: 5 },
    { op: "replace", path: "/rules/2/parameters/min_entries_to_merge_wait_minutes", before: 5, value: 1 },
  ]);
  assert.deepEqual(candidate.rules.slice(0, 2), current.rules.slice(0, 2));
  assert.deepEqual(candidate.conditions, current.conditions);
  assert.deepEqual(candidate.bypass_actors, current.bypass_actors);
  assert.equal(candidate.rules[2].parameters.grouping_strategy, "ALLGREEN");
  assert.equal(candidate.rules[2].parameters.check_response_timeout_minutes, 60);
  assert.equal(candidate.rules[2].parameters.merge_method, "SQUASH");
  assert.equal(candidate.rules[2].parameters.min_entries_to_merge, 1);
  assert.equal(candidate.id, undefined);
  assert.equal(candidate.created_at, undefined);
  assert.deepEqual(rollback.rules, current.rules);
  assert.equal(current.rules[2].parameters.max_entries_to_build, 1);
  assert.deepEqual(rulesetPlan(candidate).diff, []);
});

test("ruleset must exist at repository scope; unsupported changes fail closed", () => {
  const current = ruleset();
  assert.throws(() => rulesetPlan({ ...current, source_type: "Organization" }), /inherited/);
  assert.throws(() => rulesetPlan({ ...current, rules: [] }), /exactly one/);
  assert.throws(() => rulesetPlan({ ...current, rules: [...current.rules, current.rules[2]] }), /exactly one/);
  assert.throws(() => rulesetPlan(current, { schema: 1, select: { rule_type: "merge_queue" }, patch: [{ op: "replace", path: "/grouping_strategy", value: "HEADGREEN" }] }), /unsupported/);
});

test("live dry-run makes exactly one read; no CLI apply or mutation path exists", async () => {
  const calls = [];
  const output = await main(["--ruleset", "42", "--dry-run"], async (endpoint) => { calls.push(endpoint); return ruleset(); });
  assert.deepEqual(calls, ["repos/inspr-at/paimos/rulesets/42"]);
  assert.equal(JSON.parse(output).length, 3);
  await assert.rejects(main(["--apply"]), /unknown option/);
  await assert.rejects(main(["--ruleset-payload", "--ruleset", "42"]), /saved snapshot/);
  await assert.rejects(main(["--ruleset", "42", "--count", "3"]), /cannot be combined/);
});

test("offline CLI table, JSON, candidate and rollback work without gh or dependencies", () => {
  const cli = new URL("./ci-queue-measure.mjs", import.meta.url).pathname;
  const invoke = (...args) => spawnSync(process.execPath, [cli, ...args], { encoding: "utf8", env: { PATH: "/nonexistent" } });
  const table = invoke("--input", fixtureURL.pathname, "--split-at", "2026-10-03T12:00:00Z");
  assert.equal(table.status, 0, table.stderr);
  assert.match(table.stdout, /\| Before \| 2 \| 2 \| 1/);
  const json = invoke("--input", fixtureURL.pathname, "--json");
  assert.equal(json.status, 0, json.stderr);
  assert.equal(JSON.parse(json.stdout).observed_count, 4);
  for (const [mode, concurrency] of [["--ruleset-payload", 2], ["--rollback-payload", 1]]) {
    const result = invoke(mode, "--snapshot", rulesetURL.pathname);
    assert.equal(result.status, 0, result.stderr);
    assert.equal(JSON.parse(result.stdout).rules[2].parameters.max_entries_to_build, concurrency);
  }
});
