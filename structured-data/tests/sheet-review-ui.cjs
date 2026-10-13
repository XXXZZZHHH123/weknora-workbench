/* Run with Node + Playwright. Only a generated fixture server is contacted. */
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
const assert = require('node:assert/strict');
const { chromium } = require('playwright');
const root = path.join(__dirname, '..', 'src', 'structured_data');
const requests = [];
let published = false;
let sampleDelay = 0;
let storedDelay = 0;
let failFullCheck = false;
const mapping = {sheet:'数据汇总',fingerprint:'fixture-small',blocks:[
  {name:'销售明细',date_mode:'date',date_column:'A',category_column:'B',category_name:'地区',header_rows:[3],data_start_row:4,data_end_row:8,columns:[{column:'C',metric:'销量',unit:'件',header_cells:['C3']},{column:'D',metric:'销售额',unit:'元',header_cells:['D3']}]},
  {name:'库存记录',date_mode:'date',date_column:'F',category_column:'G',category_name:'产品',header_rows:[3],data_start_row:4,data_end_row:8,columns:[{column:'H',metric:'库存',unit:'件',header_cells:['H3']}]},
  {name:'月度汇总',date_mode:'period',date_column:'A',header_rows:[11],data_start_row:12,data_end_row:14,columns:[{column:'C',metric:'月度销售额',unit:'元',header_cells:['C11']}]}
]};
const small={name:'数据汇总',rows:15,columns:8,status:'needs_review',layout:'mixed_regions',fingerprint:'fixture-small',mapping,regions:[],reasons:['发现三个独立区块，请核对。']};
const large={name:'大数据表',rows:500000,columns:100,status:'needs_review',layout:'single_header',fingerprint:'fixture-large',mapping:{sheet:'大数据表',fingerprint:'fixture-large',blocks:[{name:'大表记录',date_mode:'date',date_column:'A',header_rows:[1],data_start_row:2,data_end_row:null,columns:[{column:'B',metric:'测试数值',unit:null,header_cells:['B1']}]}]},regions:[],reasons:[]};
const chartOnly={name:'库存图',rows:1,columns:1,status:'needs_review',layout:'chart_or_image',fingerprint:'fixture-chart',mapping:{sheet:'库存图',fingerprint:'fixture-chart',blocks:[]},charts:{chart_count:2,image_count:1},regions:[],reasons:[]};
const trusted={...small,name:'已核验规则表',status:'trusted_existing',mapping:{...mapping,sheet:'已核验规则表',blocks:[mapping.blocks[0]]}};
const sheets=()=>[{...small,status:published?'confirmed':'needs_review'},large,chartOnly,trusted];
const col=n=>{let s='';for(;n;n=Math.floor((n-1)/26))s=String.fromCharCode(65+(n-1)%26)+s;return s;};
function view(sheet,sr,sc,nr,nc){
  const rows=sheet.rows,cols=sheet.columns,er=Math.min(rows,sr+nr-1),ec=Math.min(cols,sc+nc-1),cells=[];
  const values={A1:'销售、库存与月度汇总',A3:'日期',B3:'地区',C3:'销量',D3:'销售额',F3:'日期',G3:'产品',H3:'库存',A11:'月份',C11:'销售额',A12:'2026-07',C12:268500,A13:'2026-08',C13:291800,A14:'2026-09',C14:305600};
  for(let r=sr;r<=er;r++)for(let c=sc;c<=ec;c++){const cell=col(c)+r;let value=sheet===large?(r===1?(c===1?'日期':'指标 '+col(c)):c===1?'2026-09-01':r*100+c):values[cell]??'';if(sheet===small&&r>=4&&r<=8)value=c===1||c===6?`2026-09-${String(r).padStart(2,'0')}`:c===2?'华东':c===7?'产品 A':c===3?r*24:c===4?r*2520:c===8?1000-r*50:'';cells.push({row:r,column:c,cell,value,display:String(value),raw_value:value,style_id:cell==='C3'?1:0});}
  return {sheet_name:sheet.name,total_rows:rows,total_cols:cols,range:{start_row:sr,end_row:er,start_col:sc,end_col:ec,row_count:er-sr+1,col_count:ec-sc+1},cells,merges:sheet===small?[{start_row:1,end_row:1,start_col:1,end_col:8}]:[],styles:{0:{font:{name:'Microsoft YaHei',size:11},fill:{},alignment:{}},1:{font:{bold:true,color:{rgb:'FFB91C1C'}},fill:{pattern:'solid',color:{rgb:'FFFFEED0'}},alignment:{horizontal:'center'}}},column_widths:{1:14,6:14},row_heights:{1:25},warnings:[],has_more_rows:er<rows,has_more_cols:ec<cols};
}
const server=http.createServer(async(req,res)=>{
  const url=new URL(req.url,'http://fixture');let body='';for await(const chunk of req)body+=chunk;const data=body?JSON.parse(body):null;requests.push({method:req.method,path:url.pathname,params:Object.fromEntries(url.searchParams),data});
  if(url.pathname==='/review-host'){
    res.setHeader('Content-Type','text/html');
    res.end(`<html><body><iframe id="reviewFrame" style="width:100%;height:900px;border:0" src="/?view=review&import_id=123456789012345678901234"></iframe><script>
      window.updates=[];window.addEventListener('message',e=>{
        if(e.origin!==location.origin||e.source!==document.getElementById('reviewFrame').contentWindow)return;
        if(e.data.type==='structured-ready')e.source.postMessage({type:'structured-auth',token:'fixture',tenant:'10000'},location.origin);
        if(e.data.type==='structured-import-updated')window.updates.push(e.data);
      });</script></body></html>`);return;
  }
  let result={};
  if(url.pathname==='/api/ui-config')result={title:'结构化数据工作台',workspace_label:'Excel 数据管理',agent_name:'表格数据分析',weknora_url:'http://127.0.0.1:18080'};
  else if(url.pathname==='/api/datasets')result={datasets:[]};
  else if(url.pathname==='/api/imports')result={imports:[{import_id:'123456789012345678901234',source_file:'销售数据.xlsx',updated_at:'2026-10-10T12:00:00'}]};
  else if(url.pathname.endsWith('/sheet-view')){const s=url.searchParams.get('name')===large.name?large:small;result=view(s,+url.searchParams.get('start_row'),+url.searchParams.get('start_col'),+url.searchParams.get('row_count'),+url.searchParams.get('col_count'));}
  else if(url.pathname.endsWith('/sheet'))result=sheets().find(s=>s.name===url.searchParams.get('name'));
  else if(url.pathname.endsWith('/sample-preview')){const block=data.mapping.blocks[0];if(sampleDelay)await new Promise(resolve=>setTimeout(resolve,sampleDelay));result={sample_only:true,blocks:[{name:block.name,series:[{series_id:'sample',metric:block.columns[0].metric}],sample:[{series_id:'sample',date:'2026-09-04',category:'华东',value:96,source_cell:'C4'}]}],notice:'只有少量样本，完整检查通过后才能导入。'};}
  else if(url.pathname==='/api/datasets/saved-fixture/series')result={series:['C','F','U','X'].map(c=>({series_id:'saved-'+c,metric:'20天原料',sheet:small.name,dimensions:{原表列:c},unit:'元/吨',header_cells:[c+'3']})),total:4,next_offset:null};
  else if(url.pathname==='/api/query'){if(storedDelay)await new Promise(resolve=>setTimeout(resolve,storedDelay));const c=url.searchParams.get('series_id').replace('saved-',''),offset=+url.searchParams.get('offset');result={series:{metric:'20天原料',dimensions:{原表列:c},unit:'元/吨'},record_count:26,valid_count:26,rows:[{date:'2026-09-04',value:12055+offset,raw_value:12055+offset,source_cell:c+(4+offset),date_cell:'A'+(4+offset),value_status:'valid'}]};}
  else if(url.pathname.endsWith('/preview'))result={id:'fixture-check',status:'processing'};
  else if(url.pathname==='/api/jobs/fixture-check')result=failFullCheck?{id:'fixture-check',status:'failed',error:'模拟完整检查失败',review_target:{sheet:small.name,block_index:0,field:'date_column',cells:['A4','C4']}}:{id:'fixture-check',status:'completed',result:{series_count:3,observation_count:20,mapping:{...mapping,blocks:[{...mapping.blocks[0],columns:[{column:'C',metric:'20天原料',unit:'元/吨',dimensions:{原表列:'C'}},{column:'F',metric:'20天原料',unit:'元/吨',dimensions:{原表列:'F'}}]}]},blocks:[{name:'销售明细',series_count:2,observation_count:10,series:[],sample:[],issues:[]}],sample:[],issues:[]}};
  else if(url.pathname.endsWith('/confirm')){published=true;result={id:'fixture-publish',status:'processing'};}
  else if(url.pathname==='/api/jobs/fixture-publish')result={status:'completed',result:{}};
  else if(url.pathname.startsWith('/api/imports/'))result={import_id:'123456789012345678901234',source_file:'销售数据.xlsx',dataset_id:'saved-fixture',sheets:sheets()};
  else {const file=url.pathname==='/'?'index.html':url.pathname.slice(1);if(!['index.html','sheet-review.js','sheet-review.css'].includes(file)){res.writeHead(404);res.end();return;}res.setHeader('Content-Type',file.endsWith('.js')?'application/javascript':file.endsWith('.css')?'text/css':'text/html');res.end(fs.readFileSync(path.join(root,file)));return;}
  res.setHeader('Content-Type','application/json');res.end(JSON.stringify(result));
});
(async()=>{
  const browser=await chromium.launch({headless:true,...(process.env.SHEET_REVIEW_BROWSER?{executablePath:process.env.SHEET_REVIEW_BROWSER}:{})});
  try {
    const page=await browser.newPage({viewport:{width:1500,height:1000}}),errors=[];page.on('pageerror',e=>errors.push(e.message));
    // Fulfill every request in-memory; no production service or network is used.
    await page.route('**/*',route=>new Promise((resolve,reject)=>{
      const request=route.request(),url=new URL(request.url()),headers={};let status=200;
      const req={url:url.pathname+url.search,method:request.method(),async *[Symbol.asyncIterator](){yield request.postData()||'';}};
      const res={setHeader(k,v){headers[k]=v;},writeHead(v){status=v;},end(body=''){route.fulfill({status,headers,body}).then(resolve,reject);}};
      server.listeners('request')[0](req,res).catch(reject);
    }));
    await page.goto('http://sheet-review.fixture/');await page.locator('.sheet-grid td').first().waitFor();
    assert.equal(errors.length,0,errors.join('\n'));
    assert.equal(await page.locator('#blockEditor').isVisible(),true,'Selecting a sheet must show the current block fields beside the source');
    assert.equal(await page.locator('.mapping-block.selected [data-column-field="metric"]').first().isVisible(),true,'Field review must not be hidden in advanced settings');
    const sourceBox=await page.locator('.sheet-source').boundingBox(),editorBox=await page.locator('#blockEditor').boundingBox();
    assert(editorBox.x>=sourceBox.x+sourceBox.width-1,'Fields and source must be beside each other');
    assert(Math.abs(editorBox.y-sourceBox.y)<2,'The editor must not push the source down');
    assert((await page.locator('.sheet-tab').first().boundingBox()).height<=42,'Worksheet tabs occupy one compact row');
    assert((await page.locator('.block-chip').first().boundingBox()).width<150,'Block controls must remain compact');
    assert.equal(await page.locator('.range-handle').count(),0,'Selection must not depend on tiny resize handles');
    assert.equal(await page.locator('#confirmMapping').isDisabled(),true,'Pending fields must be reviewed before import');
    async function dragCells(from,to){
      await from.scrollIntoViewIfNeeded();await to.scrollIntoViewIfNeeded();
      const a=await from.boundingBox(),b=await to.boundingBox();
      await page.mouse.move(a.x+a.width/2,a.y+a.height/2);await page.mouse.down();
      await page.mouse.move(b.x+b.width/2,b.y+b.height/2,{steps:5});await page.mouse.up();
    }
    const firstCell=page.locator('[data-cell-row="4"][data-cell-col="3"]'),lastCell=page.locator('[data-cell-row="7"][data-cell-col="4"]');
    const initialRange=await page.locator('.mapping-block.selected [data-range-input]').inputValue();
    await page.evaluate(()=>window.selectionCell=document.querySelector('[data-cell-row="4"][data-cell-col="3"]'));
    await dragCells(firstCell,lastCell);
    await page.locator('#rangeSelection').waitFor({state:'visible'});
    assert.equal(await page.locator('#selectionRange').innerText(),'C4:D7');
    assert.equal(await page.evaluate(()=>selectionCell.isConnected),true,'Drawing must preserve the grid DOM and pointer capture');
    assert.equal(await page.locator('.mapping-block.selected [data-range-input]').inputValue(),initialRange,'A temporary selection must not overwrite the mapping');
    await page.locator('#applySelection').click();
    assert.equal(await page.locator('.mapping-block.selected [data-range-input]').inputValue(),'C4:D7');
    assert.equal(await page.locator('#ackBlock').isChecked(),false,'Changing the range invalidates acknowledgement');
    await page.locator('#undoMapping').click();
    assert.equal(await page.locator('.mapping-block.selected [data-range-input]').inputValue(),initialRange);
    await firstCell.click();await page.keyboard.down('Shift');await lastCell.click();await page.keyboard.up('Shift');
    assert.equal(await page.locator('#selectionRange').innerText(),'C4:D7','Shift-click extends the previous cell anchor');
    await page.keyboard.press('Escape');
    await dragCells(firstCell,lastCell);await page.keyboard.press('Escape');
    assert.equal(await page.locator('#rangeSelection').isHidden(),true,'Escape cancels a temporary selection');
    assert.equal(await page.locator('.mapping-block.selected [data-range-input]').inputValue(),initialRange);
    await page.locator('.mapping-block.selected [data-source-cell="C3"]').click();
    assert.equal(await page.locator('[data-cell-row="3"][data-cell-col="3"]').evaluate(el=>el.classList.contains('located-cell')),true);
    await page.locator('#previewBlockSample').click();
    await page.locator('#samplePreview .sample-records').waitFor();
    assert.equal(await page.locator('#ackBlock').isChecked(),false,'A small sample never approves a block');
    assert.equal(requests.filter(r=>r.path.endsWith('/preview')||r.path.endsWith('/confirm')).length,0,'Sample review must not run a full check or publish');
    await page.locator('#previewBlockSample').click();
    sampleDelay=180;await page.locator('#previewBlockSample').click();
    await page.locator('[data-select-block]').nth(1).click();await page.waitForTimeout(250);
    assert.equal(await page.locator('#samplePreview').isHidden(),true,'A cancelled sample cannot appear in another block');
    sampleDelay=0;await page.locator('[data-select-block]').first().click();
    const nameInput=page.locator('.mapping-block.selected [data-column-field="metric"]').first();
    const savedName=await nameInput.inputValue();await nameInput.fill('');await page.locator('#ackBlock').click();
    assert.equal(await page.locator('#ackBlock').isChecked(),false,'Missing field names cannot be acknowledged');
    assert.match(await page.locator('#mappingFeedback').innerText(),/C 列的指标名称为空/);
    assert.equal(await page.locator('#mappingFeedback').getAttribute('role'),'alert');
    assert.match(await page.locator('#mappingFeedback').innerText(),/原文件和当前已导入版本仍然保留/);
    await page.locator('.mapping-block.selected [data-column-field="metric"].review-invalid').waitFor();
    assert.equal(await page.locator('[data-cell-row="3"][data-cell-col="3"]').evaluate(el=>el.classList.contains('review-error-cell')),true);
    await nameInput.fill(savedName);await page.locator('#ackBlock').check();await nameInput.fill(savedName+'（核对）');
    assert.equal(await page.locator('#ackBlock').isChecked(),false,'Editing a field name invalidates the previous acknowledgement');
    await nameInput.fill(savedName);
    await page.locator('#ackBlock').check();
    const unitInput=page.locator('.mapping-block.selected [data-column-field="unit"]').first();
    const savedUnit=await unitInput.inputValue();await unitInput.fill('万件');
    assert.equal(await page.locator('#ackBlock').isChecked(),false,'Changing a unit requires fresh field acknowledgement');
    await unitInput.fill(savedUnit);
    await page.locator('#ackBlock').check();
    await page.locator('.mapping-block.selected [data-range-input]').fill('C4:D7');
    assert.equal(await page.locator('#ackBlock').isChecked(),false,'An unapplied range cannot retain old acknowledgement');
    await page.locator('#ackBlock').click();
    assert.equal(await page.locator('#ackBlock').isChecked(),false,'Apply the visible range before acknowledging it');
    await unitInput.fill('件');
    assert.equal(await page.locator('.mapping-block.selected [data-range-input]').inputValue(),'C4:D7','Editing another field preserves an unapplied range');
    const samplesBefore=requests.filter(r=>r.path.endsWith('/sample-preview')).length;
    await page.locator('#previewBlockSample').click();
    assert.equal(requests.filter(r=>r.path.endsWith('/sample-preview')).length,samplesBefore,'An unapplied range cannot preview a different stored range');
    await page.locator('.mapping-block.selected [data-range-input]').fill(initialRange);
    await page.locator('#reviewFocus').click();
    assert.equal(await page.locator('#importReview').evaluate(el=>el.classList.contains('review-focused')),true);
    assert.equal(await page.locator('#ackBlock').evaluate(el=>{const r=el.getBoundingClientRect(),hit=document.elementFromPoint(r.x+r.width/2,r.y+r.height/2);return !!hit?.closest('.editor-footer');}),true,'The fixed import footer must not cover the block acknowledgement');
    await page.keyboard.press('Escape');
    assert.equal(await page.locator('#importReview').evaluate(el=>el.classList.contains('review-focused')),false);
    assert.equal(requests.filter(r=>r.path.endsWith('/preview')).length,0,'Opening a sheet must not parse it fully');
    assert(requests.some(r=>r.params.summary_only==='true'));
    assert.equal(await page.locator('#mappingForm>.mapping-block').count(),3);
    assert.equal(await page.locator('.check-block').count(),0);
    assert.equal(await page.locator('#previewMapping').count(),0);
    assert.equal(await page.locator('#sheetSearch').isVisible(),true);
    assert.deepEqual(await page.locator('[data-sheet-filter]').evaluateAll(els=>els.map(e=>e.dataset.sheetFilter)),['all','pending','charts']);
    assert.equal(await page.locator('.sheet-tab').count(),4);
    assert.match(await page.locator('.sheet-tab[aria-pressed="true"] small').innerText(),/15 行 × 8 列 · 3 个区块/);
    assert.equal(await page.locator('.sheet-grid th.row-number').count(),15);
    assert.equal(await page.locator('.range-map').count(),0);
    const chipTops=await page.locator('.block-chip').evaluateAll(els=>els.map(el=>Math.round(el.getBoundingClientRect().top)));
    assert.equal(chipTops.length,3);assert.equal(new Set(chipTops).size,1,'Blocks sit on one horizontal row');
    assert.equal(await page.locator('[data-cell-row="3"][data-cell-col="3"]').evaluate(el=>el.style.getPropertyValue('--source-fg')),'#B91C1C');
    assert.equal(await page.locator('.sheet-grid td[colspan="8"]').count(),1);
    await page.locator('#suggestMapping').click();await page.locator('#aiDialog').waitFor({state:'visible'});
    assert.match(await page.locator('#aiDialogText').innerText(),/不会发送/);
    assert.equal(await page.locator('#aiContinue').isHidden(),true);
    await page.locator('#aiCancel').click();
    assert.equal(requests.filter(r=>r.path.endsWith('/suggest')).length,0);
    await page.locator('[data-cell-row="4"][data-cell-col="8"]').click();
    assert.equal(await page.locator('#blockEditor').getAttribute('hidden'),null);
    assert.match(await page.locator('.mapping-block[open]>summary').innerText(),/库存记录/);
    await page.locator('[data-select-block]').first().click();
    await page.locator('.mapping-block[open] .block-settings>summary').click();await page.locator('.mapping-block[open] [data-field="name"]').fill('销售明细（核对）');
    await page.locator('#sheetSearch').fill('大数据表');
    assert.equal(await page.locator('#sheetMenuList [data-sheet-index]').count(),1,'All-sheet search filters the menu');
    assert.equal(await page.locator('.sheet-tab').count(),4,'Menu search does not remove the current worksheet tabs');
    await page.locator('#sheetMenuList [data-sheet-index="1"]').click();await page.locator('.sheet-grid th.row-number').first().waitFor();
    await page.waitForFunction(()=>document.querySelector('.source-head h3')?.textContent==='大数据表'&&document.querySelectorAll('.sheet-grid th.row-number').length===60);
    assert((await page.locator('.sheet-grid td').count())<=1440,'Large sheet must render a bounded window');
    assert.equal(await page.locator('.range-map').count(),0);
    await page.locator('#nextCols').click();await page.waitForFunction(()=>document.querySelector('#viewSummary').textContent.includes('Y1'));
    await page.locator('.sheet-tab[data-sheet-index="0"]').click();await page.waitForFunction(()=>document.querySelector('.source-head h3')?.textContent==='数据汇总'&&document.querySelectorAll('.sheet-grid th.row-number').length===15);
    assert.match(await page.locator('#draftNotice').innerText(),/恢复/);
    assert.match(await page.locator('.block-chip').first().getAttribute('title'),/销售明细（核对）/);
    assert.equal(await page.locator('.mapping-block.selected [data-field="name"]').inputValue(),'销售明细（核对）','The full block name remains in the restored mapping');
    await page.locator('#previewBlockSample').click();
    await page.locator('#samplePreview .sample-records').waitFor();
    assert.match(await page.locator('#samplePreview').innerText(),/C4/);
    const sampleBox=await page.locator('#samplePreview').boundingBox(),fieldsBox=await page.locator('#mappingForm').boundingBox();
    assert(sampleBox.y<fieldsBox.y,'Samples stay above fields, alongside the source table');
    await page.locator('#samplePreview [data-source-cell]').first().click();
    if(process.argv[2])await page.locator('#importReview').screenshot({path:process.argv[2]});
    await page.locator('#previewBlockSample').click();
    // A shared date anchor outside the data body is a separate marked source.
    assert.equal(await page.evaluate(()=>sheetReviewHelpers.hitBlock({date_cell:'AG15',data_start_row:17,data_end_row:19,header_rows:[16],columns:[{column:'AH'}]},15,33)),'anchor');
    assert.equal(await page.evaluate(()=>sheetReviewHelpers.hitBlock({data_start_row:4,data_end_row:8,header_rows:[3],columns:[{column:'A'},{column:'C'}]},4,2)),null,'Unselected gaps must not be colored');
    // Explicit range changes preserve date/header semantics and invalidate full checks.
    await page.locator('[data-select-block]').first().click();await page.locator('.mapping-block[open] [data-range-input]').fill('C4:D7');await page.locator('.mapping-block[open] [data-map-action="apply-range"]').click();
    await page.locator('.mapping-block[open] .block-settings>summary').click();assert.equal(await page.locator('.mapping-block[open] [data-field="data_end_row"]').inputValue(),'7');
    assert.equal(await page.locator('.mapping-block[open] [data-field="date_column"]').inputValue(),'A');
    await page.locator('#ackBlock').check();
    await page.locator('[data-select-block]').nth(1).click();await page.locator('#excludeCurrentBlock').click();
    await page.locator('[data-select-block]').nth(2).click();await page.locator('#excludeCurrentBlock').click();
    assert.equal(await page.locator('#confirmMapping').isDisabled(),false);
    const beforeStored=requests.length;
    await page.locator('#storedResults').click();await page.locator('#storedRowsNext').waitFor();
    assert.equal(await page.locator('[data-stored-series]').count(),4,'Every same-name stored column remains visible');
    await page.locator('.stored-field-directory>summary').click();
    for(const c of ['C','F','U','X'])assert.match(await page.locator('[data-result-series="saved-'+c+'"]').innerText(),new RegExp('原表列.*'+c));
    await page.locator('[data-result-cell="C4"]').click();
    assert.equal(await page.locator('#resultPanel').isVisible(),true,'Source navigation keeps saved records open beside the source table');
    assert.equal(await page.locator('dialog[open]').count(),0,'Saved records must never open a modal');
    assert.equal(await page.locator('[data-cell-row="4"][data-cell-col="3"]').evaluate(el=>el.classList.contains('located-cell')),true);
    await page.locator('[data-stored-series="saved-X"]').click();
    await page.waitForFunction(()=>document.querySelector('#storedRows').textContent.includes('X4'));
    if(process.argv[4])await page.locator('#resultPanel').screenshot({path:process.argv[4]});
    await page.locator('#storedRowsNext').click();await page.waitForFunction(()=>document.querySelector('#storedRows').textContent.includes('X29'));
    assert(requests.some(r=>r.path==='/api/query'&&r.params.offset==='25'&&r.params.dataset_id==='saved-fixture'));
    assert.equal(requests.slice(beforeStored).filter(r=>r.method!=='GET').length,0,'Stored results cannot run a parser/model/publish');
    storedDelay=150;await page.locator('[data-stored-series="saved-C"]').click();await page.locator('[data-stored-series="saved-U"]').click();
    await page.waitForFunction(()=>document.querySelector('#storedRows')?.textContent.includes('U4'));
    assert.equal(await page.locator('[data-stored-series]').count(),4,'Cancelled record requests cannot replace the result dialog with an error');storedDelay=0;
    storedDelay=150;await page.locator('[data-stored-series="saved-C"]').click();
    await page.locator('#closeResults').click();await page.waitForTimeout(250);
    assert.equal(await page.locator('#resultPanel').isHidden(),true,'A late record response cannot reopen a closed inline pane');
    assert.equal(await page.locator('#mappingForm').isVisible(),true,'Returning to fields preserves the current draft');
    storedDelay=0;await page.keyboard.press('Escape');
    const previewsBefore=requests.filter(r=>r.path.endsWith('/preview')).length;
    await page.locator('#confirmMapping').click();await page.locator('#confirmDialog').waitFor({state:'visible'});
    assert.match(await page.locator('#confirmList').innerText(),/不导入/);
    assert.match(await page.locator('#confirmDialog .warning').innerText(),/仅处理本次修改的工作表/);
    failFullCheck=true;await page.locator('#confirmGo').click();
    await page.waitForFunction(()=>document.querySelector('#mappingFeedback').textContent.includes('模拟完整检查失败'));
    await page.locator('.mapping-block.selected [data-field="date_column"].review-invalid').waitFor();
    assert.equal(await page.locator('[data-cell-row="4"][data-cell-col="1"]').evaluate(el=>el.classList.contains('review-error-cell')),true,'Background errors carry cell coordinates');
    assert.equal(published,false,'A failed full check must preserve the existing published version');
    assert.equal(requests.filter(r=>r.path.endsWith('/confirm')).length,0,'Full-check failure must not send a publish request');
    failFullCheck=false;await page.locator('#confirmMapping').click();
    await page.locator('#confirmGo').click();await page.waitForFunction(()=>document.querySelector('#notice').textContent.includes('已导入'));
    assert.equal(requests.filter(r=>r.path.endsWith('/preview')).length,previewsBefore+2,'Each failed or successful import attempt runs its own full check');
    const preview=requests.find(r=>r.path.endsWith('/preview'));assert.equal(preview.params.background,'true');assert.equal(preview.data.mapping.blocks.length,1);assert(!preview.data.mapping.blocks[0]._review_id);
    const confirm=requests.find(r=>r.path.endsWith('/confirm'));assert.equal(confirm.data.mapping.blocks.length,1);
    await page.waitForFunction(()=>document.querySelector('.source-head h3')?.textContent==='数据汇总');
    await page.locator('[data-sheet-filter="charts"]').click();await page.locator('.sheet-tab').first().click();
    await page.waitForFunction(()=>document.querySelector('.source-head h3')?.textContent==='库存图'&&document.querySelector('#chartPane')&&!document.querySelector('#chartPane').hidden);
    assert.equal(await page.locator('#tablePane').isHidden(),true);
    assert.equal(await page.locator('#confirmMapping').count(),0);
    assert.match(await page.locator('#chartPane').innerText(),/2 个图表/);
    assert.equal(requests.filter(r=>r.path.includes('sheet-view')&&r.params.name==='库存图').length,0);
    if(process.argv[3])await page.locator('#importReview').screenshot({path:process.argv[3]});
    assert.equal(errors.length,0,errors.join('\n'));
    await page.locator('[data-sheet-filter="all"]').click();
    await page.locator('.sheet-tab[data-sheet-index="3"]').click();
    await page.waitForFunction(()=>document.querySelector('.source-head h3')?.textContent==='已核验规则表');
    assert.equal(await page.locator('#blockEditor').isVisible(),true,'Verified rules still show their actual fields and sources');
    assert.match(await page.locator('.editor-heading').innerText(),/可调整/);
    assert.equal(await page.locator('.source-head .badge').innerText(),'已可查询');
    assert.equal(await page.locator('#previewBlockSample').innerText(),'查看记录');
    await page.locator('.sheet-processing-details>summary').click();
    assert.match(await page.locator('.sheet-processing-details').innerText(),/规则复用.*可调整/);
    assert.match(await page.locator('.source-click-hint').innerText(),/左侧原表.*高亮/);
    assert.equal(await page.locator('.mapping-block.selected [data-column-field="metric"]').first().isEnabled(),true);
    assert.equal(await page.locator('#confirmMapping').isDisabled(),true,'Unchanged verified mappings do not need republishing');
    assert.equal(await page.locator('#addMappingBlock').isEnabled(),true);
    const origins=await page.locator('.mapping-block.selected .field-origin').first().evaluate(el=>{const r=el.getBoundingClientRect();return {width:r.width,parent:el.closest('td').colSpan};});
    assert.equal(origins.parent,4,'Sources span the complete row below field inputs');
    await page.locator('.mapping-block.selected [data-source-cell="C3"]').click();
    assert.equal(await page.locator('[data-cell-row="3"][data-cell-col="3"]').evaluate(el=>el.classList.contains('located-cell')),true,'Read-only source links remain clickable');
    if(process.argv[5])await page.locator('#importReview').screenshot({path:process.argv[5]});
    await page.locator('#previewBlockSample').click();await page.locator('#storedRowsNext').waitFor();
    await page.locator('[data-result-cell="C4"]').click();
    assert.equal(await page.locator('#resultPanel').isVisible(),true,'Read-only sheets also retain inline records after source navigation');
    assert.equal(await page.locator('dialog[open]').count(),0);
    await page.locator('#closeResults').click();
    await page.locator('.mapping-block.selected [data-column-field="metric"]').first().fill('调整后的指标');
    await page.locator('.mapping-block.selected [data-range-input]').fill('C4:D7');
    await page.locator('.mapping-block.selected [data-map-action="apply-range"]').click();
    assert.equal(await page.locator('#ackBlock').isChecked(),false);
    assert.equal(await page.locator('#previewBlockSample').innerText(),'预览几条记录');
    await page.locator('#previewBlockSample').click();await page.locator('#samplePreview .sample-records').waitFor();
    assert.equal(requests.filter(r=>r.path.endsWith('/sample-preview')).at(-1).data.mapping.sheet,'已核验规则表');
    await page.locator('#ackBlock').check();
    assert.equal(await page.locator('#confirmMapping').isEnabled(),true);
    await page.locator('#confirmMapping').click();
    assert.match(await page.locator('#confirmList').innerText(),/A3:D7/);
    await page.locator('#confirmBack').click();
    published=false;
    const host=await browser.newPage({viewport:{width:1500,height:1000}});
    await host.route('**/*',route=>new Promise((resolve,reject)=>{
      const request=route.request(),url=new URL(request.url()),headers={};let status=200;
      const req={url:url.pathname+url.search,method:request.method(),async *[Symbol.asyncIterator](){yield request.postData()||'';}};
      const res={setHeader(k,v){headers[k]=v;},writeHead(v){status=v;},end(body=''){route.fulfill({status,headers,body}).then(resolve,reject);}};
      server.listeners('request')[0](req,res).catch(reject);
    }));
    await host.goto('http://sheet-review.fixture/review-host');
    const review=host.frameLocator('#reviewFrame');
    await review.locator('#ackBlock').waitFor();
    assert.equal(await review.locator('#importSelect').isDisabled(),true,'Materials review stays pinned to the requested import');
    await review.locator('#ackBlock').check();
    await review.locator('[data-select-block]').nth(1).click();await review.locator('#excludeCurrentBlock').click();
    await review.locator('[data-select-block]').nth(2).click();await review.locator('#excludeCurrentBlock').click();
    await review.locator('#confirmMapping').click();await review.locator('#confirmGo').click();
    await host.waitForFunction(()=>window.updates.length===1,{}, {timeout:5000});
    assert.deepEqual(await host.evaluate(()=>window.updates[0]),{
      type:'structured-import-updated',import_id:'123456789012345678901234',dataset_id:'',tenant_id:'10000',status:'confirmed',
    },'Publishing in the embedded editor updates the materials center in the same workspace');
    await host.close();
    console.log('PASS: compact tabs and blocks, adjacent field review, direct selection, stable drag DOM, cancellation, undo, source location, bounded samples, stale-result rejection, field acknowledgement, token gate, bounded grid, draft restore, excluded blocks, full check and publish, chart-only preview.');
  } finally {await browser.close();server.close();}
})().catch(error=>{server.close();console.error(error);process.exitCode=1;});
