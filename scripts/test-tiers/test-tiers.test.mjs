// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync, mkdtempSync, mkdirSync, writeFileSync, symlinkSync, truncateSync, utimesSync } from 'node:fs'
import { resolve } from 'node:path'
import { tmpdir } from 'node:os'
import { spawnSync, execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { fileURLToPath } from 'node:url'
import { validate, select, shard, key, exactPattern, webGraph, counts, measuredWeights, uncertain, affectedRisk, impactRisk, machineryPattern, manifestPromotions, migrationObjects, consumers, consumerCaseBound, isDocsLike } from './core.mjs'
import { reportCases, goOutcomes, browserOutcomes } from './report.mjs'
import { aggregate, jobMinutes, readReports } from './measure.mjs'
import { checkFull } from './check-full.mjs'
import { changedPaths, schedulingMode, schedulingDecision, trustedSchedulingDecision, main as tierPlanMain, effectiveLane, sourceTree, promotionsBetween } from './diff.mjs'
import { runnerDecision, runnerSelection } from './cli.mjs'
import { treeStamp, reuse, webStampEntries } from './collect.mjs'
import { inputBounds, boundedText, inputMetadata, readInput } from './inputs.mjs'
import { tierWeights } from '../../web/scripts/ci-web-shard.mjs'
import './manifests.test.mjs'
import './tiers-merge-driver.test.mjs'

const g=(pkg,name,tier='NIGHTLY')=>({kind:'go',package:pkg,name,tier,active:true})
const w=(file,name,tier='NIGHTLY')=>({kind:'node',file,name,tier})
const cases=[g('internal/auth','TestCeiling','ESSENTIAL'),g('internal/auth','TestRotation'),
  g('internal/nodes','TestCRUD','ESSENTIAL'),g('internal/nodes','TestUndo'),g('cmd/aeon','TestRoutes'),
  w('tests/scope.test.ts','ceiling','ESSENTIAL'),w('tests/scope.test.ts','revoked'),w('tests/tree.test.ts','tree')]
const fixture=()=>({version:1,tests:structuredClone(cases)})
const noFlaky={version:1,entries:[]}

test('AEON-707 work safety regressions remain classified and selected by every full gate',()=>{
  const manifest=JSON.parse(readFileSync(new URL('../ci/go-test-tiers.json',import.meta.url)))
  const protectedCases=[
    ['internal/parentbenefits','TestRetryIsPersonOnlyAndRechecksPermission'],
    ['internal/parentbenefits','TestRetryDeadlineBoundsBothBodyDecodes'],
    ['internal/parentbenefits','TestLeaseRecoveryAndTenantIsolation'],
    ['internal/parentbenefits','TestSummaryRejectsInvalidOrUnboundedModelOutput'],
    ['internal/nodes','TestWorkAggregateLimitsAreExplicitHTTPFailures'],
    ['internal/nodes','TestWorkScopeRootAndExpansionBudgets'],
    ['internal/nodes','TestWorkLifecycleBackgroundCompletionAndRevocation'],
    ['internal/releases','TestWorkParentPlacementRechecksRevokedGrantUnderFence'],
  ].map(([pkg,name])=>({kind:'go',package:pkg,name}))
  const rows=validate(manifest,manifest.tests)
  for(const row of protectedCases) {
    const declared=rows.find(candidate=>key(candidate)===key(row))
    assert.ok(declared,`Missing work safety classification: ${key(row)}`)
    assert.ok(['ESSENTIAL','GATED-FULL'].includes(declared.tier),`Work safety case deferred to NIGHTLY: ${key(row)}`)
  }
  const modes=[
    {event:'push'},
    {event:'workflow_dispatch'},
    {event:'pull_request',paths:['README.md'],forceFull:true},
    {event:'merge_group',paths:['README.md'],forceFull:true},
    {event:'pull_request',paths:['scripts/test-tiers/core.mjs']},
    {event:'merge_group',paths:['api/openapi.yaml']},
    {event:'pull_request'},
    {event:'merge_group'},
  ]
  for(const mode of modes) {
    const selection=select(rows,mode)
    assert.equal(selection.scope,'gated-full')
    const selected=new Set(selection.tests.map(key))
    for(const row of protectedCases) assert.ok(selected.has(key(row)),`${JSON.stringify(mode)} omitted ${key(row)}`)
  }
})

test('AEON-707 work migration test files have explicit tiers without changing runtime drift policy',()=>{
  const manifest=JSON.parse(readFileSync(new URL('../ci/go-test-tiers.json',import.meta.url)))
  const files=[
    'internal/parentbenefits/module_test.go',
    'internal/parentbenefits/worker_test.go',
    'internal/nodes/work_aggregates_test.go',
    'internal/nodes/work_lifecycle_test.go',
    'internal/releases/work_parents_test.go',
  ]
  const discovered=files.flatMap(file=>{
    const source=readFileSync(new URL(`../../${file}`,import.meta.url),'utf8')
    const names=[...source.matchAll(/^func (Test\w+)\(t \*testing\.T\)/gm)].map(match=>match[1])
    assert.ok(names.length,`No test declarations found in ${file}`)
    return names.map(name=>({kind:'go',package:file.slice(0,file.lastIndexOf('/')),name}))
  })
  const identities=new Set(discovered.map(key))
  const owned={...manifest,tests:manifest.tests.filter(row=>identities.has(key(row)))}
  assert.deepEqual(owned.tests.map(key).sort(),[...identities].sort(),'Work migration tests need explicit classifications')
  const rows=validate(owned,discovered,undefined,{strict:true})
  assert.equal(rows.length,discovered.length)
  assert.ok(rows.every(row=>['ESSENTIAL','GATED-FULL'].includes(row.tier)))
  // Unrelated future registrations still reconcile permissively to NIGHTLY.
  const added={kind:'go',package:'internal/nodes',name:'TestFutureUnclassified',active:true}
  const warnings=[]
  assert.equal(validate(owned,[...discovered,added],undefined,{warn:warning=>warnings.push(warning)}).at(-1).tier,'NIGHTLY')
  assert.deepEqual(warnings,[`::warning::Unclassified test (defaults to NIGHTLY; classify these): ${key(added)}`])
})

const replay=JSON.parse(readFileSync(new URL('./affected-replay.json',import.meta.url)))
const legacy=await import(`data:text/javascript;base64,${Buffer.from(replay.legacySelector).toString('base64')}`)
const replayRows=['go','web'].flatMap(kind=>JSON.parse(readFileSync(new URL(`../ci/${kind}-test-tiers.json`,import.meta.url))).tests)
const replayGraph=webGraph(fileURLToPath(new URL('../../web',import.meta.url)))
const repoRoot=fileURLToPath(new URL('../../',import.meta.url))
const repoTree=sourceTree(repoRoot)
const noPromotions={keys:new Set(),kinds:new Set(),count:0}
const readRepo=path=>{ try { return readFileSync(resolve(repoRoot,path),'utf8') } catch { return undefined } }
const realPaths=[...new Set(replay.prs.flatMap(pr=>pr.paths))]
// Affected-lane selection against the real catalogue, tree and graph.
const on=(paths,extra={})=>select(replayRows,{event:'pull_request',paths,affectedLane:'on',webImports:replayGraph,imports:replay.goImports,tree:repoTree,promotions:noPromotions,readFile:readRepo,...extra})
const decide=(paths,extra={})=>schedulingDecision('pull_request',paths,path=>readRepo(path)!==undefined,{affectedLane:'on',graph:replayGraph,tree:repoTree,promotions:noPromotions,...extra})
const goPackages=selection=>new Set(selection.tests.filter(row=>row.kind==='go'&&row.tier!=='ESSENTIAL').map(row=>row.package))
const webFiles=selection=>new Set(selection.tests.filter(row=>row.kind!=='go'&&row.tier!=='ESSENTIAL').map(row=>row.file))
const essentialOnly=selection=>selection.tests.every(row=>row.tier==='ESSENTIAL')

test('affected kill switch off matches the frozen selector for all 80 real PR lists except instruction inputs (AEON-766)',()=>{
  assert.equal(replay.prs.length,80)
  assert.equal(replay.base,'b6f74faa11d506efd3d21387ca4e2f5a9c687dcf')
  assert.equal(replay.legacySelectorSHA256,'737a4bb8a805c23e572ad83f2922e65d8c0d34a932afca2e13fd9fa51e26d710')
  assert.equal(new Set(replay.prs.map(pr=>pr.number)).size,80)
  assert.equal(createHash('sha256').update(replay.legacySelector).digest('hex'),replay.legacySelectorSHA256)
  const synthetic=[['scripts/ci/go-test-tiers.json'],['api/openapi.yaml'],['internal/db/migrations/1124_desk_notification_claims.sql'],['scripts/audit/common.py'],
    ['version.json'],['internal/auth/testdata/key_scope_ceiling.json'],['web/tests/settings-shared-harness.html'],['scripts/check-migrations.test.mjs'],['AGENTS.md'],['internal/db/migrations/README.md']]
  for(const {number,paths} of [...replay.prs,...synthetic.map(paths=>({number:paths[0],paths}))]) {
    const options={event:'pull_request',paths,imports:replay.goImports,webImports:replayGraph}
    const expected=legacy.select(replayRows,options)
    for(const affectedLane of [undefined,'','off','ON','true',' on','on ']) {
      const selected=select(replayRows,{...options,affectedLane,tree:repoTree,promotions:noPromotions,readFile:readRepo})
      const message=`PR ${number}, switch ${affectedLane}`
      // AEON-766's instruction safety rule also applies with affected narrowing off.
      if(paths.some(path=>/(?:^|\/)(?:AGENTS|CLAUDE)\.md$/i.test(path))) {
        assert.equal(selected.full,true,message)
        assert.deepEqual(selected.tests,replayRows.filter(row=>row.tier==='ESSENTIAL'||row.tier==='GATED-FULL'),message)
      } else assert.deepEqual(selected,expected,message)
    }
    assert.deepEqual(schedulingDecision('pull_request',paths,()=>true,{graph:replayGraph}),schedulingDecision('pull_request',paths,()=>true,{affectedLane:'off',graph:replayGraph}),`PR ${number}`)
  }
})

test('merge groups always use the exact full gate with either switch state, including every real diff',()=>{
  const expected=replayRows.filter(row=>row.tier==='ESSENTIAL'||row.tier==='GATED-FULL')
  for(const affectedLane of [undefined,'off','on'])for(const paths of [[],['README.md'],['internal/auth/key.go'],...replay.prs.map(pr=>pr.paths)]) {
    const selected=select(replayRows,{event:'merge_group',paths,affectedLane,webImports:replayGraph,tree:repoTree,promotions:noPromotions,readFile:readRepo})
    assert.equal(selected.full,true)
    assert.equal(selected.scope,'gated-full')
    assert.deepEqual(selected.tests,expected)
    const decision=schedulingDecision('merge_group',paths,()=>true,{affectedLane,graph:replayGraph,tree:repoTree,promotions:noPromotions})
    assert.equal(decision.mode,'full')
    assert.equal(decision.layout,'full')
  }
})

test('CI machinery from the real PRs and the pattern matrix always forces full, alone or beside a narrowable path',()=>{
  const real=realPaths.filter(path=>machineryPattern.test(path))
  const matrix=['.github/workflows/ci.yml','.github/workflows/nightly-full.yml','.github/actions/helpers/foo.ts','scripts/ci-static.mjs','scripts/ci-pr-plan.mjs','scripts/ci-tree-reuse.mjs',
    'scripts/ci-flake-guard.mjs','scripts/ci-quarantine.json','scripts/ci-runner-guard/main.go','scripts/ci-go-shards/shard.go','scripts/ci/static-checks.json','scripts/test-tiers/core.mjs',
    'scripts/test-tiers/diff.mjs','scripts/test-tier-go/main.go','scripts/releaseworkflow/workflow_test.go','go.mod','go.sum','go.work','go.work.sum','web/package.json','web/package-lock.json',
    'web/vite.config.ts','web/playwright.ui.config.ts','web/playwright.policy.ts','web/tsconfig.json','web/scripts/ci-web-shard.mjs','web/scripts/run.mjs']
  for(const path of ['.github/workflows/ci.yml','scripts/ci/go-test-tiers.json','web/ci-web-shards.json','scripts/audit/slices.json']) assert.ok(real.includes(path)||!machineryPattern.test(path),path)
  assert.ok(real.length>=10,'real machinery paths must be represented')
  for(const path of new Set([...real,...matrix])) {
    assert.ok(machineryPattern.test(path)||/generated|codegen|\.gen\.|\.pb\./.test(path),path)
    for(const paths of [[path],[path,'scripts/ci/web-test-tiers.json'],['api/openapi.yaml',path],['README.md',path]]) {
      const selected=on(paths)
      assert.equal(selected.full,true,paths.join(' '))
      assert.match(selected.reason,/^(?:CI machinery stays full|unnarrowed generated\/configuration risk): /,paths.join(' '))
      const decision=decide(paths)
      assert.equal(decision.mode,'full',paths.join(' '))
      assert.equal(decision.layout,'full')
      assert.equal(decision.reason,selected.reason)
    }
  }
})

test('every path the rules leave full forces full in the selector and the planner, including the risk-pattern matrix',()=>{
  // R3 helpers are excluded: the injected importer below legitimately maps them.
  const real=realPaths.filter(path=>affectedRisk(path,replayGraph).full&&!/^R3 /.test(affectedRisk(path,replayGraph).reason))
  const matrix=['go.mod','go.sum','go.work','go.work.sum','package.json','package-lock.json',
    '.github/workflows/ci.yml','.github/actions/helpers/foo.ts','internal/db/runner.go','internal/db/queries.sql','internal/dbtest/db.go',
    'scripts/test-tiers/core.mjs','scripts/test-tiers/diff.mjs','scripts/ci-pr-plan.mjs','scripts/check-migrations.mjs','scripts/migration-compat.sh','scripts/migration-compat-probe.py',
    'scripts/ci-weights.mjs','scripts/check.mjs','web/scripts/run.mjs','web/vite.config.ts','web/playwright.ui.config.ts',
    'web/tsconfig.json','web/package-other.json','web/src/generated/api.ts','web/tests/helpers/generated.ts',
    'web/tests/helpers/api.generated.ts','web/tests/helpers/client.gen.ts','internal/auth/client.pb.go','api/codegen.yaml','.dockerignore','Dockerfile','justfile','web/index.html']
  for(const path of ['internal/db/db.go','scripts/test-harness-sigterm-race.py','scripts/aeon-683-validation.json']) assert.ok(real.includes(path),`${path} must stay full on real data`)
  for(const path of new Set([...real,...matrix])) {
    const graph={...replayGraph,[path.slice(4)]:[],'tests/importer.spec.ts':[path.slice(4)]}
    assert.equal(on([path],{webImports:graph}).full,true,path)
    assert.equal(decide([path],{graph}).mode,'full',path)
  }
})

test('R1 manifest-only diffs take the static layout; promotions must execute and decide the shard layout',()=>{
  const manifests=realPaths.filter(path=>affectedRisk(path).rule==='R1')
  for(const path of ['scripts/ci/go-test-tiers.json','scripts/ci/web-test-tiers.json','web/ci-web-shards.json','scripts/migration-policy-exceptions.json','scripts/audit/slices.json','scripts/ci/go-shards.txt','web/scripts/ci-web-shard.test.mjs'])
    assert.ok(manifests.includes(path),`${path} must appear in the real PR lists`)
  for(const path of [...manifests,'scripts/ci/known-flaky.json','scripts/ci/test-tier-selection-baseline.json','web/scripts/aeon-681-ci.test.mjs']) {
    const selected=on([path])
    assert.equal(selected.full,false,path)
    assert.ok(essentialOnly(selected),`${path}: registration-only manifest diffs run essentials only`)
    assert.match(selected.reason,/R1: manifest\/static checks: /)
    const decision=decide([path])
    assert.deepEqual([decision.mode,decision.layout],['essential','static'],path)
    assert.ok(selected.reason.endsWith(decision.reason),path)
  }
  // The static registry itself is CI machinery, never a manifest.
  assert.equal(on(['scripts/ci/static-checks.json']).full,true)
  // Promotions: absent or lower base tier -> promoted; NIGHTLY and demotions are not.
  const head={go:{tests:[g('internal/auth','TestNew','GATED-FULL'),g('internal/auth','TestUp','ESSENTIAL'),g('internal/auth','TestNightly','NIGHTLY'),g('internal/auth','TestDown','GATED-FULL')]},
    web:{tests:[w('tests/a.unit.test.ts','unit','GATED-FULL'),{...w('tests/b.spec.ts','browser','ESSENTIAL'),kind:'browser'}]}}
  const base={go:{tests:[g('internal/auth','TestUp','GATED-FULL'),g('internal/auth','TestDown','ESSENTIAL')]},web:{tests:[w('tests/a.unit.test.ts','unit','GATED-FULL')]}}
  const promoted=manifestPromotions(base,head)
  assert.deepEqual([...promoted.keys].sort(),['browser:tests/b.spec.ts:browser','internal/auth:TestNew','internal/auth:TestUp'])
  assert.deepEqual([...promoted.kinds].sort(),['browser','go'])
  assert.throws(()=>manifestPromotions({},{go:{tests:[g('internal/auth','TestBad','LATER')]}}),/Invalid tier/)
  const rows=[g('internal/auth','TestCeiling','ESSENTIAL'),g('internal/auth','TestNew','GATED-FULL'),g('internal/other','TestOther','GATED-FULL'),
    w('tests/a.unit.test.ts','unit','GATED-FULL'),{...w('tests/b.spec.ts','browser','GATED-FULL'),kind:'browser'},{...w('tests/c.unit.test.ts','vitest','GATED-FULL'),kind:'vitest'}]
  const run=(promotions,paths=['scripts/ci/web-test-tiers.json'])=>{
    const selected=select(rows,{event:'pull_request',paths,affectedLane:'on',webImports:{},tree:{go:new Map(),webTests:new Map()},promotions})
    const risk=impactRisk(paths,{event:'pull_request',affectedLane:'on',tree:{go:new Map(),webTests:new Map()},promotions,tests:rows})
    return {selected,risk}
  }
  const pick=keys=>({keys:new Set(keys),kinds:new Set(keys.map(k=>rows.find(row=>key(row)===k).kind)),count:keys.length})
  let result=run(noPromotions)
  assert.deepEqual(result.selected.tests.map(key),['internal/auth:TestCeiling']);assert.equal(result.risk.layout,'static')
  result=run(pick(['vitest:tests/c.unit.test.ts:vitest','node:tests/a.unit.test.ts:unit']))
  assert.deepEqual(result.selected.tests.map(key),['internal/auth:TestCeiling','node:tests/a.unit.test.ts:unit','vitest:tests/c.unit.test.ts:vitest'])
  assert.equal(result.risk.layout,'static','unit promotions run in web-unit, which the static layout keeps')
  assert.match(result.selected.reason,/R1: 2 promoted node\/vitest cases execute/)
  result=run(pick(['browser:tests/b.spec.ts:browser']))
  assert.ok(result.selected.tests.some(row=>key(row)==='browser:tests/b.spec.ts:browser'));assert.equal(result.risk.layout,'full','browser promotions need web shards')
  result=run(pick(['internal/auth:TestNew']),['scripts/ci/go-test-tiers.json'])
  assert.ok(result.selected.tests.some(row=>key(row)==='internal/auth:TestNew'));assert.equal(result.risk.layout,'full','Go promotions need Go shards')
  assert.ok(!result.selected.tests.some(row=>row.package==='internal/other'),'unpromoted packages stay out')
  result=run(undefined)
  assert.equal(result.selected.full,true);assert.match(result.selected.reason,/R1 full: manifest promotions unavailable/)
  assert.equal(run(undefined,['web/ci-web-shards.json']).selected.full,false,'only tier manifests need the base comparison')
  // git-backed promotions: a base without the manifest or an invalid base fails closed.
  assert.equal(promotionsBetween(undefined,{cwd:repoRoot}),undefined)
  assert.equal(promotionsBetween('--bad',{cwd:repoRoot}),undefined)
  const same=promotionsBetween(undefined,{cwd:repoRoot,baseDirectory:repoRoot})
  assert.equal(same.count,0,'identical manifests promote nothing')
})

test('R2 contract diffs run the Go packages that read the contract and keep web at essentials',()=>{
  const selected=on(['api/openapi.yaml'])
  assert.equal(selected.full,false)
  const packages=goPackages(selected)
  for(const pkg of ['internal/chat','internal/modelregistry','internal/reportercontract','internal/harness']) assert.ok(packages.has(pkg),`${pkg} reads api/openapi.yaml`)
  assert.equal(webFiles(selected).size,0,'handwritten web wire types: no generated importers to select')
  assert.match(selected.reason,/R2: contract consumers: api\/openapi\.yaml; consumer packages: /)
  const decision=decide(['api/openapi.yaml'])
  assert.deepEqual([decision.mode,decision.layout],['essential','full'])
  assert.ok(selected.reason.endsWith(decision.reason))
  assert.equal(on(['api/openapi-messaging.yaml']).full,false)
  // The OpenAPI lint package, codegen configuration or generated sources stay full.
  assert.match(on(['api/openapi.yaml','internal/reportercontract/pins.json']).reason,/^R2 full: OpenAPI lint package changed/)
  assert.match(on(['api/codegen.yaml']).reason,/generated\/configuration/)
  // No reader at all is not a narrowing: the tree must name a consumer.
  const empty={go:new Map([['internal/x/x.go','package x']]),webTests:new Map()}
  assert.match(on(['api/openapi.yaml'],{tree:empty}).reason,/^R2 full: no Go consumer found/)
  assert.match(on(['api/openapi.yaml'],{tree:undefined}).reason,/^R2 full: no Go consumer found/)
})

test('R4 migrations map recognised schema objects to referencing packages; unreadable, unparseable or wide diffs stay full',()=>{
  assert.deepEqual([...migrationObjects(`-- comment CREATE TABLE ignored
    CREATE TABLE desk_claims (id uuid, note text DEFAULT 'INSERT INTO fake');
    ALTER TABLE ONLY public.project_leads ADD COLUMN dispatch_key_id uuid;
    CREATE UNIQUE INDEX desk_claims_idx ON desk_claims (id);
    INSERT INTO settings (k) VALUES ('new');
    UPDATE nodes SET kind = 'x' WHERE kind = 'y';
    DELETE FROM old_settings WHERE k = 'old';`)].sort(),
    ['desk_claims','nodes','old_settings','project_leads','settings'])
  assert.equal(migrationObjects('DO $$ BEGIN PERFORM 1; END $$;'),undefined)
  assert.deepEqual([...consumers({go:new Map([['internal/a/a.go','SELECT * FROM desk_claims'],['internal/b/b.go','desk_claims_extra']]),webTests:new Map()},{words:['desk_claims']}).goPackages],['internal/a'])
  const narrow='internal/db/migrations/1124_desk_notification_claims.sql'
  const simple='CREATE TABLE desk_notification_claims (id uuid, note text);'
  const selected=on([narrow],{readFile:()=>simple})
  assert.equal(selected.full,false)
  assert.ok(goPackages(selected).has('internal/db'),'the migration runner package always runs')
  const readers=consumers(repoTree,{words:[...migrationObjects(simple)]}).goPackages
  assert.ok(readers.size>=1)
  for(const pkg of readers) assert.ok(goPackages(selected).has(pkg),pkg)
  assert.match(selected.reason,/R4: migration schema consumers: .*; consumer packages: /)
  const unrelated=replayRows.find(row=>row.kind==='go'&&row.tier==='GATED-FULL'&&row.package!=='internal/db'&&!readers.has(row.package))
  assert.ok(unrelated,'fixture must include an unrelated gated package')
  assert.ok(!selected.tests.includes(unrelated),'narrow SQL selection must omit unrelated gated tests')
  assert.equal(webFiles(selected).size,0,'browser specs are API-mocked; the real server runs in e2e')
  assert.equal(decide([narrow]).mode,'full','the real migration contains unsupported policy and constraint expressions')
  const decision=decide([narrow],{readFile:()=>simple})
  assert.deepEqual([decision.mode,decision.layout],['essential','full'])
  assert.ok(selected.reason.endsWith(decision.reason))
  const wide='internal/db/migrations/1206_themes.sql'
  assert.match(on([wide]).reason,/^R4 full: unsupported or incomplete SQL/)
  assert.equal(decide([wide]).mode,'full')
  assert.match(on(['internal/db/migrations/9999_missing.sql']).reason,/^R4 full: migration unreadable/)
  assert.match(decide(['internal/db/migrations/9999_missing.sql']).reason,/^R4 full: migration unreadable/)
  assert.match(on([narrow],{readFile:()=>'-- nothing'}).reason,/^R4 full: no schema object recognised/)
  assert.match(on([narrow],{readFile:()=>simple,tree:{go:new Map(),webTests:new Map()}}).reason,/^R4 full: no Go package references desk_notification_claims/)
  // Other database internals stay full; the migrations README belongs to internal/db.
  assert.match(on(['internal/db/db.go']).reason,/^unnarrowed risk: internal\/db\/db\.go/)
  const readme=on(['internal/db/migrations/README.md'])
  assert.equal(readme.full,false);assert.ok(goPackages(readme).has('internal/db'))
  assert.equal(decide(['internal/db/migrations/README.md']).layout,'full')
})

test('R4 never narrows partially understood SQL or omits quoted, qualified or string-adjacent targets',()=>{
  const path='internal/db/migrations/9999_fixture.sql'
  const rows=[g('internal/core','TestCore','ESSENTIAL'),g('internal/billing','TestBilling','GATED-FULL'),
    g('internal/db','TestMigrations','GATED-FULL'),g('internal/unrelated','TestUnrelated','GATED-FULL')]
  const tree={go:new Map([['internal/billing/billing.go','SELECT * FROM billing_records']]),webTests:new Map()}
  const selection=sql=>select(rows,{event:'pull_request',affectedLane:'on',paths:[path],tree,readFile:()=>sql})
  const safe="CREATE TABLE harmless (id uuid, note text DEFAULT 'a--b /* literal */');"
  for(const sql of [
    `${safe} DO $$ BEGIN UPDATE billing_records SET value=1; END $$;`,
    `${safe} WITH changed AS (SELECT 1) INSERT INTO billing_records (value) VALUES (1);`,
    `${safe} WITH changed AS (SELECT 1) UPDATE billing_records SET value=1;`,
    `${safe} WITH changed AS (SELECT 1) DELETE FROM billing_records;`,
    `${safe} TRUNCATE harmless, billing_records;`,
    `${safe} CREATE FUNCTION f() RETURNS void AS $body_42$ DELETE FROM billing_records; $body_42$ LANGUAGE sql;`,
    `${safe} SELECT update_billing_records();`,
    `${safe} INSERT INTO harmless (id) SELECT id FROM billing_records;`,
    `${safe} UPDATE harmless SET id=(DELETE FROM billing_records RETURNING id);`,
    `${safe} CREATE TABLE other (id uuid REFERENCES billing_records(id));`,
    `${safe} CREATE INDEX i ON harmless (write_billing_records());`,
    `${safe} UPDATE harmless SET note=E'billing_records';`,
    `${safe} INSERT INTO harmless (note) VALUES ($x_1$billing_records$x_1$);`,
    `${safe} ALTER TABLE harmless ADD COLUMN id uuid; billing_records`,
    `${safe} /* billing_records`, `${safe} 'billing_records`, `${safe} "billing_records`,
    `${safe} $tag_1$billing_records`, `${safe}; DELETE FROM billing_records;`,
  ]) {
    assert.equal(migrationObjects(sql),undefined,sql)
    const selected=selection(sql)
    assert.equal(selected.full,true,sql)
    assert.deepEqual(selected.tests,rows,sql)
    assert.match(selected.reason,/R4 full: unsupported or incomplete SQL/)
    assert.equal(schedulingDecision('pull_request',[path],()=>true,{affectedLane:'on',tree,readFile:()=>sql}).mode,'full',sql)
  }
  for(const target of ['billing_records','public.billing_records','"billing_records"','"public"."billing_records"']) {
    const sql=`${safe} UPDATE ${target} SET value=1 WHERE id=2;`
    assert.deepEqual([...migrationObjects(sql)],['harmless','billing_records'])
    const selected=selection(sql)
    assert.equal(selected.full,false,target)
    assert.deepEqual(selected.tests,rows.slice(0,3),target)
    assert.ok(selected.tests.includes(rows[1]),target)
    assert.ok(!selected.tests.includes(rows[3]),target)
  }
  assert.deepEqual([...migrationObjects("CREATE TABLE harmless (note text DEFAULT '--'); DELETE FROM billing_records;")],['harmless','billing_records'])
  assert.deepEqual([...migrationObjects('/* outer /* DELETE FROM fake */ end */ DELETE FROM "public"."billing_records"; -- tail')],['billing_records'])
  assert.equal(migrationObjects('DELETE FROM other.billing_records;'),undefined)
  assert.equal(migrationObjects('DELETE FROM "Public"."billing_records";'),undefined)
  assert.equal(migrationObjects('CREATE TABLE harmless (note text DEFAULT \'broken\\\'string\'); DELETE FROM billing_records;'),undefined)
  assert.equal(migrationObjects('x'.repeat(inputBounds.fileBytes)),undefined)
})

test('R4 seeded comment, string and dollar-body perturbations never hide an unparsed affected object',()=>{
  let seed=257
  const random=()=>{seed=(Math.imul(seed,1664525)+1013904223)>>>0;return seed}
  const gaps=[' ','\n','/* billing_records */','-- billing_records\n','/* nested /* body */ end */']
  const valid="CREATE TABLE harmless (id uuid, note text DEFAULT 'a--b');"
  for(let n=0;n<160;n++) {
    const gap=()=>gaps[random()%gaps.length]
    const body=[
      ['DO',`$tag_${n}$ BEGIN UPDATE billing_records SET note='-- /* */'; END $tag_${n}$`],
      ['WITH','q','AS','(','SELECT',"'-- billing_records'",')','DELETE','FROM','billing_records'],
      ['TRUNCATE','harmless',',','billing_records'],
      ['CREATE','FUNCTION','f','(',')','RETURNS','void','AS',`$$ DELETE FROM billing_records; $$`,'LANGUAGE','sql'],
    ][random()%4].join(gap())
    const sql=`${gap()}${valid}${gap()}${body};${gap()}`
    assert.equal(migrationObjects(sql),undefined,`seed case ${n}: ${sql}`)
  }
})

test('consumer scans preflight file, aggregate, per-file and traversal bounds before reading',()=>{
  const directory=mkdtempSync(resolve(tmpdir(),'aeon-consumer-bounds-'))
  const file=(path,text)=>{mkdirSync(resolve(directory,path,'..'),{recursive:true});writeFileSync(resolve(directory,path),text)}
  file('internal/a/a.go','api/contract.json')
  file('cmd/b/b.go','billing_records')
  file('web/tests/reader.test.ts','version.json')
  let reads=0
  const read=(...args)=>{reads++;return readInput(...args)}
  for(const [bounds,reason] of [
    [{...inputBounds,files:3},/file count/],
    [{...inputBounds,totalBytes:10},/total byte/],
    [{...inputBounds,fileBytes:5},/per-file byte/],
    [{...inputBounds,entries:2},/entry count/],
  ]) {
    reads=0
    const tree=sourceTree(directory,{bounds,read})
    assert.equal(tree.complete,false)
    assert.match(tree.reason,reason)
    assert.equal(reads,0,'all bounds must be checked before any file read')
    assert.equal(tree.go.size+tree.webTests.size,0)
    for(const path of ['api/contract.json','version.json','internal/db/migrations/9999_test.sql','internal/a/a.go']) {
      const options={event:'pull_request',affectedLane:'on',paths:[path],tree,readFile:()=>'DELETE FROM billing_records;'}
      assert.equal(select(cases,options).full,true,path)
      assert.equal(schedulingDecision('pull_request',[path],()=>true,{affectedLane:'on',tree}).mode,'full',path)
    }
  }
  const complete=sourceTree(directory,{read})
  assert.equal(complete.complete,true)
  assert.deepEqual([...complete.go.keys()].sort(),['cmd/b/b.go','internal/a/a.go'])
  assert.equal(complete.webTests.get('tests/reader.test.ts'),'version.json')
  const failed=sourceTree(directory,{read:()=>{throw new Error('fixture read error')}})
  assert.equal(failed.complete,false);assert.match(failed.reason,/fixture read error/)
  assert.equal(failed.go.size,0)
  assert.equal(sourceTree(resolve(directory,'missing')).complete,false)
  // Sparse files test real default bounds without allocating/reading their bytes.
  file('internal/a/huge.go','')
  truncateSync(resolve(directory,'internal/a/huge.go'),inputBounds.fileBytes)
  reads=0
  assert.equal(sourceTree(directory,{read}).complete,false)
  assert.equal(reads,0)
  assert.equal(boundedText(directory,'internal/a/huge.go'),undefined)
})

test('consumer scans reject binary, malformed UTF-8 and escaping symlinks, discarding partial reads',()=>{
  for(const bytes of [Buffer.from([0,1,2]),Buffer.from([1,2,3]),Buffer.from([0xff,0xfe])]) {
    const directory=mkdtempSync(resolve(tmpdir(),'aeon-consumer-binary-'))
    mkdirSync(resolve(directory,'internal/a'),{recursive:true})
    writeFileSync(resolve(directory,'internal/a/binary.go'),bytes)
    assert.equal(sourceTree(directory).complete,false)
    assert.equal(boundedText(directory,'internal/a/binary.go'),undefined)
  }
  for(const directoryLink of [false,true]) {
    const directory=mkdtempSync(resolve(tmpdir(),'aeon-consumer-link-'))
    const outside=mkdtempSync(resolve(tmpdir(),'aeon-consumer-outside-'))
    mkdirSync(resolve(directory,'internal/a'),{recursive:true})
    writeFileSync(resolve(outside,'escape.go'),'api/contract.json')
    symlinkSync(directoryLink?outside:resolve(outside,'escape.go'),resolve(directory,'internal/a',directoryLink?'escape':'escape.go'))
    const tree=sourceTree(directory)
    assert.equal(tree.complete,false);assert.match(tree.reason,/symlink/)
    assert.equal(tree.go.size,0)
    assert.equal(boundedText(directory,directoryLink?'internal/a/escape/escape.go':'internal/a/escape.go'),undefined)
  }
  const directory=mkdtempSync(resolve(tmpdir(),'aeon-consumer-growth-'))
  const path=resolve(directory,'migration.sql')
  writeFileSync(path,'DELETE FROM billing_records;')
  const metadata=inputMetadata(directory,'migration.sql')
  writeFileSync(path,'DELETE FROM billing_records; SELECT unknown();')
  assert.throws(()=>readInput(metadata),/changed during scan/)
})

test('R5 audit tooling and R9 always-on migration tests take the static layout; their executables stay full',()=>{
  for(const path of ['scripts/audit/common.py','scripts/audit/test_audit.py','scripts/audit/run.cjs','scripts/audit/rubric.md','scripts/check-migrations.test.mjs','scripts/migration_compat_probe_test.py']) {
    const selected=on([path])
    assert.equal(selected.full,false,path);assert.ok(essentialOnly(selected),path)
    if(!path.endsWith('.md')) assert.match(selected.reason,/; R[59]: /,path) // rubric.md is docs-like
    assert.deepEqual([decide([path]).mode,decide([path]).layout],['essential','static'],path)
  }
  for(const path of ['scripts/check-migrations.mjs','scripts/migration-compat.sh','scripts/migration-compat-probe.py']) assert.match(on([path]).reason,/^unnarrowed risk: /,path)
  assert.ok(realPaths.includes('scripts/audit/test_audit.py')&&realPaths.includes('scripts/check-migrations.test.mjs'))
})

test('test-pinned instruction files require the full gate under every affected switch state (AEON-766)',()=>{
  const rollout=JSON.parse(readFileSync(new URL('../rules-bootstrap/rollout.json',import.meta.url),'utf8'))
  const pinned=rollout.targets.flatMap(({target,candidate})=>[target,candidate])
  const switches=[undefined,'','off','ON','true',' on','on ','on']
  const gated=replayRows.filter(row=>row.tier==='ESSENTIAL'||row.tier==='GATED-FULL')
  for(const path of [...pinned,'docs/AGENTS.md','docs/nested/CLAUDE.md','web/AGENTS.md','internal/auth/CLAUDE.md','cmd/aeon/AGENTS.md',
    'internal/rulesimport/testdata/pack/AGENTS.md','scripts/audit/AGENTS.md','agents.md','docs/claude.MD','internal/auth/cLaUdE.mD']) {
    assert.equal(isDocsLike(path),false,path)
    for(const paths of [[path],['README.md',path]]) for(const affectedLane of switches) {
      const message=`${paths.join()}, switch ${affectedLane}`
      const selected=on(paths,{affectedLane})
      assert.equal(selected.full,true,message)
      assert.deepEqual(selected.tests,gated,message)
      // Model existing instruction files, so deletion fallback cannot hide a missing guard.
      const decision=schedulingDecision('pull_request',paths,()=>true,{affectedLane,graph:replayGraph,tree:repoTree,promotions:noPromotions})
      assert.deepEqual([decision.mode,decision.layout],['full','full'],message)
    }
  }
  for(const path of ['README.md','docs/x.md','docs/guide.txt','CHANGELOG.md','LICENSE']) for(const affectedLane of switches) {
    const message=`${path}, switch ${affectedLane}`
    const selected=on([path],{affectedLane})
    assert.equal(selected.full,false,message)
    assert.ok(essentialOnly(selected),message)
    const decision=schedulingDecision('pull_request',[path],()=>true,{affectedLane,graph:replayGraph,tree:repoTree,promotions:noPromotions})
    assert.equal(decision.mode,'essential',message)
    assert.equal(decision.layout,affectedLane==='on'?'static':'full',message)
  }
  for(const path of ['README.md','docs/x.md','web/README.md','CHANGELOG.md','LICENSE'])
    assert.equal(isDocsLike(path),true,path)
})

test('R6 release data, R7 test data and R8 harness pages select their readers; docs-like files are static',()=>{
  const release=on(['version.json','internal/releasehistory/data/product-notes.json'])
  assert.equal(release.full,false)
  for(const pkg of ['internal/releasehistory','internal/releases']) assert.ok(goPackages(release).has(pkg),pkg)
  assert.ok(webFiles(release).has('tests/calver3.test.ts'))
  assert.equal(decide(['version.json']).layout,'full')
  assert.match(on(['version.json'],{tree:{go:new Map(),webTests:new Map()}}).reason,/^R6 full: no Go consumer found/)
  const data=on(['internal/auth/testdata/key_scope_ceiling.json'])
  assert.equal(data.full,false)
  assert.ok(goPackages(data).has('internal/auth'),'owning package');assert.ok(webFiles(data).has('tests/access.test.ts'),'web reader by name')
  const lonely=on(['internal/auth/testdata/key_scope_ceiling.json'],{tree:{go:new Map(),webTests:new Map()}})
  assert.equal(lonely.full,false);assert.ok(goPackages(lonely).has('internal/auth'),'test data without other readers still runs its owner')
  const page=on(['web/tests/settings-shared-harness.html'])
  assert.equal(page.full,false);assert.ok(webFiles(page).has('tests/settings-shared.spec.ts'))
  assert.equal(decide(['web/tests/settings-shared-harness.html']).mode,'essential')
  for(const paths of [['docs/RELEASE.md'],['web/README.md'],['scripts/audit/rubric.md'],['README.md','LICENSE','CHANGELOG.md']]) {
    const selected=on(paths)
    assert.equal(selected.full,false,paths.join());assert.ok(essentialOnly(selected))
    assert.deepEqual([decide(paths).mode,decide(paths).layout],['essential','static'],paths.join())
  }
  assert.equal(decide([]).layout,'full','an empty diff keeps the shard layout')
  assert.match(on(['internal/auth/testdata/notes.md']).reason,/R7: test data owner/,'markdown inside testdata is test data, not docs')
  assert.equal(decide(['internal/auth/testdata/notes.md']).layout,'full')
})

test('layout is static only when every path is docs, manifest, audit or always-on test material',()=>{
  for(const [paths,layout] of [[['scripts/ci/web-test-tiers.json','README.md'],'static'],[['scripts/audit/slices.json','scripts/check-migrations.test.mjs'],'static'],
    [['scripts/ci/web-test-tiers.json','web/src/lib/rowStore.ts'],'full'],[['scripts/audit/common.py','internal/auth/key.go'],'full'],[['AGENTS.md','api/openapi.yaml'],'full']]) {
    const decision=decide(paths)
    assert.equal(decision.mode==='full'?'full':decision.layout,layout,paths.join())
  }
})

test('R3 real helpers retain essentials and select every mapped importing case, including transitive units',()=>{
  const helpers=[...new Set(replay.prs.flatMap(pr=>pr.paths).filter(path=>path.startsWith('web/tests/')&&uncertain(path)))]
  let narrowed=0
  for(const path of helpers) {
    const rule=affectedRisk(path,replayGraph)
    const selected=on([path])
    if(rule.full) { assert.equal(selected.full,true,path);continue }
    narrowed++
    assert.equal(selected.full,false,path)
    assert.match(selected.reason,/R3: helper reverse dependants/)
    const impacted=legacy.reverseDependants(new Set([path.slice(4)]),replayGraph)
    assert.deepEqual(selected.tests,replayRows.filter(row=>row.tier==='ESSENTIAL'||row.kind!=='go'&&impacted.has(row.file)))
  }
  assert.ok(narrowed>0,'The real data must exercise narrowing')
  const graph={'tests/helpers/shared.ts':[],'tests/helpers/reexport.ts':['tests/helpers/shared.ts'],
    'tests/feature.unit.test.ts':['tests/helpers/reexport.ts'],'tests/feature.spec.ts':['tests/helpers/reexport.ts']}
  const rows=[...cases,w('tests/feature.unit.test.ts','unit'),{...w('tests/feature.spec.ts','browser'),kind:'browser'}]
  const result=select(rows,{event:'pull_request',paths:['web/tests/helpers/shared.ts'],affectedLane:'on',webImports:graph})
  assert.deepEqual(result.tests,rows.filter(row=>row.tier==='ESSENTIAL'||row.name==='unit'||row.name==='browser'))
})

test('R3 fails closed on unknown, deleted, unimported and over-15-spec helpers',()=>{
  const path='web/tests/helpers/shared.ts',file=path.slice(4)
  for(const graph of [{},{[file]:[]}]) {
    assert.equal(select(cases,{event:'pull_request',paths:[path],affectedLane:'on',webImports:graph}).full,true)
    assert.equal(schedulingMode('pull_request',[path],()=>true,{affectedLane:'on',graph}),'full')
  }
  for(const n of [15,16]) {
    const rows=Array.from({length:n},(_,i)=>({...w(`tests/spec-${i}.spec.ts`,`${i}`),kind:'browser'}))
    const graph={[file]:[],...Object.fromEntries(rows.map(row=>[row.file,[file]]))}
    const result=select(rows,{event:'pull_request',paths:[path],affectedLane:'on',webImports:graph})
    assert.equal(result.full,n>15)
    assert.equal(schedulingMode('pull_request',[path],()=>true,{affectedLane:'on',graph}),n>15?'full':'essential')
    assert.equal(schedulingMode('pull_request',[path],()=>false,{affectedLane:'on',graph}),'full')
    if(n>15)assert.match(result.reason,/exceed 15 specs \(16\)/)
  }
})

test('trusted tier planner uses base classifier and base graph despite candidate rewrites',()=>{
  const directory=mkdtempSync(resolve(tmpdir(),'aeon-trusted-plan-test-'))
  const home=mkdtempSync(resolve(tmpdir(),'aeon-trusted-plan-home-'))
  const env={PATH:process.env.PATH,HOME:home,XDG_CONFIG_HOME:home,GIT_CONFIG_NOSYSTEM:'1',GIT_CONFIG_GLOBAL:'/dev/null',
    GIT_AUTHOR_NAME:'Fixture',GIT_AUTHOR_EMAIL:'fixture@example.invalid',GIT_COMMITTER_NAME:'Fixture',GIT_COMMITTER_EMAIL:'fixture@example.invalid',
    GITHUB_EVENT_NAME:'pull_request',CI_AFFECTED_LANE:'on'}
  const git=args=>execFileSync('git',args,{cwd:directory,env,encoding:'utf8'})
  const file=(path,source)=>{mkdirSync(resolve(directory,path,'..'),{recursive:true});writeFileSync(resolve(directory,path),source)}
  git(['init','-q'])
  for(const name of ['core.mjs','diff.mjs','collect.mjs','inputs.mjs','migration.mjs','manifests.mjs','failures.mjs'])file(`scripts/test-tiers/${name}`,readFileSync(new URL(name,import.meta.url),'utf8'))
  file('scripts/ci/web-test-tiers.json',JSON.stringify({tests:[]}))
  file('scripts/ci/go-test-tiers.json',JSON.stringify({tests:[]}))
  file('web/src/unused.ts','export {}')
  file('web/e2e/unused.ts','export {}')
  file('web/tests/helpers/shared.ts','export const value=1')
  file('web/tests/importer.spec.ts','import "./helpers/shared"')
  git(['add','.']);git(['commit','-qm','trusted fixture'])
  const base=git(['rev-parse','HEAD']).trim()
  // The candidate classifier lies, and candidate graph gains 16 importers.
  file('scripts/test-tiers/core.mjs','throw new Error("candidate classifier executed")')
  for(let i=0;i<16;i++)file(`web/tests/extra-${i}.spec.ts`,'import "./helpers/shared"')
  const run=paths=>trustedSchedulingDecision(base,paths,env,{cwd:directory})
  // Promotions compare the base snapshot's manifest with the candidate's.
  file('scripts/ci/web-test-tiers.json',JSON.stringify({tests:[{kind:'vitest',file:'tests/promoted.unit.test.ts',name:'n',tier:'GATED-FULL'}]}))
  let manifest=run(['scripts/ci/web-test-tiers.json'])
  assert.deepEqual([manifest.mode,manifest.layout],['essential','static'])
  assert.match(manifest.reason,/R1: 1 promoted vitest cases execute/)
  file('scripts/ci/web-test-tiers.json',JSON.stringify({tests:[{kind:'browser',file:'tests/promoted.spec.ts',name:'n',tier:'ESSENTIAL'}]}))
  manifest=run(['scripts/ci/web-test-tiers.json'])
  assert.deepEqual([manifest.mode,manifest.layout],['essential','full'])
  file('scripts/ci/web-test-tiers.json',JSON.stringify({tests:[]}))
  assert.deepEqual(run(['scripts/ci/web-test-tiers.json','README.md']),{mode:'essential',layout:'static',reason:'R1: manifest/static checks: scripts/ci/web-test-tiers.json'})
  assert.equal(run(['web/tests/helpers/shared.ts']).mode,'essential')
  assert.equal(run(['web/tests/helpers/shared.ts']).layout,'full')
  assert.match(run(['web/tests/helpers/shared.ts']).reason,/R3:.*1 specs/)
  assert.equal(run(['scripts/test-tiers/core.mjs','web/tests/helpers/shared.ts']).mode,'full')
  assert.equal(run(['web/tests/extra-0.spec.ts','web/tests/helpers/shared.ts']).mode,'full','new candidate graph nodes must not narrow themselves')
  file('web/tests/new-feature.unit.test.ts','export {}')
  const paths=['web/tests/new-feature.unit.test.ts']
  const trusted=run(paths)
  const candidateGraph=webGraph(resolve(directory,'web'))
  const candidate=schedulingDecision('pull_request',paths,()=>true,{affectedLane:'on',graph:candidateGraph,checkout:directory})
  assert.equal(trusted.mode,'full','the base graph has no new file')
  assert.equal(candidate.mode,'essential','the candidate graph can map its new file')
  const rows=[w('tests/core.test.ts','core','ESSENTIAL'),{kind:'vitest',file:'tests/new-feature.unit.test.ts',name:'new feature',tier:'GATED-FULL'}]
  const options={event:'pull_request',paths,affectedLane:'on',webImports:candidateGraph,tree:sourceTree(directory)}
  const selected=runnerSelection(rows,options,candidate,trusted)
  assert.equal(selected.full,true)
  assert.equal(selected.scope,'gated-full')
  assert.deepEqual(selected.tests,rows)
  assert.equal(checkFull({...reportCases(rows,rows.map(row=>({key:key(row),status:'passed',started:true})),1,'web-unit-1'),
    full:selected.full,scope:selected.scope,exitCode:0,runId:'1',attempt:'1',sha:'a'},
    {GITHUB_RUN_ID:'1',GITHUB_RUN_ATTEMPT:'1',GITHUB_SHA:'a'}),true)
  // A newly added browser spec is raw spec-only, but absent from the base
  // graph. The effective lane must reach tier inventory/full checks, rather
  // than bypassing them through the old exact-spec branch.
  const browserPath='web/tests/new-feature.spec.ts'
  file(browserPath,'export {}')
  const browserTrusted=run([browserPath])
  const browserGraph=webGraph(resolve(directory,'web'))
  const browserCandidate=schedulingDecision('pull_request',[browserPath],()=>true,{affectedLane:'on',graph:browserGraph,checkout:directory})
  assert.equal(browserTrusted.mode,'full')
  assert.equal(browserCandidate.mode,'essential')
  assert.equal(effectiveLane('spec-only',{...browserTrusted,event:'pull_request',affectedLane:'on'}),'full')
  assert.equal(effectiveLane('spec-only',{...browserTrusted,event:'pull_request',affectedLane:'off'}),'spec-only')
  const browserRows=[rows[0],{kind:'browser',file:browserPath.slice(4),name:'new feature',tier:'GATED-FULL'}]
  const browserSelected=runnerSelection(browserRows,{...options,paths:[browserPath],webImports:browserGraph},browserCandidate,browserTrusted)
  assert.equal(browserSelected.full,true)
  assert.deepEqual(browserSelected.tests,browserRows)
  const deleted=['web/tests/deleted.unit.test.ts']
  const deletedPlan=run(deleted)
  assert.equal(deletedPlan.mode,'full')
  assert.equal(runnerSelection(rows,{...options,paths:deleted},candidate,deletedPlan).full,true)
  const staticPaths=['scripts/audit/check.py']
  const staticPlan=run(staticPaths)
  const staticCandidate=schedulingDecision('pull_request',staticPaths,()=>true,{affectedLane:'on',checkout:directory})
  const staticSelection=runnerSelection(rows,{...options,paths:staticPaths},staticCandidate,staticPlan)
  assert.equal(staticSelection.full,false)
  assert.equal(staticSelection.layout,'static')
  assert.deepEqual(staticSelection.tests,[rows[0]])
  // A missing/old classifier is represented by the planner's explicit full
  // fallback. The runner must retain it even with candidate graph metadata.
  let fallback
  try { fallback=trustedSchedulingDecision('0'.repeat(40),paths,env,{cwd:directory}) }
  catch { fallback={mode:'full',layout:'full',reason:'trusted affected classification unavailable'} }
  assert.equal(fallback.reason,'trusted affected classification unavailable')
  assert.equal(runnerSelection(rows,options,candidate,fallback).full,true)
  assert.throws(()=>run(['x'.repeat(4097)]),/Invalid trusted paths/)
  assert.throws(()=>trustedSchedulingDecision('--bad',[],env,{cwd:directory}),/Invalid trusted planner base/)
})

test('runner modes honour planner input and candidate disagreement only widens',()=>{
  const candidate={mode:'essential',layout:'full',reason:'candidate mapping'}
  const staticCandidate={...candidate,layout:'static'}
  assert.equal(runnerDecision(candidate,{mode:'full'}).mode,'full')
  for(const mode of ['essential','spec-only'])assert.deepEqual(runnerDecision(candidate,{mode}),candidate)
  assert.deepEqual(runnerDecision(staticCandidate,{mode:'static'}),staticCandidate)
  assert.deepEqual(runnerDecision(staticCandidate,{mode:'essential',layout:'static'}),staticCandidate)
  assert.equal(runnerDecision(candidate,{mode:'static'}).mode,'full')
  assert.equal(runnerDecision(staticCandidate,{mode:'essential',layout:'full'}).mode,'full')
  for(const mode of ['essential','static','spec-only'])assert.equal(runnerDecision({mode:'full',layout:'full',reason:'candidate uncertainty'},{mode}).mode,'full')
  for(const mode of ['',null,'FULL','bogus'])assert.equal(runnerDecision(candidate,{mode}).mode,'full')
  assert.equal(runnerDecision(candidate,{mode:'essential',layout:'bogus'}).mode,'full')
  assert.deepEqual(runnerDecision(candidate),candidate)
})

test('merge-group planner writes full without needing payload, checkout history or a network fetch',()=>{
  const directory=mkdtempSync(resolve(tmpdir(),'aeon-merge-full-'))
  for(const affectedLane of ['','off','on']) {
    const output=resolve(directory,`output-${affectedLane}`)
    assert.equal(tierPlanMain({GITHUB_EVENT_NAME:'merge_group',CI_AFFECTED_LANE:affectedLane,GITHUB_OUTPUT:output}),0)
    assert.equal(readFileSync(output,'utf8'),'mode=full\nlayout=full\nlane=full\n')
    for(const raw of ['full','spec-only','docs-only']) {
      writeFileSync(output,'')
      tierPlanMain({GITHUB_EVENT_NAME:'merge_group',CI_PLAN_LANE:raw,CI_AFFECTED_LANE:affectedLane,GITHUB_OUTPUT:output})
      assert.equal(readFileSync(output,'utf8'),`mode=full\nlayout=full\nlane=${affectedLane==='on'?'full':raw}\n`)
    }
  }
})

test('impact bounds reject malformed and oversized paths before building a graph or snapshot',()=>{
  for(const paths of [[null],['../web/tests/helper.ts'],['web//tests/file.ts'],['/absolute'],['web/tests/a\nb.ts'],
    ['web/tests/'+ 'x'.repeat(4097)],Array.from({length:10001},()=> 'README.md')]) {
    const options={event:'pull_request',paths,affectedLane:'on'}
    assert.equal(select(cases,options).reason,'invalid or oversized impact diff')
    assert.deepEqual(schedulingDecision('pull_request',paths),{mode:'full',layout:'full',reason:'invalid or oversized impact diff'})
    assert.throws(()=>trustedSchedulingDecision('a'.repeat(40),paths),/Invalid trusted paths/)
  }
})

test('offline replay CLI prints every real PR with selected counts, effective lane and reasons',()=>{
  const result=spawnSync(process.execPath,[fileURLToPath(new URL('./replay.mjs',import.meta.url)),'--json'],{encoding:'utf8',timeout:30_000})
  assert.equal(result.status,0,result.stderr)
  const report=JSON.parse(result.stdout)
  assert.equal(report.replay.length,80)
  // Measured on the current tree (consumers, migrations); deleted files replay as full.
  // The merge keeps PR 200 and PR 245 over the 1000-case Go consumer bound (openapi fan-out).
  assert.deepEqual(report.transitions,{'full->full':50,'full->essential':16,'essential->essential':14})
  assert.equal(report.summary.newNarrowed,30)
  assert.equal(report.summary.oldEssential,14)
  assert.ok(report.summary.fullReasons['CI machinery']>=15)
  for(const row of report.replay) {
    assert.ok(row.oldCases>0&&row.newCases>0)
    assert.ok(row.reason.length>0)
    assert.ok(['full','spec-only','docs-only'].includes(row.lane))
    assert.ok(['full','essential','static'].includes(row.new))
    assert.equal(row.newCases,Object.values(row.newKinds).reduce((sum,n)=>sum+n,0))
    assert.ok(row.new==='full'||row.newCases<=row.oldCases,`PR ${row.number} narrowed selection must not grow`)
    assert.equal(row.newJobs,row.lane==='spec-only'?12:row.new==='static'?16:row.new==='essential'?22:37)
  }
})

test('AEON-697 Go attention regressions are classified and permission and revision guards remain essential',()=>{
  const manifest=JSON.parse(readFileSync(new URL('../ci/go-test-tiers.json',import.meta.url)))
  const discovered=[]
  for(const file of ['internal/db/attention_events_migration_test.go',
    'internal/releases/node_undo_integration_test.go','internal/statusautopilot/attention_test.go']) {
    const source=readFileSync(new URL(`../../${file}`,import.meta.url),'utf8')
    const names=[...source.matchAll(/^func (Test\w+)\(t \*testing\.T\)/gm)].map(match=>match[1])
    assert.ok(names.length>0,`${file} must contain regression cases`)
    for(const name of names)discovered.push({kind:'go',package:file.slice(0,file.lastIndexOf('/')),name})
  }
  const ids=new Set(discovered.map(key)),warnings=[]
  const stored={...manifest,tests:manifest.tests.filter(row=>ids.has(key(row)))}
  assert.deepEqual(stored.tests.map(key).sort(),discovered.map(key).sort(),'Every Go attention regression needs an explicit tier entry')
  const rows=validate(stored,discovered,undefined,
    {strict:true,warn:warning=>warnings.push(warning)})
  assert.deepEqual(warnings,[])
  assert.ok(rows.every(row=>row.tier==='ESSENTIAL'||row.tier==='GATED-FULL'))
  for(const event of ['pull_request','merge_group']) {
    const core=select(rows,{event,paths:['README.md']}).tests
    for(const name of ['TestAttentionChecksRevocationUnderFinalWriteFence','TestAdditionalAttentionFencePreservesAuthenticationGuard',
      'TestAttentionResolutionVisibilityPreservesHiddenReferences','TestAttentionUndoRequiresPermissionAndActorOwnership',
      'TestAttentionReleaseUndoRejectsExternalProjectAndReleaseChanges','TestAttentionBulkReturnsPartialResultsAndGuardsUndo']) {
      assert.ok(core.some(row=>row.name===name),`${event} must retain ${name}`)
    }
    assert.deepEqual(select(rows,{event,forceFull:true}).tests.map(key).sort(),rows.map(key).sort())
  }
})

test('AEON-648 pending writes and identity fences stay protected without promoting ordinary display tests',()=>{
  const manifest=JSON.parse(readFileSync(new URL('../ci/web-test-tiers.json',import.meta.url)))
  for(const [file,names] of [
    ['tests/parent-benefit-generation.unit.test.ts',[
      'retry captures its generation/revision and discards a result after navigation',
      'tenant changes fence an old status read even for the same node id',
      'an old provider save cannot restore another person’s opt-in or drafts',
    ]],
    ['tests/work-vocabulary-card.unit.test.ts',['serializes a deferred Reload with Save and leaves the saved revision visible']],
  ]) for(const name of names) {
    const row=manifest.tests.find(row=>row.file===file&&row.name===name)
    assert.equal(row?.tier,'ESSENTIAL',`${file}: ${name}`)
    for(const event of ['pull_request','merge_group'])
      assert.deepEqual(select([row],{event,paths:['README.md']}).tests,[row])
  }
  const display=manifest.tests.find(row=>row.file==='tests/work-benefit-visibility.unit.test.ts'&&row.name==='work offers its benefit reading section in the workspace')
  assert.equal(display?.tier,'NIGHTLY')
})

test('runtime reconciliation defaults new cases to NIGHTLY and drops stale cases with named warnings',()=>{
  const stale=cases[0],added={kind:'go',package:'internal/auth',name:'TestNew',active:false}
  const discovered=[...cases.slice(1),added],warnings=[],manifest=fixture(),before=structuredClone(manifest)
  const rows=validate(manifest,discovered,noFlaky,{warn:message=>warnings.push(message)})
  assert.deepEqual(rows.map(key),discovered.map(key))
  assert.deepEqual(rows.at(-1),{...added,tier:'NIGHTLY'})
  assert.equal(rows.find(row=>row.name==='TestCRUD').tier,'ESSENTIAL')
  assert.deepEqual(warnings,[`::warning::Unclassified test (defaults to NIGHTLY; classify these): ${key(added)}`,
    `::warning::Stale manifest entry (dropped): ${key(stale)}`])
  for(const event of ['pull_request']) {
    assert.ok(select(rows,{event,paths:['internal/auth/key.go']}).tests.some(row=>key(row)===key(added)))
    assert.ok(!select(rows,{event,paths:['README.md']}).tests.some(row=>key(row)===key(added)))
  }
  assert.deepEqual(select(rows,{event:'schedule',paths:[]}).tests,rows)
  assert.deepEqual(manifest,before, 'Reconciliation must leave the stored manifest unchanged')
})

test('runtime reconciliation escapes warning titles and retains browser configuration validation',()=>{
  const row={kind:'browser',file:'tests/new.spec.ts',name:'new\ncase%',config:'playwright.ui.config.ts',project:'',line:12,id:'native'}
  const warnings=[]
  const rows=validate(fixture(),[...cases,row],noFlaky,{warn:message=>warnings.push(message)})
  assert.deepEqual(rows.at(-1),{...row,tier:'NIGHTLY',active:true})
  assert.deepEqual(warnings,[`::warning::Unclassified test (defaults to NIGHTLY; classify these): ${key(row).replaceAll('%','%25').replaceAll('\n','%0A')}`])
  for(const strict of [false,true]) assert.throws(()=>validate({version:1,tests:[{...row,config:'old.config.ts',tier:'ESSENTIAL'}]},[row],noFlaky,{strict}),/Changed browser configuration/)
})

test('reconciliation never hides malformed, duplicate or known-flaky ESSENTIAL classifications',()=>{
  for(const strict of [false,true]) {
    assert.throws(()=>validate({...fixture(),tests:[{...cases[0],tier:'UNKNOWN'}]},[],noFlaky,{strict}),/Invalid classification/)
    assert.throws(()=>validate({...fixture(),tests:[cases[0],cases[0]]},[],noFlaky,{strict}),/Duplicate manifest entry/)
    assert.throws(()=>validate(fixture(),[cases[0],cases[0]],noFlaky,{strict}),/Duplicate collected identity/)
    assert.throws(()=>validate(fixture(),[],{version:1,entries:[{key:key(cases[0]),owner:'AEON-675'}]},{strict}),/Known-flaky case cannot be ESSENTIAL/)
  }
})

test('strict checks reject unknown and stale registrations; tagging never removes a nightly test',()=>{
  assert.throws(()=>validate(fixture(),[...cases,g('internal/auth','TestNew')],noFlaky,{strict:true}),/Unclassified test.*TestNew/)
  assert.throws(()=>validate(fixture(),cases.slice(1),noFlaky,{strict:true}),/Stale manifest entry.*TestCeiling/)
  const manifest=fixture();manifest.tests[1].tags=['delete-candidate']
  const rows=validate(manifest,cases)
  const full=select(rows,{event:'schedule',paths:[]})
  assert.equal(full.tests.length,cases.length)
  assert.ok(full.tests.find(row=>row.name==='TestRotation').tags.includes('delete-candidate'))
  manifest.tests[0].tags=['delete-candidate']
  assert.throws(()=>validate(manifest,cases),/Invalid classification/)
})

test('known-flaky cases cannot enter ESSENTIAL, but remain in changed-area and nightly execution',()=>{
  for (const row of [g('internal/importer','TestSourceRequestCapAndDelay'),
    {kind:'browser',file:'tests/clip-tip.spec.ts',name:'touch disclosure',config:'playwright.ui.config.ts',project:'',tier:'NIGHTLY'}]) {
    const manifest={version:1,tests:[{...row,tier:'ESSENTIAL'}]}
    const known={version:1,entries:[{key:key(row),owner:row.kind==='go'?'AEON-675':'AEON-676'}]}
    assert.throws(()=>validate(manifest,[row],known),error=>
      error.message===`Known-flaky case cannot be ESSENTIAL: ${key(row)} (${known.entries[0].owner})`)
    manifest.tests[0].tier='NIGHTLY'
    const retained=validate(manifest,[row],known)
    assert.deepEqual(select(retained,{event:'pull_request',paths:['README.md']}).tests,[])
    assert.deepEqual(select(retained,{event:'schedule',paths:[]}).tests,retained)
    const paths=row.kind==='go'?['internal/importer/source.go']:[`web/${row.file}`]
    assert.deepEqual(select(retained,{event:'pull_request',paths,webImports:{[row.file]:[]}}).tests,retained)
    assert.deepEqual(select(retained,{event:'merge_group',paths,webImports:{[row.file]:[]}}).tests,[])
  }
})

test('known-flaky registry fails closed on missing ownership and duplicate case keys',()=>{
  for(const entry of [{key:key(cases[0])},{key:'',owner:'AEON-675'},{key:key(cases[0]),owner:'675'}])
    assert.throws(()=>validate(fixture(),cases,{version:1,entries:[entry]}),/case key and owner ticket/)
  const entry={key:key(cases[1]),owner:'AEON-675'}
  assert.throws(()=>validate(fixture(),cases,{version:1,entries:[entry,entry]}),/Duplicate known-flaky entry/)
  assert.throws(()=>validate(fixture(),cases,{version:2,entries:[]}),/registry version 1/)
})

test('known-flaky committed cases retain exact owners and cannot be promoted by the CLI default validator',()=>{
  const known=JSON.parse(readFileSync(new URL('../ci/known-flaky.json',import.meta.url)))
  const manifests=['go','web'].map(kind=>JSON.parse(readFileSync(new URL(`../ci/${kind}-test-tiers.json`,import.meta.url))))
  assert.equal(known.entries.length,9)
  assert.deepEqual(Object.fromEntries(['AEON-675','AEON-676','AEON-683'].map(owner=>
    [owner,known.entries.filter(entry=>entry.owner===owner).length])),{'AEON-675':1,'AEON-676':1,'AEON-683':7})
  for(const entry of known.entries) {
    const manifest=manifests.find(manifest=>manifest.tests.some(row=>key(row)===entry.key))
    assert.ok(manifest,`Stale known-flaky case: ${entry.key}`)
    const row=manifest.tests.find(row=>key(row)===entry.key)
    assert.equal(row.tier,'GATED-FULL',entry.key)
    assert.ok(select(manifest.tests,{event:'schedule',paths:[]}).tests.some(row=>key(row)===entry.key))
    const promoted=structuredClone(manifest)
    promoted.tests.find(row=>key(row)===entry.key).tier='ESSENTIAL'
    assert.throws(()=>validate(promoted,manifest.tests),error=>
      error.message===`Known-flaky case cannot be ESSENTIAL: ${entry.key} (${entry.owner})`)
  }
})

test('PR takes essential union changed package and reverse dependencies; docs retain core',()=>{
  const imports={'cmd/aeon':['internal/auth'],'internal/auth':[]}
  for(const event of ['pull_request']) {
    const docs=select(cases,{event,paths:['README.md','docs/RELEASE.md'],imports})
    assert.equal(docs.full,false)
    assert.deepEqual(docs.tests.map(row=>row.name),['TestCeiling','TestCRUD','ceiling'])
    const picked=select(cases,{event,paths:['docs/RELEASE.md','internal/auth/key.go'],imports})
    assert.equal(picked.full,false)
    assert.deepEqual(picked.tests.map(row=>row.name),['TestCeiling','TestRotation','TestCRUD','TestRoutes','ceiling'])
  }
})

test('large optional reverse-dependency expansion keeps all changed-package tests within the core union',()=>{
  const many=[...cases,...Array.from({length:301},(_,i)=>g('internal/dependant',`TestExtra${i}`))]
  const selected=select(many,{event:'pull_request',paths:['internal/auth/key.go'],imports:{'internal/dependant':['internal/auth']}})
  assert.equal(selected.full,false)
  assert.ok(selected.tests.some(row=>row.name==='TestRotation'))
  assert.ok(selected.tests.some(row=>row.name==='TestCRUD'))
  assert.ok(!selected.tests.some(row=>row.package==='internal/dependant'))
  assert.match(selected.reason,/optional Go reverse dependencies exceed 300/)
})

test('uncertain and missing metadata select full on every premerge event',()=>{
  for(const path of ['go.mod','go.sum','api/openapi.yaml','.github/workflows/ci.yml',
    'internal/db/migrations/9999.sql','internal/auth/testdata/token.json','web/tests/access-fixtures.ts',
    'web/vite.config.ts','web/package-lock.json','scripts/test-tiers/core.mjs','unexpected.input']) {
    for(const event of ['pull_request','merge_group']) assert.equal(select(cases,{event,paths:[path]}).full,true,path)
  }
  assert.equal(select(cases,{event:'merge_group'}).full,true)
  assert.equal(select(cases,{event:'pull_request',paths:['web/src/deleted.ts'],webImports:{}}).full,true)
})

test('fan-out chooses full shard counts for uncertain and deleted paths, small counts for mapped changes',()=>{
  for(const event of ['pull_request']) {
    assert.equal(schedulingMode(event,['README.md']), 'essential')
    assert.equal(schedulingMode(event,['internal/auth/flow.go'],()=>true),'essential')
    assert.equal(schedulingMode(event,['.github/workflows/ci.yml']), 'full')
    assert.equal(schedulingMode(event,['internal/removed/file.go'],()=>false),'full')
    assert.equal(schedulingMode(event,undefined),'full')
  }
  assert.equal(schedulingMode('schedule',['README.md']),'full')
  assert.equal(schedulingMode('merge_group',['README.md']),'full')
  assert.equal(schedulingMode('merge_group',['internal/auth/flow.go'],()=>true),'full')
})

test('uncertain infrastructure changes preserve the classified gate plus promoted essentials',()=>{
  const browser=(file,name,tier='NIGHTLY')=>({kind:'browser',file,name,tier})
  const rows=[...cases.map(row=>({...row,tier:row.tier==='NIGHTLY'?'GATED-FULL':row.tier})),browser('tests/gated.spec.ts','guard','GATED-FULL'),browser('tests/optional.spec.ts','core','ESSENTIAL'),
    browser('tests/optional.spec.ts','gallery')]
  for(const event of ['pull_request','merge_group','push']) {
    const picked=select(rows,{event,paths:['.github/workflows/ci.yml','web/package.json','web/playwright.ui.config.ts']})
    assert.equal(picked.full,true)
    assert.deepEqual(picked.tests,rows.slice(0,-1))
    assert.equal(picked.scope,'gated-full')
    assert.equal(picked.deferredBrowserCases,1)
  }
  for(const options of [{event:'schedule'},{event:'workflow_dispatch',forceAll:true},{event:'pull_request',forceAll:true}]) {
    const picked=select(rows,{...options,paths:['.github/workflows/ci.yml']})
    assert.deepEqual(picked.tests,rows)
    assert.equal(picked.scope,'catalogue')
    assert.equal(picked.deferredBrowserCases,0)
  }
})

test('optional changed browser cases run in changed-area selection; uncertain impact widens only to the gate',()=>{
  const rows=[{kind:'browser',file:'tests/optional.spec.ts',name:'race',tier:'NIGHTLY'},
    {kind:'browser',file:'tests/other.spec.ts',name:'other',tier:'GATED-FULL'}]
  const webImports={'src/store.ts':[],'tests/optional.spec.ts':['src/store.ts'],'tests/other.spec.ts':[]}
  for(const path of ['web/tests/optional.spec.ts','web/src/store.ts']) {
    const picked=select(rows,{event:'pull_request',paths:[path],webImports})
    assert.deepEqual(picked.tests,[rows[0]])
    assert.equal(picked.full,false)
  }
  for(const path of ['web/src/deleted.ts','web/src/Unmapped.vue']) {
    const picked=select(rows,{event:'pull_request',paths:[path],webImports:{...webImports,'src/Unmapped.vue':[]}})
    assert.deepEqual(picked.tests,[rows[1]],path)
    assert.equal(picked.deferredBrowserCases,1,path)
  }
})

test('lean planner fetches only the exact event base before diffing and widens on fetch failure',()=>{
  const directory=mkdtempSync(resolve(tmpdir(),'aeon-tier-planner-'))
  const eventPath=resolve(directory,'event.json'),base='a'.repeat(40)
  for(const event of ['pull_request','merge_group']) {
    writeFileSync(eventPath,JSON.stringify(event==='pull_request'?{pull_request:{base:{sha:base}}}:{merge_group:{base_sha:base}}))
    const calls=[]
    const exec=(bin,args,options)=>{calls.push({bin,args,options});return args[0]==='diff'?'README.md\0internal/auth/key.go\0':''}
    assert.deepEqual(changedPaths(event,{GITHUB_EVENT_PATH:eventPath},{fetchBase:true,exec}),['README.md','internal/auth/key.go'])
    assert.deepEqual(calls,[
      {bin:'git',args:['fetch','--no-tags','--depth=1','origin',base],options:{timeout:30_000}},
      {bin:'git',args:['diff','--name-only','-z','--no-renames',base,'HEAD'],options:{timeout:30_000}}])
    const failed=changedPaths(event,{GITHUB_EVENT_PATH:eventPath},{fetchBase:true,exec:()=>{throw new Error('fetch refused')}})
    assert.equal(failed,undefined)
    assert.equal(schedulingMode(event,failed),'full')
  }
  writeFileSync(eventPath,JSON.stringify({pull_request:{base:{sha:'--invalid'}}}))
  const invalidCalls=[]
  assert.equal(changedPaths('pull_request',{GITHUB_EVENT_PATH:eventPath},{fetchBase:true,exec:(...args)=>{invalidCalls.push(args);return ''}}),undefined)
  assert.deepEqual(invalidCalls,[], 'Invalid base must never be fetched')
})

test('lean tier-plan skips setup, deep checkout and non-premerge checkout work',()=>{
  const ci=readFileSync(new URL('../../.github/workflows/ci.yml',import.meta.url),'utf8')
  const planner=ci.slice(ci.indexOf('\n  tier-plan:'),ci.indexOf('\n  runner-route:'))
  assert.match(planner,/timeout-minutes: 2/)
  assert.match(planner,/fetch-depth: 1\n/)
  assert.doesNotMatch(planner,/setup-node|setup-go|npm ci|fetch-depth: 0/)
  assert.match(planner,/if: contains\(fromJSON\('\["pull_request","merge_group"\]'\), github.event_name\)/)
  assert.match(planner,/pull_request\) node scripts\/test-tiers\/diff.mjs/)
  assert.match(planner,/merge_group\) printf 'mode=full\\nlayout=full\\nlane=full\\n' >> "\$GITHUB_OUTPUT" ;;/)
  assert.match(planner,/printf 'mode=full\\nlayout=full\\nlane=%s\\n'/)
  assert.match(planner,/CI_PLAN_LANE: \$\{\{ needs.ci-plan.outputs.lane \}\}/)
  assert.match(planner,/^      layout: \$\{\{ steps.tiers.outputs.layout \}\}$/m)
})

