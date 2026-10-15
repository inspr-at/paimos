// SPDX-License-Identifier: AGPL-3.0-only
// Browser harness for TG1's public component contract; no knowledge API/types.
import { createApp, h, ref } from 'vue'
import { createRouter, createMemoryHistory } from 'vue-router'
import GraphCanvas from '../src/components/graph/GraphCanvas.vue'
import type { GraphData, GraphFPS, GraphNode } from '../src/lib/graphRenderer'

export async function mountTicketGraph() {
  document.querySelector('#app')?.setAttribute('hidden', '')
  const host = document.createElement('div'); host.id = 'ticket-graph-harness'; document.body.append(host)
  const data: GraphData = { nodes: [
    { id: 'ticket-194', label: 'Reusable graph', group: 'doing', color: '--kind-ticket', weight: 1, href: '/p/AEON/AEON-194' },
    { id: 'ticket-tg1', label: 'Ticket graph', group: 'backlog', color: '--kind-memory', weight: 1 },
  ], links: [{ source: 'ticket-194', target: 'ticket-tg1', kind: 'blocks', directed: true }] }
  const fps = ref<GraphFPS>(30), selected = ref(''), opened = ref('')
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: { render: () => null } }] })
  await router.push('/')
  const app = createApp({ render: () => h('div', [
    h(GraphCanvas, { data, fps: fps.value, title: 'Ticket graph', viewerKey: 'test:ticket-viewer', selectedId: selected.value,
      'onUpdate:fps': (value: GraphFPS) => { fps.value = value }, onSelect: (node: GraphNode) => { selected.value = node.id }, onOpen: (node: GraphNode) => { opened.value = node.href ?? node.id } }),
    h('output', { 'aria-label': 'Opened ticket' }, opened.value),
    h('button', { onClick: () => { fps.value = 60 } }, 'Set parent FPS'),
  ]) }).use(router)
  app.mount(host)
}

// A glimpse in a fixed box, like the header backdrop: the renderer must take
// this box's size, not the window's, however late its engine arrives.
export async function mountGlimpseGraph() {
  document.querySelector('#app')?.setAttribute('hidden', '')
  const host = document.createElement('div'); host.id = 'glimpse-graph-harness'
  host.style.cssText = 'position: relative; width: 480px; height: 140px;'
  document.body.append(host)
  const data: GraphData = { nodes: [
    { id: 'ticket-1', label: 'One', group: 'doing', color: '--kind-ticket', weight: 1 },
    { id: 'ticket-2', label: 'Two', group: 'backlog', color: '--kind-ticket', weight: 1 },
    { id: 'ticket-3', label: 'Three', group: 'backlog', color: '--kind-ticket', weight: 1 },
  ], links: [{ source: 'ticket-1', target: 'ticket-2', kind: 'blocks', directed: true }] }
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: { render: () => null } }] })
  await router.push('/')
  createApp({ render: () => h(GraphCanvas, { data, glimpse: true, layoutBias: 'elliptic', canvasClass: 'glimpse-harness-canvas' }) }).use(router).mount(host)
}
