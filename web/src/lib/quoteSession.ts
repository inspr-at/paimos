// SPDX-License-Identifier: AGPL-3.0-only
// Mutable editor state for one authenticated quote and browser tab. The P1
// server endpoint remains the sole authority for saves and normalization.
import { APIError } from './api'
import { getDraft, saveDraft, type QuoteDraft, type MutationReceipt } from './quotes/api'
import type { QuoteDocumentData } from './quotes/types'
import { mergeQuote, type ConflictChoices, type MergePreview } from './quoteMerge'
import { saveRecovery, loadRecovery, clearRecovery, downloadRecovery, holdsWork, type RecoveryDraft } from './quoteRecovery'
import type { QuoteNotice, PresenceSnapshot } from './quotePresence'

export type LocalState = 'loading'|'clean'|'dirty'|'saving'|'offline'|'failed'|'conflict'|'read-only'
export type RemoteState = 'current'|'checking'|'newer'|'unavailable'
// Why the last save did not land: 'invalid' (the server refused the document: fix it,
// a retry cannot succeed), 'forbidden' (no longer allowed to edit) or 'server' (a
// passing problem: trying again can work). Offline is its own local state.
export interface SaveFailure { kind:'invalid'|'forbidden'|'server'; status:number; message:string; body:Record<string,unknown> }
export interface SessionView {
  local:LocalState; remote:RemoteState; working:QuoteDocumentData|null; baseRevision:number
  highestRemoteRevision:number; remoteActorId:string|null; durableRecovery:boolean; review:MergePreview|null
  pendingMutationId:string|null; error:string|null; quoteState:string; failure?:SaveFailure|null
}
type Listener=(view:SessionView)=>void
const clone=<T>(value:T):T=>structuredClone(value)
const same=(a:unknown,b:unknown)=>JSON.stringify(a)===JSON.stringify(b)

