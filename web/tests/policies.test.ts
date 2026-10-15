// SPDX-License-Identifier: AGPL-3.0-only
// Risks: Policies claims agent keys never carry a permission they can carry (or
// forgets one); a rule links somewhere that will not open; the reviewer sentences
// say something the server's review-policy fields do not; the page names a model.
import test from 'node:test'
import assert from 'node:assert/strict'
import { readdirSync, readFileSync } from 'node:fs'
import { AGENT_ASKS, ASK_RISKS, askPreview, ELSEWHERE_RULES, familyOrderWords, floorSentence, reviewerRules, ruleLink, securitySentence, type ReviewerRules } from '../src/lib/policies.ts'
import ceiling from '../../internal/auth/testdata/key_scope_ceiling.json' with { type: 'json' }

// The server registry in source: one {Key: …, Risk: …, AgentGrantable: …} literal per permission.
function serverRegistry() {
  const dir = new URL('../../internal/authz/', import.meta.url)
  const rows: { key: string; risk: string; agent: boolean }[] = []
  for (const name of readdirSync(dir).filter(file => /^permissions_.*\.go$/.test(file) && !file.endsWith('_test.go'))) {
    for (const match of readFileSync(new URL(name, dir), 'utf8').matchAll(/\{Key: "([^"]+)"[^\n]*?Risk: "(\w+)"[^\n]*?AgentGrantable: (true|false)/g)) rows.push({ key: match[1]!, risk: match[2]!, agent: match[3] === 'true' })
  }
  return rows
}

test('the asks list is exactly the registry’s never-on-an-agent-key permissions, at their risk', () => {
  const registry = serverRegistry()
  // The parse saw every permission the Go parity fixture holds, so nothing hid from the regex.
  assert.equal(registry.length, ceiling.registry.length)
  assert.deepEqual(registry.filter(p => !p.agent).map(p => p.key).sort(), ceiling.registry.filter(p => !p.agent_grantable).map(p => p.key).sort())
  const listed = ASK_RISKS.flatMap(({ risk }) => AGENT_ASKS[risk].flatMap(item => item.keys.map(key => ({ key, risk }))))
  assert.equal(new Set(listed.map(p => p.key)).size, listed.length, 'a permission is listed twice')
  assert.deepEqual(listed.sort((a, b) => a.key.localeCompare(b.key)), registry.filter(p => !p.agent).map(({ key, risk }) => ({ key, risk })).sort((a, b) => a.key.localeCompare(b.key)))
  assert.deepEqual(ASK_RISKS.map(group => group.label), ['High risk', 'Medium risk', 'Low risk'])
  assert.equal(AGENT_ASKS.high.length, 11)
  assert.deepEqual(AGENT_ASKS.high.find(item => item.keys.includes('agents.create')), { title: 'Provision messaging assistants', keys: ['agents.create'] })
  // The Policies risk head renders this sentence. The pre-assistant head started
  // "High risk · 10 ·" and is what web/tests/policies-rules.spec.ts rejected in CI.
  const highHead = `High risk · ${AGENT_ASKS.high.length} · ${askPreview(AGENT_ASKS.high)}`
  assert.equal(/^High risk · 10 · Approve or deny/.test(highHead), false)
  assert.match(highHead, /^High risk · 11 · Approve or deny what agents ask for, answer questions agents ask, change roles and members, transfer workspace ownership, create and revoke API keys, provision messaging assistants,/)
  assert.equal(askPreview(AGENT_ASKS.medium), 'Connect a chat, edit the portal profile, accept a quote in the portal')
})

test('twelve rules elsewhere; a link shows only when every permission of its place is held', () => {
  assert.equal(ELSEWHERE_RULES.length, 12)
  assert.equal(new Set(ELSEWHERE_RULES.map(rule => rule.title)).size, 12)
  const all = () => true, none = () => false
  assert.deepEqual(ELSEWHERE_RULES.filter(rule => !ruleLink(rule, all)).map(rule => rule.title), ['Fix rounds and lane pause', 'Merge queue and code owners'])
  assert.deepEqual(ELSEWHERE_RULES.filter(rule => ruleLink(rule, none)).map(rule => rule.to), ['/settings/routines#rules'])
  const access = ELSEWHERE_RULES.find(rule => rule.title === 'Who holds which permission')!
  assert.equal(ruleLink(access, key => key === 'members.read'), null)
  assert.equal(ruleLink(access, key => key === 'members.read' || key === 'roles.read'), '/settings/access/roles')
  assert.equal(ruleLink(ELSEWHERE_RULES.find(rule => rule.title === 'Approvals in force')!, key => key === 'approvals.read'), '/decision-desk')
  assert.deepEqual(ELSEWHERE_RULES.filter(rule => rule.status === 'Advisory').map(rule => rule.owner), ['Agent rules', 'Lane coordination', 'GitHub'])
})

test('reviewer sentences say the review-policy fields and nothing else', () => {
  const rules: ReviewerRules = { family_order: ['openai', 'xai', 'anthropic', 'cursor', 'google', 'local'], floor: { tiers: ['strong', 'frontier'], min_effort_level: 4 }, security_excludes: ['anthropic'] }
  assert.equal(familyOrderWords(rules), 'OpenAI, xAI, Anthropic, Cursor, Google, local models')
  assert.equal(floorSentence(rules), 'Reviews run on a strong or frontier model at its highest level: xhigh, or max where a model has it.')
  assert.equal(securitySentence(rules), 'Security work is never reviewed by an Anthropic model, nor by the author’s family.')
  assert.equal(familyOrderWords({ ...rules, family_order: ['google', 'mistral'] }), 'Google, mistral')
  assert.equal(floorSentence({ ...rules, floor: { tiers: ['frontier'], min_effort_level: 5 } }), 'Reviews run on a frontier model at its highest level, at least max.')
  assert.equal(securitySentence({ ...rules, security_excludes: [] }), 'Security work is never reviewed by the author’s family.')
  assert.equal(securitySentence({ ...rules, security_excludes: ['anthropic', 'google'] }), 'Security work is never reviewed by an Anthropic or Google model, nor by the author’s family.')
  assert.deepEqual(reviewerRules({ ...rules, valid_families: ['openai'] } as Partial<ReviewerRules>), rules)
  for (const broken of [null, {}, { ...rules, family_order: 'openai' }, { ...rules, floor: { tiers: ['strong'] } }, { ...rules, security_excludes: undefined }]) assert.equal(reviewerRules(broken as Partial<ReviewerRules>), null)
})

test('Policies names no model, says no Touch ID yet, and keeps one language', () => {
  const words = [...ASK_RISKS.flatMap(({ risk }) => AGENT_ASKS[risk].map(item => item.title)), ...ELSEWHERE_RULES.flatMap(rule => [rule.title, rule.text, rule.owner])].join('\n')
  const page = readFileSync(new URL('../src/components/settings/PoliciesSection.vue', import.meta.url), 'utf8')
  const card = readFileSync(new URL('../src/components/settings/CrossFamilyReviewCard.vue', import.meta.url), 'utf8')
  for (const text of [words, page, card]) assert.doesNotMatch(text, /Touch ID|Face ID|GPT-|Opus|Grok 4|Sonnet|Gemini/)
  assert.doesNotMatch(card, /\bde\b ?\?|Prüf|Regel speichern|deliveryLanguage/)
})
