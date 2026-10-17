// SPDX-License-Identifier: AGPL-3.0-only
import { createHash } from 'node:crypto'
import { browserIdentity } from './browser-impact.mjs'

const oid = /^[a-f0-9]{40}$/
const active = tests => tests.filter(row => row.kind === 'browser' && row.active !== false)
const catalogueHash = tests => createHash('sha256').update(JSON.stringify(active(tests)
  .map(row => [browserIdentity(row), row.tier]).sort(([a], [b]) => a.localeCompare(b)))).digest('hex')

export function makeBrowserPlan(tests, selected, { base, sha, areas }) {
  if (![base, sha].every(value => oid.test(value ?? '')) || !Array.isArray(areas) || !areas.length ||
      !Array.isArray(tests) || tests.length > 20_000 || !Array.isArray(selected) || selected.length > 20_000) throw new Error('unbound or oversized affected browser plan')
  return { version: 1, scope: 'affected', base, sha, areas, catalogue_sha256: catalogueHash(tests),
    cases: active(selected).map(browserIdentity).sort() }
}

// An immutable planner case list survives candidate import-edge deletions.
// Native catalogue drift, stale identities and missing/duplicate cases fail
// wide. A narrowed execution can never issue full-tier reuse proof.
export function browserPlanSelection(tests, text, { event, base, sha, impact, affectedLane }) {
  const full = reason => ({ full: true, reason, scope: 'gated-full' })
  if (impact !== 'on' || affectedLane !== 'on' || event !== 'pull_request') return full('affected browser execution disabled')
  try {
    if (!Array.isArray(tests) || tests.length > 20_000) throw new Error('native catalogue count bound')
    if (typeof text !== 'string' || Buffer.byteLength(text) >= 512 * 1024) throw new Error('missing or oversized plan')
    const plan = JSON.parse(text)
    if (plan.version !== 1 || plan.scope !== 'affected' || ![base, sha].every(value => oid.test(value ?? '')) ||
        plan.base !== base || plan.sha !== sha || plan.catalogue_sha256 !== catalogueHash(tests) ||
        !Array.isArray(plan.areas) || !plan.areas.length || plan.areas.length > 100 ||
        plan.areas.some(area => !/^[a-z][a-z0-9-]{0,63}$/.test(area)) ||
        !Array.isArray(plan.cases) || !plan.cases.length || plan.cases.length > 20_000 ||
        !plan.cases.every(value => typeof value === 'string') || new Set(plan.cases).size !== plan.cases.length) throw new Error('invalid plan identity or catalogue')
    const wanted = new Set(plan.cases), rows = active(tests), found = rows.filter(row => wanted.has(browserIdentity(row)))
    if (found.length !== wanted.size || rows.some(row => row.tier === 'ESSENTIAL' && !wanted.has(browserIdentity(row)))) throw new Error('missing native or essential case')
    return { full: false, scope: 'affected', browser_scope: 'affected', browser_areas: plan.areas,
      tests: found, reason: 'immutable base browser selection; native catalogue verified' }
  } catch (error) { return full(`affected browser plan rejected: ${error.message}`) }
}
