/* Manifest publication, scan readiness, stale summaries and drafts: offline browser fixtures only. */
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const {chromium}=require('playwright');
const source=path.join(__dirname,'..','src','structured_data');
const block={date_mode:'date',date_column:'A',header_rows:[1],data_start_row:2,data_end_row:3,columns:[{column:'B',metric:'合成数值',unit:'单位',header_cells:['B1']}]};
const imported={name:'合成目标表',rows:3,columns:3,status:'auto_ready',layout:'single_header',fingerprint:'target-layout',reasons:[],mapping:{sheet:'合成目标表',fingerprint:'target-layout',blocks:[block]}};
const pending={...imported,name:'合成待核对表',status:'needs_review',mapping:{...imported.mapping,sheet:'合成待核对表'},published_in_dataset:false,published_dataset_id:null,published_mapping:null,published_fingerprint:null};
(async()=>{
 const browser=await chromium.launch({headless:true,...(process.env.SHEET_REVIEW_BROWSER?{executablePath:process.env.SHEET_REVIEW_BROWSER}:{})});
 try{
  const page=await browser.newPage({viewport:{width:1500,height:1000}}),errors=[],requests=[];
  let status='auto_ready',published=true,summaryPublished=false,publishedMapping=imported.mapping,mapping=imported.mapping;
  const current=()=>({...imported,status,mapping,published_in_dataset:published,published_dataset_id:published?'published-fixture':null,published_mapping:published?publishedMapping:null,published_fingerprint:status==='trusted_existing'?null:publishedMapping?.fingerprint??null});
  page.on('pageerror',e=>errors.push(e.message));
  await page.route('**/*',async route=>{
   const request=route.request(),u=new URL(request.url());requests.push({method:request.method(),path:u.pathname});let data={},type='application/json';
   if(u.pathname==='/'){data=fs.readFileSync(path.join(source,'index.html'),'utf8');type='text/html';}
   else if(['/sheet-review.js','/sheet-review.css','/workspace.css'].includes(u.pathname)){data=fs.readFileSync(path.join(source,u.pathname.slice(1)),'utf8');type=u.pathname.endsWith('.js')?'application/javascript':'text/css';}
   else if(u.pathname==='/api/ui-config')data={title:'表格分析',workspace_label:'合成验收',weknora_url:'http://fixture.test'};
   else if(u.pathname==='/api/datasets')data={datasets:[]};
   else if(u.pathname==='/api/imports')data={imports:[{import_id:'a'.repeat(24),source_file:'synthetic.xlsx'}]};
   else if(u.pathname.endsWith('/sheet'))data=u.searchParams.get('name')===imported.name?current():pending;
   else if(u.pathname.endsWith('/sheet-view'))data={sheet_name:u.searchParams.get('name'),total_rows:3,total_cols:3,range:{start_row:1,end_row:3,start_col:1,end_col:3,row_count:3,col_count:3},cells:[{row:1,column:1,cell:'A1',value:'日期',display:'日期'},{row:1,column:2,cell:'B1',value:'合成数值',display:'合成数值'},{row:2,column:1,cell:'A2',value:'2023-01-01',display:'2023-01-01'},{row:2,column:2,cell:'B2',value:100,display:'100'}],styles:{},merges:[],column_widths:{},row_heights:{},warnings:[]};
   else if(u.pathname.startsWith('/api/imports/'))data={import_id:'a'.repeat(24),source_file:'synthetic.xlsx',dataset_id:'different-scan-fixture',sheets:[pending,{...imported,status:'needs_review',published_in_dataset:summaryPublished,published_dataset_id:summaryPublished?'published-fixture':null,published_fingerprint:null}]};
   await route.fulfill({contentType:type,body:typeof data==='string'?data:JSON.stringify(data)});
  });
  const openTarget=async()=>{await page.locator('#allSheets').click();await page.locator('#sheetMenuList [data-sheet-index="1"]').click();await page.waitForFunction(()=>document.querySelector('.source-head h3')?.textContent==='合成目标表');};
  const reload=async()=>{await page.reload();await page.locator('.sheet-grid td').first().waitFor();};
  const reset=async()=>{await page.evaluate(()=>localStorage.clear());await reload();};
  await page.goto('http://fixture.test/');await page.locator('.sheet-grid td').first().waitFor();
  assert.match(await page.locator('#importSummary').innerText(),/0 张已可查询/);
  await openTarget();
  await page.waitForFunction(()=>document.querySelector('#ackBlock')?.disabled);
  assert.match(await page.locator('#importSummary').innerText(),/1 张已可查询/,'Detailed true publication replaces a stale false summary');
  await page.locator('[data-sheet-filter="pending"]').click();
  assert.equal(await page.locator('.sheet-tab[aria-pressed="true"] .sheet-state.ready').count(),1);
  assert.equal(await page.locator('.sheet-tab .sheet-state.pending').count(),1);
  assert.match(await page.locator('.mapping-block.selected .card-state').innerText(),/已可查询/);
  assert.equal(await page.locator('#confirmMapping').isDisabled(),true);
  const unit=page.locator('.mapping-block.selected [data-column-field="unit"]');
  await unit.fill('另一单位');
  assert.equal(await page.locator('#ackBlock').isChecked(),false);
  assert.equal(await page.locator('#ackBlock').isEnabled(),true);
  await reload();await openTarget();
  assert.equal(await unit.inputValue(),'另一单位');
  assert.equal(await page.locator('#ackBlock').isChecked(),false,'An edited restored draft cannot inherit publication');
  await page.locator('#ackBlock').check();assert.equal(await page.locator('#confirmMapping').isEnabled(),true);
  for(const value of ['confirmed','template_ready','auto_ready','trusted_existing','needs_review']){
   status=value;published=true;publishedMapping=imported.mapping;mapping=imported.mapping;await reset();await openTarget();
   assert.equal(await page.locator('.source-head .badge').innerText(),'已可查询');
   assert.equal(await page.locator('#ackBlock').isDisabled(),true);
   assert.equal(await page.locator('#ackBlock').isChecked(),true);
   assert.equal(await page.locator('#previewBlockSample').innerText(),'查看记录');
  }
  for(const value of ['confirmed','template_ready','auto_ready','trusted_existing']){
   status=value;published=false;summaryPublished=true;await reset();
   assert.match(await page.locator('#importSummary').innerText(),/1 张已可查询/);
   await openTarget();
   assert.match(await page.locator('#importSummary').innerText(),/0 张已可查询/,'Detailed false publication clears a stale true summary');
   assert.match(await page.locator('.source-head .badge').innerText(),/待导入/);
   assert.equal(await page.locator('#ackBlock').isChecked(),false,'Scan readiness never acknowledges a block');
   assert.equal(await page.locator('#ackBlock').isEnabled(),true);
   assert.equal(await page.locator('#confirmMapping').isDisabled(),true);
   assert.equal(await page.locator('#storedResults').count(),0);
   assert.equal(await page.locator('#previewBlockSample').innerText(),'预览几条记录');
   await page.locator('.sheet-processing-details>summary').click();
   assert.match(await page.locator('.sheet-processing-details').innerText(),/尚未发布到查询数据集/);
   await page.locator('#ackBlock').check();assert.equal(await page.locator('#confirmMapping').isEnabled(),true,'Explicit review can proceed to confirmation');
  }
  status='auto_ready';published=undefined;summaryPublished=false;await reset();await openTarget();
  assert.equal(await page.locator('#ackBlock').isChecked(),false,'Missing publication metadata must not fall back to scan status');
  published=true;publishedMapping=null;await reset();await openTarget();
  assert.equal(await page.locator('.source-head .badge').innerText(),'已可查询');
  assert.equal(await page.locator('#ackBlock').isChecked(),false,'A real sheet without published mapping cannot mark scan blocks published');
  mapping={...imported.mapping,blocks:[{...block,data_end_row:null}]};publishedMapping={...imported.mapping,blocks:[{...block,columns:block.columns.map(column=>({...column,dimensions:{}}))}]};
  await reset();await openTarget();
  assert.equal(await page.locator('.mapping-block.selected [data-field="data_end_row"]').inputValue(),'3','A fresh editor uses the actual published range rather than the scanner suggestion');
  assert.equal(await page.locator('#ackBlock').isDisabled(),true);
  const next={...block,columns:[{column:'C',metric:'合成第二值',unit:'单位',header_cells:['C1']}]};
  publishedMapping=null;mapping={...imported.mapping,blocks:[block,next]};await reset();await openTarget();
  await page.locator('.sheet-tab[data-sheet-index="0"]').click();
  publishedMapping=imported.mapping;await openTarget();
  assert.equal(await page.locator('.card-state').filter({hasText:'已可查询'}).count(),1,'Only manifest blocks are published even when scan mapping has more blocks');
  await page.locator('[data-select-block]').nth(1).click();
  assert.equal(await page.locator('#ackBlock').isChecked(),false);
  await page.locator('#excludeCurrentBlock').click();
  assert.equal(await page.locator('#confirmMapping').isDisabled(),true,'An unchanged published subset is not published again');
  await page.locator('#excludeCurrentBlock').click();await page.locator('#ackBlock').check();
  assert.equal(await page.locator('#confirmMapping').isEnabled(),true);
  // Simulate a draft saved by the former status-based UI, then remove publication.
  mapping=imported.mapping;publishedMapping=imported.mapping;await reset();await openTarget();
  await page.locator('.sheet-tab[data-sheet-index="0"]').click();
  await page.evaluate(()=>{for(const key of Object.keys(localStorage)){try{const d=JSON.parse(localStorage.getItem(key));if(d?.mapping?.sheet==='合成目标表'){for(const state of Object.values(d.states)){state.published=true;state.checked=true;}localStorage.setItem(key,JSON.stringify(d));}}catch{}}});
  published=false;await reload();await openTarget();
  assert.equal(await page.locator('#ackBlock').isChecked(),false,'Stale published draft acknowledgements do not survive manifest removal');
  published=true;await reset();await openTarget();
  await page.locator('.mapping-block.selected [data-range-input]').fill('B2:B2');
  assert.equal(await page.locator('#ackBlock').isChecked(),false);
  await reload();await openTarget();
  assert.equal(await page.locator('.mapping-block.selected [data-range-input]').inputValue(),'B2:B3');
  assert.equal(await page.locator('#ackBlock').isDisabled(),true,'Unapplied ranges do not change the published mapping');
  assert.deepEqual(errors,[]);
  assert.equal(requests.filter(request=>request.method!=='GET').length,0,'Reading states and editing local drafts never publishes or calls a model');
  console.log('PASS: authoritative publication across scan statuses, true/false summary refresh, manifest block matching, no scan fallback, partial publication, reviewed draft invalidation, no mutations.');
 }finally{await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
