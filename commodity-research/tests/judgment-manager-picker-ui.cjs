/* Real Jinja and browser; every API response is synthetic and intercepted.
   Choosing a binding keeps the reviewer's place, and "更多指标" works with one click. */
const path=require('node:path'),assert=require('node:assert/strict');
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
const prefix='/research/workbenches/synthetic',job='synthetic-picker-job-0001';
const html='<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><style>:root{--surface:#fff;--surface-soft:#f6f6f6;--text:#222;--text-muted:#666;--line:#ddd;--primary:#a63f25}body{font-family:Arial,sans-serif}</style><link rel="stylesheet" href="/static/judgment-manager.css"></head><body><button data-open-logic>管理研究逻辑</button>'+rendered.stdout+'<script>window.RESEARCH_PREFIX='+JSON.stringify(prefix)+';window.RESEARCH_CONTEXT={workbench:"synthetic"};window.researchFetch=(url,options)=>fetch(url,options)</script><script src="/static/judgment-manager.js"></script></body></html>';
const clone=value=>JSON.parse(JSON.stringify(value));
const directory=Array.from({length:45},(_,index)=>({series_id:'synthetic-'+index,name:'合成指标 '+index,unit:'吨',source_file:'synthetic.xlsx',sheet:'表'+index}));
const pending=index=>({id:'item-'+index,kind:'binding',title:'待关联研究依据 '+index,description:'选择真实来源后保留依据。'.repeat(3),original_text:'合成研究原文 '+index,status:'pending',requires_attention:true,attention_fields:['binding'],warnings:['请选择数据来源。'],can_bind:true,can_confirm:true,binding:null});
let review={draft_id:'synthetic-picker-preview',revision:1,total:30,sections:[{id:"contradiction",title:"主矛盾",items:Array.from({length:30},(_,index)=>pending(index))}]};
const preview=()=>({draft_id:review.draft_id,base_version:0,source_text:'合成研究原文',variety:'ZZ',model_id:'synthetic-model',markdown:'# 合成研究',diff:{markdown:'',fields:[]},bindings:[],unresolved:[],review_available:true,review:clone(review)});
(async()=>{
  const browser=await chromium.launch({headless:true,args:['--no-sandbox','--disable-gpu','--renderer-process-limit=1'],...(process.env.SHEET_REVIEW_BROWSER?{executablePath:process.env.SHEET_REVIEW_BROWSER}:{})});
  try{
    const page=await browser.newPage({viewport:{width:1280,height:900}}),updates=[],catalog=[],errors=[];let hold=null,release=null;
    page.on('pageerror',error=>errors.push(error.message));
    await page.route('**/*',async route=>{
      const request=route.request(),url=new URL(request.url()),p=url.pathname;let data;
      if(p==='/')return route.fulfill({contentType:'text/html; charset=utf-8',body:html});
      if(p.startsWith('/static/'))return route.fulfill({path:path.join(web,'static',p.slice(8))});
      if(p.endsWith('/manage'))data={variety:'ZZ',current:null,latest:{version:0},versions:[],can_write:true};
      else if(p.endsWith('/models'))data={models:[{id:'synthetic-model',name:'合成分析模型'}]};
      else if(p.endsWith('/panels'))data={items:[],total:0,has_more:false};
      else if(p.endsWith('/jobs/latest'))data={job:null};
      else if(p.endsWith('/jobs')&&request.method()==='POST')data={id:job,state:'queued',purpose:'judgments',workbench_id:'synthetic'};
      else if(p.endsWith('/jobs/'+job))data={id:job,state:'succeeded',purpose:'judgments',workbench_id:'synthetic',result_available:true};
      else if(p.endsWith('/jobs/'+job+'/result'))data=preview();
      else if(p.endsWith('/review/indicators')){
        const offset=Number(url.searchParams.get('offset')),limit=Number(url.searchParams.get('limit'));catalog.push(offset);
        if(hold&&offset>0)await hold;else await new Promise(resolve=>setTimeout(resolve,60));
        data={items:directory.slice(offset,offset+limit),total:directory.length,next_offset:offset+limit<directory.length?offset+limit:null};
      }else if(p.endsWith('/review/update')){
        const body=request.postDataJSON();updates.push(body);
        const item=review.sections[0].items.find(row=>row.id===body.item_id);
        item.status='confirmed';item.requires_attention=false;item.attention_fields=[];item.warnings=[];
        item.binding=directory.find(row=>row.series_id===body.series_id)||null;
        review.revision++;data=preview();
      }else throw new Error('Unexpected synthetic request '+request.method()+' '+p);
      return route.fulfill({json:data});
    });
    await page.goto('http://picker.fixture/');await page.locator('[data-open-logic]').click();
    await page.locator('[data-logic-tab="analyze"]').click();await page.locator('#logic-input').fill('合成研究原文');
    await page.locator('#logic-model').selectOption('synthetic-model');await page.locator('#logic-generate').click();
    await page.locator('#logic-review').waitFor({state:'visible'});
    const card=page.locator('article[data-review-item="item-9"]'),search=card.locator('.logic-binding-search');
    await search.scrollIntoViewIfNeeded();
    const place=()=>card.evaluate(node=>{
      let box=node.parentElement;while(box&&!(box.scrollHeight>box.clientHeight&&/(auto|scroll)/.test(getComputedStyle(box).overflowY)))box=box.parentElement;
      return {scroller:box?box.className||box.id||box.tagName:'none',scrollTop:box?box.scrollTop:0,top:Math.round(node.getBoundingClientRect().top)};
    });
    const before=await place();
    assert.notEqual(before.scroller,'none','The synthetic review is long enough to scroll');
    assert.ok(before.scrollTop>0,'The reviewer has scrolled down to a later item');
    await search.click();
    await card.locator('.logic-binding-option').nth(20).waitFor({state:'visible'});
    const more=card.locator('.logic-binding-more');
    assert.equal(await more.isVisible(),true,'A second page of the directory is offered');
    await more.click();
    await card.locator('.logic-binding-option').nth(40).waitFor({state:'attached'});
    await page.waitForTimeout(400);
    assert.equal(await card.locator('.logic-binding-menu').isVisible(),true,'One click on 更多指标 keeps the list open');
    assert.equal(await card.locator('.logic-binding-option').count(),41,'The next page is appended to the first');
    assert.deepEqual(catalog,[0,20]);
    // Pressing the list's own feedback text or gaps does not blur the search box and close the list.
    await card.locator('.logic-binding-feedback').click();await page.waitForTimeout(300);
    assert.equal(await card.locator('.logic-binding-menu').isVisible(),true,'Pressing inside the open list keeps it open');
    // Leaving and re-entering the box reopens the rows already loaded instead of starting from page one.
    await page.locator('#logic-review-count').click();await page.waitForTimeout(300);
    assert.equal(await card.locator('.logic-binding-menu').isVisible(),false);
    await search.click();await page.waitForTimeout(200);
    assert.equal(await card.locator('.logic-binding-option').count(),41,'Reopening keeps the later page');
    assert.deepEqual(catalog,[0,20],'Reopening the same query does not reload the directory');
    await card.locator('.logic-binding-option').filter({hasText:'合成指标 33'}).first().click();
    await page.waitForFunction(()=>document.querySelector('article[data-review-item="item-9"]')?.dataset.reviewStatus==='confirmed');
    assert.equal(updates.length,1,'One click on an appended row selects it');
    assert.equal(updates[0].series_id,'synthetic-33');
    assert.equal(await search.inputValue(),'合成指标 33');
    const after=await place();
    assert.ok(Math.abs(after.top-before.top)<=2,`The confirmed item stays where the reviewer was (${before.top} -> ${after.top})`);
    assert.equal(await page.evaluate(()=>document.activeElement?.closest?.('article')?.dataset.reviewItem||null),'item-9','Focus stays on the item just confirmed');
    // A press on an option that is still held while the next page arrives is not lost to a list rebuild.
    const next=page.locator('article[data-review-item="item-10"]');
    await next.locator('.logic-binding-search').click();
    await next.locator('.logic-binding-option').nth(20).waitFor({state:'visible'});
    hold=new Promise(resolve=>{release=resolve});
    await next.locator('.logic-binding-more').click();
    const option=next.locator('.logic-binding-option').filter({hasText:'合成指标 2'}).first(),box=await option.boundingBox();
    await page.mouse.move(box.x+box.width/2,box.y+box.height/2);await page.mouse.down();
    release();hold=null;
    await next.locator('.logic-binding-option').nth(40).waitFor({state:'attached'});
    await page.mouse.up();
    await page.waitForFunction(()=>document.querySelector('article[data-review-item="item-10"]')?.dataset.reviewStatus==='confirmed');
    assert.equal(updates.length,2);assert.equal(updates[1].series_id,'synthetic-2');
    // In the pending-only view a confirmed item leaves; the next pending item takes its place without a jump.
    await page.locator('#logic-review-attention').click();
    const third=page.locator('article[data-review-item="item-11"]');
    await third.locator('.logic-binding-search').scrollIntoViewIfNeeded();
    const held=await third.evaluate(node=>Math.round(node.getBoundingClientRect().top));
    const fourth=page.locator('article[data-review-item="item-8"]');
    const above=await fourth.evaluate(node=>Math.round(node.getBoundingClientRect().top));
    await fourth.locator('.logic-binding-search').click();
    await fourth.locator('.logic-binding-option').filter({hasText:'合成指标 4'}).first().click();
    await page.waitForFunction(()=>document.querySelector('article[data-review-item="item-8"]')?.hidden===true);
    const moved=await third.evaluate(node=>Math.round(node.getBoundingClientRect().top));
    assert.ok(Math.abs(moved-above)<=2,`The next pending item takes the place of the one just confirmed (${held}/${above} -> ${moved})`);
    assert.deepEqual(errors,[]);
    console.log('PASS real Chromium: one-click 更多指标, one-click choice from appended rows, scroll position and focus kept after confirmation; synthetic data only.');
  }finally{await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
