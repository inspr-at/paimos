<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { readPlanning, releaseName, type ItemPage, type PlanningItem, type PlanningRelease, type ReleasePage } from '../../lib/deliveryPlanning'
import KeyCap from '../KeyCap.vue'
import { visibleGap, type MoveSlot, type MoveSubject, type MoveTarget } from '../../lib/deliveryMoves'
const props = defineProps<{ subject: MoveSubject; initial: 'append'|'top'|'after'; person: string; releases: PlanningRelease[]; visibleItems: PlanningItem[]; advice: (subject: MoveSubject,target: MoveTarget) => string }>()
const emit = defineEmits<{ close: []; confirm: [target: MoveTarget] }>()
const dialog=ref<HTMLDialogElement>(), destination=ref(props.subject.kind === 'item' ? props.subject.record.release_id ?? '' : 'order')
const position=ref<'append'|'top'|'before'|'after'>(props.initial), search=ref(''), anchor=ref(''), feedback=ref(''), loading=ref(false)
const releasePage=shallowRef<PlanningRelease[]>(props.releases.slice(0,50)), choices=shallowRef<(PlanningItem|PlanningRelease)[]>([])
const capturedDestination=shallowRef<PlanningRelease|null>(props.subject.kind === 'item' ? props.releases.find(r=>r.release_id===props.subject.record.release_id) ?? null : null)
const selectedAnchor=shallowRef<PlanningItem|PlanningRelease|null>(null), gapCaption=ref('')
const chosenSlot=shallowRef<MoveSlot>({}), releaseCursor=ref(''), choiceCursor=ref('')
const isRelease=props.subject.kind==='release'
const id=isRelease ? props.subject.record.release_id : props.subject.record.item_id
const label=isRelease ? releaseName(props.subject.record as PlanningRelease) : (props.subject.record as PlanningItem).key
const mac=/Mac|iPhone|iPad/.test(navigator.platform)
let abort: AbortController|null=null, generation=0
const rowID=(row: PlanningItem|PlanningRelease)=>'item_id' in row?row.item_id:row.release_id
const rowName=(row: PlanningItem|PlanningRelease)=>'item_id' in row?`${row.key} · ${row.title}`:releaseName(row)
const target=computed<MoveTarget>(()=>({release:capturedDestination.value,slot:position.value==='append'?(isRelease?{position:'append' as const}:{}):position.value==='top'?{position:'top'}:chosenSlot.value}))
const anchorCaption=computed(()=>position.value==='before'||position.value==='after'?gapCaption.value:'')
const reason=computed(()=>props.advice(props.subject,target.value))
const needsAnchor=computed(()=>position.value==='before'||position.value==='after')
const canSave=computed(()=>!loading.value && !reason.value && !feedback.value && (!needsAnchor.value||!!anchor.value))
async function load(kind: 'releases'|'choices', cursor='') {
  abort?.abort(); abort=new AbortController(); const signal=abort.signal, token=++generation
  loading.value=true; feedback.value=''
  try {
    const query=new URLSearchParams({view:'planning',hide_closed:'false'})
    if(new TextEncoder().encode(search.value).length>200)throw new Error('Search is limited to 200 UTF-8 bytes.')
    if(search.value.trim()) query.set('q',search.value.trim())
    const source=kind==='releases'||isRelease?'active':destination.value?`release:${destination.value}` as const:'backlog:ranked' as const
    const answer=await readPlanning({project:props.subject.record.project_id,person:props.person,scope:'all',query:query.toString()},source,cursor,kind==='releases'||isRelease?50:200,signal)
    if(token!==generation||signal.aborted)return
    if((answer as ItemPage).next_cursor===cursor && cursor)throw new Error('The server returned a repeated cursor.')
    if(kind==='releases') { releasePage.value=(answer as ReleasePage).items; releaseCursor.value=(answer as ReleasePage).next_cursor??'' }
    else { choices.value=(answer as ItemPage|ReleasePage).items.filter(r=>rowID(r)!==id); choiceCursor.value=(answer as ItemPage|ReleasePage).next_cursor??'' }
  } catch(e) {if(token===generation&&!signal.aborted)feedback.value=e instanceof Error?e.message:'Choices could not be loaded.'}
  finally {if(token===generation)loading.value=false}
}
function chooseDestination() {
  const next=releasePage.value.find(r=>r.release_id===destination.value)??props.releases.find(r=>r.release_id===destination.value)??capturedDestination.value
  if(destination.value && next?.release_id!==destination.value){feedback.value='The destination changed. Reopen the move.';return}
  capturedDestination.value=destination.value&&next?{...next}:null
  anchor.value='';selectedAnchor.value=null;gapCaption.value='';chosenSlot.value={};choices.value=[];choiceCursor.value=''
  void load('choices')
}
function chooseAnchor() {
  const row=choices.value.find(r=>rowID(r)===anchor.value)
  if(!row)return
  // Retain the displayed-list gap when that anchor is on screen. A separate
  // searched page must not reinterpret it as a different physical intention.
  const visible=isRelease?props.releases:props.visibleItems.filter(r=>(r.release_id??'')===destination.value)
  const population=visible.some(r=>rowID(r)===anchor.value)?visible:choices.value
  try {
    chosenSlot.value=visibleGap(population,anchor.value,position.value==='after',id);selectedAnchor.value={...row}
    const gapId=chosenSlot.value.after_id??chosenSlot.value.before_id, gap=population.find(r=>rowID(r)===gapId)
    gapCaption.value=gap?`${chosenSlot.value.after_id?'After':'Before'} ${rowName(gap)}`:'';feedback.value=''
  }
  catch(e){feedback.value=e instanceof Error?e.message:'The anchor changed.'}
}
function changePosition() {anchor.value='';selectedAnchor.value=null;gapCaption.value='';chosenSlot.value={};feedback.value='';if(needsAnchor.value)void load('choices')}
function confirm(){if(canSave.value)emit('confirm',target.value)}
function keys(e:KeyboardEvent) {
  const field=e.target instanceof Element&&!!e.target.closest('input,textarea,select,[contenteditable="true"]')
  if(e.key==='Escape'){e.preventDefault();e.stopPropagation();if(field)(e.target as HTMLElement).blur();else emit('close');return}
  if(e.key==='Enter'&&(mac?e.metaKey:e.ctrlKey)&&!e.altKey){e.preventDefault();confirm();return}
  if(field||e.metaKey||e.ctrlKey||e.altKey)return
  if(['g','t','m'].includes(e.key)){e.preventDefault();position.value=e.key==='t'?'top':e.key==='m'?'after':'append';changePosition()}
}
watch(search,()=>{generation++;abort?.abort();loading.value=false;feedback.value='';anchor.value='';selectedAnchor.value=null;gapCaption.value='';chosenSlot.value={};choiceCursor.value='';releaseCursor.value='';choices.value=[];releasePage.value=[]},{flush:'sync'})
onMounted(async()=>{await nextTick();dialog.value?.showModal();dialog.value?.querySelector<HTMLButtonElement>('.cancel')?.focus({preventScroll:true});await load('releases');if(needsAnchor.value&&dialog.value?.open)void load('choices')})
onBeforeUnmount(()=>{generation++;abort?.abort()})
</script>
<template>
  <dialog ref="dialog" class="move-sheet" :aria-label="`Release for ${label}`" @keydown="keys" @cancel.prevent="emit('close')">
    <div class="move-frame">
      <header><h2>{{ isRelease?'Reorder':'Move' }} {{ label }}</h2></header>
      <div class="move-controls">
        <div class="move-commands" role="group" aria-label="Move commands">
          <button type="button" @click="position='append';changePosition()">{{ isRelease ? 'Move to end' : 'Move to release' }} <kbd>g</kbd></button>
          <button type="button" @click="position='top';changePosition()">Move to top <kbd>t</kbd></button>
          <button type="button" @click="position='after';changePosition()">Move after <kbd>m</kbd></button>
        </div>
        <label>Destination<select v-model="destination" :disabled="isRelease" aria-label="Move destination" @change="chooseDestination"><option v-if="isRelease" value="order">Upcoming order</option><template v-else><option value="">Backlog</option><option v-if="capturedDestination && !releasePage.some(r=>r.release_id===capturedDestination?.release_id)" :value="capturedDestination.release_id">{{ releaseName(capturedDestination) }}</option><option v-for="r in releasePage" :key="r.release_id" :value="r.release_id">{{ releaseName(r) }}</option></template></select></label>
        <label>Position<select v-model="position" aria-label="Move position" @change="changePosition"><option value="append">Append</option><option value="top">Global top</option><option value="before">Before work</option><option value="after">After work</option></select></label>
        <label>Find destination or work<input v-model="search" maxlength="200" aria-label="Find move choices" /></label>
        <div class="choice-actions"><button type="button" :disabled="loading" @click="load('releases')">Find releases</button><button type="button" :disabled="loading||!releaseCursor" @click="load('releases',releaseCursor)">More releases</button><button type="button" :disabled="loading||!needsAnchor" @click="load('choices')">Find work</button><button type="button" :disabled="loading||!needsAnchor||!choiceCursor" @click="load('choices',choiceCursor)">More work</button></div>
        <label>Anchor<select v-model="anchor" :disabled="!needsAnchor||loading" aria-label="Move anchor" @change="chooseAnchor"><option value="">Choose ranked work</option><option v-if="selectedAnchor && !choices.some(r=>rowID(r)===rowID(selectedAnchor!))" :value="rowID(selectedAnchor)">{{ rowName(selectedAnchor) }}</option><option v-for="row in choices" :key="rowID(row)" :value="rowID(row)">{{ rowName(row) }}</option></select></label>
        <div class="save-actions"><button type="button" class="cancel" @click="emit('close')">Cancel <kbd>Esc</kbd></button><button type="button" class="save" :disabled="!canSave" @click="confirm">Save move <KeyCap k="mod" /><KeyCap k="enter" /></button></div>
      </div>
      <div class="move-body" aria-live="polite"><p>{{ subject.record.title }}</p><p v-if="capturedDestination">Destination: {{ releaseName(capturedDestination) }}</p><p v-if="anchorCaption">{{ anchorCaption }}</p><p>{{ feedback||reason||(loading?'Loading choices…':needsAnchor&&!anchor?'Choose an anchor before saving.':target.slot.after_id?'The move follows the selected visible predecessor.':target.slot.before_id?'The move precedes the selected anchor.':position==='top'?'Place before all ranked work, including work hidden by filters.':'Append to ranked work, before new Backlog work.') }}</p><p v-if="choiceCursor || releaseCursor">More choices are available above.</p></div>
    </div>
  </dialog>
