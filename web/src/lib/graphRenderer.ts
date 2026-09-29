// SPDX-License-Identifier: AGPL-3.0-only
// TG1 integration: pass immutable GraphData to GraphCanvas (or this factory).
// The renderer owns layout copies, GPU resources and its capped animation loop.
// No API, router, knowledge/ticket schema or session dependency belongs here.
import type { ForceGraph3DInstance } from '3d-force-graph'
import type ForceGraph2D from 'force-graph'
import type { Group, MeshPhysicalMaterial, PerspectiveCamera, SphereGeometry } from 'three'
import type { OrbitControls } from 'three/examples/jsm/controls/OrbitControls.js'
import { graphOrbitPace, graphOrbitSeconds } from './graphOrbitState.ts'

export type GraphDimension = '2d' | '3d'
export type GraphLabels = 'off' | 'smart' | 'all'
export type GraphFPS = 30 | 60
export type MotionPhase = 'paused' | 'interacting' | 'orbiting'
export interface GraphNode { id: string; label: string; group: string; color: `--${string}`; weight: number; href?: string }
export interface GraphLink {
  source: string; target: string; kind: string; directed: boolean
  // Optional domain styling. Omission preserves the knowledge graph defaults.
  color?: `--${string}`; width?: number; curvature?: number
}
export interface GraphData { nodes: readonly GraphNode[]; links: readonly GraphLink[] }
export interface LayoutNode extends GraphNode { degree: number; x?: number; y?: number; z?: number; vx?: number; vy?: number; vz?: number; fx?: number; fy?: number; fz?: number }
export interface LayoutEdge extends Omit<GraphLink, 'source' | 'target'> { source: string | LayoutNode; target: string | LayoutNode }
export interface GraphEmphasis { selected: string; neighbours: Set<string>; matches: Set<string>; searching: boolean; hovered: string }
export interface GraphRenderer {
  dimension: GraphDimension
  data(value: GraphData): void
  emphasis(value: GraphEmphasis): void
  theme(): void
  resize(width: number, height: number): void
  fit(): void
  focus(id: string): void
  motion(paused: boolean): void
  labels(mode: GraphLabels): void
  frameRate(fps: GraphFPS): void
  interact(): void
  dispose(): void
}
export interface GraphRendererOptions {
  reduced: boolean; signal: AbortSignal; fps?: GraphFPS; labels?: GraphLabels
  // Fitted decoration: no labels or pointer handling, a bounded orbit and a
  // transparent clear. The host reserves a region clear of its text.
  glimpse?: boolean
  // Optional force anchors for a wide, volumetric cloud; normal graphs retain
  // their existing clustered layout. This changes physics, never projection.
  layoutBias?: 'elliptic'
  select(node: GraphNode): void; open(node: GraphNode): void; hover(node: GraphNode | null): void; clear(): void
  motionState?(phase: MotionPhase): void
  // Fired when the current label mode has been placed, not merely requested.
  labelsSettled?(ready: boolean): void
}
type Graph3D = ForceGraph3DInstance<LayoutNode, LayoutEdge>
type Graph2D = ForceGraph2D<LayoutNode, LayoutEdge>
export const endpointID = (endpoint: string | LayoutNode) => typeof endpoint === 'string' ? endpoint : endpoint.id
export const graphRadius = (node: GraphNode) => 8 * Math.sqrt(Math.max(0, Number.isFinite(node.weight) ? node.weight : 0) + 1)
export const glimpseRadius = (node: GraphNode) => Math.min(4, Math.max(2, graphRadius(node) * .22))
// Trim the outer 15% in normalised 3D space. Medians resist distant satellites,
// and per-axis spread preserves an ellipsoid rather than favouring its short axis.
export function glimpseCore(nodes: readonly LayoutNode[]): LayoutNode[] {
  if (!nodes.length) return []
  const axes = ['x', 'y', 'z'] as const
  const median = (values: number[]) => values.sort((a, b) => a - b)[Math.floor(values.length / 2)]
  const centre = axes.map(axis => median(nodes.map(n => n[axis] ?? 0)))
  const spread = axes.map((axis, i) => Math.max(1, median(nodes.map(n => Math.abs((n[axis] ?? 0) - centre[i])))))
  const distance = (n: LayoutNode) => axes.reduce((sum, axis, i) => sum + ((n[axis] ?? 0) - centre[i]) ** 2 / spread[i] ** 2, 0)
  return [...nodes].sort((a, b) => distance(a) - distance(b)).slice(0, Math.ceil(nodes.length * .85))
}
// A coprime stride scatters adjacent tickets through the cloud without dropping
// or repeating anchors for particular node counts (including multiples of 37).
export function cloudStride(count: number): number {
  const gcd = (a: number, b: number): number => b ? gcd(b, a % b) : a
  let stride = Math.max(1, Math.round(count * .618))
  while (count > 1 && gcd(stride, count) !== 1) stride++
  return stride
}
// A link is drawn only when both complete orbs fit inside the padded view.
export function glimpsePointInside(x: number, y: number, radius: number, width: number, height: number): boolean {
  return Number.isFinite(x) && Number.isFinite(y) && x - radius >= 8 && y - radius >= 8 && x + radius <= width - 8 && y + radius <= height - 8
}
export function graphLayout(data: GraphData): { nodes: LayoutNode[]; links: LayoutEdge[] } {
  const degrees = new Map<string, number>()
  for (const link of data.links) for (const id of [link.source, link.target]) degrees.set(id, (degrees.get(id) ?? 0) + 1)
  return { nodes: data.nodes.map(n => ({ ...n, degree: degrees.get(n.id) ?? 0 })), links: data.links.map(l => ({ ...l })) }
}
export interface LabelBox { x: number; y: number; w: number; h: number }
const overlaps = (a: LabelBox, b: LabelBox) => Math.abs(a.x - b.x) < (a.w + b.w) / 2 + 4 && Math.abs(a.y - b.y) < (a.h + b.h) / 2 + 3
const LABEL_FADE = 'opacity 0.7s ease-in-out'
const LABEL_FADE_MS = 700
// Critically damped: a placement change settles inside the fade, with no overshoot.
const LABEL_OMEGA = 14
function damp(x: number, v: number, target: number, dt: number, omega: number) {
  if (dt <= 0) return { x, v }
  const exp = Math.exp(-omega * dt)
  const change = x - target
  const temp = (v + omega * change) * dt
  let next = target + (change + temp) * exp
  let velocity = (v - omega * temp) * exp
  if ((x - target) * (next - target) < 0 || Math.abs(next - target) < 0.35) { next = target; velocity = 0 }
  return { x: next, v: velocity }
}
function labelFits(box: LabelBox, placed: LabelBox[], width: number, height: number) {
  const inside = box.x - box.w / 2 >= 2 && box.x + box.w / 2 <= width - 2 && box.y - box.h / 2 >= 2 && box.y + box.h / 2 <= height - 2
  return inside && !placed.some(other => overlaps(box, other))
}
// Deterministic screen-space greedy placement; try below/above/right/left and
// diagonals. All shows every label; Smart gives connected hubs first choice.
export function labelPosition(x: number, y: number, radius: number, w: number, h: number, placed: LabelBox[], width: number, height: number, always: boolean): LabelBox | undefined {
  const dx = radius + w / 2 + 7, dy = radius + h / 2 + 7
  const candidates = [[0, dy], [0, -dy], [dx, 0], [-dx, 0], [dx * .8, dy], [-dx * .8, -dy], [dx * .8, -dy], [-dx * .8, dy]]
    .map(([ox, oy]) => ({ x: x + ox, y: y + oy, w, h }))
  const inside = (b: LabelBox) => b.x - w / 2 >= 2 && b.x + w / 2 <= width - 2 && b.y - h / 2 >= 2 && b.y + h / 2 <= height - 2
  return candidates.find(b => inside(b) && !placed.some(p => overlaps(b, p))) ?? (always ? {
    x: Math.max(w / 2 + 2, Math.min(width - w / 2 - 2, x)), y: Math.max(h / 2 + 2, Math.min(height - h / 2 - 2, y + dy)), w, h,
  } : undefined)
}
function graphPalette(nodes: LayoutNode[], links: LayoutEdge[] = []) {
  const css = getComputedStyle(document.documentElement), read = (token: string) => css.getPropertyValue(token).trim()
  return { background: read('--canvas'), ink: read('--ink'), muted: read('--ink-3'), dark: css.colorScheme === 'dark',
    colors: Object.fromEntries([...nodes.map(n => n.color), ...links.flatMap(l => l.color ? [l.color] : [])].map(token => [token, read(token) || read('--teal')])) }
}

