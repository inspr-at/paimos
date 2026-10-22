// SPDX-License-Identifier: AGPL-3.0-only
// AEON-933: a hung apt or Playwright system-dependency install must fail in
// minutes, and the deadline must run as root so it can reap apt-get.
import { readdirSync, readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const directory = join(dirname(fileURLToPath(import.meta.url)), '../.github/workflows')
const script = 'bash "$GITHUB_WORKSPACE/scripts/ci/bounded-apt.sh"'
const aptCall = `${script} apt`
const playwrightCall = `${script} playwright `

export function stepsOf(text) {
  const lines = text.split('\n')
  const steps = []
  let start = -1
  const close = end => {
    if (start >= 0) steps.push({ start, text: lines.slice(start, end).join('\n') })
    start = -1
  }
  for (let i = 0; i < lines.length; i++) {
    if (/^      - /.test(lines[i])) {
      close(i)
      start = i
      continue
    }
    if (start >= 0 && lines[i].trim() !== '' && !/^ {6}/.test(lines[i])) close(i)
  }
  close(lines.length)
  return { lines, steps }
}

// AEON-1652: only this pinned artifact download may fail without failing a
// shard. Validate every field before historical execution guards exclude it;
// a renamed test command or another continue-on-error must still be rejected.
export function withoutOptionalAptDownload(text) {
  const found = stepsOf(text).steps.filter(step => /^      - name: Download optional apt bundle$/m.test(step.text))
  if (found.length !== 1) throw new Error('expected exactly one optional apt download')
  const step = found[0].text
  const normalized = step.split('\n').filter(line => line.trim() && !line.trim().startsWith('#')).join('\n')
    .replace("runner.os == 'Linux' && ", '')
    .replace(/actions\/download-artifact@[a-f0-9]{40} # v[0-9.]+/, 'actions/download-artifact@PINNED')
  const expected = `      - name: Download optional apt bundle
        if: steps.apt-image.outputs.artifact != ''
        timeout-minutes: 2
        continue-on-error: true
        uses: actions/download-artifact@PINNED
        with:
          name: \${{ steps.apt-image.outputs.artifact }}
          path: \${{ runner.temp }}/apt-bundle`
  if (normalized !== expected) throw new Error('optional apt download cannot suppress test execution')
  return text.replace(step, '')
}

function minutes(step) {
  const found = step.match(/^        timeout-minutes: (\d+)\s*$/m)
  return found ? Number(found[1]) : null
}

function rawInstall(text) {
  return text.split('\n').some(line =>
    /apt-get\b|playwright install-deps\b|playwright install --with-deps\b/.test(line) &&
    !line.includes('scripts/ci/bounded-apt.sh'))
}

function calls(step) {
  return [...step.matchAll(/bash "\$GITHUB_WORKSPACE\/scripts\/ci\/bounded-apt\.sh" [^\n]+/g)].map(match => match[0])
}

export function auditWorkflowText(name, text) {
  const failures = []
  const { lines, steps } = stepsOf(text)
  const covered = new Set()
  for (const step of steps) for (let i = 0; i < step.text.split('\n').length; i++) covered.add(step.start + i)
  lines.forEach((line, index) => {
    if (rawInstall(line) && !covered.has(index)) {
      failures.push(`${name}:${index + 1}: apt or Playwright dependency install is outside a step`)
    }
  })
  for (const step of steps) {
    const raw = rawInstall(step.text)
    const delegated = step.text.includes('scripts/ci/bounded-apt.sh')
    if (!raw && !delegated) continue
    const label = (step.text.match(/^(?:      - |        )name: (.+)$/m) || [])[1] || '(unnamed)'
    const where = `${name}: ${label}`
    if (raw) failures.push(`${where}: apt or Playwright dependency install must go through ${script}`)
    const found = calls(step.text)
    if (delegated && found.length === 0) failures.push(`${where}: bounded apt invocation is not a single command`)
    const limit = minutes(step.text)
    for (const call of found) {
      if (call.includes(` ${aptCall.split(' ').at(-1)} `) && call.startsWith(aptCall)) {
        if (limit !== 4) failures.push(`${where}: apt step needs timeout-minutes: 4`)
        if (!/\bapt (?:fish zsh|zsh fish)$/.test(call)) failures.push(`${where}: apt step must install fish and zsh`)
      } else if (call.startsWith(playwrightCall)) {
        if (limit === null || limit < 13 || limit > 18) failures.push(`${where}: Playwright dependency install needs timeout-minutes between 13 and 18`)
        const args = call.slice(playwrightCall.length)
        const allowed = ['install-deps chromium', 'install --with-deps chromium', 'install --with-deps --only-shell chromium']
        if (!allowed.includes(args)) failures.push(`${where}: unsupported Playwright dependency install: ${args}`)
      } else if (call === `${script} bundle "$RUNNER_TEMP/apt-bundle"`) {
        if (limit !== 22) failures.push(`${where}: bundle step needs timeout-minutes: 22`)
      } else failures.push(`${where}: unsupported bounded apt invocation`)
    }
  }
  return failures
}

export function auditWorkflows(dir = directory) {
  const failures = []
  for (const name of readdirSync(dir).filter(file => file.endsWith('.yml') || file.endsWith('.yaml')).sort()) {
    failures.push(...auditWorkflowText(name, readFileSync(join(dir, name), 'utf8')))
  }
  return failures
}

if (process.argv[1] && pathToFileURL(process.argv[1]).href === import.meta.url) {
  const failures = auditWorkflows()
  if (failures.length) {
    console.error(failures.join('\n'))
    process.exit(1)
  }
  console.log('apt and Playwright dependency installs are bounded')
}
