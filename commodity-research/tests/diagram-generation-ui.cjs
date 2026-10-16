/* Independent browser audit: real diagram Jinja, diagram.js and generation JS. */
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');
const {spawnSync}=require('node:child_process');
const {chromium}=require(process.env.RESEARCH_PLAYWRIGHT || 'playwright');
const web=path.join(__dirname,'..','src','tin','web');
const appScript=fs.readFileSync(path.join(web,'static/app.js'),'utf8');
const drawerScript=appScript.slice(appScript.indexOf('function toggleDrawer('),appScript.indexOf('function openDrawerWith('));
assert(drawerScript.includes('function closeDrawers('));
const render=spawnSync(process.env.RESEARCH_PYTHON||'python',['-c',`
import sys
from types import SimpleNamespace
from jinja2 import Environment,FileSystemLoader,DictLoader,ChoiceLoader,select_autoescape
host='''<!doctype html><html><head><meta charset="utf-8"><style>.hidden,.translate-x-full{display:none}#dg-canvas{width:960px;height:520px}textarea{width:650px;height:90px}button,select,textarea{margin:3px}#dg-md-drawer{padding:16px}</style><script>window.RESEARCH_PREFIX='/research/workbenches/fixture';window.researchFetch=window.fetch.bind(window);window.showToast=()=>{};</script><script src="/fixture-drawers.js"></script></head><body><div id="drawer-backdrop" class="hidden"></div>{% block main %}{% endblock %}</body></html>'''
env=Environment(loader=ChoiceLoader([DictLoader({'base.html':host}),FileSystemLoader(sys.argv[1])]),autoescape=select_autoescape(['html']))
env.globals.update(research_workbench=lambda:SimpleNamespace(metadata=SimpleNamespace(name='合成研究台')),research_identity=lambda:SimpleNamespace(can_write=True),research_static_prefix=lambda:'',static_version=lambda:'fixture')
template={'id':7,'name':'原始布局','layout':{'nodes':[{'id':'original','label':'原有节点','x':100,'y':100,'w':160,'h':60,'binding':{'kind':'none'}}],'edges':[]}}
print(env.get_template('diagram.html').render(template=template,templates_all=[dict(id=7,name='原始布局',is_default=True)],d=None))
`,path.join(web,'templates')],{encoding:'utf8'});
assert.equal(render.status,0,render.stderr);
const originalMarkdown='# 原始布局\n\n## 来源分组\n- 原有节点\n';
const generatedMarkdown='# 合成生成布局\n\n## 产业环节\n- 合成字段 <!-- bind: kb-'+ 'a'.repeat(48)+' -->\n';
const version='b'.repeat(64);
(async()=>{
 const browser=await chromium.launch({headless:true,...(process.env.SHEET_REVIEW_BROWSER?{executablePath:process.env.SHEET_REVIEW_BROWSER}:{})});
 try{
  const page=await browser.newPage({viewport:{width:1500,height:1000}}),errors=[],requests=[];
  page.on('pageerror',error=>errors.push(error.message));
  let failure=false,gate=null,modelLabel='模型 A',catalogGate=null;
  const generated={markdown:generatedMarkdown,version,nodes:2,candidates_truncated:false,
   bindings:[{node:'<img src=x onerror=alert(1)>',name:'真实来源字段',unit:'件',series_id:'kb-'+'a'.repeat(48)}]};
  await page.route('**/*',async route=>{
   const request=route.request(),url=new URL(request.url()),method=request.method();
   requests.push({path:url.pathname,method,body:request.postData()});
   if(url.pathname==='/')return route.fulfill({contentType:'text/html; charset=utf-8',body:render.stdout});
   if(url.pathname==='/fixture-drawers.js')return route.fulfill({contentType:'application/javascript; charset=utf-8',body:drawerScript});
   if(['/static/diagram.js','/static/diagram-generation.js'].includes(url.pathname))return route.fulfill({contentType:'application/javascript; charset=utf-8',body:fs.readFileSync(path.join(web,url.pathname.slice(1)),'utf8')});
   if(url.pathname.endsWith('/diagram/values'))return route.fulfill({json:{nodes:[],as_of:null}});
   if(url.pathname.endsWith('/diagram/markdown/dump'))return route.fulfill({json:{markdown:originalMarkdown}});
   if(url.pathname.endsWith('/diagram/generation/models')){
    const ownLabel=modelLabel,ownGate=catalogGate;if(ownGate)await ownGate;
    return route.fulfill({json:{models:[{id:'fixture-model-a',name:ownLabel,supports_vision:true},{id:'fixture-model-b',name:'模型 B',supports_vision:false}]}});
   }
   if(url.pathname.endsWith('/diagram/generation/sources'))return route.fulfill({json:{sources:[{id:'native-image',name:'真实原件.png',kind:'image'},{id:'native-outline',name:'大纲.md',kind:'text'}]}});
   if(url.pathname.endsWith('/diagram/generation')){
    const ownGate=gate;if(ownGate)await ownGate;
    return route.fulfill({status:failure?503:200,json:failure?{detail:'隔离模型服务暂不可用'}:generated});
   }
   if(url.pathname.endsWith('/diagram/markdown'))return route.fulfill({status:409,json:{detail:'隔离版本检查：请核对来源版本'}});
   throw Error('Unexpected browser request '+method+' '+url.pathname);
  });
  await page.goto('http://diagram-generation.fixture/');
  await page.waitForFunction(()=>typeof dg!=='undefined'&&dg.layout?.__id===7);
  const initialLayout=await page.evaluate(()=>JSON.stringify(dg.layout));
  await page.locator('#dg-md-btn').click();
  await page.waitForFunction(()=>document.querySelector('#dg-ai-model')?.options.length===2);
  assert.match(await page.locator('#dg-ai-model option').first().innerText(),/模型 A.*视觉/);
  assert.equal(await page.locator('#dg-ai-source option').count(),3,'Only actual returned KB originals enter the picker');
  assert.equal(await page.locator('#dg-generate-btn').count(),0,'md管理 is the single entry');
  await page.locator('#dg-ai-model').selectOption('fixture-model-b');
  await page.locator('#dg-ai-source').selectOption('native-image');
  assert.equal(await page.locator('#dg-ai-run').isDisabled(),true,'Text-only models cannot submit image originals');
  assert.match(await page.locator('#dg-ai-status').innerText(),/不支持图片/);
  await page.locator('#dg-ai-model').selectOption('fixture-model-a');
  assert.equal(await page.locator('#dg-ai-run').isEnabled(),true,'A real visual-capability model may submit images');
  await page.locator('#dg-ai-source').selectOption('native-image');
  const png=Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jNXYAAAAASUVORK5CYII=','base64');
  await page.locator('#dg-ai-model').selectOption('fixture-model-a');
  await page.locator('#dg-ai-image').setInputFiles({name:'合成产业图.png',mimeType:'image/png',buffer:png});
  await page.waitForFunction(()=>!document.getElementById('dg-ai-attachment').hidden);
  assert.match(await page.locator('#dg-ai-attachment span').innerText(),/合成产业图.png/);
  assert.equal(await page.locator('#dg-ai-source').inputValue(),'','An attached local image clears a conflicting native image selection');
  await page.locator('#dg-ai-source').selectOption('native-image');
  assert.equal(await page.locator('#dg-ai-attachment').isHidden(),true,'Choosing a native image replaces a conflicting local image');
  assert.equal(await page.locator('#dg-ai-image').inputValue(),'');
  await page.locator('#dg-ai-image').setInputFiles({name:'合成产业图.png',mimeType:'image/png',buffer:png});
  await page.waitForFunction(()=>!document.getElementById('dg-ai-attachment').hidden);
  await page.getByRole('button',{name:'移除图片'}).click();
  assert.equal(await page.locator('#dg-ai-attachment').isHidden(),true);
  assert.equal(await page.locator('#dg-ai-image').inputValue(),'');
  await page.locator('#dg-ai-source').selectOption('native-outline');
  await page.locator('#dg-ai-instructions').fill('保留实际来源，无法绑定的节点留空');
  await page.locator('#dg-ai-model').selectOption('fixture-model-b');
  await page.locator('#dg-ai-run').click();
  await page.waitForFunction(()=>document.getElementById('dg-md-text').value.includes('合成生成布局'));
  const first=requests.find(request=>request.path.endsWith('/diagram/generation')&&request.method==='POST');
  assert.deepEqual(JSON.parse(first.body),{model_id:'fixture-model-b',instructions:'保留实际来源，无法绑定的节点留空',image:null,source_id:'native-outline'});
  assert.equal(await page.evaluate(()=>JSON.stringify(dg.layout)),initialLayout,'Generation fills the existing Markdown editor without changing the actual graph');
  assert.equal(requests.filter(request=>/templates|\/diagram\/markdown$/.test(request.path)).length,0,'Generation never silently saves a layout');
  assert.equal(await page.locator('#dg-ai-bindings img').count(),0,'Binding reports use textContent rather than HTML');
  await page.locator('#dg-ai-bindings > summary').click();
  assert.match(await page.locator('#dg-ai-bindings ul').innerText(),/<img src=x onerror=alert\(1\)>.*真实来源字段/);
  assert.match(await page.locator('#dg-md-msg').innerText(),/尚未保存/);
  await page.getByRole('button',{name:'导入为新布局',exact:true}).click();
  await page.waitForFunction(()=>document.getElementById('dg-md-msg').textContent.includes('隔离版本检查'));
  const imported=requests.find(request=>request.path.endsWith('/diagram/markdown')&&request.method==='POST');
  assert.equal(JSON.parse(imported.body).fact_version,version,'The original dgImportMarkdown carries the actual generation version');
  assert.equal(JSON.parse(imported.body).markdown,generatedMarkdown);
  failure=true;await page.locator('#dg-md-text').fill('# 已有人工内容');await page.locator('#dg-ai-run').click();
  await page.waitForFunction(()=>document.getElementById('dg-ai-status').textContent.includes('暂不可用'));
  assert.equal(await page.locator('#dg-md-text').inputValue(),'# 已有人工内容','Failed generation retains the original edit');
  failure=false;
  async function beginHeld(){let release;gate=new Promise(resolve=>{release=resolve});const seen=page.waitForRequest(request=>request.method()==='POST'&&request.url().endsWith('/diagram/generation'));await page.locator('#dg-ai-run').click();await seen;return()=>{gate=null;release();};}
  let release=await beginHeld();
  const before=requests.filter(request=>request.method==='POST'&&request.path.endsWith('/diagram/generation')).length;
  await page.locator('#dg-ai-run').evaluate(button=>button.dispatchEvent(new MouseEvent('click',{bubbles:true})));
  await page.locator('#dg-md-text').fill('# 生成期间的人工修改');release();
  await page.waitForFunction(()=>!document.getElementById('dg-ai-run').disabled);
  assert.equal(await page.locator('#dg-md-text').inputValue(),'# 生成期间的人工修改');
  assert.match(await page.locator('#dg-ai-status').innerText(),/未覆盖/);
  assert.equal(requests.filter(request=>request.method==='POST'&&request.path.endsWith('/diagram/generation')).length,before,'Disabled generate blocks a second concurrent model request');
  release=await beginHeld();await page.locator('#dg-ai-instructions').fill('生成期间改成新的说明');release();
  await page.waitForFunction(()=>!document.getElementById('dg-ai-run').disabled);
  assert.equal(await page.locator('#dg-md-text').inputValue(),'# 生成期间的人工修改');
  assert.match(await page.locator('#dg-ai-status').innerText(),/未覆盖/);
  await page.locator('#dg-md-text').fill(originalMarkdown);
  release=await beginHeld();
  await page.locator('#dg-md-drawer > div').first().locator('button').click();
  await page.locator('#dg-md-btn').click();
  await page.waitForFunction(()=>document.getElementById('dg-md-text').value.startsWith('# 原始布局'));
  release();await page.waitForFunction(()=>!document.getElementById('dg-ai-run').disabled);
  assert.equal(await page.locator('#dg-md-text').inputValue(),originalMarkdown,'A result from the closed drawer cannot overwrite a reopened editing session');
  await page.locator('#dg-md-drawer > div').first().locator('button').click();
  let releaseCatalog;catalogGate=new Promise(resolve=>{releaseCatalog=resolve});modelLabel='过期目录';
  const oldRequest=page.waitForRequest(request=>request.url().endsWith('/generation/models'));
  await page.locator('#dg-md-btn').click();await oldRequest;
  await page.locator('#dg-md-drawer > div').first().locator('button').click();
  catalogGate=null;modelLabel='当前目录';await page.locator('#dg-md-btn').click();
  await page.waitForFunction(()=>document.querySelector('#dg-ai-model option')?.textContent.includes('当前目录'));
  releaseCatalog();await page.waitForLoadState('networkidle');
  assert.match(await page.locator('#dg-ai-model option').first().innerText(),/当前目录/,'A closed drawer catalog request cannot replace the latest picker');
  await page.locator('#dg-md-text').fill(originalMarkdown);
  const releaseOld=await beginHeld();
  await page.locator('#dg-md-drawer > div').first().locator('button').click();
  await page.locator('#dg-md-btn').click();
  await page.waitForFunction(()=>!document.getElementById('dg-ai-run').disabled);
  const releaseNew=await beginHeld();releaseOld();
  await page.waitForTimeout(30);
  assert.equal(await page.locator('#dg-ai-run').isDisabled(),true,'The old request finally handler cannot unlock a new running request');
  releaseNew();await page.waitForFunction(()=>!document.getElementById('dg-ai-run').disabled);
  await page.locator('#dg-md-text').fill(originalMarkdown);
  release=await beginHeld();await page.evaluate(()=>closeDrawers());release();
  await page.waitForLoadState('networkidle');
  assert.equal(await page.locator('#dg-md-text').inputValue(),originalMarkdown,'The actual shared backdrop-close path cancels late generation');
  await page.locator('#dg-md-btn').click();await page.waitForFunction(()=>!document.getElementById('dg-ai-run').disabled);
  await page.locator('#dg-md-text').fill(originalMarkdown);
  release=await beginHeld();await page.keyboard.press('Escape');release();await page.waitForLoadState('networkidle');
  assert.equal(await page.locator('#dg-md-text').inputValue(),originalMarkdown,'Escape cannot leave a late generation able to write');
  await page.locator('#dg-ai-model').selectOption('fixture-model-a');
  await page.locator('#dg-ai-image').setInputFiles({name:'合成产业图.png',mimeType:'image/png',buffer:png});
  await page.waitForFunction(()=>!document.getElementById('dg-ai-attachment').hidden);
  release=await beginHeld();await page.getByRole('button',{name:'移除图片'}).click();release();await page.waitForLoadState('networkidle');
  assert.equal(await page.locator('#dg-ai-run').isEnabled(),true,'Removing an image cancels and resets its running operation');
  assert.equal(await page.locator('#dg-md-text').inputValue(),originalMarkdown,'Removing an in-flight image rejects that stale result');
  assert.equal(await page.evaluate(()=>JSON.stringify(dg.layout)),initialLayout);
  assert.deepEqual(errors,[]);
  console.log('PASS: actual diagram Jinja/diagram.js/generator, models and real originals, image/remove, no-save draft, text-only binding report, original import version, failure preservation, duplicate-click suppression, edits during generation, closed/reopened result and catalog isolation; no cloud model calls.');
 }finally{await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
