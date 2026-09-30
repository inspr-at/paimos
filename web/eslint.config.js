// SPDX-License-Identifier: AGPL-3.0-only
// Fences for the session and run ledger (AEON-449). The compiler already refuses a raw row
// where an admitted one is required (src/lib/ledger.check.ts pins that down); these rules
// close the ways around it that a type cannot see: naming the endpoints outside the one
// module that stamps their answers, and reaching the two functions that make and open a
// wire row. Run by `npm run lint` and by CI.
import tsParser from '@typescript-eslint/parser'
import vueParser from 'vue-eslint-parser'

// The regular expressions avoid "/" (esquery cannot hold one): \x2f stands for it.
const endpoint = 'The session and run endpoints are read and written only by src/lib/agentRows.ts, which stamps every row with its own row_version and hands it out unreadable until the ledger admits it (AEON-449). Call one of its functions; for a session sub-resource (controls, recovery, requests, watch) build the path with sessionResource.'
const endpoints = [
  { selector: 'Literal[value=/harness-sessions/]', message: endpoint },
  { selector: 'TemplateElement[value.raw=/harness-sessions/]', message: endpoint },
  { selector: 'Literal[value=/^\\x2fruns([\\x2f?]|$)/]', message: endpoint },
  { selector: 'TemplateElement[value.raw=/^\\x2fruns([\\x2f?]|$)/]', message: endpoint },
  { selector: 'TemplateLiteral[quasis.0.value.raw=/^\\x2fwork-orders\\x2f/] > TemplateElement[value.raw=/^\\x2fruns/]', message: endpoint },
]
const cast = 'A cast does not make a row admitted: only the ledger does (src/lib/ledger.ts). Read the row through the store, or pass the wire row to the store\'s admit functions.'
const casts = [
  { selector: 'TSAsExpression > TSTypeReference[typeName.name=/^(Admitted|HarnessSession|AgentRun)$/]', message: cast },
  { selector: 'TSTypeAssertion > TSTypeReference[typeName.name=/^(Admitted|HarnessSession|AgentRun)$/]', message: cast },
]
const wrapOnly = { group: ['**/wire', '**/wire.ts'], importNames: ['wrapRow'], message: 'Only src/lib/agentRows.ts wraps a row: it is the one module that talks to the session and run endpoints.' }
const openOnly = { group: ['**/wire', '**/wire.ts'], importNames: ['openRow'], message: 'Only src/lib/ledger.ts opens a wire row: it is the one place that judges rows.' }

const script = { ecmaVersion: 'latest', sourceType: 'module' }
export default [
  { ignores: ['dist/**', 'node_modules/**', 'test-results/**', 'public/**'] },
  {
    files: ['src/**/*.ts'],
    languageOptions: { parser: tsParser, parserOptions: script },
  },
  {
    files: ['src/**/*.vue'],
    languageOptions: { parser: vueParser, parserOptions: { ...script, parser: tsParser } },
  },
  {
    files: ['src/**/*.ts', 'src/**/*.vue'],
    rules: {
      'no-restricted-syntax': ['error', ...endpoints, ...casts],
      'no-restricted-imports': ['error', { patterns: [wrapOnly, openOnly] }],
    },
  },
  {
    // The module that speaks to the endpoints, and the only one that wraps a row.
    files: ['src/lib/agentRows.ts'],
    rules: {
      'no-restricted-syntax': ['error', ...casts],
      'no-restricted-imports': ['error', { patterns: [openOnly] }],
    },
  },
  {
    // The ledger: the only place that opens a wire row and brands the one it admits.
    files: ['src/lib/ledger.ts'],
    rules: {
      'no-restricted-syntax': ['error', ...endpoints],
      'no-restricted-imports': ['error', { patterns: [wrapOnly] }],
    },
  },
]
