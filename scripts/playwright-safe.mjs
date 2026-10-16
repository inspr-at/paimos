// SPDX-License-Identifier: AGPL-3.0-only
import { spawn } from 'node:child_process'
import { appendFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, renameSync, rmdirSync, unlinkSync, writeFileSync } from 'node:fs'
import { createConnection, createServer } from 'node:net'
import { basename, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { suiteLockPath } from './playwright-global-setup.mjs'
import { acquireLock, holdLimitMs } from './playwright-lock.mjs'
import { recordRootGroup, trackGroups } from './playwright-processes.mjs'
export { acquireLock } from './playwright-lock.mjs'
export { groupProcesses } from './playwright-processes.mjs'

const delay = ms => new Promise(resolveDelay => setTimeout(resolveDelay, ms))

// One silent additive reporter works for the direct CLI and nested shard
// runners. Only event names cross this socket, never test output or arguments.
async function progressServer(lock, onError) {
  const directory = mkdtempSync('/tmp/aeon-pw-progress-'), path = join(directory, 'events')
  const sockets = new Set()
  let pending = 0, closed = false, failure
  const flush = () => {
    try { while (pending && lock.progress()) pending-- }
    catch (error) { pending = 0; failure = error; onError(error) }
  }
  const server = createServer(socket => {
    sockets.add(socket)
    let buffer = '', authenticated = false
    socket.on('error', onError)
    socket.on('close', () => sockets.delete(socket))
    socket.on('data', data => {
      buffer += data.toString()
      let end
      while ((end = buffer.indexOf('\n')) !== -1) {
        const line = buffer.slice(0, end); buffer = buffer.slice(end + 1)
        // The cap bounds one message. A coalesced burst of short lines is
        // still one heartbeat each and must not be rejected as one message.
        if (line.length > 65536) { socket.destroy(new Error('Oversized browser progress message')); return }
        if (!authenticated) {
          if (line !== lock.token) { socket.destroy(); return }
          authenticated = true
        } else if (/^[a-z]+$/.test(line)) {
          if (++pending > 8192) { pending = 0; socket.destroy(new Error('Browser progress event queue overflow')); return }
          flush()
        }
      }
      if (buffer.length > 65536) socket.destroy(new Error('Oversized browser progress message'))
    })
  })
  try {
    await new Promise((ready, reject) => { server.once('error', reject); server.listen(path, ready) })
  } catch (error) { server.close(); rmdirSync(directory); throw error }
  server.on('error', onError)
  const timer = setInterval(flush, 100)
  return { path, async close() {
    if (closed) return
    closed = true
    clearInterval(timer)
    for (const socket of sockets) socket.destroy()
    await new Promise(done => server.close(done))
    try {
      // The reporter may exit while another waiter briefly owns the guard.
      // Drain those deferred events before releasing the acquisition.
      const deadline = Date.now() + 2000
      while (pending && Date.now() < deadline) { flush(); if (pending) await delay(25) }
      if (failure) throw failure
      if (pending) throw new Error('Browser reporter progress could not be recorded; host lock retained')
    } finally { rmdirSync(directory) }
  } }
}

export default class ProgressReporter {
  constructor() {
    this.unsent = 0
    if (!process.env.AEON_PW_PROGRESS_SOCKET) return
    this.socket = createConnection(process.env.AEON_PW_PROGRESS_SOCKET)
    this.socket.on('error', error => { this.error = error })
    this.socket.write(`${process.env.AEON_PW_RUN}\n`)
  }
  printsToStdio() { return false }
  // A reporter can emit a whole run before the socket is read. Keep every
  // heartbeat and yield while the buffer is above the cap; destroying the
  // socket would fail a run whose tests had already passed.
  event() {
    if (!this.socket || this.socket.destroyed || this.error) return
    this.unsent++
    this.drainProgress()
  }
  drainProgress() {
    if (this.draining || !this.socket || this.socket.destroyed) return
    while (this.unsent > 0) {
      if (this.socket.writableLength > 32768) {
        this.draining = true
        setImmediate(() => { this.draining = false; if (!this.error) this.drainProgress() })
        return
      }
      try { this.socket.write('progress\n') }
      catch (error) { this.error = error; return }
      this.unsent--
    }
  }
  onBegin(config, suite) {
    this.event()
    if (process.env.AEON_PW_CHUNK_EXPECTED) {
      const expected = JSON.parse(readFileSync(process.env.AEON_PW_CHUNK_EXPECTED, 'utf8'))
      const keys = new Set(suite.allTests().map(test => JSON.stringify([test.parent.project().name, test.location.file, test.repeatEachIndex, test.titlePath().slice(3)])))
      if (keys.size !== expected.length || expected.some(key => !keys.has(key))) {
        this.error = new Error('Playwright chunk selection differs from the native listing; refusing partial success')
        throw this.error
      }
    }
    if (!process.env.AEON_PW_CHUNK_PLAN) return
    const chunks = new Map()
    for (const test of suite.allTests()) {
      const project = test.parent.project(), file = test.location.file
      const key = JSON.stringify([project.name, file, test.repeatEachIndex])
      const chunk = chunks.get(key) ?? { project: project.name, file, repeat: test.repeatEachIndex, repeats: project.repeatEach, titles: [], greps: [] }
      if (!chunks.has(key)) chunks.set(key, chunk)
      chunk.titles.push(test.titlePath().slice(3))
      if (typeof test._grepTitleWithTags === 'function') chunk.greps.push(grepIdentity(test._grepTitleWithTags()))
    }
    writeFileSync(process.env.AEON_PW_CHUNK_PLAN, JSON.stringify({ rootDir: config.rootDir, outputDir: config.projects[0]?.outputDir, reporters: config.reporter.filter(([name]) => name !== fileURLToPath(import.meta.url)), chunks: [...chunks.values()] }))
  }
  onTestBegin() { this.event() }
  onTestEnd() { this.event() }
  onStepBegin() { this.event() }
  onStepEnd() { this.event() }
  onStdOut() { this.event() }
  onStdErr() { this.event() }
  onError() { this.event() }
  async onEnd() {
    this.event()
    if (!this.socket) return
    const deadline = Date.now() + 20000
    while (this.unsent > 0 && !this.socket.destroyed && !this.error && Date.now() < deadline) {
      await new Promise(resolve => setImmediate(resolve))
    }
    if (this.unsent > 0) this.error ??= new Error('Browser reporter progress could not be recorded; host lock retained')
    await new Promise(done => { if (this.socket.destroyed) done(); else { this.socket.once('close', done); this.socket.end() } })
    // Playwright catches reporter exceptions. Explicitly override status so
    // selection drift or a lost progress socket cannot become a green run.
    if (this.error) { console.error(this.error.message); return { status: 'failed' } }
  }
}

export async function runOwnedCommand(command, args, { cwd, env = process.env, lockPath = suiteLockPath, graceMs = 5000, capture = false, captureLimitBytes = 64 * 1024 * 1024, rootIdentityWait, lockOptions = {}, maxHoldMs = holdLimitMs, clock = { now: Date.now, setTimeout, clearTimeout }, heartbeat = true } = {}) {
  if (process.platform === 'win32') throw new Error('Browser supervision requires POSIX process groups; use Linux CI on Windows')
  const start = Date.now()
  let lock, groupLog, snapshot, child, interrupt, peak = 0, timer, monitoringError, journalCreated = false, stdout = '', stderr = '', capturedBytes = 0, progress, leaseTimer, timedOut = false
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
    lock = await acquireLock(lockPath, { graceMs, wait: true, command: `${basename(command)} (supervised browser command)`, ...lockOptions })
    const acquiredAt = clock.now()
    groupLog = `${lockPath}.${lock.token}.groups`
    snapshot = trackGroups(groupLog)
    if (interrupt) throw Object.assign(new Error(`Browser startup interrupted by ${interrupt}`), { signal: interrupt })
    writeFileSync(groupLog, '', { flag: 'wx', mode: 0o600 })
    journalCreated = true
    if (heartbeat) progress = await progressServer(lock, error => { monitoringError ??= error; if (child?.pid) signalOwned('SIGTERM') })
    const preload = new URL('./playwright-owned-groups.mjs', import.meta.url).href
    const hasReporter = args.some(arg => arg.startsWith('--add-reporter=') && arg.slice('--add-reporter='.length).split(',').includes(fileURLToPath(import.meta.url)))
    const reporterHook = heartbeat ? (hasReporter ? env.PW_TEST_REPORTER : env.PW_TEST_REPORTER ?? fileURLToPath(import.meta.url)) : undefined
    const childEnv = { ...env, AEON_PW_GROUP_LOG: groupLog, AEON_PW_OWNER: String(process.pid), AEON_PW_OWNER_STARTED: lock.started, NODE_OPTIONS: `${env.NODE_OPTIONS ?? ''} --import=${preload}` }
    if (heartbeat) {
      if (reporterHook) childEnv.PW_TEST_REPORTER = reporterHook
      childEnv.AEON_PW_RUN = lock.token
      childEnv.AEON_PW_PROGRESS_SOCKET = progress.path
    } else {
      // Merge replays every recorded event in one turn. That replay is not a
      // live browser hold, so it must not attach the progress reporter.
      delete childEnv.PW_TEST_REPORTER
      delete childEnv.AEON_PW_PROGRESS_SOCKET
      delete childEnv.AEON_PW_CHUNK_EXPECTED
      delete childEnv.AEON_PW_CHUNK_PLAN
      delete childEnv.AEON_PW_RUN
    }
    child = spawn(command, args, { cwd, env: childEnv, stdio: capture ? ['ignore', 'pipe', 'pipe'] : 'inherit', detached: true })
    // Reserve both TERM grace and bounded KILL cleanup inside the 15-minute
    // acquisition. An oversized indivisible chunk fails; it is never replayed
    // or reported as successful after partial work.
    const budget = Math.min(maxHoldMs, holdLimitMs)
    leaseTimer = clock.setTimeout(() => {
      timedOut = true
      console.error('Aeon browser lock reached its 15-minute acquisition budget; split this spec into smaller chunks')
      signalOwned('SIGTERM')
      escalation = setTimeout(() => signalOwned('SIGKILL'), graceMs)
    }, Math.max(0, acquiredAt + budget - graceMs - 6500 - clock.now()))
    if (capture) {
      const append = (data, output) => {
        if (capturedBytes > captureLimitBytes) return
        capturedBytes += data.length
        if (capturedBytes > captureLimitBytes) {
          monitoringError ??= new Error('Browser command output exceeded its capture limit; partial output is not success')
          signalOwned('SIGTERM')
          escalation ??= setTimeout(() => signalOwned('SIGKILL'), graceMs)
          return
        }
        output(data)
      }
      child.stdout.on('data', data => append(data, data => { stdout += data }))
      child.stderr.on('data', data => append(data, data => { stderr += data }))
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
    clock.clearTimeout(leaseTimer)
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
    return { code: interrupt ? ({ SIGINT: 130, SIGTERM: 143, SIGHUP: 129 }[interrupt]) : timedOut ? 124 : result.code ?? 1, metrics, ...(capture ? { stdout, stderr } : {}) }
  } finally {
    clock.clearTimeout(leaseTimer)
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
        await progress?.close(); progress = undefined
        await lock?.releaseWhenReady({ journal: journalCreated })
      }
    } finally {
      lock?.close()
      try { await progress?.close() }
      finally {
        clearTimeout(escalation)
        for (const [signal, handler] of handlers) process.off(signal, handler)
      }
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

function withoutOptions(args, names) {
  const result = []
  for (let index = 0; index < args.length; index++) {
    const name = args[index].split('=')[0]
    if (names.includes(name)) { if (!args[index].includes('=') && name !== '--last-failed') index++; continue }
    result.push(args[index])
  }
  return result
}
const selectionLimit = 100_000
// Playwright applies --grep before the project suite is attached to the root.
// The reporter's title therefore has one extra leading space from the root's
// empty title. The filter never sees that space.
function grepIdentity(rooted) {
  return rooted.startsWith(' ') ? rooted.slice(1) : rooted
}
function escapeRegExp(value) {
  return value.replace(/[|\\{}()[\]^$+*?.]/g, '\\$&').replaceAll('\n', '\\n').replaceAll('\r', '\\r')
}
function chunkCannotUseTestList(chunk, file) {
  return [chunk.project, file, ...chunk.titles.flat()].some(part => /[\u203A\n\r]/.test(String(part)))
}
function exactGrep(greps) {
  const pattern = `/^(?:${greps.map(escapeRegExp).join('|')})$/`
  if (pattern.length > selectionLimit) throw new Error('Playwright chunk selection is too large to match exactly; refusing partial success')
  return pattern
}
// Playwright's test list splits every › and cannot round-trip a title that
// contains one. Those chunks use the exact grep identity captured at list
// time. The in-process expected set still rejects any drift.
function chunkSelector(base, chunk, file, list) {
  if (!chunkCannotUseTestList(chunk, file)) return [...base, '--test-list', list]
  const identifiable = Array.isArray(chunk.greps) && chunk.greps.length === chunk.titles.length && chunk.greps.every(grep => typeof grep === 'string' && grep.includes(file))
  if (!identifiable) throw new Error('Playwright chunk selection cannot be verified; refusing partial success')
  return [...withoutOptions(base, ['--grep', '-g']), '--grep', exactGrep(chunk.greps)]
}
function withProgressReporter(args, reporter) {
  let additional = ''
  for (let index = 0; index < args.length; index++) {
    if (args[index] === '--add-reporter') additional = args[++index]
    else if (args[index].startsWith('--add-reporter=')) additional = args[index].slice('--add-reporter='.length)
  }
  return [...withoutOptions(args, ['--add-reporter']), `--add-reporter=${additional ? additional + ',' : ''}${reporter}`]
}

// Long batches use the native list to freeze the original selection, then run
// one spec/repetition per acquisition. Native repeat sharding preserves
// testInfo.repeatEachIndex (unlike replacing --repeat-each with a shell loop).
// Every child fully exits and its verified groups are gone before release;
// its next chunk joins the tail of the same host FIFO. Each indivisible chunk
// has the supervisor's 15-minute wall budget, including shutdown. Oversized
// chunks fail with 124 and must be split by the caller. Blob merging emits the
// original reporters once and retains attachments in the run's output tree.
export async function runPlaywright(args, options = {}) {
  const began = Date.now()
  const web = fileURLToPath(new URL('../web/', import.meta.url))
  const cli = resolve(web, 'node_modules/@playwright/test/cli.js')
  const reporter = fileURLToPath(import.meta.url), env = options.env ?? process.env
  const run = (commandArgs, extra = {}) => runOwnedCommand(process.execPath, [cli, ...commandArgs], {
    cwd: web, ...options, lockOptions: { wait: true, command: `node Playwright ${commandArgs[0]}`, ...options.lockOptions }, ...extra,
  })
  args = localWorkerArgs(args, env)
  if (args.some(arg => arg === '--list' || /^--ui(?:=|$|-)/.test(arg)) || env.PWTEST_WATCH) {
    return run(['test', ...withProgressReporter(args, reporter)])
  }
  const scratch = mkdtempSync('/tmp/aeon-pw-plan-'), planFile = join(scratch, 'plan.json'), mergeConfig = join(scratch, 'merge.config.mjs')
  try {
    const listed = await run(['test', ...withProgressReporter([...args, '--list'], reporter)], {
      capture: true, env: { ...env, AEON_PW_CHUNK_PLAN: planFile, PLAYWRIGHT_JSON_OUTPUT_FILE: join(scratch, 'list.json') },
    })
    if (listed.code !== 0 || !existsSync(planFile)) {
      if (!options.capture) { process.stdout.write(listed.stdout); process.stderr.write(listed.stderr) }
      return { ...listed, code: listed.code || 1 }
    }
    const plan = JSON.parse(readFileSync(planFile, 'utf8'))
    // Empty selections still use native handling (including --pass-with-no-tests).
    if (!plan.chunks.length) return run(['test', ...withProgressReporter(args, reporter)])
    mkdirSync(plan.outputDir, { recursive: true })
    const artifacts = mkdtempSync(join(plan.outputDir, 'aeon-chunks-')), blobs = join(artifacts, 'reports')
    mkdirSync(blobs)
    writeFileSync(mergeConfig, `export default ${JSON.stringify({ testDir: plan.rootDir, reporter: plan.reporters })}\n`)
    const base = withoutOptions(args, ['--reporter', '--add-reporter', '--shard', '--test-list', '--output', '--last-failed'])
    let code = 0, last, metrics = []
    for (const [index, chunk] of plan.chunks.entries()) {
      const list = join(scratch, 'chunk.txt'), expected = join(scratch, 'expected.json'), blob = join(artifacts, `blob-${index}`, 'report.zip')
      const file = relative(plan.rootDir, chunk.file).split('\\').join('/')
      writeFileSync(list, chunk.titles.map(titles => `[${chunk.project}] › ${file} › ${titles.join(' › ')}`).join('\n') + '\n')
      writeFileSync(expected, JSON.stringify(chunk.titles.map(titles => JSON.stringify([chunk.project, chunk.file, chunk.repeat, titles]))))
      console.error(`AEON_PW_CHUNK ${index + 1}/${plan.chunks.length} spec=${basename(chunk.file)} repeat=${chunk.repeat + 1}/${chunk.repeats}`)
      last = await run(['test', ...chunkSelector(base, chunk, file, list), `--shard=${chunk.repeat + 1}/${chunk.repeats}`, '--reporter=blob', `--add-reporter=${reporter}`, '--output', join(artifacts, `chunk-${index}`)], {
        capture: true, env: { ...env, AEON_PW_CHUNK_EXPECTED: expected, PLAYWRIGHT_BLOB_OUTPUT_FILE: blob },
        lockOptions: { wait: true, ...options.lockOptions, command: `node Playwright test ${basename(chunk.file)} repeat=${chunk.repeat + 1}/${chunk.repeats}` },
      })
      metrics.push(last.metrics)
      if (existsSync(blob)) { renameSync(blob, join(blobs, `${String(index).padStart(6, '0')}.zip`)); rmdirSync(join(blob, '..')) }
      if (last.code !== 0) code ||= last.code
      // Interruptions, timeouts and missing reports are partial results. Stop
      // rather than quietly executing the remainder or masking the failure.
      if ([124, 129, 130, 143].includes(last.code) || !existsSync(join(blobs, `${String(index).padStart(6, '0')}.zip`))) {
        if (!options.capture) { process.stdout.write(last.stdout); process.stderr.write(last.stderr) }
        return { ...last, code: last.code || 1 }
      }
    }
    const mergedEnv = { ...env }
    delete mergedEnv.PW_TEST_REPORTER
    delete mergedEnv.AEON_PW_PROGRESS_SOCKET
    delete mergedEnv.AEON_PW_CHUNK_EXPECTED
    delete mergedEnv.AEON_PW_CHUNK_PLAN
    const merged = await run(['merge-reports', blobs, '--config', mergeConfig], { capture: options.capture, env: mergedEnv, heartbeat: false })
    return { ...merged, code: code || merged.code, chunks: metrics, metrics: { ...merged.metrics,
      peak: Math.max(...metrics.map(row => row.peak)), wall_ms: Date.now() - began,
    } }
  } finally {
    for (const name of ['plan.json', 'list.json', 'merge.config.mjs', 'chunk.txt', 'expected.json']) {
      try { unlinkSync(join(scratch, name)) } catch (error) { if (error.code !== 'ENOENT') throw error }
    }
    rmdirSync(scratch)
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try { process.exitCode = (await runPlaywright(process.argv.slice(2))).code }
  catch (error) { console.error(error.message); process.exitCode = ({ SIGINT: 130, SIGTERM: 143, SIGHUP: 129 }[error.signal]) ?? 1 }
}
