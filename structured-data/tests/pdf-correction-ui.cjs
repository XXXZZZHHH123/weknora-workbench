/* Real PDFTableStore behind the browser: issue -> correction -> preview -> publication. */
const assert=require('node:assert/strict'),fs=require('fs'),os=require('os'),path=require('path'),http=require('http');
const {execFileSync}=require('node:child_process');
const {chromium}=require('playwright');
const dir=fs.mkdtempSync(path.join(os.tmpdir(),'pdf-correction-'));
const python=`import sys,json\nfrom structured_data.pdf_tables import PDFTableStore\nfrom test_pdf_tables import bundle\ns=PDFTableStore(sys.argv[1]);req=json.load(sys.stdin);op=req['op']\nif op=='init':\n b=bundle([['Item','Amount'],['Example',''],['Blank','']]);b['fragments'][0]['quality']={'status':'needs_review','issues':[{'code':'TRANSCRIPTION_CELL_CONFLICT','message':'Independent transcriptions disagree on source cells'},{'code':'MISSING_HEADER','message':'At least one header is absent or unreadable'},{'code':'MODEL_UNCERTAINTY','message':'The unit row requires review'}]};b['fragments'][0]['rows'][1][1]['uncertain']=True;r=s.create_draft(b)\nelif op=='preview':r=s.validate_plan(req['id'],req['plan'])\nelif op=='publish':r=s.publish(req['id'],dict(req['plan'],reviewed=True))\nelif op=='tables':r=s.list_tables()\nelif op=='draft':r=s.read_draft(req['id'])\nelif op=='query':r=s.query(req['id'])\nprint(json.dumps(r))`;
function store(op,values={}){return JSON.parse(execFileSync('python',['-c',python,dir],{input:JSON.stringify({op,...values}),env:{...process.env,PYTHONPATH:'structured-data/src:structured-data/tests'},encoding:'utf8'}));}
const original=store('init');const automation={status:'needs_attention',accepted_count:0,attention_count:1,groups:[{fragment_ids:['table-1'],status:'needs_attention',problem:{title:'表格存在识别疑点',message:'请对照原件核对',action:'review'}}]};
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
 assert.equal(await page.locator('.pdf-reparse-controls').evaluate(e=>e.tagName),'SECTION');
 assert.equal(await page.getByRole('button',{name:'重新读取原文件',exact:true}).count(),1);
 assert.equal(await page.locator('#pdfReparseOcrMode').count(),0);
 await page.locator('.pdf-exception-drawer>summary').click();await page.getByRole('button',{name:'查看疑点',exact:true}).click();
 assert.equal(await page.locator('.pdf-quality-detail').evaluate(e=>e.open),true);
 assert.match(await page.locator('.pdf-quality-item>p').first().innerText(),/多次识别得到的单元格内容不同/);
 assert.equal(await page.locator('.pdf-quality-item>details').evaluateAll(items=>items.some(e=>e.open)),false);
 // A cell is editable immediately, without finding a separate mode switch.
 await page.getByRole('button',{name:'修改第 2 行第 2 列',exact:true}).click();
 await page.getByRole('button',{name:'取消',exact:true}).last().click();
 const editing=await page.locator('.pdf-source-viewer').elementHandle();
 assert(await page.locator('.pdf-raw-table').evaluate(e=>e.clientHeight>=230),'multiple doubts must leave a usable table viewport');
 await page.getByRole('button',{name:'修改第 2 行第 2 列',exact:true}).click();
 await page.getByRole('button',{name:'保存此格修改',exact:true}).click();assert.match(await page.locator('.pdf-cell-editor').innerText(),/请填写你在原件/);
 await page.getByRole('textbox',{name:'核对后的单元格内容',exact:true}).fill('-1.20');
 await page.getByRole('textbox',{name:'单元格核对依据',exact:true}).fill('核对第1页第2行，原文为负数 -1.20');
 await page.getByRole('button',{name:'保存此格修改',exact:true}).click();
 assert.equal(await editing.evaluate(e=>e.isConnected),true);
 for(let i=0;i<3;i++){await page.getByRole('textbox',{name:'疑点 '+(i+1)+' 核对依据',exact:true}).fill('对照第1页全部行列及列名单位，已修正第2行金额，空白单元格与原件一致');await page.getByRole('button',{name:'已核对，记录依据',exact:true}).nth(i).click();}
 await page.locator('.pdf-raw-table tbody tr').nth(2).getByRole('button',{name:'删除此行',exact:true}).click();
 await page.getByRole('textbox',{name:'行修改的原件依据'}).fill('原件没有这一空白误识别行');await page.getByRole('button',{name:'确认删除此行',exact:true}).click();
 assert.equal(await page.locator('.pdf-excluded-row').count(),1);
 await page.getByRole('button',{name:'恢复此行',exact:true}).click();assert.equal(await page.locator('.pdf-excluded-row').count(),0);
 await page.getByRole('button',{name:'补充漏掉的行',exact:true}).click();
 await page.getByRole('textbox',{name:'补行第 1 列',exact:true}).fill('Missing');await page.getByRole('textbox',{name:'补行第 2 列',exact:true}).fill('-2.50');
 await page.getByRole('textbox',{name:'行修改的原件依据'}).fill('对照原件第1页最后一行，漏识别Missing -2.50');await page.getByRole('button',{name:'保存补充行',exact:true}).click();
 assert.equal(await page.locator('.pdf-inserted-row').count(),1);
 await page.getByRole('button',{name:'下一步：设置列名和表头',exact:true}).click();
 await page.locator('[data-column-name="0"]').fill('项目');await page.locator('[data-column-name="1"]').fill('金额');
 await page.locator('[data-column-type="1"]').selectOption('decimal');
 await page.locator('[data-header-fragment][data-header-row="0"]').evaluate(e=>e.checked=true);
 await page.locator('[data-header-reason-row="0"]').evaluate(e=>e.closest('details').open=true);
 await page.locator('[data-header-reason-row="0"]').fill('原页第一行为列名');
 await page.getByRole('button',{name:'保存列名和表头',exact:true}).click();
 await page.getByRole('button',{name:'验证并预览',exact:true}).click();await page.waitForFunction(()=>document.querySelector('#pdfPreview')?.textContent.includes('-1.20'));
 assert.equal(await page.locator('#pdfPublish').isDisabled(),true);await page.locator('#pdfPublishReviewed').check();assert.equal(await page.locator('#pdfPublish').isDisabled(),false);
 await page.locator('#pdfPublish').click();await page.waitForFunction(()=>document.querySelector('#pdfReviewStatus')?.textContent.includes('已发布表格'));
 assert.equal(publishes,1);await page.waitForFunction(()=>document.querySelector('#pdfAutomationSlot strong')?.textContent==='已入库 1 张');
 assert.equal(await page.getByRole('button',{name:'查看疑点',exact:true}).count(),0,'resolved review no longer appears as pending');
 const table=store('tables').tables[0],data=store('query',{id:table.table_id});assert.match(JSON.stringify(data),/-1.2/);
 assert.deepEqual(store('draft',{id:original.draft_id}).bundle,original.bundle);
 assert.equal(table.extraction_verified,false);assert.equal(table.coverage.cell_correction_count,1);assert.equal(table.coverage.manual_inserted_row_count,1);assert.match(JSON.stringify(data),/manual_transcription/);
 assert.deepEqual(errors,[]);
 if(process.env.PDF_CORRECTION_EVIDENCE){fs.mkdirSync(process.env.PDF_CORRECTION_EVIDENCE,{recursive:true});await page.screenshot({path:path.join(process.env.PDF_CORRECTION_EVIDENCE,'correction-published.png'),fullPage:true});fs.writeFileSync(path.join(process.env.PDF_CORRECTION_EVIDENCE,'result.json'),JSON.stringify({table,data,publishes,originalUnchanged:true,errors},null,2));}
 console.log('PASS: Chinese issue opens and focuses, direct correction with mandatory evidence, field edits preserve correction, real store preview/publish/query, original unchanged, resolved issue removed, truthful visible parser options.');
 }finally{await browser.close();server.close();fs.rmSync(dir,{recursive:true,force:true});}
})().catch(e=>{console.error(e);server.close();process.exitCode=1});
