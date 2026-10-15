/* Real Jinja fragment and shipped browser controller; every request is a fixture. */
const fs = require('node:fs'), path = require('node:path'), assert = require('node:assert/strict');
const { spawnSync } = require('node:child_process');
const { chromium } = require('playwright');
const source = path.join(__dirname, '..', 'src', 'tin', 'web');
const python = process.env.RESEARCH_PYTHON || 'python';
const render = spawnSync(python, ['-c', `
import sys
from types import SimpleNamespace
from jinja2 import Environment, FileSystemLoader, select_autoescape
scope=SimpleNamespace(knowledge_base_id='fixture-kb')
env=Environment(loader=FileSystemLoader(sys.argv[1]),autoescape=select_autoescape(['html']))
env.globals.update(research_workbench=lambda:scope,research_identity=lambda:SimpleNamespace(can_write=True),research_static_prefix=lambda:'',static_version=lambda:'fixture')
print(env.get_template('_brief_export.html').render(export_kind='brief',export_id=1))
`, path.join(source,'templates')], { encoding:'utf8' });
assert.equal(render.status,0,render.stderr);
const fragment = render.stdout;
(async () => {
 const browser=await chromium.launch({headless:true,...(process.env.SHEET_REVIEW_BROWSER?{executablePath:process.env.SHEET_REVIEW_BROWSER}:{})});
 try {
  const page=await browser.newPage({viewport:{width:1200,height:800}}),errors=[],requests=[];
  page.on('pageerror',error=>errors.push(error.message));
  let folderFailure=false,exportFailure=false,parseFailure=false,wrongScope=false,delay=0,count=0;
  await page.route('**/*',async route=>{
   const request=route.request(),url=new URL(request.url());requests.push({method:request.method(),path:url.pathname,body:request.postData()});
   if(url.pathname==='/')return route.fulfill({contentType:'text/html; charset=utf-8',body:'<!doctype html><head><meta charset="utf-8"><script>window.RESEARCH_PREFIX="/research/workbenches/fixture";window.researchFetch=window.fetch.bind(window);</script></head><body><div style="position:relative;margin:80px 300px;display:flex;justify-content:flex-end">'+fragment+'</div></body>'});
   if(url.pathname==='/static/brief-export.js')return route.fulfill({contentType:'application/javascript',body:fs.readFileSync(path.join(source,'static/brief-export.js'),'utf8')});
   if(url.pathname.endsWith('/brief/export/folders'))return route.fulfill({status:folderFailure?403:200,json:folderFailure?{detail:'没有知识库写入权限'}:{knowledge_base_id:'fixture-kb',folders:[{path:'',name:'知识库根目录',depth:0},{path:'日报',name:'日报',depth:1},{path:'日报/归档',name:'归档',depth:2},{path:'<example>',name:'<example>',depth:1}]}});
   if(url.pathname.endsWith('/brief/1/export')){
    const data=request.postDataJSON();assert.deepEqual(Object.keys(data),['folder_path']);count++;
    if(delay)await new Promise(resolve=>setTimeout(resolve,delay));
    return route.fulfill({status:exportFailure?503:parseFailure?502:200,json:exportFailure?{detail:'原生上传失败，未确认保存'}:parseFailure?{detail:{message:'文件已保存但解析失败',knowledge_id:'real-fixture-id',knowledge_base_id:'fixture-kb',folder_path:data.folder_path}}:{knowledge_id:'real-fixture-id',knowledge_base_id:wrongScope?'foreign-kb':'fixture-kb',folder_path:data.folder_path,parse_status:'pending',reused:count>1}});
   }
   throw Error('Unexpected browser request '+request.method()+' '+url.pathname);
  });
  await page.goto('http://brief-export.fixture/');
  await page.locator('summary').click();await page.locator('[data-export-folder]:enabled').waitFor();
  assert.equal(await page.locator('[data-export-folder]').inputValue(),'','The default destination is the real KB root');
  assert.equal(await page.locator('option').count(),4);
  assert.equal(await page.locator('example').count(),0,'Folder labels are text, never injected markup');
  assert.equal(await page.locator('option').nth(2).innerText(),'　　归档','Directory depth stays visible in a compact native selector');
  await page.locator('[data-export-folder]').selectOption('日报/归档');
  await page.locator('[data-export-submit]').click();await page.locator('[data-export-status] a').waitFor();
  const link=page.locator('[data-export-status] a');
  assert.equal(await link.getAttribute('target'),'_top');assert.match(await link.getAttribute('href'),/knowledge_id=real-fixture-id/);
  assert.match(await link.innerText(),/已保存/);assert(!((await link.innerText()).includes('解析完成')),'Saving must not claim async parse completion');
  assert.equal(JSON.parse(requests.find(request=>request.method==='POST').body).folder_path,'日报/归档');
  await page.locator('summary').click();await page.locator('[data-export-folder]:enabled').waitFor();
  assert.equal(await page.locator('[data-export-folder]').inputValue(),'日报/归档','Reopening keeps a still-valid selected folder');
  await page.locator('[data-export-submit]').click();await page.waitForFunction(()=>document.querySelector('[data-export-status]')?.textContent.includes('已存在'));
  exportFailure=true;await page.locator('summary').click();await page.locator('[data-export-folder]:enabled').waitFor();
  await page.locator('[data-export-submit]').click();await page.waitForFunction(()=>document.querySelector('[data-export-status]')?.textContent.includes('原生上传失败'));
  assert.equal(await page.locator('[data-export-status] a').count(),0,'A failed upload cannot keep an old success link');
  assert.equal(await page.locator('[data-export-submit]').isEnabled(),true,'A failed upload can be retried');
  exportFailure=false;parseFailure=true;await page.locator('[data-export-submit]').click();
  await page.waitForFunction(()=>document.querySelector('[data-export-status]')?.textContent.includes('查看处理详情'));
  assert.match(await page.locator('[data-export-status]').innerText(),/解析失败/);
  assert.match(await page.locator('[data-export-status] a').getAttribute('href'),/knowledge_id=real-fixture-id/);
  parseFailure=false;wrongScope=true;await page.locator('[data-export-submit]').click();
  await page.waitForFunction(()=>document.querySelector('[data-export-status]')?.textContent.includes('范围不匹配'));
  assert.equal(await page.locator('[data-export-status] a').count(),0);
  wrongScope=false;delay=150;
  const before=count;await page.locator('[data-export-submit]').click();
  await page.locator('[data-export-submit]').evaluate(button=>button.dispatchEvent(new MouseEvent('click',{bubbles:true})));
  await page.locator('[data-export-status] a').waitFor();assert.equal(count,before+1,'Repeated clicks during one upload do not send concurrent requests');
  folderFailure=true;await page.locator('summary').click();await page.waitForFunction(()=>document.querySelector('[data-export-status]')?.textContent.includes('没有知识库写入权限'));
  assert.equal(await page.locator('[data-export-submit]').isDisabled(),true);
  assert.deepEqual(errors,[]);
  console.log('PASS: actual Jinja export control, root/default and nested folder selection, safe labels, real document link, repeat reuse, failures/retry, scope mismatch, duplicate-click protection, permission loss, zero model calls.');
 }finally{await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
