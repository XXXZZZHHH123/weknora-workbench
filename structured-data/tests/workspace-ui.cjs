/* Execute shipped UI against synthetic local HTTP responses. No cloud or business writes. */
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const os = require('node:os');
const http = require('node:http');
const {once} = require('node:events');
const {launchBrowser, stopBrowser} = require('./browser-launcher.cjs');

const keepArtifacts=Boolean(process.env.STRUCTURED_UI_ARTIFACTS);
const output=keepArtifacts?path.resolve(process.env.STRUCTURED_UI_ARTIFACTS):fs.mkdtempSync(path.join(os.tmpdir(),'structured-workspace-artifacts-'));
const source=process.env.STRUCTURED_UI_SOURCE || path.join(__dirname,'..','src','structured_data');
fs.mkdirSync(output,{recursive:true});
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
const ids = {primary:'1'.repeat(24), secondary:'2'.repeat(24), import:'3'.repeat(24)};
const dates = ['2025-01-01','2025-01-02','2025-01-03'];
const values = [12.5,15,17.5];
const fixtureSheet = {
  name:'合成工作表', rows:4, columns:2, status:'needs_review', layout:'single_header',
  fingerprint:'synthetic-layout-only', reasons:[],
  mapping:{sheet:'合成工作表',fingerprint:'synthetic-layout-only',blocks:[{
    date_mode:'date',date_column:'A',header_rows:[1],data_start_row:2,data_end_row:4,
    columns:[{column:'B',metric:'合成指标一',unit:null,header_cells:['B1']}]
  }]}
};
const requests = [], checks = [], errors = [], networkFailures = [], fileChoosers = [];
let uploadCount=0,nativeStall='',pdfConfigured=process.argv.includes('--upload-regression'),pdfStall=false,pdfSourceDenied=false,pdfListDenied=false;
const layoutMode=process.argv.includes('--layout-regression');let emptyImports=false;
const layoutRecords=[{draft_id:'pdf-old',source:{file_name:'same.pdf',sha256:'b'.repeat(64)},fragment_count:2,raw_row_count:2,created_at:'2025-01-01T00:00:00Z'},{draft_id:'pdf-fixture',source:{file_name:'same.pdf',sha256:'b'.repeat(64)},fragment_count:2,raw_row_count:2,created_at:'2026-01-01T00:00:00Z'},{draft_id:'pdf-other',source:{file_name:'same.pdf',sha256:'c'.repeat(64)},fragment_count:1,raw_row_count:1,created_at:'2024-01-01T00:00:00Z'}];
let pdfAutomatic=false;
const jobs = new Map();
const nativeImports = new Map();
const nativeAttempts = new Map();
function catalogue(scope) {
  return [
    {dataset_id:ids.primary,source_file:`${scope || 'legacy'}_合成样本甲.xlsx`,dataset_kind:'business',is_latest_for_source:true,imported_at:'2025-01-04T00:00:00Z',sheets:[{name:fixtureSheet.name}]},
    {dataset_id:ids.secondary,source_file:`${scope || 'legacy'}_合成样本乙.xlsx`,dataset_kind:'business',is_latest_for_source:true,imported_at:'2025-01-05T00:00:00Z',sheets:[{name:fixtureSheet.name}]}
  ];
}
function series(dataset, scope) {
  const base={dataset_id:dataset,sheet:fixtureSheet.name,dimensions:{category:'隔离测试'},unit:'测试单位',first_valid_date:dates[0],last_valid_date:dates[2],date_mode:'date',valid_count:3,record_count:3};
  return [
    {...base,series_id:`${scope}:${dataset}:date`,metric:dataset===ids.primary?'合成指标一':'合成指标二',description:'仅为浏览器夹具生成的有日期记录'},
    {...base,series_id:`${scope}:${dataset}:period`,metric:'合成期间指标',description:'期间标签保留夹具原文',date_mode:'period',periods:['样本期甲','样本期乙'],first_valid_date:null,last_valid_date:null},
    {...base,series_id:`${scope}:${dataset}:none`,metric:'合成无日期指标',description:'没有时间字段的夹具记录',date_mode:'none',first_valid_date:null,last_valid_date:null}
  ];
}
function browserPath() {
  if(process.env.SHEET_REVIEW_BROWSER)return process.env.SHEET_REVIEW_BROWSER;
  const cache=path.join(os.homedir(),'.cache','ms-playwright');
  for(const item of fs.readdirSync(cache).filter(item=>item.startsWith('chromium-')).sort().reverse()){
    const binary=path.join(cache,item,'chrome-linux64','chrome');if(fs.existsSync(binary))return binary;
  }
  throw Error('No local Chromium executable found');
}

