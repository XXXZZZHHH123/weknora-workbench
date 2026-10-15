import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'
import { computed, effectScope, nextTick, reactive, ref, watch } from 'vue'
import { createIsolatedResearchClient } from '../../api/isolatedResearch'
import { isolatedWorkbenchScope, templateById, workbenchModuleNames } from './workbenchTemplates'
import { observationChart, reusableTemplate, structureBody, structureDetails, structureTree } from './workbenchArtifacts'

const source = readFileSync(new URL('./IsolatedResearchWorkbench.vue', import.meta.url), 'utf8')
const script = source.split('<script setup lang="ts">')[1]!.split('</script>')[0]!.replace(/^import .*$/gm, '')
const code = ts.transpileModule(`${script}\n;({ overview, sources, module, guideVisible, drawer, pendingFiles, recordTitle, recordBody, recordSources, recordConfirmed, records, guide, selectModule, selectFiles, enqueueFiles, editRecord, saveRecord, saveSourceRole, sourceStatus, isWorkbook, openSources, openImport, activeImport, chooseSheet, mapping, openDataset, activeDataset, datasetSeries, showObservations, observationPanels, allRecords, client, load })`, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext } }).outputText
const tick = () => new Promise(setImmediate)
function fixture(extra?: (path:string,options?:RequestInit)=>Response | Promise<Response> | undefined) {
  const props = reactive({ workbench: { id: 'fresh-workbench', knowledge_base_id: 'fresh-kb', workspace_mode: 'isolated', template_id: 'industrial', name: 'Test-only workspace', onboarding: { status: 'active', tasks: { sources: false, review: false, research: false } } } })
  const auth = reactive({ token: 'test-token', selectedTenantId: 'test-tenant', user: { id: 'test-user' } })
  const calls: Array<{url: string; method: string; body?: any}> = [], guides: any[] = [], batches: any[] = [], emits: any[] = [], cleanups: Array<() => void> = []
  const stored = new Map<string,string>()
  const network = async (url: RequestInfo | URL, options?: RequestInit) => {
    const path = String(url), body = options?.body ? JSON.parse(String(options.body)) : undefined
    calls.push({url:path,method:options?.method||'GET',body})
    const customized=extra?.(path,options);if(customized)return customized
    if (path.endsWith('/overview')) return Response.json({ workbench: { id: props.workbench.id, knowledge_base_id: props.workbench.knowledge_base_id, workspace_mode:'isolated' }, counts: { sources:0, documents_ready:0, excel_pending:0, indicators:0, observations:0 }, readiness:{status:'empty',tasks:[]},modules:[] })
    if (path.includes('/sources?')) return Response.json({ knowledge_base_id:props.workbench.knowledge_base_id,sources:[],datasets:[],total:0 })
    if (path.endsWith('/source-imports')) return Response.json({imports:[]})
    if (path.endsWith('/indicators')) return Response.json({indicators:[]})
    if (options?.method === 'POST') return Response.json({id:'new-record',...body,revision:1})
    return Response.json({items:[]})
  }
  const scope = effectScope()
  const view: any = scope.run(() => runInNewContext(code, {
    computed, nextTick, ref, watch, defineProps:()=>props, defineEmits:()=>((...args:any[])=>emits.push(args)), defineExpose() {},
    useAuthStore:()=>auth,useUploadTasksStore:()=>({enqueue:(input:any)=>batches.push(input)}),
    createIsolatedResearchClient:(captured:any,current:any)=>createIsolatedResearchClient(captured,current,network),
    updateResearchWorkbenchOnboarding:async (id:string,input:any)=>{guides.push({id,input});return {data:{...props.workbench,onboarding:{...props.workbench.onboarding,...input,tasks:{...props.workbench.onboarding.tasks,...input.tasks}}}}},
    MessagePlugin:{info(){},success(){}}, workspaceErrorMessage:(error:any)=>error?.message || 'failed',
    isolatedWorkbenchScope,templateById,workbenchModuleNames,observationChart,reusableTemplate,structureBody,structureDetails,structureTree,
    buildUploadFileName:(file:any)=>file.webkitRelativePath||undefined,
    window:{addEventListener(){},removeEventListener(){}},localStorage:{getItem:(key:string)=>stored.get(key)||null,setItem:(key:string,value:string)=>stored.set(key,value)},
    setTimeout,clearTimeout,URL,Blob,JSON,Date,
    onMounted(){},onBeforeUnmount:(fn:()=>void)=>cleanups.push(fn),
  }))
  return { props, auth, view, calls, guides, batches, emits, close(){cleanups.forEach(fn=>fn());scope.stop()} }
}

