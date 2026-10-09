// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1061: public product sources carry neutral examples or tenant data.
import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { lstatSync, readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const roots = ['web/src', 'web/tests', 'internal', 'cmd', 'api', 'docs', 'tests']
const history = 'internal/releasehistory/data/product-notes.json'
const privateNames = /agm[0-9]+|csb[0-9]+|hsb[0-9]+|mbp26[0-9]{2}|augmentoring/gi
const digest = bytes => createHash('sha256').update(bytes).digest('hex')

export function checkPublicNames(root) {
  const allowlist = JSON.parse(readFileSync(resolve(root, 'scripts/ci/public-names-allowlist.json'), 'utf8'))
  if (!Array.isArray(allowlist) || allowlist.length > 1 || allowlist.some(entry =>
    entry.path !== history || !/^[a-f0-9]{64}$/.test(entry.sha256 ?? '') || !entry.reason?.trim())) {
    throw new Error('Only digest-pinned published release-note history may be allowlisted')
  }
  const paths = [...new Set(execFileSync('git', ['ls-files', '-z', '--cached', '--others', '--exclude-standard', '--', ...roots], {
    cwd: root, encoding: 'utf8', timeout: 30_000, maxBuffer: 32 << 20,
  }).split('\0').filter(Boolean))].sort()
  const failures = []
  for (const path of paths) {
    const full = resolve(root, path)
    let stat
    try { stat = lstatSync(full) } catch (error) { if (error.code === 'ENOENT') continue; throw error }
    if (!stat.isFile() || stat.size > (32 << 20)) throw new Error(`Cannot safely scan public source: ${path}`)
    const bytes = readFileSync(full)
    const allowed = allowlist.find(entry => entry.path === path)
    if (allowed) {
      if (digest(bytes) !== allowed.sha256) throw new Error(`Published history changed; review and refresh its allowlist digest: ${path}`)
      continue
    }
    for (const [index, line] of bytes.toString('utf8').split('\n').entries()) {
      privateNames.lastIndex = 0
      if (privateNames.test(line)) failures.push(`${path}:${index + 1}: operator host or business name`)
    }
  }
  return failures
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const failures = checkPublicNames(process.cwd())
    if (failures.length) {
      console.error(failures.join('\n'))
      process.exitCode = 1
    } else console.log('Public names: neutral; published history only is allowlisted')
  } catch (error) { console.error(error.message); process.exitCode = 1 }
}
