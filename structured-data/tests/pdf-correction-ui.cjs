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
 assert.equal(await page.locator('.pdf-reparse-controls').evaluate(e=>e.tagName),'DETAILS');
 assert.equal(await page.locator('.pdf-reparse-controls').evaluate(e=>e.open),false);
 assert.equal(await page.locator('.pdf-reparse-controls button').count(),1);
 assert.equal(await page.locator('#pdfReparseOcrMode').count(),0);
 assert.equal(await page.locator('[data-column-name="1"]').inputValue(),'金额','validated automatic fields are restored');
 assert.equal(await page.locator('[data-column-unit="1"]').inputValue(),'原件单位');
 assert.equal(await page.locator('.pdf-manual-details').evaluate(e=>e.open),false,'recognized fields stay collapsed');
 await page.locator('.pdf-exception-drawer>summary').click();await page.getByRole('button',{name:'查看疑点',exact:true}).click();
 assert.equal(await page.locator('.pdf-quality-detail').evaluate(e=>e.open),true);
 assert.match(await page.locator('.pdf-quality-item>p').first().innerText(),/识别结果不一致/);
 assert.equal(await page.locator('.pdf-quality-item>details').evaluateAll(items=>items.some(e=>e.open)),false);
 // A cell is editable immediately, without finding a separate mode switch.
 await page.getByRole('button',{name:'修改第 2 行第 2 列',exact:true}).click();
 await page.getByRole('button',{name:'取消',exact:true}).last().click();
 const editing=await page.locator('.pdf-source-viewer').elementHandle();
 assert(await page.locator('.pdf-raw-table').evaluate(e=>e.clientHeight>=230),'multiple doubts must leave a usable table viewport');
 await page.getByRole('button',{name:'修改第 2 行第 2 列',exact:true}).click();
 assert.equal(await page.getByRole('textbox',{name:'单元格核对依据',exact:true}).count(),0);
 await page.getByRole('textbox',{name:'核对后的单元格内容',exact:true}).fill('-1.20');
 await page.getByRole('button',{name:'保存修改',exact:true}).click();
 assert.equal(await editing.evaluate(e=>e.isConnected),true);
 await page.getByRole('button',{name:'确认以上标记无误',exact:true}).click();
 const onlyFlags=await page.locator('#pdfPlanEditor').inputValue();assert.equal(JSON.parse(onlyFlags).quality_reviews.length,3);assert.equal(JSON.parse(onlyFlags).cell_corrections.length,1);
 await page.locator('.pdf-raw-table tbody tr').nth(2).getByRole('button',{name:'删除此行',exact:true}).click();
await page.getByRole('button',{name:'确认删除此行',exact:true}).click();
 assert.equal(await page.locator('.pdf-excluded-row').count(),1);
 await page.getByRole('button',{name:'恢复此行',exact:true}).click();assert.equal(await page.locator('.pdf-excluded-row').count(),0);
 await page.getByRole('button',{name:'补充漏掉的行',exact:true}).click();
 await page.getByRole('textbox',{name:'补行第 1 列',exact:true}).fill('Missing');await page.getByRole('textbox',{name:'补行第 2 列',exact:true}).fill('-2.50');
await page.getByRole('button',{name:'保存补充行',exact:true}).click();
 assert.equal(await page.locator('.pdf-inserted-row').count(),1);
 await page.locator('.pdf-manual-details').evaluate(e=>e.open=true);
 await page.locator('[data-column-name="0"]').fill('项目');await page.locator('[data-column-name="1"]').fill('金额');
 await page.locator('[data-column-type="1"]').selectOption('decimal');
 await page.locator('[data-column-unit="1"]').fill('');
 await page.locator('[data-header-fragment][data-header-row="0"]').evaluate(e=>{e.checked=true;e.dispatchEvent(new Event('change',{bubbles:true}));});
 // One final button applies unsaved field settings, validates, and then publishes.
 assert.equal(await page.locator('#pdfPublishReviewed').count(),0);
 await page.locator('#pdfPublish').click();await page.waitForFunction(()=>document.querySelector('#pdfReviewStatus')?.textContent.includes('待核对'));
 assert.equal(publishes,0,'blank uncertain cells cannot be waived by confirming quality flags');
 assert.equal(await page.locator('#pdfPreview input').count(),1);
 await page.getByRole('button',{name:'确认此项',exact:true}).click();
 // Final confirmation also saves an open cell editor, including an unchanged value.
 await page.getByRole('button',{name:'修改第 2 行第 2 列',exact:true}).click();
 await page.getByRole('textbox',{name:'核对后的单元格内容',exact:true}).fill('-1.20');
 await page.locator('#pdfPublish').click();
 await page.waitForFunction(()=>document.querySelector('#pdfReviewStatus')?.textContent.includes('待核对'));
 await page.getByRole('button',{name:'确认以上标记无误',exact:true}).click();
 await page.locator('#pdfPublish').click();await page.waitForFunction(()=>document.querySelector('#pdfReviewStatus')?.textContent.includes('已入库，可用于'));
 assert.equal(publishes,1);await page.waitForFunction(()=>document.querySelector('#pdfAutomationSlot strong')?.textContent==='解析完成 · 已入库 1 张');
 assert.equal(await page.getByRole('button',{name:'查看疑点',exact:true}).count(),0,'resolved review no longer appears as pending');
 const table=store('tables').tables[0],data=store('query',{id:table.table_id});assert.match(JSON.stringify(data),/-1.2/);
 assert.deepEqual(store('draft',{id:original.draft_id}).bundle,original.bundle);
 assert.equal(table.extraction_verified,false);assert.equal('unit' in table.columns[1],false,'clearing a unit removes the old unit');assert.equal(await page.locator('#pdfPublish').isDisabled(),true);assert.equal(table.coverage.cell_correction_count,2);assert.equal(table.coverage.manual_inserted_row_count,1);assert.match(JSON.stringify(data),/manual_transcription/);
 // Previously saved tables can be edited again; cancelling restores the saved state.
 await page.getByRole('button',{name:'修改第 2 行第 2 列',exact:true}).click();assert.equal(await page.locator('#pdfPublish').isDisabled(),false);
 await page.getByRole('button',{name:'取消',exact:true}).last().click();assert.equal(await page.locator('#pdfPublish').isDisabled(),true);
 assert.deepEqual(errors,[]);
 if(process.env.PDF_CORRECTION_EVIDENCE){fs.mkdirSync(process.env.PDF_CORRECTION_EVIDENCE,{recursive:true});await page.screenshot({path:path.join(process.env.PDF_CORRECTION_EVIDENCE,'correction-published.png'),fullPage:true});fs.writeFileSync(path.join(process.env.PDF_CORRECTION_EVIDENCE,'result.json'),JSON.stringify({table,data,publishes,originalUnchanged:true,errors},null,2));}
 console.log('PASS: Chinese issue opens and focuses, direct correction without written reasons, bulk flags cannot waive uncertain cells, one-button validation/publication, field edits preserve correction, real store preview/publish/query, original unchanged, resolved issue removed, truthful visible parser options.');
 }finally{await browser.close();server.close();fs.rmSync(dir,{recursive:true,force:true});}
})().catch(e=>{console.error(e);server.close();process.exitCode=1});
