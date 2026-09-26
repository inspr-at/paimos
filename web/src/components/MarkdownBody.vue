<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, createVNode, getCurrentInstance, onBeforeUnmount, onMounted, ref, render, watch, type AppContext } from 'vue'
import MarkdownIt from 'markdown-it'
import { ATTACHMENT_REF, contentUrl } from '../lib/attachments'
import { headingSlug, type Heading } from '../lib/knowledge'
import { installMarkdownTicketLinks } from '../lib/markdownLinks'
import TicketLink from './releases/TicketLink.vue'
// anchors (knowledge pages): headings get ids and a link to themselves, the
// outline is emitted for a table of contents, and web links open in a new tab.
const props = defineProps<{ body: string; anchors?: boolean }>()
const emit = defineEmits<{ openAttachment: [id: string]; headings: [items: Heading[]]; anchor: [id: string]; jump: [id: string] }>()
// Raw HTML stays text; markdown-it rejects script/data links. linkify stays off:
// bare http(s) addresses and ticket keys are linked by our own pass, which skips
// code and links that are already written. Only this ticket's own attachments
// render as images (![caption](attachment:<id>)); any other image stays text,
// so viewing another principal's Markdown never loads third-party resources.
const markdown = new MarkdownIt({ html: false, linkify: false })
markdown.renderer.rules.image = (tokens, index) => {
  const token = tokens[index]
  const src = String(token.attrGet('src') ?? '')
  const alt = markdown.utils.escapeHtml(token.content || '')
  const match = ATTACHMENT_REF.exec(src)
  if (!match) return markdown.utils.escapeHtml(`![${token.content}](${src})`)
  const id = match[1]
  return `<button type="button" class="md-attachment" data-attachment="${id}" aria-label="Open ${alt || 'attachment'}"><img src="${contentUrl(id, 'preview')}" alt="${alt}" loading="lazy" decoding="async"></button>`
}

// GitHub-style task lists: "- [ ] open" and "- [x] done" render as read-only checkboxes.
markdown.core.ruler.after('inline', 'task-lists', state => {
  const tokens = state.tokens
  for (let index = 2; index < tokens.length; index++) {
    const inline = tokens[index]
    if (inline.type !== 'inline' || tokens[index - 1].type !== 'paragraph_open' || tokens[index - 2].type !== 'list_item_open') continue
    const first = inline.children?.[0]
    const match = first?.type === 'text' ? /^\[([ xX])\]\s+/.exec(first.content) : null
    if (!first || !match) continue
    first.content = first.content.slice(match[0].length)
    const box = new state.Token('task_checkbox', '', 0)
    box.meta = { checked: match[1] !== ' ' }
    inline.children!.unshift(box)
    tokens[index - 2].attrJoin('class', 'task-list-item')
  }
})
markdown.renderer.rules.task_checkbox = (tokens, index) =>
  `<input class="task-box" type="checkbox" disabled${tokens[index].meta?.checked ? ' checked' : ''} aria-label="${tokens[index].meta?.checked ? 'Done' : 'Not done'}"> `
installMarkdownTicketLinks(markdown)

// Heading anchors: the DOM id carries a prefix so a heading can never take an id the
// page itself uses; the link (and the URL hash) is the bare slug.
interface AnchorEnv { [key: string]: unknown; anchors?: boolean; used?: Map<string, number>; headings?: Heading[] }
const LINK_ICON = '<svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><path d="M6.6 9.4 9.4 6.6M7.2 4.4l1.2-1.2a2.6 2.6 0 0 1 3.7 3.7l-1.2 1.2M8.8 11.6l-1.2 1.2a2.6 2.6 0 0 1-3.7-3.7l1.2-1.2"/></svg>'
const inlineText = (token: { children?: { type: string; content: string }[] | null } | undefined) =>
  (token?.children ?? []).filter(child => child.type === 'text' || child.type === 'code_inline' || child.type === 'ticket_ref').map(child => child.content).join('').trim()
