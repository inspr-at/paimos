<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import type { ProjectPerson } from '../../lib/api'
import Avatar from '../Avatar.vue'

// The people most recently active in a project, newest first, as a small stack
// of avatars; the rest as "+N". The names are the tooltip and what screen
// readers hear.
const props = withDefaults(defineProps<{ people: ProjectPerson[]; size?: number; max?: number }>(), { size: 22, max: 4 })
const shown = computed(() => props.people.slice(0, props.max))
const more = computed(() => Math.max(0, props.people.length - props.max))
const names = computed(() => props.people.map(p => p.name).join(', '))
</script>

<template>
  <span v-if="people.length" class="people" :style="{ '--size': `${size}px` }" :data-tip="`Recently active: ${names}`">
    <span class="sr-only">Recently active: {{ names }}</span>
    <span class="faces" aria-hidden="true">
      <Avatar v-for="person in shown" :id="person.id" :key="person.id" class="face" :name="person.name" :kind="person.kind" :size="size" />
      <span v-if="more" class="more mono">+{{ more }}</span>
    </span>
  </span>
</template>

<style scoped>
.people { display: inline-flex; align-items: center; min-width: 0; }
.faces { display: inline-flex; align-items: center; }
/* Each face sits a little over the one before, on a ring of the surface colour. */
/* The newest face is on top; the others peek out from behind it. A face is one */
/* opaque unit (disc, picture, rim and ring), so nothing behind shows through it. */
.face { --avatar-base: var(--surface-raised); position: relative; box-shadow: 0 0 0 2px var(--surface-raised); }
.face + .face { margin-left: calc(var(--size) * -.2); }
.face:nth-child(1) { z-index: 4; } .face:nth-child(2) { z-index: 3; } .face:nth-child(3) { z-index: 2; } .face:nth-child(4) { z-index: 1; }
.more { display: inline-grid; place-items: center; min-width: var(--size); height: var(--size); margin-left: calc(var(--size) * -.2); padding: 0 5px; border-radius: 999px; background: var(--surface-2); box-shadow: 0 0 0 2px var(--surface-raised); color: var(--ink-2); font-size: 10.5px; font-weight: 600; }
</style>
