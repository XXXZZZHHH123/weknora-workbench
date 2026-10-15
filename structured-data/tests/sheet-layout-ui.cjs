/* Exercise the shipped Excel review UI using synthetic source responses only. */
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const { chromium } = require('playwright');
const source = path.resolve(process.env.SHEET_LAYOUT_SOURCE || path.join(__dirname, '..', 'src', 'structured_data'));
const output = process.env.SHEET_LAYOUT_OUTPUT && path.resolve(process.env.SHEET_LAYOUT_OUTPUT);
const requests = [], checks = [], errors = [];
const importId = '123456789012345678901234';
const mapping = {sheet:'布局夹具',fingerprint:'layout-fixture',blocks:[{name:'合成记录',date_mode:'date',date_column:'A',header_rows:[1],data_start_row:2,data_end_row:5,columns:[{column:'B',metric:'合成指标',unit:'测试单位',header_cells:['B1']}]}]};
const sheet = {name:mapping.sheet,rows:6,columns:4,status:'needs_review',layout:'single_header',fingerprint:mapping.fingerprint,mapping,regions:[],reasons:[]};
function sheetView() {
  const cells=[];
  for(let row=1;row<=6;row++)for(let column=1;column<=4;column++) {
    const cell=String.fromCharCode(64+column)+row,value=row===1?['日期','合成指标','辅助列','说明'][column-1]:column===1?'2026-01-01':column===2?row*11:'测试';
    cells.push({row,column,cell,value,display:String(value),raw_value:value,style_id:cell==='B1'?1:0});
  }
  return {sheet_name:sheet.name,total_rows:6,total_cols:4,range:{start_row:1,end_row:6,start_col:1,end_col:4,row_count:6,col_count:4},cells,merges:[],styles:{0:{font:{name:'Microsoft YaHei',size:11},fill:{},alignment:{}},1:{font:{bold:true,color:{rgb:'FFB91C1C'}},fill:{pattern:'solid',color:{rgb:'FFFFEED0'}},alignment:{horizontal:'center'}}},column_widths:{1:14},row_heights:{1:25},warnings:[],has_more_rows:false,has_more_cols:false};
}
const selectors={review:'#importReview',toolbar:'.review-file-toolbar',tabs:'.sheet-tabs-row',tab:'.sheet-tab',panes:'.review-panes',source:'.sheet-source',editor:'#blockEditor',heading:'.editor-heading',fields:'#blockEditor .mapping-fields',label:'#blockEditor label',mapping:'#mappingForm',sourceCell:'.sheet-grid td[data-cell-row="2"][data-cell-col="2"]',file:'#importSelect',unit:'.mapping-block.selected [data-column-field="unit"]',search:'#sheetSearch'};
const geometry=page=>page.evaluate(selectors=>{const origin=document.querySelector(selectors.review).getBoundingClientRect();return Object.fromEntries(Object.entries(selectors).map(([key,selector])=>{const el=document.querySelector(selector),s=getComputedStyle(el),r=el.getBoundingClientRect();return [key,{x:r.x-origin.x,y:r.y-origin.y,width:r.width,height:r.height,padding:s.padding,gap:s.gap,fontSize:s.fontSize,lineHeight:s.lineHeight,display:s.display,gridTemplateColumns:s.gridTemplateColumns}]}));},selectors);
const appearance=page=>page.evaluate(selectors=>Object.fromEntries(Object.entries(selectors).map(([key,selector])=>{const s=getComputedStyle(document.querySelector(selector));return [key,{color:s.color,background:s.backgroundColor,radius:s.borderRadius,colorScheme:s.colorScheme,primary:s.getPropertyValue('--primary').trim()}]})),selectors);
const sourceFormat=page=>page.locator('[data-cell-row="1"][data-cell-col="2"]').evaluate(el=>({style:el.getAttribute('style'),color:getComputedStyle(el).color,background:getComputedStyle(el).backgroundColor,fontWeight:getComputedStyle(el).fontWeight,textAlign:getComputedStyle(el).textAlign}));
function contrast(a,b){const luminance=color=>color.match(/[\d.]+/g).slice(0,3).map(Number).map(v=>{v/=255;return v<=.04045?v/12.92:((v+.055)/1.055)**2.4}).reduce((sum,v,i)=>sum+v*[.2126,.7152,.0722][i],0),x=luminance(a),y=luminance(b);return(Math.max(x,y)+.05)/(Math.min(x,y)+.05);}
function verify(name,fn){try{fn();checks.push({name,pass:true});}catch(error){checks.push({name,pass:false,error:error.message});}}
(async()=>{
  const browser=await chromium.launch({headless:true,...(process.env.SHEET_REVIEW_BROWSER?{executablePath:process.env.SHEET_REVIEW_BROWSER}:{})});
  const result={mode:'Synthetic shipped UI; no real account, business writes or model calls',source,checks,geometry:{},appearance:{},requests};
  try {
    const page=await browser.newPage({viewport:{width:1500,height:1000},colorScheme:'light'});page.on('pageerror',e=>errors.push(e.message));
    await page.route('**/*',async route=>{
      const request=route.request(),url=new URL(request.url());assert.equal(url.hostname,'sheet-layout.fixture');assert.equal(request.method(),'GET','No import, model or publish endpoints are allowed');requests.push({method:request.method(),path:url.pathname,query:Object.fromEntries(url.searchParams)});
      if(url.pathname.startsWith('/api/')) {
        let data;
        if(url.pathname==='/api/ui-config')data={title:'结构化数据工作台',workspace_label:'Excel 数据管理',agent_name:'表格数据分析',weknora_url:'/'};
        else if(url.pathname==='/api/datasets')data={datasets:[]};
        else if(url.pathname==='/api/imports')data={imports:[{import_id:importId,source_file:'布局夹具.xlsx',updated_at:'2026-01-01T00:00:00'}]};
        else if(url.pathname.endsWith('/sheet-view'))data=sheetView();
        else if(url.pathname.endsWith('/sheet'))data=sheet;
        else if(url.pathname==='/api/imports/'+importId)data={import_id:importId,source_file:'布局夹具.xlsx',sheets:[sheet]};
        else throw Error('Unexpected synthetic endpoint: '+url.pathname);
        await route.fulfill({contentType:'application/json',body:JSON.stringify(data)});return;
      }
      const file=url.pathname==='/'?'index.html':url.pathname.slice(1);assert(['index.html','sheet-review.css','workspace.css','sheet-review.js'].includes(file));
      await route.fulfill({contentType:file.endsWith('.js')?'application/javascript':file.endsWith('.css')?'text/css':'text/html',body:fs.readFileSync(path.join(source,file),'utf8')});
    });
    await page.goto('http://sheet-layout.fixture/');await page.locator('.sheet-grid td').first().waitFor();
    await page.locator(selectors.unit).fill('保留草稿');
    await page.evaluate(()=>{window.reviewSnapshot={origin:performance.timeOrigin,import:document.getElementById('importSelect').value,node:document.getElementById('sheetDetail'),cells:[...document.querySelectorAll('.sheet-grid td')].map(el=>[el.textContent,el.getAttribute('style')])};});
    const format=await sourceFormat(page);result.sourceFormat=format;
    verify('Workbook explicit red font, warm fill, weight and alignment are retained',()=>{assert.equal(format.color,'rgb(185, 28, 28)');assert.equal(format.background,'rgb(255, 238, 208)');assert.equal(format.fontWeight,'600');assert.equal(format.textAlign,'center');});
    for(const width of [1500,900,600]) {
      await page.setViewportSize({width,height:1000});result.geometry[width]=await geometry(page);
      verify(width+': source/editor retain the original responsive arrangement and density',()=>{const g=result.geometry[width];if(width>650){assert(Math.abs(g.editor.x-(g.source.x+g.source.width))<1);assert.equal(g.editor.y,g.source.y);assert.equal(g.editor.width,width>1300?390:325);}else{assert(g.editor.y>=g.source.y+g.source.height-1);assert.equal(g.editor.x,g.source.x);}assert.equal(g.tab.height,36);assert.equal(g.fields.gap,'9px');assert.equal(g.label.gap,'4px');assert.equal(g.mapping.padding,'10px 13px');assert.equal(g.source.padding,width>1300?'15px':'12px');});
    }
    await page.setViewportSize({width:1500,height:1000});const beforeThemeRequests=requests.length;
    for(const mode of ['light','dark']) {
      await page.emulateMedia({colorScheme:mode});await page.waitForFunction(mode=>document.documentElement.getAttribute('theme-mode')===mode,mode);
      await page.waitForFunction(mode=>{const s=getComputedStyle(document.getElementById('upload'));return s.backgroundColor===(mode==='dark'?'rgb(105, 199, 220)':'rgb(18, 109, 130)')&&s.color===(mode==='dark'?'rgb(18, 39, 53)':'rgb(255, 255, 255)');},mode);
      result.appearance[mode]=await appearance(page);result.geometry[mode]=await geometry(page);const a=result.appearance[mode];
      const outer=await page.locator('#directUpload').evaluate(el=>{const s=getComputedStyle(el),button=getComputedStyle(document.getElementById('upload'));return {primary:s.getPropertyValue('--primary').trim(),buttonBackground:button.backgroundColor,buttonColor:button.color};});
      verify(mode+': outer import controls still follow the system theme',()=>{assert.equal(outer.primary.toLowerCase(),mode==='dark'?'#69c7dc':'#126d82');assert.equal(outer.buttonBackground,mode==='dark'?'rgb(105, 199, 220)':'rgb(18, 109, 130)');assert.equal(outer.buttonColor,mode==='dark'?'rgb(18, 39, 53)':'rgb(255, 255, 255)');});
      verify(mode+': whole Excel review stays light paper with shared cool color and readable inherited text',()=>{assert.equal(a.review.background,'rgb(255, 255, 255)');assert.equal(a.review.colorScheme,'light');assert.equal(a.review.primary.toLowerCase(),'#126d82');assert.equal(a.heading.color,'rgb(32, 52, 71)');});
      verify(mode+': original compact form corners are preserved',()=>{assert.equal(a.file.radius,'6px');assert.equal(a.unit.radius,'6px');assert.equal(a.search.radius,'6px');});
      await page.locator('.sheet-tab').hover();const hover=await page.locator('.sheet-tab').evaluate(el=>{const s=getComputedStyle(el);return{color:s.color,background:s.backgroundColor}});
      verify(mode+': user-added worksheet hover stays subtly highlighted and readable',()=>{assert.equal(hover.background,'rgb(233, 243, 246)');assert(contrast(hover.color,hover.background)>=4.5);});
      await page.mouse.move(0,0);const tab=await page.locator('.sheet-tab').evaluate(el=>{const s=getComputedStyle(el);return{color:s.color,background:s.backgroundColor,underline:getComputedStyle(el,'::before').height}});
      verify(mode+': selected sheet retains paper background, accent underline and readable text',()=>{assert.equal(tab.background,'rgb(255, 255, 255)');assert.equal(tab.underline,'2px');assert(contrast(tab.color,tab.background)>=4.5);});
      const preserved=await page.evaluate(()=>reviewSnapshot.origin===performance.timeOrigin&&reviewSnapshot.import===document.getElementById('importSelect').value&&reviewSnapshot.node===document.getElementById('sheetDetail')&&JSON.stringify(reviewSnapshot.cells)===JSON.stringify([...document.querySelectorAll('.sheet-grid td')].map(el=>[el.textContent,el.getAttribute('style')]))&&document.querySelector('.mapping-block.selected [data-column-field="unit"]').value==='保留草稿');
      const actualFormat=await sourceFormat(page);
      verify(mode+': original source format, node, selected file and unsaved draft survive theme change',()=>{assert.equal(preserved,true);assert.deepEqual(actualFormat,format);});
      if(output){fs.mkdirSync(output,{recursive:true});await page.locator('#importReview').screenshot({path:path.join(output,mode+'.png')});}
    }
    verify('Theme changes preserve all measured review geometry',()=>assert.deepEqual(result.geometry.light,result.geometry.dark));
    verify('Theme changes do not fetch, reimport, publish or call models',()=>assert.equal(requests.length,beforeThemeRequests));
    verify('Original worksheet row density remains 29px',()=>assert.equal(result.geometry.light.sourceCell.height,29));
    await page.goto('http://sheet-layout.fixture/?view=review&import_id='+importId);await page.locator('.sheet-grid td').first().waitFor();
    await page.locator(selectors.unit).fill('核对页草稿');
    await page.evaluate(()=>{window.reviewSnapshot={origin:performance.timeOrigin,node:document.getElementById('sheetDetail'),cells:[...document.querySelectorAll('.sheet-grid td')].map(el=>[el.textContent,el.getAttribute('style')])};});
    const pinnedRequests=requests.length;
    for(const mode of ['light','dark']) {
      await page.emulateMedia({colorScheme:mode});await page.waitForFunction(mode=>document.documentElement.getAttribute('theme-mode')===mode,mode);
      result.appearance['pinned-'+mode]=await appearance(page);result.geometry['pinned-'+mode]=await geometry(page);const a=result.appearance['pinned-'+mode];
      verify(mode+': pinned original-review entry keeps light paper behind its top controls',()=>{assert.equal(a.review.background,'rgb(255, 255, 255)');assert.equal(a.review.colorScheme,'light');assert.equal(a.review.color,'rgb(32, 52, 71)');assert.equal(a.file.radius,'6px');});
      const pinnedContext=await page.evaluate(()=>document.body.classList.contains('review-only')&&document.getElementById('importSelect').disabled&&document.getElementById('importSelect').value==='123456789012345678901234'&&reviewSnapshot.origin===performance.timeOrigin&&reviewSnapshot.node===document.getElementById('sheetDetail')&&JSON.stringify(reviewSnapshot.cells)===JSON.stringify([...document.querySelectorAll('.sheet-grid td')].map(el=>[el.textContent,el.getAttribute('style')]))&&document.querySelector('.mapping-block.selected [data-column-field="unit"]').value==='核对页草稿');
      const pinnedFormat=await sourceFormat(page);
      verify(mode+': pinned review keeps its source identity, source formats and unsaved draft',()=>{assert.equal(pinnedContext,true);assert.deepEqual(pinnedFormat,format);});
      if(output)await page.locator('#importReview').screenshot({path:path.join(output,'pinned-'+mode+'.png')});
    }
    verify('Theme changes preserve pinned-review geometry',()=>assert.deepEqual(result.geometry['pinned-light'],result.geometry['pinned-dark']));
    verify('Pinned-review theme changes do not refetch, scan or import',()=>assert.equal(requests.length,pinnedRequests));
    verify('No uncaught browser errors',()=>assert.deepEqual(errors,[]));
    result.pass=checks.every(item=>item.pass);result.errors=errors;
  }catch(error){result.pass=false;result.error=error.stack;}
  finally{await browser.close();if(output){fs.mkdirSync(output,{recursive:true});fs.writeFileSync(path.join(output,'result.json'),JSON.stringify(result,null,2));}process.stdout.write(JSON.stringify(result,null,2)+'\n');if(!result.pass)process.exitCode=1;}
})();
