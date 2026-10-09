// SPDX-License-Identifier: AGPL-3.0-only
// Run on the approved test machine with its isolated test database. This is a
// comparison of native executions, never cached test-result evidence.
import assert from 'node:assert/strict'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { spawnSync } from 'node:child_process'
import { collectGo, root, saveJSON } from './collect.mjs'
import { load } from './cli.mjs'
import { exactPattern, key, splitOwner, validate } from './core.mjs'
import { goOutcomes } from './report.mjs'
import { generateOpenAPI } from '../../api/generate.mjs'

export function profile(output) {
  const owner = 'internal/nodes', pkg = `github.com/inspr-at/paimos/${owner}`
  const scratch = mkdtempSync(resolve(tmpdir(), 'aeon-go-profile-'))
  const env = { ...process.env, GOMAXPROCS: '2' }
  const native = (bin, args, name, cwd = root) => {
    const begin = performance.now()
    const result = spawnSync(bin, args, { cwd, env, encoding: 'utf8', timeout: 30 * 60 * 1000, maxBuffer: 64 * 1024 * 1024 })
    const seconds = (performance.now() - begin) / 1000
    writeFileSync(resolve(scratch, `${name}.stdout`), result.stdout ?? '')
    writeFileSync(resolve(scratch, `${name}.stderr`), result.stderr ?? '')
    if (result.error || result.status !== 0) throw new Error(`${name} failed: ${result.error?.code ?? result.status}; ${result.stdout?.slice(-3000)} ${result.stderr?.slice(-3000)}`)
    return { seconds, output: result.stdout }
  }
  try {
    generateOpenAPI()
    const manifest = load('go'), inventory = validate(manifest, collectGo().tests)
    const all = inventory.filter(row => row.package === owner && row.active)
    const selected = all.filter(row => row.tier !== 'NIGHTLY' && row.lane !== 'timing')
    const parts = splitOwner(selected, manifest.timingWeights.owners[owner], 2)
    const listNames = text => text.split('\n').filter(name => /^(?:Test|Fuzz)\w+$/.test(name)).sort()
    const names = all.map(row => row.name).sort(), cases = [], measurements = []
    for (const [index, part] of parts.entries()) {
      const pattern = exactPattern(part.rows.map(row => row.name))
      const beforeList = native('go', ['test', '-p', '2', '-list', '^(Test|Fuzz)', `./${owner}`], `before-list-${index}`)
      assert.deepEqual(listNames(beforeList.output), names)
      const before = native('go', ['test', '-p', '2', '-count=1', '-timeout=25m', '-json', '-run', pattern, `./${owner}`], `before-run-${index}`)
      const binary = resolve(scratch, `nodes-${index}.test`)
      const compile = native('go', ['test', '-p', '2', '-c', '-o', binary, `./${owner}`], `compile-${index}`)
      const afterList = native(binary, ['-test.list=^(Test|Fuzz)'], `after-list-${index}`, resolve(root, owner))
      assert.deepEqual(listNames(afterList.output), names)
      const after = native('go', ['tool', 'test2json', '-t', '-p', pkg, binary, '-test.v=test2json', '-test.count=1', '-test.timeout=25m', `-test.run=${pattern}`], `after-run-${index}`, resolve(root, owner))
      const outcomes = text => goOutcomes(text).sort((a, b) => a.key.localeCompare(b.key))
      assert.deepEqual(outcomes(after.output), outcomes(before.output), 'Every selected top-level outcome is preserved')
      assert.deepEqual(outcomes(after.output).map(row => row.key).sort(), part.rows.map(key).sort())
      assert.ok(outcomes(after.output).every(row => row.status === 'passed' && row.started), 'No skipped or absent proof cases')
      const started = text => text.split('\n').filter(Boolean).map(JSON.parse).filter(event => event.Action === 'run' && event.Test).map(event => `${event.Package}:${event.Test}`).sort()
      assert.deepEqual(started(after.output), started(before.output), 'Subtest execution identities are preserved')
      const elapsed = text => text.split('\n').filter(Boolean).map(JSON.parse).findLast(event => !event.Test && event.Action === 'pass').Elapsed
      measurements.push({ part: index + 1, selectedTests: part.rows.length, beforeListSeconds: beforeList.seconds,
        beforeRunWallSeconds: before.seconds, beforeRunPackageSeconds: elapsed(before.output),
        compileSeconds: compile.seconds, binaryListSeconds: afterList.seconds,
        binaryRunWallSeconds: after.seconds, binaryRunPackageSeconds: elapsed(after.output),
        executedIncludingSubtests: started(after.output).length })
      cases.push(...part.rows.map(key))
      console.log(JSON.stringify(measurements.at(-1)))
    }
    assert.equal(new Set(cases).size, selected.length)
    const report = { version: 1, ticket: 'AEON-1025', sourceCommit: native('git', ['rev-parse', 'HEAD'], 'commit').output.trim(),
      runnerClass: 'approved-mbp2606-offload', goVersion: native('go', ['version'], 'version').output.trim(),
      cache: 'Existing Go build cache; no cache purge and no test-result reuse (-count=1).',
      package: owner, nativeTests: names.length, selectedTests: cases.length, listSetEqual: true,
      executedSetEqual: true, subtestSetEqual: true, measurements, cases: cases.sort(),
      followUpOwner: 'AEON lead / AEON-1014 coordinator: post the seven-day CI outcome after deployment.' }
    saveJSON(output, report)
    return report
  } finally {
    rmSync(scratch, { recursive: true, force: true })
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  if (process.argv.length !== 3) throw new Error('Usage: node scripts/test-tiers/go-compile-profile.mjs OUTPUT.json')
  profile(resolve(process.argv[2]))
}
