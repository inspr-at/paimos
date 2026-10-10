// SPDX-License-Identifier: AGPL-3.0-only
// Structured terminal events, including explicitly skipped tests. No console
// text is mistaken for a pass, and nested subtests do not inflate case counts.
import { inspect } from 'node:util'
import { outputTail } from './failures.mjs'

export default async function* reporter(events) {
  const nodes=new Map()
  const fullName=data=>{
    const names=[data.name]
    let current=data,depth=data.nesting
    while(depth>0) {
      const parent=nodes.get(current.parentId)
      if(!parent||parent.nesting!==depth-1)throw new Error('Missing Node test ancestry')
      names.unshift(parent.name);current=parent;depth--
    }
    return names.join(' ')
  }
  for await (const event of events) {
    if(event.type==='test:enqueue')nodes.set(event.data.testId,event.data)
    if(['test:stdout','test:stderr','test:diagnostic'].includes(event.type)) {
      yield JSON.stringify({type:event.type,file:event.data.file,message:outputTail(event.data.message)+'\n'})+'\n'
    }
    if (['test:start','test:pass','test:fail'].includes(event.type)) {
      const { name, file, nesting, skip, details } = event.data
      yield JSON.stringify({type:event.type,name,fullName:fullName(event.data),file,nesting,skip:!!skip,duration:details?.duration_ms,
        ...(event.type==='test:fail'?{error:outputTail(inspect(details?.error,{colors:false,depth:5,maxStringLength:16_384}))}:{})})+'\n'
    }
  }
}
