// SPDX-License-Identifier: AGPL-3.0-only
import { createApp, h, ref } from 'vue'
import Overlay from '../src/components/quotes/collaboration/QuotePresenceOverlay.vue'
import type { QuoteDocumentData } from '../src/lib/quotes/types'
import type { PresenceSnapshot } from '../src/lib/quotePresence'

export async function mountOverlay(doc:QuoteDocumentData,sectionId:string,nodeId:string):Promise<string> {
  const root=document.createElement('div')
  root.innerHTML=`<section data-section-id="${sectionId}"><span data-text-id="${nodeId}">A😀B</span></section>`
  document.body.appendChild(root)
  const host=document.createElement('div');document.body.appendChild(host)
  const model=ref(doc)
  ;(window as unknown as {quoteOverlayModel:typeof model}).quoteOverlayModel=model
  const hash=[...new Uint8Array(await crypto.subtle.digest('SHA-256',new TextEncoder().encode('A😀B')))].map(b=>b.toString(16).padStart(2,'0')).join('')
  const presence:PresenceSnapshot={sessions:[{session_id:'session-1',principal_id:'other',name:'Alex',mode:'editing',observed_revision:1,expires_at:'2099-01-01',anchor:{section_id:sectionId,node_id:nodeId,observed_revision:1,text_sha256:hash,anchor:1,focus:3,fidelity:'precise'}}],draft_revision:1,quote_revision:1,state:'draft'}
  createApp({render:()=>h(Overlay,{root,document:model.value,revision:1,principalId:'mine',presence})}).mount(host)
  return JSON.stringify(model.value)
}
