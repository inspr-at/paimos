#!/usr/bin/env node
// SPDX-License-Identifier: AGPL-3.0-only
// Headless DOM smoke check; no browser, dependency install, or network needed.
const fs = require('node:fs')
const path = require('node:path')
const vm = require('node:vm')
const assert = require('node:assert/strict')
if (process.argv.length !== 3) {
  console.error('Usage: node scripts/audit/run.cjs PATH/audit.html')
  process.exit(2)
}
const html = fs.readFileSync(process.argv[2], 'utf8')
const data = html.match(/<script type="application\/json" id="data">([\s\S]*?)<\/script>/)?.[1]
const script = html.match(/<script>\s*([\s\S]*?)<\/script>/)?.[1]
assert(data && script, 'report must embed data and renderer')
const template = fs.readFileSync(path.join(__dirname, 'template.html'), 'utf8')
const renderer = template.match(/<script>\s*([\s\S]*?)<\/script>/)[1]
assert.equal(script, renderer, 'report must use the bundled renderer')
assert(!/<(?:link|script|img)\b[^>]*(?:href|src)=/i.test(html), 'report must be self-contained')
const audit = JSON.parse(data)
const elements = {}
const element = id => elements[id] ??= {
  id, innerHTML: '', textContent: '', children: [], handlers: {},
  addEventListener(name, handler) { this.handlers[name] = handler },
  setAttribute() {}, closest() { return null },
}
const document = {
  getElementById: id => id === 'data' ? { textContent: data } : element(id),
  querySelector: selector => element(selector),
}
// This is a hang guard, not a renderer speed assertion. Cold locale setup and
// scheduling on shared CI runners can consume a second even for two findings.
vm.runInNewContext(renderer, { document, location: { hash: '' } }, { timeout: 10000 })
assert.equal(elements['#count'].textContent, `${audit.findings.length} of ${audit.findings.length} shown`)
assert.equal((elements['#groups'].innerHTML.match(/<article class="finding"/g) ?? []).length, audit.findings.length)
let absentQuery = 'no matching finding'
while (JSON.stringify(audit).toLowerCase().includes(absentQuery)) absentQuery += 'x'
elements['#q'].handlers.input({ target: { value: absentQuery } })
assert.equal(elements['#count'].textContent, `0 of ${audit.findings.length} shown`)
elements['#q'].handlers.input({ target: { value: '' } })
elements['#openall'].handlers.click()
elements['#closeall'].handlers.click()
console.log(`audit page smoke passed (${audit.findings.length} findings)`)
