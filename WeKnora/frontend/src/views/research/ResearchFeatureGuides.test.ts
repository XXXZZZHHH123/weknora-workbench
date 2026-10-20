import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'
import { computed,effectScope,markRaw,nextTick,reactive,ref,watch } from 'vue'
import { finishResearchGuide,readResearchGuide,registerResearchGuide,researchFeatureSteps,researchGuideFramePage,researchGuideKey } from './researchFeatureGuides'
const source=readFileSync(new URL('./ResearchFeatureGuides.vue',import.meta.url),'utf8')
const script=source.split('<script setup lang="ts">')[1]!.split('</script>')[0]!.replace(/^import .*$/gm,'')
const code=ts.transpileModule(script+'\n;({active,page,acknowledge,openPanel,currentFrame})',{compilerOptions:{target:ts.ScriptTarget.ES2022,module:ts.ModuleKind.ESNext}}).outputText
function fixture(enrolled=true){
 const auth=reactive({token:'token',selectedTenantId:1,tenant:{id:1},user:{id:'user-a'}})
 const values=new Map<string,string>(),timers=new Map<number,()=>void>(),clicked:string[]=[],warnings:string[]=[],unmounts:Array<()=>void>=[]
 const storage={getItem:(key:string)=>values.get(key)??null,setItem:(key:string,value:string)=>values.set(key,value)}
 const scope={tenantId:'1',userId:'user-a',workbenchId:'wb-a',knowledgeBaseId:'kb-a'}
 if(enrolled)registerResearchGuide(storage,scope,'isolated')
 let timer=0,otherGuide=false,globalDone=true
 const doc:any={querySelector:(selector:string)=>selector==='#judgment-manager'?{open:false}:selector==='#dg-md-drawer'?{classList:{contains:()=>true}}:{click:()=>clicked.push(selector)}}
 const native:any={location:{origin:'https://test.local',pathname:'/research/workbenches/wb-a/sn'},RESEARCH_CONTEXT:{tenant:1,user:'user-a',workbench:'wb-a',knowledge_base:'kb-a'},addEventListener(){},removeEventListener(){}}
 const frame:any=markRaw({isConnected:true,contentWindow:native,contentDocument:doc});doc.defaultView=native;native.frameElement=frame
 const props=reactive({workbench:{id:'wb-a',knowledge_base_id:'kb-a',workspace_mode:'isolated'},frame,frameVersion:1,suppressed:false})
 const effects=effectScope()
 const view:any=effects.run(()=>runInNewContext(code,{computed,nextTick,ref,watch,defineProps:()=>props,useAuthStore:()=>auth,
  finishResearchGuide,readResearchGuide,researchFeatureSteps,researchGuideFramePage,researchGuideKey,
  window:{localStorage:storage,location:{origin:'https://test.local'}},document:{querySelector:()=>otherGuide?{}:null},
  MessagePlugin:{warning:(value:string)=>warnings.push(value)},isGlobalUserGuideDone:()=>globalDone,
  setTimeout:(fn:()=>void)=>{timers.set(++timer,fn);return timer},clearTimeout:(id:number)=>timers.delete(id),onBeforeUnmount:(fn:()=>void)=>unmounts.push(fn)}))
 return {view,auth,props,scope,storage,native,doc,clicked,warnings,timers,setOther:(value:boolean)=>{otherGuide=value},setGlobal:(value:boolean)=>{globalDone=value},
  async advance(){await nextTick();const pending=[...timers.values()];timers.clear();pending.forEach(fn=>fn());await nextTick()},
  close(){unmounts.forEach(fn=>fn());effects.stop()}}
}
test('existing workbenches without a creation ledger never auto-open',async()=>{
 const f=fixture(false);try{await f.advance();assert.equal(f.view.active.value,false);assert.equal(f.view.page.value,null);assert.deepEqual(f.clicked,[])}finally{f.close()}
})
test('skip and paired finish settle only this page; refresh does not reopen it',async()=>{
 const f=fixture();try{await f.advance();assert.equal(f.view.active.value,true);assert.equal(f.view.page.value,'market')
  f.view.acknowledge('skipped');f.view.acknowledge('completed');assert.equal(f.view.active.value,false)
  assert.equal(readResearchGuide(f.storage,f.scope)?.pages.market,'skipped')
  for(const page of ['indicators','judgment','diagram'] as const)assert.equal(readResearchGuide(f.storage,f.scope)?.pages[page],'pending')
  f.props.frameVersion++;await f.advance();assert.equal(f.view.active.value,false)
  f.native.location.pathname='/research/workbenches/wb-a/sn/indicators';f.props.frameVersion++;await f.advance();assert.equal(f.view.page.value,'indicators');assert.equal(f.view.active.value,true)
  f.view.acknowledge('completed');assert.equal(readResearchGuide(f.storage,f.scope)?.pages.indicators,'completed')
  f.props.frameVersion++;await f.advance();assert.equal(f.view.active.value,false)
 }finally{f.close()}
})
test('account or tenant changes cancel the old pending tour and cannot write another scope',async()=>{
 for(const changed of ['user','tenant'] as const){const f=fixture();try{
  await f.advance();assert.equal(f.view.active.value,true)
  if(changed==='user')f.auth.user.id='user-b';else f.auth.selectedTenantId=2
  await f.advance();assert.equal(f.view.active.value,false);f.view.acknowledge('completed')
  assert.equal(readResearchGuide(f.storage,f.scope)?.pages.market,'pending')
 }finally{f.close()}}
})
test('wrong frame source, origin or scope fails closed without opening native panels',async()=>{
 for(const changed of ['source','document','origin','workbench','knowledge-base'] as const){const f=fixture();try{
  if(changed==='source')f.native.frameElement={}
  if(changed==='document')f.doc.defaultView={}
  if(changed==='origin')f.native.location.origin='https://other.local'
  if(changed==='workbench')f.native.RESEARCH_CONTEXT.workbench='other'
  if(changed==='knowledge-base')f.native.RESEARCH_CONTEXT.knowledge_base='other'
  f.props.frameVersion++;await f.advance();assert.equal(f.view.active.value,false)
  await f.view.openPanel('market');assert.deepEqual(f.clicked,[])
 }finally{f.close()}}
})
test('native panels are measured without clicking a generator or a save action',async()=>{
 const f=fixture();try{
  await f.advance();await f.view.openPanel('market');assert.deepEqual(f.clicked,['#market-core-open'])
  f.props.frameVersion++;await f.advance();assert.equal(f.view.active.value,true)
  assert.ok(f.clicked.every(selector=>! /save|generate|favorite/.test(selector)))
  f.props.suppressed=true;await f.advance();assert.equal(f.view.active.value,false)
 }finally{f.close()}
})
test('welcome and other overlays finish before a feature guide starts; leaving cancels timers',async()=>{
 const f=fixture();try{
  f.setGlobal(false);await f.advance();assert.equal(f.view.active.value,false);assert.equal(f.timers.size,1)
  f.setGlobal(true);f.setOther(true);await f.advance();assert.equal(f.view.active.value,false)
  f.setOther(false);await f.advance();assert.equal(f.view.active.value,true)
  f.props.frameVersion++;await nextTick();f.close();await f.advance();assert.equal(f.view.active.value,false);assert.equal(f.timers.size,0)
 }finally{f.close()}
})
