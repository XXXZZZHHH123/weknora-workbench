/* Real stored tables: direct grid edits, column review, row operations and publication. */
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),http=require('node:http');
const {execFileSync}=require('node:child_process');
const {chromium}=require('playwright');
const dir=fs.mkdtempSync(path.join(os.tmpdir(),'pdf-navigation-'));
const py=`import sys,json
from structured_data.pdf_tables import PDFTableStore
from test_pdf_tables import bundle
s=PDFTableStore(sys.argv[1]);req=json.load(sys.stdin);op=req['op']
if op=='init':
 b=bundle([['Item','Amount']]+[[f'Label {r}',str(r*100)] for r in range(1,215 if req.get('pagination') else 38)],[['Item','Amount'],['Second-only','22']])
 for row in b['fragments'][0]['rows'][3:]:
  for cell in row: cell['uncertain']=True
 if req.get('review_flags'): b['fragments'][0]['quality']={'status':'needs_review','issues':[{'code':'MODEL_UNCERTAINTY'}]}
 if req.get('precise_issue'): b['fragments'][0]['quality']['issues'][0]['cells']=[{'row':1,'column':1}]
 r=s.create_draft(b)
elif op=='preview':r=s.validate_plan(req['id'],req['plan'])
elif op=='publish':r=s.publish(req['id'],dict(req['plan'],reviewed=True))
elif op=='tables':r=s.list_tables()
elif op=='table':r=s.read_table(req['id'])
elif op=='draft':r=s.read_draft(req['id'])
print(json.dumps(r))`;
function store(op,args={}){return JSON.parse(execFileSync('python',['-c',py,dir],{input:JSON.stringify({op,...args}),encoding:'utf8',env:{...process.env,PYTHONPATH:'structured-data/src:structured-data/tests'}}));}
const refreshFixture=true;
const draft=store('init',{review_flags:refreshFixture,pagination:Boolean(process.env.PDF_GRID_PAGINATION),precise_issue:Boolean(process.env.PDF_PRECISE_FIXTURE)});
const plans=draft.bundle.fragments.map(f=>({bundle_sha256:draft.bundle_sha256,fragment_ids:[f.id],header_rows:{[f.id]:[{row:0,reason:'source header'}]},columns:[{name:'Item',type:'text',source_columns:{[f.id]:0}},{name:'Amount',type:'decimal',source_columns:{[f.id]:1}}],excluded_rows:[],boundary_merges:[]}));
const grouped=Boolean(process.env.PDF_GROUPED_FIXTURE);
if(grouped){const ids=draft.bundle.fragments.map(f=>f.id);plans[0]={...plans[0],fragment_ids:ids,header_rows:Object.fromEntries(ids.map(id=>[id,[{row:0,reason:'source header'}]])),columns:plans[0].columns.map((c,i)=>({...c,source_columns:Object.fromEntries(ids.map(id=>[id,i]))}))};}
let automation={status:'needs_attention',accepted_count:0,attention_count:grouped?1:2,groups:(grouped?[plans[0]]:plans).map(plan=>({fragment_ids:plan.fragment_ids,status:'needs_attention',plan,problem:{action:process.env.PDF_AUTO_FIXTURE?'retry':'review',title:'待核对'}}))};
let delayFirst=false,delayPublication=false,failNextPreview=false,previewCalls=0,publishes=[];
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
 else if(url.pathname.endsWith('/preview')){previewCalls++;if(failNextPreview){failNextPreview=false;throw Error('预览请求暂时失败');}value=store('preview',{id:draft.draft_id,plan:body.plan});if(delayFirst&&body.plan.fragment_ids[0]==='table-1')await new Promise(r=>setTimeout(r,600));}
 else if(url.pathname.endsWith('/publish')){assert.equal(body.reviewed,true);value=store('publish',{id:draft.draft_id,plan:body.plan});publishes.push(body.plan.fragment_ids);if(delayPublication)await new Promise(r=>setTimeout(r,600));}
 else{res.statusCode=404;value={detail:'fixture path missing'};}
 res.setHeader('Content-Type',type);res.end(typeof value==='string'||Buffer.isBuffer(value)?value:JSON.stringify(value));
 }catch(error){res.statusCode=400;res.end(JSON.stringify({detail:error.message}));}});
