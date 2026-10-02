// SPDX-License-Identifier: AGPL-3.0-only
import { spawnSync } from 'node:child_process'
import { readFileSync } from 'node:fs'

const normalize = started => typeof started === 'string' ? started.trim().replaceAll(/\s+/g, ' ') : ''
export const validStart = started => /^\w+ \w+ \d+ \d\d:\d\d:\d\d \d{4}$/.test(normalize(started))

export function processTable() {
  // Numeric identities and executable names only; never args or environments.
  const result = spawnSync('ps', ['-axo', 'pid=,pgid=,lstart=,comm='], { encoding: 'utf8', env: { ...process.env, LC_ALL: 'C' } })
  if (result.status !== 0) throw new Error('Cannot inspect owned browser process identities')
  return result.stdout.trim().split('\n').flatMap(line => {
    const match = line.trim().match(/^(\d+)\s+(\d+)\s+(\w+\s+\w+\s+\d+\s+\d\d:\d\d:\d\d\s+\d+)\s+(.+)$/)
    return match ? [{ pid: Number(match[1]), group: Number(match[2]), started: normalize(match[3]), name: match[4] }] : []
  })
}

export function processStart(pid) {
  const result = spawnSync('ps', ['-p', String(pid), '-o', 'lstart='], { encoding: 'utf8', env: { ...process.env, LC_ALL: 'C' } })
  return result.status === 0 ? normalize(result.stdout) : ''
}

export function groupProcesses(pgid) { return processTable().filter(row => row.group === pgid) }

export function signalGroup(pgid, signal) {
  try { process.kill(-pgid, signal) } catch (error) { if (error.code !== 'ESRCH') throw error }
}

const pause = ms => Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ms)

// Only call for the process just spawned, or for this preload's own root.
// Root and detached launches must establish identity before starting work.
function* identityAttempts(child, append, { start = processStart, attempts = 20 } = {}) {
  if (!child.pid) return child
  const terminate = () => signalGroup(child.pid, 'SIGKILL')
  for (let attempt = 0; attempt < attempts; attempt++) {
    if (child.exitCode != null || child.signalCode != null) return child
    const started = start(child.pid)
    if (validStart(started)) {
      try { append({ pid: child.pid, started }); return child }
      catch (error) { terminate(); throw error }
    }
    try { process.kill(child.pid, 0) }
    catch (error) { if (error.code === 'ESRCH') return child; throw error }
    if (attempt + 1 < attempts) yield 10
  }
  terminate()
  throw new Error('Could not establish spawned process group start time; spawned group terminated')
}

export function recordSpawnedGroup(child, append, { wait = pause, ...options } = {}) {
  const attempts = identityAttempts(child, append, options)
  let step = attempts.next()
  while (!step.done) { wait(step.value); step = attempts.next() }
  return step.value
}

// Yield in the parent so Node can reap fast roots and update their exit code
// during retries. The preload and spawn hooks stay synchronous before work.
export async function recordRootGroup(child, append, { wait = ms => new Promise(resolve => setTimeout(resolve, ms)), ...options } = {}) {
  const attempts = identityAttempts(child, append, options)
  let step = attempts.next()
  while (!step.done) { await wait(step.value); step = attempts.next() }
  return step.value
}

// Read the full journal each time. A failed ps, malformed record or signal
// cannot consume a record and silently lose another owned group on retry.
export function trackGroups(log, { table = processTable, kill = signalGroup } = {}) {
  const witnesses = new Map()
  function snapshot() {
    const contents = readFileSync(log, 'utf8')
    const complete = contents.lastIndexOf('\n') + 1
    const groups = new Map(), issues = []
    for (const line of contents.slice(0, complete).split('\n').filter(Boolean)) {
      try {
        const entry = JSON.parse(line)
        if (!Number.isSafeInteger(entry.pid) || entry.pid <= 1 || !validStart(entry.started)) throw new Error('Invalid owned process group identity')
        const started = normalize(entry.started)
        if (groups.has(entry.pid) && groups.get(entry.pid) !== started) throw new Error('Conflicting owned process group identity')
        groups.set(entry.pid, started)
      } catch (error) { issues.push(error) }
    }
    if (complete !== contents.length) issues.push(new Error('Incomplete owned process group identity'))
    const rows = table(), owned = [], unknown = [], unverified = []
    for (const [pid, started] of groups) {
      const members = rows.filter(row => row.group === pid)
      if (!members.length) continue
      const leader = members.find(row => row.pid === pid)
      const previous = witnesses.get(pid) ?? []
      if (leader && leader.started !== started) {
        unknown.push(pid)
        continue
      }
      if (!leader && !members.some(row => previous.some(witness => row.pid === witness.pid && row.started === witness.started))) {
        unverified.push(pid)
        continue
      }
      witnesses.set(pid, members)
      owned.push({ pid, started, witnesses: members })
    }
    return { groups: owned, rows: rows.filter(row => owned.some(group => group.pid === row.group)), unknown, unverified, issues }
  }
  return {
    snapshot,
    signal(signal) {
      const state = snapshot(), errors = [...state.issues]
      for (const group of state.groups) {
        try {
          // Recheck each leader immediately before signalling; a previous
          // group's failure or identity change must not abort its siblings.
          const current = table().filter(row => row.group === group.pid)
          const leader = current.find(row => row.pid === group.pid)
          if (leader ? leader.started === group.started : current.some(row => group.witnesses.some(witness => row.pid === witness.pid && row.started === witness.started))) kill(group.pid, signal)
        } catch (error) { errors.push(error) }
      }
      return errors
    },
  }
}