test('workflow merge-group planner writes only full outputs without executing candidate code; PR output is unchanged',()=>{
  const ci=readFileSync(new URL('../../.github/workflows/ci.yml',import.meta.url),'utf8')
  const planner=ci.slice(ci.indexOf('\n  tier-plan:'),ci.indexOf('\n  runner-route:'))
  const run=planner.split('        run: |\n')[1].replace(/^          /gm,'')
  const directory=mkdtempSync(resolve(tmpdir(),'aeon-workflow-tier-plan-'))
  mkdirSync(resolve(directory,'scripts/test-tiers'),{recursive:true})
  writeFileSync(resolve(directory,'scripts/test-tiers/diff.mjs'),`
    import {appendFileSync} from 'node:fs';
    appendFileSync(process.env.GITHUB_OUTPUT,'mode=essential\\nlayout=static\\nlane=full\\n');
    console.log('candidate planner executed');
    process.exit(Number(process.env.CANDIDATE_STATUS));
  `)
  for(const event of ['merge_group','pull_request']) for(const flag of ['on','off',''])
    for(const lane of ['full','spec-only','docs-only']) for(const code of ['0','7']) {
      const output=resolve(directory,`${event}-${flag}-${lane}-${code}`)
      writeFileSync(output,'')
      const result=spawnSync('/bin/bash',['-e','-c',run],{cwd:directory,encoding:'utf8',timeout:10_000,
        env:{PATH:process.env.PATH,GITHUB_EVENT_NAME:event,GITHUB_OUTPUT:output,CI_AFFECTED_LANE:flag,CI_PLAN_LANE:lane,CANDIDATE_STATUS:code}})
      assert.equal(result.status,event==='merge_group'?0:Number(code),result.stderr)
      assert.equal(readFileSync(output,'utf8'),event==='merge_group'?'mode=full\nlayout=full\nlane=full\n':'mode=essential\nlayout=static\nlane=full\n')
      assert.equal(result.stdout,event==='merge_group'?'':'candidate planner executed\n')
    }
})

