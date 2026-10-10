// SPDX-License-Identifier: AGPL-3.0-only
import { pathToFileURL } from 'node:url'
import { Worker, isMainThread, parentPort, workerData } from 'node:worker_threads'

if (!isMainThread) {
  await import(pathToFileURL(workerData).href)
  parentPort.postMessage(globalThis.__tierCases)
} else if (process.argv[2] === '--batch') {
  const files = []
  for (const file of process.argv.slice(3)) {
    // One worker at a time preserves each file's fresh module/global state and
    // inherited registration hook without starting a Node process per file.
    const tests = await new Promise((resolve, reject) => {
      const worker = new Worker(new URL(import.meta.url), { workerData: file })
      let cases
      worker.once('message', value => { cases = value })
      worker.once('error', reject)
      worker.once('exit', code => {
        if (code !== 0) reject(new Error(`Node registration worker failed (${code}): ${file}`))
        else if (!Array.isArray(cases)) reject(new Error(`Node registration worker returned no cases: ${file}`))
        else resolve(cases)
      })
    })
    files.push({ file, tests })
  }
  process.stdout.write(JSON.stringify(files) + '\n')
} else {
  await import(pathToFileURL(process.argv[2]).href)
  process.stdout.write(JSON.stringify(globalThis.__tierCases) + '\n')
}
