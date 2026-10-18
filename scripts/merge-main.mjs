// SPDX-License-Identifier: AGPL-3.0-only
import { spawnSync } from 'node:child_process'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { registryPaths, parseRegistry, readRegistry, formatRegistry, atomicWrite } from './merge-drivers/registries.mjs'
import { manifestPaths, formatManifest } from './test-tiers/manifests.mjs'

const sourceRoot = fileURLToPath(new URL('../', import.meta.url))
// Policy is authored data. Regeneration only restores deterministic layout;
// it never discovers permissions, grants, privacy classes, tiers or weights.
export function regenerate(root) {
  const outputs = registryPaths.map(file => [resolve(root, file), formatRegistry(parseRegistry(readRegistry(resolve(root, file))), file)])
  outputs.push(...manifestPaths.map(file => [resolve(root, file), formatManifest(parseRegistry(readRegistry(resolve(root, file))), file, { rejectDuplicates: true })]))
  // All documents must validate before any replacement takes place.
  for (const [path, source] of outputs) atomicWrite(path, source)
}

export function run(command, args, root, timeout) {
  const result = spawnSync(command, args, { cwd: root, encoding: 'utf8', timeout, maxBuffer: 16 * 1024 * 1024 })
  if (result.error || result.signal) throw new Error(`${command} failed: ${result.error?.message ?? result.signal}`)
  return result
}

export function main(args, { root = sourceRoot, execute = run, log = console.log } = {}) {
  try {
    if (args.length !== 1 || args[0] !== '--regenerate') throw new Error('Usage: node scripts/merge-main.mjs --regenerate (after git merge origin/main)')
    const conflicts = execute('git', ['ls-files', '--unmerged', '-z'], root, 30_000)
    if (conflicts.status !== 0) throw new Error('Cannot inspect the merge index')
    if (conflicts.stdout) { log('Unresolved index conflicts remain; resolve them before regeneration.'); return 1 }
    const index = execute('git', ['diff', '--check', '--cached'], root, 30_000)
    const tree = execute('git', ['diff', '--check'], root, 30_000)
    if (index.status !== 0 || tree.status !== 0) { log(index.stdout + index.stderr + tree.stdout + tree.stderr); return 1 }
    regenerate(root)
    // Check the actual working tree, including unstaged regenerated files.
    // --merge-main would check committed HEAD and miss the merge being prepared.
    const tests = execute(process.execPath, ['--test', 'scripts/merge-drivers/merge-drivers.test.mjs'], root, 120_000)
    if (tests.status !== 0) { log(tests.stdout + tests.stderr); return tests.status ?? 2 }
    const checked = execute(process.execPath, ['scripts/ci-static.mjs', '--here', '--json'], root, 15 * 60_000)
    log(checked.stdout + checked.stderr)
    if (checked.status !== 0) return checked.status ?? 2
    log('Registry layout regenerated; working-tree static checks passed. Ready to commit; nothing staged or committed by this command.')
    return 0
  } catch (error) { log(error.message); return 2 }
}
if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) process.exitCode = main(process.argv.slice(2))
