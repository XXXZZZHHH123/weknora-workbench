/* Real template, script and CSS; synthetic API only, never a model or saved judgment. */
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');
const {spawnSync}=require('node:child_process');
const {chromium}=require(process.env.RESEARCH_PLAYWRIGHT||'playwright');
const web=path.join(__dirname,'../src/tin/web'),out=path.resolve('.build/judgment-multiple-bindings-20261102');
fs.mkdirSync(out,{recursive:true});
const rendered=spawnSync(process.env.RESEARCH_PYTHON||'python',['-c',`
import sys
from jinja2 import Environment,FileSystemLoader,select_autoescape
env=Environment(loader=FileSystemLoader(sys.argv[1]),autoescape=select_autoescape(['html']))
print(env.get_template('_judgment_manager.html').render(logic_input='',selected_judgment_id=None,j=None))
`,path.join(web,'templates')],{encoding:'utf8'});
assert.equal(rendered.status,0,rendered.stderr);
const html='<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><style>:root{--surface:#fff;--text:#222;--line:#ddd;--primary:#a63f25;--text-muted:#777;--alert:#b60019}@font-face{font-family:FixtureCJK;src:url(/fixture-font.ttf) format("truetype")}body{font-family:FixtureCJK,Arial,sans-serif}</style><link rel="stylesheet" href="/static/judgment-manager.css"></head><body><button data-open-logic>管理研究逻辑</button>'+rendered.stdout+'<script>window.RESEARCH_PREFIX="/research/workbenches/synthetic";window.RESEARCH_CONTEXT={workbench:"synthetic"};window.researchFetch=(url,options)=>fetch(url,options)</script><script src="/static/judgment-manager.js"></script></body></html>';
const fields=[{series_id:'synthetic-stock',name:'合成库存',unit:'吨'},
  {series_id:'synthetic-rate',name:'合成开工率',unit:'%'},
  {series_id:'synthetic-other',name:'合成供给',unit:'万吨'}];
const clone=value=>JSON.parse(JSON.stringify(value));
const makeItem=(id,rule)=>({id,kind:rule?'thresholds':'binding',title:rule?'价格验证条件':'供需验证依据',
  description:rule?'合成条件的计算对象保持独立。':'库存和开工率共同作为研究观察依据。',status:'ready',can_bind:true,
  can_add_binding:true,max_bindings:8,requires_binding:Boolean(rule),binding:fields[0],related_bindings:[],
  ai_binding:fields[0],ai_related_bindings:[],warnings:[],...(rule?{rule:{operator:'>',value_literal:'12',type:'观察位',horizon:'短期'}}:{})});
let review={draft_id:'synthetic-multi-preview',revision:1,total:2,sections:[
  {id:'contradiction',title:'主矛盾',items:[makeItem('research')]},
  {id:'thresholds',title:'阈值条件',items:[makeItem('rule',true)]}]};
let rejectUpdate=false;
let latestMetadata=null;
const preview=()=>({draft_id:review.draft_id,base_version:0,source_text:'合成研究原文',variety:'ZZ',model_id:'synthetic-model',
  markdown:'# 合成研究',diff:{markdown:'',fields:[]},bindings:[],unresolved:[],review_available:true,review:clone(review)});
