/* Real template/CSS/JS chart acceptance; synthetic observations only, no network data or model calls. */
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');
const {spawnSync}=require('node:child_process');
const {chromium}=require(process.env.RESEARCH_PLAYWRIGHT||'playwright');
const web=path.join(__dirname,'../src/tin/web'),out=process.env.JUDGMENT_CHART_EVIDENCE_DIR;
const fonts=path.resolve(__dirname,'../../WeKnora/frontend/public/fonts');
if(out)fs.mkdirSync(out,{recursive:true});
const timeline=Array.from({length:65},(_,index)=>({date:new Date(Date.UTC(2032,0,index+1)).toISOString().slice(0,10),value:19000+index*70+(index%4)*180}));
const metric={name:'合成价格',unit:'元/吨',series_id:'synthetic-series',latest:timeline.at(-1).value,previous:timeline.at(-2).value,date:timeline.at(-1).date,previous_date:timeline.at(-2).date,change:70,change_pct:.3,
 timeline,history_complete:true,segments:['12,40 220,8'],boundaries:[{kind:'fixed',name:'合成区间边界',value:22000,operator:'>'},{kind:'sma',name:'合成均线条件',window:20,operator:'crosses_below'}],streak:null};
const metrics=Array.from({length:4},(_,index)=>({...metric,series_id:`synthetic-series-${index}`,name:index===1?'合成指标长名称'.repeat(12):`合成指标 ${index+1}`}));
const sides={};
for(const [side,offset]of[['bull',0],['bear',2]])sides[side]={claim:`合成${side==='bull'?'多':'空'}方主逻辑`,
 evidence:[0,1].map(index=>({desc:`本条合成支持依据 ${index+1}`,series_id:metrics[offset+index].series_id,metric:metrics[offset+index]})),
 falsifiers:[{rule:{desc:'本条合成证伪条件',series_id:metrics[offset].series_id},condition:'高于 24,000 元/吨',state:'未触发',metric:metrics[offset]}]};
sides.bull.claim='合成多方观点包含较长的完整说明，用于验证左右观点长短不同时后面的研究条目仍然对齐。'.repeat(3);
sides.bull.evidence.splice(1,0,{desc:'合成纯文字支持依据，无图表也应作为完整研究条目。',series_id:null,metric:null});
sides.bear.evidence[0]={desc:'合成另一方的纯文字依据，保留自然文字内容。',series_id:null,metric:null};
sides.bear.evidence[1]={desc:'合成空方指标支持依据',series_id:metrics[2].series_id,metric:metrics[2]};
sides.bear.falsifiers.push({rule:{desc:'合成定性证伪条件'},condition:'定性变化待持续观察',state:'观察中',metric:null});
const radar=[metrics[0],metrics[2]].map((item,index)=>({id:`synthetic-risk-${index}`,name:'合成风险边界',series_id:item.series_id,type:'风控线',horizon:'短期',condition:'高于 22,000 元/吨',value:item.latest,unit:item.unit,as_of:item.date,distance:'高于边界',progress:100,state:'已触发',action_hint:'保持观察',note:null}));
const fixture={research:{sides,shared:[],pending_conditions:[],counts:{available:4,bound:4}},radar,names:Object.fromEntries(metrics.map(item=>[item.series_id,item.name])),
 j:{id:7,author:'合成研究者',written_at:'2032-03-05',review_due:'2032-03-12',status:'草稿',tone:'合成多空观察',thresholds:[{},{}],falsifiers:[{},{}],contradiction:{bull:{claim:'',evidence:[]},bear:{claim:'',evidence:[]}},priority_rule:null,whitelist:[]},
 block:{expired_days:0,blockers:[]},versions:[],d:'2032-03-05',restricted_view:true};