test('a newly created workbench stays truly empty and requests only its own evidence', async () => {
  const f=fixture()
  try {
    await nextTick();await tick()
    assert.equal(f.view.guideVisible.value,true)
    assert.equal(f.view.overview.value.counts.indicators,0)
    assert.equal(f.view.sources.value.length,0)
    assert.ok(f.calls.length>=4)
    assert.ok(f.calls.every(call=>call.url.startsWith('/research/api/workbenches/fresh-workbench/')))
    assert.ok(f.calls.every(call=>call.method==='GET'))
  } finally {f.close()}
})

test('dismissing and restoring the guide persists personal state without claiming business readiness', async () => {
  const f=fixture()
  try {
    await nextTick();await tick()
    await f.view.guide('dismissed')
    assert.equal(f.guides[0].id,'fresh-workbench')
    assert.equal(f.guides[0].input.status,'dismissed')
    Object.assign(f.props.workbench,f.emits[0][1])
    assert.equal(f.view.guideVisible.value,false)
    await f.view.guide('active')
    Object.assign(f.props.workbench,f.emits[1][1])
    assert.equal(f.view.guideVisible.value,true)
    assert.equal(f.view.overview.value.readiness.status,'empty')
    assert.equal(f.view.overview.value.counts.indicators,0)
  } finally {f.close()}
})

test('file confirmation preserves relative paths and binds the real knowledge base without creating research results', async () => {
  const f=fixture()
  try {
    await nextTick();await tick()
    f.view.selectFiles([{name:'fixture.xlsx',size:10,webkitRelativePath:'fixture-folder/fixture.xlsx'}])
    assert.equal(f.batches.length,0)
    f.view.enqueueFiles()
    assert.equal(f.batches.length,1)
    assert.equal(f.batches[0].kbId,'fresh-kb')
    assert.equal(f.batches[0].uploads[0].fileName,'fixture-folder/fixture.xlsx')
    assert.equal(f.view.pendingFiles.value.length,0)
    assert.equal(f.view.overview.value.counts.indicators,0)
  } finally {f.close()}
})

test('creating a draft does not silently cite the last opened source and records the research task separately', async () => {
  const f=fixture()
  try {
    await nextTick();await tick()
    f.view.selectModule('notes');await tick()
    f.view.editRecord()
    f.view.recordTitle.value='test-only note';f.view.recordBody.value='user supplied content'
    await f.view.saveRecord()
    const request=f.calls.find(call=>call.method==='POST'&&call.url.endsWith('/notes'))!
    assert.ok(request)
    assert.deepEqual(request.body.source_ids,[])
    assert.equal(request.body.status,'draft')
    assert.equal(request.body.confirmed,false)
    assert.equal(f.guides.at(-1).input.tasks.research,true)
  } finally {f.close()}
})

test('identity changes clear file selections and old workbench content before another request', async () => {
  const f=fixture()
  try {
    await nextTick();await tick()
    f.view.selectFiles([{name:'private-fixture.xlsx'}])
    f.auth.user.id='other-test-user'
    assert.equal(f.view.overview.value,null)
    assert.equal(f.view.sources.value.length,0)
    assert.equal(f.view.pendingFiles.value.length,0)
    assert.equal(f.view.drawer.value,null)
  } finally {f.close()}
})

test('image and archive originals use source_only while ordinary text and Excel keep native behavior', async () => {
  const f=fixture()
  try {
    await nextTick();await tick()
    f.view.selectFiles([{name:'fixture.txt'},{name:'fixture.xlsx'},{name:'fixture.png'},{name:'fixture.zip'}])
    f.view.enqueueFiles()
    assert.equal(f.batches.length,2)
    const ordinary=f.batches.find(batch=>!batch.processConfig)
    const originals=f.batches.find(batch=>batch.processConfig?.source_only)
    assert.deepEqual(Array.from(ordinary.uploads,(item:any)=>item.file.name),['fixture.txt','fixture.xlsx'])
    assert.deepEqual(Array.from(originals.uploads,(item:any)=>item.file.name),['fixture.png','fixture.zip'])
    assert.equal(originals.kbId,'fresh-kb')
  } finally {f.close()}
})

test('native stored originals show format-specific readiness instead of treating images as Excel', async () => {
  const f=fixture()
  try {
    assert.equal(f.view.sourceStatus({file_name:'fixture.xlsx',parse_status:'stored'}),'待结构化解析')
    assert.equal(f.view.sourceStatus({file_name:'fixture.png',parse_status:'stored'}),'原件已保存')
    assert.equal(f.view.sourceStatus({file_name:'fixture.zip',parse_status:'stored'}),'原件已保存')
    assert.equal(f.view.isWorkbook({file_name:'fixture.png'}),false)
    assert.equal(f.view.isWorkbook({file_name:'fixture.xlsx'}),true)
  } finally {f.close()}
})

