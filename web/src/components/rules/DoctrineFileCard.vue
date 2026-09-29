<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, useId } from 'vue'
import BizIcon from '../business/BizIcon.vue'
import { fileName, fileSummary, groupRules, lineLabel, setHeading, sourceText, type DoctrineFile, type DoctrineRule } from '../../lib/doctrine'

// One doctrine file at the pinned commit, as a quiet collapsible row. Open, it
// lists the rules under their headings: the TL;DR from git when there is one,
// then the exact source lines, each linking to those lines at the commit.
const props = defineProps<{ file: DoctrineFile; canPropose?: boolean }>()
const emit = defineEmits<{ propose: [rule: DoctrineRule] }>()
const open = ref(false)
const id = useId()
const groups = computed(() => open.value ? groupRules(props.file) : [])
const unmatched = computed(() => props.file.sidecar?.unmatched ?? [])
</script>

<template>
  <article class="file" :class="{ open }" :aria-labelledby="`${id}-name`">
    <header class="head">
      <button type="button" class="toggle" :aria-expanded="open" :aria-controls="`${id}-rules`" @click="open = !open">
        <BizIcon name="chevron-right" :size="14" class="chev" />
        <span class="names">
          <h4 :id="`${id}-name`" class="name" :title="file.path">{{ fileName(file.path) }}</h4>
          <span v-if="file.tldr" class="tldr" :title="file.tldr.en">{{ file.tldr.en }}</span>
          <span class="summary">{{ file.problem ? 'Not indexed' : fileSummary(file) }}</span>
        </span>
      </button>
      <a class="icon-btn sm flat" :href="file.url" target="_blank" rel="noopener noreferrer" :aria-label="`Open ${fileName(file.path)} at the pinned commit`" data-tip="Open in git"><BizIcon name="external" :size="14" /></a>
    </header>

    <div v-if="open" :id="`${id}-rules`" class="body">
      <p v-if="file.problem" class="quiet">This file could not be indexed: {{ file.problem }}.</p>
      <section v-for="group in groups" :key="group.set.set" class="group" :aria-label="setHeading(group.set.title)">
        <h5 class="heading" :title="group.set.title">{{ setHeading(group.set.title) }}</h5>
        <p v-if="group.set.tldr" class="set-tldr">{{ group.set.tldr.en }}<span v-if="group.set.tldr.check" class="check" data-tip="The rules changed after this TL;DR was written in git."> · may be outdated</span></p>
        <ol class="rules">
          <li v-for="rule in group.rules" :key="`${rule.start_line}-${rule.key}`" class="rule">
            <div class="main">
              <p v-if="rule.tldr" class="rule-tldr">{{ rule.tldr.en }}<span v-if="rule.tldr.check" class="check" data-tip="The rule changed after this TL;DR was written in git."> · may be outdated</span></p>
              <pre class="source" :class="{ quiet: !!rule.tldr }">{{ sourceText(rule) }}</pre>
            </div>
            <div class="rule-actions">
              <button v-if="canPropose" class="btn sm ghost" type="button" @click="emit('propose', rule)">Propose change</button>
              <a class="lines" :href="rule.url" target="_blank" rel="noopener noreferrer" :aria-label="`Open lines ${lineLabel(rule).slice(1)} of ${fileName(file.path)} in git`" :data-tip="`${file.path} ${lineLabel(rule)}`">{{ lineLabel(rule) }}</a>
            </div>
          </li>
        </ol>
      </section>
      <p v-if="file.sidecar?.problem" class="quiet note">
        <a :href="file.sidecar.url" target="_blank" rel="noopener noreferrer">TL;DR file</a> not used: {{ file.sidecar.problem }}.
      </p>
      <p v-else-if="unmatched.length" class="quiet note" :data-tip="unmatched.join(', ')">
        {{ unmatched.length }} {{ unmatched.length === 1 ? 'TL;DR matches' : 'TL;DRs match' }} no rule at this commit.
      </p>
    </div>
  </article>
</template>

<style scoped>
.file { min-width: 0; }
.head { display: flex; align-items: center; gap: 6px; padding: 6px 8px 6px 6px; min-width: 0; }
.toggle { flex: 1; display: flex; align-items: center; gap: 10px; min-width: 0; min-height: 40px; padding: 0 8px; border: 0; border-radius: 10px; background: none; color: inherit; font: inherit; text-align: left; cursor: pointer; }
@media (hover: hover) { .toggle:hover { background: var(--row-hover); } }
.toggle:focus-visible { box-shadow: var(--focus-ring); outline: none; }
.chev { flex: none; color: var(--ink-3); }
.open .chev { transform: rotate(90deg); }
@media (prefers-reduced-motion: no-preference) { .chev { transition: transform .15s ease; } }
.names { display: flex; align-items: baseline; gap: 10px; min-width: 0; flex: 1 1 auto; }
.name { flex: none; margin: 0; font-size: 14px; font-weight: 650; max-width: 60%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tldr { min-width: 0; color: var(--ink-2); font-size: 13px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.summary { flex: none; margin-left: auto; color: var(--ink-3); font-size: 12.5px; white-space: nowrap; font-variant-numeric: tabular-nums; }
.head .icon-btn { flex: none; color: var(--ink-3); }
.body { display: flex; flex-direction: column; gap: 14px; padding: 2px 12px 14px 38px; min-width: 0; }
.quiet { margin: 0; color: var(--ink-3); font-size: 12.5px; line-height: 1.45; }
.note { padding-top: 2px; }
.quiet a { color: inherit; text-decoration: underline; text-underline-offset: 2px; }
.group { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
.heading { margin: 0; color: var(--ink-2); font-size: 12.5px; font-weight: 650; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.set-tldr { margin: 0; color: var(--ink-2); font-size: 13px; line-height: 1.45; }
.rules { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; }
.rule { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 10px; align-items: start; padding: 6px 8px; margin: 0 -8px; border-radius: 10px; }
@media (hover: hover) { .rule:hover { background: var(--row-hover); } }
.main { min-width: 0; display: flex; flex-direction: column; gap: 2px; }
.rule-tldr { margin: 0; font-size: 14px; line-height: 1.5; }
.source { margin: 0; font-family: var(--mono); font-size: 12.5px; line-height: 1.55; color: var(--ink); white-space: pre-wrap; overflow-wrap: anywhere; }
.source.quiet { color: var(--ink-3); font-size: 12px; }
.check { color: var(--ink-3); font-size: 12px; }
.rule-actions { display: flex; align-items: center; flex-wrap: wrap; justify-content: flex-end; gap: 4px; }
.lines { flex: none; padding: 1px 6px; border-radius: 6px; color: var(--ink-3); font-family: var(--mono); font-size: 11.5px; line-height: 20px; text-decoration: none; font-variant-numeric: tabular-nums; }
@media (hover: hover) { .lines:hover { background: var(--surface-2); color: var(--teal-ink); } }
.lines:focus-visible { box-shadow: var(--focus-ring); outline: none; }
@media (max-width: 600px) {
  .rule { grid-template-columns: minmax(0, 1fr); gap: 4px; }
  .rule-actions { justify-content: flex-start; }
  .names { flex-direction: column; align-items: flex-start; gap: 1px; }
  .name, .tldr { max-width: 100%; }
  .summary { margin-left: 0; }
  .toggle { min-height: 52px; }
  .body { padding-left: 12px; }
}
</style>
