// SPDX-License-Identifier: AGPL-3.0-only
import type { Page } from '@playwright/test'
import type { Walker, Intake } from '../src/lib/releaseData'
import type { Call } from './work-fixtures'
export const PROJECT = 'p-pharos'
const now=Date.parse('2026-09-23T12:00:00Z')
const ago=(minutes:number)=>new Date(now-minutes*60_000).toISOString()
export type ReleaseWorld = ReturnType<typeof releaseWorld>
export function releaseWorld(start:'plan'|'live'|'open'='plan') {
  const release = (id: string, key: string, title: string, state: string, minutes: number) => ({ id, key, kind_id: 'k-release', title, body: '', fields: {}, state, parent_id: PROJECT, position: '0', created_at: ago(minutes), updated_at: ago(minutes), deleted_at: null })
  const releases = [release('r-1', 'PHAROS-30', '260901120000.0.0', 'done', 60 * 24 * 22), release('r-2', 'PHAROS-31', 'Release 2', 'backlog', 60 * 24 * 2)]
  const walkers: Record<string, Walker> = {
    'r-1': { release_node_id: 'r-1', project_node_id: PROJECT, state: 'released', revision: 4, features: [{ feature_node_id: 'n-epic', epic_key: 'PHAROS-10', title: 'Guarded multi-cloud provisioning', selection: 'all', included_count: 1, open_count: 1 }],
      tickets: [{ ticket_node_id: 'n-5', key: 'PHAROS-15', title: 'Beacon health probes', feature_node_id: 'n-epic', included: true, position: 0, estimated_hours: 5, screen_node_ids: [] }] },
    'r-2': { release_node_id: 'r-2', project_node_id: PROJECT, state: 'planning', revision: 7,
      features: [
        { feature_node_id: 'n-epic', epic_key: 'PHAROS-10', title: 'Guarded multi-cloud provisioning', selection: 'some', included_count: 2, open_count: 3 },
        { feature_node_id: 'n-epic-2', epic_key: 'PHAROS-20', title: 'Host access with Janus', selection: 'empty', included_count: 0, open_count: 0 },
      ],
      tickets: [
        { ticket_node_id: 'n-1', key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning', feature_node_id: 'n-epic', included: true, position: 0, estimated_hours: 8, screen_node_ids: [] },
        { ticket_node_id: 'n-2', key: 'PHAROS-12', title: 'Add an Oracle Cloud connector', feature_node_id: 'n-epic', included: false, position: 1, estimated_hours: 6, screen_node_ids: [] },
        { ticket_node_id: 'n-3', key: 'PHAROS-13', title: 'Run the disposable Hetzner end-to-end check', feature_node_id: 'n-epic', included: true, position: 2, estimated_hours: 3, screen_node_ids: [] },
        { ticket_node_id: 'n-4', key: 'PHAROS-14', title: 'Visual acceptance of the version pill', feature_node_id: null, included: false, position: 3, estimated_hours: null, screen_node_ids: [] },
      ] },
  }

 if(start==='live'){walkers['r-2'].state='released';walkers['r-2'].tickets=walkers['r-2'].tickets.filter(ticket=>ticket.included)}
 if(start==='open'){releases.length=0;for(const id of Object.keys(walkers))delete walkers[id]}
 return {currentReleaseId:start==='open'?null:'r-2' as string|null,releases,walkers,intake:{sources:[],turns:[],drafts:[]} as Intake}
}
export async function mockReleases(page:Page,world:ReleaseWorld) {
 const calls:Call[]=[]
 await page.route('**/api/**',async route=>{
  const request=route.request(),url=new URL(request.url()),path=url.pathname
  if(path===`/api/projects/${PROJECT}/releases`&&request.method()==='GET')return route.fulfill({json:{releases:world.releases.map((release,index)=>({id:release.id,title:release.title,number:index+1,state:world.walkers[release.id]?.state??'released'})),truncated:false}})
  if(path===`/api/projects/${PROJECT}/intake`)return route.fulfill({json:world.intake})
  const walker=path.match(/\/releases\/([^/]+)\/walker$/)
  if(walker&&world.walkers[walker[1]])return route.fulfill({json:world.walkers[walker[1]]})
  return route.fallback()
 });return calls
}
