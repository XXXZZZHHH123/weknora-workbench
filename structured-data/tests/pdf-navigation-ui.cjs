/* Two real stored tables: isolate edits, previews, saved status and late responses. */
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),http=require('node:http');
const {execFileSync}=require('node:child_process');
const {chromium}=require('playwright');
const dir=fs.mkdtempSync(path.join(os.tmpdir(),'pdf-navigation-'));
const py=`import sys,json\nfrom structured_data.pdf_tables import PDFTableStore\nfrom test_pdf_tables import bundle\ns=PDFTableStore(sys.argv[1]);req=json.load(sys.stdin);op=req['op']\nif op=='init':r=s.create_draft(bundle([['Item','Amount'],['First-only','11']],[['Item','Amount'],['Second-only','22']]))\nelif op=='preview':r=s.validate_plan(req['id'],req['plan'])\nelif op=='publish':r=s.publish(req['id'],dict(req['plan'],reviewed=True))\nelif op=='tables':r=s.list_tables()\nelif op=='table':r=s.read_table(req['id'])\nprint(json.dumps(r))`;
function store(op,args={}){return JSON.parse(execFileSync('python',['-c',py,dir],{input:JSON.stringify({op,...args}),encoding:'utf8',env:{...process.env,PYTHONPATH:'structured-data/src:structured-data/tests'}}));}
const draft=store('init');
const plans=draft.bundle.fragments.map(f=>({bundle_sha256:draft.bundle_sha256,fragment_ids:[f.id],header_rows:{[f.id]:[{row:0,reason:'source header'}]},columns:[{name:'Item',type:'text',source_columns:{[f.id]:0}},{name:'Amount',type:'decimal',source_columns:{[f.id]:1}}],excluded_rows:[],boundary_merges:[]}));
const grouped=Boolean(process.env.PDF_GROUPED_FIXTURE);
if(grouped){const ids=draft.bundle.fragments.map(f=>f.id);plans[0]={...plans[0],fragment_ids:ids,header_rows:Object.fromEntries(ids.map(id=>[id,[{row:0,reason:'source header'}]])),columns:plans[0].columns.map((c,i)=>({...c,source_columns:Object.fromEntries(ids.map(id=>[id,i]))}))};}
let automation={status:'needs_attention',accepted_count:0,attention_count:grouped?1:2,groups:(grouped?[plans[0]]:plans).map(plan=>({fragment_ids:plan.fragment_ids,status:'needs_attention',plan,problem:{action:process.env.PDF_AUTO_FIXTURE?'retry':'review',title:'待核对'}}))};
let delayFirst=false,delayPublication=false,previewCalls=0,publishes=[];
const source=path.resolve('structured-data/src/structured_data');
const css=fs.readFileSync(path.join(source,'index.html'),'utf8').match(/<style>([\s\S]*?)<\/style>/)[1]+fs.readFileSync(path.join(source,'workspace.css'),'utf8');
const server=http.createServer(async(req,res)=>{try{
 let raw='';for await(const b of req)raw+=b;const body=raw?JSON.parse(raw):{};const url=new URL(req.url,'http://localhost');let value,type='application/json';
 if(url.pathname==='/'){type='text/html';value=`<!doctype html><meta charset="utf-8"><style>${css}</style><body class="embedded knowledge-view view-import document-results"><div id="workspace"><section id="pdfReviewPanel"><div id="pdfReviewApp"></div></section></div><script>window.pdfReviewHost={getAuthEpoch:()=>0,isWorkspaceReady:()=>true,unifiedUpload:true,api:async(p,o={})=>{const r=await fetch(p,o);const v=await r.json();if(!r.ok)throw Error(v.detail);return v},authenticatedFetch:(p,o)=>fetch(p,o)}</script><script src="/pdf-review.js"></script>`;}
 else if(url.pathname==='/pdf-review.js'){type='text/javascript';value=fs.readFileSync(path.join(source,'static/pdf-review.js'));}
 else if(url.pathname==='/api/pdf/config')value={configured:true,engine:'remote_vlm'};
 else if(url.pathname==='/api/pdf/jobs')value={jobs:[]};
 else if(url.pathname.endsWith('/auto-process')){const saved=plans.map(plan=>store('publish',{id:draft.draft_id,plan}));automation={status:'completed',accepted_count:saved.length,attention_count:0,groups:saved.map(t=>({status:'accepted',table_id:t.table_id,fragment_ids:t.plan.fragment_ids}))};value={id:'automatic'};}
 else if(url.pathname==='/api/pdf/jobs/automatic')value={status:'completed',automatic_result:automation};
 else if(url.pathname==='/api/pdf/drafts')value={drafts:[{...draft,source:draft.bundle.source,fragment_count:2,automation}]};
 else if(url.pathname==='/api/pdf/tables')value=store('tables');
 else if(url.pathname.startsWith('/api/pdf/tables/'))value=store('table',{id:url.pathname.split('/').pop()});
 else if(url.pathname.endsWith('/source')){type='application/pdf';value='%PDF-1.4\nfixture';}
 else if(url.pathname==='/api/pdf/drafts/'+draft.draft_id)value={...draft,automation};
 else if(url.pathname.endsWith('/preview')){previewCalls++;value=store('preview',{id:draft.draft_id,plan:body.plan});if(delayFirst&&body.plan.fragment_ids[0]==='table-1')await new Promise(r=>setTimeout(r,600));}
 else if(url.pathname.endsWith('/publish')){assert.equal(body.reviewed,true);value=store('publish',{id:draft.draft_id,plan:body.plan});publishes.push(body.plan.fragment_ids);if(delayPublication)await new Promise(r=>setTimeout(r,600));}
 else{res.statusCode=404;value={detail:'fixture path missing'};}
 res.setHeader('Content-Type',type);res.end(typeof value==='string'||Buffer.isBuffer(value)?value:JSON.stringify(value));
 }catch(error){res.statusCode=400;res.end(JSON.stringify({detail:error.message}));}});
