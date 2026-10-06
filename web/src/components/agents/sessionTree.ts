// SPDX-License-Identifier: AGPL-3.0-only
// The session tree as rows (AEON-784): any depth, one row per visible session in
// tree order, with what a treegrid needs (level, position among its siblings,
// fold state) and what a folded parent says about the sessions under it. Free of
// Vue so it can be unit tested; the list decides which children a parent offers
// and which parents are open.
import type { SessionBranch, SessionStatus } from '../../lib/agentState'

type Viewish = { session: { id: string }; status: SessionStatus }

export interface TreeRow<T extends Viewish> {
  branch: SessionBranch<T>
  depth: number
  /** Per level from the root's children down to this row: whether a later sibling at that level follows, so its line continues. */
  guides: boolean[]
  parent: SessionBranch<T> | null
  /** The parent has children to show and may fold. */
  foldable: boolean
  open: boolean
  posinset: number
  setsize: number
  /** A filter keeps this row only as the context of a match below it. */
  contextOnly: boolean
}

export interface TreeOptions<T extends Viewish> {
  /** The children a parent offers when open (the list hides stopped ones until asked). */
  children: (branch: SessionBranch<T>) => SessionBranch<T>[]
  isOpen: (branch: SessionBranch<T>) => boolean
  /** A filter: matches stay, with their ancestors as unfolded, dimmed context. */
  match?: ((view: T) => boolean) | null
}

// A problem is a failure or a lost heartbeat; an ask waits on a person.
export const isProblem = (status: SessionStatus) => status.group === 'problem' || status.group === 'unresponsive'
export const isAsk = (status: SessionStatus) => status.group === 'needs'

export interface Below { problem: number; ask: number; names: { problem: string[]; ask: string[] } }
/** What needs a look anywhere under a parent, at any depth. */
export function below<T extends Viewish>(branch: SessionBranch<T>, name: (view: T) => string): Below {
  const out: Below = { problem: 0, ask: 0, names: { problem: [], ask: [] } }
  const walk = (b: SessionBranch<T>) => b.children.forEach(child => {
    if (isProblem(child.view.status)) { out.problem++; out.names.problem.push(name(child.view)) }
    else if (isAsk(child.view.status)) { out.ask++; out.names.ask.push(name(child.view)) }
    walk(child)
  })
  walk(branch)
  return out
}

/** How many sessions a parent would show at every depth if all were unfolded. */
export function shownBelow<T extends Viewish>(branch: SessionBranch<T>, children: (branch: SessionBranch<T>) => SessionBranch<T>[]): number {
  return children(branch).reduce((sum, child) => sum + 1 + shownBelow(child, children), 0)
}

/** The visible rows of some roots, in tree order. */
export function treeRows<T extends Viewish>(roots: SessionBranch<T>[], options: TreeOptions<T>): TreeRow<T>[] {
  const { match } = options
  const keep = new Map<SessionBranch<T>, boolean>()
  const kept = (branch: SessionBranch<T>): boolean => {
    let value = keep.get(branch)
    if (value === undefined) {
      // Every child is visited so each verdict is cached once.
      const childKept = branch.children.map(kept).some(Boolean)
      value = !match || match(branch.view) || childKept
      keep.set(branch, value)
    }
    return value
  }
  // With a filter, every kept child shows and its parents open; otherwise the list decides.
  const offered = (branch: SessionBranch<T>) => match ? branch.children.filter(kept) : options.children(branch)
  const out: TreeRow<T>[] = []
  const walk = (list: SessionBranch<T>[], depth: number, guides: boolean[], parent: SessionBranch<T> | null) => {
    const shown = list.filter(kept)
    shown.forEach((branch, index) => {
      const children = offered(branch)
      const foldable = children.length > 0
      const open = foldable && (!!match || options.isOpen(branch))
      // A row's own level says whether a sibling follows it, so the line goes on below.
      const own = depth ? [...guides, index < shown.length - 1] : []
      out.push({ branch, depth, guides: own, parent, foldable, open, posinset: index + 1, setsize: shown.length, contextOnly: !!match && !match(branch.view) })
      if (open) walk(children, depth + 1, own, branch)
    })
  }
  walk(roots, 0, [], null)
  return out
}

// The first session in a state, including one a fold hides, and the ancestors
// that have to open before its row exists. A problem includes a lost heartbeat.
// Ancestors are the parents that are in this list, root first.
export function jumpTarget<T extends { session: { id: string; parent_harness_session_id?: string | null }; status: { state: string } }>(views: readonly T[], state: string): { id: string; ancestors: string[] } | null {
  const states = state === 'problem' ? ['problem', 'unresponsive'] : [state]
  const byId = new Map(views.map(view => [view.session.id, view]))
  const children = new Map<string, T[]>()
  const roots: T[] = []
  for (const view of views) {
    const parent = view.session.parent_harness_session_id
    if (parent && parent !== view.session.id && byId.has(parent)) {
      const list = children.get(parent)
      if (list) list.push(view)
      else children.set(parent, [view])
    } else roots.push(view)
  }
  const ancestorsOf = (view: T) => {
    const chain: string[] = []
    const seen = new Set([view.session.id])
    let parent = view.session.parent_harness_session_id
    while (parent && byId.has(parent) && !seen.has(parent)) {
      chain.push(parent)
      seen.add(parent)
      parent = byId.get(parent)?.session.parent_harness_session_id
    }
    return chain.reverse()
  }
  const seen = new Set<string>()
  const walk = (view: T): T | undefined => {
    if (seen.has(view.session.id)) return
    seen.add(view.session.id)
    if (states.includes(view.status.state)) return view
    for (const child of children.get(view.session.id) ?? []) {
      const found = walk(child)
      if (found) return found
    }
  }
  for (const view of [...roots, ...views]) {
    const found = walk(view)
    if (found) return { id: found.session.id, ancestors: ancestorsOf(found) }
  }
  return null
}
