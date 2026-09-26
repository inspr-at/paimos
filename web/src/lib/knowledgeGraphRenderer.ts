// SPDX-License-Identifier: AGPL-3.0-only
// Knowledge's only translation into the reusable renderer contract. TG1 supplies
// its own GraphData adapter; it never needs knowledge types or API imports.
import { entryPath, type KnowledgeType } from './knowledge'
import { graphTypeTokens, type KnowledgeGraphData } from './knowledgeGraph'
import type { GraphData } from './graphRenderer'

export function knowledgeGraphData(data: KnowledgeGraphData, projectKey: string): GraphData {
  return {
    nodes: data.nodes.map(node => ({
      id: node.id, label: node.title, group: node.type, color: graphTypeTokens[node.type] as `--${string}`, weight: node.degree,
      href: node.kind === 'knowledge' ? entryPath(projectKey, node.type as KnowledgeType, node.slug) : undefined,
    })),
    links: data.edges.map(edge => ({ source: edge.source, target: edge.target, kind: edge.kind, directed: edge.kind === 'mention' || edge.label.split(', ').some(kind => kind !== 'relates') })),
  }
}
