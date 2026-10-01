// SPDX-License-Identifier: AGPL-3.0-only
// Fences for the session and run ledger (AEON-449). The compiler already refuses a raw row
// where an admitted one is required (src/lib/ledger.check.ts pins that down); these rules
// close the ways around it that a type cannot see: naming the endpoints outside the one
// module that stamps their answers, and reaching the two functions that make and open a
// wire row. Run by `npm run lint` and by CI.
import tsParser from '@typescript-eslint/parser'
import vueParser from 'vue-eslint-parser'

// The regular expressions avoid "/" (esquery cannot hold one): \x2f stands for it.
const endpoint = 'The session and run endpoints are read and written only by src/lib/agentRows.ts, which stamps every row with its own row_version and hands it out unreadable until the ledger admits it (AEON-449). Call one of its functions; for a session sub-resource call its fixed operation in that module.'
const endpoints = [
  { selector: 'Literal[value=/harness-sessions/]', message: endpoint },
  { selector: 'TemplateElement[value.raw=/harness-sessions/]', message: endpoint },
  { selector: 'Literal[value=/^\\x2fruns([\\x2f?]|$)/]', message: endpoint },
  { selector: 'TemplateElement[value.raw=/^\\x2fruns([\\x2f?]|$)/]', message: endpoint },
  { selector: 'TemplateLiteral[quasis.0.value.raw=/^\\x2fwork-orders\\x2f/] > TemplateElement[value.raw=/^\\x2fruns/]', message: endpoint },
]
const cast = 'A cast does not make a row admitted: only the canonical store does (src/stores/agents.ts). Read the row through the store, or pass the wire row to the store\'s admit functions.'
const casts = [
  { selector: 'TSAsExpression TSTypeReference[typeName.name=/^(Wire|Admitted|HarnessSession|AgentRun)$/]', message: cast },
  { selector: 'TSTypeAssertion TSTypeReference[typeName.name=/^(Wire|Admitted|HarnessSession|AgentRun)$/]', message: cast },
]
const wrapOnly = { group: ['**/wire', '**/wire.ts'], importNames: ['wrapRow'], message: 'Only src/lib/agentRows.ts wraps a row: it is the one module that talks to the session and run endpoints.' }
const openOnly = { group: ['**/wire', '**/wire.ts'], importNames: ['openRow'], message: 'Only src/stores/agents.ts opens a wire row: it is the one place that judges rows.' }


// Include imported aliases and local type aliases in the cast fence. This is an
// accidental-bypass guard, not a security boundary against deliberate TS abuse.
const rowNames = new Set(['Wire', 'Admitted', 'HarnessSession', 'AgentRun'])
const references = node => {
  if (!node || typeof node !== 'object') return []
  const found = node.type === 'TSTypeReference' && node.typeName?.type === 'Identifier' ? [node.typeName.name] : []
  for (const [key, value] of Object.entries(node)) {
    if (key === 'parent') continue
    if (Array.isArray(value)) for (const item of value) found.push(...references(item))
    else if (value && typeof value === 'object') found.push(...references(value))
  }
  return found
}
const aliasFence = {
  meta: { type: 'problem', schema: [], messages: { cast } },
  create(context) {
    const names = new Set(rowNames), aliases = [], assertions = []
    return {
      ImportSpecifier(node) { if (rowNames.has(node.imported.name)) names.add(node.local.name) },
      TSTypeAliasDeclaration(node) { aliases.push(node) },
      TSAsExpression(node) { assertions.push(node) },
      TSTypeAssertion(node) { assertions.push(node) },
      'Program:exit'() {
        let changed = true
        while (changed) {
          changed = false
          for (const alias of aliases) if (!names.has(alias.id.name) && references(alias.typeAnnotation).some(name => names.has(name))) {
            names.add(alias.id.name); changed = true
          }
        }
        for (const assertion of assertions) if (references(assertion.typeAnnotation).some(name => names.has(name))) context.report({ node: assertion, messageId: 'cast' })
      },
    }
  },
}
const agentsCode = ['src/components/agents/**/*.ts', 'src/components/agents/**/*.vue', 'src/stores/agents.ts', 'src/lib/agent*.ts', 'src/lib/startAgent.ts', 'src/lib/attachWatch.ts', 'src/lib/deliveryRating.ts']
const jsonFence = { selector: 'CallExpression[callee.object.name="JSON"][callee.property.name="parse"]', message: 'Use parseJson (unknown) and validate the result; JSON.parse returns any and can bypass row admission.' }
const assignFence = { selector: 'CallExpression[callee.object.name="Object"][callee.property.name="assign"]', message: 'Object.assign can carry the admission brand onto a changed copy. Use a plain spread for non-row state, and the store for rows.' }

const script = { ecmaVersion: 'latest', sourceType: 'module' }
export default [
  { plugins: { 'agent-rows': { rules: { 'no-row-cast': aliasFence } } } },
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
      'agent-rows/no-row-cast': 'error',
      'no-restricted-imports': ['error', { patterns: [wrapOnly, openOnly] }],
    },
  },
  { files: agentsCode, rules: { 'no-restricted-syntax': ['error', ...endpoints, ...casts, jsonFence, assignFence] } },
  {
    // The module that speaks to the endpoints, and the only one that wraps a row.
    files: ['src/lib/agentRows.ts'],
    rules: {
      'no-restricted-syntax': ['error', ...casts, jsonFence, assignFence],
      'no-restricted-imports': ['error', { patterns: [openOnly] }],
    },
  },
  {
    // The canonical store: the only place that opens a wire row and brands the one it admits.
    files: ['src/stores/agents.ts'],
    rules: {
      'no-restricted-syntax': ['error', ...endpoints, jsonFence, assignFence],
      'agent-rows/no-row-cast': 'off',
      'no-restricted-imports': ['error', { patterns: [wrapOnly] }],
    },
  },
  { files: ['src/lib/wire.ts'], rules: { 'agent-rows/no-row-cast': 'off', 'no-restricted-syntax': ['error', ...endpoints] } },
]
