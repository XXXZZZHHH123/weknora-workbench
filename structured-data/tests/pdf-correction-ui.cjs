/* Real PDFTableStore behind the browser: issue -> correction -> preview -> publication. */
const assert=require('node:assert/strict'),fs=require('fs'),os=require('os'),path=require('path'),http=require('http');
const {execFileSync}=require('node:child_process');
const {chromium}=require('playwright');
const dir=fs.mkdtempSync(path.join(os.tmpdir(),'pdf-correction-'));
const python=`import sys,json\nfrom structured_data.pdf_tables import PDFTableStore\nfrom test_pdf_tables import bundle\ns=PDFTableStore(sys.argv[1]);req=json.load(sys.stdin);op=req['op']\nif op=='init':\n b=bundle([['Item','Amount'],['Example',''],['Blank','']]);b['fragments'][0]['quality']={'status':'needs_review','issues':[{'code':'TRANSCRIPTION_CELL_CONFLICT','message':'Independent transcriptions disagree on source cells'},{'code':'MISSING_HEADER','message':'At least one header is absent or unreadable'},{'code':'MODEL_UNCERTAINTY','message':'The unit row requires review'}]};b['fragments'][0]['rows'][1][1]['uncertain']=True;b['fragments'][0]['rows'][2][1]['uncertain']=True;r=s.create_draft(b)\nelif op=='preview':r=s.validate_plan(req['id'],req['plan'])\nelif op=='publish':r=s.publish(req['id'],dict(req['plan'],reviewed=True))\nelif op=='tables':r=s.list_tables()\nelif op=='draft':r=s.read_draft(req['id'])\nelif op=='query':r=s.query(req['id'])\nprint(json.dumps(r))`;
function store(op,values={}){return JSON.parse(execFileSync('python',['-c',python,dir],{input:JSON.stringify({op,...values}),env:{...process.env,PYTHONPATH:'structured-data/src:structured-data/tests'},encoding:'utf8'}));}
const original=store('init');const savedPlan={bundle_sha256:original.bundle_sha256,fragment_ids:['table-1'],header_rows:{'table-1':[{row:0,reason:'真实列名'}]},columns:[{name:'项目',type:'text',source_columns:{'table-1':0}},{name:'金额',type:'text',unit:'原件单位',source_columns:{'table-1':1}}],excluded_rows:[],boundary_merges:[]};const automation={status:'needs_attention',accepted_count:0,attention_count:1,groups:[{fragment_ids:['table-1'],plan:savedPlan,status:'needs_attention',problem:{title:'表格存在识别疑点',message:'请对照原件核对',action:'review'}}]};
let publishes=0;
const source=path.resolve('structured-data/src/structured_data');
const html=fs.readFileSync(path.join(source,'index.html'),'utf8'),css=html.match(/<style>([\s\S]*?)<\/style>/)[1]+fs.readFileSync(path.join(source,'workspace.css'),'utf8');
const server=http.createServer(async(req,res)=>{try{
 let data='';for await(const chunk of req)data+=chunk;const body=data?JSON.parse(data):{};let value,type='application/json';
 const route=req.url.split('?')[0];
 if(route==='/'){type='text/html';value=`<!doctype html><meta charset="utf-8"><style>${css}</style><body class="embedded knowledge-view view-import document-results"><div id="workspace"><section id="pdfReviewPanel"><div id="pdfReviewApp"></div></section></div><script>window.pdfReviewHost={getAuthEpoch:()=>0,isWorkspaceReady:()=>true,unifiedUpload:true,api:async(p,o={})=>{const r=await fetch(p,o);const v=await r.json();if(!r.ok)throw Error(v.detail);return v},authenticatedFetch:(p,o)=>fetch(p,o)};</script><script src="/pdf-review.js"></script>`;}
 else if(route==='/pdf-review.js'){type='text/javascript';value=fs.readFileSync(path.join(source,'static/pdf-review.js'));}
 else if(route==='/api/pdf/config')value={configured:true,engine:'remote_vlm'};
 else if(route==='/api/pdf/drafts')value={drafts:[{...original,source:original.bundle.source,fragment_count:1,automation}]};
 else if(route==='/api/pdf/jobs')value={jobs:[]};
 else if(route==='/api/pdf/tables')value=store('tables');
 else if(route.endsWith('/issue-explanations'))value={issues:[{issue_index:0,explanation:'两次识别的内容不一致，需要对照原件核对。',action:'核对原页后修改数据。'},{issue_index:1,explanation:'原件列名缺失或看不清。',action:'填写有依据的列名。'},{issue_index:2,explanation:'单位的读取结果需要核对。',action:'对照原件确认单位。'}]};
 else if(route.endsWith('/source')){type='application/pdf';value='%PDF-1.4\nfixture';}
 else if(route==='/api/pdf/drafts/'+original.draft_id)value={...store('draft',{id:original.draft_id}),automation};
 else if(route.endsWith('/preview'))value=store('preview',{id:original.draft_id,plan:body.plan});
 else if(route.endsWith('/publish')){assert.equal(body.reviewed,true);value=store('publish',{id:original.draft_id,plan:body.plan});publishes++;}
 else{res.statusCode=404;value={detail:'fixture route not found'};}
 res.setHeader('Content-Type',type);res.end(typeof value==='string'||Buffer.isBuffer(value)?value:JSON.stringify(value));
 }catch(e){res.statusCode=400;res.end(JSON.stringify({detail:e.message}));}});
