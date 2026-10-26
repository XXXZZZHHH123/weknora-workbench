/* Real bundled PDF rendering and automatic-acceptance UI, isolated API fixtures. */
const fs=require('node:fs'),path=require('node:path'),http=require('node:http'),assert=require('node:assert/strict');
const {chromium}=require('playwright');
const dist=process.env.PDF_PREVIEW_DIST;
if(!dist||!fs.existsSync(path.join(dist,'pdf-table-preview.html')))throw Error('Build the frontend and set PDF_PREVIEW_DIST');
const source=path.join(__dirname,'../src/structured_data');
function pdf(){
 const objects=['<< /Type /Catalog /Pages 2 0 R >>','<< /Type /Pages /Kids [3 0 R 5 0 R 7 0 R] /Count 3 >>'];
 for(let i=0;i<3;i++){
  const stream=`BT /F1 24 Tf 40 700 Td (Original physical page ${i+1}) Tj ET`;
  objects.push(`<< /Type /Page /Parent 2 0 R /MediaBox [0 0 600 800] /Resources << /Font << /F1 9 0 R >> >> /Contents ${4+i*2} 0 R >>`,`<< /Length ${stream.length} >>\nstream\n${stream}\nendstream`);
 }
 objects.push('<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>');
 let out='%PDF-1.4\n',offsets=[0];objects.forEach((o,i)=>{offsets.push(out.length);out+=`${i+1} 0 obj\n${o}\nendobj\n`});
 const offset=out.length;out+=`xref\n0 ${objects.length+1}\n0000000000 65535 f \n`;for(const n of offsets.slice(1))out+=`${String(n).padStart(10,'0')} 00000 n \n`;
 return Buffer.from(out+`trailer << /Size ${objects.length+1} /Root 1 0 R >>\nstartxref\n${offset}\n%%EOF`);
}
const raw=pdf();
const draft={draft_id:'pdfd_fixture',bundle_sha256:'a'.repeat(64),bundle:{source:{file_name:'Preview fixture.pdf',sha256:'b'.repeat(64)},parser:{engine:'fixture'},fragments:[{id:'first',page:1,rows:[[{text:'Metric'},{text:'Value'}],[{text:'A'},{text:'-1.20'}]]},{id:'second',page:3,rows:[[{text:'Label'},{text:'Amount'}],[{text:'B'},{text:'2.30'}]]}]}};
let automation=null,automaticCalls=0;
const calls=[];
const tables=()=>automation?Array.from({length:automation.accepted_count},(_,i)=>({table_id:'pdft_'+i,draft_id:draft.draft_id,source:draft.bundle.source,status:'auto_accepted',columns:[],coverage:{logical_row_count:1}})):[];
const server=http.createServer(async(req,res)=>{
 const pathname=new URL(req.url,'http://localhost').pathname;
 let body,type='text/html';
 if(pathname==='/'){
  body=`<!doctype html><meta charset="utf-8"><style>html,body{margin:0;font:14px system-ui}body{padding:20px}button{cursor:pointer}#pdfReviewPanel{padding:20px}</style><style>${fs.readFileSync(path.join(source,'workspace.css'))}</style><body class="knowledge-view view-import document-results"><section id="pdfReviewPanel"><div id="pdfReviewApp"></div></section><script>let epoch=0;window.pdfReviewHost={getAuthEpoch:()=>epoch,isWorkspaceReady:()=>true,api:async(p,o={})=>{const r=await fetch(p,o);if(!r.ok)throw Error('API '+r.status);return r.json()},authenticatedFetch:(...args)=>fetch(...args)};</script><script src="/pdf-review.js"></script>`;
 }else if(pathname==='/pdf-review.js'){body=fs.readFileSync(path.join(source,'static/pdf-review.js'));type='text/javascript';}
 else if(pathname.endsWith('/source')){body=raw;type='application/pdf';}
 else if(pathname.startsWith('/api/')){
  type='application/json';calls.push({path:pathname,method:req.method});
  if(pathname.endsWith('/config'))body={configured:true,max_bytes:52428800};
  else if(pathname==='/api/pdf/drafts')body={drafts:[{draft_id:draft.draft_id,source:draft.bundle.source,fragment_count:2,automation}]};
  else if(pathname==='/api/pdf/tables')body={tables:tables()};
  else if(pathname==='/api/pdf/jobs')body={jobs:[]};
  else if(pathname.endsWith('/auto-process')){
   automaticCalls++;automation={status:automaticCalls===1?'needs_attention':'completed',accepted_count:automaticCalls===1?1:2,attention_count:automaticCalls===1?1:0,groups:[{fragment_ids:['first'],status:'accepted'},{fragment_ids:['second'],status:automaticCalls===1?'needs_attention':'accepted',message:'uncertain value'}]};body={id:'auto_fixture'};
  }else if(pathname==='/api/pdf/jobs/auto_fixture')body={status:'completed',draft_id:draft.draft_id,automatic_result:automation,stage:automation.attention_count?'已入库 1 张，1 张需要处理':'已自动入库 2 张'};
  else if(pathname==='/api/pdf/drafts/'+draft.draft_id)body={...draft,automation};
  else{res.statusCode=404;body={detail:'not found'};}
  body=JSON.stringify(body);
 }else{
  const file=path.resolve(dist,'.'+pathname);
  if(!file.startsWith(path.resolve(dist)+path.sep)||!fs.existsSync(file)){res.statusCode=404;body='not found';}
  else{body=fs.readFileSync(file);type=file.endsWith('.js')?'text/javascript':file.endsWith('.css')?'text/css':file.endsWith('.mjs')?'text/javascript':'text/html';}
 }
 res.setHeader('Content-Type',type);res.end(body);
});
(async()=>{
 await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
 const browser=await chromium.launch({executablePath:process.env.SHEET_REVIEW_BROWSER,headless:true,args:['--no-sandbox','--disable-dev-shm-usage']});
 try{
  const page=await browser.newPage({viewport:{width:1500,height:1000}}),errors=[];page.on('pageerror',e=>{errors.push(e.message);console.error('pageerror:',e.message)});page.on('console',m=>{if(m.type()==='error')console.error('console:',m.text())});
  await page.goto(`http://127.0.0.1:${server.address().port}`);
  await page.getByRole('button',{name:'查看表格',exact:true}).click();
  await page.waitForFunction(()=>document.querySelector('.pdf-source-viewer')?.dataset.renderedPage==='1',null,{timeout:15000}).catch(async e=>{console.error(await page.locator('#pdfReviewStatus').innerText());console.error(await page.frameLocator('.pdf-source-viewer').locator('body').innerText());throw e});
  const viewer=page.frameLocator('.pdf-source-viewer');
  assert.equal(await viewer.locator('canvas').count(),1);
  assert.equal(await viewer.locator('#paper').getAttribute('data-page'),'1');
  await page.locator('#pdfFragmentSelect').selectOption('1');
  await page.waitForFunction(()=>document.querySelector('.pdf-source-viewer')?.dataset.renderedPage==='3');
  assert.equal(await viewer.locator('#paper').getAttribute('data-page'),'3');
  for(const value of ['0','1','0'])await page.locator('#pdfFragmentSelect').selectOption(value);
  await page.waitForFunction(()=>document.querySelector('.pdf-source-viewer')?.dataset.renderedPage==='1');
  assert.equal(await viewer.locator('#paper').getAttribute('data-page'),'1');
  assert(await viewer.locator('canvas').evaluate(canvas=>{const d=canvas.getContext('2d').getImageData(0,0,canvas.width,canvas.height).data;return d.some((v,i)=>i%4!==3&&v<100)}));
  const ratio=await page.locator('.pdf-original-pane').evaluate(el=>el.clientWidth/el.parentElement.clientWidth);assert(ratio<.4);
  await page.getByRole('button',{name:'收起原件',exact:true}).click();assert.equal(await page.locator('.pdf-original-pane').isVisible(),false);
  await page.getByRole('button',{name:'显示原件',exact:true}).click();
  assert.equal(calls.filter(c=>c.path.endsWith('/auto-process')).length,0);
  await page.locator('#pdfAutoProcess').click();
  await page.locator('.pdf-exception').waitFor();
  assert.match(await page.locator('.pdf-automation').innerText(),/已入库 1 张.*1 张需要处理/);
  await page.getByRole('button',{name:'查看疑点',exact:true}).click();
  assert.equal(await page.locator('#pdfFragmentSelect').inputValue(),'1');
  await page.locator('#pdfAutoProcess').click();
  await page.locator('#pdfInventory').waitFor({state:'visible'});
  assert.match(await page.locator('#pdfDraftList').innerText(),/处理完成/);
  assert.match(await page.locator('#pdfDraftList').innerText(),/2 张表格/);
  assert.equal(calls.filter(c=>c.path.endsWith('/publish')).length,0);
  assert.deepEqual(errors,[]);
  if(process.env.PDF_PREVIEW_EVIDENCE){fs.mkdirSync(process.env.PDF_PREVIEW_EVIDENCE,{recursive:true});await page.screenshot({path:path.join(process.env.PDF_PREVIEW_EVIDENCE,'automatic-complete.png')});}
  console.log(JSON.stringify({realPdfCanvas:true,pages:[1,3,1],rapidNavigation:true,previewWidthRatio:ratio,collapsibleOriginal:true,partialSuccess:true,retry:true,noManualConfirmation:true,pageErrors:errors}));
 }finally{await browser.close();server.close();}
})().catch(e=>{console.error(e);server.close();process.exitCode=1});
