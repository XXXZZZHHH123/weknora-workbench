/* Isolated browser acceptance. No running service or business data is used. */
const {assert, render, launch, open, prefix, cycle} = require('./chart-ui-fixture.cjs');
const path = require('node:path'), {spawnSync} = require('node:child_process');
const date = '2029-09-15', unsafe = '<img src=x onerror="window.__xss=1">';
const categories = [{id:'核心标的',label:'价格与价差'},...['供给','需求','库存','平衡','宏观','其他'].map(id => ({id,label:id}))];
function defaults() { return ['price','supply','demand','inventory','balance','macro'].map((id,i) => ({id,label:categories[i].label,category:categories[i].id,description:'合成维度说明',sections:[],references:{support:0,oppose:0,total:0}})); }
const indicator = id => ({kind:'indicator',id,name:id === 'kb-1' ? unsafe : '第二项指标',unit:'吨',frequency:'weekly',source:'合成来源'});
function initial(populated = false) {
  const dimensions = defaults();
  if (populated) {
    dimensions[0].sections = [{id:'s-first',label:'现货',items:[indicator('kb-1')]},{id:'s-second',label:'价差',items:[]}];
    dimensions[0].references = {support:2,oppose:1,total:3};
  }
  return {dimensions,revision:0,can_write:true,categories,evidence:{unassigned:{support:1,oppose:1,total:2},deleted:{support:0,oppose:0,total:0}}};
}
function observations(config) {
  const observed = i => i.kind === 'chart'
    ? {...i,status:'ok',name:'合成图表',definition:{title:'合成图表',time:{mode:'cycle'}},payload:cycle()}
    : {...indicator(i.id),...i,status:'ok',latest:25,date:'2029-09-14',change:1.5,change_pct:.06,direction:'up',baseline:{label:'较 09-07',date:'2029-09-07'},display:{latest:'25',change:'+1.5',change_pct:'+6%'},position:{n:4,rank:4,rank_low:2,text:'近 5 年同期第 2 低'},freshness:{age_days:8,stale:true},points:[{date:'2029-09-07',value:23.5},{date:'2029-09-14',value:25}]};
  const dimensions = config.dimensions.map(d => ({...d,summary:{up:1,down:0,flat:0,extreme:1},sections:d.sections.map(s => ({...s,items:s.items.map(observed)}))}));
  return {as_of:date,latest_data_date:date,period:'weekly',failed:[],warnings:['history_limited'],dimensions};
}
function html(boot = {}) {
  return render('dimensions.html',{}, {dimension_page:{date,...boot}}).replace('</head>','<script defer src="/static/catalog-client.js"></script></head>');
}
async function fixture(browser, config = initial(), boot = {}) {
  let stored = structuredClone(config), conflict = false;
  const view = await open(browser,'/sn/dimensions',html(boot),({path,method,body,reply,url}) => {
    if (path === '/dimensions' && method === 'GET') return reply(stored);
    if (path === '/dimensions' && method === 'PUT') {
      if (conflict) return reply({detail:'配置已更新，请刷新后重试'},409);
      assert.equal(body.revision,stored.revision);
      stored = {...stored,revision:stored.revision + 1,dimensions:body.dimensions.map((d,i) => ({...d,id:d.id || 'd-new0000'+i,references:{support:0,oppose:0,total:0}}))};
      return reply(stored);
    }
    if (path === '/dimensions/data') return reply(observations(stored));
    if (path === '/indicators/catalog') return reply({indicators:[{series_id:'kb-1',name:unsafe,unit:'吨',source:'合成来源'},{series_id:'kb-2',name:'第二项指标',unit:'吨'}],total:2,offset:0,next_offset:null});
    if (path === '/charts') return reply({items:[{id:'chart-1',revision:3,definition:{title:'合成图表',group:'合成分组'}}],total:1,next_offset:null});
    if (path === '/strategies/models') return reply({models:[{id:'model-1',name:'合成模型'}]});
    if (path === '/dimensions/suggestions') return reply({items:[{series_id:'kb-2',dimension:stored.suggested_dimension || stored.dimensions[1].id,section:'生产',reason:unsafe,name:'第二项指标',unit:'吨'}]});
    if (path === '/dimensions/sources') return reply({items:[{title:'合成往期周报',citation:{kind:'knowledge_file',field:'source_quote',source_binding_id:'source-1',knowledge_id:'knowledge-1',file_version:'a'.repeat(64)}}],next_offset:null});
    if (path === '/dimensions/infer') {
      assert.equal(body.sources.length,1); assert.equal(body.sources[0].knowledge_id,'knowledge-1');
      return reply({dimensions:[{id:'d-inferred',label:'产业链',category:'其他',description:'由所选原文归纳',sections:[{id:'s-inferred',label:'加工',items:[]}]}]});
    }
    if (path === '/indicators/kb-1/cycle') return reply(cycle());
    return false;
  });
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
(async () => {
  const browser = await launch();
  try {
    const empty = await fixture(browser), p = empty.page;
    const nav = await p.evaluate(html => [...new DOMParser().parseFromString(html,'text/html').querySelectorAll('.research-nav-links a')].map(a => a.textContent.trim()),navigation(false));
    assert.equal(nav.indexOf('研究维度'),nav.indexOf('指标')+1); assert.equal(nav.indexOf('研究判断'),nav.indexOf('研究维度')+1);
    const restrictedNav = await p.evaluate(html => [...new DOMParser().parseFromString(html,'text/html').querySelectorAll('.research-nav-links a')].map(a => a.textContent.trim()),navigation(true));
    assert.ok(restrictedNav.includes('研究维度')); assert.equal(restrictedNav.includes('市场总览'),false);
    assert.deepEqual(await p.locator('.dimensions-section h2').allTextContents(),categories.slice(0,6).map(c => c.label));
    assert.equal(await p.locator('.dimensions-empty').count(),7);
    assert.match(await p.locator('#dimensions-empty').innerText(),/按本研究台调整/);
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
    assert.match(await p.locator('.dimensions-card').first().innerText(),/25[\s\S]*2029-09-14[\s\S]*较 09-07 \+1.5[\s\S]*近 5 年同期第 2 低[\s\S]*过旧/);
    assert.equal(await p.locator('.dimensions-card h4').first().innerText(),unsafe);
    assert.equal(await p.locator('.dimensions-section img').count(),0);
    assert.equal(await p.evaluate(() => window.__xss),undefined);
    assert.equal(await p.locator('.dimensions-spark').count(),1);
    assert.match(await p.locator('#dimensions-status').innerText(),/历史观测已达到读取上限/);
    assert.equal((await p.locator('#dimensions-status').innerText()).includes('history_limited'),false);
    // Model suggestions create an editable draft and never save automatically.
    const saved = empty.calls.filter(c => c.method === 'PUT').length;
    await p.locator('#dimensions-suggest').click(); await p.locator('#dimensions-model-run').click();
    await p.locator('#dimensions-model-adopt:not([disabled])').waitFor();
    assert.equal(await p.locator('#dimensions-suggestions small').innerText(),unsafe);
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

    const unknown = await fixture(browser,{...initial(),suggested_dimension:'d-missing'});
    await unknown.page.locator('#dimensions-suggest').click(); await unknown.page.locator('#dimensions-model-run').click();
    await unknown.page.locator('#dimensions-suggestions h3').filter({hasText:'待指定维度'}).waitFor();
    assert.equal(await unknown.page.locator('#dimensions-model-adopt').isDisabled(),true);
    assert.equal(await unknown.page.locator('#dimensions-suggestions select').inputValue(),'');
    await unknown.page.locator('#dimensions-suggestions select').selectOption('0');
    assert.equal(await unknown.page.locator('#dimensions-model-adopt').isDisabled(),false); await unknown.close();
    const readonly = await fixture(browser,{...initial(true),can_write:false,evidence_available:false});
    assert.equal(await readonly.page.locator('#dimensions-edit').isVisible(),false); assert.equal(await readonly.page.locator('#dimensions-suggest').isVisible(),false);
    assert.equal(await readonly.page.locator('.dimensions-evidence-link').count(),0);
    assert.match(await readonly.page.locator('.dimensions-heading').first().innerText(),/研判依据统计未授权/);
    assert.equal(await readonly.page.locator('.dimensions-card').count(),1); await readonly.close();
    const restrictedConfig = {...initial(true),can_write:false}, restricted = await fixture(browser,restrictedConfig,{restricted:true,config:restrictedConfig,data:{...observations(restrictedConfig),period:'daily'}});
    assert.equal(await restricted.page.locator('#dimensions-date').isDisabled(),true);
    assert.equal(await restricted.page.locator('#dimensions-period').inputValue(),'daily');
    assert.equal(await restricted.page.locator('.dimensions-card a, a.dimensions-card').count(),0);
    assert.equal(restricted.calls.length,0); await restricted.close();
    console.log('dimensions-ui: passed');
  } finally { await browser.close(); }
})().catch(error => {console.error(error);process.exitCode=1;});
