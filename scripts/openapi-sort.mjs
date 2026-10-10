// SPDX-License-Identifier: AGPL-3.0-only
// Move complete source blocks; never re-serialize descriptions, examples or schemas.
import { readFileSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { generateOpenAPI } from '../api/generate.mjs'
import { sortOpenAPI } from '../api/openapi-source.mjs'
export { sortOpenAPI } from '../api/openapi-source.mjs'

const contract = fileURLToPath(new URL('../api/openapi.yaml', import.meta.url))
export function main(args, path = contract, log = console.log) {
  if (args.length !== 1 || !['--check', '--write'].includes(args[0])) throw new Error('Usage: node scripts/openapi-sort.mjs --check|--write')
  const source = path === contract ? generateOpenAPI() : readFileSync(path, 'utf8'), sorted = sortOpenAPI(source)
  if (source === sorted) { log('OpenAPI paths and components are sorted'); return 0 }
  if (args[0] === '--check') { log('OpenAPI is unsorted; run node scripts/openapi-sort.mjs --write'); return 1 }
  writeFileSync(path, sorted)
  log('Sorted OpenAPI paths and components')
  return 0
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try { process.exitCode = main(process.argv.slice(2)) }
  catch (error) { console.error(error.message); process.exitCode = 2 }
}
