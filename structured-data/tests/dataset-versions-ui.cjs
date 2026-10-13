/* Fixture-only browser checks for current versions, history and sample isolation. */
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {chromium} = require('playwright');
const source = path.join(__dirname, '..', 'src', 'structured_data');
const current = {dataset_id:'a'.repeat(24),source_file:'棉花业务.xlsx',dataset_kind:'business',is_latest_for_source:true,imported_at:'2026-10-13T01:00:00Z',sheets:[{name:'库存'}]};
const older = {...current,dataset_id:'b'.repeat(24),is_latest_for_source:false,imported_at:'2026-10-12T01:00:00Z',import_event:{summary:'新增可查询工作表：库存'}};
const sample = {...current,dataset_id:'c'.repeat(24),source_file:'中哲_范围核对验收_非行情.xlsx',dataset_kind:'sample'};
(async()=>{
 const browser=await chromium.launch({headless:true,...(process.env.SHEET_REVIEW_BROWSER?{executablePath:process.env.SHEET_REVIEW_BROWSER}:{})});
 try{
  const page=await browser.newPage({viewport:{width:1500,height:1000}}),errors=[];
  page.on('pageerror',e=>errors.push(e.message));
  await page.route('**/*',async route=>{
   const url=new URL(route.request().url());let data={},type='application/json';
   if(url.pathname==='/api/ui-config')data={title:'表格分析',workspace_label:'中哲物产',weknora_url:'http://fixture.test'};
   else if(url.pathname==='/api/datasets')data={datasets:[current,older],samples:[sample]};
   else if(url.pathname==='/api/imports')data={imports:[],samples:[{import_id:'d'.repeat(24),source_file:sample.source_file}]};
   else if(url.pathname.endsWith('/report')){const item=[current,older,sample].find(d=>url.pathname.includes(d.dataset_id));data={manifest:{...item,series_count:1,valid_count:1,observation_count:1,issue_count:0,sha256:'real-source-fingerprint',excluded_regions:[]},issues:[]};}
   else if(url.pathname.endsWith('/series'))data={series:[],total:0};
   else if(url.pathname.endsWith('/charts'))data={charts:[],sheets:[],total:0,next_offset:null};
   else if(url.pathname==='/') {data=fs.readFileSync(path.join(source,'index.html'),'utf8');type='text/html';}
   else if(['/sheet-review.js','/sheet-review.css'].includes(url.pathname)){data=fs.readFileSync(path.join(source,url.pathname.slice(1)),'utf8');type=url.pathname.endsWith('.js')?'application/javascript':'text/css';}
   await route.fulfill({contentType:type,body:typeof data==='string'?data:JSON.stringify(data)});
  });
  await page.goto('http://fixture.test/');await page.locator('#stats b').first().waitFor();
  assert.equal(await page.locator('#dataset option').count(),1);
  assert.equal(await page.locator('#dataset').inputValue(),current.dataset_id);
  assert.equal(await page.locator('#datasetHistory').evaluate(el=>el.open),false);
  assert.equal(await page.locator('#sampleDatasets').isHidden(),true);
  assert.equal(await page.locator('#sampleImports').isHidden(),true);
  assert.equal(await page.locator('#importSelect option').count(),0);
  await page.locator('#historySummary').click();await page.locator('#historyList button').click();await page.waitForFunction(id=>document.querySelector('#dataset').value===id,older.dataset_id);
  assert.equal(await page.locator('#dataset').inputValue(),older.dataset_id);
  assert.match(await page.locator('#versionNotice').innerText(),/历史导入结果/);
  await page.waitForFunction(id=>document.querySelector('#scope [data-download]')?.dataset.download===id,older.dataset_id);
  assert.equal(await page.locator('#scope [data-download]').getAttribute('data-download'),older.dataset_id);
  assert.match(await page.locator('#historyList').innerText(),/新增可查询工作表：库存/);
  assert.equal((await page.locator('#historyList').innerText()).includes(older.dataset_id.slice(0,8)),false);
  await page.goto('http://fixture.test/?samples=1');await page.locator('#stats b').first().waitFor();
  await page.locator('#samplesSummary').click();await page.locator('#sampleList button').click();await page.waitForFunction(id=>document.querySelector('#dataset').value===id,sample.dataset_id);
  assert.equal(await page.locator('#dataset').inputValue(),sample.dataset_id);
  assert.match(await page.locator('#versionNotice').innerText(),/验收样例/);
  await page.waitForFunction(id=>document.querySelector('#scope [data-download]')?.dataset.download===id,sample.dataset_id);
  assert.equal(await page.locator('#scope [data-download]').getAttribute('data-download'),sample.dataset_id);
  await page.reload();await page.locator('#stats b').first().waitFor();
  assert.equal(await page.locator('#dataset option').count(),1);
  assert.equal(await page.locator('#dataset').inputValue(),current.dataset_id);
  assert.deepEqual(errors,[]);
  console.log('Dataset versions UI passed: current default, folded history, separate samples, explicit source links.');
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
