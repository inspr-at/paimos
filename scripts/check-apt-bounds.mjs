// SPDX-License-Identifier: AGPL-3.0-only
// AEON-933: a hung apt or Playwright system-dependency install must fail in
// minutes. Every such step carries a step timeout and a bounded retry.
import { readdirSync, readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const directory = join(dirname(fileURLToPath(import.meta.url)), '../.github/workflows')
const aptBound = 'timeout 150 sudo apt-get -o Acquire::Retries=3 -o Acquire::http::Timeout=20 -o Acquire::https::Timeout=20 '
const failures = []

function stepsOf(text) {
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

function minutes(step) {
  const found = step.match(/^        timeout-minutes: (\d+)\s*$/m)
  return found ? Number(found[1]) : null
}

for (const name of readdirSync(directory).filter(file => file.endsWith('.yml') || file.endsWith('.yaml')).sort()) {
  const { lines, steps } = stepsOf(readFileSync(join(directory, name), 'utf8'))
  const covered = new Set()
  for (const step of steps) for (let i = 0; i < step.text.split('\n').length; i++) covered.add(step.start + i)
  lines.forEach((line, index) => {
    if (/apt-get\b|playwright install-deps\b|playwright install --with-deps\b/.test(line) && !covered.has(index)) {
      failures.push(`${name}:${index + 1}: apt or Playwright dependency install is outside a step`)
    }
  })
  for (const step of steps) {
    const apt = /apt-get\b/.test(step.text)
    const playwright = /playwright install-deps\b|playwright install --with-deps\b/.test(step.text)
    if (!apt && !playwright) continue
    const label = (step.text.match(/^        name: (.+)$/m) || [])[1] || '(unnamed)'
    const where = `${name}: ${label}`
    const limit = minutes(step.text)
    if (apt) {
      if (limit !== 4) failures.push(`${where}: apt step needs timeout-minutes: 4`)
      if (!step.text.includes('command -v fish && command -v zsh')) failures.push(`${where}: apt step must skip when fish and zsh are already installed`)
      if (!step.text.includes('for i in 1 2 3; do') || !step.text.includes(aptBound)) failures.push(`${where}: apt step needs three attempts of timeout 150 with Acquire retries and 20s HTTP timeouts`)
    }
    if (playwright) {
      if (limit === null || limit < 13 || limit > 18) failures.push(`${where}: Playwright dependency install needs timeout-minutes between 13 and 18`)
      if (!/for i in 1 2 3; do\n\s+timeout 240 npx playwright install(?:-deps| --with-deps)\b/.test(step.text)) {
        failures.push(`${where}: Playwright dependency install needs three attempts of timeout 240`)
      }
    }
  }
}

if (failures.length) {
  console.error(failures.join('\n'))
  process.exit(1)
}
console.log('apt and Playwright dependency installs are bounded')
