const assert=require('node:assert/strict');
const {setup,generateReview,reviewControl}=require('./judgment-manager-ui.cjs');
(async()=>{
 {
  const env=await generateReview({caliberMode:'catalog'});
  assert.equal(reviewControl(env,'anchor','caliber-catalog'),undefined,'Catalog units are adopted without a second confirmation');
  assert.equal(reviewControl(env,'anchor','caliber-unit'),undefined);
  assert.equal(env.requests.filter(r=>r.url.endsWith('/review/update')).length,0,'Reading a preview never fabricates a user confirmation');
  assert.equal(env.requests.filter(r=>r.url.endsWith('/jobs')).length,1);
  env.context.window.JudgmentManager.close();
 }
 {
  const env=await generateReview({caliberMode:'declared'});
  assert.equal(reviewControl(env,'anchor','caliber-declare'),undefined,'Missing source units never force users to type or invent a unit');
  assert.equal(reviewControl(env,'anchor','caliber-basis'),undefined);
  const search=reviewControl(env,'anchor','search');await search.fire('focus');await search.fire('keydown',{key:'ArrowDown'});await search.fire('keydown',{key:'Enter'});
  const body=env.requests.find(r=>r.url.endsWith('/review/update')).body;
  assert.equal(body.series_id,null);assert.equal(body.action,'keep_text');assert.equal(Object.hasOwn(body,'caliber'),false);
  assert.equal(body.remember,false,'Keeping text does not invent a source or remember a unit');env.context.window.JudgmentManager.close();
 }
 {
  const env=setup({lifecycleMode:true});await env.context.window.JudgmentManager.open();
  assert.equal(env.requests.filter(r=>r.url.includes('/versions/')).length,0,'Opening management reads history metadata only');
  env.nodes['logic-version'].value='record:43';await env.nodes['logic-version'].fire('change');
  assert.equal(env.nodes['logic-history-md'].textContent,'# Historical complete MD');assert.equal(env.nodes['logic-current-md'].textContent,'# Current');
  await env.nodes['logic-history-download'].fire('click');assert.equal(await env.downloads[0].text(),'# Historical complete MD');
  await env.nodes['logic-history-reuse'].fire('click');assert.equal(env.nodes['logic-input'].value,'# Historical complete MD');
  assert.equal(env.requests.filter(r=>r.method==='POST'||r.method==='DELETE').length,0,'Export and reuse do not analyze, save or delete');
  env.nodes['logic-input'].value='Unsaved text';await env.nodes['logic-history-reuse'].fire('click');assert.equal(env.nodes['logic-input'].value,'Unsaved text');env.context.window.JudgmentManager.close();
 }
 for(const allowed of[false,true]){
  const env=setup({lifecycleMode:true,confirmDelete:allowed});await env.context.window.JudgmentManager.open();
  await env.nodes['logic-current-delete'].fire('click');const requests=env.requests.filter(r=>r.method==='DELETE');
  assert.equal(requests.length,allowed?1:0);
  if(allowed){assert.deepEqual(requests[0].body,{version:1,revision:1,confirmed:true});const url=new URL(env.navigations[0]);assert.equal(url.searchParams.has('judgment_id'),false);assert.equal(url.searchParams.get('date'),'2024-03-01');}
  env.context.window.JudgmentManager.close();
 }
 {
  const env=setup({lifecycleMode:true,deleteFailure:true});await env.context.window.JudgmentManager.open();await env.nodes['logic-current-delete'].fire('click');
  assert.match(env.nodes['logic-status'].textContent,/报告引用/);assert.equal(env.nodes['logic-current-md'].textContent,'# Current');assert.equal(env.nodes['logic-current-delete'].disabled,false);assert.equal(env.navigations.length,0);env.context.window.JudgmentManager.close();
 }
 {
  const env=setup({lifecycleMode:true,viewer:true});await env.context.window.JudgmentManager.open();assert.equal(env.nodes['logic-current-delete'].disabled,true);await env.nodes['logic-current-delete'].fire('click');assert.equal(env.requests.filter(r=>r.method==='DELETE').length,0);env.context.window.JudgmentManager.close();
 }
 {
  const env=setup({lifecycleMode:true});await env.context.window.JudgmentManager.open();env.nodes['logic-version'].value='record:43';await env.nodes['logic-version'].fire('change');
  await env.nodes['logic-history-delete'].fire('click');const request=env.requests.find(r=>r.method==='DELETE');
  assert.ok(request.url.endsWith('/records/43'));assert.deepEqual(request.body,{version:2,revision:0,confirmed:true});
  assert.equal(env.nodes['logic-current-md'].textContent,'# Current');assert.equal(env.navigations.length,0);
  assert.ok(!env.nodes['logic-version'].children.some(option=>option.value==='record:43'));env.context.window.JudgmentManager.close();
 }
 console.log('8 management/caliber scenarios passed');
})().catch(error=>{console.error(error);process.exitCode=1;});