markdown.renderer.rules.heading_open = (tokens, index, options, rawEnv, self) => {
  const env = rawEnv as AnchorEnv | undefined
  if (env?.anchors) {
    const text = inlineText(tokens[index + 1] as never)
    const id = headingSlug(text, env.used ??= new Map())
    const level = Number(tokens[index].tag.slice(1))
    ;(env.headings ??= []).push({ level, text, id })
    tokens[index].attrSet('id', `h-${id}`)
    tokens[index].attrJoin('class', 'md-heading')
    tokens[index].meta = { ...(tokens[index].meta ?? {}), anchor: id, text }
  }
  return self.renderToken(tokens, index, options)
}
markdown.renderer.rules.heading_close = (tokens, index, options, rawEnv, self) => {
  const env = rawEnv as AnchorEnv | undefined
  const open = tokens.slice(0, index).reverse().find(token => token.type === 'heading_open')
  const anchor = env?.anchors ? open?.meta?.anchor as string | undefined : undefined
  const link = anchor ? `<a class="md-anchor" href="#${anchor}" data-anchor="${anchor}" aria-label="Copy a link to ${markdown.utils.escapeHtml(String(open?.meta?.text ?? 'this section'))}">${LINK_ICON}</a>` : ''
  return link + self.renderToken(tokens, index, options)
}
markdown.renderer.rules.link_open = (tokens, index, options, rawEnv, self) => {
  const env = rawEnv as AnchorEnv | undefined
  const href = String(tokens[index].attrGet('href') ?? '')
  if (env?.anchors && /^https?:\/\//i.test(href)) { tokens[index].attrSet('target', '_blank'); tokens[index].attrSet('rel', 'noopener noreferrer') }
  return self.renderToken(tokens, index, options)
}

// Code blocks and tables scroll sideways when wide; keyboard users can reach and scroll them.
const renderFence = markdown.renderer.rules.fence!
markdown.renderer.rules.fence = (tokens, index, options, env, self) => renderFence(tokens, index, options, env, self).replace(/^<pre>/, '<pre tabindex="0">')
const renderCodeBlock = markdown.renderer.rules.code_block!
markdown.renderer.rules.code_block = (tokens, index, options, env, self) => renderCodeBlock(tokens, index, options, env, self).replace(/^<pre>/, '<pre tabindex="0">')
markdown.renderer.rules.table_open = (tokens, index, options, _env, self) => { tokens[index].attrSet('tabindex', '0'); return self.renderToken(tokens, index, options) }

const result = computed(() => {
  const env: AnchorEnv = { anchors: !!props.anchors }
  const html = markdown.render(props.body, env as Parameters<typeof markdown.render>[1])
  return { html, headings: env.headings ?? [] }
})
const rendered = computed(() => result.value.html)
watch(() => result.value.headings, headings => { if (props.anchors) emit('headings', headings) }, { immediate: true })

// TicketLink cannot live inside v-html. Each key is a placeholder; the same
// component the release history uses is mounted there, so peek and URL behaviour match.
const host = ref<HTMLElement>()
// render() has no parent, so inject would miss provides from App (the peek).
// The instance chain still reaches them; pinia and the router stay on its prototype.
const instance = getCurrentInstance()
const appContext: AppContext | null = instance
  ? { ...instance.appContext, provides: (instance as unknown as { provides: AppContext['provides'] }).provides }
  : null
const slots = new Set<HTMLElement>()
function unmountTickets() {
  for (const el of slots) render(null, el)
  slots.clear()
}
function mountTickets() {
  unmountTickets()
  const root = host.value
  if (!root || !appContext) return
  for (const el of root.querySelectorAll<HTMLElement>('[data-ticket-key]')) {
    const key = el.dataset.ticketKey
    if (!key) continue
    // v-html left the key as text. Mounting adds the link beside it unless that text goes first.
    el.replaceChildren()
    const vnode = createVNode(TicketLink, { ticketKey: key, variant: 'inline' })
    vnode.appContext = appContext
    render(vnode, el)
    slots.add(el)
  }
}
onMounted(mountTickets)
watch(rendered, () => { mountTickets() }, { flush: 'post' })
onBeforeUnmount(unmountTickets)
function click(event: MouseEvent) {
  const target = event.target as HTMLElement
  const button = target.closest<HTMLElement>('.md-attachment')
  if (button?.dataset.attachment) { event.preventDefault(); emit('openAttachment', button.dataset.attachment); return }
  if (!props.anchors) return
  const anchor = target.closest<HTMLElement>('.md-anchor')
  if (anchor?.dataset.anchor) { event.preventDefault(); emit('anchor', anchor.dataset.anchor); return }
  const link = target.closest<HTMLAnchorElement>('a[href^="#"]')
  if (link) { event.preventDefault(); emit('jump', decodeURIComponent(link.getAttribute('href')!.slice(1))) }
}
</script>
<template><div ref="host" class="markdown-body" :class="{ anchored: anchors }" v-html="rendered" @click="click" /></template>
<style scoped>
.markdown-body { overflow-wrap: anywhere; font-size: 14px; line-height: 1.65; color: var(--ink); }
.markdown-body > :deep(:first-child) { margin-top: 0; }
.markdown-body :deep(p) { margin: 0 0 .85em; color: var(--ink); }
.markdown-body :deep(h1), .markdown-body :deep(h2), .markdown-body :deep(h3), .markdown-body :deep(h4) { font-family: var(--font); font-weight: 650; letter-spacing: -.01em; line-height: 1.3; margin: 1.4em 0 .5em; color: var(--ink); }
.markdown-body :deep(h1) { font-size: 1.3em; }
.markdown-body :deep(h2) { font-size: 1.15em; }
.markdown-body :deep(h3), .markdown-body :deep(h4) { font-size: 1em; }
.markdown-body :deep(ul), .markdown-body :deep(ol) { margin: 0 0 .85em; padding-left: 1.4em; }
.markdown-body :deep(li) { margin: .2em 0; }
.markdown-body :deep(li > p) { margin: 0; }
.markdown-body :deep(li::marker) { color: var(--ink-3); }
/* Task lists hang: the box sits in its own column, centred on the first line's
   x-height, and wrapped lines align with the text, not under the box. */
.markdown-body :deep(.task-list-item) { position: relative; display: block; list-style: none; margin-left: -1.4em; padding-left: 22px; }
.markdown-body :deep(.task-box) {
  appearance: none; position: absolute; left: 0; top: calc(.825em + .08em - 7px); display: grid; place-items: center; width: 14px; height: 14px; margin: 0; border-radius: 4px;
  background: var(--field-bg); box-shadow: inset 0 0 0 1.5px var(--line-2);
}
.markdown-body :deep(.task-box:checked) { background: var(--st-ok); box-shadow: none; }
.markdown-body :deep(.task-box:checked::after) { content: ''; width: 7px; height: 4px; border-left: 1.6px solid var(--surface); border-bottom: 1.6px solid var(--surface); transform: translateY(-1px) rotate(-45deg); }
.markdown-body :deep(pre) { overflow: auto; margin: 0 0 1em; padding: 12px 14px; border-radius: var(--radius-s); background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line); }
.markdown-body :deep(code) { font-family: var(--mono); font-size: .92em; font-variant-ligatures: none; font-feature-settings: "liga" 0, "calt" 0; }
/* Inline code is a quiet tint; only code blocks keep a frame. */
.markdown-body :deep(:not(pre) > code) { padding: .5px 4px; border-radius: 4px; background: var(--code-bg); color: var(--ink); }
.markdown-body :deep(pre code) { font-size: .88em; }
/* A quote reads as typography: indented on a quiet tint, no coloured rule (rule 11). */
.markdown-body :deep(blockquote) { margin: 0 0 1em 12px; padding: 8px 14px; border-radius: 10px; background: var(--surface-2); color: var(--ink-2); }
.markdown-body :deep(blockquote > :last-child) { margin-bottom: 0; }
.markdown-body :deep(blockquote p) { color: var(--ink-2); }
.markdown-body :deep(hr) { height: 1px; margin: 1.4em 0; border: 0; background: linear-gradient(90deg, transparent, var(--line-2), transparent); }
.markdown-body :deep(table) { display: block; overflow: auto; margin: 0 0 1em; border-collapse: collapse; font-size: 13px; }
.markdown-body :deep(td), .markdown-body :deep(th) { padding: 6px 10px; border: 1px solid var(--line); text-align: left; }
.markdown-body :deep(th) { font: 500 10.5px var(--mono); letter-spacing: .1em; text-transform: uppercase; color: var(--ink-3); }
.markdown-body :deep(.md-ticket) { display: contents; }
.markdown-body :deep(a:not(.ticket-link)) { color: var(--teal); text-decoration: underline; text-decoration-color: var(--gold); text-underline-offset: 3px; }
.markdown-body :deep(.md-attachment) { display: block; max-width: 100%; margin: .4em 0 1em; padding: 0; border: 0; border-radius: 10px; overflow: hidden; background: var(--surface-sunken, var(--code-bg)); box-shadow: inset 0 0 0 1px var(--line), 0 10px 26px -18px rgba(16, 35, 39, .5); cursor: zoom-in; }
.markdown-body :deep(.md-attachment img) { display: block; max-width: 100%; height: auto; }
.markdown-body :deep(.md-attachment:focus-visible) { box-shadow: var(--focus-ring); }
.markdown-body :deep(pre:focus-visible), .markdown-body :deep(table:focus-visible) { box-shadow: var(--focus-ring); }
/* Anchored headings: the link sits after the words, quiet until the heading is hovered or it has focus. */
.anchored :deep(.md-heading) { scroll-margin-top: 72px; }
.anchored :deep(.md-anchor) { display: inline-grid; place-items: center; width: 24px; height: 24px; margin-left: 4px; vertical-align: -5px; border-radius: 6px; color: var(--ink-3); text-decoration: none; opacity: 0; }
.anchored :deep(.md-heading:hover .md-anchor), .anchored :deep(.md-anchor:focus-visible) { opacity: 1; }
.anchored :deep(.md-anchor:hover) { color: var(--teal-ink); background: var(--row-hover); }
.anchored :deep(.md-anchor:focus-visible) { box-shadow: var(--focus-ring); }
.anchored :deep(.md-heading.flash) { border-radius: 6px; background: var(--row-selected); box-shadow: 0 0 0 4px var(--row-selected); }
@media (prefers-reduced-motion: no-preference) { .anchored :deep(.md-heading) { transition: background-color .6s ease, box-shadow .6s ease; } }
@media (hover: none) { .anchored :deep(.md-anchor) { opacity: .7; } }
</style>