test('static layout accounting expects skipped Go shards, timing and browser shards but still requires unit evidence',()=>{
  const finished={status:'completed',conclusion:'success',started_at:'2026-10-06T01:00:00Z',completed_at:'2026-10-06T01:02:00Z'}
  const skipped={status:'completed',conclusion:'skipped',started_at:'2026-10-06T01:00:00Z',completed_at:'0001-01-01T00:00:00Z',steps:[]}
  const jobs=[{...finished,name:'tier-plan'},{...finished,name:'web-setup'},{...finished,name:'web-unit (1)'},{...skipped,name:'go-test (1)'},{...skipped,name:'go-timing'},{...skipped,name:'web-shard (1)'},{...skipped,name:'e2e-run'}]
  const unit={version:1,job:'web-unit-1',classes:{ESSENTIAL:{selected:1,run:1,passed:1,skipped:0,failed:0,notRun:0,platformInactive:0}}}
  const report=aggregate([unit],jobs,{lane:'full',layout:'static'})
  assert.equal(report.coverage,'reported');assert.deepEqual(report.missingEvidence,[]);assert.deepEqual(report.skippedJobs,[])
  const withoutUnits=aggregate([],jobs,{lane:'full',layout:'static'})
  assert.equal(withoutUnits.coverage,'incomplete');assert.deepEqual(withoutUnits.missingEvidence,['web-unit-1'])
  const fullLayout=aggregate([unit],jobs,{lane:'full',layout:'full'})
  assert.equal(fullLayout.coverage,'incomplete');assert.deepEqual(fullLayout.skippedJobs,['go-test (1)','go-timing','web-shard (1)'])
  assert.throws(()=>aggregate([unit],jobs,{lane:'full',layout:'wide'}),/Invalid tier layout/)
})

