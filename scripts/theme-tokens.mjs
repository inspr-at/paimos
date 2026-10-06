// SPDX-License-Identifier: AGPL-3.0-only
// node scripts/theme-tokens.mjs --write regenerates Porcelain's fallback.
// The theme-colours unit test checks it in CI without writing source files.
import { readFileSync, writeFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { PORCELAIN, themeTokens } from '../web/src/lib/themeEngine.ts'

const path = fileURLToPath(new URL('../web/src/styles/tokens.css', import.meta.url))
export function generatedBlock(mode, indent = '  ') {
  return `${indent}/* BEGIN GENERATED THEME ${mode} (scripts/theme-tokens.mjs) */\n` +
    Object.entries(themeTokens(PORCELAIN, mode)).map(([key, value]) => `${indent}${key}: ${value};`).join('\n') +
    `\n${indent}/* END GENERATED THEME ${mode} */`
}
const source = readFileSync(path, 'utf8')
const result = source.replace(/^[ \t]*\/\* BEGIN GENERATED THEME (light|dark)[^\n]*\*\/[\s\S]*?^[ \t]*\/\* END GENERATED THEME \1 \*\//gm,
  (match, mode) => generatedBlock(mode, match.match(/^[ \t]*/)[0]))
if (process.argv.includes('--write')) writeFileSync(path, result)
else if (result !== source) {
  console.error('Porcelain tokens drifted; run node scripts/theme-tokens.mjs --write')
  process.exitCode = 1
}
