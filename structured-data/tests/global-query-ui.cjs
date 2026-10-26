/* Exercise the shipped scripts with isolated HTTP fixtures. No model calls. */
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const {chromium}=require('playwright');
const source=process.env.STRUCTURED_UI_SOURCE||path.join(__dirname,'..','src','structured_data');
const ids={a:'a'.repeat(24),b:'b'.repeat(24),old:'c'.repeat(24)};
const files=[{dataset_id:ids.a,source_file:'Alpha.xlsx',dataset_kind:'business',is_latest_for_source:true,sheets:[{name:'Data'}]},
 {dataset_id:ids.b,source_file:'Beta.xlsx',dataset_kind:'business',is_latest_for_source:true,sheets:[{name:'Data'}]},
 {dataset_id:ids.old,source_file:'Alpha.xlsx',dataset_kind:'business',is_latest_for_source:false,sheets:[{name:'Data'}]}];
const base={series_id:'same-id',sheet:'Data',metric:'Shared metric',unit:'u',dimensions:{},date_mode:'date',first_valid_date:'2026-01-01',last_valid_date:'2026-01-01'};
const rows=[{...base,dataset_id:ids.a,source_file:'Alpha.xlsx'}, {...base,dataset_id:ids.b,source_file:'Beta.xlsx'},
 ...Array.from({length:199},(_,i)=>({...base,series_id:'extra-'+i,metric:'Extra '+i,dataset_id:ids.b,source_file:'Beta.xlsx'}))];