(async()=>{
 await new Promise(r=>server.listen(0,'127.0.0.1',r));
 const browser=await chromium.launch({executablePath:process.env.SHEET_REVIEW_BROWSER,headless:true,args:['--no-sandbox','--disable-dev-shm-usage']});
 try{
 const page=await browser.newPage({viewport:{width:1450,height:1000}}),errors=[];page.on('pageerror',e=>errors.push(e.message));
 const open=async()=>{await page.goto('http://127.0.0.1:'+server.address().port);await page.locator('.pdf-file-actions button').last().click();await page.locator('.pdf-column-heading input').first().waitFor();};
 const select=async index=>{await page.locator('#pdfFragmentSelect').selectOption(String(index));await page.waitForFunction(i=>JSON.parse(document.querySelector('#pdfPlanEditor').value).fragment_ids.includes('table-'+(i+1)),index);};
 const expectPreview=async word=>{await page.waitForFunction(w=>document.querySelector('#pdfPreview')?.textContent.includes(w),word);const text=await page.locator('#pdfPreview').innerText();assert(!text.includes(word==='First-only'?'Second-only':'First-only'));};
 await open();
 if(process.env.PDF_AUTO_FIXTURE){
  await page.getByRole('textbox',{name:'第 2 列名称',exact:true}).fill('Unsaved first edit');await select(1);
  await page.locator('#pdfAutoProcess').click();await page.waitForFunction(()=>document.querySelector('#pdfPublish')?.textContent==='已入库');
  await select(0);assert.equal(await page.getByRole('textbox',{name:'第 2 列名称',exact:true}).inputValue(),'Unsaved first edit');
  assert.equal(await page.locator('#pdfPublish').innerText(),'确认并入库','automatic completion preserves unsaved edits');
  await select(1);assert.equal(await page.locator('#pdfPublish').innerText(),'已入库');assert.deepEqual(errors,[]);
  console.log('PASS: automatic completion refreshes the stored table and saved status without replacing unsaved edits.');return;
 }
 assert.equal(await page.locator('.pdf-confirm-bar,.pdf-table-actions,.pdf-detail-status').count(),0);
 assert(await page.locator('#pdfPublish').evaluate(e=>e.closest('.pdf-table-toolbar')!==null));
 assert(await page.locator('#pdfTogglePreview').evaluate(e=>e.closest('.pdf-table-toolbar')!==null));
 assert.equal(await page.locator('.pdf-raw-table thead th>small').count(),0,'one editable name per column');
 await page.locator('#pdfTogglePreview').click();await expectPreview('First-only');
 await page.locator('#pdfPublish').click();await page.waitForFunction(()=>document.querySelector('#pdfPublish').textContent==='已入库');
 const savedCount=publishes.length;assert.equal(savedCount,1);
 await select(1);await expectPreview('Second-only');
 if(grouped){assert.equal(await page.locator('#pdfPublish').innerText(),'已入库','the same real logical table shares its saved state');}
 else{assert.equal(await page.locator('#pdfPublish').innerText(),'确认并入库','second table must not inherit first saved status');assert.equal(await page.locator('#pdfPublish').isEnabled(),true);}
 await select(0);await expectPreview('First-only');assert.equal(await page.locator('#pdfPublish').innerText(),'已入库');
 // A fresh visit restores the actual saved plan from the protected table endpoint.
 await page.locator('#pdfBackToFiles').click();await page.locator('.pdf-file-actions button').last().click();
 await page.locator('#pdfFragmentSelect').waitFor();await select(0);
 await page.waitForFunction(()=>document.querySelector('#pdfPublish')?.textContent==='已入库');
 await page.locator('#pdfTogglePreview').click();await expectPreview('First-only');
 if(!grouped){
 await page.locator('#pdfTogglePreview').click();
 await page.getByRole('textbox',{name:'第 2 列名称',exact:true}).fill('First edited');
 await select(1);await select(0);assert.equal(await page.getByRole('textbox',{name:'第 2 列名称',exact:true}).inputValue(),'First edited','switching keeps unsaved field edits');
 assert.equal(await page.locator('#pdfPublish').innerText(),'确认并入库');
 // Neither stale successes nor a confirm waiting for old validation may affect the new table.
 delayFirst=true;const start=previewCalls;
 await page.locator('#pdfTogglePreview').click();while(previewCalls===start)await new Promise(r=>setTimeout(r,10));
 await select(1);await expectPreview('Second-only');await page.waitForTimeout(700);await expectPreview('Second-only');
 await select(0);await page.locator('#pdfTogglePreview').click();
 await page.getByRole('textbox',{name:'第 2 列名称',exact:true}).fill('First changed again');
 const prior=previewCalls;await page.locator('#pdfPublish').click();while(previewCalls===prior)await new Promise(r=>setTimeout(r,10));
 await select(1);await page.waitForTimeout(700);assert.equal(publishes.length,savedCount,'navigating away during validation cannot publish a different table');
 assert.equal(await page.locator('#pdfPublish').innerText(),'确认并入库');
 delayFirst=false;delayPublication=true;await page.locator('#pdfPublish').click();while(publishes.length===savedCount)await new Promise(r=>setTimeout(r,10));
 await select(0);await page.waitForTimeout(700);assert.equal(await page.locator('#pdfPublish').innerText(),'确认并入库','late save cannot mark a different edited table saved');
 await select(1);await page.waitForFunction(()=>document.querySelector('#pdfPublish').textContent==='已入库');assert.deepEqual(publishes.at(-1),['table-2']);
 }
 assert.equal(await page.locator('#pdfDetailStatus').isVisible(),false,'no success explanation below controls');assert.deepEqual(errors,[]);
 if(process.env.PDF_NAV_EVIDENCE){fs.mkdirSync(process.env.PDF_NAV_EVIDENCE,{recursive:true});await page.locator('#pdfDraftDetail').evaluate(e=>e.scrollTop=0);await page.screenshot({path:path.join(process.env.PDF_NAV_EVIDENCE,grouped?'grouped.png':'navigation.png')});}
 console.log(JSON.stringify({separateTables:!grouped,grouped,previewFollowsSelection:true,savedStatusFollowsSelection:true,restoreStoredPlan:true,stalePreviewSuppressed:true,noWrongPublication:true,errors}));
 }finally{await browser.close();server.close();fs.rmSync(dir,{recursive:true,force:true});}
})().catch(error=>{console.error(error);server.close();process.exitCode=1});