test('changed modules select dependent specs/units; equal file basenames do not broaden impact',()=>{
  const graph={'src/ceiling.ts':[],'tests/scope.test.ts':['src/ceiling.ts'],'tests/tree.test.ts':[]}
  const selected=select(cases,{event:'pull_request',paths:['web/src/ceiling.ts'],webImports:graph})
  assert.deepEqual(selected.tests.filter(row=>row.kind==='node').map(row=>row.name),['ceiling','revoked'])
  assert.deepEqual(select(cases,{event:'pull_request',paths:['web/tests/tree.test.ts'],webImports:graph}).tests.filter(row=>row.kind==='node').map(row=>row.name),['ceiling','tree'])
})

test('a mapped Go change does not widen unrelated web tests; unmapped rendered components widen web',()=>{
  const goOnly=cases.filter(row=>row.kind==='go'),webOnly=cases.filter(row=>row.kind!=='go')
  assert.equal(select(webOnly,{event:'pull_request',paths:['internal/auth/flow.go']}).full,false)
  assert.deepEqual(select(webOnly,{event:'pull_request',paths:['internal/auth/flow.go']}).tests.map(row=>row.name),['ceiling'])
  assert.equal(select(goOnly,{event:'pull_request',paths:['web/src/ceiling.ts']}).full,false)
  assert.equal(select(webOnly,{event:'pull_request',paths:['web/src/Unmapped.vue'],webImports:{'src/Unmapped.vue':[]}}).full,true)
})