(async()=>{
  const browser=await chromium.launch({headless:true,args:['--no-sandbox','--disable-gpu','--renderer-process-limit=1'],
    ...(process.env.SHEET_REVIEW_BROWSER?{executablePath:process.env.SHEET_REVIEW_BROWSER}:{})});
  const requests=[],errors=[];
  try{
    const page=await browser.newPage({viewport:{width:1280,height:1000}});
    page.on('pageerror',error=>errors.push(error.message));
    await page.route('**/*',async route=>{
      const request=route.request(),url=new URL(request.url()),p=url.pathname;
      requests.push({path:p,method:request.method(),body:request.postDataJSON?.()});let data;
      if(p==='/')return route.fulfill({contentType:'text/html; charset=utf-8',body:html});
      if(p==='/fixture-font.ttf')return route.fulfill({path:path.resolve('WeKnora/internal/chart/fonts/WorkbenchChartCJK-Regular.ttf')});
      if(p.startsWith('/static/'))return route.fulfill({path:path.join(web,'static',p.slice(8))});
      if(p.endsWith('/manage'))data={variety:'ZZ',current:null,latest:{version:0},versions:[],can_write:true};
      else if(p.endsWith('/models'))data={models:[{id:'synthetic-model',name:'合成分析模型'}]};
      else if(p.endsWith('/panels'))data={items:[],total:0,has_more:false};
      else if(p.endsWith('/jobs/latest'))data={job:latestMetadata};
      else if(p.endsWith('/jobs')&&request.method()==='POST')data={id:'synthetic-multi-job-0001',state:'queued',purpose:'judgments',workbench_id:'synthetic'};
      else if(p.endsWith('/jobs/synthetic-multi-job-0001'))data={id:'synthetic-multi-job-0001',state:'succeeded',purpose:'judgments',workbench_id:'synthetic',result_available:true};
      else if(p.endsWith('/jobs/synthetic-multi-job-0001/result'))data=preview();
      else if(p.endsWith('/review/indicators')){
        const search=url.searchParams.get('search')||'';data={items:fields.filter(row=>!search||row.name.includes(search)),total:3,next_offset:null};
      }else if(p.endsWith('/review/update')){
        if(rejectUpdate)return route.fulfill({status:422,json:{detail:{message:'请核对本条主要指标，已保留当前修改。',attention_fields:['binding']}}});
        const body=request.postDataJSON(),item=review.sections.flatMap(section=>section.items).find(item=>item.id===body.item_id);
        item.status={confirm:'confirmed',keep_text:'kept_text',exclude:'excluded'}[body.action];
        if(Object.hasOwn(body,'series_id'))item.binding=body.series_id?fields.find(row=>row.series_id===body.series_id):null;
        if(Object.hasOwn(body,'related_series_ids'))item.related_bindings=body.related_series_ids.map(identifier=>fields.find(row=>row.series_id===identifier));
        if(body.caliber){item.caliber={...item.caliber,...body.caliber,confirmed:true};if(body.caliber.unit)item.binding={...item.binding,unit:body.caliber.unit};}
        item.bindings=[item.binding,...item.related_bindings].filter(Boolean);review.revision++;data=preview();
      }else throw new Error('Unexpected synthetic request '+request.method()+' '+p);
      return route.fulfill({json:data});
    });
    await page.goto('http://multiple-bindings.fixture/');await page.locator('[data-open-logic]').click();
    await page.locator('[data-logic-tab="analyze"]').click();await page.locator('#logic-input').fill('合成研究原文');
    await page.locator('#logic-model').selectOption('synthetic-model');await page.locator('#logic-generate').click();
    const card=page.locator('article[data-review-item="research"]'),rule=page.locator('article[data-review-item="rule"]');
    await card.waitFor({state:'visible'});await page.evaluate(()=>document.fonts.ready);
    assert.equal(await card.locator('.logic-binding-search').count(),1);
    const updateCount=()=>requests.filter(row=>row.path.endsWith('/review/update')).length;
    const before=updateCount();await card.getByRole('button',{name:'新增绑定指标',exact:true}).click();
    assert.equal(await card.locator('.logic-binding-search').count(),2);assert.equal(updateCount(),before);
    assert.equal(await page.locator('#logic-apply').isDisabled(),false,'An empty extra row never blocks saving');
    await card.locator('.logic-binding-search').nth(1).click();
    assert.equal(await card.locator('.logic-binding-row').nth(1).locator('.logic-binding-option').first().innerText(),'暂无，暂不关联');
    await card.locator('.logic-binding-row').nth(1).getByRole('option').filter({hasText:'合成开工率'}).click();
    await page.waitForFunction(()=>document.querySelector('article[data-review-item="research"]')?.dataset.reviewStatus==='confirmed');
    assert.deepEqual(await card.locator('.logic-binding-search').evaluateAll(nodes=>nodes.map(node=>node.value)),['合成库存','合成开工率']);
    assert.equal(await page.locator('.logic-review-card').count(),2,'Research entries remain grouped');
    const saved=requests.filter(row=>row.path.endsWith('/review/update')).at(-1).body;
    assert.equal(saved.series_id,fields[0].series_id);assert.deepEqual(saved.related_series_ids,[fields[1].series_id]);
    await page.setViewportSize({width:390,height:900});
    assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true);
    assert.equal(await card.evaluate(node=>node.getBoundingClientRect().right<=innerWidth),true);
    await page.screenshot({path:path.join(out,'multiple-bindings-390.png'),fullPage:true});
    await card.locator('[data-review-control="remove-binding"]').click();
    await page.waitForFunction(()=>document.querySelectorAll('article[data-review-item="research"] .logic-binding-search').length===1);
    assert.equal(requests.filter(row=>row.path.endsWith('/review/update')).at(-1).body.series_id,fields[0].series_id);
    assert.deepEqual(requests.filter(row=>row.path.endsWith('/review/update')).at(-1).body.related_series_ids,[]);
    await page.locator('#logic-review-nav [data-review-section="thresholds"]').click();
    await rule.getByRole('button',{name:'新增绑定指标',exact:true}).click();
    await rule.locator('.logic-binding-search').nth(1).click();
    await rule.locator('.logic-binding-row').nth(1).getByRole('option').filter({hasText:'合成开工率'}).click();
    await page.waitForFunction(()=>document.querySelector('article[data-review-item="rule"]')?.dataset.reviewStatus==='confirmed');
    assert.match(await rule.locator('.logic-item-condition').innerText(),/高于 12 吨/);
    assert.equal(await rule.locator('.logic-item-label').filter({hasText:'计算指标'}).count(),1);
    rejectUpdate=true;await rule.locator('.logic-binding-search').nth(1).click();
    await rule.locator('.logic-binding-row').nth(1).getByRole('option').filter({hasText:'合成供给'}).click();
    await rule.locator('.logic-field-error').filter({hasText:'请核对本条主要指标'}).waitFor({state:'visible'});
    assert.deepEqual(await rule.locator('.logic-binding-search').evaluateAll(nodes=>nodes.map(node=>node.value)),['合成库存','合成供给']);
    assert.equal(await page.locator('#logic-apply').isDisabled(),true,'Rejected changes stay visible and unsynchronized');
    await page.setViewportSize({width:1280,height:1000});
    await page.screenshot({path:path.join(out,'multiple-bindings-1280.png'),fullPage:true});
    review={draft_id:'synthetic-manual-preview',revision:1,total:1,sections:[{id:'falsifiers',title:'证伪条件',items:[{
      ...makeItem('manual',true),kind:'falsifiers',title:'人工核对的验证条件',status:'pending',requires_attention:true,attention_fields:['condition'],
      binding:{...fields[0],unit:null,frequency:null},rule:{expression:'change > 0',side:'bull',consecutive_periods:2},
      caliber:{catalog_unit:null,model_unit:'吨',catalog_frequency:null,frequency:null,frequency_required:true,can_declare:true,can_declare_frequency:true}
    }]}]};
    latestMetadata={id:'synthetic-latest-result-0001',purpose:'judgments',workbench_id:'synthetic',state:'succeeded',result_available:true};
    await page.reload();await page.locator('[data-open-logic]').click();
    await page.setViewportSize({width:390,height:900});
    for(const tab of ['current','analyze','versions']){
      await page.locator('[data-logic-tab="'+tab+'"]').click();
      const recovery=page.locator('#logic-restore-result');assert.equal(await recovery.isVisible(),true);
      const bounds=await recovery.evaluate(node=>({left:node.getBoundingClientRect().left,right:node.getBoundingClientRect().right,
        parentLeft:document.querySelector('#judgment-manager').getBoundingClientRect().left,parentRight:document.querySelector('#judgment-manager').getBoundingClientRect().right}));
      assert.ok(bounds.left-bounds.parentLeft>=12&&bounds.parentRight-bounds.right>=12,'Restore action has content padding in every tab');
    }
    await page.locator('[data-logic-tab="analyze"]').click();await page.locator('#logic-input').fill('另一份合成研究原文');
    await page.locator('#logic-model').selectOption('synthetic-model');await page.locator('#logic-generate').click();
    const manual=page.locator('article[data-review-item="manual"]');await manual.getByRole('button',{name:'核对并确认整条',exact:true}).click();
    const confirmation=page.locator('#logic-confirm-item-dialog');await confirmation.waitFor({state:'visible'});await page.evaluate(()=>document.fonts.ready);
    assert.equal(await confirmation.locator('[data-review-control="confirm-unit"]').inputValue(),'吨');
    assert.equal(await confirmation.locator('[data-review-control="confirm-frequency"]').inputValue(),'');
    await confirmation.locator('[data-review-control="confirm-frequency"]').selectOption('周');
    await confirmation.getByRole('button',{name:'确认整条',exact:true}).click();
    await confirmation.locator('.logic-field-error').filter({hasText:'请核对本条主要指标'}).waitFor({state:'visible'});
    assert.equal(await confirmation.locator('[data-review-control="confirm-frequency"]').inputValue(),'周','Rejected metadata remains in the same confirmation form');
    await confirmation.getByRole('button',{name:'返回修改',exact:true}).click();
    await manual.getByRole('button',{name:'核对并确认整条',exact:true}).click();await confirmation.waitFor({state:'visible'});
    assert.equal(await confirmation.locator('[data-review-control="confirm-frequency"]').inputValue(),'周');
    await page.screenshot({path:path.join(out,'item-confirmation-390.png'),fullPage:true});
    rejectUpdate=false;await confirmation.getByRole('button',{name:'确认整条',exact:true}).click();
    await confirmation.waitFor({state:'detached'});assert.equal(await manual.getAttribute('data-review-status'),'confirmed');
    const confirmed=requests.filter(row=>row.path.endsWith('/review/update')).at(-1).body;
    assert.equal(confirmed.caliber.mode,'declared');assert.equal(confirmed.caliber.unit,'吨');assert.equal(confirmed.caliber.frequency,'周');
    assert.equal(requests.filter(row=>row.path.endsWith('/apply')).length,0);
    assert.equal(requests.filter(row=>row.path.includes('/indicator-values')||row.path.includes('/radar')).length,0);
    assert.deepEqual(errors,[]);
    fs.writeFileSync(path.join(out,'browser-result.json'),JSON.stringify({passed:true,errors,model_calls:0,real_business_writes:0,
      source_sha256:Object.fromEntries(['static/judgment-manager.js','static/judgment-manager.css','templates/_judgment_manager.html'].map(name=>[name,require('node:crypto').createHash('sha256').update(fs.readFileSync(path.join(web,name))).digest('hex')])),
      cases:['default-one and explicit add','grouped search/empty-first/save','390px layout','field-local remove','primary rule operand unchanged','rejected update retains group','shared restore action padding in three tabs','one manual unit/frequency confirmation with rejected-edit retention']},null,2));
    console.log('PASS real Chromium: multiple bindings, 390px, independent rule operand, explicit whole-item metadata confirmation and restore padding across three tabs; no model or business writes.');
  }finally{await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