const render=spawnSync(process.env.RESEARCH_PYTHON||'python',['-c',`
import json,sys
from jinja2 import Environment,FileSystemLoader,DictLoader,ChoiceLoader,select_autoescape
host='''<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/fonts/noto-sans-sc/font.css"><style>:root{--text:#232323;--text-muted:#666;--surface:white;--line:#ddd;--line-strong:#bbb;--primary:#a34325;--amber:#a26616;--up:#a33;--down:#29885c}*{box-sizing:border-box}body{margin:0;padding:18px;font-family:Arial,'WeKnora Noto Sans SC',sans-serif}h3{font-size:14px}</style>{% block head %}{% endblock %}</head><body>{% block main %}{% endblock %}</body></html>'''
env=Environment(loader=ChoiceLoader([DictLoader({'base.html':host,'_restricted_entries.html':''}),FileSystemLoader(sys.argv[1])]),autoescape=select_autoescape(['html']))
env.filters['num']=lambda value,unit=None: str(value)
data=json.loads(sys.argv[2])
env.globals.update(human_text=lambda value,*args:str(value or ''),indicator_name=lambda value,*args:data['names'].get(value,'合成指标'),research_workbench=lambda:None,research_static_prefix=lambda:'',static_version=lambda:'fixture',research_url=lambda value:value)
print(env.get_template('judgment.html').render(**data))
`,path.join(web,'templates'),JSON.stringify(fixture)],{encoding:'utf8'});
assert.equal(render.status,0,render.stderr);
const html=render.stdout;
(async()=>{
 const browser=await chromium.launch({headless:true,...(process.env.SHEET_REVIEW_BROWSER?{executablePath:process.env.SHEET_REVIEW_BROWSER}:{})});
 const report={passed:false,checks:[],screenshots:[],page_errors:[]};
 try{
  const page=await browser.newPage({viewport:{width:1280,height:800}});page.setDefaultTimeout(10000);
  page.on('pageerror',error=>report.page_errors.push(error.message));
  await page.route('**/*',route=>{
   const url=new URL(route.request().url());
   if(url.origin!=='http://judgment-chart.fixture')throw new Error('Unexpected nonfixture request');
   if(url.pathname==='/')return route.fulfill({contentType:'text/html; charset=utf-8',body:html});
   if(url.pathname.startsWith('/fonts/')){
    const file=path.resolve(fonts,url.pathname.slice('/fonts/'.length));
    assert.ok(file.startsWith(fonts+path.sep),'Fixture fonts stay in the local font directory');
    return route.fulfill({contentType:file.endsWith('.css')?'text/css':'font/woff2',body:fs.readFileSync(file)});
   }
   if(url.pathname==='/static/judgment-research.css'&&process.env.JUDGMENT_CHART_TEST_CSS)return route.fulfill({contentType:'text/css',body:fs.readFileSync(process.env.JUDGMENT_CHART_TEST_CSS)});
   if(['/static/judgment-chart.js','/static/judgment-research.css','/static/judgment-manager.css','/static/research-display.js'].includes(url.pathname))return route.fulfill({contentType:url.pathname.endsWith('.js')?'application/javascript':'text/css',body:fs.readFileSync(path.join(web,url.pathname.slice(1)),'utf8')});
   throw new Error('Unexpected fixture request '+url.pathname);
  });
  await page.goto('http://judgment-chart.fixture/');
  await page.evaluate(()=>document.fonts.ready);
  const chart=page.locator('[data-judgment-chart]').first(),svg=chart.locator('svg');
  await page.locator('[data-research-item="evidence"]').first().waitFor();
  for(const side of['bull','bear']){
   const column=page.locator(`.judgment-hypothesis[data-hypothesis="${side}"]`);
   assert.equal(await column.locator('[data-research-item="evidence"]').count(),side==='bull'?3:2);
   assert.equal(await column.locator('[data-research-item="falsifier"]').count(),side==='bull'?1:2);
   const entry=column.locator('[data-research-item="evidence"]').filter({has:page.locator('[data-judgment-chart]')}).first();
   assert.equal(await entry.locator('[data-judgment-chart]').count(),1);
   assert.match(await entry.locator('[data-item-threshold]').innerText(),/合成风险边界.*高于 22,000.*已触发/s);
   const style=await entry.evaluate(el=>({border:getComputedStyle(el).borderTopStyle,radius:getComputedStyle(el).borderRadius,padding:getComputedStyle(el).padding}));
   assert.equal(style.border,'solid');assert.notEqual(style.radius,'0px');assert.notEqual(style.padding,'0px');
  }
  report.checks.push('Actual judgment page keeps unequal evidence/falsifier counts without fake cards, including text-only and chart-backed items with related thresholds');
  report.layouts=[];
  for(const width of[1280,960,390]){
   await page.setViewportSize({width,height:900});
   await page.waitForFunction(()=>[...document.querySelectorAll('[data-metric-trend]')].every(el=>Math.abs(el.viewBox.baseVal.width-el.getBoundingClientRect().width)<1));
   const layout=await page.evaluate(()=>{
    const box=el=>{const r=el.getBoundingClientRect();return{left:r.left,right:r.right,top:r.top,bottom:r.bottom,height:r.height}};
    return{width:innerWidth,scroll:document.documentElement.scrollWidth,
     columns:[...document.querySelectorAll('.judgment-hypothesis')].map(el=>{const style=getComputedStyle(el),headings=el.querySelectorAll(':scope>h4');return{
      ...box(el),borders:['Top','Right','Bottom','Left'].map(side=>({width:parseFloat(style['border'+side+'Width']),kind:style['border'+side+'Style']})),
      padding:parseFloat(style.paddingLeft),headingBorder:parseFloat(getComputedStyle(el.querySelector('h3')).borderTopWidth),
      title:box(el.querySelector('h3')),claim:box(el.querySelector('.judgment-hypothesis-claim')),support:box(headings[0]),falsifier:box(headings[1]),
      evidence:[...el.querySelectorAll('[data-research-item="evidence"]')].map(box),conditions:[...el.querySelectorAll('[data-research-item="falsifier"]')].map(box)};})};
   });
   assert.ok(layout.scroll<=width,`${width}px: no horizontal page overflow`);
   for(const column of layout.columns){
    assert.ok(column.borders.every(item=>item.width>0&&item.kind==='solid'),`${width}px: all four outer borders remain visible`);
    assert.ok(column.padding>=12,`${width}px: outer frame has padding`);
    assert.equal(column.headingBorder,0,`${width}px: heading does not repeat the outer color line`);
    assert.ok([...column.evidence,...column.conditions].every(card=>card.left-column.left>=12&&column.right-card.right>=12),`${width}px: inner cards do not touch the outer frame`);
   }
   const[left,right]=layout.columns;
   if(width>760){
    assert.ok(right.left>=left.right,`${width}px: two side columns remain separate`);
    const same=(a,b,what)=>{assert.ok(Math.abs(a.top-b.top)<1,`${width}px: ${what} top aligns`);assert.ok(Math.abs(a.height-b.height)<1,`${width}px: ${what} height aligns`);};
    for(const part of['title','claim','support','falsifier'])same(left[part],right[part],part);
    left.evidence.slice(0,right.evidence.length).forEach((card,index)=>same(card,right.evidence[index],`evidence ${index+1}`));
    same(left.conditions[0],right.conditions[0],'falsifier item');
    assert.ok(Math.abs(left.bottom-right.bottom)<1,`${width}px: side frames end together with unequal item counts`);
   }else{
    assert.ok(right.top>=left.bottom,'Mobile returns to one natural column');
    assert.ok(right.claim.height<left.claim.height,'Mobile short claim has its natural height');
    assert.ok(right.evidence[0].height<left.evidence[0].height,'Mobile text-only card is not forced to the chart card height');
   }
   await svg.focus();await page.keyboard.press('End');
   assert.ok(await chart.locator('[data-chart-axis-date]').count()>=2);
   assert.equal(await chart.locator('[data-chart-axis-value]').count(),3);
   assert.match(await chart.locator('[data-chart-tooltip]').innerText(),/2032-03-05.*MA20/s);
   const tip=await chart.locator('[data-chart-tooltip]').boundingBox();assert.ok(tip.x>=0&&tip.x+tip.width<=width,`${width}px: tooltip stays inside viewport`);
   report.layouts.push(layout);
   if(out){await page.evaluate(()=>document.fonts.ready);const file=path.join(out,`aligned-cards-${width}.png`);await page.screenshot({path:file,fullPage:true});report.screenshots.push(file);}
  }
  report.checks.push('1280px and 960px outer frames and corresponding title/claim/evidence/falsifier rows align and stretch equally; 390px returns to natural card heights');
  await page.setViewportSize({width:1280,height:800});
  await page.waitForFunction(()=>[...document.querySelectorAll('[data-metric-trend]')].every(el=>Math.abs(el.viewBox.baseVal.width-el.getBoundingClientRect().width)<1));
  const longLabel=page.locator('.judgment-metric-heading>span').filter({hasText:'合成指标长名称'}).first();
  assert.equal(await longLabel.getAttribute('title'),metrics[1].name);
  assert.equal(await longLabel.evaluate(el=>getComputedStyle(el).textOverflow),'ellipsis');
  await chart.locator('[data-chart-axis-date]').first().waitFor();
  assert.equal(await chart.locator('[data-chart-axis-date]').count(),3);
  assert.equal(await chart.locator('[data-chart-axis-value]').count(),3);
  assert.equal(await chart.locator('[data-chart-unit]').innerText(),'元/吨');
  assert.equal(await chart.locator('[data-chart-axis-date]').first().textContent(),timeline[0].date);
  assert.equal(await chart.locator('[data-chart-axis-date]').last().textContent(),timeline.at(-1).date);
  assert.match(await svg.getAttribute('aria-label'),/横轴日期.*纵轴数值.*元\/吨/);
  assert.equal(await chart.locator('[data-boundary-value="22000"]').count(),1);
  assert.match(await chart.locator('[data-chart-average="20"] title').first().textContent(),/下穿 MA20/);
  report.checks.push('Actual Jinja chart has dates, numeric ticks, unit, fixed threshold and dynamic crossing MA20');
  await svg.hover({position:{x:240,y:60}});
  await chart.locator('[data-chart-tooltip]:not([hidden])').waitFor();
  assert.match(await chart.locator('[data-chart-tooltip]').innerText(),/2032-.*元\/吨.*MA20/s);
  assert.match(await chart.locator('[data-chart-tooltip]').innerText(),/下穿 MA20/);
  assert.doesNotMatch(await chart.locator('[data-chart-tooltip]').innerText(),/crosses_below|NaN/);
  assert.equal(await chart.locator('[data-chart-crosshair]').getAttribute('visibility'),'visible');
  await chart.locator('[data-chart-ma]').selectOption('5');
  assert.ok(await chart.locator('[data-chart-average="5"]').count()>0);
  assert.ok(await chart.locator('[data-chart-average="20"]').count()>0,'Configured rule MA remains visible alongside an optional overlay');
  await svg.focus();await page.keyboard.press('End');
  assert.match(await chart.locator('[data-chart-tooltip]').innerText(),new RegExp(timeline.at(-1).date));
  assert.match(await chart.locator('[data-chart-tooltip]').innerText(),/MA5/);
  report.checks.push('Pointer hover, crosshair, keyboard date selection and optional MA5 preserve the rule MA20');
  if(out){await page.evaluate(()=>document.fonts.ready);const file=path.join(out,'chart-axes-desktop.png');await page.screenshot({path:file,fullPage:true});report.screenshots.push(file);}
  await page.setViewportSize({width:390,height:844});
  await page.waitForFunction(()=>document.querySelector('[data-metric-trend]').viewBox.baseVal.width<390);
  assert.equal(await chart.locator('[data-chart-axis-date]').count(),2);
  const geometry=await page.evaluate(()=>({document:document.documentElement.scrollWidth,viewport:innerWidth,
   cards:[...document.querySelectorAll('[data-research-item]')].map(card=>{const r=card.getBoundingClientRect();return{left:r.left,right:r.right}}),
   charts:[...document.querySelectorAll('[data-metric-trend]')].map(svg=>({width:svg.getBoundingClientRect().width,viewBox:svg.viewBox.baseVal.width,
    labels:[...svg.querySelectorAll('[data-chart-axis-date],[data-chart-axis-value]')].map(label=>{const r=label.getBoundingClientRect();return{left:r.left,right:r.right,width:r.width}})}))}));
  assert.ok(geometry.document<=geometry.viewport,'390px viewport has no horizontal overflow');
  assert.ok(geometry.cards.every(card=>card.left>=0&&card.right<=390),'Every research item card stays within the mobile viewport');
  for(const item of geometry.charts){
   assert.ok(Math.abs(item.width-item.viewBox)<1,'Axis font size is not horizontally compressed on mobile');
   assert.ok(item.labels.every(label=>label.left>=0&&label.right<=390),'Mobile tick labels stay inside the viewport');
  }
  await svg.focus();await page.keyboard.press('End');
  const tip=await chart.locator('[data-chart-tooltip]').boundingBox();assert.ok(tip.x>=0&&tip.x+tip.width<=390,'Tooltip remains inside the narrow viewport');
  assert.match(await chart.locator('[data-chart-tooltip]').innerText(),/MA5/);
  assert.equal(await chart.locator('[data-boundary-value="22000"]').count(),1);
  report.checks.push('390px layout reflows with readable axis glyphs, two dates, in-bounds tooltip and no horizontal overflow');
  if(out){await page.evaluate(()=>document.fonts.ready);const file=path.join(out,'chart-axes-mobile.png');await page.screenshot({path:file,fullPage:true});report.screenshots.push(file);}
  assert.deepEqual(report.page_errors,[]);report.passed=true;
  console.log('PASS: actual judgment metric template, axes dates/values/unit, pointer+keyboard hover, MA overlay, threshold lines and 390px layout; synthetic data only.');
 }catch(error){report.error=String(error.stack||error);throw error;}
 finally{if(out)fs.writeFileSync(path.join(out,'browser-report.json'),JSON.stringify(report,null,2));await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
