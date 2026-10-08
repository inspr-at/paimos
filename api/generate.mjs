// SPDX-License-Identifier: AGPL-3.0-only
// The bundle is local build output. Only area sources belong in git.
import { readFileSync, readdirSync, lstatSync, writeFileSync, renameSync } from 'node:fs'
import { resolve, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import { randomUUID } from 'node:crypto'
import { splitOpenAPI, sortOpenAPI } from './openapi-source.mjs'
import { inputMetadata, readInput } from '../scripts/test-tiers/inputs.mjs'

export const apiRoot = dirname(fileURLToPath(import.meta.url))
const limit = 16 * 1024 * 1024
const compare = (a, b) => a < b ? -1 : a > b ? 1 : 0
const withoutLicense = text => text.replace(/^# SPDX-License-Identifier: AGPL-3\.0-only\n/, '')

export function bundleOpenAPI(directory = apiRoot) {
  let bytes = 0
  const read = path => {
    const stat = lstatSync(path)
    if (!stat.isFile() || stat.isSymbolicLink() || stat.size > limit || (bytes += stat.size) > limit)
      throw new Error('OpenAPI source must be a regular file within the 16 MiB bundle bound')
    return readInput(inputMetadata(directory, path, limit), limit)
  }
  const fragmentRoot = resolve(directory, 'areas')
  const stat = lstatSync(fragmentRoot)
  if (!stat.isDirectory() || stat.isSymbolicLink()) throw new Error('Expected regular OpenAPI areas directory')
  const files = readdirSync(fragmentRoot)
  if (!files.length || files.length > 256 || files.some(file => !/^[a-z0-9]+(?:-[a-z0-9]+)*\.yaml$/.test(file)))
    throw new Error('Expected 1..256 named YAML area fragments')
  const base = splitOpenAPI(withoutLicense(read(resolve(directory, 'openapi.base.yaml'))))
  if (base.paths.size || [...base.components.values()].some(map => map.size)) throw new Error('OpenAPI base must contain only metadata and empty section headings')
  const owners = new Map()
  const add = (target, incoming, section, file) => {
    for (const [key, block] of incoming) {
      const identity = `${section}:${key}`
      if (target.has(key)) throw new Error(`Duplicate ${identity} in ${owners.get(identity)} and ${file}`)
      if (section === 'paths' && !key.startsWith('/')) throw new Error(`Invalid OpenAPI path: ${key}`)
      owners.set(identity, file)
      target.set(key, block)
    }
  }
  for (const file of files.sort(compare)) {
    const fragment = splitOpenAPI(read(resolve(fragmentRoot, file)))
    if (fragment.header.split('\n').some(line => line.trim() && !line.trimStart().startsWith('#')))
      throw new Error(`Fragment metadata belongs in openapi.base.yaml: ${file}`)
    add(base.paths, fragment.paths, 'paths', file)
    for (const [section, entries] of fragment.components) {
      if (!base.components.has(section)) throw new Error(`Unknown component section ${section} in ${file}`)
      add(base.components.get(section), entries, `components.${section}`, file)
    }
  }
  const ordered = map => [...map].sort(([a], [b]) => compare(a, b)).map(([, block]) => block).join('')
  const source = base.header + 'paths:\n' + base.pathPrefix + ordered(base.paths) + 'components:\n' + base.componentPrefix +
    [...base.components].map(([section, entries]) => base.componentHeaders.get(section) + ordered(entries)).join('')
  return sortOpenAPI(source)
}

export function generateOpenAPI(directory = apiRoot) {
  const source = bundleOpenAPI(directory), path = resolve(directory, 'openapi.yaml')
  try {
    const stat = lstatSync(path)
    if (!stat.isFile() || stat.isSymbolicLink()) throw new Error('Bundle output must be a regular file')
    if (stat.size <= limit && readFileSync(path, 'utf8') === source) return source
  } catch (error) { if (error.code !== 'ENOENT') throw error }
  // Concurrent test runners can prepare the same checkout without partial reads.
  const temporary = resolve(directory, `.openapi-${randomUUID()}.tmp`)
  writeFileSync(temporary, source, { flag: 'wx', mode: 0o644 })
  renameSync(temporary, path)
  return source
}

export function main(args) {
  if (args.length !== 1 || !['--write', '--check', '--stdout'].includes(args[0])) throw new Error('Usage: node api/generate.mjs --write|--check|--stdout')
  if (args[0] === '--stdout') { process.stdout.write(bundleOpenAPI()); return 0 }
  if (args[0] === '--write') { generateOpenAPI(); console.log('Generated api/openapi.yaml'); return 0 }
  const expected = bundleOpenAPI(), actual = readFileSync(resolve(apiRoot, 'openapi.yaml'), 'utf8')
  if (actual !== expected) { console.error('OpenAPI bundle drift; run node api/generate.mjs --write'); return 1 }
  console.log('OpenAPI bundle matches its area sources')
  return 0
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try { process.exitCode = main(process.argv.slice(2)) }
  catch (error) { console.error(error.message); process.exitCode = 2 }
}
