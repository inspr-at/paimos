// SPDX-License-Identifier: AGPL-3.0-only
import { createHash } from 'node:crypto'

const uuid = value => {
  const h = createHash('sha256').update(`AEON-1355-synthetic:${value}`).digest('hex')
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-4${h.slice(13, 16)}-8${h.slice(17, 20)}-${h.slice(20, 32)}`
}
const quote = value => `'${String(value).replaceAll("'", "''")}'`

// Only numeric distributions cross the production boundary. Names, text, IDs,
// priorities and dates below are generated independently of production content.
export function fixtureSQL(inventory) {
  const sql = ["BEGIN; SELECT set_config('aeon.visible_projects','*',true);",
    "SELECT set_config('aeon.tenant_id',(SELECT id::text FROM tenants WHERE slug='perf1355'),true);"]
  const tenant = "NULLIF(current_setting('aeon.tenant_id',true),'')::uuid"
  const kind = slug => `(SELECT id FROM node_kinds WHERE tenant_id=${tenant} AND slug=${quote(slug)})`
  for (const [projectIndex, project] of inventory.projects.entries()) {
    if (!/^[A-Z]+$/.test(project.key) || !Number.isInteger(project.work_items) || project.work_items < 2 || project.work_items > 20000) throw Error('Invalid count-only fixture inventory')
    const root = uuid(project.key), nodes = [], levels = new Map()
    sql.push(`INSERT INTO nodes(tenant_id,id,key,kind_id,title,fields) VALUES(${tenant},${quote(root)},${quote(`PRJ-${projectIndex + 1}`)},${kind('project')},${quote(`Synthetic ${project.key}`)},${quote(JSON.stringify({ project_key: project.key }))}::jsonb);`)
    let index = 0
    for (const shape of project.shapes) {
      if (![shape.depth, shape.children, shape.count].every(v => Number.isInteger(v) && v >= 0 && v <= 20000)) throw Error('Invalid shape count')
      for (let i = 0; i < shape.count; i++) {
        const node = { id: uuid(`${project.key}-${++index}`), index, depth: shape.depth, quota: shape.children }
        nodes.push(node)
        if (!levels.has(node.depth)) levels.set(node.depth, [])
        levels.get(node.depth).push(node)
      }
    }
    if (nodes.length !== project.work_items) throw Error('Fixture count mismatch')
    for (const [depth, level] of [...levels].sort(([a], [b]) => a - b)) {
      const parents = (levels.get(depth - 1) ?? []).filter(node => node.quota > 0)
      let parentIndex = 0
      for (const node of level) {
        while (parents[parentIndex]?.quota === 0) parentIndex++
        const parent = parents[parentIndex]
        // Project scope starts at depth 1. Missing non-work ancestors are
        // represented by the project root; the report must disclose that case.
        const parentID = parent?.id ?? root
        if (parent) parent.quota--
        const fields = { priority: ['high', 'medium', 'low'][node.index % 3],
          area: ['frontend', 'backend'][node.index % 2], estimate_hours: 2,
          estimate_source: 'agent', estimate_at: '2026-10-01T00:00:00Z' }
        sql.push(`INSERT INTO nodes(tenant_id,id,key,kind_id,title,body,fields,state,parent_id,position) VALUES(${tenant},${quote(node.id)},${quote(`${project.key}-${node.index}`)},${kind('work')},${quote(`Synthetic task ${node.index}`)},${quote('Generated benchmark description. '.repeat(30))},${quote(JSON.stringify(fields))}::jsonb,${quote(node.index % 4 === 0 ? 'done' : 'open')},${quote(parentID)},${node.index});`)
      }
    }
    let commentNode = 0
    for (const [count, tickets] of Object.entries(project.comment_histogram)) {
      const n = Number(count)
      if (!Number.isInteger(n) || n < 0 || n > 10000 || !Number.isInteger(tickets)) throw Error('Invalid comment histogram')
      for (let t = 0; t < tickets; t++) {
        const node = nodes[commentNode++]
        if (!node) throw Error('Comment histogram exceeds node count')
        if (n) sql.push(`INSERT INTO events(tenant_id,node_id,actor_principal_id,type,after,at) SELECT ${tenant},${quote(node.id)},(SELECT id FROM principals WHERE tenant_id=${tenant} AND kind='person' LIMIT 1),'comment.created',jsonb_build_object('body_markdown','Synthetic comment '||g),'2026-10-01Z'::timestamptz+g*interval '1 second' FROM generate_series(1,${n}) g;`)
      }
    }
    if (commentNode !== nodes.length) throw Error('Comment histogram incomplete')
    if (!Number.isInteger(project.incident_relations) || project.incident_relations > 100000) throw Error('Invalid relation count')
    // Count-equivalent directed edges; production topology/types are not copied.
    for (let i = 0; i < project.incident_relations; i++) {
      const a = nodes[i % nodes.length], b = nodes[(i + 1 + Math.floor(i / nodes.length)) % nodes.length]
      if (a.id === b.id) throw Error('Fixture edge capacity exceeded')
      sql.push(`INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES(${tenant},${quote(a.id)},${quote(b.id)},'implements');`)
    }
  }
  sql.push('COMMIT; ANALYZE nodes; ANALYZE events; ANALYZE node_relations;')
  return sql.join('\n')
}
