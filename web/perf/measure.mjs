// SPDX-License-Identifier: AGPL-3.0-only
// Shared measurement decisions for the AEON-1355 harness. The Playwright spec
// and the regression test both use these so a budget or renderer change cannot
// drift between the cell and the report.

export const graphLaunchArgs = ['--use-gl=angle', '--use-angle=swiftshader', '--enable-unsafe-swiftshader']

// GraphCanvas starts in 2D and pauses when this media query matches.
export const measurementReducedMotion = 'no-preference'

// The 50 ms long-task rule applies while typing or scrolling. Other actions
// keep the long-task counts and pass or fail on the time budget alone.
export function measurementPass(action, usableMs, budgetMs, longTasks) {
  const withinBudget = Number.isFinite(usableMs) && Number.isFinite(budgetMs) && usableMs <= budgetMs
  if (action === 'search' || action === 'scroll') return withinBudget && Array.isArray(longTasks) && longTasks.length === 0
  return withinBudget
}

// A ready 2D canvas is the reduced-motion or WebGL fallback, not the Graph view.
export function graphMeasurementFault(dimension) {
  if (dimension === '3d') return null
  const shown = dimension == null || dimension === '' ? 'missing' : String(dimension)
  return `graph canvas data-dimension is ${shown}, expected 3d`
}

export function graphProbe(dimension, ready) {
  if (dimension !== '3d' && (dimension || ready)) return { reject: dimension || 'missing' }
  if (ready === true && dimension === '3d') return { ready: true }
  return null
}

const instrumentationBody = `
window.__baselineGraphDimension = ''
window.__baselineReady = requirement => {
  const find = selector => document.querySelector(selector)
  const list = !!find('table.tickets[aria-busy="false"] tr.ticket-row:not(.ghost)')
  if (requirement.type === 'list') {
    if (!list) return false
    if (requirement.role && !find(\`table[role="\${requirement.role}"]\`)) return false
    if (requirement.query && new URLSearchParams(location.search).get(requirement.query[0]) !== requirement.query[1]) return false
    if (requirement.absent && find(requirement.absent)) return false
    if ('checked' in requirement && find('input[aria-label="Hide closed"]')?.checked !== requirement.checked) return false
    if ('sortBefore' in requirement && find('th[data-column-id="title"]')?.getAttribute('aria-sort') === requirement.sortBefore) return false
    return true
  }
  if (requirement.type === 'graph') {
    const canvas = find('.ticket-graph-canvas')
    if (!canvas) return false
    const dimension = canvas.getAttribute('data-dimension')
    const ready = canvas.getAttribute('data-ready') === 'true'
    const probe = graphProbe(dimension, ready)
    if (probe && probe.reject) {
      window.__baselineGraphDimension = probe.reject
      return false
    }
    return !!(probe && probe.ready) && !find('.tg-state, .graph-error')
  }
  if (requirement.type === 'ticket') return !!find('.ticket-ws .inline-title') && !find('.ws-skeleton')
  if (requirement.type === 'desk') return !!find('#desk-title') && ![...document.querySelectorAll('[role="status"]')].some(el => el.textContent.includes('Reading the desk')) && !find('.source-warnings')
  if (requirement.type === 'agents') return !!find('#agents-title') && !find('[aria-label="Loading sessions"]')
  if (requirement.type === 'hover') return !!find('.tooltip')
  if (requirement.type === 'scroll') return find('#main').scrollTop > 0
  return false
}
window.__baseline = { start: 0, paint: null, usable: null, tasks: [] }
const observer = new PerformanceObserver(list => {
  for (const entry of list.getEntries()) window.__baseline.tasks.push({ start: entry.startTime, duration: entry.duration })
})
observer.observe({ type: 'longtask', buffered: true })
window.__baselineObserver = observer
window.__baselineArm = requirement => {
  const m = window.__baseline
  m.start = performance.now(); m.paint = null; m.usable = null
  performance.mark('AEON1355-start')
  const sample = () => {
    if (window.__baselineGraphDimension) return
    if (window.__baselineReady(requirement)) {
      // The first frame containing the result, followed by a full paint
      // opportunity. All timestamps stay inside the browser, not the driver.
      m.paint = performance.now()
      requestAnimationFrame(() => { m.usable = performance.now(); performance.mark('AEON1355-usable') })
    } else requestAnimationFrame(sample)
  }
  requestAnimationFrame(sample)
}
`
export const instrumentationSource = `const graphProbe = ${graphProbe.toString()};\n${instrumentationBody}`
