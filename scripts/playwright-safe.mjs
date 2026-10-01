// SPDX-License-Identifier: AGPL-3.0-only
import { spawn, spawnSync } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { closeSync, openSync, readFileSync, unlinkSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { suiteLockPath } from './playwright-global-setup.mjs'

const delay = ms => new Promise(resolveDelay => setTimeout(resolveDelay, ms))

export function acquireLock(path = suiteLockPath) {
  const token = randomUUID()
  let fd
  try { fd = openSync(path, 'wx', 0o600) } catch (error) {
    if (error.code !== 'EEXIST') throw error
    let pid = 'unknown'
    try { pid = JSON.parse(readFileSync(path, 'utf8')).pid } catch { /* fail closed */ }
    throw new Error(`Another Aeon browser suite holds ${path} (PID ${pid}). Wait for it to finish. If its owner has exited, inspect the lock and move that stale lock to trash; never stop another worker's processes.`)
  }
  try { writeFileSync(fd, JSON.stringify({ pid: process.pid, token, started: new Date().toISOString() })) }
  finally { closeSync(fd) }
  return { token, release() {
    // Never remove a lock that another owner replaced.
    if (JSON.parse(readFileSync(path, 'utf8')).token === token) unlinkSync(path)
  } }
}

function processTable() {
  // Read numeric identity and executable names only, never args or environments.
  const result = spawnSync('ps', ['-axo', 'pid=,pgid=,lstart=,comm='], { encoding: 'utf8' })
  if (result.status !== 0) throw new Error('Cannot inspect the owned browser process group')
  return result.stdout.trim().split('\n').flatMap(line => {
    const match = line.trim().match(/^(\d+)\s+(\d+)\s+(\w+\s+\w+\s+\d+\s+\d\d:\d\d:\d\d\s+\d+)\s+(.+)$/)
    return match ? [{ pid: Number(match[1]), group: Number(match[2]), started: match[3].replaceAll(/\s+/g, ' '), name: match[4] }] : []
  })
}

export function groupProcesses(pgid) { return processTable().filter(row => row.group === pgid) }

function signalGroup(pgid, signal) {
  try { process.kill(-pgid, signal) } catch (error) { if (error.code !== 'ESRCH') throw error }
}

function trackGroups(log) {
  const groups = new Map()
  let consumed = 0, rootAdded = false
  return child => {
    if (!rootAdded && child?.pid) { groups.set(child.pid, ''); rootAdded = true }
    const contents = readFileSync(log, 'utf8')
    const complete = contents.lastIndexOf('\n') + 1
    for (const line of contents.slice(consumed, complete).split('\n').filter(Boolean)) {
      const entry = JSON.parse(line)
      if (!Number.isSafeInteger(entry.pid) || entry.pid <= 1) throw new Error('Invalid owned process group identity')
      groups.set(entry.pid, entry.started.replaceAll(/\s+/g, ' '))
    }
    consumed = complete
    const table = processTable()
    for (const [pid, started] of groups) {
      const leader = table.find(row => row.pid === pid)
      if (leader && started && leader.started !== started) throw new Error('Owned process group identity changed; refusing to signal it')
      // Retire exited groups so large suites do not poll every historical PID.
      if (!table.some(row => row.group === pid)) groups.delete(pid)
    }
    return { groups: [...groups.keys()], rows: table.filter(row => groups.has(row.group)) }
  }
}

export async function runOwnedCommand(command, args, { cwd, env = process.env, lockPath = suiteLockPath, graceMs = 5000 } = {}) {
  if (process.platform === 'win32') throw new Error('Browser supervision requires POSIX process groups; use Linux CI on Windows')
  const lock = acquireLock(lockPath)
  const groupLog = `${lockPath}.${lock.token}.groups`
  writeFileSync(groupLog, '', { flag: 'wx', mode: 0o600 })
  const start = Date.now()
  let child, interrupt, peak = 0, timer, monitoringError
  const browserCount = rows => rows.filter(row => /chrom(?:e|ium)|headless_shell/i.test(row.name)).length
  const snapshot = trackGroups(groupLog)
  const rowsOwned = () => snapshot(child).rows
  const signalOwned = signal => { for (const group of snapshot(child).groups) signalGroup(group, signal) }
  const sample = () => {
    try { peak = Math.max(peak, browserCount(rowsOwned())) }
    catch (error) { monitoringError = error; signalGroup(child.pid, 'SIGTERM') }
  }
  const handlers = new Map()
  let escalation
  try {
    const preload = new URL('./playwright-owned-groups.mjs', import.meta.url).href
    child = spawn(command, args, { cwd, env: { ...env, AEON_PW_RUN: lock.token, AEON_PW_GROUP_LOG: groupLog, NODE_OPTIONS: `${env.NODE_OPTIONS ?? ''} --import=${preload}` }, stdio: 'inherit', detached: true })
    const exited = new Promise((resolveExit, reject) => {
      child.once('error', reject)
      // 'exit', not 'close': an orphan may still hold inherited output pipes.
      child.once('exit', (code, signal) => resolveExit({ code, signal }))
    })
    for (const signal of ['SIGINT', 'SIGTERM', 'SIGHUP']) {
      const handler = () => {
        if (interrupt) return
        interrupt = signal
        signalGroup(child.pid, signal)
        escalation = setTimeout(() => signalOwned('SIGKILL'), graceMs)
      }
      handlers.set(signal, handler)
      process.on(signal, handler)
    }
    console.log('AEON_PW_PROCESSES before=0 (new owned group)')
    sample()
    timer = setInterval(sample, 250)
    const result = await exited
    clearInterval(timer)
    clearTimeout(escalation)
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
    console.log(`AEON_PW_PROCESSES ${JSON.stringify(metrics)}`)
    if (rows.length) throw new Error('Owned browser processes survived shutdown; host lock retained')
    if (monitoringError) throw monitoringError
    return { code: interrupt ? ({ SIGINT: 130, SIGTERM: 143, SIGHUP: 129 }[interrupt]) : result.code ?? 1, metrics }
  } finally {
    clearInterval(timer)
    clearTimeout(escalation)
    for (const [signal, handler] of handlers) process.off(signal, handler)
    // Keep the lock if cleanup cannot prove the group is gone.
    if (!child?.pid || rowsOwned().length === 0) { unlinkSync(groupLog); lock.release() }
  }
}

export async function runPlaywright(args, options = {}) {
  const web = fileURLToPath(new URL('../web/', import.meta.url))
  const cli = resolve(web, 'node_modules/@playwright/test/cli.js')
  return runOwnedCommand(process.execPath, [cli, 'test', ...args], { cwd: web, ...options })
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try { process.exitCode = (await runPlaywright(process.argv.slice(2))).code }
  catch (error) { console.error(error.message); process.exitCode = 1 }
}
