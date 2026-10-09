// SPDX-License-Identifier: AGPL-3.0-only
import { key } from './core.mjs'

// These manifest-owned values locate output artifacts only. Unknown environment
// keys are execution policy, including absence versus presence, and must match.
const artifactVariables = new Set(['AEON_SCREENSHOTS_DIR', 'RELEASE_LIST_SHOTS', 'ATTACH_DISCOVERABLE_SHOTS',
  'ATTACH_SHOTS', 'AEON_RECURRENCE_SHOTS', 'AEON_DESK_SHOTS', 'AEON_521A_SHOTS', 'AGENT_ACTIVITY_SHOTS', 'STATUS_AUTOPILOT_SHOTS'])

function launchFlags(flags) {
  const kept = []
  let trace = false
  for (let index = 0; index < flags.length; index++) {
    const flag = flags[index]
    // The tier runner has always overridden every group's worker count to 1.
    if (flag === '--workers' || flag === '-j') { index++; continue }
    if (flag.startsWith('--workers=') || /^-j=?\d/.test(flag)) continue
    if (flag === '--trace=retain-on-failure') { trace = true; continue }
    kept.push(flag)
  }
  return { flags: kept, trace }
}

// Selection has already happened. Only coalesce compatible launch policies:
// same config/project/flags, no conflicting environment values, the same host
// restriction, and the same trace retention. Screenshot variables are a union.
// Trace stays with the groups that request it. Widening retain-on-failure onto
// a neighbour made the routed panel stability case exceed its 120s budget
// (shard 6, run 37923530553, 17 of 24 viewports).
export function browserBatches(rows, policy) {
  const batches = [], assigned = new Set()
  for (const group of policy.groups) {
    const files = new Set(group.specs.map(spec => spec.file))
    const selected = rows.filter(row => row.kind === 'browser' && row.active !== false && files.has(row.file))
    if (!selected.length) continue
    for (const row of selected) {
      if (assigned.has(key(row))) throw new Error(`Duplicate browser policy owner: ${key(row)}`)
      if (row.config !== undefined && row.config !== group.config || group.project && row.project !== group.project)
        throw new Error(`Browser launch policy mismatch: ${key(row)}`)
      assigned.add(key(row))
    }
    const launch = launchFlags(group.flags)
    const executionEnv = Object.entries(group.env).filter(([name]) => !artifactVariables.has(name))
      .sort(([left], [right]) => left < right ? -1 : left > right ? 1 : 0)
    const signature = JSON.stringify([group.config, group.project ?? null, group.hostedOnly ?? null, launch.flags, executionEnv, launch.trace])
    let batch = batches.find(candidate => candidate.signature === signature &&
      Object.entries(group.env).every(([name, value]) => !Object.hasOwn(candidate.env, name) || candidate.env[name] === value))
    if (!batch) {
      batch = { id: group.id, config: group.config, project: group.project, env: {}, flags: launch.flags,
        groups: [], rows: [], signature, trace: false }
      batches.push(batch)
    }
    batch.groups.push(group.id)
    batch.rows.push(...selected)
    Object.assign(batch.env, group.env)
    batch.trace ||= launch.trace
  }
  for (const row of rows.filter(row => row.kind === 'browser' && row.active !== false))
    if (!assigned.has(key(row))) throw new Error(`Missing browser policy owner: ${key(row)}`)
  return batches.map(({ signature, trace, ...batch }) => ({ ...batch,
    id: batch.groups.length > 1 ? `${batch.id}-combined` : batch.id,
    flags: [...batch.flags, ...(trace ? ['--trace=retain-on-failure'] : [])] }))
}

// Sum native attempt durations, including retries. An incomplete report must
// not invent an overhead measurement; the runner's separate result checks
// still determine whether execution was valid.
export function browserCaseSeconds(report) {
  let milliseconds = 0, complete = true
  const visit = suites => {
    for (const suite of suites ?? []) {
      for (const spec of suite.specs ?? []) for (const test of spec.tests ?? []) for (const result of test.results ?? []) {
        if (!Number.isFinite(result.duration) || result.duration < 0) complete = false
        else milliseconds += result.duration
      }
      visit(suite.suites)
    }
  }
  visit(report.suites)
  return complete ? milliseconds / 1000 : null
}
