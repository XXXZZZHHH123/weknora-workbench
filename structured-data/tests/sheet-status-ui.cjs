/* Exercise saved sheet state, stale summaries and edited drafts in a fixture browser. */
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const {chromium}=require('playwright');
const source=path.join(__dirname,'..','src','structured_data');
const imported={name:'已导入表',rows:3,columns:2,status:'auto_ready',layout:'single_header',fingerprint:'imported-layout',reasons:[],mapping:{sheet:'已导入表',fingerprint:'imported-layout',blocks:[{date_mode:'date',date_column:'A',header_rows:[1],data_start_row:2,data_end_row:3,columns:[{column:'B',metric:'价格',unit:'元',header_cells:['B1']}]}]}};
const pending={...imported,name:'待核对表',status:'needs_review',mapping:{...imported.mapping,sheet:'待核对表'}};
(async()=>{
 const browser=await chromium.launch({headless:true,...(process.env.SHEET_REVIEW_BROWSER?{executablePath:process.env.SHEET_REVIEW_BROWSER}:{})});
 try{
  const page=await browser.newPage({viewport:{width:1500,height:1000}}),errors=[];let status='auto_ready';
  page.on('pageerror',e=>errors.push(e.message));
  await page.route('**/*',async route=>{
   const u=new URL(route.request().url());let data={},type='application/json';
   if(u.pathname==='/') {data=fs.readFileSync(path.join(source,'index.html'),'utf8');type='text/html';}
   else if(['/sheet-review.js','/sheet-review.css'].includes(u.pathname)){data=fs.readFileSync(path.join(source,u.pathname.slice(1)),'utf8');type=u.pathname.endsWith('.js')?'application/javascript':'text/css';}
   else if(u.pathname==='/api/ui-config')data={title:'表格分析',workspace_label:'中哲物产',weknora_url:'http://fixture.test'};
   else if(u.pathname==='/api/datasets')data={datasets:[]};
   else if(u.pathname==='/api/imports')data={imports:[{import_id:'a'.repeat(24),source_file:'业务.xlsx'}]};
   else if(u.pathname.endsWith('/sheet'))data=u.searchParams.get('name')===imported.name?{...imported,status}:pending;
   else if(u.pathname.endsWith('/sheet-view'))data={sheet_name:u.searchParams.get('name'),total_rows:3,total_cols:2,range:{start_row:1,end_row:3,start_col:1,end_col:2,row_count:3,col_count:2},cells:[{row:1,column:1,cell:'A1',value:'日期',display:'日期'},{row:1,column:2,cell:'B1',value:'价格',display:'价格'},{row:2,column:1,cell:'A2',value:'2023-01-01',display:'2023-01-01'},{row:2,column:2,cell:'B2',value:100,display:'100'}],styles:{},merges:[],column_widths:{},row_heights:{},warnings:[]};
   else if(u.pathname.startsWith('/api/imports/'))data={import_id:'a'.repeat(24),source_file:'业务.xlsx',sheets:[pending,{...imported,status:'needs_review'}]}; // A stale summary must yield to the latest sheet response.
   await route.fulfill({contentType:type,body:typeof data==='string'?data:JSON.stringify(data)});
  });
  await page.goto('http://fixture.test/');await page.locator('.sheet-grid td').first().waitFor();
  assert.equal(await page.locator('.sheet-tab[aria-pressed="true"] .sheet-state.pending').count(),1);
  await page.locator('#allSheets').click();await page.locator('#sheetMenuList [data-sheet-index="1"]').click();
  await page.waitForFunction(()=>document.querySelector('.source-head h3')?.textContent==='已导入表'&&document.querySelector('#ackBlock')?.disabled);
  assert.equal(await page.locator('[data-sheet-filter="pending"]').getAttribute('aria-pressed'),'true');
  assert.equal(await page.locator('.sheet-tab[aria-pressed="true"] .sheet-state.ready').count(),1,'The selected imported sheet remains visible and green under the pending filter');
  assert.equal(await page.locator('.sheet-tab .sheet-state.pending').count(),1,'Other pending sheets remain yellow');
  assert.match(await page.locator('.mapping-block.selected .card-state').innerText(),/已导入/);
  assert.match(await page.locator('#confirmHint').innerText(),/本表已导入/);
  assert.match(await page.locator('#ackBlockLabel').innerText(),/已导入/);
  assert.equal(await page.locator('#confirmMapping').isDisabled(),true);
  const unit=page.locator('.mapping-block.selected [data-column-field="unit"]');
  await unit.fill('万元');
  assert.match(await page.locator('.mapping-block.selected .card-state').innerText(),/待核对/);
  assert.equal(await page.locator('#ackBlock').isChecked(),false);
  assert.equal(await page.locator('#ackBlock').isEnabled(),true);
  assert.equal(await page.locator('#confirmMapping').isDisabled(),true);
  await page.reload();await page.locator('.sheet-grid td').first().waitFor();
  await page.locator('#allSheets').click();await page.locator('#sheetMenuList [data-sheet-index="1"]').click();
  await page.waitForFunction(()=>document.querySelector('.source-head h3')?.textContent==='已导入表');
  assert.equal(await page.locator('.mapping-block.selected [data-column-field="unit"]').inputValue(),'万元');
  assert.equal(await page.locator('#ackBlock').isChecked(),false,'An edited restored draft cannot inherit the published acknowledgement');
  await page.locator('#ackBlock').check();assert.equal(await page.locator('#confirmMapping').isEnabled(),true);
  // A fresh browser session must reflect confirmed, automatic and template imports.
  for(const importedStatus of ['confirmed','template_ready','auto_ready']){
   status=importedStatus;await page.evaluate(()=>localStorage.clear());await page.reload();await page.locator('.sheet-grid td').first().waitFor();
   await page.locator('#allSheets').click();await page.locator('#sheetMenuList [data-sheet-index="1"]').click();
   await page.waitForFunction(()=>document.querySelector('.source-head h3')?.textContent==='已导入表'&&document.querySelector('#ackBlock')?.disabled);
   assert.equal(await page.locator('.sheet-tab[aria-pressed="true"] .sheet-state.ready').count(),1);
   assert.match(await page.locator('.mapping-block.selected .card-state').innerText(),/已导入/);
  }
  assert.deepEqual(errors,[]);
  console.log('PASS: imported tabs stay green, pending tabs stay yellow, fresh status replaces stale summary, saved blocks are imported, edited/restored drafts require review.');
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
