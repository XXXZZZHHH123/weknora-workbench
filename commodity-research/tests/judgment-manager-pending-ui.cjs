/* Real Jinja and browser; every API response is synthetic and intercepted. */
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');
const {spawnSync}=require('node:child_process');
const {chromium}=require(process.env.RESEARCH_PLAYWRIGHT||'playwright');
const web=path.join(__dirname,'../src/tin/web');
const rendered=spawnSync(process.env.RESEARCH_PYTHON||'python',['-c',`
import sys
from jinja2 import Environment,FileSystemLoader,select_autoescape
env=Environment(loader=FileSystemLoader(sys.argv[1]),autoescape=select_autoescape(['html']))
print(env.get_template('_judgment_manager.html').render(logic_input='',selected_judgment_id=None,j=None))
`,path.join(web,'templates')],{encoding:'utf8'});
assert.equal(rendered.status,0,rendered.stderr);
const prefix='/research/workbenches/synthetic',job='synthetic-pending-job-0001';
const html='<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><style>:root{--surface:#fff;--text:#222;--line:#ddd;--primary:#a63f25}body{font-family:Arial,sans-serif}</style><link rel="stylesheet" href="/static/judgment-manager.css"></head><body><button data-open-logic>管理研究逻辑</button>'+rendered.stdout+'<script>window.RESEARCH_PREFIX='+JSON.stringify(prefix)+';window.RESEARCH_CONTEXT={workbench:"synthetic"};window.researchFetch=(url,options)=>fetch(url,options)</script><script src="/static/judgment-manager.js"></script></body></html>';
const clone=value=>JSON.parse(JSON.stringify(value));
const field={series_id:'synthetic-quantity',name:'合成研究数量',unit:'吨'};
let review={draft_id:'synthetic-pending-preview',revision:1,total:5,sections:[
  {id:'contradiction',title:'主矛盾',items:[
    {id:'binding',kind:'binding',title:'待关联研究依据',description:'选择真实来源后保留依据。',original_text:'合成研究原文',status:'pending',requires_attention:true,attention_fields:['binding'],warnings:['请选择数据来源。'],can_bind:true,can_confirm:true,binding:null},
    {id:'ready',kind:'description',title:'已解析研究背景',description:'已通过校验的背景。',status:'ready',can_bind:false,can_confirm:true}]},
  {id:'rules',title:'边界条件',items:[
    {id:'boundary',kind:'falsifiers',title:'待核对研究边界',description:'边界信息尚不完整。',status:'pending',requires_attention:true,attention_fields:['condition'],warnings:['补充数字依据，或仅保留文字。'],can_bind:true,can_confirm:false,binding:field,rule:{expression:'value >= 12000',side:'bull',consecutive_periods:1}},
    {id:'confirmed',kind:'description',title:'已经确认的说明',description:'此前已明确确认。',status:'confirmed',requires_attention:true,attention_fields:['content'],warnings:['过期的确认提示。'],can_bind:false,can_confirm:true},
    {id:'caliber',kind:'thresholds',title:'已经确认的来源口径',description:'来源单位已核对。',status:'confirmed',can_bind:true,can_confirm:true,binding:field,rule:{operator:'>=',value_literal:'12'},caliber:{confirmed:true,can_declare:true,unit:'吨',basis:'合成表头依据',scope_note:'',confirmation_required:false}}]}]};