async function main(){
 const profile=fs.mkdtempSync(path.join(os.tmpdir(),'structured-refresh-'));
 let chrome,socket;
 const server=http.createServer(async(req,res)=>{
  const url=new URL(req.url,'http://localhost'),route=url.pathname.replace(/^\/structured(?=\/)/,'');
  const scope=req.headers['x-knowledge-base-id']||'';
  const chunks=[];for await(const chunk of req)chunks.push(chunk);const payload=Buffer.concat(chunks);
  requests.push({method:req.method,path:url.pathname,query:Object.fromEntries(url.searchParams),scope,tenant:req.headers['x-tenant-id']||'',authorization:req.headers.authorization||'',authorized:['Bearer fixture-token','Bearer fixture-renewed'].includes(req.headers.authorization||''),size:payload.length});
  if(nativeStall==='initial-refresh-renew'&&route==='/api/datasets'||nativeStall==='source-refresh-timeout'&&route==='/api/datasets'||nativeStall==='source-sheet-timeout'&&route.endsWith('/sheet'))await pause(800);
  let body={},type='application/json',status=200;
  if(route==='/harness'){
   const review=url.searchParams.get('review')==='1',legacy=url.searchParams.get('legacy')==='1',settings=url.searchParams.get('settings')==='1',nativeSource=url.searchParams.get('source')||'';
   body=`<!doctype html><html lang="zh-CN"><head><meta charset="UTF-8"><style>html,body{margin:0;height:100%;overflow:hidden}iframe{display:block;width:100%;height:100%;border:0}</style></head><body><iframe id="frame" src="/structured/${review?'?view=review&import_id='+ids.import:settings?'?view=model-settings':''}" title="隔离结构化页面"></iframe><script>const frame=document.getElementById('frame');window.auth=(kb='kb-a',token='fixture-token',sourceKnowledgeId='',userId='fixture-user',sourceFileType='xlsx')=>frame.contentWindow.postMessage({type:'structured-auth',token,tenant:10000,userId,knowledgeBaseId:kb,sourceKnowledgeId,sourceFileType},location.origin);window.view=mode=>frame.contentWindow.postMessage({type:'structured-view',mode},location.origin);addEventListener('message',e=>{if(e.origin!==location.origin||e.source!==frame.contentWindow)return;if(e.data?.type==='structured-ready'){auth(${legacy?"''":"'kb-a'"},'fixture-token',${JSON.stringify(nativeSource)});${legacy?'':"view('import')"}}else if(e.data?.type==='structured-refresh-auth'){frame.contentWindow.postMessage({type:'structured-auth-refreshed',requestId:e.data.requestId,token:e.data.token},location.origin)}else if(e.data?.type==='structured-navigate'&&e.data.view===''){frame.src='/structured/';}});</script></body></html>`;type='text/html';
  }else if(route.startsWith('/fonts/')&&!route.includes('..')){
   const target=path.join(__dirname,'../../WeKnora/frontend/public',route);if(fs.existsSync(target)){body=fs.readFileSync(target);type=route.endsWith('.css')?'text/css':'font/woff2';}else{status=404;body='Font not found';}
  }else if(route==='/'){body=fs.readFileSync(path.join(source,'index.html'),'utf8');type='text/html';}
  else if(['/sheet-review.js','/sheet-review.css','/workspace.css'].includes(route)){
   const target=path.join(source,route.slice(1));if(!fs.existsSync(target)){status=404;body={detail:'Fixture static resource absent'}}else{body=fs.readFileSync(target,'utf8');type=route.endsWith('.js')?'application/javascript':'text/css';}
  }else if(route==='/pdf-review.js'){
   body=fs.readFileSync(path.join(source,'static','pdf-review.js'),'utf8');type='application/javascript';
  }else if(route==='/api/pdf/config')body={configured:pdfConfigured,max_bytes:52428800};
  else if(route==='/api/pdf/import'&&req.method==='POST'){if(pdfStall)await pause(650);pdfAutomatic=payload.toString().includes('automatic-fixture.pdf');body={id:'pdf-native-job'};}
  else if(route==='/api/pdf/jobs')body={jobs:[]};
  else if(route==='/api/pdf/drafts'){if(pdfListDenied){status=403;body={detail:'隔离夹具：无法读取处理记录'};}else body={drafts:layoutMode?layoutRecords:[{draft_id:'pdf-fixture',source:{file_name:'fixture.pdf'},fragment_count:1,raw_row_count:1}]};}
  else if(route==='/api/pdf/tables')body={tables:layoutMode?[{table_id:'published-old',draft_id:'pdf-old',source:{file_name:'same.pdf',sha256:'b'.repeat(64)},title:'Reviewed data',columns:[{}],coverage:{logical_row_count:1}}]:[]};
  else if(route==='/api/pdf/import-knowledge'&&req.method==='POST'){
   const input=JSON.parse(payload),sourceId=input.source_knowledge_id;requests.at(-1).sourceKnowledgeId=sourceId;requests.at(-1).pdfKnowledgeImport=true;body={id:'pdf-native-job'};
  }else if(route==='/api/pdf/jobs/pdf-native-job')body={status:'completed',draft_id:'pdf-fixture',...(pdfAutomatic?{stage:'已自动入库 1 张表格',automatic_result:{status:'completed',accepted_count:1,attention_count:0}}:{})};
  else if(route==='/api/pdf/drafts/pdf-fixture')body={draft_id:'pdf-fixture',bundle_sha256:'a'.repeat(64),bundle:{source:{file_name:'fixture.pdf',sha256:'b'.repeat(64)},parser:{engine:'fixture',version:'1'},fragments:[{id:'fixture-fragment',page:1,rows:[[{text:'Synthetic PDF cell'}]]},...(layoutMode?[{id:'second-fragment',page:2,rows:[[{text:'Second synthetic table'},{text:'12345678901234567890'}]]}]:[])]}};
  else if(route==='/api/pdf/drafts/pdf-fixture/source'){
   if(pdfSourceDenied){status=403;body='Denied'}else{body=Buffer.from('%PDF-1.4 synthetic fixture');type='application/pdf';}
  }else if(route==='/api/ui-config')body={title:'结构化数据库',workspace_label:'隔离浏览器验收',weknora_url:'http://127.0.0.1'};
  else if(route==='/api/datasets')body={datasets:catalogue(scope)};
  else if(route==='/api/model-settings'&&req.method==='GET')body={native_model_id:'fixture-chat',protocol:'platform',can_edit:true,models:[{id:'fixture-chat',name:'Fixture configured chat'}]};
  else if(route==='/api/imports')body={imports:emptyImports?[]:[{import_id:ids.import,source_file:'同一合成原件.xlsx',updated_at:'2025-01-05T00:00:00Z'},...[...nativeImports.values()].filter(row=>row.scope===scope)]};
  else if(route.endsWith('/sheet-view'))body={sheet_name:fixtureSheet.name,total_rows:4,total_cols:2,range:{start_row:1,end_row:4,start_col:1,end_col:2,row_count:4,col_count:2},cells:[{row:1,column:1,cell:'A1',value:'日期',display:'日期'},{row:1,column:2,cell:'B1',value:'合成指标一',display:'合成指标一'},...dates.flatMap((date,i)=>[{row:i+2,column:1,cell:`A${i+2}`,value:date,display:date},{row:i+2,column:2,cell:`B${i+2}`,value:values[i],display:String(values[i])}])],styles:{},merges:[],column_widths:{},row_heights:{},warnings:[]};
  else if(route.endsWith('/sheet')){const restored=route.includes('6'.repeat(24));body={...fixtureSheet,status:restored?'confirmed':fixtureSheet.status,published_in_dataset:restored,published_dataset_id:restored?ids.secondary:null,published_mapping:restored?fixtureSheet.mapping:null,published_fingerprint:restored?fixtureSheet.fingerprint:null};}
  else if(route.endsWith('/report')){const dataset=route.split('/')[3],item=catalogue(scope).find(item=>item.dataset_id===dataset);await pause(250);body={manifest:{...item,series_count:12417,valid_count:608180,observation_count:1048771,issue_count:72945,sha256:'synthetic-source-fingerprint',excluded_regions:[]},issues:[{sheet:fixtureSheet.name,cell:'B5',message:'夹具范围外原值不参与查询'}]};}
  else if(route.endsWith('/series')){const dataset=route.split('/')[3],search=url.searchParams.get('search')||'';const candidates=route==='/api/series'?catalogue(scope).flatMap(item=>series(item.dataset_id,scope).map(row=>({...row,source_file:item.source_file}))):series(dataset,scope);const rows=candidates.filter(row=>!search||`${row.metric} ${row.sheet}`.includes(search));body={series:rows,total:rows.length,truncated:false};}
  else if(route.endsWith('/charts'))body={charts:[],sheets:[],total:0,next_offset:null};
  else if(route==='/api/query'){
   const p=url.searchParams,dataset=p.get('dataset_id'),selected=series(dataset,scope).find(row=>row.series_id===p.get('series_id'));
   await pause(500);
   if(!selected){status=404;body={detail:'夹具中不存在当前库指标'};}
   else if(p.get('start_date')&&p.get('end_date')&&p.get('start_date')>p.get('end_date')){status=422;body={detail:'开始日期不能晚于结束日期（隔离夹具）'};}
   else{
    let rows=dates.map((date,i)=>({date:selected.date_mode==='date'?date:null,period:selected.date_mode==='period'?'样本期甲':null,value:values[i],source_cell:`B${i+2}`,date_cell:selected.date_mode==='date'?`A${i+2}`:null,period_cell:selected.date_mode==='period'?`A${i+2}`:null,value_status:'valid'}));
    if(selected.date_mode==='date')rows=rows.filter(row=>(!p.get('start_date')||row.date>=p.get('start_date'))&&(!p.get('end_date')||row.date<=p.get('end_date')));
    if(p.get('period')&&p.get('period')!=='样本期甲')rows=[];
    const aggregate=p.get('operation')==='sum'?rows.reduce((sum,row)=>sum+row.value,0):null;
    body={status:rows.length?'ok':'no_data',aggregate,valid_count:rows.length,record_count:rows.length,source:{file:catalogue(scope).find(item=>item.dataset_id===dataset).source_file,sheet:fixtureSheet.name},series:selected,rows,rows_are:'all',truncated:false,warnings:[]};
   }
  }else if(route==='/api/import-knowledge'&&req.method==='POST'){
   const input=JSON.parse(payload),sourceId=input.source_knowledge_id;requests.at(-1).restoreReviewedState=input.restore_reviewed_state;nativeStall=sourceId;requests.at(-1).sourceKnowledgeId=sourceId;nativeAttempts.set(sourceId,(nativeAttempts.get(sourceId)||0)+1);
   if(sourceId==='source-oversize'){status=413;body={detail:'原件超过允许的文件大小。'};}
   else if(sourceId==='source-timeout'){await pause(300);body={detail:'The fixture request should have been aborted'};}
   else if(sourceId==='source-expired'&&req.headers.authorization==='Bearer fixture-token'){status=401;body={detail:'隔离夹具：登录已过期，请重新登录'};}
   else if(sourceId==='source-denied'||sourceId==='source-retry'&&nativeAttempts.get(sourceId)===1){status=403;body={detail:'隔离夹具：当前知识库原件无读取权限'};}
   else{
    const importId=sourceId==='source-recovered'?'6'.repeat(24):sourceId==='source-new'?'5'.repeat(24):sourceId==='source-stale'?'6'.repeat(24):'4'.repeat(24);
    const row={import_id:importId,source_file:sourceId==='source-stale'?'迟到原件.xlsx':sourceId==='source-new'?'新知识库原件.xlsx':'知识库内原件.xlsx',updated_at:'2025-01-06T00:00:00Z',scope};nativeImports.set(importId,row);
    const result={import_id:importId,dataset_id:ids.secondary,source_knowledge_id:sourceId,recovered_legacy:sourceId==='source-recovered'};
    if(sourceId==='source-new'||sourceId==='source-poll-timeout'){const id=sourceId==='source-new'?'native-job':'native-poll-timeout';jobs.set(id,{count:0,nativeResult:result});body={id,status:'processing',source_knowledge_id:sourceId};}
    else{if(sourceId==='source-stale')await pause(650);body={status:'completed',reused:true,result,source_knowledge_id:sourceId==='source-mismatch'?'another-source':sourceId};}
   }
  }else if(route==='/api/import'&&req.method==='POST'){
   uploadCount++;const id=`fixture-job-${uploadCount}`;jobs.set(id,{count:0,failed:uploadCount===3});body={id};
  }else if(route.startsWith('/api/jobs/')){
   if(route.endsWith('/native-poll-timeout'))await pause(800);
   const job=jobs.get(route.split('/').at(-1));if(!job){status=404;body={detail:'Unknown synthetic job'}}else if(job.count++===0)body={status:'processing',stage:'正在处理隔离上传'};
   else if(job.failed)body={status:'failed',error:'夹具模拟：文件结构无法读取'};
   else body={status:'completed',result:job.nativeResult||{import_id:ids.import,dataset_id:ids.primary,elapsed_seconds:1.2,reused:false}};
  }else if(route.startsWith('/api/imports/')){const id=route.split('/')[3],row=nativeImports.get(id);body={import_id:id,source_file:row?.source_file||'同一合成原件.xlsx',sheets:[{...fixtureSheet,status:route.includes('6'.repeat(24))?'confirmed':fixtureSheet.status,published_in_dataset:route.includes('6'.repeat(24)),published_dataset_id:route.includes('6'.repeat(24))?ids.secondary:null}]};}
  else {status=404;body={detail:'Unsupported isolated route'};}
  res.writeHead(status,{'Content-Type':type});res.end(Buffer.isBuffer(body)?body:typeof body==='string'?body:JSON.stringify(body));
 });
 try{
  server.listen(0,'127.0.0.1');await once(server,'listening');const origin=`http://127.0.0.1:${server.address().port}`;
  const launched=await launchBrowser({executablePath:browserPath(),profileDir:profile});chrome=launched.browser;
  socket=new WebSocket(launched.page.webSocketDebuggerUrl);await once(socket,'open');let serial=0;const pending=new Map();
  socket.addEventListener('message',event=>{const message=JSON.parse(event.data);if(message.id&&pending.has(message.id)){pending.get(message.id)(message);pending.delete(message.id)}else if(message.method==='Runtime.exceptionThrown')errors.push(message.params.exceptionDetails.exception?.description||message.params.exceptionDetails.text);else if(message.method==='Network.loadingFailed')networkFailures.push(message.params.errorText);else if(message.method==='Page.fileChooserOpened')fileChoosers.push(message.params);});
  const call=async(method,params={})=>{const id=++serial,result=new Promise(resolve=>pending.set(id,resolve));socket.send(JSON.stringify({id,method,params}));const response=await result;if(response.error)throw Error(JSON.stringify(response.error));return response.result;};
  const js=async expression=>{const result=await call('Runtime.evaluate',{expression,awaitPromise:true,returnByValue:true});if(result.exceptionDetails)throw Error(result.exceptionDetails.exception?.description||result.exceptionDetails.text);return result.result.value;};
  const doc="document.getElementById('frame')?.contentDocument";
  const exec=expression=>js(`(()=>{const d=${doc},w=d?.defaultView;${expression}})()`);
  const wait=async(expression,timeout=15000)=>{const deadline=Date.now()+timeout;while(Date.now()<deadline){if(await exec(`if(!d?.getElementById('workspace'))return false;return ${expression}`))return;await pause(50)}throw Error('Timed out: '+expression);};
  const verify=async(name,expression)=>{assert.equal(await exec(`return ${expression}`),true,name);checks.push(name);};
  const click=selector=>exec(`d.querySelector(${JSON.stringify(selector)}).click()`);
  const input=async(selector,value)=>exec(`const el=d.querySelector(${JSON.stringify(selector)});el.value=${JSON.stringify(value)};el.dispatchEvent(new w.Event('input',{bubbles:true}));el.dispatchEvent(new w.Event('change',{bubbles:true}))`);
  const screenshot=async name=>{if(!keepArtifacts)return;await js("(document.getElementById('frame')?.contentDocument||document).fonts.ready.then(()=>true)");fs.mkdirSync(path.join(output,'screenshots'),{recursive:true});const shot=await call('Page.captureScreenshot',{format:'png',captureBeyondViewport:false});fs.writeFileSync(path.join(output,'screenshots',name+'.png'),Buffer.from(shot.data,'base64'));};
  const resize=async width=>{await call('Emulation.setDeviceMetricsOverride',{width,height:980,deviceScaleFactor:1,mobile:false});await pause(150);};
  const verifySheetHighlight=async theme=>{
    const original=await exec("return d.querySelector('.sheet-tab').getAttribute('aria-pressed')");
    const point=await js(`(()=>{const f=document.getElementById('frame'),outer=f.getBoundingClientRect(),r=f.contentDocument.querySelector('.sheet-tab').getBoundingClientRect();return {x:outer.left+r.left+r.width/2,y:outer.top+r.top+r.height/2}})()`);
    for(const selected of [false,true]){
      await exec(`d.querySelector('.sheet-tab').setAttribute('aria-pressed',${JSON.stringify(String(selected))})`);
      await call('Input.dispatchMouseEvent',{type:'mouseMoved',x:point.x,y:point.y});
      await verify(`${theme}: ${selected?'selected':'unselected'} sheet hover uses a subtle highlight and readable text`,`(()=>{
        const tab=d.querySelector('.sheet-tab'),style=w.getComputedStyle(tab),probe=d.createElement('span');
        probe.style.backgroundColor='var(--soft)';tab.parentElement.append(probe);const highlight=w.getComputedStyle(probe).backgroundColor;probe.remove();
        const luminance=color=>color.match(/[\\d.]+/g).slice(0,3).map(Number).map(value=>{const v=value/255;return v<=.04045?v/12.92:((v+.055)/1.055)**2.4}).reduce((sum,v,i)=>sum+v*[.2126,.7152,.0722][i],0);
        const foreground=luminance(style.color),background=luminance(style.backgroundColor),contrast=(Math.max(foreground,background)+.05)/(Math.min(foreground,background)+.05);
        return tab.matches(':hover') && style.backgroundColor===highlight && contrast>=4.5;
      })()`);
    }
    await exec(`d.querySelector('.sheet-tab').setAttribute('aria-pressed',${JSON.stringify(original)})`);
    await call('Input.dispatchMouseEvent',{type:'mouseMoved',x:0,y:0});
    await verify(`${theme}: selected sheet retains a surface background and accent underline`,`(()=>{const tab=d.querySelector('.sheet-tab'),probe=d.createElement('span');probe.style.backgroundColor='var(--paper)';tab.parentElement.append(probe);const paper=w.getComputedStyle(probe).backgroundColor;probe.remove();return w.getComputedStyle(tab).backgroundColor===paper && w.getComputedStyle(tab,'::before').height==='2px'})()`);
  };
  const visible=id=>`d.getElementById('${id}') && w.getComputedStyle(d.getElementById('${id}')).display!=='none' && d.getElementById('${id}').getBoundingClientRect().height>0`;
  await call('Runtime.enable');await call('Page.enable');await call('Network.enable');await resize(1600);await call('Page.navigate',{url:origin+'/harness'});
  await wait("d.querySelector('.sheet-grid td') && d.querySelector('#dataset option')");
  assert(!requests.some(r=>r.path.endsWith('/report')||r.path.endsWith('/series')||r.path.endsWith('/charts')));
  checks.push('Import renders saved review without waiting for report, series or native charts');
  if(layoutMode) {
   await click('#workbookResults');
   await click('[data-sheet-filter="charts"]');
   await wait("d.getElementById('sheetList').textContent.includes('没有匹配')");
   await verify('An empty filtered sheet strip retains its complete bottom border',"w.getComputedStyle(d.querySelector('.sheet-tabs-row')).borderBottomWidth==='1px' && parseFloat(w.getComputedStyle(d.querySelector('.sheet-tabs-row')).borderBottomLeftRadius)>0");
   await screenshot('workbook-filter-empty');
   emptyImports=true;await click('#reloadImports');await wait("d.getElementById('importReview').classList.contains('review-empty')");
   await verify('A workbook with no imports shows one empty state without dead controls or extra explanation',"d.getElementById('workbookEmpty').getBoundingClientRect().height>0 && w.getComputedStyle(d.querySelector('.sheet-tabbar')).display==='none' && w.getComputedStyle(d.getElementById('sheetDetail')).display==='none' && !d.body.innerText.includes('在本知识库导入 Excel 后')");
   await screenshot('workbook-no-files');
   await click('#documentResults');await wait("d.querySelectorAll('.pdf-file-table tbody>tr').length===2");
   await verify('A file is listed once per content identity and same-name distinct files stay separate',"d.querySelectorAll('.pdf-file-table tbody>tr').length===2 && d.querySelector('.pdf-file-table tbody>tr').dataset.fileIdentity==='sha:'+'b'.repeat(64) && d.querySelector('.pdf-file-table tbody>tr').textContent.includes('1 张表格')");
   await verify('History and internal IDs stay collapsed',"!d.querySelector('.pdf-file-history').open && !d.querySelector('.record-details').open && !d.getElementById('pdfReviewPanel').innerText.includes('待核对表格') && !d.getElementById('pdfReviewPanel').innerText.includes('pdf-old')");
   await screenshot('pdf-file-inventory');
   await click('.pdf-file-table tbody>tr:first-child .pdf-file-actions button:last-child');await wait("d.querySelector('.pdf-raw-cell')?.textContent==='Synthetic PDF cell'");
   assert(requests.some(r=>r.path==='/structured/api/pdf/drafts/pdf-fixture'));
   await verify('Opening a file hides its inventory and displays one selected table beside the original',"w.getComputedStyle(d.getElementById('pdfInventory')).display==='none' && d.querySelectorAll('.pdf-raw-table').length===1 && d.getElementById('pdfFragmentSelect').options.length===2 && d.querySelector('.pdf-source-viewer').dataset.page==='1'");
   await exec("d.querySelector('.pdf-fragment-choice input').click()");
   await verify('Selection is explicit and counted before field recognition',"d.getElementById('pdfSelectionCount').textContent==='已选择 1 处表格'");
   await input('#pdfFragmentSelect','1');await wait("d.querySelector('.pdf-raw-cell')?.textContent==='Second synthetic table'");
   await verify('Long numeric source text stays on one line',"(()=>{const cell=[...d.querySelectorAll('.pdf-raw-cell')].find(n=>n.textContent==='12345678901234567890');const range=d.createRange();range.selectNodeContents(cell);return range.getClientRects().length===1})()");
   await verify('Table navigation retains selection and follows actual source pages',"d.getElementById('pdfSelectionCount').textContent==='已选择 1 处表格' && !d.querySelector('.pdf-fragment-choice input').checked && d.querySelector('.pdf-source-viewer').dataset.page==='2'");
   await js("document.documentElement.style.setProperty('--td-text-color-secondary','#59534e');document.documentElement.style.setProperty('--app-font-family','Georgia, \"WeKnora Noto Serif SC\", serif')");
   await wait("w.getComputedStyle(d.getElementById('pdfReviewPanel')).getPropertyValue('--muted').trim()==='#59534e'");
   await verify('PDF inherits the host font and muted color',"w.getComputedStyle(d.querySelector('.pdf-raw-cell')).fontFamily===w.getComputedStyle(d.getElementById('directUpload')).fontFamily && w.getComputedStyle(d.querySelector('.pdf-table-meta')).color==='rgb(89, 83, 78)'");
   assert(!requests.some(r=>r.path.endsWith('/suggest')||r.path.endsWith('/publish')));
   await screenshot('pdf-side-by-side');
   await resize(650);await verify('The review stacks cleanly without document horizontal overflow',"d.documentElement.scrollWidth<=d.documentElement.clientWidth+1");await screenshot('pdf-review-narrow');
   await click('#pdfBackToFiles');await verify('Returning to the inventory clears the opened source and preserves file history',"!d.getElementById('pdfDraftDetail').children.length && w.getComputedStyle(d.getElementById('pdfInventory')).display!=='none' && d.querySelectorAll('.pdf-file-table tbody>tr').length===2");
   assert.deepEqual(errors,[]);checks.push('No JavaScript errors, model calls or publication writes');
   if(keepArtifacts)fs.writeFileSync(path.join(output,'layout-acceptance.json'),JSON.stringify({passed:checks.length,checks},null,2));console.log(JSON.stringify({passed:checks.length,checks}));return;
  }
  if(process.argv.includes('--upload-regression')) {
   const choose = (name, size=16) => exec(`const dt=new w.DataTransfer();dt.items.add(new w.File([new Uint8Array(${size})],${JSON.stringify(name)}));d.getElementById('file').files=dt.files;d.getElementById('file').dispatchEvent(new w.Event('change',{bubbles:true}))`);
   const drop = names => exec(`const dt=new w.DataTransfer();for(const name of ${JSON.stringify(names)})dt.items.add(new w.File(['fixture'],name));d.getElementById('directUpload').dispatchEvent(new w.DragEvent('drop',{bubbles:true,cancelable:true,dataTransfer:dt}))`);
   const writes = () => requests.filter(r=>r.method==='POST'&&['/structured/api/import','/structured/api/pdf/import'].includes(r.path));
   await verify('One accessible upload entry supports both actual formats',"w.getComputedStyle(d.getElementById('processingResults')).display!=='none' && d.getElementById('file').accept==='.xlsx,.pdf' && !d.getElementById('pdfUploadFile') && d.getElementById('file').getAttribute('aria-label')==='上传文件'");
   await choose('not-supported.csv');await wait("d.getElementById('jobState').textContent.includes('暂不支持')");assert.equal(writes().length,0);
   await choose('large.pdf',50*1024*1024+1);await wait("d.getElementById('jobState').textContent.includes('超过 50 MB')");assert.equal(writes().length,0);
   await drop(['one.pdf','two.xlsx']);await wait("d.getElementById('jobState').textContent.includes('每次上传一个')");assert.equal(writes().length,0);checks.push('Unsupported, oversized and multiple files never reach an import API');
   await choose('synthetic.XLSX');await wait("d.getElementById('jobState').textContent.includes('正在处理隔离上传')");await drop(['duplicate.pdf']);assert.equal(writes().length,1);await verify('Processing prevents duplicate file selection',"d.getElementById('file').disabled && d.getElementById('upload').disabled");
   await wait("d.getElementById('jobState').textContent.includes('扫描完成') && !d.getElementById('file').disabled");assert.equal(writes()[0].path,'/structured/api/import');checks.push('Selecting Excel starts its existing scan without a second click');
   await drop(['synthetic.PDF']);await wait("d.getElementById('jobState').textContent.includes('PDF 识别完成') && !d.getElementById('file').disabled");
   assert.equal(writes()[1].path,'/structured/api/pdf/import');assert(writes().every(r=>r.scope==='kb-a'&&r.authorized));
   await verify('PDF results open automatically without another upload picker',"d.body.classList.contains('document-results') && d.getElementById('documentResults').getAttribute('aria-pressed')==='true' && d.getElementById('pdfDraftDetail').textContent.includes('Synthetic PDF cell') && !d.getElementById('pdfUploadFile')");
   await verify('Optional instructions and technical IDs are collapsed',"!d.querySelector('#pdfDraftList .pdf-file-history').open && !d.querySelector('#pdfDraftList .record-details').open");
   assert(!requests.some(r=>r.path.endsWith('/publish')||r.path.endsWith('/suggest')));checks.push('Drag-and-drop preserves scope and does not invoke AI mapping or publish data');
   await screenshot('unified-pdf-results');
   await click('#workbookResults');await verify('Users can return to the workbook review',"w.getComputedStyle(d.getElementById('importReview')).display!=='none' && w.getComputedStyle(d.getElementById('pdfReviewPanel')).display==='none'");
   await choose('bad.xlsx');await wait("d.getElementById('jobState').textContent.includes('扫描完成') && !d.getElementById('file').disabled");
   await choose('bad.xlsx');await wait("d.getElementById('jobState').textContent.includes('文件结构无法读取') && !d.getElementById('file').disabled");await verify('Failures expose an explicit retry button',"!d.getElementById('upload').classList.contains('hidden')");
   await click('#upload');await wait("d.getElementById('jobState').textContent.includes('扫描完成') && !d.getElementById('file').disabled");checks.push('Retry reuses the selected file and releases busy state');
   pdfConfigured=false;const count=writes().length;await choose('disabled.pdf');await wait("d.getElementById('jobState').textContent.includes('尚未配置') && !d.getElementById('file').disabled");assert.equal(writes().length,count);checks.push('Unavailable PDF parser stays an explicit error without a false completion');
   pdfConfigured=true;pdfStall=true;await choose('late.pdf');await wait("d.getElementById('file').disabled");await pause(50);await js("window.auth('kb-b')");await wait("d.getElementById('dataset')?.textContent.includes('kb-b') && !d.getElementById('file').disabled");await pause(800);
   await verify('A late PDF response cannot populate the new knowledge base',"!d.getElementById('jobState').textContent.includes('PDF 识别完成') && !d.getElementById('pdfDraftDetail')?.textContent.includes('Synthetic PDF cell') && d.getElementById('selectedFileName').textContent==='支持 Excel、PDF'");
   pdfStall=false;pdfListDenied=true;const listFailureWrites=writes().length;await choose('list-failure.pdf');await wait("!d.getElementById('file').disabled && d.getElementById('jobState').textContent.includes('无法读取处理记录')");assert.equal(writes().length,listFailureWrites);
   pdfListDenied=false;await click('#upload');await wait("d.getElementById('jobState').textContent.includes('PDF 识别完成') && !d.getElementById('file').disabled && d.getElementById('pdfDraftDetail')?.textContent.includes('Synthetic PDF cell')");checks.push('A failed workspace load blocks submission and retry rebuilds the review');
   pdfSourceDenied=true;await choose('denied-source.pdf');await wait("!d.getElementById('file').disabled && d.getElementById('jobState').textContent.includes('无法打开核对结果')");checks.push('Protected source failure is not reported as a successful import');
   pdfSourceDenied=false;await choose('automatic-fixture.pdf');await wait("d.getElementById('jobState').textContent.includes('已自动入库 1 张表格') && !d.getElementById('file').disabled");
   await verify('The unified uploader preserves automatic acceptance and never asks for another confirmation',"!d.getElementById('jobState').textContent.includes('核对并发布') && !d.getElementById('pdfDraftDetail').children.length && w.getComputedStyle(d.getElementById('pdfInventory')).display!=='none'");
   await resize(650);await screenshot('unified-upload-mobile');
   assert.deepEqual(errors,[]);checks.push('No JavaScript runtime errors');
   if(keepArtifacts)fs.writeFileSync(path.join(output,'upload-acceptance.json'),JSON.stringify({passed:checks.length,checks,writes:writes()},null,2));
   console.log(JSON.stringify({passed:checks.length,checks,real_model_calls:0,real_business_writes:0}));return;
  }
  if(process.argv.includes('--loading-regression')) {
   await verify('Saved review is visible on the first visit',"Boolean(d.querySelector('.sheet-grid td'))");
   await js("window.savedGrid=document.getElementById('frame').contentDocument.getElementById('sheetDetail');window.view('query')");
   await wait("d.querySelector('#seriesRows button') && d.querySelector('#scope [data-download]')");
   checks.push('Query resources load on demand without recreating the review');
   await js("window.view('import')");
   await verify('Returning from query preserves the existing grid',"window.savedGrid===d.getElementById('sheetDetail') && w.getComputedStyle(d.getElementById('importReview')).display!=='none'");
   await js("window.auth('kb-a','fixture-token','source-recovered')");
   await wait("d.getElementById('importSelect').value==='666666666666666666666666' && !d.getElementById('upload').disabled");
   await verify('Restored reviews render automatically without leaving the page',"d.querySelector('.sheet-tab').textContent.includes('合成工作表') && d.getElementById('importSummary').textContent.includes('1 张已可查询') && d.getElementById('jobState').textContent.includes('已恢复')");
   const before=requests.filter(r=>r.path==='/structured/api/datasets').length;
   nativeStall='initial-refresh-renew';
   await call('Page.navigate',{url:origin+'/harness'});
   for(let attempt=0;attempt<200&&requests.filter(r=>r.path==='/structured/api/datasets').length===before;attempt++)await pause(10);
   await js("window.auth('kb-a','fixture-renewed')");
   await wait("d.querySelector('.sheet-grid td') && d.getElementById('importSelect').value");
   checks.push('Token renewal during the initial request retries initialization without a page switch');
   nativeStall='';
   assert.deepEqual(errors,[]);checks.push('No JavaScript errors');
   console.log(JSON.stringify({passed:checks.length,checks}));return;
  }
  await verify('Import mode exposes upload and original review',`${visible('directUpload')} && ${visible('importReview')}`);
  const selectorBounds=await exec("const element=d.getElementById('importSelect'),rect=element.getBoundingClientRect(),style=w.getComputedStyle(element);return {width:rect.width,height:rect.height,fontSize:style.fontSize,padding:style.padding,toolbar:element.parentElement.parentElement.className}");if(keepArtifacts)fs.writeFileSync(path.join(output,'current-file-bounds.json'),JSON.stringify(selectorBounds,null,2));
  await verify('Current file selector stays compact without stretching across the review',"d.getElementById('importSelect').getBoundingClientRect().width<=360 && d.getElementById('importSelect').getBoundingClientRect().height<=38 && d.getElementById('importSelect').getBoundingClientRect().width>=160");
  await screenshot('01_导入_1600');
  if(process.argv.includes('--baseline')){await js("window.view('query')");await pause(150);await screenshot('00_改动前查询_1600');console.log(JSON.stringify({baseline:true,checks}));return;}
  await verify('Chinese file picker initially shows an explicit empty selection',"d.querySelector('.file-trigger').textContent==='上传文件' && d.getElementById('selectedFileName').textContent==='支持 Excel、PDF' && d.getElementById('file').getAttribute('aria-label')==='上传文件'");
  await call('Page.setInterceptFileChooserDialog',{enabled:true});await exec("d.getElementById('file').focus()");
  await call('Input.dispatchKeyEvent',{type:'keyDown',key:'Tab',code:'Tab',windowsVirtualKeyCode:9});await call('Input.dispatchKeyEvent',{type:'keyUp',key:'Tab',code:'Tab',windowsVirtualKeyCode:9});
  await call('Input.dispatchKeyEvent',{type:'keyDown',key:'Tab',code:'Tab',windowsVirtualKeyCode:9,modifiers:8});await call('Input.dispatchKeyEvent',{type:'keyUp',key:'Tab',code:'Tab',windowsVirtualKeyCode:9,modifiers:8});
  await verify('Real file input remains keyboard-focusable with visible custom control focus',"d.activeElement.id==='file' && d.activeElement.matches(':focus-visible') && w.getComputedStyle(d.querySelector('.file-trigger')).outlineWidth!=='0px'");
  await call('Input.dispatchKeyEvent',{type:'keyDown',key:' ',code:'Space',windowsVirtualKeyCode:32,nativeVirtualKeyCode:32});await call('Input.dispatchKeyEvent',{type:'keyUp',key:' ',code:'Space',windowsVirtualKeyCode:32,nativeVirtualKeyCode:32});
  for(let count=0;!fileChoosers.length&&count<100;count++)await pause(50);assert.equal(fileChoosers.length,1,'Space on the real file input must open the native picker');checks.push('Keyboard Space opens the actual file chooser');
  const fixtureDir=path.join(output,'fixture-files');fs.mkdirSync(fixtureDir,{recursive:true});const fixtureFile=path.join(fixtureDir,'隔离上传.xlsx');fs.writeFileSync(fixtureFile,Buffer.from([80,75,3,4]));
  await call('DOM.setFileInputFiles',{backendNodeId:fileChoosers[0].backendNodeId,files:[fixtureFile]});await wait("d.getElementById('selectedFileName').textContent==='隔离上传.xlsx'");
  await verify('Native file selection updates the real filename and accessible status',"d.getElementById('selectedFileName').title==='隔离上传.xlsx' && d.getElementById('selectedFileName').getAttribute('role')==='status'");
  await wait("d.getElementById('jobState').textContent.includes('扫描完成') && !d.getElementById('upload').disabled");
  await js("window.auth('kb-a','fixture-renewed')");await pause(50);await verify('Token refresh keeps the selected filename in the same KB',"d.getElementById('selectedFileName').textContent==='隔离上传.xlsx'");await js("window.auth('kb-a')");
  await exec("window.reviewNode=d.getElementById('sheetDetail');const unit=d.querySelector('[data-column-field=unit]');unit.value='KB A 草稿';unit.dispatchEvent(new w.Event('input',{bubbles:true}))");
  await verifySheetHighlight('Light');
  const themeRequests=requests.length;
  await exec("window.themeSnapshot={origin:w.performance.timeOrigin,import:d.getElementById('importSelect').value,node:d.getElementById('sheetDetail'),cells:[...d.querySelectorAll('.sheet-grid td')].map(el=>[el.textContent,el.getAttribute('style')])}");
  await js("document.getElementById('frame').contentWindow.postMessage({type:'workspace-theme',mode:'dark'},location.origin)");
  await wait("d.documentElement.getAttribute('theme-mode')==='dark'");
  await pause(180);
  await verify('Dark query and import controls use the shared theme and readable filled-button text',`(()=>{const root=w.getComputedStyle(d.documentElement),button=w.getComputedStyle(d.getElementById('upload')),probe=d.createElement('span');probe.style.backgroundColor='var(--primary)';probe.style.color='var(--on-primary)';d.body.append(probe);const expected=w.getComputedStyle(probe),same=button.backgroundColor===expected.backgroundColor&&button.color===expected.color;probe.remove();const lum=color=>color.match(/[\\d.]+/g).slice(0,3).map(Number).map(value=>{const v=value/255;return v<=.04045?v/12.92:((v+.055)/1.055)**2.4}).reduce((sum,v,i)=>sum+v*[.2126,.7152,.0722][i],0),fg=lum(button.color),bg=lum(button.backgroundColor);return same&&w.getComputedStyle(d.getElementById('queryWorkspace')).getPropertyValue('--primary').trim()===root.getPropertyValue('--primary').trim()&&(Math.max(fg,bg)+.05)/(Math.min(fg,bg)+.05)>=4.5})()`);
  await verifySheetHighlight('Dark');
  await verify('Changing theme keeps the original source cells, review node, selected import and unsaved draft',"window.themeSnapshot.origin===w.performance.timeOrigin && window.themeSnapshot.import===d.getElementById('importSelect').value && window.themeSnapshot.node===d.getElementById('sheetDetail') && JSON.stringify(window.themeSnapshot.cells)===JSON.stringify([...d.querySelectorAll('.sheet-grid td')].map(el=>[el.textContent,el.getAttribute('style')])) && d.querySelector('[data-column-field=unit]').value==='KB A 草稿'");
  assert.equal(requests.length,themeRequests);checks.push('Theme messages do not reload data, query sources or start another import');
  await js("document.getElementById('frame').contentWindow.postMessage({type:'workspace-theme',mode:'light'},location.origin)");
  await wait("d.documentElement.getAttribute('theme-mode')==='light'");
  await pause(180);
  await verify('Light query and import controls return to the same primary and surface tokens',`(()=>{const probe=d.createElement('span');probe.style.backgroundColor='var(--primary)';probe.style.color='var(--paper)';d.body.append(probe);const expected=w.getComputedStyle(probe),same=w.getComputedStyle(d.getElementById('upload')).backgroundColor===expected.backgroundColor&&w.getComputedStyle(d.getElementById('queryWorkspace')).backgroundColor===expected.color;probe.remove();return same&&w.getComputedStyle(d.getElementById('queryWorkspace')).getPropertyValue('--primary').trim()===w.getComputedStyle(d.documentElement).getPropertyValue('--primary').trim()})()`);
  await call('Emulation.setEmulatedMedia',{features:[{name:'prefers-reduced-motion',value:'reduce'}]});
  await verify('Reduced motion removes import and query control transitions',"w.getComputedStyle(d.getElementById('upload')).transitionDuration==='0s' && w.getComputedStyle(d.getElementById('query')).transitionDuration==='0s'");
  await call('Emulation.setEmulatedMedia',{features:[{name:'prefers-reduced-motion',value:'no-preference'}]});
  await js("window.view('query')");await wait("d.querySelector('#seriesRows button') && d.querySelector('#scope [data-download]')");
  await input('#dataset','__all__');await wait("d.querySelectorAll('#seriesRows button').length===6");
  await verify('Query can include every current business file',"d.getElementById('dataset').value==='__all__' && d.querySelectorAll('#seriesRows button').length===6");
  await input('#dataset',ids.primary);await wait("d.querySelectorAll('#seriesRows button').length===3 && d.querySelector('#scope [data-download]')?.dataset.download==='111111111111111111111111'");
  await verify('Query mode keeps original review DOM and edit state hidden',"window.reviewNode===d.getElementById('sheetDetail') && w.getComputedStyle(d.getElementById('importReview')).display==='none' && d.querySelector('[data-column-field=unit]').value==='KB A 草稿'");
  await verify('Query awaits a chosen metric',"d.getElementById('query').disabled && d.getElementById('selectedSeries').value===''");
  await verify('Unified upload presents one drop target',"(()=>{const style=w.getComputedStyle(d.getElementById('directUpload'));return style.borderTopStyle==='dashed' && d.getElementById('file').accept==='.xlsx,.pdf' && !d.getElementById('pdfUploadFile')})()");
  await verify('File selector, metric picker and query result share one visible working surface',`${visible('queryWorkspace')} && d.getElementById('queryWorkspace').contains(d.getElementById('dataset')) && d.getElementById('queryWorkspace').contains(d.getElementById('seriesRows')) && d.getElementById('queryWorkspace').contains(d.getElementById('query'))`);
  await screenshot('02_查询_未选择_1600');
  await js("window.view('source')");await pause(100);
  await click('#datasetQuality>summary');await pause(100);
  await verify('Removed source view cannot restore a separate page, and query keeps original downloads',`!d.body.classList.contains('view-source') && ${visible('queryWorkspace')} && !(${visible('importReview')}) && d.querySelector('#scope [data-download]').getBoundingClientRect().height>0 && d.querySelector('#scope [data-download]').dataset.download===${JSON.stringify(ids.primary)}`);
  await screenshot('02b_查询_保留原件证据_1600');await click('#datasetQuality>summary');await js("window.view('query')");await pause(100);
  await input('#search','合成指标一');await exec("d.getElementById('search').focus()");await call('Input.dispatchKeyEvent',{type:'keyDown',key:'Enter',code:'Enter',windowsVirtualKeyCode:13,text:'\r',unmodifiedText:'\r'});await call('Input.dispatchKeyEvent',{type:'keyUp',key:'Enter',code:'Enter',windowsVirtualKeyCode:13});await wait("d.querySelectorAll('#seriesRows button').length===1");
  assert(requests.some(r=>r.path.endsWith('/series')&&r.query.search==='合成指标一'&&r.scope==='kb-a'));checks.push('Metric search sends exact selected file and KB scope');checks.push('Keyboard Enter submits the metric search form');
  await click('#seriesRows [data-index="0"]');await wait("!d.getElementById('query').disabled && d.getElementById('selectedSeries').value.endsWith(':date')");
  await verify('Selected human-readable metric is visible with selected state',"d.getElementById('selectedMetricTitle').textContent.includes('合成指标一') && d.querySelector('#seriesRows [aria-pressed=true]')!==null");
  await input('#start',dates[0]);await input('#end',dates[2]);await input('#operation','sum');await click('#seriesRows [data-index="0"]');await verify('Reselecting the same metric retains the chosen range and calculation',"d.getElementById('start').value==='2025-01-01' && d.getElementById('end').value==='2025-01-03' && d.getElementById('operation').value==='sum'");await click('#query');
  await wait("d.getElementById('answer').textContent.includes('45') && d.getElementById('answer').textContent.includes('B4')");
  await verify('Date-bounded query displays exact sum and all source cells',"d.getElementById('answer').textContent.includes('计算结果：45') && ['B2','B3','B4','A2','A3','A4'].every(s=>d.getElementById('answer').textContent.includes(s)) && d.getElementById('answer').textContent.includes('kb-a_合成样本甲.xlsx') && d.getElementById('answer').textContent.includes('测试单位')");
  assert(requests.some(r=>r.path==='/structured/api/query'&&r.query.start_date===dates[0]&&r.query.end_date===dates[2]&&r.query.operation==='sum'&&r.scope==='kb-a'));checks.push('Query parameters retain selected range, operation and genuine scoped ID');
  await click('#query');await verify('Repeated query clears previous evidence and shows pending status',"d.getElementById('answer').textContent==='' && d.getElementById('query').disabled && d.getElementById('queryStatus').textContent.includes('正在查询')");await wait("d.getElementById('answer').textContent.includes('计算结果：45') && !d.getElementById('query').disabled");
  await screenshot('03_查询_结果与来源_1600');
  for(const width of [900,600]){
   await resize(width);await exec("w.scrollTo(0,0)");
   await verify(`Query layout at ${width} px has no document horizontal overflow`,"d.documentElement.scrollWidth<=d.documentElement.clientWidth+1");
   await screenshot(`04_查询_${width}`);
  }
  await resize(600);await js("document.getElementById('frame').style.cssText='width:340px;height:100%;margin-left:260px;border:0'");await pause(150);await exec("w.scrollTo(0,0)");
  await verify('Narrow embedded query keeps all large statistics fully readable without overlap',`(()=>{
   if(w.innerWidth!==340)return false;
   const numbers=[...d.querySelectorAll('#stats b')],expected=['12417','608,180','1,048,771','72,945'];
   if(numbers.length!==4||numbers.some((number,index)=>number.textContent!==expected[index]))return false;
   const ranges=numbers.map(number=>{const range=d.createRange();range.selectNodeContents(number);return range.getBoundingClientRect()});
   if(ranges.some((rect,index)=>{const cell=numbers[index].parentElement.getBoundingClientRect();return rect.width<=0||rect.left<cell.left-1||rect.right>cell.right+1||rect.bottom>cell.bottom+1}))return false;
   return ranges.every((rect,index)=>ranges.slice(index+1).every(other=>rect.right<=other.left||other.right<=rect.left||rect.bottom<=other.top||other.bottom<=rect.top));
  })()`);
  await verify('Narrow embedded query does not overflow its 340 px frame',"d.documentElement.scrollWidth<=d.documentElement.clientWidth+1");await screenshot('04b_查询_600外框_340内容');
  await js("document.getElementById('frame').style.cssText=''");await resize(1600);
  await input('#start','2026-01-01');await input('#end','2026-01-02');await input('#operation','rows');await click('#query');await wait("d.getElementById('answer').textContent.includes('没有匹配')");checks.push('A valid date range with no rows is explicit');
  await input('#start','2026-02-01');await input('#end','2026-01-01');await click('#query');await wait("d.getElementById('notice').textContent.includes('开始日期不能晚于')");await verify('Failed query does not leave a previous result or source file displayed',"!d.getElementById('answer').textContent.includes('查询完成') && !d.getElementById('answer').textContent.includes('kb-a_合成样本甲.xlsx')");checks.push('Query errors remain visible without JavaScript failure');
  await input('#search','合成期间');await click('#find');await wait("d.querySelectorAll('#seriesRows button').length===1 && d.getElementById('seriesRows').textContent.includes('期间')");await click('#seriesRows [data-index="0"]');
  await verify('Period metric exposes exact period filter and disables date/latest controls',"w.getComputedStyle(d.getElementById('periodLabel')).display!=='none' && w.getComputedStyle(d.getElementById('startLabel')).display==='none' && d.querySelector('#operation option[value=latest]').disabled");
  await input('#period','样本期甲');await click('#query');await wait("d.getElementById('answer').textContent.includes('样本期甲')");assert(requests.some(r=>r.path==='/structured/api/query'&&r.query.period==='样本期甲'&&!r.query.start_date&&!r.query.end_date));checks.push('Period query sends original label without invented dates');
  await input('#search','合成无日期');await click('#find');await wait("d.getElementById('seriesRows').textContent.includes('无日期')");await click('#seriesRows [data-index="0"]');await verify('Undated metric has no forced time filter',"w.getComputedStyle(d.getElementById('startLabel')).display==='none' && w.getComputedStyle(d.getElementById('periodLabel')).display==='none'");
  await click('#query');await input('#search','');await input('#dataset',ids.secondary);await verify('Pending file switch immediately clears old file statistics and source link',"d.getElementById('stats').textContent==='' && !d.querySelector('#scope [data-download]') && d.getElementById('report').textContent===''");await wait("d.getElementById('seriesRows').textContent.includes('合成指标二') && d.getElementById('selectedSeries').value===''");await pause(650);await verify('Late result from a previous file cannot populate the new file query',"d.getElementById('answer').textContent==='' && d.getElementById('selectedSeries').value===''");await verify('Switching files clears metric and previous query result',"d.getElementById('query').disabled && !d.getElementById('answer').textContent.includes('B2')");await click('#seriesRows [data-index="0"]');await click('#query');await wait("d.getElementById('answer').textContent.includes('kb-a_合成样本乙.xlsx')");checks.push('Newly chosen file supplies the resulting evidence');
  await input('#search','不存在的夹具指标');await click('#find');await wait("d.querySelectorAll('#seriesRows button').length===0");await verify('No matching metric is explained',"d.getElementById('seriesCount').textContent.includes('0')");
  await js("window.view('import')");await pause(100);await verify('View switching preserves original review edits',"window.reviewNode===d.getElementById('sheetDetail') && d.querySelector('[data-column-field=unit]').value==='KB A 草稿'");
  await exec("const dt=new w.DataTransfer();dt.items.add(new w.File([new Uint8Array([80,75,3,4])],'隔离上传.xlsx',{type:'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet'}));d.getElementById('file').files=dt.files;d.getElementById('file').dispatchEvent(new w.Event('change',{bubbles:true}));d.getElementById('upload').click()");
  await wait("d.getElementById('jobState').textContent.includes('正在处理隔离上传')");await verify('Upload pending state prevents duplicate submission',"d.getElementById('upload').disabled");await wait("d.getElementById('jobState').textContent.includes('扫描完成') && !d.getElementById('upload').disabled");checks.push('Successful upload refreshes existing sheet review and available datasets');
  await verify('Upload uses multipart file in the selected knowledge base',"d.querySelector('.sheet-grid td')!==null");assert(requests.some(r=>r.path==='/structured/api/import'&&r.method==='POST'&&r.scope==='kb-a'&&r.size>100));
  await screenshot('05_导入_完成_1600');
  await exec("const dt=new w.DataTransfer();dt.items.add(new w.File(['fixture'],'隔离上传.xlsx'));d.getElementById('file').files=dt.files;d.getElementById('file').dispatchEvent(new w.Event('change',{bubbles:true}))");await wait("d.getElementById('jobState').textContent.includes('文件结构无法读取') && !d.getElementById('upload').disabled");checks.push('Failed upload explains error and releases the action button');await screenshot('06_导入_失败_1600');
  for(const width of [900,600]){await resize(width);await exec("w.scrollTo(0,0)");await verify(`Import layout at ${width} px has no document horizontal overflow`,"d.documentElement.scrollWidth<=d.documentElement.clientWidth+1");await screenshot(`07_导入_${width}`);}
  await resize(1600);await exec("const unit=d.querySelector('[data-column-field=unit]');unit.value='KB A 草稿';unit.dispatchEvent(new w.Event('input',{bubbles:true}))");
  await js("window.auth('kb-b')");await wait("d.querySelector('.sheet-grid td') && d.querySelector('[data-column-field=unit]').value==='' && d.getElementById('dataset').textContent.includes('kb-b')");checks.push('Changing KB clears first KB draft and loads other KB real IDs');
  await verify('Changing KB clears the selected native file and displayed filename',"d.getElementById('file').files.length===0 && d.getElementById('selectedFileName').textContent==='支持 Excel、PDF'");
  await exec("const unit=d.querySelector('[data-column-field=unit]');unit.value='KB B 草稿';unit.dispatchEvent(new w.Event('input',{bubbles:true}))");await js("window.auth('kb-a')");await wait("d.querySelector('[data-column-field=unit]')?.value==='KB A 草稿' && d.getElementById('dataset')?.textContent.includes('kb-a')");checks.push('Returning to first KB restores only its own persistent draft');
  assert(requests.filter(r=>r.path.startsWith('/structured/api/')&&!r.path.endsWith('/ui-config')).every(r=>['kb-a','kb-b'].includes(r.scope)&&r.tenant==='10000'&&r.authorized));checks.push('All business requests retain tenant, bearer and explicit KB scope');
  await js("window.view('query')");await pause(100);await exec("d.getElementById('search').focus()");await call('Input.dispatchKeyEvent',{type:'keyDown',key:'Tab',code:'Tab',windowsVirtualKeyCode:9});await call('Input.dispatchKeyEvent',{type:'keyUp',key:'Tab',code:'Tab',windowsVirtualKeyCode:9});
  await verify('Keyboard Tab reaches the adjacent actionable control',"d.activeElement.tagName==='BUTTON' && d.activeElement.id==='find' && d.activeElement.matches(':focus-visible')");checks.push('Visible keyboard focus survives the new layout');
  await call('Page.navigate',{url:origin+'/harness?review=1'});await wait("d.querySelector('.sheet-grid td') && d.getElementById('importSelect').disabled");
  await verify('Pinned review-only view retains original grid and hides upload/query',`${visible('importReview')} && !(${visible('directUpload')}) && !(${visible('queryWorkspace')})`);await screenshot('08_独立原表核对_1600');
  const reviewBridgeRequests=requests.filter(r=>r.path.endsWith('/import-knowledge')).length;await js("window.auth('kb-a','fixture-token','source-reused')");await pause(150);assert.equal(requests.filter(r=>r.path.endsWith('/import-knowledge')).length,reviewBridgeRequests);checks.push('Review-only frames ignore native source auth and keep their pinned original');
  await call('Page.navigate',{url:origin+'/harness?legacy=1'});await wait("d.querySelector('.sheet-grid td') && d.getElementById('dataset')?.textContent.includes('legacy')");
  await verify('Legacy iframe entry retains existing review and query controls',`${visible('importReview')} && ${visible('queryWorkspace')} && !d.body.classList.contains('knowledge-view')`);checks.push('Legacy requests remain unbound to a knowledge base');
  await call('Page.navigate',{url:origin+'/structured/'});
  for(let count=0;count<200;count++){if(await js("document.querySelector('.sheet-grid td') && document.querySelector('#seriesRows button')"))break;await pause(50);}
  assert.equal(await js("getComputedStyle(document.getElementById('directUpload')).display!=='none' && document.querySelector('.file-trigger').getBoundingClientRect().width>0 && document.getElementById('queryWorkspace').getBoundingClientRect().height>0"),true);checks.push('Standalone legacy page retains visible import action and complete query workspace');await screenshot('09_原空间独立页_1600');
  await call('Page.navigate',{url:origin+'/harness'});await wait("d.querySelector('.sheet-grid td')");
  await js("window.auth('kb-a','fixture-token','source-reused')");
  await wait("d.getElementById('importSelect').value==='444444444444444444444444' && !d.getElementById('upload').disabled");
  await verify('Knowledge-base Excel reuses its bound scan without a second local file selection',"d.getElementById('file').files.length===0 && d.getElementById('importSelect').selectedOptions[0].textContent.includes('知识库内原件.xlsx') && d.querySelector('.sheet-grid td')!==null");
  await verify('Native source page explains reuse and hides the unnecessary second file picker',"d.body.classList.contains('native-source') && d.getElementById('directUpload').querySelector('h2').textContent==='处理知识库原件' && d.getElementById('directUpload').textContent.includes('无需重新选择文件') && w.getComputedStyle(d.querySelector('.import-actions')).display==='none' && d.getElementById('jobState').textContent.includes('知识库内原件.xlsx')");
  assert.equal(requests.filter(r=>r.path.endsWith('/import-knowledge')&&r.sourceKnowledgeId==='source-reused').length,1);await js("window.auth('kb-a','fixture-token','source-reused')");await pause(100);assert.equal(requests.filter(r=>r.path.endsWith('/import-knowledge')&&r.sourceKnowledgeId==='source-reused').length,1);checks.push('Repeated iframe auth does not enqueue duplicate source scans');
  assert(requests.some(r=>r.path.endsWith('/import-knowledge')&&r.scope==='kb-a'&&r.authorized&&r.sourceKnowledgeId==='source-reused'));checks.push('Native Excel bridge sends real source identity and current authenticated KB scope');assert(requests.filter(r=>r.path.endsWith('/import-knowledge')).every(r=>r.restoreReviewedState===false),'KB originals must not restore legacy workspace mappings');checks.push('KB native bridge explicitly uses fresh source review without legacy restore');
  const excelImportCount=requests.filter(r=>r.path==='/structured/api/import-knowledge').length,pdfImportCount=requests.filter(r=>r.path==='/structured/api/pdf/import-knowledge').length;
  await js("window.auth('kb-a','fixture-token','source-pdf-fixture','fixture-user','pdf')");
  await wait("d.getElementById('pdfDraftDetail')?.textContent.includes('Synthetic PDF cell')");
  await verify('An iframe PDF source message opens the PDF review panel and hides Excel import',"d.body.classList.contains('pdf-native-source') && w.getComputedStyle(d.getElementById('pdfReviewPanel')).display!=='none' && w.getComputedStyle(d.getElementById('directUpload')).display==='none' && d.getElementById('pdfDraftDetail').textContent.includes('fixture.pdf')");
  assert.equal(requests.filter(r=>r.path==='/structured/api/pdf/import-knowledge'&&r.sourceKnowledgeId==='source-pdf-fixture'&&r.scope==='kb-a'&&r.authorized).length,pdfImportCount+1,'PDF source must use the authenticated PDF import endpoint');
  assert.equal(requests.filter(r=>r.path==='/structured/api/import-knowledge').length,excelImportCount,'PDF source must not call the Excel native import API');
  await js("window.auth('kb-a','fixture-token','source-reused','fixture-user','xlsx')");
  await wait("d.getElementById('importSelect').value==='444444444444444444444444' && !d.getElementById('upload').disabled && !d.body.classList.contains('pdf-native-source')");
  assert.equal(requests.filter(r=>r.path==='/structured/api/import-knowledge'&&r.sourceKnowledgeId==='source-reused').length,excelImportCount+1);
  checks.push('Switching the same iframe back to XLSX restores its native Excel import route');
  await js("window.auth('kb-a','fixture-token','source-new')");await wait("d.getElementById('importSelect').value==='555555555555555555555555' && !d.getElementById('upload').disabled");
  assert(requests.some(r=>r.path.endsWith('/api/jobs/native-job')));checks.push('First native source scan follows existing asynchronous job progress and opens its sheet review');await screenshot('10_知识库原件_直接解析');
  await js("window.auth('kb-a','fixture-token','source-stale')");await pause(100);await js("window.auth('kb-a','fixture-renewed','source-reused')");await wait("d.getElementById('importSelect').value==='444444444444444444444444' && !d.getElementById('upload').disabled");await pause(800);
  await verify('Renewed credentials cannot let the previous source response overwrite the current authorized scan',"d.getElementById('importSelect').value==='444444444444444444444444' && d.getElementById('retrySourceImport').classList.contains('hidden')");
  await js("window.auth('kb-a','fixture-token','source-stale')");await pause(100);await js("window.auth('kb-a','fixture-token','source-reused')");await wait("d.getElementById('importSelect').value==='444444444444444444444444' && !d.getElementById('upload').disabled");await pause(800);
  await verify('Late native source response cannot replace a more recently selected document',"d.getElementById('importSelect').value==='444444444444444444444444' && d.getElementById('importSelect').selectedOptions[0].textContent.includes('知识库内原件.xlsx')");
  await js("window.auth('kb-a','fixture-token','source-stale')");await pause(100);await js("window.auth('kb-b')");await wait("d.getElementById('dataset')?.textContent.includes('kb-b') && !d.getElementById('upload').disabled");await pause(800);
  await verify('Changing KB cancels late native UI updates and hides the prior source failure action',"d.getElementById('dataset').textContent.includes('kb-b') && !d.getElementById('importSelect').textContent.includes('知识库内原件.xlsx') && d.getElementById('retrySourceImport').classList.contains('hidden')");
  await js("window.auth('kb-b','fixture-token','source-denied')");await wait("d.getElementById('jobState').textContent.includes('无读取权限') && !d.getElementById('upload').disabled");await verify('Native source permission errors stay visible with an explicit retry action',`${visible('retrySourceImport')} && !d.getElementById('jobState').textContent.includes('就绪')`);
  await verify('Failed source preparation replaces the loading placeholder with its actual error',"d.querySelector('#sheetDetail [role=alert]').textContent.includes('无读取权限') && !d.getElementById('sheetDetail').textContent.includes('正在准备')");
  await js("window.auth('kb-b','fixture-token','source-oversize')");await wait("d.getElementById('jobState').textContent.includes('超过允许') && !d.getElementById('upload').disabled");
  for(const theme of ['light','dark']){
   await exec(`d.documentElement.setAttribute('theme-mode',${JSON.stringify(theme)})`);
   await verify(`${theme}: retry original button keeps readable text after an oversized-file failure`,`(()=>{
    const button=d.getElementById('retrySourceImport'),style=w.getComputedStyle(button);
    const luminance=color=>color.match(/[\\d.]+/g).slice(0,3).map(Number).map(value=>{const v=value/255;return v<=.04045?v/12.92:((v+.055)/1.055)**2.4}).reduce((sum,v,i)=>sum+v*[.2126,.7152,.0722][i],0);
    const foreground=luminance(style.color),background=luminance(style.backgroundColor);
    return button.getBoundingClientRect().height>0 && (Math.max(foreground,background)+.05)/(Math.min(foreground,background)+.05)>=4.5 && d.querySelector('#sheetDetail [role=alert]').textContent.includes('超过允许');
   })()`);
  }
  await exec("d.documentElement.setAttribute('theme-mode','light');w.originalTimeout=w.setTimeout;w.setTimeout=(fn,ms,...args)=>w.originalTimeout(fn,ms===120000?50:ms,...args)");
  await pause(220);await screenshot('12_原件读取失败_重试按钮');
  await js("window.auth('kb-b','fixture-token','source-timeout')");await wait("d.getElementById('jobState').textContent.includes('超时') && !d.getElementById('upload').disabled");
  await verify('A stalled native download times out into an actionable error instead of an endless spinner',`${visible('retrySourceImport')} && d.querySelector('#sheetDetail [role=alert]').textContent.includes('超时')`);
  await exec("w.setTimeout=(fn,ms,...args)=>w.originalTimeout(fn,ms===120000?50:ms===30000?350:ms,...args)");
  for(const sourceId of ['source-poll-timeout','source-refresh-timeout','source-sheet-timeout']){
   await js(`window.auth('kb-b','fixture-token',${JSON.stringify(sourceId)})`);await wait("d.getElementById('jobState').textContent.includes('超时') && !d.getElementById('upload').disabled");
   await verify(`${sourceId}: every native preparation request has its own recoverable timeout`,`${visible('retrySourceImport')} && d.querySelector('#sheetDetail [role=alert]').textContent.includes('超时') && !d.getElementById('sheetDetail').textContent.includes('正在读取')`);
  }
  await js("window.auth('kb-b','fixture-token','source-new')");await wait("d.getElementById('importSelect').value==='555555555555555555555555' && !d.getElementById('upload').disabled");checks.push('Long-running scan polling can outlast the initial download request budget');
  await exec("w.setTimeout=w.originalTimeout;delete w.originalTimeout");
  await call('Network.emulateNetworkConditions',{offline:true,latency:0,downloadThroughput:-1,uploadThroughput:-1});
  await js("window.auth('kb-b','fixture-token','source-offline')");await wait("!d.getElementById('retrySourceImport').classList.contains('hidden') && !d.getElementById('upload').disabled");
  await verify('A disconnected network releases source loading and presents a retry action',"d.querySelector('#sheetDetail [role=alert]')!==null && !d.getElementById('sheetDetail').textContent.includes('正在准备')");
  await call('Network.emulateNetworkConditions',{offline:false,latency:0,downloadThroughput:-1,uploadThroughput:-1});await click('#retrySourceImport');await wait("d.getElementById('importSelect').value==='444444444444444444444444' && !d.getElementById('upload').disabled");checks.push('Retry succeeds for the same native source after the network reconnects');
  await js("window.auth('kb-b','fixture-token','source-retry')");await wait("d.getElementById('jobState').textContent.includes('无读取权限') && !d.getElementById('upload').disabled");await click('#retrySourceImport');await wait("d.getElementById('importSelect').value==='444444444444444444444444' && !d.getElementById('upload').disabled");checks.push('Retry loads the same authorized source after its temporary failure resolves');
  await js("window.auth('kb-b','fixture-token','source-mismatch')");await wait("d.getElementById('jobState').textContent.includes('原件标识与当前选择不一致') && !d.getElementById('upload').disabled");await verify('Mismatched source identity is rejected without opening another scan',"d.getElementById('importSelect').value==='' && d.getElementById('retrySourceImport').classList.contains('hidden')===false");
  await js("window.auth('kb-b','fixture-token','source-expired')");await wait("d.getElementById('notice').textContent.includes('登录已过期') && !d.getElementById('upload').disabled");await verify('Expired credentials show their real error while keeping the protected workspace hidden',"d.getElementById('workspace').classList.contains('hidden') && d.getElementById('importSelect').value===''");
  await js("window.auth('kb-b','fixture-renewed','source-expired')");await wait("d.getElementById('importSelect').value==='444444444444444444444444' && !d.getElementById('upload').disabled");await verify('Credential renewal retries the same source and clears the expired login error',"d.getElementById('notice').textContent==='' && !d.getElementById('workspace').classList.contains('hidden') && d.getElementById('retrySourceImport').classList.contains('hidden')");
  const settingsStart=requests.length,settingsBridgeRequests=requests.filter(r=>r.path.endsWith('/import-knowledge')).length;await call('Page.navigate',{url:origin+'/harness?settings=1&source=source-reused'});await wait("d.getElementById('nativeModel').value==='fixture-chat' && d.getElementById('modelSettings').open && d.getElementById('importSelect').value==='444444444444444444444444'");await pause(150);
  await verify('Old settings URL opens a compact model overlay above the existing import workspace',`d.getElementById('modelSettings').open && (${visible('workspace')}) && !d.getElementById('modelBase') && d.body.classList.contains('native-source')`);assert.equal(requests.filter(r=>r.path.endsWith('/import-knowledge')).length,settingsBridgeRequests+1);assert(requests.slice(settingsStart).some(r=>r.path.endsWith('/api/model-settings')&&r.method==='GET'&&r.scope==='kb-a'));checks.push('Native settings overlay keeps its current source context');await screenshot('11_原件_字段模型浮层');
  await click('#modelClose');await verify('Closing model settings preserves the current source scan without navigation',"!d.getElementById('modelSettings').open && d.body.classList.contains('native-source') && d.getElementById('importSelect').selectedOptions[0].textContent.includes('知识库内原件.xlsx') && d.getElementById('jobState').textContent.includes('已有的工作表识别记录')");assert.equal(requests.filter(r=>r.path.endsWith('/import-knowledge')).length,settingsBridgeRequests+1);
  assert(!requests.some(r=>r.path.includes('/suggest')||r.path.includes('/resolve_fields')||r.path.includes('/model-settings')&&r.method!=='GET'));checks.push('No model execution or configuration writes were called');assert.deepEqual(errors,[]);checks.push('No JavaScript runtime errors');assert.deepEqual(networkFailures.filter(error=>!['net::ERR_ABORTED','net::ERR_INTERNET_DISCONNECTED'].includes(error)),[]);checks.push('Only intentionally cancelled requests and the simulated offline request failed');
  if(keepArtifacts)fs.writeFileSync(path.join(output,'browser-acceptance.json'),JSON.stringify({passed:checks.length,checks,screenshots:keepArtifacts?fs.readdirSync(path.join(output,'screenshots')):[],real_model_calls:0,real_business_writes:0,fixture_upload_requests:uploadCount,requests},null,2));
  console.log(JSON.stringify({passed:checks.length,checks,real_model_calls:0,real_business_writes:0}));
 }finally{
  socket?.close();
  try{await stopBrowser(chrome);}finally{
   await new Promise(resolve=>server.close(resolve));fs.rmSync(profile,{recursive:true,force:true,maxRetries:5,retryDelay:100});if(!keepArtifacts)fs.rmSync(output,{recursive:true,force:true});
  }
 }
}
main().catch(error=>{if(keepArtifacts)fs.writeFileSync(path.join(output,'browser-failure.json'),JSON.stringify({error:error.stack,completed:checks,errors,networkFailures,requests},null,2));console.error(error);process.exitCode=1});
