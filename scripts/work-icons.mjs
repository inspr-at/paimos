// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1142: internal/nodes/work_icons.json is the one list of work-level icons.
// Go embeds it and the web imports it; this script writes its names into the
// API enum (WorkLevel.icon in api/areas/settings.yaml). --check fails on drift.
import { readFileSync, writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
export const sourcePath = resolve(root, 'internal/nodes/work_icons.json')
export const contractPath = resolve(root, 'api/areas/settings.yaml')
const yamlWords = new Set(['y', 'n', 'yes', 'no', 'on', 'off', 'true', 'false', 'null'])

/** The icon names in picker order; throws on a list the server, API and web could not share. */
export function workIconNames(document) {
  const icons = document?.icons
  if (!Array.isArray(icons) || icons.length === 0 || icons.length > 200) throw new Error('work_icons.json: expected 1..200 icons')
  const names = new Set()
  for (const icon of icons) {
    if (!/^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$/.test(icon?.name ?? '') || icon.name.length > 40) throw new Error(`work_icons.json: invalid name ${JSON.stringify(icon?.name)}`)
    if (names.has(icon.name)) throw new Error(`work_icons.json: duplicate ${icon.name}`)
    if (typeof icon.label !== 'string' || !icon.label.trim() || icon.label.length > 40) throw new Error(`work_icons.json: ${icon.name} needs a label`)
    if (!Array.isArray(icon.tags) || icon.tags.length > 12 || icon.tags.some(tag => typeof tag !== 'string' || tag !== tag.trim().toLowerCase() || !tag || tag.length > 40)) throw new Error(`work_icons.json: ${icon.name} needs up to 12 lowercase tags`)
    names.add(icon.name)
  }
  return [...names]
}

/** The WorkLevel.icon property block, wrapped to readable lines. */
export function iconBlock(names) {
  const values = ["''", ...names.map(name => yamlWords.has(name) ? `'${name}'` : name)]
  const lines = []
  let line = '          enum: ['
  for (const [i, value] of values.entries()) {
    const piece = value + (i < values.length - 1 ? ',' : ']')
    if (line.length + piece.length + 1 > 120 && !line.endsWith('[')) { lines.push(line); line = '            ' + piece } else line += (line.endsWith('[') ? '' : ' ') + piece
  }
  lines.push(line)
  return ['        icon:', '          type: string', "          description: 'Blank draws the level default. The list is internal/nodes/work_icons.json.'", ...lines].join('\n') + '\n'
}

/** settings.yaml with the WorkLevel.icon block replaced. */
export function withIconEnum(contract, names) {
  const lines = contract.split('\n')
  const schema = lines.indexOf('    WorkLevel:')
  if (schema < 0) throw new Error('settings.yaml: WorkLevel schema not found')
  let start = schema + 1
  while (start < lines.length && /^ {6,}/.test(lines[start]) && !/^ {8}icon:/.test(lines[start])) start++
  if (!/^ {8}icon:/.test(lines[start] ?? '')) throw new Error('settings.yaml: WorkLevel.icon not found')
  let end = start + 1
  while (end < lines.length && /^ {9,}/.test(lines[end])) end++
  return [...lines.slice(0, start), iconBlock(names).slice(0, -1), ...lines.slice(end)].join('\n')
}

export function main(args) {
  if (args.length !== 1 || !['--write', '--check'].includes(args[0])) throw new Error('Usage: node scripts/work-icons.mjs --write|--check')
  const names = workIconNames(JSON.parse(readFileSync(sourcePath, 'utf8')))
  const contract = readFileSync(contractPath, 'utf8'), expected = withIconEnum(contract, names)
  if (args[0] === '--write') {
    if (expected !== contract) writeFileSync(contractPath, expected)
    console.log(`WorkLevel.icon lists ${names.length} icons; now run node api/generate.mjs --write`)
    return 0
  }
  if (expected !== contract) { console.error('WorkLevel.icon drifted from internal/nodes/work_icons.json; run node scripts/work-icons.mjs --write'); return 1 }
  return 0
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? '').href) process.exitCode = main(process.argv.slice(2))