export async function createGraphRenderer(host: HTMLElement, dimension: GraphDimension, options: GraphRendererOptions): Promise<GraphRenderer | null> {
  void import('./graphMotion.ts').then(mod => mod.ensureGraphMotion())
  let three: typeof import('three') | undefined, g3: Graph3D | undefined, g2: Graph2D | undefined
  let geometry: SphereGeometry | undefined
  const materials = new Map<string, MeshPhysicalMaterial>()
  const objects = new Map<string, Group>()
  let nodes: LayoutNode[] = [], links: LayoutEdge[] = [], labelOrder: LayoutNode[] = []
  let palette = graphPalette(nodes), disposed = false, paused = options.reduced
  let emphasis: GraphEmphasis = { selected: '', neighbours: new Set(), matches: new Set(), searching: false, hovered: '' }
  const glimpse = options.glimpse === true
  let labelMode = glimpse ? 'off' : (options.labels ?? 'smart'), fps = options.fps ?? 60
  let paintFrame = 0, fitFrame = 0, frame = 0, lastFrame = 0, driftTime = 0, resumeAt = 0, dragging = false
  let lastPhase: MotionPhase | undefined
  let labelsWereSettled: boolean | undefined
  let settleTimer: ReturnType<typeof setTimeout> | undefined, pickTimer: ReturnType<typeof setTimeout> | undefined
  let cameraTaken = false, fitOnSettle = true
  let glimpseLaidOut = false
  // A contrast test sets window.__aeonGlimpsePin and calls __aeonReleaseGlimpsePin
  // once layout is stable. Real frames hold until that release, then one fixed
  // clock is drawn and kept. Production leaves both unset.
  let glimpsePinned = false, glimpsePinPending = false, glimpsePinReleased = false, glimpseLayoutReady = false, glimpsePinInstalled = false
  type GlimpsePinWindow = Window & { __aeonGlimpsePin?: { frames?: number }; __aeonReleaseGlimpsePin?: () => void }
  let anchorStride = 1
  let pointerNode: LayoutNode | null = null, openedAt = -Infinity
  let lastPick: { node: LayoutNode; x: number; y: number; at: number } | null = null
  const duration = () => glimpse || options.reduced || paused ? 0 : 650
  const active = (n: LayoutNode) => (!emphasis.selected || emphasis.neighbours.has(n.id)) && (!emphasis.searching || emphasis.matches.has(n.id))
  const alpha = (n: LayoutNode) => active(n) ? 1 : .14
  const color = (n: LayoutNode) => palette.colors[n.color] ?? palette.muted
  const touches = (l: LayoutEdge) => !!emphasis.selected && [endpointID(l.source), endpointID(l.target)].includes(emphasis.selected)
  const particles = (l: LayoutEdge) => !paused && l.directed && touches(l) ? 1 : 0
  const linkColor = (l: LayoutEdge) => {
    if (glimpse) return /^#[\da-f]{6}$/i.test(palette.muted) ? palette.muted + '38' : palette.muted
    const color = l.color ? palette.colors[l.color] ?? palette.muted : palette.muted
    return /^#[\da-f]{6}$/i.test(color) ? color + (touches(l) ? 'aa' : emphasis.selected || emphasis.searching ? '18' : l.color ? '80' : palette.dark ? '48' : '40') : color
  }
  const labelLayer = document.createElement('div')
  labelLayer.className = 'graph-labels'; labelLayer.setAttribute('aria-hidden', 'true')
  interface LabelEntry {
    el: HTMLSpanElement; w: number; h: number
    ox: number; oy: number; vx: number; vy: number; tox: number; toy: number
    want: boolean; anchored: boolean; opacityReady: boolean; fadeDoneAt: number
  }
  const labels = new Map<string, LabelEntry>()
  let labelClock = 0
  const prefersReduced = () => options.reduced || window.matchMedia('(prefers-reduced-motion: reduce)').matches
  function measureLabel(label: LabelEntry) {
    if (label.w > 0) return
    const hidden = label.el.hidden
    if (hidden) label.el.hidden = false
    label.w = label.el.offsetWidth
    label.h = label.el.offsetHeight
    if (hidden) label.el.hidden = true
  }
  function fadeTo(label: LabelEntry, opacity: string, reduced: boolean) {
    const show = opacity !== '0'
    if (show && label.el.hidden) {
      label.el.hidden = false
      label.el.style.transition = 'none'
      label.el.style.opacity = '0'
      void label.el.offsetWidth
    }
    if (!show && label.el.hidden) { label.opacityReady = true; return }
    const transition = reduced ? 'none' : LABEL_FADE
    if (label.el.style.transition !== transition) label.el.style.transition = transition
    if (label.el.style.opacity !== opacity) {
      label.el.style.opacity = opacity
      label.opacityReady = reduced
      label.fadeDoneAt = performance.now() + (reduced ? 0 : LABEL_FADE_MS)
    } else if (reduced) label.opacityReady = true
    if (!label.opacityReady && performance.now() >= label.fadeDoneAt) label.opacityReady = true
    if (!show && label.opacityReady) label.el.hidden = true
  }
  function rebuildLabels() {
    if (glimpse) return
    labelLayer.replaceChildren(); labels.clear(); labelClock = 0
    const fragment = document.createDocumentFragment()
    const transition = prefersReduced() ? 'none' : LABEL_FADE
    for (const n of nodes) {
      const el = document.createElement('span')
      el.className = 'graph-label'
      el.hidden = true
      el.textContent = n.label.length > 34 ? n.label.slice(0, 31) + '…' : n.label
      el.style.opacity = '0'
      el.style.transition = transition
      fragment.append(el)
      labels.set(n.id, { el, w: 0, h: 0, ox: 0, oy: 0, vx: 0, vy: 0, tox: 0, toy: 0, want: false, anchored: false, opacityReady: true, fadeDoneAt: 0 })
    }
    labelLayer.append(fragment)
    for (const label of labels.values()) measureLabel(label)
  }
  function orderLabels() {
    labelOrder = [...nodes].sort((a, b) => Number(b.id === emphasis.selected) - Number(a.id === emphasis.selected) || Number(b.id === emphasis.hovered) - Number(a.id === emphasis.hovered) || b.degree - a.degree || a.id.localeCompare(b.id))
  }
  function sceneColor() { return glimpse ? 'rgba(0,0,0,0)' : palette.background }
  function clearGlimpse() { if (glimpse) g3?.renderer().setClearAlpha(0) }
  function glimpseLinkVisible(link: LayoutEdge) {
    if (!glimpse) return true
    if (!glimpseLaidOut) return false
    return [link.source, link.target].every(n => {
      if (typeof n === 'string') return false
      const p = g3 ? g3.graph2ScreenCoords(n.x ?? 0, n.y ?? 0, n.z ?? 0) : g2!.graph2ScreenCoords(n.x ?? 0, n.y ?? 0)
      return glimpsePointInside(p.x, p.y, glimpseRadius(n), graph.width(), graph.height())
    })
  }
  function sizeGlimpseOrbs() {
    if (!glimpse || !g3 || !three || !glimpseLaidOut) return
    const camera = g3.camera() as PerspectiveCamera
    const projection = g3.height() / (2 * Math.tan(camera.fov * Math.PI / 360))
    for (const n of nodes) {
      const depth = -new three.Vector3(n.x ?? 0, n.y ?? 0, n.z ?? 0).applyMatrix4(camera.matrixWorldInverse).z
      const object = objects.get(n.id)
      if (object) { object.visible = depth > camera.near; object.scale.setScalar(glimpseRadius(n) * Math.max(0, depth) / (projection * graphRadius(n))) }
    }
  }
  function placeLabels(dt: number) {
    if (glimpse) return
    const graph = g3 ?? g2
    if (!graph || disposed) return
    const placed: LabelBox[] = [], width = graph.width(), height = graph.height()
    const camera = g3?.camera() as PerspectiveCamera | undefined
    const projected = new Map<string, { x: number; y: number; radius: number }>()
    for (const n of nodes) {
      const screen = g3 ? g3.graph2ScreenCoords(n.x ?? 0, n.y ?? 0, n.z ?? 0) : g2!.graph2ScreenCoords(n.x ?? 0, n.y ?? 0)
      if (!Number.isFinite(screen.x) || screen.x < 0 || screen.x > width || screen.y < 0 || screen.y > height) continue
      let radius = graphRadius(n) * (g2?.zoom() ?? 1)
      if (camera && three) {
        const world = new three.Vector3(n.x ?? 0, n.y ?? 0, n.z ?? 0), projected = world.clone().project(camera)
        if (projected.z < -1 || projected.z > 1) continue
        // Perspective radius depends on camera-space depth, not radial distance.
        const depth = -world.applyMatrix4(camera.matrixWorldInverse).z
        radius = graphRadius(n) * height / (2 * depth * Math.tan(camera.fov * Math.PI / 360))
      }
      projected.set(n.id, { ...screen, radius })
      placed.push({ x: screen.x, y: screen.y, w: radius * 2, h: radius * 2 })
    }
    const reduced = prefersReduced()
    for (const n of labelOrder) {
      const label = labels.get(n.id); if (!label) continue
      measureLabel(label)
      const mandatory = n.id === emphasis.selected || n.id === emphasis.hovered
      const screen = projected.get(n.id)
      const wasWanted = label.want
      let box: LabelBox | undefined
      if (screen && (labelMode !== 'off' || mandatory)) {
        // Keep a slot that still fits so orbiting does not retarget every frame.
        if (wasWanted && label.anchored) {
          const kept: LabelBox = { x: screen.x + label.tox + label.w / 2, y: screen.y + label.toy + label.h / 2, w: label.w, h: label.h }
          if (labelFits(kept, placed, width, height)) box = kept
        }
        box ??= labelPosition(screen.x, screen.y, screen.radius, label.w, label.h, placed, width, height, mandatory || labelMode === 'all')
      }
      if (!box || !screen) {
        label.want = false
        if (screen && label.anchored) label.el.style.transform = `translate(${screen.x + label.ox}px, ${screen.y + label.oy}px)`
        fadeTo(label, '0', reduced)
        continue
      }
      const tox = Math.round(box.x - box.w / 2 - screen.x)
      const toy = Math.round(box.y - box.h / 2 - screen.y)
      label.want = true
      label.anchored = true
      label.tox = tox
      label.toy = toy
      // Glide only the offset from the orb. The orb's screen position is applied
      // raw each frame, so a moving camera does not leave the label behind.
      if (!wasWanted || reduced) { label.ox = tox; label.oy = toy; label.vx = 0; label.vy = 0 }
      else if (dt > 0 && (label.ox !== tox || label.oy !== toy || label.vx !== 0 || label.vy !== 0)) {
        const x = damp(label.ox, label.vx, tox, dt, LABEL_OMEGA)
        const y = damp(label.oy, label.vy, toy, dt, LABEL_OMEGA)
        label.ox = x.x; label.vx = x.v; label.oy = y.x; label.vy = y.v
      }
      placed.push(box)
      label.el.style.transform = `translate(${screen.x + label.ox}px, ${screen.y + label.oy}px)`
      label.el.style.zIndex = mandatory ? '2' : '1'
      fadeTo(label, mandatory ? '1' : String(Math.max(0.55, alpha(n))), reduced)
    }
  }
  function labelAtRest(label: LabelEntry) {
    const placed = !label.want || (label.ox === label.tox && label.oy === label.toy)
    return label.opacityReady && placed
  }
  // Settled when the fade and the glide have arrived, not when they were requested.
  function labelSettlement() {
    if (glimpse) return !nodes.length || glimpseLaidOut
    const laidOut = nodes.every(n => n.x !== undefined && n.y !== undefined)
    if (!nodes.length) return true
    if (!laidOut) return false
    let shown = 0
    for (const label of labels.values()) {
      if (!labelAtRest(label)) return false
      if (label.want) shown++
    }
    if (labelMode === 'all') return shown === nodes.length
    if (labelMode === 'off') {
      const must = nodes.some(n => n.id === emphasis.selected || n.id === emphasis.hovered)
      return must ? shown > 0 : shown === 0
    }
    return shown > 0
  }
  function publishLabels() {
    const now = performance.now()
    const dt = labelClock ? Math.min(0.1, (now - labelClock) / 1000) : 0
    labelClock = now
    placeLabels(dt)
    const settled = labelSettlement()
    if (settled === labelsWereSettled) return
    labelsWereSettled = settled
    options.labelsSettled?.(settled)
  }
  function publishPhase(now = performance.now()) {
    const phase: MotionPhase = paused ? 'paused' : dragging || now < resumeAt ? 'interacting' : 'orbiting'
    if (phase === lastPhase) return
    lastPhase = phase
    options.motionState?.(phase)
  }
  function material(n: LayoutNode, shell: boolean) {
    const key = `${shell ? 'shell' : color(n)}:${alpha(n)}`
    if (!materials.has(key)) {
      const m = new three!.MeshPhysicalMaterial({ color: shell ? '#ffffff' : color(n), roughness: shell ? .08 : .28, metalness: 0,
        clearcoat: 1, clearcoatRoughness: .06, transparent: true, opacity: alpha(n) * (shell ? .38 : .86), depthWrite: !shell,
        emissive: shell ? '#ffffff' : color(n), emissiveIntensity: shell ? .08 : .09 })
      if (shell) {
        // Fresnel transparency avoids transmission's extra full-scene render pass.
        m.onBeforeCompile = shader => { shader.fragmentShader = shader.fragmentShader.replace('#include <opaque_fragment>', 'diffuseColor.a *= 0.12 + 0.88 * pow(1.0 - abs(dot(normalize(normal), normalize(vViewPosition))), 2.0);\n#include <opaque_fragment>') }
        m.customProgramCacheKey = () => 'graph-glass-fresnel-v1'
      }
      materials.set(key, m)
    }
    return materials.get(key)!
  }
  function updateObjects() {
    if (!three) return
    for (const n of nodes) {
      const group = objects.get(n.id); if (!group) continue
      ;(group.children[0] as import('three').Mesh).material = material(n, false)
      ;(group.children[1] as import('three').Mesh).material = material(n, true)
    }
  }
  function draw2D(n: LayoutNode, ctx: CanvasRenderingContext2D, scale: number, picking?: string) {
    const x = n.x ?? 0, y = n.y ?? 0, r = glimpse ? glimpseRadius(n) / scale : graphRadius(n)
    ctx.globalAlpha = picking ? 1 : alpha(n)
    ctx.beginPath(); ctx.arc(x, y, r, 0, Math.PI * 2)
    if (picking) { ctx.fillStyle = picking; ctx.fill(); return }
    const shell = ctx.createRadialGradient(x, y, r * .84, x, y, r)
    shell.addColorStop(0, '#ffffff08'); shell.addColorStop(.65, '#ffffff18'); shell.addColorStop(1, palette.dark ? '#ffffff60' : '#b9c8cc80')
    ctx.fillStyle = shell; ctx.fill()
    ctx.beginPath(); ctx.arc(x, y, r * .88, 0, Math.PI * 2)
    ctx.fillStyle = color(n); ctx.fill()
    const highlight = ctx.createRadialGradient(x - r * .36, y - r * .4, 0, x - r * .1, y - r * .1, r * 1.2)
    highlight.addColorStop(0, '#ffffffec'); highlight.addColorStop(.18, '#ffffff85'); highlight.addColorStop(.48, '#ffffff14'); highlight.addColorStop(.83, '#071e331e'); highlight.addColorStop(1, '#ffffff88')
    ctx.fillStyle = highlight; ctx.fill()
    ctx.beginPath(); ctx.arc(x, y, r, 0, Math.PI * 2)
    ctx.lineWidth = .7 / scale; ctx.strokeStyle = palette.dark ? '#ffffff40' : '#b9c8cc70'; ctx.stroke()
    if (n.id === emphasis.selected) { ctx.beginPath(); ctx.arc(x, y, r + 3 / scale, 0, Math.PI * 2); ctx.strokeStyle = color(n); ctx.stroke() }
    ctx.globalAlpha = 1
  }
  const pointerArea = (n: LayoutNode, color: string, ctx: CanvasRenderingContext2D, scale: number) => draw2D(n, ctx, scale, color)
  function refreshPicking(delay: number) {
    clearTimeout(pickTimer)
    pickTimer = setTimeout(() => { if (!disposed) g2?.nodePointerAreaPaint(pointerArea) }, delay)
  }
  function interact() { cameraTaken = true; resumeAt = performance.now() + 5000; clearTimeout(settleTimer) }
  function motion(value: boolean) {
    // The pinned pose ignores later play, occlusion and visibility.
    if (glimpsePinned) {
      paused = true
      ;(g3 ?? g2)?.cooldownTicks(0).linkDirectionalParticles(particles)
      publishPhase()
      redraw()
      return
    }
    paused = value
    if (!value) resumeAt = 0 // Explicit play takes effect immediately, including reduced-motion opt-in.
    const graph = g3 ?? g2
    graph?.cooldownTicks(paused ? 0 : 140).linkDirectionalParticles(particles)
    if (!paused) graph?.d3ReheatSimulation()
    publishPhase()
    redraw()
  }
  function redraw() {
    if (disposed) return
    updateObjects(); orderLabels()
    const graph = g3 ?? g2
    graph?.linkColor(linkColor).linkDirectionalParticles(particles)
    g2?.nodeCanvasObject((n, ctx, scale) => draw2D(n, ctx, scale))
    publishLabels()
  }
  if (dimension === '3d') {
    const modules = await Promise.all([import('3d-force-graph'), import('three')])
    if (options.signal.aborted) return null
    three = modules[1]
    try {
      g3 = new modules[0].default(host, { controlType: 'orbit', rendererConfig: { antialias: true, alpha: true, powerPreference: 'low-power' } }) as unknown as Graph3D
      g3.renderer().setPixelRatio(Math.min(window.devicePixelRatio, 1.5))
      if (glimpse) {
        const camera = g3.camera() as PerspectiveCamera
        camera.fov = 36; camera.updateProjectionMatrix()
        g3.cameraPosition({ x: 90, y: 150, z: 750 }, { x: 0, y: 0, z: 0 }, 0)
      }
      geometry = new three.SphereGeometry(1, 20, 14)
      const key = new three.DirectionalLight('#fff5e9', 2.4); key.position.set(-180, 240, 320)
      const rim = new three.DirectionalLight('#c6e9ff', 2); rim.position.set(180, 40, -120)
      g3.lights([new three.AmbientLight('#ffffff', 1.25), key, rim])
      g3.showNavInfo(false).backgroundColor(sceneColor()).nodeThreeObject(n => {
        const group = new three!.Group(), radius = graphRadius(n)
        const core = new three!.Mesh(geometry, material(n, false)); core.scale.setScalar(radius * .88)
        const shell = new three!.Mesh(geometry, material(n, true)); shell.scale.setScalar(radius)
        group.add(core, shell); objects.set(n.id, group)
        return group
      }).linkOpacity(1).linkWidth(l => glimpse ? 0 : l.width ?? .55)
      const controls = g3.controls() as OrbitControls
      controls.autoRotateSpeed = graphOrbitPace() // Speed 1 is one turn per 60s; the default pace is one turn per 120s.
      controls.enableDamping = !options.reduced
      if (glimpse) { controls.enableRotate = false; controls.enableZoom = false; controls.enablePan = false }
      clearGlimpse()
    } catch {
      const failedRenderer = g3?.renderer(); g3?._destructor(); failedRenderer?.forceContextLoss(); g3 = undefined
      geometry?.dispose(); materials.forEach(m => m.dispose()); materials.clear(); host.replaceChildren()
    }
  }
  if (!g3) {
    const { default: ForceGraph } = await import('force-graph')
    if (options.signal.aborted) return null
    g2 = new ForceGraph<LayoutNode, LayoutEdge>(host)
    g2.backgroundColor(sceneColor()).nodeCanvasObject((n, ctx, scale) => draw2D(n, ctx, scale)).nodePointerAreaPaint(pointerArea).linkWidth(l => glimpse ? .5 : l.width ?? (touches(l) ? 1 : .6))
  }
  if (!glimpse) host.append(labelLayer)
  const graph = (g3 ?? g2)!
  graph.nodeLabel(() => '').linkLabel(() => '').nodeRelSize(8).nodeVal(n => Math.pow(Math.max(0, n.weight) + 1, 1.5))
    .linkColor(linkColor).linkVisibility(glimpseLinkVisible).linkDirectionalParticles(particles).linkDirectionalParticleWidth(1.4).linkDirectionalParticleSpeed(.002)
    .linkDirectionalArrowLength(l => !glimpse && l.directed ? 3 : 0).linkDirectionalArrowRelPos(1).linkCurvature(l => glimpse ? 0 : l.curvature ?? 0)
    .warmupTicks(90).cooldownTicks(paused ? 0 : 140).d3VelocityDecay(.38)
    .onNodeClick((node, event) => {
      if (glimpse || performance.now() - openedAt < 100) return
      lastPick = { node, x: event.clientX, y: event.clientY, at: performance.now() }; options.select(node)
    }).onNodeHover(node => { if (glimpse) return; pointerNode = node; options.hover(node) })
    .onBackgroundClick(() => { if (!glimpse && performance.now() - openedAt >= 100) options.clear() })
    .onNodeDrag(() => { if (!glimpse) interact() }).onNodeDragEnd(() => { if (!glimpse) interact() })
    .onEngineStop(() => { if (fitOnSettle && !cameraTaken && !emphasis.selected) fit(); fitOnSettle = false })
  const pointerDown = () => { dragging = true; interact() }
  const pointerMove = () => { if (dragging) interact() }
  const pointerUp = () => { if (dragging) { dragging = false; interact() } }
  if (!glimpse) {
    host.addEventListener('pointerdown', pointerDown, { passive: true }); host.addEventListener('wheel', interact, { passive: true })
    window.addEventListener('pointermove', pointerMove, { passive: true }); window.addEventListener('pointerup', pointerUp); window.addEventListener('pointercancel', pointerUp)
  }
  function doubleClick(event: MouseEvent) {
    const recent = lastPick && performance.now() - lastPick.at < 600 && Math.hypot(event.clientX - lastPick.x, event.clientY - lastPick.y) < 8
    const node = pointerNode ?? (recent ? lastPick!.node : null)
    if (node) { event.preventDefault(); openedAt = performance.now(); options.open(node) }
  }
  if (!glimpse) host.addEventListener('dblclick', doubleClick)
  graph.d3Force('charge')?.strength(-60)
  // O(n) soft clusters: stable group anchors keep sparse same-group nodes near
  // each other without rigidly partitioning a strongly connected graph.
  let groups = new Map<string, { x: number; y: number; z: number }>()
  graph.d3Force('group-centre', (alpha: number) => {
    const strength = options.layoutBias === 'elliptic' ? .015 : .045
    for (const n of nodes) {
      const at = groups.get(n.group) ?? { x: 0, y: 0, z: 0 }
      n.vx = (n.vx ?? 0) + (at.x - (n.x ?? 0)) * strength * alpha
      n.vy = (n.vy ?? 0) + (at.y - (n.y ?? 0)) * strength * alpha
      if (g3) n.vz = (n.vz ?? 0) + (at.z - (n.z ?? 0)) * strength * alpha
    }
  })
  graph.d3Force('link')?.distance((l: LayoutEdge) => 45 + (typeof l.source === 'object' ? graphRadius(l.source) : 5) + (typeof l.target === 'object' ? graphRadius(l.target) : 5))
  if (options.layoutBias === 'elliptic') {
    graph.d3Force('link')?.strength(.05)
    graph.d3Force('elliptic', (alpha: number) => {
      const radius = 55 + Math.sqrt(nodes.length) * 3
      // The backdrop spans the whole header, including the masked text islands.
      // Spread anchors to the content edges instead of the old middle column.
      const aspect = Math.max(1.4, Math.min(12, graph.width() / Math.max(1, graph.height()) * 1.65))
      nodes.forEach((n, i) => {
        const index = (i * anchorStride) % nodes.length
        const y = 1 - 2 * (index + .5) / nodes.length, angle = i * Math.PI * (3 - Math.sqrt(5))
        const ring = Math.sqrt(1 - y * y)
        const x = Math.cos(angle) * ring * radius * aspect, z = Math.sin(angle) * ring * radius
        n.vx = (n.vx ?? 0) + (x - (n.x ?? 0)) * .12 * alpha
        n.vy = (n.vy ?? 0) + (y * radius - (n.y ?? 0)) * .12 * alpha
        if (g3) n.vz = (n.vz ?? 0) + (z - (n.z ?? 0)) * .12 * alpha
      })
    })
  }
  // Both engines' public resume method draws one synchronous frame, then
  // schedules a RAF; pause cancels that RAF. Own just one clock so 30 FPS caps
  // physics, WebGL, labels and picking together, rather than only the camera.
  graph.pauseAnimation()
  function glimpsePinFrameCount() {
    if (!glimpse || typeof window === 'undefined') return 0
    const requested = (window as GlimpsePinWindow).__aeonGlimpsePin?.frames
    const frames = typeof requested === 'number' ? Math.floor(requested) : 0
    return Number.isFinite(frames) && frames > 0 ? Math.min(frames, 600) : 0
  }
  function renderFrame(now: number, synthetic = false) {
    // Phase follows the click even when this tab is hidden or the frame budget
    // skips the draw. The orbit itself still waits for a visible frame.
    publishPhase(now)
    const phase = lastPhase
    const pace = graphOrbitPace()
    const shownSeconds = String(graphOrbitSeconds())
    if (host.dataset.orbitSeconds !== shownSeconds) host.dataset.orbitSeconds = shownSeconds
    if (g3) {
      const controls = g3.controls() as OrbitControls
      controls.autoRotate = phase === 'orbiting' && pace > 0 && !glimpse
      controls.autoRotateSpeed = pace
    }
    // Real frames hold until the test releases the pin, and after that pose is drawn.
    if (!synthetic && (document.hidden || glimpsePinPending || glimpsePinned)) return
    if (glimpse && (!nodes.length || nodes.some(n => n.x === undefined))) return
    if (now - lastFrame < 1000 / fps - .5) return
    const dt = Math.min(.1, (now - (lastFrame || now)) / 1000); lastFrame = now
    // Pace 1 matches the old drift. Default (120s) is half of that; Off adds none.
    if (phase === 'orbiting' && pace > 0) {
      const before = driftTime
      driftTime += dt * pace
      if (g3 && three && glimpse) {
        // The bounded orbit reveals parallax without turning the wide cloud
        // end-on. Its rate follows the same viewer preference as other graphs.
        const camera = g3.camera(), target = (g3.controls() as OrbitControls).target
        const orbit = new three.Spherical().setFromVector3(camera.position.clone().sub(target))
        orbit.theta += (Math.sin(driftTime / 18) - Math.sin(before / 18)) * .12
        orbit.phi += (Math.cos(driftTime / 24) - Math.cos(before / 24)) * .025
        camera.position.copy(target).add(new three.Vector3().setFromSpherical(orbit))
      } else if (g3) {
        const camera = g3.camera(), controls = g3.controls() as OrbitControls
        camera.position.y += Math.cos(driftTime / 7) * dt * pace * camera.position.distanceTo(controls.target) * .003
      } else if (g2 && !glimpse) {
        const center = g2.centerAt()
        g2.centerAt(center.x + Math.cos(driftTime / 9) * dt * pace * .9, center.y + Math.sin(driftTime / 7) * dt * pace * .6)
      }
    }
    if (disposed) return
    sizeGlimpseOrbs()
    clearGlimpse(); graph.resumeAnimation(); if (disposed) return; graph.pauseAnimation(); publishLabels()
  }
  function tick(now: number) {
    if (disposed) return
    frame = requestAnimationFrame(tick)
    renderFrame(now)
  }
  function maybeRunGlimpsePin() {
    const frames = glimpsePinFrameCount()
    if (!frames || !glimpsePinReleased || !glimpseLayoutReady || glimpsePinned || disposed) return
    runGlimpsePin(frames)
  }
  function runGlimpsePin(frames: number) {
    if (glimpsePinned || disposed) return
    cancelAnimationFrame(fitFrame)
    clearTimeout(settleTimer)
    lastFrame = 0
    driftTime = 0
    const sim = g3 ?? g2
    // Same pose the glimpse camera is created with, so the scripted orbit does not
    // depend on how many real frames ran before the test released the pin.
    if (g3) g3.cameraPosition({ x: 90, y: 150, z: 750 }, { x: 0, y: 0, z: 0 }, 0)
    // The engine also stops on a wall-clock cooldown. A pinned run must stop on tick count alone.
    sim?.cooldownTime(3_600_000)
    sim?.d3ReheatSimulation()
    for (let i = 1; i <= frames; i++) renderFrame(17 * i, true)
    sim?.cooldownTime(15_000)
    glimpsePinPending = false
    glimpsePinned = true
    sim?.cooldownTicks(0)
    host.dataset.glimpsePin = String(frames)
    motion(true)
  }
  function installGlimpsePinRelease() {
    glimpsePinInstalled = true
    ;(window as GlimpsePinWindow).__aeonReleaseGlimpsePin = () => {
      glimpsePinReleased = true
      maybeRunGlimpsePin()
    }
  }
  glimpsePinPending = glimpsePinFrameCount() > 0
  if (glimpsePinPending) installGlimpsePinRelease()
  frame = requestAnimationFrame(tick)
  // Fit fills about 80% of the stage along its tighter side, bubbles included.
  // No closer than 2.2 screen pixels per graph unit, so a few entries stay calm.
  const FILL = .8, MAX_ZOOM = 2.2
  function fit() {
    if (disposed || !nodes.length) return
    if (glimpse && (!glimpseLaidOut || graph.width() < 24 || graph.height() < 24)) return
    if (g3 && three) {
      const camera = g3.camera() as PerspectiveCamera
      const right = new three.Vector3(1, 0, 0).applyQuaternion(camera.quaternion)
      const up = new three.Vector3(0, 1, 0).applyQuaternion(camera.quaternion)
      const back = new three.Vector3(0, 0, 1).applyQuaternion(camera.quaternion)
      const fitted = glimpse ? glimpseCore(nodes) : nodes
      const span = (axis: import('three').Vector3) => {
        let low = Infinity, high = -Infinity
        for (const n of fitted) { const d = axis.x * (n.x ?? 0) + axis.y * (n.y ?? 0) + axis.z * (n.z ?? 0), r = glimpse ? 0 : graphRadius(n); low = Math.min(low, d - r); high = Math.max(high, d + r) }
        return (low + high) / 2
      }
      const centre = right.clone().multiplyScalar(span(right)).add(up.clone().multiplyScalar(span(up))).add(back.clone().multiplyScalar(span(back)))
      const tan = Math.tan(camera.fov * Math.PI / 360), aspect = Math.max(1, g3.width()) / Math.max(1, g3.height())
      let distance = glimpse ? 1 : g3.height() / (2 * tan * MAX_ZOOM)
      for (const n of fitted) {
        const v = new three.Vector3(n.x ?? 0, n.y ?? 0, n.z ?? 0).sub(centre), depth = v.dot(back)
        // Fit only the dense core's HEIGHT in a glimpse. Outliers and sides
        // can enter the fade without shrinking the entire cloud into a strip.
        if (glimpse) {
          distance = Math.max(distance, depth + Math.abs(v.dot(up)) / (tan * (1.20 - 8 / g3.height())))
        }
        else {
          const r = graphRadius(n)
          distance = Math.max(distance, depth + (Math.abs(v.dot(up)) + r) / (tan * FILL), depth + (Math.abs(v.dot(right)) + r) / (tan * aspect * FILL))
        }
      }
      const at = centre.clone().add(back.multiplyScalar(distance))
      g3.cameraPosition({ x: at.x, y: at.y, z: at.z }, { x: centre.x, y: centre.y, z: centre.z }, duration())
      publishLabels()
    } else if (g2) {
      let x0 = Infinity, x1 = -Infinity, y0 = Infinity, y1 = -Infinity
      for (const n of glimpse ? glimpseCore(nodes) : nodes) { const r = glimpse ? 0 : graphRadius(n), x = n.x ?? 0, y = n.y ?? 0; x0 = Math.min(x0, x - r); x1 = Math.max(x1, x + r); y0 = Math.min(y0, y - r); y1 = Math.max(y1, y + r) }
      const zoom = glimpse ? Math.max(1, g2.height() * 1.15 - 8) / Math.max(1, y1 - y0) : Math.min(MAX_ZOOM, g2.width() * FILL / Math.max(1, x1 - x0), g2.height() * FILL / Math.max(1, y1 - y0))
      g2.centerAt((x0 + x1) / 2, (y0 + y1) / 2, duration()); g2.zoom(zoom, duration()); refreshPicking(duration())
      publishLabels()
    }
    if (glimpse) { sizeGlimpseOrbs(); graph.linkVisibility(glimpseLinkVisible); host.style.visibility = '' }
  }
  // A selection sits in the middle with its direct links in view (80% of the stage),
  // no closer than 1.8 screen pixels per graph unit.
  const FOCUS_ZOOM = 1.8
  function focus(id: string) {
    clearTimeout(settleTimer)
    const node = nodes.find(n => n.id === id); if (!node) return
    const around = links.flatMap(l => endpointID(l.source) === id ? [endpointID(l.target)] : endpointID(l.target) === id ? [endpointID(l.source)] : [])
      .map(other => nodes.find(n => n.id === other)).filter((n): n is LayoutNode => !!n)
    const x = node.x ?? 0, y = node.y ?? 0, z = node.z ?? 0
    if (g3 && three) {
      const camera = g3.camera() as PerspectiveCamera
      const back = new three.Vector3(camera.position.x - x, camera.position.y - y, camera.position.z - z)
      if (back.lengthSq() < 1e-6) back.set(0, 0, 1)
      back.normalize()
      const right = new three.Vector3().crossVectors(new three.Vector3(0, 1, 0).applyQuaternion(camera.quaternion), back).normalize()
      const up = new three.Vector3().crossVectors(back, right).normalize()
      const tan = Math.tan(camera.fov * Math.PI / 360), aspect = Math.max(1, g3.width()) / Math.max(1, g3.height())
      let distance = g3.height() / (2 * tan * FOCUS_ZOOM)
      for (const n of around) {
        const v = new three.Vector3((n.x ?? 0) - x, (n.y ?? 0) - y, (n.z ?? 0) - z), r = graphRadius(n), depth = v.dot(back)
        distance = Math.max(distance, depth + (Math.abs(v.dot(up)) + r) / (tan * FILL), depth + (Math.abs(v.dot(right)) + r) / (tan * aspect * FILL))
      }
      g3.cameraPosition({ x: x + back.x * distance, y: y + back.y * distance, z: z + back.z * distance }, { x, y, z }, duration())
      publishLabels()
    } else if (g2) {
      let zoom = FOCUS_ZOOM
      for (const n of around) {
        const r = graphRadius(n)
        zoom = Math.min(zoom, g2.width() * FILL / 2 / (Math.abs((n.x ?? 0) - x) + r), g2.height() * FILL / 2 / (Math.abs((n.y ?? 0) - y) + r))
      }
      g2.centerAt(x, y, duration()); g2.zoom(Math.max(.2, zoom), duration()); refreshPicking(duration())
      publishLabels()
    }
  }
  // The engines lay out and build their objects a moment after new data. Wait for both
  // (bounded), so labels attach and the camera frames real positions rather than the origin.
  function whenLaidOut(run: () => void, frames = 90) {
    cancelAnimationFrame(paintFrame)
    paintFrame = requestAnimationFrame(() => {
      if (disposed) return
      const ready = nodes.every(n => n.x !== undefined) && (!g3 || nodes.every(n => objects.has(n.id)))
      if (!ready && frames > 0) whenLaidOut(run, frames - 1)
      else run()
    })
  }
  return {
    dimension: g3 ? '3d' : '2d',
    data(value) {
      pointerNode = null; lastPick = null; cameraTaken = false; fitOnSettle = true
      glimpseLaidOut = false
      glimpseLayoutReady = false
      glimpsePinned = false
      if (glimpsePinFrameCount()) { glimpsePinPending = true; delete host.dataset.glimpsePin }
      if (glimpse) host.style.visibility = 'hidden'
      objects.clear()
      const layout = graphLayout(value); nodes = layout.nodes; links = layout.links
      anchorStride = cloudStride(nodes.length)
      palette = graphPalette(nodes, links)
      const names = [...new Set(nodes.map(n => n.group))].sort(), radius = names.length > 1 ? 35 + Math.sqrt(nodes.length) * 9 : 0
      groups = new Map(names.map((group, i) => { const angle = i * 2 * Math.PI / names.length; return [group, { x: Math.cos(angle) * radius, y: Math.sin(angle) * radius, z: Math.sin(angle * 2) * radius * .3 }] }))
      labelsWereSettled = undefined
      options.labelsSettled?.(false)
      graph.graphData({ nodes, links }); rebuildLabels(); redraw()
      clearTimeout(settleTimer)
      whenLaidOut(() => {
        if (glimpse) glimpseLaidOut = true
        redraw(); if (emphasis.selected) focus(emphasis.selected); else if (!cameraTaken) fit(); publishLabels()
        glimpseLayoutReady = true
        maybeRunGlimpsePin()
      })
      if (!paused && !glimpsePinPending) settleTimer = setTimeout(() => { if (emphasis.selected) focus(emphasis.selected); else if (!cameraTaken) fit() }, 1200)
    },
    emphasis(value) { emphasis = value; redraw() },
    theme() {
      palette = graphPalette(nodes, links); graph.backgroundColor(sceneColor()); clearGlimpse()
      materials.forEach(m => m.dispose()); materials.clear(); redraw()
    },
    resize(width, height) {
      const changed = graph.width() !== width || graph.height() !== height
      graph.width(Math.max(1, width)).height(Math.max(1, height)); cancelAnimationFrame(fitFrame)
      if (changed && options.layoutBias === 'elliptic' && glimpseLaidOut && !glimpsePinPending && !glimpsePinned) { fitOnSettle = true; graph.d3ReheatSimulation() }
      const node = g2 && emphasis.selected ? nodes.find(n => n.id === emphasis.selected) : undefined
      if (node) fitFrame = requestAnimationFrame(() => { g2?.centerAt(node.x ?? 0, node.y ?? 0); refreshPicking(0) })
      else if (!glimpsePinned && !cameraTaken && !emphasis.selected) fitFrame = requestAnimationFrame(fit)
    },
    fit, focus, motion, interact,
    labels(mode) { if (glimpse) return; labelMode = mode; labelsWereSettled = undefined; publishLabels() },
    frameRate(value) { fps = value },
    dispose() {
      if (disposed) return
      disposed = true
      if (glimpsePinInstalled && typeof window !== 'undefined') delete (window as GlimpsePinWindow).__aeonReleaseGlimpsePin
      cancelAnimationFrame(frame); cancelAnimationFrame(paintFrame); cancelAnimationFrame(fitFrame); clearTimeout(settleTimer); clearTimeout(pickTimer)
      host.removeEventListener('dblclick', doubleClick); host.removeEventListener('pointerdown', pointerDown); host.removeEventListener('wheel', interact)
      window.removeEventListener('pointermove', pointerMove); window.removeEventListener('pointerup', pointerUp); window.removeEventListener('pointercancel', pointerUp)
      graph.onNodeHover(() => {}).onNodeClick(() => {}).onBackgroundClick(() => {}).onNodeDrag(() => {}).onNodeDragEnd(() => {}).onEngineStop(() => {}).pauseAnimation()
      const renderer = g3?.renderer()
      const gl = renderer?.getContext()
      const canvas = renderer?.domElement ?? null
      graph._destructor(); geometry?.dispose(); materials.forEach(m => m.dispose()); materials.clear(); objects.clear(); labels.clear()
      // _destructor drops three's context-restored listener first. loseContext
      // then posts webglcontextlost for a later turn; dispatch it now so the
      // context is released before this canvas leaves the document. A once-only
      // listener ignores the browser's later copy of the same event.
      renderer?.forceContextLoss()
      if (gl && canvas) canvas.dispatchEvent(new Event('webglcontextlost', { cancelable: true }))
      nodes = []; links = []; host.replaceChildren()
    },
  }
}
