/* Isolated browser acceptance. No running service or business data is used. */
const {assert, render, launch, open, prefix, cycle} = require('./chart-ui-fixture.cjs');
const path = require('node:path'), {spawnSync} = require('node:child_process');
const date = '2029-09-15', unsafe = '<img src=x onerror="window.__xss=1">';
const setupJob = {id:'setup-fixture-review',purpose:'workbench_setup',workbench_id:'fixture',state:'succeeded'};
const categories = [{id:'核心标的',label:'价格与价差'},...['供给','需求','库存','平衡','宏观','其他'].map(id => ({id,label:id}))];
function defaults() { return ['price','supply','demand','inventory','balance','macro'].map((id,i) => ({id,label:categories[i].label,category:categories[i].id,description:'合成维度说明',sections:[],references:{support:0,oppose:0,total:0}})); }
const indicator = id => ({kind:'indicator',id,name:id === 'kb-1' ? unsafe : '第二项指标',unit:'吨',frequency:'weekly',source:'合成来源',sheet:'合成工作表'});
function initial(populated = false) {
  const dimensions = defaults();
  if (populated) {
    dimensions[0].sections = [{id:'s-first',label:'现货',items:[indicator('kb-1')]},{id:'s-second',label:'价差',items:[]}];
    dimensions[0].references = {support:2,oppose:1,total:3};
  }
  return {dimensions,revision:populated ? 1 : 0,can_write:true,categories,evidence:{unassigned:{support:1,oppose:1,total:2},deleted:{support:0,oppose:0,total:0}}};
}
function observations(config) {
  const observed = i => i.kind === 'chart'
    ? {...i,status:'ok',name:'合成图表',definition:{title:'合成图表',time:{mode:'cycle'}},payload:cycle()}
    : {...indicator(i.id),...i,status:'ok',latest:25,date:'2029-09-14',change:1.5,change_pct:.06,direction:'up',baseline:{label:'较 09-07',date:'2029-09-07'},display:{latest:'25',change:'+1.5',change_pct:'+6%'},position:{n:4,rank:4,rank_low:2,text:'近 5 年同期第 2 低'},freshness:{age_days:8,stale:true},points:[{date:'2029-09-07',value:23.5},{date:'2029-09-14',value:25}],...(config.card_overrides || {}),...(config.no_update ? {direction:'no_update',update_status:'no_update',update_notice:'本期无新数据（最新 2029-09-14）'} : {})};
  const dimensions = config.dimensions.map(d => ({...d,summary:{up:config.no_update ? 0 : 1,down:0,flat:0,no_update:config.no_update ? 1 : 0,extreme:1},sections:d.sections.map(s => ({...s,items:s.items.map(observed)}))}));
  return {as_of:date,latest_data_date:date,period:'weekly',failed:[],warnings:config.warnings || ['history_limited'],history_coverage_limited:config.history_coverage_limited,dimensions,judgment_id:config.judgment_id};
}
function html(boot = {}, setupReview = false) {
  const scripts = ['app','catalog-client',...(setupReview ? ['workbench-setup-review'] : [])];
  return render('dimensions.html',{}, {dimension_page:{date,...boot}})
    .replace('</head>',scripts.map(script => `<script defer src="/static/${script}.js"></script>`).join('') + '</head>')
    .replace('</body>','<div id="drawer-backdrop" hidden></div></body>');
}
// Use the real built-in definitions and merge implementation; no HTTP service is started.
function templateResult(input) {
  const result = spawnSync(process.env.RESEARCH_PYTHON,['-c',String.raw`
import json,sys
from fastapi import HTTPException
from tin.web.dimension_templates import template_list,merge_template
body=json.load(sys.stdin)
try:
    result={'templates':template_list()} if body['operation']=='list' else {'dimensions':merge_template(body['id'],body['previous'])}
    print(json.dumps({'status':200,'result':result},ensure_ascii=False))
except HTTPException as error:
    print(json.dumps({'status':error.status_code,'result':{'detail':error.detail}},ensure_ascii=False))
`],{input:JSON.stringify(input),encoding:'utf8'});
  assert.equal(result.status,0,result.stderr); return JSON.parse(result.stdout);
}
async function fixture(browser, config = initial(), boot = {}, pathname = '/sn/dimensions', options = {}) {
  let stored = structuredClone(config), conflict = false;
  const jobs = new Map();
  const view = await open(browser,pathname,html(boot,options.setupReview),({path,method,body,reply,url}) => {
    if (path === '/dimensions' && method === 'GET') return reply(stored);
    if (path === '/dimensions' && method === 'PUT') {
      if (conflict) return reply({detail:'配置已更新，请刷新后重试'},409);
      assert.equal(body.revision,stored.revision);
      stored = {...stored,revision:stored.revision + 1,dimensions:body.dimensions.map((d,i) => ({...d,id:d.id || 'd-new0000'+i,references:{support:0,oppose:0,total:0}}))};
      return reply(stored);
    }
    if (path === '/dimensions/data') return reply(observations(stored));
    if (path === '/dimensions/templates') return reply(templateResult({operation:'list'}).result);
    if (path.startsWith('/dimensions/templates/') && path.endsWith('/draft')) {
      assert.equal(body.revision,stored.revision);
      const id = path.split('/')[3], previous = body.dimensions ?? (stored.revision ? stored.dimensions : []);
      const result = templateResult({operation:'merge',id,previous});
      if (result.status !== 200) return reply(result.result,result.status);
      return reply({...stored,...result.result,template:{id,name:id === 'general-six' ? '通用六维度' : '纺织产业链'},dimensions:result.result.dimensions.map(d => ({...d,references:stored.dimensions.find(old => old.id === d.id)?.references || {total:0,support:0,oppose:0}}))});
    }
    if (path === '/indicators/catalog') return reply({indicators:[{series_id:'kb-1',name:unsafe,unit:'吨',source:'合成来源'},{series_id:'kb-2',name:'第二项指标',unit:'吨'}],total:2,offset:0,next_offset:null});
    if (path === '/charts') return reply({items:[{id:'chart-1',revision:3,definition:{title:'合成图表',group:'合成分组'}}],total:1,next_offset:null});
    if (path === '/strategies/models') return reply({models:[{id:'model-1',name:'合成模型'}]});
    const submit = (purpose, result) => {
      const id = 'dimension-fixture-job-' + (jobs.size + 1);
      const job = {id, purpose, workbench_id:'fixture', state:'succeeded'};
      jobs.set(id,{...result,job,request:body});
      return reply({...job,state:'queued'},202);
    };
    if (path === '/analysis-jobs/latest') return reply({job: null});
    if (options.setupReview && path === '/analysis-jobs/' + setupJob.id + '/result') {
      return reply({job:setupJob,items:[{part:'dimensions',state:'succeeded',data:{dimensions:defaults()}}]});
    }
    if (path.startsWith('/analysis-jobs/')) {
      const [, , id, result] = path.split('/');
      return reply(result ? jobs.get(id) : jobs.get(id).job);
    }
    if (path === '/dimensions/suggestions') {
      if (stored.duplicate_sections) {
        const dimension = (body.dimensions || stored.dimensions)[0], sections = dimension.sections.map((s, index) => ({dimension:dimension.id,section:s.label,section_id:s.id,item_id:'i'+(index+1),candidate_count:10+index,selected_count:1,empty_reason:''}));
        return submit('dimension_fields',{sections,items:sections.map((s,index) => ({...s,...(index ? {section_id:undefined} : {}),series_id:'kb-'+(index+2),name:'环节指标'+(index+1),reason:'合成选择理由',unit:'吨'}))});
      }
      const dimensions = body.dimensions || stored.dimensions, dimension = stored.suggested_dimension || dimensions[1].id;
      const section = dimensions[1].sections[0]?.label || '';
      const sections = dimensions.flatMap(d => (d.sections.length ? d.sections : [{label:''}]).map(s => {
        const picked = d.id === dimension && s.label === section;
        return {dimension:d.id,section:s.label,candidate_count:20,selected_count:picked ? 1 : 0,empty_reason:picked ? '' : '没有口径一致的候选 ' + unsafe};
      }));
      return submit('dimension_fields',{as_of:body.date,sections,ignored_count:3,ignored_reason:'模型返回 3 个不在候选目录的指标，已忽略',
        items:[{series_id:'kb-2',dimension,section,reason:unsafe,name:'第二项指标',unit:'吨',sheet:'合成候选工作表'}]});
    }
    if (path === '/dimensions/sources') return reply({items:[{title:'合成往期周报',citation:{kind:'knowledge_file',field:'source_quote',source_binding_id:'source-1',knowledge_id:'knowledge-1',file_version:'a'.repeat(64)}}],next_offset:null});
    if (path === '/dimensions/infer') {
      assert.equal(body.sources.length,1); assert.equal(body.sources[0].knowledge_id,'knowledge-1');
      return submit('dimension_inference',{dimensions:[{id:'d-inferred',label:'产业链',category:'其他',description:'由所选原文归纳',sections:[{id:'s-inferred',label:'加工',items:[]}]}]});
    }
    if (/^\/indicators\/[^/]+\/cycle$/.test(path)) {
      return options.cycleRequest ? options.cycleRequest({url,reply}) : reply(cycle());
    }
    return false;
  },options);
  return {...view,get stored(){return stored;},setConflict(value){conflict=value;}};
}
function navigation(restricted) {
  const rendered = spawnSync(process.env.RESEARCH_PYTHON,['-c',String.raw`
import sys
from types import SimpleNamespace
from jinja2 import Environment,FileSystemLoader,select_autoescape
restricted=sys.argv[2]=='true'
scope=SimpleNamespace(scope_version=2,restricted=restricted,metadata=SimpleNamespace(name='导航夹具'),view_modules=('indicators',))
env=Environment(loader=FileSystemLoader(sys.argv[1]),autoescape=select_autoescape(['html']))
env.globals.update(research_identity=lambda:None,research_workbench=lambda:scope,research_prefix=lambda:'',research_url=lambda s:s,research_static_prefix=lambda:'',static_version=lambda:'fixture')
env.filters['num']=lambda value,*args:str(value)
print(env.get_template('base.html').render(nav='dimensions',restricted_view=restricted,d=None,ticker=[]))
`,path.join(__dirname,'../src/tin/web/templates'),String(restricted)],{encoding:'utf8'});
  assert.equal(rendered.status,0,rendered.stderr); return rendered.stdout;
}
async function checkPrimaryButton(browser) {
  const view = await fixture(browser,initial(),{},`/sn/dimensions?setup_job=${setupJob.id}&setup_part=dimensions`,{setupReview:true});
  const page = view.page, dialog = page.getByRole('dialog',{name:'核对周报研究维度'});
  const button = dialog.getByRole('button',{name:'将这套维度作为编辑草稿'});
  await button.waitFor();
  const colors = () => button.evaluate(element => {
    const style = getComputedStyle(element), probe = document.createElement('span');
    element.append(probe);
    const resolve = token => {
      probe.style.color = `var(${token})`;
      return getComputedStyle(probe).color;
    };
    const result = {background:style.backgroundColor,foreground:style.color,border:style.borderTopColor,
      primary:resolve('--primary'),hover:resolve('--primary-hover'),onPrimary:resolve('--on-primary'),soft:resolve('--surface-soft')};
    probe.remove(); return result;
  });
  const luminance = rgb => rgb.match(/\d+/g).slice(0,3).map(Number).map(channel => {
    const value = channel / 255;
    return value <= .04045 ? value / 12.92 : ((value + .055) / 1.055) ** 2.4;
  }).reduce((sum, channel, index) => sum + channel * [.2126,.7152,.0722][index],0);
  for (const theme of ['light','dark']) {
    await page.emulateMedia({colorScheme:theme});
    await page.waitForFunction(mode => document.documentElement.getAttribute('theme-mode') === mode,theme);
    const normal = await colors();
    assert.equal(normal.foreground,normal.onPrimary,`${theme} normal text follows the primary text color`);
    assert.equal(normal.background,normal.primary,`${theme} normal background remains primary`);
    for (const state of ['hover','focus-visible']) {
      if (state === 'hover') await button.hover();
      else {
        await page.mouse.move(0,0);
        await dialog.getByRole('button',{name:'关闭',exact:true}).focus();
        await page.keyboard.press('Tab');
        assert.equal(await button.evaluate(element => element.matches(':focus-visible')),true);
      }
      const {background,foreground,border,hover,onPrimary,soft} = await colors();
      assert.notEqual(background,soft,`${theme} ${state} avoids the surface hover color`);
      assert.equal(background,hover,`${theme} ${state} background follows the primary hover color`);
      assert.equal(border,hover,`${theme} ${state} border follows the primary hover color`);
      assert.equal(foreground,onPrimary,`${theme} ${state} text follows the primary text color`);
      const values = [luminance(foreground),luminance(background)];
      assert.ok((Math.max(...values) + .05) / (Math.min(...values) + .05) >= 4.5,`${theme} ${state} text contrast is at least 4.5`);
    }
    await dialog.getByRole('button',{name:'关闭',exact:true}).focus();
  }
  await view.close();
}
async function checkViewCards(browser) {
  const config = initial(true), longName = '合成产线：开机率（周） / 周 / ' + '表头中的指标口径与样本说明。'.repeat(15);
  const note = {cell:'B1',text:'如实保留表头中的样本说明。'};
  const stage = (id, label, ...items) => ({id,label,items});
  config.dimensions = [{...config.dimensions[0],sections:[
    stage('s-first','现货',{...indicator('kb-1'),name:longName,field_metadata:{header_notes:[note]}}),
    stage('s-chart','趋势图',{kind:'chart',id:'chart-1',revision:3}),
    stage('s-single','单条目',indicator('kb-2')),
    stage('s-multi','多条目',indicator('kb-3'),indicator('kb-4'),indicator('kb-5')),
    stage('s-after','后续环节',indicator('kb-6')),
    stage('s-last','末尾环节',indicator('kb-7')),
    stage('s-empty','空环节')
  ]}];
  const scope = series => JSON.stringify([17,'fixture-user','fixture',series]);
  const judgmentKey = 'tin.judgment.chart.v1:' + scope('kb-7');
  const requestGate = Promise.withResolvers();
  let active = 0, peak = 0, pauseFirst = true;
  const view = await fixture(browser,config,{},'/sn/dimensions',{
    storage:{[judgmentKey]:{view:'cycle',semantics:'inventory'}},
    async cycleRequest({url,reply}) {
      active++; peak = Math.max(peak,active);
      try {
        if (pauseFirst && url.pathname.endsWith('/kb-1/cycle')) await requestGate.promise;
        else await new Promise(resolve => setTimeout(resolve,120));
        if (url.pathname.endsWith('/kb-7/cycle')) return await reply({detail:'合成同期读取失败原因'},503);
        return await reply(cycle());
      } finally { active--; }
    }
  });
  const page = view.page, stages = page.locator('.dimensions-stage'), cards = page.locator('.dimensions-card');
  const first = cards.first(), chart = cards.filter({has:page.locator('.dimensions-chart-plot')});
  const trend = first.locator('[data-metric-trend]'), title = first.locator('h4 a');
  const requests = () => view.calls.filter(call => call.path.endsWith('/cycle'));
  const near = (a,b) => Math.abs(a-b) < 1;
  try {
    await trend.locator('[data-chart-axis-date]').first().waitFor();
    assert.deepEqual(await stages.locator('h3').allTextContents(),['现货','趋势图','单条目','多条目','后续环节','末尾环节','空环节']);
    const rows = await Promise.all((await stages.all()).map(row => row.boundingBox()));
    assert.ok(near(rows[0].y,rows[1].y) && rows[1].x >= rows[0].x + rows[0].width,'Adjacent single-item stages share a row');
    assert.ok(near(rows[3].x,rows[0].x) && near(rows[3].x + rows[3].width,rows[1].x + rows[1].width),'The multi-item stage spans both columns');
    assert.ok(near(rows[2].x,rows[0].x) && rows[4].y >= rows[3].y + rows[3].height,'Later stages preserve order and leave the gap before the multi-item stage');
    const multi = stages.nth(3).locator('.dimensions-card');
    const [left,right,third] = await Promise.all((await multi.all()).map(card => card.boundingBox()));
    assert.ok(near(left.y,right.y) && right.x >= left.x + left.width && near(third.x,left.x) && third.y >= left.y + left.height);
    assert.ok(near(await chart.evaluate(node => node.getBoundingClientRect().width),await first.evaluate(node => node.getBoundingClientRect().width)));
    assert.match(await chart.locator('h4 a').getAttribute('href'),/\/sn\/charts\/chart-1\?date=2029-09-15$/);
    assert.equal(await chart.locator('[data-chart-view]').count(),0);
    const chartHeight = await chart.locator('.dimensions-chart-plot svg').evaluate(node => node.getBoundingClientRect().height);
    assert.ok(chartHeight >= 210 && chartHeight <= 240,`Chart library plot has card height, got ${chartHeight}px`);
    await page.setViewportSize({width:800,height:900});
    const stacked = await Promise.all((await stages.all()).map(row => row.boundingBox()));
    assert.ok(stacked.every((row,index) => near(row.x,stacked[0].x) && (!index || row.y >= stacked[index - 1].y + stacked[index - 1].height)));
    const [top,bottom] = await Promise.all([multi.nth(0).boundingBox(),multi.nth(1).boundingBox()]);
    assert.ok(near(top.x,bottom.x) && bottom.y >= top.y + top.height);
    await page.setViewportSize({width:1280,height:900});

    assert.equal(await first.locator('h4').getAttribute('title'),longName);
    assert.equal(await title.textContent(),longName);
    assert.match(await title.getAttribute('href'),/\/sn\/indicators\/kb-1\?date=2029-09-15$/);
    assert.equal(await first.locator('a').count(),1);
    assert.equal(await title.evaluate(element => {
      const style = getComputedStyle(element);
      return style.webkitLineClamp === '2' && element.clientHeight <= 2 * parseFloat(style.lineHeight) + 1
        && element.scrollHeight > element.clientHeight;
    }),true,'Long names render at most two lines while retaining the full text');
    await first.locator('.dimensions-info-trigger').hover();
    const info = first.locator('.dimensions-tooltip'); await info.waitFor();
    assert.ok((await info.innerText()).includes(longName));
    assert.equal(await info.locator('.header-note-text').innerText(),note.text);
    await page.mouse.move(0,0);
    assert.equal(await first.locator('[data-chart-ma], [data-chart-semantics]').count(),0);
    assert.deepEqual(await first.locator('[data-chart-view]').allTextContents(),['走势','同期']);
    assert.deepEqual(await trend.locator('[data-chart-axis-date]').allTextContents(),['2029-09-07','2029-09-14']);
    assert.ok(await trend.locator('[data-chart-axis-value]').count() >= 2);
    assert.equal(await first.locator('[data-chart-unit]').innerText(),'吨');
    assert.equal(await trend.evaluate(node => getComputedStyle(node).height),'156px');
    await trend.hover();
    await first.locator('[data-chart-tooltip]').waitFor();
    assert.equal(await trend.locator('[data-chart-crosshair]').getAttribute('visibility'),'visible');
    assert.equal(requests().length,0,'The trend, info panel and judgment preference make no cycle request');

    const cycleTab = first.getByRole('tab',{name:'同期'}), trendTab = first.getByRole('tab',{name:'走势'});
    await cycleTab.click();
    await first.locator('[data-chart-cycle]').getByText('正在读取往年同期…').waitFor();
    await cycleTab.click();
    requestGate.resolve(); pauseFirst = false;
    const cyclePlot = first.locator('[data-chart-cycle] svg');
    await cyclePlot.waitFor();
    assert.equal(await trend.isVisible(),false);
    const cycleHeight = await cyclePlot.evaluate(node => node.getBoundingClientRect().height);
    assert.ok(cycleHeight >= 240 && cycleHeight <= 300,`Cycle plot fits the card, got ${cycleHeight}px`);
    await trendTab.click(); await trend.waitFor(); await cycleTab.click(); await cyclePlot.waitFor();
    assert.equal(requests().length,1,'Repeated selection uses the same in-flight or rendered cycle');
    assert.equal(requests()[0].url.searchParams.get('date'),date);

    for (const series of ['kb-2','kb-3','kb-4','kb-5','kb-6']) await page.locator('#dimension-indicator-' + series).getByRole('tab',{name:'同期'}).click();
    await page.waitForFunction(() => document.querySelectorAll('[data-chart-cycle]:not([hidden]) svg').length === 6);
    assert.equal(requests().length,6);
    assert.deepEqual(await page.evaluate(key => JSON.parse(localStorage.getItem(key)), 'tin.dimensions.chart.v1:' + scope('kb-1')),{view:'cycle'});
    assert.deepEqual(await page.evaluate(key => JSON.parse(localStorage.getItem(key)),judgmentKey),{view:'cycle',semantics:'inventory'});
    peak = 0;
    await page.reload({waitUntil:'domcontentloaded'});
    await page.waitForFunction(() => document.querySelectorAll('[data-chart-cycle]:not([hidden]) svg').length === 6);
    assert.equal(await page.locator('[data-chart-view="cycle"][aria-selected="true"]').count(),6);
    assert.equal(requests().length,12);
    assert.ok(peak >= 1 && peak <= 4,`Restoring six preferences started at most four requests at a time, got ${peak}`);
    const seventh = page.locator('#dimension-indicator-kb-7');
    assert.equal(await seventh.getByRole('tab',{name:'走势'}).getAttribute('aria-selected'),'true');
    await seventh.getByRole('tab',{name:'同期'}).click();
    await seventh.locator('[data-chart-cycle]').getByText('合成同期读取失败原因').waitFor();
    await seventh.getByRole('tab',{name:'走势'}).click();

    const pendingDate = '2029-09-16', nextDate = '2029-09-17';
    await page.evaluate(pending => {
      const fetch = window.researchFetch;
      window.pendingCycleSignals = [];
      window.researchFetch = (url, options) => {
        const request = new URL(url,location.href);
        if (!request.pathname.endsWith('/cycle') || request.searchParams.get('date') !== pending) return fetch(url,options);
        window.pendingCycleSignals.push(options?.signal);
        return new Promise((_,reject) => options?.signal?.addEventListener('abort',event => reject(event.target.reason),{once:true}));
      };
    },pendingDate);
    const setDate = value => page.locator('#dimensions-date').evaluate((input,date) => {
      input.value = date; input.dispatchEvent(new Event('change'));
    },value);
    await setDate(pendingDate);
    await page.waitForFunction(() => window.pendingCycleSignals.length >= 1);
    await setDate(nextDate);
    await page.waitForFunction(() => document.querySelectorAll('[data-chart-cycle]:not([hidden]) svg').length === 6);
    const stale = await page.evaluate(() => window.pendingCycleSignals.map(signal => signal?.aborted === true));
    assert.ok(stale.length <= 4 && stale.every(Boolean),'Changing date cancels in-flight cycle reads and discards the previous render queue');
    assert.equal(requests().filter(call => call.url.searchParams.get('date') === nextDate).length,6);
  } finally { requestGate.resolve(); await view.close(); }
}
(async () => {
  const browser = await launch();
  try {
    await checkPrimaryButton(browser);
    await checkViewCards(browser);
    const empty = await fixture(browser), p = empty.page;
    const nav = await p.evaluate(html => [...new DOMParser().parseFromString(html,'text/html').querySelectorAll('.research-nav-links a')].map(a => a.textContent.trim()),navigation(false));
    assert.equal(nav.indexOf('研究维度'),nav.indexOf('指标')+1); assert.equal(nav.indexOf('研究判断'),nav.indexOf('研究维度')+1);
    const restrictedNav = await p.evaluate(html => [...new DOMParser().parseFromString(html,'text/html').querySelectorAll('.research-nav-links a')].map(a => a.textContent.trim()),navigation(true));
    assert.ok(restrictedNav.includes('研究维度')); assert.equal(restrictedNav.includes('市场总览'),false);
    assert.equal(await p.locator('#dimensions-start').isVisible(),true);
    for (const entry of ['general','industry','infer']) assert.equal(await p.locator('#dimensions-start-'+entry).isVisible(),true);
    assert.equal(await p.locator('.dimensions-section:visible').count(),0);
    await p.locator('#dimensions-start-general').click();
    await p.locator('#dimensions-edit-actions').waitFor({state:'visible'});
    assert.equal(empty.calls.filter(c => c.method === 'PUT').length,0);
    assert.deepEqual(await p.locator('.dimensions-section').evaluateAll(rows => rows.map(row => row.dataset.dimensionId)),['price','supply','demand','inventory','balance','macro']);
    await p.locator('#dimensions-cancel').click();
    await p.locator('#dimensions-edit').click();
    await p.locator('#dimensions-add').click();
    const last = p.locator('.dimensions-section').last(); await last.getByRole('textbox',{name:'维度名称',exact:true}).fill('棉花链');
    await last.getByLabel('通用类别').selectOption('其他'); await last.getByRole('button',{name:'增加环节',exact:true}).click();
    await last.getByRole('textbox',{name:'环节名称',exact:true}).fill('加工');
    await last.getByRole('button',{name:'添加条目',exact:true}).click();
    await p.locator('#dimensions-picker-list .dimensions-choice').nth(0).locator('input').check();
    await p.locator('#dimensions-picker-kind').selectOption('chart');
    await p.locator('#dimensions-picker-list .dimensions-choice input').check();
    await p.locator('#dimensions-picker-close').click();
    await p.locator('#dimensions-save').click();
    await p.locator('.dimensions-section h2').filter({hasText:'棉花链'}).waitFor();
    assert.equal(empty.stored.dimensions.at(-1).sections[0].items.length,2);
    assert.deepEqual(empty.stored.dimensions.at(-1).sections[0].items[1],{kind:'chart',id:'chart-1',revision:3});
    await p.locator('.dimensions-chart-plot svg').waitFor();
    assert.match(await p.locator('.dimensions-card').first().innerText(),/25[\s\S]*较 09-07 \+1.5[\s\S]*2029-09-14[\s\S]*近 5 年同期第 2 低[\s\S]*停更 8 天/);
    assert.equal(await p.locator('.dimensions-card h4').first().innerText(),unsafe);
    assert.equal(await p.locator('.dimensions-card .dimensions-sheet').first().textContent(),'工作表：合成工作表');
    assert.doesNotMatch(await p.locator('.dimensions-card-main').first().innerText(),/合成来源|合成工作表|weekly/);
    assert.equal(await p.locator('.judgment-metric-value > span').first().innerText(),'吨');
    const firstInfo = p.locator('.dimensions-info-trigger').first(), firstTip = p.locator('.dimensions-tooltip').first();
    assert.equal(await firstTip.isVisible(),false);
    await firstInfo.hover(); await firstTip.waitFor({state:'visible'});
    assert.match(await firstTip.innerText(),/频率：weekly[\s\S]*来源：合成来源[\s\S]*工作表：合成工作表/);
    const originalURL = p.url(); await firstInfo.click(); await p.mouse.move(0,0);
    assert.equal(await firstTip.isVisible(),true); assert.equal(p.url(),originalURL);
    await p.keyboard.press('Escape'); assert.equal(await firstTip.isVisible(),false);
    assert.equal(await p.locator('.dimensions-section img').count(),0);
    assert.equal(await p.evaluate(() => window.__xss),undefined);
    assert.equal(await p.locator('[data-metric-trend]').count(),1);
    assert.match(await p.locator('#dimensions-status').innerText(),/历史观测已达到读取上限/);
    assert.equal((await p.locator('#dimensions-status').innerText()).includes('history_limited'),false);
    // Model suggestions create an editable draft and never save automatically.
    const saved = empty.calls.filter(c => c.method === 'PUT').length;
    await p.locator('#dimensions-suggest').click(); await p.locator('#dimensions-model-run').click();
    await p.locator('#dimensions-model-adopt:not([disabled])').waitFor();
    assert.equal(await p.locator('#dimensions-suggestions > :first-child').innerText(),'模型返回 3 个不在候选目录的指标，已忽略');
    assert.equal(await p.locator('#dimensions-suggestions .dimensions-suggestion-reason').innerText(),unsafe);
    assert.match(await p.locator('.dimensions-recall-summary').first().innerText(),/价格与价差 · 候选 20 · 已选 0 · 留空原因：没有口径一致的候选/);
    assert.equal(await p.locator('.dimensions-empty-reason').first().innerText(),' · 留空原因：没有口径一致的候选 ' + unsafe);
    assert.equal(await p.locator('#dimensions-suggestions .dimensions-sheet').innerText(),'合成候选工作表');
    assert.equal(await p.locator('#dimensions-suggestions img').count(),0);
    assert.equal(empty.calls.find(c => c.path === '/dimensions/suggestions').body.date,date);
    await p.locator('#dimensions-suggestions input[type=text]').fill('新环节');
    await p.locator('#dimensions-model-adopt').click();
    assert.equal(empty.calls.filter(c => c.method === 'PUT').length,saved);
    assert.equal(await p.locator('.dimensions-edit-card').count(),3);
    await p.locator('#dimensions-cancel').click();
    // Inferred dimensions require an explicit draft adoption and a later save.
    await p.locator('#dimensions-edit').click(); await p.locator('#dimensions-infer').click();
    await p.locator('#dimensions-source-list input').check(); await p.locator('#dimensions-model-run').click();
    await p.locator('#dimensions-model-adopt:not([disabled])').waitFor();
    p.once('dialog', dialog => dialog.dismiss()); await p.locator('#dimensions-model-adopt').click();
    assert.equal(await p.locator('#dimensions-model-dialog').isVisible(),true);
    p.once('dialog', dialog => dialog.accept()); await p.locator('#dimensions-model-adopt').click();
    assert.equal(await p.getByRole('textbox',{name:'维度名称',exact:true}).inputValue(),'产业链');
    assert.equal(empty.calls.filter(c => c.method === 'PUT').length,saved);
    await p.locator('#dimensions-save').click(); await p.locator('.dimensions-section h2').filter({hasText:'产业链'}).waitFor();
    await empty.close();

    // An unchanged stale source has its own state while retaining seasonal position and freshness.
    const stoppedConfig = initial(true); stoppedConfig.no_update = true;
    stoppedConfig.dimensions[0].sections[0].items[0] = {...indicator('kb-1'),name:'可读指标名称',metric:'数据来源：某机构 / 原始技术列',sheet:unsafe};
    const stopped = await fixture(browser,stoppedConfig), n = stopped.page;
    await n.locator('.dimensions-change.no_update').waitFor();
    assert.equal(await n.locator('.dimensions-card h4').innerText(),'可读指标名称');
    assert.equal(await n.locator('.dimensions-card .dimensions-sheet').textContent(),'工作表：' + unsafe);
    assert.equal(await n.locator('.dimensions-change').innerText(),'本期无新数据');
    assert.match(await n.locator('.dimensions-tooltip').textContent(),/本期无新数据（最新 2029-09-14）/);
    assert.match(await n.locator('.dimensions-summary').first().innerText(),/↑0 ↓0 · 持平 0 · 本期无新数据 1/);
    assert.match(await n.locator('.dimensions-card').innerText(),/近 5 年同期第 2 低[\s\S]*停更 8 天/);
    assert.equal(await n.locator('.dimensions-card img').count(),0);
    await n.locator('#dimensions-date').fill('2029-09-18'); await n.locator('#dimensions-date').dispatchEvent('change');
    await n.locator('#dimensions-suggest').click(); await n.locator('#dimensions-model-run').click();
    await n.locator('#dimensions-model-adopt:not([disabled])').waitFor();
    assert.equal(stopped.calls.find(c => c.path === '/dimensions/suggestions').body.date,'2029-09-18');
    await stopped.close();

    // Coverage explanations are deduplicated in the info panel; the compact body stays readable.
    const noisyConfig = initial(true), sampleNotice = '有效往期同期样本不足3年，不提供排名';
    noisyConfig.warnings = ['history_limited','observation_window_limited','history_limited','empty_segments_omitted','insufficient_cycles'];
    noisyConfig.history_coverage_limited = true;
    noisyConfig.card_overrides = {unit:'未注明',source:'合成机构',source_file:unsafe,sheet:'提示工作表',
      position:{n:1,rank:null,notice:sampleNotice,text:sampleNotice},history_coverage:{limited:true},
      warnings:['insufficient_cycles','insufficient_cycles','empty_segments_omitted','history_limited','observation_window_limited','baseline_unavailable','baseline_unavailable']};
    const noisy = await fixture(browser,noisyConfig), z = noisy.page;
    assert.equal(await z.locator('.dimensions-position').innerText(),'—');
    assert.equal(await z.locator('.judgment-metric-value > span').count(),0);
    const compact = await z.locator('.dimensions-card-main').innerText();
    assert.doesNotMatch(compact,/未注明|样本不足|没有观测|读取上限|合成机构|提示工作表/);
    assert.equal((compact.match(/停更/g) || []).length,1);
    const warning = await z.locator('#dimensions-status').innerText();
    assert.equal((warning.match(/读取上限/g) || []).length,1);
    assert.doesNotMatch(warning,/样本不足|没有观测|insufficient_cycles/);
    const detailButton = z.locator('.dimensions-info-trigger'), detail = z.locator('.dimensions-tooltip');
    await detailButton.focus(); await detail.waitFor({state:'visible'});
    const notes = (await detail.locator('p:not(.dimensions-info-name)').allTextContents()).join('\n');
    for (const text of ['样本不足3年','没有观测的历史周期已省略','读取上限','缺少可比的往期观测','单位未注明','合成机构','提示工作表',unsafe]) assert.equal(notes.split(text).length - 1,1,text);
    assert.equal(await detail.locator('img').count(),0);
    await z.keyboard.press('Enter'); await z.mouse.move(0,0); assert.equal(await detail.isVisible(),true);
    await z.locator('.dimensions-stage h3').first().click(); assert.equal(await detail.isVisible(),false);
    // The info panel stays inside the viewport in both multi-column and phone layouts.
    for (const width of [800,360]) {
      await z.setViewportSize({width,height:900}); await detailButton.click();
      const bounds = await detail.boundingBox(); assert.ok(bounds.x >= 7 && bounds.x + bounds.width <= width - 7);
      await z.keyboard.press('Escape');
    }
    await noisy.close();

    // A capped raw read that already covers five past cycles needs no coverage warning.
    const coveredConfig = initial(true); coveredConfig.history_coverage_limited = false;
    coveredConfig.card_overrides = {limited:true,history_coverage:{limited:false},warnings:['history_limited','observation_window_limited','empty_segments_omitted']};
    const covered = await fixture(browser,coveredConfig), c = covered.page;
    assert.equal(await c.locator('#dimensions-status').innerText(),'');
    assert.doesNotMatch(await c.locator('.dimensions-tooltip').textContent(),/读取上限/);
    assert.match(await c.locator('.dimensions-tooltip').textContent(),/没有观测的历史周期已省略/);
    await covered.close();

    // Same-label sections retain separate targets; item_id also resolves an optional section_id.
    const duplicateConfig = {...initial(),revision:1,duplicate_sections:true};
    duplicateConfig.dimensions = [{...duplicateConfig.dimensions[0],sections:[{id:'s-duplicate1',label:'同名环节',items:[]},{id:'s-duplicate2',label:'同名环节',items:[]}]}];
    const duplicate = await fixture(browser,duplicateConfig), d = duplicate.page;
    await d.locator('#dimensions-suggest').click(); await d.locator('#dimensions-model-run').click();
    await d.locator('#dimensions-model-adopt:not([disabled])').waitFor();
    assert.match(await d.locator('.dimensions-recall-summary[data-section-id="s-duplicate1"]').innerText(),/候选 10 · 已选 1/);
    assert.match(await d.locator('.dimensions-recall-summary[data-section-id="s-duplicate2"]').innerText(),/候选 11 · 已选 1/);
    await d.locator('#dimensions-model-adopt').click();
    assert.equal(await d.locator('.dimensions-stage-editor').nth(0).locator('.dimensions-edit-card').getAttribute('data-item-id'),'kb-2');
    assert.equal(await d.locator('.dimensions-stage-editor').nth(1).locator('.dimensions-edit-card').getAttribute('data-item-id'),'kb-3');
    assert.equal(duplicate.calls.filter(c => c.method === 'PUT').length,0);
    await duplicate.close();

    // All cold-start entries work. Industry application and model adoption stay unsaved until Save.
    const textile = await fixture(browser), t = textile.page;
    // Entering Edit alone does not turn the implicit defaults into a saved configuration.
    await t.locator('#dimensions-edit').click(); await t.locator('#dimensions-templates').click();
    await t.locator('[data-template-id="textile-chain"]').click();
    await t.locator('#dimensions-template-dialog').waitFor({state:'hidden'});
    assert.equal(await t.locator('.dimensions-section').count(),8);
    assert.equal(await t.locator('.dimensions-template-extra').count(),0);
    await t.locator('#dimensions-cancel').click();
    await t.locator('#dimensions-start-infer').click();
    await t.locator('#dimensions-source-controls').waitFor({state:'visible'});
    assert.match(await t.locator('#dimensions-model-title').innerText(),/往期周报/);
    await t.locator('#dimensions-model-close').click();
    await t.locator('#dimensions-start-industry').click();
    await t.locator('[data-template-id="textile-chain"]').click();
    await t.locator('#dimensions-edit-actions').waitFor({state:'visible'});
    assert.equal(await t.locator('.dimensions-section').count(),8);
    assert.match(await t.locator('#dimensions-template-notice').innerText(),/可让模型按维度推荐指标/);
    assert.equal(textile.stored.revision,0); assert.equal(textile.calls.filter(c => c.method === 'PUT').length,0);
    await t.locator('#dimensions-template-suggest').click(); await t.locator('#dimensions-model-run').click();
    await t.locator('#dimensions-model-adopt:not([disabled])').waitFor();
    const suggestionCall = textile.calls.find(c => c.path === '/dimensions/suggestions'), suggestedDraft = suggestionCall.body.dimensions;
    assert.equal(suggestionCall.body.date,date);
    assert.equal(suggestedDraft.length,8); assert.equal(suggestedDraft[1].label,'美棉供给');
    assert.equal(await t.locator('#dimensions-suggestions select').inputValue(),'1');
    await t.locator('#dimensions-model-adopt').click();
    assert.equal(textile.calls.filter(c => c.method === 'PUT').length,0);
    await t.locator('#dimensions-save').click();
    await t.locator('.dimensions-section h2').filter({hasText:'美棉供给'}).waitFor();
    assert.equal(textile.stored.revision,1); assert.equal(textile.stored.dimensions.length,8);
    assert.equal(textile.stored.dimensions[1].sections.flatMap(s => s.items).length,1);
    assert.equal(await t.locator('#dimensions-start').isVisible(),false);
    await textile.close();

    // Matching names retain bindings and ids; unmatched, referenced dimensions remain for explicit deletion.
    const existing = initial(true), named = structuredClone(existing.dimensions[0]); named.id='d-existing';
    const extra = {...structuredClone(named),id:'d-extra',label:'研究员自定义',sections:[{id:'s-extra',label:'手动整理',items:[indicator('kb-2')]}]};
    existing.dimensions=[named,extra];
    const merged = await fixture(browser,existing), m=merged.page;
    await m.locator('#dimensions-edit').click();
    await m.locator('.dimensions-section').first().getByRole('textbox',{name:'环节名称',exact:true}).first().fill('尚未保存的环节');
    await m.locator('#dimensions-templates').click(); await m.locator('[data-template-id="textile-chain"]').click();
    await m.locator('#dimensions-template-dialog').waitFor({state:'hidden'});
    assert.equal(await m.locator('.dimensions-section').count(),9);
    assert.equal(await m.locator('.dimensions-edit-card').count(),2);
    assert.equal(await m.locator('.dimensions-section').first().getAttribute('data-dimension-id'),'d-existing');
    assert.ok((await m.locator('.dimensions-section').first().getByRole('textbox',{name:'环节名称',exact:true}).evaluateAll(inputs => inputs.map(input => input.value))).includes('尚未保存的环节'));
    const outside=m.locator('.dimensions-section').last(); assert.match(await outside.innerText(),/模板外/);
    m.once('dialog',dialog => {assert.match(dialog.message(),/3 条研判依据/); return dialog.dismiss();});
    await outside.getByRole('button',{name:'删除维度',exact:true}).click();
    assert.equal(await m.locator('.dimensions-section').count(),9);
    m.once('dialog',dialog => dialog.accept()); await outside.getByRole('button',{name:'删除维度',exact:true}).click();
    assert.equal(merged.calls.filter(c => c.method === 'PUT').length,0);
    await m.locator('#dimensions-save').click(); await m.locator('#dimensions-edit-actions').waitFor({state:'hidden'});
    assert.deepEqual(merged.calls.find(c => c.method === 'PUT').body.confirm_delete,['d-extra']);
    assert.equal(merged.stored.dimensions[0].sections.flatMap(s => s.items)[0].id,'kb-1');
    await merged.close();

    // An existing saved six-dimension board can exceed twelve during review without losing data.
    const oversized=await fixture(browser,initial(true)), o=oversized.page;
    await o.locator('#dimensions-edit').click(); await o.locator('#dimensions-templates').click();
    await o.locator('[data-template-id="textile-chain"]').click();
    await o.locator('#dimensions-template-dialog').waitFor({state:'hidden'});
    assert.equal(await o.locator('.dimensions-section').count(),13);
    assert.equal(await o.locator('.dimensions-template-extra').count(),5);
    assert.equal(await o.locator('.dimensions-edit-card').count(),1);
    assert.match(await o.locator('#dimensions-template-limits').innerText(),/13[\s\S]*12/);
    await o.locator('#dimensions-save').click();
    assert.equal(oversized.calls.filter(c => c.method === 'PUT').length,0);
    await o.locator('#dimensions-template-suggest').click();
    assert.equal(await o.locator('#dimensions-model-dialog').isVisible(),false);
    o.once('dialog',dialog => dialog.accept());
    await o.locator('.dimensions-section').last().getByRole('button',{name:'删除维度',exact:true}).click();
    assert.equal(await o.locator('#dimensions-template-limits').isVisible(),false);
    await o.locator('#dimensions-save').click(); await o.locator('#dimensions-edit-actions').waitFor({state:'hidden'});
    assert.equal(oversized.stored.dimensions.length,12);
    assert.equal(oversized.stored.dimensions[0].sections.flatMap(s => s.items)[0].id,'kb-1');
    await oversized.close();

    const view = await fixture(browser,initial(true)), q = view.page;
    assert.match(await q.locator('.dimensions-evidence-link').first().innerText(),/支持 2 \/ 反对 1 \/ 未标注 2/);
    await q.locator('#dimensions-edit').click();
    await q.locator('.dimensions-section').first().getByRole('textbox',{name:'维度名称',exact:true}).fill('自定义价格');
    await q.locator('.dimensions-edit-card').dragTo(q.locator('.dimensions-stage-editor').nth(1).locator('.dimensions-grid'));
    assert.equal(await q.locator('.dimensions-stage-editor').first().locator('.dimensions-edit-card').count(),0);
    assert.equal(await q.locator('.dimensions-stage-editor').nth(1).locator('.dimensions-edit-card').count(),1);
    await q.locator('.dimensions-section').first().getByRole('button',{name:'下移',exact:true}).click();
    await q.locator('#dimensions-save').click(); await q.locator('.dimensions-section h2').filter({hasText:'自定义价格'}).waitFor();
    assert.equal(view.stored.dimensions[1].id,'price'); assert.equal(view.stored.dimensions[1].label,'自定义价格');
    assert.equal(view.stored.dimensions[1].sections[1].items[0].id,'kb-1');
    await view.close();

    const deletion = await fixture(browser,initial(true)), r = deletion.page;
    await r.locator('#dimensions-edit').click();
    r.once('dialog', dialog => {assert.match(dialog.message(),/3 条研判依据/);return dialog.dismiss();});
    await r.locator('.dimensions-section').first().getByRole('button',{name:'删除维度',exact:true}).click();
    assert.equal(await r.locator('.dimensions-section').count(),6);
    r.once('dialog', dialog => dialog.accept()); await r.locator('.dimensions-section').first().getByRole('button',{name:'删除维度',exact:true}).click();
    assert.equal(await r.locator('.dimensions-section').count(),5); await r.locator('#dimensions-save').click();
    await r.locator('#dimensions-edit-actions').waitFor({state:'hidden'});
    assert.deepEqual(deletion.calls.find(c => c.method === 'PUT').body.confirm_delete,['price']);
    await r.locator('#dimensions-edit').click(); deletion.setConflict(true); await r.locator('#dimensions-save').click();
    await r.locator('#dimensions-status').filter({hasText:'请刷新'}).waitFor(); assert.equal(await r.locator('#dimensions-edit-actions').isVisible(),true);
    await deletion.close();

    const unknown = await fixture(browser,{...initial(),revision:1,suggested_dimension:'d-missing'});
    await unknown.page.locator('#dimensions-suggest').click(); await unknown.page.locator('#dimensions-model-run').click();
    await unknown.page.locator('#dimensions-suggestions h3').filter({hasText:'待指定维度'}).waitFor();
    assert.equal(await unknown.page.locator('#dimensions-model-adopt').isDisabled(),true);
    assert.equal(await unknown.page.locator('#dimensions-suggestions select').inputValue(),'');
    await unknown.page.locator('#dimensions-suggestions select').selectOption('0');
    assert.equal(await unknown.page.locator('#dimensions-model-adopt').isDisabled(),false); await unknown.close();
    const readonly = await fixture(browser,{...initial(true),can_write:false,evidence_available:false});
    assert.equal(await readonly.page.locator('#dimensions-edit').isVisible(),false); assert.equal(await readonly.page.locator('#dimensions-suggest').isVisible(),false);
    assert.equal(await readonly.page.locator('#dimensions-start').isVisible(),false); assert.equal(await readonly.page.locator('#dimensions-templates').isVisible(),false);
    assert.equal(await readonly.page.locator('.dimensions-evidence-link').count(),0);
    assert.match(await readonly.page.locator('.dimensions-heading').first().innerText(),/研判依据统计未授权/);
    assert.equal(await readonly.page.locator('.dimensions-card').count(),1); await readonly.close();
    const referencedConfig = {...initial(true), judgment_id:'42'};
    referencedConfig.dimensions[0].sections[0].items[0].judgment_references = [
      {anchor:'judgment-evidence-bull0',label:'多方依据'},
      {anchor:'judgment-evidence-bear0',label:'空方依据'},
      {anchor:'judgment-threshold-watch',label:'阈值 <30'},
      {anchor:'judgment-falsifier-invalidate',label:'证伪 >50·连续2期'},
      {anchor:'judgment-threshold-watch',label:'待核对 2'}
    ];
    referencedConfig.dimensions[0].sections[1].items.push(indicator('kb-2'));
    const referenced = await fixture(browser,referencedConfig,{},'/sn/dimensions?judgment_id=42#dimension-indicator-kb-1');
    const references = referenced.page.locator('#dimension-indicator-kb-1 .dimensions-judgment-references');
    assert.equal(await references.locator('span').innerText(),'研判引用');
    assert.deepEqual(await references.locator('a').allTextContents(),['多方依据','空方依据','阈值 <30','证伪 >50·连续2期','待核对 2']);
    assert.deepEqual(await references.locator('a').evaluateAll(links => links.map(link => link.getAttribute('href'))),
      ['judgment-evidence-bull0','judgment-evidence-bear0','judgment-threshold-watch','judgment-falsifier-invalidate','judgment-threshold-watch']
        .map(anchor => prefix + '/sn/judgment?judgment_id=42&date=' + date + '#' + anchor));
    assert.equal(await referenced.page.locator('#dimension-indicator-kb-2 .dimensions-judgment-references').count(),0);
    assert.equal(referenced.calls.find(call => call.path === '/dimensions/data').url.searchParams.get('judgment_id'),'42');
    await referenced.close();
    const restrictedConfig = {...initial(true),can_write:false};
    restrictedConfig.dimensions[0].sections[1].items.push({kind:'chart',id:'chart-1',revision:3});
    const restricted = await fixture(browser,restrictedConfig,{restricted:true,config:restrictedConfig,data:{...observations(restrictedConfig),period:'daily'}});
    assert.equal(await restricted.page.locator('#dimensions-date').isDisabled(),true);
    assert.equal(await restricted.page.locator('#dimensions-period').inputValue(),'daily');
    assert.equal(await restricted.page.locator('.dimensions-card a, a.dimensions-card').count(),0);
    assert.equal(await restricted.page.locator('[data-chart-view="cycle"]').count(),0);
    assert.equal(await restricted.page.locator('[data-metric-trend] [data-chart-axis-date]').count(),2);
    assert.equal(await restricted.page.locator('#dimensions-start').isVisible(),false); assert.equal(await restricted.page.locator('#dimensions-template-suggest').isVisible(),false);
    assert.equal(restricted.calls.length,0); await restricted.close();
    const sharedConfig = {...referencedConfig,can_write:false};
    const shared = await fixture(browser,sharedConfig,{restricted:true,config:sharedConfig,data:{...observations(sharedConfig),period:'daily'}});
    assert.equal(await shared.page.locator('#dimension-indicator-kb-1 .dimensions-judgment-references a').first().getAttribute('href'),prefix + '/sn/judgment#judgment-evidence-bull0');
    assert.equal(shared.calls.length,0); await shared.close();
    console.log('dimensions-ui: passed');
  } finally { await browser.close(); }
})().catch(error => {console.error(error);process.exitCode=1;});
