// SPDX-License-Identifier: AGPL-3.0-only
import { createHash } from 'node:crypto'
import { boundedText } from './inputs.mjs'

const proofConfidence = new Set(['source-supported', 'bisected', 'ticket-confirmed', 'fix-diff-confirmed', 'recorded-failure'])
const rejectedConfidence = new Set(['unresolved', 'inherited-source-attribution'])
const sha = /^[a-f0-9]{40}$/
const prefix = /^web\/src\/(?:[a-zA-Z0-9_-]+\/)+$/
const cache = new Map()
export const hasBrowserSource = paths => Array.isArray(paths) && paths.some(file => file.startsWith('web/src/'))

// Use the recorded S1 result on historical trees. A full fallback with no miss
// is useful safety evidence, but does not qualify an area for narrowing.
export function browserAreaEvidence(area, rows) {
  const result = { cases: 0, affected: 0, rejected: 0, misses: [], complete: true }
  if (!Array.isArray(rows) || !rows.length || rows.length > 20_000) return { ...result, complete: false }
  const seen = new Set()
  for (const row of rows) {
    if (row.observed?.kind !== 'browser' || !row.causingChange?.paths?.some(file => area.prefixes.some(prefix => file.startsWith(prefix)))) continue
    if (rejectedConfidence.has(row.causalConfidence)) { result.rejected++; continue }
    if (!proofConfidence.has(row.causalConfidence) || typeof row.id !== 'string' || seen.has(row.id) ||
        !sha.test(row.treeBeforeFix ?? '') || row.observed.testedSHA !== row.treeBeforeFix ||
        !sha.test(row.causingChange.base ?? '') || row.replay?.trustedGraphSHA !== row.causingChange.base ||
        !['affected', 'gated-full', 'none'].includes(row.replay?.s1?.scope) || typeof row.replay.s1.selected !== 'boolean') {
      result.complete = false
      continue
    }
    seen.add(row.id)
    result.cases++
    if (!row.replay.s1.selected) result.misses.push(row.id)
    else if (row.replay.s1.scope === 'affected') result.affected++
  }
  result.misses.sort()
  return result
}

// Both files come from the immutable planner capsule. Pinning the corpus
// prevents an edited or partial replay from keeping an old qualification.
export function loadBrowserAreas(checkout, { read = boundedText } = {}) {
  if (read === boundedText && cache.has(checkout)) return cache.get(checkout)
  let result
  try {
    const policy = JSON.parse(read(checkout, 'scripts/ci/browser-area-rollout.json', 1024 * 1024))
    const corpus = read(checkout, 'scripts/test-tiers/affected-replay.json', 16 * 1024 * 1024)
    if (policy?.version !== 1 || policy.selector !== 'impact-ci-s1' || typeof corpus !== 'string' ||
        policy.corpus_sha256 !== createHash('sha256').update(corpus).digest('hex') ||
        !Array.isArray(policy.areas) || !policy.areas.length || policy.areas.length > 100) throw new Error('invalid or stale area qualification')
    const names = new Set(), prefixes = []
    for (const area of policy.areas) {
      if (!/^[a-z][a-z0-9-]{0,63}$/.test(area.id ?? '') || names.has(area.id) ||
          !['affected', 'gated-full'].includes(area.mode) || !Array.isArray(area.prefixes) || !area.prefixes.length || area.prefixes.length > 100 ||
          area.prefixes.some(value => typeof value !== 'string' || value.length > 1024 || !prefix.test(value))) throw new Error('invalid area ownership')
      names.add(area.id)
      for (const value of area.prefixes) {
        if (prefixes.some(other => value.startsWith(other) || other.startsWith(value))) throw new Error('overlapping area ownership')
        prefixes.push(value)
      }
    }
    const rows = JSON.parse(corpus).historicalRegressions
    result = { complete: true, corpus_sha256: policy.corpus_sha256,
      areas: policy.areas.map(area => ({ ...area, evidence: browserAreaEvidence(area, rows) })) }
  } catch { result = { complete: false, reason: 'browser area qualification unavailable, invalid or stale', areas: [] } }
  if (read === boundedText) cache.set(checkout, result)
  return result
}

export function browserAreaDecision(paths, policy) {
  const full = reason => ({ full: true, areas: [], reason })
  if (policy?.complete !== true) return full(policy?.reason ?? 'browser area qualification unavailable')
  if (paths.some(file => !/^web\/(?:src|tests|e2e)\/.+\.(?:ts|js|mjs|vue|html)$/.test(file) &&
      !/^(?:docs\/|README(?:\.|$)|LICENSE(?:\.|$)|CHANGELOG(?:\.|$))/.test(file) &&
      !['scripts/ci/web-test-tiers.json', 'web/ci-web-shards.json'].includes(file) &&
      !/^web\/scripts\/ci-web-shard-specs\/.+\.spec\.ts\.json$/.test(file))) return full('shared or unknown input mixed with browser area change')
  const areas = new Set()
  for (const file of paths.filter(file => file.startsWith('web/src/'))) {
    const owners = policy.areas.filter(area => area.prefixes.some(prefix => file.startsWith(prefix)))
    if (owners.length !== 1) return full(`unqualified or ambiguous browser area: ${file}`)
    const area = owners[0], evidence = area.evidence
    // A confirmed miss closes the gate even if somebody leaves mode=affected.
    if (evidence?.misses?.length) return full(`browser area ${area.id}: ${evidence.misses.length} historical shadow misses; gated-full required`)
    if (area.mode !== 'affected' || evidence?.complete !== true || !(evidence.cases > 0) || !(evidence.affected > 0))
      return full(`browser area ${area.id}: zero-miss affected shadow evidence not qualified or area rolled back`)
    areas.add(area.id)
  }
  return { full: false, areas: [...areas].sort(), reason: 'qualified browser areas' }
}