test('web dependency graph follows Vue, TS, export and index imports transitively',()=>{
  const root=mkdtempSync(resolve(tmpdir(),'aeon-tier-graph-'))
  for(const directory of ['src','src/lib','tests','e2e'])mkdirSync(resolve(root,directory),{recursive:true})
  writeFileSync(resolve(root,'src/Screen.vue'),'<template/><script setup>import { grant } from "./lib"</script>')
  writeFileSync(resolve(root,'src/lib/index.ts'),'export { grant } from "./grant"')
  writeFileSync(resolve(root,'src/lib/grant.ts'),'export const grant=true')
  writeFileSync(resolve(root,'tests/grant.spec.ts'),'import "../src/Screen.vue"')
  const graph=webGraph(root)
  assert.deepEqual(graph['src/Screen.vue'],['src/lib/index.ts'])
  const row={kind:'browser',file:'tests/grant.spec.ts',name:'grant',tier:'NIGHTLY'}
  assert.equal(select([row],{event:'pull_request',paths:['web/src/lib/grant.ts'],webImports:graph}).tests.length,1)
})

test('two tier shards contain every selected identity exactly once, including equal printed titles',()=>{
  const duplicate=[w('tests/param.test.ts','undefined'),w('tests/param.test.ts','undefined')].map((row,i)=>({...row,occurrence:i+1}))
  const rows=[...cases,...duplicate],all=[...shard(rows,1,2),...shard(rows,2,2)]
  assert.deepEqual(all.map(key).sort(),rows.map(key).sort())
  assert.equal(new Set(all.map(key)).size,rows.length)
  const pattern=new RegExp(exactPattern(['test [1]','ends.$']))
  assert.ok(pattern.test('test [1]'));assert.ok(!pattern.test('test 1'));assert.ok(!pattern.test('other ends.$'))
})

for(const [kind,maxShards,options] of [['go',7,{}],['browser',12,{}],['node',4,{}],['node',4,{firstShardLast:true}]]) {
  test(`${kind}${options.firstShardLast?' (first shard last)':''} zero-weight owners fill shards before reusing tied bins for counts 1..${maxShards}`,()=>{
    for(let count=1;count<=maxShards;count++) {
      for(const ownerCount of new Set([0,1,Math.max(0,count-1),count,count+1,2*count+1])) {
        for(const distribution of ['mixed','all-zero','equal-positive']) {
          const owners=Array.from({length:ownerCount},(_,i)=>kind==='go'
            ? `internal/owner-${String(i).padStart(2,'0')}`
            : `tests/owner-${String(i).padStart(2,'0')}.${kind==='browser'?'spec':'test'}.ts`)
          // Different registration counts ensure the tie-break counts owners,
          // rather than splitting owners or comparing their number of tests.
          const rows=owners.flatMap((owner,i)=>Array.from({length:1+i%3},(_,j)=>kind==='go'
            ? g(owner,`TestCase${j}`) : {...w(owner,`case ${j}`),kind}))
          const weights=Object.fromEntries(owners.map((owner,i)=>[owner,
            distribution==='all-zero'?0:distribution==='mixed'?(i===0?1:0):1]))
          const before={rows:structuredClone(rows),weights:{...weights}}
          const bins=Array.from({length:count},(_,i)=>shard(rows,i+1,count,weights,options))
          const ownerOf=row=>kind==='go'?row.package:row.file
          const ownerBins=bins.map(bin=>[...new Set(bin.map(ownerOf))])
          const context=`${kind}${options.firstShardLast?' (first shard last)':''}: ${count} shards, ${ownerCount} owners, ${distribution}`
          assert.deepEqual(bins.flat().map(key).sort(),rows.map(key).sort(),context)
          assert.equal(new Set(bins.flat().map(key)).size,rows.length,context)
          assert.deepEqual(ownerBins.flat().sort(),owners,`${context}: owners stay whole`)
          assert.equal(ownerBins.filter(bin=>bin.length).length,Math.min(count,ownerCount),`${context}: empty shards only when owners are fewer`)
          for(let i=0;i<count;i++) {
            assert.deepEqual(shard([...rows].reverse(),i+1,count,weights,options).map(key).sort(),bins[i].map(key).sort(),`${context}: input order cannot change assignment`)
          }
          if(distribution==='all-zero'&&!options.firstShardLast) {
            // Equal weights and owner counts resolve by path then shard index.
            assert.deepEqual(ownerBins,Array.from({length:count},(_,i)=>owners.filter((_,j)=>j%count===i)),context)
          }
          assert.deepEqual({rows,weights},before,`${context}: inputs remain unchanged`)
        }
      }
    }
  })
}