const requests=[];
(async()=>{
 const browser=await chromium.launch({headless:true,...(process.env.SHEET_REVIEW_BROWSER?{executablePath:process.env.SHEET_REVIEW_BROWSER}:{})});
 try{
  const page=await browser.newPage({viewport:{width:1500,height:1000}}),errors=[];
  page.on('pageerror',error=>errors.push(error.message));
  await page.route('**/*',async route=>{
   const url=new URL(route.request().url()),p=url.searchParams;requests.push({path:url.pathname,params:Object.fromEntries(p)});
   let data={},type='application/json';
   if(url.pathname.startsWith('/fonts/')){await route.fulfill({status:200,contentType:'text/css',body:''});return;}
   if(url.pathname==='/api/ui-config')data={title:'Fixture',workspace_label:'Fixture',weknora_url:'http://fixture.test'};
   else if(url.pathname==='/pdf-review.js'){await route.fulfill({contentType:'application/javascript',body:''});return;}
   else if(url.pathname==='/api/pdf/tables')data={tables:[]};
   else if(url.pathname==='/api/datasets')data={datasets:files};
   else if(url.pathname==='/api/imports')data={imports:[]};
   else if(url.pathname.endsWith('/report')){const d=files.find(d=>url.pathname.includes(d.dataset_id));data={manifest:{...d,series_count:1,valid_count:1,observation_count:1,issue_count:0,excluded_regions:[]},issues:[]};}
   else if(url.pathname.endsWith('/series')){
    let found=rows.filter(row=>url.pathname==='/api/series'||url.pathname.includes(row.dataset_id));
    if(p.get('search'))found=found.filter(row=>row.source_file.includes(p.get('search'))||row.metric.includes(p.get('search')));
    const offset=Number(p.get('offset')||0),limit=Number(p.get('limit')||100);
    data={series:found.slice(offset,offset+limit),total:found.length,truncated:found.length>offset+limit,next_offset:found.length>offset+limit?offset+limit:null};
   }else if(url.pathname.endsWith('/charts')){
    data={charts:url.pathname==='/api/charts'?files.slice(0,2).map(d=>({dataset_id:d.dataset_id,source_file:d.source_file,title:'Shared plot',sheet:'Plot',status:'linked',anchor_row:1,axis_titles:[],curves:[{series_id:'chart_same-id',label:'Shared curve',value_ref:'Data!B2',category_ref:'Data!A2',status:'linked',reason:''}]})):[],sheets:['Plot'],total:2,next_offset:null};
   }else if(url.pathname.includes('/chart-series/'))data={...base,series_id:'chart_same-id'};
   else if(url.pathname==='/api/query'){
    const d=files.find(d=>d.dataset_id===p.get('dataset_id'));assert(d,'Query requires an exact real dataset ID');
    data={status:'ok',aggregate:null,valid_count:1,record_count:1,source:{file:d.source_file,sheet:'Data'},series:base,rows:[{date:'2026-01-01',value:d.dataset_id===ids.a?7:19,source_cell:'B2',date_cell:'A2',value_status:'valid'}],warnings:[]};
   }else if(url.pathname==='/'){data=fs.readFileSync(path.join(source,'index.html'),'utf8');type='text/html';}
   else if(['/pdf-query.js','/sheet-review.js','/sheet-review.css','/workspace.css'].includes(url.pathname)){data=fs.readFileSync(path.join(source,url.pathname.slice(1)),'utf8');type=url.pathname.endsWith('.js')?'application/javascript':'text/css';}
   else throw Error('Unexpected fixture route: '+url.pathname);
   await route.fulfill({contentType:type,body:typeof data==='string'?data:JSON.stringify(data)});
  });
  await page.goto('http://fixture.test/');await page.locator('#seriesRows button').first().waitFor();
  assert.equal(await page.locator('#dataset').inputValue(),'__all__');
  assert.equal(await page.locator('#dataset option').count(),3);
  assert.match(await page.locator('#seriesRows button').nth(0).innerText(),/Alpha.xlsx/);
  assert.match(await page.locator('#seriesRows button').nth(1).innerText(),/Beta.xlsx/);
  await page.locator('#seriesRows button').nth(1).click();await page.locator('#query').click();
  await page.waitForFunction(()=>document.querySelector('#answer')?.textContent.includes('Beta.xlsx'));
  assert.equal(requests.filter(r=>r.path==='/api/query').at(-1).params.dataset_id,ids.b);
  assert.equal(await page.locator('#seriesRows [aria-pressed="true"]').count(),1);
  assert.equal(await page.locator('#dataset').inputValue(),'__all__');
  await page.locator('#seriesRows button').nth(0).click();await page.locator('#query').click();
  await page.waitForFunction(()=>document.querySelector('#answer')?.textContent.includes('Alpha.xlsx'));
  assert.equal(requests.filter(r=>r.path==='/api/query').at(-1).params.dataset_id,ids.a);
  await page.locator('#nextSeries').click();await page.waitForFunction(()=>document.querySelector('#seriesCount').textContent.includes('101—200'));
  await page.locator('#nextSeries').click();await page.waitForFunction(()=>document.querySelector('#seriesCount').textContent.includes('201—201'));
  assert.equal(await page.locator('#seriesRows button').count(),1);
  assert.equal(await page.locator('#nextSeries').isDisabled(),true);
  await page.locator('#previousSeries').click();await page.waitForFunction(()=>document.querySelector('#seriesCount').textContent.includes('101—200'));
  await page.locator('#search').fill('Extra');await page.locator('#find').click();
  await page.waitForFunction(()=>document.querySelector('#seriesCount').textContent.includes('199 个指标'));
  assert(requests.some(r=>r.path==='/api/series'&&r.params.search==='Extra'&&r.params.offset==='0'));
  // Two chart curves share an ID, but preserve the second file's source.
  await page.locator('#chartList details').nth(1).locator('summary').click();
  await page.locator('#chartList [data-chart-series]').nth(1).click();await page.locator('#query').click();
  await page.waitForFunction(()=>document.querySelector('#answer')?.textContent.includes('Beta.xlsx'));
  assert.equal(requests.filter(r=>r.path==='/api/query').at(-1).params.dataset_id,ids.b);
  assert(requests.some(r=>r.path===`/api/datasets/${ids.b}/chart-series/chart_same-id`));
  await page.locator('#dataset').selectOption(ids.a);
  await page.waitForFunction(()=>document.querySelector('#scope [data-download]')?.dataset.download==='a'.repeat(24));
  assert(requests.some(r=>r.path===`/api/datasets/${ids.a}/series`));
  await page.locator('#dataset').selectOption('__all__');await page.waitForFunction(()=>document.querySelector('#scope [data-download="'+ 'b'.repeat(24)+'"]'));
  // A new import changes the catalogue while the selected scope stays all.
  await page.evaluate(()=>refresh());
  const before=requests.filter(r=>r.path==='/api/series').length;
  files.push({dataset_id:'d'.repeat(24),source_file:'Gamma.xlsx',dataset_kind:'business',is_latest_for_source:true,sheets:[{name:'Data'}]});
  rows.unshift({...base,dataset_id:'d'.repeat(24),source_file:'Gamma.xlsx',metric:'Extra newly imported'});
  await page.evaluate(()=>refresh());
  assert(requests.filter(r=>r.path==='/api/series').length>before);
  assert.equal(await page.locator('#dataset').inputValue(),'__all__');
  assert.match(await page.locator('#seriesRows button').first().innerText(),/Gamma.xlsx/);
  assert.deepEqual(errors,[]);
  console.log(JSON.stringify({passed:13,checks:['All files is default','Latest files only in picker','Global source labels','Duplicate series IDs keep exact file','Selection stays global','Independent selected state','Global pagination','Search resets global page','Chart curves keep exact file','Single file filter preserved','Return to all files','New import invalidates all-files catalogue','No JS errors or model calls']}));
 }finally{await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
