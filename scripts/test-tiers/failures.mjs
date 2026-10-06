// SPDX-License-Identifier: AGPL-3.0-only
import { appendFileSync } from 'node:fs'

// Keep diagnostics small even when an assertion emits a single enormous line.
export function outputTail(text,{lines=40,characters=16_384}={}) {
  return String(text ?? '').trimEnd().split('\n').slice(-lines).join('\n').slice(-characters)
}

export function goFailures(text) {
  const output=new Map(),failed=new Map()
  for(const line of text.split('\n').filter(line=>line.trim())) {
    const event=JSON.parse(line),owner=(event.Package??event.ImportPath)?.replace(/^github\.com\/inspr-at\/paimos\//,'')
    if(!owner)continue
    const id=name=>JSON.stringify([owner,name??''])
    if(event.Action==='output'||event.Action==='build-output') {
      // A parent's tail includes failing subtests, but never another test's output.
      const names=['']
      if(event.Test) {
        const parts=event.Test.split('/')
        for(let i=1;i<=parts.length;i++)names.push(parts.slice(0,i).join('/'))
      }
      for(const name of names)output.set(id(name),outputTail((output.get(id(name))??'')+(event.Output??''))+'\n')
    }
    if(event.Action==='fail'||event.Action==='build-fail')failed.set(id(event.Test),{kind:'go',owner,name:event.Test??'(package)'})
  }
  const failedTests=new Set([...failed.values()].filter(row=>row.name!=='(package)').map(row=>row.owner))
  return [...failed].filter(([,failure])=>failure.name!=='(package)'||!failedTests.has(failure.owner))
    .map(([id,failure])=>({...failure,output:outputTail(output.get(id))}))
}

export function nodeFailures(text,owner,stderr='') {
  const events=text.split('\n').filter(Boolean).map(line=>JSON.parse(line))
  const output=events.filter(event=>['test:stdout','test:stderr','test:diagnostic'].includes(event.type))
    .map(event=>event.message??'').join('')
  return events.filter(event=>event.type==='test:fail'&&!event.skip).map(event=>({
    kind:'node',owner,name:event.fullName,output:outputTail([output,stderr,event.error].filter(Boolean).join('\n')),
  }))
}

export function vitestFailures(report,owner,capturedOutput='') {
  return (report.testResults??[]).flatMap(file=>(file.assertionResults??[])
    .filter(result=>result.status==='failed').map(result=>{
      const details=result.failureMessages?.join('\n')??''
      // Reserve half the total budget for each source so neither a verbose
      // assertion nor captured stdout/stderr can displace the other entirely.
      const budget={lines:20,characters:8_191}
      const output=details&&capturedOutput
        ? [outputTail(details,budget),outputTail(capturedOutput,budget)].join('\n')
        : outputTail(details||capturedOutput)
      return {kind:'vitest',owner,name:[...result.ancestorTitles,result.title].join(' > '),output}
    }))
}

export function printFailures(failures,{summary,log=console.log}={}) {
  if(!failures.length)return
  const text=failures.map(failure=>{
    const header=`FAIL ${failure.kind}: ${JSON.stringify(failure.owner)} — ${JSON.stringify(failure.name)} (tail: up to 40 lines / 16,384 characters)`
    const lines=outputTail(failure.output)||'(no captured output)'
    // Prefix captured lines so test output cannot become an Actions command.
    log(`${header}\n${lines.split('\n').map(line=>`  | ${line}`).join('\n')}`)
    return `${header}\n${lines}`
  }).join('\n\n')
  if(summary) {
    let longest=0
    for(const match of text.matchAll(/`+/g))longest=Math.max(longest,match[0].length)
    const fence='`'.repeat(Math.max(3,longest+1))
    appendFileSync(summary,`\n### Failing tier tests\n\nFull captured output remains in the tier artifacts.\n\n${fence}text\n${text}\n${fence}\n`)
  }
}