(async()=>{
 await new Promise(r=>server.listen(0,'127.0.0.1',r));
 const browser=await chromium.launch({executablePath:process.env.SHEET_REVIEW_BROWSER,headless:true,args:['--no-sandbox','--disable-dev-shm-usage']});
 try{
 const page=await browser.newPage({viewport:{width:1500,height:1100}}),errors=[];page.on('pageerror',e=>errors.push(e.message));
 await page.goto('http://127.0.0.1:'+server.address().port);await page.locator('.pdf-file-actions button').last().click();
 const grid=page.locator('#pdfPreview');await grid.locator('.pdf-grid-input').first().waitFor();
 const cell=(row,col)=>grid.locator(`textarea[data-fragment-id="table-1"][data-source-row="${row}"][data-source-column="${col}"]`);
 const plan=async()=>JSON.parse(await page.locator('#pdfPlanEditor').inputValue());

 if(process.env.PDF_GRID_PAGINATION){
  assert.equal(await grid.locator('.is-pending').count(),396);
  await grid.locator('.pdf-column-review[data-plan-column="0"]').click();await grid.locator('.pdf-column-review[data-plan-column="1"]').click();
  assert.equal((await plan()).cell_corrections.length,396,'column confirmation only covers rendered source cells');
  await page.locator('#pdfPublish').click();await page.waitForFunction(()=>document.querySelector('#pdfDetailStatus')?.textContent.includes('待核对'));assert.equal(publishes.length,0);
  await page.locator('.pdf-review-findings').getByRole('button',{name:'Item · 14 处',exact:true}).click();
  assert.match(await grid.locator('.pdf-grid-pages').innerText(),/201–214/);assert.equal(await grid.locator('.is-pending').count(),28);
  await grid.locator('.pdf-column-review[data-plan-column="0"]').click();await grid.locator('.pdf-column-review[data-plan-column="1"]').click();
  await page.locator('#pdfPublish').click();await page.waitForFunction(()=>document.querySelector('#pdfPublish')?.textContent==='已入库');assert.equal(publishes.length,1);assert.equal((await plan()).cell_corrections.length,424);
  assert.deepEqual(errors,[]);console.log('PASS: paged column review never confirms unseen cells; column summary locates the remaining page.');return;
 }
 assert.equal(await grid.locator('.is-pending').count(),70);assert.equal((await plan()).cell_corrections?.length||0,0,'opening the grid does not review any cell');
 assert.match(await page.locator('.pdf-review-findings').innerText(),/Item.*35.*Amount.*35/s);
 assert.equal(await page.getByRole('button',{name:'查看并修改',exact:true}).count(),0);
 await cell(3,0).fill('Edited label');await cell(3,1).fill('-1.20');
 assert.equal(await page.locator('.pdf-cell-editor').count(),0);assert.equal(await grid.locator('.is-pending').count(),68);
 if(process.env.PDF_GRID_EVIDENCE)await page.screenshot({path:path.join(process.env.PDF_GRID_EVIDENCE,'inline-pending.png')});
 await page.locator('#pdfPublish').click();await page.waitForFunction(()=>document.querySelector('#pdfDetailStatus')?.textContent.includes('待核对'));
 assert.equal(publishes.length,0,'unreviewed cells still block publication');assert.equal(await cell(3,1).inputValue(),'-1.20');
 await grid.locator('.pdf-column-review[data-plan-column="0"]').click();await grid.locator('.pdf-column-review[data-plan-column="1"]').click();
 assert.equal(await grid.locator('.is-pending').count(),0);assert.equal((await plan()).cell_corrections.length,70,'two explicit column confirmations record the remaining exact source cells');
 const removeRow=cell(1,0).locator('xpath=ancestor::tr');await removeRow.getByRole('button',{name:'删除行',exact:true}).click();
 assert.equal((await plan()).excluded_rows.length,1);await removeRow.getByRole('button',{name:'恢复行',exact:true}).click();assert.equal((await plan()).excluded_rows.length,0);
 await removeRow.getByRole('button',{name:'删除行',exact:true}).click();
 await page.getByRole('button',{name:'新增行',exact:true}).click();
 await grid.getByRole('textbox',{name:'补行第 1 列',exact:true}).fill('Manual row');await grid.getByRole('textbox',{name:'补行第 2 列',exact:true}).fill('1000');
 const manualRow=grid.getByRole('textbox',{name:'补行第 1 列',exact:true}).locator('xpath=ancestor::tr');
 await manualRow.getByRole('button',{name:'删除行',exact:true}).click();assert.equal((await plan()).inserted_rows.length,0);
 await manualRow.getByRole('button',{name:'恢复行',exact:true}).click();assert.equal((await plan()).inserted_rows.length,1);assert.equal(await grid.getByRole('textbox',{name:'补行第 2 列',exact:true}).inputValue(),'1000');
 await cell(3,1).fill('invalid decimal');await page.locator('#pdfPublish').click();await page.locator('#pdfDetailStatus').waitFor({state:'visible'});
 assert.equal(publishes.length,0);assert.equal(await cell(3,1).inputValue(),'invalid decimal','validation errors keep the editable grid and user input');await cell(3,1).fill('-1.20');
 await grid.getByRole('textbox',{name:'第 1 列名称',exact:true}).fill('Name');assert.equal(await cell(3,0).inputValue(),'Edited label','column edits do not remove the grid');
 await page.locator('#pdfFragmentSelect').selectOption('1');await grid.locator('textarea[data-fragment-id="table-2"]').first().waitFor();
 assert.equal((await plan()).cell_corrections?.length||0,0,'column review cannot mark a different table reviewed');
 await page.locator('#pdfFragmentSelect').selectOption('0');await cell(3,0).waitFor();
 assert.equal(await cell(3,0).inputValue(),'Edited label');assert.equal(await cell(3,1).inputValue(),'-1.20');assert.equal(await grid.getByRole('textbox',{name:'补行第 1 列',exact:true}).inputValue(),'Manual row');
 assert.equal(await grid.getByRole('textbox',{name:'第 1 列名称',exact:true}).inputValue(),'Name');
 await page.locator('#pdfPublish').click();await page.waitForFunction(()=>document.querySelector('#pdfPublish')?.textContent==='已入库');
 assert.equal(publishes.length,1);assert.deepEqual(publishes[0],['table-1']);
 const record=store('table',{id:store('tables').tables[0].table_id});assert.equal(record.plan.cell_corrections.length,70);assert.equal(record.plan.inserted_rows.length,1);assert.equal(record.plan.excluded_rows.length,1);
 assert(record.rows.some(r=>r.values.Name==='Edited label'&&Number(r.values.Amount)===-1.2));assert(record.rows.some(r=>r.values.Name==='Manual row'&&Number(r.values.Amount)===1000));
 assert(!record.rows.some(r=>r.values.Name==='Label 1'));assert.deepEqual(store('draft',{id:draft.draft_id}).bundle,draft.bundle);
 assert(record.plan.cell_corrections.every(c=>c.fragment_id==='table-1'));assert.deepEqual(errors,[]);
 if(process.env.PDF_GRID_EVIDENCE)await page.screenshot({path:path.join(process.env.PDF_GRID_EVIDENCE,'inline-published.png')});
 console.log('PASS: editable preview, 70 precise flags summarized by column, direct multi-cell edits, explicit column review, add/delete/restore rows, editable validation errors, switch persistence, real stored publication and immutable original.');
 }finally{await browser.close();server.close();fs.rmSync(dir,{recursive:true,force:true});}
})().catch(error=>{console.error(error);server.close();process.exitCode=1});
