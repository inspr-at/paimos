// SPDX-License-Identifier: AGPL-3.0-only
// AEON-206: ticket keys and bare http(s) addresses in Markdown, outside code and
// links that are already there.
import assert from 'node:assert/strict'
import { test } from 'node:test'
import MarkdownIt from 'markdown-it'
import { installMarkdownTicketLinks, splitPlainText, trimBareUrl } from '../src/lib/markdownLinks.ts'

function render(source: string) {
  const markdown = new MarkdownIt({ html: false, linkify: false })
  markdown.core.ruler.after('inline', 'task-lists', () => {})
  installMarkdownTicketLinks(markdown)
  return markdown.render(source)
}

test('a ticket key is a placeholder and the same key in code is not', () => {
  const html = render('See AEON-80 and `AEON-80`.\n\n```\nAEON-80\n```\n')
  assert.match(html, /data-ticket-key="AEON-80"/)
  assert.match(html, /<code>AEON-80<\/code>/)
  assert.equal(html.split('data-ticket-key').length - 1, 1)
  assert.doesNotMatch(html, /<pre[\s\S]*data-ticket-key/)
})

test('an existing link is left alone, including a key in its text', () => {
  const html = render('[AEON-80](https://example.com/docs) and PAI-1064')
  assert.match(html, /href="https:\/\/example\.com\/docs"/)
  assert.doesNotMatch(html, /data-ticket-key="AEON-80"/)
  assert.match(html, /data-ticket-key="PAI-1064"/)
})

test('a bare https address is a link and trailing punctuation stays outside it', () => {
  const html = render('Read https://example.com/safe?q=1. Then stop.')
  assert.match(html, /href="https:\/\/example\.com\/safe\?q=1"/)
  assert.match(html, /<\/a>\./)
  assert.doesNotMatch(html, /href="javascript:/)
})

test('javascript and credentialled addresses stay text', () => {
  const html = render('javascript:alert(1) and https://user:pass@example.com/secret')
  assert.doesNotMatch(html, /<a /)
  assert.match(html, /javascript:alert\(1\)/)
  assert.match(html, /user:pass@example.com/)
})

test('a key inside an address is part of the address', () => {
  const html = render('https://example.com/AEON-80')
  assert.doesNotMatch(html, /data-ticket-key/)
  assert.match(html, /href="https:\/\/example\.com\/AEON-80"/)
})

test('splitPlainText keeps boundaries and balanced parentheses', () => {
  assert.deepEqual(splitPlainText('AEON-80, PAI-1064.'), [
    { kind: 'ticket', text: 'AEON-80' },
    { kind: 'text', text: ', ' },
    { kind: 'ticket', text: 'PAI-1064' },
    { kind: 'text', text: '.' },
  ])
  assert.deepEqual(splitPlainText('aeon-80 AEON-80X AEON-0 TOOLONGPREF-80'), [{ kind: 'text', text: 'aeon-80 AEON-80X AEON-0 TOOLONGPREF-80' }])
  assert.deepEqual(splitPlainText('XAEON-80'), [{ kind: 'ticket', text: 'XAEON-80' }])
  assert.equal(trimBareUrl('https://en.wikipedia.org/wiki/Aeon_(band)'), 'https://en.wikipedia.org/wiki/Aeon_(band)')
  assert.equal(trimBareUrl('https://example.com/a).'), 'https://example.com/a')
  assert.equal(splitPlainText('See (https://example.com/a) now.')[0]?.kind, 'text')
  const parts = splitPlainText('See (https://example.com/a) now.')
  assert.ok(parts.some(part => part.kind === 'url' && part.text === 'https://example.com/a'))
})