const preview=()=>({draft_id:review.draft_id,base_version:0,source_text:'合成研究原文',variety:'ZZ',model_id:'synthetic-model',markdown:'# 合成研究',diff:{markdown:'',fields:[]},bindings:[],unresolved:[],review_available:true,review:clone(review)});
(async()=>{
  const browser=await chromium.launch({headless:true,args:['--no-sandbox','--disable-gpu','--renderer-process-limit=1'],...(process.env.SHEET_REVIEW_BROWSER?{executablePath:process.env.SHEET_REVIEW_BROWSER}:{})});
  try{
    const page=await browser.newPage({viewport:{width:1280,height:1000}}),requests=[],errors=[];
    page.on('pageerror',error=>errors.push(error.message));
    await page.route('**/*',async route=>{
      const request=route.request(),url=new URL(request.url()),p=url.pathname;requests.push({path:p,method:request.method()});let data;
      if(p==='/')return route.fulfill({contentType:'text/html; charset=utf-8',body:html});
      if(p.startsWith('/static/'))return route.fulfill({path:path.join(web,'static',p.slice(8))});
      if(p.endsWith('/manage'))data={variety:'ZZ',current:null,latest:{version:0},versions:[],can_write:true};
      else if(p.endsWith('/models'))data={models:[{id:'synthetic-model',name:'合成分析模型'}]};
      else if(p.endsWith('/panels'))data={items:[],total:0,has_more:false};
      else if(p.endsWith('/jobs/latest'))data={job:null};
      else if(p.endsWith('/jobs')&&request.method()==='POST')data={id:job,state:'queued',purpose:'judgments',workbench_id:'synthetic'};
      else if(p.endsWith('/jobs/'+job))data={id:job,state:'succeeded',purpose:'judgments',workbench_id:'synthetic',result_available:true};
      else if(p.endsWith('/jobs/'+job+'/result'))data=preview();
      else if(p.endsWith('/review/indicators'))data={items:[field],total:1,next_offset:null};
      else if(p.endsWith('/review/update')){
        const body=request.postDataJSON(),item=review.sections.flatMap(section=>section.items).find(item=>item.id===body.item_id);
        if(item.id==='boundary'&&body.action==='confirm')return route.fulfill({status:422,json:{detail:{message:'原文没有可核对的12000阈值依据，未采用此量化条件。',attention_fields:['condition']}}});
        item.status={confirm:'confirmed',keep_text:'kept_text',exclude:'excluded'}[body.action];
        if(Object.hasOwn(body,'series_id'))item.binding=body.series_id===null?null:field;
        review.revision++;data=preview();
      }else throw new Error('Unexpected synthetic request '+request.method()+' '+p);
      return route.fulfill({json:data});
    });
    await page.goto('http://pending-group.fixture/');await page.locator('[data-open-logic]').click();
    await page.locator('[data-logic-tab="analyze"]').click();await page.locator('#logic-input').fill('合成研究原文');
    await page.locator('#logic-model').selectOption('synthetic-model');await page.locator('#logic-generate').click();
    const nav=page.locator('#logic-review-nav');
    await page.locator('#logic-review').waitFor({state:'visible'});
    assert.deepEqual(await page.locator('.logic-review-views > button').allTextContents(),['仅看问题','全部内容']);
    assert.equal(await nav.locator('[data-review-section="__pending__"]').count(),0,'The filter is the only pending entry');
    assert.equal(await page.locator('.logic-review-card').count(),5,'Original category cards remain intact');
    await page.locator('#logic-review-attention').click();
    const card=()=>page.locator('article[data-review-item="binding"]');
    await card().locator('.logic-binding-search').click();
    assert.equal(await card().locator('.logic-binding-option').first().innerText(),'暂无，暂不关联');
    await card().locator('.logic-binding-option').filter({hasText:field.name}).click();
    await page.waitForFunction(()=>document.querySelector('#logic-review-nav [data-review-section="contradiction"]').hidden);
    assert.equal(await card().isHidden(),true,'Confirmed content leaves the pending filter');
    assert.equal(await card().locator('[data-invalid="true"]').count(),0,'Confirmed content has no stale red fields');
    assert.equal(await page.locator('article[data-review-item="boundary"]').isVisible(),true);
    await page.locator('#logic-review-all').click();await nav.locator('[data-review-section="contradiction"]').click();
    assert.equal(await card().isVisible(),true,'All content keeps confirmed records in their original category');
    await card().locator('.logic-binding-search').fill('新的来源');
    assert.equal(await card().locator('.logic-binding-search').evaluate(node=>document.activeElement===node),true);
    await card().locator('.logic-binding-search').pressSequentially('继续编辑');
    assert.equal(await card().locator('.logic-binding-search').inputValue(),'新的来源继续编辑');
    await page.locator('#logic-review-attention').click();
    await card().locator('.logic-binding-search').click();await card().locator('.logic-binding-option').filter({hasText:field.name}).click();
    const boundary=page.locator('article[data-review-item="boundary"]');
    await boundary.locator('.logic-binding-search').click();await boundary.locator('.logic-binding-option').filter({hasText:field.name}).click();
    await page.waitForFunction(()=>document.querySelector('article[data-review-item="boundary"] [data-review-field="condition"]').dataset.invalid==='true'&&document.querySelector('#logic-apply').disabled);
    await boundary.locator('[data-review-field="condition"] .logic-field-error').filter({hasText:'原文没有可核对的12000阈值依据'}).waitFor({state:'visible'});
    assert.match(await boundary.locator('[data-review-field="condition"]').innerText(),/12000/,'Automatic source units never bypass a rejected numeric boundary');
    assert.equal(await boundary.locator('[data-review-control="caliber-unit"]').count(),0);
    await boundary.locator('.logic-item-adjustments > summary').click();await boundary.getByRole('button',{name:'仅保留文字',exact:true}).click();
    await page.locator('#logic-review-empty').waitFor({state:'visible'});
    assert.match(await page.locator('#logic-review-empty').innerText(),/暂无需要处理的问题/);
    assert.equal(await page.locator('.logic-review-card:visible').count(),0);
    assert.equal(await page.locator('.logic-review-card [data-invalid="true"]').count(),0);
    await page.locator('#logic-review-all').click();await nav.locator('[data-review-section="rules"]').click();
    const caliber=page.locator('article[data-review-item="caliber"]');
    assert.equal(await caliber.getByRole('button',{name:'确认本条口径',exact:true}).count(),0);
    assert.equal(await caliber.getByRole('button',{name:'保存口径修改',exact:true}).count(),0);
    assert.equal(await caliber.locator('[data-review-control="caliber-unit"]').count(),0);
    assert.equal(await caliber.locator('.logic-binding-meta').innerText(),'采用指标口径 · 吨 · 频率未注明');
    assert.equal(requests.filter(row=>row.path.endsWith('/apply')).length,0);
    assert.equal(requests.filter(row=>row.path.endsWith('/jobs')&&row.method==='POST').length,1);
    assert.deepEqual(errors,[]);
    console.log('PASS real Chromium: one pending filter, original business categories, no repeated red fields, preserved typing focus, automatic selected-source caliber and numeric rejection; no real model or business writes.');
  }finally{await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