test('a new-record click event cannot be interpreted as an existing record to PATCH', async () => {
  const f=fixture()
  try {
    await nextTick();await tick()
    f.view.selectModule('notes');await tick()
    f.view.editRecord({type:'click',target:{}})
    f.view.recordTitle.value='test-only click-created note'
    await f.view.saveRecord()
    assert.ok(f.calls.some(call=>call.method==='POST'&&call.url.endsWith('/notes')))
    assert.equal(f.calls.some(call=>call.method==='PATCH'),false)
  } finally {f.close()}
})


test('rapid import, sheet and dataset selection ignores late responses within the same workbench',async()=>{
  let importA!:(value:Response)=>void,sheetA!:(value:Response)=>void,datasetA!:(value:Response)=>void
  const f=fixture(path=>{
    if(path.endsWith('/source-imports/import-a'))return new Promise(resolve=>{importA=resolve})
    if(path.endsWith('/source-imports/import-b'))return Response.json({import_id:'import-b',sheets:[]})
    if(path.endsWith('/sheet?name=sheet-a'))return new Promise(resolve=>{sheetA=resolve})
    if(path.endsWith('/sheet?name=sheet-b'))return Response.json({mapping:{sheet:'sheet-b',blocks:[]}})
    if(path.includes('/datasets/dataset-a/'))return new Promise(resolve=>{datasetA=resolve})
    if(path.includes('/datasets/dataset-b/'))return Response.json({series:[{series_id:'field-b'}],total:1,next_offset:null})
  })
  try{
    await nextTick();await tick()
    const first=f.view.openImport('import-a');await f.view.openImport('import-b');importA(Response.json({import_id:'import-a',sheets:[]}));await first
    assert.equal(f.view.activeImport.value.import_id,'import-b')
    const oldSheet=f.view.chooseSheet('sheet-a');await f.view.chooseSheet('sheet-b');sheetA(Response.json({mapping:{sheet:'sheet-a',blocks:[]}}));await oldSheet
    assert.equal(f.view.mapping.value.sheet,'sheet-b')
    const oldDataset=f.view.openDataset({dataset_id:'dataset-a'});await f.view.openDataset({dataset_id:'dataset-b'});datasetA(Response.json({series:[{series_id:'field-a'}],total:1,next_offset:null}));await oldDataset
    assert.equal(f.view.activeDataset.value,'dataset-b')
    assert.equal(f.view.datasetSeries.value[0].series_id,'field-b')
  }finally{f.close()}
})

test('late trend pages cannot replace the selected indicator, while compare keeps two requested series',async()=>{
  const deferred=new Map<string,(response:Response)=>void>()
  const f=fixture(path=>{if(path.includes('/observations?'))return new Promise(resolve=>deferred.set(path.includes('/field-a/')?'field-a':'field-b',resolve))})
  const response=(id:string)=>Response.json({indicator:{series_id:id,name:id,unit:'test-unit'},observations:[],total:0,next_offset:null})
  try{
    await nextTick();await tick();f.view.selectModule('data')
    const a=f.view.showObservations({series_id:'field-a'}),b=f.view.showObservations({series_id:'field-b'})
    deferred.get('field-b')!(response('field-b'));await b;deferred.get('field-a')!(response('field-a'));await a
    assert.deepEqual(Array.from(f.view.observationPanels.value,(panel:any)=>panel.indicator.series_id),['field-b'])
    f.view.selectModule('compare')
    const compareA=f.view.showObservations({series_id:'field-a'}),compareB=f.view.showObservations({series_id:'field-b'})
    deferred.get('field-b')!(response('field-b'));await compareB;deferred.get('field-a')!(response('field-a'));await compareA
    assert.deepEqual(Array.from(f.view.observationPanels.value,(panel:any)=>panel.indicator.series_id),['field-a','field-b'])
  }finally{f.close()}
})

test('export pagination includes records beyond the first response and rejects incomplete fragments',async()=>{
  const f=fixture(path=>{
    if(path.includes('/notes?offset=0'))return Response.json({items:[{id:'one'},{id:'two'}],total:3,truncated:true})
    if(path.includes('/notes?offset=2'))return Response.json({items:[{id:'three'}],total:3,truncated:false})
    if(path.includes('/briefs?'))return Response.json({items:[],total:3,truncated:true})
  })
  try{
    await nextTick();await tick()
    const all=await f.view.allRecords(f.view.client(),'/notes','items')
    assert.deepEqual(Array.from(all,(item:any)=>item.id),['one','two','three'])
    await assert.rejects(f.view.allRecords(f.view.client(),'/briefs','items'),/未完整/)
  }finally{f.close()}
})
