/* Strict auto-ready confirmation: intercepted, synthetic browser fixtures only. */
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const {chromium}=require('playwright');
const source=path.join(__dirname,'..','src','structured_data');
const importId='a'.repeat(24);
const block={date_mode:'date',date_column:'A',header_rows:[1],data_start_row:2,data_end_row:3,columns:[{column:'B',metric:'合成字段',unit:'件',header_cells:['B1']}]};
function sheet(name,status,fieldCount,published=false){
 const mapping={sheet:name,fingerprint:'fixture-'+name,blocks:[{...structuredClone(block),columns:Array.from({length:fieldCount},(_,index)=>({...structuredClone(block.columns[0]),column:String.fromCharCode(66+index),header_cells:[String.fromCharCode(66+index)+'1']}))}]};
 return {name,status,field_count:fieldCount,rows:3,columns:3,layout:'single_header',fingerprint:mapping.fingerprint,mapping,reasons:[],import_state:published?'imported':'pending',published_in_dataset:published,published_dataset_id:published?'fixture-snapshot':null,published_mapping:published?mapping:null,published_fingerprint:published?mapping.fingerprint:null};
}
const sheets=[sheet('合成自动表甲','auto_ready',2,true),sheet('合成自动表乙','auto_ready',1),sheet('合成待核对表','needs_review',1),sheet('合成模板表','template_ready',1),sheet('合成已确认表','confirmed',1,true)];
const reason='合成日期无法解析 <img src=x onerror="throw 1">';
(async()=>{
 const browser=await chromium.launch({headless:true,...(process.env.SHEET_REVIEW_BROWSER?{executablePath:process.env.SHEET_REVIEW_BROWSER}:{})});
 try{
  const page=await browser.newPage({viewport:{width:1600,height:1100}}),errors=[],requests=[];
  let batches=0,release=false,job=null;
  page.on('pageerror',error=>errors.push(error.message));
  await page.route('**/*',async route=>{
   const request=route.request(),url=new URL(request.url()),payload=request.postDataJSON();
   requests.push({method:request.method(),path:url.pathname,payload});
   let data={},type='application/json';
   if(url.pathname==='/'){data=fs.readFileSync(path.join(source,'index.html'),'utf8');type='text/html';}
   else if(['/pdf-query.js','/sheet-review.js','/sheet-review.css','/workspace.css'].includes(url.pathname)){data=fs.readFileSync(path.join(source,url.pathname.slice(1)),'utf8');type=url.pathname.endsWith('.js')?'application/javascript':'text/css';}
   else if(url.pathname==='/api/ui-config')data={title:'合成核对',workspace_label:'Fixture',weknora_url:'http://fixture.test'};
   else if(url.pathname==='/api/datasets')data={datasets:[]};
   else if(url.pathname==='/api/imports')data={imports:[{import_id:importId,source_file:'synthetic.xlsx'}]};
   else if(url.pathname.endsWith('/sheet'))data=sheets.find(item=>item.name===url.searchParams.get('name'));
   else if(url.pathname.endsWith('/sheet-view'))data={sheet_name:url.searchParams.get('name'),total_rows:3,total_cols:3,range:{start_row:1,end_row:3,start_col:1,end_col:3,row_count:3,col_count:3},cells:[{row:1,column:1,cell:'A1',value:'日期',display:'日期'},{row:1,column:2,cell:'B1',value:'合成字段',display:'合成字段'},{row:2,column:1,cell:'A2',value:'2026-01-01',display:'2026-01-01'},{row:2,column:2,cell:'B2',value:10,display:'10'}],styles:{},merges:[],column_widths:{},row_heights:{},warnings:[]};
   else if(url.pathname.endsWith('/confirm-auto-ready')){
    batches++;release=false;job={id:'auto-ready-'+batches,names:payload.sheets};
    assert.deepEqual(Object.keys(payload).sort(),['save_template','sheets']);
    data={id:job.id,status:'processing'};
   }else if(job&&url.pathname==='/api/jobs/'+job.id){
    if(!release)data={id:job.id,status:'processing',stage:'正在逐表检查'};
    else{
     const success=batches===1?[sheets[0].name]:job.names;
     for(const name of success){const target=sheets.find(item=>item.name===name);target.status='confirmed';target.import_state='imported';target.published_in_dataset=true;target.published_dataset_id='fixture-snapshot';target.published_mapping=target.mapping;}
     data={id:job.id,status:'completed',result:{import_id:importId,sheets_imported:success,sheets_failed:batches===1?[{sheet:sheets[1].name,message:reason}]:[],sheets_pending:[sheets[2].name]}};
    }
   }else if(url.pathname==='/api/imports/'+importId)data={import_id:importId,source_file:'synthetic.xlsx',sheets};
   await route.fulfill({contentType:type,body:typeof data==='string'?data:JSON.stringify(data)});
  });
  const posts=()=>requests.filter(item=>item.method==='POST');
  const open=async()=>{await page.locator('#confirmAutoReadySheets').click();await page.waitForFunction(()=>document.getElementById('confirmDialog').open);};
  await page.goto('http://fixture.test/');await page.locator('.sheet-grid td').first().waitFor();
  assert.equal(posts().length,0,'Opening the review must stay read only');
  assert.match(await page.locator('#confirmAutoReadySheets').innerText(),/\(2\)/,'Already published auto_ready sheets still require confirmation');
  await open();
  assert.equal(await page.locator('#confirmDialogTitle').innerText(),'确认全部已检查通过的工作表');
  const preview=await page.locator('#confirmList').innerText();
  assert.match(preview,/合成自动表甲 · 2 个字段/);assert.match(preview,/合成自动表乙 · 1 个字段/);
  for(const omitted of sheets.slice(2))assert(!preview.includes(omitted.name),'Only auto_ready sheets may appear in the confirmation preview');
  assert.equal(posts().length,0,'Opening the preview does not publish');
  await page.locator('#confirmBack').click();assert.equal(posts().length,0,'Cancelling the preview does not publish');
  await open();await Promise.all([page.waitForResponse(response=>response.url().endsWith('/confirm-auto-ready')),page.locator('#confirmGo').click()]);
  await page.waitForFunction(()=>document.getElementById('confirmAutoReadySheets').disabled);
  await page.waitForFunction(()=>!document.getElementById('confirmDialog').open);
  assert.deepEqual(posts().at(-1).payload.sheets,[sheets[0].name,sheets[1].name]);
  assert.equal(posts().length,1,'Explicit second confirmation creates one bounded batch');
  release=true;
  await page.locator('[data-sheet-confirm="failed"]').waitFor();
  assert.match(await page.locator('[data-sheet-confirm="success"]').innerText(),/合成自动表甲：已确认/);
  assert.equal(await page.locator('[data-sheet-confirm="failed"]').innerText(),sheets[1].name+'：'+reason);
  assert.equal(await page.locator('#mappingFeedback img').count(),0,'Failure text must be escaped');
  assert.match(await page.locator('#confirmAutoReadySheets').innerText(),/\(1\)/,'Successful sheets stay confirmed and leave the next selection');
  assert.equal(sheets[2].status,'needs_review');assert.equal(sheets[3].status,'template_ready');
  await open();
  const retryPreview=await page.locator('#confirmList').innerText();
  assert(!retryPreview.includes(sheets[0].name));assert(retryPreview.includes(sheets[1].name));
  await Promise.all([page.waitForResponse(response=>response.url().endsWith('/confirm-auto-ready')),page.locator('#confirmGo').click()]);
  await page.waitForFunction(()=>document.getElementById('confirmAutoReadySheets').disabled);
  release=true;
  await page.waitForFunction(()=>document.querySelector('#mappingFeedback')?.textContent.includes('已确认 1 张工作表；0 张未完成'));
  assert.deepEqual(posts().at(-1).payload.sheets,[sheets[1].name]);
  assert.equal(await page.locator('[data-sheet-confirm="failed"]').count(),0);
  assert.equal(await page.locator('#confirmAutoReadySheets').isDisabled(),true);
  assert(!posts().some(item=>/\/(confirm|preview|confirm-all|assist-confirm)$/.test(item.path)),'The new operation sends only the selected names to the strict batch endpoint');
  sheets[1].status='auto_ready';await page.reload();await page.locator('.sheet-grid td').first().waitFor();
  await open();await page.evaluate(()=>window.sheetReviewAuthChanged());
  assert.equal(await page.locator('#confirmDialog').evaluate(dialog=>dialog.open),false,'Changing account scope closes the stale preview');
  assert.equal(posts().length,2);
  assert.deepEqual(errors,[]);
  console.log('PASS: auto_ready-only preview with field counts, second confirmation, cancellation, single submission, per-sheet escaped failures, retained successes, bounded retry and account-scope invalidation.');
 }finally{await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