export class QuoteSession {
  readonly quoteId:string; readonly tenantId:string; readonly principalId:string; clientSessionId:string
  private readonly tabKey:string; private readonly instanceId=crypto.randomUUID(); private channel:BroadcastChannel|null=null
  private probe:{nonce:string;collided:boolean}|null=null
  private tabPersistent=true
  private listeners=new Set<Listener>(); private generation=0; private editSequence=0; private disposed=false
  private inFlight:Promise<void>|null=null; private checking:Promise<void>|null=null; private pending:{id:string;revision:number;document:QuoteDocumentData;editSequence:number}|null=null
  private base:QuoteDocumentData|null=null; private reviewedTheirs:QuoteDraft|null=null; private baseVersion:number|undefined
  private ownMutations=new Set<string>(); private checkTimer:number|undefined; private checkDelay=4000
  view:SessionView={local:'loading',remote:'current',working:null,baseRevision:0,highestRemoteRevision:0,remoteActorId:null,durableRecovery:true,review:null,pendingMutationId:null,error:null,quoteState:'draft'}
  constructor(scope:{quoteId:string;tenantId:string;principalId:string;clientSessionId?:string}) {
    this.quoteId=scope.quoteId;this.tenantId=scope.tenantId;this.principalId=scope.principalId
    this.tabKey=`aeon-quote-tab:${scope.tenantId}:${scope.principalId}:${scope.quoteId}`
    let stored:string|null=null
    if(!scope.clientSessionId) {
      try { stored=sessionStorage.getItem(this.tabKey) } catch { this.tabPersistent=false }
    }
    this.clientSessionId=scope.clientSessionId??stored??crypto.randomUUID()
    if(!scope.clientSessionId) this.persistSessionId()
    try { this.channel=new BroadcastChannel('aeon-quote-tab-sessions')
      this.channel.onmessage=(event:MessageEvent<{kind:string;session:string;nonce?:string;to?:string;instance?:string}>)=>{
        const message=event.data;if(message.session!==this.clientSessionId || message.instance===this.instanceId)return
        if(message.kind==='probe' && message.nonce)this.channel?.postMessage({kind:'present',session:this.clientSessionId,to:message.nonce,instance:this.instanceId})
        const probe=this.probe;if(message.kind==='present' && probe && message.to===probe.nonce)probe.collided=true
      }
    } catch { this.channel=null }
  }
  private persistSessionId():void {
    try { sessionStorage.setItem(this.tabKey,this.clientSessionId) }
    catch { this.tabPersistent=false }
    // Without a persistent tab ID, a durable draft cannot be found on reload.
    if(!this.tabPersistent) this.view.durableRecovery=false
  }
  private async ensureUniqueTab():Promise<void> {
    if(!this.channel)return
    const probe={nonce:crypto.randomUUID(),collided:false};this.probe=probe
    this.channel.postMessage({kind:'probe',session:this.clientSessionId,nonce:probe.nonce,instance:this.instanceId})
    await new Promise<void>(resolve=>window.setTimeout(resolve,80))
    if(this.probe===probe && probe.collided){this.clientSessionId=crypto.randomUUID();this.persistSessionId()}
    this.probe=null
  }
  subscribe(fn:Listener):()=>void {this.listeners.add(fn);fn(this.view);return()=>this.listeners.delete(fn)}
  private emit():void {for(const fn of this.listeners) fn(this.view)}
  private scope() {return {tenantId:this.tenantId,principalId:this.principalId,quoteId:this.quoteId,sessionId:this.clientSessionId}}
  private recovery():RecoveryDraft|null {return this.base && this.view.working ? {...this.scope(),baseRevision:this.view.baseRevision,baseVersion:this.baseVersion,base:clone(this.base),mine:clone(this.view.working),pendingMutationId:this.pending?.id,savedAt:Date.now(),schemaVersion:1}:null}
  // A copy is kept only when it holds work the saved draft does not: a read-only
  // quote or an unchanged draft leaves none behind to offer later (and never
  // overwrites a copy that does hold work).
  private async persist():Promise<void> {
    const value=this.recovery();if(!value || !holdsWork(value))return
    const ok=await saveRecovery(value);if(!this.disposed){this.view.durableRecovery=ok && this.tabPersistent;this.emit()}
  }
  async open():Promise<RecoveryDraft|null> {
    const generation=++this.generation
    await this.ensureUniqueTab()
    if(this.disposed || generation!==this.generation)return null
    const server=await getDraft(this.quoteId)
    if(this.disposed || generation!==this.generation) return null
    this.base=clone(server.document);this.view.working=clone(server.document);this.view.baseRevision=server.draft_revision;this.baseVersion=typeof server.base_version==='number'?server.base_version:undefined
    this.view.highestRemoteRevision=server.draft_revision;this.view.local='clean';this.view.error=null;this.view.failure=null;this.emit()
    this.scheduleCheck();window.addEventListener('focus',this.focus);document.addEventListener('visibilitychange',this.visibility);window.addEventListener('online',this.online)
    const stored=await loadRecovery(this.scope())
    if(!stored)return null
    // A copy with nothing in it, or one from another version's draft (it was issued and
    // revised since), is never offered: it would put the wrong content on this draft.
    const otherVersion=stored.baseVersion!==undefined && this.baseVersion!==undefined && stored.baseVersion!==this.baseVersion
    if(!holdsWork(stored) || otherVersion || stored.baseRevision>server.draft_revision){await clearRecovery(this.scope()).catch(()=>{});return null}
    // Restoration always requires a user decision; opening cannot replace the server copy.
    return stored
  }
  restore(stored:RecoveryDraft):void {
    // An issued quote never takes local work: it goes into a revision instead (adopt).
    if(this.view.local==='read-only' || this.view.quoteState!=='draft')return
    if(this.disposed || !this.base || !this.view.working || stored.tenantId!==this.tenantId || stored.principalId!==this.principalId || stored.quoteId!==this.quoteId || stored.sessionId!==this.clientSessionId) return
    this.base=clone(stored.base);this.view.baseRevision=stored.baseRevision;this.view.working=clone(stored.mine)
    this.view.local='dirty';this.editSequence++;this.view.remote=stored.baseRevision<this.view.highestRemoteRevision?'newer':'current';this.emit();void this.persist()
  }
  // Unsaved work from before the quote was issued, carried into its new revision: the
  // work (its base to mine) is merged onto the fresh draft. Where both changed the
  // same place, the review decides, as for any newer draft.
  async adopt(stored:RecoveryDraft):Promise<'applied'|'review'|'refused'> {
    if(this.disposed || !this.base || !this.view.working || this.view.local==='read-only' || this.view.quoteState!=='draft' || stored.tenantId!==this.tenantId || stored.principalId!==this.principalId || stored.quoteId!==this.quoteId) return 'refused'
    const preview=mergeQuote(stored.base,stored.mine,this.base)
    if(preview.document && !preview.conflicts.length){
      this.view.working=preview.document;this.editSequence++;this.view.local=same(this.base,preview.document)?'clean':'dirty';this.emit();void this.persist();return 'applied'
    }
    this.base=clone(stored.base);this.view.working=clone(stored.mine);this.editSequence++;this.view.local='dirty';this.view.remote='newer';this.emit()
    await this.reviewChanges();return 'review'
  }
  // Drops the copy offered on opening without touching the saved draft.
  async discardRecovery():Promise<void> {await clearRecovery(this.scope()).catch(()=>{})}
  edit(document:QuoteDocumentData):void {
    if(this.disposed || !this.base || this.view.local==='read-only') return
    if(this.view.local==='failed')this.view.failure=null
    this.view.working=clone(document);this.editSequence++
    if(this.view.local!=='saving' && this.view.local!=='conflict') this.view.local=same(this.base,document)?'clean':'dirty'
    // Edited back to the saved draft: no copy is left to offer.
    this.emit();if(same(this.base,document) && !this.pending)void clearRecovery(this.scope()).catch(()=>{});else void this.persist()
  }
  onNotice(notice:QuoteNotice):void {
    if(this.disposed)return
    if(notice.state!=='draft') {this.view.quoteState=notice.state;if(this.view.local!=='clean') void this.persist();this.view.local='read-only'}
    if(notice.draft_revision<=this.view.baseRevision) {this.emit();return}
    const own=notice.client_session_id===this.clientSessionId && !!notice.mutation_id && (notice.mutation_id===this.pending?.id || this.ownMutations.has(notice.mutation_id))
    this.view.highestRemoteRevision=Math.max(this.view.highestRemoteRevision,notice.draft_revision)
    if(!own){this.view.remote='newer';this.view.remoteActorId=notice.actor_principal_id}
    this.emit()
  }
  onPresence(snapshot:PresenceSnapshot):void {
    if(this.disposed)return
    this.view.quoteState=snapshot.state
    if(snapshot.state!=='draft'){if(this.view.local!=='clean')void this.persist();this.view.local='read-only'}
    if(snapshot.draft_revision>this.view.baseRevision){this.view.highestRemoteRevision=Math.max(snapshot.draft_revision,this.view.highestRemoteRevision)
      // A pending own mutation may already have committed before its HTTP ACK.
      if(!this.pending || snapshot.draft_revision>this.pending.revision+1) this.view.remote='newer'
    }
    this.emit()
  }
  async save():Promise<void> {
    if(this.disposed || !this.base || !this.view.working || this.view.local==='read-only' || this.view.local==='conflict' || this.view.remote==='newer') return
    if(this.inFlight) return this.inFlight
    if(same(this.base,this.view.working)){this.view.local='clean';this.emit();return}
    if(!this.pending)this.pending={id:crypto.randomUUID(),revision:this.view.baseRevision,document:clone(this.view.working),editSequence:this.editSequence}
    const sent=this.pending;this.view.local='saving';this.view.pendingMutationId=sent.id;this.emit();void this.persist()
    this.inFlight=(async()=>{
      try {
        const ack=await saveDraft(this.quoteId,sent.revision,sent.document,this.clientSessionId,sent.id)
        if(this.disposed || this.pending!==sent)return
        await this.acknowledge(sent,ack)
      } catch(e) {
        if(this.disposed || this.pending!==sent)return
        if(e instanceof APIError && (e.status===412 || e.status===409)) {this.view.local='conflict';this.view.remote='newer';this.pending=null;await this.reviewChanges().catch(()=>{})}
        else if(e instanceof TypeError) {this.view.local='offline';this.view.remote='unavailable';this.view.failure=null;this.view.error=e.message}
        else {
          const status=e instanceof APIError?e.status:0
          const kind:SaveFailure['kind']=status===400 || status===413 || status===422?'invalid':status===401 || status===403?'forbidden':'server'
          const message=e instanceof Error?e.message:'Save failed'
          this.view.local='failed';this.view.error=message;this.view.failure={kind,status,message,body:e instanceof APIError?e.body:{}}
          // A refused document did not commit: the next save sends what is on the paper
          // then, under a new mutation. A passing failure keeps its mutation for the retry.
          if(kind==='server')this.view.remote='unavailable'
          else {this.pending=null;this.view.pendingMutationId=null}
        }
        this.emit();void this.persist()
      } finally {this.inFlight=null}
    })()
    return this.inFlight
  }
  private async acknowledge(sent:NonNullable<QuoteSession['pending']>,ack:MutationReceipt):Promise<void> {
    this.ownMutations.add(sent.id);this.pending=null;this.view.pendingMutationId=null
    if(ack.acknowledged_revision!==sent.revision+1 || ack.current_revision<ack.acknowledged_revision) {this.view.local='conflict';this.view.remote='newer';await this.reviewChanges().catch(()=>{});return}
    if(ack.replayed && !ack.document) {
      // A lost response can be acknowledged after another writer has advanced.
      const server=await getDraft(this.quoteId)
      if(server.draft_revision!==ack.acknowledged_revision){this.view.local='conflict';this.view.remote='newer';await this.reviewChanges().catch(()=>{});return}
      ack.document=server.document
    }
    const canonical=ack.document??sent.document
    const late=this.editSequence!==sent.editSequence
    if(late && this.view.working) {
      const preview=mergeQuote(sent.document,this.view.working,canonical)
      if(!preview.document || preview.conflicts.length){this.view.local='conflict';this.view.review=preview;return}
      this.view.working=preview.document
    } else this.view.working=clone(canonical)
    this.base=clone(canonical);this.view.baseRevision=ack.acknowledged_revision
    this.view.highestRemoteRevision=Math.max(this.view.highestRemoteRevision,ack.current_revision)
    this.view.remote=this.view.highestRemoteRevision>this.view.baseRevision?'newer':'current'
    this.view.local=same(this.base,this.view.working)?'clean':'dirty';this.view.error=null;this.view.failure=null;this.emit()
    if(this.view.local==='clean') await clearRecovery(this.scope()).catch(()=>{})
    else {await this.persist();if(this.view.remote==='current') queueMicrotask(()=>{void this.save()})}
  }
  async retry():Promise<void> {if(this.view.local==='failed'||this.view.local==='offline') await this.save()}
  async reviewChanges():Promise<MergePreview|null> {
    if(!this.base || !this.view.working)return null
    const generation=this.generation;const edits=this.editSequence
    const theirs=await getDraft(this.quoteId)
    if(this.disposed || generation!==this.generation || edits!==this.editSequence)return null
    this.reviewedTheirs=theirs;this.view.highestRemoteRevision=Math.max(this.view.highestRemoteRevision,theirs.draft_revision)
    this.view.review=mergeQuote(this.base,this.view.working,theirs.document);this.view.remote='newer';this.emit();void this.persist()
    return this.view.review
  }
  async acceptReview(choices:ConflictChoices):Promise<boolean> {
    if(!this.base || !this.view.working || !this.reviewedTheirs || this.inFlight)return false
    const preview=mergeQuote(this.base,this.view.working,this.reviewedTheirs.document,choices)
    if(!preview.document || preview.conflicts.some(c=>!choices[c.path])){this.view.review=preview;this.emit();return false}
    this.base=clone(this.reviewedTheirs.document);this.view.baseRevision=this.reviewedTheirs.draft_revision;this.view.working=preview.document
    this.view.local='dirty';this.view.remote='current';this.view.review=null;this.reviewedTheirs=null;this.editSequence++;this.emit();await this.persist()
    await this.save();return same(this.base,this.view.working)
  }
  async reload(discardLocal=false):Promise<'loaded'|'review'|'stale'> {
    if(this.disposed)return 'stale'
    if(this.inFlight) {await this.inFlight;if(this.disposed)return 'stale'}
    if(this.view.local!=='clean' && !discardLocal){await this.reviewChanges();return 'review'}
    const generation=this.generation,edits=this.editSequence
    const server=await getDraft(this.quoteId)
    if(this.disposed || generation!==this.generation || edits!==this.editSequence)return 'stale'
    if(server.draft_revision<this.view.highestRemoteRevision){this.view.remote='newer';this.emit();return 'stale'}
    if(this.view.local!=='clean' && !discardLocal){await this.reviewChanges();return 'review'}
    this.base=clone(server.document);this.view.working=clone(server.document);this.view.baseRevision=server.draft_revision;this.baseVersion=typeof server.base_version==='number'?server.base_version:this.baseVersion
    this.view.highestRemoteRevision=server.draft_revision;this.view.remote='current';this.view.local='clean';this.view.review=null;this.view.error=null;this.view.failure=null;this.editSequence++;this.emit()
    if(discardLocal) await clearRecovery(this.scope()).catch(()=>{})
    return 'loaded'
  }
  async check():Promise<void> {
    if(this.disposed || this.checking)return this.checking??Promise.resolve()
    const generation=this.generation;this.view.remote=this.view.remote==='newer'?'newer':'checking';this.emit()
    this.checking=(async()=>{try{const server=await getDraft(this.quoteId)
      if(this.disposed || generation!==this.generation)return
      this.view.highestRemoteRevision=Math.max(this.view.highestRemoteRevision,server.draft_revision)
      if(this.view.remote==='newer' || this.view.highestRemoteRevision>this.view.baseRevision && (!this.pending || this.view.highestRemoteRevision>this.pending.revision+1))this.view.remote='newer'
      else this.view.remote=this.pending?'checking':'current'
      this.checkDelay=4000
    }catch{if(!this.disposed){this.view.remote='unavailable';this.checkDelay=Math.min(20000,this.checkDelay*2)}}finally{this.checking=null;if(!this.disposed){this.emit();this.scheduleCheck()}}})()
    return this.checking
  }
  private scheduleCheck():void {window.clearTimeout(this.checkTimer);if(!this.disposed)this.checkTimer=window.setTimeout(()=>{void this.check()},this.checkDelay)}
  private focus=()=>{void this.check()}
  // Back online: work kept while offline saves by itself.
  private online=()=>{if(this.view.local==='offline')void this.save()}
  private visibility=()=>{if(!document.hidden)void this.check()}
  exportRecovery():void {const value=this.recovery();if(value)downloadRecovery(value)}
  dispose():void {this.disposed=true;this.generation++;this.channel?.close();window.clearTimeout(this.checkTimer);window.removeEventListener('focus',this.focus);window.removeEventListener('online',this.online);document.removeEventListener('visibilitychange',this.visibility);if(this.view.local!=='clean')void this.persist();this.listeners.clear()}
}
