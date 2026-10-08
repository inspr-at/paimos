// SPDX-License-Identifier: AGPL-3.0-only
// AEON-837: a tracked .pyc or __pycache__ entry dirties every worktree that runs tests.
import { execFileSync } from 'node:child_process'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

export function trackedBytecode(paths) {
  return paths.filter(path => path.endsWith('.pyc') || path.split('/').includes('__pycache__'))
}

export function listTracked(root) {
  let out
  try {
    out = execFileSync('git', ['--no-optional-locks', '-C', root, 'ls-files', '-z'], {
      encoding: 'utf8',
      maxBuffer: 32 * 1024 * 1024,
    })
  } catch (error) {
    const detail = error instanceof Error ? error.message : 'git ls-files failed'
    throw new Error(`cannot list tracked paths: ${detail}`)
  }
  return out.split('\0').filter(Boolean)
}

function main() {
  const root = resolve(fileURLToPath(new URL('..', import.meta.url)))
  const hits = trackedBytecode(listTracked(root))
  if (hits.length) {
    console.error(`Tracked Python bytecode is forbidden:\n${hits.join('\n')}`)
    process.exitCode = 1
    return
  }
  console.log('Tracked bytecode: none')
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main()
