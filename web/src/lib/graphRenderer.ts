// SPDX-License-Identifier: AGPL-3.0-only
// TG1 integration: pass immutable GraphData to GraphCanvas (or this factory).
// The renderer owns layout copies, GPU resources and its capped animation loop.
// No API, router, knowledge/ticket schema or session dependency belongs here.
import type { ForceGraph3DInstance } from '3d-force-graph'
import type ForceGraph2D from 'force-graph'
import type { Group, MeshPhysicalMaterial, PerspectiveCamera, SphereGeometry } from 'three'
import type { OrbitControls } from 'three/examples/jsm/controls/OrbitControls.js'

export type GraphDimension = '2d' | '3d'
export type GraphLabels = 'off' | 'smart' | 'all'
export type GraphFPS = 30 | 60
export type MotionPhase = 'paused' | 'interacting' | 'orbiting'
export interface GraphNode { id: string; label: string; group: string; color: `--${string}`; weight: number; href?: string }
export interface GraphLink { source: string; target: string; kind: string; directed: boolean }
export interface GraphData { nodes: readonly GraphNode[]; links: readonly GraphLink[] }
export interface LayoutNode extends GraphNode { degree: number; x?: number; y?: number; z?: number; vx?: number; vy?: number; vz?: number }
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
  select(node: GraphNode): void; open(node: GraphNode): void; hover(node: GraphNode | null): void; clear(): void
  motionState?(phase: MotionPhase): void
}
type Graph3D = ForceGraph3DInstance<LayoutNode, LayoutEdge>
type Graph2D = ForceGraph2D<LayoutNode, LayoutEdge>
export const endpointID = (endpoint: string | LayoutNode) => typeof endpoint === 'string' ? endpoint : endpoint.id
export const graphRadius = (node: GraphNode) => 8 * Math.sqrt(Math.max(0, Number.isFinite(node.weight) ? node.weight : 0) + 1)
export function graphLayout(data: GraphData): { nodes: LayoutNode[]; links: LayoutEdge[] } {
  const degrees = new Map<string, number>()
  for (const link of data.links) for (const id of [link.source, link.target]) degrees.set(id, (degrees.get(id) ?? 0) + 1)
  return { nodes: data.nodes.map(n => ({ ...n, degree: degrees.get(n.id) ?? 0 })), links: data.links.map(l => ({ ...l })) }
}
export interface LabelBox { x: number; y: number; w: number; h: number }
const overlaps = (a: LabelBox, b: LabelBox) => Math.abs(a.x - b.x) < (a.w + b.w) / 2 + 4 && Math.abs(a.y - b.y) < (a.h + b.h) / 2 + 3
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
function graphPalette(nodes: LayoutNode[]) {
  const css = getComputedStyle(document.documentElement), read = (token: string) => css.getPropertyValue(token).trim()
  return { background: read('--canvas'), ink: read('--ink'), muted: read('--ink-3'), dark: css.colorScheme === 'dark',
    colors: Object.fromEntries(nodes.map(n => [n.color, read(n.color) || read('--teal')])) }
}

