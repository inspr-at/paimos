// SPDX-License-Identifier: AGPL-3.0-only
import { spawn } from 'node:child_process'
import { appendFileSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { suiteLockPath } from './playwright-global-setup.mjs'
import { acquireLock } from './playwright-lock.mjs'
import { recordRootGroup, trackGroups } from './playwright-processes.mjs'
export { acquireLock } from './playwright-lock.mjs'
export { groupProcesses } from './playwright-processes.mjs'

const delay = ms => new Promise(resolveDelay => setTimeout(resolveDelay, ms))

export async function runOwnedCommand(command, args, { cwd, env = process.env, lockPath = suiteLockPath, graceMs = 5000, capture = false, rootIdentityWait } = {}) {
  if (process.platform === 'win32') throw new Error('Browser supervision requires POSIX process groups; use Linux CI on Windows')
  const start = Date.now()
  let lock, groupLog, snapshot, child, interrupt, peak = 0, timer, monitoringError, journalCreated = false, stdout = '', stderr = ''
  const browserCount = rows => rows.filter(row => /chrom(?:e|ium)|headless_shell/i.test(row.name)).length
  const rowsOwned = () => snapshot.snapshot().rows
  const signalOwned = signal => {
    try {
      const errors = snapshot.signal(signal)
      if (errors.length) monitoringError ??= errors[0]
    } catch (error) { monitoringError ??= error }
  }
  const sample = () => {
    try { peak = Math.max(peak, browserCount(rowsOwned())) }
    catch (error) { monitoringError ??= error; signalOwned('SIGTERM') }
  }
  const handlers = new Map()
  let escalation
  for (const signal of ['SIGINT', 'SIGTERM', 'SIGHUP']) {
    const handler = () => {
      if (interrupt) return
      interrupt = signal
      if (!journalCreated || !child?.pid) return
      // Capture live browsers before signalling; short runs may finish
      // between periodic samples, especially immediately after readiness.
      sample()
      signalOwned(signal)
      escalation = setTimeout(() => signalOwned('SIGKILL'), graceMs)
    }
    handlers.set(signal, handler)
    process.on(signal, handler)
  }
  try {
    lock = await acquireLock(lockPath, { graceMs })
    groupLog = `${lockPath}.${lock.token}.groups`
    snapshot = trackGroups(groupLog)
    if (interrupt) throw Object.assign(new Error(`Browser startup interrupted by ${interrupt}`), { signal: interrupt })
    writeFileSync(groupLog, '', { flag: 'wx', mode: 0o600 })
    journalCreated = true
    const preload = new URL('./playwright-owned-groups.mjs', import.meta.url).href
    child = spawn(command, args, { cwd, env: { ...env, AEON_PW_RUN: lock.token, AEON_PW_GROUP_LOG: groupLog, AEON_PW_OWNER: String(process.pid), AEON_PW_OWNER_STARTED: lock.started, NODE_OPTIONS: `${env.NODE_OPTIONS ?? ''} --import=${preload}` }, stdio: capture ? ['ignore', 'pipe', 'pipe'] : 'inherit', detached: true })
    if (capture) {
      child.stdout.on('data', data => { stdout += data })
      child.stderr.on('data', data => { stderr += data })
    }
    const exited = new Promise((resolveExit, reject) => {
      child.once('error', reject)
      // 'exit', not 'close': an orphan may still hold inherited output pipes.
      child.once('exit', (code, signal) => resolveExit({ code, signal }))
    })
    // Inject only retry scheduling for deterministic exit/lookup interleavings.
    // Tests can wait on a child-exit barrier without racing Node startup
    // against the production retry window. Identity checks and the bounded
    // attempt count remain in recordRootGroup.
    try { await recordRootGroup(child, entry => appendFileSync(groupLog, `${JSON.stringify(entry)}\n`), { wait: rootIdentityWait }) }
    catch (error) { await exited; throw error }
    if (interrupt) signalOwned(interrupt)
    console.error('AEON_PW_PROCESSES before=0 (new owned group)')
    sample()
    timer = setInterval(sample, 250)
    const result = await exited
    clearInterval(timer)
    clearTimeout(escalation)
    sample()
    // Playwright closes fixture browsers normally. Sweep only this run's groups
    // as a backstop for failed startup, custom launches and interrupted workers.
    signalOwned('SIGTERM')
    const deadline = Date.now() + graceMs
    while (rowsOwned().length && Date.now() < deadline) await delay(50)
    if (rowsOwned().length) signalOwned('SIGKILL')
    const killDeadline = Date.now() + 2000
    while (rowsOwned().length && Date.now() < killDeadline) await delay(50)
    const rows = rowsOwned()
    const metrics = { before: 0, peak, after: browserCount(rows), remaining_processes: rows.length, wall_ms: Date.now() - start, signal: interrupt ?? result.signal }
    console.error(`AEON_PW_PROCESSES ${JSON.stringify(metrics)}`)
    const state = snapshot.snapshot()
    if (rows.length || state.unverified.length || state.issues.length) throw new Error('Owned browser processes or incomplete identities survived shutdown; host lock retained')
    if (monitoringError) throw monitoringError
    return { code: interrupt ? ({ SIGINT: 130, SIGTERM: 143, SIGHUP: 129 }[interrupt]) : result.code ?? 1, metrics, ...(capture ? { stdout, stderr } : {}) }
  } finally {
    clearInterval(timer)
    clearTimeout(escalation)
    // Always attempt cleanup after a throw. A reused PID is excluded by the
    // tracker; it cannot abort signalling or lock release for valid siblings.
    let releasable = !child?.pid
    try {
      if (journalCreated && child?.pid) {
        signalOwned('SIGKILL')
        const deadline = Date.now() + 2000
        while (rowsOwned().length && Date.now() < deadline) await delay(50)
        const state = snapshot.snapshot()
        releasable = state.rows.length === 0 && state.unverified.length === 0 && state.issues.length === 0
      }
      if (releasable) {
        lock?.release({ journal: journalCreated })
      }
    } finally {
      lock?.close()
      clearTimeout(escalation)
      for (const [signal, handler] of handlers) process.off(signal, handler)
    }
  }
}

export function localWorkerArgs(args, env = process.env) {
  if ((env.CI && !['0', 'false'].includes(env.CI)) || env.PW_WORKERS !== undefined) return [...args]
  const result = []
  for (let index = 0; index < args.length; index++) {
    const arg = args[index]
    if (arg === '--workers' || arg === '-j') { index++; continue }
    if (arg.startsWith('--workers=') || /^-j=?\d/.test(arg)) continue
    result.push(arg)
  }
  return [...result, '--workers=1']
}

export async function runPlaywright(args, options = {}) {
  const web = fileURLToPath(new URL('../web/', import.meta.url))
  const cli = resolve(web, 'node_modules/@playwright/test/cli.js')
  return runOwnedCommand(process.execPath, [cli, 'test', ...localWorkerArgs(args, options.env ?? process.env)], { cwd: web, ...options })
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try { process.exitCode = (await runPlaywright(process.argv.slice(2))).code }
  catch (error) { console.error(error.message); process.exitCode = ({ SIGINT: 130, SIGTERM: 143, SIGHUP: 129 }[error.signal]) ?? 1 }
}
