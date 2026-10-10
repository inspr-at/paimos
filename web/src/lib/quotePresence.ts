// SPDX-License-Identifier: AGPL-3.0-only
// One quote-scoped stream per editor tab. The coordinator mounts this client
// with the editor; presence never enters the document model or PDF renderer.
import { api, APIError } from './api'
import { avatarColor, type AvatarColor } from './avatar'
import type { QuoteDocumentData, EditorSelection } from './quotes/types'

export interface PresenceAnchor { section_id: string; node_id?: string; observed_revision: number; text_sha256?: string; anchor?: number; focus?: number; fidelity: 'section'|'precise' }
export interface CollaboratorSession { session_id: string; principal_id: string; name: string; has_avatar?: boolean; mode: 'viewing'|'editing'|'idle'; anchor?: PresenceAnchor; observed_revision: number; expires_at: string }
export interface PresenceSnapshot { sessions: CollaboratorSession[]; draft_revision: number; quote_revision: number; state: 'draft'|'issued'|'accepted'|'void' }
export interface QuoteNotice { id: number; quote_node_id: string; type: string; actor_principal_id: string; draft_revision: number; quote_revision: number; state: string; client_session_id?: string; mutation_id?: string }
type Listener = (snapshot: PresenceSnapshot) => void
type NoticeListener = (notice: QuoteNotice) => void
// A collaborator's colour is their avatar's hue, saturated enough for a caret and
// a name label (--presence-l/-c follow the theme; white or dark ink sits on it).
const HUES: Record<AvatarColor, number> = { slate: 255, sage: 150, moss: 125, ocean: 222, steel: 238, denim: 258, iris: 290, plum: 330, rose: 12, clay: 45, sand: 82, teal: 188 }
export const collaboratorHue = (principalId: string): number => HUES[avatarColor(principalId)]
export const collaboratorColor = (principalId: string): string => `oklch(var(--presence-l) var(--presence-c) ${collaboratorHue(principalId)})`
const path = (id:string) => `/quotes/${encodeURIComponent(id)}/presence`
async function json<T>(response:Response):Promise<T> { const body=await response.json().catch(()=>({})); if(!response.ok) throw new APIError(response.status,'Quote presence unavailable',body); return body as T }
async function textHash(text:string):Promise<string> { const bytes=await crypto.subtle.digest('SHA-256',new TextEncoder().encode(text)); return [...new Uint8Array(bytes)].map(b=>b.toString(16).padStart(2,'0')).join('') }
export async function selectionAnchor(selection:EditorSelection,doc:QuoteDocumentData,revision:number):Promise<PresenceAnchor|null> {
  const section=doc.sections.find(s=>s.id===selection.sectionId); if(!section) return null
  const fallback:PresenceAnchor={section_id:section.id,observed_revision:revision,fidelity:'section'}
  const sel=selection.text; if(!sel || sel.anchor.nodeId!==sel.focus.nodeId) return fallback
  const node=section.nodes.find(n=>n.id===sel.anchor.nodeId); if(!node) return fallback
  const a=sel.anchor.offset,f=sel.focus.offset
  if(a<0 || f<0 || a>node.text.length || f>node.text.length || (a>0 && /[\uD800-\uDBFF]/.test(node.text[a-1]) && /[\uDC00-\uDFFF]/.test(node.text[a])) || (f>0 && /[\uD800-\uDBFF]/.test(node.text[f-1]) && /[\uDC00-\uDFFF]/.test(node.text[f]))) return fallback
  return {...fallback,node_id:node.id,anchor:a,focus:f,text_sha256:await textHash(node.text),fidelity:'precise'}
}
export async function anchorFidelity(anchor:PresenceAnchor|undefined,doc:QuoteDocumentData,revision:number):Promise<'missing'|'section'|'precise'> {
  if(!anchor) return 'missing'; const section=doc.sections.find(s=>s.id===anchor.section_id); if(!section) return 'missing'
  if(anchor.fidelity!=='precise' || anchor.observed_revision!==revision) return 'section'
  const node=section.nodes.find(n=>n.id===anchor.node_id);if(!node || node.text.length<Math.max(anchor.anchor??0,anchor.focus??0))return 'section'
  return await textHash(node.text)===anchor.text_sha256?'precise':'section'
}
export class QuotePresence {
  readonly quoteId:string; readonly principalId:string
  sessionId:string|null=null; snapshot:PresenceSnapshot|null=null
  private stream:EventSource|null=null; private heartbeatTimer:number|undefined; private pendingTimer:number|undefined
  private lastUpdate=0; private desired:{ mode:'viewing'|'editing'|'idle'; anchor:PresenceAnchor|null; revision:number; interacted:boolean }|null=null
  private updating=false; private pendingUpdate=false; private joinRequest:Promise<void>|null=null; private retryAt=0; private retryDelay=1000
  private disposed=false; private onSnapshot:Listener; private onNotice:NoticeListener
  constructor(quoteId:string,principalId:string,onSnapshot:Listener,onNotice:NoticeListener) { this.quoteId=quoteId;this.principalId=principalId;this.onSnapshot=onSnapshot;this.onNotice=onNotice }
  async start(revision:number):Promise<void> {
    this.disposed=false; this.desired??={mode:'viewing',anchor:null,revision,interacted:false}; await this.join(revision)
    if(this.disposed) return
    this.stream=new EventSource(`/api/quotes/${encodeURIComponent(this.quoteId)}/collaboration/stream`,{withCredentials:true})
    this.stream.addEventListener('presence',(event)=>{ if(this.disposed) return; this.snapshot=JSON.parse((event as MessageEvent).data) as PresenceSnapshot;this.onSnapshot(this.snapshot) })
    this.stream.addEventListener('quote_change',(event)=>{ if(this.disposed) return;this.onNotice(JSON.parse((event as MessageEvent).data) as QuoteNotice) })
    this.stream.addEventListener('access_revoked',()=>this.stop())
    this.heartbeatTimer=window.setInterval(()=>{void this.update()},15000)
    document.addEventListener('visibilitychange',this.visibility)
    window.addEventListener('focus',this.focus)
  }
  private join(revision:number):Promise<void> {
    if(this.joinRequest) return this.joinRequest
    this.joinRequest=this.joinOnce(revision).finally(()=>{this.joinRequest=null})
    return this.joinRequest
  }
  private async joinOnce(revision:number):Promise<void> {
    const body={mode:document.hidden?'idle':this.desired?.mode??'viewing',observed_revision:revision,...(this.sessionId?{resume_session_id:this.sessionId}:{})}
    const result=await json<{session_id:string;snapshot:PresenceSnapshot}>(await api(path(this.quoteId),{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)}))
    if(this.disposed) return; this.retryAt=0;this.retryDelay=1000;this.sessionId=result.session_id;this.snapshot=result.snapshot;this.onSnapshot(result.snapshot)
  }
  private async rejoin(revision:number):Promise<void> {
    if(this.disposed || Date.now()<this.retryAt) return
    try { await this.join(revision) }
    catch(e) {
      if(e instanceof APIError && (e.status===401 || e.status===403)) { this.stop(); return }
      this.retryAt=Date.now()+this.retryDelay;this.retryDelay=Math.min(60000,this.retryDelay*2)
    }
  }
  setSelection(anchor:PresenceAnchor|null,mode:'viewing'|'editing',revision:number):void {
    this.desired={mode,anchor,revision,interacted:mode==='editing'};if(this.disposed) return
    window.clearTimeout(this.pendingTimer);this.pendingTimer=window.setTimeout(()=>{void this.update()},Math.max(0,220-(Date.now()-this.lastUpdate)))
  }
  private visibility=()=>{if(document.hidden && this.desired) this.desired.mode='idle';void this.update()}
  private focus=()=>{void this.refresh();void this.update()}
  async refresh():Promise<void> { if(this.disposed) return; try { const s=await json<PresenceSnapshot>(await api(path(this.quoteId)));if(!this.disposed){this.snapshot=s;this.onSnapshot(s)} } catch(e) { if(e instanceof APIError && (e.status===401 || e.status===403)) this.stop() } }
  private finishUpdate():void {
    this.updating=false
    if(this.disposed || !this.pendingUpdate) return
    // A timer may fire while a PATCH or rejoin is held. Coalesce those requests,
    // then flush the latest desired state after the throttle and retry fence.
    window.clearTimeout(this.pendingTimer)
    this.pendingTimer=window.setTimeout(()=>{void this.update()},Math.max(0,220-(Date.now()-this.lastUpdate),this.retryAt-Date.now()))
  }
  private async update():Promise<void> {
    if(this.disposed || !this.desired) return
    if(this.updating) {this.pendingUpdate=true;return}
    if(!this.sessionId) {
      this.updating=true
      try { await this.rejoin(this.desired.revision) } finally {
        // Joining restores the lease; PATCH carries its latest selection.
        if(this.sessionId) this.pendingUpdate=true
        this.finishUpdate()
      }
      return
    }
    if(Date.now()-this.lastUpdate<200) {window.clearTimeout(this.pendingTimer);this.pendingTimer=window.setTimeout(()=>{void this.update()},220-(Date.now()-this.lastUpdate));return}
    this.lastUpdate=Date.now()
    const wanted=this.desired; const mode=document.hidden?'idle':wanted.mode
    this.updating=true;this.pendingUpdate=false
    try {
      const s=await json<PresenceSnapshot>(await api(`${path(this.quoteId)}/${this.sessionId}`,{method:'PATCH',headers:{'Content-Type':'application/json'},body:JSON.stringify({mode,observed_revision:wanted.revision,anchor:wanted.anchor,interacted:wanted.interacted})}))
      if(this.desired===wanted) wanted.interacted=false
      if(!this.disposed){this.snapshot=s;this.onSnapshot(s)}
    } catch(e) {
      if(e instanceof APIError && e.status===404 && !this.disposed){this.sessionId=null;await this.rejoin(wanted.revision)}
      if(e instanceof APIError && (e.status===401 || e.status===403)) this.stop()
    } finally { this.finishUpdate() }
  }
  stop():void {
    this.disposed=true;this.stream?.close();this.stream=null;window.clearInterval(this.heartbeatTimer);window.clearTimeout(this.pendingTimer)
    document.removeEventListener('visibilitychange',this.visibility);window.removeEventListener('focus',this.focus)
    if(this.sessionId) { void api(`${path(this.quoteId)}/${this.sessionId}`,{method:'DELETE',keepalive:true}).catch(()=>{}) }
    this.sessionId=null
  }
}
