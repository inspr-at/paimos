// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { run } from '../../scripts/test-tiers/cli.mjs'
import { command, root, web } from '../../scripts/test-tiers/collect.mjs'
import { resolve } from 'node:path'

test('native Node and Vitest selectors execute the requested registrations, rather than skip them', async () => {
  const manifest=JSON.parse(readFileSync(resolve(root,'scripts/ci/web-test-tiers.json'),'utf8'))
  const node=manifest.tests.find(row=>row.tier==='ESSENTIAL'&&row.file==='tests/access.test.ts')
  const vitest=manifest.tests.find(row=>row.tier==='ESSENTIAL'&&row.file==='tests/authz.unit.test.ts')
  const nested=manifest.tests.find(row=>row.file==='tests/kind-convert.test.ts')
  assert.ok(node);assert.ok(vitest);assert.ok(nested)
  // Collection must evaluate parameter registrations without executing bodies.
  const collected=JSON.parse(command(process.execPath,['--import',resolve(root,'scripts/test-tiers/node-collect-hook.mjs'),
    resolve(root,'scripts/test-tiers/node-collect.mjs'),resolve(web,node.file)],{cwd:web}))
  assert.equal(collected.length,manifest.tests.filter(row=>row.file===node.file).length)
  assert.ok(collected.some(row=>row.name===node.name))
  const rows=[node,vitest,nested]
  const job='native-selection-regression'
  const env={...process.env,GITHUB_STEP_SUMMARY:undefined}
  // This launches an independent CLI, not a recursive node:test run.
  delete env.NODE_TEST_CONTEXT
  assert.equal(await run('web',{tests:rows,all:rows,full:false,reason:'runner regression'},
    {unit:true,job,env}),0)
  const report=JSON.parse(readFileSync(resolve(root,`tmp/test-tiers/${job}-measurement.json`),'utf8'))
  assert.equal(report.classes.ESSENTIAL.passed,2)
  assert.equal(report.classes.ESSENTIAL.skipped,0)
  assert.equal(report.classes.ESSENTIAL.notRun,0)
  assert.equal(report.classes.NIGHTLY.passed,1)
  assert.equal(report.classes.NIGHTLY.notRun,0)
})
