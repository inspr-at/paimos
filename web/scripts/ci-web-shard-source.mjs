// SPDX-License-Identifier: AGPL-3.0-only
// Per-spec source records merge on GitHub without any custom merge driver.
// The runtime policy is assembled in memory; no generated layout is committed.
import { readdirSync, lstatSync } from 'node:fs'
import { dirname, resolve, relative } from 'node:path'
import { inputMetadata, readInput } from '../../scripts/test-tiers/inputs.mjs'

export const specSource = 'scripts/ci-web-shard-specs'
const stable = value => Array.isArray(value) ? value.map(stable) : value && typeof value === 'object'
  ? Object.fromEntries(Object.keys(value).sort().map(key => [key, stable(value[key])])) : value
export const formatSpecSource = value => JSON.stringify(stable(value), null, 2) + '\n'

export function policySources(path) {
  const root = dirname(path)
  const policy = JSON.parse(readInput(inputMetadata(root, relative(root, path), 4 * 1024 * 1024), 4 * 1024 * 1024))
  if (policy.specSource === undefined) return { policy, records: [] } // Historical/fixture policies.
  if (policy.specSource !== specSource || !Array.isArray(policy.groups) || policy.groups.some(group => !Array.isArray(group.specs) || group.specs.length))
    throw new Error('Expected per-spec sources and empty policy spec lists')
  const files = []
  let entries = 0, bytes = 0
  const visit = directory => {
    const stat = lstatSync(resolve(root, directory))
    if (!stat.isDirectory() || stat.isSymbolicLink()) throw new Error('Invalid spec source directory')
    for (const entry of readdirSync(resolve(root, directory), { withFileTypes: true })) {
      if (++entries > 20_000) throw new Error('Spec source entry bound reached')
      const file = `${directory}/${entry.name}`
      if (entry.isSymbolicLink()) throw new Error('Symlink in spec sources')
      if (entry.isDirectory()) visit(file)
      else {
        if (!entry.isFile() || !file.endsWith('.spec.ts.json')) throw new Error(`Invalid spec source: ${file}`)
        const metadata = inputMetadata(root, file, 64 * 1024)
        bytes += metadata.size
        if (files.length >= 10_000 || bytes > 8 * 1024 * 1024) throw new Error('Spec source size bound reached')
        files.push({ file, metadata })
      }
    }
  }
  visit(specSource)
  // Bound all metadata before the first record read.
  const records = files.sort((a, b) => a.file.localeCompare(b.file)).map(({ file, metadata }) => {
    const source = readInput(metadata, 64 * 1024), value = JSON.parse(source)
    if (!/^(?:tests|e2e)\/[a-zA-Z0-9_/-]+\.spec\.ts$/.test(value.spec?.file ?? '') ||
        file !== `${specSource}/${value.spec.file}.json` || typeof value.group !== 'string' ||
        !Number.isFinite(value.spec.weightSeconds) || value.spec.weightSeconds <= 0 ||
        Object.keys(value).some(key => !['group', 'spec', 'nightlyTiming'].includes(key))) throw new Error(`Invalid spec source identity: ${file}`)
    return { file: resolve(root, file), value, source }
  })
  return { policy, records }
}

export function loadPolicy(path) {
  const { policy, records } = policySources(path)
  if (policy.specSource === undefined) return policy
  const groups = new Map(policy.groups.map(group => [group.id, group]))
  if (groups.size !== policy.groups.length) throw new Error('Duplicate policy group')
  if (Object.keys(policy.nightlyTiming?.files ?? {}).length) throw new Error('Nightly file timings belong in per-spec sources')
  for (const { value: { group, spec, nightlyTiming } } of records) {
    if (!groups.has(group)) throw new Error(`Unknown policy group: ${group}`)
    groups.get(group).specs.push(spec)
    if (nightlyTiming !== undefined) {
      policy.nightlyTiming ??= { files: {} }
      policy.nightlyTiming.files[spec.file] = nightlyTiming
    }
  }
  for (const group of groups.values()) if (!group.specs.length) throw new Error(`Missing spec sources for group: ${group.id}`)
  // Source metadata is not part of the assembled execution policy.
  delete policy.specSource
  return policy
}
