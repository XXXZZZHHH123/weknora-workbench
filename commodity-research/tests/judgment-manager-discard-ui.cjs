/* Real Jinja and browser; every API response is synthetic and intercepted.
   An unsaved background result can be discarded, from the toolbar or while it is under review. */
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
const prefix='/research/workbenches/synthetic',job='synthetic-discard-job-0001';
const html='<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><style>:root{--surface:#fff;--surface-soft:#f6f6f6;--text:#222;--text-muted:#666;--line:#ddd;--primary:#a63f25}body{font-family:Arial,sans-serif}</style><link rel="stylesheet" href="/static/judgment-manager.css"></head><body><button data-open-logic>管理研究逻辑</button>'+rendered.stdout+'<script>window.RESEARCH_PREFIX='+JSON.stringify(prefix)+';window.RESEARCH_CONTEXT={workbench:"synthetic"};window.researchFetch=(url,options)=>fetch(url,options)</script><script src="/static/judgment-manager.js"></script></body></html>';
const clone=value=>JSON.parse(JSON.stringify(value));
const review={draft_id:'synthetic-discard-preview',revision:1,total:1,sections:[{id:'contradiction',title:'主矛盾',items:[
  {id:'item-1',kind:'description',title:'已解析研究背景',description:'已通过校验的背景。',status:'ready',can_bind:false,can_confirm:true}]}]};
const preview=()=>({draft_id:review.draft_id,base_version:0,source_text:'合成研究原文',variety:'ZZ',model_id:'synthetic-model',markdown:'# 合成研究',diff:{markdown:'',fields:[]},bindings:[],unresolved:[],review_available:true,review:clone(review)});
(async()=>{
  const browser=await chromium.launch({headless:true,args:['--no-sandbox','--disable-gpu','--renderer-process-limit=1'],...(process.env.SHEET_REVIEW_BROWSER?{executablePath:process.env.SHEET_REVIEW_BROWSER}:{})});
  try{
    const page=await browser.newPage({viewport:{width:1280,height:900}}),discards=[],errors=[];let state='succeeded';
    page.on('pageerror',error=>errors.push(error.message));
    const meta=()=>({id:job,state,purpose:'judgments',workbench_id:'synthetic',result_available:state==='succeeded',...(state==='superseded'?{message:'已舍弃暂存结果',discarded:true}:{})});
    await page.route('**/*',async route=>{
      const request=route.request(),url=new URL(request.url()),p=url.pathname;let data;
      if(p==='/')return route.fulfill({contentType:'text/html; charset=utf-8',body:html});
      if(p.startsWith('/static/'))return route.fulfill({path:path.join(web,'static',p.slice(8))});
      if(p.endsWith('/manage'))data={variety:'ZZ',current:null,latest:{version:0},versions:[],can_write:true};
      else if(p.endsWith('/models'))data={models:[{id:'synthetic-model',name:'合成分析模型'}]};
      else if(p.endsWith('/panels'))data={items:[],total:0,has_more:false};
      else if(p.endsWith('/jobs/latest'))data={job:state==='succeeded'?meta():null};
      else if(p.endsWith('/jobs/'+job))data=meta();
      else if(p.endsWith('/jobs/'+job+'/result'))data={...preview(),job:meta()};
      else if(p.endsWith('/jobs/'+job+'/discard')&&request.method()==='POST'){discards.push(p);state='superseded';data={job:meta()};}
      else throw new Error('Unexpected synthetic request '+request.method()+' '+p);
      return route.fulfill({json:data});
    });
    const restore=page.locator('#logic-restore-result'),discard=page.locator('#logic-discard-result');
    await page.goto('http://discard.fixture/');await page.locator('[data-open-logic]').click();
    await discard.waitFor({state:'visible'});
    assert.equal(await restore.isVisible(),true,'Viewing and discarding are offered together');
    page.once('dialog',dialog=>dialog.dismiss());await discard.click();await page.waitForTimeout(300);
    assert.deepEqual(discards,[],'Cancelling the confirmation sends nothing');
    // Open the result, then discard it while it is under review.
    await restore.click();await page.locator('#logic-review').waitFor({state:'visible'});
    assert.equal(await discard.isVisible(),true,'The result under review can still be discarded');
    page.once('dialog',dialog=>dialog.accept());await discard.click();
    await page.locator('#logic-status').filter({hasText:'已舍弃暂存结果'}).waitFor();
    assert.equal(discards.length,1);
    assert.equal(await page.locator('#logic-review').isVisible(),false,'The discarded review is cleared');
    assert.equal(await discard.isVisible(),false);assert.equal(await restore.isVisible(),false);
    // Reopening finds no staged result to offer.
    await page.locator('[data-logic-close]').click();await page.locator('[data-open-logic]').click();await page.waitForTimeout(600);
    assert.equal(await restore.isVisible(),false);assert.equal(await discard.isVisible(),false);
    assert.deepEqual(errors,[]);
    console.log('PASS real Chromium: discard offered beside the staged result and during its review, confirmation required, review cleared; synthetic data only.');
  }finally{await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
