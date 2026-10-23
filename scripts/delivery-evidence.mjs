// SPDX-License-Identifier: AGPL-3.0-only
// Measurement publisher for trusted coordinator tooling. It runs no review,
// merge, deployment or model, and inherits the selected CLI's existing auth.
import { spawnSync } from 'node:child_process'
import { closeSync, constants, fstatSync, openSync, readSync } from 'node:fs'
import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

const repositoryOK = value => typeof value === 'string' && value.length <= 200 && /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(value)
const uuid = /^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$/i
const kinds = new Set(['review', 'merge_round', 'release', 'review_audit', 'escaped_defect'])
export const maxFactBytes = 64 << 10

export function publishFacts({ project, repository, facts, instance, aeon }, { execute = spawnSync } = {}) {
  if (!uuid.test(project ?? '') || !repositoryOK(repository) || typeof instance !== 'string' || !/^[A-Za-z0-9_-]{1,64}$/.test(instance) ||
      typeof aeon !== 'string' || !aeon || !Array.isArray(facts) || facts.length < 1 || facts.length > 100) throw new Error('delivery_evidence_binding_invalid')
  const seen = new Set()
  const fields = new Set(['kind', 'key', 'pull_request', 'head_sha', 'started_at', 'at', 'outcome', 'release', 'tag', 'healthy_at'])
  for (const fact of facts) {
    const key = `${fact?.kind}\0${fact?.key}`
    if (!kinds.has(fact?.kind) || typeof fact.key !== 'string' || !/^[A-Za-z0-9._:/+-]{1,128}$/.test(fact.key) || seen.has(key)) throw new Error('delivery_evidence_facts_invalid')
    if (Object.keys(fact).some(field => !fields.has(field) || typeof fact[field] === 'object' && fact[field] !== null ||
        typeof fact[field] === 'string' && fact[field].length > 256) || typeof fact.outcome !== 'string') throw new Error('delivery_evidence_facts_invalid')
    seen.add(key)
  }
  const body = JSON.stringify({ repository, facts })
  if (Buffer.byteLength(body) > maxFactBytes) throw new Error('delivery_evidence_oversized')
  // No shell, credentials in arguments, response-body diagnostics or retries
  // that hide a failed/partial write. An unchanged replay uses the same keys.
  const result = execute(aeon, ['--instance', instance, 'curl', `/api/projects/${project}/delivery/metrics/facts`, '-X', 'POST', '--data', body],
    { encoding: 'utf8', timeout: 15_000, maxBuffer: maxFactBytes, stdio: ['ignore', 'pipe', 'pipe'] })
  if (result.error || result.signal || result.status !== 0) throw new Error('delivery_evidence_publish_failed')
  let stored
  try { stored = JSON.parse(result.stdout).stored } catch { throw new Error('delivery_evidence_receipt_invalid') }
  if (stored !== facts.length) throw new Error('delivery_evidence_receipt_partial')
  return { status: 'evidence_recorded', stored }
}

export function evidenceMain(args, dependencies) {
  const options = {}
  for (let i = 0; i < args.length; i += 2) {
    const flag = args[i], value = args[i + 1]
    if (!['--project', '--repository', '--instance', '--aeon', '--input'].includes(flag) || options[flag.slice(2)] || !value) throw new Error('delivery_evidence_arguments_invalid')
    options[flag.slice(2)] = value
  }
  if (!options.input) throw new Error('delivery_evidence_input_invalid')
  const fd = openSync(options.input, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK)
  let input
  try {
    const info = fstatSync(fd)
    if (!info.isFile() || info.size > maxFactBytes) throw new Error('delivery_evidence_input_invalid')
    const raw = Buffer.alloc(maxFactBytes + 1)
    let size = 0, count
    while (size < raw.length && (count = readSync(fd, raw, size, raw.length - size, null))) size += count
    if (size > maxFactBytes) throw new Error('delivery_evidence_oversized')
    input = JSON.parse(raw.subarray(0, size).toString('utf8'))
  } finally { closeSync(fd) }
  if (input.repository !== undefined && input.repository !== options.repository) throw new Error('delivery_evidence_binding_invalid')
  return publishFacts({ ...options, facts: input.facts }, dependencies)
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  try { console.log(JSON.stringify(evidenceMain(process.argv.slice(2)))) }
  catch { console.error('Delivery evidence was not confirmed; retain the input and retry with the same keys and captured repository.'); process.exitCode = 1 }
}