</template>
<style scoped>
.move-sheet { position: fixed; inset: 7vh auto auto 50%; transform: translateX(-50%); margin: 0; padding: 0; width: min(42rem,calc(100vw - 32px)); max-height: 86dvh; border: 1px solid var(--line); border-radius: 12px; color: var(--ink); background: var(--surface); box-shadow: var(--shadow-pop); }
.move-sheet::backdrop { background: var(--scrim); }
.move-frame { display: flex; flex-direction: column; max-height: 86dvh; }
header { padding: 16px 20px 8px; flex-shrink: 0; }
h2 { margin: 0; font-size: 17px; } header p { display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; margin: 6px 0 0; font-size: 13px; line-height: 18px; height: 36px; }
.move-controls { padding: 8px 20px; display: grid; grid-template-columns: minmax(0,1fr) minmax(0,1fr); gap: 10px; flex-shrink: 0; }
label { display: grid; gap: 5px; min-width: 0; font-size: 12px; }
select,input { width: 100%; min-width: 0; box-sizing: border-box; height: 44px; border: 1px solid var(--line); border-radius: 4px; background: var(--surface); color: var(--ink); padding: 0 8px; }
.move-commands,.choice-actions,.save-actions { grid-column: 1/-1; display: flex; gap: 6px; flex-wrap: wrap; }
.move-commands button { flex: 1; }
button { min-height: 44px; border: 0; background: transparent; color: var(--ink); border-radius: 4px; padding: 6px 10px; font-size: 12px; }
button:focus-visible,select:focus-visible,input:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.save-actions { justify-content: flex-end; }
button:hover:not(:disabled) { background: var(--row-hover); }
.save { background: var(--chip-teal-bg); color: var(--teal-ink); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.move-body { padding: 4px 20px 16px; overflow: auto; font-size: 13px; line-height: 1.5; color: var(--ink-2); }
kbd { white-space: nowrap; margin-left: 5px; font: 10px var(--mono); color: var(--ink-3); }
@media(max-width:720px) { .move-sheet { inset: 0; width: 100%; height: 100dvh; max-height: 100dvh; max-width: none; transform: none; border: 0; border-radius: 0; } .move-frame { height: 100dvh; max-height: 100dvh; padding-top: env(safe-area-inset-top); box-sizing: border-box; } .move-controls { padding-inline: 12px; } .move-body { flex: 1; min-height: 0; } .save-actions { position: absolute; bottom: 0; left: 0; right: 0; padding: 8px 12px calc(8px + env(safe-area-inset-bottom)); background: var(--surface); border-top: 1px solid var(--line); } .move-body { padding-bottom: calc(80px + env(safe-area-inset-bottom)); } }
</style>
