// SPDX-License-Identifier: AGPL-3.0-only
import policy from '../../../internal/agentactivity/privacy.json' with { type: 'json' }

const opaque = new RegExp(policy.opaque_pattern)
const forbiddenUnicode = new RegExp(policy.unicode_categories.map(category => `\\p{${category}}`).join('|'), 'u')

export function safeActivityText(text: string): boolean {
  const lower = text.toLowerCase()
  return !/[\uD800-\uDFFF]/u.test(text) && !opaque.test(text) && !forbiddenUnicode.test(text)
    && ![...policy.forbidden_characters].some(character => text.includes(character))
    && !policy.credential_words.some(word => lower.includes(word))
}

export function cleanActivityNote(raw: string | null | undefined): string {
  const withoutControls = raw?.replace(/\p{Cc}/gu, '') ?? ''
  const text = withoutControls.trim()
  return text && [...text].length <= 120 && safeActivityText(withoutControls) ? text : ''
}
