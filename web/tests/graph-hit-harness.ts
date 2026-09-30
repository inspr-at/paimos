// SPDX-License-Identifier: AGPL-3.0-only
// AEON-447: mounts the 2D graph renderer alone, so a spec can size the canvas,
// resize it and move its camera without the app around it.
import { createGraphRenderer, type GraphData, type GraphRenderer } from '../src/lib/graphRenderer'

export interface HitHarness { renderer: GraphRenderer; host: HTMLElement; events: string[]; width: number; height: number }
declare global { interface Window { __hit?: HitHarness } }

// `fixed` keeps the initial camera, so nodes can land outside a small canvas.
export async function mountHitGraph(data: GraphData, width: number, height: number, camera: 'fit' | 'fixed') {
  document.querySelector('#app')?.setAttribute('hidden', '')
  const host = document.createElement('div')
  host.id = 'hit-graph'
  host.style.cssText = `position:relative;width:${width}px;height:${height}px`
  document.body.prepend(host)
  const events: string[] = []
  const renderer = await createGraphRenderer(host, '2d', {
    reduced: true, signal: new AbortController().signal,
    select: n => events.push(`select:${n.id}`), open: n => events.push(`open:${n.id}`),
    hover: n => events.push(`hover:${n?.id ?? ''}`), clear: () => events.push('clear'),
  })
  if (!renderer) throw new Error('the graph renderer did not start')
  window.__hit = { renderer, host, events, width, height }
  renderer.motion(true)
  renderer.data(data)
  if (camera === 'fixed') renderer.interact()
  renderer.resize(width, height)
}

// The same 1px change an app layout shift makes.
export function resizeHitGraph(dw: number, dh: number) {
  const hit = window.__hit!
  hit.width += dw; hit.height += dh
  hit.host.style.width = `${hit.width}px`; hit.host.style.height = `${hit.height}px`
  hit.renderer.resize(hit.width, hit.height)
  return { settled: hit.host.dataset.settled, pickable: [...hit.host.querySelectorAll<HTMLElement>('[data-node-id]')].map(el => el.dataset.nodePickable) }
}

export function moveHitCamera(id: string) {
  const hit = window.__hit!
  hit.renderer.focus(id)
  return { settled: hit.host.dataset.settled, pickable: [...hit.host.querySelectorAll<HTMLElement>('[data-node-id]')].map(el => el.dataset.nodePickable) }
}
