/* Shipped query UI + actual PDFTableStore SQL. Synthetic values never enter app code. */
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),http=require('node:http');
const {execFileSync}=require('node:child_process'),{chromium}=require('playwright');
const root=fs.mkdtempSync(path.join(os.tmpdir(),'pdf-query-')),source=path.resolve('structured-data/src/structured_data');
const python=`import sys,json\nfrom structured_data.pdf_tables import PDFTableStore\nfrom test_pdf_tables import bundle,setup\nr=json.load(sys.stdin);s=PDFTableStore(sys.argv[1]+'/scope')\nif r['op']=='init':\n s,d,p=setup(__import__('pathlib').Path(sys.argv[1]),bundle([['Item','Value']]+[[str(i),str(-i)] for i in range(1,122)]));p['title']='Synthetic measurements';s.publish(d['draft_id'],dict(p,reviewed=True));out=s.list_tables()\nelif r['op']=='read':out=s.read_table(r['id'])\nelse:out=s.query(r['id'],r['sql'],limit=100)\nprint(json.dumps(out))`;
const store=data=>JSON.parse(execFileSync('python',['-c',python,root],{input:JSON.stringify(data),encoding:'utf8',env:{...process.env,PYTHONPATH:'structured-data/src:structured-data/tests'}}));
const catalogue=store({op:'init'}),table=catalogue.tables[0],queries=[];
const server=http.createServer(async(req,res)=>{try{
 const u=new URL(req.url,'http://localhost'),file=u.pathname==='/'?'index.html':u.pathname.slice(1);let data={},type='application/json';
 if(['index.html','workspace.css','sheet-review.css','sheet-review.js','pdf-query.js'].includes(file)){data=fs.readFileSync(path.join(source,file));type=file.endsWith('.js')?'application/javascript':file.endsWith('.css')?'text/css':'text/html';}
 else if(file==='pdf-review.js'){data='';type='application/javascript';}
 else if(u.pathname==='/api/datasets')data={datasets:[]};
 else if(u.pathname==='/api/imports')data={imports:[]};
 else if(u.pathname==='/api/pdf/tables')data=catalogue;
 else if(u.pathname.endsWith('/query')){queries.push(u.searchParams.get('sql'));data=store({op:'query',id:table.table_id,sql:u.searchParams.get('sql')});}
 else if(u.pathname.startsWith('/api/pdf/tables/'))data=store({op:'read',id:table.table_id});
 else if(u.pathname==='/api/ui-config')data={title:'Synthetic workspace'};
 res.setHeader('Content-Type',type);res.end(Buffer.isBuffer(data)||typeof data==='string'?data:JSON.stringify(data));
 }catch(error){res.statusCode=400;res.end(JSON.stringify({detail:error.message}));}});
(async()=>{await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));const browser=await chromium.launch({executablePath:process.env.SHEET_REVIEW_BROWSER,headless:true,args:['--no-sandbox','--disable-dev-shm-usage']});
 try{const page=await browser.newPage({viewport:{width:1450,height:950}}),errors=[];page.on('pageerror',e=>errors.push(e.message));
 await page.goto('http://127.0.0.1:'+server.address().port);await page.locator('[data-pdf-table]').waitFor();
 assert.equal(await page.locator('#dataset option').count(),2);
 await page.locator('[data-pdf-table]').click();await page.waitForFunction(()=>document.querySelector('#pdfQueryStatus')?.textContent.includes('100 行'));
 assert.equal(await page.locator('#pdfQueryOutput tbody tr').count(),100);assert.equal(await page.locator('#pdfQueryNext').isDisabled(),false);
 await page.locator('#pdfQueryNext').click();await page.waitForFunction(()=>document.querySelector('#pdfQueryStatus')?.textContent.includes('21 行'));
 await page.locator('#pdfQueryOperation').selectOption('sum');await page.locator('#pdfQueryForm button[type=submit]').click();
 await page.waitForFunction(()=>document.querySelector('#pdfQueryOutput')?.textContent.includes('-7381'));
 assert.match(await page.locator('#pdfQueryStatus').innerText(),/121 行/);
 await page.locator('#pdfQueryOperation').selectOption('rows');await page.locator('#pdfFilterColumn').selectOption('col1');await page.locator('#pdfFilterOperator').selectOption('lt');await page.locator('#pdfFilterValue').fill('-120');await page.locator('#pdfQueryForm button[type=submit]').click();
 await page.waitForFunction(()=>document.querySelector('#pdfQueryStatus')?.textContent.includes('1 行'));
 assert.match(await page.locator('#pdfQueryOutput').innerText(),/-121/);assert.equal(await page.locator('[data-source-page="1"]').count(),1);
 await page.locator('#pdfQueryOutput details summary').click();assert.match(await page.locator('#pdfQueryOutput').innerText(),/第 122 行、第 2 列/);
 await page.locator('#dataset').selectOption('pdf:'+table.source.sha256);await page.waitForFunction(()=>document.querySelector('#pdfQueryStatus')?.textContent.includes('100 行'));
 assert.equal(await page.locator('#seriesSearchForm').isVisible(),false);
 await page.setViewportSize({width:620,height:700});assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
 await page.evaluate(()=>{authEpoch++;window.dispatchEvent(new Event('structured-auth-scope-change'));});assert.equal(await page.locator('#pdfQueryDetail').isVisible(),false);
 assert.deepEqual(errors,[]);console.log('PASS: PDF-only catalogue, original rows/pages, two-page navigation, whole-table sum, typed filters, source-cell row numbering, narrow screen and scope cleanup.');
 }finally{await browser.close();server.close();fs.rmSync(root,{recursive:true,force:true});}
})().catch(error=>{console.error(error);server.close();process.exitCode=1;});
