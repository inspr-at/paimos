// SPDX-License-Identifier: AGPL-3.0-only
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { resolve } from 'node:path'
import { root } from './collect.mjs'
import { exactPattern } from './core.mjs'
import { outputTail } from './failures.mjs'

// AEON-1025: only this owner has a native compile/list/run profile and an
// executed-case equality proof in scripts/ci/go-compile-baseline.json.
export const compiledGoOwners = ['internal/nodes']

export function createGoRunner({ execute, command, env, compileOwners = compiledGoOwners, now = () => performance.now() }) {
  const owners = new Map(), measurements = []
  let scratch
  const timed = (action, record) => {
    const begin = now()
    try { return action() } finally { record((now() - begin) / 1000) }
  }
  const options = { env: { ...env, GOMAXPROCS: '2' } }
  return {
    measurements,
    prepare(owner, stem) {
      // GOFLAGS can contain runtime options that -c does not bake into the
      // binary. Keep the original runner for anything beyond CI's known flag.
      const flags = env.GOFLAGS?.trim() ?? ''
      const measurement = { owner, compiled: compileOwners.includes(owner) && ['', '-count=1'].includes(flags), compileSeconds: 0, listSeconds: 0, executions: [] }
      measurements.push(measurement)
      const entry = { measurement }
      owners.set(owner, entry)
      if (measurement.compiled) {
        scratch ??= mkdtempSync(resolve(tmpdir(), 'aeon-go-binaries-'))
        entry.binary = resolve(scratch, `${owner.replaceAll('/', '-')}.test`)
        // Normal go test omits linker debug symbols; -c otherwise keeps them,
        // increasing link/copy/startup work without strengthening the tests.
        const result = timed(() => execute('go', ['test', '-p', '2', '-c', '-ldflags=-s -w', '-o', entry.binary, `./${owner}`], `${stem}.compile.log`, options),
          seconds => { measurement.compileSeconds = seconds })
        if (result.code || result.error || result.signal) throw new Error(`Go test compilation failed: ${owner}; ${outputTail(`${result.output}\n${result.stderr}`) || result.error || result.signal || result.code}`)
      }
      return timed(() => entry.binary
        ? command(entry.binary, ['-test.paniconexit0', '-test.list=^(Test|Fuzz)'], { cwd: resolve(root, owner), env: { ...options.env, PWD: resolve(root, owner) } })
        : command('go', ['test', '-p', '2', '-list', '^(Test|Fuzz)', `./${owner}`], options),
        seconds => { measurement.listSeconds = seconds })
    },
    execute(owner, rows, stem) {
      const { binary, measurement } = owners.get(owner)
      const pattern = exactPattern(rows.map(row => row.name))
      // go test runs the binary in the source package directory. test2json's
      // package identity and verbose marker retain top-level/subtest events,
      // package failures, skips and panic diagnostics for the existing gate.
      const args = binary
        ? ['tool', 'test2json', '-t', '-p', `github.com/inspr-at/paimos/${owner}`, binary,
          '-test.paniconexit0', '-test.v=test2json', '-test.count=1', '-test.timeout=25m', `-test.run=${pattern}`]
        : ['test', '-p', '2', '-count=1', '-timeout=25m', '-json', '-run', pattern, `./${owner}`]
      return timed(() => execute('go', args, `${stem}.jsonl`, binary
        ? { cwd: resolve(root, owner), env: { ...options.env, PWD: resolve(root, owner) } } : options),
        seconds => { measurement.executions.push({ attempt: measurement.executions.length + 1, selectedTests: rows.length, seconds }) })
    },
    close() {
      if (scratch) rmSync(scratch, { recursive: true, force: true })
    },
  }
}