test('fewer owners than shards retain successful empty-shard reports in Go, unit and browser callers',async()=>{
  const {run}=await import('./cli.mjs')
  const env={RUNNER_ENVIRONMENT:'github-hosted',GITHUB_RUN_ID:'empty-shard',GITHUB_RUN_ATTEMPT:'1',GITHUB_SHA:'a'.repeat(40)}
  for(const [kind,unit,count,row] of [['go',false,7,cases[0]],['web',true,4,cases[5]],
    ['web',false,12,{kind:'browser',file:'tests/only.spec.ts',name:'only',tier:'ESSENTIAL'}]]) {
    const owner=row.kind==='go'?row.package:row.file
    const tests=shard([row],count,count,{[owner]:0})
    assert.deepEqual(tests,[])
    const job=`ops257r3-empty-${kind}-${unit?'unit':'cases'}`
    assert.equal(await run(kind,{tests,all:[row],full:true,scope:'gated-full',reason:'fewer owners than shards'},
      {unit,job,env}),0)
    const report=JSON.parse(readFileSync(new URL(`../../tmp/test-tiers/${job}-measurement.json`,import.meta.url)))
    assert.equal(checkFull(report,env),true)
    assert.ok(Object.values(report.classes).every(tier=>tier.selected===0&&tier.run===0&&tier.passed===0&&tier.notRun===0&&tier.failed===0))
  }
})

test('reports distinguish missing, skipped and failed outcomes; nested Go subtests cannot inflate totals',()=>{
  const text=[{Package:'github.com/inspr-at/paimos/internal/auth',Test:'TestCeiling',Action:'run'},
    {Package:'github.com/inspr-at/paimos/internal/auth',Test:'TestCeiling/sub',Action:'pass'},
    {Package:'github.com/inspr-at/paimos/internal/auth',Test:'TestCeiling',Action:'pass'}].map(JSON.stringify).join('\n')
  const report=reportCases(cases.slice(0,3),goOutcomes(text),60,'go-test-1')
  assert.equal(report.classes.ESSENTIAL.passed,1)
  assert.equal(report.classes.ESSENTIAL.notRun,1)
  assert.equal(report.classes.NIGHTLY.notRun,1)
  assert.equal(report.executionMinutes,1);assert.equal(report.runnerMinutes,null)
  const inactive=reportCases([{...cases[0],active:false}],[],0,'darwin-only')
  assert.equal(inactive.classes.ESSENTIAL.platformInactive,1)
  assert.equal(inactive.classes.ESSENTIAL.passed,0)
  const browser={kind:'browser',file:'tests/a.spec.ts',name:'action',tier:'ESSENTIAL',id:'id',project:''}
  assert.throws(()=>browserOutcomes({suites:[{specs:[{id:'id',tests:[{results:[{status:'failed'},{status:'passed'}]}]}]}]},[browser]),/Automatic retries forbidden/)
})

test('job accounting includes setup in minutes and reports missing artifacts rather than zero-case success',()=>{
  const job={name:'go-test (1)',status:'completed',conclusion:'failure',started_at:'2026-10-04T01:00:00Z',completed_at:'2026-10-04T01:02:00Z'}
  assert.equal(jobMinutes(job),2)
  assert.equal(jobMinutes({...job,status:'in_progress'}),null)
  const measurement=aggregate([], [job])
  assert.equal(measurement.measured.goRunnerMinutes,2)
  assert.equal(measurement.coverage,'incomplete')
  assert.deepEqual(measurement.missingEvidence,['go-test-1'])
})

test('skipped jobs have no runner duration and cannot hide missing test evidence',()=>{
  const skipped={name:'tree-reuse',status:'completed',conclusion:'skipped',
    started_at:'2026-10-05T12:38:25Z',completed_at:'2026-10-05T12:38:24Z'}
  for(const timestamps of [{},{started_at:skipped.completed_at},{started_at:null,completed_at:null}]) {
    assert.equal(jobMinutes({...skipped,...timestamps}),null)
  }
  const measurement=aggregate([], [skipped,{...skipped,name:'web-unit (1)'}])
  assert.ok(measurement.jobs.every(job=>job.runnerMinutes===null))
  assert.equal(measurement.measured.webRunnerMinutes,0)
  assert.equal(measurement.coverage,'incomplete')
  assert.deepEqual(measurement.missingEvidence,['web-unit-1'])
  for(const conclusion of ['success','failure','cancelled']) {
    assert.throws(()=>jobMinutes({...skipped,conclusion}),/Invalid job timestamps: tree-reuse/)
  }
})

test('Actions skipped jobs with zero completed_at never invalidate PR, queue, push or nightly accounting',()=>{
  // Exact shape returned for tree-reuse on the failing PR/merge-group runs.
  const skipped={name:'tree-reuse',status:'completed',conclusion:'skipped',
    started_at:'2026-10-05T10:00:00Z',completed_at:'0001-01-01T00:00:00Z',steps:[]}
  const lanes={
    pull_request:{names:['tree-reuse','cache-prime','go-test (1)','go-timing','web-setup','web-shard (1)'],options:{lane:'docs-only'},missing:[]},
    merge_group:{names:['tree-reuse','cache-prime'],options:{lane:'full'},missing:[]},
    push:{names:['go-test (1)','go-timing','web-setup','web-shard (1)','release-check-run','e2e-run'],options:{lane:'full',reusedFrom:'123'},missing:[]},
    workflow_dispatch:{names:['tree-reuse','cache-prime'],options:{lane:'full'},missing:[]},
    schedule:{names:['nightly-go-test (1)','nightly-go-timing','nightly-web-setup','nightly-web-shard (1)'],options:{},missing:['go-test-1','go-timing','web-unit','web-shard-1']},
  }
  for(const [event,{names,options,missing}] of Object.entries(lanes)) for(const timestamps of [
    {},{started_at:'0001-01-01T00:00:00Z'},
    {started_at:null,completed_at:null},{started_at:0,completed_at:0},
  ]) {
    const jobs=names.map(name=>({...skipped,name,...timestamps}))
    const report=aggregate([],jobs,options)
    assert.ok(report.jobs.every(job=>job.conclusion==='skipped'&&job.runnerMinutes===null),event)
    assert.deepEqual(report.missingEvidence,missing,event)
    assert.equal(report.coverage,missing.length?'incomplete':options.reusedFrom?'reused':'reported',event)
    assert.equal(report.measured.goRunnerMinutes,0,event)
    assert.equal(report.measured.webRunnerMinutes,0,event)
    assert.deepEqual(report.classes,{},'Skipped jobs must not fabricate case passes')
  }
  assert.equal(jobMinutes({...skipped,status:'queued',conclusion:null}),null)
  assert.equal(jobMinutes({...skipped,conclusion:'cancelled',runner_id:0}),null)
  assert.throws(()=>jobMinutes({...skipped,conclusion:'cancelled',runner_id:1}),/Invalid job timestamps/)
  assert.throws(()=>jobMinutes({...skipped,conclusion:'cancelled',runner_id:0,steps:[{name:'Checkout',conclusion:'success'}]}),/Invalid job timestamps/)
})

// Classification: fixed go-static/nightly-go-static CI checks, outside the
// dynamically collected Go/web tier and browser-shard inventories.
test('AEON-732 full and nightly skipped tier fixtures remain incomplete after upstream failures',()=>{
  const finished={status:'completed',started_at:'2026-10-05T10:00:00Z',completed_at:'2026-10-05T10:02:00Z'}
  const skipped={...finished,conclusion:'skipped',completed_at:'0001-01-01T00:00:00Z',steps:[]}
  for(const lane of [undefined,'full']) for(const prerequisite of ['tier-plan','web-setup']) {
    const jobs=[{...finished,name:prerequisite,conclusion:'failure'},
      ...['go-test (1)','go-timing','web-unit (1)','web-shard (1)'].map(name=>({...skipped,name}))]
    const report=aggregate([],jobs,{lane})
    assert.equal(report.coverage,'incomplete')
    assert.equal(report.coverageReason,'skipped after upstream failure')
    assert.deepEqual(report.missingEvidence,['go-test-1','go-timing','web-unit-1','web-shard-1'])
    assert.deepEqual(report.upstreamFailures,[{name:prerequisite,status:'completed',conclusion:'failure'}])
    assert.deepEqual(report.skippedJobs,jobs.slice(1).map(job=>job.name))
    assert.ok(report.jobs.slice(1).every(job=>job.runnerMinutes===null))
    assert.equal(report.measured.goRunnerMinutes,0)
    assert.equal(report.measured.webRunnerMinutes,prerequisite==='web-setup'?2:0)
    assert.deepEqual(report.classes,{})
    // Required skips remain incomplete even without prerequisite metadata.
    const noPrerequisite=aggregate([],jobs.slice(1),{lane})
    assert.equal(noPrerequisite.coverage,'incomplete')
    assert.equal(noPrerequisite.coverageReason,'required tier jobs skipped')
  }
  const nightly=aggregate([], [{...finished,name:'nightly-web-setup',conclusion:'failure'},
    {...skipped,name:'nightly-web-shard (1)'}])
  assert.equal(nightly.coverage,'incomplete')
  assert.equal(nightly.coverageReason,'skipped after upstream failure')
  assert.deepEqual(nightly.missingEvidence,['web-unit','web-shard-1'])
  assert.deepEqual(nightly.upstreamFailures,[{name:'nightly-web-setup',status:'completed',conclusion:'failure'}])
  for(const name of ['tier-plan','web-setup','nightly-web-setup']) for(const conclusion of ['failure','cancelled','skipped']) {
    const job={...finished,name,conclusion,...(conclusion==='skipped'?{completed_at:'0001-01-01T00:00:00Z'}:{})}
    // Even a retained unit artifact cannot conceal a later setup/build failure.
    const report=aggregate([reportCases([],[],0,'web-unit')],[job])
    const skippedUnit=name==='nightly-web-setup'&&conclusion==='skipped'
    assert.deepEqual(report.missingEvidence,skippedUnit?['web-unit']:[])
    assert.equal(report.coverage,'incomplete')
    assert.equal(report.coverageReason,skippedUnit?'skipped after upstream failure':'upstream jobs did not succeed')
    assert.deepEqual(report.upstreamFailures,[{name,status:'completed',conclusion}])
  }
  const jobs=['go-test (1)','go-timing','web-unit (1)','web-shard (1)'].map(name=>({...skipped,name}))
  const exact=aggregate([],jobs,{lane:'spec-only'})
  assert.equal(exact.coverage,'untiered')
  assert.deepEqual(exact.missingEvidence,[])
  assert.deepEqual(exact.classes,{})
  // Actions may skip a matrix before it expands, leaving the family name.
  const unexpanded=aggregate([],jobs.map(job=>({...job,name:job.name.replace(/ \(\d+\)$/,'')})))
  assert.equal(unexpanded.coverage,'incomplete')
  assert.deepEqual(unexpanded.missingEvidence,['go-test','go-timing','web-unit','web-shard'])
  const unexpected=aggregate([], [{...finished,name:'go-test (1)',conclusion:'success'}],{lane:'docs-only'})
  assert.equal(unexpected.coverage,'incomplete','Executed docs-only jobs still require evidence')
  const successful=aggregate([reportCases([],[],0,'go-test-1')],[{...finished,name:'go-test (1)',conclusion:'success'}],{lane:'full'})
  assert.equal(successful.coverage,'reported')
  assert.deepEqual(successful.missingEvidence,[])
  const artifactForSkipped=aggregate([reportCases([],[],0,'go-test-1')],[jobs[0]],{lane:'full'})
  assert.equal(artifactForSkipped.coverage,'incomplete')
  assert.deepEqual(artifactForSkipped.missingEvidence,['go-test-1'])
})

test('AEON-732 measurement CLI fixtures expose upstream skips in JSON, summary and exit status',()=>{
  const root=mkdtempSync(resolve(tmpdir(),'aeon-tier-measurement-'))
  const finished={status:'completed',started_at:'2026-10-05T10:00:00Z',completed_at:'2026-10-05T10:02:00Z'}
  const skipped={...finished,conclusion:'skipped',completed_at:'0001-01-01T00:00:00Z',steps:[]}
  const jobs=[{...finished,name:'tier-plan',conclusion:'failure'},
    ...['go-test (1)','go-timing','web-setup','web-unit (1)','web-shard (1)'].map(name=>({...skipped,name}))]
  const evidence=resolve(root,'evidence'),summary=resolve(root,'summary.txt')
  mkdirSync(evidence)
  for(const fixture of [
    {lane:'full',status:1,coverage:'incomplete',reason:'skipped after upstream failure'},
    {lane:'docs-only',planned:true,status:0,coverage:'reported'},
    {lane:'full',planned:true,reuse:'merge_group',status:0,coverage:'reused'},
    {lane:'spec-only',planned:true,status:0,coverage:'untiered'},
    {lane:'spec-only',status:1,coverage:'incomplete',reason:'upstream jobs did not succeed'},
  ]) {
    const fixtureJobs=jobs.map(job=>fixture.planned&&(job.name==='tier-plan'||fixture.lane==='spec-only'&&job.name.startsWith('web-'))
      ? {...job,...finished,conclusion:'success'}:job)
    writeFileSync(resolve(root,'gh'),`#!${process.execPath}\nif (process.argv[2] !== 'api' || process.argv[3] !== 'repos/example/aeon/actions/runs/123/attempts/2/jobs?per_page=100&page=1') process.exit(2)\nprocess.stdout.write(${JSON.stringify(JSON.stringify({total_count:fixtureJobs.length,jobs:fixtureJobs}))})\n`,{mode:0o755})
    writeFileSync(summary,'')
    const result=spawnSync(process.execPath,[fileURLToPath(new URL('./measure.mjs',import.meta.url)),evidence],{
      cwd:root,encoding:'utf8',timeout:10_000,
      env:{PATH:root,GITHUB_REPOSITORY:'example/aeon',GITHUB_RUN_ID:'123',GITHUB_RUN_ATTEMPT:'2',GITHUB_SHA:'a'.repeat(40),
        GITHUB_STEP_SUMMARY:summary,CI_LANE:fixture.lane,REUSE:fixture.reuse??'',SOURCE_RUN:fixture.reuse?'456':''},
    })
    assert.ifError(result.error)
    assert.equal(result.status,fixture.status,result.stderr)
    assert.equal(result.stderr,'')
    const report=JSON.parse(readFileSync(resolve(root,'tmp/test-tier-run-measurement.json'),'utf8'))
    const output=JSON.parse(result.stdout)
    for(const evidence of [report,output]) {
      assert.equal(evidence.coverage,fixture.coverage)
      assert.equal(evidence.coverageReason,fixture.reason)
      assert.deepEqual(evidence.classes,{})
    }
    assert.ok(readFileSync(summary,'utf8').includes(`Test tiers: ${fixture.coverage}${fixture.reason?` — ${fixture.reason}`:''};`))
    if(fixture.lane==='full'&&!fixture.reuse) {
      assert.deepEqual(report.missingEvidence,['go-test-1','go-timing','web-unit-1','web-shard-1'])
      assert.ok(readFileSync(summary,'utf8').includes('tier-plan (failure), web-setup (skipped)'))
      assert.equal(report.measured.sharedPlanningRunnerMinutes,2)
      assert.equal(report.measured.goRunnerMinutes,0)
      assert.equal(report.measured.webRunnerMinutes,0)
    } else assert.deepEqual(report.missingEvidence,[])
  }
})

test('completed jobs that ran still reject absent, zero, invalid and reversed timestamps',()=>{
  const job={name:'go-test (1)',status:'completed',started_at:'2026-10-05T10:00:00Z',completed_at:'2026-10-05T10:02:00Z'}
  for(const conclusion of ['success','failure','cancelled']) for(const timestamps of [
    {started_at:null},{completed_at:null},{started_at:undefined},{completed_at:undefined},
    {started_at:''},{completed_at:''},{started_at:0},{completed_at:0},
    {started_at:'0001-01-01T00:00:00Z'},{completed_at:'0001-01-01T00:00:00Z'},
    {started_at:'invalid'},{completed_at:'2026-10-05T09:59:59Z'},
  ]) assert.throws(()=>aggregate([], [{...job,conclusion,...timestamps}]),/Invalid job timestamps: go-test \(1\)/)
})

