import assert from 'node:assert/strict'
import test from 'node:test'
import { readResearchGuide, registerResearchGuide, finishResearchGuide, researchGuideFramePage, researchFeatureSteps, researchGuideKey, researchGuidePages, type ResearchGuideScope } from './researchFeatureGuides'
const scope: ResearchGuideScope = { tenantId:'1',userId:'user-a',workbenchId:'workbench-a',knowledgeBaseId:'kb-a' }
function storage() { const map=new Map<string,string>();return {map,getItem:(key:string)=>map.get(key)??null,setItem:(key:string,value:string)=>map.set(key,value)} }

test('only an explicit successful isolated creation enrolls a workbench',()=>{
 const s=storage();assert.equal(readResearchGuide(s,scope),null)
 assert.equal(registerResearchGuide(s,scope,'legacy'),false);assert.equal(readResearchGuide(s,scope),null)
 assert.equal(registerResearchGuide(s,scope,'isolated'),true)
 assert.ok(researchGuidePages.every(page=>readResearchGuide(s,scope)?.pages[page]==='pending'))
 assert.equal(finishResearchGuide(s,scope,'market','skipped'),true)
 assert.equal(registerResearchGuide(s,scope,'isolated'),false)
 assert.equal(readResearchGuide(s,scope)?.pages.market,'skipped')
})
test('four feature pages persist independently across refreshes and re-entry',()=>{
 const s=storage();registerResearchGuide(s,scope,'isolated')
 for(const [index,page]of ['market','indicators','judgment','diagram'].entries()){
   const outcome=index%2?'completed':'skipped'
   assert.equal(finishResearchGuide(s,scope,page as any,outcome),true)
   const reopened=readResearchGuide({...s},scope)!
   assert.equal(reopened.pages[page as keyof typeof reopened.pages],outcome)
   for(const next of ['market','indicators','judgment','diagram'].slice(index+1))assert.equal(reopened.pages[next as keyof typeof reopened.pages],'pending')
 }
 assert.equal(readResearchGuide(s,scope)?.pages.home,'pending')
})
test('accounts, spaces, libraries and workbenches have separate progress',()=>{
 const s=storage();registerResearchGuide(s,scope,'isolated');finishResearchGuide(s,scope,'market','completed')
 for(const changed of ['tenantId','userId','workbenchId','knowledgeBaseId'] as const){const next={...scope,[changed]:'other'};assert.notEqual(researchGuideKey(next),researchGuideKey(scope));assert.equal(readResearchGuide(s,next),null);registerResearchGuide(s,next,'isolated');assert.equal(readResearchGuide(s,next)?.pages.market,'pending')}
 assert.equal(readResearchGuide(s,scope)?.pages.market,'completed')
 assert.notEqual(researchGuideKey({...scope,userId:'a:b',workbenchId:'c'}),researchGuideKey({...scope,userId:'a',workbenchId:'b:c'}))
})
test('corrupt, inaccessible and missing storage never auto-enroll existing resources',()=>{
 const s=storage();s.map.set(researchGuideKey(scope),'{');assert.equal(readResearchGuide(s,scope),null);assert.equal(registerResearchGuide(s,scope,'isolated'),false)
 assert.equal(readResearchGuide(s,{...scope,userId:''}),null)
 const broken={getItem(){throw Error('blocked')},setItem(){throw Error('blocked')}}
 assert.equal(readResearchGuide(broken,scope),null);assert.equal(registerResearchGuide(broken,scope,'isolated'),false);assert.equal(finishResearchGuide(broken,scope,'market','completed'),false)
})
test('the native frame must match origin, exact route, user, space and both bindings',()=>{
 const origin='https://test.local',context={tenant:1,user:'user-a',workbench:'workbench-a',knowledge_base:'kb-a'}
 const frame={origin,pathname:'/research/workbenches/workbench-a/sn',context}
 assert.equal(researchGuideFramePage(scope,frame,origin),'market')
 assert.equal(researchGuideFramePage(scope,{...frame,pathname:'/research/workbenches/workbench-a/'},origin),'home')
 assert.equal(researchGuideFramePage(scope,{...frame,pathname:'/research/workbenches/workbench-a/sn/'},origin),'market')
 for(const key of ['tenant','user','workbench','knowledge_base'])assert.equal(researchGuideFramePage(scope,{...frame,context:{...context,[key]:'wrong'}},origin),null)
 for(const pathname of ['/research/sn','/research/workbenches/other/sn','/research/workbenches/workbench-a/sn/indicators/one'])assert.equal(researchGuideFramePage(scope,{...frame,pathname},origin),null)
 assert.equal(researchGuideFramePage(scope,{...frame,origin:'https://other.local'},origin),null)
 assert.equal(researchGuideFramePage(scope,{...frame,context:null},origin),null)
})
test('a v2 tour belongs to the workbench and survives adding or removing data sources',()=>{
 const s=storage(), independent:ResearchGuideScope={...scope,knowledgeBaseId:'',scopeVersion:2,sourcesRevision:1}
 assert.equal(registerResearchGuide(s,independent,'isolated'),true)
 assert.equal(finishResearchGuide(s,independent,'market','completed'),true)
 const changed={...independent,knowledgeBaseId:'another-library',sourcesRevision:4}
 assert.equal(researchGuideKey(changed),researchGuideKey(independent))
 assert.equal(readResearchGuide(s,changed)?.pages.market,'completed')
 assert.equal(registerResearchGuide(s,changed,'isolated'),false)
 assert.notEqual(researchGuideKey(independent),researchGuideKey(scope))
 assert.equal(researchGuideKey({...independent,sourcesRevision:0}),'')
})
test('v2 frame tours reject an obsolete source revision without requiring a primary library',()=>{
 const independent:ResearchGuideScope={...scope,knowledgeBaseId:'',scopeVersion:2,sourcesRevision:3}
 const origin='https://test.local',context={tenant:1,user:'user-a',workbench:'workbench-a',knowledge_base:null,scope_version:2,sources_revision:3}
 const frame={origin,pathname:'/research/workbenches/workbench-a/sn',context}
 assert.equal(researchGuideFramePage(independent,frame,origin),'market')
 for(const changed of [{sources_revision:2},{sources_revision:'3'},{scope_version:1}])
   assert.equal(researchGuideFramePage(independent,{...frame,context:{...context,...changed}},origin),null)
})
test('functional tours only open read-only panels and never invoke generate/save/favorite',async()=>{
 const calls:string[]=[]
 const steps=Object.fromEntries(researchGuidePages.map(page=>[page,researchFeatureSteps(page,panel=>{calls.push(panel)})]))
 for(const list of Object.values(steps))for(const step of list)await step.before?.()
 assert.deepEqual(calls,['market','logic-analysis','logic-close','diagram','diagram-close'])
 assert.match(steps.market[2]!.description!,/自动加入/)
 assert.match(steps.indicators[1]!.description!,/书签/)
 assert.match(steps.judgment[2]!.description!,/不代表已生效/)
 assert.match(steps.judgment[3]!.description!,/数据缺失/)
 assert.match(steps.diagram[1]!.description!,/不会编造/)
})
