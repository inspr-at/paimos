// SPDX-License-Identifier: AGPL-3.0-only
// Structured terminal events, including explicitly skipped tests. No console
// text is mistaken for a pass, and nested subtests do not inflate case counts.
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
    if (['test:start','test:pass','test:fail'].includes(event.type)) {
      const { name, file, nesting, skip, details } = event.data
      yield JSON.stringify({type:event.type,name,fullName:fullName(event.data),file,nesting,skip:!!skip,duration:details?.duration_ms})+'\n'
    }
  }
}