test('four unit shards partition the existing ledger by file with every identity selected once',()=>{
  const manifest=JSON.parse(readFileSync(new URL('../ci/web-test-tiers.json',import.meta.url)))
  for(const event of ['pull_request','merge_group','push','workflow_dispatch','schedule']) {
    const selected=select(manifest.tests,{event,paths:['.github/workflows/ci.yml']}).tests.filter(row=>row.kind!=='browser')
    const bins=Array.from({length:4},(_,i)=>shard(selected,i+1,4))
    assert.deepEqual(bins.flat().map(key).sort(),selected.map(key).sort(),event)
    const owners=new Map()
    bins.forEach((rows,i)=>rows.forEach(row=>{
      if(owners.has(row.file))assert.equal(owners.get(row.file),i,'A file must not run twice')
      owners.set(row.file,i)
    }))
    const sizes=bins.map(rows=>rows.length).sort((a,b)=>a-b)
    // Nightly retains a large parameterized file; it is intentionally kept
    // whole and does not participate in the four full-gate unit jobs.
    if(event!=='schedule')assert.ok(sizes[3]<=1.3*(sizes[1]+sizes[2])/2,`Unit registration balance: ${sizes}`)
  }
})

test('serial tier weights rebalance the twelve browser shards without changing full or changed-area coverage',()=>{
  const manifest=JSON.parse(readFileSync(new URL('../../web/ci-web-shards.json',import.meta.url)))
  const rows=JSON.parse(readFileSync(new URL('../ci/web-test-tiers.json',import.meta.url))).tests
  const graph=Object.fromEntries([...new Set(rows.map(row=>row.file))].map(file=>[file,[]]))
  for(const options of [
    {event:'pull_request',paths:['.github/workflows/ci.yml']},
    {event:'merge_group',paths:['web/tests/clip-tip.spec.ts'],webImports:graph},
    {event:'push'}, {event:'workflow_dispatch'}, {event:'schedule'},
  ]) {
    const selected=select(rows,options).tests.filter(row=>row.kind==='browser')
    const weights=tierWeights(manifest,selected)
    const bins=Array.from({length:12},(_,i)=>shard(selected,i+1,12,weights))
    assert.deepEqual(bins.flat().map(key).sort(),selected.map(key).sort(),options.event)
    if(options.paths?.[0]==='.github/workflows/ci.yml'||options.event==='push') {
      const loads=bins.map(bin=>[...new Set(bin.map(row=>row.file))].reduce((n,file)=>n+weights[file],0)).sort((a,b)=>a-b)
      assert.ok(loads[11]<=1.3*(loads[5]+loads[6])/2,`Serial scheduling estimates: ${loads}`)
      assert.ok(loads[11]<=1.25*(loads[5]+loads[6])/2,`Hosted scheduling estimates: ${loads}`)
    }
  }
})

test('hosted unit and Go weights preserve files/packages and every selected identity',()=>{
  for(const kind of ['web','go']) {
    const manifest=JSON.parse(readFileSync(new URL(`../ci/${kind}-test-tiers.json`,import.meta.url)))
    for(const event of ['pull_request','merge_group','push','schedule']) {
      const rows=select(manifest.tests,{event,paths:['.github/workflows/ci.yml']}).tests.filter(row=>kind==='web'?row.kind!=='browser':row.lane!=='timing')
      const weights=measuredWeights(manifest,rows),count=kind==='web'?4:7
      const bins=Array.from({length:count},(_,i)=>shard(rows,i+1,count,weights,{firstShardLast:kind==='web'}))
      assert.deepEqual(bins.flat().map(key).sort(),rows.map(key).sort())
      const owners=new Map()
      bins.forEach((bin,i)=>bin.forEach(row=>{
        const owner=kind==='go'?row.package:row.file
        if(owners.has(owner))assert.equal(owners.get(owner),i,'An owner cannot run twice')
        owners.set(owner,i)
      }))
    }
  }
})

test('unit tie preference keeps the heaviest file away from shard 1 extra checks without losing cases',()=>{
  const rows=[...Array(4)].flatMap((_,i)=>[1,2].map(n=>({kind:'node',file:`tests/unit-${i}.test.ts`,name:`case ${n}`})))
  const weights=Object.fromEntries(rows.map(row=>[row.file, row.file.includes('unit-0')?44:30]))
  const bins=Array.from({length:4},(_,i)=>shard(rows,i+1,4,weights,{firstShardLast:true}))
  assert.deepEqual(bins.flat().map(key).sort(),rows.map(key).sort())
  assert.deepEqual(bins[1],rows.slice(0,2))
  assert.ok(bins[0].every(row=>row.file!=='tests/unit-0.test.ts'))
  assert.deepEqual(shard(rows,1,1,weights,{firstShardLast:true}).map(key).sort(),rows.map(key).sort())
  assert.deepEqual(shard(rows,1,4,weights),rows.slice(0,2),'Go/browser default tie policy stays unchanged')
})

test('measured weights scale the selected slice, retain zero elapsed and reject invalid data',()=>{
  const manifest={timingWeights:{owners:{'tests/scope.test.ts':{seconds:10,selectedTests:2}}}}
  assert.deepEqual(measuredWeights(manifest,[cases[5]]),{'tests/scope.test.ts':5})
  assert.deepEqual(measuredWeights(manifest,cases),{'tests/scope.test.ts':10})
  manifest.timingWeights.owners['tests/scope.test.ts'].seconds=0
  assert.deepEqual(measuredWeights(manifest,cases),{'tests/scope.test.ts':0})
  for(const timing of [{seconds:-1,selectedTests:2},{seconds:NaN,selectedTests:2},{seconds:1,selectedTests:0},{seconds:1,selectedTests:1.5}]) {
    manifest.timingWeights.owners['tests/scope.test.ts']=timing
    assert.throws(()=>measuredWeights(manifest,cases),/Invalid measured timing: tests\/scope.test.ts/)
  }
})

test('unit measurement artifacts are required for every executed shard and legacy nightly units',()=>{
  const directory=mkdtempSync(resolve(tmpdir(),'aeon-unit-measurements-'))
  const job=name=>({name,status:'completed',conclusion:'success',started_at:'2026-10-05T10:00:00Z',completed_at:'2026-10-05T10:02:00Z'})
  const names=['web-unit-1','web-unit-2','web-unit-3','web-unit-4']
  for(const name of names)writeFileSync(resolve(directory,`${name}-measurement.json`),JSON.stringify(reportCases([],[],0,name)))
  const jobs=names.map((_,i)=>job(`web-unit (${i+1})`))
  const reports=readReports(directory)
  assert.equal(reports.length,4)
  assert.equal(aggregate(reports,jobs).coverage,'reported')
  for(const name of names)assert.deepEqual(aggregate(reports.filter(r=>r.job!==name),jobs).missingEvidence,[name])
  assert.deepEqual(aggregate([], [job('nightly-web-setup')]).missingEvidence,['web-unit'])
  assert.deepEqual(aggregate([], [job('web-setup')]).missingEvidence,[],'Main setup no longer executes units')
  const exact=aggregate([], [job('web-unit (1)'),job('web-shard (1)')],{lane:'spec-only'})
  assert.equal(exact.coverage,'untiered')
  assert.deepEqual(exact.classes,{})
  assert.deepEqual(exact.missingEvidence,[])
  assert.match(exact.caseScope,/no tier passes are claimed/)
  assert.deepEqual(aggregate([], [job('web-unit (1)'),job('web-shard (1)')],{lane:'full'}).missingEvidence,['web-unit-1','web-shard-1'])
})

test('rerun measurements reject earlier-attempt artifacts instead of replaying case passes',()=>{
  const job={name:'go-test (1)',status:'completed',started_at:'2026-10-04T01:00:00Z',completed_at:'2026-10-04T01:01:00Z'}
  const earlier={...reportCases([cases[0]],[{key:key(cases[0]),status:'passed',started:true}],1,'go-test-1'),runId:'123',attempt:'1',sha:'a'}
  const report=aggregate([earlier],[job],{runId:'123',attempt:'2',sha:'a'})
  assert.equal(report.coverage,'incomplete');assert.deepEqual(report.classes,{})
  assert.deepEqual(report.missingEvidence,['go-test-1']);assert.equal(report.excludedEvidence.length,1)
  const fresh=aggregate([{...earlier,attempt:'2'}],[job],{runId:'123',attempt:'2',sha:'a'})
  assert.equal(fresh.coverage,'reported');assert.equal(fresh.classes.ESSENTIAL.passed,1)
})

test('exact-SHA reuse records provenance and current costs without fabricating fresh case passes',()=>{
  const jobs=[{name:'go-test (1)',status:'completed',started_at:'2026-10-04T01:00:00Z',completed_at:'2026-10-04T01:00:30Z'}]
  const reused=aggregate([],jobs,{reusedFrom:'123'})
  assert.equal(reused.coverage,'reused');assert.equal(reused.reusedFrom,'123')
  assert.deepEqual(reused.classes,{});assert.deepEqual(reused.missingEvidence,[])
  assert.equal(reused.measured.goRunnerMinutes,0.5)
  assert.throws(()=>aggregate([],jobs,{reusedFrom:'invalid'}),/Invalid reused source/)
  assert.throws(()=>aggregate([{}],jobs,{reusedFrom:'123'}),/must not report fresh test passes/)
})

test('committed allowlists preserve classifications, helpers and reviewed AEON-541 demotions without fixed inventory sizes',()=>{
  const go=JSON.parse(readFileSync(new URL('../ci/go-test-tiers.json',import.meta.url)))
  const web=JSON.parse(readFileSync(new URL('../ci/web-test-tiers.json',import.meta.url)))
  for(const manifest of [go,web]) {
    const reconciled=validate(manifest,manifest.tests)
    assert.deepEqual(reconciled.map(key).sort(),manifest.tests.map(key).sort())
    for(const tier of ['ESSENTIAL','GATED-FULL']) assert.ok(reconciled.some(row=>row.tier===tier),tier)
  }
  for(const helper of ['TestFakeVendorProcess','TestNoOutboundServerHelper'])assert.equal(go.tests.find(row=>row.name===helper).tier,'ESSENTIAL')
  const known=JSON.parse(readFileSync(new URL('../ci/known-flaky.json',import.meta.url)))
  const guards=web.tests.filter(row=>row.file==='tests/no-shift.spec.ts'&&known.entries.some(entry=>entry.key===key(row)))
  assert.deepEqual(guards.map(key).sort(),known.entries.filter(entry=>entry.key.startsWith('browser:tests/no-shift.spec.ts:')).map(entry=>entry.key).sort())
  assert.ok(guards.every(row=>row.tier==='GATED-FULL'))
  assert.ok(go.tests.some(row=>row.tags?.includes('delete-candidate')))
  assert.ok(go.tests.some(row=>row.lane==='timing'))
  assert.ok([...go.tests,...web.tests].filter(row=>row.tags?.includes('delete-candidate')).every(row=>row.tier==='GATED-FULL'||row.tier==='NIGHTLY'))
})

// These exact registrations were missing in the AEON-706 static report.
// Keep AEON-707's reviewed work-order promotion and main's new nightly guards.
for(const [label,pkg,tier,names] of [
  ['work-order body guards','internal/workorders','GATED-FULL',[
    'TestBufferBodyBoundsAndDecode','TestEndpointBodyNetworkDeadline','TestEndpointPositionedBodyNetworkDeadline',
  ]],
  ['builder and shared-runtime guards','scripts/releaseworkflow','NIGHTLY',[
    'TestBuilderChecksumAndPluginPathFailClosed','TestBuilderVersionAssertionFailsClosed',
    'TestRehearsedBuilderPins','TestParallelWebUnitsShareRuntimeAndRunOnce',
  ]],
]) test(`AEON-706 ${label} retain explicit classifications and intended gate execution`,()=>{
  const manifest=JSON.parse(readFileSync(new URL('../ci/go-test-tiers.json',import.meta.url)))
  const discovered=names.map(name=>g(pkg,name))
  const ids=discovered.map(key).sort(), required=new Set(ids)
  const stored=manifest.tests.filter(row=>required.has(key(row)))
  assert.deepEqual(stored.map(key).sort(),ids,'Every reported registration needs exactly one explicit classification')
  const rows=validate({...manifest,tests:stored},discovered,noFlaky,{strict:true})
  for(const row of rows) {
    assert.equal(row.tier,tier,key(row))
    assert.equal(manifest.postGateCases.includes(key(row)),tier==='NIGHTLY',`${key(row)} must retain its reviewed post-gate policy`)
  }
  for(const event of ['pull_request','merge_group','push','schedule']) {
    const selected=select(rows,{event,forceFull:true}).tests.map(key).sort()
    assert.deepEqual(selected,event==='schedule'||tier==='GATED-FULL'?ids:[],`${event}: preserve explicit gate scope`)
  }
  assert.deepEqual(select(rows,{event:'pull_request',forceAll:true}).tests.map(key).sort(),ids)
})

test('strict classification maintenance is scheduled separately and never a required PR or nightly test dependency',()=>{
  const nightly=readFileSync(new URL('../../.github/workflows/nightly-full.yml',import.meta.url),'utf8')
  const ci=readFileSync(new URL('../../.github/workflows/ci.yml',import.meta.url),'utf8')
  const job=nightly.slice(nightly.indexOf('\n  nightly-tier-classification:'),nightly.indexOf('\n  nightly-web-shard:'))
  assert.match(job,/if: github.event_name == 'schedule'/)
  assert.match(job,/cli\.mjs check go --strict/)
  assert.match(job,/cli\.mjs check web --strict/)
  assert.match(job,/Report web cases needing classification\n\s+if: always\(\)/)
  assert.doesNotMatch(ci,/--strict/)
  assert.doesNotMatch(nightly,/needs:.*nightly-tier-classification/)
})

test('nightly runs every tier and fixed gate; PR/MQ aggregates and compatibility remain',()=>{
  const read=name=>readFileSync(new URL(`../../.github/workflows/${name}`,import.meta.url),'utf8')
  const nightly=read('nightly-full.yml'),ci=read('ci.yml')
  assert.match(nightly,/schedule:\n\s+- cron:/);assert.match(nightly,/workflow_dispatch:/)
  assert.doesNotMatch(nightly,/pull_request:|merge_group:|runner-route:/)
  for(const command of ['run go --all --shard','run web --all --unit','run web --all --shard'])assert.ok(nightly.includes(command))
  for(const id of ['go-static','go-timing','migration-compat','e2e','release-check'])assert.ok(nightly.includes(`  nightly-${id}:`))
  assert.match(ci,/  go:\n\s+if: always\(\)\n\s+needs: \[ci-plan, go-test, go-static, go-timing, tree-reuse, cache-prime, tier-plan\]/)
  assert.match(ci,/  web:\n\s+name: web\n\s+if: always\(\)\n\s+needs: \[ci-plan, web-setup, web-unit, web-shard, tree-reuse, cache-prime, tier-plan\]/)
  assert.match(ci,/  migration-compat:\n\s+permissions:/)
  assert.doesNotMatch(ci,/ci-flake-guard\.mjs --kind/)
})

test('full-execution proof binds actual complete outcomes to this run, attempt and SHA',()=>{
  const report={...reportCases(cases,cases.map(row=>({key:key(row),status:'passed',started:true})),1,'go-test-1'),full:true,scope:'catalogue',exitCode:0,runId:'123',attempt:'2',sha:'a'}
  const env={GITHUB_RUN_ID:'123',GITHUB_RUN_ATTEMPT:'2',GITHUB_SHA:'a'}
  assert.equal(checkFull(report,env),true)
  for(const change of [{full:false},{exitCode:1},{attempt:'1'},{sha:'b'},{classes:{...report.classes,NIGHTLY:{...report.classes.NIGHTLY,notRun:1}}}])assert.throws(()=>checkFull({...report,...change},env),/Full execution/)
})

test('browser registrations blocked before execution remain notRun rather than counted as failed runs',()=>{
  const row={kind:'browser',file:'tests/a.spec.ts',name:'action',tier:'ESSENTIAL',id:'id',project:''}
  const outcomes=browserOutcomes({suites:[{specs:[{id:'id',tests:[{results:[]}]}]}]},[row])
  const report=reportCases([row],outcomes,1,'web-shard-1')
  assert.equal(report.classes.ESSENTIAL.notRun,1)
  assert.equal(report.classes.ESSENTIAL.run,0)
  assert.equal(report.classes.ESSENTIAL.failed,0)
})

