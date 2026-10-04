// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it, vi } from 'vitest'
import { effectScope, nextTick, ref } from 'vue'
import { APIError } from '../src/lib/api'
import { moveAdvice, refusal, visibleGap, type MoveSubject } from '../src/lib/deliveryMoves'
import { usePlanningMoves } from '../src/lib/usePlanningMoves'
import type { PlanningItem, PlanningRelease } from '../src/lib/deliveryPlanning'
const item=(id:string,release='a',rank='V'):PlanningItem=>({item_id:id,project_id:'project',release_id:release,rank,revision:rank?1:0,node_revision:'node',title:id,key:id,kind:'ticket',state:'open',created_at:'',estimated_hours:null,expedite:false,due_on:null})
const release=(id:string,rank:string):PlanningRelease=>({release_id:id,project_id:'project',rank,revision:1,visibility:'internal',state:'planned',title:id,rollup:{units:1,completed:0,open_hours:1},build_summary:{}})
describe('canonical placement',()=>{
 it('sends one anchor across hidden gaps and partial page boundaries',()=>{
  const rows=[item('a'),item('b'),item('moved')]
  expect(visibleGap(rows,'b',false,'moved')).toEqual({after_id:'a'})
  expect(visibleGap(rows,'a',true,'moved')).toEqual({after_id:'a'})
  expect(visibleGap(rows,'a',false,'moved')).toEqual({before_id:'a'})
  expect(visibleGap([item('last-page-anchor')],'last-page-anchor',true,'moved')).toEqual({after_id:'last-page-anchor'})
  expect(()=>visibleGap([item('tail','', '')],'tail',true,'moved')).toThrow('anchor changed')
  expect(()=>visibleGap(rows,'moved',true,'moved')).toThrow('anchor changed')
 })
 it('allows an agent final gap in a later release and explains known refusals',()=>{
  const a=release('a','B'),b=release('b','D'),subject:MoveSubject={kind:'item',record:item('work')}
  expect(moveAdvice(subject,{release:b,slot:{before_id:'peer'}},[a,b],true,true)).toBe('')
  expect(moveAdvice(subject,{release:a,slot:{position:'top'}},[a,b],true,true)).toContain('later')
  b.state='frozen';expect(moveAdvice(subject,{release:b,slot:{}},[a,b],false,true)).toContain('frozen')
  b.state='planned';b.rollup.units=1000;expect(moveAdvice(subject,{release:b,slot:{}},[a,b],false,true)).toContain('full');b.rollup.units=1;
  expect(refusal(new APIError(409,'Refused',{code:'rank_space_exhausted'}))).toContain('rank space');expect(refusal(new APIError(409,'Unknown specific refusal',{code:'new_reason'}))).toBe('Unknown specific refusal');
  b.entry_closes_at='2026-01-01';expect(moveAdvice(subject,{release:b,slot:{}},[a,b],true,true,Date.UTC(2026,9,4))).toContain('Entry has closed')
  a.visibility='published';expect(moveAdvice({kind:'release',record:a},{release:b,slot:{}},[a,b],false,true)).toContain('Published releases')
  expect(moveAdvice(subject,{release:b,slot:{}},[a,b],false,false)).toContain('permission')
 })
 it('invalidates drafts on person/query changes and refuses a stale subject before writing',async()=>{
  vi.stubGlobal('cancelAnimationFrame',vi.fn())
  vi.stubGlobal('document',{activeElement:null})
  const identity=ref('owner'),rows=ref([item('work')]),scope=effectScope(),commit=vi.fn()
  const state=scope.run(()=>usePlanningMoves({root:ref(),identity:()=>identity.value,releases:()=>[],items:()=>rows.value,scroll:()=>null,agent:()=>false,allowed:()=>true,stale:()=>false,actions:{identity:()=>identity.value,begin:()=>identity.value,commit,failed:vi.fn(),receipt:vi.fn()}}))!
  state.openItem('work');expect(state.draft.value?.record.revision).toBe(1)
  rows.value=[{...item('work'),revision:2}];state.confirm({release:null,slot:{}});expect(state.feedback.value).toContain('changed');expect(commit).not.toHaveBeenCalled()
  identity.value='another-person';await nextTick();expect(state.draft.value).toBeNull();expect(state.feedback.value).toBe('')
  scope.stop();vi.unstubAllGlobals()
 })
})