(async()=>{
 await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));const browser=await chromium.launch({headless:true,executablePath:process.env.SHEET_REVIEW_BROWSER,args:['--no-sandbox','--disable-dev-shm-usage']});
 try{const page=await browser.newPage({viewport:{width:1450,height:1000}}),errors=[];page.on('pageerror',e=>errors.push(e.message));
 await page.goto('http://127.0.0.1:'+server.address().port);
 await page.locator('.pdf-file-actions button').last().click();await page.locator('#pdfFragmentSelect').waitFor();
 assert.equal(await page.locator('#pdfManualForm,.pdf-mapping-section,.pdf-supporting,.pdf-technical').count(),0);
 assert.equal(await page.getByRole('button',{name:'重新解析',exact:true}).count(),1);
 assert.equal(await page.getByRole('textbox',{name:'第 2 列名称',exact:true}).inputValue(),'金额');
 assert.equal(await page.locator('#pdfPlanEditor').isVisible(),false);
 assert.match(await page.locator('.pdf-review-findings').innerText(),/第 2 行，第 2 列/);
 assert.match(await page.locator('.pdf-review-findings').innerText(),/未提供具体错误位置/);
 assert.doesNotMatch(await page.locator('#pdfDraftDetail').innerText(),/请补全列名|检查是否有漏行|请核对文字|更多来源线索|表格设置|高级映射/);
 // The issue action opens exactly the cited cell, not a generic form.
 await page.locator('.pdf-review-findings .pdf-quality-item').filter({hasText:'第 2 行，第 2 列'}).getByRole('button',{name:'查看并修改'}).click();
 assert.match(await page.locator('.pdf-cell-editor legend').innerText(),/第 2 行 · 第 2 列/);
 assert.equal(await page.locator('td.pdf-cell-focused').getAttribute('data-source-row'),'1');
 await page.getByRole('textbox',{name:'核对后的单元格内容',exact:true}).fill('-1.20');
 await page.getByRole('button',{name:'保存修改',exact:true}).click();
 const viewer=await page.locator('.pdf-source-viewer').elementHandle();
 const menu=()=>page.locator('.pdf-raw-table tbody tr').nth(2).locator('.pdf-row-menu');
 await menu().locator('summary').click();await menu().getByRole('button',{name:'删除此行',exact:true}).click();
 assert.equal(await page.getByRole('textbox',{name:'行修改的原件依据'}).count(),0);
 await page.getByRole('button',{name:'确认删除此行',exact:true}).click();assert.equal(await page.locator('.pdf-excluded-row').count(),1);
 await menu().locator('summary').click();await menu().getByRole('button',{name:'恢复此行',exact:true}).click();assert.equal(await page.locator('.pdf-excluded-row').count(),0);
 await page.getByRole('button',{name:'补充漏掉的行',exact:true}).click();
 await page.getByRole('textbox',{name:'补行第 1 列',exact:true}).fill('Missing');await page.getByRole('textbox',{name:'补行第 2 列',exact:true}).fill('-2.50');
 await page.getByRole('button',{name:'保存补充行',exact:true}).click();assert.equal(await page.locator('.pdf-inserted-row').count(),1);
 const headerMenu=()=>page.locator('.pdf-raw-table tbody tr').first().locator('.pdf-row-menu');
 await headerMenu().locator('summary').click();await headerMenu().getByRole('button',{name:'改为数据行',exact:true}).click();
 await headerMenu().locator('summary').click();await headerMenu().getByRole('button',{name:'用这行作列名',exact:true}).click();
 assert.equal(await page.getByRole('textbox',{name:'第 2 列名称',exact:true}).inputValue(),'Amount');
 await page.getByRole('textbox',{name:'第 1 列名称',exact:true}).fill('项目');await page.getByRole('textbox',{name:'第 2 列名称',exact:true}).fill('金额');
 const options=page.locator('.pdf-column-options').nth(1);await options.locator('summary').click();
 await page.getByRole('combobox',{name:'第 2 列类型',exact:true}).selectOption('decimal');await page.getByRole('textbox',{name:'第 2 列单位',exact:true}).fill('');
 await page.getByRole('button',{name:'已对照原件核对',exact:true}).click();
 let plan=JSON.parse(await page.locator('#pdfPlanEditor').inputValue());assert.equal(plan.quality_reviews.length,3);assert.equal(plan.cell_corrections.length,1);
 assert(await viewer.evaluate(e=>e.isConnected));
 await page.locator('#pdfPublish').click();await page.waitForFunction(()=>document.querySelector('#pdfReviewStatus')?.textContent.includes('待核对'));
 assert.equal(publishes,0,'whole-table confirmation cannot waive an uncertain blank cell');
 await page.locator('.pdf-review-findings').getByRole('button',{name:'查看并修改',exact:true}).click();
 assert.equal(await page.getByRole('textbox',{name:'核对后的单元格内容'}).inputValue(),'');
 // Final confirmation saves this open editor, then still requires table flags to be confirmed.
 await page.locator('#pdfPublish').click();await page.waitForFunction(()=>document.querySelector('#pdfReviewStatus')?.textContent.includes('待核对'));
 await page.getByRole('button',{name:'已对照原件核对',exact:true}).click();await page.locator('#pdfPublish').click();
 await page.waitForFunction(()=>document.querySelector('#pdfPublish')?.textContent==='已入库');
 assert.equal(publishes,1);await page.waitForFunction(()=>document.querySelector('#pdfAutomationSlot strong')?.textContent==='解析完成 · 已入库 1 张');
 const table=store('tables').tables[0],data=store('query',{id:table.table_id});assert.match(JSON.stringify(data),/-1.2/);
 assert.deepEqual(store('draft',{id:original.draft_id}).bundle,original.bundle);
 assert.equal(table.extraction_verified,false);assert.equal('unit' in table.columns[1],false);assert.equal(table.coverage.cell_correction_count,2);assert.equal(table.coverage.manual_inserted_row_count,1);
 assert.equal(await page.locator('#pdfPublish').isDisabled(),true);
 await page.locator('#pdfTogglePreview').click();
 await page.getByRole('button',{name:'修改第 2 行第 2 列',exact:true}).click();assert.equal(await page.locator('#pdfPublish').isDisabled(),false);
 await page.getByRole('button',{name:'取消',exact:true}).last().click();assert.equal(await page.locator('#pdfPublish').isDisabled(),true);
 assert.deepEqual(errors,[]);
 if(process.env.PDF_CORRECTION_EVIDENCE){fs.mkdirSync(process.env.PDF_CORRECTION_EVIDENCE,{recursive:true});await page.locator('#pdfDraftDetail').evaluate(e=>e.scrollTop=0);await page.screenshot({path:path.join(process.env.PDF_CORRECTION_EVIDENCE,'correction-published.png')});}
 console.log('PASS: direct column/format/header edits, exact source-cell focus, consolidated unlocated review, no explanatory/settings panels, no uncertain-cell bypass, real store publication, immutable original, repeat edit.');
 }finally{await browser.close();server.close();fs.rmSync(dir,{recursive:true,force:true});}
})().catch(e=>{console.error(e);server.close();process.exitCode=1});