test('three-tier full is exactly ESSENTIAL plus GATED-FULL even for unknown impact and explicit full',()=>{
  const rows=[g('internal/auth','TestCore','ESSENTIAL'),g('internal/auth','TestExisting','GATED-FULL'),
    g('internal/auth','TestUnclassified'),
    {kind:'browser',file:'tests/gated.spec.ts',name:'gate',tier:'GATED-FULL'},
    {kind:'browser',file:'tests/optional.spec.ts',name:'gallery',tier:'NIGHTLY'},
    {kind:'browser',file:'tests/gated.spec.ts',name:'new case defaults to nightly',tier:'NIGHTLY'},
    {kind:'vitest',file:'tests/new.unit.test.ts',name:'new unit defaults to nightly',tier:'NIGHTLY'}]
  for(const event of ['pull_request','merge_group','push','workflow_dispatch']) {
    for(const paths of [undefined,['.github/workflows/ci.yml'],['web/src/deleted.ts'],['scripts/check.mjs','web/tests/optional.spec.ts']]) {
      for(const forceFull of [false,true]) {
        const selection=select(rows,{event,paths,forceFull})
        assert.deepEqual(selection.tests,[rows[0],rows[1],rows[3]],`${event} ${paths} explicit=${forceFull}`)
        assert.equal(selection.scope,'gated-full')
        assert.equal(selection.deferredBrowserCases,2)
      }
    }
  }
  for(const options of [{event:'schedule'},{event:'pull_request',forceAll:true},{event:'workflow_dispatch',forceAll:true}]) {
    const selection=select(rows,{...options,paths:['README.md']})
    assert.deepEqual(selection.tests,rows)
    assert.equal(selection.full,true)
    assert.equal(selection.scope,'catalogue')
    assert.equal(selection.deferredBrowserCases,0)
  }
  // The changed-area lane still exercises optional tests when their area changes.
  for(const event of ['pull_request']) assert.deepEqual(
    select(rows,{event,paths:['web/tests/optional.spec.ts'],webImports:{'tests/optional.spec.ts':[]}}).tests,[rows[0],rows[4]])
  for(const event of ['pull_request']) assert.deepEqual(
    select(rows,{event,paths:['web/tests/gated.spec.ts'],webImports:{'tests/gated.spec.ts':[]}}).tests,[rows[0],rows[3],rows[5]])
})

test('three-tier validation and reports retain GATED-FULL results and deletion candidates',()=>{
  const rows=[g('internal/auth','TestCore','ESSENTIAL'),{...g('internal/auth','TestExisting','GATED-FULL'),tags:['delete-candidate']},g('internal/auth','TestNew')]
  const manifest={version:1,tests:rows}
  assert.doesNotThrow(()=>validate(manifest,rows,noFlaky))
  assert.deepEqual(validate(manifest,rows,noFlaky),rows)
  assert.deepEqual(counts(rows),{ESSENTIAL:1,'GATED-FULL':1,NIGHTLY:1})
  const report=reportCases(rows,[{key:key(rows[0]),status:'passed',started:true},{key:key(rows[1]),status:'failed',started:true}],1,'go-test-1')
  assert.equal(report.classes['GATED-FULL'].failed,1)
  assert.equal(report.classes['GATED-FULL'].run,1)
  assert.equal(report.classes.NIGHTLY.notRun,1)
  const known={version:1,entries:[{key:key(rows[1]),owner:'AEON-675'}]}
  assert.deepEqual(validate(manifest,rows,known),rows)
  const promoted=rows.map(row=>({...row,tier:'ESSENTIAL',tags:[]}))
  assert.throws(()=>validate({version:1,tests:promoted},rows,known),/Known-flaky case cannot be ESSENTIAL/)
})

test('AEON-686 host-capacity bound and fence cases run in every PR gate',()=>{
  // Unlisted cases default to NIGHTLY, so new Go tests in these files would
  // silently leave the PR gate; derive the list from source, not by hand.
  const go=JSON.parse(readFileSync(new URL('../ci/go-test-tiers.json',import.meta.url)))
  const sources=[['internal/hostcapacity','policy_test.go'],['internal/agentpairing','host_capacity_test.go']]
  const rows=sources.flatMap(([pkg,file])=>[...readFileSync(new URL(`../../${pkg}/${file}`,import.meta.url),'utf8')
    .matchAll(/^func (Test\w+)\(t \*testing\.T\)/gm)].map(match=>`${pkg}:${match[1]}`))
  for(const required of ['internal/hostcapacity:TestHostCapacityLargestEncodingsStayBounded',
    'internal/agentpairing:TestHostCapacityBoundsHeldByWritersWithoutDatabaseChecks'])assert.ok(rows.includes(required),required)
  for(const id of rows) {
    const row=go.tests.find(row=>key(row)===id)
    assert.equal(row?.tier,'ESSENTIAL',id)
    for(const event of ['pull_request','merge_group'])
      assert.ok(select(go.tests,{event,paths:['README.md']}).tests.some(selected=>key(selected)===id),`${event}: ${id}`)
  }
})

test('three-tier manifests allow new NIGHTLY cases and never promote ungated browser cases',()=>{
  const policy=JSON.parse(readFileSync(new URL('../../web/ci-web-shards.json',import.meta.url)))
  const gated=new Set(policy.groups.filter(group=>group.gate!==false).flatMap(group=>group.specs.map(spec=>spec.file)))
  const go=JSON.parse(readFileSync(new URL('../ci/go-test-tiers.json',import.meta.url)))
  const web=JSON.parse(readFileSync(new URL('../ci/web-test-tiers.json',import.meta.url)))
  // Cases first classified after the old gate retain the NIGHTLY default.
  // An explicit inventory keeps the assertion strict for every legacy case.
  const postGateCases=new Set([...(go.postGateCases??[]),...(web.postGateCases??[])])
  assert.equal(postGateCases.size,(go.postGateCases??[]).length+(web.postGateCases??[]).length)
  for(const id of postGateCases) {
    const row=[...go.tests,...web.tests].find(row=>key(row)===id)
    assert.ok(row,`Unknown post-gate case: ${id}`)
    assert.equal(row.tier,'NIGHTLY',id)
  }
  for(const row of [...go.tests,...web.tests]) {
    if(postGateCases.has(key(row)))continue
    if(row.tier==='ESSENTIAL')continue
    assert.equal(row.tier,row.kind!=='browser'||gated.has(row.file)?'GATED-FULL':'NIGHTLY',key(row))
  }
  // This established Knowledge registration caused the merge-group regression:
  // its launch group is gated, so it must retain its full-gate classification.
  assert.equal(web.tests.find(row=>row.kind==='browser'&&row.file==='tests/knowledge.spec.ts'&&
    row.name==='the Knowledge tab groups by kind, filters, searches the text and moves with keys')?.tier,'GATED-FULL')
  assert.ok(web.tests.some(row=>row.kind==='browser'&&row.tier==='NIGHTLY'))
  assert.ok(web.tests.some(row=>row.kind==='browser'&&row.tier==='GATED-FULL'))
})

test('three-tier nightly explicitly requests all cases and remains outside required CI jobs',()=>{
  const nightly=readFileSync(new URL('../../.github/workflows/nightly-full.yml',import.meta.url),'utf8')
  const ci=readFileSync(new URL('../../.github/workflows/ci.yml',import.meta.url),'utf8')
  for(const command of ['run go --all --shard','run go --all --timing','run web --all --unit','run web --all --shard'])assert.ok(nightly.includes(command),command)
  assert.doesNotMatch(nightly,/cli\.mjs run (?:go|web) --full/)
  assert.doesNotMatch(ci,/cli\.mjs run (?:go|web) --all|needs:.*nightly-/)
})

test('three-tier full proof rejects incomplete gated results and old two-tier evidence',()=>{
  const empty={selected:0,run:0,passed:0,skipped:0,failed:0,notRun:0,platformInactive:0}
  const report={version:1,full:true,scope:'gated-full',exitCode:0,runId:'123',attempt:'2',sha:'a',
    classes:{ESSENTIAL:{...empty},'GATED-FULL':{...empty,selected:1,run:1,passed:1},NIGHTLY:{...empty}}}
  const env={GITHUB_RUN_ID:'123',GITHUB_RUN_ATTEMPT:'2',GITHUB_SHA:'a'}
  assert.equal(checkFull(report,env),true)
  for(const scope of [undefined,'changed-area','browser-gate'])assert.throws(()=>checkFull({...report,scope},env),/Full execution/)
  const {['GATED-FULL']:gated,...oldClasses}=report.classes
  assert.throws(()=>checkFull({...report,classes:oldClasses},env),/Full execution/)
  for(const counts of [{...gated,notRun:1},{...gated,failed:1}])assert.throws(()=>checkFull({...report,classes:{...report.classes,'GATED-FULL':counts}},env),/Full execution/)
})

test('web collection is reused within a process only while the stamped tree is unchanged', () => {
  const base = mkdtempSync(resolve(tmpdir(), 'aeon-724-stamp-'))
  mkdirSync(resolve(base, 'src/deep'), { recursive: true }); mkdirSync(resolve(base, 'tests'))
  mkdirSync(resolve(base, 'node_modules/pkg'), { recursive: true }); mkdirSync(resolve(base, 'dist'))
  writeFileSync(resolve(base, 'package.json'), '{}'); writeFileSync(resolve(base, 'src/deep/a.ts'), 'a')
  writeFileSync(resolve(base, 'tests/a.test.ts'), 'a'); writeFileSync(resolve(base, 'node_modules/pkg/index.js'), '1')
  assert.deepEqual(webStampEntries, ['.', 'src', 'tests'])
  const initial = treeStamp(base, webStampEntries)
  assert.equal(treeStamp(base, webStampEntries), initial, 'stable without changes')
  // Installed dependencies and build output never invalidate the inventory.
  writeFileSync(resolve(base, 'node_modules/pkg/index.js'), '22'); writeFileSync(resolve(base, 'dist/bundle.js'), 'x')
  assert.equal(treeStamp(base, webStampEntries), initial)
  // A same-size edit changes mtime; nested source, native tests and top-level configs all count.
  const stamps = new Set([initial])
  for (const [file, text] of [['src/deep/a.ts', 'b'], ['tests/b.test.ts', 'new'], ['playwright.ui.config.ts', 'export {}']]) {
    const clock = new Date(Date.now() + stamps.size * 2000)
    writeFileSync(resolve(base, file), text); utimesSync(resolve(base, file), clock, clock)
    const stamp = treeStamp(base, webStampEntries)
    assert.ok(!stamps.has(stamp), file); stamps.add(stamp)
  }
  // Missing entries stamp as absent rather than throwing; the memo hands out private copies.
  assert.notEqual(treeStamp(base, ['missing']), treeStamp(base, ['.']))
  const memo = {}, runs = []
  const compute = () => { runs.push(1); return { tests: [{ kind: 'vitest', name: 'a' }] } }
  const first = reuse(memo, 'one', compute)
  first.tests[0].name = 'poisoned'
  assert.deepEqual(reuse(memo, 'one', compute), { tests: [{ kind: 'vitest', name: 'a' }] })
  assert.equal(runs.length, 1, 'unchanged stamp reuses')
  assert.deepEqual(reuse(memo, 'two', compute), { tests: [{ kind: 'vitest', name: 'a' }] })
  assert.equal(runs.length, 2, 'changed stamp recomputes')
  reuse(memo, 'two', compute, { fresh: true })
  assert.equal(runs.length, 3, 'fresh recomputes')
  assert.equal(memo.stamp, 'two')
})

// AEON-642: nightly `check web --strict` rejects a manifest name Vitest no
// longer emits. The state model generates its titles from these objects, and
// comments in that fixture contain apostrophes, so the scan has to skip
// comments before it treats a quote as a string.
function themeEditorObjectBody(source, constName) {
  const start = source.indexOf(`const ${constName}`)
  if (start < 0) throw new Error(`missing ${constName}`)
  const eq = source.indexOf('= {', start)
  if (eq < 0 || eq - start > 500) throw new Error(`no object for ${constName}`)
  let depth = 0, end = -1
  for (let p = eq + 2; p < source.length; p++) {
    if (source.startsWith('//', p)) { p = source.indexOf('\n', p); if (p < 0) break; continue }
    if (source.startsWith('/*', p)) { const stop = source.indexOf('*/', p + 2); p = stop < 0 ? source.length : stop + 1; continue }
    const c = source[p]
    if (c === "'" || c === '"' || c === '`') {
      p++
      while (p < source.length && source[p] !== c) { if (source[p] === '\\') p++; p++ }
      continue
    }
    if (c === '{') depth++
    else if (c === '}') { depth--; if (depth === 0) { end = p; break } }
  }
  if (end < 0) throw new Error(`unclosed ${constName}`)
  return source.slice(eq + 3, end)
}
function themeEditorObjectKeys(body) {
  const keys = []
  let i = 0, depth = 0
  while (i < body.length) {
    while (i < body.length && /\s/.test(body[i])) i++
    if (i >= body.length) break
    if (body.startsWith('//', i)) { const n = body.indexOf('\n', i); i = n < 0 ? body.length : n + 1; continue }
    if (body.startsWith('/*', i)) { const stop = body.indexOf('*/', i + 2); i = stop < 0 ? body.length : stop + 2; continue }
    if (depth === 0) {
      const match = /^('([^']*)'|"([^"]*)"|([A-Za-z_][A-Za-z0-9_]*))\s*:/.exec(body.slice(i))
      if (match) { keys.push(match[2] ?? match[3] ?? match[4]); i += match[0].length; continue }
    }
    const c = body[i]
    if (c === "'" || c === '"' || c === '`') {
      i++
      while (i < body.length && body[i] !== c) { if (body[i] === '\\') i++; i++ }
      i++
      continue
    }
    if (c === '{') depth++
    else if (c === '}') depth--
    i++
  }
  return keys
}
function themeEditorStateModelNames(unit, editor) {
  if (!/const STATES\b[^=]*=\s*\{\s*\.\.\.IDLE,\s*\.\.\.PENDING\s*\}/.test(unit)) throw new Error('theme editor states are no longer IDLE plus PENDING')
  if (!/const OUTCOME_NAMES = \['resolved', \.\.\.ERROR_CLASSES\]/.test(unit)) throw new Error('theme editor outcome names changed')
  const begin = unit.indexOf("describe('theme editor state model'")
  const end = unit.indexOf("describe('theme editor runner'", begin)
  if (begin < 0 || end < 0) throw new Error('theme editor state model block moved')
  const region = unit.slice(begin, end)
  const cell = region.match(/it\.each\(cells\)\('(%s[^']*)'/)
  const outcomeFormats = [...region.matchAll(/it\.each\(outcomes\)\('(%s[^']*)'/g)].map(match => match[1])
  if (!cell || outcomeFormats.length !== 3) throw new Error('theme editor matrix titles changed')
  const fill = (fmt, args) => { let n = 0; return fmt.replace(/%s/g, () => args[n++]) }
  const idle = themeEditorObjectKeys(themeEditorObjectBody(unit, 'IDLE'))
  const pending = themeEditorObjectKeys(themeEditorObjectBody(unit, 'PENDING'))
  const events = themeEditorObjectKeys(themeEditorObjectBody(unit, 'EVENTS'))
  for (const [label, keys] of [['IDLE', idle], ['PENDING', pending], ['EVENTS', events]]) {
    if (new Set(keys).size !== keys.length || !keys.length) throw new Error(`duplicate or empty ${label} keys`)
  }
  const classes = [...editor.match(/export const ERROR_CLASSES = \[([^\]]+)\]/)[1].matchAll(/'([^']+)'/g)].map(match => match[1])
  if (new Set(classes).size !== classes.length || !classes.length) throw new Error('ERROR_CLASSES changed shape')
  const states = [...idle, ...pending.filter(name => !idle.includes(name))]
  const outcomes = ['resolved', ...classes]
  const prefix = 'theme editor state model > '
  const names = new Set()
  for (const state of states) for (const event of events) names.add(prefix + fill(cell[1], [state, event]))
  for (const name of pending) for (const outcome of outcomes) for (const fmt of outcomeFormats) names.add(prefix + fill(fmt, [name, outcome]))
  if (names.size !== states.length * events.length + pending.length * outcomes.length * outcomeFormats.length) throw new Error('theme editor case generation collided')
  return names
}

test('AEON-642 theme editor state model registrations are exactly the cases the suite emits', () => {
  const unit = readFileSync(new URL('../../web/tests/theme-editor.unit.test.ts', import.meta.url), 'utf8')
  const editor = readFileSync(new URL('../../web/src/stores/themeEditor.ts', import.meta.url), 'utf8')
  const expected = themeEditorStateModelNames(unit, editor)
  const manifest = JSON.parse(readFileSync(new URL('../ci/web-test-tiers.json', import.meta.url)))
  const prefix = 'theme editor state model > '
  const declared = manifest.tests.filter(row => row.kind === 'vitest' && row.file === 'tests/theme-editor.unit.test.ts' && row.name.startsWith(prefix))
  const declaredNames = new Set(declared.map(row => row.name))
  const stale = [...declaredNames].filter(name => !expected.has(name)).sort()
  const missing = [...expected].filter(name => !declaredNames.has(name)).sort()
  assert.deepEqual({ stale: stale.length, missing: missing.length, staleSample: stale.slice(0, 3), missingSample: missing.slice(0, 3) },
    { stale: 0, missing: 0, staleSample: [], missingSample: [] })
  for (const row of declared) {
    assert.equal(row.tier, 'NIGHTLY', row.name)
    assert.equal(manifest.postGateCases.includes(key(row)), true, row.name)
  }
  const ids = new Set(declared.map(key))
  const extraPostGate = manifest.postGateCases.filter(id => id.includes(prefix) && !ids.has(id))
  assert.deepEqual(extraPostGate, [])
})