export async function createGraphRenderer(host: HTMLElement, dimension: GraphDimension, options: GraphRendererOptions): Promise<GraphRenderer | null> {
  let three: typeof import('three') | undefined, g3: Graph3D | undefined, g2: Graph2D | undefined
  let geometry: SphereGeometry | undefined
  const materials = new Map<string, MeshPhysicalMaterial>()
  const objects = new Map<string, Group>()
  let nodes: LayoutNode[] = [], links: LayoutEdge[] = [], labelOrder: LayoutNode[] = []
  let palette = graphPalette(nodes), disposed = false, paused = options.reduced
  let emphasis: GraphEmphasis = { selected: '', neighbours: new Set(), matches: new Set(), searching: false, hovered: '' }
  let labelMode = options.labels ?? 'smart', fps = options.fps ?? 60
  let paintFrame = 0, fitFrame = 0, frame = 0, lastFrame = 0, driftTime = 0, resumeAt = 0, dragging = false
  let lastPhase: MotionPhase | undefined
  let settleTimer: ReturnType<typeof setTimeout> | undefined, pickTimer: ReturnType<typeof setTimeout> | undefined
  let cameraTaken = false, fitOnSettle = true
  let pointerNode: LayoutNode | null = null, openedAt = -Infinity
  let lastPick: { node: LayoutNode; x: number; y: number; at: number } | null = null
  const duration = () => options.reduced || paused ? 0 : 650
  const active = (n: LayoutNode) => (!emphasis.selected || emphasis.neighbours.has(n.id)) && (!emphasis.searching || emphasis.matches.has(n.id))
  const alpha = (n: LayoutNode) => active(n) ? 1 : .14
  const color = (n: LayoutNode) => palette.colors[n.color] ?? palette.muted
  const touches = (l: LayoutEdge) => !!emphasis.selected && [endpointID(l.source), endpointID(l.target)].includes(emphasis.selected)
  const particles = (l: LayoutEdge) => !paused && l.directed && touches(l) ? 1 : 0
  const linkColor = (l: LayoutEdge) => /^#[\da-f]{6}$/i.test(palette.muted) ? palette.muted + (touches(l) ? 'aa' : emphasis.selected || emphasis.searching ? '18' : palette.dark ? '48' : '40') : palette.muted
  const labelLayer = document.createElement('div')
  labelLayer.className = 'graph-labels'; labelLayer.setAttribute('aria-hidden', 'true')
  const labels = new Map<string, { el: HTMLSpanElement; w: number; h: number }>()
  function rebuildLabels() {
    labelLayer.replaceChildren(); labels.clear()
    const fragment = document.createDocumentFragment()
    for (const n of nodes) {
      const el = document.createElement('span')
      el.className = 'graph-label'; el.textContent = n.label.length > 34 ? n.label.slice(0, 31) + '…' : n.label
      fragment.append(el)
      labels.set(n.id, { el, w: 0, h: 0 })
    }
    labelLayer.append(fragment)
    for (const label of labels.values()) { label.w = label.el.offsetWidth; label.h = label.el.offsetHeight }
  }
  function orderLabels() {
    labelOrder = [...nodes].sort((a, b) => Number(b.id === emphasis.selected) - Number(a.id === emphasis.selected) || Number(b.id === emphasis.hovered) - Number(a.id === emphasis.hovered) || b.degree - a.degree || a.id.localeCompare(b.id))
  }
  function placeLabels() {
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
    for (const n of labelOrder) {
      const label = labels.get(n.id); if (!label) continue
      const mandatory = n.id === emphasis.selected || n.id === emphasis.hovered
      label.el.hidden = true
      if (labelMode === 'off' && !mandatory) continue
      const screen = projected.get(n.id); if (!screen) continue
      const radius = screen.radius
      const box = labelPosition(screen.x, screen.y, radius, label.w, label.h, placed, width, height, mandatory || labelMode === 'all')
      if (!box) continue
      placed.push(box); label.el.hidden = false
      label.el.style.transform = `translate(${Math.round(box.x - box.w / 2)}px, ${Math.round(box.y - box.h / 2)}px)`
      label.el.style.opacity = mandatory ? '1' : String(Math.max(.55, alpha(n)))
      label.el.style.zIndex = mandatory ? '2' : '1'
    }
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
    const x = n.x ?? 0, y = n.y ?? 0, r = graphRadius(n)
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
    paused = value
    if (!value) resumeAt = 0 // Explicit play takes effect immediately, including reduced-motion opt-in.
    const graph = g3 ?? g2
    graph?.cooldownTicks(paused ? 0 : 140).linkDirectionalParticles(particles)
    if (!paused) graph?.d3ReheatSimulation()
    redraw()
  }
  function redraw() {
    if (disposed) return
    updateObjects(); orderLabels()
    const graph = g3 ?? g2
    graph?.linkColor(linkColor).linkDirectionalParticles(particles)
    g2?.nodeCanvasObject((n, ctx, scale) => draw2D(n, ctx, scale))
    placeLabels()
  }
  if (dimension === '3d') {
    const modules = await Promise.all([import('3d-force-graph'), import('three')])
    if (options.signal.aborted) return null
    three = modules[1]
    try {
      g3 = new modules[0].default(host, { controlType: 'orbit', rendererConfig: { antialias: true, alpha: true, powerPreference: 'low-power' } }) as unknown as Graph3D
      g3.renderer().setPixelRatio(Math.min(window.devicePixelRatio, 1.5))
      geometry = new three.SphereGeometry(1, 20, 14)
      const key = new three.DirectionalLight('#fff5e9', 2.4); key.position.set(-180, 240, 320)
      const rim = new three.DirectionalLight('#c6e9ff', 2); rim.position.set(180, 40, -120)
      g3.lights([new three.AmbientLight('#ffffff', 1.25), key, rim])
      g3.showNavInfo(false).backgroundColor(palette.background).nodeThreeObject(n => {
        const group = new three!.Group(), radius = graphRadius(n)
        const core = new three!.Mesh(geometry, material(n, false)); core.scale.setScalar(radius * .88)
        const shell = new three!.Mesh(geometry, material(n, true)); shell.scale.setScalar(radius)
        group.add(core, shell); objects.set(n.id, group)
        return group
      }).linkOpacity(1).linkWidth(.55)
      const controls = g3.controls() as OrbitControls
      controls.autoRotateSpeed = 1 // OrbitControls: one revolution per 60 seconds, using elapsed time.
      controls.enableDamping = !options.reduced
    } catch {
      const failedRenderer = g3?.renderer(); g3?._destructor(); failedRenderer?.forceContextLoss(); g3 = undefined
      geometry?.dispose(); materials.forEach(m => m.dispose()); materials.clear(); host.replaceChildren()
    }
  }
  if (!g3) {
    const { default: ForceGraph } = await import('force-graph')
    if (options.signal.aborted) return null
    g2 = new ForceGraph<LayoutNode, LayoutEdge>(host)
    g2.backgroundColor(palette.background).nodeCanvasObject((n, ctx, scale) => draw2D(n, ctx, scale)).nodePointerAreaPaint(pointerArea).linkWidth(l => touches(l) ? 1 : .6)
  }
  host.append(labelLayer)
  const graph = (g3 ?? g2)!
  graph.nodeLabel(() => '').linkLabel(() => '').nodeRelSize(8).nodeVal(n => Math.pow(Math.max(0, n.weight) + 1, 1.5))
    .linkColor(linkColor).linkDirectionalParticles(particles).linkDirectionalParticleWidth(1.4).linkDirectionalParticleSpeed(.002)
    .linkDirectionalArrowLength(l => l.directed ? 3 : 0).linkDirectionalArrowRelPos(1)
    .warmupTicks(90).cooldownTicks(paused ? 0 : 140).d3VelocityDecay(.38)
    .onNodeClick((node, event) => {
      if (performance.now() - openedAt < 100) return
      lastPick = { node, x: event.clientX, y: event.clientY, at: performance.now() }; options.select(node)
    }).onNodeHover(node => { pointerNode = node; options.hover(node) })
    .onBackgroundClick(() => { if (performance.now() - openedAt >= 100) options.clear() })
    .onNodeDrag(interact).onNodeDragEnd(interact)
    .onEngineStop(() => { if (fitOnSettle && !cameraTaken && !emphasis.selected) fit(); fitOnSettle = false })
  const pointerDown = () => { dragging = true; interact() }
  const pointerMove = () => { if (dragging) interact() }
  const pointerUp = () => { if (dragging) { dragging = false; interact() } }
  host.addEventListener('pointerdown', pointerDown, { passive: true }); host.addEventListener('wheel', interact, { passive: true })
  window.addEventListener('pointermove', pointerMove, { passive: true }); window.addEventListener('pointerup', pointerUp); window.addEventListener('pointercancel', pointerUp)
  function doubleClick(event: MouseEvent) {
    const recent = lastPick && performance.now() - lastPick.at < 600 && Math.hypot(event.clientX - lastPick.x, event.clientY - lastPick.y) < 8
    const node = pointerNode ?? (recent ? lastPick!.node : null)
    if (node) { event.preventDefault(); openedAt = performance.now(); options.open(node) }
  }
  host.addEventListener('dblclick', doubleClick)
  graph.d3Force('charge')?.strength(-60)
  // O(n) soft clusters: stable group anchors keep sparse same-group nodes near
  // each other without rigidly partitioning a strongly connected graph.
  let groups = new Map<string, { x: number; y: number; z: number }>()
  graph.d3Force('group-centre', (alpha: number) => {
    for (const n of nodes) {
      const at = groups.get(n.group) ?? { x: 0, y: 0, z: 0 }
      n.vx = (n.vx ?? 0) + (at.x - (n.x ?? 0)) * .045 * alpha
      n.vy = (n.vy ?? 0) + (at.y - (n.y ?? 0)) * .045 * alpha
      if (g3) n.vz = (n.vz ?? 0) + (at.z - (n.z ?? 0)) * .045 * alpha
    }
  })
  graph.d3Force('link')?.distance((l: LayoutEdge) => 45 + (typeof l.source === 'object' ? graphRadius(l.source) : 5) + (typeof l.target === 'object' ? graphRadius(l.target) : 5))
  // Both engines' public resume method draws one synchronous frame, then
  // schedules a RAF; pause cancels that RAF. Own just one clock so 30 FPS caps
  // physics, WebGL, labels and picking together, rather than only the camera.
  graph.pauseAnimation()
  function tick(now: number) {
    if (disposed) return
    frame = requestAnimationFrame(tick)
    if (document.hidden || now - lastFrame < 1000 / fps - .5) return
    const dt = Math.min(.1, (now - (lastFrame || now)) / 1000); lastFrame = now
    const phase: MotionPhase = paused ? 'paused' : dragging || now < resumeAt ? 'interacting' : 'orbiting'
    if (phase !== lastPhase) { lastPhase = phase; options.motionState?.(phase) }
    if (g3) (g3.controls() as OrbitControls).autoRotate = phase === 'orbiting'
    if (phase === 'orbiting') {
      driftTime += dt
      if (g3) {
        const camera = g3.camera(), controls = g3.controls() as OrbitControls
        camera.position.y += Math.cos(driftTime / 7) * dt * camera.position.distanceTo(controls.target) * .003
      } else if (g2) {
        const center = g2.centerAt()
        g2.centerAt(center.x + Math.cos(driftTime / 9) * dt * .9, center.y + Math.sin(driftTime / 7) * dt * .6)
      }
    }
    graph.resumeAnimation(); graph.pauseAnimation(); placeLabels()
  }
  frame = requestAnimationFrame(tick)
  // Fit fills about 80% of the stage along its tighter side, bubbles included.
  // No closer than 2.2 screen pixels per graph unit, so a few entries stay calm.
  const FILL = .8, MAX_ZOOM = 2.2
  function fit() {
    if (disposed || !nodes.length) return
    if (g3 && three) {
      const camera = g3.camera() as PerspectiveCamera
      const right = new three.Vector3(1, 0, 0).applyQuaternion(camera.quaternion)
      const up = new three.Vector3(0, 1, 0).applyQuaternion(camera.quaternion)
      const back = new three.Vector3(0, 0, 1).applyQuaternion(camera.quaternion)
      const span = (axis: import('three').Vector3) => {
        let low = Infinity, high = -Infinity
        for (const n of nodes) { const d = axis.x * (n.x ?? 0) + axis.y * (n.y ?? 0) + axis.z * (n.z ?? 0), r = graphRadius(n); low = Math.min(low, d - r); high = Math.max(high, d + r) }
        return (low + high) / 2
      }
      const centre = right.clone().multiplyScalar(span(right)).add(up.clone().multiplyScalar(span(up))).add(back.clone().multiplyScalar(span(back)))
      const tan = Math.tan(camera.fov * Math.PI / 360), aspect = Math.max(1, g3.width()) / Math.max(1, g3.height())
      let distance = g3.height() / (2 * tan * MAX_ZOOM)
      for (const n of nodes) {
        const v = new three.Vector3(n.x ?? 0, n.y ?? 0, n.z ?? 0).sub(centre), r = graphRadius(n), depth = v.dot(back)
        distance = Math.max(distance, depth + (Math.abs(v.dot(up)) + r) / (tan * FILL), depth + (Math.abs(v.dot(right)) + r) / (tan * aspect * FILL))
      }
      const at = centre.clone().add(back.multiplyScalar(distance))
      g3.cameraPosition({ x: at.x, y: at.y, z: at.z }, { x: centre.x, y: centre.y, z: centre.z }, duration())
    } else if (g2) {
      let x0 = Infinity, x1 = -Infinity, y0 = Infinity, y1 = -Infinity
      for (const n of nodes) { const r = graphRadius(n), x = n.x ?? 0, y = n.y ?? 0; x0 = Math.min(x0, x - r); x1 = Math.max(x1, x + r); y0 = Math.min(y0, y - r); y1 = Math.max(y1, y + r) }
      const zoom = Math.min(MAX_ZOOM, g2.width() * FILL / Math.max(1, x1 - x0), g2.height() * FILL / Math.max(1, y1 - y0))
      g2.centerAt((x0 + x1) / 2, (y0 + y1) / 2, duration()); g2.zoom(zoom, duration()); refreshPicking(duration())
    }
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
    } else if (g2) {
      let zoom = FOCUS_ZOOM
      for (const n of around) {
        const r = graphRadius(n)
        zoom = Math.min(zoom, g2.width() * FILL / 2 / (Math.abs((n.x ?? 0) - x) + r), g2.height() * FILL / 2 / (Math.abs((n.y ?? 0) - y) + r))
      }
      g2.centerAt(x, y, duration()); g2.zoom(Math.max(.2, zoom), duration()); refreshPicking(duration())
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
      objects.clear()
      const layout = graphLayout(value); nodes = layout.nodes; links = layout.links
      palette = graphPalette(nodes)
      const names = [...new Set(nodes.map(n => n.group))].sort(), radius = names.length > 1 ? 35 + Math.sqrt(nodes.length) * 9 : 0
      groups = new Map(names.map((group, i) => { const angle = i * 2 * Math.PI / names.length; return [group, { x: Math.cos(angle) * radius, y: Math.sin(angle) * radius, z: Math.sin(angle * 2) * radius * .3 }] }))
      graph.graphData({ nodes, links }); rebuildLabels(); redraw()
      clearTimeout(settleTimer)
      whenLaidOut(() => { redraw(); if (emphasis.selected) focus(emphasis.selected); else if (!cameraTaken) fit() })
      if (!paused) settleTimer = setTimeout(() => { if (emphasis.selected) focus(emphasis.selected); else if (!cameraTaken) fit() }, 1200)
    },
    emphasis(value) { emphasis = value; redraw() },
    theme() {
      palette = graphPalette(nodes); graph.backgroundColor(palette.background)
      materials.forEach(m => m.dispose()); materials.clear(); redraw()
    },
    resize(width, height) {
      graph.width(Math.max(1, width)).height(Math.max(1, height)); cancelAnimationFrame(fitFrame)
      const node = g2 && emphasis.selected ? nodes.find(n => n.id === emphasis.selected) : undefined
      if (node) fitFrame = requestAnimationFrame(() => { g2?.centerAt(node.x ?? 0, node.y ?? 0); refreshPicking(0) })
      else if (!cameraTaken && !emphasis.selected) fitFrame = requestAnimationFrame(fit)
    },
    fit, focus, motion, interact,
    labels(mode) { labelMode = mode; placeLabels() },
    frameRate(value) { fps = value },
    dispose() {
      if (disposed) return
      disposed = true
      cancelAnimationFrame(frame); cancelAnimationFrame(paintFrame); cancelAnimationFrame(fitFrame); clearTimeout(settleTimer); clearTimeout(pickTimer)
      host.removeEventListener('dblclick', doubleClick); host.removeEventListener('pointerdown', pointerDown); host.removeEventListener('wheel', interact)
      window.removeEventListener('pointermove', pointerMove); window.removeEventListener('pointerup', pointerUp); window.removeEventListener('pointercancel', pointerUp)
      graph.onNodeHover(() => {}).onNodeClick(() => {}).onBackgroundClick(() => {}).onNodeDrag(() => {}).onNodeDragEnd(() => {}).onEngineStop(() => {}).pauseAnimation()
      const renderer = g3?.renderer()
      graph._destructor(); geometry?.dispose(); materials.forEach(m => m.dispose()); materials.clear(); objects.clear(); labels.clear()
      renderer?.forceContextLoss(); nodes = []; links = []; host.replaceChildren()
    },
  }
}
